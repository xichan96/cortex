package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// JSONL RPC protocol.
//
// stdout carries only protocol records, one per line, strictly terminated by
// LF. Diagnostics belong on stderr. There are three record families:
//
//	command  client -> server   {"id":"1","type":"command","command":"prompt","data":{...}}
//	response server -> client   {"id":"1","type":"response","command":"prompt","success":true,"data":{...}}
//	event    server -> client   {"type":"event","event":{"type":"agent_settled",...}}
//
// id is optional for commands. Malformed commands produce a response with
// command "parse" and no id.

// Command is a client request record. Command holds the command name; for
// compatibility the name may also be placed in Type (for example
// {"type":"get_state"}) when Type is not "command".
type Command struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type,omitempty"`
	Command string          `json:"command,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`

	// Flat fields accepted as an alternative to Data, matching pi's RPC shape.
	Text              string  `json:"text,omitempty"`
	Message           string  `json:"message,omitempty"`
	Images            []Image `json:"images,omitempty"`
	Source            string  `json:"source,omitempty"`
	StreamingBehavior string  `json:"streamingBehavior,omitempty"`
	Since             string  `json:"since,omitempty"`
	Leaf              string  `json:"leaf,omitempty"`
	EntryID           string  `json:"entryId,omitempty"`
	SessionID         string  `json:"sessionId,omitempty"`
	Instructions      string  `json:"instructions,omitempty"`
}

// Name returns the effective command name.
func (c Command) Name() string {
	if c.Command != "" {
		return c.Command
	}
	if c.Type != "" && c.Type != "command" {
		return c.Type
	}
	return ""
}

// Response is a server reply to one command. Data and Error are mutually
// exclusive in practice; Success discriminates.
type Response struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// EventEnvelope wraps an event record on stdout.
type EventEnvelope struct {
	Type  string `json:"type"`
	Event Event  `json:"event"`
}

// EncodeCommand marshals a command to JSON without framing.
func EncodeCommand(c Command) ([]byte, error) {
	if c.Type == "" {
		c.Type = "command"
	}
	return json.Marshal(c)
}

// DecodeCommand decodes one command record.
func DecodeCommand(data []byte) (Command, error) {
	var c Command
	if err := json.Unmarshal(data, &c); err != nil {
		return Command{}, err
	}
	if c.Name() == "" {
		return Command{}, fmt.Errorf("rpc: command name is missing")
	}
	return c, nil
}

// EncodeResponse marshals a response to JSON without framing.
func EncodeResponse(r Response) ([]byte, error) {
	if r.Type == "" {
		r.Type = "response"
	}
	return json.Marshal(r)
}

// DecodeResponse decodes one response record.
func DecodeResponse(data []byte) (Response, error) {
	var r Response
	if err := json.Unmarshal(data, &r); err != nil {
		return Response{}, err
	}
	return r, nil
}

// Server serves the JSONL RPC protocol on in/out until in reaches EOF. It
// subscribes to rt before reading commands, forwards events, and calls
// rt.Close on shutdown.
func Server(rt Runtime, in io.Reader, out io.Writer) error {
	if rt == nil {
		return fmt.Errorf("rpc: nil runtime")
	}
	s := &server{rt: rt, out: out}
	unsubscribe := rt.Subscribe(func(e Event) {
		_ = s.write(EventEnvelope{Type: "event", Event: e})
	})
	defer unsubscribe()

	reader := bufio.NewReader(in)
	for {
		line, err := readJSONLine(reader)
		if len(line) > 0 {
			if handleErr := s.handle(line); handleErr != nil {
				return handleErr
			}
		}
		if err != nil {
			if err == io.EOF {
				return rt.Close(context.Background())
			}
			return err
		}
	}
}

type server struct {
	rt  Runtime
	out io.Writer
	mu  sync.Mutex
}

func (s *server) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.out.Write(b)
	return err
}

