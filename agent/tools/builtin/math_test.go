package builtin

import (
	"math"
	"testing"

	"github.com/xichan96/cortex/pkg/errors"
)

func TestMathTool_Name(t *testing.T) {
	tool := NewMathTool()
	if tool.Name() != "math_calculate" {
		t.Errorf("Expected name 'math_calculate', got '%s'", tool.Name())
	}
}

func TestMathTool_Description(t *testing.T) {
	tool := NewMathTool()
	desc := tool.Description()
	if desc == "" {
		t.Error("Description should not be empty")
	}
}

func TestMathTool_Schema(t *testing.T) {
	tool := NewMathTool()
	schema := tool.Schema()

	if schema["type"] != "object" {
		t.Error("Schema type should be 'object'")
	}

	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Schema should have properties")
	}

	if _, ok := properties["expression"]; !ok {
		t.Error("Schema should have 'expression' property")
	}

	if _, ok := properties["use_degrees"]; !ok {
		t.Error("Schema should have 'use_degrees' property")
	}

	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatal("Schema should have required array")
	}

	found := false
	for _, r := range required {
		if r == "expression" {
			found = true
			break
		}
	}
	if !found {
		t.Error("'expression' should be in required array")
	}
}

func TestMathTool_Metadata(t *testing.T) {
	tool := NewMathTool()
	metadata := tool.Metadata()

	if metadata.SourceNodeName != "math" {
		t.Errorf("Expected SourceNodeName 'math', got '%s'", metadata.SourceNodeName)
	}

	if metadata.IsFromToolkit {
		t.Error("IsFromToolkit should be false")
	}

	if metadata.ToolType != "builtin" {
		t.Errorf("Expected ToolType 'builtin', got '%s'", metadata.ToolType)
	}
}

