package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/model"
)

// sseData renders one server-sent data frame.
func sseData(data string) string {
	return "data: " + data + "\n\n"
}

func testProvider(t *testing.T, handler http.HandlerFunc) (*Provider, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		// Keep existing tests single-attempt and fast; retry behavior has its
		// own tests below.
		Retry: &model.TransportRetryPolicy{MaxRetries: 0},
	}), server
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

func TestProviderCatalog(t *testing.T) {
	provider := New(Config{})
	if provider.ID() != "openai" {
		t.Fatalf("ID() = %q", provider.ID())
	}
	models := provider.Models()
	if len(models) != 6 {
		t.Fatalf("Models() returned %d entries, want 6", len(models))
	}
	ids := map[string]bool{}
	for _, m := range models {
		ids[m.ID] = true
		if m.Provider != "openai" || m.API != model.APIOpenAICompletions {
			t.Fatalf("model %s missing provider/api: %+v", m.ID, m)
		}
		if m.ContextWindow <= 0 || m.MaxTokens <= 0 {
			t.Fatalf("model %s has incomplete metadata: %+v", m.ID, m)
		}
		if m.MaxTokens > m.ContextWindow {
			t.Fatalf("model %s maxTokens exceeds context: %+v", m.ID, m)
		}
		if len(m.Input) == 0 {
			t.Fatalf("model %s declares no input modalities", m.ID)
		}
		if m.BaseURL != DefaultBaseURL {
			t.Fatalf("model %s baseURL = %q", m.ID, m.BaseURL)
		}
	}
	for _, want := range []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4.1-mini", "o3", "o4-mini"} {
		if !ids[want] {
			t.Fatalf("catalog missing %s", want)
		}
	}
	if !catalogModel(t, "o3").Reasoning {
		t.Fatal("o3 should be marked as a reasoning model")
	}
	if _, err := model.NewStaticCatalog(models); err != nil {
		t.Fatalf("catalog does not validate: %v", err)
	}

	// Models() returns copies: mutating one must not leak into the provider.
	models[0].ID = "mutated"
	if provider.Models()[0].ID == "mutated" {
		t.Fatal("Models() exposed shared catalog state")
	}
}

