# 评审报告：《子代理系统改造设计：结果结构化 + 完成通知推送》

> 被评设计：`docs/design/subagent.md`（基于 worktree HEAD `33fd4ca`，设计自述基于 `5d5130c`）
> 评审基准：worktree 实际代码 + 评估报告 `docs/optimization-review-vs-codex.md` 第十三章 + codex 参考实现（`/Users/CHENXI/rust/codex/codex-rs`）
> 评审日期：2026-08-28
> 评审纪律：仅新增本文件，未改任何业务/文档代码。

---

## 0. 结论摘要

**可以进入实现**，但需先解决 2 个 BLOCKER、采纳 6 个 RECOMMENDED。推荐路径：**从 S1（方案 A 纯结构化）开始，S2（超时治理）紧跟，两者独立验证后合入；S3（B1 fire-and-forget）可在 S1+S2 稳定后启动；S4（B2 turn 唤醒）留到 roadmap 与 send_message 一起做**——与设计自荐顺序一致。

总体判断：这是一份**质量相当高**的设计——事实核查 18 处引用中 17 处准确、1 处 line 号偏移（语义无错）；方案 A→B1→B2 的分阶段与"信封内嵌裸字符串"兼容策略成立；mailbox 与 eventbus 分工的论证正确；超时治理方向对。主要问题集中在**没有真正落到代码执行的破坏性风险**（工具缓存毒化、`SubagentManager.Close` 孤儿回收缺口、B2 与现 `Session.run` 结构的兼容细节），以及几个**成本被低估**的点（父 turn 阻塞下 fire-and-forget 语义、`files_changed` 采集、B1 的工具注入）。

**BLOCKER（先改再落地）：2 | RECOMMENDED（落地采纳）：6 | OPTIONAL：5**

---

## 1. 事实核查结果（逐条 pass/fail）

