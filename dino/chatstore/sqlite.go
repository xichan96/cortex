package chatstore

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
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"
)

type SQLite struct {
	db        *sql.DB
	sessionID string
	config    *Config
	mu        sync.RWMutex
	stats     Stats
	comparing atomic.Bool
}

var (
	sharedMu    sync.Mutex
	sharedDBBy  = map[string]*sql.DB{}
	migrateOnce sync.Map
)

func OpenSharedChatStore(dir, sqliteFile string) (*sql.DB, error) {
	return openSharedSQLite(dir, sqliteFile)
}

func renameLegacySharedDBFile(absDir, sqliteFile string) {
	newPath := filepath.Join(absDir, sqliteFile)
	oldPath := filepath.Join(absDir, "cortex_chat.db")
	if _, err := os.Stat(newPath); err == nil {
		return
	}
	if _, err := os.Stat(oldPath); err == nil {
		_ = os.Rename(oldPath, newPath)
	}
}

func openSharedSQLite(dir, sqliteFile string) (*sql.DB, error) {
	if sqliteFile == "" {
		sqliteFile = DefaultSharedDBFile
	}
	if dir == "" {
		dir = "./cortex_sessions"
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absDir, 0755); err != nil {
		return nil, fmt.Errorf("create persist directory: %w", err)
	}
	renameLegacySharedDBFile(absDir, sqliteFile)

	dbPath, err := filepath.Abs(filepath.Join(absDir, sqliteFile))
	if err != nil {
		return nil, err
	}

	sharedMu.Lock()
	if db := sharedDBBy[dbPath]; db != nil {
		sharedMu.Unlock()
		return db, nil
	}
	sharedMu.Unlock()

	// WAL + busy_timeout: ingest 后台写与用户会话写共享同一连接池，
	// 默认 rollback-journal 下写事务会偶发 "database is locked"。
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := initSharedSQLiteDB(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("init db: %w", err)
	}

	v, _ := migrateOnce.LoadOrStore(dbPath, &sync.Once{})
	once := v.(*sync.Once)
	once.Do(func() {
		if migErr := migrateLegacySessionDBs(context.Background(), db, absDir, sqliteFile); migErr != nil {
			logger.Warn("[SQLite] legacy migration", slog.String("error", migErr.Error()))
		}
	})

	sharedMu.Lock()
	defer sharedMu.Unlock()
	if existing := sharedDBBy[dbPath]; existing != nil {
		db.Close()
		return existing, nil
	}
	sharedDBBy[dbPath] = db
	return db, nil
}

