package mcp

import (
	"context"

	"github.com/gin-gonic/gin"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpsrv "github.com/mark3labs/mcp-go/server"
	"github.com/xichan96/cortex/agent/engine"
)

type Handler interface {
	Agent() gin.HandlerFunc
}

type handler struct {
	engine *engine.AgentEngine
	opt    Options
}

func NewHandler(engine *engine.AgentEngine, opt Options) Handler {
	return &handler{
		engine: engine,
		opt:    opt,
	}
}

func (h *handler) Agent() gin.HandlerFunc {
	var mcp = mcpsrv.NewMCPServer(
		h.opt.Server.Name,
		h.opt.Server.Version,
		mcpsrv.WithToolCapabilities(true),
	)
	h.registerTools(mcp)
	mcpHandler := mcpsrv.NewStreamableHTTPServer(
		mcp,
		mcpsrv.WithEndpointPath("/mcp"),
	)
	return gin.WrapH(mcpHandler)
}

func (h *handler) registerTools(mcp *mcpsrv.MCPServer) {
	mcp.AddTool(
		mcpgo.NewTool("ping", mcpgo.WithDescription("health check")),
		func(_ context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok"), nil
		},
	)
	chatTool := mcpgo.NewTool(h.opt.Tool.Name, mcpgo.WithDescription(h.opt.Tool.Description),
		mcpgo.WithString("message", func(prop map[string]any) {
			prop["description"] = "message to send to the agent"
		}, mcpgo.Required()),
	)
	mcp.AddTool(
		chatTool,
		func(_ context.Context, request mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			message := request.GetString("message", "")
			result, err := h.engine.Execute(message, nil)
			if err != nil {
				return mcpgo.NewToolResultError(err.Error()), nil
			}
			return mcpgo.NewToolResultText(result.Output), nil
		},
	)
}
