package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/question"
	"github.com/xichan96/cortex/agent/tools/builtin/runtime"
	"github.com/xichan96/cortex/agent/types"
	agentutils "github.com/xichan96/cortex/agent/utils"
)

// answerFactory 满足 SessionFactory 最小接口（复用 wake_test.go 的 fakeFactory 语义）。
type answerFactory struct{}

func (answerFactory) RecordLoop(sessionID string, action agentutils.LoopDetectAction) {}
func (answerFactory) RecordTokens(ctx context.Context, sessionID string, tokens int)  {}
func (answerFactory) Detect(ctx context.Context, sessionID string, action agentutils.LoopDetectAction) *agentutils.LoopDetectResult {
	return nil
}

// answerProvider 流式 provider：echo 最后一条 user 消息，供验证回答被注入。
type answerProvider struct {
	mu  chan struct{}
	got string
}

func (p *answerProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	return types.Message{Content: "done", Usage: types.Usage{TotalTokens: 10}}, nil
}
func (p *answerProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	return p.ChatWithToolsStream(ctx, messages, nil)
}
func (p *answerProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return p.Chat(ctx, messages)
}
func (p *answerProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	var last string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && messages[i].Content != "" {
			last = messages[i].Content
			break
		}
	}
	select {
	case p.mu <- struct{}{}:
		p.got = last
		<-p.mu
	default:
	}
	ch := make(chan types.StreamMessage, 2)
	u := types.Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10}
	ch <- types.StreamMessage{Type: "chunk", Content: "answered", Usage: &u}
	ch <- types.StreamMessage{Type: "end", Usage: &u}
	close(ch)
	return ch, nil
}
func (p *answerProvider) GetModelName() string { return "answer-mock" }
func (p *answerProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "answer-mock"}
}

// TestSession_AnswerQuestion 验证 AnswerQuestion 注入回答 → session 消费 → 新 turn
// 以回答为输入执行（question-reflow §2.2）。
func TestSession_AnswerQuestion(t *testing.T) {
	prov := &answerProvider{mu: make(chan struct{}, 1)}
	cfg := types.NewAgentConfig()
	cfg.MaxIterations = 2
	eng := engine.NewAgentEngine(prov, cfg)

	s := NewSession("q-answer", eng, answerFactory{}, context.Background(), DefaultConfig(), nil, nil, NoWakeSource())
	s.Start()
	defer s.Close()

	// 观察 answer turn 执行（questionAnswerDisplay 消息）。
	done := make(chan struct{}, 8)
	id := s.Subscribe(ObserverFunc(func(ev *Event) {
		if ev.IsDone() {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}))
	defer s.Unsubscribe(id)

	if err := s.AnswerQuestion("call_1", "yes please"); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("answer turn did not complete")
	}

	// 回答应以含 toolCallID 的前缀注入为最后 user 消息。
	prov.mu <- struct{}{}
	got := prov.got
	<-prov.mu
	if got == "" {
		t.Fatal("provider did not observe the answer input")
	}
	if got != "[question answer for call_1] yes please" {
		t.Errorf("answer input = %q", got)
	}
}

// TestAnswerQuestion_EmptyToolCallID 验证空 toolCallID 被拒绝。
func TestAnswerQuestion_EmptyToolCallID(t *testing.T) {
	s := NewSession("q-empty", newTestEngine(&streamProvider{}), fakeFactory{}, context.Background(), DefaultConfig(), nil, nil, NoWakeSource())
	s.Start()
	defer s.Close()
	if err := s.AnswerQuestion("", "answer"); err == nil {
		t.Error("empty toolCallID should be rejected")
	}
}

// TestQuestionFromOutput_Struct 验证 struct 形态的 SentinelQuestionResult 被识别。
// 必须用命名类型 runtime.SentinelQuestionResult（评审 R4/B1）——匿名 struct 断言
// 对命名类型永不命中，用匿名 struct 直测会掩盖回归。
func TestQuestionFromOutput_Struct(t *testing.T) {
	out := runtime.SentinelQuestionResult{Ok: true, Question: "请确认是否继续？", AskUser: true}
	if q := questionFromOutput(out); q != "请确认是否继续？" {
		t.Errorf("expected question text, got %q", q)
	}
}

