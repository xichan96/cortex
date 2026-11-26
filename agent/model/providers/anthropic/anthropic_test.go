package anthropic

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

// sseEvent renders one server-sent event frame.
func sseEvent(name, data string) string {
	return "event: " + name + "\ndata: " + data + "\n\n"
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

func sonnetModel(t *testing.T) *model.Model {
	t.Helper()
	for _, m := range New(Config{}).Models() {
		if m.ID == "claude-sonnet-4-5" {
			copy := m
			return &copy
		}
	}
	t.Fatal("sonnet model missing from catalog")
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
	if provider.ID() != "anthropic" {
		t.Fatalf("ID() = %q", provider.ID())
	}
	models := provider.Models()
	if len(models) != 3 {
		t.Fatalf("Models() returned %d entries, want 3", len(models))
	}
	ids := map[string]bool{}
	for _, m := range models {
		ids[m.ID] = true
		if m.Provider != "anthropic" || m.API != model.APIAnthropicMessages {
			t.Fatalf("model %s missing provider/api: %+v", m.ID, m)
		}
		if m.ContextWindow <= 0 || m.MaxTokens <= 0 || !m.Reasoning {
			t.Fatalf("model %s has incomplete metadata: %+v", m.ID, m)
		}
		if len(m.Input) == 0 {
			t.Fatalf("model %s declares no input modalities", m.ID)
		}
		if m.BaseURL != DefaultBaseURL {
			t.Fatalf("model %s baseURL = %q", m.ID, m.BaseURL)
		}
	}
	for _, want := range []string{"claude-sonnet-4-5", "claude-opus-4-1", "claude-haiku-4-5"} {
		if !ids[want] {
			t.Fatalf("catalog missing %s", want)
		}
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

func TestStreamTextToolUseAndUsage(t *testing.T) {
	var gotBody map[string]any
	var gotHeader http.Header

	sse := "" +
		sseEvent("message_start", `{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-5","usage":{"input_tokens":120,"cache_read_input_tokens":10,"cache_creation_input_tokens":5,"output_tokens":1}}}`) +
		sseEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`) +
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`) +
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sseEvent("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}}`) +
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`) +
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"a.txt\"}"}}`) +
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		sseEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42,"output_tokens_details":{"thinking_tokens":7}}}`) +
		sseEvent("message_stop", `{"type":"message_stop"}`)

	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	})

	req := &model.Request{
		Model: sonnetModel(t),
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
		model.StreamTextStart,
		model.StreamTextDelta,
		model.StreamTextDelta,
		model.StreamTextEnd,
		model.StreamToolStart,
		model.StreamToolDelta,
		model.StreamToolDelta,
		model.StreamToolEnd,
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

	if got := events[2].Delta + events[3].Delta; got != "Hello world" {
		t.Fatalf("text deltas = %q", got)
	}
	if got, want := events[7].ToolCall.ArgumentsDelta, `"a.txt"}`; got != want {
		t.Fatalf("argument delta = %q, want %q", got, want)
	}
	if got := events[8].ToolCall; got == nil || got.ID != "toolu_1" || got.Name != "Read" {
		t.Fatalf("tool end = %+v", got)
	}
	if got := events[8].ToolCall.Arguments["path"]; got != "a.txt" {
		t.Fatalf("tool arguments = %+v", events[8].ToolCall.Arguments)
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
	calls := message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "toolu_1" || calls[0].Name != "Read" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if calls[0].Arguments["path"] != "a.txt" || calls[0].ArgumentsError != nil {
		t.Fatalf("tool arguments = %+v (err %v)", calls[0].Arguments, calls[0].ArgumentsError)
	}
	if message.StopReason != model.StopReasonToolUse {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if message.Provider != "anthropic" || message.API != model.APIAnthropicMessages ||
		message.Model != "claude-sonnet-4-5" || message.ResponseID != "msg_1" {
		t.Fatalf("message metadata = %+v", message)
	}
	usage := message.Usage
	if usage.Input != 135 || usage.Output != 42 || usage.CacheRead != 10 || usage.CacheWrite != 5 ||
		usage.Reasoning != 7 || usage.TotalTokens != 177 {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.Cost.Total <= 0 {
		t.Fatalf("catalog cost estimate missing: %+v", usage.Cost)
	}

	// Request headers.
	if got := gotHeader.Get("x-api-key"); got != "test-key" {
		t.Fatalf("x-api-key = %q", got)
	}
	if got := gotHeader.Get("anthropic-version"); got != DefaultAPIVersion {
		t.Fatalf("anthropic-version = %q", got)
	}
	if got := gotHeader.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}

	// Request body.
	if got := gotBody["model"]; got != "claude-sonnet-4-5" {
		t.Fatalf("model = %v", got)
	}
	if got := gotBody["max_tokens"]; got != float64(1024) {
		t.Fatalf("max_tokens = %v", got)
	}
	if got := gotBody["stream"]; got != true {
		t.Fatalf("stream = %v", got)
	}
	system, _ := gotBody["system"].([]any)
	if len(system) != 1 || system[0].(map[string]any)["text"] != "You are helpful." {
		t.Fatalf("system = %+v", gotBody["system"])
	}
	if _, ok := system[0].(map[string]any)["cache_control"]; ok {
		t.Fatalf("cache_control should be absent without CacheRetention: %+v", system[0])
	}
	messages, _ := gotBody["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %+v", gotBody["messages"])
	}
	user := messages[0].(map[string]any)
	if user["role"] != "user" {
		t.Fatalf("message role = %v", user["role"])
	}
	blocks := user["content"].([]any)
	if len(blocks) != 1 || blocks[0].(map[string]any)["text"] != "read a.txt" {
		t.Fatalf("message content = %+v", blocks)
	}
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", gotBody["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["name"] != "Read" || tool["description"] != "Read a file" {
		t.Fatalf("tool = %+v", tool)
	}
	schema := tool["input_schema"].(map[string]any)
	if schema["type"] != "object" || schema["required"].([]any)[0] != "path" {
		t.Fatalf("input_schema = %+v", schema)
	}
}

func TestRequestConversionImagesToolResultsThinkingAndCache(t *testing.T) {
	var gotBody map[string]any
	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseEvent("message_start", `{"type":"message_start","message":{"id":"msg","model":"claude-sonnet-4-5","usage":{"input_tokens":1}}}`)+
			sseEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)+
			sseEvent("message_stop", `{"type":"message_stop"}`))
	})

	temperature := 0.7
	req := &model.Request{
		Model: sonnetModel(t),
		System: []model.ContentBlock{
			{Type: model.ContentText, Text: "system prompt"},
		},
		Messages: []model.Message{
			{Role: model.RoleUser, Content: []model.ContentBlock{
				{Type: model.ContentText, Text: "look"},
				{Type: model.ContentImage, Image: &model.ImageContent{Data: "aGVsbG8=", MIMEType: "image/png"}},
			}},
			{Role: model.RoleAssistant, Content: []model.ContentBlock{
				{Type: model.ContentText, Text: "calling"},
				{Type: model.ContentToolCall, ToolCall: &model.ToolCall{
					ID: "toolu_9", Name: "Read", Arguments: map[string]any{"path": "a.txt"},
				}},
			}},
			{Role: model.RoleTool, Content: []model.ContentBlock{
				{Type: model.ContentToolResult, ToolResult: &model.ToolResult{
					ToolCallID: "toolu_9", Text: "file body",
				}},
			}},
			{Role: model.RoleUser, Content: []model.ContentBlock{
				{Type: model.ContentText, Text: "thanks"},
			}},
		},
		Tools: []model.ToolDeclaration{{
			Name:       "Read",
			Parameters: json.RawMessage(`{"properties":{"path":{"type":"string"}}}`),
		}},
		Thinking:       model.ThinkingHigh,
		MaxTokens:      20_000,
		Temperature:    &temperature,
		CacheRetention: model.CacheRetentionLong,
	}

	stream, err := provider.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collectEvents(t, stream)

	// System block carries a 1h cache breakpoint.
	system := gotBody["system"].([]any)[0].(map[string]any)
	cache := system["cache_control"].(map[string]any)
	if cache["type"] != "ephemeral" || cache["ttl"] != "1h" {
		t.Fatalf("system cache_control = %+v", cache)
	}

	// Tool schema was normalized to an object and the last tool is cached.
	tools := gotBody["tools"].([]any)
	tool := tools[len(tools)-1].(map[string]any)
	schema := tool["input_schema"].(map[string]any)
	if schema["type"] != "object" || schema["properties"] == nil {
		t.Fatalf("normalized schema = %+v", schema)
	}
	if tool["cache_control"] == nil {
		t.Fatalf("last tool has no cache_control: %+v", tool)
	}

	messages := gotBody["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %+v", messages)
	}
	userBlocks := messages[0].(map[string]any)["content"].([]any)
	image := userBlocks[1].(map[string]any)
	source := image["source"].(map[string]any)
	if image["type"] != "image" || source["type"] != "base64" ||
		source["media_type"] != "image/png" || source["data"] != "aGVsbG8=" {
		t.Fatalf("image block = %+v", image)
	}

	assistantBlocks := messages[1].(map[string]any)["content"].([]any)
	toolUse := assistantBlocks[1].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "toolu_9" || toolUse["name"] != "Read" {
		t.Fatalf("tool_use block = %+v", toolUse)
	}
	if toolUse["input"].(map[string]any)["path"] != "a.txt" {
		t.Fatalf("tool_use input = %+v", toolUse["input"])
	}

	// The tool result and the following user text merge into one user message,
	// and its last block carries the history cache breakpoint.
	toolUserBlocks := messages[2].(map[string]any)["content"].([]any)
	if len(toolUserBlocks) != 2 {
		t.Fatalf("tool result message = %+v", toolUserBlocks)
	}
	result := toolUserBlocks[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "toolu_9" || result["content"] != "file body" {
		t.Fatalf("tool_result block = %+v", result)
	}
	if toolUserBlocks[1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("last message block has no cache_control: %+v", toolUserBlocks[1])
	}

	// Extended thinking was enabled and temperature dropped.
	thinking := gotBody["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(16384) {
		t.Fatalf("thinking = %+v", thinking)
	}
	if _, ok := gotBody["temperature"]; ok {
		t.Fatalf("temperature should be omitted with thinking enabled: %+v", gotBody["temperature"])
	}
}

func TestConfigAndRequestHeaders(t *testing.T) {
	var gotHeader http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseEvent("message_start", `{"type":"message_start","message":{"id":"m","model":"claude-sonnet-4-5","usage":{"input_tokens":1}}}`)+
			sseEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`)+
			sseEvent("message_stop", `{"type":"message_stop"}`))
	}))
	t.Cleanup(server.Close)

	provider := New(Config{
		APIKey:     "config-key",
		BaseURL:    server.URL + "/",
		APIVersion: "2024-01-01",
		Headers:    map[string]string{"X-Custom": "config"},
	})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    sonnetModel(t),
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
	if got := gotHeader.Get("anthropic-version"); got != "2024-01-01" {
		t.Fatalf("anthropic-version = %q", got)
	}
	if got := gotHeader.Get("x-api-key"); got != "config-key" {
		t.Fatalf("x-api-key = %q", got)
	}
	if provider.Models()[0].BaseURL != strings.TrimSuffix(server.URL, "/") {
		t.Fatalf("Models() baseURL = %q", provider.Models()[0].BaseURL)
	}
}

