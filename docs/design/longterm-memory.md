# 长期记忆系统接线：落地方案设计

> 分支：`opt-longterm-mem`（worktree） · 状态：**设计稿，未改任何业务代码** · 日期：2026-08-28
> 目标：把已实现但完全未接线的 `dino/mem`（死代码）变成 live 能力，并借鉴 Codex memories 子系统的两阶段写入 / 渐进式披露 / 模型驱动检索 / 反噪声设计。

---

## 0. 现状核实（基于本仓库代码）

| 现状 | 证据 |
|---|---|
| `dino/mem/` 无任何包 import（死代码） | 全仓 grep 仅 `dino/README.md:62`、`dino/README-CN.md:62` 提及；无 `.go` 引用 `MemoryTools`/`RunIngestLoop`/`IngestRuntime` |
| ingest 管线已实现（Phase-1 风格） | `dino/mem/ingest.go:27` `runIngestOnce`，`ingest.go:74` 4 worker，`dino/mem/loop.go:30` 裸 ticker |
| 记忆工具已实现 | `dino/mem/tool.go:189` `sqliteMemoryTool.Execute`，9 个 action：`get_preference/list_preferences/search_knowledge/search_indexes/memory_stats/build_system_prompt/set_preference/add_knowledge` |
| memkit 全套 store | `pkg/memkit/interfaces.go`；SQLite 实现 `pkg/memkit/sqlite/sqlite.go:65-181`（preferences/knowledge/context/indexes/page_index/memory_ingest_* 表） |
| `SearchKnowledge` = 子串匹配 | `pkg/memkit/sqlite/sqlite_knowledge.go:234-249` `matchKnowledgeQuery`（`strings.Contains`，遍历全表，无 LIMIT 下推到 SQL、无打分排序） |
| `BuildSystemPrompt` 无分层 | `pkg/memkit/manager.go:432-493`：把「全部 medium+ prefs + top-10 priority 知识」塞成 `User Context:`，一刀切 |
| 短期记忆 = `dino/chatstore` | `dino/factory.go:610-640` 构造 `chatstore.NewSQLite`/`NewInMemory`，经 `memoryAdapter`（`factory.go:79-211`）挂到 `agent.SetMemory` |
| 压缩摘要不生效 | `chatstore/memory.go:303-307` `Hybrid.GetSummary`；`dino/chatstore/sqlite.go:273-283` `SQLite.GetSummary`。**引擎 `prepareMessages`（`agent/engine/agent_execution.go:840-903`）只读 `GetChatHistory`，从不读 `GetSummary`**；`Hybrid` 构造 `NewHybrid` 全仓零调用方。压缩后（`sqlite.go:285-311`）旧行被删，只留窗口 + metadata.summary |
| `DeterministicCompact` 启发式 | `chatstore/compact.go:42-140`；非英语/代码密集场景退化（只有工具名/文件路径/最近的 user 消息，无语义摘要） |
| `dino/chatstore/memdir.go` 零调用方 | MEMORY.md 风格索引，`MemDirProvider` 无消费者 |
| 配置无长期记忆字段 | `dino/config.go:38-49` `MemoryConfig` 只有短期字段 |
| LLM provider 抽象 | `agent/types.LLMProvider`（`Chat`/`ChatWithTools`）；`dino/factory.go:390` `createLLMProvider` 已在 `NewDinoFactory` 构造，可直接复用 |
| shared DB | `chatstore/sqlite.go:50-103` `openSharedSQLite` 全局 `sharedDBBy` map 保证单连接；memkit `SharedSQLiteManager`（`dino/mem/shared.go:17-52`）同 DB 复用 |

**一句话**：所有零件都在，只差三根线——① factory 构造并启动 ingest；② 记忆工具暴露给 session；③ 记忆摘要/记忆上下文注入 system prompt 与检索反馈。

---

## 1. 接线点：dino/mem 如何变成 live 子系统

### 1.1 总体形态

**做成 dino 的 live 子系统（factory 构造、session 复用），不做插件式**。理由：

- `dino/mem` 已深度依赖 `dino/chatstore` + `pkg/memkit` + `agent/types.LLMProvider`，它们都是 dino 栈内类型；插件化只会增加一层无收益的抽象。
- `dino/mem/shared.go:17` `SharedSQLiteManager` 已经用进程级 `sharedMemByDB` map 做了「同 DB 单 Manager」去重，天然适合 factory 一次性构造、多 session 共享。
- 对照 codex：memories 是核心扩展（`ext/memories`），但接线责任在宿主；cortex 的宿主就是 `NewDinoFactory`。

### 1.2 新增工厂内部字段与构造

```go
// dino/factory.go 内 dinoFactory struct 新增：
type dinoFactory struct {
    // ...现有字段
    longTermMem *mem.LongTermMem   // 新增：长期记忆子系统句柄（nil = 未启用）
    memIngestCancel context.CancelFunc // ingest loop 的取消钩子
}
```

新增包级构造入口 `dino/mem/subsystem.go`（新文件，见 §1.4），由 `NewDinoFactory` 在 `cfg` 校验后调用：

```go
func NewLongTermMem(ctx context.Context, cfg *Config, llm types.LLMProvider, log *slog.Logger) (*LongTermMem, error)
```

其中 `Config` 指 `dino.Config`（长期记忆段，见 §7）。`NewDinoFactory` 接线位置：`dino/factory.go:468-471`（`for _, opt := range opts` 之前）附近，即 **`mcpManager` 构造之后、options 应用之前**——保证 `WithSessionTools`/`WithHooks` 等自定义 option 仍能覆盖默认行为。

