package types

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Constant definitions
const (
	// Cache-related constants
	DefaultCacheSize    = 100             // default tool cache size
	CacheExpirationTime = 5 * time.Minute // cache expiration time
	// Execution-related constants
	DefaultChannelBuffer = 50   // default channel buffer size
	MaxTruncationLength  = 2048 // maximum truncation length
	MinChannelBuffer     = 10   // minimum channel buffer size
	// Performance-related constants
	DefaultBufferPoolSize = 1024                   // default buffer pool size (1KB)
	IterationDelay        = 100 * time.Millisecond // inter-iteration delay
)

// Tool state constants (aligned with OpenCode)
const (
	ToolStatePending   = "pending"
	ToolStateRunning   = "running"
	ToolStateCompleted = "completed"
	ToolStateError     = "error"

	ToolEmptyResultMessage = "Tool executed successfully but returned no result"
)

// StreamResult tool event types
const (
	StreamEventToolCall       = "tool_call"
	StreamEventToolInputStart = "tool_input_start"
	StreamEventToolInputEnd   = "tool_input_end"
	StreamEventToolResult     = "tool_result"
	StreamEventToolError      = "tool_error"
)

// Context keys
const (
	ContextKeyTemperature         = "temperature"
	ContextKeyMaxTokens           = "max_tokens"
	ContextKeyTopP                = "top_p"
	ContextKeyMaxCompletionTokens = "max_completion_tokens"
	ContextKeyReasoningEffort     = "reasoning_effort"
)

// AgentConfig agent configuration
type AgentConfig struct {
	MaxIterations            int                                                               `json:"maxIterations"`
	SystemMessage            string                                                            `json:"systemMessage"`
	ChatMessageRole          string                                                            `json:"chatMessageRole,omitempty"`
	Temperature              float32                                                           `json:"temperature"`
	MaxTokens                int                                                               `json:"maxTokens,omitempty"`
	MaxCompletionTokens      int                                                               `json:"maxCompletionTokens,omitempty"`
	TopP                     float32                                                           `json:"topP"`
	FrequencyPenalty         float32                                                           `json:"frequencyPenalty"`
	PresencePenalty          float32                                                           `json:"presencePenalty"`
	StopSequences            []string                                                          `json:"stopSequences"`
	Timeout                  time.Duration                                                     `json:"timeout"`
	ToolExecutionTimeout     time.Duration                                                     `json:"toolExecutionTimeout"`
	ToolTimeouts             map[string]time.Duration                                          `json:"toolTimeouts"`
	RetryAttempts            int                                                               `json:"retryAttempts"`
	RetryDelay               time.Duration                                                     `json:"retryDelay"`
	EnableToolRetry          bool                                                              `json:"enableToolRetry"`
	MaxHistoryMessages       int                                                               `json:"maxHistoryMessages"`
	MaxBudgetTokens          int                                                               `json:"maxBudgetTokens"`
	RemainPromptTokens       func() int                                                        `json:"-"`
	EnableMemoryCompress     bool                                                              `json:"enableMemoryCompress"`
	MemoryCompressThreshold  int                                                               `json:"memoryCompressThreshold"` // message count incl. each assistant + tool row from tool rounds
	CompactAfterTurns        int                                                               `json:"compactAfterTurns"`       // completed Execute/ExecuteStream saves before compress; 0=off
	MemoryCompressRatio      float32                                                           `json:"memoryCompressRatio"`
	LogSilent                bool                                                              `json:"logSilent"`
	LogFile                  string                                                            `json:"logFile"`
	DoomLoopThreshold        int                                                               `json:"doomLoopThreshold"`
	OnDoomLoop               func(toolName string, input map[string]interface{}) bool          `json:"-"`
	ToolTimeoutCalculator    func(toolName string, input map[string]interface{}) time.Duration `json:"-"`
	MaxToolCallsPerIteration int                                                               `json:"maxToolCallsPerIteration"`
	ToolResultWriteDir       string                                                            `json:"toolResultWriteDir"`
	DefaultToolResultMaxLen  int                                                               `json:"defaultToolResultMaxLen"`
	ToolErrorMaxLen          int                                                               `json:"toolErrorMaxLen"`
	ReasoningEffort          string                                                            `json:"reasoningEffort"`
	// PromptCaching enables provider prompt caching (Anthropic cache_control
	// breakpoints) and cache usage backfill. Default on: it is a pure cost
	// optimization with no correctness impact. Set false for providers/proxies
	// that choke on cache_control (R9 escape hatch).
	PromptCaching bool `json:"promptCaching,omitempty"`
	ToolParallelismLimit     int                                                               `json:"toolParallelismLimit,omitempty"` // 0=默认 max(4, GOMAXPROCS*2) 封顶 32；>0 用该值
	StreamBufferSize         int                                                               `json:"streamBufferSize,omitempty"`     // 0=默认 50；>0 用该值
}

func (c *AgentConfig) EffectiveMaxCompletionTokens() int {
	if c == nil {
		return 0
	}
	if c.MaxCompletionTokens > 0 {
		return c.MaxCompletionTokens
	}
	return c.MaxTokens
}

