// Package transcript implements the append-only JSONL session tree described
// in docs/design/general-agent-framework.md §3.
//
// A session file starts with a Header line followed by Entry lines. Entries
// form a tree through EntryBase.ParentID; the active branch is the path from
// the current leaf back to the root. Files are append-only: branching only
// moves the in-memory leaf pointer, and compaction appends a new entry instead
// of deleting history.
package transcript

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// EntryType is the discriminant of the entry union.
type EntryType string

const (
	EntrySession       EntryType = "session"
	EntryMessage       EntryType = "message"
	EntryModelChange   EntryType = "model_change"
	EntryThinking      EntryType = "thinking_level_change"
	EntryUsage         EntryType = "usage"
	EntryCompaction    EntryType = "compaction"
	EntryBranchSummary EntryType = "branch_summary"
	EntryCustom        EntryType = "custom"
	EntryCustomMessage EntryType = "custom_message"
	EntryLabel         EntryType = "label"
	EntrySessionInfo   EntryType = "session_info"
)

// ContentBlock is the content block interface used by custom messages. It is
// an alias for types.MessagePart so existing tool/UI code can consume it
// without conversion (design §3.2, transitional adapter).
type ContentBlock = types.MessagePart

// Header is the first line of a session file. It is metadata and is not part
// of the entry tree.
type Header struct {
	Type          string    `json:"type"` // "session"
	Version       int       `json:"version"`
	ID            string    `json:"id"`
	Timestamp     time.Time `json:"timestamp"`
	CWD           string    `json:"cwd,omitempty"`
	ParentSession string    `json:"parentSession,omitempty"`
}

// EntryBase is embedded by every entry. ParentID is nil for a root entry.
type EntryBase struct {
	Type      EntryType `json:"type"`
	ID        string    `json:"id"`
	ParentID  *string   `json:"parentId"`
	Timestamp time.Time `json:"timestamp"`
}

// Base returns the embedded base so concrete entries share Entry.
func (b *EntryBase) Base() *EntryBase { return b }

// Entry is the entry discriminated union. Only pointers implement it.
type Entry interface {
	Base() *EntryBase
	EntryType() EntryType
}

// SystemMessage is the complete prompt/tool checkpoint stored on a compaction
// entry. It becomes the leading system message when rebuilding context.
type SystemMessage struct {
	Role    string          `json:"role,omitempty"`
	Content string          `json:"content"`
	Tools   json.RawMessage `json:"tools,omitempty"`
}

// MessageEntry stores a conversation message.
type MessageEntry struct {
	EntryBase
	Message types.Message `json:"message"`
}

func (e *MessageEntry) EntryType() EntryType { return EntryMessage }

// ModelChangeEntry records a provider/model switch.
type ModelChangeEntry struct {
	EntryBase
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}

func (e *ModelChangeEntry) EntryType() EntryType { return EntryModelChange }

// ThinkingLevelChangeEntry records a reasoning/thinking level switch.
type ThinkingLevelChangeEntry struct {
	EntryBase
	ThinkingLevel string `json:"thinkingLevel"`
}

func (e *ThinkingLevelChangeEntry) EntryType() EntryType { return EntryThinking }

// UsageEntry records model-attributed usage that is not an assistant message.
// It never participates in model context.
type UsageEntry struct {
	EntryBase
	Kind     string      `json:"kind"`
	Provider string      `json:"provider"`
	Model    string      `json:"model"`
	Usage    types.Usage `json:"usage"`
}

func (e *UsageEntry) EntryType() EntryType { return EntryUsage }

// CompactionEntry replaces the context before FirstKeptEntryID with Summary.
// The original entries remain in the file.
type CompactionEntry struct {
	EntryBase
	Summary          string          `json:"summary"`
	FirstKeptEntryID string          `json:"firstKeptEntryId"`
	TokensBefore     int             `json:"tokensBefore"`
	SystemCheckpoint *SystemMessage  `json:"systemMessage,omitempty"`
	Usage            *types.Usage    `json:"usage,omitempty"`
	Details          json.RawMessage `json:"details,omitempty"`
}

func (e *CompactionEntry) EntryType() EntryType { return EntryCompaction }

// BranchSummaryEntry captures context from an abandoned branch. It becomes a
// system message in the rebuilt context.
type BranchSummaryEntry struct {
	EntryBase
	Summary string          `json:"summary"`
	FromID  string          `json:"fromId"`
	Usage   *types.Usage    `json:"usage,omitempty"`
	Details json.RawMessage `json:"details,omitempty"`
}

func (e *BranchSummaryEntry) EntryType() EntryType { return EntryBranchSummary }

// CustomEntry stores extension state. It never participates in model context.
type CustomEntry struct {
	EntryBase
	CustomType string          `json:"customType"`
	Data       json.RawMessage `json:"data,omitempty"`
}

func (e *CustomEntry) EntryType() EntryType { return EntryCustom }

// CustomMessageEntry injects an extension message into model context.
type CustomMessageEntry struct {
	EntryBase
	CustomType string         `json:"customType"`
	Content    []ContentBlock `json:"content"`
	Display    bool           `json:"display"`
}

func (e *CustomMessageEntry) EntryType() EntryType { return EntryCustomMessage }

// LabelEntry is a user-defined bookmark on another entry.
type LabelEntry struct {
	EntryBase
	TargetID string `json:"targetId"`
	Label    string `json:"label,omitempty"`
}

func (e *LabelEntry) EntryType() EntryType { return EntryLabel }

// SessionInfoEntry carries session metadata such as a display name.
type SessionInfoEntry struct {
	EntryBase
	Name string `json:"name,omitempty"`
}

