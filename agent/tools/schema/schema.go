package schema

import (
	"fmt"
	"strings"

	"github.com/xichan96/cortex/pkg/errors"
)

func toSnakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}

func snakeToLowerCamel(key string) string {
	parts := strings.Split(key, "_")
	if len(parts) < 2 {
		return key
	}
	var b strings.Builder
	b.WriteString(strings.ToLower(parts[0]))
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		low := strings.ToLower(p)
		b.WriteByte(low[0] - 32)
		if len(low) > 1 {
			b.WriteString(low[1:])
		}
	}
	return b.String()
}

func getInputVal(input map[string]interface{}, key string) (interface{}, bool) {
	if v, ok := input[key]; ok {
		return v, true
	}
	snake := toSnakeCase(key)
	if v, ok := input[snake]; ok {
		return v, true
	}
	if camel := snakeToLowerCamel(key); camel != key {
		if v, ok := input[camel]; ok {
			return v, true
		}
	}
	return nil, false
}

func parseRequired(schema map[string]interface{}) []string {
	raw, ok := schema["required"]
	if !ok {
		return nil
	}
	sl, ok := raw.([]interface{})
	if ok {
		out := make([]string, 0, len(sl))
		for _, v := range sl {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	if strSl, ok := raw.([]string); ok {
		return strSl
	}
	return nil
}

func parseProperties(schema map[string]interface{}) map[string]string {
	raw, ok := schema["properties"]
	if !ok {
		return nil
	}
	pm, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}
	out := make(map[string]string)
	for k, v := range pm {
		prop, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		if t, ok := prop["type"].(string); ok {
			out[k] = strings.TrimSpace(strings.ToLower(t))
		}
	}
	return out
}

func checkType(typ string, val interface{}) bool {
	switch typ {
	case "string":
		_, ok := val.(string)
		return ok
	case "number":
		switch val.(type) {
		case float64, int, int64:
			return true
		}
		return false
	case "integer":
		switch v := val.(type) {
		case int, int64:
			return true
		case float64:
			return v == float64(int64(v))
		}
		return false
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "array", "object":
		return true
	default:
		return true
	}
}

func ValidateInput(schema map[string]interface{}, input map[string]interface{}, toolName string) error {
	if schema == nil || input == nil {
		return nil
	}
	required := parseRequired(schema)
	properties := parseProperties(schema)
	for _, key := range required {
		v, has := getInputVal(input, key)
		if !has {
			return errors.EC_TOOL_INPUT_ERROR.Wrap(fmt.Errorf("%s: required field %q is missing. Please rewrite the input to match the schema", toolName, key))
		}
		if typ, ok := properties[key]; ok && typ == "string" {
			if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
				return errors.EC_TOOL_INPUT_ERROR.Wrap(fmt.Errorf("%s: required field %q must be a non-empty string. Please rewrite the input to match the schema", toolName, key))
			}
		}
	}
	for key, typ := range properties {
		v, has := getInputVal(input, key)
		if !has {
			continue
		}
		if v == nil {
			continue
		}
		if !checkType(typ, v) {
			return errors.EC_TOOL_INPUT_ERROR.Wrap(fmt.Errorf("%s: field %q must be of type %s. Please rewrite the input to match the schema", toolName, key, typ))
		}
	}
	return nil
}
