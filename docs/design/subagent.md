# 子代理系统改造设计：结果结构化 + 完成通知推送

> 对应评估报告第十三章（`docs/optimization-review-vs-codex.md`）13.3 落地要点表中「**结果结构化 + 完成通知推送给父代理（非裸字符串阻塞）**」，P1 最优先项（第 433 行）。
> 本设计只覆盖这一步 + 治理超时孤儿；send_message/followup_task 队列 vs 唤醒、list_agents/close_agent、持久化拓扑等列为后续阶段 roadmap（见 §10）。
> 设计阶段：**不改任何业务代码**，仅产出本文档。

---

## 1. 现状事实核查（基于当前 worktree HEAD `5d5130c`）

### 1.1 子代理 = 一次性 AgentEngine
- `dino/agent/subagent.go:52-66` `NewSubagent` 要求 `info.Mode == ModeSubagent`，否则报错。
- `subagent.go:113-149` 每次 `Execute` 都 `engine.NewAgentEngine(s.llmProvider, cfg)`（117 行）构建全新 engine——**无跨调用 LLM 状态/记忆**。
- 硬限制常量 `subagent.go:15-19`：`subagentMaxIterations = 50`、`subagentExecuteTimeout = 3 * time.Minute`、`subagentMaxFileBytes = 32 << 10`；`buildConfig`（151-174）设 `EnableMemoryCompress = false`。

### 1.2 工具链
- 唯一多代理工具 `delegate_to_agent`（`dino/agent/manager_subagent.go:155-215`）：
  - Schema 只含 `agent`（enum **硬编码 `["general"]`**，178 行）+ `task`。
  - `Execute`（193-215）同步调 `t.manager.Execute(...)`，返回**裸字符串** `result.Output`（212 行）。
- 自动委派是死代码：`ShouldDelegate`（`manager_subagent.go:68-114`）+ `SubagentHandler`（331-434）从未被构造/调用。活的只有 LLM 主动调 `delegate_to_agent`。
  - 证据：`SubagentHandler` 无任何构造点；`factory.go` 只构造 `NewSubagentManager` + `NewSubagentTool`。
- `delegate_to_agent` 注册进父代理工具集在 `dino/factory.go:593-604`（`NewSubagentTool(f.subagentManager)`，若 `ReplayToParentMemory` 再包一层 `newDelegateParentMemoryTool`）。

### 1.3 父代理执行路径（阻塞工具调用）
- 父代理 = `dino/session/session.go` 的 `Session` + 共享 `engine.AgentEngine`（`dino/factory.go:530`）。
- `Session.run()`（`session.go:255-292`）select 两个来源：`s.input` channel 与 `s.queue`（`dino/queue`）。队列 item 由 `processQueueItem`（294-311）同步执行——**同一时刻只有一个 turn 在跑**。
- turn 内工具调用：`agent/engine/agent_execution.go:334-423`（goroutine 池并发跑同层工具）→ `executeToolWithTimeout`（`agent_tools.go:44-80`）。
- **工具返回 → 观察结果**：`buildToolCallResults`（`agent_execution.go:241-277`）把 `tool.Execute` 返回值经 `SanitizeToolResult` + `FormatToolResult` + `TruncateToolResult` 转成字符串 observation 注入下一轮 LLM 消息。**因此工具可以返回结构化 `interface{}`（map），会自动 JSON 序列化注入**（`FormatToolResult`，`types.go:336-351`）。
- 注：`LangChainAgentEngine`（`agent/engine/langchain_engine.go:224`）另有同步 `tool.Execute(ctx, args)` 路径，不经 `executeToolWithTimeout`——但 dino 主路径用 `AgentEngine`（`factory.go:530`），不冲突。

### 1.4 超时治理现状
- `executeToolWithTimeout`（`agent_tools.go:44-80`）：goroutine + channel 实现超时；**goroutine 在超时后继续跑**（41-43 行注释明说这是"可接受 trade-off"）。子代理是异步调用工具内部阻塞等待 → 超时后子代理继续烧 token。
- LLM HTTP 层：`anthropic_native.go:179` 用 `http.NewRequestWithContext(ctx, ...)`，**ctx 取消会传播到 HTTP**；OpenAI/DeepSeek 走 langchain（`agent/providers/langchain_llm.go`），ctx 同样传递。即：**只要传入的 ctx 被 cancel，LLM 请求会中断**。

### 1.5 事件/观察基础设施
- `pkg/eventbus`：`StandardEventBus`（`pkg/eventbus/eventbus.go:95-393`）+ `NewGeneric[T]`（396）+ `GlobalBus` 全局实例（654）。dino 侧已包一层 `dino/bus.go` `Bus`（`NewBus()` 用 `eventbus.NewGeneric[BusEvent]()`）。
- `dino/session/event.go`：`Session` 有 `output chan *Event` + `observers`（`session.go:112-119`），有 `Event` 结构。
- hooks：`agent/hooks/hooks.go`，`AgentEngine.SetHooks`（`factory.go:531-533`）。`OnAfterToolCall` / `OnAfterIteration` / `OnAfterEnd` 可用于观察。

---

## 2. 本任务范围界定

**做（P1 第一优先）：**
1. `delegate_to_agent` 结果结构化：返回信封（output / files_changed / error / status / duration / usage），父代理拿到的不再是裸字符串。
2. 完成通知推送：子代理完成后**主动**把结构化结果发给父代理，父代理通过**自己输入侧的事件到达**知道完成——而非继续依赖阻塞轮询。
3. 超时/孤儿治理：`executeToolWithTimeout` 超时后真正取消子代理（ctx 传播）。

