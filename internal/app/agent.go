package app

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jinzhu/copier"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/internal/config"
	"github.com/xichan96/cortex/pkg/cache"
	"github.com/xichan96/cortex/pkg/logger"
	"github.com/xichan96/cortex/trigger/http"
	"github.com/xichan96/cortex/trigger/mcp"
)

type Agent interface {
	// build agent
	setupLLM() (types.LLMProvider, error)
	setupMemory(sessionID string) types.MemoryProvider
	setupTools() ([]types.Tool, error)
	build(sessionID string) (*engine.AgentEngine, error)
	Engine(sessionID string) (*engine.AgentEngine, error)

	// trigger methods
	HttpTrigger() http.Handler
	McpTrigger() (mcp.Handler, error)
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

func (a *agent) build(sessionID string) (*engine.AgentEngine, error) {
	llmProvider, err := a.setupLLM()
	if err != nil {
		return nil, fmt.Errorf("failed to setup LLM: %w", err)
	}
	if llmProvider == nil {
		return nil, fmt.Errorf("LLM provider is nil")
	}

	memoryProvider := a.setupMemory(sessionID)
	tools, err := a.setupTools()
	if err != nil {
		return nil, fmt.Errorf("failed to setup tools: %w", err)
	}

	for _, tool := range tools {
		a.logger.Info("Tool added", slog.String("tool", tool.Name()))
	}

	agentConfig := types.NewAgentConfig()
	if err := copier.Copy(agentConfig, a.config.Agent); err != nil {
		return nil, fmt.Errorf("failed to copy agent config: %w", err)
	}

	if a.config.Agent.Timeout != "" {
		timeout, err := a.config.Agent.TimeoutDuration()
		if err != nil {
			return nil, fmt.Errorf("failed to parse timeout: %w", err)
		}
		agentConfig.Timeout = timeout
	}

	engine := engine.NewAgentEngine(llmProvider, agentConfig)
	engine.SetMemory(memoryProvider)
	engine.AddTools(tools)
	return engine, nil
}

func (a *agent) Engine(sessionID string) (*engine.AgentEngine, error) {
	var v interface{}
	if err := cache.Local.Get(sessionID, &v); err == nil {
		if eng, ok := v.(*engine.AgentEngine); ok {
			return eng, nil
		}
	}

	agentEngine, err := a.build(sessionID)
	if err != nil {
		return nil, err
	}

	cache.Local.Set(sessionID, agentEngine, 10*time.Minute)
	return agentEngine, nil
}

func (a *agent) HttpTrigger() http.Handler {
	return http.NewHandler()
}

func (a *agent) McpTrigger() (mcp.Handler, error) {
	engine, err := a.Engine(uuid.New().String())
	if err != nil {
		return nil, err
	}
	mcpHandler := mcp.NewHandler(engine, mcp.Options{
		Server: mcp.Metadata{
			Name:    a.config.Agent.MCP.Server.Name,
			Version: a.config.Agent.MCP.Server.Version,
		},
		Tool: mcp.Metadata{
			Name:        a.config.Agent.MCP.Tool.Name,
			Description: a.config.Agent.MCP.Tool.Description,
		},
	})
	return mcpHandler, nil
}
