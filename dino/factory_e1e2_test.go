package dino

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/xichan96/cortex/agent/types"
	dinoTools "github.com/xichan96/cortex/dino/tools"
)

// deferredMockTool is a Deferred tool with a stable name, for discover tests.
type deferredMockTool struct {
	name string
}

func (t *deferredMockTool) Name() string { return t.name }
func (t *deferredMockTool) Description() string {
	return fmt.Sprintf("deferred mock %s", t.name)
}
func (t *deferredMockTool) Schema() map[string]interface{} { return map[string]interface{}{} }
func (t *deferredMockTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (t *deferredMockTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{SourceNodeName: t.name, ToolType: "mock", Exposure: types.ExposureDeferred}
}

// newDiscoverFactory builds a dinoFactory with the deferred caches and config
// needed by discoverTool, and pre-loads the given deferred tools (wrapped as-is).
func newDiscoverFactory(maxDiscovered int, deferredNames ...string) *dinoFactory {
	cfg := DefaultConfig()
	cfg.Tools.MaxDiscoveredTools = maxDiscovered

	f := &dinoFactory{
		config:               cfg,
		sessionDeferredTools:   make(map[string]map[string]types.Tool),
		sessionDiscoveredTools: make(map[string][]string),
	}
	if len(deferredNames) > 0 {
		f.sessionDeferredTools["s"] = make(map[string]types.Tool)
		for _, n := range deferredNames {
			f.sessionDeferredTools["s"][n] = &deferredMockTool{name: n}
		}
	}
	return f
}

func TestDiscoverTool_InjectsOnceThenIdempotent(t *testing.T) {
	f := newDiscoverFactory(0, "mcp_a", "mcp_b")

	var injected []string
	inject := func(_ context.Context, tools []types.Tool) {
		for _, tl := range tools {
			injected = append(injected, tl.Name())
		}
	}

	// First discovery succeeds.
	if err := f.discoverTool(context.Background(), "s", "mcp_a", inject); err != nil {
		t.Fatalf("first discover: %v", err)
	}
	if len(injected) != 1 || injected[0] != "mcp_a" {
		t.Fatalf("want exactly mcp_a injected, got %v", injected)
	}
	discovered := f.sessionDiscoveredTools["s"]
	if len(discovered) != 1 || discovered[0] != "mcp_a" {
		t.Fatalf("sessionDiscoveredTools: want [mcp_a], got %v", discovered)
	}

	// Second discovery of the same name is a no-op (idempotent).
	if err := f.discoverTool(context.Background(), "s", "mcp_a", inject); err == nil {
		t.Fatal("re-discovering an already-injected tool must return an error")
	}
	if len(injected) != 1 {
		t.Fatalf("idempotent: injected must stay 1, got %d", len(injected))
	}

	// A different tool still works.
	if err := f.discoverTool(context.Background(), "s", "mcp_b", inject); err != nil {
		t.Fatalf("second tool discover: %v", err)
	}
	if len(injected) != 2 {
		t.Fatalf("want 2 injected total, got %d", len(injected))
	}
}

func TestDiscoverTool_CapLimit(t *testing.T) {
	f := newDiscoverFactory(1, "mcp_a", "mcp_b")

	var injected []string
	inject := func(_ context.Context, tools []types.Tool) {
		for _, tl := range tools {
			injected = append(injected, tl.Name())
		}
	}

	if err := f.discoverTool(context.Background(), "s", "mcp_a", inject); err != nil {
		t.Fatalf("discover mcp_a: %v", err)
	}
	// Cap reached: mcp_b refused.
	if err := f.discoverTool(context.Background(), "s", "mcp_b", inject); err == nil {
		t.Fatal("discover beyond cap must be refused")
	}
	if len(injected) != 1 {
		t.Fatalf("cap: want 1 injected, got %d", len(injected))
	}
	// mcp_b remains discoverable in the deferred cache (not consumed).
	if _, ok := f.sessionDeferredTools["s"]["mcp_b"]; !ok {
		t.Fatal("mcp_b must stay in deferred cache after cap refusal")
	}
}

func TestDiscoverTool_ConcurrentParallel(t *testing.T) {
	// 20 deferred tools, 50 parallel discover calls hammering distinct names
	// plus repeats. discoverTool must be race-free (run with -race) and each
	// name injected exactly once.
	const n = 20
	names := make([]string, 0, n)
	for i := 0; i < n; i++ {
		names = append(names, fmt.Sprintf("mcp_%02d", i))
	}
	f := newDiscoverFactory(0, names...)

	var mu sync.Mutex
	injected := make(map[string]int)
	inject := func(_ context.Context, tools []types.Tool) {
		mu.Lock()
		defer mu.Unlock()
		for _, tl := range tools {
			injected[tl.Name()]++
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := names[i%n]
			_ = f.discoverTool(context.Background(), "s", name, inject)
		}(i)
	}
	wg.Wait()

	if len(injected) != n {
		t.Fatalf("want %d tools injected exactly once, got %d", n, len(injected))
	}
	for name, count := range injected {
		if count != 1 {
			t.Errorf("tool %s injected %d times, want 1", name, count)
		}
	}
	if len(f.sessionDiscoveredTools["s"]) != n {
		t.Fatalf("sessionDiscoveredTools: want %d, got %d", n, len(f.sessionDiscoveredTools["s"]))
	}
}

func TestDeferredMCPTool_SetsDeferredExposure(t *testing.T) {
	// A plain (Direct-exposure) tool — mirrors a real *pkg/mcp.MCPTool.
	raw := &plainMockTool{name: "mcp_x"}
	wrapped := dinoTools.NewDeferredMCPTool(raw)
	if !wrapped.Metadata().Exposure.IsDeferred() {
		t.Fatal("deferredMCPTool must report ExposureDeferred")
	}
	if wrapped.Name() != "mcp_x" {
		t.Fatalf("name must forward, got %s", wrapped.Name())
	}
	// The wrapper must not mutate the shared original.
	if !raw.Metadata().Exposure.IsDirect() {
		t.Fatal("original tool exposure must be untouched")
	}
}

// plainMockTool has default (Direct) exposure.
type plainMockTool struct{ name string }

func (t *plainMockTool) Name() string { return t.name }
func (t *plainMockTool) Description() string {
	return fmt.Sprintf("plain mock %s", t.name)
}
func (t *plainMockTool) Schema() map[string]interface{} { return map[string]interface{}{} }
func (t *plainMockTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (t *plainMockTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{SourceNodeName: t.name, ToolType: "mock"}
}

func TestToolSearchEnabledFlag_ExcludesDeferredFromVisible(t *testing.T) {
	reg := dinoTools.NewRegistry()
	if err := reg.Register(&deferredMockTool{name: "hidden_deferred"}); err != nil {
		t.Fatal(err)
	}
	if len(reg.GetAllVisible()) != 0 {
		t.Fatalf("GetAllVisible must exclude deferred, got %v", reg.GetAllVisible())
	}
	if len(reg.GetDeferred()) != 1 {
		t.Fatalf("GetDeferred must include deferred, got %d", len(reg.GetDeferred()))
	}
}
