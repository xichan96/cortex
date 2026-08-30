# 深度技术评审：长期记忆系统接线方案

> 评审对象：`docs/design/longterm-memory.md`（worktree `review-longterm-mem`）
> 评审依据：worktree 内真实代码（`dino/`、`pkg/memkit/`、`agent/`、`pkg/memkit/sqlite/`）+ 评估报告 `docs/optimization-review-vs-codex.md` 第十二章 + Codex 参照实现 `~/rust/codex/codex-rs/`
> 日期：2026-08-28 · 状态：**评审稿，未改动任何业务代码**

---

## 0. 结论摘要

**设计方向正确、零件核实基本准确、落地顺序合理，可以进入实现；但有 3 个 BLOCKER 必须在开工前解决。**

总体评价：这份设计把评估报告第十二章的"接线 + 渐进式披露 + 检索反馈"落实得相当完整，事实核查**绝大多数 pass**（仅 3 处点名：`tool.go` 的"9 个 action"列举、`BuildLayeredPrompt` 的排序键、Phase2 锁的 SQL 并发语义表述）。它正确识别出 `dino/mem` 是纯死代码、`GetSummary` 无人调用、`BuildSystemPrompt` 一刀切这三个现状问题。

但设计存在三个必须先修的事实性/逻辑性硬伤，否则实现会踩坑：

1. **【BLOCKER-1】`getGlobalUserID` 的"全局 user"前提不成立**——整个仓库没有任何代码往 `metadata` 表写 `user_id`（唯一写入点是 `summary` 键，`chatstore/sqlite.go:309`）。设计 §1.3/§7.3/风险表反复宣称"多 session 共享同一份记忆空间 / 按 user 全局"，但运行时 `getGlobalUserID` 永远回退到 `sessionID`。**当前实际语义是 per-session 记忆**，而设计的全局共享、跨 session 检索、`max_unused_days` 剪枝都建立在 per-user 之上。这直接影响：L1 注入取的是哪个 uid？`search_knowledge` 跨 session 搜不搜得到？需要实现时把 `uid` 语义显式锚定（要么接受 per-session + 说明迁移路径，要么在 `SaveContext`/`CreateSession` 写 `user_id` 键）。

2. **【BLOCKER-2】`BuildLayeredPrompt` L1 的"每 category 最高 priority 的 3-5 条"逻辑跑不通**——memkit 所有写入路径都写 `PriorityMedium`（`manager.go:294,329`），**没有生产者写入 High/Critical**，`priority` 字段天然全是 5。按 priority 排序取 top-N 退化为"按插入序取前 N"，随机性很大。建议改为 `updated_at DESC` + 每 category 限额，或引入 `usage_count` 预排序（自 Step 2 起）。

3. **【BLOCKER-3】引用反馈"返回即计数"会把 `search_knowledge` 读操作自身污染成自反馈环**——返回给模型 ≠ 模型用了。设计自己 §4.4 也承认是"退化版"，但默认把它放进了 Step 2（最小可用的一部分）。若 `usage_count` 参与排序（§4.2-3）和保留豁免（§7.3 `usage_count > 0 永不删`），"返回即计数"会让高频被搜但从不被用的条目永远霸榜、永不被剪枝。建议：默认 `RecordKnowledgeUse` **只在模型显式写回时**（`set_preference`/`add_knowledge` 引用某条）计数，或放到 `turn_observe` 扫描（设计已列为 Phase 2 增强，应升级为 Step 2 的默认实现而非进阶项），或者第一步先不加 `usage_count` 排序、只保留"引用豁免剪枝"。

**建议从 Step 1（接线最小可用）+ 修正 B1 的 uid 语义 开始**。Step 2 的"返回即计数"在 B3 未定前不要做；Step 3（`GetSummary` 注入）独立、收益明确、侵入极小，可穿插。

---

## 1. 事实核查结果（逐条）

