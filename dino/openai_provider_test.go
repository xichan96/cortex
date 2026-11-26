package dino

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

func openAISSE(data string) string {
	return "data: " + data + "\n\n"
}

func openAITextSSE(text string) string {
	return openAISSE(`{"id":"c","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"`+text+`"}}]}`) +
		openAISSE(`{"id":"c","model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19}}`) +
		openAISSE(`[DONE]`)
}

// TestOpenAIProviderChat drives the registered "openai" provider through a
// DinoFactory against a fake chat-completions endpoint. It verifies the
// OpenAI-compatible protocol's request shape, auth header, usage mapping and
// the prefer_max_completion_tokens compatibility switch.
func TestOpenAIProviderChat(t *testing.T) {
	// Other package tests clear the global registry, so re-register here.
	RegisterLLMProvider(OpenAIProviderType, newOpenAIModelProvider)

	var (
		gotPath string
		gotAuth string
		gotBody map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &gotBody); err != nil {
			t.Errorf("decode body: %v (%s)", err, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, openAITextSSE("Hello from dino"))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Provider.Type = OpenAIProviderType
	cfg.Provider.BaseURL = srv.URL + "/v1"
	cfg.Provider.APIKey = "test-key"
	cfg.Provider.PreferMaxCompletionTokens = true
	cfg.DefaultModel = "gpt-4o-mini"
	cfg.MaxTokens = 256

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	provider := factory.GetLLMProvider()
	if provider == nil {
		t.Fatal("GetLLMProvider returned nil")
	}

	msg, err := provider.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if msg.Content != "Hello from dino" {
		t.Errorf("content = %q, want %q", msg.Content, "Hello from dino")
	}
	if msg.Usage.PromptTokens != 12 || msg.Usage.CompletionTokens != 7 || msg.Usage.TotalTokens != 19 {
		t.Errorf("usage = %+v, want prompt=12 completion=7 total=19", msg.Usage)
	}

	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody["model"] != "gpt-4o-mini" {
		t.Errorf("body model = %v", gotBody["model"])
	}
	if _, ok := gotBody["max_tokens"]; ok {
		t.Errorf("max_tokens present despite prefer_max_completion_tokens: %+v", gotBody)
	}
	if gotBody["max_completion_tokens"] != float64(256) {
		t.Errorf("max_completion_tokens = %v, want 256", gotBody["max_completion_tokens"])
	}
}
