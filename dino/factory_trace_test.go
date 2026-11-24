package dino

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/session"
)

// traceFilePath mirrors dino/trace's on-disk naming (trace-<safe session id>.jsonl).
func traceFilePath(dir, sessionID string) string {
	return filepath.Join(dir, "trace-"+strings.ReplaceAll(sessionID, "/", "_")+".jsonl")
}

// hasTraceRecorder reports whether the factory still holds a trace recorder for
// the session. traceMu is a leaf lock; the test must not hold it while calling
// other factory methods.
func hasTraceRecorder(f *dinoFactory, sessionID string) bool {
	f.traceMu.Lock()
	defer f.traceMu.Unlock()
	_, ok := f.sessionTracers[sessionID]
	return ok
}

// createSessionWithin calls CreateSession in a goroutine and fails the test if
// it does not return before the deadline. This is the regression guard for the
// Trace.Enabled=true self-deadlock: the factory used to re-lock f.mu (already
// held by CreateSession) while wiring the recorder, so the call blocked forever.
func createSessionWithin(t *testing.T, f DinoFactory, ctx context.Context, sessionID string) (*session.Session, error) {
	t.Helper()
	type result struct {
		sess *session.Session
		err  error
	}
	done := make(chan result, 1)
	go func() {
		s, err := f.CreateSession(ctx, sessionID)
		done <- result{sess: s, err: err}
	}()

	select {
	case res := <-done:
		return res.sess, res.err
	case <-time.After(2 * time.Second):
		t.Fatalf("CreateSession(%q) blocked for >2s (possible f.mu self-deadlock)", sessionID)
		return nil, nil
	}
}

// runOneTurn sends one message and waits for the session's done event.
func runOneTurn(t *testing.T, sess *session.Session) {
	t.Helper()
	done := make(chan struct{}, 1)
	sess.Subscribe(session.ObserverFunc(func(ev *session.Event) {
		if ev != nil && ev.IsDone() {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}))
	sess.Input() <- types.NewAgentInput("hello")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not complete within 5s")
	}
}

func readTrace(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace %s: %v", path, err)
	}
	return string(data)
}

// TestCreateSessionTraceDoesNotDeadlock is the P0 regression test: with
// Trace.Enabled=true, CreateSession must return promptly instead of
// self-deadlocking on f.mu, register exactly one recorder, create the trace
// file, and let CloseSession drain + fsync + close it (turn_start readable).
func TestCreateSessionTraceDoesNotDeadlock(t *testing.T) {
	dir := t.TempDir()
	cfg := getTestConfig()
	cfg.Trace.Enabled = true
	cfg.Trace.Dir = dir

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	const sessionID = "trace-deadlock"
	sess, err := createSessionWithin(t, factory, context.Background(), sessionID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess == nil {
		t.Fatal("CreateSession returned a nil session")
	}

	df := factory.(*dinoFactory)
	if !hasTraceRecorder(df, sessionID) {
		t.Fatal("Trace.Enabled=true but no recorder registered for session")
	}

	path := traceFilePath(dir, sessionID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("trace file was not created: %v", err)
	}

	runOneTurn(t, sess)

	// CloseSession owns the recorder lifecycle: it must unregister it and
	// make the queued events durable on disk.
	factory.CloseSession(sessionID)
	if hasTraceRecorder(df, sessionID) {
		t.Error("recorder still registered after CloseSession")
	}

	if got := readTrace(t, path); !strings.Contains(got, `"turn_start"`) {
		t.Fatalf("trace after CloseSession does not contain turn_start:\n%s", got)
	}
}

// TestShutdownDrainsTraceRecorder verifies the other lifecycle owner: Shutdown
// (without CloseSession) must also drain + fsync + close every recorder.
func TestShutdownDrainsTraceRecorder(t *testing.T) {
	dir := t.TempDir()
	cfg := getTestConfig()
	cfg.Trace.Enabled = true
	cfg.Trace.Dir = dir

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}

	const sessionID = "trace-shutdown"
	sess, err := createSessionWithin(t, factory, context.Background(), sessionID)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	runOneTurn(t, sess)

	// No CloseSession: Shutdown itself must flush the recorder.
	if err := factory.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	df := factory.(*dinoFactory)
	if hasTraceRecorder(df, sessionID) {
		t.Error("recorder still registered after Shutdown")
	}
	if got := readTrace(t, traceFilePath(dir, sessionID)); !strings.Contains(got, `"turn_start"`) {
		t.Fatalf("trace after Shutdown does not contain turn_start:\n%s", got)
	}
}

// TestTraceDisabledCreatesNoRecorder pins the zero-overhead path: with
// Trace.Enabled=false no recorder is constructed and no trace file is written.
func TestTraceDisabledCreatesNoRecorder(t *testing.T) {
	dir := t.TempDir()
	cfg := getTestConfig()
	cfg.Trace.Enabled = false
	cfg.Trace.Dir = dir

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	const sessionID = "trace-disabled"
	if _, err := createSessionWithin(t, factory, context.Background(), sessionID); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	df := factory.(*dinoFactory)
	if hasTraceRecorder(df, sessionID) {
		t.Error("Trace.Enabled=false but a recorder was registered")
	}
	matches, err := filepath.Glob(filepath.Join(dir, "trace-*.jsonl"))
	if err != nil {
		t.Fatalf("glob trace files: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("Trace.Enabled=false but trace files exist: %v", matches)
	}
}