// TestQuestionFromOutput_Map 验证 map 形态（JSON 反序列化后）被识别。
func TestQuestionFromOutput_Map(t *testing.T) {
	out := map[string]interface{}{
		"ok":       true,
		"question": "Are you sure?",
		"ask_user": true,
	}
	if q := questionFromOutput(out); q != "Are you sure?" {
		t.Errorf("expected question text, got %q", q)
	}
}

// TestQuestionFromOutput_NotQuestion 验证非 question sentinel 返回空。
func TestQuestionFromOutput_NotQuestion(t *testing.T) {
	// ask_user=false 的 sentinel 不触发。
	out := map[string]interface{}{
		"ok":       true,
		"question": "not a real question",
		"ask_user": false,
	}
	if q := questionFromOutput(out); q != "" {
		t.Errorf("expected empty for ask_user=false, got %q", q)
	}
	// 普通工具输出（map 无 ask_user）不触发。
	if q := questionFromOutput(map[string]interface{}{"result": "ok"}); q != "" {
		t.Errorf("expected empty for non-question output, got %q", q)
	}
	// nil 不触发。
	if q := questionFromOutput(nil); q != "" {
		t.Errorf("expected empty for nil, got %q", q)
	}
}

// TestEventTypeQuestion_Constant 验证事件类型常量定义。
func TestEventTypeQuestion_Constant(t *testing.T) {
	if EventTypeQuestion != "question" {
		t.Errorf("EventTypeQuestion = %q, want %q", EventTypeQuestion, "question")
	}
}

// ---- 同轮阻塞（QuestionBlocking）测试 ----

func newQuestionOnlySession(id string, mutate func(*Config)) *Session {
	// Start from DefaultConfig: a zero-value Config has an unbuffered output
	// channel, so emit would stall when no output consumer is attached.
	cfg := DefaultConfig()
	if mutate != nil {
		mutate(cfg)
	}
	return NewSession(id, nil, fakeFactory{}, context.Background(), cfg, nil, nil, NoWakeSource())
}

func testQuestionSpecs() []question.Spec {
	return []question.Spec{{
		ID: "scope", Header: "范围", Question: "处理哪些服务？",
		Options: []question.Option{
			{Label: "全部（Recommended）"},
			{Label: "仅核心"},
		},
	}}
}

// waitQuestionEvent 从观察者通道取一个可回答问题事件。
func waitQuestionEvent(t *testing.T, ch <-chan *Event) *Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no question event")
		return nil
	}
}

func TestAskQuestion_BlockingHappyPath(t *testing.T) {
	s := newQuestionOnlySession("q-block", func(c *Config) { c.QuestionBlocking = true })
	events := make(chan *Event, 8)
	var count int
	var mu sync.Mutex
	s.Subscribe(ObserverFunc(func(ev *Event) {
		if ev.Type == EventTypeQuestion {
			mu.Lock()
			count++
			mu.Unlock()
			events <- ev
		}
	}))

	type result struct {
		answers question.Answers
		err     error
	}
	done := make(chan result, 1)
	go func() {
		ans, err := s.askQuestion(context.Background(), question.Request{
			ToolCallID: "call_1",
			Questions:  testQuestionSpecs(),
		})
		done <- result{ans, err}
	}()

	ev := waitQuestionEvent(t, events)
	if !ev.QuestionPending {
		t.Error("event must be marked pending")
	}
	if ev.QuestionID == "" {
		t.Error("event must carry a request id")
	}
	if ev.ToolCallID != "call_1" {
		t.Errorf("tool_call_id = %q", ev.ToolCallID)
	}
	if ev.Question != "处理哪些服务？" {
		t.Errorf("compat question = %q", ev.Question)
	}
	if len(ev.Questions) != 1 || ev.Questions[0].ID != "scope" || len(ev.Questions[0].Options) != 2 {
		t.Errorf("structured questions = %+v", ev.Questions)
	}

	// 用 tool_call_id 回答（兼容只持有 tool call id 的消费方）。
	if err := s.AnswerQuestionWithData("call_1", map[string][]string{"scope": {"仅核心"}}); err != nil {
		t.Fatalf("AnswerQuestionWithData: %v", err)
	}
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("ask err: %v", res.err)
		}
		if got := res.answers["scope"]; len(got) != 1 || got[0] != "仅核心" {
			t.Errorf("answers = %+v", res.answers)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ask did not return")
	}

	mu.Lock()
	got := count
	mu.Unlock()
	if got != 1 {
		t.Errorf("question event emitted %d times, want 1", got)
	}
	if ids := s.PendingQuestionIDs(); len(ids) != 0 {
		t.Errorf("pending registry not cleaned: %v", ids)
	}
	// 重复回答 -> unknown（投递前已摘除）。
	if err := s.AnswerQuestionWithData("call_1", map[string][]string{"scope": {"x"}}); !errors.Is(err, question.ErrUnknownQuestion) {
		t.Errorf("second answer error = %v, want ErrUnknownQuestion", err)
	}
}

