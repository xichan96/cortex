package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/xichan96/cortex/agent/pipeline"
	"github.com/xichan96/cortex/agent/question"
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
	return ae.executeToolCallWithTimeout(ctx, tool, "", args, timeout)
}

// executeToolCallWithTimeout is executeToolWithTimeout with the model's
// toolCallID. When a pipeline is installed, the call runs through the pipeline
// stages; otherwise it falls back to the legacy direct execution path.
func (ae *AgentEngine) executeToolCallWithTimeout(ctx context.Context, tool types.Tool, toolCallID string, args map[string]any, timeout time.Duration) (any, error) {
	// Create context with timeout if specified
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Make the model's tool-call id visible to tools that need to correlate
	// side-channel interactions with the tool part (the question tool emits a
	// blocking request carrying this id).
	if toolCallID != "" {
		ctx = question.WithToolCallID(ctx, toolCallID)
	}

	p := ae.ToolPipeline()

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

		var val any
		var err error
		if p != nil {
			val, err = ae.executeViaPipeline(ctx, p, tool, toolCallID, args)
		} else {
			val, err = tool.Execute(ctx, args)
		}
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

// executeViaPipeline adapts a pipeline.Result back to the engine's tool-output
// contract. Recoverable errors become a non-fatal {ok:false} observation (like
// dino's nonFatalTool), so the model can recover; only fatal pipeline errors
// propagate to the caller.
func (ae *AgentEngine) executeViaPipeline(ctx context.Context, p *pipeline.Pipeline, tool types.Tool, toolCallID string, args map[string]any) (any, error) {
	if toolCallID == "" {
		toolCallID = uuid.NewString()
	}
	res, err := p.ExecuteOne(ctx, &pipeline.Call{
		ID:        toolCallID,
		Name:      tool.Name(),
		Args:      args,
		Tool:      tool,
		SessionID: ae.SessionID(),
	})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, fmt.Errorf("tool pipeline returned no result for %s", tool.Name())
	}
	if res.IsError {
		return map[string]any{
			"ok":    false,
			"tool":  tool.Name(),
			"error": res.Text(),
		}, nil
	}
	if text := res.Text(); text != "" {
		return text, nil
	}
	if res.Structured != nil {
		return res.Structured, nil
	}
	if res.Details != nil {
		return res.Details, nil
	}
	return "", nil
}

func (ae *AgentEngine) getToolTimeout(toolName string, args map[string]interface{}) time.Duration {
	cfg := ae.getConfig()
	if cfg == nil {
		return 0
	}
	if cfg.ToolTimeoutCalculator != nil {
		if timeout := cfg.ToolTimeoutCalculator(toolName, args); timeout > 0 {
			return timeout
		}
	}
	if toolTimeout, ok := cfg.ToolTimeouts[toolName]; ok {
		return toolTimeout
	}
	return cfg.ToolExecutionTimeout
}

// ==================== Tool Dependency Management Methods ====================

// sortToolCallsByDependencies orders calls for execution while preserving the
// model's original order. Claude Code returns tool results in the order the
// model emitted the tool_use blocks; cortex previously sorted by tool-name
// priority and keyed calls by tool name, which both reordered results and
// collapsed repeated calls in one batch (two Read calls became one).
//
// New behavior: dependency metadata is still honored (a call that depends on
// an earlier call runs after it); otherwise original order is kept, and calls
// with the same tool name stay distinct because everything is index-keyed.
func (ae *AgentEngine) sortToolCallsByDependencies(toolCalls []types.ToolCall) ([]types.ToolCall, error) {
	n := len(toolCalls)
	if n <= 1 {
		return toolCalls, nil
	}

	deps := make([][]int, n) // per-call dependency indices
	ae.mu.RLock()
	for i, tc := range toolCalls {
		tool, ok := ae.toolsMap[tc.Function.Name]
		if !ok {
			continue
		}
		for _, depName := range tool.Metadata().Dependencies {
			// Prefer the closest earlier call with that tool name; if the
			// dependency appears later in the batch, the DFS moves it before
			// this call (preserving the rest of the model's order).
			dep := -1
			for j := i - 1; j >= 0; j-- {
				if toolCalls[j].Function.Name == depName {
					dep = j
					break
				}
			}
			if dep < 0 {
				for j := i + 1; j < n; j++ {
					if toolCalls[j].Function.Name == depName {
						dep = j
						break
					}
				}
			}
			if dep >= 0 {
				deps[i] = append(deps[i], dep)
			}
		}
	}
	ae.mu.RUnlock()

	state := make([]uint8, n) // 0 unvisited, 1 in progress, 2 done
	out := make([]types.ToolCall, 0, n)
	var visit func(int) error
	visit = func(i int) error {
		switch state[i] {
		case 1:
			return fmt.Errorf("circular dependency detected involving tool: %s", toolCalls[i].Function.Name)
		case 2:
			return nil
		}
		state[i] = 1
		for _, d := range deps[i] {
			if err := visit(d); err != nil {
				return err
			}
		}
		state[i] = 2
		out = append(out, toolCalls[i])
		return nil
	}
	for i := 0; i < n; i++ {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// groupSortedToolCallsByLayer groups tool call indices by execution layer.
// sorted is in dependency order; calls with no dependency edges share a layer
// and may run concurrently. Index-keyed so duplicate tool names (e.g. two Read
// calls in one batch) are not collapsed.
func (ae *AgentEngine) groupSortedToolCallsByLayer(sorted []types.ToolCall) [][]int {
	n := len(sorted)
	if n == 0 {
		return nil
	}
	deps := make([][]int, n)
	ae.mu.RLock()
	for i, tc := range sorted {
		tool, ok := ae.toolsMap[tc.Function.Name]
		if !ok {
			continue
		}
		for _, depName := range tool.Metadata().Dependencies {
			for j := i - 1; j >= 0; j-- {
				if sorted[j].Function.Name == depName {
					deps[i] = append(deps[i], j)
					break
				}
			}
		}
		// sorted is already dependency-ordered, so every dependency is earlier.
	}
	ae.mu.RUnlock()

	layerOf := make([]int, n)
	maxL := 0
	for i := range sorted {
		maxDepLayer := -1
		for _, d := range deps[i] {
			if layerOf[d] > maxDepLayer {
				maxDepLayer = layerOf[d]
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
