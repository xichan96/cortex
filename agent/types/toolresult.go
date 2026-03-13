package types

import (
	"strings"
)

func truncateText(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n…(truncated)…"
}

func sanitizeToolResultValue(v interface{}, maxTextLen int) interface{} {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case string:
		return truncateText(val, maxTextLen)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, elem := range val {
			if k == "content" {
				if arr, ok := elem.([]interface{}); ok {
					sanitized := make([]interface{}, 0, len(arr))
					for _, item := range arr {
						sanitized = append(sanitized, sanitizeContentItem(item, maxTextLen))
					}
					out[k] = sanitized
					continue
				}
			}
			out[k] = sanitizeToolResultValue(elem, maxTextLen)
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(val))
		for _, item := range val {
			out = append(out, sanitizeToolResultValue(item, maxTextLen))
		}
		return out
	default:
		return v
	}
}

func sanitizeContentItem(item interface{}, maxTextLen int) interface{} {
	m, ok := item.(map[string]interface{})
	if !ok {
		return item
	}
	typ, _ := m["type"].(string)
	switch strings.TrimSpace(strings.ToLower(typ)) {
	case "text":
		if text, ok := m["text"].(string); ok {
			out := make(map[string]interface{}, len(m))
			for k, v := range m {
				out[k] = v
			}
			out["text"] = truncateText(text, maxTextLen)
			return out
		}
	case "image":
		out := make(map[string]interface{}, len(m)+2)
		for k, v := range m {
			if k == "data" {
				continue
			}
			out[k] = v
		}
		if data, ok := m["data"].(string); ok {
			out["bytes"] = len(data)
		}
		out["omitted"] = true
		return out
	}
	return item
}

func SanitizeToolResult(result interface{}, maxTextLen int) interface{} {
	if maxTextLen <= 0 {
		maxTextLen = MaxTruncationLength
	}
	return sanitizeToolResultValue(result, maxTextLen)
}
