package parser

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xichan96/cortex/agent/types"
)

// JSONSchemaParser parses LLM output based on a JSON schema
type JSONSchemaParser struct {
	Schema   map[string]interface{}
	Required []string
}

// NewJSONSchemaParser creates a new JSON schema parser
func NewJSONSchemaParser(schema map[string]interface{}) *JSONSchemaParser {
	required, _ := schema["required"].([]string)
	return &JSONSchemaParser{
		Schema:   schema,
		Required: required,
	}
}

func (p *JSONSchemaParser) Parse(output string) (interface{}, error) {
	// Try to extract JSON from output
	jsonStr := extractJSON(output)
	if jsonStr == "" {
		return nil, fmt.Errorf("no JSON found in output")
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Validate required fields
	if p.Required != nil {
		for _, field := range p.Required {
			if _, ok := result[field]; !ok {
				return nil, fmt.Errorf("missing required field: %s", field)
			}
		}
	}

	return result, nil
}

func (p *JSONSchemaParser) GetFormatInstructions() string {
	schemaJSON, err := json.Marshal(p.Schema)
	if err != nil {
		return ""
	}
	return fmt.Sprintf(`Please respond with valid JSON that matches this schema:
%s

Your response should be ONLY the JSON object, with no additional text.`, schemaJSON)
}

// extractJSON extracts JSON from a string that may contain extra text
func extractJSON(s string) string {
	// Try to find JSON object in the output
	start := strings.Index(s, "{")
	if start == -1 {
		return ""
	}

	// Find matching closing brace
	depth := 0
	end := -1
	for i := start; i < len(s); i++ {
		if s[i] == '{' {
			depth++
		} else if s[i] == '}' {
			depth--
			if depth == 0 {
				end = i + 1
				break
			}
		}
	}

	if end > start {
		return s[start:end]
	}

	return ""
}

// ToolCallParser parses tool calls from LLM output
type ToolCallParser struct {
	Tools []types.Tool
}

func NewToolCallParser(tools []types.Tool) *ToolCallParser {
	return &ToolCallParser{Tools: tools}
}

func (p *ToolCallParser) Parse(output string) (interface{}, error) {
	// This parser is for extracting tool calls from free-text output
	// It's a simple implementation - in practice, you'd want more sophisticated parsing
	return nil, fmt.Errorf("ToolCallParser not yet implemented")
}

func (p *ToolCallParser) GetFormatInstructions() string {
	if len(p.Tools) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("You have access to the following tools:\n\n")

	for _, tool := range p.Tools {
		sb.WriteString(fmt.Sprintf("## %s\n%s\n\n", tool.Name(), tool.Description()))
	}

	sb.WriteString("When you need to use a tool, respond with:")
	sb.WriteString("\n```json\n")
	sb.WriteString(`{"tool": "tool_name", "arguments": {"arg1": "value1"}}`)
	sb.WriteString("\n```\n")

	return sb.String()
}

// StructuredOutputParser combines multiple parsers for complex output
type StructuredOutputParser struct {
	Parsers []types.OutputParser
}

func NewStructuredOutputParser(parsers ...types.OutputParser) *StructuredOutputParser {
	return &StructuredOutputParser{Parsers: parsers}
}

func (p *StructuredOutputParser) Parse(output string) (interface{}, error) {
	for _, parser := range p.Parsers {
		result, err := parser.Parse(output)
		if err == nil {
			return result, nil
		}
	}
	return nil, fmt.Errorf("failed to parse with any parser")
}

func (p *StructuredOutputParser) GetFormatInstructions() string {
	var instructions []string
	for _, parser := range p.Parsers {
		instructions = append(instructions, parser.GetFormatInstructions())
	}
	return strings.Join(instructions, "\n\n")
}
