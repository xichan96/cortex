package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Options configures NewMemoryRuntime.
type Options struct {
	// SessionID is generated when empty.
	SessionID string
	Model     string
	Thinking  string
	// Driver executes turns. It may be nil for entry-only use; Prompt then
	// fails with ErrNoDriver.
	Driver Driver
	// Handler optionally consumes prompts (extension commands).
	Handler PromptHandler
	// Commands are advertised through get_commands.
	Commands []CommandInfo
	// Sessions implements fork/clone/switch_session when non-nil.
	Sessions SessionManager
}

// MemoryRuntime is a fully in-process Runtime. It owns the event sequence, the
// queue, and an append-only entry tree; a Driver does the model/tool work.
type MemoryRuntime struct {
	sessionID string
	driver    Driver
	handler   PromptHandler
	commands  []CommandInfo
	sessions  SessionManager

	mu         sync.Mutex
	model      string
	thinking   string
	running    bool
	closed     bool
	started    bool
	settledCh  chan struct{}
	cancel     context.CancelFunc
	turnID     string
	lastError  string
	pending    map[string]struct{}
	entries    []Entry
	entryIndex map[string]int
	leaf       string

	steerQueue    *MessageQueue
	followUpQueue *MessageQueue

	emitMu  sync.Mutex
	seq     int64
	subMu   sync.Mutex
	nextSub uint64
	subs    map[uint64]func(Event)
}

// NewMemoryRuntime creates an empty session.
func NewMemoryRuntime(opts Options) *MemoryRuntime {
	id := opts.SessionID
	if id == "" {
		id = newID("sess_")
	}
	r := &MemoryRuntime{
		sessionID:  id,
		driver:     opts.Driver,
		handler:    opts.Handler,
		commands:   append([]CommandInfo(nil), opts.Commands...),
		sessions:   opts.Sessions,
		model:      opts.Model,
		thinking:   opts.Thinking,
		settledCh:  make(chan struct{}),
		pending:    make(map[string]struct{}),
		entryIndex: make(map[string]int),
		subs:       make(map[uint64]func(Event)),
	}
	close(r.settledCh) // idle sessions are settled
	r.steerQueue = newMessageQueue(r.emitQueueUpdate)
	r.followUpQueue = newMessageQueue(r.emitQueueUpdate)
	return r
}

// SessionID returns the session identifier.
func (r *MemoryRuntime) SessionID() string { return r.sessionID }

// Prompt implements Runtime.
func (r *MemoryRuntime) Prompt(ctx context.Context, in PromptInput) (Disposition, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if in.Text == "" && len(in.Images) == 0 {
		return "", ErrEmptyPrompt
	}
	if r.handler != nil {
		handled, err := r.handler(ctx, in)
		if err != nil {
			return "", err
		}
		if handled {
			return DispositionHandled, nil
		}
	}

	msg := Message{Role: "user", Text: in.Text, Images: in.Images, Source: in.Source}
	behavior := normalizeBehavior(in.StreamingBehavior)

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return "", ErrClosed
	}
	if r.running {
		switch behavior {
		case BehaviorSteer:
			entry := r.appendEntryLocked(Entry{Type: EntryMessage, Message: &msg})
			r.steerQueue.push(msg, false)
			turnID := r.turnID
			r.mu.Unlock()
			r.emit(EventEntryAppended, turnID, EntryAppendedPayload{Entry: entry})
			r.emitQueueUpdate()
			return DispositionQueued, nil
		case BehaviorFollowUp:
			entry := r.appendEntryLocked(Entry{Type: EntryMessage, Message: &msg})
			r.followUpQueue.push(msg, false)
			turnID := r.turnID
			r.mu.Unlock()
			r.emit(EventEntryAppended, turnID, EntryAppendedPayload{Entry: entry})
			r.emitQueueUpdate()
			return DispositionQueued, nil
		default:
			r.mu.Unlock()
			return "", ErrBusy
		}
	}

	entry := r.appendEntryLocked(Entry{Type: EntryMessage, Message: &msg})
	r.lastError = ""
	runCtx := r.startLocked()
	r.mu.Unlock()

	r.ensureSessionStarted()
	r.emit(EventEntryAppended, "", EntryAppendedPayload{Entry: entry})
	go r.runLoop(runCtx, []Message{msg})
	return DispositionStarted, nil
}

