package app

import (
	"context"
	"fmt"

	emailtool "github.com/xichan96/cortex/agent/tools/builtin/email"
	"github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/tools/builtin/math"
	"github.com/xichan96/cortex/agent/tools/builtin/net"
	"github.com/xichan96/cortex/agent/tools/builtin/runtime"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/internal/config"
	"github.com/xichan96/cortex/pkg/email"
	"github.com/xichan96/cortex/pkg/mcp"
)

func (a *agent) setupTools(ctx context.Context) ([]types.Tool, error) {
	var tools []types.Tool

	toolsCfg := a.config.Tools

	if toolsCfg.Builtin.Enabled {
		tools = append(tools, a.initBuiltinTools()...)
	}

	for _, mcpCfg := range toolsCfg.MCP {
		if mcpCfg.Enabled {
			mcpTools, err := a.initMCPTools(ctx, mcpCfg)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize MCP tools: %w", err)
			}
			tools = append(tools, mcpTools...)
		}
	}

	return tools, nil
}

func (a *agent) initBuiltinTools() []types.Tool {
	var tools []types.Tool
	cfg := a.config.Tools.Builtin

	if cfg.SSH.Enabled {
		tools = append(tools, net.NewSSHTool())
	}

	if cfg.File.Enabled {
		tools = append(tools, fs.NewFileTool(""))
	}

	if cfg.Command.Enabled {
		tools = append(tools, runtime.NewCommandTool())
	}

	if cfg.Math.Enabled {
		tools = append(tools, math.NewMathTool())
	}

	if cfg.Ping.Enabled {
		tools = append(tools, net.NewPingTool())
	}

	if cfg.Time.Enabled {
		tools = append(tools, runtime.NewTimeTool())
	}

	if cfg.Email.Enabled {
		emailCfg := &email.Config{
			Address: cfg.Email.Config.Address,
			Name:    cfg.Email.Config.Name,
			Pwd:     cfg.Email.Config.Pwd,
			Host:    cfg.Email.Config.Host,
			Port:    cfg.Email.Config.Port,
		}
		tools = append(tools, emailtool.NewEmailTool(emailCfg))
	}

	return tools
}

func (a *agent) initMCPTools(ctx context.Context, cfg config.MCPConfig) ([]types.Tool, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("MCP URL is required")
	}

	mcpClient := mcp.NewClient(cfg.URL, cfg.Transport, cfg.Headers)

	if err := mcpClient.Connect(ctx); err != nil {
		return nil, fmt.Errorf("failed to connect to MCP server: %w", err)
	}

	tools := mcpClient.GetTools()
	return tools, nil
}
