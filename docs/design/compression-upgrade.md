# 压缩系统升级设计方案：DeterministicCompact → LLM 摘要 + Hybrid 默认化

> 设计日期：2026-08-29
> 对应条目：`docs/next-round.md` **P3.4**（`DeterministicCompact` → LLM 摘要替换）+ **P3.5**（`Hybrid` provider 默认化）
> 参考：评估报告第三章（`docs/optimization-review-vs-codex.md`，Codex `core/src/compact.rs` 做法）、`docs/design/prompt-caching.md` §2.3 / Step 4（head summary → tail summary 记录在案）
> 状态：**设计定稿**，未动业务代码

---

## 0. 结论摘要

| # | 决策 | 理由 |
|---|---|---|
| D1 | `dino/factory.go` 的 `CreateSession` 内构造 `Hybrid`，包裹 `SQLite`/`InMemory`，**默认启用** | `Hybrid` 零调用方是 P3.5 的根因；`NewHybrid(sessionID, provider, llmProvider, config)` 签名与 `memory.go:251` 完全匹配，factory 是唯一有 `llmProvider` 的构造点 |
| D2 | `LLMSummaryAdapter` 桥接 `types.LLMProvider` → `chatstore.LLMProvider`，`GenerateSummary` 用 `ChatWithTools` 调一次摘要模型 | `types.LLMProvider`（`llm.go:6-18`）**没有** `GenerateSummary` 方法，`memory.go:237-239` 需要它；不改 `types.LLMProvider` 接口体（复用 prompt-caching 的 R3 断言经验） |
| D3 | 摘要作为 **user 消息**、标记编码、**放在历史最后**（输入前） | Codex `SUMMARY_PREFIX` 模型（`compact.rs:657-733`）；保护 tools breakpoint 之前的缓存段（`prompt-caching.md:142` 记录在案） |
| D4 | **尾部原文保留**：最近 `KeepRecentCount` 条消息 + 最近若干条 user 消息原文，上限 `MaxRecentTailTokens`（默认 20k，`EstimateTokens` 计） | Codex `COMPACT_USER_MESSAGE_MAX_TOKENS = 20_000`；`memory.go:315-319` 已有 `KeepRecentCount` 尾部保留骨架 |
| D5 | `DeterministicCompact` **保留作 fallback**：LLM 失败/超时 → `generateBasicSummary` → `DeterministicCompact`（可控降级，绝不丢历史） | `memory.go:335-342` 已有 `err → generateBasicSummary` 分支 |
| D6 | `GetSummary` 注入点**不动**（`agent_execution.go:1056-1067`，engine 组装时 system 之后、history 之前）；新增摘要尾注放 `history` 末尾 | 头部注入不阻塞本次 P3.4/P3.5；D6b 待评估（见 §6） |
| D7 | **P3.5 与 P3.4 合并为一条迁移主线**，四步，每步独立可验证（§8） | P3.5 是 P3.4 的前置接线，拆开无意义 |

---

## 1. 现状（已核实）

### 1.1 压缩 provider 全景

- **Provider 接口**：`dino/chatstore/memory.go:25-33` — `AddMessage` / `GetMessages` / `GetSummary` / `Compress` / `Clear` / `GetStats` / `StoredMessageCount`。
- **`InMemory`**（`memory.go:62-84`）：`SimpleMemoryProvider` 包装 + `generateSummary`（`memory.go:179-184`）→ `DeterministicCompact`（纯本地启发式）。压缩触发：`AddMessage` 计数超 `MemoryCompressThreshold` 后 `compressAsync`（`memory.go:98-102`）。
- **`SQLite`**（`sqlite.go:20-27`）：`Compress`（`sqlite.go:287-313`）`DeterministicCompact("", old, ...)` → 删旧行 → `metadata.summary` 写入。摘要持久化，`GetSummary`（`sqlite.go:275-285`）从 `metadata` 读。
- **`Hybrid`**（`memory.go:241-262`）：**已实现、零调用方**。`NewHybrid(sessionID, provider, llmProvider, config)`。`Compress`（`memory.go:309-359`）已有骨架：尾部保留 `KeepRecentCount` + LLM 摘要（`memory.go:335-342`）+ `provider.Clear` + 尾部回写。**缺**：尾部 user 原文保留（现在只保固定条数）、摘要位置/编码。
- **`chatstore.LLMProvider`**（`memory.go:237-239`）：`GenerateSummary(ctx, messages) (string, error)` — 唯一 LLM 摘要入口。`types.LLMProvider` **无此方法**。

### 1.2 接线现状

