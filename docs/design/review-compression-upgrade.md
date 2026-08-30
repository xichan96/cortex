# 评审报告：压缩系统升级设计方案（P3.4/P3.5）

> 评审对象：`docs/design/compression-upgrade.md`
> 评审日期：2026-08-29
> 评审方式：逐条对照 worktree 内真实代码（file:line 均已核实）
> 被评设计状态：**设计定稿，未动业务代码**（本次评审也不改代码/现有文档，仅新增本报告）

---

## 0. 结论摘要

| 项 | 结论 |
|---|---|
| **能否进入实现** | **需先解决 2 个 BLOCKER 后可以**。核心问题不在方向（「尾部原文 + LLM 摘要 + 摘要最后注入」模型正确、与 Codex 对齐、分层清晰），而在于**注入点自相矛盾会导致双重摘要注入**（步 1 即触发），以及改动清单一处自相矛盾（删 `estimateTokens` 与「sqlite.go 不改」冲突，编译不过）。 |
| **BLOCKER** | **2**：B1 双重摘要注入；B2 删 `estimateTokens` 破坏 sqlite.go 编译 |
| **RECOMMENDED** | **6**：R1–R6（LLM 摘要输入工具配对消毒、摘要调用独立超时、SQLite 摘要输入上限、§5.3 共享 config 耦合、SummaryLength 无观测通道、步 1 验收缺单注入断言） |
| **OPTIONAL** | **4**：O1–O4（子代理不受影响确认、CJK 语言配置、尾部预算可配置化、D6b 依赖注入点选型） |
| **事实核查** | 24 条引用中 **19 PASS / 1 PARTIAL / 4 FAIL**（FAIL 详见 FC-08/FC-27/FC-28/FC-30） |
| **工作量估计** | 中。改动集中在 `dino/chatstore` + `dino/factory.go`，engine 不动（D6b 除外），迁移 5 步每步可独立验证、可回滚。 |

**最关键的发现**：设计 D6a「engine 头部注入保持不动」与 §4.4「`Hybrid.GetMessages` 摘要移尾部」**并存**，而 engine 的 `GetSummary` 头部注入（`agent_execution.go:1056-1067`）读的正是 `Hybrid.GetSummary`（`factory.go:170-172` → `memory.go:303-307`）——两处输出同一份 `m.summary`。结果是摘要**同时**出现在 system 段（engine 注入）和 history 尾部（`GetMessages` 注入），且该双重注入在**步 1（P3.5 接线）就已发生**，不是步 4 才出现。设计的 §2 目标图只画了一个 summary，与自身 §4.4 的保留头部注入描述矛盾。必须在步 1 前定死单一注入源。

---

## 1. 事实核查（逐条 pass/fail）

> 行号均为 worktree 当前代码。`compact.go`/`memory.go`/`sqlite.go` = `dino/chatstore/*`。

