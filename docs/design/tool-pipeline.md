# 工具执行管线修复 · 详细落地方案

> 评估报告出处：[`docs/optimization-review-vs-codex.md`](../../optimization-review-vs-codex.md) 第四章（工具输出二次截断）、第十一章 11.1（nonFatalTool/toolResultLimiter 死代码、无并发上限、ApprovalStore.Respond 丢弃）、第二章（流式背压静默丢）。
>
> 范围：只设计，不改业务代码。落地时改动文件清单见 §8，全部集中在 `agent/` 与 `dino/` 源码。

---

## 0. 目标与决策摘要

| # | 修复 | 决策 | 默认值 | 工作量 |
|---|------|------|--------|--------|
| F1 | 截断保留中间 + 结构化 header + 预留字节 | 保留中间 + `…N chars/tokens truncated…` + 结构化 header；header 预留字节由**单一截断入口**承担 | header 上限 512B | 中 |
| F2 | 接线 nonFatalTool + toolResultLimiter | factory 接线，顺序 `approval → nonFatal → limiter` | 120KB / 60KB（保持现状默认） | 极小 |
| F3 | 错误二分（可恢复喂回 vs 致命） | 单列（不并入 F2），F2 先把错误**全部**转可恢复 | — | 中 |
| F4 | 并行无并发上限 | semaphore（`x/sync/semaphore`），排队非报错 | 默认 `GOMAXPROCS*2`，下限 4，上限 32 | 小 |
| F5 | ApprovalStore.Respond 静默丢弃 | 未知 id 阻塞 5s（默认），返回 `bool` | 5s | 小 |
| F6 | 流式事件背压静默丢 | 合流式降级：阻塞 + 若同类型事件堆积则合并丢弃中间 | 阻塞优先 | 中 |
| F7 | question 工具 stub | 单列，方案 A（engine 哨兵结果 → 挂起） | — | 中 |

迁移顺序见 §9：**F2 → F4 → F5 → F6 → F1 →（F3 / F7 单列）**。

---

## 1. 现状事实核对（file:line）

### 1.1 工具输出二次截断

双层截断链路（`buildToolCallResults`）：

1. `SanitizeToolResult` 递归截断（`agent/types/toolresult.go:81-86` → `sanitizeToolResultValue` → `truncateText`，`toolresult.go:7-12`）：
   - string 截断到 `maxTextLen`（`toolresult.go:19-20`）；
   - `content` 数组里 `text` 项截断（`toolresult.go:24-32,48-62`）；
   - `image` 项剥离 `data`、写 `bytes`/`omitted`（`toolresult.go:64-76`）。
2. `FormatToolResult` JSON 化（`agent/types/agent.go:336-351`），可能**再膨胀**。
3. `TruncateToolResult` 二次截断 + 超大输出落盘（`agent/types/truncate.go:10-30`）：
   - `maxLen <= 0` 回退 `MaxTruncationLength=2048`（`agent.go:18`）；
   - 无 writeDir → `TruncateString`（`agent.go:303-309`，**只留头 + `"..."`**，不保留中间）；
   - 有 writeDir → 写盘，`display = content[:maxLen] + "\n…(truncated). Full output saved to: …"`（`truncate.go:28`）——**头截断**；
   - 省略标记/提示语**不在 `maxLen` 预算内**，产物长度 = `maxLen + marker`，history 层（`trimHistoryToTokenBudget`，`agent_execution.go:987-1017`）按 `RoughTokensForMessage`（`tokens.go:20`）对**已截断内容**再切一刀。

调用点：`agent/engine/agent_execution.go:270-274`：

```go
truncationLength := ae.getToolTruncationLength(name)   // agent_cache.go:48-63，默认 2048
sanitized := types.SanitizeToolResult(r.result, truncationLength)
formatted := types.FormatToolResult(sanitized)
observation, _, _ := types.TruncateToolResult(formatted, truncationLength, writeDir)
```

### 1.2 nonFatalTool / toolResultLimiter 死代码

- 实现存在：`dino/tools/tool_wrappers.go`
  - `nonFatalTool`（`:17-68`）：错误 → `{ok:false, tool, error, hint}` 喂回模型；`LoopDetectedError` 例外透传（`:42-46`）。
  - `toolResultLimiter`（`:71-159`）：string/[]byte/Stringer/JSON 各路径，默认 120KB/60KB（`:81-85`）。
- 零引用确认：全仓 grep `WrapNonFatalTool`/`WrapToolResultLimiter` 仅命中 `tool_wrappers.go` 自身与 `tool_wrappers_test.go`。
- factory 只接 3 个包装器（`dino/factory.go:548-604`）：
  `wrapWorkspacePathTools`（`:561,584`）→ `NewApprovalTool`（`:563,586`）→ `WrapLoopDetection`（`:565,588,601`）。
- 后果：**结果上限未生效**；MCP/bash 错误作为硬错误经 `runToolCallsByLayer` 的 `results[idx].err`（`agent_execution.go:415`）→ `toolObservationError`（`agent_execution.go:73-89`）→ 喂回模型但**终止本轮 tool 迭代循环**（`Execute` 返回 `hasMore=false`，`agent_execution.go:1090`）。

### 1.3 并行无并发上限

`runToolCallsByLayer`（`agent_execution.go:293-426`）：拓扑分层后对每个 `layer` 建 `errgroup.WithContext`（`:321`），`g.Go`（`:334`）无 `SetLimit`。N 个 bash/MCP 全量并发。仅有的闸门是 `MaxToolCallsPerIteration` 总数上限（`prepareToolCalls`，`:233-237`），不控制并发度。

### 1.4 ApprovalStore.Respond 非阻塞丢弃

`dino/defined_tool.go:306-321`：`select { case ch <- approved: default: Warn }`。通道 `make(chan bool, 1)`（`:271`），一旦 `RequestApproval` 已消费上一个值（`:293`），再 `Respond` 必然走 `default` 静默丢。`Clear`（`:323-333`）同样非阻塞。

调用链：外部（HTTP/MCP 网关）→ `dinoFactory.RespondToolApproval`（`factory.go:705-709`）→ `ApprovalStore.Respond`。仓库内无其他调用方（grep 确认）。

### 1.5 流式事件背压静默丢

`ExecuteStream`（`agent_execution.go:722-828`）：

