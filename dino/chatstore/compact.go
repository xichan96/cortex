package chatstore

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CompactConfig controls the deterministic compaction behavior.
type CompactConfig struct {
	// MaxRecentRequests is how many recent user messages to preserve verbatim.
	MaxRecentRequests int
	// MaxSummaryLines is the maximum number of lines in the summary section.
	MaxSummaryLines int
}

func DefaultCompactConfig() CompactConfig {
	return CompactConfig{
		MaxRecentRequests: 3,
		MaxSummaryLines:   100,
	}
}

// EstimateTokens provides a rough token count estimate.
// ASCII ~4 chars per token; non-ASCII ~2 tokens per rune（对齐 agent/types.RoughTokenEstimate，评审 §5.1）。
// CJK（UTF-8 3 字节/rune）按 rune 计非 ASCII → *2，比字节级保守但单调，够作预算用。
func EstimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	asciiCount := 0
	for _, r := range text {
		if r < 128 {
			asciiCount++
		}
	}
	nonASCII := utf8.RuneCountInString(text) - asciiCount
	return asciiCount/4 + nonASCII*2 + 1
}

// MaxRecentTailTokens 尾部 user 原文 token 预算（对齐 Codex COMPACT_USER_MESSAGE_MAX_TOKENS = 20_000）。
const MaxRecentTailTokens = 20_000

// splitTailUserMessages 从 messages 尾部回溯，把对话切成 (tail, older)：
//   - tail：最近 KeepRecentCount 条消息（所有 role，原文）+ 往前继续吸收 user 消息原文，
//     直到累积 token 预算 maxTailTokens 或消息耗尽（取并集的尾部）；
//   - older：送 LLM 摘要的旧段。
//
// 规则（对齐 Codex compact.rs:586-642 / 657-733 精神）：
//  1. 以最后一条消息为锚，tail 至少包含最近 KeepRecentCount 条消息（兼容现有语义）；
//  2. 向前吸收更多 user 消息原文，直到累计 EstimateTokens >= maxTailTokens；
//  3. 切割点落在完整消息边界（tail = messages[split:]，older = messages[:split]）；
//  4. tail 保持原文结构（含 tool_calls 完整配对，避免 repairLLMMessageToolOrdering 修复成本）。
func splitTailUserMessages(messages []Message, keepRecent int, maxTailTokens int) (tail []Message, older []Message) {
	if maxTailTokens <= 0 {
		maxTailTokens = MaxRecentTailTokens
	}
	if keepRecent <= 0 {
		keepRecent = 1
	}

	total := len(messages)
	if total <= keepRecent {
		return messages, nil
	}

	split := total - keepRecent
	budget := maxTailTokens

	// 从 split 处继续向前吸收 user 消息原文，直到 token 预算耗尽或到达第 1 条。
	for i := split - 1; i >= 0; i-- {
		msg := messages[i]
		if !strings.EqualFold(msg.Role, "user") {
			continue
		}
		tok := EstimateTokens(msg.Content)
		if tok > budget {
			// 单条消息超预算：仍保留该条（切割点不能落在消息中间），不再往前。
			break
		}
		budget -= tok
		split = i
	}

	return messages[split:], messages[:split]
}

// DeterministicCompact produces a structured summary of older messages
// without any LLM call. It extracts key information locally.
func DeterministicCompact(existingSummary string, messages []Message, cfg CompactConfig) string {
	if len(messages) == 0 {
		return existingSummary
	}

	var b strings.Builder

	// Previous summary
	if existingSummary != "" {
		b.WriteString("<previous_summary>\n")
		b.WriteString(existingSummary)
		b.WriteString("\n</previous_summary>\n\n")
	}

	// Scope
	userCount, assistantCount, toolCount := 0, 0, 0
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			userCount++
		case "assistant":
			assistantCount++
		case "tool":
			toolCount++
		}
	}
	b.WriteString("<scope>")
	b.WriteString(strconv.Itoa(userCount) + " user, ")
	b.WriteString(strconv.Itoa(assistantCount) + " assistant, ")
	b.WriteString(strconv.Itoa(toolCount) + " tool messages")
	b.WriteString("</scope>\n\n")

	// Tools mentioned
	toolsSeen := extractToolNames(messages)
	if len(toolsSeen) > 0 {
		b.WriteString("<tools_mentioned>")
		first := true
		for _, name := range toolsSeen {
			if !first {
				b.WriteString(", ")
			}
			b.WriteString(name)
			first = false
		}
		b.WriteString("</tools_mentioned>\n\n")
	}

	// Recent user requests
	recentUser := extractRecentUserMessages(messages, cfg.MaxRecentRequests)
	if len(recentUser) > 0 {
		b.WriteString("<recent_user_requests>\n")
		for _, req := range recentUser {
			truncated := truncateLine(req, 160)
			b.WriteString("- ")
			b.WriteString(truncated)
			b.WriteString("\n")
		}
		b.WriteString("</recent_user_requests>\n\n")
	}

	// Pending work
	pending := extractPendingWork(messages)
	if len(pending) > 0 {
		b.WriteString("<pending_work>\n")
		for _, p := range pending {
			b.WriteString("- ")
			b.WriteString(truncateLine(p, 160))
			b.WriteString("\n")
		}
		b.WriteString("</pending_work>\n\n")
	}

	// Key files
	files := extractKeyFiles(messages)
	if len(files) > 0 {
		b.WriteString("<key_files>")
		first := true
		for _, f := range files {
			if !first {
				b.WriteString(", ")
			}
			b.WriteString(f)
			first = false
		}
		b.WriteString("</key_files>\n\n")
	}

	// Current work context (last non-empty assistant message)
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && strings.TrimSpace(messages[i].Content) != "" {
			b.WriteString("<current_work>")
			b.WriteString(truncateLine(messages[i].Content, 200))
			b.WriteString("</current_work>\n")
			break
		}
	}

	return b.String()
}

