# 评审：Compaction 前缀保留（P3.1 · prompt caching Step 4）

> 被评文档：`docs/design/compaction-prefix.md`（2026-08-29）
> 评审日期：2026-08-29 · 评审基线：`p3-review-compaction` worktree
> 评审范围：事实核查（逐条核实 file:line / 函数 / 断言）→ 逻辑正确性 → 可行性 → 结论

---

## 0. 结论摘要

| 维度 | 结论 |
|---|---|
| **能否进入实现** | **可以，但必须先解决 2 个 BLOCKER**：① §3.3 `buildTailSummary` 直接调 `chatstore.DeterministicCompact` 会引入 `agent/engine → dino/chatstore` 反向依赖（与设计自己的 §7.3「engine never imports dino」自相矛盾）；② §2.1「段3 跨 compaction 存活」目标在 Anthropic 全前缀缓存语义下**不可实现**，且与 §0「前 2 段」自相矛盾，需统一验收口径。 |
| **从哪步开始** | 修完 BLOCKER 后从 **Step 1**（纯函数三段式 trim + 开关）开始；但 Step 1 与 Step 2 建议**合并为一次提交**，否则中间态存在「双摘要」（头部 system 摘要 + 尾部 assistant 摘要）回归。 |
| **与压缩升级（P3.4/3.5）是否需串行** | **不需串行**。纯 `DeterministicCompact` 上可独立落地；但 `SummaryGenerator` 接口签名需统一（见 REC-3），否则 P3.4 挂接点错位。 |
| **核心收益是否成立** | **成立**。把摘要从 system 移到尾部 + trim 保头，使 **段1（system）+ 段2（tools）跨 compaction 存活**——这是本方案真实且正确的收益。 |
| 事实核查 | 22 处引用，21 处准确，1 处（§1.3 行号）轻微偏差 |

---

## 1. 事实核查（逐条 pass/fail）

> 全部对照 `p3-review-compaction` worktree 内真实代码。✅=准确，⚠️=轻微偏差，❌=错误。

### 1.1 现状代码引用