- `dino/factory.go:394-499` `NewDinoFactory`：`createLLMProvider`（`config.go:301-309`）→ `llmProvider`（`types.LLMProvider`）。
- `dino/factory.go:651-676` `CreateSession`：`NewSQLite`（`Type=="sqlite"` 或 `PersistEnabled`）else `NewInMemory` → `agent.SetMemory(&memoryAdapter{provider})`。
- `dino/factory.go:170-172` `memoryAdapter.GetSummary`：转发到 `provider.GetSummary`（已接线）。
- `agent_execution.go:1056-1067` `prepareMessages`：类型断言 `GetSummary` → 非空摘要作为 **system 消息**插 system 之后、history 之前（R3 断言式，接口体未加方法）。

### 1.3 双压缩触发路径

1. **chatstore 内自触发**：`AddMessage` 计数超阈值（`memory.go:98-102` / `sqlite.go:228-232`）→ 后台 `compressAsync`。
2. **engine 触发**：`saveToMemoryAndMaybeCompress`（`agent_execution.go:652-749`）turn 保存后，按 `MemoryCompressThreshold`（byCount）/ `CompactAfterTurns`（byTurn）→ `ae.memory.CompressMemory(compressCtx, llm, keepWindow)`（`agent_execution.go:740`）→ `memoryAdapter.CompressMemory`（`factory.go:197-199`）→ `provider.Compress(ctx)`。
   - 传入的 `llm` 是 `ae.model`（`agent_execution.go:712`）——**engine 路径已把 `types.LLMProvider` 递给 memory**，只是 `InMemory`/`SQLite` 的 `Compress` 忽略它（用 `DeterministicCompact`）。
   - 这条路径是 LLM 摘要的关键：`CompressMemory` 有 `llm` 参数可用（`factory.go:197`），但当前 `memoryAdapter.CompressMemory` 丢弃 `llm` 与 `maxMessages`。

### 1.4 预算裁剪层（分工参照）

- `trimHistoryToTokenBudget`（`agent_execution.go:1187-1217`）：**engine 内**、每次 `prepareMessages` 都跑的**最后防线**——把 `history`（已含摘要注入后的完整列表）按 `MaxBudgetTokens` 预算从尾部向前裁（丢最旧，`used+c > rem` 即 break，`agent_execution.go:1205-1211`）。
- 位置：`prepareMessages` 组装 `system + summary + history + input`（`agent_execution.go:1046-1102`），`trimHistoryToTokenBudget` 只作用于 `history`（`agent_execution.go:1088`），**不裁剪 system/summary/input**。
- 含义：**若摘要注入到 history 尾部，trim 会把它当普通消息；若按 budget 超限，摘要被丢，尾部 user 原文可能保留。** 这是本设计 §3.3 / §6 的张力点。

### 1.5 token 估算

- 三处不一致：`compact.go:26-38` `EstimateTokens`（非 ASCII `*0.5`）、`memory.go:411-422` `estimateTokens`（非 ASCII `*2`）、`agent/types/tokens.go:7-18` `RoughTokenEstimate`（非 ASCII `*2`）。
- 对 CJK（UTF-8 3 字节/rune，非 ASCII 计）三者给出**互相矛盾**的数字。摘要尾部保留的预算计算必须统一。

---

## 2. 目标模型（对齐 Codex）

```
[system 提示]                     ← tools breakpoint 之前，缓存前缀（不动）
[初始上下文（L1 长期记忆等）]       ← 在最后一条真实 user 消息之前重注入
[history 中段（被压缩掉的部分）]    ← 由 LLM summary 替代
[尾部 user 原文（最近若干条，≤预算）]
[Summary（最后一项，user 消息，标记编码）]
[input user 消息]                 ← 最后一条真实 user 消息
```

Codex 对应：本地 compact = 最近用户消息尾部原文（保留到 `COMPACT_USER_MESSAGE_MAX_TOKENS = 20_000`）+ LLM summary；summary 作为**最后一项**注入；初始上下文在最后一条真实用户消息**前**重注入，绝不在 summary 之后；summary 以 `SUMMARY_PREFIX` 标记编码为 user 消息（`optimization-review-vs-codex.md:66-84`）。

---

## 3. P3.5 Hybrid 默认化

### 3.1 构造点：`dino/factory.go` `CreateSession`

`dino/factory.go:651-676` 现在 `memProvider` 直接赋值。改为：

```go
// dino/factory.go CreateSession，替换 651-676 段
memProvider = /* NewSQLite 或 NewInMemory，原逻辑不变 */
if f.config.Memory.EnableLLMCompress {
    var lsm chatstore.LLMProvider
    if f.llmProvider != nil {
        lsm = chatstore.NewLLMSummaryAdapter(f.llmProvider)   // §3.3
    }
    memProvider = chatstore.NewHybrid(sessionID, memProvider, lsm, memConfig)
    logger.Info("[DinoFactory] Hybrid memory wrapper enabled", slog.String("session_id", sessionID))
}
agent.SetMemory(ctx, &memoryAdapter{provider: memProvider})
```

