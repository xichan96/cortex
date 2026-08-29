# 子代理 S3/S4 设计：mailbox + spawn/wait + completion notifier + B2 turn 唤醒

> 对应 `docs/next-round.md` **P3.3**。S1（结果结构化）+ S2（超时治理）+ S3 铺路（session 级 cancel map）已落地；本设计完成 S3 本体（mailbox + `spawn_agent`/`wait_agent` + completion notifier）与 S4（B2 turn 唤醒）。
> 设计阶段：**不改任何业务代码**，仅产出本文档（`docs/design/subagent-s3s4.md`）。
> 前置依赖：P2.3 工具命名（`next-round.md:45-50`，`docs/design/subagent.md` §14 遗留点 2）。**本设计取「新工具族 `spawn_agent`/`wait_agent`」**，理由见 §3.1。

---

## 1. 现状事实核查（基于当前 worktree HEAD `5d427a1`）

### 1.1 S1/S2/S3 铺路已落地
- **结果信封**：`dino/agent/result.go:18-47` `DelegateResult`（`Agent`/`TaskID`/`Status`/`Output`/`FilesChanged`/`Error`/`DurationMS`/`Iterations`/`Usage`/`TimestampMS`，时间统一 unix 毫秒 int64）；`DelegateResultFromResult`（`result.go:54-94`）成功/错误路径折叠；`NewErrorDelegateResult`（`result.go:97-99`）；`Truncated`（`result.go:104-131`）生成 codex 对齐的 FINAL_ANSWER 文本（默认 `DefaultDelegateTruncatedRunes = 2000` runes ≈ 1000 tokens，`result.go:50`）。
- **子代理执行**：`dino/agent/subagent.go:125-202` `Execute`（engine 每次新建、无跨调用状态）；`statusFromStreamError`（`subagent.go:206-217`）把 ctx deadline/cancel 折叠为 `timeout`/`cancelled`。
- **session 级 cancel 分桶**（S3 铺路）：`SubagentManager.sessionCancels` `map[sessionID]map[taskID]context.CancelFunc`（`manager_subagent.go:28`）；`CloseSession`（`manager_subagent.go:96-98`）+ `registerSubagentCancel`（`101-116`）/`unregisterSubagentCancel`（`119-131`）/`cancelSessionAll`（`134-150`）。`dino/factory.go:721` `CloseSession` 已调 `subagentManager.CloseSession(sessionID)`。
- **配置**：`dino/agent/info.go:138-153` `SubagentConfig` 有 `Enabled`/`ReplayToParentMemory`/`MaxHistoryMessages`/`NotifyCompletion`（默认 true）/`CompletionMaxRunes`（默认 2000）/`DelegateReturnMode`；`dino/config.go:245-251` 默认值已填。

### 1.2 现有多代理工具
- 唯一活的工具 `delegate_to_agent`：`SubagentTool`（`manager_subagent.go:152-245`），schema 含 `agent`（enum 硬编码 `["general"]`）+ `task` + `task_id`（`168-188`）；`Execute` 同步调 `t.manager.Execute(...)` 返回 `*DelegateResult` 信封（`207-245`），`string` 模式兼容（`219-230`）。`Metadata` 返回 `Extra["no_cache"]=true`（`190-199`）。
- 注册：`dino/factory.go:630-645`（`NewSubagentTool` + 可选 `ReplayToParentMemory` 包装 + `WrapToolResultLimiter`/`WrapNonFatalTool`/`WrapLoopDetection`）。

### 1.3 父代理执行模型（B2 改造对象）
- `dino/session/session.go:89-110` `Session` 持 `input chan interface{}`、`output chan *Event`、`queue dinoQueue.Interface`、`agent *engine.AgentEngine`、`ctx/cancel`。
- `run()`（`session.go:255-292`）：`for + select` 三路——`s.ctx.Done()` / `s.input`（`273-284`，类型分支 `AgentInput`/`string`/默认） / `queueChan`（`285-289`，`processQueueItem`）。**一次只跑一个 turn**，turn 内 `executeWithInput`（`334`）同步跑完。
- `executeWithInput`（`334-…`）：`context.WithCancel(ctx)` 派生 `turnCtx`，`defer cancelTurn()`（`346-351`）；`s.agent.ExecuteStream(turnCtx, agentInput, nil)`（`409`）。
- 工具调用在 turn 内阻塞（`agent/engine/agent_tools.go:44-80` `executeToolWithTimeout`）。

### 1.4 包依赖方向（B2 接线约束）
- `dino/session` **不 import** `dino/agent`；`dino/agent` **不 import** `dino/session`。二者通过 `dino/factory.go` 组装。
- `dino/agent` 依赖：`agent/engine`、`agent/types`、`agent/hooks`、`dino/permission`（`subagent.go:11-14`）、`pkg/logger`、`google/uuid`。
- `Session` 已依赖：`agent/engine`、`agent/types`、`dino/queue`、`agent/utils`、`pkg/logger`。
- 结论：**session 层只能拿到 `dino/agent` 的纯数据/纯函数（如 `DelegateResult`、`AgentPath`），不能反手依赖 `SubagentManager`**。B2 的注入信息通过 `*DelegateResult`（含 `AgentPath` 字段）在 factory 组装时传递。

### 1.5 现有测试基建
- `dino/agent/manager_subagent_test.go:11-112`：`subagentMockLLMProvider`（可注入响应序列/计数/可选取消感知）、`subagentMockFactory`（可选 LLM 覆盖）、`newTestSubagentManager*` 系列（`result_test.go` 扩展了 mode）。复用即可。
- `dino/agent/result_test.go`：信封 JSON round-trip / `Truncated` / `DelegateReturnMode` 已有测试。

---

## 2. 本任务范围界定

**做（P3.3 S3 + S4）：**
1. **S3-mailbox**：独立可注入的会话级 mailbox（评审 R5），`Put`/`Drain`/`Peek`/`Drop`，TTL + cap，session 关闭时显式回收。
2. **S3-tools**：`spawn_agent`（fire-and-forget，返回 `task_id`）/ `wait_agent`（mailbox 阻塞订阅，返回 `{message, timed_out}`）。
3. **S3-notifier**：子代理完成 → `mailbox.Put` + `subagent.completed` 旁路事件；信封截断 + 失败回退文案。
4. **S4-B2**：mailbox 到达 → 父代理新 turn 唤醒 + 注入截断信封文本（唯一动 `Session.run` 调度的一刀）。

