package param

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/xichan96/cortex/pkg/errors"
)

type Options struct {
	Required bool
	Label    string
}

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

func raw(params map[string]interface{}, key string) (interface{}, bool) {
	if v, ok := params[key]; ok {
		return v, true
	}
	snake := toSnakeCase(key)
	if v, ok := params[snake]; ok {
		return v, true
	}
	return nil, false
}

func errRequired(label string) error {
	return errors.EC_TOOL_INPUT_ERROR.Wrap(fmt.Errorf("%s required", label))
}

func ReadString(params map[string]interface{}, key string, opts Options) (string, error) {
	label := opts.Label
	if label == "" {
		label = key
	}
	v, ok := raw(params, key)
	if !ok {
		if opts.Required {
			return "", errRequired(label)
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		if opts.Required {
			return "", errors.EC_TOOL_INPUT_ERROR.Wrap(fmt.Errorf("%s must be a string", label))
		}
		return "", nil
	}
	s = strings.TrimSpace(s)
	if s == "" && opts.Required {
		return "", errRequired(label)
	}
	return s, nil
}

func ReadNumber(params map[string]interface{}, key string, opts Options) (float64, error) {
	label := opts.Label
	if label == "" {
		label = key
	}
	v, ok := raw(params, key)
	if !ok {
		if opts.Required {
			return 0, errRequired(label)
		}
		return 0, nil
	}
	switch val := v.(type) {
	case float64:
		return val, nil
	case int:
		return float64(val), nil
	case int64:
		return float64(val), nil
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			if opts.Required {
				return 0, errRequired(label)
			}
			return 0, nil
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, errors.EC_TOOL_INPUT_ERROR.Wrap(fmt.Errorf("%s must be a number", label))
		}
		return n, nil
	default:
		if opts.Required {
			return 0, errors.EC_TOOL_INPUT_ERROR.Wrap(fmt.Errorf("%s must be a number", label))
		}
		return 0, nil
	}
}

func ReadBool(params map[string]interface{}, key string, opts Options) (bool, error) {
	label := opts.Label
	if label == "" {
		label = key
	}
	v, ok := raw(params, key)
	if !ok {
		if opts.Required {
			return false, errRequired(label)
		}
		return false, nil
	}
	switch val := v.(type) {
	case bool:
		return val, nil
	case string:
		s := strings.TrimSpace(strings.ToLower(val))
		if s == "" {
			if opts.Required {
				return false, errRequired(label)
			}
			return false, nil
		}
		return s == "true" || s == "1" || s == "yes", nil
	default:
		if opts.Required {
			return false, errors.EC_TOOL_INPUT_ERROR.Wrap(fmt.Errorf("%s must be a boolean", label))
		}
		return false, nil
	}
}
