package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/xichan96/cortex/agent/types"
	credis "github.com/xichan96/cortex/pkg/middle/redis"
)

type RedisMemoryProvider struct {
	mu                 sync.RWMutex
	client             *credis.Client
	sessionID          string
	maxHistoryMessages int
	keyPrefix          string
}

func NewRedisMemoryProvider(client *credis.Client, sessionID string) *RedisMemoryProvider {
	return &RedisMemoryProvider{
		client:             client,
		sessionID:          sessionID,
		maxHistoryMessages: 100,
		keyPrefix:          "chat_messages",
	}
}

func NewRedisMemoryProviderWithLimit(client *credis.Client, sessionID string, maxHistoryMessages int) *RedisMemoryProvider {
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

	partsJSON, _ := types.SerializeMessageParts(message.Parts)
	if partsJSON != "" {
		msgData["parts"] = partsJSON
	}
	if x := encodeMessageToolExtras(message); x != "" {
		msgData["tool_extras"] = x
	}

	msgJSON, err := json.Marshal(msgData)
	if err != nil {
		return err
	}

	key := p.getKey()
	if err := p.client.LPush(ctx, key, msgJSON).Err(); err != nil {
		return err
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
	// Fetch recent messages (LRange 0 is newest)
	results, err := p.client.LRange(ctx, key, 0, int64(queryLimit-1)).Result()
	if err != nil {
		return nil, err
	}

	messages := make([]types.Message, 0, len(results)+1)

	// Fetch summary
	summaryKey := key + ":summary"
	summaryJSON, err := p.client.Get(ctx, summaryKey).Result()
	if err == nil && summaryJSON != "" {
		var summaryData map[string]interface{}
		if err := json.Unmarshal([]byte(summaryJSON), &summaryData); err == nil {
			if content, ok := summaryData["content"].(string); ok && content != "" {
				messages = append(messages, types.Message{
					Role:    "system",
					Content: fmt.Sprintf("Previous conversation summary: %s", content),
				})
			}
		}
	} else if err != nil && err != redis.Nil { // Ignore key not found
		// Log error or ignore? For memory, maybe best to ignore if just missing
	}

	// Process recent messages (reverse order: LRange returns [newest, ..., oldest])
	// We want chronological: [oldest, ..., newest]
	for i := len(results) - 1; i >= 0; i-- {
		var msgData map[string]interface{}
		if err := json.Unmarshal([]byte(results[i]), &msgData); err != nil {
			continue
		}

		role, _ := msgData["role"].(string)
		content, _ := msgData["content"].(string)
		name, _ := msgData["name"].(string)

		msg := types.Message{
			Role:    role,
			Content: content,
			Name:    name,
		}

		if partsJSON, ok := msgData["parts"].(string); ok && partsJSON != "" {
			parts, _ := types.DeserializeMessageParts(partsJSON)
			msg.Parts = parts
		}
		if x, ok := msgData["tool_extras"].(string); ok {
			applyStoredToolExtras(&msg, x)
		}

		messages = append(messages, msg)
	}

	return messages, nil
}

func (p *RedisMemoryProvider) LoadMemoryVariables(ctx context.Context) (map[string]interface{}, error) {
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

func (p *RedisMemoryProvider) SaveContext(ctx context.Context, input, output map[string]interface{}) error {
	if inputMsg, ok := input["input"].(string); ok {
		role, _ := input["role"].(string)
		if role == "" {
			role = "user"
		}
		msg := types.Message{
			Role:    role,
			Content: inputMsg,
		}
		if parts, ok := input["parts"].([]types.MessagePart); ok {
			msg.Parts = parts
		}
		if err := p.AddMessage(ctx, msg); err != nil {
			return err
		}
	}
	if err := saveOutputWithToolSteps(ctx, p, output); err != nil {
		return err
	}
	return nil
}

func (p *RedisMemoryProvider) Clear(ctx context.Context) error {
	key := p.getKey()
	return p.client.Del(ctx, key).Err()
}

func (p *RedisMemoryProvider) GetChatHistory(ctx context.Context) ([]types.Message, error) {
	key := p.getKey()
	// Fetch all messages (0 to -1)
	results, err := p.client.LRange(ctx, key, 0, -1).Result()
	if err != nil {
		return nil, err
	}

	messages := make([]types.Message, 0, len(results))
	// Reverse order to be chronological
	for i := len(results) - 1; i >= 0; i-- {
		var msgData map[string]interface{}
		if err := json.Unmarshal([]byte(results[i]), &msgData); err != nil {
			continue
		}

		role, _ := msgData["role"].(string)
		content, _ := msgData["content"].(string)
		name, _ := msgData["name"].(string)

		msg := types.Message{
			Role:    role,
			Content: content,
			Name:    name,
		}

		if partsJSON, ok := msgData["parts"].(string); ok && partsJSON != "" {
			parts, _ := types.DeserializeMessageParts(partsJSON)
			msg.Parts = parts
		}
		if x, ok := msgData["tool_extras"].(string); ok {
			applyStoredToolExtras(&msg, x)
		}

		messages = append(messages, msg)
	}

	return messages, nil
}

// CompressMemory compresses old messages into a summary (implements MemoryProvider interface)
func (p *RedisMemoryProvider) CompressMemory(ctx context.Context, llm types.LLMProvider, maxMessages int) error {
	if llm == nil {
		return fmt.Errorf("LLM provider is required for memory compression")
	}

	p.mu.Lock()

	key := p.getKey()
	summaryKey := key + ":summary"

	// 1. Get current summary state
	var lastSummarizedTimestamp int64 = 0
	currentSummary := ""

	summaryJSON, err := p.client.Get(ctx, summaryKey).Result()
	if err == nil && summaryJSON != "" {
		var summaryData map[string]interface{}
		if err := json.Unmarshal([]byte(summaryJSON), &summaryData); err == nil {
			if ts, ok := summaryData["last_summarized_timestamp"].(float64); ok {
				lastSummarizedTimestamp = int64(ts)
			}
			if content, ok := summaryData["content"].(string); ok {
				currentSummary = content
			}
		}
	}

	// 2. Get potentially unsummarized messages (those older than maxMessages)
	// LRange returns [newest, ..., oldest]
	// We start from maxMessages index (skipping the recent ones)
	rawMessages, err := p.client.LRange(ctx, key, int64(maxMessages), -1).Result()
	if err != nil {
		p.mu.Unlock()
		return err
	}

	if len(rawMessages) == 0 {
		p.mu.Unlock()
		return nil // Nothing to summarize
	}

	// 3. Filter messages that are newer than lastSummarizedTimestamp
	var docsToSummarize []map[string]interface{}

	// Iterate from beginning (NewestOld) to end (OldestOld)
	for _, rawMsg := range rawMessages {
		var msgData map[string]interface{}
		if err := json.Unmarshal([]byte(rawMsg), &msgData); err != nil {
			continue
		}

		createdAtVal, ok := msgData["created_at"]
		var createdAt int64
		if ok {
			if ts, ok := createdAtVal.(float64); ok {
				createdAt = int64(ts)
			} else if ts, ok := createdAtVal.(int64); ok {
				createdAt = ts
			}
		}

		if createdAt > lastSummarizedTimestamp {
			docsToSummarize = append(docsToSummarize, msgData)
		} else {
			// Found a message that is already summarized or older.
			// Since list is ordered (newest first), all subsequent messages are also older.
			break
		}
	}

	if len(docsToSummarize) == 0 {
		p.mu.Unlock()
		return nil
	}

	// docsToSummarize is currently [NewestOld, ..., OldestUnsummarized]
	// We need chronological order [OldestUnsummarized, ..., NewestOld] for summarization
	// Reverse the slice
	for i, j := 0, len(docsToSummarize)-1; i < j; i, j = i+1, j-1 {
		docsToSummarize[i], docsToSummarize[j] = docsToSummarize[j], docsToSummarize[i]
	}

	// 4. Generate summary
	newContent := ""
	for _, doc := range docsToSummarize {
		role, _ := doc["role"].(string)
		content, _ := doc["content"].(string)
		newContent += fmt.Sprintf("%s: %s\n", role, content)
	}
	p.mu.Unlock()

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
	if err != nil {
		return fmt.Errorf("failed to generate memory summary: %w", err)
	}

	// 5. Update summary in Redis
	p.mu.Lock()
	defer p.mu.Unlock()
	// New timestamp is the timestamp of the newest message we just summarized (which is the last in docsToSummarize)
	newestSummarizedDoc := docsToSummarize[len(docsToSummarize)-1]
	var newTimestamp int64
	if ts, ok := newestSummarizedDoc["created_at"].(float64); ok {
		newTimestamp = int64(ts)
	} else if ts, ok := newestSummarizedDoc["created_at"].(int64); ok {
		newTimestamp = ts
	} else {
		newTimestamp = time.Now().Unix() // Fallback
	}

	newSummaryData := map[string]interface{}{
		"content":                   summaryMsg.Content,
		"last_summarized_timestamp": newTimestamp,
		"updated_at":                time.Now().Unix(),
	}

	newSummaryJSON, err := json.Marshal(newSummaryData)
	if err != nil {
		return fmt.Errorf("failed to marshal summary data: %w", err)
	}

	if err := p.client.Set(ctx, summaryKey, newSummaryJSON, 0).Err(); err != nil {
		return fmt.Errorf("failed to save summary to Redis: %w", err)
	}

	return nil
}
