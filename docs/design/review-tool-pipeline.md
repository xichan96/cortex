# 工具执行管线修复方案（tool-pipeline.md）· 深度技术评审

> 被评文档：`docs/design/tool-pipeline.md`（F1–F7）
> 评估报告出处：主仓 `docs/optimization-review-vs-codex.md`（第二/四/十一章）
> 评审依据：worktree HEAD 实际代码（`agent/`、`dino/`、`pkg/`、`trigger/`）+ Codex 参照实现（`~/rust/codex/codex-rs`）
> 评审日期：2026-08-28

---

## 0. 结论摘要

设计整体质量**高**：事实引用绝大部分准确，方案针对性强，迁移顺序合理，F2/F4/F5/F6/F1 的五步走设计成熟。**可以进入实现**。

但存在 **2 个必须修改的 BLOCKER**、**8 个落地时应采纳的 RECOMMENDED**、**4 个可选 OPTIONAL**：

- **BLOCKER-1（F2 事实性错误）**：设计 §1.2 与 §3 对「MCP/bash 错误**终止本轮 tool 迭代循环**、`Execute` 返回 `hasMore=false`」的**现状断言与真实代码不符**。真实代码中任何 `err != nil` 都会被 `buildToolCallResults` 追加进 `intermediateSteps`，而 `executeIteration` 的 `hasMore = len(intermediateSteps) > 0`（`agent_execution.go:1090`、`:1353`）恒为 **true**。**错误（含 MCP 错误）从不停迭代——它会继续迭代直到 model 不再请求工具、maxIterations 或 doom loop。** 因此 F2 的「收益描述」（修复 MCP 错误硬终止迭代）是**虚构的收益**；F2 的真实收益只剩「limiter 结果上限生效」，工作量/收益叙事需要重写。这也放大了 F2 把**所有**错误转 `ok:false` 喂回模型的风险：当前循环本来就会把这些错误继续喂给模型，`nonFatalTool` 只是换了个载体（从 tool message 的 observation 变成结构化 map），而**新增的**循环放大主要来自「审批被拒/权限拒绝」这类**不可恢复**错误被转成了可恢复——见 BLOCKER-2。
- **BLOCKER-2（F2 审批拒绝被吞）**：`ApprovalTool.Execute` 的「rejected by user」（`defined_tool.go:385`）与 `ExternalPathApprovalTool` 的「denied」（`:511`）是**不可恢复**的用户否决。F2 的包装顺序把 `nonFatal` 放在 approval **外层**，会把这些错误转成 `{ok:false}` 喂回模型 → 模型重试审批 → 再次否决 → 直到 doom loop。**必须**让 approval 拒绝作为 fatal 错误透传（这正是 F3 的范畴，但 F2 阶段就必须处理，否则 F2 上线即引入比现状更差的 UX）。最小解：`nonFatalTool` 对审批拒绝错误（或其包装的错误码）直接透传。

**F1 的核心理念正确（单一入口预留字节），但 HeaderBudget=512 与正文预算下限 maxLen/4 会互相矛盾**（RECOMMENDED-1），且「history 层不再二次截断」的论证链里有一步站不住（RECOMMENDED-2）。F4/F5/F6 方案正确、风险可控。F3/F7 单列合理。F1 是唯一改模型可见输出格式的改动，release note 处理偏弱（RECOMMENDED-6）。

**最危险的是 F6**（消费者 `for result := range stream` 退出时机 + 引擎引擎级 ctx 与 turn ctx 错配，见 BLOCKER-3 风险段）——设计在 §6.2 自己点出了死锁，缓解却依赖一个不存在的「引擎结束即 cancel」不变量。这是落地时**必须写死锁回归测试**的一项。

迁移顺序 **F2→F4→F5→F6→F1** 总体合理，但 F2 在 BLOCKER-1/BLOCKER-2 修正前不应排在第一位；建议把 **F1 提到 F2 之前** 或先做 F6（理由见 §4）。

---

## 1. 事实核查结果（逐条 pass/fail）

> 所有行号均对照 worktree HEAD 真实代码。`✅ pass` = 与代码一致；`❌ fail` = 与代码不符（点名指出）；`⚠️ 存疑` = 基本成立但论证有缺口。

### 1.1 截断链路（F1 的现状描述）

