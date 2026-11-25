package runtime

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/xichan96/cortex/agent/question"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

// SentinelQuestionResult is the structured payload QuestionTool.Execute returns
// when no interactive asker is installed for the turn. Instead of hard-erroring
// it reports that the question must be surfaced to the user and that the agent
// loop should not continue waiting on a result it cannot produce. dino's runner
// detects this shape and emits an EventTypeQuestion; the answer arrives as a
// fresh user input (legacy halt-and-reflow mode).
//
// AskUser is the sentinel marker. Question keeps the first question's text for
// old consumers; Questions carries the full structured payload.
type SentinelQuestionResult struct {
	Ok        bool            `json:"ok"`
	AskUser   bool            `json:"ask_user"`
	Question  string          `json:"question,omitempty"`
	Questions []question.Spec `json:"questions,omitempty"`
}

// AnsweredQuestionResult is returned when an asker is installed on the turn
// context: the tool blocked until the user answered and the answers are handed
// back to the model as the tool result, so the same turn continues. It is not
// a sentinel (AskUser/ask_user is absent), which is how the engine knows not to
// halt the iteration.
type AnsweredQuestionResult struct {
	Ok        bool             `json:"ok"`
	Answered  bool             `json:"answered"`
	Answers   question.Answers `json:"answers"`
	Questions []question.Spec  `json:"questions,omitempty"`
}

type QuestionTool struct {
	// MaxQuestions bounds the number of questions per call (default 3).
	MaxQuestions int
}

func NewQuestionTool() types.Tool {
	return &QuestionTool{MaxQuestions: question.MaxQuestions}
}

func (t *QuestionTool) Name() string {
	return "question"
}

//go:embed question.txt
var questionDescription string

func (t *QuestionTool) Description() string {
	return questionDescription
}

func (t *QuestionTool) Schema() map[string]interface{} {
	optionSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"label":       map[string]interface{}{"type": "string", "description": "Short option label."},
			"description": map[string]interface{}{"type": "string", "description": "One sentence explaining the option."},
			"preview":     map[string]interface{}{"type": "string", "description": "Optional preview content (e.g. code or a diff) shown for this option."},
		},
		"required": []string{"label"},
	}
	questionSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id": map[string]interface{}{
				"type":        "string",
				"description": "Stable snake_case identifier, used as the key in the answers map (e.g. \"scope\").",
			},
			"header": map[string]interface{}{
				"type":        "string",
				"description": "Short label shown above the question (at most 12 characters).",
			},
			"question": map[string]interface{}{
				"type":        "string",
				"description": "The question to ask the user (one sentence).",
			},
			"options": map[string]interface{}{
				"type":        "array",
				"description": "Optional 2-4 mutually exclusive options. Omit for free-text answers. Put the recommended option first and append \" (Recommended)\" to its label. Do not add an \"Other\" option; clients append it automatically.",
				"items":       optionSchema,
			},
			"multiSelect": map[string]interface{}{
				"type":        "boolean",
				"description": "Allow selecting several options (and free text).",
			},
			"isSecret": map[string]interface{}{
				"type":        "boolean",
				"description": "Ask the client to mask the answer (secrets, tokens, credentials).",
			},
		},
		"required": []string{"id", "header", "question"},
	}
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"questions": map[string]interface{}{
				"type":        "array",
				"description": "1-3 structured questions. Prefer this over the legacy `question` field.",
				"items":       questionSchema,
			},
			"question": map[string]interface{}{
				"type":        "string",
				"description": "Legacy single free-text question. Converted to one question with id \"question\". Use `questions` for new calls.",
			},
		},
	}
}

// Execute turns the model's input into validated questions, then either blocks
// on the turn's asker (same-turn mode) or returns the halt sentinel (legacy).
func (t *QuestionTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	specs, err := ParseQuestions(input, t.maxQuestions())
	if err != nil {
		return nil, err
	}

	if asker, ok := question.AskerFromContext(ctx); ok {
		answers, askErr := asker.Ask(ctx, question.Request{
			SessionID:  "",
			ToolCallID: question.ToolCallIDFromContext(ctx),
			Questions:  specs,
		})
		if askErr != nil {
			// Surface the transport failure to the model: it must not pretend
			// it received an answer. The engine keeps the turn alive (no
			// sentinel) so the model can ask again or proceed differently.
			return nil, askErr
		}
		normalized := question.NormalizeAnswers(answers)
		if normalized == nil {
			// Keep the tool result shape stable even when the host answers
			// with nil (e.g. the user skipped every question).
			normalized = question.Answers{}
		}
		return AnsweredQuestionResult{
			Ok:        true,
			Answered:  true,
			Answers:   normalized,
			Questions: specs,
		}, nil
	}

	return SentinelQuestionResult{
		Ok:        true,
		AskUser:   true,
		Question:  question.FirstText(specs),
		Questions: specs,
	}, nil
}

func (t *QuestionTool) maxQuestions() int {
	if t == nil || t.MaxQuestions <= 0 {
		return question.MaxQuestions
	}
	return t.MaxQuestions
}

func (t *QuestionTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "question",
		IsFromToolkit:  false,
		ToolType:       "runtime",
	}
}

