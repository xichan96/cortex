package types

import "context"

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
