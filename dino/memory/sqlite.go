package memory

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/xichan96/cortex/pkg/logger"
)

const maxSessionIDLen = 128

type SQLite struct {
	db        *sql.DB
	sessionID string
	config    *Config
	mu        sync.RWMutex
	stats     Stats
	comparing atomic.Bool
}

type sqliteMessage struct {
	ID        int64
	Role      string
	Content   string
	Timestamp time.Time
	ToolCalls string
}

func NewSQLite(sessionID string, config *Config) (Provider, error) {
	if config == nil {
		config = DefaultConfig()
	}

	sanitizedID := sanitizeSessionID(sessionID)

	dir := config.PersistDirectory
	if dir == "" {
		dir = "./dino_sessions"
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create persist directory: %w", err)
	}

	dbPath := filepath.Join(dir, fmt.Sprintf("session_%s.db", sanitizedID))
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if err := initSQLiteDB(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("init db: %w", err)
	}

	return &SQLite{
		db:        db,
		sessionID: sessionID,
		config:    config,
		stats:     Stats{},
	}, nil
}

func initSQLiteDB(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		tool_calls TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_messages_timestamp ON messages(timestamp);
	CREATE TABLE IF NOT EXISTS metadata (
		key TEXT PRIMARY KEY,
		value TEXT
	);
	`
	_, err := db.Exec(schema)
	return err
}

func (s *SQLite) AddMessage(ctx context.Context, msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	toolCallsJSON := ""
	if msg.ToolCalls != nil {
		toolCallsJSON = fmt.Sprintf("%v", msg.ToolCalls)
	}

	_, err := s.db.ExecContext(ctx,
		"INSERT INTO messages (role, content, timestamp, tool_calls) VALUES (?, ?, ?, ?)",
		msg.Role, msg.Content, time.Now().Format(time.RFC3339), toolCallsJSON)
	if err != nil {
		return err
	}

	s.stats.MessageCount++
	s.stats.TotalTokens += estimateTokens(msg.Content)

	if s.config.EnableMemoryCompress && s.stats.MessageCount > s.config.MemoryCompressThreshold {
		if s.comparing.CompareAndSwap(false, true) {
			go s.compressAsync(ctx)
		}
	}

	return nil
}

func (s *SQLite) GetMessages(ctx context.Context, limit int) ([]Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var query string
	var args []interface{}

	if limit > 0 {
		query = "SELECT role, content, timestamp, tool_calls FROM messages ORDER BY timestamp ASC LIMIT ?"
		args = append(args, limit)
	} else {
		query = "SELECT role, content, timestamp, tool_calls FROM messages ORDER BY timestamp ASC"
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var role, content, timestampStr, toolCalls string
		if err := rows.Scan(&role, &content, &timestampStr, &toolCalls); err != nil {
			return nil, err
		}
		messages = append(messages, Message{
			Role:    role,
			Content: content,
		})
	}

	return messages, rows.Err()
}

func (s *SQLite) GetSummary(ctx context.Context) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var summary string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'summary'").Scan(&summary)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return summary, err
}

func (s *SQLite) Compress(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	messages, err := s.getAllMessages()
	if err != nil {
		return err
	}

	if len(messages) <= s.config.KeepRecentCount {
		return nil
	}

	old := messages[:len(messages)-s.config.KeepRecentCount]

	summary := fmt.Sprintf("Previous conversation summary: %d messages about: %s",
		len(old), summarizeMessages(old))

	_, err = s.db.ExecContext(ctx, "DELETE FROM messages WHERE id NOT IN (SELECT id FROM messages ORDER BY timestamp DESC LIMIT ?)",
		s.config.KeepRecentCount)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx, "INSERT OR REPLACE INTO metadata (key, value) VALUES ('summary', ?)", summary)
	return err
}

func (s *SQLite) getAllMessages() ([]Message, error) {
	rows, err := s.db.Query("SELECT role, content, timestamp, tool_calls FROM messages ORDER BY timestamp ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var role, content, timestampStr, toolCalls string
		if err := rows.Scan(&role, &content, &timestampStr, &toolCalls); err != nil {
			return nil, err
		}
		messages = append(messages, Message{
			Role:    role,
			Content: content,
		})
	}
	return messages, rows.Err()
}

func (s *SQLite) compressAsync(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.comparing.Store(false)
		s.mu.Unlock()
	}()

	if err := s.Compress(ctx); err != nil {
		logger.Warn("[SQLite] Compress error", slog.String("error", err.Error()))
	}
}

func (s *SQLite) Clear(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, "DELETE FROM messages")
	s.stats = Stats{}
	return err
}

func (s *SQLite) GetStats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats
}

func (s *SQLite) Close() error {
	return s.db.Close()
}

func summarizeMessages(messages []Message) string {
	if len(messages) == 0 {
		return "nothing"
	}
	var topics []string
	seen := make(map[string]bool)
	for _, msg := range messages {
		words := extractKeywords(msg.Content)
		for _, w := range words {
			if !seen[w] && len(w) > 3 {
				seen[w] = true
				topics = append(topics, w)
				if len(topics) >= 5 {
					break
				}
			}
		}
		if len(topics) >= 5 {
			break
		}
	}
	if len(topics) == 0 {
		return "various topics"
	}
	result := ""
	for i, t := range topics {
		if i > 0 {
			result += ", "
		}
		result += t
	}
	return result
}

func extractKeywords(text string) []string {
	keywords := []string{}
	words := []string{}
	current := ""
	for _, c := range text {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			current += string(c)
		} else {
			if len(current) > 4 {
				words = append(words, current)
			}
			current = ""
		}
	}
	if len(current) > 4 {
		words = append(words, current)
	}

	stopWords := map[string]bool{
		"the": true, "and": true, "that": true, "this": true, "with": true,
		"have": true, "from": true, "they": true, "will": true, "your": true,
	}

	for _, w := range words {
		lower := strings.ToLower(w)
		if !stopWords[lower] {
			keywords = append(keywords, lower)
		}
	}
	return keywords
}

func sanitizeSessionID(id string) string {
	if len(id) > maxSessionIDLen {
		id = id[:maxSessionIDLen]
	}
	result := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			result = append(result, c)
		} else {
			result = append(result, '_')
		}
	}
	return string(result)
}
