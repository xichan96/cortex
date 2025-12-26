package llm

import (
	"github.com/tmc/langchaingo/llms/openai"
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

// OpenAIOptions OpenAI configuration options
type OpenAIOptions struct {
	APIKey  string
	BaseURL string
	Model   string
	OrgID   string
	APIType string // "openai", "azure"
}

// NewOpenAIClient creates a new OpenAI client and returns LLMProvider
func NewOpenAIClient(opts OpenAIOptions) (types.LLMProvider, error) {
	if opts.APIKey == "" {
		return nil, errors.EC_LLM_API_KEY_REQUIRED
	}

	if opts.Model == "" {
		opts.Model = GPT4oMini.String()
	}

	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.openai.com"
	}

	pooledClient := providers.GetPooledHTTPClient()

	client, err := openai.New(
		openai.WithToken(opts.APIKey),
		openai.WithBaseURL(opts.BaseURL),
		openai.WithModel(opts.Model),
		openai.WithOrganization(opts.OrgID),
		openai.WithHTTPClient(pooledClient),
	)
	if err != nil {
		return nil, errors.NewError(errors.EC_LLM_CLIENT_CREATE_FAILED.Code, errors.EC_LLM_CLIENT_CREATE_FAILED.Message).Wrap(err)
	}

	// Directly return LLMProvider
	return providers.NewLangChainLLMProvider(client, opts.Model), nil
}

// OpenAIModel OpenAI model constants
type OpenAIModel string

const (
	// GPT-4 models
	GPT4      OpenAIModel = "gpt-4"
	GPT4Turbo OpenAIModel = "gpt-4-turbo"
	GPT4o     OpenAIModel = "gpt-4o"
	GPT4oMini OpenAIModel = "gpt-4o-mini"
	GPT41     OpenAIModel = "gpt-4.1"

	// GPT-3.5 models
	GPT35Turbo OpenAIModel = "gpt-3.5-turbo"

	// Other models
	TextDavinci003 OpenAIModel = "text-davinci-003"
	TextCurie001   OpenAIModel = "text-curie-001"
)

// String returns model name as string
func (m OpenAIModel) String() string {
	return string(m)
}

// DefaultOpenAIOptions default OpenAI configuration
func DefaultOpenAIOptions() OpenAIOptions {
	return OpenAIOptions{
		BaseURL: "https://api.openai.com",
		Model:   GPT4oMini.String(),
	}
}

// OpenAIClient quickly creates OpenAI client and returns LLMProvider
func OpenAIClient(apiKey, model string) (types.LLMProvider, error) {
	if model == "" {
		model = GPT4oMini.String()
	}
	opts := OpenAIOptions{
		APIKey: apiKey,
		Model:  model,
	}
	return NewOpenAIClient(opts)
}

// OpenAIClientWithBaseURL quickly creates OpenAI client with custom BaseURL
func OpenAIClientWithBaseURL(apiKey, baseURL, model string) (types.LLMProvider, error) {
	if model == "" {
		model = GPT4oMini.String()
	}

	opts := OpenAIOptions{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	}

	return NewOpenAIClient(opts)
}
