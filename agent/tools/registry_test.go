package tools

import (
	"context"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

// exposureMockTool is a minimal types.Tool carrying an explicit exposure.
type exposureMockTool struct {
	name     string
	exposure types.ToolExposure
}

func (t *exposureMockTool) Name() string                    { return t.name }
func (t *exposureMockTool) Description() string             { return "mock " + t.name }
func (t *exposureMockTool) Schema() map[string]interface{}  { return map[string]interface{}{} }
func (t *exposureMockTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	return nil, nil
}
func (t *exposureMockTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{SourceNodeName: t.name, Exposure: t.exposure}
}

func TestExposure_DefaultIsDirect(t *testing.T) {
	// Empty exposure must behave as Direct (zero migration for existing tools).
	if !types.ToolExposure("").IsDirect() {
		t.Fatal("empty exposure must be Direct")
	}
	if types.ToolExposure("").IsDeferred() || types.ToolExposure("").IsHidden() {
		t.Fatal("empty exposure must not be deferred/hidden")
	}
	if !types.ExposureDirect.IsDirect() || types.ExposureDirect.IsDeferred() {
		t.Fatal("ExposureDirect must be Direct only")
	}
	if !types.ExposureDeferred.IsDeferred() || types.ExposureDeferred.IsDirect() {
		t.Fatal("ExposureDeferred must be Deferred only")
	}
	if !types.ExposureHidden.IsHidden() || types.ExposureHidden.IsDirect() {
		t.Fatal("ExposureHidden must be Hidden only")
	}
}

func TestRegistry_GetAllVisible_FiltersExposure(t *testing.T) {
	reg := NewRegistry()
	must := func(tool types.Tool) {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name(), err)
		}
	}
	must(&exposureMockTool{name: "a_direct"})                                    // default exposure -> direct
	must(&exposureMockTool{name: "b_direct", exposure: types.ExposureDirect})    // explicit direct
	must(&exposureMockTool{name: "c_deferred", exposure: types.ExposureDeferred}) // deferred
	must(&exposureMockTool{name: "d_hidden", exposure: types.ExposureHidden})    // hidden

	visible := reg.GetAllVisible()
	if len(visible) != 2 {
		t.Fatalf("GetAllVisible: want 2 direct tools, got %d (%v)", len(visible), names(visible))
	}
	for _, name := range []string{"a_direct", "b_direct"} {
		if !contains(visible, name) {
			t.Errorf("GetAllVisible missing direct tool %s", name)
		}
	}

	deferred := reg.GetDeferred()
	if len(deferred) != 1 || deferred[0].Name() != "c_deferred" {
		t.Fatalf("GetDeferred: want [c_deferred], got %v", names(deferred))
	}

	if _, err := reg.GetDeferredTool("c_deferred"); err != nil {
		t.Fatalf("GetDeferredTool(c_deferred): %v", err)
	}
	if _, err := reg.GetDeferredTool("a_direct"); err == nil {
		t.Fatal("GetDeferredTool(a_direct) must error (not deferred)")
	}
	if _, err := reg.GetDeferredTool("missing"); err == nil {
		t.Fatal("GetDeferredTool(missing) must error")
	}

	// GetAll semantics unchanged: returns every registered tool (subagent/debug path).
	all := reg.GetAll()
	if len(all) != 4 {
		t.Fatalf("GetAll: want 4 tools, got %d (%v)", len(all), names(all))
	}
}

func names(tools []types.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name())
	}
	return out
}

func contains(tools []types.Tool, name string) bool {
	for _, t := range tools {
		if t.Name() == name {
			return true
		}
	}
	return false
}
