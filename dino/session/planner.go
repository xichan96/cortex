package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

type PlannerHelper struct {
	enabled      bool
	prompt       string
	autoApprove  bool
	llmProvider  types.LLMProvider
	toolSchemas  []map[string]interface{}
	approvalChan chan PlanApproval
}

type PlanApproval struct {
	Approved bool
	Plan     *Plan
}

func NewPlannerHelper(enabled bool, prompt string, autoApprove bool, llmProvider types.LLMProvider, toolSchemas []map[string]interface{}) *PlannerHelper {
	return &PlannerHelper{
		enabled:      enabled,
		prompt:       prompt,
		autoApprove:  autoApprove,
		llmProvider:  llmProvider,
		toolSchemas:  toolSchemas,
		approvalChan: make(chan PlanApproval, 1),
	}
}

func (p *PlannerHelper) IsEnabled() bool {
	return p.enabled && p.llmProvider != nil
}

func (p *PlannerHelper) CreatePlan(ctx context.Context, input string) (*Plan, error) {
	if !p.IsEnabled() {
		return nil, nil
	}

	messages := []types.Message{
		{Role: "system", Content: p.prompt},
		{Role: "user", Content: input},
	}

	resp, err := p.llmProvider.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("planner LLM call failed: %w", err)
	}

	body := extractJSONFromContent(resp.Content)
	var plan Plan
	if err := json.Unmarshal([]byte(body), &plan); err != nil {
		return nil, fmt.Errorf("failed to parse plan: %w", err)
	}

	if len(plan.Steps) == 0 {
		return nil, fmt.Errorf("no steps in plan")
	}

	return &plan, nil
}

func extractJSONFromContent(s string) string {
	s = strings.TrimSpace(s)
	const (
		markdownFence = "```"
		jsonLang      = "json"
	)
	if i := strings.Index(s, markdownFence); i >= 0 {
		rest := s[i+len(markdownFence):]
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(strings.ToLower(rest), jsonLang) {
			rest = rest[len(jsonLang):]
		}
		rest = strings.TrimSpace(rest)
		if j := strings.Index(rest, markdownFence); j >= 0 {
			return strings.TrimSpace(rest[:j])
		}
		return strings.TrimSpace(rest)
	}
	return s
}

const defaultApprovalTimeout = 5 * time.Minute

func (p *PlannerHelper) RequestApproval(ctx context.Context, plan *Plan) bool {
	if p.autoApprove {
		return true
	}
	timeout := defaultApprovalTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d > 0 && d < timeout {
			timeout = d
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case approval := <-p.approvalChan:
		return approval.Approved
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (p *PlannerHelper) Approve(approved bool) {
	select {
	case p.approvalChan <- PlanApproval{Approved: approved, Plan: nil}:
	default:
	}
}

func (p *PlannerHelper) ApprovalChan() chan<- PlanApproval {
	return p.approvalChan
}
