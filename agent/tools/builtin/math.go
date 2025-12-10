package builtin

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type MathTool struct {
	useDegrees bool
}

func NewMathTool() types.Tool {
	return &MathTool{useDegrees: false}
}

func (t *MathTool) Name() string {
	return "math_calculate"
}

func (t *MathTool) Description() string {
	return "Perform mathematical calculations. Supports basic operations (+, -, *, /), advanced operations (^, √, %, !), trigonometric functions (sin, cos, tan), and supports both degrees and radians mode."
}

func (t *MathTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"expression": map[string]interface{}{
				"type":        "string",
				"description": "Mathematical expression to evaluate (e.g., '2+3*4', 'sin(30)', 'sqrt(16)')",
			},
			"use_degrees": map[string]interface{}{
				"type":        "boolean",
				"description": "Use degrees for trigonometric functions (default: false, uses radians)",
			},
		},
		"required": []string{"expression"},
	}
}

func (t *MathTool) Execute(input map[string]interface{}) (interface{}, error) {
	expression, ok := input["expression"].(string)
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'expression' parameter: must be a string"))
	}
	if expression == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'expression' parameter cannot be empty"))
	}

	useDegrees := false
	if val, ok := input["use_degrees"].(bool); ok {
		useDegrees = val
	}

	expression = strings.TrimSpace(expression)
	if expression == "" {
		return nil, errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("expression cannot be empty"))
	}

	result, err := t.evaluate(expression, useDegrees)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}

	return map[string]interface{}{
		"result":      result,
		"expression":  expression,
		"use_degrees": useDegrees,
	}, nil
}

func (t *MathTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "math",
		IsFromToolkit:  false,
		ToolType:       "builtin",
	}
}

type parser struct {
	expr       string
	pos        int
	useDegrees bool
}

func (t *MathTool) evaluate(expr string, useDegrees bool) (float64, error) {
	p := &parser{
		expr:       strings.ReplaceAll(strings.ReplaceAll(expr, " ", ""), "√", "sqrt"),
		pos:        0,
		useDegrees: useDegrees,
	}

	result, err := p.parseExpression()
	if err != nil {
		return 0, err
	}

	if p.pos < len(p.expr) {
		return 0, fmt.Errorf("unexpected character at position %d: %c", p.pos, p.expr[p.pos])
	}

	return result, nil
}

func (p *parser) parseExpression() (float64, error) {
	return p.parseAddition()
}

func (p *parser) parseAddition() (float64, error) {
	left, err := p.parseMultiplication()
	if err != nil {
		return 0, err
	}

	for {
		if p.pos >= len(p.expr) {
			break
		}

		op := p.expr[p.pos]
		if op == '+' {
			p.pos++
			right, err := p.parseMultiplication()
			if err != nil {
				return 0, err
			}
			left += right
		} else if op == '-' {
			p.pos++
			right, err := p.parseMultiplication()
			if err != nil {
				return 0, err
			}
			left -= right
		} else {
			break
		}
	}

	return left, nil
}

func (p *parser) parseMultiplication() (float64, error) {
	left, err := p.parsePower()
	if err != nil {
		return 0, err
	}

	for {
		if p.pos >= len(p.expr) {
			break
		}

		op := p.expr[p.pos]
		if op == '*' {
			p.pos++
			right, err := p.parsePower()
			if err != nil {
				return 0, err
			}
			left *= right
		} else if op == '/' {
			p.pos++
			right, err := p.parsePower()
			if err != nil {
				return 0, err
			}
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			left /= right
		} else if op == '%' {
			p.pos++
			right, err := p.parsePower()
			if err != nil {
				return 0, err
			}
			if right == 0 {
				return 0, fmt.Errorf("modulo by zero")
			}
			left = math.Mod(left, right)
		} else {
			break
		}
	}

	return left, nil
}

func (p *parser) parsePower() (float64, error) {
	left, err := p.parseUnary()
	if err != nil {
		return 0, err
	}

	for {
		if p.pos >= len(p.expr) {
			break
		}

		if p.expr[p.pos] == '^' {
			p.pos++
			right, err := p.parseUnary()
			if err != nil {
				return 0, err
			}
			left = math.Pow(left, right)
		} else {
			break
		}
	}

	return left, nil
}

func (p *parser) parseUnary() (float64, error) {
	if p.pos >= len(p.expr) {
		return 0, fmt.Errorf("unexpected end of expression")
	}

	if p.expr[p.pos] == '-' {
		p.pos++
		val, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		return -val, nil
	}

	if p.expr[p.pos] == '+' {
		p.pos++
		return p.parseUnary()
	}

	return p.parseFactor()
}

