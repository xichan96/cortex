package providers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/sql/mysql"
	"gorm.io/gorm"
)

type MySQLMessageDocument struct {
	ID        uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	SessionID string    `gorm:"type:varchar(255);index;not null" json:"session_id"`
	Role      string    `gorm:"type:varchar(50);not null" json:"role"`
	Content   string    `gorm:"type:text" json:"content"`
	Name      string    `gorm:"type:varchar(255)" json:"name,omitempty"`
	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
}

func (MySQLMessageDocument) TableName() string {
	return "chat_messages"
}

type MySQLMemoryProvider struct {
	mu                 sync.RWMutex
	client             *mysql.Client
	sessionID          string
	maxHistoryMessages int
	tableName          string
}

func NewMySQLMemoryProvider(client *mysql.Client, sessionID string) *MySQLMemoryProvider {
	return &MySQLMemoryProvider{
		client:             client,
		sessionID:          sessionID,
		maxHistoryMessages: 100,
		tableName:          "chat_messages",
	}
}

func NewMySQLMemoryProviderWithLimit(client *mysql.Client, sessionID string, maxHistoryMessages int) *MySQLMemoryProvider {
	return &MySQLMemoryProvider{
		client:             client,
		sessionID:          sessionID,
		maxHistoryMessages: maxHistoryMessages,
		tableName:          "chat_messages",
	}
}

func (p *MySQLMemoryProvider) SetMaxHistoryMessages(limit int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxHistoryMessages = limit
}

func (p *MySQLMemoryProvider) SetTableName(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tableName = name
}

func (p *MySQLMemoryProvider) getDB() *gorm.DB {
	return p.client.DB
}

func (p *MySQLMemoryProvider) initTable(ctx context.Context) error {
	p.mu.RLock()
	tableName := p.tableName
	p.mu.RUnlock()

	doc := MySQLMessageDocument{}
	return p.getDB().WithContext(ctx).Table(tableName).AutoMigrate(&doc)
}

func (p *MySQLMemoryProvider) AddMessage(ctx context.Context, message types.Message) error {
	if err := p.initTable(ctx); err != nil {
		return err
	}

	p.mu.RLock()
	sessionID := p.sessionID
	maxHistoryMessages := p.maxHistoryMessages
	tableName := p.tableName
	p.mu.RUnlock()

	doc := MySQLMessageDocument{
		SessionID: sessionID,
		Role:      message.Role,
		Content:   message.Content,
		Name:      message.Name,
		CreatedAt: time.Now(),
	}

	if err := p.getDB().WithContext(ctx).Table(tableName).Create(&doc).Error; err != nil {
		return err
	}

	if maxHistoryMessages > 0 {
		return p.trimHistory(ctx)
	}
	return nil
}

func (p *MySQLMemoryProvider) GetMessages(ctx context.Context, limit int) ([]types.Message, error) {
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

	var docs []MySQLMessageDocument
	err := p.getDB().WithContext(ctx).Table(tableName).
		Where("session_id = ?", sessionID).
		Order("created_at ASC").
		Limit(queryLimit).
		Find(&docs).Error
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

func (p *MySQLMemoryProvider) LoadMemoryVariables() (map[string]interface{}, error) {
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

func (p *MySQLMemoryProvider) SaveContext(input, output map[string]interface{}) error {
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

func (p *MySQLMemoryProvider) Clear() error {
	ctx := context.Background()
	p.mu.RLock()
	sessionID := p.sessionID
	tableName := p.tableName
	p.mu.RUnlock()

	return p.getDB().WithContext(ctx).Table(tableName).
		Where("session_id = ?", sessionID).
		Delete(&MySQLMessageDocument{}).Error
}

func (p *MySQLMemoryProvider) GetChatHistory() ([]types.Message, error) {
	ctx := context.Background()
	p.mu.RLock()
	maxHistoryMessages := p.maxHistoryMessages
	p.mu.RUnlock()
	return p.GetMessages(ctx, maxHistoryMessages)
}

func (p *MySQLMemoryProvider) trimHistory(ctx context.Context) error {
	p.mu.RLock()
	maxHistoryMessages := p.maxHistoryMessages
	sessionID := p.sessionID
	tableName := p.tableName
	p.mu.RUnlock()

	if maxHistoryMessages <= 0 {
		return nil
	}

	var docs []MySQLMessageDocument
	err := p.getDB().WithContext(ctx).Table(tableName).
		Where("session_id = ?", sessionID).
		Order("created_at ASC").
		Limit(maxHistoryMessages).
		Find(&docs).Error
	if err != nil {
		return err
	}

	if len(docs) == 0 {
		return nil
	}

	var totalCount int64
	err = p.getDB().WithContext(ctx).Table(tableName).
		Where("session_id = ?", sessionID).
		Count(&totalCount).Error
	if err != nil {
		return err
	}

	if totalCount <= int64(maxHistoryMessages) {
		return nil
	}

	oldestKeptDoc := docs[0]
	return p.getDB().WithContext(ctx).Table(tableName).
		Where("session_id = ? AND created_at < ?", sessionID, oldestKeptDoc.CreatedAt).
		Delete(&MySQLMessageDocument{}).Error
}

func (p *MySQLMemoryProvider) CompressMemory(llm types.LLMProvider, maxMessages int) error {
	if llm == nil {
		return fmt.Errorf("LLM provider is required for memory compression")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	ctx := context.Background()
	sessionID := p.sessionID
	tableName := p.tableName

	var docs []MySQLMessageDocument
	err := p.getDB().WithContext(ctx).Table(tableName).
		Where("session_id = ?", sessionID).
		Order("created_at ASC").
		Limit(10000).
		Find(&docs).Error
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

	now := time.Now()
	compressedMessages := make([]MySQLMessageDocument, 0, len(systemMessages)+1+len(recentMessages))

	for _, msg := range systemMessages {
		compressedMessages = append(compressedMessages, MySQLMessageDocument{
			SessionID: sessionID,
			Role:      msg.Role,
			Content:   msg.Content,
			Name:      msg.Name,
			CreatedAt: now,
		})
	}

	compressedMessages = append(compressedMessages, MySQLMessageDocument{
		SessionID: sessionID,
		Role:      "system",
		Content:   fmt.Sprintf("Previous conversation summary: %s", summaryMsg.Content),
		CreatedAt: now,
	})

	for _, msg := range recentMessages {
		compressedMessages = append(compressedMessages, MySQLMessageDocument{
			SessionID: sessionID,
			Role:      msg.Role,
			Content:   msg.Content,
			Name:      msg.Name,
			CreatedAt: now,
		})
	}

	tx := p.getDB().WithContext(ctx).Begin()
	defer tx.Rollback()

	if err := tx.Table(tableName).CreateInBatches(compressedMessages, 100).Error; err != nil {
		return fmt.Errorf("failed to insert compressed messages: %w", err)
	}

	var insertedDocs []MySQLMessageDocument
	err = tx.Table(tableName).
		Where("session_id = ? AND created_at = ?", sessionID, now).
		Find(&insertedDocs).Error
	if err != nil || len(insertedDocs) < len(compressedMessages) {
		return fmt.Errorf("failed to verify compressed messages insertion, rolled back")
	}

	if err := tx.Table(tableName).
		Where("session_id = ? AND created_at < ?", sessionID, now).
		Delete(&MySQLMessageDocument{}).Error; err != nil {
		return fmt.Errorf("failed to delete old messages after compression, rolled back: %w", err)
	}

	return tx.Commit().Error
}