func TestAskQuestion_Cancel(t *testing.T) {
	s := newQuestionOnlySession("q-cancel", func(c *Config) { c.QuestionBlocking = true })
	events := make(chan *Event, 4)
	s.Subscribe(ObserverFunc(func(ev *Event) {
		if ev.Type == EventTypeQuestion {
			events <- ev
		}
	}))

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := s.askQuestion(ctx, question.Request{ToolCallID: "c1", Questions: testQuestionSpecs()})
		errCh <- err
	}()

	_ = waitQuestionEvent(t, events)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, question.ErrQuestionCancelled) {
			t.Errorf("err = %v, want ErrQuestionCancelled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ask did not return after cancel")
	}
	if ids := s.PendingQuestionIDs(); len(ids) != 0 {
		t.Errorf("pending registry leaked: %v", ids)
	}
}

func TestAskQuestion_Timeout(t *testing.T) {
	s := newQuestionOnlySession("q-timeout", func(c *Config) {
		c.QuestionBlocking = true
		c.QuestionTimeout = 40 * time.Millisecond
	})
	errCh := make(chan error, 1)
	go func() {
		_, err := s.askQuestion(context.Background(), question.Request{ToolCallID: "c1", Questions: testQuestionSpecs()})
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, question.ErrQuestionTimeout) {
			t.Errorf("err = %v, want ErrQuestionTimeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ask did not time out")
	}
	if ids := s.PendingQuestionIDs(); len(ids) != 0 {
		t.Errorf("pending registry leaked: %v", ids)
	}
}

