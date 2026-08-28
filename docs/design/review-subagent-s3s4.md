# 子代理 S3/S4 设计评审报告

> 被评文档：`docs/design/subagent-s3s4.md`（P3.3 子代理 S3 mailbox+spawn/wait+notifier，S4 B2 turn 唤醒）
> 评审基准：worktree HEAD `c54429f`（= 设计文档本身所在 commit；设计 §1 自称基于 `5d427a1`，但 S3/S4 设计文档本身已在其上）
> 评审类型：设计评审（不改业务代码，仅产出本报告）

---

## 0. 结论摘要

**可以进入实现，但必须先解决 3 个 BLOCKER，且强烈建议按「S3 独立、S4 独立 commit」分步落地。**

- **3 个 BLOCKER**（必须先改再落地）：
  1. **import cycle**：`CompletionNotifier` 签名用 `*dino.Bus`，`dino/agent` 不 import 根 `dino` 包，而根 `dino` 包 import `dino/agent` —— 直接编译失败。notifier 的 Bus 必须剥到 `dino/factory.go` 层（§6.2 类型里不要 `*dino.Bus`，或把 notifier 拆成纯数据 + 由 factory 注入一个 `func(BusEvent)` 回调）。
  2. **`DrainAll` 适配器与 `wait_agent` 的 `Drain` 竞态**：§7.2 声称"turn 内 session 不在 select 循环"结构性关闭竞态，**不成立**。wait_agent 的工具 goroutine 与 factory 适配器的 `SubscribeAll` 回调**同时在跑**：子代理完成 → `Put` → 两个消费方（wait_agent 的 `Drain(taskID)` 与适配器的 `DrainAll()`）竞争同一 mailbox 条目。wait_agent 拿不到 → 返回 `"no result"` 假超时，且唤醒 payload 被吞。必须明确消费仲裁（见 BLOCKER 2 的具体方案）。
  3. **wait_agent 与 `executeToolWithTimeout` 的 timeout 交互断言错误**：默认引擎 `ToolExecutionTimeout = 60s`（`config.go:160`），而 wait_agent 默认 `timeout_ms = 180s`。engine 层的 60s timeout 先触发，`executeToolWithTimeout` 的 `case <-ctx.Done()`（`agent_tools.go:74-78`）**先于** wait_agent 内部的 `case <-ctx.Done()` 返回，父代理拿到的是 `EC_TOOL_EXECUTION_TIMEOUT` **tool error**，不是设计预期的 `{timed_out:true}` 信封。设计 §5.2 的"wait_agent 主动返回 nil error 后 engine select 先收到 result 通道"在 engine timeout 更短的场景**不成立**。

- **强烈建议 RECOMMENDED**：
  - S4 的 `wake_on_completion` 独立开关（设计已列遗留点 2，建议默认 false 灰度）。
  - `notify` 回调同步触发导致 notifier 阻塞：`SubscribeAll` 回调里 `s.ch <- payload`（buffer 8）在完成密集时会阻塞子代理 goroutine（`close(done)` 在 Notify 之后），违背 §5.1"Done = 已完成且已回发"与 §6.2"不得阻塞"。适配器侧必须用 select+drop 或 buffered+非阻塞。
  - `Event.Source` 字段的消费方核对：`dino/client.go:396` 与 `dino/session/turn_observe.go:86` 按 `EventTypeMessage` 直接消费 `Content`。唤醒 turn 注入会以用户消息形态出现在这两个消费方，仅靠 `Source` 字段而这两个消费方不读它的话，"幽灵用户消息"并未被过滤。需同步改这两个消费方或用 displayContent 标记。

- **事实核查**：14 条引用中 **11 条准确（PASS），3 条不准确（FAIL）**。总体事实质量高，行号/函数名基本精确；主要问题集中在 S4 的调度设计与 notifier 的包依赖两处。

- **工作量估计**：S3a-S3d 合计约 3-4 人日（mailbox+spawn/wait+notifier+factory 接线），S4a-S4b 约 2-3 人日（含 wake 适配器与消费方改动）。与设计自估（"S3 中 / S4 大"）相当，但需在 S4a 计入消费方（client.go / turn_observe.go）改动与优先级灰度开关。

