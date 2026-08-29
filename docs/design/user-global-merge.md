# 长期记忆 user 全局合并：跨 session 记忆空间设计方案

> 分支：`p3-design-user-merge`（worktree） · 状态：**设计稿，未改任何业务代码** · 日期：2026-08-29
> 目标：把当前 **per-session** 长期记忆语义升级为 **user 全局**（同一 user 的多个 session 共享同一记忆空间），解决 `docs/next-round.md` P3.2。
> 参照：`docs/design/longterm-memory.md`（接线 + 两阶段 + 检索反馈已落地）与其评审 `docs/design/review-longterm-memory.md`（B1 决策 (a) 的后续独立任务）。

---

## 0. 现状核实（基于本仓库当前代码，2026-08-29）

| 现状 | 证据 |
|---|---|
| `getGlobalUserID` 永远回退 sessionID | `dino/mem/ingest.go:270-278` 读 `metadata WHERE session_id=? AND key='user_id'`；**全仓无任何代码写 `user_id` 键**（metadata 表唯一写入是 `summary`：`dino/chatstore/sqlite.go:182,311`）→ 返回 `sessionID` |
| metadata 表结构 | `dino/chatstore/sqlite.go:118-123`：`(session_id, key, value)`，主键 `(session_id, key)` |
| ingest 写入以 session 为 uid | `dino/mem/ingest.go:101` `userID := getGlobalUserID(...)` → `:218` `SetUserPreference` / `:222` `AddKnowledgeWithCategory`，所有条目 `user_id = sessionID` |
| 记忆工具以 session 为 uid | `dino/mem/tool.go:215` `uid := t.sessionID`；`:241` `SearchKnowledge(ctx, uid, ...)`；`:244` `recordSearchResults(t.sessionID, ...)`（probe 按 session 登记） |
| L1 注入以 session 为 uid | `dino/factory.go:527` `f.longTermMem.BuildLayeredPrompt(ctx, sessionID)`；`BuildLayeredPrompt` 定义 `dino/mem/subsystem.go:189` |
| 记忆工具构造以 session 为 uid | `dino/factory.go:577` `MemoryToolsForSession(sessionID, ...)`；定义 `dino/mem/subsystem.go:154-169` |
| 检索按 user_id 过滤 | `pkg/memkit/sqlite/sqlite_knowledge.go:130` `WHERE user_id = ?`；`sqlite_preference.go:61,78` 同 |
| 内容级去重是 per-user 的 | `sqlite_knowledge.go:38` `WHERE user_id = ? AND LOWER(...normalized content...)`——**同一 user 下同内容自动合并 tags** |
| preference 覆盖是 per-user 的 | `sqlite_preference.go:47` `INSERT OR REPLACE ... UNIQUE(user_id, category, key)`（约束在 `pkg/memkit/sqlite/sqlite.go:77`） |
| Phase 2 已按 user 遍历 | `dino/mem/subsystem.go:307` `runPhase2Loop` → `:335` `runPhase2Merge` → `:498` `mergeDuplicateKnowledge` / `:364` `llmConflictMerge`，均 `SELECT DISTINCT user_id FROM knowledge`；锁原语 `pkg/memkit/sqlite/phase2.go:30` `TryClaimPhase2` |
| session 无 user 概念 | `dino/session/session.go:42-56` `Config` 无 user 字段；`CreateSession(ctx, sessionID, opts...)`（`dino/factory.go:502`）调用方只传 sessionID |
| 引用反馈按 id、probe 按 session | `dino/mem/usage_feedback.go:71` `ObserveAssistantFeedback(ctx, sessionID, text)`；`RecordKnowledgeUse(id)`（`sqlite_knowledge.go:250`）按条目 id 计数，不受 user 影响 |
| 配置定义与默认 | `MemLongTermConfig` 在 `dino/mem/types.go:42-58`（`dino/config.go:68` 别名引用）；默认 `Enabled: false`（`dino/config.go:231`） |

**一句话**：唯一缺的是「把 session 归属到 user」的载体——`metadata.user_id` 表结构已存在、`getGlobalUserID` 已能读、memkit 的检索/去重/覆盖/Phase 2 合并**全部天然 per-user**。只要让「session → user」的映射被写进 metadata 并被 L1/工具/ingest 使用，跨 session 共享就自动成立，**memkit 层几乎零改动**。

---

## 0.1 总体决策（关键设计选择）