```go
// NewDinoFactory 内，loadBuiltinTools / mcpManager 之后：
if cfg.LongTermMemory.Enabled {
    ltm, err := mem.NewLongTermMem(ctx, cfg, llmProvider, logger.GetLogger())
    if err != nil {
        logger.Warn("[DinoFactory] long-term memory disabled", slog.String("error", err.Error()))
    } else {
        f.longTermMem = ltm
        f.memIngestCancel = ltm.Start() // 见 §1.5
    }
}
```

### 1.3 注入给谁

1. **共享 Manager → 记忆工具**：`LongTermMem.Manager()` 返回 `memkit.Manager`（`dino/mem/shared.go:43` 的 `SharedSQLiteManager` 产物）。每个 `CreateSession` 用 `mem.MemoryTools`（`dino/mem/tool.go:289`）构造**模型可见工具**，经 `sessionToolsProvider` 或直接 `agent.AddTools` 注入（见 §2）。
2. **共享 Manager → ingest**：ingest loop 复用同一 Manager 写库（`dino/mem/ingest.go:27-97` 已是该形态）。
3. **长期记忆上下文 → 会话 system prompt**：`LongTermMem.BuildLayeredPrompt(ctx, uid)`（新函数，见 §4.3），由 factory 在 `CreateSession` 拼 `agentConfig.SystemMessage` 时追加（见 §4.2）。

**关键设计**：`LongTermMem` 只持有「全局的 Manager + 配置 + LLM factory」，**不持有任何 session 状态**。session 粒度只体现为 `uid = sessionID`（`dino/mem/ingest.go:253-261` `getGlobalUserID` 已把 sessionID 提升为 userID）。多 session 共享同一份记忆空间，与 codex「记忆按 user 全局、跨 rollout 复用」对齐；这也意味着记忆工具天然无需 per-session 构造，一次构造、处处引用。

### 1.4 `dino/mem/subsystem.go` 职责（新文件）

```go
type LongTermMem struct {
    cfg      *dino.MemLongTermConfig // 长期记忆配置段（§7）
    mgr      memkit.Manager
    log      *slog.Logger
    newLLM   func(ctx context.Context) (types.LLMProvider, error) // ingest 用独立 provider（默认复用工厂 provider）
    mu       sync.Mutex        // Start/Stop 幂等
    cancel   context.CancelFunc
    wg       sync.WaitGroup
}

func (l *LongTermMem) Manager() memkit.Manager
func (l *LongTermMem) Start() context.CancelFunc   // 启动 ingest loop
func (l *LongTermMem) Stop()                        // Shutdown 时调用
func (l *LongTermMem) BuildLayeredPrompt(ctx context.Context, uid string) string  // §4.3
func (l *LongTermMem) IngestNow(ctx context.Context) error // 手动触发一次（内部/外部测试用）
```

Manager 构造直接复用 `mem.SharedSQLiteManager(cfg.Memory.PersistDirectory, cfg.Memory.PersistFileName)`（`dino/mem/shared.go:17`），**不加新的 Manager 状态**——它就是进程内单例，天然幂等。

### 1.5 运行时启动 ingest

**默认：ticker 启动 + 事件触发可选。** 保留 `RunIngestLoop`（`dino/mem/loop.go:9`）作为主力，理由：

- 它是纯后台循环，失败只 `log.Warn`，不会污染用户 turn（`loop.go:47` 单次 `runIngestOnce` 全部错误被吸收）。
- 已内置 `WaitReady`、`StartParams`、`TickParams` 抽象（`dino/mem/types.go:32-38`），无需重写。

具体接线：

```go
func (l *LongTermMem) Start() context.CancelFunc {
    ctx, cancel := context.WithCancel(context.Background())
    l.cancel = cancel
    l.wg.Add(1)
    go func() {
        defer l.wg.Done()
        mem.RunIngestLoop(ctx, mem.IngestLoopOptions{
            WaitReady: func(ctx context.Context) error { return nil },
            StartParams: func() (string, string, time.Duration, error) {
                return l.cfg.PersistDirectory, l.cfg.PersistFileName,
                    l.cfg.IngestInterval, nil
            },
            TickParams: func() (bool, int, int, mem.IngestRuntime) {
                return l.cfg.Enabled, l.cfg.IngestBatchMax,
                    l.cfg.IngestMinNew, l.runtime()
            },
            NewLLM: func(ctx context.Context, _ mem.IngestRuntime) (types.LLMProvider, error) {
                return l.newLLM(ctx)
            },
        })
    }()
    return cancel
}
```

**Phase-1 到 Phase-2 的升级**（对照 codex 两阶段，见 §3.2）放入同文件 `runPhase2Merge`，由 ticker 内先 Phase1 后 Phase2 顺序触发（同一 tick 内串行，避免两个全局流程打架）。

### 1.6 Shutdown 关闭

`dino/factory.go:768-805` `Shutdown` 内新增：

```go
if f.longTermMem != nil {
    f.longTermMem.Stop() // cancel ctx → loop.go:33 select 退出 → wg.Wait()
}
```

放在 `f.mcpManager.Close()` 之后，与现有子系统关闭顺序一致。

---

## 2. 记忆工具暴露：9 个操作的取舍与 schema

### 2.1 暴露策略

`dino/mem/tool.go` 的 9 个 action（`tool.go:54-59`）按「模型可见 vs 内部用」分类：