---

## 1. 事实核查（逐条 PASS/FAIL）

| # | 设计引用 | 核查结论 | 证据 |
|---|---|---|---|
| 1 | `result.go:18-47` `DelegateResult` 字段清单 + `DelegateResultFromResult`(54-94) + `NewErrorDelegateResult`(97-99) + `Truncated`(104-131) + `DefaultDelegateTruncatedRunes=2000`(50) | **PASS** | 与 `dino/agent/result.go` 逐字段核对一致；`Truncated` 的错误态回退文案在 `result.go:119-123`。 |
| 2 | `subagent.go:125-202` `Execute` 每次新建 engine + `statusFromStreamError`(206-217) 折叠 timeout/cancelled | **PASS** | `subagent.go:130` `engine.NewAgentEngine(...)` 每次新建；`statusFromStreamError` 在 `206-217`。 |
| 3 | `manager_subagent.go` `sessionCancels` map 分桶(28)、`CloseSession`(96-98)、`registerSubagentCancel`(101-116)、`unregisterSubagentCancel`(119-131)、`cancelSessionAll`(134-150) | **PASS** | 逐行核对一致。 |
| 4 | `dino/factory.go:721` `CloseSession` 调 `subagentManager.CloseSession(sessionID)` | **PASS** | `dino/factory.go:710-723` 确认，且在 `f.mu.Lock()` 持锁下调用。 |
| 5 | `info.go:138-153` `SubagentConfig` 含 `NotifyCompletion`(默认 true)/`CompletionMaxRunes`(默认 2000)/`DelegateReturnMode`；`config.go:245-251` 默认值 | **PASS** | `dino/agent/info.go:138-153`、`dino/config.go:245-251` 一致。 |
| 6 | `SubagentTool`(`manager_subagent.go:152-245`) schema 含 `agent` enum `["general"]` + `task` + `task_id`；`Metadata` no_cache(190-199)；string 模式(219-230) | **PASS** | `manager_subagent.go:168-199`、`207-245` 一致。 |
| 7 | 注册：`factory.go:630-645` `NewSubagentTool` + `ReplayToParentMemory` 包装 + Wrap 链 | **PASS** | `dino/factory.go:630-645` 一致。 |
| 8 | `session.go:89-110` `Session` 持 input/output/queue/agent/ctx；`run()`(255-292) 三路 select（ctx.Done / input / queueChan）；一次只跑一个 turn | **PASS** | `session.go:89-110`、`255-292` 一致。 |
| 9 | `executeWithInput`(334) turnCtx 派生 + `s.agent.ExecuteStream`(409)；工具调用 turn 内阻塞（`agent_tools.go:44-80`） | **PASS** | `session.go:334-351`、`agent_tools.go:44-80` 一致。 |
| 10 | 包依赖方向：`dino/session` 不 import `dino/agent`，`dino/agent` 不 import `dino/session`，经 factory 组装 | **PASS** | 全仓 grep 确认（`dino/session/*.go` 无 `dino/agent`；`dino/agent/*.go` 无 `dino/session`）。**但注意 §6.2 的 `CompletionNotifier` 打破了这条**（见 FAIL 12）。 |
| 11 | 现有测试基建：`manager_subagent_test.go:11-112` mock + `result_test.go` 扩展 mode | **PASS** | `manager_subagent_test.go:11-112` 有 `subagentMockLLMProvider`/`subagentMockFactory`；`result_test.go:358-441` 有 `newTestSubagentManager(WithMode)`。 |
| 12 | `CompletionNotifier` 构造注入 `*dino.Bus`（`bus.go:15-23`），实例级，不碰 `GetGlobalBus` | **FAIL** | `*dino.Bus` 定义在根 `dino` 包（`bus.go:15-23`）。`dino/agent` 目前不 import 根 `dino`；根 `dino` 包 import `dino/agent`（`factory.go:23`、`config.go:9`）。若 `dino/agent/completion.go` 引 `*dino.Bus`，形成 **`dino → dino/agent → dino` 环**，Go 编译报 import cycle。§12 风险 7 只防范了"session 依赖 agent"方向，漏了 notifier 这处。 |
| 13 | §5.2 wait_agent 的 `case <-ctx.Done()` 返回 `{timed_out:true}` 而非 engine tool error；engine `ctx.Err()` 分支(`:74-78`)只在 tool 没返回时触发 | **FAIL** | 设计对 `executeToolWithTimeout` 的 **select 竞争**理解有误。`agent_tools.go:71-79`：engine 层 `select` 同时监听 `resultChan` 与 `ctx.Done()`。默认 `ToolExecutionTimeout=60s`（`dino/config.go:160`）**短于** wait_agent 默认 `timeout_ms=180s`。60s 时 engine 的 `ctx.WithTimeout`（`agent_tools.go:48`）先 cancel → engine select 走 `ctx.Done()` 返回 `EC_TOOL_EXECUTION_TIMEOUT` 错误。wait_agent 工具 goroutine 内部的 `case <-ctx.Done()` 此刻**尚未触发**（它继承的是同一个已 cancel 的 ctx，理论上会触发，但 engine 的 select 已经先返回了）——父代理拿到 tool error 而非 `{timed_out:true}`。设计想依赖的"wait_agent 先返回 nil error 所以 engine select 走 result 通道"只在 wait_agent 内部 timeout < engine timeout 时成立。 |
| 14 | §7.2 "turn 内 session 不在 select 循环，DrainAll 竞态窗口由结构性质关闭" | **FAIL** | 竞态的双方不是"session select 循环"与 wait_agent，而是 **factory 适配器的 `SubscribeAll` 回调 goroutine** 与 **wait_agent 工具 goroutine**。两者都响应 `Put` 触发的回调、都操作同一 mailbox。子代理完成 → notifier `Put` → `SubscribeAll`（适配器 `DrainAll`）与 wait_agent（`SubscribeOnce` 回调 `Drain`）**同时**被触发。若适配器先跑，`DrainAll` 取走消息 → wait_agent 的 `Drain(taskID)` 返回 nil → 走 §5.2 的 `"no result"` 假超时；同时该结果也不会进 wake payload。设计 §7.2 只论证了"wakeCh 分支只在 idle 时服务"，没有论证两个**并发消费方**在同一 mailbox 上的互斥。 |