func NewAgentConfig() *AgentConfig {
	return &AgentConfig{
		MaxIterations:            10,
		SystemMessage:            "",
		ChatMessageRole:          "",
		Temperature:              0.7,
		MaxCompletionTokens:      4096,
		TopP:                     1.0,
		FrequencyPenalty:         0.0,
		PresencePenalty:          0.0,
		StopSequences:            []string{},
		Timeout:                  30 * time.Second,
		ToolExecutionTimeout:     60 * time.Second,
		RetryAttempts:            3,
		RetryDelay:               1 * time.Second,
		EnableToolRetry:          true,
		MaxHistoryMessages:       100,
		EnableMemoryCompress:     false,
		MemoryCompressThreshold:  50,
		MemoryCompressRatio:      0.5,
		LogSilent:                false,
		LogFile:                  "",
		DoomLoopThreshold:        5,
		OnDoomLoop:               nil,
		MaxToolCallsPerIteration: 0,
		ToolResultWriteDir:       "",
		DefaultToolResultMaxLen:  0,
		ToolErrorMaxLen:          0,
		ReasoningEffort:          "",
		PromptCaching:            true,
		ToolParallelismLimit:     0,
		StreamBufferSize:         0,
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

// AgentInput agent execution input
type AgentInput struct {
	Text  string        `json:"text,omitempty"`
	Parts []MessagePart `json:"parts,omitempty"`
}

// String returns the string representation of the input
func (i AgentInput) String() string {
	if len(i.Parts) > 0 {
		var texts []string
		for _, part := range i.Parts {
			if txt, ok := part.(TextPart); ok {
				texts = append(texts, txt.Text)
			}
		}
		if len(texts) > 0 {
			return fmt.Sprintf("%v", texts)
		}
		return "[Multimodal Content]"
	}
	return i.Text
}

// NewAgentInput creates a new agent input with text
func NewAgentInput(text string) AgentInput {
	return AgentInput{
		Text: text,
	}
}

// NewAgentInputWithParts creates a new agent input with message parts
func NewAgentInputWithParts(parts []MessagePart) AgentInput {
	return AgentInput{
		Parts: parts,
	}
}

// ToMessage converts AgentInput to Message
func (i AgentInput) ToMessage(role string) Message {
	msg := Message{
		Role: role,
	}
	if len(i.Parts) > 0 {
		msg.Parts = i.Parts
	} else {
		msg.Content = i.Text
	}
	return msg
}

// ConvertToAgentInput converts input to AgentInput
// Supports string and AgentInput types
func ConvertToAgentInput(input interface{}) (AgentInput, error) {
	switch v := input.(type) {
	case string:
		return NewAgentInput(v), nil
	case AgentInput:
		return v, nil
	default:
		return AgentInput{}, fmt.Errorf("unsupported input type: %T", input)
	}
}

// BufferPool for reusing byte buffers to reduce GC pressure
var BufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 0, DefaultBufferPoolSize) // Use constant defined size
	},
}

// ==================== Data Structures and Type Definitions ====================
// AgentResult agent execution result
type AgentResult struct {
	Output            string            `json:"output"`
	ToolCalls         []ToolCallRequest `json:"tool_calls"`
	IntermediateSteps []ToolCallData    `json:"intermediate_steps"`
	Usage             Usage             `json:"usage"`
	StopCause         AgentStopCause    `json:"stop_cause,omitempty"`
}

// ToolCacheEntry tool cache entry with LRU support
type ToolCacheEntry struct {
	Result    interface{}
	Err       error
	Timestamp time.Time
	Prev      *ToolCacheEntry
	Next      *ToolCacheEntry
	Key       string
}

// StreamResult streaming result
type StreamResult struct {
	Type    string
	Content string
	Result  *AgentResult
	Error   error
	// Tool event fields
	ToolEvent *ToolEvent
	// StopCause classifies Error (e.g. context_window) for harness consumers.
	StopCause AgentStopCause
}

// ToolEvent represents a tool execution lifecycle event
type ToolEvent struct {
	Event      string                 // Event type: tool_call, tool_input_start, tool_input_end, tool_result, tool_error
	ToolName   string                 // Tool name
	ToolCallID string                 // Tool call ID
	State      string                 // State: pending, running, completed, error
	Input      map[string]interface{} // Tool input arguments
	Output     interface{}            // Tool execution result
	Error      string                 // Error message if failed
	Duration   time.Duration          // Execution duration
}

// ToolCallback is an interface for receiving tool execution events in real-time
type ToolCallback interface {
	OnToolCall(toolName string, toolCallID string, input map[string]interface{})
	OnToolInputStart(toolName string, toolCallID string, input map[string]interface{})
	OnToolInputEnd(toolName string, toolCallID string, input map[string]interface{})
	OnToolResult(toolName string, toolCallID string, output interface{})
	OnToolError(toolName string, toolCallID string, err error)
}