| # | 设计引用 | 实测 | 判定 |
|---|---|---|---|
| FC-01 | `compact.go:42` `DeterministicCompact`，纯本地启发式 | `DeterministicCompact` 在 `compact.go:42-140`，无 LLM 调用 ✓；`DefaultCompactConfig`（MaxRecentRequests=3, MaxSummaryLines=100）在 `compact.go:17-22` ✓ | **PASS** |
| FC-02 | `memory.go:62-84` `InMemory` 为 `SimpleMemoryProvider` 包装；`generateSummary`（`memory.go:179-184`）→ `DeterministicCompact` | struct `memory.go:62-70`、`NewInMemory` `72-84` ✓；`generateSummary` `179-184` 调 `DeterministicCompact(existing, messages, DefaultCompactConfig())` ✓ | **PASS** |
| FC-03 | 压缩触发：`AddMessage` 计数超 `MemoryCompressThreshold` 后 `compressAsync`（`memory.go:98-102`） | `AddMessage` 在 `memory.go:86-105`，触发条件在 `98-102` ✓ | **PASS** |
| FC-04 | `SQLite`（`sqlite.go:20-27`）；`Compress`（`287-313`）`DeterministicCompact("", old, ...)` → 删旧行 → `metadata.summary`；`GetSummary`（`275-285`）从 metadata 读 | struct `sqlite.go:20-27` ✓；`Compress` `287-313`、第 302 行 `DeterministicCompact("", old, DefaultCompactConfig())`、`304-306` 删旧行、`311` 写 metadata ✓；`GetSummary` `275-285` 读 metadata ✓ | **PASS** |
| FC-05 | `Hybrid`（`memory.go:241-262`）已实现、**零调用方**；`NewHybrid(sessionID, provider, llmProvider, config)`；`Compress`（`309-359`）已有骨架：`KeepRecentCount` 尾部保留 + LLM 分支（`335-342`）+ `provider.Clear` + 尾部回写 | struct `241-249`、`NewHybrid` `251-262` ✓；`Compress` `309-359` ✓；LLM 分支 `334-342` ✓；`315-320` KeepRecentCount 保留、`326-332` 前次摘要前置、`348-357` Clear+回写 ✓。**零调用方确认**（全仓 grep 仅 `memory.go:251` 定义处）✓ | **PASS** |
| FC-06 | `chatstore.LLMProvider`（`memory.go:237-239`）`GenerateSummary(ctx, messages) (string, error)`；`types.LLMProvider` **无此方法** | `memory.go:237-239` ✓；`types.LLMProvider`（`agent/types/llm.go:6-18`）仅 Chat/ChatWithTools/GetModelName 等，**无** `GenerateSummary` ✓ | **PASS** |
| FC-07 | `NewHybrid` 签名 `(sessionID string, provider Provider, llmProvider LLMProvider, config *Config)`（`memory.go:251`）；缺省 config 兜底（`251-262`） | `memory.go:251` ✓；`252-254` nil config → `DefaultConfig()` ✓ | **PASS** |
| FC-08 | §3.3：「`compressCtx` 已有预算（`agent_execution.go:732-738`：默认 `Timeout`，下限 2min）」 | `732-738` 实测：`dt := config.Timeout; if dt <= 0 { dt = 10*time.Minute } else if dt < 2*time.Minute { dt = 2*time.Minute }`。**默认是 10 分钟（`Timeout<=0` 时），不是「默认 Timeout」**；仅下限是 2min | **FAIL**（误读代码；对 R2 有实际影响，见下） |
| FC-09 | §1.2 `factory.go:394-499` `NewDinoFactory`；`createLLMProvider`（`config.go:301-309`） | `NewDinoFactory` `394-500` ✓；`createLLMProvider` `config.go:301-309` ✓ | **PASS** |
| FC-10 | §1.2 `factory.go:651-676` `CreateSession`：`NewSQLite`（`Type=="sqlite"` 或 `PersistEnabled`）else `NewInMemory` | 内存段实际 `651-681`（`SetMemory` 在 676、logger 在 677-681）；条件 `f.config.Memory.Type == "sqlite" || f.config.Memory.PersistEnabled` 在 664 ✓。行号范围略截短（少 681 一行） | **PASS**（行号微差，无实质影响） |
| FC-11 | §1.2 `factory.go:170-172` `memoryAdapter.GetSummary` 转发 `provider.GetSummary`；`factory.go:197-199` `CompressMemory` 丢弃 `llm`/`maxMessages` | `GetSummary` `170-172` ✓；`CompressMemory` `197-199` `return m.provider.Compress(ctx)` ✓（确实丢弃两个参数） | **PASS** |
| FC-12 | §1.2/§6.1 `agent_execution.go:1056-1067` `prepareMessages` 类型断言 `GetSummary` → system 消息插 system 之后、history 之前 | `prepareMessages` 摘要注入 `1053-1067`：`1057` 类型断言、`1060` 非空判定、`1061-1064` 构造 system 消息，插在 `1046-1051`（system）之后、`1092-1094`（history）之前 ✓ | **PASS** |
| FC-13 | §1.3 `saveToMemoryAndMaybeCompress`（`652-749`）；`llm` 为 `ae.model`（`712`）；`CompressMemory` 调用（`740`） | `652-749` ✓；`712` `llm := ae.model` ✓；`740` `ae.memory.CompressMemory(compressCtx, llm, keepWindow)` ✓ | **PASS** |
| FC-14 | §1.4 `trimHistoryToTokenBudget`（`1187-1217`）；`used+c > rem` 即 break（`1205-1211`）；只作用于 history（`1088`） | `1187-1217` ✓；`1205-1211` 从尾向前、`if used+c > rem { break }` ✓；调用点 `1088` 在 history 上、`fixed` 不含 summary（summary 走 `GetSummary` 头部，不进 history）✓ | **PASS** |
| FC-15 | §1.5 三处 token 估算不一致：`compact.go:26-38`（非 ASCII `*0.5`）、`memory.go:411-422`（`*2`）、`agent/types/tokens.go:7-18`（`*2`） | `compact.go:36-37` `nonASCII := len(text)-asciiCount; return asciiCount/4 + nonASCII*2/4 + 1` → 非 ASCII **`*0.5`** ✓（注意：注释声称 `~2 tokens per char`，**代码与注释不符**，设计按代码 `*0.5` 表述正确）；`memory.go:421` 非 ASCII `*2` ✓；`tokens.go:17` `*2` ✓。三者确实互相矛盾 | **PASS** |
| FC-16 | §5.1 断言 `EstimateTokens` 统一后系数与 `agent/types.RoughTokenEstimate` 一致（`*2`） | 设计的替代实现 `asciiCount/4 + nonASCII*2 + 1` 与 `tokens.go:17`（`asciiCount/4 + nonAsciiCount*2`）一致，仅多 `+1`。方向正确 | **PASS** |
| FC-17 | §2/§4.4 `mergeConsecutiveRoles`（`anthropic_native.go:653`）会合并相邻 user | `anthropic_native.go:655-682`：相邻同 role 合并 ✓。尾部 summary(user) 紧跟 input(user) 时被合并、顺序保留 ✓ | **PASS** |
| FC-18 | §3.1 `f.llmProvider` 在 `NewDinoFactory` 就绪（`factory.go:399`）；`DinoFactory` 暴露 `GetLLMProvider()`（`factory.go:295`）；`memConfig` 即 `factory.go:652-659` 已构造的 `*chatstore.Config` | `399` `createLLMProvider(cfg)` ✓；`295` 接口方法 ✓；`652-659` `chatstore.Config` 字面量 ✓ | **PASS** |
| FC-19 | §3.2 `MemoryConfig`（`config.go:41-51`）加字段；`DefaultConfig`（`216-224`）置 true | `MemoryConfig` `41-52` ✓；`DefaultConfig.Memory` `216-226` ✓（`EnableCompress=true`、`CompressThreshold=50`、`KeepRecentCount=10`） | **PASS** |
| FC-20 | §3.2 hostconfig：`MemorySettings`（`types.go:25-30`）+ `coalesce.go:237-257` 合并 | `MemorySettings` `25-29` ✓（只有 MaxBudgetTokens/CompactAfterTurns/CompressThreshold 三项，无 EnableCompress 字段）；`coalesce.go` 内存段 `233-257`，`EnableCompress=true` 硬编码在 `237`，host 覆盖逻辑 `238-252` ✓。**注**：`MemorySettings` 现无 `EnableCompress`/`KeepRecentCount` 字段，设计的 `EnableLLMCompress` 合并需按 `237` 的硬编码默认 + host 显式覆盖模式新增 | **PASS** |
| FC-21 | §4.2 `generateBasicSummary`（`memory.go:391-409`）退役；`compact.go:43-45` messages 空时保留原 summary | `generateBasicSummary` `391-409`，调用点仅 `338/341`（Hybrid.Compress）✓，步 2 替换后无调用方可删 ✓；`compact.go:43-45` `if len(messages)==0 { return existingSummary }` ✓ | **PASS** |
| FC-22 | §4.4 现状 `Hybrid.GetMessages`（`memory.go:282-301`）把 summary 包成 system 插头部 | `282-301`：`292-297` 构造 `Role:"system"` summary 消息并 `297` 前插 `messages` ✓ | **PASS** |
| FC-23 | §4.4 `agent_execution.go:1100` 末尾追加 `input.ToMessage("user")` | `1100` ✓ | **PASS** |
| FC-24 | §5.3 `SQLite.Compress`（`287-313`）+ `compressAsync` 自触发（`sqlite.go:228-232`）会双触发 | `Compress` `287-313` ✓；`228-232` `AddMessage` 内 `go s.compressAsync(ctx)` ✓ | **PASS** |
| FC-25 | §6.3 `Hybrid.compressing` atomic（`memory.go:248`）；`memoryCompressMu`（`agent_execution.go:730`）；chatstore 自触发 `memory.go:272-276` | `248` ✓；`730` ✓；`Hybrid.AddMessage` 自触发 `270-277`（设计引 `272-276`，实际 `270-277`，含外层 `if EnableMemoryCompress`）✓ | **PASS** |
| FC-26 | 引用 `next-round.md:86-88`（P3.5 动机）、`prompt-caching.md:142`（尾部摘要记录在案）、`optimization-review-vs-codex.md:74-84`（Codex 做法） | `next-round.md:85-90` P3.5「Hybrid 零调用方、摘要不产出」✓（86-88 落在此段内）；`prompt-caching.md:142`「后续做 Codex 式 compaction 需把 summary 从头部 system 改为尾部 user」✓；`optimization-review-vs-codex.md:74-77` 尾部原文 + LLM summary、`SUMMARY_PREFIX`、`compact.rs:586-642/657-733` ✓ | **PASS** |
| FC-27 | §5.1「`memory.go:411-422` 的重复 `estimateTokens` 删除」**vs** §7 改动清单「`sqlite.go` **不改**（除非 A4）」 | **自相矛盾**：`estimateTokens` 是 chatstore 包内函数（`memory.go:411-422`），`sqlite.go:226`（`AddMessage` 的 `s.stats.TotalTokens += estimateTokens(msg.Content)`）与它同包引用。删掉即 `sqlite.go` 编译失败 | **FAIL** |
| FC-28 | D6a「engine 头部注入不动」+ §4.4「`GetMessages` 摘要移尾部」 | 见 §2「结论摘要」：两处注入同一份 `m.summary`。engine `1056-1067` 走 `memoryAdapter.GetSummary`（`factory.go:170-172`）→ `Hybrid.GetSummary`（`memory.go:303-307`）；`GetMessages` 尾部注入走 `m.summary`。**双注入**，且步 1 就触发（见 B1） | **FAIL** |
| FC-29 | §3.3 `ChatWithTools(ctx, msgs, nil)` 摘要不需要工具 | nil tools 可行（`agent/providers/langchain_llm.go:207-254` `convertToLangChainTools(nil)` 空切片；`anthropic_native.go` 对 tools 循环 nil 安全；OpenAI/DeepSeek/Volce 走 `LangChainLLMProvider`）。但 **`older` 内孤儿 tool 消息**未消毒（见 R1）：Anthropic 会把 `tool` role 映射为带 `tool_result` 的 user 块（`anthropic_native.go:437-451`），若切割点落在 assistant(tool_use) 与其 tool 结果之间，摘要调用 400 → 永久降级 fallback | **PARTIAL** |
| FC-30 | §8「步 1/2 不改变注入顺序（头部摘要）故可先合入 dev 冒烟」 | **有误**：步 1 接线 Hybrid 后，`GetChatHistory`（`factory.go:174-191` → `Hybrid.GetMessages`）立即开始**在 history 头部**携带 summary（`memory.go:292-297`），叠加 engine `GetSummary` 头部注入 → 步 1 即改变注入行为（出现双摘要）。并非「不改变注入顺序」 | **FAIL** |