- `resultChan := make(chan types.StreamResult, types.DefaultChannelBuffer)`（`:727`，默认 50，`agent.go:17`）。
- `ae.resultSender`（`:731-737`）：`select { case resultChan <- result: default: /* skip */ }` —— 通道满即静默丢。
- 直接 `resultChan <-` 的路径（`:782,791,802,813,1129,1146,1225,1282,1287`）是**阻塞式**（无 select），不丢但可能卡死该 goroutine。
- 消费者：`dino/session/session.go:409-462` `for result := range stream`，每个 event `s.emit`（`session.go:220-239`）会投到 `s.output`（buffer=10）且 `emit` 带 5s 超时（`:224-237`）——消费端本身有兜底，但上游在 `resultChan` 处丢的是**完整 tool_event**，消费端无从补回。

### 1.6 question 工具 stub

- `question` 权限默认 `ActionDeny`（`dino/permission/permission.go:34`）；`ModeBuild`/`ModePlan` 放开（`:65,74`）但**没有任何 runner 处理**。
- `Execute` 永远返回 `EC_TOOL_EXECUTION_FAILED`（`agent/tools/builtin/runtime/question.go:45-51`）。
- 全仓无 `SourceNodeName=="question"` / `"plan_enter"` / `"plan_exit"` 的 runner 侧处理（grep 确认）。
- 即：build/plan 模式下 `question` 可用但会**硬错误**终止迭代——比「默认拒绝」更糟（默认拒绝时模型根本看不到它）。

---

## 2. F1 截断重构（第四章）

### 2.1 目标状态

1. **保留中间**（头尾都留，参考 Codex `truncate.rs:15-36` 的 `split_budget = 50/50` + 省略标记）。
2. **结构化 header**（参考 Codex `core/tools/context.rs:485-509` 的 `response_header`），含：chunk id / 耗时 / exit code / 原始 token 数 / 输出字节数。
3. **预留 header + 省略标记字节**（参考 `context.rs:511-536` 的 `output_budget = byte_budget - header.len() - 1`），保证 history 层（`trimHistoryToTokenBudget`）不再二次截断。
4. **单一截断入口**：所有截断（结构化、header、省略标记、落盘提示）都由一个函数做；落盘仅作为**替代方案**（超大输出写文件），不再与「结构化截断」叠加。

### 2.2 数据结构（新增 `agent/types/truncate.go`）

```go
// OutputHeader 工具输出结构化 header。任何字段为空则对应行省略（参考 Codex response_header）。
type OutputHeader struct {
    ChunkID         string        `json:"chunk_id,omitempty"`
    WallTime        time.Duration `json:"wall_time,omitempty"`       // 执行耗时
    ExitCode        *int          `json:"exit_code,omitempty"`       // bash 等可取到
    OriginalBytes   int           `json:"original_bytes,omitempty"`  // 截断前原始输出字节数
    OriginalTokens  int           `json:"original_tokens,omitempty"` // approx，= OriginalBytes/4
    TotalLines      int           `json:"total_lines,omitempty"`     // 原始行数
    SavedPath       string        `json:"saved_path,omitempty"`      // 超大输出落盘路径（替代截断）
    OmittedCharCount int          `json:"omitted_chars,omitempty"`   // 省略的字符数（与 OriginalBytes 二选一，避免冗余）
}

const (
    // HeaderBudget 预留字节上限；含 \n 与 "Output:" 引导行。
    // 2048*0.25=512 是 Codex 的 1.2 倍安全系数下界，避免 header 吃掉大部分正文预算。
    HeaderBudget = 512
)

// TruncationMeta 截断结果，供调用方（engine/buildToolCallResults）记录。
type TruncationMeta struct {
    Truncated     bool
    SavedFilePath string
    OriginalBytes int
}
```

### 2.3 核心函数（全部放 `agent/types/truncate.go`，不动 `toolresult.go` 的递归逻辑）

```go
// TruncateMiddle 保留头尾、中间省略标记（UTF-8 安全，字节预算）。
// 实现对照 Codex truncate.rs split_string（char 边界扫描），marker = "\n…N chars truncated…\n"。
func TruncateMiddle(s string, maxBytes int) (out string, omitted int)

// BuildOutputHeader 生成 header 文本（形如 Codex response_header）。
func BuildOutputHeader(h OutputHeader) string

// TruncateToolResult 重构后的单一入口。签名兼容旧调用（agent_execution.go:273），但语义改变：
//   - 保留中间；
//   - 先算 headerLen，正文预算 = maxLen - headerLen（HeaderBudget 封顶），marker 计入正文预算；
//   - 若仍需落盘（writeDir != "" 且 OriginalBytes 超过阈值），正文预算让给落盘提示行，
//     返回 header + 落盘提示（含绝对路径），正文不落上下文。
func TruncateToolResult(content string, maxLen int, writeDir string, header OutputHeader) (display string, meta TruncationMeta)

// SanitizeToolResult 保持签名（toolresult.go:81），但内部调用 TruncateMiddle 而非 truncateText，
// 且 maxTextLen 直接作为「正文+标记」预算（预留由外层 TruncateToolResult 统一做，见 §2.4 分工）。
```

### 2.4 `SanitizeToolResult` 与 history 层的关系（谁预留）

**关键决策：预留字节只做一次，且只由最外层 `TruncateToolResult` 承担。** 理由：

- 内层 `SanitizeToolResult` 的 `truncateText` 在**结构化中间层**（map/数组内的 string）逐字段截断，此时不知道最终 JSON 化后的总长，无法有意义地「预留」——它预留了，外层 `FormatToolResult` 的 JSON 括号/引号/缩进还会撑破预算。
- 外层 `TruncateToolResult` 拿到的是**最终观测字符串**（`formatted`），这是唯一能算准 `header + marker + 正文 ≤ maxLen` 的位置。
- history 层（`trimHistoryToTokenBudget`）按整条消息 token 预算裁剪，**永远可能再截**；我们的目标不是让它不截，而是让它截的**不是**「已经截过的中间区域」，即把「截断提示 + header」作为结构化整体一起裁掉（它本来就是一个 tool message 的完整 content）。

分工：

