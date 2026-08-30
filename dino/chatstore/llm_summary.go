package chatstore

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	agenttypes "github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/logger"
)

// defaultSummaryTimeout 摘要调用的独立短超时。compressCtx 由 engine 兜底（默认
// 10min、下限 2min，见 agent_execution.go saveToMemoryAndMaybeCompress），一次挂死的
// 摘要调用不该占据 compress goroutine 这么久——独立超时把 LLM 摘要与压缩主路径解耦
// （评审 R2）。
const defaultSummaryTimeout = 90 * time.Second

// SummaryMarker 摘要注入的编码前缀（对齐 Codex SUMMARY_PREFIX 语义）。只用于
// GetMessages 注入侧区分，存储侧（Hybrid.summary）是裸文本。
const SummaryMarker = "[Summary]"

const (
	// defaultSummaryMaxTokens 摘要输出长度近似约束（写入 prompt）。
	defaultSummaryMaxTokens = 2000
	// defaultSummaryMaxInput 摘要输入消息条数上限：SQLite.GetMessages(0) 返回全量
	// 历史（无 LIMIT），长会话下 older 可能巨大；InMemory 有 maxHistoryMessages 窗口
	// 兜底。条数上限与 Hybrid.Compress 的尾部 token 预算互补（评审 R3）。
	defaultSummaryMaxInput = 200
)

// LLMSummaryAdapter 桥接 types.LLMProvider → chatstore.LLMProvider。
// 不改 types.LLMProvider 接口体（与 prompt-caching R3 的类型断言策略一致）。
type LLMSummaryAdapter struct {
	llm       agenttypes.LLMProvider
	timeout   time.Duration
	maxTokens int // 摘要输出上限提示（写入 prompt，模型侧近似约束）
	maxInput  int // older 输入条数上限（SQLite 全量历史兜底，评审 R3）
}

// NewLLMSummaryAdapter 构造摘要适配器。timeout<=0 用 defaultSummaryTimeout；
// maxTokens/maxInput<=0 使用默认值。
func NewLLMSummaryAdapter(llm agenttypes.LLMProvider) *LLMSummaryAdapter {
	return &LLMSummaryAdapter{
		llm:       llm,
		timeout:   defaultSummaryTimeout,
		maxTokens: defaultSummaryMaxTokens,
		maxInput:  defaultSummaryMaxInput,
	}
}

const summarySystemPrompt = `You are a conversation compactor for an AI agent.
Produce a concise summary of the conversation below, in the same language as the conversation.
Keep: the user's goals and constraints, decisions made, files touched, pending work, and unresolved issues.
Preserve exact file paths, command names, tool names, and any code identifiers verbatim.
Omit: tool output bodies, boilerplate, and incidental detail.
Output only the summary text, no preamble.
The summary must not exceed %d tokens.`

// GenerateSummary 用 ChatWithTools 调一次摘要模型。失败/超时返回 error，由调用方
// （Hybrid.Compress）回退 DeterministicCompact。
func (a *LLMSummaryAdapter) GenerateSummary(ctx context.Context, messages []Message) (string, error) {
	if a == nil || a.llm == nil {
		return "", nil
	}

	// R2：独立短超时。摘要调用在主压缩 goroutine 里，compressCtx 超时窗口太宽
	// （默认 10min）；这里给 90s 兜底，超时即回退 DeterministicCompact。
	timeout := a.timeout
	if timeout <= 0 {
		timeout = defaultSummaryTimeout
	}
	sumCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// R3：输入条数上限，优先保留最近消息（old 段末尾最贴近当前上下文）。
	maxInput := a.maxInput
	if maxInput <= 0 {
		maxInput = defaultSummaryMaxInput
	}
	input := capSummaryInput(sanitizeSummaryInput(messages), maxInput)

	maxTokens := a.maxTokens
	if maxTokens <= 0 {
		maxTokens = defaultSummaryMaxTokens
	}
	msgs := make([]agenttypes.Message, 0, 1+len(input))
	msgs = append(msgs, agenttypes.Message{
		Role:    "system",
		Content: fmt.Sprintf(summarySystemPrompt, maxTokens),
	})
	for _, m := range input {
		msgs = append(msgs, agenttypes.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
		})
	}

	resp, err := a.llm.ChatWithTools(sumCtx, msgs, nil) // 摘要不需要工具
	if err != nil {
		logger.Warn("[LLMSummaryAdapter] summary LLM call failed",
			slog.String("error", err.Error()), slog.String("model", a.llm.GetModelName()))
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

// sanitizeSummaryInput 摘要输入消毒（评审 R1）：丢弃孤儿 tool 消息、剥离未配对的
// ToolCalls 块。older 切割点可能落在 assistant(tool_use) 与其 tool 结果之间（孤儿
// tool_use），或 tool 结果被切进 tail（孤儿 tool）——Anthropic 对这类消息 400，
// 直接送会导致摘要调用永久降级。
func sanitizeSummaryInput(messages []Message) []Message {
	out := make([]Message, 0, len(messages))
	pending := make(map[string]struct{})
	for _, m := range messages {
		role := strings.ToLower(m.Role)
		switch {
		case role == "tool":
			// 仅当 tool 结果匹配一个尚未配对的 tool_use 时才保留。
			if m.ToolCallID == "" {
				continue
			}
			if _, ok := pending[m.ToolCallID]; !ok {
				continue
			}
			delete(pending, m.ToolCallID)
		case role == "assistant":
			if len(m.ToolCalls) == 0 {
				// 无 tool_calls 的 assistant：保留原文，但清掉悬空的 pending。
				m.ToolCalls = nil
				pending = make(map[string]struct{})
			} else {
				np := make(map[string]struct{}, len(m.ToolCalls))
				for _, tc := range m.ToolCalls {
					if tc.ID != "" {
						np[tc.ID] = struct{}{}
					}
				}
				pending = np
			}
		default:
			// user/system 消息打断 tool 配对链。
			pending = make(map[string]struct{})
		}
		out = append(out, m)
	}
	// 丢弃尾部未配对的 assistant(tool_use)（其 tool 结果被切进 tail）。
	for len(out) > 0 && strings.EqualFold(out[len(out)-1].Role, "assistant") && len(out[len(out)-1].ToolCalls) > 0 {
		out = out[:len(out)-1]
	}
	return out
}

// capSummaryInput 对 older 做条数上限（评审 R3）。优先保留尾部（最近）消息。
func capSummaryInput(messages []Message, max int) []Message {
	if max <= 0 || len(messages) <= max {
		return messages
	}
	return messages[len(messages)-max:]
}
