# 评审报告：长期记忆 user 全局合并（P3.2）设计

> 评审对象：`docs/design/user-global-merge.md`（2026-08-29）
> 评审日期：2026-08-29 · 评审人：cortex 资深评审（只产出本报告，未改任何代码/现有文档）
> 评审基准：本 worktree 内真实代码（commit `6ef40d0`）

---

## 0. 结论摘要

**方向正确、零件基本准确，但不能按当前设计直接进入实现**。

设计抓住的核心杠杆是对的：`metadata` 表已存在、`getGlobalUserID` 已能读、memkit 检索/去重/覆盖全部天然按 `user_id` 键控——只改调用方 uid 参数、不动存储层，这个判断经核实**成立**。归属链路（`WithUserID` > `DefaultUserID` > `"default"`）、`INSERT OR IGNORE` 固化、`UserMergeEnabled` 默认关、Phase 2 挂迁移，这些设计决策总体合理。

**但存在 4 个 BLOCKER，全部落在「迁移 + 合并」这一核心卖点上**，且其中 2 个会导致迁移直接报错或永不收敛：

1. **B1（知识去重不收敛）**：`mergeDuplicateKnowledge` → `reingestUserKnowledge` → `Add` 对已存在的重复行只合并 tags，**从不删除重复行**，迁移归拢后同 user 的重复 knowledge 条目不会收敛为一条，设计测试 M1/I5 的「合并为 1 条」断言必失败。
2. **B2（preferences 迁移撞 UNIQUE 约束）**：迁移 `UPDATE preferences SET user_id=u WHERE user_id=uid` 在「两个被合并 session 有同 (category,key) 偏好」时违反 `UNIQUE(user_id, category, key)`，迁移报错中断——而这正是迁移的核心目标场景（设计自己的 I3 语义）。
3. **B3（双 uid 源漂移）**：工具/L1 用内存解析的 `userID`，ingest 用 `metadata.user_id`——两个来源。`DefaultUserID` 变更或跨重启 `WithUserID` 不一致时（`INSERT OR IGNORE` 固化旧值），同一 session 的读写分裂到两个 user，记忆分裂。
4. **B4（迁移被 `Phase2Merge` 开关错误耦合）**：迁移挂 `runPhase2Merge`，但 `runPhase2Loop` 只在 `Phase2Merge=true` 时启动（`subsystem.go:110,322`）。`UserMergeEnabled=true` + `Phase2Merge=false` 时，新写入走 user 全局、旧数据永不迁移，永久碎片化，且无任何报错。

另有一个事实引用错误（`dino/mem/ingest_meta.go` 应为 `pkg/memkit/sqlite/ingest_meta.go`，§3.3）和一处「memkit 层几乎零改动」的过度承诺（Step 4 已要求改 `sqlite_knowledge.go`）。

**修复 B1-B4 后再进实现**；Step 1-2（uid 打通 + 检索）的接口方向可保留，但 B3 要求工具层 uid 来源改为读 metadata，会影响 Step 1 的工具接线设计。

---

## 1. 事实核查（逐条 pass/fail）

