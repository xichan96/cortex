package web

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

const (
	WebSearchToolName = "web_search"
	apiBaseURL        = "https://mcp.exa.ai"
	apiEndpoint       = "/mcp"
	defaultNumResults = 8
)

type McpSearchRequest struct {
	Jsonrpc string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  struct {
		Name      string `json:"name"`
		Arguments struct {
			Query                string `json:"query"`
			NumResults           int    `json:"numResults,omitempty"`
			Livecrawl            string `json:"livecrawl,omitempty"`
			Type                 string `json:"type,omitempty"`
			ContextMaxCharacters int    `json:"contextMaxCharacters,omitempty"`
		} `json:"arguments"`
	} `json:"params"`
}

type McpSearchResponse struct {
	Jsonrpc string `json:"jsonrpc"`
	Result  struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
}

type WebSearchTool struct {
	client *http.Client
}

func NewWebSearchTool() types.Tool {
	return &WebSearchTool{
		client: &http.Client{
			Timeout: 25 * time.Second,
		},
	}
}

func (t *WebSearchTool) Name() string {
	return WebSearchToolName
}

//go:embed websearch.txt
var webSearchDescriptionTemplate string

func (t *WebSearchTool) Description() string {
	return strings.ReplaceAll(webSearchDescriptionTemplate, "{{year}}", strconv.Itoa(time.Now().Year()))
}

func (t *WebSearchTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Websearch query",
			},
			"num_results": map[string]interface{}{
				"type":        "integer",
				"description": "Number of search results to return (default: 8)",
			},
			"livecrawl": map[string]interface{}{
				"type":        "string",
				"description": "Live crawl mode - 'fallback': use live crawling as backup if cached content unavailable, 'preferred': prioritize live crawling (default: 'fallback')",
				"enum":        []string{"fallback", "preferred"},
			},
			"type": map[string]interface{}{
				"type":        "string",
				"description": "Search type - 'auto': balanced search (default), 'fast': quick results, 'deep': comprehensive search",
				"enum":        []string{"auto", "fast", "deep"},
			},
			"context_max_characters": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum characters for context string optimized for LLMs (default: 10000)",
			},
		},
		"required": []string{"query"},
	}
}

func (t *WebSearchTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	query, ok := input["query"].(string)
	if !ok || query == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("query is required"))
	}

	searchRequest := McpSearchRequest{
		Jsonrpc: "2.0",
		ID:      1,
		Method:  "tools/call",
		Params: struct {
			Name      string `json:"name"`
			Arguments struct {
				Query                string `json:"query"`
				NumResults           int    `json:"numResults,omitempty"`
				Livecrawl            string `json:"livecrawl,omitempty"`
				Type                 string `json:"type,omitempty"`
				ContextMaxCharacters int    `json:"contextMaxCharacters,omitempty"`
			} `json:"arguments"`
		}{
			Name: "web_search_exa",
			Arguments: struct {
				Query                string `json:"query"`
				NumResults           int    `json:"numResults,omitempty"`
				Livecrawl            string `json:"livecrawl,omitempty"`
				Type                 string `json:"type,omitempty"`
				ContextMaxCharacters int    `json:"contextMaxCharacters,omitempty"`
			}{
				Query: query,
				Type:  "auto",
			},
		},
	}

	if numResults, ok := input["num_results"].(float64); ok {
		searchRequest.Params.Arguments.NumResults = int(numResults)
	} else {
		searchRequest.Params.Arguments.NumResults = defaultNumResults
	}

	if livecrawl, ok := input["livecrawl"].(string); ok {
		searchRequest.Params.Arguments.Livecrawl = livecrawl
	} else {
		searchRequest.Params.Arguments.Livecrawl = "fallback"
	}

	if contextMaxChars, ok := input["context_max_characters"].(float64); ok {
		searchRequest.Params.Arguments.ContextMaxCharacters = int(contextMaxChars)
	}

	requestBody, err := json.Marshal(searchRequest)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiBaseURL+apiEndpoint, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}

	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("search error (%d)", resp.StatusCode))
	}

	// Similar to the TS implementation, we'd need to handle SSE response here.
	// For simplicity, we'll assume a single JSON response for now.
	// In a real implementation, you would need a proper SSE parser.
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := line[6:]
			var mcpResponse McpSearchResponse
			if err := json.Unmarshal([]byte(data), &mcpResponse); err == nil {
				if len(mcpResponse.Result.Content) > 0 {
					return map[string]interface{}{
						"output":   mcpResponse.Result.Content[0].Text,
						"title":    fmt.Sprintf("Web search: %s", query),
						"metadata": map[string]interface{}{},
					}, nil
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}

	return map[string]interface{}{
		"output":   "No search results found. Please try a different query.",
		"title":    fmt.Sprintf("Web search: %s", query),
		"metadata": map[string]interface{}{},
	}, nil
}

func (t *WebSearchTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "web_search",
		IsFromToolkit:  false,
		ToolType:       "web",
	}
}
