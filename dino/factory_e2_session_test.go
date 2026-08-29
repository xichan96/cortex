package dino

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// capturingLLMProvider records every tools slice passed to ChatWithTools so a
// test can assert what the model sees across turns.
type capturingLLMProvider struct {
	mu        sync.Mutex
	responses []string
	idx       int
	// toolsSeen[i] = tools passed to ChatWithTools on turn i.
	toolsSeen [][]string
	// emitToolSearch on turn N (1-based) makes the provider return a
	// tool_search call before the final plain response.
	emitToolSearch bool
}

func (m *capturingLLMProvider) Chat(ctx context.Context, messages []types.Message) (types.Message, error) {
	return m.next("")
}

func (m *capturingLLMProvider) ChatWithTools(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	m.mu.Lock()
	m.toolsSeen = append(m.toolsSeen, names)
	emit := m.emitToolSearch
	m.mu.Unlock()

	if emit && len(m.toolsSeen) == 1 {
		// First turn: ask the model to call tool_search.
		resp := types.Message{
			Content: "searching",
			ToolCalls: []types.ToolCall{
				{ID: "call_1", Type: "function", Function: types.ToolFunction{
					Name:      "tool_search",
					Arguments: map[string]interface{}{"query": "deferred"},
				}},
			},
			Usage: types.Usage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20},
		}
		return resp, nil
	}
	return m.next("done")
}

func (m *capturingLLMProvider) ChatStream(ctx context.Context, messages []types.Message) (<-chan types.StreamMessage, error) {
	ch := make(chan types.StreamMessage, 1)
	ch <- types.StreamMessage{Content: "done"}
	close(ch)
	return ch, nil
}

func (m *capturingLLMProvider) ChatWithToolsStream(ctx context.Context, messages []types.Message, tools []types.Tool) (<-chan types.StreamMessage, error) {
	m.recordTurn(tools)
	ch := make(chan types.StreamMessage, 2)
	select {
	case <-ctx.Done():
		return ch, ctx.Err()
	default:
	}
	emit := false
	m.mu.Lock()
	emit = m.emitToolSearch && len(m.toolsSeen) == 1
	m.mu.Unlock()
	if emit {
		// First streaming turn: request tool_search. Type must be "tool_calls"
		// for executeStreamIteration to route them into tool execution.
		ch <- types.StreamMessage{
			Type:    "tool_calls",
			Content: "searching",
			ToolCalls: []types.ToolCall{
				{ID: "call_s1", Type: "function", Function: types.ToolFunction{
					Name:      "tool_search",
					Arguments: map[string]interface{}{"query": "deferred"},
				}},
			},
			Usage: &types.Usage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20},
		}
	} else {
		ch <- types.StreamMessage{Content: "done", Usage: &types.Usage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20}}
	}
	close(ch)
	return ch, nil
}

// recordTurn appends the tool names of the given turn (used by the streaming
// path; the non-streaming path records inside ChatWithTools).
func (m *capturingLLMProvider) recordTurn(tools []types.Tool) {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	m.mu.Lock()
	m.toolsSeen = append(m.toolsSeen, names)
	m.mu.Unlock()
}

func (m *capturingLLMProvider) GetModelName() string { return "capture" }
func (m *capturingLLMProvider) GetModelMetadata() types.ModelMetadata {
	return types.ModelMetadata{Name: "capture", MaxTokens: 4096}
}

func (m *capturingLLMProvider) next(content string) (types.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := m.idx
	m.idx++
	if idx < len(m.responses) {
		content = m.responses[idx]
	}
	return types.Message{
		Content: content,
		Usage:   types.Usage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20},
	}, nil
}

func (m *capturingLLMProvider) toolsOnTurn(turn int) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if turn < 1 || turn > len(m.toolsSeen) {
		return nil
	}
	return m.toolsSeen[turn-1]
}

