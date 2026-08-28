# Compaction 前缀保留（P3.1 · prompt caching Step 4）

> 设计文档 · 分支 `p3-design-compaction` · 2026-08-29
> 对应 `docs/next-round.md` P3.1 与 `docs/design/prompt-caching.md` §12.4 遗留项（Step 4）。
> 参考：评估报告第三章 Codex 做法（`docs/optimization-review-vs-codex.md:66-84`，`core/src/compact.rs`）。

## 0. 结论摘要

| 主题 | 决策 |
|---|---|
| 目标 | compaction 后 prompt cache 仍能命中 **system + tools + 最近稳定 assistant 块** 的前缀（预算式 ≤4 breakpoint 中的前 2 段） |
| `trimHistoryToTokenBudget` | 从「保留尾部、丢头部」改为「**保头 + 压缩中间 + 保尾**」三段式：头部缓存锚（≤`CacheAnchorTokens`，默认 0=关闭）始终原文保留，超出预算部分整体替换为**尾部摘要**，严格不超预算 |
| 摘要注入位置 | **放最后**（Codex `SUMMARY_PREFIX` 模型），不从头部 system 注入 —— 与缓存前缀、与 Anthropic `mergeConsecutiveRoles` 语义双兼容 |
| 是否依赖 P3.4/3.5 | **不依赖**。本方案在纯 `DeterministicCompact` 上独立落地（Step 1-3）；P3.4 压缩升级替换的是「压缩内容生成器」，位置模型不变（接口已预留） |
| 配置 | 默认**关闭**（`CompactionPrefix`，`AgentConfig` + `dino.Config.Memory`），保守默认；开启后行为逐项可回滚 |
| 测试 | 纯函数单测（前缀保留/预算不超/摘要占位/缓存命中模拟）+ engine 集成 + dino chatstore 集成 |
| 迁移 | 4 步（见 §8），每步独立可验证、可 `git revert` |

核心一句话：**「严格不超预算」契约不改** —— 头部锚点单独算预算，中间历史整体换成一个「尾部摘要」消息，摘要不是追加而是**替换**，因此总 token 只减不增。

---

## 1. 现状（已核实，全部基于代码事实）

### 1.1 `trimHistoryToTokenBudget` 是「保留尾部、丢头部」

`agent/engine/agent_execution.go:1187-1217`：

```go
func trimHistoryToTokenBudget(history []types.Message, maxBudgetTokens int, ...) []types.Message {
	fixed := /* system + previousRequests + input 的估算 token */
	rem := maxBudgetTokens - fixed
	used := 0
	start := len(history)
	for i := len(history) - 1; i >= 0; i-- {   // 从尾往头数
		c := types.RoughTokensForMessage(history[i])
		if used+c > rem { break }
		used += c
		start = i
	}
	return history[start:]                       // 丢头部、保尾部
}
```

- 唯一调用点 `agent_execution.go:1088`（`prepareMessages` 内，`budgetTrim` 开启时）。
- 语义：**从尾部倒推**，能塞进 `rem` 的历史保留，前缀（system + 旧历史 + 首个 cache breakpoint 段）被整段丢弃。
- 对 prompt cache 的影响：`docs/design/prompt-caching.md` §1.6 已记录 —— 每次预算裁剪边界 `start` 随输入漂移，**系统 + tools 之后的历史段全部缓存失效**；且 history 本身被缩短后，之前建立的「从尾部往头数的 assistant breakpoint」（`applyCacheControl` §3.2 布局：system 1 + last tool 1 + history 2）位置全部前移失效。

### 1.2 预算契约

- `MaxBudgetTokens`（`agent/types/agent.go:72`）+ `RemainPromptTokens`（`agent.go:73`）在 `prepareMessages`（`agent_execution.go:1069-1088`）计算 `budgetCap`。
- 预算仅约束 **system + history + previousRequests + input** 的 `RoughTokensForMessage` 估算和；`RoughTokensForMessage`（`agent/types/tokens.go:20-48`）只估算 `Content/Name/ToolCallID/Parts/ToolCalls`，**不含** `Usage` 字段。
- 因此「摘要消息」只要以 `types.Message{Role:"assistant", Content: <summary>}` 形式存在，其 token 估算天然成立（§3 用它）。

### 1.3 前缀修复依赖链

`prepareMessages` 顺序（`agent_execution.go:1024-1103`）：

1. `system`（`1046-1051`）
2. **压缩摘要注入**（`1056-1067`）：若 `ae.memory` 实现 `GetSummary(context.Context)(string,error)` 且非空 → 追加一条 `{Role:"system", Content:"Previous conversation summary:\n"+summary}`，插在 system 之后、history 之前
3. `history = trimHistoryToTokenBudget(...)`（`1088`，budget 开启时）
4. `history = repairLLMMessageToolOrdering(history)`（`1090`）
5. `history` → `previousRequests` → `input`（`1092-1100`）

**关键观察**：摘要 system 消息与 `trimHistoryToTokenBudget` 是**两条独立路径** —— 摘要注入发生在 trim 之前（`1056` 早于 `1088`），trim 只作用于 `history`，摘要不在 history 里、不受预算约束。这是现状的一个隐含矛盾：预算开启时，**摘要 system + 被 trim 的 history 可能重复占用上下文**，且摘要位置（头部）破坏缓存前缀。

