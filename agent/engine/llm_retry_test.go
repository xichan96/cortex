package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

func TestIsRetryableLLMError(t *testing.T) {
	retry := []string{"429 too many requests", "rate limit exceeded", "HTTP 503", "connection reset", "unexpected EOF"}
	for _, s := range retry {
		if !isRetryableLLMError(errors.New(s)) {
			t.Errorf("%q should be retryable", s)
		}
	}
	for _, s := range []string{"invalid api key", "400 bad request", "context length exceeded", ""} {
		if isRetryableLLMError(errors.New(s)) {
			t.Errorf("%q should not be retryable", s)
		}
	}
}

type retryProvider struct {
	*MockLLMProvider
	mu    sync.Mutex
	fails int
	calls int
}

func (p *retryProvider) ChatWithTools(context.Context, []types.Message, []types.Tool) (types.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls <= p.fails {
		return types.Message{}, errors.New("429 rate limit")
	}
	return types.Message{Role: "assistant", Content: "ok"}, nil
}

func TestChatWithToolsRetryRecoversTransientErrors(t *testing.T) {
	cfg := types.NewAgentConfig()
	provider := &retryProvider{MockLLMProvider: NewMockLLMProvider(), fails: 2}
	eng := NewAgentEngine(provider, cfg)
	resp, err := eng.chatWithToolsRetry(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if resp.Content != "ok" || provider.calls != 3 {
		t.Fatalf("content=%q calls=%d, want ok after 3 calls", resp.Content, provider.calls)
	}
}

func TestIsContextOverflowError(t *testing.T) {
	for _, s := range []string{"maximum context length exceeded", "context_length_exceeded", "prompt is too long"} {
		if !isContextOverflowError(errors.New(s)) {
			t.Errorf("%q should be an overflow", s)
		}
	}
	if isContextOverflowError(errors.New("invalid api key")) {
		t.Fatal("auth error classified as overflow")
	}
}

func TestTrimMessagesForOverflowKeepsSystemAndSafeTail(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "u1"},
		{Role: "assistant", ToolCalls: []types.ToolCall{call("a")}},
		{Role: "tool", ToolCallID: "a", Content: "r"},
		{Role: "user", Content: "u2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "u3"},
	}
	got := trimMessagesForOverflow(msgs, 2)
	if len(got) == 0 || got[0].Role != "system" {
		t.Fatalf("system message lost: %#v", got)
	}
	if got[1].Role == "tool" {
		t.Fatalf("trimmed tail starts with an orphan tool result: %#v", got)
	}
}

func TestSafeTailBoundarySkipsOrphanToolResults(t *testing.T) {
	msgs := []types.Message{
		{Role: "assistant", ToolCalls: []types.ToolCall{call("a")}},
		{Role: "tool", ToolCallID: "a"},
		{Role: "user", Content: "next"},
	}
	if got := safeTailBoundary(msgs, 1); got != 2 {
		t.Fatalf("boundary = %d, want 2", got)
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		msg  string
		want time.Duration
	}{
		{"anthropic API error 503: all upstream targets failed: network, retry after 4s", 4 * time.Second},
		{"429 too many requests; retry-after: 2", 2 * time.Second},
		{"retry_after=1500ms", 1500 * time.Millisecond},
		{"no hint here", 0},
	}
	for _, tc := range cases {
		if got := parseRetryAfter(errors.New(tc.msg)); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", tc.msg, got, tc.want)
		}
	}
	if got := retryDelayForError(errors.New("503 retry after 4s"), 0); got != 4*time.Second {
		t.Fatalf("retryDelayForError = %s, want 4s", got)
	}
	if got := retryDelayForError(errors.New("429"), 0); got != 500*time.Millisecond {
		t.Fatalf("fallback delay = %s, want 500ms", got)
	}
}