func TestMathTool_Execute_BasicAddition(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "2+3",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("Result should be a map")
	}

	if resultMap["result"].(float64) != 5.0 {
		t.Errorf("Expected result 5.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_BasicSubtraction(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "10-4",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != 6.0 {
		t.Errorf("Expected result 6.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_BasicMultiplication(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "3*4",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != 12.0 {
		t.Errorf("Expected result 12.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_BasicDivision(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "15/3",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != 5.0 {
		t.Errorf("Expected result 5.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_OperatorPrecedence(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "2+3*4",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	expected := 2.0 + 3.0*4.0
	if resultMap["result"].(float64) != expected {
		t.Errorf("Expected result %f, got %f", expected, resultMap["result"])
	}
}

func TestMathTool_Execute_Parentheses(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "(2+3)*4",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != 20.0 {
		t.Errorf("Expected result 20.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_NegativeNumbers(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "-5+3",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != -2.0 {
		t.Errorf("Expected result -2.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_Power(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "2^3",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != 8.0 {
		t.Errorf("Expected result 8.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_Sqrt(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "sqrt(16)",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != 4.0 {
		t.Errorf("Expected result 4.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_Modulo(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "17%5",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != 2.0 {
		t.Errorf("Expected result 2.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_Factorial(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "5!",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != 120.0 {
		t.Errorf("Expected result 120.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_SinRadians(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "sin(0)",
		"use_degrees": false,
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	expected := math.Sin(0)
	if math.Abs(resultMap["result"].(float64)-expected) > 1e-10 {
		t.Errorf("Expected result %f, got %f", expected, resultMap["result"])
	}
}

func TestMathTool_Execute_SinDegrees(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "sin(90)",
		"use_degrees": true,
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	expected := math.Sin(90 * math.Pi / 180)
	if math.Abs(resultMap["result"].(float64)-expected) > 1e-10 {
		t.Errorf("Expected result %f, got %f", expected, resultMap["result"])
	}
}

func TestMathTool_Execute_CosRadians(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "cos(0)",
		"use_degrees": false,
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	expected := math.Cos(0)
	if math.Abs(resultMap["result"].(float64)-expected) > 1e-10 {
		t.Errorf("Expected result %f, got %f", expected, resultMap["result"])
	}
}

func TestMathTool_Execute_TanRadians(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "tan(0)",
		"use_degrees": false,
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	expected := math.Tan(0)
	if math.Abs(resultMap["result"].(float64)-expected) > 1e-10 {
		t.Errorf("Expected result %f, got %f", expected, resultMap["result"])
	}
}

func TestMathTool_Execute_ComplexExpression(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "(2+3)*4-10/2",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	expected := (2.0+3.0)*4.0 - 10.0/2.0
	if resultMap["result"].(float64) != expected {
		t.Errorf("Expected result %f, got %f", expected, resultMap["result"])
	}
}

func TestMathTool_Execute_DecimalNumbers(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "3.5+2.5",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != 6.0 {
		t.Errorf("Expected result 6.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_InvalidExpression(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "2**3",
	}

	_, err := tool.Execute(input)
	if err == nil {
		t.Fatal("Execute should return error for invalid expression")
	}

	errObj, ok := err.(*errors.Error)
	if !ok {
		t.Fatalf("Expected *errors.Error, got %T", err)
	}

	if errObj.Code != errors.EC_TOOL_EXECUTION_FAILED.Code {
		t.Errorf("Expected error code %d, got %d", errors.EC_TOOL_EXECUTION_FAILED.Code, errObj.Code)
	}
}

func TestMathTool_Execute_DivisionByZero(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "10/0",
	}

	_, err := tool.Execute(input)
	if err == nil {
		t.Fatal("Execute should return error for division by zero")
	}

	errObj, ok := err.(*errors.Error)
	if !ok {
		t.Fatalf("Expected *errors.Error, got %T", err)
	}

	if errObj.Code != errors.EC_TOOL_EXECUTION_FAILED.Code {
		t.Errorf("Expected error code %d, got %d", errors.EC_TOOL_EXECUTION_FAILED.Code, errObj.Code)
	}
}

func TestMathTool_Execute_ModuloByZero(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "10%0",
	}

	_, err := tool.Execute(input)
	if err == nil {
		t.Fatal("Execute should return error for modulo by zero")
	}

	errObj, ok := err.(*errors.Error)
	if !ok {
		t.Fatalf("Expected *errors.Error, got %T", err)
	}

	if errObj.Code != errors.EC_TOOL_EXECUTION_FAILED.Code {
		t.Errorf("Expected error code %d, got %d", errors.EC_TOOL_EXECUTION_FAILED.Code, errObj.Code)
	}
}

func TestMathTool_Execute_NegativeSqrt(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "sqrt(-1)",
	}

	_, err := tool.Execute(input)
	if err == nil {
		t.Fatal("Execute should return error for square root of negative number")
	}

	errObj, ok := err.(*errors.Error)
	if !ok {
		t.Fatalf("Expected *errors.Error, got %T", err)
	}

	if errObj.Code != errors.EC_TOOL_EXECUTION_FAILED.Code {
		t.Errorf("Expected error code %d, got %d", errors.EC_TOOL_EXECUTION_FAILED.Code, errObj.Code)
	}
}

func TestMathTool_Execute_NegativeFactorial(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "(-5)!",
	}

	_, err := tool.Execute(input)
	if err == nil {
		t.Fatal("Execute should return error for factorial of negative number")
	}

	errObj, ok := err.(*errors.Error)
	if !ok {
		t.Fatalf("Expected *errors.Error, got %T", err)
	}

	if errObj.Code != errors.EC_TOOL_EXECUTION_FAILED.Code {
		t.Errorf("Expected error code %d, got %d", errors.EC_TOOL_EXECUTION_FAILED.Code, errObj.Code)
	}
}

func TestMathTool_Execute_NonIntegerFactorial(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "5.5!",
	}

	_, err := tool.Execute(input)
	if err == nil {
		t.Fatal("Execute should return error for factorial of non-integer")
	}

	errObj, ok := err.(*errors.Error)
	if !ok {
		t.Fatalf("Expected *errors.Error, got %T", err)
	}

	if errObj.Code != errors.EC_TOOL_EXECUTION_FAILED.Code {
		t.Errorf("Expected error code %d, got %d", errors.EC_TOOL_EXECUTION_FAILED.Code, errObj.Code)
	}
}

func TestMathTool_Execute_EmptyExpression(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "",
	}

	_, err := tool.Execute(input)
	if err == nil {
		t.Fatal("Execute should return error for empty expression")
	}

	errObj, ok := err.(*errors.Error)
	if !ok {
		t.Fatalf("Expected *errors.Error, got %T", err)
	}

	if errObj.Code != errors.EC_PARAMETER_MISSING.Code {
		t.Errorf("Expected error code %d, got %d", errors.EC_PARAMETER_MISSING.Code, errObj.Code)
	}
}

func TestMathTool_Execute_InvalidParameterType(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": 123,
	}

	_, err := tool.Execute(input)
	if err == nil {
		t.Fatal("Execute should return error for invalid parameter type")
	}

	errObj, ok := err.(*errors.Error)
	if !ok {
		t.Fatalf("Expected *errors.Error, got %T", err)
	}

	if errObj.Code != errors.EC_TOOL_PARAMETER_INVALID.Code {
		t.Errorf("Expected error code %d, got %d", errors.EC_TOOL_PARAMETER_INVALID.Code, errObj.Code)
	}
}

func TestMathTool_Execute_MissingParenthesis(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "(2+3",
	}

	_, err := tool.Execute(input)
	if err == nil {
		t.Fatal("Execute should return error for missing closing parenthesis")
	}

	errObj, ok := err.(*errors.Error)
	if !ok {
		t.Fatalf("Expected *errors.Error, got %T", err)
	}

	if errObj.Code != errors.EC_TOOL_EXECUTION_FAILED.Code {
		t.Errorf("Expected error code %d, got %d", errors.EC_TOOL_EXECUTION_FAILED.Code, errObj.Code)
	}
}

func TestMathTool_Execute_UnknownFunction(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "unknown(5)",
	}

	_, err := tool.Execute(input)
	if err == nil {
		t.Fatal("Execute should return error for unknown function")
	}

	errObj, ok := err.(*errors.Error)
	if !ok {
		t.Fatalf("Expected *errors.Error, got %T", err)
	}

	if errObj.Code != errors.EC_TOOL_EXECUTION_FAILED.Code {
		t.Errorf("Expected error code %d, got %d", errors.EC_TOOL_EXECUTION_FAILED.Code, errObj.Code)
	}
}

func TestMathTool_Execute_Logarithm(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "log(100)",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != 2.0 {
		t.Errorf("Expected result 2.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_AbsoluteValue(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "abs(-5)",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != 5.0 {
		t.Errorf("Expected result 5.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_NestedExpressions(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "sqrt(2^2+3^2)",
	}

	result, err := tool.Execute(input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	expected := math.Sqrt(2*2 + 3*3)
	if math.Abs(resultMap["result"].(float64)-expected) > 1e-10 {
		t.Errorf("Expected result %f, got %f", expected, resultMap["result"])
	}
}

