package transcript

import (
	"context"
	"fmt"
	"strings"

	"github.com/xichan96/cortex/agent/types"
)

// BuildOptions controls context rebuilding.
type BuildOptions struct {
	// MaxTokens trims the oldest messages until the approximate context fits.
	// The trim point never separates a tool call from its tool result.
	MaxTokens int
	// IncludeCustom injects custom_message entries. The zero value excludes
	// them; use DefaultBuildOptions for the usual behavior.
	IncludeCustom bool
	// SystemCheckpoint emits the compaction's system checkpoint as the leading
	// system message. The zero value omits it; use DefaultBuildOptions to keep
	// the full checkpoint.
	SystemCheckpoint bool
	// LeafID rebuilds from a specific entry instead of the current leaf.
	LeafID string
}

// DefaultBuildOptions returns the options that preserve full model context.
func DefaultBuildOptions() BuildOptions {
	return BuildOptions{IncludeCustom: true, SystemCheckpoint: true}
}

// ModelRef identifies the provider/model selected by the active branch.
type ModelRef struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

// BuildReport describes how the context was assembled.
type BuildReport struct {
	// Tokens is the approximate token count of the returned messages.
	Tokens int `json:"tokens"`
	// TokensBefore is the count before MaxTokens trimming.
	TokensBefore int `json:"tokensBefore"`
	// Cut is the number of leading messages removed by the token budget.
	Cut int `json:"cut"`
	// CutEntryID is the entry that owns the first returned message.
	CutEntryID string `json:"cutEntryId,omitempty"`
	// CompactionID is the latest compaction applied on the active path.
	CompactionID string `json:"compactionId,omitempty"`
	// Model and ThinkingLevel are the latest selections on the active path.
	Model         *ModelRef `json:"model,omitempty"`
	ThinkingLevel string    `json:"thinkingLevel,omitempty"`
	// EntryIDs lists the entries that contributed model-visible messages, in
	// context order.
	EntryIDs []string `json:"entryIds,omitempty"`
	// Diagnostics reports broken parents, missing firstKeptEntryId targets and
	// other recoverable problems encountered while rebuilding.
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

type contextItem struct {
	entryID  string
	messages []types.Message
}

// BuildContext rebuilds the model-visible messages for the active branch.
//
// It walks leaf→root, applies the latest compaction, converts entries to
// types.Message and optionally trims the oldest messages to MaxTokens. Tool
// calls and their tool results are never separated.
func (l *Log) BuildContext(ctx context.Context, opts BuildOptions) ([]types.Message, BuildReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, BuildReport{}, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	var report BuildReport
	leaf := opts.LeafID
	if leaf == "" {
		leaf = l.leafID
	}
	if leaf == "" {
		return nil, report, nil
	}

	path, diags := l.pathToDiagnostic(leaf)
	report.Diagnostics = append(report.Diagnostics, diags...)
	if len(path) == 0 {
		return nil, report, nil
	}

	report.Model, report.ThinkingLevel = pathSettings(path)

	compactionIdx := -1
	for i := len(path) - 1; i >= 0; i-- {
		if _, ok := path[i].(*CompactionEntry); ok {
			compactionIdx = i
			break
		}
	}

	var items []contextItem
	if compactionIdx < 0 {
		for _, e := range path {
			items = appendMessages(items, e, opts.IncludeCustom)
		}
	} else {
		cp := path[compactionIdx].(*CompactionEntry)
		report.CompactionID = cp.Base().ID
		if opts.SystemCheckpoint && cp.SystemCheckpoint != nil {
			role := cp.SystemCheckpoint.Role
			if role == "" {
				role = "system"
			}
			items = append(items, contextItem{
				entryID:  cp.Base().ID,
				messages: []types.Message{{Role: role, Content: cp.SystemCheckpoint.Content}},
			})
		}
		items = append(items, contextItem{
			entryID:  cp.Base().ID,
			messages: []types.Message{{Role: "system", Content: cp.Summary}},
		})

		// Entries retained before the compaction, starting at firstKeptEntryId.
		switch cp.FirstKeptEntryID {
		case "":
			report.Diagnostics = append(report.Diagnostics, Diagnostic{
				EntryID: cp.Base().ID,
				Message: "compaction has an empty firstKeptEntryId; retaining no pre-compaction entries",
			})
		case cp.Base().ID:
			// Retain-none compaction.
		default:
			found := false
			for i := 0; i < compactionIdx; i++ {
				e := path[i]
				if e.Base().ID == cp.FirstKeptEntryID {
					found = true
				}
				if !found {
					continue
				}
				// The checkpoint replaces pre-compaction system messages.
				if isSystemMessageEntry(e) {
					continue
				}
				// Older compactions in the retained range no longer contribute.
				if _, ok := e.(*CompactionEntry); ok {
					continue
				}
				items = appendMessages(items, e, opts.IncludeCustom)
			}
			if !found {
				report.Diagnostics = append(report.Diagnostics, Diagnostic{
					EntryID: cp.Base().ID,
					Message: fmt.Sprintf("firstKeptEntryId %q is not on the active path", cp.FirstKeptEntryID),
				})
			}
		}

		for i := compactionIdx + 1; i < len(path); i++ {
			e := path[i]
			if _, ok := e.(*CompactionEntry); ok {
				continue
			}
			items = appendMessages(items, e, opts.IncludeCustom)
		}
	}

	var (
		messages   []types.Message
		messageIDs []string
	)
	for _, item := range items {
		for _, m := range item.messages {
			messages = append(messages, m)
			messageIDs = append(messageIDs, item.entryID)
		}
	}

	for _, m := range messages {
		report.TokensBefore += types.RoughTokensForMessage(m)
	}
	report.Tokens = report.TokensBefore

	if opts.MaxTokens > 0 && report.Tokens > opts.MaxTokens {
		cut := findBudgetCut(messages, opts.MaxTokens)
		report.Cut = cut
		messages = messages[cut:]
		messageIDs = messageIDs[cut:]
		report.Tokens = 0
		for _, m := range messages {
			report.Tokens += types.RoughTokensForMessage(m)
		}
	}

	// A malformed compaction may start on a tool result whose assistant call
	// was summarized away. Never hand an orphan to the model.
	messages, messageIDs = dropOrphanToolResults(messages, messageIDs, &report.Diagnostics)
	report.Tokens = 0
	for _, m := range messages {
		report.Tokens += types.RoughTokensForMessage(m)
	}
	report.EntryIDs = uniqueEntryIDs(messageIDs)
	if len(messageIDs) > 0 {
		report.CutEntryID = messageIDs[0]
	}
	return messages, report, nil
}

func dropOrphanToolResults(messages []types.Message, entryIDs []string, diags *[]Diagnostic) ([]types.Message, []string) {
	available := make(map[string]bool)
	outMessages := make([]types.Message, 0, len(messages))
	outIDs := make([]string, 0, len(entryIDs))
	for i, m := range messages {
		if strings.EqualFold(m.Role, "tool") {
			if m.ToolCallID == "" || !available[m.ToolCallID] {
				if diags != nil {
					*diags = append(*diags, Diagnostic{
						EntryID: entryIDs[i],
						Message: fmt.Sprintf("orphaned tool result %q dropped from context", m.ToolCallID),
					})
				}
				continue
			}
		}
		if strings.EqualFold(m.Role, "assistant") {
			for _, call := range m.ToolCalls {
				available[call.ID] = true
			}
		}
		outMessages = append(outMessages, m)
		outIDs = append(outIDs, entryIDs[i])
	}
	return outMessages, outIDs
}

func uniqueEntryIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if len(out) == 0 || out[len(out)-1] != id {
			out = append(out, id)
		}
	}
	return out
}

