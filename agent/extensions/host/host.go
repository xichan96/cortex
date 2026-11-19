package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Defaults applied by Start when Options leaves a field at its zero value.
const (
	DefaultTimeout     = 30 * time.Second
	DefaultMaxRestarts = 3
	shutdownGrace      = 2 * time.Second
)

// Host lifecycle errors.
var (
	ErrClosed          = errors.New("pluginhost: host is closed")
	ErrPluginExited    = errors.New("pluginhost: plugin process exited")
	ErrRestartLimit    = errors.New("pluginhost: restart limit reached")
	ErrNoCommand       = errors.New("pluginhost: command is required")
	ErrNilHost         = errors.New("pluginhost: nil host")
	ErrNoToolRegistrar = errors.New("pluginhost: tools registrar is not available")
)

// ExecSpec describes the plugin process to launch. Env replaces the inherited
// environment when non-nil, matching os/exec.Cmd.Env (design §7.5 credential
// isolation is left to the caller).
type ExecSpec struct {
	Command string
	Args    []string
	Env     []string
	Dir     string
}

// Options tunes one Host.
type Options struct {
	// Timeout bounds every protocol request, including the initialize
	// handshake. Zero uses DefaultTimeout.
	Timeout time.Duration
	// MaxRestarts is the number of times a crashed plugin is respawned.
	// Zero uses DefaultMaxRestarts; a negative value disables restarts.
	MaxRestarts int
	// Config is forwarded verbatim in the initialize params.
	Config json.RawMessage
	// Stderr receives the plugin's stderr. Nil discards it.
	Stderr io.Writer
}

func (o Options) normalized() Options {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxRestarts == 0 {
		o.MaxRestarts = DefaultMaxRestarts
	}
	if o.MaxRestarts < 0 {
		o.MaxRestarts = 0
	}
	return o
}

// Host owns one plugin child process. All methods are safe for concurrent use.
// A Host is created stopped; Start spawns and handshakes the process.
type Host struct {
	spec ExecSpec
	opts Options

	mu       sync.Mutex
	closed   bool
	started  bool
	restarts int
	sess     *session
	info     PluginInfo
}

