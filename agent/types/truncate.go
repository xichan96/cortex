package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// OutputHeader is the structured header prepended to a truncated tool output.
// Any field with a zero value omits its line (mirroring Codex's response_header).
type OutputHeader struct {
	ChunkID          string        `json:"chunk_id,omitempty"`
	WallTime         time.Duration `json:"wall_time,omitempty"`        // execution duration
	ExitCode         *int          `json:"exit_code,omitempty"`        // bash etc. when available
	OriginalBytes    int           `json:"original_bytes,omitempty"`   // raw output bytes before truncation
	OriginalTokens   int           `json:"original_tokens,omitempty"`  // approximate, via RoughTokenEstimate
	TotalLines       int           `json:"total_lines,omitempty"`      // line count of the raw output
	SavedPath        string        `json:"saved_path,omitempty"`       // when output was written to disk instead
	OmittedCharCount int           `json:"omitted_chars,omitempty"`    // chars elided by TruncateMiddle
}

// HeaderBudget caps how many bytes the structured header (including the
// "Output:" guide line) may occupy. maxLen*0.25 is a floor below which the
// header yields to the body, so a tiny per-tool budget (e.g. 256) still leaves
// the bulk of the budget to the body rather than to the header.
const HeaderBudget = 512

// headerFloor returns the header byte allowance: at most HeaderBudget, and at
// most maxLen/4 so the body always keeps at least 3/4 of the budget.
func headerAllowance(maxLen int) int {
	if maxLen <= 0 {
		maxLen = MaxTruncationLength
	}
	quarter := maxLen / 4
	if quarter < HeaderBudget {
		return quarter
	}
	return HeaderBudget
}

// TruncationMeta reports what a truncation did so the caller can record it.
type TruncationMeta struct {
	Truncated     bool
	SavedFilePath string
	OriginalBytes int
}

// BuildOutputHeader renders the structured header as text. One line per
// non-zero field, then a final "Output:" guide line separating header from
// body (mirrors Codex's response_header). It is the only place that builds the
// header, so byte accounting for the budget happens once.
func BuildOutputHeader(h OutputHeader) string {
	var b strings.Builder
	if h.ChunkID != "" {
		fmt.Fprintf(&b, "chunk_id: %s\n", h.ChunkID)
	}
	if h.WallTime > 0 {
		fmt.Fprintf(&b, "wall_time: %v\n", h.WallTime.Round(time.Millisecond))
	}
	if h.ExitCode != nil {
		fmt.Fprintf(&b, "exit_code: %d\n", *h.ExitCode)
	}
	if h.OriginalBytes > 0 {
		fmt.Fprintf(&b, "original_bytes: %d\n", h.OriginalBytes)
	}
	if h.OriginalTokens > 0 {
		fmt.Fprintf(&b, "original_tokens: %d\n", h.OriginalTokens)
	}
	if h.TotalLines > 0 {
		fmt.Fprintf(&b, "total_lines: %d\n", h.TotalLines)
	}
	if h.OmittedCharCount > 0 {
		fmt.Fprintf(&b, "omitted_chars: %d\n", h.OmittedCharCount)
	}
	b.WriteString("Output:\n")
	return b.String()
}

// TruncateMiddle keeps the head and tail of s, joining them with an omission
// marker, so the model sees both ends of a large output instead of only the
// head. It is UTF-8 safe (never splits a multi-byte rune) and respects a byte
// budget including the marker.
//
// The marker itself is counted inside the budget; the body share for head/tail
// is what remains. A minimum of minKeep bytes is given to each of head and
// tail; if the budget is too small for both, the head wins (the marker is
// shortened only as a last resort).
func TruncateMiddle(s string, maxBytes int) (out string, omitted int) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, 0
	}
	marker := "\n…truncated…\n"
	markerBytes := len(marker)

	// Reserve space for the marker and give the tail a fair share (default 50%).
	bodyBudget := maxBytes - markerBytes
	if bodyBudget <= 0 {
		// Ultra-small budget: keep only the head (still marker-free, exact fit).
		return truncateUTF8Head(s, maxBytes), utf8.RuneCountInString(s) - utf8.RuneCountInString(truncateUTF8Head(s, maxBytes))
	}
	tailShare := bodyBudget / 2
	headShare := bodyBudget - tailShare

	headBytes := utf8BytesAt(s, headShare)
	tail := utf8TailBytes(s, tailShare)
	if len(headBytes)+markerBytes+len(tail) > maxBytes {
		// Tail share too generous for its rune boundary; shrink head to fit.
		headBytes = utf8BytesAt(s, maxBytes-len(tail)-markerBytes)
	}
	out = string(headBytes) + marker + tail
	omitted = utf8.RuneCountInString(s) - (utf8.RuneCountInString(string(headBytes)) + utf8.RuneCountInString(tail))
	return out, omitted
}

// utf8BytesAt returns the longest prefix of s whose encoded length is at most
// n bytes without splitting a rune.
func utf8BytesAt(s string, n int) []byte {
	if n >= len(s) {
		return []byte(s)
	}
	b := []byte(s)
	for n > 0 && n < len(b) && !utf8.RuneStart(b[n]) {
		n--
	}
	return b[:n]
}