- **`memConfig` 即 `factory.go:652-659` 已构造的 `*chatstore.Config`**，直接复用；`NewHybrid` 缺省 config 逻辑在 `memory.go:251-262` 已兜底。
- `sessionID` 即当前 session。
- `f.llmProvider` 在 `NewDinoFactory` 里 `createLLMProvider` 已就绪（`factory.go:399`），`DinoFactory` 接口暴露 `GetLLMProvider()`（`factory.go:295`），无需新增。

### 3.2 默认开 or 开关：**默认开 + 开关**

- **默认开**：P3.5 的动机就是 `Hybrid` 从未被构造、摘要不产出（`next-round.md:86-88`）。默认开才能让已接线的 `GetSummary` 注入真正生效。
- **开关**：`dino/config.go:41-51` `MemoryConfig` 新增字段 `EnableLLMCompress bool`（yaml `enable_llm_compress`），`DefaultConfig`（`config.go:216-224`）置 `true`。
  - 注意命名：chatstore 已有 `EnableMemoryCompress`（`memory.go:43`）控制**是否压缩**；新开关只控制**压缩是否走 LLM 摘要**（否则退回 `DeterministicCompact`）。语义正交。
- **hostconfig 链路**（`dino/hostconfig/`）：`MemorySettings`（`types.go:25-30`）加字段 + `coalesce.go:237-257` 区段合并；默认 `true`，显式 `false` 覆盖。与现有 `EnableCompress`/`KeepRecentCount` 合并风格一致（`coalesce.go:244-255`）。

### 3.3 桥接适配器 `NewLLMSummaryAdapter`

`types.LLMProvider`（`llm.go:6-18`）没有 `GenerateSummary`。新增：

```go
// dino/chatstore/llm_summary.go（新文件）
package chatstore

// LLMSummaryAdapter 桥接 types.LLMProvider → chatstore.LLMProvider。
// 不改 types.LLMProvider 接口体（与 prompt-caching R3 的类型断言策略一致）。
type LLMSummaryAdapter struct {
    llm types.LLMProvider
}

func NewLLMSummaryAdapter(llm types.LLMProvider) *LLMSummaryAdapter {
    return &LLMSummaryAdapter{llm: llm}
}

func (a *LLMSummaryAdapter) GenerateSummary(ctx context.Context, messages []Message) (string, error) {
    return a.summarize(ctx, messages)
}

const summarySystemPrompt = `You are a conversation compactor for an AI agent.
Produce a concise summary of the conversation below, in the same language as the conversation.
Keep: the user's goals and constraints, decisions made, files touched, pending work, and unresolved issues.
Preserve exact file paths, command names, tool names, and any code identifiers verbatim.
Omit: tool output bodies, boilerplate, and incidental detail.
Output only the summary text, no preamble.`

func (a *LLMSummaryAdapter) summarize(ctx context.Context, messages []Message) (string, error) {
    if a.llm == nil {
        return "", nil
    }
    msgs := make([]types.Message, 0, 1+len(messages))
    msgs = append(msgs, types.Message{Role: "system", Content: summarySystemPrompt})
    for _, m := range messages {
        msgs = append(msgs, types.Message{Role: m.Role, Content: m.Content, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID})
    }
    resp, err := a.llm.ChatWithTools(ctx, msgs, nil)   // 摘要不需要工具
    if err != nil {
        return "", err
    }
    return strings.TrimSpace(resp.Content), nil
}
```

要点：
- 复用 `types.Message` / `ChatWithTools`，不新开连接、不新增模型配置；`GetModelName()`（`llm.go:16`）可作日志字段。
- **超时**：`CompressMemory` 传的 `compressCtx` 已有预算（`agent_execution.go:732-738`：默认 `Timeout`，下限 2min）；`Hybrid.Compress` 的 LLM 失败即回退（§4.2），无需额外重试——一次尝试失败即降级，避免 LLM 摘要与主循环抢 token。
- **可选**：`NewLLMSummaryAdapter` 构造时可接受 `maxSummaryTokens`（默认 e.g. 2000）→ prompt 里加 "不超过 N tokens"，模型侧近似约束。

### 3.4 `NewHybrid` 签名满足

`NewHybrid(sessionID string, provider Provider, llmProvider LLMProvider, config *Config)`（`memory.go:251`）——上述调用逐参满足，**签名零改动**。

---

## 4. P3.4 DeterministicCompact → LLM 摘要替换

### 4.1 调用点：`Hybrid.Compress`（`memory.go:309-359`）

当前 `Compress` 已含 LLM 分支（`memory.go:334-342`）。改造为四段：

