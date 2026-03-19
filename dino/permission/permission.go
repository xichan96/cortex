package permission

import (
	"fmt"
	"path/filepath"

	agentutils "github.com/xichan96/cortex/agent/utils"
)

type Action = agentutils.PermAction
type Rule = agentutils.PermRule
type Ruleset = agentutils.PermRuleset
type Evaluator = agentutils.PermEvaluator

var (
	ActionAllow = agentutils.PermActionAllow
	ActionDeny  = agentutils.PermActionDeny
	ActionAsk   = agentutils.PermActionAsk
)

var (
	NewEvaluator     = agentutils.NewPermEvaluator
	Evaluate         = agentutils.PermEvaluate
	FromConfig       = agentutils.PermFromConfig
	FromAllowDenyAsk = agentutils.PermFromAllowDenyAsk
	Merge            = agentutils.PermMerge
)

func DefaultRuleset() Ruleset {
	return Ruleset{
		{Permission: "*", Action: ActionAllow},
		{Permission: "doom_loop", Action: ActionAsk},
		{Permission: "external_directory", Action: ActionAsk, Extra: map[string]interface{}{"*": ActionAllow}},
		{Permission: "question", Action: ActionDeny},
		{Permission: "plan_enter", Action: ActionDeny},
		{Permission: "plan_exit", Action: ActionDeny},
		{Permission: "read", Action: ActionAllow, Extra: map[string]interface{}{
			"*":             ActionAllow,
			"*.env":         ActionAsk,
			"*.env.*":       ActionAsk,
			"*.env.example": ActionAllow,
		}},
	}
}

type AgentMode string

const (
	ModeBuild      AgentMode = "build"
	ModePlan       AgentMode = "plan"
	ModeExplore    AgentMode = "explore"
	ModeGeneral    AgentMode = "general"
	ModeCompaction AgentMode = "compaction"
	ModeTitle      AgentMode = "title"
	ModeSummary    AgentMode = "summary"
	ModeSubagent   AgentMode = "subagent"
)

func BuildAgentRuleset(mode AgentMode) Ruleset {
	rs := DefaultRuleset()

	switch mode {
	case ModeBuild:
		for i := range rs {
			if rs[i].Permission == "question" {
				rs[i].Action = ActionAllow
			}
			if rs[i].Permission == "plan_enter" {
				rs[i].Action = ActionAllow
			}
		}
	case ModePlan:
		for i := range rs {
			if rs[i].Permission == "question" {
				rs[i].Action = ActionAllow
			}
			if rs[i].Permission == "plan_exit" {
				rs[i].Action = ActionAllow
			}
			if rs[i].Permission == "edit" {
				rs[i].Action = ActionDeny
				rs[i].Extra = map[string]interface{}{
					"*":                                    ActionDeny,
					".opencode/plans/*.md":                 ActionAllow,
					filepath.Join("$DATA", "plans", "*.md"): ActionAllow,
				}
			}
		}
	case ModeExplore:
		for i := range rs {
			rs[i].Action = ActionDeny
		}
		rs = append(rs,
			Rule{Permission: "grep", Action: ActionAllow},
			Rule{Permission: "glob", Action: ActionAllow},
			Rule{Permission: "list", Action: ActionAllow},
			Rule{Permission: "bash", Action: ActionAllow},
			Rule{Permission: "webfetch", Action: ActionAllow},
			Rule{Permission: "websearch", Action: ActionAllow},
			Rule{Permission: "codesearch", Action: ActionAllow},
			Rule{Permission: "read", Action: ActionAllow},
		)
	case ModeGeneral:
		for i := range rs {
			if rs[i].Permission == "todoread" {
				rs[i].Action = ActionDeny
			}
			if rs[i].Permission == "todowrite" {
				rs[i].Action = ActionDeny
			}
		}
	case ModeSubagent:
		for i := range rs {
			if rs[i].Permission == "todoread" {
				rs[i].Action = ActionDeny
			}
			if rs[i].Permission == "todowrite" {
				rs[i].Action = ActionDeny
			}
		}
	}

	return rs
}

type ToolMeta struct {
	Title    string                 `json:"title"`
	Snapshot string                 `json:"snapshot,omitempty"`
	Start    int64                  `json:"start"`
	End      int64                  `json:"end,omitempty"`
	Extra    map[string]interface{} `json:"extra,omitempty"`
}

type PermissionRequest struct {
	ID         string                 `json:"id"`
	SessionID  string                 `json:"session_id"`
	Permission string                 `json:"permission"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

func (r *PermissionRequest) String() string {
	return fmt.Sprintf("PermissionRequest{id=%s, session=%s, permission=%s}", r.ID, r.SessionID, r.Permission)
}

type PermissionResponse struct {
	RequestID string `json:"request_id"`
	Reply     string `json:"reply"`
}
