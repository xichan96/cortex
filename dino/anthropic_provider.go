package dino

import (
	"fmt"
	"strings"

	"github.com/xichan96/cortex/agent/model"
	"github.com/xichan96/cortex/agent/model/providers/anthropic"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

// AnthropicModelProviderType is the dino provider type that serves Anthropic
// through the new agent/model layer (model.Provider + EngineProvider) instead
// of the legacy agent/llm client. It is deliberately distinct from the
// existing "anthropic" type so hosts can opt in without changing behavior.
const AnthropicModelProviderType = "anthropic-model"

func init() {
	RegisterLLMProvider(AnthropicModelProviderType, newAnthropicModelProvider)
}

// newAnthropicModelProvider adapts anthropic.Provider to types.LLMProvider.
//
// It mirrors the legacy "anthropic" factory's credential contract: an empty
// API key is rejected with errors.EC_LLM_API_KEY_REQUIRED (environment lookup
// stays in the host/CLI layer). BaseURL and Headers come straight from
// cfg.Provider so gateways and custom auth headers keep working.
func newAnthropicModelProvider(cfg *Config) (types.LLMProvider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("anthropic-model: config is required")
	}
	apiKey := strings.TrimSpace(cfg.Provider.APIKey)
	if apiKey == "" {
		return nil, errors.EC_LLM_API_KEY_REQUIRED
	}

	provider := anthropic.New(anthropic.Config{
		APIKey:  apiKey,
		BaseURL: cfg.Provider.BaseURL,
		Headers: cfg.Provider.Headers,
	})

	m, err := anthropicModelFor(provider, cfg.DefaultModel)
	if err != nil {
		return nil, err
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

// anthropicModelFor picks the catalog entry named by defaultModel. An empty
// name falls back to the first catalog model. An unknown name is passed
// through as a minimal model descriptor so newly released model IDs work
// without a cortex update; the Messages API only needs the ID.
func anthropicModelFor(provider *anthropic.Provider, defaultModel string) (*model.Model, error) {
	models := provider.Models()
	name := strings.TrimSpace(defaultModel)
	if name == "" {
		if len(models) == 0 {
			return nil, fmt.Errorf("anthropic-model: provider %q has no models", provider.ID())
		}
		m := models[0]
		return &m, nil
	}
	for i := range models {
		if models[i].ID == name {
			m := models[i]
			return &m, nil
		}
	}

	// Models() reports the provider's effective base URL; reuse it for the
	// pass-through descriptor. It is metadata only — dispatch still goes
	// through the provider.
	baseURL := ""
	if len(models) > 0 {
		baseURL = models[0].BaseURL
	}
	return &model.Model{
		ID:       name,
		Name:     name,
		Provider: provider.ID(),
		API:      model.APIAnthropicMessages,
		BaseURL:  baseURL,
		Input:    []model.Modality{model.ModalityText, model.ModalityImage},
	}, nil
}
