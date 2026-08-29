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
	client  *http.Client
	baseURL string // 可注入（测试用 httptest mock SSE）；空 = apiBaseURL
}

func NewWebSearchTool() types.Tool {
	return &WebSearchTool{
		client:  &http.Client{Timeout: 25 * time.Second},
		baseURL: apiBaseURL,
	}
}

// NewWebSearchToolWithBaseURL returns a web_search tool that talks to the given
// base URL (used by tests to mock the SSE stream).
func NewWebSearchToolWithBaseURL(baseURL string) types.Tool {
	return &WebSearchTool{
		client:  &http.Client{Timeout: 25 * time.Second},
		baseURL: baseURL,
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

	req, err := http.NewRequestWithContext(ctx, "POST", t.baseURL+apiEndpoint, bytes.NewBuffer(requestBody))
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

	// SSE 完整解析：exa MCP 的搜索结果可能横跨多条 `data:` 块（评估报告 11.1
	// 点名的脆弱点 —— 旧实现只读第一个可解析的 data 行就 return，丢后续结果）。
	// 这里收集全部 data: 块，逐块尝试解析为 McpSearchResponse，并把所有
	// content[].text 拼接起来（每条 data 可能是完整结果，也可能是片段）。
	//
	// 兼容性：只认 `data: ` 前缀行（非标准 SSE / 其他前缀行忽略），无 content
	// 的结果视为「无匹配」，返回空结果而非错误。
	var outputParts []string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]
		if data == "" {
			continue
		}
		var mcpResponse McpSearchResponse
		if err := json.Unmarshal([]byte(data), &mcpResponse); err != nil {
			// 单块解析失败：继续扫其余块，避免一个坏块丢掉整批结果。
			continue
		}
		for _, c := range mcpResponse.Result.Content {
			if strings.TrimSpace(c.Text) != "" {
				outputParts = append(outputParts, c.Text)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}

	if len(outputParts) == 0 {
		return map[string]interface{}{
			"output":   "No search results found. Please try a different query.",
			"title":    fmt.Sprintf("Web search: %s", query),
			"metadata": map[string]interface{}{},
		}, nil
	}

	return map[string]interface{}{
		"output":   strings.Join(outputParts, "\n\n"),
		"title":    fmt.Sprintf("Web search: %s", query),
		"metadata": map[string]interface{}{"result_count": len(outputParts)},
	}, nil
}

func (t *WebSearchTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "web_search",
		IsFromToolkit:  false,
		ToolType:       "web",
	}
}
