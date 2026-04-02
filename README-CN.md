# CORTEX
![cortex-desc.png](docs/images/desc.png)
<p align="center">CORTEX 是一个基于 Go 语言构建的高性能 AI Agent 框架，专为高效集成大型语言模型 (LLM) 而设计。</p>

<p align="center">
  <img alt="GitHub commit activity" src="https://img.shields.io/github/commit-activity/m/xichan96/cortex"/>
  <img alt="Github Last Commit" src="https://img.shields.io/github/last-commit/xichan96/cortex"/>
</p>

<p align="center">
  <a href="#概述">概述</a>
  · <a href="#特性">特性</a>
  · <a href="#快速开始">快速开始</a>
  · <a href="#核心组件">核心组件</a>
  · <a href="#dino-高级编排">Dino 高级编排</a>
  · <a href="#工具生态">工具生态</a>
  · <a href="#技能系统-agent-skills">技能系统</a>
  · <a href="#记忆系统">记忆系统</a>
  · <a href="#示例">示例</a>
  · <a href="#触发器-triggers">触发器</a>
  · <a href="#许可证">许可证</a>
</p>

<p align="center">
  <a href="README.md">English</a> | 简体中文
</p>

## 概述

CORTEX 旨在融合轻量级框架的易用性与 Go 语言的稳健性能。它不仅提供了与各类主流 LLM 的深度集成，还内置了一套完整的工具链，助力开发者快速构建具备复杂工具调用能力的 AI Agent。

与传统框架相比，CORTEX 更专注于生产环境的落地，具备工业级的错误处理机制、灵活的配置系统以及极致的资源利用率。得益于 Go 语言的并发优势，CORTEX 能够为下一代 AI 应用提供卓越的性能表现和安全保障。

设计理念上，CORTEX 侧重于轻量化和易集成性。针对无需复杂流程编排的场景，CORTEX 摒弃了繁重的依赖和配置负担，在保留核心 Agent 能力的同时，大幅降低了集成门槛和资源占用，是构建高效、嵌入式 AI 应用的理想选择。

**`agent` 与 Dino**：**`agent`**（`github.com/xichan96/cortex/agent`）是基础用法——不需要复杂多轮对话与「模型自动决策并连续调用工具」时，直接集成进业务项目即可：例如单次调用生成报告、摘要、抽取、分类、结构化输出等，用 `engine` + `llm` + 可选工具，不必引入 Dino。**Dino**（`github.com/xichan96/cortex/dino`）是高级用法——面向多轮对话、自动决策并多步工具调用的**智能体**（助手、IDE Agent、客服机器人等），并叠加上下文隔离、预算、审批、事件观测等治理能力。

## 特性

- **智能代理引擎**：核心引擎支持复杂的工具调用与逻辑推理，轻松构建智能 Agent。
- **广泛的 LLM 支持**：深度集成 OpenAI、DeepSeek、火山引擎 (Volce) 等主流模型，并支持自定义 Provider。
- **多模态交互**：原生支持文本、图像等多模态数据的处理与交互。
- **动态技能 (Skills)**：基于文件系统的技能与懒加载；YAML 头可配置 `paths`、`when_to_use`、`allowed_tools`，支持路径 glob（`doublestar`）过滤，加载器可选解析符号链接与按真实路径去重。
- **开放工具生态**：`agent/tools/builtin` 覆盖文件、搜索、Shell/后台任务、网络与 Web、Docker、邮件、数学及 `mcp_client`；可自注册工具扩展。
- **实时流式响应**：全链路支持流式 (Streaming) 传输，为交互式应用提供丝滑的用户体验。
- **混合记忆架构**：“全量记录 + 滚动摘要”，异步压缩经互斥与按轮计数（`CompactAfterTurns`）避免重叠；组 prompt 时可按粗略 Token 预算裁剪近期对话（`MaxBudgetTokens`、`RemainPromptTokens`）；Provider 可实现廉价 `StoredMessageCount` 辅助门控。支持 LangChain、MongoDB、Redis、MySQL 及 SQLite。
- **灵活配置**：提供细粒度的配置选项，满足对 Agent 行为的精准控制需求。
- **高并发工具调用**：支持并行执行多个工具调用，显著提升任务处理效率。
- **企业级错误处理**：内置完善的错误重试与降级机制，确保系统稳健运行。
- **Dino 生产编排**：多会话隔离、实时事件订阅、Token/工具/时间预算、循环检测、危险工具审批、优先任务队列、计划模式与子代理；会话快照保存/恢复（消息窗口、用量、记忆压缩轮次计数）；YAML 记忆调优（`max_budget_tokens`、`compact_after_turns`）及可选子代理结果回灌父会话记忆；内置工具与 MCP、Skills 集成。

