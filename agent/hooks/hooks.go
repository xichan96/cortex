package hooks

import (
	"context"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// HookContext holds context information for hook execution
type HookContext struct {
	AgentID   string
	SessionID string
	Iteration int
	Timestamp time.Time
	Metadata  map[string]interface{}
}

// HookFunc is a function that can be registered as a hook
type HookFunc func(ctx context.Context, hc *HookContext, data interface{}) (interface{}, error)

// Hooks defines lifecycle hooks for agent execution
type Hooks interface {
	// Before execution hooks
	OnBeforeStart(ctx context.Context, hc *HookContext, input *types.AgentInput) error
	OnBeforeIteration(ctx context.Context, hc *HookContext, iteration int) error
	OnBeforeLLMCall(ctx context.Context, hc *HookContext, messages []types.Message) error
	OnBeforeToolCall(ctx context.Context, hc *HookContext, toolName string, input map[string]interface{}) error

	// After execution hooks
	OnAfterToolCall(ctx context.Context, hc *HookContext, toolName string, output interface{}, err error) error
	OnAfterLLMCall(ctx context.Context, hc *HookContext, response *types.Message) error
	OnAfterIteration(ctx context.Context, hc *HookContext, iteration int, result *types.AgentResult) error
	OnAfterEnd(ctx context.Context, hc *HookContext, finalResult *types.AgentResult) error

	// Error hooks
	OnError(ctx context.Context, hc *HookContext, err error) error
}

// HooksFunc is a functional adapter for Hooks
type HooksFunc struct {
	onBeforeStart     func(ctx context.Context, hc *HookContext, input *types.AgentInput) error
	onBeforeIteration func(ctx context.Context, hc *HookContext, iteration int) error
	onBeforeLLMCall   func(ctx context.Context, hc *HookContext, messages []types.Message) error
	onBeforeToolCall  func(ctx context.Context, hc *HookContext, toolName string, input map[string]interface{}) error
	onAfterToolCall   func(ctx context.Context, hc *HookContext, toolName string, output interface{}, err error) error
	onAfterLLMCall    func(ctx context.Context, hc *HookContext, response *types.Message) error
	onAfterIteration  func(ctx context.Context, hc *HookContext, iteration int, result *types.AgentResult) error
	onAfterEnd        func(ctx context.Context, hc *HookContext, finalResult *types.AgentResult) error
	onError           func(ctx context.Context, hc *HookContext, err error) error
}

func (h *HooksFunc) OnBeforeStart(ctx context.Context, hc *HookContext, input *types.AgentInput) error {
	if h.onBeforeStart != nil {
		return h.onBeforeStart(ctx, hc, input)
	}
	return nil
}

func (h *HooksFunc) OnBeforeIteration(ctx context.Context, hc *HookContext, iteration int) error {
	if h.onBeforeIteration != nil {
		return h.onBeforeIteration(ctx, hc, iteration)
	}
	return nil
}

func (h *HooksFunc) OnBeforeLLMCall(ctx context.Context, hc *HookContext, messages []types.Message) error {
	if h.onBeforeLLMCall != nil {
		return h.onBeforeLLMCall(ctx, hc, messages)
	}
	return nil
}

func (h *HooksFunc) OnBeforeToolCall(ctx context.Context, hc *HookContext, toolName string, input map[string]interface{}) error {
	if h.onBeforeToolCall != nil {
		return h.onBeforeToolCall(ctx, hc, toolName, input)
	}
	return nil
}

func (h *HooksFunc) OnAfterToolCall(ctx context.Context, hc *HookContext, toolName string, output interface{}, err error) error {
	if h.onAfterToolCall != nil {
		return h.onAfterToolCall(ctx, hc, toolName, output, err)
	}
	return nil
}

func (h *HooksFunc) OnAfterLLMCall(ctx context.Context, hc *HookContext, response *types.Message) error {
	if h.onAfterLLMCall != nil {
		return h.onAfterLLMCall(ctx, hc, response)
	}
	return nil
}

func (h *HooksFunc) OnAfterIteration(ctx context.Context, hc *HookContext, iteration int, result *types.AgentResult) error {
	if h.onAfterIteration != nil {
		return h.onAfterIteration(ctx, hc, iteration, result)
	}
	return nil
}

func (h *HooksFunc) OnAfterEnd(ctx context.Context, hc *HookContext, finalResult *types.AgentResult) error {
	if h.onAfterEnd != nil {
		return h.onAfterEnd(ctx, hc, finalResult)
	}
	return nil
}

func (h *HooksFunc) OnError(ctx context.Context, hc *HookContext, err error) error {
	if h.onError != nil {
		return h.onError(ctx, hc, err)
	}
	return nil
}

// NewHooksFunc creates a new HooksFunc with the given functions
func NewHooksFunc(
	onBeforeStart func(ctx context.Context, hc *HookContext, input *types.AgentInput) error,
	onBeforeIteration func(ctx context.Context, hc *HookContext, iteration int) error,
	onBeforeLLMCall func(ctx context.Context, hc *HookContext, messages []types.Message) error,
	onBeforeToolCall func(ctx context.Context, hc *HookContext, toolName string, input map[string]interface{}) error,
	onAfterToolCall func(ctx context.Context, hc *HookContext, toolName string, output interface{}, err error) error,
	onAfterLLMCall func(ctx context.Context, hc *HookContext, response *types.Message) error,
	onAfterIteration func(ctx context.Context, hc *HookContext, iteration int, result *types.AgentResult) error,
	onAfterEnd func(ctx context.Context, hc *HookContext, finalResult *types.AgentResult) error,
	onError func(ctx context.Context, hc *HookContext, err error) error,
) *HooksFunc {
	return &HooksFunc{
		onBeforeStart:     onBeforeStart,
		onBeforeIteration: onBeforeIteration,
		onBeforeLLMCall:   onBeforeLLMCall,
		onBeforeToolCall:  onBeforeToolCall,
		onAfterToolCall:   onAfterToolCall,
		onAfterLLMCall:    onAfterLLMCall,
		onAfterIteration:  onAfterIteration,
		onAfterEnd:        onAfterEnd,
		onError:           onError,
	}
}

// ChainHooks chains multiple hooks together
type ChainHooks struct {
	hooks []Hooks
}

func NewChainHooks(hooks ...Hooks) *ChainHooks {
	return &ChainHooks{hooks: hooks}
}

func (c *ChainHooks) Add(hook Hooks) {
	c.hooks = append(c.hooks, hook)
}

func (c *ChainHooks) OnBeforeStart(ctx context.Context, hc *HookContext, input *types.AgentInput) error {
	for _, h := range c.hooks {
		if err := h.OnBeforeStart(ctx, hc, input); err != nil {
			return err
		}
	}
	return nil
}

func (c *ChainHooks) OnBeforeIteration(ctx context.Context, hc *HookContext, iteration int) error {
	for _, h := range c.hooks {
		if err := h.OnBeforeIteration(ctx, hc, iteration); err != nil {
			return err
		}
	}
	return nil
}

func (c *ChainHooks) OnBeforeLLMCall(ctx context.Context, hc *HookContext, messages []types.Message) error {
	for _, h := range c.hooks {
		if err := h.OnBeforeLLMCall(ctx, hc, messages); err != nil {
			return err
		}
	}
	return nil
}

func (c *ChainHooks) OnBeforeToolCall(ctx context.Context, hc *HookContext, toolName string, input map[string]interface{}) error {
	for _, h := range c.hooks {
		if err := h.OnBeforeToolCall(ctx, hc, toolName, input); err != nil {
			return err
		}
	}
	return nil
}

func (c *ChainHooks) OnAfterToolCall(ctx context.Context, hc *HookContext, toolName string, output interface{}, err error) error {
	for _, h := range c.hooks {
		if err := h.OnAfterToolCall(ctx, hc, toolName, output, err); err != nil {
			return err
		}
	}
	return nil
}

func (c *ChainHooks) OnAfterLLMCall(ctx context.Context, hc *HookContext, response *types.Message) error {
	for _, h := range c.hooks {
		if err := h.OnAfterLLMCall(ctx, hc, response); err != nil {
			return err
		}
	}
	return nil
}

func (c *ChainHooks) OnAfterIteration(ctx context.Context, hc *HookContext, iteration int, result *types.AgentResult) error {
	for _, h := range c.hooks {
		if err := h.OnAfterIteration(ctx, hc, iteration, result); err != nil {
			return err
		}
	}
	return nil
}

func (c *ChainHooks) OnAfterEnd(ctx context.Context, hc *HookContext, finalResult *types.AgentResult) error {
	for _, h := range c.hooks {
		if err := h.OnAfterEnd(ctx, hc, finalResult); err != nil {
			return err
		}
	}
	return nil
}

func (c *ChainHooks) OnError(ctx context.Context, hc *HookContext, err error) error {
	for _, h := range c.hooks {
		if err := h.OnError(ctx, hc, err); err != nil {
			return err
		}
	}
	return nil
}

// NoOpHooks is a no-op implementation of Hooks
type NoOpHooks struct{}

func (NoOpHooks) OnBeforeStart(ctx context.Context, hc *HookContext, input *types.AgentInput) error {
	return nil
}
func (NoOpHooks) OnBeforeIteration(ctx context.Context, hc *HookContext, iteration int) error {
	return nil
}
func (NoOpHooks) OnBeforeLLMCall(ctx context.Context, hc *HookContext, messages []types.Message) error {
	return nil
}
func (NoOpHooks) OnBeforeToolCall(ctx context.Context, hc *HookContext, toolName string, input map[string]interface{}) error {
	return nil
}
func (NoOpHooks) OnAfterToolCall(ctx context.Context, hc *HookContext, toolName string, output interface{}, err error) error {
	return nil
}
func (NoOpHooks) OnAfterLLMCall(ctx context.Context, hc *HookContext, response *types.Message) error {
	return nil
}
func (NoOpHooks) OnAfterIteration(ctx context.Context, hc *HookContext, iteration int, result *types.AgentResult) error {
	return nil
}
func (NoOpHooks) OnAfterEnd(ctx context.Context, hc *HookContext, finalResult *types.AgentResult) error {
	return nil
}
func (NoOpHooks) OnError(ctx context.Context, hc *HookContext, err error) error { return nil }

// Runner is a helper to run hooks from AgentEngine
type Runner struct {
	hooks Hooks
	ctx   *HookContext
}

func NewRunner(hooks Hooks, agentID, sessionID string) *Runner {
	if hooks == nil {
		hooks = NoOpHooks{}
	}
	return &Runner{
		hooks: hooks,
		ctx: &HookContext{
			AgentID:   agentID,
			SessionID: sessionID,
			Timestamp: time.Now(),
			Metadata:  make(map[string]interface{}),
		},
	}
}

func (r *Runner) BeforeStart(input *types.AgentInput) error {
	return r.hooks.OnBeforeStart(context.Background(), r.ctx, input)
}

func (r *Runner) BeforeIteration(iteration int) error {
	r.ctx.Iteration = iteration
	r.ctx.Timestamp = time.Now()
	return r.hooks.OnBeforeIteration(context.Background(), r.ctx, iteration)
}

func (r *Runner) BeforeLLMCall(messages []types.Message) error {
	return r.hooks.OnBeforeLLMCall(context.Background(), r.ctx, messages)
}

func (r *Runner) BeforeToolCall(toolName string, input map[string]interface{}) error {
	return r.hooks.OnBeforeToolCall(context.Background(), r.ctx, toolName, input)
}

func (r *Runner) AfterToolCall(toolName string, output interface{}, err error) error {
	return r.hooks.OnAfterToolCall(context.Background(), r.ctx, toolName, output, err)
}

func (r *Runner) AfterLLMCall(response *types.Message) error {
	return r.hooks.OnAfterLLMCall(context.Background(), r.ctx, response)
}

func (r *Runner) AfterIteration(iteration int, result *types.AgentResult) error {
	r.ctx.Iteration = iteration
	return r.hooks.OnAfterIteration(context.Background(), r.ctx, iteration, result)
}

func (r *Runner) AfterEnd(finalResult *types.AgentResult) error {
	return r.hooks.OnAfterEnd(context.Background(), r.ctx, finalResult)
}

func (r *Runner) OnError(err error) error {
	return r.hooks.OnError(context.Background(), r.ctx, err)
}