func TestAskQuestion_DuplicateExplicitID(t *testing.T) {
	s := newQuestionOnlySession("q-dup", func(c *Config) { c.QuestionBlocking = true })
	events := make(chan *Event, 4)
	s.Subscribe(ObserverFunc(func(ev *Event) {
		if ev.Type == EventTypeQuestion {
			events <- ev
		}
	}))

	errCh := make(chan error, 1)
	go func() {
		_, err := s.askQuestion(context.Background(), question.Request{ID: "dup-id", ToolCallID: "c1", Questions: testQuestionSpecs()})
		errCh <- err
	}()
	ev := waitQuestionEvent(t, events)

	_, err := s.askQuestion(context.Background(), question.Request{ID: "dup-id", ToolCallID: "c2", Questions: testQuestionSpecs()})
	if !errors.Is(err, question.ErrDuplicateQuestion) {
		t.Errorf("duplicate id err = %v", err)
	}

	if err := s.AnswerQuestionWithData(ev.QuestionID, map[string][]string{"scope": {"ok"}}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	<-errCh
}

func TestAskQuestion_ConcurrentIsolation(t *testing.T) {
	s := newQuestionOnlySession("q-conc", func(c *Config) { c.QuestionBlocking = true })
	events := make(chan *Event, 8)
	s.Subscribe(ObserverFunc(func(ev *Event) {
		if ev.Type == EventTypeQuestion {
			events <- ev
		}
	}))

	type result struct {
		toolCallID string
		answers    question.Answers
		err        error
	}
	results := make(chan result, 2)
	for _, callID := range []string{"call_a", "call_b"} {
		callID := callID
		go func() {
			ans, err := s.askQuestion(context.Background(), question.Request{ToolCallID: callID, Questions: testQuestionSpecs()})
			results <- result{callID, ans, err}
		}()
	}

	evByCall := map[string]*Event{}
	for i := 0; i < 2; i++ {
		ev := waitQuestionEvent(t, events)
		evByCall[ev.ToolCallID] = ev
	}
	if evByCall["call_a"].QuestionID == evByCall["call_b"].QuestionID {
		t.Fatal("request ids must be unique")
	}
	// 逆序回答，验证答案按请求 ID 路由而不是按回答顺序。
	if err := s.AnswerQuestionWithData(evByCall["call_b"].QuestionID, map[string][]string{"scope": {"b-answer"}}); err != nil {
		t.Fatalf("answer b: %v", err)
	}
	if err := s.AnswerQuestionWithData(evByCall["call_a"].QuestionID, map[string][]string{"scope": {"a-answer"}}); err != nil {
		t.Fatalf("answer a: %v", err)
	}

	for i := 0; i < 2; i++ {
		select {
		case res := <-results:
			if res.err != nil {
				t.Fatalf("%s err: %v", res.toolCallID, res.err)
			}
			got := ""
			if vals := res.answers["scope"]; len(vals) > 0 {
				got = vals[0]
			}
			if res.toolCallID == "call_a" && got != "a-answer" {
				t.Errorf("call_a got %q", got)
			}
			if res.toolCallID == "call_b" && got != "b-answer" {
				t.Errorf("call_b got %q", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent ask did not return")
		}
	}
}

func TestAnswerQuestionWithData_Unknown(t *testing.T) {
	s := newQuestionOnlySession("q-unknown", func(c *Config) { c.QuestionBlocking = true })
	if err := s.AnswerQuestionWithData("nope", map[string][]string{"a": {"b"}}); !errors.Is(err, question.ErrUnknownQuestion) {
		t.Errorf("err = %v, want ErrUnknownQuestion", err)
	}
	if err := s.AnswerQuestionWithData("", nil); err == nil {
		t.Error("empty id must error")
	}
}

// TestQuestionFromOutput_Structured 验证哨兵里的结构化 questions 被保留。
func TestQuestionFromOutput_Structured(t *testing.T) {
	out := runtime.SentinelQuestionResult{
		Ok:       true,
		AskUser:  true,
		Question: "first",
		Questions: []question.Spec{{
			ID: "scope", Header: "范围", Question: "first",
			Options: []question.Option{{Label: "A"}, {Label: "B"}},
		}},
	}
	if got := questionFromOutput(out); got != "first" {
		t.Errorf("questionFromOutput = %q", got)
	}
	sentinel := runtime.SentinelFromOutput(out)
	if sentinel == nil || len(sentinel.Questions) != 1 || sentinel.Questions[0].ID != "scope" {
		t.Errorf("structured sentinel = %+v", sentinel)
	}
}

// ---- 端到端：同轮阻塞 ----

// blockingQuestionProvider 第一轮要求调用 question，第二轮验证 tool result 里
// 已带答案并继续输出。
type blockingQuestionProvider struct {
	mu               sync.Mutex
	calls            int
	secondSawAnswers bool
}

func (p *blockingQuestionProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	return types.Message{Content: "done", Usage: types.Usage{TotalTokens: 10}}, nil
}
func (p *blockingQuestionProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	return p.ChatWithToolsStream(ctx, messages, nil)
}
func (p *blockingQuestionProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return p.Chat(ctx, messages)
}
func (p *blockingQuestionProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	if call > 1 {
		for _, m := range messages {
			if m.Role == "tool" && strings.Contains(m.Content, `"answered": true`) {
				p.secondSawAnswers = true
			}
		}
	}
	p.mu.Unlock()

	u := types.Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10}
	ch := make(chan types.StreamMessage, 4)
	if call == 1 {
		ch <- types.StreamMessage{Type: "tool_calls", ToolCalls: []types.ToolCall{{
			ID:   "call_q",
			Type: "function",
			Function: types.ToolFunction{
				Name: "question",
				Arguments: map[string]interface{}{
					"questions": []interface{}{map[string]interface{}{
						"id": "scope", "header": "范围", "question": "处理哪些服务？",
						"options": []interface{}{
							map[string]interface{}{"label": "全部"},
							map[string]interface{}{"label": "仅核心"},
						},
					}},
				},
			},
		}}, Usage: &u}
	} else {
		ch <- types.StreamMessage{Type: "chunk", Content: "done", Usage: &u}
	}
	ch <- types.StreamMessage{Type: "end", Usage: &u}
	close(ch)
	return ch, nil
}
func (p *blockingQuestionProvider) GetModelName() string { return "blocking-mock" }
func (p *blockingQuestionProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "blocking-mock"}
}

func TestSession_QuestionBlocking_SameTurn(t *testing.T) {
	prov := &blockingQuestionProvider{}
	cfg := types.NewAgentConfig()
	cfg.MaxIterations = 4
	eng := engine.NewAgentEngine(prov, cfg)
	eng.AddTool(context.Background(), runtime.NewQuestionTool())

	sessCfg := DefaultConfig()
	sessCfg.QuestionBlocking = true
	s := NewSession("q-e2e", eng, fakeFactory{}, context.Background(), sessCfg, nil, nil, NoWakeSource())
	s.Start()
	defer s.Close()

	done := make(chan struct{}, 4)
	var countMu sync.Mutex
	questionCount := 0
	s.Subscribe(ObserverFunc(func(ev *Event) {
		switch ev.Type {
		case EventTypeQuestion:
			countMu.Lock()
			questionCount++
			countMu.Unlock()
			if !ev.QuestionPending {
				t.Errorf("blocking question event must be pending")
				return
			}
			if err := s.AnswerQuestionWithData(ev.QuestionID, map[string][]string{
				"scope": {"仅核心"},
				"note":  {"user_note: 别动生产"},
			}); err != nil {
				t.Errorf("AnswerQuestionWithData: %v", err)
			}
		case EventTypeDone:
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}))

	s.Input() <- "please ask me"
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not complete")
	}

	prov.mu.Lock()
	calls := prov.calls
	saw := prov.secondSawAnswers
	prov.mu.Unlock()
	if calls < 2 {
		t.Errorf("provider calls = %d, want >= 2 (same turn must continue)", calls)
	}
	if !saw {
		t.Error("second LLM call did not receive the answered tool result")
	}
	countMu.Lock()
	gotQuestions := questionCount
	countMu.Unlock()
	if gotQuestions != 1 {
		t.Errorf("question event emitted %d times, want 1", gotQuestions)
	}
}

// TestSession_QuestionLegacy_HaltsTurn 验证未开启 QuestionBlocking 时的旧行为：
// question 工具返回哨兵 → session emit 结构化 EventTypeQuestion（非 pending）→
// 本轮结束（provider 只被调用一次），答案需通过 AnswerQuestion 另起一轮。
func TestSession_QuestionLegacy_HaltsTurn(t *testing.T) {
	prov := &blockingQuestionProvider{}
	cfg := types.NewAgentConfig()
	cfg.MaxIterations = 4
	eng := engine.NewAgentEngine(prov, cfg)
	eng.AddTool(context.Background(), runtime.NewQuestionTool())

	s := NewSession("q-legacy", eng, fakeFactory{}, context.Background(), DefaultConfig(), nil, nil, NoWakeSource())
	s.Start()
	defer s.Close()

	questions := make(chan *Event, 4)
	done := make(chan struct{}, 4)
	s.Subscribe(ObserverFunc(func(ev *Event) {
		switch ev.Type {
		case EventTypeQuestion:
			questions <- ev
		case EventTypeDone:
			select {
			case done <- struct{}{}:
			default:
			}
		}
	}))

	s.Input() <- "please ask me"
	select {
	case ev := <-questions:
		if ev.QuestionPending {
			t.Error("legacy question must not be pending")
		}
		if len(ev.Questions) != 1 || ev.Questions[0].ID != "scope" {
			t.Errorf("structured questions = %+v", ev.Questions)
		}
		if ev.Question != "处理哪些服务？" {
			t.Errorf("compat question = %q", ev.Question)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no legacy question event")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not complete after legacy sentinel")
	}

	prov.mu.Lock()
	calls := prov.calls
	prov.mu.Unlock()
	if calls != 1 {
		t.Errorf("provider calls = %d, want 1 (legacy mode halts the turn)", calls)
	}
}
