package dino

import (
	"fmt"
	"strings"

	"github.com/xichan96/cortex/agent/model"
	"github.com/xichan96/cortex/agent/model/providers/anthropic"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

// AnthropicProviderType is the dino provider type that serves the Anthropic
// Messages protocol through the agent/model layer.
const AnthropicProviderType = "anthropic"

func init() {
	RegisterLLMProvider(AnthropicProviderType, newAnthropicModelProvider)
}

// newAnthropicModelProvider adapts anthropic.Provider to types.LLMProvider.
//
// It mirrors the OpenAI-compatible factory's credential contract: an empty API
// key is rejected with errors.EC_LLM_API_KEY_REQUIRED (environment lookup stays
// in the host/CLI layer). BaseURL and Headers come straight from cfg.Provider so
// gateways and custom auth headers keep working.
func newAnthropicModelProvider(cfg *Config) (types.LLMProvider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("anthropic: config is required")
	}
	apiKey := strings.TrimSpace(cfg.Provider.APIKey)
	if apiKey == "" {
		return nil, errors.EC_LLM_API_KEY_REQUIRED
	}

	provider := anthropic.New(anthropic.Config{
		APIKey:  apiKey,
		BaseURL: cfg.Provider.BaseURL,
		Headers: cfg.Provider.Headers,
		Retry:   providerRetry(cfg),
	})

	m := model.ModelFor(provider, cfg.DefaultModel, model.APIAnthropicMessages, []model.Modality{
		model.ModalityText, model.ModalityImage,
	})
	if m == nil {
		return nil, fmt.Errorf("anthropic: no model configured")
	}

	opts := model.EngineOptions{MaxTokens: cfg.MaxTokens}
	// The engine only applies a positive temperature (agent_execution.go), so
	// 0/negative means "unset" and must not be sent to Anthropic as explicit 0.
	if cfg.Temperature > 0 {
		temperature := float64(cfg.Temperature)
		opts.Temperature = &temperature
	}

	return model.NewEngineProvider(provider, m, opts)
}
