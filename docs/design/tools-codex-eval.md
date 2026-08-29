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

- **bash**：`dino/tools/builtin.go:37-51` 包装 `runtime.NewCommandTool` → `pkg/shell/shell.go:139-152`（注：`dino/verify/shell.go:10` 的 `VerifyShell` 是 runner 的 verifier，不是 bash 工具本体）。支持 timeout（`command.go:78-94`，ctx deadline 或显式 `timeout` 参数，默认 30s）、background（`command.go:67-76` → `pkg/shell/background.go`）、工作目录（`fs.EffectiveWorkingDir`）。**无输出硬上限**（bytes.Buffer 无界，`shell.go:330`），靠 F1 截断兜底。
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

---

## 4. E3 · apply_patch 流式解析（评估 + 暂缓）

### 4.1 Codex 的做法

- **Lark 语法自由文本工具**（`apply_patch_spec.rs:9-28`）：模型直接给整个 patch 文本（`*** Begin Patch\n*** Update File: …\n@@ … @@`），不是 JSON schema 参数。
- **流式增量解析**（`apply_patch.rs:86-155`）：`StreamingPatchParser` 消费参数增量（Anthropic 流式 `input_json_delta`），边解析边发 `PatchApplyUpdatedEvent`（500ms 节流），模型能在调用完成前看到 patch 实时解析进度。
- **写权限从解析出的路径推导**（`apply_patch.rs:236-271`）：跳过已有写权限的目录的父目录。

### 4.2 cortex 现状与差距

| 维度 | cortex（`fs/edit.go`） | Codex apply_patch | 差距 |
|---|---|---|---|
| 工具形态 | JSON schema：`{path, old_str, new_str}` | 自由文本 patch | 结构性差异 |
| hunk 数 | **单 hunk**（`strings.Replace(…,1)`，`edit.go:77`） | 任意多 hunk + 增删文件 | 能力差 |
| 解析 | 无解析（直接字符串匹配） | Lark 语法增量解析 | 无 |
| 流式进度 | 无中途反馈通道 | `PatchApplyUpdatedEvent` | 无 |
| 写权限 | 路径审批（`ExternalPathApprovalTool`，`defined_tool.go:508-549`）+ `SafePath` | 从解析路径推导 + 父目录跳过 | 现状已覆盖 |

**关键判断：cortex 的「流式进度」收益为负。**

1. **无中途反馈通道**：cortex 的流式（`ExecuteStream`）只把 `tool_use` 完整结果发给模型，没有「模型可读的进行中 tool_result」。Codex 的 `PatchApplyUpdatedEvent` 依赖「模型在工具执行期间能看到进度」——cortex 的 `resultSender`（`agent_execution.go:731-737`）是**单向**的，模型无法在 `input_json_delta` 到达前看到部分结果。
2. **JSON-schema 工具在现模型上更稳**：cortex 14 个工具全是 JSON 参数，模型（尤其当前用的 Claude 系列）对 `old_str`/`new_str` 精确替换的遵循度高。Lark 自由文本 patch 需要模型掌握一套新语法，且解析失败路径多。
3. **单 hunk 限制是真痛点，但解法不同**：`edit.go:77` 一次只能替换一处。模型要改多处需多次调用。**多 hunk 的 JSON 化（不是 Lark）才是低成本高收益的中间态**（见 §4.4）。

### 4.3 结论：完整 apply_patch（P3，暂缓）

完整移植 Codex 的 Lark + StreamingParser 工作量**大**（新解析器 + 流式事件管线 + 写权限推导重构），收益依赖「中途反馈」这一 cortex 没有的通道。**不建议近期做。**

### 4.4 中间态：多 hunk JSON patch（P2，可选）

不改工具形态，扩展 `edit_file` schema 支持**多 hunk**：

```go
// edit_file schema 扩展（保持向后兼容：hunks 缺省时用旧 path/old_str/new_str）
"properties": {
    "path": {"type": "string", "description": "File to edit"},
    "hunks": {
        "type": "array",
        "items": {
            "type": "object",
            "properties": {
                "old_str": {"type": "string"},
                "new_str": {"type": "string"}
            },
            "required": ["old_str", "new_str"]
        },
        "description": "Multiple precise replacements applied in order"
    }
}
```