| action | 模型可见? | 理由 |
|---|---|---|
| `get_preference` | ✅ | 读取用户偏好，高频，无副作用 |
| `list_preferences` | ✅ | 一次性看全量偏好，语义清晰 |
| `search_knowledge` | ✅ | 长期记忆检索主力（§5 改造后） |
| `search_indexes` | ⚠️ 默认关 | 面向 README/MEMORY.md 文档索引；cortex 无注入文档管线，默认不进 tool list，配置 `expose_search_indexes: true` 才开 |
| `memory_stats` | ✅ | 低成本自省，模型可用于判断「记忆是否为空、是否值得搜」 |
| `build_system_prompt` | ❌ 内部用 | 由 factory 直接调用 `BuildLayeredPrompt` 注入 system prompt（§4）；若暴露给模型会导致模型自行刷 prompt、挤占上下文。**从模型可见 action 中移除**，改为内部方法。 |
| `set_preference` | ✅ | 模型主动记录偏好（仅当用户明确要求或明显持久事实） |
| `add_knowledge` | ✅ | 同上，记录知识 |
| （隐含 `time`） | ✅ | `tool.go:303-305` `NewTimeTool`，保留 |

> 注：`get_preference` 目前 `tool.go:204-211` 只支持精确 key 匹配（`GetUserPreference(ctx, uid, category, key)`）。`list_preferences` 全量返回可能超 `maxMemoryToolJSONBytes`（`tool.go:17` 256KB），模型自行承担截断风险；可接受。

### 2.2 工具名与数量

- **单一工具 `memory`**（`newSQLiteMemoryTool` 默认名，`tool.go:296`），action 字段分派。保持现状，不改名——已有 schema 字段（`tool.go:48-76`）足够，模型对 action-enum 工具更稳。
- 暴露时 `writeTag` 用 `memory_tool_write`（`tool.go:300`），ingest 写入用 `memory_ingest`（`ingest.go:192`）——**两条写入路径通过 tag 区分**，检索反馈排序时可分别对待（模型显式写入权重 > ingest 自动抽取）。

### 2.3 工具注册位置

`CreateSession` 内、`agent.AddTools(ctx, wrappedTools)`（`factory.go:608`）之前，追加：

```go
if f.longTermMem != nil {
    ltmTools := mem.MemoryTools(mem.MemoryToolOptions{
        SessionID:  sessionID,
        PersistDir: f.config.Memory.PersistDirectory,
        SQLiteFile: f.config.Memory.PersistFileName,
        Log:        logger.GetLogger(),
        ToolName:   f.config.LongTermMemory.ToolName, // 默认 "memory"
        Description: f.config.LongTermMemory.ToolDescription,
        WriteKnowledgeTag: f.config.LongTermMemory.WriteKnowledgeTag, // 默认 "memory_tool_write"
    })
    wrappedTools = append(wrappedTools, wrapMemoryTools(ltmTools, sessionID, ...)...) // 复用 approval/loop-detection wrap
}
```

注意 `MemoryTools` 内部走 `SharedSQLiteManager` 会与 `f.longTermMem.Manager()` 返回同一实例（`dino/mem/shared.go:32-52` 同 key 复用），**不会开第二个 DB 连接**。wrap 复用 `factory.go:550-590` 的 permission evaluator + `WrapLoopDetection` + approval 链路，把 `memory` 当成普通工具纳入 allow/deny/ask 体系（用户可在 `config.Tools.Allowed/Denied` 里控制，见 §7）。

### 2.4 `build_system_prompt` 的处置

`memkit.Manager.BuildSystemPrompt`（`manager.go:432`）保留为底层函数，但**不再暴露给模型**；factory 改用新的分层 prompt 构造（§4.3）。`dino/mem/tool.go` 中 `case "build_system_prompt":`（`tool.go:230-237`）从 enum 中删除，避免模型调用后拿到的「全部记忆塞入」与分层设计冲突。

---

## 3. 记忆写入：来源、时机、两阶段与最小信号门

### 3.1 来源与时机

- **来源**：`messages` 表（`chatstore/sqlite.go:107-115` 建表），字段 `id/session_id/role/content/timestamp/tool_calls`。ingest 已按 `session_id` + `id > lastID` 增量扫描（`ingest.go:102-108`），cursor 落 `memory_ingest_cursor` 表（`pkg/memkit/sqlite/sqlite.go:160-163`）。
- **时机**：ticker（默认 `2m`，`loop.go:30-47`）触发 `runIngestOnce`。**当前 ticker 是唯一启动源**；不做消息级实时触发——理由：LLM 抽取是昂贵调用，实时触发会与用户 turn 抢 token 预算；codex 同样是后台后台阶段轮询 + 租约，非实时。
- **批次**：`batchMax`（默认 50，`loop.go:42`）+ `minNew`（默认 2，`loop.go:44`）——`len(batch) < minNew` 直接跳过 LLM（`ingest.go:127-129`），这本身就是第一道最小信号门。

### 3.2 两阶段写入（Phase1 抽取 + Phase2 全局合并）

**对照 codex**（`state/src/runtime/memories.rs`）：
- Phase 1：每线程认领 stale 线程 → LLM 抽取 `{raw_memory, rollout_summary, rollout_slug}`（8 并发，租约 + ownership_token）。
- Phase 2：单全局锁（6h 冷却 + 心跳）→ git baseline diff 判断「是否有活」→ 受限内部 agent 更新 `MEMORY.md`/`skills/`/`memory_summary.md`。

**cortex 落地**：

