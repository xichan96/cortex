package dino

import (
	"testing"

	dinoTools "github.com/xichan96/cortex/dino/tools"
)

// TestLoadBuiltinToolsRegistersAlignedNames verifies the factory exposes the
// Claude Code tool names and no longer registers list_directory.
func TestLoadBuiltinToolsRegistersAlignedNames(t *testing.T) {
	reg := dinoTools.NewRegistry()
	if err := loadBuiltinTools(reg, t.TempDir()); err != nil {
		t.Fatalf("loadBuiltinTools: %v", err)
	}
	for _, name := range []string{"Bash", "Grep", "Glob", "WebFetch", "WebSearch"} {
		if !reg.Has(name) {
			t.Errorf("registry missing %q", name)
		}
	}
	if reg.Has("list_directory") {
		t.Error("list_directory must not be registered")
	}
	for _, old := range []string{"bash", "grep", "glob", "web_fetch", "web_search"} {
		if reg.Has(old) {
			t.Errorf("legacy tool name %q must not be registered", old)
		}
	}
}
