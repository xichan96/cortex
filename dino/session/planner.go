package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

type PlanMode string

const (
	PlanModeAuto  PlanMode = "auto"
	PlanModeAsk   PlanMode = "ask"
	PlanModeNever PlanMode = "never"
)

type PlannerHelper struct {
	enabled       bool
	prompt        string
	autoApprove   bool
	llmProvider   types.LLMProvider
	toolSchemas   []map[string]interface{}
	approvalChan  chan PlanApproval
	mode          PlanMode
	planThreshold int
}

type PlanApproval struct {
	Approved bool
	Plan     *Plan
}

func NewPlannerHelper(enabled bool, prompt string, autoApprove bool, llmProvider types.LLMProvider, toolSchemas []map[string]interface{}) *PlannerHelper {
	return &PlannerHelper{
		enabled:       enabled,
		prompt:        prompt,
		autoApprove:   autoApprove,
		llmProvider:   llmProvider,
		toolSchemas:   toolSchemas,
		approvalChan:  make(chan PlanApproval, 1),
		mode:          PlanModeAsk,
		planThreshold: 3,
	}
}

func (p *PlannerHelper) IsEnabled() bool {
	return p.enabled && p.llmProvider != nil
}

func (p *PlannerHelper) SetMode(mode PlanMode) {
	p.mode = mode
}

func (p *PlannerHelper) GetMode() PlanMode {
	return p.mode
}

func (p *PlannerHelper) CreatePlan(ctx context.Context, input string) (*Plan, error) {
	if !p.IsEnabled() {
		return nil, nil
	}

	messages := []types.Message{
		{Role: "system", Content: p.buildSystemPrompt()},
		{Role: "user", Content: input},
	}

	resp, err := p.llmProvider.Chat(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("planner LLM call failed: %w", err)
	}

	body := extractJSONFromContent(resp.Content)
	var planData struct {
		Plan struct {
			Goal      string     `json:"goal"`
			Steps     []PlanStep `json:"steps"`
			Reasoning string     `json:"reasoning,omitempty"`
		} `json:"plan"`
	}

	if err := json.Unmarshal([]byte(body), &planData); err != nil {
		var steps []PlanStep
		if stepsErr := json.Unmarshal([]byte(body), &steps); stepsErr == nil {
			return &Plan{
				Goal:  input,
				Steps: steps,
			}, nil
		}
		return nil, fmt.Errorf("failed to parse plan: %w", err)
	}

	if len(planData.Plan.Steps) == 0 {
		return nil, fmt.Errorf("no steps in plan")
	}

	return &Plan{
		Goal:      planData.Plan.Goal,
		Steps:     planData.Plan.Steps,
		Reasoning: planData.Plan.Reasoning,
	}, nil
}

func (p *PlannerHelper) buildSystemPrompt() string {
	var sb strings.Builder
	sb.WriteString(p.prompt)
	sb.WriteString("\n\nYou have access to the following tools:\n")

	for _, schema := range p.toolSchemas {
		if name, ok := schema["name"].(string); ok {
			if desc, ok := schema["description"].(string); ok {
				sb.WriteString(fmt.Sprintf("- %s: %s\n", name, desc))
			}
		}
	}

	sb.WriteString(`
Format your response as JSON with the following structure:
{
  "plan": {
    "goal": "overall goal description",
    "reasoning": "explanation of the plan approach",
    "steps": [
      {
        "index": 0,
        "tool": "tool_name",
        "input": {"param": "value"},
        "description": "what this step does",
        "reasoning": "why this step is needed"
      }
    ]
  }
}

Only respond with valid JSON, no markdown or additional text.`)
	return sb.String()
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

	switch p.mode {
	case PlanModeAuto:
		return true
	case PlanModeNever:
		return false
	case PlanModeAsk:
		return p.waitForApproval(ctx, plan)
	default:
		return p.waitForApproval(ctx, plan)
	}
}

func (p *PlannerHelper) waitForApproval(ctx context.Context, plan *Plan) bool {
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

func (p *PlannerHelper) ShouldEnterPlanMode(input string) bool {
	if !p.IsEnabled() {
		return false
	}

	keywords := []string{
		"plan",
		"design",
		"architecture",
		"roadmap",
		"strategy",
		"outline",
		"步骤",
		"计划",
		"设计",
	}

	lower := strings.ToLower(input)
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}

	return false
}
