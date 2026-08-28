package mem

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	cortexSys "github.com/xichan96/cortex/agent/tools/builtin/system"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/memkit"
)

const (
	maxMemoryToolJSONBytes  = 256 * 1024
	maxKnowledgeSearchLimit = 100
	maxIndexSearchLimit     = 50
	maxPrefKeyRunes         = 200
	maxPrefValueRunes       = 8192
	maxKnowledgeWriteRunes  = 32000
	maxMemoryTagCount       = 32
	maxMemoryTagRunes       = 64
)

const defaultMemoryToolDescription = `Long-term memory (same SQLite as chat). Read: search_knowledge (query), get_preference (category/key), list_preferences, memory_stats. Write: set_preference (category/key/value), add_knowledge (content, category user|feedback|project|reference, optional tags), forget_knowledge (id from search results), forget_preference (category/key). Call writes only when the user clearly asks to remember something or when a durable fact should persist; skip trivial chat.`

// memoryAction 是模型的可见 action 全集（build_system_prompt 已从模型可见中移除）。
// 默认暴露子集由 MemoryToolOptions.HideInternalActions / ExposeSearchIndexes 控制。
var memoryActions = []string{
	"get_preference", "list_preferences",
	"search_knowledge",
	"search_indexes",
	"memory_stats",
	"set_preference", "add_knowledge",
	"forget_knowledge", "forget_preference",
}

type sqliteMemoryTool struct {
	sessionID            string
	mgr                  memkit.Manager
	name                 string
	writeTag             string
	description          string
	exposeSearchIndexes  bool
	hideInternalActions  bool
}

func newSQLiteMemoryTool(sessionID string, mgr memkit.Manager, name, writeTag, description string, exposeSearchIndexes, hideInternalActions bool) types.Tool {
	if description == "" {
		description = defaultMemoryToolDescription
	}
	return &sqliteMemoryTool{
		sessionID:           sessionID,
		mgr:                 mgr,
		name:                name,
		writeTag:            writeTag,
		description:         description,
		exposeSearchIndexes: exposeSearchIndexes,
		hideInternalActions: hideInternalActions,
	}
}

func (t *sqliteMemoryTool) Name() string { return t.name }

func (t *sqliteMemoryTool) Description() string { return t.description }

func (t *sqliteMemoryTool) Schema() map[string]interface{} {
	actions := make([]string, 0, len(memoryActions))
	for _, a := range memoryActions {
		if a == "search_indexes" && !t.exposeSearchIndexes {
			continue
		}
		actions = append(actions, a)
	}
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        actions,
				"description": "Operation: reads, writes, or forget.",
			},
			"category": map[string]interface{}{"type": "string"},
			"key":      map[string]interface{}{"type": "string"},
			"value":    map[string]interface{}{"type": "string", "description": "For set_preference."},
			"content":  map[string]interface{}{"type": "string", "description": "For add_knowledge: one atomic fact."},
			"tags": map[string]interface{}{
				"description": "For add_knowledge: optional semicolon-separated tags or JSON array of strings.",
			},
			"query": map[string]interface{}{"type": "string"},
			"limit": map[string]interface{}{"type": "integer"},
			"id":    map[string]interface{}{"type": "string", "description": "For forget_knowledge: the id returned by search_knowledge."},
		},
		"required": []string{"action"},
	}
}

func (t *sqliteMemoryTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{ToolType: "builtin", SourceNodeName: t.name}
}

func strArg(in map[string]interface{}, k string) string {
	v, ok := in[k]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func intArg(in map[string]interface{}, k string, def int) int {
	v, ok := in[k]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	default:
		return def
	}
}

func capLimit(v, def, max int) int {
	if v <= 0 {
		v = def
	}
	if v > max {
		return max
	}
	return v
}

func clipRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max])
}

func parseSemicolonTags(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(out) >= maxMemoryTagCount {
			break
		}
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, clipRunes(p, maxMemoryTagRunes))
	}
	return out
}

func tagsFromInput(in map[string]interface{}) []string {
	v, ok := in["tags"]
	if !ok || v == nil {
		return nil
	}
	switch x := v.(type) {
	case string:
		return parseSemicolonTags(x)
	case []interface{}:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if len(out) >= maxMemoryTagCount {
				break
			}
			var s string
			if st, ok := e.(string); ok {
				s = strings.TrimSpace(st)
			} else if e != nil {
				s = strings.TrimSpace(fmt.Sprint(e))
			}
			if s == "" {
				continue
			}
			out = append(out, clipRunes(s, maxMemoryTagRunes))
		}
		return out
	default:
		return parseSemicolonTags(fmt.Sprint(v))
	}
}

func normalizeKnowledgeCategory(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "user" || s == "feedback" || s == "project" || s == "reference" {
		return s
	}
	return "project"
}