执行：`edit.go` 读文件一次，依次对每个 hunk 做 `strings.Replace(…,1)`；任一 hunk 找不到 `old_str` → 整批失败（无部分应用，保持原子性）。`ExternalPathApprovalTool` 的 `collectOutsideAbsPaths`（`defined_tool.go`）已按工具名+输入路径收集——多 hunk 场景 path 仍只有一个，**审批逻辑零改动**。

**收益**：一次调用改多处，减少 round-trip；不引入新语法。**风险**：hunk 间顺序敏感（前一个替换改变后一个的匹配上下文）——文档里要求模型用「互不重叠的 hunk」。

> **决策**：§4.4 的「多 hunk JSON」是本设计的推荐中间态（P2，可选）；完整 Lark+流式（§4.3）P3 暂缓。两者独立，不互斥。

---

## 5. E5 · 并行门控（已做，不追）

### 5.1 现状

`agent_execution.go:463-467`：`errgroup.SetLimit(getToolParallelismLimit())`，全局并发上限（默认 `max(4, GOMAXPROCS*2)` 封顶 32，`AgentConfig.ToolParallelismLimit` 可配）。fatal 错误取消同层并行（`:489-497`）。

### 5.2 Codex 的差异

Codex 用 `async RwLock`：并行工具 `lock.read()`（并发），非并行工具 `lock.write()`（独占，`parallel.rs:152-156`）——即**按工具属性区分共享/独占**。cortex 的 errgroup 是「无依赖层内全并发 + 全局限额」，不区分共享/独占。

### 5.3 是否值得做「共享/独占」？

**不做。** 理由：

1. cortex 的依赖拓扑已把「必须串行」建模为**层间依赖**（`Dependencies`，`agent_execution.go:433-458` 拓扑排序）；同层工具**天然可并行**。Codex 的写锁是给「同一工具并发写会冲突」（如 `edit_file` 并发改同一文件）的场景，但 cortex 的 `MaxToolCallsPerIteration` + 单 hunk edit 已把冲突面压到极小。
2. 引入共享/独占需要每个工具声明 `ParallelSafe bool`，且要在 errgroup 之上再套一层锁调度——复杂度不成比例。
3. **真正要防的**是「N 个 bash/MCP 全量并发」——已被 SetLimit 解决。

**残余风险标注**：同层多个 `edit_file` 改同一文件仍可能并发竞态（`edit.go` 无文件锁）。当前默认并发 4–32，概率低；若要做，只需给 `fs` 工具加一个**进程内 per-path 锁**（`sync.Map[string]*sync.Mutex`），比 Codex 的读写锁更精准。列入「可选 P3」。

---

## 6. E6 · `contains_external_context()` 输出标记（轻量版）

### 6.1 Codex 的做法

工具输出声明「含外部上下文」（web/MCP），memory 生成自动关闭（`tool_output.rs:21-24`），防止外部搜索污染长期记忆。

### 6.2 cortex 现状

| 记忆层 | 现状 | 是否已防污染 |
|---|---|---|
| 长期记忆（`dino/mem/ingest.go`） | **tool 角色消息整体跳过**（`:136-139`）——任何工具输出都不进抽取 | 已防（比 Codex 更粗但方向一致） |
| 短期记忆（chatstore，`factory.go` `memoryAdapter.SaveContext`） | 工具步骤经 `MessagesFromToolSteps`（`agent/providers/memory_save.go:50-57`）存为 tool 角色消息 | 存，但 tool 消息用于历史窗口重建，不参与记忆抽取 |
| 记忆工具（`dino/mem/tool.go:27`） | 模型**主动写** `add_knowledge`/`set_preference` 时才会落长期记忆 | 模型可把 web 结果写进去（污染源） |

### 6.3 差距与方案

**差距**：`dino/mem/ingest.go` 的「tool 整体跳过」够用；真正的污染路径是**模型主动把外部内容写进 `add_knowledge`**，ingest 无法识别（它只扫 transcript，不扫 memory 工具写入）。Codex 的标记机制在**工具输出层**拦截——比 ingest 层更早。

**轻量方案：工具名分类函数 + 记忆工具写入过滤。**

```go
// dino/mem/tool.go 新增
// ExternalContextTool 判断工具是否产生外部上下文（web 搜索/抓取、MCP 外部调用）。
func ExternalContextTool(name string) bool {
    switch {
    case name == "web_search" || name == "web_fetch":
        return true
    case strings.HasPrefix(name, "mcp://"): // MCP 工具命名空间
        return true
    }
    return false
}
```

