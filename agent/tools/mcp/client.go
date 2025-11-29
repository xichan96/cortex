package mcp

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/xichan96/cortex/agent/types"
)

// Client MCP client - using official SDK

type Client struct {
	serverURL  string
	transport  string // "httpStreamable" or "sse"
	headers    map[string]string
	mcpClient  *client.Client
	tools      []types.Tool
	toolsMu    sync.RWMutex
	connected  bool
	connectMu  sync.Mutex
	httpClient *http.Client
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
		serverURL:  url,
		transport:  transport,
		headers:    headers,
		tools:      make([]types.Tool, 0),
		httpClient: &http.Client{},
	}
}

// Connect connects to MCP server
func (c *Client) Connect(ctx context.Context) error {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	if c.connected {
		return nil
	}

	fmt.Printf("Connecting to MCP server: %s (transport: %s)\n", c.serverURL, c.transport)

	var err error

	switch c.transport {
	case "http", "httpStreamable":
		c.mcpClient, err = client.NewStreamableHttpClient(c.serverURL)
	case "sse":
		c.mcpClient, err = client.NewSSEMCPClient(c.serverURL, client.WithHeaders(c.headers))
	default:
		return fmt.Errorf("unsupported transport: %s", c.transport)
	}

	if err != nil {
		return fmt.Errorf("failed to create MCP client: %w", err)
	}

	if err := c.mcpClient.Start(ctx); err != nil {
		return fmt.Errorf("failed to start MCP client: %w", err)
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
		return fmt.Errorf("failed to initialize MCP client: %w", err)
	}

	c.connected = true

	// Get available tool list
	if err := c.refreshTools(ctx); err != nil {
		c.connected = false
		c.mcpClient.Close()
		return fmt.Errorf("failed to refresh tools: %w", err)
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
	c.connectMu.Lock()
	defer c.connectMu.Unlock()
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
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	if !c.connected {
		return nil, fmt.Errorf("not connected to MCP server")
	}

	params := mcp.CallToolRequest{
		Request: mcp.Request{
			Method: "tools/call",
		},
		Params: mcp.CallToolParams{
			Name:      toolName,
			Arguments: arguments,
		},
	}

	result, err := c.mcpClient.CallTool(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("failed to call tool %s: %w", toolName, err)
	}

	if result.IsError {
		return nil, fmt.Errorf("tool %s returned error: %v", toolName, result.Content)
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
		return fmt.Errorf("no active client")
	}

	fmt.Printf("Fetching tool list from MCP server...\n")

	request := mcp.ListToolsRequest{}
	result, err := c.mcpClient.ListTools(ctx, request)
	if err != nil {
		return fmt.Errorf("failed to get tools from server: %w", err)
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

	fmt.Printf("Successfully fetched %d tools\n", len(mcpTools))
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
		return nil, fmt.Errorf("MCP tool not connected to client")
	}

	ctx := context.Background()
	return t.client.CallTool(ctx, t.name, input)
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