**不做（后续阶段 roadmap，§10 简述）：**
- `send_message`（QueueOnly 不唤醒）vs `followup_task`（TriggerTurn 唤醒）工具族；
- `wait_agent`（订阅 mailbox）→ 本轮用轻量 mailbox 直读 + 注入；
- `list_agents` / `close_agent` / depth limit / 权限交集；
- 持久化 spawn 边 + InteractionEdge trace；
- 自动委派 `ShouldDelegate` 死代码接线或删除（评估 13.3 第二行，极小工作量，可顺手在 §9 收尾）。

---

## 3. 目标数据模型

### 3.1 结构化结果信封（新增文件 `dino/agent/result.go`）

```go
// dino/agent/result.go
package agent

// DelegateResult 是 delegate_to_agent 的返回信封。
// 同时是完成通知推送给父代理的载荷（payload 部分）。
type DelegateResult struct {
    Agent     string   `json:"agent"`                // 子代理名（如 "general"）
    TaskID    string   `json:"task_id"`              // 每次委派生成的 run id（uuid）
    Status    string   `json:"status"`               // "completed" | "error" | "timeout" | "cancelled"
    Output    string   `json:"output,omitempty"`     // 子代理最终文本输出
    FilesChanged []string `json:"files_changed,omitempty"` // 子代理报告改动/涉猎的文件路径
    Error     string   `json:"error,omitempty"`      // 失败时的错误摘要（截断）
    Duration  time.Duration `json:"duration_ms"`     // 墙钟耗时 ms（含队列等待）
    Iterations int     `json:"iterations"`           // 实际迭代数
    Usage     types.Usage `json:"usage"`             // 子代理本次 token 用量
    Timestamp time.Time `json:"timestamp"`
}

// Truncated 返回给父代理 LLM 的紧凑文本视图（1000-token 信封截断，对齐 codex session_prefix.rs:9-13）。
func (r *DelegateResult) Truncated(maxRunes int) string
```

> `Duration time.Duration` JSON 序列化不友好，序列化字段用 `duration_ms int64`。struct 字段类型取 `int64`，文档里写 ms。

### 3.2 兼容策略：**信封内嵌裸字符串**

父代理 LLM 已按"delegate 返回一段文本"的心智建模。为了**不破坏现有提示词/下游行为**，信封同时满足两个消费方：

- **工具返回值**（`delegate_to_agent.Execute` 的 `interface{}`）：返回 `DelegateResult` 结构体。经 `FormatToolResult`（`types.go:336-351`）自动 JSON 序列化注入观察——**父代理模型仍能读到 output 字段，只是多了结构化元数据**。
- **程序化消费方**（若有人 `result.Output` 断言）：`Result.Output` 仍保留为"子代理最终文本输出"；`DelegateResult.Output` 与之等价。对旧工具返回值形态的兼容见 §5.2 的 A 阶段。

### 3.3 AgentPath 雏形（为后续 list_agents/持久化铺垫，本轮只做一层）

```go
// dino/agent/path.go —— 本轮只实现 root→child 一层寻址
type AgentPath string // "/root" | "/root/general"（本轮最多一层）

func RootAgentPath() AgentPath                  { return "/root" }
func (p AgentPath) Join(child string) AgentPath { return AgentPath(string(p) + "/" + child) }
func (p AgentPath) Parent() (AgentPath, bool)   // rsplit("/")
```

代码树每节点持 `AgentPath`，信封带 `ParentPath`/`AgentPath`，完成通知按 `ParentPath` 投递。

---

## 4. 完成通知推送：方案 A vs 方案 B

### 4.1 关键约束（决定取舍）

cortex 父代理执行模型是**严格串行 turn**：
- `Session.run` select 输入/队列，一次只跑一个 `executeWithInput`（`session.go:334`），turn 内 `AgentEngine` 同步跑完整轮直到结束。
- 工具调用在 turn 内是阻塞的：`delegate_to_agent` 若同步执行，父 turn 就卡住等子代理。
- **没有"父代理空闲等待子代理完成"的原语**——除了继续跑别的工具，或者让 turn 结束。

因此在 cortex 的"父 turn 阻塞执行"模型下，"非阻塞 + 通知"若只是把子代理放后台，父代理 turn 结束后就**没有可唤醒的载体**——除非引入"父 turn 中途醒来"（= codex 的 input queue + turn 调度，改动最大）或者走 eventbus/observer 推送 + `wait_agent`（本轮以轻量 mailbox + 注入实现）。

### 4.2 方案 A：保留阻塞，结果结构化（最小改动）

**做法**：`delegate_to_agent` 同步执行，但返回结构化信封；完成通知就是**工具返回值本身**。加"完成观察事件"上报（见 §4.4），但父代理得知完成仍靠"工具返回"。

- 改动文件：`dino/agent/subagent.go`（Result 扩展）、`dino/agent/manager_subagent.go`（返回信封）、`dino/agent/result.go`（新增）。
- 收益：结构化为后续所有多代理能力打地基；观察/审计立即可用；零执行模型改动。
- 局限：仍然阻塞；无 fire-and-forget / 并行 fan-out；父代理无法在子代理跑的时候干别的。

### 4.3 方案 B：异步 + 通知队列（P1 完整版）

在 A 之上加"fire-and-forget + 完成回发"。两层递进：

