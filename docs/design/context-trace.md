# 上下文存储 + Trace 事件日志：设计方案（对标 Codex rollout-trace）

> 评估报告出处：[`docs/optimization-review-vs-codex.md`](../optimization-review-vs-codex.md) 第十章（harness engineering，10.1 trace 记录 + 离线回放 / 10.2 token + 计时分相）。
> 前置事实：评估报告第十章「**cortex 完全没有 trace / 结构化指标底座**，10.1 是缺失最大的一块」。
> 范围：**只设计，不改业务代码**。落地时改动文件清单见 §9，全部集中在 `agent/`（引擎挂钩点）、`dino/trace/`（新包）、`dino/factory.go`（接线）、`dino/session`（编排事件）、`cmd/trace-replay/`（新 CLI）。
> 分支：`ctx-trace-design`。

---

## 0. 目标与范围决策

### 0.1 要设计什么

把 **agent 会话的完整事件流**（LLM 调用、工具执行、token usage、compaction、消息、编排事件）落盘成**不可变 JSONL 事件日志**，支撑：

1. **离线回放**：把原始事件还原成「模型实际看到了什么」的可读对话/工具序列（评估报告 10.1 的 reducer 轻量版）。
2. **后续 eval 底座**：每个 trace 事件携带 token / 耗时字段，replay 聚合出逐 turn / 逐工具 / 逐 phase 账本（评估报告 10.2）。
3. **事故定位**：完整证据链，崩溃后可回放不丢事件。

### 0.2 明确取舍：上下文存储本身改不改（ResponseItemEnvelope 化）

**本轮不做** ResponseItemEnvelope 化，**只做 trace 旁路记录**。理由：

- Codex 的 `ContextManager`（`Arc<Vec<ResponseItemEnvelope>>`，`core/src/context_manager/history.rs:49-72`）把**内存态**改造成结构化事件流是**侵入式重构**：它要求 `prepareMessages`/`buildNextMessages`/compaction/chatstore 全部换成结构化 item 模型，改动面横跨 `agent/engine` + `dino/chatstore`，且与正在使用的压缩链路（`dino/chatstore/memory.go` Hybrid、`agent/engine/compaction.go` 三段式 trim）纠缠。
- 而 trace 是**旁路**：只"记录"不改"存储"，用评估报告自己的话（10.1）：**「原始证据 ↔ 语义图分离」**——trace 记录原样发生的事件，reducer 在离线侧还原语义，**运行期不需要结构化上下文模型**。

**分工结论**（本设计的核心立场）：

| 层 | 职责 | 是否本轮 | 证据 |
|---|---|---|---|
| `dino/chatstore` `messages` 表 | **状态**：`prepareMessages` 每轮读取的上下文事实来源（`agent/engine/agent_execution.go:1024-1031` → `memory.GetChatHistory`） | 保留不动 | `dino/chatstore/sqlite.go:107-127` |
| **trace JSONL（新增）** | **记录**：不可变事件日志，与 chatstore 无读写关系 | ✅ 本轮 | — |
| 内存 `ResponseItemEnvelope` 化 | 上下文模型升级（Codex 三层第 1 层） | **后续** | 见 §8「后续」 |

**为什么 trace 是「记录」不是「存储」**：chatstore 的 `messages` 表回答"模型下一轮将看到什么"（且会因压缩删行、只存窗口）；trace JSONL 回答"这一轮实际发生了什么"（完整、不可变、逐事件）。两者正交，**本轮绝不互相读写**。后续若要做「trace → chatstore 恢复」（崩溃续跑），走已存在的 `memoryAdapter.ReplayMessages`（`dino/factory.go:212-230`），见 §8。

### 0.3 Codex 范本与本设计的映射

| Codex（rollout-trace crate） | cortex 本设计 |
|---|---|
| 不可变 `.jsonl` 全量事件日志（`~/.codex/sessions/rollout-*.jsonl`） | `<trace_dir>/trace-<session_id>.jsonl`（§4） |
| 单信封 schema_version/seq/wall_time_unix_ms/rollout_id/thread_id/turn_id（`raw_event.rs:33-43`） | `TraceEvent` 信封（§2） |
| `Disabled`/`Enabled` 上下文句柄，热路径不分支（`thread.rs:76-91`） | 接口注入 + nil-guard（§5，Go 惯用零开销等价物） |
| 先写 payload 后写引用事件（`writer.rs:97-100`） | payload 外置 + 原子 rename 序（§4.4） |
| reducer 还原「模型实际看到了什么」 | `trace-replay` 工具 + `RenderText`/`JSON`（§6） |
| `InferenceCall` 五类 token 账本（`model/conversation.rs:161-193`） | `llm_call_end` 事件携带 `types.Usage`（§8） |

---

## 1. 现状盘点（grep 证据）

### 1.1 无任何 trace / rollout / 事件日志

```
$ grep -rn "rollout\|TraceEvent\|trace-replay\|jsonl\|RolloutWriter" --include="*.go" --include="*.md" .
# 命中全部是评估报告 / 设计文档里的"Codex 侧"描述，无一个 cortex 实现符号
```

**结论**：从零起。评估报告 10.1 的「缺失最大的一块」属实。

### 1.2 现有可复用的"事件流"载体

| 载体 | 位置 | 覆盖 | 缺口 |
|---|---|---|---|
| `ExecuteStream` 的 `resultChan <-chan StreamResult` | `agent/engine/agent_execution.go:901-1012` 返回；`sendStreamResult`（`:156-175`）；`executeStreamIteration`（`:1443-1605`） | chunk/reasoning/tool_event/error/end——**消费方看到的完整流** | ① 非流式 `Execute` 不走此通道（`:763-889`）；② 每轮 token usage（`msg.Usage`，`:1505`）被消费进 `result.Usage`，**不随 stream 事件下发**（只在 `end` 的 `Result.Usage` 出累计值）；③ compaction 在 `prepareMessages` 内，stream 完全不可见；④ 通道满时 `sendStreamResult` 会阻塞/在 ctx 取消时丢（`:169-174`） |
| `hooks.Hooks` 生命周期钩子 | `agent/hooks/hooks.go:23-38` 接口；`Runner`（`:265-322`）；调用点 `agent_execution.go:457,964,1314,1318,1361,1423,1457,1464,1542,1551` | turn/iteration/LLM/tool 的前后事件 | ① **无 token usage**：`AfterLLMCall(response *types.Message)`（`:1547-1551`）构造的 `responseMsg` 只有 `Content`，`Usage` 不经过；② **无 chunk/reasoning 事件**；③ `NewRunner(ae.getHooks(), "", "")`（`:457,937,1314,1457`）**agentID/sessionID 传空串**——session 归属丢；④ 钩子返回 error 但引擎只 `LogError`（`:1319,1341`），做记录通道语义不对 |
| `streamToolCallback` 工具事件 | `agent/engine/agent_execution.go:249-338`，只在 `ExecuteStream` 且有 `resultSender` 时构造（`:927-932`） | tool_call/input_start/input_end/result/error + **Duration** | 非流式 `Execute` 不经过 |
| session `Observer` 编排事件 | `dino/session/session.go:207-232`（`Subscribe`/`notifyObservers`）；`dino/session/event.go:12-53`（`EventType*` + `Event`） | message/thinking/tool_call/tool_result/token_usage/error/done/approval/plan/question/subagent | 在 session 循环 goroutine，不在引擎内；无 iteration 粒度 |
| chatstore `messages`/`metadata` 表 | `dino/chatstore/sqlite.go:107-127`；`AddMessage`（`:209-235`） | **状态**：role/content/tool_calls | 压缩删行、只存窗口，非完整证据 |

