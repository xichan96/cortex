package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// spillFactor is how far over budget output must be before it is written to
// disk instead of kept (middle-truncated) in the context. Mirrors
// types.TruncateToolResult's heuristic.
const spillFactor = 8

// DirSpillWriter writes oversized output to Dir using the call ID as filename.
// Hosts that need a different layout inject their own SpillWriter instead.
type DirSpillWriter struct {
	Dir string
}

// Spill implements SpillWriter.
func (w DirSpillWriter) Spill(_ context.Context, c *Call, content string) (string, error) {
	if w.Dir == "" {
		return "", errors.New("dir spill writer: empty dir")
	}
	if err := os.MkdirAll(w.Dir, 0o750); err != nil {
		return "", err
	}
	name := spillFileName(c)
	path := filepath.Join(w.Dir, name+".txt")
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		return "", err
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs, nil
	}
	return path, nil
}

func spillFileName(c *Call) string {
	name := ""
	if c != nil {
		name = sanitizeFileName(c.ID)
		if name == "" {
			name = sanitizeFileName(c.Name)
		}
	}
	if name == "" {
		name = fmt.Sprintf("call_%d", time.Now().UnixNano())
	}
	return name
}

// sanitizeFileName keeps path-safe bytes and caps the length.
func sanitizeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= 96 {
			break
		}
	}
	return b.String()
}

// sanitize is the single truncation entry point. It applies the per-tool or
// default budget to every text part, spills far-over-budget output through the
// injected writer, and otherwise reuses types.TruncateToolResult (header,
// omission marker, and UTF-8 safety all counted inside the budget).
func (p *Pipeline) sanitize(ctx context.Context, c *Call, r *Result) {
	if r == nil || len(r.Content) == 0 {
		return
	}
	budget := p.trunc.DefaultMaxBytes
	if c != nil && c.Tool != nil {
		if m := c.Tool.Metadata().MaxTruncationLength; m > 0 {
			budget = m
		}
	}
	if budget <= 0 {
		budget = types.MaxTruncationLength
	}

	parts := make([]types.MessagePart, 0, len(r.Content))
	for _, part := range r.Content {
		textPart, ok := part.(types.TextPart)
		if !ok || len(textPart.Text) <= budget {
			parts = append(parts, part)
			continue
		}
		text := textPart.Text
		header := p.outputHeader(c, r, text)

		if p.trunc.Spill != nil && len(text) > budget*spillFactor {
			if path, err := p.trunc.Spill.Spill(ctx, c, text); err == nil && path != "" {
				header.SavedPath = path
				display := types.BuildOutputHeader(header) +
					"\nFull output saved to: " + path + " — use Read/Grep to view.\n"
				if len(display) > budget {
					// The path hint must survive even a tiny budget; TruncateMiddle
					// counts its marker inside the budget.
					display, _ = types.TruncateMiddle(display, budget)
				}
				parts = append(parts, types.TextPart{Text: display})
				continue
			}
			// Spill failed: fall through to in-context truncation.
		}

		display, _ := types.TruncateToolResult(text, budget, "", header)
		parts = append(parts, types.TextPart{Text: display})
	}
	r.Content = parts
}

func (p *Pipeline) outputHeader(c *Call, r *Result, text string) types.OutputHeader {
	if p.trunc.Header != nil {
		h := p.trunc.Header(c, r)
		if h.ChunkID == "" && c != nil {
			h.ChunkID = c.ID
		}
		return h
	}
	h := types.OutputHeader{
		OriginalBytes:  len(text),
		OriginalTokens: types.RoughTokenEstimate(text),
	}
	// Sub-millisecond durations round to "0s" in the rendered header; omit them
	// rather than print a misleading zero.
	if r.Duration >= time.Millisecond {
		h.WallTime = r.Duration
	}
	if c != nil {
		h.ChunkID = c.ID
	}
	if text != "" {
		h.TotalLines = strings.Count(text, "\n") + 1
	}
	return h
}
