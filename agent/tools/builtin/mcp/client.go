package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/mcp"
)

type MCPClientTool struct {
	client *mcp.Client
	mu     sync.RWMutex
}

func NewMCPClientTool() types.Tool {
	return &MCPClientTool{}
}

func (t *MCPClientTool) Name() string {
	return "mcp_client"
}

func (t *MCPClientTool) Description() string {
	return "A built-in tool for interacting with MCP servers. Supports connecting to a server, listing available tools, and calling tools."
}

func (t *MCPClientTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"description": "Action to perform: 'connect', 'list_tools', 'call_tool', 'disconnect'",
				"enum":        []string{"connect", "list_tools", "call_tool", "disconnect"},
			},
			"server_url": map[string]interface{}{
				"type":        "string",
				"description": "MCP server URL (required for 'connect')",
			},
			"transport": map[string]interface{}{
				"type":        "string",
				"description": "Transport type: 'sse' or 'http' (optional, default: 'sse')",
				"enum":        []string{"sse", "http"},
			},
			"headers": map[string]interface{}{
				"type":        "object",
				"description": "Headers for connection (optional)",
				"additionalProperties": map[string]interface{}{
					"type": "string",
				},
			},
			"tool_name": map[string]interface{}{
				"type":        "string",
				"description": "Tool name to call (required for 'call_tool')",
			},
			"arguments": map[string]interface{}{
				"type":        "object",
				"description": "Arguments for the tool call (required for 'call_tool')",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Timeout in seconds (optional, default: 30 for connect, 60 for call_tool)",
			},
		},
		"required": []string{"action"},
	}
}

func (t *MCPClientTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	action, ok := input["action"].(string)
	if !ok {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("action is required"))
	}

	switch action {
	case "connect":
		return t.connect(ctx, input)
	case "list_tools":
		return t.listTools(ctx)
	case "call_tool":
		return t.callTool(ctx, input)
	case "disconnect":
		return t.disconnect(ctx)
	default:
		return nil, errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("unknown action: %s", action))
	}
}

func (t *MCPClientTool) connect(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.client != nil && t.client.IsConnected() {
		_ = t.client.Disconnect(ctx)
	}

	serverURL, ok := input["server_url"].(string)
	if !ok || serverURL == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("server_url is required for connect"))
	}

	transport := "sse"
	if val, ok := input["transport"].(string); ok && val != "" {
		transport = val
	}

	var headers map[string]string
	if val, ok := input["headers"].(map[string]interface{}); ok {
		headers = make(map[string]string)
		for k, v := range val {
			if strVal, ok := v.(string); ok {
				headers[k] = strVal
			}
		}
	}

	client := mcp.NewClient(serverURL, transport, headers)

	timeout := 30 * time.Second
	if val, ok := input["timeout"].(float64); ok {
		timeout = time.Duration(val) * time.Second
	} else if val, ok := input["timeout"].(int); ok {
		timeout = time.Duration(val) * time.Second
	}

	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := client.Connect(connectCtx); err != nil {
		return nil, err
	}

	t.client = client
	return map[string]interface{}{
		"status":     "connected",
		"server_url": serverURL,
		"tool_count": len(client.GetTools()),
	}, nil
}

func (t *MCPClientTool) disconnect(ctx context.Context) (interface{}, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.client == nil {
		return map[string]interface{}{"status": "disconnected", "message": "no active connection"}, nil
	}

	disconnectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err := t.client.Disconnect(disconnectCtx)
	t.client = nil
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"status": "disconnected"}, nil
}

func (t *MCPClientTool) listTools(ctx context.Context) (interface{}, error) {
	t.mu.RLock()
	client := t.client
	t.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, errors.EC_MCP_NOT_CONNECTED
	}

	tools := client.GetTools()
	toolList := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		toolList = append(toolList, map[string]interface{}{
			"name":        tool.Name(),
			"description": tool.Description(),
			"schema":      tool.Schema(),
		})
	}
	return toolList, nil
}

func (t *MCPClientTool) callTool(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	t.mu.RLock()
	client := t.client
	t.mu.RUnlock()

	if client == nil || !client.IsConnected() {
		return nil, errors.EC_MCP_NOT_CONNECTED
	}

	toolName, ok := input["tool_name"].(string)
	if !ok || toolName == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("tool_name is required for call_tool"))
	}

	arguments, ok := input["arguments"].(map[string]interface{})
	if !ok {
		arguments = make(map[string]interface{})
	}

	timeout := 60 * time.Second
	if val, ok := input["timeout"].(float64); ok {
		timeout = time.Duration(val) * time.Second
	} else if val, ok := input["timeout"].(int); ok {
		timeout = time.Duration(val) * time.Second
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return client.CallTool(callCtx, toolName, arguments)
}

func (t *MCPClientTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: t.Name(),
		IsFromToolkit:  false,
		ToolType:       "mcp",
	}
}
