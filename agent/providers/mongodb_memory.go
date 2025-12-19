package providers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/mongodb"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type MessageDocument struct {
	ID        primitive.ObjectID `bson:"_id,omitempty"`
	SessionID string             `bson:"session_id"`
	Role      string             `bson:"role"`
	Content   string             `bson:"content"`
	Name      string             `bson:"name,omitempty"`
	CreatedAt time.Time          `bson:"created_at"`
}

type MongoDBMemoryProvider struct {
	mu                 sync.RWMutex
	client             *mongodb.Client
	sessionID          string
	maxHistoryMessages int
	collectionName     string
}

func NewMongoDBMemoryProvider(client *mongodb.Client, sessionID string) *MongoDBMemoryProvider {
	return &MongoDBMemoryProvider{
		client:             client,
		sessionID:          sessionID,
		maxHistoryMessages: 100,
		collectionName:     "chat_messages",
	}
}

func NewMongoDBMemoryProviderWithLimit(client *mongodb.Client, sessionID string, maxHistoryMessages int) *MongoDBMemoryProvider {
	return &MongoDBMemoryProvider{
		client:             client,
		sessionID:          sessionID,
		maxHistoryMessages: maxHistoryMessages,
		collectionName:     "chat_messages",
	}
}

func (p *MongoDBMemoryProvider) SetMaxHistoryMessages(limit int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxHistoryMessages = limit
}

func (p *MongoDBMemoryProvider) SetCollectionName(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.collectionName = name
}

func (p *MongoDBMemoryProvider) getCollection() *mongodb.Client {
	p.mu.RLock()
	collectionName := p.collectionName
	p.mu.RUnlock()
	return p.client.Collection(collectionName)
}

func (p *MongoDBMemoryProvider) AddMessage(ctx context.Context, message types.Message) error {
	p.mu.RLock()
	sessionID := p.sessionID
	maxHistoryMessages := p.maxHistoryMessages
	p.mu.RUnlock()

	doc := MessageDocument{
		SessionID: sessionID,
		Role:      message.Role,
		Content:   message.Content,
		Name:      message.Name,
		CreatedAt: time.Now(),
	}
	_, err := p.getCollection().InsertOne(ctx, doc)
	if err != nil {
		return err
	}

	if maxHistoryMessages > 0 {
		return p.trimHistory(ctx)
	}
	return nil
}

func (p *MongoDBMemoryProvider) GetMessages(ctx context.Context, limit int) ([]types.Message, error) {
	p.mu.RLock()
	sessionID := p.sessionID
	maxHistoryMessages := p.maxHistoryMessages
	p.mu.RUnlock()

	filter := bson.M{"session_id": sessionID}
	var docs []MessageDocument

	queryLimit := limit
	if queryLimit <= 0 {
		queryLimit = maxHistoryMessages
		if queryLimit <= 0 {
			queryLimit = 1000
		}
	}

	sort := []string{"created_at"}
	_, err := p.getCollection().QueryByPaging(ctx, filter, sort, 1, int64(queryLimit), &docs)
	if err != nil {
		return nil, err
	}

	messages := make([]types.Message, 0, len(docs))
	for _, doc := range docs {
		messages = append(messages, types.Message{
			Role:    doc.Role,
			Content: doc.Content,
			Name:    doc.Name,
		})
	}

	return messages, nil
}

