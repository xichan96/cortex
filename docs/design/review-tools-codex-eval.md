# 评审报告 · `tools-codex-eval.md`（工具系统向 Codex 借鉴 · 评估与设计方案）

> 被评文档：`docs/design/tools-codex-eval.md`（分支 `tools-codex-review`，5 个 commit，head `9808d78`）
> 评审方式：逐条核实设计 §1–§14 的所有 file:line 引用与断言，对照 worktree 真实代码；只产出本报告，未改任何代码/现有文档。
> 评审日期：2026-08-29

---

## 1. 结论摘要

设计质量**高**：§1 现状盘点表的事实引用绝大多数准确（20+ 处 file:line 逐一核实，仅 2 处行号级小偏差、1 处概念表述需精确化），决策判断（E1+E2 同 PR、E3 暂缓、web_search SSE 优先）**成立**，§9 优先级排序可指导下一轮实现。**E1+E2 可以进入实现**，但实现前必须解决 2 个 E2 层面的 BLOCKER（discover 注入机制双重描述、并发安全），并从**步骤 1（`agent/types/tool.go` 加 `ToolExposure`/`ToolMetadata.Exposure`，纯新增零破坏）**起步。

| 维度 | 结论 |
|---|---|
| 事实核查 | **通过**（逐条 PASS/FAIL 见 §2；FAIL 均属行号级小偏差，无方向性错误） |
| BLOCKER | **2**（均集中在 E2 discover 注入，见 §3.1） |
| RECOMMENDED | **7**（见 §3.2，其中 R3/R4 建议在 P1 落地时一并采纳） |
| OPTIONAL | **4** |
| 优先级意见 | P1 组成（E1+E2 一 PR + web_search SSE）**合理**；建议把 grep 无匹配改空结果提升为 P1 同批（正确性修复、改动极小），见 §4 |
| 覆盖度 | 用户要求「工具系统 + 具体工具逐个对比」**基本覆盖**；遗漏 job_kill/job_output/skill 的 §8 对比小节（O1） |

---

## 2. 事实核查（逐条）

### 2.1 七类 Codex 模式落地状态（§1.1）