### 1.4 dino chatstore 两条压缩路径都「删旧 + 摘要存头部」

| 路径 | 位置 | 压缩动作 | 摘要产出 |
|---|---|---|---|
| `InMemory`（dino 默认） | `dino/chatstore/memory.go:142-167`（`compress`） | 删 `KeepRecentCount` 之前的旧消息（`156-158`），`summary` 存内存字段 | `DeterministicCompact`（`179-184`） |
| `SQLite` | `dino/chatstore/sqlite.go:287-313`（`Compress`） | DELETE 旧行，保留最近 `KeepRecentCount`（`304-306`），summary 存 metadata `key='summary'`（`311`） | `DeterministicCompact("", old, ...)`（`302`） |
| `Hybrid`（零调用方，P3.5 目标） | `dino/chatstore/memory.go:309-359` | `provider.Clear` + 回填 recent（`348-356`） | LLM `GenerateSummary`（`336`，失败 fallback `generateBasicSummary`） |

摘要的**读取**有两处注入为 system 消息：

- `Hybrid.GetMessages`（`memory.go:282-301`）：`append([]Message{summaryMsg}, messages...)` → 头部。
- engine `prepareMessages` 的 `GetSummary` 探测（`agent_execution.go:1056-1067`）：`dino/factory.go:170-172` 的 `memoryAdapter.GetSummary` 转发 `chatstore.Provider.GetSummary`。**InMemory/SQLite 都实现 `GetSummary`**（`memory.go:111-115`、`sqlite.go:275-285`），所以 dino 默认 InMemory 路径的摘要**已**作为 system 消息注入 engine 上下文头部。

> 注：`agent/providers/langchain_memory.go:167-254`（`CompressMemory`，非 dino 路径，engine 直接设 memory 时走）是第三条压缩路径，同样把 summary 包成 system 放头部（`74-81`）。本方案优先覆盖 dino 路径（P3.1 目标），langchain provider 路径作为 Step 4 延伸（§8）。

### 1.5 缓存 breakpoint 布局（prompt caching 已落地）

`agent/llm/anthropic_native.go` `applyCacheControl`（`552-589`），预算式 ≤4：

```
system block          1 breakpoint  (SystemBreakpoint && system != "")
last tool             1 breakpoint  (ToolsBreakpoint && len(tools)>0)
history assistant     ≤ HistoryEveryN (默认 2)，从尾往头数，cap 到 4
```

- **前 2 段（system + tools）是「最稳定段」**：位于 `trimHistoryToTokenBudget` 裁剪点之前（`agent_execution.go:1088` 只动 history，system/tools 由请求构建时固定）。compaction 只要不动 system/tools，这 2 段缓存天然存活。
- **第 3 段（history 尾部 breakpoint）**：锚在「从尾往头数的 assistant 消息」上（`markHistoryBreakpoints` `596-630`）。history 被 trim 缩短后，尾部锚点失效 → 跨 turn 缓存命中率上限 = compaction 决定。这正是本方案要修的。
- system 是**拼接字符串**（`buildRequest` `425-433`：所有 system role 消息 `\n` 拼接），摘要 system 消息拼接进 system → **摘要内容变化 ⇒ system 段整段重写 ⇒ 第 1 段缓存失效**。这是「摘要放头部」对缓存破坏力的精确机制。

### 1.6 现有测试基座

- engine：`agent/engine/agent_test.go` 已有 `TestPrepareMessages_SummaryInjection`（`441-471`，断言摘要 system 位置在 system 之后）——本方案若改摘要位置，此测试需同步改断言。
- anthropic：`agent/llm/anthropic_native_test.go` 已有 breakpoint 预算测试（B2 layout，`208` 起）。
- chatstore：`dino/chatstore/` **无测试文件**（只有 admin/memdir），需新增。

---

## 2. 目标与约束

### 2.1 目标

compaction 后，下一 turn 请求至少命中：

1. **段 1：system**（含初始上下文）—— 前提：摘要不再进 system。
2. **段 2：tools** —— 前提：tools 数组不变（现状已满足）。
3. **段 3（尽力）：最近稳定 assistant 块** —— 前提：history 尾部（最近几轮 assistant 消息）在 compaction 后**原文保留**。

### 2.2 约束（不可打破）

1. **严格不超预算**：`trimHistoryToTokenBudget` 的核心契约不变（`docs/design/prompt-caching.md` §2.2a 已定）。任何「保留前缀」都不允许让总 token 超预算。
2. **Anthropic 协议**：`mergeConsecutiveRoles`（`anthropic_native.go:655-677`）要求 user/assistant 交替；tool 消息在 Anthropic 格式化为 user（`440-442`）。任何新注入的消息（摘要）必须满足交替约束，否则 400。
3. **缓存 breakpoint 数量上限**：`types.MaxAnthropicCacheBreakpoints = 4`（`agent/types/prompt_cache.go:28`），不可因注入新增 breakpoint 而超限。
4. **不回归**：`promptCache.Enabled=false` 时请求体字节级不变（既有承诺，`anthropic_native.go:531-536`）。

---

## 3. 设计：`trimHistoryToTokenBudget` 改造（engine 层，核心）

### 3.1 新算法：保头 + 压缩中间 + 保尾（三段式）