func TestStreamHTTPErrorBecomesErrorEvent(t *testing.T) {
	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	})

	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    sonnetModel(t),
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
	if events[1].Err == nil || events[1].Err.Code != "rate_limit_error" ||
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

func TestStreamInvalidToolArguments(t *testing.T) {
	sse := "" +
		sseEvent("message_start", `{"type":"message_start","message":{"id":"msg_2","model":"claude-sonnet-4-5","usage":{"input_tokens":10}}}`) +
		sseEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_2","name":"broken","input":{}}}`) +
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{not json"}}`) +
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sseEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`) +
		sseEvent("message_stop", `{"type":"message_stop"}`)

	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    sonnetModel(t),
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

func TestStreamThinkingBlocks(t *testing.T) {
	sse := "" +
		sseEvent("message_start", `{"type":"message_start","message":{"id":"msg_3","model":"claude-sonnet-4-5","usage":{"input_tokens":5}}}`) +
		sseEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`) +
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`) +
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`) +
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sseEvent("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`) +
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`) +
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":1}`) +
		sseEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`) +
		sseEvent("message_stop", `{"type":"message_stop"}`)

	provider, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    sonnetModel(t),
		Thinking: model.ThinkingMedium,
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "go"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := collectEvents(t, stream)
	if err := model.ValidateSequence(events); err != nil {
		t.Fatalf("invalid event sequence: %v", err)
	}
	kinds := make([]model.StreamEventType, len(events))
	for i, event := range events {
		kinds[i] = event.Type
	}
	want := []model.StreamEventType{
		model.StreamStart,
		model.StreamThinkingStart, model.StreamThinkingDelta, model.StreamThinkingEnd,
		model.StreamTextStart, model.StreamTextDelta, model.StreamTextEnd,
		model.StreamDone,
	}
	if len(kinds) != len(want) {
		t.Fatalf("events = %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("events = %v, want %v", kinds, want)
		}
	}
	message, _ := stream.Result()
	if len(message.Content) != 2 || message.Content[0].Type != model.ContentThinking ||
		message.Content[0].Thinking != "plan" || message.Content[0].ThinkingSignature != "sig" {
		t.Fatalf("thinking block = %+v", message.Content)
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
		Model:    sonnetModel(t),
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentBlock{{Type: model.ContentText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	go func() {
		// Drain events so the provider goroutine is not blocked on the channel.
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
	sse := sseEvent("message_start", `{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-5","usage":{"input_tokens":10}}}`) +
		sseEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		sseEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`) +
		sseEvent("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		sseEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`) +
		sseEvent("message_stop", `{"type":"message_stop"}`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"upstream hiccup"}}`)
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
		Model:    sonnetModel(t),
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
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`)
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
		Model:    sonnetModel(t),
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
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"down"}}`)
	}))
	t.Cleanup(server.Close)

	provider := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Retry:   &model.TransportRetryPolicy{MaxRetries: 0},
	})
	stream, err := provider.Stream(context.Background(), &model.Request{
		Model:    sonnetModel(t),
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
