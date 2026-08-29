# 评审报告：context-trace（上下文存储 + Trace 事件日志）

- 被评设计：`docs/design/context-trace.md`（commit d130818）
- 评审基准：worktree `ctx-trace-review`，对照 `agent/`、`dino/` 真实代码逐条核实
- 评审范围：事实核查 → BLOCKER/RECOMMENDED/OPTIONAL → scope 意见 → 总体评价
- 评审纪律：只产出本报告，未改动任何代码/现有文档

---

## 结论摘要

设计整体**高质量、方向正确、可以进入实现**。事实核查 26 条中 23 条 PASS、3 条 MINOR-FAIL（2 条证据行/措辞瑕疵、1 条理由被夸大但结论成立）。「trace 旁路记录 vs chatstore 状态层」的分工站得住，事件模型对齐 codex 且补了 iteration 粒度，挂钩点选型（直接调用点而非 hooks 扩签名）正确，落盘/零开销/回放设计可落地。

但存在 **3 个 BLOCKER**，其中 2 个直接作用于核心步骤 S2（引擎挂钩点），1 个只影响 S5（subagent）：

- **B1**：`turn_end` 设计记录点（ExecuteStream defer :955-958）作用域内**拿不到任何 TurnEndPayload 字段**——finalResult 是 `executeStreamWithIterations` 的局部变量，只在 `end` 流事件里下发。照表实现会记出空 turn_end。
- **B2**：`runToolCallsByLayer` 工具事件记录点只列了 fresh success/fatal/final-err 三条路径，**漏了缓存命中分支**（:517-537，占比极大）与输入校验 fatal 分支（:497-502），且 `tool_call`（start）事件没有独立记录点。
- **B3**：§3.5 声称 subagent trace「走引擎钩子，天然带 ThreadID」「无需改 subagent 构建」——**不成立**。`subagent.go:141` 每次新建 `AgentEngine`，新引擎 tracer 为 nil，`SetTracer` 只在 `CreateSession` 对 session 引擎调用。不改 subagent 构建则子代理事件全丢。

修复成本低、不影响设计骨架。**建议按 S1 → S2（含 B1/B2 修正）→ S3 → S4 → S5（含 B3 修正）顺序推进**。

---

## 一、事实核查（逐条 pass / fail）

### §1.1「无任何 trace / rollout / 事件日志」—— PASS
仓库内 grep `jsonl|RolloutWriter|TraceEvent` 无实现符号（`docs/` 与评估报告除外）。「缺失最大的一块」属实。

### §1.2 现有可复用载体盘点—— PASS（含 1 条证据行瑕疵）

| 设计断言 | 真实代码 | 判定 |
|---|---|---|
| `ExecuteStream` 返回 `<-chan StreamResult`（:901-1012），goroutine 内执行 | :901 签名、:934 `go func` | PASS |
| `sendStreamResult` :156-175 | :156-175 `select ctx.Done → false` | PASS |
| `executeStreamIteration` :1443-1605 | :1443-1605 | PASS |
| 非流式 `Execute` 不走 resultChan（:763-889） | :763-889 同步循环 | PASS |
| 每轮 `msg.Usage` :1505 只累计进 `result.Usage`，不随流下发 | :1505-1507；`end` 事件才带累计值（:1425-1428） | PASS |
| chunk 合并 50ms（:21-30） | :21-30 `chunkMergeFlushInterval=50ms` | PASS |
| 通道满时 `sendStreamResult` 在 ctx 取消时丢（:169-174） | :169-174 | PASS |
| hooks 接口 :23-38、Runner :265-322 | :23-38、:264-323 | PASS |
| hooks 调用点 `457,964,1314,1318,1361,1423,1457,1464,1542,1551` | 全部命中；**另有 :489 `BeforeToolCall`、:562/:576/:595 `AfterToolCall` 未列入**（不影响结论「hooks 已在热路径」，但清单不完整） | PASS(清单不完整) |
| `AfterLLMCall` 的 `responseMsg` 只有 Content、Usage 不经过（:1547-1551） | :1548-1550 仅 `Content`；Usage 在 `result.Usage`（:1505-1507） | PASS |
| `NewRunner(ae.getHooks(),"","")` agentID/sessionID 空串（:457,937,1314,1457） | 四处全空串 | PASS |
| `streamToolCallback` :249-338 只在有 `resultSender` 时构造（:927-932） | 类型 :214-338、构造条件 :927-932；ExecuteStream 内 `resultSender` 恒非 nil（:914-922） | PASS |
| session `Subscribe`/`notifyObservers` :207-232 | :207-213 / :221-232 | PASS |
| `dino/session/event.go:12-53` EventType + Event | :12-29 / :31-53 | PASS |
| chatstore `messages` 表 :107-127、`AddMessage` :209-235 | :108-127、:209-235 | PASS |
| 压缩删行、只存窗口（`Compress` 删旧） | sqlite.go:304-306 | PASS |