| # | 设计引用 | 核查结果 | 证据 |
|---|---|---|---|
| F1 | `NewSubagent` 要求 `ModeSubagent`，否则报错 | **PASS** | `dino/agent/subagent.go:52-66`，mode 校验在 53-55 行 |
| F2 | 每次 `Execute` 全新 engine（subagent.go:117） | **PASS** | `subagent.go:117` `eng := engine.NewAgentEngine(s.llmProvider, cfg)` |
| F3 | 硬限制 `subagent.go:15-19`（50 iter / 3min / 32KB） | **PASS** | `subagent.go:15-19`，常量值一致 |
| F4 | `buildConfig` 设 `EnableMemoryCompress = false`（151-174） | **PASS** | `subagent.go:170` |
| F5 | `delegate_to_agent` schema 只含 agent+task，enum 硬编码 `["general"]`（178 行） | **PASS** | `manager_subagent.go:178` |
| F6 | `Execute` 同步调 `manager.Execute`，返回裸字符串 `result.Output`（212 行） | **PASS** | `manager_subagent.go:205,211-213` |
| F7 | `ShouldDelegate` + `SubagentHandler` 是死代码，无构造点 | **PASS** | `grep NewSubagentHandler/newSubagentExecutor` 无调用方；`factory.go` 只构造 `NewSubagentManager`(437) + `NewSubagentTool`(594) |
| F8 | `delegate_to_agent` 注册在 `factory.go:593-604`，`ReplayToParentMemory` 再包一层 | **PASS** | `factory.go:593-604`；`newDelegateParentMemoryTool`(219) + `ReplayToParentMemory` 分支(596-600) |
| F9 | 父代理共享 engine（`factory.go:530`） | **PASS** | `factory.go:530` `engine.NewAgentEngine(f.llmProvider, agentConfig)` |
| F10 | `Session.run` select 输入/队列，同刻一 turn（session.go:255-292） | **PASS** | `session.go:255-292`，`processQueueItem`(294-311) 同步执行 |
| F11 | turn 内工具并发池（`agent_execution.go:334-423`）→ `executeToolWithTimeout` | **PASS（line 微偏）** | 实际 goroutine 池在 `runToolCallsByLayer`（`agent_execution.go:320-426`，errgroup at 321）；334-423 在 320-426 区间内，语义无误 |
| F12 | `executeToolWithTimeout`（`agent_tools.go:44-80`，设计 §1.3 写 44-80，§1.4 写 39-80 且引 41-43 注释） | **PASS** | 函数体 44-80；设计 §1.4 的 "39-80 / 41-43 注释" 是**小 line 偏移**（注释实际在 40-43），语义准确 |
| F13 | `buildToolCallResults`（`agent_execution.go:241-277`）经 Sanitize/Format/Truncate 注入 | **PASS** | `agent_execution.go:241-277`，271-273 行 |
| F14 | `FormatToolResult` 自动 JSON 序列化（`types.go:336-351`） | **PASS（文件路径偏）** | 实际在 `agent/types/agent.go:336-351`（非 types.go）；JSON `MarshalIndent` 逻辑一致 |
| F15 | `LangChainAgentEngine` 有同步 `tool.Execute(ctx, args)` 路径（`langchain_engine.go:224`），dino 主路径用 AgentEngine | **PASS** | `langchain_engine.go` `handleToolCalls` 内 `tool.Execute(ctx, ...)`；dino `factory.go:530` 用 AgentEngine（非 langchain 引擎） |
| F16 | LLM HTTP ctx 传播：`anthropic_native.go:179` `NewRequestWithContext`；OpenAI/DeepSeek 走 langchain 传 ctx | **PASS** | `agent/llm/anthropic_native.go:179`；`agent/providers/langchain_llm.go:88-106` 传 ctx |
| F17 | `pkg/eventbus`：`StandardEventBus`(95-393) + `NewGeneric`(396) + `GlobalBus`(654)；dino 包 `NewBus()`（bus.go） | **PASS（GlobalBus line 微偏）** | `StandardEventBus` 95，`NewGeneric` 396，`GlobalBus` 定义在 650-654；`dino/bus.go:19-23` `NewBus` 用 `eventbus.NewGeneric[BusEvent]` |
| F18 | `Session` 有 output chan + observers（session.go:112-119）+ Event 结构 | **PASS** | `session.go:89-110`（output/observers 在 92/105-106）；`event.go` 有 Event 结构 |
| F19 | hooks `OnAfterToolCall/OnAfterIteration/OnAfterEnd` 可用于观察 | **PASS** | `agent/hooks/hooks.go:23-38` 接口齐全；`factory.go:531-533` `SetHooks` |
| F20 | `ReplayToParentMemory` 默认关（`config.go:204-216`） | **PASS** | `dino/config.go:204-216` 的 `Subagent` 默认值**未设**该字段 → 零值 false |

**核查小结**：18+ 项事实引用全部为真，仅两处 line 号轻微偏移（F12 的 39/41-43、F14 的文件名 types.go vs agent.go），均不改变语义。**设计作者对代码现状的掌握是可靠的。** 唯一需要补充的事实：

- **F21（设计遗漏）**：`SubagentManager` 持有**共享**的 `*Manager`（`manager_subagent.go:55`），`Manager` 内 `subagents map` 按 name 缓存 Subagent（`manager_subagent.go:218-241`），**跨会话共享同一缓存、同一组子代理工具实例**（`factory.go:437` 每 factory 一个 manager，多 session 共用）。这直接影响设计 §5.4 把 mailbox/notifier 挂 `SubagentManager` 的会话隔离性（见 R5）。

---

## 2. 事实核查中发现的 3 个设计未覆盖的现状事实

> 这些不是"设计写错"，而是"设计没写到但会实质影响实现"的事实：