```
入参：history []types.Message, maxBudgetTokens int, config, previousRequests, input

1. fixed = system + previousRequests + input 的估算 token      // 同现状 agent_execution.go:1191-1198
2. 若 config.CompactionPrefix 关闭（默认）→ 走现状 trim（§1.1），完全不变

3. anchor = config.CacheAnchorTokens（默认 0）                  // 头部缓存锚 token 预算
   - anchorTokens = clamp(anchor, 0, rem)                       // 不超过 rem，防锚点挤爆
   - 从头部往尾数，收集 history[0:anchorIdx]，使 RoughTokensForMessage 累加 ≤ anchorTokens
   - anchorIdx ≥ 1（至少保留一条头部消息，通常第 0 条是 user 初始请求）
   → headMsgs = history[:anchorIdx]                             // 原文保留，缓存锚

4. tail = 从尾往头数收集 history[tailStart:]，使累加 ≤ rem - anchorTokens
   - 至少保留一条（现状语义 history[start:] 永远 ≥1 条）
   → tailMsgs = history[tailStart:]

5. mid = history[anchorIdx:tailStart]
   - 若 len(mid) == 0 → 返回 append(headMsgs, tailMsgs...)       // 无需压缩
   - 否则 mid 整体替换为 1 条摘要消息：                            // ← 压缩核心
       summaryMsg = types.Message{
           Role: "assistant",
           Content: buildTailSummary(config, mid),                // 见 §3.3
       }
     （摘要的 token 已含在 anchor 预算之外？否 —— 摘要加入后需再校验）

6. 校验：sum(headMsgs) + RoughTokensForMessage(summaryMsg) + sum(tailMsgs) ≤ rem
   - 不满足（理论上仅当 summary 本身超预算）→ 降级：丢 summary，仅保 head + tail，
     或对 head 收缩后重试（见 §3.4 降级链）

7. 返回 append(headMsgs, append([]Message{summaryMsg}, tailMsgs...)...)
```

**为什么是「替换」而不是「追加」**：摘要放在 head 与 tail 之间，替代了被压缩的 mid。总 token = head + 1 条摘要 + tail ≤ 原 head+mid+tail（mid 被压成一条），**单调不增**，严格不超预算由此直接成立（只要摘要 ≤ mid 的 token 和；摘要通常远小于 mid）。

**请求形态**（Anthropic 视角）：

```
[system (含初始上下文)] [tools]
── cache_control 段1/段2：不变，始终命中 ──
history[0:anchorIdx]                 ← 头部缓存锚，原文
  │  ← 摘要 assistant 消息（SummaryPrefix 包裹，见 §3.3）
history[tailStart:]                   ← 尾部原文
  └── 最近 assistant 块 → 段3 breakpoint 锚，跨 compaction 存活 ──
[previousRequests] [input]
```

### 3.2 头部缓存锚的取值与语义

- `CacheAnchorTokens`（新字段，`agent/types/agent.go`）默认 **0 = 关闭**。开启时（dino 默认建议 2048，§6.3），把「初始上下文」原文保留在头部，使其永远参与缓存前缀。
- 锚点预算占 `rem` 的一部分，**从 rem 里扣**，因此不会打破总预算。代价：锚点挤压可用的尾部原文预算 → 尾部保留变少。这是可接受的：头部锚点是「可命中缓存的前缀」，尾部是「最近上下文」；两者都是缓存友好的，中间被压缩最合理。
- **锚点选头部而非系统**：system 已是独立缓存段（段 1），锚点要保护的是「system 之后的第一个历史段」——它位于 tools 段之后、是第 3 段 breakpoint 的前置。历史第 0 条（通常是初始 user 请求或首个 assistant 回合）不随每次 turn 变化，是最值得锚定的。

### 3.3 摘要消息构造（`buildTailSummary`）——与压缩升级（P3.4）的接口对齐点

```go
// 新增，agent/engine/compaction.go（或 agent/engine/agent_execution.go 同文件）
func buildTailSummary(cfg *CompactionOptions, mid []types.Message) string {
	if cfg == nil || cfg.SummaryGenerator == nil {
		// 纯 DeterministicCompact 路径（P3.1 独立落地，不依赖 P3.4）
		return chatstore.DeterministicCompact("", toChatstoreMessages(mid), chatstore.DefaultCompactConfig())
	}
	return cfg.SummaryGenerator(mid)  // P3.4 挂 LLM 摘要的接入点（§7）
}
```

- **确定性路径**：复用 `dino/chatstore.DeterministicCompact`（`compact.go:42-140`），参数为 `[]chatstore.Message`（`type Message = agenttypes.Message`，`memory.go:15`，**类型同构**，直接转换切片即可，无 schema 问题）。
- **P3.4 接入点**：`CompactionOptions.SummaryGenerator func([]types.Message) string` 是一个纯函数注入点，P3.4 把 LLM 摘要（`Hybrid` 的 `GenerateSummary`，`memory.go:336`）挂在这里即可，**engine 不需要 import `dino/chatstore` 的 LLM 设施**（避免 `agent/engine → dino` 依赖方向问题）。§7 详述。
- 摘要消息 role 用 **assistant**（非 system）：
  - 不进 system 拼接（`buildRequest` `425-433`）→ **不破坏段 1 缓存**（§1.5 的破坏机制被根除）。
  - 与尾部 user 消息构成交替，满足 `mergeConsecutiveRoles` 语义：`[user(锚)] [assistant(摘要)] [user|assistant(尾部)]...`。若尾部第 1 条恰是 assistant（罕见），合并后仍合法。