**不做（后续 roadmap，§11 简述）：**
- `send_message`（QueueOnly）/ `followup_task`（TriggerTurn）——依赖 S4 的注入载体，列入 roadmap。
- `list_agents` / `close_agent` / 多级 `AgentPath` / depth limit / 权限交集（restrict-only）。
- 持久化 spawn 边 + InteractionEdge trace；树级 budget + LRU 驻留。
- `delegate_to_agent` 删除（保留，兼容策略见 §6）。

---

## 3. 工具命名与 schema（对齐 P2.3 决策）

### 3.1 P2.3 结论：用新工具族，不扩 `delegate_to_agent`

评审（`docs/design/review-subagent.md`）建议新工具名，避免 `async: true` 标志与阻塞语义纠缠（`next-round.md:47`）。本设计落定：

- **`delegate_to_agent`**：保留，语义不变（同步阻塞 + 信封）。已有用户按"委派一次、等结果"的心智建模，删除会破坏存量 prompt。
- **`spawn_agent`**：新增，fire-and-forget。父代理在**同一 turn** 可连发多个 spawn 做并行 fan-out。
- **`wait_agent`**：新增，阻塞订阅 mailbox。`executeToolWithTimeout` 已提供超时外壳，配合工具内 ctx.Done 分支防挂起。

> 关键设计点：spawn 与 wait 是**同一 run 生命周期**的两端——`spawn_agent` 返回的 `task_id` 就是 `DelegateResult.TaskID`，`wait_agent` 用 `task_id` 从 mailbox 精确取回。这比 codex 的"send_message + followup_task 按名称寻址"简单一层，且与现有 `DelegateResult.TaskID`（`result.go:22`，S1 已生成）自然衔接。

### 3.2 `spawn_agent` schema

```json
{
  "type": "object",
  "properties": {
    "agent":       {"type": "string", "enum": ["general"], "description": "要派生的子代理名（本轮仅 general）"},
    "task":        {"type": "string", "description": "子代理任务描述"},
    "task_name":   {"type": "string", "description": "可选；给这次 run 起名（roadmap list_agents 用）"},
    "fork_turns":  {"type": "integer", "description": "可选；本轮忽略（等价 continue 语义，暂无历史快照可 fork），留 schema 占位"},
    "model":       {"type": "string", "description": "可选；模型覆盖，空 = 继承父代理 provider（本轮支持，S3 落地时按 provider 能力校验）"},
    "timeout_ms":  {"type": "integer", "description": "可选；覆盖默认 3min watchdog"}
  },
  "required": ["agent", "task"]
}
```

返回（工具返回 `interface{}`，经 `FormatToolResult` JSON 化注入观察，`docs/design/subagent.md` §1.3）：

```json
{"task_id": "<uuid>", "agent": "general", "task_name": "<可选>", "status": "spawned", "max_concurrent_spawns": 4}
```

- `task_id` 由 `Spawn` 入口 `uuid.NewString()` 生成（复用 S1 依赖）。
- **并发上限**：channel semaphore（`MaxConcurrentSpawns`，默认 4，对齐 codex）。满时 `Spawn` 阻塞排队（不返回错误），避免模型重试语义。

### 3.3 `wait_agent` schema

```json
{
  "type": "object",
  "properties": {
    "task_id":    {"type": "string", "description": "spawn_agent 返回的 task_id（必填）"},
    "timeout_ms": {"type": "integer", "description": "可选；阻塞等待上限，默认 180000（3min）"}
  },
  "required": ["task_id"]
}
```

返回：

```json
{"task_id": "<uuid>", "completed": true, "timed_out": false, "message": "<DelegateResult.Truncated() 截断文本>"}
{"task_id": "<uuid>", "completed": false, "timed_out": true, "message": "wait_agent timed out after 180000ms"}
```

- `message` 直接是 `DelegateResult.Truncated(CompletionMaxRunes)`——父代理拿到即可消费，无需二次解析（对齐 codex `wait.rs` 返回 `{message, timed_out}` 摘要，评估 13.2 `docs/optimization-review-vs-codex.md:374`）。
- **不返回完整 `*DelegateResult`**，避免整包 JSON 灌进上下文（信封内嵌在 Truncated 文本里，`message` 已经带 status + payload）。

---

## 4. mailbox 设计（S3 核心，评审 R5）

### 4.1 设计约束（评审 R5 / R6 修正）
- **独立可注入组件**：不挂在 `SubagentManager`（factory 级、跨 session 共享，`review-subagent.md:94-96`），而是**每 session 一个实例**，由 factory 在 `CreateSession` 时构造并注入。生命周期跟随 session。
- **按 sessionID 分桶**：即便实现是全局 map 分桶，也保证 `Drop(sessionID)` 在 session 关闭时显式回收（评审 R5 要求 session 关闭钩子调 `Drop`）。
- **cap + TTL**：防单个 session 塞爆（spawn 大量子代理未消费）+ 防孤儿结果（子代理完成但父代理已走）。
- **B2 依赖**：mailbox 需向 `Session.run` 提供"有新消息"的信号（`Peek` 非空 + change 通知），唤醒新 turn。

### 4.2 类型与接口（新文件 `dino/agent/mailbox.go`）

```go
package agent

// Mailbox 承载"子代理 → 父代理"的完成通知（模型可见投递，非 eventbus 旁路）。
// 评审 R5：独立组件、每 session 一个，不挂 SubagentManager。
type Mailbox struct {
	mu    sync.RWMutex
	msgs  map[string]*MailboxEntry // key = task_id
	seq   int64                    // 单调递增，B2 唤醒信号用
	// 订阅唤醒：由 SubagentManager 注册的 NotifyWake（S4）或 wait_agent 的阻塞 channel。
	// B2：每 session 一个 notifier，由 factory 注入；无则 nil（纯 S3 模式）。
	notify func(taskID string)
	// 统计/诊断
	cap     int // 默认 64
	ttl     time.Duration // 默认 0（不启用）；>0 时惰性清理
}

// MailboxEntry 单条完成记录（含元数据，供 Drop/TTL 诊断）。
type MailboxEntry struct {
	Result      *DelegateResult
	CreatedAtMS int64
	ExpireAtMS  int64 // 0 = 不失效
}

func NewMailbox(cap int, ttl time.Duration) *Mailbox
func (mb *Mailbox) Put(taskID string, r *DelegateResult) error // cap 满时返回 error（Spawn notifier 降级为只发事件）
func (mb *Mailbox) Peek(taskID string) *DelegateResult          // 只读，不删（wait_agent 用）
func (mb *Mailbox) Drain(taskID string) *DelegateResult         // 读取并删除（wait_agent 用）
func (mb *Mailbox) DrainAll() []*DelegateResult                 // 取走全部未读（factory 适配器用）
func (mb *Mailbox) Len() int
func (mb *Mailbox) Drop()                                       // session 关闭回收（幂等，实例级无需 sessionID）
func (mb *Mailbox) SubscribeAll(done func()) string             // 全局完成订阅（factory 适配器用，见 §7.2）
func (mb *Mailbox) UnsubscribeAll(id string)                    // 反注册 SubscribeAll
```

