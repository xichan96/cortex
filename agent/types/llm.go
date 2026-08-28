package types

import "context"

// LLMProvider defines LLM provider interface
type LLMProvider interface {
	// Basic chat functionality
	Chat(ctx context.Context, messages []Message) (Message, error)
	ChatStream(ctx context.Context, messages []Message) (<-chan StreamMessage, error)

	// Tool call support
	ChatWithTools(ctx context.Context, messages []Message, tools []Tool) (Message, error)
	ChatWithToolsStream(ctx context.Context, messages []Message, tools []Tool) (<-chan StreamMessage, error)

	// Model information
	GetModelName() string
	GetModelMetadata() ModelMetadata
}

// ModelMetadata model metadata
type ModelMetadata struct {
	Name      string                 `json:"name"`
	Version   string                 `json:"version"`
	MaxTokens int                    `json:"maxTokens"`
	Tools     []Tool                 `json:"tools,omitempty"`
	Extra     map[string]interface{} `json:"extra,omitempty"`
}

// Message message structure
type Message struct {
	Role       string        `json:"role"` // "system", "user", "assistant", "tool"
	Content    string        `json:"content"`
	Name       string        `json:"name,omitempty"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Parts      []MessagePart `json:"parts,omitempty"` // Multi-modal content support
	Usage      Usage         `json:"usage,omitempty"` // Token usage information
}

// Usage token usage information
//
// B1 (review): PromptTokens/TotalTokens must reflect the *total* input (uncached +
// cached read + cache creation), because Anthropic's `input_tokens` is the uncached
// remainder only. CachedTokens/CacheCreationTokens are a split *within* the input.
type Usage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	ReasoningTokens     int `json:"reasoning_tokens,omitempty"` // Thinking tokens (o1, o3 series etc.)
	CachedTokens        int `json:"cached_tokens,omitempty"`    // Cache read tokens (0.1x price)
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"` // Cache write tokens (1.25x price)
}

// MessagePart message part interface
type MessagePart interface {
	isMessagePart()
}

// TextPart text content part
type TextPart struct {
	Text string `json:"text"`
}

func (TextPart) isMessagePart() {}

// ImageURLPart image URL part
type ImageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // "low", "high", "auto"
}

func (ImageURLPart) isMessagePart() {}

// ImageDataPart image data part
type ImageDataPart struct {
	Data     []byte `json:"data"`
	MIMEType string `json:"mime_type"`
}

func (ImageDataPart) isMessagePart() {}

// ToolCall tool call
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction tool function
type ToolFunction struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// StreamMessage streaming message
type StreamMessage struct {
	Type      string     `json:"type"` // "chunk", "end", "error", "tool_calls", "reasoning"
	Content   string     `json:"content,omitempty"`
	Reasoning string     `json:"reasoning,omitempty"` // Thinking/reasoning content
	Error     string     `json:"error,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Usage     *Usage     `json:"usage,omitempty"`
}

// ReasoningMessage represents a message with reasoning/thinking content
type ReasoningMessage struct {
	Content   string `json:"content"`
	Reasoning string `json:"reasoning,omitempty"`
}

// MemoryProvider memory system interface
type MemoryProvider interface {
	// Load memory variables
	LoadMemoryVariables(ctx context.Context) (map[string]interface{}, error)

	// Save context
	SaveContext(ctx context.Context, input, output map[string]interface{}) error

	// Clear memory
	Clear(ctx context.Context) error

	// GetChatHistory returns history sized for LLM context: implementations MUST window (e.g. match GetMessages(limit<=0): maxHistoryMessages cap + optional summary). Returning the full unbounded store will blow prompts; the engine does not apply a second message-count cut. Optional: implement StoredMessageCount(ctx) for compress gating (see agent/engine).
	GetChatHistory(ctx context.Context) ([]Message, error)

	// Compress memory (optional, for memory compression)
	CompressMemory(ctx context.Context, llm LLMProvider, maxMessages int) error
}

type MemoryReplay interface {
	ReplayMessages(ctx context.Context, messages []Message) error
}

// OutputParser output parser interface
type OutputParser interface {
	Parse(output string) (interface{}, error)
	GetFormatInstructions() string
}
