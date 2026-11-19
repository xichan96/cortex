// Package runtime defines a transport-neutral session runtime and a JSONL RPC
// protocol that exposes it over stdin/stdout.
//
// A Runtime owns one session: it accepts prompts, steers and follow-ups,
// exposes an append-only entry log, and publishes a totally ordered event
// stream. Transports (in-process SDK, JSONL RPC, future HTTP/WS) are thin I/O
// adapters over this interface and must not duplicate agent logic.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// Errors returned by Runtime implementations.
var (
	// ErrBusy means a prompt arrived while a run is active without an explicit
	// streaming behavior.
	ErrBusy = errors.New("runtime: session is busy")
	// ErrNotStreaming means a steering message was sent while no run is active.
	ErrNotStreaming = errors.New("runtime: no active run")
	// ErrClosed means the runtime has been closed.
	ErrClosed = errors.New("runtime: session is closed")
	// ErrNoDriver means the in-process runtime has no driver configured.
	ErrNoDriver = errors.New("runtime: no driver configured")
	// ErrEmptyPrompt means a message had neither text nor images.
	ErrEmptyPrompt = errors.New("runtime: prompt is empty")
	// ErrEntryNotFound means an entry id (or a since/leaf cursor) is unknown.
	ErrEntryNotFound = errors.New("runtime: entry not found")
	// ErrUnsupported means the runtime does not implement an optional command.
	ErrUnsupported = errors.New("runtime: command not supported")
)

// Disposition reports what happened to a prompt.
type Disposition string

const (
	// DispositionStarted means the prompt started a run.
	DispositionStarted Disposition = "started"
	// DispositionQueued means the prompt was queued for later delivery.
	DispositionQueued Disposition = "queued"
	// DispositionHandled means a handler consumed the prompt; no run started.
	DispositionHandled Disposition = "handled"
)

// StreamingBehavior selects how a prompt arriving during a run is delivered.
type StreamingBehavior string

const (
	// BehaviorSteer injects the message after the current assistant turn and
	// its tool calls, before the next model request.
	BehaviorSteer StreamingBehavior = "steer"
	// BehaviorFollowUp delivers the message once the agent would stop, starting
	// a new round.
	BehaviorFollowUp StreamingBehavior = "followUp"
)

