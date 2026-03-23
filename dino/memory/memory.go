package memory

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentproviders "github.com/xichan96/cortex/agent/providers"
	agenttypes "github.com/xichan96/cortex/agent/types"
)

type Message = agenttypes.Message

const DefaultSharedDBFile = "shared_chat.db"

type ToolCall struct {
	Name   string `json:"name"`
	Input  string `json:"input"`
	Result string `json:"result"`
}

type Provider interface {
	AddMessage(ctx context.Context, msg Message) error
	GetMessages(ctx context.Context, limit int) ([]Message, error)
	GetSummary(ctx context.Context) (string, error)
	Compress(ctx context.Context) error
	Clear(ctx context.Context) error
	GetStats() Stats
}

type Stats struct {
	MessageCount  int `json:"message_count"`
	SummaryLength int `json:"summary_length"`
	TotalTokens   int `json:"total_tokens"`
}

type Config struct {
	MaxHistoryMessages      int
	EnableMemoryCompress    bool
	MemoryCompressThreshold int
	KeepRecentCount         int
	CompressionRatio        float32
	PersistDirectory        string
	SQLiteFile              string
}

func DefaultConfig() *Config {
	return &Config{
		MaxHistoryMessages:      100,
		EnableMemoryCompress:    true,
		MemoryCompressThreshold: 50,
		KeepRecentCount:         10,
		CompressionRatio:        0.5,
		PersistDirectory:        "./dino_sessions",
	}
}

type InMemory struct {
	*agentproviders.SimpleMemoryProvider
	mu        sync.RWMutex
	summary   string
	config    *Config
	sessionID string
	stats     Stats
	comparing atomic.Bool
}

func NewInMemory(sessionID string, config *Config) Provider {
	if config == nil {
		config = DefaultConfig()
	}

	return &InMemory{
		SimpleMemoryProvider: agentproviders.NewSimpleMemoryProviderWithLimit(config.MaxHistoryMessages),
		summary:              "",
		config:               config,
		sessionID:            sessionID,
		stats:                Stats{},
	}
}

func (m *InMemory) AddMessage(ctx context.Context, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if msg.Role == "" {
		msg.Role = "user"
	}

	m.SimpleMemoryProvider.AddMessage(ctx, msg)
	m.stats.MessageCount++
	m.stats.TotalTokens += estimateTokens(msg.Content)

	if m.config.EnableMemoryCompress && m.stats.MessageCount > m.config.MemoryCompressThreshold {
		if m.comparing.CompareAndSwap(false, true) {
			go m.compressAsync(ctx)
		}
	}

	return nil
}

func (m *InMemory) GetMessages(ctx context.Context, limit int) ([]Message, error) {
	return m.SimpleMemoryProvider.GetMessages(ctx, limit)
}

func (m *InMemory) GetSummary(ctx context.Context) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.summary, nil
}

func (m *InMemory) Compress(ctx context.Context) error {
	return m.compress(ctx)
}

func (m *InMemory) Clear(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.SimpleMemoryProvider.Clear(ctx)
	m.summary = ""
	m.stats = Stats{}

	return nil
}

func (m *InMemory) GetStats() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stats
}

func (m *InMemory) compress(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	allMsgs, err := m.SimpleMemoryProvider.GetMessages(ctx, 0)
	if err != nil {
		return err
	}
	if len(allMsgs) <= m.config.KeepRecentCount {
		return nil
	}

	recentCount := m.config.KeepRecentCount
	recentMessages := make([]Message, recentCount)
	copy(recentMessages, allMsgs[len(allMsgs)-recentCount:])
	olderMessages := make([]Message, len(allMsgs)-recentCount)
	copy(olderMessages, allMsgs[:len(allMsgs)-recentCount])

	summaryText := m.generateSummary(m.summary, olderMessages)

	m.summary = summaryText
	m.stats.MessageCount = len(recentMessages)
	m.stats.SummaryLength = len(summaryText)

	return nil
}

func (m *InMemory) compressAsync(ctx context.Context) {
	defer m.comparing.Store(false)
	select {
	case <-ctx.Done():
		return
	default:
	}
	_ = m.compress(ctx)
}

func (m *InMemory) generateSummary(existingSummary string, messages []Message) string {
	if len(messages) == 0 {
		return existingSummary
	}

	var contentBuilder strings.Builder
	if existingSummary != "" {
		contentBuilder.WriteString("Previous Summary: ")
		contentBuilder.WriteString(existingSummary)
		contentBuilder.WriteString("\n")
	}
	for _, msg := range messages {
		contentBuilder.WriteString(msg.Role)
		contentBuilder.WriteString(": ")
		contentBuilder.WriteString(msg.Content)
		contentBuilder.WriteString("\n")
	}

	summary := "Summary of previous conversation:\n"
	summary += "User and assistant exchanged " + strconv.Itoa(len(messages)) + " messages.\n"
	summary += "Topics discussed include the user's requests and assistant's responses.\n"

	return summary
}

type ConversationSummary struct {
	mu        sync.RWMutex
	messages  []Message
	summary   string
	updatedAt time.Time
}

func NewConversationSummary() *ConversationSummary {
	return &ConversationSummary{
		messages:  make([]Message, 0),
		updatedAt: time.Now(),
	}
}