| 设计引用 | 真实代码 | 判定 |
|---|---|---|
| `SanitizeToolResult` 递归截断 string 到 `maxTextLen`（`toolresult.go:19-20`） | ✅ `toolresult.go:19-20` 对 `string` 调 `truncateText(val, maxTextLen)` | ✅ |
| `content` 数组 `text` 项截断、`image` 项剥离 data 写 `bytes`/`omitted`（`:24-32,48-62` / `:64-76`） | ✅ `toolresult.go:24-32`（map 内 content 分支）、`:48-62`（sanitizeContentItem text）、`:64-76`（image） | ✅ |
| `SanitizeToolResult` **真的递归截断** | ✅ `sanitizeToolResultValue` 对 `map`/`[]interface{}` 递归，见 `toolresult.go:14-46` | ✅ |
| `FormatToolResult` JSON 化在 `agent/types/agent.go:336-351` | ✅ `agent.go:336-351`（`json.MarshalIndent` 2 空格缩进） | ✅ |
| `FormatToolResult` 可能**再膨胀** | ✅ 缩进 2 空格对嵌套 map 增加显著字节；且 `SanitizeToolResult` 只截**字段内 string**，JSON 括号/键名/缩进不在预算内 | ✅ |
| `TruncateToolResult` 在 `truncate.go:10-30`，无 writeDir → `TruncateString`（`agent.go:303-309`）**只留头 + `"..."`，不保留中间** | ✅ `truncate.go:17-19` → `TruncateString`（`agent.go:304-309`）`s[:maxLen] + "..."` | ✅ |
| 有 writeDir → `display = content[:maxLen] + "\n…(truncated). Full output saved to: …"`（`truncate.go:28`）——**头截断** | ✅ `truncate.go:28` | ✅ |
| 省略标记/提示语**不在 maxLen 预算内**，产物长度 = `maxLen + marker` | ✅ `truncate.go:28` 中 marker 拼在 `content[:maxLen]` 之后 | ✅ |
| `TruncateToolResult` 有 writeDir 时**只留头**（不做中间保留） | ✅ `truncate.go:28` | ✅ |
| history 层 `trimHistoryToTokenBudget`（`agent_execution.go:987-1017`）对**已截断内容**再切一刀 | ⚠️ 见下 | ⚠️ |
| `RoughTokensForMessage`（`tokens.go:20`） | ✅ `tokens.go:20` | ✅ |

**对「history 层二次截断」的核实**：`trimHistoryToTokenBudget`（`agent_execution.go:987-1017`）**不是**「对单条 tool 消息内容再切一刀」，而是**从后往前整条丢弃** tool 消息（`:1003-1016`，`used+c > rem` 即停，丢掉更老的消息）。它**永远不会把已截断的 tool 内容再切开**——它只丢整条。设计 §1.1 说「history 层按 `RoughTokensForMessage` 对已截断内容再切一刀」**不准确**（见 RECOMMENDED-2 的完整论证）。「二次截断」真实的另一个来源是 `TruncateString` 的 `s[:maxLen]` 会**切开 UTF-8 多字节字符**产生乱码（`agent.go:308` 字节切片非字符边界安全）——设计 F1 用 `TruncateMiddle` UTF-8 安全扫描修正这一点，方向正确。

### 1.2 调用点（agent_execution.go:270-274）

| 设计引用 | 真实代码 | 判定 |
|---|---|---|
| `agent_execution.go:270-274` 三行调用链 | ✅ `:270` truncationLength、`:271` Sanitize、`:272` Format、`:273` Truncate、`:274` toolCallData | ✅ |
| `getToolTruncationLength` 默认 2048（`agent_cache.go:48-63`） | ✅ `agent_cache.go:48-63`，末行 `return types.MaxTruncationLength`（2048） | ✅ |
| **`TruncateToolResult` 第三个返回值被忽略**（`observation, _, _ :=`） | ✅ `:273` 丢弃 `filePath`——**但这是关键发现**：当前 `buildToolCallResults` **从未把落盘路径写进 observation 之外任何地方**，模型只能靠 `truncate.go:28` 内嵌的 PATH 文本读取。F1 若改走「正文不落上下文、只给 PATH」，需确认 UI/消费者不需要原文。 | ⚠️ |

### 1.3 `nonFatalTool` / `toolResultLimiter` 死代码（F2）

| 设计引用 | 真实代码 | 判定 |
|---|---|---|
| 实现存在 `tool_wrappers.go:17-68` / `:71-159` | ✅ | ✅ |
| `nonFatalTool` 错误 → `{ok:false, tool, error, hint}` 喂回模型（`:48-53`） | ✅ `tool_wrappers.go:48-53` | ✅ |
| `LoopDetectedError` 例外透传（`:42-46`） | ✅ `tool_wrappers.go:42-46` | ✅ |
| `toolResultLimiter` 默认 120KB/60KB（`:81-85`） | ✅ `tool_wrappers.go:81-85` | ✅ |
| **零引用断言**：全仓 grep `WrapNonFatalTool`/`WrapToolResultLimiter` 仅命中自身与测试 | ✅ 实测 grep 仅 `tool_wrappers.go:21,77` + `tool_wrappers_test.go` | ✅ |
| factory 只接 3 个包装器（`factory.go:548-604`）：path→approval→loopDetection | ✅ `factory.go:561-565, 584-588, 601`（subagent 处 `:601` 只包 loopDetection） | ✅ |
| 后果：「MCP 调用错误作为硬错误经 `results[idx].err` → `toolObservationError` → 喂回模型但**终止本轮 tool 迭代循环**（`Execute` 返回 `hasMore=false`，`agent_execution.go:1090`）」 | ❌ **事实错误**。见 BLOCKER-1。真实代码：`buildToolCallResults` 对 `r.err != nil` 追加 `intermediateSteps`（`:253-260`），`executeIteration` 返回 `hasMore = len(intermediateSteps) > 0`（`:1090`）→ **true**；`executeStreamIteration` 同（`:1353`）。错误**从不**终止迭代循环。 | ❌ |
| 「MCP/bash 错误硬终止」在评估报告 §11.1 原文 | ⚠️ 评估报告原文即含此断言（`optimization-review-vs-codex.md:245`），**评估报告本身就错了**，设计沿袭了错误。 | ❌ |