| # | 决策点 | 决策 | 理由 |
|---|---|---|---|
| D1 | user 身份的来源 | **`CreateSession` 通过新 `session.WithUserID(uid)` option 传入**；未传时回退配置 `DefaultUserID`；再回退常量 `"default"` | 单机/单进程场景开箱即用（默认单 user），多租户由上层服务显式传 uid 隔离；不引入 auth/登录，保持最小侵入 |
| D2 | 归属载体 | **复用 `metadata` 表写 `user_id` 键**（`chatstore/sqlite.go:118-123`），`INSERT OR IGNORE` 幂等 | 表已存在、`getGlobalUserID` 已读，无需新表；主键 `(session_id, key)` 天然「一个 session 一个归属」 |
| D3 | 写入方 | `LongTermMem.SetSessionUser(ctx, sessionID, userID)`，由 `dino/factory.go` `CreateSession` 调用 | 归属在 session 创建时确定；ingest 后跑自然能读到（`ingest.go:270` 已读） |
| D4 | 合并语义 | **不做新合并算法**——memkit 现有 per-user 内容级去重（`sqlite_knowledge.go:38`）+ preference 覆盖（`sqlite_preference.go:47`）+ Phase 2 全局合并（`subsystem.go:335`）在 user 归并后**自动跨 session 生效** | 去重/覆盖/合并全部按 `user_id` 键控，同一 user 下多 session 条目自然收敛 |
| D5 | 兼容 | 旧 session（无 `metadata.user_id`）保持回退 sessionID；`UserMergeEnabled=false` 时行为与现状**完全一致** | 默认关 + 回退 = 零破坏性 |
| D6 | 迁移 | 新 `MigrateLegacySessionKnowledge`（`pkg/memkit/sqlite`）在 Phase 2 内执行：把「该 session 名下的旧条目」从 `user_id=sessionID` 归拢到它的 `metadata.user_id`；幂等 | 迁移目标取每个 session 自己的归属，而非一刀切 default，多租户也正确 |
| D7 | 默认开关 | `UserMergeEnabled` 默认 **false**（保持 per-session） | 迁移有数据归拢副作用，需显式开启；与整个 LTM 默认关一致 |

---

## 1. 多 session 归属策略

### 1.1 归属链路（谁写 user_id）

```
CreateSession(ctx, sessionID, opts...)
  ├─ session.WithUserID("u_123") option          ← 多租户：上层服务为每个用户传固定 uid
  ├─ Config.DefaultUserID                        ← 单用户：配置统一归属
  └─ 常量 "default"                              ← 兜底
        ↓
  ResolveUserID(userCfg, defaultUserID) → userID
        ↓
  LongTermMem.SetSessionUser(ctx, sessionID, userID)   ← 写 metadata (session_id,'user_id',userID)，INSERT OR IGNORE
        ↓
  L1:   BuildLayeredPrompt(ctx, userID)          ← factory.go:527 改传 userID
  Tools: MemoryToolsForSession(sessionID, WithUserID(userID)) ← factory.go:577
  Ingest: getGlobalUserID(ctx, db, sessionID) 已读 metadata   ← 自动拿到 userID
```

### 1.2 新配置字段（`dino/mem/types.go` `MemLongTermConfig`）

```go
UserMergeEnabled bool   `yaml:"user_merge_enabled"` // 默认 false。开启 user 全局合并：
                       // 未显式归属的 session 归 DefaultUserID，Phase 2 迁移旧数据
DefaultUserID     string `yaml:"default_user_id"`   // 默认 ""（空则用常量 defaultUserIDFallback="default"）。
                       // 仅 UserMergeEnabled=true 时生效
```

### 1.3 新 session option（`dino/session/session.go`）

```go
// Config 追加：
UserID string // 显式归属的 user；空 = 未显式指定

// 新 option：
func WithUserID(userID string) Option {
    return func(c *Config) { c.UserID = userID }
}
```

### 1.4 归属解析（`dino/mem/user.go`，新文件，纯函数便于测试）

```go
// defaultUserIDFallback 是「未显式归属且无配置」时的兜底 user。
const defaultUserIDFallback = "default"

// ResolveUserID 解析一个 session 的归属 user。
// 优先级：sessionConfigUserID（WithUserID）> defaultUserID（配置）> defaultUserIDFallback。
func ResolveUserID(sessionConfigUserID, defaultUserID string) string {
    if sessionConfigUserID != "" {
        return sessionConfigUserID
    }
    if defaultUserID != "" {
        return defaultUserID
    }
    return defaultUserIDFallback
}
```

### 1.5 归属写入（`dino/mem/subsystem.go` 新增方法）

```go
// SetSessionUser 把一个 session 归属到 userID，写 metadata 表。
// INSERT OR IGNORE：已有归属的 session 不覆盖（归属在创建时固化，不动态变更）。
func (l *LongTermMem) SetSessionUser(ctx context.Context, sessionID, userID string) error {
    db, err := mgrDB(ctx, l.mgr)
    if err != nil {
        return err
    }
    _, err = db.ExecContext(ctx,
        `INSERT OR IGNORE INTO metadata (session_id, key, value) VALUES (?, 'user_id', ?)`,
        sessionID, userID)
    return err
}
```

### 1.6 factory 接线（`dino/factory.go` `CreateSession`）

在 `factory.go:526`（L1 注入处）之前解析 userID 并写归属：

```go
// CreateSession 内，agentConfig.SystemMessage 拼装之前：
userID := ""
if f.longTermMem != nil {
    userID = mem.ResolveUserID(sessionCfg.UserID, f.config.LongTermMemory.DefaultUserID)
    if f.config.LongTermMemory.UserMergeEnabled {
        _ = f.longTermMem.SetSessionUser(ctx, sessionID, userID) // 失败仅记日志，不影响会话
    } else {
        userID = "" // 未开启：保持 per-session，L1/工具仍用 sessionID
    }
}
// L1（原 :526-530）：
if f.longTermMem != nil {
    l1UID := userID
    if l1UID == "" {
        l1UID = sessionID
    }
    if l1 := f.longTermMem.BuildLayeredPrompt(ctx, l1UID); l1 != "" {
        systemPrompt += "\n\n" + l1
    }
}
// 工具（原 :577）：
ltmTools := f.longTermMem.MemoryToolsForSession(sessionID,
    mem.WithToolNameOverride("memory"),
    mem.WithUserID(userID)) // userID="" → 工具回退 sessionID
```

