package sqlite

import (
	"encoding/json"
	"fmt"
	"strings"
)

func escapeLikePattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func coalesceString(s, defaultVal string) string {
	if s == "" {
		return defaultVal
	}
	return s
}

func joinTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return strings.Join(tags, ",")
}

func parseTagsFromDB(content string) []string {
	if content == "" {
		return []string{}
	}
	parts := strings.Split(content, ",")
	var tags []string
	for _, t := range parts {
		t = strings.TrimSpace(t)
		t = strings.Trim(t, "\"")
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

func safeJSONMarshal(v interface{}) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("json marshal error: %w", err)
	}
	return string(b), nil
}

func safeJSONUnmarshal(data string, v interface{}) error {
	if data == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(data), v); err != nil {
		return fmt.Errorf("json unmarshal error: %w", err)
	}
	return nil
}