**补充事实观察（设计未引用但影响评审结论）：**

- **O-1**：engine 工具执行是**并行的**（`agent_execution.go:465` errgroup + `g.Go`），且 `ToolParallelismLimit` 可配。同一 turn 内多个 `wait_agent`/`spawn_agent` 会真正并发跑——fan-out 成立，但也意味着多个 wait_agent 工具 goroutine 同时阻塞，全在各自的 `executeToolWithTimeout` 60s 外壳内。
- **O-2**：`executeToolWithTimeout` 超时后**工具 goroutine 仍继续运行**（`agent_tools.go:41-42` 注释自认 trade-off）。wait_agent 在 engine 60s 超时后，其内部的 `time.After(180s)` / `case <-ctx.Done()` 分支仍可能稍后返回并**写入已返回的 resultChan**（有缓冲 1，不会阻塞），但父 LLM 已拿到 60s 超时错误。设计 §12 风险 2 只覆盖了"wait_agent 自身 timeout 与 engine timeout 关系"，没有覆盖默认值 60s < 180s 的常态。
- **O-3**：`EventTypeMessage` 的消费方是 `dino/client.go:396`（`hb.onMessage(event.Content)`）与 `dino/session/turn_observe.go:86`（`obs.AssistantText += ev.Content`）。这两处**只读 `Content`，不读 `Source`**。S4b 给 `Event` 加 `Source` 字段后，若不改这两处消费方，唤醒注入文本仍会以"用户消息/助手消息"形态出现在这两个消费方——`Source` 字段对现有消费方**没有过滤效果**。§7.7 只说"消费端识别后可不渲染或折叠"，没有列出需要改的消费方清单。
- **O-4**：`notify`/`SubscribeAll` 回调在 `Put` 的锁内被同步触发（设计 §5.3 明示"Put 在锁内查订阅者"），而适配器的回调里执行 `DrainAll` + `s.ch <- payload`。`s.ch` buffer 只有 8，完成密集时**阻塞 notifier → 阻塞子代理 goroutine**（`close(done)` 在 Notify 之后，§5.1 步骤 4）。设计 §6.2 声明"Notify 不得阻塞"，与自己的回调实现冲突。
- **O-5**：`subagent.md` §3.3 的 `AgentPath` 已含 `Parent()` 方法，设计 §9 的新 `path.go` 签名只写了 `Join`/`String`，**遗漏 `Parent()`**（S3 若按新签名实现，会丢掉 subagent.md 铺路的方法）。另外设计 §9 说 `DelegateResult` 加 `AgentPath`/`ParentPath`，但 `result.go` 的 `Truncated()` 现在硬编码 `Sender: /root/...`（`result.go:115`），S3 加字段后需同步改 `Truncated` 用 `AgentPath`，设计未提。

