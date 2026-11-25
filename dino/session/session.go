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
	"github.com/xichan96/cortex/agent/question"
	"github.com/xichan96/cortex/agent/tools/builtin/runtime"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/chatstore"
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
	// ExtraSystemPrompt 会话级系统提示词，**追加**在工厂级 SystemPrompt 之后。
	// 空 = 只用工厂级。用于「同一个 agent 在不同会话里承担不同角色」。
	ExtraSystemPrompt string
	// QuestionBlocking 打开 question 工具的同轮阻塞模式：工具在 turn 内 emit
	// 一次 EventTypeQuestion（QuestionPending=true）并等待答案，答案作为 tool
	// result 返回，模型继续本轮。false（默认）保持旧行为：工具返回哨兵、
	// 本轮结束，答案通过 AnswerQuestion 作为新输入开启下一轮。
	QuestionBlocking bool
	// QuestionTimeout 是同轮阻塞的等待上限。<=0 表示只受 turn ctx（工具超时 /
	// 用户取消）约束。超时返回 question.ErrQuestionTimeout，不会永久卡住。
	QuestionTimeout time.Duration
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

// WithExtraSystemPrompt 给本会话追加系统提示词（工厂级 SystemPrompt 之后）。
// 追加而非替换：工厂级提示词承载工具选择、行为准则等全局约束，替换掉会让
// 模型立刻偏离既定行为。
func WithExtraSystemPrompt(prompt string) Option {
	return func(c *Config) {
		c.ExtraSystemPrompt = prompt
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

// WithQuestionBlocking enables same-turn blocking for the question tool: the
// tool waits for the user's answer and returns it as its tool result. timeout
// bounds the wait; <=0 relies on the turn/tool context.
func WithQuestionBlocking(enabled bool, timeout time.Duration) Option {
	return func(c *Config) {
		c.QuestionBlocking = enabled
		c.QuestionTimeout = timeout
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

	// 同轮阻塞提问的待答表（QuestionBlocking=true 时使用）。
	// questionMu 保护两张表：pendingQuestions 以请求 ID 为键，questionToolCalls
	// 把 tool_call_id 映射到请求 ID（兼容只拿到 tool call ID 的旧消费方）。
	// 每个请求独立 channel，答案按 ID 路由，并发提问互不串扰。
	questionMu        sync.Mutex
	pendingQuestions  map[string]*pendingQuestion
	questionToolCalls map[string]string
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

		pendingQuestions:  make(map[string]*pendingQuestion),
		questionToolCalls: make(map[string]string),
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

// pendingQuestion 是一次等待中的结构化提问。answers 只需 1 个缓冲：投递方
// 在投递前先从表里摘除，保证「只答一次」。
type pendingQuestion struct {
	requestID  string
	toolCallID string
	answers    chan question.Answers
}

// AnswerQuestionWithData 回答一次**同轮阻塞**的提问（QuestionPending=true 的
// EventTypeQuestion）。id 可以是事件里的 QuestionID（请求 ID），也可以是
// ToolCallID（兼容只持有 tool call ID 的消费方）。答案按请求 ID 路由到对应
// 的等待者，作为 question 工具的 tool result 返回，本轮继续。
//
// 空闲/旧模式下没有等待中的请求时返回 question.ErrUnknownQuestion；旧模式
// 回答请继续使用 AnswerQuestion。
func (s *Session) AnswerQuestionWithData(id string, answers map[string][]string) error {
	if s == nil {
		return errors.New("question: nil session")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("question: request id is required")
	}
	return s.deliverQuestionAnswers(id, question.Answers(answers))
}

// deliverQuestionAnswers 按请求 ID（或 tool_call_id）投递答案。
func (s *Session) deliverQuestionAnswers(id string, answers question.Answers) error {
	s.questionMu.Lock()
	pq := s.pendingQuestions[id]
	if pq == nil {
		if requestID, ok := s.questionToolCalls[id]; ok {
			pq = s.pendingQuestions[requestID]
		}
	}
	if pq == nil {
		s.questionMu.Unlock()
		return fmt.Errorf("%w: %s", question.ErrUnknownQuestion, id)
	}
	// 投递前摘除：重复回答得到 ErrUnknownQuestion，而不会阻塞或覆盖。
	delete(s.pendingQuestions, pq.requestID)
	if pq.toolCallID != "" {
		delete(s.questionToolCalls, pq.toolCallID)
	}
	s.questionMu.Unlock()

	select {
	case pq.answers <- question.NormalizeAnswers(answers):
		return nil
	default:
		return fmt.Errorf("%w: %s", question.ErrUnknownQuestion, id)
	}
}

func (s *Session) registerPendingQuestion(pq *pendingQuestion) error {
	s.questionMu.Lock()
	defer s.questionMu.Unlock()
	if _, ok := s.pendingQuestions[pq.requestID]; ok {
		return fmt.Errorf("%w: %s", question.ErrDuplicateQuestion, pq.requestID)
	}
	if pq.toolCallID != "" {
		if _, taken := s.questionToolCalls[pq.toolCallID]; taken {
			return fmt.Errorf("%w: tool_call_id %s", question.ErrDuplicateQuestion, pq.toolCallID)
		}
		s.questionToolCalls[pq.toolCallID] = pq.requestID
	}
	s.pendingQuestions[pq.requestID] = pq
	return nil
}

func (s *Session) unregisterPendingQuestion(pq *pendingQuestion) {
	s.questionMu.Lock()
	defer s.questionMu.Unlock()
	if cur, ok := s.pendingQuestions[pq.requestID]; ok && cur == pq {
		delete(s.pendingQuestions, pq.requestID)
	}
	if pq.toolCallID != "" {
		if requestID, ok := s.questionToolCalls[pq.toolCallID]; ok && requestID == pq.requestID {
			delete(s.questionToolCalls, pq.toolCallID)
		}
	}
}

// PendingQuestionIDs 返回当前等待回答的请求 ID（调试/管理用）。
func (s *Session) PendingQuestionIDs() []string {
	if s == nil {
		return nil
	}
	s.questionMu.Lock()
	defer s.questionMu.Unlock()
	out := make([]string, 0, len(s.pendingQuestions))
	for id := range s.pendingQuestions {
		out = append(out, id)
	}
	return out
}

// sessionQuestionAsker 把 Session 适配成 question.Asker：emit 一次问题事件，
// 然后阻塞等待 AnswerQuestionWithData。turn ctx 取消、会话关闭、请求超时都会
// 返回明确错误并清理待答表，不会永久卡住。
type sessionQuestionAsker struct {
	sess *Session
}

func (a *sessionQuestionAsker) Ask(ctx context.Context, req question.Request) (question.Answers, error) {
	if a == nil || a.sess == nil {
		return nil, question.ErrQuestionUnavailable
	}
	return a.sess.askQuestion(ctx, req)
}

func (s *Session) askQuestion(ctx context.Context, req question.Request) (question.Answers, error) {
	if len(req.Questions) == 0 {
		return nil, fmt.Errorf("question: no questions to ask")
	}
	requestID := strings.TrimSpace(req.ID)
	if requestID == "" {
		requestID = "question-" + uuid.NewString()
	}
	pq := &pendingQuestion{
		requestID:  requestID,
		toolCallID: req.ToolCallID,
		answers:    make(chan question.Answers, 1),
	}
	if err := s.registerPendingQuestion(pq); err != nil {
		return nil, err
	}
	defer s.unregisterPendingQuestion(pq)

	// 只 emit 一次：事件用请求 ID 作为 QuestionID，ToolCallID 关联工具分片。
	s.emit(&Event{
		Type:            EventTypeQuestion,
		ToolName:        "question",
		ToolCallID:      req.ToolCallID,
		QuestionID:      requestID,
		Question:        question.FirstText(req.Questions),
		Questions:       req.Questions,
		QuestionPending: true,
	})

	var timeoutCh <-chan time.Time
	if s.config != nil && s.config.QuestionTimeout > 0 {
		timer := time.NewTimer(s.config.QuestionTimeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	select {
	case answers := <-pq.answers:
		return answers, nil
	case <-timeoutCh:
		return nil, fmt.Errorf("%w: %s", question.ErrQuestionTimeout, requestID)
	case <-ctx.Done():
		return nil, question.ContextError(requestID, ctx.Err())
	case <-s.ctx.Done():
		return nil, fmt.Errorf("%w: %s: session closed", question.ErrQuestionCancelled, requestID)
	}
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

// CompactResult is the engine's manual compaction report, re-exported so hosts
// that only import dino/session can describe the outcome without also importing
// agent/engine.
type CompactResult = engine.CompactResult

// Compact manually compacts this session's memory (the manual /compact entry
// point). It blocks until compression finishes and shares the engine's
// memoryCompressMu with automatic compression, so the two never run
// concurrently; a call made while an automatic compression is in flight waits
// for it to finish.
//
// A session whose stored message count is at or below the provider's retention
// window is a no-op: it returns a result with Compacted == 0 and no error.
func (s *Session) Compact(ctx context.Context) (*CompactResult, error) {
	return s.CompactWithInstructions(ctx, "")
}

// CompactWithInstructions 手动压缩，并把用户关注点带进摘要提示词
// （/compact <focus>）。空 instructions 等价于 Compact。
func (s *Session) CompactWithInstructions(ctx context.Context, instructions string) (*CompactResult, error) {
	agent := s.GetAgent()
	if agent == nil {
		return nil, errors.New("session: agent is not available")
	}
	if strings.TrimSpace(instructions) != "" {
		ctx = chatstore.WithCompactionInstructions(ctx, instructions)
	}
	return agent.CompactMemory(ctx)
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

// EmitApprovalRequest 把一次「工具等待审批」推给观察者。
//
// 为什么需要导出：审批是**阻塞等待用户输入**的（ApprovalStore 里 select 等
// channel），而消费方（上层 UI/网关）只有通过事件才知道「现在有一个审批在等」。
// 没有这条事件，用户永远看不到请求，只能等超时——那和「静默拒绝」没区别。
//
// 与 EventTypeApproval 的区别：那个是**策略已给出结论**（如自动批准），
// 这条是**请求尚在等待**，载荷带 requestID 供消费方回灌决议。
func (s *Session) EmitApprovalRequest(requestID, toolName, arguments string) {
	s.emit(&Event{
		Type:            EventTypeApproval,
		ToolName:        toolName,
		RequestID:       requestID,
		Content:         arguments,
		ApprovalPending: true,
	})
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

// resetTurnBudget clears a per-session budget before a new run when the
// implementation supports Reset. Kept as an optional interface so old fake
// budgets that only implement CanExecute keep compiling.
func resetTurnBudget(budget interface{ CanExecute(sessionID string) bool }, sessionID string) {
	if resetter, ok := budget.(interface{ Reset(string) }); ok {
		resetter.Reset(sessionID)
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
	// 同轮阻塞提问：只有显式开启时才把 asker 注入 turn ctx。没有 asker 的
	// turn 里 question 工具回退为哨兵/暂停（旧行为），所以关闭时行为不变。
	if s.config != nil && s.config.QuestionBlocking {
		turnCtx = question.WithAsker(turnCtx, &sessionQuestionAsker{sess: s})
	}
	s.turnMu.Lock()
	s.cancelCurrentTurn = cancelTurn
	s.turnMu.Unlock()
	defer func() {
		s.turnMu.Lock()
		s.cancelCurrentTurn = nil
		s.turnMu.Unlock()
		cancelTurn()
	}()

	// Budget 是「一次运行」的预算，不是会话生命周期累计：cortex 的
	// agentutils.Budget 按 session 累加 tokens/tool calls/time，若不在每轮
	// 开始时清零，长会话迟早会永久停在 “Budget exceeded”。带 Reset 的实现
	// 在这里清零；只实现 CanExecute 的旧 fake 保持原行为。
	resetTurnBudget(s.budget, s.id)
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
		case "tool_input_delta":
			if result.ToolEvent != nil {
				s.emit(&Event{
					Type:       EventTypeToolInputDelta,
					ToolName:   result.ToolEvent.ToolName,
					ToolCallID: result.ToolEvent.ToolCallID,
					Content:    deltaString(result.ToolEvent.Input["delta"]),
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
					// 同轮阻塞模式的结果没有 ask_user 标记，且问题事件已由
					// askQuestion 发出，这里不会重复 emit。
					if result.ToolEvent.ToolName == "question" {
						if q := runtime.SentinelFromOutput(result.ToolEvent.Output); q != nil {
							s.emit(&Event{
								Type:       EventTypeQuestion,
								SessionID:  s.id,
								ToolName:   result.ToolEvent.ToolName,
								ToolCallID: result.ToolEvent.ToolCallID,
								Question:   q.Question,
								Questions:  q.Questions,
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
	if q := runtime.SentinelFromOutput(output); q != nil {
		return q.Question
	}
	return ""
}

// deltaString 从 map 里取字符串（tool_input_delta 的 delta 字段）。
func deltaString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