// Steer implements Runtime.
func (r *MemoryRuntime) Steer(ctx context.Context, m Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.Text == "" && len(m.Images) == 0 {
		return ErrEmptyPrompt
	}
	m.Role = roleOrUser(m.Role)

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	if !r.running {
		r.mu.Unlock()
		return ErrNotStreaming
	}
	entry := r.appendEntryLocked(Entry{Type: EntryMessage, Message: &m})
	r.steerQueue.push(m, false)
	turnID := r.turnID
	r.mu.Unlock()

	r.emit(EventEntryAppended, turnID, EntryAppendedPayload{Entry: entry})
	r.emitQueueUpdate()
	return nil
}

// FollowUp implements Runtime. When the session is idle the follow-up starts a
// new turn immediately.
func (r *MemoryRuntime) FollowUp(ctx context.Context, m Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.Text == "" && len(m.Images) == 0 {
		return ErrEmptyPrompt
	}
	m.Role = roleOrUser(m.Role)

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	entry := r.appendEntryLocked(Entry{Type: EntryMessage, Message: &m})
	r.followUpQueue.push(m, false)
	started := false
	var runCtx context.Context
	if !r.running {
		r.lastError = ""
		runCtx = r.startLocked()
		started = true
	}
	turnID := r.turnID
	r.mu.Unlock()

	r.emit(EventEntryAppended, turnID, EntryAppendedPayload{Entry: entry})
	r.emitQueueUpdate()
	if started {
		r.ensureSessionStarted()
		go r.runLoop(runCtx, nil)
	}
	return nil
}

// Abort implements Runtime. Pending steering and follow-up messages are
// discarded; use AbortWithQueue to retrieve them.
func (r *MemoryRuntime) Abort(ctx context.Context) error {
	_, _, err := r.AbortWithQueue(ctx)
	return err
}

// AbortWithQueue cancels the active run, waits for the session to settle, and
// returns the messages that were still queued.
func (r *MemoryRuntime) AbortWithQueue(ctx context.Context) (steering, followUp []Message, err error) {
	steering, followUp = r.clearQueues()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return steering, followUp, ErrClosed
	}
	settled := r.settledCh
	cancel := r.cancel
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	select {
	case <-settled:
		return steering, followUp, nil
	case <-ctx.Done():
		return steering, followUp, ctx.Err()
	}
}

// ClearQueue implements QueueController.
func (r *MemoryRuntime) ClearQueue(ctx context.Context) (steering, followUp []Message) {
	return r.clearQueues()
}

func (r *MemoryRuntime) clearQueues() (steering, followUp []Message) {
	steering = r.steerQueue.drain()
	followUp = r.followUpQueue.drain()
	if len(steering) > 0 || len(followUp) > 0 {
		r.emitQueueUpdate()
	}
	return steering, followUp
}

// Subscribe implements Runtime. Callbacks are invoked serially in event order;
// they must not call back into the runtime in a way that emits another event
// from the same goroutine.
func (r *MemoryRuntime) Subscribe(fn func(Event)) Unsubscribe {
	if fn == nil {
		return func() {}
	}
	r.subMu.Lock()
	r.nextSub++
	id := r.nextSub
	r.subs[id] = fn
	r.subMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.subMu.Lock()
			delete(r.subs, id)
			r.subMu.Unlock()
		})
	}
}

// State implements Runtime.
func (r *MemoryRuntime) State() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return State{
		SessionID:        r.sessionID,
		Model:            r.model,
		Thinking:         r.thinking,
		IsStreaming:      r.running,
		PendingToolCalls: len(r.pending),
		Error:            r.lastError,
	}
}

// Settled implements Runtime.
func (r *MemoryRuntime) Settled() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.settledCh
}

