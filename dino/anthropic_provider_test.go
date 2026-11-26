package dino

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xichan96/cortex/agent/types"
	pkgerrors "github.com/xichan96/cortex/pkg/errors"
)

// anthropicSSE renders one Anthropic Messages SSE frame.
func anthropicSSE(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// anthropicTextSSE is a complete text-only response with usage.
func anthropicTextSSE(text string) string {
	return anthropicSSE("message_start", `{"type":"message_start","message":{"id":"msg_test","model":"claude-sonnet-4-5","usage":{"input_tokens":12,"cache_read_input_tokens":3,"cache_creation_input_tokens":2,"output_tokens":1}}}`) +
		anthropicSSE("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		anthropicSSE("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+text+`"}}`) +
		anthropicSSE("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		anthropicSSE("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`) +
		anthropicSSE("message_stop", `{"type":"message_stop"}`)
}

// TestAnthropicModelProviderChat drives the registered "anthropic" provider
// through a DinoFactory against a fake Messages API. It verifies the request
// carries the Anthropic auth headers, that prompt caching is applied by
// default, and that text/usage come back through the types.LLMProvider
// adapter without touching the real API.
func TestAnthropicModelProviderChat(t *testing.T) {
	// Other package tests clear the global registry, so re-register here.
	RegisterLLMProvider(AnthropicProviderType, newAnthropicModelProvider)

	var (
		gotAPIKey  string
		gotVersion string
		gotCustom  string
		gotBody    map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q, want /v1/messages", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotCustom = r.Header.Get("x-cortex-test")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("decode body: %v (%s)", err, body)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicTextSSE("Hello from dino"))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Provider.Type = AnthropicProviderType
	cfg.Provider.BaseURL = srv.URL
	cfg.Provider.APIKey = "test-key"
	cfg.Provider.Headers = map[string]string{"x-cortex-test": "present"}
	cfg.DefaultModel = "claude-sonnet-4-5"
	cfg.MaxTokens = 256
	cfg.Temperature = 0.3

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	provider := factory.GetLLMProvider()
	if provider == nil {
		t.Fatal("GetLLMProvider returned nil")
	}
	if name := provider.GetModelName(); name != "claude-sonnet-4-5" {
		t.Errorf("GetModelName = %q, want claude-sonnet-4-5", name)
	}

	msg, err := provider.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if msg.Content != "Hello from dino" {
		t.Errorf("content = %q, want %q", msg.Content, "Hello from dino")
	}
	// Input is input_tokens + cache_read + cache_creation; output is the
	// message_delta output_tokens.
	if msg.Usage.PromptTokens != 17 || msg.Usage.CompletionTokens != 7 || msg.Usage.TotalTokens != 24 {
		t.Errorf("usage = %+v, want prompt=17 completion=7 total=24", msg.Usage)
	}
	if msg.Usage.CachedTokens != 3 || msg.Usage.CacheCreationTokens != 2 {
		t.Errorf("cache usage = %+v, want cached=3 creation=2", msg.Usage)
	}

	if gotAPIKey != "test-key" {
		t.Errorf("x-api-key = %q, want test-key", gotAPIKey)
	}
	if gotVersion == "" {
		t.Error("anthropic-version header is missing")
	}
	if gotCustom != "present" {
		t.Errorf("custom header = %q, want present", gotCustom)
	}
	if gotBody["model"] != "claude-sonnet-4-5" {
		t.Errorf("body model = %v, want claude-sonnet-4-5", gotBody["model"])
	}
	if gotBody["max_tokens"] != float64(256) {
		t.Errorf("body max_tokens = %v, want 256", gotBody["max_tokens"])
	}
	if gotBody["stream"] != true {
		t.Errorf("body stream = %v, want true", gotBody["stream"])
	}
	// Config.Temperature is float32, so compare against the widened value.
	if want := float64(float32(0.3)); gotBody["temperature"] != want {
		t.Errorf("body temperature = %v, want %v", gotBody["temperature"], want)
	}
	if !lastContentCacheControl(gotBody) {
		t.Error("expected a prompt-cache breakpoint on the last message")
	}
}

// lastContentCacheControl reports whether the final content block of the last
// wire message carries a cache_control marker.
func lastContentCacheControl(body map[string]any) bool {
	messages, _ := body["messages"].([]any)
	if len(messages) == 0 {
		return false
	}
	last, _ := messages[len(messages)-1].(map[string]any)
	content, _ := last["content"].([]any)
	if len(content) == 0 {
		return false
	}
	block, _ := content[len(content)-1].(map[string]any)
	return block["cache_control"] != nil
}

// TestAnthropicProviderPromptCachingDisabled verifies the escape hatch:
// enabled=false sends no cache_control marker.
func TestAnthropicProviderPromptCachingDisabled(t *testing.T) {
	RegisterLLMProvider(AnthropicProviderType, newAnthropicModelProvider)

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicTextSSE("ok"))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Provider.Type = AnthropicProviderType
	cfg.Provider.BaseURL = srv.URL
	cfg.Provider.APIKey = "test-key"
	cfg.DefaultModel = "claude-sonnet-4-5"
	cfg.PromptCaching.Enabled = false

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	if _, err := factory.GetLLMProvider().Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if lastContentCacheControl(gotBody) {
		t.Error("cache_control present while prompt caching is disabled")
	}
}

// TestAnthropicModelProviderTemperatureUnset verifies that dino's zero-value
// temperature is omitted rather than sent as an explicit 0.
func TestAnthropicModelProviderTemperatureUnset(t *testing.T) {
	RegisterLLMProvider(AnthropicProviderType, newAnthropicModelProvider)

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicTextSSE("ok"))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Provider.Type = AnthropicProviderType
	cfg.Provider.BaseURL = srv.URL
	cfg.Provider.APIKey = "test-key"
	cfg.DefaultModel = "claude-sonnet-4-5"
	cfg.Temperature = 0 // DefaultConfig sets 0.7; test the zero-value contract.

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	if _, err := factory.GetLLMProvider().Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if _, ok := gotBody["temperature"]; ok {
		t.Errorf("temperature should be omitted when unset, body = %v", gotBody["temperature"])
	}
}

// TestAnthropicModelProviderUnknownModelPassThrough checks that a model ID
// outside the bundled catalog is forwarded as-is instead of failing.
func TestAnthropicModelProviderUnknownModelPassThrough(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Provider.APIKey = "test-key"
	cfg.DefaultModel = "claude-not-yet-in-catalog"

	provider, err := newAnthropicModelProvider(cfg)
	if err != nil {
		t.Fatalf("newAnthropicModelProvider: %v", err)
	}
	if name := provider.GetModelName(); name != "claude-not-yet-in-catalog" {
		t.Errorf("GetModelName = %q, want claude-not-yet-in-catalog", name)
	}
	if version := provider.GetModelMetadata().Version; version != "claude-not-yet-in-catalog" {
		t.Errorf("metadata version = %q, want claude-not-yet-in-catalog", version)
	}
}

// TestAnthropicModelProviderRequiresAPIKey mirrors the legacy "anthropic"
// factory: an empty key is rejected with the shared sentinel error.
func TestAnthropicModelProviderRequiresAPIKey(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultModel = "claude-sonnet-4-5"

	_, err := newAnthropicModelProvider(cfg)
	if err == nil {
		t.Fatal("expected an error for an empty API key")
	}
	if !stdErrors.Is(err, pkgerrors.EC_LLM_API_KEY_REQUIRED) {
		t.Errorf("error = %v, want EC_LLM_API_KEY_REQUIRED", err)
	}
}

// TestAnthropicModelProviderEmptyDefaultModelUsesCatalog ensures an empty
// DefaultModel falls back to the catalog's first entry.
func TestAnthropicModelProviderEmptyDefaultModelUsesCatalog(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Provider.APIKey = "test-key"

	provider, err := newAnthropicModelProvider(cfg)
	if err != nil {
		t.Fatalf("newAnthropicModelProvider: %v", err)
	}
	if name := provider.GetModelName(); name == "" {
		t.Error("GetModelName is empty, want first catalog model")
	}
}