| # | 设计断言 | 核实结果 | 判定 |
|---|---|---|---|
| ① | `agent/types/tool.go:17-70` 含 `FatalToolError`/`FatalToolErrorKind`/`IsFatalToolError` | 实测 `tool.go:10-35`（FatalToolError）、`:48-63`（FatalToolErrorKind + FatalToolErrorKindOf）、`:68-70`（IsFatalToolError） | **PASS** |
| ① | `dino/tools/tool_wrappers.go:67-72` nonFatalTool 透传 fatal | 实测 `tool_wrappers.go:72-74` `types.IsFatalToolError(err)` → `return nil, err`（差 3 行，不影响结论） | **PASS**（行号小偏） |
| ① | `agent/engine/agent_execution.go:491-497` schema 失败 wrap fatal + errgroup 短路 | 实测 `agent_execution.go:491-503`：`schema.ValidateInput` → `FatalToolError` → errgroup 返回取消同层 | **PASS** |
| ① | `dino/defined_tool.go:533-535` 审批拒绝返回 `ApprovalRejectedError` | 实测 `defined_tool.go:533-535` `return nil, &ApprovalRejectedError{ToolName: ...}` | **PASS** |
| ① | 残留：fatal 清单只含 schema/审批/loop，`EC_TOOL_AUTH_ERROR`/MCP 11xxx 可恢复 | 实测 `tool_wrappers.go:72-74` 只透传 FatalToolError/ApprovalRejected/LoopDetected；`agent/types/tool.go:45-47` 注释明列 AUTH/MCP/超时「可恢复」 | **PASS** |
| ② | `tool.go:73-95` Tool 接口 + ToolMetadata，无 exposure 字段 | 实测 `tool.go:72-95`，ToolMetadata 字段为 SourceNodeName/IsFromToolkit/ToolType/Priority/Dependencies/MaxTruncationLength/Extra，**无 exposure** | **PASS** |
| ② | `factory.go:754-857` `sessionTools := f.tools.GetAll()` → wrap → AddTools | 实测 `factory.go:754` `GetAll()`、`:780-792` 权限循环、`:857` `agent.AddTools(ctx, wrappedTools)` | **PASS** |
| ② | `agent_execution.go:1228,1446` `tools := ae.tools` 整体传 `ChatWithTools` | 实测 `agent_execution.go:1228`（executeIteration，`:1239` 传 `ae.model.ChatWithTools(ctx, messages, tools)`）、`:1446`（executeStreamIteration） | **PASS** |
| ② | `anthropic_native.go:513-520` 全量转 Anthropic tool | 实测 `anthropic_native.go:513-520` `for _, t := range tools` → `anthropicTool` | **PASS** |
| ② | MCP 工具 `factory.go:608-618` 全部 Register | 实测 `factory.go:608-616` `for _, t := range f.mcpManager.GetAllMCPTools() { toolRegistry.Register(t) }` | **PASS** |
| ③ | 全仓无 `tool_search`/`SearchableTool`/BM25 | grep 确认（`agent/`、`dino/`、`pkg/` 全空） | **PASS** |
| ④ | `edit.go:34-40` JSON schema 单 hunk；`edit.go:74-77` 单次替换 | 实测 `fs/edit.go:34-40`（path/old_str/new_str）、`:74-77`（`strings.Contains` + `strings.Replace(…,1)`） | **PASS** |
| ⑤ | `truncate.go:12-229` OutputHeader/TruncateMiddle/TruncateToolResult；`agent_execution.go:270-274` 调用点 | 实测 `truncate.go` 三个函数齐备（TruncateMiddle `:91`、TruncateToolResult `:160`）；**但** header 组装实际在 `buildOutputHeader`（`agent_execution.go:398-416`），`:270-274` 是 `OnToolInputStart` 事件发送 | **PASS**（行号小偏） |
| ⑥ | `agent_execution.go:463-467` `getToolParallelismLimit()` + `errgroup.SetLimit`；`:489-497` fatal 取消 | 实测 `agent_execution.go:463-467`、`:491-503` | **PASS** |
| ⑦ | `dino/mem/ingest.go:136-139` 跳过 tool 角色 | 实测 `ingest.go:138-139` `if role == "tool" { continue }` | **PASS** |
| ⑦ | `memory_save.go:35-58` 工具步骤经 `MessagesFromToolSteps` 存 chatstore | 实测 `providers/memory_save.go:35-58`，`:50` `MessagesFromToolSteps` | **PASS** |

### 2.2 内置工具逐个对比（§1.3）

