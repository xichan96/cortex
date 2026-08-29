# 工具系统向 Codex 借鉴 · 评估与设计方案

> 评估报告出处：[`docs/optimization-review-vs-codex.md`](../../optimization-review-vs-codex.md) 第十一章（工具系统）。
> 前置设计：[`docs/design/tool-pipeline.md`](../../design/tool-pipeline.md)（F1–F7，已落地，见其 §13 实现备注）。
> 范围：**只设计，不改业务代码**。落地时改动文件清单见各节与 §10，全部集中在 `agent/` 与 `dino/` 源码。
> 分支：`tools-codex-eval`。

---

## 0. 目标与决策摘要

本设计回答一个问题：**Codex 工具系统还有哪些模式值得 cortex 借鉴，各自性价比如何。**

Codex 的 7 类模式逐一评估结论（详见 §2–§8）：

| # | Codex 模式 | 评估结论 | 优先级 | 工作量 |
|---|-----------|---------|--------|--------|
| E1 | **ToolExposure（注册与模型可见性解耦）** | **做**。Direct/Deferred/Hidden 三态；工厂按态过滤可见规格；Deferred 工具仍可分发给已注册调用。收益：MCP 工具多时不撑爆上下文、`mcp_client` 逃生舱可退场 | **P1** | 中 |
| E2 | **tool_search 延迟发现** | **做（轻量版）**。`SearchableTool` 接口 + 关键词索引；`tool_search` 工具返回工具名/描述，下一轮注入为正式工具。必须与 E1 的 Deferred 配套 | **P1** | 中 |
| E3 | **apply_patch 流式解析** | **暂缓**。Lark 语法自由文本工具与 cortex 的 JSON-schema 工具哲学冲突；流式增量解析收益依赖「模型能边写边看进度」，cortex 无中途反馈通道。**但**可低成本先做「多 hunk patch」JSON 版，E3 完整版 P3 | **P3** | 大 |
| E4 | **结构化结果头 + 中间截断** | **已做**（F1，`agent/types/truncate.go`）。无需新设计；本设计仅指出与 Codex 的残余差距（header 在落盘路径下不完整、超长工具无流式输出） | 已完成 | — |
| E5 | **并行门控 + 取消** | **已做**（F4，`errgroup.SetLimit`）。Codex 的「并行工具共享读锁 / 非并行独占写锁」在 cortex 的依赖拓扑下收益有限，不追 | 已完成 | — |
| E6 | **`contains_external_context()` 输出标记** | **做（轻量）**。记忆写入时按工具名分类：`web_*`/MCP 输出不落入长期记忆抽取；短期 chatstore 已按「tool 角色整体跳过」兜底。改动极小（一个分类函数 + ingest 过滤） | **P2** | 小 |
| E7 | **错误二分（可恢复喂回 vs 致命）** | **已做**（F3，`FatalToolError` + `FatalToolErrorKind`）。剩余工作是把更多错误码纳入 fatal 清单（`EC_TOOL_AUTH_ERROR`/`EC_TOOL_INPUT_ERROR`/MCP 分类），单独评审 | **P2** | 小 |

**按工具逐个对比 Codex 的结论**（§9）：bash/read_file/grep/glob/list_directory/web_fetch/web_search/todo/question/MCP/edit_file/write_file。**唯一值得动刀的是 web_search（SSE 只读第一行，脆弱）与 bash（输出无硬上限，但已被 F1 截断兜底）。** 其余「现状足够、不做」。

**推荐落地顺序**：E1+E2（一套体系，连带退场 `mcp_client`）→ E6（外部上下文标记）→ E7（fatal 清单补全）→ E3（P3 排期）。

---

## 1. 现状盘点表（grep 证据）

### 1.1 七个 Codex 模式的落地状态