**B1. 后台执行 + 完成事件（不改父 turn 模型）**
- 新工具 `spawn_agent`：`manager.Spawn(...)` 立刻返回 `{task_id, agent_path}`（不阻塞）。
- 子代理在**独立 goroutine** 跑 `Subagent.Execute(ctx, req)`，ctx 从父 `Session` 派生（取消传播）。
- 完成时调用 `CompletionNotifier.Notify(DelegateResult)`，notifier 干两件事：
  1. 写父代理 mailbox（`dino/agent/mailbox.go`，新文件）；
  2. `dino/bus.go` `Bus.Publish("subagent.completed", sessionID, envelope)`（可选，供 UI/审计消费）。
- 父代理用 `wait_agent`（见 §4.5）读取 mailbox 拿结果。

**B2. 父 turn 唤醒（完整 codex 模型）**
- 完成通知除写 mailbox 外，还向父 `Session` 输入侧注入一条唤醒消息（等价 codex `inter_agent_communication` 的 `trigger_turn=false` 变体：注入但不抢 turn）。
- 需要 `Session` 支持"mailbox 有内容 → 触发新一轮 turn 且上下文注入 mailbox 内容"。**这要求改 `Session.run` 的 select 结构**，是本设计里最重的一刀（改动面见 §8.2）。评估报告 13.3 把它列为 P1，但**前置于 send_message/followup_task** 实现，本轮 B2 作为 P1 的"唤醒"收尾，不做队列/唤醒二分。

> **推荐路径**：先落 **A**（纯结构化，独立可验证），再落 **B1**（fire-and-forget + mailbox + 完成事件），最后在 roadmap 里安排 **B2**（turn 唤醒，与 send_message 一起做）。理由：B2 需要动 `Session.run` 的调度，混入 A 一起改会让"结构化"这步无法单独验证。

### 4.4 方案 A/B 对照表

| 维度 | A 阻塞+结构化 | B1 后台+通知 | B2 +turn 唤醒 |
|---|---|---|---|
| 父代理得知完成 | 工具返回 | mailbox 轮询（`wait_agent` 内建，非 LLM 轮询） | mailbox 到达即注入上下文 |
| fire-and-forget | ❌ | ✅ | ✅ |
| 并行 fan-out | ❌ | ✅（多次 spawn） | ✅ |
| 修改执行模型 | 无 | 无（goroutine 后台） | 改 `Session.run` select + mailbox 注入 |
| 改动文件 | 3 个（2 改 1 增） | +`mailbox.go`+`spawn/wait` 工具+`completion.go` | +`session.go` 注入逻辑 |
| 独立可验证 | ✅ | ✅ | 依赖 B1 |
| 对应 codex | 无 | 对应 completion watcher 的"信封 + 回发"（`control.rs:569-659`） | 对应 input queue 注入（`input_queue.rs`） |

### 4.5 mailbox 设计（方案 B 的核心，轻量版）

**不直接套 `pkg/eventbus` 做父子投递**，理由：
1. `eventbus` 是**无状态发布/订阅**，订阅者在父 turn 之外，无法把事件"注入"父代理的下一个 LLM 上下文（父代理上下文由 `Session` 全权掌控）。
2. mailbox 需要**按 task_id 拉取 + 只读一次**（`wait_agent` 返回后删除），eventbus 的 slot 语义不匹配。
3. 全局 `GlobalBus` 跨会话，父子投递必须按 sessionID 隔离——eventbus 无路由概念。

**用法**：`eventbus` 用于**旁路可观测**（`subagent.completed` 事件给 UI/harness/审计），mailbox 用于**模型可见投递**。二者互补。

```go
// dino/agent/mailbox.go
type Mailbox struct {
    mu sync.RWMutex
    m  map[string][]*DelegateResult // sessionID -> 待读结果（FIFO）
}
func (mb *Mailbox) Put(sessionID string, r *DelegateResult)
func (mb *Mailbox) Drain(sessionID string) []*DelegateResult // wait_agent 用
func (mb *Mailbox) Peek(sessionID string) []*DelegateResult // B2 注入用，不删
func (mb *Mailbox) Drop(sessionID string)
```
- `Mailbox` 挂 `SubagentManager`（或 `dinoFactory`，会话级）。
- `wait_agent` 语义（对齐 codex `wait.rs`）：`timeout_ms` 内阻塞等 `Put`（用 `sync.Cond` 或 channel），超时返回 `{completed: false, message: "timed out"}`；拿到后 **Drain** 该 task_id 的结果，返回结构化摘要。

### 4.6 完成通知信封（对齐 codex `session_prefix.rs:19-36`）

codex 完成消息：`Message Type: FINAL_ANSWER\nTask name: …\nSender: …\nPayload:\n…`，**payload 截断到 1000 tokens**，错误时附"此 agent 的 turn 失败了，如需可再给它任务"回退文案（`session_prefix.rs:13`）。

cortex 版（`DelegateResult.Truncated`）：
```
Message Type: FINAL_ANSWER
Task name: /root
Sender: /root/general
Status: completed
Payload:
<output，runes 上限 2000≈1000 tokens；超限截断加 …(truncated)>
```
错误态：
```
Message Type: FINAL_ANSWER
Task name: /root
Sender: /root/general
Status: error
Payload:
Agent errored: <错误前 N 字>
This agent's turn failed. If you still need this agent, use the available collaboration tools to give it another task.
```
（文案可直接复用 codex 原文，符合其回退意图。）

---

## 5. 接口签名与改动文件清单

