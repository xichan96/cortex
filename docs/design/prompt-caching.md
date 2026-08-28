# Prompt Caching 落地方案

> 设计文档 · 分支 `opt-prompt-caching` · 2026-08-28
> 对应评估报告第三章《一、差距最大：Prompt Caching（成本影响最大）》（`docs/optimization-review-vs-codex.md:29-47`）。

## 0. 目标与结论摘要

长 agentic 会话（多次 turn、每次 turn 内多轮工具迭代）的成本大头是**重复发送的前缀**：system prompt + 工具定义 + 历史消息。cortex 是 HTTP 轮询（非 WebSocket 增量），无法像 Codex 那样只发增量字节，但 Anthropic 服务端 prompt cache 的命中条件是「请求前缀与上次一致」，**HTTP 全量重发 + 前缀稳定 + `cache_control` breakpoint 同样能吃满缓存**，这是最直接的收益路径。

本方案：

| 主题 | 决策 |
|---|---|
| 前缀稳定性 | 单次 `Execute` 内**天然前缀稳定**（现状已满足）；跨 turn 与 compaction 需额外保留策略（见 §2） |
| breakpoint 放法 | 在 **llm 客户端层**（`agent/llm/anthropic_native.go`）注入 `cache_control`；system / tools / 每 N 条历史各一个 |
| 配置 | 默认**开启**，可配置；`AgentConfig.PromptCaching` + `dino.Config.Memory.PromptCaching` |
| usage 回填 | provider response 的 `cache_read_input_tokens` / `cache_creation_input_tokens` → `Usage.CachedTokens` / 新增 `CacheCreationTokens` |
| OpenAI 兼容 provider | 无 cache_control 协议，仅做 usage 回填（`prompt_tokens_details.cached_tokens`） |
| 增量请求 | **不做**。仅靠前缀稳定 + breakpoint |
| 验证 | 单测 mock SSE + usage 断言 + 可选集成脚本 |

落地分 4 步，每步可独立测试、可回滚（见 §7）。

---

## 1. 现状（已核实，全部基于代码事实）

### 1.1 Usage 字段存在但从未填充

`agent/types/llm.go:41-47`：

```go
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"` // Thinking tokens (o1, o3 series etc.)
	CachedTokens     int `json:"cached_tokens,omitempty"`    // Cache read tokens
}
```

`CachedTokens`（`llm.go:46`）与 `ReasoningTokens`（`llm.go:45`）**全仓无任何写点**。`grep` 确认仅此定义与 JSON tag。

### 1.2 请求构建：system 是拼接字符串，无 cache_control

`agent/llm/anthropic_native.go` 是主 Anthropic 客户端（dino 的 `anthropic` provider 走 `newNativeAnthropicProvider`，见 `agent/llm/anthropic.go:25`）。请求模型：

- `anthropicRequest`（`anthropic_native.go:28-35`）：`System string`、`Messages []anthropicMessage`、`Tools []anthropicTool`，**均无 cache_control 字段**。
- `anthropicContentBlock`（`anthropic_native.go:42-50`）：`Type/Text/ID/Name/Input/ToolUseID/Content`，无 `cache_control`。
- `anthropicTool`（`anthropic_native.go:52-56`）：无 `cache_control`。
- `buildRequest`（`anthropic_native.go:351-462`）：把多个 system 消息**拼接成单个 string**（`355-363`），tools 顺序按传入数组（`443-450`）。system 作为请求顶层字段，无法直接挂 breakpoint（Anthropic system 参数要支持 cache_control 需改为 blocks 数组）。

### 1.3 SSE 解析丢弃缓存字段

`readStream`（`anthropic_native.go:205-348`）：

- `message_start` 事件只取 `InputTokens`（`251-256`）：

```go
case "message_start":
	var ev anthropicMessageStartData
	if json.Unmarshal([]byte(data), &ev) == nil {
		inputTokens = ev.Message.Usage.InputTokens
	}