// Image is a transport-neutral image attachment. Exactly one of Data (base64)
// or URL is normally set.
type Image struct {
	Data     []byte `json:"data,omitempty"`
	URL      string `json:"url,omitempty"`
	MIMEType string `json:"mimeType,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// Message is one user or assistant message.
type Message struct {
	Role   string  `json:"role,omitempty"`
	Text   string  `json:"text"`
	Images []Image `json:"images,omitempty"`
	Source string  `json:"source,omitempty"`
}

// PromptInput is the payload of Prompt.
type PromptInput struct {
	Text              string            `json:"text"`
	Images            []Image           `json:"images,omitempty"`
	Source            string            `json:"source,omitempty"`
	StreamingBehavior StreamingBehavior `json:"streamingBehavior,omitempty"`
}

// State is a snapshot of the session state.
type State struct {
	SessionID        string `json:"session_id"`
	Model            string `json:"model,omitempty"`
	Thinking         string `json:"thinking,omitempty"`
	IsStreaming      bool   `json:"is_streaming"`
	PendingToolCalls int    `json:"pending_tool_calls"`
	Error            string `json:"error,omitempty"`
}

// EntryType identifies the kind of a transcript entry. The richer entry model
// lives in agent/transcript; runtime only needs a stable minimal envelope.
type EntryType string

const (
	// EntryMessage is a user or assistant message.
	EntryMessage EntryType = "message"
	// EntryCustom is an extension-defined entry.
	EntryCustom EntryType = "custom"
	// EntryCompaction records a compaction checkpoint.
	EntryCompaction EntryType = "compaction"
	// EntrySessionInfo records session metadata changes such as a name.
	EntrySessionInfo EntryType = "session_info"
	// EntryModelChange records a model change.
	EntryModelChange EntryType = "model_change"
	// EntryThinking records a thinking-level change.
	EntryThinking EntryType = "thinking_level_change"
)

// Entry is one immutable node in the session's append-only entry tree.
type Entry struct {
	Type      EntryType       `json:"type"`
	ID        string          `json:"id"`
	ParentID  *string         `json:"parentId"`
	Timestamp time.Time       `json:"timestamp"`
	Message   *Message        `json:"message,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// EntryQuery selects entries. Since is an exclusive cursor; Leaf selects a
// branch tip (empty means the current active leaf for a single branch, or all
// entries in append order when the implementation keeps abandoned branches).
type EntryQuery struct {
	Since string `json:"since,omitempty"`
	Leaf  string `json:"leaf,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// EntriesResult is the response of Entries. Leaf always reports the selected
// (or current active) leaf so clients can detect branch movement in one call.
type EntriesResult struct {
	Entries []Entry `json:"entries"`
	Leaf    string  `json:"leaf"`
}

// Unsubscribe removes a subscription.
type Unsubscribe func()

// Subscription is a channel-based view of an event stream for callers that
// prefer channels over callbacks. Events are never dropped by the runtime, so
// consumers should drain Events or leave buffer room.
type Subscription struct {
	Events <-chan Event
	cancel Unsubscribe
}

// Unsubscribe stops delivery.
func (s *Subscription) Unsubscribe() {
	if s != nil && s.cancel != nil {
		s.cancel()
	}
}

// SubscribeEvents adapts a callback subscription to a channel subscription.
// The channel is not closed on unsubscribe; use the done signal by selecting on
// the returned Subscription's cancel completion if needed.
func SubscribeEvents(rt Runtime, buffer int) *Subscription {
	if buffer < 0 {
		buffer = 0
	}
	ch := make(chan Event, buffer)
	done := make(chan struct{})
	var once sync.Once
	cancel := rt.Subscribe(func(e Event) {
		select {
		case ch <- e:
		case <-done:
		}
	})
	s := &Subscription{Events: ch}
	s.cancel = func() {
		once.Do(func() {
			close(done)
			if cancel != nil {
				cancel()
			}
		})
	}
	return s
}

// Runtime is the transport-neutral session interface.
type Runtime interface {
	// Prompt submits a new user turn. During an active run it returns
	// ErrBusy unless PromptInput.StreamingBehavior is set.
	Prompt(ctx context.Context, in PromptInput) (Disposition, error)
	// Steer queues a message for delivery inside the active run.
	Steer(ctx context.Context, m Message) error
	// FollowUp queues a message delivered once the agent would stop.
	FollowUp(ctx context.Context, m Message) error
	// Abort cancels the active run and waits for the session to settle.
	Abort(ctx context.Context) error
	// Subscribe registers an event callback. It returns a cancellation func.
	Subscribe(fn func(Event)) Unsubscribe
	// State returns a snapshot (safe to call at any time).
	State() State
	// Entries reads entries after Since on the branch selected by Leaf.
	Entries(ctx context.Context, q EntryQuery) (*EntriesResult, error)
	// Settled returns a channel closed when no automatic work remains.
	Settled() <-chan struct{}
	// Close stops the session and waits for it to settle.
	Close(ctx context.Context) error
}

// QueueController is an optional Runtime capability used by clear_queue.
type QueueController interface {
	// ClearQueue removes and returns pending steering and follow-up messages.
	ClearQueue(ctx context.Context) (steering, followUp []Message)
}

// Compactor is an optional Runtime capability used by compact.
type Compactor interface {
	// Compact runs a manual compaction.
	Compact(ctx context.Context, instructions string) error
}

// DriverCompactor lets a Driver perform the actual compaction work.
type DriverCompactor interface {
	Compact(ctx context.Context, instructions string) (summary string, err error)
}

// SessionManager implements session-level operations that live outside a single
// in-process runtime (persisted sessions, process workers, and so on).
type SessionManager interface {
	Fork(ctx context.Context, sessionID, entryID string) (newSessionID string, err error)
	Clone(ctx context.Context, sessionID string) (newSessionID string, err error)
	Switch(ctx context.Context, sessionID string) error
}

// SessionController is an optional Runtime capability used by fork, clone and
// switch_session.
type SessionController interface {
	Fork(ctx context.Context, entryID string) (sessionID string, err error)
	Clone(ctx context.Context) (sessionID string, err error)
	SwitchSession(ctx context.Context, sessionID string) error
}

// CommandInfo describes a slash command available through get_commands.
type CommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`
}

// CommandProvider is an optional Runtime capability used by get_commands.
type CommandProvider interface {
	Commands(ctx context.Context) ([]CommandInfo, error)
}

// PromptHandler may consume a prompt before it reaches the driver (for example
// extension commands). Returning handled=true sets DispositionHandled.
type PromptHandler func(ctx context.Context, in PromptInput) (handled bool, err error)

// Driver runs one agent turn. Implementations must:
//   - emit message/tool/compaction/retry events through req.Emit;
//   - return the authoritative assistant messages in RunResult.Messages;
//   - respect ctx cancellation and return promptly (Abort waits for it).
//
// The runtime owns session/turn lifecycle, queue and entry events.
type Driver interface {
	Run(ctx context.Context, req RunRequest) (RunResult, error)
}

// RunRequest is passed to Driver.Run.
type RunRequest struct {
	SessionID string
	TurnID    string
	// Messages are the user messages that start this turn (an initial prompt,
	// steering messages that accumulated before the run, or follow-ups).
	Messages []Message
	// Steer lets the driver drain messages queued while it is running. Call
	// Drain (or Wait) between model requests.
	Steer *MessageQueue
	// Emit publishes an event. The runtime fills in session, turn, seq and
	// timestamp; options/approval payloads are the driver's choice.
	Emit Emitter
}

// RunResult is the outcome of one Driver.Run.
type RunResult struct {
	// Messages are authoritative messages generated by this run.
	Messages []Message
	// Entries are additional entries (for example tool transcript entries).
	Entries []Entry
	// WillRetry asks the runtime to start another low-level run immediately.
	WillRetry bool
	// Error describes a run failure that is reported in agent_end.
	Error string
	// Aborted reports that the run ended because of cancellation.
	Aborted bool
}

// Emitter publishes runtime events. Typed payloads are defined in event.go.
type Emitter interface {
	Emit(typ EventType, payload any)
}

// MessageQueue is a concurrency-safe FIFO used for steering messages. Wait is
// convenient for drivers that have nothing else to block on.
type MessageQueue struct {
	mu       sync.Mutex
	items    []Message
	wake     chan struct{}
	onChange func()
}

func newMessageQueue(onChange func()) *MessageQueue {
	return &MessageQueue{wake: make(chan struct{}), onChange: onChange}
}

// Push appends a message and wakes Wait callers.
func (q *MessageQueue) Push(m Message) {
	q.push(m, true)
}

func (q *MessageQueue) push(m Message, notify bool) {
	q.mu.Lock()
	q.items = append(q.items, m)
	close(q.wake)
	q.wake = make(chan struct{})
	q.mu.Unlock()
	if notify {
		q.notify()
	}
}

func (q *MessageQueue) notify() {
	if q.onChange != nil {
		q.onChange()
	}
}

// Drain removes and returns all queued messages.
func (q *MessageQueue) Drain() []Message {
	items := q.drain()
	if len(items) > 0 {
		q.notify()
	}
	return items
}

func (q *MessageQueue) drain() []Message {
	q.mu.Lock()
	items := q.items
	q.items = nil
	q.mu.Unlock()
	return items
}

// Len returns the number of queued messages.
func (q *MessageQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Snapshot returns a copy of the queued messages.
func (q *MessageQueue) Snapshot() []Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return []Message{}
	}
	out := make([]Message, len(q.items))
	copy(out, q.items)
	return out
}

// Wait blocks until a message is available, then drains the queue. It returns
// nil when ctx is canceled.
func (q *MessageQueue) Wait(ctx context.Context) []Message {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			items := q.items
			q.items = nil
			q.mu.Unlock()
			q.notify()
			return items
		}
		wake := q.wake
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		}
	}
}