1. `SanitizeToolResult`：递归净化（image 去 data、超长 string 保守截断）——**保留中间 + 省略标记**，标记字节算在它自己的 `maxTextLen` 内。
2. `FormatToolResult`：不变（`agent.go:336-351`）。
3. `TruncateToolResult`：**唯一**做「header 预留 + 最终字节预算」的地方。
4. history 层（`trimHistoryToTokenBudget`）：不改；它用 `RoughTokensForMessage` 对整条 tool 消息裁，由于 tool 消息 content 已经是 `≤ maxLen` 的字符串，它的 token 估算（`tokens.go:7-18`，ASCII/4）与字节预算同尺度，二次截断只会发生在「预算比 maxLen 更紧」的极端场景——此时截掉的是尾部，不会把中间省略标记切开（可接受，且比现状「截掉标记让模型误以为输出完整」强）。

### 2.5 调用点与签名变更

| 文件 | 现状 | 变更 |
|---|---|---|
| `agent/types/truncate.go` | `TruncateToolResult(content, maxLen, writeDir)` | `TruncateToolResult(content, maxLen, writeDir, header OutputHeader)`；新增 `TruncateMiddle`、`BuildOutputHeader`、`OutputHeader`、`TruncationMeta`、`HeaderBudget` |
| `agent/types/toolresult.go` | `truncateText`（头截断） | 内部改调 `TruncateMiddle`；`truncateText` 可删或保留为「无 header 场景」包装 |
| `agent/engine/agent_execution.go:270-274` | 构造 `sanitized`→`formatted`→`TruncateToolResult(formatted, truncationLength, writeDir)` | 组装 `OutputHeader{ChunkID: toolCall.ID, WallTime: r.duration, ExitCode: <从 result 探测>, OriginalBytes: <r.result 字符串化长度>}`，传 `TruncateToolResult(formatted, truncationLength, writeDir, header)` |
| `agent/engine/agent_execution.go:91-96` | `stepResult{result, err, cached, duration}` | 不需要加字段；duration 已在 `:392` 记录。ExitCode 从 result 探测（见下） |

**ExitCode 探测**：bash/command 工具返回 `map{"exit_code": int}`（`agent/tools/builtin/runtime/command.go:110-124`）。在 `buildToolCallResults` 里对 `r.result` 做一次 `map[string]interface{}` 探测取 `exit_code`；探测不到就留空。MCP 结果无此字段，header 省略该行。

### 2.6 落盘路径行为变化

现状（`truncate.go:20-28`）：`content[:maxLen] + "\n…(truncated). Full output saved to: PATH…"`——落盘提示的 PATH 在 `maxLen` **之外**，本身可能被 history 裁掉，模型看到「截断」却不知道去哪读。

重构后：超大输出（`OriginalBytes > maxLen*8`，建议阈值可配）直接：
```
<header: chunk id / 耗时 / exit code / OriginalBytes / OriginalTokens / TotalLines>
<"Full output saved to: PATH — use read_file/grep to view.">
```
正文不出现；header+提示行总长受 `HeaderBudget` 约束（提示行如果超预算，PATH 必须保留、其余 header 行让位——实现按「PATH 行优先级最高」排 header 字段顺序）。

非落盘路径：
```
<header 行，各字段一行>
Output:
<TruncateMiddle 结果：头 50% + "\n…N chars truncated…\n" + 尾 50%>
```
header + `Output:` 引导 + 截断结果总长 `≤ maxLen`。

---

## 3. F2 接线 nonFatalTool + toolResultLimiter（第十一章 11.1）

### 3.1 接线位置与顺序

改 `dino/factory.go` 的两处工具循环（`sessionTools` 循环 `:550-567` 与 `sessionToolsProvider` 循环 `:570-591`），以及 subagent delegate 注册处（`:593-604`）。**抽一个 helper**，避免三处重复：

```go
// factory.go 内新增
func (f *dinoFactory) wrapSessionTool(t types.Tool, sessionID string, senderAdapter *toolEventSenderAdapter) types.Tool {
    wrapped := wrapWorkspacePathTools(t, f.config.WorkspaceRoot, sessionID, f.approvalStore) // 保持最内
    if _, ok := wrapped.(*ApprovalTool); !ok && needApproval(name) { /* 见下 */ }
    wrapped = dinoTools.WrapToolResultLimiter(wrapped, f.resultLimiterMaxBytes, f.resultLimiterMaxStringBytes)
    wrapped = dinoTools.WrapNonFatalTool(wrapped)
    wrapped = dinoTools.WrapLoopDetection(wrapped, sessionID, f.loopDetector, senderAdapter)
    return wrapped
}
```

**包装顺序决策：`approval → limiter → nonFatal → loopDetection`（最内到最外）。**

推演（从内到外）：
1. `approval`（含路径审批 `wrapWorkspacePathTools` + `ApprovalTool`）——**最内**：审批必须看原始输入/路径，且审批被拒要能终止（硬错误），不能被 nonFatal 吞成「喂回模型继续」。
2. `limiter`（`toolResultLimiter`）——在 approval 外层：限的是**执行结果**，不管审批层；且限流产物是 `{ok:false,error}` 普通 map（`tool_wrappers.go:104-107`），可被 nonFatal 正常返回。
3. `nonFatal`——在 limiter 外层：把 limiter 的「超大结果错误」和底层 MCP/bash 错误**统一转成喂回模型的可恢复结果**；`LoopDetectedError` 仍透传（`tool_wrappers.go:42-46`）。
4. `loopDetection`——**最外**：循环检测要看到真实调用（含失败调用），且 `LoopDetectedError` 必须穿透 nonFatal 让引擎终止（`tool_wrappers.go:220-228` 返回 `nil, loopErr`）。

> ⚠️ 现有代码里 approval 的 `needApproval` 判定在循环内散落（`:557-559,579-581`），`ApprovalTool` 构造需 `dangerous map[string]bool`。重构 helper 时把 `needApproval` 作为参数传入，保持判定逻辑不变。

### 3.2 结果上限生效后的行为变化

| 场景 | 现状 | 接线后 |
|---|---|---|
| MCP/bash 返回 `err != nil` | 硬错误 → `results[idx].err`（`agent_execution.go:415`）→ 本轮 tool 迭代终止 | `{ok:false, tool, error, hint}` 喂回模型，模型可改输入重试，**迭代继续** |
| 输出 > 120KB（byte/JSON） | 原样进 `SanitizeToolResult` → 截断后可能仍 > maxLen 且丢失原文 | limiter 返回 `{ok:false, error:"Tool result is too large…"}`（`tool_wrappers.go:151-156`），不吞 token |
| 输出 string > 60KB | 同上 | 同上（`:102-108`） |
| `LoopDetectedError` | 硬错误终止 | 仍硬错误终止（nonFatal 透传，`:42-46`）——**语义不变** |

