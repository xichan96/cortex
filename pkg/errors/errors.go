package errors

import (
	"fmt"
)

// Error agent engine error type
type Error struct {
	Code    int
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%d: %s (caused by: %v)", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%d: %s", e.Code, e.Message)
}

func (e *Error) Wrap(err error) *Error {
	e.Err = err
	return e
}

func (e *Error) Unwrap() error {
	return e.Err
}

// NewError creates an agent engine error
// Creates an agent engine error with error code and detailed information
// Parameters:
//   - code: error code
//   - message: error description
//   - err: original error (optional)
//
// Returns:
//   - agent engine error instance
func NewError(code int, message string) *Error {
	return &Error{
		Code:    code,
		Message: message,
	}
}

// WrapWithSkip wraps an error with a generic SQL error, skipping skip frames
// This is a compatibility function for existing code that uses WrapWithSkip
func WrapWithSkip(skip int, err error) error {
	if err == nil {
		return nil
	}
	return EC_SQL_ERROR.Wrap(err)
}
