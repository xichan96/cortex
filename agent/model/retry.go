package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SleepFunc waits for d or until ctx is canceled. Tests inject a recorder.
type SleepFunc func(ctx context.Context, d time.Duration) error

// DefaultTransportMaxRetries is a suggested retry budget for callers that want
// the library's general-purpose transport policy. Providers use
// DefaultProviderRetryMaxRetries unless their Config overrides it. The initial
// attempt is not counted in either value.
const DefaultTransportMaxRetries = 2

// DefaultProviderRetryMaxRetries is the number of pre-stream transport retries
// a provider performs when its Config.Retry is nil. One retry keeps latency
// bounded when the endpoint is a gateway that already retries upstream calls;
// hosts can set MaxRetries to 0 to disable provider-level retries entirely.
const DefaultProviderRetryMaxRetries = 1

const (
	defaultTransportBaseDelay = 500 * time.Millisecond
	defaultTransportMaxDelay  = 8 * time.Second
	defaultTransportMaxRetry  = 60 * time.Second
)

// TransportError is a provider HTTP/transport failure. A zero StatusCode means
// the request never produced an HTTP response (connection failure).
type TransportError struct {
	StatusCode int
	Headers    http.Header
	Message    string
	Err        error
}

// NewTransportError builds a TransportError.
func NewTransportError(statusCode int, headers http.Header, message string, err error) *TransportError {
	return &TransportError{StatusCode: statusCode, Headers: headers, Message: message, Err: err}
}

// Error implements error.
func (e *TransportError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("transport error: HTTP %d", e.StatusCode)
	}
	return "transport error"
}

// Unwrap exposes the underlying error.
func (e *TransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// TransportRetryPolicy configures transport-level retries. Zero values use
// defaults. A negative MaxRetryDelay disables the server-requested delay cap.
type TransportRetryPolicy struct {
	MaxRetries      int
	BaseDelay       time.Duration
	MaxDelay        time.Duration
	MaxRetryDelay   time.Duration
	NoRetryStatuses []int
	Sleep           SleepFunc
	Now             func() time.Time
	// Jitter returns a value in [0,1). Nil means no jitter, which keeps delay
	// computation deterministic.
	Jitter func() float64
}

func (p TransportRetryPolicy) withDefaults() TransportRetryPolicy {
	if p.MaxRetries < 0 {
		p.MaxRetries = 0
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = defaultTransportBaseDelay
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = defaultTransportMaxDelay
	}
	if p.MaxRetryDelay == 0 {
		p.MaxRetryDelay = defaultTransportMaxRetry
	}
	if p.Sleep == nil {
		p.Sleep = SleepWithContext
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	return p
}

// ErrRetryDelayTooLong reports that a server-requested retry delay exceeds the
// configured cap. The caller should surface the original error instead of
// sleeping, so a higher-level policy can decide.
var ErrRetryDelayTooLong = errors.New("server requested retry delay exceeds cap")

// IsRetryableTransportError classifies a transport failure. It honors
// x-should-retry, retries 408/409/429/5xx and connection failures, and treats
// context cancellation as terminal.
func IsRetryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var transportErr *TransportError
	if !errors.As(err, &transportErr) {
		// Plain errors at this layer are connection/read failures.
		return true
	}
	if hint := transportErr.Headers.Get("x-should-retry"); hint != "" {
		switch strings.ToLower(strings.TrimSpace(hint)) {
		case "true":
			return true
		case "false":
			return false
		}
	}
	if transportErr.StatusCode == 0 {
		return true
	}
	status := transportErr.StatusCode
	return status == http.StatusRequestTimeout ||
		status == http.StatusConflict ||
		status == http.StatusTooManyRequests ||
		status >= 500
}

// TransportRetryDelay computes the delay before the next transport attempt.
// retryIndex is zero-based. Server-provided retry-after-ms / retry-after values
// take precedence over exponential backoff. A delay above MaxRetryDelay returns
// ErrRetryDelayTooLong.
func TransportRetryDelay(err error, retryIndex int, now time.Time, policy TransportRetryPolicy) (time.Duration, error) {
	p := policy.withDefaults()
	if retryIndex < 0 {
		retryIndex = 0
	}

	if transportErr := asTransportError(err); transportErr != nil && transportErr.Headers != nil {
		if raw := transportErr.Headers.Get("retry-after-ms"); raw != "" {
			if value, parseErr := strconv.ParseFloat(strings.TrimSpace(raw), 64); parseErr == nil {
				if delay, ok := floatToDuration(value, time.Millisecond); ok {
					return capRetryDelay(delay, p, transportErr.Error())
				}
			}
		}
		if raw := transportErr.Headers.Get("retry-after"); raw != "" {
			if seconds, parseErr := strconv.ParseFloat(strings.TrimSpace(raw), 64); parseErr == nil {
				if delay, ok := floatToDuration(seconds, time.Second); ok {
					return capRetryDelay(delay, p, transportErr.Error())
				}
			}
			if at, parseErr := http.ParseTime(strings.TrimSpace(raw)); parseErr == nil {
				return capRetryDelay(at.Sub(now), p, transportErr.Error())
			}
		}
	}

	delay := p.BaseDelay
	for i := 0; i < retryIndex && delay < p.MaxDelay; i++ {
		delay *= 2
	}
	if delay > p.MaxDelay {
		delay = p.MaxDelay
	}
	if p.Jitter != nil {
		jitter := p.Jitter()
		if jitter < 0 {
			jitter = 0
		} else if jitter >= 1 {
			jitter = 0.999999
		}
		delay -= time.Duration(float64(delay) * 0.25 * jitter)
	}
	if delay < 0 {
		delay = 0
	}
	return delay, nil
}

// floatToDuration converts a server-provided value (for example milliseconds
// or seconds) into a duration, rejecting NaN, negatives and overflow.
func floatToDuration(value float64, unit time.Duration) (time.Duration, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, false
	}
	limit := float64(math.MaxInt64) / float64(unit)
	if value > limit {
		return math.MaxInt64, true
	}
	return time.Duration(value * float64(unit)), true
}