| # | 设计声明 | 核实结果 | 证据 |
|---|---|---|---|
| F1 | `dino/mem/` 无任何包 import（死代码） | ✅ PASS | 全仓 grep `"dino/mem"`（排除 dino/mem 自身与 test）零命中；`MemoryTools`/`RunIngestLoop`/`IngestRuntime` 在 dino/mem 外零引用；`dino/README.md:62`、`dino/README-CN.md:62` 确实只提"可选 wiring"。`dino/mem/` 下无任何 `*_test.go` |
| F2 | ingest Phase-1 管线已实现：`ingest.go:27` `runIngestOnce`、`ingest.go:74` 4 worker | ✅ PASS | `runIngestOnce` 在 ingest.go:27；`ingestSessionWorkers = 4`（ingest.go:19），`for i := 0; i < ingestSessionWorkers; i++` 在 ingest.go:74。batch 查询在 :107-108；`minNew` 门在 :127-129；LLM 抽取 `extractKnowledgeItems` :363；落库 :177-213；cursor 写回 :214-216 |
| F3 | `loop.go:30` 裸 ticker | ✅ PASS | `ticker := time.NewTicker(interval)` 在 loop.go:30；<5s 钳制到 2m 在 :27-29；`enabled=false` continue 在 :38-39。`IngestLoopOptions`/`IngestRuntime` 在 types.go:32-38 ✅ |
| F4 | `tool.go:189` `sqliteMemoryTool.Execute` | ✅ PASS | `Execute` 定义在 tool.go:189 |
| F5 | "9 个 action"（清单） | ⚠️ **FAIL（点名）** | enum 实际是 **8 个 action**：`get_preference/list_preferences/search_knowledge/search_indexes/memory_stats/build_system_prompt/set_preference/add_knowledge`（tool.go:54-59）。设计 §0 表写"9 个 action"，同时自己的枚举（§2.1 表 + `time`）才凑到 9 个（把隐含的 `time` 也算进去）。§0 的"9 个"表述与 §2.1 自相矛盾（§2.1 说 8 个 action + time）。**点名**：§0 表行应改为"8 个 action（tool.go:54-59）+ 隐含 time 工具" |
| F6 | `SearchKnowledge` 子串匹配 `sqlite_knowledge.go:234-249` | ✅ PASS | `matchKnowledgeQuery` 在 :234-249，`strings.Contains` 遍历 Go 层过滤，`LIMIT` 在过滤后切分（:205-212），SQL 无子串过滤。排序 `priority DESC, updated_at DESC` 在 :136 |
| F7 | `BuildSystemPrompt` 无分层 `manager.go:432-493` | ✅ PASS | 函数在 :432-493：medium+ prefs 全量 + `PriorityHigh` top-10 知识（`Limit:10, Priority:&buildPromptPriorityHigh` :457-460），拼成 `User Context:`（:489）。**补充事实**：目前**没有任何写入方产生 High 优先级条目**（见 B2），所以 BuildSystemPrompt 的知识段实际上总为空（除非外部手动设 priority） |
| F8 | 短期记忆 = `factory.go:610-640` 构造 chatstore | ✅ PASS | `NewSQLite`/`NewInMemory` 分支在 :620-633；`agent.SetMemory(ctx, &memoryAdapter{provider: memProvider})` 在 :635。`memoryAdapter` 定义在 :79-211 |
| F9 | `Hybrid.GetSummary` `memory.go:303-307` | ✅ PASS | 在 :303-307 |
| F10 | `SQLite.GetSummary` `sqlite.go:273-283` | ✅ PASS | 在 :273-283 |
| F11 | `prepareMessages`（`agent_execution.go:840-903`）只读 `GetChatHistory` | ✅ PASS | :843-848 读 `ae.memory.GetChatHistory`；全文无 `GetSummary` 调用。system message 拼接在 :862-867 |
| F12 | `NewHybrid` 全仓零调用方 | ✅ PASS | grep 确认只有 `dino/chatstore/memory.go` 定义处 |
| F13 | `GetSummary` 引擎从不调用 | ✅ PASS | grep 全仓 `.GetSummary(` 仅 chatstore 内部实现 + `agent/types/llm.go` 注释提及，无引擎/工厂调用方 |
| F14 | 压缩后旧行被删只留窗口 + summary（`sqlite.go:285-311`） | ✅ PASS | `Compress` 在 :285-311：`DELETE FROM messages ... NOT IN (... LIMIT KeepRecentCount)` + `INSERT OR REPLACE ... 'summary'` |
| F15 | `DeterministicCompact` `compact.go:42-140` | ✅ PASS | `DeterministicCompact` 从 :42 到 :140。设计引用的"退化场景"描述与评估报告第三章一致 |
| F16 | `memdir.go` 零调用方 | ✅ PASS | `MemDirProvider`/`memdir` 在 dino/chatstore/memdir.go 外零引用 |
| F17 | `config.go:38-49` `MemoryConfig` 只有短期字段 | ✅ PASS | :38-49 确无长期记忆字段；`PersistEnabled` 默认 false 在 :201 |
| F18 | `agent/types.LLMProvider`（`Chat`/`ChatWithTools`） | ✅ PASS | llm.go:6-17 含 `Chat`/`ChatStream`/`ChatWithTools`/`ChatWithToolsStream` |
| F19 | `createLLMProvider` 已在 `NewDinoFactory` 构造 | ✅ PASS | `createLLMProvider` 在 config.go:266；`NewDinoFactory` 调用在 factory.go:390 |
| F20 | `openSharedSQLite` 全局 `sharedDBBy` map 单连接 | ✅ PASS | sqlite.go:29-33 `sharedDBBy = map[string]*sql.DB{}`；:71-102 复用逻辑 |
| F21 | `SharedSQLiteManager`（`shared.go:17-52`）同 DB 复用 | ✅ PASS | `sharedMemByDB` map :12-15；:32-52 同 key 复用，返回同一个 `memkit.Manager`。**补充**：`SharedSQLiteManager` 与 `openSharedSQLite` 两级都用进程级 map，Manager 内部持同一 `*sql.DB`，天然"同 DB 单 Manager"，设计 §1.4 说法成立 |
| F22 | 接线位置 `factory.go:468-471`（`for _, opt := range opts` 之前） | ✅ PASS | :468 `for _, opt := range opts`。**补充**：该处 `f.llmProvider`、`f.config` 都已就绪；但注意 `mcpManager` 构造在 :440-466（`if cfg.MCP.Enabled` 内），**:468 位于 mcpManager 之后**，与设计"mcpManager 之后、options 之前"一致。**顺序合理性点评见 §2.1** |
| F23 | `mcpManager` 构造顺序 | ✅ PASS | `NewMCPManager` + servers + tools 注册在 :440-466，:468 之前 |
| F24 | `Shutdown` 在 :768-805 | ✅ PASS | `func (f *dinoFactory) Shutdown` 在 :768；`mcpManager.Close()` 在 :790-792 |
| F25 | `agent.AddTools` 在 factory.go:608 | ✅ PASS | :608 |
| F26 | `SaveContext` 存 `Input: %s` 前缀（factory.go:126,128） | ✅ PASS | :126 和 :150 都是 `Content: fmt.Sprintf("Input: %s", ...)`；设计 §5.1 引用的 :126 正确 |
| F27 | `chatstore/sqlite.go:107-115` messages 建表 | ✅ PASS | :107-114（含 `tool_calls` 列），index 在 :115 |
| F28 | cursor 落 `memory_ingest_cursor`（`sqlite.go:160-163`） | ✅ PASS | :160-163；`IngestSetCursor` upsert 在 `ingest_meta.go:14-26`（设计 §11 引用的 `ingest_meta.go:20-26` ✅） |
| F29 | ingest 增量扫描 `session_id + id > lastID`（ingest.go:102-108） | ✅ PASS | :107 查询 `WHERE session_id = ? AND id > ? ORDER BY id ASC LIMIT ?` |
| F30 | `loop.go:47` 单次 runIngestOnce 错误被吸收 | ✅ PASS | :47 调用，无 panic/向上抛错 |
| F31 | `tool.go:296` 单一工具 `memory`（`newSQLiteMemoryTool`） | ✅ PASS | `MemoryTools` 在 :289，name 默认 "memory" :294-297，`newSQLiteMemoryTool` 在 :315；`writeTag` 默认 `memory_tool_write` :298-301 |
| F32 | `ingest.go:192` 用 tag `memory_ingest` 区分 | ✅ PASS | :192 `tags := append([]string{"memory_ingest"}, it.Tags...)` |
| F33 | `tool.go:230-237` `build_system_prompt` case | ✅ PASS | :230-237 |
| F34 | `get_preference` 精确 key（tool.go:204-211） | ✅ PASS | :210 `GetUserPreference(ctx, uid, category, key)` |
| F35 | `maxMemoryToolJSONBytes` tool.go:17 256KB | ✅ PASS | :17 |
| F36 | `SharedSQLiteManager` 内 `MemoryTools` 同实例复用（shared.go:32-52） | ✅ PASS | 同一 map key → 同一 Manager，不会开第二个 DB 连接 |
| F37 | wrap 复用 factory.go:550-590 permission/approval/loop | ✅ PASS | :550-567（registry tools）、:569-591（sessionToolsProvider）同一套 `evaluator`/`wrapWorkspacePathTools`/`NewApprovalTool`/`WrapLoopDetection` |
| F38 | `ingest.go:376` 已有 `END` 提示、`ingest.go:403` `ParseIngestItemsTab` 处理 `END` | ✅ PASS | :376 `If nothing to store, output exactly one line: END`；:403 `if s == "" || s == "END" { return nil, nil }` |
| F39 | 内容级去重 `sqlite_knowledge.go:29-43` | ✅ PASS | :29-43（normalized content 查重 + 仅合并 tags，**不覆盖 content**——设计 §3.2 的"内容级去重"表述准确） |
| F40 | `PageKeywordScore` `pkg/memkit/utils/page_score.go:5` | ✅ PASS | :5 定义；PageIndex 使用在 `sqlite_page_index.go:134` |
| F41 | `MemoryItem.ID` `typescore/types.go:42` | ✅ PASS | :42 |
| F42 | `MaxKnowledge` 默认 5000（manager.go:170） | ✅ PASS | manager.go:168 `MaxKnowledge: 5000`（设计写 :170，实为 :168，行号差 2，**微偏但不影响结论**） |
| F43 | `turn_observe.go` 已有 turn 观测钩子 | ✅ PASS | `dino/session/turn_observe.go` 存在，`ObserveOneUserTurn` 在 :48 |
| F44 | `manager.go:474` 检索内容截断 500 字符 | ✅ PASS | :474 `content[:500] + "..."` |
| F45 | `maxKnowledgeWriteRunes`/`maxPrefValueRunes` tool.go:252,260 | ✅ PASS | :252（pref value）、:260（knowledge write） |
| F46 | `getGlobalUserID` `ingest.go:253-261` | ✅ PASS | :253-261，读 `metadata ... key='user_id'`，无则回退 sessionID。**前提缺失见 B1** |
| F47 | Phase-1 多进程只重复读不破坏数据（ingest_meta.go upsert） | ✅ PASS | `IngestSetCursor` `ON CONFLICT DO UPDATE` 幂等；但**多进程共享 `sharedMemByDB` 进程级 map 不成立跨进程**——多进程各自开 Manager，写同一 DB 文件。SQLite 默认 journal 模式无 WAL（见 R4） |
| F48 | `InMemory.GetSummary` memory.go:111 | ✅ PASS | :111（设计 §6.2 引 memory.go:111 ✅） |
| F49 | codex `CONCURRENCY_LIMIT=8` | ✅ PASS | `memories/write/src/lib.rs:81` |
| F50 | codex Phase2 单全局锁 + 6h 冷却 | ✅ PASS | `try_claim_global_phase2_job` memories.rs:1076；`PHASE2_SUCCESS_COOLDOWN_SECONDS = 6*60*60` :21-22 |
| F51 | codex 引用反馈 `usage_count`/`last_usage` + 模型输出 citation | ✅ PASS | `stream_events_utils.rs:165-193`：从 assistant 最终输出 strip `<memory-citation>` → `record_stage1_output_usage`；`memory_citation.rs` 定义结构化 citation。设计 §4.4 的"cortex 无结构化引用标记"判断准确 |
| F52 | codex 渐进式披露 2.5k token | ✅ PASS | `ext/memories/src/lib.rs:16` `MEMORY_TOOL_DEVELOPER_INSTRUCTIONS_SUMMARY_TOKEN_LIMIT = 2_500`；`prompts.rs:25-48` 注入方式 = **developer 指令（PromptFragment）**，非合并 system |
| F53 | codex no-op 偏好 `stage_one_system.md:25-46` | ✅ PASS | :25-26 "No-op is allowed and preferred"（路径为 `memories/write/templates/memories/stage_one_system.md`） |
| F54 | codex 标记剥离 `phase1.rs:469-488` | ✅ PASS | :474-478 剥离 `<skill>`/`AGENTS.md` 标记片段 |
| F55 | codex max_unused_days（默认 30） | ✅ PASS | memories.rs:396-429、449-474 |