- **Phase 1 = 现有 `dino/mem/ingest.go`**，保持 4 worker + per-session cursor。它已经产出「每条知识条目」——等价于 codex 的 `raw_memory`，只是写进 SQLite 而非文件。
- **Phase 2 = 新增「全局合并」**，解决两个现有缺口：
  1. **跨 session 事实冲突/重复**：memkit 已做了 `knowledge` 表内容级去重（`sqlite_knowledge.go:29-43`），但没有「冲突消解」；Phase 2 对同一 `(user_id, category)` 下相似条目做一次 LLM 合并（成本可控，因为只在 Phase 2 触发，且只挑 cursor 窗口内有新增的条目）。
  2. **`MEMORY.md` 分层索引**：把 memkit 的 `preferences` + `knowledge` 渲染成 codex 风格的 `memory_summary.md` + `MEMORY.md` 三层（见 §4）。

Phase 2 的并发保护（对照 codex 单全局锁）：

```go
// 新表（迁移加到 pkg/memkit/sqlite/sqlite.go migrate()）：
CREATE TABLE IF NOT EXISTS memory_phase2_lock (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    holder TEXT,
    lease_until DATETIME,
    updated_at DATETIME
)
```

```go
// 认领：INSERT ... ON CONFLICT DO UPDATE WHERE lease_until < now 或无持有者
// 语义：唯一行 id=1，幂等获取全局锁，6 小时冷却（对照 codex try_claim_global_phase2_job）
func tryClaimPhase2(ctx context.Context, db *sql.DB, holder string, lease time.Duration) (bool, error)
```

Phase 2 流程（新函数 `runPhase2Merge`，`dino/mem/subsystem.go`）：

1. `tryClaimPhase2` 拿全局锁（拿不到直接返回，等下一 tick）。
2. 收集 `memory_ingest_stats`（`sqlite.go:164-169`）中 `count` 增量 > 0 的 session。
3. 对每 session 用 `SearchKnowledge` 拉新增条目（按 `updated_at` 排序，tag 含 `memory_ingest`），做去重/合并（优先复用 `mergeTags` + 内容归一化，`sqlite_knowledge.go:67-102`）。
4. **LLM 冲突消解（可选，配置开关 `phase2.llm_merge`）**：同 category 且 `PageKeywordScore` > 0.6 的条目对，调 LLM 判断是否合并。
5. 写回 `knowledge`/`preferences`，更新 Phase 2 指纹（记录本次合并到的 `last_message_id` 水位，下次增量）。
6. 释放全局锁（`DELETE FROM memory_phase2_lock WHERE holder = ?`）。

**对比结论**：cortex 不需要 codex 的 git-baseline-diff（那是文件工作区特有的）；SQLite 有 cursor + updated_at，天然可判定增量。全局锁 + 冷却 + holder 足以防多进程/多 goroutine 并发合并。

### 3.3 最小信号门（no-op allowed）

`ingest.go` 已有三层门，**保留并在 prompt 层强化**：

1. **长度门**：`minNew`（`loop.go:44`）——批次消息太少直接不调 LLM。
2. **规则门**：`IngestRule`（`ingest.go:219-251`）——`count_only` action 只计数不落库（`ingest.go:157-165`）。
3. **内容门**：`isValidMemoryItem`（`ingest.go:263-298`）+ `containsTechSignal`——trivial 短语、过短内容被滤除。

**新增（对照 codex `stage_one_system.md:25-46` 的 no-op 偏好）**：在 `extractKnowledgeItems` 的 system prompt（`ingest.go:364-377`）中**显式写入 no-op 偏好**：

```
If nothing is durable or reusable (one-off queries, generic status updates,
temporary facts, common knowledge, no preferences/constraints worth persisting),
output exactly one line: END
```

（现有 prompt 已有 `If nothing to store, output exactly one line: END`，`ingest.go:376`；设计为：把这条提升为首要指令，并加 codex 风格的引导句「No-op is preferred over low-signal writes」。`ParseIngestItemsTab` 已处理 `END`，`ingest.go:403`。）

---

## 4. 记忆读取/检索与分层披露

### 4.1 检索现状与问题

`SearchKnowledge` 现状（`sqlite_knowledge.go:234-249` `matchKnowledgeQuery`）：
- 全表 `WHERE user_id = ?` 扫描后在 Go 里 `strings.Contains`，**LIMIT 只是取前 N**，无 SQL 层过滤，条目多时全查全过。
- 排序只有 `priority DESC, updated_at DESC`（`sqlite_knowledge.go:136`），query 命中度不打分。

### 4.2 检索改造方案（模型驱动检索 + 引用反馈，无需 embedding）

借鉴 codex「模型主动搜 + 引用反馈 → usage_count 排序」，落地为**三层**：

1. **SQL 层：query 关键词下推 + 多字段匹配**。把 `matchKnowledgeQuery` 改为 SQL：

```sql
SELECT ... FROM knowledge WHERE user_id = ?
  AND (LOWER(content) LIKE '%'||?||'%'
       OR LOWER(tags) LIKE '%'||?||'%'
       OR LOWER(category) LIKE '%'||?||'%')
  ORDER BY priority DESC, updated_at DESC
  LIMIT ?
```

> 注意：`LIKE '%...%'` 无法走索引，但 knowledge 表 `MaxKnowledge` 默认 5000（`manager.go:170`），量级可控；配合 `LIMIT` 下推，代价远小于现状「全表读 + Go 过滤」。保留 SQL 子串匹配为**基础召回**。

2. **排序层：加入 `PageKeywordScore`**（已有 `pkg/memkit/utils/page_score.go:5`，计算 query token 在 title+text 中的命中率）。`SearchKnowledge` 返回条目后按 `score` 排序（score 相同的按 `priority`、`updated_at`）。该函数在 PageIndex 已用（`sqlite_page_index.go:134`），纯计算无 IO，直接复用。