func TestStreamTextReasoningToolCallsAndUsage(t *testing.T) {
	var gotBody map[string]any
	var gotHeader http.Header

	sse := "" +
		sseData(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`) +
		sseData(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"reasoning_content":"Let me "},"finish_reason":null}]}`) +
		sseData(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"reasoning_content":"think."},"finish_reason":null}]}`) +
		sseData(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`) +
		sseData(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}`) +
		sseData(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"Read","arguments":"{\"path\":"}}]},"finish_reason":null}]}`) +
		sseData(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"Write","arguments":"{\"path\":\"b.txt\"}"}}]},"finish_reason":null}]}`) +
		sseData(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.txt\"}"}}]},"finish_reason":null}]}`) +
		sseData(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`) +
		sseData(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":20},"completion_tokens_details":{"reasoning_tokens":10}}}`) +
		sseData(`[DONE]`)

	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	})

	req := &model.Request{
		Model: catalogModel(t, "gpt-4o"),
		System: []model.ContentBlock{
			{Type: model.ContentText, Text: "You are helpful."},
		},
		Messages: []model.Message{
			{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "read a.txt"}}},
		},
		Tools: []model.ToolDeclaration{{
			Name:        "Read",
			Description: "Read a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
		}},
		MaxTokens: 1024,
	}

	stream, err := provider.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collectEvents(t, stream)
	if err := model.ValidateSequence(events); err != nil {
		t.Fatalf("invalid event sequence: %v", err)
	}

	wantTypes := []model.StreamEventType{
		model.StreamStart,
		model.StreamThinkingStart, model.StreamThinkingDelta, model.StreamThinkingDelta, model.StreamThinkingEnd,
		model.StreamTextStart, model.StreamTextDelta, model.StreamTextDelta, model.StreamTextEnd,
		model.StreamToolStart, model.StreamToolDelta,
		model.StreamToolStart, model.StreamToolDelta,
		model.StreamToolDelta,
		model.StreamToolEnd, model.StreamToolEnd,
		model.StreamDone,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(wantTypes), events)
	}
	for i, want := range wantTypes {
		if events[i].Type != want {
			t.Fatalf("event %d = %s, want %s", i, events[i].Type, want)
		}
	}

	message, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if message == nil {
		t.Fatal("Result returned nil message")
	}
	if message.Text() != "Hello world" {
		t.Fatalf("message text = %q", message.Text())
	}
	if len(message.Content) != 4 || message.Content[0].Type != model.ContentThinking ||
		message.Content[0].Thinking != "Let me think." {
		t.Fatalf("thinking block = %+v", message.Content)
	}
	calls := message.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("tool calls = %+v", calls)
	}
	if calls[0].ID != "call_a" || calls[0].Name != "Read" || calls[0].Arguments["path"] != "a.txt" ||
		calls[0].ArgumentsError != nil {
		t.Fatalf("first tool call = %+v", calls[0])
	}
	if calls[1].ID != "call_b" || calls[1].Name != "Write" || calls[1].Arguments["path"] != "b.txt" {
		t.Fatalf("second tool call = %+v", calls[1])
	}
	if message.StopReason != model.StopReasonToolUse {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if message.Provider != "openai" || message.API != model.APIOpenAICompletions ||
		message.Model != "gpt-4o" || message.ResponseID != "chatcmpl-1" || message.ResponseModel != "gpt-4o" {
		t.Fatalf("message metadata = %+v", message)
	}
	usage := message.Usage
	if usage.Input != 100 || usage.Output != 50 || usage.CacheRead != 20 ||
		usage.Reasoning != 10 || usage.TotalTokens != 150 {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.Cost.Total <= 0 {
		t.Fatalf("catalog cost estimate missing: %+v", usage.Cost)
	}

	// Request headers.
	if got := gotHeader.Get("Authorization"); got != "Bearer test-key" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := gotHeader.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := gotHeader.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q", got)
	}

	// Request body.
	if got := gotBody["model"]; got != "gpt-4o" {
		t.Fatalf("model = %v", got)
	}
	if got := gotBody["stream"]; got != true {
		t.Fatalf("stream = %v", got)
	}
	if got := gotBody["max_tokens"]; got != float64(1024) {
		t.Fatalf("max_tokens = %v", got)
	}
	options := gotBody["stream_options"].(map[string]any)
	if options["include_usage"] != true {
		t.Fatalf("stream_options = %+v", options)
	}
	messages := gotBody["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %+v", messages)
	}
	system := messages[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "You are helpful." {
		t.Fatalf("system message = %+v", system)
	}
	tools := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", gotBody["tools"])
	}
	tool := tools[0].(map[string]any)
	function := tool["function"].(map[string]any)
	if tool["type"] != "function" || function["name"] != "Read" || function["description"] != "Read a file" {
		t.Fatalf("tool = %+v", tool)
	}
	schema := function["parameters"].(map[string]any)
	if schema["type"] != "object" || schema["required"].([]any)[0] != "path" {
		t.Fatalf("parameters = %+v", schema)
	}
}

