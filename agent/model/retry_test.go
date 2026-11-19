package model

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type sleepRecorder struct {
	delays []time.Duration
	fn     func(context.Context, time.Duration) error
}

func (r *sleepRecorder) sleep(ctx context.Context, d time.Duration) error {
	r.delays = append(r.delays, d)
	if r.fn != nil {
		return r.fn(ctx, d)
	}
	return nil
}

func transportError(status int, headers http.Header) *TransportError {
	return NewTransportError(status, headers, "", nil)
}

func TestIsRetryableTransportError(t *testing.T) {
	headers := func(key, value string) http.Header {
		h := http.Header{}
		h.Set(key, value)
		return h
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"408", transportError(http.StatusRequestTimeout, nil), true},
		{"409", transportError(http.StatusConflict, nil), true},
		{"429", transportError(http.StatusTooManyRequests, nil), true},
		{"500", transportError(500, nil), true},
		{"503", transportError(503, nil), true},
		{"400", transportError(400, nil), false},
		{"401", transportError(401, nil), false},
		{"404", transportError(404, nil), false},
		{"no response", transportError(0, nil), true},
		{"plain network error", errors.New("connection reset"), true},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"x-should-retry true overrides 400", transportError(400, headers("x-should-retry", "true")), true},
		{"x-should-retry false overrides 503", transportError(503, headers("x-should-retry", "false")), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryableTransportError(tt.err); got != tt.want {
				t.Fatalf("IsRetryableTransportError = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTransportRetryDelay(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

	t.Run("exponential backoff", func(t *testing.T) {
		policy := TransportRetryPolicy{BaseDelay: 500 * time.Millisecond, MaxDelay: 8 * time.Second}
		for _, tc := range []struct {
			index int
			want  time.Duration
		}{{0, 500 * time.Millisecond}, {1, time.Second}, {2, 2 * time.Second}, {4, 8 * time.Second}} {
			got, err := TransportRetryDelay(errors.New("network"), tc.index, now, policy)
			if err != nil || got != tc.want {
				t.Fatalf("index %d: got %v, %v want %v", tc.index, got, err, tc.want)
			}
		}
	})

	t.Run("max delay caps exponential", func(t *testing.T) {
		policy := TransportRetryPolicy{BaseDelay: 500 * time.Millisecond, MaxDelay: time.Second}
		got, err := TransportRetryDelay(errors.New("network"), 5, now, policy)
		if err != nil || got != time.Second {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("retry-after-ms wins", func(t *testing.T) {
		h := http.Header{}
		h.Set("retry-after-ms", "1500")
		h.Set("retry-after", "99")
		got, err := TransportRetryDelay(transportError(429, h), 0, now, TransportRetryPolicy{})
		if err != nil || got != 1500*time.Millisecond {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("retry-after seconds", func(t *testing.T) {
		h := http.Header{}
		h.Set("retry-after", "2")
		got, err := TransportRetryDelay(transportError(429, h), 0, now, TransportRetryPolicy{})
		if err != nil || got != 2*time.Second {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("retry-after HTTP date", func(t *testing.T) {
		h := http.Header{}
		h.Set("retry-after", now.Add(3*time.Second).Format(http.TimeFormat))
		got, err := TransportRetryDelay(transportError(503, h), 0, now, TransportRetryPolicy{})
		if err != nil || got != 3*time.Second {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("past HTTP date clamps to zero", func(t *testing.T) {
		h := http.Header{}
		h.Set("retry-after", now.Add(-time.Minute).Format(http.TimeFormat))
		got, err := TransportRetryDelay(transportError(503, h), 0, now, TransportRetryPolicy{})
		if err != nil || got != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("server delay above cap", func(t *testing.T) {
		h := http.Header{}
		h.Set("retry-after", "5")
		_, err := TransportRetryDelay(transportError(429, h), 0, now, TransportRetryPolicy{MaxRetryDelay: time.Second})
		if !errors.Is(err, ErrRetryDelayTooLong) {
			t.Fatalf("err = %v, want ErrRetryDelayTooLong", err)
		}
	})

	t.Run("negative cap disables cap", func(t *testing.T) {
		h := http.Header{}
		h.Set("retry-after", "5")
		got, err := TransportRetryDelay(transportError(429, h), 0, now, TransportRetryPolicy{MaxRetryDelay: -1})
		if err != nil || got != 5*time.Second {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("jitter", func(t *testing.T) {
		policy := TransportRetryPolicy{
			BaseDelay: 400 * time.Millisecond,
			Jitter:    func() float64 { return 0.5 },
		}
		got, err := TransportRetryDelay(errors.New("network"), 0, now, policy)
		if err != nil || got != 350*time.Millisecond {
			t.Fatalf("got %v, %v want 350ms", got, err)
		}
	})
}

func TestTransportRetrySucceedsAfterRetries(t *testing.T) {
	recorder := &sleepRecorder{}
	policy := TransportRetryPolicy{
		MaxRetries: 3,
		BaseDelay:  100 * time.Millisecond,
		Sleep:      recorder.sleep,
	}
	calls := 0
	err := TransportRetry(context.Background(), func(context.Context) error {
		calls++
		if calls < 3 {
			return transportError(503, nil)
		}
		return nil
	}, policy)
	if err != nil {
		t.Fatalf("TransportRetry: %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if len(recorder.delays) != 2 || recorder.delays[0] != 100*time.Millisecond || recorder.delays[1] != 200*time.Millisecond {
		t.Fatalf("delays = %v", recorder.delays)
	}
}

func TestTransportRetryStopsOnTerminalErrors(t *testing.T) {
	recorder := &sleepRecorder{}
	policy := TransportRetryPolicy{MaxRetries: 3, Sleep: recorder.sleep}

	t.Run("non-retryable status", func(t *testing.T) {
		calls := 0
		err := TransportRetry(context.Background(), func(context.Context) error {
			calls++
			return transportError(401, nil)
		}, policy)
		if err == nil || calls != 1 || len(recorder.delays) != 0 {
			t.Fatalf("err=%v calls=%d delays=%v", err, calls, recorder.delays)
		}
	})

	t.Run("no-retry status override", func(t *testing.T) {
		calls := 0
		err := TransportRetry(context.Background(), func(context.Context) error {
			calls++
			return transportError(503, nil)
		}, TransportRetryPolicy{MaxRetries: 3, NoRetryStatuses: []int{503}, Sleep: recorder.sleep})
		if err == nil || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	})

	t.Run("budget exhausted", func(t *testing.T) {
		calls := 0
		err := TransportRetry(context.Background(), func(context.Context) error {
			calls++
			return transportError(503, nil)
		}, TransportRetryPolicy{MaxRetries: 2, Sleep: recorder.sleep})
		if err == nil || calls != 3 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		err := TransportRetry(ctx, func(context.Context) error {
			calls++
			return transportError(503, nil)
		}, policy)
		if !errors.Is(err, context.Canceled) || calls != 0 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	})

	t.Run("canceled during sleep", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancelingSleep := func(context.Context, time.Duration) error {
			cancel()
			return context.Canceled
		}
		calls := 0
		err := TransportRetry(ctx, func(context.Context) error {
			calls++
			return transportError(503, nil)
		}, TransportRetryPolicy{MaxRetries: 3, Sleep: cancelingSleep})
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	})
}

func TestSemanticErrorClassification(t *testing.T) {
	terminal := []string{
		"insufficient_quota",
		"Monthly usage limit reached",
		"billing hard limit reached",
		"401 Unauthorized",
		"invalid_request_error: max_tokens too large",
		"context_length_exceeded",
		"prompt is too long: 210000 tokens",
		"invalid api key",
	}
	for _, message := range terminal {
		if !IsTerminalSemanticError(message) {
			t.Errorf("%q should be terminal", message)
		}
		if IsRetryableSemanticError(message) {
			t.Errorf("%q should not be retryable", message)
		}
	}

	retryable := []string{
		"429 Too Many Requests",
		"overloaded_error",
		"The server is temporarily unavailable",
		"connection reset by peer",
		"read ECONNRESET",
		"stream ended without a terminal event",
		"you can retry your request",
	}
	for _, message := range retryable {
		if !IsRetryableSemanticError(message) {
			t.Errorf("%q should be retryable", message)
		}
		if IsTerminalSemanticError(message) {
			t.Errorf("%q should not be terminal", message)
		}
	}

	// A terminal phrase wins even when a retryable phrase also appears.
	if IsRetryableSemanticError("429 rate limit exceeded, quota exceeded") {
		t.Fatal("terminal error matched as retryable")
	}
	if IsRetryableSemanticError("") || IsTerminalSemanticError("") {
		t.Fatal("empty message should be neither")
	}
}

func TestIsRetryableAssistantError(t *testing.T) {
	tests := []struct {
		name    string
		message *AssistantMessage
		want    bool
	}{
		{"nil", nil, false},
		{"stop", &AssistantMessage{StopReason: StopReasonStop}, false},
		{"aborted", &AssistantMessage{StopReason: StopReasonAborted, ErrorMessage: "429"}, false},
		{"error without message", &AssistantMessage{StopReason: StopReasonError}, false},
		{"transient", &AssistantMessage{StopReason: StopReasonError, ErrorMessage: "429 rate limit"}, true},
		{"terminal", &AssistantMessage{StopReason: StopReasonError, ErrorMessage: "insufficient_quota"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryableAssistantError(tt.message); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSemanticRetryDelay(t *testing.T) {
	policy := RetryPolicy{BaseDelay: time.Second, MaxDelay: 3 * time.Second}
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{{0, time.Second}, {1, time.Second}, {2, 2 * time.Second}, {3, 3 * time.Second}, {10, 3 * time.Second}} {
		if got := SemanticRetryDelay(policy, tc.attempt); got != tc.want {
			t.Fatalf("attempt %d: got %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestSemanticRetryRetriesTransientMessages(t *testing.T) {
	recorder := &sleepRecorder{}
	policy := RetryPolicy{
		Enabled:    true,
		MaxRetries: 3,
		BaseDelay:  10 * time.Millisecond,
		Sleep:      recorder.sleep,
	}
	calls := 0
	var scheduled []time.Duration
	var finished struct {
		called  bool
		success bool
		attempt int
	}
	message, err := SemanticRetry(context.Background(), func(context.Context) (*AssistantMessage, error) {
		calls++
		if calls < 3 {
			return &AssistantMessage{StopReason: StopReasonError, ErrorMessage: "429 too many requests"}, nil
		}
		return &AssistantMessage{StopReason: StopReasonStop}, nil
	}, policy, &SemanticRetryCallbacks{
		OnRetryScheduled: func(attempt, maxAttempts int, delay time.Duration, errorMessage string) {
			scheduled = append(scheduled, delay)
		},
		OnRetryFinished: func(success bool, attempt int, finalError string) {
			finished.called = true
			finished.success = success
			finished.attempt = attempt
		},
	})
	if err != nil {
		t.Fatalf("SemanticRetry: %v", err)
	}
	if message.StopReason != StopReasonStop || calls != 3 {
		t.Fatalf("message=%+v calls=%d", message, calls)
	}
	if len(recorder.delays) != 2 || recorder.delays[0] != 10*time.Millisecond || recorder.delays[1] != 20*time.Millisecond {
		t.Fatalf("delays = %v", recorder.delays)
	}
	if len(scheduled) != 2 {
		t.Fatalf("scheduled callbacks = %d", len(scheduled))
	}
	if !finished.called || !finished.success || finished.attempt != 2 {
		t.Fatalf("finished = %+v", finished)
	}
}

func TestSemanticRetryDoesNotRetryTerminalOrAbortedMessages(t *testing.T) {
	t.Run("terminal", func(t *testing.T) {
		calls := 0
		message, err := SemanticRetry(context.Background(), func(context.Context) (*AssistantMessage, error) {
			calls++
			return &AssistantMessage{StopReason: StopReasonError, ErrorMessage: "insufficient_quota"}, nil
		}, RetryPolicy{Enabled: true, MaxRetries: 3}, nil)
		if err != nil || calls != 1 || message.StopReason != StopReasonError {
			t.Fatalf("err=%v calls=%d message=%+v", err, calls, message)
		}
	})

	t.Run("aborted", func(t *testing.T) {
		calls := 0
		message, err := SemanticRetry(context.Background(), func(context.Context) (*AssistantMessage, error) {
			calls++
			return &AssistantMessage{StopReason: StopReasonAborted}, nil
		}, RetryPolicy{Enabled: true, MaxRetries: 3}, nil)
		if err != nil || calls != 1 || message.StopReason != StopReasonAborted {
			t.Fatalf("err=%v calls=%d message=%+v", err, calls, message)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		calls := 0
		message, err := SemanticRetry(context.Background(), func(context.Context) (*AssistantMessage, error) {
			calls++
			return &AssistantMessage{StopReason: StopReasonError, ErrorMessage: "429"}, nil
		}, RetryPolicy{Enabled: false, MaxRetries: 3}, nil)
		if err != nil || calls != 1 || message.StopReason != StopReasonError {
			t.Fatalf("err=%v calls=%d message=%+v", err, calls, message)
		}
	})
}

func TestSemanticRetryExhaustionAndCancellation(t *testing.T) {
	t.Run("exhaustion", func(t *testing.T) {
		calls := 0
		recorder := &sleepRecorder{}
		message, err := SemanticRetry(context.Background(), func(context.Context) (*AssistantMessage, error) {
			calls++
			return &AssistantMessage{StopReason: StopReasonError, ErrorMessage: "503 service unavailable"}, nil
		}, RetryPolicy{Enabled: true, MaxRetries: 1, BaseDelay: time.Millisecond, Sleep: recorder.sleep}, nil)
		if err != nil || calls != 2 || message.StopReason != StopReasonError {
			t.Fatalf("err=%v calls=%d message=%+v", err, calls, message)
		}
		if len(recorder.delays) != 1 {
			t.Fatalf("delays = %v", recorder.delays)
		}
	})

	t.Run("canceled during backoff", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		message, err := SemanticRetry(ctx, func(context.Context) (*AssistantMessage, error) {
			calls++
			return &AssistantMessage{StopReason: StopReasonError, ErrorMessage: "429"}, nil
		}, RetryPolicy{
			Enabled:    true,
			MaxRetries: 3,
			Sleep: func(context.Context, time.Duration) error {
				cancel()
				return context.Canceled
			},
		}, nil)
		if err != nil {
			t.Fatalf("SemanticRetry: %v", err)
		}
		if calls != 1 || message.StopReason != StopReasonAborted {
			t.Fatalf("calls=%d stop=%q", calls, message.StopReason)
		}
	})

	t.Run("producer error", func(t *testing.T) {
		want := errors.New("boom")
		_, err := SemanticRetry(context.Background(), func(context.Context) (*AssistantMessage, error) {
			return nil, want
		}, RetryPolicy{Enabled: true, MaxRetries: 3}, nil)
		if !errors.Is(err, want) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("nil producer", func(t *testing.T) {
		if _, err := SemanticRetry(context.Background(), nil, RetryPolicy{Enabled: true}, nil); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestTransportRetryNilRequest(t *testing.T) {
	if err := TransportRetry(context.Background(), nil, TransportRetryPolicy{}); err == nil {
		t.Fatal("expected error")
	}
}