### 3.3 配置接入

`dino/config.go` 的 `ToolConfig` 增加字段，默认值对齐 limiter 现值（`tool_wrappers.go:81-85`）：

```go
type ToolConfig struct {
    // ...现有
    ResultLimiterMaxBytes        int `yaml:"result_limiter_max_bytes,omitempty"`        // 默认 120_000
    ResultLimiterMaxStringBytes  int `yaml:"result_limiter_max_string_bytes,omitempty"` // 默认 60_000
}
```

`DefaultConfig()` 填默认值（`config.go:159-175`）。factory 里 `WrapToolResultLimiter(wrapped, f.config.Tools.ResultLimiterMaxBytes, f.config.Tools.ResultLimiterMaxStringBytes)`。

### 3.4 错误二分是否顺手引入

**单列（F3），不并入 F2。** 理由：

- `nonFatalTool` 现在把**所有**非 loop 错误转可恢复。真正的二分（Codex `function_call_error.rs:5-10`：`RespondToModel` vs `Fatal`）需要：
  - 定义 `FatalError` 类型（如 schema 校验失败、权限/审批被拒、工具不存在——这些是**输入错了**，重试也无效，应让引擎终止或换策略）；
  - `nonFatalTool` 只吞 `RespondToModel` 类错误，`FatalError` 透传；
  - 引擎 `runToolCallsByLayer` 对 fatal 错误提前终止（而非继续跑其他并行工具）。
- F2 先把错误**全部喂回模型**已经修复「MCP 错误硬终止迭代」的稳定性 bug，收益独立可验证。
- F3 需要改动 `agent/types/tool.go`（加错误类型）、`agent_execution.go`（fatal 短路）与 `tool_wrappers.go`（分类），是一个独立的中等改动，建议在 F2 落地后单独评审。

F3 待定设计要点（写在这里供后续排期）：
- `agent/types/tool.go` 新增 `type FatalToolError struct{ Err error; Reason string }`，实现 `Unwrap`。
- `nonFatalTool.Execute`：`errors.As(err, &fatal)` → 透传。
- 引擎：`results[idx].err` 若是 fatal，`runToolCallsByLayer` 里 `cancel()` 同层其余 goroutine（`errgroup` 已支持），`executeIteration` 终止本轮。
- 哪些算 fatal（初稿）：schema 校验失败（`agent_execution.go:344`）、`EC_TOOL_AUTH_ERROR`、`EC_TOOL_INPUT_ERROR`、`LoopDetectedError`。

---

## 4. F4 并发上限（errgroup 限流）

### 4.1 方案：semaphore + 排队

`runToolCallsByLayer`（`agent_execution.go:320-423`）内、`g.Go` 的回调**开头**获取许可：

```go
// 每个 layer 一个 semaphore（或整个 runToolCallsByLayer 共用一个，见下）
limit := ae.getToolParallelismLimit()
sem := semaphore.NewWeighted(int64(limit))
g, gctx := errgroup.WithContext(ctx)
for _, idx := range layer {
    idx := idx
    g.Go(func() error {
        if err := sem.Acquire(gctx, 1); err != nil { // 排队，不报错；ctx 取消则退出
            return nil
        }
        defer sem.Release(1)
        // ...现有执行体
    })
}
```

- **排队而非报错**：`sem.Acquire` 阻塞等许可；只有 `gctx` 被取消（context 取消/超时/错误）才返回，此时该工具调用被跳过（`results[idx]` 保持零值，`buildToolCallResults` 的 `exists[idx]` 已为 true 但 result 为零值——需在 `buildToolCallResults` 对「零值结果」打一个 `cancelled` 标记，见 §4.4）。
- **per-layer vs 全局**：选**全局一个 semaphore**（在 `runToolCallsByLayer` 开头建，所有 layer 共用）。理由：并行工具分属不同 layer（有依赖的先跑），若 per-layer 限制，每个 layer 都能全速跑 N 个，总并发 = 层数×N，起不到「全局压并发」的目的；而并行 bash/MCP 的数量由「同一层内无依赖的工具数」决定，全局 semaphore 能跨层限总量。缺点：后一层会等前一层释放——这是期望行为（依赖必须等）。

### 4.2 默认值与配置

- 默认：`max(4, GOMAXPROCS*2)`，封顶 32。
- 配置项加到 `agent/types/agent.go` 的 `AgentConfig`：

```go
// AgentConfig 新增
ToolParallelismLimit int `json:"toolParallelismLimit,omitempty"` // 0=默认；>0 用该值
```

- `NewAgentConfig()` 填 0（表示用默认）。
- `getToolParallelismLimit()`：`config.ToolParallelismLimit > 0` 用之，否则 `max(4, min(32, runtime.GOMAXPROCS(0)*2))`。
- dino 侧透传：`dino/config.go` 的 `Config` 加 `Tools.MaxToolParallelism int`（yaml `max_tool_parallelism`），`CreateSession` 里 `agentConfig.ToolParallelismLimit = f.config.Tools.MaxToolParallelism`（`factory.go:497-528` 一带）。

### 4.3 对行为的影响

- 无依赖的并行工具从「全量并发」变为「最多 N 个并发」，其余排队。
- 不影响正确性（errgroup 本来就等所有 `g.Go` 完成）；只影响 wall-time。
- 超限**排队**，与 `MaxToolCallsPerIteration`（`agent_execution.go:233-237`，**总数**截断）语义互补：一个是总量闸门，一个是并发闸门。

### 4.4 边界：被取消的排队工具

`sem.Acquire` 返回 error（ctx 取消）时不能留下「零值 stepResult 但 exists=true」——`buildToolCallResults` 会把零值当「成功但空结果」（`:267-274`）。处理：`g.Go` 内 Acquire 失败时给 `results[idx]` 写 `stepResult{err: gctx.Err()}`（`agent_execution.go:415` 现有错误路径已经会走 `toolObservationError`）。同 `errgroup` 语义：一个工具致命错误（F3 落地后）cancel 其余。

---

## 5. F5 ApprovalStore.Respond 静默丢弃