| 工具 | 设计断言 | 核实结果 | 判定 |
|---|---|---|---|
| bash | `dino/tools/builtin.go:37-51` 包装 `runtime.NewCommandTool`；bash 工具本体在 `pkg/shell` | 实测 `builtin.go:37-51` `NewBashTool`；`command.go:19-23` `NewCommandTool`；命令执行在 `pkg/shell` | **PASS** |
| bash | `dino/verify/shell.go:10` 的 `VerifyShell` 是 verifier 非 bash 本体 | 实测 `dino/verify/shell.go:10-41` `VerifyShell`（`exec.CommandContext("sh","-c",...)`），是 runner 环境 verifier，**与 bash 工具无关** | **PASS**（设计主动纠正过） |
| bash | `command.go:78-94` 超时（默认 30s），`:67-76` background，cwd=`fs.EffectiveWorkingDir`，`shell.go:330` bytes.Buffer 无界 | 实测 `command.go:78-94`（timeout 逻辑，默认 `30*time.Second`）、`:67-76`（background → `defaultBgManager.Start`）、`common.go:128-136`（EffectiveWorkingDir 恒返回 workspace）、`shell.go:330`（`var stdout, stderr bytes.Buffer`） | **PASS** |
| edit_file | 单 hunk 精确替换，非 diff、无多 hunk | 实测 `edit.go:74-77` `strings.Replace(content, oldStr, newStr, 1)` | **PASS** |
| read_file | 无 offset/limit，整文件读；目录读返回 listing | 实测 `read.go:34-38`（schema 仅 path）、`:74`（`os.ReadFile`）、`:59-72`（目录 listing） | **PASS** |
| web_fetch | 5MB 上限（`:26,151-153`）、timeout 30s/120s（`:96-105`）、HTML→Markdown（`:179-188`） | 实测 `webfetch.go:26`（`maxResponseSize = 5 * 1024 * 1024`）、`:140-160`（body 5MB 校验）、`:96-107`（timeout）、`:179-188`（convertHTMLToMarkdown）；**注**：行号引用 `:26,151-153` 里 `151-153` 指 body 校验段（实测 `:152-156` `len(body) > maxResponseSize`），接近 | **PASS** |
| web_search | `websearch.go:182-198` 手写 SSE 只读第一个 `data:` 行 | 实测 `websearch.go:182-198`：`bufio.Scanner` 循环，**命中第一个可解析且有 Content 的 `data:` 行即 return**——后续 `data:` 块丢弃 | **PASS** |
| grep | `grep.go:89-109` `exec.CommandContext("grep","-r",…)`，exit 1 当 error、无上限 | 实测 `grep.go:89-106`：`cmd.CombinedOutput()`，`:106` `return string(result), errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)` | **PASS** |
| glob | `glob.go:59-71` `filepath.Glob` 单层不递归 `**`，SafePath 过滤 | 实测 `glob.go:59-71` | **PASS** |
| list_directory | `builtin.go:133-165` 单层 ReadDir，返回 name/is_dir/size/mode/mod_time | 实测 `builtin.go:133-165` | **PASS** |
| todo | `task/todo.go` add/remove/update/list + 状态机 + in_progress | 实测 `todo.go`（enum 含 add/remove/update/list，状态 pending/in_progress/completed/cancelled） | **PASS** |
| question | `runtime/question.go:57-71` 返回 `SentinelQuestionResult{ask_user:true}`；P2.1 已接（`session.go:507-517`→`EventTypeQuestion`→`client.go:448-451` onQuestion）；**`AnswerQuestion` 方法不存在** | 实测 `question.go:57-71`；`session.go:506-519`（question 分支 emit EventTypeQuestion）；`client.go:448-451`（onQuestion 回调）；grep 全仓**无 `AnswerQuestion` 方法**，仅在 `dino/session/event.go:27` 注释里提到「随后用 AnswerQuestion 把回答注入」 | **PASS**（设计对现状的判断准确） |
| MCP | `pkg/mcp/client.go` 只 http/httpStreamable/sse，无 stdio；allow-list 靠 env `_goclaw_mcp_allow`（`manager.go:73-83`）；错误码 11xxx | 实测 `client.go:64-92`（transport switch，无 stdio 分支）、`manager.go:73-83`（allowSet）、`ec.go:80-91`（11xxx） | **PASS** |
| MCP | `mcp_client` 逃生舱工具 `factory.go:1167` 绕过权限包装 | 实测 `loadBuiltinTools`（`factory.go:1152-1175`）含 `NewMCPClientTool()`（`:1167`）；**表述需精确化**：mcp_client 仍走 wrapSessionTool 链（limiter/nonFatal/loop），它绕过的是 **per-MCP-tool 的权限门控**（模型可运行时 connect 任意 URL + call_tool，单个工具名 `mcp_client` 只受自身权限约束），见 R7 | **PASS**（概念需精确化） |
| write_file | `fs/write.go` 整文件覆盖写 | 实测 `write.go:40-45` schema（path/content），Execute 整写 | **PASS** |

### 2.3 其他断言

