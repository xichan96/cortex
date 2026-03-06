package providers

import (
	"context"
	"fmt"
	"sync"

	"github.com/xichan96/cortex/agent/types"
)

// SimpleMemoryProvider simple memory provider implementation
type SimpleMemoryProvider struct {
	mu                 sync.RWMutex
	messages           []types.Message
	maxHistoryMessages int
	summary            string
	summaryIdx         int // Index of the last message included in the summary
}

// NewSimpleMemoryProvider creates a new simple memory provider
func NewSimpleMemoryProvider() *SimpleMemoryProvider {
	return &SimpleMemoryProvider{
		messages:           make([]types.Message, 0),
		maxHistoryMessages: 100,
		summaryIdx:         -1,
	}
}

// NewSimpleMemoryProviderWithLimit creates a new simple memory provider with max history limit
func NewSimpleMemoryProviderWithLimit(maxHistoryMessages int) *SimpleMemoryProvider {
	return &SimpleMemoryProvider{
		messages:           make([]types.Message, 0),
		maxHistoryMessages: maxHistoryMessages,
		summaryIdx:         -1,
	}
}

// SetMaxHistoryMessages sets the maximum history messages limit
func (p *SimpleMemoryProvider) SetMaxHistoryMessages(limit int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxHistoryMessages = limit
}

// AddMessage adds a message
func (p *SimpleMemoryProvider) AddMessage(ctx context.Context, message types.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, message)
	return nil
}

// GetMessages gets messages
func (p *SimpleMemoryProvider) GetMessages(ctx context.Context, limit int) ([]types.Message, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	queryLimit := limit
	if queryLimit <= 0 {
		queryLimit = p.maxHistoryMessages
		if queryLimit <= 0 {
			queryLimit = 1000
		}
	}

	totalMessages := len(p.messages)

	// If total messages fit in limit, just return them (plus summary if exists)
	if totalMessages <= queryLimit {
		messages := make([]types.Message, 0, totalMessages+1)
		if p.summary != "" {
			messages = append(messages, types.Message{
				Role:    "system",
				Content: fmt.Sprintf("Previous conversation summary: %s", p.summary),
			})
		}
		messages = append(messages, p.messages...)
		return messages, nil
	}

	// Otherwise, return summary + recent messages
	messages := make([]types.Message, 0, queryLimit+1)
	if p.summary != "" {
		messages = append(messages, types.Message{
			Role:    "system",
			Content: fmt.Sprintf("Previous conversation summary: %s", p.summary),
		})
	}

	start := totalMessages - queryLimit
	messages = append(messages, p.messages[start:]...)
	return messages, nil
}

// LoadMemoryVariables loads memory variables (implements MemoryProvider interface)
func (p *SimpleMemoryProvider) LoadMemoryVariables(ctx context.Context) (map[string]interface{}, error) {
	p.mu.RLock()
	limit := p.maxHistoryMessages
	p.mu.RUnlock()

	messages, err := p.GetMessages(ctx, limit)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"history": messages,
	}, nil
}

// SaveContext saves context (implements MemoryProvider interface)
func (p *SimpleMemoryProvider) SaveContext(ctx context.Context, input, output map[string]interface{}) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if inputMsg, ok := input["input"].(string); ok {
		role, _ := input["role"].(string)
		if role == "" {
			role = "user"
		}
		p.messages = append(p.messages, types.Message{
			Role:    role,
			Content: inputMsg,
		})
	}
	if outputMsg, ok := output["output"].(string); ok {
		role, _ := output["role"].(string)
		if role == "" {
			role = "assistant"
		}
		p.messages = append(p.messages, types.Message{
			Role:    role,
			Content: outputMsg,
		})
	}
	return nil
}

// Clear clears memory (implements MemoryProvider interface)
func (p *SimpleMemoryProvider) Clear(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = make([]types.Message, 0)
	p.summary = ""
	p.summaryIdx = -1
	return nil
}

// ClearWithContext clears memory with context (for backward compatibility)
func (p *SimpleMemoryProvider) ClearWithContext(ctx context.Context) error {
	return p.Clear(ctx)
}

// GetChatHistory gets chat history (implements MemoryProvider interface)
func (p *SimpleMemoryProvider) GetChatHistory(ctx context.Context) ([]types.Message, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	// Return full history as requested "all conversations will be recorded"
	// For in-memory provider, this is just returning the slice.
	// If the caller needs context-window sized history, they should use GetMessages with limit.
	messages := make([]types.Message, len(p.messages))
	copy(messages, p.messages)
	return messages, nil
}

// CompressMemory compresses old messages into a summary (implements MemoryProvider interface)
func (p *SimpleMemoryProvider) CompressMemory(ctx context.Context, llm types.LLMProvider, maxMessages int) error {
	if llm == nil {
		return fmt.Errorf("LLM provider is required for memory compression")
	}

	p.mu.Lock()

	// 1. Check if compression is needed
	// We want to summarize messages that are outside the window of 'maxMessages'.
	// But we also need to respect what has already been summarized.
	// p.summaryIdx points to the last message that was included in the summary.

	totalMessages := len(p.messages)
	if totalMessages <= maxMessages {
		p.mu.Unlock()
		return nil
	}

	// Calculate how many new messages need to be summarized
	// We want to keep the last 'maxMessages' untouched.
	// So we summarize up to index: totalMessages - maxMessages - 1
	targetIdx := totalMessages - maxMessages - 1

	if targetIdx <= p.summaryIdx {
		p.mu.Unlock()
		return nil // Nothing new to summarize
	}

	// 2. Identify messages to summarize
	// From (p.summaryIdx + 1) to targetIdx
	msgsToSummarize := p.messages[p.summaryIdx+1 : targetIdx+1]

	if len(msgsToSummarize) == 0 {
		p.mu.Unlock()
		return nil
	}

	// 3. Generate summary
	newContent := ""
	for _, msg := range msgsToSummarize {
		newContent += fmt.Sprintf("%s: %s\n", msg.Role, msg.Content)
	}

	currentSummary := p.summary
	p.mu.Unlock() // Unlock before LLM call

	prompt := fmt.Sprintf(`Current summary of conversation:
%s

New lines of conversation:
%s

Please update the summary to include the new information, keeping it concise but preserving key details.`, currentSummary, newContent)

	if currentSummary == "" {
		prompt = fmt.Sprintf(`Please provide a concise summary of the following conversation history:
%s`, newContent)
	}

	summaryMsg, err := llm.Chat(ctx, []types.Message{
		{
			Role:    "system",
			Content: "You are a helpful assistant that summarizes conversation history.",
		},
		{
			Role:    "user",
			Content: prompt,
		},
	})

	p.mu.Lock() // Lock to update state
	defer p.mu.Unlock()

	if err != nil {
		return fmt.Errorf("failed to generate memory summary: %w", err)
	}

	// 4. Update state
	p.summary = summaryMsg.Content
	p.summaryIdx = targetIdx

	// We DO NOT delete messages from p.messages to preserve full history.

	return nil
}
