package dino

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"
)

type Client struct {
	factory  DinoFactory
	cfg      *clientConfig
	mu       sync.RWMutex
	sessions map[string]*ClientSession
}

type ClientOption func(*clientConfig)

type clientConfig struct {
	sessionBufferSize int
	enableQueue       bool
	queueMaxSize      int
	queueMaxPending   int
}

func WithSessionBufferSize(size int) ClientOption {
	return func(c *clientConfig) {
		c.sessionBufferSize = size
	}
}

func WithSessionQueueEnabled(maxSize, maxPending int) ClientOption {
	return func(c *clientConfig) {
		c.enableQueue = true
		c.queueMaxSize = maxSize
		c.queueMaxPending = maxPending
	}
}

func NewClient(factory DinoFactory, opts ...ClientOption) *Client {
	cfg := &clientConfig{
		sessionBufferSize: 100,
		enableQueue:       false,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return &Client{
		factory:  factory,
		cfg:      cfg,
		sessions: make(map[string]*ClientSession),
	}
}

func (c *Client) CreateSession(ctx context.Context, sessionID string) (*ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if session, exists := c.sessions[sessionID]; exists {
		return session, nil
	}

	var opts []SessionOption
	opts = append(opts, WithInputBufferSize(c.getConfig().sessionBufferSize))
	opts = append(opts, WithOutputBufferSize(c.getConfig().sessionBufferSize))

	if c.getConfig().enableQueue {
		opts = append(opts, WithQueueEnabled(c.getConfig().queueMaxSize, c.getConfig().queueMaxPending))
	}

	session, err := c.factory.CreateSession(ctx, sessionID, opts...)
	if err != nil {
		return nil, err
	}

	bufSize := c.getConfig().sessionBufferSize
	clientSession := &ClientSession{
		Session:    session,
		inputChan:  make(chan interface{}, bufSize),
		outputChan: make(chan *Event, bufSize),
		doneChan:   make(chan struct{}),
		errChan:    make(chan error, 1),
		wg:         sync.WaitGroup{},
	}

	clientSession.wg.Add(1)
	go clientSession.forwardOutput()
	clientSession.wg.Add(1)
	go clientSession.handleInput()

	c.sessions[sessionID] = clientSession

	return clientSession, nil
}

func (c *Client) GetSession(sessionID string) *ClientSession {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessions[sessionID]
}

func (c *Client) CloseSession(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if session, exists := c.sessions[sessionID]; exists {
		session.Close()
		delete(c.sessions, sessionID)
	}
}

func (c *Client) CloseAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, session := range c.sessions {
		session.Close()
	}
	c.sessions = make(map[string]*ClientSession)
}

func (c *Client) getConfig() *clientConfig {
	return c.cfg
}

type SessionLister interface {
	ListSessions() []string
}

func (c *Client) ListSessions() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	sessions := make([]string, 0, len(c.sessions))
	for id := range c.sessions {
		sessions = append(sessions, id)
	}
	return sessions
}

func NewAgentInput(text string) types.AgentInput {
	return types.NewAgentInput(text)
}

type ClientSession struct {
	*Session

	inputChan  chan interface{}
	outputChan chan *Event
	doneChan   chan struct{}
	errChan    chan error
	wg         sync.WaitGroup
	closeOnce  sync.Once
}

func (cs *ClientSession) Input() chan<- interface{} {
	return cs.inputChan
}

func (cs *ClientSession) Output() <-chan *Event {
	return cs.outputChan
}

func (cs *ClientSession) Done() <-chan struct{} {
	return cs.doneChan
}

func (cs *ClientSession) Err() <-chan error {
	return cs.errChan
}

func (cs *ClientSession) Send(ctx context.Context, input types.AgentInput) error {
	select {
	case cs.inputChan <- input:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-cs.doneChan:
		return fmt.Errorf("session closed")
	}
}

