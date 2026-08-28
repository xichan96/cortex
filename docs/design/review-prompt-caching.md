# Prompt Caching 落地方案 · 深度技术评审

> 评审对象：`docs/design/prompt-caching.md`（分支 `opt-prompt-caching`，2026-08-28）
> 评审依据：worktree 内真实代码 + Anthropic 官方缓存规则（claude-api skill `shared/prompt-caching.md` 为权威）
> 评审日期：2026-08-28
> 范围：只产评审报告，不改任何代码。

---

## 0. 结论摘要

**该设计可以进入实现，但必须先在 Step 1/Step 2 的落地前修正两处 BLOCKER（4-breakpoint 上限、assistant 消息挂 breakpoint 的可行性与收益），并采纳若干 RECOMMENDED。修正后从 Step 1（usage 回填）开始落地；Step 2（breakpoint 注入）是核心，但 §3.2 的历史段方案需要重写。**

总体评价：设计对**现状代码事实的核查非常扎实**（绝大多数 file:line 引用精确无误，对前缀稳定性的分析、对 `trimHistoryToTokenBudget` / 窗口滑动 / compaction 对缓存影响的判断、以及「增量请求不做」的取舍都是正确的），usage 回填设计（Step 1）是**高质量、零风险、可立即落地**的。但 breakpoint 布局（§3.2/§3.3）存在两个与 Anthropic API 事实不符的核心缺陷：**每请求最多 4 个 `cache_control` breakpoint** 以及 **assistant 消息内容块挂 breakpoint 的收益/合法性存疑**。这两点直接推翻设计的「system + 每个 tool + 每 N 条 assistant 历史」布局和 `HistoryEveryN` 默认值 5 的依据，属于 BLOCKER。此外，`PromptCacheOptions` 的类型归属（§5.1）在文档内自相矛盾，需在落地时统一。

---

## 1. 事实核查结果（逐条 pass/fail）

### 1.1 代码事实（worktree 内）