- **`SUMMARY_PREFIX` 标记**（Codex 模型）：摘要文本包一层结构化标签，模型能区分「这是压缩上下文」而非新内容：

  ```
  [CONVERSATION COMPACTED - prior context follows]
  <previous_summary>...</previous_summary>
  <recent_user_requests>...</recent_user_requests>
  <current_work>...</current_work>
  ```

  `DeterministicCompact` 已产出 `<scope>/<tools_mentioned>/<recent_user_requests>/<pending_work>/<key_files>/<current_work>` 结构（`compact.go:68-137`），天然满足「结构化摘要」，无需额外加工；只需在最外层包一个 `[CONVERSATION COMPACTED ...]` 头。

### 3.4 降级链（保证「严格不超预算」恒成立）

| 条件 | 动作 |
|---|---|
| 摘要 token ≤ mid token（预期常态） | 直接替换，总预算安全 |
| 摘要 token > mid token（罕见，摘要反而更大） | **不替换**，丢 mid（`history[:anchorIdx] + tail`），等于现状行为但头部锚保留 |
| 锚点预算 > rem | anchor 收缩到 rem（`clamp`），极端下 anchor=0 → 行为完全等同现状 |
| 尾部预算 < 单条历史 | tail ≥1 条（保留最后一条，现状语义），head 可能被挤压 → 锚点先收缩、后丢弃 |
| `repairLLMMessageToolOrdering` 误伤头部锚 | 锚点只取「自 anchorIdx 起连续合法前缀」：若 history[0] 是孤儿 tool（无匹配 assistant），锚点从第一条 assistant 起算（详见 §4.1） |

**结果**：任何输入下输出要么 ≤ 预算（三段式成功），要么退化为现状行为（≤ 预算，因为现状已保证）。预算契约零破坏。

### 3.5 与 `repairLLMMessageToolOrdering` 的先后

- `prepareMessages` 现在先 trim 后 repair（`1088` → `1090`）。三段式 trim 后，head 与 tail 之间的摘要可能使 repair 看到「assistant 摘要」夹在两个真实消息之间。
- `repairLLMMessageToolOrdering`（`1108-1185`）只做：丢前导孤儿 tool、丢「assistant 无匹配 tool_call」、丢「tool 无匹配 assistant」。摘要 assistant 无 `ToolCalls` → repair 会**原样保留**（`1124-1139` 非 tool 分支直接 append），不误删。安全。
- 唯一注意：若摘要恰好出现在 repair 认为的「assistant+tool 序列」中段，不影响正确性（repair 是**过滤**不是重排）。**无需改 repair**。

---

## 4. 设计：chatstore 层配合（摘要注入位置）

### 4.1 决策：摘要放最后，双路径改造

现状摘要有两个注入点，全部在头部：

1. `Hybrid.GetMessages`（`memory.go:292-298`）：`append([]Message{summaryMsg}, messages...)`
2. engine `prepareMessages` 的 `GetSummary` 探测（`agent_execution.go:1056-1067`）：插 system 之后

**问题**：
- 头部摘要 ⇒ 进 system 拼接 ⇒ **段 1 缓存失效**（§1.5）。
- 头部摘要还占 `MaxHistoryMessages` 窗口内的一条 system，污染 `SimpleMemoryProvider.GetMessages` 的消息计数语义。

**决策（Codex `SUMMARY_PREFIX` 模型）**：
- **摘要放最后**，作为一条独立消息（role 见 §3.3）。
- 落点选在 **engine 层统一处理**，而不是 `Hybrid.GetMessages`：
  - `GetMessages` 是存储接口（`chatstore.Provider`），**不该改变消息顺序**（顺序属于「上下文组装」职责，engine 的 `prepareMessages` 才是组装者）。
  - 改 `GetMessages` 会破坏 `memoryAdapter.GetChatHistory`（`factory.go:174-191`）拿到的 history 语义（摘要混入 history，干扰 trim/repair/预算估算）。
  - **正确分层**：chatstore 只负责「产出摘要字符串」（`GetSummary` 已接线），engine `prepareMessages` 负责「把摘要放哪」。

### 4.2 具体改动点

**`agent/engine/agent_execution.go` `prepareMessages`（`1024-1103`）**：

```go
// 现状 1056-1067：摘要作为 system 消息插在 system 之后、history 之前
// 改为：摘要变量单独取出，参与 trim 预算（§3.1 step 5），
//       最终以 assistant 消息追加在 history 尾部之后、previousRequests 之前
```

新组装顺序：

```
1. system（不变，1046-1051）
2. history（trim 后，含 head + 摘要 + tail，§3）
3. summary（assistant 消息，若存在）       ← 从头部 system 移到尾部
4. previousRequests
5. input
```

注意顺序：摘要**必须在 previousRequests 之前**（previousRequests 是本次 Execute 内的最近工具回合，需最后紧贴 input；摘要只是历史压缩），且**在 input 之前**（摘要不是新内容）。

