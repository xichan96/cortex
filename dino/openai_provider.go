package dino

import (
	"fmt"
	"strings"

	"github.com/xichan96/cortex/agent/model"
	"github.com/xichan96/cortex/agent/model/providers/openai"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

// OpenAIProviderType is the dino provider type that serves the OpenAI chat
// completions protocol through the agent/model layer. Any OpenAI-compatible
// endpoint (DeepSeek, Volcengine, a local gateway) uses this type together
// with Provider.BaseURL and DefaultModel.
const OpenAIProviderType = "openai"

func init() {
	RegisterLLMProvider(OpenAIProviderType, newOpenAIModelProvider)
}

// newOpenAIModelProvider adapts openai.Provider to types.LLMProvider.
func newOpenAIModelProvider(cfg *Config) (types.LLMProvider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("openai: config is required")
	}
	apiKey := strings.TrimSpace(cfg.Provider.APIKey)
	if apiKey == "" {
		return nil, errors.EC_LLM_API_KEY_REQUIRED
	}

	provider := openai.New(openai.Config{
		APIKey:                    apiKey,
		BaseURL:                   cfg.Provider.BaseURL,
		Headers:                   cfg.Provider.Headers,
		PreferMaxCompletionTokens: cfg.Provider.PreferMaxCompletionTokens,
		Retry:                     providerRetry(cfg),
	})

	m := model.ModelFor(provider, cfg.DefaultModel, model.APIOpenAICompletions, []model.Modality{
		model.ModalityText, model.ModalityImage,
	})
	if m == nil {
		return nil, fmt.Errorf("openai: no model configured")
	}

	opts := model.EngineOptions{MaxTokens: cfg.MaxTokens}
	if cfg.Temperature > 0 {
		temperature := float64(cfg.Temperature)
		opts.Temperature = &temperature
	}

	return model.NewEngineProvider(provider, m, opts)
}