### 1.4 errgroup 无并发上限（F4）

| 设计引用 | 真实代码 | 判定 |
|---|---|---|
| `runToolCallsByLayer`（`agent_execution.go:293-426`）拓扑分层 | ✅ `:310` `groupSortedToolCallsByLayer` | ✅ |
| 每 layer 建 `errgroup.WithContext`（`:321`），`g.Go`（`:334`）无 `SetLimit` | ✅ `:321` `g, gctx := errgroup.WithContext(ctx)`；`:334` `g.Go(...)` 前无 `g.SetLimit(...)` | ✅ |
| 仅有的闸门是 `MaxToolCallsPerIteration` 总数上限（`prepareToolCalls`，`:233-237`） | ✅ `:234-236`，`cfg.MaxToolCallsPerIteration > 0` 时截断 | ✅ |
| 「N 个 bash/MCP 全量并发」 | ✅ errgroup 无 limit，同一 layer 内无依赖工具全并发 | ✅ |
| `MaxToolCallsPerIteration` 控制**并发度**的否定 | ✅ 它只切列表头部，不管并发 | ✅ |

**注意**：设计 §4.1 说「排队工具被跳过（results[idx] 保持零值，exists[idx] 已为 true）」需要修正——`results` 初始化就是全零值，`exists` 是按工具名查到的 true，Acquire 失败写 `stepResult{err: gctx.Err()}`（§4.4）才正确。设计自己在 §4.4 处理了，OK。

### 1.5 ApprovalStore.Respond 非阻塞丢弃（F5）

| 设计引用 | 真实代码 | 判定 |
|---|---|---|
| `defined_tool.go:306-321`：`select { case ch <- approved: default: Warn }` | ✅ `defined_tool.go:314-320` | ✅ |
| 通道 `make(chan bool, 1)`（`:271`） | ✅ `defined_tool.go:271` | ✅ |
| `RequestApproval` 消费上一个值（`:293`）后再 `Respond` 必走 `default` | ✅ `:292-296` 消费 `<-ch` 后 `cleanup()` **从 pending 删掉**；再次 `Respond` 时 `ch == nil` 直接 return（`:311-313`）。**注意**：double-respond 的第二次实际走「未知 id」路径，不是「通道满」路径。通道满只发生在 `RequestApproval` 还在阻塞等值时（比如用户连点两次，第二次响应在消费前到达）。设计 §5.2 对此语义的描述基本对，但把「必走 default」归因于「通道满」略不准确——更多是「已 cleanup 的 nil ch」。 | ⚠️ |
| `Clear`（`:323-333`）非阻塞 | ✅ `:323-333` | ✅ |
| 调用链：外部 → `dinoFactory.RespondToolApproval`（`factory.go:705-709`） | ✅ `factory.go:705-709` | ✅ |
| 「仓库内无其他调用方」 | ✅ 实测 grep 全仓 `RespondToolApproval` 仅 interface（`:290`）+ impl（`:705`）；**但**外部入口是 `dino/client.go` 的 `OnApproval` handler（`:420`）——事件层面才有真正的消费者（`HandlerBuilder.onApproval`）。**没有发现任何**实际的 `RespondToolApproval` 调用方（无 gateway/HTTP 实现，`ApprovalSender` 也没有实现者——`SendToolApprovalRequest` 全仓无 impl）。⚠️ 这意味着 F5 在**当前仓库**影响面是「无人调用」，是死路径的健壮性改进；设计 §1.4 的「外部（HTTP/MCP 网关）」调用链**在本仓库不存在**。 | ⚠️ |
| `ApprovalTool.Execute` 消费通道（`:292-296` 的 `<-ch`） | ✅ `:292-296` | ✅ |

### 1.6 流式事件背压（F6）

| 设计引用 | 真实代码 | 判定 |
|---|---|---|
| `resultChan := make(chan types.StreamResult, types.DefaultChannelBuffer)`（`:727`，默认 50，`agent.go:17`） | ✅ `agent_execution.go:727`；`agent.go:17` DefaultChannelBuffer=50 | ✅ |
| `ae.resultSender`（`:731-737`）满即静默丢 | ✅ `:731-737` `select { case resultChan <- result: default: }` | ✅ |
| 直接 `resultChan <-` 的路径（`:782,791,802,813,1129,1146,1225,1282,1287`）是阻塞式 | ✅ 逐一核对均无 select | ✅ |
| 消费者 `dino/session/session.go:409-462`，每 event `s.emit`（`:220-239`）buffer=10 + 5s 超时 | ✅ `session.go:409-462`；`session.go:220-239`（`OutputBufferSize` 默认 10，`session.go:39`；5s 超时 `:224-237`） | ✅ |
| 「消费端本身有兜底，但上游在 resultChan 处丢的是完整 tool_event，消费端无从补回」 | ✅ 正确 | ✅ |
| **设计 §6.2 死锁缓解**：`resultSender` 监听 `ae.ctx.Done()`；「引擎结束（`ExecuteStream` 返回后 close(resultChan)）时消费端必然退出 range，发送端在 ctx 取消时退出」 | ❌ **论证有缺口**。见 BLOCKER-3。`ae.ctx` 是**引擎生命周期** ctx（`agent.go:68-69,107-110`，仅 `Stop()` 时 `ae.cancel()`，全仓无其他 `ae.cancel()` 调用）；`ExecuteStream` 结束时**不会** cancel `ae.ctx`——`executeStreamWithIterations` 里的 `resultChan <- "end"`（`:1225`）和 close（`:772`）都在 `ae.ctx` 存活的情况下执行。若消费者停止 range（UI 断开），`ae.resultSender` 阻塞时 `ae.ctx.Done()` **不会**触发（除非有人调 `agent.Stop()`）。真正能救的是**调用方传入的 turn ctx**，但 `resultSender` 闭包捕获的只有 `resultChan`（`:727-737`），**没有** ctx。 | ❌ |