**`Hybrid.GetMessages`（`memory.go:282-301`）**：去掉头部注入（`292-298`）。`Hybrid` 的摘要改由 engine 统一读 `GetSummary`（`GetSummary` 已实现 `303-307`）。`GetMessages` 只返回真实 history。

**`dino/factory.go` `memoryAdapter`**：`GetSummary`（`170-172`）已转发，**无需改**。摘要读取路径不变，只是注入位置从 engine 的头部改到尾部。

**`agent/providers/langchain_memory.go`（`74-81`）**：这是 agent 直连（非 dino）路径。`GetChatHistory` 返回的 summary system 消息在头部。**本方案不改**（§8 Step 4 延伸）：dino 是 P3.1 目标；langchain provider 的摘要注入同样破坏缓存前缀，但它是独立 MemoryProvider 实现，改它的 `GetChatHistory` 会影响其它消费方。列入后续。

### 4.3 与 `MergeConsecutiveRoles` 的兼容性验证

注入后的请求序列（Anthropic 格式，tool→user 映射后）：

```
user(初始) [锚]
user(tool_result)...          ← tail 可能是 tool→user
assistant(摘要)               ← 摘要
user(tool_result)...          ← tail 其余
assistant(最近) [段3 breakpoint]
user(previousRequests → tool_result)
user(input)
```

- `mergeConsecutiveRoles` 合并相邻同 role。摘要 role=assistant 时，若相邻历史也是 assistant（少见），合并后摘要文本与历史 assistant 文本拼接成一块 → breakpoint 可能挂到合并块的末尾（`markHistoryBreakpoints` 从尾往头数）→ 段 3 锚点后移但不破坏段 1/2。
- 不会产生 400：合并只是把同 role 拼接，Anthropic 合法。
- **风险**：摘要被合并进相邻 assistant 块后，模型可能混淆「摘要」与「真实历史」。缓解：摘要内容带 `SUMMARY_PREFIX` 头（§3.3），且尽量让摘要前邻 user（用 `tailStart` 选在 user 边界处，§8 调优）。**接受为已知限制**。

### 4.4 `DeterministicCompact` 的签名约束

- `DeterministicCompact(existingSummary string, messages []Message, cfg CompactConfig) string`（`compact.go:42`）。engine 层调用时传 `existingSummary=""` 会丢失「跨多次压缩的累积摘要」——现状 `InMemory.compress`（`memory.go:160`）传 `m.summary`，`SQLite.Compress` 传 `""`（`sqlite.go:302`，每次都从零重建）。
- **engine 的 `buildTailSummary` 无法拿到已存在的 summary**（它在 `prepareMessages` 只读 `GetSummary` 返回的字符串）。两案：
  - (a) 简单版：`buildTailSummary` 只对 `mid` 做单次 compact（`existingSummary=""`），把已有的 `GetSummary` 内容也拼进摘要（`GetSummary` 已在 §3.3 之前由 engine 读出）。**推荐** —— 零状态、幂等、可测试。
  - (b) 复杂版：chatstore 暴露 `Compress(old []Message)` 增量接口，engine 不直接调 `DeterministicCompact`。与 P3.4 接口强耦合，**不做**。

---

## 5. 配置

### 5.1 `agent/types/agent.go`（engine 级）

```go
// AgentConfig 新增（agent.go:54-88 区域）
// CompactionPrefix enables prefix-preserving compaction: keeps a head cache
// anchor, replaces the middle with a tail summary, and preserves recent tail
// verbatim, so prompt-cache breakpoints 1-2 (system+tools) survive compaction.
// Default off: with it off, trimHistoryToTokenBudget keeps today's behavior.
CompactionPrefix bool `json:"compactionPrefix,omitempty"`
// CacheAnchorTokens caps the head-cache-anchor budget (tokens). 0 = no anchor
// (head is dropped, summary still replaces the middle). Default 0.
CacheAnchorTokens int `json:"cacheAnchorTokens,omitempty"`
```

`NewAgentConfig`（`agent.go:112-146`）：`CompactionPrefix: false`、`CacheAnchorTokens: 0`。

### 5.2 `dino/config.go`（dino 级）

```go
// MemoryConfig 新增（dino/config.go:39-49）
CompactionPrefix bool `yaml:"compaction_prefix"`   // 默认 false
CacheAnchorTokens int  `yaml:"cache_anchor_tokens"` // 默认 0
```

`dino/factory.go` `CreateSession`（`541-566` 附近）映射进 `agentConfig`：

```go
agentConfig.CompactionPrefix = f.config.Memory.CompactionPrefix
agentConfig.CacheAnchorTokens = f.config.Memory.CacheAnchorTokens
```

### 5.3 默认关闭的理由

- `trimHistoryToTokenBudget` 是**存量默认开启**的预算机制（`MaxBudgetTokens` 默认 0 即关闭 trim，但 dino `EnableCompress` 默认 true、用户可能开 budget）。
- 前缀保留改变 trim 的输出结构（多一条摘要 assistant 消息）→ 对模型可见的行为变化，**保守默认**：用户显式开启才启用，逐项可回滚（§8）。
- 与 prompt caching 的关系：`CompactionPrefix` 是**独立开关**，不依赖 `PromptCaching`。即使缓存关闭，三段式 trim 的「保尾 + 摘要」也是质量改善（上下文更聚焦）；即使缓存开启但 `CompactionPrefix` 关闭，现状缓存收益仍在（§2.2 段 1/2 不受 trim 影响）。**两者正交**，避免配置耦合。