func TestRequestConversionImagesToolResultsAndThinking(t *testing.T) {
	var gotBody map[string]any
	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseData(`{"id":"c","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)+sseData(`[DONE]`))
	})

	temperature := 0.7
	req := &model.Request{
		Model: catalogModel(t, "gpt-4o"),
		System: []model.ContentBlock{
			{Type: model.ContentText, Text: "system prompt"},
		},
		Messages: []model.Message{
			{Role: model.RoleUser, Content: []model.ContentBlock{
				{Type: model.ContentText, Text: "look"},
				{Type: model.ContentImage, Image: &model.ImageContent{Data: "aGVsbG8=", MIMEType: "image/jpeg"}},
			}},
			{Role: model.RoleAssistant, Content: []model.ContentBlock{
				{Type: model.ContentThinking, Thinking: "secret reasoning"},
				{Type: model.ContentText, Text: "calling"},
				{Type: model.ContentToolCall, ToolCall: &model.ToolCall{
					ID: "call_9", Name: "Read", Arguments: map[string]any{"path": "a.txt"},
				}},
			}},
			{Role: model.RoleTool, Content: []model.ContentBlock{
				{Type: model.ContentToolResult, ToolResult: &model.ToolResult{ToolCallID: "call_9", Text: "file body"}},
			}},
			{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "thanks"}}},
		},
		Tools: []model.ToolDeclaration{{
			Name:       "Read",
			Parameters: json.RawMessage(`{"properties":{"path":{"type":"string"}}}`),
		}},
		Temperature: &temperature,
	}

	stream, err := provider.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collectEvents(t, stream)

	messages := gotBody["messages"].([]any)
	if len(messages) != 5 {
		t.Fatalf("messages = %+v", messages)
	}

	system := messages[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "system prompt" {
		t.Fatalf("system = %+v", system)
	}

	user := messages[1].(map[string]any)
	parts := user["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("user content = %+v", parts)
	}
	if parts[0].(map[string]any)["type"] != "text" || parts[0].(map[string]any)["text"] != "look" {
		t.Fatalf("text part = %+v", parts[0])
	}
	image := parts[1].(map[string]any)
	imageURL := image["image_url"].(map[string]any)
	if image["type"] != "image_url" || imageURL["url"] != "data:image/jpeg;base64,aGVsbG8=" {
		t.Fatalf("image part = %+v", image)
	}

	assistant := messages[2].(map[string]any)
	if assistant["content"] != "calling" {
		t.Fatalf("assistant content = %+v", assistant["content"])
	}
	toolCalls := assistant["tool_calls"].([]any)
	call := toolCalls[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if call["id"] != "call_9" || call["type"] != "function" || fn["name"] != "Read" {
		t.Fatalf("tool call = %+v", call)
	}
	if fn["arguments"] != `{"path":"a.txt"}` {
		t.Fatalf("tool arguments = %v", fn["arguments"])
	}

	toolMessage := messages[3].(map[string]any)
	if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "call_9" || toolMessage["content"] != "file body" {
		t.Fatalf("tool message = %+v", toolMessage)
	}

	thanks := messages[4].(map[string]any)
	if thanks["role"] != "user" || thanks["content"] != "thanks" {
		t.Fatalf("final user message = %+v", thanks)
	}

	if gotBody["temperature"] != 0.7 {
		t.Fatalf("temperature = %v", gotBody["temperature"])
	}
	tools := gotBody["tools"].([]any)
	schema := tools[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	if schema["type"] != "object" || schema["properties"] == nil {
		t.Fatalf("normalized schema = %+v", schema)
	}
}

func TestReasoningModelUsesMaxCompletionTokensAndEffort(t *testing.T) {
	var gotBody map[string]any
	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseData(`{"id":"c","model":"o3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)+sseData(`[DONE]`))
	})

	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:     catalogModel(t, "o3"),
		Thinking:  model.ThinkingXHigh,
		MaxTokens: 4096,
		Messages:  []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collectEvents(t, stream)

	if _, ok := gotBody["max_tokens"]; ok {
		t.Fatalf("o-series must not send max_tokens: %+v", gotBody)
	}
	if got := gotBody["max_completion_tokens"]; got != float64(4096) {
		t.Fatalf("max_completion_tokens = %v", got)
	}
	if got := gotBody["reasoning_effort"]; got != "high" {
		t.Fatalf("reasoning_effort = %v", got)
	}
}

