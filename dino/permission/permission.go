package permission

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

type Action string

const (
	ActionAllow Action = "allow"
	ActionDeny  Action = "deny"
	ActionAsk   Action = "ask"
)

type Rule struct {
	Permission string                 `json:"permission"`
	Action     Action                 `json:"action"`
	Pattern    string                 `json:"pattern,omitempty"`
	Extra      map[string]interface{} `json:"extra,omitempty"`
}

type Ruleset []Rule

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
					"*":                                     ActionDeny,
					".opencode/plans/*.md":                  ActionAllow,
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
		// Subagents inherit from general
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

type Evaluator struct {
	mu      sync.RWMutex
	Ruleset Ruleset
}

func NewEvaluator(ruleset Ruleset) *Evaluator {
	return &Evaluator{Ruleset: ruleset}
}

func (e *Evaluator) Evaluate(permission string, input map[string]interface{}) Action {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, rule := range e.Ruleset {
		if rule.Permission != permission && rule.Permission != "*" {
			continue
		}

		if rule.Pattern != "" && input != nil {
			continue
		}

		if action := e.evaluateRule(rule, input); action != "" {
			return action
		}
	}

	if permission == "external_directory" && input != nil {
		if _, ok := input["path"].(string); ok {
			for _, rule := range e.Ruleset {
				if rule.Permission == "external_directory" && rule.Extra != nil {
					if defaultAction, ok := rule.Extra["*"].(string); ok && defaultAction == string(ActionAllow) {
						return ActionAsk
					}
				}
			}
		}
	}

	return ActionDeny
}

func (e *Evaluator) evaluateRule(rule Rule, input map[string]interface{}) Action {
	if rule.Action != "" && rule.Pattern == "" {
		return rule.Action
	}

	if rule.Extra != nil && input != nil {
		if defaultAction, ok := rule.Extra["*"].(string); ok {
			return Action(defaultAction)
		}

		for key, val := range rule.Extra {
			if inputVal, exists := input[key]; exists {
				if strVal, ok := inputVal.(string); ok {
					if matched, _ := filepath.Match(key, strVal); matched {
						if action, ok := val.(string); ok {
							return Action(action)
						}
					}
				}
			}
		}
	}

	return ""
}

func (e *Evaluator) Merge(other Ruleset) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Ruleset = append(e.Ruleset, other...)
}

func Evaluate(permission string, name string, ruleset Ruleset) Rule {
	for _, rule := range ruleset {
		if rule.Permission == permission || rule.Permission == "*" {
			if rule.Pattern != "" {
				if matched, _ := filepath.Match(rule.Pattern, name); matched {
					return rule
				}
			} else {
				return rule
			}
		}
	}
	return Rule{Permission: permission, Action: ActionDeny}
}

func FromConfig(config map[string]interface{}) Ruleset {
	var ruleset Ruleset
	var wildcardRule *Rule
	for perm, val := range config {
		var action Action
		var pattern string
		var extra map[string]interface{}

		switch v := val.(type) {
		case string:
			action = Action(v)
		case map[string]interface{}:
			for k, v := range v {
				if k == "*" {
					if str, ok := v.(string); ok {
						action = Action(str)
					}
				} else if strings.HasPrefix(k, "pattern:") {
					pattern = strings.TrimPrefix(k, "pattern:")
				} else {
					if extra == nil {
						extra = make(map[string]interface{})
					}
					extra[k] = v
				}
			}
		}

		rule := Rule{
			Permission: perm,
			Action:     action,
			Pattern:    pattern,
			Extra:      extra,
		}
		if perm == "*" {
			wildcardRule = &rule
		} else {
			ruleset = append(ruleset, rule)
		}
	}
	if wildcardRule != nil {
		ruleset = append(ruleset, *wildcardRule)
	}
	return ruleset
}

func FromAllowDenyAsk(denied, ask, allowed []string) Ruleset {
	var rs Ruleset
	for _, name := range denied {
		rs = append(rs, Rule{Permission: name, Action: ActionDeny})
	}
	for _, name := range ask {
		rs = append(rs, Rule{Permission: name, Action: ActionAsk})
	}
	for _, name := range allowed {
		rs = append(rs, Rule{Permission: name, Action: ActionAllow})
	}
	if len(allowed) > 0 {
		rs = append(rs, Rule{Permission: "*", Action: ActionDeny})
	}
	return rs
}

func Merge(rulesets ...Ruleset) Ruleset {
	var result Ruleset
	for _, rs := range rulesets {
		result = append(result, rs...)
	}
	return result
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
