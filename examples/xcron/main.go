package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/llm"
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/skills"
	"github.com/xichan96/cortex/agent/tools/builtin/runtime"
	"github.com/xichan96/cortex/agent/tools/builtin/scheduler"
	"github.com/xichan96/cortex/agent/types"
	clogger "github.com/xichan96/cortex/pkg/logger"
	"github.com/xichan96/cortex/pkg/xcron"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// getLLMProvider creates an LLM provider (using Volcengine configuration)
func getLLMProvider() (types.LLMProvider, error) {
	// Use Volcengine client as in agent-skills example
	opts := llm.VolceOptions{
		APIKey: "", // Relies on environment variable or default
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

	// Try to find the skills directory (relative to project root)
	skillsDir := filepath.Join(cwd, "examples", "agent-skills", "skills")

	log.Info().Str("path", skillsDir).Msg("Loading skills")

	loadedSkills, err := skills.LoadSkillsFromDirs(ctx, clogger.GetLogger(), []string{skillsDir})
	if err != nil {
		return nil, fmt.Errorf("failed to load skills: %w", err)
	}

	log.Info().Int("count", len(loadedSkills)).Msg("Skills loaded")
	for _, s := range loadedSkills {
		log.Info().Str("skill", s.Name).Msg("Loaded Skill")
	}

	// Inject skills into system prompt
	skillsPrompt := skills.BuildSystemPromptInjection(loadedSkills)
	if skillsPrompt != "" {
		agentConfig.SystemMessage += skillsPrompt
	}

	return loadedSkills, nil
}

func main() {
	// 1. Setup logger
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	// 2. Setup xcron DB
	db, err := gorm.Open(sqlite.Open("xcron_agent_demo.db"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to open database")
	}
	if err := db.AutoMigrate(&xcron.Job{}); err != nil {
		log.Fatal().Err(err).Msg("Failed to migrate database")
	}

	// 3. Initialize Scheduler
	store := xcron.NewGormJobStore(db)
	sched := xcron.NewScheduler(store)

	// 4. Initialize Agent Components
	llmProvider, err := getLLMProvider()
	if err != nil {
		log.Fatal().Err(err).Msg("Error initializing LLM")
	}

	agentConfig := types.NewAgentConfig()
	agentConfig.SystemMessage = "You are a helpful AI assistant with scheduling capabilities. When asked to do something in the future, use the schedule_job tool."
	agentConfig.Timeout = 120 * time.Second // Increased timeout for long operations
	ctx := context.Background()
	// Load Skills (Weather, etc.)
	_, err = loadAndInjectSkills(ctx, agentConfig)
	if err != nil {
		log.Warn().Err(err).Msg("Failed to load skills (continuing without them)")
	}

	agentEngine := engine.NewAgentEngine(llmProvider, agentConfig)
	agentEngine.SetMemory(ctx, providers.NewSimpleMemoryProvider())

	// 5. Register Tools
	// - Command Tool (for Weather skill)
	agentEngine.AddTool(ctx, runtime.NewCommandTool())
	// - Scheduler Tools (for scheduling tasks)
	schedTools := scheduler.NewSchedulerTools(sched)
	for _, t := range schedTools {
		agentEngine.AddTool(ctx, t)
	}

	// 6. Register Agent Task Handler
	// This is where the magic happens: When the timer fires, xcron calls this function.
	// We use the Agent to execute the stored payload.
	scheduler.RegisterAgentTaskHandler(sched, func(input string) error {
		log.Info().Str("instruction", input).Msg("⏰ Future Task Triggered! Executing via Agent...")

		// Create a new context or use background
		// Note: executing agent might take time
		result, err := agentEngine.Execute(ctx, input, nil)
		if err != nil {
			log.Error().Err(err).Msg("❌ Agent execution failed")
			return err
		}

		log.Info().Str("output", result.Output).Msg("✅ Agent Execution Result")
		return nil
	})

	// 7. Start Scheduler
	sched.Start()
	log.Info().Msg("🚀 Scheduler started")
	defer sched.Stop()

	// 8. Execute the User Request
	userRequest := "2min schedule a task to query the weather in guangzhou"
	log.Info().Str("request", userRequest).Msg("👤 User Request")

	result, err := agentEngine.Execute(ctx, userRequest, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("Agent failed to process initial request")
	}
	log.Info().Str("response", result.Output).Msg("🤖 Agent Response")

	// 9. Keep running to wait for the scheduled task
	log.Info().Msg("⏳ Waiting for scheduled task to fire (Press Ctrl+C to exit)...")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Info().Msg("Shutting down...")
}
