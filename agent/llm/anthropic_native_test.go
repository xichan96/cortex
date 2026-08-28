package llm

import (
	"io"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

// mockSSEBody builds an io.ReadCloser from raw SSE text.
func mockSSEBody(sse string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(sse))
}

// TestReadStreamBackfillsCacheUsage (U6) verifies message_start cache fields
// are backfilled into types.Usage using the B1 formula:
//   PromptTokens = input_tokens + cache_read_input_tokens + cache_creation_input_tokens
//   TotalTokens  = PromptTokens + output_tokens
func TestReadStreamBackfillsCacheUsage(t *testing.T) {
	sse := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"output_tokens\":5,\"cache_read_input_tokens\":300,\"cache_creation_input_tokens\":200}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n" +
		"event: content_block_delta\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: content_block_stop\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	p := &NativeAnthropicProvider{}
	out := make(chan types.StreamMessage, 16)
	p.readStream(mockSSEBody(sse), out)

	var final *types.Usage
	for msg := range out {
		if msg.Type == "end" {
			final = msg.Usage
		}
	}
	if final == nil {
		t.Fatal("expected end event with usage")
	}
	if final.CachedTokens != 300 {
		t.Errorf("CachedTokens = %d, want 300", final.CachedTokens)
	}
	if final.CacheCreationTokens != 200 {
		t.Errorf("CacheCreationTokens = %d, want 200", final.CacheCreationTokens)
	}
	// B1: total prompt = uncached (100) + cache read (300) + cache creation (200) = 600
	if final.PromptTokens != 600 {
		t.Errorf("PromptTokens = %d, want 600 (total input incl. cache)", final.PromptTokens)
	}
	if final.CompletionTokens != 5 {
		t.Errorf("CompletionTokens = %d, want 5", final.CompletionTokens)
	}
	if final.TotalTokens != 605 {
		t.Errorf("TotalTokens = %d, want 605", final.TotalTokens)
	}
}

// TestReadStreamNoCacheFields ensures absence of cache fields degrades to zero
// and PromptTokens stays the plain input_tokens total.
func TestReadStreamNoCacheFields(t *testing.T) {
	sse := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":50,\"output_tokens\":10}}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"output_tokens\":10}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"

	p := &NativeAnthropicProvider{}
	out := make(chan types.StreamMessage, 16)
	p.readStream(mockSSEBody(sse), out)

	var final *types.Usage
	for msg := range out {
		if msg.Type == "end" {
			final = msg.Usage
		}
	}
	if final == nil {
		t.Fatal("expected end event with usage")
	}
	if final.PromptTokens != 50 || final.TotalTokens != 60 {
		t.Errorf("PromptTokens/TotalTokens = %d/%d, want 50/60", final.PromptTokens, final.TotalTokens)
	}
	if final.CachedTokens != 0 || final.CacheCreationTokens != 0 {
		t.Errorf("cache fields should default to 0, got %d/%d", final.CachedTokens, final.CacheCreationTokens)
	}
}

// TestCollectStreamPassesUsage (U7) verifies collectStream forwards backfilled usage.
func TestCollectStreamPassesUsage(t *testing.T) {
	ch := make(chan types.StreamMessage, 3)
	ch <- types.StreamMessage{Type: "chunk", Content: "answer"}
	ch <- types.StreamMessage{Type: "end", Usage: &types.Usage{
		PromptTokens:        600,
		CompletionTokens:    5,
		TotalTokens:         605,
		CachedTokens:        300,
		CacheCreationTokens: 200,
	}}
	close(ch)

	msg, err := collectStream(ch)
	if err != nil {
		t.Fatalf("collectStream error: %v", err)
	}
	if msg.Usage.CachedTokens != 300 {
		t.Errorf("CachedTokens = %d, want 300", msg.Usage.CachedTokens)
	}
	if msg.Usage.CacheCreationTokens != 200 {
		t.Errorf("CacheCreationTokens = %d, want 200", msg.Usage.CacheCreationTokens)
	}
	if msg.Usage.PromptTokens != 600 {
		t.Errorf("PromptTokens = %d, want 600", msg.Usage.PromptTokens)
	}
}