---

## 2. 评审维度

### 2.1 正确性

**现状问题是否真被解决：**
- **S3-mailbox**：解决了。每 session 实例 + `Drop` + cap + TTL，纠正了上轮评审 R5 的"挂 SubagentManager 跨 session 共享"矛盾。键=task_id、`Drain` 只读一次，语义正确。✔
- **S3-spawn/wait**：机制正确（task_id = `DelegateResult.TaskID` 的自然衔接是亮点），但 wait_agent 的 timeout 交互有硬伤（FAIL 13）。子代理 `cfg.Timeout = 3min`（`subagent.go:261`）与 spawn watchdog 3min 双保险合理。
- **S3-notifier**：mailbox + eventbus 分工延续 S1 结论正确，**但 `*dino.Bus` 引用是编译级 BLOCKER**（FAIL 12）。
- **S4-B2**：方向正确（mailbox 到达 → 新 turn），`WakePayload` 纯文本注入避免 session 依赖 agent 是正确设计。**但消费仲裁竞态未解决**（FAIL 14），且未明确"唤醒 turn 在 queue 模式下"与预算/planner 的交互。

**逻辑漏洞 / 边界：**
- 唤醒 turn 未检查 `budget.CanExecute` 语义（`session.go:353` 只在 executeWithInput 内查，唤醒 turn 同样会查——但若预算已耗尽，唤醒 turn 每次都会 emit "Budget exceeded" 错误事件，可能刷屏。设计未讨论）。
- `onSubagentCompletion` 只在 `s.wake == nil || !s.IsRunning()` 时 return（§7.5），但 `IsRunning()` 在 session `Stop()` 后为 false——停止的 session 不会处理唤醒，正确。但 `Stop()` 与唤醒到达的竞态（Stop 后 mailbox 仍被 notifier Put）未覆盖：结果落 mailbox 但无 session 消费，靠 `Drop` 兜底，可接受但设计未写明。
- §7.4 "唤醒可重入、不丢消息"依赖 `DrainAll` **未消费**时消息留 mailbox。但适配器 `DrainAll` 在 `Put` 回调里**立即执行**（§7.2 的 `SubscribeAll(func(){ DrainAll ... })`），一旦回调触发，消息即被取走、不可重入——**与 §7.4 的重入承诺矛盾**。真正"可重入"的前提是适配器**不在回调里 DrainAll**，而是 session 端在 `onSubagentCompletion` 里 Drain。这是设计内部逻辑不一致。

### 2.2 可行性