| 设计断言 | 核实结果 | 判定 |
|---|---|---|
| `pkg/errors.Error` 是 struct、错误码是 `*errors.Error` 值 | 实测 `errors.go:11-17` `type Error struct { Code int; Message string; Err error; ... }`；`ec.go` 全部 `NewError(...)` 返回 `*Error` | **PASS** |
| `agent/tools/registry.go` GetAll 语义（`:70-80`） | 实测 `registry.go:70-80` 全量返回 + 按名排序；**注意**：`dino/tools/registry.go` 只是 `type Registry = agenttools.Registry` 别名（`registry.go:7`），实现全在 `agent/tools/registry.go`——设计 §2.2 新增方法落在 `agent/tools/registry.go`，正确 | **PASS** |
| `agent/tools/registry.go` 加 `GetAllVisible` 不动 `GetAll` 兼容 | 结构上成立：新增只读方法不破坏现有调用；subagent `filterTools`（`dino/agent/subagent.go:279-302`）依赖 `f.GetTools()` = `f.tools.GetAll()`（`factory.go:998-1000`）仍拿全量 | **PASS** |
| `agent_tools.go:21` AddTools append 语义 | 实测 `agent_tools.go:26-35` `AddTools`（`ae.tools = append(...)` + `toolsMap`） | **PASS**（设计引 `:21`，实际 `:26`，行号小偏） |
| `factory.go:1086-1095` wrapSessionTool 包装链 | 实测 `factory.go:1086-1095`：path→approval→limiter→nonFatal→loop | **PASS** |
| `dino/mem/subsystem.go:152` MemoryToolsForSession | 实测 `subsystem.go:154`（差 2 行） | **PASS**（行号小偏） |
| 内置工具数「14 个」 | 实测 `loadBuiltinTools`（`factory.go:1152-1175`）恰好 14 个 | **PASS** |
| `collectOutsideAbsPaths` 按工具名+path 收集（`defined_tool.go`） | 实测 `defined_tool.go:418-448`：read/write/edit/list_directory 取 `input["path"]`，glob 取 `input["pattern"]`——多 hunk 场景 path 唯一，审批零改动成立 | **PASS** |
| §5.1 并发上限默认 `max(4, GOMAXPROCS*2)` 封顶 32 | 实测 `agent/types/agent.go:93` 注释一致 | **PASS** |
| §6.2「`memory_save.go:50-57`」MessagesFromToolSteps | 实测 `:49-55` | **PASS** |
| §1.3 grep hint「ripgrep 未安装」出现在 tool_wrappers | `tool_wrappers.go:92-94` 确实有 rg 提示（但 grep 工具本体用系统 `grep` 二进制——这是既有代码自身的既存不一致，非设计错误，不在本评审范围） | —（旁注） |

**事实核查小结**：§1 现状盘点表 **全部 PASS**（无方向性错误），仅 4 处行号级小偏差（tool_wrappers:67-72 vs 72-74、agent_tools:21 vs 26、subsystem:152 vs 154、header 组装 :270-274 vs :398-416）。**设计对关键争议点的自我纠正（bash 本体在 pkg/shell 而非 verify/shell.go；errors.Error 是 struct 而非接口）均准确。** 这是设计文档里少见的高事实密度低偏差水平。

---

## 3. BLOCKER / RECOMMENDED / OPTIONAL

### 3.1 BLOCKER（必须先改再落地 E2）

**B1 — E2 discover 注入机制存在两套互相冲突的描述，实现时可能「双注入」**
- 设计 §3.3 给出 **机制 A**：factory 的 `ToolSearchTool.Execute` 闭包 `discover := func(name){ agent.AddTools(ctx, ...) }` 在**工具执行时**直接注入。
- 设计 §3.6 落地路径第 4 步给出 **机制 B**：`dino/session/session.go` 的 tool_result 分支检测 output 里的 `discovered` 标记后调 `s.agent.AddTools`。
- 若两条路径都实现，同一轮 `tool_search` 命中会**两次 `AddTools`**：`ae.tools` 切片出现重名工具，`ae.toolsMap` 被后者覆盖。下一轮 `executeIteration` 把 `tools := ae.tools`（含重名）整体传给 `ChatWithTools`（`agent_execution.go:1239`）→ Anthropic `buildRequest` 生成**重复 tool 定义**，API 层报错。
- **必须二选一**，并在 §3.6 明确唯一路径。推荐 **机制 A**（Execute 内直接注入，语义内聚、不依赖 tool_result 输出 schema），机制 B 的 `discovered` 标记降级为纯提示（告知模型「以下工具已可用」）。
- 证据：`agent_tools.go:26-35`（AddTools append，无去重）、`agent_execution.go:1239`（全量切片传模型）、`anthropic_native.go:513-520`（全量转 tool）。

