package providers

import (
	"context"

	"github.com/xichan96/cortex/agent/types"
)

// SimpleMemoryProvider simple memory provider implementation
type SimpleMemoryProvider struct {
	messages []types.Message
}

// NewSimpleMemoryProvider creates a new simple memory provider
func NewSimpleMemoryProvider() *SimpleMemoryProvider {
	return &SimpleMemoryProvider{
		messages: make([]types.Message, 0),
	}
}

// AddMessage adds a message
func (p *SimpleMemoryProvider) AddMessage(ctx context.Context, message types.Message) error {
	p.messages = append(p.messages, message)
	return nil
}

// GetMessages gets messages
func (p *SimpleMemoryProvider) GetMessages(ctx context.Context, limit int) ([]types.Message, error) {
	if limit <= 0 || limit >= len(p.messages) {
		return p.messages, nil
	}

	// Return the latest limit messages
	start := len(p.messages) - limit
	return p.messages[start:], nil
}

// Clear clears memory
func (p *SimpleMemoryProvider) Clear(ctx context.Context) error {
	p.messages = make([]types.Message, 0)
	return nil
}
