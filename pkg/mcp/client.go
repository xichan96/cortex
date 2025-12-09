package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/logger"
)

// Client MCP client - using official SDK

type Client struct {
	serverURL string
	transport string // "httpStreamable" or "sse"
	headers   map[string]string
	mcpClient *client.Client
	tools     []types.Tool
	toolsMu   sync.RWMutex
	connected bool
	connectMu sync.RWMutex
	logger    *logger.Logger
}

// NewClient creates a new MCP client
func NewClient(url string, transport string, headers map[string]string) *Client {
	if transport == "" {
		transport = "sse" // default to SSE
	}
	if headers == nil {
		headers = make(map[string]string)
	}

	return &Client{
		serverURL: url,
		transport: transport,
		headers:   headers,
		tools:     make([]types.Tool, 0),
		logger:    logger.NewLogger(),
	}
}

// Connect connects to MCP server
func (c *Client) Connect(ctx context.Context) error {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	if c.connected {
		return nil
	}

	c.logger.Info("Connecting to MCP server",
		slog.String("server_url", c.serverURL),
		slog.String("transport", c.transport))

	var err error

	switch c.transport {
	case "http", "httpStreamable":
		c.mcpClient, err = client.NewStreamableHttpClient(c.serverURL)
	case "sse":
		c.mcpClient, err = client.NewSSEMCPClient(c.serverURL, client.WithHeaders(c.headers))
	default:
		return errors.NewError(errors.EC_MCP_UNSUPPORTED_TRANSPORT.Code, fmt.Sprintf("unsupported transport: %s", c.transport))
	}

	if err != nil {
		return errors.NewError(errors.EC_MCP_CLIENT_CREATE_FAILED.Code, errors.EC_MCP_CLIENT_CREATE_FAILED.Message).Wrap(err)
	}

	if err := c.mcpClient.Start(ctx); err != nil {
		return errors.NewError(errors.EC_MCP_CLIENT_START_FAILED.Code, errors.EC_MCP_CLIENT_START_FAILED.Message).Wrap(err)
	}

	// Initialize client
	initRequest := mcp.InitializeRequest{
		Request: mcp.Request{
			Method: "initialize",
		},
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo: mcp.Implementation{
				Name:    "cortex-mcp-client",
				Version: "1.0.0",
			},
		},
	}

	_, err = c.mcpClient.Initialize(ctx, initRequest)
	if err != nil {
		c.mcpClient.Close()
		return errors.NewError(errors.EC_MCP_CLIENT_INIT_FAILED.Code, errors.EC_MCP_CLIENT_INIT_FAILED.Message).Wrap(err)
	}

	c.connected = true

	// Get available tool list
	if err := c.refreshTools(ctx); err != nil {
		c.connected = false
		c.mcpClient.Close()
		return errors.NewError(errors.EC_MCP_REFRESH_TOOLS_FAILED.Code, errors.EC_MCP_REFRESH_TOOLS_FAILED.Message).Wrap(err)
	}

	return nil
}

// Disconnect disconnects from MCP server
func (c *Client) Disconnect(ctx context.Context) error {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	if !c.connected {
		return nil
	}

	if c.mcpClient != nil {
		c.mcpClient.Close()
		c.mcpClient = nil
	}

	c.connected = false
	c.tools = make([]types.Tool, 0)

	return nil
}

// IsConnected checks if connected
func (c *Client) IsConnected() bool {
	c.connectMu.RLock()
	defer c.connectMu.RUnlock()
	return c.connected
}

// GetTools gets available tools
func (c *Client) GetTools() []types.Tool {
	c.toolsMu.RLock()
	defer c.toolsMu.RUnlock()

	tools := make([]types.Tool, len(c.tools))
	copy(tools, c.tools)
	return tools
}

