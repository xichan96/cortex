package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/xichan96/cortex/agent/approval"
	"github.com/xichan96/cortex/agent/types"
)

const (
	// maxConcurrentCeiling caps Limits.MaxConcurrent (§4.4).
	maxConcurrentCeiling = 32
	// defaultConcurrencyFloor is the minimum default batch concurrency.
	defaultConcurrencyFloor = 4
)

// Config wires the pipeline's pluggable stages. Zero values select safe,
// working defaults, except Authorizer: a nil Authorizer denies all calls
// (fail-closed). Use AllowAllAuthorizer explicitly when a host has gated
// execution elsewhere.
type Config struct {
	Registry   Registry
	Validator  Validator
	Authorizer Authorizer
	Executor   Executor
	Recorder   Recorder
	EventSink  EventSink

	BeforeHooks []BeforeStage
	AfterHooks  []AfterStage

	Limits     Limits
	Truncation Truncation
}

// Pipeline executes tool calls through a fixed stage sequence. It is safe for
// concurrent use; nested calls may invoke ExecuteOne from inside an Executor.
type Pipeline struct {
	registry   Registry
	validator  Validator
	authorizer Authorizer
	executor   Executor
	recorder   Recorder
	events     EventSink
	before     []BeforeStage
	after      []AfterStage
	limits     Limits
	trunc      Truncation
}

// New builds a Pipeline, applying defaults.
func New(cfg Config) *Pipeline {
	p := &Pipeline{
		registry:   cfg.Registry,
		validator:  cfg.Validator,
		authorizer: cfg.Authorizer,
		executor:   cfg.Executor,
		recorder:   cfg.Recorder,
		events:     cfg.EventSink,
		before:     cfg.BeforeHooks,
		after:      cfg.AfterHooks,
		limits:     cfg.Limits.normalized(),
		trunc:      cfg.Truncation,
	}
	if p.validator == nil {
		p.validator = SchemaValidator{}
	}
	if p.executor == nil {
		p.executor = ToolExecutor{}
	}
	if p.authorizer == nil {
		p.authorizer = DenyAllAuthorizer{Reason: "no authorizer configured"}
	}
	if p.trunc.DefaultMaxBytes <= 0 {
		p.trunc.DefaultMaxBytes = types.MaxTruncationLength
	}
	return p
}

// Limits returns the normalized limits in effect.
func (p *Pipeline) Limits() Limits { return p.limits }

func (l Limits) normalized() Limits {
	if l.MaxConcurrent <= 0 {
		l.MaxConcurrent = defaultConcurrency()
	}
	if l.MaxConcurrent > maxConcurrentCeiling {
		l.MaxConcurrent = maxConcurrentCeiling
	}
	if l.Retries < 0 {
		l.Retries = 0
	}
	return l
}

func defaultConcurrency() int {
	n := runtime.GOMAXPROCS(0) * 2
	if n < defaultConcurrencyFloor {
		n = defaultConcurrencyFloor
	}
	if n > maxConcurrentCeiling {
		n = maxConcurrentCeiling
	}
	return n
}

// preparedCall is a call that passed resolve/prepare/validate/before/authorize.
// blocked is non-nil when the call must not execute.
type preparedCall struct {
	call    *Call
	blocked *Result
}

// ExecuteOne runs one call through every stage. The returned error is non-nil
// only for fatal (types.FatalToolErrorKind) failures; every other failure is in
// the Result.
func (p *Pipeline) ExecuteOne(ctx context.Context, c *Call) (*Result, error) {
	pc, err := p.prepare(ctx, c)
	if pc != nil && pc.blocked != nil {
		// err is non-nil only when a stage produced a fatal error; the blocked
		// result is still recorded so the callID is never left dangling.
		p.finish(ctx, c, pc.blocked, err)
		return pc.blocked, err
	}
	if err != nil {
		return nil, err
	}
	return p.runPrepared(ctx, c)
}

