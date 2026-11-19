package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

// ToolArgumentError records why a finished tool call's argument text was not a
// JSON object. Raw keeps the exact text the model sent so validation can report
// it back; Arguments is empty whenever this is set.
type ToolArgumentError struct {
	Message string `json:"message"`
	Raw     string `json:"raw"`
}

// Error implements error.
func (e *ToolArgumentError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// ParseToolArguments parses the complete argument text of a finished tool call.
// It tolerates the repairable mistakes models commonly make (raw control
// characters and invalid escapes inside strings) but never keeps a partial
// prefix: text that is not a valid JSON object yields an empty argument map and
// a ToolArgumentError. It never panics.
func ParseToolArguments(text string) (map[string]any, *ToolArgumentError) {
	if strings.TrimSpace(text) == "" {
		return map[string]any{}, nil
	}
	parsed, err := parseJSONWithRepair(text)
	if err != nil {
		return map[string]any{}, &ToolArgumentError{Message: err.Error(), Raw: text}
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return map[string]any{}, &ToolArgumentError{
			Message: "tool arguments must be a JSON object",
			Raw:     text,
		}
	}
	return object, nil
}

// JSONArgsParser incrementally parses streamed tool-call argument text. Append
// each delta; Snapshot returns the value of the text received so far and can be
// called at any point. Finish performs the strict final parse that reports
// malformed arguments.
//
// Snapshot re-parses the accumulated text, which keeps the implementation
// tolerant and interruption-safe at the cost of O(n²) work over a long stream.
// Tool arguments are normally small; use a provider-specific parser only if a
// model emits megabytes of arguments.
type JSONArgsParser struct {
	text strings.Builder
}

// NewJSONArgsParser returns an empty parser.
func NewJSONArgsParser() *JSONArgsParser {
	return &JSONArgsParser{}
}

// Text returns the raw argument text received so far.
func (p *JSONArgsParser) Text() string {
	return p.text.String()
}

// Len returns the number of raw bytes received so far.
func (p *JSONArgsParser) Len() int {
	return p.text.Len()
}

// Reset discards all text.
func (p *JSONArgsParser) Reset() {
	p.text.Reset()
}

// Append adds one delta and returns the partial arguments, which are always an
// object (never nil). The returned map is a fresh snapshot owned by the caller.
func (p *JSONArgsParser) Append(delta string) map[string]any {
	p.text.WriteString(delta)
	return p.Snapshot()
}

// Snapshot returns the partial arguments of the text received so far. An
// unterminated string shows the characters read so far; an unterminated
// container shows the members/elements read so far; members without a value
// yet are omitted. Non-object partial values yield an empty map.
func (p *JSONArgsParser) Snapshot() map[string]any {
	value := parsePartialJSON(p.text.String())
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return map[string]any{}
	}
	return object
}

// Finish returns the strict final parse of the accumulated text.
func (p *JSONArgsParser) Finish() (map[string]any, *ToolArgumentError) {
	return ParseToolArguments(p.text.String())
}

// parseJSONWithRepair uses encoding/json first and only repairs the text when
// the strict parse fails.
func parseJSONWithRepair(text string) (any, error) {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err == nil {
		return value, nil
	} else {
		repaired := repairJSON(text)
		if repaired == text {
			return nil, err
		}
		var repairedValue any
		if repairErr := json.Unmarshal([]byte(repaired), &repairedValue); repairErr != nil {
			return nil, repairErr
		}
		return repairedValue, nil
	}
}

