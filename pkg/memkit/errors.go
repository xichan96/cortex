package memkit

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound         = errors.New("memory: not found")
	ErrAlreadyExists    = errors.New("memory: already exists")
	ErrInvalidInput     = errors.New("memory: invalid input")
	ErrMaxLimitExceeded = errors.New("memory: max limit exceeded")
	ErrStoreNotReady    = errors.New("memory: store not ready")
	ErrUnsupported      = errors.New("memory: unsupported operation")
)

type ErrorCode string

const (
	ErrCodeNotFound         ErrorCode = "NOT_FOUND"
	ErrCodeAlreadyExists    ErrorCode = "ALREADY_EXISTS"
	ErrCodeInvalidInput     ErrorCode = "INVALID_INPUT"
	ErrCodeMaxLimitExceeded ErrorCode = "MAX_LIMIT_EXCEEDED"
	ErrCodeStoreNotReady    ErrorCode = "STORE_NOT_READY"
	ErrCodeUnsupported      ErrorCode = "UNSUPPORTED"
	ErrCodeInternal         ErrorCode = "INTERNAL"
)

type MemoryError struct {
	Code    ErrorCode
	Message string
	Err     error
}

func (e *MemoryError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("memory: %s - %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("memory: %s - %s", e.Code, e.Message)
}

func (e *MemoryError) Unwrap() error {
	return e.Err
}

func NewError(code ErrorCode, message string) *MemoryError {
	return &MemoryError{Code: code, Message: message}
}

func WrapError(err error, code ErrorCode, message string) *MemoryError {
	return &MemoryError{Code: code, Message: message, Err: err}
}

func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

func IsMaxLimitExceeded(err error) bool {
	return errors.Is(err, ErrMaxLimitExceeded)
}