3. **反馈层：新增 `usage_count`/`last_usage` 列**（对照 codex `usage_count`/`last_usage` 自反馈环）。迁移：

```sql
ALTER TABLE knowledge ADD COLUMN usage_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE knowledge ADD COLUMN last_usage DATETIME;
```

当模型**真正引用**某条记忆时（下文 §4.4）→ `RecordKnowledgeUse(ctx, userID, id)` 自增。排序权重：`score DESC`（查询即时相关）→ `usage_count DESC, last_usage DESC`（历史被用程度）→ `priority DESC, updated_at DESC`。**不引入 embedding/向量库**，与 codex 一致。

### 4.3 分层披露（渐进式，替代 build_system_prompt 一刀切）

对照 codex 三层（`memory_summary.md` 总是加载 → `MEMORY.md` 按需搜 → rollout_summaries 被指向才开），cortex 映射：

| codex 层 | cortex 载体 | 注入时机 |
|---|---|---|
| L1 `memory_summary.md`（总是加载，≤2.5k token） | `BuildLayeredPrompt` L1：**所有 `preferences` + 每 category 最高 priority 的 3-5 条 knowledge 摘要** | **总是注入** system prompt 尾部（`CreateSession` 时计算一次，`agentConfig.SystemMessage += ...`） |
| L2 `MEMORY.md`（按需搜索） | L2：`search_knowledge` 工具 + `search_indexes`（可选） | **模型在 turn 内按需调用**，命中即进上下文 |
| L3 rollout_summaries（被指向才开） | L3：`get_preference`/`search_indexes` 精读单条 | 模型自行决定 |

新函数签名：

```go
// dino/mem/subsystem.go
func (l *LongTermMem) BuildLayeredPrompt(ctx context.Context, uid string) string {
    // L1：preferences（全量，截断到 maxL1Tokens）
    //   manager.GetUserPreferences → "- category.key: value"
    // L1：knowledge（按 category 分组，每 group 取 priority 最高的前 N，截断内容 300 字符）
    //   manager.SearchKnowledge(ctx, uid, "", maxItems) + 本地分组
    // 输出形如：
    //   # Long-term memory (auto)
    //   ## Preferences
    //   - user.name: 喜欢简体中文
    //   ## Knowledge
    //   - [project] 该项目用 Go + SQLite ...
    // 总长 ≤ cfg.LongTermMemory.PromptMaxTokens（默认 2500，对照 codex 2.5k）
}
```

**为什么这样**：`BuildSystemPrompt`（`manager.go:432-493`）的问题是把「全部 medium+ prefs + 10 条知识」无条件塞入；分层后 L1 只放**摘要级 + 稳定偏好**，detail 留给工具调用，天然对齐 codex 渐进式披露，token 预算固定。

### 4.4 引用反馈怎么落地

- `sqliteMemoryTool` 的 `search_knowledge` 返回 `MemoryItem` 时，`Execute` 结果里已带 `id`（`MemoryItem.ID`，`typescore/types.go:42`）。
- **hook 点**：记忆工具返回结果 → 模型在后续 assistant 消息里输出引用。当前实现没有结构化引用标记，最简单可靠的做法是**在 `search_knowledge` 内部按「返回了哪些 id」直接 `RecordKnowledgeUse` 计数**——即「被检索到且返回给模型 = 一次使用」。这是 codex 引用计数的退化版但零协议成本。
- 进阶（可后续）：在 `dino/session/turn_observe.go`（已有 turn 观测钩子）扫描 assistant 最终输出是否包含记忆内容的子串/标签，命中才计数，避免「搜了不用」也计数。**首版用「返回即计」**，把精确引用反馈列为 Phase 2 增强。

---

## 5. 反噪声设计

### 5.1 注入内容剔除（不背自己的脚手架）

**问题**：chatstore 里存的是「完整的会话消息」，其中会包含：
- **工具输出**（bash 输出、文件内容）——`messages` 表 `tool_calls` 列有工具名，`content` 是工具返回（`sqlite.go:211-218` 只存序列化后的 tool_persist）。
- **系统脚手架**：`SaveContext`（`factory.go:95-160`）会把 `Input: {...}` 包装 JSON 也存进 `messages`，`Content: "Input: %s"` 前缀（`factory.go:126`）就是特征。

**落地**（对照 codex `phase1.rs:469-488` 标记剥离）：
1. **role 过滤**：ingest 抽取时**跳过 `tool` role** 的消息内容——工具输出多数是执行细节，不是持久事实。保留 `assistant`/`user` 的正文。修改 `ingest.go:131-141` 拼 transcript 处，按 role 白名单过滤。
2. **前缀剥离**：`Content` 以 `Input: ` 开头的行（`factory.go:126,128` 产生）剥离该前缀后当 user 消息处理（保留语义、去掉包装）。
3. **注入标记**：在 `ChatContentFilter`/`isValidMemoryItem` 增加「脚手架词表」（`Input: `、`Tool Result:`、`<system>` 等），命中即跳过；`ingest.go:263-298` 处扩展。

### 5.2 密钥脱敏

- `extractKnowledgeItems` 的 system prompt 已有「do not store secrets (passwords, API keys, tokens)」（`ingest.go:377`）。
- **加一层确定性 redact**（对照 codex `phase1.rs:320-322` 前后都 redact）：新增 `pkg/memkit/utils/redact.go`（或 `dino/mem` 内）`RedactSecrets(s string) string`，用正则抓常见密钥形态：

```go
(?i)(api[_-]?key|token|password|secret|authorization|bearer)\s*[=:]\s*\S+   →  $1: [REDACTED_SECRET]
```