### 1.3 关键可复用结构（trace payload 直接借用）

- `types.StreamResult`（`agent/types/agent.go:262-271`）：`Type/Content/Result/Error/ToolEvent/StopCause`。
- `types.ToolEvent`（`agent/types/agent.go:274-283`）：`Event/ToolName/ToolCallID/State/Input/Output/Error/Duration`。
- `types.Usage`（`agent/types/llm.go:45-52`）：`PromptTokens/CompletionTokens/TotalTokens/ReasoningTokens/CachedTokens/CacheCreationTokens`——**5 类 token 齐备**（10.2 账本的原始数据）。
- `types.AgentResult`（`agent/types/agent.go:243-249`）：`Output/ToolCalls/IntermediateSteps/Usage/StopCause`。
- `types.AgentStopCause`（`agent/types/stop_cause.go:10-16`）：`max_iterations/context_window/doom_loop`。
- `dino/session/Event`（`dino/session/event.go:31-53`）：session 层编排事件的现成结构（含 `Timestamp/SessionID/Source`）。
- token 估算 `types.RoughTokenEstimate`（`agent/types/tokens.go:7-18`）。

### 1.4 引擎无 sessionID——归属从 dino 注入

`NewAgentEngine(model, config)`（`agent/engine/agent.go:111-136`）**不接收 sessionID**；sessionID 在 dino 层（`dino/factory.go:661` `CreateSession`）。因此 trace 的 `session_id` 不能由引擎自填，必须由 **dino 层构造 recorder 时绑定**（§3.2 `SetTracer(ctx, sessionID, tracer)`）。

---

## 2. TraceEvent 事件模型

### 2.1 信封（对齐 codex `raw_event.rs:33-43`）

```go
// dino/trace/trace.go（新包，包级常量 + 类型）

// SchemaVersion 是信封 schema 的版本号。语义变更（新增必填字段、改字段含义）
// 时 +1；新增可选字段不 bump。
const SchemaVersion = 1

// TraceEvent 是每行 JSONL 的完整信封（单信封：部分回放安全）。
type TraceEvent struct {
	SchemaVersion  int             `json:"schema_version"`              // = SchemaVersion
	Seq            int64           `json:"seq"`                         // 文件内单调递增（从 1 起），原子分配
	WallTimeUnixMS int64           `json:"wall_time_unix_ms"`           // 事件入队时刻（Unix 毫秒）
	TraceID        string          `json:"trace_id"`                    // 一次 ExecuteStream/Execute 调用 = 一个 turn 段；recorder 见 turn_start 轮转
	SessionID      string          `json:"session_id"`                  // 文件归属；subagent = 父 session + thread 后缀（§4.2）
	TurnID         int             `json:"turn_id"`                     // session 内单调递增的 turn 序（recorder 分配）
	Iteration      int             `json:"iteration,omitempty"`         // 引擎 iteration（llm_call/tool 事件带；-1/缺省 = 非迭代粒度）
	ThreadID       string          `json:"thread_id,omitempty"`         // subagent 层级路径（如 "/root/task_1"），codex thread_id 语义
	ParentTraceID  string          `json:"parent_trace_id,omitempty"`   // subagent 溯源（父 turn 的 TraceID）
	Type           string          `json:"type"`                        // 事件类型（§2.3 枚举）
	Payload        json.RawMessage `json:"payload"`                     // 类型化 payload（§2.4）
}

// Event 是引擎/dino 侧传给 recorder 的最小事件（recorder 负责补信封字段）。
type Event struct {
	Type          string
	Iteration     int
	ThreadID      string
	ParentTraceID string
	Payload       any // 必须可 json.Marshal
}
```

**字段对齐说明**：codex 的 `rollout_id` → cortex 的 `trace_id`（一次 agent 执行）；`thread_id` → `ThreadID`（子代理路径）；`turn_id` → `TurnID`（session 内 turn 序）。新增 `iteration`（cortex 引擎是迭代循环，Codex 的 turn 概念落在 iteration 粒度）。

### 2.2 Recorder / Tracer 接口

```go
// dino/trace/trace.go

// Tracer 是引擎持有的 trace 句柄。nil = Disabled（§5 零开销）。
type Tracer interface {
	// Record 追加一个事件，非阻塞（写 goroutine 异步 drain）。
	Record(ev Event)
	// Flush 尽力把已入队事件落盘（turn_end 时调用，保证整 turn 可读）。
	Flush() error
	// Close 关闭通道、drain、fsync、关文件。幂等。
	Close() error
}

// NewRecorder 构造一个 session 绑定的 recorder（dino 层调用）。
// sessionID 由 dino 层传入（引擎无 sessionID，见 §1.4）。
func NewRecorder(dir, sessionID string, cfg Config) (*Recorder, error)
```

### 2.3 事件类型枚举（Payload 契约）