- **改动面**：S3 集中在 `dino/agent`（新文件 mailbox/completion/path/tools_spawn）+ `dino/factory.go` 接线；S4 动 `dino/session`（wake.go + session.go select + event.go）+ `dino/factory.go` 适配器 + **两个现有消费方**（client.go / turn_observe.go，设计未列）。S4a 独立 commit 可回滚的判断正确。
- **接口签名**：`NewSession` 加 `wake WakeSource` 参数，唯一调用点 `factory.go:696`，改动可控。但 `Session` 新增 `wake` 字段后，所有现有 `NewSession` 测试/调用方都要更新——grep 确认只有 factory 一处调用，OK。
- **迁移顺序**：S3a→S3d→S4a→S4b 分步验证清晰，每步独立可测。**但 S4a 与 S4b 的边界设计（Event.Source + 消费方改动）必须一起提交**，否则 S4a 落地的唤醒 turn 会把 FINAL_ANSWER 文本喷到现有 UI（client.go 当用户消息显示）。
- **能否回滚**：S4a 独立 commit 可 revert；但 S3 的 mailbox 实例化在 `CreateSession` 里（设计风险 8），S3d 一旦上线，`CloseSession` 的 `Drop` 与 factory 的 `sessionMailboxes` 表必须同步回滚，否则泄漏。设计未给"回滚配对"说明。

### 2.3 覆盖度（对照评审重点）

| 评审重点 | 设计是否答到 |
|---|---|
| 事实核查引用准确性 | 11/14 PASS，3 处 FAIL（见 §1） |
| mailbox 独立可注入 / TTL/cap / session 关闭 Drop / 消费时机 | ✔ 全部答到，TTL 默认不开有说明（遗留点 3） |
| spawn_agent schema / task_id / wait 阻塞语义 + ctx.Done | schema 与 task_id 答到；**ctx.Done 交互答错**（FAIL 13） |
| completion notifier 回发（eventbus vs mailbox）+ 截断 1000 tokens + 失败回退 | ✔ 答到，但 `*dino.Bus` 引入 cycle（FAIL 12） |
| B2 唤醒（唯一动调度）— 优先级 / 分支 / 风险 | 分支与优先级语义答到；**消费仲裁竞态与重入承诺矛盾未答到**（FAIL 14 + §2.1） |
| 与 delegate_to_agent 关系 / 兼容 | ✔ 保留策略清晰（§8 表格完整） |
| 测试清单 | ✔ 相当完整（21 单测 + 5 集成），但缺少：唤醒 turn 的 budget 耗尽 / client.go 消费方过滤 / notifier 阻塞的回归测试 |

### 2.4 风险

- **上线风险**：S3 独立落地风险低（不碰调度）；S4 动 `Session.run` select + 唤醒注入，影响所有现有 turn/queue/planner 行为——设计已列风险 6 并给 S4a 独立 commit，但**优先级灰度开关（wake_on_completion 默认 false）设计只列在遗留点 2，未做进 S4a 默认值**，这是上线前必须拍板的。
- **兼容性**：`Event.Source` 默认 `user` 向后兼容（字段缺失 JSON 为 omitempty 则不存在——需确认 Event 序列化不带 omitempty 时旧消费方是否报错，`event.go:27-42` 无 Source，新增后 `json:"source,omitempty"` 即可）。
- **对现有行为影响**：S3d 给所有 session 建 mailbox + notifier，`CloseSession` 多一次 `Drop`，开销极小。S4 唤醒 turn 是全新行为，默认关则零影响。

### 2.5 工作量估计

- S3a（path+mailbox 无订阅+单测 1-5）：0.5-1 人日
- S3b（completion+Spawn+cancel 接线+单测 6-9,15-18）：1-1.5 人日
- S3c（tools_spawn+SubscribeOnce+单测 10-14+集成1）：1 人日
- S3d（factory 接线+CloseSession Drop+集成 1,4,5）：0.5-1 人日
- **S4a**（wake.go+适配器+Session.run wakeCh+onSubagentCompletion+单测 19-20+集成 3）：**1.5-2 人日**（含消费仲裁竞态修复与消费方改动）
- **S4b**（Event.Source+displayContent 标记+单测 21+client.go/turn_observe.go 消费方）：0.5-1 人日
- **合计：约 5-7.5 人日**，与设计自估的"S3 中 / S4 大"一致，但 S4a 需计入竞态修复与消费方改动，比设计自估略高。

---

## 3. BLOCKER（必须先改再落地）

### BLOCKER 1 — `dino/agent` 引 `*dino.Bus` 造成 import cycle