- **记忆工具写入侧**：`sqliteMemoryTool`（`dino/mem/tool.go`）的 `add_knowledge` action 增加一个 `source` 字段；若调用上下文是「web 结果 → 记知识」，提示模型优先把来源标为 `reference`，且 web 结果写 `user/feedback` 类知识时降权。**更简单的一刀**：`add_knowledge` 的 schema 加 `external_context: bool`（模型诚实标注），标注后 `user/feedback` 类拒绝、`reference` 类接受。
- **ingest 侧已足够**：`ingest.go:136-139` 跳过 tool 消息，无需改。

### 6.4 收益与成本

- **收益**：中低。长期记忆抽取已跳过工具输出，污染主要来自模型主动写入——这是低频且模型可被 prompt 约束的行为。
- **成本**：小（一个分类函数 + `add_knowledge` schema 加字段 + 校验）。
- **决策**：**P2 做轻量版**（改 `dino/mem/tool.go` 一个文件），但不做 Codex 的「输出标记贯穿 tool_output 类型」全量改造。

---

## 7. E7 · 错误分类（已做，补遗漏）

### 7.1 现状

- `FatalToolError`（`agent/types/tool.go:17-70`）+ `FatalToolErrorKind`（dino veto/loop 实现）。
- 引擎 fatal 短路（`agent_execution.go:491-497`）；nonFatalTool 透传 fatal（`tool_wrappers.go:67-72`）。
- 错误码体系：`agent/types/toolresult.go` + `pkg/errors`（EC_TOOL_*）。

### 7.2 剩余问题

`nonFatalTool`（`tool_wrappers.go:44-96`）现在把**所有非 fatal 错误**转可恢复 `{ok:false}` 喂回模型。其中几类「重试也无效」的错误被误判为可恢复：

| 错误码 | 现状 | 应该 | 理由 |
|---|---|---|---|
| `EC_TOOL_AUTH_ERROR` | 可恢复 | **fatal** | 凭证错误重试输入无用 |
| `EC_TOOL_INPUT_ERROR`（参数语义错） | 可恢复 | fatal（部分） | schema 已做 fatal（`agent_execution.go:491-497`）；参数语义错（如 `old_str` 找不到，`edit.go:74-76`）重试可能有用，**保留可恢复** |
| MCP 11xxx（`EC_MCP_TOOL_RETURNED_ERROR`） | 可恢复 | **可恢复**（保留） | MCP server 可能临时失败，重试有效 |
| `EC_MCP_NOT_CONNECTED` / `EC_MCP_CLIENT_INIT_FAILED` | 可恢复 | fatal | 连接态错误重试同一输入无效 |
| 工具不存在（`agent_execution.go:364` `tool not found`） | 已在引擎层处理 | — | 已是 fatal 语义（`EC_TOOL_NOT_FOUND` 未走 nonFatal） |

### 7.3 方案

**不动** `FatalToolError` 机制本身（已正确）。只做**分类清单补全**：

```go
// dino/tools/tool_wrappers.go 内 nonFatalTool.Execute 的错误分类（增强版）。
// pkg/errors.Error 是 struct { Code int; Message string }（errors.go:11-13），
// 错误码常量是 *errors.Error 值（ec.go:26,83-88）。errors.As 到 *errors.Error 后比 Code。
func classifyToolError(err error) error {
    var ec *errors.Error
    if stderrors.As(err, &ec) {
        switch ec.Code {
        case errors.EC_TOOL_AUTH_ERROR.Code,
             errors.EC_MCP_NOT_CONNECTED.Code,
             errors.EC_MCP_CLIENT_INIT_FAILED.Code,
             errors.EC_MCP_CLIENT_START_FAILED.Code:
            return &types.FatalToolError{Err: err, Reason: "non-retryable tool state error"}
        }
    }
    return err // 其余保持可恢复
}
```

- 错误码常量是 `*errors.Error`（`pkg/errors/ec.go:26,83-88`），字段 `Code`/`Message`（`errors.go:11-13`），`errors.As` 到 `*errors.Error` 后比 `ec.Code`。
- **注意**：`EC_TOOL_AUTH_ERROR` 变 fatal 后，`ExternalPathApprovalTool` 的审批拒绝已是 `ApprovalRejectedError`（fatal，`defined_tool.go:535`）；路径权限不足的错误（`EC_TOOL_PARAMETER_INVALID` 包装的 SafePath 失败）仍可恢复——模型可换路径重试，合理。

### 7.4 收益与成本