| Codex 模式 | 状态 | 证据（file:line） | 说明 |
|---|---|---|---|
| ① 工具错误二分（RespondToModel vs Fatal） | **已做** | `agent/types/tool.go:17-70`（`FatalToolError`/`FatalToolErrorKind`/`IsFatalToolError`）；`dino/tools/tool_wrappers.go:67-72`（nonFatalTool 透传 fatal）；`agent/engine/agent_execution.go:491-497`（schema 失败 wrap fatal + errgroup 短路）；`dino/defined_tool.go:533-535`（审批拒绝返回 `ApprovalRejectedError` 不可恢复） | F3 已落地。残留：fatal 清单只含 schema/审批/loop 三类，`EC_TOOL_AUTH_ERROR`/`EC_TOOL_INPUT_ERROR`/MCP 11xxx 仍算可恢复（见 §7.3） |
| ② ToolExposure（Direct/Deferred/Hidden） | **未做** | `agent/types/tool.go:73-95`（`Tool` 接口 + `ToolMetadata`，无 exposure 字段）；`dino/factory.go:754-857`（`sessionTools := f.tools.GetAll()` → 全部 `wrapSessionTool` → `agent.AddTools` → 全部进模型）；`agent/engine/agent_execution.go:1228,1446`（`tools := ae.tools` 整体传给 `ChatWithTools`）；`agent/llm/anthropic_native.go:513-520`（`for _, t := range tools` 全量转 Anthropic tool） | **注册 = 必须给模型**。MCP 工具（`factory.go:608-618` 全部 `Register`）全量进上下文。无 Deferred 概念 |
| ③ tool_search 延迟发现 | **未做** | 全仓无 `tool_search`/`SearchableTool`/BM25（§1.2 grep 为空） | 无延迟发现机制 |
| ④ apply_patch 流式解析 | **未做** | `agent/tools/builtin/fs/edit.go:34-40`（JSON schema：path/old_str/new_str，单 hunk）；`edit.go:74-77`（`strings.Contains` + `strings.Replace(…,1)` 单次替换） | JSON 结构化工具，非自由文本 patch；无流式解析 |
| ⑤ 结构化结果头 + 中间截断 | **已做** | `agent/types/truncate.go:12-229`（`OutputHeader`/`TruncateMiddle`/`TruncateToolResult`）；`agent/engine/agent_execution.go:270-274`（调用点组装 header）；`tool-pipeline.md` §13 F1 | 已落地。残余差距见 §4.2 |
| ⑥ 并行门控 + 取消 | **已做（部分）** | `agent/engine/agent_execution.go:463-467`（`getToolParallelismLimit()` + `errgroup.SetLimit`）；`:489-497`（fatal 取消同层）；`tool-pipeline.md` §13 F4 | 并发上限已做。Codex 的「并行=读锁/非并行=写锁」未做（§5 论证不值得） |
| ⑦ `contains_external_context()` 输出标记 | **未做（部分等价）** | `dino/mem/ingest.go:136-139`（**跳过 tool 角色消息**——工具输出整体不进长期记忆抽取）；`agent/providers/memory_save.go:35-58`（工具步骤经 `MessagesFromToolSteps` 存 chatstore，tool 角色消息在） | 短期记忆存工具输出、长期记忆跳过——与 Codex 的「输出标记 + memory 关闭」**方向一致但更粗**。无「按工具分类」的细粒度（见 §6） |

### 1.2 内置工具注册与可见性全链路（E1 的改造点）

```
loadBuiltinTools (dino/factory.go:1152-1175)  →  f.tools (Registry)
  └ 14 个工具 + MCP 全量 (factory.go:608-618, 1153-1167)
CreateSession (factory.go:754)
  └ sessionTools := f.tools.GetAll()            // 全部
  └ + 长期记忆工具 (factory.go:759-763)          // memory_* 工具
  └ + 权限过滤 (factory.go:780-791)               // Deny 跳过；Ask → needApproval
  └ wrapSessionTool (factory.go:1086-1095)       // 路径审批→approval→limiter→nonFatal→loop
agent.AddTools (factory.go:857)
  └ ae.tools (agent/engine/agent_tools.go:21)
ChatWithTools (agent_execution.go:1228,1244)
  └ buildRequest → anthropicTool 全量 (anthropic_native.go:513-520)
```