- **位置**：`subagent-s3s4.md` §6.2（`CompletionNotifier` 类型签名 `bus *dino.Bus`）、§9（改动文件表 `dino/agent/completion.go`）。
- **证据**：`dino/agent` 全仓无 import 根 `dino`（grep 确认）；根 `dino` 包 import `dino/agent`（`dino/factory.go:23`、`dino/config.go:9`）。`completion.go` 引 `*dino.Bus` → `dino → dino/agent → dino` 环，Go 编译失败。
- **修法**：两种，任选其一：
  1. `CompletionNotifier` 不持 `*dino.Bus`，改持 `func(eventType string, sessionID string, data interface{})` 回调（函数类型可定义在 `dino/agent`，不引根包），factory 注入 `dino.Bus.Publish`。或
  2. 把 notifier 拆成"纯数据投递（mailbox.Put）+ 由 factory 侧订阅 mailbox 发事件"，`dino/agent` 完全不含 Bus 类型。
- **影响**：§6.2 类型签名、§9 接口签名汇总、§10 单测 16（"用 `NewBus()` 构造的实例"——单测在 `dino/agent` 包内，同样引不到根 `dino`）都要跟着改。

### BLOCKER 2 — `DrainAll` 适配器与 `wait_agent.Drain` 消费仲裁未定义

- **位置**：§7.2（`sessionWakeSource` 的 `SubscribeAll(func(){ DrainAll ... })`）与 §5.2（wait_agent 的 `Drain(taskID)`）。
- **证据**：`Put` 触发订阅回调（§5.3），适配器 `SubscribeAll` 与 wait_agent `SubscribeOnce` 是**两个独立消费方**，都响应同一 `Put`。§7.2 的"结构性关闭竞态"论证只针对 session select 循环，没有针对两个并发消费方。子代理完成时若 wait_agent 正阻塞（turn 内），适配器回调 `DrainAll` 先跑 → wait_agent `Drain(taskID)` 返回 nil → `"no result"` 假超时（§5.2 分支），且该结果不进入 wake payload——结果既没被 wait_agent 消费，也没被唤醒注入。
- **修法**：明确**唯一消费者**。建议：wait_agent 是**唯一** `Drain(taskID)` 消费者；适配器**不 DrainAll**，只把"有消息"通知给 session，session 在 `onSubagentCompletion` 里才 `DrainAll`（同时这也修复了 §7.4"重入"与 §2.1 的矛盾）。或者反过来：wait_agent 只 `Peek` + 等 channel，适配器统一 `DrainAll` 并注入，wait_agent 从"已注入的 turn"里拿——但这样 wait_agent 与唤醒会重复注入。**推荐前者**（wait_agent 独占 Drain，适配器只做通知+ session 端 Drain）。
- **测试**：新增 `TestWaitAgent_AndAdapter_Concurrent`（spawn 两个，一个 wait_agent 阻塞，一个走唤醒路径，`-race`），确保无假超时、无丢结果。

### BLOCKER 3 — wait_agent 默认 timeout 交互：engine 60s 先于 wait 180s

- **位置**：§5.2 wait_agent 阻塞语义 + §12 风险 2。
- **证据**：默认 `ToolExecutionTimeout = 60s`（`dino/config.go:160`）；wait_agent 默认 `timeout_ms = 180s`（§3.3 schema）。engine `executeToolWithTimeout` 用 `ctx.WithTimeout(ctx, toolTimeout)`（`agent_tools.go:48`）包住工具，`select` 在 `resultChan` 与 `ctx.Done()` 间竞争（`agent_tools.go:71-79`）。60s 到 → engine select 走 `ctx.Done()`，返回 `EC_TOOL_EXECUTION_TIMEOUT` **错误**，父代理看到 tool error，不是 `{timed_out:true}` 信封。设计 §5.2 的"wait_agent 主动返回 nil error，engine 先收 result 通道"只在 `wait 内部 timeout < engine timeout` 时成立，而默认值恰恰相反。
- **修法**：wait_agent 的**有效等待上限必须 < engine 工具超时**。具体：在 factory 注册 wait_agent 工具时，给 `ToolTimeoutCalculator`/`ToolTimeouts` 设一个**高于** wait 内部 timeout 的值（如 `ToolTimeouts["wait_agent"] = 200s` > 180s），或把 wait 默认 timeout 降到 engine 默认 60s 以下。两种都必须在文档写明依赖。设计 §5.2 说"外层 executeToolWithTimeout 已包一层 timeout"是现状描述，但没意识到**谁先到**决定了返回形态。
- **测试**：`TestWaitAgent_CtxDone`（单测 12）要改为验证"wait 内部 timeout < engine timeout 时返回信封、> 时返回 tool error"，断言设计选择的行为。