| # | 设计引用 | 真实代码 | 结论 |
|---|---|---|---|
| F1 | `getGlobalUserID` ingest.go:270-278 读 metadata、回退 sessionID | `dino/mem/ingest.go:270-278`，`SELECT value FROM metadata WHERE session_id=? AND key='user_id'`，无则返 sessionID | ✅ PASS |
| F2 | 全仓无任何代码写 `metadata.user_id` 键；metadata 唯一写入是 summary（chatstore:182,311） | grep 确认 metadata 写仅 `chatstore/sqlite.go:182`（legacy 迁移）、`:311`（compact）写 summary；`:354` 与 `admin.go:319` 是 DELETE；无 `user_id` 写入方 | ✅ PASS |
| F3 | metadata 表结构 `(session_id,key,value)` 主键 `(session_id,key)` | `dino/chatstore/sqlite.go:118-123` | ✅ PASS |
| F4 | ingest 以 session 为 uid：`:101` getGlobalUserID、`:218` SetUserPreference、`:222` AddKnowledgeWithCategory | 逐行吻合 | ✅ PASS |
| F5 | 工具以 session 为 uid：tool.go:215、:241 Search、:244 recordSearchResults、:232/:236/:249/:253 | 逐行吻合 | ✅ PASS |
| F6 | L1 注入 factory.go:527 `BuildLayeredPrompt(ctx, sessionID)` | `dino/factory.go:527` | ✅ PASS |
| F7 | `BuildLayeredPrompt` 定义 subsystem.go:189；`MemoryToolsForSession` subsystem.go:154-169 | 逐行吻合 | ✅ PASS |
| F8 | 记忆工具构造 factory.go:577 | `dino/factory.go:577` | ✅ PASS |
| F9 | 检索 `WHERE user_id = ?`：sqlite_knowledge.go:130、sqlite_preference.go:61,78 | 逐行吻合 | ✅ PASS |
| F10 | 内容级去重 sqlite_knowledge.go:38「同 user 下同内容自动合并 tags」 | `Add` 去重分支确实只合并 tags（:44-46）。**但注意**：这只对「INSERT 时发现已存在」成立；对「表里已存在的两条重复」**不收敛**（见 B1） | ⚠️ PARTIAL |
| F11 | preference 覆盖 sqlite_preference.go:47 `INSERT OR REPLACE`；约束 sqlite.go:77 `UNIQUE(user_id, category, key)` | 逐行吻合 | ✅ PASS |
| F12 | Phase 2 按 user 遍历：subsystem.go:307/:335/:498/:364；锁 phase2.go:30 `TryClaimPhase2` | 逐行吻合；`mergeDuplicateKnowledge` 与 `llmConflictMerge` 均 `SELECT DISTINCT user_id FROM knowledge` | ✅ PASS |
| F13 | session 无 user 概念：session.go:42-56 Config | Config 实际在 `session.go:25-34`（行号偏），但确实无 UserID 字段 | ✅ PASS（行号小偏） |
| F14 | `CreateSession(ctx, sessionID, opts...)` factory.go:502；调用方 examples/main.go:142、client.go:60、factory.go:795 | 逐行吻合 | ✅ PASS |
| F15 | 引用反馈：usage_feedback.go:71 `ObserveAssistantFeedback(ctx, sessionID, text)`、:38 `recordSearchResults` | 逐行吻合 | ✅ PASS |
| F16 | `RecordKnowledgeUse(id)` sqlite_knowledge.go:250 按 id 计数 | 逐行吻合 | ✅ PASS |
| F17 | `MemLongTermConfig` types.go:42-58；config.go:68 别名；config.go:231 `Enabled: false` 默认 | 逐行吻合 | ✅ PASS |
| F18 | cursor 主键 session_id：`memory_ingest_cursor`（ingest_meta.go） | 表结构在 `pkg/memkit/sqlite/sqlite.go:160-163`，**但文件是 `pkg/memkit/sqlite/ingest_meta.go` 而非 `dino/mem/ingest_meta.go`** | ❌ FAIL（路径错；内容 PASS） |
| F19 | `context` 表 sqlite.go:97-111 有 user_id 列、主键 session_id | `pkg/memkit/sqlite/sqlite.go:97-111` | ✅ PASS |
| F20 | Phase 2 冷却 6h、租约 10min（§2.4/§4.5 隐含） | `phase2.go:23-24` `Phase2Lease=10min`、`Phase2Cooldown=6h` | ✅ PASS |
| F21 | 「memkit 层几乎零改动」 | Step 4 已明确改 `sqlite_knowledge.go` `Add`；B1 还需改去重/删除逻辑。**零改动承诺与改动清单自相矛盾** | ❌ FAIL（过度承诺） |
| F22 | 迁移只动 knowledge/preferences，不动 context/indexes/page_index | 表结构支持此边界划分 | ✅ PASS |

**事实核查小计：22 项核实，18 项 PASS，2 项 PARTIAL/FAIL（F10 语义、F18 路径），2 项 FAIL（F18 文件路径、F21 过度承诺）。整体事实基础扎实，核心行号全部准确。**

---

## 2. BLOCKER（必须先改再落地）