// utf8TailBytes returns the longest suffix of s whose encoded length is at
// most n bytes without splitting a rune.
func utf8TailBytes(s string, n int) string {
	if n >= len(s) {
		return s
	}
	start := len(s) - n
	for start > 0 && start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// truncateUTF8Head is a UTF-8-safe head-only truncation (no marker).
func truncateUTF8Head(s string, n int) string {
	if n >= len(s) {
		return s
	}
	return string(utf8BytesAt(s, n))
}

// TruncateToolResult is the single truncation entry point. It guarantees the
// returned display string is at most maxLen bytes (header + marker + body all
// within budget) and is UTF-8 safe.
//
// When writeDir is non-empty and the raw output is huge (OriginalBytes >
// maxLen*8), the full output is written to a file and the display becomes just
// the header + a "saved to" hint — the body does not enter the context at all.
// Otherwise the display is header + "Output:" guide + TruncateMiddle result.
func TruncateToolResult(content string, maxLen int, writeDir string, header OutputHeader) (display string, meta TruncationMeta) {
	if maxLen <= 0 {
		maxLen = MaxTruncationLength
	}
	meta.OriginalBytes = len(content)

	if len(content) <= maxLen {
		// No truncation needed, but still keep a small header if provided? No:
		// content fits, return it verbatim — headers are for truncated output
		// only, keeping small results clean for the model.
		return content, meta
	}

	// Decide whether to spill to disk. Only when a writeDir is configured and
	// the output is far over budget (8x) is the file path preferred over
	// keeping the middle.
	if writeDir != "" && len(content) > maxLen*8 {
		if path, ok := writeToolResult(writeDir, content); ok {
			h := header
			h.SavedPath = path
			// Body budget is given to the header; the saved-path line must
			// survive even in a tiny budget, so render header first then trim.
			hd := BuildOutputHeader(h)
			hint := "\nFull output saved to: " + path + " — use read_file/grep to view.\n"
			full := hd + hint
			if len(full) <= maxLen {
				meta.Truncated = true
				meta.SavedFilePath = path
				return full, meta
			}
			// Budget too small for the whole hint: keep the path, drop the rest
			// of the header (path line has highest priority).
			short := buildSavedPathOnly(path, maxLen)
			meta.Truncated = true
			meta.SavedFilePath = path
			return short, meta
		}
		// Write failed — fall through to in-context middle truncation.
	}

	// In-context truncation: header reserves bytes, body gets the rest.
	// The body always keeps at least maxLen/4 (R-1): the header allowance is
	// capped at maxLen/4, so the body wins on tiny budgets.
	allowance := headerAllowance(maxLen)

	// First pass: size the header without the omitted-count line.
	hd := BuildOutputHeader(header)
	if len(hd) > allowance {
		hd = trimHeaderToBudget(hd, allowance)
	}
	headerLen := len(hd)
	bodyBudget := maxLen - headerLen

	// Second pass: run the middle truncation, then fold the real omitted count
	// into the header. The extra line may push the header over headerLen; the
	// final header is then trimmed so the total stays within maxLen — but the
	// "Output:" guide line is always preserved (only the lines above it yield).
	body, omitted := TruncateMiddle(content, bodyBudget)
	h := header
	h.OmittedCharCount = omitted
	hdFinal := BuildOutputHeader(h)
	if len(hdFinal) > headerLen {
		hdFinal = trimHeaderPreserveGuide(hdFinal, headerLen)
	}
	display = hdFinal + body
	if len(display) > maxLen {
		// Last-resort hard cap, still UTF-8 safe.
		display = truncateUTF8Head(display, maxLen)
	}
	meta.Truncated = true
	return display, meta
}

// trimHeaderPreserveGuide trims a rendered header to at most n bytes while
// keeping the trailing "Output:\n" guide line intact: only the lines above the
// guide are cut. Guarantees a result of exactly n bytes when the guide fits.
func trimHeaderPreserveGuide(h string, n int) string {
	const guide = "Output:\n"
	if len(h) <= n {
		return h
	}
	if !strings.HasSuffix(h, guide) {
		return trimHeaderToBudget(h, n)
	}
	prefix := h[:len(h)-len(guide)]
	if len(prefix)+len(guide) <= n {
		return prefix + guide
	}
	keep := n - len(guide)
	if keep < 0 {
		return trimHeaderToBudget(h, n)
	}
	return trimHeaderToBudget(prefix, keep) + guide
}

// writeToolResult writes content to a fresh file under writeDir and returns
// its absolute path.
func writeToolResult(writeDir, content string) (string, bool) {
	if err := os.MkdirAll(writeDir, 0750); err != nil {
		return "", false
	}
	name := fmt.Sprintf("tool_%d_%d.txt", time.Now().UnixNano(), len(content)%10000)
	path := filepath.Join(writeDir, name)
	if err := os.WriteFile(path, []byte(content), 0640); err != nil {
		return "", false
	}
	return path, true
}

// buildSavedPathOnly returns a display that only carries the saved-path hint
// (header body dropped) fitting within maxLen bytes, UTF-8 safe.
func buildSavedPathOnly(path string, maxLen int) string {
	base := "\nFull output saved to: " + path + " — use read_file/grep to view.\n"
	if len(base) <= maxLen {
		return base
	}
	// Extremely tiny budget: keep "Full output saved to: " + as much of the
	// path as fits.
	prefix := "\nFull output saved to: "
	keep := maxLen - len(prefix)
	if keep < 1 {
		return truncateUTF8Head(base, maxLen)
	}
	return prefix + truncateUTF8Head(path, keep)
}

// trimHeaderToBudget cuts header text to at most n bytes on a rune boundary.
func trimHeaderToBudget(h string, n int) string {
	if n >= len(h) {
		return h
	}
	return truncateUTF8Head(h, n)
}