**事实核查小计：53 项核实，1 项点名 FAIL（F5），1 项行号微偏（F42），1 项前提缺失（F46/B1），其余全部 PASS。**

---

## 2. 设计正确性评估

### 2.1 接线取舍：dino live 子系统 vs 插件式 —— ✅ 合理，但接线位置有一个隐患

做成 factory 构造的 live 子系统是对的：`dino/mem` 深度依赖 `chatstore` + `memkit` + `agent/types.LLMProvider`，都是栈内类型，插件化无收益。`SharedSQLiteManager` 进程级单例也天然支持"一次构造、多 session 复用"。

**接线位置隐患**：设计把构造放在 `factory.go:468`（options 应用之前）。此时 `f.llmProvider`、`f.config` 已就绪，`logger.GetLogger()` 可调用——但 **`WithSessionTools`/`WithHooks` 等 option 还没应用**。设计声称"保证 WithSessionTools 仍能覆盖默认行为"——这是对的（因为 sessionToolsProvider 是 per-session 的，tools 注入在 `CreateSession` 时才做）。但 L1 prompt 注入在 `CreateSession` 时拼 `agentConfig.SystemMessage`（factory.go:492-496），如果用户 option 里**覆盖了 SystemPrompt**，L1 追加逻辑要放在 `CreateSession` 内部（拼完 `f.config.SystemPrompt` + skills 之后），而不是构造时。设计 §4.3 说"`CreateSession` 时计算一次"，方向对，但建议明确：L1 追加放在 factory.go:492-496 之后、`agentConfig.SystemMessage = systemPrompt` 赋值处（:496）合并。

