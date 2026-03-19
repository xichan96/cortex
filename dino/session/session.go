package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"

	agentutils "github.com/xichan96/cortex/agent/utils"
	dinoQueue "github.com/xichan96/cortex/dino/queue"
)

type Budget interface {
	CanExecute(sessionID string) bool
}

type Config struct {
	InputBufferSize    int
	OutputBufferSize   int
	EnableQueue        bool
	QueueSize          int
	MaxPending         int
	PlannerEnabled     bool
	PlannerPrompt      string
	PlannerAutoApprove bool
}

func DefaultConfig() *Config {
	return &Config{
		InputBufferSize:    10,
		OutputBufferSize:   10,
		EnableQueue:        false,
		QueueSize:          100,
		MaxPending:         10,
		PlannerEnabled:     false,
		PlannerPrompt:      "Think silently about the steps needed. First explain your plan, then proceed.",
		PlannerAutoApprove: false,
	}
}

type Option func(*Config)

func WithInputBufferSize(size int) Option {
	return func(c *Config) {
		c.InputBufferSize = size
	}
}

func WithOutputBufferSize(size int) Option {
	return func(c *Config) {
		c.OutputBufferSize = size
	}
}

func WithQueueEnabled(maxSize, maxPending int) Option {
	return func(c *Config) {
		c.EnableQueue = true
		c.QueueSize = maxSize
		c.MaxPending = maxPending
	}
}

func WithPlannerEnabled(enabled bool, prompt string, autoApprove bool) Option {
	return func(c *Config) {
		c.PlannerEnabled = enabled
		c.PlannerPrompt = prompt
		c.PlannerAutoApprove = autoApprove
	}
}

type SessionFactory interface {
	RecordLoop(sessionID string, action agentutils.LoopDetectAction)
	RecordTokens(ctx context.Context, sessionID string, tokens int)
	Detect(ctx context.Context, sessionID string, action agentutils.LoopDetectAction) *agentutils.LoopDetectResult
}

type budgetChecker interface {
	CanExecute(sessionID string) bool
}

type Session struct {
	id      string
	input   chan string
	output  chan *Event
	ctx     context.Context
	cancel  context.CancelFunc
	agent   *engine.AgentEngine
	factory SessionFactory
	mu      sync.RWMutex
	running bool
	queue   dinoQueue.Interface
	wg      sync.WaitGroup
	config  *Config
	planner *PlannerHelper
	budget  budgetChecker

	observers   map[string]Observer
	observersMu sync.RWMutex
}

type Observer interface {
	OnEvent(event *Event)
}

type ObserverFunc func(event *Event)

func (f ObserverFunc) OnEvent(event *Event) {
	f(event)
}

func NewSession(id string, agent *engine.AgentEngine, factory SessionFactory, ctx context.Context, cfg *Config, planner *PlannerHelper, budget interface{ CanExecute(sessionID string) bool }) *Session {
	sessionCtx, cancel := context.WithCancel(ctx)

	queue := dinoQueue.New(&dinoQueue.Config{
		MaxSize:    cfg.QueueSize,
		MaxPending: cfg.MaxPending,
		Timeout:    5 * time.Minute,
	})

	return &Session{
		id:        id,
		input:     make(chan string, cfg.InputBufferSize),
		output:    make(chan *Event, cfg.OutputBufferSize),
		agent:     agent,
		factory:   factory,
		ctx:       sessionCtx,
		cancel:    cancel,
		running:   true,
		queue:     queue,
		config:    cfg,
		planner:   planner,
		budget:    budget,
		observers: make(map[string]Observer),
	}
}

func (s *Session) Start() {
	s.wg.Add(1)
	go s.run()
}

func (s *Session) ID() string {
	return s.id
}

func (s *Session) Input() chan<- string {
	return s.input
}

func (s *Session) Output() <-chan *Event {
	return s.output
}

func (s *Session) Context() context.Context {
	return s.ctx
}

func (s *Session) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

func (s *Session) GetAgent() *engine.AgentEngine {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agent
}

func (s *Session) GetMemory() types.MemoryProvider {
	if s.agent == nil {
		return nil
	}
	return s.agent.GetMemory()
}

func (s *Session) Subscribe(obs Observer) string {
	id := generateUUID()
	s.observersMu.Lock()
	s.observers[id] = obs
	s.observersMu.Unlock()
	return id
}