func (t *sqliteMemoryTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.mgr == nil {
		return nil, fmt.Errorf("memory manager unavailable")
	}
	uid := t.sessionID
	act := strArg(input, "action")
	if act == "" {
		return nil, fmt.Errorf("action is required")
	}

	var (
		out interface{}
		err error
	)

	switch act {
	case "get_preference":
		if strArg(input, "key") == "" {
			return nil, fmt.Errorf("key is required")
		}
		var v string
		v, err = t.mgr.GetUserPreference(ctx, uid, strArg(input, "category"), strArg(input, "key"))
		out = map[string]string{"value": v}
	case "list_preferences":
		var m map[string]string
		m, err = t.mgr.GetUserPreferences(ctx, uid)
		out = m
	case "search_knowledge":
		lim := capLimit(intArg(input, "limit", 20), 20, maxKnowledgeSearchLimit)
		var items []memkit.MemoryItem
		items, err = t.mgr.SearchKnowledge(ctx, uid, strArg(input, "query"), lim)
		// 引用反馈候选登记：仅登记「返回给模型的条目」，不在这里计数
		// （B3：计数发生在 turn 结束后模型实际引用时）。
		recordSearchResults(t.sessionID, items)
		out = items
	case "search_indexes":
		lim := capLimit(intArg(input, "limit", 10), 10, maxIndexSearchLimit)
		var res *memkit.IndexSearchResult
		res, err = t.mgr.SearchIndexes(ctx, uid, strArg(input, "query"), lim)
		out = res
	case "memory_stats":
		var st *memkit.MemoryStats
		st, err = t.mgr.GetStats(ctx, uid)
		out = st
	case "forget_knowledge":
		id := strings.TrimSpace(strArg(input, "id"))
		if id == "" {
			return nil, fmt.Errorf("id is required")
		}
		err = t.mgr.Knowledge().Delete(ctx, id)
		out = map[string]any{"ok": true}
	case "forget_preference":
		cat := strings.TrimSpace(strArg(input, "category"))
		if cat == "" {
			cat = "user"
		}
		key := strings.TrimSpace(strArg(input, "key"))
		if key == "" {
			return nil, fmt.Errorf("key is required")
		}
		err = t.mgr.Preferences().Delete(ctx, uid, cat, key)
		out = map[string]any{"ok": true}
	case "set_preference":
		cat := strings.TrimSpace(strArg(input, "category"))
		if cat == "" {
			cat = "user"
		}
		key := strings.TrimSpace(strArg(input, "key"))
		val := strArg(input, "value")
		if key == "" {
			return nil, fmt.Errorf("key is required")
		}
		if strings.TrimSpace(val) == "" {
			return nil, fmt.Errorf("value is required")
		}
		key = clipRunes(key, maxPrefKeyRunes)
		val = clipRunes(val, maxPrefValueRunes)
		err = t.mgr.SetUserPreference(ctx, uid, cat, key, val)
		out = map[string]any{"ok": true}
	case "add_knowledge":
		body := strings.TrimSpace(strArg(input, "content"))
		if body == "" {
			return nil, fmt.Errorf("content is required")
		}
		body = clipRunes(body, maxKnowledgeWriteRunes)
		cat := normalizeKnowledgeCategory(strArg(input, "category"))
		tagList := tagsFromInput(input)
		allTags := make([]string, 0, 1+len(tagList))
		allTags = append(allTags, t.writeTag)
		for _, tg := range tagList {
			if len(allTags) >= 1+maxMemoryTagCount {
				break
			}
			allTags = append(allTags, tg)
		}
		err = t.mgr.AddKnowledgeWithCategory(ctx, uid, body, cat, allTags...)
		out = map[string]any{"ok": true}
	default:
		return nil, fmt.Errorf("unknown action %q", act)
	}
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if len(b) > maxMemoryToolJSONBytes {
		return nil, fmt.Errorf("result exceeds %d bytes; narrow query, lower limit, or shorten content", maxMemoryToolJSONBytes)
	}
	return string(b), nil
}

func MemoryTools(opts MemoryToolOptions) []types.Tool {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	name := opts.ToolName
	if name == "" {
		name = "memory"
	}
	writeTag := opts.WriteKnowledgeTag
	if writeTag == "" {
		writeTag = "memory_tool_write"
	}
	var tools []types.Tool
	if !opts.OmitTimeTool {
		tools = append(tools, cortexSys.NewTimeTool())
	}
	mgr, err := SharedSQLiteManager(opts.PersistDir, opts.SQLiteFile)
	if err != nil {
		log.Warn("memory tool disabled", "session_id", opts.SessionID, "persist_dir", opts.PersistDir, "error", err)
		return tools
	}
	if mgr == nil {
		log.Warn("memory tool disabled", "session_id", opts.SessionID, "persist_dir", opts.PersistDir, "error", "nil manager")
		return tools
	}
	tools = append(tools, newSQLiteMemoryTool(opts.SessionID, mgr, name, writeTag, opts.Description, opts.ExposeSearchIndexes, opts.HideInternalActions))
	return tools
}
