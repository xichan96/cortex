// Package openai implements the model.Provider contract for OpenAI's Chat
// Completions API (wire protocol "openai-completions").
//
// The provider is transport-neutral: it converts model.Request values into the
// JSON body OpenAI expects, performs one streaming HTTP call, and maps the SSE
// chunk sequence onto model.StreamEvent values. It deliberately does no
// semantic retry; callers layer model.TransportRetry / model.SemanticRetry on
// top when they want it.
//
// The same wire implementation is reused by OpenAI-compatible providers (see
// the deepseek package and NewCompatible).
package openai

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/xichan96/cortex/agent/model"
)

const (
	// DefaultBaseURL is the OpenAI API root. The provider appends
	// "/chat/completions" to it.
	DefaultBaseURL = "https://api.openai.com/v1"
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
	// APIKey is sent as "Authorization: Bearer <key>". When empty the header is
	// omitted so a gateway that authenticates another way still works.
	APIKey string
	// BaseURL is the API root, e.g. "https://api.openai.com/v1". It must not
	// include "/chat/completions". Empty means the provider's default.
	BaseURL string
	// Headers are added to every request. Config headers are applied after the
	// defaults, so they may override them.
	Headers map[string]string
	// HTTPClient is optional; nil means http.DefaultClient. A custom client can
	// set a proxy or transport, but should not set a total Timeout for streaming.
	HTTPClient *http.Client
}

// CompatibleOptions identifies an OpenAI-compatible endpoint that reuses this
// package's wire implementation. It is how sibling provider packages (for
// example deepseek) share the chat-completions stream decoder.
type CompatibleOptions struct {
	// ID is the provider identity returned by ID() and recorded on messages.
	// Empty means "openai".
	ID string
	// DefaultBaseURL is used when Config.BaseURL is empty. Empty means
	// DefaultBaseURL.
	DefaultBaseURL string
	// Models is the static catalog. Empty means the OpenAI catalog. Catalog
	// entries should already carry the right Provider and API values.
	Models []model.Model
	// SupportsReasoningEffort sends reasoning_effort for models whose
	// Reasoning flag is set. OpenAI understands the field; providers that
	// silently ignore it should leave this false.
	SupportsReasoningEffort bool
}

// Provider is the Chat Completions implementation of model.Provider. It is
// immutable after construction and safe for concurrent use.
type Provider struct {
	id              string
	apiKey          string
	baseURL         string
	headers         map[string]string
	client          *http.Client
	models          []model.Model
	reasoningEffort bool
}

// Compile-time contract check.
var _ model.Provider = (*Provider)(nil)

// New returns an OpenAI provider. Zero-value Config fields fall back to
// defaults.
func New(cfg Config) *Provider {
	return NewCompatible(CompatibleOptions{
		ID:                      "openai",
		DefaultBaseURL:          DefaultBaseURL,
		Models:                  builtinModels(),
		SupportsReasoningEffort: true,
	}, cfg)
}

// NewCompatible returns a provider for an OpenAI-compatible chat-completions
// endpoint under a different identity. BaseURL normalization (trailing slash
// removal, default fallback) and request/stream handling are identical to New.
func NewCompatible(opts CompatibleOptions, cfg Config) *Provider {
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		id = "openai"
	}
	defaultBaseURL := strings.TrimSpace(opts.DefaultBaseURL)
	if defaultBaseURL == "" {
		defaultBaseURL = DefaultBaseURL
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(defaultBaseURL, "/")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	headers := make(map[string]string, len(cfg.Headers))
	for name, value := range cfg.Headers {
		headers[name] = value
	}
	models := opts.Models
	if models == nil {
		models = builtinModels()
	}
	return &Provider{
		id:              id,
		apiKey:          cfg.APIKey,
		baseURL:         baseURL,
		headers:         headers,
		client:          client,
		models:          models,
		reasoningEffort: opts.SupportsReasoningEffort,
	}
}

// ID implements model.Provider.
func (p *Provider) ID() string { return p.id }

// Models implements model.Provider. The slice is owned by the caller and each
// entry's BaseURL reflects this provider's configured endpoint.
func (p *Provider) Models() []model.Model {
	out := make([]model.Model, len(p.models))
	for i, m := range p.models {
		out[i] = m.Clone()
		out[i].BaseURL = p.baseURL
	}
	return out
}

// endpoint returns the chat-completions URL.
func (p *Provider) endpoint() string { return p.baseURL + "/chat/completions" }

