package main

import (
	"context"
	"log"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/llm"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/mcp"
)

// Custom Agent Provider implementation
type MyAgentProvider struct {
	agentEngine *engine.AgentEngine
	mcpClient   *mcp.Client
}

// GetAgentEngine gets the agent engine
func (p *MyAgentProvider) GetAgentEngine() *engine.AgentEngine {
	return p.agentEngine
}

// GetMCPClient gets the MCP client
func (p *MyAgentProvider) GetMCPClient() *mcp.Client {
	return p.mcpClient
}

// createAgentEngine creates an agent engine
func createAgentEngine() (*engine.AgentEngine, error) {
	// LLM configuration
	llmAPIKey := ""
	llmBaseURL := "https://xxx.cn"
	llmModel := "gpt-4.1"

	// Create LLM provider
	llmProvider, err := llm.OpenAIClientWithBaseURL(
		llmAPIKey,
		llmBaseURL,
		llmModel,
	)
	if err != nil {
		return nil, err
	}

	// Create agent configuration
	agentConfig := types.NewAgentConfig()
	agentConfig.MaxIterations = 5
	agentConfig.SystemMessage = "You are a task self-check assistant: Users may send you task_id or links. You need to: 1. Collect information as needed. If previous information is insufficient to analyze the problem, continue collecting (task details, logs, workflow, task monitoring statistics, etc.) 2. Comprehensive analysis of problems or evaluation of resource utilization of training tasks based on the collected information"

	// Set advanced parameters
	agentConfig.Temperature = 0.7
	agentConfig.MaxTokens = 2048
	agentConfig.TopP = 0.9
	agentConfig.FrequencyPenalty = 0.1
	agentConfig.PresencePenalty = 0.1
	agentConfig.Timeout = 30 * time.Second
	agentConfig.RetryAttempts = 3
	agentConfig.EnableToolRetry = true

	// Create agent engine
	agentEngine := engine.NewAgentEngine(llmProvider, agentConfig)

	return agentEngine, nil
}

// createMCPClient creates an MCP client
func createMCPClient(agentEngine *engine.AgentEngine) (*mcp.Client, error) {
	// MCP configuration
	mcpURL := "https://xxx.cn/api/train/mcp/sse"
	mcpTransport := "http"
	mcpHeaders := map[string]string{
		"Content-Type": "application/json; charset=utf-8",
	}

	// Create MCP client
	mcpClient := mcp.NewClient(
		mcpURL,
		mcpTransport,
		mcpHeaders,
	)

	// Connect to MCP server
	ctx := context.Background()
	if err := mcpClient.Connect(ctx); err != nil {
		return nil, err
	}

	// Get MCP tools and add to agent engine
	mcpTools := mcpClient.GetTools()
	if len(mcpTools) > 0 {
		log.Printf("Found %d AI training tools, adding to agent engine...", len(mcpTools))
		agentEngine.AddTools(mcpTools)
	}

	return mcpClient, nil
}