---

## 4. RECOMMENDED（落地时采纳）

1. **`wake_on_completion` 灰度开关**（§15 遗留点 2）：建议默认 **false**，显式开启才启用 B2 唤醒。设计 §7.3 直接让 `wake != nil` 即启用，无开关——S4 作为"唯一动调度"的一刀，必须默认关、灰度开。`config.go` 加 `WakeOnCompletion bool`（默认 false），factory 只在 true 时构造 `sessionWakeSource`。
2. **适配器回调非阻塞**（§2.2 的 O-4）：`SubscribeAll` 回调里 `s.ch <- payload` 需 `select + default` 或足够大缓冲 + 丢弃策略，否则完成密集时阻塞 notifier → 阻塞子代理 goroutine。补回归测试 `TestAdapter_DoesNotBlockNotifier`。
3. **消费方改动与 S4a 同 commit**：`dino/client.go:396`、`dino/session/turn_observe.go:86` 需识别 `Source == "subagent"` 并过滤/折叠，否则 S4a 一上线唤醒文本就喷到 UI 与 assistant-text 统计。§7.7 应把这两个文件列为改动清单。
4. **`Truncated()` 与 `AgentPath` 联动**：S3 给 `DelegateResult` 加 `AgentPath` 后，`result.go:113-116` 硬编码 `Task name: /root` / `Sender: /root/...` 应改用 `AgentPath`/`ParentPath` 字段，否则信封文本与实际树结构脱节。补 `TestTruncated_UsesAgentPath`。
5. **`path.go` 补 `Parent()`**：`subagent.md` §3.3 已定义 `Parent()`，设计 §9 新签名遗漏。按 subagent.md 实现保持一致。
6. **budget 耗尽时唤醒 turn 的节流**：`executeWithInput` 在预算耗尽时 emit `EventTypeError`（`session.go:353-359`）。唤醒 turn 若反复触发会在 UI 刷"Budget exceeded"。建议 `onSubagentCompletion` 在注入前查一次 `budget.CanExecute(s.id)`，不满足则直接 `Drop` 该 payload。
7. **`Session.run` 的 `wakeCh` 分支加 nil 保护之外还要加容量保护**：`wakeCh` 是 `<-chan`，session 关闭后 `Wake()` 通道不会关闭（适配器 goroutine 生命周期未定义）。设计 §7.3 没有给适配器 goroutine 的退出路径——session `Close()` 时应通知适配器停（否则 goroutine 泄漏，`TestSessionClose_DropsMailbox` 只断言 mailbox 不断言 goroutine）。补适配器 `Close()`/`Stop()` 并在 factory `CloseSession` 调用。
8. **S3d 回滚配对说明**：mailbox 实例化 + `CloseSession` Drop + `sessionMailboxes` 表是**三个必须同滚**的改动，设计 §11 未说明回滚配对。建议在 §11 标注"S3d 内部不可拆分回滚"。

---

## 5. OPTIONAL

1. **`NotifyCompletion:false` 时的行为**：`config.go:248` 默认 true，但 S3 落地后若用户设 false，是"mailbox 不 Put"还是"Put 但事件不发"？设计 §6.2 未接 `NotifyCompletion` 开关。建议明确：`NotifyCompletion=false` → notifier 直接跳过（不 Put 不发事件），与 S1 语义一致。
2. **`spawn_agent` 的 `model` 字段**：S3 仅支持继承（风险 9），schema 里仍暴露 `model` 字段会让模型尝试传值然后拿到 error。建议 S3 阶段从 schema 里去掉 `model`，等真正支持时再加，避免模型困惑。
3. **`task_name` 校验**：schema 允许任意字符串，roadmap `list_agents` 要用它作显示名。建议 S3 起就限制长度（如 ≤64）并记录到 mailbox entry。
4. **eventbus `SlotOnce`/`Unslot` 的 `isSameHandler` 问题**：上轮评审 R4 提到 `eventbus.go:642-648` 反注册失效。本设计 notifier 用 `Subscribe` 不反注册，规避了它；但适配器 `UnsubscribeAll` 若实现有误可能残留回调。建议 `UnsubscribeAll` 走"按 id 分桶删除"而非 handler 比较。