```go
func (m *Hybrid) Compress(ctx context.Context) error {
    messages, err := m.provider.GetMessages(ctx, 0)      // 全量
    if err != nil { return err }
    if len(messages) <= m.config.KeepRecentCount { return nil }

    // A) 尾部原文保留（新）：最近 user 消息原文，预算 MaxRecentTailTokens
    tailUser, older := m.splitTailUserMessages(messages) // §4.3

    // B) 前次摘要前置（已有：memory.go:326-332）
    m.summaryMu.RLock(); existing := m.summary; m.summaryMu.RUnlock()
    if existing != "" {
        older = append([]Message{{Role: "system", Content: "Previous Summary:\n" + existing}}, older...)
    }

    // C) LLM 摘要 + 确定性 fallback（memory.go:334-342 保留）
    var summary string
    if m.llmProvider != nil {
        if s, err := m.llmProvider.GenerateSummary(ctx, older); err == nil && strings.TrimSpace(s) != "" {
            summary = s
        } else if err != nil {
            logger.Warn("[Hybrid] LLM summary failed, using deterministic fallback", slog.String("error", err.Error()))
            summary = DeterministicCompact(existing, older, DefaultCompactConfig())
        } else {
            summary = DeterministicCompact(existing, older, DefaultCompactConfig())
        }
    } else {
        summary = DeterministicCompact(existing, older, DefaultCompactConfig())
    }

    // D) 落盘：清空 → 写回 tailUser（尾部原文）+ 摘要入 provider.GetSummary
    m.summaryMu.Lock(); m.summary = summary; m.summaryMu.Unlock()
    if err := m.provider.Clear(ctx); err != nil { return err }
    for _, msg := range tailUser {
        _ = m.provider.AddMessage(ctx, msg)
    }
    return nil
}
```

关键差异 vs 现状：
- 尾部保留从「固定 `KeepRecentCount` 条」升级为「`KeepRecentCount` 条 + 按 token 预算的 user 原文」（§4.3）。
- LLM 失败从 `generateBasicSummary`（`memory.go:391-409`，模板废话）改为 `DeterministicCompact`（`compact.go:42-140`，结构化启发式）——保留真实信息，非模板。

### 4.2 `DeterministicCompact` 保留作 fallback（决策 D5）

- **保留**。理由：LLM 摘要失败/超时/空结果时必须有真实信息的降级，`DeterministicCompact` 是现成、无网络、纯本地。`generateBasicSummary`（`memory.go:391-409`）可退役（无调用方后删除）。
- 降级链：`LLM 成功 → LLM 摘要`；`LLM 失败/空 → DeterministicCompact`；`messages 空 → 保留原 summary`（`compact.go:43-45` 已处理）。
- 错误路径：`Hybrid.Compress` 的 LLM 调用**不允许**因超时阻塞主路径太久——`compressCtx` 超时（`agent_execution.go:732-738`）兜底；`autoCompress`（`memory.go:381-389`）与 `addMessage` 内的压缩（`memory.go:272-276`）都在 goroutine，不阻塞 `AddMessage` 返回。

### 4.3 尾部原文保留策略

新增 `chatstore` 包内函数（`compact.go` 或 `memory.go`）：

```go
// MaxRecentTailTokens 尾部 user 原文 token 预算（对齐 Codex COMPACT_USER_MESSAGE_MAX_TOKENS = 20_000）。
const MaxRecentTailTokens = 20_000

// splitTailUserMessages 从 messages 尾部回溯，收集：
//  1. 最近 KeepRecentCount 条消息（所有 role，原文）；
//  2. 往前继续吸收 user 消息原文，直到累积 token 预算 MaxRecentTailTokens 或消息耗尽。
// 返回 (tail, older)。older 送 LLM 摘要，tail 回写 provider。
func splitTailUserMessages(messages []Message, keepRecent int, maxTailTokens int) (tail []Message, older []Message)
```

规则（对齐 Codex `compact.rs:586-642` / `657-733` 精神）：
1. 以**最后一条真实 user 消息**为锚，其后的消息（含 assistant/tool 收尾）全部保留。
2. 向前吸收更多 user 消息原文，直到累计 `EstimateTokens`（统一 §5.1）≥ `MaxRecentTailTokens` 或到达第 1 条消息。
3. `KeepRecentCount`（消息条数下限）继续生效——`tail` 至少包含最近 `KeepRecentCount` 条消息（兼容现有 `memory.go:315-319` / `sqlite.go:296-306` 语义），**取两者并集的尾部**。
4. 切割点必须落在完整消息边界（不能拦腰截断一条消息）；从 `messages` 下标切分，`tail = messages[split:]`，`older = messages[:split]`。
5. `splitTailUserMessages` 返回后，`older` 中的**非 user 中间消息**由 LLM 摘要处理；`tail` 保持原文结构（含 tool_calls 完整配对，避免 `repairLLMMessageToolOrdering` 修复成本，`agent_execution.go:1108-1185`）。

### 4.4 摘要格式（marker/编码）