### 5.1 方案对比

| 方案 | 做法 | 优点 | 缺点 |
|---|---|---|---|
| A 阻塞（推荐） | 通道满时阻塞等接收方 | 语义简单、绝不丢 | 调用方（HTTP/MCP 网关 goroutine）可能挂住 |
| B 缓冲扩容 | `pending` 值改为队列/更大 buffer | 不阻塞 | 响应会过期（approval 已超时/被清理），丢弃是「过期丢弃」而非 bug |
| C 报错 | 返回 `error`，满即返回错误 | 调用方感知 | 调用方没法补救（人已点了批准） |

**选 A + 超时**：`Respond` 阻塞写，但带超时（默认 5s，对齐 `session.emit` 的 5s 兜底，`session.go:224-238`）。未知 requestID 立即返回（现状行为，保持）。

### 5.2 签名与实现

```go
// defined_tool.go:306-321 重构
const approvalRespondTimeout = 5 * time.Second

func (s *ApprovalStore) Respond(requestID string, approved bool) bool {
    s.mu.Lock()
    ch := s.pending[requestID]
    s.mu.Unlock()
    if ch == nil {
        return false // 未知/已清理
    }
    select {
    case ch <- approved:
        return true
    case <-time.After(approvalRespondTimeout):
        logger.Warn("[ApprovalStore] approval response timed out", slog.String("request_id", requestID))
        return false
    }
}
```

- 返回值语义：`true`=已送达，`false`=未知 id 或超时。调用方 `dinoFactory.RespondToolApproval`（`factory.go:705-709`）目前无返回；**保留**无返回值签名（外部接口不变），内部记一次 `logger`。为便于测试，暴露 `Respond(requestID string, approved bool) bool`，factory 忽略返回值即可（接口 `DinoFactory.RespondToolApproval` 不动）。
- **为什么阻塞不丢是对的**：`RequestApproval` 的通道 buffer=1（`defined_tool.go:271`），其消费方是 `ApprovalTool.Execute`（`defined_tool.go:292-296` 的 `<-ch`）。通道满只发生在「上一次响应还没被消费」——这通常意味着用户**连点两次**（第一次已消费并继续执行）。阻塞后第二次响应会送达给下一次 `RequestApproval`——这其实是**期望语义**（预批准）。超时兜底防止调用方永久挂住。
- `Clear`（`:323-333`）保持非阻塞（它就是要丢弃所有 pending，正常）。

---

## 6. F6 流式背压（静默丢 → 合流式降级）

### 6.1 决策：阻塞 + 合并

`ae.resultSender`（`agent_execution.go:731-737`）改为：

```go
ae.resultSender = func(result types.StreamResult) {
    select {
    case resultChan <- result:
        return
    case <-ae.ctx.Done(): // 引擎取消，停止发送
        return
    default:
        // 通道满：阻塞发送（保真）；若同一事件类型堆积超过阈值，合并丢弃中间。
    }
}
```

- **满时默认阻塞**（发送到 `resultChan` 或 ctx 取消）。这是最保真的方案：tool_event 是离散关键事件（tool_call/tool_result/tool_error），不可合并，宁可背压。
- **合并仅限 chunk/reasoning 这类内容事件**：若 `resultChan` 满且当前是 `chunk`，用一个小无锁缓冲 + 定时 flush 把同类型文本片段合并成一段（见下）。合并是**可选优化**，第一版只做「阻塞」，合并作为第二阶段（因为阻塞已解决「静默丢」这个正确性 bug，合并是吞吐优化）。
- 直接 `resultChan <-` 的路径（`:782,791,802,813,1129,1146,1225,1282,1287`）保持阻塞（本来就对）。

### 6.2 为什么阻塞安全（对调用方的影响）

消费者链路 `dino/session/session.go:409-462`：
- `for result := range stream` 消费；每个 event `s.emit`（`:220-239`）投 `s.output`（buffer=10）+ 5s 超时。
- 若 `s.output` 满（下游 UI/日志慢），`emit` 会阻塞 **5s** 后丢弃——**这是第二层背压**，已存在。
- 上游 `resultSender` 阻塞会卡住 `streamToolCallback.sendEvent`（`agent_execution.go:123-131`），进而卡住 `OnToolCall/OnToolResult`（`:133-196`）——这些回调在 `runToolCallsByLayer` 的 **errgroup goroutine 内**（`:338-340,396-397`），阻塞会让工具执行变慢。**但这是期望的**：下游慢，上游就该慢（背压传导），而不是丢事件。
- 风险：**死锁**。若消费者停止消费（比如 UI 断开但 `range stream` 没退出），`resultSender` 永久阻塞，errgroup goroutine 卡死 → `runToolCallsByLayer` 不返回 → `ExecuteStream` 卡住。**缓解**：`resultSender` 的 select 必须监听 `ae.ctx.Done()`（上面代码已含）；引擎结束（`ExecuteStream` 返回后 close(resultChan)，`:772`）时消费端必然退出 range，发送端在 ctx 取消时退出。dino 侧 `ExecuteResponse` 路径由 `turnCtx` 取消兜底（`session.go:401` `turnCtx`，`ObserveOneUserTurn` 的 `finish` 通道，`turn_observe.go:120-134`）。

### 6.3 对 trace / approval / 日志的影响

- **不丢事件**是它们的共同前提（评估报告 §2 原文：影响 UI、日志、approval、trace）。阻塞方案保证 tool_event 完整。
- approval 走的是 `ApprovalStore`（与流无关），但 tool_error 事件现在**不会丢**，意味着「MCP 调用失败喂回模型」这类错误也会被 UI 看到——与 F2 配合，用户能看到工具失败详情而非「静默消失」。
- `session.emit` 的 5s 超时丢弃（`:224-238`）**不在此次范围**（它是 dino 层的已存在兜底），但值得在风险里标注：两层背压语义不同（engine 层不丢、session 层 5s 超时丢），长期应统一（见 §10 风险）。

### 6.4 配置

`AgentConfig` 加 `StreamBufferSize int`（0=默认 50，`agent.go:17`），`ExecuteStream` 用 `ae.getStreamBufferSize()` 替代字面量 `types.DefaultChannelBuffer`（`:727`）。默认不变，给需要大缓冲的场景逃生。

---

## 7. F7 question 工具 stub（单列）