> 注意：`sessionCfg` 在 `factory.go:514-517` 应用 option 后拿到，`WithUserID` 已生效。

### 1.7 工具层 uid 语义（`dino/mem/tool.go`）

- `MemoryToolOptions` 加 `UserID string`；`sqliteMemoryTool` 加 `userID string` 字段。
- `Execute` 中 `uid := t.userID; if uid == "" { uid = t.sessionID }`（`tool.go:215` 处替换）。
- **probe 登记保持 `t.sessionID`**（`tool.go:244`）：引用反馈是「本次 turn 搜了哪些条目、模型输出是否引用」，必须按 session 隔离；`RecordKnowledgeUse(id)` 按 id 全局计数，与 user 无关，跨 session 迁移后 id 不变仍命中。
- `WithUserID(uid)` 新 MemoryToolOption（`dino/mem/subsystem.go:179` 附近）。

---

## 2. 合并语义

### 2.1 归属后自动获得的合并能力（无需新算法）

| 场景 | 现状行为（per-user） | user 归并后的效果 |
|---|---|---|
| 跨 session 同 content 的 knowledge | `sqlite_knowledge.go:38` `WHERE user_id=?` + normalized content 匹配 → 合并 tags、更新 updated_at | **自动生效**：A、B 两 session 写同内容，user_id 相同 → 收敛为一条 |
| 跨 session 同 (cat,key) 的 preference | `sqlite_preference.go:47` `INSERT OR REPLACE` + `UNIQUE(user_id, category, key)` → 后写覆盖 | **自动生效**：同 user 下后者覆盖前者，`updated_at` 记录最后写入 |
| 跨 session 语义重复（非精确重复） | `subsystem.go:364` `llmConflictMerge` 按 user 两两 LLM 判定合并（`Phase2LLMMerge=true` 时） | **自动生效**：遍历 user_id，候选集变为该 user 全 session 条目 |

**结论**：合并语义的「按键」全部是 `user_id`。把 uid 从 sessionID 换成 userID 后，memkit 层（`sqlite_knowledge.go` / `sqlite_preference.go` / `subsystem.go` Phase 2）**零改动**即获得跨 session 合并。

### 2.2 冲突消解规则

- **knowledge 精确重复**（同 user + normalized content）：tags 合并，content 保留原值，`usage_count`/`last_usage` 不动（`Add` 的 UPDATE 只改 tags+updated_at，`sqlite_knowledge.go:44-47`）。
- **preference 同 (cat,key) 冲突**：后写覆盖（`INSERT OR REPLACE`）。合并语义即「以最近一次写入为准」。若需保留历史可在 `metadata` 里记 `source=sessionID`（见 §9 待定点）。
- **语义重复**：`Phase2LLMMerge`（默认 false）做 LLM 判定；保留较新条目、tags 并入、删除较旧（`subsystem.go:426-433`）。

### 2.3 usage_count / updated_at 如何参与

- `usage_count`/`last_usage`：**合并不触碰**——`Add` 去重只改 tags+updated_at；`RecordKnowledgeUse` 只按 id 自增（`sqlite_knowledge.go:250`）。迁移也不触碰（§4）。
- `updated_at`：**已知缺陷**——`mergeDuplicateKnowledge` → `reingestUserKnowledge`（`subsystem.go:529`）用 `Add` 重写全部条目，`Add` 会把 `updated_at` 刷新为 now，导致「最近更新」排序在每次 Phase 2 后被刷平。user 归并后条目变多，此问题更明显。**Step 4 修复**：合并去重时保留原 `updated_at`（`Add` 的 UPDATE 分支改为 `updated_at = MIN(updated_at, now)` 或保留原值）。

### 2.4 Phase 2 复用点

`runPhase2Merge`（`subsystem.go:335`）已经：认领全局锁（`TryClaimPhase2`，`phase2.go:30`）→ 剪枝（`PruneUnused`）→ `mergeDuplicateKnowledge` →（可选）`llmConflictMerge` → 记冷却（`MarkPhase2Cooldown`）。**全部按 user 遍历**，跨 session 合并自动成立。唯一新增：在拿锁后插入迁移钩子（§4.3），保证旧数据先归拢再合并。

---

## 3. 检索范围：per-session → 跨 user 全局

### 3.1 检索入口的 uid 来源（全部收敛到一处）

