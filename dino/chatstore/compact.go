package chatstore

import (
	"regexp"
	"strconv"
	"strings"
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
// ~4 chars per token for ASCII, ~2 tokens per char for non-ASCII.
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
	nonASCII := len(text) - asciiCount
	return asciiCount/4 + nonASCII*2/4 + 1
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