func asTransportError(err error) *TransportError {
	var transportErr *TransportError
	if errors.As(err, &transportErr) {
		return transportErr
	}
	return nil
}

func capRetryDelay(delay time.Duration, policy TransportRetryPolicy, message string) (time.Duration, error) {
	if delay < 0 {
		delay = 0
	}
	if policy.MaxRetryDelay > 0 && delay > policy.MaxRetryDelay {
		return 0, fmt.Errorf("%w: server requested %s (max %s): %s",
			ErrRetryDelayTooLong, delay, policy.MaxRetryDelay, message)
	}
	return delay, nil
}

// TransportRetry runs request with bounded retries. It is intended for
// provider HTTP calls. Context cancellation aborts both the request and any
// backoff sleep.
func TransportRetry(ctx context.Context, request func(context.Context) error, policy TransportRetryPolicy) error {
	if request == nil {
		return errors.New("model: nil transport request")
	}
	p := policy.withDefaults()
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := request(ctx)
		if err == nil {
			return nil
		}
		if !IsRetryableTransportError(err) || statusIn(p.NoRetryStatuses, err) {
			return err
		}
		if attempt >= p.MaxRetries {
			return err
		}
		delay, delayErr := TransportRetryDelay(err, attempt, p.Now(), p)
		if delayErr != nil {
			return fmt.Errorf("%w: %w", delayErr, err)
		}
		if sleepErr := p.Sleep(ctx, delay); sleepErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return sleepErr
		}
	}
}

func statusIn(statuses []int, err error) bool {
	if len(statuses) == 0 {
		return false
	}
	transportErr := asTransportError(err)
	if transportErr == nil {
		return false
	}
	for _, status := range statuses {
		if transportErr.StatusCode == status {
			return true
		}
	}
	return false
}

// SleepWithContext is the default SleepFunc.
func SleepWithContext(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RetryPolicy is the engine-level (semantic) retry policy. Attempts use
// exponential backoff: BaseDelay * 2^(attempt-1), capped at MaxDelay.
type RetryPolicy struct {
	Enabled    bool
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	Sleep      SleepFunc
}

// DefaultRetryPolicy returns the standard engine retry policy.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		Enabled:    true,
		MaxRetries: 3,
		BaseDelay:  time.Second,
		MaxDelay:   60 * time.Second,
	}
}

func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.MaxRetries < 0 {
		p.MaxRetries = 0
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = time.Second
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = 60 * time.Second
	}
	if p.Sleep == nil {
		p.Sleep = SleepWithContext
	}
	return p
}

// SemanticRetryDelay computes the bounded exponential backoff for a retry.
// attempt is one-based.
func SemanticRetryDelay(policy RetryPolicy, attempt int) time.Duration {
	p := policy.withDefaults()
	if attempt < 1 {
		attempt = 1
	}
	delay := p.BaseDelay
	for i := 1; i < attempt && delay < p.MaxDelay; i++ {
		delay *= 2
	}
	if delay > p.MaxDelay {
		delay = p.MaxDelay
	}
	return delay
}

