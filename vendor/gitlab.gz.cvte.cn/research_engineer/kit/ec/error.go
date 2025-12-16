// Package ec
package ec

import (
	"fmt"
	"strings"

	"github.com/pkg/errors"
)

func wrapError(skip int, err error, msg string) *ErrorCode {
	e := &ErrorCode{
		Status: systemErrorStart,
		err:    err,
	}

	if errCode, ok := err.(*ErrorCode); ok {
		e.Status = errCode.Status
	}

	if len(msg) > 0 {
		e.Msg = fmt.Sprintf("%s: %s", msg, err.Error())
	} else {
		e.Msg = err.Error()
	}

	tr, ok := err.(StackTracer)
	// 传入的error未实现StackTrace接口或者error的堆栈为空，则使用自定义的获取堆栈方法
	if !ok || len(tr.ErrStack()) == 0 {
		e.stack = ErrorCallers(4 + skip)
	} else {
		e.stack = tr.ErrStack()
	}
	return e
}

// New 新建错误
func New(ms string) *ErrorCode {
	return &ErrorCode{
		Status: systemErrorStart,
		Msg:    ms,
		stack:  nil,
		err:    newBaseError(ms),
	}
}

// Errorf 返回格式化错误
func Errorf(format string, args ...interface{}) error {
	ms := fmt.Sprintf(format, args...)
	return &ErrorCode{
		Status: systemErrorStart,
		Msg:    ms,
		stack:  nil,
		err:    newBaseError(ms),
	}
}

// NewErrorCode 新建错误携带错误码
func NewErrorCode(code int32, ms string) *ErrorCode {
	return &ErrorCode{
		Status: code,
		Msg:    ms,
		stack:  nil,
		err:    newBaseError(ms),
	}
}

// Wrap 通用错误wrap
func Wrap(err error, ms ...string) *ErrorCode {
	return WrapWithSkip(1, err, ms...)
}

// Wrapf 格式化错误信息
func Wrapf(err error, format string, args ...interface{}) *ErrorCode {
	return WrapWithSkip(1, err, fmt.Sprintf(format, args...))
}

// WrapWithSkip 通用错误wrap可以跳过错误栈
func WrapWithSkip(skip int, err error, ms ...string) *ErrorCode {
	if err == nil {
		return nil
	}
	var msg string
	if len(ms) > 0 {
		msg = strings.Join(ms, " ")
	}
	return wrapError(skip, err, msg)
}

// Cause is 返回原始的err
func Cause(err error) error {
	type causer interface {
		Cause() error
	}

	for err != nil {
		cause, ok := err.(causer)
		if !ok {
			break
		}
		err = cause.Cause()
	}
	return err
}

// IsErrCode ...
func IsErrCode(src error, dst *ErrorCode) bool {
	eCode, ok := src.(*ErrorCode)
	if !ok {
		return false
	}
	return eCode.Status == dst.Status
}

// IsErr ...
func IsErr(src, dst error) bool {
	return errors.Is(Cause(src), Cause(dst))
}
