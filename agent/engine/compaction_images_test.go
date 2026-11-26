package engine

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

func imgData(id int) types.ImageDataPart {
	return types.ImageDataPart{Data: []byte(fmt.Sprintf("img-%d", id)), MIMEType: "image/png"}
}

// imageMarkers returns the payload markers of every ImageDataPart in order.
func imageMarkers(m types.Message) []string {
	var out []string
	for _, p := range m.Parts {
		if d, ok := p.(types.ImageDataPart); ok {
			out = append(out, string(d.Data))
		}
	}
	return out
}

// imageCount / placeholderCount aggregate over a message slice.
func imageCount(msgs []types.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(imageMarkers(m))
	}
	return n
}

func placeholderCount(msgs []types.Message) int {
	n := 0
	for _, m := range msgs {
		for _, p := range m.Parts {
			if tp, ok := p.(types.TextPart); ok && tp.Text == omittedImagePlaceholder {
				n++
			}
		}
	}
	return n
}

// TestTrimOlderImageParts_KeepsMostRecent is the core policy test: only the
// newest keep images survive, older ones become a TextPart placeholder at the
// same index, and role/tool pairing/other parts do not move.
func TestTrimOlderImageParts_KeepsMostRecent(t *testing.T) {
	history := []types.Message{
		{Role: "user", Content: "u0", Parts: []types.MessagePart{imgData(0)}},
		{
			Role:       "tool",
			Content:    "tool result",
			ToolCallID: "call-1",
			Parts: []types.MessagePart{
				types.TextPart{Text: "before"},
				imgData(1),
				imgData(2),
				types.TextPart{Text: "after"},
			},
		},
		{Role: "user", Content: "u2", Parts: []types.MessagePart{imgData(3), imgData(4)}},
	}

	out := trimOlderImageParts(history, 3)
	if len(out) != len(history) {
		t.Fatalf("message count changed: %d != %d", len(out), len(history))
	}

	// 5 images total, keep the newest 3: img-0 and img-1 are replaced.
	for _, m := range out[:1] {
		if len(m.Parts) != 1 {
			t.Fatalf("parts count changed: %v", m.Parts)
		}
		if tp, ok := m.Parts[0].(types.TextPart); !ok || tp.Text != omittedImagePlaceholder {
			t.Fatalf("oldest image not replaced: %#v", m.Parts[0])
		}
	}
	tool := out[1]
	if tool.Role != "tool" || tool.ToolCallID != "call-1" || tool.Content != "tool result" {
		t.Fatalf("tool message metadata changed: %+v", tool)
	}
	if len(tool.Parts) != 4 {
		t.Fatalf("tool parts count changed: %v", tool.Parts)
	}
	if tp, ok := tool.Parts[0].(types.TextPart); !ok || tp.Text != "before" {
		t.Fatalf("leading text part moved/lost: %#v", tool.Parts[0])
	}
	if tp, ok := tool.Parts[1].(types.TextPart); !ok || tp.Text != omittedImagePlaceholder {
		t.Fatalf("tool part 1 should be the placeholder, got %#v", tool.Parts[1])
	}
	if got := imageMarkers(tool); !reflect.DeepEqual(got, []string{"img-2"}) {
		t.Fatalf("tool kept images = %v, want [img-2]", got)
	}
	if tp, ok := tool.Parts[3].(types.TextPart); !ok || tp.Text != "after" {
		t.Fatalf("trailing text part moved/lost: %#v", tool.Parts[3])
	}
	if got := imageMarkers(out[2]); !reflect.DeepEqual(got, []string{"img-3", "img-4"}) {
		t.Fatalf("newest images changed: %v", got)
	}

	// The caller's history must not be mutated (repeated prepareMessages calls
	// re-read the same history).
	if got := imageMarkers(history[0]); !reflect.DeepEqual(got, []string{"img-0"}) {
		t.Fatalf("input history was mutated: %v", got)
	}
	if got := imageMarkers(history[1]); !reflect.DeepEqual(got, []string{"img-1", "img-2"}) {
		t.Fatalf("input history was mutated: %v", got)
	}
}