func initSharedSQLiteDB(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
		tool_calls TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_messages_session_ts ON messages(session_id, timestamp);
	CREATE TABLE IF NOT EXISTS metadata (
		session_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value TEXT,
		PRIMARY KEY (session_id, key)
	);
	`
	_, err := db.Exec(schema)
	return err
}

func migrateLegacySessionDBs(ctx context.Context, shared *sql.DB, absDir, sharedSQLiteFile string) error {
	ents, err := os.ReadDir(absDir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == sharedSQLiteFile || !strings.HasPrefix(name, "session_") || !strings.HasSuffix(name, ".db") {
			continue
		}
		sid := strings.TrimSuffix(strings.TrimPrefix(name, "session_"), ".db")
		if sid == "" {
			continue
		}
		var n int
		if err := shared.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE session_id = ?", sid).Scan(&n); err != nil {
			continue
		}
		if n > 0 {
			continue
		}
		legacyPath := filepath.Join(absDir, name)
		legDB, err := sql.Open("sqlite3", legacyPath+"?mode=ro")
		if err != nil {
			continue
		}
		rows, err := legDB.QueryContext(ctx, "SELECT role, content, timestamp, tool_calls FROM messages ORDER BY timestamp ASC")
		if err != nil {
			legDB.Close()
			continue
		}
		for rows.Next() {
			var role, content, ts, toolCalls string
			if err := rows.Scan(&role, &content, &ts, &toolCalls); err != nil {
				rows.Close()
				legDB.Close()
				return err
			}
			if _, err := shared.ExecContext(ctx,
				"INSERT INTO messages (session_id, role, content, timestamp, tool_calls) VALUES (?, ?, ?, ?, ?)",
				sid, role, content, ts, toolCalls); err != nil {
				rows.Close()
				legDB.Close()
				return err
			}
		}
		rows.Close()

		var summary string
		if err := legDB.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'summary'").Scan(&summary); err == nil && summary != "" {
			_, _ = shared.ExecContext(ctx, "INSERT OR REPLACE INTO metadata (session_id, key, value) VALUES (?, 'summary', ?)", sid, summary)
		}
		legDB.Close()
		_ = os.Rename(legacyPath, legacyPath+".bak")
	}
	return nil
}

func NewSQLite(sessionID string, config *Config) (Provider, error) {
	if config == nil {
		config = DefaultConfig()
	}

	dir := config.PersistDirectory
	db, err := openSharedSQLite(dir, config.SQLiteFile)
	if err != nil {
		return nil, err
	}

	return &SQLite{
		db:        db,
		sessionID: sessionID,
		config:    config,
		stats:     Stats{},
	}, nil
}

func (s *SQLite) AddMessage(ctx context.Context, msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	toolPersist := ""
	if s, err := types.MarshalMessageToolPersist(msg); err == nil {
		toolPersist = s
	}

	_, err := s.db.ExecContext(ctx,
		"INSERT INTO messages (session_id, role, content, timestamp, tool_calls) VALUES (?, ?, ?, ?, ?)",
		s.sessionID, msg.Role, msg.Content, time.Now().Format(time.RFC3339), toolPersist)
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
		query = "SELECT role, content, timestamp, tool_calls FROM messages WHERE session_id = ? ORDER BY timestamp ASC LIMIT ?"
		args = []interface{}{s.sessionID, limit}
	} else {
		query = "SELECT role, content, timestamp, tool_calls FROM messages WHERE session_id = ? ORDER BY timestamp ASC"
		args = []interface{}{s.sessionID}
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
		m := Message{
			Role:    role,
			Content: content,
		}
		_ = types.ApplyMessageToolPersist(&m, toolCalls)
		messages = append(messages, m)
	}

	return messages, rows.Err()
}

func (s *SQLite) GetSummary(ctx context.Context) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var summary string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE session_id = ? AND key = 'summary'", s.sessionID).Scan(&summary)
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

	summary := DeterministicCompact("", old, DefaultCompactConfig())

	_, err = s.db.ExecContext(ctx, `DELETE FROM messages WHERE session_id = ? AND id NOT IN (
		SELECT id FROM (SELECT id FROM messages WHERE session_id = ? ORDER BY timestamp DESC LIMIT ?)
	)`, s.sessionID, s.sessionID, s.config.KeepRecentCount)
	if err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx, "INSERT OR REPLACE INTO metadata (session_id, key, value) VALUES (?, 'summary', ?)", s.sessionID, summary)
	return err
}

func (s *SQLite) getAllMessages() ([]Message, error) {
	rows, err := s.db.Query("SELECT role, content, timestamp, tool_calls FROM messages WHERE session_id = ? ORDER BY timestamp ASC", s.sessionID)
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
		m := Message{
			Role:    role,
			Content: content,
		}
		_ = types.ApplyMessageToolPersist(&m, toolCalls)
		messages = append(messages, m)
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

	_, _ = s.db.ExecContext(ctx, "DELETE FROM metadata WHERE session_id = ?", s.sessionID)
	_, err := s.db.ExecContext(ctx, "DELETE FROM messages WHERE session_id = ?", s.sessionID)
	s.stats = Stats{}
	return err
}

func (s *SQLite) GetStats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stats
}

func (s *SQLite) StoredMessageCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE session_id = ?", s.sessionID).Scan(&n)
	return n, err
}

func (s *SQLite) Close() error {
	return nil
}