### 1.7 question 工具 stub（F7）

| 设计引用 | 真实代码 | 判定 |
|---|---|---|
| `question` 权限默认 `ActionDeny`（`permission.go:34`）；`ModeBuild`/`ModePlan` 放开（`:65,74`） | ✅ `permission.go:34`（question=Deny）、`:65,68`（Build）、`:74,77`（Plan） | ✅ |
| `QuestionTool.Execute` 永远返回 `EC_TOOL_EXECUTION_FAILED`（`question.go:45-51`） | ✅ `question.go:49-50` | ✅ |
| 全仓无 `SourceNodeName=="question"` / `plan_enter` / `plan_exit` 的 runner 侧处理 | ✅ 实测 grep 仅 `question.go:55` 定义 SourceNodeName，无消费 | ✅ |
| 「build/plan 模式下 question 可用但会硬错误终止迭代」 | ⚠️ **终止迭代**部分同 BLOCKER-1 错误——`EC_TOOL_EXECUTION_FAILED` 走 `intermediateSteps` → `hasMore=true` → **继续迭代**，不会终止。但「比默认拒绝更糟」的定性仍成立（模型真的会看到 question 工具的报错 observation 而不是「工具不可用」）。 | ⚠️ |

### 1.8 其他引用

| 设计引用 | 真实代码 | 判定 |
|---|---|---|
| `bash/command` 返回 `map{"exit_code": int}`（`command.go:110-124`） | ✅ `command.go:110-124` | ✅ |
| `trimHistoryToTokenBudget` 在 `agent_execution.go:987-1017` | ✅ `:987-1017` | ✅ |
| `RoughTokensForMessage` 用 ASCII/4 估算（`tokens.go:7-18`） | ✅ `tokens.go:7-18`（RoughTokenEstimate ASCII/4 + non-ASCII*2） | ✅ |
| 落盘阈值建议 `OriginalBytes > maxLen*8` | — 待定（§12.2） | — |
| `ToolResultWriteDir` 默认空，`config.go:193-203` | ✅ 但 `config.go:193-203` 是 Memory 段；`ToolResultWriteDir` 是 **agent 层**配置（`agent.go:84`），dino 侧**从未设置**（grep 确认），默认空串。设计引用的行号不准（应为 agent 层），结论正确。 | ⚠️ |
| F7 单列、`loadBuiltinTools` 去 `NewQuestionTool`（`factory.go:816`） | ✅ `factory.go:816` | ✅ |
| F2 默认值对齐 limiter 现值 `tool_wrappers.go:81-85` | ✅ | ✅ |
| dino `ToolConfig` 在 `config.go:70` 起 | ✅ `config.go:70-75` | ✅ |
| Codex `truncate.rs:15-36` split_budget=50/50 | ✅ `~/rust/codex/codex-rs/utils/string/src/truncate.rs`（split_budget `budget/2`，`truncate_middle_with_token_budget` 保留头尾） | ✅ |
| Codex `context.rs:485-509` response_header、`:511-536` output_budget 预留 | ✅ `core/src/tools/context.rs`（response_header `:485-509` 附近；`response_text` 的 `output_budget = byte_budget*1.2 - header.len() - 1`） | ✅ |
| **Codex 的预留系数是 `truncation_policy * 1.2`** | ✅ `context.rs:516` `(self.truncation_policy * 1.2).byte_budget().saturating_sub(...)` | ✅ |

---

## 2. BLOCKER（必须先改再落地）

### BLOCKER-1 — F2 基于错误事实：MCP/工具错误「从不终止迭代」，收益叙事不成立

**证据**：
- `agent_execution.go:253-260`：`buildToolCallResults` 对 `r.err != nil` 追加 `intermediateSteps`（错误文本经 `toolObservationError` 生成）。
- `agent_execution.go:1090`：`return result, len(intermediateSteps) > 0, nil`；`:1353` 同理。→ **任何错误都令 `hasMore=true`**。
- `executeStreamWithIterations`（`:1165`）`if !hasMore { break }` —— 有错误就继续下一轮。

**后果**：
1. 设计 §1.2「MCP 错误硬终止迭代」、§3.2 表格「MCP/bash 返回 err → 本轮 tool 迭代终止」、§11 风险表「原本 MCP 硬错误终止」**全部是虚构前提**。评估报告 §11.1 原判断也错，设计沿袭。
2. F2 的**实际收益只剩**「`toolResultLimiter` 120KB/60KB 结果上限生效」（这是真收益，防 token 溢出），以及「错误以结构化 `{ok:false,...}` 呈现」（比 observation 文本略好，但**不是**「修复迭代终止 bug」）。
3. 「迭代继续」的行为**现状已存在**，不是 F2 带来的。F2 落地后真正变的是**载体**：错误从 tool message observation 字符串 → 结构化 map。