func (s *server) respond(resp Response) error {
	if resp.Type == "" {
		resp.Type = "response"
	}
	return s.write(resp)
}

func (s *server) respondData(id, command string, data any) error {
	var raw json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return s.respond(Response{ID: id, Command: command, Success: false, Error: err.Error()})
		}
		raw = b
	}
	return s.respond(Response{ID: id, Command: command, Success: true, Data: raw})
}

func (s *server) respondError(id, command string, err error) error {
	return s.respond(Response{ID: id, Command: command, Success: false, Error: err.Error()})
}

func (s *server) handle(line []byte) error {
	cmd, err := DecodeCommand(line)
	if err != nil {
		return s.respond(Response{Command: "parse", Success: false, Error: err.Error()})
	}
	ctx := context.Background()

	switch cmd.Name() {
	case "prompt":
		in, err := promptInputFromCommand(cmd)
		if err != nil {
			return s.respondError(cmd.ID, "prompt", err)
		}
		disposition, err := s.rt.Prompt(ctx, in)
		if err != nil {
			return s.respondError(cmd.ID, "prompt", err)
		}
		return s.respondData(cmd.ID, "prompt", map[string]any{"disposition": disposition})

	case "steer", "follow_up":
		msg, err := messageFromCommand(cmd)
		if err != nil {
			return s.respondError(cmd.ID, cmd.Name(), err)
		}
		if cmd.Name() == "steer" {
			err = s.rt.Steer(ctx, msg)
		} else {
			err = s.rt.FollowUp(ctx, msg)
		}
		if err != nil {
			return s.respondError(cmd.ID, cmd.Name(), err)
		}
		return s.respondData(cmd.ID, cmd.Name(), map[string]any{"disposition": DispositionQueued})

	case "abort":
		if err := s.rt.Abort(ctx); err != nil {
			return s.respondError(cmd.ID, "abort", err)
		}
		return s.respondData(cmd.ID, "abort", nil)

	case "clear_queue":
		controller, ok := s.rt.(QueueController)
		if !ok {
			return s.respondError(cmd.ID, "clear_queue", ErrUnsupported)
		}
		steering, followUp := controller.ClearQueue(ctx)
		return s.respondData(cmd.ID, "clear_queue", map[string]any{
			"steering": queueTexts(steering),
			"followUp": queueTexts(followUp),
		})

	case "get_state":
		return s.respondData(cmd.ID, "get_state", s.rt.State())

	case "get_entries":
		query := entryQueryFromCommand(cmd)
		result, err := s.rt.Entries(ctx, query)
		if err != nil {
			return s.respondError(cmd.ID, "get_entries", err)
		}
		return s.respondData(cmd.ID, "get_entries", result)

	case "compact":
		compactor, ok := s.rt.(Compactor)
		if !ok {
			return s.respondError(cmd.ID, "compact", ErrUnsupported)
		}
		if err := compactor.Compact(ctx, instructionsFromCommand(cmd)); err != nil {
			return s.respondError(cmd.ID, "compact", err)
		}
		return s.respondData(cmd.ID, "compact", nil)

	case "fork":
		controller, ok := s.rt.(SessionController)
		if !ok {
			return s.respondError(cmd.ID, "fork", ErrUnsupported)
		}
		entryID := cmd.EntryID
		if entryID == "" && len(cmd.Data) > 0 {
			var payload struct {
				EntryID string `json:"entryId"`
			}
			if err := json.Unmarshal(cmd.Data, &payload); err != nil {
				return s.respondError(cmd.ID, "fork", fmt.Errorf("invalid fork data: %w", err))
			}
			entryID = payload.EntryID
		}
		if entryID == "" {
			return s.respondError(cmd.ID, "fork", fmt.Errorf("rpc: fork requires entryId"))
		}
		sessionID, err := controller.Fork(ctx, entryID)
		if err != nil {
			return s.respondError(cmd.ID, "fork", err)
		}
		return s.respondData(cmd.ID, "fork", map[string]any{"sessionId": sessionID})

	case "clone":
		controller, ok := s.rt.(SessionController)
		if !ok {
			return s.respondError(cmd.ID, "clone", ErrUnsupported)
		}
		sessionID, err := controller.Clone(ctx)
		if err != nil {
			return s.respondError(cmd.ID, "clone", err)
		}
		return s.respondData(cmd.ID, "clone", map[string]any{"sessionId": sessionID})

	case "switch_session":
		controller, ok := s.rt.(SessionController)
		if !ok {
			return s.respondError(cmd.ID, "switch_session", ErrUnsupported)
		}
		sessionID := cmd.SessionID
		if sessionID == "" && len(cmd.Data) > 0 {
			var payload struct {
				SessionID string `json:"sessionId"`
			}
			if err := json.Unmarshal(cmd.Data, &payload); err != nil {
				return s.respondError(cmd.ID, "switch_session", fmt.Errorf("invalid switch_session data: %w", err))
			}
			sessionID = payload.SessionID
		}
		if sessionID == "" {
			return s.respondError(cmd.ID, "switch_session", fmt.Errorf("rpc: switch_session requires sessionId"))
		}
		if err := controller.SwitchSession(ctx, sessionID); err != nil {
			return s.respondError(cmd.ID, "switch_session", err)
		}
		return s.respondData(cmd.ID, "switch_session", nil)

	case "get_commands":
		provider, ok := s.rt.(CommandProvider)
		if !ok {
			return s.respondData(cmd.ID, "get_commands", map[string]any{"commands": []CommandInfo{}})
		}
		commands, err := provider.Commands(ctx)
		if err != nil {
			return s.respondError(cmd.ID, "get_commands", err)
		}
		if commands == nil {
			commands = []CommandInfo{}
		}
		return s.respondData(cmd.ID, "get_commands", map[string]any{"commands": commands})

	default:
		return s.respondError(cmd.ID, cmd.Name(), fmt.Errorf("rpc: unknown command %q", cmd.Name()))
	}
}

