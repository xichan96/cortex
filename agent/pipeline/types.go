// Package pipeline is the single path every tool invocation takes: model
// initiated, nested, or script initiated. It wires the stages
//
//	resolve -> prepare_args -> validate -> authorize -> before
//	        -> execute -> after -> sanitize -> record
//
// and is transport neutral. All ordinary failures become a Result so the model
// can see and recover from them; only errors implementing
// types.FatalToolErrorKind are returned to the caller to unwind the iteration.
package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// Call is one tool invocation travelling through the pipeline.
type Call struct {
	// ID is the toolCallID; every call in a batch must have a unique ID.
	ID string
	// Name is the tool name as requested by the model.
	Name string
	// RawArgs is the undecoded argument JSON, when the caller has it.
	RawArgs json.RawMessage
	// Args is the decoded argument object. When nil, prepare decodes RawArgs.
	Args map[string]any
	// Tool is the resolved tool. When nil, the registry is consulted.
	Tool types.Tool
	// SessionID / TurnID identify the conversation context.
	SessionID string
	TurnID    string
	// ParentID is the parent toolCallID for nested calls.
	ParentID string
	// StartedAt is when the call entered the pipeline. ExecuteOne fills it when
	// zero.
	StartedAt time.Time
	// Internal marks engine-internal calls (nested or script driven). Hidden
	// tools may only be resolved by internal calls.
	Internal bool
}

// Result is the outcome of a tool call.
type Result struct {
	// Content is what the model sees.
	Content []types.MessagePart
	// Details is unstructured metadata for UI/logs.
	Details any
	// Structured is for programmatic callers (outputSchema).
	Structured json.RawMessage
	// Usage is optional token accounting.
	Usage *types.Usage
	// IsError marks a recoverable tool error.
	IsError bool
	// Terminate asks the engine to stop the current loop.
	Terminate bool
	// Duration is the wall time spent executing.
	Duration time.Duration
}

// TextResult returns a successful text result.
func TextResult(text string) *Result {
	return &Result{Content: []types.MessagePart{types.TextPart{Text: text}}}
}

// ErrorResult returns a recoverable error result with a plain-text message.
func ErrorResult(msg string) *Result {
	return &Result{Content: []types.MessagePart{types.TextPart{Text: msg}}, IsError: true}
}

