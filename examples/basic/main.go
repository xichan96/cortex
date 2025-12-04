package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/llm"
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/tools/mcp"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/mongodb"
)

// getLLMProvider creates an LLM provider (using hardcoded configuration)
func getLLMProvider() (types.LLMProvider, error) {
	// Hardcoded configuration
	apiKey := ""
	baseURL := ""
	model := "gpt-4.1"

	fmt.Printf("Using custom API: %s, Model: %s\n", baseURL, model)
	llmProvider, err := llm.OpenAIClientWithBaseURL(apiKey, baseURL, model)
	if err != nil {
		return nil, fmt.Errorf("Failed to create OpenAI client: %w", err)
	}
	return llmProvider, nil
}

// initMCPClient initializes the MCP client and connects to the AI training service
func initMCPClient() (*mcp.Client, error) {
	// AI training service MCP configuration
	mcpURL := "https://ai.cn/api/train/mcp/sse"

	// Using HTTP transport mode (supports both HTTP streamable and SSE modes)
	transport := "http"
	headers := map[string]string{
		"Content-Type": "application/json; charset=utf-8",
	}

	fmt.Printf("Connecting to AI training service MCP: %s (transport: %s)\n", mcpURL, transport)

	// Create MCP client
	client := mcp.NewClient(mcpURL, transport, headers)

	// Connect to MCP server
	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("Failed to connect to MCP server: %w", err)
	}

	fmt.Println("Successfully connected to AI training service MCP")
	return client, nil
}

// initMongoDBMemory initializes MongoDB client and creates MongoDB memory provider
func initMongoDBMemory(sessionID string) (types.MemoryProvider, error) {
	// MongoDB configuration
	mongoURI := "mongodb://localhost:27017"
	database := "cortex"
	username := "cortex"
	password := "cortex"

	fmt.Printf("Connecting to MongoDB: %s, Database: %s\n", mongoURI, database)

	// Create MongoDB client
	mongoClient, err := mongodb.NewClient(
		mongodb.SetURI(mongoURI),
		mongodb.SetDatabase(database),
		mongodb.SetBasicAuth(username, password),
	)
	if err != nil {
		return nil, fmt.Errorf("Failed to create MongoDB client: %w", err)
	}

	fmt.Printf("Successfully connected to MongoDB, Session ID: %s\n", sessionID)

	// Create MongoDB memory provider with limit
	memory := providers.NewMongoDBMemoryProviderWithLimit(mongoClient, sessionID, 100)
	return memory, nil
}

func main() {
	fmt.Println("=== AI training service MCP integration test ===")

	// Get LLM provider
	llmProvider, err := getLLMProvider()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	// Initialize MCP client
	mcpClient, err := initMCPClient()
	if err != nil {
		fmt.Printf("MCP initialization error: %v\n", err)
		return
	}
	defer mcpClient.Disconnect(context.Background())

	// Create agent configuration - using new NewAgentConfig constructor
	agentConfig := types.NewAgentConfig()

	// Set basic parameters
	agentConfig.MaxIterations = 5
	agentConfig.SystemMessage = "You are a task self-check assistant: xxx"

	// Set advanced parameters (new feature)
	agentConfig.Temperature = 0.7          // Moderate creativity
	agentConfig.MaxTokens = 2048           // Limit response length
	agentConfig.TopP = 0.9                 // Top P sampling
	agentConfig.FrequencyPenalty = 0.1     // Frequency penalty
	agentConfig.PresencePenalty = 0.1      // Presence penalty
	agentConfig.Timeout = 30 * time.Second // Timeout duration
	agentConfig.RetryAttempts = 3          // Retry attempts
	agentConfig.EnableToolRetry = true     // Enable tool retry

	// Create agent engine
	agentEngine := engine.NewAgentEngine(llmProvider, agentConfig)

	// Initialize MongoDB memory
	// memory := providers.NewSimpleMemoryProvider()
	sessionID := fmt.Sprintf("session_%d", time.Now().Unix())
	memory, err := initMongoDBMemory(sessionID)
	if err != nil {
		fmt.Printf("MongoDB memory initialization error: %v, falling back to simple memory\n", err)
		memory = providers.NewSimpleMemoryProvider()
	}
	agentEngine.SetMemory(memory)

	// Get MCP tools and add to agent engine
	mcpTools := mcpClient.GetTools()
	if len(mcpTools) > 0 {
		fmt.Printf("Found %d AI training tools, adding to agent engine...\n", len(mcpTools))
		agentEngine.AddTools(mcpTools)

		// Show available tools
		fmt.Println("\n--- Available AI training tools ---")
		for _, tool := range mcpTools {
			fmt.Printf("- %s: %s\n", tool.Name(), tool.Description())
		}
	}

	fmt.Printf("Agent created with %d tools\n", len(mcpTools))
	fmt.Printf("Agent configuration: Temperature=%.1f, MaxTokens=%d, Timeout=%v\n",
		agentConfig.Temperature, agentConfig.MaxTokens, agentConfig.Timeout)

	// Test basic chat (may use tools)
	fmt.Println("\n--- Basic Chat Test (Integrated with AI Training Tools) ---")
	testQuery := "What is the reason for this task failure: https://xxx/034dc32bc01ae400/task/03957acd221a6d00"

	// Using streaming execution
	fmt.Printf("User: %s\n", testQuery)
	fmt.Printf("Assistant: ")

	stream, err := agentEngine.ExecuteStream(testQuery, nil)
	if err != nil {
		log.Printf("Agent streaming execution error: %v", err)
		return
	}

	var finalResult *engine.AgentResult
	var isFirstChunk = true
	for result := range stream {
		switch result.Type {
		case "chunk":
			// Output all content without filtering to observe complete streaming output
			content := result.Content
			if isFirstChunk {
				isFirstChunk = false
			}
			fmt.Printf("%s", content)
		case "error":
			log.Printf("Streaming execution error: %v", result.Error)
			return
		case "end":
			finalResult = result.Result
		}
	}
	fmt.Println() // New line

	// If there are tool calls, display detailed information
	if finalResult != nil && len(finalResult.ToolCalls) > 0 {
		fmt.Println("\n--- Tool Call Details ---")
		for i, toolCall := range finalResult.ToolCalls {
			fmt.Printf("Tool Call %d:\n", i+1)
			fmt.Printf("  Tool: %s\n", toolCall.Tool)
			fmt.Printf("  Input: %v\n", toolCall.ToolInput)
			fmt.Printf("  Call ID: %s\n", toolCall.ToolCallID)
		}
	}
}