---

## 6. 总体评价

**设计质量：高，但 S4 的调度设计是薄弱点。**

- **优点**：
  - 事实基础扎实——14 条引用 11 条精确命中，行号准确到函数级，说明设计者对照代码逐行核对过，这在多份设计文档里是最高的。
  - 上轮评审的 BLOCKER/R5/R6 全部被认真消化：mailbox 独立可注入 + 每 session 一个、session 关闭 Drop、B2 作为 S3 价值兑现点、保留 delegate_to_agent 的兼容策略——都是对的。
  - `task_id` 复用 `DelegateResult.TaskID`、wait 返回 `Truncated` 摘要而非整包信封、`WakePayload` 纯文本注入保持包解耦——三个关键决策都简洁正确。
  - 迁移顺序 S3a→S4a 分步 + S4a 独立 commit，回滚意识好。

- **主要不足（按影响排序）**：
  1. **import cycle（BLOCKER 1）**——最基础的一处，类型签名直接编译失败，但容易被"设计阶段"漏掉，因为它需要跨 `dino/agent` 与根 `dino` 两个包方向验证。
  2. **消费仲裁竞态（BLOCKER 2）**——wait_agent 与唤醒适配器两个消费者争同一 mailbox，是 S3/S4 交汇的核心正确性问题，设计用一句"结构性质天然关闭"带过，实际不成立。
  3. **timeout 交互断言错误（BLOCKER 3）**——默认值 60s < 180s 让设计的 ctx 分支逻辑在常态下不触发，父代理拿到的是 tool error。
  4. **`Event.Source` 只加字段不改消费方**——"幽灵用户消息"问题实际没解决，只是留了字段。
  5. **唤醒可重入承诺与 `DrainAll` 在回调里立即执行矛盾**——§7.4 与 §7.2 自相矛盾。

- **S3 与 S4 是否必须一起做**：**不必，可以分**。S3a-S3d 完全不碰调度（mailbox/spawn/wait/notifier 都是独立组件），可独立落地并全绿；S4 才动 `Session.run`。设计 §11 的分步也支持这点。但要注意：**S3 单独落地时，完成通知是惰性可见的**（R6 已明示），父代理 spawn 后要等下一用户 turn 才看到 mailbox——产品价值有限，所以 S4 才是兑现点。建议排期上 S3 与 S4 之间不要隔太久。

- **能否进入实现**：**修完 3 个 BLOCKER 后可以**。BLOCKER 1 是编译级、改动局部（notifier 签名）；BLOCKER 2 需要定"wait_agent 独占 Drain / 适配器只通知"的仲裁；BLOCKER 3 是 wait_agent 注册时配 `ToolTimeouts` 或在 schema 里约束 timeout 上限。三者都在 S3a/S3c 阶段就能一并解决，不推后 S4。

---

## 7. 评审结论

| 维度 | 结论 |
|---|---|
| 事实核查 | 14 条引用 11 PASS / 3 FAIL |
| BLOCKER | 3（import cycle / 消费仲裁竞态 / timeout 交互断言） |
| RECOMMENDED | 8（开关、非阻塞、消费方、Truncated/AgentPath、Parent、budget 节流、适配器 goroutine 生命周期、回滚配对） |
| OPTIONAL | 4 |
| 是否可进入实现 | **修完 3 个 BLOCKER 后可以**；S3 与 S4 可分步，S4 需独立 commit + 默认关灰度开 |
| 工作量 | S3 约 3-4 人日，S4 约 2-3 人日，合计 5-7.5 人日 |