**另一个顺序问题**：`NewLongTermMem` 若在构造时立即 `Start()`（:67），而 `Shutdown` 在 :768——中间任何 panic / 早期 return 都会让 goroutine 泄漏。建议 `Start()` 移到 factory 完全构造后（或 Shutdown 幂等已覆盖——设计有 `Stop` 幂等，OK）。

### 2.2 分层披露在 cortex 的注入机制怎么落地 —— ✅ 有具体注入点，但有 token 预算细节未敲定

- cortex 的注入点确实是 `CreateSession` 拼 `agentConfig.SystemMessage`（factory.go:492-496），`prepareMessages` 会把它作为第一条 system 消息（agent_execution.go:862-867）。**L1 追加到 SystemMessage 尾部可行**。
- **但注意**：`SystemMessage` 拼一次后存进 `agentConfig`，**不会随每 turn 重新计算**。`LongTermMem.BuildLayeredPrompt(ctx, uid)` 如果要在每 turn 反映最新记忆（用户刚 `set_preference` 完想马上生效），放 `CreateSession` 只算一次就**滞后**。需要决定：L1 是"session 级快照"（可接受，与 codex 的 memory_summary 每 thread 注入一次类似）还是"每 turn 刷新"（需改 `prepareMessages` 或 hook）。设计 §4.3 的表写"`CreateSession` 时计算一次"——与 codex"per thread 注入"对齐，**可接受但要在文档里说清楚这是 session 级快照**。
- **token 预算冲突**：L1 `PromptMaxTokens=2500` 硬顶是好的，但还要考虑：skills 注入（`BuildSystemPromptInjectionWithTriggers`）也在 SystemMessage 里追加；`MaxBudgetTokens`/`RemainPromptTokens` 会在 `prepareMessages` 里对 **history** 做 `trimHistoryToTokenBudget`（agent_execution.go:869-889），**system 消息不参与裁剪**——所以 L1 2.5k 是固定开销，不会挤掉 history 之外的东西，但会**挤占 total prompt budget**。若预算紧张，`RemainPromptTokens` 回退时 L1 仍占 2.5k。风险低（默认关），但要意识到 L1 是硬性 token 成本。
- **Anthropic 兼容性**：多 system 消息在 anthropic_native.go:352-361 会被拼接（`system += "\n" + m.Content`），所以 L1 追加到 SystemMessage 字符串而非单独消息，**对 Anthropic 无害**；对 OpenAI 也兼容（system 消息合并）。§6.2 的摘要注入（独立 system 消息）在 anthropic 上会被拼进同一个 system 字段——**顺序是"L1 记忆 → 摘要"还是反过来**，会影响模型优先级，设计未指定（OPTIONAL 级提示）。

### 2.3 模型驱动检索 + usage 反馈：cortex 有没有 citation 载体 —— ✅ 判断准确，但"返回即计数"有硬伤

- 设计正确判断：cortex 无结构化引用标记（对比 codex `<memory-citation>` 是模型输出流里 strip 出来 → `record_stage1_output_usage`，见 F51）。cortex 的 assistant 最终输出里没有任何记忆引用载体。
- **"返回即计数"是自反馈环**（B3）：`search_knowledge` 返回的条目被高频检索 ≠ 被模型实际使用。放在排序权重里会劣化。**证据**：`Execute` 的 `search_knowledge` case（tool.go:216-220）拿到 `items` 直接序列化返回，模型看不到 id 语义，计数没有任何"模型真实引用"的信号。
- **建议**：优先做 `turn_observe` 子串匹配（设计已列为 Phase 2 增强），因为它零协议成本、语义是"模型在最终回复里用了这段内容"。若嫌粗糙，更保守的默认是：**先不加 usage 排序，只把 `usage_count > 0` 作为剪枝豁免**（保留语义成立且无害），等精确反馈落地再开排序。这能拆掉 B3。