### §1.3 可复用结构盘点—— 全部 PASS
`types.StreamResult`（agent.go:262-271）、`ToolEvent`（:274-283）、`Usage`（llm.go:45-52，5 类 token 齐备）、`AgentResult`（:243-249）、`AgentStopCause`（stop_cause.go:10-14）、`session.Event`（event.go:31-53）、`RoughTokenEstimate`（tokens.go:7-18）——逐一命中。

### §1.4 引擎无 sessionID—— PASS
`NewAgentEngine(model, config)`（agent.go:111-136）无 sessionID；`CreateSession` 在 factory.go:661。`SetTracer(ctx, sessionID, t)` 由 dino 层注入的决策正确。（注：`ctx` 参数对 recorder 无实际语义，见 O-6。）

### 其它关键引用—— PASS（含 1 条证据行错误）
- `memoryAdapter.ReplayMessages`（factory.go:212-230）真实存在。PASS
- `WrapToolResultLimiter` 在 factory.go:1176，默认 120_000 字节（config.go:109 / tool_wrappers.go:110）。PASS
- subagent 每次 `Execute` 新建 `AgentEngine`（subagent.go:141）、走 `ExecuteStream`（:160）。PASS（但见 B3）
- 工具统一经 `runToolCallsByLayer` → `executeToolWithTimeout` → `tool.Execute`；MCP/审批 wrapper 在 dino 层包裹（factory.go:1171-1180）不改引擎路径。PASS
- 引擎不 import dino（agent.go 只 import agent/*、pkg/*）；dino import agent/hooks 无环（factory.go:15、subagent.go:12）。§3.2④ 的接口位置方案成立。PASS
- §11 文件清单含 `agent/engine/agent_config.go`——文件存在。PASS

### MINOR-FAIL（不影响结论，但点名指出）

1. **§4.1 证据行错误**：「chatstore 的 `PersistDirectory` 默认 `./dino_sessions`，`dino/chatstore/sqlite.go:56`」。sqlite.go:55 的 SQLite 内部 fallback 是 `./cortex_sessions`；真实默认 `./dino_sessions` 在 `dino/config.go:253`（MemoryConfig 默认）与 `dino/chatstore/memory.go:60`（chatstore.DefaultConfig）。结论（trace 与 chatstore 同根）正确，但引用的证据行是错的——应改指 memory.go:60 或 dino/config.go:253。
2. **§3.1 理由 1 被夸大**：「非流式 `Execute` 不走 resultChan，trace 会漏掉所有同步调用」——dino runtime 实际**只**用 `ExecuteStream`（session.go:465、subagent.go:160）；bare `Execute` 仅 `examples/basic/main.go:172` 等示例使用。dino 会话不会因此丢事件。理由 2/3（token usage 不在流里、compaction 不可见）才是否决 resultChan 旁路的真正原因，结论仍成立。
3. **§3.1 措辞**：「hooks 已在热路径…零新增调用点」——实际 §3.2③ 自己列了 ~12 处新增 `ae.tracer.Record` 调用点。「零新增调用点」应理解为「零新增 hooks 调用点」。

---

## 二、BLOCKER（必须先改再落地）

### B1 —— `turn_end` 记录点无数据（作用于 S2，核心）
**证据**：设计 §3.2③ 表「ExecuteStream defer（close(resultChan) 前）→ turn_end → :955-958」。但该 defer（:940-958）作用域内的局部变量只有 `startTime`；`finalResult`（:1296-1298 声明）、`iteration`、`finalResult.Usage`（:1351-1358 累计）、StopCause（:1390-1395）全部在 `executeStreamWithIterations` 内，仅通过 `end` 流事件下发（:1425-1428）。`TurnEndPayload` 需要的 `Output/Usage/Iterations/StopCause/WallMS` 在这个点**一个都拿不到**。失败路径（:1339-1348 提前 return）连 `end` 事件都没有，turn_end 更无从记录。

**修法**：turn_end 移入 `executeStreamWithIterations` 内（在 :1428 之后、函数返回前记录；失败路径在 :1347 前用 ErrorPayload 形态记录），或把 `finalResult`/`err` 提升到 ExecuteStream goroutine 闭包作用域。同理：`turn_start` 应在 :964 `BeforeStart` 之前记录（钩子失败会提前 return :970，若在其后记录则 turn_start 缺失）。

### B2 —— `runToolCallsByLayer` 工具事件覆盖不全（作用于 S2，核心）
**证据**：设计 §3.2③ 表只列 :555（fresh success）、:571-577（fatal）、:591-595（final err）。漏了：
- **缓存命中分支 :517-537**：`getCachedToolResult` 命中后走 `OnToolResult(…, toolResult)`（:522-524）直接返回。工具缓存是常态路径（`toolCache` LRU，agent.go:71-75），漏记 = eval 账本少记大部分工具调用。设计 payload `ToolResultPayload` 有 `Cached bool`，但没有对应的记录点。
- **输入校验 fatal :497-502**：`schema.ValidateInput` 失败走 `OnToolError`（:499-501）直接返回，无记录点。
- **`tool_call`（start）没有独立记录点**：设计表把 `tool_call` 与 result 共行，但 `ToolCallPayload.StartWallMS` 语义要求**在分派前**记录（:485-489 `callback.OnToolCall` 之后）。若只在 result 点记录，start 时间戳实际是 end。

**修法**：`tool_call` 记在 :485-489；`tool_result`/`tool_error` 覆盖四条路径——cached（:517-537）、fresh success（:555-563）、fatal（:571-577）、final err（:591-596）。

### B3 —— subagent 引擎无 tracer，§3.5「无需改 subagent 构建」不成立（作用于 S5，不影响 S1-S4）
**证据**：`subagent.go:141` 每次 `Execute` 新建 `AgentEngine`，其 tracer 字段为 nil；`SetTracer` 只在 `CreateSession`（factory.go:762-774）对 session 引擎调用。子代理在 `SubagentManager` goroutine（manager_subagent.go:99-131）执行，也不经过父 session 的 `Observer`。设计 §3.5 声称「同一 Recorder：子代理执行在父 session 内……走引擎钩子，天然带 ThreadID」——新引擎没接 recorder，**事件全丢**，「无需改 subagent 构建」是错的。

**修法**：把 recorder 构造器（或 Tracer 工厂）注入 `SubagentManager` → `subagentImpl.Execute`（:141 之后 `eng.SetTracer(...)`），并传入 `thread_id`（= AgentPath，manager_subagent.go:122）/ `parent_trace_id`（= 父 turn 当前 TraceID）。这同时是 §9 S5 的前提，S1-S4 不受影响。

---

## 三、RECOMMENDED（落地时采纳）

### R1 —— envelope `iteration,omitempty` 与 iteration=0 冲突（§2.1）
`Iteration int json:"iteration,omitempty"`：首次 LLM 调用是 iteration 0，`omitempty` 会丢弃 → 首个 `llm_call` 事件无 iteration 字段，reducer 无法区分「iteration 0」与「非迭代粒度」（设计 §2.1 自述「-1/缺省 = 非迭代粒度」，但 omitempty 对 int 只省略 0，不省略 -1）。改用 `*int` + omitempty，或去掉 omitempty 恒写。

### R2 —— `executeStreamIteration` 的 `llm_call_end` 数据源要标注（§3.2③）
设计表说 :1547-1553 记录 `llm_call_end` 带 `msg.Usage`，但该处作用域内 `responseMsg`（:1548-1550）只有 Content；真实 Usage 在 `result.Usage`（:1505-1507，循环内累计）。照表实现会记 0。表里应注明数据源是 `result.Usage`（且 `output_len` 用 `outputBuilder.Len()`）。

### R3 —— turn_end 同步 Flush 与消费者延迟耦合（§4.3 × §3.2③）
turn_end 记录 + `Flush()` 放 ExecuteStream defer（close(resultChan) **前**），磁盘慢时 Flush 阻塞 → `close(resultChan)`（:956）延迟 → range 消费者卡住、`isRunning.Store(false)`（:957）不释放 → 紧接的再次 `ExecuteStream` 会得 `EC_AGENT_BUSY`。建议 close(resultChan) **之后**再 Flush，或 Flush 带超时/后台化，保住「turn_end fsync 保证整 turn 可读」的同时不耦合会话收尾延迟。

### R4 —— 子代理 orchestration 事件不会经父 session Observer（§3.3 × §3.5）
子代理在 SubagentManager goroutine（manager_subagent.go:99-131）执行，不在 session 循环内，`notifyObservers`（session.go:221-232）收不到子代理编排事件。若需要子代理的 planner/approval 事件进 trace，需额外接线（或依赖 B3 的引擎侧事件）。设计 §3.3「零侵入」仅对父 turn 成立。

### R5 —— trace 敏感数据无脱敏/过滤钩子（§4/§7 未覆盖）
trace JSONL 会落盘工具输入/输出（含文件内容、潜在密钥）。评估报告 §10 提到 codex 有秘钥脱敏。建议本轮至少声明风险，并预留可插拔的 payload 过滤器接口（`Record` 前一层 `Filter(ev) (Event, bool)`）。

### R6 —— §11 文件清单与 §5.2 配置决策不一致
§11 列了 `agent/engine/agent_config.go: AgentConfig.TraceEnabled bool`，但 §5.2/§13.1 推荐 dino 层 `f.config.Trace.Enabled`（引擎 tracer=nil 不感知配置）。落地时统一到 dino 层，§11 该行删除或改标注「（备选，不推荐）」。

---

## 四、OPTIONAL

### O1 —— 工具定义只存名字，无法精确还原 prompt（§2.4 / §6.2 措辞）
`llm_call` 只记 `Tools []string`，默认截断 messages。默认配置下「模型实际看到了什么」是**近似还原**（内容截断 + 工具 schema 缺失）。若精确还原是硬需求，加 `CaptureToolSchemas` 开关或 schema hash；至少 §6.2 标题应注明「默认近似」。

### O2 —— 目录 fsync（§4.4）
外置 payload `os.Rename` 前只 fsync 了文件。为收紧「崩溃后回放不指向缺失文件」的窗口，建议 rename 后 fsync 父目录。

### O3 —— `TestDisabledTracerZeroOverhead` 白盒位置（§5.3）
测试放 `dino/trace/recorder_test.go` 却访问 `eng.tracer`（unexported 字段）——需要 engine 包白盒。要么移到 `agent/engine` 包内，要么加导出 getter（如 `TracerEnabled() bool`）。

### O4 —— turn_id 轮转规则未定义（§2.1）
同一 session 内 subagent 的 `turn_start` 是否消耗 turn_id 序列？建议 turn_id 只对根 turn（`thread_id` 空）递增，subagent 靠 `thread_id` + `parent_trace_id` 区分，避免父子混用序列。

### O5 —— `SetTracer(ctx, sessionID, t)` 的 ctx 参数无语义（§3.2）
recorder 不感知 context；`ctx` 参数无用，建议去掉（或改为 `SetTracer(sessionID, t)`），与 §5.2 构造侧一致。

### O6 —— `llm_chunk` 事件默认关，但 `CaptureChunks` 打开时 reducer 体积（§4.3）
逐 chunk 进 trace 时，即使合并 50ms，高吞吐流事件量仍大；§6.2 reducer 需定义 chunk 折叠规则。本轮默认关即可，S6 再补。

---

## 五、scope 意见

**「只做 trace 旁路、不做 ResponseItemEnvelope 化」取舍正确**。理由核实成立：

- 上下文存储改造成本被正确识别——`prepareMessages`/`buildNextMessages`/三段式 trim（agent_execution.go:1024-1131、compaction.go:88-145）与 chatstore（sqlite.go:287-313 压缩删行）纠缠，侵入式重构风险高。
- 「trace 是记录、chatstore 是存储、正交不互读写」分工站得住——chatstore 回答「下一轮看到什么」（会压缩/删行/只存窗口），trace 回答「这一轮实际发生什么」（不可变）。后续 trace→chatstore 恢复走 `ReplayMessages`（factory.go:212-230）合理（Clear 后按序 AddMessage，语义正确）。
- **被低估的风险只有一个且不大**：B2（工具事件覆盖）与 B3（subagent 接线）的改动面比设计宣称的「两处小侵入 + 一个 setter」大——`runToolCallsByLayer` 实际有 5 条工具路径、subagent 需要注入链路。其余风险（hooks 改动面——设计已正确选择不改 hooks 签名、性能——异步写 + 非阻塞投递）已覆盖。
- **eval 账本（§8）顺带设计到位**：`types.Usage` 五字段与 codex `InferenceCall` 一一对应（llm.go:45-52 的 B1 语义注释确认 PromptTokens 为 total input），`llm_call_end.DurationMS` + `tool_result.DurationMS` + `Cached` 已覆盖 10.2 主体；「dispatch 排队 vs handler 执行」分离留后续，合理。

**迁移顺序（§9）与测试清单（§10）完整可执行**。S1-S4 无 BLOCKER 依赖，S5 依赖 B3 修正。§10.2 集成测试提到 `dino/agent/result_test.go`——文件存在，扩展可行。

---

## 六、总体评价

**结论：设计能指导实现，trace 本轮可以进入实现。**

- **事实基础扎实**：26 条关键引用 23 条精确命中，事件流载体、可复用结构、数据流（token usage 只走 end 事件、compaction 在 prepareMessages 内、hooks 空归属）判断全部正确。证据行错误仅 1 处（§4.1 sqlite.go:56），且不改变结论。
- **方案正确**：直接调用点 + nil-guard（而非 hooks 扩签名）是对的选择——hooks 空 agentID/sessionID（:457,937,1314,1457）且语义是「可被外部监听」，trace 需要「内部必达记录」。接口放 `agent/hooks` 避免 import cycle，已被 `compaction` 注入先例（agent.go:95-98）验证。
- **3 个 BLOCKER 全是「实现细节级」而非「方向级」**：B1/B2 是 §3.2③ 记录点表格的落点/覆盖缺陷，B3 是 §3.5 的过度声称。修复都集中在 `agent_execution.go` 一个文件内，不触碰设计骨架。

**建议推进顺序**：S1（`dino/trace` recorder 包，纯新包）→ S2（引擎挂钩点，**先修 B1/B2**）→ S3（dino 接线）→ S4（trace-replay）→ S5（编排 + subagent，**先修 B3**）。S1 可立即开工；S2 开工前把 B1/B2 的修法落进 §3.2③ 表。

---

## 附：评审统计

- 事实核查：PASS 23 / MINOR-FAIL 3（§4.1 证据行、§3.1 理由 1 夸大、§3.1 措辞）
- BLOCKER：**3**（B1 turn_end 无数据 / B2 工具事件覆盖不全 / B3 subagent tracer 缺失）
- RECOMMENDED：**6**（R1-R6）
- OPTIONAL：**6**（O1-O6）
- scope 结论：旁路取舍正确，可进入实现
