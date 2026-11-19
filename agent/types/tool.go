package types

import (
	"context"
	"fmt"

	stderrors "errors"
)

// FatalToolError marks a tool error that is NOT recoverable by feeding it back
// to the model: retrying the same input cannot succeed. Fatal errors should
// surface to the engine and stop (or restructure) the current iteration rather
// than being converted into a recoverable {ok:false} result.
//
// Examples: schema/input validation failures, permission/approval vetoes, a
// tool that does not exist. nonFatalTool passes these through as real errors.
type FatalToolError struct {
	Err    error
	Reason string
}

func (e *FatalToolError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("fatal tool error: %s", e.Reason)
	}
	if e.Err != nil {
		return fmt.Sprintf("fatal tool error: %v", e.Err)
	}
	return "fatal tool error"
}

func (e *FatalToolError) Unwrap() error { return e.Err }

// FatalToolErrorKind implements agent/types.FatalToolErrorKind.
func (e *FatalToolError) FatalToolErrorKind() {}

// FatalToolErrorKind is implemented by every error type the tool pipeline treats
// as FATAL (F3/P4.2): an error that cannot be fixed by retrying the same input,
// so it must surface to the engine and unwind the iteration instead of being fed
// back to the model as a recoverable {ok:false} result. Engine code recognizes
// fatal errors via FatalToolErrorKindOf/IsFatalToolError without importing dino;
// dino/tools errors (ApprovalRejectedError, LoopDetectedError) implement it so a
// user veto or a loop also short-circuits the errgroup like a schema failure.
//
// Errors that are NOT fatal but still carry error codes (EC_TOOL_INPUT_ERROR,
// EC_TOOL_AUTH_ERROR, MCP 11xxx, EC_TOOL_EXECUTION_TIMEOUT, …) are recoverable:
// the model can change arguments or retry later, so they are fed back.
type FatalToolErrorKind interface {
	error
	FatalToolErrorKind()
}

var _ FatalToolErrorKind = (*FatalToolError)(nil)

// FatalToolErrorKindOf classifies an error as fatal (FatalToolErrorKind) or
// recoverable (nil). It unwraps %w-wrapped errors.
func FatalToolErrorKindOf(err error) FatalToolErrorKind {
	var fatal FatalToolErrorKind
	if stderrors.As(err, &fatal) {
		return fatal
	}
	return nil
}

// IsFatalToolError reports whether err is a fatal tool error. The schema
// validation failure at agent_execution.go:407 is already wrapped in
// FatalToolError; dino-side veto/loop errors implement FatalToolErrorKind.
func IsFatalToolError(err error) bool {
	return FatalToolErrorKindOf(err) != nil
}

// ToolExposure controls a tool's visibility in the model's initial tool list
// (E1). Registration and model-visibility are decoupled: a tool stays registered
// (dispatchable) regardless of its exposure.
//
//   - ExposureDirect: registered AND visible in the initial list (current default).
//   - ExposureDeferred: registered but NOT in the initial list; the model finds it
//     via tool_search, and it is injected into ae.tools the next iteration.
//   - ExposureHidden: registered and dispatchable by the engine, never visible to
//     the model (reserved for escape-hatch / internal orchestration tools).
type ToolExposure string

const (
	// ExposureDirect 默认：注册即对模型可见（现状行为）。空值等价于 Direct。
	ExposureDirect ToolExposure = "direct"
	// ExposureDeferred 注册可分发但初始列表不可见；模型通过 tool_search 发现后
	// 下一轮才注入为正式工具。
	ExposureDeferred ToolExposure = "deferred"
	// ExposureHidden 完全不可见（但仍可被引擎内部分发）。
	ExposureHidden ToolExposure = "hidden"
)

// IsDirect reports whether the exposure yields initial-list visibility.
// The empty value is treated as Direct (zero migration: tools that never set
// Exposure keep current behavior).
func (e ToolExposure) IsDirect() bool { return e == "" || e == ExposureDirect }

// IsDeferred reports whether the exposure is deferred.
func (e ToolExposure) IsDeferred() bool { return e == ExposureDeferred }

// IsHidden reports whether the exposure is hidden.
func (e ToolExposure) IsHidden() bool { return e == ExposureHidden }

// ExecutionMode controls how a tool call is scheduled when it appears in a
// batch of calls from one assistant message (P0-2). The zero value means
// "inherit the batch default": the batch runs concurrently unless another
// tool in the same batch declares Sequential.
type ExecutionMode string

const (
	// ExecutionModeParallel allows the call to run concurrently with its
	// siblings. The empty value is equivalent to Parallel.
	ExecutionModeParallel ExecutionMode = "parallel"
	// ExecutionModeSequential forces the whole batch to run serially. A tool
	// opts in when it mutates shared process state (e.g. changing cwd), so
	// running it alongside other calls would be unsafe.
	ExecutionModeSequential ExecutionMode = "sequential"
)

// IsSequential reports whether the mode forces a serial batch.
func (m ExecutionMode) IsSequential() bool { return m == ExecutionModeSequential }

// RiskLevel classifies how dangerous a tool invocation is (P0-2, §5.2/§5.6).
// Approval scopes are narrowed for high-risk calls (see approval.ScopesForRisk).
type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

// IsHigh reports whether the risk level is high.
func (r RiskLevel) IsHigh() bool { return r == RiskHigh }

// IsKnown reports whether the risk level is one of the declared levels. The
// empty value is unknown and is treated as low by callers that need a default.
func (r RiskLevel) IsKnown() bool {
	return r == RiskLow || r == RiskMedium || r == RiskHigh
}