关键语义：
- **键是 `task_id`**，不是 sessionID。`Put` 用子代理 run 的 `task_id` 存；`wait_agent`/B2 用同一 `task_id` 精确取。session 隔离靠**实例隔离**（每 session 一个实例）+ `Drop` 兜底，不需要全局 map（评审 R5 的修正）。
- **cap 满**：`Put` 返回 error。notifier 收到后降级为只发旁路事件 + 打日志，不阻塞子代理完成路径（结果已折叠进 `DelegateResult`，子代理 goroutine 可退出；父代理下次 spawn 会看到旧结果被逐出——记录为已知行为 §9 风险）。
- **TTL 惰性清理**：`Peek`/`Len` 时顺带扫过期条目；默认不启用（session 生命周期内 TTL 意义不大），`SpawnTimeout` 配置可开启。
- **`notify` 回调**：wait_agent 阻塞与 B2 唤醒共用。S3 阶段 wait_agent 注册一个临时 channel notifier；S4 阶段 factory 注入 session 级 notifier（见 §7）。

### 4.3 消费者与 B1 降级语义（评审 R6 明示）
- `wait_agent` 阻塞读（工具内）：`Drain(taskID)`。
- B2 唤醒注入：`DrainAll()` 注入下一 turn。
- **纯 S3（无 S4）时完成通知是"惰性可见"**：落 mailbox 等下次用户 turn 才消费（`review-subagent.md:98-100`）。本设计**明确把 B2 作为 S3 价值的兑现点**，S3 测试里完成通知可见性按 mailbox 状态断言，不假设父代理自动醒来。

---

## 5. Spawn / Wait 实现

### 5.1 `SubagentManager.Spawn`（`manager_subagent.go` 新增）

```go
// SpawnHandle 由 Spawn 立刻返回，后台 goroutine 异步执行。
type SpawnHandle struct {
	TaskID    string
	AgentPath AgentPath
	Done      <-chan struct{} // 完成即关闭（notifier 已 Put mailbox）
}

// Spawn 发起 fire-and-forget 子代理。ctx 为父 turn ctx（取消传播）。
// 后台 goroutine 里执行 subagent.Execute，完成后经 CompletionNotifier 回发。
func (sm *SubagentManager) Spawn(
	ctx context.Context,
	parentSessionID string,
	req *Request,
	parent AgentPath,
	mailbox *Mailbox,
	opts SpawnOptions,
) (*SpawnHandle, error)
```

`Spawn` 内部：
1. 并发 semaphore 取令牌（`MaxConcurrentSpawns`），满则阻塞排队。
2. `taskID := uuid.NewString()`；`taskCtx, cancel := context.WithCancel(ctx)`；`ctx.WithTimeout` 做 watchdog（默认 3min，`opts.TimeoutMS` 覆盖）。
3. `sm.registerSubagentCancel(parentSessionID, taskID, cancel)`（S3 铺路接口，`manager_subagent.go:101`）；`defer sm.unregisterSubagentCancel(...)`（`119`）。
4. `done := make(chan struct{})`；go func：`result, err := subagent.Execute(taskCtx, req)` → `env := DelegateResultFromResult(agentName, result, err)`，`env.TaskID = taskID`，`env.AgentPath = path.String()`，`env.ParentPath = parent.String()` → `notifier.Notify(parentSessionID, env)` → `close(done)`。**goroutine 退出时 `close(done)` 在 notifier 之后**，保证 Done 语义 = "已完成且已回发"。
5. 返回 `&SpawnHandle{...}`。

### 5.2 `wait_agent` 工具（新文件 `dino/agent/tools_spawn.go`）

```go
type SpawnAgentTool struct {
	manager  *SubagentManager
	mailbox  func() *Mailbox // 当前 session 的 mailbox（注入式，避免工具持有 session 状态）
	path     func() AgentPath
	sessionID func() string
}

type WaitAgentTool struct {
	manager       *SubagentManager
	mailbox       func() *Mailbox
	defaultTimeout time.Duration
}
```

**wait_agent 阻塞语义 + ctx 防挂起（评审点名 `agent_tools.go` 的 ctx 交互）**：

```go
func (t *WaitAgentTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	taskID, _ := input["task_id"].(string)
	timeout := defaultTimeout(t, input)

	mb := t.mailbox()
	if mb == nil {
		return map[string]interface{}{"task_id": taskID, "completed": false, "timed_out": true, "message": "no mailbox"}, nil
	}
	if r := mb.Drain(taskID); r != nil {
		return t.final(taskID, r)
	}

	// 尚未完成：阻塞订阅（一次），select ctx.Done 防挂起。
	// 关键：外层 executeToolWithTimeout 已包一层 timeout（agent_tools.go:44-80），
	// 但那是 engine 的 tool timeout；这里对 ctx.Done 分支返回 nil,nil（工具正常返回
	// 超时信封），让 LLM 拿到 {timed_out:true} 而非 tool timeout 错误。
	ch := make(chan struct{})
	mb.SubscribeOnce(taskID, func() { close(ch) }) // 见 §5.3
	defer mb.Unsubscribe(taskID, ch)

	select {
	case <-ch:
		if r := mb.Drain(taskID); r != nil {
			return t.final(taskID, r)
		}
		return map[string]interface{}{"task_id": taskID, "completed": false, "timed_out": true, "message": "no result"}, nil
	case <-ctx.Done():
		return map[string]interface{}{"task_id": taskID, "completed": false, "timed_out": true, "message": "wait_agent cancelled"}, nil
	case <-time.After(timeout):
		return map[string]interface{}{"task_id": taskID, "completed": false, "timed_out": true, "message": fmt.Sprintf("wait_agent timed out after %dms", timeout.Milliseconds())}, nil
	}
}
```

