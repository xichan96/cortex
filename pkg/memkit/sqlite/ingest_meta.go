package sqlite

import (
	"context"
	"database/sql"
)

func IngestGetCursor(ctx context.Context, db *sql.DB, sessionID string) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `SELECT last_message_id FROM memory_ingest_cursor WHERE session_id = ?`, sessionID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

func IngestSetCursor(ctx context.Context, db *sql.DB, sessionID string, lastMessageID int64) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO memory_ingest_cursor (session_id, last_message_id) VALUES (?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET last_message_id = excluded.last_message_id`,
		sessionID, lastMessageID)
	return err
}

func IngestIncStat(ctx context.Context, db *sql.DB, sessionID, ruleName string) error {
	if ruleName == "" {
		ruleName = "default"
	}
	_, err := db.ExecContext(ctx,
		`INSERT INTO memory_ingest_stats (session_id, rule_name, count) VALUES (?, ?, 1)
		 ON CONFLICT(session_id, rule_name) DO UPDATE SET count = count + 1`,
		sessionID, ruleName)
	return err
}