func (s *Session) Unsubscribe(id string) {
	s.observersMu.Lock()
	delete(s.observers, id)
	s.observersMu.Unlock()
}

func (s *Session) notifyObservers(event *Event) {
	s.observersMu.RLock()
	observers := make([]Observer, 0, len(s.observers))
	for _, obs := range s.observers {
		observers = append(observers, obs)
	}
	s.observersMu.RUnlock()

	for _, obs := range observers {
		obs.OnEvent(event)
	}
}

func (s *Session) emit(event *Event) {
	event.Timestamp = time.Now()
	event.SessionID = s.id
	s.notifyObservers(event)
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	select {
	case s.output <- event:
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			logger.Error("[session] event send timeout",
				slog.String("session_id", s.id),
				slog.String("type", string(event.Type)))
		} else {
			logger.Warn("[session] event not sent: session closed",
				slog.String("session_id", s.id),
				slog.String("type", string(event.Type)))
		}
	}
}

func (s *Session) emitPlan(plan *Plan) {
	s.emit(&Event{
		Type: EventTypePlan,
		Plan: plan,
	})

	for _, step := range plan.Steps {
		s.emit(&Event{
			Type:    EventTypePlanStep,
			Content: fmt.Sprintf("%d. %s: %v", step.Index+1, step.Tool, step.Input),
		})
	}
}

func (s *Session) run() {
	defer s.wg.Done()
	defer close(s.output)

	queueChan := func() <-chan *dinoQueue.Item {
		if s.queue != nil {
			return s.queue.OutputChan()
		}
		return nil
	}()

	for {
		select {
		case <-s.ctx.Done():
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
			return
		case input, ok := <-s.input:
			if !ok {
				return
			}
			s.processInput(input)
		case item, ok := <-queueChan:
			if !ok {
				return
			}
			s.processQueueItem(item)
		}
	}
}

func (s *Session) processQueueItem(item *dinoQueue.Item) {
	startTime := time.Now()
	result := s.processInputWithResult(item.Content)
	duration := time.Since(startTime)

	if s.factory != nil {
		s.factory.RecordLoop(s.id, agentutils.LoopDetectAction{Type: "output", Content: result.Content})
	}
	if result.ToolCalls != nil && s.factory != nil {
		s.factory.RecordTokens(s.ctx, s.id, result.Usage.TotalTokens)
	}

	s.queue.Complete(item, &dinoQueue.Result{
		Output:   result.Content,
		Error:    result.Error,
		Duration: duration,
	})
}

func (s *Session) processInput(input string) *ExecuteResponse {
	return s.processInputWithResult(input)
}

