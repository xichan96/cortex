package providers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/middle/mongodb"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type MessageDocument struct {
	ID        primitive.ObjectID `bson:"_id,omitempty"`
	SessionID string             `bson:"session_id"`
	Role      string             `bson:"role"`
	Content   string             `bson:"content"`
	Name      string             `bson:"name,omitempty"`
	Parts     string             `bson:"parts,omitempty"`
	CreatedAt time.Time          `bson:"created_at"`
}

type SummaryDocument struct {
	ID                      primitive.ObjectID `bson:"_id,omitempty"`
	SessionID               string             `bson:"session_id"`
	Content                 string             `bson:"content"`
	LastSummarizedMessageID primitive.ObjectID `bson:"last_summarized_message_id"`
	CreatedAt               time.Time          `bson:"created_at"`
	UpdatedAt               time.Time          `bson:"updated_at"`
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
	p.mu.RUnlock()

	partsJSON, _ := types.SerializeMessageParts(message.Parts)

	doc := MessageDocument{
		SessionID: sessionID,
		Role:      message.Role,
		Content:   message.Content,
		Name:      message.Name,
		Parts:     partsJSON,
		CreatedAt: time.Now(),
	}
	_, err := p.getCollection().InsertOne(ctx, doc)
	if err != nil {
		return err
	}

	return nil
}

func (p *MongoDBMemoryProvider) GetMessages(ctx context.Context, limit int) ([]types.Message, error) {
	p.mu.RLock()
	sessionID := p.sessionID
	maxHistoryMessages := p.maxHistoryMessages
	p.mu.RUnlock()

	// 1. Fetch summary
	var summaryDoc SummaryDocument
	summaryCollection := p.client.Collection("chat_summaries")
	err := summaryCollection.FindOne(ctx, bson.M{"session_id": sessionID}, &summaryDoc)
	hasSummary := err == nil && summaryDoc.Content != ""

	// 2. Fetch recent messages
	queryLimit := limit
	if queryLimit <= 0 {
		queryLimit = maxHistoryMessages
		if queryLimit <= 0 {
			queryLimit = 1000
		}
	}

	filter := bson.M{"session_id": sessionID}
	var docs []MessageDocument

	// Sort by created_at DESC to get the most recent messages
	sort := []string{"-created_at"}
	_, err = p.getCollection().QueryByPaging(ctx, filter, sort, 1, int64(queryLimit), &docs)
	if err != nil {
		return nil, err
	}

	messages := make([]types.Message, 0, len(docs)+1)

	// Add summary if exists
	if hasSummary {
		messages = append(messages, types.Message{
			Role:    "system",
			Content: fmt.Sprintf("Previous conversation summary: %s", summaryDoc.Content),
		})
	}

	// Reverse docs to get chronological order (Oldest -> Newest)
	for i := len(docs) - 1; i >= 0; i-- {
		doc := docs[i]
		parts, _ := types.DeserializeMessageParts(doc.Parts)
		messages = append(messages, types.Message{
			Role:    doc.Role,
			Content: doc.Content,
			Name:    doc.Name,
			Parts:   parts,
		})
	}

	return messages, nil
}