**必须做**：重写 F2 的收益/行为描述；把 F2 定位从「稳定性 bug 修复」降级为「结果上限 + 错误结构化」，并独立验证 limiter 收益。

---

### BLOCKER-2 — F2 包装顺序会让「审批被拒」被 nonFatal 吞掉，产生比现状更糟的循环

**证据**：
- 包装顺序 `approval → limiter → nonFatal → loopDetection`（设计 §3.1）：`nonFatal` 在 approval **外层**。
- `ApprovalTool.Execute` 拒绝时返回普通 error（`defined_tool.go:385` `"tool '%s' was rejected by user"`），**不是** `EC_TOOL_AUTH_ERROR`（那只是 `toolObservationError` 里 `stderrors.Is` 的展示分支），是一个 `fmt.Errorf`。
- `ExternalPathApprovalTool` 拒绝同理（`defined_tool.go:511`）。
- `nonFatalTool.Execute`（`tool_wrappers.go:33-53`）对非 loop 错误一律转 `{ok:false}` 喂回模型。

**后果**：用户点「拒绝」→ 模型看到 `{ok:false, error:"tool X was rejected by user"}` → 大概率**重试同一工具** → 再弹审批 → 用户再拒 → ……直到 doom loop（`agent_execution.go:1173-1178`）。这在 BLOCKER-1 的前提下尤其严重：当前循环**本来就会继续**，而 approval 拒绝的**语义**是「请换方案/停下」，喂回模型重试是错误语义。

**必须做**（F2 落地前最小解）：
- 在 `nonFatalTool.Execute` 中对审批拒绝做透传。由于 `ApprovalTool` 返回的是裸 `fmt.Errorf`（无错误码），需要**先给 approval 拒绝包装一个可识别错误**（如 `EC_TOOL_AUTH_ERROR.Wrap(...)` 或新 `ApprovalRejectedError` 类型），再让 `nonFatal` 用 `errors.As/Is` 透传。
- 或者改包装顺序：`nonFatal → approval → limiter → loopDetection`（approval 在 nonFatal **外层**，拒绝时 error 先穿过 nonFatal 回到引擎的 `results[idx].err` 路径）。但这个顺序会让 limiter 在 approval 内层、审批结果不限额——不合设计意图。**推荐前者**（给 approval 拒绝加可识别错误 + nonFatal 透传），并把这条写成 F3 的「fatal 初稿清单」里最高优先级项。

---

### BLOCKER-3 — F6 死锁缓解依赖不存在的「引擎结束即 cancel」不变量（风险最高项）

**证据**：
- `ae.ctx` 是**引擎生命周期** ctx：`agent.go:68-69, 107-110`（`NewAgentEngine` 里 `context.WithCancel(context.Background())`）；唯一 `ae.cancel()` 在 `Stop()`（`agent.go:167`）。`ExecuteStream` 本身**从不** cancel `ae.ctx`。
- `resultSender` 闭包（`agent_execution.go:731-737`）只捕获 `resultChan`，**未捕获任何 ctx**。
- `executeStreamWithIterations` 结束时 `resultChan <- "end"`（`:1225`）然后 defer close（`:772`）——`ae.ctx` 全程存活。
- 消费者 `for result := range stream`（`session.go:409`、`trigger/http/handler.go:140`、`subagent.go:127`）在收到 `end` 或 `error` 后退出；**若 UI/网络断开但 range 没退出**（HTTP SSE handler 有 `ctx.Done()` 检查会退出；session 的 range 在 `ExecuteStream` 返回前不会退出——它等 `end`）。

**后果**：消费者停止消费且不退出 range 时，`resultSender` 阻塞在 `case resultChan <- result`，`ae.ctx.Done()` 不触发 → errgroup goroutine 卡死 → `runToolCallsByLayer` 不返回 → `executeStreamIteration` 不返回 → `ExecuteStream` 不 close → **永久卡死**。设计 §6.2 的「引擎结束即 close + ctx 取消，发送端必然退出」在**当前代码里不成立**。

**触发路径更宽**：
- 所有消费者的 `range stream` 接收本身**都无 ctx 守卫**——`session.go:409`、`trigger/http/handler.go:140`、`subagent.go:127` 都是裸 `for result := range stream`。HTTP 消费者虽然循环体内有 `ctx.Done()` 检查（`handler.go:141-145`），但那是**收到事件后**才执行；若引擎侧阻塞不 close，`range` 接收照样卡死。
- `session.emit`（`session.go:207-219` 的 `notifyObservers` + `:220-239`）在发 `s.output` 之前会**同步调用所有 observer**（`notifyObservers`，`:216-218`）。若任一 UI observer 的 `OnEvent` 永久阻塞（慢回调/死循环），`emit` 卡死 → 消费者停在 `:409` 的 emit 上不再读 `resultChan` → 引擎 `resultSender` 阻塞 → 死锁。这是设计完全没覆盖的路径。
- 即便调用方调 `CancelCurrentTurn`（`session.go:325-333`）取消 `turnCtx`，只要 errgroup goroutine 已阻塞在 `resultSender` 的**无 ctx 守卫阻塞发送**里，`g.Wait()`（`:423`）仍不返回——turn 取消也救不了当前实现。