func promptInputFromCommand(cmd Command) (PromptInput, error) {
	var wire struct {
		Text              string `json:"text"`
		Message           string `json:"message"`
		Images            []Image
		Source            string `json:"source"`
		StreamingBehavior string `json:"streamingBehavior"`
	}
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &wire); err != nil {
			return PromptInput{}, fmt.Errorf("rpc: invalid prompt data: %w", err)
		}
	} else {
		wire.Text = cmd.Text
		wire.Message = cmd.Message
		wire.Images = cmd.Images
		wire.Source = cmd.Source
		wire.StreamingBehavior = cmd.StreamingBehavior
	}
	if wire.Text == "" {
		wire.Text = wire.Message
	}
	if wire.Text == "" && len(wire.Images) == 0 {
		return PromptInput{}, ErrEmptyPrompt
	}
	return PromptInput{
		Text:              wire.Text,
		Images:            wire.Images,
		Source:            wire.Source,
		StreamingBehavior: normalizeBehavior(StreamingBehavior(wire.StreamingBehavior)),
	}, nil
}

func messageFromCommand(cmd Command) (Message, error) {
	var wire struct {
		Text    string `json:"text"`
		Message string `json:"message"`
		Role    string `json:"role"`
		Source  string `json:"source"`
		Images  []Image
	}
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &wire); err != nil {
			return Message{}, fmt.Errorf("rpc: invalid message data: %w", err)
		}
	} else {
		wire.Text = cmd.Text
		wire.Message = cmd.Message
		wire.Source = cmd.Source
		wire.Images = cmd.Images
	}
	if wire.Text == "" {
		wire.Text = wire.Message
	}
	if wire.Text == "" && len(wire.Images) == 0 {
		return Message{}, ErrEmptyPrompt
	}
	return Message{Role: roleOrUser(wire.Role), Text: wire.Text, Images: wire.Images, Source: wire.Source}, nil
}