| # | 设计引用 | 真实代码 | 判定 |
|---|---|---|---|
| F1 | `trimHistoryToTokenBudget`（`agent_execution.go:1187-1217`）「从尾部倒推、丢头部」 | `agent_execution.go:1187-1217` 确实从尾往头数 `for i := len(history)-1; i>=0; i--`，`start` 反推，返回 `history[start:]` | ✅ |
| F2 | 唯一调用点 `agent_execution.go:1088`（`prepareMessages` 内） | grep 确认：仅 `agent_execution.go:1088` 一处 | ✅ |
| F3 | 摘要 system 注入在 `prepareMessages` `1056-1067`，插 system 之后、history 之前 | `agent_execution.go:1053-1067`：类型断言探测 `GetSummary`，`append` system 摘要，位于 system（1046-1051）之后 | ✅ |
| F4 | 预算契约 `MaxBudgetTokens`（`agent/types/agent.go:72`）+ `RemainPromptTokens`（`:73`）在 `prepareMessages` 计算 budgetCap | `agent_execution.go:1069-1086`；`agent.go:72-73` 字段确认 | ✅ |
| F5 | `RoughTokensForMessage`（`agent/types/tokens.go:20-48`）不含 `Usage` 字段 | `tokens.go:20-48` 只估 `Content/Name/ToolCallID/Parts/ToolCalls` | ✅ |
| F6 | `repairLLMMessageToolOrdering`（`agent_execution.go:1108-1185`），非 tool 分支直接 append | `1108-1185`；`1124-1139` 非 tool/无 ToolCalls 的 assistant 原样 append | ✅ |
| F7 | 摘要注入（1056）早于 trim（1088），两条独立路径 | 顺序确认：`1056` 在 `1088` 之前，摘要不在 `history` 里 | ✅ |
| F8 | `InMemory.compress`（`memory.go:142-167`）删旧消息（156-158），`DeterministicCompact`（179-184） | `memory.go:154-158`（recent/older 切分）、`179-184`（`generateSummary` 调 `DeterministicCompact`） | ✅ |
| F9 | `SQLite.Compress`（`sqlite.go:287-313`）DELETE 旧行（304-306），summary 存 metadata `key='summary'`（311），`DeterministicCompact("", old)`（302） | `sqlite.go:287-313`：300（`old`）、302（`DeterministicCompact("", old, ...)`）、304-306（DELETE）、311（INSERT OR REPLACE metadata） | ✅ |
| F10 | `Hybrid` 零调用方 | `grep NewHybrid`：仅 `memory.go:251` 定义，无任何非测试调用 | ✅ |
| F11 | `Hybrid.GetMessages`（`memory.go:282-301`）头部注入 `append([]Message{summaryMsg}, messages...)` | `memory.go:292-298` | ✅ |
| F12 | `memoryAdapter.GetSummary` 转发（`dino/factory.go:170-172`）；InMemory/SQLite 都实现 `GetSummary`（`memory.go:111-115`、`sqlite.go:275-285`） | 全部确认 | ✅ |
| F13 | `langchain_memory.go:167-254`（`CompressMemory`）第三条压缩路径，summary 包 system 放头部（74-81） | `CompressMemory` 167-254；`GetMessages` 74-81 包 `Previous conversation summary:` system 消息 | ✅ |
| F14 | `applyCacheControl`（`anthropic_native.go:552-589`）预算式 ≤4 | 552-589 确认 | ✅ |
| F15 | `markHistoryBreakpoints`（`596-630`）从尾往头数 assistant | `596-630` 确认，`for i := len(msgs)-1; i>=0` | ✅ |
| F16 | `buildRequest` system 拼接（`425-433`），tool→user（`440-442`） | `425-433`、`441` 确认 | ✅ |
| F17 | `MaxAnthropicCacheBreakpoints=4`（`agent/types/prompt_cache.go:28`）；`mergeConsecutiveRoles`（`anthropic_native.go:655-677`） | `prompt_cache.go:26`、`anthropic_native.go:655-675` | ✅ |
| F18 | `Enabled=false` 时请求体字节级不变（`anthropic_native.go:531-536`） | 531-536 注释 + `534` 条件注入 | ✅ |
| F19 | `TestPrepareMessages_SummaryInjection`（`agent_test.go:441-471`）断言摘要 system 在 msgs[1] | `441-471`：`msgs[1].Role == "system"` + `Previous conversation summary:` | ✅ |
| F20 | dino `EnableCompress` 默认 true；`MaxBudgetTokens` 默认 0 | `dino/config.go:217`（`EnableCompress: true`）；`NewAgentConfig`（`agent.go:112-146`）未设 `MaxBudgetTokens`→零值 0 | ✅ |
| F21 | `agent/engine → dino` 依赖方向现状 | `grep` 确认 agent/engine、agent/llm、agent/types 均不 import `xichan96/cortex/dino` | ✅（**支撑 BLOCKER-1**） |
| F22 | §1.3 称摘要注入在 `1024-1103` 顺序列表 | `prepareMessages` 实为 `1024-1103`（行号吻合）；但设计 §1.3 编号括号内行号「1046-1051」「1056-1067」「1088」「1090」均准确 | ✅ |

### 1.2 文档引用（prompt-caching.md / next-round.md / codex 评估）

| # | 设计引用 | 真实内容 | 判定 |
|---|---|---|---|
| F23 | `prompt-caching.md` §1.6 记录 trim 破坏前缀、§2.2a「严格不超预算契约」、§2.3 compaction 记录、§5.3 配置优先级、§12.4 Step 4 遗留 | 全部核实：§1.6（`agent_execution.go:987-1017` 注：该文件为**旧行号**，当前 1187）、§2.2a（`§2.2(a)`）、§2.3（两条压缩路径）、§5.3（dino→agent→llm 优先级）、§12.4（`trimHistoryToTokenBudget` 与 summary 头部未动） | ✅（提示：prompt-caching.md 内旧行号 987-1017 与当前文件 1187 不符，属该文档自身的陈旧引用，非本设计错误） |
| F24 | `next-round.md` P3.1（61-65）「依赖：需先解决 DeterministicCompact→LLM 摘要或至少让 compact 不破坏前缀」 | `next-round.md:61-65` 确认 | ✅ |
| F25 | `optimization-review-vs-codex.md:66-84`（Codex 尾部原文 + LLM summary + 摘要最后 + `SUMMARY_PREFIX`） | `66-84` 确认 | ✅ |