// Start launches spec, performs the initialize handshake, and returns a ready
// Host. A handshake or protocol-version failure terminates the child and is
// returned to the caller without affecting the rest of the session.
func Start(ctx context.Context, spec ExecSpec, opts Options) (*Host, error) {
	if spec.Command == "" {
		return nil, ErrNoCommand
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h := &Host{spec: spec, opts: opts.normalized()}
	if _, err := h.acquire(ctx); err != nil {
		return nil, err
	}
	return h, nil
}

// Info returns the plugin descriptor from the initialize handshake. It is zero
// before Start succeeds.
func (h *Host) Info() PluginInfo {
	if h == nil {
		return PluginInfo{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.info
}

// ListTools asks the plugin for its tool declarations.
func (h *Host) ListTools(ctx context.Context) ([]ToolSpec, error) {
	ctx, cancel := h.requestContext(ctx)
	defer cancel()

	s, err := h.acquire(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.call(ctx, MethodToolsList, nil, nil)
	if err != nil {
		return nil, err
	}
	var out toolsListResult
	if len(result) > 0 {
		if err := json.Unmarshal(result, &out); err != nil {
			return nil, fmt.Errorf("pluginhost: decode tools/list result: %w", err)
		}
	}
	return out.Tools, nil
}

// CallTool executes name with the raw JSON arguments and returns the raw JSON
// result. Streamed progress updates are discarded; use CallToolStream to
// observe them.
func (h *Host) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return h.CallToolStream(ctx, name, args, nil)
}

// CallToolStream executes name and invokes onUpdate for every progress event
// the plugin streams for this call. If onUpdate returns an error the call is
// cancelled and the error is returned. onUpdate runs on the calling goroutine.
func (h *Host) CallToolStream(ctx context.Context, name string, args json.RawMessage, onUpdate func(ToolUpdate) error) (json.RawMessage, error) {
	ctx, cancel := h.requestContext(ctx)
	defer cancel()

	if name == "" {
		return nil, errors.New("pluginhost: tool name is required")
	}
	s, err := h.acquire(ctx)
	if err != nil {
		return nil, err
	}
	params := callToolParams{Name: name, Arguments: args, Stream: onUpdate != nil}
	return s.call(ctx, MethodToolsCall, params, onUpdate)
}

// Emit asks the plugin to observe or gate an event (hooks/emit). kind selects
// gate or notification semantics; a gate may return DecisionBlock.
func (h *Host) Emit(ctx context.Context, event string, kind EmitKind, payload json.RawMessage) (EmitResult, error) {
	ctx, cancel := h.requestContext(ctx)
	defer cancel()

	if event == "" {
		return EmitResult{}, fmt.Errorf("pluginhost: event is required")
	}
	if kind != EmitGate {
		kind = EmitNone
	}
	s, err := h.acquire(ctx)
	if err != nil {
		return EmitResult{}, err
	}
	result, err := s.call(ctx, MethodHooksEmit, emitParams{Event: event, Kind: kind, Payload: payload}, nil)
	if err != nil {
		return EmitResult{}, err
	}
	var out EmitResult
	if len(result) > 0 && string(result) != "null" {
		if err := json.Unmarshal(result, &out); err != nil {
			return EmitResult{}, fmt.Errorf("pluginhost: decode hooks/emit result: %w", err)
		}
	}
	return out, nil
}

// ListResources asks the plugin for the skill/prompt/MCP resources it exposes.
func (h *Host) ListResources(ctx context.Context) ([]ResourceSpec, error) {
	ctx, cancel := h.requestContext(ctx)
	defer cancel()

	s, err := h.acquire(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.call(ctx, MethodResourcesList, nil, nil)
	if err != nil {
		return nil, err
	}
	var out resourcesListResult
	if len(result) > 0 {
		if err := json.Unmarshal(result, &out); err != nil {
			return nil, fmt.Errorf("pluginhost: decode resources/list result: %w", err)
		}
	}
	return out.Resources, nil
}

// Shutdown performs the orderly shutdown handshake and terminates the process.
// It is idempotent and safe to call concurrently with Close.
func (h *Host) Shutdown(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	s := h.sess
	h.mu.Unlock()

	if s == nil {
		return nil
	}
	_, err := s.call(ctx, MethodShutdown, nil, nil)
	s.terminate(shutdownGrace)
	if err != nil && !errors.Is(err, ErrPluginExited) {
		return fmt.Errorf("pluginhost: shutdown: %w", err)
	}
	return nil
}

// Close terminates the plugin. It is safe to call more than once; subsequent
// calls return nil.
func (h *Host) Close() error {
	if h == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.opts.Timeout+shutdownGrace)
	defer cancel()
	return h.Shutdown(ctx)
}

// acquire returns a live, initialized session, spawning and handshaking a new
// process when the previous one crashed (bounded by Options.MaxRestarts).
func (h *Host) acquire(ctx context.Context) (*session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.acquireLocked(ctx)
}

func (h *Host) acquireLocked(ctx context.Context) (*session, error) {
	if h.closed {
		return nil, ErrClosed
	}
	if h.sess != nil && !h.sess.isDead() {
		return h.sess, nil
	}
	if h.started {
		if h.restarts >= h.opts.MaxRestarts {
			return nil, fmt.Errorf("%w: %d", ErrRestartLimit, h.restarts)
		}
		h.restarts++
	}

	s, err := h.spawn()
	if err != nil {
		return nil, err
	}
	info, err := h.initialize(ctx, s)
	if err != nil {
		s.terminate(0)
		return nil, err
	}
	h.sess = s
	h.info = info
	h.started = true
	return s, nil
}

func (h *Host) spawn() (*session, error) {
	cmd := exec.Command(h.spec.Command, h.spec.Args...)
	cmd.Dir = h.spec.Dir
	if h.spec.Env != nil {
		cmd.Env = h.spec.Env
	}
	if h.opts.Stderr != nil {
		cmd.Stderr = h.opts.Stderr
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pluginhost: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pluginhost: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pluginhost: start %q: %w", h.spec.Command, err)
	}
	s := &session{
		host:    h,
		cmd:     cmd,
		stdin:   stdin,
		stdout:  stdout,
		pending: make(map[uint32]*pendingCall),
		deadCh:  make(chan struct{}),
	}
	go s.readLoop()
	go func() { s.fail(cmd.Wait()) }()
	return s, nil
}

// initialize performs the handshake and validates the protocol version.
func (h *Host) initialize(ctx context.Context, s *session) (PluginInfo, error) {
	params := InitializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    HostCapabilities(),
		Config:          h.opts.Config,
	}
	result, err := s.call(ctx, MethodInitialize, params, nil)
	if err != nil {
		return PluginInfo{}, fmt.Errorf("pluginhost: initialize: %w", err)
	}
	var info PluginInfo
	if len(result) > 0 {
		if err := json.Unmarshal(result, &info); err != nil {
			return PluginInfo{}, fmt.Errorf("pluginhost: decode initialize result: %w", err)
		}
	}
	if info.ProtocolVersion != ProtocolVersion {
		return PluginInfo{}, fmt.Errorf("%w: plugin=%d host=%d", ErrUnsupportedProtocol, info.ProtocolVersion, ProtocolVersion)
	}
	return info, nil
}

// requestContext applies Options.Timeout unless the caller already set an
// earlier deadline.
func (h *Host) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, h.opts.Timeout)
}