func (e *SessionInfoEntry) EntryType() EntryType { return EntrySessionInfo }

// UnknownEntryTypeError is returned when a line carries an entry type this
// version does not know about. It is reported as a line diagnostic rather than
// being silently dropped.
type UnknownEntryTypeError struct {
	Type EntryType
}

func (e *UnknownEntryTypeError) Error() string {
	if e.Type == "" {
		return "transcript: entry is missing its type"
	}
	return fmt.Sprintf("transcript: unknown entry type %q", e.Type)
}

// MessageEntry and CustomMessageEntry need custom JSON handling because
// types.MessagePart is an interface. The wire format tags each part with a
// "type" discriminant (matching types.SerializeMessageParts).

func (e *MessageEntry) MarshalJSON() ([]byte, error) {
	msg, err := marshalMessage(e.Message)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		EntryBase
		Message json.RawMessage `json:"message"`
	}{EntryBase: e.EntryBase, Message: msg})
}

func (e *MessageEntry) UnmarshalJSON(data []byte) error {
	var aux struct {
		EntryBase
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	msg, err := unmarshalMessage(aux.Message)
	if err != nil {
		return err
	}
	e.EntryBase = aux.EntryBase
	e.Message = msg
	return nil
}

func (e *CustomMessageEntry) MarshalJSON() ([]byte, error) {
	content, err := marshalContentBlocks(e.Content)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		EntryBase
		CustomType string          `json:"customType"`
		Content    json.RawMessage `json:"content"`
		Display    bool            `json:"display"`
	}{
		EntryBase:  e.EntryBase,
		CustomType: e.CustomType,
		Content:    content,
		Display:    e.Display,
	})
}

func (e *CustomMessageEntry) UnmarshalJSON(data []byte) error {
	var aux struct {
		EntryBase
		CustomType string          `json:"customType"`
		Content    json.RawMessage `json:"content"`
		Display    bool            `json:"display"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	content, err := unmarshalContentBlocks(aux.Content)
	if err != nil {
		return err
	}
	e.EntryBase = aux.EntryBase
	e.CustomType = aux.CustomType
	e.Content = content
	e.Display = aux.Display
	return nil
}

// UnmarshalEntry decodes one entry using an envelope for the discriminant and
// a typed unmarshal for the payload.
func UnmarshalEntry(data []byte) (Entry, error) {
	var env struct {
		Type EntryType `json:"type"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode entry envelope: %w", err)
	}

	var entry Entry
	switch env.Type {
	case EntryMessage:
		v := &MessageEntry{}
		if err := json.Unmarshal(data, v); err != nil {
			return nil, fmt.Errorf("decode message entry: %w", err)
		}
		entry = v
	case EntryModelChange:
		v := &ModelChangeEntry{}
		if err := json.Unmarshal(data, v); err != nil {
			return nil, fmt.Errorf("decode model_change entry: %w", err)
		}
		entry = v
	case EntryThinking:
		v := &ThinkingLevelChangeEntry{}
		if err := json.Unmarshal(data, v); err != nil {
			return nil, fmt.Errorf("decode thinking_level_change entry: %w", err)
		}
		entry = v
	case EntryUsage:
		v := &UsageEntry{}
		if err := json.Unmarshal(data, v); err != nil {
			return nil, fmt.Errorf("decode usage entry: %w", err)
		}
		entry = v
	case EntryCompaction:
		v := &CompactionEntry{}
		if err := json.Unmarshal(data, v); err != nil {
			return nil, fmt.Errorf("decode compaction entry: %w", err)
		}
		entry = v
	case EntryBranchSummary:
		v := &BranchSummaryEntry{}
		if err := json.Unmarshal(data, v); err != nil {
			return nil, fmt.Errorf("decode branch_summary entry: %w", err)
		}
		entry = v
	case EntryCustom:
		v := &CustomEntry{}
		if err := json.Unmarshal(data, v); err != nil {
			return nil, fmt.Errorf("decode custom entry: %w", err)
		}
		entry = v
	case EntryCustomMessage:
		v := &CustomMessageEntry{}
		if err := json.Unmarshal(data, v); err != nil {
			return nil, fmt.Errorf("decode custom_message entry: %w", err)
		}
		entry = v
	case EntryLabel:
		v := &LabelEntry{}
		if err := json.Unmarshal(data, v); err != nil {
			return nil, fmt.Errorf("decode label entry: %w", err)
		}
		entry = v
	case EntrySessionInfo:
		v := &SessionInfoEntry{}
		if err := json.Unmarshal(data, v); err != nil {
			return nil, fmt.Errorf("decode session_info entry: %w", err)
		}
		entry = v
	default:
		return nil, &UnknownEntryTypeError{Type: env.Type}
	}
	return finishEntry(entry)
}

// MarshalEntry encodes one entry, including its type discriminator.
func MarshalEntry(e Entry) ([]byte, error) {
	if e == nil {
		return nil, fmt.Errorf("transcript: cannot marshal a nil entry")
	}
	if e.Base() == nil {
		return nil, fmt.Errorf("transcript: entry has no base")
	}
	e.Base().Type = e.EntryType()
	return json.Marshal(e)
}

func finishEntry(e Entry) (Entry, error) {
	base := e.Base()
	if base == nil {
		return nil, fmt.Errorf("transcript: entry has no base")
	}
	if base.ID == "" {
		return nil, fmt.Errorf("transcript: entry id is required")
	}
	if base.ParentID != nil && *base.ParentID == "" {
		return nil, fmt.Errorf("transcript: parentId must be null or a non-empty id")
	}
	base.Type = e.EntryType()
	return e, nil
}