// registerCaptureProvider registers the given provider under a unique name and
// returns a factory built with it.
func newCaptureFactory(provider *capturingLLMProvider, extraAllowed ...string) (DinoFactory, error) {
	name := fmt.Sprintf("e2-capture-%d", time.Now().UnixNano())
	RegisterLLMProvider(name, func(cfg *Config) (types.LLMProvider, error) {
		return provider, nil
	})
	cfg := DefaultConfig()
	cfg.Provider.Type = name
	// Disable long-term memory and subagent wiring for a lean session.
	cfg.LongTermMemory.Enabled = false
	// Deferred tools must be allowed or they are dropped at pre-wrap time.
	cfg.Tools.Allowed = append(cfg.Tools.Allowed, extraAllowed...)
	return NewDinoFactory(cfg)
}

// TestE2_ToolSearch_EndToEnd wires a Deferred tool into the factory registry,
// creates a session, lets the mock model call tool_search, and asserts the tool
// becomes visible on the next turn while it was absent from the initial list.
func TestE2_ToolSearch_EndToEnd(t *testing.T) {
	provider := &capturingLLMProvider{responses: []string{"plain answer"}, emitToolSearch: true}
	factory, err := newCaptureFactory(provider, "deferred_alpha")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	df := factory.(*dinoFactory)
	if err := df.tools.Register(&deferredMockTool{name: "deferred_alpha"}); err != nil {
		t.Fatalf("register deferred tool: %v", err)
	}

	ctx := context.Background()
	sess, err := factory.CreateSession(ctx, "e2-session")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer sess.Close()

	// Drive one turn; the mock emits tool_search, engine runs it (injecting
	// deferred_alpha), then the next ChatWithTools sees it.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Output() {
			if ev.Type == EventTypeDone {
				return
			}
		}
	}()
	sess.Input() <- "find me a deferred tool"

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("session did not finish")
	}

	// Initial tool list must NOT include the deferred tool.
	first := provider.toolsOnTurn(1)
	for _, n := range first {
		if n == "deferred_alpha" {
			t.Fatal("deferred tool must not appear in the initial tool list")
		}
	}
	foundSearch := false
	for _, n := range first {
		if n == "tool_search" {
			foundSearch = true
		}
	}
	if !foundSearch {
		t.Fatalf("initial tool list must include tool_search, got %v", first)
	}

	// After discovery, the tool is in the engine and the deferred cache is drained.
	df.discoverMu.Lock()
	_, stillDeferred := df.sessionDeferredTools["e2-session"]["deferred_alpha"]
	discovered := df.sessionDiscoveredTools["e2-session"]
	df.discoverMu.Unlock()
	if stillDeferred {
		t.Fatal("deferred_alpha must be removed from deferred cache after discovery")
	}
	if len(discovered) != 1 || discovered[0] != "deferred_alpha" {
		t.Fatalf("discovered: want [deferred_alpha], got %v", discovered)
	}
}

// TestE2_ToolSearch_Disabled no-op path: ToolSearchEnabled=false must not add the
// tool_search tool to the visible list.
func TestE2_ToolSearch_Disabled(t *testing.T) {
	provider := &capturingLLMProvider{responses: []string{"plain answer"}}
	name := fmt.Sprintf("e2-disable-%d", time.Now().UnixNano())
	RegisterLLMProvider(name, func(cfg *Config) (types.LLMProvider, error) {
		return provider, nil
	})
	cfg := DefaultConfig()
	cfg.Provider.Type = name
	cfg.LongTermMemory.Enabled = false
	cfg.Tools.ToolSearchEnabled = false
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	ctx := context.Background()
	sess, err := factory.CreateSession(ctx, "e2-disabled-session")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer sess.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Output() {
			if ev.Type == EventTypeDone {
				return
			}
		}
	}()
	sess.Input() <- "hello"

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("session did not finish")
	}

	first := provider.toolsOnTurn(1)
	for _, n := range first {
		if n == "tool_search" {
			t.Fatal("tool_search must not appear when ToolSearchEnabled=false")
		}
	}
}
