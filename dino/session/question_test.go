package session

import (
	"context"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/tools/builtin/runtime"
	"github.com/xichan96/cortex/agent/types"
	agentutils "github.com/xichan96/cortex/agent/utils"
)

// answerFactory 满足 SessionFactory 最小接口（复用 wake_test.go 的 fakeFactory 语义）。
type answerFactory struct{}

func (answerFactory) RecordLoop(sessionID string, action agentutils.LoopDetectAction) {}
func (answerFactory) RecordTokens(ctx context.Context, sessionID string, tokens int) {}
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
func (p *answerProvider) GetModelName() string          { return "answer-mock" }
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
