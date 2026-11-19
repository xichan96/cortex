package dino

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/xichan96/cortex/agent/extensions"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"
)

// extensionToolAdapter exposes an extension-provided tool through the
// model-facing types.Tool interface. The extension tool receives the raw JSON
// arguments object; schema validation is the host pipeline's job.
type extensionToolAdapter struct {
	name string
	tool extensions.Tool
	meta types.ToolMetadata
}

func (a *extensionToolAdapter) Name() string        { return a.name }
func (a *extensionToolAdapter) Description() string { return a.tool.Description() }

func (a *extensionToolAdapter) Schema() map[string]any {
	if provider, ok := a.tool.(extensions.SchemaProvider); ok {
		if schema := provider.Schema(); schema != nil {
			return schema
		}
	}
	return map[string]any{"type": "object"}
}

func (a *extensionToolAdapter) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return a.tool.Execute(ctx, raw)
}

func (a *extensionToolAdapter) Metadata() types.ToolMetadata { return a.meta }

// WithExtensionRegistry makes the factory expose the registry's active tools
// to every session. The registry itself is owned by the host; extensions are
// registered (and revoked) there.
func WithExtensionRegistry(reg *extensions.Registry) FactoryOption {
	return func(f *dinoFactory) {
		if reg != nil {
			f.extensionRegistry = reg
		}
	}
}

// registerExtensionTools copies the registry's active tools into the dino tool
// registry. It runs after all FactoryOptions so hosts can install the registry
// in the same call that builds the factory.
func (f *dinoFactory) registerExtensionTools() {
	if f.extensionRegistry == nil {
		return
	}
	for _, reg := range f.extensionRegistry.ListTools() {
		tool, _, ok := f.extensionRegistry.LookupTool(reg.Name)
		if !ok {
			continue
		}
		adapter := &extensionToolAdapter{
			name: reg.Name,
			tool: tool,
			meta: types.ToolMetadata{
				ToolType:       "extension",
				SourceNodeName: reg.Source.String(),
				Extra: map[string]interface{}{
					"version": reg.Version,
				},
			},
		}
		if err := f.tools.Register(adapter); err != nil {
			logger.Warn("[DinoFactory] Failed to register extension tool",
				slog.String("tool", reg.Name),
				slog.String("source", reg.Source.String()),
				slog.String("error", err.Error()))
		}
	}
}
