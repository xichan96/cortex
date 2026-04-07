package sqlite

import (
	"database/sql"
	"fmt"

	"github.com/xichan96/cortex/pkg/middle/sql/sqlite"
)

type SQLiteStore struct {
	db       *sql.DB
	sharedDB bool
}

func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	client, err := sqlite.NewClient(&sqlite.Config{
		Path:             dbPath,
		MaxOpenConn:      10,
		MaxIdleConn:      3,
		MaxIdleTimeSec:   120,
		DisableErrorHook: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite: %w", err)
	}

	sqlDB, err := client.DB.DB()
	if err != nil {
		return nil, err
	}

	store := &SQLiteStore{db: sqlDB, sharedDB: false}
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("failed to migrate: %w", err)
	}

	return store, nil
}

func NewSQLiteStoreFromDB(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("db is nil")
	}
	store := &SQLiteStore{db: db, sharedDB: true}
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("failed to migrate: %w", err)
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.sharedDB {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) DB() *sql.DB {
	return s.db
}

func (s *SQLiteStore) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS preferences (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT 'default',
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			priority INTEGER DEFAULT 5,
			metadata TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, category, key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_prefs_user ON preferences(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_prefs_category ON preferences(user_id, category)`,

		`CREATE TABLE IF NOT EXISTS knowledge (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			category TEXT NOT NULL DEFAULT 'general',
			content TEXT NOT NULL,
			tags TEXT,
			metadata TEXT,
			source TEXT,
			priority INTEGER DEFAULT 5,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_know_user ON knowledge(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_know_category ON knowledge(user_id, category)`,

		`CREATE TABLE IF NOT EXISTS context (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			user_id TEXT,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			metadata TEXT,
			ttl_seconds INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ctx_session ON context(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_ctx_user ON context(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_ctx_expires ON context(expires_at)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_ctx_session_key ON context(session_id, key)`,

		`CREATE TABLE IF NOT EXISTS indexes (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			source_id TEXT NOT NULL,
			title TEXT NOT NULL,
			root_id TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, source_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_idx_user ON indexes(user_id)`,

		`CREATE TABLE IF NOT EXISTS index_nodes (
			id TEXT PRIMARY KEY,
			index_id TEXT NOT NULL,
			parent_id TEXT,
			title TEXT NOT NULL,
			level INTEGER NOT NULL,
			start_line INTEGER,
			end_line INTEGER,
			content TEXT,
			summary TEXT,
			prefix_summary TEXT DEFAULT '',
			tags TEXT,
			metadata TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_node_index ON index_nodes(index_id)`,
		`CREATE INDEX IF NOT EXISTS idx_node_parent ON index_nodes(parent_id)`,

		`CREATE TABLE IF NOT EXISTS page_index (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL DEFAULT '',
			ref_kind TEXT,
			ref_id TEXT,
			metadata TEXT,
			priority INTEGER DEFAULT 5,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_page_index_user ON page_index(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_page_index_user_kind ON page_index(user_id, kind)`,

		`CREATE TABLE IF NOT EXISTS memory_ingest_cursor (
			session_id TEXT NOT NULL PRIMARY KEY,
			last_message_id INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS memory_ingest_stats (
			session_id TEXT NOT NULL,
			rule_name TEXT NOT NULL,
			count INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (session_id, rule_name)
		)`,
	}

	for _, m := range migrations {
		if _, err := s.db.Exec(m); err != nil {
			return err
		}
	}

	_, _ = s.db.Exec(`ALTER TABLE index_nodes ADD COLUMN prefix_summary TEXT DEFAULT ''`)

	return nil
}
