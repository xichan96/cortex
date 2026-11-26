package model

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// FailureClass identifies why a provider request failed. It is the
// provider-neutral half of the "what should the caller do next" decision:
// failure classes map to a primary FailureAction, and a credential pool or
// target router can add its own fallback (for example: try another credential
// first, then another target).
//
// The taxonomy follows the LLM gateway design used by Dojo
// (docs/design/llm-gateway.md §5.4); it is deliberately wire-level, so callers
// do not need to parse provider-specific error bodies themselves.
type FailureClass string

const (
	// FailureCredential means the upstream rejected the credential (401).
	FailureCredential FailureClass = "credential"
	// FailureRateLimit means the upstream asked the caller to slow down (429
	// without a quota signal).
	FailureRateLimit FailureClass = "rate_limit"
	// FailureQuota means the account is out of budget or access was blocked
	// (402, or quota/credit/billing text). Credentials usually need an
	// operator to re-enable them rather than a short cooldown.
	FailureQuota FailureClass = "quota"
	// FailureTransient means a temporary 5xx/408/409-style failure that is
	// worth retrying against the same credential.
	FailureTransient FailureClass = "transient"
	// FailureOverloaded means the upstream is at capacity (529 / overloaded
	// text). Switching to another target is more useful than retrying the same
	// one.
	FailureOverloaded FailureClass = "overloaded"
	// FailureRegionBlocked means the request came from an unsupported country
	// or region.
	FailureRegionBlocked FailureClass = "region_blocked"
	// FailureClient means the request itself is invalid (400/413/422,
	// context-too-long, unknown model). Retrying cannot help.
	FailureClient FailureClass = "client"
	// FailureNetwork means no HTTP response was received (dial/TLS/reset).
	FailureNetwork FailureClass = "network"
	// FailureCanceled means the caller canceled the request or its deadline
	// expired. Retrying cannot help.
	FailureCanceled FailureClass = "canceled"
	// FailureStreamBroken means the response had already started streaming
	// when the connection failed. The request is no longer replayable, so
	// callers must not retry it. Classify does not return this value today;
	// stream-aware callers should use it when they know content was emitted.
	FailureStreamBroken FailureClass = "stream_broken"
	// FailureUnknown is the fallback for errors that carry no usable signal.
	FailureUnknown FailureClass = "unknown"
)

// FailureAction is the first recovery step for a failure. A credential pool or
// target router may follow up: ActionSwitchCredential falls back to another
// target when every credential is cooled down, and ActionRetrySame falls back
// to another target when retries are exhausted.
type FailureAction int

const (
	// ActionNone means there was no failure (or the caller supplied nil).
	ActionNone FailureAction = iota
	// ActionRetrySame retries the same target/credential after a backoff.
	ActionRetrySame
	// ActionSwitchCredential tries another credential for the same provider,
	// then another target when none is available.
	ActionSwitchCredential
	// ActionSwitchTarget tries another provider/target.
	ActionSwitchTarget
	// ActionNoRetry surfaces the error to the caller.
	ActionNoRetry
)

// String renders the action for logs and test failures.
func (a FailureAction) String() string {
	switch a {
	case ActionNone:
		return "none"
	case ActionRetrySame:
		return "retry_same"
	case ActionSwitchCredential:
		return "switch_credential"
	case ActionSwitchTarget:
		return "switch_target"
	case ActionNoRetry:
		return "no_retry"
	default:
		return "unknown"
	}
}

// FailureActionFor maps a class to its primary recovery action.
func FailureActionFor(class FailureClass) FailureAction {
	switch class {
	case FailureTransient:
		return ActionRetrySame
	case FailureOverloaded:
		return ActionSwitchTarget
	case FailureCredential, FailureRateLimit, FailureQuota, FailureRegionBlocked, FailureNetwork:
		return ActionSwitchCredential
	case FailureClient, FailureCanceled, FailureStreamBroken, FailureUnknown:
		return ActionNoRetry
	default:
		return ActionNoRetry
	}
}

