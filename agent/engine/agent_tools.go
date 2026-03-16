package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

// ==================== Tool Management Methods ====================

// AddTool adds a tool
func (ae *AgentEngine) AddTool(ctx context.Context, tool types.Tool) {
	ae.mu.Lock()
	defer ae.mu.Unlock()

	toolName := tool.Name()
	ae.tools = append(ae.tools, tool)
	ae.toolsMap[toolName] = tool
}

// AddTools adds multiple tools
func (ae *AgentEngine) AddTools(ctx context.Context, tools []types.Tool) {
	ae.mu.Lock()
	defer ae.mu.Unlock()

	for _, tool := range tools {
		toolName := tool.Name()
		ae.tools = append(ae.tools, tool)
		ae.toolsMap[toolName] = tool
	}
}

// ==================== Tool Execution Methods ====================

// executeToolWithTimeout executes a tool with timeout control
// Uses goroutine + channel to implement timeout without modifying Tool interface
// Note: The goroutine will continue running after timeout, but will naturally complete.
// This is an acceptable trade-off since the Tool interface doesn't support context cancellation.
// The goroutine will finish and clean up resources automatically, preventing leaks.
func (ae *AgentEngine) executeToolWithTimeout(ctx context.Context, tool types.Tool, args map[string]any, timeout time.Duration) (any, error) {
	// Create context with timeout if specified
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Use goroutine to allow execution to be interrupted by context
	type result struct {
		value any
		err   error
	}
	resultChan := make(chan result, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				resultChan <- result{value: nil, err: fmt.Errorf("tool execution panic: %v", r)}
			}
		}()

		// Pass context to tool execution
		val, err := tool.Execute(ctx, args)
		resultChan <- result{value: val, err: err}
	}()

	select {
	case res := <-resultChan:
		return res.value, res.err
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			return nil, errors.EC_TOOL_EXECUTION_TIMEOUT.Wrap(fmt.Errorf("tool execution timeout after %v", timeout))
		}
		return nil, ctx.Err()
	}
}

// ==================== Tool Dependency Management Methods ====================

// sortToolCallsByDependencies sorts tool calls by priority and dependencies using topological sort
// Returns sorted tool calls and error if circular dependency is detected
func (ae *AgentEngine) sortToolCallsByDependencies(toolCalls []types.ToolCall) ([]types.ToolCall, error) {
	if len(toolCalls) <= 1 {
		return toolCalls, nil
	}

	// Build dependency graph and priority map
	dependencyGraph := make(map[string][]string)   // tool -> dependencies
	priorityMap := make(map[string]int)            // tool -> priority
	toolCallMap := make(map[string]types.ToolCall) // tool name -> tool call

	ae.mu.RLock()
	for _, tc := range toolCalls {
		toolName := tc.Function.Name
		toolCallMap[toolName] = tc

		// Get tool metadata
		if tool, exists := ae.toolsMap[toolName]; exists {
			metadata := tool.Metadata()
			priorityMap[toolName] = metadata.Priority
			if len(metadata.Dependencies) > 0 {
				// Copy dependencies to avoid race conditions
				deps := make([]string, len(metadata.Dependencies))
				copy(deps, metadata.Dependencies)
				dependencyGraph[toolName] = deps
			}
		} else {
			priorityMap[toolName] = 0
		}
	}
	ae.mu.RUnlock()

	// Detect circular dependencies
	if err := ae.detectCircularDependencies(dependencyGraph); err != nil {
		return nil, err
	}

	// Topological sort with priority
	sorted := make([]types.ToolCall, 0, len(toolCalls))
	visited := make(map[string]bool)
	inProgress := make(map[string]bool)

	var visit func(string) error
	visit = func(toolName string) error {
		if inProgress[toolName] {
			return fmt.Errorf("circular dependency detected involving tool: %s", toolName)
		}
		if visited[toolName] {
			return nil
		}

		inProgress[toolName] = true

		// Visit dependencies first
		if deps, hasDeps := dependencyGraph[toolName]; hasDeps {
			for _, dep := range deps {
				if _, exists := toolCallMap[dep]; exists {
					if err := visit(dep); err != nil {
						return err
					}
				}
			}
		}

		inProgress[toolName] = false
		visited[toolName] = true

		// Add to sorted list
		if tc, exists := toolCallMap[toolName]; exists {
			sorted = append(sorted, tc)
		}

		return nil
	}

	// Sort by priority first, then visit
	type toolWithPriority struct {
		toolCall types.ToolCall
		priority int
	}
	toolsWithPriority := make([]toolWithPriority, 0, len(toolCalls))
	for _, tc := range toolCalls {
		toolsWithPriority = append(toolsWithPriority, toolWithPriority{
			toolCall: tc,
			priority: priorityMap[tc.Function.Name],
		})
	}

	// Sort by priority (descending) using efficient sort.Slice
	sort.Slice(toolsWithPriority, func(i, j int) bool {
		return toolsWithPriority[i].priority > toolsWithPriority[j].priority
	})

	// Visit tools in priority order
	for _, twp := range toolsWithPriority {
		if err := visit(twp.toolCall.Function.Name); err != nil {
			return nil, err
		}
	}

	return sorted, nil
}

