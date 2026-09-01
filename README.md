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
  · <a href="#dino">Dino</a>
  · <a href="#tools">Tools</a>
  · <a href="#agent-skills">Agent Skills</a>
  · <a href="#memory-system">Memory System</a>
  · <a href="#examples">Examples</a>
  · <a href="#triggers">Triggers</a>
  · <a href="#license">License</a>
</p>

<p align="center">
  English | <a href="README-CN.md">简体中文</a>
</p>

## Overview

By bridging the simplicity of a lightweight framework with the robustness of Go, CORTEX enables seamless integration with leading LLMs. It provides a comprehensive toolkit for building AI agents capable of complex tool execution and reasoning.

Designed for production environments, CORTEX prioritizes reliability, configurability, and resource efficiency. It empowers developers to build next-generation AI applications with the performance and safety guarantees inherent to the Go ecosystem.

**Design Philosophy**: CORTEX adopts a minimalist approach, focusing on streamlined integration and low resource footprint. It eliminates heavy dependencies and complex orchestration overhead, making it the ideal choice for developers who need powerful Agent capabilities without the bloat of a full-fledged workflow automation platform.

**`agent` vs Dino**: **`agent`** (`github.com/xichan96/cortex/agent`) is the baseline—when you do **not** need complex multi-turn chat with the model **autonomously deciding and chaining tools**, integrate it directly into your project: one-shot report generation, summarization, extraction, classification, structured output, etc., using `engine` + `llm` and optional tools (no Dino required). **`dino`** (`github.com/xichan96/cortex/dino`) is the advanced stack for **intelligent agents** (assistants, IDE agents, bots): multi-turn dialogue, automatic tool decisions and multi-step execution, plus session isolation, budgets, approvals, and observability.

## Features

- **Intelligent Agent Engine**: A robust core for building agents with advanced reasoning and tool-calling capabilities.
- **Broad LLM Support**: OpenAI and Anthropic native clients, plus any OpenAI-compatible endpoint (DeepSeek, Volcengine, ...) via BaseURL, and custom providers.
- **Multi-Modal Native**: Effortlessly process and generate text, images, and other media formats.
- **Dynamic Skills**: File-system-based skills with lazy loading; YAML frontmatter supports `paths`, `when_to_use`, and `allowed_tools`, with path-glob filtering (`doublestar`) and optional symlink resolve / real-path dedupe in the loader.
- **Extensible Tooling**: `agent/tools/builtin` spans fs, search, shell/background jobs, net/web, Docker, email, math, and `mcp_client`; register custom `types.Tool` as needed.
- **Real-Time Streaming**: Full support for response streaming, enabling interactive, low-latency user experiences.
- **Hybrid Memory Architecture**: Full history plus rolling summaries, with asynchronous compression (mutex-serialized, turn-aware via `CompactAfterTurns`) so compress work does not overlap. Prompt assembly can trim recent chat to a rough token budget (`MaxBudgetTokens`, `RemainPromptTokens`). Providers may expose cheap `StoredMessageCount` for smarter gating. Compatible with LangChain, MongoDB, Redis, MySQL, and SQLite.
- **Granular Configuration**: Extensive options to fine-tune agent behavior and performance.
- **Parallel Execution**: Efficiently execute multiple tool calls concurrently to minimize wait times.
- **Production-Grade Reliability**: Comprehensive error handling and retry mechanisms built for stability.
- **Dino production orchestration**: Multi-session isolation, real-time event subscription, token/tool/time budgets, loop detection, approval for risky tools, priority task queue, planner mode, and subagents; session snapshot save/restore (messages window, usage, memory turn counter); YAML memory tuning (`max_budget_tokens`, `compact_after_turns`) and optional subagent replay into parent memory; curated builtins plus MCP and skills integration.

## Architecture Overview

Cortex follows a modular architecture with the following key components:

