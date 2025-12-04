package errors

import (
	"fmt"
)

// AgentError agent engine error type
type AgentError struct {
	Code    int
	Message string
	Err     error
}

func (e *AgentError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%d: %s (caused by: %v)", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%d: %s", e.Code, e.Message)
}

func (e *AgentError) Wrap(err error) *AgentError {
	e.Err = err
	return e
}

func (e *AgentError) Unwrap() error {
	return e.Err
}

// NewAgentError creates an agent engine error
// Creates an agent engine error with error code and detailed information
// Parameters:
//   - code: error code
//   - message: error description
//   - err: original error (optional)
//
// Returns:
//   - agent engine error instance
func NewAgentError(code int, message string) *AgentError {
	return &AgentError{
		Code:    code,
		Message: message,
	}
}