---

## 2. BLOCKER（必须先改再落地）

### B1 — 双重摘要注入（步 1 即触发；设计与自身目标图矛盾）

**证据链**：
- engine 头部注入：`agent_execution.go:1056-1067` 类型断言 `GetSummary`，摘要非空即插 system 段；
- 该断言命中 `memoryAdapter.GetSummary`（`factory.go:170-172`）→ `Hybrid.GetSummary`（`memory.go:303-307`）返回 `m.summary`；
- `Hybrid.GetMessages`（步 1 仍为现状 `memory.go:282-301` 头部前插 system；步 4 改为 `memory.go` 尾部追加 `[Summary]`+summary）输出的是**同一份** `m.summary`；
- `prepareMessages` 的 `history` 来自 `GetChatHistory`（`agent_execution.go:1028`）→ `memoryAdapter.GetChatHistory`（`factory.go:174-191`）→ `provider.GetMessages(0)` → `Hybrid.GetMessages`。

**结果**：
- 步 1（P3.5 接线 + 首次压缩产出摘要后）：`[system, "Previous conversation summary:\nX", "X"(history 头部), ...]` —— X 出现两次；
- 步 4（摘要移尾部）：`[system, "Previous conversation summary:\nX", ...history, "X"[Summary](user), input]` —— X 仍出现两次，且 **§2 目标图（仅一个 summary 在尾部）与 §4.4 描述（保留头部注入）互相矛盾**。