### 2.4 两阶段写入：裸 ticker vs 全局锁 —— ✅ 保留了 ticker + 新增 Phase2 锁，方向对；但有并发细节

- 设计保留 `RunIngestLoop` 裸 ticker 作为 Phase 1，新增 Phase 2 全局锁表 `memory_phase2_lock`。**方向正确**：Phase 1 靠 cursor 天然幂等，多进程只会重复读；Phase 2 需要互斥。
- **SQL 表述问题（点名）**：设计 §3.2 的认领 SQL 写 `INSERT ... ON CONFLICT DO UPDATE WHERE lease_until < now`——**SQLite 不支持 `ON CONFLICT ... WHERE` 子句**（那是 PG 语法）。SQLite 的 `ON CONFLICT DO UPDATE WHERE <expr>` 在 `UPDATE` 子句中**支持**（`ON CONFLICT(x) DO UPDATE SET ... WHERE <expr>`，该 WHERE 是 UPDATE 的过滤）。所以可行写法是 `INSERT INTO memory_phase2_lock (id, holder, lease_until, updated_at) VALUES (1, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET holder=excluded.holder, lease_until=excluded.lease_until, updated_at=excluded.updated_at WHERE lease_until < strftime(...)`。设计没写错到不可实现，但**直接把 PG 语义抄进文档会误导实现**——建议改文档里的 SQL 伪码并注明是 SQLite 方言。
- **Phase 2 与 Phase 1 在同一 ticker 内串行**（设计 :137）——OK，避免两全局流程打架。但 4 worker 的 Phase 1（并发） + 串行 Phase 2 顺序，如果 Phase 2 用同一个 Manager（同进程），与 Phase 1 并发写没冲突（SQLite 单连接串行化 + memkit 内容去重）。**多进程场景**：Phase 2 锁表在 SQLite 里，跨进程也能互斥（前提是 WAL/busy_timeout，见 R4）。设计风险表"多进程同时跑 ingest/Phase2"缓解措施正确。
- **6h 冷却对应 codex 的 `PHASE2_SUCCESS_COOLDOWN_SECONDS`**（F50）✅。但设计把冷却写进锁的 `lease_until`（持有者字段）——**冷却 ≠ 租约**：codex 的 6h 是"成功后 6h 内不再跑"，租约是"认领后最长持有时间"。设计把两者混成一个 `lease_until`。建议拆成两列：`lease_until`（租约，如 10min 心跳续期）+ `cooldown_until`（成功冷却 6h），否则一个慢的 Phase 2 会占住 6h 锁，别的实例 6h 内都进不来。

### 2.5 反噪声落到哪 —— ✅ 明确，缺一个"prompt 注入内容"的显式来源标记

- role 过滤（跳过 tool 消息）、`Input:` 前缀剥离、脚手架词表、`RedactSecrets` 双端——都落到 ingest 侧，环节明确（§5.1 的 1/2/3 条 + §5.2）。
- **缺一块**：codex 的标记剥离针对的是 **AGENTS.md / skills / external context 注入**（F54）。cortex 里 skills 注入发生在 system prompt（factory.go:494 `BuildSystemPromptInjectionWithTriggers`），**不会进 chatstore messages**，所以"背自己的脚手架"在 cortex 的主要载体其实是：`Input: {...}` 包装 + tool 输出。设计的覆盖是对的。但 skills 内容可能被模型复述进 assistant 消息——这条靠 `RedactSecrets` 兜不住，靠 `isValidMemoryItem` 的 trivial 过滤也拦不住"复述的 skill 指令"。建议（OPTIONAL）在抽取 prompt 里加一条"跳过你在 system 里见过的指令性内容"（类似 codex stage_one 的 no-op 引导）。

### 2.6 `GetSummary` 接线与长期记忆的分工 —— ✅ 不冲突，但 §6.2 的接口设计有个签名问题

- 分工表（§6.1）清晰：短期窗口 vs 长期事实，两链并存。
- **接口签名问题（点名）**：设计 §6.2 说在 `agent/types.MemoryProvider` 新增可选接口 `interface{ GetSummary(context.Context) (string, error) }`，并说"不动现有接口，用类型断言探测"。方向对。但 `chatstore.Provider` 接口**已经声明了 `GetSummary`**（memory.go:28），而 `memoryAdapter` 没有实现 `MemoryProvider.GetSummary`。**更自然的做法**：让 `memoryAdapter` 直接加一个 `GetSummary(ctx) (string, error)` 方法转发到 `m.provider.GetSummary`，engine 用类型断言 `ae.memory.(interface{ GetSummary(...) })` 探测——不需要新增独立接口类型。设计写成"新增可选接口"，实现时注意：**在 `MemoryProvider` 接口体上加方法会破坏其他实现**，正确做法是**只在 `memoryAdapter` 上加方法 + engine 断言**，设计 §6.2 的括号注解说对了，但 §10 改动文件清单写"agent/types/llm.go — 可选 GetSummary 接口"可能让人误以为改接口体。建议文档措辞改为"engine 内类型断言 + memoryAdapter 加方法，不改 MemoryProvider 接口体"。
- **`Hybrid` 是否参与**：设计说 `Hybrid` 才有意义，但 `NewHybrid` 现在零调用方——如果 chatstore 默认仍是 `NewSQLite` + `DeterministicCompact`，那 `GetSummary` 注入的摘要来自 SQLite 的确定性摘要（`metadata.summary`），**不是 LLM 摘要**。LLM 摘要需要把 chatstore provider 换成 `Hybrid`（属评估第三章，设计已排除）。设计 §6.2 在任务范围内接线 `GetSummary`，**效果是"确定性摘要进上下文"**，这本身有价值但不要宣称是 LLM 摘要。文档 :401 说"同时 DeterministicCompact 在摘要可用前仍是回退"——表述准确。

