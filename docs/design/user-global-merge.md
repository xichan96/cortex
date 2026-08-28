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