| 检索路径 | 现状调用 | 改造后 |
|---|---|---|
| 工具 `search_knowledge` | `tool.go:241` `SearchKnowledge(ctx, t.sessionID, ...)` | `SearchKnowledge(ctx, t.userID 或回退 sessionID, ...)` |
| 工具 `list_preferences` | `tool.go:236` `GetUserPreferences(ctx, uid)` | 同上 |
| 工具 `get_preference` | `tool.go:232` `GetUserPreference(ctx, uid, cat, key)` | 同上 |
| 工具 `search_indexes` | `tool.go:249` `SearchIndexes(ctx, uid, ...)` | 同上 |
| 工具 `memory_stats` | `tool.go:253` `GetStats(ctx, uid)` | 同上 |
| L1 注入 | `factory.go:527` `BuildLayeredPrompt(ctx, sessionID)` | `BuildLayeredPrompt(ctx, userID)`（`userID==""` 时回退 sessionID） |

**关键**：memkit 的 `Search`（`sqlite_knowledge.go:128`）/`GetByUser`（`sqlite_preference.go:76`）的过滤条件 `WHERE user_id = ?` **不用改**——参数从 sessionID 换成 userID 即跨 session 全局检索。这是本设计的核心杠杆：**只改调用方的 uid 参数，不改存储层**。

### 3.2 uid 参数如何在各层传递（避免散落）

1. **工具层**：`sqliteMemoryTool.userID` 字段（§1.7）。`MemoryToolsForSession` 增加 `WithUserID` option，未开启合并时 `userID=""` 保持 sessionID 语义。
2. **子系统层**：`BuildLayeredPrompt(ctx, uid)` 签名不变，调用方传 userID。
3. **ingest 层**：`getGlobalUserID` 不动——它已读 `metadata.user_id`，`SetSessionUser` 写入后自动返回 userID。

### 3.3 检索隔离边界（必须保留的 per-session 语义）

- **引用反馈 probe 按 session**：`usage_feedback.go:38` `recordSearchResults(t.sessionID, items)`、`:71` `ObserveAssistantFeedback(ctx, sessionID, text)`——probe 是「turn 内搜了哪些」，**不能**换成 userID，否则 A session 的搜索会把引用记到 B session 的 probe 上。`RecordKnowledgeUse(id)` 按全局 id 计数，不受影响。
- **cursor 按 session**：`memory_ingest_cursor`（`ingest_meta.go`）主键 `session_id`，**保持**——ingest 增量扫描天然按 session 推进，与 user 无关。

---

## 4. 兼容与迁移

### 4.1 向后兼容矩阵

| 场景 | `UserMergeEnabled=false`（默认） | `UserMergeEnabled=true` |
|---|---|---|
| 已有 session（无 `metadata.user_id`） | 行为与现状完全一致：`getGlobalUserID` 回退 sessionID，读写都 per-session | 创建时 `SetSessionUser` 写入归属；旧 session 若未重建，`getGlobalUserID` 仍回退 sessionID，直到迁移归拢 |
| 已有知识条目（`user_id=sessionID`） | 不动 | Phase 2 迁移归拢到归属 user（§4.3） |
| 新 session | 无归属写入，per-session | 写归属，读写 user 全局 |
| L1 / 工具 | sessionID 语义（`userID=""` 回退） | userID 语义 |

### 4.2 旧 session 仍能读到自己的记忆（关键约束）

迁移**归拢而非删除**：把 `user_id=sessionID` 的条目改写到该 session 的归属 `metadata.user_id`。同一 user 的所有旧 session 归到同一 user_id，因此：

- 旧 session A 继续用 `userID`（= 原 sessionID 的归属）检索 → 能读到 A 自己原条目（已归拢），**也**能读到 B 归拢来的共享条目——这正是 user 全局合并的意图。
- 未开启合并的进程/旧二进制：读到的是 `user_id=sessionID` 的残留（未被迁移的 session）——**默认关时无迁移，无残留**。

### 4.3 迁移：`MigrateLegacySessionKnowledge`（`pkg/memkit/sqlite/migrate_user.go`，新文件）

```go
// MigrateLegacySessionKnowledge 把「以 sessionID 为 user_id 的旧条目」归拢到
// 该 session 的 metadata.user_id。幂等：已归拢（user_id 不再是任何 sessionID）
// 的条目不重复处理。
//
// 目标 user 取自每个 session 自己的归属（metadata.user_id），非一刀切 default，
// 保证多租户下迁移正确。
func MigrateLegacySessionKnowledge(ctx context.Context, db *sql.DB) (int, error)
```

逻辑：
1. `SELECT DISTINCT user_id FROM knowledge` 找出所有「可能是 sessionID 的 uid」。
2. 对每个候选 uid：`SELECT value FROM metadata WHERE session_id = ? AND key = 'user_id'` 查归属；查不到（无归属）跳过——**该 session 数据保持原样**，等它被 `SetSessionUser` 创建后下次迁移处理。
3. 有归属 `u` 且 `u != uid`：`UPDATE knowledge SET user_id = u WHERE user_id = uid`；`UPDATE preferences SET user_id = u WHERE user_id = uid`。
4. **目标 user 归并后的内容级去重**：`UPDATE` 后同 user 下可能产生 normalized-content 重复——交给随后的 `mergeDuplicateKnowledge`（§2.4 顺序保证）用现有 `Add` 去重逻辑收敛。
5. `context` / `indexes` / `page_index` 的 `user_id` 不迁移——它们分别是 session 级临时上下文（`context` 表 `session_id` 主键）与文档索引，不参与用户长期记忆归并。**边界在 §9 待定点 2 说明。**