// knownFileExtensions are extensions we recognize as file paths.
var knownFileExtensions = []string{
	".go", ".js", ".ts", ".tsx", ".jsx", ".py", ".rs", ".java", ".c", ".cpp", ".h",
	".md", ".yaml", ".yml", ".json", ".toml", ".xml", ".html", ".css", ".sql",
	".sh", ".bash", ".zsh", ".fish", ".lua", ".rb", ".php", ".swift", ".kt",
	".vue", ".svelte", ".astro", ".dart", ".ex", ".exs", ".erl", ".hs",
}

// pendingKeywords are keywords that indicate unfinished work.
var pendingKeywords = []string{
	"todo", "to-do", "to_do",
	"next", "pending", "follow-up", "followup", "follow up",
	"remaining", "left", "unfinished", "incomplete",
	"need to", "should", "must",
}

// extractToolNames extracts unique tool names from tool messages in order.
func extractToolNames(messages []Message) []string {
	seen := make(map[string]bool)
	var result []string
	for _, msg := range messages {
		if msg.Role == "tool" || msg.Role == "assistant" {
			// Look for tool call patterns in content
			// Tool messages typically have the tool name in metadata or content
			name := extractToolNameFromContent(msg.Content)
			if name != "" && !seen[name] {
				seen[name] = true
				result = append(result, name)
			}
		}
	}
	return result
}

// extractToolNameFromContent tries to find a tool name in message content.
func extractToolNameFromContent(content string) string {
	// Look for patterns like "Using tool: xxx" or "Tool: xxx" or just tool names
	// This is heuristic since we don't have structured tool call data here
	lines := strings.SplitN(content, "\n", 3)
	for _, line := range lines[:min(2, len(lines))] {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Using ") || strings.HasPrefix(line, "Tool: ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return strings.Trim(parts[len(parts)-1], ":.,;!?")
			}
		}
	}
	return ""
}

// extractRecentUserMessages returns the last n user messages.
func extractRecentUserMessages(messages []Message, n int) []string {
	var userMsgs []string
	for _, msg := range messages {
		if msg.Role == "user" && strings.TrimSpace(msg.Content) != "" {
			userMsgs = append(userMsgs, msg.Content)
		}
	}
	if len(userMsgs) <= n {
		return userMsgs
	}
	return userMsgs[len(userMsgs)-n:]
}

// extractPendingWork finds messages containing pending-work keywords.
func extractPendingWork(messages []Message) []string {
	var pending []string
	seen := make(map[string]bool)
	for _, msg := range messages {
		lower := strings.ToLower(msg.Content)
		for _, kw := range pendingKeywords {
			if strings.Contains(lower, kw) {
				truncated := truncateLine(msg.Content, 120)
				if !seen[truncated] {
					seen[truncated] = true
					pending = append(pending, truncated)
				}
				break
			}
		}
	}
	// Limit to last 5
	if len(pending) > 5 {
		pending = pending[len(pending)-5:]
	}
	return pending
}

// filePathRegex matches Unix-style file paths with known extensions.
var filePathRegex = regexp.MustCompile(`(?:/[\w.\-]+)+\.(?:` + strings.Join(func() []string {
	exts := make([]string, len(knownFileExtensions))
	for i, e := range knownFileExtensions {
		exts[i] = strings.TrimPrefix(e, ".")
	}
	return exts
}(), "|") + `)`)

// extractKeyFiles extracts unique file paths from messages.
func extractKeyFiles(messages []Message) []string {
	seen := make(map[string]bool)
	var files []string
	for _, msg := range messages {
		matches := filePathRegex.FindAllString(msg.Content, -1)
		for _, m := range matches {
			if !seen[m] {
				seen[m] = true
				files = append(files, m)
			}
		}
	}
	// Limit to 20 most recent unique files
	if len(files) > 20 {
		files = files[len(files)-20:]
	}
	return files
}

// truncateLine truncates a string to maxLen characters, appending "..." if needed.
func truncateLine(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