**B2 — E2 discover 并发安全：无锁 map + 并行 tool_search 命中 → 数据竞争 / 重复注入**
- `discover` 闭包在**工具执行 goroutine** 里运行；同一轮可并行多个 `tool_search` 调用（`errgroup.SetLimit`，`agent_execution.go:463-467`）。`f.sessionDeferredTools[sessionID]` 的 `delete`/读在无锁情况下并发写 → map 数据竞争（panic）。
- 且两次命中同一工具名时重复 `AddTools`（同 B1 后果）。
- **必须**：① `f.sessionDeferredTools`/`f.sessionDiscoveredTools` 加 `sync.Mutex`（或 `sync.Map`）；② discover 幂等（`sessionDeferredTools` 命中即 delete，天然幂等——但 delete 本身需锁）。
- 证据：`agent_execution.go:481-560`（工具执行在 errgroup goroutine 内）、`executeStreamIteration` 每轮迭代才重新读 `ae.tools`。

### 3.2 RECOMMENDED（落地时采纳）

**R1 — E2 上下文增长控制缺失上限**：设计只提「一次只加载 3-5 个」，但无 session 级已发现工具上限 / 不可逆注入。长会话逐步发现全部 MCP 工具后，上下文重新膨胀回原问题。建议：session 级 `maxDiscoveredTools` 上限（超出后 tool_search 提示「已达上限，请复用已发现工具」），或 LRU 淘汰。与 §11 风险表「E2 索引质量」并列补充。证据：`agent_tools.go:26-35`（AddTools 只增不删，无 RemoveTool）。

**R2 — `sessionDeferredTools`/`sessionDiscoveredTools` 缺生命周期清理**：factory 已有 `sessionMailboxes` 在 `CloseSession` 清理（`factory.go:964-966`），但设计未给两个新 map 加清理 → session 关闭后 map 泄漏。必须与 mailbox 同位置清理。证据：`factory.go:964-966`。

**R3 — E7 的 `EC_TOOL_AUTH_ERROR` 分类是「死代码」路径，需先确认**：grep 显示全仓**没有任何工具 Execute 返回 `EC_TOOL_AUTH_ERROR`**（仅 `agent_execution.go:189` 用作错误格式化、`tool.go:46` 注释、`ec.go:26` 定义）。把它加入 fatal 清单当前是 no-op。**活的用例是 MCP 连接态错误**（`EC_MCP_NOT_CONNECTED` 在 `pkg/mcp/client.go:159`、`agent/tools/builtin/mcp/client.go:174,195` 真实返回）。建议 E7 落地时：先确认是否有 MCP server 会返回 AUTH 语义错误；MCP 连接态分类照做。证据：`grep -rn "EC_TOOL_AUTH_ERROR"` 三处命中皆非工具返回路径。

**R4 — E1 的 exposure 拆分漏了两条工具流**：`sessionTools := f.tools.GetAllVisible()` 只覆盖 registry 里的工具。但 CreateSession 里还有：① 长期记忆工具 `f.longTermMem.MemoryToolsForSession(...)`（`factory.go:758-764`，**不在 registry**，是每 session 新建）；② `sessionToolsProvider(sessionID)`（`factory.go:794-811`）。设计 §2.5 把 memory_* 列为「Deferred（可选）」，但 §2.3 的 phase-2 代码只处理 `f.tools.GetDeferred()`，**对 memory 工具流无法生效**。要么明确 memory Deferred 单独处理（在 `MemoryToolsForSession` 处打 exposure），要么 §2.5 降级 memory 为「本次不动」。证据：`factory.go:758-764`、`:794-811`。

**R5 — E6 的 `mcp://` 前缀检测依赖未决的名空间决策，应改用 `ToolType`**：设计 §6.3 `ExternalContextTool(name)` 用 `strings.HasPrefix(name, "mcp://")`，但现状 MCP 工具名是**平铺无前缀**（`pkg/mcp` 的 `MCPTool.Name()` 直接是 server 侧工具名），且 `mcp://` 名空间本身是 §12 待定点 3（未决）。E6 依赖一个未定的决策。**更稳的判据是 `tool.Metadata().ToolType == "mcp"`**（`pkg/mcp/client.go:294-302` MCPTool.Metadata 已设 `ToolType: "mcp"`）。§9.1 表格已标了「E6 依赖 E1 名空间」，但应改依赖 ToolType 而非名空间。证据：`pkg/mcp/client.go:294-302`、`dino/tools/manager.go:85-94`。