// ArgumentPreparer is an optional tool interface (P0-2). PrepareArguments runs
// after raw model arguments are decoded and before schema validation, letting a
// tool normalize or migrate legacy arguments (pi's prepareArguments hook). A
// tool that does not implement it is prepared as-is.
//
// The returned map becomes the validated/authorized/executed argument set. An
// error is surfaced to the model as a recoverable tool error unless it
// implements FatalToolErrorKind, in which case it unwinds the iteration.
type ArgumentPreparer interface {
	PrepareArguments(args map[string]interface{}) (map[string]interface{}, error)
}

// ExecutionModeProvider is an optional tool interface consulted by the
// pipeline scheduler. It takes precedence over ToolMetadata.ExecutionMode.
type ExecutionModeProvider interface {
	ExecutionMode() ExecutionMode
}

// RiskProvider is an optional tool interface consulted by the approval
// authorizer. It takes precedence over ToolMetadata.Risk.
type RiskProvider interface {
	Risk() RiskLevel
}

// ToolExecutionMode returns the tool's declared execution mode, preferring the
// optional ExecutionModeProvider interface over metadata. The zero value means
// parallel/inherit.
func ToolExecutionMode(t Tool) ExecutionMode {
	if t == nil {
		return ""
	}
	if p, ok := t.(ExecutionModeProvider); ok {
		if m := p.ExecutionMode(); m != "" {
			return m
		}
	}
	return t.Metadata().ExecutionMode
}

// ToolRisk returns the tool's declared risk level, preferring the optional
// RiskProvider interface over metadata. The empty value means unknown.
func ToolRisk(t Tool) RiskLevel {
	if t == nil {
		return ""
	}
	if p, ok := t.(RiskProvider); ok {
		if r := p.Risk(); r != "" {
			return r
		}
	}
	return t.Metadata().Risk
}

// Tool defines tool interface
type Tool interface {
	// Tool basic information
	Name() string
	Description() string
	Schema() map[string]interface{}

	// Tool execution
	Execute(ctx context.Context, input map[string]interface{}) (interface{}, error)

	// Tool metadata
	Metadata() ToolMetadata
}

// ToolMetadata tool metadata
type ToolMetadata struct {
	SourceNodeName      string                 `json:"sourceNodeName"`
	IsFromToolkit       bool                   `json:"isFromToolkit"`
	ToolType            string                 `json:"toolType"`                      // "mcp","http","builtin"
	Priority            int                    `json:"priority,omitempty"`            // 优先级，数字越大优先级越高
	Dependencies        []string               `json:"dependencies,omitempty"`        // 依赖的工具名称列表
	MaxTruncationLength int                    `json:"maxTruncationLength,omitempty"` // 工具结果截断长度，0表示使用默认值
	Exposure            ToolExposure           `json:"exposure,omitempty"`            // E1：模型可见性；空值=direct（向后兼容）
	SearchKeywords      []string               `json:"searchKeywords,omitempty"`      // E2：tool_search 索引关键词；空则用 Name+Description 分词
	ExecutionMode       ExecutionMode          `json:"executionMode,omitempty"`       // P0-2：批内调度模式；空值=parallel
	Risk                RiskLevel              `json:"risk,omitempty"`                // P0-2：审批风险分级；空值=unknown
	Extra               map[string]interface{} `json:"extra,omitempty"`
}

// ToolCallRequest tool call request
type ToolCallRequest struct {
	Tool       string                 `json:"tool"`
	ToolInput  map[string]interface{} `json:"toolInput"`
	ToolCallID string                 `json:"toolCallId"`
	Type       string                 `json:"type,omitempty"`
	Log        string                 `json:"log,omitempty"`
	MessageLog []interface{}          `json:"messageLog,omitempty"`
}

// ToolAction tool action
type ToolAction struct {
	NodeName string                 `json:"nodeName"`
	Input    map[string]interface{} `json:"input"`
	Type     string                 `json:"type"`
	ID       string                 `json:"id"`
	Metadata ActionMetadata         `json:"metadata"`
}

// ActionMetadata action metadata
type ActionMetadata struct {
	ItemIndex int `json:"itemIndex"`
}

// EngineRequest engine request
type EngineRequest struct {
	Actions  []ToolAction            `json:"actions"`
	Metadata RequestResponseMetadata `json:"metadata"`
}

// EngineResponse engine response
type EngineResponse struct {
	ActionResponses []ActionResponse        `json:"actionResponses"`
	Metadata        RequestResponseMetadata `json:"metadata"`
}

// ActionResponse action response
type ActionResponse struct {
	Action *ToolAction `json:"action"`
	Data   interface{} `json:"data"`
	Error  string      `json:"error,omitempty"`
}

// RequestResponseMetadata request response metadata
type RequestResponseMetadata struct {
	ItemIndex        int            `json:"itemIndex,omitempty"`
	PreviousRequests []ToolCallData `json:"previousRequests,omitempty"`
	IterationCount   int            `json:"iterationCount,omitempty"`
}

// ToolCallData tool call data
type ToolCallData struct {
	Action      ToolActionStep `json:"action"`
	Observation string         `json:"observation"`
}

// ToolActionStep tool action step
type ToolActionStep struct {
	Tool       string      `json:"tool"`
	ToolInput  interface{} `json:"toolInput"`
	Log        interface{} `json:"log"`
	ToolCallID interface{} `json:"toolCallId"`
	Type       interface{} `json:"type"`
}
