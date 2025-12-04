package providers

import (
	"context"
	"sync"

	"github.com/xichan96/cortex/agent/types"
)

// SimpleMemoryProvider simple memory provider implementation
type SimpleMemoryProvider struct {
	mu                 sync.RWMutex
	messages           []types.Message
	maxHistoryMessages int
}

// NewSimpleMemoryProvider creates a new simple memory provider
func NewSimpleMemoryProvider() *SimpleMemoryProvider {
	return &SimpleMemoryProvider{
		messages:           make([]types.Message, 0),
		maxHistoryMessages: 100,
	}
}

// NewSimpleMemoryProviderWithLimit creates a new simple memory provider with max history limit
func NewSimpleMemoryProviderWithLimit(maxHistoryMessages int) *SimpleMemoryProvider {
	return &SimpleMemoryProvider{
		messages:           make([]types.Message, 0),
		maxHistoryMessages: maxHistoryMessages,
	}
}

// SetMaxHistoryMessages sets the maximum history messages limit
func (p *SimpleMemoryProvider) SetMaxHistoryMessages(limit int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxHistoryMessages = limit
	if limit > 0 && len(p.messages) > limit {
		p.messages = p.messages[len(p.messages)-limit:]
	}
}

// AddMessage adds a message
func (p *SimpleMemoryProvider) AddMessage(ctx context.Context, message types.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, message)
	if p.maxHistoryMessages > 0 && len(p.messages) > p.maxHistoryMessages {
		p.messages = p.messages[len(p.messages)-p.maxHistoryMessages:]
	}
	return nil
}

// GetMessages gets messages
func (p *SimpleMemoryProvider) GetMessages(ctx context.Context, limit int) ([]types.Message, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if limit <= 0 || limit >= len(p.messages) {
		messages := make([]types.Message, len(p.messages))
		copy(messages, p.messages)
		return messages, nil
	}
	start := len(p.messages) - limit
	messages := make([]types.Message, limit)
	copy(messages, p.messages[start:])
	return messages, nil
}

// LoadMemoryVariables loads memory variables (implements MemoryProvider interface)
func (p *SimpleMemoryProvider) LoadMemoryVariables() (map[string]interface{}, error) {
	p.mu.RLock()
	messages := make([]types.Message, len(p.messages))
	copy(messages, p.messages)
	maxHistoryMessages := p.maxHistoryMessages
	p.mu.RUnlock()
	if maxHistoryMessages > 0 && len(messages) > maxHistoryMessages {
		messages = messages[len(messages)-maxHistoryMessages:]
	}
	return map[string]interface{}{
		"history": messages,
	}, nil
}

// SaveContext saves context (implements MemoryProvider interface)
func (p *SimpleMemoryProvider) SaveContext(input, output map[string]interface{}) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if inputMsg, ok := input["input"].(string); ok {
		p.messages = append(p.messages, types.Message{
			Role:    "user",
			Content: inputMsg,
		})
		if p.maxHistoryMessages > 0 && len(p.messages) > p.maxHistoryMessages {
			p.messages = p.messages[len(p.messages)-p.maxHistoryMessages:]
		}
	}
	if outputMsg, ok := output["output"].(string); ok {
		p.messages = append(p.messages, types.Message{
			Role:    "assistant",
			Content: outputMsg,
		})
		if p.maxHistoryMessages > 0 && len(p.messages) > p.maxHistoryMessages {
			p.messages = p.messages[len(p.messages)-p.maxHistoryMessages:]
		}
	}
	return nil
}

// Clear clears memory (implements MemoryProvider interface)
func (p *SimpleMemoryProvider) Clear() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = make([]types.Message, 0)
	return nil
}

// ClearWithContext clears memory with context (for backward compatibility)
func (p *SimpleMemoryProvider) ClearWithContext(ctx context.Context) error {
	return p.Clear()
}

// GetChatHistory gets chat history (implements MemoryProvider interface)
func (p *SimpleMemoryProvider) GetChatHistory() ([]types.Message, error) {
	p.mu.RLock()
	messages := make([]types.Message, len(p.messages))
	copy(messages, p.messages)
	maxHistoryMessages := p.maxHistoryMessages
	p.mu.RUnlock()
	if maxHistoryMessages > 0 && len(messages) > maxHistoryMessages {
		messages = messages[len(messages)-maxHistoryMessages:]
	}
	return messages, nil
}
