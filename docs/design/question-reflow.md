# Question 回答回流：`AnswerQuestion` 设计

> 出处：`tool-pipeline.md` §7（F7 方案 A）+ `tools-codex-eval.md` §8.9 question 行。
> 状态：设计（2026-08-29）。P2.1 已落地部分（哨兵结果 + `EventTypeQuestion`），本设计补上「回答回流」闭环。
> 分支：`question-reflow`（待建）。

---

## 0. 目标与范围

把 question 工具的「提问 → 回答」闭环补齐：UI 收到 `EventTypeQuestion` 后，经 `AnswerQuestion(toolCallID, answer)` 把回答注入，agent 以回答为下一条输入继续。

**核心决策：不做 engine「挂起迭代」机制**（F7 方案 A 的「挂起」语义不需要）。理由是：

- 当前 engine 迭代是「一个输入 → 完整执行循环」。question 工具返回哨兵后，`hasMore=false` 让当前 turn 自然结束。
- 回答作为**新输入**走现有 `s.input` 通道 → 新 turn。UI 的提问和回答是两个独立 turn。
- 这比「挂起当前 turn 等回答」简单得多，且不破坏 engine 的单向执行模型。

**对齐 S4/B2 先例**：subagent completion 唤醒就是「外部事件 → 注入新 turn」（`onSubagentCompletion`，session.go:326-343）。`AnswerQuestion` 走同一路径。

---

## 1. 现状盘点（grep 证据）

### 1.1 已落地（P2.1）

| 载体 | 位置 | 覆盖 |
|---|---|---|
| `SentinelQuestionResult{ok, question, ask_user}` | `agent/tools/builtin/runtime/question.go:57-71` | `QuestionTool.Execute` 返回哨兵（非 fatal） |
| `EventTypeQuestion` emit | `dino/session/session.go:506-519` | session 检测 question 工具 `tool_result` → emit，携带 `Question` + `ToolCallID` |
| `onQuestion` 回调 | `dino/client.go:402-405,448-451` | UI 经回调拿到 `(question, toolCallID)` |

### 1.2 缺口

1. **无 `AnswerQuestion` 通道**：`client.go`/`session.go` 事件注释提到的 `AnswerQuestion` 方法**不存在**。UI 拿到问题后无法把回答注入。
2. **`question` 权限默认 `ActionDeny`**（`dino/permission/permission.go:34`）；`ModeBuild`/`ModePlan` 放开（`:65,74`）但无回答回流 → 「放开但必报硬错」的中间态仍在（虽已从硬错改为哨兵，模型仍无法获得回答）。

### 1.3 可复用机制

| 机制 | 位置 | 复用点 |
|---|---|---|
| `s.input` 通道 | `dino/session/session.go:176,293-304` | `AnswerQuestion` 注入回答走此通道 |
| `processInputWithAgentInput` | `session.go:377-378` | 回答作为 `types.AgentInput` 处理 |
| `ObserveOneUserTurn` | `turn_observe.go:50-154` | 同步等待回答 turn 结束（可选，供调用方拿结果） |
| `onSubagentCompletion` | `session.go:326-343` | 「外部事件 → 注入新 turn」先例 |

---

## 2. 方案：`AnswerQuestion` + 哨兵 `hasMore=false`

### 2.1 engine 侧：哨兵 → `hasMore=false`

`buildToolCallResults` 返回后，`intermediateSteps` 里检测 question 工具的哨兵结果 → 结束本轮，不继续迭代。**两条执行路径都覆盖**：

- **流式**：`executeStreamIteration`（`agent_execution.go:1475`）tool 执行后，`hasQuestionSentinel(intermediateSteps)` → 返回 `hasMore=false`。
- **非流式**：`executeIteration`（`agent_execution.go:1365` 附近）tool 执行后，`hasQuestionSentinel(intermediateSteps)` → 返回 `continueIterating=false`。

```go
// agent_execution.go，两条路径 tool 执行后：
// question 工具返回哨兵 → 结束本轮，不再继续迭代（UI 回答会作为新输入）。
if hasQuestionSentinel(result.IntermediateSteps) {
    return result, false, nil // hasMore=false / continueIterating=false
}
```

