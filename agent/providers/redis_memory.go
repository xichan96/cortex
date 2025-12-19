package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/redis"
)

type RedisMemoryProvider struct {
	mu                 sync.RWMutex
	client             *redis.Client
	sessionID          string
	maxHistoryMessages int
	keyPrefix          string
}

func NewRedisMemoryProvider(client *redis.Client, sessionID string) *RedisMemoryProvider {
	return &RedisMemoryProvider{
		client:             client,
		sessionID:          sessionID,
		maxHistoryMessages: 100,
		keyPrefix:          "chat_messages",
	}
}

func NewRedisMemoryProviderWithLimit(client *redis.Client, sessionID string, maxHistoryMessages int) *RedisMemoryProvider {
	return &RedisMemoryProvider{
		client:             client,
		sessionID:          sessionID,
		maxHistoryMessages: maxHistoryMessages,
		keyPrefix:          "chat_messages",
	}
}

func (p *RedisMemoryProvider) SetMaxHistoryMessages(limit int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxHistoryMessages = limit
}

func (p *RedisMemoryProvider) SetKeyPrefix(prefix string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keyPrefix = prefix
}

func (p *RedisMemoryProvider) getKey() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.keyPrefix + ":" + p.sessionID
}

func (p *RedisMemoryProvider) AddMessage(ctx context.Context, message types.Message) error {
	msgData := map[string]interface{}{
		"role":       message.Role,
		"content":    message.Content,
		"name":       message.Name,
		"created_at": time.Now().Unix(),
	}

	msgJSON, err := json.Marshal(msgData)
	if err != nil {
		return err
	}

	key := p.getKey()
	if err := p.client.LPush(ctx, key, msgJSON).Err(); err != nil {
		return err
	}

	if p.maxHistoryMessages > 0 {
		return p.trimHistory(ctx)
	}
	return nil
}

func (p *RedisMemoryProvider) GetMessages(ctx context.Context, limit int) ([]types.Message, error) {
	p.mu.RLock()
	maxHistoryMessages := p.maxHistoryMessages
	p.mu.RUnlock()

	queryLimit := limit
	if queryLimit <= 0 {
		queryLimit = maxHistoryMessages
		if queryLimit <= 0 {
			queryLimit = 1000
		}
	}

	key := p.getKey()
	results, err := p.client.LRange(ctx, key, 0, int64(queryLimit-1)).Result()
	if err != nil {
		return nil, err
	}

	messages := make([]types.Message, 0, len(results))
	for i := len(results) - 1; i >= 0; i-- {
		var msgData map[string]interface{}
		if err := json.Unmarshal([]byte(results[i]), &msgData); err != nil {
			continue
		}

		role, _ := msgData["role"].(string)
		content, _ := msgData["content"].(string)
		name, _ := msgData["name"].(string)

		messages = append(messages, types.Message{
			Role:    role,
			Content: content,
			Name:    name,
		})
	}

	return messages, nil
}

func (p *RedisMemoryProvider) LoadMemoryVariables() (map[string]interface{}, error) {
	ctx := context.Background()
	p.mu.RLock()
	maxHistoryMessages := p.maxHistoryMessages
	p.mu.RUnlock()
	messages, err := p.GetMessages(ctx, maxHistoryMessages)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"history": messages,
	}, nil
}

