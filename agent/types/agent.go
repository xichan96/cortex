package types

import (
	"encoding/json"
	"fmt"
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
}

// TruncateString truncates a string to the specified length
func TruncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// FormatToolResult formats tool execution result to string
// Uses JSON marshaling for better representation of complex data structures
func FormatToolResult(result interface{}) string {
	if result == nil {
		return "Tool executed successfully but returned no result"
	}
	// Try JSON marshaling first for better formatting
	if jsonBytes, err := json.MarshalIndent(result, "", "  "); err == nil {
		return string(jsonBytes)
	}
	// Fallback to string representation if JSON marshaling fails
	return fmt.Sprintf("%v", result)
}