func entryQueryFromCommand(cmd Command) EntryQuery {
	query := EntryQuery{Since: cmd.Since, Leaf: cmd.Leaf}
	if len(cmd.Data) > 0 {
		var payload EntryQuery
		if err := json.Unmarshal(cmd.Data, &payload); err == nil {
			if payload.Since != "" {
				query.Since = payload.Since
			}
			if payload.Leaf != "" {
				query.Leaf = payload.Leaf
			}
			if payload.Limit > 0 {
				query.Limit = payload.Limit
			}
		}
	}
	return query
}

func instructionsFromCommand(cmd Command) string {
	if cmd.Instructions != "" {
		return cmd.Instructions
	}
	if len(cmd.Data) > 0 {
		var payload struct {
			Instructions       string `json:"instructions"`
			CustomInstructions string `json:"customInstructions"`
		}
		if json.Unmarshal(cmd.Data, &payload) == nil {
			if payload.Instructions != "" {
				return payload.Instructions
			}
			return payload.CustomInstructions
		}
	}
	return ""
}

func readJSONLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	line = bytes.TrimSpace(line)
	return line, err
}

// Client is a JSONL RPC client that implements Runtime. It reads records in a
// background goroutine and correlates responses by id. Events received before
// Subscribe are not replayed.
type Client struct {
	in  io.Writer
	out io.Reader

	writeMu sync.Mutex
	mu      sync.Mutex

	nextID    uint64
	inflight  map[string]chan Response
	subs      map[uint64]func(Event)
	nextSub   uint64
	state     State
	pending   map[string]struct{}
	settledCh chan struct{}
	closed    bool
	readErr   error
	done      chan struct{}
	doneOnce  sync.Once
}

// NewClient starts an RPC client that writes commands to in and reads records
// from out.
func NewClient(in io.Writer, out io.Reader) *Client {
	c := &Client{
		in:        in,
		out:       out,
		inflight:  make(map[string]chan Response),
		subs:      make(map[uint64]func(Event)),
		pending:   make(map[string]struct{}),
		settledCh: make(chan struct{}),
		done:      make(chan struct{}),
	}
	close(c.settledCh) // idle until the first run starts
	go c.readLoop()
	return c
}

// Prompt implements Runtime.
func (c *Client) Prompt(ctx context.Context, in PromptInput) (Disposition, error) {
	data, err := c.request(ctx, "prompt", in)
	if err != nil {
		return "", err
	}
	var payload struct {
		Disposition Disposition `json:"disposition"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("rpc: invalid prompt response: %w", err)
	}
	if payload.Disposition == "" {
		return "", fmt.Errorf("rpc: prompt response is missing disposition")
	}
	return payload.Disposition, nil
}

// Steer implements Runtime.
func (c *Client) Steer(ctx context.Context, m Message) error {
	_, err := c.request(ctx, "steer", m)
	return err
}

// FollowUp implements Runtime.
func (c *Client) FollowUp(ctx context.Context, m Message) error {
	_, err := c.request(ctx, "follow_up", m)
	return err
}

// Abort implements Runtime.
func (c *Client) Abort(ctx context.Context) error {
	_, err := c.request(ctx, "abort", nil)
	return err
}

// ClearQueue implements QueueController over RPC.
func (c *Client) ClearQueue(ctx context.Context) (steering, followUp []Message) {
	data, err := c.request(ctx, "clear_queue", nil)
	if err != nil {
		return nil, nil
	}
	var payload struct {
		Steering []string `json:"steering"`
		FollowUp []string `json:"followUp"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return nil, nil
	}
	for _, text := range payload.Steering {
		steering = append(steering, Message{Role: "user", Text: text})
	}
	for _, text := range payload.FollowUp {
		followUp = append(followUp, Message{Role: "user", Text: text})
	}
	return steering, followUp
}

