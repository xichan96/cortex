# 通用智能体框架 · 详细设计（借鉴 pi）

> 参考实现：[`pi`](file:///Users/talentc/code/ai/pi)（TypeScript monorepo）。本文只设计，不改业务代码；落地时按 §11 分阶段推进。
>
> 目标：把 cortex 从“一套可用的 agent 编排库”收敛为“通用智能体框架”——同一套会话核心可被进程内 SDK、HTTP/SSE、WebSocket/RPC、MCP、CLI 复用；工具、审批、插件、技能、模型、存储都有稳定边界。
>
> 原则：**能包装就不重写**。cortex 已有的审批、预算、压缩、长期记忆、deferred 工具、Anthropic 缓存不要推翻，只做收口和补齐。

---

## 0. 设计摘要

| 维度 | 结论 | 优先级 |
|---|---|---|
| Agent home | 新增 `~/.cortex` + `<cwd>/.cortex` 两级目录，配置/资源/context/信任分层 | P0 |
| 会话记录 | JSONL append-only 事件树作为 source of truth，SQLite 退为索引 | P1 |
| 工具调用 | 单一管线（prepare → validate → authorize → before → execute → after → sanitize → record） | P0 |
| 审批 | 定位为管线中的 authorize gate；policy 与 UI bridge 分离 | P0 |
| Skill | 对齐 agentskills.io，正文懒加载，约束真正执行 | P0 |
| 插件 | 进程内 Go Extension + 进程外 stdio JSON-RPC 插件 | P0 设计 / P1 实现 |
| LLM | API / Provider / Catalog 分层 + 统一流事件 + 增量工具参数 | P1 |
| 运行时 | 一个 session core，多 transport；区分 done 与 settled | P1 |
| 远端执行 | `ExecutionEnv`（local/ssh/docker），工具在远端、模型与凭据在本地 | P2 |
| codemode | 脚本调工具，嵌套调用不进上下文；Go 用 goja | P2 |
| durable | 任务状态机 + sleeping/waiting + 信号唤醒 + requestId 幂等 | P2 |
| telemetry/evals | 供应商中立 span + 预注册 cohort/配对报告 | P2 |

**不借鉴**：TUI/themes/keybindings、jiti/ESM 别名、TypeBox、npm/git 安装管道、40 家 provider 生成目录、`context_edit` 溢出恢复的边缘逻辑、pi 的“只做 trust、不做执行审批”安全姿态、chord 应用运行时。

---

## 1. 现状与差距（基于当前 HEAD）

### 1.1 已有能力（不要重做）

- `agent/engine`：迭代循环、按 `ToolMetadata.Dependencies` 拓扑分层、`errgroup` 并发、`MaxToolCallsPerRound`、超时/重试、LRU 缓存、doom loop。
- `agent/types`：多模态 `Message`、`Usage`、`ToolExposure`（direct/deferred/hidden）、`FatalToolError`、`TruncateMiddle` + spill。
- `dino/permission`：allow/deny/ask ruleset + AgentMode；`dino/defined_tool.go` 的 `ApprovalStore` 已支持 request id、流式事件、无限等待。
- `agent/skills`：SKILL.md + YAML frontmatter，按需返回正文。
- `agent/model/providers/anthropic`：`cache_control` 断点、usage 回填。
- `dino/chatstore`：SQLite 持久化、Hybrid LLM 压缩、确定性兜底。
- `dino/mem` + `pkg/memkit`：长期记忆 ingest/合并/分层 prompt。
- `dino/session`：事件输出、queue、planner、question、subagent wake。
- `dino/harness` / `dino/runner`：checkpoint、verify、任务循环。

### 1.2 结构性缺口

| 缺口 | 现状 | 影响 |
|---|---|---|
| 无 home 目录 | 默认 `./dino_sessions/shared_chat.db`，所有会话一张表 | 无用户级配置/资源/信任/会话分区 |
| 无 append-only 会话日志 | SQLite 行 + 可选 snapshot | 无分支/fork/回放；压缩后原始历史不可重建 |
| 工具管线分散 | engine 执行、dino wrapper、permission、approval 各管一段 | 审批/改写/截断顺序不唯一，嵌套调用绕过 |
| 无插件运行时 | 扩展 = 改 Go 代码 | 通用框架无法让第三方扩展工具/模型/UI |
| Skill 正文启动全量读 | `LoadSkillsFromDirs` 读入 `Content` | 大技能集浪费内存；`paths`/`allowed_tools` 未接线 |
| LLM 无模型目录 | 模型名常量 + `ModelMetadata{Name,Version,MaxTokens}` | 无 context window/pricing/capability/cache 元数据 |
| 无 transport 中性 runtime | `dino.Client` 进程内；HTTP/MCP trigger 绕过 dino | 同一 agent 无法一致地暴露给 SDK/HTTP/WS/CLI |
| 无 sandbox | Bash 以宿主权限执行，仅路径审批 | 远端/多租户场景没有安全边界 |
| 无插件/资源包分发 | 无 | 无法共享 skill/prompt/agent/MCP 组合 |

---

## 2. Agent home：目录、配置与信任

### 2.1 目录树

用户级 `<agent-dir>` 默认 `~/.cortex`，可用 `CORTEX_HOME` 或 `--cortex-dir` 覆盖。

```text
~/.cortex/
  settings.json            # 用户默认：模型、工具、压缩、资源路径、包声明
  models.json              # provider endpoint、模型覆盖
  models-store.json        # 动态刷新目录的缓存
  mcp.json                 # 全局 MCP server
  mcp-auth.json            # MCP OAuth token
  auth.json                # API key / OAuth（0600 + 文件锁）
  trust.json               # 规范路径 -> true/false；trust.json.lock
  permissions.json         # 记住的审批 scope（workspace/always）
  AGENTS.override.md       # 用户全局指令（也接受 AGENTS.md / CORTEX.md / CLAUDE.md）
  SYSTEM.md                # 替换默认 system prompt
  APPEND_SYSTEM.md         # 追加 system prompt
  skills/<name>/SKILL.md + scripts/ references/ assets/
  prompts/*.md             # 斜杠命令模板
  agents/*.md              # 子智能体定义
  plugins/                 # 插件与包安装、缓存、marketplace
    installed.json
    cache/
    store/
  memory/                  # 用户级长期记忆
  sessions/--<encoded-cwd>--/<timestamp>_<session-id>.jsonl
  cache/                   # 工具缓存、web 缓存、模型目录
  traces/                  # 可选 JSONL trace
  logs/
  backups/
  file-history/<session>/  # 可选文件快照
  tmp/
  bin/                     # 托管二进制（rg/fd 等）
```

项目级 `<cwd>/.cortex/` 只放与项目绑定的配置和资源，**只查当前 cwd，不向上查找**：

```text
<cwd>/.cortex/
  settings.json
  mcp.json
  SYSTEM.md
  APPEND_SYSTEM.md
  skills/
  prompts/
  agents/
  plugins/
  memory/                  # 项目级长期记忆
<cwd>/AGENTS.md            # 项目 context（也接受 CORTEX.md / CLAUDE.md）
```

`.agents/skills` 是跨工具约定：从 cwd 向上走到 git repo root（含）收集；`~/.agents/skills` 视为可信用户内容。

### 2.2 配置优先级与合并

```text
CLI flags > 环境变量 > project（trusted）> user > 内置默认
```

- 标量：后者覆盖前者。
- 资源列表（skills/prompts/agents/plugins）：按优先级**合并并去重**，同一身份先出现者生效。
- `models` / `mcp`：按身份合并（`provider/model`、server name），project 覆盖 user。
- 仅全局可设：`default_project_trust`、`http_proxy`、`self_update_command`、`session_dir` 的默认值。project 覆盖这些键直接报错，避免破坏信任模型。
- `session_dir` 在 trust 解析前读取：`--session-dir` > `CORTEX_SESSION_DIR` > settings > 默认。
- 文件格式：默认 `settings.json`；为了兼容现有仓库习惯，`.yaml`/`.yml` 也接受，解析成同一结构。同一目录同时存在时 `.json` 优先并产生 warning。

配置结构（新增 `agent/home`）：

```go
// agent/home/settings.go
type Settings struct {
    DefaultProvider string `json:"default_provider"`
    DefaultModel    string `json:"default_model"`
    DefaultThinking string `json:"default_thinking"`
    Tools           ToolSettings     `json:"tools"`
    Compaction      CompactionSettings `json:"compaction"`
    Resources       ResourceSettings `json:"resources"`
    Packages        []PackageRef     `json:"packages"`
    // 未知键保留，供插件读取；写回时原样带回。
    Extra map[string]json.RawMessage `json:"-"`
}

type ToolSettings struct {
    Default []string `json:"default"` // 纯名字替换；"+x"/"-x" 修改继承选择
    Exclude []string `json:"exclude"`
}

type ResourceSettings struct {
    Skills  []string `json:"skills"`
    Prompts []string `json:"prompts"`
    Agents  []string `json:"agents"`
    Plugins []string `json:"plugins"`
}
```

加载结果带诊断，不因单个文件损坏而失败：

```go
type Layer int
const (LayerBuiltin Layer = iota; LayerUser; LayerProject; LayerEnv; LayerCLI)

type Resolved struct {
    Settings Settings
    Sources  map[string]Layer // 每个键最终来源，供 /config 与排障
    Diagnostics []Diagnostic  // 未知键、非法值、被覆盖项
}
```

### 2.3 context 文件

规则与 pi 一致：

1. 先加载 `<agent-dir>` 的一个全局文件。
2. 再从文件系统根到 cwd，逐目录加载；每目录**只取一个**。
3. 候选顺序：`AGENTS.override.md > AGENTS.md > AGENTS.MD > CORTEX.md > CLAUDE.md > CLAUDE.MD`。
4. `AGENTS.override.md` 只替换**同目录**的 `AGENTS.md`/`CORTEX.md`/`CLAUDE.md`，不抑制其他目录或全局文件。
5. 每个文件以 `<project_instructions path="...">…</project_instructions>` 包裹；BOM 去除；核心层不截断，注入时按 token 预算裁剪并显式标注。
6. context 文件**不需要 project trust**；它们是内容，不是可执行资源。
7. 嵌套 git worktree：worktree 自己的 context 文件遮蔽主仓库同名文件，避免重复注入（按 realpath 比较目录）。

`SYSTEM.md` / `APPEND_SYSTEM.md`：project 版**替换**同名全局版，不合并。

```go
type ContextFile struct {
    Path    string
    Scope   string // "user" | "project"
    Content string
}
func LoadContextFiles(ctx context.Context, p Paths) ([]ContextFile, []Diagnostic, error)
```

### 2.4 trust

trust 只回答一个问题：**是否加载项目提供的可执行资源**；它不限制工具执行，也不是沙箱。

触发 trust 的资源：

- `<cwd>/.cortex/settings.json`、`mcp.json`
- `<cwd>/.cortex/{plugins,skills,prompts,agents}` 存在且非空
- `<cwd>/.cortex/SYSTEM.md`、`APPEND_SYSTEM.md`
- 祖先目录的 `.agents/skills`

决策顺序：

```text
--approve / --no-approve
  > 扩展的 project_trust handler（第一个返回 yes/no 的拥有决定权）
  > trust.json 中最近的祖先决策
  > default_project_trust（默认 ask）
```

非交互模式（HTTP/MCP/JSON/RPC）无法弹窗：`ask`/`never` 一律**不加载**项目资源，`always` 才加载；需要一次例外时显式传 `--approve`。

```go
// agent/home/trust.go
type TrustDecision int
const (TrustUnknown TrustDecision = iota; TrustAllowed; TrustDenied)

type TrustStore interface {
    Lookup(ctx context.Context, canonicalPath string) (TrustDecision, bool, error)
    Save(ctx context.Context, canonicalPath string, d TrustDecision) error
}

type TrustRequest struct {
    CWD     string
    Reasons []string // 触发 trust 的具体资源
    Mode    TrustMode // interactive | non_interactive
}
func ResolveTrust(ctx context.Context, req TrustRequest, store TrustStore, opts TrustOptions) (TrustDecision, error)
```

`trust.json` 使用规范路径（realpath），文件锁串行化；保存父目录信任等价于覆盖其后代，查找时由近及远。

### 2.5 资源发现顺序

| 资源 | 用户 | 项目 | 跨工具 | 包 |
|---|---|---|---|---|
| skills | `~/.cortex/skills` | `.cortex/skills` | `~/.agents/skills`、祖先 `.agents/skills` 到 git root | package `skills/` |
| prompts | `~/.cortex/prompts` | `.cortex/prompts` | — | package `prompts/` |
| agents | `~/.cortex/agents` | `.cortex/agents` | — | package `agents/` |
| plugins | `~/.cortex/plugins` | `.cortex/plugins` | — | package `extensions/` |
| MCP | `~/.cortex/mcp.json` | `.cortex/mcp.json` | — | package `mcp/` |

优先级：CLI 显式路径 > project > user > `.agents` > package > builtin。同名冲突保留首个并产生 warning。

### 2.6 与现有配置的兼容

- `dino.Config` 继续作为**程序化宿主配置**，新增 `home.FromResolved(resolved) (*dino.Config, error)` 由 settings 生成；不删除任何字段。
- 根 `cortex.go` / `internal/app` / `cortex.yaml` 的 legacy 路径已删除；`dino.Config` 仍可由 `cmd/cortex -config` 的 YAML 覆盖，新 CLI 默认读 `settings.json`。
- `dino.SkillsConfig.Paths` 继续可用；未显式配置时由 home 的 skill 根填充。
- `MemoryConfig.PersistDirectory` 继续指向 SQLite；home 只决定默认位置 `~/.cortex/cache/`。

---

## 3. 会话记录、对话存储与记忆

### 3.1 决策

1. **JSONL append-only 事件树是 source of truth**；SQLite 只做索引/查询，不作为唯一真相。
2. 每个会话一个文件：`~/.cortex/sessions/--<encoded-cwd>--/<timestamp>_<session-id>.jsonl`。
3. 条目一旦写入不再修改；分支 = 新的 `parentId`；压缩 = 追加 `compaction` 条目，不删除原始消息。
4. 活跃分支由“文件中最后一条条目”确定；`branch(id)` 只移动内存指针，下一次 append 才落盘。
5. 单写者：每个会话文件加 flock；跨进程读允许，写必须持锁。

### 3.2 Entry 模型

```go
// agent/transcript/entry.go
type EntryType string

const (
    EntrySession       EntryType = "session"
    EntryMessage       EntryType = "message"
    EntryModelChange   EntryType = "model_change"
    EntryThinking      EntryType = "thinking_level_change"
    EntryUsage         EntryType = "usage"
    EntryCompaction    EntryType = "compaction"
    EntryBranchSummary EntryType = "branch_summary"
    EntryCustom        EntryType = "custom"
    EntryCustomMessage EntryType = "custom_message"
    EntryLabel         EntryType = "label"
    EntrySessionInfo   EntryType = "session_info"
)

type EntryBase struct {
    Type      EntryType `json:"type"`
    ID        string    `json:"id"`
    ParentID  *string   `json:"parentId"`
    Timestamp time.Time `json:"timestamp"`
}

type Header struct {
    Type          string    `json:"type"` // "session"
    Version       int       `json:"version"` // 1
    ID            string    `json:"id"`
    Timestamp     time.Time `json:"timestamp"`
    CWD           string    `json:"cwd"`
    ParentSession string    `json:"parentSession,omitempty"`
}

type MessageEntry struct {
    EntryBase
    Message Message `json:"message"`
}

type ModelChangeEntry struct {
    EntryBase
    Provider string `json:"provider"`
    ModelID  string `json:"modelId"`
}

type UsageEntry struct {
    EntryBase
    Kind     string `json:"kind"` // "turn" | "compaction" | "cache_warm" | ...
    Provider string `json:"provider"`
    Model    string `json:"model"`
    Usage    Usage  `json:"usage"`
}

type CompactionEntry struct {
    EntryBase
    Summary          string          `json:"summary"`
    FirstKeptEntryID string          `json:"firstKeptEntryId"`
    TokensBefore     int             `json:"tokensBefore"`
    SystemCheckpoint *SystemMessage  `json:"systemMessage,omitempty"`
    Usage            *Usage          `json:"usage,omitempty"`
    Details          json.RawMessage `json:"details,omitempty"`
}

type BranchSummaryEntry struct {
    EntryBase
    Summary string          `json:"summary"`
    FromID  string          `json:"fromId"`
    Usage   *Usage          `json:"usage,omitempty"`
    Details json.RawMessage `json:"details,omitempty"`
}

type CustomEntry struct {
    EntryBase
    CustomType string          `json:"customType"`
    Data       json.RawMessage `json:"data,omitempty"`
}

type CustomMessageEntry struct {
    EntryBase
    CustomType string    `json:"customType"`
    Content    []ContentBlock `json:"content"`
    Display    bool      `json:"display"`
}

type LabelEntry struct {
    EntryBase
    TargetID string `json:"targetId"`
    Label    string `json:"label,omitempty"`
}

type SessionInfoEntry struct {
    EntryBase
    Name string `json:"name"`
}
```

解码采用“信封 + 自定义 `UnmarshalJSON`”处理判别联合，禁止用 `map[string]any` 直接反序列化消息。

**内容块统一**：新代码使用 `ContentBlock`（`text` / `image` / `thinking` / `tool_call` / `tool_result`），它覆盖现有 `agent/types.MessagePart` 的语义；迁移期 `ContentBlock` 先作为 `MessagePart` 的类型别名/适配器，`Message.Parts` 字段名不变，避免一次性重写所有工具与 UI。`ToolCall`、`Thinking` 等需要跨 provider 回放的字段（`thinkingSignature`、`thoughtSignature`）用 `json.RawMessage` 保留。

### 3.3 写入协议

```go
type Log struct {
    path    string
    f       *os.File
    lock    *flock.File
    header  Header
    entries []Entry
    byID    map[string]Entry
    leafID  string
}

func Open(path string, opts OpenOptions) (*Log, error)
func (l *Log) Append(e Entry) (string, error) // 分配 id、写一行 JSON、更新索引/leaf
func (l *Log) AppendMessage(m Message) (string, error)
func (l *Log) Branch(id string) error
func (l *Log) Close() error
```

- 写一行：`json.Marshal` → `Write` → `Sync`（默认每 turn 同步一次；批量模式可配置）。
- 追加失败：回滚内存索引与 leaf，不留下“内存有、磁盘无”的状态。
- 文件损坏：保留原始片段，报告行号；不要静默丢弃。
- 迁移：读到旧版本 header 时自动改写为新版本，保留 `.bak`。

### 3.4 上下文重建

```go
type BuildOptions struct {
    MaxTokens       int
    IncludeCustom   bool
    SystemCheckpoint bool
}

func (l *Log) BuildContext(opts BuildOptions) ([]Message, BuildReport, error)
```

步骤：

1. 从 leaf 沿 `parentId` 回到 root，得到活动路径。
2. 若路径上有 `compaction`，取最新一个：
   - 先放它的 `SystemCheckpoint`（若存在）；
   - 放 `firstKeptEntryId` 到 compaction 之前的非 system 条目；
   - 放 compaction 之后的条目。
3. 按顺序过滤：`message` → 原消息；`custom_message` → 注入消息；`compaction` → summary 作为 system 消息；`branch_summary` → summary；`usage`/`custom`/`label`/`session_info` → 不进模型上下文。
4. 提取模型/thinking：路径上最后一个 `model_change` / `thinking_level_change`。
5. 可选地按 token 预算裁剪；裁剪点**不得落在 tool call 与其 tool result 之间**。

### 3.5 压缩

触发：

```text
context_tokens > context_window - reserve_tokens
```

在以下时机检查：每轮工具结果追加后（`prepare_next_turn`）、新用户输入前、模型 overflow/`length` 后的一次恢复尝试。

流程：

1. 找到切点：从尾部向前累计 token，保留 `keep_recent_tokens`；切点只能是 user/assistant/自定义消息，**不能切在 tool result**。
2. 若单个 user 回合超过预算，允许在 assistant 消息处切分，并生成“历史摘要 + 回合前缀摘要”后合并。
3. 序列化：`[User]` / `[Assistant]` / `[Assistant tool calls]` / `[Tool result]`，工具结果截断到 2000 字符，thinking 不参与摘要。
4. 追加 `CompactionEntry{summary, firstKeptEntryId, tokensBefore, systemCheckpoint, usage}`。
5. 再次压缩时，从上一个 compaction 的 `firstKeptEntryId` 开始摘要，保留“上次幸存但这次仍超预算”的消息。
6. 文件操作（读/改）从工具调用中提取，跨压缩累计，写入 `details`。

扩展钩子：`session_before_compact` 可取消或提供自定义 summary；`session_compact_failed` 上报失败；branch summary 走 `session_before_tree`。

> 与现状的关系：`dino/chatstore` 的 Hybrid/LLM 摘要与确定性兜底保留为“摘要器实现”，但压缩结果要写入 transcript 的 `CompactionEntry`；SQLite 里的 `metadata.summary` 只做索引/兼容读取。

`context_edit`（把失败尝试从上下文隐藏）暂不实现；overflow 恢复先走“压缩后重试一次”，不做条目级替换。

### 3.6 分支、分叉、导出

- `branch(id)`：移动 leaf；下一次 append 成为该条目的子节点。
- `fork(entryID)` / `clone()`：把 root→目标路径复制到新文件，header 写 `parentSession`。
- `export`：输出当前分支 JSONL 或 HTML。
- `import`：校验 header/ID 后写入新文件，避免覆盖已有会话。
- `list`：按项目目录列出，mtime + header cwd 校验；支持 rename/delete（删除走回收站）。

### 3.7 SQLite 索引（可选）

`~/.cortex/cache/index.db`：

- `sessions(session_id, path, cwd, title, updated_at, parent_id)`
- `messages(rowid, session_id, entry_id, role, text, timestamp)` + FTS5
- `usage(session_id, provider, model, input, output, cache_read, cache_write, cost, timestamp)`
- `labels(entry_id, label)`

索引可随时从 JSONL 重建；删除索引不丢数据。写入路径：transcript append 成功后异步更新；失败只记日志。

### 3.8 记忆分层

| 层 | 位置 | 内容 | 生命周期 |
|---|---|---|---|
| session | transcript + compaction summary | 当前会话事实、摘要 | 会话 |
| project | `<cwd>/.cortex/memory/` | 该仓库的偏好/知识 | 项目 |
| user | `~/.cortex/memory/` | 跨项目的用户偏好 | 用户 |
| static | context 文件 | 人为维护的指令 | 文件 |

- 复用 `dino/mem` 的 ingest/merge 逻辑，把 scope 从 `sessionID` 扩展为 `Scope{Kind, UserID, Workspace, SessionID}`。
- 存储：`shared_chat.db` 增加 `scope_kind/scope_id/workspace` 列；新装用户可用独立 `memory.db`。旧库迁移保留原 session 行为。
- 检索：先 FTS/关键词，再考虑 embedding；`Embedding` 字段暂不启用。
- 注入：system prompt → global context → project context → user memory → project memory → skill 索引 → tool 声明。每层有预算和明确标签，冲突时 project > user > session（当前会话显式陈述最高）。
- 隐私：不存凭据、不存完整工具输出；提供 `forget`/`stats` 工具；user-global merge 默认关闭，开启需显式配置。

### 3.9 snapshot 与 log 的关系

- 新设计里 transcript 是连续日志，不再需要“按需快照”作为主存储。
- `dino/session/snapshot.go` 保留为**导出/恢复**格式：`BuildSnapshot` 从 transcript 投影，`ApplySnapshot` 写入新会话文件。
- `dino/runner` 的 checkpoint blob 不受影响；任务 checkpoint 与会话日志分开。

---

## 4. 工具调用管线

### 4.1 目标

把所有工具相关逻辑收进一条管线，任何工具调用（模型发起、嵌套、脚本发起）都走同一条路径：

```text
resolve → prepare_args → validate → authorize → before_hooks
        → execute → after_hooks → sanitize/limit → record
```

### 4.2 数据模型

```go
// agent/pipeline/types.go
type Call struct {
    ID        string
    Name      string
    RawArgs   json.RawMessage
    Args      map[string]any
    Tool      types.Tool
    SessionID string
    TurnID    string
    ParentID  string // 嵌套调用的父 toolCallID
    StartedAt time.Time
}

type Result struct {
    Content    []types.ContentBlock // 给模型
    Details    any                  // 给 UI/日志
    Structured json.RawMessage      // 给程序调用（outputSchema）
    Usage      *types.Usage
    IsError    bool
    Terminate  bool
    Duration   time.Duration
}

type Action int
const (
    ActionContinue Action = iota
    ActionBlock            // 不执行，返回 error result
    ActionReplace          // 用 Stage 提供的结果替换
)

type Decision struct {
    Action    Action
    Reason    string
    Terminate bool
    Args      map[string]any // 可选：改写参数
    Result    *Result        // ActionReplace 时必填
}

type Stage interface {
    Name() string
    Before(ctx context.Context, c *Call) (Decision, error)
    After(ctx context.Context, c *Call, r *Result) error
}
```

### 4.3 阶段语义

| 阶段 | 实现 | 失败语义 |
|---|---|---|
| resolve | registry 查找，检查 exposure | 未知工具 → error result |
| prepare_args | `Tool.prepareArguments`（新增可选接口） | 失败 → error result |
| validate | JSON Schema + 类型 coercion | 失败 → error result（可恢复，不 fatal） |
| authorize | `PermEvaluator` + approval broker | deny → error result；ask → 阻塞等待；取消 → error result |
| before_hooks | 扩展 `tool_call`；可改参数/block | handler 错误 fail-closed |
| execute | timeout + retry + semaphore | 错误转 `Result.IsError`，不抛 |
| after_hooks | 扩展 `tool_result`；可改 content/details/isError | handler 错误 → error result |
| sanitize/limit | 单一截断入口；spill 到文件 | 不失败 |
| record | transcript/事件/metrics | 记录失败不阻断工具结果 |

**关键规则**

- 参数校验在 authorize 之前；authorize 看到的是校验后的参数。
- hook 改写参数后**必须重新校验**（pi 不复验，是风险）。
- 嵌套工具调用通过 `pipeline.ExecuteOne` 重新走完整管线，`ParentID` 记录父调用；父调用的权限/预算/取消传播到子调用。
- 所有错误都成为 `Result`；只有明确的 `FatalToolErrorKind` 才向上抛，用于终止迭代。

### 4.4 并行调度

```go
type Limits struct {
    MaxConcurrent int           // 默认 max(4, GOMAXPROCS*2)，上限 32
    PerToolTimeout map[string]time.Duration
    DefaultTimeout time.Duration
    Retries        int
}
```

规则：

1. 模型一条 assistant 消息里的多个调用，**prepare 阶段按源顺序串行**（授权可能弹窗，避免并发弹窗）。
2. 若任一工具声明 `ExecutionMode == "sequential"`，整批串行；否则已批准的调用并发执行。
3. `tool_execution_end` 事件按**完成顺序**发出；tool result 消息按**源顺序**追加到上下文。
4. 中途 abort：已开始的调用收到 ctx 取消；未开始的调用合成 `Operation aborted` error result，保证每个 `toolCallID` 都有结果。
5. 单轮调用总数上限沿用 `MaxToolCallsPerRound`；并发上限与总数上限分离。

### 4.5 截断与结果长度

- 单一截断入口：`sanitize/limit` 阶段。先序列化，再按工具的 `MaxTruncationLength`/全局预算截断，保留头尾（`TruncateMiddle`），标记省略字节数；超大输出 spill 到 `~/.cortex/cache/tool-results/<session>/<callID>.txt` 并在结果中给路径。
- 省略标记与 header 计入预算，避免“截断后仍然超长”。
- 模型因 `stopReason=="length"` 截断时，**整批工具调用不执行**，合成错误结果让模型重发；这比执行半个 JSON 安全。

### 4.6 暴露与 tool_search

- `direct`：初始工具列表可见。
- `deferred`：注册可分发，初始不可见；`tool_search` 命中后激活。
- `hidden`：仅引擎内部可分发。
- `codemode`（P2）：只在脚本中可调用，不声明给模型。

`tool_search` 使用 BM25 对 name/description/schema 文本排序；激活结果写入 transcript 的工具集变更条目（见 4.7）。MCP 工具默认 `deferred`，`tool_search` 每次最多加载 N 个，避免一次塞满上下文。

### 4.7 工具集变更入 transcript

工具集是 prompt 的一部分，必须可回放。新增 system 消息增量：

```go
type ToolDeclaration struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    Parameters  json.RawMessage `json:"parameters"`
}

type SystemMessage struct {
    Role        string            `json:"role"` // "system"
    Sections    map[string]string `json:"sections,omitempty"` // 按名字打补丁，nil/空删除
    ToolsAdded   []ToolDeclaration `json:"toolsAdded,omitempty"`
    ToolsRemoved []string          `json:"toolsRemoved,omitempty"`
    Timestamp   time.Time          `json:"timestamp"`
}
```

- 首次请求持久化完整 sections + 工具声明。
- 之后的变化只写增量；回放按顺序应用，得到当前 prompt 与工具集。
- prompt cache 因此稳定：未变化的 section/tool 前缀保持字节一致。
- fork/clone/compaction 都保留这些 system 条目；compaction 的 `systemCheckpoint` 记录压缩边界时的完整 prompt 与工具声明。

### 4.8 工具结果消息

在 `Message` 上扩展：

```go
type Message struct {
    Role       string        `json:"role"`
    Content    string        `json:"content"`
    Parts      []MessagePart `json:"parts,omitempty"`
    ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
    ToolCallID string        `json:"tool_call_id,omitempty"`
    ToolName   string        `json:"tool_name,omitempty"`
    Details    any           `json:"details,omitempty"`
    Structured json.RawMessage `json:"structured,omitempty"`
    IsError    bool          `json:"is_error,omitempty"`
    Usage      Usage         `json:"usage,omitempty"`
    StopReason string        `json:"stop_reason,omitempty"`
    Model      string        `json:"model,omitempty"`
}
```

兼容：旧代码只读 `Content` 仍然工作；新渲染器读 `Parts/Details`。

---

## 5. 审批与权限

### 5.1 概念分离

| 概念 | 回答 | 存放 |
|---|---|---|
| trust | 是否加载项目提供的可执行资源 | `trust.json` |
| exposure | 模型是否看得见工具 | 工具元数据 / settings |
| permission | 这个工具/参数允许、拒绝还是询问 | ruleset + settings |
| approval | 询问时谁来答、答案是否记住 | broker + `permissions.json` |
| sandbox | 即使允许，进程能碰什么 | Docker/SSH/OS sandbox |

**文档与错误信息必须明确：trust 不是 sandbox。**

### 5.2 决策模型

```go
type RiskLevel string
const (RiskLow RiskLevel = "low"; RiskMedium = "medium"; RiskHigh = "high")

type ApprovalScope string
const (
    ScopeOnce     ApprovalScope = "once"
    ScopeSession  ApprovalScope = "session"
    ScopeWorkspace ApprovalScope = "workspace" // 记住到项目
    ScopeAlways   ApprovalScope = "always"     // 记住到用户
)

type ApprovalRequest struct {
    ID         string
    SessionID  string
    TurnID     string
    ToolCallID string
    ToolName   string
    Args       map[string]any
    Risk       RiskLevel
    Rule       string
    Scopes     []ApprovalScope
    Timeout    time.Duration // 0 = 永不超时（手动模式）
}

type ApprovalResponse struct {
    Decision Action // allow | deny
    Scope    ApprovalScope
    Remember bool
    Reason   string
}

type Broker interface {
    Request(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error)
}
```

Broker 实现：

- `CallbackBroker`：宿主直接给函数（现有 `ApprovalSender` 适配）。
- `ChannelBroker`：会话 channel + `Respond(id, approved)`（现有 `ApprovalStore`）。
- `RPCBroker`：通过 transport 发 `approval_request` / 收 `approval_response`。
- `PolicyBroker`：auto-allow/auto-deny 的规则引擎（CI/非交互）。

多个 Broker 用组合器：先查 remembered scope，再问交互 broker，最后落默认策略。

### 5.3 管线位置

authorize 阶段在 validate 之后、execute 之前：

```text
allow   → 继续
deny    → Result{IsError:true, Content:"blocked by policy: <reason>"}
ask     → broker.Request；allow 继续，deny 同上
timeout → Result{IsError:true, Content:"approval timed out"}
取消    → Result{IsError:true, Content:"approval cancelled"}
```

- 默认无 broker 时 **deny**（fail-closed），不允许静默放行。
- 审批拒绝是**可恢复错误**：模型看到原因后可以换方案；只有策略明确要求时才 `Terminate`。
- 嵌套调用同样经过 authorize；子调用继承父调用的 session/turn 和取消信号。

### 5.4 通用 UI bridge

审批、question、plan 共用一套请求/响应：

```text
request:  {id, kind:"approval"|"question"|"plan", session_id, payload, timeout}
response: {id, kind, approved|cancelled|value, remember?}
notify:   {kind:"status"|"notify", payload} // 不等待
```

- 每个请求有 ID；超时可由服务端自动拒绝或交给宿主决定。
- RPC/WS 模式下，`agent_settled` 之前允许挂起；`abort` 要取消所有未决请求。
- 交互层（TUI/Web）只实现 bridge，不直接调 `ApprovalStore`。

### 5.5 scope 持久化

- `once`：仅本次调用。
- `session`：当前会话内存。
- `workspace`：`<cwd>/.cortex/permissions.json`（project trust 后才可写）。
- `always`：`~/.cortex/permissions.json`。
- 记录键：`(tool, pattern, scope)`，例如 `Bash:git push:*`、`write:<abs path>`；写入时带时间戳与来源，支持 `/permissions` 查看/撤销。
- 密钥类操作（auth、ssh key）默认不允许 remember。

### 5.6 外部路径与风险

- `WorkspaceRoot` 是默认可写根；`AllowedReadRoots` 是额外可读根。
- 工作区外写：`ask`；工作区外读：按规则，默认 `ask`（可用 `AllowedReadRoots` 放行技能目录）。
- 废弃 `workspace_root: "*"`：配置校验直接报错，要求显式 `dangerously_allow_all_paths: true` 并产生启动警告。
- 风险分级：工具元数据 `Risk` + 参数模式（`rm -rf`、`git push --force`、写 `.git/`、读 `.env`、网络出口）→ `high`；命中高风险时 scope 只给 `once/session`。

### 5.7 sandbox 后端

```go
type Sandbox interface {
    Name() string
    Run(ctx context.Context, spec ExecSpec) (ExecResult, error)
    ReadFile(ctx context.Context, path string) ([]byte, error)
    WriteFile(ctx context.Context, path string, data []byte, mode fs.FileMode) error
}
```

- `HostSandbox`：现状，明确标记“无隔离”。
- `DockerSandbox`：复用 `pkg/docker`；工作区挂载、只读根、网络策略、资源限制。
- `SSHSandbox`/`ExecutionEnv`：复用 `pkg/ssh`；工具在远端执行，模型与凭据留本地。
- `LandlockSandbox`（Linux，P2）：文件/网络限制，无需容器。
- 配置：`sandbox: {backend: "host"|"docker"|"ssh", ...}`；Bash/file 工具统一从 `Sandbox` 取能力，禁止工具自行 `os/exec`。

### 5.8 与现有实现的映射

| 现有 | 新位置 |
|---|---|
| `agent/utils/permission.go` | `authorize` stage 的规则引擎（保持 API） |
| `dino/defined_tool.go` `ApprovalStore` | `ChannelBroker` 的一种实现 |
| `dino/factory.go` `RespondToolApproval` | `Broker.Respond` + RPC bridge |
| `dino/session/event.go` `EventTypeApproval` | UI bridge 的 `approval_request` 事件；保留兼容字段 |
| `ExternalPathApprovalTool` 包装 | `authorize` 的外部路径规则，不再包工具 |

---

## 6. Skill 系统

### 6.1 规范

以 [agentskills.io](https://agentskills.io/specification) 为可移植核心：

| 字段 | 处理 |
|---|---|
| `name` | 小写字母/数字/连字符，≤64，无首尾/连续连字符 |
| `description` | 必填，≤1024；决定路由 |
| `license` / `compatibility` / `metadata` | 保留 |
| `allowed-tools` | 本 skill 强制展开的回合内生效（见 6.5） |
| `disable-model-invocation` | 只允许 `/skill:name` 显式调用 |

cortex 扩展（`triggers`、`tags`、`priority`、`dependencies`、`paths`、`when_to_use`、`timeout`、`retries`、`fallback`）迁移到 `metadata.cortex`；旧文件仍按顶层字段解析，但产生 deprecation warning。

### 6.2 目录与发现

```text
<skill>/
  SKILL.md
  scripts/
  references/
  assets/
```

- 递归扫描配置根；包含 `SKILL.md` 的目录是根，不再下钻。
- 忽略 `.gitignore`/`.ignore`、点目录、`node_modules`。
- 根目录下的独立 `.md` 文件可作为兼容输入，但不推荐。
- 冲突：首个发现者生效并 warning；name 与目录名不一致不报错（规范允许，但建议一致）。
- 坏文件/无 description：跳过 + 诊断，不阻止启动。

### 6.3 懒加载与 prompt 合同

启动时只加载元数据：

```xml
<available_skills>
  <skill>
    <name>pdf-tools</name>
    <description>Extract text and tables from PDFs. Use when ...</description>
    <location>/Users/me/.cortex/skills/pdf-tools/SKILL.md</location>
  </skill>
</available_skills>
```

正文不进入 system prompt。模型通过：

- `skill` 工具：`{name}` → 返回去掉 frontmatter 的正文，并把 `location/baseDir` 告诉模型；
- `read` 工具：直接读 `SKILL.md`（模型知道路径）；
- `/skill:name args`：强制展开。

展开格式：

```xml
<skill name="pdf-tools" location="/abs/SKILL.md">
References are relative to /abs.
<正文>
</skill>
<args>
```

正文缓存按 `mtime+size` 失效；`/reload` 清缓存。

### 6.4 命令与参数

- `prompts/*.md`：文件名即命令；frontmatter `description`、`argument-hint`；正文是 prompt。
- 参数替换：`$1..$N`、`$@`/`$ARGUMENTS`、`${1:-default}`、`${@:N}`、`${@:N:L}`；只替换一次，不递归。
- 解析顺序：扩展命令 → `input` 事件 → skill 命令 → prompt 模板 → `before_agent_start`。
- 冲突：project > user > package > builtin，首个胜出；不同扩展同名命令加 `/name:1`、`/name:2` 后缀。

### 6.5 约束执行

- `paths`：按当前活跃文件/工作区 Glob 过滤**可广告**的 skill；不匹配的不出现在 `<available_skills>`，但 `/skill:name` 仍可用。
- `allowed_tools`：在 `/skill:name` 或 `skill` 工具激活的回合内，把工具集限制为声明集合（与全局 permission 取交集）；回合结束恢复。若声明了未知工具，启动时 warning，不静默忽略。
- 不允许“声明了但不生效”：要么实现，要么删字段。

### 6.6 生命周期

- 文件变更监听（fsnotify）或 `/reload` 重新扫描；进行中的回合使用旧快照。
- 删除的 skill 从索引移除；正在执行的回合不中断。
- 启动诊断：非法 frontmatter、重复 name、缺失 description、未知字段、`allowed_tools` 未知项。

### 6.7 打包

Skill 作为 package 资源分发（见 7.6）；包内 `skills/` 目录按同一规则发现。环境依赖写在 skill 的 `compatibility`/`metadata`，不自动安装。

### 6.8 与现状的兼容

- `agent/skills.Skill` 保留；新增 `LazyLoader` 和 `Spec` 解析，不破坏 `Registry` API。
- 现有 `PlannerHelper`/LLM 规划路径**废弃**：路由由 description + 模型决定；`ExecutionPlan` 保留给内部编排，不再由额外 LLM 调用生成。
- `Triggers` 只用于 UI/提示，不参与自动执行。

---

## 7. 插件与扩展

### 7.1 两层架构

| 层 | 形态 | 适用 | 隔离 |
|---|---|---|---|
| 进程内 | Go `Extension` 接口 | 核心/可信扩展、需要访问内部类型 | 无，与宿主同权限 |
| 进程外 | 子进程 + stdio JSON-RPC | 第三方、语言无关、可热插拔 | 进程级，可限制环境/凭据 |

不建议 Go `.so` plugin：版本/构建标签/平台问题多，热更新困难。

### 7.2 进程内 Extension API

```go
// agent/extensions/api.go
type Extension interface {
    Name() string
    Init(ctx context.Context, api *API) error
    Shutdown(ctx context.Context) error
}

type API struct {
    Tools     ToolRegistrar
    Commands  CommandRegistrar
    Models    ModelRegistrar
    Resources ResourceRegistrar
    Events    EventBus
    Sessions  SessionAccess
    UI        UserInterface
    Config    json.RawMessage // 扩展自己的配置段
}

type Event string

const (
    EventProjectTrust       Event = "project_trust"
    EventSessionStart       Event = "session_start"
    EventSessionBeforeSwitch Event = "session_before_switch"
    EventSessionBeforeFork  Event = "session_before_fork"
    EventSessionBeforeCompact Event = "session_before_compact"
    EventSessionCompact     Event = "session_compact"
    EventSessionShutdown    Event = "session_shutdown"
    EventAgentStart         Event = "agent_start"
    EventTurnStart          Event = "turn_start"
    EventTurnEnd            Event = "turn_end"
    EventAgentBeforeSettle  Event = "agent_before_settle"
    EventAgentSettled       Event = "agent_settled"
    EventMessageStart       Event = "message_start"
    EventMessageUpdate      Event = "message_update"
    EventMessageEnd         Event = "message_end"
    EventToolCall           Event = "tool_call"
    EventToolResult         Event = "tool_result"
    EventToolExecutionStart Event = "tool_execution_start"
    EventToolExecutionUpdate Event = "tool_execution_update"
    EventToolExecutionEnd   Event = "tool_execution_end"
    EventContext            Event = "context"
    EventBeforeProviderRequest Event = "before_provider_request"
    EventAfterProviderResponse Event = "after_provider_response"
    EventInput              Event = "input"
    EventUserBash           Event = "user_bash"
    EventModelSelect        Event = "model_select"
)

type Handler func(ctx context.Context, ev *EventEnvelope) (EventResult, error)
```

### 7.3 事件分类与顺序

| 类别 | 语义 | 示例 | 结果 |
|---|---|---|---|
| notification | 只观察，不可改 | `agent_start`, `tool_execution_start` | 无 |
| mutation | 原地改对象 | `tool_call` 改 args | 后续 handler 看到修改 |
| transform | 链式转换 | `context`, `message_end` | 上一个输出是下一个输入 |
| gate | 阻断/取消 | `tool_call` block、`project_trust` | 首个决定性结果胜出 |
| boundary | 在边界做一次决策 | `turn_end`, `agent_before_settle` | `continue`/`end` |
| last-override | 后者覆盖 | 少数配置项 | 最后一个非空 |

- 顺序：扩展加载顺序 → 注册顺序；同一扩展内按注册顺序。
- handler 错误隔离并上报；`tool_call` 错误 **fail-closed**；`context` 转换错误回退原值。
- 事件载荷包含 `session_id`、`turn_id`、`tool_call_id`、`source`，便于审计。

### 7.4 注册表、冲突与来源

```go
type Registration struct {
    Name    string
    Source  SourceInfo // builtin | user | project | package:<id> | plugin:<id>
    Version string
    Priority int
}
```

- 同名工具/命令：project > user > package > builtin；扩展可显式 `Override: true` 替换 builtin。
- 注册表记录来源，`/plugins`、`/tools` 可显示“来自哪个包/文件”。
- 移除扩展时一并撤销其注册项；进行中的调用使用快照。

### 7.5 进程外插件协议

传输：子进程 stdin/stdout，长度前缀帧：

```text
u32 length | u8 type | u32 id | u32 json_len | json | raw
type: request | response | error | event | cancel | ping
```

方法：

| 方法 | 方向 | 用途 |
|---|---|---|
| `initialize` | host → plugin | 协议版本、宿主能力、配置 |
| `tools/list` | host → plugin | 列出工具声明 |
| `tools/call` | host → plugin | 执行工具，支持流式 `event` |
| `hooks/emit` | host → plugin | 事件通知/闸门，响应 `Decision` |
| `resources/list` | host → plugin | 插件提供的 skill/prompt/MCP 资源 |
| `shutdown` | host → plugin | 有序退出 |

- 超时、取消（`cancel` + id）、心跳/死亡检测；崩溃后有限次数重启。
- 插件声明 `capabilities`（tools/hooks/resources/ui），宿主只暴露对应 API。
- 凭据按插件隔离：环境变量白名单，不继承宿主全部环境。
- 协议版本协商失败：禁用插件并报告，不影响会话启动。

### 7.6 包与分发

`cortex-pack.json`（目录或包根）：

```json
{
  "name": "acme-tools",
  "version": "1.2.0",
  "cortex": {
    "extensions": ["./extensions/*.go", "./bin/plugin"],
    "skills": ["./skills"],
    "prompts": ["./prompts/*.md"],
    "agents": ["./agents/*.md"],
    "mcp": ["./mcp.json"]
  }
}
```

- 来源：local path、git（tag/commit 固定）、OCI/压缩包（P2）。不直接复用 npm。
- 身份去重：local 用 realpath，git 用 repo URL（不含 ref），包用 name。
- 过滤器：省略 = 全部；`[]` = 无；`!Glob` 排除；`+path`/`-path` 精确包含/排除。
- 安装失败非致命：warning + 跳过 + 下次启动/`/reload` 重试；`cortex install` 返回非零。
- 项目包声明受 trust 门控；安装前打印将要加载的可执行资源。
- 版本/完整性：记录 commit/tag/hash；更新不移动已固定的 ref；可选签名校验（P2）。

### 7.7 信任、隔离与重载

- 进程内扩展：视为可信代码，加载前需要显式配置或包 trust；文档说明与宿主同权限。
- 进程外插件：仍是本机用户权限，但可用容器/低权限用户进一步隔离；提供 `--plugin-sandbox` 后端。
- `/reload`：清资源缓存、停旧进程、重新发现、重新绑定、发 `session_start(reason:"reload")`；旧上下文对象失效。
- 任何扩展/插件都不得静默修改 permission 规则；只能通过 `authorize` 事件提供建议，最终由策略层决定。

### 7.8 与 MCP 的关系

MCP 是**工具/资源提供方**的一种，不是插件机制本身：

- MCP server 注册为 `Source: mcp:<name>` 的工具，默认 `deferred`。
- 进程外插件可以通过 `resources/list` 返回 MCP server 声明，最终仍走 MCP 客户端。
- 插件协议不替代 MCP；MCP 保持标准兼容（stdio/streamable HTTP）。

---

## 8. LLM 层

### 8.1 分层

```text
API（wire protocol）      anthropic-messages / openai-completions / openai-responses / ...
      ↑
Provider（认证/baseURL/模型目录/能力过滤）
      ↑
Catalog（静态模型元数据 + models.json 覆盖 + 动态刷新缓存）
      ↑
ModelRuntime（按请求选择物理模型、虚拟路由、缓存策略）
```

`agent/engine` 只依赖 `ModelRuntime`，不直接依赖任何 provider 包。

### 8.2 模型元数据

```go
// agent/model/model.go
type Model struct {
    ID            string
    Name          string
    Provider      string
    API           API
    BaseURL       string
    Input         []Modality // text, image, audio, pdf
    ContextWindow int
    MaxTokens     int
    Reasoning     bool
    ThinkingLevels map[ThinkingLevel]any // nil/缺席 = 不支持
    PromptCache   *PromptCacheSpec
    Cost          Cost
    Compat        json.RawMessage
    Headers       map[string]string
}

type Cost struct {
    InputPerMillion      float64
    OutputPerMillion     float64
    CacheReadPerMillion  float64
    CacheWritePerMillion float64
    Tiers                []CostTier
}
```

来源：

1. 内置静态表（只覆盖 cortex 主推的几个模型/供应商）。
2. `~/.cortex/models.json` / `.cortex/models.json` 覆盖（按 `provider/model` 合并，字段级）。
3. 动态刷新（`RefreshModels`）写入 `models-store.json`，带 generation/取消；失败保留旧值。

### 8.3 Provider 与 Stream

```go
type Provider interface {
    ID() string
    Models() []Model
    Stream(ctx context.Context, req *Request) (Stream, error)
    Images(ctx context.Context, req *ImageRequest) (ImageResult, error) // 可选
}

type Request struct {
    Model          *Model
    System         []ContentBlock
    Messages       []Message
    Tools          []ToolDeclaration
    Thinking       ThinkingLevel
    MaxTokens      int
    Temperature    *float64
    CacheRetention CacheRetention // none | short | long
    Headers        map[string]string
}

type Stream interface {
    Events() <-chan StreamEvent
    Result() (*AssistantMessage, error) // 流结束后权威消息
    Close() error
}

// Runtime 是 engine 看到的唯一模型入口：选物理模型、注入认证、套缓存策略。
type Runtime struct {
    catalog   Catalog
    providers map[string]Provider
    virtual   map[string]*VirtualModel
}

func (r *Runtime) Stream(ctx context.Context, req *Request) (Stream, error)
func (r *Runtime) Resolve(ctx context.Context, ref ModelRef, reason RouteReason) (*Model, error)
```

约束：

- `Stream` 返回后不得抛异常；错误编码为 `error` 事件 + 最终消息 `stop_reason="error"`。
- provider 内部不做无限重试；transport retry 由 provider 完成，semantic retry 由 engine 完成。
- 不支持的选项**静默降级**（例如 thinking 不支持时忽略），而不是报错；但要在诊断中记录。

### 8.4 统一流事件

```go
type StreamEventType string
const (
    StreamStart         StreamEventType = "start"
    StreamTextStart     StreamEventType = "text_start"
    StreamTextDelta     StreamEventType = "text_delta"
    StreamTextEnd       StreamEventType = "text_end"
    StreamThinkingStart StreamEventType = "thinking_start"
    StreamThinkingDelta StreamEventType = "thinking_delta"
    StreamThinkingEnd   StreamEventType = "thinking_end"
    StreamToolStart     StreamEventType = "toolcall_start"
    StreamToolDelta     StreamEventType = "toolcall_delta"
    StreamToolEnd       StreamEventType = "toolcall_end"
    StreamDone          StreamEventType = "done"
    StreamError         StreamEventType = "error"
)

type StreamEvent struct {
    Type      StreamEventType
    Index     int
    Delta     string
    ToolCall  *ToolCallDelta
    Partial   *AssistantMessage // 可选；transport 可裁剪
    Message   *AssistantMessage // done/error 时权威
    Usage     *Usage
    StopReason StopReason
    Err       *StreamError
}
```

- `text/thinking/toolcall` 都有 start/delta/end；`end` 是权威内容。
- `done.reason ∈ {stop,length,toolUse,deferred}`；`error.reason ∈ {aborted,error}`。
- transport 只发 delta；需要完整快照的客户端自行累积。RPC/JSON 模式默认剥离 `Partial`。

### 8.5 增量工具参数

- `toolcall_start` 带 `id/name`；`toolcall_delta` 是原始 JSON 文本片段；`toolcall_end` 给解析结果。
- 使用增量 JSON 解析器（容错、可中断），流结束后做最终解析。
- 解析失败：`ToolCall.ArgumentsError` 保存错误和原文，`Arguments={}`；validate 阶段返回可恢复错误，让模型重发。
- 不要求所有 provider 都流式参数：一次性 delta 也接受；provider 声明 `supportsToolStream=false` 时，engine 仍按同一事件序列合成。

### 8.6 重试

两级：

1. **transport retry**（provider 内部）：408/409/429/5xx、`retry-after`/`retry-after-ms`、连接中断；尊重 `ctx` 取消；最大延迟上限（默认 60s）。
2. **semantic retry**（engine）：可重试的速率/临时错误，指数退避；终止性错误（配额/账单/鉴权/参数）加入 denylist，不重试。

- overflow 不重试：识别 `context_length_exceeded` 等错误，触发压缩后**重试一次**。
- `stopReason="length"` 不自动续写；工具调用整批失败（见 4.5）。

### 8.7 认证

```go
type CredentialStore interface {
    Resolve(ctx context.Context, provider string) (*Credential, error) // 请求级 > 存储 > 环境
    Refresh(ctx context.Context, provider string) (*Credential, error)
}
```

- `auth.json` 0600 + 文件锁；结构 `{provider: {type:"api_key", key/env} | {type:"oauth", access, refresh, expires}}`。
- 刷新串行化：锁内检查 5 分钟有效窗口；失败不静默回退到环境变量（避免用错账号）。
- 支持 `$ENV` 与 `!command` 取值；`!command` 仅在 trust 的配置中允许。
- OAuth 登录流放在 CLI/RPC 层，provider 只消费凭据。

### 8.8 prompt cache

- 保留现有 Anthropic 实现：system、最后一个 tool、最后一条会话内容块三个断点；≤4 个。
- `CacheRetention`：`none|short|long`；模型元数据声明 TTL 与价格。
- 压缩时保留 head cache anchor（现有 `CompactionPrefix`/`CacheAnchorTokens`），并确保工具集变更以增量形式写入，避免前缀抖动。
- cache warming（idle/streaming）可选：仅当模型声明 TTL 且预计节省超过阈值时启用；用量计入 totals，但不进模型上下文。

### 8.9 虚拟模型（P2）

```go
type RouteReason string
const (RouteUser RouteReason = "user"; RouteContinuation = "continuation"; RouteRetry = "retry"; RouteDirect = "direct")

type RouteRequest struct {
    Reason       RouteReason
    Messages     []Message
    Previous     *Model // 最近一次成功的物理模型
    Failed       *Model // 重试时的失败模型
    State        json.RawMessage
}
type RouteResult struct {
    Model    *Model
    Thinking ThinkingLevel
    State    json.RawMessage
}

type VirtualModel struct {
    ID     string
    Route  func(ctx context.Context, req RouteRequest) (RouteResult, error)
}
```

- 选择（虚拟模型）与派发（物理模型）分离；assistant 消息记录物理模型。
- 路由状态作为 `custom` 条目存在会话分支上，fork/compaction 后仍可用。
- continuation/retry 保持粘性以保护 prompt cache 与 thinking 签名。

### 8.10 与现状的映射

| 现有 | 新位置 |
|---|---|
| `agent/llm/anthropic_native.go` | `agent/model/providers/anthropic`（已迁移，缓存改为 retention 断点） |
| `agent/llm/langchain`（OpenAI-compatible） | `agent/model/providers/openai`（已迁移；DeepSeek/Volce 走同一 `BaseURL` 配置） |
| `agent/types.LLMProvider` | 保留兼容接口；engine 内部改用 `model.Runtime` |
| `ModelMetadata` | 扩展为 `Model`（保留旧字段） |
| `dino.ProviderConfig` | 转为 `models.json` 的一层 |

---

## 9. 运行时与传输

### 9.1 一个 core，多 transport

```text
                 ┌──────────── Session Core ────────────┐
SDK (in-process) │ transcript + engine + pipeline +     │
HTTP/SSE ────────┤ approval broker + extension host     │
WS/RPC ──────────┤ + event bus                          │
CLI (json/rpc) ──┘                                      │
                 └──────────────────────────────────────┘
```

所有 transport 只做 I/O 适配，不复制 agent 逻辑。原 `trigger/http` 与 `trigger/mcp` 已删除；对外入口统一走 `agent/runtime/gateway`（HTTP/SSE + WS）和 `cmd/cortex`（stdio/JSONL）。

### 9.2 事件 schema

```go
type Event struct {
    Type      EventType      `json:"type"`
    SessionID string         `json:"session_id"`
    TurnID    string         `json:"turn_id,omitempty"`
    Seq       int64          `json:"seq"`
    Timestamp time.Time      `json:"timestamp"`
    Payload   json.RawMessage `json:"payload"`
}
```

事件集合：

- 生命周期：`session_start`, `session_end`, `agent_start`, `agent_end`, `agent_settled`
- 回合：`turn_start`, `turn_end`
- 消息：`message_start`, `message_update`（delta-only）, `message_end`（权威）
- 工具：`tool_execution_start/update/end`
- 队列：`queue_update`
- 压缩：`compaction_start`, `compaction_end`, `compaction_failed`
- 重试：`retry_start`, `retry_end`
- 条目：`entry_appended`
- 审批/交互：`approval_request`, `approval_response`, `question`, `plan`
- 状态：`model_changed`, `thinking_level_changed`, `session_info_changed`

`agent_end` 表示“本次 run 结束”；`agent_settled` 表示“没有自动重试、压缩恢复或排队消息待处理”。客户端等待 `settled` 才算真正空闲。

### 9.3 steering / follow-up / abort

- `Prompt`：新一轮输入。流式期间若再调用，必须显式 `steer` 或 `follow_up`，否则返回错误。
- `Steer`：在当前 assistant 回合与工具执行完之后注入，影响下一轮请求。
- `FollowUp`：等 agent 本来要停止时才注入，开启新一轮。
- 队列模式：`all` / `one-at-a-time`。
- `Abort`：取消当前 run，返回未消费的排队消息，等待 idle 后返回。
- 工具调用中途 abort：为每个未完成调用合成结果，保证消息序列完整。

### 9.4 RPC 协议

JSONL（严格 LF 分隔；stdout 只放协议，诊断走 stderr）：

```json
{"id":"1","type":"command","command":"prompt","data":{"text":"hi"}}
{"id":"1","type":"response","command":"prompt","success":true,"data":{"disposition":"started"}}
{"type":"event","event":{"type":"agent_settled"}}
```

命令：

- `prompt` / `steer` / `follow_up` / `abort` / `clear_queue`
- `get_state` / `set_model` / `get_available_models` / `set_thinking`
- `compact` / `get_session_stats` / `get_entries?since=&leaf=`
- `fork` / `clone` / `switch_session` / `new_session`
- `export` / `import` / `set_session_name`
- `Bash` / `abort_bash`
- `get_commands`

响应统一 `{id,type:"response",command,success,data|error}`；解析失败返回 `command:"parse"`。`prompt` 的 `disposition ∈ {started,queued,handled}`。

扩展 UI：

```json
{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"...","timeout":30000}
{"type":"extension_ui_response","id":"ui-1","confirmed":true}
```

### 9.5 SDK

`dino.Client` 演进为薄封装：

```go
type Runtime interface {
    Prompt(ctx context.Context, in PromptInput) (Disposition, error)
    Steer(ctx context.Context, m Message) error
    FollowUp(ctx context.Context, m Message) error
    Abort(ctx context.Context) error
    Subscribe(fn func(Event)) (cancel func())
    State() State
    Entries(ctx context.Context, since string) (entries []Entry, leaf string, err error)
    Settled() <-chan struct{}
    Close(ctx context.Context) error
}
```

- 进程内直接调用；远端通过 RPC 客户端实现同一接口。
- 工具/扩展注入：`Options{Tools, Extensions, Provider, Store}`；SDK 可关闭内建工具。
- 订阅返回取消函数；`Close` 等待 run settle。

### 9.6 session worker（P2，托管场景）

- 一台机器一个 server；server 只存 catalog（session 元数据、归属、状态），worker 存 transcript 与运行态。
- 每会话一个 worker 进程；首次准入时启动，空闲停止；崩溃有限重启。
- 准入在 spawn 前完成（不存在/无权限的会话不启动进程）。
- worker 只拿到 `{dir, session_id}`；通过 live query 跟随自己的行。
- runner 接口支持 local / remote / container；内存预算按 worker 计。
- 该能力只在需要托管多会话/多用户时实现，不影响单机框架。

---

## 10. 模块划分与文件清单

### 10.1 新增包

```text
agent/
  home/            # 目录解析、settings 分层、trust、context 文件
    paths.go
    settings.go
    config.go
    trust.go
    context.go
    env.go
  resources/       # 统一资源发现：skills/prompts/agents/mcp/packages
    loader.go
    precedence.go
    manifest.go
    package.go
  transcript/      # JSONL 事件树、重建、压缩、导出
    entry.go
    log.go
    context.go
    compact.go
    branch.go
    export.go
    index_sqlite.go
  pipeline/        # 工具调用管线
    types.go
    pipeline.go
    schedule.go
    truncate.go
    stages/        # resolve/validate/authorize/execute/sanitize/record
  model/           # catalog/provider/stream/retry/auth/virtual
    model.go
    catalog.go
    provider.go
    stream.go
    retry.go
    auth.go
    virtual.go
    providers/anthropic/
    providers/openai/
  extensions/      # 进程内 API、事件、注册表；进程外协议
    api.go
    events.go
    registry.go
    host.go
    protocol.go
  approval/        # Broker 接口、scope、remembered store（与 transport 无关）
    broker.go
    scope.go
    store.go
  runtime/         # transport 中性 SessionRuntime、事件、RPC 编解码
    runtime.go
    event.go
    rpc.go
    gateway/       # HTTP/SSE + WebSocket 传输（RuntimeHandler）
dino/
  approval/        # ChannelBroker、UI bridge 适配（从 defined_tool 抽离）
  sandbox/         # Host/Docker/SSH backend
cmd/
  cortex/          # CLI：interactive/json/rpc 模式（P1/P2）
```

### 10.2 修改点

| 文件 | 改动 |
|---|---|
| `agent/types/tool.go` | 增加 `prepareArguments`、`Risk`、`ExecutionMode` 可选接口；`ToolExposure` 增加 codemode |
| `agent/types/llm.go` | `Message` 增加 `ToolName/Details/Structured/IsError/StopReason/Model`；`Usage` 增加 cost |
| `agent/engine/agent_execution.go` | 执行路径改为调用 `pipeline`；旧路径保留开关 |
| `agent/engine/agent_config.go` | 从 home/settings 生成；`ToolParallelismLimit` 与 pipeline 共享 |
| `agent/skills/loader.go` | 改为 lazy 元数据加载；正文按需读；保留旧 API 适配 |
| `dino/factory.go` | 用 `home.Resolve` + `resources.Load`；把 wrapper 改为 pipeline stage |
| `dino/defined_tool.go` | `ApprovalStore` 改为 `ChannelBroker`；保留兼容方法 |
| `dino/session/session.go` | 事件映射到 runtime Event；transcript 双写 |
| `dino/chatstore/*` | 保持读取兼容；新增 transcript 镜像；压缩写入 `CompactionEntry` |

### 10.3 依赖方向（防循环）

```text
agent/types                 ← 最底层，不依赖其他 agent 包
agent/home                  ← 只依赖 stdlib + yaml/json
agent/model                 → types
agent/skills                → types
agent/transcript            → types
agent/approval              → types
agent/pipeline              → types, approval
agent/extensions            → types, pipeline, skills, model, transcript(只读)
agent/resources             → types, skills, extensions(只读 manifest)
agent/engine                → types, pipeline, model, skills；通过 Recorder 接口写 transcript
agent/runtime               → engine, transcript, pipeline, extensions, resources
dino/*                      → agent/*；不把 dino 类型塞回 agent
agent/runtime/gateway       → agent/runtime, approval；不直接引用 dino
cmd/*                       → agent/runtime 或 dino
```

约定：

- `agent/engine` 不 import `agent/transcript`；需要记录时用 `types.Recorder`/`types.EventSink` 接口，由 `agent/runtime` 注入实现。
- `agent/extensions` 不 import `agent/engine`；它只依赖抽象（API/Registry/Pipeline），由 runtime 绑定。
- `dino` 可以依赖 `agent`，禁止 `agent` 依赖 `dino`（现有代码已遵守，保持）。
- `trigger` 不得 import `dino`，否则后续 CLI/SDK/托管会绑死。

---

## 11. 落地顺序

### P0（先把“边界”立住，行为不变）

1. **home 目录与配置**
   - 新增 `agent/home`；实现根目录解析、settings 分层、全局键校验、context 文件加载、trust store。
   - `dino.NewDinoFactory` 增加可选 `WithHome(paths)`；未启用时行为与现在完全一致。
   - 验收：优先级矩阵测试；trust 拦截项目资源；context 文件顺序/覆盖/包裹正确；`cmd/cortex -config` 的 YAML 覆盖继续可用（原 `cortex.yaml` legacy 路径已删除）。

2. **工具管线收口**
   - 新增 `agent/pipeline`；把 resolve/prepare/validate/authorize/execute/after/sanitize/record 串起来。
   - 把现有 `wrapWorkspacePathTools`、`nonFatalTool`、`toolResultLimiter`、`NewApprovalTool`、loop detection 改成 stage。
   - 保留 `agent/engine` 旧路径，用 `EnableToolPipeline` 开关切换；先用同一套测试对比新旧行为。
   - 验收：审批顺序、错误可恢复/致命、并发/顺序、截断、嵌套调用矩阵测试全部通过；现有 dino 测试不变绿。

3. **审批 gate 与 UI bridge**
   - `ApprovalStore` 适配为 `Broker`；authorize stage 调用 broker；默认无 broker 时 deny。
   - 增加 scope（once/session/workspace/always）与 `permissions.json`；先只实现 once/session，workspace/always 留接口。
   - 统一 request/response 事件，保留现有 `EventTypeApproval` 字段兼容。
   - 验收：拒绝/超时/取消/无通道/嵌套调用都有确定结果；事件 ID 可回灌。

4. **Skill v2**
   - 按规范解析 frontmatter；cortex 扩展迁移到 `metadata.cortex`，兼容旧字段 + warning。
   - 改为元数据懒加载；正文按需读；`/skill:name` 展开；`paths`/`allowed_tools` 接线。
   - 删除 LLM planner 的调用路径（保留代码一版，标记 deprecated）。
   - 验收：大技能集启动内存/耗时明显下降；非法技能不阻止启动；约束实际生效。

5. **Extension 设计定稿 + 进程内骨架**
   - 落 `agent/extensions` 的 `Extension`/`API`/事件枚举/注册表；先接 2 个内建扩展（如 `tool_search`、`codemode` 占位）验证。
   - 不做动态加载；进程外协议只出 spec 和 conformance 测试骨架。

### P1（换掉“真相源”，补齐对外接口）

6. **transcript**
   - 新增 JSONL 日志/树/重建/压缩边界；`dino/chatstore` 双写（SQLite + JSONL），读取仍走 SQLite。
   - 新增 `export/import/fork/clone`；`session-dir` 配置与目录布局。
   - 灰度：新会话写 JSONL；旧会话读取迁移；对比消息序列一致后，切换 JSONL 为 source of truth。
   - 验收：回放确定性、压缩后上下文一致、fork/import 往返、损坏文件可诊断。

7. **runtime + RPC**
   - 抽出 `SessionRuntime`；`dino.Client` 适配；`agent/runtime/gateway` 实现 RPC server；原 `trigger/http`/`mcp` 已删除。
   - 事件 schema 落地；`agent_settled`；steering/follow-up/abort。
   - 验收：同一会话通过进程内/HTTP/WS 得到一致事件序列；RPC 并发命令按 id 关联；abort 后消息完整。

8. **LLM 层**
   - `agent/model`：Catalog（内置 + models.json）、Provider 接口、统一 StreamEvent、增量 JSON、两级重试、auth store。
   - Anthropic 与 OpenAI-compatible 适配器已完成迁移；`agent/llm` legacy 实现已删除。
   - 验收：同一请求在 anthropic/openai 适配器产生可比较的 StreamEvent；工具参数增量可渲染；重试不重复计费；缓存断点数量正确。

### P2（能力扩展）

9. 进程外插件 host + `cortex-pack` 安装器。
10. `codemode`（goja + journaled tool calls）、`ExecutionEnv`（local/ssh/docker）、durable 信号/幂等。
11. telemetry 契约 + eval harness（预注册 cohort、精确配对、blocked pair）。
12. session worker（托管场景）与多租户鉴权。

---

## 12. 测试策略

| 层 | 用例 |
|---|---|
| home/config | 优先级矩阵、全局键非法覆盖、context 顺序/覆盖、worktree 遮蔽、trust 决策链 |
| transcript | 金样 JSONL、回放确定性、压缩切点（不切 tool result）、fork/import 往返、损坏行诊断 |
| pipeline | 每个 stage 的单测；allow/deny/ask/timeout/cancel；并发/顺序/嵌套；截断/溢出；fatal vs recoverable |
| skill | 规范 frontmatter、冲突、坏文件、懒加载、`/skill:` 展开、`allowed_tools` 生效、热重载 |
| extensions | 事件顺序/分类/错误隔离；冲突覆盖；reload 后旧上下文失效；进程外协议 conformance（崩溃/超时/取消） |
| model | 流事件序列；参数增量与非法 JSON；stop reason；retry 分类；auth 刷新串行；catalog 覆盖合并 |
| runtime | RPC 契约、并发命令、settled、abort 返回队列、事件 delta 重组 |
| e2e | 一个会话从 CLI/HTTP/WS 发起，审批/工具/压缩/恢复全链路；golden trace |

测试原则：默认不调用真实模型；用 faux provider 和 fixture。真实 API 测试单独标记、默认跳过。

---

## 13. 风险与回滚

| 风险 | 缓解 |
|---|---|
| 工具管线重构改变行为 | `EnableToolPipeline` 开关 + 新旧路径差分测试；先只读场景，再写场景 |
| 双写期间数据不一致 | JSONL 写入成功后才更新索引；启动时对比/告警；保留 SQLite 读取回退 |
| prompt cache 回退 | 工具集变更必须增量；压缩保 head anchor；用 cache 命中率指标监控 |
| 配置迁移破坏现有部署 | `dino.Config` 字段不减；`cmd/cortex -config` 的 YAML 覆盖继续可用；原 `cortex.yaml` legacy 路径已删除；home 默认关闭直到 P0 完成 |
| 插件安全 | 进程外默认、环境白名单、无凭据继承、显式 trust；文档不把 trust 当 sandbox |
| skill 约束误伤 | `allowed_tools` 只限制当前回合；未知工具启动 warning；可配置 `strict_skills` |
| JSONL 并发损坏 | flock + 单写者；append 失败回滚；定期校验/备份 |

回滚策略：每个 P 阶段都有 feature flag；transcript 切换前保留 SQLite 读取路径；插件 host 可整体禁用。

---

## 14. 不借鉴清单（明确写死）

- pi 的 TUI 渲染、themes、keybindings、终端 escape 处理。
- jiti/ESM 模块别名、TypeBox schema builder、npm/git 安装管道。
- 40 家 provider 的生成目录与每 provider 一个 adapter；只保留 compat flags + 少量适配器。
- `context_edit`/overflow 条目级替换；先用“压缩后重试一次”。
- pi 的“project trust 但不做执行审批/沙箱”安全姿态；cortex 面向 HTTP/MCP/多用户，必须保留审批并补 sandbox。
- chord 的对象运行时与 p2/p3 的应用层插件（rollups/voice/Discord 等）；只借鉴 session worker/ExecutionEnv 的架构。
- 把 `workspace_root: "*"` 当作便捷开关；改为显式危险选项 + 警告。

---

## 15. 与 pi 的映射速查

| pi | cortex 目标 |
|---|---|
| `~/.pi/agent` + `.pi` | `~/.cortex` + `.cortex` |
| `AGENTS.md`/`CLAUDE.md` context | `AGENTS.md`/`CORTEX.md`/`CLAUDE.md` |
| `sessions/--cwd--/<ts>_<id>.jsonl` | 同构 |
| `CompactionEntry.firstKeptEntryId` | 同构 |
| `beforeToolCall/afterToolCall` | pipeline `before_hooks/after_hooks` |
| `tool_call` block/terminate | authorize/before stage 的 `Decision` |
| `ExtensionAPI` + 40 events | `agent/extensions.API` + 事件枚举 |
| `SKILL.md` 懒加载 | 同构 |
| `Api/Provider/Model catalog` | `agent/model` |
| `StreamEvent` | 同构 |
| `agent_settled` | runtime `settled` |
| `codemode`/`ExecutionEnv`/`durable` | P2，按需实现 |
