package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
)

// HTTPClient HTTP client
type HTTPClient struct {
	client    *fasthttp.Client
	baseURL   string
	authToken string
	headers   map[string]string
}

// NewHTTPClient creates a new HTTP client
func NewHTTPClient(baseURL, authToken string) *HTTPClient {
	return &HTTPClient{
		client: &fasthttp.Client{
			MaxConnsPerHost:     100,
			ReadTimeout:         30 * time.Second,
			WriteTimeout:        30 * time.Second,
			MaxIdleConnDuration: 60 * time.Second,
		},
		baseURL:   strings.TrimSuffix(baseURL, "/"),
		authToken: authToken,
		headers:   make(map[string]string),
	}
}

// SetHeader sets a custom header
func (c *HTTPClient) SetHeader(key, value string) {
	c.headers[key] = value
}

// Get sends a GET request
func (c *HTTPClient) Get(ctx context.Context, path string, params map[string]string) ([]byte, error) {
	url := c.baseURL + path

	if len(params) > 0 {
		query := make([]string, 0, len(params))
		for key, value := range params {
			query = append(query, fmt.Sprintf("%s=%s", key, value))
		}
		url += "?" + strings.Join(query, "&")
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(url)
	req.Header.SetMethod("GET")
	c.setFastHTTPHeaders(req)

	if err := c.client.Do(req, resp); err != nil {
		return nil, fmt.Errorf("GET request failed: %w", err)
	}

	return c.readFastHTTPResponse(resp)
}

// Post sends a POST request
func (c *HTTPClient) Post(ctx context.Context, path string, body interface{}) ([]byte, error) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(c.baseURL + path)
	req.Header.SetMethod("POST")
	c.setFastHTTPHeaders(req)

	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		req.SetBody(jsonBody)
		req.Header.SetContentType("application/json")
	}

	if err := c.client.Do(req, resp); err != nil {
		return nil, fmt.Errorf("POST request failed: %w", err)
	}

	return c.readFastHTTPResponse(resp)
}

// Put sends a PUT request
func (c *HTTPClient) Put(ctx context.Context, path string, body interface{}) ([]byte, error) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(c.baseURL + path)
	req.Header.SetMethod("PUT")
	c.setFastHTTPHeaders(req)

	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		req.SetBody(jsonBody)
		req.Header.SetContentType("application/json")
	}

	if err := c.client.Do(req, resp); err != nil {
		return nil, fmt.Errorf("PUT request failed: %w", err)
	}

	return c.readFastHTTPResponse(resp)
}

// Delete sends a DELETE request
func (c *HTTPClient) Delete(ctx context.Context, path string) error {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(c.baseURL + path)
	req.Header.SetMethod("DELETE")
	c.setFastHTTPHeaders(req)

	if err := c.client.Do(req, resp); err != nil {
		return fmt.Errorf("DELETE request failed: %w", err)
	}

	if resp.StatusCode() >= 400 {
		body := resp.Body()
		return fmt.Errorf("DELETE request failed with status %d: %s", resp.StatusCode(), string(body))
	}

	return nil
}

// setFastHTTPHeaders sets fasthttp request headers
func (c *HTTPClient) setFastHTTPHeaders(req *fasthttp.Request) {
	// Set auth header
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	// Set custom headers
	for key, value := range c.headers {
		req.Header.Set(key, value)
	}
}

// readFastHTTPResponse reads fasthttp response
func (c *HTTPClient) readFastHTTPResponse(resp *fasthttp.Response) ([]byte, error) {
	body := resp.Body()

	if resp.StatusCode() >= 400 {
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode(), string(body))
	}

	return body, nil
}
