package tools

import (
	"testing"
)

func TestServerConfig(t *testing.T) {
	config := &ServerConfig{
		Name:      "test-server",
		URL:       "http://localhost:8080",
		Transport: "sse",
		Headers:   map[string]string{"Authorization": "Bearer token"},
		Type:      "remote",
		Command:   []string{"node", "server.js"},
		Env:       map[string]string{"NODE_ENV": "test"},
	}

	if config.Name != "test-server" {
		t.Errorf("expected Name 'test-server', got %s", config.Name)
	}
	if config.URL != "http://localhost:8080" {
		t.Errorf("expected URL 'http://localhost:8080', got %s", config.URL)
	}
	if config.Transport != "sse" {
		t.Errorf("expected Transport 'sse', got %s", config.Transport)
	}
	if len(config.Headers) != 1 {
		t.Errorf("expected 1 header, got %d", len(config.Headers))
	}
	if config.Type != "remote" {
		t.Errorf("expected Type 'remote', got %s", config.Type)
	}
	if len(config.Command) != 2 {
		t.Errorf("expected 2 commands, got %d", len(config.Command))
	}
	if len(config.Env) != 1 {
		t.Errorf("expected 1 env, got %d", len(config.Env))
	}
}

func TestToolInfo(t *testing.T) {
	info := ToolInfo{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: []byte(`{"type":"object"}`),
	}

	if info.Name != "test_tool" {
		t.Errorf("expected Name 'test_tool', got %s", info.Name)
	}
	if info.Description != "A test tool" {
		t.Errorf("expected Description 'A test tool', got %s", info.Description)
	}
}

func TestMCPServerNotification(t *testing.T) {
	notification := MCPServerNotification{
		Type:      "tool_start",
		SessionID: "session-123",
		Tool:      "test_tool",
		CallID:    "call-456",
		State:     "pending",
		Output:    "result",
		Error:     "",
	}

	if notification.Type != "tool_start" {
		t.Errorf("expected Type 'tool_start', got %s", notification.Type)
	}
	if notification.SessionID != "session-123" {
		t.Errorf("expected SessionID 'session-123', got %s", notification.SessionID)
	}
	if notification.Tool != "test_tool" {
		t.Errorf("expected Tool 'test_tool', got %s", notification.Tool)
	}
	if notification.State != "pending" {
		t.Errorf("expected State 'pending', got %s", notification.State)
	}
}

func TestNewMCPManager(t *testing.T) {
	manager := NewMCPManager()
	if manager == nil {
		t.Fatal("NewMCPManager returned nil")
	}
	if manager.servers == nil {
		t.Error("servers map should be initialized")
	}
	if len(manager.servers) != 0 {
		t.Errorf("expected empty servers map, got %d", len(manager.servers))
	}
}

func TestMCPManagerClose(t *testing.T) {
	manager := NewMCPManager()
	manager.Close()
}

func TestMCPStreamHandler(t *testing.T) {
	manager := NewMCPManager()
	handler := NewMCPStreamHandler(manager, "test-session")

	if handler == nil {
		t.Fatal("NewMCPStreamHandler returned nil")
	}
	if handler.manager != manager {
		t.Error("handler should reference the same manager")
	}
	if handler.sessionID != "test-session" {
		t.Errorf("expected sessionID 'test-session', got %s", handler.sessionID)
	}
}

func TestMCPStreamHandlerSetOnToolCall(t *testing.T) {
	manager := NewMCPManager()
	handler := NewMCPStreamHandler(manager, "test-session")

	fn := func(notification MCPServerNotification) {
	}

	handler.SetOnToolCall(fn)

	handler.mu.RLock()
	defer handler.mu.RUnlock()
	if handler.onToolCall == nil {
		t.Error("onToolCall should be set")
	}
}

func TestMCPStreamHandlerSetOnError(t *testing.T) {
	manager := NewMCPManager()
	handler := NewMCPStreamHandler(manager, "test-session")

	fn := func(err error) {
	}

	handler.SetOnError(fn)

	handler.mu.RLock()
	defer handler.mu.RUnlock()
	if handler.onError == nil {
		t.Error("onError should be set")
	}
}

func TestConvertToMCPServerConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      map[string]interface{}
		expectError bool
		expectType  string
	}{
		{
			name: "full config with URL",
			config: map[string]interface{}{
				"name":      "server1",
				"url":       "http://localhost:8080",
				"transport": "sse",
				"headers":   map[string]string{"Auth": "token"},
				"type":      "remote",
				"command":   []interface{}{"node", "app.js"},
				"env":       map[string]interface{}{"NODE_ENV": "test"},
			},
			expectError: false,
			expectType:  "remote",
		},
		{
			name: "minimal config without type",
			config: map[string]interface{}{
				"name": "server2",
				"url":  "http://localhost:9090",
			},
			expectError: false,
			expectType:  "remote",
		},
		{
			name: "minimal config without URL",
			config: map[string]interface{}{
				"name": "server3",
			},
			expectError: false,
			expectType:  "local",
		},
		{
			name: "missing name",
			config: map[string]interface{}{
				"url": "http://localhost:8080",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ConvertToMCPServerConfig(tt.config)

			if tt.expectError {
				if err == nil {
					t.Error("expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Name != tt.config["name"] {
				t.Errorf("expected Name '%v', got '%s'", tt.config["name"], result.Name)
			}

			if result.Type != tt.expectType {
				t.Errorf("expected Type '%s', got '%s'", tt.expectType, result.Type)
			}
		})
	}
}

func TestConvertToMCPServerConfigHeaders(t *testing.T) {
	config := map[string]interface{}{
		"name":    "test-server",
		"headers": map[string]string{"X-Custom": "value"},
	}

	result, err := ConvertToMCPServerConfig(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Headers == nil {
		t.Fatal("Headers should not be nil")
	}

	if result.Headers["X-Custom"] != "value" {
		t.Errorf("expected Header 'X-Custom' = 'value', got '%s'", result.Headers["X-Custom"])
	}
}

func TestConvertToMCPServerConfigCommand(t *testing.T) {
	config := map[string]interface{}{
		"name":    "test-server",
		"command": []interface{}{"python", "server.py", "--port", "8080"},
	}

	result, err := ConvertToMCPServerConfig(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Command) != 4 {
		t.Errorf("expected 4 command parts, got %d", len(result.Command))
	}

	expected := []string{"python", "server.py", "--port", "8080"}
	for i, cmd := range expected {
		if result.Command[i] != cmd {
			t.Errorf("expected Command[%d] = '%s', got '%s'", i, cmd, result.Command[i])
		}
	}
}

func TestConvertToMCPServerConfigEnv(t *testing.T) {
	config := map[string]interface{}{
		"name": "test-server",
		"env":  map[string]interface{}{"DEBUG": "true", "PORT": "8080"},
	}

	result, err := ConvertToMCPServerConfig(config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Env == nil {
		t.Fatal("Env should not be nil")
	}

	if result.Env["DEBUG"] != "true" {
		t.Errorf("expected Env 'DEBUG' = 'true', got '%s'", result.Env["DEBUG"])
	}

	if result.Env["PORT"] != "8080" {
		t.Errorf("expected Env 'PORT' = '8080', got '%s'", result.Env["PORT"])
	}
}