**事实核查小结：25 处引用全部准确（含 1 处对外部文档陈旧行号的说明性提示）。设计的事实事前核查工作扎实，未发现臆造代码。**

---

## 2. 逻辑正确性（评审重点）

### 2.1 ❌ BLOCKER-1：§3.3 引入 `agent/engine → dino/chatstore` 反向依赖，与 §7.3 自相矛盾

- **证据**：设计 §3.3 伪代码 `buildTailSummary` 直接调 `chatstore.DeterministicCompact("", toChatstoreMessages(mid), chatstore.DefaultCompactConfig())`；同时 §7.3 写死「engine never imports dino」（为避免 `agent/engine → dino` 依赖方向问题）。
- **现状**：`agent/engine` 目前**不 import 任何 dino 包**（F21）。`dino/chatstore` 单向依赖 `agent/types`、`agent/providers`。若 engine 直接调 `DeterministicCompact`，`agent/engine → dino/chatstore` 成立——dino 是应用层，agent 是核心层，这会形成**核心层反向依赖应用层**的架构污染，并且使 P3.1 与 P3.5（`Hybrid` 默认化）在编译层面耦合。
- **修复（必须在落地前）**：`DeterministicCompact` 的调用**不在 engine 内部**，而是由**构造方注入**。即 `CompactionOptions.SummaryGenerator` 必须始终非 nil（在 dino/factory.go 构造时以闭包注入 `func(mid []types.Message) string { return chatstore.DeterministicCompact("", mid, chatstore.DefaultCompactConfig()) }`），engine 侧 `buildTailSummary` **只调用 `cfg.SummaryGenerator`**，`SummaryGenerator == nil` 时降级为「丢 mid，保 head+tail」。engine 不 import dino，依赖方向保持单向。§3.3 的「类型同构、直接转换切片」改为「`type Message = agenttypes.Message` 是**类型别名**，`[]types.Message` 与 `[]chatstore.Message` 同型，闭包内可直接传，无需转换」。
- **附带收益**：这样 `SummaryGenerator` 从「P3.4 可选 hook」变成「必有实现」，反而更接近 §7.3 的声明。

### 2.2 ❌ BLOCKER-2：§2.1「段3（尽力）：最近稳定 assistant 块跨 compaction 存活」在 Anthropic 缓存语义下不可实现

- **证据链**：
  1. Anthropic 缓存命中是**全前缀**语义：一个 breakpoint 缓存的是「从 messages[0] 到该 breakpoint」的**完整前缀**，不是「该块本身」。
  2. compaction 把 history 从 N 条缩为 `head + 1 条摘要 + tail`（KeepRecentCount=10 附近）。无论 head 是否锚定，**摘要替换了 mid**——段3 breakpoint 的前缀（system + tools + head + 摘要 + tail 到该 assistant）与 compaction 前（system + tools + 原 100 条 history）**必然不同**。
  3. 摘要内容 = `DeterministicCompact(existingSummary, mid)`，mid 随每次 turn 累积变化 → 摘要每次变 → head 之后的前缀在**跨 turn 也**不稳定（即使不 compaction）。段3 在 compaction 后的每一次新请求都是「冷建立」，不可能「跨 compaction 存活」。
- **后果**：§0 表格写「前 2 段」，§2.1 目标却列「段3（尽力）：跨 compaction 存活」。**两处矛盾**。若 I1 集成测试（§6.4）把段3 纳入验收断言，必失败。
- **修复**：把目标统一为「**段1（system）+ 段2（tools）跨 compaction 存活**」（这是本方案真实、正确、可验收的收益）；段3 重定义为「compaction 后新请求**冷建立**的覆盖范围（tail 原文使最近 assistant 可被锚定，MinCacheTokens 阈值达标）」——措辞从「跨 compaction 存活」改为「compaction 后新缓存的锚定基础」。I1 断言只测段1/2（CachedTokens > 0 且段1/2 前缀字节相等）。

> 注：段3 不可跨 compaction 存活**不是本方案的设计缺陷**——任何「压缩中间」的方案都必然使 history 段缓存失效。真正的目标就是段1/2。问题只在文档目标表述与验收口径，修正后不影响核心方案。

