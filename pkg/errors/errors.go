package errors

import (
	"context"
	"fmt"

	stderrors "errors"
)

// Error agent engine error type
type Error struct {
	Code         int
	Message      string
	Err          error
	Retryable    bool
	RetryAfterMs int
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%d: %s (caused by: %v)", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%d: %s", e.Code, e.Message)
}

func (e *Error) Wrap(err error) *Error {
	return &Error{
		Code:         e.Code,
		Message:      e.Message,
		Err:          err,
		Retryable:    e.Retryable,
		RetryAfterMs: e.RetryAfterMs,
	}
}

func (e *Error) Unwrap() error {
	return e.Err
}

func (e *Error) Is(target error) bool {
	if e == nil {
		return false
	}
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Code == t.Code
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

func NewRetryableError(code int, message string, retryAfterMs int) *Error {
	return &Error{
		Code:         code,
		Message:      message,
		Retryable:    true,
		RetryAfterMs: retryAfterMs,
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

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if stderrors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var e *Error
	if stderrors.As(err, &e) {
		return e.Retryable
	}
	return false
}