### B1 —— `mergeDuplicateKnowledge` 不收敛重复，迁移「合并为 1 条」的承诺落空

**证据**：`dino/mem/subsystem.go:529-560` `reingestUserKnowledge` 对每 user 的全部条目逐个调 `mgr.AddKnowledgeWithCategory(ctx, uid, it.content, ...)`；而 `Add`（`pkg/memkit/sqlite/sqlite_knowledge.go:36-47`）去重分支只做
```sql
UPDATE knowledge SET tags = ?, updated_at = ? WHERE id = ?
```
——**只合并 tags、从不删除重复行，也不插入新行**。因此对迁移归拢后同 user 存在的两条相同内容 R1/R2：
- `Add(R1)` → 命中 R1，合并 tags；
- `Add(R2)` → 命中 R1（同 user 同内容），再次合并 tags；
- **R2 仍在表中**。行数不变。

**影响**：设计 §2.1 表第 1 行「跨 session 同 content 的 knowledge … 收敛为一条」、§4.3 第 4 步「交给随后的 mergeDuplicateKnowledge 用现有 Add 去重逻辑收敛」、§4.4「一个 tick 内完成归拢 + 去重」、测试 I5/M1 的「合并为 1 条」断言——**全部不成立**。`mergeDuplicateKnowledge` 对已存在的重复是 no-op，只会反复刷 `updated_at`。

注：`Add` 的去重在**新写入路径**有效（A 写 X、B 写 X → B 的 INSERT 前查重，只更新 A 的行，不产生第二行，I2 能过）。坏掉的只有**已存在重复的收敛路径**（正是迁移场景）。

**修法**：`mergeDuplicateKnowledge` 需对重复行真正删除（去重时保留一行、删除其余，tags 并入保留行）；或迁移归拢时直接按 `(user_id, normalized_content)` 分组去重。测试 M1/I5 应改为断言「行数减少到 1 且 tags 合并」，而不是依赖 `Add` 的副作用。

### B2 —— preferences 迁移撞 `UNIQUE(user_id, category, key)`，迁移直接报错

**证据**：设计 §4.3 第 3 步
```sql
UPDATE preferences SET user_id = u WHERE user_id = uid
```
而 `preferences` 表有 `UNIQUE(user_id, category, key)`（`pkg/memkit/sqlite/sqlite.go:77`）。若被合并的两个 session（`sessA`、`sessB`，均归属 u）各有一条 `(category, key)` 相同、value 不同的 preference——**这正是 per-session 时代的常态、也是 user 合并要覆盖的核心场景（设计自己的 I3）**——则：
- 处理 `sessA` 候选：`UPDATE ... SET user_id=u` 成功；
- 处理 `sessB` 候选：`UPDATE ... SET user_id=u` → `(u, cat, key)` 已存在 → **UNIQUE violation → 迁移报错**。

设计 §4.5 的「任一失败返回错误，Phase 2 记日志，下一 tick 重试」意味着该 session 的偏好**永远迁不过去**（每次都在同一行撞约束），且 `sessB` 之前的 session 归拢也随整个 tick 失败被丢弃——「半途中断无中间态损坏」的承诺对 preferences 不成立。

**修法**：preferences 迁移不能裸 `UPDATE`，应按设计 §2.2 的「后写覆盖」语义做 `INSERT OR REPLACE ... SELECT`（或先 `DELETE ... WHERE user_id=uid` 再 `INSERT`），让后迁移的覆盖先迁移的。测试需补：跨 session 同 `(cat,key)` 的迁移用例（现测试清单只有 knowledge 的 M1）。

### B3 —— 工具/L1 与 ingest 使用两个 uid 来源，跨重启漂移导致记忆分裂

**证据**：
- 工具/L1：设计 §1.6 factory 内 `userID = mem.ResolveUserID(sessionCfg.UserID, f.config.LongTermMemory.DefaultUserID)`，`WithUserID(userID)` 传内存解析值；§1.7 工具 `uid := t.userID; if "" → t.sessionID`。
- ingest：`getGlobalUserID`（`ingest.go:270`）读 `metadata.user_id`，无则 sessionID。