// CallTool calls a tool on the MCP server
func (c *Client) CallTool(ctx context.Context, toolName string, arguments map[string]interface{}) (interface{}, error) {
	c.connectMu.RLock()
	if !c.connected {
		c.connectMu.RUnlock()
		return nil, errors.EC_MCP_NOT_CONNECTED
	}
	mcpClient := c.mcpClient
	c.connectMu.RUnlock()

	params := mcp.CallToolRequest{
		Request: mcp.Request{
			Method: "tools/call",
		},
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: arguments,
		},
	}

	result, err := mcpClient.CallTool(ctx, params)
	if err != nil {
		return nil, errors.NewError(errors.EC_MCP_CALL_TOOL_FAILED.Code, fmt.Sprintf("failed to call tool %s", toolName)).Wrap(err)
	}

	if result.IsError {
		return nil, errors.NewError(errors.EC_MCP_TOOL_RETURNED_ERROR.Code, fmt.Sprintf("tool %s returned error: %v", toolName, result.Content))
	}

	return map[string]interface{}{
		"tool":    toolName,
		"status":  "success",
		"message": result.Content,
	}, nil
}

// refreshTools refreshes tool list
func (c *Client) refreshTools(ctx context.Context) error {
	if c.mcpClient == nil {
		return errors.EC_MCP_NO_ACTIVE_CLIENT
	}

	c.logger.Info("Fetching tool list from MCP server")

	request := mcp.ListToolsRequest{}
	result, err := c.mcpClient.ListTools(ctx, request)
	if err != nil {
		return errors.NewError(errors.EC_MCP_GET_TOOLS_FAILED.Code, errors.EC_MCP_GET_TOOLS_FAILED.Message).Wrap(err)
	}

	// Convert fetched tools to MCP tools
	mcpTools := make([]types.Tool, 0, len(result.Tools))
	for _, tool := range result.Tools {
		// Handle empty input schema - default to object type
		schema := map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
			"required":   []string{},
		}

		// Only use actual schema values if Type field is not empty
		if tool.InputSchema.Type != "" {
			schema["type"] = tool.InputSchema.Type
			if tool.InputSchema.Properties != nil {
				schema["properties"] = tool.InputSchema.Properties
			}
			if tool.InputSchema.Required != nil {
				schema["required"] = tool.InputSchema.Required
			}
		}

		mcpTool := NewMCPTool(tool.Name, tool.Description, schema)
		mcpTool.SetClient(c)
		mcpTools = append(mcpTools, mcpTool)
	}

	c.toolsMu.Lock()
	c.tools = mcpTools
	c.toolsMu.Unlock()

	c.logger.Info("Successfully fetched tools from MCP server",
		slog.Int("tool_count", len(mcpTools)))
	return nil
}

// MCPTool MCP tool implementation
type MCPTool struct {
	name        string
	description string
	schema      map[string]interface{}
	client      *Client
}

// NewMCPTool creates a new MCP tool
func NewMCPTool(name, description string, schema map[string]interface{}) *MCPTool {
	return &MCPTool{
		name:        name,
		description: description,
		schema:      schema,
	}
}

// SetClient sets MCP client
func (t *MCPTool) SetClient(client *Client) {
	t.client = client
}

// Name gets tool name
func (t *MCPTool) Name() string {
	return t.name
}

// Description gets tool description
func (t *MCPTool) Description() string {
	return t.description
}

// Schema gets tool schema
func (t *MCPTool) Schema() map[string]interface{} {
	return t.schema
}

// Execute executes the tool
func (t *MCPTool) Execute(input map[string]interface{}) (interface{}, error) {
	if t.client == nil {
		return nil, errors.NewError(errors.EC_MCP_TOOL_NOT_CONNECTED.Code, errors.EC_MCP_TOOL_NOT_CONNECTED.Message)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := t.client
	return client.CallTool(ctx, t.name, input)
}

// Metadata gets tool metadata
func (t *MCPTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: t.name,
		IsFromToolkit:  true,
		ToolType:       "mcp",
		Extra: map[string]interface{}{
			"client_connected": t.client != nil && t.client.IsConnected(),
		},
	}
}
