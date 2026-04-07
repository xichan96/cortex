package pkg

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/xichan96/cortex/pkg/mcp"
)

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