### 5.4 配置优先级

```
dino.Config.Memory.CompactionPrefix → types.AgentConfig.CompactionPrefix → engine trim 行为
```

（与 prompt caching 的优先级模式 `dino → agent → llm` 一致，`docs/design/prompt-caching.md` §5.3。）

---

## 6. 测试

### 6.1 纯函数单测（核心，`agent/engine/agent_test.go` 或新 `agent/engine/compaction_test.go`）

| # | 用例 | 断言 |
|---|---|---|
| T1 | 关闭 `CompactionPrefix`：三段式 trim 输出与现状逐字节一致 | `CompactionPrefix=false` 时 `trimHistoryToTokenBudget` 结果 == 现状函数结果（回归防护） |
| T2 | 开启 + 预算充足：head（锚）+ 摘要 + tail 三段，head 与 tail 是原文切片 | 构造 30 条消息，预算大 → `out[0:anchorIdx]` 等于 `history[:anchorIdx]`（DeepEqual），`out` 含恰好 1 条 role=assistant 的摘要，尾部等于 `history[tailStart:]` |
| T3 | 预算紧：总 token 严格 ≤ `rem` | 对多组 (预算, 消息长度) 断言 `sum(RoughTokensForMessage(out)) ≤ rem` |
| T4 | 摘要超预算（人造：mid 极小、摘要 text 超大）：降级不替换 | 输出 = head + tail（无摘要），且 ≤ rem |
| T5 | 锚点挤压尾部：极端预算下 head 保留、tail 收缩 | `anchor=1024`、预算小 → head 仍在，tail 只有最后 1 条 |
| T6 | 摘要消息 role=assistant，带 `[CONVERSATION COMPACTED` 前缀 | `out[anchorIdx].Content` 前缀匹配 |
| T7 | 空 history / 全 tool / 全 user 边界 | 不 panic，输出合法交替（无相邻同 role 非摘要） |
| T8 | 缓存命中模拟（前缀稳定性）：两次调用，第二次只追加新 turn | `trimHistoryToTokenBudget` 后接 `buildNextMessages`，断言第二次请求的 `history[0:anchorIdx] + 摘要 + tail` 与第一次的**前缀逐条相等**（模拟「compaction 后缓存前缀仍在」） |

### 6.2 engine 集成（`agent/engine/agent_test.go`）

| # | 用例 | 断言 |
|---|---|---|
| T9 | `prepareMessages` 摘要位置：开启 `CompactionPrefix` 后，`GetSummary` 返回的摘要出现在 **history 之后、input 之前**，role=assistant | 更新 `TestPrepareMessages_SummaryInjection`（`441-471`）断言：不再在 `msgs[1]`（system 后），而在末尾前 |
| T10 | 预算 + 摘要 + 前缀共存的完整 `prepareMessages` | 开启 `MaxBudgetTokens` + `CompactionPrefix` + `GetSummary` → 输出含摘要、总估算 ≤ 预算、system 段不变 |

### 6.3 dino chatstore 集成（新 `dino/chatstore/compaction_test.go`）

| # | 用例 | 断言 |
|---|---|---|
| T11 | `InMemory.Compress` + engine 注入：压缩后 `GetMessages` 返回 `KeepRecentCount` 条、`GetSummary` 非空 | 压缩阈值后触发，`StoredMessageCount == KeepRecentCount` |
| T12 | SQLite `Compress`：旧行删除、summary 写 metadata | `GetMessages` 只含最近 `KeepRecentCount`；`GetSummary` 非空 |
| T13 | 摘要放最后链路（模拟真实 dino）：factory 构建 `memoryAdapter` + engine，`prepareMessages` 输出顺序 | 断言摘要 assistant 消息位于 history 之后 |

### 6.4 集成（真实 API，加 `//go:build integration`）

| # | 用例 | 验证 |
|---|---|---|
| I1 | `examples/dino` 长会话（>100 消息）+ `enable_compress` + `compaction_prefix: true` | 压缩前后的 `usage.cache_read_input_tokens` 对比：段 1/2 缓存命中（`CachedTokens > 0`）；对比关闭 `compaction_prefix` 的对照组 |

---

## 7. 与 P3.4 / P3.5 的关系（接口对齐）

### 7.1 不依赖，但接口对齐

| 项 | P3.1（本方案） | P3.4（`DeterministicCompact`→LLM 摘要） | 对齐点 |
|---|---|---|---|
| 摘要生成 | `DeterministicCompact`（`compact.go:42`） | `Hybrid.GenerateSummary`（`memory.go:336`） | `buildTailSummary` 的 `SummaryGenerator func([]types.Message) string` 注入点（§3.3） |
| 摘要位置 | 尾部 assistant 消息（§3.3） | P3.4 的 `docs/next-round.md:81` 方向「摘要作为最后一项注入」 | **位置模型一致**，P3.1 先把位置定死 |
| 摘要消息 role | assistant + `SUMMARY_PREFIX` | P3.4 未定 role；`Hybrid.GetMessages` 现状是 system | P3.1 定 role=assistant，P3.4 沿用（改 role 会破坏缓存，别改） |
| 预算交互 | 摘要**替换** mid，不超预算（§3.4） | P3.4 需保持同一约束 | P3.1 的降级链就是 P3.4 的预算护栏 |
| LLM 依赖 | 无（纯确定性） | `Hybrid` 构造（P3.5 默认化） | P3.1 独立落地；P3.4 只是换「摘要怎么造」 |

