package llm

import (
	"github.com/tmc/langchaingo/llms/openai"
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

// DeepSeekOptions DeepSeek configuration options
type DeepSeekOptions struct {
	APIKey  string
	BaseURL string
	Model   string
}

// NewDeepSeekClient creates a new DeepSeek client and returns LLMProvider
func NewDeepSeekClient(opts DeepSeekOptions) (types.LLMProvider, error) {
	if opts.APIKey == "" {
		return nil, errors.EC_LLM_API_KEY_REQUIRED
	}

	if opts.Model == "" {
		opts.Model = DeepSeekChat
	}

	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.deepseek.com"
	}

	pooledClient := providers.GetPooledHTTPClient()

	client, err := openai.New(
		openai.WithToken(opts.APIKey),
		openai.WithBaseURL(opts.BaseURL),
		openai.WithModel(opts.Model),
		openai.WithHTTPClient(pooledClient),
	)
	if err != nil {
		return nil, errors.NewError(errors.EC_LLM_CLIENT_CREATE_FAILED.Code, errors.EC_LLM_CLIENT_CREATE_FAILED.Message).Wrap(err)
	}

	// Directly return LLMProvider
	return providers.NewLangChainLLMProvider(client, opts.Model), nil
}

// QuickDeepSeekProvider quickly creates a DeepSeek provider
func QuickDeepSeekProvider(apiKey, model string) (types.LLMProvider, error) {
	if model == "" {
		model = DeepSeekChat
	}
	opts := DeepSeekOptions{
		APIKey: apiKey,
		Model:  model,
	}
	return NewDeepSeekClient(opts)
}

// DeepSeekModel predefined DeepSeek model constants
const (
	DeepSeekChat   = "deepseek-chat"
	DeepSeekCoder  = "deepseek-coder"
	DeepSeekReason = "deepseek-reasoner"
)

// DefaultDeepSeekOptions default DeepSeek configuration
func DefaultDeepSeekOptions() DeepSeekOptions {
	return DeepSeekOptions{
		BaseURL: "https://api.deepseek.com",
		Model:   DeepSeekChat,
	}
}