**耦合点：`GetAll()` 一个函数同时服务「注册」（可执行）与「可见」（给模型）。** E1 的解耦就是在 `GetAll` 与 `buildRequest` 之间插入 exposure 过滤。

### 1.3 内置工具逐个对比（详见 §9 表格）

- **bash**：`dino/tools/builtin.go:37-51` 包装 `runtime.NewCommandTool` → `dino/verify/shell.go`（待确认）→ `pkg/shell/shell.go:139-152`。支持 timeout（`command.go:78-94`，ctx deadline 或显式 `timeout` 参数，默认 30s）、background（`command.go:67-76` → `pkg/shell/background.go`）、工作目录（`fs.EffectiveWorkingDir`）。**无输出硬上限**（bytes.Buffer 无界，`shell.go:330`），靠 F1 截断兜底。
- **edit_file**：`fs/edit.go:34-40`，单 hunk 精确字符串替换（`strings.Replace(…,1)`），非 diff 语义，不支持多 hunk。
- **read_file**：`fs/read.go:34-78`，**无 offset/limit**，整文件读；目录读返回 listing。大文件靠 F1 截断兜底。
- **web_fetch**：`web/webfetch.go`，5MB 上限（`:26,151-153`）、timeout（`:96-105`，默认 30s 上限 120s）、HTML→Markdown（`:179-188`）。**无限流**。
- **web_search**：`web/websearch.go:182-198`，**手写 SSE 只读第一个 `data:` 行**，脆弱（正是评估报告 11.1 点名的）。
- **grep**：`search/grep.go:89-109`，`exec.CommandContext("grep", "-r", …)`，无上限（输出行数无 cap）、无 ignore 规则、exit 1（无匹配）当 error 返回。
- **glob**：`search/glob.go:59-71`，`filepath.Glob` 单层（**不递归 `**`**），SafePath 过滤。
- **list_directory**：`dino/tools/builtin.go:133-165`，单层 ReadDir，返回 name/is_dir/size/mode/mod_time，无递归、无排序上限。
- **todo**：`task/todo.go`，完整 add/remove/update/list + 状态机 + 唯一 in_progress。Codex 无对应内置（用 agent 自管理）。**现状足够**。
- **question**：`runtime/question.go:57-71` 返回 `SentinelQuestionResult{ask_user:true}`；P2.1 已接异步事件（`dino/session/session.go:507-517` → `EventTypeQuestion` → `client.go:448-451` onQuestion 回调）。**用户回答回流无 `AnswerQuestion` 通道**（`session.go` 只有 `Input()`/`ObserveOneUserTurn`，事件注释里写的 `AnswerQuestion` 方法**不存在**）。
- **MCP**：`pkg/mcp/client.go`（http/httpStreamable/sse，无 stdio）；allow-list 靠 env `_goclaw_mcp_allow`（`dino/tools/manager.go:73-83`）；错误码 11xxx（`pkg/errors`）。`mcp_client` 逃生舱工具（`factory.go:1167`）绕过权限包装——E1 落地后应退场。
- **write_file**：`fs/write.go:35-40`，整文件覆盖写。

### 1.4 「额外评估项」落地状态

| 额外项 | 状态 | 证据 |
|---|---|---|
| 结构化结果头 + 中间截断 | 已做 | §1.1 ⑤ |
| 并行门控 | 已做（`SetLimit`） | §1.1 ⑥ |
| `contains_external_context` | 未做（短记忆存、长记忆整体跳过） | §1.1 ⑦ |
| 错误分类遗漏 | 部分（fatal 清单窄） | §1.1 ①，§7.3 |

---

## 2. E1 · ToolExposure：注册与模型可见性解耦

### 2.1 目标

Codex 的 `ToolExposure`（`tools/src/tool_executor.rs:51-80`）三态：

