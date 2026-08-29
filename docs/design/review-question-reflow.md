# 评审报告：question-reflow（AnswerQuestion 回答回流）

- 被评设计：`docs/design/question-reflow.md`（commit 见 git log）
- 评审基准：worktree `question-reflow`，对照 `agent/`、`dino/` 真实代码逐条核实（含 Go 类型断言语义编译验证）
- 评审纪律：只产出本报告，未改动任何代码/文档

---

## 结论摘要

设计方向（engine 层 `hasMore=false` 结束 turn + session 层 `AnswerQuestion` 经 `s.input` 注入新 turn）与真实架构一致，关键机制（序列化链、`Action.Tool`、`s.input` type-switch、`ClientSession` 内嵌 Session）均核查属实，**核心可行**。

但存在 **2 个 BLOCKER**，根因相同：Go 的命名类型 vs 匿名 struct 类型断言语义。

- **B1**：`questionFromOutput` 的匿名 struct 断言对命名类型 `SentinelQuestionResult` 永不命中 → 真实流 `EventTypeQuestion` **永不 emit**，回流闭环无起点。P2.1「已落地」名不副实。
- **B2**：`hasQuestionSentinel` 若照抄 `questionFromOutput` 的写法会同样静默失效；应改用 `Action.Tool == "question"` 工具名检测。

修复成本低，不推翻设计骨架。**建议顺序**：先修 B1（`questionFromOutput` 改断言命名类型 + 真实类型集成测试），再按 R2 工具名检测实现 engine 停止逻辑，最后实现 `AnswerQuestion`。

---

## 一、事实核查

| 设计断言 | 判定 | 证据 |
|---|---|---|
| `SentinelQuestionResult` 定义 + Execute 返回 struct | PASS | `question.go:18-22` 定义；`:66-70` 返回 struct（非 JSON 字符串） |
| `buildToolCallResults` 序列化链（Sanitize→Format→Truncate） | PASS | `agent_execution.go:357-394`；`FormatToolResult`（`agent.go:368-383`）输出 `{"ok":true,"question":"...","ask_user":true}` |
| `ToolCallData.Action.Tool` 可取工具名 | PASS | `tool.go:182-194`；`toolCallData()`（`agent_execution.go:139-149`）写入 |
| 流式/非流式返回点 + 插入位置 | PASS（行号有错，见 R1） | 流式 `executeStreamIteration` `:1657` 返回 `:1866`；非流式 `executeIteration` `:1373` 返回 `:1483` |
| `AnswerQuestion` 注入 `s.input` | PASS | `session.go:151,293-304,377-378`；`types.NewAgentInput` 命中 `case types.AgentInput` |
| Client 持有 session 引用 | PASS（签名缺陷，见 O3） | `client.go:18` map + `:150-151` 内嵌 |
| `questionFromOutput` 双形态先例 | **FAIL** | 见 B1 |

## 二、BLOCKER

### B1 — `questionFromOutput` 匿名 struct 断言永不命中命名类型
- `session.go:661-670` 用 `output.(struct{Ok bool...})` 匿名 struct 断言。Go 规范：命名类型 ≠ 未命名 struct 类型，断言永不命中。
- 真实流 `QuestionTool.Execute` 返回命名 `SentinelQuestionResult`，经 dino 包装链（toolResultLimiter/nonFatalTool/loopDetectingTool 全部透传 struct）到达 `OnToolResult`，最终 `ToolEvent.Output` 是命名 struct → 两个分支都不中 → 返回 `""`。
- **推论**：`EventTypeQuestion` 在真实流中从未触发。`question_test.go:8-17` 用匿名 struct 直测掩盖了 bug。
- **修法**：`questionFromOutput` 改断言 `runtime.SentinelQuestionResult`（dino/session import runtime 无环）；或先 JSON round-trip 再走 map 分支。补真实命名类型测试。

### B2 — engine `hasQuestionSentinel` 若照抄先例会静默失效
- 若用匿名 struct 断言必然不中；`r.result` 只在 `buildToolCallResults` 内存在，`IntermediateSteps` 里只有 JSON 字符串。
- **修法**：只查 `Action.Tool == "question"`（R2），零解析成本，对 `TruncateToolResult` 截断边缘情形鲁棒。

## 三、RECOMMENDED

- **R1**：修正 doc 行号（`executeStreamIteration` `:1657`，`SentinelQuestionResult` `question.go:18-22`）。
- **R2**：`hasQuestionSentinel` 用工具名检测而非 JSON 解析。
- **R3**：回答注入的 UI 回声——`executeWithInput` 会对注入 AgentInput emit `EventTypeMessage`（Source=user），建议沿用 `onSubagentCompletion` 的 displayContent 标记（`session.go:425-427`）折叠，避免 UI 噪音。
- **R4**：测试必须用真实命名类型，不能像 `question_test.go` 用匿名 struct。

## 四、OPTIONAL

- **O1**：`Event.QuestionID` 字段（`event.go:46`）从未填充，实际用 `ToolCallID`——删或统一。
- **O2**：doc「不阻塞」措辞不准（`s.input` 缓冲满会阻塞）。
- **O3**：`Client.AnswerQuestion(toolCallID, answer)` 无 sessionID 不可路由——依赖 `ClientSession` 内嵌继承，或加 sessionID。

---

## 五、总体评价

**结论：设计骨架正确、可进入实现；但必须先修 B1（P2.1 的既有 bug），否则整个闭环无起点。** 评审发现了一个此前未被注意的既有缺陷（`questionFromOutput` 对命名类型无效），这比设计本身更重要——它意味着 question 功能实际从未工作过。建议实现顺序：B1 修复 → B2 工具名检测 → `AnswerQuestion` 注入 → 测试（真实类型）。

## 附：评审统计
- 事实核查：PASS 6 / FAIL 1（`questionFromOutput`）
- BLOCKER：**2**（B1 检测坏先例 / B2 engine 照抄会失效）
- RECOMMENDED：**4**（R1-R4）
- OPTIONAL：**3**（O1-O3）
