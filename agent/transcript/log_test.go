package transcript

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

var testNow = func() time.Time {
	return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
}

func newTestLog(t *testing.T) (*Log, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	l, err := Open(path, OpenOptions{
		Create: true,
		Header: Header{ID: "sess-test", CWD: "/tmp/project"},
		Now:    testNow,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, path
}

func ptr(s string) *string { return &s }

func TestCreateAppendReopen(t *testing.T) {
	l, path := newTestLog(t)

	h := l.Header()
	if h.Type != HeaderType || h.Version != FormatVersion || h.ID != "sess-test" {
		t.Fatalf("header = %+v", h)
	}
	if !h.Timestamp.Equal(testNow()) {
		t.Fatalf("timestamp = %v", h.Timestamp)
	}

	u, err := l.AppendMessage(types.Message{Role: "user", Content: "hello"})
	if err != nil {
		t.Fatalf("append user: %v", err)
	}
	if len(u) != 8 {
		t.Fatalf("generated id %q is not 8 chars", u)
	}
	if e, _ := l.Get(u); e.Base().ParentID != nil {
		t.Fatalf("first entry parent = %v, want nil", *e.Base().ParentID)
	}

	a, err := l.AppendMessage(types.Message{Role: "assistant", Content: "hi"})
	if err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	if e, _ := l.Get(a); e.Base().ParentID == nil || *e.Base().ParentID != u {
		t.Fatalf("second entry parent = %v, want %s", e.Base().ParentID, u)
	}
	if l.Leaf() != a {
		t.Fatalf("leaf = %q, want %q", l.Leaf(), a)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ro, err := Open(path, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer ro.Close()
	if ro.Len() != 2 || ro.Leaf() != a {
		t.Fatalf("len=%d leaf=%q", ro.Len(), ro.Leaf())
	}
	pathEntries, err := ro.PathTo(a)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if len(pathEntries) != 2 || pathEntries[0].Base().ID != u {
		t.Fatalf("path = %v", pathEntries)
	}
	if _, err := ro.AppendMessage(types.Message{Role: "user"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("append on read-only err = %v", err)
	}
}

func TestBranchAndReplay(t *testing.T) {
	l, path := newTestLog(t)
	u1, _ := l.AppendMessage(types.Message{Role: "user", Content: "one"})
	a1, _ := l.AppendMessage(types.Message{Role: "assistant", Content: "two"})
	_, _ = l.AppendMessage(types.Message{Role: "user", Content: "three"})

	if err := l.Branch(u1); err != nil {
		t.Fatalf("branch: %v", err)
	}
	if l.Leaf() != u1 {
		t.Fatalf("leaf = %q", l.Leaf())
	}
	alt, err := l.AppendMessage(types.Message{Role: "user", Content: "alternate"})
	if err != nil {
		t.Fatalf("append alt: %v", err)
	}
	if e, _ := l.Get(alt); e.Base().ParentID == nil || *e.Base().ParentID != u1 {
		t.Fatalf("alt parent = %v", e.Base().ParentID)
	}

	original, err := l.PathTo(a1)
	if err != nil || len(original) != 2 {
		t.Fatalf("original path = %v, err %v", original, err)
	}
	if err := l.Branch("missing"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("branch missing err = %v", err)
	}

	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	ro, err := Open(path, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer ro.Close()
	if ro.Len() != 4 {
		t.Fatalf("len = %d, want 4", ro.Len())
	}
	if ro.Leaf() != alt {
		t.Fatalf("replayed leaf = %q, want %q", ro.Leaf(), alt)
	}
	replayed, err := ro.PathTo(alt)
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if len(replayed) != 2 || replayed[0].Base().ID != u1 {
		t.Fatalf("replayed path = %v", replayed)
	}
}

func TestCorruptLineDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.jsonl")
	content := strings.Join([]string{
		`{"type":"session","version":1,"id":"s","timestamp":"2025-01-01T00:00:00Z","cwd":"/tmp"}`,
		`{"type":"message","id":"m1","parentId":null,"timestamp":"2025-01-01T00:00:01Z","message":{"role":"user","content":"ok"}}`,
		`{not json`,
		`{"type":"message","id":"m2","parentId":"m1","timestamp":"2025-01-01T00:00:02Z","message":{"role":"assistant","content":"still here"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	l, err := Open(path, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer l.Close()

	if l.Len() != 2 || l.Leaf() != "m2" {
		t.Fatalf("len=%d leaf=%q", l.Len(), l.Leaf())
	}
	diags := l.Diagnostics()
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %v", diags)
	}
	if diags[0].Line != 3 {
		t.Fatalf("diagnostic line = %d, want 3", diags[0].Line)
	}
	if !strings.Contains(diags[0].Raw, "not json") {
		t.Fatalf("raw = %q", diags[0].Raw)
	}
	if !strings.Contains(diags[0].Message, "invalid character") {
		t.Fatalf("message = %q", diags[0].Message)
	}
}

func TestTornTailIsRepairedAndDiagnosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "torn.jsonl")
	content := `{"type":"session","version":1,"id":"s","timestamp":"2025-01-01T00:00:00Z","cwd":"/tmp"}
{"type":"message","id":"m1","parentId":null,"timestamp":"2025-01-01T00:00:01Z","message":{"role":"user","content":"ok"}}
{"type":"message","id":"bad"`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	l, err := Open(path, OpenOptions{Now: testNow})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(l.Diagnostics()) != 1 || l.Diagnostics()[0].Line != 3 {
		t.Fatalf("diagnostics = %v", l.Diagnostics())
	}
	appended, err := l.AppendMessage(types.Message{Role: "assistant", Content: "after repair"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if e, _ := l.Get(appended); e.Base().ParentID == nil || *e.Base().ParentID != "m1" {
		t.Fatalf("parent = %v, want m1", e.Base().ParentID)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ro, err := Open(path, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer ro.Close()
	if ro.Len() != 2 {
		t.Fatalf("len = %d, want 2", ro.Len())
	}
	if len(ro.Diagnostics()) != 1 || ro.Diagnostics()[0].Line != 3 {
		t.Fatalf("reopen diagnostics = %v", ro.Diagnostics())
	}
	if ro.Leaf() != appended {
		t.Fatalf("leaf = %q, want %q", ro.Leaf(), appended)
	}
}

func TestConcurrentWriterLock(t *testing.T) {
	l, path := newTestLog(t)
	if _, err := Open(path, OpenOptions{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("second writer err = %v, want ErrLocked", err)
	}

	ro, err := Open(path, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("read-only open must succeed: %v", err)
	}
	_ = ro.Close()

	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	l2, err := Open(path, OpenOptions{Now: testNow})
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	_ = l2.Close()
}

func TestUnsupportedVersions(t *testing.T) {
	dir := t.TempDir()
	write := func(name, line string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(line+"\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		return p
	}

	old := write("old.jsonl", `{"type":"session","version":2,"id":"s","timestamp":"2025-01-01T00:00:00Z"}`)
	if _, err := Open(old, OpenOptions{}); err == nil {
		t.Fatal("expected version error")
	} else {
		var verr *VersionError
		if !errors.As(err, &verr) || verr.Version != 2 {
			t.Fatalf("err = %v (%T)", err, err)
		}
	}

	noVersion := write("noversion.jsonl", `{"type":"session","id":"s","timestamp":"2025-01-01T00:00:00Z"}`)
	if _, err := Open(noVersion, OpenOptions{}); err == nil {
		t.Fatal("expected version error")
	} else {
		var verr *VersionError
		if !errors.As(err, &verr) || verr.Version != 0 {
			t.Fatalf("err = %v (%T)", err, err)
		}
	}

	wrongType := write("wrongtype.jsonl", `{"type":"not-session","version":1,"id":"s","timestamp":"2025-01-01T00:00:00Z"}`)
	if _, err := Open(wrongType, OpenOptions{}); !errors.Is(err, ErrMalformedHeader) {
		t.Fatalf("err = %v", err)
	}

	noHeader := write("noheader.jsonl", `{"type":"message","id":"m","parentId":null,"timestamp":"2025-01-01T00:00:00Z","message":{"role":"user","content":"x"}}`)
	if _, err := Open(noHeader, OpenOptions{}); !errors.Is(err, ErrMalformedHeader) {
		t.Fatalf("err = %v", err)
	}

	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Open(empty, OpenOptions{}); !errors.Is(err, ErrMalformedHeader) {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateRejectsUnsupportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.jsonl")
	_, err := Open(path, OpenOptions{Create: true, Header: Header{ID: "s", Version: 9}})
	var verr *VersionError
	if !errors.As(err, &verr) || verr.Version != 9 {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(path); statErr == nil {
		t.Fatal("file should not exist after failed create")
	}
}

func TestAppendParentNotFound(t *testing.T) {
	l, _ := newTestLog(t)
	_, err := l.Append(&MessageEntry{
		EntryBase: EntryBase{ID: "x", ParentID: ptr("missing")},
		Message:   types.Message{Role: "user", Content: "x"},
	})
	if !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("err = %v", err)
	}
	if l.Len() != 0 {
		t.Fatalf("len = %d, want 0", l.Len())
	}
}

func TestAppendDuplicateID(t *testing.T) {
	l, _ := newTestLog(t)
	if _, err := l.Append(&SessionInfoEntry{EntryBase: EntryBase{ID: "dup"}, Name: "a"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := l.Append(&SessionInfoEntry{EntryBase: EntryBase{ID: "dup"}, Name: "b"}); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestAppendFailureDoesNotUpdateMemory(t *testing.T) {
	l, _ := newTestLog(t)
	first, err := l.AppendMessage(types.Message{Role: "user", Content: "ok"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	// A nil content block cannot be encoded; the append must fail before the
	// in-memory index advances.
	bad := &MessageEntry{Message: types.Message{Role: "user", Parts: []types.MessagePart{nil}}}
	if _, err := l.Append(bad); err == nil {
		t.Fatal("expected marshal error")
	}
	if l.Len() != 1 || l.Leaf() != first {
		t.Fatalf("len=%d leaf=%q", l.Len(), l.Leaf())
	}
	if bad.Base().ID != "" {
		t.Fatalf("failed entry retained id %q", bad.Base().ID)
	}
}
