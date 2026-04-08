package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/xichan96/cortex/dino/chatstore"
)

const maxSanitizedSessionIDLen = 128

var (
	ErrPersistedSessionNotFound = errors.New("persisted session not found")

	hostRuntimeUserInjectRx = regexp.MustCompile(`(?s)\A\[[Hh]ost local date/time[^\]]*\]\s*\n*`)
)

type ChatHistoryEntry struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	ToolCalls string `json:"tool_calls,omitempty"`
}

type PersistedSessionInfo struct {
	ID        string
	Title     string
	UpdatedAt time.Time
}

func StripHostRuntimeUserInject(s string) string {
	return hostRuntimeUserInjectRx.ReplaceAllLiteralString(s, "")
}

func StripHostRuntimeFromHistoryContent(role, content string) string {
	r := strings.TrimSpace(role)
	if r != "" && !strings.EqualFold(r, "user") {
		return content
	}
	c := strings.TrimSpace(content)
	if strings.HasPrefix(c, "Input: ") {
		payload := strings.TrimSpace(strings.TrimPrefix(c, "Input: "))
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(payload), &obj) == nil {
			if raw, ok := obj["input"]; ok {
				var s string
				if json.Unmarshal(raw, &s) == nil {
					s2 := StripHostRuntimeUserInject(s)
					if b, err := json.Marshal(s2); err == nil {
						obj["input"] = json.RawMessage(b)
						if out, err := json.Marshal(obj); err == nil {
							return "Input: " + string(out)
						}
					}
				}
			}
		}
	}
	return StripHostRuntimeUserInject(content)
}

func SanitizeSessionIDForFile(id string) string {
	if len(id) > maxSanitizedSessionIDLen {
		id = id[:maxSanitizedSessionIDLen]
	}
	var b strings.Builder
	b.Grow(len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func LegacySessionDBPath(sessionID, persistDir string) string {
	san := SanitizeSessionIDForFile(sessionID)
	return filepath.Join(persistDir, fmt.Sprintf("session_%s.db", san))
}

func MessageContentAsSessionListTitle(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	payload := s
	if after, ok := strings.CutPrefix(s, "Input: "); ok {
		payload = strings.TrimSpace(after)
	}
	var obj struct {
		Input string `json:"input"`
		Parts []struct {
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := json.Unmarshal([]byte(payload), &obj); err == nil {
		if t := strings.TrimSpace(obj.Input); t != "" {
			return StripHostRuntimeUserInject(t)
		}
		for _, p := range obj.Parts {
			if t := strings.TrimSpace(p.Text); t != "" {
				return StripHostRuntimeUserInject(t)
			}
		}
	}
	return StripHostRuntimeUserInject(s)
}

func truncateSessionTitle(s string) string {
	s = MessageContentAsSessionListTitle(s)
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= 80 {
		return s
	}
	return string(r[:80]) + "…"
}

func TruncateSessionListTitle(s string) string {
	return truncateSessionTitle(s)
}

func readFirstUserMessageTitle(ctx context.Context, dbPath string) string {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return ""
	}
	db, err := sql.Open("sqlite3", abs+"?mode=ro")
	if err != nil {
		return ""
	}
	defer db.Close()
	var content string
	q := `SELECT content FROM messages WHERE LOWER(role) = 'user' ORDER BY timestamp ASC LIMIT 1`
	if err := db.QueryRowContext(ctx, q).Scan(&content); err != nil {
		return ""
	}
	return truncateSessionTitle(content)
}

func parseSQLiteTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local); err == nil {
		return t
	}
	return time.Now()
}

func listLegacySessionFilesOnly(dir, sharedSQLiteFile string) ([]PersistedSessionInfo, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []PersistedSessionInfo
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == sharedSQLiteFile || !strings.HasPrefix(name, "session_") || !strings.HasSuffix(name, ".db") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "session_"), ".db")
		if id == "" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		title := readFirstUserMessageTitle(context.Background(), filepath.Join(dir, name))
		out = append(out, PersistedSessionInfo{ID: id, Title: title, UpdatedAt: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func readLegacySQLiteChatHistory(ctx context.Context, dbPath string) ([]ChatHistoryEntry, error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", abs+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	q := "SELECT role, content, tool_calls FROM messages ORDER BY timestamp ASC"
	rows, err := db.QueryContext(ctx, q)
	threeCol := true
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such column") {
			rows, err = db.QueryContext(ctx, "SELECT role, content FROM messages ORDER BY timestamp ASC")
			threeCol = false
		}
		if err != nil {
			return nil, err
		}
	}
	defer rows.Close()

	var out []ChatHistoryEntry
	for rows.Next() {
		var role, content string
		if threeCol {
			var tc sql.NullString
			if err := rows.Scan(&role, &content, &tc); err != nil {
				return nil, err
			}
			tcs := ""
			if tc.Valid {
				tcs = tc.String
			}
			out = append(out, ChatHistoryEntry{Role: role, Content: StripHostRuntimeFromHistoryContent(role, content), ToolCalls: tcs})
			continue
		}
		if err := rows.Scan(&role, &content); err != nil {
			return nil, err
		}
		out = append(out, ChatHistoryEntry{Role: role, Content: StripHostRuntimeFromHistoryContent(role, content)})
	}
	return out, rows.Err()
}

