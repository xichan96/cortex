package web

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

const (
	WebFetchToolName = "web_fetch"
	maxResponseSize  = 5 * 1024 * 1024 // 5MB
	defaultTimeout   = 30 * time.Second
	maxTimeout       = 120 * time.Second
)

type WebFetchTool struct {
	client *http.Client
}

func NewWebFetchTool() types.Tool {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 10
	transport.IdleConnTimeout = 90 * time.Second
	client := &http.Client{
		Timeout:   defaultTimeout,
		Transport: transport,
	}
	return &WebFetchTool{client: client}
}

func (t *WebFetchTool) Name() string {
	return WebFetchToolName
}

//go:embed webfetch.txt
var webFetchDescription string

func (t *WebFetchTool) Description() string {
	return webFetchDescription
}

func (t *WebFetchTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"url": map[string]interface{}{
				"type":        "string",
				"description": "The URL to fetch content from",
			},
			"format": map[string]interface{}{
				"type":        "string",
				"description": "The format to return the content in (text, markdown, or html). Defaults to markdown.",
				"enum":        []string{"text", "markdown", "html"},
				"default":     "markdown",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Optional timeout in seconds (max 120)",
			},
		},
		"required": []string{"url"},
	}
}

func (t *WebFetchTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	url, ok := input["url"].(string)
	if !ok || url == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("url is required"))
	}

	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, errors.EC_PARAMETER_INVALID.Wrap(fmt.Errorf("URL must start with http:// or https://"))
	}

	format := "markdown"
	if f, ok := input["format"].(string); ok {
		format = f
	}

	timeout := defaultTimeout
	if v, ok := input["timeout"].(float64); ok && v > 0 {
		timeout = time.Duration(v) * time.Second
	}
	if v, ok := input["timeout"].(int); ok && v > 0 {
		timeout = time.Duration(v) * time.Second
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}

	acceptHeader := "*/*"
	switch format {
	case "markdown":
		acceptHeader = "text/markdown;q=1.0, text/x-markdown;q=0.9, text/plain;q=0.8, text/html;q=0.7, */*;q=0.1"
	case "text":
		acceptHeader = "text/plain;q=1.0, text/markdown;q=0.9, text/html;q=0.8, */*;q=0.1"
	case "html":
		acceptHeader = "text/html;q=1.0, application/xhtml+xml;q=0.9, text/plain;q=0.8, text/markdown;q=0.7, */*;q=0.1"
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36")
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("request failed with status code: %d", resp.StatusCode))
	}

	if contentLength := resp.Header.Get("content-length"); contentLength != "" {
		if cl, err := strconv.ParseInt(contentLength, 10, 64); err == nil {
			if cl > maxResponseSize {
				return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("response too large (exceeds 5MB limit)"))
			}
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}

	if len(body) > maxResponseSize {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("response too large (exceeds 5MB limit)"))
	}

	contentType := resp.Header.Get("content-type")
	mime := strings.Split(contentType, ";")[0]
	title := fmt.Sprintf("%s (%s)", url, contentType)

	if strings.HasPrefix(mime, "image/") && mime != "image/svg+xml" {
		base64Content := base64.StdEncoding.EncodeToString(body)
		return map[string]interface{}{
			"title":    title,
			"output":   "Image fetched successfully",
			"metadata": map[string]interface{}{},
			"attachments": []map[string]interface{}{
				{
					"type": "file",
					"mime": mime,
					"url":  fmt.Sprintf("data:%s;base64,%s", mime, base64Content),
				},
			},
		}, nil
	}

	content := string(body)

	switch format {
	case "markdown":
		if strings.Contains(contentType, "text/html") {
			markdown, err := convertHTMLToMarkdown(content)
			if err != nil {
				return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
			}
			return map[string]interface{}{
				"output":   markdown,
				"title":    title,
				"metadata": map[string]interface{}{},
			}, nil
		}
		return map[string]interface{}{
			"output":   content,
			"title":    title,
			"metadata": map[string]interface{}{},
		}, nil

	case "text":
		if strings.Contains(contentType, "text/html") {
			text, err := extractTextFromHTML(content)
			if err != nil {
				return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
			}
			return map[string]interface{}{
				"output":   text,
				"title":    title,
				"metadata": map[string]interface{}{},
			}, nil
		}
		return map[string]interface{}{
			"output":   content,
			"title":    title,
			"metadata": map[string]interface{}{},
		}, nil

	case "html":
		return map[string]interface{}{
			"output":   content,
			"title":    title,
			"metadata": map[string]interface{}{},
		}, nil

	default:
		return map[string]interface{}{
			"output":   content,
			"title":    title,
			"metadata": map[string]interface{}{},
		}, nil
	}
}

func extractTextFromHTML(htmlContent string) (string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
	if err != nil {
		return "", err
	}
	doc.Find("script, style, noscript, iframe, object, embed").Remove()
	return doc.Text(), nil
}

func convertHTMLToMarkdown(htmlContent string) (string, error) {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			html.WithHardWraps(),
		),
	)
	var buf bytes.Buffer
	if err := md.Convert([]byte(htmlContent), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (t *WebFetchTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "web_fetch",
		IsFromToolkit:  false,
		ToolType:       "web",
	}
}
