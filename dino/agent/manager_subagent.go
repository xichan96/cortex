package agent

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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

	// —— S3/B1 铺路（评审 B2 BLOCKER，S1 阶段无 spawn 所以只有接口 + 钩子）——
	// sessionCancels 按 sessionID 分包：map[sessionID]map[taskID]context.CancelFunc。
	// CloseSession 通知 manager 释放该 session 派生的所有子代理 cancel，杜绝孤儿
	// goroutine 在父 session 关闭后继续烧 token。S3 spawn_agent 落地时把子代理 ctx
	// 的 cancel 注册进这里，CloseSession 遍历 cancel。
	sessionCancels map[string]map[string]context.CancelFunc
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
		config:         config,
		manager:        NewManager(factory, config.MaxHistoryMessages),
		llmProv:        factory.GetLLMProvider(),
		sessionCancels: make(map[string]map[string]context.CancelFunc),
		tools:          factory.GetTools(),
		compiled:       compiled,
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
	// S3/B1 铺路（评审 B2）：Close 全量 cancel 所有 session 的子代理。
	sm.cancelSessionAll("")
}

// CloseSession 释放指定 session 派生的所有子代理 cancel（评审 B2 BLOCKER）。
// dino factory 的 CloseSession 必须调用它（见 dino/factory.go 接线），否则 spawn
// 的子代理 goroutine 会脱离父 session 生命周期、只能靠 watchdog 收尸。
// S1 阶段无 spawn（delegate 仍同步执行），此处只有分桶/释放接口，S3 落地填充。
func (sm *SubagentManager) CloseSession(sessionID string) {
	sm.cancelSessionAll(sessionID)
}

// registerSubagentCancel 注册一次子代理执行的 cancel（S3 spawn_agent 用）。
func (sm *SubagentManager) registerSubagentCancel(sessionID, taskID string, cancel context.CancelFunc) {
	if sm == nil || sessionID == "" || taskID == "" || cancel == nil {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.sessionCancels == nil {
		sm.sessionCancels = make(map[string]map[string]context.CancelFunc)
	}
	bucket := sm.sessionCancels[sessionID]
	if bucket == nil {
		bucket = make(map[string]context.CancelFunc)
		sm.sessionCancels[sessionID] = bucket
	}
	bucket[taskID] = cancel
}

// unregisterSubagentCancel 移除已完成的子代理 cancel 注册（S3 spawn_agent 完成时调）。
func (sm *SubagentManager) unregisterSubagentCancel(sessionID, taskID string) {
	if sm == nil {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if bucket := sm.sessionCancels[sessionID]; bucket != nil {
		delete(bucket, taskID)
		if len(bucket) == 0 {
			delete(sm.sessionCancels, sessionID)
		}
	}
}

// cancelSessionAll cancel 指定 session（或全部，sessionID==""）的所有子代理。
func (sm *SubagentManager) cancelSessionAll(sessionID string) {
	if sm == nil {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for sid, bucket := range sm.sessionCancels {
		if sessionID != "" && sid != sessionID {
			continue
		}
		for taskID, cancel := range bucket {
			cancel()
			delete(bucket, taskID)
		}
		delete(sm.sessionCancels, sid)
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
	return "Delegate complex tasks to specialized subagents (general) for better results"
}

func (t *SubagentTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent": map[string]interface{}{
				"type":        "string",
				"description": "The agent to delegate to: 'general' for complex research",
				"enum":        []string{"general"},
			},
			"task": map[string]interface{}{
				"type":        "string",
				"description": "The task description for the subagent",
			},
			"task_id": map[string]interface{}{
				"type":        "string",
				"description": "Optional; omit to auto-generate. Reuse to attach to an existing run (B 阶段).",
			},
		},
		"required": []string{"agent", "task"},
	}
}

func (t *SubagentTool) Metadata() types.ToolMetadata {
	// B1（评审 BLOCKER）：工具结果缓存毒化。delegate_to_agent 返回值从裸字符串改成
	// 信封后，AgentEngine 的 toolCache（LRU + 5min TTL，key = toolName+args MD5）会让
	// 父 LLM 在 5 分钟内混读裸字符串/信封两种形态。这里用 Extra["no_cache"]=true
	// （agent_execution.go:354-359 已支持）从根上关掉 delegate 的缓存。
	return types.ToolMetadata{
		ToolType: "builtin",
		Extra:    map[string]interface{}{"no_cache": true},
	}
}

// Execute 返回 *DelegateResult 信封（方案 A，S1）。
// 错误路径折叠进信封（评审 R1）：manager.Execute 出错时返回 Status=="error" 的信封 +
// nil error，让错误态在 A 阶段对模型可见，而非走 toolObservationError 字符串分支。
func (t *SubagentTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.manager == nil {
		return "", nil
	}

	agentName, _ := input["agent"].(string)
	task, _ := input["task"].(string)

	if agentName == "" || task == "" {
		return nil, fmt.Errorf("agent and task are required")
	}

	start := time.Now()
	result, err := t.manager.Execute(ctx, agentName, task)

	env := DelegateResultFromResult(agentName, result, err)
	env.TaskID = uuid.NewString()
	if env.DurationMS == 0 {
		env.DurationMS = time.Since(start).Milliseconds()
	}

	if result != nil {
		t.maybeReplayToParentMemory(ctx, agentName, task, result)
	}
	return env, nil
}

const replayTaskMax = 2048
const replayOutputMax = 12000

func (t *SubagentTool) maybeReplayToParentMemory(ctx context.Context, agentName, task string, result *Result) {
	if t.manager == nil || t.manager.config == nil || !t.manager.config.ReplayToParentMemory {
		return
	}
	pm := ParentMemoryFromContext(ctx)
	if pm == nil || pm.Memory == nil {
		return
	}
	taskPreview := task
	if len(taskPreview) > replayTaskMax {
		taskPreview = taskPreview[:replayTaskMax] + "…"
	}
	out := result.Output
	if len(out) > replayOutputMax {
		out = out[:replayOutputMax] + "…"
	}
	label := "[subagent " + agentName + "] " + taskPreview
	if err := pm.Memory.SaveContext(ctx, map[string]interface{}{
		"input": label,
		"role":  "user",
	}, map[string]interface{}{"output": out}); err != nil {
		logger.Warn("[SubagentTool] replay to parent memory failed",
			slog.String("session_id", pm.SessionID),
			slog.String("agent", agentName),
			slog.String("error", err.Error()))
	}
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
