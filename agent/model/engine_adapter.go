package model

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/xichan96/cortex/agent/types"
)

// EngineOptions tunes the adapter used by the existing agent engine.
type EngineOptions struct {
	MaxTokens      int
	Temperature    *float64
	Thinking       ThinkingLevel
	CacheRetention CacheRetention
}

// EngineProvider adapts a model.Provider to types.LLMProvider so the existing
// agent engine can consume the new model layer without changing its loop.
//
// The adapter is intentionally thin: it converts messages/tools, maps stream
// events, and reports model metadata. Retries and auth live in the provider.
type EngineProvider struct {
	provider Provider
	model    *Model
	opts     EngineOptions
}

// NewEngineProvider builds an engine-facing provider. model may be nil, in
// which case the provider's first catalog entry is used.
func NewEngineProvider(p Provider, m *Model, opts EngineOptions) (*EngineProvider, error) {
	if p == nil {
		return nil, fmt.Errorf("model: nil provider")
	}
	return &EngineProvider{provider: p, model: m, opts: opts}, nil
}

// Compile-time check.
var _ types.LLMProvider = (*EngineProvider)(nil)

func (e *EngineProvider) modelFor() (*Model, error) {
	if e.model != nil {
		return e.model, nil
	}
	models := e.provider.Models()
	if len(models) == 0 {
		return nil, fmt.Errorf("model: provider %q has no models", e.provider.ID())
	}
	return &models[0], nil
}

// Chat implements types.LLMProvider.
func (e *EngineProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	return e.ChatWithTools(ctx, messages, nil)
}

// ChatStream implements types.LLMProvider.
func (e *EngineProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	return e.ChatWithToolsStream(ctx, messages, nil)
}

// ChatWithTools implements types.LLMProvider.
func (e *EngineProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	stream, err := e.ChatWithToolsStream(ctx, messages, tools)
	if err != nil {
		return types.Message{}, err
	}
	var text, reasoning string
	var toolCalls []types.ToolCall
	var usage types.Usage
	for ev := range stream {
		switch ev.Type {
		case "chunk":
			text += ev.Content
		case "reasoning":
			reasoning += ev.Reasoning
		case "tool_calls":
			toolCalls = append(toolCalls, ev.ToolCalls...)
		case "end":
			if ev.Usage != nil {
				usage = *ev.Usage
			}
		case "error":
			return types.Message{}, fmt.Errorf("model: %s", ev.Error)
		}
	}
	return types.Message{
		Role:      "assistant",
		Content:   text,
		ToolCalls: toolCalls,
		Usage:     usage,
	}, nil
}

// ChatWithToolsStream implements types.LLMProvider.
func (e *EngineProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	m, err := e.modelFor()
	if err != nil {
		return nil, err
	}
	req, err := e.buildRequest(m, messages, tools)
	if err != nil {
		return nil, err
	}
	stream, err := e.provider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}

	out := make(chan types.StreamMessage, 16)
	go func() {
		defer close(out)
		defer stream.Close()

		sawText, sawTools := false, false
		for ev := range stream.Events() {
			switch ev.Type {
			case StreamTextDelta:
				sawText = true
				if !send(ctx, out, types.StreamMessage{Type: "chunk", Content: ev.Delta}) {
					return
				}
			case StreamThinkingDelta:
				if !send(ctx, out, types.StreamMessage{Type: "reasoning", Reasoning: ev.Delta}) {
					return
				}
			case StreamToolDelta:
				if ev.ToolCall != nil && ev.ToolCall.ArgumentsDelta != "" {
					if !send(ctx, out, types.StreamMessage{
						Type:       "tool_input_delta",
						Content:    ev.ToolCall.ArgumentsDelta,
						ToolCallID: ev.ToolCall.ID,
						ToolName:   ev.ToolCall.Name,
					}) {
						return
					}
				}
			case StreamToolEnd:
				sawTools = true
				if ev.ToolCall != nil {
					if !send(ctx, out, types.StreamMessage{
						Type:       "tool_calls",
						ToolCalls:  []types.ToolCall{toTypesToolCall(*ev.ToolCall)},
						ToolCallID: ev.ToolCall.ID,
						ToolName:   ev.ToolCall.Name,
					}) {
						return
					}
				}
			case StreamDone:
				if !sawText && ev.Message != nil && ev.Message.Text() != "" {
					if !send(ctx, out, types.StreamMessage{Type: "chunk", Content: ev.Message.Text()}) {
						return
					}
				}
				if !sawTools && ev.Message != nil {
					for _, tc := range ev.Message.ToolCalls() {
						if !send(ctx, out, types.StreamMessage{
							Type:      "tool_calls",
							ToolCalls: []types.ToolCall{toTypesToolCallFromCall(tc)},
						}) {
							return
						}
					}
				}
				usage := usageFromEvent(ev)
				if !send(ctx, out, types.StreamMessage{Type: "end", Usage: &usage}) {
					return
				}
				return
			case StreamError:
				msg := "model stream error"
				if ev.Err != nil {
					msg = ev.Err.Error()
				}
				send(ctx, out, types.StreamMessage{Type: "error", Error: msg})
				return
			}
		}
		// Provider closed without a terminal event: still close the engine turn.
		send(ctx, out, types.StreamMessage{Type: "end", Usage: &types.Usage{}})
	}()
	return out, nil
}