`hasQuestionSentinel` 检测 `IntermediateSteps` 里是否有 `Action.Tool == "question"` 且其 `Observation` 是哨兵形态。**在 `buildToolCallResults` 内识别 struct 形态**（`r.result` 是 `SentinelQuestionResult`），把「需停迭代」标记进 `stepResult`，或直接在 `buildToolCallResults` 后检测 `IntermediateSteps` 的 `Observation` 字符串（经 `FormatToolResult` 序列化，JSON 含 `"ask_user":true`）。

**为什么在 engine 层**：`hasMore=false` 是 engine 的既有语义（tool 执行后不再迭代 = turn 正常结束）。哨兵本身已是非 fatal 结果，engine 只需「识别后停止」，无需新增错误类型。

### 2.2 session 侧：`AnswerQuestion`

```go
// dino/session/session.go
// AnswerQuestion 把用户对 question 工具的回答注入为下一条 user 消息。
// toolCallID 与 EventTypeQuestion 的 ToolCallID 对应（client.go:448-451）。
func (s *Session) AnswerQuestion(toolCallID, answer string) error {
    if s == nil || toolCallID == "" {
        return fmt.Errorf("question: tool_call_id is required")
    }
    select {
    case s.input <- types.NewAgentInput(fmt.Sprintf("[question answer for %s] %s", toolCallID, answer)):
    case <-s.ctx.Done():
        return s.ctx.Err()
    }
    return nil
}
```

- **注入形态**：带 `[question answer for <toolCallID>]` 前缀，让模型知道这是对先前提问的回答（对齐 `ObserveOneUserTurn` 的输入注入，`turn_observe.go:127`）。
- **不阻塞**：`s.input` 有缓冲，非阻塞投递；session 循环消费后执行新 turn。
- **幂等**：不校验 toolCallID 是否真实存在过（UI 应只对收到的 `EventTypeQuestion` 调一次）；空 toolCallID 拒绝。

### 2.3 client 侧：`AnswerQuestion` 入口

```go
// dino/client.go
// AnswerQuestion 把用户对 question 的回答发给 session。
func (c *Client) AnswerQuestion(toolCallID, answer string) error
```

`Client` 持有 session 引用（`client.go` 已持有），转发到 `session.AnswerQuestion`。

### 2.4 权限保持默认 deny

不改 `permission.go:34`（`question` 默认 `ActionDeny`）。`ModeBuild`/`ModePlan` 已放开（`:65,74`），回答回流落地后这两个模式下的 question 真正可用。默认 deny 避免「模型乱问」。

---

## 3. 变更文件清单

| 文件 | 动作 | 内容 |
|---|---|---|
| `agent/engine/agent_execution.go` | 修改 | `executeStreamIteration` 检测 question 哨兵 → `hasMore=false`；`hasQuestionSentinel` helper |
| `dino/session/session.go` | 修改 | `AnswerQuestion(toolCallID, answer)` 注入 |
| `dino/client.go` | 修改 | `AnswerQuestion` 转发 |
| 测试 | 新增 | §5 |

---

## 4. 风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| 哨兵检测漏（question 输出形态多样：struct vs JSON map） | 不停止迭代 | `hasQuestionSentinel` 同时处理 struct 与 map 形态（session.go:659-665 已有 `questionFromOutput` 先例） |
| 回答注入无关联校验 | UI 误调 | 文档声明只对 `EventTypeQuestion` 调一次；空 toolCallID 拒绝 |
| `question` 权限默认 deny | 模型看不到 | 保持现状；`ModeBuild`/`ModePlan` 已放开 |

---

## 5. 测试清单

| 测试 | 覆盖 | 断言 |
|---|---|---|
| `TestEngineQuestionSentinelStopsIteration` | engine | question 哨兵在 `IntermediateSteps` → `hasMore=false`；无哨兵 → 继续 |
| `TestSessionAnswerQuestion` | session | `AnswerQuestion` 注入 → session 循环消费 → 新 turn 执行 |
| `TestClientAnswerQuestion` | client | 转发到 session，toolCallID 空拒绝 |

---

## 6. 不做的事

- **不做 engine「挂起迭代」**（F7 方案 A 的字面挂起）：回答作为新 turn 注入，语义等价且更简单。
- **不改 question 权限默认值**：保持 `ActionDeny`。
- **不做「同一 turn 内继续」**：UI 回答永远是新 turn。
