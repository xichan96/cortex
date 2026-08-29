# Cortex 下一轮优化项清单

> 整理日期：2026-08-28
> 来源：4 个 P0 实现分支（prompt caching / 工具管线 / 长期记忆 / 子代理）完成并合并到 dev 后，各设计文档「实现备注」章的遗留待定点。
> 参考：评估报告 [`docs/optimization-review-vs-codex.md`](optimization-review-vs-codex.md) 的落地优先级表。
> 状态：**几乎全部完成（2026-08-29）**。已完成：P1.1 / P1.2 / P2.2 / P3.1 / P3.2 / P3.3 / P3.4 / P3.5 / P4.1 / P4.2 + context-trace 专项 S1-S5 + tools-codex-eval 的 E6/E7。剩余见下方「待办」标记。
> 参考：评估报告 [`docs/optimization-review-vs-codex.md`](optimization-review-vs-codex.md) 的落地优先级表。

---

## Context-Trace（评估报告第十章 · 专项）—— ✅ 已完成（2026-08-29）

- **设计**：`docs/design/context-trace.md` + 评审 `docs/design/review-context-trace.md`（合并 `ctx-trace-impl` 进 dev，commit `e829464`）。
- **实现**：S1-S5 全部落地，dev 分支 commit 序列：
  - S1 `f50ddf7`：`dino/trace` recorder（异步 JSONL、非阻塞投递、体积控制、轮转）
  - S2 `94f2756`：引擎挂钩点（`hooks.Tracer` + `SetTracer` + 各记录点，含 B1/B2 修正）
  - S3+S4 `a044f04`：dino 接线 + `trace-replay` CLI + reducer（`RenderText`/`Reduce`）
  - S5 `01a76e2`：subagent 引擎注入 tracer（B3）
- **评审 BLOCKER 处理**：B1（turn_end 移入 `executeStreamWithIterations`）/ B2（tool_call 独立记录点 + 4 条工具路径全覆盖）/ B3（subagent 新引擎注入 tracer）全部修正。
- **开启方式**：`dino.Config.Trace.Enabled = true`（默认关 = 零开销）。回放：`go run ./cmd/trace-replay --dir ./dino_sessions/traces --session <sid>`。
- **测试**：`dino/trace` 单测（envelope/append/durability/崩溃半写/dropped/截断/轮转/replay）+ 引擎零开销断言全绿，`-race` 通过。

---

## P1 · 低成本高收益（建议下一轮先做）

### P1.1 `ReasoningTokens` 回填（prompt caching O1）—— ✅ 已完成（`ee0c51f`）
- **背景**：`agent/types/llm.go:46` 的 `Usage.ReasoningTokens` 仍为 0。Anthropic 的 `usage.output_tokens` 不含 reasoning tokens（`usage.reasoning_tokens` 单独给，如 Claude thinking）。
- **改动**：`agent/llm/anthropic_native.go` 的 usage 解析处，把 `reasoning_tokens` 映射到 `Usage.ReasoningTokens`。同时 `dino/session/turn_observe.go` 的 token 复制处顺带带上。
- **工作量**：极小（~10 行）。**收益**：token 观测完整，thinking 成本可追踪。
- **依赖**：无。

### P1.2 F6 chunk 合并第二段（工具管线）—— ✅ 已完成（`3e5fb0a`+`d39d7d2`）
- **背景**：F6 流式背压第一版只做了「阻塞 + turn ctx 修复」，chunk 合并（§6.1 第二阶段）未做。高吞吐时大量 chunk 事件逐条投递仍可能压满通道。
- **改动**：`agent/engine/agent_execution.go` 的 resultSender 或发送端，对 `chunk` 类型做无锁缓冲 + 定时 flush（如 50ms），合并成一条大 chunk。
- **工作量**：中。**收益**：高吞吐下事件数下降，消费端（UI/日志）压力降低。
- **依赖**：无。**注意**：合并不影响正确性（chunk 顺序保持），但要保证 `end`/`error`/`tool_event` 即时投递不合并。

### P1.3 `git push origin dev`（运维）
- **背景**：dev 领先 origin/dev 41 commit，4 项 P0 + 修复 + 收尾均未推远端。
- **建议**：push 前先确认远端 main/dev 策略（是否走 PR）。若直接 push 到 dev，需用户授权。
- **工作量**：零。**收益**：远端备份 + 协作可见。

---

## P2 · 需产品决策

