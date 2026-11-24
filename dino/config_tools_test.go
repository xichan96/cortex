package dino

import "testing"

// TestDefaultConfig_UsesClaudeFilesystemToolNames guards the fs tool rename:
// the default allow-list, approval list and timeouts must reference the new
// names, or the renamed builtins become invisible/unapproved at runtime.
func TestDefaultConfig_UsesClaudeFilesystemToolNames(t *testing.T) {
	cfg := DefaultConfig()

	for _, name := range []string{"Read", "Write", "Edit"} {
		if !containsToolName(cfg.Tools.Allowed, name) {
			t.Errorf("Tools.Allowed missing %q", name)
		}
		if _, ok := cfg.ToolTimeouts[name]; !ok {
			t.Errorf("ToolTimeouts missing %q", name)
		}
	}
	for _, name := range []string{"Write", "Edit"} {
		if !containsToolName(cfg.Tools.ApprovalRequired, name) {
			t.Errorf("Tools.ApprovalRequired missing %q", name)
		}
	}
	for _, old := range []string{"read_file", "write_file", "edit_file"} {
		if containsToolName(cfg.Tools.Allowed, old) {
			t.Errorf("Tools.Allowed still contains legacy name %q", old)
		}
		if _, ok := cfg.ToolTimeouts[old]; ok {
			t.Errorf("ToolTimeouts still contains legacy name %q", old)
		}
	}
}

func containsToolName(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}
