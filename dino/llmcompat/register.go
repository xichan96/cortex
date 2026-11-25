// Package llmcompat registers the langchaingo-based OpenAI-compatible LLM
// providers (openai/deepseek/volce). They are kept out of package dino so a
// host that only uses Anthropic does not link langchaingo and its transitive
// dependencies. Hosts that need these providers should blank-import this
// package:
//
//	import _ "github.com/xichan96/cortex/dino/llmcompat"
package llmcompat

import (
	lcllm "github.com/xichan96/cortex/agent/llm/langchain"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino"
)

func init() {
	dino.RegisterLLMProvider("openai", func(cfg *dino.Config) (types.LLMProvider, error) {
		return lcllm.NewOpenAIClient(lcllm.OpenAIOptions{
			APIKey:  cfg.Provider.APIKey,
			BaseURL: cfg.Provider.BaseURL,
			Model:   cfg.DefaultModel,
		})
	})
	dino.RegisterLLMProvider("deepseek", func(cfg *dino.Config) (types.LLMProvider, error) {
		return lcllm.NewDeepSeekClient(lcllm.DeepSeekOptions{
			APIKey: cfg.Provider.APIKey,
			Model:  cfg.DefaultModel,
		})
	})
	dino.RegisterLLMProvider("volce", func(cfg *dino.Config) (types.LLMProvider, error) {
		return lcllm.NewVolceClient(lcllm.VolceOptions{
			APIKey: cfg.Provider.APIKey,
			Model:  cfg.DefaultModel,
		})
	})
}
