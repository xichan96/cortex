package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/types"
)

// mcpContentFragment returns one SSE `data:` payload carrying a single content
// chunk of a McpSearchResponse.
func mcpContentFragment(id int, text string) string {
	return fmt.Sprintf(`data: {"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":%q}]}}`, id, text)
}

// sseStream builds a response body from the given fragments, joined with
// standard SSE event framing.
func sseStream(fragments ...string) string {
	var b strings.Builder
	for _, f := range fragments {
		b.WriteString("event: message\n")
		b.WriteString(f)
		b.WriteString("\n\n")
	}
	return b.String()
}

// newSearchServer returns an httptest server that serves the given SSE body on
// the /mcp endpoint.
func newSearchServer(body string, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != apiEndpoint {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestWebSearchSSE_MultipleDataBlocksMerged(t *testing.T) {
	// 三条 data: 块，各含一个片段 —— 旧实现只返回第一块的 content。
	body := sseStream(
		mcpContentFragment(1, "result one"),
		mcpContentFragment(2, "result two"),
		mcpContentFragment(3, "result three"),
	)
	srv := newSearchServer(body, http.StatusOK)
	defer srv.Close()

	tool := NewWebSearchToolWithBaseURL(srv.URL)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "golang sse"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", res)
	}
	out, _ := m["output"].(string)
	for _, want := range []string{"result one", "result two", "result three"} {
		if !strings.Contains(out, want) {
			t.Errorf("merged output missing %q; got:\n%s", want, out)
		}
	}
	if m["metadata"] == nil {
		t.Error("metadata must be present")
	}
}

func TestWebSearchSSE_InterleavedNonDataLinesIgnored(t *testing.T) {
	// `id:`/`retry:` 等非 data 行必须被忽略，只有 data: 参与解析。
	body := "id: 1\nretry: 100\n" +
		mcpContentFragment(1, "only result") + "\n\n"
	srv := newSearchServer(body, http.StatusOK)
	defer srv.Close()

	tool := NewWebSearchToolWithBaseURL(srv.URL)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "x"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, _ := res.(map[string]interface{})["output"].(string)
	if !strings.Contains(out, "only result") {
		t.Fatalf("data block must be parsed despite non-data lines, got %q", out)
	}
}

func TestWebSearchSSE_NoResults(t *testing.T) {
	// 无 data / data 无 content → 返回「无结果」提示，非错误。
	srv := newSearchServer(sseStream(), http.StatusOK)
	defer srv.Close()

	tool := NewWebSearchToolWithBaseURL(srv.URL)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "nothing"})
	if err != nil {
		t.Fatalf("no-results must not error: %v", err)
	}
	m := res.(map[string]interface{})
	if !strings.Contains(m["output"].(string), "No search results") {
		t.Fatalf("expected no-results message, got %q", m["output"])
	}
}

func TestWebSearchSSE_BadFragmentDoesNotDropOthers(t *testing.T) {
	// 中间一块解析失败不丢整批（继续扫其余块）。
	body := sseStream(
		mcpContentFragment(1, "first"),
		`data: {this is not json}`,
		mcpContentFragment(3, "third"),
	)
	srv := newSearchServer(body, http.StatusOK)
	defer srv.Close()

	tool := NewWebSearchToolWithBaseURL(srv.URL)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"query": "x"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, _ := res.(map[string]interface{})["output"].(string)
	if !strings.Contains(out, "first") || !strings.Contains(out, "third") {
		t.Fatalf("valid fragments must survive a bad one, got %q", out)
	}
}

func TestWebSearchSSE_HTTPErrorStatus(t *testing.T) {
	srv := newSearchServer("oops", http.StatusInternalServerError)
	defer srv.Close()

	tool := NewWebSearchToolWithBaseURL(srv.URL)
	if _, err := tool.Execute(context.Background(), map[string]interface{}{"query": "x"}); err == nil {
		t.Fatal("non-200 must return an error")
	}
}

var _ types.Tool = (*WebSearchTool)(nil)
