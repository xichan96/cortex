package mcp

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/pkg/errors"
)

type Handler interface {
	Agent() gin.HandlerFunc
}

type handler struct {
	engine    *engine.AgentEngine
	opt       Options
	mcpServer *mcpsrv.MCPServer
}

func NewHandler(engine *engine.AgentEngine, opt Options) Handler {
	mcp := mcpsrv.NewMCPServer(
		opt.Server.Name,
		opt.Server.Version,
		mcpsrv.WithToolCapabilities(true),
	)
	h := &handler{
		engine:    engine,
		opt:       opt,
		mcpServer: mcp,
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
	mcp.AddTool(
		mcpgo.NewTool("ping", mcpgo.WithDescription("health check")),
		func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
				return mcpgo.NewToolResultText("ok"), nil
			}
		},
	)
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
				return nil, ctx.Err()
			default:
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
				} else {
					errorMsg = err.Error()
				}
				return mcpgo.NewToolResultError(errorMsg), nil
			}
			return mcpgo.NewToolResultText(result.Output), nil
		},
	)
}