func ListPersistedSessions(dir, sharedSQLiteFile string) ([]PersistedSessionInfo, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	db, err := chatstore.OpenSharedChatStore(dir, sharedSQLiteFile)
	if err != nil {
		return listLegacySessionFilesOnly(dir, sharedSQLiteFile)
	}
	rows, err := db.Query(`
SELECT agg.session_id, agg.mt,
  (SELECT content FROM messages m2 WHERE m2.session_id = agg.session_id AND LOWER(m2.role) = 'user' ORDER BY m2.timestamp ASC LIMIT 1) AS first_user
FROM (
  SELECT session_id, MAX(timestamp) AS mt FROM messages GROUP BY session_id
) agg
ORDER BY agg.mt DESC`)
	if err != nil {
		return listLegacySessionFilesOnly(dir, sharedSQLiteFile)
	}
	defer rows.Close()
	var out []PersistedSessionInfo
	for rows.Next() {
		var sid, ts string
		var firstUser sql.NullString
		if err := rows.Scan(&sid, &ts, &firstUser); err != nil {
			return nil, err
		}
		title := ""
		if firstUser.Valid && firstUser.String != "" {
			title = truncateSessionTitle(MessageContentAsSessionListTitle(firstUser.String))
		}
		out = append(out, PersistedSessionInfo{ID: sid, Title: title, UpdatedAt: parseSQLiteTime(ts)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	legacy, err := listLegacySessionFilesOnly(dir, sharedSQLiteFile)
	if err != nil {
		return out, nil
	}
	seen := make(map[string]bool, len(out))
	for _, p := range out {
		seen[p.ID] = true
	}
	for _, p := range legacy {
		if !seen[p.ID] {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func RemovePersistedSession(dir, sharedSQLiteFile, sessionID string) error {
	if sessionID == "" {
		return ErrPersistedSessionNotFound
	}
	db, err := chatstore.OpenSharedChatStore(dir, sharedSQLiteFile)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	res, err := tx.Exec("DELETE FROM messages WHERE session_id = ?", sessionID)
	if err != nil {
		tx.Rollback()
		return err
	}
	_, err = tx.Exec("DELETE FROM metadata WHERE session_id = ?", sessionID)
	if err != nil {
		tx.Rollback()
		return err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	legacy := LegacySessionDBPath(sessionID, dir)
	if err := os.Remove(legacy); err == nil {
		return nil
	}
	return ErrPersistedSessionNotFound
}

func ReadPersistedChatHistory(ctx context.Context, dir, sharedSQLiteFile, sessionID string) ([]ChatHistoryEntry, error) {
	if sessionID == "" {
		return nil, ErrPersistedSessionNotFound
	}
	db, err := chatstore.OpenSharedChatStore(dir, sharedSQLiteFile)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT role, content, tool_calls FROM messages WHERE session_id = ? ORDER BY timestamp ASC", sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatHistoryEntry
	for rows.Next() {
		var role, content string
		var tc sql.NullString
		if err := rows.Scan(&role, &content, &tc); err != nil {
			return nil, err
		}
		tcs := ""
		if tc.Valid {
			tcs = tc.String
		}
		out = append(out, ChatHistoryEntry{Role: role, Content: StripHostRuntimeFromHistoryContent(role, content), ToolCalls: tcs})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		return out, nil
	}
	legacy := LegacySessionDBPath(sessionID, dir)
	if _, err := os.Stat(legacy); err == nil {
		return readLegacySQLiteChatHistory(ctx, legacy)
	}
	return nil, ErrPersistedSessionNotFound
}
