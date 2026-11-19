package host

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/extensions"
)

func TestPluginExtensionRegistersTools(t *testing.T) {
	h := startHelper(t, "echo", Options{Timeout: 5 * time.Second})

	var updates []int
	ext := NewPluginExtension("acme", h)
	ext.OnUpdate = func(update ToolUpdate) error {
		var data struct {
			Step int `json:"step"`
		}
		if err := json.Unmarshal(update.Data, &data); err != nil {
			return err
		}
		updates = append(updates, data.Step)
		return nil
	}

	registry := extensions.NewRegistry()
	ctx := context.Background()
	if err := registry.RegisterExtension(ctx, ext, extensions.SourceInfo{Kind: extensions.SourcePlugin, ID: "acme"}, nil); err != nil {
		t.Fatalf("RegisterExtension: %v", err)
	}

	registrations := registry.ListTools()
	if len(registrations) != 2 {
		t.Fatalf("registered tools = %+v", registrations)
	}
	tool, reg, ok := registry.LookupTool("echo")
	if !ok {
		t.Fatal("echo tool not registered")
	}
	if reg.Source.Kind != extensions.SourcePlugin || reg.Source.ID != "acme" {
		t.Fatalf("source = %+v", reg.Source)
	}
	if reg.Version != "0.1.0" {
		t.Fatalf("version = %q", reg.Version)
	}

	schemaProvider, ok := tool.(extensions.SchemaProvider)
	if !ok {
		t.Fatal("tool does not implement SchemaProvider")
	}
	if schemaProvider.Schema()["type"] != "object" {
		t.Fatalf("schema = %+v", schemaProvider.Schema())
	}

	out, err := tool.Execute(ctx, json.RawMessage(`{"message":"hi"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	value, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", out)
	}
	if value["tool"] != "echo" {
		t.Fatalf("result = %+v", value)
	}
	if len(updates) != 2 || updates[0] != 1 || updates[1] != 2 {
		t.Fatalf("updates = %v", updates)
	}

	if err := registry.UnregisterExtension(ctx, "acme"); err != nil {
		t.Fatalf("UnregisterExtension: %v", err)
	}
	if _, _, ok := registry.LookupTool("echo"); ok {
		t.Fatal("echo tool still registered after unregister")
	}
	if _, err := h.ListTools(ctx); err != ErrClosed {
		t.Fatalf("ListTools after unregister = %v, want ErrClosed", err)
	}
}

func TestPluginToolWithoutHost(t *testing.T) {
	tool := NewPluginTool(nil, ToolSpec{Name: "x"})
	if _, err := tool.Execute(context.Background(), nil); err != ErrNilHost {
		t.Fatalf("err = %v, want ErrNilHost", err)
	}
	if tool.Schema()["type"] != "object" {
		t.Fatalf("default schema = %+v", tool.Schema())
	}
}

func TestPluginExtensionNilAPI(t *testing.T) {
	ext := NewPluginExtension("acme", nil)
	if err := ext.Init(context.Background(), &extensions.API{}); err != ErrNilHost {
		t.Fatalf("err = %v, want ErrNilHost", err)
	}
}
