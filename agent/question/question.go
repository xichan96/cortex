// Package question defines the structured interactive-question contract shared
// by the question tool, the agent engine and the session layer.
//
// It is deliberately a leaf package (stdlib only) so that all three layers can
// depend on it without import cycles. The wire format matches the Dojo /
// Claude Code / Codex convention: questions carry a stable snake_case id, a
// short header, the question text, 2-4 options and multi-select / secret
// flags. Free-text answers are conventionally prefixed with "user_note:".
package question

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	// MaxQuestions is the maximum number of questions in one tool call.
	// Hosts may lower it; the tool validates against the configured limit.
	MaxQuestions = 3
	// MinOptions/MaxOptions bound the option list when one is provided.
	MinOptions = 2
	MaxOptions = 4
	// MaxHeaderRunes bounds the short header (Claude Code: 12 characters).
	MaxHeaderRunes = 12
	// LegacyQuestionID is the id assigned to a legacy `{question: "..."}`
	// input converted into a single free-text question.
	LegacyQuestionID = "question"
	// UserNotePrefix marks a free-text answer inside an answers list, matching
	// the Codex request_user_input convention.
	UserNotePrefix = "user_note:"
)

// Option is one selectable answer.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	// Preview is optional rich content shown when the option is focused
	// (single-select questions only by convention).
	Preview string `json:"preview,omitempty"`
}

// Spec is one question shown to the user.
type Spec struct {
	// ID is a stable snake_case identifier used as the answers map key.
	ID string `json:"id"`
	// Header is a short label (<= question.MaxHeaderRunes runes).
	Header string `json:"header"`
	// Question is the single-sentence question text.
	Question string `json:"question"`
	// Options is optional; empty means free text. When present it must hold
	// 2-4 entries. Clients append their own "Other" entry; agents must not.
	Options []Option `json:"options,omitempty"`
	// MultiSelect allows several options (and free text) per question.
	MultiSelect bool `json:"multi_select,omitempty"`
	// IsSecret asks the client to mask the answer.
	IsSecret bool `json:"is_secret,omitempty"`
}

// UnmarshalJSON accepts both the canonical snake_case keys (multi_select /
// is_secret) used on the wire and the camelCase spellings (multiSelect /
// isSecret) used in the tool schema, so model input and host JSON round-trip
// through the same type.
func (s *Spec) UnmarshalJSON(data []byte) error {
	type alias struct {
		ID               string   `json:"id"`
		Header           string   `json:"header"`
		Question         string   `json:"question"`
		Options          []Option `json:"options"`
		MultiSelect      *bool    `json:"multi_select"`
		MultiSelectCamel *bool    `json:"multiSelect"`
		IsSecret         *bool    `json:"is_secret"`
		IsSecretCamel    *bool    `json:"isSecret"`
	}
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	s.ID = strings.TrimSpace(a.ID)
	s.Header = strings.TrimSpace(a.Header)
	s.Question = strings.TrimSpace(a.Question)
	s.Options = a.Options
	s.MultiSelect = a.MultiSelect != nil && *a.MultiSelect
	if a.MultiSelectCamel != nil {
		s.MultiSelect = *a.MultiSelectCamel
	}
	s.IsSecret = a.IsSecret != nil && *a.IsSecret
	if a.IsSecretCamel != nil {
		s.IsSecret = *a.IsSecretCamel
	}
	return nil
}

// Answers maps a question id to the user's selected answers. A free-text
// answer is stored as a normal entry prefixed with UserNotePrefix, or under
// the well-known "user_note" key, so hosts can choose either representation.
type Answers map[string][]string

// Request is one blocking question round presented to a host.
type Request struct {
	// ID is the request id. Empty lets the asker assign a unique one.
	ID string
	// SessionID is the session that asked (informational).
	SessionID string
	// ToolCallID is the model's tool-call id, used to tie the question to the
	// tool part in the transcript.
	ToolCallID string
	// Questions are the validated questions (1..MaxQuestions).
	Questions []Spec
}

// Asker is implemented by hosts (dino session, bridges) that can present a
// question and wait for the answer inside the current turn. Ask must honor ctx
// cancellation and must never block forever without a timeout/cancel path.
type Asker interface {
	Ask(ctx context.Context, req Request) (Answers, error)
}

