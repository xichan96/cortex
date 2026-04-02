package llm

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/tmc/langchaingo/llms/openai"
	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

// CompatibilityHTTPClient handles API compatibility issues
type CompatibilityHTTPClient struct {
	client                   *http.Client
	preferMaxCompletionTokens bool
}

// Do implements the Doer interface
func (c *CompatibilityHTTPClient) Do(req *http.Request) (*http.Response, error) {
	// Only intercept chat completion requests
	// Relaxed check: POST method and body contains "model" and "messages"
	// This handles cases where the URL path might be rewritten or different (e.g. proxies)
	if req.Method == http.MethodPost {
		// Read body
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body.Close()

		// Check if it's a chat completion request (GPT-5, o1, o3)
		var bodyMap map[string]interface{}
		shouldResetBody := true

		if err := json.Unmarshal(bodyBytes, &bodyMap); err == nil {
			if _, hasMessages := bodyMap["messages"]; hasMessages {
				isReasoning := false
				if model, ok := bodyMap["model"].(string); ok {
					ml := strings.ToLower(strings.TrimSpace(model))
					isReasoning = strings.HasPrefix(ml, "gpt-5") ||
						strings.HasPrefix(ml, "o1") ||
						strings.HasPrefix(ml, "o3") ||
						strings.HasPrefix(ml, "o4")
				}
				if isReasoning || c.preferMaxCompletionTokens {
					changed := false
					if maxTokens, exists := bodyMap["max_tokens"]; exists {
						bodyMap["max_completion_tokens"] = maxTokens
						delete(bodyMap, "max_tokens")
						changed = true
					}
					if isReasoning {
						if _, exists := bodyMap["temperature"]; exists {
							delete(bodyMap, "temperature")
							changed = true
						}
					}
					if changed {
						newBodyBytes, err := json.Marshal(bodyMap)
						if err == nil {
							bodyBytes = newBodyBytes
							req.ContentLength = int64(len(bodyBytes))
							req.Header.Set("Content-Length", strconv.Itoa(len(bodyBytes)))
						}
					}
				}
			}
		}

		// Reset body
		if shouldResetBody {
			req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		}
	}

	return c.client.Do(req)
}

// OpenAIOptions OpenAI configuration options
type OpenAIOptions struct {
	APIKey  string
	BaseURL string
	Model   string
	OrgID   string
	APIType string // "openai", "azure"
	// PreferMaxCompletionTokens maps max_tokens -> max_completion_tokens for APIs that reject max_tokens.
	PreferMaxCompletionTokens bool
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
	compatClient := &CompatibilityHTTPClient{
		client:                    pooledClient,
		preferMaxCompletionTokens: opts.PreferMaxCompletionTokens,
	}

	client, err := openai.New(
		openai.WithToken(opts.APIKey),
		openai.WithBaseURL(opts.BaseURL),
		openai.WithModel(opts.Model),
		openai.WithOrganization(opts.OrgID),
		openai.WithHTTPClient(compatClient),
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