// Entries implements Runtime. With an empty Leaf it returns entries in append
// order (including abandoned branches); with a Leaf it returns the root-to-leaf
// path. Since is exclusive and must exist on the selected path.
func (r *MemoryRuntime) Entries(ctx context.Context, q EntryQuery) (*EntriesResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	var selected []Entry
	leaf := r.leaf
	if q.Leaf != "" {
		path, err := r.pathLocked(q.Leaf)
		if err != nil {
			return nil, err
		}
		selected = path
		leaf = q.Leaf
	} else {
		selected = append([]Entry(nil), r.entries...)
	}

	if q.Since != "" {
		idx := -1
		for i := range selected {
			if selected[i].ID == q.Since {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("%w: %s", ErrEntryNotFound, q.Since)
		}
		selected = selected[idx+1:]
	}
	if q.Limit > 0 && len(selected) > q.Limit {
		selected = selected[:q.Limit]
	}

	out := make([]Entry, len(selected))
	copy(out, selected)
	return &EntriesResult{Entries: out, Leaf: leaf}, nil
}

// AppendEntry appends an entry to the active branch. A nil ParentID means the
// current leaf; an explicit parent may point at older entries to create a
// branch (the new entry then becomes the active leaf).
func (r *MemoryRuntime) AppendEntry(ctx context.Context, e Entry) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	if e.Type == "" {
		e.Type = EntryCustom
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return Entry{}, ErrClosed
	}
	if e.ID != "" {
		if _, exists := r.entryIndex[e.ID]; exists {
			r.mu.Unlock()
			return Entry{}, fmt.Errorf("runtime: entry %s already exists", e.ID)
		}
	}
	if e.ParentID != nil {
		if _, exists := r.entryIndex[*e.ParentID]; !exists {
			r.mu.Unlock()
			return Entry{}, fmt.Errorf("%w: parent %s", ErrEntryNotFound, *e.ParentID)
		}
	}
	entry := r.appendEntryLocked(e)
	turnID := r.turnID
	r.mu.Unlock()

	r.emit(EventEntryAppended, turnID, EntryAppendedPayload{Entry: entry})
	return entry, nil
}

func (r *MemoryRuntime) appendEntryLocked(e Entry) Entry {
	if e.ID == "" {
		e.ID = newID("e_")
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if e.ParentID == nil && r.leaf != "" {
		parent := r.leaf
		e.ParentID = &parent
	}
	r.entryIndex[e.ID] = len(r.entries)
	r.entries = append(r.entries, e)
	r.leaf = e.ID
	return e
}

func (r *MemoryRuntime) pathLocked(leaf string) ([]Entry, error) {
	idx, ok := r.entryIndex[leaf]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrEntryNotFound, leaf)
	}
	var reversed []Entry
	seen := make(map[string]struct{})
	for {
		e := r.entries[idx]
		if _, dup := seen[e.ID]; dup {
			return nil, fmt.Errorf("runtime: entry cycle at %s", e.ID)
		}
		seen[e.ID] = struct{}{}
		reversed = append(reversed, e)
		if e.ParentID == nil {
			break
		}
		next, exists := r.entryIndex[*e.ParentID]
		if !exists {
			return nil, fmt.Errorf("%w: %s", ErrEntryNotFound, *e.ParentID)
		}
		idx = next
	}
	out := make([]Entry, len(reversed))
	for i := range reversed {
		out[len(reversed)-1-i] = reversed[i]
	}
	return out, nil
}

// Fork implements SessionController by delegating to Options.Sessions.
func (r *MemoryRuntime) Fork(ctx context.Context, entryID string) (string, error) {
	return r.delegateSession(ctx, func(m SessionManager, sid string) (string, error) {
		return m.Fork(ctx, sid, entryID)
	})
}

// Clone implements SessionController by delegating to Options.Sessions.
func (r *MemoryRuntime) Clone(ctx context.Context) (string, error) {
	return r.delegateSession(ctx, func(m SessionManager, sid string) (string, error) {
		return m.Clone(ctx, sid)
	})
}

// SwitchSession implements SessionController by delegating to Options.Sessions.
func (r *MemoryRuntime) SwitchSession(ctx context.Context, sessionID string) error {
	r.mu.Lock()
	manager := r.sessions
	r.mu.Unlock()
	if manager == nil {
		return ErrUnsupported
	}
	return manager.Switch(ctx, sessionID)
}

func (r *MemoryRuntime) delegateSession(ctx context.Context, fn func(SessionManager, string) (string, error)) (string, error) {
	r.mu.Lock()
	manager := r.sessions
	sid := r.sessionID
	r.mu.Unlock()
	if manager == nil {
		return "", ErrUnsupported
	}
	return fn(manager, sid)
}

// Compact implements Compactor. Actual work is optional and delegated to a
// DriverCompactor driver.
func (r *MemoryRuntime) Compact(ctx context.Context, instructions string) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrClosed
	}
	if r.running {
		r.mu.Unlock()
		return ErrBusy
	}
	compactor, ok := r.driver.(DriverCompactor)
	r.mu.Unlock()
	if !ok || compactor == nil {
		return ErrUnsupported
	}

	r.emit(EventCompactionStart, "", CompactionStartPayload{Reason: "manual"})
	summary, err := compactor.Compact(ctx, instructions)
	if err != nil {
		r.emit(EventCompactionFailed, "", CompactionFailedPayload{Error: err.Error()})
		return err
	}
	r.emit(EventCompactionEnd, "", CompactionEndPayload{Summary: summary})
	return nil
}

