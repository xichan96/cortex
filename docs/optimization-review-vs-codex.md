# Cortex 优化评估报告（对照 Codex）

> 评估日期：2026-08-28
> 范围：仅评估，不涉及代码改动
> 对比对象：[OpenAI Codex CLI](https://github.com/openai/codex)（`codex-rs/`，Rust，生产级编码 agent）

## 背景与方法

Cortex 是 Go 编写的 AI Agent 框架（`agent/` 基础引擎 + `dino/` 高级编排层）。
Codex 是 OpenAI 的官方 CLI 编码 agent，经过大规模生产验证，是架构上的参考实现。

本文通过四个维度的深度调研对比两个项目：

1. **agent 循环 / 上下文 / 压缩 / 流式**（cortex `agent/engine/*` ↔ codex `core/src/session/*`、`core/src/context_manager/*`）
2. **工具 / 权限 / 沙箱**（cortex `dino/tools/*` ↔ codex `core/src/tools/*`、`sandboxing/`）
3. **provider / prompt caching / 重试**（cortex `agent/llm/*` ↔ codex `model-provider/`、`core/src/client.rs`）
4. **harness engineering / 验证 / 评测 / trace**（cortex `dino/harness/*`、`dino/runner/*` ↔ codex `rollout-trace/`、`exec/`、`analytics/`）

**核心结论**：两者的差距不在 agent 循环本身（都有迭代循环、流式、工具并行），而在五个被低估的层面——

- **成本**：cortex 完全没有 prompt caching
- **正确性**：流式丢事件、工具输出二次截断、verifier 误判
- **可观测性**：无 trace / 结构化指标底座
- **维护性**：死代码、双 memory 系统、eventbus 反注册失效
- **未接线的成熟子系统**：`dino/mem`（长期记忆）+ memory 工具已实现但**零调用方**；`nonFatalTool`/`toolResultLimiter` 包装器是死代码；`question`/`codesearch`/`http_request` 工具是 stub

---

## 一、差距最大：Prompt Caching（成本影响最大）

### 现状
- `Usage.CachedTokens` / `ReasoningTokens` 字段已存在（`agent/types/llm.go:45-46`）但**从未填充**。
- 无 Anthropic `cache_control` breakpoint，无 OpenAI 缓存利用。

### Codex 做法
- `prompt_cache_key` 随每个请求发送（`core/src/client.rs:540-552`）。
- WebSocket 增量请求（`core/src/client.rs:1336-1374`）：仅当新请求是上一请求的**严格扩展**（同 model/instructions/tools/reasoning，input 扩展上一 input）时，只发送增量——这是真正的"缓存断点"机制，history 前缀稳定则服务端上下文缓存被保留。
- compaction 刻意保留缓存前缀（`compact.rs:317`），并跟踪 `auto_compact_window` / `prefill_input_tokens` 供 `BodyAfterPrefix` scope 计算。

### 落地要点
cortex 是 HTTP 轮询，最直接收益：
1. 消息数组保持**前缀稳定**（system + 工具定义在前不动，新消息只追加）。
2. Anthropic 侧加 `cache_control` breakpoint。
3. 在 `Usage` 中回填 `cached_input_tokens`，后续可观测成本。

长 agentic 会话成本可显著下降。**工作量：中，收益：成本大幅下降。**

---

## 二、流式事件背压静默丢数据（正确性隐患）

### 现状
- `ExecuteStream` 通道满时 `default: // Channel full, skip event`（`agent/engine/agent_execution.go:731-737`）——流式事件静默消失，消费者拿不到完整 `tool_event`。

### Codex 对照
- 消息传递 session 循环（submission channel + 后台 turn task，`core/src/session/handlers.rs:520-724`），事件从不因"满"而丢，可 mid-turn 打断/注入输入。

### 落地要点
- 满时至少阻塞，或降级为合并事件；绝不该静默丢。
- 这影响所有依赖完整事件流的消费者（UI、日志、approval、trace）。

**工作量：小，收益：正确性。**

---

## 三、Compaction 从"丢最旧"升级为"保留尾部 + 摘要"

### 现状
- `trimHistoryToTokenBudget` 丢最旧（`agent/engine/agent_execution.go:987-1017`）。
- `DeterministicCompact` 靠正则/关键词启发式（`dino/chatstore/compact.go:42-140`），非英语/代码密集场景退化明显。
- token 估算是字符/4 启发式（`agent/types/tokens.go:7-18`），对 CJK 偏差大。

### Codex 做法（`core/src/compact.rs`）
- 本地 compact 算法：**最近用户消息尾部原文（保留到 `COMPACT_USER_MESSAGE_MAX_TOKENS = 20_000`）+ LLM summary**。
- summary 作为**最后一项**注入（`build_compacted_history_with_limit`，`compact.rs:657-733`）。
- 初始上下文在最后一条真实用户消息**前**重注入（`insert_initial_context_before_last_real_user_or_summary`，`compact.rs:586-642`），绝不在 summary 之后。
- summary 以 `SUMMARY_PREFIX` 标记，编码为 user 消息。
- 每个 compaction 推进 `auto_compact_window`，为 prompt cache 前缀记账。

### 落地要点
- dino 已有 LLM 压缩能力，把确定性启发式替换为"尾部原文 + LLM summary"模型。
- CJK token 估算需修正（字节级）。

**工作量：中，收益：长会话质量。**

---

## 四、工具输出截断被二次截断（bug）

### 现状
- `SanitizeToolResult` 递归截断文本、超大输出写文件让模型读（`agent/types/agent.go:334-351`、`agent/types/truncate.go`）。
- **未预留 header/省略标记空间**，history 层可能再次截断已截断的内容。

### Codex 做法
- 独立截断库 `utils/output-truncation`：`truncate_middle` **保留中间**（非头尾）。
- 输出带结构化 header（chunk id、耗时、exit code、原始 token 数）。
- 预留 header/omission 标记字节，避免 history 二次截断（`core/src/tools/context.rs:511-536`）。

**工作量：小，收益：正确性。**

---

## 五、两套 memory 系统合并

### 现状
- `agent/providers/*`（LLM 增量摘要，如 `sqlite_memory.go:266-371`）与 `dino/chatstore/*`（确定性 compact）并存，压缩/删除语义不一致（一个删旧行一个不删）。
- dino 实际用的是 chatstore（`dino/factory.go:79-211` 有 `memoryAdapter`），`agent/providers` 基本是遗留。
- 另外 `initTable()` 每次 `AddMessage`/`GetMessages` 都调 `AutoMigrate`（`sqlite_memory.go:85-95`），热路径开销。

### 落地要点
- 收敛到一套（保留 chatstore），标注 `agent/providers` 为 deprecated 或移除。
- `AutoMigrate` 移到初始化阶段。

**工作量：中，收益：一致性。**

---

## 六、删除 `langchain_engine.go` 死代码

### 现状
- `agent/engine/langchain_engine.go` 是第二个不完整的 engine：`SetRateLimiter` / `SetToolCallback` / `SetHooks` / `SetMemory` / output parser 全是 silent no-op（`langchain_engine.go:359-372`、`399-407`）。
- `Execute` 忽略 `previousRequests`。
- 留着会误导调用方以为功能可用。

**工作量：小，收益：维护性。**

---

## 七、事件总线反注册失效

### 现状
- `pkg/eventbus/eventbus.go:643-648` 的 `isSameHandler` 硬返回 `false`，非泛型 handler 的 `Unslot` 按设计失效。
- 依赖反射指针相等性，无法保证。

### 落地要点
- 要么修（改为 handler 注册时返回 token），要么删掉非泛型 bus。

**工作量：小，收益：维护性。**

---

## 八、MCP stdio 声称支持但未实现

### 现状
- config 广告 `"stdio"` 传输（`dino/config.go:89-99`），但 `pkg/mcp/client.go:63-74` 只实现 http / httpStreamable / sse。
- 本地 stdio MCP 服务器会报 `EC_MCP_UNSUPPORTED_TRANSPORT`。

### 落地要点
- 要么实现 stdio 传输，要么从配置/文档中移除该类型。

**工作量：小，收益：可用性/诚实性。**

---

## 九、沙箱 / 权限下沉到 engine

### 现状
- 权限/审批只在 dino 包装层（`dino/factory.go:550-591`），裸 `agent/engine` 无权限门。
- shell 直接 `sh -c`（`dino/verify/shell.go:20`），无沙箱。

### Codex 做法
- **Orchestrator 模式**（`core/src/tools/orchestrator.rs:125-527`）：每工具统一走 approval → 选 sandbox → attempt → sandbox 被拒后**重新审批**再无沙箱重试（approval caching 避免重复询问）。
- 审批优先级：hooks → Guardian → 用户（`core/src/tools/approvals.rs:485-557`）。
- 网络审批 + MITM 代理（`NetworkApprovalService`）。

### 落地要点
- 至少把权限门下沉为 engine 的一个可选 hook，让裸 engine 也能受限。

---

## 十、Harness engineering（重点板块）

### 事实澄清：codex 仓库内也没有 eval 框架

codex 仓库里**没有 eval 数据集、没有 golden 测试、没有批量评测**。经典 codex eval 方案（准备 repo + 任务列表 → 跑 agent → 查 git diff / 跑测试打分）位于**外部私有仓库**。

codex 真正强的是支撑 eval 的**基础设施**。cortex 恰好相反：**有验证器 + 检查点，但没有 trace / 指标底座**。两者互补，合并起来才是完整方案。

### Cortex 现有强项（不应丢弃）

- **可插拔 verifier**：`Verifier` 接口 + `CompositeVerifier` / `TextMustContainVerifier` / `AndVerifier` / `LLMVerifier` 可组合（`dino/runner/verifier.go`）。
- **检查点 / 恢复**：blob store 检查点 + 版本剪枝（`dino/runner/engine.go:152-188`、`blob_session_store.go`），有 `TestCheckpointResumeOuterIteration` 验证。
- **通用 outer loop**：`RunOuterLoop[S any]` 泛型化、verifier/stall/completion 全部注入式（`dino/harness/outer_loop.go:34-97`）。

### 10.1 补齐 trace 记录 + 离线回放（缺失最大的一块）

Codex 的 `rollout-trace` crate 是可复制的范本，关键设计均语言无关：

- **原始事件日志** `trace.jsonl` + **重 payload** 存 `payloads/*.json`，且**先写 payload 后写引用事件**（`rollout-trace/src/writer.rs:97-100`）——崩溃后回放不会指向缺失文件。
- **单信封**：每个事件带 `schema_version` / `seq` / `wall_time_unix_ms` / `rollout_id` / `thread_id` / `codex_turn_id`（`rollout-trace/src/raw_event.rs:33-43`），部分回放安全。
- **No-op 可用的上下文句柄**：`Disabled` / `Enabled` 枚举（`rollout-trace/src/thread.rs:76-91`），热路径不分支，不开 trace 零开销。
- **原始证据 ↔ 语义图分离**：reducer 把事件还原成"模型实际看到了什么"——conversation item（含 `produced_by` 溯源）、tool call（含 execution window / requester / kind）、compaction checkpoint、token usage。

**工作量：大，收益：eval 底座 + 事故定位。**

### 10.2 结构化指标：token + 计时分相

- **现状**：cortex 有 `FileSnapshot` 和消息持久化，但**没有逐 step 的 token / 耗时账本**。
- **Codex 做法**：
  - 每个 `InferenceCall` 记录 `input / cached_input / cache_write_input / output / reasoning_output` tokens（`rollout-trace/src/model/conversation.rs:161-193`）。
  - `TurnProfile` 把墙钟时间归到 sampling / tool-blocking / compaction / overhead 各相（`core/src/turn_timing.rs:305-351`），RAII guard 记账。
  - `ToolCallTimingGuard` 区分 dispatch（排队等待）vs handler（实际执行）耗时（`core/src/tools/parallel.rs:303-340`）。

**价值**：这是效率 eval 的前提——"时间花在哪"、"每次推理多少 token"。

**工作量：中，收益：效率 eval。**

### 10.3 无头运行契约

- Codex `exec` 保证 stdout 只承载最终答案 / JSONL 事件，其余全走 stderr（`codex-rs/exec/src/lib.rs:1-5`）。
- 没有这个纪律，任何外部 harness 都解析不了 agent 输出。
- cortex 的 HTTP trigger 已有类似 JSON 输出，但建议明确契约（error/日志不混入 stdout 数据通道）。

### 10.4 正确性校验：LLM verifier 接入默认链

- **现状**：默认验证链 = 「退出码 0 + 子串匹配」（`dino/runner/verifier.go:49`），**误判风险**——任务"成功"但代码是错的。
- **落地要点**：`LLMVerifier` 已存在（`verifier_llm.go:14-55`），接入默认链只需一行（`dino/runner/engine.go:234-239` 处有注释标记）。
- 这是 harness 板块**性价比最高**的一步。

### 10.5 stall 检测升级为相似度

- `StallAccumulate` 只做字节级指纹（`dino/harness/stall.go:42-48`），`SimilarityThreshold` 配置存在但未使用。
- 加模糊相似度（如编辑距离/最小相似度阈值）可降低误报。

### 10.6 批量评测跑批（远期）

Codex 的支撑设施：
- `agent-graph-store`：线程父子拓扑持久化（`upsert_thread_spawn_edge` / BFS descendants），支撑多 agent 运行图评估。
- SQLite session / thread 索引（`rollout/src/state_db.rs`），queryable 历史 → 批量 eval 结果表。
- `codex debug trace-reduce` CLI（`cli/src/main.rs:2181`）：离线把原始 trace 还原为语义图，与运行解耦。

Cortex 有了 trace 后，加一个"任务目录 → 跑批 → 聚合通过率"的 runner 即可。

---

## 十一、工具系统

### 11.1 现状总览（cortex）

- **工具定义**：`types.Tool` 接口（`agent/types/tool.go:6-17`）+ `ToolMetadata`；schema 是**手写 Go map 字面量**，没有 JSON-schema 生成库（`agent/tools/schema/schema.go` 只做输入校验，且校验浅——数组/对象一律接受，`schema.go:122-123`）。
- **dino 实际注册 14 个工具**（`dino/factory.go:808-823`）：`read_file`/`write_file`/`edit_file`/`bash`/`glob`/`grep`/`list_directory`/`question`/`job_kill`/`job_output`/`web_fetch`/`web_search`/`todo`/`mcp_client`。
- **未注册的能力缺口**：docker 10 件套、ssh、file、command、math、email、http_request 在代码里存在但 dino 的 `loadBuiltinTools` **从不注册**（`internal/app/tools.go:44-107`）。
- **stub / 半成品**：`question` 权限默认拒绝且无 runner 处理（`dino/permission/permission.go:34`）；`codesearch` 硬编码 "not yet implemented"（`search/codesearch.go:48`）；`http_request` 仅 GET 且从未注册；`web_search` 用手写 SSE 解析器只读第一行 `data:`（`web/websearch.go:182-198`），脆弱。
- **执行流水线**：`executeIteration` → 拓扑排序 + `MaxToolCallsPerIteration` → `runToolCallsByLayer`（errgroup 并行，`agent_execution.go:320-423`）→ 结果 sanitize。**无并发上限**（errgroup 无 limit），存在 N 个 `bash`/MCP 并行无限制风险。
- **包装器只用了 3 个**（`dino/factory.go:548-604`）：路径审批、approval、循环检测。**`nonFatalTool` 和 `toolResultLimiter` 是死代码**（零引用），因此 120KB/60KB 结果上限**未生效**；MCP 调用错误会作为硬错误终止迭代。**ApprovalStore.Respond 通道满时非阻塞丢弃**（`defined_tool.go:314-320`），审批响应可能静默丢失。
- **MCP**：支持 http/httpStreamable/sse，**无 stdio**；allow-list 靠 env 变量 `_goclaw_mcp_allow`；`mcp_client` 逃生舱工具绕过权限包装。
- **Skills**：`agent/skills/loader.go` 扫描 `**/SKILL.md` 全量 eager 加载（无懒加载）；`skill` 工具只返回 skill 内容文本，不执行步骤。

### 11.2 Codex 工具系统值得借鉴的模式

**① 最小 trait + 2 值错误分类**
- `ToolExecutor` 只有 3 个必需方法（`tools/src/tool_executor.rs:106-130`），handle 返回 `Box<dyn ToolOutput>` trait 对象。
- 错误仅两种：`RespondToModel(String)`（可恢复，喂回模型）vs `Fatal(String)`（`tools/src/function_call_error.rs:5-10`）。这使 dispatch 能统一把可恢复错误转给模型、致命错误中止——cortex 应采纳这个二分。

**② Exposure 一等公民（cortex 最缺）**
- `ToolExposure`：`Direct`（初始列表可见）/ `Deferred`（注册可分发但初始列表不可见，靠 tool_search 发现）/ `Hidden`（完全不可见）等（`tool_executor.rs:51-80`）。
- 注册与模型可见性**解耦**：`build_model_visible_specs` 只遍历 `exposure.is_direct()` 的工具（`spec_plan.rs:537-574`）。
- 这解决 cortex 的"注册 = 必须给模型"的二元问题，尤其 MCP 工具多时。

**③ Tool Search / 延迟发现（最独特的模式）**
- 工具可声明 `Deferred` + `search_info()`；模型初始列表里有一个 `tool_search` 工具（BM25 检索，`tool_search.rs:151-158`），调用后返回匹配的 `LoadableToolSpec`，**下一轮才成为真正的工具定义**。
- cortex 可做轻量版：`SearchableTool` 接口 + `tool_search` 工具返回工具名/描述，下一轮注入为可用工具。MCP 工具数量增长后收益大。

**④ apply_patch 模式（edit_file 的改造方向）**
- `apply_patch` 是 **Lark 语法自由文本工具**，不是 JSON schema（`apply_patch_spec.rs:9-28`），模型直接给整个 patch 文本而非 JSON 参数。
- **流式增量解析**：`StreamingPatchParser` 消费参数增量，边解析边发 `PatchApplyUpdatedEvent`（500ms 节流，`apply_patch.rs:86-155`），模型能在调用完成前看到 patch 实时解析进度。
- **写权限从解析出的路径推导**：跳过已有写权限的目录的父目录，避免"可写目录授予父目录访问权"（`apply_patch.rs:236-271`）。

**⑤ 结构化结果头 + 中间截断**
- `ExecCommandToolOutput` 输出结构化 header（Chunk ID / Wall time / Exit code / session ID / Original token count），且**预留 header 字节**避免 history 二次截断（`core/src/tools/context.rs:511-536`）。
- `truncate_middle_with_token_budget` 保留头尾、中间加省略标记（`utils/string/src/truncate.rs:15-36`）。

**⑥ 并行门控 + 取消**
- 并行工具走 `lock.read()`（并发），非并行走 `lock.write()`（独占），用 async RwLock 串行化非并行工具（`parallel.rs:152-156`）。
- 取消：dispatch future 包 `AbortOnDropHandle`，`tokio::select!` 与取消 token 竞速，取消时 abort future 并返回 `aborted_response`（`parallel.rs:179-205`）。
- `ToolCallTimingGuard` 区分 dispatch（排队）vs handler（执行）耗时（`parallel.rs:303-340`）。

**⑦ `contains_external_context()` 输出标记**
- 工具输出声明"含外部上下文"（web/MCP），memory 生成自动关闭（`tool_output.rs:21-24`）。cortex 的外部搜索工具同样不应污染长期记忆。

### 11.3 落地要点（按性价比排序）

| 项 | 工作量 | 收益 |
|---|---|---|
| 接上 `nonFatalTool` + `toolResultLimiter`（一行接线，结果上限生效） | 极小 | 正确性/稳定性 |
| 修复 `question` 工具（runner 侧处理 + 权限放开）或删除 | 小 | 可用性 |
| `ToolExposure` 枚举解耦注册与可见性 | 中 | 架构 |
| 工具错误二分（可恢复喂回模型 / 致命中止） | 中 | 健壮性 |
| `tool_search` 延迟发现（MCP 多时） | 中 | 可扩展性 |
| 结果结构化 header + 中间截断 | 中 | 正确性 |
| 并行执行加并发上限 | 极小 | 稳定性 |
| 补齐 dino 缺失工具（docker/ssh/http 等） | 中 | 能力 |
| `edit_file` 改造成 patch 流式解析 | 大 | 效率/体验 |

---

## 十二、Memory 系统

### 12.1 现状总览（cortex）

**短期（会话）记忆——dino 实际用的：`dino/chatstore/*`**
- 所有会话共享一个 `shared_chat.db`（`sqlite.go:50-103`），表 `messages(session_id, role, content, timestamp, tool_calls)` + `metadata`。
- 压缩只用 `DeterministicCompact`（`compact.go:42-140`，纯启发式）；**LLM 摘要的 `Hybrid` provider 从未被构造**（grep 无 `NewHybrid` 调用方）。
- 异步压缩：`AddMessage` 时消息数 > 阈值触发 `go compressAsync`（`memory.go:98-102`）；`SQLite.Compress` **删除旧行**并把确定性摘要存 `metadata`，但 `GetSummary` **引擎从不调用**——摘要实际不进上下文，压缩后只剩窗口内消息。

**长期（持久）记忆——`dino/mem/` 已实现但完全未接线**
- `dino/mem/ingest.go`：memkit ingest，扫描 chatstore，LLM 抽取 `content<category<tags` 知识条目（类别 user/feedback/project/reference），4 worker + ticker。
- `dino/mem/tool.go`：`sqliteMemoryTool` 暴露 `get_preference`/`search_knowledge`/`build_system_prompt` 等 9 个操作。
- `pkg/memkit/*`：Preferences/Knowledge/Index/PageIndex store，SQLite 实现。
- **但 `go list` 确认没有任何包 import `dino/mem`——纯死代码。** 运行时无 LLM 事实抽取、无记忆工具暴露、`build_system_prompt` 无人调用。**live dino 路径下长期记忆=空。**
- `dino/chatstore/memdir.go`（MEMORY.md 风格索引）也是零调用方。
- 子代理记忆：`delegate_to_agent` 的 `ReplayToParentMemory` 会把子代理任务/输出写回父会话 chatstore（`factory.go:213-231`）。

**遗留 providers（`agent/providers/*`）**：仅 `internal/app` 用，dino 不碰。

### 12.2 Codex Memory 系统值得借鉴的模式

**① 两阶段写入管线（dino/mem 缺 Phase 2）**
- **Phase 1（每线程抽取）**：从 state DB 认领 stale 线程（SQL `INSERT ... WHERE (COUNT running < cap) ON CONFLICT DO UPDATE` 租约 + `lease_until` + `retry_remaining=3` + `ownership_token`，`state/src/runtime/memories.rs:671-843`），8 路并发（`stage_one::CONCURRENCY_LIMIT`），模型抽取出结构化 JSON `{raw_memory, rollout_summary, rollout_slug}`。
- **Phase 2（全局合并）**：**单全局锁**（`try_claim_global_phase2_job`，6 小时冷却 + 心跳），把 Phase 1 输出同步进 `~/.codex/memories/` 文件工作区，用 **git baseline diff 判断"是否有活"**（`write/src/workspace.rs:15-48`），再派一个**受限内部 agent**（no-network、只写工作区、无审批、collab 关）去更新 `MEMORY.md`/`skills/`/`memory_summary.md`，最后校验产物（`MEMORY.md` 必须存在、`memory_summary.md` 必须以 `v1` 开头）。
- **dino 的 4-worker ticker 只是 Phase 1，缺 Phase 2 的全局合并。**

**② 渐进式披露（cortex 最该学的分层）**
- 三层：`memory_summary.md`（**总是加载**，2.5k token 上限）→ `MEMORY.md`（按需搜索）→ `rollout_summaries/` + `skills/`（被指向时才打开）。
- 注入：`ContextContributor` 把渲染后的 `read_path.md` 注入每个线程的 developer 指令（`ext/memories/src/extension.rs:51-77`），内含"quick memory pass"算法（先 skim summary → 搜 MEMORY.md → 最多开 1-2 个 rollout summary → 预算 ≤4-6 步搜索）。
- 对比 cortex：`build_system_prompt` 想把全部记忆塞进 system prompt，无分层。

**③ 模型驱动检索 + 引用反馈（无需 embedding）**
- 模型通过**文件系统工具**主动搜记忆，并在回复里输出 `<oai-mem-citation>` 标记引用哪些 rollout（`core/src/stream_events_utils.rs:114-188`）。
- 引用 → 增量 `usage_count`/`last_usage` → 反过来决定 Phase 2 合并排序（`usage_count DESC, last_usage DESC`，`state/src/runtime/memories.rs:446-541`）。**记忆越被用越容易被合并保留**——自反馈环，无向量库。
- 对比 cortex 的 memkit：`SearchKnowledge` 是 SQL `LIKE` 子串匹配（`sqlite_knowledge.go:234-249`），无排序反馈。

**④ 反噪声设计**
- **"最小信号门"**：抽取 prompt 明确允许并偏好 no-op——没有值得记的就不记（`stage_one_system.md:25-46`）。
- **标记注入内容剔除**：注入的上下文（AGENTS.md、skills、external context）靠标记识别，抽取前从 transcript 剥离，模型不会背自己的脚手架（`phase1.rs:469-488`）。
- **秘钥脱敏**：rollout 抽取前后都 redact secrets（`phase1.rs:320-322`）。
- **线程 memory_mode 生命周期**：`enabled/disabled/polluted`，外部上下文污染可标记并停止贡献（`state/src/runtime/memories.rs:619-653`）。

**⑤ 保留/遗忘**
- `max_unused_days`（默认 30）剪枝未使用的 stage-1 输出（`state/src/runtime/memories.rs:396-429`）；删除线程记忆会触发 Phase 2 合并清理陈旧内容。

### 12.3 落地要点（按性价比排序）

| 项 | 工作量 | 收益 |
|---|---|---|
| **把 `dino/mem` + memory 工具接线**（目前纯死代码，接上即得长期记忆） | 中 | 能力质变 |
| 压缩摘要注入上下文（`GetSummary` 目前无人调用，摘要不生效） | 极小 | 上下文质量 |
| `DeterministicCompact` → 尾部原文 + LLM 摘要 | 中 | 长会话质量 |
| 记忆分层：总是加载的小摘要 + 按需搜索 | 中 | 上下文精简 |
| 模型驱动检索 + 引用反馈排序 | 中 | 记忆相关性 |
| 最小信号门 + 注入内容剔除（抽取质量） | 小 | 记忆纯度 |
| 租约/认领 + 全局锁（替代裸 ticker） | 中 | 并发正确性 |

---

## 十三、Subagent / 多代理

### 13.1 现状总览（cortex）

- **子代理 = 一次性 `AgentEngine`**：`NewSubagent` 要求 `ModeSubagent`（`dino/agent/subagent.go:52-66`），每次 `Execute` 构建全新 engine（`subagent.go:117`），无 LLM 状态/记忆跨调用。
- **硬限制**（`subagent.go:15-19`）：50 次迭代 / 3 分钟超时 / 每附件 32KB 截断 / 无 memory 压缩。
- **工具**：`delegate_to_agent`（`manager_subagent.go:155-215`），schema 只有 `agent`（enum **硬编码 `["general"]`**）+ `task`，返回**裸字符串** `result.Output`。**仅此一个多代理工具**。
- **自动委派是死代码**：`ShouldDelegate` 关键词/正则打分（`manager_subagent.go:68-114`）+ `SubagentHandler` 从未被构造/调用——只有 LLM 主动调 `delegate_to_agent` 工具是活的。
- **同步阻塞**：委派是阻塞工具调用，父代理停在那里等子代理跑完；无 fire-and-forget、无并行 fan-out、无 join 原语。
- **无递归**：子代理的工具集来自 registry 内置（`subagent.go:234`），不含 `delegate_to_agent`——不能派生子子代理。
- **无中途通信**：子代理无法在运行中给父代理发消息，唯一的通道是结束时返回的字符串。
- **`ReplayToParentMemory`**：子代理任务（2KB）和输出（12KB）截断后写回父会话 chatstore（`manager_subagent.go:217-246`）；**默认关闭**（`config.go:204-216` 未设该字段）。
- **隔离薄弱**：父/子共享同一 `llmProvider`、同一工具实例（无工具级状态隔离）、同一 budget；子代理无自己的 session/chatstore/budget。
- **超时后 goroutine 继续跑**：`executeToolWithTimeout` 文档化警告（`agent_tools.go:39-80`）——超时的子代理可能继续烧 token。

### 13.2 Codex 多代理系统值得借鉴的模式

**① 完整生命周期工具集（cortex 只有一个 delegate）**
- V2 工具族（`core/src/tools/handlers/multi_agents_v2/`）：`spawn_agent`（带 `task_name`/`fork_turns`/`model` 覆盖）、`send_message`（**QueueOnly**，只投递不唤醒）、`followup_task`（**TriggerTurn**，唤醒目标开新任务）、`wait_agent`（订阅 mailbox watch，返回 `{message, timed_out}` 摘要）、`interrupt_agent`、`list_agents`。
- **模型对自己的 agent 树有自我认知**（`list_agents`）+ 能剪枝（`close_agent`）——cortex 完全没有。

**② 结构化身份 / 寻址**
- `AgentPath` 层级规范名（`/root/task1/task_3`，`agent_path.rs:54-72`），`send_message`/`followup_task` 按名寻址，而非裸 thread id。
- `SessionSource::SubAgent` 编码 `parent_thread_id`/`depth`/`agent_path`/`agent_role`（`protocol/src/protocol.rs:2775-2790`）——树结构的唯一事实来源。

**③ 完成通知推送（cortex 缺失的关键机制）**
- 每个子代理一个**分离的 completion watcher**（`control.rs:569-659`）：订阅子代理状态，等终态后主动发 `InterAgentCommunication` 回父代理——父代理通过自己输入队列里的**消息到达**得知完成，而非轮询。
- 结果信封截断到 **1000 tokens**，错误时附"此 agent 失败了，如需可再给它任务"的回退文案（`session_prefix.rs:19-36`）。

**④ 队列 vs 唤醒消息**
- `send_message`（只注入上下文，不唤醒）vs `followup_task`（唤醒 + 新任务）——解耦"传话"与"派活"。
- 消息信封：`{Message Type, Task name, Sender, Payload}` 结构化文本块（`context/inter_agent_message.rs:62-70`）。

**⑤ 权限单调性（安全模式）**
- 子代理权限 profile 与父代理**取交集**（`spawn.rs:479-523`），角色只能**限制**不能扩权（`agent/role.rs:1-4`）——"子代理永不比父代理更特权"。

**⑥ 树级 budget + LRU 驻留**
- 共享 `RolloutBudget`（整棵树加权 token budget）+ `AgentExecutionLimiter`（并发容量）+ `V2Residency`（LRU 逐出空闲 agent 释放并发槽位）。比 cortex 的"单代理 50 迭代/3 分钟"墙钟限制更精细。
- `agent_max_depth` 默认 **1**（只允许 root→child 一层）、`max_concurrent_threads_per_session` 默认 **4**。

**⑦ 持久化拓扑图 + 边级 trace**
- `agent-graph-store`（SQLite）：`upsert_thread_spawn_edge`/`list_thread_spawn_children`/`list_thread_spawn_descendants`（BFS），支持崩溃恢复（恢复/续跑后代）。
- `rollout-trace` 的 `InteractionEdge`：`SpawnAgent`/`AssignAgentTask`/`SendMessage`/`AgentResult`/`CloseAgent`（`model/runtime.rs:303-325`），把父工具的调用与子代理可见消息配对——多代理运行可按 DAG 离线评测。

**⑧ 提示纪律（可直接抄）**
- 父代理指导：默认**仅显式请求才委派**；先决定"我现在该本地做什么"再委派，别把阻塞任务甩出去然后干等；委派"有界侧车任务"；`wait_agent` 要克制；实现类任务**分割不相交写作用域**；让子代理在最终答案里**列出改动的文件路径**（`multi_agents_spec.rs:691-747`）。
- 子代理指导：共享同一文件系统、编辑互相立即可见；final channel 的内容立即回传给父代理（`session/multi_agents.rs:11-59`）。
- 多代理模式作为 **developer 消息开关**（`ExplicitRequestOnly` → `Proactive`，由 reasoning effort/catalog 触发），而非启发式分类器。

### 13.3 落地要点（按性价比排序）

| 项 | 工作量 | 收益 |
|---|---|---|
| 结果结构化 + 完成通知推送给父代理（非裸字符串阻塞） | 中 | 多代理基础 |
| 接上死代码的自动委派（`ShouldDelegate`）或删除 | 极小 | 清理/功能 |
| 子代理并发上限 + 树级 token budget | 中 | 稳定性 |
| 角色化（restrict-only）+ 权限交集 | 中 | 安全 |
| `list_agents`/`close_agent` + depth limit | 中 | 可管理性 |
| `send_message`/`followup_task` 队列 vs 唤醒 | 大 | 协作能力 |
| 持久化 spawn 边 + InteractionEdge trace | 大 | 多代理 eval |
| 提示纪律注入（先本地、分割写作用域、报告文件路径） | 小 | 质量 |

---

## 落地优先级排序（更新）

| 优先级 | 项 | 工作量 | 收益 |
|---|---|---|---|
| P0 | Prompt caching（cache_control + 前缀稳定） | 中 | 成本大幅下降 |
| P0 | 流式事件不静默丢 | 小 | 正确性 |
| P0 | LLM verifier 接入默认链 | 极小 | 显著降误判 |
| P0 | **把 `dino/mem` + memory 工具接线**（纯死代码，接上即得长期记忆） | 中 | 能力质变 |
| P1 | Compaction 保留尾部 + 摘要 | 中 | 长会话质量 |
| P1 | 工具输出防二次截断 | 小 | 正确性 |
| P1 | **接上 `nonFatalTool` + `toolResultLimiter`**（结果上限生效） | 极小 | 稳定性 |
| P1 | 删死代码 / 修 eventbus / MCP stdio 二选一 | 小 | 维护性 |
| P1 | 压缩摘要注入上下文（`GetSummary` 接线） | 极小 | 上下文质量 |
| P1 | **子代理结果结构化 + 完成通知推送** | 中 | 多代理基础 |
| P2 | trace 记录 + 离线回放 | 大 | eval 底座 + 定位 |
| P2 | token / 计时分相指标 | 中 | 效率 eval |
| P2 | 两套 memory 收敛 | 中 | 一致性 |
| P2 | ToolExposure / 工具错误二分 | 中 | 架构 |
| P2 | 记忆分层 + 模型驱动检索 | 中 | 记忆相关性 |
| P2 | 子代理并发上限 + 树级 budget | 中 | 稳定性 |
| P3 | 沙箱 / 权限下沉 engine | 大 | 安全 |
| P3 | 批量评测跑批 | 大 | eval 完整化 |
| P3 | apply_patch 流式解析 | 大 | 效率/体验 |
| P3 | 多代理生命周期工具族 / 持久化拓扑 | 大 | 协作能力 |

---



## 关键文件索引

### Cortex 侧
- 循环：`agent/engine/agent_execution.go`、`agent/engine/agent_loop.go`、`agent/engine/agent_tools.go`
- LLM：`agent/llm/anthropic_native.go`、`agent/providers/langchain_llm.go`
- 编排：`dino/factory.go`、`dino/session/session.go`、`dino/runner/engine.go`
- 工具：`agent/types/tool.go`、`dino/tools/{registry,manager,tool_wrappers,builtin,mcp,skill}.go`、`pkg/mcp/client.go`
- 存储：`dino/chatstore/{sqlite,compact}.go`、`agent/providers/*`
- 长期记忆：`dino/mem/{ingest,loop,tool}.go`、`pkg/memkit/*`（当前死代码）
- Subagent：`dino/agent/{subagent,manager_subagent,prompt,info,tool_ctx}.go`
- Harness：`dino/harness/{outer_loop,stall,fingerprint,store}.go`、`dino/runner/{verifier,verifier_llm,checkpoint,blob_session_store}.go`、`dino/verify/*`

### Codex 侧（`codex-rs/`）
- Session 循环：`core/src/session/handlers.rs`、`core/src/session/turn.rs`
- 上下文：`core/src/context_manager/history.rs`、`core/src/session/context_window.rs`
- 压缩：`core/src/compact.rs`、`compact_remote.rs`、`compact_remote_v2.rs`
- 工具：`core/src/tools/orchestrator.rs`、`core/src/tools/parallel.rs`、`utils/output-truncation/`
- 工具定义：`tools/src/{tool_executor,tool_spec,tool_search,function_call_error,json_schema}.rs`、`core/src/tools/{spec_plan,registry,parallel}.rs`
- Provider：`model-provider/src/provider.rs`、`core/src/client.rs`、`core/src/responses_retry.rs`
- 配置：`config/src/config_layer_source.rs`、`core/src/config/mod.rs`
- Trace / eval 底座：`rollout-trace/`、`rollout/`、`exec/`、`analytics/`、`agent-graph-store/`
- Memory：`memories/{read,write}/`、`ext/memories/`、`state/src/runtime/memories.rs`、`context-fragments/`
- 多代理：`core/src/tools/handlers/multi_agents_v2/`、`core/src/agent/{control,registry,role,path}.rs`、`agent-graph-store/`、`protocol/src/protocol.rs`（`SessionSource::SubAgent`）
