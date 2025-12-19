package types

import "time"

// Tool defines tool interface
type Tool interface {
	// Tool basic information
	Name() string
	Description() string
	Schema() map[string]interface{}

	// Tool execution
	Execute(input map[string]interface{}) (interface{}, error)

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

// AgentConfig agent configuration
type AgentConfig struct {
	MaxIterations           int           `json:"maxIterations"`
	SystemMessage           string        `json:"systemMessage"`
	Temperature             float32       `json:"temperature"`             // 温度参数 (0.0-1.0)
	MaxTokens               int           `json:"maxTokens"`               // 最大token数
	TopP                    float32       `json:"topP"`                    // Top P采样
	FrequencyPenalty        float32       `json:"frequencyPenalty"`        // 频率惩罚
	PresencePenalty         float32       `json:"presencePenalty"`         // 存在惩罚
	StopSequences           []string      `json:"stopSequences"`           // 停止序列
	Timeout                 time.Duration `json:"timeout"`                 // 超时时间
	ToolExecutionTimeout    time.Duration `json:"toolExecutionTimeout"`    // 工具执行超时时间
	RetryAttempts           int           `json:"retryAttempts"`           // 重试次数
	RetryDelay              time.Duration `json:"retryDelay"`              // 重试延迟
	EnableToolRetry         bool          `json:"enableToolRetry"`         // 启用工具重试
	MaxHistoryMessages      int           `json:"maxHistoryMessages"`      // 最大历史消息数
	EnableMemoryCompress    bool          `json:"enableMemoryCompress"`    // 启用记忆压缩
	MemoryCompressThreshold int           `json:"memoryCompressThreshold"` // 记忆压缩阈值（消息数量）
	MemoryCompressRatio     float32       `json:"memoryCompressRatio"`     // 记忆压缩比例（0.0-1.0）
}

// NewAgentConfig creates a new agent configuration with reasonable defaults
func NewAgentConfig() *AgentConfig {
	return &AgentConfig{
		MaxIterations:           10,
		SystemMessage:           "",
		Temperature:             0.7,
		MaxTokens:               4096,
		TopP:                    1.0,
		FrequencyPenalty:        0.0,
		PresencePenalty:         0.0,
		StopSequences:           []string{},
		Timeout:                 30 * time.Second,
		ToolExecutionTimeout:    60 * time.Second,
		RetryAttempts:           3,
		RetryDelay:              1 * time.Second,
		EnableToolRetry:         true,
		MaxHistoryMessages:      100,
		EnableMemoryCompress:    false,
		MemoryCompressThreshold: 50,
		MemoryCompressRatio:     0.5,
	}
}

// StreamEvent stream event
type StreamEvent struct {
	Type       string      `json:"type"`
	Content    string      `json:"content,omitempty"`
	ToolResult interface{} `json:"toolResult,omitempty"`
	EventName  string      `json:"eventName,omitempty"`
	Data       interface{} `json:"data,omitempty"`
}
