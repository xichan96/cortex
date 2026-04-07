package stores

import (
	"errors"
)

var (
	ErrNotFound     = errors.New("memory: not found")
	ErrInvalidInput = errors.New("memory: invalid input")
)