**与 `executeToolWithTimeout` 的交互（评审 R 点名）**：
- `agent/engine/agent_tools.go:44-80`：`executeToolWithTimeout` 用 goroutine + channel，`tool.Execute(ctx, args)` 传的是**带 timeout 的 ctx**（`:48,67`）。超时后 goroutine 继续跑直到 `select` 返回（`:71-79` 注释的 trade-off）。
- wait_agent 自己的 `case <-ctx.Done()` 分支保证：**engine 层 timeout 一到，ctx 被 cancel → 这里立刻返回 `{timed_out:true}` 信封**，不会让工具 goroutine 挂到 `time.After` 之后才返回。同时因为 `Execute` 返回 `nil error`，engine 层不会把超时当 tool error 重试/报错（`executeToolWithTimeout` 的 `ctx.Err()` 分支 `:74-78` 只在 ctx 已 done 且 tool 没返回时触发——wait_agent 主动返回后 engine 层 `select` 先收到 result 通道，不会走 ctx.Done）。
- 需要确认 `WaitAgentTool.Metadata()` 返回 `no_cache: true`（同 `SubagentTool`，`manager_subagent.go:190-199`），否则 engine 的 toolCache 会按 task_id 缓存结果信封。

### 5.3 mailbox 订阅原语（wait_agent 阻塞用，S3 内部；S4 复用为唤醒 notifier）

mailbox 增加：

```go
func (mb *Mailbox) SubscribeOnce(taskID string, done func()) string // 返回订阅 id（去重 key）
func (mb *Mailbox) Unsubscribe(taskID string, id string)
```

实现：`mb.mu` 下维护 `map[taskID]map[id]func()`；`Put` 时触发 `taskID` 的订阅者并删除。注意 `Put` 与 `SubscribeOnce` 的**竞态**：wait_agent 先 `Drain`（`5.2` 第二步）再 SubscribeOnce，若 Put 发生在 Drain 与 Subscribe 之间，Put 已触发但订阅未注册——因此 `Put` 需在锁内"查订阅者 + 无订阅者则仅存消息"，而 wait_agent 的 `SubscribeOnce` 需先检查消息是否存在（消息存在则直接返回，不注册）。**实现时用 mailbox 内部一把锁串起 `Peek`/`Put`/`SubscribeOnce`**，杜绝窗口。

---

## 6. completion notifier（S3，新文件 `dino/agent/completion.go`）

### 6.1 职责
`Subagent.Execute` 完成后（`Spawn` goroutine 内），notifier 干两件事：
1. **mailbox.Put**（模型可见投递，S3 消费方 = wait_agent / S4 = B2 注入）。
2. **旁路事件** `Bus.Publish("subagent.completed", parentSessionID, envelope)`（UI/审计，`dino/bus.go:25-31`）。

### 6.2 类型

```go
// Notifier 由 factory 构造并注入 SubagentManager（评审 R5：实例级，勿碰 GetGlobalBus）。
type CompletionNotifier struct {
	bus      *dino.Bus // 实例级（dino/bus.go:15-23）；nil = 不发事件
	mailboxes func(parentSessionID string) *Mailbox // 按父 session 取 mailbox
}

func NewCompletionNotifier(bus *dino.Bus, getMailbox func(string) *Mailbox) *CompletionNotifier

// Notify 在子代理 goroutine 内调用（不得阻塞：mailbox.Put 失败仅降级为事件）。
func (n *CompletionNotifier) Notify(parentSessionID, taskID string, env *DelegateResult) {
	mb := n.mailboxes(parentSessionID)
	if mb != nil {
		if err := mb.Put(taskID, env); err != nil {
			logger.Warn("[completion] mailbox full, degraded to event-only", ...)
		}
	}
	if n.bus != nil {
		n.bus.Publish("subagent.completed", parentSessionID, env)
	}
}
```

### 6.3 截断 + 失败回退文案
- 投递载体用 `env.Truncated(CompletionMaxRunes)`（`result.go:104-131`）：默认 2000 runes ≈ 1000 tokens；错误态自动带"Agent errored …"回退文案 + 引导（`result.go:119-123`）。
- 事件载荷用完整 `*DelegateResult`（JSON 全字段，供 UI/审计），**不进 LLM 上下文**。

### 6.4 与 eventbus 的分工（保持 S1 结论）
- mailbox = 模型可见投递、按 task_id 拉取、只读一次（`docs/design/subagent.md` §4.5 三条理由）。
- eventbus = 旁路可观测。**必须用实例级 `*Bus`**（factory 构造一次注入），不用 `GetGlobalBus()` 单例（`review-subagent.md:92`，多会话串扰）。

---

## 7. B2 turn 唤醒（S4，唯一动 `Session.run` 调度的一刀）

### 7.1 现状缺口（评审 R6 正是此点）
父代理 spawn 后 turn 结束，若无用户输入，**没有任何驱动让父代理处理 mailbox**。S3 的完成通知只是落 mailbox，父代理要等下一次用户输入才消费。S4 补上"mailbox 有消息 → 父代理自动开新 turn"。

### 7.2 信号载体：payload 通道（factory 适配，保持包解耦）
不在 `Session.run` 里直接引 `*Mailbox`（避免 `dino/session → dino/agent` 依赖，§1.4），也不让 `dino/agent` 引 session 类型。方案：**`dino/session` 定义纯数据 `WakePayload` + 接口 `WakeSource`；`dino/factory.go` 写一个小适配器**，把 mailbox 的完成通知转成 `WakePayload` 推给 session。

```go
// 在 dino/session 包定义（新文件 dino/session/wake.go）：
// WakePayload 是已完成子代理的紧凑注入单元（纯数据，session 不接触 agent 类型）。
type WakePayload struct {
	TaskID string // DelegateResult.TaskID
	Text   string // DelegateResult.Truncated() 结果，直接注入下一 turn
}

type WakeSource interface {
	Wake() <-chan WakePayload // 有新完成通知时产出一个 payload
}
```

**factory 适配器（`dino/factory.go`，每 session 一个 goroutine）**：

```go
// sessionWakeSource 订阅 mailbox 完成通知，DrainAll 后转成 WakePayload 推给 session。
type sessionWakeSource struct {
	ch chan session.WakePayload
}

func newSessionWakeSource(mb *agent.Mailbox, maxRunes int) *sessionWakeSource {
	s := &sessionWakeSource{ch: make(chan session.WakePayload, 8)} // 小缓冲，防完成密集时阻塞 notifier
	mb.SubscribeAll(func() {
		for _, env := range mb.DrainAll() { // 取走全部未读
			s.ch <- session.WakePayload{TaskID: env.TaskID, Text: env.Truncated(maxRunes)}
		}
	})
	return s
}
```

