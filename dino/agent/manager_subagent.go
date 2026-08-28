package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"
)

type SubagentManager struct {
	config  *SubagentConfig
	manager *Manager
	llmProv types.LLMProvider
	tools   []types.Tool
	mu      sync.RWMutex

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

	return &SubagentManager{
		config:         config,
		manager:        NewManager(factory, config.MaxHistoryMessages),
		llmProv:        factory.GetLLMProvider(),
		sessionCancels: make(map[string]map[string]context.CancelFunc),
		tools:          factory.GetTools(),
	}
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

// Execute 按 DelegateReturnMode 分支返回形态（设计 §14 遗留点 1，P2.2）：
//   - envelope（默认）：返回 *DelegateResult 信封（S1 方案 A）。错误路径折叠进信封
//     （评审 R1）：manager.Execute 出错时返回 Status=="error" 的信封 + nil error，
//     让错误态在 A 阶段对模型可见，而非走 toolObservationError 字符串分支。
//   - string：返回裸字符串 result.Output（S1 之前的兼容形态），错误直接透传，不做信封
//     折叠。FilesChanged/Status/Usage 等信息在该模式下不进工具返回值——纯兼容开关。
func (t *SubagentTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.manager == nil {
		return "", nil
	}

	agentName, _ := input["agent"].(string)
	task, _ := input["task"].(string)

	if agentName == "" || task == "" {
		return nil, fmt.Errorf("agent and task are required")
	}

	if t.manager.config != nil &&
		DelegateReturnModeOrDefault(t.manager.config.DelegateReturnMode) == DelegateReturnModeString {
		result, err := t.manager.Execute(ctx, agentName, task)
		if err != nil {
			return nil, err
		}
		if result != nil {
			t.maybeReplayToParentMemory(ctx, agentName, task, result)
			return result.Output, nil
		}
		return "", nil
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