| # | 设计断言 | 核实结果 | 证据 |
|---|---|---|---|
| F1 | `agent/types/llm.go:41-47` 的 `Usage` 定义 | **PASS**（行号精确） | `agent/types/llm.go:41-47` |
| F2 | `CachedTokens`（`llm.go:46`）与 `ReasoningTokens`（`llm.go:45`）全仓无任何写点 | **PASS** | `grep CachedTokens\|ReasoningTokens` 仅命中 `llm.go` 定义处 |
| F3 | `anthropicRequest`（`anthropic_native.go:28-35`）无 cache_control 字段 | **PASS**（行号 28-35 精确） | `anthropic_native.go:28-35` |
| F4 | `anthropicContentBlock`（`42-50`）、`anthropicTool`（`52-56`）无 cache_control | **PASS** | `anthropic_native.go:42-50,52-56` |
| F5 | `buildRequest` 在 `351-462`，system 拼接 string（`355-363`） | **PASS**（行号精确；355-363 为 system 拼接段） | `anthropic_native.go:351-462`，system 拼接实际在 355-363 |
| F6 | `newNativeAnthropicProvider`（`122-137`） | **PASS**（行号精确） | `anthropic_native.go:122-137` |
| F7 | `readStream`（`205-348`）`message_start` 只取 `InputTokens`（`251-256`） | **PASS**（行号精确） | `anthropic_native.go:251-256` |
| F8 | `anthropicMessageStartData`（`92-100`）无缓存字段 | **PASS**（行号精确） | `anthropic_native.go:92-100` |
| F9 | `message_delta` 组装 usage（`318-324`） | **PASS**（行号精确） | `anthropic_native.go:318-324` |
| F10 | `Execute` 累加点 `642-644`；`executeStreamWithIterations` 迭代 `1156-1158`、最终 `1212-1214`；`executeIteration` `1048-1057` 是唯一 usage 回填入口 | **PASS**（行号全部精确） | `agent_execution.go:642-644, 1156-1158, 1212-1214, 1048-1057` |
| F11 | `prepareMessages`（`840-903`）顺序：system → history → previousRequests → input | **PASS**（行号精确） | `agent_execution.go:840-903` |
| F12 | `buildNextMessages`（`agent_messages.go:10-27`）只追加 | **PASS**（行号精确） | `agent_messages.go:10-27`，复制后 append，无重排 |
| F13 | `trimHistoryToTokenBudget`（`987-1017`）从头部裁剪 | **PASS**（行号精确；逻辑为从尾部反推 `start` 后 `history[start:]`） | `agent_execution.go:987-1017` |
| F14 | `SimpleMemoryProvider.GetMessages` 窗口滑动（`langchain_memory.go:93-94`） | **PASS**（行号精确） | `langchain_memory.go:93-94`，`start := totalMessages - queryLimit; messages[start:]` |
| F15 | `repairLLMMessageToolOrdering`（`908-985`） | **PASS**（行号精确，但**注意**：它在 trim 之后**无条件**对全部 history 执行，并非设计表格里写的「仅在 trim/压缩后修复」） | `agent_execution.go:890, 908-985` |
| F16 | compaction 摘要放头部：`langchain_memory.go:74-81` | **PASS**（行号精确） | `langchain_memory.go:74-81` |
| F17 | `dino/chatstore/memory.go:292-298`（Hybrid summary 插头部） | **PASS**（行号 292-298 精确） | `dino/chatstore/memory.go:282-301` |
| F18 | `DeterministicCompact`（`compact.go:42-140`） | **PASS**（行号 42 起精确；末尾实际到 ~140 行之前） | `compact.go:40-140` |
| F19 | `dino.Config` / `DefaultConfig`（`config.go:114-222`） | **PASS** | `dino/config.go:114` 起 |
| F20 | `AgentConfig`（`agent.go:54-88`）、`NewAgentConfig`（`100-130`） | **PASS**（行号精确） | `agent/types/agent.go:54-88,100-130` |
| F21 | `SetConfig` 在 `agent_config.go:124`，engine 持有 `ae.model` | **PASS** | `agent/engine/agent_config.go:124-137` |
| F22 | `LLMProvider` 接口（`llm.go:6-18`） | **PASS**（行号精确） | `agent/types/llm.go:6-18` |
| F23 | `mergeConsecutiveRoles`（`466-486`）统一块数组 | **PASS**（行号精确；逻辑上合并发生在消息序列化之后） | `anthropic_native.go:441,466-486` |
| F24 | `collectStream`（`538-564`）透传 `Message.Usage` | **PASS**（行号精确） | `anthropic_native.go:538-564` |
| F25 | `StreamMessage.Usage *Usage`（`llm.go:97`） | **PASS**（行号精确） | `agent/types/llm.go:97` |
| F26 | `extractUsage`（`langchain_llm.go:436-499`） | **PASS**（行号精确） | `agent/providers/langchain_llm.go:436-499` |
| F27 | `anthropic.go:25` dino 的 anthropic provider 走 `newNativeAnthropicProvider` | **PASS** | `agent/llm/anthropic.go:25`；`dino/config.go:257-263` |
| F28 | DeepSeek/Volce 走 langchain 路径 | **PASS** | `agent/llm/deepseek.go:38-41`、`agent/llm/volce.go:33-36` 均返回 `providers.NewLangChainLLMProvider` |
| F29 | `dino/factory.go` 构建 AgentConfig 与 provider | **PASS**（注意：`factory.go` 通过 `engine.NewAgentEngine(f.llmProvider, agentConfig)` 传配置，**从不调用 `SetConfig`**） | `dino/factory.go:491-530` |
| F30 | `dino/chatstore/memory.go:179-184`（dino InMemory 走 DeterministicCompact） | **PASS**（实际是 `memory.go:176-181` 的 `generateSummary` → `DeterministicCompact`） | `dino/chatstore/memory.go:176-181` |

**代码事实核查：30 项全部 PASS。** 设计的现状分析极其扎实。

### 1.2 外部协议事实（Anthropic 缓存规则）