- **收益**：低-中。把「连接态错误」从无限重试（loop）中解放，减少无意义 token 消耗。
- **成本**：小（一个分类函数 + 测试）。
- **决策**：**P2 做**，与 `tool-pipeline.md` §13 遗留待定点 1（`EC_TOOL_AUTH_ERROR` 是否进 fatal）闭环。

---

## 8. 内置工具逐个对比 Codex（现状 → 差距 → 是否值得优化）

> 用户补充要求。逐工具给「现状 → Codex 做法 → 差距 → 性价比 → 落地方案」。结论先行：**大多数「现状足够，不做」；唯一强烈推荐动刀的是 web_search（SSE 解析）与 bash（输出硬上限，但已被 F1 兜底）。**

### 8.1 bash / command

| 维度 | cortex 现状 | Codex 做法 | 差距 |
|---|---|---|---|
| 超时 | ctx deadline 或显式 `timeout`（默认 30s，`command.go:78-94`）；`EC_TOOL_EXECUTION_TIMEOUT`（`:105-107`） | 会话级 `session_token_limit` + 超时 | 已对齐 |
| 会话复用 | `pkg/shell/shell.go` 每次 `NewShell`（`command.go:99`）——**无跨调用状态**（cwd/env 不延续） | 持久 shell session（cwd 延续、env 延续） | **差距**：cortex 每次调用独立 cwd（`EffectiveWorkingDir(t.workspace)`，恒等于 workspace）；模型无法 `cd` 后接着跑 |
| 输出截断 | **无硬上限**（`bytes.Buffer` 无界，`shell.go:330`），靠 F1 截断（`truncate.go:160-229`）兜底 | `maxOutputBytes` 硬上限 + header | F1 已兜底；超长输出仍会全量进内存 |
| 环境隔离 | `shell.EnvironNonInteractive()`（`shell.go`）+ 可选 blockFuncs；**无沙箱** | 沙箱（可选） | 沙箱是 P3 大项（评估报告已列） |
| 工作目录切换 | `fs.EffectiveWorkingDir` 固定 workspace；bash 无 `cwd` 参数 | 会话持久 cwd | 见「会话复用」 |
| 后台任务 | `background` + `job_output`/`job_kill`（`command.go:67-76`，`pkg/shell/background.go`） | 同款 | 已对齐 |

**差距本质**：cortex 的 bash 是「无状态每次执行」vs Codex 的「会话复用 + cwd 延续」。

**是否值得优化**：
- **输出硬上限（值得，小）**：`command.go` 或 `pkg/shell` 加 `MaxOutputBytes`（默认如 4MB），超限即截断返回 + 提示。**但** F1 已把「喂模型」的上限做好（`TruncateToolResult` 落盘/中间截断），真正缺的是「内存上限」（超大输出撑爆 bytes.Buffer）。**P2 小项**：给 `pkg/shell` 的 `exec` 用 `io.LimitReader` 风格的有界 buffer。
- **会话复用（暂缓）**：需要跨调用的 cwd/env 状态容器（session 级 `*shell.Shell` 单例 + mutex），与「模型 `cd` 后状态延续」的收益绑定。但**有状态 shell 是调试陷阱**（状态泄漏难查），且 cortex 工作流多用绝对路径（`SafePath` 已返回绝对路径）。**P3 暂缓**，除非产品明确要「长会话 shell 状态」。

### 8.2 edit_file

| 维度 | cortex 现状 | Codex 做法 | 差距 |
|---|---|---|---|
| 精确替换 vs 整文件重写 | `old_str`/`new_str` **精确字符串替换**（`edit.go:74-77`）；`write_file` 是整文件重写 | `apply_patch` Lark：多 hunk、增删文件 | 精度已对齐（甚至更稳）；**hunk 数不足** |
| diff 语义 | 无 diff，单次 `Replace` | patch 应用 + 上下文校验 | 单 hunk 找错时整批失败（原子性好） |
| 流式 | 无 | 流式解析 + 进度事件 | 见 §4，cortex 无中途反馈通道 |

**结论**：精确字符串替换是 Codex 的**超集替代**（对当前模型更稳）。唯一值得做的是 §4.4 的多 hunk JSON（P2 可选）。**不做 Lark。**

### 8.3 read_file

| 维度 | cortex 现状 | Codex 做法 | 差距 |
|---|---|---|---|
| offset/limit | **无**（`read.go:40-78` 整文件读） | 支持行范围读 + 截断提示 | **差距**：大文件只能整读后靠 F1 截断（浪费内存，且模型拿不到中间段） |
| 目录读 | 返回 listing（`read.go:59-72`） | 有 `list_directory` | 已有 |
| 大文件 | F1 截断兜底 | 行范围 + 自动截断提示 | 见上 |