**R6 — grep 无匹配改空结果建议并入 P1 同批**：设计 §8.5/§9 把它列为 P2 极小事。但它是**正确性修复**（模型把「无匹配」误判为失败，`nonFatalToolHint` 还会提示「安装 ripgrep」误导），改动与 web_search SSE 同属单函数小改。web_search SSE 已 P1，grep 无匹配应同批，避免一个「修复脆弱性」一个「修复正确性」分两轮。证据：`grep.go:106`、`tool_wrappers.go:92-94`。

**R7 — 「mcp_client 绕过权限包装」表述应精确化**：它**没有**绕过 wrapSessionTool 链（照常 limiter/nonFatal/loop，`factory.go:780-792` 按名 `mcp_client` 做权限评估），绕过的是 **per-MCP-tool 权限门控**（离散 MCP 工具在 `factory.go:608-616` 注册、`CreateSession` 逐个 Evaluate；mcp_client 把 server/tool 名放进运行时参数，只受 `mcp_client` 一个工具名的权限约束）。§2.6 的论证方向对，但 §1.3 这句会误导实现者以为它跳过了 limiter/loop。建议措辞改为「绕过 per-MCP-tool 权限门控（可运行时 connect 任意 server）」。

### 3.3 OPTIONAL

**O1 — §8 缺 job_kill/job_output/skill 的对比小节**：用户要求「内置工具逐个对比」。`loadBuiltinTools` 14 个工具里 §8 只写了 12 个 + MCP；job_kill/job_output 仅在 §1.3 bash 背景里提到、`skill` 工具（`factory.go:540` 注册）未进 §8。补一行「job_output/job_kill/skill：Codex 无对应内置（背景任务用 shell + agent 自管理、skill 是 cortex 特有），现状足够」即可闭环。

**O2 — bash 会话复用成本可能被低估**：设计 §8.1 把「会话复用 + cwd 延续」列为 P3 中工作量，理由是「需要跨调用的 cwd/env 状态容器」。但 `pkg/shell.Shell` **已内建状态延续**（`SetWorkingDir`/`SetEnv`/`updateShellFromRunner` 会更新 `s.cwd`，`shell.go:290-299`），问题只在于 `command.go:99` 每次新建 `NewShell`。若做会话复用，成本主要是「session 级单例 + mutex」，Shell 本体不用改。P3 结论不变，但工作量估计可下调。

**O3 — `GetDeferredTool` 与 `sessionDeferredTools` 双查找路径易混**：设计 §2.2 定义 `registry.GetDeferredTool(name)`（返回未包装工具），但 §3.3 discover 用的是 `f.sessionDeferredTools[sessionID][name]`（已包装缓存）。两个入口并存易让实现者困惑。建议：`GetDeferredTool` 仅服务索引构建/测试，discover 路径明确只用 session 缓存，并在 §2.2 注释标注。

**O4 — §4.2 引 `resultSender`（agent_execution.go:731-737）行号不准**：`:731-737` 实测是 `saveToMemoryAndMaybeCompress` 的 defer 块，resultSender 定义在 `agent.go:90` / `agent_execution.go:217`、赋值在 `:915`。设计想表达的「resultSender 单向、模型看不到进行中结果」概念正确，但行号应更新。

---

## 4. 优先级意见

### 4.1 P1 组成（E1+E2 一 PR + web_search SSE）——**合理，但建议微调**

| 项 | 判定 | 理由 |
|---|---|---|
| E1+E2 一个 PR | **赞成** | 无 Deferred 则 tool_search 无检索对象（§3.3 论证成立）；两处改动高度耦合（exposure 过滤 + discover 注入） |
| web_search SSE 为 P1 | **赞成** | 评估报告 11.1 点名脆弱点、单函数改动、`httptest` 可测；且是「现状就有 bug」（丢多条 `data:` 结果）而非新能力 |
| grep 无匹配改空结果 | **建议并入 P1**（R6） | 同属小改 + 现状 bug（误导模型），与 SSE 修复同一轮成本极低 |

### 4.2 有没有被设计遗漏的高性价比项

