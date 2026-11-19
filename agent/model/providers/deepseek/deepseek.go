// Package deepseek implements the model.Provider contract for DeepSeek's chat
// completions API ("openai-completions" wire protocol).
//
// DeepSeek speaks the OpenAI chat-completions protocol, so the implementation
// reuses the openai package's request encoder and SSE decoder. This package
// supplies the provider identity, default endpoint, static model catalog and
// DeepSeek-specific behavior: reasoning_content is mapped to thinking events,
// and options DeepSeek does not understand (for example reasoning_effort) are
// silently omitted.
package deepseek

import (
	"net/http"

	"github.com/xichan96/cortex/agent/model"
	"github.com/xichan96/cortex/agent/model/providers/openai"
)

const (
	// DefaultBaseURL is the public DeepSeek API root. The provider appends
	// "/chat/completions" to it.
	DefaultBaseURL = "https://api.deepseek.com"
)

// Config configures the DeepSeek provider.
type Config struct {
	// APIKey is sent as "Authorization: Bearer <key>".
	APIKey string
	// BaseURL is the API root. Empty means DefaultBaseURL.
	BaseURL string
	// Headers are added to every request and may override the defaults.
	Headers map[string]string
	// HTTPClient is optional; nil means http.DefaultClient.
	HTTPClient *http.Client
}

// Provider is the DeepSeek implementation of model.Provider.
type Provider = openai.Provider

// Compile-time contract check.
var _ model.Provider = (*Provider)(nil)

// New returns a DeepSeek provider. Zero-value Config fields fall back to
// defaults.
//
// DeepSeek ignores reasoning_effort, so the provider never sends it even when
// Request.Thinking is set; reasoning is controlled by the selected model
// (deepseek-reasoner) instead.
func New(cfg Config) *Provider {
	return openai.NewCompatible(openai.CompatibleOptions{
		ID:                      "deepseek",
		DefaultBaseURL:          DefaultBaseURL,
		Models:                  builtinModels(),
		SupportsReasoningEffort: false,
	}, openai.Config{
		APIKey:     cfg.APIKey,
		BaseURL:    cfg.BaseURL,
		Headers:    cfg.Headers,
		HTTPClient: cfg.HTTPClient,
	})
}

// builtinModels is the static catalog for the DeepSeek provider.
//
// Prices are USD per million tokens from DeepSeek's published pricing and are
// used only to estimate cost locally. Max output sizes are rounded down so the
// catalog never over-promises capacity.
func builtinModels() []model.Model {
	return []model.Model{
		{
			ID:            "deepseek-chat",
			Name:          "DeepSeek Chat",
			Provider:      "deepseek",
			API:           model.APIOpenAICompletions,
			BaseURL:       DefaultBaseURL,
			Input:         []model.Modality{model.ModalityText},
			ContextWindow: 128_000,
			MaxTokens:     8_192,
			Cost: model.Cost{
				InputPerMillion:     0.28,
				OutputPerMillion:    0.42,
				CacheReadPerMillion: 0.028,
			},
		},
		{
			ID:            "deepseek-reasoner",
			Name:          "DeepSeek Reasoner",
			Provider:      "deepseek",
			API:           model.APIOpenAICompletions,
			BaseURL:       DefaultBaseURL,
			Input:         []model.Modality{model.ModalityText},
			ContextWindow: 128_000,
			MaxTokens:     32_768,
			Reasoning:     true,
			Cost: model.Cost{
				InputPerMillion:     0.28,
				OutputPerMillion:    0.42,
				CacheReadPerMillion: 0.028,
			},
		},
	}
}
