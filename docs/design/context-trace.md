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