**附带影响**：设计 §6.2/D6b 假设「尾部摘要会被 `trimHistoryToTokenBudget` 先裁掉」——但只要有头部注入，摘要永远不会完全丢失，D6b 的紧迫性被设计未意识到的头部兜底削弱了。这反过来证明**必须先定死单一注入源**，否则 D6b 的分析基准也是错的。

**修改建议（三选一，设计需显式选定）**：
1. **走完整 Codex 模型（推荐，与 §2 目标图一致）**：步 1 即禁用 engine 头部注入（改 `prepareMessages`，使 `Hybrid` 活跃时跳过 `GetSummary` 断言，或给 `memoryAdapter.GetSummary` 返回空）；摘要只由 `GetMessages` 尾部注入。步 1/2 的注入点改动要同步进行，不能「头部注入不动」。
2. **保持 engine 头部单注入**：`Hybrid.GetMessages` **不要**追加尾部摘要（步 4 取消）。放弃 §2 目标图，接受头部摘要对 tools breakpoint 前缓存段的影响（`prompt-caching.md:142` 已记录为降级）。
3. **双注入并存但内容不同**：头尾各注入一次（如头部放长摘要、尾部放 marker）——不建议，复杂且浪费 token。

> 无论选哪种，**步 1 验收必须加「单一摘要注入」断言**（见 R6）。

