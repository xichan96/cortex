package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// spawn/wait 工具族（S3，subagent-s3s4 §3/§5.2）。P2.3 定稿：新工具族，保留 delegate。
//
// 依赖注入方式：工具不持有 session 状态，用闭包注入当前 session 的 mailbox/path/sessionID
// （factory 构造时绑定）。这让工具实例可复用于多个 session（factory 按 session 注册）。

// SpawnAgentTool fire-and-forget 派生子代理（subagent-s3s4 §3.2）。
type SpawnAgentTool struct {
	manager   *SubagentManager
	mailbox   func() *Mailbox
	path      func() AgentPath
	sessionID func() string
}

// NewSpawnAgentTool 构造 spawn 工具。mailbox 当前 session 的 mailbox（可为 nil，纯 S3 惰性模式）。
func NewSpawnAgentTool(manager *SubagentManager, mailbox func() *Mailbox, path func() AgentPath, sessionID func() string) *SpawnAgentTool {
	return &SpawnAgentTool{
		manager:   manager,
		mailbox:   mailbox,
		path:      path,
		sessionID: sessionID,
	}
}

func (t *SpawnAgentTool) Name() string { return "spawn_agent" }

func (t *SpawnAgentTool) Description() string {
	return "Spawn a subagent to run a task in the background (fire-and-forget). Returns a task_id you can later pass to wait_agent to collect the result."
}

func (t *SpawnAgentTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"agent": map[string]interface{}{
				"type":        "string",
				"description": "The agent to spawn: 'general' for complex research",
				"enum":        []string{"general"},
			},
			"task": map[string]interface{}{
				"type":        "string",
				"description": "The task description for the subagent",
			},
			"task_name": map[string]interface{}{
				"type":        "string",
				"description": "Optional; name for this run (roadmap list_agents)",
			},
			"fork_turns": map[string]interface{}{
				"type":        "integer",
				"description": "Optional; ignored this round (placeholder)",
			},
			"timeout_ms": map[string]interface{}{
				"type":        "integer",
				"description": "Optional; override the default watchdog timeout",
			},
		},
		"required": []string{"agent", "task"},
	}
}

func (t *SpawnAgentTool) Metadata() types.ToolMetadata {
	// 工具结果缓存毒化（评审 §5.2 结尾 / 风险 10）：spawn 返回动态 task_id，
	// 绝不能被 engine toolCache 缓存。
	return types.ToolMetadata{
		ToolType: "builtin",
		Extra:    map[string]interface{}{"no_cache": true},
	}
}

// Execute 派生并立即返回 {task_id}。max_concurrent_spawns 反映 semaphore 容量。
func (t *SpawnAgentTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	if t.manager == nil {
		return map[string]interface{}{
			"error": "subagent manager unavailable",
		}, nil
	}
	agentName, _ := input["agent"].(string)
	task, _ := input["task"].(string)
	if agentName == "" || task == "" {
		return nil, fmt.Errorf("agent and task are required")
	}

	var taskName string
	if v, ok := input["task_name"].(string); ok {
		taskName = v
	}
	var timeoutMS int64
	if v, ok := input["timeout_ms"].(int64); ok {
		timeoutMS = v
	} else if v, ok := input["timeout_ms"].(int); ok {
		timeoutMS = int64(v)
	}

	sid := ""
	if t.sessionID != nil {
		sid = t.sessionID()
	}
	parent := RootAgentPath()
	if t.path != nil {
		parent = t.path()
	}
	var mb *Mailbox
	if t.mailbox != nil {
		mb = t.mailbox()
	}

	handle, err := t.manager.Spawn(ctx, sid, &Request{
		AgentName: agentName,
		Input:     task,
	}, parent, mb, SpawnOptions{
		TimeoutMS: timeoutMS,
		TaskName:  taskName,
	})
	if err != nil {
		return nil, err
	}
	if handle == nil {
		return map[string]interface{}{"task_id": "", "error": "spawn unavailable"}, nil
	}

	maxConc := DefaultMaxConcurrentSpawns
	if cfg := t.manager.config; cfg != nil && cfg.MaxConcurrentSpawns > 0 {
		maxConc = cfg.MaxConcurrentSpawns
	}
	return map[string]interface{}{
		"task_id":               handle.TaskID,
		"agent":                 agentName,
		"task_name":             taskName,
		"status":                "spawned",
		"max_concurrent_spawns": maxConc,
	}, nil
}

// WaitAgentTool 阻塞订阅 mailbox 取回 spawn 结果（subagent-s3s4 §3.3/§5.2）。
type WaitAgentTool struct {
	manager        *SubagentManager
	mailbox        func() *Mailbox
	defaultTimeout time.Duration
}

// NewWaitAgentTool 构造 wait 工具。defaultTimeout 工具内部等待上限；
// 注意：必须 < engine 对该工具的 ToolTimeouts 值（评审 BLOCKER-3），factory 注册时配。
func NewWaitAgentTool(manager *SubagentManager, mailbox func() *Mailbox, defaultTimeout time.Duration) *WaitAgentTool {
	if defaultTimeout <= 0 {
		defaultTimeout = DefaultWaitAgentTimeout
	}
	return &WaitAgentTool{
		manager:        manager,
		mailbox:        mailbox,
		defaultTimeout: defaultTimeout,
	}
}

