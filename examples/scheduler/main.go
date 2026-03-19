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
	"github.com/xichan96/cortex/agent/tools"
	"github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/tools/builtin/runtime"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"
	"github.com/xichan96/cortex/pkg/middle/sql/sqlite"
	"github.com/xichan96/cortex/pkg/xcron"
	"github.com/xichan96/cortex/scheduler"
)

func getLLMProvider() (types.LLMProvider, error) {
	apiKey := ""
	baseURL := ""
	model := "gpt-4.1"

	fmt.Printf("Using LLM API: %s, Model: %s\n", baseURL, model)
	llmProvider, err := llm.OpenAIClientWithBaseURL(apiKey, baseURL, model)
	if err != nil {
		return nil, fmt.Errorf("Failed to create OpenAI client: %w", err)
	}
	return llmProvider, nil
}

// loadAndInjectSkills handles skill loading and system prompt injection
func loadAndInjectSkills(ctx context.Context, agentConfig *types.AgentConfig) ([]skills.Skill, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current working directory: %w", err)
	}

	var skillDirs []string
	if envDir := os.Getenv("SKILLS_DIR"); envDir != "" {
		skillDirs = append(skillDirs, envDir)
	}
	skillDirs = append(skillDirs,
		filepath.Join(cwd, "examples", "scheduler", "skills"),
		filepath.Join(cwd, "examples", "skills", "skills"),
		filepath.Join(cwd, "skills"),
	)

	unique := make(map[string]struct{}, len(skillDirs))
	var existing []string
	for _, dir := range skillDirs {
		if dir == "" {
			continue
		}
		if _, ok := unique[dir]; ok {
			continue
		}
		unique[dir] = struct{}{}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			existing = append(existing, dir)
		}
	}
	if len(existing) == 0 {
		return nil, fmt.Errorf("no skills directory found")
	}

	fmt.Printf("Loading skills from: %s\n", strings.Join(existing, ", "))

	loadedSkills, err := skills.LoadSkillsFromDirs(ctx, logger.GetLogger(), existing)
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

	// 5.1 Initialize Persistence (SQLite)
	cwd, _ := os.Getwd()
	dbPath := filepath.Join(cwd, "scheduler.db")
	fmt.Printf("Using SQLite database: %s\n", dbPath)

	sqliteConfig := &sqlite.Config{
		Path: dbPath,
	}
	sqliteClient, err := sqlite.NewClient(sqliteConfig)
	if err != nil {
		log.Fatalf("Failed to initialize SQLite: %v", err)
	}

	// AutoMigrate
	if err := sqliteClient.DB.AutoMigrate(&xcron.Job{}); err != nil {
		log.Fatalf("Failed to migrate database: %v", err)
	}

	// 5.2 Initialize Scheduler Service with GormJobStore
	jobStore := xcron.NewGormJobStore(sqliteClient.DB)
	cronScheduler := xcron.NewScheduler(jobStore)
	if os.Getenv("SCHEDULER_SERIAL") == "1" {
		cronScheduler.SetMaxConcurrent(1)
	}
	schedulerService := scheduler.NewService(cronScheduler)

	// Configure Scheduler Service
	// We need a tool registry for the scheduler to use when executing agent tasks
	toolRegistry := tools.NewRegistry()
	// Register basic tools to the registry so scheduled agents can use them
	toolRegistry.Register(fs.NewFileTool(""))
	toolRegistry.Register(runtime.NewCommandTool(""))

	// Configure agent for scheduler with per-session memory (SQLite)
	memFactory := func(sessionID string) types.MemoryProvider {
		return providers.NewSQLiteMemoryProvider(sqliteClient, sessionID)
	}
	agentConfig.MaxIterations = 30
	schedulerService.ConfigureAgentWithMemoryFactory(llmProvider, agentConfig, memFactory, toolRegistry, registry)

	// Start the scheduler
	cronScheduler.Start()
	defer cronScheduler.Stop()

	// 6. Register Essential Tools
	// The agent needs 'read_file' to read skill definitions, and 'command' to execute skill instructions.
	agentEngine.AddTool(ctx, fs.NewFileTool(""))
	agentEngine.AddTool(ctx, runtime.NewCommandTool(""))

	// Register Scheduler Tools
	schedulerTools := scheduler.NewTools(schedulerService)
	agentEngine.AddTools(ctx, schedulerTools)

	// Inject session_id guidance for scheduling jobs (associate with this chat session)
	chatSessionID := fmt.Sprintf("chat-%d", time.Now().UnixNano())
	agentConfig.SystemMessage += fmt.Sprintf("\nWhen you schedule jobs using 'schedule_job', ALWAYS include session_id: '%s' to associate memory with this conversation. If needed, include max_iterations: 30.", chatSessionID)

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
		stream, err := agentEngine.ExecuteStream(ctx, types.NewAgentInput(userInput), nil) // passing nil for tool_choice
		if err != nil {
			log.Printf("Execution error: %v", err)
			continue
		}

		var finalResult *types.AgentResult

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

		// Display usage statistics
		if finalResult != nil {
			fmt.Printf("\n--- Token Usage ---\n")
			fmt.Printf("Prompt: %d, Completion: %d, Total: %d\n",
				finalResult.Usage.PromptTokens,
				finalResult.Usage.CompletionTokens,
				finalResult.Usage.TotalTokens)
			fmt.Println("-------------------")
		}

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

	// Display total usage statistics
	totalUsage := agentEngine.GetTotalUsage()
	fmt.Println("\n=== Final Token Usage ===")
	fmt.Printf("Total Prompt Tokens: %d\n", totalUsage.PromptTokens)
	fmt.Printf("Total Completion Tokens: %d\n", totalUsage.CompletionTokens)
	fmt.Printf("Total Tokens: %d\n", totalUsage.TotalTokens)
	fmt.Println("=========================")

	fmt.Println("Goodbye!")
}