**必须做**：
1. `resultSender` 的 select 必须监听 **调用方传入的 turn ctx**（`executeStreamIteration` 收到的 `ctx`），而不是 `ae.ctx`——即把 ctx 放进闭包。`turnCtx`（`session.go:342-346`）由 `cancelTurn` 在 `executeWithInput` defer 中取消（`:347-354`），turn 结束即解除阻塞，这才是对的信号。
2. 由于 `ae.resultSender` 是引擎级共享字段（`agent.go:91`），改法是把 ctx 作为 `resultSender` 闭包捕获（在 `ExecuteStream` 里 `ae.resultSender = func(...)` 时用 `ctx`），或把 resultSender 签名改为接收 ctx。
3. **落地必须加死锁回归测试**：消费者停止 range + 不读 + 引擎不 Stop → 断言 `ExecuteStream` 在 ctx 取消后能退出（对应设计测试清单 #21，但断言前提要改成「turn ctx 取消」而非「引擎 cancel」）。

---

## 3. RECOMMENDED（落地时采纳）

### R-1（F1）`HeaderBudget=512` 与正文预算下限 `maxLen/4` 冲突
设计 §11 风险表给 `HeaderBudget=512` 封顶 + 正文预算下限 `max(0, maxLen - min(headerLen, HeaderBudget, maxLen/4))`。当 `maxLen=2048` 时 `maxLen/4=512`，与 HeaderBudget 相等，无冲突；但当 `maxLen` 是**工具级**小值（`getToolTruncationLength` 支持 tool metadata 覆盖，`agent_cache.go:53-58`），如 `maxLen=256`：`min(headerLen, 512, 64)=64`，正文预算=192——如果 header 实际 300B，就超过 64 上限，**header 会被截掉一截**，与「PATH 行优先级最高」冲突。建议把 `maxLen/4` 下限改成**正文预算下限**（保证至少 `maxLen/4` 给正文，header 让位），而不是让 header 吃正文。

### R-2（F1）「history 层不再二次截断」的论证链站不住——但要修的是**描述**，不是方案
如 §1.1 所述，`trimHistoryToTokenBudget` 是**整条丢消息**，从不切开单条 content；`RoughTokensForMessage` 的 token 估算与字节预算也**不同尺度**（ASCII/4 + 非 ASCII×2，`tokens.go:7-18`）——「同尺度」不成立。真实情况是：
- 二次截断的**真凶**是 `TruncateString` 的 `s[:maxLen]`（UTF-8 不安全）与 `truncate.go:28` 的 marker 超预算。
- F1 的「单一入口 + 预留」仍然是对的（它保证**观测串 ≤ maxLen**、marker 完整），只是「history 层因此不再二次截断」这句要改成「history 层本来就不切开单条 content，F1 主要收益是 marker/UTF-8 完整 + 保留中间 + 落盘路径可靠」。
- 建议在设计里把 §2.4 的推理改为准确表述，避免 reviewer 读到错误论证而误判。

### R-3（F1）`OutputHeader` 的 `OriginalTokens = OriginalBytes/4` 与 `RoughTokenEstimate` 不一致
`RoughTokenEstimate` 是非 ASCII×2（`tokens.go:9`），`OriginalBytes/4` 对 CJK/UTF-8 输出低估约 4 倍。header 里标 `original_tokens` 会被模型/日志当成真 token 数。建议要么用 `RoughTokenEstimate`，要么把字段改名 `original_bytes` 保留字节数（header 已有 `OriginalBytes`，`OriginalTokens` 二选一即可——设计自己也标注了「与 OriginalBytes 二选一」，建议直接删掉 `OriginalTokens`，减少歧义）。

### R-4（F2）limiter 120KB 与截断 2048 差 30 倍：默认值应下调，且 limiter 阈值应**按观测预算联动**
设计 §12.3 提出「是否调低到 32KB」，这是对的但表述偏软。论证：limiter 管的是**JSON 原样**上限，2048 是**观测串**上限；120KB JSON → `SanitizeToolResult`（2048）→ `FormatToolResult`（膨胀）→ `TruncateToolResult`（2048）——**大头全被截断丢掉**，limiter 的 120KB 只省了「先 JSON marshal 再截」的浪费，且 **limiter 返回 `{ok:false}` 的错误 map 本身会进 `SanitizeToolResult`**（非 string 分支递归截到 2048），不会爆。真正的浪费是 120KB 里大部分被截断扔掉。**建议**：limiter 默认 32KB/16KB，或按 `DefaultToolResultMaxLen`（agent 配置）联动；保留配置项覆盖。

