package types

import (
	"strings"
	"testing"
)

// TestImagePartsUseFixedEstimate locks the post-fix contract: every image
// part costs ImageEstimateTokens regardless of payload size, URL length or
// detail hint. The old len(Data)/4 estimate made a 400KB screenshot look like
// ~100k tokens (the Dojo auto-compact regression).
func TestImagePartsUseFixedEstimate(t *testing.T) {
	if ImageEstimateTokens != 1600 {
		t.Fatalf("ImageEstimateTokens = %d, want 1600 (aligns with Dojo's UI estimate)", ImageEstimateTokens)
	}
	cases := []struct {
		name string
		part MessagePart
	}{
		{"tiny-data", ImageDataPart{Data: []byte{1, 2, 3, 4}, MIMEType: "image/png"}},
		{"400kb-data", ImageDataPart{Data: make([]byte, 400*1024), MIMEType: "image/png"}},
		{"3mb-data-url-payload", ImageDataPart{Data: make([]byte, 3*1024*1024), MIMEType: "image/jpeg"}},
		{"empty-data", ImageDataPart{MIMEType: "image/png"}},
		{"short-url", ImageURLPart{URL: "https://example.test/a.png"}},
		{"long-url", ImageURLPart{URL: "https://example.test/" + strings.Repeat("a", 4000) + ".png", Detail: "high"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RoughTokensForMessage(Message{Role: "user", Parts: []MessagePart{tc.part}})
			if got != ImageEstimateTokens {
				t.Fatalf("RoughTokensForMessage(image) = %d, want %d (payload/URL must not affect the estimate)", got, ImageEstimateTokens)
			}
		})
	}
}

// TestImagePartsStackWithText verifies text and image parts add up: the text
// behavior is unchanged and each image contributes exactly ImageEstimateTokens.
func TestImagePartsStackWithText(t *testing.T) {
	text := strings.Repeat("x", 400) // 100 tokens
	msg := Message{
		Role:    "user",
		Content: "caption",
		Parts: []MessagePart{
			TextPart{Text: text},
			ImageDataPart{Data: []byte("one")},
			ImageURLPart{URL: "https://example.test/two.png"},
			TextPart{Text: "tail"},
		},
	}
	want := RoughTokenEstimate("caption") +
		RoughTokenEstimate(text) +
		2*ImageEstimateTokens +
		RoughTokenEstimate("tail")
	if got := RoughTokensForMessage(msg); got != want {
		t.Fatalf("RoughTokensForMessage = %d, want %d", got, want)
	}
}

// TestTextOnlyEstimateUnchanged pins the non-image behavior: role metadata,
// Content and tool calls are still estimated with RoughTokenEstimate.
func TestTextOnlyEstimateUnchanged(t *testing.T) {
	msg := Message{
		Role:       "assistant",
		Content:    "plain text content",
		Name:       "helper",
		ToolCallID: "call-1",
		Parts:      []MessagePart{TextPart{Text: "part text"}},
	}
	want := RoughTokenEstimate("plain text content") +
		RoughTokenEstimate("helper") +
		RoughTokenEstimate("call-1") +
		RoughTokenEstimate("part text")
	if got := RoughTokensForMessage(msg); got != want {
		t.Fatalf("RoughTokensForMessage = %d, want %d", got, want)
	}
}