两个来源在 session 创建瞬间相等（`SetSessionUser` 写的就是解析值）。但 `INSERT OR IGNORE` 固化后**不再跟随解析值变化**：
- 场景 A：进程重启 + `DefaultUserID` 从 `"u1"` 改为 `"u2"` → 工具/L1 用 `u2`，ingest 读 metadata 仍 `u1` → 同一 session 的读写在两个 user 间分裂；
- 场景 B：`UserMergeEnabled` 开启前创建的 session 无 metadata → `SetSessionUser` 首次写 `u`，但 `getGlobalUserID` 若在写之前被 ingest 读到（含失败路径 `_ = SetSessionUser` 吞错）→ 落到 sessionID；
- 场景 C：设计 §9.2 待定点 1 明确「所有调用方都不传 `WithUserID`」——一旦未来部分调用方传、部分不传，同 sessionID 的归属随重启漂移。

**影响**：设计风险表「user 身份错误」列的中等风险实际是结构性风险——归属不在单一事实源。`getGlobalUserID` 的「已能读、自动生效」只对 ingest 成立，工具层没复用。

**修法**：工具/L1 的 uid 也应以 **metadata 为准**（复用 `getGlobalUserID` 语义：读 metadata → 无则 sessionID），内存解析值只用于 `SetSessionUser` 的**首次写入**；`MemoryToolsForSession` 构造时读一次 metadata 缓存到 tool 字段，而非每次 Execute 查库。这样工具/ingest/L1 三处永远同源。

### B4 —— 迁移被 `Phase2Merge` 开关错误耦合，配置组合下静默失效

**证据**：迁移钩子挂在 `runPhase2Merge`（设计 §4.4），但 `runPhase2Loop` 只在 `cfg.Phase2Merge` 时启动（`dino/mem/subsystem.go:110`），tick 内 `!cfg.Phase2Merge` 直接 `continue`（`:322`）。因此：

| `UserMergeEnabled` | `Phase2Merge` | 行为 |
|---|---|---|
| true | true（默认） | 正常：新写 user 全局 + 迁移 |
| **true** | **false** | **新写走 user 全局，旧数据永不迁移，无任何日志/报错，永久碎片化** |

**影响**：设计「默认关 + 回退 = 零破坏」的承诺只覆盖 `UserMergeEnabled=false`，未覆盖 `UserMergeEnabled=true && Phase2Merge=false` 这个用户可能配置出的静默坏状态。且「迁移有数据归拢副作用，需显式开启」的前提是迁移真会跑。

**修法**：迁移要么独立成 tick（不依赖 `Phase2Merge`），要么在 config 交互矩阵里明确「`UserMergeEnabled=true` 要求 `Phase2Merge=true`」并加启动时校验/警告。

---

## 3. RECOMMENDED（落地时采纳）

### R1 —— 去重归一化 SQL 与 Go 不一致，含连续空白的内容绕过去重
`pkg/memkit/sqlite/sqlite_knowledge.go:38` 的 SQL 是 `LOWER(REPLACE(REPLACE(REPLACE(REPLACE(content,' ',' '),'\t',' '),'\n',' '),'\r',' '))`——`REPLACE(content,' ',' ')` 是**空操作**，且不折叠连续空白、不 TrimSpace；Go 侧 `normalizeContentForDedup`（:72-89）会 `TrimSpace + ToLower + 折叠连续空格`。两者对「hello  world」（双空格）、首尾空白不一致 → 去重 miss → 插入重复行。对迁移收敛（B1 修法）尤其重要。**修法**：SQL 侧与 Go 侧用同一归一化，或在 Go 侧做归一去重。

### R2 —— `updated_at` 刷平还会连带杀死 `PruneUnused`，Step 4 修法应改方向
设计 §2.3/Step 4 已正确识别 `reingestUserKnowledge` 每次 Phase 2 把全部条目 `updated_at` 刷成 now（L1 按 `updated_at DESC` 选条失真）。但**不止于此**：`PruneUnused`（`phase2.go:75-92`）按 `updated_at < cutoff AND usage_count=0` 删除——每次 Phase 2 刷平后 `updated_at` 全部 > cutoff，**永不剪枝**。设计建议的「`Add` 去重分支 `updated_at = MIN(updated_at, now)`」方向不对：正常新写入同内容**应该**刷新新鲜度，只有 reingest 不该刷。**修法**：`reingestUserKnowledge` 走专门的「仅合并 tags、保留原 `updated_at`」路径，与正常 `Add` 分离；或 `Add` 去重分支改为 `updated_at = MAX(updated_at, now)` + reingest 不调用 `Add` 的刷新分支。

