# Dino - Cortex Advanced Orchestration
  
<p align="center">
  <strong>English</strong> | <a href="README-CN.md">简体中文</a>
</p>

Dino is the advanced orchestration component of the [Cortex](https://github.com/xichan96/cortex) framework. It provides enterprise-grade session management, resource budgeting, loop detection, and tool orchestration capabilities for building production-ready AI agents.

---

## Overview

Dino extends the core Cortex agent capabilities with advanced features designed for production environments:

- **Multi-Session Management**: Handle multiple concurrent user sessions with isolated context
- **Resource Budgeting**: Prevent runaway costs with token, tool call, and time limits
- **Loop Detection**: Automatically detect and prevent repetitive action loops
- **Tool Approval Workflows**: Require human approval for dangerous operations
- **Task Queue**: Built-in priority queue for batch processing
- **Planner Mode**: Break down complex tasks into executable plans

---

## Features

### Session Management
- Channel-based input/output for non-blocking communication
- Observer pattern for real-time event subscription
- Automatic session cleanup and resource management

### Budget Control
- Configurable token limits per session
- Tool call count restrictions
- Time-based budget enforcement

### Loop Detection
- Semantic similarity detection for action patterns
- Configurable threshold and max repeat counts
- Automatic loop prevention with user notifications

### Tool System
- Built-in tools: `read_file`, `write_file`, `edit_file`, `bash`, `glob`, `grep`, `list_directory`
- Extensible tool registry
- Dangerous tool approval workflows
- MCP (Model Context Protocol) support

### Planner Mode
- Step-by-step plan generation before execution
- Auto-approval option for trusted workflows
- Plan visualization through events

### Long-running tasks & harness (`dino/task`, `dino/runner`)
- **`dino/task`**: neutral types (`Task`, `TaskConfig`, `StopReason`, checkpoint `TaskSession`, `SessionSwapper`, …)
- **`dino/runner`**: `DefaultTaskEngine` wired to `dino/harness` (verify, stall, multi-outer iterations), shell/text/LLM verifiers, in-memory and blob-backed `SessionStore` implementations

### Chat history vs structured memory (naming)
- **`dino/chatstore`**: per-session **chat transcript** `Provider` for the factory (`OpenSharedChatStore`, SQLite / in-memory backends)
- **`pkg/memkit`**: **preferences, knowledge, index, PageIndex** — structured memory; complements `chatstore`, different role

### Embedding Dino in a host app (`hostconfig`, `mem`, `pkg`)
- **`dino/hostconfig`**: **mapstructure-friendly** types for a typical host YAML (`HostAppConfig`, cortex/tool/MCP slices), **`CoalesceDinoFromHost`** (merge into `dino.Config`), **`ExpandConfigPath`**, MCP entries → `dino.MCP` (`MergeMCPServersIntoDino`), and **`ListMCPTools` / `ListMCPToolsFromToolConfigs`** for capability discovery. No goclaw import.
- **`dino/mem`**: optional **memory ingest loop** and **memory tool** wiring over shared chat SQLite + `memkit`; host supplies LLM factory, paths, and logging via options.
- **`dino/pkg`**: small **non-config** helpers only, e.g. **user-echo skip** (`MarkPendingUserEcho` / `TakePendingUserEchoMatch`) and **scheduler tools bound to a chat session id**.

---

## Installation

```bash
go get github.com/xichan96/cortex/dino
```

---

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/xichan96/cortex/dino"
)

func main() {
    // 1. Create configuration
    cfg := dino.DefaultConfig()
    cfg.Provider.APIKey = "your-api-key"
    cfg.WorkspaceRoot = "/path/to/workspace"

    // 2. Create factory
    factory, err := dino.NewDinoFactory(cfg)
    if err != nil {
        log.Fatal(err)
    }
    defer factory.Shutdown(context.Background())

    // 3. Create client
    client := dino.NewClient(factory)

    // 4. Create session
    session, err := client.CreateSession(context.Background(), "session-1")
    if err != nil {
        log.Fatal(err)
    }

    // 5. Send message and get response
    ctx := context.Background()
    event, err := session.SendAndWait(ctx, "List files in the current directory")
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println("Response:", event.Content)
}
```

---

## Configuration

```yaml
# dino.yaml
default_model: gpt-4o-mini
default_provider: openai
temperature: 0.7
max_tokens: 4096
timeout: 30s
max_iterations: 10

workspace_root: "."

# Tool configuration
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

# Loop detection
loop_detection:
  enabled: true
  max_repeats: 3
  similarity_threshold: 0.8

# Budget control
budget:
  enabled: true
  max_tokens: 100000
  max_tool_calls: 50
  max_time_ms: 300000

# Planner mode
planner_mode:
  enabled: false
  prompt_plan: "First, analyze the task and create a step-by-step plan."
  auto_approve: false
```

---

## Advanced Usage

### With Stream Events

```go
type StreamHandler struct{}

func (h *StreamHandler) SendStreamEvent(sessionID string, event interface{}) {
    // Handle stream events
}

factory, _ := dino.NewDinoFactory(cfg, dino.WithStreamEventSender(handler))
```

### With Task Queue

```go
import "github.com/xichan96/cortex/dino/queue"

session, _ := client.CreateSession(ctx, "queue-session", dino.WithQueueEnabled(100, 10))
resultChan, _ := session.Enqueue("task 1", queue.PriorityNormal)
result := <-resultChan
```

### With Event Observers

```go
session.SubscribeFunc(func(event *dino.Event) {
    switch event.Type {
    case dino.EventTypeMessage:
        fmt.Println("Message:", event.Content)
    case dino.EventTypeThinking:
        fmt.Println("Thinking:", event.Thinking)
    case dino.EventTypeToolCall:
        fmt.Println("Tool:", event.ToolName)
    case dino.EventTypeDone:
        fmt.Println("Done")
    }
})
```

---

## Core Types

| Type | Description |
|------|-------------|
| `Client` | Main entry point for session management |
| `Session` | Represents a single user conversation |
| `Config` | Configuration for Dino factory |
| `Event` | Real-time event during execution |
| `Skill` | Custom prompt template for specific tasks |
| `Budget` | Resource usage tracking and limits |

---

## Architecture

```
dino/
├── client.go        # Client and session management
├── factory.go       # Session factory, tool registry, budget
├── config.go        # Configuration types
├── defined_tool.go  # DefinedTool, ToolContext, ApprovalStore
├── types.go         # Core type definitions
├── bus.go           # Event bus
├── session/         # Session implementation
│   ├── session.go   # Core session logic
│   ├── event.go     # Event definitions
│   ├── info.go      # Session info
│   └── planner.go   # Planner helper
├── queue/           # Task queue
├── task/            # Long-task types (Task, StopReason, SessionStore)
├── runner/          # Outer-loop engine, verifiers, checkpoint stores
├── chatstore/       # Session chat transcript Provider (SQLite / in-memory)
├── hostconfig/      # Host YAML shapes + CoalesceDinoFromHost + MCP listing helpers
├── mem/             # Memory ingest loop + memory tools (host-injected deps)
├── pkg/             # Echo-skip + scheduler session tool wrappers
├── permission/      # Tool permission
├── agent/           # Subagent and prompts
└── tools/           # Registry, builtin, skill, MCP
```

---

## License

MIT License - see [LICENSE](../LICENSE) for details.
