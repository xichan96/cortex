package model

import (
	"context"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

type fakeStream struct {
	events []StreamEvent
	result *AssistantMessage
}

func (s *fakeStream) Events() <-chan StreamEvent {
	ch := make(chan StreamEvent, len(s.events))
	for _, ev := range s.events {
		ch <- ev
	}
	close(ch)
	return ch
}
func (s *fakeStream) Result() (*AssistantMessage, error) { return s.result, nil }
func (s *fakeStream) Close() error                       { return nil }

type fakeProvider struct {
	id     string
	models []Model
	stream *fakeStream
	req    *Request
}

func (p *fakeProvider) ID() string      { return p.id }
func (p *fakeProvider) Models() []Model { return p.models }
func (p *fakeProvider) Stream(_ context.Context, req *Request) (Stream, error) {
	p.req = req
	return p.stream, nil
}

func testModel() Model {
	return Model{
		ID:            "test-model",
		Name:          "Test Model",
		Provider:      "fake",
		API:           API("fake-api"),
		ContextWindow: 128000,
		MaxTokens:     4096,
		Reasoning:     true,
		Input:         []Modality{ModalityText, ModalityImage},
	}
}

func TestEngineProviderChatWithTools(t *testing.T) {
	m := testModel()
	provider := &fakeProvider{
		id:     "fake",
		models: []Model{m},
		stream: &fakeStream{
			events: []StreamEvent{
				{Type: StreamTextDelta, Delta: "Hel"},
				{Type: StreamTextDelta, Delta: "lo"},
				{Type: StreamThinkingDelta, Delta: "think"},
				{Type: StreamToolDelta, ToolCall: &ToolCallDelta{Index: 0, ID: "call-1", Name: "Bash", ArgumentsDelta: `{"cmd":`}},
				{Type: StreamToolEnd, ToolCall: &ToolCallDelta{Index: 0, ID: "call-1", Name: "Bash", Arguments: map[string]any{"cmd": "ls"}}},
				{Type: StreamDone, Message: &AssistantMessage{
					Content:    []ContentBlock{{Type: ContentText, Text: "Hello"}},
					Usage:      Usage{Input: 10, Output: 2, TotalTokens: 12},
					StopReason: StopReasonStop,
				}},
			},
		},
	}
	ep, err := NewEngineProvider(provider, &m, EngineOptions{MaxTokens: 1024})
	if err != nil {
		t.Fatalf("NewEngineProvider: %v", err)
	}

	got, err := ep.ChatWithTools(context.Background(), []types.Message{
		{Role: "user", Content: "hi"},
	}, []types.Tool{&adapterFakeTool{}})
	if err != nil {
		t.Fatalf("ChatWithTools: %v", err)
	}
	if got.Content != "Hello" {
		t.Errorf("content = %q", got.Content)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Function.Name != "Bash" {
		t.Fatalf("tool calls = %+v", got.ToolCalls)
	}
	if got.ToolCalls[0].Function.Arguments["cmd"] != "ls" {
		t.Errorf("tool args = %+v", got.ToolCalls[0].Function.Arguments)
	}
	if got.Usage.PromptTokens != 10 || got.Usage.CompletionTokens != 2 {
		t.Errorf("usage = %+v", got.Usage)
	}
	if provider.req.MaxTokens != 1024 {
		t.Errorf("request max tokens = %d", provider.req.MaxTokens)
	}
	if len(provider.req.Tools) != 1 || provider.req.Tools[0].Name != "echo" {
		t.Errorf("request tools = %+v", provider.req.Tools)
	}
}

func TestEngineProviderStreamMapping(t *testing.T) {
	m := testModel()
	provider := &fakeProvider{
		id:     "fake",
		models: []Model{m},
		stream: &fakeStream{events: []StreamEvent{
			{Type: StreamTextDelta, Delta: "a"},
			{Type: StreamThinkingDelta, Delta: "t"},
			{Type: StreamToolDelta, ToolCall: &ToolCallDelta{ID: "c", Name: "echo", ArgumentsDelta: "{}"}},
			{Type: StreamToolEnd, ToolCall: &ToolCallDelta{ID: "c", Name: "echo", Arguments: map[string]any{}}},
			{Type: StreamDone, Usage: &Usage{Input: 1, Output: 1, TotalTokens: 2}},
		}},
	}
	ep, _ := NewEngineProvider(provider, &m, EngineOptions{})
	ch, err := ep.ChatWithToolsStream(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("ChatWithToolsStream: %v", err)
	}
	var typesSeen []string
	for ev := range ch {
		typesSeen = append(typesSeen, ev.Type)
	}
	want := []string{"chunk", "reasoning", "tool_input_delta", "tool_calls", "end"}
	if len(typesSeen) != len(want) {
		t.Fatalf("events = %v, want %v", typesSeen, want)
	}
	for i := range want {
		if typesSeen[i] != want[i] {
			t.Fatalf("events = %v, want %v", typesSeen, want)
		}
	}
}

func TestEngineProviderMetadata(t *testing.T) {
	m := testModel()
	ep, _ := NewEngineProvider(&fakeProvider{id: "fake", models: []Model{m}}, &m, EngineOptions{})
	if ep.GetModelName() != "test-model" {
		t.Errorf("name = %q", ep.GetModelName())
	}
	md := ep.GetModelMetadata()
	if md.Version != "test-model" || md.MaxTokens != 4096 {
		t.Errorf("metadata = %+v", md)
	}
	if md.Extra["context_window"] != 128000 {
		t.Errorf("extra = %+v", md.Extra)
	}
}

func TestEngineProviderPromptCacheRetention(t *testing.T) {
	m := testModel()
	provider := &fakeProvider{
		id:     "fake",
		models: []Model{m},
		stream: &fakeStream{events: []StreamEvent{{Type: StreamDone}}},
	}
	ep, err := NewEngineProvider(provider, &m, EngineOptions{})
	if err != nil {
		t.Fatalf("NewEngineProvider: %v", err)
	}

	drain := func() {
		t.Helper()
		ch, err := ep.ChatWithToolsStream(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
		if err != nil {
			t.Fatalf("ChatWithToolsStream: %v", err)
		}
		for range ch {
		}
	}

	// Without SetPromptCacheOptions the explicit EngineOptions value wins.
	drain()
	if provider.req.CacheRetention != CacheRetentionNone {
		t.Fatalf("default retention = %q, want none", provider.req.CacheRetention)
	}

	ep.SetPromptCacheOptions(types.PromptCacheOptions{Enabled: true})
	drain()
	if provider.req.CacheRetention != CacheRetentionShort {
		t.Fatalf("enabled retention = %q, want short", provider.req.CacheRetention)
	}
	if !ep.PromptCacheOptions().Enabled {
		t.Fatal("PromptCacheOptions should report enabled")
	}

	ep.SetPromptCacheOptions(types.PromptCacheOptions{Enabled: false})
	drain()
	if provider.req.CacheRetention != CacheRetentionNone {
		t.Fatalf("disabled retention = %q, want none", provider.req.CacheRetention)
	}
}

func TestEngineProviderErrorEvent(t *testing.T) {
	m := testModel()
	provider := &fakeProvider{
		id:     "fake",
		models: []Model{m},
		stream: &fakeStream{events: []StreamEvent{
			{Type: StreamError, Err: &StreamErrorInfo{Message: "boom"}},
		}},
	}
	ep, _ := NewEngineProvider(provider, &m, EngineOptions{})
	if _, err := ep.Chat(context.Background(), []types.Message{{Role: "user", Content: "x"}}); err == nil {
		t.Fatal("Chat must return the stream error")
	}
}

type adapterFakeTool struct{}

func (t *adapterFakeTool) Name() string        { return "echo" }
func (t *adapterFakeTool) Description() string { return "echo input" }
func (t *adapterFakeTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *adapterFakeTool) Execute(context.Context, map[string]interface{}) (interface{}, error) {
	return "", nil
}
func (t *adapterFakeTool) Metadata() types.ToolMetadata { return types.ToolMetadata{} }