| 态 | 模型初始列表 | 可分发给已注册调用 | 用途 |
|---|---|---|---|
| `Direct` | 可见 | 是 | 默认工具 |
| `Deferred` | 不可见 | 是 | 注册但靠 `tool_search` 发现后才进上下文 |
| `Hidden` | 不可见 | 是 | 逃生舱 / 内部编排工具（`spawn_agent` 等） |

cortex 现状「注册 = 必须给模型」。改造后目标：

1. `types.ToolMetadata` 增加 exposure 字段，默认 `Direct`（**零迁移**：现有工具不声明即全可见）。
2. 模型可见规格只在 `CreateSession` 组装时过滤 `Direct`（或本会话已 discover 的 Deferred）。
3. Deferred 工具**仍然注册在 Registry**（`Get`/执行分发路径不变），只是不进初始工具列表。
4. Hidden 工具同样注册，但永不可见——留给内部编排（subagent 工具族）与 `mcp_client` 逃生舱（若保留）。

### 2.2 类型与接口设计

**`agent/types/tool.go` 新增：**

```go
// ToolExposure 控制工具在模型初始工具列表中的可见性。
type ToolExposure string

const (
    // ExposureDirect 默认：注册即对模型可见（现状行为）。
    ExposureDirect   ToolExposure = "direct"
    // ExposureDeferred 注册可分发但初始列表不可见；模型通过 tool_search 发现后
    // 下一轮才注入为正式工具。
    ExposureDeferred ToolExposure = "deferred"
    // ExposureHidden 完全不可见（但仍可被引擎内部分发）。
    ExposureHidden   ToolExposure = "hidden"
)

func (e ToolExposure) IsDirect() bool   { return e == "" || e == ExposureDirect }
func (e ToolExposure) IsDeferred() bool { return e == ExposureDeferred }
func (e ToolExposure) IsHidden() bool   { return e == ExposureHidden }
```

**`ToolMetadata` 扩展（`tool.go:87-95`）：**

```go
type ToolMetadata struct {
    // ...现有字段
    Exposure ToolExposure `json:"exposure,omitempty"` // 默认 direct（省略即直接可见）
    // SearchKeywords 供 tool_search 索引；空则用 Description 分词。
    SearchKeywords []string `json:"searchKeywords,omitempty"`
}
```

**`agent/tools/registry.go` 新增可见性查询（不破坏现有 API）：**

```go
// GetAllVisible 返回 exposure 为 direct 的工具（按名排序）。E1 过滤入口。
func (r *Registry) GetAllVisible() []types.Tool
// GetDeferred 返回 exposure 为 deferred 的工具。供 tool_search 索引。
func (r *Registry) GetDeferred() []types.Tool
// GetDeferredTool 按名取一个 deferred 工具（tool_search 命中后注入用）。
func (r *Registry) GetDeferredTool(name string) (types.Tool, error)
```

`GetAll()`（`registry.go:70-80`）保持原语义（全部工具，供 subagent 分发/调试），**不改为只返 direct**，避免破坏 `dino/agent/subagent.go:279-310` 的 `filterTools`（子代理仍需要全部工具再按权限过滤）。

### 2.3 工厂接线：`CreateSession` 过滤可见性

改动 `dino/factory.go:754-857`。当前 `sessionTools := f.tools.GetAll()` → 全量 wrap。改为**两阶段**：

```go
// 阶段 1：direct 工具 → 直接进可见列表
sessionTools := f.tools.GetAllVisible()          // registry.go 新增

// 阶段 2：deferred 工具 → 供 tool_search 检索，但不直接进可见列表
var deferredTools []types.Tool
if f.config.Tools.ToolSearchEnabled {            // 配置开关，默认 true
    deferredTools = f.tools.GetDeferred()
    // tool_search 工具本身加入可见列表（§3）
}
```

其余逻辑（权限过滤 → wrap → AddTools）只处理可见列表。**Deferred 工具的包装链问题**见 §2.4。

