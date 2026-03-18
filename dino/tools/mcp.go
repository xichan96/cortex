package tools

import (
	"context"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/mcp"
)

// NewMCPTools creates an MCP client and returns the tools provided by the server.
// The caller is responsible for closing the client when it's no longer needed.
func NewMCPTools(ctx context.Context, url, transport string, headers map[string]string) (*mcp.Client, []types.Tool, error) {
	// Create MCP client
	client := mcp.NewClient(url, transport, headers)

	// Connect to MCP server
	if err := client.Connect(ctx); err != nil {
		return nil, nil, fmt.Errorf("failed to connect to MCP server: %w", err)
	}

	// Get tools from the server
	tools := client.GetTools()

	return client, tools, nil
}