### 7.2 P3.5（Hybrid 默认化）的依赖点

P3.5 把 `Hybrid` 挂成默认压缩 provider（`next-round.md:85-89`）。P3.1 不依赖它，但**若 P3.5 落地**：
- `Hybrid.GetMessages` 的头部摘要注入（`memory.go:292-298`）**必须去掉**，否则 P3.5 上线后摘要又回到头部破坏缓存。本方案 §4.2 已把它列为改动点 → P3.5 实现时**直接复用本方案的改动**，不会冲突。
- `Hybrid.Compress`（`memory.go:309-359`）的摘要生成（LLM）与 P3.1 的 `buildTailSummary` 是**两条独立压缩触发路径**：P3.1 走 engine `prepareMessages`（每次请求时压缩），P3.4/P3.5 走 chatstore `Compress`（阈值/turn 触发，异步）。**两者不互斥**：chatstore 的 `Compress` 是存储层的「删旧 + 记摘要」（§4），engine 的 trim 是「请求时的预算裁剪 + 摘要替换」。P3.1 完成后，若 P3.5 让 `GetSummary` 返回 LLM 摘要，engine 的 `buildTailSummary` 可以**优先用 `GetSummary` 已产出的摘要**（复用，不重复压缩），`SummaryGenerator` 只在 `GetSummary` 为空时兜底。

### 7.3 本方案给 P3.4/3.5 留下的接口

```go
// agent/engine/compaction.go（新增）
type CompactionOptions struct {
	Enabled          bool
	CacheAnchorTokens int
	// SummaryGenerator is the P3.4 hook: replace deterministic compaction with
	// an LLM summary. engine never imports dino; P3.4 wires it at construction.
	SummaryGenerator func(mid []types.Message) string
}
```

`AgentEngine` 增加字段 `compaction *CompactionOptions`；`NewAgentEngine` 默认 `nil`（= 关闭）；`dino/factory.go` 构造时若 `CompactionPrefix` 开启则赋值（P3.1 阶段 `SummaryGenerator=nil`）。

---

## 8. 迁移步骤（每步独立可验证、可回滚）

| 步 | 内容 | 改动文件 | 验证 | 回滚 |
|---|---|---|---|---|
| **Step 1** | 纯函数改造：`trimHistoryToTokenBudget` 三段式 + 开关。摘要位置**仍走现状**（head system 注入，不动 `1056-1067`）。行为在关闭时与现状一致 | `agent/types/agent.go`（字段+默认）、`agent/engine/agent_execution.go`（`trimHistoryToTokenBudget` 改造 + `prepareMessages` 传摘要进 trim）、新 `agent/engine/compaction.go` | T1-T7 全绿；现有测试（含 `TestPrepareMessages_SummaryInjection`）不破 | `git revert` Step 1 commit |
| **Step 2** | 摘要位置改尾部：`prepareMessages` 的 `1056-1067` 改为尾部 assistant 注入；`Hybrid.GetMessages` 去掉头部注入 | `agent/engine/agent_execution.go`、`dino/chatstore/memory.go` | T8-T10、T13 全绿；更新 `TestPrepareMessages_SummaryInjection` 断言 | `git revert` Step 2 |
| **Step 3** | dino 配置接线：`dino/config.go` + `dino/factory.go` 映射 `CompactionPrefix`/`CacheAnchorTokens`；engine 构造注入 `CompactionOptions` | `dino/config.go`、`dino/factory.go`、`agent/engine/agent.go`（构造） | T11/T12 新增 + dino 现有测试；`examples/dino` 手测 | `git revert` Step 3 |
| **Step 4**（后续/可选） | langchain provider 摘要位置（`langchain_memory.go:74-81`）与 `SimpleMemoryProvider` 滑动窗口（`93-94`）前缀友好化 | `agent/providers/langchain_memory.go` | 现有 engine 测试 | 独立分支评估 |

**Step 1 与 2 分离的原因**：Step 1 只改 trim 算法（输出多一条摘要，但仍在头部 system 拼接 → 缓存收益未完全体现，但预算与正确性已验证）；Step 2 才改位置（缓存收益体现）。两步各自可独立验证、独立回滚，风险最小化。

**Step 3 默认关闭**：`dino/config.go` 默认 `CompactionPrefix: false` → 现有用户零行为变化，仅在显式开启后生效。

---

## 9. 风险点

