package hostconfig

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/xichan96/cortex/pkg/mcp"
)

type MCPToolConfig struct {
	Name           string            `mapstructure:"name"`
	Disabled       bool              `mapstructure:"disabled"`
	URL            string            `mapstructure:"url"`
	Transport      string            `mapstructure:"transport"`
	Headers        map[string]string `mapstructure:"headers"`
	Tools          []string          `mapstructure:"tools"`
	AuthType       string            `mapstructure:"auth_type"`
	BearerToken    string            `mapstructure:"bearer_token"`
	AuthHeaderName string            `mapstructure:"auth_header_name"`
	AuthHeaderVal  string            `mapstructure:"auth_header_value"`
}

func NormalizeMCPTransport(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "httpStreamable"
	}
	low := strings.ToLower(s)
	switch low {
	case "http", "https":
		return "httpStreamable"
	case "httpstreamable", "streamable", "streamable-http":
		return "httpStreamable"
	case "sse":
		return "sse"
	default:
		return s
	}
}

func MCPHeadersFromTool(m MCPToolConfig) map[string]string {
	headers := make(map[string]string)
	for k, v := range m.Headers {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		headers[k] = strings.TrimSpace(v)
	}
	at := strings.ToLower(strings.TrimSpace(m.AuthType))
	if (at == "" || at == "bearer") && strings.TrimSpace(m.BearerToken) != "" {
		headers["Authorization"] = "Bearer " + strings.TrimSpace(m.BearerToken)
	}
	if hn := strings.TrimSpace(m.AuthHeaderName); hn != "" {
		headers[hn] = strings.TrimSpace(m.AuthHeaderVal)
	}
	return headers
}

func ListMCPToolsFromToolConfigs(ctx context.Context, entries []MCPToolConfig) []MCPCapabilityTool {
	srvs := make([]MCPServer, 0, len(entries))
	for _, m := range entries {
		srvs = append(srvs, MCPServer{
			Name:       m.Name,
			Disabled:   m.Disabled,
			URL:        m.URL,
			Transport:  NormalizeMCPTransport(m.Transport),
			Headers:    MCPHeadersFromTool(m),
			ToolsAllow: m.Tools,
		})
	}
	return ListMCPTools(ctx, srvs)
}

type MCPServer struct {
	Name       string
	URL        string
	Disabled   bool
	Transport  string
	Headers    map[string]string
	ToolsAllow []string
}

type MCPCapabilityTool struct {
	Server string
	ID     string
	Name   string
	Desc   string
}

func mcpToolAllowSet(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	m := make(map[string]struct{})
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" {
			m[n] = struct{}{}
		}
	}
	return m
}

func ListMCPTools(ctx context.Context, servers []MCPServer) []MCPCapabilityTool {
	if len(servers) == 0 {
		return nil
	}
	var out []MCPCapabilityTool
	for i, s := range servers {
		if s.Disabled {
			continue
		}
		u := strings.TrimSpace(s.URL)
		if u == "" {
			continue
		}
		srv := strings.TrimSpace(s.Name)
		if srv == "" {
			srv = "mcp_" + strconv.Itoa(i)
		}
		allow := mcpToolAllowSet(s.ToolsAllow)
		transport := strings.TrimSpace(s.Transport)
		if transport == "" {
			transport = "httpStreamable"
		}
		headers := s.Headers
		if headers == nil {
			headers = map[string]string{}
		}
		cli := mcp.NewClient(u, transport, headers)
		qctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		err := cli.Connect(qctx)
		cancel()
		if err != nil {
			continue
		}
		for _, t := range cli.GetTools() {
			n := strings.TrimSpace(t.Name())
			if n == "" {
				continue
			}
			if allow != nil {
				if _, ok := allow[n]; !ok {
					continue
				}
			}
			out = append(out, MCPCapabilityTool{
				Server: srv,
				ID:     srv + "/" + n,
				Name:   n,
				Desc:   strings.TrimSpace(t.Description()),
			})
		}
		_ = cli.Disconnect(context.Background())
	}
	return out
}