1. **工具结果缓存**：`AgentEngine` 有 `toolCache`（`agent_cache.go:83-120`，LRU + 5min TTL，key = toolName+args MD5）。`delegate_to_agent` 改变返回值形状后，**同 args 的旧缓存（裸字符串）会在 5 分钟内命中并注入父 LLM**——A 阶段灰度期会出现"部分调用返回信封、部分返回旧字符串"的混装。设计 §5.2 只 grep 了消费方，没考虑缓存。→ **B1**
2. **`SubagentManager.Close()` 目前只在 factory `CloseAll` 时被调**（`factory.go:787`），**逐 session 关闭（`CloseSession`）不会回收子代理**。设计 §7.2 第 5 点把 `Close` 改成 cancel map 全 cancel，但没说明**谁在 session 粒度触发**——若 `spawn_agent` 的子代理 goroutine 挂在 factory 级 manager 上，父 session 关了它还在跑（靠 watchdog 收尸）。→ **B2**
3. **eventbus 的 `dino/bus.go` `Bus` 实例目前零使用方**（grep `NewBus()`/`GetGlobalBus()` 无业务调用），设计把它当"旁路可观测"载体其实是**从零开始建管线**，不是"复用现有"——工作量和验证成本比设计预期高一点。→ **R4**

---

## 3. 分级清单

### BLOCKER（必须先改再落地）

**B1. 工具结果缓存毒化未处理 —— `delegate_to_agent` 返回信封后，5 分钟 TTL 的旧缓存会让父 LLM 混读"裸字符串/信封"两种形态**
证据：`agent/engine/agent_cache.go:83-120`（`getCachedToolResult`/`setCachedToolResult`，key=toolName+args MD5，`CacheExpirationTime=5min`，`agent/types/agent.go:15`）；缓存写入在 `agent_execution.go:366,394`；`delegate_to_agent` 返回值在 `manager_subagent.go:212`。
**为何是 BLOCKER**：设计 §3.2 兼容策略的核心论据是"返回值形态变化不破坏现有消费方"，但缓存让这个保证**在同一进程内自相矛盾**——灰度切换瞬间旧 cache key 命中时父 LLM 拿到裸字符串，miss 时拿到信封 JSON，观察到的行为不稳定且无法归因。
**修法**（任选其一）：
1. 改动点引入**缓存 buster**：A 阶段让 `SubagentTool.Execute` 返回前 `ae` 无法自清（工具拿不到 engine），退而在 schema 的 `task_id` 参数上做文章——`task_id` 进 args，cache key 变 → 天然 miss。但 `task_id` 是 B 阶段字段。
2. **关闭 delegate 的缓存**：利用 `Metadata().Extra["no_cache"]=true`（`agent_execution.go:354-359` 已支持），`SubagentTool.Metadata()` 返回 `no_cache`。改动最小、语义最干净。**推荐**。
3. 或接受"切换窗口 5min 内混装"并在测试里显式断言（不推荐，脏）。

**B2. 孤儿子代理的回收路径不闭环 —— `SubagentManager.Close` 没有"逐 session"触发点，B1 后台 goroutine 会脱离父 session 生命周期**
证据：`dino/factory.go:669-674` `CloseSession` **不调** `subagentManager.Close()`；`CloseAll`(679) 在 shutdown goroutine 里调（`factory.go:787`）；`Manager` 的 subagent 缓存按 name 全局共享（`manager_subagent.go:218-241`）。
**为何是 BLOCKER**：设计 §7.2 承诺"`Close` 改为 cancel map 全 cancel，杜绝孤儿"，但 `Spawn` 的子代理 ctx 若派生自**父 session 的 ctx**（设计 §4.3 B1 明说"ctx 从父 Session 派生"），则必须有一个"父 session 关闭 → 该 session 派生的所有子代理 ctx 全 cancel"的钩子。现状 `CloseSession` 不触达 manager，spawn 的子代理只能靠 3 分钟 watchdog 收尸——用户切 session/关 session 后 token 仍烧到 watchdog 上限，恰好是设计要根治的问题没根治。
**修法**：B1 落地时 `CloseSession` 需通知 manager 释放**该 session 的** cancel map（manager 持 `map[sessionID]map[taskID]CancelFunc`，或按 session 分包 mailbox）。A/S1 阶段无 spawn，此项不阻塞 S1，但**必须在 S3(B1) 之前**补上——所以列为 BLOCKER（对 B1 而言），S1 可先行。