func (p *MongoDBMemoryProvider) LoadMemoryVariables(ctx context.Context) (map[string]interface{}, error) {
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

func (p *MongoDBMemoryProvider) SaveContext(ctx context.Context, input, output map[string]interface{}) error {
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
	if outputMsg, ok := output["output"].(string); ok {
		role, _ := output["role"].(string)
		if role == "" {
			role = "assistant"
		}
		if err := p.AddMessage(ctx, types.Message{
			Role:    role,
			Content: outputMsg,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (p *MongoDBMemoryProvider) Clear(ctx context.Context) error {
	filter := bson.M{"session_id": p.sessionID}
	return p.getCollection().DeleteAll(ctx, filter)
}

func (p *MongoDBMemoryProvider) GetChatHistory(ctx context.Context) ([]types.Message, error) {
	p.mu.RLock()
	sessionID := p.sessionID
	p.mu.RUnlock()

	filter := bson.M{"session_id": sessionID}
	var docs []MessageDocument
	// Get all messages, sorted by created_at ASC
	sort := []string{"created_at"}
	_, err := p.getCollection().QueryByPaging(ctx, filter, sort, 1, 100000, &docs) // High limit to get all
	if err != nil {
		return nil, err
	}

	messages := make([]types.Message, 0, len(docs))
	for _, doc := range docs {
		parts, _ := types.DeserializeMessageParts(doc.Parts)
		messages = append(messages, types.Message{
			Role:    doc.Role,
			Content: doc.Content,
			Name:    doc.Name,
			Parts:   parts,
		})
	}
	return messages, nil
}

// CompressMemory compresses old messages into a summary (implements MemoryProvider interface)
func (p *MongoDBMemoryProvider) CompressMemory(ctx context.Context, llm types.LLMProvider, maxMessages int) error {
	if llm == nil {
		return fmt.Errorf("LLM provider is required for memory compression")
	}

	p.mu.Lock()

	sessionID := p.sessionID

	// 1. Get current summary state
	var summaryDoc SummaryDocument
	summaryCollection := p.client.Collection("chat_summaries")
	err := summaryCollection.FindOne(ctx, bson.M{"session_id": sessionID}, &summaryDoc)

	var currentSummary string

	if err == nil {
		// lastSummarizedTime = summaryDoc.CreatedAt
	}

	var lastSummarizedTimestamp time.Time
	if !summaryDoc.LastSummarizedMessageID.IsZero() {
		var lastMsg MessageDocument
		if err := p.getCollection().FindOne(ctx, bson.M{"_id": summaryDoc.LastSummarizedMessageID}, &lastMsg); err == nil {
			lastSummarizedTimestamp = lastMsg.CreatedAt
		}
	}

	if err == nil {
		currentSummary = summaryDoc.Content
	}

	// 2. Get unsummarized messages
	// Filter: session_id AND created_at > lastSummarizedTimestamp
	filter := bson.M{
		"session_id": sessionID,
		"created_at": bson.M{"$gt": lastSummarizedTimestamp},
	}
	sort := []string{"created_at"}
	var unsummarizedDocs []MessageDocument
	// Fetch all potential unsummarized messages
	_, err = p.getCollection().QueryByPaging(ctx, filter, sort, 1, 10000, &unsummarizedDocs)
	if err != nil {
		p.mu.Unlock()
		return err
	}

	if len(unsummarizedDocs) <= maxMessages {
		p.mu.Unlock()
		return nil // Nothing to summarize, all fit in recent window
	}

	// We have more messages than maxMessages.
	// We want to keep the last `maxMessages` as raw.
	// So we summarize the first `len - maxMessages`.
	numToSummarize := len(unsummarizedDocs) - maxMessages
	docsToSummarize := unsummarizedDocs[:numToSummarize]

	// 3. Generate summary
	newContent := ""
	for _, doc := range docsToSummarize {
		newContent += fmt.Sprintf("%s: %s\n", doc.Role, doc.Content)
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

	// 4. Save summary
	p.mu.Lock()
	defer p.mu.Unlock()
	lastDoc := docsToSummarize[len(docsToSummarize)-1]

	newSummaryDoc := SummaryDocument{
		SessionID:               sessionID,
		Content:                 summaryMsg.Content,
		LastSummarizedMessageID: lastDoc.ID,
		UpdatedAt:               time.Now(),
		CreatedAt:               time.Now(), // Only for new doc
	}

	if summaryDoc.ID.IsZero() {
		// Insert new
		_, err = summaryCollection.InsertOne(ctx, newSummaryDoc)
	} else {
		// Update existing
		newSummaryDoc.CreatedAt = summaryDoc.CreatedAt // Preserve original creation time
		err = summaryCollection.Update(ctx, bson.M{"_id": summaryDoc.ID}, bson.M{
			"content":                    summaryMsg.Content,
			"last_summarized_message_id": lastDoc.ID,
			"updated_at":                 time.Now(),
		})
	}

	return err
}