**是否值得优化**：**P2 小项**。给 `read_file` schema 加 `offset`/`limit`（行号范围），超范围返回「TotalLines + 截断提示」——一次 API 改动（`read.go`）即可，复用 `fs/common.go` 的工具。收益：大文件高效读取、避免 F1 截断整读浪费。**这是本表里性价比最高的单工具优化之一。**

### 8.4 web_fetch / web_search

| 维度 | cortex 现状 | Codex 做法 | 差距 |
|---|---|---|---|
| 限流 | 无（`webfetch.go` 每次新 HTTP） | fetch 限流（并发/速率） | 低优先级 |
| SSE | **手写只读第一行**（`websearch.go:182-198`） | 完整 SSE 解析 | **差距大**：exa MCP 返回可能多条 `data:`，只读第一条会丢结果 |
| 超时 | fetch 30s/120s 上限；search 25s | 同 | 已对齐 |
| 输出上限 | fetch 5MB（`:26,151-153`）；search 结果数 8 | `maxOutputBytes` | fetch 5MB 进内存 + F1 截断，可接受 |

**是否值得优化**：
- **web_search SSE（值得，小-中）**：`websearch.go:182-198` 从「只读第一个 `data:` 行」改为完整 `bufio.Scanner` 循环收集所有 `data:` 块（或改用 `github.com/r3labs/sse` 类库），解析全部 content。**这是评估报告 11.1 明确点名的脆弱点**，且改动小（一个函数）。**P1 做。**
- **限流（暂缓）**：单 agent 并发 web 调用已受 `ToolParallelismLimit`（`agent_execution.go:463-467`）约束，无需工具内限流。P3。

### 8.5 grep / glob / list_directory

| 维度 | cortex 现状 | Codex 做法 | 差距 |
|---|---|---|---|
| grep 上限 | 无（`grep.go:89-109`，`CombinedOutput` 无行数 cap） | 结果上限 + 截断提示 | **差距**：超大 grep 结果全量进内存 |
| grep ignore | 无 ignore 规则（不排 vendor/node_modules） | 尊重 ignore 文件 | **差距**：搜到 vendor 噪音 |
| grep exit 1 | 当 error 返回（`grep.go:106`） | 「无匹配」当正常空结果 | **差距**：模型看到 error 误判失败 |
| glob 递归 | `filepath.Glob` 单层，**不递归 `**`**（`glob.go:59-71`） | 支持 `**` 递归 | **差距** |
| list_directory | 单层 ReadDir，无上限（`dino/tools/builtin.go:133-165`） | 同 | 已对齐 |

**是否值得优化**：
- **grep 无匹配当 error（值得，极小）**：`grep.go:106` 改为 `exit 1 + 空输出 → 返回空结果（正常）`。**P2 极小事**。
- **grep 结果上限（P2 小项）**：`exec` 改用 `io.LimitReader` 或 `bytes.Buffer` 后截断 + 提示。
- **glob `**` 递归（暂缓）**：`filepath.Glob` 不支持 `**`；换 `doublestar` 库或手写递归。收益中，但 glob 常被 grep/bash find 替代。P3。
- **ignore 规则（暂缓）**：需要读 `.gitignore`/`.ignore` 并做过滤，收益中，P3。

### 8.6 todo / question

| 维度 | cortex 现状 | Codex 做法 | 差距 |
|---|---|---|---|
| todo | 完整 add/remove/update/list + 状态机（`task/todo.go`） | 无内置（agent 用文件自管理） | cortex 更好；**不做** |
| question | P2.1 已接：`SentinelQuestionResult` → `EventTypeQuestion` → `onQuestion` 回调（`session.go:507-517`，`client.go:448-451`） | 无直接对应 | **差距**：**用户回答回流无 `AnswerQuestion` 通道**（事件注释提到的方法不存在）。模型提问后 UI 可回调 `onQuestion`，但回答怎么回到当前 turn 未定义（`session.go` 只有 `Input()`/`ObserveOneUserTurn`） |

