package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type driverFunc func(ctx context.Context, req RunRequest) (RunResult, error)

func (f driverFunc) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	return f(ctx, req)
}

type recorder struct {
	mu     sync.Mutex
	events []Event
	unsub  Unsubscribe
}

func record(rt Runtime) *recorder {
	rec := &recorder{}
	rec.unsub = rt.Subscribe(func(e Event) {
		rec.mu.Lock()
		rec.events = append(rec.events, e)
		rec.mu.Unlock()
	})
	return rec
}

func (r *recorder) snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

func (r *recorder) types() []EventType {
	out := []EventType{}
	for _, e := range r.snapshot() {
		out = append(out, e.Type)
	}
	return out
}

func (r *recorder) count(typ EventType) int {
	n := 0
	for _, e := range r.snapshot() {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitSettled(t *testing.T, rt Runtime) {
	t.Helper()
	select {
	case <-rt.Settled():
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for runtime to settle")
	}
}

func TestMemoryRuntimeEventSequence(t *testing.T) {
	driver := driverFunc(func(ctx context.Context, req RunRequest) (RunResult, error) {
		req.Emit.Emit(EventMessageStart, MessagePayload{Message: Message{Role: "assistant"}})
		req.Emit.Emit(EventMessageUpdate, MessageUpdatePayload{ContentIndex: 0, Delta: "he"})
		req.Emit.Emit(EventMessageUpdate, MessageUpdatePayload{ContentIndex: 0, Delta: "llo"})
		req.Emit.Emit(EventMessageEnd, MessagePayload{Message: Message{Role: "assistant", Text: "hello"}})
		return RunResult{Messages: []Message{{Role: "assistant", Text: "hello"}}}, nil
	})

	rt := NewMemoryRuntime(Options{SessionID: "sess-1", Driver: driver, Model: "test-model"})
	rec := record(rt)

	disposition, err := rt.Prompt(context.Background(), PromptInput{Text: "hi"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if disposition != DispositionStarted {
		t.Fatalf("disposition = %q, want started", disposition)
	}
	waitSettled(t, rt)
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := rt.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	want := []EventType{
		EventSessionStart,
		EventEntryAppended, // user message
		EventTurnStart,
		EventAgentStart,
		EventMessageStart,
		EventMessageUpdate,
		EventMessageUpdate,
		EventMessageEnd,
		EventEntryAppended, // assistant message
		EventTurnEnd,
		EventAgentEnd,
		EventAgentSettled,
		EventSessionEnd,
	}
	got := rec.types()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event types:\n got %v\nwant %v", got, want)
	}

	events := rec.snapshot()
	for i, e := range events {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d seq = %d, want %d", i, e.Seq, i+1)
		}
		if e.SessionID != "sess-1" {
			t.Fatalf("event %d session = %q", i, e.SessionID)
		}
		if e.Timestamp.IsZero() {
			t.Fatalf("event %d has zero timestamp", i)
		}
	}

	for _, e := range events {
		switch e.Type {
		case EventTurnStart, EventAgentStart, EventTurnEnd, EventAgentEnd,
			EventMessageStart, EventMessageUpdate, EventMessageEnd:
			if e.TurnID == "" {
				t.Fatalf("%s missing turn id", e.Type)
			}
		case EventAgentSettled, EventSessionStart, EventSessionEnd:
			if e.TurnID != "" {
				t.Fatalf("%s unexpectedly has turn id %q", e.Type, e.TurnID)
			}
		}
	}

	// message_update is delta-only.
	for _, e := range events {
		if e.Type != EventMessageUpdate {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatalf("message_update payload: %v", err)
		}
		if _, ok := payload["message"]; ok {
			t.Fatal("message_update must not carry a full message snapshot")
		}
		if _, ok := payload["delta"]; !ok {
			t.Fatal("message_update must carry delta")
		}
	}

	state := rt.State()
	if state.SessionID != "sess-1" || state.Model != "test-model" || state.IsStreaming {
		t.Fatalf("unexpected final state: %+v", state)
	}
}

func TestMemoryRuntimeSettledWaitsForQueuedSteering(t *testing.T) {
	var mu sync.Mutex
	var runs [][]Message
	release := make(chan struct{})

	driver := driverFunc(func(ctx context.Context, req RunRequest) (RunResult, error) {
		mu.Lock()
		index := len(runs)
		runs = append(runs, append([]Message(nil), req.Messages...))
		mu.Unlock()
		if index == 0 {
			select {
			case <-release:
			case <-ctx.Done():
				return RunResult{}, ctx.Err()
			}
			return RunResult{Messages: []Message{{Role: "assistant", Text: "first"}}}, nil
		}
		return RunResult{Messages: []Message{{Role: "assistant", Text: "second"}}}, nil
	})

	rt := NewMemoryRuntime(Options{SessionID: "s", Driver: driver})
	rec := record(rt)

	if _, err := rt.Prompt(context.Background(), PromptInput{Text: "start"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "agent_start", func() bool { return rec.count(EventAgentStart) == 1 })

	if err := rt.Steer(context.Background(), Message{Text: "more"}); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	waitFor(t, "queue_update with steering", func() bool {
		for _, e := range rec.snapshot() {
			if e.Type != EventQueueUpdate {
				continue
			}
			var payload QueueUpdatePayload
			if e.DecodePayload(&payload) == nil && len(payload.Steering) == 1 && payload.Steering[0] == "more" {
				return true
			}
		}
		return false
	})

	close(release)
	waitSettled(t, rt)

	mu.Lock()
	gotRuns := len(runs)
	var second []Message
	if gotRuns > 1 {
		second = runs[1]
	}
	mu.Unlock()

	if gotRuns != 2 {
		t.Fatalf("runs = %d, want 2 (queued steering must start a second run)", gotRuns)
	}
	if len(second) != 1 || second[0].Text != "more" {
		t.Fatalf("second run messages = %+v, want the steering message", second)
	}

	if got := rec.count(EventAgentEnd); got != 2 {
		t.Fatalf("agent_end count = %d, want 2", got)
	}
	if got := rec.count(EventAgentSettled); got != 1 {
		t.Fatalf("agent_settled count = %d, want 1", got)
	}

	// The single settled event must follow both agent_end events.
	settledIndex := -1
	lastEnd := -1
	for i, e := range rec.snapshot() {
		if e.Type == EventAgentEnd {
			lastEnd = i
		}
		if e.Type == EventAgentSettled {
			settledIndex = i
		}
	}
	if settledIndex <= lastEnd {
		t.Fatal("agent_settled must follow the last agent_end")
	}
}

func TestMemoryRuntimeFollowUpStartsNewRun(t *testing.T) {
	var mu sync.Mutex
	var runs [][]Message
	release := make(chan struct{})

	driver := driverFunc(func(ctx context.Context, req RunRequest) (RunResult, error) {
		mu.Lock()
		index := len(runs)
		runs = append(runs, append([]Message(nil), req.Messages...))
		mu.Unlock()
		if index == 0 {
			select {
			case <-release:
			case <-ctx.Done():
				return RunResult{}, ctx.Err()
			}
		}
		return RunResult{Messages: []Message{{Role: "assistant", Text: "done"}}}, nil
	})

	rt := NewMemoryRuntime(Options{Driver: driver})
	rec := record(rt)

	if _, err := rt.Prompt(context.Background(), PromptInput{Text: "start"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "agent_start", func() bool { return rec.count(EventAgentStart) == 1 })

	if err := rt.FollowUp(context.Background(), Message{Text: "after"}); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	close(release)
	waitSettled(t, rt)

	mu.Lock()
	defer mu.Unlock()
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	if len(runs[1]) != 1 || runs[1][0].Text != "after" {
		t.Fatalf("second run messages = %+v", runs[1])
	}
}

func TestMemoryRuntimeAbortCancelsAndReturnsQueued(t *testing.T) {
	started := make(chan struct{})
	driver := driverFunc(func(ctx context.Context, req RunRequest) (RunResult, error) {
		close(started)
		<-ctx.Done()
		return RunResult{}, ctx.Err()
	})

	rt := NewMemoryRuntime(Options{Driver: driver})
	rec := record(rt)

	if _, err := rt.Prompt(context.Background(), PromptInput{Text: "start"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	<-started
	if err := rt.Steer(context.Background(), Message{Text: "steer"}); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	if err := rt.FollowUp(context.Background(), Message{Text: "follow"}); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	steering, followUp, err := rt.AbortWithQueue(ctx)
	if err != nil {
		t.Fatalf("AbortWithQueue: %v", err)
	}
	if len(steering) != 1 || steering[0].Text != "steer" {
		t.Fatalf("steering = %+v", steering)
	}
	if len(followUp) != 1 || followUp[0].Text != "follow" {
		t.Fatalf("followUp = %+v", followUp)
	}
	waitSettled(t, rt)

	if got := rec.count(EventAgentStart); got != 1 {
		t.Fatalf("agent_start count = %d, want 1", got)
	}
	var settled AgentSettledPayload
	for _, e := range rec.snapshot() {
		if e.Type == EventAgentSettled {
			if err := e.DecodePayload(&settled); err != nil {
				t.Fatalf("settled payload: %v", err)
			}
		}
	}
	if !settled.Aborted {
		t.Fatal("agent_settled should report aborted=true")
	}
	if state := rt.State(); state.IsStreaming {
		t.Fatal("state still streaming after abort")
	}
}

func TestMemoryRuntimePromptDisposition(t *testing.T) {
	t.Run("handled", func(t *testing.T) {
		driverCalled := false
		rt := NewMemoryRuntime(Options{
			Driver: driverFunc(func(ctx context.Context, req RunRequest) (RunResult, error) {
				driverCalled = true
				return RunResult{}, nil
			}),
			Handler: func(ctx context.Context, in PromptInput) (bool, error) {
				return true, nil
			},
		})
		rec := record(rt)
		disposition, err := rt.Prompt(context.Background(), PromptInput{Text: "/handled"})
		if err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		if disposition != DispositionHandled {
			t.Fatalf("disposition = %q, want handled", disposition)
		}
		if driverCalled {
			t.Fatal("driver must not run for a handled prompt")
		}
		if got := rec.count(EventSessionStart); got != 0 {
			t.Fatalf("session_start count = %d, want 0", got)
		}
	})

	t.Run("busy and queued", func(t *testing.T) {
		started := make(chan struct{})
		driver := driverFunc(func(ctx context.Context, req RunRequest) (RunResult, error) {
			close(started)
			<-ctx.Done()
			return RunResult{}, ctx.Err()
		})
		rt := NewMemoryRuntime(Options{Driver: driver})
		if _, err := rt.Prompt(context.Background(), PromptInput{Text: "first"}); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		<-started

		if _, err := rt.Prompt(context.Background(), PromptInput{Text: "second"}); !errors.Is(err, ErrBusy) {
			t.Fatalf("second prompt err = %v, want ErrBusy", err)
		}
		disposition, err := rt.Prompt(context.Background(), PromptInput{
			Text:              "third",
			StreamingBehavior: BehaviorSteer,
		})
		if err != nil {
			t.Fatalf("queued prompt: %v", err)
		}
		if disposition != DispositionQueued {
			t.Fatalf("disposition = %q, want queued", disposition)
		}

		if err := rt.Abort(context.Background()); err != nil {
			t.Fatalf("Abort: %v", err)
		}
	})
}

func TestMemoryRuntimeConcurrentSubscribers(t *testing.T) {
	driver := driverFunc(func(ctx context.Context, req RunRequest) (RunResult, error) {
		for i := 0; i < 4; i++ {
			req.Emit.Emit(EventMessageUpdate, MessageUpdatePayload{ContentIndex: 0, Delta: "x"})
		}
		return RunResult{}, nil
	})
	rt := NewMemoryRuntime(Options{SessionID: "subs", Driver: driver})

	const subscribers = 8
	recorders := make([]*recorder, subscribers)
	for i := range recorders {
		recorders[i] = record(rt)
	}

	if _, err := rt.Prompt(context.Background(), PromptInput{Text: "go"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitSettled(t, rt)

	var reference []Event
	for i, rec := range recorders {
		events := rec.snapshot()
		if len(events) == 0 {
			t.Fatalf("subscriber %d received no events", i)
		}
		if i == 0 {
			reference = events
			continue
		}
		if len(events) != len(reference) {
			t.Fatalf("subscriber %d got %d events, want %d", i, len(events), len(reference))
		}
		for j := range events {
			if events[j].Type != reference[j].Type || events[j].Seq != reference[j].Seq {
				t.Fatalf("subscriber %d diverged at event %d", i, j)
			}
		}
	}
}

func TestMemoryRuntimeToolStateTracking(t *testing.T) {
	driver := driverFunc(func(ctx context.Context, req RunRequest) (RunResult, error) {
		req.Emit.Emit(EventToolExecutionStart, ToolExecutionStartPayload{ToolCallID: "call-1", ToolName: "Bash"})
		return RunResult{}, nil
	})
	rt := NewMemoryRuntime(Options{Driver: driver})
	rec := record(rt)
	if _, err := rt.Prompt(context.Background(), PromptInput{Text: "go"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitSettled(t, rt)
	if state := rt.State(); state.PendingToolCalls != 1 {
		t.Fatalf("pending tool calls = %d, want 1", state.PendingToolCalls)
	}
	_ = rec
}

func TestMemoryRuntimeEntriesCursorAndBranches(t *testing.T) {
	rt := NewMemoryRuntime(Options{SessionID: "entries"})
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
	e3, err := rt.AppendEntry(ctx, Entry{Type: EntryCustom, ParentID: &parent, Data: json.RawMessage(`{"n":3}`)})
	if err != nil {
		t.Fatalf("append e3: %v", err)
	}

	all, err := rt.Entries(ctx, EntryQuery{})
	if err != nil {
		t.Fatalf("Entries all: %v", err)
	}
	if len(all.Entries) != 3 || all.Leaf != e3.ID {
		t.Fatalf("all entries = %+v leaf=%q", all.Entries, all.Leaf)
	}

	firstBranch, err := rt.Entries(ctx, EntryQuery{Leaf: e2.ID})
	if err != nil {
		t.Fatalf("Entries leaf e2: %v", err)
	}
	if ids(firstBranch.Entries) != e1.ID+","+e2.ID {
		t.Fatalf("branch e2 = %v", ids(firstBranch.Entries))
	}
	if firstBranch.Leaf != e2.ID {
		t.Fatalf("leaf = %q, want %q", firstBranch.Leaf, e2.ID)
	}

	secondBranch, err := rt.Entries(ctx, EntryQuery{Leaf: e3.ID})
	if err != nil {
		t.Fatalf("Entries leaf e3: %v", err)
	}
	if ids(secondBranch.Entries) != e1.ID+","+e3.ID {
		t.Fatalf("branch e3 = %v", ids(secondBranch.Entries))
	}

	since, err := rt.Entries(ctx, EntryQuery{Since: e1.ID, Leaf: e2.ID})
	if err != nil {
		t.Fatalf("Entries since: %v", err)
	}
	if ids(since.Entries) != e2.ID {
		t.Fatalf("since e1 = %v, want [%s]", ids(since.Entries), e2.ID)
	}

	if _, err := rt.Entries(ctx, EntryQuery{Since: "missing"}); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("missing since err = %v, want ErrEntryNotFound", err)
	}
	if _, err := rt.Entries(ctx, EntryQuery{Leaf: "missing"}); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("missing leaf err = %v, want ErrEntryNotFound", err)
	}
}

func TestMemoryRuntimePromptCreatesMessageEntries(t *testing.T) {
	driver := driverFunc(func(ctx context.Context, req RunRequest) (RunResult, error) {
		return RunResult{Messages: []Message{{Role: "assistant", Text: "answer"}}}, nil
	})
	rt := NewMemoryRuntime(Options{Driver: driver})
	if _, err := rt.Prompt(context.Background(), PromptInput{Text: "question"}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitSettled(t, rt)

	result, err := rt.Entries(context.Background(), EntryQuery{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(result.Entries))
	}
	if result.Entries[0].Message == nil || result.Entries[0].Message.Text != "question" {
		t.Fatalf("first entry = %+v", result.Entries[0])
	}
	if result.Entries[1].Message == nil || result.Entries[1].Message.Text != "answer" {
		t.Fatalf("second entry = %+v", result.Entries[1])
	}
	if result.Leaf != result.Entries[1].ID {
		t.Fatalf("leaf = %q, want last entry", result.Leaf)
	}
}

func TestMessageQueue(t *testing.T) {
	q := newMessageQueue(nil)
	q.Push(Message{Text: "a"})
	q.Push(Message{Text: "b"})
	if q.Len() != 2 {
		t.Fatalf("len = %d", q.Len())
	}
	if snap := q.Snapshot(); len(snap) != 2 || snap[1].Text != "b" {
		t.Fatalf("snapshot = %+v", snap)
	}
	items := q.Drain()
	if len(items) != 2 || items[0].Text != "a" {
		t.Fatalf("drain = %+v", items)
	}
	if q.Len() != 0 {
		t.Fatalf("len after drain = %d", q.Len())
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go q.Push(Message{Text: "later"})
	waited := q.Wait(ctx)
	if len(waited) != 1 || waited[0].Text != "later" {
		t.Fatalf("wait = %+v", waited)
	}
}

func TestEventJSONRoundTrip(t *testing.T) {
	payload, err := json.Marshal(ToolExecutionStartPayload{
		ToolCallID: "call-1",
		ToolName:   "Bash",
		Args:       json.RawMessage(`{"command":"ls"}`),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	original := Event{
		Type:      EventToolExecutionStart,
		SessionID: "s1",
		TurnID:    "t1",
		Seq:       7,
		Timestamp: time.Now().UTC().Truncate(time.Second),
		Payload:   payload,
	}
	encoded, err := EncodeEvent(original)
	if err != nil {
		t.Fatalf("EncodeEvent: %v", err)
	}
	decoded, err := DecodeEvent(encoded)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", decoded, original)
	}
}

func ids(entries []Entry) string {
	out := ""
	for i, e := range entries {
		if i > 0 {
			out += ","
		}
		out += e.ID
	}
	return out
}