- **存储格式**：纯文本，前缀标记 `[Summary]`（对齐 Codex `SUMMARY_PREFIX` 语义）。`m.summary` 存裸文本，`GetSummary`（`memory.go:303-307`）返回前不额外包装——marker 只用于**注入侧**区分。
- **注入格式**（`Hybrid.GetMessages`，`memory.go:282-301` 现状是把 summary 包成 system 插**头部**，需改）：

```go
// Hybrid.GetMessages 改造：summary 作为最后一项（user 消息），不插头部。
// 头部注入职责移交给 engine（agent_execution.go:1056-1067 保持现状，D6a）。
func (m *Hybrid) GetMessages(ctx context.Context, limit int) ([]Message, error) {
    messages, err := m.provider.GetMessages(ctx, limit)
    if err != nil { return nil, err }
    m.summaryMu.RLock(); summary := m.summary; m.summaryMu.RUnlock()
    if summary != "" {
        messages = append(messages, Message{
            Role:    "user",
            Content: "[Summary]\n" + summary,
        })
    }
    return messages, nil
}
```

- **engine 注入侧**（`agent_execution.go:1056-1067`）保持 system 头部注入**不动**（D6a，§6 分析）：`prepareMessages` 里 `history` 末尾会被追加 `input.ToMessage("user")`（`agent_execution.go:1100`），若 `GetMessages` 已把 summary 放尾部，最终顺序为 `system + L1 + history… + summary(user) + input(user)`——但 Anthropic 侧 `mergeConsecutiveRoles`（`anthropic_native.go:653`）会合并相邻 user；`input` 是「最后一条真实 user 消息」，summary 在其**前**，符合「绝不在 summary 之后」约束。

---

## 5. 配套修正

### 5.1 token 估算统一

三处实现互相矛盾（§1.5）。**设计决策**：`chatstore` 内统一到 `EstimateTokens`（`compact.go:26-38`），并修正其非 ASCII 系数与 `agent/types.RoughTokenEstimate`（`tokens.go:7-18`）一致（`*2`）。

```go
// compact.go:26-38 修正：非 ASCII 系数 *2（对齐 agent/types/tokens.go:17）
func EstimateTokens(text string) int {
    asciiCount := 0
    for _, r := range text {
        if r < 128 { asciiCount++ }
    }
    nonASCII := utf8.RuneCountInString(text) - asciiCount
    return asciiCount/4 + nonASCII*2 + 1
}
```

- `memory.go:411-422` 的重复 `estimateTokens` 删除，统一引用 `EstimateTokens`。
- 注：`EstimateTokens`（chatstore）与 `RoughTokenEstimate`（agent/types）仍分属两包，但**系数一致**后尾部预算（chatstore）与 engine 预算裁剪（`agent_execution.go:1187`）在 CJK 上的判断不再互相矛盾。
- CJK 偏差说明：UTF-8 中文 1 rune 3 字节，按 rune 计非 ASCII → `*2` tokens/rune，比字节级保守但单调，够作预算用；字节级精确分词是后续任务（`optimization-review-vs-codex.md:83`）。

### 5.2 `memoryAdapter.CompressMemory` 透传 llm

`factory.go:197-199` 现在丢弃 `llm` 与 `maxMessages`。改：

```go
func (m *memoryAdapter) CompressMemory(ctx context.Context, llm types.LLMProvider, maxMessages int) error {
    // llm 已由 engine 传入（agent_execution.go:712 ae.model）；
    // Hybrid 在 factory 构造时已注入 LLMSummaryAdapter，无需在此二次传递。
    // maxMessages 留作尾部预算上限的提示（本版沿用 provider 内部 Config，不强耦合）。
    return m.provider.Compress(ctx)
}
```

保持签名（`agent/types/llm.go:126` 接口要求），实现不变即可——**Hybrid 在构造时已持有 `llmProvider`**，engine 路径的 `llm` 参数不再需要；注释说明即可，避免双源。

### 5.3 `SQLite` 摘要对称性（可选，随步 4）

`SQLite.Compress`（`sqlite.go:287-313`）当前用 `DeterministicCompact` 且摘要持久化到 `metadata`。若 `Type=="sqlite"` + `EnableLLMCompress`，`Hybrid.Compress` 已接管摘要（§4.1），但 `SQLite` 内部还有独立的 `compressAsync`（`sqlite.go:228-232`）会**双触发**。

**设计**：`EnableLLMCompress` 时，`factory.go` 把底层 provider 的 `config.EnableMemoryCompress` 置 `false`，压缩统一由 `Hybrid` 触发，避免 `SQLite.compressAsync` 与 `Hybrid.Compress` 竞争写 `metadata`/删行：

```go
// factory.go CreateSession
if f.config.Memory.EnableLLMCompress {
    memConfig.EnableMemoryCompress = false   // 压缩统一由 Hybrid 层驱动
}
```

