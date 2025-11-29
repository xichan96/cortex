package types

import (
	"context"
	"encoding/json"
	"fmt"
)

// LangChainToolWrapper LangChain tool wrapper
type LangChainToolWrapper struct {
	tool Tool
}

// NewLangChainToolWrapper creates a new LangChain tool wrapper
func NewLangChainToolWrapper(tool Tool) *LangChainToolWrapper {
	return &LangChainToolWrapper{tool: tool}
}

// Name returns tool name
func (w *LangChainToolWrapper) Name() string {
	return w.tool.Name()
}

// Description returns tool description
func (w *LangChainToolWrapper) Description() string {
	return w.tool.Description()
}

// Call invokes the tool (LangChain interface)
func (w *LangChainToolWrapper) Call(ctx context.Context, input string) (string, error) {
	// Parse input
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		// If not JSON, try as simple string
		args = map[string]interface{}{"input": input}
	}

	// Execute the tool
	result, err := w.tool.Execute(args)
	if err != nil {
		return "", err
	}

	// Convert result to string
	return fmt.Sprintf("%v", result), nil
}