// TestTrimOlderImageParts_WithinKeepIsNoop verifies ≤ keep images are left
// alone (same slice, no placeholders).
func TestTrimOlderImageParts_WithinKeepIsNoop(t *testing.T) {
	history := []types.Message{
		{Role: "user", Parts: []types.MessagePart{imgData(0), imgData(1)}},
		{Role: "user", Parts: []types.MessagePart{imgData(2), imgData(3)}},
	}
	out := trimOlderImageParts(history, maxKeptImages)
	if placeholderCount(out) != 0 {
		t.Fatalf("unexpected placeholders: %v", out)
	}
	if !reflect.DeepEqual(out, history) {
		t.Fatalf("output diverged for ≤ keep images:\ngot=%v\nwant=%v", out, history)
	}
	if len(out) > 0 && &out[0] != &history[0] {
		t.Fatalf("expected the input slice to be returned unchanged")
	}
}

// TestTrimOlderImageParts_Idempotent verifies a second pass over the already
// trimmed output changes nothing.
func TestTrimOlderImageParts_Idempotent(t *testing.T) {
	history := []types.Message{
		{Role: "user", Parts: []types.MessagePart{imgData(0), imgData(1), imgData(2)}},
		{Role: "assistant", Parts: []types.MessagePart{imgData(3), imgData(4), imgData(5)}},
	}
	first := trimOlderImageParts(history, 2)
	second := trimOlderImageParts(first, 2)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("second call changed the output:\nfirst=%v\nsecond=%v", first, second)
	}
	if got := imageCount(first); got != 2 {
		t.Fatalf("kept images = %d, want 2", got)
	}
	if got := placeholderCount(first); got != 4 {
		t.Fatalf("placeholders = %d, want 4", got)
	}
}

// imageTestHistory builds a history whose head (index 0) carries one image,
// followed by four text fillers and six image messages. At the budget used by
// the compaction tests the middle image is dropped and the tail keeps five
// images, so exactly one tail image must be replaced.
func imageTestHistory() []types.Message {
	history := []types.Message{
		{Role: "user", Content: "head", Parts: []types.MessagePart{imgData(100)}},
	}
	for i := 0; i < 4; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		history = append(history, types.Message{
			Role:    role,
			Content: fmt.Sprintf("filler number %04d", i),
		})
	}
	for i := 0; i < 6; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		history = append(history, types.Message{
			Role:    role,
			Content: fmt.Sprintf("shot %d", i),
			Parts:   []types.MessagePart{imgData(200 + i)},
		})
	}
	return history
}

func imageTestConfig() *types.AgentConfig {
	cfg := types.NewAgentConfig()
	cfg.SystemMessage = "system"
	cfg.CompactionPrefix = true
	cfg.CacheAnchorTokens = 1700
	cfg.MaxBudgetTokens = 9700
	return cfg
}

// TestCompaction_DropsOlderTailImages drives the real over-budget trim: the
// head image is a prompt-cache anchor and stays verbatim, the middle image is
// dropped with the middle, and of the five tail images the oldest becomes a
// placeholder while the newest four survive.
func TestCompaction_DropsOlderTailImages(t *testing.T) {
	cfg := imageTestConfig()
	history := imageTestHistory()
	rem := cfg.MaxBudgetTokens - fixedTokens(cfg, nil, "input")
	if msgSumTokens(history) <= rem {
		t.Fatalf("test setup: history (%d) should exceed rem (%d)", msgSumTokens(history), rem)
	}

	out, folded := trimHistoryToTokenBudgetCompaction(history, cfg.MaxBudgetTokens, cfg, nil, "", nil, types.NewAgentInput("input"))
	if folded {
		t.Fatalf("nil generator should not fold a summary")
	}
	if got := msgSumTokens(out); got > rem {
		t.Fatalf("output over budget: %d > %d", got, rem)
	}

	if got := imageMarkers(out[0]); !reflect.DeepEqual(got, []string{"img-100"}) {
		t.Fatalf("head cache anchor image must stay verbatim, got %v", got)
	}
	if got, want := imageCount(out), 5; got != want {
		t.Fatalf("kept images = %d, want %d (head + newest four): %v", got, want, out)
	}
	if got, want := placeholderCount(out), 1; got != want {
		t.Fatalf("placeholders = %d, want %d", got, want)
	}
	var markers []string
	for _, m := range out {
		markers = append(markers, imageMarkers(m)...)
	}
	if want := []string{"img-100", "img-202", "img-203", "img-204", "img-205"}; !reflect.DeepEqual(markers, want) {
		t.Fatalf("kept image markers = %v, want %v", markers, want)
	}

	// Repeated calls over the same history must be stable, and must not have
	// mutated the caller's history.
	out2, _ := trimHistoryToTokenBudgetCompaction(history, cfg.MaxBudgetTokens, cfg, nil, "", nil, types.NewAgentInput("input"))
	if !reflect.DeepEqual(out, out2) {
		t.Fatalf("repeated trim is not stable:\nfirst=%v\nsecond=%v", out, out2)
	}
	if got := imageMarkers(history[5]); !reflect.DeepEqual(got, []string{"img-200"}) {
		t.Fatalf("input history was mutated: %v", got)
	}
}