### RECOMMENDED（落地时采纳）

**R1. A 阶段 `SubagentTool.Execute` 的 error 路径被设计忽略 —— `manager.Execute` 出错时工具返回 `nil, err`，父 LLM 拿到的是**错误字符串 observation**（`toolObservationError`，`agent_execution.go:73-89`），不是信封，`status:"error"` 语义在 A 阶段**对模型不可见****
证据：`manager_subagent.go:205-208` `if err != nil { return nil, err }`；错误 observation 格式化在 `agent_execution.go:257`。
设计 §3.1 的 `Status` 字段（completed/error/timeout/cancelled）在 A 阶段只在**成功路径**被填，失败路径走 err 分支。要让"错误态信封"在 A 阶段就生效（测试 #6 `TestSubagentExecute_StatusError` 是针对 `Execute` 内部的），需把 `SubagentTool.Execute` 的 error 也折叠进信封（返回 `*DelegateResult{Status:"error", Error:...}` + nil error），并同步确认"错误 observation 文案变化不破坏下游"。这是设计测试清单与实现之间的一处 gap，需在实现时决策。

**R2. `DurationMS`/`Timestamp` 用 `time.Time`/`int64` 混排 + `Truncated` 用 `maxRunes`，字段语义要统一**
设计 §3.1 自己已经指出了 `time.Duration` JSON 不友好并改成 `int64`（§3.1 注），但 `Timestamp time.Time`（RFC3339）与 `duration_ms int64` 混排会增加模型解析负担；`types.Usage` 有 json tag（`agent/types/llm.go:41-52`，可确认）。建议：`Timestamp` 也转 `unix_ms` 或 `omitempty`，`Truncated` 的 `maxRunes` 上限明确对齐 codex `COMPLETION_MESSAGE_MAX_TOKENS=1000`（`codex-rs/core/src/session_prefix.rs:10`，已核实）。

**R3. `files_changed` 双路采集的成本被低估：结构化解析 (a) 依赖 prompt 引导的 LLM 自觉，程序化 (b) 依赖 tool_event 但**当前 `subagentImpl.Execute` 的 stream 循环只处理 `chunk` 和 `result` 两类**（`subagent.go:127-140`），补 `tool_event` 分支是实打实的新逻辑 + 键名兼容（`write_file`/`edit_file` 用 `path` 键，`fs/write.go:37` `fs/edit.go:36` 已核实）**
证据：`subagent.go:131-139` 只 `switch result.Type` 到 `chunk` 且只检查 `result.Result != nil`；`streamToolCallback` 发 `tool_event`（`agent_execution.go:123-131`）；write/edit 的 path 键在 `agent/tools/builtin/fs/write.go:37`、`edit.go:36`。
另注意：**bash 的 git 操作没有 path 键**，设计建议的启发式（git status 快照）自认本轮不做，那么 bash 改文件的场景 `files_changed` 会漏——需在 §9.1#4/#5 之外补一个"bash git 操作"测试或明确降级说明。

**R4. "eventbus 作旁路可观测"在当前代码里是**从零建设**不是复用：`dino/bus.go` 的 `Bus`/`GetGlobalBus` 无任何业务使用方（grep 全仓），且 `dino/bus.go:122-127` 的 `GetGlobalBus` 单例 + `SetGlobalBus` 存在（`SetGlobalBus` 用 `globalBusOnce.Do(func(){})` 先占位再覆盖，有"先 New 后覆盖仍生效"的坑，`bus.go:129-132`）**
设计 §8.2 建议 `Bus.Publish("subagent.completed", ...)`，但没有指定**实例级 Bus 从哪来**（factory 没建）。建议：S3 里在 factory 显式构造一个实例级 `*Bus` 注入 notifier，并**不要碰 `GetGlobalBus` 单例**（多会话隔离，设计 §11 风险 5 自己写了这点）。`isSameHandler` 反注册失效（`eventbus.go:642-648`）对**本设计**影响有限（notifier 订阅者是长期存活、无需 Unslot），设计 §11 未提但结论方向一致——旁路订阅用 `Subscribe` 且不反注册即可。

