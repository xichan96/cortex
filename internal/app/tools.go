package app

import (
	"context"

	"github.com/xichan96/cortex/agent/tools/builtin"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/internal/config"
	"github.com/xichan96/cortex/pkg/email"
	"github.com/xichan96/cortex/pkg/mcp"
)

func (a *agent) setupTools() []types.Tool {
	var tools []types.Tool

	toolsCfg := a.config.Tools

	if toolsCfg.Builtin.Enabled {
		tools = append(tools, a.initBuiltinTools()...)
	}

	for _, mcpCfg := range toolsCfg.MCP {
		if mcpCfg.Enabled {
			mcpTools := a.initMCPTools(mcpCfg)
			tools = append(tools, mcpTools...)
		}
	}

	return tools
}

func (a *agent) initBuiltinTools() []types.Tool {
	var tools []types.Tool
	cfg := a.config.Tools.Builtin

	if cfg.SSH.Enabled {
		tools = append(tools, builtin.NewSSHTool())
	}

	if cfg.File.Enabled {
		tools = append(tools, builtin.NewFileTool())
	}

	if cfg.Command.Enabled {
		tools = append(tools, builtin.NewCommandTool())
	}

	if cfg.Math.Enabled {
		tools = append(tools, builtin.NewMathTool())
	}

	if cfg.Ping.Enabled {
		tools = append(tools, builtin.NewPingTool())
	}

	if cfg.Time.Enabled {
		tools = append(tools, builtin.NewTimeTool())
	}

	if cfg.Email.Enabled {
		emailCfg := &email.Config{
			Address: cfg.Email.Config.Address,
			Name:    cfg.Email.Config.Name,
			Pwd:     cfg.Email.Config.Pwd,
			Host:    cfg.Email.Config.Host,
			Port:    cfg.Email.Config.Port,
		}
		tools = append(tools, builtin.NewEmailTool(emailCfg))
	}

	return tools
}

func (a *agent) initMCPTools(cfg config.MCPConfig) []types.Tool {
	if cfg.URL == "" {
		return nil
	}

	mcpClient := mcp.NewClient(cfg.URL, cfg.Transport, cfg.Headers)

	ctx := context.Background()
	if err := mcpClient.Connect(ctx); err != nil {
		return nil
	}

	return mcpClient.GetTools()
}
