# CORTEX
![cortex-desc.png](docs/images/desc.png)
<p align="center">CORTEX 是一个为高效集成和利用大型语言模型 (LLM) 而设计的 AI Agent 框架，使用 Go 语言构建。</p>

<p align="center">
  <img alt="GitHub commit activity" src="https://img.shields.io/github/commit-activity/m/xichan96/cortex"/>
  <img alt="Github Last Commit" src="https://img.shields.io/github/last-commit/xichan96/cortex"/>
</p>

<p align="center">
  <a href="#概述">概述</a>
  · <a href="#特性与状态">特性</a>
  · <a href="#安装">安装</a>
  · <a href="#基本用法">用法</a>
  · <a href="#agent-模块使用">Agent 模块</a>
  · <a href="#示例">示例</a>
  · <a href="#许可证">许可证</a>
</p>

<p align="center">
  <a href="README.md">English</a> | 简体中文
</p>

## 概述

CORTEX 是一个为高效集成和利用大型语言模型 (LLM) 而设计的 AI Agent 框架。它使用 Go 语言构建，Go 是企业应用中最受欢迎的编程语言之一。CORTEX 结合了轻量级框架的简单性和 Go 语言的稳健性与性能，提供了与各种 LLM 的无缝集成，并提供了一套全面的工具，用于构建具有工具调用能力的 AI 代理。

与其他代理框架不同，CORTEX 专为生产环境部署而设计，具有强大的错误处理、灵活的配置和高效的资源利用。以 Go 语言为基础，CORTEX 为下一代 AI 应用提供卓越的性能和安全的代理能力。

CORTEX 实现的功能类似于 n8n 的 AI Agent，但采用了轻量级设计理念。在实际开发中，许多场景并不需要 n8n 提供的复杂流程编排能力，且将 n8n 完整集成到自有项目中存在一定的配置复杂度和资源占用问题。相比之下，本库专为简化集成流程而设计，保持了核心的 AI Agent 功能同时大幅降低了使用门槛，非常适合对资源占用和集成复杂度有严格要求的项目场景。

## 特性与状态

- **智能代理引擎**：用于创建具有高级工具调用能力的 AI 代理的核心功能。
- **LLM 集成**：无缝支持 OpenAI、DeepSeek、Volce（火山引擎）和自定义 LLM 提供商。
- **多模态支持**：轻松处理文本、图像和其他媒体格式。
- **工具生态系统**：可扩展的工具系统，内置 MCP 和 HTTP 客户端。
- **流式传输支持**：为交互式应用程序提供实时响应流式传输。
- **记忆体**：用于保存对话历史的上下文感知内存系统，支持 LangChain、MongoDB、Redis、MySQL 和 SQLite 存储。
- **配置灵活性**：全面的选项，用于微调代理行为。
- **并行工具调用**：高效地同时执行多个工具。
- **健壮的错误处理**：全面的错误管理和重试机制。

## 架构概述

Cortex 采用模块化架构，包含以下关键组件：

