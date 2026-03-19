package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/permission"
)

type Result struct {
	Output string
	Error  error
	Usage  types.Usage
}

type Request struct {
	AgentName string
	Prompt    string
	Input     string
	Files     []FileAttachment
}

type FileAttachment struct {
	Path    string
	Name    string
	Content []byte
}

type Subagent interface {
	Execute(ctx context.Context, req *Request) (*Result, error)
	Close()
}

type subagentImpl struct {
	info         *Info
	llmProvider  types.LLMProvider
	parentConfig *types.AgentConfig
	tools        []types.Tool
	mu           sync.RWMutex
	lastUsage    types.Usage
}

func NewSubagent(info *Info, llmProvider types.LLMProvider, tools []types.Tool) (Subagent, error) {
	if info.Mode != ModeSubagent {
		return nil, fmt.Errorf("agent %s is not a subagent (mode: %s)", info.Name, info.Mode)
	}

	parentConfig := types.NewAgentConfig()
	parentConfig.MaxIterations = 100
	parentConfig.Timeout = 5 * time.Minute

	return &subagentImpl{
		info:         info,
		llmProvider:  llmProvider,
		parentConfig: parentConfig,
		tools:        tools,
	}, nil
}

func (s *subagentImpl) Execute(ctx context.Context, req *Request) (*Result, error) {
	s.mu.RLock()
	cfg := s.buildConfig(req)
	filteredTools := s.filterTools(req.Prompt)
	llmProvider := s.llmProvider
	s.mu.RUnlock()

	eng := engine.NewAgentEngine(llmProvider, cfg)
	eng.AddTools(ctx, filteredTools)

	agentInput := types.NewAgentInput(req.Input)
	stream, err := eng.ExecuteStream(ctx, agentInput, nil)
	if err != nil {
		return nil, err
	}

	var output string
	var lastUsage types.Usage
	for result := range stream {
		if result.Error != nil {
			return &Result{Error: result.Error}, nil
		}
		switch result.Type {
		case "chunk":
			output += result.Content
		case "tool_call":
			if result.ToolEvent != nil {
			}
		}
		if result.Result != nil {
			output = result.Result.Output
			lastUsage = result.Result.Usage
		}
	}

	return &Result{
		Output: output,
		Usage:  lastUsage,
	}, nil
}

func (s *subagentImpl) buildConfig(req *Request) *types.AgentConfig {
	cfg := types.NewAgentConfig()

	if s.info.Prompt != "" {
		cfg.SystemMessage = s.info.Prompt
	}

	if s.info.Temperature != nil {
		cfg.Temperature = float32(*s.info.Temperature)
	}

	if s.info.TopP != nil {
		cfg.TopP = float32(*s.info.TopP)
	}

	cfg.MaxIterations = 50
	cfg.Timeout = 3 * time.Minute
	cfg.EnableMemoryCompress = false

	return cfg
}

func (s *subagentImpl) filterTools(prompt string) []types.Tool {
	evaluator := permission.NewEvaluator(s.info.Permission)
	var filtered []types.Tool

	for _, tool := range s.tools {
		action := evaluator.Evaluate(tool.Name(), nil)
		if action == permission.ActionAllow {
			filtered = append(filtered, tool)
		}
	}

	return filtered
}

func (s *subagentImpl) Close() {
}

type Manager struct {
	mu        sync.RWMutex
	subagents map[string]Subagent
	factory   Factory
}

type Factory interface {
	GetAgent(name string) (*Info, bool)
	GetLLMProvider() types.LLMProvider
	GetTools() []types.Tool
}

func NewManager(factory Factory) *Manager {
	return &Manager{
		subagents: make(map[string]Subagent),
		factory:   factory,
	}
}

func (m *Manager) GetSubagent(name string) (Subagent, error) {
	m.mu.RLock()
	if sa, ok := m.subagents[name]; ok {
		m.mu.RUnlock()
		return sa, nil
	}
	m.mu.RUnlock()

	info, ok := m.factory.GetAgent(name)
	if !ok {
		return nil, fmt.Errorf("agent not found: %s", name)
	}

	if info.Mode != ModeSubagent {
		return nil, fmt.Errorf("agent %s is not a subagent", name)
	}

	sa, err := NewSubagent(info, m.factory.GetLLMProvider(), m.factory.GetTools())
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.subagents[name] = sa
	m.mu.Unlock()

	return sa, nil
}

func (m *Manager) Execute(ctx context.Context, req *Request) (*Result, error) {
	sa, err := m.GetSubagent(req.AgentName)
	if err != nil {
		return nil, err
	}
	return sa.Execute(ctx, req)
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sa := range m.subagents {
		sa.Close()
	}
	m.subagents = make(map[string]Subagent)
}

func (m *Manager) CloseAgent(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sa, ok := m.subagents[name]; ok {
		sa.Close()
		delete(m.subagents, name)
	}
}