### 7.1 现状确认

- `question` 在 `ModeBuild`/`ModePlan` 放开权限（`permission.go:65,74`），但 `QuestionTool.Execute` 永远报错（`question.go:50`），且无 runner 处理 `SourceNodeName=="question"`。
- 它是「特殊工具」：应当**挂起迭代、把问题发给 UI、等用户回答**——但 agent engine 是纯执行引擎，没有「挂起等待用户」的机制。

### 7.2 两个候选方案

| 方案 | 做法 | 工作量 | 风险 |
|---|---|---|---|
| A. 引擎哨兵结果 | `QuestionTool.Execute` 返回**结构化结果**（不是错误）：`{ok:true, question:"…", ask_user:true}`；engine 在 `buildToolCallResults` 后检测到该结构 → 不继续迭代，把问题通过 `OnToolResult`/tool_event 发给 UI，返回 `hasMore=false` 让调用方（dino session）决定怎么接 | 中 | 需在 `agent_execution.go` 加检测点 + dino 侧处理该哨兵发 `EventTypeQuestion` |
| B. 移除/降级 | 权限默认 `ActionDeny`（改回），且从不注册（`loadBuiltinTools` 去掉 `NewQuestionTool`，`factory.go:816`） | 极小 | 能力丢失；`question.txt` 描述里的「调用即可与用户交互」变假 |

**推荐 A（保能力）**，但**不作为本次四件套之一**——它跨 engine+dino 两层，且「用户回答如何回流到对话」没有现成通道（`Session` 只有 `Input()` 注入新 turn）。与 F3 一样**单列**，建议在 F2/F4/F5/F6/F1 落地、F3 评审后再排。若产品上「question 必须能问」不是近期待办，B 是更诚实的中间态（防止「放开权限但必报硬错」这个更糟的状态）。

---

## 8. 改动文件清单（按修复汇总）

| 文件 | 修复 | 改动 |
|---|---|---|
| `agent/types/truncate.go` | F1 | 重构：新增 `OutputHeader`、`TruncationMeta`、`HeaderBudget`、`TruncateMiddle`、`BuildOutputHeader`；改 `TruncateToolResult` 签名（加 `header OutputHeader`） |
| `agent/types/toolresult.go` | F1 | `truncateText` → 内部改调 `TruncateMiddle`；`SanitizeToolResult` 签名不变 |
| `agent/engine/agent_execution.go` | F1/F4/F6 | `:270-274` 组装 header 传入；`:320-423` 加 semaphore；`:727-737` 改 `resultSender` 阻塞；`:727` 用 `getStreamBufferSize()` |
| `agent/engine/agent_cache.go` | F4 | 新增 `getToolParallelismLimit()`；`agent_execution.go` 同包可见即可（或放 `agent_tools.go`） |
| `agent/types/agent.go` | F4/F6 | `AgentConfig` 加 `ToolParallelismLimit`、`StreamBufferSize`；`NewAgentConfig` 填 0 |
| `dino/factory.go` | F2 | 抽 `wrapSessionTool` helper，两处循环 + subagent 注册处接线 `limiter → nonFatal` |
| `dino/config.go` | F2/F4 | `ToolConfig` 加 `ResultLimiterMaxBytes`/`ResultLimiterMaxStringBytes`；`Config` 加 `Tools.MaxToolParallelism`；`DefaultConfig` 填默认值 |
| `dino/defined_tool.go` | F5 | `Respond` 阻塞+超时，返回 `bool` |
| `dino/permission/permission.go` | F7（单列） | 方案 A 不动；方案 B 改默认 Deny |
| `agent/tools/builtin/runtime/question.go` | F7（单列） | 方案 A：`Execute` 返回哨兵结果 |
| `agent/types/tool.go` | F3（单列） | `FatalToolError` |
| `dino/tools/tool_wrappers.go` | F2/F3 | F2 不必要改；F3 在 `nonFatalTool.Execute` 加 fatal 透传 |

---

## 9. 迁移顺序（每步独立可验证）

**性价比排序：先接线（收益最大、改动最小），再并发/背压（稳定性），最后截断重构（正确性但涉及格式变更）。**

| 步 | 修复 | 验证方式 | 依赖 |
|---|---|---|---|
| 1 | **F2 接线**（nonFatal + limiter） | `go test ./dino/tools/`（已有 `tool_wrappers_test.go`）+ 手动跑一个 MCP 调用失败场景，观察错误被喂回模型、迭代继续 | 无 |
| 2 | **F4 并发上限** | `go test ./agent/engine/`；写一个 mock 并行工具 + `ToolParallelismLimit=1` 断言串行执行；`=4` 断言最多 4 并发 | 无 |
| 3 | **F5 Respond 阻塞** | 单测：满通道时 `Respond` 阻塞/超时、返回 false；未知 id 返回 false；`RequestApproval` 正常消费后返回 true | 无 |
| 4 | **F6 流式背压** | 单测：慢消费者下 `resultSender` 不丢 tool_event；ctx 取消后发送方退出不死锁 | 无 |
| 5 | **F1 截断重构** | 单测（§10）+ 手动：超大 bash 输出观察 header/省略标记/落盘提示；确认 history 层不再二次截断中间 | 无 |
| 6 | **F3 错误二分**（单列） | 单测：`FatalToolError` 透传、`RespondToModel` 喂回、fatal 短路并行 | 需 F2 |
| 7 | **F7 question**（单列） | 单测：哨兵结果被 engine 识别、`hasMore=false`；dino 发 `EventTypeQuestion` | 需 F4（不依赖） |

每步完成即 `git commit`（Conventional Commits），可独立回滚。**F1 是唯一改变「模型看到的工具输出格式」的改动**，落地时建议配一条 `release note` 说明 header 格式，便于对比回归。

---

## 10. 测试清单

### F1 截断重构（`agent/types/truncate_test.go` 新增/改）