**R5. mailbox 挂 `SubagentManager`（factory 级、跨 session 共享）与 mailbox 按 sessionID 分桶自相矛盾——需要明确 mailbox 的生命周期/所有权**
证据：`SubagentManager` 是 factory 单例（`factory.go:437`）；`Manager` 内 subagent 缓存跨 session 共享（`manager_subagent.go:218-241`）。
设计 §4.5 的 `Mailbox` map 按 sessionID 分桶是对的，但"挂 `SubagentManager`"意味着 mailbox 随 factory 死、不随 session 死——父 session 关闭后 `Drop(sessionID)` 谁调？TTL 兜底（设计风险 4）行，但**需要在 session 关闭钩子里显式 `Drop`**，并给 mailbox 加 cap（防单个 session 塞爆）。建议 mailbox 独立为可注入组件，由 factory 每 session 建，或 session 关闭时回收。

**R6. B1 的"父 turn 阻塞"与 fire-and-forget 的语义张力未被设计承认 —— 现 `Session.run` 里父代理**一次只跑一个 turn**（`session.go:266-291`），turn 内 LLM 串行。`spawn_agent` 非阻塞返回后，**同一 turn 内父 LLM 能不能继续干活，取决于 LLM 会不会在同一 turn 再发工具调用**（能，多工具调用 per iteration，`agent_execution.go:1060-1090`）；但父 turn 结束后，若没有再收到用户输入，**没有任何东西驱动父代理去 `wait_agent`/处理 mailbox**——B1 的"完成通知"若无 B2 的 turn 唤醒，父代理实际仍是"下一轮用户输入时才发现"**
证据：`session.go:266-291` run 循环只在 `s.input`/queue 有内容时跑 turn。
这是 B1 与 B2 的真正分界。设计 §4.3 把 B1 描述为"不改父 turn 模型"，代价是**完成通知在 B1 下不保证被父代理及时消费**（只是落 mailbox 等下次 turn）。这本身可接受（mailbox 不丢），但设计应把"B1 完成通知可能延迟到下一用户 turn 才可见"写成**已知行为**，而不是暗示 B1 已实现"推送"。→ 影响 roadmap 排序：若产品要"父代理主动在子代理完成时继续"，B2 不是可选项而是必做项，工作量应计入。

### OPTIONAL

**O1.** `AgentPath` 一层雏形（§3.3）：合理，`Parent()` rsplit 与 codex `rsplit_once`（`control.rs:606`）对齐。建议本轮**不落盘**（只在内存），否则 §12.5 的持久化又要动 schema。可把 `AgentPath` 定义为纯 string 类型 + 校验函数，等 list_agents 时再物化。

**O2.** 设计 §5.4 的 `SubagentConfig` 新增 5 字段里 `MaxConcurrentSpawns`/`SpawnTimeout` 只在 B 阶段用——**建议 S1 不引入**，配置 schema 一次成型但字段后补默认值，避免 S1 就背上"未使用配置"。

**O3.** 测试 #8 `TestSubagentTool_BackwardCompatString`：把信封喂给 `FormatToolResult` 断言 JSON 含 `"output"`——注意 `SanitizeToolResult` 会**先递归截断**信封（`toolresult.go:81-`，map 递归 `sanitizeToolResultValue`），`output` 长文本会被截到 `MaxTruncationLength=2048`（`agent/types/agent.go:18`）再进 `FormatToolResult`。父 LLM 看到的不是"完整 output + 元数据"，而是**被截断的 output**。设计 §3.2"父代理仍能读到 output"成立但要写明截断副作用。