### 5.1 改动文件总表（按方案 A→B1→B2 标注阶段）

| 文件 | 阶段 | 改动 |
|---|---|---|
| `dino/agent/result.go` | A | **新增**：`DelegateResult` + `Truncated` + `FilesChanged` 解析 |
| `dino/agent/subagent.go` | A | `Result` 扩字段（`FilesChanged`/`Iterations`/`Duration`/`Status`）；`Execute` 计算它们 |
| `dino/agent/manager_subagent.go` | A/B1 | `SubagentTool.Execute` 返回信封；`SubagentManager` 挂 mailbox/notifier；新增 `Spawn`/`Wait` |
| `dino/agent/path.go` | B1 | **新增**：`AgentPath`（一层） |
| `dino/agent/mailbox.go` | B1 | **新增**：mailbox |
| `dino/agent/completion.go` | B1 | **新增**：completion watcher/notifier（`Notify(DelegateResult)` → mailbox.Put + eventbus.Publish） |
| `dino/agent/tools_spawn.go` | B1 | **新增**：`spawn_agent` / `wait_agent` 工具 |
| `dino/config.go` | A | `agent.SubagentConfig` 加字段（见 §5.4） |
| `dino/factory.go` | B1 | 构造 mailbox/notifier；注册 spawn/wait 工具；父 ctx 派生传给 `Spawn` |
| `dino/session/session.go` | B2 | mailbox 注入到下一 turn 上下文（见 §8.2） |
| `agent/engine/agent_tools.go` | 超时 | `executeToolWithTimeout` 增加 cancel 语义（见 §7） |
| `dino/agent/agent_test.go` / `manager_subagent_test.go` | A/B1 | 测试（见 §9） |

### 5.2 A 阶段接口变更（向后兼容）

**现有导出符号**（外部可能引用，需保持）：
- `type Result struct { Output string; Error error; Usage types.Usage }`（`subagent.go:21-25`）——**保留字段**，新增字段 `Status string`、`Duration time.Duration`、`Iterations int`、`FilesChanged []string`（`omitempty` 语义）。`Output` 语义不变。
- `type Request struct{...}`（27-32）不变。
- `func (t *SubagentTool) Execute(...)`（`manager_subagent.go:193-215`）返回值类型从裸字符串改为 `*DelegateResult`——**这是唯一破坏性签名变更**。兼容：`DelegateResult` 有 `Output` 字段，任何下游把返回值当 `string` 或 `fmt.Sprintf("%v")` 的用法会拿到 JSON——需 grep 确认无外部强断言（见风险 §11.2）。

**`delegate_to_agent` 工具 schema** 扩展（`manager_subagent.go:171-187`）：
```json
{
  "type": "object",
  "properties": {
    "agent":   {"type":"string","enum":["general"],"description":"..."},
    "task":    {"type":"string","description":"..."},
    "task_id": {"type":"string","description":"optional; omit to auto-generate. Reuse to attach to an existing run (B 阶段)."},
    "timeout_ms": {"type":"integer","description":"optional; overrides default 3min for this run (B 阶段)."}
  },
  "required": ["agent","task"]
}
```
新增字段全 optional，`required` 不变 → 老调用方不破坏。

### 5.3 B1 阶段接口

```go
// SubagentManager 新增
func (sm *SubagentManager) Spawn(ctx context.Context, req *Request, parent AgentPath) (*SpawnHandle, error)
// SpawnHandle: { TaskID string; AgentPath AgentPath; Done <-chan struct{} }

// 新工具
type SpawnAgentTool struct{ manager *SubagentManager } // Name()="spawn_agent"
func (t *SpawnAgentTool) Execute(...) (interface{}, error) // 返回 {"task_id":..., "agent_path":...}
type WaitAgentTool struct{ manager *SubagentManager; defaultTimeout time.Duration }
func (t *WaitAgentTool) Execute(...) (interface{}, error) // {"completed":bool,"message":string,"result":*DelegateResult}
```

### 5.4 配置字段（`dino/agent/info.go:138-151` SubagentConfig + `dino/config.go:204-216` 默认值）

```go
type SubagentConfig struct {
    Enabled              bool `yaml:"enabled"`
    TriggerOnKeyword     bool `yaml:"trigger_on_keyword"`
    ReplayToParentMemory bool `yaml:"replay_to_parent_memory"`
    MaxHistoryMessages   int  `yaml:"max_history_messages"`
    // —— 新增 ——
    NotifyCompletion     bool `yaml:"notify_completion"`   // B 阶段：完成是否写 mailbox+发事件，默认 true
    CompletionMaxRunes   int  `yaml:"completion_max_runes"` // 信封截断，默认 2000（≈1000 tokens）
    SpawnTimeout         time.Duration `yaml:"spawn_timeout"` // B 阶段 wait_agent 默认超时，默认 3min
    MaxConcurrentSpawns  int  `yaml:"max_concurrent_spawns"` // B 阶段并发上限，默认 4（对齐 codex max_concurrent_threads_per_session=4）
    Triggers             []SubagentTrigger `yaml:"triggers"`
}
```
默认值写进 `config.go:204-216` 的 `Subagent: agent.SubagentConfig{...}`。

---

## 6. 结果结构化：字段来源与实现细节

### 6.1 `Output` / `FilesChanged` / `Iterations` / `Duration` / `Usage` 从哪来

`Subagent.Execute`（`subagent.go:113-149`）现在只收集 output + usage。改造后：