```

  `anthropicMessageStartData`（`92-100`）的 Usage 只有 `InputTokens/OutputTokens`，**没有** `cache_read_input_tokens` / `cache_creation_input_tokens`。

- `message_delta` 组装 usage（`318-324`）：只填 `CompletionTokens/PromptTokens/TotalTokens`。

### 1.4 引擎 usage 累加点丢弃 CachedTokens

- `Execute`：`agent_execution.go:642-644` 累加 `PromptTokens/CompletionTokens/TotalTokens`。
- `executeStreamWithIterations`：`agent_execution.go:1156-1158`（每次迭代）与 `1212-1214`（最终 totalUsage）。
- `executeIteration`：`1048-1057` 直接把 `response.Usage` 赋给 result，此处是唯一能回填的入口点。

### 1.5 消息数组构建：单次 Execute 内天然前缀稳定

`prepareMessages`（`agent_execution.go:840-903`）组装顺序：

1. system 消息（`862-867`）
2. memory `GetChatHistory` 返回的 history（`843-848, 892-894`）
3. `previousRequests`（`896-898`，本次 Execute 内多次调用场景）
4. 本次 input（`900`）

每次迭代通过 `buildNextMessages`（`agent_messages.go:10-27`）**只追加** assistant + tool 消息，不重排、不改写历史 → 单次 `Execute` 内（含 stream 模式）迭代间前缀完全一致。

### 1.6 破坏前缀稳定性的因素

| 因素 | 位置 | 影响 |
|---|---|---|
| `trimHistoryToTokenBudget` 丢最旧 | `agent_execution.go:987-1017` | 从 history **头部**裁剪 → 前缀截断，后续请求全部缓存失效 |
| `SimpleMemoryProvider.GetMessages` 滑动窗口 | `agent/providers/langchain_memory.go:93-94` | 超出 `maxHistoryMessages` 时从头部截断 |
| `repairLLMMessageToolOrdering` | `agent_execution.go:908-985` | 仅在 trim/压缩后修复；若修复改变头部内容同样影响前缀，但**尾部修复**（丢 orphan tool）通常不影响首个 breakpoint |
| compaction 摘要注入 | `dino/chatstore/memory.go:292-298`（Hybrid）、`langchain_memory.go:74-81` | 摘要作为 **system 消息插在头部**（engine 拼接时在 system 之后），插入位置固定则相对稳定，但摘要内容每次压缩会变 |

**结论**：cortex 单次 agentic 会话（一次 turn 内多次工具迭代）**天然命中缓存**；真正需要设计的是
(a) breakpoint 放在不会因窗口滑动而移动的位置；
(b) 跨 turn 长会话在 compaction 时尽量保留前缀；
(c) usage 回填观测缓存收益。

---

## 2. 设计：前缀稳定性

### 2.1 现状评估：单次 Execute 无需改动

`buildNextMessages` 只追加不重排（`agent_messages.go:24-26`），`prepareMessages` 的头部（system + history 前段）在一次 Execute 生命周期内不变。**迭代 2..N 自动吃到迭代 1 建立的缓存**，前提是 breakpoint 放在头部稳定区（§3 的策略即基于此）。

### 2.2 需要改的两处

#### (a) `trimHistoryToTokenBudget` 保留「缓存锚」——不在预算裁剪路径上动历史头部，而是让 breakpoint 定义在裁剪之后仍稳定的位置

`trimHistoryToTokenBudget`（`agent_execution.go:987-1017`）在 `MaxBudgetTokens` / `RemainPromptTokens` 开启时从头部裁剪。裁剪边界 `start` 由尾部 token 预算反推，随输入长度漂移，**必然改变前缀**。

设计决策：**本方案不把裁剪路径做成前缀保留**（那是 Codex compaction 的完整重设计，超出本任务范围）。相反：
- breakpoint 间隔 N（§3.3）作为**缓存段**，budget trim 只在 `config.PromptCaching.TrimRetainsCacheAnchor` 开启时，保证至少保留「从首个 breakpoint 到当前裁剪点」——但鉴于实现复杂度，**第一步默认关闭此开关**，trim 仍按现状裁剪（缓存失效可接受，budget 优先级更高）。
- 文档记录：`MaxBudgetTokens` 与 prompt cache 互斥优先级 = **budget 优先，缓存尽力而为**。

> 理由：`trimHistoryToTokenBudget` 的核心契约是「严格不超预算」，任何前缀保留都会打破它。真正的解决是 Codex 式「尾部原文 + LLM 摘要 + 摘要放最后」的重设计（`optimization-review-vs-codex.md:66-84`），列为后续任务。

#### (b) 窗口滑动保持首个 breakpoint 稳定

`SimpleMemoryProvider.GetMessages`（`langchain_memory.go:93-94`）窗口滑动截断头部。由于 tools 定义 + system 在请求构建时位于历史之前，**首个 breakpoint 放在 tools 段末尾**（§3.2），它永远在窗口裁剪点之前 → 缓存段 = [tools 开头 … tools 末尾] 在滑动时**始终完整**。

历史段（breakpoint 2 之后）随窗口滑动自然失效重写，这是可接受的：缓存命中的主体是 system + tools（不变）+ 每次 turn 内新增的历史。

### 2.3 compaction 时保留缓存前缀

`dino/chatstore` 的两种压缩路径：

1. **确定性 compact**（`dino/chatstore/compact.go:42-140`，dino 默认 `InMemory` 走 `DeterministicCompact`，见 `memory.go:179-184`）：产出结构化 summary 字符串，`Hybrid.GetMessages` 把它包成 **system 消息插在头部**（`memory.go:292-298`）。
2. **LLM 增量摘要**（`agent/providers/langchain_memory.go:167-254`）：`summary` 存在 provider 上，`GetMessages` 同样包成 system 插头部（`74-81`）。

两条路径都把 summary 放**头部**。对缓存的影响：
- summary 是 system 段的一部分，位于 tools breakpoint 之前 → **summary 变化会导致 system+summary 段整体重写**（含 tools breakpoint 之前的缓存段）。
- 但这只影响「跨压缩边界」的缓存；压缩是低频事件（`CompactAfterTurns` / 阈值触发）。

设计决策：
- **本方案不动 compaction 的位置**（把 summary 移到尾部是 Codex `SUMMARY_PREFIX` 模型，见 `compact.rs:657-733`，属后续任务）。
- 在 `dino/chatstore` 压缩触发时，**不主动清空缓存**（Anthropic 5 分钟 TTL 内，同一会话的后续请求若前缀一致仍能命中）；若压缩改变 summary，仅该段失效，属可接受降级。
- 记录在案：后续若做 Codex 式 compaction 重设计，需把 summary 从「头部 system 消息」改为「尾部 user 消息」，以保护 tools breakpoint 之前的缓存段。

### 2.4 增量请求：不做

cortex 是 HTTP 轮询（`stream` 方法走 POST SSE，`anthropic_native.go:173-202`），没有 WebSocket 长连接可复用。实现 Codex 式增量请求（`client.rs:1336-1374`，靠 `previous_response_id`）需要：
- 会话级连接复用；
- 请求严格扩展判定（`client.rs:404-427` 的字段全等比较）；
- 增量体编码。

收益仅省**上行字节**（TTFB 略降），而缓存命中的成本收益已由「前缀稳定 + breakpoint」完全获得。**明确不做，避免复杂度失控**。若未来引入 WS，本方案的 breakpoint/usage 结构可无缝平移。

---

## 3. 设计：cache_control breakpoint

### 3.1 抽象层选择：llm 客户端层（provider 层之上、业务层之下）

选项分析：

| 层 | 优点 | 缺点 | 结论 |
|---|---|---|---|
| provider 层（`agent/llm/*` 各 client 内部） | 最贴近 HTTP 协议；OpenAI/Anthropic 各自实现，互不干扰 | 每个 provider 各写一遍 | ✅ **选此** |
| llm 客户端层（`agent/llm` 之上包一层中间件） | 单一实现 | 需拦截/重建请求体，与现有 `LLMProvider` 接口（`llm.go:6-18`）不匹配，且无法拿到已序列化的 Anthropic 结构 | ✗ 接口不适配 |
| engine 层 | 无需碰 provider | engine 不应知道 Anthropic 专属字段；会污染 `types.Message` | ✗ |

**决策**：breakpoint 注入在 `agent/llm/anthropic_native.go` 的 `buildRequest`（Anthropic 专属，位置集中）。OpenAI 路径无此协议，不注入。

理由：`LLMProvider` 接口（`agent/types/llm.go:6-18`）的入参是 `[]types.Message` + `[]types.Tool`，Anthropic 协议细节（`cache_control` 是 `anthropicMessage`/`anthropicTool`/system blocks 的字段）**只存在于序列化层**。在 `buildRequest` 注入最小改动、最内聚，且 engine 完全无感。

### 3.2 具体放法（Anthropic Messages API 语义）

Anthropic 缓存规则：
- `cache_control: {type: "ephemeral"}` 可放在 **system blocks**（`system` 改为 `[]content_block` 数组）、**tools 数组的每个元素**、**messages 内容块**。
- 缓存段 = 从 breakpoint 到请求末尾（或下一个 breakpoint）。
- 只有最长的**前缀**会命中；`ephemeral` 5 分钟 TTL。

**breakpoint 布局（每个请求）**：

```
┌─────────────────────────────────────────────────────┐
│ system [cache_control]        ← 段1: system(含summary)│
├─────────────────────────────────────────────────────┤
│ tools[0..n-1] 每个 tool 都带 cache_control            │
│   └─ 语义：tools 段独立成段，且是"最稳定的段"           │
├─────────────────────────────────────────────────────┤
│ messages[0..k] 每 N 条历史消息的最后一条挂 cache_control│
│   └─ 段3+: 历史段，随窗口滑动失效但前缀段1/2始终命中      │
└─────────────────────────────────────────────────────┘
```

规则细节：

1. **system**：`anthropicRequest.System` 从 `string` 改为 `interface{}`（string 兼容旧行为，开启缓存时用 `[]anthropicContentBlock{{Type:"text", Text:..., CacheControl:&{Type:"ephemeral"}}}`）。
2. **tools**：**每个** tool 挂 `cache_control`。这是工具数组中最稳定、且能独立成段的位置。Anthropic 官方推荐对 tools 使用（tools 定义通常远大于 1024 token 的缓存最小阈值）。
3. **历史消息**：`anthropicContentBlock` 加 `CacheControl *anthropicCacheControl`。对**每个 assistant 消息**（其内容块）按「从尾往头数、每 N 条 assistant 消息的最后一块」挂 breakpoint，`N` 默认 `5`。**只挂在 assistant 消息的最后一个 text/tool_use 块上**（tool_result 块不能挂）。

   > 为什么按 assistant 消息计数而不是 user：agentic 轨迹里 user/tool 消息多为短工具结果，assistant 消息往往携带思考与工具调用，是 token 大头；且按 assistant 计数避免 tool_result 不能挂 breakpoint 的坑。

4. **第一个历史 breakpoint 距请求头 ≥ cache 最小 token 阈值**：若开启缓存且 message 段整体 < `MinCacheTokens`（默认 1024，Anthropic 最小缓存单位），则**不挂**（避免无效缓存写）。

### 3.3 struct / 字段改动

`agent/llm/anthropic_native.go`：

```go
// —— 新增 ——
type anthropicCacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// —— 修改 ——
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    interface{}        `json:"system,omitempty"` // string | []anthropicContentBlock
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string                 `json:"role"`
	Content interface{}            `json:"content"` // string | []anthropicContentBlock
}

type anthropicContentBlock struct {
	Type         string               `json:"type"`
	Text         string               `json:"text,omitempty"`
	ID           string               `json:"id,omitempty"`
	Name         string               `json:"name,omitempty"`
	Input        json.RawMessage      `json:"input,omitempty"`
	ToolUseID    string               `json:"tool_use_id,omitempty"`
	Content      string               `json:"content,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	InputSchema  interface{}            `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}
```

`NativeAnthropicProvider` 增加字段：

```go
type NativeAnthropicProvider struct {
	apiKey       string
	baseURL      string
	model        string
	maxTokens    int
	client       *http.Client
	promptCache  PromptCacheOptions // 新增
}

// —— 新增，agent/llm/prompt_cache.go ——
type PromptCacheOptions struct {
	Enabled        bool
	SystemBreakpoint bool
	ToolsBreakpoint  bool
	HistoryEveryN  int  // 每 N 条 assistant 消息挂 1 个 breakpoint；0=不挂历史
	MinCacheTokens int  // 历史段小于该值不挂 breakpoint；默认 1024
}

func DefaultPromptCacheOptions() PromptCacheOptions {
	return PromptCacheOptions{
		Enabled:          true,
		SystemBreakpoint: true,
		ToolsBreakpoint:  true,
		HistoryEveryN:    5,
		MinCacheTokens:   1024,
	}
}
```

### 3.4 构造入口

`newNativeAnthropicProvider`（`anthropic_native.go:122-137`）读取开关（来源见 §5）默认开启：

```go
func newNativeAnthropicProvider(apiKey, baseURL, model string) *NativeAnthropicProvider {
	...
	return &NativeAnthropicProvider{
		...
		promptCache: DefaultPromptCacheOptions(),
	}
}

// —— 新增 setter（供 engine/dino 配置注入）——
func (p *NativeAnthropicProvider) SetPromptCacheOptions(opts PromptCacheOptions) { p.promptCache = opts }
```

`buildRequest` 改造（`anthropic_native.go:351-462`）：
- system 构建：若 `promptCache.Enabled && SystemBreakpoint && system != ""` → `System = []anthropicContentBlock{{Type:"text", Text: system, CacheControl:&anthropicCacheControl{Type:"ephemeral"}}}`，否则保持 string（向后兼容，`json:"system,omitempty"` 的 `interface{}` 反序列化对旧 string 请求不受影响——注意 Anthropic API **接受 string 或 array 两种 system 形式**）。
- tools：若 `ToolsBreakpoint` → 每个 `anthropicTool` 加 `CacheControl`。
- history：序列化消息循环时（`355-438`）按 §3.2 规则给 assistant 消息末尾块挂 breakpoint。
- 若 `!promptCache.Enabled` → 完全保持现状输出，**字节级兼容**（回归零风险）。

### 3.5 兼容性注意点

- 挂 breakpoint 的消息块必须是**数组形式 content**（`[]anthropicContentBlock`）；纯字符串 content 的 assistant 消息需要先转成 block 数组。现有 `mergeConsecutiveRoles`（`anthropic_native.go:466-486`）已统一块数组，新增逻辑在合并**之后**做，保证「assistant 消息块结构」已定型。
- `anthropicContentBlock` 新增字段有 `omitempty`，且只在 `Enabled` 时设置 → 旧请求体不变。
- 部分 OpenAI 兼容代理（走 anthropic 客户端但非真 Anthropic）可能忽略 `cache_control`；这是安全降级（服务端忽略未知字段），不报错。Anthropic API 对未知字段**不报错**（它们本身在 schema 里）。

---

## 4. 设计：token 计费与 usage 回填

### 4.1 回填时机与归属

回填发生在 **provider response 解析处**（`readStream` / `collectStream`），由 `NativeAnthropicProvider` 完成。engine 层只需把 `Usage` 原样透传并在累加时把新字段一并累加。

### 4.2 Anthropic response 映射

Anthropic SSE `message_start` 的 usage 实际字段：

```json
{ "message_start": { "message": { "usage": {
    "input_tokens": 3000,
    "cache_creation_input_tokens": 1500,
    "cache_read_input_tokens": 1200,
    "output_tokens": 500
}}}}
```

（`message_delta` 只给 `output_tokens`。）

映射到 `types.Usage`：

| Anthropic | `types.Usage` | 说明 |
|---|---|---|
| `input_tokens` | `PromptTokens` | 总输入（含缓存读写） |
| `output_tokens` | `CompletionTokens` | 输出 |
| `input_tokens + output_tokens` | `TotalTokens` | 现状已算 |
| `cache_read_input_tokens` | `CachedTokens` | ✅ 现有字段，本方案**首次填充** |
| `cache_creation_input_tokens` | **`CacheCreationTokens`（新增字段）** | 用于可观测「写缓存花了多少」 |

`agent/types/llm.go`：

```go
type Usage struct {
	PromptTokens         int `json:"prompt_tokens"`
	CompletionTokens     int `json:"completion_tokens"`
	TotalTokens          int `json:"total_tokens"`
	ReasoningTokens      int `json:"reasoning_tokens,omitempty"`
	CachedTokens         int `json:"cached_tokens,omitempty"`           // cache read
	CacheCreationTokens  int `json:"cache_creation_tokens,omitempty"`  // cache write (新增)
}
```

注意：Anthropic 的 `input_tokens` **已包含** `cache_read_input_tokens` + `cache_creation_input_tokens`（即非缓存输入 = `input_tokens - cached - created`）。因此 `PromptTokens`/`TotalTokens` 语义不变，**不重复计入**；`CachedTokens`/`CacheCreationTokens` 是「输入内部分拆」，成本 = `PromptTokens` 计费但 `CachedTokens` 按 0.1× 计价——这正是回填价值：可观测「实际付费的缓存命中量」。

### 4.3 SSE 解析改动（`anthropic_native.go`）

`anthropicMessageStartData`（`92-100`）扩展 Usage：

```go
type anthropicMessageStartData struct {
	Type    string `json:"type"`
	Message struct {
		Usage struct {
			InputTokens             int `json:"input_tokens"`
			OutputTokens            int `json:"output_tokens"`
			CacheReadInputTokens    int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}
```

`readStream`（`205-348`）在 `message_start` 分支记录三个输入值：

```go
case "message_start":
	var ev anthropicMessageStartData
	if json.Unmarshal([]byte(data), &ev) == nil {
		inputTokens = ev.Message.Usage.InputTokens
		usage.CachedTokens = ev.Message.Usage.CacheReadInputTokens
		usage.CacheCreationTokens = ev.Message.Usage.CacheCreationInputTokens
	}
```

`message_delta` 分支保持只填输出（`318-324`）——它是**最后一次** `message_delta` 才有的 `output_tokens`，输入相关在 `message_start` 已有，无需重复。

`StreamMessage` 的 `Usage *Usage`（`llm.go:97`）已透传 `*Usage`，`collectStream`（`anthropic_native.go:538-564`）把 `end` 事件 Usage 赋给 `Message.Usage`，无需改动。

### 4.4 engine 累加新字段

三处累加点全部补 `CachedTokens` / `CacheCreationTokens`：

- `Execute`：`agent_execution.go:642-644`
- `executeStreamWithIterations` 迭代累加：`agent_execution.go:1156-1158`
- `executeStreamWithIterations` 最终 totalUsage：`agent_execution.go:1212-1214`

```go
totalUsage.PromptTokens += result.Usage.PromptTokens
totalUsage.CompletionTokens += result.Usage.CompletionTokens
totalUsage.TotalTokens += result.Usage.TotalTokens
totalUsage.CachedTokens += result.Usage.CachedTokens
totalUsage.CacheCreationTokens += result.Usage.CacheCreationTokens
```

（`ReasoningTokens` 同理可顺带透传；本方案聚焦缓存，reasoning 回填列入后续。）

### 4.5 OpenAI 兼容 provider（无 cache_control）

`providers/langchain_llm.go` 的 `extractUsage`（`436-499`）从 `GenerationInfo` 提取 usage。OpenAI Responses/Chat 的缓存字段：

```json
{ "usage": { "prompt_tokens": 3000,
    "prompt_tokens_details": { "cached_tokens": 1200 } } }
```

改动 `extractUsage`：

```go
// 在 direct keys 分支追加：
if v, ok := choice.GenerationInfo["prompt_tokens_details"]; ok {
	if pd, ok := v.(map[string]interface{}); ok {
		if cv, ok := pd["cached_tokens"]; ok {
			usage.CachedTokens = getInt(cv)
		}
	}
}
```

以及 `token_usage` map 分支同样处理 `cached_tokens` / `prompt_tokens_details`。OpenAI 侧只做**回填观测**，不注入任何 cache 标记（`cached_tokens` 由 OpenAI 服务端自动判定）。

DeepSeek / Volce（`agent/llm/deepseek.go`、`volce.go`）走 langchain 路径或各自实现；DeepSeek 兼容 OpenAI usage 格式，可复用 `extractUsage` 的 `prompt_tokens_details` 处理。各文件改动点由实现时逐一确认。

---

## 5. 设计：配置开关

### 5.1 `agent/types/agent.go`（engine 级）

`AgentConfig`（`agent.go:54-88`）新增：

```go
type AgentConfig struct {
	...
	// PromptCaching enables provider prompt caching (Anthropic cache_control breakpoints)
	// and usage backfill. Default on.
	PromptCaching bool `json:"promptCaching,omitempty"`
}
```

`NewAgentConfig`（`agent.go:100-130`）默认 `true`。

默认**开启**理由：
- Anthropic 缓存是**纯成本优化**，无正确性影响；服务端对未知/忽略字段安全降级。
- breakpoint 注入在 `Enabled` 为 true 时才改变请求体；默认开使长会话立即获益。
- 提供关闭逃生门（供应商兼容问题 / 调试原始请求时）。

engine 在设置模型后把配置透传给 provider（`agent_config.go:124-137` 的 `SetConfig` 里加一段）：

```go
if provider, ok := ae.model.(interface{ SetPromptCacheOptions(llm.PromptCacheOptions) }); ok {
	opts := llm.DefaultPromptCacheOptions()
	opts.Enabled = ae.config.PromptCaching
	provider.SetPromptCacheOptions(opts)
}
```

（`SetConfig` 在 `agent_config.go:124`，engine 持有 `ae.model`。注意 `agent` 包与 `llm` 包的关系：engine 已 import `agent/llm`？需确认——`AgentEngine` 存的是 `types.LLMProvider`（`types` 包），直接引用 `llm.PromptCacheOptions` 会引入 `engine → llm` 依赖。**更干净**：把 `PromptCacheOptions` 定义移到 `agent/types`，或定义在 `types` 下作为通用配置。→ 见 §3.3 修正：`PromptCacheOptions` 放 `agent/types/prompt_cache.go`，`SetPromptCacheOptions(PromptCacheOptions)` 变成 `types` 包内接口 `PromptCacheConfigurer`，engine 断言类型，`agent/llm` 实现。）

修正：

```go
// agent/types/prompt_cache.go（新增）
type PromptCacheOptions struct {
	Enabled           bool
	SystemBreakpoint  bool
	ToolsBreakpoint   bool
	HistoryEveryN     int
	MinCacheTokens    int
}

// 接口，engine 断言：
type PromptCacheConfigurer interface {
	SetPromptCacheOptions(PromptCacheOptions)
}
```

engine `SetConfig`：

```go
if pc, ok := ae.model.(types.PromptCacheConfigurer); ok {
	opts := types.DefaultPromptCacheOptions()
	opts.Enabled = ae.config.PromptCaching
	pc.SetPromptCacheOptions(opts)
}
```

`agent/llm/anthropic_native.go` 的 `NativeAnthropicProvider` 实现 `SetPromptCacheOptions`，并把 `anthropicCacheControl` 相关序列化逻辑留在 `llm` 包内。

### 5.2 `dino/config.go`（dino 级）

`dino.Config` 新增：

```go
type Config struct {
	...
	PromptCaching *PromptCachingConfig `yaml:"prompt_caching,omitempty"` // nil = 默认开启
}

type PromptCachingConfig struct {
	Enabled           bool `yaml:"enabled"`             // 默认 true
	SystemBreakpoint  bool `yaml:"system_breakpoint"`
	ToolsBreakpoint   bool `yaml:"tools_breakpoint"`
	HistoryEveryN     int  `yaml:"history_every_n"`     // 默认 5
	MinCacheTokens    int  `yaml:"min_cache_tokens"`    // 默认 1024
}
```

`DefaultConfig()`（`dino/config.go:114-222`）默认：

```go
PromptCaching: &PromptCachingConfig{
	Enabled:          true,
	SystemBreakpoint: true,
	ToolsBreakpoint:  true,
	HistoryEveryN:    5,
	MinCacheTokens:   1024,
},
```

`dino/factory.go` 构建 `AgentConfig` 时（`cfg` 在 `factory.go` 中组装）把 `PromptCaching` 映射进 `types.AgentConfig.PromptCaching`，并透传 breakpoint 子项给 provider（见 §3.4 的 setter）。

### 5.3 配置优先级

```
dino.Config.PromptCaching(如有) → types.AgentConfig.PromptCaching → llm 层 opts
```

单开/单关任一层都可覆盖；默认全开。

---

## 6. 设计：验证与测试

### 6.1 单测清单（核心）

| # | 用例 | 文件 | 断言 |
|---|---|---|---|
| U1 | `buildRequest` 开启缓存：system 变成 blocks 数组且带 `cache_control` | `agent/llm/anthropic_native_test.go` | JSON 反序列化后 `system[0].cache_control.type == "ephemeral"` |
| U2 | 开启缓存：每个 tool 带 `cache_control` | 同上 | `tools[i].cache_control != nil` |
| U3 | 历史消息每 N 条 assistant 挂 breakpoint，只挂 assistant 最后块 | 同上 | 构造 12 条 assistant 消息，`HistoryEveryN=5` → 恰有 2 个历史 breakpoint；无 breakpoint 挂在 tool_result 块 |
| U4 | 关闭缓存：请求体与现状字节级一致 | 同上 | `promptCache.Enabled=false` 时输出 JSON 与改动前逐字节相等 |
| U5 | 历史段 < MinCacheTokens 不挂 breakpoint | 同上 | 短消息不产生历史 breakpoint |
| U6 | `readStream` mock SSE：`message_start` 带缓存字段 → usage 回填 | 同上 | `CachedTokens == cache_read_input_tokens`；`CacheCreationTokens == cache_creation_input_tokens` |
| U7 | `collectStream` 透传 usage | 同上 | `Message.Usage.CachedTokens` 正确 |
| U8 | engine 累加 CachedTokens | `agent/engine/agent_test.go` | 两次迭代 mock provider 各返回 CachedTokens=100 → total 200 |
| U9 | `extractUsage` 解析 `prompt_tokens_details.cached_tokens` | `agent/providers/langchain_llm_test.go` | OpenAI 兼容 usage 回填 `CachedTokens` |
| U10 | `prepareMessages` 前缀稳定：同一 history 两次调用返回相同消息数组 | `agent/engine/agent_test.go` | 相等（回归防护） |

### 6.2 mock SSE 方法

`readStream` 直接消费 `io.ReadCloser` → 用 `io.NopCloser(strings.NewReader(sse))` 构造含 `event: message_start\ndata: {...}\n\n...` 的流喂给 `readStream`，读 `out` channel 断言。**无需起 HTTP server**。

### 6.3 集成测试（可选，加 `//go:build integration`）

| # | 用例 | 验证 |
|---|---|---|
| I1 | 真实 Anthropic key：同一 session 连发 3 个 turn | `usage.cache_read_input_tokens > 0` 在 turn 2、3 出现 |
| I2 | 关闭缓存对照组 | `cache_read_input_tokens == 0` |
| I3 | 长会话 + compaction 后 | 观测缓存命中率（允许降级，记录数值） |

跑法：`examples/dino` 起 session，日志打印 `cached_tokens` 累计值（§4.4 回填后 `AgentResult.Usage` 可观测）。

### 6.4 成本验证方法（观测）

- 每 `AgentResult.Usage` 现在含 `CachedTokens`/`CacheCreationTokens` → 计算 `savings = CachedTokens / (PromptTokens + CacheCreationTokens)`。
- dino `observe_turn.go` 若记录 usage 摘要，新增两字段打日志（小改动，可并入第 2 步）。

---

## 7. 迁移步骤

每步独立编译、独立测试、可回滚（`git revert` 单步 commit）。

### Step 1：usage 回填（纯观测，零行为变更）
- 改 `agent/types/llm.go`：`Usage` 加 `CacheCreationTokens`。
- 改 `agent/llm/anthropic_native.go`：`anthropicMessageStartData` 扩展 + `readStream` message_start 回填。
- 改 `agent/providers/langchain_llm.go`：`extractUsage` 解析 `prompt_tokens_details.cached_tokens`。
- 改 engine 三处累加点（§4.4）。
- 测试：U6/U7/U9/U8。
- **验证**：现有测试全绿；无任何请求体变化（此步不碰 `buildRequest`）。

### Step 2：breakpoint 注入（Anthropic 开启缓存）
- 新增 `agent/types/prompt_cache.go`：`PromptCacheOptions` + `PromptCacheConfigurer`。
- `agent/llm/anthropic_native.go`：struct 扩展 + `buildRequest` 注入（§3.2/§3.3）+ `SetPromptCacheOptions`。
- `agent/types/agent.go`：`AgentConfig.PromptCaching` 默认 true；engine `SetConfig` 透传。
- 测试：U1-U5。
- **验证**：U4 保证关闭时字节兼容；U1-U3 保证开启时 breakpoint 正确；跑一个真实 Anthropic 请求确认 `cache_read_input_tokens > 0`（手动/集成）。

### Step 3：dino 配置接线
- `dino/config.go`：`PromptCachingConfig` + 默认值。
- `dino/factory.go`：把 dino 配置映射进 engine `AgentConfig.PromptCaching` 与 provider `SetPromptCacheOptions`。
- 测试：U10（前缀稳定回归）+ dino 现有测试。
- **验证**：`examples/dino` 长会话跑通，usage 累计正确。

### Step 4（后续任务，不在本方案内）：compaction 前缀保留
- `trimHistoryToTokenBudget` 与 `dino/chatstore` summary 从头部改尾部（Codex `SUMMARY_PREFIX` 模型，`compact.rs:657-733`）。
- 依赖本方案 §2.3 的记录。单独开分支评估。

**回滚策略**：任一步出问题，`git revert <step-commit>` 即可。Step 1/2 互不依赖（可独立合入）；Step 3 依赖 Step 2。

---

## 8. 风险点

| 风险 | 等级 | 缓解 |
|---|---|---|
| Anthropic `cache_control` 字段结构不匹配导致 400 | 中 | 挂 breakpoint 只在 `Enabled` 时；U1-U4 单测锁定 JSON 结构；先小流量验证 |
| system 从 string 改 array 后旧代理不兼容 | 低 | `interface{}` 兼容两种；`Enabled=false` 时原样 string |
| `mergeConsecutiveRoles` 合并后块结构变化影响 breakpoint 位置 | 中 | breakpoint 注入放在合并**之后**（§3.5） |
| budget trim / 窗口滑动导致缓存失效（**预期行为**） | 低 | 文档化：budget 优先；首段（system+tools）仍稳定命中 |
| `MaxBudgetTokens` 用户开启时缓存收益下降 | 低 | 不强制保留前缀（§2.2a），后续 compaction 重设计解决 |
| 每请求 breakpoint 增加请求体（~几十字节） | 低 | 可忽略 |
| OpenAI 兼容代理忽略 `cache_control` | 低 | 服务端忽略未知字段，安全降级 |
| `cache_creation_input_tokens` 对某些模型缺失 | 低 | 缺省为 0，回填容错 |

---

## 9. 改动文件清单

**新增**
- `agent/types/prompt_cache.go` — `PromptCacheOptions`、`PromptCacheConfigurer`、`DefaultPromptCacheOptions`
- `agent/llm/anthropic_native_test.go` — U1-U7
- `agent/providers/langchain_llm_test.go` — U9（若已有测试文件则并入）
- `docs/design/prompt-caching.md` — 本文档

**修改**
- `agent/types/llm.go` — `Usage.CacheCreationTokens`
- `agent/types/agent.go` — `AgentConfig.PromptCaching`（默认 true）
- `agent/llm/anthropic_native.go` — `anthropicCacheControl`、`anthropicRequest.System` 类型、`anthropicContentBlock`/`anthropicTool` 加字段、`readStream` 回填、`buildRequest` 注入、`SetPromptCacheOptions`
- `agent/providers/langchain_llm.go` — `extractUsage` 缓存字段
- `agent/engine/agent_execution.go` — 三处 usage 累加点
- `agent/engine/agent_config.go` — `SetConfig` 透传 `PromptCaching`
- `agent/engine/agent_test.go` — U8/U10
- `dino/config.go` — `PromptCachingConfig` + 默认值
- `dino/factory.go` — 配置接线

**不改**
- `agent/engine/agent_execution.go` 的 `trimHistoryToTokenBudget`（前缀保留属后续任务）
- `dino/chatstore/*`（compaction 位置属后续任务）
- HTTP 轮询 / 流式通道（增量请求明确不做）

---

## 10. 接口签名汇总

```go
// agent/types/prompt_cache.go
type PromptCacheOptions struct {
	Enabled          bool
	SystemBreakpoint bool
	ToolsBreakpoint  bool
	HistoryEveryN    int
	MinCacheTokens   int
}
func DefaultPromptCacheOptions() PromptCacheOptions

type PromptCacheConfigurer interface {
	SetPromptCacheOptions(PromptCacheOptions)
}

// agent/types/llm.go
type Usage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	ReasoningTokens     int `json:"reasoning_tokens,omitempty"`
	CachedTokens        int `json:"cached_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
}

// agent/types/agent.go
type AgentConfig struct {
	...
	PromptCaching bool `json:"promptCaching,omitempty"` // 默认 true
}

// agent/llm/anthropic_native.go
func (p *NativeAnthropicProvider) SetPromptCacheOptions(opts types.PromptCacheOptions)

// dino/config.go
type PromptCachingConfig struct {
	Enabled          bool `yaml:"enabled"`
	SystemBreakpoint bool `yaml:"system_breakpoint"`
	ToolsBreakpoint  bool `yaml:"tools_breakpoint"`
	HistoryEveryN    int  `yaml:"history_every_n"`
	MinCacheTokens   int  `yaml:"min_cache_tokens"`
}
```

---

## 11. 留给用户的待定点

1. **默认开启 vs 默认关闭**：本方案建议默认开启（纯成本优化）。若你有供应商/计费侧疑虑，把 `AgentConfig.PromptCaching` 默认改为 `false` 即可，一行改动。
2. **HistoryEveryN 取值**：默认 5。若会话消息平均 token 很大（工具结果长），可调小到 3 提高缓存粒度；若担心 breakpoint 太多，调大到 8。建议上线后用 §6.4 的 savings 指标调优。
3. **`ReasoningTokens` 回填**：顺带实现（Claude thinking 的 `thinking_tokens` 在 SSE `thinking_delta` 有 token 计数），或留给后续。
4. **compaction 前缀保留（Step 4）**：是否排期？影响长会话（>100 消息）缓存命中率的上限。
5. **OpenAI 兼容 provider 是否也做 `cached_tokens` 观测回填**：本方案已含（Step 1），仅观测、不注入标记。

---

## 12. 实现备注（2026-08-28，分支 impl-prompt-caching）

实现按评审修正 B1/B2/B3 + R1/R2/R4/R10 落地，以下是相对原设计 §3/§4/§5 的偏差记录：

### 12.1 usage 回填（Step 1）
- 按 **B1** 修正：`PromptTokens = input_tokens + cache_read_input_tokens + cache_creation_input_tokens`（总输入），`CachedTokens`/`CacheCreationTokens` 为输入内部分拆；`TotalTokens = PromptTokens + CompletionTokens`。§4.2 原表「input_tokens 已含缓存」的断言被推翻。
- R3 补全累加点：`agent_execution.go`（Execute 循环 + stream 迭代 + stream 最终 totalUsage）、`langchain_engine.go` 三处、`agent.go:143` `GetTotalUsage` 经 `Usage` 的 json tag 自动随 snapshot 序列化（`snapshot.go:64` 无需改代码）。
- OpenAI 兼容回填（§4.5 + O4）：`extractUsage` 的 `token_usage` 分支（此前直接 return 会跳过 cached 解析）与 direct-keys 分支均解析 `prompt_tokens_details.cached_tokens` / `cached_tokens`。

### 12.2 breakpoint 布局（Step 2）——按 B2 预算式重写
- 原设计「system + 每 tool + 每 N 条 assistant」被 B2 否决（4-breakpoint 硬上限）。实现为 **预算式 ≤4**：
  - system 1 个（system 非空时，转 `[]anthropicContentBlock` 数组挂 `cache_control`）；
  - tools 段只挂**最后一个 tool** 1 个；
  - 历史段最多 `HistoryEveryN`（默认 **2**，非原设计的间隔 5）个，且总数 ≤ `types.MaxAnthropicCacheBreakpoints = 4`；超限时历史段**降级丢弃**而非报错。
- 历史 breakpoint 挂在**从尾往头数**的 assistant 消息最后一块（block 数组形态，`mergeConsecutiveRoles` 之后注入），`tool_result` 块永远不挂。
- 客户端硬编码 4 上限常量 + 超限测试（20 tools + 30 消息 → ≤4）。
- `MinCacheTokens` 默认 **4096**（R2：Opus/Sonnet 4.6+ 需 2048-4096，1024 会静默不缓存）。历史段估计 token < 阈值时不挂历史 breakpoint。

### 12.3 类型归属（B3）与配置接线（R4/R10）
- `PromptCacheOptions`/`PromptCacheConfigurer`/`MaxAnthropicCacheBreakpoints` 统一放 `agent/types/prompt_cache.go`；`NativeAnthropicProvider` 实现该接口。engine 只断言 `types.PromptCacheConfigurer`，无 `engine → agent/llm` 依赖。
- R4：dino `factory.go` 从不调用 `SetConfig`，因此 engine 在 **`NewAgentEngine` 构造时**（`propagateConfigLocked`）就向 provider 透传 `AgentConfig.PromptCaching`；`SetConfig` 同样调用。
- R10：`dino.Config.PromptCaching` 用**可空子字段**（`*bool`/`*int`），部分 YAML 覆盖不零化其他开关；`PromptCacheConfigurer` 增加 `PromptCacheOptions()` getter，engine 合并时**只改 Enabled**、保留 provider 已配置的子项（dino factory 构造时 `ConfigurePromptCache` 已设置子项）。
- factory 在 `NewDinoFactory` 创建 provider 后即 `ConfigurePromptCache`，使共享 provider 被 session 引擎与 subagent 引擎共同继承。

### 12.4 遗留（未实现，见 §11 / 评审 R6）
- **compaction 前缀保留（Step 4）**：不实现。`trimHistoryToTokenBudget` 与 `dino/chatstore` summary 的头部位置未动；长会话（>100 消息）缓存命中率上限由 compaction 决定。排期另议。
- **`ReasoningTokens` 回填**（O1）：未实现，仍为 0。
- **增量请求**（§2.4）：明确不做，HTTP 轮询 + 前缀稳定 + breakpoint 已获缓存收益。
- 集成测试（真实 API，§6.3）：未跑，留 TODO。
