// budget.go: session-level quota for tokens, tool calls, and time.
package utils

import (
	"context"
	"sync"
)

type Cost struct {
	Tokens int   `json:"tokens"`
	Calls  int   `json:"calls"`
	TimeMs int64 `json:"time_ms"`
}

type BudgetRequest struct {
	SessionID string
	ToolCalls []string
	Estimated Cost
}

type BudgetResult struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
	Remain  Cost   `json:"remain"`
}

type BudgetState struct {
	UsedTokens int   `json:"used_tokens"`
	UsedCalls  int   `json:"used_calls"`
	UsedTimeMs int64 `json:"used_time_ms"`
	MaxTokens  int   `json:"max_tokens"`
	MaxCalls   int   `json:"max_calls"`
	MaxTimeMs  int64 `json:"max_time_ms"`
}

type BudgetConfig struct {
	Enabled      bool
	MaxTokens    int
	MaxToolCalls int
	MaxTimeMs    int64
}

type Budget interface {
	Check(ctx context.Context, req BudgetRequest) (BudgetResult, error)
	Consume(ctx context.Context, sessionID string, amount Cost) error
	GetState(sessionID string) BudgetState
	Reset(sessionID string)
	ResetAll()
	RecordToolCall(ctx context.Context, sessionID string)
	RecordTokens(ctx context.Context, sessionID string, tokens int)
	CanExecute(sessionID string) bool
}

type budget struct {
	config   *BudgetConfig
	sessions map[string]*BudgetState
	mu       sync.RWMutex
}

func NewBudget(cfg *BudgetConfig) Budget {
	if cfg == nil {
		cfg = &BudgetConfig{
			Enabled:      true,
			MaxTokens:    100000,
			MaxToolCalls: 50,
			MaxTimeMs:    300000,
		}
	}
	return &budget{
		config:   cfg,
		sessions: make(map[string]*BudgetState),
	}
}

func (b *budget) Check(ctx context.Context, req BudgetRequest) (BudgetResult, error) {
	if !b.config.Enabled {
		return BudgetResult{Allowed: true}, nil
	}

	b.mu.RLock()
	state, exists := b.sessions[req.SessionID]
	var usedTokens, usedCalls int
	var usedTimeMs int64
	var maxTokens, maxCalls int
	var maxTimeMs int64

	if exists {
		usedTokens = state.UsedTokens
		usedCalls = state.UsedCalls
		usedTimeMs = state.UsedTimeMs
		maxTokens = state.MaxTokens
		maxCalls = state.MaxCalls
		maxTimeMs = state.MaxTimeMs
	} else {
		maxTokens = b.config.MaxTokens
		maxCalls = b.config.MaxToolCalls
		maxTimeMs = b.config.MaxTimeMs
	}
	b.mu.RUnlock()

	var remTok, remCalls int
	var remTime int64
	if b.config.MaxTokens > 0 {
		remTok = maxTokens - usedTokens - req.Estimated.Tokens
		if remTok < 0 {
			return BudgetResult{Allowed: false, Reason: "exceeded token budget", Remain: Cost{Tokens: remTok}}, nil
		}
	}
	if b.config.MaxToolCalls > 0 {
		remCalls = maxCalls - usedCalls - req.Estimated.Calls
		if remCalls < 0 {
			return BudgetResult{Allowed: false, Reason: "exceeded tool call budget", Remain: Cost{Calls: remCalls}}, nil
		}
	}
	if b.config.MaxTimeMs > 0 {
		remTime = maxTimeMs - usedTimeMs - req.Estimated.TimeMs
		if remTime < 0 {
			return BudgetResult{Allowed: false, Reason: "exceeded time budget", Remain: Cost{TimeMs: remTime}}, nil
		}
	}

	return BudgetResult{Allowed: true, Remain: Cost{Tokens: remTok, Calls: remCalls, TimeMs: remTime}}, nil
}

func (b *budget) Consume(ctx context.Context, sessionID string, amount Cost) error {
	if !b.config.Enabled {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	state, exists := b.sessions[sessionID]
	if !exists {
		state = &BudgetState{
			MaxTokens: b.config.MaxTokens,
			MaxCalls:  b.config.MaxToolCalls,
			MaxTimeMs: b.config.MaxTimeMs,
		}
		b.sessions[sessionID] = state
	}

	state.UsedTokens += amount.Tokens
	state.UsedCalls += amount.Calls
	state.UsedTimeMs += amount.TimeMs

	return nil
}

func (b *budget) GetState(sessionID string) BudgetState {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if state, exists := b.sessions[sessionID]; exists {
		return *state
	}

	return BudgetState{
		MaxTokens: b.config.MaxTokens,
		MaxCalls:  b.config.MaxToolCalls,
		MaxTimeMs: b.config.MaxTimeMs,
	}
}

func (b *budget) Reset(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.sessions, sessionID)
}

func (b *budget) ResetAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessions = make(map[string]*BudgetState)
}

func (b *budget) RecordToolCall(ctx context.Context, sessionID string) {
	b.Consume(ctx, sessionID, Cost{Calls: 1})
}

func (b *budget) RecordTokens(ctx context.Context, sessionID string, tokens int) {
	b.Consume(ctx, sessionID, Cost{Tokens: tokens})
}

func (b *budget) CanExecute(sessionID string) bool {
	if !b.config.Enabled {
		return true
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	state, exists := b.sessions[sessionID]
	if !exists {
		return true
	}

	if b.config.MaxToolCalls > 0 && state.UsedCalls >= b.config.MaxToolCalls {
		return false
	}
	if b.config.MaxTokens > 0 && state.UsedTokens >= b.config.MaxTokens {
		return false
	}
	if b.config.MaxTimeMs > 0 && state.UsedTimeMs >= b.config.MaxTimeMs {
		return false
	}
	return true
}
