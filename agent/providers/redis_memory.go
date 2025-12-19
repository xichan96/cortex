package providers

import (
	"context"
	"encoding/json"
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