| # | 设计断言 | 核实结果 | 证据 |
|---|---|---|---|
| P1 | `cache_control: {type:"ephemeral"}` 可放在 system blocks / tools 数组元素 / messages 内容块 | **PASS** | Anthropic 官方规则；claude-api skill `shared/prompt-caching.md`：「Goes on any content block: system text blocks, tool definitions, message content blocks (`text`, `image`, `tool_use`, `tool_result`, `document`).」 |
| P2 | 缓存段 = 从 breakpoint 到请求末尾（或下一个 breakpoint） | **PASS** | 官方规则：cache key = 从请求头到该 breakpoint 的累计前缀 |
| P3 | 只有最长前缀命中；ephemeral 5 分钟 TTL | **PASS** | 官方规则 |
| P4 | **每请求最多 4 个 `cache_control` breakpoint** | **FAIL（设计遗漏）** | Anthropic 硬限制：超过 4 个返回 400 `"A maximum of 4 blocks with cache_control may be provided. Found 5."`。claude-api skill：**「Max 4 `cache_control` breakpoints per request.」**。设计 §3.3 的布局（system 1 + 每个 tool N + 每 5 条 assistant 历史 M）在任意工具数 > 3 或历史段 > 3 时**必然超限**，默认配置直接打 400 |
| P5 | `cache_control` 可以挂在 **assistant 消息**的 text/tool_use 块上 | **存疑（FAIL）** | 官方文档和 skill 只确认 system / tools / messages 内容块（`text`/`image`/`tool_use`/`tool_result`/`document`），并明确**不能挂在 `thinking`/`redacted_thinking` 块上**。对 assistant 消息本身没有否定，但「按 assistant 消息计数、每 N 条挂一个」的布局在 4-breakpoint 硬限制下**无实现空间**——见 BLOCKER-2 |
| P6 | `cache_control` 可挂在 `tool_result` 块 | **FAIL（设计自己都说 tool_result 不能挂，但现实是 tool_result 块根级可挂）** | skill：「message content blocks (`text`, `image`, `tool_use`, `tool_result`, `document`)」——tool_result **根级**可挂。langchain PR #34959 佐证：tool_result 的 cache_control 必须放在块根级而非内部 content 子块。设计的「tool_result 块不能挂」表述**不准确**（正确表述是「不能挂在 tool_result 的 content 子块上」）——但这不影响设计结论（对工具结果挂缓存无意义） |
| P7 | 历史段 < 最小缓存 token 阈值不挂 | **PASS** | 官方规则：最小可缓存前缀模型相关（1024~4096），短前缀静默不缓存。设计默认 1024 偏低但安全 |
| P8 | `message_start` usage 含 `cache_read_input_tokens` / `cache_creation_input_tokens` | **PASS** | 官方确认；Go SDK `resp.Usage.CacheCreationInputTokens` / `CacheReadInputTokens` |
| P9 | **`input_tokens` 是「未缓存的剩余部分」，不含缓存读写** | **FAIL（设计 §4.2 的计费/语义断言错误）** | claude-api skill `shared/prompt-caching.md` 明确：**「`input_tokens` is the uncached remainder only. Total prompt size = `input_tokens + cache_creation_input_tokens + cache_read_input_tokens`.」** 设计的 §4.2 表格断言「`input_tokens` → `PromptTokens`（总输入含缓存读写）」、§4.2 注释「input_tokens 已包含 cache_read + cache_creation」**是错的**。Anthropic 的 `input_tokens` 是**纯非缓存输入**。含义：映射 `PromptTokens = input_tokens` 会**显著低估**总输入（长会话里大部分输入其实是 cache_read，此时 PromptTokens 只显示几千）。见 BLOCKER-1 |
| P10 | `message_delta` 只给 `output_tokens` | **PASS** | 官方确认（usage 在 message_start / message_delta 累计） |
| P11 | `cache_read` 0.1× / `cache_creation` 1.25× 计费 | **PASS**（§4.2 注释里的「0.1×」正确；但设计把 `PromptTokens` 当作计费基数的隐含语义错误，见 P9） | skill：读 0.1×、写 1.25× |
| P12 | 最小缓存阈值默认 1024 | **PASS（但应随模型调整）** | skill 表格：Opus 4.8/4.6 等需 4096，Sonnet 4.6 需 2048，Sonnet 4.5 才 1024。设计默认 1024 在 Opus/Sonnet 4.6+ 上会导致「挂 breakpoint 但静默不缓存」——见 RECOMMENDED-2 |
| P13 | `system` 改 array 形式 Anthropic 接受 | **PASS** | skill Go 示例 `System: []anthropic.TextBlockParam{...}`；Anthropic 接受 string 或 array 两种 |
| P14 | Anthropic 对未知字段不报错（安全降级） | **PASS**（但注意：breakpoint **数量超限会 400**，不是「忽略未知字段」的降级场景） | skill / 生态 |
| P15 | 缓存命中要求前缀字节级一致；改 tools/system/模型使整个缓存失效 | **PASS** | skill：「Any byte change anywhere in the prefix invalidates everything after it」「Tools render at position 0」 |
| P16 | **渲染顺序是 tools → system → messages**（而非设计 §3.2 图示的 system → tools） | **FAIL（设计图示顺序错误，但结论方向不变）** | skill：「Render order is: `tools` → `system` → `messages`. A breakpoint on the last system block caches both tools and system together.」 设计的 ASCII 图把 system 画在 tools 之前。实际 HTTP 请求体里 system/tools 是平级顶层字段，缓存 key 按 tools 在前。这**不影响**「tools 段最稳定」的核心结论，但 §3.2 的图示和「system 段包含 summary」的表述需修正 |