// Cooldown reports how long a credential should be skipped after this class.
// Zero means no automatic cooldown. FailureQuota is special: callers should
// usually disable the credential until an operator re-enables it, because
// waiting out a cooldown will not restore a depleted account.
func (c FailureClass) Cooldown() time.Duration {
	switch c {
	case FailureCredential, FailureRegionBlocked:
		return 10 * time.Minute
	case FailureRateLimit:
		return time.Minute
	case FailureNetwork:
		return 30 * time.Second
	default:
		return 0
	}
}

// Classify inspects err and returns its failure class plus the primary action.
// It understands *TransportError, *StreamErrorInfo and wrapped forms of both;
// plain errors (a connection failure with no HTTP response) classify as
// FailureNetwork.
//
// Context cancellation and deadlines classify as FailureCanceled: the caller
// asked to stop, so retrying another target would only waste work.
func Classify(err error) (FailureClass, FailureAction) {
	if err == nil {
		return "", ActionNone
	}
	class := classify(err)
	return class, FailureActionFor(class)
}

func classify(err error) FailureClass {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return FailureCanceled
	}

	status, text := errorStatusText(err)
	switch status {
	case http.StatusUnauthorized:
		return FailureCredential
	case http.StatusPaymentRequired:
		return FailureQuota
	case http.StatusTooManyRequests:
		if containsAny(text, quotaTexts) {
			return FailureQuota
		}
		return FailureRateLimit
	case 529:
		return FailureOverloaded
	case http.StatusRequestTimeout:
		return FailureTransient
	case http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		if containsAny(text, overloadedTexts) {
			return FailureOverloaded
		}
		return FailureTransient
	}

	switch {
	case containsAny(text, quotaTexts):
		return FailureQuota
	case containsAny(text, credentialTexts):
		return FailureCredential
	case containsAny(text, regionTexts):
		return FailureRegionBlocked
	case containsAny(text, rateLimitTexts):
		return FailureRateLimit
	case containsAny(text, overloadedTexts):
		return FailureOverloaded
	}

	switch {
	case status >= 400 && status < 500:
		return FailureClient
	case status >= 500:
		return FailureTransient
	}
	return FailureNetwork
}

// errorStatusText extracts the HTTP status and a lowercase text blob from an
// error. StreamErrorInfo wins on status because it describes what was sent to
// the caller; a wrapped TransportError supplies the same status (and headers)
// when the stream type is absent.
func errorStatusText(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	status := 0
	parts := make([]string, 0, 3)

	var streamErr *StreamErrorInfo
	if errors.As(err, &streamErr) {
		status = streamErr.StatusCode
		parts = append(parts, streamErr.Code, streamErr.Message)
	}
	var transportErr *TransportError
	if errors.As(err, &transportErr) {
		if status == 0 {
			status = transportErr.StatusCode
		}
		parts = append(parts, transportErr.Error())
	}
	if len(parts) == 0 {
		parts = append(parts, err.Error())
	}
	return status, strings.ToLower(strings.Join(parts, " "))
}

func containsAny(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// Text tables cover OpenAI, Anthropic and the common compatible providers.
// Unknown statuses fall back to the 4xx/5xx default above.
var (
	credentialTexts = []string{
		"invalid_api_key",
		"incorrect api key",
		"invalid api key",
		"authentication_error",
		"unauthenticated",
		"invalid authentication",
		"api key not valid",
	}
	quotaTexts = []string{
		"insufficient_quota",
		"credit balance",
		"spend limit",
		"blocked_api_access",
		"exceeded your current quota",
		"billing_hard_limit",
	}
	rateLimitTexts = []string{
		"rate limit",
		"rate_limit",
		"too many requests",
		"quota_exceeded",
		"resource_exhausted",
		"requests per minute",
		"tokens per minute",
	}
	overloadedTexts = []string{
		"overloaded_error",
		"overloaded",
		"capacity",
	}
	regionTexts = []string{
		"unsupported countries",
		"location is not supported",
		"country not supported",
		"region is not supported",
	}
)
