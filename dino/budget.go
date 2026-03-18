package dino

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

	remainTokens := maxTokens - usedTokens - req.Estimated.Tokens
	remainCalls := maxCalls - usedCalls - req.Estimated.Calls
	remainTimeMs := maxTimeMs - usedTimeMs - req.Estimated.TimeMs

	if remainTokens < 0 {
		return BudgetResult{Allowed: false, Reason: "exceeded token budget", Remain: Cost{Tokens: remainTokens}}, nil
	}
	if remainCalls < 0 {
		return BudgetResult{Allowed: false, Reason: "exceeded tool call budget", Remain: Cost{Calls: remainCalls}}, nil
	}
	if remainTimeMs < 0 {
		return BudgetResult{Allowed: false, Reason: "exceeded time budget", Remain: Cost{TimeMs: remainTimeMs}}, nil
	}

	return BudgetResult{Allowed: true, Remain: Cost{Tokens: remainTokens, Calls: remainCalls, TimeMs: remainTimeMs}}, nil
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

	return state.UsedCalls < b.config.MaxToolCalls &&
		state.UsedTokens < b.config.MaxTokens &&
		state.UsedTimeMs < b.config.MaxTimeMs
}
