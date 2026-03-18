package agent

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/xichan96/cortex/agent/engine"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/permission"
	"github.com/xichan96/cortex/pkg/logger"
)

type SubagentManager struct {
	config   *SubagentConfig
	manager  *Manager
	llmProv  types.LLMProvider
	tools    []types.Tool
	mu       sync.RWMutex
	compiled map[string][]*regexp.Regexp
}

type SubagentManagerFactory interface {
	GetLLMProvider() types.LLMProvider
	GetTools() []types.Tool
	GetAgent(name string) (*Info, bool)
}

func NewSubagentManager(config *SubagentConfig, factory SubagentManagerFactory) *SubagentManager {
	if config == nil || !config.Enabled {
		return nil
	}

	compiled := make(map[string][]*regexp.Regexp)
	for _, t := range config.Triggers {
		for _, pattern := range t.Patterns {
			re, err := regexp.Compile(pattern)
			if err != nil {
				logger.Warn("[SubagentManager] invalid regex pattern",
					slog.String("pattern", pattern),
					slog.String("agent", t.AgentName),
					slog.String("error", err.Error()))
				continue
			}
			compiled[t.AgentName] = append(compiled[t.AgentName], re)
		}
	}

	return &SubagentManager{
		config:   config,
		manager:  NewManager(factory),
		llmProv:  factory.GetLLMProvider(),
		tools:    factory.GetTools(),
		compiled: compiled,
	}
}

type TriggerResult struct {
	AgentName  string
	Confidence float64
	Reason     string
}

func (sm *SubagentManager) ShouldDelegate(input string) *TriggerResult {
	if sm == nil || sm.config == nil || !sm.config.Enabled {
		return nil
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	inputLower := strings.ToLower(input)
	var bestResult *TriggerResult

	for _, trigger := range sm.config.Triggers {
		score := 0
		reason := ""

		if sm.config.TriggerOnKeyword {
			for _, keyword := range trigger.Keywords {
				if strings.Contains(inputLower, strings.ToLower(keyword)) {
					score += 10
					reason += "keyword match: " + keyword + "; "
				}
			}
		}

		if patterns, ok := sm.compiled[trigger.AgentName]; ok {
			for _, re := range patterns {
				if re.MatchString(input) {
					score += 20
					reason += "pattern match: " + re.String() + "; "
				}
			}
		}

		if score > 0 {
			confidence := float64(score) / float64(trigger.Priority+score)
			if bestResult == nil || confidence > bestResult.Confidence {
				bestResult = &TriggerResult{
					AgentName:  trigger.AgentName,
					Confidence: confidence,
					Reason:     reason,
				}
			}
		}
	}

	return bestResult
}

func (sm *SubagentManager) Execute(ctx context.Context, agentName, input string) (*Result, error) {
	if sm == nil || sm.manager == nil {
		return nil, nil
	}

	logger.Info("[SubagentManager] delegating to subagent",
		slog.String("agent", agentName),
		slog.String("input_length", strconv.Itoa(len(input))))

	sa, err := sm.manager.GetSubagent(agentName)
	if err != nil {
		return nil, err
	}

	result, err := sa.Execute(ctx, &Request{
		AgentName: agentName,
		Input:     input,
	})

	if err != nil {
		logger.Error("[SubagentManager] subagent execution failed",
			slog.String("agent", agentName),
			slog.String("error", err.Error()))
		return nil, err
	}

	logger.Info("[SubagentManager] subagent completed",
		slog.String("agent", agentName),
		slog.Int("output_length", len(result.Output)))

	return result, nil
}

func (sm *SubagentManager) Close() {
	if sm != nil && sm.manager != nil {
		sm.manager.Close()
	}
}

type SubagentTool struct {
	manager *SubagentManager
}

func NewSubagentTool(manager *SubagentManager) *SubagentTool {
	return &SubagentTool{manager: manager}
}

func (t *SubagentTool) Name() string {
	return "delegate_to_agent"
}

func (t *SubagentTool) Description() string {
	return "Delegate complex tasks to specialized subagents (explore, general) for better results"
}

func (t *SubagentTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"name":        t.Name(),
		"description": t.Description(),
		"parameters": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"agent": map[string]interface{}{
					"type":        "string",
					"description": "The agent to delegate to: 'explore' for code search, 'general' for complex research",
					"enum":        []string{"explore", "general"},
				},
				"task": map[string]interface{}{
					"type":        "string",
					"description": "The task description for the subagent",
				},
			},
			"required": []string{"agent", "task"},
		},
	}
}

func (t *SubagentTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{ToolType: "builtin"}
}

func (t *SubagentTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.manager == nil {
		return "", nil
	}

	agentName, _ := input["agent"].(string)
	task, _ := input["task"].(string)

	if agentName == "" || task == "" {
		return nil, fmt.Errorf("agent and task are required")
	}

	result, err := t.manager.Execute(ctx, agentName, task)
	if err != nil {
		return nil, err
	}

	if result != nil {
		return result.Output, nil
	}
	return "", nil
}