### R-5（F4）semaphore 排队是「同层内」排队，per-layer 与全局的选择建议保留全局，但要处理「后层等前层」的层间队头阻塞
设计选全局 semaphore 正确（per-layer 会层数×N）。但注意：拓扑分层后，**第 2 层工具必须等第 1 层全部完成**（`g.Wait()` 在 layer 循环内，`:423`），而全局 semaphore 在**第 1 层内**就限了并发——如果第 1 层有 N 个慢工具占满 semaphore，第 2 层的依赖被拖慢。这是**期望**的（依赖必须等），但要写进测试/文档。另外 `sem.Acquire` 排队时**无优先级**——先到先得，与工具依赖顺序无关（同层内本来就无依赖，无妨）。

### R-6（F1）release note 处理偏弱
设计 §9 说「F1 是唯一改模型可见格式的改动，落地时建议配一条 release note」。但 F1 还改了两个**消费端可见**的东西：① `tool_callback.go:34` 的 `SendToolResult` 用 `FormatToolResult(output)` 直接格式化**未截断**结果——这不受影响（它不走 TruncateToolResult）；② **UI 依赖的 observation 字符串格式**（header 行、`Output:` 引导、`…N chars truncated…`）会变。建议 release note 明确：模型提示词若解析 tool 输出格式（如 grep `"Full output saved to:"`）需同步。实际上**当前无模型 prompt 依赖该格式**（grep 确认无），可标注「已验证无依赖」。

### R-7（F5）F5 在**当前仓库是无调用方死路径**——应确认外部调用方存在后再改语义
`RespondToolApproval` 全仓无外部调用，`ApprovalSender` 无实现者。F5 的阻塞+超时改造**无害**但**在当前仓库无人受益**（且 `approvalRespondTimeout=5s` 与 `NewApprovalStore` 的审批超时 `5min` 是两个不同超时，设计未区分）。**建议**：确认 gateway/HTTP 层（`trigger/gateway/protocol.go:18` `MethodToolApproval`）确实会把审批响应接进 `RespondToolApproval`（当前**没有**）；若没有，F5 应降级为 OPTIONAL 或与 F3 一起等「审批链路真正接线」后再做。同时给 `Respond` 的返回值语义加上 `approved` 参数为 false 时**不应预批准**的明确测试（R-7 补充：预批准语义只在「同一 requestID 第二次响应」时发生，测试 #17 已覆盖）。

### R-8（F6）session.emit 的 5s 超时与 engine 层「不丢」语义长期应统一——设计已在 §10 标注，建议落地时顺手把 `emit` 的丢弃从 `logger.Error` 升级为带事件的丢弃计数
设计 §6.3 已点出两层背压语义不同（engine 不丢、session 5s 超时丢）。这是**对**的设计取舍（engine 层不丢是正确性，session 层超时是保活兜底），不需要改；但建议 session.emit 丢弃时发一个可观测计数（metrics/日志字段），否则「5s 超时丢」仍然静默。

---

## 4. 迁移顺序意见

设计 §9 顺序：**F2 → F4 → F5 → F6 → F1 →（F3/F7）**。总体合理（先接线/小改，再稳定性，最后格式变更），但结合 BLOCKER 有修正：

1. **F2 不应排第一**。BLOCKER-1 已证 F2 的核心收益是虚构的，BLOCKER-2 显示 F2 直接上线会引入「审批拒绝→重试循环」的更糟行为。F2 若要落地，必须先做 BLOCKER-2 的最小修（approval 拒绝透传）。**修正后的 F2**（结果上限 + 错误结构化 + 审批拒绝透传）仍可排第一，但工作量从「极小」升到「小」。
2. **建议把 F6 提前到 F2 之前或并行**。F6 是**正确性 bug**（静默丢事件），影响所有消费者（UI/日志/approval/trace），且改动小、独立（只动 `resultSender`）。F6 的 BLOCKER-3（ctx 修复）与 F2 无依赖。按「先修正确性 bug、再接线、再并发、再格式」：**F6 → F4 → F5 → F2（修正后）→ F1 →（F3/F7）** 更顺。
3. **F1 应尽早**，因为它是唯一改模型可见格式的改动，越晚做越容易与其它改动冲突。但 F1 依赖 F6 不动（它们都改 `agent_execution.go` 同一区域 `:270-274` vs `:727-737`，不冲突）。放第五位可接受，但若排期允许，F1 可提到 F4 之前。
4. **F3/F7 单列**：同意。F3 的 fatal 初稿清单（schema 校验 `:344`、`EC_TOOL_AUTH_ERROR`、`EC_TOOL_INPUT_ERROR`、`LoopDetectedError`）方向对，但**BLOCKER-2 的「审批拒绝」必须进清单**——这是 F3 里优先级最高的一项，甚至应先于 F2 落地。F7 依赖「question 能力是否近期要用」的产品决策（§12 无此项决策），建议保持单列。
5. 每一步独立 commit 可回滚：**同意**，这是本设计最强的工程优点。

---

## 5. 覆盖度检查（任务要求是否都答到）

