package telemetry

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"unicode"
)

// MaxStringAttributeLength is the longest string (or string element) that
// ValidateAttributes accepts. It is a guard against accidentally capturing
// prompts, completions, file contents, or other payloads.
const MaxStringAttributeLength = 1024

// ErrInvalidAttributes is wrapped by every error returned by
// [ValidateAttributes].
var ErrInvalidAttributes = errors.New("telemetry: invalid attributes")

// AttributeError identifies the attribute key that failed validation.
type AttributeError struct {
	Key    string
	Reason string
}

func (e *AttributeError) Error() string {
	if e.Key == "" {
		return fmt.Sprintf("telemetry: invalid attributes: %s", e.Reason)
	}
	return fmt.Sprintf("telemetry: invalid attribute %q: %s", e.Key, e.Reason)
}

// Unwrap reports [ErrInvalidAttributes].
func (e *AttributeError) Unwrap() error { return ErrInvalidAttributes }

// hardDenySegments are key words that are sensitive wherever they appear.
var hardDenySegments = map[string]struct{}{
	"authorization": {},
	"cookie":        {},
	"password":      {},
	"passwd":        {},
	"pwd":           {},
	"secret":        {},
	"secrets":       {},
	"credential":    {},
	"credentials":   {},
	"apikey":        {},
	"bearer":        {},
	"pem":           {},
}

// hardDenyPairs are adjacent normalized key words that are sensitive together.
var hardDenyPairs = [][2]string{
	{"api", "key"},
	{"access", "token"},
	{"refresh", "token"},
	{"id", "token"},
	{"auth", "token"},
	{"session", "token"},
	{"private", "key"},
	{"client", "secret"},
	{"secret", "key"},
	{"set", "cookie"},
}

// contentSegments name user payloads. They are rejected unless the key ends in
// a metadata suffix, so "input_count" and "content_length" remain valid while
// "tool.args" and "message.content" do not.
var contentSegments = map[string]struct{}{
	"prompt":      {},
	"prompts":     {},
	"completion":  {},
	"completions": {},
	"message":     {},
	"messages":    {},
	"content":     {},
	"contents":    {},
	"body":        {},
	"payload":     {},
	"headers":     {},
	"header":      {},
	"args":        {},
	"argument":    {},
	"arguments":   {},
	"result":      {},
	"results":     {},
	"output":      {},
	"outputs":     {},
	"transcript":  {},
	"file":        {},
	"files":       {},
	"data":        {},
	"input":       {},
	"inputs":      {},
	"stdout":      {},
	"stderr":      {},
}

// metadataSuffixes are terminal key words that describe shape rather than
// payload, such as counts and sizes.
var metadataSuffixes = map[string]struct{}{
	"count":    {},
	"length":   {},
	"size":     {},
	"bytes":    {},
	"type":     {},
	"name":     {},
	"id":       {},
	"kind":     {},
	"status":   {},
	"duration": {},
	"ms":       {},
	"total":    {},
	"sum":      {},
	"index":    {},
	"version":  {},
	"enabled":  {},
	"hash":     {},
	"encoding": {},
}

// ValidateAttributes checks that attrs contain only control metadata.
//
// It rejects known-sensitive keys (authorization, cookies, credentials,
// prompts, message contents, tool arguments and results, headers, file
// payloads, transcripts), unsupported value shapes (maps, structs, pointers,
// byte slices), control characters in strings, over-long strings, and
// non-finite floats. The key check is a heuristic deny-list; callers remain
// responsible for not recording user payloads under innocuous names.
//
// Adapters do not call this on every span. Use it in tests and at trust
// boundaries where instrumentation-provided attributes enter a backend.
func ValidateAttributes(attrs Attributes) error {
	if len(attrs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(attrs))
	for key := range attrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := attrs[key]
		if key == "" || strings.TrimSpace(key) == "" {
			return &AttributeError{Key: key, Reason: "attribute key is empty"}
		}
		if reason, denied := sensitiveKey(key); denied {
			return &AttributeError{Key: key, Reason: reason}
		}
		if err := validateAttributeValue(key, value); err != nil {
			return err
		}
	}
	return nil
}

// sensitiveKey reports whether a dotted or hyphenated key names user content
// or a secret.
func sensitiveKey(key string) (string, bool) {
	segments := keySegments(key)
	if len(segments) == 0 {
		return "attribute key has no alphanumeric characters", true
	}
	for _, segment := range segments {
		if _, denied := hardDenySegments[segment]; denied {
			return "sensitive attribute key: credentials and headers must not be recorded", true
		}
	}
	for index := 0; index+1 < len(segments); index++ {
		pair := [2]string{segments[index], segments[index+1]}
		for _, denied := range hardDenyPairs {
			if pair == denied {
				return "sensitive attribute key: credentials and headers must not be recorded", true
			}
		}
	}

	content := false
	for _, segment := range segments {
		if _, isContent := contentSegments[segment]; isContent {
			content = true
			break
		}
	}
	if !content {
		return "", false
	}
	if _, metadata := metadataSuffixes[segments[len(segments)-1]]; metadata {
		return "", false
	}
	return "attribute may capture user content; record only control metadata such as ids, names, counts, status, and durations", true
}

// keySegments splits a key on non-alphanumeric characters and camel-case
// boundaries, lowercasing each word. "X-Api-Key" becomes [x api key].
func keySegments(key string) []string {
	runes := []rune(key)
	segments := make([]string, 0, 4)
	var current []rune
	flush := func() {
		if len(current) > 0 {
			segments = append(segments, string(current))
			current = current[:0]
		}
	}
	for index, char := range runes {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) {
			flush()
			continue
		}
		if unicode.IsUpper(char) && len(current) > 0 {
			previous := runes[index-1]
			if unicode.IsLower(previous) || unicode.IsDigit(previous) {
				flush()
			}
		}
		current = append(current, unicode.ToLower(char))
	}
	flush()
	return segments
}

func validateAttributeValue(key string, value any) error {
	if value == nil {
		return &AttributeError{Key: key, Reason: "nil attribute values are not supported"}
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Bool:
		return nil
	case reflect.String:
		return validateStringAttribute(key, rv.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return nil
	case reflect.Float32, reflect.Float64:
		number := rv.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return &AttributeError{Key: key, Reason: "non-finite float values are not supported"}
		}
		return nil
	case reflect.Slice:
		switch rv.Type().Elem().Kind() {
		case reflect.Uint8:
			return &AttributeError{Key: key, Reason: "byte slices are not supported; they may contain file contents"}
		case reflect.String:
			for index := 0; index < rv.Len(); index++ {
				if err := validateStringAttribute(key, rv.Index(index).String()); err != nil {
					return err
				}
			}
			return nil
		case reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			return nil
		default:
			return &AttributeError{Key: key, Reason: "unsupported slice element type"}
		}
	default:
		return &AttributeError{Key: key, Reason: fmt.Sprintf("unsupported value type %T; use scalars or slices of scalars", value)}
	}
}

func validateStringAttribute(key, value string) error {
	if len(value) > MaxStringAttributeLength {
		return &AttributeError{Key: key, Reason: fmt.Sprintf("string value is %d bytes; the limit is %d to avoid capturing user content", len(value), MaxStringAttributeLength)}
	}
	for _, char := range value {
		if char < 0x20 || (char >= 0x7f && char <= 0x9f) {
			return &AttributeError{Key: key, Reason: "string values must not contain control characters or newlines"}
		}
	}
	return nil
}