```go
const (
	// —— 引擎层（agent/engine 记录）——
	EventTurnStart   = "turn_start"    // Payload: TurnStartPayload    一次 ExecuteStream/Execute 开始
	EventTurnEnd     = "turn_end"      // Payload: TurnEndPayload      结束（含 stop cause）
	EventLLMCall     = "llm_call"      // Payload: LLMCallPayload      一次模型调用（开始即记录 messages）
	EventLLMCallEnd  = "llm_call_end"  // Payload: LLMCallEndPayload   模型调用结束（含 Usage/耗时/请求的工具）
	EventLLMChunk    = "llm_chunk"     // Payload: ChunkPayload        合并后的 chunk 文本（默认关，§4.3）
	EventToolCall    = "tool_call"     // Payload: ToolCallPayload     工具开始执行（input + start ts）
	EventToolResult  = "tool_result"   // Payload: ToolResultPayload   工具成功（output + duration + cached）
	EventToolError   = "tool_error"    // Payload: ToolErrorPayload    工具失败（err + duration）
	EventCompaction  = "compaction"    // Payload: CompactionPayload   prepareMessages 里的 trim/摘要注入
	EventMemorySave  = "memory_save"   // Payload: MemorySavePayload   SaveContext/触发压缩
	EventError       = "error"         // Payload: ErrorPayload        stream/迭代错误

	// —— dino 编排层（session.Observer 记录）——
	EventOrchestration = "orchestration" // Payload: *session.Event    planner/approval/question/subagent 等
)
```

### 2.4 Payload 结构（对应 §1.3 可复用结构）

```go
type TurnStartPayload struct {
	Input           types.AgentInput `json:"input"`
	Model           string           `json:"model"`      // ae.model.GetModelName()
	SystemPromptLen int              `json:"system_prompt_len"`
	MaxIterations   int              `json:"max_iterations"`
	ToolNames       []string         `json:"tool_names,omitempty"` // 可见工具名（证据）
}

type TurnEndPayload struct {
	Output     string            `json:"output"`
	Usage      types.Usage       `json:"usage"`       // 累计
	Iterations int               `json:"iterations"`
	StopCause  types.AgentStopCause `json:"stop_cause,omitempty"`
	WallMS     int64             `json:"wall_ms"`     // turn 墙钟
}

type LLMCallPayload struct {
	Messages     []types.Message `json:"messages"`      // 引擎实际发给模型的完整输入（§4.3 体积控制）
	Tools        []string        `json:"tools,omitempty"`
	EstTokensIn  int             `json:"est_tokens_in"` // types.RoughTokensForMessage 估算
}

type LLMCallEndPayload struct {
	Usage       types.Usage `json:"usage"`          // msg.Usage（含 cached/cache_creation/reasoning）
	DurationMS  int64       `json:"duration_ms"`
	OutputLen   int         `json:"output_len"`
	Reasoning   string      `json:"reasoning,omitempty"`
	ToolCalls   []types.ToolCallRequest `json:"tool_calls,omitempty"` // 模型请求的工具
	HasError    bool        `json:"has_error,omitempty"`
	Error       string      `json:"error,omitempty"`
}

type ChunkPayload struct {
	Content string `json:"content"`
}

type ToolCallPayload struct {
	ToolName string                 `json:"tool_name"`
	ToolCallID string               `json:"tool_call_id"`
	Input    map[string]interface{} `json:"input"`
	StartWallMS int64               `json:"start_wall_ms"`
}

type ToolResultPayload struct {
	ToolName  string      `json:"tool_name"`
	ToolCallID string     `json:"tool_call_id"`
	Output    interface{} `json:"output"`           // sanitize 前的原始结果（或 truncate 后？见 §4.3）
	DurationMS int64      `json:"duration_ms"`
	Cached    bool        `json:"cached,omitempty"`
}

type ToolErrorPayload struct {
	ToolName  string `json:"tool_name"`
	ToolCallID string `json:"tool_call_id"`
	Error     string `json:"error"`
	DurationMS int64  `json:"duration_ms"`
}

type CompactionPayload struct {
	BeforeCount  int    `json:"before_count"`
	AfterCount   int    `json:"after_count"`
	BudgetTokens int    `json:"budget_tokens"`
	SummaryFolded bool  `json:"summary_folded,omitempty"`
	HasSummary   bool   `json:"has_summary,omitempty"`
	Mode         string `json:"mode"` // "tail_only" | "three_phase" | "none"
}

type MemorySavePayload struct {
	InputRole string `json:"input_role"`
	CompressTriggered bool `json:"compress_triggered"`
	Reason   string `json:"reason,omitempty"` // "threshold" | "compact_after_turns" | ""
}

type ErrorPayload struct {
	Message  string `json:"message"`
	StopCause types.AgentStopCause `json:"stop_cause,omitempty"`
}
```

---

## 3. 挂钩点设计（最少侵入方案）

### 3.1 结论先行：两个记录点，都不碰 `ExecuteStream` 的 resultChan 主循环

| 记录点 | 位置 | 记录什么 | 为什么 |
|---|---|---|---|
| **引擎钩子（新增 `TraceHooks` 实现 `hooks.Hooks`）** | `agent/engine` 现有 hookRunner 调用点 | `turn_start`/`llm_call`/`llm_call_end`/`tool_*`/`error`/`turn_end` | hooks 已在热路径（`agent_execution.go:457,964,1314,1318,1361,1423,1457,1464,1542,1551`），零新增调用点；但需**扩两个钩子签名**（§3.2） |
| **编排 Observer（session 侧）** | `dino/session/session.go:207-232` `Subscribe` | `orchestration`（planner/approval/question/subagent） | session 层已有 observer 机制，与引擎钩子正交 |

**为什么不在 `ExecuteStream` 的 resultChan 旁路**（评估报告 10.1 的直觉方案）：

1. **非流式 `Execute` 缺失**：`Execute`（`agent_execution.go:763-889`）不走 resultChan，trace 会漏掉所有同步调用（subagent 的父代理 `delegate_to_agent` 走 `ExecuteStream`，但裸引擎 `Execute` 调用方存在）。
2. **token usage 不在流事件里**：每轮 `msg.Usage`（`executeStreamIteration` `:1505-1507`）只累计进 `result.Usage`，`end` 事件才下发累计值（`:1425-1428`）——**逐轮 token 账本（10.2）在流旁路拿不到**，必须在 `executeStreamIteration` 的 LLM 调用点记录。
3. **compaction 不可见**：`prepareMessages` 内的 trim/摘要（`agent_execution.go:1084-1122`）完全在 resultChan 事件流之外。
4. **通道满会丢**：`sendStreamResult` 在 ctx 取消时 drop（`:169-174`），trace 不能依赖「能过消费通道」的事件。

**结论**：hooks + 少量侵入点（`executeIteration`/`executeStreamIteration` 的 LLM 调用点、`prepareMessages` 压缩点）是最小、最完整的方案。resultChan 旁路留作 §3.4 的「可选补充」。

