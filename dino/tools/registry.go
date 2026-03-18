package tools

import (
	"sort"
	"sync"

	"github.com/xichan96/cortex/agent/types"
)

type Registry struct {
	mu    sync.RWMutex
	tools map[string]types.Tool
}

func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]types.Tool),
	}
}

func (r *Registry) Register(tool types.Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := tool.Name()
	if _, exists := r.tools[name]; exists {
		return ErrToolAlreadyRegistered(name)
	}

	r.tools[name] = tool
	return nil
}

func (r *Registry) Get(name string) (types.Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tool, exists := r.tools[name]
	if !exists {
		return nil, ErrToolNotFound(name)
	}

	return tool, nil
}

func (r *Registry) GetAll() []types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]types.Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		result = append(result, tool)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Name() < result[j].Name()
	})

	return result
}

func (r *Registry) Remove(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[name]; !exists {
		return ErrToolNotFound(name)
	}

	delete(r.tools, name)
	return nil
}

func (r *Registry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.tools = make(map[string]types.Tool)
}

func (r *Registry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return len(r.tools)
}

func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, exists := r.tools[name]
	return exists
}

type ToolError string

func (e ToolError) Error() string {
	return string(e)
}

func ErrToolNotFound(name string) error {
	return ToolError("tool not found: " + name)
}

func ErrToolAlreadyRegistered(name string) error {
	return ToolError("tool already registered: " + name)
}