**为何这样设计化解了 wait_agent 冲突**：wait_agent 只在 **turn 内**（工具调用）阻塞；session 的 `run()` select 循环在 turn 期间**不运行**，因此 `wakeCh` 分支只在 idle 时被服务。于是：
- **idle 时完成**：适配器 `DrainAll` 取走结果 → 唤醒注入（wait_agent 此时不可能在阻塞）。
- **turn 内完成**（wait_agent 正阻塞等该 task）：适配器也触发，但 `DrainAll` 为空（wait_agent 的 `Drain` 已拿走）→ 不发 payload；session 仍忙 turn，`wakeCh` 分支不服务。turn 结束后无残留唤醒。
两者消费不冲突，竞态窗口由"turn 内 session 不在 select 循环"这一结构性质天然关闭。

### 7.3 `Session.run` 改动（`session.go:266-291`）

```go
func (s *Session) run() {
	defer s.wg.Done()
	defer close(s.output)

	queueChan := func() <-chan *dinoQueue.Item { ... }()

	// 新增：B2 唤醒源（mailbox 到达通知；nil = 无唤醒，行为同现状）。
	var wakeCh <-chan session.WakePayload
	if s.wake != nil {
		wakeCh = s.wake.Wake()
	}

	for {
		select {
		case <-s.ctx.Done():
			...
			return
		case input, ok := <-s.input:
			... // 用户输入（保持最高优先级：select 随机，见 7.4）
		case item, ok := <-queueChan:
			...
			s.processQueueItem(item)
		case p := <-wakeCh: // 新增：mailbox 有完成通知 → 新 turn（仅 idle 时可达）
			s.onSubagentCompletion(p) // 见 7.5
		}
	}
}
```

### 7.4 优先级语义（评审 §11 风险 6 + R6）
- Go select 对就绪 case **随机**。要让"用户输入"优先于"唤醒"，需保证 **idle 时只有唤醒分支在 select 里**，用户输入一到就替换。做法：`onSubagentCompletion` 只在 `s.running` 且**无进行中 turn** 时触发；turn 进行中时该分支天然不竞争（turn 是同步阻塞在 `executeWithInput` 内，select 没在转）。
- 真正的竞争窗口是 **idle**：用户输入与唤醒同时就绪 → select 随机。缓解：唤醒 turn 内容 = 纯注入（`DelegateResult.Truncated` 片段），不产生面向用户的输出副作用；若用户输入被随机选中，唤醒消息留在 mailbox（`DrainAll` 未消费），下一轮仍可注入——**唤醒可重入、不丢消息**（mailbox 直到 Drain 才删）。这满足"不吞用户消息"。
- **唤醒在 queue 模式下的行为**：`EnableQueue` 时（`session.go:63-69`）队列 item 是主要驱动；唤醒 turn 与队列 item 同时存在时按 select 随机、均可重入。S4 测试覆盖普通模式即可，queue 模式作为已知行为记录。

### 7.5 `onSubagentCompletion`（新方法）

```go
// onSubagentCompletion 收到一个唤醒 payload（已由适配器 DrainAll 并截断）→ 注入新 turn。
// 注意：此时 select 循环是 idle 的（turn 内 select 不服务本分支，§7.2），
// 注入的 turn 是"父代理自主继续"，而非对用户输入的响应。
func (s *Session) onSubagentCompletion(p WakePayload) {
	if s.wake == nil || !s.IsRunning() {
		return
	}
	// 注入新 turn：AgentInput 为完成片段（Message Type: FINAL_ANSWER 头，模型可识别
	// 是子代理回传而非用户消息），父代理在下一轮决定 wait / spawn 更多 / 收尾。
	in := types.NewAgentInput(p.Text)
	s.processInputWithAgentInput(in) // 复用现有 turn 执行路径（session.go:321-323）
}
```

> 实现注：factory 的适配器在 `DrainAll` 已把**多个**未读结果合并进各自 payload，session 侧一次只消费一个 payload（`Wake()` 每完成发一个）。若一次唤醒需合并多条，适配器把多条 `Truncated` 拼进一个 `WakePayload.Text`（§7.2 循环里拼接，见实现）。session 层只负责"拿到 payload → 注入"，不感知 mailbox。

> 注入的是 `agentInput.String()` 全文作为 assistant 侧续文——实际实现走 `executeWithInput(turnCtx, agentInput, "subagent completion")`，displayContent 用标记串避免污染 UI（§7.7 事件）。`DelegateResult.Truncated` 文本本身带 "Message Type: FINAL_ANSWER\nSender:…" 头，模型可识别这是子代理回传而非用户消息。

### 7.6 `Session` 装配（`dino/session/session.go:89-110` 新增字段 + `NewSession` 签名）

```go
// 新增字段
wake WakeSource

// NewSession 增加 wake 参数（nil = 禁用 B2）：
func NewSession(id string, agent *engine.AgentEngine, factory SessionFactory,
	ctx context.Context, cfg *Config, planner *PlannerHelper,
	budget interface{ CanExecute(sessionID string) bool }, wake WakeSource) *Session
```

调用点 `dino/factory.go:696` 改传 `wake`（由 `newSessionWakeSource(mailbox, config.Subagent.CompletionMaxRunes)` 构造）。`dino/agent` 的 `Mailbox` 经 factory 适配器转成 `WakeSource` 暴露给 session；**`dino/session` 不 import `dino/agent`**（§1.4 依赖方向保持，§9 决策 D5）。截断 runes 数在适配器构造时传入，session 层不需要 `completionMaxRunes` 字段（已在 7.2 用 `maxRunes` 参数截断）。

### 7.7 事件与 UI
- 唤醒 turn 是**自动、无用户输入**的。`processInputWithAgentInput` 会 emit `EventTypeMessage`（`session.go:361-364`）。为避免 UI 显示一条"幽灵用户消息"，`executeWithInput` 的 displayContent 走特殊标记（如 `"[subagent-completion]"`），消费端（`dino` harness / UI）识别后可不渲染或折叠。**S4 实现时给 `EventTypeMessage` 补一个 `Source` 字段（`user`/`system`/`subagent`，默认 user）**，最小侵入。

---

## 8. 与 `delegate_to_agent` 的关系（兼容策略）