`agent.AddTools`（`agent/engine/agent_tools.go:21`）与 `ae.tools` 不做改动——**engine 层不感知 exposure**。Exposure 只影响「哪些工具进 `ae.tools`」，engine 仍以「在 `ae.tools` 里」为可调用依据（`agent_execution.go:434-437` `getToolByName`）。这样 Deferred 工具发现后**动态加入 `ae.tools`** 即可用（`agent.AddTools` 已有 append 语义，`agent_tools.go:21`）。

### 2.4 Deferred 工具的包装链（关键设计决策）

Deferred 工具被发现后**下一轮**才成为 `ae.tools` 成员，但它必须拥有与 direct 工具**相同的包装链**（路径审批 → approval → limiter → nonFatal → loop，`factory.go:1086-1095`）。

**方案：包装提前 + 注册延后。**

在 `CreateSession` 阶段就把 deferred 工具也 `wrapSessionTool` 一遍，但**只存入 session 级缓存**（`map[string]types.Tool`），不 `AddTools`。`tool_search` 命中时（§3）从缓存取出已包装工具 `AddTools` 进 engine。

```go
// CreateSession 内新增
f.sessionDeferredTools[sessionID] = map[string]types.Tool{}
for _, t := range deferredTools {
    // 与 direct 相同的权限评估 + wrapSessionTool
    name := t.Name()
    action := evaluator.Evaluate(name, nil)
    if action == permission.ActionDeny { continue }
    wrapped := f.wrapSessionTool(t, sessionID, senderAdapter, needApproval)
    f.sessionDeferredTools[sessionID][name] = wrapped
}
```

理由：`wrapSessionTool` 需要 `sessionID`/`approvalStore`/`senderAdapter`/`needApproval`，这些在 `CreateSession` 里都有；运行中重新 wrap 要持有 factory 全量状态，不如构造期一次完成。**代价是构造期多做一次包装（无执行副作用）**，可接受。

### 2.5 对现有工具的影响（逐工具）

| 工具 | 建议 exposure | 理由 |
|---|---|---|
| read_file / write_file / edit_file / bash / glob / grep / list_directory / todo / question / job_kill / job_output | `Direct`（默认，零改动） | 高频核心工具 |
| web_fetch / web_search | `Direct`（默认） | 现有行为不变；E6 落地后记忆不受污染 |
| **MCP 工具（全部）** | **`Deferred`（推荐）** | 工具多时上下文爆炸的根治；靠 tool_search 按需发现 |
| memory_*（`dino/mem/subsystem.go:152-...`） | `Deferred`（可选） | 记忆操作低频，模型需要时再发现；与 E2 的 `search_knowledge` 检索语义天然契合 |
| mcp_client | `Deferred` 或 `Hidden`（二选一，见 §2.6） | 逃生舱工具；MCP 全量 Deferred 后它只剩调试价值 |
| delegate_to_agent / spawn_agent / wait_agent | `Direct`（现状） | 子代理工具仍需模型主动调用；`Hidden` 会破坏现有 multi-agent 流程 |
| skill | `Direct`（现状） | — |

**推荐：MCP 全量 `Deferred` 由配置驱动**（`_goclaw_mcp_allow` 里同时支持 `tool@exposure` 标记，或工厂对 MCP 工具统一 `ExposureDeferred`）。用户侧用一个开关 `tools.mcp_deferred: true|false`（默认 false 保持兼容，迁到默认 true 见 §10 迁移顺序）。

### 2.6 `mcp_client` 逃生舱的退场（连带收益）

现状 `mcp_client`（`dino/factory.go:1167`，`agent/tools/builtin/mcp/client.go`）是「通用 MCP 调用」逃生舱：模型可 `connect` 任意 server 并 `call_tool`，**绕过权限包装**。E1 落地、MCP 工具以 Deferred 方式注册后，`mcp_client` 不再必要：

- 模型要调某个 MCP 工具 → `tool_search` 发现 → 正式工具（走完整包装链 + 权限审批）。
- 移除 `mcp_client` 注册（`loadBuiltinTools` 去掉 `NewMCPClientTool()`）或改 `Hidden` 保留调试。
- **风险**：`mcp_client` 是部分 MCP server 的唯一入口（server 未在 config 里配置、运行时动态 connect）。若产品依赖动态 MCP 接入，保留 `Hidden` 版（可见性隐藏但可分发改名 `Hidden`，需确认 MCP 动态接入需求，见 §12 待定点）。

