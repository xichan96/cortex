package dino

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/runtime"
	"github.com/xichan96/cortex/agent/types"
	dinoQueue "github.com/xichan96/cortex/dino/queue"
)

// SessionRuntime adapts a dino ClientSession to the transport-neutral
// agent/runtime.Runtime interface. It owns one session and translates the
// session's event stream into runtime events.
//
// This is the bridge used by gateway/CLI transports: the transports speak
// runtime.Runtime, while existing dino sessions keep their current execution
// path. Queue delivery is best-effort (dino's Session.Enqueue), and Entries is
// backed by the session's MemoryProvider until live transcripts are wired.
type SessionRuntime struct {
	cs *ClientSession

	mu       sync.Mutex
	closed   bool
	running  bool
	settled  chan struct{}
	settleOK bool
	seq      int64
	subs     map[uint64]func(runtime.Event)
	nextSub  uint64

	messageStarted bool
	lastText       string
	pendingTools   int
	lastErr        string
}

// NewSessionRuntime starts the event translation loop for cs.
func NewSessionRuntime(cs *ClientSession) *SessionRuntime {
	a := &SessionRuntime{
		cs:   cs,
		subs: make(map[uint64]func(runtime.Event)),
		seq:  0,
		settled: func() chan struct{} {
			ch := make(chan struct{})
			close(ch)
			return ch
		}(),
		settleOK: true,
	}
	go a.loop()
	return a
}

// Compile-time check.
var _ runtime.Runtime = (*SessionRuntime)(nil)

func (a *SessionRuntime) loop() {
	for ev := range a.cs.Output() {
		a.handle(ev)
	}
	a.finish(false, "")
}

func (a *SessionRuntime) handle(ev *Event) {
	if ev == nil {
		return
	}
	switch ev.Type {
	case EventTypeMessage:
		a.mu.Lock()
		started := a.messageStarted
		a.messageStarted = true
		a.lastText += ev.Content
		text := ev.Content
		a.mu.Unlock()
		if !started {
			a.emit(runtime.EventMessageStart, runtime.MessagePayload{
				Message: runtime.Message{Role: "assistant"},
			})
		}
		if text != "" {
			a.emit(runtime.EventMessageUpdate, runtime.MessageUpdatePayload{
				Role:         "assistant",
				ContentIndex: 0,
				Delta:        text,
			})
		}
	case EventTypeThinking:
		if ev.Thinking == "" {
			return
		}
		a.emit(runtime.EventMessageUpdate, runtime.MessageUpdatePayload{
			Role:         "assistant",
			ContentIndex: 1,
			Delta:        ev.Thinking,
		})
	case EventTypeToolStart, EventTypeToolCall:
		a.mu.Lock()
		a.pendingTools++
		a.mu.Unlock()
		args, _ := json.Marshal(ev.ToolInput)
		a.emit(runtime.EventToolExecutionStart, runtime.ToolExecutionStartPayload{
			ToolCallID: ev.ToolCallID,
			ToolName:   ev.ToolName,
			Args:       args,
		})
	case EventTypeToolResult:
		a.mu.Lock()
		if a.pendingTools > 0 {
			a.pendingTools--
		}
		a.mu.Unlock()
		result, _ := json.Marshal(ev.ToolOutput)
		a.emit(runtime.EventToolExecutionEnd, runtime.ToolExecutionEndPayload{
			ToolCallID: ev.ToolCallID,
			ToolName:   ev.ToolName,
			Result:     result,
		})
	case EventTypeError:
		a.finish(false, ev.Error)
	case EventTypeDone:
		a.finish(false, "")
	}
}

func (a *SessionRuntime) finish(aborted bool, errMsg string) {
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return
	}
	a.running = false
	a.pendingTools = 0
	if errMsg != "" {
		a.lastErr = errMsg
	}
	settled := a.settled
	closeSettled := !a.settleOK
	if closeSettled {
		a.settleOK = true
	}
	text := a.lastText
	lastErr := a.lastErr
	a.mu.Unlock()

	payload := runtime.AgentEndPayload{Aborted: aborted, Error: errMsg}
	if text != "" {
		payload.Messages = []runtime.Message{{Role: "assistant", Text: text}}
	}
	if lastErr != "" && payload.Error == "" {
		payload.Error = lastErr
	}
	a.emit(runtime.EventAgentEnd, payload)
	a.emit(runtime.EventAgentSettled, runtime.AgentSettledPayload{Aborted: aborted})

	// Publish settled only after the terminal events were delivered, so a
	// waiter that wakes on Settled can already observe the full event stream.
	if closeSettled {
		a.mu.Lock()
		if a.settled == settled {
			close(settled)
		}
		a.mu.Unlock()
	}
}

func (a *SessionRuntime) emit(t runtime.EventType, payload any) {
	var raw json.RawMessage
	if payload != nil {
		if data, err := json.Marshal(payload); err == nil {
			raw = data
		}
	}
	a.mu.Lock()
	a.seq++
	ev := runtime.Event{
		Type:      t,
		SessionID: a.cs.ID(),
		Seq:       a.seq,
		Timestamp: time.Now().UTC(),
		Payload:   raw,
	}
	subs := make([]func(runtime.Event), 0, len(a.subs))
	for _, fn := range a.subs {
		subs = append(subs, fn)
	}
	a.mu.Unlock()
	for _, fn := range subs {
		fn(ev)
	}
}

