package model

import (
	"reflect"
	"testing"
)

func TestJSONArgsParserSegmentedToolCall(t *testing.T) {
	parser := NewJSONArgsParser()

	partial := parser.Append(`{"path": "/tmp/a`)
	if partial["path"] != "/tmp/a" {
		t.Fatalf("partial path = %v", partial["path"])
	}

	partial = parser.Append(`bc", "count": 2`)
	if partial["path"] != "/tmp/abc" || partial["count"] != float64(2) {
		t.Fatalf("partial = %+v", partial)
	}

	partial = parser.Append(`, "nested": {"x": [1, 2`)
	nested, ok := partial["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested not parsed: %+v", partial)
	}
	items, ok := nested["x"].([]any)
	if !ok || len(items) != 2 || items[0] != float64(1) || items[1] != float64(2) {
		t.Fatalf("nested array = %+v", nested["x"])
	}

	partial = parser.Append(`]}}`)
	nested = partial["nested"].(map[string]any)
	items = nested["x"].([]any)
	if len(items) != 2 {
		t.Fatalf("final nested array = %+v", items)
	}

	args, argErr := parser.Finish()
	if argErr != nil {
		t.Fatalf("Finish: %v", argErr)
	}
	want := map[string]any{
		"path":   "/tmp/abc",
		"count":  float64(2),
		"nested": map[string]any{"x": []any{float64(1), float64(2)}},
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("final args = %#v, want %#v", args, want)
	}
}

func TestJSONArgsParserPartialValues(t *testing.T) {
	tests := []struct {
		name     string
		streamed string
		key      string
		want     any
	}{
		{"unterminated string", `{"s": "hel`, "s", "hel"},
		{"escaped newline", `{"s": "a\n`, "s", "a\n"},
		{"literal prefix", `{"ok": tru`, "ok", true},
		{"number exponent incomplete", `{"n": 1e`, "n", float64(1)},
		{"number without exponent digits", `{"n": 1e+`, "n", float64(1)},
		{"missing colon and comma", `{"a" 1 "b": 2}`, "b", float64(2)},
		{"trailing comma", `{"a": 1,`, "a", float64(1)},
		{"unterminated escape held back", `{"s": "a\`, "s", "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := NewJSONArgsParser().Append(tt.streamed)
			if got := value[tt.key]; !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("%s = %#v, want %#v (full: %#v)", tt.key, got, tt.want, value)
			}
		})
	}
}

func TestJSONArgsParserAllPrefixesAreSafe(t *testing.T) {
	document := `{"a":"x\ny","b":[true,false,null,12.5],"c":{"d":"\u00e9"}}`
	for i := 0; i <= len(document); i++ {
		parser := NewJSONArgsParser()
		partial := parser.Append(document[:i])
		if partial == nil {
			t.Fatalf("prefix %d: nil snapshot", i)
		}
	}
	parser := NewJSONArgsParser()
	parser.Append(document)
	finish, argErr := parser.Finish()
	if argErr != nil {
		t.Fatalf("Finish valid document: %v", argErr)
	}
	if finish["a"] != "x\ny" {
		t.Fatalf("a = %q", finish["a"])
	}
	array := finish["b"].([]any)
	if len(array) != 4 || array[2] != nil || array[3] != float64(12.5) {
		t.Fatalf("b = %#v", array)
	}
	nested := finish["c"].(map[string]any)
	if nested["d"] != "é" {
		t.Fatalf("c.d = %q", nested["d"])
	}
}

func TestParseToolArgumentsErrors(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"truncated object", `{"a": 1`},
		{"missing value", `{"a": }`},
		{"not an object", `[1,2]`},
		{"plain text", `read file please`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, argErr := ParseToolArguments(tt.text)
			if argErr == nil {
				t.Fatalf("expected error, got args %#v", args)
			}
			if len(args) != 0 {
				t.Fatalf("arguments should be empty on error, got %#v", args)
			}
			if argErr.Raw != tt.text || argErr.Message == "" {
				t.Fatalf("error missing context: %+v", argErr)
			}
		})
	}
}

func TestParseToolArgumentsRepairsCommonModelMistakes(t *testing.T) {
	// A raw newline inside a string is not valid JSON but is common.
	args, argErr := ParseToolArguments("{\"text\": \"line1\nline2\"}")
	if argErr != nil {
		t.Fatalf("raw control character not repaired: %v", argErr)
	}
	if args["text"] != "line1\nline2" {
		t.Fatalf("text = %q", args["text"])
	}

	// An invalid escape should be kept literally instead of failing the call.
	args, argErr = ParseToolArguments(`{"path": "c:\dir"}`)
	if argErr != nil {
		t.Fatalf("invalid escape not repaired: %v", argErr)
	}
	if args["path"] != `c:\dir` {
		t.Fatalf("path = %q", args["path"])
	}
}

func TestParseToolArgumentsEmpty(t *testing.T) {
	for _, text := range []string{"", "   ", "\n\t"} {
		args, argErr := ParseToolArguments(text)
		if argErr != nil || args == nil || len(args) != 0 {
			t.Fatalf("empty %q => %#v, %v", text, args, argErr)
		}
	}
}

func TestJSONArgsParserResetAndText(t *testing.T) {
	parser := NewJSONArgsParser()
	parser.Append(`{"a":1}`)
	if parser.Len() != len(`{"a":1}`) || parser.Text() != `{"a":1}` {
		t.Fatalf("text = %q len = %d", parser.Text(), parser.Len())
	}
	parser.Reset()
	if parser.Len() != 0 || len(parser.Snapshot()) != 0 {
		t.Fatal("reset did not clear state")
	}
}
