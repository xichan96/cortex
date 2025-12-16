package app

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jinzhu/copier"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/internal/config"
	"github.com/xichan96/cortex/pkg/cache"
	"github.com/xichan96/cortex/trigger/http"
	"github.com/xichan96/cortex/trigger/mcp"
)

type Agent interface {
	// build agent
	setupLLM() types.LLMProvider
	setupMemory(sessionID string) types.MemoryProvider
	setupTools() []types.Tool
	build(sessionID string) (*engine.AgentEngine, error)
	Engine(sessionID string) (*engine.AgentEngine, error)

	// trigger methods
	HttpTrigger() http.Handler
	McpTrigger() (mcp.Handler, error)
}

type agent struct {
	config *config.Config
}

func NewAgent() Agent {
	return &agent{
		config: config.Get(),
	}
}

func (a *agent) build(sessionID string) (*engine.AgentEngine, error) {
	llmProvider := a.setupLLM()
	memoryProvider := a.setupMemory(sessionID)
	tools := a.setupTools()
	for _, tool := range tools {
		fmt.Println("add tool:", tool.Name())
	}
	agentConfig := types.NewAgentConfig()
	if err := copier.Copy(agentConfig, a.config.Agent); err != nil {
		return nil, err
	}
	engine := engine.NewAgentEngine(llmProvider, agentConfig)
	engine.SetMemory(memoryProvider)
	engine.AddTools(tools)
	return engine, nil
}

func (a *agent) Engine(sessionID string) (*engine.AgentEngine, error) {
	agentEngine := &engine.AgentEngine{}
	var v interface{}
	if err := cache.Local.Get(sessionID, &v); err != nil {
		agentEngine, err = a.build(sessionID)
		if err != nil {
			return nil, err
		}
		cache.Local.Set(sessionID, agentEngine, 10*time.Minute)
	}
	if v != nil {
		if eng, ok := v.(*engine.AgentEngine); ok {
			agentEngine = eng
		}
	}
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