### 2.3 ⚠️ RECOMMENDED-1：Step 1 中间态存在「双摘要」副作用，不应作为可合入/可发布状态

- **证据**：Step 1 只改 trim 算法、不动 `1056-1067`（头部 system 摘要注入保留）。此时若 `CompactionPrefix=true` + budget 开启 + GetSummary 非空：
  - `prepareMessages` 注入头部 system 摘要（`1056-1067`，`Previous conversation summary:`）
  - trim 三段式又生成尾部 assistant 摘要（`buildTailSummary`）
  - → **同一请求出现两条内容不同的摘要**，且头部摘要不进预算、尾部摘要进预算 → 总请求 token 可能超预算。
- **修复**：Step 1 与 Step 2 合并为一次提交（§8 的「两步独立回滚」收益不足以抵消双摘要回归风险）；或 Step 1 明确标注为「开发中间态，不发布、不合主」。若坚持分离，至少 Step 1 的 T 用例必须加「预算 + CompactionPrefix + GetSummary 三开」场景，断言总预算不超。

### 2.4 ⚠️ RECOMMENDED-2：head/tail 边界上的 tool 配对破坏缺少测试覆盖

- **证据**：三段式把 `mid` 替换为摘要 assistant 后，可能产生三类配对缺口：
  1. head 末尾 `assistant(ToolCalls)` 的 tool_result 落在 mid 中 → 被替换 → repair 走 `if !complete` 分支剥离 ToolCalls（`1133-1179`），**安全**但需测试锁定。
  2. tail 开头是孤儿 tool（其 assistant 在 mid 中）→ repair `1125-1135` drop，**安全**。
  3. head 末尾 `assistant(ToolCalls)` 的 tool_result 恰好保留在 tail 开头 → repair 保留，**安全**。
- 设计 §3.5 已正确论证「repair 是过滤不是重排、无需改」，但 **T7（空/全 tool/全 user 边界）没有覆盖上述任一配对缺口场景**。修复：在 T7 增加「head 末尾带 ToolCalls 的 assistant + tail 开头 tool_result」与「tail 开头孤儿 tool」两个用例，断言 repair 输出合法且不 panic。
- **这是落地前必须补的测试**（repair 是 400 的最后防线，一旦三段式输出漏过 repair，Anthropic 侧 `tool_result 必须紧跟 tool_use` 会直接 400）。

### 2.5 ⚠️ RECOMMENDED-3：`SummaryGenerator` / `buildTailSummary` 签名三处不一致，且缺少 `existingSummary`

- **证据**：
  - §3.3 `buildTailSummary(cfg *CompactionOptions, mid []types.Message) string`
  - §4.4(a) 推荐「把已有的 `GetSummary` 内容也拼进摘要（`existingSummary`）」——但签名**没有该参数**
  - §7.3 `SummaryGenerator func(mid []types.Message) string`
- **后果**：若按 §4.4(a) 实现，确定性路径跨多次压缩的**累积摘要会丢**（`DeterministicCompact("", mid)` 每次从零重建，与 `InMemory.compress` 传 `m.summary` 的现状语义不符——`sqlite.go:302` 现状虽传 `""` 但那是 chatstore 内部行为，engine 侧新引入时应取更优语义）。且 P3.4 挂 LLM 摘要时通常也想要 existingSummary。
- **修复**：统一为 `SummaryGenerator func(existingSummary string, mid []types.Message) string`（engine 在 `prepareMessages` 先读 `GetSummary`，把结果传给 generator）。这同时让 §7.3 的 P3.4 接入点更完整。

### 2.6 ⚠️ RECOMMENDED-4：`CompactionOptions` 注入 engine 的机制未定义

- **证据**：§7.3「`AgentEngine` 增加字段 `compaction *CompactionOptions`；`NewAgentEngine` 默认 nil」——但 `NewAgentEngine`（`factory.go:568` 调用）签名未改，dino/factory 也没有 setter 说明。engine 的 `getConfig()` 只读 `AgentConfig`，`CompactionOptions` 是独立于 `AgentConfig` 的字段，`SetConfig`/`prepareMessages` 取不到（`getConfig` 返回 `*types.AgentConfig`）。
- **修复**：明确三种之一：(a) 扩展 `NewAgentEngine` 变参选项；(b) 新增 `SetCompactionOptions` setter（engine 现有 `SetConfig` 模式，`agent_execution.go:99-113`）；(c) 把 `CompactionPrefix`/`CacheAnchorTokens` 直接并入 `types.AgentConfig`（§5.1 已加字段），engine 只从 `getConfig()` 读，`SummaryGenerator` 单独用 setter 注入。**推荐 (c)**：字段走配置、函数走注入，最贴合现有模式，避免 `CompactionOptions` 与 `AgentConfig` 双源。