func (p *RedisMemoryProvider) SaveContext(input, output map[string]interface{}) error {
	ctx := context.Background()
	if inputMsg, ok := input["input"].(string); ok {
		if err := p.AddMessage(ctx, types.Message{
			Role:    "user",
			Content: inputMsg,
		}); err != nil {
			return err
		}
	}
	if outputMsg, ok := output["output"].(string); ok {
		if err := p.AddMessage(ctx, types.Message{
			Role:    "assistant",
			Content: outputMsg,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (p *RedisMemoryProvider) Clear() error {
	ctx := context.Background()
	key := p.getKey()
	return p.client.Del(ctx, key).Err()
}

func (p *RedisMemoryProvider) GetChatHistory() ([]types.Message, error) {
	ctx := context.Background()
	p.mu.RLock()
	maxHistoryMessages := p.maxHistoryMessages
	p.mu.RUnlock()
	return p.GetMessages(ctx, maxHistoryMessages)
}

func (p *RedisMemoryProvider) trimHistory(ctx context.Context) error {
	p.mu.RLock()
	maxHistoryMessages := p.maxHistoryMessages
	p.mu.RUnlock()

	if maxHistoryMessages <= 0 {
		return nil
	}

	key := p.getKey()
	return p.client.LTrim(ctx, key, 0, int64(maxHistoryMessages-1)).Err()
}

// CompressMemory compresses old messages into a summary (implements MemoryProvider interface)
func (p *RedisMemoryProvider) CompressMemory(llm types.LLMProvider, maxMessages int) error {
	if llm == nil {
		return fmt.Errorf("LLM provider is required for memory compression")
	}

	ctx := context.Background()
	messages, err := p.GetChatHistory()
	if err != nil {
		return err
	}

	if len(messages) <= maxMessages {
		return nil
	}

	// Keep system messages and recent messages
	systemMessages := make([]types.Message, 0)
	recentMessages := make([]types.Message, 0)
	oldMessages := make([]types.Message, 0)

	for i, msg := range messages {
		if msg.Role == "system" {
			systemMessages = append(systemMessages, msg)
		} else if i < len(messages)-maxMessages {
			oldMessages = append(oldMessages, msg)
		} else {
			recentMessages = append(recentMessages, msg)
		}
	}

	if len(oldMessages) == 0 {
		return nil
	}

	// Generate summary of old messages
	summaryPrompt := "Please provide a concise summary of the following conversation history, preserving key information and context:\n\n"
	for _, msg := range oldMessages {
		summaryPrompt += fmt.Sprintf("%s: %s\n", msg.Role, msg.Content)
	}

	summaryMsg, err := llm.Chat([]types.Message{
		{
			Role:    "system",
			Content: "You are a helpful assistant that summarizes conversation history while preserving important context and key information.",
		},
		{
			Role:    "user",
			Content: summaryPrompt,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to generate memory summary: %w", err)
	}

	// Prepare compressed messages
	compressedMessages := make([]types.Message, 0, len(systemMessages)+1+len(recentMessages))
	compressedMessages = append(compressedMessages, systemMessages...)
	compressedMessages = append(compressedMessages, types.Message{
		Role:    "system",
		Content: fmt.Sprintf("Previous conversation summary: %s", summaryMsg.Content),
	})
	compressedMessages = append(compressedMessages, recentMessages...)

	// Use temporary key for atomic replacement
	tempKey := p.getKey() + ":temp:" + fmt.Sprintf("%d", time.Now().UnixNano())
	key := p.getKey()

	// Insert compressed messages to temporary key first
	for i := len(compressedMessages) - 1; i >= 0; i-- {
		msg := compressedMessages[i]
		msgData := map[string]interface{}{
			"role":       msg.Role,
			"content":    msg.Content,
			"name":       msg.Name,
			"created_at": time.Now().Unix(),
		}
		msgJSON, err := json.Marshal(msgData)
		if err != nil {
			// Clean up temp key on error
			p.client.Del(ctx, tempKey)
			return fmt.Errorf("failed to marshal message: %w", err)
		}
		if err := p.client.LPush(ctx, tempKey, msgJSON).Err(); err != nil {
			// Clean up temp key on error
			p.client.Del(ctx, tempKey)
			return fmt.Errorf("failed to insert compressed message to temp key: %w", err)
		}
	}

	// Verify temp key has correct number of messages
	tempCount, err := p.client.LLen(ctx, tempKey).Result()
	if err != nil || tempCount != int64(len(compressedMessages)) {
		// Clean up temp key on verification failure
		p.client.Del(ctx, tempKey)
		return fmt.Errorf("failed to verify compressed messages in temp key")
	}

	// Atomically replace old key with temp key using RENAME
	// This is atomic in Redis - either succeeds or fails, no partial state
	if err := p.client.Rename(ctx, tempKey, key).Err(); err != nil {
		// If rename fails, clean up temp key
		p.client.Del(ctx, tempKey)
		return fmt.Errorf("failed to atomically replace messages: %w", err)
	}

	// Apply max history limit if needed
	if p.maxHistoryMessages > 0 {
		if err := p.trimHistory(ctx); err != nil {
			// Log but don't fail - data is already compressed
			return fmt.Errorf("failed to trim history after compression: %w", err)
		}
	}

	return nil
}
