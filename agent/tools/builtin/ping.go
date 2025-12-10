package builtin

import (
	"fmt"
	"net"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type PingTool struct{}

func NewPingTool() types.Tool {
	return &PingTool{}
}

func (t *PingTool) Name() string {
	return "net_check"
}

func (t *PingTool) Description() string {
	return "Check network connectivity to a remote host and port using TCP connection."
}

func (t *PingTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"address": map[string]interface{}{
				"type":        "string",
				"description": "Target address in format 'host:port' (e.g., 'example.com:80' or '192.168.1.1:22')",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Connection timeout in seconds (default: 5)",
			},
		},
		"required": []string{"address"},
	}
}

func (t *PingTool) Execute(input map[string]interface{}) (interface{}, error) {
	address, ok := input["address"].(string)
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'address' parameter: must be a string"))
	}
	if address == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'address' parameter cannot be empty"))
	}

	timeout := 5 * time.Second
	if timeoutVal, ok := input["timeout"].(float64); ok {
		timeout = time.Duration(timeoutVal) * time.Second
	} else if timeoutVal, ok := input["timeout"].(int); ok {
		timeout = time.Duration(timeoutVal) * time.Second
	}

	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return map[string]interface{}{
			"connected": false,
			"address":   address,
			"error":     err.Error(),
		}, nil
	}
	defer conn.Close()

	return map[string]interface{}{
		"connected": true,
		"address":   address,
		"timeout":   timeout.Seconds(),
	}, nil
}

func (t *PingTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "net",
		IsFromToolkit:  false,
		ToolType:       "builtin",
	}
}
