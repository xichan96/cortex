package sandbox_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xichan96/cortex/dino/sandbox"
)

func TestHostSandboxReadDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	sb := newHost(t, sandbox.HostOptions{Root: root})

	entries, err := sb.ReadDir(context.Background(), ".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	got := make(map[string]sandbox.FileInfo, len(entries))
	for _, entry := range entries {
		got[entry.Name] = entry
	}
	if info, ok := got["a.txt"]; !ok || info.IsDir || info.Size != 2 {
		t.Fatalf("a.txt entry = %+v, ok=%v", info, ok)
	}
	if info, ok := got["sub"]; !ok || !info.IsDir {
		t.Fatalf("sub entry = %+v, ok=%v", info, ok)
	}
}

func TestHostSandboxReadDirRejectsEscape(t *testing.T) {
	sb := newHost(t, sandbox.HostOptions{Root: t.TempDir()})
	_, err := sb.ReadDir(context.Background(), "..")
	if !errors.Is(err, sandbox.ErrPathEscape) {
		t.Fatalf("ReadDir(..) error = %v, want ErrPathEscape", err)
	}
}

func TestHostSandboxImplementsDirLister(t *testing.T) {
	var _ sandbox.DirLister = newHost(t, sandbox.HostOptions{Root: t.TempDir()})
}

func TestParseBackend(t *testing.T) {
	tests := []struct {
		in      string
		want    sandbox.Backend
		wantErr bool
	}{
		{"host", sandbox.BackendHost, false},
		{" Docker ", sandbox.BackendDocker, false},
		{"SSH", sandbox.BackendSSH, false},
		{"landlock", sandbox.BackendLandlock, false},
		{"", "", true},
		{"firecracker", "", true},
	}
	for _, tc := range tests {
		got, err := sandbox.ParseBackend(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseBackend(%q) = %q, want error", tc.in, got)
			} else if !errors.Is(err, sandbox.ErrInvalidSpec) {
				t.Errorf("ParseBackend(%q) error = %v, want ErrInvalidSpec", tc.in, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseBackend(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseBackend(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