// Commands implements CommandProvider.
func (r *MemoryRuntime) Commands(ctx context.Context) ([]CommandInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]CommandInfo(nil), r.commands...), nil
}

// SetModel changes the active model and publishes model_changed.
func (r *MemoryRuntime) SetModel(model string) {
	r.mu.Lock()
	if r.model == model {
		r.mu.Unlock()
		return
	}
	r.model = model
	r.mu.Unlock()
	r.emit(EventModelChanged, "", ModelChangedPayload{Model: model})
}

// SetThinking changes the thinking level and publishes
// thinking_level_changed.
func (r *MemoryRuntime) SetThinking(level string) {
	r.mu.Lock()
	if r.thinking == level {
		r.mu.Unlock()
		return
	}
	r.thinking = level
	r.mu.Unlock()
	r.emit(EventThinkingLevelChanged, "", ThinkingLevelChangedPayload{Level: level})
}

// Close implements Runtime. It aborts the run, discards queued messages and
// emits session_end exactly once.
func (r *MemoryRuntime) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	settled := r.settledCh
	cancel := r.cancel
	started := r.started
	r.mu.Unlock()

	r.clearQueues()
	if cancel != nil {
		cancel()
	}
	select {
	case <-settled:
	case <-ctx.Done():
		return ctx.Err()
	}

	if started {
		r.emit(EventSessionEnd, "", SessionEndPayload{})
	}
	r.subMu.Lock()
	r.subs = make(map[uint64]func(Event))
	r.subMu.Unlock()
	return nil
}

func (r *MemoryRuntime) startLocked() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	r.running = true
	r.settledCh = make(chan struct{})
	r.cancel = cancel
	return ctx
}

func (r *MemoryRuntime) ensureSessionStarted() {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	model := r.model
	r.mu.Unlock()
	r.emit(EventSessionStart, "", SessionStartPayload{Model: model})
}

func (r *MemoryRuntime) runLoop(ctx context.Context, initial []Message) {
	pending := initial
	retry := false
	aborted := false

	for {
		if !retry {
			if len(pending) == 0 {
				pending = r.steerQueue.Drain()
			}
			if len(pending) == 0 {
				pending = r.followUpQueue.Drain()
			}
		}
		if len(pending) == 0 {
			r.mu.Lock()
			if r.steerQueue.Len() > 0 || r.followUpQueue.Len() > 0 {
				r.mu.Unlock()
				retry = false
				continue
			}
			r.running = false
			r.cancel = nil
			r.turnID = ""
			settled := r.settledCh
			r.mu.Unlock()

			r.emit(EventAgentSettled, "", AgentSettledPayload{Aborted: aborted})
			close(settled)
			return
		}

		retry = false
		turnID := newID("turn_")
		messages := pending
		pending = nil
		r.clearError()

		r.mu.Lock()
		r.turnID = turnID
		r.mu.Unlock()

		r.emit(EventTurnStart, turnID, nil)
		r.emit(EventAgentStart, turnID, nil)

		result := r.runOnce(ctx, turnID, messages)
		r.recordResult(turnID, result)

		aborted = aborted || result.Aborted || ctx.Err() != nil
		payload := AgentEndPayload{
			Messages:  result.Messages,
			WillRetry: result.WillRetry,
			Error:     result.Error,
			Aborted:   aborted,
		}
		r.emit(EventTurnEnd, turnID, TurnEndPayload{Message: lastMessage(result.Messages)})
		r.emit(EventAgentEnd, turnID, payload)

		if aborted {
			break
		}
		if result.WillRetry {
			retry = true
		}
	}

	r.mu.Lock()
	r.running = false
	r.cancel = nil
	r.turnID = ""
	settled := r.settledCh
	r.mu.Unlock()

	r.emit(EventAgentSettled, "", AgentSettledPayload{Aborted: aborted})
	close(settled)
}

func (r *MemoryRuntime) runOnce(ctx context.Context, turnID string, messages []Message) (result RunResult) {
	if r.driver == nil {
		return RunResult{Error: ErrNoDriver.Error()}
	}
	req := RunRequest{
		SessionID: r.sessionID,
		TurnID:    turnID,
		Messages:  messages,
		Steer:     r.steerQueue,
		Emit:      runtimeEmitter{r: r, turnID: turnID},
	}
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("driver panic: %v", p)
			}
		}()
		result, err = r.driver.Run(ctx, req)
	}()
	if err != nil && result.Error == "" {
		result.Error = err.Error()
	}
	if ctx.Err() != nil {
		result.Aborted = true
	}
	return result
}

