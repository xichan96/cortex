package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xichan96/cortex/agent/types"
)

// CutPoint is the boundary between summarized history and retained context.
type CutPoint struct {
	// Index is the first kept entry. It equals len(path) for retain-none
	// compactions and -1 for an empty path.
	Index int `json:"index"`
	// EntryID is path[Index].Base().ID, or "" for retain-none/empty paths.
	EntryID string `json:"entryId,omitempty"`
	// TokensKept is the approximate token size of the retained suffix.
	TokensKept int `json:"tokensKept"`
	// IsSplitTurn reports that the cut falls inside a user turn (at an
	// assistant or custom message); the caller should summarize the turn
	// prefix separately.
	IsSplitTurn bool `json:"isSplitTurn"`
	// TurnStartIndex/ID point at the user message starting a split turn.
	TurnStartIndex   int    `json:"turnStartIndex,omitempty"`
	TurnStartEntryID string `json:"turnStartEntryId,omitempty"`
}

// FindCutPoint walks backwards over the active path, accumulating tokens, and
// returns the boundary that keeps approximately keepRecentTokens. Cut points
// are user/assistant messages and custom messages; tool results are never cut
// points, so a tool call and its result always stay together.
func FindCutPoint(path []Entry, keepRecentTokens int) CutPoint {
	if len(path) == 0 {
		return CutPoint{Index: -1}
	}
	if keepRecentTokens <= 0 {
		return CutPoint{Index: len(path)}
	}

	candidates := make([]int, 0, len(path))
	for i, e := range path {
		if isCutPointEntry(e) {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		cut := CutPoint{Index: 0, EntryID: path[0].Base().ID}
		cut.TokensKept = tokensForRange(path, 0)
		return cut
	}

	cutIndex := candidates[0]
	accumulated := 0
	for i := len(path) - 1; i >= 0; i-- {
		tokens := tokensForEntry(path[i])
		if tokens == 0 {
			continue
		}
		accumulated += tokens
		if accumulated >= keepRecentTokens {
			// Prefer the closest valid cut point at or after this entry. This
			// keeps trailing tool results with their assistant tool call.
			chosen := candidates[len(candidates)-1]
			for _, c := range candidates {
				if c >= i {
					chosen = c
					break
				}
			}
			cutIndex = chosen
			break
		}
	}

	// Include adjacent metadata entries (usage, labels, model changes) in the
	// retained range without moving past context-visible entries.
	for cutIndex > 0 {
		prev := path[cutIndex-1]
		if _, isCompaction := prev.(*CompactionEntry); isCompaction {
			break
		}
		if len(entryContextMessages(prev, true)) > 0 {
			break
		}
		cutIndex--
	}

	cut := CutPoint{
		Index:      cutIndex,
		EntryID:    path[cutIndex].Base().ID,
		TokensKept: tokensForRange(path, cutIndex),
	}
	if !isTurnStartEntry(path[cutIndex]) {
		for i := cutIndex; i >= 0; i-- {
			if isTurnStartEntry(path[i]) {
				cut.IsSplitTurn = true
				cut.TurnStartIndex = i
				cut.TurnStartEntryID = path[i].Base().ID
				break
			}
		}
		if !cut.IsSplitTurn {
			cut.TurnStartIndex = -1
		}
	}
	return cut
}

// FindCutPoint computes a cut point for the current leaf.
func (l *Log) FindCutPoint(keepRecentTokens int) (CutPoint, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	path, err := l.pathToLocked("")
	if err != nil {
		return CutPoint{}, err
	}
	return FindCutPoint(path, keepRecentTokens), nil
}

// FindCutPointFor computes a cut point for a specific branch.
func (l *Log) FindCutPointFor(entryID string, keepRecentTokens int) (CutPoint, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	path, err := l.pathToLocked(entryID)
	if err != nil {
		return CutPoint{}, err
	}
	return FindCutPoint(path, keepRecentTokens), nil
}

func isCutPointEntry(e Entry) bool {
	switch v := e.(type) {
	case *MessageEntry:
		role := strings.ToLower(v.Message.Role)
		return role == "user" || role == "assistant"
	case *CustomMessageEntry:
		return true
	default:
		return false
	}
}

func isTurnStartEntry(e Entry) bool {
	switch v := e.(type) {
	case *MessageEntry:
		return strings.EqualFold(v.Message.Role, "user")
	case *CustomMessageEntry:
		return true
	default:
		return false
	}
}

func tokensForEntry(e Entry) int {
	total := 0
	for _, m := range entryContextMessages(e, true) {
		total += types.RoughTokensForMessage(m)
	}
	return total
}

func tokensForRange(path []Entry, start int) int {
	total := 0
	for i := start; i < len(path); i++ {
		total += tokensForEntry(path[i])
	}
	return total
}

// CompactionOptions configures BuildCompactionWithOptions.
type CompactionOptions struct {
	Summary          string
	FirstKeptEntryID string
	TokensBefore     int
	SystemCheckpoint *SystemMessage
	Usage            *types.Usage
	Details          json.RawMessage
}

// BuildCompaction builds a compaction entry. ID, parentId and timestamp are
// assigned by Log.Append; the summary text is supplied by the caller (this
// package never calls an LLM). For iterative summaries, feed the previous
// summary into FormatCompactionInput and pass the resulting text here.
//
// firstKeptID should be the entry id returned by FindCutPoint. A retain-none
// compaction passes an empty value; callers that want to reference the
// compaction itself can set FirstKeptEntryID to its id after Append.
func BuildCompaction(summary, firstKeptID string, tokensBefore int) *CompactionEntry {
	return BuildCompactionWithOptions(CompactionOptions{
		Summary:          summary,
		FirstKeptEntryID: firstKeptID,
		TokensBefore:     tokensBefore,
	})
}

// BuildCompactionWithOptions is BuildCompaction plus usage/details/checkpoint.
func BuildCompactionWithOptions(opts CompactionOptions) *CompactionEntry {
	return &CompactionEntry{
		EntryBase:        EntryBase{Type: EntryCompaction},
		Summary:          opts.Summary,
		FirstKeptEntryID: opts.FirstKeptEntryID,
		TokensBefore:     opts.TokensBefore,
		SystemCheckpoint: opts.SystemCheckpoint,
		Usage:            opts.Usage,
		Details:          opts.Details,
	}
}

const maxCompactionToolResultRunes = 2000

// FormatCompactionInput serializes messages for a summarizer. A non-empty
// previousSummary is prepended so repeated compactions can update the summary
// iteratively (design §3.5 step 5). Tool results are truncated and thinking is
// omitted, matching the compaction prompt contract.
func FormatCompactionInput(previousSummary string, messages []types.Message) string {
	var b strings.Builder
	if previousSummary != "" {
		b.WriteString("<previous_summary>\n")
		b.WriteString(previousSummary)
		b.WriteString("\n</previous_summary>\n\n")
	}
	for _, m := range messages {
		role := strings.ToLower(m.Role)
		switch role {
		case "user":
			writeBlock(&b, "[User]", messageText(m))
		case "assistant":
			text := messageText(m)
			if text != "" {
				writeBlock(&b, "[Assistant]", text)
			}
			if len(m.ToolCalls) > 0 {
				b.WriteString("[Assistant tool calls]\n")
				for _, call := range m.ToolCalls {
					args, err := json.Marshal(call.Function.Arguments)
					if err != nil {
						args = []byte("{}")
					}
					b.WriteString(call.Function.Name)
					b.WriteString("(")
					b.Write(args)
					b.WriteString(")\n")
				}
				b.WriteString("\n")
			}
		case "tool":
			text := messageText(m)
			text = truncateRunes(text, maxCompactionToolResultRunes)
			writeBlock(&b, "[Tool result]", text)
		case "system":
			writeBlock(&b, "[System]", messageText(m))
		default:
			text := messageText(m)
			if text != "" {
				writeBlock(&b, "["+m.Role+"]", text)
			}
		}
	}
	return b.String()
}

func writeBlock(b *strings.Builder, header, text string) {
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func messageText(m types.Message) string {
	if m.Content != "" {
		return m.Content
	}
	var parts []string
	for _, part := range m.Parts {
		if text, ok := part.(types.TextPart); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…[truncated]"
}

// String is a small helper for debugging cut points.
func (c CutPoint) String() string {
	return fmt.Sprintf("cut(index=%d id=%q tokens=%d splitTurn=%t)", c.Index, c.EntryID, c.TokensKept, c.IsSplitTurn)
}