| 字段 | 来源 |
|---|---|
| `Output` | 现有 `output.String()`（140 行） |
| `Usage` | `lastUsage` + `eng.GetTotalUsage()` 兜底（141-143 行） |
| `Iterations` | `AgentEngine` 迭代计数：`ExecuteStream` 已暴露 `result.Type=="final"`/`AgentResult`；在 stream 循环里数 `result.Result != nil` 的次数；或给 `AgentEngine` 加 `GetIterationCount()`（`agent.go` 引擎现有循环维护，见 `agent_execution.go` 的迭代状态） |
| `Duration` | `time.Since(start)` 在 `Execute` 入口/出口 |
| `FilesChanged` | 两条路：(a) **结构化**：子代理 `general.txt` prompt 注入"最终答案须列出改动文件路径"（对齐 codex `multi_agents_spec.rs:731`），父解析 `Output` 里的路径清单；(b) **程序化**：子代理 engine 加 `toolEvent` 观察器（`streamToolCallback`，`agent_execution.go:133-202`），捕获 `write_file`/`edit_file` 的 `ToolInput` 里的 `path` 字段。本轮实现 (b) 兜底 + (a) prompt 引导，输出取并集去重。 |
| `Status` | 终态判定：正常结束 `completed`；`Result.Error != nil` → `error`；ctx deadline → `timeout`；ctx canceled → `cancelled` |
| `TaskID` | 每次委派在 `SubagentTool.Execute`/`Spawn` 入口 `uuid.NewString()`（`github.com/google/uuid` 已在 go.mod，`session.go` 在用） |

### 6.2 触发 files_changed 捕获的挂点

`streamToolCallback` 已把工具事件发到 `StreamResult`（`Type=="tool_event"`, `ToolEvent{Event:"tool_result", ToolName, ToolCallID, Input}`）。`Subagent.Execute` 的 stream 循环（`subagent.go:127-140`）**当前只处理 `chunk` 和 `result` 类型**，需补 `case "tool_event"` 分支，匹配 `write_file`/`edit_file`/`bash`(git mv/cp) 的 `ToolEvent.Input["path"]`，收集到 `filesChanged []string`。子代理工具集里这些工具的输入键名以 `agent/tools/builtin` 为准（`write_file` 用 `path` 等），实现时按实际键名取。

---

## 7. 超时/孤儿治理（根治）

### 7.1 现状问题
`executeToolWithTimeout`（`agent_tools.go:44-80`）：ctx.WithTimeout 包住 goroutine，超时后 select 走 `ctx.Done()` 返回，但 goroutine 里 `tool.Execute(ctx, args)` 的 ctx **是同一个** —— 已带 timeout。**理论上 LLM HTTP 调用会在超时点被取消**（`anthropic_native.go:179` `NewRequestWithContext`；langchain HTTP 亦传 ctx）。真正的问题是：
1. **子代理内部的中间工具调用**：某个 `bash` 命令（`agent/tools/builtin`）可能不监听 ctx（外部进程），超时后仍跑。
2. **超时返回后，父 turn 已把子代理标记失败，但子代理 goroutine 若继续，会继续消费共享 `llmProvider`/budget**。

### 7.2 根治措施
1. **确保 cancel 传播**：`executeToolWithTimeout` 在 `ctx.Done()` 分支里调用 `cancel()`（当前 `defer cancel()` 只在函数返回时执行——超时分支 return 时 defer 会跑，OK）；更关键的是**把超时 ctx 显式传给 `tool.Execute`**（当前已传 `ctx`，即带 timeout 的那个）。补上注释明确语义。
2. **子代理侧 ctx 派生**：`SubagentTool.Execute`/`Spawn` 里用 `context.WithCancel(parentCtx)` 派生子代理 ctx；父 turn 结束（`cancelTurn`，`session.go:346-351`）时子代理 ctx 一并取消。
3. **孤儿兜底 watchdog**：子代理 ctx 附 `context.WithTimeout(ctx, 3*time.Minute)`（可配置 `spawn_timeout`）。即使父 turn 结束未取消，子代理最多再跑 3 分钟，之后 LLM HTTP 被取消、goroutine 退出。
4. **并发上限**：`MaxConcurrentSpawns`（默认 4），用 channel semaphore 排队；超出的 spawn 在 `Spawn` 里阻塞或返回 `{"error":"too many concurrent subagents"}`（本轮阻塞排队即可）。
5. **关闭语义**：`SubagentManager.Close()`（`manager_subagent.go:149-153`）改为 cancel 所有进行中的子代理 ctx（`Manager` 持 `map[taskID]context.CancelFunc`），杜绝孤儿。

### 7.3 副作用确认
ctx 取消会让**已发出的 LLM 请求**中断（HTTP 层已支持），不会出现"取消后还在烧 token"；`bash` 等进程型工具需要各自监听 ctx（已在 `pkg/shell` 有 ctx 支持则直接用；无则属已知残留，在 §11 风险列明）。

---

## 8. 父代理如何知道子代理完成（三种载体的选型）

### 8.1 输入队列（B2，codex 主路径）
- codex：`input_queue.rs` mailbox `VecDeque` + `watch::Sender<InputQueueActivity>`；`inter_agent_communication` 把 `InterAgentCommunication` 入队，`trigger_turn=false` 只注入不唤醒，`true` 唤醒开新 turn（`handlers.rs:81-96`）。
- cortex 对应改造：`Session` 增加 mailbox 引用，`run()` select 增加 `case <-mailboxChange` 分支：若有未读完成通知且无进行中 turn → 触发新 `executeWithInput` 并在 `AgentInput` 里注入 `DelegateResult.Truncated()` 文本块（`role: assistant` 风格片段）。**放 B2**。

