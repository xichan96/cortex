package shell

import (
	"context"
	"strings"
	"testing"
)

// TestBoundedBuffer_CapsOutput verifies boundedBuffer stops accumulating at the
// cap and appends a truncation marker.
func TestBoundedBuffer_CapsOutput(t *testing.T) {
	var b boundedBuffer
	b.max = 8
	payload := "hello world this is long"
	n, _ := b.Write([]byte(payload))
	if n != len(payload) {
		t.Fatalf("Write returned %d, want %d (all consumed)", n, len(payload))
	}
	if b.buf.Len() != 8 {
		t.Fatalf("buffer len = %d, want 8", b.buf.Len())
	}
	if !b.truncated {
		t.Fatal("truncated should be set")
	}
	s := b.String()
	if !strings.Contains(s, "... (output truncated)") {
		t.Errorf("marker missing: %q", s)
	}
	if !strings.HasPrefix(s, "hello wo") {
		t.Errorf("expected head kept, got %q", s)
	}
}

// TestBoundedBuffer_Unbounded verifies max=0 keeps legacy behavior.
func TestBoundedBuffer_Unbounded(t *testing.T) {
	var b boundedBuffer
	b.max = 0
	n, _ := b.Write([]byte("hello"))
	if n != 5 {
		t.Fatalf("Write returned %d", n)
	}
	if b.String() != "hello" {
		t.Errorf("got %q", b.String())
	}
	if b.truncated {
		t.Error("truncated should be false when unbounded")
	}
}

// TestShellMaxOutput verifies a shell with MaxOutputBytes caps Exec output
// without erroring the command: the head is kept, the tail is cut.
func TestShellMaxOutput(t *testing.T) {
	sh := NewShell(&Options{MaxOutputBytes: 16})
	stdout, _, err := sh.Exec(context.Background(), "echo 0123456789ABCDEF")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.HasPrefix(stdout, "0123456789ABCDEF") {
		t.Errorf("stdout head lost: %q", stdout)
	}
	if strings.Contains(stdout, "0123456789ABCDEF\n012") {
		t.Errorf("stdout not truncated at cap: %q", stdout)
	}
}

// TestShellUnboundedDefault verifies the default shell has no output cap.
func TestShellUnboundedDefault(t *testing.T) {
	sh := NewShell(nil)
	stdout, _, err := sh.Exec(context.Background(), "seq 1 50")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 50 {
		t.Errorf("got %d lines, want 50", len(lines))
	}
}
