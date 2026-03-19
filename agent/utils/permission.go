// permission.go: permission evaluation (allow/deny/ask) for tools and operations.
package utils

import (
	"path/filepath"
	"strings"
	"sync"
)

type PermAction string

const (
	PermActionAllow PermAction = "allow"
	PermActionDeny  PermAction = "deny"
	PermActionAsk   PermAction = "ask"
)

type PermRule struct {
	Permission string                 `json:"permission"`
	Action     PermAction             `json:"action"`
	Pattern    string                 `json:"pattern,omitempty"`
	Extra      map[string]interface{} `json:"extra,omitempty"`
}

type PermRuleset []PermRule

type PermEvaluator struct {
	mu      sync.RWMutex
	Ruleset PermRuleset
}

func NewPermEvaluator(ruleset PermRuleset) *PermEvaluator {
	return &PermEvaluator{Ruleset: ruleset}
}

func (e *PermEvaluator) Evaluate(permission string, input map[string]interface{}) PermAction {
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
					if defaultAction, ok := rule.Extra["*"].(string); ok && defaultAction == string(PermActionAllow) {
						return PermActionAsk
					}
				}
			}
		}
	}

	return PermActionDeny
}

func (e *PermEvaluator) evaluateRule(rule PermRule, input map[string]interface{}) PermAction {
	if rule.Action != "" && rule.Pattern == "" {
		return rule.Action
	}
	if rule.Extra != nil && input != nil {
		if defaultAction, ok := rule.Extra["*"].(string); ok {
			return PermAction(defaultAction)
		}
		for key, val := range rule.Extra {
			if inputVal, exists := input[key]; exists {
				if strVal, ok := inputVal.(string); ok {
					if matched, _ := filepath.Match(key, strVal); matched {
						if action, ok := val.(string); ok {
							return PermAction(action)
						}
					}
				}
			}
		}
	}
	return ""
}

func (e *PermEvaluator) Merge(other PermRuleset) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Ruleset = append(e.Ruleset, other...)
}

func PermEvaluate(permission string, name string, ruleset PermRuleset) PermRule {
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
	return PermRule{Permission: permission, Action: PermActionDeny}
}

func PermFromConfig(config map[string]interface{}) PermRuleset {
	var ruleset PermRuleset
	var wildcardRule *PermRule
	for perm, val := range config {
		var action PermAction
		var pattern string
		var extra map[string]interface{}

		switch v := val.(type) {
		case string:
			action = PermAction(v)
		case map[string]interface{}:
			for k, v := range v {
				if k == "*" {
					if str, ok := v.(string); ok {
						action = PermAction(str)
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

		rule := PermRule{
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

func PermFromAllowDenyAsk(denied, ask, allowed []string) PermRuleset {
	var rs PermRuleset
	for _, name := range denied {
		rs = append(rs, PermRule{Permission: name, Action: PermActionDeny})
	}
	for _, name := range ask {
		rs = append(rs, PermRule{Permission: name, Action: PermActionAsk})
	}
	for _, name := range allowed {
		rs = append(rs, PermRule{Permission: name, Action: PermActionAllow})
	}
	if len(allowed) > 0 {
		rs = append(rs, PermRule{Permission: "*", Action: PermActionDeny})
	}
	return rs
}

func PermMerge(rulesets ...PermRuleset) PermRuleset {
	var result PermRuleset
	for _, rs := range rulesets {
		result = append(result, rs...)
	}
	return result
}