**协议事实核查：16 项中 5 项 FAIL（P4/P5/P9/P16 为 FAIL，P6 表述不准确）。**

---

## 2. BLOCKER（必须先改再落地）

### B1 · usage 计费语义错误：`PromptTokens = input_tokens` 低估总输入（Step 1 即受影响）

- **位置**：设计 §4.2 表格 + §4.2 注释（`docs/design/prompt-caching.md:330-353`）
- **事实**：Anthropic 的 `input_tokens` **不含**缓存读写。官方规则：`Total prompt size = input_tokens + cache_creation_input_tokens + cache_read_input_tokens`。设计的断言「`input_tokens` 已包含 `cache_read_input_tokens` + `cache_creation_input_tokens`」是**错误**的，`PromptTokens`/`TotalTokens` 的语义**会改变**——长会话里绝大部分输入走 cache_read，此时 `PromptTokens` 只反映未缓存的那一小部分。
- **影响**：Step 1 如果按设计映射，`Usage.PromptTokens` / `TotalTokens` 在缓存开启后**数值暴跌**（只统计未缓存输入），现有依赖 `TotalTokens` 的调用点（`dino/session/session.go:303,493` 的 `RecordTokens`、budget 计算、`snapshot.go:64`）会**错误地低估消耗**，甚至让 `MaxBudgetTokens` 的预算形同虚设。
- **修法**：`PromptTokens = input_tokens + cache_read_input_tokens + cache_creation_input_tokens`（即总输入），`CachedTokens`/`CacheCreationTokens` 作为输入内部分拆。`TotalTokens = PromptTokens + CompletionTokens` 语义不变。这是**纯回填逻辑修正，不影响请求体**，Step 1 仍是零行为变更——但必须按正确公式填。

### B2 · 4-breakpoint 硬限制：§3.2 的 breakpoint 布局与 `HistoryEveryN=5` 默认值必然 400

- **位置**：设计 §3.2（`prompt-caching.md:178-200`）、§3.3（`203-276`）
- **事实**：Anthropic 每请求**最多 4 个 `cache_control` breakpoint**，超出返回 HTTP 400（`"Found 5"`）。设计布局 = system(1) + 每个 tool(≥1) + 每 5 条 assistant 历史挂 1 个（长会话轻易 > 2）。**任意工具数 ≥ 3 且历史段 ≥ 1 即超限**，dino 默认工具远多于 3 个（`dino/tools/builtin.go` 就有 20+ 工具），默认配置下**每个请求都 400**。
- **影响**：按设计实现的 Step 2 会把所有走 Anthropic 的请求打崩，这是硬性不可上线缺陷。
- **修法**（RECOMMENDED-1 的布局必须落地）：
  - system 1 个（若 system+tools 一起缓存：只挂 **tools 最后一个工具**，或只挂 **system 最后一个 block**，二选一）。
  - 历史段最多再分配 **1~2 个**（如挂最近一个稳定 assistant 块 / 每 N 段但总数封顶 4）。
  - `HistoryEveryN` 不再是「每 N 条挂一个」的**间隔**，而是「历史段最多用掉 N 个 breakpoint 名额」的**预算**。默认 `HistoryEveryN` 语义需重写为「历史 breakpoint 上限 = 2」之类。
  - 必须在客户端**硬编码 4 上限**并测试超限不回退（参考生态里 `enforce_breakpoint_cap` 的实现，见 aigw-anthropic）。

### B3 · `PromptCacheOptions` 类型归属自相矛盾（§5.1 文档内部错误）

