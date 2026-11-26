package model

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		class FailureClass
		act   FailureAction
	}{
		{
			name:  "nil",
			err:   nil,
			class: "",
			act:   ActionNone,
		},
		{
			name:  "caller canceled",
			err:   context.Canceled,
			class: FailureCanceled,
			act:   ActionNoRetry,
		},
		{
			name:  "caller deadline",
			err:   fmt.Errorf("request: %w", context.DeadlineExceeded),
			class: FailureCanceled,
			act:   ActionNoRetry,
		},
		{
			name:  "unauthorized",
			err:   &TransportError{StatusCode: http.StatusUnauthorized, Message: "unauthorized"},
			class: FailureCredential,
			act:   ActionSwitchCredential,
		},
		{
			name:  "payment required",
			err:   &TransportError{StatusCode: http.StatusPaymentRequired, Message: "payment required"},
			class: FailureQuota,
			act:   ActionSwitchCredential,
		},
		{
			name:  "rate limited",
			err:   &TransportError{StatusCode: http.StatusTooManyRequests, Message: "rate limit reached"},
			class: FailureRateLimit,
			act:   ActionSwitchCredential,
		},
		{
			name:  "429 with quota text",
			err:   &TransportError{StatusCode: http.StatusTooManyRequests, Message: `{"error":{"type":"insufficient_quota"}}`},
			class: FailureQuota,
			act:   ActionSwitchCredential,
		},
		{
			name:  "529 overloaded",
			err:   &TransportError{StatusCode: 529, Message: "overloaded"},
			class: FailureOverloaded,
			act:   ActionSwitchTarget,
		},
		{
			name:  "500 overloaded text",
			err:   &TransportError{StatusCode: http.StatusInternalServerError, Message: "overloaded_error: try later"},
			class: FailureOverloaded,
			act:   ActionSwitchTarget,
		},
		{
			name:  "500 transient",
			err:   &TransportError{StatusCode: http.StatusInternalServerError, Message: "internal error"},
			class: FailureTransient,
			act:   ActionRetrySame,
		},
		{
			name:  "502 transient",
			err:   &TransportError{StatusCode: http.StatusBadGateway, Message: "bad gateway"},
			class: FailureTransient,
			act:   ActionRetrySame,
		},
		{
			name:  "408 transient",
			err:   &TransportError{StatusCode: http.StatusRequestTimeout, Message: "timeout"},
			class: FailureTransient,
			act:   ActionRetrySame,
		},
		{
			name:  "400 client",
			err:   &TransportError{StatusCode: http.StatusBadRequest, Message: "invalid_request_error"},
			class: FailureClient,
			act:   ActionNoRetry,
		},
		{
			name:  "context length is client error",
			err:   &TransportError{StatusCode: http.StatusBadRequest, Message: "context_length_exceeded"},
			class: FailureClient,
			act:   ActionNoRetry,
		},
		{
			name:  "403 with region text",
			err:   &TransportError{StatusCode: http.StatusForbidden, Message: "unsupported countries"},
			class: FailureRegionBlocked,
			act:   ActionSwitchCredential,
		},
		{
			name:  "403 unknown 4xx",
			err:   &TransportError{StatusCode: http.StatusForbidden, Message: "forbidden"},
			class: FailureClient,
			act:   ActionNoRetry,
		},
		{
			name:  "network",
			err:   &TransportError{Message: "dial tcp: connection refused"},
			class: FailureNetwork,
			act:   ActionSwitchCredential,
		},
		{
			name:  "plain network error",
			err:   errors.New("read: connection reset by peer"),
			class: FailureNetwork,
			act:   ActionSwitchCredential,
		},
		{
			name: "stream error carries status",
			err: &StreamErrorInfo{
				Code:       "rate_limit_error",
				Message:    "slow down",
				StatusCode: http.StatusTooManyRequests,
			},
			class: FailureRateLimit,
			act:   ActionSwitchCredential,
		},
		{
			name: "stream error unwraps transport",
			err: &StreamErrorInfo{
				Code:       "http_401",
				Message:    "unauthorized",
				StatusCode: http.StatusUnauthorized,
				Cause:      &TransportError{StatusCode: http.StatusUnauthorized},
			},
			class: FailureCredential,
			act:   ActionSwitchCredential,
		},
		{
			name:  "wrapped transport error",
			err:   fmt.Errorf("provider call: %w", &TransportError{StatusCode: http.StatusServiceUnavailable}),
			class: FailureTransient,
			act:   ActionRetrySame,
		},
		{
			name:  "unknown with no signal",
			err:   &StreamErrorInfo{Code: "request_error", Message: "malformed request"},
			class: FailureNetwork,
			act:   ActionSwitchCredential,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			class, action := Classify(tt.err)
			if class != tt.class || action != tt.act {
				t.Fatalf("Classify() = (%q, %s), want (%q, %s)", class, action, tt.class, tt.act)
			}
		})
	}
}

func TestFailureActionFor(t *testing.T) {
	tests := []struct {
		class FailureClass
		act   FailureAction
	}{
		{FailureCredential, ActionSwitchCredential},
		{FailureRateLimit, ActionSwitchCredential},
		{FailureQuota, ActionSwitchCredential},
		{FailureTransient, ActionRetrySame},
		{FailureOverloaded, ActionSwitchTarget},
		{FailureRegionBlocked, ActionSwitchCredential},
		{FailureClient, ActionNoRetry},
		{FailureNetwork, ActionSwitchCredential},
		{FailureCanceled, ActionNoRetry},
		{FailureStreamBroken, ActionNoRetry},
		{FailureUnknown, ActionNoRetry},
		{FailureClass("bogus"), ActionNoRetry},
	}
	for _, tt := range tests {
		if got := FailureActionFor(tt.class); got != tt.act {
			t.Fatalf("FailureActionFor(%q) = %s, want %s", tt.class, got, tt.act)
		}
	}
}

func TestFailureActionString(t *testing.T) {
	tests := map[FailureAction]string{
		ActionNone:             "none",
		ActionRetrySame:        "retry_same",
		ActionSwitchCredential: "switch_credential",
		ActionSwitchTarget:     "switch_target",
		ActionNoRetry:          "no_retry",
		FailureAction(99):      "unknown",
	}
	for action, want := range tests {
		if got := action.String(); got != want {
			t.Fatalf("%d.String() = %q, want %q", action, got, want)
		}
	}
}

func TestFailureCooldown(t *testing.T) {
	tests := map[FailureClass]time.Duration{
		FailureCredential:    10 * time.Minute,
		FailureRegionBlocked: 10 * time.Minute,
		FailureRateLimit:     time.Minute,
		FailureNetwork:       30 * time.Second,
		FailureQuota:         0,
		FailureTransient:     0,
		FailureOverloaded:    0,
		FailureClient:        0,
		FailureCanceled:      0,
		FailureStreamBroken:  0,
		FailureUnknown:       0,
	}
	for class, want := range tests {
		if got := class.Cooldown(); got != want {
			t.Fatalf("%q.Cooldown() = %s, want %s", class, got, want)
		}
	}
}