### 2.7 ⚠️ RECOMMENDED-5：`CacheAnchorTokens` 挤压尾部预算时，tail 可能无法提供段3 锚定基础

- **证据**：§3.1 step 4 `tail` 预算 = `rem - anchorTokens`。anchor 越大，tail 越短。若 `CacheAnchorTokens=2048`（建议值）而 `MaxBudgetTokens` 较小时，tail 可能只剩最后 1 条（T5 已承认）。此时最近 assistant 块可能不在 tail 中 → 段3 breakpoint 无锚或锚到摘要上（`markHistoryBreakpoints` 会锚到摘要 assistant 或 head 的 assistant）→ 段3 命中价值归零，且**摘要 assistant 被锚**意味着「摘要进缓存前缀」，摘要变化即失效。
- **修复**：文档补一句约束——`CacheAnchorTokens` 建议 ≤ `MaxBudgetTokens` 的 30%（或与 `KeepRecentCount` 的估算联动）；并在 §6 加一个「anchor 挤压致 tail 无 assistant」的 T 用例，断言此时段3 breakpoint 不落到摘要上（或接受并记录）。

### 2.8 ⚠️ RECOMMENDED-6：`SimpleMemoryProvider.GetMessages` 的头部 summary 注入是本方案遗漏的第三条头部注入路径

- **证据**：`InMemory` 内嵌 `SimpleMemoryProvider`，其 `GetMessages`（`langchain_memory.go:74-81`）在 summary 非空时**也会**在头部注入 `system` 摘要。但 `InMemory.summary` 字段是 chatstore 层单独维护的（`memory.go:65`），`SimpleMemoryProvider.summary` 是**另一个**字段——`InMemory.compress` 只写 `m.summary`，**不写** `SimpleMemoryProvider.summary`，所以走 `InMemory.GetMessages` 时 SimpleMemoryProvider.summary 恒为空，实际不注入。**设计 §1.4 引用 langchain_memory.go:74-81 是正确的**（针对 `CompressMemory` 路径）。
- 但 Step 4（langchain provider 直接路径）时，`SimpleMemoryProvider.GetMessages` 的头部注入与 `CompressMemory` 共用同一 `summary` 字段——届时两条注入路径都要处理，设计 §8 Step 4 已列入。**确认无遗漏，仅提示**。

### 2.9 已正确处理、无需改动的点（确认）

| 设计声明 | 核实结论 |
|---|---|
| §3.1「摘要替换 mid 而非追加，总 token 单调不增」 | ✅ 正确。head + 1 摘要 + tail ≤ head + mid + tail 当摘要 ≤ mid 时；降级链覆盖摘要 > mid 的异常 |
| §3.1 step 3「anchorIdx ≥ 1，强制至少一条头部消息」 | ✅ 关键：head≥1 保证摘要 assistant 不是 messages[0]，满足 Anthropic「首条必须 user」的硬约束（`buildRequest` 中摘要 role=assistant，若置首会 400）。设计已意识到，正确 |
| §3.4 降级链 | ✅ 自洽：任何分支要么 ≤ 预算，要么退化现状（现状已保证 ≤ 预算）。预算契约零破坏成立 |
| §3.5 repair 与摘要交互 | ✅ 摘要无 `ToolCalls`，repair `1124-1139` 原样保留；配对缺口由 repair 兜底（见 REC-2 需补测试） |
| §4.1「GetMessages 是存储接口，不该改顺序」 | ✅ 分层正确。摘要注入落在 engine 是正确归属 |
| §4.3 mergeConsecutiveRoles 兼容 | ✅ 摘要 assistant 与相邻历史合并不会 400；breakpoint 锚点后移但段1/2 不受影响；`SUMMARY_PREFIX` 缓解混淆是合理接受 |
| §5.3 默认关闭 | ✅ 合理。trim 输出结构变化对模型可见，保守默认 + 可回滚正确 |
| §7.1 与 P3.4 位置模型一致 | ✅ 摘要放最后与 `next-round.md:81` 方向一致 |
| §9 预算估算 CJK 偏差 | ✅ 诚实：契约在「估算空间」严格成立，真实超预算由 `MaxBudgetTokens` 保守余量吸收，与现状同源 |

