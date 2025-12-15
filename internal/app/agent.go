package app

import (
	"fmt"

	"github.com/jinzhu/copier"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/internal/config"
	"github.com/xichan96/cortex/trigger/http"
	"github.com/xichan96/cortex/trigger/mcp"
)

type Agent interface {
	// build agent
	setupLLM() types.LLMProvider
	setupMemory(sessionID string) types.MemoryProvider
	setupTools() []types.Tool
	Build(sessionID string) error
	// trigger methods
	HttpTrigger() http.Handler
	McpTrigger() mcp.Handler
}

type agent struct {
	config *config.Config
	engine *engine.AgentEngine
}

func NewAgent() Agent {
	return &agent{
		config: config.Get(),
	}
}

func (a *agent) Build(sessionID string) error {
	llmProvider := a.setupLLM()
	memoryProvider := a.setupMemory(sessionID)
	tools := a.setupTools()
	for _, tool := range tools {
		fmt.Println("add tool:", tool.Name())
	}
	agentConfig := types.NewAgentConfig()
	if err := copier.Copy(agentConfig, a.config.Agent); err != nil {
		return err
	}
	a.engine = engine.NewAgentEngine(llmProvider, agentConfig)
	a.engine.SetMemory(memoryProvider)
	a.engine.AddTools(tools)
	return nil
}
func (a *agent) HttpTrigger() http.Handler {
	return http.NewHandler(a.engine)
}

func (a *agent) McpTrigger() mcp.Handler {
	mcpHandler := mcp.NewHandler(a.engine, mcp.Options{
		Server: mcp.Metadata{
			Name:    a.config.Agent.MCP.Server.Name,
			Version: a.config.Agent.MCP.Server.Version,
		},
		Tool: mcp.Metadata{
			Name:        a.config.Agent.MCP.Tool.Name,
			Description: a.config.Agent.MCP.Tool.Description,
		},
	})
	return mcpHandler
}