// Compact implements Compactor over RPC.
func (c *Client) Compact(ctx context.Context, instructions string) error {
	_, err := c.request(ctx, "compact", map[string]string{"instructions": instructions})
	return err
}

// Fork implements SessionController over RPC.
func (c *Client) Fork(ctx context.Context, entryID string) (string, error) {
	data, err := c.request(ctx, "fork", map[string]string{"entryId": entryID})
	if err != nil {
		return "", err
	}
	var payload struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("rpc: invalid fork response: %w", err)
	}
	return payload.SessionID, nil
}

// Clone implements SessionController over RPC.
func (c *Client) Clone(ctx context.Context) (string, error) {
	data, err := c.request(ctx, "clone", nil)
	if err != nil {
		return "", err
	}
	var payload struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("rpc: invalid clone response: %w", err)
	}
	return payload.SessionID, nil
}

// SwitchSession implements SessionController over RPC.
func (c *Client) SwitchSession(ctx context.Context, sessionID string) error {
	_, err := c.request(ctx, "switch_session", map[string]string{"sessionId": sessionID})
	return err
}

// Commands implements CommandProvider over RPC.
func (c *Client) Commands(ctx context.Context) ([]CommandInfo, error) {
	data, err := c.request(ctx, "get_commands", nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Commands []CommandInfo `json:"commands"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("rpc: invalid get_commands response: %w", err)
	}
	return payload.Commands, nil
}

// Subscribe implements Runtime. Events are delivered from the client's read
// goroutine; callbacks must not block indefinitely.
func (c *Client) Subscribe(fn func(Event)) Unsubscribe {
	if fn == nil {
		return func() {}
	}
	c.mu.Lock()
	c.nextSub++
	id := c.nextSub
	c.subs[id] = fn
	c.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.subs, id)
			c.mu.Unlock()
		})
	}
}

// State implements Runtime and returns the state observed from events. Call
// get_state through a dedicated client if an authoritative snapshot is needed.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Entries implements Runtime.
func (c *Client) Entries(ctx context.Context, q EntryQuery) (*EntriesResult, error) {
	data, err := c.request(ctx, "get_entries", q)
	if err != nil {
		return nil, err
	}
	var result EntriesResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("rpc: invalid get_entries response: %w", err)
	}
	if result.Entries == nil {
		result.Entries = []Entry{}
	}
	return &result, nil
}

// Settled implements Runtime.
func (c *Client) Settled() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.settledCh
}

// Close implements Runtime by waiting for the session to settle. It does not
// close the underlying reader or writer.
func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	settled := c.settledCh
	c.mu.Unlock()

	select {
	case <-settled:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
	}
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

func (c *Client) request(ctx context.Context, name string, data any) (json.RawMessage, error) {
	var payload json.RawMessage
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("rpc: encode %s: %w", name, err)
		}
		payload = b
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		return nil, fmt.Errorf("rpc: connection closed: %w", err)
	}
	c.nextID++
	id := fmt.Sprintf("c%d", c.nextID)
	ch := make(chan Response, 1)
	c.inflight[id] = ch
	c.mu.Unlock()

	line, err := EncodeCommand(Command{ID: id, Type: "command", Command: name, Data: payload})
	if err != nil {
		c.removeInflight(id)
		return nil, err
	}
	if err := c.writeLine(line); err != nil {
		c.removeInflight(id)
		return nil, err
	}

	select {
	case resp := <-ch:
		if !resp.Success {
			if resp.Error == "" {
				return nil, fmt.Errorf("rpc: %s failed", name)
			}
			return nil, fmt.Errorf("rpc: %s", resp.Error)
		}
		return resp.Data, nil
	case <-ctx.Done():
		c.removeInflight(id)
		return nil, ctx.Err()
	case <-c.done:
		c.removeInflight(id)
		c.mu.Lock()
		err := c.readErr
		c.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("rpc: connection closed: %w", err)
		}
		return nil, ErrClosed
	}
}

func (c *Client) removeInflight(id string) {
	c.mu.Lock()
	delete(c.inflight, id)
	c.mu.Unlock()
}

func (c *Client) writeLine(line []byte) error {
	line = append(line, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.in.Write(line)
	return err
}

func (c *Client) readLoop() {
	reader := bufio.NewReader(c.out)
	for {
		line, err := readJSONLine(reader)
		if len(line) > 0 {
			c.handleRecord(line)
		}
		if err != nil {
			c.fail(err)
			return
		}
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.readErr == nil {
		c.readErr = err
	}
	c.mu.Unlock()
	c.doneOnce.Do(func() { close(c.done) })
}

func (c *Client) handleRecord(line []byte) {
	var record struct {
		Type  string `json:"type"`
		Event *Event `json:"event,omitempty"`
		Response
	}
	if err := json.Unmarshal(line, &record); err != nil {
		return
	}
	switch record.Type {
	case "event":
		if record.Event != nil {
			c.handleEvent(*record.Event)
		}
	case "response":
		if record.ID == "" {
			return
		}
		c.mu.Lock()
		ch, ok := c.inflight[record.ID]
		delete(c.inflight, record.ID)
		c.mu.Unlock()
		if ok {
			ch <- record.Response
		}
	}
}

func (c *Client) handleEvent(e Event) {
	c.mu.Lock()
	switch e.Type {
	case EventSessionStart:
		if e.SessionID != "" {
			c.state.SessionID = e.SessionID
		}
		var p SessionStartPayload
		if e.DecodePayload(&p) == nil && p.Model != "" {
			c.state.Model = p.Model
		}
	case EventAgentStart:
		if !c.state.IsStreaming {
			c.state.IsStreaming = true
			c.settledCh = make(chan struct{})
		}
	case EventAgentSettled:
		if c.state.IsStreaming {
			c.state.IsStreaming = false
			close(c.settledCh)
		}
	case EventSessionEnd:
		if c.state.IsStreaming {
			c.state.IsStreaming = false
			close(c.settledCh)
		}
	case EventToolExecutionStart:
		var p ToolExecutionStartPayload
		if e.DecodePayload(&p) == nil && p.ToolCallID != "" {
			c.pending[p.ToolCallID] = struct{}{}
			c.state.PendingToolCalls = len(c.pending)
		}
	case EventToolExecutionEnd:
		var p ToolExecutionEndPayload
		if e.DecodePayload(&p) == nil && p.ToolCallID != "" {
			delete(c.pending, p.ToolCallID)
			c.state.PendingToolCalls = len(c.pending)
		}
	case EventAgentEnd:
		var p AgentEndPayload
		if e.DecodePayload(&p) == nil && p.Error != "" {
			c.state.Error = p.Error
		}
	case EventModelChanged:
		var p ModelChangedPayload
		if e.DecodePayload(&p) == nil && p.Model != "" {
			c.state.Model = p.Model
		}
	case EventThinkingLevelChanged:
		var p ThinkingLevelChangedPayload
		if e.DecodePayload(&p) == nil && p.Level != "" {
			c.state.Thinking = p.Level
		}
	}
	subs := make([]func(Event), 0, len(c.subs))
	for _, fn := range c.subs {
		subs = append(subs, fn)
	}
	c.mu.Unlock()

	for _, fn := range subs {
		callSubscriber(fn, e)
	}
}

var (
	_ Runtime           = (*MemoryRuntime)(nil)
	_ QueueController   = (*MemoryRuntime)(nil)
	_ Compactor         = (*MemoryRuntime)(nil)
	_ SessionController = (*MemoryRuntime)(nil)
	_ CommandProvider   = (*MemoryRuntime)(nil)
	_ Runtime           = (*Client)(nil)
	_ QueueController   = (*Client)(nil)
	_ Compactor         = (*Client)(nil)
	_ SessionController = (*Client)(nil)
	_ CommandProvider   = (*Client)(nil)
)