---

## 3. 可行性

### 3.1 接口签名

- `CompactionOptions` 结构需按 **BLOCKER-1 + REC-3 + REC-4** 调整：`SummaryGenerator` 必填（构造注入）、签名带 `existingSummary`、注入机制选 (c)（字段进 `AgentConfig` + generator setter）。
- `types.Message` 与 `chatstore.Message` 是**类型别名**（`memory.go:15` `type Message = agenttypes.Message`），`[]types.Message` 与 `[]chatstore.Message` 同型，闭包内直接传即可——无 schema 问题，设计此点正确。

### 3.2 改动面（相对现状）

| 文件 | 改动量 | 评估 |
|---|---|---|
| `agent/engine/agent_execution.go` | 中：trim 三段式 + prepareMessages 摘要取用 | 核心，可控 |
| `agent/engine/compaction.go`（新） | 小 | 见签名修正 |
| `agent/types/agent.go` | 小：2 字段 | 零风险 |
| `dino/config.go` / `dino/factory.go` | 小：配置映射 + 注入 | 需 REC-4 定机制 |
| `dino/chatstore/memory.go` | 小：Hybrid.GetMessages 去头部注入 | 改动后 P3.5 兼容 |
| `agent/llm/anthropic_native.go` | **不改** | ✅ breakpoint 布局稳定 |
| `agent/providers/langchain_memory.go` | 不改（Step 4） | ✅ |

### 3.3 迁移顺序与回滚

- 建议 **Step 1+2 合并**（REC-1）；Step 3 独立；Step 4 后续。每步 `git revert` 成立（配置默认关 → 回滚无痕）。
- **回滚安全性**：`CompactionPrefix` 默认 false + T1 逐字节回归 → 现状用户零行为变化，回滚成本低。✅

### 3.4 测试清单评估

- T1-T7 纯函数：**覆盖充分**（除 REC-2 需补配对缺口用例）。T8 缓存前缀模拟**需要修正口径**（见 BLOCKER-2：只能模拟「段1/2 前缀相等」，不能模拟段3 跨 compaction 命中）。
- T9/T10 集成：需同步 REC-1（Step 合并后断言单摘要）。
- T11/T12 chatstore 集成：**价值有限**——它们断言的是**现状行为**（压缩后 `GetMessages` 返回 KeepRecentCount 条、`GetSummary` 非空），与 P3.1 新增功能无直接关系。建议改为断言「factory 配置映射正确 + prepareMessages 摘要尾部注入」（T13 已覆盖后者）。
- I1 真实 API：断言修正为段1/2（BLOCKER-2）。`//go:build integration` + 需 Anthropic key，作为**可选验收**而非 CI 门禁。

---

## 4. 风险

| 风险 | 等级 | 评估 |
|---|---|---|
| `agent/engine → dino` 反向依赖（BLOCKER-1） | **BLOCKER** | 架构污染 + 编译耦合，必须注入化 |
| 段3 目标不可兑现导致验收失败（BLOCKER-2） | **BLOCKER** | 文档/验收口径修正即可，不影响核心方案 |
| Step 1 双摘要 + 预算超限（REC-1） | 中 | 合并 Step 1+2 |
| tool 配对缺口漏过 repair → Anthropic 400（REC-2） | 中 | 补 T 用例；repair 是最后防线 |
| 摘要 assistant 与相邻历史合并，模型混淆（设计 §9） | 中 | `SUMMARY_PREFIX` + `tailStart` 选 user 边界，可接受 |
| `CacheAnchorTokens` 挤压尾部致段3 无锚 / 摘要被锚（REC-5） | 低-中 | 文档约束 + T 用例 |
| 预算估算 CJK 偏差（设计 §9） | 中 | 与现状同源，P3.4 复核时同步 |
| 与 P3.4/3.5 接口错位（REC-3） | 低 | 签名统一即可 |