func (s *ConversationSummary) AddMessage(msg Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.messages = append(s.messages, msg)
	s.updatedAt = time.Now()
}

func (s *ConversationSummary) GetSummary() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.summary
}

func (s *ConversationSummary) SetSummary(summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.summary = summary
	s.updatedAt = time.Now()
}

func (s *ConversationSummary) GetMessageCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return len(s.messages)
}

func (s *ConversationSummary) GetLastUpdated() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.updatedAt
}

type LLMProvider interface {
	GenerateSummary(ctx context.Context, messages []Message) (string, error)
}

type Hybrid struct {
	provider    Provider
	sessionID   string
	summary     string
	summaryMu   sync.RWMutex
	llmProvider LLMProvider
	config      *Config
	compressing atomic.Bool
}

func NewHybrid(sessionID string, provider Provider, llmProvider LLMProvider, config *Config) *Hybrid {
	if config == nil {
		config = DefaultConfig()
	}

	return &Hybrid{
		provider:    provider,
		sessionID:   sessionID,
		llmProvider: llmProvider,
		config:      config,
	}
}

func (m *Hybrid) AddMessage(ctx context.Context, msg Message) error {
	err := m.provider.AddMessage(ctx, msg)
	if err != nil {
		return err
	}

	if m.config.EnableMemoryCompress {
		messages, _ := m.provider.GetMessages(ctx, 0)
		if len(messages) > m.config.MemoryCompressThreshold {
			if m.compressing.CompareAndSwap(false, true) {
				go m.autoCompress(ctx)
			}
		}
	}

	return nil
}

func (m *Hybrid) GetMessages(ctx context.Context, limit int) ([]Message, error) {
	messages, err := m.provider.GetMessages(ctx, limit)
	if err != nil {
		return nil, err
	}

	m.summaryMu.RLock()
	summary := m.summary
	m.summaryMu.RUnlock()

	if summary != "" {
		summaryMsg := Message{
			Role:    "system",
			Content: summary,
		}
		messages = append([]Message{summaryMsg}, messages...)
	}

	return messages, nil
}

func (m *Hybrid) GetSummary(ctx context.Context) (string, error) {
	m.summaryMu.RLock()
	defer m.summaryMu.RUnlock()
	return m.summary, nil
}

func (m *Hybrid) Compress(ctx context.Context) error {
	messages, err := m.provider.GetMessages(ctx, 0)
	if err != nil {
		return err
	}

	if len(messages) <= m.config.KeepRecentCount {
		return nil
	}

	recentCount := m.config.KeepRecentCount
	olderMessages := messages[:len(messages)-recentCount]

	m.summaryMu.RLock()
	existingSummary := m.summary
	m.summaryMu.RUnlock()

	if existingSummary != "" {
		summaryMsg := Message{
			Role:    "system",
			Content: "Previous Summary:\n" + existingSummary,
		}
		olderMessages = append([]Message{summaryMsg}, olderMessages...)
	}

	var summary string
	if m.llmProvider != nil {
		summary, err = m.llmProvider.GenerateSummary(ctx, olderMessages)
		if err != nil {
			summary = generateBasicSummary(olderMessages)
		}
	} else {
		summary = generateBasicSummary(olderMessages)
	}

	m.summaryMu.Lock()
	m.summary = summary
	m.summaryMu.Unlock()

	err = m.provider.Clear(ctx)
	if err != nil {
		return err
	}

	recentMessages := messages[len(messages)-recentCount:]
	for _, msg := range recentMessages {
		_ = m.provider.AddMessage(ctx, msg)
	}

	return nil
}

func (m *Hybrid) Clear(ctx context.Context) error {
	m.summaryMu.Lock()
	m.summary = ""
	m.summaryMu.Unlock()

	return m.provider.Clear(ctx)
}

func (m *Hybrid) GetStats() Stats {
	stats := m.provider.GetStats()
	m.summaryMu.RLock()
	stats.SummaryLength = len(m.summary)
	m.summaryMu.RUnlock()
	return stats
}

func (m *Hybrid) autoCompress(ctx context.Context) {
	defer m.compressing.Store(false)
	select {
	case <-ctx.Done():
		return
	default:
	}
	_ = m.Compress(ctx)
}

func generateBasicSummary(messages []Message) string {
	if len(messages) == 0 {
		return ""
	}

	var contentBuilder strings.Builder
	for _, msg := range messages {
		contentBuilder.WriteString(msg.Role)
		contentBuilder.WriteString(": ")
		contentBuilder.WriteString(msg.Content)
		contentBuilder.WriteString("\n")
	}

	summary := "Summary of previous conversation:\n"
	summary += "User and assistant exchanged " + strconv.Itoa(len(messages)) + " messages.\n"
	summary += "Topics discussed include the user's requests and assistant's responses.\n"

	return summary
}

func estimateTokens(text string) int {
	asciiCount := 0
	nonAsciiCount := 0
	for _, r := range text {
		if r < 128 {
			asciiCount++
		} else {
			nonAsciiCount++
		}
	}
	return (asciiCount / 4) + (nonAsciiCount * 2)
}