// prepare runs resolve -> prepare_args -> validate -> authorize -> before. It
// emits the start event once the tool is resolved, so a denied call still has a
// matching start/end pair. Ordinary stage failures become blocked results; a
// stage may still surface a fatal error, which is returned alongside a result
// so the caller can record it and then unwind.
func (p *Pipeline) prepare(ctx context.Context, c *Call) (*preparedCall, error) {
	if c == nil {
		return nil, errors.New("pipeline: nil call")
	}
	if c.StartedAt.IsZero() {
		c.StartedAt = time.Now()
	}
	if c.ID == "" {
		return &preparedCall{call: c, blocked: ErrorResult("tool call is missing an id")}, nil
	}

	// resolve
	if c.Tool == nil {
		if p.registry == nil {
			return &preparedCall{call: c, blocked: ErrorResult("unknown tool: " + c.Name)}, nil
		}
		tool, ok := p.registry.Resolve(c.Name)
		if !ok || tool == nil {
			return &preparedCall{call: c, blocked: ErrorResult("unknown tool: " + c.Name)}, nil
		}
		c.Tool = tool
	}
	if c.Tool.Metadata().Exposure.IsHidden() && !c.Internal {
		return &preparedCall{call: c, blocked: ErrorResult("tool not available: " + c.Name)}, nil
	}
	p.emitStart(ctx, c)

	// prepare_args: decode raw JSON, then let the tool normalize/migrate.
	if c.Args == nil {
		args, err := decodeArgs(c.RawArgs)
		if err != nil {
			return &preparedCall{call: c, blocked: ErrorResult("invalid tool arguments: " + err.Error())}, nil
		}
		c.Args = args
	}
	if c.Args == nil {
		c.Args = map[string]any{}
	}
	if prep, ok := c.Tool.(types.ArgumentPreparer); ok {
		prepared, err := prep.PrepareArguments(c.Args)
		if err != nil {
			return &preparedCall{call: c, blocked: ErrorResult(err.Error())}, fatalErrorOrNil(err)
		}
		if prepared != nil {
			c.Args = prepared
		}
	}

	// validate
	if blocked, err := p.validate(ctx, c); blocked != nil {
		return &preparedCall{call: c, blocked: blocked}, err
	}

	// authorize (after validation, before hooks: the authorizer must see the
	// validated arguments, §4.3).
	d, err := p.authorizer.Authorize(ctx, c)
	if err != nil {
		if ferr := fatalErrorOrNil(err); ferr != nil {
			return &preparedCall{call: c, blocked: ErrorResult(ferr.Error())}, ferr
		}
		return &preparedCall{call: c, blocked: authorizationErrorResult(err)}, nil
	}
	blocked, _ := p.decisionResult(c, d, true)
	if blocked != nil {
		return &preparedCall{call: c, blocked: blocked}, nil
	}

	// before hooks (extensions/plugins). A hook error is fail-closed.
	for _, hook := range p.before {
		d, err := hook.Before(ctx, c)
		if err != nil {
			return &preparedCall{call: c, blocked: ErrorResult(formatHookError(hook.Name(), err))}, fatalErrorOrNil(err)
		}
		blocked, revalidate := p.decisionResult(c, d, false)
		if blocked != nil {
			return &preparedCall{call: c, blocked: blocked}, nil
		}
		if revalidate {
			// pi does not re-validate rewritten args; that is a known risk.
			// We do, so the executor never receives invalid arguments.
			if b, verr := p.validate(ctx, c); b != nil {
				return &preparedCall{call: c, blocked: b}, verr
			}
		}
	}
	return &preparedCall{call: c}, nil
}

// fatalErrorOrNil reports err only when it is fatal, so ordinary stage errors
// stay recoverable results. Fatal errors must unwind the iteration.
func fatalErrorOrNil(err error) error {
	if err != nil && types.IsFatalToolError(err) {
		return err
	}
	return nil
}