### R3 —— 快照恢复路径（factory.go:795）创建的 session 无归属，落进 `"default"` 桶
`RestoreSessionSnapshot`（`dino/factory.go:795`）调 `f.CreateSession(ctx, sessionID)` **不带任何 option**。`UserMergeEnabled=true` 时该 session 归属 `"default"`。若主流程用 `WithUserID("u1")`，恢复的 session 写进 `"default"`——与「归属创建时固化」的设计冲突，且可能把用户数据写进默认桶。**修法**：快照恢复路径同样走 resolve + `SetSessionUser`（或快照携带 userID）。

### R4 —— 迁移「uid 即 sessionID」启发式可能与真实 user_id 命名空间碰撞
`MigrateLegacySessionKnowledge` 对 `SELECT DISTINCT user_id FROM knowledge` 的每个候选查 `metadata(session_id=?, 'user_id')`。若一个真实多租户 uid 恰好等于某个 session 的 id（`sess_xyz`），且该 session 有归属，迁移会把**该真实 user 的整桶数据**改写到别人的 user——无 guard。**修法**：迁移前过滤「metadata 里同时以该 uid 为 user_id 的真实用户」或加显式白名单；至少文档明确 user_id 与 sessionID 命名空间隔离。

### R5 —— `Client.CreateSession` 不暴露 `WithUserID`，待定点 1 需要先定死
设计 §9.2 待定点 1 正确识别所有调用方不传 `WithUserID`。若不解决，多租户只能靠 `DefaultUserID`，且 dino 层（`dino/types.go:35` 已 `type SessionOption = session.Option`）需要补 `dino.WithUserID` 转发封装，否则上层无法透传。**建议**：本次实现只支持 `DefaultUserID`（单用户），`WithUserID` 透传列为独立后续；同时把「多租户必须显式 WithUserID、`"default"` 兜底仅限单用户」写进 config 文档。

### R6 —— 迁移目标去重的边界（B1 修法依赖）与 `updated_at` 排序的联动
B1 修法若在迁移归拢后立即对同 user 去重，去重的「保留行」选取会影响 `updated_at`（保留新的 vs 旧的）。建议保留行取 `updated_at` 最新、tags 并集——与 L1 选条语义一致。

### R7 —— `search_indexes` / `page_index` 未迁移，user 全局检索会漏旧索引
设计 §9.1 承认 `search_indexes` 默认不暴露、影响面小。但开启 `ExposeSearchIndexes=true` 时，旧 session 的 index（`user_id=sessionID`）在 user 全局检索下永远搜不到（`sqlite_page_index.go:113` `WHERE user_id=?`）。**建议**：文档写明，或迁移时一并处理（indexes 有 `UNIQUE(user_id, source_id)`，同样有 B2 式的约束碰撞）。

### R8 —— `SetSessionUser` 吞错（`_ =`）会立刻造成工具/ingest 不同源
设计 §1.6 `_ = f.longTermMem.SetSessionUser(...)`。若写失败，工具/L1 用解析 uid、ingest 回退 sessionID——**当次即分裂**（不依赖重启/配置变更）。**修法**：失败时至少记日志；更稳的是让工具层也从 metadata 读（B3 修法顺带覆盖）。

---

## 4. OPTIONAL