## 架构概述

Cortex 采用模块化架构设计，核心组件如下：

> **注**：`agent` 包底层复用了 [LangChain](https://github.com/tmc/langchaingo) 的核心能力，通过其强大的 LLM 交互接口与工具调用机制，构建出灵活且强大的智能代理系统。

```
cortex/
├── agent/             # 核心代理功能
│   ├── engine/       # 代理引擎实现
│   ├── llm/          # LLM 提供商集成
│   ├── skills/       # 技能加载与管理（含 prompt/ 模板）
│   ├── tools/        # 工具生态系统（MCP、HTTP、内置工具）
│   ├── types/        # 核心类型定义
│   ├── providers/    # 外部服务提供商（记忆、LLM 适配等）
│   ├── hooks/        # 生命周期钩子
│   └── utils/        # 限流、循环检测、权限、预算等工具
├── dino/             # 高级编排（Client/Factory、预算、审批、队列、事件总线）
│   ├── agent/        # 子代理与提示模板
│   ├── session/      # 会话实现与计划辅助
│   ├── memory/       # 记忆管理（如 SQLite）
│   ├── queue/        # 优先任务队列
│   ├── tools/        # 工具注册、内置工具、MCP、技能工具
│   └── permission/   # 工具权限与审批
├── trigger/          # 触发器模块
│   ├── http/         # HTTP 触发器（REST API）
│   └── mcp/          # MCP 触发器（MCP 服务器）
└── examples/         # 示例应用程序
```

## 快速开始

### 安装

```bash
go get github.com/xichan96/cortex
```

### 极简示例

以下代码展示了如何快速构建一个具备天气查询能力的 AI Agent：

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/llm"
	"github.com/xichan96/cortex/agent/types"
)

func main() {
	// 1. 初始化 LLM Provider
	llmProvider, err := llm.OpenAIClient("your-api-key", "gpt-4o-mini")
	if err != nil {
		panic(err)
	}

	// 2. 配置 Agent
	agentConfig := types.NewAgentConfig()
	agentConfig.SystemMessage = "你是一个有帮助的 AI 助手。"
	agentConfig.Timeout = 30 * time.Second

	// 3. 创建引擎实例
	agentEngine := engine.NewAgentEngine(llmProvider, agentConfig)

	// 4. 执行任务
	result, err := agentEngine.Execute(context.Background(), "今天纽约的天气怎么样？", nil)
	if err != nil {
		fmt.Printf("执行失败: %v\n", err)
		return
	}

	fmt.Printf("回复: %s\n", result.Output)
}
```

### 启动服务

Cortex 提供了开箱即用的 HTTP 服务，支持通过配置文件快速启动：

```bash
# 使用默认配置 cortex.yaml 启动
go run cortex.go

# 指定配置文件路径
go run cortex.go -config /path/to/cortex.yaml
```

服务默认监听 `:5678` 端口，提供以下核心接口：
- `POST /chat`: 标准对话接口
- `POST /chat/stream`: 流式对话接口 (SSE)
- `ANY /mcp`: MCP 协议接口

## 核心组件

### `agent` 包（基础）

不依赖 Dino 时，`agent` 即完整基础层：LLM、引擎、工具与记忆等积木，适合单次或浅层调用、自行编排请求生命周期。典型场景是管道里「调一次模型（+ 可选工具）出结果」，而非长会话里由模型反复选工具。下面 LLM 示例属于该路径。

### LLM 集成

Cortex 提供了统一的接口适配多种 LLM Provider，支持灵活的参数配置：

```go
// OpenAI
llmProvider, _ := llm.OpenAIClient("sk-...", "gpt-4o")

// DeepSeek
llmProvider, _ := llm.QuickDeepSeekProvider("sk-...", "deepseek-chat")

