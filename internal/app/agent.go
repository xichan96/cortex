package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jinzhu/copier"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/skills"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/internal/config"
	"github.com/xichan96/cortex/pkg/cache"
	"github.com/xichan96/cortex/pkg/logger"
	"github.com/xichan96/cortex/trigger/http"
	"github.com/xichan96/cortex/trigger/mcp"
)

type Agent interface {
	// build agent
	setupLLM(ctx context.Context) (types.LLMProvider, error)
	setupMemory(ctx context.Context, sessionID string) types.MemoryProvider
	setupTools(ctx context.Context) ([]types.Tool, error)
	build(ctx context.Context, sessionID string) (*engine.AgentEngine, error)
	Engine(ctx context.Context, sessionID string) (*engine.AgentEngine, error)

	// trigger methods
	HttpTrigger(ctx context.Context) http.Handler
	McpTrigger(ctx context.Context) (mcp.Handler, error)
}

type agent struct {
	config *config.Config
	logger *logger.Logger
}

func NewAgent() Agent {
	return &agent{
		config: config.Get(),
		logger: logger.NewLogger(),
	}
}

func (a *agent) build(ctx context.Context, sessionID string) (*engine.AgentEngine, error) {
	llmProvider, err := a.setupLLM(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to setup LLM: %w", err)
	}
	if llmProvider == nil {
		return nil, fmt.Errorf("LLM provider is nil")
	}

	memoryProvider := a.setupMemory(ctx, sessionID)

	tools, err := a.setupTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to setup tools: %w", err)
	}

	for _, tool := range tools {
		a.logger.Info("Tool added", slog.String("tool", tool.Name()))
	}

	agentConfig := types.NewAgentConfig()
	// Fix: copier.Copy arguments are (to, from)
	if err := copier.Copy(agentConfig, &a.config.Agent); err != nil {
		return nil, fmt.Errorf("failed to copy agent config: %w", err)
	}

	// Load skills if configured
	if len(a.config.Skills.Paths) > 0 {
		loadedSkills, err := skills.LoadSkillsFromDirs(ctx, a.logger, a.config.Skills.Paths)
		if err != nil {
			return nil, fmt.Errorf("failed to load skills: %w", err)
		}
		skillsPrompt := skills.BuildSystemPromptInjection(loadedSkills)
		if skillsPrompt != "" {
			agentConfig.SystemMessage += "\n" + skillsPrompt
			a.logger.Info("Skills loaded and injected into system prompt", slog.Int("count", len(loadedSkills)))
		}
	}

	if a.config.Agent.Timeout != "" {
		timeout, err := time.ParseDuration(a.config.Agent.Timeout)
		if err != nil {
			return nil, fmt.Errorf("failed to parse timeout: %w", err)
		}
		agentConfig.Timeout = timeout
	}

	engine := engine.NewAgentEngine(llmProvider, agentConfig)
	engine.SetMemory(ctx, memoryProvider)
	engine.AddTools(ctx, tools)
	return engine, nil
}

func (a *agent) Engine(ctx context.Context, sessionID string) (*engine.AgentEngine, error) {
	var v interface{}
	if err := cache.Local.Get(sessionID, &v); err == nil {
		if eng, ok := v.(*engine.AgentEngine); ok {
			return eng, nil
		}
	}

	agentEngine, err := a.build(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	cache.Local.Set(sessionID, agentEngine, 10*time.Minute)
	return agentEngine, nil
}

func (a *agent) HttpTrigger(ctx context.Context) http.Handler {
	return http.NewHandler()
}

func (a *agent) McpTrigger(ctx context.Context) (mcp.Handler, error) {
	engine, err := a.Engine(ctx, uuid.New().String())
	if err != nil {
		return nil, err
	}
	mcpHandler, err := mcp.NewHandler(engine, mcp.Options{
		Server: mcp.Metadata{
			Name:    a.config.Agent.MCP.Server.Name,
			Version: a.config.Agent.MCP.Server.Version,
		},
		Tool: mcp.Metadata{
			Name:        a.config.Agent.MCP.Tool.Name,
			Description: a.config.Agent.MCP.Tool.Description,
		},
	})
	if err != nil {
		return nil, err
	}
	return mcpHandler, nil
}