**O4.** §12 roadmap 里 `send_message` 的 `InterAgentMessage` 泛化：codex 的 `InterAgentMessageType` 是 `Message/NewTask` 两态（`inter_agent_message.rs:14-22`，已核实），cortex 版若把 `DelegateResult` 直接泛化会丢掉 MessageType 语义——泛化时保留 message_type 字段即可，属实现细节。

**O5.** 超时治理 §7.2 第 1 点"补注释明确语义"其实是**零改动**（`executeToolWithTimeout` 已把带 timeout 的 ctx 传给 `tool.Execute`，`agent_tools.go:67`）。真正的改动面在"子代理侧 ctx 派生 + watchdog + Close cancel"，建议 §5.1 表格里把"超时"行的描述改成这三件事，别让实现者误以为要改 `executeToolWithTimeout` 本体。

---

## 4. 方案 A / B 取舍意见

**同意设计的推荐：A → B1 → B2，且 A 独立先落。** 核心理由（与设计 §4.1/§4.3 一致，补充两点）：

1. **A 是唯一能独立验证"结构化"这一步的阶段**：`delegate_to_agent` 返回值 shape 变化、`FormatToolResult` JSON 注入、`Result` 字段扩展、缓存毒化（B1）——都能在**不碰执行模型**的前提下用 `go test ./dino/agent/...` 验证。这与"结果结构化"作为多代理所有后续能力的地基（评估 13.3 第一行）自洽。
2. **B1 的价值在当前执行模型下被高估**（见 R6）：B1 提供的"fire-and-forget + 完成落 mailbox"在没有 B2 的情况下，完成通知是**惰性可见**的（下一用户 turn 才消费）。也就是说 B1 相对 A 的增量，在"父代理自主继续推进"这个用户感知层面收益有限；它的真正价值是**为 B2/send_message 打 mailbox 基础**。因此 roadmap 应明确：**B2 不是"可选收尾"，而是 B1 价值的兑现点**——这与评估报告把它列 P1 一致，但设计 §4.3 把 B2 说成"与 send_message 一起做"，实际按依赖应**先于 send_message**（B2 的 mailbox 注入是 send_message 注入的前提）。
3. **`wait_agent` 在 B1 下的阻塞语义与 `executeToolWithTimeout` 冲突**：`wait_agent` 若做成"阻塞到子代理完成"，在父 turn 里它就是另一个"会超时的阻塞工具调用"——超时后父 LLM 会拿到 `completed:false` 摘要（对齐 codex `wait.rs:92-93,168-170`，已核实）。但**父 turn 的 turnCtx 在 `CancelCurrentTurn`/session close 时会 cancel**（`session.go:342-351`），wait_agent 必须对这种 cancel 快速退出，否则它自己就成了新的"超时后还挂着"的 goroutine——设计 §4.5 用 `sync.Cond`/channel 阻塞 + 需要补 ctx.Done 分支。

**对设计 §14 遗留问题表态**：
- #1（破坏性变更是否可接受）：**可接受，但需处理 B1（缓存）**。信封内嵌 `output` + `no_cache` 后，父 LLM 消费无感；`delegate_return_mode` 双模式开关会污染工具 schema，不建议。
- #2（新工具名 vs 扩 schema）：**新工具名**（spawn_agent/wait_agent）更贴近 codex 工具族，且避免 `delegate_to_agent` 的 `async` 标志与阻塞语义纠缠。同意设计默认。
- #3（prompt 引导文案本地化）：general.txt 目前英文（`info.go:97` loadPrompt("general")），文件路径引导建议**先英文**，本地化是提示词工程问题，不阻塞。
- #4（mailbox TTL 1h / 并发 4）：对齐 codex `max_concurrent_threads_per_session=4`（评估 13.2 ⑥）合理；TTL 建议跟随 session 生命周期优先于固定 TTL（见 R5）。