### 3.2 引擎侧改造（两处小侵入 + 一个 setter）

**① 新增 trace 字段 + setter**（`agent/engine/agent.go`）：

```go
// AgentEngine 新增字段：
tracer dino_trace.Tracer // nil = disabled。接口注入，不用全局开关（§5）

// 注意：agent/engine 不 import dino（评审 BLOCKER B1 先例）。因此 Tracer 接口
// 定义在 agent 侧可见处——放 agent/hooks 或新建 agent/trace。见 §3.2.4。
func (ae *AgentEngine) SetTracer(ctx context.Context, sessionID string, t trace.Tracer)
```

**② 扩两个 hooks 签名**（`agent/hooks/hooks.go`，加尾部可选参数，兼容现有 10 个实现——`HooksFunc`/`NoOpHooks`/`ChainHooks` 只需加参数透传，dino 现有 `WithHooks` 用 `HooksFunc` 匿名函数不受影响）：

```go
OnAfterLLMCall(ctx, hc, response *types.Message, usage *types.Usage, duration time.Duration, toolCalls []types.ToolCallRequest)
OnAfterToolCall(ctx, hc, toolName string, output interface{}, err error, duration time.Duration, cached bool)
```

> 设计权衡：加参数会动 10 个实现。备选方案是**不改 hooks 接口**，只在 `executeIteration`/`executeStreamIteration`/`runToolCallsByLayer` 的既有位置直接调 `ae.tracer.Record(...)`（nil-guard，§5）。**本设计推荐备选**——理由：hooks 的 `Runner` 已经给空 agentID/sessionID（`:457,937,1314,1457`），且 hook 语义是"可被外部监听"，而 trace 是"内部必达记录"，混用会引入「记录被 hook 实现方跳过」的不确定性。直接调用点更少、更确定。hooks 扩签名列为 §8 后续选项（若未来需要多观察方）。

**③ 直接调用点**（全部 nil-guard，`ae.tracer != nil`）：

| 调用点 | 事件 | 位置 |
|---|---|---|
| `ExecuteStream` 启动（`BeforeStart` 之后） | `turn_start` | `agent_execution.go:963-971` 后 |
| `Execute` 启动（`waitRateLimit` 后） | `turn_start` | `agent_execution.go:776-781` 后 |
| `executeIteration` `ae.model.ChatWithTools` 前/后 | `llm_call` / `llm_call_end` | `agent_execution.go:1244-1251` |
| `executeStreamIteration` `ChatWithToolsStream` 前/后 | `llm_call` / `llm_call_end` | `agent_execution.go:1466-1471` + `:1547-1553` |
| `runToolCallsByLayer` 内 success/fatal/err | `tool_call`/`tool_result`/`tool_error` | `agent_execution.go:555,571-577,591-595`（已有 `stepResult.duration`） |
| `prepareMessages` 三段式 trim | `compaction` | `agent_execution.go:1107-1111` |
| `saveToMemoryAndMaybeCompress` | `memory_save` | `agent_execution.go:652-749` |
| stream/迭代错误 | `error` | `agent_execution.go:1323-1347,1439-1543` |
| `ExecuteStream` defer（`close(resultChan)` 前）/ `Execute` 返回前 | `turn_end` | `agent_execution.go:955-958` / `:888` |

**④ 接口放哪**：`Tracer` 接口不 import dino（评审 BLOCKER B1 先例——`agent/engine` 不得 import `dino/`）。放 `agent/hooks` 包（hooks 已定义引擎侧接口，`agent_execution.go:14` 已 import）。dino 侧 `dino/trace/recorder.go` import `agent/hooks` 实现接口。**无 import cycle**（`dino → agent/hooks` 单向）。

### 3.3 dino 编排层：session Observer 旁路

```go
// dino/trace/recorder.go
type orchestrationObserver struct{ r *Recorder }
func (o orchestrationObserver) OnEvent(ev *session.Event) {
	if ev == nil { return }
	o.r.Record(trace.Event{Type: trace.EventOrchestration, Payload: ev})
}
```

接线：`dino/factory.go` `CreateSession` 里 `sess.Subscribe(orchestrationObserver{...})`（`session.go:207-232`）。**零侵入**——observer 机制已存在，factory 已持有 `sessionID`。

### 3.4 可选补充（后续）：resultChan 旁路

若未来需要「回放精确到消费方看到的每条 chunk/tool_event」，可在 `ExecuteStream` 的 goroutine 内对每个 `sendStreamResult` 包装一处 `tracer.Record`。**本轮不做**——chunk 粒度体积大（§4.3），且 hooks + 调用点已覆盖语义事件。

### 3.5 subagent 溯源（用户提的 dino/ 优化点）

`dino/agent/subagent.go:136-213` 每次 `Execute` 构建**全新 `AgentEngine`**（`:141`），`ExecuteStream`（`:160`）后 hook 已用于数迭代（`:147-158`）。trace 对 subagent 的集成：

- **同一 `Recorder`**：子代理执行在父 session 内，`thread_id` = 子代理 `AgentPath`（S3 `dino/agent/manager_subagent.go` `AgentPath`），`parent_trace_id` = 父 turn 的 `TraceID`。
- **trace_id 轮转**：子代理每次 `Execute` 调用 = 一个独立 `trace_id`（`subagent.go:160` 每次新 turn），`parent_trace_id` 指向父 trace。S3 `Spawn`（`manager_subagent.go:89-120`）后台执行时同样带 `thread_id` 与 `parent_trace_id`。
- **归属传递**：`dino/factory.go` 的 `sessionWakeSource`（`:267-321`）等旁路事件不受影响——subagent 的 trace 走引擎钩子，天然带 `ThreadID`。
- **S3 `InteractionEdge` 等价物**：`spawn_agent`/`wait_agent` 工具的调用/结果会以 `tool_call`/`tool_result` 记录在父 trace 中，`tool_result` payload 里携带子代理 `Result`（含 `task_id`）——离线 reducer 据此配对父子（§6.3）。这是评估报告 13.2⑦ `InteractionEdge` 的轻量等价。

---

## 4. 落盘策略

### 4.1 路径与文件名

```
<trace_dir>/trace-<session_id>.jsonl          # 主事件日志（不可变 append-only）
<trace_dir>/.trace-<session_id>.jsonl.tmp     # 写缓冲文件（轮转时用）
<trace_dir>/payloads/<trace_id>-<seq>.json    # 重 payload 外置（§4.4，默认关）
```