// TestCompaction_UnderBudgetKeepsImages: a comfortable budget means no trim,
// so even many old images are left alone ("普通对话不受影响").
func TestCompaction_UnderBudgetKeepsImages(t *testing.T) {
	cfg := imageTestConfig()
	cfg.MaxBudgetTokens = 100000
	history := imageTestHistory()

	out, _ := trimHistoryToTokenBudgetCompaction(history, cfg.MaxBudgetTokens, cfg, nil, "", nil, types.NewAgentInput("input"))
	if !reflect.DeepEqual(out, history) {
		t.Fatalf("under-budget trim should return history unchanged")
	}
	if got, want := imageCount(out), 7; got != want {
		t.Fatalf("kept images = %d, want %d", got, want)
	}
	if placeholderCount(out) != 0 {
		t.Fatalf("unexpected placeholders under budget: %v", out)
	}
}

// TestCompaction_LLMPathDropsOlderTailImages covers the LLM summary variant
// (the other over-budget return path).
func TestCompaction_LLMPathDropsOlderTailImages(t *testing.T) {
	cfg := imageTestConfig()
	history := imageTestHistory()
	opts := &CompactionOptions{
		LLMSummaryGenerator: func(ctx context.Context, existing string, mid []types.Message) (string, error) {
			return "llm-summary", nil
		},
	}

	out, folded := trimHistoryToTokenBudgetCompaction(history, cfg.MaxBudgetTokens, cfg, opts, "", nil, types.NewAgentInput("input"))
	if !folded {
		t.Fatalf("expected the LLM summary to be folded, got %v", out)
	}
	rem := cfg.MaxBudgetTokens - fixedTokens(cfg, nil, "input")
	if got := msgSumTokens(out); got > rem {
		t.Fatalf("output over budget: %d > %d", got, rem)
	}
	if got := imageMarkers(out[0]); !reflect.DeepEqual(got, []string{"img-100"}) {
		t.Fatalf("head cache anchor image must stay verbatim, got %v", got)
	}
	if got, want := imageCount(out), 5; got != want {
		t.Fatalf("kept images = %d, want %d: %v", got, want, out)
	}
	if got, want := placeholderCount(out), 1; got != want {
		t.Fatalf("placeholders = %d, want %d", got, want)
	}
}

// TestLegacyTrimKeepsImages pins backward compatibility: CompactionPrefix=false
// uses trimHistoryToTokenBudget, which must not rewrite image parts.
func TestLegacyTrimKeepsImages(t *testing.T) {
	cfg := imageTestConfig()
	cfg.CompactionPrefix = false
	history := imageTestHistory()

	out := trimHistoryToTokenBudget(history, cfg.MaxBudgetTokens, cfg, nil, types.NewAgentInput("input"))
	if placeholderCount(out) != 0 {
		t.Fatalf("legacy path must not insert image placeholders: %v", out)
	}
	want := imageCount(history)
	// Only whole messages may be dropped; the kept ones keep their images.
	kept := 0
	for _, m := range out {
		kept += len(imageMarkers(m))
	}
	if kept == 0 {
		t.Fatalf("legacy trim dropped every image, history images=%d", want)
	}
	if kept > want {
		t.Fatalf("legacy trim created images: %d > %d", kept, want)
	}
}
