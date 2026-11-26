// Package anthropic implements the model.Provider contract for Anthropic's
// Messages API (wire protocol "anthropic-messages").
//
// The provider is transport-neutral: it converts model.Request values into the
// JSON body Anthropic expects, performs one streaming HTTP call, and maps the
// SSE event sequence onto model.StreamEvent values. It deliberately does no
// semantic retry. It does perform bounded transport retries for failures that
// happen before the response stream starts (see Config.Retry); callers can
// layer model.SemanticRetry on top when they want semantic retries too.
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/xichan96/cortex/agent/model"
)

const (
	// DefaultBaseURL is the public Anthropic API root.
	DefaultBaseURL = "https://api.anthropic.com"
	// DefaultAPIVersion is the Messages API version this provider targets.
	DefaultAPIVersion = "2023-06-01"
	// defaultMaxTokens is a conservative fallback when neither the request nor
	// the catalog entry specifies max_tokens. Anthropic requires the field.
	defaultMaxTokens = 8192
	// defaultImageMediaType is used when an ImageContent block carries base64
	// data but no MIME type.
	defaultImageMediaType = "image/png"
	// maxSSELineBytes bounds a single SSE line. Tool arguments can be large, so
	// this is intentionally generous.
	maxSSELineBytes = 4 * 1024 * 1024
	// maxErrorBodyBytes bounds how much of a non-2xx response body is read.
	maxErrorBodyBytes = 64 * 1024
)

// Config configures a Provider.
type Config struct {
	// APIKey is sent as x-api-key. When empty the header is omitted so a
	// gateway that authenticates with a custom header (see Headers) still works.
	APIKey string
	// BaseURL is the API root. Empty means DefaultBaseURL. The provider appends
	// "/v1/messages"; do not include that path here.
	BaseURL string
	// APIVersion is sent as anthropic-version. Empty means DefaultAPIVersion.
	APIVersion string
	// Headers are added to every request. Config headers are applied after the
	// defaults, so they may override them.
	Headers map[string]string
	// HTTPClient is optional; nil means http.DefaultClient. A custom client can
	// set a proxy or transport, but should not set a total Timeout for streaming.
	HTTPClient *http.Client
	// Retry configures retries for failures that happen before the response
	// stream starts. Nil uses the default policy: one retry after the initial
	// attempt. Set MaxRetries to 0 to disable retries, for example when
	// BaseURL points at a gateway that already retries upstream calls.
	//
	// Once the response stream has started, failures are never retried: the
	// request is not replayable.
	Retry *model.TransportRetryPolicy
}

// Provider is the Anthropic Messages implementation of model.Provider. It is
// immutable after New and safe for concurrent use.
type Provider struct {
	apiKey     string
	baseURL    string
	apiVersion string
	headers    map[string]string
	client     *http.Client
	models     []model.Model
	retry      model.TransportRetryPolicy
}

// Compile-time contract check.
var _ model.Provider = (*Provider)(nil)

