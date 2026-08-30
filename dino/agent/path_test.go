package agent

import "testing"

func TestAgentPath_JoinParent(t *testing.T) {
	root := RootAgentPath()
	if root.String() != "/root" {
		t.Errorf("expected root path /root, got %q", root.String())
	}
	child := root.Join("general")
	if child.String() != "/root/general" {
		t.Errorf("expected /root/general, got %q", child.String())
	}
	parent, ok := child.Parent()
	if !ok || parent != root {
		t.Errorf("expected parent /root, got %q ok=%v", parent, ok)
	}
	if _, ok := root.Parent(); ok {
		t.Error("expected root to have no parent")
	}
}
