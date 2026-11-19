package deepseek

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xichan96/cortex/agent/model"
)

func sseData(data string) string {
	return "data: " + data + "\n\n"
}

func catalogModel(t *testing.T, id string) *model.Model {
	t.Helper()
	for _, m := range New(Config{}).Models() {
		if m.ID == id {
			copy := m
			return &copy
		}
	}
	t.Fatalf("model %s missing from catalog", id)
	return nil
}

func collectEvents(t *testing.T, stream model.Stream) []model.StreamEvent {
	t.Helper()
	var events []model.StreamEvent
	for event := range stream.Events() {
		events = append(events, event)
	}
	return events
}

func TestProviderCatalogAndDefaults(t *testing.T) {
	provider := New(Config{})
	if provider.ID() != "deepseek" {
		t.Fatalf("ID() = %q", provider.ID())
	}
	models := provider.Models()
	if len(models) != 2 {
		t.Fatalf("Models() returned %d entries, want 2", len(models))
	}
	ids := map[string]bool{}
	for _, m := range models {
		ids[m.ID] = true
		if m.Provider != "deepseek" || m.API != model.APIOpenAICompletions {
			t.Fatalf("model %s missing provider/api: %+v", m.ID, m)
		}
		if m.ContextWindow <= 0 || m.MaxTokens <= 0 {
			t.Fatalf("model %s has incomplete metadata: %+v", m.ID, m)
		}
		if m.BaseURL != DefaultBaseURL {
			t.Fatalf("model %s baseURL = %q", m.ID, m.BaseURL)
		}
	}
	for _, want := range []string{"deepseek-chat", "deepseek-reasoner"} {
		if !ids[want] {
			t.Fatalf("catalog missing %s", want)
		}
	}
	if !catalogModel(t, "deepseek-reasoner").Reasoning {
		t.Fatal("deepseek-reasoner should be marked as a reasoning model")
	}
	if _, err := model.NewStaticCatalog(models); err != nil {
		t.Fatalf("catalog does not validate: %v", err)
	}
}

func TestStreamReasoningMapsToThinkingAndIgnoresReasoningEffort(t *testing.T) {
	var gotBody map[string]any
	var gotHeader http.Header
	var gotPath string

	sse := "" +
		sseData(`{"id":"ds-1","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`) +
		sseData(`{"id":"ds-1","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"reasoning_content":"think "},"finish_reason":null}]}`) +
		sseData(`{"id":"ds-1","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"reasoning_content":"hard"},"finish_reason":null}]}`) +
		sseData(`{"id":"ds-1","model":"deepseek-reasoner","choices":[{"index":0,"delta":{"content":"answer"},"finish_reason":null}]}`) +
		sseData(`{"id":"ds-1","model":"deepseek-reasoner","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`) +
		sseData(`{"id":"ds-1","model":"deepseek-reasoner","choices":[],"usage":{"prompt_tokens":30,"completion_tokens":5,"total_tokens":35,"prompt_cache_hit_tokens":10,"prompt_cache_miss_tokens":20}}`) +
		sseData(`[DONE]`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	t.Cleanup(server.Close)

	provider := New(Config{APIKey: "ds-key", BaseURL: server.URL})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    catalogModel(t, "deepseek-reasoner"),
		Thinking: model.ThinkingHigh,
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "why"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collectEvents(t, stream)
	if err := model.ValidateSequence(events); err != nil {
		t.Fatalf("invalid event sequence: %v", err)
	}

	want := []model.StreamEventType{
		model.StreamStart,
		model.StreamThinkingStart, model.StreamThinkingDelta, model.StreamThinkingDelta, model.StreamThinkingEnd,
		model.StreamTextStart, model.StreamTextDelta, model.StreamTextEnd,
		model.StreamDone,
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events: %+v", len(events), events)
	}
	for i := range want {
		if events[i].Type != want[i] {
			t.Fatalf("event %d = %s, want %s", i, events[i].Type, want[i])
		}
	}

	message, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if message.Provider != "deepseek" || message.API != model.APIOpenAICompletions ||
		message.Model != "deepseek-reasoner" || message.ResponseID != "ds-1" {
		t.Fatalf("message metadata = %+v", message)
	}
	if message.Text() != "answer" {
		t.Fatalf("text = %q", message.Text())
	}
	if len(message.Content) != 2 || message.Content[0].Type != model.ContentThinking ||
		message.Content[0].Thinking != "think hard" {
		t.Fatalf("thinking block = %+v", message.Content)
	}
	usage := message.Usage
	if usage.Input != 30 || usage.Output != 5 || usage.CacheRead != 10 || usage.TotalTokens != 35 {
		t.Fatalf("usage = %+v", usage)
	}

	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if got := gotHeader.Get("Authorization"); got != "Bearer ds-key" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := gotBody["model"]; got != "deepseek-reasoner" {
		t.Fatalf("model = %v", got)
	}
	if _, ok := gotBody["reasoning_effort"]; ok {
		t.Fatalf("DeepSeek must not receive reasoning_effort: %+v", gotBody)
	}
}

func TestHTTPErrorBecomesErrorEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key","type":"authentication_error","code":"invalid_api_key"}}`)
	}))
	t.Cleanup(server.Close)

	provider := New(Config{APIKey: "bad", BaseURL: server.URL})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    catalogModel(t, "deepseek-chat"),
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collectEvents(t, stream)
	if err := model.ValidateSequence(events); err != nil {
		t.Fatalf("invalid event sequence: %v", err)
	}
	last := events[len(events)-1]
	if last.Type != model.StreamError || last.Err == nil {
		t.Fatalf("last event = %+v", last)
	}
	if last.Err.Code != "invalid_api_key" || last.Err.StatusCode != http.StatusUnauthorized || last.Err.Retryable {
		t.Fatalf("error info = %+v", last.Err)
	}
	message, _ := stream.Result()
	if message.StopReason != model.StopReasonError {
		t.Fatalf("result = %+v", message)
	}
}
