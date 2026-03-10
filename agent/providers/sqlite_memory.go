package providers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/middle/sql/sqlite"
	"gorm.io/gorm"
)

type SQLiteMessageDocument struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID string    `gorm:"type:varchar(255);index;not null" json:"session_id"`
	Role      string    `gorm:"type:varchar(50);not null" json:"role"`
	Content   string    `gorm:"type:text" json:"content"`
	Name      string    `gorm:"type:varchar(255)" json:"name,omitempty"`
	Parts     string    `gorm:"type:text" json:"parts,omitempty"`
	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
}

type SQLiteSummaryDocument struct {
	ID                      uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID               string    `gorm:"type:varchar(255);uniqueIndex;not null" json:"session_id"`
	Content                 string    `gorm:"type:text" json:"content"`
	LastSummarizedMessageID uint      `gorm:"index" json:"last_summarized_message_id"`
	CreatedAt               time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt               time.Time `gorm:"index;not null" json:"updated_at"`
}

func (SQLiteMessageDocument) TableName() string {
	return "chat_messages"
}

func (SQLiteSummaryDocument) TableName() string {
	return "chat_summaries"
}

type SQLiteMemoryProvider struct {
	mu                 sync.RWMutex
	client             *sqlite.Client
	sessionID          string
	maxHistoryMessages int
	tableName          string
}

func NewSQLiteMemoryProvider(client *sqlite.Client, sessionID string) *SQLiteMemoryProvider {
	return &SQLiteMemoryProvider{
		client:             client,
		sessionID:          sessionID,
		maxHistoryMessages: 100,
		tableName:          "chat_messages",
	}
}

func NewSQLiteMemoryProviderWithLimit(client *sqlite.Client, sessionID string, maxHistoryMessages int) *SQLiteMemoryProvider {
	return &SQLiteMemoryProvider{
		client:             client,
		sessionID:          sessionID,
		maxHistoryMessages: maxHistoryMessages,
		tableName:          "chat_messages",
	}
}

func (p *SQLiteMemoryProvider) SetMaxHistoryMessages(limit int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxHistoryMessages = limit
}

func (p *SQLiteMemoryProvider) SetTableName(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tableName = name
}

func (p *SQLiteMemoryProvider) getDB() *gorm.DB {
	return p.client.DB
}

func (p *SQLiteMemoryProvider) initTable(ctx context.Context) error {
	p.mu.RLock()
	tableName := p.tableName
	p.mu.RUnlock()

	// Ensure both tables exist
	if err := p.getDB().WithContext(ctx).Table(tableName).AutoMigrate(&SQLiteMessageDocument{}); err != nil {
		return err
	}
	return p.getDB().WithContext(ctx).Table("chat_summaries").AutoMigrate(&SQLiteSummaryDocument{})
}

func (p *SQLiteMemoryProvider) AddMessage(ctx context.Context, message types.Message) error {
	if err := p.initTable(ctx); err != nil {
		return err
	}

	p.mu.RLock()
	sessionID := p.sessionID
	tableName := p.tableName
	p.mu.RUnlock()

	partsJSON, _ := types.SerializeMessageParts(message.Parts)

	doc := SQLiteMessageDocument{
		SessionID: sessionID,
		Role:      message.Role,
		Content:   message.Content,
		Name:      message.Name,
		Parts:     partsJSON,
		CreatedAt: time.Now(),
	}

	if err := p.getDB().WithContext(ctx).Table(tableName).Create(&doc).Error; err != nil {
		return err
	}

	return nil
}

func (p *SQLiteMemoryProvider) GetMessages(ctx context.Context, limit int) ([]types.Message, error) {
	if err := p.initTable(ctx); err != nil {
		return nil, err
	}

	p.mu.RLock()
	sessionID := p.sessionID
	maxHistoryMessages := p.maxHistoryMessages
	tableName := p.tableName
	p.mu.RUnlock()

	queryLimit := limit
	if queryLimit <= 0 {
		queryLimit = maxHistoryMessages
		if queryLimit <= 0 {
			queryLimit = 1000
		}
	}

	// Fetch recent messages
	var docs []SQLiteMessageDocument
	err := p.getDB().WithContext(ctx).Table(tableName).
		Where("session_id = ?", sessionID).
		Order("created_at DESC"). // Get latest first
		Limit(queryLimit).
		Find(&docs).Error
	if err != nil {
		return nil, err
	}

	// Reverse to chronological order
	messages := make([]types.Message, 0, len(docs)+1)
	for i := len(docs) - 1; i >= 0; i-- {
		parts, _ := types.DeserializeMessageParts(docs[i].Parts)
		messages = append(messages, types.Message{
			Role:    docs[i].Role,
			Content: docs[i].Content,
			Name:    docs[i].Name,
			Parts:   parts,
		})
	}

	// Fetch summary
	var summaryDoc SQLiteSummaryDocument
	err = p.getDB().WithContext(ctx).Table("chat_summaries").
		Where("session_id = ?", sessionID).
		Order("updated_at DESC").
		First(&summaryDoc).Error

	if err == nil && summaryDoc.Content != "" {
		// Prepend summary as a system message
		summaryMsg := types.Message{
			Role:    "system",
			Content: fmt.Sprintf("Previous conversation summary: %s", summaryDoc.Content),
		}
		messages = append([]types.Message{summaryMsg}, messages...)
	} else if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}

	return messages, nil
}

