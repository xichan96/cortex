package tools

import (
	agenttools "github.com/xichan96/cortex/agent/tools"
)

type Registry = agenttools.Registry

func NewRegistry() *Registry {
	return agenttools.NewRegistry()
}

type ToolError string

func (e ToolError) Error() string {
	return string(e)
}

func ErrToolNotFound(name string) error {
	return ToolError("tool not found: " + name)
}

func ErrToolAlreadyRegistered(name string) error {
	return ToolError("tool already registered: " + name)
}