---

## 5. BLOCKER / RECOMMENDED / OPTIONAL 汇总

### BLOCKER（必须先改再落地）

| # | 问题 | 证据 | 修复 |
|---|---|---|---|
| B1 | `buildTailSummary` 直接调 `chatstore.DeterministicCompact` 引入 `agent/engine → dino/chatstore` 反向依赖，与 §7.3「engine never imports dino」自相矛盾 | 设计 §3.3 伪代码 vs §7.3；F21 确认现状无此依赖 | `SummaryGenerator` 由 dino/factory 以闭包注入，engine 只调 `cfg.SummaryGenerator`；nil 时降级丢 mid |
| B2 | §2.1「段3 跨 compaction 存活」在 Anthropic 全前缀缓存语义下不可实现，与 §0「前 2 段」矛盾；I1 若按段3 验收必失败 | 缓存前缀语义 + 摘要替换 mid + 摘要随 turn 变化；设计 §0 vs §2.1 | 目标统一为段1/2；段3 改措辞为「compaction 后新缓存冷建立的锚定基础」；I1 断言只测段1/2 |

### RECOMMENDED（落地时采纳）

| # | 问题 | 修复 |
|---|---|---|
| R1 | Step 1 中间态双摘要 + 预算超限 | Step 1+2 合并为一次提交；或明确内部态不发布 |
| R2 | head/tail 边界 tool 配对缺口无测试 | T7 增加两个配对缺口用例，断言 repair 输出合法 |
| R3 | `SummaryGenerator`/`buildTailSummary` 签名三处不一致、缺 `existingSummary` | 统一为 `func(existingSummary string, mid []types.Message) string` |
| R4 | `CompactionOptions` 注入机制未定义 | 选 (c)：字段进 `AgentConfig` + generator 用 setter 注入 |
| R5 | `CacheAnchorTokens` 挤压尾部预算 | 文档约束建议值（≤30% 预算）+ 补「tail 无 assistant」T 用例 |
| R6 | T11/T12 断言现状行为、价值有限 | 改为断言配置映射 + 摘要尾部注入（或删除） |

### OPTIONAL

| # | 问题 |
|---|---|
| O1 | `history[0]` 非 user（异常会话）时 head[0]=assistant → Anthropic 首条非 user 400。现状同样存在（trim 后首条可能非 user），本方案未放大但 T7 可补一例 |
| O2 | 摘要消息的 `Usage` 字段：`RoughTokensForMessage` 不含 Usage，若未来摘要携带 Usage 不影响估算——无需处理，仅记录 |
| O3 | `tailStart` 选 user 边界的调优（设计 §8 提及）可作为后续微优化，非本期必须 |

---

## 6. 总体评价

**这是一个事实核查扎实、逻辑基本自洽、分层正确的设计文档**：25 处代码/文档引用全部准确，核心决策（摘要放尾部保段1/2、三段式保头压中保尾、摘要替换而非追加、默认关闭、纯 DeterministicCompact 独立落地）方向正确，且对 repair、mergeConsecutiveRoles、预算降级链、Anthropic 首条 user 约束等边界都有充分思考。

**两个 BLOCKER 均非方案本身缺陷，而是「架构依赖方向」和「验收口径」问题**：
- B1 是工程实现方式，改注入即可，不动核心算法；
- B2 是文档目标表述与 Anthropic 缓存语义的偏差，修正 §0/§2.1/§6.4 的措辞与断言即可，**核心收益（段1/2 跨 compaction 存活）真实成立且是本方案的正确价值主张**。

**结论：修掉 B1、B2（以及采纳 R1 合并 Step 1+2、R2 补配对测试、R3 统一签名）后可以进入实现**，从 Step 1（纯函数三段式 trim）开始；与 P3.4/P3.5 **不需串行**，可独立落地，但接口签名（R3）应在落地时一步到位，避免 P3.4 挂接时返工。

---

*评审完成。BLOCKER 2 个（B1 依赖方向、B2 目标口径），RECOMMENDED 6 个，OPTIONAL 3 个。核心结论：方案可行，收益正确（段1/2 跨 compaction 存活），修掉 2 个 BLOCKER 后可从 Step 1 落地，与 P3.4 不需串行。*
