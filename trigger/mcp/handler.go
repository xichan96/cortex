package mcp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gin-gonic/gin"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/logger"
)

type Handler interface {
	Agent() gin.HandlerFunc
}

type handler struct {
	engine    *engine.AgentEngine
	opt       Options
	mcpServer *mcpsrv.MCPServer
	logger    *logger.Logger
}

func NewHandler(engine *engine.AgentEngine, opt Options) Handler {
	if engine == nil {
		// 使用默认 logger 记录错误，但继续创建 handler
		logger.NewLogger().LogError("NewHandler", fmt.Errorf("agent engine is nil"))
	}

	if opt.Tool.Name == "" {
		logger.NewLogger().LogError("NewHandler", fmt.Errorf("tool name is required"))
	}

	mcp := mcpsrv.NewMCPServer(
		opt.Server.Name,
		opt.Server.Version,
		mcpsrv.WithToolCapabilities(true),
	)
	h := &handler{
		engine:    engine,
		opt:       opt,
		mcpServer: mcp,
		logger:    logger.NewLogger(),
	}
	h.registerTools(mcp)
	return h
}

func (h *handler) Agent() gin.HandlerFunc {
	mcpHandler := mcpsrv.NewStreamableHTTPServer(
		h.mcpServer,
		mcpsrv.WithEndpointPath("/mcp"),
	)
	return gin.WrapH(mcpHandler)
}

func (h *handler) registerTools(mcp *mcpsrv.MCPServer) {
	h.logger.Info("Registering MCP tools",
		slog.String("tool_name", h.opt.Tool.Name),
		slog.String("server_name", h.opt.Server.Name))

	mcp.AddTool(
		mcpgo.NewTool("ping", mcpgo.WithDescription("health check")),
		func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			select {
			case <-ctx.Done():
				h.logger.Info("Ping tool context cancelled",
					slog.String("reason", ctx.Err().Error()))
				return nil, ctx.Err()
			default:
				return mcpgo.NewToolResultText("ok"), nil
			}
		},
	)

	if h.opt.Tool.Name == "" {
		h.logger.LogError("registerTools", fmt.Errorf("tool name is required"))
		return
	}

	chatTool := mcpgo.NewTool(h.opt.Tool.Name, mcpgo.WithDescription(h.opt.Tool.Description),
		mcpgo.WithString("message", func(prop map[string]any) {
			prop["description"] = "message to send to the agent"
		}, mcpgo.Required()),
	)
	mcp.AddTool(
		chatTool,
		func(ctx context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			select {
			case <-ctx.Done():
				h.logger.Info("Chat tool context cancelled",
					slog.String("reason", ctx.Err().Error()))
				return nil, ctx.Err()
			default:
			}

			if h.engine == nil {
				h.logger.LogError("Chat tool", fmt.Errorf("agent engine is nil"))
				return mcpgo.NewToolResultError("agent engine is not available"), nil
			}

			message := request.GetString("message", "")
			if message == "" {
				return mcpgo.NewToolResultError("message parameter is required"), nil
			}

			result, err := h.engine.Execute(message, nil)
			if err != nil {
				var errorMsg string
				if e, ok := err.(*errors.Error); ok {
					errorMsg = fmt.Sprintf("%d: %s", e.Code, e.Message)
					h.logger.LogError("Chat tool execution", err,
						slog.Int("error_code", e.Code))
				} else {
					errorMsg = err.Error()
					h.logger.LogError("Chat tool execution", err)
				}
				return mcpgo.NewToolResultError(errorMsg), nil
			}
			return mcpgo.NewToolResultText(result.Output), nil
		},
	)
}