func (s *Session) processInputWithResult(input string) *ExecuteResponse {
	if err := s.ctx.Err(); err != nil {
		return &ExecuteResponse{
			SessionID: s.id,
			Error:     err,
		}
	}

	if s.budget != nil && !s.budget.CanExecute(s.id) {
		s.emit(&Event{Type: EventTypeError, Error: "Budget exceeded"})
		return &ExecuteResponse{
			SessionID: s.id,
			Error:     fmt.Errorf("budget exceeded"),
		}
	}

	s.emit(&Event{
		Type:    EventTypeMessage,
		Content: input,
	})

	if s.factory != nil {
		if result := s.factory.Detect(s.ctx, s.id, agentutils.LoopDetectAction{Type: "input", Content: input}); result != nil {
			if result.IsLoop {
				logger.Warn("[session] loop detected",
					slog.String("session_id", s.id),
					slog.String("suggestion", result.Suggestion))
				s.emit(&Event{Type: EventTypeError, Error: result.Suggestion})
				return &ExecuteResponse{SessionID: s.id, Error: fmt.Errorf("%s", result.Suggestion)}
			}
			if result.Count > 0 {
				logger.Info("[session] repeated action count",
					slog.String("session_id", s.id),
					slog.Int("count", result.Count))
			}
		}
	}

	if s.planner != nil && s.planner.IsEnabled() {
		plan, err := s.planner.CreatePlan(s.ctx, input)
		if err != nil {
			s.emit(&Event{Type: EventTypeError, Error: fmt.Sprintf("Failed to create plan: %v", err)})
		} else if plan != nil {
			s.emitPlan(plan)

			approved := s.planner.RequestApproval(s.ctx, plan)
			if !approved {
				s.emit(&Event{Type: EventTypeDone})
				return &ExecuteResponse{
					SessionID: s.id,
					Content:   "Plan rejected by user",
				}
			}
		}
	}

	agentInput := types.NewAgentInput(input)
	stream, err := s.agent.ExecuteStream(s.ctx, agentInput, nil)
	if err != nil {
		s.emit(&Event{Type: EventTypeError, Error: err.Error()})
		return &ExecuteResponse{SessionID: s.id, Error: err}
	}

	var execResult *types.AgentResult
	for result := range stream {
		if result.Error != nil {
			s.emit(&Event{Type: EventTypeError, Error: result.Error.Error()})
			return &ExecuteResponse{SessionID: s.id, Error: result.Error}
		}
		switch result.Type {
		case "reasoning":
			s.emit(&Event{Type: EventTypeThinking, Content: result.Content})
		case "chunk":
			s.emit(&Event{Type: EventTypeMessage, Content: result.Content})
		case "tool_call":
			if result.ToolEvent != nil {
				s.emit(&Event{
					Type:      EventTypeToolCall,
					ToolName:  result.ToolEvent.ToolName,
					ToolInput: result.ToolEvent.Input,
				})
			}
		case "tool_event":
			if result.ToolEvent != nil {
				switch result.ToolEvent.Event {
				case "tool_call", "tool_input_start":
					s.emit(&Event{
						Type:       EventTypeToolCall,
						ToolName:   result.ToolEvent.ToolName,
						ToolCallID: result.ToolEvent.ToolCallID,
						ToolInput:  result.ToolEvent.Input,
					})
				case "tool_result":
					s.emit(&Event{
						Type:       EventTypeToolResult,
						ToolName:   result.ToolEvent.ToolName,
						ToolCallID: result.ToolEvent.ToolCallID,
						ToolOutput: result.ToolEvent.Output,
					})
				case "tool_error":
					s.emit(&Event{
						Type:       EventTypeError,
						Content:    result.ToolEvent.Error,
						ToolName:   result.ToolEvent.ToolName,
						ToolCallID: result.ToolEvent.ToolCallID,
					})
				}
			}
		}
		if result.Result != nil {
			execResult = result.Result
		}
	}

	if execResult == nil {
		return &ExecuteResponse{SessionID: s.id}
	}

	if s.factory != nil {
		s.factory.RecordTokens(s.ctx, s.id, execResult.Usage.TotalTokens)
	}

	s.emit(&Event{Type: EventTypeMessage, Content: execResult.Output})

	if execResult.Usage.TotalTokens > 0 {
		s.emit(&Event{
			Type:  EventTypeTokenUsage,
			Usage: &execResult.Usage,
		})
	}

	s.emit(&Event{Type: EventTypeDone})

	return &ExecuteResponse{
		SessionID: s.id,
		Content:   execResult.Output,
		Usage:     execResult.Usage,
	}
}

func (s *Session) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		s.cancel()
		s.running = false
	}
}

func (s *Session) Close() {
	s.Stop()
	if s.queue != nil {
		s.queue.Close()
	}
	s.wg.Wait()

	s.observersMu.Lock()
	for id := range s.observers {
		delete(s.observers, id)
	}
	s.observersMu.Unlock()
}

func (s *Session) Enqueue(content string, priority dinoQueue.Priority) (<-chan *dinoQueue.Result, error) {
	return s.queue.Enqueue(s.ctx, content, priority)
}

func (s *Session) EnqueueBatch(contents []string, priority dinoQueue.Priority) ([]<-chan *dinoQueue.Result, error) {
	return s.queue.EnqueueBatch(s.ctx, contents, priority)
}

func (s *Session) QueueSize() int {
	return s.queue.Size()
}

func (s *Session) QueuePending() int {
	return s.queue.Pending()
}

func (s *Session) QueueStats() dinoQueue.Stats {
	return s.queue.GetStats()
}

func (s *Session) ApprovePlan(approved bool) {
	if s.planner != nil {
		s.planner.Approve(approved)
	}
}

type ExecuteResponse struct {
	SessionID string
	Content   string
	ToolCalls []ToolCallInfo
	Usage     Usage
	Error     error
}

type ToolCallInfo struct {
	ID    string
	Name  string
	Input map[string]interface{}
}

func generateUUID() string {
	return uuid.New().String()
}
