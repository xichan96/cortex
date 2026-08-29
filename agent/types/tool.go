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
