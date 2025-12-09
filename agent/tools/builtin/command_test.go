package builtin

import (
	"testing"
	"time"

	"github.com/xichan96/cortex/pkg/errors"
)

func TestCommandTool_Name(t *testing.T) {
	tool := NewCommandTool()
	if tool.Name() != "command" {
		t.Errorf("Expected name 'command', got '%s'", tool.Name())
	}
}

func TestCommandTool_Description(t *testing.T) {
	tool := NewCommandTool()
	desc := tool.Description()
	if desc == "" {
		t.Error("Description should not be empty")
	}
}

func TestCommandTool_Schema(t *testing.T) {
	tool := NewCommandTool()
	schema := tool.Schema()

	if schema["type"] != "object" {
		t.Error("Schema type should be 'object'")
	}

	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Schema should have properties")
	}

	if _, ok := properties["command"]; !ok {
		t.Error("Schema should have 'command' property")
	}

	if _, ok := properties["timeout"]; !ok {
		t.Error("Schema should have 'timeout' property")
	}

	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("Schema should have required array")
	}

	found := false
	for _, r := range required {
		if r == "command" {
			found = true
			break
		}
	}
	if !found {
		t.Error("'command' should be in required array")
	}
}

func TestCommandTool_Metadata(t *testing.T) {
	tool := NewCommandTool()
	metadata := tool.Metadata()

	if metadata.SourceNodeName != "command" {
		t.Errorf("Expected SourceNodeName 'command', got '%s'", metadata.SourceNodeName)
	}

	if metadata.IsFromToolkit {
		t.Error("IsFromToolkit should be false")
	}

	if metadata.ToolType != "builtin" {
		t.Errorf("Expected ToolType 'builtin', got '%s'", metadata.ToolType)
	}
}

func TestCommandTool_Execute_Success(t *testing.T) {
	tool := NewCommandTool()

	input := map[string]interface{}{
		"command": "echo hello",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("Result should be a map")
	}

	if resultMap["exit_code"].(int) != 0 {
		t.Errorf("Expected exit_code 0, got %d", resultMap["exit_code"])
	}

	if resultMap["stdout"].(string) != "hello\n" {
		t.Errorf("Expected stdout 'hello\\n', got '%s'", resultMap["stdout"])
	}
}

func TestCommandTool_Execute_CommandFailure(t *testing.T) {
	tool := NewCommandTool()

	input := map[string]interface{}{
		"command": "false",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute should not return error for failed command: %v", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("Result should be a map")
	}

	if resultMap["exit_code"].(int) == 0 {
		t.Error("Expected non-zero exit code for failed command")
	}

	if _, ok := resultMap["error"]; !ok {
		t.Error("Result should contain 'error' field for failed command")
	}
}

func TestCommandTool_Execute_InvalidCommandType(t *testing.T) {
	tool := NewCommandTool()

	input := map[string]interface{}{
		"command": 123,
	}

	_, err := tool.Execute(input)
	if err == nil {
		t.Fatal("Execute should return error for invalid command type")
	}

	errObj, ok := err.(*errors.Error)
	if !ok {
		t.Fatalf("Expected *errors.Error, got %T", err)
	}

	if errObj.Code != errors.EC_TOOL_PARAMETER_INVALID.Code {
		t.Errorf("Expected error code %d, got %d", errors.EC_TOOL_PARAMETER_INVALID.Code, errObj.Code)
	}
}

func TestCommandTool_Execute_EmptyCommand(t *testing.T) {
	tool := NewCommandTool()

	input := map[string]interface{}{
		"command": "",
	}

	_, err := tool.Execute(input)
	if err == nil {
		t.Fatal("Execute should return error for empty command")
	}

	errObj, ok := err.(*errors.Error)
	if !ok {
		t.Fatalf("Expected *errors.Error, got %T", err)
	}

	if errObj.Code != errors.EC_PARAMETER_MISSING.Code {
		t.Errorf("Expected error code %d, got %d", errors.EC_PARAMETER_MISSING.Code, errObj.Code)
	}
}

func TestCommandTool_Execute_WithTimeoutFloat64(t *testing.T) {
	tool := NewCommandTool()

	input := map[string]interface{}{
		"command": "echo test",
		"timeout": float64(5),
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("Result should be a map")
	}

	if resultMap["exit_code"].(int) != 0 {
		t.Error("Command should succeed")
	}
}

func TestCommandTool_Execute_WithTimeoutInt(t *testing.T) {
	tool := NewCommandTool()

	input := map[string]interface{}{
		"command": "echo test",
		"timeout": 5,
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("Result should be a map")
	}

	if resultMap["exit_code"].(int) != 0 {
		t.Error("Command should succeed")
	}
}

func TestCommandTool_Execute_Timeout(t *testing.T) {
	tool := NewCommandTool()

	input := map[string]interface{}{
		"command": "sleep 2",
		"timeout": float64(1),
	}

	_, err := tool.Execute(input)
	if err == nil {
		t.Fatal("Execute should return error for timeout")
	}

	errObj, ok := err.(*errors.Error)
	if !ok {
		t.Fatalf("Expected *errors.Error, got %T", err)
	}

	if errObj.Code != errors.EC_TOOL_EXECUTION_TIMEOUT.Code {
		t.Errorf("Expected error code %d, got %d", errors.EC_TOOL_EXECUTION_TIMEOUT.Code, errObj.Code)
	}
}

func TestCommandTool_Execute_DefaultTimeout(t *testing.T) {
	tool := NewCommandTool()

	input := map[string]interface{}{
		"command": "echo test",
	}

	start := time.Now()
	result, err := tool.Execute(input)
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if duration > 35*time.Second {
		t.Error("Command should use default timeout of 30 seconds")
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("Result should be a map")
	}

	if resultMap["exit_code"].(int) != 0 {
		t.Error("Command should succeed")
	}
}

func TestCommandTool_Execute_CommandWithArgs(t *testing.T) {
	tool := NewCommandTool()

	input := map[string]interface{}{
		"command": "echo hello world",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("Result should be a map")
	}

	if resultMap["stdout"].(string) != "hello world\n" {
		t.Errorf("Expected stdout 'hello world\\n', got '%s'", resultMap["stdout"])
	}
}

func TestCommandTool_Execute_Stderr(t *testing.T) {
	tool := NewCommandTool()

	input := map[string]interface{}{
		"command": "sh -c 'echo error >&2'",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("Result should be a map")
	}

	if resultMap["stderr"].(string) == "" {
		t.Error("stderr should not be empty")
	}
}