func (p *MongoDBMemoryProvider) LoadMemoryVariables() (map[string]interface{}, error) {
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

func (p *MongoDBMemoryProvider) SaveContext(input, output map[string]interface{}) error {
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

func (p *MongoDBMemoryProvider) Clear() error {
	ctx := context.Background()
	filter := bson.M{"session_id": p.sessionID}
	return p.getCollection().DeleteAll(ctx, filter)
}

func (p *MongoDBMemoryProvider) GetChatHistory() ([]types.Message, error) {
	ctx := context.Background()
	p.mu.RLock()
	maxHistoryMessages := p.maxHistoryMessages
	p.mu.RUnlock()
	return p.GetMessages(ctx, maxHistoryMessages)
}

func (p *MongoDBMemoryProvider) trimHistory(ctx context.Context) error {
	p.mu.RLock()
	maxHistoryMessages := p.maxHistoryMessages
	sessionID := p.sessionID
	p.mu.RUnlock()

	if maxHistoryMessages <= 0 {
		return nil
	}

	filter := bson.M{"session_id": sessionID}
	sort := []string{"created_at"}
	var docs []MessageDocument
	totalCount, err := p.getCollection().QueryByPaging(ctx, filter, sort, 1, int64(maxHistoryMessages), &docs)
	if err != nil {
		return err
	}

	if totalCount <= int64(maxHistoryMessages) {
		return nil
	}

	if len(docs) > 0 {
		oldestKeptDoc := docs[0]
		deleteFilter := bson.M{
			"session_id": sessionID,
			"created_at": bson.M{"$lt": oldestKeptDoc.CreatedAt},
		}
		return p.getCollection().DeleteAll(ctx, deleteFilter)
	}

	return nil
}

// CompressMemory compresses old messages into a summary (implements MemoryProvider interface)
func (p *MongoDBMemoryProvider) CompressMemory(llm types.LLMProvider, maxMessages int) error {
	if llm == nil {
		return fmt.Errorf("LLM provider is required for memory compression")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	ctx := context.Background()
	sessionID := p.sessionID
	collectionName := p.collectionName
	collection := p.client.Collection(collectionName)
	filter := bson.M{"session_id": sessionID}
	var docs []MessageDocument
	sort := []string{"created_at"}
	_, err := collection.QueryByPaging(ctx, filter, sort, 1, 10000, &docs)
	if err != nil {
		return err
	}

	messages := make([]types.Message, 0, len(docs))
	for _, doc := range docs {
		messages = append(messages, types.Message{
			Role:    doc.Role,
			Content: doc.Content,
			Name:    doc.Name,
		})
	}

	if len(messages) <= maxMessages {
		return nil
	}

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

	compressedMessages := make([]MessageDocument, 0, len(systemMessages)+1+len(recentMessages))
	now := time.Now()

	for _, msg := range systemMessages {
		compressedMessages = append(compressedMessages, MessageDocument{
			SessionID: p.sessionID,
			Role:      msg.Role,
			Content:   msg.Content,
			Name:      msg.Name,
			CreatedAt: now,
		})
	}

	compressedMessages = append(compressedMessages, MessageDocument{
		SessionID: p.sessionID,
		Role:      "system",
		Content:   fmt.Sprintf("Previous conversation summary: %s", summaryMsg.Content),
		CreatedAt: now,
	})

	for _, msg := range recentMessages {
		compressedMessages = append(compressedMessages, MessageDocument{
			SessionID: p.sessionID,
			Role:      msg.Role,
			Content:   msg.Content,
			Name:      msg.Name,
			CreatedAt: now,
		})
	}

	insertData := make([]interface{}, len(compressedMessages))
	for i := range compressedMessages {
		insertData[i] = compressedMessages[i]
	}

	if err := collection.Insert(ctx, insertData); err != nil {
		return fmt.Errorf("failed to insert compressed messages: %w", err)
	}

	var insertedDocs []MessageDocument
	countFilter := bson.M{"session_id": p.sessionID, "created_at": now}
	_, err = collection.QueryByPaging(ctx, countFilter, []string{"created_at"}, 1, int64(len(compressedMessages)), &insertedDocs)
	if err != nil || len(insertedDocs) < len(compressedMessages) {
		collection.DeleteAll(ctx, bson.M{"session_id": p.sessionID, "created_at": now})
		return fmt.Errorf("failed to verify compressed messages insertion, rolled back")
	}

	deleteFilter := bson.M{
		"session_id": p.sessionID,
		"created_at": bson.M{"$lt": now},
	}

	if err := collection.DeleteAll(ctx, deleteFilter); err != nil {
		collection.DeleteAll(ctx, bson.M{"session_id": p.sessionID, "created_at": now})
		return fmt.Errorf("failed to delete old messages after compression, rolled back: %w", err)
	}

	return nil
}