// requestHeaders merges the provider defaults, Config.Headers and the
// per-request headers. Request headers win.
func (p *Provider) requestHeaders(req *model.Request) map[string]string {
	headers := make(map[string]string, len(p.headers)+4)
	headers["Content-Type"] = "application/json"
	headers["Accept"] = "text/event-stream"
	if p.apiKey != "" {
		headers["Authorization"] = "Bearer " + p.apiKey
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
		return nil, errors.New("openai: request is required")
	}
	if req.Model == nil {
		return nil, errors.New("openai: request model is required")
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

// builtinModels is the static catalog for the OpenAI provider.
//
// Prices are USD per million tokens from OpenAI's published pricing and are
// used only to estimate cost locally. Context windows and max output sizes are
// rounded down where published limits are unusual, so the catalog never
// over-promises capacity.
func builtinModels() []model.Model {
	openaiThinking := map[model.ThinkingLevel]any{
		model.ThinkingMinimal: "low",
		model.ThinkingLow:     "low",
		model.ThinkingMedium:  "medium",
		model.ThinkingHigh:    "high",
		model.ThinkingXHigh:   "high",
		model.ThinkingMax:     "high",
	}
	return []model.Model{
		{
			ID:            "gpt-4o",
			Name:          "GPT-4o",
			Provider:      "openai",
			API:           model.APIOpenAICompletions,
			BaseURL:       DefaultBaseURL,
			Input:         []model.Modality{model.ModalityText, model.ModalityImage},
			ContextWindow: 128_000,
			MaxTokens:     16_384,
			Cost: model.Cost{
				InputPerMillion:     2.5,
				OutputPerMillion:    10,
				CacheReadPerMillion: 1.25,
			},
		},
		{
			ID:            "gpt-4o-mini",
			Name:          "GPT-4o mini",
			Provider:      "openai",
			API:           model.APIOpenAICompletions,
			BaseURL:       DefaultBaseURL,
			Input:         []model.Modality{model.ModalityText, model.ModalityImage},
			ContextWindow: 128_000,
			MaxTokens:     16_384,
			Cost: model.Cost{
				InputPerMillion:     0.15,
				OutputPerMillion:    0.6,
				CacheReadPerMillion: 0.075,
			},
		},
		{
			ID:            "gpt-4.1",
			Name:          "GPT-4.1",
			Provider:      "openai",
			API:           model.APIOpenAICompletions,
			BaseURL:       DefaultBaseURL,
			Input:         []model.Modality{model.ModalityText, model.ModalityImage},
			ContextWindow: 1_000_000,
			MaxTokens:     32_768,
			Cost: model.Cost{
				InputPerMillion:     2,
				OutputPerMillion:    8,
				CacheReadPerMillion: 0.5,
			},
		},
		{
			ID:            "gpt-4.1-mini",
			Name:          "GPT-4.1 mini",
			Provider:      "openai",
			API:           model.APIOpenAICompletions,
			BaseURL:       DefaultBaseURL,
			Input:         []model.Modality{model.ModalityText, model.ModalityImage},
			ContextWindow: 1_000_000,
			MaxTokens:     32_768,
			Cost: model.Cost{
				InputPerMillion:     0.4,
				OutputPerMillion:    1.6,
				CacheReadPerMillion: 0.1,
			},
		},
		{
			ID:             "o3",
			Name:           "o3",
			Provider:       "openai",
			API:            model.APIOpenAICompletions,
			BaseURL:        DefaultBaseURL,
			Input:          []model.Modality{model.ModalityText, model.ModalityImage},
			ContextWindow:  200_000,
			MaxTokens:      100_000,
			Reasoning:      true,
			ThinkingLevels: cloneThinking(openaiThinking),
			Cost: model.Cost{
				InputPerMillion:     2,
				OutputPerMillion:    8,
				CacheReadPerMillion: 0.5,
			},
		},
		{
			ID:             "o4-mini",
			Name:           "o4-mini",
			Provider:       "openai",
			API:            model.APIOpenAICompletions,
			BaseURL:        DefaultBaseURL,
			Input:          []model.Modality{model.ModalityText, model.ModalityImage},
			ContextWindow:  200_000,
			MaxTokens:      100_000,
			Reasoning:      true,
			ThinkingLevels: cloneThinking(openaiThinking),
			Cost: model.Cost{
				InputPerMillion:     1.1,
				OutputPerMillion:    4.4,
				CacheReadPerMillion: 0.275,
			},
		},
	}
}

func cloneThinking(in map[model.ThinkingLevel]any) map[model.ThinkingLevel]any {
	out := make(map[model.ThinkingLevel]any, len(in))
	for level, value := range in {
		out[level] = value
	}
	return out
}
