package app

import (
	"github.com/xichan96/cortex/agent/llm"
	"github.com/xichan96/cortex/agent/types"
)

func (a *agent) setupLLM() types.LLMProvider {
	llmCfg := a.config.LLM

	switch llmCfg.Provider {
	case "openai":
		return a.initOpenAI()
	case "deepseek":
		return a.initDeepSeek()
	case "volce":
		return a.initVolce()
	default:
		return nil
	}
}

func (a *agent) initOpenAI() types.LLMProvider {
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
		return nil
	}
	return provider
}

func (a *agent) initDeepSeek() types.LLMProvider {
	cfg := a.config.LLM.DeepSeek
	opts := llm.DeepSeekOptions{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
	}

	provider, err := llm.NewDeepSeekClient(opts)
	if err != nil {
		return nil
	}
	return provider
}

func (a *agent) initVolce() types.LLMProvider {
	cfg := a.config.LLM.Volce
	opts := llm.VolceOptions{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
	}

	provider, err := llm.NewVolceClient(opts)
	if err != nil {
		return nil
	}
	return provider
}