| 风险 | 等级 | 缓解 |
|---|---|---|
| 摘要 assistant 消息与相邻历史被 `mergeConsecutiveRoles` 合并，模型混淆「摘要」vs「历史」 | 中 | `SUMMARY_PREFIX` 结构化头（§3.3）；`tailStart` 尽量选在 user 边界（§8 调优）；合并不会 400，只影响段 3 breakpoint 锚点位置（段 1/2 不受影响） |
| 预算估算（`RoughTokensForMessage`）对 CJK 偏差大，`mid` 估算小 → 摘要替换后实际 token 超预算 | 中 | **估算与约束同源**：预算契约用同一 `RoughTokensForMessage`（现状已如此，不是本方案引入）；摘要 token 用同一估算 → 契约在「估算空间」严格成立；真实超预算由 `MaxBudgetTokens` 的保守余量吸收。P3.4 若改 token 估算（`optimization-review-vs-codex.md:84` CJK 修正）需同步复核 |
| 摘要位置从头部移到尾部后，`GetSummary` 内容变了 → 压缩边界的缓存段 1（system）重写一次 | 低 | 压缩是低频事件（`CompactAfterTurns`/阈值）；跨压缩边界的单次失效可接受（`prompt-caching.md` §2.3 已定）。**本方案收益是段 1/2 在压缩后不再因「摘要进 system」而失效** |
| `repairLLMMessageToolOrdering` 与摘要交互 | 低 | 摘要无 `ToolCalls`，repair 原样保留（§3.5 已核） |
| `MaxBudgetTokens` 用户同时开 budget + 压缩：双重压缩（chatstore `Compress` 删旧 + engine trim 摘要） | 中 | 两路径职责不同（存储层压缩 vs 请求层裁剪，§7.2）；摘要 token 计入预算 → 不会双重膨胀；文档记录建议 `CompactionPrefix` 与 `MaxBudgetTokens` 一起开 |
| 开启后历史窗口行为变化（`MaxHistoryMessages` 窗口内多一条 assistant 摘要） | 低 | `DeterministicCompact` 摘要通常 < 几条真实消息；`SimpleMemoryProvider.GetMessages` 只按条数窗口，摘要占用 1 条配额，可接受 |
| 与未来 P3.4 LLM 摘要：LLM 摘要质量不稳定导致模型困惑 | 中 | 默认关闭；P3.4 上线时先灰度（`CompactionPrefix` 开关即是逃生门） |
| 字节级兼容承诺破坏（`Enabled=false`） | 低 | `CompactionPrefix` 独立开关，默认 false → 现状字节不变；T1 锁死回归 |

---

## 10. 改动文件清单

**新增**
- `docs/design/compaction-prefix.md` — 本文档
- `agent/engine/compaction.go` — `CompactionOptions`、`buildTailSummary`、三段式 trim 实现（或并入 `agent_execution.go`）
- `agent/engine/compaction_test.go` — T1-T8
- `dino/chatstore/compaction_test.go` — T11-T13

**修改**
- `agent/types/agent.go` — `AgentConfig.CompactionPrefix`、`CacheAnchorTokens`（默认 off/0）
- `agent/engine/agent_execution.go` — `trimHistoryToTokenBudget` 三段式（`1187-1217`）；`prepareMessages` 摘要取用/尾部注入（`1056-1067`）；`buildTailSummary` 调用
- `agent/engine/agent_test.go` — 更新 `TestPrepareMessages_SummaryInjection`；新增 T9/T10
- `dino/chatstore/memory.go` — `Hybrid.GetMessages` 去掉头部摘要注入（`292-298`）
- `dino/config.go` — `MemoryConfig.CompactionPrefix`/`CacheAnchorTokens`
- `dino/factory.go` — 映射进 `agentConfig` + 构造 `CompactionOptions`

**不改**
- `dino/chatstore/compact.go` — `DeterministicCompact` 复用，签名不动（§4.4）
- `agent/engine/agent_execution.go` 的 `repairLLMMessageToolOrdering` — 无需改（§3.5）
- `agent/llm/anthropic_native.go` — breakpoint 布局不动（本方案是「让前缀稳定」，不是改 breakpoint）
- `agent/providers/langchain_memory.go` — 列入 §8 Step 4，本方案不碰
- `docs/design/prompt-caching.md` — 不动（本方案是其 §12.4 遗留的落地，读它不写它）

---

## 11. 留给用户的待定点

1. **默认开关**：本方案默认关闭（保守）。若你倾向默认开（长会话用户多、缓存收益明显），把 `NewAgentConfig` 的 `CompactionPrefix` 改为 `true` + 给 `CacheAnchorTokens` 设个默认（如 2048）即可，但需先跑 I1 集成对照确认摘要不干扰模型行为。
2. **`CacheAnchorTokens` 默认值**：0=不锚头部（摘要仍替换中间，尾部保留）。若开锚，建议从 2048 起调（覆盖初始上下文 + 第一轮 assistant 回合）。
3. **摘要 role**：本方案定 `assistant`（不进 system，保段 1 缓存）。若你担心模型把摘要当「新回复」，可改为 `user` + 摘要前缀——但 user 摘要会在末尾与 input 交替上更接近，且 `mergeConsecutiveRoles` 可能把它并进前一条 user；`assistant` 是更稳的选择，待验证。
4. **是否把 langchain provider 摘要路径（§8 Step 4）纳入本轮**：P3.1 聚焦 dino；agent 直连（非 dino）用户若也用长会话 + prompt caching，需要同一套处理。
5. **P3.4 排期**：本方案不阻塞、也不被 P3.4 阻塞。若你想先要「LLM 摘要质量」再上「前缀保留」，可等 P3.4 后再做 Step 1-2（接口已对齐）；若想先拿缓存收益，现在即可按 §8 落地。