`SQLite.GetSummary`/`metadata.summary` 保留（`Hybrid.Compress` 通过 `provider.AddMessage`/`Clear` 间接维护 DB，摘要仍持久化在 `Hybrid.summary` 内存——重启丢失；如需 SQLite 持久化摘要，步 4 可选把 `Hybrid.Compress` 的 summary 同步写 `metadata`，列为待定点 A4）。

---

## 6. 与 engine `trimHistoryToTokenBudget` 的分工

### 6.1 分层

| 层 | 触发 | 作用域 | 手段 | 结果 |
|---|---|---|---|---|
| **chatstore 压缩**（`Hybrid.Compress` / `SQLite.Compress`） | 消息数超阈值 / `CompactAfterTurns`（`agent_execution.go:664-748`） | **跨 turn**，把「旧消息」折叠成摘要 | LLM/确定性摘要 + 尾部原文保留 + `Clear` 删旧 | 历史规模**数量级下降**，摘要 + 尾部 |
| **engine 预算裁剪**（`trimHistoryToTokenBudget`） | 每次 `prepareMessages`（`agent_execution.go:1088`） | **单次 turn**，token 硬预算 | 从尾向前按 token 丢最旧（`agent_execution.go:1205-1211`） | 保证单次请求**严格不超预算** |

- 职责互补：chatstore 处理「对话很长、跨 turn 增长」；engine 处理「单次请求被 `MaxBudgetTokens`/`RemainPromptTokens` 卡死」的最后防线。chatstore 压缩让 engine 裁剪几乎不触发（历史变小）。
- **prompt cache 角度**：`trimHistoryToTokenBudget` 丢最旧破坏前缀（`next-round.md:62`），chatstore 尾部保留不破坏——所以升级后长会话命中率由 chatstore 压缩质量决定，engine 裁剪仅兜底极端预算。

### 6.2 摘要注入后 engine 能否感知

- **能（现状即感知）**：`prepareMessages` 的类型断言 `GetSummary`（`agent_execution.go:1056-1067`）读的是 `memoryAdapter.GetSummary → provider.GetSummary → Hybrid.summary`，压缩落盘后摘要立即出现在 system 之后。**注入点不改**（D6a）。
- **尾部摘要与 trim 的交互**（§3.3 张力）：`Hybrid.GetMessages` 把 summary 放尾部后，`trimHistoryToTokenBudget` 会对它一视同仁按 token 裁。**设计**：`prepareMessages` 中把 `history` 的最后一个 user 消息（即 summary marker）识别为**不可裁**？——否，过度复杂。折中：让 summary 的 token 估算极小（`EstimateTokens("[Summary]\n"+summary)` 已计入预算），且 `trimHistoryToTokenBudget` 从尾向前裁（`agent_execution.go:1205`）会**先丢 summary 再丢真实 user 原文**——因为 summary 在 history 最后。这正是 Codex 相反（summary 最后 → 缓存前缀稳定；但预算裁剪会先牺牲 summary）。

  **决策 D6b（待评估，本节默认不实现）**：若实测发现预算裁剪总把摘要裁掉，步 4 可加「summary 消息豁免」——在 `trimHistoryToTokenBudget` 里对 `Content` 以 `[Summary]` 开头的最后一条消息跳过裁剪。**工作量小，但改 engine 语义，需产品确认**。默认行为先让数据说话（可观测：`Stats.SummaryLength`）。

### 6.3 双触发去重

engine 的 byCount 触发（`agent_execution.go:698`）与 chatstore 的 `AddMessage` 计数触发（`memory.go:272-276`）可能**先后触发两次压缩**。`Hybrid.compressing` atomic（`memory.go:248`）+ `memoryCompressMu`（`agent_execution.go:730`）各自防重，但两把锁不互斥。**设计**：`EnableLLMCompress` 默认开时，保留 chatstore 自触发（响应快）但把阈值设到比 engine `CompressThreshold` 更高，使 engine 路径通常先到；或步 4 评估关掉 chatstore 自触发、只留 engine 路径。列为待定点 A5。

---

## 7. 改动文件清单

| 文件 | 改动 | 类型 |
|---|---|---|
| `dino/config.go` | `MemoryConfig` 加 `EnableLLMCompress bool`（yaml `enable_llm_compress`）；`DefaultConfig` 置 `true` | 新增字段 |
| `dino/hostconfig/types.go` | `MemorySettings` 加 `EnableLLMCompress bool` | 新增字段 |
| `dino/hostconfig/coalesce.go` | §3.2 合并逻辑 | 改 |
| `dino/factory.go` | `CreateSession` 651-676 段：`NewHybrid` 包裹 + `EnableLLMCompress` 时关底层自触发；`CompressMemory` 注释透传（§5.2） | 改 |
| `dino/chatstore/llm_summary.go` | **新**：`LLMSummaryAdapter` + `summarySystemPrompt` | 新增 |
| `dino/chatstore/memory.go` | `Hybrid.GetMessages` 摘要移尾部（§4.4）；`Hybrid.Compress` 四段改造（§4.1）；删重复 `estimateTokens` | 改 |
| `dino/chatstore/compact.go` | `EstimateTokens` 系数统一（§5.1）；新增 `splitTailUserMessages` + `MaxRecentTailTokens` | 改/增 |
| `dino/chatstore/sqlite.go` | 不改（`SQLite.Compress` 保留确定性，作为 `Hybrid` 底层；`metadata.summary` 保留） | 无（除非 A4） |
| `agent/engine/agent_execution.go` | **不改**（注入点保持 `1056-1067`；`trimHistoryToTokenBudget` 不动；D6b 待评估） | 无（除非 D6b） |