| 维度 | `delegate_to_agent`（保留） | `spawn_agent`/`wait_agent`（新增） |
|---|---|---|
| 语义 | 同步阻塞，一次调用一个结果 | fire-and-forget + 显式 wait（可并行 fan-out） |
| 返回值 | `*DelegateResult` 信封（S1） | spawn→`{task_id}`；wait→`{completed,message}` |
| 执行路径 | `SubagentTool.Execute` → `sm.Execute`（同步） | `SpawnAgentTool` → `sm.Spawn`（异步 goroutine） |
| 完成通知 | 工具返回值（turn 内可见） | mailbox + `subagent.completed` 事件（S3）；唤醒注入（S4） |
| 配置开关 | `DelegateReturnMode`（`info.go:148-152`） | `MaxConcurrentSpawns`/`SpawnTimeout`/`NotifyCompletion` |

**取舍**：
- 保留 `delegate_to_agent`——存量 prompt 与 `ReplayToParentMemory` 包装（`factory.go:633-637`）依赖它，删除成本高收益低。
- 两条路径**共享同一份** `Manager` 子代理缓存 + 同一 `DelegateResult` 信封 + 同一 cancel 分桶。`sm.Execute` 与 `sm.Spawn` 在 `GetSubagent`（`manager_subagent.go:310-333`）汇合。
- 父代理 prompt 引导：**简单任务用 `delegate_to_agent`，需要并行/后台跑时用 `spawn_agent` + `wait_agent`**（S5 prompt 引导，roadmap）。

---

## 9. 改动文件清单与接口签名汇总

| 文件 | 阶段 | 改动 |
|---|---|---|
| `dino/agent/mailbox.go` | S3 | **新增**：`Mailbox`/`MailboxEntry` + `Put`/`Peek`/`Drain`/`DrainAll`/`Len`/`Drop` + `SubscribeOnce`/`Unsubscribe`（wait_agent 阻塞）+ `SubscribeAll`/`UnsubscribeAll`（B2 适配器） |
| `dino/agent/completion.go` | S3 | **新增**：`CompletionNotifier` + `Notify`（mailbox.Put + 实例级 Bus.Publish） |
| `dino/agent/path.go` | S3 | **新增**：`AgentPath`（一层：root→child，`docs/design/subagent.md` §3.3 铺路） |
| `dino/agent/tools_spawn.go` | S3 | **新增**：`SpawnAgentTool` / `WaitAgentTool` + `SpawnHandle` + `SpawnOptions` |
| `dino/agent/manager_subagent.go` | S3 | `SubagentManager` 加 `Spawn(...)`；加 `mailboxes`/`notifier` 注入字段；`CloseSession` 增加 mailbox Drop |
| `dino/agent/result.go` | S3 | `DelegateResult` 加 `AgentPath`/`ParentPath` 字段（omitempty） |
| `dino/agent/info.go` | S3 | `SubagentConfig` 加 `MaxConcurrentSpawns`/`SpawnTimeout`（`SpawnTimeout` 复用 S1 未引入项，`docs/design/subagent.md` §15 备注 6） |
| `dino/config.go` | S3 | 默认值：`MaxConcurrentSpawns: 4`、`SpawnTimeout: 3 * time.Minute`（`config.go:245-251`） |
| `dino/factory.go` | S3/S4 | 构造 `*Bus`（实例级）+ `Mailbox`（每 session）+ `CompletionNotifier`；注册 spawn/wait 工具（`630-645` 同区块）；`CreateSession` 传入 `wake`（`696`）；`CloseSession` 加 mailbox Drop（`721` 附近） |
| `dino/session/wake.go` | S4 | **新增**：`WakePayload` 纯数据 + `WakeSource` 接口 |
| `dino/session/session.go` | S4 | `Session` 加 `wake` 字段；`NewSession` 签名加参（`127`）；`run()` select 加 `wakeCh` 分支（`266-291`）；新方法 `onSubagentCompletion(p WakePayload)`；`executeWithInput` 的 displayContent 标记 |
| `dino/session/event.go` | S4 | `Event` 加 `Source` 字段（默认 `user`，唤醒注入 = `subagent`） |
| `dino/factory.go` | S4 | **新增** `sessionWakeSource` 适配器（mailbox → WakePayload，`DrainAll` + `Truncated`），`CreateSession` 传入 |
| `dino/agent/manager_subagent_test.go` 等 | S3/S4 | 测试（§10） |

**接口签名汇总**（Go）：

```go
// dino/agent/mailbox.go
func NewMailbox(cap int, ttl time.Duration) *Mailbox
func (mb *Mailbox) Put(taskID string, r *DelegateResult) error
func (mb *Mailbox) Peek(taskID string) *DelegateResult
func (mb *Mailbox) Drain(taskID string) *DelegateResult
func (mb *Mailbox) DrainAll() []*DelegateResult
func (mb *Mailbox) Len() int
func (mb *Mailbox) Drop()
func (mb *Mailbox) SubscribeOnce(taskID string, done func()) string // wait_agent 阻塞用
func (mb *Mailbox) Unsubscribe(taskID string, id string)
func (mb *Mailbox) SubscribeAll(done func()) string                  // B2 适配器用（§7.2）
func (mb *Mailbox) UnsubscribeAll(id string)

// dino/agent/path.go
type AgentPath string
func RootAgentPath() AgentPath
func (p AgentPath) Join(child string) AgentPath
func (p AgentPath) String() string

// dino/agent/completion.go
func NewCompletionNotifier(bus *dino.Bus, getMailbox func(string) *Mailbox) *CompletionNotifier
func (n *CompletionNotifier) Notify(parentSessionID, taskID string, env *DelegateResult)

// dino/agent/manager_subagent.go（新增）
type SpawnOptions struct {
	TimeoutMS int64
	Model     string // 模型覆盖，"" = 继承
	TaskName  string
}
func (sm *SubagentManager) Spawn(ctx context.Context, parentSessionID string, req *Request,
	parent AgentPath, mailbox *Mailbox, opts SpawnOptions) (*SpawnHandle, error)

// dino/session/wake.go（新增）
type WakePayload struct {
	TaskID string // DelegateResult.TaskID
	Text   string // DelegateResult.Truncated() 结果，直接注入下一 turn
}
type WakeSource interface {
	Wake() <-chan WakePayload
}

// dino/session/session.go
func NewSession(id string, agent *engine.AgentEngine, factory SessionFactory, ctx context.Context,
	cfg *Config, planner *PlannerHelper, budget budgetChecker, wake WakeSource) *Session
```

> 依赖方向确认：`dino/session` 只依赖**自己包内**的 `WakePayload`/`WakeSource`（纯数据 + 通道），**不 import `dino/agent`**。`DrainAll`/`Truncated` 全在 `dino/factory.go` 的 `sessionWakeSource` 适配器里完成（§7.2）。§1.4 的包解耦保持（决策 D5）。

