package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xichan96/cortex/agent/types"
	dinoLoop "github.com/xichan96/cortex/dino/loop"
)

// nonFatalTool prevents tool execution errors from terminating the agent loop.
// Instead, it returns the error as part of the tool result so the LLM can see it and correct.
type nonFatalTool struct {
	inner types.Tool
}

func WrapNonFatalTool(inner types.Tool) types.Tool {
	if inner == nil {
		return nil
	}
	return &nonFatalTool{inner: inner}
}

func (t *nonFatalTool) Name() string                   { return t.inner.Name() }
func (t *nonFatalTool) Description() string            { return t.inner.Description() }
func (t *nonFatalTool) Schema() map[string]interface{} { return t.inner.Schema() }
func (t *nonFatalTool) Metadata() types.ToolMetadata   { return t.inner.Metadata() }

func (t *nonFatalTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	result, err := t.inner.Execute(ctx, input)
	if err == nil {
		return result, nil
	}
	if ctx != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// For loop detected error, we want to return it as a real error to stop the loop
	var loopErr *LoopDetectedError
	if errors.As(err, &loopErr) {
		return nil, err
	}

	return map[string]interface{}{
		"ok":    false,
		"tool":  t.inner.Name(),
		"error": err.Error(),
		"hint":  nonFatalToolHint(t.inner.Name(), err),
	}, nil
}

func nonFatalToolHint(name string, err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if (name == "bash" || name == "execute_command") && strings.Contains(msg, "command is not allowed") {
		return "command execution is not allowed. Please use a single safe command, or use glob/grep/read_file to split the task."
	}
	if name == "grep" && (strings.Contains(msg, "executable file not found") || strings.Contains(msg, "no such file or directory")) {
		return "ripgrep (rg) is not installed in the current environment. Please install it first, or use glob/read_file to split the task."
	}
	return ""
}

// toolResultLimiter limits the maximum length of a tool's output to prevent token overflow.
type toolResultLimiter struct {
	inner          types.Tool
	maxBytes       int
	maxStringBytes int
}

func WrapToolResultLimiter(inner types.Tool, maxBytes, maxStringBytes int) types.Tool {
	if inner == nil {
		return nil
	}
	if maxBytes <= 0 {
		maxBytes = 120_000
	}
	if maxStringBytes <= 0 {
		maxStringBytes = 60_000
	}
	return &toolResultLimiter{inner: inner, maxBytes: maxBytes, maxStringBytes: maxStringBytes}
}

func (t *toolResultLimiter) Name() string                   { return t.inner.Name() }
func (t *toolResultLimiter) Description() string            { return t.inner.Description() }
func (t *toolResultLimiter) Schema() map[string]interface{} { return t.inner.Schema() }
func (t *toolResultLimiter) Metadata() types.ToolMetadata   { return t.inner.Metadata() }

func (t *toolResultLimiter) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	result, err := t.inner.Execute(ctx, input)
	if err != nil {
		return nil, err
	}

	// For strings, check length directly to avoid JSON marshaling overhead
	if strResult, ok := result.(string); ok {
		if len(strResult) > t.maxStringBytes {
			return map[string]interface{}{
				"ok":    false,
				"error": fmt.Sprintf("Tool result is too large (%d bytes), exceeding maximum allowed size (%d bytes). Please refine your query.", len(strResult), t.maxStringBytes),
			}, nil
		}
		return result, nil
	}

	// For byte slices, check length directly
	if byteResult, ok := result.([]byte); ok {
		if len(byteResult) > t.maxBytes {
			return map[string]interface{}{
				"ok":    false,
				"error": fmt.Sprintf("Tool result is too large (%d bytes), exceeding maximum allowed size (%d bytes). Please refine your query.", len(byteResult), t.maxBytes),
			}, nil
		}
		return result, nil
	}

	// For complex objects, use string representation first if possible
	if stringer, ok := result.(fmt.Stringer); ok {
		str := stringer.String()
		if len(str) > t.maxStringBytes {
			return map[string]interface{}{
				"ok":    false,
				"error": fmt.Sprintf("Tool result is too large (%d bytes), exceeding maximum allowed size (%d bytes).", len(str), t.maxStringBytes),
			}, nil
		}
		// Continue to let JSON marshalling handle it if it passed string length check
	}

	// Simple check: convert to JSON and measure length
	b, err := json.Marshal(result)
	if err != nil {
		// Fallback: try to convert to string representation
		strResult := fmt.Sprintf("%v", result)
		if len(strResult) > t.maxStringBytes {
			return map[string]interface{}{
				"ok":    false,
				"error": fmt.Sprintf("Tool result (stringified) is too large (%d bytes), exceeding maximum allowed size (%d bytes).", len(strResult), t.maxStringBytes),
			}, nil
		}
		// If it's small enough, return the original result (caller handles marshaling/processing)
		// But warn about marshaling issue if possible, or just let it pass as raw object
		return result, nil
	}

	if len(b) > t.maxBytes {
		return map[string]interface{}{
			"ok":    false,
			"error": fmt.Sprintf("Tool result is too large (%d bytes), exceeding maximum allowed size (%d bytes). Please refine your query to return less data.", len(b), t.maxBytes),
		}, nil
	}

	return result, nil
}

type LoopDetectedError struct {
	ToolName   string
	Suggestion string
}

func (e *LoopDetectedError) Error() string {
	return fmt.Sprintf("Loop detected with tool %s. %s", e.ToolName, e.Suggestion)
}

// loopDetectingTool prevents tools from being called in a loop.
type loopDetectingTool struct {
	inner     types.Tool
	sessionID string
	detector  dinoLoop.Detector
	sender    ToolEventSender
}

func WrapLoopDetection(inner types.Tool, sessionID string, detector dinoLoop.Detector, sender ToolEventSender) types.Tool {
	if inner == nil || sessionID == "" || detector == nil {
		return inner
	}
	return &loopDetectingTool{inner: inner, sessionID: sessionID, detector: detector, sender: sender}
}

func (t *loopDetectingTool) Name() string                   { return t.inner.Name() }
func (t *loopDetectingTool) Description() string            { return t.inner.Description() }
func (t *loopDetectingTool) Schema() map[string]interface{} { return t.inner.Schema() }
func (t *loopDetectingTool) Metadata() types.ToolMetadata   { return t.inner.Metadata() }

func (t *loopDetectingTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	inputBytes, err := json.Marshal(input)
	var content string
	if err != nil {
		content = fmt.Sprintf("%v", input)
	} else {
		content = string(inputBytes)
	}

	action := dinoLoop.Action{
		Type:    t.inner.Name(),
		Content: content,
	}

	result := t.detector.Detect(ctx, t.sessionID, action)
	if result.IsLoop {
		err := &LoopDetectedError{
			ToolName:   t.inner.Name(),
			Suggestion: result.Suggestion,
		}
		if t.sender != nil {
			t.sender.SendToolError(t.sessionID, "", t.inner.Name(), err.Error())
		}
		return nil, err
	}

	execResult, err := t.inner.Execute(ctx, input)

	t.detector.Record(t.sessionID, action)

	return execResult, err
}