func (r *MemoryRuntime) recordResult(turnID string, result RunResult) {
	for i := range result.Messages {
		m := result.Messages[i]
		m.Role = roleOrAssistant(m.Role)
		entry := r.appendEntry(Entry{Type: EntryMessage, Message: &m})
		r.emit(EventEntryAppended, turnID, EntryAppendedPayload{Entry: entry})
	}
	for _, e := range result.Entries {
		entry, err := r.AppendEntry(context.Background(), e)
		if err == nil {
			_ = entry // AppendEntry already emitted entry_appended
		}
	}
}

func (r *MemoryRuntime) appendEntry(e Entry) Entry {
	r.mu.Lock()
	entry := r.appendEntryLocked(e)
	r.mu.Unlock()
	return entry
}

func (r *MemoryRuntime) clearError() {
	r.mu.Lock()
	r.lastError = ""
	r.mu.Unlock()
}

func (r *MemoryRuntime) emitQueueUpdate() {
	steering := queueTexts(r.steerQueue.Snapshot())
	followUp := queueTexts(r.followUpQueue.Snapshot())

	r.mu.Lock()
	turnID := r.turnID
	r.mu.Unlock()

	r.emit(EventQueueUpdate, turnID, QueueUpdatePayload{Steering: steering, FollowUp: followUp})
}

func (r *MemoryRuntime) emit(typ EventType, turnID string, payload any) {
	var raw json.RawMessage
	if payload != nil {
		if b, err := json.Marshal(payload); err == nil {
			raw = b
		}
	}
	r.trackEvent(typ, raw)

	r.emitMu.Lock()
	r.seq++
	event := Event{
		Type:      typ,
		SessionID: r.sessionID,
		TurnID:    turnID,
		Seq:       r.seq,
		Timestamp: time.Now().UTC(),
		Payload:   raw,
	}
	subs := r.subscriberSnapshot()
	for _, fn := range subs {
		callSubscriber(fn, event)
	}
	r.emitMu.Unlock()
}

func (r *MemoryRuntime) trackEvent(typ EventType, raw json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch typ {
	case EventToolExecutionStart:
		var p ToolExecutionStartPayload
		if json.Unmarshal(raw, &p) == nil && p.ToolCallID != "" {
			r.pending[p.ToolCallID] = struct{}{}
		}
	case EventToolExecutionEnd:
		var p ToolExecutionEndPayload
		if json.Unmarshal(raw, &p) == nil && p.ToolCallID != "" {
			delete(r.pending, p.ToolCallID)
		}
	case EventAgentEnd:
		var p AgentEndPayload
		if json.Unmarshal(raw, &p) == nil && p.Error != "" {
			r.lastError = p.Error
		}
	case EventModelChanged:
		var p ModelChangedPayload
		if json.Unmarshal(raw, &p) == nil && p.Model != "" {
			r.model = p.Model
		}
	case EventThinkingLevelChanged:
		var p ThinkingLevelChangedPayload
		if json.Unmarshal(raw, &p) == nil && p.Level != "" {
			r.thinking = p.Level
		}
	}
}

func (r *MemoryRuntime) subscriberSnapshot() []func(Event) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	if len(r.subs) == 0 {
		return nil
	}
	out := make([]func(Event), 0, len(r.subs))
	for _, fn := range r.subs {
		out = append(out, fn)
	}
	return out
}

func callSubscriber(fn func(Event), e Event) {
	defer func() { _ = recover() }()
	fn(e)
}

type runtimeEmitter struct {
	r      *MemoryRuntime
	turnID string
}

func (e runtimeEmitter) Emit(typ EventType, payload any) {
	e.r.emit(typ, e.turnID, payload)
}

func normalizeBehavior(b StreamingBehavior) StreamingBehavior {
	switch string(b) {
	case "steer":
		return BehaviorSteer
	case "followUp", "follow_up":
		return BehaviorFollowUp
	default:
		return b
	}
}

func roleOrUser(role string) string {
	if role == "" {
		return "user"
	}
	return role
}

func roleOrAssistant(role string) string {
	if role == "" {
		return "assistant"
	}
	return role
}

func queueTexts(messages []Message) []string {
	out := make([]string, 0, len(messages))
	for _, m := range messages {
		out = append(out, m.Text)
	}
	return out
}

func lastMessage(messages []Message) *Message {
	if len(messages) == 0 {
		return nil
	}
	m := messages[len(messages)-1]
	return &m
}

func newID(prefix string) string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(buf[:])
}
