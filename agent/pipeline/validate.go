package pipeline

import (
	"context"
	"errors"

	toolschema "github.com/xichan96/cortex/agent/tools/schema"
)

// SchemaValidator validates prepared arguments against the tool's JSON schema
// using the same validator the legacy engine uses. Validation failures are
// recoverable results, not fatal errors.
type SchemaValidator struct{}

// Validate implements Validator.
func (SchemaValidator) Validate(_ context.Context, c *Call) error {
	if c == nil || c.Tool == nil {
		return nil
	}
	return toolschema.ValidateInput(c.Tool.Schema(), c.Args, c.Tool.Name())
}

// ToolExecutor invokes the tool directly. It is the default Executor.
type ToolExecutor struct{}

// Execute implements Executor.
func (ToolExecutor) Execute(ctx context.Context, c *Call) (any, error) {
	if c == nil || c.Tool == nil {
		return nil, errors.New("pipeline: tool is not resolved")
	}
	return c.Tool.Execute(ctx, c.Args)
}