1. `TestTruncateMiddle_KeepsHeadAndTail`：超长串，断言头尾均在、含省略标记、总长 ≤ 预算。
2. `TestTruncateMiddle_UTF8Boundary`：多字节字符（CJK/emoji）在边界处不切碎（用 `utf8.ValidString` 断言）。
3. `TestTruncateMiddle_ShortInputNoOp`：≤ 预算时原样返回、`omitted==0`。
4. `TestBuildOutputHeader_FieldsOmitted`：空字段的行省略；`Output:` 引导行总在。
5. `TestTruncateToolResult_ReservesHeader`：`maxLen=1000`，`BuildOutputHeader` 长 400 → 正文预算 ≤ 600；`len(display) ≤ 1000`（**核心断言**）。
6. `TestTruncateToolResult_WriteFile`：`writeDir` 设临时目录，超大输出 → 返回含绝对路径的提示、文件存在、正文不出现。
7. `TestTruncateToolResult_HeaderBudgetCapped`：header 超 `HeaderBudget` 时 PATH 行仍在、其余 header 行被裁。
8. `TestSanitizeToolResult_MiddlePreserved`：嵌套 map/数组内 string 截断后保留中间（回归 `toolresult.go` 递归行为）。

### F2 接线（`dino/factory_test.go` 或 `dino/tools/tool_wrappers_test.go`）

9. `TestWrapOrder_NonFatalOuter`：最外层是 loopDetection，其内是 nonFatal、再内 limiter、最内 approval（通过 `fmt.Sprintf("%T", …)` 断言包装类型顺序）。
10. `TestNonFatal_MCPErrorFeedsBack`：inner 返回普通 error → `Execute` 返回 `{ok:false, error}` 而非 error（**已有类似用例，补一条工具名= mcp 的**）。
11. `TestLimiter_ConfigDefaults`：`ToolConfig` 零值时 factory 用 120KB/60KB。

### F4 并发上限（`agent/engine/agent_execution_test.go`）

12. `TestParallelismLimit_One`：`ToolParallelismLimit=1`，4 个无依赖工具，用 atomic 计数断言最大并发 ==1，且都执行。
13. `TestParallelismLimit_Four`：`=4`，10 个工具，断言最大并发 ≤4、全部完成。
14. `TestParallelismLimit_Default`：0 值 → `max(4, GOMAXPROCS*2)`。
15. `TestParallelismLimit_CancelQueue`：`=1` 且一个工具挂住 + ctx 超时 → 排队工具被跳过、不泄漏 goroutine（`go vet` + `goleak` 可选）。

### F5 Respond（`dino/defined_tool_test.go`）

16. `TestRespond_UnknownID`：未知 id → 立即返回 false。
17. `TestRespond_BlocksThenSucceeds`：通道满（先灌一个值）→ 新 goroutine `Respond` 阻塞；消费旧值后 `Respond` 送达、返回 true。
18. `TestRespond_Timeout`：无消费者 → 5s 后返回 false（用 `approvalRespondTimeout` 可注入的短超时测试）。
19. `TestRequestApproval_ThenRespond`：`RequestApproval` 后 `Respond(true)` → 返回 true 且 `approved==true`。

### F6 流式背压（`agent/engine/agent_execution_test.go`）

20. `TestResultSender_NoDrop`：消费者慢（每 10ms 消费一个），生产 200 个 tool_event → 断言**全部**收到、顺序不变。
21. `TestResultSender_BlockOnCtxCancel`：消费者不读，取消 ctx → `resultSender` 返回、不 panic、`ExecuteStream` 正常 close。
22. `TestStreamBufferSize_Config`：`StreamBufferSize=200` → `resultChan` cap 生效。

### F3/F7（单列，供排期）

23. `TestNonFatal_FatalPassthrough`（F3）：`FatalToolError` 透传为 error。
24. `TestQuestion_SentinelResult`（F7）：`Execute` 返回 `{ask_user:true}` 结构而非 error；engine 检测后 `hasMore=false`。

---

## 11. 风险点

| 风险 | 概率/影响 | 缓解 |
|---|---|---|
| **F1 改变模型可见输出格式**：header 行可能被模型误读为「输出内容」 | 中/中 | header 用 `Output:` 引导行分隔（Codex 同款）；release note 说明；`SanitizeToolResult` 内部字段仍净化，header 只加在外层观测串 |
| **F1 预留字节算不准**：`maxLen` 极小（如 64）时 header 吃掉全部预算 | 低/低 | `HeaderBudget=512` 封顶 + `maxLen/4` 兜底下限：正文预算 = `max(0, maxLen - min(headerLen, HeaderBudget, maxLen/4))` |
| **F2 语义变化**：原本「MCP 硬错误终止」变成「喂回模型」，模型可能反复失败（loop 放大） | 中/中 | `LoopDetectedError` 已透传（`tool_wrappers.go:42-46`）；若循环严重，`DoomLoopThreshold`/budget 兜底（`agent_execution.go:1173-1178`）；F3 的 fatal 分类是长期解 |
| **F2 nonFatal 吞掉审批拒绝**：approval 被拒是「可恢复」错误，会喂回模型导致重试审批 | 中/中 | 包装顺序 approval 最内：被拒时 `ApprovalTool` 返回的应是 fatal 类错误（F3 落地后透传）；F2 阶段先接受「喂回模型让模型换工具」，行为优于「硬终止」 |
| **F4 排队导致 wall-time 变长**：并行 bash 从全并发变 N 并发 | 低/低 | 默认 `GOMAXPROCS*2`（足够宽松）；可配置 |
| **F4 排队 + errgroup 语义**：Acquire 失败时 `results[idx]` 零值被当成功 | 中/高 | §4.4：Acquire 失败写 `stepResult{err: gctx.Err()}` |
| **F5 阻塞 5s**：HTTP/MCP 网关响应变慢 | 低/低 | 超时兜底；`RequestApproval` 消费方在工具 goroutine，5s 内必然消费（除非工具被 cancel） |
| **F6 背压传导**：慢消费者卡住工具执行（errgroup goroutine 阻塞在 `sendEvent`） | 中/中 | ctx 取消兜底 + dino 层 `emit` 5s 超时已是第二道闸；文档注明「慢下游 = 慢工具」是有意设计 |
| **F6 死锁**：消费者停止 `range stream` | 低/高 | `resultSender` 必须 select `ae.ctx.Done()`（§6.1）；`ExecuteStream` 结束即 close + ctx 取消，发送端必然退出 |
| **F3 分类误伤**：把「可重试」误判为 fatal 或反之 | 中/中 | 初稿清单保守（仅 schema/权限/loop 三类）；评审时逐条过 |
| **F7 无回流通道**：用户回答无法注入当前 turn | 中/中 | 方案 A 只做「挂起 + 通知 UI」，「回答回流」需新通道（超出本任务）；产品若近期要用，需另立设计 |