// Prompt implements runtime.Runtime.
func (a *SessionRuntime) Prompt(ctx context.Context, in runtime.PromptInput) (runtime.Disposition, error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return "", runtime.ErrClosed
	}
	if a.running {
		switch in.StreamingBehavior {
		case runtime.BehaviorSteer:
			a.mu.Unlock()
			if err := a.Steer(ctx, runtime.Message{Text: in.Text, Images: in.Images}); err != nil {
				return "", err
			}
			return runtime.DispositionQueued, nil
		case runtime.BehaviorFollowUp:
			a.mu.Unlock()
			if err := a.FollowUp(ctx, runtime.Message{Text: in.Text, Images: in.Images}); err != nil {
				return "", err
			}
			return runtime.DispositionQueued, nil
		default:
			a.mu.Unlock()
			return "", runtime.ErrBusy
		}
	}
	a.running = true
	a.messageStarted = false
	a.lastText = ""
	a.pendingTools = 0
	a.lastErr = ""
	a.settled = make(chan struct{})
	a.settleOK = false
	a.mu.Unlock()

	a.emit(runtime.EventAgentStart, nil)
	a.emit(runtime.EventTurnStart, nil)

	if err := a.cs.Send(ctx, types.AgentInput{Text: in.Text, Parts: imagesToParts(in.Images)}); err != nil {
		a.finish(false, err.Error())
		return "", err
	}
	return runtime.DispositionStarted, nil
}

func imagesToParts(images []runtime.Image) []types.MessagePart {
	if len(images) == 0 {
		return nil
	}
	parts := make([]types.MessagePart, 0, len(images))
	for _, img := range images {
		if len(img.Data) > 0 {
			parts = append(parts, types.ImageDataPart{Data: img.Data, MIMEType: img.MIMEType})
			continue
		}
		if img.URL != "" {
			parts = append(parts, types.ImageURLPart{URL: img.URL, Detail: img.Detail})
		}
	}
	return parts
}

// Steer implements runtime.Runtime. dino delivers queued messages between
// turns; true mid-turn steering is not exposed by ClientSession.
func (a *SessionRuntime) Steer(_ context.Context, m runtime.Message) error {
	_, err := a.cs.Enqueue(m.Text, dinoQueue.PriorityHigh)
	return err
}

// FollowUp implements runtime.Runtime.
func (a *SessionRuntime) FollowUp(_ context.Context, m runtime.Message) error {
	_, err := a.cs.Enqueue(m.Text, dinoQueue.PriorityNormal)
	return err
}

// Abort implements runtime.Runtime.
func (a *SessionRuntime) Abort(ctx context.Context) error {
	a.cs.CancelCurrentTurn()
	a.mu.Lock()
	settled := a.settled
	a.mu.Unlock()
	select {
	case <-settled:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Subscribe implements runtime.Runtime.
func (a *SessionRuntime) Subscribe(fn func(runtime.Event)) runtime.Unsubscribe {
	if fn == nil {
		return func() {}
	}
	a.mu.Lock()
	id := a.nextSub
	a.nextSub++
	a.subs[id] = fn
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		delete(a.subs, id)
		a.mu.Unlock()
	}
}

// State implements runtime.Runtime.
func (a *SessionRuntime) State() runtime.State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return runtime.State{
		SessionID:        a.cs.ID(),
		IsStreaming:      a.running,
		PendingToolCalls: a.pendingTools,
		Error:            a.lastErr,
	}
}

// Entries implements runtime.Runtime using the session's chat history. Live
// transcript entries are a later integration step.
func (a *SessionRuntime) Entries(ctx context.Context, q runtime.EntryQuery) (*runtime.EntriesResult, error) {
	mem := a.cs.GetMemory()
	if mem == nil {
		return &runtime.EntriesResult{}, nil
	}
	msgs, err := mem.GetChatHistory(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]runtime.Entry, 0, len(msgs))
	base := time.Now().UTC()
	for i, msg := range msgs {
		entries = append(entries, runtime.Entry{
			Type:      runtime.EntryMessage,
			ID:        fmt.Sprintf("msg-%d", i+1),
			Timestamp: base,
			Message:   &runtime.Message{Role: msg.Role, Text: msg.Content, Source: "memory"},
		})
	}
	switch {
	case q.Since != "":
		idx := -1
		for i, e := range entries {
			if e.ID == q.Since {
				idx = i
				break
			}
		}
		if idx >= 0 {
			entries = entries[idx+1:]
		}
	case q.Limit > 0 && len(entries) > q.Limit:
		entries = entries[len(entries)-q.Limit:]
	}
	return &runtime.EntriesResult{Entries: entries}, nil
}

// Settled implements runtime.Runtime.
func (a *SessionRuntime) Settled() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settled
}

// Close implements runtime.Runtime.
func (a *SessionRuntime) Close(ctx context.Context) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.mu.Unlock()
	a.cs.Close()
	a.finish(false, "")
	return nil
}