// Text concatenates the TextPart content of the result.
func (r *Result) Text() string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range r.Content {
		if t, ok := p.(types.TextPart); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// AppendText appends a text part to the result.
func (r *Result) AppendText(s string) {
	if r == nil {
		return
	}
	r.Content = append(r.Content, types.TextPart{Text: s})
}

// Action is a stage decision.
type Action int

const (
	// ActionContinue proceeds to the next stage.
	ActionContinue Action = iota
	// ActionBlock does not execute; the call gets an error result carrying the
	// decision reason.
	ActionBlock
	// ActionReplace skips execution and uses Decision.Result instead.
	ActionReplace
)

// Decision is what a Stage or Authorizer tells the pipeline to do.
type Decision struct {
	Action Action
	Reason string
	// Terminate asks the engine to stop after this result.
	Terminate bool
	// Args optionally rewrites the call arguments. After a rewrite the
	// pipeline re-validates them before authorization.
	Args map[string]any
	// Result is required for ActionReplace.
	Result *Result
}

// Continue returns the zero decision.
func Continue() Decision { return Decision{Action: ActionContinue} }

// Block returns a decision that prevents execution.
func Block(reason string) Decision { return Decision{Action: ActionBlock, Reason: reason} }

// Replace returns a decision that substitutes a precomputed result.
func Replace(r *Result) Decision { return Decision{Action: ActionReplace, Result: r} }

// Stage is an extension hook. Before runs after authorize and may block,
// rewrite arguments, or replace the result; After runs on an executed result
// and may mutate it. A Before error is fail-closed, an After error replaces the
// result with an error result.
type Stage interface {
	Name() string
	Before(ctx context.Context, c *Call) (Decision, error)
	After(ctx context.Context, c *Call, r *Result) error
}

// BeforeStage is the optional half of Stage for hooks that only care about the
// pre-execution point.
type BeforeStage interface {
	Name() string
	Before(ctx context.Context, c *Call) (Decision, error)
}

// AfterStage is the optional half of Stage for hooks that only care about the
// post-execution point.
type AfterStage interface {
	Name() string
	After(ctx context.Context, c *Call, r *Result) error
}

// Registry resolves a tool by name. The pipeline uses it when Call.Tool is nil.
type Registry interface {
	Resolve(name string) (types.Tool, bool)
}

// RegistryFunc adapts a function to Registry.
type RegistryFunc func(name string) (types.Tool, bool)

// Resolve implements Registry.
func (f RegistryFunc) Resolve(name string) (types.Tool, bool) { return f(name) }

// Validator checks the prepared arguments. The default is SchemaValidator.
// Validation failures are recoverable results, not fatal errors.
type Validator interface {
	Validate(ctx context.Context, c *Call) error
}

// ValidatorFunc adapts a function to Validator.
type ValidatorFunc func(ctx context.Context, c *Call) error

// Validate implements Validator.
func (f ValidatorFunc) Validate(ctx context.Context, c *Call) error { return f(ctx, c) }

// Authorizer decides whether a call may execute. Returning a Block decision or
// an error yields a recoverable error result. A nil authorizer (or the default
// DenyAllAuthorizer) denies: the pipeline is fail-closed.
type Authorizer interface {
	Authorize(ctx context.Context, c *Call) (Decision, error)
}

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(ctx context.Context, c *Call) (Decision, error)

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(ctx context.Context, c *Call) (Decision, error) {
	return f(ctx, c)
}

// Executor performs the actual tool work. The default ToolExecutor calls
// Call.Tool.Execute with Call.Args.
type Executor interface {
	Execute(ctx context.Context, c *Call) (any, error)
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc func(ctx context.Context, c *Call) (any, error)

// Execute implements Executor.
func (f ExecutorFunc) Execute(ctx context.Context, c *Call) (any, error) { return f(ctx, c) }

// EventSink observes pipeline lifecycle events. Emit must not block for long;
// sink failures are the sink's responsibility (the pipeline ignores them).
type EventSink interface {
	Emit(ctx context.Context, ev Event)
}

// EventSinkFunc adapts a function to EventSink.
type EventSinkFunc func(ctx context.Context, ev Event)

// Emit implements EventSink.
func (f EventSinkFunc) Emit(ctx context.Context, ev Event) { f(ctx, ev) }

// Event types emitted by the pipeline.
const (
	EventToolExecutionStart = "tool_execution_start"
	EventToolExecutionEnd   = "tool_execution_end"
)

// Event is a pipeline lifecycle event. Record failures never block a result.
type Event struct {
	Type      string
	CallID    string
	Name      string
	SessionID string
	TurnID    string
	ParentID  string
	Duration  time.Duration
	IsError   bool
	Result    *Result
	Err       error
}

// Recorder records the final result (transcript/event/metrics). Record errors
// never block the tool result.
type Recorder interface {
	Record(ctx context.Context, c *Call, r *Result) error
}

// RecorderFunc adapts a function to Recorder.
type RecorderFunc func(ctx context.Context, c *Call, r *Result) error

// Record implements Recorder.
func (f RecorderFunc) Record(ctx context.Context, c *Call, r *Result) error {
	return f(ctx, c, r)
}

// Limits bounds batch scheduling and single-call execution.
type Limits struct {
	// MaxConcurrent is the batch concurrency ceiling. Zero picks
	// min(32, max(4, GOMAXPROCS*2)). A value <= 1 forces a serial batch.
	MaxConcurrent int
	// PerToolTimeout overrides DefaultTimeout for a named tool.
	PerToolTimeout map[string]time.Duration
	// DefaultTimeout bounds one execution attempt. Zero means no timeout.
	DefaultTimeout time.Duration
	// Retries is the number of extra attempts after a recoverable failure.
	Retries int
}

// Truncation configures the sanitize stage.
type Truncation struct {
	// DefaultMaxBytes is the per-text-part budget when the tool metadata does
	// not set MaxTruncationLength. Zero uses types.MaxTruncationLength.
	DefaultMaxBytes int
	// Spill, when set, receives oversized output so the context keeps only a
	// path hint. Injecting a writer lets hosts choose the directory layout.
	Spill SpillWriter
	// Header, when set, overrides the default OutputHeader construction.
	Header func(c *Call, r *Result) types.OutputHeader
}

// SpillWriter persists oversized tool output and returns its path.
type SpillWriter interface {
	Spill(ctx context.Context, c *Call, content string) (path string, err error)
}

// SpillWriterFunc adapts a function to SpillWriter.
type SpillWriterFunc func(ctx context.Context, c *Call, content string) (string, error)

// Spill implements SpillWriter.
func (f SpillWriterFunc) Spill(ctx context.Context, c *Call, content string) (string, error) {
	return f(ctx, c, content)
}