- **默认 `<trace_dir>`**：`./dino_sessions/traces`（与 `chatstore` 的 `PersistDirectory` 同根，`dino/chatstore/sqlite.go:56` 默认 `./dino_sessions`）。可用 `TRACE_DIR` 或 dino `Config.Trace.Dir` 覆盖（§11 文件清单 / §13 待定点 1）。
- **`session_id` 转义**：sessionID 可能含 `/`（如 `NewIsolatedTaskSessionID` 前缀），文件名替换 `/`→`_`。subagent 与父 session 共用同一文件（§4.2）。

### 4.2 文件组织：subagent 不分文件

Codex 每 rollout 一个文件；cortex 的 `session_id` 是**对话容器**（多 turn 累积），`trace_id` 是**一次执行**（turn 段）。本设计：

- **每 session 一个文件**：`trace-<session_id>.jsonl`，父 turn 与所有子代理执行**追加在同一文件**。
- 区分靠信封字段：`trace_id`（一次执行）、`thread_id`（子代理路径）、`parent_trace_id`（溯源）。`trace-replay --session <id>` 按 `session_id` 过滤，`--trace <trace_id>` 精确到单次执行。

**为什么不分文件**：cortex 的 session 是进程内多 turn 结构（`dino/session/session.go:100-123`），一次 dino 运行 = 一个 session = 多个 `ExecuteStream` 调用。每个调用开新文件会造成碎片化；同文件 + 信封字段在回放时用 `trace_id` 切割，等价且更易遍历。

### 4.3 写策略：异步批写 + 事件体积控制

**写路径**（`dino/trace/recorder.go`）：

```
engine/hook 调用 Record() ──非阻塞投递──▶ events chan (容量 Config.QueueSize, 默认 256)
                                            │
                                    writer goroutine（1 个）
                                            ▼
                              bufio.Writer → 每 Config.FlushInterval(默认 200ms) 或
                              len(events) ≥ Config.BatchSize(默认 512) 时 Flush
                                            ▼
                              os.File.Write + 周期 Sync（每 turn_end 一次 fsync）
```

**阻塞语义**：`Record()` 用 `select { case ch <- ev: default: }` **非阻塞丢弃 + 计数**——trace 是旁路，绝不能阻塞/拖慢引擎（与 `sendStreamResult` 满则丢同类，但 trace 丢失是**可接受的降级**，因为 10.1 的价值在「大多在、可回放」；§7 风险列了改进项）。`Flush()` 在 `turn_end` 时同步等待 writer drain + fsync，**保证每个已完成的 turn 可读**。

**事件体积控制**（默认全开，避免 JSONL 膨胀到影响 eval 与磁盘）：

| 事件 | 默认 | 开关 | 理由 |
|---|---|---|---|
| `llm_call` 的完整 `Messages` | **截断**：每条消息内容 `TruncateString(…, 4096)` + 工具定义只存名字 | `Config.CaptureFullMessages`（默认 false） | 工具输出可 120KB+（`WrapToolResultLimiter`，`dino/factory.go:1176`），全量进 trace 会爆炸 |
| `tool_result` 的 `Output` | **截断**：`types.TruncateToolResult` 同参数（`agent/types/truncate.go`）+ 存 `original_bytes`/`original_tokens` 计数 | `Config.CaptureFullToolOutput`（默认 false） | 同上；`types.FormatToolResult`/`TruncateToolResult` 已有截断基建 |
| `llm_chunk` 逐 chunk | **默认关** | `Config.CaptureChunks`（默认 false） | 逐 token 进 trace 体积最大；合并 chunk 有 50ms 缓冲（`agent_execution.go:21-30`）也不够省 |
| `orchestration` | **全量** | — | session 事件小 |

> 后续要精确回放「模型看到完整工具输出」时再开 `CaptureFullToolOutput`；本轮截断 + 计数已满足 10.2 账本与语义还原。

### 4.4 「先写 payload 后写引用事件」的保证

Codex `rollout-trace/src/writer.rs:97-100` 的核心是**崩溃后回放不指向缺失文件**。cortex 默认单文件 JSONL 下，事件即 payload，无「先写 payload 后写引用」问题。**外置重 payload 时**（`Config.ExternalizePayloads`，默认关）：

1. **先** `os.CreateTemp(payloads/)` → 写入 payload JSON → `fsync` → `os.Rename` 到 `payloads/<trace_id>-<seq>.json`（**原子**）。
2. **再** 写引用事件（`"payload_ref": "payloads/<trace_id>-<seq>.json"`，`TraceEvent` 加 `PayloadRef string` 可选字段）到主 JSONL。
3. 回放遇 `payload_ref` 读不到文件 → 明确报「payload 缺失（seq=…）」而不是静默成功。

崩溃窗口内最多丢失**一个未 rename 的 payload 及其引用事件**——主文件永不指向缺失文件（rename 在前）。单文件模式（默认）下，崩溃丢最后一批 `events chan` 里未 flush 的事件，已 flush 的完整可回放。

### 4.5 清理 / 生命周期

| 时机 | 动作 |
|---|---|
| `Recorder.Close()`（`dino/factory.go` `CloseSession`/`Shutdown` 调用） | drain chan → flush → fsync → close 文件 |
| 磁盘水位（`Config.MaxBytes`，默认 512MB/session） | writer 达到后 `Close` 当前文件 → `os.Rename` 为 `<name>.1.jsonl`（原子轮转）→ 开新文件。回放按 `trace-*.jsonl` + `trace-*.1.jsonl` 按 seq 归并 |
| 长期保留 | 暂不自动清理（Codex 保留在 `~/.codex/sessions`）。后续 eval 归并任务再决定 TTL（§8） |
| 进程崩溃 | `events chan` 中未写事件丢失（有 `dropped_events` 计数）；已 flush 事件完整。`turn_end` 的 fsync 保证整 turn 可读 |

---

## 5. 零开销（Disabled）设计

### 5.1 核心机制：nil 接口，而非全局开关

Codex 用 `Disabled`/`Enabled` 枚举让热路径不分支（`thread.rs:76-91`）。Go 的等价物是**接口注入 + nil-guard**：

```go
// 引擎持有：AgentEngine.tracer 类型为 dino_trace.Tracer（接口），默认 nil。
// 调用点形态（每个记录点）：
if ae.tracer != nil {
    ae.tracer.Record(trace.Event{Type: trace.EventLLMCall, Iteration: iteration, Payload: ...})
}
```

**为什么不全局开关**（`if enabled` 包全局）：