func send(ctx context.Context, ch chan<- types.StreamMessage, msg types.StreamMessage) bool {
	select {
	case ch <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

// GetModelName implements types.LLMProvider.
func (e *EngineProvider) GetModelName() string {
	if m, err := e.modelFor(); err == nil {
		return m.ID
	}
	return ""
}

// GetModelMetadata implements types.LLMProvider.
func (e *EngineProvider) GetModelMetadata() types.ModelMetadata {
	m, err := e.modelFor()
	if err != nil {
		return types.ModelMetadata{}
	}
	extra := map[string]any{
		"provider":       m.Provider,
		"api":            string(m.API),
		"context_window": m.ContextWindow,
		"reasoning":      m.Reasoning,
	}
	if len(m.Input) > 0 {
		extra["input"] = m.Input
	}
	return types.ModelMetadata{
		Name:      m.Name,
		Version:   m.ID,
		MaxTokens: m.MaxTokens,
		Extra:     extra,
	}
}

func (e *EngineProvider) buildRequest(m *Model, messages []types.Message, tools []types.Tool) (*Request, error) {
	var system []ContentBlock
	converted := make([]Message, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == "system" {
			system = append(system, ContentBlock{Type: ContentText, Text: msg.Content})
			continue
		}
		converted = append(converted, toModelMessage(msg))
	}
	decls := make([]ToolDeclaration, 0, len(tools))
	for _, tool := range tools {
		params, err := json.Marshal(tool.Schema())
		if err != nil {
			return nil, fmt.Errorf("model: marshal schema for %s: %w", tool.Name(), err)
		}
		decls = append(decls, ToolDeclaration{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  params,
		})
	}
	return &Request{
		Model:          m,
		System:         system,
		Messages:       converted,
		Tools:          decls,
		Thinking:       e.opts.Thinking,
		MaxTokens:      e.opts.MaxTokens,
		Temperature:    e.opts.Temperature,
		CacheRetention: e.opts.CacheRetention,
	}, nil
}

func toModelMessage(msg types.Message) Message {
	role := Role(msg.Role)
	switch role {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
	default:
		role = RoleUser
	}

	if role == RoleTool {
		return Message{
			Role: RoleTool,
			Content: []ContentBlock{{
				Type: ContentToolResult,
				ToolResult: &ToolResult{
					ToolCallID: msg.ToolCallID,
					ToolName:   msg.Name,
					Text:       msg.Content,
				},
			}},
		}
	}

	blocks := make([]ContentBlock, 0, len(msg.Parts)+len(msg.ToolCalls)+1)
	for _, part := range msg.Parts {
		switch p := part.(type) {
		case types.TextPart:
			if p.Text != "" {
				blocks = append(blocks, ContentBlock{Type: ContentText, Text: p.Text})
			}
		case types.ImageURLPart:
			blocks = append(blocks, ContentBlock{
				Type:  ContentImage,
				Image: &ImageContent{URL: p.URL},
			})
		case types.ImageDataPart:
			blocks = append(blocks, ContentBlock{
				Type: ContentImage,
				Image: &ImageContent{
					Data:     base64.StdEncoding.EncodeToString(p.Data),
					MIMEType: p.MIMEType,
				},
			})
		}
	}
	if len(blocks) == 0 && msg.Content != "" {
		blocks = append(blocks, ContentBlock{Type: ContentText, Text: msg.Content})
	}
	for _, call := range msg.ToolCalls {
		args := call.Function.Arguments
		if args == nil {
			args = map[string]any{}
		}
		blocks = append(blocks, ContentBlock{
			Type: ContentToolCall,
			ToolCall: &ToolCall{
				ID:        call.ID,
				Name:      call.Function.Name,
				Arguments: args,
			},
		})
	}
	return Message{Role: role, Content: blocks}
}

func toTypesToolCall(delta ToolCallDelta) types.ToolCall {
	args := delta.Arguments
	if args == nil {
		args = map[string]any{}
	}
	return types.ToolCall{
		ID:   delta.ID,
		Type: "function",
		Function: types.ToolFunction{
			Name:      delta.Name,
			Arguments: args,
		},
	}
}

func toTypesToolCallFromCall(call ToolCall) types.ToolCall {
	args := call.Arguments
	if args == nil {
		args = map[string]any{}
	}
	return types.ToolCall{
		ID:   call.ID,
		Type: "function",
		Function: types.ToolFunction{
			Name:      call.Name,
			Arguments: args,
		},
	}
}

func usageFromEvent(ev StreamEvent) types.Usage {
	if ev.Usage != nil {
		return toTypesUsage(*ev.Usage)
	}
	if ev.Message != nil {
		return toTypesUsage(ev.Message.Usage)
	}
	return types.Usage{}
}

func toTypesUsage(u Usage) types.Usage {
	return types.Usage{
		PromptTokens:        u.Input,
		CompletionTokens:    u.Output,
		TotalTokens:         u.TotalTokens,
		ReasoningTokens:     u.Reasoning,
		CachedTokens:        u.CacheRead,
		CacheCreationTokens: u.CacheWrite,
	}
}