### B2 — 删 `estimateTokens` 与「sqlite.go 不改」自相矛盾 → 编译不过

`estimateTokens` 定义在 `memory.go:411-422`，`sqlite.go:226`（`AddMessage`）与 `sqlite.go` 同包引用。§5.1 说删除 `memory.go` 的 `estimateTokens`，§7 改动清单却写 `sqlite.go` **不改**。二者不能共存。

**修改建议**：改动清单把 `sqlite.go:226` 一并改为 `EstimateTokens`（或保留 `estimateTokens` 只改系数）。这是机械改动，但设计文档需自洽，否则实现者按清单「不改 sqlite.go」执行会踩编译错误。

---

## 3. RECOMMENDED（落地时采纳）

### R1 — LLM 摘要输入（`older`）需工具配对消毒
`Hybrid.Compress` 步 C 把 `older` 直接送 `GenerateSummary`。`older` 尾部可能是孤儿 `assistant(tool_use)`（其 tool 结果被切进 `tail`）或孤儿 `tool` 消息（其 tool_use 在更早的已摘要段）。Anthropic 对这类消息返回 400（`anthropic_native.go:437-451` 把 tool role 映射为 `tool_result` 块，需匹配的 `tool_use`），导致摘要调用失败 → 每次压缩都静默降级到 `DeterministicCompact`，LLM 摘要路径形同虚设。设计 §4.3 规则 4 只保证了 `tail` 配对完整，没管 `older`。
**建议**：`LLMSummaryAdapter.summarize` 内对输入过滤（丢弃 `Role=="tool"` 消息 + 剥离孤儿的 `ToolCalls`），或复用 `repairLLMMessageToolOrdering`（`agent_execution.go:1108-1185`）的处理逻辑；补一条单测（切割点落在 tool 配对中间）。

### R2 — 摘要调用应有独立短超时（设计误读 compressCtx 预算）
FC-08：`compressCtx` 默认是 **10 分钟**（`agent_execution.go:733-734`：`Timeout<=0 → 10*time.Minute`），不是「默认 Timeout」。一次挂死的摘要调用可占据 compress goroutine 达 10 分钟（`chatstore` 自触发路径下还持 `compressing` atomic 锁）。设计 §3.3「一次尝试失败即降级」的前提是超时足够短，现不成立。
**建议**：`LLMSummaryAdapter` 构造时接受或内部设置摘要专用超时（如 60–120s，独立 `context.WithTimeout`），与主循环/压缩预算解耦；文档中把 `732-738` 的语义写对。