`context` 表注意：`sqlite.go:97-111` 的 `context` 表有 `user_id` 列但主键是 `session_id`，是 session 级；迁移只动 `knowledge`/`preferences`。

### 4.4 迁移触发点（挂进 Phase 2）

`runPhase2Merge`（`subsystem.go:335`）拿锁后、`mergeDuplicateKnowledge` 之前插入：

```go
if cfg.UserMergeEnabled {
    n, err := memsqlite.MigrateLegacySessionKnowledge(ctx, db)
    if err != nil {
        return err // 或记日志继续；建议记日志继续（剪枝/合并下一 tick 重试）
    }
    if n > 0 {
        log.Info("memory_phase2: migrated legacy session knowledge", "count", n)
    }
}
```

**为什么放 Phase 2**：迁移有全表写 + 去重，需要全局锁互斥（多进程），Phase 2 的 `TryClaimPhase2` 正好提供；且迁移后立刻跑 `mergeDuplicateKnowledge` 收敛重复，一个 tick 内完成「归拢 + 去重」。

### 4.5 迁移幂等与失败路径

- **幂等**：第二步的归属查询天然幂等——归拢过的 uid 已不再是 `knowledge.user_id` 候选（除非该 session 又写入了新条目；此时它「旧条目已归拢、新条目直接写 user_id」，下次迁移只搬新条目）。重复执行不产生重复行。
- **失败路径**：`MigrateLegacySessionKnowledge` 内用单个 `UPDATE`（原子）逐 session 处理；任一失败返回错误，Phase 2 记日志，下一 tick（或 6h 冷却后）重试。**半途中断**：已成功的 session 归拢保留，未处理的下次继续——无中间态损坏。
- **无归属 session**：跳过（§4.3-2）。这意味着「开启合并前已存在、但从未重建、也没被 `SetSessionUser` 写过的 session」其数据**永远留在 per-session 态**，直到该 session 被创建一次。这是可接受的折中——迁移不猜用户身份。

---

## 5. 配置

### 5.1 默认值策略

**默认关**（`UserMergeEnabled: false`）。理由：

1. **迁移有数据归拢副作用**：开启后 Phase 2 会把旧 session 数据重新归属，破坏「per-session 即租户隔离」的现状语义（虽然当前没有真正多租户，但语义改变需要显式 opt-in）。
2. **与整个 LTM 一致**：`LongTermMemory.Enabled` 默认 false（`dino/config.go:231`），开启长期记忆已是 opt-in；user 合并是更进一步的 opt-in。
3. **零破坏承诺**：默认关 = 行为与当前实现逐字节一致，可随时回退。

### 5.2 新增配置字段

`dino/mem/types.go` `MemLongTermConfig` 追加（§1.2 已列，此处给出完整默认值）：

```go
// UserMergeEnabled 默认 false：开启后 session 归属到 user（WithUserID > DefaultUserID > "default"），
// L1/工具/ingest 以 user 为 uid 检索，Phase 2 迁移旧 session 数据并跨 session 合并。
UserMergeEnabled bool   `yaml:"user_merge_enabled"`
// DefaultUserID 默认 ""：未显式 WithUserID 的 session 归属到该值；为空则回退常量 "default"。
// 单用户部署设一个固定 uid 即可让所有 session 共享记忆空间。
DefaultUserID     string `yaml:"default_user_id"`
```

`dino/config.go` `DefaultConfig()` 追加：

```go
LongTermMemory: MemLongTermConfig{
    // ...现有字段
    UserMergeEnabled: false,
    DefaultUserID:    "",
},
```

### 5.3 配置交互矩阵

| `UserMergeEnabled` | 显式 `WithUserID` | 行为 |
|---|---|---|
| false | — | per-session，与现状一致 |
| true | 有 | 该 session 归属显式 uid，读写 user 全局 |
| true | 无（`DefaultUserID` 非空） | 归属 `DefaultUserID` |
| true | 无（`DefaultUserID` 空） | 归属 `"default"` |

---

## 6. 测试清单

### 6.1 单测（纯函数/层内）

| # | 位置 | 用例 |
|---|---|---|
| T1 | `dino/mem/user_test.go`（新） | `ResolveUserID`：三个优先级（WithUserID > DefaultUserID > "default"）、空值组合 |
| T2 | `dino/mem/subsystem_test.go` | `SetSessionUser`：写 metadata `user_id` 键；重复调用不覆盖（INSERT OR IGNORE）；空 userID 拒绝或回退 |
| T3 | `pkg/memkit/sqlite/migrate_user_test.go`（新） | `MigrateLegacySessionKnowledge`：有归属的 session 条目归拢；无归属跳过；已归拢不重复处理（幂等）；knowledge+preferences 都搬 |
| T4 | 同上 | 迁移后同 user 内容重复由 `mergeDuplicateKnowledge` 收敛（调用现有去重验证条数） |
| T5 | `dino/mem/tool_test.go` | 工具 `userID` 字段：有值用 userID、空值回退 sessionID；`search_knowledge` probe 仍按 session 登记 |