// 火山引擎 (Volcengine)
llmProvider, _ := llm.VolceClient("ak-...", "doubao-pro-32k")
```

## Dino 高级编排

[Dino](dino/README-CN.md) 用于构建**智能体产品**：多轮对话、模型根据上下文**自动决定是否调用工具、调用哪几个、如何衔接多步**，并在长会话中持续迭代；同时满足多用户、多会话、预算与审批等上线要求。底层仍依赖 `agent` 的执行能力，由 **`dino` 管会话生命周期、观测面与风控面**。

### 定位

- **`agent`**：基础集成；单请求/批处理、单次生成报告类任务、简单 API；组件含 LLM Provider、Engine、Skills、记忆 Provider、hooks、内置工具。
- **`dino`**：智能体形态；多轮 + 自动工具决策与执行闭环；含 `NewDinoFactory` / `NewClient` / `Session`、事件总线（`bus`）、`DefinedTool` 与工具回调、按工具超时与全局预算、循环检测、白名单/黑名单与审批、SQLite 记忆、子代理与 Manager、可选 `queue` 批处理与 Planner。

### 会话与可观测性

- 每会话独立上下文；`Send` / `SendAndWait` 等 API 驱动一轮或多轮工具循环。
- `Subscribe` / `SubscribeFunc` 订阅执行过程：`Message`、`Thinking`、`ToolCall`、`Done` 等事件，便于终端 UI、日志与埋点。
- 支持注入流式事件发送方（工厂 Option），对接自有 SSE/WebSocket 管道。

### 工具与安全

- `Config.Tools`：`Allowed` / `Denied` / `ApprovalRequired` 控制面；`ToolTimeouts` 与 `ToolTimeoutCalculator` 按工具与入参细化超时。
- `defined_tool`：`DefinedTool`、`ToolContext`、审批存储（危险操作可阻塞至人工确认）。
- 内置工具覆盖读写编辑文件、`glob`/`grep`、`bash`、`list_directory` 等；可接 MCP 与基于目录的 Skills（`cfg.Skills`）。

### 资源与稳定性

- **预算**：`Budget` 限制 Token、工具调用次数、 wall-clock，防止失控消耗。
- **记忆调优（YAML / 工厂）**：`memory.max_budget_tokens`、`memory.compact_after_turns`；子代理历史条数上限（默认 48）；可选 `replay_to_parent_memory`，将子任务与输出（截断）写入父会话记忆。
- **会话快照**：保存/恢复窗口内消息、累计用量及引擎记忆压缩轮次计数，便于持久化会话。
- **循环检测**：`LoopDetection` 基于语义相似度与重复次数中断死循环。
- **计划模式**：`PlannerMode` 可先产出步骤计划再执行，可选自动批准。

### 最小用法

```bash
go get github.com/xichan96/cortex/dino
```

```go
import (
	"context"
	"fmt"
	"log"

	"github.com/xichan96/cortex/dino"
)

