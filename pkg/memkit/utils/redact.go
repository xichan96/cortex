package utils

import "regexp"

// 常见密钥形态的确定性脱敏（对照 codex phase1 前后都 redact）。
//
// 两种形态：
//   - `key=value` / `key: value`（单 token）：`api_key=sk-abc` → `api_key: [REDACTED_SECRET]`
//   - `Authorization: Bearer <jwt>` / `Bearer <token>`（多 token，直到行尾或逗号）：
//     `Authorization: Bearer eyJ...` → `Authorization: [REDACTED_SECRET]`
//
// 只匹配「键 + 分隔符 + 值」的常见形态，避免误伤普通文本。
var (
	secretValueRe = regexp.MustCompile(`(?i)((?:api[_-]?key|token|password|secret|access[_-]?key|private[_-]?key|client[_-]?secret))\s*[=:]\s*[^\s,;]+`)
	bearerValueRe = regexp.MustCompile(`(?i)(\b(?:authorization|bearer)\s*[=:]\s+)[A-Za-z0-9._~+/= -]+`)
)

// RedactSecrets 将文本中的密钥值替换为 [REDACTED_SECRET]，返回脱敏后的字符串。
// 幂等：已脱敏的文本再次调用不改变结果。
func RedactSecrets(s string) string {
	if s == "" {
		return s
	}
	s = secretValueRe.ReplaceAllString(s, "${1}: [REDACTED_SECRET]")
	// 先处理 authorization/bearer：整体替换键、可选 Bearer 词与值。
	s = bearerValueRe.ReplaceAllString(s, "${1}[REDACTED_SECRET]")
	return s
}