---

## 10. 测试清单

### 10.1 单元测试（`dino/agent/`，复用 `manager_subagent_test.go:11-112` mock 基建）

**S3-mailbox**
1. `TestMailbox_PutDrainOnce`：Put→Drain 一次可取，二次为 nil。
2. `TestMailbox_PutDrainAll`：多个 Put→DrainAll 按到达序返回且清空。
3. `TestMailbox_CapFull`：cap=2 时第 3 个 Put 返回 error，旧消息不丢（或按设计逐出策略）。
4. `TestMailbox_TTLExpiry`：TTL 到期的条目在 Peek 时被清理。
5. `TestMailbox_SubscribeOnceRace`：Put 与 SubscribeOnce 竞态（先订阅/后订阅/中间 Put）都能收到（用 `-race` 跑）。

**S3-spawn/wait**
6. `TestSpawn_FireAndForget`：`Spawn` 立即返回，Done channel 在完成后关闭，mailbox 有结果。
7. `TestSpawn_ParentCancelKillsSubagent`：父 ctx cancel → 子代理 ctx 取消，`sessionCancels` 分桶被 `unregisterSubagentCancel` 清理（`-race`）。
8. `TestSpawn_RegistersSessionCancel`：`registerSubagentCancel` 写入 `sessionCancels[parentSessionID]`，`CloseSession` 后 cancel 全调。
9. `TestSpawn_MaxConcurrent`：semaphore 上限生效，第 5 个排队。
10. `TestWaitAgent_ReceivesCompletion`：spawn 后 wait_agent 阻塞到完成，返回 `{completed:true, message 含 FINAL_ANSWER}`。
11. `TestWaitAgent_Timeout`：未完成时超时返回 `{completed:false, timed_out:true}`。
12. `TestWaitAgent_CtxDone`：`executeToolWithTimeout` 的 timeout 先到 → ctx cancel → wait_agent 返回 `timed_out` 信封而非 engine tool error（验证 §5.2 的 ctx 分支）。
13. `TestSpawnAgentTool_ReturnsTaskID`：spawn 工具返回 `{task_id}`，与 mailbox 里 `DelegateResult.TaskID` 一致。
14. `TestWaitAgent_NoCache`：`WaitAgentTool.Metadata().Extra["no_cache"]==true`。

**S3-notifier**
15. `TestCompletionNotifier_PutsMailbox`：Notify 后 mailbox 有结果。
16. `TestCompletionNotifier_EmitsBusEvent`：实例级 Bus 收到 `subagent.completed`（用 `NewBus()` 构造的实例，不用 `GetGlobalBus`）。
17. `TestCompletionNotifier_MailboxFullDegrades`：cap 满 → Notify 不 panic、仍发事件。
18. `TestCompletionNotifier_Truncated`：`Notify` 载荷用 `Truncated(CompletionMaxRunes)` 截断。

**S4-唤醒**
19. `TestSession_WakeFiresTurn`：mailbox 有消息 → 适配器 `Wake()` 产出 `WakePayload` → session 新 turn 执行（`dino/session` 或 `dino` 集成，注入的 agentInput 含 FINAL_ANSWER 片段）。
20. `TestWakeAdapter_DrainAllOnce`：适配器收到完成通知后 `DrainAll` 取走全部并截断，重复触发不再产出 payload（`dino/factory` 侧单测）。
21. `TestSession_UserInputPriority`：唤醒与用户输入并发时用户输入不被吞（唤醒可重入，见 §7.4）。

### 10.2 集成测试（`dino/subagent_integration_test.go` 新增）
1. `TestSpawnWait_EndToEnd`：真实 `SubagentManager` + mock LLM，spawn 两个并行 → 两个 wait，顺序无关性。
2. `TestSessionWithSpawn_FullTurn`：经 `Session` + 工具注册（仿 `factory.go:630-645`）跑"spawn→wait"完整 turn。
3. `TestWakeOnCompletion_FullSession`：父 turn spawn 后结束 → mailbox 到达 → 父代理自动新 turn 消费（断言第 2 个 turn 的 agentInput 含 FINAL_ANSWER 片段）。
4. `TestSessionClose_DropsMailbox`：session 关闭 → mailbox Drop → 无泄漏（goroutine 计数容忍抖动）。
5. `TestTimeoutOrphanNoLeak`：子代理超时后 goroutine 计数归零（`runtime.NumGoroutine` 前后对比）。

---

## 11. 迁移顺序（S3 → S4，分步可验证）

| 步骤 | 内容 | 验证点 | 独立可测 |
|---|---|---|---|
| **S3a** | `path.go` + `mailbox.go`（无订阅）+ 单测 1-5 | `go test ./dino/agent/ -run Mailbox` 全绿 | ✅ |
| **S3b** | `completion.go` + `Spawn` + cancel 分桶接线 + 单测 6-9,15-18 | `go test ./dino/agent/ -run 'Spawn|Completion|Mailbox'` | ✅ |
| **S3c** | `tools_spawn.go`（spawn/wait 工具）+ `SubscribeOnce` + 单测 10-14 | `go test ./dino/agent/`；集成 1 | ✅ |
| **S3d** | `factory.go` 接线（mailbox 每 session + notifier + 工具注册 + CloseSession Drop）+ 集成 1,4,5 | `go test ./dino/...` 全绿 | ✅ |
| **S4a** | `dino/session/wake.go`（WakePayload/WakeSource）+ `factory.go` `sessionWakeSource` 适配器 + `Session.run` wakeCh 分支 + `onSubagentCompletion` + 单测 19-20 | `go test ./dino/session/ ./dino/`；集成 3 | ✅（**独立 commit，唯一动调度的一刀**） |
| **S4b** | `Event.Source` + `executeWithInput` displayContent 标记 + 单测 21 | 集成 3 增强 + UI 检查 | ✅ |

**S3 是否需要 P2.3 先定**：是。`spawn_agent`/`wait_agent` 工具名与 `delegate_to_agent` 保留决策是 S3 全部 schema 与 prompt 引导的前提。本设计已按 P2.3=新工具族定稿（§3），若 P2.3 改判为"扩 delegate schema"，S3b/S3c 需返工（影响面：tools_spawn.go + factory 注册 + prompt，mailbox/notifier 不受影响）。

每个 S 独立 commit（Conventional Commits，本地分支，不 push）。S3a-S3c 不碰执行模型，风险最低；S4a 单独 commit 以便回滚。

---

## 12. 风险点