---

## 8. 迁移顺序（每步独立可验证）

| 步 | 内容 | 交付 | 验证 |
|---|---|---|---|
| **步 1** | P3.5 接线：`EnableLLMCompress` 字段 + factory `NewHybrid` 包裹 + `NewLLMSummaryAdapter` | Hybrid 被构造；`GetSummary` 链路（engine → memoryAdapter → Hybrid）首次产出摘要 | 单测：factory 构造后 `provider` 是 `*Hybrid`；集成：`GetSummary` 非空；日志含 `Hybrid memory wrapper enabled` |
| **步 2** | `Hybrid.Compress` 改：LLM 摘要 + `DeterministicCompact` fallback（替换 `generateBasicSummary`）；摘要 marker | LLM 摘要真正写入 `Hybrid.summary`；失败回退确定性 | 单测：mock LLM 成功/失败/空 → 摘要分别为 LLM 文本 / `DeterministicCompact` 输出；集成：压缩后 `GetSummary` 非空、有 `[Summary]` 注入 |
| **步 3** | 尾部原文保留：`splitTailUserMessages` + `MaxRecentTailTokens`；`EstimateTokens` 统一 | 尾部 user 原文按 token 预算保留，`older` 送摘要 | 单测：预算边界、CJK 文本、切割点完整消息、`KeepRecentCount` 下限 |
| **步 4** | 摘要注入位置：`Hybrid.GetMessages` 尾部；评估 D6b / A4 / A5 | 对齐 Codex「摘要最后一项 + 绝不在 input 之后」 | 集成：`GetMessages` 顺序断言（history…, summary, input）；实测预算裁剪是否裁掉摘要（可观测 `Stats.SummaryLength`） |
| **步 5**（可选收尾） | `generateBasicSummary` 删除（无调用方）；`SQLite` 摘要持久化对称（A4） | 死代码清理 | `go build ./...` + `grep` 无引用 |

每步 commit（Conventional Commits），各自可回滚；步 1/2 不改变注入顺序（头部摘要）故可先合入 dev 冒烟，步 3/4 是行为变更需评估报告。

---

## 9. 测试清单

### 9.1 单测（`dino/chatstore`）

| 用例 | 覆盖 |
|---|---|
| `TestNewHybrid_NilProvider/NilLLM/NilConfig` | 构造兜底（`memory.go:251-262`） |
| `TestLLMSummaryAdapter_GenerateSummary` | mock `types.LLMProvider`（`ChatWithTools` 返回摘要）；断言 system prompt + `messages` 透传、`Content` trim |
| `TestHybrid_Compress_LLMSuccess` | mock LLM 返回摘要 → `summary` 更新、尾部保留 `tail`、`older` 送 LLM |
| `TestHybrid_Compress_LLMFail_Fallback` | mock LLM 返回 error → 回退 `DeterministicCompact`（非空、含 `<scope>` 等标记） |
| `TestHybrid_Compress_LLMEmpty_Fallback` | mock 返回空串 → 同回退 |
| `TestSplitTailUserMessages_Budget` | 纯 ASCII 大文本：切分到 `MaxRecentTailTokens` |
| `TestSplitTailUserMessages_CJK` | 中文文本：预算按 rune `*2` 计，不超限、不截断消息 |
| `TestSplitTailUserMessages_MinKeep` | `KeepRecentCount` 下限优先于 token 预算 |
| `TestSplitTailUserMessages_CompleteMessages` | 切割点不拦腰断消息（tool_calls 配对完整） |
| `TestEstimateTokens_CJK` | 修正后系数（非 ASCII `*2`）与 `agent/types.RoughTokenEstimate` 一致 |
| `TestHybrid_GetMessages_SummaryTail` | summary 为 user 消息、最后一项、带 `[Summary]` 前缀 |
| `TestHybrid_GetMessages_NoSummary` | summary 空 → 不追加 |

### 9.2 集成测试（`dino` / `dino/mem`）

