# CORTEX
![cortex-desc.png](docs/images/desc.png)
<p align="center">CORTEX is a high-performance AI Agent framework built in Go, engineered for the efficient integration and orchestration of Large Language Models (LLMs).</p>

<p align="center">
  <img alt="GitHub commit activity" src="https://img.shields.io/github/commit-activity/m/xichan96/cortex"/>
  <img alt="Github Last Commit" src="https://img.shields.io/github/last-commit/xichan96/cortex"/>
</p>

<p align="center">
  <a href="#overview">Overview</a>
  · <a href="#features">Features</a>
  · <a href="#quick-start">Quick Start</a>
  · <a href="#core-components">Core Components</a>
  · <a href="#tools">Tools</a>
  · <a href="#agent-skills">Agent Skills</a>
  · <a href="#memory-system">Memory System</a>
  · <a href="#examples">Examples</a>
  · <a href="#license">License</a>
</p>

<p align="center">
  English | <a href="README-CN.md">简体中文</a>
</p>

## Overview

By bridging the simplicity of a lightweight framework with the robustness of Go, CORTEX enables seamless integration with leading LLMs. It provides a comprehensive toolkit for building AI agents capable of complex tool execution and reasoning.

Designed for production environments, CORTEX prioritizes reliability, configurability, and resource efficiency. It empowers developers to build next-generation AI applications with the performance and safety guarantees inherent to the Go ecosystem.

**Design Philosophy**: CORTEX adopts a minimalist approach, focusing on streamlined integration and low resource footprint. It eliminates heavy dependencies and complex orchestration overhead, making it the ideal choice for developers who need powerful Agent capabilities without the bloat of a full-fledged workflow automation platform.

## Features

- **Intelligent Agent Engine**: A robust core for building agents with advanced reasoning and tool-calling capabilities.
- **Broad LLM Support**: Seamless integration with OpenAI, DeepSeek, Volce, and custom providers.
- **Multi-Modal Native**: Effortlessly process and generate text, images, and other media formats.
- **Dynamic Skills**: File-system-based skill management with Lazy Loading for optimal performance.
- **Extensible Tooling**: Built-in support for MCP and HTTP clients, making tool extension trivial.
- **Real-Time Streaming**: Full support for response streaming, enabling interactive, low-latency user experiences.
- **Hybrid Memory Architecture**: Implements a hybrid strategy combining full conversation history with rolling summaries. This approach optimizes token usage while retaining full context, backed by asynchronous compression to ensure low latency under high concurrency. Compatible with LangChain, MongoDB, Redis, MySQL, and SQLite.
- **Granular Configuration**: Extensive options to fine-tune agent behavior and performance.
- **Parallel Execution**: Efficiently execute multiple tool calls concurrently to minimize wait times.
- **Production-Grade Reliability**: Comprehensive error handling and retry mechanisms built for stability.

## Architecture Overview

Cortex follows a modular architecture with the following key components:

> **Note**: The `agent` package is built on top of [LangChain](https://github.com/tmc/langchaingo), leveraging its powerful LLM interaction and tool-calling capabilities to build intelligent agent systems.

```
cortex/
├── agent/             # Core agent functionality
│   ├── engine/        # Agent engine implementation
│   ├── llm/           # LLM provider integrations
│   ├── skills/        # Skill loading and management
│   ├── tools/         # Tool ecosystem (MCP, HTTP)
│   ├── types/         # Core type definitions
│   ├── providers/     # External service providers
│   ├── errors/        # Error handling
│   └── logger/        # Structured logging
├── trigger/           # Trigger modules
│   ├── http/          # HTTP trigger (REST API)
│   └── mcp/           # MCP trigger (MCP server)
└── examples/          # Example applications
```

## Quick Start

### Installation

```bash
go get github.com/xichan96/cortex
```

### Minimal Example

Create a weather-checking AI agent in seconds:

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
	// 1. Initialize LLM Provider
	llmProvider, err := llm.OpenAIClient("your-api-key", "gpt-4o-mini")
	if err != nil {
		panic(err)
	}

	// 2. Configure Agent
	agentConfig := types.NewAgentConfig()
	agentConfig.SystemMessage = "You are a helpful AI assistant."
	agentConfig.Timeout = 30 * time.Second

	// 3. Create Engine
	agentEngine := engine.NewAgentEngine(llmProvider, agentConfig)

	// 4. Execute
	result, err := agentEngine.Execute(context.Background(), "What is the weather in New York?", nil)
	if err != nil {
		fmt.Printf("Execution failed: %v\n", err)
		return
	}

	fmt.Printf("Response: %s\n", result.Output)
}
```

### Run the Service

Cortex ships with a ready-to-deploy HTTP server:

```bash
# Run with default config
go run cortex.go