### 2.7 测试策略

1. `TestMetadataExposure_DefaultDirect`：空 exposure → `IsDirect()==true`。
2. `TestRegistry_GetAllVisible_FiltersDeferred`：注册 direct+deferred+hidden → `GetAllVisible` 只含 direct。
3. `TestFactory_SessionToolsExcludesDeferred`（`dino/factory_test.go`）：配置 MCP 工具为 deferred → `CreateSession` 后 `ae.tools` 不含 MCP 工具名。
4. `TestFactory_DeferredWrappedForSearch`：deferred 工具已 `wrapSessionTool`（断言包装链类型顺序，同 `tool-pipeline.md` §10.9）。
5. `TestSubagent_StillSeesAllTools`：`GetAll()` 语义不变（回归 `filterTools`）。

### 2.8 落地路径

1. `agent/types/tool.go`：`ToolExposure` + `ToolMetadata.Exposure`/`SearchKeywords`（无破坏）。
2. `agent/tools/registry.go`：`GetAllVisible`/`GetDeferred`/`GetDeferredTool`。
3. `dino/factory.go:754-857`：两阶段拆分的 `sessionDeferredTools` 缓存 + `GetAllVisible`。
4. `dino/config.go`：`Tools.ToolSearchEnabled`、`Tools.MCPDeferred`。
5. `dino/tools/manager.go`（MCP）：`GetAllMCPTools` 返回时按 config 设 `Exposure`。
6. 移除/降级 `mcp_client`（§2.6）。
7. 测试 + release note（模型可见工具列表变化）。

---

## 3. E2 · tool_search 延迟发现（轻量版）

### 3.1 目标

Codex 的 `tool_search`（`tools/src/tool_search.rs:151-158`，BM25）让模型在初始列表里看到一个 `tool_search` 工具，调用后返回匹配的 `LoadableToolSpec`，**下一轮**才成为正式工具。cortex 做**轻量关键词版**（不引入 BM25 库）：

- 初始工具列表里只有一个 `tool_search` 工具（schema：`query`）。
- 它检索所有 `Deferred` 工具的 `SearchKeywords` + `Description` 分词，返回 Top-K 工具名/描述。
- **命中工具在下一轮被注入 `ae.tools`**（session 级缓存，`AddTools` append），模型即可正常调用。

### 3.2 接口设计

**`agent/tools/builtin/runtime/tool_search.go`（新文件）：**

```go
package runtime

// SearchableTool 声明工具可被 tool_search 按关键词检索。实现方返回搜索信息。
// 不实现此接口的 Deferred 工具回退用 Name+Description 做关键词。
type SearchableTool interface {
    SearchInfo() SearchInfo
}

type SearchInfo struct {
    Keywords   []string // 额外关键词（除 Name/Description 外的同义词、动作词）
    Category   string   // 分组：e.g. "mcp:github", "memory"
    SearchOnly bool     // true：仅可搜索，发现后也可调用（默认）; 预留
}

// ToolSearchTool 注入初始列表的检索工具。
type ToolSearchTool struct {
    // 检索闭包由 factory 注入（deferred 工具的索引在 session 构造期构建）。
    index *ToolSearchIndex
}

func (t *ToolSearchTool) Name() string { return "tool_search" }
func (t *ToolSearchTool) Schema() map[string]interface{} {
    return schemaObject(map[string]interface{}{
        "query":    map[string]interface{}{"type": "string", "description": "Search query, e.g. 'github' or 'memory'"},
        "max_results": map[string]interface{}{"type": "integer", "description": "Max results (default 8)"},
    }, []string{"query"})
}
func (t *ToolSearchTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
    // 返回 []map{name, description, category}，按评分降序，无结果返回空数组（不是错误）。
}
```

**索引 `agent/tools/builtin/runtime/tool_search_index.go`：**