- 全局开关是**分支**：每个调用点 `if cfg.TraceEnabled` 仍是分支，且**无法在编译期去除**；更糟的是全局开关被测试/并发修改时产生数据竞争。
- nil 接口的 `if ae.tracer != nil` 是**一次指针比较**（cmp $0, 地址），现代 CPU 上预测分支成本趋近零；`tracer` 字段本身是 nil 指针，**无分配、无锁**。
- 语义清晰：不 trace 时**引擎里完全没有 recorder 对象**，dino 层也不构造/不开文件/不起 goroutine。

### 5.2 构造侧：只有 dino 层开启

```go
// dino/factory.go CreateSession
if f.config.Trace.Enabled {
    r, err := dino_trace.NewRecorder(f.config.Trace.Dir, sessionID, cfg)
    if err != nil { logger.Warn(...); /* trace 失败不阻断会话 */ }
    else {
        agent.SetTracer(ctx, sessionID, r)         // engine.tracer 非 nil
        sess.Subscribe(dino_trace.NewOrchestrationObserver(r)) // session 编排事件
        // recorder 生命周期绑 CloseSession（f.CloseSession / f.Shutdown）
    }
}
```

`NewRecorder` 失败（磁盘不可写）→ 记日志 + 继续（trace 是旁路，失败降级为 Disabled，**绝不阻断 agent 会话**）。

### 5.3 零开销断言（测试）

```go
// dino/trace/recorder_test.go
func TestDisabledTracerZeroOverhead(t *testing.T) {
    eng := engine.NewAgentEngine(mockLLM, types.NewAgentConfig())
    if eng.tracer != nil { t.Fatal("tracer must default to nil") }
    // 不调用 SetTracer 跑一轮 ExecuteStream：断言无文件产生、无 goroutine 泄漏
    // （runtime.NumGoroutine 前后对比）、无分配（testing.AllocsPerRun 或 -benchmem）
}
```

---

## 6. 离线回放：`trace-replay` 工具

### 6.1 CLI 形态

```
go run ./cmd/trace-replay --dir <trace_dir> --session <session_id> [--trace <trace_id>] [--thread <thread_id>] [--format text|json] [--jsonl]
```

- **默认 `text`**：可读的对话/工具序列（§6.2）。
- **`--format json`**：语义图（§6.3），供脚本/eval 消费。
- **`--jsonl`**：原样吐 `TraceEvent` 行（调试用）。

### 6.2 `RenderText`：还原「模型实际看到了什么」

按 `trace_id`（或 session）顺序折叠事件：

```
════ Turn trace_12345 · session abc · 2 iterations · 3 tools ════
→ USER   「修复 flaky test」
  · model: gpt-4o · input_est=8.2k tok
  · llm_call (iter 0) → output=256 B · usage: prompt=8100/cache=2100/write=6000/comp=280/reason=0 · 1.2s
  └ TOOL read_file(/tests/e2e_test.go) [call_1] → 2.1s · 4.2KB (orig 9.1KB) · exit=0
  └ TOOL bash(go test ./...) [call_2] → 12.4s · 3.1KB
  · llm_call (iter 1) → output=1.8KB · usage: ... · 3.4s
  → final: "Fixed: added cleanup in teardown."
════ 总计: 4 calls · prompt=16.3k / comp=2.1k · wall=19.7s ════
```

**reducer 规则**（语义图轻量版，对应评估报告 10.1「reducer 还原成模型实际看到了什么」）：

1. 每 `llm_call` 用其 `Messages`（或截断后的）还原**该轮上下文**；`llm_call_end` 补充输出/tool_calls。
2. `tool_call` → `tool_result`/`tool_error` 按 `tool_call_id` 配对；配不上的标 `[dangling]`。
3. `turn_end` 输出 `final` 行 + 聚合账本。
4. `orchestration` 事件按时间戳穿插（planner/approval/question 标记）。
5. **subagent**：`thread_id != ""` 的 `llm_call`/`tool_*` 缩进为子树；`parent_trace_id` 提供 DAG 边（§3.5）。

### 6.3 `JSON` 语义图：eval 消费的结构化还原

```go
// dino/trace/replay.go
type ReducedTurn struct {
    TraceID    string        `json:"trace_id"`
    SessionID  string        `json:"session_id"`
    ThreadID   string        `json:"thread_id,omitempty"`
    ParentTraceID string     `json:"parent_trace_id,omitempty"`
    Input      string        `json:"input"`
    FinalOutput string       `json:"final_output"`
    Iterations []ReducedIteration `json:"iterations"`
    Usage      types.Usage   `json:"usage"`
    WallMS     int64         `json:"wall_ms"`
    StopCause  string        `json:"stop_cause,omitempty"`
}
type ReducedIteration struct {
    Index     int          `json:"index"`
    LLMCalls  []ReducedLLMCall `json:"llm_calls"`
    ToolCalls []ReducedToolCall `json:"tool_calls"`
}
type ReducedLLMCall struct {
    Output     string        `json:"output"`
    Reasoning  string        `json:"reasoning,omitempty"`
    Usage      types.Usage   `json:"usage"`
    DurationMS int64         `json:"duration_ms"`
    Tools      []string      `json:"tools,omitempty"`
}
type ReducedToolCall struct {
    ToolName string `json:"tool_name"`
    Input    any    `json:"input"`
    Output   any    `json:"output,omitempty"`
    Error    string `json:"error,omitempty"`
    DurationMS int64 `json:"duration_ms"`
    Cached   bool   `json:"cached,omitempty"`
}
```

**这对 eval 的直接价值**：runner（`dino/runner/engine.go`）已有 `TurnSnapshot`/`TaskResult`，但那是运行期快照；`trace-replay --format json` 给出**可跨进程重放**的结构化轨迹，10.2 的「时间花在哪 / 每次推理多少 token」从 `ReducedTurn.Usage` + `ReducedLLMCall.DurationMS` 直接聚合。

---

## 7. 与 chatstore 的关系（明确分工）

```
┌────────────────────────────┐   ┌──────────────────────────────┐
│ chatstore.messages 表      │   │ trace JSONL（新增）           │
│  = 状态层                   │   │  = 记录层                     │
│  回答问题：下一轮模型看到啥  │   │  回答问题：这一轮实际发生啥    │
│  写入：SaveContext/memoryAd │   │  写入：engine hooks + session │
│        adpter（factory.go:96│   │        Observer（§3）         │
│        -161）              │   │  读取：仅 trace-replay（离线） │
│  读取：prepareMessages      │   │  删除：磁盘轮转/手动           │
│        （agent_execution.go│   │  压缩删行：无（不可变）        │
│        :1024-1031）        │   │                                │
│  压缩删行：Compress 删旧    │   │                                │
└────────────────────────────┘   └──────────────────────────────┘
```

