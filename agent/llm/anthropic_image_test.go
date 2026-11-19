package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

// TestNativeAnthropicSendsImageBlocks 图片必须以真正的 image block 发给上游，
// 而不是 `[image data: ...]` 文本占位（那会让模型看不到图）。
func TestNativeAnthropicSendsImageBlocks(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()

	p := newNativeAnthropicProvider("k", srv.URL, "claude-test")
	if _, err := p.Chat(context.Background(), []types.Message{{
		Role: "user",
		Parts: []types.MessagePart{
			types.ImageDataPart{Data: []byte("img"), MIMEType: "image/png"},
			types.TextPart{Text: "看图"},
		},
	}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if !strings.Contains(body, `"type":"image"`) {
		t.Fatalf("missing image block: %s", body)
	}
	if !strings.Contains(body, `"media_type":"image/png"`) || !strings.Contains(body, `"data":"aW1n"`) {
		t.Fatalf("missing base64 source: %s", body)
	}

	// URL 形式同样要映射成 image block。
	if _, err := p.Chat(context.Background(), []types.Message{{
		Role: "user",
		Parts: []types.MessagePart{
			types.ImageURLPart{URL: "https://example.com/a.png"},
		},
	}}); err != nil {
		t.Fatalf("Chat(url): %v", err)
	}
	if !strings.Contains(body, `"type":"image"`) || !strings.Contains(body, `"url":"https://example.com/a.png"`) {
		t.Fatalf("missing url image block: %s", body)
	}
}