- **位置**：设计 §3.3（`203-276`）定义 `PromptCacheOptions` 在 `agent/llm/prompt_cache.go`，§5.1（`467-497`）先自我否定又修正到 `agent/types/prompt_cache.go`，§10（`657-668`）接口签名又写回 `agent/types/prompt_cache.go`。
- **事实**：`AgentEngine.model` 的类型是 `types.LLMProvider`（`agent/types/llm.go:6-18`），engine 包 import 的是 `agent/types`。若 `PromptCacheOptions` 定义在 `agent/llm`，engine 引用它会产生 `engine → agent/llm` 依赖环（`agent/llm` 已 import `agent/types`，而 engine 属于 `agent` 包的兄弟包——需确认无环，但设计自己的 §5.1 已承认「更干净：定义到 types」）。
- **影响**：落地时若按 §3.3 的定义放 `agent/llm`，engine `SetConfig` 透传段无法编译（或引入不想要的依赖方向）。
- **修法**：**统一放 `agent/types`**（`prompt_cache.go`），`NativeAnthropicProvider` 实现 `types.PromptCacheConfigurer`。文档 §3.3 与 §5.1 需对齐。

---

## 3. RECOMMENDED（落地时采纳）

### R1 · 重写 breakpoint 布局为「≤4 个、预算式分配」并修正工具/系统段

- **位置**：§3.2 全部、§3.3 的 `anthropicTool`/system 结构
- **要点**：见 B2 修法。推荐：**工具段只挂最后一个 tool**（Anthropic 官方推荐、且工具段是最稳定段）、**system 块 1 个**、**历史段按预算最多 1~2 个**（挂在最近一个「稳定的 assistant 消息末尾块」上，参考生态推荐布局「system 1 / tools 1 / 最近稳定 assistant 1」）。`tools` 段若与 system 合并缓存，二选一挂即可。这样**总 breakpoint ≤ 4** 恒成立。

### R2 · `MinCacheTokens` 默认值应随模型走（当前 1024 会静默无效）

- **位置**：§3.3 `DefaultPromptCacheOptions`（`prompt-caching.md:267-275`）
- **事实**：最小可缓存前缀因模型而异：Opus 4.8/4.7/4.6/4.5/Haiku 4.5 需 **4096**，Fable 5/Sonnet 4.6 需 **2048**，仅 Sonnet 4.5 及更旧为 **1024**（skill 表格）。cortex 默认模型是 Claude Sonnet 4（`ClaudeSonnet4.String()` = `claude-sonnet-4-20250514`，`anthropic.go:29`），属于 1024 档——默认 1024 恰好可用；但用户切到 Opus/Sonnet 4.6+ 后 1024 档 breakpoint **静默不缓存**（无报错、`cache_creation_input_tokens:0`）。建议：默认提到 4096，或按 model 映射，避免「以为开了缓存实际没生效」。

### R3 · 补上 4 个遗漏的 usage 累加/消费点

- **位置**：§4.4 只列了 3 处（`agent_execution.go:642-644/1156-1158/1212-1214`）
- **遗漏点**：
  1. `agent/engine/langchain_engine.go:109-111, 129-131, 169-171` 三处 `totalUsage` 累加（LangChain 引擎路径，虽 dino 未用但库 API 存在，Step 1 应一并改以免不一致）。
  2. `agent/engine/agent.go:143` `GetTotalUsage()`、`dino/session/snapshot.go:64` 用 `ag.GetTotalUsage()` 做会话快照/恢复——`CachedTokens` 需随 snapshot 序列化（`Usage` 带 `json` tag，自动覆盖，但需确认恢复路径 `RestoreTotalUsage`）。
  3. `dino/session/session.go:303,493` `RecordTokens(s.id, result.Usage.TotalTokens)`——若 B1 修正后 `TotalTokens` 变大，这里**自动受益**（预算更准），无需改但需回归测试。
  4. `dino/types.go:39` `Usage = session.Usage = agenttypes.Usage` 别名链——新增 `CacheCreationTokens` 字段会自动透传到 dino `Event.Usage`，`turn_observe.go:90-93` 已复制整个 Usage，无需改。

### R4 · `dino/factory.go` 从不调用 `SetConfig`——Step 2 的透传依赖点要写清

- **位置**：§5.1（`prompt-caching.md:457-465`）写的「engine 在 `SetConfig` 里透传」在 dino 路径**不成立**。
- **事实**：`dino/factory.go:530` 用 `engine.NewAgentEngine(f.llmProvider, agentConfig)` 构造，`NewAgentEngine`（`agent.go:107`）只在构造时把 config 存进 `ae.config`，**全程没有 `SetConfig` 调用**（`grep SetConfig` 在非 vendor 代码里零命中，除了 `agent_config.go` 自身定义）。
- **影响**：Step 2 若只改 `SetConfig`，dino 默认路径**永远不触发透传**，缓存默认开启就成了空话。修法二选一：(a) 在 `NewAgentEngine` 里同时做 provider 断言透传（推荐，构造即生效）；(b) dino `factory.go` 构造后显式调用 `agent.SetConfig` 或直接对 provider 调 setter。