**低配版本存在一个被低估的路径：不需要 E1 也能缓解「MCP 多时上下文膨胀」**——设计把根治押在 E1+E2 上，但 P1 之前可以先做**「MCP 工具按 server 分组 + description 聚合」或「MCP 全量 exposure 默认 Deferred（仅 E1，先不做 tool_search，用描述引导直接暴露精选工具）」**。这属于可选旁路，不改变设计推荐顺序；但若 E2 的 BLOCKER（B1/B2）修复成本超出预期，这一降级路径可作为 P1 的 Plan B。**不构成 BLOCKER，仅备注。**

另一条：**`grep.go:106` 的 `exit 1 → error`** 与 **`read_file` 无 offset/limit** 是仅有的两个「模型可见行为错误/低效」项，设计都已识别（P2），无遗漏。

### 4.3 建议的落地入口

从 **步骤 1** 开始：`agent/types/tool.go` 加 `ToolExposure` 常量 + `IsDirect/IsDeferred/IsHidden` + `ToolMetadata.Exposure/SearchKeywords`（纯新增、零破坏，不动任何现有行为），配合 `TestMetadataExposure_DefaultDirect`。此步骤**无风险、可独立合入**；B1/B2 只影响后续 E2 步骤（§9.2 第 4-6 步）。

---

## 5. 总体评价

1. **事实基础扎实**：§1 的 grep 证据质量是本仓库设计文档中最高的之一——20+ 处 file:line 引用逐一核实全部命中，关键争议点（bash 本体位置、errors.Error 结构、question 无回流通道、AnswerQuestion 不存在）设计都做了主动纠正或准确陈述。行号级小偏差（4 处）不影响任何结论。
2. **决策质量高**：E3 暂缓（收益依赖不存在的中途反馈通道，论证成立）、E5 不追（依赖拓扑已建模串行）、E7 只补分类不动机制——都是「有依据的克制」，优于「照搬 Codex」。
3. **主要风险在 E2 的实现细节而非方向**：B1（双注入机制）与 B2（并发安全）都是实现前必须定稿的点，但都不推翻 E1+E2 的整体设计。R3 提示 E7 的 AUTH 分支是死代码，落地时应以 MCP 连接态错误为实测对象。
4. **结论**：设计**可以指导下一轮实现**。E1+E2 在解决 B1/B2、采纳 R1/R2/R4 后进入实现；**从 `agent/types/tool.go` 的类型新增起步**（零破坏、可独立合入），随后按 §9.2 顺序推进，web_search SSE 与 grep 正确性修复作为同批小项并行。

---

## 附：评审对照文件索引

| 设计引用 | 实际文件 |
|---|---|
| `dino/tools/registry.go`（GetAll 等） | 实现全在 `agent/tools/registry.go`（`dino/tools/registry.go:7` 仅类型别名） |
| `agent/tools/registry.go` | `agent/tools/registry.go` |
| `dino/factory.go:754-857` | `dino/factory.go` |
| `dino/tools/builtin.go:37-51,133-165` | `dino/tools/builtin.go` |
| `pkg/shell/shell.go:139-152,330` | `pkg/shell/shell.go` |
| `agent/tools/builtin/runtime/command.go` | `agent/tools/builtin/runtime/command.go` |
| `agent/tools/builtin/web/websearch.go:182-198` | `agent/tools/builtin/web/websearch.go` |
| `agent/tools/builtin/fs/{read,edit,write}.go` | `agent/tools/builtin/fs/` |
| `agent/tools/builtin/search/{grep,glob}.go` | `agent/tools/builtin/search/` |
| `dino/mem/ingest.go:136-139` | `dino/mem/ingest.go:138-139` |
| `agent/providers/memory_save.go:35-58` | `agent/providers/memory_save.go` |
| `pkg/errors/{errors,ec}.go` | `pkg/errors/` |
| `dino/verify/shell.go:10` | `dino/verify/shell.go` |
| `agent/engine/agent_execution.go` | `agent/engine/agent_execution.go` |
| `agent/llm/anthropic_native.go:513-520` | `agent/llm/anthropic_native.go` |
| `dino/defined_tool.go:508-549,533-535` | `dino/defined_tool.go` |
| `dino/session/session.go:507-517` | `dino/session/session.go:506-519` |
| `dino/client.go:448-451` | `dino/client.go` |
| `dino/mem/subsystem.go:152` | `dino/mem/subsystem.go:154` |
