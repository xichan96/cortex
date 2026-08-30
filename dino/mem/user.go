package mem

import (
	"context"
	"database/sql"
)

// P3.2 user 全局合并：session → user 的归属载体是 metadata 表的 `user_id` 键。
// 纯函数/无状态的归属解析放这里，便于单测。

// defaultUserIDFallback 是「未显式归属且无配置」时的兜底 user。
const defaultUserIDFallback = "default"

// ResolveUserID 解析一个 session 的归属 user。
// 优先级：sessionConfigUserID（WithUserID）> defaultUserID（配置）> defaultUserIDFallback。
func ResolveUserID(sessionConfigUserID, defaultUserID string) string {
	if sessionConfigUserID != "" {
		return sessionConfigUserID
	}
	if defaultUserID != "" {
		return defaultUserID
	}
	return defaultUserIDFallback
}

// UserIDForSession 以 metadata.user_id 为 uid 单一事实源（评审 B3 修法）：
// 读 metadata 表，有归属返回归属；无归属返回 sessionID（per-session 语义）。
// 工具/L1/ingest 三处都走这里，避免「内存解析值 vs metadata」双源漂移。
func UserIDForSession(ctx context.Context, db *sql.DB, sessionID string) string {
	var userID string
	err := db.QueryRowContext(ctx,
		`SELECT value FROM metadata WHERE session_id = ? AND key = 'user_id'`, sessionID).Scan(&userID)
	if err == nil && userID != "" {
		return userID
	}
	return sessionID
}