> **Note**: The `agent` package is built on top of [LangChain](https://github.com/tmc/langchaingo), leveraging its powerful LLM interaction and tool-calling capabilities to build intelligent agent systems.

```
cortex/
├── agent/             # Core agent functionality
│   ├── engine/       # Agent engine implementation
│   ├── llm/          # LLM provider integrations
│   ├── skills/       # Skill loading and management (prompt/ templates)
│   ├── tools/        # Tool ecosystem (MCP, HTTP, builtins)
│   ├── types/        # Core type definitions
│   ├── providers/    # External service providers (memory, LLM adapters)
│   ├── hooks/        # Lifecycle hooks
│   └── utils/        # Ratelimit, loop detection, permission, budget
├── dino/             # Advanced orchestration (Client/Factory, budget, approval, queue, bus)
│   ├── agent/        # Subagent and prompts
│   ├── session/      # Session implementation and planner helpers
│   ├── memory/       # Memory management (e.g. SQLite)
│   ├── queue/        # Priority task queue
│   ├── tools/        # Registry, builtins, MCP, skill tool
│   └── permission/   # Tool permission and approval
├── trigger/          # Trigger modules
│   ├── http/         # HTTP trigger (REST API)
│   └── mcp/          # MCP trigger (MCP server)
└── examples/         # Example applications
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

### The `agent` package (baseline)

Without Dino, `agent` is the full baseline: LLM, engine, tools, and memory for one-shot or shallow calls where **you** own the request lifecycle—not a long-running loop where the model repeatedly picks tools. The LLM snippet below is part of that path.

### LLM Integration

Unified interface for major LLM providers:

```go
// OpenAI
llmProvider, _ := llm.OpenAIClient("sk-...", "gpt-4o")

// Anthropic
llmProvider, _ := llm.AnthropicClient("sk-...", "claude-sonnet-4-20250514")

// Other OpenAI-compatible endpoints (DeepSeek, Volcengine, ...): just set BaseURL
opts := llm.OpenAIOptions{APIKey: "sk-...", BaseURL: "https://api.deepseek.com", Model: "deepseek-chat"}
llmProvider, _ := llm.NewOpenAIClient(opts)
```

## Dino

<p align="center"><img src="docs/images/dino.png" alt="dino" width="120" /></p>

[Dino](dino/README.md) targets **intelligent agent** workloads: multi-turn dialogue, automatic tool choice and chained execution, long-lived runs. It reuses `agent` for execution while owning session lifecycle, observability, and guardrails.

### Role split

- **`agent`**: Baseline integration; single-request/batch jobs, one-shot report-style tasks, simple APIs; LLM providers, engine, skills, memory providers, hooks, builtins.
- **`dino`**: Intelligent-agent stack; multi-turn plus automatic tool choice and execution loops; `NewDinoFactory` / `NewClient` / `Session`, event bus (`bus`), `DefinedTool` and tool callbacks, per-tool and global timeouts, budgets, loop detection, allow/deny/approval lists, SQLite memory, subagents and manager, optional `queue` batching, and planner mode.

### Sessions and observability

- Isolated context per session; drive turns with `Send`, `SendAndWait`, etc.
- `Subscribe` / `SubscribeFunc` for `Message`, `Thinking`, `ToolCall`, `Done`, and related events—ideal for terminals, logging, and analytics.
- Factory options can attach a stream event sink for SSE/WebSocket bridges.

### Tools and safety

- `Config.Tools`: `Allowed`, `Denied`, `ApprovalRequired`; `ToolTimeouts` plus `ToolTimeoutCalculator` for dynamic limits.
- `defined_tool`: `DefinedTool`, `ToolContext`, and approval storage for human-in-the-loop risky operations.
- Builtins cover read/write/edit files, `glob`/`grep`, `bash`, `list_directory`, and more; wire MCP and filesystem skills via `cfg.Skills`.

### Resources and stability

- **Budget**: caps tokens, tool calls, and wall time.
- **Memory tuning (YAML / factory)**: `memory.max_budget_tokens`, `memory.compact_after_turns`; subagent history cap (default 48); optional `replay_to_parent_memory` for delegating work while recording truncated task/output in the parent session.
- **Session snapshots**: save and restore windowed messages, cumulative usage, and the engine memory-turn counter for durable sessions.
- **Loop detection**: semantic similarity and repeat counts to break infinite loops.
- **Planner mode**: optional plan-then-execute flow with auto-approve when trusted.

### Minimal usage

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
	ev, err := session.SendAndWait(context.Background(), "List this directory")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(ev.Content)
}
```

Full options, queue APIs, subagents, and event types: [dino/README.md](dino/README.md). Runnable sample: [examples/dino](examples/dino).

## Tools

Builtins live under `agent/tools/builtin/` by package; register `types.Tool` instances with the engine to enable them (defaults depend on your agent config).

| Package | Tool names |
|---------|------------|
| `fs/` | `read_file`, `write_file`, `edit_file`, `file` |
| `search/` | `glob`, `grep`, `codesearch` (placeholder) |
| `runtime/` | `command`, `question`, `job_kill`, `job_output` |
| `task/` | `todo` (in-process tasks: IDs, pending / in_progress / completed / cancelled, list & update) |
| `net/` | `ssh`, `net_check` |
| `web/` | `web_search`, `web_fetch` |
| `system/` | `get_time`, `http_request` |
| `email/` | `send_email` |
| `math/` | `math_calculate` |
| `docker/` | `docker_list_containers`, `docker_inspect_container`, `docker_container_logs`, `docker_exec`, `docker_create_container`, `docker_start_container`, `docker_stop_container`, `docker_restart_container`, `docker_remove_container`, `docker_pull_image` |
| `mcp/` | `mcp_client` |

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
2.  **Describe**: Use Markdown to describe the task and provide executable examples (e.g., `curl` commands, SQL queries). Optional YAML frontmatter can set `name`, `description`, `paths` (globs for when the skill applies), `when_to_use`, and `allowed_tools`; active-path filtering merges only matching skills into the system prompt.
3.  **Discover**: Cortex scans the skills directory (optional symlink resolution and dedupe by real path) and injects applicable skills.
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

1.  **Raw History**: Preserves every interaction for complete auditability (retrieval is windowed; full store depends on the backend).
2.  **Rolling Summary**: Asynchronously generates concise summaries of past conversations; compaction can be gated by turns (`CompactAfterTurns`) and optional stored message counts.
3.  **Smart Retrieval**: Builds prompts from summary plus recent messages; recent chat can be trimmed to a rough token budget before the LLM call (`MaxBudgetTokens`, `RemainPromptTokens`, heuristic token estimate in `agent/types`).
4.  **Async Processing**: Summary and compression run in the background with panic recovery and locking so user turns stay responsive.

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

- [Release summary](docs/RELEASE_SUMMARY.md): English notes on recent memory, skills, tools, and Dino changes.
- [Basic Example](examples/basic): Fundamental usage patterns.
- [Dino Example](examples/dino): Dino orchestration (sessions, budget, tool approval).
- [Chat Web](examples/chat-web): Full-stack chat application (Gin + React).
- [MCP Server](examples/mcp-server): Expose Agent as an MCP service.
- [Agent Skills](examples/skills): Dynamic skill loading and usage.
- [Task Scheduling](examples/xcron): Task scheduling with xcron.

## Triggers

`trigger/` exposes the agent over different protocols for external integration.

#### HTTP Trigger
Standard RESTful API for chat and streaming.

#### MCP Trigger
Compliant with the [Model Context Protocol (MCP)](https://modelcontextprotocol.io/), so the agent can be used as a tool from clients such as Claude Desktop.

## Contributing

Issues and Pull Requests are welcome!

## License

MIT License