type subagentExecutor struct {
	manager *SubagentManager
	llmProv types.LLMProvider
	tools   []types.Tool
}

func newSubagentExecutor(manager *SubagentManager, llm types.LLMProvider, tools []types.Tool) *subagentExecutor {
	return &subagentExecutor{
		manager: manager,
		llmProv: llm,
		tools:   tools,
	}
}

func (e *subagentExecutor) autoDelegate(ctx context.Context, input string) (*Result, string, error) {
	if e.manager == nil {
		return nil, "", nil
	}

	trigger := e.manager.ShouldDelegate(input)
	if trigger == nil || trigger.Confidence < 0.3 {
		return nil, "", nil
	}

	logger.Info("[SubagentExecutor] auto-delegating",
		slog.String("agent", trigger.AgentName),
		slog.Float64("confidence", trigger.Confidence),
		slog.String("reason", trigger.Reason))

	result, err := e.manager.Execute(ctx, trigger.AgentName, input)
	if err != nil {
		return nil, trigger.AgentName, err
	}

	return result, trigger.AgentName, nil
}

func buildSubagentEngine(ctx context.Context, agentInfo *Info, llm types.LLMProvider, tools []types.Tool) *engine.AgentEngine {
	cfg := types.NewAgentConfig()

	if agentInfo.Prompt != "" {
		cfg.SystemMessage = agentInfo.Prompt
	}

	if agentInfo.Temperature != nil {
		cfg.Temperature = float32(*agentInfo.Temperature)
	}

	if agentInfo.TopP != nil {
		cfg.TopP = float32(*agentInfo.TopP)
	}

	cfg.MaxIterations = 50
	cfg.Timeout = 3 * 60e9
	cfg.EnableMemoryCompress = false

	eng := engine.NewAgentEngine(llm, cfg)

	evaluator := permission.NewEvaluator(agentInfo.Permission)
	var filteredTools []types.Tool
	for _, tool := range tools {
		action := evaluator.Evaluate(tool.Name(), nil)
		if action == permission.ActionAllow {
			filteredTools = append(filteredTools, tool)
		}
	}

	eng.AddTools(ctx, filteredTools)
	return eng
}

type SubagentEventEmitter interface {
	Emit(event *SubagentEvent)
}

type SubagentEvent struct {
	Type      string
	AgentName string
	Content   string
	Error     error
	Done      bool
}

type SubagentHandler struct {
	manager *SubagentManager
	emitter SubagentEventEmitter
}

func NewSubagentHandler(manager *SubagentManager, emitter SubagentEventEmitter) *SubagentHandler {
	return &SubagentHandler{
		manager: manager,
		emitter: emitter,
	}
}

func (h *SubagentHandler) ProcessInput(ctx context.Context, input string) (bool, error) {
	if h.manager == nil {
		return false, nil
	}

	trigger := h.manager.ShouldDelegate(input)
	if trigger == nil || trigger.Confidence < 0.3 {
		return false, nil
	}

	if h.emitter != nil {
		h.emitter.Emit(&SubagentEvent{
			Type:      "delegate_start",
			AgentName: trigger.AgentName,
			Content:   input,
		})
	}

	result, err := h.manager.Execute(ctx, trigger.AgentName, input)
	if err != nil {
		if h.emitter != nil {
			h.emitter.Emit(&SubagentEvent{
				Type:      "delegate_error",
				AgentName: trigger.AgentName,
				Error:     err,
			})
		}
		return true, err
	}

	if result != nil && h.emitter != nil {
		h.emitter.Emit(&SubagentEvent{
			Type:      "delegate_result",
			AgentName: trigger.AgentName,
			Content:   result.Output,
		})
	}

	if h.emitter != nil {
		h.emitter.Emit(&SubagentEvent{
			Type:      "delegate_done",
			AgentName: trigger.AgentName,
			Done:      true,
		})
	}

	return true, nil
}

func (h *SubagentHandler) ManualDelegate(ctx context.Context, agentName, input string) (*Result, error) {
	if h.manager == nil {
		return nil, nil
	}

	if h.emitter != nil {
		h.emitter.Emit(&SubagentEvent{
			Type:      "delegate_start",
			AgentName: agentName,
			Content:   input,
		})
	}

	result, err := h.manager.Execute(ctx, agentName, input)
	if err != nil {
		if h.emitter != nil {
			h.emitter.Emit(&SubagentEvent{
				Type:      "delegate_error",
				AgentName: agentName,
				Error:     err,
			})
		}
		return nil, err
	}

	if result != nil && h.emitter != nil {
		h.emitter.Emit(&SubagentEvent{
			Type:      "delegate_result",
			AgentName: agentName,
			Content:   result.Output,
		})
	}

	if h.emitter != nil {
		h.emitter.Emit(&SubagentEvent{
			Type:      "delegate_done",
			AgentName: agentName,
			Done:      true,
		})
	}

	return result, nil
}