// New returns an Anthropic provider. Zero-value fields fall back to defaults.
func New(cfg Config) *Provider {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	apiVersion := strings.TrimSpace(cfg.APIVersion)
	if apiVersion == "" {
		apiVersion = DefaultAPIVersion
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	headers := make(map[string]string, len(cfg.Headers))
	for name, value := range cfg.Headers {
		headers[name] = value
	}
	retry := model.TransportRetryPolicy{MaxRetries: model.DefaultProviderRetryMaxRetries}
	if cfg.Retry != nil {
		retry = *cfg.Retry
	}
	return &Provider{
		apiKey:     cfg.APIKey,
		baseURL:    baseURL,
		apiVersion: apiVersion,
		headers:    headers,
		client:     client,
		models:     builtinModels(),
		retry:      retry,
	}
}

// ID implements model.Provider.
func (p *Provider) ID() string { return "anthropic" }

// Models implements model.Provider. The slice is owned by the caller.
func (p *Provider) Models() []model.Model {
	out := make([]model.Model, len(p.models))
	for i, m := range p.models {
		out[i] = m.Clone()
		out[i].BaseURL = p.baseURL
	}
	return out
}

// endpoint returns the Messages API URL.
func (p *Provider) endpoint() string { return p.baseURL + "/v1/messages" }

// requestHeaders merges the provider defaults, Config.Headers and the
// per-request headers. Request headers win.
func (p *Provider) requestHeaders(req *model.Request) map[string]string {
	headers := make(map[string]string, len(p.headers)+4)
	headers["Content-Type"] = "application/json"
	headers["Accept"] = "text/event-stream"
	headers["anthropic-version"] = p.apiVersion
	if p.apiKey != "" {
		headers["x-api-key"] = p.apiKey
	}
	for name, value := range p.headers {
		headers[name] = value
	}
	if req != nil {
		for name, value := range req.Headers {
			headers[name] = value
		}
	}
	return headers
}

// Stream implements model.Provider. It returns as soon as the request is
// scheduled; transport failures, HTTP errors and mid-stream errors all surface
// as a start event followed by an error event, matching the design contract
// that a returned stream never "throws".
//
// Only a malformed request (nil Request/Model or an unmarshalable tool schema)
// is returned as a synchronous error.
func (p *Provider) Stream(ctx context.Context, req *model.Request) (model.Stream, error) {
	if req == nil {
		return nil, errors.New("anthropic: request is required")
	}
	if req.Model == nil {
		return nil, errors.New("anthropic: request model is required")
	}
	body, err := p.buildRequestBody(req)
	if err != nil {
		return nil, err
	}

	streamCtx, cancel := context.WithCancel(ctx)
	s := newStream(streamCtx, cancel, p, req, body)
	go s.run()
	return s, nil
}

// thinkingBudget maps a provider-neutral level onto Anthropic's
// thinking.budget_tokens. The values are conservative defaults; the caller can
// override them through the catalog's ThinkingLevels when needed.
func thinkingBudget(level model.ThinkingLevel) int {
	switch level {
	case model.ThinkingMinimal:
		return 1024
	case model.ThinkingLow:
		return 2048
	case model.ThinkingMedium:
		return 8192
	case model.ThinkingHigh:
		return 16384
	case model.ThinkingXHigh:
		return 24576
	case model.ThinkingMax:
		return 32000
	default:
		return 0
	}
}

// builtinModels is the static catalog for the Anthropic provider.
//
// Prices are USD per million tokens from Anthropic's published pricing and are
// used only to estimate cost locally (Anthropic does not report a billed
// amount). Context windows are the standard 200K; the 1M-context beta is not
// requested automatically. Max output sizes are the published limits. When in
// doubt these values are rounded down to avoid over-promising.
func builtinModels() []model.Model {
	thinking := map[model.ThinkingLevel]any{
		model.ThinkingMinimal: 1024,
		model.ThinkingLow:     2048,
		model.ThinkingMedium:  8192,
		model.ThinkingHigh:    16384,
		model.ThinkingXHigh:   24576,
		model.ThinkingMax:     32000,
	}
	cache := &model.PromptCacheSpec{ShortTTLSeconds: 300, LongTTLSeconds: 3600}

	return []model.Model{
		{
			ID:             "claude-sonnet-4-5",
			Name:           "Claude Sonnet 4.5",
			Provider:       "anthropic",
			API:            model.APIAnthropicMessages,
			BaseURL:        DefaultBaseURL,
			Input:          []model.Modality{model.ModalityText, model.ModalityImage},
			ContextWindow:  200_000,
			MaxTokens:      64_000,
			Reasoning:      true,
			ThinkingLevels: cloneThinking(thinking),
			PromptCache:    clonePromptCache(cache),
			Cost: model.Cost{
				InputPerMillion:      3,
				OutputPerMillion:     15,
				CacheReadPerMillion:  0.3,
				CacheWritePerMillion: 3.75,
			},
		},
		{
			ID:             "claude-opus-4-1",
			Name:           "Claude Opus 4.1",
			Provider:       "anthropic",
			API:            model.APIAnthropicMessages,
			BaseURL:        DefaultBaseURL,
			Input:          []model.Modality{model.ModalityText, model.ModalityImage},
			ContextWindow:  200_000,
			MaxTokens:      32_000,
			Reasoning:      true,
			ThinkingLevels: cloneThinking(thinking),
			PromptCache:    clonePromptCache(cache),
			Cost: model.Cost{
				InputPerMillion:      15,
				OutputPerMillion:     75,
				CacheReadPerMillion:  1.5,
				CacheWritePerMillion: 18.75,
			},
		},
		{
			ID:             "claude-haiku-4-5",
			Name:           "Claude Haiku 4.5",
			Provider:       "anthropic",
			API:            model.APIAnthropicMessages,
			BaseURL:        DefaultBaseURL,
			Input:          []model.Modality{model.ModalityText, model.ModalityImage},
			ContextWindow:  200_000,
			MaxTokens:      64_000,
			Reasoning:      true,
			ThinkingLevels: cloneThinking(thinking),
			PromptCache:    clonePromptCache(cache),
			Cost: model.Cost{
				InputPerMillion:      1,
				OutputPerMillion:     5,
				CacheReadPerMillion:  0.1,
				CacheWritePerMillion: 1.25,
			},
		},
	}
}

func cloneThinking(in map[model.ThinkingLevel]any) map[model.ThinkingLevel]any {
	out := make(map[model.ThinkingLevel]any, len(in))
	for level, budget := range in {
		out[level] = budget
	}
	return out
}

func clonePromptCache(in *model.PromptCacheSpec) *model.PromptCacheSpec {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

// modelErrorf formats a provider-scoped error.
func modelErrorf(format string, args ...any) error {
	return fmt.Errorf("anthropic: "+format, args...)
}