### 8.2 事件总线（旁路，B1 即可用）
- `dino/bus.go` `Bus.Publish("subagent.completed", parentSessionID, envelope)`，`Subscribe("subagent.completed", ...)`。供 UI/harness/审计消费；**不作为** 完成通知注入 LLM 的通道（原因见 §4.5）。
- 评估报告 13.1 提的"无中途通信，唯一通道是结束时返回字符串"——mailbox+事件解决"结束时"的通知；"中途通信"归 roadmap 的 send_message。

### 8.3 轮询（不推荐做 LLM 可见轮询）
- codex 的 `wait_agent` 是**工具内阻塞订阅**（`wait.rs`），不是 LLM 轮询。cortex 的 `wait_agent` 同样做成工具内阻塞 + mailbox `sync.Cond`，模型只调一次，不会写"循环调用 wait_agent"的提示词。

### 8.4 推荐组合
- **A**：阻塞 + 结构化（完成 = 工具返回）。
- **B1**：`spawn_agent`（fire-and-forget）+ `wait_agent`（mailbox 阻塞取）+ `subagent.completed` 事件（旁路）。
- **B2**：mailbox 注入下一 turn（完成即"消息到达"，无需显式 wait）。

---

## 9. 测试清单（单测 + 集成）

### 9.1 单元测试（`dino/agent/`，复用现有 mock 模式：`agent_test.go` / `manager_subagent_test.go` 的 `mockLLMProvider`/`mockFactory`）

**A 阶段**
1. `TestDelegateResult_Fields`：`DelegateResult` 各字段 JSON round-trip。
2. `TestDelegateResult_Truncated_Completed`：正常态信封格式 + 超限截断。
3. `TestDelegateResult_Truncated_Error`：错误态含回退文案。
4. `TestSubagentExecute_Structured`：mock LLM 返回含 files list，断言 `FilesChanged` 被解析。
5. `TestSubagentExecute_CollectsToolEvents`：mock 工具 `write_file` 触发 tool_event，断言 `FilesChanged` 含该 path。
6. `TestSubagentExecute_StatusError`：`Execute` 中途 `Result.Error` → `Status=="error"`。
7. `TestSubagentExecute_StatusTimeout`：mock LLM 阻塞到 ctx deadline → `Status=="timeout"`，且 mock LLM 收到 cancel（断言 ctx.Done 传播）。
8. `TestSubagentTool_ReturnsEnvelope`：`SubagentTool.Execute` 返回 `*DelegateResult` 且 `Output` 与旧 `Result.Output` 相等（向后兼容）。
9. `TestSubagentTool_BackwardCompatString`：把返回值喂给 `types.FormatToolResult`，断言 JSON 含 `"output"` 字段（老消费方不炸）。
10. `TestNewTaskID_Unique`：两次委派 task_id 不同。

**B1 阶段**
11. `TestManagerSpawn_FireAndForget`：`Spawn` 立即返回，后台 goroutine 完成。
12. `TestMailbox_PutDrainOnce`：Put 后可 Drain 一次，二次为空。
13. `TestWaitAgent_ReceivesCompletion`：`wait_agent` 阻塞直到子代理完成，返回结果信封。
14. `TestWaitAgent_Timeout`：超时返回 `{completed:false, timed_out:true}`。
15. `TestCompletionNotifier_EmitsBusEvent`：完成时 `subagent.completed` 事件发布（用 `dino/bus.go` 的 `Bus` 实例）。
16. `TestSpawn_ParentCancelKillsSubagent`：父 ctx cancel → 子代理 ctx 也 cancel，后台 goroutine 退出（用可 cancel 的 mock LLM 断言）。
17. `TestMaxConcurrentSpawns`：并发上限生效，第 5 个排队。

**B2 阶段**
18. `TestSession_MailboxInjection`：mailbox 有完成通知 → 下一 turn 的 `AgentInput` 含 FINAL_ANSWER 片段。
19. `TestSession_WakeOnCompletion`：无进行中 turn 时，mailbox 到达触发新 turn。

### 9.2 集成测试（`dino/dino_test.go` 或新增 `dino/subagent_integration_test.go`）
1. `TestDelegateEndToEnd_Structured`：真实 `SubagentManager` + mock LLM，父代理调 `delegate_to_agent`，断言返回信封字段。
2. `TestSpawnWait_EndToEnd`：spawn 两个子代理并行，wait 两个结果，断言顺序无关性。
3. `TestSessionWithSubagent_FullTurn`：经 `Session` + 工具注册（仿 `factory.go:593-604`）跑完整 turn。
4. `TestTimeoutOrphanNoLeak`：子代理超时后 goroutine 计数归零（runtime.NumGoroutine 前后对比，容忍抖动）。

---

## 10. 迁移顺序（分步可验证）

| 步骤 | 内容 | 验证点 | 独立可测 |
|---|---|---|---|
| **S1** | A 阶段：`result.go` + `Result` 扩字段 + 工具返回信封 + 配置字段 | 现有 `go test ./dino/agent/...` 全绿；9.1#1-10 全绿 | ✅ |
| **S2** | 超时治理：ctx 派生 + watchdog + `Close` cancel | 9.1#7, 9.2#4；手动：设短超时观察 LLM 请求被 cancel | ✅ |
| **S3** | B1：`path.go` + `mailbox.go` + `completion.go` + `spawn_agent`/`wait_agent` 工具 + `factory.go` 接线 | 9.1#11-17；9.2#2 | ✅ |
| **S4** | B2：`Session.run` mailbox 注入 + 唤醒 | 9.1#18-19 | ✅ |
| **S5**（可选收尾） | 接上/删除 `ShouldDelegate` 死代码；`general.txt` prompt 加"列出改动文件"引导 | 9.1#4 强化 | ✅ |