func (cs *ClientSession) SendAndWait(ctx context.Context, input types.AgentInput) (*Event, error) {
	if err := cs.Send(ctx, input); err != nil {
		return nil, err
	}

	for {
		select {
		case event, ok := <-cs.outputChan:
			if !ok {
				return nil, fmt.Errorf("session closed")
			}
			if event.IsDone() || event.IsError() {
				var err error
				if event.Error != "" {
					err = errors.New(event.Error)
				}
				return event, err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-cs.doneChan:
			return nil, fmt.Errorf("session closed")
		}
	}
}

func (cs *ClientSession) Subscribe(observer Observer) string {
	return cs.Session.Subscribe(observer)
}

func (cs *ClientSession) Unsubscribe(id string) {
	cs.Session.Unsubscribe(id)
}

func (cs *ClientSession) SubscribeFunc(fn func(*Event)) string {
	return cs.Session.Subscribe(ObserverFunc(fn))
}

func (cs *ClientSession) Close() {
	cs.closeOnce.Do(func() {
		close(cs.doneChan)

		if cs.Session != nil {
			cs.Session.Close()
		}
		cs.wg.Wait()

		close(cs.inputChan)
		close(cs.outputChan)
		close(cs.errChan)
	})
}

func (cs *ClientSession) handleInput() {
	defer cs.wg.Done()

	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("handleInput panic: %v", r)
			logger.Error("[ClientSession]", slog.String("error", err.Error()))
			select {
			case cs.errChan <- err:
			case <-cs.doneChan:
			}
		}
	}()

	if cs.Session == nil {
		return
	}

	input := cs.Session.Input()
	ctx := cs.Session.Context()

	for {
		select {
		case msg, ok := <-cs.inputChan:
			if !ok {
				return
			}
			input <- msg
		case <-ctx.Done():
			return
		}
	}
}

func (cs *ClientSession) sendError(err error) {
	select {
	case cs.errChan <- err:
	default:
	}
}

func (cs *ClientSession) forwardOutput() {
	defer cs.wg.Done()

	defer func() {
		if r := recover(); r != nil {
			err := fmt.Errorf("forwardOutput panic: %v", r)
			logger.Error("[ClientSession]", slog.String("error", err.Error()))
			select {
			case cs.errChan <- err:
			case <-cs.doneChan:
			}
		}
	}()

	if cs.Session == nil {
		return
	}

	output := cs.Session.Output()
	ctx := cs.Session.Context()

	for {
		select {
		case <-cs.doneChan:
			return
		case event, ok := <-output:
			if !ok {
				return
			}
			select {
			case cs.outputChan <- event:
			case <-cs.doneChan:
				return
			case <-ctx.Done():
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

type EventHandler func(event *Event)

type HandlerBuilder struct {
	session      *ClientSession
	onMessage    func(content string)
	onThinking   func(thinking string)
	onToolCall   func(toolName string, input map[string]interface{})
	onToolResult func(toolName string, output interface{})
	onError      func(err string)
	onDone       func()
	onApproval   func(toolName string, approved bool)
}

func (hb *HandlerBuilder) OnMessage(fn func(content string)) *HandlerBuilder {
	hb.onMessage = fn
	return hb
}

func (hb *HandlerBuilder) OnThinking(fn func(thinking string)) *HandlerBuilder {
	hb.onThinking = fn
	return hb
}

func (hb *HandlerBuilder) OnToolCall(fn func(toolName string, input map[string]interface{})) *HandlerBuilder {
	hb.onToolCall = fn
	return hb
}

func (hb *HandlerBuilder) OnToolResult(fn func(toolName string, output interface{})) *HandlerBuilder {
	hb.onToolResult = fn
	return hb
}

func (hb *HandlerBuilder) OnError(fn func(err string)) *HandlerBuilder {
	hb.onError = fn
	return hb
}

func (hb *HandlerBuilder) OnDone(fn func()) *HandlerBuilder {
	hb.onDone = fn
	return hb
}

func (hb *HandlerBuilder) OnApproval(fn func(toolName string, approved bool)) *HandlerBuilder {
	hb.onApproval = fn
	return hb
}

func (hb *HandlerBuilder) Build() string {
	handler := func(event *Event) {
		switch event.Type {
		case EventTypeMessage:
			if hb.onMessage != nil {
				hb.onMessage(event.Content)
			}
		case EventTypeThinking:
			if hb.onThinking != nil {
				hb.onThinking(event.Thinking)
			}
		case EventTypeToolCall:
			if hb.onToolCall != nil {
				hb.onToolCall(event.ToolName, event.ToolInput)
			}
		case EventTypeToolResult:
			if hb.onToolResult != nil {
				hb.onToolResult(event.ToolName, event.ToolOutput)
			}
		case EventTypeError:
			if hb.onError != nil {
				hb.onError(event.Error)
			}
		case EventTypeDone:
			if hb.onDone != nil {
				hb.onDone()
			}
		case EventTypeApproval, EventTypeApproved:
			if hb.onApproval != nil {
				hb.onApproval(event.ToolName, event.Approved)
			}
		}
	}
	return hb.session.SubscribeFunc(handler)
}

func (cs *ClientSession) Handler() *HandlerBuilder {
	return &HandlerBuilder{session: cs}
}
