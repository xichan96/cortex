package builtin

import (
	"fmt"
	"time"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
	"github.com/xichan96/cortex/pkg/ssh"
)

type SSHTool struct{}

func NewSSHTool() types.Tool {
	return &SSHTool{}
}

func (t *SSHTool) Name() string {
	return "ssh"
}

func (t *SSHTool) Description() string {
	return "Execute commands on a remote server via SSH. Supports password, private key, and SSH agent authentication."
}

func (t *SSHTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"username": map[string]interface{}{
				"type":        "string",
				"description": "SSH username",
			},
			"password": map[string]interface{}{
				"type":        "string",
				"description": "SSH password (optional if private_key or agent_socket is provided)",
			},
			"address": map[string]interface{}{
				"type":        "string",
				"description": "SSH server address (hostname or IP)",
			},
			"port": map[string]interface{}{
				"type":        "integer",
				"description": "SSH server port (default: 22)",
			},
			"private_key": map[string]interface{}{
				"type":        "string",
				"description": "SSH private key content (optional if password or agent_socket is provided)",
			},
			"agent_socket": map[string]interface{}{
				"type":        "string",
				"description": "SSH agent socket path or env variable (e.g., 'env:SSH_AUTH_SOCK') (optional)",
			},
			"timeout": map[string]interface{}{
				"type":        "integer",
				"description": "Connection timeout in seconds (default: 15)",
			},
			"bastion": map[string]interface{}{
				"type":        "string",
				"description": "Bastion host address for jump server (optional)",
			},
			"bastion_port": map[string]interface{}{
				"type":        "integer",
				"description": "Bastion host port (default: 22)",
			},
			"bastion_user": map[string]interface{}{
				"type":        "string",
				"description": "Bastion host username (default: same as username)",
			},
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Command to execute on the remote server",
			},
		},
		"required": []string{"username", "address", "command"},
	}
}

func (t *SSHTool) Execute(input map[string]interface{}) (interface{}, error) {
	username, ok := input["username"].(string)
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'username' parameter: must be a string"))
	}
	if username == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'username' parameter cannot be empty"))
	}

	address, ok := input["address"].(string)
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'address' parameter: must be a string"))
	}
	if address == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'address' parameter cannot be empty"))
	}

	command, ok := input["command"].(string)
	if !ok {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(fmt.Errorf("invalid 'command' parameter: must be a string"))
	}
	if command == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("'command' parameter cannot be empty"))
	}

	cfg := ssh.Cfg{
		Username: username,
		Address:  address,
	}

	if password, ok := input["password"].(string); ok && password != "" {
		cfg.Password = password
	}

	if privateKey, ok := input["private_key"].(string); ok && privateKey != "" {
		cfg.PrivateKey = privateKey
	}

	if agentSocket, ok := input["agent_socket"].(string); ok && agentSocket != "" {
		cfg.AgentSocket = agentSocket
	}

	if port, ok := input["port"].(float64); ok {
		cfg.Port = int(port)
	} else if port, ok := input["port"].(int); ok {
		cfg.Port = port
	}

	if timeout, ok := input["timeout"].(float64); ok {
		cfg.Timeout = time.Duration(timeout) * time.Second
	} else if timeout, ok := input["timeout"].(int); ok {
		cfg.Timeout = time.Duration(timeout) * time.Second
	}

	if bastion, ok := input["bastion"].(string); ok && bastion != "" {
		cfg.Bastion = bastion
	}

	if bastionPort, ok := input["bastion_port"].(float64); ok {
		cfg.BastionPort = int(bastionPort)
	} else if bastionPort, ok := input["bastion_port"].(int); ok {
		cfg.BastionPort = bastionPort
	}

	if bastionUser, ok := input["bastion_user"].(string); ok && bastionUser != "" {
		cfg.BastionUser = bastionUser
	}

	conn, err := ssh.NewConnection(cfg)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to establish SSH connection: %w", err))
	}
	defer conn.Close()

	stdout, err := conn.Exec(command)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("failed to execute command: %w", err))
	}

	return map[string]interface{}{
		"output":  stdout,
		"command": command,
	}, nil
}

func (t *SSHTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "ssh",
		IsFromToolkit:  false,
		ToolType:       "builtin",
	}
}