// session is one plugin process plus its in-flight calls.
type session struct {
	host   *Host
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  uint32
	pending map[uint32]*pendingCall

	deadOnce sync.Once
	deadCh   chan struct{}
	exitErr  error
}

type pendingCall struct {
	id      uint32
	results chan callResult
	updates chan ToolUpdate
	done    chan struct{}
	once    sync.Once
	onEvent func(ToolUpdate) error
}

type callResult struct {
	frame Frame
	err   error
}

func newPendingCall(id uint32, onEvent func(ToolUpdate) error) *pendingCall {
	return &pendingCall{
		id:      id,
		results: make(chan callResult, 1),
		updates: make(chan ToolUpdate, 32),
		done:    make(chan struct{}),
		onEvent: onEvent,
	}
}

func (p *pendingCall) close() {
	p.once.Do(func() { close(p.done) })
}

func (s *session) isDead() bool {
	select {
	case <-s.deadCh:
		return true
	default:
		return false
	}
}

func (s *session) err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitErr
}

// fail marks the session dead and releases every pending call. It is called by
// both the reader (EOF) and the Wait goroutine; sync.Once keeps it single-shot.
func (s *session) fail(err error) {
	s.deadOnce.Do(func() {
		if err == nil {
			err = io.EOF
		}
		s.mu.Lock()
		s.exitErr = err
		pending := s.pending
		s.pending = make(map[uint32]*pendingCall)
		s.mu.Unlock()

		for _, p := range pending {
			select {
			case p.results <- callResult{err: fmt.Errorf("%w: %v", ErrPluginExited, err)}:
			default:
			}
			p.close()
		}
		close(s.deadCh)
	})
}

func (s *session) readLoop() {
	for {
		frame, err := ReadFrame(s.stdout)
		if err != nil {
			s.fail(err)
			return
		}
		s.dispatch(frame)
	}
}

func (s *session) dispatch(frame Frame) {
	switch frame.Type {
	case FrameResponse, FrameError:
		s.mu.Lock()
		p := s.pending[frame.ID]
		delete(s.pending, frame.ID)
		s.mu.Unlock()
		if p == nil {
			return
		}
		select {
		case p.results <- callResult{frame: frame}:
		case <-p.done:
		}
	case FrameEvent:
		var ev eventMessage
		if len(frame.JSON) > 0 {
			if err := json.Unmarshal(frame.JSON, &ev); err != nil {
				return
			}
		}
		id := frame.ID
		if ev.ID != 0 {
			id = ev.ID
		}
		s.mu.Lock()
		p := s.pending[id]
		s.mu.Unlock()
		if p == nil || p.onEvent == nil {
			return
		}
		update := ToolUpdate{Event: ev.Event, Data: ev.Data}
		select {
		case p.updates <- update:
		case <-p.done:
		}
	case FramePing:
		// No pong: liveness is detected through process exit. A ping is a
		// harmless no-op so future heartbeats remain wire-compatible.
	case FrameCancel:
		// Plugins may cancel host-initiated calls; the caller observes dead
		// silence as a timeout instead.
	}
}