| 评估报告问题 | 设计是否覆盖 | 备注 |
|---|---|---|
| F1 工具输出二次截断 | ✅ | 方案正确，但现状论证有一处失真（R-2）+ HeaderBudget 冲突（R-1） |
| F2 nonFatal/limiter 死代码接线 | ✅ 但 | BLOCKER-1 收益虚构、BLOCKER-2 引入审批循环——接线本身对，**收益叙事与副作用必须重写** |
| F3 工具错误二分 | ✅ 单列 | 合理；BLOCKER-2 的审批拒绝应是 F3 最高优先级 |
| F4 并发无上限 | ✅ | 方案正确，全局 semaphore 决策正确 |
| F5 ApprovalStore.Respond 丢弃 | ✅ | 方案无害但当前是无调用方死路径（R-7） |
| F6 流式背压 | ✅ 但 | 正确性问题识别对、方案方向对，但死锁缓解不变量不存在（BLOCKER-3） |
| F7 question stub | ✅ 单列 | 现状判断有一处「终止迭代」失真（同 BLOCKER-1） |

**被设计遗漏的调用点/消费者**：
1. **`dino/tools/tool_callback.go:34`**：`ToolCallbackAdapter.OnToolResult` 用 `FormatToolResult(output)` 发**未截断**结果到 stream sender——这是另一条**绕过截断管线**的路径，UI 会看到「原始超大输出」的 tool_result（F1 只改 `buildToolCallResults`，不覆盖这里）。评估报告 §2 说「流式丢事件影响 UI/日志/approval/trace」，但**截断 F1 同样影响 UI 看到的 tool_result 大小**——设计没提这条。
2. **`trigger/http/handler.go:140`**：SSE 消费者，`for result := range stream` 且每事件 `sendSSEvent`（`ctx.Done()` 检查有，但若客户端断开写失败会 return 退出 range——**F6 死锁分析必须包含 HTTP 消费者**）。HTTP 消费者在 `ctx.Done()` 时退出，比较安全；session 消费者（`session.go:409`）没有主动 ctx 检查，是 F6 主风险点。
3. **`dino/agent/subagent.go:127`**：subagent 的 `ExecuteStream` 消费者，同样 `for result := range stream`——F6 死锁分析漏了它。
4. **trace**：仓库内无 trace 系统（grep 确认），评估报告提的 trace 消费者当前不存在，但 F6 修复是前置条件，无冲突。
5. **approval**：`ApprovalStore` 与流无关，但 BLOCKER-2 显示 approval 拒绝的**错误分类**（fatal vs recoverable）被 F2/F3 交叉影响——这是设计遗漏的耦合点。

---

## 6. 工作量估计评估

| 修复 | 设计估计 | 评审意见 |
|---|---|---|
| F1 | 中 | **中偏大**。`TruncateMiddle` 的 UTF-8 边界 + 字节预算 + header 优先级排序 + 落盘路径重构 + 测试 8 条，且要改 `TruncateToolResult` 签名（影响 `agent_execution.go:273` 一个调用点，OK）+ `OutputHeader` 探测逻辑。按设计测试清单（§10 F1 8 条）是「中」。 |
| F2 | 极小 | **上修为小**。接线本身极小（helper 一处），但 BLOCKER-2 要求给 approval 拒绝加可识别错误 + nonFatal 透传 + 测试，跨 `defined_tool.go`/`tool_wrappers.go`/`factory.go` 三处。 |
| F3 | 中 | 合理 |
| F4 | 小 | 合理（`x/sync/semaphore` 引入 + 配置透传 + 测试 4 条） |
| F5 | 小 | 合理但注意无调用方（R-7），测试 4 条 |
| F6 | 中 | **上修为中偏大**。BLOCKER-3 的 ctx 修复要把 ctx 捕获进 `resultSender` 闭包 + 死锁回归测试 + 可能动 `agent.go` 的 `resultSender` 签名。 |
| F7 | 中 | 合理（方案 A 跨 engine+dino） |

---

## 7. 总体评价

- **优点**：事实核查绝大部分准确（8/8 截断链路、死代码零引用、errgroup、resultSender 全部核实无误）；方案与 Codex 参照实现对齐（`truncate.rs`/`context.rs` 引用准确）；迁移分步可回滚是最强工程优点；风险表覆盖全面（尤其 F2/F4/F5/F6 各自的语义变化）。
- **核心问题**：两处**现状事实错误**（BLOCKER-1 的「错误终止迭代」、BLOCKER-2 的「approval 拒绝可恢复」）都集中在 **F2**——因为 F2 是「接线现有死代码」，设计的收益判断建立在评估报告的错误断言上。这让「F2 排第一、极小工作量、收益最大」的性价比叙事**塌了一半**。
- **风险最高项**：F6 的死锁缓解（BLOCKER-3）——设计自己点出了风险却给了不成立的缓解，落地时最容易在长会话 + 慢 UI 场景复现。
- **结论**：**F4、F5、F6、F1 可以进入实现**（F6 需先做 BLOCKER-3 的 ctx 修复，F1 需吸收 R-1/R-2）；**F2 必须先修 BLOCKER-1（收益叙事）+ BLOCKER-2（审批拒绝透传）再落地**；**F3 的清单必须加入审批拒绝（来自 BLOCKER-2）**；F5 建议降级等待审批链路真实接线（R-7）；F7 维持单列，取决于产品决策。

---

## 附：评审纪律符合性

- 仅新增 `docs/design/review-tool-pipeline.md`，未改动任何源码/现有文档。
- 所有事实引用均对照 worktree HEAD 真实代码 + 主仓评估报告 + `~/rust/codex/codex-rs` 参照实现逐条核实。
