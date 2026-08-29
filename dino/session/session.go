package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/tools/builtin/runtime"
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
	// UserID 显式归属的 user；空 = 未显式指定（回退配置 DefaultUserID，再回退 "default"）。
	// P3.2 user 全局合并：归属在 session 创建时固化（INSERT OR IGNORE），不动态变更。
	UserID string
}

func DefaultConfig() *Config {
	return &Config{
		InputBufferSize:    64,
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

// WithUserID 显式归属一个 session 到 userID（多租户隔离用）。
// 未传时由 factory 回退配置 DefaultUserID / 常量 "default"。
func WithUserID(userID string) Option {
	return func(c *Config) {
		c.UserID = userID
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
	input   chan interface{}
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
	// wake B2 唤醒源（S4，subagent-s3s4 §7）。nil = 无唤醒，行为同现状。
	wake WakeSource

	observers   map[string]Observer
	observersMu sync.RWMutex

	turnMu            sync.Mutex
	cancelCurrentTurn context.CancelFunc
}

type Observer interface {
	OnEvent(event *Event)
}

type ObserverFunc func(event *Event)

func (f ObserverFunc) OnEvent(event *Event) {
	f(event)
}

// OutputObserver streams session events (same contract as Observer). Subscribe runs
// callbacks synchronously in the session loop; for channel fan-out, use a goroutine
// ranging Session.Output() so slow consumers do not block emit.
type OutputObserver = Observer

func NewSession(id string, agent *engine.AgentEngine, factory SessionFactory, ctx context.Context, cfg *Config, planner *PlannerHelper, budget interface{ CanExecute(sessionID string) bool }, wake WakeSource) *Session {
	sessionCtx, cancel := context.WithCancel(ctx)

	queue := dinoQueue.New(&dinoQueue.Config{
		MaxSize:    cfg.QueueSize,
		MaxPending: cfg.MaxPending,
		Timeout:    5 * time.Minute,
	})

	return &Session{
		id:        id,
		input:     make(chan interface{}, cfg.InputBufferSize),
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
		wake:      wake,
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

func (s *Session) Input() chan<- interface{} {
	return s.input
}

// AnswerQuestion 把用户对 question 工具的回答注入为下一条输入（question-reflow
// §2.2）。toolCallID 与 EventTypeQuestion 事件的 ToolCallID 对应
// （client.go onQuestion 回调）。注入走 s.input 通道，session 循环消费后作为
// 新 turn 执行；回答的 displayContent 标记为 questionAnswerDisplay，UI 折叠显示。
func (s *Session) AnswerQuestion(toolCallID, answer string) error {
	if s == nil {
		return errors.New("question: nil session")
	}
	if toolCallID == "" {
		return errors.New("question: tool_call_id is required")
	}
	text := fmt.Sprintf("[question answer for %s] %s", toolCallID, answer)
	select {
	case s.input <- &questionAnswerInput{text: text}:
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	return nil
}

// questionAnswerInput 是 AnswerQuestion 注入的输入载体（内部类型，避免与普通
// user 输入混淆）。session 循环 type-switch 识别后走 questionAnswerDisplay 标记。
type questionAnswerInput struct {
	text string
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

	// 新增（S4/B2，subagent-s3s4 §7.3）：mailbox 到达唤醒源。nil = 无唤醒，行为同现状。
	var wakeCh <-chan struct{}
	if s.wake != nil {
		wakeCh = s.wake.Wake()
	}

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
			switch in := input.(type) {
			case types.AgentInput:
				s.processInputWithAgentInput(in)
			case *questionAnswerInput:
				s.executeWithInput(s.ctx, types.NewAgentInput(in.text), questionAnswerDisplay)
			case string:
				s.processInput(in)
			default:
				s.processInput(fmt.Sprintf("%v", input))
			}
		case item, ok := <-queueChan:
			if !ok {
				return
			}
			s.processQueueItem(item)
		case _, ok := <-wakeCh:
			if !ok {
				return
			}
			s.onSubagentCompletion()
		}
	}
}

// onSubagentCompletion（S4/B2，subagent-s3s4 §7.5）：收到唤醒信号 → 从唤醒源 Collect
// 全部未读完成 → 每个 payload 注入一个新 turn（父代理自主继续，非用户输入响应）。
// 评审修正：
//   - 只在 idle 时可达（turn 内 select 循环不服务本分支；wait_agent 只在 turn 内阻塞）
//     → Collect（DrainAll）与 wait_agent 的 Drain(taskID) 结构互斥（BLOCKER-2）。
//   - 唤醒可重入：payload 由 Collect 从 mailbox 取走，若预算耗尽被跳过，消息已消费，
//     不重复注入（评审 RECOMMENDED-6：注入前查 budget，避免刷屏）。
func (s *Session) onSubagentCompletion() {
	if s.wake == nil || !s.IsRunning() {
		return
	}
	payloads := s.wake.Collect()
	if len(payloads) == 0 {
		return
	}
	for _, p := range payloads {
		if s.budget != nil && !s.budget.CanExecute(s.id) {
			logger.Warn("[session] subagent completion skipped: budget exhausted",
				slog.String("session_id", s.id),
				slog.String("task_id", p.TaskID))
			continue
		}
		s.executeWithInput(s.ctx, types.NewAgentInput(p.Text), subagentCompletionDisplay)
	}
}

// subagentCompletionDisplay 唤醒 turn 的 displayContent 标记（S4/B2，subagent-s3s4 §7.7）。
// executeWithInput 用它在 EventTypeMessage 上打 Source=subagent，消费方（client.go /
// turn_observe.go）据此折叠，避免"幽灵用户消息"喷到 UI 与 assistant-text 统计。
const subagentCompletionDisplay = "[subagent-completion]"

// questionAnswerDisplay 是 AnswerQuestion 注入 turn 的 displayContent 标记
// （question-reflow §2.2 / 评审 R3）。与 subagent-completion 同理，用它把注入的
// 回答从「用户消息回声」中折叠，避免 UI 噪音与 ObserveOneUserTurn 的统计污染。
const questionAnswerDisplay = "[question-answer]"

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
	return s.executeWithInput(s.ctx, types.NewAgentInput(input), input)
}

func (s *Session) processInputWithAgentInput(agentInput types.AgentInput) *ExecuteResponse {
	return s.executeWithInput(s.ctx, agentInput, agentInput.String())
}

func (s *Session) CancelCurrentTurn() {
	s.turnMu.Lock()
	fn := s.cancelCurrentTurn
	s.turnMu.Unlock()
	if fn != nil {
		fn()
	}
}

func (s *Session) executeWithInput(ctx context.Context, agentInput types.AgentInput, displayContent string) *ExecuteResponse {
	if err := s.ctx.Err(); err != nil {
		return &ExecuteResponse{
			SessionID: s.id,
			Error:     err,
		}
	}

	turnCtx, cancelTurn := context.WithCancel(ctx)
	s.turnMu.Lock()
	s.cancelCurrentTurn = cancelTurn
	s.turnMu.Unlock()
	defer func() {
		s.turnMu.Lock()
		s.cancelCurrentTurn = nil
		s.turnMu.Unlock()
		cancelTurn()
	}()

	if s.budget != nil && !s.budget.CanExecute(s.id) {
		s.emit(&Event{Type: EventTypeError, Error: "Budget exceeded"})
		return &ExecuteResponse{
			SessionID: s.id,
			Error:     fmt.Errorf("budget exceeded"),
		}
	}

	msgEv := &Event{
		Type:    EventTypeMessage,
		Content: displayContent,
		// S4/B2（subagent-s3s4 §7.7）：Source 默认 user；唤醒注入 turn 打 subagent
		// 供消费方折叠（评审 O-3）。JSON omitempty："" 不序列化（旧消费方兼容），
		// 显式 user 序列化为 "user"（语义一致）。
		Source: EventSourceUser,
	}
	if displayContent == subagentCompletionDisplay || displayContent == questionAnswerDisplay {
		msgEv.Source = EventSourceSubagent
	}
	s.emit(msgEv)

	if s.factory != nil {
		if result := s.factory.Detect(turnCtx, s.id, agentutils.LoopDetectAction{Type: "input", Content: displayContent}); result != nil {
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
		plan, err := s.planner.CreatePlan(turnCtx, displayContent)
		if err != nil {
			s.emit(&Event{Type: EventTypeError, Error: fmt.Sprintf("Failed to create plan: %v", err)})
		} else if plan != nil {
			s.emitPlan(plan)

			approved := s.planner.RequestApproval(turnCtx, plan)
			if !approved {
				s.emit(&Event{Type: EventTypeDone})
				return &ExecuteResponse{
					SessionID: s.id,
					Content:   "Plan rejected by user",
				}
			}
		}
	}

	stream, err := s.agent.ExecuteStream(turnCtx, agentInput, nil)
	if err != nil {
		s.emit(&Event{Type: EventTypeError, Error: err.Error()})
		return &ExecuteResponse{SessionID: s.id, Error: err}
	}

	var execResult *types.AgentResult
	var streamed strings.Builder
	for result := range stream {
		if result.Error != nil {
			ev := &Event{Type: EventTypeError, Error: result.Error.Error()}
			if result.StopCause != "" {
				ev.StopCause = string(result.StopCause)
			}
			s.emit(ev)
			return &ExecuteResponse{SessionID: s.id, Error: result.Error}
		}
		switch result.Type {
		case "reasoning":
			s.emit(&Event{Type: EventTypeThinking, Content: result.Content})
		case "chunk":
			streamed.WriteString(result.Content)
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
					// P2.1 question 工具：输出为 SentinelQuestionResult（AskUser=true）
					// 时 emit EventTypeQuestion，UI 拿到问题后可回答注入。
					if result.ToolEvent.ToolName == "question" {
						if q := questionFromOutput(result.ToolEvent.Output); q != "" {
							s.emit(&Event{
								Type:       EventTypeQuestion,
								SessionID:  s.id,
								ToolName:   result.ToolEvent.ToolName,
								ToolCallID: result.ToolEvent.ToolCallID,
								Question:   q,
							})
						}
					}
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
		if errors.Is(turnCtx.Err(), context.Canceled) {
			s.emit(&Event{Type: EventTypeError, Error: "cancelled"})
		}
		s.emit(&Event{Type: EventTypeDone})
		return &ExecuteResponse{SessionID: s.id, Error: turnCtx.Err()}
	}

	if s.factory != nil {
		s.factory.RecordTokens(turnCtx, s.id, execResult.Usage.TotalTokens)
	}

	out := execResult.Output
	if out != "" {
		acc := streamed.String()
		switch {
		case acc == "":
			s.emit(&Event{Type: EventTypeMessage, Content: out})
		case out == acc:
		case strings.HasPrefix(out, acc):
			if tail := out[len(acc):]; tail != "" {
				s.emit(&Event{Type: EventTypeMessage, Content: tail})
			}
		case strings.HasSuffix(acc, out):
		default:
			s.emit(&Event{Type: EventTypeMessage, Content: out})
		}
	}

	if execResult.Usage.TotalTokens > 0 {
		s.emit(&Event{
			Type:  EventTypeTokenUsage,
			Usage: &execResult.Usage,
		})
	}

	doneEv := &Event{Type: EventTypeDone}
	if execResult.StopCause != "" {
		doneEv.StopCause = string(execResult.StopCause)
	}
	s.emit(doneEv)

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

// questionFromOutput 从 question 工具输出提取提问内容（P2.1）。
// SentinelQuestionResult 可能以命名 struct 或 JSON 反序列化后的 map 形态出现；
// 非 question sentinel 返回空串。
//
// 评审 B1（question-reflow）：struct 分支必须断言命名类型
// runtime.SentinelQuestionResult —— Go 的类型断言对「命名类型 vs 匿名 struct」
// 要求类型同一性，匿名 struct 断言对命名类型永不命中（此前 P2.1 的
// EventTypeQuestion 因此在真实流中从未触发）。
func questionFromOutput(output interface{}) string {
	if output == nil {
		return ""
	}
	// struct 形态：命名类型 runtime.SentinelQuestionResult。
	if v, ok := output.(runtime.SentinelQuestionResult); ok {
		if v.AskUser {
			return v.Question
		}
		return ""
	}
	// map 形态（工具结果经 FormatToolResult / JSON round-trip 后）。
	if m, ok := output.(map[string]interface{}); ok {
		if ask, ok := m["ask_user"].(bool); ok && ask {
			if q, ok := m["question"].(string); ok {
				return q
			}
		}
	}
	return ""
}
