package agent

import (
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// 子代理结构化结果信封（设计 docs/design/subagent.md §3.1，方案 A）。
//
// DelegateResult 是 delegate_to_agent 的返回信封，同时是完成通知推送给父代理的载荷。
// 兼容策略：Output 保留子代理最终文本输出（即旧 Result.Output 语义），父代理现有消费方
// 不破坏；工具返回值经 FormatToolResult 自动 JSON 序列化注入观察。
//
// 字段类型统一（评审 R2）：时间一律 unix 毫秒 int64，避免 time.Time 与 int64 混排
// 增加模型解析负担；可选字段用 omitempty，零值不出现在 JSON 里。
type DelegateResult struct {
	// Agent 子代理名（如 "general"）。
	Agent string `json:"agent"`
	// TaskID 每次委派生成的 run id（uuid）。B 阶段可复用 task_id 挂接既有 run。
	TaskID string `json:"task_id"`
	// Status 终态："completed" | "error" | "timeout" | "cancelled"。
	Status string `json:"status"`
	// Output 子代理最终文本输出（裸字符串语义保留，向后兼容）。
	Output string `json:"output,omitempty"`
	// FilesChanged 子代理报告改动/涉猎的文件路径（程序化 tool_event 采集 + prompt 引导）。
	FilesChanged []string `json:"files_changed,omitempty"`
	// Error 失败时的错误摘要（截断）。
	Error string `json:"error,omitempty"`
	// DurationMS 墙钟耗时 ms（含队列等待）。
	DurationMS int64 `json:"duration_ms"`
	// Iterations 实际迭代数。
	Iterations int `json:"iterations"`
	// Usage 子代理本次 token 用量。
	Usage types.Usage `json:"usage"`
	// TimestampMS 完成时刻 unix 毫秒。
	TimestampMS int64 `json:"timestamp_ms"`
	// AgentPath 子代理在委派树中的路径（/root/<agent>，S3 铺路，subagent-s3s4 §9）。
	AgentPath string `json:"agent_path,omitempty"`
	// ParentPath 父代理路径（委派发起方，S3 铺路）。
	ParentPath string `json:"parent_path,omitempty"`
}

// 信封状态常量。
const (
	DelegateStatusCompleted = "completed"
	DelegateStatusError     = "error"
	DelegateStatusTimeout   = "timeout"
	DelegateStatusCancelled = "cancelled"
)

// 信封截断上限默认值（≈1000 tokens，对齐 codex COMPLETION_MESSAGE_MAX_TOKENS=1000）。
const DefaultDelegateTruncatedRunes = 2000

// DelegateResultFromResult 把子代理执行结果折叠进信封（成功路径）。err 非 nil 时返回
// Status=="error" 的错误态信封（评审 R1：错误路径折叠，让错误态在 A 阶段对模型可见）。
func DelegateResultFromResult(agentName string, result *Result, err error) *DelegateResult {
	if err != nil {
		return &DelegateResult{
			Agent:       agentName,
			Status:      DelegateStatusError,
			Error:       types.NormalizeToolError(err, types.ToolErrorMaxLen),
			TimestampMS: time.Now().UnixMilli(),
		}
	}
	if result == nil {
		return &DelegateResult{
			Agent:       agentName,
			Status:      DelegateStatusCompleted,
			TimestampMS: time.Now().UnixMilli(),
		}
	}
	status := result.Status
	if status == "" {
		if result.Error != nil {
			status = DelegateStatusError
		} else {
			status = DelegateStatusCompleted
		}
	}
	env := &DelegateResult{
		Agent:        agentName,
		Status:       status,
		Output:       result.Output,
		FilesChanged: result.FilesChanged,
		Iterations:   result.Iterations,
		Usage:        result.Usage,
		TimestampMS:  time.Now().UnixMilli(),
	}
	if result.Duration > 0 {
		env.DurationMS = result.Duration.Milliseconds()
	}
	if result.Error != nil {
		env.Error = types.NormalizeToolError(result.Error, types.ToolErrorMaxLen)
	}
	return env
}

// NewErrorDelegateResult 构造纯错误态信封（无 Result，如 manager 取子代理失败）。
func NewErrorDelegateResult(agentName string, err error) *DelegateResult {
	return DelegateResultFromResult(agentName, nil, err)
}

// Truncated 返回给父代理 LLM 的紧凑文本视图（对齐 codex session_prefix.rs 完成消息格式）。
// maxRunes<=0 时用 DefaultDelegateTruncatedRunes（2000 runes ≈ 1000 tokens）。
// 错误态附回退文案（复用 codex 原文，符合其回退意图）。
func (r *DelegateResult) Truncated(maxRunes int) string {
	if r == nil {
		return ""
	}
	if maxRunes <= 0 {
		maxRunes = DefaultDelegateTruncatedRunes
	}

	var b strings.Builder
	b.WriteString("Message Type: FINAL_ANSWER\n")
	// 评审 RECOMMENDED-4：Sender/Task name 优先用 AgentPath/ParentPath 字段，
	// 兜底回退 /root/<agent> 硬编码形态（S1 老信封无路径字段时保持文本不变）。
	taskName := r.ParentPath
	if taskName == "" {
		taskName = "/root"
	}
	sender := r.AgentPath
	if sender == "" {
		sender = "/root/" + r.Agent
	}
	b.WriteString("Task name: " + taskName + "\n")
	b.WriteString("Sender: " + sender + "\n")
	b.WriteString("Status: " + r.Status + "\n")
	b.WriteString("Payload:\n")

	if r.Status == DelegateStatusError {
		b.WriteString("Agent errored: " + truncateRunes(r.Error, maxRunes) + "\n")
		b.WriteString("This agent's turn failed. If you still need this agent, use the available collaboration tools to give it another task.")
		return b.String()
	}

	payload := r.Output
	if payload == "" {
		payload = "(no output)"
	}
	b.WriteString(truncateRunes(payload, maxRunes))
	return b.String()
}

// truncateRunes 按 rune 数截断，超限加 "…(truncated)"。
func truncateRunes(s string, maxRunes int) string {
	if len(s) == 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…(truncated)"
}
