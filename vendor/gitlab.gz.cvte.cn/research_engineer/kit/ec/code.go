package ec

import (
	"fmt"
	"io"

	"github.com/pkg/errors"
)

const systemErrorStart = 50000

// 基础接口错误码
var (
	Success      = NewErrorCode(0, "ok")
	BadParams    = NewErrorCode(400, "bad params")
	Unauthorized = NewErrorCode(401, "unauthorized")
	Forbidden    = NewErrorCode(403, "forbidden")
	NoFound      = NewErrorCode(404, "no found")
	ExistedErr   = NewErrorCode(444, "Existed")
	UnknownErr   = NewErrorCode(500, "system error")
)

// ErrorCode 响应状态码
type ErrorCode struct {
	Status int32  `json:"status"`
	Msg    string `json:"msg"`
	stack  errors.StackTrace
	err    error
}

// Cause 实现causer
func (ec *ErrorCode) Cause() error {
	return ec.err
}

// Error - return error message
func (ec *ErrorCode) Error() string {
	return ec.Msg
}

// ErrStack 实现StackTracer
func (ec *ErrorCode) ErrStack() errors.StackTrace {
	return ec.stack
}

// IsSystemError 是否系统错误
func (ec *ErrorCode) IsSystemError() bool {
	return ec.Status >= systemErrorStart
}

// WithStack 携带错误栈
func (ec ErrorCode) WithStack() *ErrorCode {
	return &ErrorCode{
		Status: ec.Status,
		Msg:    ec.Msg,
		stack:  ErrorCallers(3),
		err:    ec.err,
	}
}

// Format 格式化打印
func (ec *ErrorCode) Format(s fmt.State, verb rune) {
	switch verb {
	case 'v':
		if s.Flag('+') {
			io.WriteString(s, fmt.Sprintf("%s %d", ec.Msg, ec.Status))
			ec.stack.Format(s, verb)
			return
		}
		fallthrough
	case 's':
		io.WriteString(s, ec.Msg)
	case 'q':
		fmt.Fprintf(s, "%q", ec.Msg)
	}
}

func newBaseError(ms string) *baseError {
	return &baseError{msg: ms}
}

type baseError struct {
	msg string
}

func (e baseError) Error() string {
	return e.msg
}