func (p *parser) parseFactor() (float64, error) {
	if p.pos >= len(p.expr) {
		return 0, fmt.Errorf("unexpected end of expression")
	}

	var val float64
	var err error

	if p.expr[p.pos] == '(' {
		p.pos++
		val, err = p.parseExpression()
		if err != nil {
			return 0, err
		}
		if p.pos >= len(p.expr) || p.expr[p.pos] != ')' {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		p.pos++
	} else if unicode.IsLetter(rune(p.expr[p.pos])) {
		val, err = p.parseFunction()
		if err != nil {
			return 0, err
		}
	} else {
		val, err = p.parseNumber()
		if err != nil {
			return 0, err
		}
		return val, nil
	}

	if p.pos < len(p.expr) && p.expr[p.pos] == '!' {
		p.pos++
		if val < 0 {
			return 0, fmt.Errorf("factorial of negative number")
		}
		if val != math.Floor(val) {
			return 0, fmt.Errorf("factorial of non-integer number")
		}
		n := int(val)
		if n > 170 {
			return 0, fmt.Errorf("factorial too large (max 170)")
		}
		result := 1.0
		for i := 2; i <= n; i++ {
			result *= float64(i)
		}
		return result, nil
	}

	return val, nil
}

func (p *parser) parseFunction() (float64, error) {
	start := p.pos
	for p.pos < len(p.expr) && unicode.IsLetter(rune(p.expr[p.pos])) {
		p.pos++
	}
	funcName := p.expr[start:p.pos]

	if p.pos >= len(p.expr) || p.expr[p.pos] != '(' {
		return 0, fmt.Errorf("function '%s' must be followed by '('", funcName)
	}
	p.pos++

	arg, err := p.parseExpression()
	if err != nil {
		return 0, err
	}

	if p.pos >= len(p.expr) || p.expr[p.pos] != ')' {
		return 0, fmt.Errorf("missing closing parenthesis for function '%s'", funcName)
	}
	p.pos++

	switch funcName {
	case "sqrt":
		if arg < 0 {
			return 0, fmt.Errorf("square root of negative number")
		}
		return math.Sqrt(arg), nil
	case "sin":
		if p.useDegrees {
			arg = arg * math.Pi / 180
		}
		return math.Sin(arg), nil
	case "cos":
		if p.useDegrees {
			arg = arg * math.Pi / 180
		}
		return math.Cos(arg), nil
	case "tan":
		if p.useDegrees {
			arg = arg * math.Pi / 180
		}
		result := math.Tan(arg)
		if math.IsInf(result, 0) {
			return 0, fmt.Errorf("tangent is undefined for this angle")
		}
		return result, nil
	case "asin", "arcsin":
		result := math.Asin(arg)
		if math.IsNaN(result) {
			return 0, fmt.Errorf("arcsine argument must be in range [-1, 1]")
		}
		if p.useDegrees {
			result = result * 180 / math.Pi
		}
		return result, nil
	case "acos", "arccos":
		result := math.Acos(arg)
		if math.IsNaN(result) {
			return 0, fmt.Errorf("arccosine argument must be in range [-1, 1]")
		}
		if p.useDegrees {
			result = result * 180 / math.Pi
		}
		return result, nil
	case "atan", "arctan":
		result := math.Atan(arg)
		if p.useDegrees {
			result = result * 180 / math.Pi
		}
		return result, nil
	case "ln":
		if arg <= 0 {
			return 0, fmt.Errorf("logarithm of non-positive number")
		}
		return math.Log(arg), nil
	case "log", "log10":
		if arg <= 0 {
			return 0, fmt.Errorf("logarithm of non-positive number")
		}
		return math.Log10(arg), nil
	case "exp":
		return math.Exp(arg), nil
	case "abs":
		return math.Abs(arg), nil
	case "floor":
		return math.Floor(arg), nil
	case "ceil":
		return math.Ceil(arg), nil
	case "round":
		return math.Round(arg), nil
	default:
		return 0, fmt.Errorf("unknown function: %s", funcName)
	}
}

func (p *parser) parseNumber() (float64, error) {
	start := p.pos

	if p.pos >= len(p.expr) {
		return 0, fmt.Errorf("unexpected end of expression")
	}

	if p.expr[p.pos] == '.' {
		return 0, fmt.Errorf("invalid number format at position %d", p.pos)
	}

	hasDot := false
	for p.pos < len(p.expr) {
		c := p.expr[p.pos]
		if c == '.' {
			if hasDot {
				break
			}
			hasDot = true
			p.pos++
		} else if unicode.IsDigit(rune(c)) {
			p.pos++
		} else {
			break
		}
	}

	if p.pos == start {
		return 0, fmt.Errorf("invalid character at position %d: %c", p.pos, p.expr[p.pos])
	}

	numStr := p.expr[start:p.pos]
	val, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number format: %s", numStr)
	}

	if p.pos < len(p.expr) && p.expr[p.pos] == '!' {
		p.pos++
		if val < 0 {
			return 0, fmt.Errorf("factorial of negative number")
		}
		if val != math.Floor(val) {
			return 0, fmt.Errorf("factorial of non-integer number")
		}
		n := int(val)
		if n > 170 {
			return 0, fmt.Errorf("factorial too large (max 170)")
		}
		result := 1.0
		for i := 2; i <= n; i++ {
			result *= float64(i)
		}
		return result, nil
	}

	return val, nil
}