### 2.7 默认关 vs 自动连带开启

默认关合理（安全默认，LLM token 成本）。`PersistEnabled=false` 时长期记忆无从写起（`PersistDirectory` 默认 `./dino_sessions`，`openSharedSQLite` 会 `MkdirAll` 创建——即使不显式开启持久化，`NewSQLite` 也会建库）。设计 §7.1 说"不开持久化就无从长期"——**技术上限不完全成立**（即使 `PersistEnabled=false`，只要 `Memory.Type=="sqlite"` 或 persist 目录可达，库仍会建），但作为默认策略没问题。建议文档把"长期记忆依赖持久化"表述精确为"依赖 PersistDirectory 可写 + SQLite provider"。

---

## 3. 可行性 / 风险

### 3.1 ingest 用 LLM 抽取是否阻塞/污染用户 turn

- **不阻塞**：ticker 是独立 goroutine（loop.go:32-48），`runIngestOnce` 所有错误只 `log.Warn`，不碰用户 turn 路径。设计 §1.5 论证正确。
- **污染风险**：ingest 与用户 turn **并发写同一 knowledge 表**，靠 SQLite 单连接（`openSharedSQLite`）+ memkit 内容级去重兜底（F39）。**真实风险是锁竞争**：SQLite 默认（非 WAL）下，ingest 的写事务会与用户会话的写（`AddMessage`）争锁，极端情况 `database is locked`。当前 `openSharedSQLite` **没设 busy_timeout、没开 WAL**（见 R4）。这是**必须落地时补的**，否则多实例/高频下 `AddMessage` 偶发失败。
- **失败重试**：`extractKnowledgeItems` 失败（LLM 超时/限流）→ `runIngestOnce` log.Warn 返回，**cursor 不动**，下一 tick 重扫同一批。这天然是重试。但没有指数退避：LLM 连续失败时每 2m 打一次 Warn + 重试。可接受（日志噪声），OPTIONAL 加退避。

### 3.2 记忆污染（脏数据）清理

- 设计了 `max_unused_days=30` 剪枝（§7.3）+ `delete/forget` 靠工具覆盖写。但：
- **`knowledge` 表没有删除路径**：memkit 的 `SQLiteKnowledgeStore.Delete/Clear` 存在（sqlite_knowledge.go:251-259），但**没有暴露给模型或用户**——`sqliteMemoryTool` 的 action 列表里**没有 delete/forget**。设计 §11 说"delete/forget 操作留给用户（工具 set_preference 覆盖写）"——`set_preference` 能覆盖偏好，但**知识条目无法删除**（`add_knowledge` 只能新增/去重合并）。这是**实现缺口**：建议 Step 2 或 Step 6 加 `forget_knowledge` action（按 id/content 删除），否则污染知识只能等 30 天剪枝，且 `usage_count=0` 才剪。
- **Phase 2 LLM 合并默认关**：`Phase2LLMMerge=false`，则冲突消解基本不跑——`knowledge` 去重只做**内容级精确匹配**（normalized 后全等），语义重复（"用户用 Go" vs "用户主要语言是 Go"）会并存。可接受（默认关是为了省 token），但文档应明确"不开 LLM 合并时跨 session 语义重复不消解"。

### 3.3 默认开关与配置

默认关 ✅。配置字段齐全（§7.2），有默认值。**缺一个字段**：ingest 抽取用的 LLM 与主 LLM 是否同一 provider 的预算/并发隔离——设计只有 `UseSameLLMForIngest`。**共用同一个 LLM provider 意味着 ingest 的 token 消耗记入同一 budget**（`f.llmProvider` 无独立 quota），用户 turn 的 `RemainPromptTokens` 会被 ingest 消耗挤压（虽然 ingest 是 `Chat` 非流式，独立 goroutine）。风险中低（默认关 + minNew 门），但建议在 `LongTermMem` 构造时用独立 `newLLM`（可 clone provider）并**不进 factory budget**，与 `Budget.RecordTokens` 解耦。

### 3.4 兼容性 / 现有行为影响

- 默认关 → 现有行为零变化 ✅。
- Step 2 `ALTER TABLE knowledge ADD COLUMN usage_count` 是**不可回滚的 schema 变更**（SQLite 不支持 DROP COLUMN 直到 3.35，且 `ALTER ADD COLUMN` 加了 `NOT NULL DEFAULT 0` 会重写表）。文档 §8.1#6 说"ALTER 重跑不炸"——但 **`ALTER TABLE ADD COLUMN` 不是幂等的**，重跑会报 duplicate column。迁移要带 `PRAGMA table_info` 检查。这是测试清单里没覆盖的点（点名，OPTIONAL~RECOMMENDED 级）。
- §6.2 的 `GetSummary` 注入是纯增量，对现有 provider 零改动 ✅。

### 3.5 多租户

`getGlobalUserID` 现状 = sessionID（B1），设计风险表把"多租户隔离"列为"需 user 概念，独立评估"——诚实。但**必须先在 B1 定语义**，否则 Step 1 的"uid=sessionID"与 §1.3 的"多 session 共享记忆"自相矛盾。

---

## 4. BLOCKER / RECOMMENDED / OPTIONAL 分级清单

### BLOCKER（必须先改再落地）

