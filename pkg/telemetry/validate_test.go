package telemetry

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestValidateAttributesAcceptsControlMetadata(t *testing.T) {
	cases := map[string]Attributes{
		"nil":                   nil,
		"empty":                 {},
		"id":                    {"cortex.agent.id": "run-1"},
		"duration":              {"cortex.agent.duration_ms": 12},
		"name":                  {"cortex.tool.name": "read"},
		"int64 count":           {"cortex.tool.call_count": int64(3)},
		"message count":         {"cortex.session.message_count": 4},
		"input count":           {"input_count": 1},
		"content length":        {"content_length": 100},
		"token count":           {"llm.token_count": 42},
		"max tokens":            {"llm.max_tokens": 1024},
		"bool":                  {"cortex.agent.enabled": true},
		"string slice":          {"cortex.agent.kinds": []string{"a", "b"}},
		"float slice":           {"cortex.agent.values": []float64{1.5, 2}},
		"int slice":             {"cortex.agent.counts": []int{1, 2}},
		"bool slice":            {"cortex.agent.flags": []bool{true, false}},
		"uint8 scalar":          {"cortex.agent.byte_count": uint8(2)},
		"status":                {"cortex.tool.status": "ok"},
		"file count":            {"cortex.session.file_count": 2},
		"prompt length":         {"cortex.agent.prompt_length": 300},
		"max string len value":  {"cortex.agent.name": strings.Repeat("a", MaxStringAttributeLength)},
		"tab-free string value": {"cortex.agent.name": "tool.read"},
	}
	for name, attributes := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAttributes(attributes); err != nil {
				t.Fatalf("ValidateAttributes(%v) = %v, want nil", attributes, err)
			}
		})
	}
}

func TestValidateAttributesRejectsSensitiveKeys(t *testing.T) {
	keys := []string{
		"prompt",
		"cortex.prompt",
		"gen_ai.prompt",
		"message.content",
		"cortex.tool.args",
		"tool.arguments",
		"cortex.tool.result",
		"tool.output",
		"http.request.headers.authorization",
		"authorization",
		"X-Api-Key",
		"apiKey",
		"api_key",
		"access_token",
		"refresh_token",
		"password",
		"client_secret",
		"Set-Cookie",
		"cortex.session.transcript",
		"file.path",
		"error.message",
		"http.headers",
		"tool.payload",
		"",
		"   ",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			err := ValidateAttributes(Attributes{key: "value"})
			if err == nil {
				t.Fatalf("ValidateAttributes accepted sensitive key %q", key)
			}
			if !errors.Is(err, ErrInvalidAttributes) {
				t.Fatalf("error %v does not wrap ErrInvalidAttributes", err)
			}
			var attributeErr *AttributeError
			if !errors.As(err, &attributeErr) {
				t.Fatalf("error %v is not an *AttributeError", err)
			}
			if attributeErr.Key != key {
				t.Fatalf("AttributeError.Key = %q, want %q", attributeErr.Key, key)
			}
		})
	}
}

func TestValidateAttributesRejectsUnsafeValues(t *testing.T) {
	type payload struct{ Content string }
	cases := map[string]Attributes{
		"nil value":           {"cortex.agent.meta": nil},
		"map value":           {"cortex.agent.meta": map[string]string{"a": "b"}},
		"struct value":        {"cortex.agent.meta": payload{Content: "secret"}},
		"pointer value":       {"cortex.agent.meta": &payload{}},
		"byte slice":          {"cortex.agent.blob": []byte("file contents")},
		"interface slice":     {"cortex.agent.mixed": []any{1}},
		"long string":         {"cortex.agent.name": strings.Repeat("a", MaxStringAttributeLength+1)},
		"long string element": {"cortex.agent.names": []string{strings.Repeat("a", MaxStringAttributeLength+1)}},
		"newline in string":   {"cortex.agent.name": "line\nbreak"},
		"control in string":   {"cortex.agent.name": "escape\x1bsequence"},
		"newline in element":  {"cortex.agent.names": []string{"ok", "bad\nvalue"}},
		"NaN":                 {"cortex.agent.ratio": math.NaN()},
		"positive infinity":   {"cortex.agent.ratio": math.Inf(1)},
		"negative infinity":   {"cortex.agent.ratio": math.Inf(-1)},
	}
	for name, attributes := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateAttributes(attributes)
			if err == nil {
				t.Fatalf("ValidateAttributes(%v) accepted an unsafe value", attributes)
			}
			if !errors.Is(err, ErrInvalidAttributes) {
				t.Fatalf("error %v does not wrap ErrInvalidAttributes", err)
			}
		})
	}
}

func TestKeySegments(t *testing.T) {
	cases := map[string][]string{
		"cortex.tool.name": []string{"cortex", "tool", "name"},
		"X-Api-Key":        []string{"x", "api", "key"},
		"apiKey":           []string{"api", "key"},
		"a__b..c":          []string{"a", "b", "c"},
		"...":              nil,
		"HTTP2Request":     []string{"http2", "request"},
	}
	for key, want := range cases {
		if got := keySegments(key); !equalStrings(got, want) {
			t.Fatalf("keySegments(%q) = %v, want %v", key, got, want)
		}
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