// decisionResult applies a decision. policyPrefix marks authorize denials,
// which read "blocked by policy: <reason>". The bool reports whether the call's
// arguments were rewritten and must be re-validated.
func (p *Pipeline) decisionResult(c *Call, d Decision, policyPrefix bool) (*Result, bool) {
	switch d.Action {
	case ActionContinue:
		if d.Args != nil {
			c.Args = d.Args
			return nil, true
		}
		return nil, false
	case ActionBlock:
		msg := d.Reason
		if msg == "" {
			msg = "blocked"
		}
		if policyPrefix {
			msg = "blocked by policy: " + msg
		}
		r := ErrorResult(msg)
		r.Terminate = d.Terminate
		return r, false
	case ActionReplace:
		if d.Result == nil {
			return ErrorResult("replace decision without a result"), false
		}
		if d.Terminate {
			d.Result.Terminate = true
		}
		return d.Result, false
	default:
		return ErrorResult(fmt.Sprintf("unknown stage action %d", d.Action)), false
	}
}

func (p *Pipeline) validate(ctx context.Context, c *Call) (*Result, error) {
	if p.validator == nil {
		return nil, nil
	}
	if err := p.validator.Validate(ctx, c); err != nil {
		return ErrorResult(err.Error()), fatalErrorOrNil(err)
	}
	return nil, nil
}

func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	// Plain Unmarshal (not UseNumber) keeps numbers as float64, matching the
	// engine's decoding and the schema validator's type checks.
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

// runPrepared runs execute -> after -> sanitize -> record for an authorized
// call. It returns a fatal error unchanged so the caller can unwind.
func (p *Pipeline) runPrepared(ctx context.Context, c *Call) (*Result, error) {
	r, fatalErr := p.execute(ctx, c)
	if fatalErr != nil {
		p.finish(ctx, c, r, fatalErr)
		return r, fatalErr
	}
	if err := p.runAfter(ctx, c, r); err != nil {
		replaced := ErrorResult(err.Error())
		replaced.Terminate = r.Terminate
		replaced.Duration = r.Duration
		replaced.Details = r.Details
		r = replaced
	}
	p.finish(ctx, c, r, nil)
	return r, nil
}

func (p *Pipeline) runAfter(ctx context.Context, c *Call, r *Result) error {
	for _, hook := range p.after {
		if err := hook.After(ctx, c, r); err != nil {
			return fmt.Errorf("after hook %s: %w", hook.Name(), err)
		}
	}
	return nil
}

// execute calls the executor with per-tool timeout and retries. Recoverable
// errors become an error Result; fatal errors are returned to the caller.
func (p *Pipeline) execute(ctx context.Context, c *Call) (*Result, error) {
	start := time.Now()
	attempts := p.limits.Retries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			lastErr = err
			break
		}
		execCtx, cancel := p.executionContext(ctx, c.Name)
		out, err := p.executor.Execute(execCtx, c)
		cancel()
		if err == nil {
			r := normalizeOutput(out)
			r.Duration = time.Since(start)
			return r, nil
		}
		if types.IsFatalToolError(err) {
			r := ErrorResult(err.Error())
			r.Duration = time.Since(start)
			return r, err
		}
		lastErr = err
		// A cancelled parent means no further attempt can succeed.
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("tool execution failed")
	}
	r := ErrorResult(lastErr.Error())
	r.Duration = time.Since(start)
	return r, nil
}