### R5 · `repairLLMMessageToolOrdering` 并非「仅在 trim/压缩后修复」——会无条件改头部

- **位置**：设计 §1.6 表格第三行（`prompt-caching.md:94`）
- **事实**：`prepareMessages` 在 `agent_execution.go:890` **无条件**调用 `repairLLMMessageToolOrdering(history)`（trim 之后），即**每次请求**都会跑，可能丢弃头部 orphan tool 消息（`agent_execution.go:913-916`）。它对缓存前缀的影响不是「低频、仅 trim 后」，而是**每个请求都可能改变 history 头部**。
- **影响**：如果头部历史消息恰是 orphan tool（会话开头概率低，但存在），会导致跨 turn 历史段缓存失效。设计把它归为「尾部修复通常不影响首个 breakpoint」基本成立（首段是 tools+system），但**文档表述需修正**，且验证用例 U10 应覆盖「头部被 repair 改动的场景」。

### R6 · §2.3 的「compaction 前缀保留不在本方案内」是**合理裁剪**，但收益上限要明说

- **位置**：§2.3、Step 4（`prompt-caching.md:128-142, 605-607`）
- **判断**：**合理裁剪，不算漏。** 理由：(a) 本方案的核心收益在**单次 Execute 内**（一次 turn 的多轮工具迭代），这是 agentic 会话 token 大头；(b) 跨 turn 前缀保留依赖 Codex 式 compaction 重设计（`compact.rs:657-733` 的 `SUMMARY_PREFIX` 模型，已核实存在），其改动面（chatstore 存储格式、summary 位置、`trimHistoryToTokenBudget` 契约）远超本任务；(c) 5 分钟 TTL 意味着跨 turn（尤其用户思考间隔 > 5 分钟）缓存本就容易过期。但**必须如实记录**：长会话（> 100 消息）的缓存命中率上限被 compaction 决定，本方案 Step 4 之前这部分收益拿不到。

### R7 · 「增量请求不做」的判断**成立**，但理由补充

- **位置**：§2.4（`prompt-caching.md:144-151`）
- **判断**：**成立。** HTTP 轮询 + 前缀稳定 + breakpoint 确实能吃到服务端缓存（Anthropic 缓存是服务端前缀匹配，与传输方式无关）。增量请求（Codex `previous_response_id`，`client.rs:1336-1374` 已核实）收益仅为省上行字节 + TTFB，而缓存命中的成本收益已由前缀稳定获得。**但**注意：5 分钟 TTL 下，用户两次 turn 间隔 > 5 分钟时缓存会过期（cache read = 0），这是 HTTP 轮询 + 短 TTL 的固有限制，与「不做增量请求」无关。文档可加一句「若未来需要跨长间隔复用，可评估 1h TTL breakpoint（`cache_control: {type:"ephemeral", ttl:"1h"}`），写成本 2×、读 0.1×，适合 bursty 长会话」。

### R8 · `system` 改 `interface{}` 的方案正确，但 `System string` 序列化旧行为要单测锁定

- **位置**：§3.3 `anthropicRequest.System`（`prompt-caching.md:216`）
- **判断**：`interface{}` + `omitempty` 兼容 string/array 两态是正确做法（Anthropic 两者都接受）。但 `json.Marshal` 对 `interface{}` 里的 string 与 `[]anthropicContentBlock` 输出**字节级不同**，U4 的「Enabled=false 字节兼容」必须对比**改动前后的完整请求体**（不止 system 字段），并锁定 `System` 为空时 `omitempty` 不输出。这条 U4 是回滚安全网，务必保留。

### R9 · 风险表补一行：**默认开启 + 走 OpenAI 兼容中转的 Anthropic provider**