func appendMessages(items []contextItem, e Entry, includeCustom bool) []contextItem {
	msgs := entryContextMessages(e, includeCustom)
	if len(msgs) == 0 {
		return items
	}
	return append(items, contextItem{entryID: e.Base().ID, messages: msgs})
}

// entryContextMessages converts one entry to model-visible messages. The
// result is empty for state-only entries.
func entryContextMessages(e Entry, includeCustom bool) []types.Message {
	switch v := e.(type) {
	case *MessageEntry:
		msg := v.Message
		return []types.Message{msg}
	case *CustomMessageEntry:
		if !includeCustom {
			return nil
		}
		return []types.Message{customMessageToMessage(v.Content)}
	case *BranchSummaryEntry:
		if v.Summary == "" {
			return nil
		}
		return []types.Message{{Role: "system", Content: v.Summary}}
	case *CompactionEntry:
		// Used for token estimation and by callers that treat compactions
		// generically; BuildContext handles the leading compaction separately.
		return []types.Message{{Role: "system", Content: v.Summary}}
	default:
		return nil
	}
}

func customMessageToMessage(content []ContentBlock) types.Message {
	switch len(content) {
	case 0:
		return types.Message{Role: "user"}
	case 1:
		if text, ok := content[0].(types.TextPart); ok {
			return types.Message{Role: "user", Content: text.Text}
		}
	}
	return types.Message{Role: "user", Parts: content}
}