---

## 12. 留给用户的待定点

1. **F1 header 字段**：`ExitCode` 探测只覆盖 bash/command（`command.go:110-124` 的 `exit_code` 键）。MCP 结果无 exit code，是否要为 MCP 统一补一个「provider error code」字段进 header？（需要 MCP client 改造，建议单列。）
2. **F1 落盘阈值**：建议 `OriginalBytes > maxLen*8`，是否可接受？`ToolResultWriteDir` 默认空（`config.go:193-203`）——**当前默认不落盘**，超大输出会走 `TruncateMiddle`（保留中间），不会写文件。需确认产品是否希望默认落盘。
3. **F2 limiter 阈值**：120KB/60KB（`tool_wrappers.go:81-85`）与 `MaxTruncationLength=2048`（`agent.go:18`）差 30 倍——limiter 的 120KB 是「JSON 原样」上限，截断的 2048 是「喂模型观测串」上限。是否要把 limiter 默认调低（如 32KB）以少跑一次截断？
4. **F4 默认并发**：`max(4, GOMAXPROCS*2)` 封顶 32。对 MCP 工具多的环境是否偏大？默认值可通过 yaml `max_tool_parallelism` 覆盖。
5. **F6 合并阶段**：第一版只做「阻塞」，chunk 合并（§6.1）作为第二阶段。是否现在就要？
6. **F5 返回值**：`Respond` 返回 `bool` 但 `DinoFactory.RespondToolApproval`（`factory.go:705`）签名不动——网关是否需要拿到「送达/超时」信号？需要的话改接口签名（破坏性）。
7. **F3 / F7 排期**：单列的两项是否进入本季度，还是先落四件套（F2/F4/F5/F6）+ F1？

---

## 13. 实现备注（2026-08-28，落地时相对设计的偏离记录）

按评审 BLOCKER/B-RECOMMENDED 修正后的实际落地决策，全部实现于分支 `impl-tool-pipeline`：

- **F4**：改用 `errgroup.SetLimit`（vendor 内 `x/sync` 有 `errgroup` 无 `semaphore`）实现全局并发上限，效果等同 semaphore 排队。`AgentConfig.ToolParallelismLimit`（0=默认 `max(4, GOMAXPROCS*2)` 封顶 32）+ dino `Tools.MaxToolParallelism`（yaml `max_tool_parallelism`）。**注意**：引擎依赖排序以工具名为 key，同名工具的并行调用会折叠——测试因此用不同名工具。
- **F5**：`ApprovalStore.Respond` 阻塞 + `approvalRespondTimeout=5s` 超时，返回 `bool`（未知 id / 超时 → false）。`DinoFactory.RespondToolApproval` 签名保持（返回值被忽略）。
- **F6**：`resultSender` 闭包捕获**调用方 turn ctx**（`ExecuteStream` 入参），满时阻塞发送；`ae.ctx` 仍作为第二道保险监听。**所有**直接 `resultChan <-` 路径改走 `sendStreamResult(ctx, ...)`（nil ctx 安全），chunk/reasoning 发送失败即中止迭代。`resultChan` 用 `getStreamBufferSize()`（`StreamBufferSize` 可配）。
- **F1**：`TruncateToolResult` 签名改为 `(content, maxLen, writeDir, header OutputHeader) (display, TruncationMeta)`——原第三个返回值 `filePath` 从未被消费，折叠进 `TruncationMeta`。`TruncateMiddle` 50/50 + marker，UTF-8 安全；`truncateText`（toolresult.go）与 `TruncateString`（agent.go）都改为 UTF-8 安全。落盘阈值 `len(content) > maxLen*8`，落盘时正文不进上下文，仅 header + 保存提示（PATH 优先级最高）。采纳 R-1（正文预算下限 maxLen/4，header 让位）+ R-2（真凶是 `s[:maxLen]` UTF-8 不安全）。`output_chars` 字段在最终 header 中渲染，且 guide 行「Output:」永不丢失（`trimHeaderPreserveGuide`）。
- **F2**：抽 `wrapSessionTool` helper，三处接线 `approval → limiter → nonFatal → loopDetection`。**B1**：收益定位改写为「limiter 结果上限生效 + 错误结构化 `{ok:false}` 呈现」（不再声称修复迭代终止）。**B2**：`ApprovalRejectedError` 定义在 **`dino/tools`**（避免 `dino/tools`↔`dino` 循环依赖，`dino` 侧用类型别名），`nonFatalTool` 对其 `errors.As` 透传；`ApprovalTool`/`ExternalPathApprovalTool` 拒绝时返回该类型。
- **F3**（落地）：`types.FatalToolError`（带 `Unwrap`）；`nonFatalTool` 透传；引擎 schema 校验失败 wrap 为 fatal 并从 errgroup 返回，取消同层并行调用。审批拒绝为最高优先 fatal 类（来自 B2）。
- **F7**（部分落地）：`QuestionTool.Execute` 返回 `SentinelQuestionResult{ok:true, question, ask_user:true}` 而非硬错误，避免 build/plan 模式下「放开权限但必报硬错」。**未做**：runner 侧检测该 sentinel → 发 `EventTypeQuestion` + 用户回答回流通道（产品决策点，见 §12.7）。
- **R-7**：`ApprovalStore.Respond` 改造对当前仓库无调用方（`RespondToolApproval` 无外部调用），为健壮性改进，无害。
- **R-8**：`session.emit` 5s 超时丢弃未改（保留为 session 层兜底），engine 层不再丢事件。

### 遗留待定点
1. `FatalToolError` 目前只用于 schema 校验；`EC_TOOL_AUTH_ERROR` / `EC_TOOL_INPUT_ERROR` / MCP 错误分类是否也该进 fatal 清单需单独评审。
2. F7 的「用户回答回流」无通道，产品近期若要用 question 需另立设计。
3. F6 的 chunk 合并（§6.1 第二阶段）未做，第一版仅阻塞保真。
4. limiter 默认 120KB/60KB 未下调（§12.3）；`dino/session` 层 `emit` 超时丢弃仍未带可观测计数。
5. `go test ./...` 中 `scheduler` 包因 vendor 缺 `github.com/stretchr/objx`（网络拉取失败）无法编译，与本次改动无关（base commit 同样失败）。