| # | 问题 | 证据 | 建议 |
|---|---|---|---|
| B1 | **`getGlobalUserID` 的全局 user 前提不成立**：全仓无任何代码写 `metadata.user_id`，`getGlobalUserID` 永远回退 sessionID。设计 §1.3/§7.3/§11 反复宣称"多 session 共享记忆/按 user 全局"不成立；运行时是 **per-session 记忆**。影响 L1 注入 uid、跨 session 检索、`max_unused_days` 剪枝的语义 | `dino/mem/ingest.go:253-261`；全仓 grep 确认 `metadata` 表唯一写入是 `'summary'` 键（`chatstore/sqlite.go:309`），无 `user_id` 写入方 | 实现前锚定语义，二选一：(a) 接受 per-session，删掉/改写 §1.3"多 session 共享"表述，`uid=sessionID`；(b) 在 `SaveContext`/`CreateSession` 写 `user_id` 键（需设计多 session 归属策略）。**推荐 (a) 起步**，把"user 全局合并"列为后续独立任务——与 §12 待定点 5 一致 |
| B2 | **`BuildLayeredPrompt` L1 "每 category 最高 priority 的 3-5 条" 无意义**：所有写入路径都写 `PriorityMedium`，`priority` 全是 5，无 High/Critical 生产者 | `manager.go:294,329`（`Priority: PriorityMedium`）；`AddKnowledgeWithCategory`/`SetUserPreference` 都写 medium；`buildPromptPriorityHigh` 只在 BuildSystemPrompt 读取侧存在 | L1 排序改为 `updated_at DESC` + 每 category 限额；或引入 `usage_count` 预排序（自 Step 2 后可用）。至少要在文档里承认当前 priority 无区分度，不能用 priority 做 L1 选条 |
| B3 | **引用反馈"返回即计数"是自反馈环**：`search_knowledge` 返回 ≠ 模型使用，却计入 `usage_count` 参与排序 + 保留豁免，高频被搜条目将永不剪枝、永久霸榜 | `dino/mem/tool.go:216-220`（search_knowledge 直接返回 items）；设计 §4.4"返回即计数"；§4.2-3 排序权重；§7.3 `usage_count>0 永不删` | Step 2 默认改为 `turn_observe` 子串/标签匹配（设计已列为增强，升级为默认）；或先只做"引用豁免剪枝"、不加 usage 排序。文档若坚持"返回即计数"，应明确其语义为"被检索即热度"，并**不参与保留豁免** |

### RECOMMENDED（落地时采纳）

| # | 建议 | 证据 | 说明 |
|---|---|---|---|
| R1 | **SQLite 开 WAL + busy_timeout**：ingest 后台写与用户 turn 写并发，默认 journal 下会偶发 `database is locked` | `chatstore/sqlite.go:78` `sql.Open("sqlite3", dbPath)` 无 `_journal_mode=WAL&_busy_timeout`；全仓无 WAL 设置 | 在 `openSharedSQLite` 连接串加 pragma（`_journal_mode=WAL&_busy_timeout=5000`）。改动极小、收益大，建议放 Step 1 |
| R2 | **Phase2 锁 SQL 修正**：文档伪码 `ON CONFLICT DO UPDATE WHERE lease_until < now` 是 PG 语义，SQLite 不支持；且**租约与冷却被混成一个 `lease_until`** | 设计 §3.2 :241；codex `memories.rs:21-22`（6h cooldown 独立于 lease） | 文档改成 SQLite 方言（`ON CONFLICT(id) DO UPDATE SET ... WHERE lease_until < strftime(...)`），并拆成 `lease_until`（短租约，心跳续期）+ `cooldown_until`（6h 成功冷却）两列 |
| R3 | **`GetSummary` 接口落地方式澄清**：不要改 `MemoryProvider` 接口体；在 `memoryAdapter` 加方法 + engine 类型断言 | `agent/types/llm.go:107-122`（接口含 5 个必选方法）；`dino/factory.go:79`（memoryAdapter） | §10 改动清单里"agent/types/llm.go — 可选 GetSummary 接口"措辞易误导；实际应改 `dino/factory.go`（memoryAdapter 加方法）+ `agent/engine/agent_execution.go:840-903`（断言注入） |
| R4 | **`ALTER TABLE ADD COLUMN` 迁移非幂等**：`NOT NULL DEFAULT 0` 重跑报 duplicate column；测试 #6 说"ALTER 重跑不炸"不成立 | 设计 §8.1#6；SQLite `ALTER TABLE ADD COLUMN` 语义 | 迁移用 `PRAGMA table_info(knowledge)` 检查列存在再 ALTER；测试改为"无列则加、有列则跳过" |
| R5 | **ingest LLM 与主 LLM 解耦 budget**：`UseSameLLMForIngest=true` 时 ingest 消耗计入同一 provider，挤压用户 turn 预算 | `dino/config.go:266` `createLLMProvider`；`Budget.RecordTokens`（factory.go:346-348） | `LongTermMem.newLLM` 若复用 `f.llmProvider`，确认 ingest 调用不经过 `Budget.RecordTokens`；否则给 ingest 用独立 provider 实例 |
| R6 | **补充 `forget_knowledge`/`delete` 工具**：知识条目无删除路径，污染后只能等 30 天剪枝（且 `usage_count>0` 永删不了） | `sqliteKnowledgeStore.Delete` 存在但工具层无 action（tool.go:54-59 无 delete） | Step 6 或 Step 2 加 `forget_knowledge` action（按 id），与 §11 风险表"delete/forget 留给用户"对齐 |
| R7 | **L1 注入位置明确到行**：`CreateSession` 拼 SystemMessage 在 factory.go:492-496；L1 追加必须在该处与 skills 注入一起，且注明"session 级快照，不随 turn 刷新" | factory.go:492-496；`agentConfig.SystemMessage` 在 prepareMessages :862-867 直接使用，无每 turn 重算 | 若需每 turn 刷新，改 `prepareMessages` 或加 per-turn hook；否则文档写明快照语义 |

### OPTIONAL（可选）

