package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/xichan96/cortex/agent/providers"
	"github.com/xichan96/cortex/agent/types"
)

const (
	anthropicVersion     = "2023-06-01"
	anthropicDefaultURL  = "https://api.anthropic.com"
	anthropicMessagesPath = "/v1/messages"
	anthropicMaxTokens   = 8096
	// sseMaxLineBytes is the max expected SSE line size; 4MB is generous.
	sseMaxLineBytes = 4 * 1024 * 1024
)

// ---- Request types --------------------------------------------------------

// anthropicCacheControl marks a content block as a prompt-cache breakpoint.
// Anthropic enforces a hard cap of types.MaxAnthropicCacheBreakpoints per request.
type anthropicCacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

func ephemeralCacheControl() *anthropicCacheControl {
	return &anthropicCacheControl{Type: "ephemeral"}
}

type anthropicRequest struct {
	Model     string              `json:"model"`
	MaxTokens int                 `json:"max_tokens"`
	// System is a string (legacy) or []anthropicContentBlock when prompt
	// caching places a breakpoint on the system block. Anthropic accepts both.
	System    interface{}         `json:"system,omitempty"`
	Messages  []anthropicMessage  `json:"messages"`
	Tools     []anthropicTool     `json:"tools,omitempty"`
	Stream    bool                `json:"stream"`
}

type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []anthropicContentBlock
}

type anthropicContentBlock struct {
	Type         string               `json:"type"`
	Text         string               `json:"text,omitempty"`
	ID           string               `json:"id,omitempty"`
	Name         string               `json:"name,omitempty"`
	Input        json.RawMessage      `json:"input,omitempty"`
	ToolUseID    string               `json:"tool_use_id,omitempty"`
	Content      string               `json:"content,omitempty"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicTool struct {
	Name         string               `json:"name"`
	Description  string               `json:"description"`
	InputSchema  interface{}          `json:"input_schema"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

// ---- SSE event types -------------------------------------------------------

type anthropicDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

type anthropicContentBlockStartData struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type  string `json:"type"`
		ID    string `json:"id,omitempty"`
		Name  string `json:"name,omitempty"`
		Text  string `json:"text,omitempty"`
	} `json:"content_block"`
}

type anthropicContentBlockDeltaData struct {
	Type  string         `json:"type"`
	Index int            `json:"index"`
	Delta anthropicDelta `json:"delta"`
}

