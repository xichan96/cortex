package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/permission"
)

const (
	subagentMaxIterations   = 50
	subagentExecuteTimeout  = 3 * time.Minute
	subagentMaxFileBytes    = 32 << 10
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
	info               *Info
	llmProvider        types.LLMProvider
	tools              []types.Tool
	maxHistoryMessages int
}

func NewSubagent(info *Info, llmProvider types.LLMProvider, tools []types.Tool, maxHistoryMessages int) (Subagent, error) {
	if info.Mode != ModeSubagent {
		return nil, fmt.Errorf("agent %s is not a subagent (mode: %s)", info.Name, info.Mode)
	}
	mh := maxHistoryMessages
	if mh <= 0 {
		mh = 48
	}
	return &subagentImpl{
		info:               info,
		llmProvider:        llmProvider,
		tools:              tools,
		maxHistoryMessages: mh,
	}, nil
}

func delegateContext(req *Request) string {
	switch {
	case req.Prompt != "" && req.Input != "":
		return req.Prompt + "\n\n" + req.Input
	case req.Prompt != "":
		return req.Prompt
	default:
		return req.Input
	}
}

func buildAgentInput(req *Request) types.AgentInput {
	text := strings.TrimSpace(delegateContext(req))
	if len(req.Files) == 0 {
		return types.NewAgentInput(text)
	}
	parts := make([]types.MessagePart, 0, 1+len(req.Files))
	if text != "" {
		parts = append(parts, types.TextPart{Text: text})
	}
	for _, f := range req.Files {
		body := f.Content
		truncated := false
		if len(body) > subagentMaxFileBytes {
			body = body[:subagentMaxFileBytes]
			truncated = true
		}
		var b strings.Builder
		b.WriteString("--- file: ")
		b.WriteString(f.Name)
		b.WriteString(" (")
		b.WriteString(f.Path)
		b.WriteString(") ---\n")
		b.Write(body)
		if truncated {
			b.WriteString("\n…(truncated)")
		}
		parts = append(parts, types.TextPart{Text: b.String()})
	}
	if len(parts) == 0 {
		return types.NewAgentInput("")
	}
	return types.NewAgentInputWithParts(parts)
}

func (s *subagentImpl) Execute(ctx context.Context, req *Request) (*Result, error) {
	cfg := s.buildConfig(req)
	filteredTools := s.filterTools()

	eng := engine.NewAgentEngine(s.llmProvider, cfg)
	eng.AddTools(ctx, filteredTools)

	stream, err := eng.ExecuteStream(ctx, buildAgentInput(req), nil)
	if err != nil {
		return nil, err
	}

	var output strings.Builder
	var lastUsage types.Usage
	for result := range stream {
		if result.Error != nil {
			return &Result{Error: result.Error}, nil
		}
		switch result.Type {
		case "chunk":
			output.WriteString(result.Content)
		}
		if result.Result != nil {
			output.Reset()
			output.WriteString(result.Result.Output)
			lastUsage = result.Result.Usage
		}
	}
	if lastUsage.TotalTokens == 0 && lastUsage.PromptTokens == 0 && lastUsage.CompletionTokens == 0 {
		lastUsage = eng.GetTotalUsage()
	}

	return &Result{
		Output: output.String(),
		Usage:  lastUsage,
	}, nil
}

func (s *subagentImpl) buildConfig(req *Request) *types.AgentConfig {
	cfg := types.NewAgentConfig()

	if s.info.Prompt != "" {
		cfg.SystemMessage = SubagentSystemGuidelines + "\n\n" + s.info.Prompt
	} else {
		cfg.SystemMessage = SubagentSystemGuidelines
	}

	if s.info.Temperature != nil {
		cfg.Temperature = float32(*s.info.Temperature)
	}

	if s.info.TopP != nil {
		cfg.TopP = float32(*s.info.TopP)
	}

	cfg.MaxIterations = subagentMaxIterations
	cfg.Timeout = subagentExecuteTimeout
	cfg.EnableMemoryCompress = false
	cfg.MaxHistoryMessages = s.maxHistoryMessages

	return cfg
}

func (s *subagentImpl) filterTools() []types.Tool {
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
	mu                   sync.RWMutex
	subagents            map[string]Subagent
	factory              Factory
	subagentMaxHistory   int
}

type Factory interface {
	GetAgent(name string) (*Info, bool)
	GetLLMProvider() types.LLMProvider
	GetTools() []types.Tool
}

func NewManager(factory Factory, maxHistoryMessages int) *Manager {
	mh := maxHistoryMessages
	if mh <= 0 {
		mh = 48
	}
	return &Manager{
		subagents:          make(map[string]Subagent),
		factory:            factory,
		subagentMaxHistory: mh,
	}
}

func (m *Manager) GetSubagent(name string) (Subagent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sa, ok := m.subagents[name]; ok {
		return sa, nil
	}

	info, ok := m.factory.GetAgent(name)
	if !ok {
		return nil, fmt.Errorf("agent not found: %s", name)
	}

	if info.Mode != ModeSubagent {
		return nil, fmt.Errorf("agent %s is not a subagent", name)
	}

	sa, err := NewSubagent(info, m.factory.GetLLMProvider(), m.factory.GetTools(), m.subagentMaxHistory)
	if err != nil {
		return nil, err
	}

	m.subagents[name] = sa
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