### 6.2 集成测试（多 session 场景）

| # | 用例 | 验证点 |
|---|---|---|
| I1 | `UserMergeEnabled=true`，session A、B 同 user（`WithUserID("u1")`）→ A 写 knowledge `K`，B `search_knowledge(K 关键词)` | B 能搜到 A 写的 K（跨 session 检索） |
| I2 | A 写 content `X`，B 写同 content `X` → 查 `knowledge` 表 | 同 user 下合并为 1 条、tags 合并（`usage_count` 不变） |
| I3 | A `set_preference(cat,key,v1)`，B `set_preference(cat,key,v2)` → B `get_preference` | B 读到 v2（后写覆盖跨 session 生效） |
| I4 | `UserMergeEnabled=false` → A、B 各自写同名 content `X` → 查表 | **仍是 2 条**（per-session 不变，回归） |
| I5 | 预置旧数据（`user_id="sess_old"` 若干条目 + `metadata(sess_old,'user_id','u1')`）→ 触发一次 Phase 2（`runPhase2Merge` 或 `MigrateLegacySessionKnowledge`）→ 查 `knowledge` | 条目 `user_id` 变为 `u1`，`sess_old` 名下无残留；`search_knowledge(u1)` 能搜到 |
| I6 | 旧 session 无归属（无 `metadata.user_id`）→ 跑迁移 | 其条目不动，`search_knowledge(sess_old)` 仍按 sessionID 能搜到（兼容） |
| I7 | `Phase2LLMMerge=true` + A、B 各写语义重复条目 → 跑 Phase 2 | LLM 判定合并为 1 条（跨 session 语义合并） |
| I8 | 引用反馈：A 搜索条目 `K`，A turn 输出包含 `K` 片段 → `K.usage_count` +1；B 的搜索不干扰 A 的 probe | probe per-session 隔离；计数按全局 id |
| I9 | `UserMergeEnabled=true` + 并发两个进程各跑 Phase 2 | `TryClaimPhase2` 只放行一个（迁移+合并串行） |

### 6.3 迁移专项

| # | 用例 |
|---|---|
| M1 | 迁移目标 user 已存在同名 content（A 旧条目 + B 新条目同内容）→ 归拢后合并为 1 条、tags 并 |
| M2 | 迁移中 `metadata.user_id` 指向自己（`u==uid`）→ 跳过（不产生无意义 UPDATE） |
| M3 | 迁移幂等：连续跑两次 → 第二次迁移条数为 0 |
| M4 | `UserMergeEnabled` 从 false 改 true 再改 false → 已归拢数据保留（不回滚），新写入回到 per-session |

---

## 7. 迁移顺序（分步，每步独立可验证）

| Step | 内容 | 验证 | 涉及文件 |
|---|---|---|---|
| **1. uid 语义打通（最小可用）** | `ResolveUserID` + `SetSessionUser` + factory 写归属 + L1/工具改传 userID + `WithUserID` option；`UserMergeEnabled=true` 生效 | T1/T2/T5、I1/I3/I4、I8；`go build ./...`、`go test ./dino/... ./pkg/memkit/...` | 新增 `dino/mem/user.go`；改 `dino/mem/{subsystem,tool,types}.go`、`dino/session/session.go`、`dino/factory.go`、`dino/config.go` |
| **2. 检索跨 session** | 确认全部检索入口（工具 6 处 + L1）uid 已换成 userID；验证跨 session 检索 | I1/I2/I3 | 同 Step 1（复用），无新文件 |
| **3. 迁移** | `MigrateLegacySessionKnowledge` + 挂进 Phase 2 + `WithUserID` 时旧 session 创建即写归属 | I5/I6、M1-M4 | 新增 `pkg/memkit/sqlite/migrate_user.go`；改 `dino/mem/subsystem.go`（runPhase2Merge） |
| **4. 合并增强** | 修 `reingestUserKnowledge` 的 `updated_at` 刷平缺陷；验证 Phase 2 合并跨 session 收敛 | I2/I7 | `pkg/memkit/sqlite/sqlite_knowledge.go`（`Add` UPDATE 分支）、`dino/mem/subsystem.go` |

> Step 1-2 构成「跨 session 共享」最小可用（无迁移）；Step 3 补旧数据归拢；Step 4 修已知缺陷。每步独立 commit（`feat(mem): ...`），不 push。

---

## 8. 改动文件清单

### 新增
- `dino/mem/user.go` — `ResolveUserID` / `defaultUserIDFallback`
- `pkg/memkit/sqlite/migrate_user.go` — `MigrateLegacySessionKnowledge`
- 测试：`dino/mem/user_test.go`、`pkg/memkit/sqlite/migrate_user_test.go`

