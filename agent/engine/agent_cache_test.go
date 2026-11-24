package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/types"
)

// metaTool is a MockTool with configurable metadata.
type metaTool struct {
	*MockTool
	meta types.ToolMetadata
}

func (t *metaTool) Metadata() types.ToolMetadata { return t.meta }

func TestInvalidateToolCache(t *testing.T) {
	eng := NewAgentEngine(NewMockLLMProvider(), types.NewAgentConfig())
	args := map[string]any{"x": "1"}

	eng.setCachedToolResult("tool_a", args, "cached-a", nil)
	if _, _, ok := eng.getCachedToolResult("tool_a", args); !ok {
		t.Fatal("expected cache hit before invalidation")
	}

	eng.InvalidateToolCache()

	if _, _, ok := eng.getCachedToolResult("tool_a", args); ok {
		t.Fatal("expected cache miss after invalidation")
	}
	eng.toolCacheMu.RLock()
	n := len(eng.toolCache)
	head, tail := eng.toolCacheHead, eng.toolCacheTail
	eng.toolCacheMu.RUnlock()
	if n != 0 || head != nil || tail != nil {
		t.Fatalf("cache not fully cleared: size=%d head=%v tail=%v", n, head, tail)
	}
}

func TestInvalidateToolCacheConcurrent(t *testing.T) {
	eng := NewAgentEngine(NewMockLLMProvider(), types.NewAgentConfig())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				args := map[string]any{"i": i, "j": j}
				eng.setCachedToolResult("tool", args, j, nil)
				eng.getCachedToolResult("tool", args)
				eng.InvalidateToolCache()
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			eng.InvalidateToolCache()
		}
	}()
	wg.Wait()
}

// TestInvalidatingToolClearsCache drives the engine end-to-end: a cacheable
// tool runs, an invalidating tool runs, and the repeated cacheable call must
// execute again instead of serving the stale cached result.
func TestInvalidatingToolClearsCache(t *testing.T) {
	provider := NewMockLLMProvider()
	call := func(name string) types.Message {
		return types.Message{
			Role: "assistant",
			ToolCalls: []types.ToolCall{{
				ID:       "call_" + name,
				Type:     "function",
				Function: types.ToolFunction{Name: name, Arguments: map[string]interface{}{"x": float64(1)}},
			}},
		}
	}
	provider.AddResponse(call("cacheable_tool"))
	provider.AddResponse(call("invalidating_tool"))
	provider.AddResponse(call("cacheable_tool"))
	provider.AddResponse(types.Message{Role: "assistant", Content: "done"})

	config := types.NewAgentConfig()
	config.MaxIterations = 5
	eng := NewAgentEngine(provider, config)

	var runs int32
	cacheable := &metaTool{MockTool: NewMockTool("cacheable_tool"), meta: types.ToolMetadata{}}
	cacheable.WithExecuteFunc(func(ctx context.Context, input map[string]interface{}) (interface{}, error) {
		return fmt.Sprintf("run-%d", atomic.AddInt32(&runs, 1)), nil
	})
	invalidating := &metaTool{
		MockTool: NewMockTool("invalidating_tool"),
		meta: types.ToolMetadata{Extra: map[string]interface{}{
			"no_cache":          true,
			"invalidates_cache": true,
		}},
	}
	eng.AddTool(context.Background(), cacheable)
	eng.AddTool(context.Background(), invalidating)

	if _, err := eng.Execute(context.Background(), types.NewAgentInput("go"), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := atomic.LoadInt32(&runs); got != 2 {
		t.Fatalf("cacheable tool ran %d times, want 2 (cache must be invalidated)", got)
	}
}

// TestReadAfterWriteThroughEngine is the end-to-end regression for P0-2: a Read
// after a Write must observe the new file content, and the repeated Read must
// not be served from cache.
func TestReadAfterWriteThroughEngine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("return a - b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := NewMockLLMProvider()
	toolCall := func(id, name string, args map[string]interface{}) types.Message {
		return types.Message{
			Role: "assistant",
			ToolCalls: []types.ToolCall{{
				ID:       id,
				Type:     "function",
				Function: types.ToolFunction{Name: name, Arguments: args},
			}},
		}
	}
	provider.AddResponse(toolCall("c1", "Read", map[string]interface{}{"file_path": path}))
	provider.AddResponse(toolCall("c2", "Write", map[string]interface{}{"file_path": path, "content": "return a + b\n"}))
	provider.AddResponse(toolCall("c3", "Read", map[string]interface{}{"file_path": path}))
	provider.AddResponse(types.Message{Role: "assistant", Content: "done"})

	config := types.NewAgentConfig()
	config.MaxIterations = 5
	eng := NewAgentEngine(provider, config)
	eng.AddTools(context.Background(), []types.Tool{fs.NewReadTool(dir), fs.NewWriteTool(dir)})

	rec := &recordingToolCallback{}
	eng.SetToolCallback(context.Background(), rec)

	if _, err := eng.Execute(context.Background(), types.NewAgentInput("fix it"), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	observations := rec.results()
	if len(observations) != 3 {
		t.Fatalf("got %d tool results, want 3: %v", len(observations), observations)
	}
	if !strings.Contains(observations[0], "return a - b") {
		t.Fatalf("first Read = %q, want old content", observations[0])
	}
	if strings.Contains(observations[0], "return a + b") {
		t.Fatalf("first Read unexpectedly has new content: %q", observations[0])
	}
	if !strings.Contains(observations[2], "return a + b") {
		t.Fatalf("second Read = %q, want fresh content", observations[2])
	}
	if strings.Contains(observations[2], "return a - b") {
		t.Fatalf("second Read served stale cached content: %q", observations[2])
	}
}

// recordingToolCallback captures tool outputs in arrival order.
type recordingToolCallback struct {
	mu      sync.Mutex
	outputs []string
}

func (c *recordingToolCallback) OnToolCall(string, string, map[string]interface{})       {}
func (c *recordingToolCallback) OnToolInputStart(string, string, map[string]interface{}) {}
func (c *recordingToolCallback) OnToolInputEnd(string, string, map[string]interface{})   {}
func (c *recordingToolCallback) OnToolError(string, string, error)                       {}

func (c *recordingToolCallback) OnToolResult(_ string, _ string, output interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outputs = append(c.outputs, fmt.Sprint(output))
}

func (c *recordingToolCallback) results() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.outputs))
	copy(out, c.outputs)
	return out
}
