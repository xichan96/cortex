package dino

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xichan96/cortex/agent/extensions"
	"github.com/xichan96/cortex/agent/types"
)

type schemaExtensionTool struct {
	calls int
}

func (t *schemaExtensionTool) Name() string        { return "ext_hello" }
func (t *schemaExtensionTool) Description() string { return "says hello" }
func (t *schemaExtensionTool) Schema() map[string]any {
	return map[string]any{
		"type":       "object",
		"x-test-tag": true,
		"properties": map[string]any{"name": map[string]any{"type": "string"}},
	}
}
func (t *schemaExtensionTool) Execute(_ context.Context, input json.RawMessage) (any, error) {
	t.calls++
	return map[string]any{"echo": string(input)}, nil
}

type bareExtensionTool struct{}

func (t *bareExtensionTool) Name() string        { return "ext_bare" }
func (t *bareExtensionTool) Description() string { return "no schema" }
func (t *bareExtensionTool) Execute(_ context.Context, input json.RawMessage) (any, error) {
	return string(input), nil
}

func findTool(tools []types.Tool, name string) types.Tool {
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	return nil
}

func TestNewDinoFactory_ExtensionToolWiring(t *testing.T) {
	reg := extensions.NewRegistry()
	tool := &schemaExtensionTool{}
	if err := reg.RegisterTool(extensions.Registration{
		Name:    "ext_hello",
		Version: "1.2.3",
		Source:  extensions.SourceInfo{Kind: extensions.SourceBuiltin},
	}, tool); err != nil {
		t.Fatalf("RegisterTool: %v", err)
	}

	factory, err := NewDinoFactory(getTestConfig(), WithExtensionRegistry(reg))
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	adapted := findTool(factory.GetTools(), "ext_hello")
	if adapted == nil {
		t.Fatal("extension tool ext_hello was not registered")
	}
	if adapted.Description() != "says hello" {
		t.Errorf("description = %q", adapted.Description())
	}
	schema := adapted.Schema()
	if schema["x-test-tag"] != true {
		t.Errorf("schema = %+v, want schema from SchemaProvider", schema)
	}
	if adapted.Metadata().ToolType != "extension" {
		t.Errorf("tool type = %q, want extension", adapted.Metadata().ToolType)
	}

	out, err := adapted.Execute(context.Background(), map[string]any{"name": "cortex"})
	if err != nil {
		t.Fatalf("adapted Execute: %v", err)
	}
	if tool.calls != 1 {
		t.Fatalf("extension tool calls = %d, want 1", tool.calls)
	}
	echo, ok := out.(map[string]any)["echo"].(string)
	if !ok || echo == "" {
		t.Fatalf("output = %+v, want echo of raw args", out)
	}
}

func TestNewDinoFactory_ExtensionToolSchemaFallback(t *testing.T) {
	reg := extensions.NewRegistry()
	if err := reg.RegisterTool(extensions.Registration{Name: "ext_bare"}, &bareExtensionTool{}); err != nil {
		t.Fatalf("RegisterTool: %v", err)
	}

	factory, err := NewDinoFactory(getTestConfig(), WithExtensionRegistry(reg))
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	adapted := findTool(factory.GetTools(), "ext_bare")
	if adapted == nil {
		t.Fatal("extension tool ext_bare was not registered")
	}
	schema := adapted.Schema()
	if schema["type"] != "object" {
		t.Fatalf("fallback schema = %+v, want permissive object schema", schema)
	}
}