### 修改
- `dino/mem/types.go` — `MemLongTermConfig` 加 `UserMergeEnabled`/`DefaultUserID`；`MemoryToolOptions` 加 `UserID`
- `dino/mem/subsystem.go` — `SetSessionUser`、`WithUserID` option、`runPhase2Merge` 插迁移钩子
- `dino/mem/tool.go` — `sqliteMemoryTool.userID` 字段 + `Execute` uid 解析（`tool.go:215`）
- `dino/session/session.go` — `Config.UserID` + `WithUserID`
- `dino/factory.go` — `CreateSession` 解析 userID、写归属、L1/工具传 userID（`factory.go:526-530,577`）
- `dino/config.go` — 默认值
- `pkg/memkit/sqlite/sqlite_knowledge.go` — （Step 4）`Add` UPDATE 分支保留原 `updated_at`
- `docs/design/user-global-merge.md`（本文档）

### 不动
- `pkg/memkit/sqlite/sqlite_knowledge.go`（`Search`/`Add`）的 per-user 逻辑——uid 参数变了就够
- `pkg/memkit/sqlite/sqlite_preference.go`——同上
- `dino/mem/ingest.go` `getGlobalUserID`——已读 metadata，`SetSessionUser` 写入后自动生效
- `dino/mem/usage_feedback.go`——probe 按 session 隔离保持
- `pkg/memkit/sqlite/phase2.go`——锁/剪枝原样复用

---

## 9. 风险点与待定点

### 9.1 风险

| 风险 | 等级 | 缓解 |
|---|---|---|
| 多 session 同 user 记忆互相污染（A 的记忆误导 B） | 中 | 这正是 user 全局合并的**意图**，非缺陷；隔离需求方（如不同项目共用一个 user）需上层按 project 拆分 user_id，或在 `DefaultUserID` 里含 workspace 维度 |
| 迁移误归拢（把 sessionID 当成 user_id 处理） | 低 | 迁移只动 `knowledge`/`preferences` 的 `user_id`；目标取 metadata 归属，无归属不动 |
| `updated_at` 刷平影响 L1 选条（Step 4 前） | 低-中 | Step 4 修复；Step 1-3 期间 L1 选条用 `updated_at DESC` 会偏向最近合并的条目 |
| 迁移与 ingest 并发写同一 user 的 knowledge | 低 | SQLite 单连接 + WAL + busy_timeout（`chatstore/sqlite.go:80`）；Phase 2 有全局锁，ingest 靠 cursor 幂等 |
| 多租户下 `"default"` 兜底误串 | 中 | 文档明确：多租户必须显式 `WithUserID`，不能依赖 `"default"`；未来可加「无显式 uid 时禁用 user 合并」的开关（待定点） |
| `context`/`indexes`/`page_index` 的 `user_id` 不迁移，跨 session 检索时这些表仍是 per-session | 低 | 这些表不是「长期用户记忆」的主体；`search_indexes` 默认不暴露（`ExposeSearchIndexes=false`），影响面小 |

### 9.2 留给用户的待定点

1. **`WithUserID` 的调用方**：本设计在 `CreateSession` 签名上通过 `session.WithUserID(uid)` 传入，但当前所有调用方（`examples/dino/main.go:142`、`dino/client.go:60`、`dino/factory.go:795`）都不传。**需用户决定**：上层服务是否在 `Client.CreateSession` 暴露 userID 参数？还是只在配置 `DefaultUserID` 层支持（单用户场景够用）？
2. **`context` 表是否归并**：`context` 表是 session 级临时上下文（`sqlite.go:97-111`），本设计不迁移。若上层想让它跨 session 共享（比如跨 session 传递临时偏好），需单独设计——建议不并入，保持 session 级语义。
3. **归属可否变更**：`SetSessionUser` 用 `INSERT OR IGNORE` 固化归属。若用户需要「同 session 切换 user」（如账号切换），需要新的覆盖策略——建议不支持，session 创建即固定归属，账号切换开新 session。
4. **迁移时机**：迁移挂 Phase 2（6h 冷却内最多跑一次成功）。若用户希望开启后立刻迁移，可提供 `IngestNow` 之外的 `MigrateNow(ctx)` 手动触发入口。
5. **`DefaultUserID` 是否含 workspace 维度**：单进程多项目（各自独立代码库）共用一个 user 会让记忆串项目。若需要按项目隔离，`DefaultUserID` 应设为 `"<workspace>"` 或 `"<project>"`——由部署方决定。

---

## 10. 设计完成状态（2026-08-29）

**状态**：设计稿完成，未改任何业务代码（仅 docs/design/ 下新增本文件）。可进入实现，按 §7 四步推进。

**改动清单**（实现时）：
- 新增 `dino/mem/user.go`（`ResolveUserID`）、`pkg/memkit/sqlite/migrate_user.go`（`MigrateLegacySessionKnowledge`）
- 修改 `dino/mem/{types,subsystem,tool}.go`、`dino/session/session.go`、`dino/factory.go`、`dino/config.go`、`pkg/memkit/sqlite/sqlite_knowledge.go`（Step 4）

**关键决策**：
- uid 从 sessionID 换 userID 只改调用方（L1/工具/ingest 读 metadata 自动生效），**memkit 存储层零改动**
- 归属载体复用 `metadata` 表写 `user_id` 键；合并复用 memkit 现有 per-user 去重/覆盖/Phase 2
- `UserMergeEnabled` 默认 false，兼容性零破坏