// repairJSON escapes raw control characters inside strings and doubles
// backslashes before invalid escape sequences. It mirrors the lenient parsing
// accepted by pi's repairJson().
func repairJSON(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	inString := false
	for i := 0; i < len(text); i++ {
		char := text[i]
		if !inString {
			out.WriteByte(char)
			if char == '"' {
				inString = true
			}
			continue
		}
		switch char {
		case '"':
			out.WriteByte(char)
			inString = false
		case '\\':
			if i+1 >= len(text) {
				out.WriteString(`\\`)
				continue
			}
			next := text[i+1]
			if next == 'u' && i+5 < len(text) && isHexString(text[i+2:i+6]) {
				out.WriteString(`\u`)
				out.WriteString(text[i+2 : i+6])
				i += 5
				continue
			}
			if strings.ContainsRune(`"\/bfnrt`, rune(next)) {
				out.WriteByte('\\')
				out.WriteByte(next)
				i++
				continue
			}
			out.WriteString(`\\`)
		default:
			if char < 0x20 {
				out.WriteString(escapeControlByte(char))
			} else {
				out.WriteByte(char)
			}
		}
	}
	// A dangling backslash at the end of an unterminated string is left as the
	// escaped backslash written above, which keeps the repaired text parseable.
	return out.String()
}

func escapeControlByte(char byte) string {
	switch char {
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	default:
		return fmt.Sprintf(`\u%04x`, char)
	}
}

func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// partialParser is a tolerant recursive-descent parser for text that may stop
// at any byte. It never panics and always makes progress.
type partialParser struct {
	text string
	pos  int
}

func parsePartialJSON(text string) any {
	p := &partialParser{text: text}
	value, _ := p.value()
	return value
}