```go
// ToolSearchIndex 简单的 token → tool 倒排 + 词频评分（轻量替代 BM25）。
type ToolSearchIndex struct {
    docs   []indexedTool        // name / description / keywords / category
    tokens map[string][]docHit  // 小写 token → 命中文档
}
func NewToolSearchIndex(tools []types.Tool) *ToolSearchIndex
func (ix *ToolSearchIndex) Search(query string, max int) []SearchResult
type SearchResult struct {
    Name        string `json:"name"`
    Description string `json:"description"`
    Category    string `json:"category,omitempty"`
}
```

评分：查询 token 对文档命中数 × 位置权重（name 命中权重 3、keywords 2、description 1），无 BM25 的 IDF——**对工具名检索（`github`/`memory`/`search`）足够**。可选第二阶段换 BM25（`github.com/bbalet/stopwords` 级别）。

### 3.3 与 E1 的配合（session 级 discover 缓存）

`tool_search` 工具**只索引 Deferred 工具**（`f.sessionDeferredTools`）。发现即注入：

```go
// factory 注入 ToolSearchTool 的 Execute 闭包（go 的接口 + 闭包方案）：
discover := func(name string) {
    if wrapped, ok := f.sessionDeferredTools[sessionID][name]; ok {
        agent.AddTools(ctx, []types.Tool{wrapped})   // 下一轮可见
        delete(f.sessionDeferredTools[sessionID], name)
        f.sessionDiscoveredTools[sessionID] = append(..., name)
    }
}
```

**「下一轮」的语义**：`tool_search.Execute` 返回命中列表 + 一个 `discovered: ["github_tool"]` 标记；engine 的 `buildToolCallResults`/`OnToolResult` 回调里，session 层（`dino/session/session.go` 的 tool_result 分支）检测到 `discovered` 后调 `agent.AddTools`。下一轮 `ChatWithTools`（`agent_execution.go:1244`）自然带上。**无需改 engine 状态机。**

> 简化：第一版允许 `tool_search` 命中**立即**注入（同一轮内模型可能接着调用刚发现的工具，但 Anthropic 一次 response 内的工具调用列表在调用 `tool_search` 时已固定——所以实际生效仍在下一轮，语义一致）。

### 3.4 MCP 工具多时的收益

MCP server 往往暴露 20–100+ 工具。现状全量 Direct：
- 每个工具 schema 全进 `buildRequest`（`anthropic_native.go:513-520`）→ 上下文膨胀、`MaxToolCallsPerIteration`/prompt cache 都受影响。
- 模型面对大工具列表时选择质量下降。

Deferred + tool_search 后：
- 初始列表 ≈ 14 builtin + tool_search + memory（可控）。
- 模型按任务检索（"deploy" → deploy 工具族），一次只加载 3–5 个。
- **代价**：多一次工具调用 round-trip（检索 + 下一轮执行）。对明确知道要做什么的模型，成本可接受。

### 3.5 测试策略

1. `TestToolSearchIndex_NameHit`：query 命中 name → 最高分。
2. `TestToolSearchIndex_KeywordHit`：命中 `SearchInfo.Keywords`。
3. `TestToolSearch_NoResult`：空结果 → 空数组非错误。
4. `TestToolSearch_DiscoverInjectsTool`（`dino/factory_test.go`）：`tool_search` 返回 discovered → 下一轮 `ae.tools` 含该工具。

### 3.6 落地路径

1. `agent/tools/builtin/runtime/tool_search_index.go`：倒排 + 评分。
2. `agent/tools/builtin/runtime/tool_search.go`：工具本体（Execute 查索引）。
3. `dino/factory.go`：构造索引（用 `sessionDeferredTools`）→ 注入 `ToolSearchTool` 到可见列表 → `Execute` 闭包 discover 注入。
4. `dino/session/session.go`：tool_result 分支检测 `discovered` → `AddTools`。
5. 测试。

> **与 E1 强耦合**：没有 Deferred，tool_search 没有检索对象。落地时 E1+E2 一个 PR。