func (p *SQLiteMemoryProvider) LoadMemoryVariables(ctx context.Context) (map[string]interface{}, error) {
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

func (p *SQLiteMemoryProvider) SaveContext(ctx context.Context, input, output map[string]interface{}) error {
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

func (p *SQLiteMemoryProvider) Clear(ctx context.Context) error {
	if err := p.initTable(ctx); err != nil {
		return err
	}
	p.mu.RLock()
	sessionID := p.sessionID
	tableName := p.tableName
	p.mu.RUnlock()

	return p.getDB().WithContext(ctx).Table(tableName).
		Where("session_id = ?", sessionID).
		Delete(&SQLiteMessageDocument{}).Error
}

func (p *SQLiteMemoryProvider) GetChatHistory(ctx context.Context) ([]types.Message, error) {
	if err := p.initTable(ctx); err != nil {
		return nil, err
	}
	p.mu.RLock()
	sessionID := p.sessionID
	tableName := p.tableName
	p.mu.RUnlock()

	var docs []SQLiteMessageDocument
	err := p.getDB().WithContext(ctx).Table(tableName).
		Where("session_id = ?", sessionID).
		Order("created_at ASC").
		Find(&docs).Error
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

func (p *SQLiteMemoryProvider) CompressMemory(ctx context.Context, llm types.LLMProvider, maxMessages int) error {
	if llm == nil {
		return fmt.Errorf("LLM provider is required for memory compression")
	}

	p.mu.Lock()
	sessionID := p.sessionID
	tableName := p.tableName

	// 1. Get current summary
	var summaryDoc SQLiteSummaryDocument
	err := p.getDB().WithContext(ctx).Table("chat_summaries").
		Where("session_id = ?", sessionID).
		Order("updated_at DESC").
		First(&summaryDoc).Error

	lastSummarizedID := uint(0)
	currentSummary := ""
	if err == nil {
		lastSummarizedID = summaryDoc.LastSummarizedMessageID
		currentSummary = summaryDoc.Content
	} else if err != gorm.ErrRecordNotFound {
		p.mu.Unlock()
		return err
	}

	// 2. Get unsummarized messages
	var unsummarizedDocs []SQLiteMessageDocument
	err = p.getDB().WithContext(ctx).Table(tableName).
		Where("session_id = ? AND id > ?", sessionID, lastSummarizedID).
		Order("created_at ASC").
		Find(&unsummarizedDocs).Error
	if err != nil {
		p.mu.Unlock()
		return err
	}

	// 3. Determine what to summarize
	// We want to keep maxMessages as "recent" (unsummarized) context.
	// So we summarize everything except the last maxMessages.
	if len(unsummarizedDocs) <= maxMessages {
		p.mu.Unlock()
		return nil // Nothing to summarize yet
	}

	numToSummarize := len(unsummarizedDocs) - maxMessages
	docsToSummarize := unsummarizedDocs[:numToSummarize]
	lastDoc := docsToSummarize[len(docsToSummarize)-1]

	// 4. Generate summary
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

	// 5. Save updated summary
	p.mu.Lock()
	defer p.mu.Unlock()
	newSummaryDoc := SQLiteSummaryDocument{
		SessionID:               sessionID,
		Content:                 summaryMsg.Content,
		LastSummarizedMessageID: lastDoc.ID,
		CreatedAt:               time.Now(),
		UpdatedAt:               time.Now(),
	}

	// We can either update the existing record or insert a new one (history of summaries).
	// For now, let's keep one summary per session to keep it simple, or insert new to track history.
	// The GetMessages uses Order("updated_at DESC").First(), so inserting new is fine and safer.
	if err := p.getDB().WithContext(ctx).Table("chat_summaries").Create(&newSummaryDoc).Error; err != nil {
		return fmt.Errorf("failed to save summary: %w", err)
	}

	return nil
}