**是否值得优化**：**question 回答回流（值得，中，P2 单列）**。方案：session 提供 `AnswerQuestion(questionID, answer string)` —— 把回答作为下一条 user 消息注入（复用 `Input()`/`ObserveOneUserTurn`），同时挂起当前 turn 等待（类似 F7 方案 A 的「挂起迭代」）。**但**：这需要 engine 的「question 工具返回后暂停」语义（`hasMore=false` + 等待输入），与 `tool-pipeline.md` §7 的 F7 未做项重叠。**建议与 F7 合并排期**（同一个「提问-回答回流」机制）。

### 8.7 MCP

| 维度 | cortex 现状 | Codex 做法 | 差距 |
|---|---|---|---|
| 工具名空间 | 平铺 `t.Name()`（MCP 工具名可能与 builtin 撞名，`factory.go:608-618` 注册冲突报错） | 名空间/前缀 | **差距**：撞名是注册期报错而非隔离 |
| 错误分类 | 11xxx 错误码，MCP 错误可恢复（`pkg/mcp/client.go:176-181`） | 统一 ToolError 二分 | E7 已覆盖连接态 |
| 动态接入 | `mcp_client` 逃生舱（`builtin/mcp/client.go`） | 无逃生舱（配置驱动） | E1 后应退场 |
| stdio | **无**（`pkg/mcp/client.go` 只 http/sse） | 有 stdio transport | **差距**：本地 MCP server（stdio）无法接入。`tool-pipeline.md` §13 已列「MCP stdio 二选一」，非本设计重点 |

**结论**：MCP 的优化都在 E1（Deferred）+ E7（错误分类）里覆盖，无需单独设计。**名空间前缀（可选 P3）**：`mcp://server/tool` 命名规避撞名——但会改变模型可见工具名，需与 E1 一起评估。

### 8.8 write_file

`fs/write.go` 整文件覆盖写，与 Codex 的 `write_file` 等价。**不做**（整写语义正确，模型有 read 兜底）。

### 8.9 逐工具优化优先级汇总

| 工具 | 优化项 | 优先级 | 工作量 | 落地方案 |
|---|---|---|---|---|
| web_search | SSE 完整解析 | **P1** | 小-中 | `websearch.go:182-198` 改完整 SSE 循环 |
| read_file | offset/limit 行范围 | **P2** | 小 | `read.go` schema + 实现 |
| grep | 无匹配返回空 + 结果上限 | **P2** | 小 | `grep.go:106` + LimitReader |
| bash | 输出内存硬上限 | **P2** | 小 | `pkg/shell` 有界 buffer |
| question | 回答回流 `AnswerQuestion` | **P2**（与 F7 合并） | 中 | session 注入 + 挂起 |
| glob | `**` 递归 | P3 | 小-中 | doublestar/手写 |
| grep | ignore 规则 | P3 | 中 | 读 .gitignore 过滤 |
| bash | 会话复用 + cwd 延续 | P3 | 中 | session 级 shell 单例 |
| web_fetch | 限流 | P3 | 小 | 并发受 SetLimit 已约束 |
| MCP | 名空间前缀 | P3 | 中 | 与 E1 一起评估 |

---

## 9. 优先级排序与落地路径

### 9.1 推荐顺序（按性价比）

| 轮次 | 项 | 理由 | 依赖 |
|---|---|---|---|
| **下一轮（P1）** | E1 ToolExposure + E2 tool_search（一个 PR） | MCP 多时的上下文根治；架构收益 | 无 |
| **下一轮（P1）** | web_search SSE 完整解析 | 评估报告点名脆弱点，改动小 | 无 |
| **P2** | E6 外部上下文标记 | 防记忆污染（轻量） | E1 的 exposure 命名空间（`mcp://`） |
| **P2** | E7 fatal 清单补全 | 防连接态错误无限重试 | 无 |
| **P2** | read_file offset/limit | 大文件高效读 | 无 |
| **P2** | grep 空结果 + bash 内存上限 | 正确性 + 稳定性小项 | 无 |
| **P2（与 F7 合并）** | question 回答回流 | 提问能力闭环 | F7 评审 |
| **P2（可选）** | edit_file 多 hunk JSON | 减少 round-trip | 无 |
| **P3** | E3 apply_patch Lark + 流式 | 收益依赖无的中途反馈通道 | — |
| **P3** | glob `**` / grep ignore / bash 会话复用 / MCP 名空间 / fetch 限流 | 收益中、冲突小 | — |

### 9.2 落地路径（E1+E2 详细步骤）

