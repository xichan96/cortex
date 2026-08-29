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