### R3 — SQLite 全量历史作 LLM 输入未设上限
`SQLite.GetMessages(0)` 返回**全量**行（`sqlite.go:247-249` 无 LIMIT），而 `InMemory.GetMessages(0)` 有 `maxHistoryMessages`（默认 100）窗口（`langchain_memory.go:62-67`）。SQLite 下 `Hybrid.Compress` 的 `older` 可能是整个会话历史——长会话下摘要调用输入巨大，且不受 `MaxRecentTailTokens` 约束（该预算只作用于 `tail`）。engine 侧 `keepWindow`（`agent_execution.go:703-709`）已被 `memoryAdapter.CompressMemory` 丢弃。
**建议**：把 `maxMessages`（或 `keepWindow`）透传给 `Hybrid.Compress`，对 `older` 做条数/预算上限；或至少文档明确 SQLite 路径的输入边界。

### R4 — §5.3 共享 `memConfig` 的耦合需讲清（一步关死两层自触发）
`factory.go` §5.3 把 `memConfig.EnableMemoryCompress = false` 后传给 `NewHybrid`。`memConfig` 是**同一个指针**：`Hybrid.AddMessage` 的自触发（`memory.go:270`）和底层 `InMemory.AddMessage`/`SQLite.AddMessage` 的自触发（`memory.go:98`、`sqlite.go:228`）都读它 → **同时关死两层自触发**，压缩只剩 engine 路径（`saveToMemoryAndMaybeCompress`）。这与 §6.3/A5「保留 chatstore 自触发、阈值拉开」的选项一矛盾——实际落地的是 A5 选项二（engine-only）。
**建议**：设计明示「`EnableLLMCompress` 时压缩统一由 engine 触发（A5 选项二）」，并把 `Hybrid.autoCompress`（`memory.go:381-389`）标注为预留/可删；若想保留 Hybrid 自触发，需要独立字段控制两层。

### R5 — `Stats.SummaryLength` 观测通道不存在
设计在 §6.2/D6b 与 §9.3 反复以「可观测 `Stats.SummaryLength`」作为决策数据点，但 `GetStats` 全仓**无生产读点**（grep：`GetStats` 仅 queue/session 有消费，chatstore 的 `Stats` 无人调用），D6b 和「预算裁剪是否裁掉摘要」无从观测。
**建议**：在 `saveToMemoryAndMaybeCompress` 压缩成功后（`agent_execution.go:743`）或 `prepareMessages` 加一条 `SummaryLength`/注入次数日志（顺带可断言单注入，见 R6）。

### R6 — 步 1 验收缺「单一摘要注入」断言
步 1 的集成验收只有「`GetSummary` 非空、日志含 wrapper enabled」。在 B1 未修前此验收会「通过」而系统实际已双注入。补一条：`prepareMessages` 输出中 summary 文本（或 `[Summary]` marker）出现**恰好一次**；`GetMessages` 顺序断言为 `[system…, history…, summary, input]`（步 4 后）。

---

## 4. OPTIONAL（可选）

| # | 项 | 说明 |
|---|---|---|
| O1 | 子代理会话不受影响确认 | `NewInMemory`/`NewSQLite` 仅 `factory.go` 一处（dino 主会话）；子代理（SubagentManager）用独立 `engine.NewAgentEngine` + 自有/父记忆，不走 chatstore。设计可在 §7 加一句确认，避免读者误判覆盖面 |
| O2 | CJK 摘要语言（A6） | `summarySystemPrompt` 的「same language as conversation」是提示词约束，无强制；如实测 CJK 摘要语言漂移，再上 `SummaryLanguage` 配置 |
| O3 | 尾部预算可配置化（A3） | `MaxRecentTailTokens=20k` / `KeepRecentCount=10` 常量可后续 YAML 暴露；建议至少把 20k 提为 `Config` 字段，避免硬编码进 `compact.go` |
| O4 | D6b 依赖注入点选型 | 若走 B1 方案 1（尾部单注入），`trimHistoryToTokenBudget` 会先裁掉尾部摘要 → 需 D6b（summary 豁免裁剪）；若走方案 2（头部单注入），摘要不在 history 内、不被裁 → D6b 无需做。选型决定后 D6b 的取舍是确定的，不必「数据说话」 |

