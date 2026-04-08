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
		opts.Model = ClaudeSonnet4.String()
	}

	// Use native Anthropic client (/v1/messages) to avoid langchaingo's
	// bufio.Scanner 64KB line limit with OpenAI-compat proxies.
	return newNativeAnthropicProvider(opts.APIKey, opts.BaseURL, opts.Model), nil
}

type AnthropicModel string

const (
	ClaudeOpus4     AnthropicModel = "claude-opus-4-20250514"
	ClaudeSonnet4   AnthropicModel = "claude-sonnet-4-20250514"
	ClaudeHaiku4    AnthropicModel = "claude-haiku-4-20250514"
	Claude35Sonnet  AnthropicModel = "claude-3-5-sonnet-20241022"
	Claude35Haiku   AnthropicModel = "claude-3-5-haiku-20241022"
)

func (m AnthropicModel) String() string {
	return string(m)
}

func DefaultAnthropicOptions() AnthropicOptions {
	return AnthropicOptions{
		BaseURL: "https://api.anthropic.com/v1",
		Model:   ClaudeSonnet4.String(),
	}
}

func AnthropicClient(apiKey, model string) (types.LLMProvider, error) {
	if model == "" {
		model = ClaudeSonnet4.String()
	}
	opts := AnthropicOptions{
		APIKey: apiKey,
		Model:  model,
	}
	return NewAnthropicClient(opts)
}

func AnthropicClientWithBaseURL(apiKey, baseURL, model string) (types.LLMProvider, error) {
	if model == "" {
		model = ClaudeSonnet4.String()
	}
	opts := AnthropicOptions{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	}
	return NewAnthropicClient(opts)
}