每个 S 独立 commit、独立跑测试；S1-S2 无执行模型改动，风险最低，建议最先合入。

---

## 11. 风险点

1. **工具返回值破坏性变更**（S1）：`delegate_to_agent` 从 string → 信封，下游若强断言 `string(result)` 会拿到 JSON。缓解：`DelegateResult.Output` 兜底 + 仓库内 grep 确认消费方（当前仓库内只有 `manager_subagent.go:212` 自身与 `factory.go` 包装层，无外部消费者——需在实现时再 grep 一遍，含 examples/）。
2. **`files_changed` 依赖 LLM 自觉**：结构化解析 (a) 依赖 prompt 引导，(b) 依赖工具输入键名。缓解：双路并集 + 对 `bash` 的 git 操作做启发式（`git status --porcelain` 快照比对可选，工作量不大但本轮不做）。
3. **后台 goroutine 生命周期**（B1）：`spawn_agent` 后子代理独立跑，父 session 关闭/用户离开时需能回收。缓解：`Manager` 持 cancel map + `Close` 全 cancel + 3 分钟 watchdog。
4. **mailbox 内存泄漏**：`Put` 后无人 `Drain`（子代理完成但父代理已不在）。缓解：`mailbox` 设 TTL（如 1h 过期清理，`Drop` on session close）。
5. **`eventbus` 全局实例跨会话串扰**：`dino/bus.go` 的 `Bus` 是实例级（`NewBus()`），但 `eventbus.GlobalBus`（`eventbus.go:654`）是全局的，且 `dino/bus.go:124` 还有 `GetGlobalBus()` 单例。**必须用实例级 `Bus`**（factory 构造一次，注入 notifier），不要用 `eventbus.Slot`/`Emit` 包级函数或 `GetGlobalBus()`，否则多会话事件互相串。
6. **B2 注入顺序**：mailbox 注入若与用户输入并发，需保证不吞用户消息。缓解：注入走 `Session.run` 的 select 分支（`session.go:266-291`），与用户输入同优先级、按到达序。
7. **`Duration`/`time` 字段 JSON**：`time.Duration` 序列化是 int64 ns，不友好。字段声明为 `DurationMS int64`（文档说明），`Usage` 复用 `types.Usage`（有 json tag）。
8. **ctx 取消对共享 provider 的影响**：子代理取消不能取消父代理/其他子代理的共享 `llmProvider` 请求——HTTP client 无状态（每次 `NewRequestWithContext`），安全。

---

## 12. 后续阶段 roadmap（简述，不在本轮范围）

按评估 13.3 剩余项排序：
1. **send_message（QueueOnly）/ followup_task（TriggerTurn）**：mailbox 扩展为「消息队列」，`DelegateResult` 泛化为 `InterAgentMessage{MessageType, TaskName, Sender, Payload}`（对齐 codex `inter_agent_message.rs:62-70`）；`trigger_turn` 标志在 `Session.run` 注入时区分"注入不唤醒" vs "唤醒开 turn"。依赖 B2。
2. **list_agents / close_agent / depth limit**：`AgentPath` 扩展多层级 + `Manager` 增加 `list`/`close`（`close_agent` 即现有 `Manager.CloseAgent`，`manager_subagent.go:260-267`，需要暴露成工具）；`agent_max_depth` 默认 1。
3. **权限单调性（restrict-only）**：子代理权限 = 父权限 ∩ 子 `Info.Permission`（对齐 codex `role.rs:1-4`）；当前 `filterTools`（`subagent.go:176-188`）只按子规则集过滤，需改为交集。
4. **树级 budget + LRU 驻留**：`RolloutBudget` 整树 token 预算（现有 `dino/Budget` 是会话级）+ 空闲子代理 LRU 逐出。
5. **持久化 spawn 边 + InteractionEdge trace**：SQLite `upsert_thread_spawn_edge`/BFS 后代 + `rollout-trace` 边，崩溃恢复。
6. **提示纪律注入**：父代理"先本地再委派/分割写作用域/报告文件路径"（对齐 `multi_agents_spec.rs:691-747`）——S5 已含文件路径部分。
7. **接上/删除自动委派死代码**：`ShouldDelegate` + `SubagentHandler` 二选一（评估 13.3 第二行，极小工作量）。

---

## 13. 关键决策摘要（给评审）

1. **信封而非裸字符串**：`delegate_to_agent` 返回 `*DelegateResult`，`Output` 保留原语义，`FormatToolResult` 自动 JSON 化注入，向后兼容。
2. **完成通知用 mailbox + 事件双轨**：mailbox（模型可见、按 task 拉取、只读一次）承载完成通知；`pkg/eventbus` 只做旁路可观测，不作为注入通道（因其无状态、无路由、全局实例会跨会话串扰）。
3. **阻塞 vs 异步**：推荐 A → B1 → B2 渐进，每步独立验证；B2（turn 唤醒）是唯一需要动 `Session.run` 调度的一刀，留到 send_message 一起做。
4. **超时治理**：ctx 派生 + watchdog + `Close` cancel，根因是"取消传播"而非仅"返回超时错误"。
5. **结果信封截断对齐 codex**：1000 tokens / 错误附回退文案，复用 codex 文案。

