package engine

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"
)

const llmRetryAttempts = 4

// isRetryableLLMError classifies transient provider/gateway failures. These are
// safe to retry because the model call did not produce a usable turn.
func isRetryableLLMError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, marker := range []string{
		"429", "rate limit", "too many requests",
		"500", "502", "503", "504",
		"connection reset", "connection refused", "broken pipe",
		"unexpected eof", "temporarily unavailable", "i/o timeout", "timeout",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func retryDelay(attempt int) time.Duration {
	return time.Duration(1<<attempt) * 500 * time.Millisecond
}

// retryDelayForError prefers the provider/gateway's Retry-After hint. Dojo's
// LLM gateway returns e.g. "all upstream targets failed: network, retry after
// 4s" on 503; retrying after 0.5s/1s only burns the attempts before the
// upstream recovers. Fall back to bounded exponential backoff.
func retryDelayForError(err error, attempt int) time.Duration {
	if d := parseRetryAfter(err); d > 0 {
		if d > 30*time.Second {
			return 30 * time.Second
		}
		return d
	}
	return retryDelay(attempt)
}

// parseRetryAfter extracts "retry after 4s" / "retry-after: 4" style hints.
// Returns 0 when absent or unparsable.
func parseRetryAfter(err error) time.Duration {
	if err == nil {
		return 0
	}
	s := strings.ToLower(err.Error())
	for _, marker := range []string{"retry after ", "retry-after: ", "retry-after=", "retry_after="} {
		idx := strings.Index(s, marker)
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(s[idx+len(marker):])
		token := strings.Fields(rest)
		if len(token) == 0 {
			continue
		}
		v := strings.TrimRight(token[0], "),.;]")
		if d, parseErr := time.ParseDuration(v); parseErr == nil && d > 0 {
			return d
		}
		// Bare seconds, e.g. "retry-after: 4".
		if n, parseErr := time.ParseDuration(v + "s"); parseErr == nil && n > 0 {
			return n
		}
	}
	return 0
}

// isContextOverflowError recognizes provider errors that mean "prompt too
// large". They are recoverable once by compacting/trimming and retrying.
func isContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, marker := range []string{
		"context length", "context_length_exceeded", "maximum context",
		"prompt is too long", "too many tokens", "reduce the length",
		"input is too long",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

// trimMessagesForOverflow keeps leading system messages and the most recent
// `keep` non-system messages, adjusting the cut so the kept tail does not start
// with an orphan tool result.
func trimMessagesForOverflow(messages []types.Message, keep int) []types.Message {
	if len(messages) == 0 {
		return messages
	}
	var system []types.Message
	rest := messages
	for len(rest) > 0 && rest[0].Role == "system" {
		system = append(system, rest[0])
		rest = rest[1:]
	}
	if keep <= 0 || len(rest) <= keep {
		return messages
	}
	start := safeTailBoundary(rest, len(rest)-keep)
	out := make([]types.Message, 0, len(system)+len(rest)-start)
	out = append(out, system...)
	out = append(out, rest[start:]...)
	return out
}

func loggerRetry(op string, attempt int, err error) {
	logger.LogError(op, err, slog.Int("attempt", attempt))
}

// chatWithToolsRetry retries transient failures with bounded exponential
// backoff. Context cancellation is never retried.
func (ae *AgentEngine) chatWithToolsRetry(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	var lastErr error
	for attempt := 0; attempt < llmRetryAttempts; attempt++ {
		resp, err := ae.model.ChatWithTools(ctx, messages, tools)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil || !isRetryableLLMError(err) || attempt == llmRetryAttempts-1 {
			break
		}
		loggerRetry("chatWithTools", attempt, err)
		select {
		case <-time.After(retryDelayForError(err, attempt)):
		case <-ctx.Done():
			return types.Message{}, ctx.Err()
		}
	}
	return types.Message{}, lastErr
}

// openToolStreamRetry retries a failed stream *open*. Errors delivered as
// stream messages after the channel is open are not retried (partial output
// may have been consumed already).
func (ae *AgentEngine) openToolStreamRetry(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	var lastErr error
	for attempt := 0; attempt < llmRetryAttempts; attempt++ {
		stream, err := ae.model.ChatWithToolsStream(ctx, messages, tools)
		if err == nil {
			return stream, nil
		}
		lastErr = err
		if ctx.Err() != nil || !isRetryableLLMError(err) || attempt == llmRetryAttempts-1 {
			break
		}
		loggerRetry("chatWithToolsStream", attempt, err)
		select {
		case <-time.After(retryDelayForError(err, attempt)):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}