- **位置**：§8 风险表（`prompt-caching.md:613-625`）
- **判断**：设计 §3.5 已断言「OpenAI 兼容代理忽略 cache_control = 安全降级」。这**大体正确**（多数中转转发未知字段），但有一个例外：**部分中转/代理会把 system 的 array 形式翻译错**（已知 DeepSeek `/anthropic` 端点对 system-array 返回 `messages[1].role: unknown variant 'system'` 400，见生态 issue）。`Enabled=false` 字节级兼容 + 默认开启 + 中转场景 = 需要一条「系统 prompt 数组形式在某些中转 provider 上 400」的风险行，并把「默认开启」的逃生门（`AgentConfig.PromptCaching=false` 一行）写清楚。

### R10 · Step 3 的 dino 接线改动面不全

- **位置**：§7 Step 3（`prompt-caching.md:599-603`）
- **遗漏**：`dino/factory.go` 的 provider 是**通过 `f.llmProvider` 注入**的（`factory.go:530`），provider 的构造在 `dino/config.go:257-263`（`createLLMProvider` → registry → `NewAnthropicClient`）。Step 3 要接的其实有两处：(a) 把 `f.config.PromptCaching` 映射进 `types.AgentConfig.PromptCaching`（`factory.go:491-528` 的 agentConfig 组装段）；(b) 让 `createLLMProvider` / `NewAnthropicClient` 链能把 breakpoint 子项透传给 `NativeAnthropicProvider`。`NewAnthropicClient`（`anthropic.go:14-26`）签名没有 options 承载 `PromptCacheOptions`——要么加参数，要么靠 engine 侧 setter。设计 §5.3 的优先级链（dino → AgentConfig → llm opts）需明确哪一层承载子项（建议 dino 只传 `Enabled`，子项走 provider setter 或默认）。

---

## 4. OPTIONAL

| # | 项 | 说明 |
|---|---|---|
| O1 | `ReasoningTokens` 回填 | 设计 §11.3 已列。`thinking_delta` 目前被当 reasoning 文本消费（`anthropic_native.go:281-285`），token 计数在 Anthropic SSE 里通过 `message_start` 的 `usage` 无独立 thinking 字段（需从 `thinking_delta`/`message_delta` 特殊处理）——留给后续，不阻塞。 |
| O2 | cache 预热 | skill 提供 `max_tokens:0` 预热法。对 dino 长会话，可在会话首请求后、用户思考间隙预热历史段缓存；非必需，量级小。 |
| O3 | `savings` 指标 | 设计 §6.4 的 `savings = CachedTokens / (PromptTokens + CacheCreationTokens)` 在 B1 修正后应改为 `CachedTokens / (PromptTokens + CacheCreationTokens + CachedTokens)`（分母应为总输入）或直接用 `cache_read_input_tokens` 的绝对值。 |
| O4 | OpenAI 侧 `cached_tokens` 回填（§4.5） | DeepSeek/Volce 走 langchain，`extractUsage` 的 `prompt_tokens_details.cached_tokens` 是只读观测，改动小且不注入标记——与 Step 1 一起做即可。注意：`token_usage` map 分支返回前要先处理 `prompt_tokens_details`（当前 `extractUsage` 在 `token_usage` 命中时直接 `return`，会跳过 direct-keys 分支，需在 `token_usage` 分支内也加 `cached_tokens` 解析——设计 §4.5 的代码只写在 direct keys 分支）。 |

---

## 5. 分维度评价

### 正确性
- 现状分析正确率高（30/30 代码事实核实通过），对「单次 Execute 内天然前缀稳定」「trim/滑动/compaction 破坏前缀」「增量请求不做」的判断全部成立。
- **但 breakpoint 布局与计费语义两处与 Anthropic API 事实不符**，是设计正确性的主要缺陷。前者直接导致默认配置 400，后者导致 Step 1 的 usage 数值错误。两者都可修，且修法明确。
- 对「每 N 条 assistant 挂 breakpoint 扩大收益」的机制理解有偏差：收益大头不是历史段 breakpoint 数量，而是**首段（tools+system）稳定命中** + 末端最近的 breakpoint 让「新增历史」之前的部分复用。4 个 breakpoint 里通常 1~2 个用于历史即可。

### 可行性
- Step 1（usage 回填）**可行性高**：改动集中在 `readStream` message_start + `extractUsage` + 3 处（实为 6 处，见 R3）累加点，不碰请求体，可独立测试回滚。**前提是按 B1 的正确公式填**。
- Step 2（breakpoint 注入）**可行但需重写 §3.2**：布局改预算式 ≤4 后，改动面不变（仍是 `buildRequest` + struct 扩展 + setter），U1-U5 单测可覆盖。**R4 的构造路径问题必须同时处理**，否则 dino 默认开不起来。
- Step 3 接线改动面需按 R10 补全。迁移顺序（1 → 2 → 3）合理，Step 1/2 互不依赖的判断正确。

