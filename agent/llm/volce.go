package llm

import (
	"github.com/tmc/langchaingo/llms/openai"
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

// VolceOptions Volce configuration options
type VolceOptions struct {
	APIKey  string
	BaseURL string
	Model   string
}

// NewVolceClient creates a new Volce client and returns LLMProvider
func NewVolceClient(opts VolceOptions) (types.LLMProvider, error) {
	if opts.APIKey == "" {
		return nil, errors.EC_LLM_API_KEY_REQUIRED
	}

	if opts.Model == "" {
		opts.Model = DoubaoSeed1.String()
	}

	if opts.BaseURL == "" {
		opts.BaseURL = "https://ark.cn-beijing.volces.com/api/v3"
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

	return providers.NewLangChainLLMProvider(client, opts.Model), nil
}

// VolceModel Volce model constants
type VolceModel string

const (
	DoubaoSeed1    VolceModel = "doubao-seed-1-6-251015"
	DoubaoSeedream VolceModel = "doubao-seedream-4-5-251128"
	DeepSeekV32    VolceModel = "deepseek-v3-2-251201"
	KimiK2         VolceModel = "kimi-k2-250905"
)

// String returns the model name as a string
func (m VolceModel) String() string {
	return string(m)
}

// DefaultVolceOptions
func DefaultVolceOptions() VolceOptions {
	return VolceOptions{
		BaseURL: "https://ark.cn-beijing.volces.com/api/v3",
		Model:   DoubaoSeed1.String(),
	}
}

// VolceClient
func VolceClient(apiKey, model string) (types.LLMProvider, error) {
	if model == "" {
		model = DoubaoSeed1.String()
	}
	opts := VolceOptions{
		APIKey: apiKey,
		Model:  model,
	}
	return NewVolceClient(opts)
}

// VolceClientWithBaseURL quickly creates a Volce client with a custom BaseURL
func VolceClientWithBaseURL(apiKey, baseURL, model string) (types.LLMProvider, error) {
	if model == "" {
		model = DoubaoSeed1.String()
	}

	opts := VolceOptions{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	}

	return NewVolceClient(opts)
}