---

## 14. 遗留待定点（需要用户拍板）

1. **`delegate_to_agent` 返回值破坏性变更是否可接受**，还是要求信封同时提供"纯文本模式"（config 开关 `delegate_return_mode: "string"|"envelope"`）。
2. **B 阶段是否需要 `spawn_agent`/`wait_agent` 新工具名**，还是继续只扩 `delegate_to_agent` 的 schema（`async: true` + `task_id`）——两种都对 codex 语义有取舍（新工具更贴近 codex 工具族，扩 schema 改动更小）。
3. **`files_changed` 采集的 prompt 引导文案**是否需要本地化（`general.txt` 目前是英文）。
4. **mailbox TTL / 并发上限默认值**（1h / 4）是否符合生产预期。

---

## 15. 实现备注（S1 + S2/S3 铺路，2026-08-28）

> 实现纪律要求：记录实现中偏离设计的决策。S1（方案 A）+ S2（ctx 传播验证）+ S3/B1 铺路（B2 回收钩子）已落地，S2 超时本体 / S3 fire-and-forget / S4 B2 未实现，见 §10 迁移表。

**评审修正的落地情况：**
- **B1（缓存毒化）**：`SubagentTool.Metadata()` 返回 `Extra["no_cache"]=true`。已用 `TestDelegateTool_EngineCacheBypass` 验证：两次同 args 的 delegate 调用，子代理 LLM 各执行一次（缓存不命中）。
- **B2（session 回收钩子）**：`SubagentManager` 加 `map[sessionID]map[taskID]CancelFunc` 分包 + `CloseSession(sessionID)` + `register/unregisterSubagentCancel` + `cancelSessionAll`；`dino/factory.go` `CloseSession` 已调 `subagentManager.CloseSession(sessionID)`。S1 无 spawn，接口是 S3 预留。
- **R1（错误折叠）**：`SubagentTool.Execute` 的 error 路径返回 `*DelegateResult{Status:"error", Error:...}` + nil error（不再走 `toolObservationError` 字符串）。
- **R2（字段类型统一）**：信封时间字段 `TimestampMS int64`（unix_ms）+ `DurationMS int64`，可选字段 omitempty；不用 `time.Time`。
- **R3（files_changed 双路）**：程序化 (b) 走 `tool_event` 采集 `write_file`/`edit_file` 的 `path` 键（`StreamEventToolInputStart` 阶段，避免与结果阶段重复）；prompt 引导 (a) 留 S5。bash git 场景已知残留。

**实现中的关键决策（偏离/补充设计）：**
1. **Iterations 来源改用 `OnAfterIteration` 钩子**：设计 §6.1 提"stream 循环数 result.Result"，但 `ExecuteStream` 只在 `end` 事件携带 `AgentResult`（`agent_execution.go:1225-1228`），stream 循环计数只会是 1。改用 `eng.SetHooks` 的 `OnAfterIteration` 取最大轮数，权威且不依赖 stream 事件。`subagent.go` 的 stream 循环只负责 output/files_changed/usage。
2. **错误态 Status 判定**：stream `error` 事件按 `ctx.DeadlineExceeded -> "timeout"` / `ctx.Canceled -> "cancelled"` / 其余 `"error"` 折叠（`statusFromStreamError`）。这依赖 mock/真实 provider 在 ctx 取消时把 `ctx.Err()` 作为错误返回（anthropic 原生与 langchain HTTP 层都传 ctx，符合设计 F16 核实）。
3. **task_id 每次委派生成**：`SubagentTool.Execute` 入口 `uuid.NewString()`（复用 go.mod 已有依赖）。B 阶段可改为从 `input["task_id"]` 读取。
4. **`DelegateResult.Truncated` 的 Sender 硬编码 `/root/`**：S1 无 `AgentPath`（§3.3 未落盘，按评审 O1），`AgentPath` 铺路留 S3。`task name /root` 同理。
5. **`newTestSubagentManager` / `subagentMockFactory.llm`**：测试基建扩展，允许注入自定义 LLM。
6. **配置字段**：按评审 O2，S1 只加 `NotifyCompletion`/`CompletionMaxRunes`；`SpawnTimeout`/`MaxConcurrentSpawns` 留 S3 再引入，避免 S1 背"未使用配置"。

**S2 超时治理落地程度：** `executeToolWithTimeout` 本体未改（评审 O5 澄清：它已把 timeout ctx 传给 `tool.Execute`，`agent_tools.go:67`）。S2 的"子代理侧 ctx 派生 + watchdog + Close cancel"中：ctx 传播链路已由 `TestSubagentExecute_StatusTimeoutAndCancel` 验证（子代理 `Execute(ctx)` → engine `enrichContextWithConfig` → LLM provider，ctx deadline/cancel 到达 provider 且折叠进信封 Status）；watchdog 兜底在 S1 是 `cfg.Timeout = 3min`（`buildConfig`，子代理引擎级超时，等价设计 §7.2.3 的 watchdog 行为）；`Close` cancel 见 B2 铺路。**S2 无需额外代码，S1+S3 铺路已覆盖其验证点。**

**roadmap 待办（未实现，按 §10/§12）：** S3（mailbox + spawn/wait + notifier）、S4（B2 turn 唤醒）、send_message/followup_task、list_agents/close_agent、持久化拓扑、自动委派死代码接线/删除。