### 覆盖度
- 评审重点问题全部覆盖：现状问题（usage 空置、无 breakpoint、无记账）、前缀稳定性、breakpoint 布局、usage 映射、增量取舍、compaction 裁剪、迁移步骤、风险、验证——都有对应章节。
- **缺**：4-breakpoint 上限、`input_tokens` 语义、`factory.go` 不调 `SetConfig` 这三处关键事实未核实到（且前两处方向性错误）。

### 风险
- 上线风险集中在 Step 2：默认开启 + 布局超限 = 全量 400（B2）；中转 provider system-array 兼容（R9）。Step 1 若按 B1 修正后零风险。
- `Enabled=false` 字节级兼容**成立**（条件：U4 锁定完整请求体），回滚策略（单步 revert）合理。

### 工作量估计
- 设计未给明确人日。按改动面估计：Step 1 ≈ 0.5~1 天（含测试），Step 2 ≈ 1~1.5 天（含布局重写与 U1-U5），Step 3 ≈ 0.5 天。总量 ≈ 2~3 天，估计**合理**。未计 Step 4（compaction 重设计，单独评估）。

---

## 6. 总体评价

设计文档的**工程质量很高**：代码事实核查扎实、问题拆解清晰、迁移步骤可独立测试可回滚、「增量请求不做」和「compaction 裁剪」两个取舍在工程上正确。它的短板不在对 cortex 的了解，而在对 **Anthropic 缓存协议的两处硬性事实**（4-breakpoint 上限、`input_tokens` 语义）的核实缺失——这直接推翻了 §3.2 的 breakpoint 布局和 §4.2 的 usage 映射。

**结论：可以进入实现，但从 Step 1 开始，且先修 B1/B3；Step 2 必须按 B2 + R1 重写布局。**

**落地顺序建议：**
1. 先修 B1（usage 映射公式）与 B3（类型归属），连同 Step 1 一起合入——这一步零请求体风险，先拿到 usage 可观测。
2. 按 R1 重写 §3.2 布局为预算式 ≤4，再实现 Step 2，U4 字节兼容测试兜底。
3. Step 3 按 R4/R10 修正接线。
4. Step 4（compaction 前缀保留）按 R6 记录为后续独立任务。

---

## 附录：关键证据 file:line

| 结论 | 证据 |
|---|---|
| 4-breakpoint 上限 | Anthropic 官方 + claude-api skill `shared/prompt-caching.md`「Max 4 `cache_control` breakpoints per request」；生态 400 报错 `"Found 5"` |
| `input_tokens` 不含缓存 | claude-api skill `shared/prompt-caching.md`「`input_tokens` is the uncached remainder only. Total = input_tokens + cache_creation + cache_read」 |
| 渲染顺序 tools→system→messages | claude-api skill `shared/prompt-caching.md`「Render order is: tools → system → messages」 |
| 最小缓存阈值按模型 | claude-api skill `shared/prompt-caching.md` 表格（Opus 4.8 4096 / Sonnet 4.6 2048 / Sonnet 4.5 1024） |
| `factory.go` 不调 `SetConfig` | `dino/factory.go:491-530`（`engine.NewAgentEngine(f.llmProvider, agentConfig)`，无 `SetConfig`）；`agent/engine/agent_config.go:124` 定义；非 vendor `grep .SetConfig(` 零命中 |
| 遗漏的 usage 累加点 | `agent/engine/langchain_engine.go:109-111,129-131,169-171` |
| `repairLLMMessageToolOrdering` 无条件执行 | `agent_execution.go:890` |
| `extractUsage` token_usage 分支提前 return | `agent/providers/langchain_llm.go:437-467`（`token_usage` 命中即 `return usage`） |
| 默认模型 Sonnet 4（1024 档） | `agent/llm/anthropic.go:29` `ClaudeSonnet4 = "claude-sonnet-4-20250514"` |
| Codex `SUMMARY_PREFIX` 模型存在 | `/Users/CHENXI/rust/codex/codex-rs/core/src/compact.rs:356,573` + `prompts/src/compact.rs:2` |
| Codex 增量请求存在 | `/Users/CHENXI/rust/codex/codex-rs/core/src/client.rs:1336-1374`（`get_incremental_items`） |