// ParseQuestions normalizes and validates a question tool input. It accepts
// the structured `questions` array or the legacy single `question` string
// (converted to one free-text question with id "question").
//
// Validation errors are EC_PARAMETER_* so hosts can distinguish a bad tool call
// from a transport failure.
func ParseQuestions(input map[string]interface{}, maxQuestions int) ([]question.Spec, error) {
	if maxQuestions <= 0 {
		maxQuestions = question.MaxQuestions
	}
	if input == nil {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("questions is required"))
	}

	if raw, ok := input["questions"]; ok && raw != nil {
		specs, err := decodeQuestions(raw)
		if err != nil {
			return nil, err
		}
		if len(specs) > 0 {
			if err := ValidateQuestions(specs, maxQuestions); err != nil {
				return nil, err
			}
			return specs, nil
		}
	}

	if raw, ok := input["question"]; ok && raw != nil {
		text, isString := raw.(string)
		if !isString {
			return nil, errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("question must be a string"))
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("question is required"))
		}
		return []question.Spec{{
			ID:       question.LegacyQuestionID,
			Header:   "Question",
			Question: text,
		}}, nil
	}

	return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("questions is required: provide 1-%d structured questions or a legacy question string", maxQuestions))
}

// decodeQuestions accepts any slice/map shape the model or a host may provide
// (including []question.Spec in tests) by round-tripping through JSON.
func decodeQuestions(raw interface{}) ([]question.Spec, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("questions must be an array of question objects: %w", err))
	}
	var specs []question.Spec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("questions must be an array of question objects: %w", err))
	}
	return specs, nil
}

var questionIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidateQuestions enforces the structured-question contract. Messages name
// the offending question/option so the model can fix the call in one retry.
func ValidateQuestions(specs []question.Spec, maxQuestions int) error {
	if maxQuestions <= 0 {
		maxQuestions = question.MaxQuestions
	}
	if len(specs) == 0 {
		return errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("questions must contain at least 1 question"))
	}
	if len(specs) > maxQuestions {
		return errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("too many questions: got %d, max %d", len(specs), maxQuestions))
	}
	seenIDs := make(map[string]int, len(specs))
	for i := range specs {
		spec := &specs[i]
		where := fmt.Sprintf("questions[%d]", i)
		if spec.ID == "" {
			return errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("%s.id is required", where))
		}
		if !questionIDPattern.MatchString(spec.ID) {
			return errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("%s.id %q must be snake_case (lowercase letters, digits, underscores)", where, spec.ID))
		}
		if prev, dup := seenIDs[spec.ID]; dup {
			return errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("%s.id %q duplicates questions[%d].id", where, spec.ID, prev))
		}
		seenIDs[spec.ID] = i
		if spec.Header == "" {
			return errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("%s.header is required", where))
		}
		if n := utf8.RuneCountInString(spec.Header); n > question.MaxHeaderRunes {
			return errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("%s.header is %d characters, max %d", where, n, question.MaxHeaderRunes))
		}
		if spec.Question == "" {
			return errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("%s.question is required", where))
		}
		if err := validateOptions(where, spec.Options); err != nil {
			return err
		}
	}
	return nil
}

func validateOptions(where string, options []question.Option) error {
	if len(options) == 0 {
		return nil
	}
	if len(options) < question.MinOptions || len(options) > question.MaxOptions {
		return errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("%s.options must have %d-%d entries (omit options for free text), got %d", where, question.MinOptions, question.MaxOptions, len(options)))
	}
	seenLabels := make(map[string]int, len(options))
	for j, opt := range options {
		label := strings.TrimSpace(opt.Label)
		if label == "" {
			return errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("%s.options[%d].label is required", where, j))
		}
		if prev, dup := seenLabels[label]; dup {
			return errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("%s.options[%d].label %q duplicates options[%d].label", where, j, label, prev))
		}
		seenLabels[label] = j
		options[j].Label = label
	}
	return nil
}

// SentinelFromOutput recognizes a question sentinel in a tool result. It
// handles the named struct (direct tool execution), a pointer, the JSON map
// form after a round-trip, and a JSON string. Returns nil when output is not a
// sentinel or AskUser is false (e.g. the blocking-mode answered result).
func SentinelFromOutput(output interface{}) *SentinelQuestionResult {
	var sentinel SentinelQuestionResult
	switch v := output.(type) {
	case nil:
		return nil
	case SentinelQuestionResult:
		sentinel = v
	case *SentinelQuestionResult:
		if v == nil {
			return nil
		}
		sentinel = *v
	case map[string]interface{}:
		if !truthy(v["ask_user"]) {
			return nil
		}
		data, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		if err := json.Unmarshal(data, &sentinel); err != nil {
			return nil
		}
	case string:
		if !strings.Contains(v, `"ask_user"`) {
			return nil
		}
		if err := json.Unmarshal([]byte(v), &sentinel); err != nil {
			return nil
		}
	case []byte:
		if err := json.Unmarshal(v, &sentinel); err != nil {
			return nil
		}
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		if err := json.Unmarshal(data, &sentinel); err != nil {
			return nil
		}
	}
	if !sentinel.AskUser {
		return nil
	}
	if sentinel.Question == "" {
		sentinel.Question = question.FirstText(sentinel.Questions)
	}
	if sentinel.Question == "" && len(sentinel.Questions) == 0 {
		return nil
	}
	return &sentinel
}

func truthy(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	default:
		return false
	}
}