在**抽取前**对 transcript 做（`ingest.go:131` 拼串后、LLM 前）和**落库前**对 `it.Content` 做（`ingest.go:177-213`）双保险。

### 5.3 其他噪声门

- `isValidMemoryItem` 已过滤 trivial 短语（`ingest.go:281`）；保持。
- `add_knowledge`/`set_preference` 工具写入**不做 LLM 门**（模型显式写入是用户意图，直接落库），但 `maxKnowledgeWriteRunes`/`maxPrefValueRunes` 截断保留（`tool.go:252,260`）。
- 检索侧：`search_knowledge` 返回条目 `content` 截断到 500 字符（`manager.go:474` 已有类似处理，检索路径复用）。

---

## 6. 与短期记忆的关系

### 6.1 分工

| 维度 | 短期（chatstore） | 长期（memkit） |
|---|---|---|
| 范围 | 单 session 窗口 | 全局 user，跨 session |
| 内容 | 原文消息 + 压缩摘要 | 结构化偏好/知识条目 |
| 生命周期 | session 内 | 持久 |
| 检索 | 顺序窗口 | 关键词 + 反馈排序 |
| 写入 | 引擎自动 | 工具显式 + ingest 自动 |

**两条链并存，互不冲突**：chatstore 管「这轮对话上下文」，memkit 管「跨会话的持久事实」。`memory` 工具模型可见后，模型会在合适的 turn 主动读写长期记忆，短期窗口继续由 `DeterministicCompact`/`Hybrid` 管理。

### 6.2 `GetSummary` 接线（压缩摘要注入上下文）——**在本任务范围内**

当前引擎不读摘要（§0 已核实）。设计：

- `memoryAdapter`（`factory.go:79`）新增 `GetSummary(ctx)` 实现，转发到 `provider.GetSummary`（SQLite/InMemory 都已有实现，`sqlite.go:273`、`memory.go:111`）。
- `agent/types.MemoryProvider` 接口（`agent/types/llm.go:107-122`）**新增可选接口** `interface{ GetSummary(context.Context) (string, error) }`（不动现有接口，避免破坏 agent/providers 其余实现——用类型断言探测）。引擎 `prepareMessages`（`agent_execution.go:840-903`）在拼 `messages` 前，若 `ae.memory` 实现了该接口且摘要非空，则插入一条 `system: "Previous conversation summary:\n"+summary` 消息（放在 history 之前）。
- **效果**：压缩不再「丢信息」，摘要进上下文；`Hybrid` 才有意义（LLM 摘要路径，`memory.go:303-359`）。同时 `DeterministicCompact`（`compact.go:42-140`）在摘要可用前仍是回退。

> 注意：这是对 `agent` 包的最小侵入（一次类型断言 + 一个可选方法），不改变 `agent/providers/*` 既有实现。**放本任务 Step 3（§9）**，与长期记忆的 L1 注入（§4.3）互不干扰。

### 6.3 两套 memory 收敛

**不在本任务范围**。评估报告第五章（`docs/optimization-review-vs-codex.md:103-115`）提出的「收敛到 chatstore、标注 agent/providers deprecated、AutoMigrate 移到初始化」是独立工作；本设计只在 §6.2 引入可选 `GetSummary` 接口，不删除任何 provider。收敛列为后续任务，设计上预留了 `GetSummary` 可选接口，届时两套语义能平滑对齐。

---

## 7. 配置与开关

### 7.1 默认值

**默认关**。理由：LLM 抽取会消耗额外 token，且记忆可能污染尚未验证的多租户场景；用户显式开启符合「安全默认」。与 `PersistEnabled` 现状（`config.go:201` 默认 false）一致——**长期记忆依赖持久化**，不开持久化就无从长期。

### 7.2 `dino/config.go` 新增配置段

```go
// dino/config.go，MemoryConfig 之后新增：
type MemLongTermConfig struct {
    Enabled             bool          `yaml:"enabled"`                        // 总开关
    ToolName            string        `yaml:"tool_name"`                      // 默认 "memory"
    ToolDescription     string        `yaml:"tool_description"`               // 默认 defaultMemoryToolDescription
    WriteKnowledgeTag   string        `yaml:"write_knowledge_tag"`            // 默认 "memory_tool_write"
    ExposeSearchIndexes bool          `yaml:"expose_search_indexes"`          // 默认 false
    IngestInterval      time.Duration `yaml:"ingest_interval"`                // 默认 2m（≥5s，loop.go:27 钳制）
    IngestBatchMax      int           `yaml:"ingest_batch_max"`               // 默认 50
    IngestMinNew        int           `yaml:"ingest_min_new"`                 // 默认 2
    SystemExtra         string        `yaml:"system_extra"`                   // ingest 抽取 prompt 附加政策（IngestRuntime.SystemExtra）
    EnableContentFilter bool          `yaml:"enable_content_filter"`          // 默认 true
    PromptMaxTokens     int           `yaml:"prompt_max_tokens"`              // 默认 2500（对照 codex 2.5k）
    Phase2Merge         bool          `yaml:"phase2_merge"`                   // 全局合并，默认 true
    Phase2LLMMerge      bool          `yaml:"phase2_llm_merge"`               // LLM 冲突消解，默认 false（避免额外 token）
    MaxUnusedDays       int           `yaml:"max_unused_days"`                // 保留/遗忘，默认 30（§7.3）
    UseSameLLMForIngest bool          `yaml:"use_same_llm_for_ingest"`        // 默认 true
}

// Config struct 追加：
LongTermMemory MemLongTermConfig `yaml:"long_term_memory"`

// DefaultConfig() 追加：
LongTermMemory: MemLongTermConfig{
    Enabled:             false,
    ToolName:            "memory",
    WriteKnowledgeTag:   "memory_tool_write",
    ExposeSearchIndexes: false,
    IngestInterval:      2 * time.Minute,
    IngestBatchMax:      50,
    IngestMinNew:        2,
    EnableContentFilter: true,
    PromptMaxTokens:     2500,
    Phase2Merge:         true,
    Phase2LLMMerge:      false,
    MaxUnusedDays:       30,
    UseSameLLMForIngest: true,
},
```

