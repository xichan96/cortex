package app

import (
	"context"
	"fmt"

	"github.com/xichan96/cortex/agent/llm"
	"github.com/xichan96/cortex/agent/types"
)

func (a *agent) setupLLM(ctx context.Context) (types.LLMProvider, error) {
	llmCfg := a.config.LLM

	switch llmCfg.Provider {
	case "openai":
		return a.initOpenAI(ctx)
	case "anthropic":
		return a.initAnthropic(ctx)
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", llmCfg.Provider)
	}
}

func (a *agent) initOpenAI(ctx context.Context) (types.LLMProvider, error) {
	cfg := a.config.LLM.OpenAI
	opts := llm.OpenAIOptions{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		OrgID:   cfg.OrgID,
		APIType: cfg.APIType,
	}

	provider, err := llm.NewOpenAIClient(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize OpenAI client: %w", err)
	}
	return provider, nil
}

func (a *agent) initAnthropic(ctx context.Context) (types.LLMProvider, error) {
	cfg := a.config.LLM.Anthropic
	opts := llm.AnthropicOptions{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
	}

	provider, err := llm.NewAnthropicClient(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Anthropic client: %w", err)
	}
	return provider, nil
}