> 注意：agent 包基于 [LangChain](https://github.com/tmc/langchaingo) 实现，利用其强大的 LLM 交互和工具调用能力构建智能代理系统。

```
cortex/
├── agent/             # 核心代理功能
│   ├── engine/        # 代理引擎实现
│   ├── llm/           # LLM 提供商集成
│   ├── tools/         # 工具生态系统（MCP、HTTP）
│   ├── types/         # 核心类型定义
│   ├── providers/     # 外部服务提供商
│   ├── errors/        # 错误处理
│   └── logger/        # 结构化日志记录
├── trigger/           # 触发器模块
│   ├── http/          # HTTP 触发器（REST API）
│   └── mcp/           # MCP 触发器（MCP 服务器）
└── examples/          # 示例应用程序
    ├── basic/         # 基本用法示例
    ├── chat-web/      # 基于Web的聊天应用
    │   └── server/    # Web服务器实现
    └── mcp-server/    # MCP 服务器示例
```

## 快速开始

### 安装

```bash
go get github.com/xichan96/cortex
```

### 基本用法

以下是如何使用 Cortex 创建 AI 代理的简单示例：

```go
package main

import (
	"fmt"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/llm"
	"github.com/xichan96/cortex/agent/types"
)

func main() {
	// 1. 创建 LLM 提供商
	llmProvider, err := llm.OpenAIClient("your-api-key", "gpt-4o-mini")
	if err != nil {
		fmt.Printf("创建 LLM 提供商时出错: %v\n", err)
		return
	}

	// 2. 创建代理配置
	agentConfig := types.NewAgentConfig()
	// 基本配置
	agentConfig.MaxIterations = 5                  // 最大迭代次数
	agentConfig.ReturnIntermediateSteps = true    // 返回中间步骤
	agentConfig.SystemMessage = "你是一个有帮助的 AI 助手。"

	// 高级配置
	agentConfig.Temperature = 0.7                  // 创造力水平
	agentConfig.MaxTokens = 2048                   // 响应长度限制
	agentConfig.TopP = 0.9                         // Top P 采样
	agentConfig.FrequencyPenalty = 0.1             // 频率惩罚
	agentConfig.PresencePenalty = 0.1              // 存在惩罚
	agentConfig.Timeout = 30 * time.Second         // 请求超时
	agentConfig.RetryAttempts = 3                  // 重试次数
	agentConfig.EnableToolRetry = true             // 启用工具重试
	agentConfig.ToolRetryAttempts = 2              // 工具重试次数
	agentConfig.ParallelToolCalls = true           // 并行工具调用
	agentConfig.ToolCallTimeout = 10 * time.Second // 工具调用超时

	// 3. 创建代理引擎
	agentEngine := engine.NewAgentEngine(llmProvider, agentConfig)

	// 4. 添加工具（可选）
	// agentEngine.AddTool(yourTool)

	// 5. 执行代理
	result, err := agentEngine.Execute("今天纽约的天气怎么样？", nil)
	if err != nil {
		fmt.Printf("执行代理时出错: %v\n", err)
		return
	}

	fmt.Printf("代理结果: %s\n", result.Output)
}
```

### 运行主程序

Cortex 提供了开箱即用的主程序，可以通过配置文件快速启动服务：

```bash
# 使用默认配置文件 cortex.yaml
go run cortex.go

# 或指定配置文件路径
go run cortex.go -config /path/to/cortex.yaml
```

主程序提供了以下 HTTP 端点：
- `POST /chat`: 标准聊天接口
- `POST /chat/stream`: 流式聊天接口
- `ANY /mcp`: MCP 协议接口

默认服务端口为 `:5678`，可通过配置文件进行修改。

### API 接口文档

#### POST /chat

标准聊天接口，返回完整的对话结果。

**请求体：**
```json
{
  "session_id": "string",  // 会话ID，用于区分不同的对话会话
  "message": "string"       // 用户消息内容
}
```

**响应：**
```json
{
  "output": "string",                    // AI 代理的回复内容
  "tool_calls": [                        // 工具调用列表（如果有）
    {
      "tool": "string",                  // 工具名称
      "tool_input": {},                  // 工具输入参数
      "tool_call_id": "string",          // 工具调用ID
      "type": "string"                   // 工具调用类型
    }
  ],
  "intermediate_steps": []               // 中间步骤（如果启用）
}
```

**示例：**
```bash
curl -X POST http://localhost:5678/chat \
  -H "Content-Type: application/json" \
  -d '{
    "session_id": "user-123",
    "message": "今天北京的天气怎么样？"
  }'
```

#### POST /chat/stream

流式聊天接口，使用 Server-Sent Events (SSE) 实时返回响应。

**请求体：**
```json
{
  "session_id": "string",  // 会话ID，用于区分不同的对话会话
  "message": "string"       // 用户消息内容
}
```

**响应格式（SSE）：**

响应使用 `text/event-stream` 格式，包含以下事件类型：

1. **chunk 事件** - 内容片段
```
data: {"type":"chunk","content":"今天"}
```

2. **error 事件** - 错误信息
```
data: {"type":"error","error":"错误描述"}
```

3. **end 事件** - 结束标记
```
data: {"type":"end","end":true,"data":{"output":"完整回复","tool_calls":[],"intermediate_steps":[]}}
```

**示例：**
```bash
curl -X POST http://localhost:5678/chat/stream \
  -H "Content-Type: application/json" \
  -d '{
    "session_id": "user-123",
    "message": "给我讲一个故事"
  }'
```

**JavaScript 示例：**
```javascript
const eventSource = new EventSource('http://localhost:5678/chat/stream', {
  method: 'POST',
  body: JSON.stringify({
    session_id: 'user-123',
    message: '给我讲一个故事'
  })
});

eventSource.onmessage = (event) => {
  const data = JSON.parse(event.data);
  if (data.type === 'chunk') {
    console.log(data.content);
  } else if (data.type === 'end') {
    eventSource.close();
  }
};
```

#### ANY /mcp

MCP（Model Context Protocol）协议接口，支持 MCP 客户端连接。

该接口遵循 MCP 协议规范，自动注册以下工具：
- `ping`: 健康检查工具
- 可配置的聊天工具（名称和描述通过配置文件设置）

**使用方式：**

通过 MCP 客户端连接到 `http://localhost:5678/mcp`，即可使用注册的工具。

**示例（使用 MCP 客户端）：**
```bash
# MCP 客户端连接示例
mcp-client connect http://localhost:5678/mcp
```

### Docker 部署

使用 Docker 快速部署 Cortex 服务：

```bash
# 构建 Docker 镜像
docker build -f build/Dockerfile -t cortex:latest .

# 运行容器
docker run -d -p 5678:5678 \
  -v /path/to/cortex.yaml:/go/bin/cortex.yaml \
  cortex:latest \
  /go/bin/cortex -config /go/bin/cortex.yaml
```

## Agent 模块使用

Agent 模块是 Cortex 框架的核心，提供智能和工具集成功能。

### LLM 提供商集成

Cortex 支持 OpenAI、DeepSeek、Volce（火山引擎）和自定义 LLM 提供商，具有灵活的配置选项：

```go
// OpenAI 默认配置
llmProvider, err := llm.OpenAIClient("your-api-key", "gpt-4o-mini")

// OpenAI 自定义基础 URL
llmProvider, err := llm.OpenAIClientWithBaseURL("your-api-key", "https://custom-api.example.com", "custom-model")

// DeepSeek 集成
llmProvider, err := llm.QuickDeepSeekProvider("your-api-key", "deepseek-chat")

// Volce（火山引擎）集成
llmProvider, err := llm.VolceClient("your-api-key", "doubao-seed-1-6-251015")

// Volce 自定义基础 URL
llmProvider, err := llm.VolceClientWithBaseURL("your-api-key", "https://ark.cn-beijing.volces.com/api/v3", "doubao-seed-1-6-251015")

// 使用 OpenAI 的高级选项
opts := llm.OpenAIOptions{
	APIKey:  "your-api-key",
	BaseURL: "https://api.openai.com",
	Model:   "gpt-4o",
	OrgID:   "your-organization-id",
}
llmProvider, err := llm.NewOpenAIClient(opts)

// 使用 DeepSeek 的高级选项
opts := llm.DeepSeekOptions{
	APIKey:  "your-api-key",
	BaseURL: "https://api.deepseek.com",
	Model:   "deepseek-chat",
}
llmProvider, err := llm.NewDeepSeekClient(opts)

// 使用 Volce 的高级选项
opts := llm.VolceOptions{
	APIKey:  "your-api-key",
	BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
	Model:   "doubao-seed-1-6-251015",
}
llmProvider, err := llm.NewVolceClient(opts)
```

### Agent 配置

使用 `AgentConfig` 结构体对代理进行广泛配置：

```go
agentConfig := types.NewAgentConfig()

// 基本配置
agentConfig.MaxIterations = 5                  // 最大迭代次数
agentConfig.ReturnIntermediateSteps = true    // 返回中间步骤
agentConfig.SystemMessage = "你是一个有帮助的 AI 助手。"

// 高级配置
agentConfig.Temperature = 0.7                  // 创造力水平
agentConfig.MaxTokens = 2048                   // 响应长度限制
agentConfig.TopP = 0.9                         // Top P 采样
agentConfig.FrequencyPenalty = 0.1             // 频率惩罚
agentConfig.PresencePenalty = 0.1              // 存在惩罚
agentConfig.Timeout = 30 * time.Second         // 请求超时
agentConfig.RetryAttempts = 3                  // 重试次数
agentConfig.EnableToolRetry = true             // 启用工具重试
agentConfig.ToolRetryAttempts = 2              // 工具重试次数
agentConfig.ParallelToolCalls = true           // 并行工具调用
agentConfig.ToolCallTimeout = 10 * time.Second // 工具调用超时
```

### Agent 引擎创建

```go
// 使用 LLM 提供商和配置创建代理引擎
agentEngine := engine.NewAgentEngine(llmProvider, agentConfig)
```

### 工具管理

通过添加工具扩展代理的能力：

```go
// 添加单个工具
agentEngine.AddTool(tool)

// 添加多个工具
agentEngine.AddTools([]types.Tool{tool1, tool2, tool3})
```

### Agent 执行

使用各种输入类型和模式执行代理：

```go
// 使用文本输入执行
result, err := agentEngine.Execute("法国的首都是什么？", nil)
if err != nil {
	// 处理错误
}
fmt.Printf("代理输出: %s\n", result.Output)

// 使用流式传输执行
stream, err := agentEngine.ExecuteStream("给我讲一个关于 AI 的故事。", nil)
if err != nil {
	// 处理错误
}

for chunk := range stream {
	if chunk.Error != nil {
		// 处理流式传输错误
		break
	}
	fmt.Printf("%s", chunk.Content)
}

// 注意：当前版本 Execute 方法仅支持文本输入
// 多模态输入（如图像）功能正在开发中
```

### 内置工具集成

#### MCP 工具集成

利用对 MCP（模型控制协议）工具的内置支持：

```go
import "github.com/xichan96/cortex/pkg/mcp"

// 创建 MCP 客户端
mcpClient := mcp.NewClient("https://api.example.com/mcp/sse", "http", map[string]string{
	"Content-Type": "application/json",
})

// 连接到 MCP 服务器
ctx := context.Background()
if err := mcpClient.Connect(ctx); err != nil {
	// 处理连接错误
}

// 获取 MCP 工具并添加到代理
mcpTools := mcpClient.GetTools()
agentEngine.AddTools(mcpTools)

// 完成后不要忘记断开连接
defer mcpClient.Disconnect(ctx)
```

#### 内建工具

Cortex 提供了一系列开箱即用的内建工具，可以直接添加到Agent中使用：

##### SSH 工具

通过 SSH 在远程服务器上执行命令，支持密码、私钥和 SSH 代理认证，还支持跳板机：

```go
import "github.com/xichan96/cortex/agent/tools/builtin"

// 创建 SSH 工具
sshTool := builtin.NewSSHTool()
agentEngine.AddTool(sshTool)
```

SSH 工具支持以下参数：
- `username`: SSH 用户名（必需）
- `address`: SSH 服务器地址（必需）
- `command`: 要执行的命令（必需）
- `password`: SSH 密码（可选）
- `private_key`: SSH 私钥内容（可选）
- `agent_socket`: SSH 代理套接字路径（可选）
- `port`: SSH 服务器端口（默认：22）
- `timeout`: 连接超时时间（秒，默认：15）
- `bastion`: 跳板机地址（可选）
- `bastion_port`: 跳板机端口（默认：22）
- `bastion_user`: 跳板机用户名（可选）

##### 文件工具

执行文件和目录操作，包括读取、写入、创建、删除、复制、移动和列出操作：

```go
import "github.com/xichan96/cortex/agent/tools/builtin"

// 创建文件工具
fileTool := builtin.NewFileTool()
agentEngine.AddTool(fileTool)
```

文件工具支持以下操作：
- `read_file`: 读取文件内容
- `write_file`: 写入文件
- `append_file`: 追加内容到文件
- `create_dir`: 创建目录
- `delete_file`: 删除文件
- `delete_dir`: 删除目录
- `list_dir`: 列出目录内容
- `exists`: 检查文件或目录是否存在
- `copy`: 复制文件或目录
- `move`: 移动文件或目录
- `is_file`: 检查路径是否为文件
- `is_dir`: 检查路径是否为目录

##### 邮件工具

发送邮件，支持 HTML、纯文本和 Markdown 内容类型：

```go
import (
	"github.com/xichan96/cortex/agent/tools/builtin"
	"github.com/xichan96/cortex/pkg/email"
)

// 配置邮件客户端
emailConfig := &email.Config{
	SMTPHost:     "smtp.example.com",
	SMTPPort:     587,
	SMTPUsername: "your-username",
	SMTPPassword: "your-password",
	From:         "sender@example.com",
}

// 创建邮件工具
emailTool := builtin.NewEmailTool(emailConfig)
agentEngine.AddTool(emailTool)
```

邮件工具支持以下参数：
- `to`: 收件人邮箱地址列表（必需）
- `subject`: 邮件主题（必需）
- `type`: 内容类型，支持 `text/html`、`text/plain`、`text/markdown`（必需）
- `message`: 邮件内容（必需）

##### 命令工具

在本地执行 shell 命令并返回输出，支持超时配置：

```go
import "github.com/xichan96/cortex/agent/tools/builtin"

// 创建命令工具
commandTool := builtin.NewCommandTool()
agentEngine.AddTool(commandTool)
```

命令工具支持以下参数：
- `command`: 要执行的命令（必需）
- `timeout`: 命令执行超时时间（秒，默认：30）

##### 数学计算工具

执行数学计算，支持基本运算、高级运算和三角函数：

```go
import "github.com/xichan96/cortex/agent/tools/builtin"

// 创建数学工具
mathTool := builtin.NewMathTool()
agentEngine.AddTool(mathTool)
```

数学工具支持以下参数：
- `expression`: 数学表达式（必需），支持：
  - 基本运算：`+`, `-`, `*`, `/`, `%`
  - 高级运算：`^`（幂运算）, `√` 或 `sqrt`（开方）, `!`（阶乘）
  - 三角函数：`sin`, `cos`, `tan`, `asin`/`arcsin`, `acos`/`arccos`, `atan`/`arctan`
  - 对数函数：`ln`, `log`/`log10`, `exp`
  - 其他函数：`abs`, `floor`, `ceil`, `round`
- `use_degrees`: 是否使用角度制（默认：false，使用弧度制）

##### 时间工具

获取指定时区的当前时间：

```go
import "github.com/xichan96/cortex/agent/tools/builtin"

// 创建时间工具
timeTool := builtin.NewTimeTool()
agentEngine.AddTool(timeTool)
```

时间工具支持以下参数：
- `timezone`: 时区名称（可选，默认：`Asia/Hong_Kong`），例如：`Asia/Hong_Kong`、`America/New_York`、`UTC`

##### 网络检查工具

检查到远程主机的网络连通性：

```go
import "github.com/xichan96/cortex/agent/tools/builtin"

// 创建网络检查工具
pingTool := builtin.NewPingTool()
agentEngine.AddTool(pingTool)
```

网络检查工具支持以下参数：
- `address`: 目标地址，格式为 `host:port`（必需），例如：`example.com:80` 或 `192.168.1.1:22`
- `timeout`: 连接超时时间（秒，默认：5）

### 触发器模块

Cortex 提供触发器模块，通过不同协议暴露您的代理，便于与各种系统集成。

#### HTTP 触发器

将您的代理暴露为 HTTP API 端点，支持聊天和流式聊天：

```go
import (
	"github.com/gin-gonic/gin"
	httptrigger "github.com/xichan96/cortex/trigger/http"
)

// 创建 HTTP 处理器
httpHandler := httptrigger.NewHandler(agentEngine)

// 设置路由
r := gin.Default()
r.POST("/chat", httpHandler.ChatAPI)
r.GET("/chat/stream", httpHandler.StreamChatAPI)
r.POST("/chat/stream", httpHandler.StreamChatAPI)
```

HTTP 触发器提供两个端点：
- `ChatAPI`: 标准聊天端点，返回完整结果
- `StreamChatAPI`: 流式聊天端点，使用服务器发送事件 (SSE) 提供实时响应

#### MCP 触发器

将您的代理暴露为 MCP（模型上下文协议）服务器，允许其他 MCP 客户端将其作为工具使用：

```go
import (
	"github.com/gin-gonic/gin"
	"github.com/xichan96/cortex/trigger/mcp"
)

// 配置 MCP 选项
mcpOpt := mcp.Options{
	Server: mcp.Metadata{
		Name:    "cortex-mcp",
		Version: "0.1.0",
	},
	Tool: mcp.Metadata{
		Name:        "chat",
		Description: "与 AI 代理聊天",
	},
}

// 创建 MCP 处理器
mcpHandler := mcp.NewHandler(agentEngine, mcpOpt)

// 设置路由
r := gin.Default()
mcpGroup := r.Group("/mcp")
mcpGroup.Any("", mcpHandler.Agent())
```

MCP 触发器自动注册：
- `ping`: 健康检查工具
- 一个可配置的聊天工具，用于执行代理

## 示例

### 基本示例

`examples/basic` 目录包含一个简单示例，演示如何使用 Cortex 创建通过 MCP 连接到 AI 训练服务的 AI 代理。

```go
// 请参阅 examples/basic/main.go 获取完整示例
```

### 聊天 Web 示例

`examples/chat-web` 目录包含使用 Cortex 和 HTTP 触发器的基于 Web 的聊天应用程序。

```go
// 请参阅 examples/chat-web/main.go 获取完整示例
```
![chat-web-screenshot.png](docs/images/c.png)

### MCP 服务器示例

`examples/mcp-server` 目录包含一个示例，演示如何将您的代理暴露为 MCP 服务器。

```go
// 请参阅 examples/mcp-server/main.go 获取完整示例
```
## 高级用法

### 自定义工具

您可以通过实现 `types.Tool` 接口来创建自定义工具：

```go
type CustomTool struct {}

func (t *CustomTool) Name() string {
	return "custom_tool"
}

func (t *CustomTool) Description() string {
	return "执行特定功能的自定义工具"
}

func (t *CustomTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"input": map[string]interface{}{
				"type":        "string",
				"description": "自定义工具的输入",
			},
		},
		"required": []string{"input"},
	}
}

func (t *CustomTool) Execute(input map[string]interface{}) (interface{}, error) {
	// 工具执行逻辑
	return "工具结果", nil
}

func (t *CustomTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "custom_tool",
		IsFromToolkit:  false,
		ToolType:       "custom",
	}
}
```

### 记忆体管理

Cortex 提供用于对话历史的内存管理功能，支持多种存储后端：

#### LangChain 记忆体（默认）

```go
// 设置 LangChain 内存提供商
memoryProvider := providers.NewLangChainMemory()
agentEngine.SetMemory(memoryProvider)

// 配置内存使用
agentConfig.MaxTokensFromMemory = 1000 // 内存中的最大令牌数
```

#### MongoDB 记忆体

使用 MongoDB 作为持久化存储：

```go
import (
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/pkg/mongodb"
)

// 创建 MongoDB 客户端
mongoClient, err := mongodb.NewClient("mongodb://localhost:27017", "database_name")
if err != nil {
	// 处理错误
}

// 创建 MongoDB 内存提供商
memoryProvider := providers.NewMongoDBMemoryProvider(mongoClient, "session-id")

// 可选：设置最大历史消息数
memoryProvider.SetMaxHistoryMessages(100)

// 可选：设置集合名称（默认为 "chat_messages"）
memoryProvider.SetCollectionName("chat_messages")

// 设置内存提供商
agentEngine.SetMemory(memoryProvider)
```

#### Redis 记忆体

使用 Redis 作为持久化存储：

```go
import (
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/pkg/redis"
)

// 创建 Redis 客户端
redisClient := redis.NewClient(&redis.Options{
	Addr: "localhost:6379",
})

// 创建 Redis 内存提供商
memoryProvider := providers.NewRedisMemoryProvider(redisClient, "session-id")

// 可选：设置最大历史消息数
memoryProvider.SetMaxHistoryMessages(100)

// 可选：设置键前缀（默认为 "chat_messages"）
memoryProvider.SetKeyPrefix("chat_messages")

// 设置内存提供商
agentEngine.SetMemory(memoryProvider)
```

#### MySQL 记忆体

使用 MySQL 作为持久化存储：

```go
import (
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/pkg/sql/mysql"
)

// 创建 MySQL 客户端
mysqlCfg := &mysql.Config{
	Host:     "localhost",
	Port:     3306,
	User:     "root",
	Password: "password",
	Database: "cortex",
}
mysqlClient, err := mysql.NewClient(mysqlCfg)
if err != nil {
	// 处理错误
}

// 创建 MySQL 内存提供商
memoryProvider := providers.NewMySQLMemoryProvider(mysqlClient, "session-id")

// 可选：设置最大历史消息数
memoryProvider.SetMaxHistoryMessages(100)

// 可选：设置表名（默认为 "chat_messages"）
memoryProvider.SetTableName("chat_messages")

// 设置内存提供商
agentEngine.SetMemory(memoryProvider)
```

#### SQLite 记忆体

使用 SQLite 作为持久化存储：

```go
import (
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/pkg/sql/sqlite"
)

// 创建 SQLite 客户端
sqliteCfg := &sqlite.Config{
	Path: "cortex.db",
}
sqliteClient, err := sqlite.NewClient(sqliteCfg)
if err != nil {
	// 处理错误
}

// 创建 SQLite 内存提供商
memoryProvider := providers.NewSQLiteMemoryProvider(sqliteClient, "session-id")

// 可选：设置最大历史消息数
memoryProvider.SetMaxHistoryMessages(100)

// 可选：设置表名（默认为 "chat_messages"）
memoryProvider.SetTableName("chat_messages")

// 设置内存提供商
agentEngine.SetMemory(memoryProvider)
```

### 错误处理

Cortex 包含全面的错误处理：

```go
import "github.com/xichan96/cortex/agent/errors"

// 检查特定错误类型
if errors.Is(err, errors.ErrToolExecution) {
	// 处理工具执行错误
} else if errors.Is(err, errors.ErrLLMCall) {
	// 处理 LLM 调用错误
}
```

## 配置参考

### 代理配置选项

| 选项 | 描述 | 默认值 |
|--------|-------------|---------|
| `MaxIterations` | 最大迭代次数 | 5 |
| `ReturnIntermediateSteps` | 返回中间步骤 | false |
| `SystemMessage` | 系统提示消息 | "" |
| `Temperature` | LLM 温度（创造力） | 0.7 |
| `MaxTokens` | 每个响应的最大令牌数 | 2048 |
| `TopP` | Top P 采样参数 | 0.9 |
| `FrequencyPenalty` | 频率惩罚 | 0.1 |
| `PresencePenalty` | 存在惩罚 | 0.1 |
| `Timeout` | 请求超时 | 30s |
| `RetryAttempts` | 重试次数 | 3 |
| `EnableToolRetry` | 启用工具重试 | false |
| `ToolRetryAttempts` | 工具重试次数 | 2 |
| `ParallelToolCalls` | 启用并行工具调用 | false |
| `ToolCallTimeout` | 工具调用超时 | 10s |
| `MaxTokensFromMemory` | 内存中的最大令牌数 | 1000 |
| `EnableCache` | 启用响应缓存 | true |
| `CacheSize` | 缓存项的最大数量 | 1000 |

## 贡献

欢迎贡献！请随时提交 Pull Request。

## 许可证

本项目采用 MIT 许可证 - 有关详细信息，请参阅 LICENSE 文件。

## 支持

如有问题、疑问或功能请求，请在 GitHub 存储库中创建问题。