- **O1**：`llmConflictMerge` 的 `Add`-then-`Delete(drop.id)` 顺序（`subsystem.go:426-433`）：当 keep/drop 内容**文本完全一致**（语义重复=精确重复）时，`Add(keep.content, drop.tags)` 先去重到 drop 行并合并 tags，随后 `Delete(drop.id)` 把合并结果删掉——保留行（keep）tags 未更新、drop 的 tags 丢失。仅影响精确重复+LLM 判定合并的边界，且是既有代码问题。
- **O2**：`memory_stats` 在 user 全局下返回整个 user 的条目数，工具描述建议同步说明「全局记忆」。
- **O3**：`forget_knowledge` 按全局 id 删除，一个 session 可以删除另一 session 写入的知识——这是全局合并的意图，但值得在工具描述里写清。
- **O4**：`ObserveAssistantFeedback` 的 probe 按 session 隔离正确；但 user 全局下 session A 的搜索把 B 的记忆条目放进 A 的 probe，A 的输出引用 B 的条目会计到 B 条目上——符合全局语义，无需改。
- **O5**：`dino/chatstore/sqlite.go:354`、`admin.go:319` 的 `DELETE FROM metadata WHERE session_id=?` 会连 `user_id` 键一起删——删除 session 后归属消失、下次创建重新归属，行为可接受，但值得在文档说明。

---

## 5. 总体评价

### 正确性
- **现状判断准确**：per-session 语义、无 `user_id` 写入方、memkit per-user 键控——全部核实通过。设计对「为什么换 uid 就够」的解释是自洽的。
- **核心逻辑漏洞集中在迁移收敛**：B1（去重不收敛）+ B2（preferences 撞约束）直接击穿「归拢 + 去重」这一卖点；B3 是结构性的双源漂移；B4 是配置组合的静默失效。四个 BLOCKER 都在设计「认为自动获得的能力」处——说明设计高估了 memkit 现有合并逻辑的完备性，没有把 `Add` 的「只合并不删除」和 preferences 的 `UNIQUE` 约束当作迁移的前提来核对。
- 已正确识别的已知缺陷（`updated_at` 刷平）真实存在，但**低估了连带影响**（剪枝失效，R2）。

### 可行性
- 接口签名、配置字段、测试清单、迁移顺序齐全且细化到行号，工程上可执行。
- 但「memkit 层几乎零改动」不实：Step 4 已改 `sqlite_knowledge.go`，B1/B2 修法还要再动去重与迁移 SQL。改动面比设计声称的大一档，主要落在 `pkg/memkit/sqlite`。
- 回滚性：默认关 = 零破坏，成立；但 `UserMergeEnabled=true` 后迁移有归拢副作用，设计已意识到（§5.1），可接受。

### 覆盖度
- 评审重点 1（现状）✅、2（归属策略）✅、3（合并语义）⚠️（收敛承诺虚标）、4（检索改造）✅、5（迁移）⚠️（B2 撞约束未覆盖）、6（风险）⚠️（双源漂移未列入风险表）、7（可行性）✅、8（结论）❌（需改后再进实现）。

### 工作量估计（含 B1-B4 修法与测试）
| 部分 | 估计 |
|---|---|
| Step 1-2 uid 打通（session/types/subsystem/tool/factory） | 0.5–1 天 |
| B3 单源改造（工具读 metadata） | 0.5–1 天 |
| Step 3 迁移 + B1/B2 修法（去重删除、preferences REPLACE） | 1.5–2.5 天 |
| Step 4 `updated_at` + 剪枝（R2） | 0.5–1 天 |
| 集成/迁移测试（I1-I9、M1-M4 修正版） | 1–1.5 天 |
| **合计** | **约 4–7 人日** |

### 结论
**设计总体扎实、方向正确，事实基础 95% 可靠，但 4 个 BLOCKER 集中在迁移/合并核心，需先改设计（B1-B4）再进入实现。** 建议修法：
1. `mergeDuplicateKnowledge` 改为真去重（删重复行）；
2. preferences 迁移改 `INSERT OR REPLACE`（后写覆盖）语义；
3. 工具/L1/ingest 统一以 metadata 为 uid 单一事实源；
4. 迁移独立于 `Phase2Merge` 开关或加配置互斥校验。

修复后按设计 §7 的 Step 1-4 推进即可；Step 1-2 的接口方向可直接复用。

---
*评审完成：BLOCKER 4 / RECOMMENDED 8 / OPTIONAL 5 · 事实核查 22 项（18 PASS / 1 PARTIAL / 3 FAIL）· 结论：需改 B1-B4 后再进入实现*