| # | 建议 | 说明 |
|---|---|---|
| O1 | 抽取 prompt 加"跳过 system 指令性内容"引导（防模型复述 skills） | 补 §5 反噪声在 prompt 层的缺口 |
| O2 | ingest 失败指数退避（连续失败时拉长 ticker） | 降低日志噪声 |
| O3 | §0 表"9 个 action"改为 8 个 action + time（F5 点名） | 文档措辞 |
| O4 | L1 与 §6.2 摘要的**system 消息顺序**（L1 记忆 → 摘要）在 Anthropic 合并后会影响模型优先级，建议固定为"L1 在前、摘要在后" | anthropic_native.go:352-361 顺序拼接 |
| O5 | `search_indexes` 默认不暴露 OK，但若接 memdir 需单独设计（§12 已列） | 文档已覆盖 |

---

## 5. 覆盖度对照（评估报告 12.3）

| 12.3 落地要点 | 设计覆盖 | 评价 |
|---|---|---|
| 把 `dino/mem` + memory 工具接线 | ✅ Step 1（§1） | 完整，含 factory 构造/注入/Shutdown |
| 压缩摘要注入上下文（`GetSummary`） | ✅ Step 3（§6.2） | 有接口方案 + engine 注入点（R3 澄清） |
| `DeterministicCompact` → 尾部原文 + LLM 摘要 | ⚠️ **部分** | 只接了 `GetSummary` 注入；**LLM 摘要替换 `DeterministicCompact` 明确排除**（§6.3，"评估报告第三章独立任务"）。设计是诚实的，但 12.3 该要点**未在本任务覆盖**——需在结论中明示 |
| 记忆分层（总是加载小摘要 + 按需搜索） | ✅ Step 4（§4.3） | `BuildLayeredPrompt` L1 + 工具 L2/L3；B2 修正 priority 排序 |
| 模型驱动检索 + 引用反馈排序 | ✅ Step 2（§4.2-4.4） | 完整；B3 修正"返回即计数" |
| 最小信号门 + 注入内容剔除 | ✅ Step 5（§5） | 三层门 + role 过滤 + 前缀剥离 + 词表 + redact |
| 租约/认领 + 全局锁（替代裸 ticker） | ✅ Step 6（§3.2） | Phase 2 锁表 + 冷却；R2 修正 SQL 方言与租约/冷却拆分 |

**覆盖度结论**：12.3 的 7 项要点中 **6 项完整覆盖、1 项部分覆盖**（"尾部原文+LLM 摘要"只做了摘要注入半边，LLM 摘要替换留到评估第三章）。设计在 §6.3 明确声明了这一范围切割，无隐瞒——**覆盖度合格**。

---

## 6. 工作量与迁移顺序评价

- **Step 顺序合理**：1→2 构成最小可用（写/读/反馈），3-6 增强，每步独立 commit、可回滚（除 Step 2 的 schema 变更，R4）。首版"Step 1-2 即最小可用"成立。
- **工作量估计偏乐观但可接受**：Step 1 涉及 factory + config + 新 subsystem.go + tool.go 改 enum，估计中等偏大；Step 2 的 `SearchKnowledge` 改造 + 排序 + 迁移是一次较集中的 memkit 改动；Step 6 的 Phase 2 + 锁是最大的一块（新表 + 新流程 + 并发语义）。文档没给具体人日/行数估计，但改动文件清单（§10）完整，可据此排期。
- **回滚性**：除 `ALTER TABLE ADD COLUMN`（不可回滚，R4）外，其余纯增量。Step 2 建议单独一个 PR，方便需要时带着迁移一起 revert。

---

## 7. 总体评价

这是一份**高质量、可直接开工**的设计：现状问题识别准确、Codex 对照有据可查（F49-F55 全部核实）、分层披露和反噪声落到具体环节、测试清单覆盖了关键路径、范围切割诚实（§6.3）。

**先决条件（做任何实现前）**：
1. 定 `getGlobalUserID` 语义（B1）——per-session vs per-user，直接决定 L1 注入、检索范围、剪枝三个模块的实现。
2. 定引用反馈的默认实现（B3）——"返回即计数"必须降级或改 `turn_observe`，否则最小可用里就埋了排序劣化。
3. 修正 L1 排序键（B2）——priority 无区分度，换 `updated_at` 或 usage。

**推荐开工顺序**：先修 B1（文档层锚定语义，改 §1.3/§7.3/风险表）→ 从 **Step 1 接线最小可用**开始实现（同时顺手带 R1 的 WAL/busy_timeout，成本极低收益大）→ Step 2 先做迁移 + SQL 下推 + `RecordKnowledgeUse`（按 B3 定稿的默认），usage 排序可延后 → Step 3 `GetSummary` 注入穿插（独立、收益明确）→ Step 4-6。

**一句话**：方向对、零件准、可以动手；但 B1（uid 语义）/B2（priority 无区分）/B3（自反馈环）三个点要先在文档定稿里修正，且实现时务必给 SQLite 加 WAL + busy_timeout。

---

## 附录：评审范围与方法

- 被评文档：`docs/design/longterm-memory.md`（worktree `review-longterm-mem`，HEAD b33cdf5）
- 评估报告：主仓 `docs/optimization-review-vs-codex.md` 第十二章（:297-353）
- Codex 参照：`~/rust/codex/codex-rs/`（memories.rs / extension.rs / prompts.rs / stream_events_utils.rs / stage_one_system.md / phase1.rs / memory_citation.rs）
- 核查方式：逐条 grep + Read，53 项事实引用逐一对照 worktree 内真实代码；Codex 侧抽查关键常量与函数
- 评审纪律：只产出本报告，未改动任何业务代码