func main() {
	cfg := dino.DefaultConfig()
	cfg.Provider.APIKey = "your-api-key"
	cfg.WorkspaceRoot = "/path/to/workspace"

	factory, err := dino.NewDinoFactory(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer factory.Shutdown(context.Background())

	client := dino.NewClient(factory)
	session, err := client.CreateSession(context.Background(), "sid-1")
	if err != nil {
		log.Fatal(err)
	}
	ev, err := session.SendAndWait(context.Background(), "列出当前目录")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(ev.Content)
}
```

完整配置项、队列、子代理与事件类型见 [dino/README-CN.md](dino/README-CN.md)；可运行示例：[examples/dino](examples/dino)。

## 工具生态

内置工具源码在 `agent/tools/builtin/`，按子包划分；实现 `types.Tool` 注册进引擎即可启用（具体默认集取决于你的 Agent 配置）。

| 目录 | 工具名 |
|------|--------|
| `fs/` | `read_file`、`write_file`、`edit_file`、`file` |
| `search/` | `glob`、`grep`、`codesearch`（当前为占位实现） |
| `runtime/` | `command`（本地 Shell）、`question`（向用户澄清）、`job_kill`、`job_output`（后台命令任务） |
| `task/` | `todo`（进程内任务：ID、pending / in_progress / completed / cancelled，支持列出与更新） |
| `net/` | `ssh`、`net_check`（连通性检测） |
| `web/` | `web_search`、`web_fetch` |
| `system/` | `get_time`、`http_request` |
| `email/` | `send_email` |
| `math/` | `math_calculate` |
| `docker/` | `docker_list_containers`、`docker_inspect_container`、`docker_container_logs`、`docker_exec`、`docker_create_container`、`docker_start_container`、`docker_stop_container`、`docker_restart_container`、`docker_remove_container`、`docker_pull_image` |
| `mcp/` | `mcp_client`（连接外部 MCP Server） |

### 自定义工具

实现 `types.Tool` 接口即可轻松扩展自定义工具：

```go
type MyTool struct{}

func (t *MyTool) Name() string { return "my_tool" }
func (t *MyTool) Description() string { return "这是一个自定义工具" }
func (t *MyTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
    // 业务逻辑...
    return "执行结果", nil
}
```

## 技能系统 (Agent Skills)

Cortex 实现了一套独特的**基于文件系统的技能系统**，允许您在不重新编译代码的情况下扩展 Agent 的能力。

### 工作原理

1.  **定义**：在技能目录中创建 `SKILL.md` 文件（例如 `./skills/my-skill/SKILL.md`）。
2.  **描述**：使用 Markdown 描述任务并提供可执行示例（如 `curl`、SQL）。可选 YAML 头可写 `name`、`description`、`paths`（适用路径 glob）、`when_to_use`、`allowed_tools`；按当前激活路径过滤后，仅匹配技能会合并进系统提示。
3.  **发现**：扫描技能目录（可选解析符号链接、按真实路径去重）并注入适用技能。
4.  **执行**：Agent 按 `SKILL.md` 指引完成任务。

### 技能示例 (`skills/weather/SKILL.md`)

````markdown
---
name: weather
description: Get current weather using command line tools.
---

# Weather

To check the weather, use `curl` with wttr.in:

```bash
curl -s "wttr.in/New+York?format=3"
```
````

这种方式使您可以轻松地将任何 CLI 工具、API 或脚本转化为 Agent 的原生能力。

## 任务调度系统

Cortex 内置了强大的任务调度系统 (`xcron`)，允许 Agent 自主管理定时任务。这使得 Agent 不仅能响应即时请求，还能处理周期性任务或延时任务。

### 核心功能

- **多种调度模式**：
  - `oneshot`: 延时执行一次（例如：“10分钟后提醒我喝水”）。
  - `periodic`: 周期性执行（例如：“每隔 2 小时检查一次服务器状态”）。
  - `cron`: 基于 Cron 表达式的精确定时（例如：“每天早上 8 点发送日报”）。
- **持久化存储**：支持任务持久化，即使服务重启，任务也不会丢失。
- **Agent 自驱动**：Agent 可以通过内置工具 (`schedule_job`, `list_jobs`, `delete_job`) 自主创建、查询和管理任务。

### 示例场景

用户：“每天早上 9 点帮我总结一下昨天的 Hacker News 热点。”

Agent 会自动调用 `schedule_job` 工具：
```json
{
  "name": "hn_daily_summary",
  "type": "cron",
  "schedule": "0 0 9 * * *",
  "payload": "总结昨天的 Hacker News 热点新闻，并发送给我。",
  "task_type": "agent_task"
}
```

## 记忆系统

Cortex 引入了先进的**混合记忆架构 (Hybrid Memory Architecture)**，旨在解决长对话场景下的 Token 消耗与上下文丢失问题。

### 架构特点

1.  **全量记录 (Raw History)**：完整保留交互（读取侧多为窗口化，具体取决于后端）。
2.  **滚动摘要 (Rolling Summary)**：后台异步压缩摘要；可按轮次（`CompactAfterTurns`）与可选消息计数门控触发。
3.  **智能检索**：组合「摘要 + 近期对话」；近期对话可在请求前按粗略 Token 预算裁剪（`MaxBudgetTokens`、`RemainPromptTokens`，估算见 `agent/types`）。
4.  **异步处理**：摘要与压缩后台执行，带 Panic 恢复与锁，避免与用户轮次争抢。

### 存储后端

支持多种持久化存储方案，只需简单配置即可切换：

- **Memory (默认)**: 基于 LangChain 的内存实现，适用于测试。
- **Redis**: 高性能键值存储，推荐生产环境使用。
- **MongoDB**: 灵活的文档存储。
- **MySQL / SQLite**: 传统关系型数据库支持。

```go
// 示例：使用 Redis 作为记忆存储
redisClient := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
memory := providers.NewRedisMemoryProvider(redisClient, "session-id")
agentEngine.SetMemory(context.Background(), memory)
```

## 示例

- [版本摘要（英文）](docs/RELEASE_SUMMARY.md)：近期记忆、技能、工具与 Dino 变更说明。
- [Basic Example](examples/basic): 基础用法演示。
- [Dino Example](examples/dino): Dino 高级编排（会话、预算、工具审批）。
- [Chat Web](examples/chat-web): 基于 Gin + React 的完整聊天应用。
- [MCP Server](examples/mcp-server): 将 Agent 暴露为 MCP 服务。
- [Agent Skills](examples/skills): 动态加载和使用 Agent 技能。
- [Task Scheduling](examples/xcron): 使用 xcron 进行任务调度。

## 触发器 (Triggers)

`trigger/` 将 Agent 暴露为不同协议入口，便于外部系统集成。

#### HTTP 触发器
标准 RESTful API，支持普通对话与流式对话。

#### MCP 触发器
遵循 [Model Context Protocol (MCP)](https://modelcontextprotocol.io/)，可将 Cortex Agent 作为工具供 Claude Desktop 等客户端调用。

## 贡献

欢迎提交 Issue 和 Pull Request！

## 许可证

MIT License