---

## 5. 各评审维度意见

### 正确性
- 方案 A 确实解决"返回裸字符串无结构化信息"（现状问题 1）——通过信封 + `FormatToolResult` JSON 注入，机制经代码验证成立。
- 方案 B 的"完成通知"在 B1 下是**降级语义**（落 mailbox，惰性消费，R6），要到 B2 才真正解决"父代理干等"（现状问题 2）。设计对 B1 的描述有轻微"营销化"（把"回发"写成已推送），评审按 R6 澄清。
- 超时治理（现状问题 3）：**方向正确**。`executeToolWithTimeout` 已把 timeout ctx 传给 `tool.Execute`（`agent_tools.go:67`），LLM HTTP 层会中断（F16 验证）；"子代理内部 bash 等进程型工具不听 ctx"设计已自认残留（§7.3，`pkg/shell/shell.go:324` `runner.Run(ctx, line)`——mvdan interp 的 ctx cancel **会**停执行器循环，但外部子进程未必被 kill，属已知残留）。**根治程度判定：对 LLM token 烧录有效，对进程型工具有限**——设计措辞应把"根治"降级为"显著缓解"。上下文链路：`Session.executeWithInput` 的 `turnCtx`（`session.go:342`）→ `delegate_to_agent` 工具 ctx → 子代理 `Execute(ctx)` → engine `enrichContextWithConfig`（`agent_execution.go:428-442`）→ LLM。**链路完整**，B1 spawn 派生自父 session ctx 即可。

### 可行性 / 改动面
- A 阶段改动面（§5.1 表）：`result.go` 增 + `subagent.go` 改 + `manager_subagent.go` 改 + `config.go` 加字段 + 测试——**与设计一致，可 S1 独立 commit**。唯一遗漏：**缓存（B1）**。
- 接口签名：`SubagentTool.Execute` 返回 `*DelegateResult` 是唯一破坏性变更，设计已 grep 确认仓库内消费方仅 `manager_subagent.go:212` 自身 + `factory.go` 包装层（`delegateParentMemoryTool` 透传 `Execute`，`factory.go:227-240`）。**补充**：`dino/client.go:410` `onToolResult` 把 `ToolOutput`（`interface{}`）透传给 harness（`examples/dino/main.go:164` 用 `%v`/truncate 打印），信封 JSON 会让 UI 打印变长但**不崩溃**——属可接受的展示层变化，实现时看一眼。
- 迁移顺序 S1→S5 每步独立可验证：**成立**（S2 单独验证需要手动设短超时观察 cancel，S1 全绿后可插队）。回滚：A 阶段破坏性变更回滚 = 恢复裸字符串返回 + 清缓存，成本低。

### 覆盖度
- 任务要求的问题（裸字符串 / 阻塞无通知 / 超时孤儿）**都覆盖到**，且 roadmap（send_message/followup_task/list_agents/持久化拓扑）与评估 13.3 一致。评价报告 13.3 要求的"完成通知推送"被拆成 B1(回发)+B2(唤醒)两段，**语义上满足**。
- 设计遗漏（本报告补充）：缓存毒化（B1）、session 关闭不回收 spawn 子代理（B2）、error 路径无信封（R1）、`SanitizeToolResult` 截断副作用（O3）。

### 风险
- **最高风险阶段：S4（B2）**。它动 `Session.run` 的 select 结构（`session.go:266-291`），直接影响所有现有 turn 调度/队列/planner 行为；且 B2 的"mailbox 注入下一 turn"与用户输入/queue item 的优先级要竞争（设计 §11 风险 6 已列）。**建议：B2 独立 PR，与 send_message 一起做但作为两个 commit**。
- **树级并发 + budget 共享**（评审要求第 4 点）：**设计明确"不做"**（§12.4 列 roadmap）。现状是父/子共享 `llmProvider`/budget（评估 13.1 已指出"隔离薄弱"）。本设计的 `MaxConcurrentSpawns` 只限并发数，**不限树级 token**——并发 4 个子代理各跑满 3 分钟可烧掉 4×(~10k tokens)×多次迭代。对 P1 可接受（评估把它列 P2），但建议在 §5.4 的 `SpawnTimeout` 之外给子代理加一个**单次委派 token 预算**（或复用父 `RemainPromptTokens`），成本很低、防呆价值高。