- **不互相读写**：trace 不读 chatstore，chatstore 不读 trace。本设计的分工就是 Codex 的「rollout（trace）≠ state_db（索引）」映射——cortex 的 chatstore 同时扮演了 state_db 的角色（session/thread 归属、queryable 历史），trace 只是补上「不可变事件日志」这一半。
- **后续可选**：`trace-replay` 反哺——把 `ReducedTurn` 写回 chatstore `metadata`（`sqlite.go:118-123`，key `trace_<trace_id>`）做索引；或崩溃续跑时用 trace 重建 messages（走 `ReplayMessages`）。**本轮不做**，避免破坏「trace 是纯记录」的边界。
- **评估报告 12.1 的既有问题不影响本设计**：chatstore 双 memory 收敛（`agent/providers` deprecated）与本设计正交。

---

## 8. 后续 eval 基础（10.2 token + 计时分相）

### 8.1 本轮顺带设计的账本字段

每个 `llm_call_end` 事件已携带 `types.Usage`（5 类 token）+ `DurationMS`；每个 `tool_result`/`tool_error` 已携带 `DurationMS` + `Cached`。这**已覆盖评估报告 10.2 的 `InferenceCall` 账本**（`input/cached_input/cache_write_input/output/reasoning_output`）。`types.Usage` 的字段恰好一一对应：

| 10.2 Codex `InferenceCall` | cortex `types.Usage`（`llm.go:45-52`） |
|---|---|
| input | `PromptTokens`（含 uncached+cached+write，B1 语义） |
| cached_input | `CachedTokens` |
| cache_write_input | `CacheCreationTokens` |
| output | `CompletionTokens` |
| reasoning_output | `ReasoningTokens` |

**本轮不做**的分相计时（`TurnProfile`/`ToolCallTimingGuard`，`core/src/turn_timing.rs:305-351`）：引擎的工具执行时间可以**近似**从 `tool_call`→`tool_result` 的 `wall_ms` 差值得到（`runToolCallsByLayer` 已有 `stepResult.duration`，`agent_execution.go:207-212`）；「dispatch 排队 vs handler 执行」的分离需要动 `runToolCallsByLayer` 的 errgroup 内部——**列为后续**。

### 8.2 聚合器

```go
// dino/trace/aggregate.go（后续，非本轮）
type TurnProfile struct {
    SamplingMS  int64 // Σ llm_call_end.duration_ms
    ToolMS      int64 // Σ tool_result/tool_error.duration_ms
    CompactionMS int64 // Σ compaction 事件附近墙钟（近似）
    OverheadMS  int64 // wall_ms - (sampling+tool+compaction)
    Tokens      types.Usage
}
```

`trace-replay --format json` 输出 `ReducedTurn` 后，聚合器按 `StopReason`（`dino/task/types.go:89-100` 已有 `StopReason*`）分组出效率账本。

---

## 9. 迁移顺序（每步可独立验证）

| 步 | 改动 | 验证 | 独立可用 |
|---|---|---|---|
| **S1 信封 + recorder** | `dino/trace/trace.go`（类型/常量）+ `recorder.go`（chan + writer goroutine + JSONL 落盘 + 体积控制 + 零开销单测）。**不接引擎**，先 `go test ./dino/trace/` 单测 | 信封字段/seq 单调/崩溃半写容错/`Flush` 语义 | ✅ 纯新包，无依赖 |
| **S2 引擎挂钩点** | `agent/hooks` 放 `Tracer` 接口；`agent/engine` 加 `tracer` 字段 + `SetTracer` + 调用点（§3.2③）。跑现有 engine 测试确认**不 trace 时零回归**（nil-guard 生效） | `TestDisabledTracerZeroOverhead`；engine 测试全绿 | ✅ 引擎带 disabled trace 跑一轮 |
| **S3 dino 接线** | `dino/factory.go` `CreateSession`：`Trace.Enabled` 时 `NewRecorder` + `SetTracer` + `Subscribe`；`CloseSession`/`Shutdown` 时 `Close()`。跑一次 dino 会话，确认 JSONL 落盘 | 手动 `go run ./cmd/trace-replay --dir ./dino_sessions/traces --session <sid>` 出可读序列 | ✅ 端到端 trace 产生 |
| **S4 trace-replay** | `cmd/trace-replay/main.go` + `dino/trace/replay.go`（`RenderText`/`Reduce`）。用 S3 的真实 JSONL 做 golden | `go test ./dino/trace/`（replay 单测：构造 trace → 断言 text/json 输出） | ✅ 工具独立 |
| **S5 编排事件 + subagent** | session `Observer` 接 `orchestration` 事件；subagent `thread_id`/`parent_trace_id` 传递 | 子代理场景 replay 出子树缩进 + 父子配对 | ✅ 增量 |
| **S6（后续）** | 重 payload 外置、`trace-replay` 反哺 metadata、批量 eval 聚合、`TurnProfile` | 各自单测 | — |

**S1–S5 各步都向后兼容**：不 trace = 现状（nil tracer + 不 Subscribe）。**每步独立可验证**，不必等整轮完成。

---

## 10. 测试清单

### 10.1 单测

| 测试 | 覆盖 | 断言 |
|---|---|---|
| `TestEnvelopeFields` | `TraceEvent` 信封 | `schema_version=1`；`seq` 从 1 单调递增；`wall_time_unix_ms` 单调非降 |
| `TestJSONLAppend` | recorder 落盘 | 每行是合法 JSON；`Payload` 反序列化回原类型；行序 = 入队序 |
| `TestFlushDurability` | `turn_end` flush | `Flush()` 后读文件：整 turn 事件全在；文件 fsync 后字节稳定 |
| `TestCrashPartialWrite` | 半写容错 | 手工 truncate 文件最后一行（模拟崩溃半写）→ replay 跳过坏行 + 报 `truncated_line`，不 panic |
| `TestDroppedEventsCounter` | 满 chan 丢弃 | 小队列 + 快速灌入 → `dropped_events` 计数准确；引擎不阻塞 |
| `TestDisabledTracerZeroOverhead` | 零开销 | 不 `SetTracer`：无文件、无 goroutine 泄漏、`AllocsPerRun` 不因调用点增加（bench） |
| `TestExternalizePayloads` | 先 payload 后引用 | rename 序：payload 文件先存在，事件引用后写；模拟 payload 缺失 → 明确报错 |
| `TestReplayRenderText` | reducer | 构造 trace → 断言对话/工具序列文本（含缩进、配对、dangling 标记） |
| `TestReplayReduceJSON` | 语义图 | 断言 `ReducedTurn` 字段聚合正确（usage/耗时/iteration 分组） |
| `TestReplaySubagentTree` | 溯源 | `thread_id`/`parent_trace_id` 还原子树 + 父子配对 |
| `TestRotate` | 磁盘水位 | 达 `MaxBytes` → 轮转 `trace-*.1.jsonl`；replay 按 seq 归并 |

