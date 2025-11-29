package llm

import (
	"fmt"

	"github.com/tmc/langchaingo/llms/openai"
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/types"
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
		return nil, fmt.Errorf("API key is required")
	}

	if opts.Model == "" {
		opts.Model = DeepSeekChat
	}

	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.deepseek.com"
	}

	// Using OpenAI compatible mode since DeepSeek supports OpenAI API format
	client, err := openai.New(
		openai.WithToken(opts.APIKey),
		openai.WithBaseURL(opts.BaseURL),
		openai.WithModel(opts.Model),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create DeepSeek client: %w", err)
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
