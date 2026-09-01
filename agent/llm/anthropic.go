package llm

import (
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type AnthropicOptions struct {
	APIKey  string
	BaseURL string
	Model   string
}

func NewAnthropicClient(opts AnthropicOptions) (types.LLMProvider, error) {
	if opts.APIKey == "" {
		return nil, errors.EC_LLM_API_KEY_REQUIRED
	}

	if opts.Model == "" {
		return nil, errors.EC_LLM_MODEL_REQUIRED
	}

	// Use native Anthropic client (/v1/messages) to avoid langchaingo's
	// bufio.Scanner 64KB line limit with OpenAI-compat proxies.
	return newNativeAnthropicProvider(opts.APIKey, opts.BaseURL, opts.Model), nil
}

func AnthropicClient(apiKey, model string) (types.LLMProvider, error) {
	opts := AnthropicOptions{
		APIKey: apiKey,
		Model:  model,
	}
	return NewAnthropicClient(opts)
}

func AnthropicClientWithBaseURL(apiKey, baseURL, model string) (types.LLMProvider, error) {
	opts := AnthropicOptions{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	}
	return NewAnthropicClient(opts)
}