// ToolCallbackFunc is a functional adapter for ToolCallback
type ToolCallbackFunc struct {
	onToolCall       func(toolName string, toolCallID string, input map[string]interface{})
	onToolInputStart func(toolName string, toolCallID string, input map[string]interface{})
	onToolInputEnd   func(toolName string, toolCallID string, input map[string]interface{})
	onToolResult     func(toolName string, toolCallID string, output interface{})
	onToolError      func(toolName string, toolCallID string, err error)
}

func (f *ToolCallbackFunc) OnToolCall(toolName string, toolCallID string, input map[string]interface{}) {
	if f.onToolCall != nil {
		f.onToolCall(toolName, toolCallID, input)
	}
}

func (f *ToolCallbackFunc) OnToolInputStart(toolName string, toolCallID string, input map[string]interface{}) {
	if f.onToolInputStart != nil {
		f.onToolInputStart(toolName, toolCallID, input)
	}
}

func (f *ToolCallbackFunc) OnToolInputEnd(toolName string, toolCallID string, input map[string]interface{}) {
	if f.onToolInputEnd != nil {
		f.onToolInputEnd(toolName, toolCallID, input)
	}
}

func (f *ToolCallbackFunc) OnToolResult(toolName string, toolCallID string, output interface{}) {
	if f.onToolResult != nil {
		f.onToolResult(toolName, toolCallID, output)
	}
}

func (f *ToolCallbackFunc) OnToolError(toolName string, toolCallID string, err error) {
	if f.onToolError != nil {
		f.onToolError(toolName, toolCallID, err)
	}
}

// TruncateString truncates a string to the specified length, keeping the head
// (a UTF-8-safe convenience used for logging and short labels). For
// tool-output truncation that preserves the middle, use TruncateMiddle.
func TruncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return truncateUTF8Head(s, maxLen) + "..."
}

const ToolErrorMaxLen = 400

func NormalizeToolError(err error, maxLen int) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '\r' {
			s = s[:i]
			break
		}
	}
	s = strings.TrimSpace(s)
	if maxLen <= 0 {
		maxLen = ToolErrorMaxLen
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// FormatToolResult formats tool execution result to string
// Uses JSON marshaling for better representation of complex data structures
func FormatToolResult(result interface{}) string {
	if result == nil {
		return ToolEmptyResultMessage
	}
	if s, ok := result.(string); ok && strings.TrimSpace(s) == "" {
		return ToolEmptyResultMessage
	}
	if jsonBytes, err := json.MarshalIndent(result, "", "  "); err == nil {
		s := strings.TrimSpace(string(jsonBytes))
		if s == `""` || s == "null" {
			return ToolEmptyResultMessage
		}
		return string(jsonBytes)
	}
	return fmt.Sprintf("%v", result)
}

// SerializeMessageParts serializes message parts to JSON string
func SerializeMessageParts(parts []MessagePart) (string, error) {
	if len(parts) == 0 {
		return "", nil
	}

	var serializedParts []map[string]interface{}

	for _, part := range parts {
		partMap := make(map[string]interface{})

		switch p := part.(type) {
		case TextPart:
			partMap["type"] = "text"
			partMap["text"] = p.Text
		case ImageURLPart:
			partMap["type"] = "image_url"
			partMap["image_url"] = map[string]interface{}{
				"url":    p.URL,
				"detail": p.Detail,
			}
		case ImageDataPart:
			partMap["type"] = "image_data"
			partMap["image_data"] = map[string]interface{}{
				"data":      p.Data,
				"mime_type": p.MIMEType,
			}
		default:
			continue
		}
		serializedParts = append(serializedParts, partMap)
	}

	bytes, err := json.Marshal(serializedParts)
	if err != nil {
		return "", err
	}

	return string(bytes), nil
}

// DeserializeMessageParts deserializes JSON string to message parts
func DeserializeMessageParts(data string) ([]MessagePart, error) {
	if data == "" {
		return nil, nil
	}

	var rawParts []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &rawParts); err != nil {
		return nil, err
	}

	var parts []MessagePart

	for _, rawPart := range rawParts {
		var typeStr string
		if err := json.Unmarshal(rawPart["type"], &typeStr); err != nil {
			continue
		}

		switch typeStr {
		case "text":
			var text string
			if err := json.Unmarshal(rawPart["text"], &text); err == nil {
				parts = append(parts, TextPart{Text: text})
			}
		case "image_url":
			var imgURL struct {
				URL    string `json:"url"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(rawPart["image_url"], &imgURL); err == nil {
				parts = append(parts, ImageURLPart{URL: imgURL.URL, Detail: imgURL.Detail})
			}
		case "image_data":
			var imgData struct {
				Data     []byte `json:"data"`
				MIMEType string `json:"mime_type"`
			}
			if err := json.Unmarshal(rawPart["image_data"], &imgData); err == nil {
				parts = append(parts, ImageDataPart{Data: imgData.Data, MIMEType: imgData.MIMEType})
			}
		}
	}

	if len(parts) == 0 && len(rawParts) > 0 {
		return nil, fmt.Errorf("failed to deserialize any message parts")
	}

	return parts, nil
}
