package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/llm"
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/skills"
	"github.com/xichan96/cortex/agent/tools/builtin"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"
)

// getLLMProvider creates an LLM provider (using Volcengine configuration)
func getLLMProvider() (types.LLMProvider, error) {
	// Use Volcengine client
	// If model is empty, llm.NewVolceClient will use the default (DoubaoSeed1)
	// If baseURL is empty, llm.NewVolceClient will use the default
	opts := llm.VolceOptions{
		APIKey: "",
		Model:  "deepseek-v3-1-250821",
	}

	llmProvider, err := llm.NewVolceClient(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create Volcengine client: %w", err)
	}
	return llmProvider, nil
}

// loadAndInjectSkills handles skill loading and system prompt injection
func loadAndInjectSkills(ctx context.Context, agentConfig *types.AgentConfig) ([]skills.Skill, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current working directory: %w", err)
	}

	// Try to find the skills directory
	skillsDir := filepath.Join(cwd, "examples", "skills", "skills")
	if _, err := os.Stat(skillsDir); os.IsNotExist(err) {
		skillsDir = filepath.Join(cwd, "skills")
	}

	fmt.Printf("Loading skills from: %s\n", skillsDir)

	loadedSkills, err := skills.LoadSkillsFromDirs(ctx, logger.GetLogger(), []string{skillsDir})
	if err != nil {
		return nil, fmt.Errorf("failed to load skills: %w", err)
	}

	fmt.Printf("Successfully loaded %d skills.\n", len(loadedSkills))
	for _, s := range loadedSkills {
		fmt.Printf(" - Loaded Skill: %s (Path: %s)\n", s.Name, s.Path)
	}

	// Inject skills into system prompt (Lazy Load pattern)
	skillsPrompt := skills.BuildSystemPromptInjection(loadedSkills)
	if skillsPrompt != "" {
		fmt.Println("\n[System] Injecting skills prompt...")
		agentConfig.SystemMessage += skillsPrompt
	}

	return loadedSkills, nil
}

func main() {
	fmt.Println("=== Agent Skills Example ===")

	// 1. Initialize LLM Provider
	llmProvider, err := getLLMProvider()
	if err != nil {
		log.Fatalf("Error initializing LLM: %v", err)
	}

	// 2. Initialize Agent Config (matching basic/main.go structure)
	agentConfig := types.NewAgentConfig()
	agentConfig.SystemMessage = "You are a helpful AI assistant."

	// Advanced parameters
	agentConfig.MaxIterations = 10
	agentConfig.Temperature = 0.7
	agentConfig.MaxTokens = 2048
	agentConfig.TopP = 0.9
	agentConfig.FrequencyPenalty = 0.1
	agentConfig.PresencePenalty = 0.1
	agentConfig.Timeout = 60 * time.Second
	agentConfig.RetryAttempts = 3
	agentConfig.EnableToolRetry = true
	agentConfig.LogSilent = true // Enable silent mode for logger

	ctx := context.Background()
	// 3. Load Skills and update System Prompt
	loadedSkills, err := loadAndInjectSkills(ctx, agentConfig)
	if err != nil {
		log.Fatalf("Skill loading error: %v", err)
	}

	// 3.1 Register skills for Planner
	registry := skills.NewRegistry()
	for i := range loadedSkills {
		registry.Register(&loadedSkills[i])
	}
	planner := skills.NewPlanner(registry)

	// 4. Create Agent Engine
	agentEngine := engine.NewAgentEngine(llmProvider, agentConfig)

	// 5. Initialize Memory (Explicitly using SimpleMemoryProvider)
	memory := providers.NewSimpleMemoryProvider()
	agentEngine.SetMemory(ctx, memory)

	// 6. Register Essential Tools
	// The agent needs 'read_file' to read skill definitions, and 'command' to execute skill instructions.
	agentEngine.AddTool(ctx, builtin.NewFileTool())
	agentEngine.AddTool(ctx, builtin.NewCommandTool())

	fmt.Println("\n=== Final System Prompt ===")
	fmt.Println(agentConfig.SystemMessage)
	fmt.Println("===========================")

	// 7. Interactive Loop
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("\nChat with the agent (type 'quit' or 'exit' to stop):")

	for {
		fmt.Print("\nUser: ")
		userInput, _ := reader.ReadString('\n')
		userInput = strings.TrimSpace(userInput)

		if userInput == "quit" || userInput == "exit" {
			break
		}
		if userInput == "" {
			continue
		}

		// Check for skills execution plan
		plan, err := planner.Plan(ctx, userInput)
		if err == nil && len(plan.Steps) > 0 {
			fmt.Println("\n[Planner] Identified skills execution plan:")
			var skillsContext strings.Builder
			skillsContext.WriteString("Based on your request, the following skills have been identified. You MUST use their instructions to fulfill the request:\n\n")

			for i, step := range plan.Steps {
				skillNames := make([]string, len(step.Skills))
				for j, s := range step.Skills {
					skillNames[j] = s.Name
					// Append skill content to context
					skillsContext.WriteString(fmt.Sprintf("## Skill: %s\n%s\n\n", s.Name, s.Content))
				}
				fmt.Printf("  Step %d (%s): Executing %s\n", i+1, step.Mode, strings.Join(skillNames, ", "))
			}

			// Update userInput with context to fast-track execution
			userInput = skillsContext.String() + "User Request: " + userInput
			fmt.Println("[Planner] Injecting skill context and executing...")
		}

		fmt.Printf("Assistant: ")

		// Use streaming execution
		stream, err := agentEngine.ExecuteStream(ctx, userInput, nil) // passing nil for tool_choice
		if err != nil {
			log.Printf("Execution error: %v", err)
			continue
		}

		var finalResult *engine.AgentResult

		for result := range stream {
			switch result.Type {
			case "chunk":
				fmt.Print(result.Content)
			case "error":
				log.Printf("\nStreaming error: %v", result.Error)
			case "end":
				finalResult = result.Result
			}
		}
		fmt.Println() // Newline after stream

		// Display tool calls if any occurred
		if finalResult != nil && len(finalResult.ToolCalls) > 0 {
			fmt.Println("\n--- Tool Call Details ---")
			for i, toolCall := range finalResult.ToolCalls {
				fmt.Printf("Tool Call %d:\n", i+1)
				fmt.Printf("  Tool: %s\n", toolCall.Tool)
				fmt.Printf("  Input: %v\n", toolCall.ToolInput)
			}
			fmt.Println("-------------------------")
		}
	}

	fmt.Println("Goodbye!")
}