### 工作量估计
- 设计的"S1 中等 / B1 中等 / B2 大"估计**总体合理**，但 `files_changed` 双路采集（R3）和 mailbox/session 生命周期（R5）建议各 +0.5~1 人日。测试清单（§9）相当完整（19 单测 + 4 集成），是亮点。

---

## 6. 总体评价

- **优点**：事实核查扎实（18+ 项引用几乎全对）；"信封内嵌裸字符串"兼容策略经代码验证成立（`FormatToolResult` JSON 化 + `output` 字段保留）；mailbox 与 eventbus 分工的论证（§4.5 三条理由）正确且与 codex 实现对照准确；分阶段 A→B1→B2 的验证边界清晰；测试清单是同类设计里少见的完整。
- **主要不足**：三个"脱离代码现实的乐观假设"——缓存毒化（B1）、session 关闭不回收 spawn（B2）、B1 完成通知的惰性消费（R6）。前两个是 BLOCKER，第三个是 roadmap 排序依据。
- **进入实现条件**：S1（A 阶段）**可以直接开工**（B1 的缓存修法成本极低，一并做掉即可）；S3（B1）启动前必须补 B2 的 session 回收钩子；B2 独立 PR。

**一句话结论**：**可以进入实现；从 S1 开始，先把 delegate 返回信封 + `no_cache` 一起落，再 S2 超时治理，B1 前补齐 session 回收；B2 与 send_message 同 PR 但分 commit。**

---

## 附录：与 codex 参考实现的对照核实

| 设计引用 | codex 实际 | 核查 |
|---|---|---|
| `session_prefix.rs:9-13` 1000-token 截断 | `COMPLETION_MESSAGE_MAX_TOKENS=1000`、`ERROR_NEXT_ACTION` 文案在 `core/src/session_prefix.rs:10,13` | **一致**（文案原文核实） |
| `session_prefix.rs:19-36` 完成消息格式 | `format_inter_agent_completion_message` 在 `session_prefix.rs:16-`，`InterAgentCompletionMessage::new(...).render()` | **一致** |
| `inter_agent_message.rs:62-70` 信封 | `body()` 在 `inter_agent_message.rs:62-`，格式 `Message Type: {}\nTask name: {}\nSender: {}\nPayload:\n{}` | **一致** |
| `control.rs:569-659` completion watcher | `maybe_start_completion_watcher` 在 `core/src/agent/control.rs:569-660`；`trigger_turn=false` 注入（`control.rs:639-646`） | **一致**（还看到 V2 分支走 `inter_agent_communication` 注入、非 V2 走 `inject_fragment_without_turn`） |
| `handlers.rs:81-96` inter_agent_communication | `trigger_turn || has_outstanding_durable_sleep` → `maybe_start_turn_for_pending_work` | **一致** |
| `multi_agents_spec.rs:731` 报告文件路径 | "list the file paths it changed in the final answer" 在 spec 731 附近 | **一致** |
| `wait.rs` 阻塞订阅 | `timeout_at(deadline, futures.next())`，`DEFAULT_WAIT_TIMEOUT_MS` | **一致**（cortex 版需补 ctx.Done 分支，见 §4） |
| `role.rs:1-4` restrict-only | `core/src/agent/role.rs:1-` 注释"never replace the parent session's authority" | **一致** |

> 结论：设计对 codex 的引用全部可查、语义准确；`control.rs` 的 watcher 还比设计描述的更细（V2/非 V2 双路径），cortex 实现 B1 时不必照搬，mailbox 方案更贴合现有模型。
