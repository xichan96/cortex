# Dino - Cortex 高级编排

<p align="center">
  <a href="README.md">English</a> | <strong>简体中文</strong>
</p>

Dino 是 [Cortex](https://github.com/xichan96/cortex) 框架的高级编排组件。它提供了企业级的会话管理、资源预算、循环检测和工具编排能力，用于构建生产级别的 AI 代理。

---

## 概述

Dino 在核心 Cortex 代理能力的基础上，添加了专为生产环境设计的高级功能：

- **多会话管理**：处理多个并发用户会话，每个会话上下文隔离
- **资源预算**：通过 token、工具调用次数和时间限制防止成本失控
- **循环检测**：自动检测并防止重复动作循环
- **工具审批工作流**：危险操作需要人工审批
- **任务队列**：内置优先队列支持批处理
- **计划模式**：将复杂任务分解为可执行的计划

---

## 功能特性

### 会话管理
- 基于 Channel 的输入/输出，实现非阻塞通信
- 观察者模式实现实时事件订阅
- 自动会话清理和资源管理

### 预算控制
- 每个会话可配置的 token 限制
- 工具调用次数限制
- 基于时间的预算执行

### 循环检测
- 动作模式的语义相似度检测
- 可配置的阈值和最大重复次数
- 自动阻止循环并通知用户

### 工具系统
- 内置工具：`read_file`、`write_file`、`edit_file`、`bash`、`glob`、`grep`、`list_directory`
- 可扩展的工具注册表
- 危险工具审批工作流
- MCP (Model Context Protocol) 支持

### 计划模式
- 执行前生成分步骤计划
- 信任工作流可选择自动审批
- 通过事件可视化计划

### 长任务与 harness（`dino/task`、`dino/runner`）
- **`dino/task`**：`Task` / `TaskConfig` / `StopReason`、检查点会话 `TaskSession`、`SessionSwapper` 等中性类型
- **`dino/runner`**：`DefaultTaskEngine` 对接 `dino/harness` 外环（验证、停滞、多轮续跑）、shell/文本/LLM 验证器、内存与 Blob 上的 `SessionStore`

### 聊天记录与结构化记忆（命名区分）
- **`dino/chatstore`**：Dino 工厂使用的**会话聊天记录** `Provider`（`OpenSharedChatStore`、按会话 SQLite / InMemory）
- **`pkg/memkit`**（模块 `github.com/xichan96/cortex/pkg/memkit`）：**偏好 / 知识 / 索引 / PageIndex** 等结构化记忆，与 `chatstore` 并列使用、职责不同

---

## 安装

```bash
go get github.com/xichan96/cortex/dino
```

---

## 快速开始

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/xichan96/cortex/dino"
)

func main() {
    // 1. 创建配置
    cfg := dino.DefaultConfig()
    cfg.Provider.APIKey = "your-api-key"
    cfg.WorkspaceRoot = "/path/to/workspace"

    // 2. 创建工厂
    factory, err := dino.NewDinoFactory(cfg)
    if err != nil {
        log.Fatal(err)
    }
    defer factory.Shutdown(context.Background())

    // 3. 创建客户端
    client := dino.NewClient(factory)

    // 4. 创建会话
    session, err := client.CreateSession(context.Background(), "session-1")
    if err != nil {
        log.Fatal(err)
    }

    // 5. 发送消息并获取响应
    ctx := context.Background()
    event, err := session.SendAndWait(ctx, "列出当前目录的文件")
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println("响应:", event.Content)
}
```

---

## 配置

```yaml
# dino.yaml
default_model: gpt-4o-mini
default_provider: openai
temperature: 0.7
max_tokens: 4096
timeout: 30s
max_iterations: 10

workspace_root: "."

# 工具配置
tools:
  allowed:
    - read_file
    - write_file
    - edit_file
    - glob
    - grep
    - bash
    - list_directory
  approval_required:
    - bash
    - write_file
    - edit_file

# 循环检测
loop_detection:
  enabled: true
  max_repeats: 3
  similarity_threshold: 0.8

# 预算控制
budget:
  enabled: true
  max_tokens: 100000
  max_tool_calls: 50
  max_time_ms: 300000

# 计划模式
planner_mode:
  enabled: false
  prompt_plan: "首先分析任务并创建分步骤计划。"
  auto_approve: false
```

---

## 高级用法

### 流式事件

```go
type StreamHandler struct{}

func (h *StreamHandler) SendStreamEvent(sessionID string, event interface{}) {
    // 处理流式事件
}

factory, _ := dino.NewDinoFactory(cfg, dino.WithStreamEventSender(handler))
```

### 任务队列

```go
import "github.com/xichan96/cortex/dino/queue"

session, _ := client.CreateSession(ctx, "queue-session", dino.WithQueueEnabled(100, 10))
resultChan, _ := session.Enqueue("任务 1", queue.PriorityNormal)
result := <-resultChan
```

### 事件观察者

```go
session.SubscribeFunc(func(event *dino.Event) {
    switch event.Type {
    case dino.EventTypeMessage:
        fmt.Println("消息:", event.Content)
    case dino.EventTypeThinking:
        fmt.Println("思考:", event.Thinking)
    case dino.EventTypeToolCall:
        fmt.Println("工具:", event.ToolName)
    case dino.EventTypeDone:
        fmt.Println("完成")
    }
})
```

---

## 核心类型

| 类型 | 描述 |
|------|------|
| `Client` | 会话管理的主要入口点 |
| `Session` | 代表单个用户对话 |
| `Config` | Dino 工厂的配置 |
| `Event` | 执行过程中的实时事件 |
| `Skill` | 特定任务的自定义提示模板 |
| `Budget` | 资源使用追踪和限制 |

---

## 架构

```
dino/
├── client.go        # 客户端和会话管理
├── factory.go       # 会话工厂、工具注册表、预算
├── config.go        # 配置类型
├── defined_tool.go  # DefinedTool、ToolContext、ApprovalStore
├── types.go         # 核心类型定义
├── bus.go           # 事件总线
├── session/         # 会话实现
│   ├── session.go   # 核心会话逻辑
│   ├── event.go     # 事件定义
│   ├── info.go      # 会话信息
│   └── planner.go   # 计划助手
├── queue/           # 任务队列
├── task/            # 长任务类型（Task、StopReason、SessionStore 接口）
├── runner/          # 外环编排与验证器、检查点存储实现
├── chatstore/       # 会话聊天记录 Provider（SQLite / 内存）
├── permission/      # 工具权限
├── agent/           # 子代理与提示词
└── tools/           # 注册表、内置工具、技能、MCP
```

---

## 许可证

MIT 许可证 - 详见 [LICENSE](../LICENSE)
