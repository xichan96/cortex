package engine

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/types"
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

// bufferPool for reusing byte buffers to reduce GC pressure
var bufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 0, DefaultBufferPoolSize) // 使用常量定义的大小
	},
}

// ==================== Data Structures and Type Definitions ====================

// AgentResult agent execution result
type AgentResult struct {
	Output            string                  `json:"output"`
	ToolCalls         []types.ToolCallRequest `json:"tool_calls"`
	IntermediateSteps []types.ToolCallData    `json:"intermediate_steps"`
}

// toolCacheEntry tool cache entry with LRU support
type toolCacheEntry struct {
	result    interface{}
	err       error
	timestamp time.Time
	prev      *toolCacheEntry
	next      *toolCacheEntry
	key       string
}

// StreamResult streaming result
type StreamResult struct {
	Type    string
	Content string
	Result  *AgentResult
	Error   error
}

// truncateString truncates a string to the specified length
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// formatToolResult formats tool execution result to string
// Uses JSON marshaling for better representation of complex data structures
func formatToolResult(result interface{}) string {
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