type anthropicMessageDeltaData struct {
	Type  string         `json:"type"`
	Delta anthropicDelta `json:"delta"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthropicMessageStartData struct {
	Type    string `json:"type"`
	Message struct {
		Usage struct {
			InputTokens             int `json:"input_tokens"`
			OutputTokens            int `json:"output_tokens"`
			CacheReadInputTokens    int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type anthropicErrorData struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// ---- Native Anthropic LLMProvider -----------------------------------------

// NativeAnthropicProvider implements LLMProvider using the Anthropic /v1/messages API directly.
// This avoids langchaingo's OpenAI-compat layer and its bufio.Scanner 64KB line limit.
type NativeAnthropicProvider struct {
	apiKey    string
	baseURL   string
	model     string
	maxTokens int
	client    *http.Client
	promptCache types.PromptCacheOptions
}

func newNativeAnthropicProvider(apiKey, baseURL, model string) *NativeAnthropicProvider {
	if baseURL == "" {
		baseURL = anthropicDefaultURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if model == "" {
		model = ClaudeSonnet4.String()
	}
	return &NativeAnthropicProvider{
		apiKey:      apiKey,
		baseURL:     baseURL,
		model:       model,
		maxTokens:   anthropicMaxTokens,
		client:      providers.GetPooledHTTPClient(),
		promptCache: types.DefaultPromptCacheOptions(),
	}
}

// SetPromptCacheOptions implements types.PromptCacheConfigurer. The engine
// calls this during NewAgentEngine (and SetConfig) to enable/disable prompt
// cache breakpoint injection. Defaults (types.DefaultPromptCacheOptions) are on.
func (p *NativeAnthropicProvider) SetPromptCacheOptions(opts types.PromptCacheOptions) {
	p.promptCache = opts
}

func (p *NativeAnthropicProvider) GetModelName() string { return p.model }
func (p *NativeAnthropicProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: p.model, Version: anthropicVersion, MaxTokens: p.maxTokens}
}

// Chat sends a synchronous request (non-streaming).
func (p *NativeAnthropicProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	ch, err := p.ChatStream(ctx, messages)
	if err != nil {
		return types.Message{}, err
	}
	return collectStream(ch)
}

// ChatWithTools sends a synchronous request with tool definitions.
func (p *NativeAnthropicProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	ch, err := p.ChatWithToolsStream(ctx, messages, tools)
	if err != nil {
		return types.Message{}, err
	}
	return collectStream(ch)
}

// ChatStream starts a streaming request without tools.
func (p *NativeAnthropicProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	return p.stream(ctx, messages, nil)
}

// ChatWithToolsStream starts a streaming request with tool definitions.
func (p *NativeAnthropicProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	return p.stream(ctx, messages, tools)
}

// stream is the core streaming implementation.
func (p *NativeAnthropicProvider) stream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	body, err := p.buildRequest(messages, tools, true)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+anthropicMessagesPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic API error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	out := make(chan types.StreamMessage, 64)
	go p.readStream(resp.Body, out)
	return out, nil
}

// readStream reads the SSE stream and writes to out channel.
func (p *NativeAnthropicProvider) readStream(body io.ReadCloser, out chan<- types.StreamMessage) {
	defer close(out)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, sseMaxLineBytes), sseMaxLineBytes)

	// State for accumulating tool call inputs across input_json_delta events.
	type toolState struct {
		id   string
		name string
		buf  strings.Builder
	}
	toolBlocks := map[int]*toolState{}
	var usage types.Usage
	var inputTokens int
	// B1: Anthropic `input_tokens` is the *uncached remainder* only. The total
	// prompt size is input_tokens + cache_read_input_tokens + cache_creation_input_tokens.
	var cacheReadTokens, cacheCreationTokens int

	var currentEvent string

	send := func(m types.StreamMessage) {
		out <- m
	}

	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// Blank line: end of SSE event — reset event type.
			currentEvent = ""
			continue
		}

		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}

		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		switch currentEvent {
		case "message_start":
			var ev anthropicMessageStartData
			if json.Unmarshal([]byte(data), &ev) == nil {
				inputTokens = ev.Message.Usage.InputTokens
				cacheReadTokens = ev.Message.Usage.CacheReadInputTokens
				cacheCreationTokens = ev.Message.Usage.CacheCreationInputTokens
			}

		case "content_block_start":
			var ev anthropicContentBlockStartData
			if json.Unmarshal([]byte(data), &ev) == nil && ev.ContentBlock.Type == "tool_use" {
				toolBlocks[ev.Index] = &toolState{
					id:   ev.ContentBlock.ID,
					name: ev.ContentBlock.Name,
				}
			}

		case "content_block_delta":
			var ev anthropicContentBlockDeltaData
			if json.Unmarshal([]byte(data), &ev) != nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					send(types.StreamMessage{Type: "chunk", Content: ev.Delta.Text})
				}
			case "input_json_delta":
				if ts, ok := toolBlocks[ev.Index]; ok {
					ts.buf.WriteString(ev.Delta.PartialJSON)
				}
			case "thinking_delta":
				if ev.Delta.Text != "" {
					send(types.StreamMessage{Type: "reasoning", Reasoning: ev.Delta.Text})
				}
			}

		case "content_block_stop":
			// Check if this block was a tool_use block.
			var ev struct {
				Index int `json:"index"`
			}
			if json.Unmarshal([]byte(data), &ev) == nil {
				if ts, ok := toolBlocks[ev.Index]; ok {
					// Parse the accumulated JSON input.
					var args map[string]interface{}
					rawInput := ts.buf.String()
					if rawInput == "" {
						rawInput = "{}"
					}
					if err := json.Unmarshal([]byte(rawInput), &args); err != nil {
						args = map[string]interface{}{"_raw": rawInput}
					}
					send(types.StreamMessage{
						Type: "tool_calls",
						ToolCalls: []types.ToolCall{{
							ID:   ts.id,
							Type: "function",
							Function: types.ToolFunction{
								Name:      ts.name,
								Arguments: args,
							},
						}},
					})
					delete(toolBlocks, ev.Index)
				}
			}

		case "message_delta":
			var ev anthropicMessageDeltaData
			if json.Unmarshal([]byte(data), &ev) == nil {
				usage.CompletionTokens = ev.Usage.OutputTokens
				// B1: PromptTokens/TotalTokens reflect total input (uncached + cache read + cache write).
				usage.PromptTokens = inputTokens + cacheReadTokens + cacheCreationTokens
				usage.TotalTokens = usage.PromptTokens + ev.Usage.OutputTokens
				usage.CachedTokens = cacheReadTokens
				usage.CacheCreationTokens = cacheCreationTokens
			}

		case "message_stop":
			send(types.StreamMessage{Type: "end", Usage: &usage})
			return

		case "error":
			var ev anthropicErrorData
			if json.Unmarshal([]byte(data), &ev) == nil {
				send(types.StreamMessage{Type: "error", Error: ev.Error.Message})
			} else {
				send(types.StreamMessage{Type: "error", Error: data})
			}
			return
		}
	}

	if err := scanner.Err(); err != nil {
		send(types.StreamMessage{Type: "error", Error: err.Error()})
		return
	}

	// Stream ended without explicit message_stop — still send end.
	send(types.StreamMessage{Type: "end", Usage: &usage})
}

// buildRequest converts cortex types to an Anthropic API request body.
func (p *NativeAnthropicProvider) buildRequest(messages []types.Message, tools []types.Tool, stream bool) ([]byte, error) {
	var system string
	var anthropicMsgs []anthropicMessage

	for _, m := range messages {
		if m.Role == "system" {
			if system == "" {
				system = m.Content
			} else {
				system += "\n" + m.Content
			}
			continue
		}

		role := m.Role
		if role == "user" || role == "human" {
			role = "user"
		} else if role == "assistant" || role == "ai" {
			role = "assistant"
		} else if role == "tool" {
			role = "user" // tool results go as user messages in Anthropic format
		}

		// Build content blocks.
		if role == "user" && m.ToolCallID != "" {
			// Tool result.
			result := m.Content
			if strings.TrimSpace(result) == "" {
				result = types.ToolEmptyResultMessage
			}
			block := anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   result,
			}
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{
				Role:    "user",
				Content: []anthropicContentBlock{block},
			})
			continue
		}

		if role == "assistant" && len(m.ToolCalls) > 0 {
			// Assistant message with tool calls.
			var blocks []anthropicContentBlock
			if m.Content != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				rawArgs, _ := json.Marshal(tc.Function.Arguments)
				blocks = append(blocks, anthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: json.RawMessage(rawArgs),
				})
			}
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{Role: "assistant", Content: blocks})
			continue
		}

		// Build parts (multimodal or text).
		if len(m.Parts) > 0 {
			var blocks []anthropicContentBlock
			for _, part := range m.Parts {
				switch pt := part.(type) {
				case types.TextPart:
					blocks = append(blocks, anthropicContentBlock{Type: "text", Text: pt.Text})
				case types.ImageURLPart:
					// For image URLs, wrap in a source block (simplified).
					blocks = append(blocks, anthropicContentBlock{Type: "text", Text: "[image: " + pt.URL + "]"})
				case types.ImageDataPart:
					// Embed base64 image.
					blocks = append(blocks, anthropicContentBlock{Type: "text", Text: fmt.Sprintf("[image data: %s]", pt.MIMEType)})
				}
			}
			if len(blocks) == 0 {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
			}
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{Role: role, Content: blocks})
		} else {
			content := m.Content
			if content == "" {
				content = " " // Anthropic requires non-empty content
			}
			anthropicMsgs = append(anthropicMsgs, anthropicMessage{Role: role, Content: content})
		}
	}

	// Merge consecutive same-role messages (Anthropic requires alternating user/assistant).
	anthropicMsgs = mergeConsecutiveRoles(anthropicMsgs)

	var anthropicTools []anthropicTool
	for _, t := range tools {
		anthropicTools = append(anthropicTools, anthropicTool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: sanitizeToolSchema(t.Schema()),
		})
	}

	req := anthropicRequest{
		Model:     p.model,
		MaxTokens: p.maxTokens,
		System:    system,
		Messages:  anthropicMsgs,
		Tools:     anthropicTools,
		Stream:    stream,
	}

	// B2 (review): inject cache_control breakpoints with a hard budget of
	// MaxAnthropicCacheBreakpoints. When disabled the request body must stay
	// byte-identical to pre-caching output.
	if p.promptCache.Enabled {
		req = p.applyCacheControl(req)
	}

	return json.Marshal(req)
}

// applyCacheControl injects cache_control breakpoints into a request using the
// budget-based layout (review B2/R1), which never exceeds
// MaxAnthropicCacheBreakpoints per request:
//
//  1. system block           — 1 breakpoint (if SystemBreakpoint && system != "")
//  2. last tool              — 1 breakpoint (if ToolsBreakpoint && len(tools) > 0)
//  3. history messages       — up to HistoryEveryN breakpoints (capped so total ≤ 4)
//
// Budgeting: history may only claim (4 - alreadyUsed) breakpoints. If history
// would exceed its share, we degrade by dropping history breakpoints rather
// than risk an HTTP 400.
func (p *NativeAnthropicProvider) applyCacheControl(req anthropicRequest) anthropicRequest {
	pc := p.promptCache
	used := 0

	// 1) System block breakpoint. system is a plain string at this point; wrap
	// it in a content block array so a breakpoint can attach (Anthropic accepts
	// either form).
	if pc.SystemBreakpoint && req.System != nil && req.System != "" {
		req.System = []anthropicContentBlock{{
			Type:         "text",
			Text:         req.System.(string),
			CacheControl: ephemeralCacheControl(),
		}}
		used++
	}

	// 2) Last tool breakpoint. Tools render at position 0 and are the most
	// stable segment, so a single breakpoint here is the highest-value anchor.
	if pc.ToolsBreakpoint && len(req.Tools) > 0 {
		req.Tools[len(req.Tools)-1].CacheControl = ephemeralCacheControl()
		used++
	}

	// 3) History breakpoints. Budget = min(HistoryEveryN, 4-used). We scan
	// assistant messages from the end and attach a breakpoint to the last block
	// of the chosen ones. Only the most recent stable assistant blocks matter:
	// a breakpoint on an older message lets the prefix up to it stay cached even
	// as new tool turns append.
	historyBudget := pc.HistoryEveryN
	if remaining := types.MaxAnthropicCacheBreakpoints - used; historyBudget > remaining {
		historyBudget = remaining
	}
	if historyBudget > 0 {
		req.Messages = p.markHistoryBreakpoints(req.Messages, historyBudget, pc.MinCacheTokens)
	}

	return req
}

// markHistoryBreakpoints places up to budget breakpoints on assistant messages
// (from the end backwards), each on the message's last content block. Messages
// must already be in block-array form (mergeConsecutiveRoles guarantees this).
// If the estimated history segment is smaller than MinCacheTokens, no
// breakpoint is placed (the write would silently not cache).
func (p *NativeAnthropicProvider) markHistoryBreakpoints(msgs []anthropicMessage, budget int, minTokens int) []anthropicMessage {
	if budget <= 0 || len(msgs) == 0 {
		return msgs
	}
	if minTokens > 0 {
		if est := estimateHistoryTokens(msgs); est < minTokens {
			return msgs
		}
	}

	placed := 0
	for i := len(msgs) - 1; i >= 0 && placed < budget; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		blocks, ok := msgs[i].Content.([]anthropicContentBlock)
		if !ok || len(blocks) == 0 {
			continue
		}
		// Attach to the last block. tool_use / text blocks may carry a
		// breakpoint; never attach to tool_result (it appears only on user
		// messages anyway, but guard for safety).
		last := &blocks[len(blocks)-1]
		if last.Type == "tool_result" {
			continue
		}
		if last.CacheControl != nil {
			continue
		}
		last.CacheControl = ephemeralCacheControl()
		msgs[i].Content = blocks
		placed++
	}
	return msgs
}

// estimateHistoryTokens gives a cheap length-based estimate of the message
// segment size (chars/4 ≈ tokens), used only to gate whether history
// breakpoints are worth writing (min prefix threshold).
func estimateHistoryTokens(msgs []anthropicMessage) int {
	total := 0
	for _, m := range msgs {
		switch v := m.Content.(type) {
		case string:
			total += len(v)
		case []anthropicContentBlock:
			for _, b := range v {
				total += len(b.Text) + len(b.Content)
				if b.Input != nil {
					total += len(b.Input)
				}
			}
		}
	}
	return total / 4
}

// mergeConsecutiveRoles merges consecutive messages with the same role.
// Anthropic requires alternating user/assistant messages.
func mergeConsecutiveRoles(msgs []anthropicMessage) []anthropicMessage {
	if len(msgs) == 0 {
		return msgs
	}
	result := make([]anthropicMessage, 0, len(msgs))
	result = append(result, msgs[0])

	for i := 1; i < len(msgs); i++ {
		prev := &result[len(result)-1]
		cur := msgs[i]
		if prev.Role == cur.Role {
			// Merge: convert both to block slices if needed.
			prevBlocks := toBlocks(prev.Content)
			curBlocks := toBlocks(cur.Content)
			prev.Content = append(prevBlocks, curBlocks...)
		} else {
			result = append(result, cur)
		}
	}
	return result
}

// sanitizeToolSchema recursively ensures all array-type properties have an "items" field,
// which is required by the Anthropic API.
func sanitizeToolSchema(schema interface{}) interface{} {
	m, ok := schema.(map[string]interface{})
	if !ok {
		return schema
	}
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = v
	}
	if t, ok := result["type"].(string); ok && t == "array" {
		if _, hasItems := result["items"]; !hasItems {
			result["items"] = map[string]interface{}{}
		}
	}
	if props, ok := result["properties"].(map[string]interface{}); ok {
		newProps := make(map[string]interface{}, len(props))
		for pk, pv := range props {
			newProps[pk] = sanitizeToolSchema(pv)
		}
		result["properties"] = newProps
	}
	if items, ok := result["items"]; ok {
		result["items"] = sanitizeToolSchema(items)
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := result[key].([]interface{}); ok {
			newArr := make([]interface{}, len(arr))
			for i, item := range arr {
				newArr[i] = sanitizeToolSchema(item)
			}
			result[key] = newArr
		}
	}
	return result
}

func toBlocks(content interface{}) []anthropicContentBlock {
	switch v := content.(type) {
	case string:
		return []anthropicContentBlock{{Type: "text", Text: v}}
	case []anthropicContentBlock:
		return v
	default:
		return nil
	}
}

// collectStream drains a StreamMessage channel into a single Message.
func collectStream(ch <-chan types.StreamMessage) (types.Message, error) {
	var sb strings.Builder
	var toolCalls []types.ToolCall
	var usage types.Usage

	for msg := range ch {
		switch msg.Type {
		case "chunk":
			sb.WriteString(msg.Content)
		case "tool_calls":
			toolCalls = append(toolCalls, msg.ToolCalls...)
		case "end":
			if msg.Usage != nil {
				usage = *msg.Usage
			}
		case "error":
			return types.Message{}, fmt.Errorf("%s", msg.Error)
		}
	}

	return types.Message{
		Role:      "assistant",
		Content:   sb.String(),
		ToolCalls: toolCalls,
		Usage:     usage,
	}, nil
}
