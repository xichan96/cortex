package ec

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/pkg/errors"
)

var errorsFilter []string

// StackTracer 错误跟踪
type StackTracer interface {
	ErrStack() errors.StackTrace
}

// ErrorCallers 获取调用栈
func ErrorCallers(skip int) errors.StackTrace {
	const depth = 32
	var pcs [depth]uintptr
	n := runtime.Callers(skip, pcs[:])
	var stack errors.StackTrace
	for i := 0; i < n; i++ {
		funcName := runtime.FuncForPC(pcs[i]).Name()
		// 过滤
		isIgnore := false
		for _, filter := range errorsFilter {
			if strings.HasPrefix(funcName, filter) {
				isIgnore = true
				break
			}
		}
		if isIgnore {
			continue
		}
		stack = append(stack, errors.Frame(pcs[i]))
	}
	return stack
}

// SetStackFilter 设置错误栈过滤
func SetStackFilter(filters ...string) {
	errorsFilter = make([]string, len(filters))
	for i, filter := range filters {
		errorsFilter[i] = filter
	}
}

// PrintStack 打印错误栈
func PrintStack(es StackTracer) {
	fmt.Print(es)
	fmt.Printf("%+v\n", es.ErrStack())
	fmt.Println()
}
