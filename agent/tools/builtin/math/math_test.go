package math

import (
	"context"
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

	result, err := tool.Execute(context.Background(), input)
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

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != 6.0 {
		t.Errorf("Expected result 6.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_OperatorPrecedence(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "2+3*4",
	}

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	expected := 2.0 + 3.0*4.0
	if resultMap["result"].(float64) != expected {
		t.Errorf("Expected result %f, got %f", expected, resultMap["result"])
	}
}

func TestMathTool_Execute_InvalidExpression(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "2**3",
	}

	_, err := tool.Execute(context.Background(), input)
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

func TestMathTool_Execute_Sqrt(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "sqrt(16)",
	}

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	if resultMap["result"].(float64) != 4.0 {
		t.Errorf("Expected result 4.0, got %f", resultMap["result"])
	}
}

func TestMathTool_Execute_NestedExpressions(t *testing.T) {
	tool := NewMathTool()

	input := map[string]interface{}{
		"expression": "sqrt(2^2+3^2)",
	}

	result, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	resultMap := result.(map[string]interface{})
	expected := math.Sqrt(2*2 + 3*3)
	if math.Abs(resultMap["result"].(float64)-expected) > 1e-10 {
		t.Errorf("Expected result %f, got %f", expected, resultMap["result"])
	}
}
