package parser

import (
	"fmt"
	"testing"
)

func TestJSONSchemaParser_Parse(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{
				"type": "string",
			},
			"age": map[string]interface{}{
				"type": "integer",
			},
		},
		"required": []string{"name"},
	}

	parser := NewJSONSchemaParser(schema)

	tests := []struct {
		name      string
		input     string
		wantErr   bool
		checkFunc func(result interface{}) bool
	}{
		{
			name:    "valid JSON with required field",
			input:   `{"name": "John", "age": 30}`,
			wantErr: false,
			checkFunc: func(result interface{}) bool {
				m := result.(map[string]interface{})
				return m["name"] == "John" && m["age"] == float64(30)
			},
		},
		{
			name:    "valid JSON without optional field",
			input:   `{"name": "Jane"}`,
			wantErr: false,
			checkFunc: func(result interface{}) bool {
				m := result.(map[string]interface{})
				return m["name"] == "Jane"
			},
		},
		{
			name:    "missing required field",
			input:   `{"age": 25}`,
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			input:   `not json`,
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   ``,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parser.Parse(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.checkFunc != nil {
				if !tt.checkFunc(result) {
					t.Errorf("Parse() result = %v, check failed", result)
				}
			}
		})
	}
}

func TestJSONSchemaParser_GetFormatInstructions(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{
				"type": "string",
			},
		},
	}

	parser := NewJSONSchemaParser(schema)
	instructions := parser.GetFormatInstructions()

	if instructions == "" {
		t.Error("GetFormatInstructions() returned empty string")
	}

	// Check that schema is included
	if !contains(instructions, `"type"`) {
		t.Error("GetFormatInstructions() should include schema type")
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple JSON object",
			input: `{"key": "value"}`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "JSON with prefix text",
			input: `Here is the result: {"name": "test"}`,
			want:  `{"name": "test"}`,
		},
		{
			name:  "JSON with suffix text",
			input: `{"name": "test"} is the result`,
			want:  `{"name": "test"}`,
		},
		{
			name:  "JSON wrapped in text",
			input: `Result: {"data": {"value": 123}} End`,
			want:  `{"data": {"value": 123}}`,
		},
		{
			name:  "no JSON",
			input: `plain text`,
			want:  ``,
		},
		{
			name:  "nested JSON",
			input: `{"outer": {"inner": "value"}, "other": 123}`,
			want:  `{"outer": {"inner": "value"}, "other": 123}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStructuredOutputParser(t *testing.T) {
	// Create two parsers: one for JSON, one fallback
	jsonParser := NewJSONSchemaParser(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"result": map[string]interface{}{
				"type": "string",
			},
		},
		"required": []string{"result"},
	})

	// This parser always fails
	failParser := &failParser{}

	// Create structured parser that tries jsonParser first, then failParser
	structured := NewStructuredOutputParser(jsonParser, failParser)

	// Should succeed with jsonParser
	result, err := structured.Parse(`{"result": "success"}`)
	if err != nil {
		t.Errorf("Expected success, got error: %v", err)
	}
	if result == nil {
		t.Error("Expected non-nil result")
	}

	// Test with invalid JSON - should try failParser and fail
	result2, err := structured.Parse(`invalid`)
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
	if result2 != nil {
		t.Error("Expected nil result for error case")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

type failParser struct{}

func (p *failParser) Parse(output string) (interface{}, error) {
	return nil, fmt.Errorf("always fails")
}

func (p *failParser) GetFormatInstructions() string {
	return ""
}