1. **S3 完成通知惰性可见（评审 R6 已明示）**：纯 S3 下父代理要等下一用户 turn 才消费 mailbox。缓解：S4 作为 S3 价值兑现点；S3 测试按 mailbox 状态断言，不假设自动唤醒。
2. **wait_agent 挂起**：`executeToolWithTimeout` 超时后 goroutine 继续跑（`agent_tools.go:41-43` trade-off），若 wait_agent 阻塞在 `time.After` 而 engine 层 timeout 更短，goroutine 会残留到 engine timeout 后由 ctx.Done 返回。缓解：wait_agent 显式 `case <-ctx.Done()`（§5.2），engine timeout → ctx cancel → 立即返回，goroutine 最短存活。
3. **mailbox cap 满丢结果**：spawn 大量未消费时 cap 满，`Put` 降级为事件。缓解：cap 默认 64（对齐 codex 会话线程上限量级）+ 日志；记录为已知行为。
4. **Put 与 SubscribeOnce 竞态**：wait_agent 先 Drain 后订阅的窗口。缓解：mailbox 一把锁串起 `Peek/Put/SubscribeOnce`（§5.3），`-race` 单测覆盖。
5. **B2 唤醒 vs 用户输入**：idle 时 select 随机。缓解：唤醒可重入（DrainAll 前消息不丢）、注入 turn 无面向用户副作用、`Event.Source` 标记供 UI 过滤（§7.4/7.7）。
6. **B2 动 `Session.run` 调度**：影响所有现有 turn/queue/planner 行为。缓解：S4a 独立 commit + 全量 `go test ./dino/...` + queue 模式已知行为记录（§7.4）。
7. **`dino/session` 依赖 `dino/agent`**：若 B2 让 session 直接碰 `*DelegateResult` 会破坏 §1.4 解耦。缓解：session 只见 `WakePayload`（纯文本，包内定义），`DrainAll`/`Truncated` 全在 `dino/factory.go` 的 `sessionWakeSource` 适配器完成（§7.2，§9 决策 D5）。
8. **每 session 一个 mailbox 的构造点**：`CreateSession`（`factory.go:696`）需在 `NewSession` 前拿到 mailbox 实例。缓解：factory 的 session 表（`sessions map`）旁挂 `sessionMailboxes map[sessionID]*Mailbox`，`CloseSession` 时删除 + Drop（`721` 附近）。
9. **model 覆盖（spawn_agent `model` 字段）**：不同 model 需要不同的 `LLMProvider`。缓解：S3 仅支持空（继承父 provider）；非空时在 factory 校验/忽略并返回 error，roadmap 再支持。
10. **工具结果缓存毒化**：`spawn_agent`/`wait_agent` 必须 `no_cache`（§5.2 结尾），否则 engine toolCache 按 task_id 缓存静态信封（S3b 单测 14 覆盖）。

---

## 13. 关键决策摘要（给评审）

1. **P2.3 定稿：新工具族** `spawn_agent`/`wait_agent`，保留 `delegate_to_agent`（存量 prompt 兼容）。spawn 的 `task_id` = `DelegateResult.TaskID`，wait 用 task_id 精确取回，比 codex 按名称寻址简单一层。
2. **mailbox 独立可注入、每 session 一个**（评审 R5）：不挂 `SubagentManager`；factory 在 `CreateSession` 构造、`CloseSession` Drop；键 = task_id；cap + TTL 防塞爆/防孤儿。
3. **wait_agent = 工具内阻塞订阅 mailbox**，`case <-ctx.Done()` 防挂起，返回 `{completed, timed_out, message}`，`message` 直接是 `DelegateResult.Truncated`。
4. **completion notifier = mailbox.Put + 实例级 Bus 旁路事件**；截断走 `Truncated(CompletionMaxRunes)`（默认 2000 runes≈1000 tokens）+ 错误态回退文案（`result.go` 已实现，复用）。
5. **B2 用 `Wake() <-chan struct{}` + payload 纯文本通道注入**，`Session.run` select 加 wakeCh 分支；唤醒可重入（DrainAll 前不丢）；`Event.Source` 标记 UI。**保持 `dino/session` 不依赖 `dino/agent` 类型**（决策 D5）。
6. **迁移 S3a→S3d→S4a→S4b**，S4a 独立 commit（唯一动调度的一刀，可回滚）；S3 依赖 P2.3 先定。

---

## 14. 后续 roadmap（简述，不在本轮范围）

1. **send_message（QueueOnly）/ followup_task（TriggerTurn）**：mailbox 泛化为 `InterAgentMessage` 队列；`trigger_turn` 标志区分"注入不唤醒" vs "唤醒开新任务"。**依赖 S4 的注入载体**（`Wake()` 已有）。
2. **list_agents / close_agent / depth limit**：`AgentPath` 多层级（`path.go` 已铺一层）+ `Manager.list/close`（`Manager.CloseAgent` 已存在 `subagent.go:352-359`，需暴露成工具）。
3. **权限 restrict-only**：子代理权限 = 父 ∩ 子（`filterTools` `subagent.go:268-280` 改交集）。
4. **持久化 spawn 边 + InteractionEdge trace**：SQLite + 崩溃恢复。
5. **S5 prompt 引导**：父代理"先本地再委派/并行 spawn/报告文件路径"（`general.txt`）；`FilesChanged` 程序化采集已就绪（`subagent.go:224-241`），prompt 兜底待加。
6. **树级 budget + LRU 驻留**：`RolloutBudget` 整树预算。

---

## 15. 遗留待定点（需用户/评审拍板）

1. **P2.3 最终确认**：本设计按"新工具族"定稿。若改判"扩 delegate schema"，影响面见 §11。
2. **B2 是否需要 product 层开关**：`NotifyCompletion: true` 已存在（`config.go:248`），但"父代理自动醒来"是否要独立开关（如 `wake_on_completion`，默认 false → true 灰度）？默认开还是默认关需拍板。
3. **mailbox cap / TTL 默认值**：cap 64 / TTL 0（session 生命周期内不过期）是否合理？`review-subagent.md` 建议 TTL 跟随 session 生命周期，本设计默认不开 TTL，可再确认。
4. **`Event.Source` 字段**：新增字段是否会被现有 UI/harness 消费方破坏（默认 `user`，向后兼容）？需 grep 消费方。
5. **queue 模式下唤醒行为**（§7.4）：默认不特殊处理（随机、可重入），是否需要显式"queue 模式禁用唤醒"配置？
6. **`model` 覆盖**：S3 仅支持继承；是否需要 S3 就支持 provider 切换（额外工作量）。
