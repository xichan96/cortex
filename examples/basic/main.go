package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/llm"
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/middle/redis"
	// "github.com/xichan96/cortex/pkg/mongodb"
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

// initRedisMemory initializes Redis client and creates Redis memory provider
func initRedisMemory(sessionID string) (types.MemoryProvider, error) {
	// Redis configuration
	redisConfig := &redis.Config{
		Host:     "localhost",
		Port:     6379,
		DB:       0,
		Password: "",
		Username: "",
	}

	fmt.Printf("Connecting to Redis: %s:%d, DB: %d\n", redisConfig.Host, redisConfig.Port, redisConfig.DB)

	// Create Redis client
	redisClient, err := redis.NewClient(redisConfig)
	if err != nil {
		return nil, fmt.Errorf("Failed to create Redis client: %w", err)
	}

	fmt.Printf("Successfully connected to Redis, Session ID: %s\n", sessionID)

	// Create Redis memory provider with limit
	memory := providers.NewRedisMemoryProviderWithLimit(redisClient, sessionID, 100)
	return memory, nil
}

// initMongoDBMemory initializes MongoDB client and creates MongoDB memory provider
// func initMongoDBMemory(sessionID string) (types.MemoryProvider, error) {
// 	// MongoDB configuration
// 	mongoURI := "mongodb://localhost:27017"
// 	database := "cortex"
// 	username := "cortex"
// 	password := "cortex"
//
// 	fmt.Printf("Connecting to MongoDB: %s, Database: %s\n", mongoURI, database)
//
// 	// Create MongoDB client
// 	mongoClient, err := mongodb.NewClient(
// 		mongodb.SetURI(mongoURI),
// 		mongodb.SetDatabase(database),
// 		mongodb.SetBasicAuth(username, password),
// 	)
// 	if err != nil {
// 		return nil, fmt.Errorf("Failed to create MongoDB client: %w", err)
// 	}
//
// 	fmt.Printf("Successfully connected to MongoDB, Session ID: %s\n", sessionID)
//
// 	// Create MongoDB memory provider with limit
// 	memory := providers.NewMongoDBMemoryProviderWithLimit(mongoClient, sessionID, 100)
// 	return memory, nil
// }

func main() {
	fmt.Println("=== AI training service MCP integration test ===")

	// Get LLM provider
	llmProvider, err := getLLMProvider()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

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
	// agentConfig.EnableToolRetry = true     // Enable tool retry

	ctx := context.Background()
	// Create agent engine
	agentEngine := engine.NewAgentEngine(llmProvider, agentConfig)

	// Initialize Redis memory
	memory := providers.NewSimpleMemoryProvider()
	// sessionID := fmt.Sprintf("session_%d", time.Now().Unix())
	// memory, err := initRedisMemory(sessionID)
	// if err != nil {
	// 	fmt.Printf("Redis memory initialization error: %v, falling back to simple memory\n", err)
	// 	memory = providers.NewSimpleMemoryProvider()
	// }
	agentEngine.SetMemory(ctx, memory)

	// Initialize MongoDB memory (commented out)
	// sessionID := fmt.Sprintf("session_%d", time.Now().Unix())
	// memory, err := initMongoDBMemory(sessionID)
	// if err != nil {
	// 	fmt.Printf("MongoDB memory initialization error: %v, falling back to simple memory\n", err)
	// 	memory = providers.NewSimpleMemoryProvider()
	// }
	// agentEngine.SetMemory(ctx, memory)

	fmt.Printf("Agent created with 0 tools\n")
	fmt.Printf("Agent configuration: Temperature=%.1f, MaxTokens=%d, Timeout=%v\n",
		agentConfig.Temperature, agentConfig.MaxTokens, agentConfig.Timeout)

	// Test basic chat (may use tools)
	fmt.Println("\n--- Basic Chat Test (Integrated with AI Training Tools) ---")
	testQuery := "what is the image about?"
	// imageURL := "https://screenshot.jpeg"

	// Base64 image example (1x1 red pixel)
	base64Image := ""
	imageData, _ := base64.StdEncoding.DecodeString(base64Image)

	// Using streaming execution
	fmt.Printf("User: %s (with base64 image)\n", testQuery)
	fmt.Printf("Assistant: ")

	// Create multi-modal input
	input := types.NewAgentInputWithParts([]types.MessagePart{
		types.TextPart{Text: testQuery},
		// types.ImageURLPart{URL: imageURL},
		types.ImageDataPart{
			Data:     imageData,
			MIMEType: "image/png",
		},
	})

	stream, err := agentEngine.ExecuteStream(ctx, input, nil)
	if err != nil {
		log.Printf("Agent streaming execution error: %v", err)
		return
	}

	var finalResult *types.AgentResult
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