1. **`agent/types/tool.go`**：`ToolExposure` 常量 + `IsDirect/IsDeferred/IsHidden`；`ToolMetadata.Exposure`/`SearchKeywords`。
2. **`agent/tools/registry.go`**：`GetAllVisible`/`GetDeferred`/`GetDeferredTool`（不动 `GetAll`）。
3. **`dino/factory.go:754-857`**：`sessionTools := f.tools.GetAllVisible()`；deferred 工具预包装入 `f.sessionDeferredTools[sessionID]`；`mcp_deferred` config 控制 MCP 是否 Deferred。
4. **`dino/tools/builtin/runtime/tool_search{,_index}.go`**：索引 + 工具。
5. **`dino/factory.go`**：构造索引 → 注入 `ToolSearchTool` → Execute 闭包 discover → `AddTools`。
6. **`dino/session/session.go`**：tool_result 分支检测 `discovered` → `AddTools`。
7. **`dino/config.go`**：`Tools.ToolSearchEnabled`（默认 true）、`Tools.MCPDeferred`（默认 false，灰度后翻 true）。
8. **MCP exposure**：`dino/tools/manager.go` `AddServer` 时按 config 给 `mcpTools` 打 `ExposureDeferred`。
9. **退场 `mcp_client`**：`loadBuiltinTools` 去掉或降级 `Hidden`。
10. **测试 + release note**。

### 9.3 落地路径（其他 P1/P2）

- **web_search SSE**：`web/websearch.go` 的 `Execute` 里 `bufio.Scanner` 循环收集全部 `data:` 行 → 合并解析；或引入 `github.com/r3labs/sse/v2`。测试用 `httptest` mock SSE 流。
- **E6**：`dino/mem/tool.go` `add_knowledge` schema 加 `external_context: bool` + `ExternalContextTool(name)` 分类；校验 user/feedback 拒绝外部来源。
- **E7**：`dino/tools/tool_wrappers.go` `classifyToolError` + 测试。
- **read_file offset/limit**：`agent/tools/builtin/fs/read.go` schema 加 `offset`/`limit`（int），实现按行切片，返回 `total_lines`。
- **grep**：`search/grep.go` 改 `cmd.Output()` 分支（exit 1 空输出 → 空结果）+ 结果上限。
- **bash 内存上限**：`pkg/shell/shell.go` `exec` 用有界 buffer（如 `cap = 8MB`，超限截断并标记）。

---

## 10. 改动文件清单（按项汇总）

| 文件 | 项 | 改动 |
|---|---|---|
| `agent/types/tool.go` | E1 | `ToolExposure` + `IsDirect/IsDeferred/IsHidden`；`ToolMetadata.Exposure`/`SearchKeywords` |
| `agent/tools/registry.go` | E1 | `GetAllVisible`/`GetDeferred`/`GetDeferredTool` |
| `dino/factory.go:754-857,1086-1095` | E1+E2 | 两阶段 sessionTools；`sessionDeferredTools` 预包装缓存；`ToolSearchTool` 注入 + discover 闭包 |
| `dino/config.go` | E1+E2 | `Tools.ToolSearchEnabled`、`Tools.MCPDeferred` |
| `dino/tools/manager.go` | E1 | `AddServer` 按 config 给 MCP 工具打 `ExposureDeferred` |
| `dino/tools/builtin.go:1167` | E1 | `mcp_client` 移除或降级 `Hidden` |
| `agent/tools/builtin/runtime/tool_search.go` | E2 | 新文件：`ToolSearchTool` + `SearchableTool`/`SearchInfo` |
| `agent/tools/builtin/runtime/tool_search_index.go` | E2 | 新文件：倒排 + 评分 |
| `dino/session/session.go:507-517` | E2 | tool_result 分支检测 `discovered` → `AddTools` |
| `agent/tools/builtin/web/websearch.go:182-198` | P1 | 完整 SSE 解析 |
| `dino/mem/tool.go` | E6 | `add_knowledge` schema 加 `external_context`；`ExternalContextTool(name)` |
| `dino/tools/tool_wrappers.go` | E7 | `classifyToolError`（auth/connect 态 → fatal） |
| `agent/tools/builtin/fs/read.go` | P2 | `offset`/`limit` + `total_lines` |
| `agent/tools/builtin/search/grep.go` | P2 | exit 1 空输出 → 空结果 + 上限 |
| `pkg/shell/shell.go` | P2 | `exec` 有界 buffer |
| `agent/tools/builtin/fs/edit.go` | P2（可选） | 多 hunk JSON patch |
| `dino/session/session.go` + `dino/client.go` | P2（与 F7 合并） | `AnswerQuestion(questionID, answer)` 注入回流 |

