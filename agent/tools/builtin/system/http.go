package system

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

type HTTPTool struct {
	client *http.Client
}

func NewHTTPTool() types.Tool {
	return &HTTPTool{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *HTTPTool) Name() string {
	return "http_request"
}

//go:embed http.txt
var httpDescription string

func (t *HTTPTool) Description() string {
	return httpDescription
}

func (t *HTTPTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "The URL to fetch",
			},
		},
		"required": []string{"url"},
	}
}

func (t *HTTPTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	urlVal, ok := input["url"].(string)
	if !ok || urlVal == "" {
		return nil, fmt.Errorf("invalid parameters: url is required")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", urlVal, nil)
	if err != nil {
		return nil, err
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return string(body), nil
}

func (t *HTTPTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "http",
		IsFromToolkit:  false,
		ToolType:       "system",
	}
}
