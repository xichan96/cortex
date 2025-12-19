package types

// LLMProvider defines LLM provider interface
type LLMProvider interface {
	// Basic chat functionality
	Chat(messages []Message) (Message, error)
	ChatStream(messages []Message) (<-chan StreamMessage, error)

	// Tool call support
	ChatWithTools(messages []Message, tools []Tool) (Message, error)
	ChatWithToolsStream(messages []Message, tools []Tool) (<-chan StreamMessage, error)

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
	Type      string     `json:"type"` // "chunk", "end", "error", "tool_calls"
	Content   string     `json:"content,omitempty"`
	Error     string     `json:"error,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// MemoryProvider memory system interface
type MemoryProvider interface {
	// Load memory variables
	LoadMemoryVariables() (map[string]interface{}, error)

	// Save context
	SaveContext(input, output map[string]interface{}) error

	// Clear memory
	Clear() error

	// Get chat history
	GetChatHistory() ([]Message, error)

	// Compress memory (optional, for memory compression)
	CompressMemory(llm LLMProvider, maxMessages int) error
}

// OutputParser output parser interface
type OutputParser interface {
	Parse(output string) (interface{}, error)
	GetFormatInstructions() string
}