# Run with custom config
go run cortex.go -config /path/to/cortex.yaml
```

Default endpoints (port `:5678`):
- `POST /chat`: Standard chat
- `POST /chat/stream`: Streaming chat (SSE)
- `ANY /mcp`: MCP Protocol endpoint

## Core Components

### LLM Integration

Unified interface for major LLM providers:

```go
// OpenAI
llmProvider, _ := llm.OpenAIClient("sk-...", "gpt-4o")

// DeepSeek
llmProvider, _ := llm.QuickDeepSeekProvider("sk-...", "deepseek-chat")

// Volcengine
llmProvider, _ := llm.VolceClient("ak-...", "doubao-pro-32k")
```

### Triggers

Expose your agent via different protocols.

#### HTTP Trigger
Standard RESTful API for chat and streaming.

#### MCP Trigger
Fully compliant with the [Model Context Protocol (MCP)](https://modelcontextprotocol.io/), allowing your agent to serve as a tool for MCP clients (e.g., Claude Desktop).

## Tools

Extensive built-in tool library:

- **MCP Tools**: Connect to any MCP Server.
- **File Operations**: Safe filesystem access.
- **SSH**: Remote server management.
- **Email**: Send HTML/Text emails.
- **Math**: Complex calculations.
- **System Command**: Secure shell execution.

### Custom Tools

Implement the `types.Tool` interface to extend capabilities:

```go
type MyTool struct{}

func (t *MyTool) Name() string { return "my_tool" }
func (t *MyTool) Description() string { return "A custom tool" }
func (t *MyTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
    // Business logic...
    return "Result", nil
}
```

## Agent Skills

Cortex implements a unique **filesystem-based skill system** that allows you to teach the agent new capabilities without recompiling code.

### How it Works

1.  **Define**: Create a `SKILL.md` file in a directory (e.g., `./skills/my-skill/SKILL.md`).
2.  **Describe**: Use Markdown to describe the task and provide executable examples (e.g., `curl` commands, SQL queries).
3.  **Discover**: Cortex automatically scans the skills directory and injects available skills into the system prompt.
4.  **Execute**: When the agent needs to perform a task, it follows the instructions in your `SKILL.md`.

### Example Skill (`skills/weather/SKILL.md`)

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

This approach allows you to leverage any CLI tool, API, or script as a first-class agent capability.

## Task Scheduling System

Cortex features a powerful built-in task scheduling system (`xcron`), allowing agents to autonomously manage scheduled tasks. This empowers agents to handle not just immediate requests, but also periodic jobs or delayed tasks.

### Key Features

- **Flexible Scheduling Modes**:
  - `oneshot`: Execute once after a delay (e.g., "Remind me to drink water in 10 minutes").
  - `periodic`: Execute at regular intervals (e.g., "Check server status every 2 hours").
  - `cron`: Precise timing using Cron expressions (e.g., "Send a daily report at 8:00 AM").
- **Persistence**: Tasks are persisted, ensuring they survive service restarts.
- **Agent-Driven**: Agents can autonomously create, query, and manage tasks using built-in tools (`schedule_job`, `list_jobs`, `delete_job`).

### Example Scenario

User: "Summarize the top Hacker News stories from yesterday every morning at 9 AM."

The Agent automatically invokes the `schedule_job` tool:
```json
{
  "name": "hn_daily_summary",
  "type": "cron",
  "schedule": "0 0 9 * * *",
  "payload": "Summarize yesterday's top Hacker News stories and send them to me.",
  "task_type": "agent_task"
}
```

## Memory System

Cortex features an advanced **Hybrid Memory Architecture** designed for long-running conversations.

### Key Features

1.  **Raw History**: Preserves every interaction for complete auditability.
2.  **Rolling Summary**: Asynchronously generates concise summaries of past conversations.
3.  **Smart Retrieval**: Dynamically constructs prompts using "Summary + Recent Context" to maximize information density within token limits.
4.  **Async Processing**: Summary generation happens in the background with automatic panic recovery, ensuring zero latency impact on user interactions.

### Storage Backends

Switch storage with a single line of config:

- **Memory (Default)**: Ephemeral, for testing.
- **Redis**: High-performance KV store (Recommended for production).
- **MongoDB**: Flexible document store.
- **MySQL / SQLite**: Relational database support.

```go
// Example: Redis Memory
redisClient := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
memory := providers.NewRedisMemoryProvider(redisClient, "session-id")
agentEngine.SetMemory(context.Background(), memory)
```

## Examples

- [Basic Example](examples/basic): Fundamental usage patterns.
- [Chat Web](examples/chat-web): Full-stack chat application (Gin + React).
- [MCP Server](examples/mcp-server): Expose Agent as an MCP service.
- [Agent Skills](examples/skills): Dynamic skill loading and usage.
- [Task Scheduling](examples/xcron): Task scheduling with xcron.

## Contributing

Issues and Pull Requests are welcome!

## License

MIT License