---

## 11. 风险点

| 风险 | 概率/影响 | 缓解 |
|---|---|---|
| **E1 改变模型可见工具列表**：`mcp_deferred=true` 后模型不再直接看到 MCP 工具，若不熟悉 `tool_search` 会「不会用」 | 中/中 | 工具描述引导（`tool_search` 的 description 写清楚用法）；默认 `mcp_deferred=false` 灰度；`ToolSearchEnabled` 可关 |
| **E2 discover 时机**：`tool_search` 命中后注入 `ae.tools`，若模型下一轮没调（列表变化但模型选错），浪费一轮 | 低/低 | `tool_search` 返回 `discovered` 标记进上下文；模型看到「新工具已可用」自会尝试 |
| **E2 索引质量**：关键词检索命中不准，模型找不到需要的工具 → 放弃 | 中/中 | 评分覆盖 name/keywords/description；`SearchableTool` 让 MCP server 提供关键词；可配 `SearchOnly` |
| **E1 包装链遗漏**：Deferred 工具被发现后若包装链不完整（漏 approval/limiter），绕过权限审批 | 高/高 | §2.4 明确「构造期预包装」；测试断言包装链类型顺序（`tool-pipeline.md` §10.9 复用） |
| **E6 误伤**：`add_knowledge` 加 `external_context` 后，模型诚实标注或忘记标注导致知识分类错误 | 低/中 | 只是分类调整，不阻塞；`reference` 类仍接受 |
| **E7 误伤**：`EC_TOOL_AUTH_ERROR` 判 fatal 后，临时性 auth（token 过期但 refresh 后可用）被中止 | 中/中 | fatal 只影响「重试同一输入」；模型换输入（刷新 token 的工具）可继续。fatal 清单评审时逐条过 |
| **web_search SSE**：exa API 改格式 / 非标准 SSE | 低/低 | 只读 `data:` 前缀行的兼容逻辑保留；`httptest` 覆盖多块场景 |
| **question 回流**：挂起等待用户回答的 turn 语义复杂（与 budget/超时/流式交互） | 中/高 | 与 F7 合并评审；第一版只做「注入下一条 user 消息」不挂起 |

---

## 12. 留给用户的待定点

1. **MCP Deferred 默认值**：`mcp_deferred` 默认 `false`（保持现状），灰度后翻 `true`——是否接受「默认直连」过渡期？
2. **`mcp_client` 去留**：移除 vs 降级 `Hidden`。取决于产品是否依赖「运行时动态 connect 任意 MCP server」。（若依赖，`Hidden` 版保留，但 Hidden 工具不被模型看到——动态接入需走其他通道。）
3. **MCP 工具名空间**：是否在 E1 落地时一并引入 `mcp://server/tool` 前缀？（改变模型可见名，需 release note。）
4. **question 回答回流**：是否进入本季度？它跨 engine+dino 两层，且与 `tool-pipeline.md` F7 的未做项重叠——建议合并排期，但需确认产品「提问-回答」近期是否要用。
5. **bash 会话复用**：P3 的「长会话 shell 状态」是否有产品诉求？有状态 shell 是调试陷阱，倾向不做。
6. **edit_file 多 hunk**：P2 可选。是否需要？（当前单 hunk + 多次调用模型已能工作。）
7. **E3 apply_patch**：确认暂缓到 P3？（本设计的判断是：Lark 语法收益依赖 cortex 没有的「中途反馈」通道。）

---

## 13. 实现备注（落地时相对设计的偏离记录）

> 本设计文档在落地时若与代码不符，在此记录偏离（参照 `tool-pipeline.md` §13 的惯例）。当前为空。

## 14. 与既有设计的关系

- `tool-pipeline.md` F1–F7 已落地（§13 实现备注）。本设计**不重复** F1（截断）/F3（错误二分）/F4（并发），只评估残余差距。
- F7 的「question 回答回流」（`tool-pipeline.md` §12.7 遗留）与本设计 §8.6 的 `AnswerQuestion` 是同一机制，**建议合并排期**。
- 评估报告 `optimization-review-vs-codex.md` 落地优先级表的 P2（ToolExposure / 工具错误二分）中，「错误二分」已在 `tool-pipeline` 分支落地（F3），本设计补 ToolExposure（E1）+ tool_search（E2）+ 错误分类补全（E7）。