### P2.1 F7 `question` 用户回答回流通道（工具管线）—— ✅ 已完成（`d0236c8`，异步事件 + OnQuestion 回调）
- **背景**：`QuestionTool.Execute` 已返回 `SentinelQuestionResult` sentinel（F7 部分落地），但 runner 侧检测 + `EventTypeQuestion` + 回答回流通道未做。当前 `question` 权限默认拒绝且无消费方。
- **决策点**：question 的交互模型（同步阻塞等待回答？还是异步事件 + 外部回答注入？）；权限默认值。
- **工作量**：中。**收益**：agent 可向用户提问，交互能力提升。
- **依赖**：需产品/用户拍板交互模型。

### P2.2 `delegate_return_mode` 开关（子代理 §14）—— ✅ 已完成（`4a87747`）
- **背景**：`delegate_to_agent` 返回值已从裸字符串改为信封（`DelegateResult`）。破坏性变更——父代理 prompt 里如果预期纯文本，信封可能干扰。
- **决策点**：是否加 `delegate_return_mode: "string" | "envelope"` 配置开关（默认 envelope）。
- **工作量**：小。**收益**：兼容旧 prompt/下游消费者。
- **依赖**：无。

### P2.3 `spawn_agent` vs 扩 `delegate_to_agent` schema（子代理 §14）—— ✅ 已完成（S3 采用新工具名 `spawn_agent`/`wait_agent`，`f3b04c8`）
- **背景**：S3（mailbox + spawn/wait + notifier）落地时，工具名用新工具 `spawn_agent`/`wait_agent`（贴近 codex 工具族），还是扩 `delegate_to_agent` schema（`async: true` + `task_id`）。
- **决策点**：工具命名与 schema 演进路径。评审建议新工具名（避免 async 标志与阻塞语义纠缠）。
- **工作量**：中（S3 时一并做）。**收益**：更清晰的多代理工具族。
- **依赖**：S3 排期。

### P2.4 子代理权限 restrict-only（子代理评估）—— ✅ 已完成（`2f4ce49`）
- **背景**：当前子代理共享父 `llmProvider`/budget，无权限交集约束。codex 的「子代理永不比父代理更特权」（权限取交集）未实现。
- **决策点**：是否引入角色化权限交集（restrict-only）。评审列为 P2。
- **工作量**：中。**收益**：安全（子代理不能扩权）。
- **依赖**：无。

---

## P3 · 较大工程（需专项设计 + 实现）

### P3.1 Compaction 前缀保留（prompt caching Step 4）—— ✅ 已完成（`508137d`）
- **背景**：`trimHistoryToTokenBudget`（`agent/engine/agent_execution.go:987-1017`）丢最旧，破坏 prompt cache 前缀稳定性。`dino/chatstore/compact.go` 的 `DeterministicCompact` 也非前缀友好。长会话（>100 消息）缓存命中率上限由 compaction 决定。
- **方向**（评估报告第三章 Codex 做法）：「尾部原文 + LLM 摘要 + 摘要放最后」模型；compaction 刻意保留缓存前缀，跟踪 `auto_compact_window`。
- **工作量**：中-大。**收益**：长会话成本进一步下降（当前已做的前缀稳定在 compaction 后失效）。
- **依赖**：需先解决 `DeterministicCompact` → LLM 摘要（P3.4）或至少让 compact 不破坏前缀。

### P3.2 长期记忆 user 全局合并（长期记忆）—— ✅ 已完成（`139be67`+`f6fc5a3`）
- **背景**：当前长期记忆是 **per-session** 语义（`uid = sessionID`，评审 B1 决策 (a)）。user 全局合并（跨 session 共享记忆空间）是后续独立任务。
- **方向**：`getGlobalUserID`（`dino/mem/ingest.go:253-261`）需要有 `metadata.user_id` 写入方；`SaveContext`/`CreateSession` 写 user 归属；Phase 2 全局合并锁跨 session 复用。
- **工作量**：中-大。**收益**：记忆跨会话复用，agent 个性化增强。
- **依赖**：需设计多 session 归属策略。

### P3.3 子代理 S3/S4（mailbox + spawn + notifier + turn 唤醒）—— ✅ 已完成（`f3b04c8`+`818f5df`）
- **背景**：S1（结构化）+ S2（超时治理）+ S3 铺路（session 级 cancel map）已落地。S3 的 mailbox + `spawn_agent`/`wait_agent` + completion notifier，S4 的 B2 turn 唤醒未做。
- **方向**：mailbox 独立可注入组件（评审 R5）；completion watcher 主动回发父代理（codex `control.rs:569-659`）；B2 需动 `Session.run` 调度（`session.go:266-291`），与 `send_message` 一起做。
- **工作量**：大（S3 + S4）。**收益**：多代理协作能力（fire-and-forget + 完成通知 + 唤醒）。
- **依赖**：P2.3 工具命名先定。

