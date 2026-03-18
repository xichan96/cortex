package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	mcpclient "github.com/xichan96/cortex/pkg/mcp"
)

type ServerConfig struct {
	Name      string
	URL       string
	Transport string
	Headers   map[string]string
	Type      string // "local" or "remote"
	Command   []string
	Env       map[string]string
}

type MCPManager struct {
	mu      sync.RWMutex
	servers map[string]*MCPClient
}

type MCPClient struct {
	client *mcpclient.Client
	config *ServerConfig
	tools  map[string]ToolInfo
	mu     sync.RWMutex
}

type ToolInfo struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

type MCPServerNotification struct {
	Type      string
	SessionID string
	Tool      string
	CallID    string
	State     string
	Output    interface{}
	Error     string
}

func NewMCPManager() *MCPManager {
	return &MCPManager{
		servers: make(map[string]*MCPClient),
	}
}

func (m *MCPManager) AddServer(ctx context.Context, name string, config *ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.servers[name]; exists {
		return fmt.Errorf("MCP server %s already exists", name)
	}

	client := mcpclient.NewClient(config.URL, config.Transport, config.Headers)
	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to MCP server %s: %w", name, err)
	}

	mcpclientClient := &MCPClient{
		client: client,
		config: config,
		tools:  make(map[string]ToolInfo),
	}

	m.servers[name] = mcpclientClient
	return nil
}

func (m *MCPManager) RemoveServer(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	client, exists := m.servers[name]
	if !exists {
		return fmt.Errorf("MCP server %s not found", name)
	}

	client.client.Disconnect(context.Background())
	delete(m.servers, name)
	return nil
}

func (m *MCPManager) GetServer(name string) (*MCPClient, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	client, ok := m.servers[name]
	return client, ok
}

func (m *MCPManager) ListServers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	servers := make([]string, 0, len(m.servers))
	for name := range m.servers {
		servers = append(servers, name)
	}
	return servers
}

func (m *MCPManager) ListTools() map[string][]ToolInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string][]ToolInfo)
	for name, client := range m.servers {
		client.mu.RLock()
		tools := make([]ToolInfo, 0, len(client.tools))
		for _, t := range client.tools {
			tools = append(tools, t)
		}
		client.mu.RUnlock()
		result[name] = tools
	}
	return result
}

func (c *MCPClient) ExecuteTool(ctx context.Context, name string, input map[string]interface{}) (interface{}, error) {
	return c.client.CallTool(ctx, name, input)
}

func (c *MCPClient) ListTools() []ToolInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	tools := make([]ToolInfo, 0, len(c.tools))
	for _, t := range c.tools {
		tools = append(tools, t)
	}
	return tools
}

func (c *MCPClient) GetConfig() *ServerConfig {
	return c.config
}

func (m *MCPManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, client := range m.servers {
		client.client.Disconnect(context.Background())
	}
	m.servers = make(map[string]*MCPClient)
}

type MCPStreamHandler struct {
	manager    *MCPManager
	sessionID  string
	onToolCall func(notification MCPServerNotification)
	onError    func(err error)
	mu         sync.RWMutex
}

func NewMCPStreamHandler(manager *MCPManager, sessionID string) *MCPStreamHandler {
	return &MCPStreamHandler{
		manager:   manager,
		sessionID: sessionID,
	}
}

func (h *MCPStreamHandler) SetOnToolCall(fn func(notification MCPServerNotification)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onToolCall = fn
}

func (h *MCPStreamHandler) SetOnError(fn func(err error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onError = fn
}

func (h *MCPStreamHandler) Execute(ctx context.Context, serverName, toolName string, input map[string]interface{}) (interface{}, error) {
	client, ok := h.manager.GetServer(serverName)
	if !ok {
		return nil, fmt.Errorf("MCP server %s not found", serverName)
	}

	h.mu.RLock()
	onCall := h.onToolCall
	onErr := h.onError
	h.mu.RUnlock()

	if onCall != nil {
		onCall(MCPServerNotification{
			Type:      "tool_start",
			SessionID: h.sessionID,
			Tool:      toolName,
			CallID:    fmt.Sprintf("mcpclient-%s-%d", toolName, time.Now().UnixMilli()),
			State:     "pending",
		})
	}

	result, err := client.ExecuteTool(ctx, toolName, input)

	if err != nil {
		if onErr != nil {
			onErr(err)
		}
		if onCall != nil {
			onCall(MCPServerNotification{
				Type:      "tool_error",
				SessionID: h.sessionID,
				Tool:      toolName,
				CallID:    fmt.Sprintf("mcpclient-%s-%d", toolName, time.Now().UnixMilli()),
				State:     "error",
				Error:     err.Error(),
			})
		}
		return nil, err
	}

	if onCall != nil {
		onCall(MCPServerNotification{
			Type:      "tool_complete",
			SessionID: h.sessionID,
			Tool:      toolName,
			CallID:    fmt.Sprintf("mcpclient-%s-%d", toolName, time.Now().UnixMilli()),
			State:     "completed",
			Output:    result,
		})
	}

	return result, nil
}

func ConvertToMCPServerConfig(config map[string]interface{}) (*ServerConfig, error) {
	serverConfig := &ServerConfig{}

	if name, ok := config["name"].(string); ok {
		serverConfig.Name = name
	} else {
		return nil, fmt.Errorf("MCP server config missing name")
	}

	if url, ok := config["url"].(string); ok {
		serverConfig.URL = url
	}

	if transport, ok := config["transport"].(string); ok {
		serverConfig.Transport = transport
	}

	if headers, ok := config["headers"].(map[string]string); ok {
		serverConfig.Headers = headers
	}

	if serverType, ok := config["type"].(string); ok {
		serverConfig.Type = serverType
	} else if serverConfig.URL != "" {
		serverConfig.Type = "remote"
	} else {
		serverConfig.Type = "local"
	}

	if cmd, ok := config["command"].([]interface{}); ok {
		for _, c := range cmd {
			if str, ok := c.(string); ok {
				serverConfig.Command = append(serverConfig.Command, str)
			}
		}
	}

	if env, ok := config["env"].(map[string]interface{}); ok {
		serverConfig.Env = make(map[string]string)
		for k, v := range env {
			if str, ok := v.(string); ok {
				serverConfig.Env[k] = str
			}
		}
	}

	return serverConfig, nil
}