// DefaultWaitAgentTimeout wait_agent 默认阻塞等待上限。
// 评审 BLOCKER-3：默认 engine ToolExecutionTimeout=60s。wait 内部有效上限必须 < engine
// 工具超时，否则 engine 的 ctx 先 cancel，父代理拿到 tool error 而非 {timed_out:true}。
// 这里默认 30s < 60s；factory 注册时另配 ToolTimeouts["wait_agent"] 抬高 engine 外壳
// （见 subagent-s3s4 评审 §3 BLOCKER-3 修法）。
const DefaultWaitAgentTimeout = 30 * time.Second

// WaitAgentToolShellTimeout factory 为 wait_agent 配的 engine 工具外壳超时。
// 必须 > DefaultWaitAgentTimeout（30s），保证 wait 内部先超时返回 {timed_out:true}
// 信封，engine 的 select 收到 result 通道而非 EC_TOOL_EXECUTION_TIMEOUT。
const WaitAgentToolShellTimeout = 200 * time.Second

func (t *WaitAgentTool) Name() string { return "wait_agent" }

func (t *WaitAgentTool) Description() string {
	return "Wait for a spawned subagent to finish and collect its result. Blocking; returns immediately if the result is already available."
}

func (t *WaitAgentTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task_id": map[string]interface{}{
				"type":        "string",
				"description": "The task_id returned by spawn_agent",
			},
			"timeout_ms": map[string]interface{}{
				"type":        "integer",
				"description": "Optional; maximum time to block waiting, defaults to 30000ms",
			},
		},
		"required": []string{"task_id"},
	}
}

func (t *WaitAgentTool) Metadata() types.ToolMetadata {
	// 评审风险 10：wait 结果信封随 task_id 动态，禁缓存。
	return types.ToolMetadata{
		ToolType: "builtin",
		Extra:    map[string]interface{}{"no_cache": true},
	}
}

// Execute 阻塞订阅 mailbox，返回 {task_id, completed, timed_out, message}。
// message 直接是 DelegateResult.Truncated() 截断文本（父代理即可消费）。
//
// 评审 BLOCKER-3 交互：defaultTimeout（默认 30s）< engine ToolTimeouts["wait_agent"]
// （factory 配 200s）< engine ToolExecutionTimeout（60s 兜底）。wait 内部先超时返回
// {timed_out:true} 信封（nil error），engine 的 select 先收到 result 通道 → 不触发
// EC_TOOL_EXECUTION_TIMEOUT。ctx.Done 分支兜底 engine 外壳若被配得更短。
func (t *WaitAgentTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	taskID, _ := input["task_id"].(string)
	if taskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	timeout := t.defaultTimeout
	if v, ok := input["timeout_ms"].(int64); ok && v > 0 {
		timeout = time.Duration(v) * time.Millisecond
	} else if v, ok := input["timeout_ms"].(int); ok && v > 0 {
		timeout = time.Duration(v) * time.Millisecond
	}

	mb := t.mailbox()
	if mb == nil {
		return map[string]interface{}{
			"task_id": taskID, "completed": false, "timed_out": true,
			"message": "no mailbox available for this session",
		}, nil
	}

	// 快速路径：结果已落 mailbox。
	if r := mb.Drain(taskID); r != nil {
		return t.final(taskID, r), nil
	}

	// 阻塞订阅一次：先查消息是否存在（锁内），不存在才注册，杜绝 Put/订阅竞态。
	ch := make(chan struct{})
	var once sync.Once
	subID := mb.SubscribeOnce(taskID, func() {
		once.Do(func() { close(ch) })
	})
	defer func() {
		if subID != "" {
			mb.Unsubscribe(taskID, subID)
		}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ch:
		if r := mb.Drain(taskID); r != nil {
			return t.final(taskID, r), nil
		}
		return map[string]interface{}{
			"task_id": taskID, "completed": false, "timed_out": true,
			"message": "subagent finished but result was collected by another consumer",
		}, nil
	case <-ctx.Done():
		// engine 外壳超时先到：返回信封而非 error，让父代理看到 timed_out 而非 tool error。
		msg := "wait_agent cancelled"
		if ctx.Err() != nil && ctx.Err().Error() != "" {
			msg = "wait_agent cancelled: " + ctx.Err().Error()
		}
		return map[string]interface{}{
			"task_id": taskID, "completed": false, "timed_out": true, "message": msg,
		}, nil
	case <-timer.C:
		return map[string]interface{}{
			"task_id": taskID, "completed": false, "timed_out": true,
			"message": fmt.Sprintf("wait_agent timed out after %dms", timeout.Milliseconds()),
		}, nil
	}
}

func (t *WaitAgentTool) final(taskID string, env *DelegateResult) map[string]interface{} {
	maxRunes := DefaultDelegateTruncatedRunes
	if t.manager != nil && t.manager.config != nil && t.manager.config.CompletionMaxRunes > 0 {
		maxRunes = t.manager.config.CompletionMaxRunes
	}
	return map[string]interface{}{
		"task_id":   taskID,
		"completed": true,
		"timed_out": false,
		"message":   env.Truncated(maxRunes),
	}
}
