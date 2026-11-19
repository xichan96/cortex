package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type rawRecord struct {
	Type    string          `json:"type"`
	Event   *Event          `json:"event,omitempty"`
	ID      string          `json:"id,omitempty"`
	Command string          `json:"command,omitempty"`
	Success bool            `json:"success,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

type rawConn struct {
	toServer   *io.PipeWriter
	fromServer *io.PipeReader
	lines      chan []byte
	done       chan error
}

func startRaw(t *testing.T, rt Runtime) *rawConn {
	t.Helper()
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := Server(rt, serverIn, serverOut)
		_ = serverOut.Close()
		done <- err
	}()

	lines := make(chan []byte, 1024)
	stop := make(chan struct{})
	go func() {
		reader := bufio.NewReader(clientIn)
		for {
			line, err := readJSONLine(reader)
			if len(line) > 0 {
				select {
				case lines <- line:
				case <-stop:
					return
				}
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()

	c := &rawConn{toServer: clientOut, fromServer: clientIn, lines: lines, done: done}
	t.Cleanup(func() {
		close(stop)
		_ = clientOut.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Errorf("rpc server did not exit")
		}
		_ = clientIn.Close()
	})
	return c
}

func (c *rawConn) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(c.toServer, line+"\n"); err != nil {
		t.Fatalf("write command: %v", err)
	}
}

func (c *rawConn) next(t *testing.T) rawRecord {
	t.Helper()
	select {
	case line, ok := <-c.lines:
		if !ok {
			t.Fatal("rpc stream closed while waiting for a record")
		}
		var record rawRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatalf("decode record %q: %v", line, err)
		}
		return record
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an rpc record")
		return rawRecord{}
	}
}

func (c *rawConn) response(t *testing.T) rawRecord {
	t.Helper()
	for {
		record := c.next(t)
		if record.Type == "response" {
			return record
		}
	}
}

func TestRPCResponseIDCorrelation(t *testing.T) {
	rt := NewMemoryRuntime(Options{SessionID: "rpc-session"})
	conn := startRaw(t, rt)

	conn.send(t, `{"id":"a","type":"command","command":"get_state"}`)
	conn.send(t, `{"id":"b","type":"command","command":"get_entries","data":{}}`)

	first := conn.response(t)
	if first.ID != "a" || first.Command != "get_state" || !first.Success {
		t.Fatalf("first response = %+v", first)
	}
	var state State
	if err := json.Unmarshal(first.Data, &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.SessionID != "rpc-session" {
		t.Fatalf("state session = %q", state.SessionID)
	}

	second := conn.response(t)
	if second.ID != "b" || second.Command != "get_entries" || !second.Success {
		t.Fatalf("second response = %+v", second)
	}
	var entries EntriesResult
	if err := json.Unmarshal(second.Data, &entries); err != nil {
		t.Fatalf("decode entries: %v", err)
	}
	if len(entries.Entries) != 0 || entries.Leaf != "" {
		t.Fatalf("entries = %+v", entries)
	}

	// The compact pi-style shape (command name in type) is accepted too.
	conn.send(t, `{"id":"c","type":"get_state"}`)
	third := conn.response(t)
	if third.ID != "c" || third.Command != "get_state" || !third.Success {
		t.Fatalf("third response = %+v", third)
	}
}

func TestRPCGetEntriesLeafCursor(t *testing.T) {
	rt := NewMemoryRuntime(Options{SessionID: "cursor"})
	ctx := context.Background()
	e1, err := rt.AppendEntry(ctx, Entry{Type: EntryCustom, Data: json.RawMessage(`{"n":1}`)})
	if err != nil {
		t.Fatalf("append e1: %v", err)
	}
	e2, err := rt.AppendEntry(ctx, Entry{Type: EntryCustom, Data: json.RawMessage(`{"n":2}`)})
	if err != nil {
		t.Fatalf("append e2: %v", err)
	}
	parent := e1.ID
	if _, err := rt.AppendEntry(ctx, Entry{Type: EntryCustom, ParentID: &parent}); err != nil {
		t.Fatalf("append branch: %v", err)
	}

	conn := startRaw(t, rt)
	conn.send(t, fmt.Sprintf(`{"id":"cursor","type":"command","command":"get_entries","data":{"since":%q,"leaf":%q}}`, e1.ID, e2.ID))
	record := conn.response(t)
	if !record.Success {
		t.Fatalf("get_entries failed: %s", record.Error)
	}
	var result EntriesResult
	if err := json.Unmarshal(record.Data, &result); err != nil {
		t.Fatalf("decode entries: %v", err)
	}
	if len(result.Entries) != 1 || result.Entries[0].ID != e2.ID {
		t.Fatalf("entries = %+v, want only %s", result.Entries, e2.ID)
	}
	if result.Leaf != e2.ID {
		t.Fatalf("leaf = %q, want %q", result.Leaf, e2.ID)
	}
}

func TestRPCParseError(t *testing.T) {
	conn := startRaw(t, NewMemoryRuntime(Options{SessionID: "parse"}))

	conn.send(t, `{"id":"d","type":"command","command":`)
	record := conn.response(t)
	if record.ID != "" {
		t.Fatalf("parse error response id = %q, want empty", record.ID)
	}
	if record.Command != "parse" || record.Success || record.Error == "" {
		t.Fatalf("parse error response = %+v", record)
	}

	conn.send(t, `{"id":"e","type":"command"}`)
	missing := conn.response(t)
	if missing.Command != "parse" || missing.Success {
		t.Fatalf("missing-name response = %+v", missing)
	}

	conn.send(t, `{"id":"f","type":"command","command":"no_such_command"}`)
	unknown := conn.response(t)
	if unknown.ID != "f" || unknown.Command != "no_such_command" || unknown.Success {
		t.Fatalf("unknown command response = %+v", unknown)
	}
	if !strings.Contains(unknown.Error, "unknown command") {
		t.Fatalf("unknown command error = %q", unknown.Error)
	}
}

func TestRPCClientPromptEventsStateAndEntries(t *testing.T) {
	driver := driverFunc(func(ctx context.Context, req RunRequest) (RunResult, error) {
		req.Emit.Emit(EventMessageStart, MessagePayload{Message: Message{Role: "assistant"}})
		req.Emit.Emit(EventMessageUpdate, MessageUpdatePayload{ContentIndex: 0, Delta: "ack"})
		req.Emit.Emit(EventMessageEnd, MessagePayload{Message: Message{Role: "assistant", Text: "ack"}})
		return RunResult{Messages: []Message{{Role: "assistant", Text: "ack"}}}, nil
	})
	rt := NewMemoryRuntime(Options{SessionID: "client-session", Driver: driver, Model: "m1"})
	client := startClient(t, rt)

	sub := SubscribeEvents(client, 64)
	defer sub.Unsubscribe()

	disposition, err := client.Prompt(context.Background(), PromptInput{Text: "hello"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if disposition != DispositionStarted {
		t.Fatalf("disposition = %q, want started", disposition)
	}

	waitEventType(t, sub.Events, EventAgentSettled)

	state := client.State()
	if state.SessionID != "client-session" {
		t.Fatalf("state session = %q", state.SessionID)
	}
	if state.Model != "m1" {
		t.Fatalf("state model = %q", state.Model)
	}
	if state.IsStreaming {
		t.Fatal("state still streaming after settle")
	}

	all, err := client.Entries(context.Background(), EntryQuery{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(all.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(all.Entries))
	}
	since, err := client.Entries(context.Background(), EntryQuery{Since: all.Entries[0].ID})
	if err != nil {
		t.Fatalf("Entries since: %v", err)
	}
	if len(since.Entries) != 1 || since.Entries[0].ID != all.Entries[1].ID {
		t.Fatalf("since entries = %+v", since.Entries)
	}
	if since.Leaf != all.Entries[1].ID {
		t.Fatalf("leaf = %q, want %q", since.Leaf, all.Entries[1].ID)
	}

	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestRPCClientClearQueueAndAbort(t *testing.T) {
	started := make(chan struct{})
	driver := driverFunc(func(ctx context.Context, req RunRequest) (RunResult, error) {
		close(started)
		<-ctx.Done()
		return RunResult{}, ctx.Err()
	})
	rt := NewMemoryRuntime(Options{Driver: driver})
	client := startClient(t, rt)

	if _, err := client.Prompt(context.Background(), PromptInput{Text: "start"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	<-started

	if err := client.Steer(context.Background(), Message{Text: "steer"}); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if err := client.FollowUp(context.Background(), Message{Text: "follow"}); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}

	steering, followUp := client.ClearQueue(context.Background())
	if len(steering) != 1 || steering[0].Text != "steer" {
		t.Fatalf("steering = %+v", steering)
	}
	if len(followUp) != 1 || followUp[0].Text != "follow" {
		t.Fatalf("followUp = %+v", followUp)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Abort(ctx); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	select {
	case <-client.Settled():
	case <-time.After(3 * time.Second):
		t.Fatal("client did not settle after abort")
	}
	if client.State().IsStreaming {
		t.Fatal("client still streaming after abort")
	}
}

func TestRPCClientSessionCommands(t *testing.T) {
	manager := &fakeSessions{}
	rt := NewMemoryRuntime(Options{
		SessionID: "s0",
		Sessions:  manager,
		Commands: []CommandInfo{
			{Name: "review", Description: "review changes", Source: "prompt"},
			{Name: "deploy", Description: "deploy", Source: "skill"},
		},
	})
	client := startClient(t, rt)
	ctx := context.Background()

	commands, err := client.Commands(ctx)
	if err != nil {
		t.Fatalf("Commands: %v", err)
	}
	if len(commands) != 2 || commands[0].Name != "review" {
		t.Fatalf("commands = %+v", commands)
	}

	forked, err := client.Fork(ctx, "entry-1")
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if forked != "fork-1" {
		t.Fatalf("forked = %q", forked)
	}
	cloned, err := client.Clone(ctx)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if cloned != "clone-1" {
		t.Fatalf("cloned = %q", cloned)
	}
	if err := client.SwitchSession(ctx, "s9"); err != nil {
		t.Fatalf("SwitchSession: %v", err)
	}

	manager.mu.Lock()
	calls := append([]string(nil), manager.calls...)
	manager.mu.Unlock()
	want := []string{"fork:s0:entry-1", "clone:s0", "switch:s9"}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("manager calls = %v, want %v", calls, want)
	}
}

func startClient(t *testing.T, rt Runtime) *Client {
	t.Helper()
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := Server(rt, serverIn, serverOut)
		_ = serverOut.Close()
		done <- err
	}()
	client := NewClient(clientOut, clientIn)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = client.Close(ctx)
		_ = clientOut.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Errorf("rpc server did not exit")
		}
		_ = clientIn.Close()
	})
	return client
}

func waitEventType(t *testing.T, events <-chan Event, typ EventType) Event {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("event stream closed before %s", typ)
			}
			if event.Type == typ {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for event %s", typ)
		}
	}
}

type fakeSessions struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeSessions) Fork(ctx context.Context, sessionID, entryID string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "fork:"+sessionID+":"+entryID)
	f.mu.Unlock()
	return "fork-1", nil
}

func (f *fakeSessions) Clone(ctx context.Context, sessionID string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "clone:"+sessionID)
	f.mu.Unlock()
	return "clone-1", nil
}

func (f *fakeSessions) Switch(ctx context.Context, sessionID string) error {
	f.mu.Lock()
	f.calls = append(f.calls, "switch:"+sessionID)
	f.mu.Unlock()
	return nil
}