func TestStreamHTTPErrorBecomesErrorEvent(t *testing.T) {
	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
	})

	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    catalogModel(t, "gpt-4o"),
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream returned a synchronous error: %v", err)
	}
	events := collectEvents(t, stream)
	if err := model.ValidateSequence(events); err != nil {
		t.Fatalf("invalid event sequence: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Type != model.StreamStart || events[1].Type != model.StreamError {
		t.Fatalf("event types = %s, %s", events[0].Type, events[1].Type)
	}
	if events[1].Err == nil || events[1].Err.Code != "rate_limit_exceeded" ||
		events[1].Err.StatusCode != http.StatusTooManyRequests || !events[1].Err.Retryable {
		t.Fatalf("error info = %+v", events[1].Err)
	}

	message, err := stream.Result()
	if err != nil {
		t.Fatalf("Result returned a transport error: %v", err)
	}
	if message == nil || message.StopReason != model.StopReasonError {
		t.Fatalf("result = %+v", message)
	}
	if !strings.Contains(message.ErrorMessage, "slow down") {
		t.Fatalf("error message = %q", message.ErrorMessage)
	}
}

func TestStreamMidStreamErrorPayload(t *testing.T) {
	sse := "" +
		sseData(`{"id":"c","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`) +
		sseData(`{"error":{"message":"upstream exploded","type":"server_error"}}`)

	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    catalogModel(t, "gpt-4o"),
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "go"}}}},
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
	if last.Err.Code != "server_error" || !last.Err.Retryable {
		t.Fatalf("error info = %+v", last.Err)
	}
	message, _ := stream.Result()
	if message.StopReason != model.StopReasonError || !strings.Contains(message.ErrorMessage, "upstream exploded") {
		t.Fatalf("result = %+v", message)
	}
	if message.Text() != "partial" {
		t.Fatalf("partial content lost: %+v", message.Content)
	}
}

func TestStreamFinishReasonWithoutDone(t *testing.T) {
	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseData(`{"id":"c","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    catalogModel(t, "gpt-4o"),
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "go"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collectEvents(t, stream)
	if err := model.ValidateSequence(events); err != nil {
		t.Fatalf("invalid event sequence: %v", err)
	}
	message, _ := stream.Result()
	if message.StopReason != model.StopReasonStop || message.Text() != "ok" {
		t.Fatalf("result = %+v", message)
	}
}

func TestStreamContentFilterBecomesErrorEvent(t *testing.T) {
	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseData(`{"id":"c","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`)+sseData(`[DONE]`))
	})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    catalogModel(t, "gpt-4o"),
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "go"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collectEvents(t, stream)
	last := events[len(events)-1]
	if last.Type != model.StreamError {
		t.Fatalf("last event = %+v", last)
	}
	message, _ := stream.Result()
	if message.StopReason != model.StopReasonError {
		t.Fatalf("result = %+v", message)
	}
}

func TestStreamInvalidToolArguments(t *testing.T) {
	sse := "" +
		sseData(`{"id":"c","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function","function":{"name":"broken","arguments":"{not json"}}]},"finish_reason":null}]}`) +
		sseData(`{"id":"c","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`) +
		sseData(`[DONE]`)

	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    catalogModel(t, "gpt-4o"),
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "go"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collectEvents(t, stream)
	if err := model.ValidateSequence(events); err != nil {
		t.Fatalf("invalid event sequence: %v", err)
	}
	message, _ := stream.Result()
	calls := message.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls = %+v", calls)
	}
	if calls[0].ArgumentsError == nil || calls[0].ArgumentsError.Raw != "{not json" {
		t.Fatalf("expected argument error, got %+v", calls[0])
	}
	if len(calls[0].Arguments) != 0 {
		t.Fatalf("malformed arguments kept values: %+v", calls[0].Arguments)
	}
}

