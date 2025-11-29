package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
)

// LangChainToolAdapter LangChain tool adapter
type LangChainToolAdapter struct {
	baseTool types.Tool
}

// NewLangChainToolAdapter creates a new LangChain tool adapter
func NewLangChainToolAdapter(baseTool types.Tool) *LangChainToolAdapter {
	return &LangChainToolAdapter{baseTool: baseTool}
}

// Name returns the tool name
func (a *LangChainToolAdapter) Name() string {
	return a.baseTool.Name()
}

// Description returns the tool description
func (a *LangChainToolAdapter) Description() string {
	return a.baseTool.Description()
}

// Call calls the tool (LangChain interface)
func (a *LangChainToolAdapter) Call(ctx context.Context, input string) (string, error) {
	// Parse input
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		// If not JSON, try as simple string
		args = map[string]interface{}{"input": input}
	}

	// Execute tool
	result, err := a.baseTool.Execute(args)
	if err != nil {
		return "", err
	}

	// Convert result to string
	return fmt.Sprintf("%v", result), nil
}
