package tools

import (
	"fmt"
	"sort"
	"sync"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

// Registry tool registry
type Registry struct {
	tools       map[string]types.Tool
	toolsByType map[string][]types.Tool
	mu          sync.RWMutex
}

// NewRegistry creates a new tool registry
func NewRegistry() *Registry {
	return &Registry{
		tools:       make(map[string]types.Tool),
		toolsByType: make(map[string][]types.Tool),
	}
}

// Register registers a tool
func (r *Registry) Register(tool types.Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		return errors.NewError(errors.EC_TOOL_ALREADY_REGISTERED.Code, fmt.Sprintf("tool %s already registered", name))
	}

	r.tools[name] = tool

	// Update index
	toolType := tool.Metadata().ToolType
	r.toolsByType[toolType] = append(r.toolsByType[toolType], tool)

	return nil
}

// RegisterMultiple registers multiple tools
func (r *Registry) RegisterMultiple(tools []types.Tool) error {
	for _, tool := range tools {
		if err := r.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

// Get gets a tool
func (r *Registry) Get(name string) (types.Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, exists := r.tools[name]
	if !exists {
		return nil, errors.NewError(errors.EC_TOOL_NOT_FOUND.Code, fmt.Sprintf("tool %s not found", name))
	}

	return tool, nil
}

// GetAll gets all tools (sorted by name)
func (r *Registry) GetAll() []types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]types.Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name() < tools[j].Name() })
	return tools
}

// GetAllVisible returns only tools whose exposure is Direct (the default).
// It is the E1 filter used when assembling the model's initial tool list.
// GetAll() keeps its original semantics (all tools) for subagent dispatch/debug.
func (r *Registry) GetAllVisible() []types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]types.Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		if tool.Metadata().Exposure.IsDirect() {
			tools = append(tools, tool)
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name() < tools[j].Name() })
	return tools
}

// GetDeferred returns all tools whose exposure is Deferred. It feeds the
// tool_search index (E2).
func (r *Registry) GetDeferred() []types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tools := make([]types.Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		if tool.Metadata().Exposure.IsDeferred() {
			tools = append(tools, tool)
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name() < tools[j].Name() })
	return tools
}

// GetDeferredTool returns a single deferred tool by name. It is used by the
// tool_search index build and by tests; the runtime discover path uses the
// per-session pre-wrapped cache instead (see dino/factory.go sessionDeferredTools).
func (r *Registry) GetDeferredTool(name string) (types.Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, exists := r.tools[name]
	if !exists {
		return nil, errors.NewError(errors.EC_TOOL_NOT_FOUND.Code, fmt.Sprintf("tool %s not found", name))
	}
	if !tool.Metadata().Exposure.IsDeferred() {
		return nil, errors.NewError(errors.EC_TOOL_NOT_FOUND.Code, fmt.Sprintf("tool %s is not deferred", name))
	}
	return tool, nil
}

// GetByType gets tools by type
func (r *Registry) GetByType(toolType string) []types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if tools, ok := r.toolsByType[toolType]; ok {
		// Return a copy to prevent external modification of the slice
		result := make([]types.Tool, len(tools))
		copy(result, tools)
		return result
	}

	return []types.Tool{}
}

// Remove removes a tool
func (r *Registry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[name]; !exists {
		return errors.NewError(errors.EC_TOOL_NOT_FOUND.Code, fmt.Sprintf("tool %s not found", name))
	}

	// Remove from main map
	tool := r.tools[name]
	delete(r.tools, name)

	// Remove from index
	toolType := tool.Metadata().ToolType
	if tools, ok := r.toolsByType[toolType]; ok {
		newTools := make([]types.Tool, 0, len(tools)-1)
		for _, t := range tools {
			if t.Name() != name {
				newTools = append(newTools, t)
			}
		}
		if len(newTools) == 0 {
			delete(r.toolsByType, toolType)
		} else {
			r.toolsByType[toolType] = newTools
		}
	}

	return nil
}

// Clear clears all tools
func (r *Registry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.tools = make(map[string]types.Tool)
}

// Size gets the number of tools
func (r *Registry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.tools)
}

// Has reports whether a tool is registered by name
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, exists := r.tools[name]
	return exists
}

// Manager tool manager
type Manager struct {
	registry *Registry
}

// NewManager creates a new tool manager
func NewManager() *Manager {
	return &Manager{
		registry: NewRegistry(),
	}
}

// Register registers a tool
func (m *Manager) Register(tool types.Tool) error {
	return m.registry.Register(tool)
}

// RegisterMultiple registers multiple tools
func (m *Manager) RegisterMultiple(tools []types.Tool) error {
	return m.registry.RegisterMultiple(tools)
}

// Get gets a tool
func (m *Manager) Get(name string) (types.Tool, error) {
	return m.registry.Get(name)
}

// GetAll gets all tools
func (m *Manager) GetAll() []types.Tool {
	return m.registry.GetAll()
}

// GetByType gets tools by type
func (m *Manager) GetByType(toolType string) []types.Tool {
	return m.registry.GetByType(toolType)
}

// Remove removes a tool
func (m *Manager) Remove(name string) error {
	return m.registry.Remove(name)
}

// Clear clears all tools
func (m *Manager) Clear() {
	m.registry.Clear()
}

// Size gets the number of tools
func (m *Manager) Size() int {
	return m.registry.Size()
}

// ToolInfo tool information
type ToolInfo struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Type        string                 `json:"type"`
	Metadata    types.ToolMetadata     `json:"metadata"`
	Schema      map[string]interface{} `json:"schema"`
}

// GetToolInfo gets tool information
func (m *Manager) GetToolInfo(name string) (*ToolInfo, error) {
	tool, err := m.registry.Get(name)
	if err != nil {
		return nil, err
	}

	metadata := tool.Metadata()

	return &ToolInfo{
		Name:        tool.Name(),
		Description: tool.Description(),
		Type:        metadata.ToolType,
		Metadata:    metadata,
		Schema:      tool.Schema(),
	}, nil
}

// GetAllToolInfo gets all tool information
func (m *Manager) GetAllToolInfo() []ToolInfo {
	tools := m.registry.GetAll()
	info := make([]ToolInfo, len(tools))

	for i, tool := range tools {
		metadata := tool.Metadata()
		info[i] = ToolInfo{
			Name:        tool.Name(),
			Description: tool.Description(),
			Type:        metadata.ToolType,
			Metadata:    metadata,
			Schema:      tool.Schema(),
		}
	}

	return info
}