// AskerFunc adapts a function to Asker.
type AskerFunc func(ctx context.Context, req Request) (Answers, error)

// Ask implements Asker.
func (f AskerFunc) Ask(ctx context.Context, req Request) (Answers, error) {
	return f(ctx, req)
}

// Errors callers can match with errors.Is.
var (
	// ErrQuestionUnavailable means no host can present the question.
	ErrQuestionUnavailable = errors.New("question: no interactive asker available")
	// ErrQuestionTimeout means nobody answered before the deadline.
	ErrQuestionTimeout = errors.New("question: timed out waiting for an answer")
	// ErrQuestionCancelled means the turn/session was cancelled while waiting.
	ErrQuestionCancelled = errors.New("question: cancelled while waiting for an answer")
	// ErrUnknownQuestion means the request id is unknown or already answered.
	ErrUnknownQuestion = errors.New("question: unknown or already answered request")
	// ErrDuplicateQuestion means the request id is already pending.
	ErrDuplicateQuestion = errors.New("question: duplicate request id")
)

// Context plumbing. The session injects an Asker for the duration of a turn;
// the engine injects the tool-call id around each individual tool execution.

type askerKey struct{}

type toolCallIDKey struct{}

// WithAsker returns a context that exposes a to tools executed within it.
// A nil asker is ignored so callers can inject unconditionally.
func WithAsker(ctx context.Context, a Asker) context.Context {
	if ctx == nil || a == nil {
		return ctx
	}
	return context.WithValue(ctx, askerKey{}, a)
}

// AskerFromContext returns the Asker installed on ctx, if any.
func AskerFromContext(ctx context.Context) (Asker, bool) {
	if ctx == nil {
		return nil, false
	}
	a, ok := ctx.Value(askerKey{}).(Asker)
	return a, ok && a != nil
}

// WithToolCallID records the current tool-call id on ctx.
func WithToolCallID(ctx context.Context, id string) context.Context {
	if ctx == nil || id == "" {
		return ctx
	}
	return context.WithValue(ctx, toolCallIDKey{}, id)
}

// ToolCallIDFromContext returns the current tool-call id, or "".
func ToolCallIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}

// FirstText returns the first question's text, or "".
func FirstText(specs []Spec) string {
	for _, q := range specs {
		if strings.TrimSpace(q.Question) != "" {
			return q.Question
		}
	}
	return ""
}

// NormalizeAnswers trims blank entries and returns a copy. A nil map stays
// nil; an all-blank map becomes an empty (non-nil) map.
func NormalizeAnswers(in Answers) Answers {
	if in == nil {
		return nil
	}
	out := make(Answers, len(in))
	for id, values := range in {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		var kept []string
		for _, v := range values {
			if v = strings.TrimSpace(v); v != "" {
				kept = append(kept, v)
			}
		}
		out[id] = kept
	}
	return out
}

// FormatAnswers renders answers as a stable, model-readable text block. Used
// by the legacy new-turn fallback and by hosts that persist answers as text.
// Missing questions are reported so the model does not assume an answer.
func FormatAnswers(specs []Spec, answers Answers) string {
	var b strings.Builder
	for _, q := range specs {
		b.WriteString("- ")
		if q.ID != "" {
			b.WriteString(q.ID)
			b.WriteString(": ")
		}
		vals := answers[q.ID]
		if len(vals) == 0 {
			b.WriteString("(not answered)")
		} else {
			b.WriteString(strings.Join(vals, ", "))
		}
		b.WriteByte('\n')
	}
	// Answers for ids the model did not declare (host free-form additions)
	// are appended so nothing is silently dropped.
	for id, vals := range answers {
		found := false
		for _, q := range specs {
			if q.ID == id {
				found = true
				break
			}
		}
		if found {
			continue
		}
		b.WriteString("- ")
		b.WriteString(id)
		b.WriteString(": ")
		b.WriteString(strings.Join(vals, ", "))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// ContextError maps a turn-context error to the question error vocabulary.
func ContextError(id string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s: %w", ErrQuestionTimeout, id, err)
	}
	return fmt.Errorf("%w: %s: %w", ErrQuestionCancelled, id, err)
}
