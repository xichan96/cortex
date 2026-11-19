package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

func TestForkCloneExportImport(t *testing.T) {
	l, sourcePath := newTestLog(t)
	u1 := appendMsg(t, l, types.Message{Role: "user", Content: "one"})
	a1 := appendMsg(t, l, types.Message{Role: "assistant", Content: "two"})
	appendMsg(t, l, types.Message{Role: "user", Content: "abandoned"})
	appendMsg(t, l, types.Message{Role: "assistant", Content: "abandoned answer"})

	if err := l.Branch(u1); err != nil {
		t.Fatalf("branch: %v", err)
	}
	u1b := appendMsg(t, l, types.Message{Role: "user", Content: "alternate"})
	a1b := appendMsg(t, l, types.Message{Role: "assistant", Content: "alternate answer"})

	// Fork an earlier point: only root→a1 is copied.
	forkPath := filepath.Join(t.TempDir(), "fork.jsonl")
	forked, err := l.Fork(a1, forkPath)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	defer forked.Close()
	if forked.Len() != 2 {
		t.Fatalf("fork len = %d", forked.Len())
	}
	if got := forked.Header().ParentSession; got != sourcePath {
		t.Fatalf("parentSession = %q, want %q", got, sourcePath)
	}
	if forked.Header().CWD != l.Header().CWD {
		t.Fatalf("cwd = %q", forked.Header().CWD)
	}
	forkIDs := pathIDs(mustPath(t, forked, ""))
	if !reflect.DeepEqual(forkIDs, []string{u1, a1}) {
		t.Fatalf("fork path = %v", forkIDs)
	}

	// Clone copies the current (alternate) branch.
	clonePath := filepath.Join(t.TempDir(), "clone.jsonl")
	cloned, err := l.Clone(clonePath)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	defer cloned.Close()
	cloneIDs := pathIDs(mustPath(t, cloned, ""))
	if !reflect.DeepEqual(cloneIDs, []string{u1, u1b, a1b}) {
		t.Fatalf("clone path = %v", cloneIDs)
	}

	// Export the alternate branch and import it into a fresh file.
	var buf bytes.Buffer
	if err := l.ExportBranch(&buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	exported := splitLines(buf.String())
	if len(exported) != 4 { // header + 3 entries
		t.Fatalf("export lines = %d", len(exported))
	}
	var header Header
	if err := json.Unmarshal([]byte(exported[0]), &header); err != nil {
		t.Fatalf("export header: %v", err)
	}
	if header.ID != l.Header().ID {
		t.Fatalf("export header id = %q", header.ID)
	}

	importPath := filepath.Join(t.TempDir(), "imported.jsonl")
	imported, err := Import(strings.NewReader(buf.String()), importPath)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	defer imported.Close()
	if imported.Len() != 3 || imported.Leaf() != a1b {
		t.Fatalf("imported len=%d leaf=%q", imported.Len(), imported.Leaf())
	}
	if imported.Header().ID != header.ID {
		t.Fatalf("imported header id = %q", imported.Header().ID)
	}

	var reexport bytes.Buffer
	if err := imported.ExportBranch(&reexport); err != nil {
		t.Fatalf("re-export: %v", err)
	}
	assertJSONLinesEqual(t, exported, splitLines(reexport.String()))

	// Exports are branch-specific.
	var forkBuf bytes.Buffer
	if err := l.ExportBranchFrom(&forkBuf, a1); err != nil {
		t.Fatalf("export from: %v", err)
	}
	if len(splitLines(forkBuf.String())) != 3 {
		t.Fatalf("fork export = %d lines", len(splitLines(forkBuf.String())))
	}
	if err := l.ExportBranchFrom(&bytes.Buffer{}, "missing"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("export missing err = %v", err)
	}
}

func TestForkDoesNotOverwrite(t *testing.T) {
	l, _ := newTestLog(t)
	appendMsg(t, l, types.Message{Role: "user", Content: "x"})

	dst := filepath.Join(t.TempDir(), "taken.jsonl")
	if err := os.WriteFile(dst, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := l.Fork("", dst); !errors.Is(err, ErrExists) {
		t.Fatalf("fork err = %v", err)
	}
	valid := `{"type":"session","version":1,"id":"s","timestamp":"2025-01-01T00:00:00Z"}` + "\n"
	if _, err := Import(strings.NewReader(valid), dst); !errors.Is(err, ErrExists) {
		t.Fatalf("import err = %v", err)
	}
	if data, err := os.ReadFile(dst); err != nil || string(data) != "keep me" {
		t.Fatalf("destination was modified: %q, %v", data, err)
	}
}

func TestImportValidates(t *testing.T) {
	dir := t.TempDir()
	header := `{"type":"session","version":1,"id":"s","timestamp":"2025-01-01T00:00:00Z","cwd":"/tmp"}`

	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "dangling parent",
			content: header + "\n" +
				`{"type":"message","id":"m","parentId":"missing","timestamp":"2025-01-01T00:00:01Z","message":{"role":"user","content":"x"}}` + "\n",
			want: "missing",
		},
		{
			name: "duplicate id",
			content: header + "\n" +
				`{"type":"session_info","id":"m","parentId":null,"timestamp":"2025-01-01T00:00:01Z","name":"a"}` + "\n" +
				`{"type":"session_info","id":"m","parentId":null,"timestamp":"2025-01-01T00:00:02Z","name":"b"}` + "\n",
			want: "duplicate",
		},
		{
			name:    "corrupt line",
			content: header + "\n{not json\n",
			want:    "corrupt",
		},
		{
			name:    "unsupported version",
			content: `{"type":"session","version":2,"id":"s","timestamp":"2025-01-01T00:00:00Z"}` + "\n",
			want:    "version",
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := filepath.Join(dir, tc.name+".jsonl")
			_, err := Import(strings.NewReader(tc.content), dst)
			if err == nil {
				t.Fatalf("case %d: expected error", i)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
			if _, statErr := os.Stat(dst); statErr == nil {
				t.Fatal("destination must not be created when validation fails")
			}
		})
	}
}

func TestImportHeaderOnly(t *testing.T) {
	content := `{"type":"session","version":1,"id":"only","timestamp":"2025-01-01T00:00:00Z"}` + "\n"
	dst := filepath.Join(t.TempDir(), "only.jsonl")
	l, err := Import(strings.NewReader(content), dst)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	defer l.Close()
	if l.Len() != 0 || l.Leaf() != "" {
		t.Fatalf("len=%d leaf=%q", l.Len(), l.Leaf())
	}
}

func mustPath(t *testing.T, l *Log, id string) []Entry {
	t.Helper()
	path, err := l.PathTo(id)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	return path
}

func assertJSONLinesEqual(t *testing.T, want, got []string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("line counts differ: want %d got %d\nwant:\n%s\ngot:\n%s", len(want), len(got), strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
	for i := range want {
		var w, g any
		if err := json.Unmarshal([]byte(want[i]), &w); err != nil {
			t.Fatalf("want line %d: %v", i+1, err)
		}
		if err := json.Unmarshal([]byte(got[i]), &g); err != nil {
			t.Fatalf("got line %d: %v", i+1, err)
		}
		if !reflect.DeepEqual(w, g) {
			t.Errorf("line %d differs:\nwant %s\ngot  %s", i+1, want[i], got[i])
		}
	}
}