func TestConfigAndRequestHeaders(t *testing.T) {
	var gotHeader http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseData(`{"id":"c","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)+sseData(`[DONE]`))
	}))
	t.Cleanup(server.Close)

	provider := New(Config{
		APIKey:  "config-key",
		BaseURL: server.URL + "/",
		Headers: map[string]string{"X-Custom": "config"},
	})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    catalogModel(t, "gpt-4o"),
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "hi"}}}},
		Headers:  map[string]string{"X-Custom": "request"},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collectEvents(t, stream)

	if got := gotHeader.Get("X-Custom"); got != "request" {
		t.Fatalf("X-Custom = %q, want request-level override", got)
	}
	if got := gotHeader.Get("Authorization"); got != "Bearer config-key" {
		t.Fatalf("Authorization = %q", got)
	}
	if provider.Models()[0].BaseURL != strings.TrimSuffix(server.URL, "/") {
		t.Fatalf("Models() baseURL = %q", provider.Models()[0].BaseURL)
	}
}

func TestStreamCancellation(t *testing.T) {
	release := make(chan struct{})
	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-release // block until the test closes
	})
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := provider.Stream(ctx, &model.Request{
		Model:    catalogModel(t, "gpt-4o"),
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	go func() {
		for range stream.Events() {
		}
	}()
	cancel()

	message, err := stream.Result()
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if message == nil || message.StopReason != model.StopReasonAborted {
		t.Fatalf("aborted result = %+v", message)
	}
	_ = stream.Close()
}

func TestPreferMaxCompletionTokens(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseData(`{"id":"c","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)+sseData(`[DONE]`))
	}))
	t.Cleanup(server.Close)

	provider := New(Config{
		APIKey:                    "k",
		BaseURL:                   server.URL,
		PreferMaxCompletionTokens: true,
	})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:     catalogModel(t, "gpt-4o"),
		MaxTokens: 1234,
		Messages:  []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collectEvents(t, stream)

	if _, ok := gotBody["max_tokens"]; ok {
		t.Fatalf("must not send max_tokens: %+v", gotBody)
	}
	if got := gotBody["max_completion_tokens"]; got != float64(1234) {
		t.Fatalf("max_completion_tokens = %v", got)
	}
}

func TestDefaultRetryPolicy(t *testing.T) {
	if got := New(Config{}).retry.MaxRetries; got != model.DefaultProviderRetryMaxRetries {
		t.Fatalf("default retry = %d, want %d", got, model.DefaultProviderRetryMaxRetries)
	}
	if got := New(Config{Retry: &model.TransportRetryPolicy{MaxRetries: 0}}).retry.MaxRetries; got != 0 {
		t.Fatalf("explicit retry = %d, want 0", got)
	}
}

func TestStreamRetriesTransientErrorBeforeStreaming(t *testing.T) {
	var attempts int32
	sse := sseData(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`) +
		sseData(`[DONE]`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"message":"upstream hiccup","type":"server_error","code":"server_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	t.Cleanup(server.Close)

	provider := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Retry: &model.TransportRetryPolicy{
			MaxRetries: 1,
			BaseDelay:  time.Millisecond,
			MaxDelay:   time.Millisecond,
		},
	})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    catalogModel(t, "gpt-4o"),
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collectEvents(t, stream)
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	if err := model.ValidateSequence(events); err != nil {
		t.Fatalf("invalid event sequence: %v", err)
	}
	if last := events[len(events)-1]; last.Type != model.StreamDone {
		t.Fatalf("last event = %+v, want done", last)
	}
}

func TestStreamDoesNotRetryClientError(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad request","type":"invalid_request_error","code":"invalid_request_error"}}`)
	}))
	t.Cleanup(server.Close)

	provider := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Retry: &model.TransportRetryPolicy{
			MaxRetries: 3,
			BaseDelay:  time.Millisecond,
		},
	})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    catalogModel(t, "gpt-4o"),
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collectEvents(t, stream)
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	if len(events) != 2 || events[1].Type != model.StreamError || events[1].Err.StatusCode != http.StatusBadRequest {
		t.Fatalf("events = %+v", events)
	}
}

func TestStreamRetryDisabled(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"down","type":"server_error","code":"server_error"}}`)
	}))
	t.Cleanup(server.Close)

	provider := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Retry:   &model.TransportRetryPolicy{MaxRetries: 0},
	})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    catalogModel(t, "gpt-4o"),
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collectEvents(t, stream)
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	if len(events) != 2 || events[1].Type != model.StreamError {
		t.Fatalf("events = %+v", events)
	}
}