---

## 5. 总体评价

### 正确性
方向完全正确：用「尾部原文 + LLM 摘要 + 摘要最后注入」替换纯启发式（对齐 Codex），`DeterministicCompact` 保留作 fallback、三段降级链，`GetSummary` 头部注入现状不动——这些都对，且对现状（`Hybrid` 零调用方、`generateBasicSummary` 模板废话 fallback、三处 token 估算互相矛盾）的剖析**高度准确**。但注入点选型（D6a 保持头部 + §4.4 移尾部并存）是设计里唯一的结构性矛盾，且它让 §2 目标图、§6.2/D6b 分析、§8 步 1/2「不改变注入顺序」全部站不住——这不是局部瑕疵，是必须先行决断的设计轴心。另外对 `compressCtx` 超时语义的误读（FC-08）放大了摘要调用可占用的时间窗口。

### 可行性
接口层面极稳：`NewHybrid` 签名零改动、`NewLLMSummaryAdapter` 是纯新增文件、engine 注入点（除 B1 需定夺外）不动、`CompressMemory` 保持签名只改注释。迁移 5 步每步独立可验证、每步 commit 可回滚，符合纪律。**但**：改动清单的 `sqlite.go 不改` 与删 `estimateTokens` 冲突（B2）；步 1「不改变注入顺序」的预判错误（FC-30）意味着步 1 的风险等级评估偏低。

### 覆盖度
对评审重点 7 问中「事实核查」「Hybrid 默认化」「摘要替换」「尾部原文保留」「与 trim 分工」都答到了且质量高（§1 现状、§4、§6、§9 测试清单是全文档最强的部分）。短板集中在**注入选型未闭环**（双注入、D6b 依赖未定）和**可观测性落空**（R5）。

### 风险
上线风险主要两个：① 摘要双注入（信息重复、Anthropic system 段膨胀、缓存前缀被两份摘要污染）——B1 修复后消失；② LLM 摘要路径因孤儿 tool 消息永久降级（R1）——不修则 P3.4 的核心收益在工具密集会话里拿不到。兼容性上，`EnableLLMCompress` 默认 true + 独立开关 + hostconfig 覆盖，设计得当；对现有 per-session 存储（SQLite `metadata.summary`）无破坏（A4 的不持久化是已知取舍）。

### 工作量
中。主体集中在 `chatstore` 三个文件 + `factory.go` 一段 + 新 `llm_summary.go`；engine 除 B1 定夺外基本不动。预计实现 2–3 个工作日（含测试），测试清单已相当完整（约 14 项单测 + 4 项集成），补 R1/R6 两条即可覆盖本轮缺口。

### 结论
**设计总体高质量，事实准确度在同级设计文档中属上游**，但必须：
1. 先定死单一摘要注入源（B1，推荐方案 1「尾部单注入」与 §2 目标图自洽）；
2. 修掉改动清单的 `sqlite.go` 自相矛盾（B2）；
3. 落地时采纳 R1–R6。

修完 B1/B2 后即可从**步 1（P3.5 接线，含注入选型）**进入实现；步 1 的验证需同步带上「单一注入」断言，步 3/4 的行为变更按设计走独立 commit + 冒烟。

---

## 6. 评审结论摘要（提交用）

- BLOCKER：**2**（B1 双重摘要注入 / B2 删 estimateTokens 与 sqlite.go 冲突）
- RECOMMENDED：**6**（R1 摘要输入消毒 / R2 摘要超时 / R3 SQLite 输入上限 / R4 共享 config 耦合 / R5 观测缺失 / R6 单注入断言）
- 关键结论：方向正确、事实核查基本全部命中，但注入点选型（D6a 与 §4.4 并存）导致步 1 即双注入，需先定死单一注入源；修后可从步 1 进入实现。