### 7.3 保留/遗忘（对照 codex `max_unused_days`）

- Phase 2 合并时执行：`DELETE FROM knowledge WHERE usage_count = 0 AND last_usage IS NULL AND updated_at < now()-max_unused_days`。
- `preferences` 同理按 `updated_at` 剪枝（保留 `MaxPreferences` 上限，`manager.go:166`）。
- `usage_count > 0` 的条目**永不自动删除**（被引用即豁免），与 codex「使用即保留」一致。

---

## 8. 测试清单

### 8.1 单测

| # | 位置 | 用例 |
|---|---|---|
| 1 | `dino/mem/ingest_test.go`（新） | `ParseIngestItemsTab`：合法 tab 行、END、空串、多余列、非法 category 回退 project |
| 2 | 同上 | `isValidMemoryItem` + `containsTechSignal`：trivial 短语、短内容无 tech signal、URL/path/version 通过 |
| 3 | 同上 | `ingestRuleMatches`：字符范围、PhrasesAny、MaxUserMessages 组合 |
| 4 | 同上 | `RedactSecrets`：api_key/token/password 形态命中，普通文本不误伤 |
| 5 | `dino/mem/loop_test.go`（新） | `RunIngestLoop`：interval 钳制（<5s→2m）、ticker 触发、`enabled=false` 跳过 |
| 6 | `pkg/memkit/sqlite/` | 新增列迁移幂等（`usage_count`/`last_usage`/`memory_phase2_lock` 存在且 `ALTER` 重跑不炸） |
| 7 | `pkg/memkit/sqlite/sqlite_knowledge_test.go` | `RecordKnowledgeUse` 自增 + `last_usage` 更新；`SearchKnowledge` SQL 下推后 LIMIT 生效 |
| 8 | `pkg/memkit/sqlite/` | `tryClaimPhase2`：未持锁可拿、持有中不可拿、lease 过期可重拿、释放后可重拿 |
| 9 | `dino/mem/subsystem_test.go`（新） | `BuildLayeredPrompt` token 预算上限、空记忆返回空、分类分组正确 |
| 10 | `dino/factory`（新增） | `NewDinoFactory` 在 `Enabled=true` 时构造 `longTermMem` 且 `Shutdown` 干净退出 |

### 8.2 集成测试

| # | 用例 | 验证点 |
|---|---|---|
| I1 | 开启长期记忆 → 会话写入多条 user/assistant 消息 → ticker 触发（或 `IngestNow`）→ 查 `knowledge` 表 | ingest 落库、cursor 推进、`memory_ingest` tag 存在 |
| I2 | 模型在会话中调用 `memory` 工具 `add_knowledge`/`search_knowledge` | 工具可用、写入/读取走通、permission 包装生效 |
| I3 | `search_knowledge` 返回后 `usage_count` 递增 | 引用反馈闭环 |
| I4 | 开启 `EnableMemoryCompress` + 装配 `GetSummary` 可选接口 → 压缩后 `prepareMessages` 摘要入上下文 | §6.2 生效、历史不丢 |
| I5 | 多进程/多 ticker 同时跑 Phase 2 | 全局锁生效，只有 1 个 merge 执行 |

### 8.3 反噪声验证

| # | 用例 |
|---|---|
| N1 | transcript 含 `Input: {...}` 包装行 → 抽取内容不含 `Input:` 前缀 |
| N2 | transcript 含 `sk-xxx` / `password: yyy` → 落库为 `[REDACTED_SECRET]` |
| N3 | 纯寒暄会话（ok/thanks/谢谢）→ `END`，`knowledge` 无新增 |

---

## 9. 迁移顺序（最小可用 → 完整）

每步独立可验证、可回滚、可单独上线。

| Step | 内容 | 验证 | 涉及文件 |
|---|---|---|---|
| **1. 接线最小可用** | factory 构造 `LongTermMem` + `MemoryTools` 暴露 `memory` 工具 + ticker 启动 ingest（仅 Phase 1，保留现有 4-worker + cursor） | I1、I2；`go vet`、单测 #1-3 | 新增 `dino/mem/subsystem.go`；改 `dino/factory.go`、`dino/config.go`、`dino/mem/tool.go`（移除 `build_system_prompt` action） |
| **2. 检索反馈排序** | `usage_count`/`last_usage` 迁移 + `SearchKnowledge` 排序改造（score + 反馈）+ `RecordKnowledgeUse` + `search_knowledge` 返回即计数 | I3、单测 #6-7 | `pkg/memkit/sqlite/sqlite_knowledge.go`、`pkg/memkit/sqlite/sqlite.go`（migrate）、`dino/mem/tool.go` |
| **3. 摘要入上下文** | `GetSummary` 可选接口 + engine `prepareMessages` 注入摘要 + `memoryAdapter.GetSummary` | I4 | `agent/types/llm.go`、`agent/engine/agent_execution.go`、`dino/factory.go`（memoryAdapter） |
| **4. 分层披露** | `BuildLayeredPrompt` L1 注入 `CreateSession` system prompt；`build_system_prompt` 彻底移出模型可见 | 单测 #9；手测 system prompt 无记忆时为空 | `dino/mem/subsystem.go`、`dino/factory.go` |
| **5. 反噪声** | role 过滤 + `Input:` 前缀剥离 + `RedactSecrets` 双端 redact | N1-N3、单测 #4 | `dino/mem/ingest.go`、新增 `pkg/memkit/utils/redact.go` |
| **6. Phase 2 全局合并 + 保留/遗忘** | `memory_phase2_lock` + `runPhase2Merge` + `max_unused_days` 剪枝 | 单测 #8；I5 | `dino/mem/subsystem.go`、`pkg/memkit/sqlite/sqlite.go` |