### P3.4 `DeterministicCompact` → LLM 摘要替换（长期记忆/压缩）—— ✅ 已完成（`afe7324`）
- **背景**：`DeterministicCompact`（`dino/chatstore/compact.go:42-140`）是纯启发式，非英语/代码密集场景退化。`Hybrid.GetSummary`（LLM 摘要）已实现但 `NewHybrid` 零调用方。
- **方向**：把确定性启发式替换为「尾部原文 + LLM summary」模型（评估报告第三章）。摘要作为最后一项注入，初始上下文在最后一条真实用户消息前重注入。
- **工作量**：中-大。**收益**：长会话质量（CJK/代码密集场景）。
- **依赖**：需先确认 `Hybrid` provider 默认化（P3.5）或另起压缩 provider。

### P3.5 `Hybrid` provider 默认化（长期记忆）—— ✅ 已完成（`afe7324`）
- **背景**：`dino/chatstore/memory.go:251` 的 `NewHybrid` 无任何调用方。`Hybrid` 提供 LLM 摘要压缩能力，但从未被构造。
- **方向**：`dino/factory.go` 构造 `Hybrid` 作为默认压缩 provider（或配置开关），接上 `GetSummary` 注入链路（S1 已留接口）。
- **工作量**：中。**收益**：压缩摘要真正生效（当前 `GetSummary` 虽接线但 Hybrid 未构造，摘要实际不产出）。
- **依赖**：无（可与 P3.4 分开做）。

---

## P4 · 清理性

### P4.1 自动委派死代码接线/删除（子代理）—— ✅ 已完成（`fe08de0`，删除方案）
- **背景**：`ShouldDelegate`（`manager_subagent.go:68-114`）+ `SubagentHandler` 从未被构造/调用——只有 LLM 主动调 `delegate_to_agent` 是活的。
- **决策**：接线（启用关键词/正则打分自动委派）还是删除（明确只保留显式委派）。
- **工作量**：极小（接线）或小（删除）。**收益**：清理死代码 / 增加自动委派能力。
- **依赖**：产品决策是否要自动委派。
- **状态**：✅ 已定稿（2026-08-28，分支 nxt-autodelegate）：**删除**。`ShouldDelegate` 仅被无构造点的 `subagentExecutor.autoDelegate`/`SubagentHandler.ProcessInput` 调用；`DefaultConfig` 里 `TriggerOnKeyword:true` + `general` trigger 已配但从未被消费（非「配置未开」而是「无接线」）。产品路径只走显式委派 `delegate_to_agent`，对齐 codex「ExplicitRequestOnly，非启发式分类器」。已删 `ShouldDelegate`/`TriggerResult`/`SubagentHandler`/`SubagentEvent`/`SubagentEventEmitter`/`subagentExecutor`/`buildSubagentEngine` + 对应配置字段（`TriggerOnKeyword`/`Triggers`/`SubagentTrigger`）。如需自动委派，后续按 S5 重新设计（事件通知 + 显式开关），而非复活启发式打分。

### P4.2 `FatalToolError` 清单扩展评审（工具管线 F3）—— ✅ 已完成（`69455ec`）
- **背景**：`FatalToolError` 目前只用于 schema 校验失败（`agent_execution.go:407`）。`EC_TOOL_AUTH_ERROR`/`EC_TOOL_INPUT_ERROR`/MCP 错误分类是否进 fatal 清单需单独评审。
- **决策**：哪些错误该 fatal（终止迭代）vs recoverable（喂回模型）。审批拒绝已透传（F2/BLOCKER-2），但完整清单未定。
- **工作量**：小。**收益**：错误分类完备，避免不该重试的循环。
- **依赖**：无。

### P4.3 scheduler 包遗留确认 —— ✅ 已关闭（objx 已在 vendor，`go build ./scheduler/` + `go test ./scheduler/` 通过）
- **背景**：tool-pipeline §13 遗留第 5 条称「scheduler 缺 objx 无法编译」。**已核实过时**：objx v0.5.2 已在 vendor，`go build ./scheduler/` + `go test ./scheduler/` 通过。此条可关闭。

---

## 建议下一轮排期

| 轮次 | 项 | 理由 |
|---|---|---|
| **下一轮（并行）** | P1.1 ReasoningTokens、P1.2 F6 chunk 合并、P2.2 delegate_return_mode、P4.1 自动委派清理、P4.2 FatalToolError 评审 | 都是小改动，可并行，低风险 |
| **下一轮（串行设计）** | P1.3 push 前确认远端策略 | 运维决策 |
| **随后（专项）** | P2.1 question 回流、P2.4 子代理权限 | 需产品决策 |
| **专项（大）** | P3.1 compaction、P3.2 user 全局合并、P3.3 S3/S4、P3.4/P3.5 压缩升级 | 各自需要设计 + 实现两个阶段 |
