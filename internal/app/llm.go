package app

import (
	"context"
	"fmt"

	lcllm "github.com/xichan96/cortex/agent/llm/langchain"
	"github.com/xichan96/cortex/agent/types"
)

func (a *agent) setupLLM(ctx context.Context) (types.LLMProvider, error) {
	llmCfg := a.config.LLM

	switch llmCfg.Provider {
	case "openai":
		return a.initOpenAI(ctx)
	case "deepseek":
		return a.initDeepSeek(ctx)
	case "volce":
		return a.initVolce(ctx)
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", llmCfg.Provider)
	}
}

func (a *agent) initOpenAI(ctx context.Context) (types.LLMProvider, error) {
	cfg := a.config.LLM.OpenAI
	opts := lcllm.OpenAIOptions{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		OrgID:   cfg.OrgID,
		APIType: cfg.APIType,
	}

	provider, err := lcllm.NewOpenAIClient(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize OpenAI client: %w", err)
	}
	return provider, nil
}

func (a *agent) initDeepSeek(ctx context.Context) (types.LLMProvider, error) {
	cfg := a.config.LLM.DeepSeek
	opts := lcllm.DeepSeekOptions{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
	}

	provider, err := lcllm.NewDeepSeekClient(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize DeepSeek client: %w", err)
	}
	return provider, nil
}

func (a *agent) initVolce(ctx context.Context) (types.LLMProvider, error) {
	cfg := a.config.LLM.Volce
	opts := lcllm.VolceOptions{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
	}

	provider, err := lcllm.NewVolceClient(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize Volce client: %w", err)
	}
	return provider, nil
}