var (
	nonRetryableProviderPattern = regexp.MustCompile(`(?i)` + strings.Join([]string{
		// Subscription/account limits that reset after hours, not seconds.
		`GoUsageLimitError`,
		`FreeUsageLimitError`,
		`monthly usage limit reached`,
		`available balance`,
		`subscription_sharing_usage_limit_exceeded`,
		`subscription_sharing_workplace_individual_usage_limit_exceeded`,
		// Quota, budget, billing and authentication errors.
		`insufficient_quota`,
		`quota[ _]exceeded`,
		`out of budget`,
		`billing`,
		`invalid[ _]api[ _]key`,
		`invalid api key`,
		`incorrect api key`,
		`unauthorized`,
		`forbidden`,
		`permission denied`,
		// Deterministic request/parameter errors.
		`invalid_request_error`,
		`bad request`,
		`model_not_found`,
		`content[ _]policy`,
		`context_length_exceeded`,
		`maximum context length`,
		`context window`,
		`prompt is too long`,
		`request too large`,
		// HTTP 400/401/403/404/413/422 with token boundaries, so request ids and
		// byte counts do not match.
		`(?:^|[^0-9])(?:400|401|403|404|413|422)(?:[^0-9]|$)`,
	}, "|"))

	retryableProviderPattern = regexp.MustCompile(`(?i)` + strings.Join([]string{
		`overloaded`,
		`server_busy`,
		`servers are currently busy`,
		`currently experiencing high demand`,
		`model is at capacity`,
		`rate.?limit`,
		`too many requests`,
		`(?:^|[^0-9])(?:408|409|429|500|502|503|504|520|524|529)(?:[^0-9]|$)`,
		`(?:service|server).?(?:is.?)?(?:temporarily.?)?unavailable`,
		`server.?error`,
		`internal.?error`,
		`provider.?returned.?(?:an.?)?error`,
		`unable to forward upstream`,
		`upstream.?request.?failed`,
		`stream was interrupted`,
		`stream disconnected before completion`,
		`ended without`,
		`stream ended before`,
		`network.?error`,
		`connection.?(?:error|refused|lost|closed|reset)`,
		`other side closed`,
		`fetch failed`,
		`request to \S+ failed, reason:`,
		`getaddrinfo`,
		`(?:ECONNRESET|ECONNABORTED|ECONNREFUSED|ETIMEDOUT|EPIPE|ENOTFOUND|EAI_AGAIN)`,
		`socket hang up`,
		`websocket.?(?:closed|error)`,
		`timed? out`,
		`timeout`,
		`ResourceExhausted`,
		`you can retry your request`,
		`try your request again`,
		`please retry your request`,
		`retry delay`,
	}, "|"))
)

// IsTerminalSemanticError reports whether an error message is deterministically
// non-retryable (quota/billing/auth/parameters/context overflow).
func IsTerminalSemanticError(message string) bool {
	if strings.TrimSpace(message) == "" {
		return false
	}
	return nonRetryableProviderPattern.MatchString(message)
}

// IsRetryableSemanticError reports whether an error message looks transient.
// Terminal errors win even when they also match a transient keyword.
func IsRetryableSemanticError(message string) bool {
	if strings.TrimSpace(message) == "" {
		return false
	}
	if IsTerminalSemanticError(message) {
		return false
	}
	return retryableProviderPattern.MatchString(message)
}

// IsRetryableAssistantError reports whether a failed assistant message is
// transient and safe to retry. Aborted messages are never retried.
func IsRetryableAssistantError(message *AssistantMessage) bool {
	if message == nil || message.StopReason != StopReasonError {
		return false
	}
	return IsRetryableSemanticError(message.ErrorMessage)
}

// SemanticRetryCallbacks observes the retry loop. All fields are optional.
type SemanticRetryCallbacks struct {
	OnRetryScheduled    func(attempt, maxAttempts int, delay time.Duration, errorMessage string)
	OnRetryAttemptStart func()
	OnRetryFinished     func(success bool, attempt int, finalError string)
}

// SemanticRetry runs produce until it returns a non-error message, a terminal
// error, or the retry budget is exhausted. Transport errors returned directly
// from produce are not retried here; providers own transport retry. Cancellation
// during backoff returns the failed message rewritten as aborted.
func SemanticRetry(
	ctx context.Context,
	produce func(context.Context) (*AssistantMessage, error),
	policy RetryPolicy,
	callbacks *SemanticRetryCallbacks,
) (*AssistantMessage, error) {
	if produce == nil {
		return nil, errors.New("model: nil semantic retry producer")
	}
	if !policy.Enabled {
		return produce(ctx)
	}
	p := policy.withDefaults()
	attempt := 0
	var lastRetry *struct {
		attempt      int
		errorMessage string
	}

	finish := func(success bool, finalError string) {
		if lastRetry != nil && callbacks != nil && callbacks.OnRetryFinished != nil {
			callbacks.OnRetryFinished(success, lastRetry.attempt, finalError)
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		message, err := produce(ctx)
		if err != nil {
			return nil, err
		}
		if message == nil {
			return nil, errors.New("model: semantic retry producer returned a nil message")
		}
		if message.StopReason == StopReasonAborted {
			finish(false, message.ErrorMessage)
			return message, nil
		}
		if message.StopReason != StopReasonError {
			finish(true, "")
			return message, nil
		}
		if attempt >= p.MaxRetries || !IsRetryableAssistantError(message) {
			finish(false, message.ErrorMessage)
			return message, nil
		}

		attempt++
		lastRetry = &struct {
			attempt      int
			errorMessage string
		}{attempt: attempt, errorMessage: message.ErrorMessage}
		delay := SemanticRetryDelay(p, attempt)
		if callbacks != nil && callbacks.OnRetryScheduled != nil {
			callbacks.OnRetryScheduled(attempt, p.MaxRetries, delay, message.ErrorMessage)
		}
		if sleepErr := p.Sleep(ctx, delay); sleepErr != nil {
			finish(false, lastRetry.errorMessage)
			aborted := *message
			aborted.StopReason = StopReasonAborted
			return &aborted, nil
		}
		if callbacks != nil && callbacks.OnRetryAttemptStart != nil {
			callbacks.OnRetryAttemptStart()
		}
	}
}