**留给用户的待定点**：
1. `WithUserID` 是否需在 `Client.CreateSession` 暴露（现所有调用方不传）
2. `context` 表是否跨 session 归并（建议不并入）
3. 归属是否支持变更（建议 session 创建即固化）
4. 迁移是否需要 `MigrateNow` 手动触发（现挂 Phase 2）
5. `DefaultUserID` 是否含 workspace 维度（防多项目串记忆）

---

## 11. 实现备注（2026-08-29，按评审 B1-B4 修正实现）

实现时按 `docs/design/review-user-global-merge.md` 的 4 个 BLOCKER 修正，与本文档原设计的差异如下：

### 与设计 §7 Step 的对应

| 设计 Step | 实现 | 偏差 |
|---|---|---|
| Step 1-2 uid 打通 + 检索跨 session | `user.go`（`ResolveUserID`/`UserIDForSession`）、`session.WithUserID`、`SetSessionUser`、factory 归属写入、工具 `WithUserID` | **评审 B3 修正**：工具/L1/ingest 的 uid 不再各自解析，统一以 `metadata.user_id` 为单一事实源（`UserIDForSession` / `SessionUserID`），内存解析值只用于 `SetSessionUser` 首次写入 |
| Step 3 迁移 | `pkg/memkit/sqlite/migrate_user.go` 挂 Phase 2 | **评审 B2 修正**：preferences 迁移逐条按 `(owner,cat,key)` 冲突消解（updated_at 较新者胜），避免撞 `UNIQUE(user_id, category, key)`；knowledge 迁移后由 `DedupUserKnowledge` 收敛 |
| Step 4 合并增强 | `DedupUserKnowledge`（`phase2.go`）+ `Add` 去重分支 `updated_at = MAX(updated_at, now)` | **评审 B1/R6/task B2/B3 修正**：`mergeDuplicateKnowledge` 不再走 `reingestUserKnowledge`→`Add` 的「只合并 tags 不删行」路径，改为真去重（保留 updated_at 最新行、tags 并入、删其余行、保留原 updated_at 不刷平） |

### 关键实现决策

1. **真去重取代 Add 副作用**：原设计的「迁移归拢后交给 mergeDuplicateKnowledge 用 Add 收敛」不成立（`Add` 只合并不删行）。新增 `memsqlite.DedupUserKnowledge(ctx, db, userID)` 按 `normalizeContentForDedup` 分组真收敛，`mergeDuplicateKnowledge` 遍历所有 user 调用它。设计 §2.4/§4.4 的「一个 tick 内完成归拢 + 去重」因此成立。
2. **updated_at 保留**：`DedupUserKnowledge` 保留行取 `updated_at` 最新、tags 并集，且**不刷平**（task B3：`PruneUnused` 按 `updated_at < cutoff AND usage_count=0` 剪枝，刷平会永不剪枝）。`Add` 去重分支改用 `updated_at = MAX(updated_at, now)`——正常新写入同内容刷新新鲜度，但不超过既有值。
3. **迁移独立于 `Phase2Merge`**（评审 B4）：`Start` 在 `Phase2Merge || UserMergeEnabled` 时启动 loop；`runPhase2Loop` tick 同样按该条件放行。`UserMergeEnabled=true && Phase2Merge=false` 时迁移仍会跑，新写 user 全局、旧数据归拢，不永久碎片化。
4. **单用户限定**（task B4）：本次只支持 `DefaultUserID`（单用户）。`WithUserID` 透传（`Client.CreateSession` 暴露参数）为后续独立任务；「多租户必须显式 WithUserID、`"default"` 兜底仅限单用户」已写进 `MemLongTermConfig` 文档注释。
5. **快照恢复走归属**（评审 R3）：`RestoreSessionSnapshot` 在 `UserMergeEnabled` 时给恢复的 session 传 `WithUserID(DefaultUserID)`，避免落进错误 user 桶。
6. **`SetSessionUser` 失败记日志**（评审 R8）：不再 `_ =` 吞错；失败时工具/L1 因 `SessionUserID` 读不到归属而回退 sessionID（per-session 语义），不分裂。

### 未纳入本次实现的评审建议（RECOMMENDED/OPTIONAL）

- **R1**（SQL/Go 归一化不一致）：`Add` 去重的 SQL `LOWER(REPLACE(...))` 与 Go `normalizeContentForDedup` 对连续空白/首尾空白的处理不一致，写入路径对「double-space」同义内容可能产生瞬时重复。因 Phase 2 的 `DedupUserKnowledge` 用 Go 归一化收敛，终态一致；写入瞬态重复由下一 tick Phase 2 收敛。未改 `Add` SQL（避免改动既有写入路径的查重口径）。
- **R7**（`search_indexes`/`page_index` 未迁移）：保持设计边界，indexes/page_index 不随 user 归并；`ExposeSearchIndexes=true` 时旧 session index 在 user 全局检索下不可见。已文档说明。
- **O1**（`llmConflictMerge` 精确重复边界）：既有代码问题，未改。

### 已知边界

- `context` 表不迁移（session 级临时上下文）；`indexes`/`page_index` 不迁移（文档索引）。
- 迁移只处理「knowledge + preferences 的 user_id 归拢」；`context`/`indexes`/`page_index` 的 `user_id` 保持原样。