// call sends one request and waits for its response, streaming updates, or
// cancellation.
func (s *session) call(ctx context.Context, method string, params any, onEvent func(ToolUpdate) error) (json.RawMessage, error) {
	req := requestMessage{Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("pluginhost: encode %s params: %w", method, err)
		}
		req.Params = raw
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("pluginhost: encode %s request: %w", method, err)
	}

	p := newPendingCall(s.allocateID(), onEvent)
	s.mu.Lock()
	s.pending[p.id] = p
	s.mu.Unlock()

	if err := s.writeFrame(Frame{Type: FrameRequest, ID: p.id, JSON: body}); err != nil {
		s.removePending(p.id)
		p.close()
		if s.isDead() {
			return nil, fmt.Errorf("%w: %v", ErrPluginExited, s.err())
		}
		return nil, fmt.Errorf("pluginhost: write %s request: %w", method, err)
	}
	defer func() {
		s.removePending(p.id)
		p.close()
	}()

	for {
		select {
		case res := <-p.results:
			if res.err != nil {
				return nil, res.err
			}
			// The reader dispatches events before the response, but select may
			// surface the response first. Drain the buffered updates so callers
			// observe every streamed event in order.
			if err := drainUpdates(p); err != nil {
				return nil, err
			}
			if res.frame.Type == FrameError {
				return nil, decodeRPCError(res.frame)
			}
			return decodeResult(res.frame, method)
		case update := <-p.updates:
			if p.onEvent == nil {
				continue
			}
			if err := p.onEvent(update); err != nil {
				_ = s.writeFrame(Frame{Type: FrameCancel, ID: p.id, JSON: []byte(`{"reason":"update handler failed"}`)})
				return nil, err
			}
		case <-ctx.Done():
			_ = s.writeFrame(Frame{Type: FrameCancel, ID: p.id, JSON: []byte(`{"reason":"canceled"}`)})
			return nil, ctx.Err()
		case <-p.done:
			return nil, fmt.Errorf("%w: %v", ErrPluginExited, s.err())
		case <-s.deadCh:
			err := s.err()
			if err == nil {
				err = io.EOF
			}
			return nil, fmt.Errorf("%w: %v", ErrPluginExited, err)
		}
	}
}

// drainUpdates invokes the pending call's update handler for every event that
// is already buffered. It never blocks: the reader is the only producer and
// signals completion through the response frame.
func drainUpdates(p *pendingCall) error {
	if p.onEvent == nil {
		return nil
	}
	for {
		select {
		case update := <-p.updates:
			if err := p.onEvent(update); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func (s *session) allocateID() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	if s.nextID == 0 {
		s.nextID = 1
	}
	return s.nextID
}

func (s *session) removePending(id uint32) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

func (s *session) writeFrame(f Frame) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return WriteFrame(s.stdin, f)
}

// terminate asks the plugin to exit by closing stdin, then kills it if it does
// not exit within grace.
func (s *session) terminate(grace time.Duration) {
	_ = s.stdin.Close()
	if grace > 0 {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-s.deadCh:
			return
		case <-timer.C:
		}
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-s.deadCh:
	case <-timer.C:
	}
}

func decodeResult(frame Frame, method string) (json.RawMessage, error) {
	if len(frame.JSON) == 0 {
		return nil, nil
	}
	var resp responseMessage
	if err := json.Unmarshal(frame.JSON, &resp); err != nil {
		return nil, fmt.Errorf("pluginhost: decode %s response: %w", method, err)
	}
	return resp.Result, nil
}

func decodeRPCError(frame Frame) error {
	var rpc RPCError
	if len(frame.JSON) == 0 {
		return &RPCError{Code: CodeInternal, Message: "empty error frame"}
	}
	if err := json.Unmarshal(frame.JSON, &rpc); err != nil {
		return &RPCError{Code: CodeInternal, Message: string(frame.JSON)}
	}
	return &rpc
}
