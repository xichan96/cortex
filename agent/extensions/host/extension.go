package host

import (
	"context"
	"encoding/json"

	"github.com/xichan96/cortex/agent/extensions"
)

// PluginExtension adapts a running Host to the in-process extensions surface
// (design §7.2). Init asks the plugin for its tools and registers each one
// through API.Tools; the Registry therefore owns tool lifecycle and revokes
// them when the extension is unregistered.
type PluginExtension struct {
	// Source identifies the plugin in the registry. When Kind is empty it
	// defaults to SourcePlugin with the extension name as ID.
	Source extensions.SourceInfo
	// OnUpdate, when set, receives streamed tool progress for every tool call.
	OnUpdate func(ToolUpdate) error

	host *Host
	name string
}

var (
	_ extensions.Extension      = (*PluginExtension)(nil)
	_ extensions.Tool           = (*PluginTool)(nil)
	_ extensions.SchemaProvider = (*PluginTool)(nil)
)

// NewPluginExtension wraps host as an extension named name. An empty name is
// derived from the plugin's initialize result, falling back to "plugin".
func NewPluginExtension(name string, host *Host) *PluginExtension {
	if name == "" && host != nil {
		name = host.Info().Name
	}
	if name == "" {
		name = "plugin"
	}
	return &PluginExtension{
		host: host,
		name: name,
		Source: extensions.SourceInfo{
			Kind: extensions.SourcePlugin,
			ID:   name,
		},
	}
}

// Name implements extensions.Extension.
func (e *PluginExtension) Name() string {
	if e == nil {
		return ""
	}
	return e.name
}

// Init implements extensions.Extension. Every tool the plugin declares is
// registered under the plugin source; the plugin's version is stamped on each
// registration.
func (e *PluginExtension) Init(ctx context.Context, api *extensions.API) error {
	if e == nil || e.host == nil {
		return ErrNilHost
	}
	if api == nil || api.Tools == nil {
		return ErrNoToolRegistrar
	}
	tools, err := e.host.ListTools(ctx)
	if err != nil {
		return err
	}
	info := e.host.Info()
	source := e.Source
	if source.Kind == "" {
		source = extensions.SourceInfo{Kind: extensions.SourcePlugin, ID: e.name}
	}
	for _, spec := range tools {
		tool := NewPluginTool(e.host, spec)
		tool.OnUpdate = e.OnUpdate
		reg := extensions.Registration{
			Name:    spec.Name,
			Source:  source,
			Version: info.Version,
		}
		if err := api.Tools.Register(reg, tool); err != nil {
			return err
		}
	}
	return nil
}

// Shutdown implements extensions.Extension by stopping the plugin process.
func (e *PluginExtension) Shutdown(ctx context.Context) error {
	if e == nil || e.host == nil {
		return nil
	}
	return e.host.Shutdown(ctx)
}

// PluginTool exposes one remote tool as an extensions.Tool. Execute sends the
// raw arguments over the protocol and returns the decoded JSON result.
type PluginTool struct {
	// OnUpdate, when set, receives streamed progress events for Execute calls.
	OnUpdate func(ToolUpdate) error

	host *Host
	spec ToolSpec
}

// NewPluginTool wraps one plugin tool declaration.
func NewPluginTool(host *Host, spec ToolSpec) *PluginTool {
	return &PluginTool{host: host, spec: spec}
}

// Name implements extensions.Tool.
func (t *PluginTool) Name() string { return t.spec.Name }

// Description implements extensions.Tool.
func (t *PluginTool) Description() string { return t.spec.Description }

// Schema implements extensions.SchemaProvider with the schema declared by the
// plugin. A missing or invalid schema falls back to a permissive object schema.
func (t *PluginTool) Schema() map[string]any { return schemaOf(t.spec.InputSchema) }

// Execute implements extensions.Tool. The raw input is forwarded unchanged; the
// plugin's JSON result is decoded into a Go value. A result that is not valid
// JSON is returned as a string.
func (t *PluginTool) Execute(ctx context.Context, input json.RawMessage) (any, error) {
	if t.host == nil {
		return nil, ErrNilHost
	}
	args := input
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	result, err := t.host.CallToolStream(ctx, t.spec.Name, args, t.OnUpdate)
	if err != nil {
		return nil, err
	}
	if len(result) == 0 || string(result) == "null" {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(result, &value); err != nil {
		return string(result), nil
	}
	return value, nil
}
