# Basic Example: AI Agent with MCP Integration

This example demonstrates how to create an AI agent using the Cortex framework that connects to an AI training service via MCP (Model Control Protocol).

## Overview

The basic example showcases the core functionality of the Cortex framework, including:

- Creating an LLM provider
- Initializing an MCP client to connect to an AI training service
- Configuring the agent with various parameters
- Creating and executing the agent engine
- Using tools provided by the MCP service

## Prerequisites

- Go 1.18 or later
- Access to an LLM provider (OpenAI or compatible)
- Access to an AI training service with MCP endpoint

## Installation

1. Clone the repository:

```bash
git clone https://github.com/xichan96/cortex.git
cd cortex
```

2. Install dependencies:

```bash
go mod download
```

## Configuration

The example requires the following configuration parameters, which are currently hardcoded in `main.go`:

### LLM Configuration
- `apiKey`: Your LLM provider API key
- `baseURL`: The base URL for your LLM provider
- `model`: The model to use (e.g., "gpt-4.1")

### MCP Configuration
- `mcpURL`: The MCP endpoint URL (e.g., "https://ai.cn/api/train/mcp/sse")
- `transport`: The transport mode ("http" or "sse")
- `headers`: HTTP headers for the MCP connection

## Usage

1. Update the configuration parameters in `main.go`:

```go
// LLM configuration
apiKey := "your-api-key"
baseURL := "https://api.example.com"
model := "gpt-4.1"

// MCP configuration
mcpURL := "https://ai.example.com/api/train/mcp/sse"
```

2. Run the example:

```bash
go run examples/basic/main.go
```

## Example Structure

The example consists of the following main components:

### 1. LLM Provider Setup

```go
func getLLMProvider() (types.LLMProvider, error) {
    // Configuration
    apiKey := ""
    baseURL := ""
    model := "gpt-4.1"

    // Create OpenAI client
    llmProvider, err := llm.OpenAIClientWithBaseURL(apiKey, baseURL, model)
    if err != nil {
        return nil, fmt.Errorf("Failed to create OpenAI client: %w", err)
    }
    return llmProvider, nil
}
```

### 2. MCP Client Initialization

```go
func initMCPClient() (*mcp.Client, error) {
    // MCP configuration
    mcpURL := "https://ai.cn/api/train/mcp/sse"
    transport := "http"
    headers := map[string]string{
        "Content-Type": "application/json; charset=utf-8",
    }

    // Create and connect MCP client
    client := mcp.NewClient(mcpURL, transport, headers)
    ctx := context.Background()
    if err := client.Connect(ctx); err != nil {
        return nil, fmt.Errorf("Failed to connect to MCP server: %w", err)
    }

    return client, nil
}
```

### 3. Agent Configuration

```go
// Create agent configuration
agentConfig := types.NewAgentConfig()

// Basic parameters
agentConfig.MaxIterations = 5
agentConfig.ReturnIntermediateSteps = true
agentConfig.SystemMessage = "You are a task self-check assistant: xxx"

// Advanced parameters
agentConfig.Temperature = 0.7
agentConfig.MaxTokens = 2048
agentConfig.TopP = 0.9
agentConfig.FrequencyPenalty = 0.1
agentConfig.PresencePenalty = 0.1
agentConfig.Timeout = 30 * time.Second
agentConfig.RetryAttempts = 3
agentConfig.EnableToolRetry = true
agentConfig.ToolRetryAttempts = 2
agentConfig.ParallelToolCalls = true
agentConfig.ToolCallTimeout = 10 * time.Second
```

### 4. Agent Engine Creation and Execution

```go
// Create agent engine
agentEngine := engine.NewAgentEngine(llmProvider, agentConfig)

// Add MCP tools
mcpTools := mcpClient.GetTools()
if len(mcpTools) > 0 {
    agentEngine.AddTools(mcpTools)
}

// Execute agent (code continues in the full example)
```

## Key Features Demonstrated

1. **LLM Integration**: Shows how to connect to an LLM provider using the Cortex framework.

2. **MCP Client**: Demonstrates how to connect to an AI training service via MCP.

3. **Agent Configuration**: Illustrates the comprehensive configuration options available for the agent.

4. **Tool Integration**: Shows how to add tools from an MCP service to the agent.

5. **Error Handling**: Demonstrates proper error handling throughout the application.

## Understanding the Output

When running the example, you will see output similar to:

```
=== AI Training Service MCP Integration Test ===
Using custom API: https://api.example.com, Model: gpt-4.1
Connecting to AI training service MCP: https://ai.example.com/api/train/mcp/sse (transport: http)
Successfully connected to AI training service MCP
```

The actual output will depend on your specific configuration and the responses from the LLM provider and MCP service.

## Troubleshooting

### Common Issues

1. **API Key Errors**: Ensure your LLM provider API key is valid and has the necessary permissions.

2. **Connection Issues**: Verify that the MCP endpoint URL is correct and accessible from your network.

3. **Configuration Errors**: Double-check all configuration parameters in `main.go`.

### Debugging

- Add more logging statements to track the flow of execution.
- Check the response from the LLM provider and MCP service.
- Verify that the MCP client successfully connects and retrieves tools.

## Next Steps

After running this basic example, you can explore more advanced features of the Cortex framework:

1. Create custom tools by implementing the `types.Tool` interface.
2. Configure memory management for conversation history.
3. Explore the streaming capabilities for real-time interactions.
4. Try the chat web example for a browser-based interface.

## License

This example is part of the Cortex framework and is licensed under the MIT License.