### 10.2 集成

- **S3 端到端**：dino 配置 `Trace.Enabled=true` → 会话 → 断言 JSONL 产生 → `trace-replay` 输出可读。
- **不 trace 回归**：现有 engine/dino 测试全绿（tracer 默认 nil）。
- **subagent**：`spawn_agent`/`delegate_to_agent` 场景 trace 包含父子 trace（`dino/agent/result_test.go` 现有测试扩 trace 断言）。

---

## 11. 落地改动文件清单（实施时）

| 文件 | 动作 | 内容 |
|---|---|---|
| `dino/trace/trace.go` | 新增 | `TraceEvent`/`Event`/payload 结构/事件类型常量/`SchemaVersion` |
| `dino/trace/recorder.go` | 新增 | `Recorder`/`NewRecorder`/writer goroutine/`Flush`/`Close`/体积控制/轮转 |
| `dino/trace/replay.go` | 新增 | `RenderText`/`Reduce`/`ReducedTurn*` |
| `dino/trace/recorder_test.go`、`replay_test.go` | 新增 | §10.1 单测 |
| `agent/hooks/hooks.go` | 修改 | 新增 `Tracer` 接口（或新建 `agent/hooks/trace.go`） |
| `agent/engine/agent.go` | 修改 | `AgentEngine.tracer` 字段 + `SetTracer` |
| `agent/engine/agent_execution.go` | 修改 | §3.2③ 调用点（`turn_start/turn_end/llm_call/llm_call_end/tool_*/compaction/memory_save/error`） |
| `agent/engine/agent_config.go` | 修改 | `AgentConfig.TraceEnabled bool`（或放 dino config，§13 待定点 1） |
| `dino/factory.go` | 修改 | `CreateSession` 接线 + `CloseSession`/`Shutdown` 关闭 recorder |
| `dino/config.go` | 修改 | `Config.Trace.TraceConfig`（`Enabled/Dir/QueueSize/FlushInterval/Capture*`） |
| `dino/types.go` | 修改 | （可选）export `Trace` 相关类型 |
| `cmd/trace-replay/main.go` | 新增 | CLI 入口 |

**不在范围内**：`dino/chatstore`（messages 表不动）、`agent/providers`（遗留不动）、`ExecuteStream` resultChan 主循环（不动）。

---

## 12. 风险点与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| **磁盘满 / 写失败** | trace 写入失败 | `NewRecorder`/writer 错误 → 记日志 + 降级为 Disabled（`tracer=nil`），**绝不阻断 agent 会话**；`Close` 幂等 |
| **JSONL 膨胀**（`llm_call` 全量 messages / `tool_result` 全量 output） | 磁盘占用、eval 变慢 | §4.3 体积控制默认截断 + 计数；`CaptureFull*` 显式开关 |
| **chan 满丢事件** | 回放不完整 | 非阻塞丢弃有计数（`dropped_events`）；`turn_end` 的 `Flush` 保证已完成的 turn 完整；§8 后续可改「满时降级为合并事件」 |
| **hook/调用点遗漏**（某工具路径不经过 `runToolCallsByLayer`） | 工具事件缺 | `tool_call/tool_result` 只在 `runToolCallsByLayer` 记录；MCP/审批等 wrapper 在 `dino` 层（`factory.go:1171-1180`）包裹，引擎层仍统一经过 `runToolCallsByLayer`——审查确认无旁路 |
| **引擎无 sessionID** | trace 无法归属 | §1.4 决策：dino 层构造 recorder 绑定；引擎 trace 不感知 sessionID |
| **agent/engine import dino 循环** | 编译失败 | §3.2④：`Tracer` 接口放 `agent/hooks`，dino → agent/hooks 单向 |
| **subagent 每次新建 engine** | trace 归属断裂 | §3.5：同一 recorder + `thread_id`/`parent_trace_id` 溯源；无需改 subagent 构建 |
| **性能**（trace 开时拖慢引擎） | 会话延迟 | 异步写 goroutine 解耦；`Record` 非阻塞；开启后 benchmark 对比（§5.3） |
| **崩溃半写行** | replay 坏行 | replay 跳过 + 报 `truncated_line`；轮转 rename 原子（§4.4） |

---

## 13. 遗留待定点（留给用户的决策）

1. **`Trace` 配置放哪**：`dino.Config.Trace`（推荐，dino 是编排层）还是 `types.AgentConfig.TraceEnabled`（引擎层）+ dino 读配置。本设计倾向 dino 层（sessionID 在 dino，且裸引擎用户默认无 trace）。
2. **`CaptureFullToolOutput` 默认值**：本轮默认关（截断 + 计数）。若要「回放精确还原模型看到的完整工具输出」作为 eval 的硬前提，需默认开（磁盘代价）。
3. **`trace-replay` 反哺 chatstore**（`ReducedTurn` → metadata 索引 / 崩溃续跑重建 messages）：是否纳入，纳入哪个版本。
4. **批量 eval 跑批**（评估报告 10.6）：`trace-replay --format json` 输出 → 按 `StopReason` 聚合通过率的 runner，是否在 trace 落地后单独立项。
5. **`Tracer` 接口 vs hooks 扩签名**（§3.2②）：本设计选了「直接调用点 + nil-guard」。若未来需要多个观察方（UI/审计/telemetry），再考虑扩 hooks 或引入 observer 注册表。
6. **tok 估算 vs 真实 token**：`llm_call` 的 `est_tokens_in` 用 `types.RoughTokenEstimate`（`tokens.go:7-18`），对 CJK 偏差大（评估报告 12.1 已点名）；`llm_call_end` 的 `Usage` 是 provider 返回的真实值——**eval 账本以真实值为准**，`est_tokens_in` 只作证据。