> Step 1-2 即构成「最小可用」：长期记忆可写、可读、有反馈。Step 3-6 是增强。每个 Step 独立 commit（Conventional Commits，`feat(mem): ...`），不 push。

---

## 10. 改动文件清单（汇总）

### 新增
- `dino/mem/subsystem.go` — `LongTermMem` 子系统：构造/Start/Stop、`BuildLayeredPrompt`、`runPhase2Merge`、`tryClaimPhase2`
- `pkg/memkit/utils/redact.go` — `RedactSecrets`
- 测试：`dino/mem/ingest_test.go`、`dino/mem/loop_test.go`、`dino/mem/subsystem_test.go`、`pkg/memkit/sqlite/mem_longterm_test.go`

### 修改
- `dino/config.go` — `MemLongTermConfig` 段 + `Config.LongTermMemory` + 默认值
- `dino/factory.go` — 构造 `longTermMem`、Start/Shutdown、`CreateSession` 注入记忆工具与 L1 prompt、`memoryAdapter.GetSummary`
- `dino/mem/tool.go` — 移除 `build_system_prompt` action；`search_knowledge` 返回即计数
- `dino/mem/ingest.go` — no-op prompt 强化、role 过滤、`Input:` 剥离、双端 redact
- `pkg/memkit/sqlite/sqlite.go` — migrate 增加 `usage_count`/`last_usage`/`memory_phase2_lock`（幂等）
- `pkg/memkit/sqlite/sqlite_knowledge.go` — SQL 下推 + score/feedback 排序 + `RecordKnowledgeUse`
- `agent/types/llm.go` — 可选 `GetSummary` 接口（类型断言，不破坏既有实现）
- `agent/engine/agent_execution.go` — `prepareMessages` 摘要注入
- `docs/design/longterm-memory.md`（本文档）

### 不动
- `pkg/memkit/manager.go`（`BuildSystemPrompt` 保留为底层，不移除）
- `agent/providers/*`（评估第五章收敛不在本任务）
- `dino/chatstore/memdir.go`（仍零调用方，独立评估）

---

## 11. 风险点

| 风险 | 等级 | 缓解 |
|---|---|---|
| LLM 抽取 token 成本不可控 | 中 | 默认关 + `minNew` 门 + 批次上限 + `Phase2LLMMerge` 默认 false + no-op 偏好 prompt |
| ingest 与用户 turn 并发写同一 `knowledge` 表 | 低 | SQLite 单写连接（`openSharedSQLite` 全局复用，`sqlite.go:50-103`）+ memkit `Add` 幂等（内容级去重，`sqlite_knowledge.go:29-43`） |
| 多进程同时跑 ingest/Phase2 | 中 | Phase 2 全局锁 + holder + lease；Phase 1 靠 cursor（`memory_ingest_cursor` 主键 upsert，`ingest_meta.go:20-26`），两个进程只会重复读，不会破坏数据 |
| 记忆污染上下文（模型被无关记忆误导） | 中 | 分层披露（L1 摘要优先稳定偏好）+ 检索按 score 排序 + `PromptMaxTokens` 硬顶 |
| `LIKE` 全表扫描性能 | 低 | 5000 条上限 + `LIMIT` 下推；如超出再考虑 FTS5 |
| `agent` 包侵入（`GetSummary` 接口） | 低 | 类型断言可选接口，既有 provider 零改动；`prepareMessages` 逻辑纯增量 |
| 记忆里存了敏感信息（即使有 redact） | 中 | redact 双端 + 抽取 prompt 明确禁止；`delete`/`forget` 操作留给用户（工具 `set_preference` 覆盖写） |
| 多租户隔离 | 中 | `getGlobalUserID`（`ingest.go:253-261`）以 session 为 uid，全局共享空间；若要真正多租户需 user 概念，独立评估 |

---

## 12. 留给用户的待定点

1. **默认开关**：本文案默认关。若你希望 `PersistEnabled=true` 时自动连带开启长期记忆，可改 §7.1。
2. **工具暴露粒度**：`search_indexes` 默认不暴露（`ExposeSearchIndexes=false`）；若你打算接 memdir/MEMORY.md 文档管线，需要单独设计（`dino/chatstore/memdir.go` 目前零调用方）。
3. **Phase 2 LLM 合并**：`Phase2LLMMerge` 默认 false。若你的知识库噪声高、重复多，建议开启并观察 token 成本。
4. **`GetSummary` 接口 vs `Hybrid`**：§6.2 只接可选接口；`Hybrid` provider（LLM 摘要，`memory.go:241-359`）是否作为 chatstore 的默认 provider 替换 `DeterministicCompact`，属评估报告第三章独立任务，不在本设计内。
5. **引用反馈精度**：首版「返回即计数」较粗糙；若要精确到「模型实际引用」，需在 `turn_observe` 层做子串/标签匹配（§4.4 进阶），单独排期。