func (p *Pipeline) executionContext(ctx context.Context, toolName string) (context.Context, context.CancelFunc) {
	timeout := p.limits.DefaultTimeout
	if t, ok := p.limits.PerToolTimeout[toolName]; ok {
		timeout = t
	}
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

// finish applies sanitization then records. Sanitization never fails; record
// errors are ignored.
func (p *Pipeline) finish(ctx context.Context, c *Call, r *Result, execErr error) {
	if r == nil {
		r = ErrorResult("tool execution produced no result")
	}
	p.sanitize(ctx, c, r)
	p.emitEnd(ctx, c, r, execErr)
	if p.recorder != nil {
		_ = p.recorder.Record(ctx, c, r)
	}
}

func (p *Pipeline) emitStart(ctx context.Context, c *Call) {
	if p.events == nil || c == nil {
		return
	}
	p.events.Emit(ctx, Event{
		Type:      EventToolExecutionStart,
		CallID:    c.ID,
		Name:      c.Name,
		SessionID: c.SessionID,
		TurnID:    c.TurnID,
		ParentID:  c.ParentID,
	})
}

func (p *Pipeline) emitEnd(ctx context.Context, c *Call, r *Result, err error) {
	if p.events == nil || c == nil {
		return
	}
	p.events.Emit(ctx, Event{
		Type:      EventToolExecutionEnd,
		CallID:    c.ID,
		Name:      c.Name,
		SessionID: c.SessionID,
		TurnID:    c.TurnID,
		ParentID:  c.ParentID,
		Duration:  r.Duration,
		IsError:   r.IsError,
		Result:    r,
		Err:       err,
	})
}

// normalizeOutput converts whatever a tool returned into a Result. It supports
// the pi-style map ({content, details, isError}) as well as plain strings and
// arbitrary values (JSON-encoded).
func normalizeOutput(out any) *Result {
	switch v := out.(type) {
	case nil:
		return &Result{}
	case *Result:
		if v == nil {
			return &Result{}
		}
		return v
	case Result:
		return &v
	case []types.MessagePart:
		return &Result{Content: v}
	case types.MessagePart:
		return &Result{Content: []types.MessagePart{v}}
	case string:
		return TextResult(v)
	case map[string]any:
		return resultFromMap(v)
	case []byte:
		return TextResult(string(v))
	default:
		return TextResult(stringify(v))
	}
}

func resultFromMap(m map[string]any) *Result {
	r := &Result{}
	hasContent := false
	if c, ok := m["content"]; ok && c != nil {
		if parts := contentParts(c); len(parts) > 0 {
			r.Content = parts
			hasContent = true
		}
	}
	if !hasContent {
		r.Content = []types.MessagePart{types.TextPart{Text: stringify(m)}}
	}
	if d, ok := m["details"]; ok {
		r.Details = d
	}
	if e, ok := m["isError"].(bool); ok {
		r.IsError = e
	}
	if e, ok := m["is_error"].(bool); ok {
		r.IsError = e
	}
	if t, ok := m["terminate"].(bool); ok {
		r.Terminate = t
	}
	if s, ok := m["structured"]; ok {
		if raw, err := json.Marshal(s); err == nil {
			r.Structured = raw
		}
	}
	return r
}

// contentParts converts a pi-style content value (string, text block, or list)
// into message parts.
func contentParts(v any) []types.MessagePart {
	switch c := v.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []types.MessagePart{types.TextPart{Text: c}}
	case []any:
		parts := make([]types.MessagePart, 0, len(c))
		for _, item := range c {
			parts = append(parts, contentParts(item)...)
		}
		return parts
	case map[string]any:
		if t, ok := c["text"].(string); ok {
			return []types.MessagePart{types.TextPart{Text: t}}
		}
		return nil
	default:
		return []types.MessagePart{types.TextPart{Text: stringify(c)}}
	}
}

func stringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func formatHookError(name string, err error) string {
	if name == "" {
		return "tool hook failed: " + err.Error()
	}
	return fmt.Sprintf("tool hook %s failed: %v", name, err)
}

// authorizationErrorResult maps authorizer/broker errors to the stable result
// messages defined by the design (§5.3).
func authorizationErrorResult(err error) *Result {
	switch {
	case errors.Is(err, approval.ErrApprovalTimeout), errors.Is(err, context.DeadlineExceeded):
		return ErrorResult("approval timed out")
	case errors.Is(err, approval.ErrApprovalCancelled), errors.Is(err, context.Canceled):
		return ErrorResult("approval cancelled")
	case errors.Is(err, approval.ErrApprovalDenied):
		return ErrorResult("blocked by policy: " + err.Error())
	case errors.Is(err, approval.ErrApprovalUnavailable):
		return ErrorResult("blocked by policy: approval broker unavailable")
	default:
		return ErrorResult("authorization failed: " + err.Error())
	}
}