func (p *partialParser) skipSpace() {
	for p.pos < len(p.text) {
		switch p.text[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *partialParser) value() (any, bool) {
	p.skipSpace()
	if p.pos >= len(p.text) {
		return nil, false
	}
	switch char := p.text[p.pos]; {
	case char == '{':
		return p.object()
	case char == '[':
		return p.array()
	case char == '"':
		return p.stringValue(), true
	case char == 't':
		return p.literal("true", true)
	case char == 'f':
		return p.literal("false", false)
	case char == 'n':
		return p.literal("null", nil)
	case char == '-' || (char >= '0' && char <= '9'):
		return p.number()
	default:
		// Unknown word: drop it. Advance at least one byte so callers progress.
		p.pos++
		for p.pos < len(p.text) && !isJSONDelimiter(p.text[p.pos]) {
			p.pos++
		}
		return nil, false
	}
}

func (p *partialParser) literal(word string, value any) (any, bool) {
	rest := p.text[p.pos:]
	if strings.HasPrefix(word, rest) {
		// The text is an unterminated prefix of the literal; show its value.
		p.pos = len(p.text)
		return value, true
	}
	if strings.HasPrefix(rest, word) {
		p.pos += len(word)
		return value, true
	}
	// Malformed literal: skip the word.
	for p.pos < len(p.text) && !isJSONDelimiter(p.text[p.pos]) {
		p.pos++
	}
	return nil, false
}

func (p *partialParser) number() (any, bool) {
	start := p.pos
	for p.pos < len(p.text) {
		char := p.text[p.pos]
		if (char >= '0' && char <= '9') || char == '-' || char == '+' || char == '.' || char == 'e' || char == 'E' {
			p.pos++
			continue
		}
		break
	}
	token := p.text[start:p.pos]
	if value, err := strconv.ParseFloat(token, 64); err == nil {
		return value, true
	}
	// An exponent without digits is ignored, as pi's streaming parser does.
	if trimmed := trimIncompleteExponent(token); trimmed != token {
		if value, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return value, true
		}
	}
	return nil, false
}

func trimIncompleteExponent(token string) string {
	i := len(token)
	if i > 0 && (token[i-1] == 'e' || token[i-1] == 'E') {
		return token[:i-1]
	}
	if i > 1 && (token[i-1] == '+' || token[i-1] == '-') && (token[i-2] == 'e' || token[i-2] == 'E') {
		return token[:i-2]
	}
	return token
}

// stringValue reads a string, tolerating an unterminated tail and invalid
// escapes. The opening quote must be at p.pos.
func (p *partialParser) stringValue() string {
	p.pos++ // opening quote
	var out strings.Builder
	for p.pos < len(p.text) {
		char := p.text[p.pos]
		switch char {
		case '"':
			p.pos++
			return out.String()
		case '\\':
			p.pos++
			if p.pos >= len(p.text) {
				// Dangling escape: hold it back, matching the streaming view of
				// an escape that has not completed yet.
				return out.String()
			}
			escape := p.text[p.pos]
			switch escape {
			case '"', '\\', '/':
				out.WriteByte(escape)
				p.pos++
			case 'b':
				out.WriteByte('\b')
				p.pos++
			case 'f':
				out.WriteByte('\f')
				p.pos++
			case 'n':
				out.WriteByte('\n')
				p.pos++
			case 'r':
				out.WriteByte('\r')
				p.pos++
			case 't':
				out.WriteByte('\t')
				p.pos++
			case 'u':
				if p.pos+4 < len(p.text) {
					hex := p.text[p.pos+1 : p.pos+5]
					if code, err := strconv.ParseUint(hex, 16, 16); err == nil {
						r := rune(code)
						p.pos += 5
						if utf16.IsSurrogate(r) {
							if p.pos+6 <= len(p.text) && p.text[p.pos] == '\\' && p.text[p.pos+1] == 'u' {
								if low, err := strconv.ParseUint(p.text[p.pos+2:p.pos+6], 16, 16); err == nil {
									if decoded := utf16.DecodeRune(r, rune(low)); decoded != '�' {
										out.WriteRune(decoded)
										p.pos += 6
										continue
									}
								}
							}
							out.WriteRune('�')
							continue
						}
						out.WriteRune(r)
						continue
					}
				}
				// Incomplete or invalid \u sequence: keep it as literal text.
				out.WriteByte('\\')
				out.WriteByte('u')
				p.pos++
			default:
				// Invalid escape: keep the backslash literal and re-read the
				// escaped character on the next iteration.
				out.WriteByte('\\')
			}
		default:
			out.WriteByte(char)
			p.pos++
		}
	}
	return out.String()
}

func (p *partialParser) object() (any, bool) {
	p.pos++ // {
	object := make(map[string]any)
	for {
		p.skipSpace()
		if p.pos >= len(p.text) {
			return object, true
		}
		switch p.text[p.pos] {
		case '}':
			p.pos++
			return object, true
		case ',':
			p.pos++
			continue
		case '"':
			key := p.stringValue()
			p.skipSpace()
			if p.pos < len(p.text) && p.text[p.pos] == ':' {
				p.pos++
			}
			p.skipSpace()
			if p.pos >= len(p.text) {
				// The value has not started yet: omit the member.
				return object, true
			}
			if p.text[p.pos] == '}' || p.text[p.pos] == ',' {
				// Missing value: omit the member.
				if p.text[p.pos] == ',' {
					p.pos++
				}
				continue
			}
			start := p.pos
			value, ok := p.value()
			if ok {
				object[key] = value
			}
			if p.pos == start {
				p.pos++
			}
		default:
			// Missing key (or garbage): skip one byte so parsing progresses.
			p.pos++
		}
	}
}

func (p *partialParser) array() (any, bool) {
	p.pos++ // [
	array := make([]any, 0)
	for {
		p.skipSpace()
		if p.pos >= len(p.text) {
			return array, true
		}
		switch p.text[p.pos] {
		case ']':
			p.pos++
			return array, true
		case ',':
			p.pos++
			continue
		}
		start := p.pos
		value, ok := p.value()
		if ok {
			array = append(array, value)
		}
		if p.pos == start {
			p.pos++
		}
	}
}

func isJSONDelimiter(char byte) bool {
	switch char {
	case ' ', '\t', '\n', '\r', ',', ':', '{', '}', '[', ']', '"':
		return true
	default:
		return false
	}
}