func isSystemMessageEntry(e Entry) bool {
	m, ok := e.(*MessageEntry)
	return ok && strings.EqualFold(m.Message.Role, "system")
}

func pathSettings(path []Entry) (*ModelRef, string) {
	var (
		model    *ModelRef
		fallback *ModelRef
		thinking string
	)
	for _, e := range path {
		switch v := e.(type) {
		case *ModelChangeEntry:
			model = &ModelRef{Provider: v.Provider, ModelID: v.ModelID}
		case *ThinkingLevelChangeEntry:
			thinking = v.ThinkingLevel
		case *UsageEntry:
			if v.Provider != "" || v.Model != "" {
				fallback = &ModelRef{Provider: v.Provider, ModelID: v.Model}
			}
		}
	}
	if model == nil {
		model = fallback
	}
	return model, thinking
}

// pathToDiagnostic walks leaf→root without failing on broken links.
func (l *Log) pathToDiagnostic(id string) ([]Entry, []Diagnostic) {
	var (
		reversed []Entry
		diags    []Diagnostic
	)
	seen := make(map[string]bool)
	for id != "" {
		if seen[id] {
			diags = append(diags, Diagnostic{EntryID: id, Message: fmt.Sprintf("parent cycle detected at entry %q", id)})
			break
		}
		seen[id] = true
		e, ok := l.byID[id]
		if !ok {
			diags = append(diags, Diagnostic{EntryID: id, Message: fmt.Sprintf("entry %q is missing", id)})
			break
		}
		reversed = append(reversed, e)
		if e.Base().ParentID == nil {
			break
		}
		id = *e.Base().ParentID
	}
	path := make([]Entry, len(reversed))
	for i := range reversed {
		path[len(reversed)-1-i] = reversed[i]
	}
	return path, diags
}

// findBudgetCut returns the smallest index whose suffix fits the budget while
// starting on a safe boundary. A tool result may never be the first kept
// message: that would orphan it from its assistant tool call.
func findBudgetCut(messages []types.Message, maxTokens int) int {
	n := len(messages)
	if n <= 1 {
		return 0
	}
	suffix := make([]int, n+1)
	for i := n - 1; i >= 0; i-- {
		suffix[i] = suffix[i+1] + types.RoughTokensForMessage(messages[i])
	}
	safe := func(i int) bool {
		if i <= 0 || i >= n {
			return false
		}
		return !strings.EqualFold(messages[i].Role, "tool")
	}
	for i := 1; i < n; i++ {
		if safe(i) && suffix[i] <= maxTokens {
			return i
		}
	}
	// Even the smallest safe suffix exceeds the budget. Trim as much as
	// possible without splitting a tool pair.
	for i := n - 1; i >= 1; i-- {
		if safe(i) {
			return i
		}
	}
	return 0
}
