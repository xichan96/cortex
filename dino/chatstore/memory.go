package chatstore

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentproviders "github.com/xichan96/cortex/agent/providers"
	agenttypes "github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"
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
	StoredMessageCount(ctx context.Context) (int, error)
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
	m.stats.TotalTokens += EstimateTokens(msg.Content)

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

func (m *InMemory) StoredMessageCount(ctx context.Context) (int, error) {
	return m.SimpleMemoryProvider.StoredMessageCount(ctx)
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
	return DeterministicCompact(existingSummary, messages, DefaultCompactConfig())
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
	// injectSummaryTail 摘要注入模式（评审 B1 单一注入源）。
	// true = Hybrid 活跃：摘要只由 GetMessages 尾部注入，engine 头部 GetSummary 注入被
	// memoryAdapter 禁用；false = Hybrid 不活跃：GetMessages 不追加尾部摘要，engine 头部
	// 注入照常（非 Hybrid 底层 provider 场景）。
	injectSummaryTail bool
}

func NewHybrid(sessionID string, provider Provider, llmProvider LLMProvider, config *Config) *Hybrid {
	if config == nil {
		config = DefaultConfig()
	}

	return &Hybrid{
		provider:          provider,
		sessionID:         sessionID,
		llmProvider:       llmProvider,
		config:            config,
		injectSummaryTail: true,
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

// TailSummaryEnabled 返回当前摘要注入模式：true = 摘要由 GetMessages 尾部注入
// （engine 头部 GetSummary 注入应被禁用，单一注入源）；false = 尾部注入关闭。
func (m *Hybrid) TailSummaryEnabled() bool {
	return m.injectSummaryTail
}

func (m *Hybrid) GetMessages(ctx context.Context, limit int) ([]Message, error) {
	messages, err := m.provider.GetMessages(ctx, limit)
	if err != nil {
		return nil, err
	}

	if !m.injectSummaryTail {
		return messages, nil
	}

	m.summaryMu.RLock()
	summary := m.summary
	m.summaryMu.RUnlock()

	if summary != "" {
		messages = append(messages, Message{
			Role:    "user",
			Content: SummaryMarker + "\n" + summary,
		})
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

	// A) 尾部原文保留：最近 KeepRecentCount 条 + 按 token 预算吸收的 user 原文。
	tailUser, older := splitTailUserMessages(messages, m.config.KeepRecentCount, MaxRecentTailTokens)

	// B) 前次摘要前置到 older，送 LLM 生成新摘要。
	m.summaryMu.RLock()
	existingSummary := m.summary
	m.summaryMu.RUnlock()

	if existingSummary != "" {
		summaryMsg := Message{
			Role:    "system",
			Content: "Previous Summary:\n" + existingSummary,
		}
		older = append([]Message{summaryMsg}, older...)
	}

	// C) LLM 摘要 + 确定性 fallback（DeterministicCompact 替换 generateBasicSummary）。
	var summary string
	if m.llmProvider != nil {
		s, sErr := m.llmProvider.GenerateSummary(ctx, older)
		if sErr != nil {
			logger.Warn("[Hybrid] LLM summary failed, using deterministic fallback",
				slog.String("error", sErr.Error()))
			summary = DeterministicCompact(existingSummary, older, DefaultCompactConfig())
		} else if strings.TrimSpace(s) == "" {
			logger.Warn("[Hybrid] LLM summary empty, using deterministic fallback")
			summary = DeterministicCompact(existingSummary, older, DefaultCompactConfig())
		} else {
			summary = strings.TrimSpace(s)
		}
	} else {
		summary = DeterministicCompact(existingSummary, older, DefaultCompactConfig())
	}

	m.summaryMu.Lock()
	m.summary = summary
	m.summaryMu.Unlock()

	// D) 落盘：清空 → 回写 tailUser（尾部原文）；摘要存 m.summary（GetMessages 尾部注入）。
	err = m.provider.Clear(ctx)
	if err != nil {
		return err
	}

	for _, msg := range tailUser {
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

func (m *Hybrid) StoredMessageCount(ctx context.Context) (int, error) {
	return m.provider.StoredMessageCount(ctx)
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