// detectCircularDependencies detects circular dependencies in the dependency graph
func (ae *AgentEngine) detectCircularDependencies(graph map[string][]string) error {
	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	var hasCycle func(string) bool
	hasCycle = func(toolName string) bool {
		visited[toolName] = true
		recStack[toolName] = true

		if deps, exists := graph[toolName]; exists {
			for _, dep := range deps {
				if !visited[dep] {
					if hasCycle(dep) {
						return true
					}
				} else if recStack[dep] {
					return true
				}
			}
		}

		recStack[toolName] = false
		return false
	}

	for toolName := range graph {
		if !visited[toolName] {
			if hasCycle(toolName) {
				return fmt.Errorf("circular dependency detected in tool dependencies")
			}
		}
	}

	return nil
}

// groupSortedToolCallsByLayer groups tool call indices by execution layer. sorted must be in
// dependency order (e.g. from sortToolCallsByDependencies) so that each tool's dependencies
// appear earlier in the slice; tools in the same layer have no dependency on each other.
func (ae *AgentEngine) groupSortedToolCallsByLayer(sorted []types.ToolCall) [][]int {
	if len(sorted) == 0 {
		return nil
	}
	nameToIdx := make(map[string]int)
	toolRefs := make([]types.Tool, len(sorted))
	for i, tc := range sorted {
		name := tc.Function.Name
		nameToIdx[name] = i
		toolRefs[i], _ = ae.getToolByName(name)
	}
	depGraph := make(map[string][]string)
	for i, tool := range toolRefs {
		if tool == nil {
			continue
		}
		meta := tool.Metadata()
		if len(meta.Dependencies) == 0 {
			continue
		}
		deps := make([]string, 0, len(meta.Dependencies))
		for _, d := range meta.Dependencies {
			if _, in := nameToIdx[d]; in {
				deps = append(deps, d)
			}
		}
		if len(deps) > 0 {
			depGraph[sorted[i].Function.Name] = deps
		}
	}

	layerOf := make([]int, len(sorted))
	maxL := 0
	for i := range sorted {
		name := sorted[i].Function.Name
		deps := depGraph[name]
		maxDepLayer := -1
		for _, d := range deps {
			if j, ok := nameToIdx[d]; ok && j < i && layerOf[j] > maxDepLayer {
				maxDepLayer = layerOf[j]
			}
		}
		layerOf[i] = maxDepLayer + 1
		if layerOf[i] > maxL {
			maxL = layerOf[i]
		}
	}
	layers := make([][]int, maxL+1)
	for i, l := range layerOf {
		layers[l] = append(layers[l], i)
	}
	return layers
}