| 用例 | 覆盖 |
|---|---|
| `TestFactory_CreateSession_HybridWrapped` | `SetMemory` 的 provider 是 `*Hybrid`；`EnableLLMCompress=false` 时不是 |
| `TestEngine_GetSummaryInjection` | 构造 `Hybrid` + 预置 summary → `prepareMessages` 输出 system 后含摘要（现状 `agent_execution.go:1056-1067` 断言） |
| `TestCompressMemory_EnginePath` | `saveToMemoryAndMaybeCompress` 触发 → `memoryAdapter.CompressMemory` → `Hybrid.Compress` → `GetSummary` 非空（mock `ae.model`） |
| `TestHybrid_SQLite_Roundtrip` | `Type=="sqlite"` + `EnableLLMCompress`：压缩后消息行数降、尾部保留、摘要不空；`EnableLLMCompress` 时底层自触发关闭（§5.3） |

### 9.3 手动 / 可观测

- 跑一个 >50 消息的长会话（CJK prompt），观察：`Stats.SummaryLength > 0`、`GetStats().MessageCount` 回落、日志 `Memory compressed successfully`（`agent_execution.go:743`）。
- 预算裁剪是否裁掉尾部摘要（D6b 数据点）。
- 摘要质量抽查：文件路径/工具名/待办是否保留（`summarySystemPrompt` 要求 verbatim）。

---

## 10. 风险

| 风险 | 影响 | 缓解 |
|---|---|---|
| **LLM 摘要失败/超时** | 摘要为空 → `GetSummary` 注入空 → 历史信息丢失（若尾部原文也没留够） | 三段降级链（§4.2）+ `compressCtx` 超时（`agent_execution.go:732-738`）+ 尾部原文兜底；`DeterministicCompact` 保证非空 |
| **LLM 摘要 token 成本** | 每次压缩一次摘要调用；长会话多次压缩成本累积 | 摘要调用在 goroutine、`EnableLLMCompress` 开关可关；压缩阈值调高降低频率；`summarySystemPrompt` 约束输出长度 |
| **CJK 估算偏差** | 尾部预算或 trim 裁剪在 CJK 下误判（少保留/多保留） | `EstimateTokens` 统一为 rune `*2`（§5.1），与 engine 一致；字节级精确分词列为后续任务 |
| **双触发竞争** | chatstore 自触发 + engine 触发并发压缩，`metadata`/删行竞争 | `EnableLLMCompress` 时关底层自触发（§5.3）+ `compressing`/`memoryCompressMu` 防重；A5 待评估 |
| **摘要注入顺序破坏** | summary 在尾部但被 `trimHistoryToTokenBudget` 裁掉 → 缓存前缀仍稳定但摘要丢失 | D6b 待评估（summary 豁免裁剪）；可观测 `Stats.SummaryLength` 数据说话 |
| **摘要 marker 被模型误解** | `[Summary]` 标记、user role 可能与真实 user 消息混淆 | marker 语义弱（Codex 同款）；`summarySystemPrompt` 明示「这是压缩摘要非用户请求」；可后续换 `system` role + 尾部位置（若 provider 允许） |
| **SQLite 摘要不持久化** | 重启后 `Hybrid.summary` 内存丢失，`metadata.summary` 与 `Hybrid.summary` 不一致 | A4 待评估：`Hybrid.Compress` 同步写 `metadata`（`INSERT OR REPLACE`） |

---

## 11. 留给用户的待定点

| # | 待定点 | 默认 | 影响 |
|---|---|---|---|
| A1 | `EnableLLMCompress` 默认 `true` 是否接受（意味着压缩开始消耗 LLM token） | `true`（P3.5 动机） | token 成本 |
| A2 | 摘要注入位置：D6a（engine 头部，现状不动）vs 完整 Codex 模型（尾部摘要 + `trimHistoryToTokenBudget` 豁免 `[Summary]`） | D6a | prompt cache 前缀（长会话命中率） |
| A3 | `splitTailUserMessages` 的 `KeepRecentCount` 与 `MaxRecentTailTokens` 默认值（20k token / 10 条）是否需要 YAML 暴露 | 常量默认，后续可配置 | 尾部保留量 |
| A4 | `Hybrid` 摘要是否同步写 `SQLite metadata`（重启持久化） | 不写（内存摘要） | 重启后摘要丢失 |
| A5 | chatstore 自触发压缩（`memory.go:272-276`）与 engine 触发（`agent_execution.go:698`）是否收敛为单一触发源 | 双触发 + 阈值拉开 | 压缩频率 / 竞争 |
| A6 | `summarySystemPrompt` 语言跟随（「same language as conversation」）是否对 CJK 摘要质量足够，是否需要显式 `SummaryLanguage` 配置 | 跟随对话 | CJK 摘要质量 |

---

## 12. 参考

- `docs/optimization-review-vs-codex.md` 第三章（Compaction，`compact.rs` 做法）
- `docs/next-round.md` P3.4 / P3.5
- `docs/design/prompt-caching.md` §2.3、Step 4（摘要头部→尾部记录在案）
- `docs/design/longterm-memory.md`（`GetSummary` 接线、评审 R3）
