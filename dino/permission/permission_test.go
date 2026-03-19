package permission

import (
	"testing"
)

func TestActionConstants(t *testing.T) {
	if ActionAllow != "allow" {
		t.Errorf("expected ActionAllow to be 'allow', got %s", ActionAllow)
	}
	if ActionDeny != "deny" {
		t.Errorf("expected ActionDeny to be 'deny', got %s", ActionDeny)
	}
	if ActionAsk != "ask" {
		t.Errorf("expected ActionAsk to be 'ask', got %s", ActionAsk)
	}
}

func TestAgentModeConstants(t *testing.T) {
	modes := []struct {
		mode     AgentMode
		expected string
	}{
		{ModeBuild, "build"},
		{ModePlan, "plan"},
		{ModeExplore, "explore"},
		{ModeGeneral, "general"},
		{ModeCompaction, "compaction"},
		{ModeTitle, "title"},
		{ModeSummary, "summary"},
		{ModeSubagent, "subagent"},
	}

	for _, m := range modes {
		if m.mode != AgentMode(m.expected) {
			t.Errorf("expected mode %s, got %s", m.expected, m.mode)
		}
	}
}

func TestDefaultRuleset(t *testing.T) {
	rs := DefaultRuleset()

	if len(rs) == 0 {
		t.Fatal("DefaultRuleset should not be empty")
	}

	foundStar := false
	for _, rule := range rs {
		if rule.Permission == "*" && rule.Action == ActionAllow {
			foundStar = true
			break
		}
	}
	if !foundStar {
		t.Error("DefaultRuleset should have a rule for '*' with ActionAllow")
	}

	for _, rule := range rs {
		if rule.Permission == "question" && rule.Action != ActionDeny {
			t.Error("question should be denied by default")
		}
		if rule.Permission == "plan_enter" && rule.Action != ActionDeny {
			t.Error("plan_enter should be denied by default")
		}
		if rule.Permission == "plan_exit" && rule.Action != ActionDeny {
			t.Error("plan_exit should be denied by default")
		}
	}
}

func TestBuildAgentRulesetBuild(t *testing.T) {
	rs := BuildAgentRuleset(ModeBuild)

	var questionAllowed, planEnterAllowed bool
	for _, rule := range rs {
		if rule.Permission == "question" && rule.Action == ActionAllow {
			questionAllowed = true
		}
		if rule.Permission == "plan_enter" && rule.Action == ActionAllow {
			planEnterAllowed = true
		}
	}

	if !questionAllowed {
		t.Error("ModeBuild should allow question")
	}
	if !planEnterAllowed {
		t.Error("ModeBuild should allow plan_enter")
	}
}

func TestBuildAgentRulesetPlan(t *testing.T) {
	rs := BuildAgentRuleset(ModePlan)

	var questionAllowed, planExitAllowed bool
	for _, rule := range rs {
		if rule.Permission == "question" && rule.Action == ActionAllow {
			questionAllowed = true
		}
		if rule.Permission == "plan_exit" && rule.Action == ActionAllow {
			planExitAllowed = true
		}
	}

	if !questionAllowed {
		t.Error("ModePlan should allow question")
	}
	if !planExitAllowed {
		t.Error("ModePlan should allow plan_exit")
	}
}

func TestBuildAgentRulesetExplore(t *testing.T) {
	rs := BuildAgentRuleset(ModeExplore)

	allowedTools := map[string]bool{
		"grep":       true,
		"glob":       true,
		"list":       true,
		"bash":       true,
		"webfetch":   true,
		"websearch":  true,
		"codesearch": true,
		"read":       true,
	}

	for _, rule := range rs {
		if allowedTools[rule.Permission] && rule.Action == ActionAllow {
			delete(allowedTools, rule.Permission)
		}
	}

	for tool := range allowedTools {
		t.Errorf("ModeExplore should allow %s", tool)
	}
}

func TestBuildAgentRulesetGeneral(t *testing.T) {
	rs := BuildAgentRuleset(ModeGeneral)

	for _, rule := range rs {
		if rule.Permission == "todoread" && rule.Action != ActionDeny {
			t.Error("ModeGeneral should deny todoread")
		}
		if rule.Permission == "todowrite" && rule.Action != ActionDeny {
			t.Error("ModeGeneral should deny todowrite")
		}
	}
}

func TestNewEvaluator(t *testing.T) {
	rs := Ruleset{
		{Permission: "read", Action: ActionAllow},
		{Permission: "edit", Action: ActionDeny},
	}

	evaluator := NewEvaluator(rs)
	if evaluator == nil {
		t.Fatal("NewEvaluator should not return nil")
	}

	if len(evaluator.Ruleset) != 2 {
		t.Errorf("expected 2 rules, got %d", len(evaluator.Ruleset))
	}
}

func TestEvaluatorEvaluate(t *testing.T) {
	rs := Ruleset{
		{Permission: "read", Action: ActionAllow},
		{Permission: "edit", Action: ActionDeny},
		{Permission: "bash", Action: ActionAsk},
	}

	evaluator := NewEvaluator(rs)

	tests := []struct {
		permission string
		want       Action
	}{
		{"read", ActionAllow},
		{"edit", ActionDeny},
		{"bash", ActionAsk},
		{"unknown", ActionDeny},
	}

	for _, tt := range tests {
		t.Run(tt.permission, func(t *testing.T) {
			got := evaluator.Evaluate(tt.permission, nil)
			if got != tt.want {
				t.Errorf("Evaluate(%s) = %s, want %s", tt.permission, got, tt.want)
			}
		})
	}
}

func TestEvaluatorEvaluateWithInput(t *testing.T) {
	rs := Ruleset{
		{Permission: "read", Action: ActionAllow, Extra: map[string]interface{}{
			"*": ActionAllow,
		}},
	}

	evaluator := NewEvaluator(rs)

	tests := []struct {
		name  string
		input map[string]interface{}
		want  Action
	}{
		{"no input", nil, ActionAllow},
		{"with input", map[string]interface{}{"path": "/some/file.go"}, ActionAllow},
		{"empty input", map[string]interface{}{}, ActionAllow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluator.Evaluate("read", tt.input)
			if got != tt.want {
				t.Errorf("Evaluate(read, %v) = %s, want %s", tt.input, got, tt.want)
			}
		})
	}
}

func TestEvaluatorEvaluateExternalDirectory(t *testing.T) {
	rs := Ruleset{
		{Permission: "external_directory", Action: ActionAsk, Extra: map[string]interface{}{
			"*": ActionAllow,
		}},
	}

	evaluator := NewEvaluator(rs)

	got := evaluator.Evaluate("external_directory", map[string]interface{}{
		"path": "/some/path",
	})

	if got != ActionAsk {
		t.Errorf("expected ActionAsk for external_directory, got %s", got)
	}
}

func TestEvaluatorMerge(t *testing.T) {
	rs1 := Ruleset{
		{Permission: "read", Action: ActionAllow},
	}
	rs2 := Ruleset{
		{Permission: "edit", Action: ActionDeny},
	}

	evaluator := NewEvaluator(rs1)
	evaluator.Merge(rs2)

	if len(evaluator.Ruleset) != 2 {
		t.Errorf("expected 2 rules after merge, got %d", len(evaluator.Ruleset))
	}
}

func TestEvaluate(t *testing.T) {
	rs := Ruleset{
		{Permission: "read", Action: ActionAllow},
		{Permission: "edit", Action: ActionDeny},
	}

	tests := []struct {
		permission string
		name       string
		want       Action
	}{
		{"read", "file.go", ActionAllow},
		{"edit", "file.go", ActionDeny},
		{"unknown", "file.go", ActionDeny},
	}

	for _, tt := range tests {
		t.Run(tt.permission, func(t *testing.T) {
			rule := Evaluate(tt.permission, tt.name, rs)
			if rule.Action != tt.want {
				t.Errorf("Evaluate(%s, %s) = %s, want %s", tt.permission, tt.name, rule.Action, tt.want)
			}
		})
	}
}

func TestEvaluateWithPattern(t *testing.T) {
	rs := Ruleset{
		{Permission: "read", Action: ActionDeny, Pattern: "*.secret"},
		{Permission: "read", Action: ActionAllow},
	}

	rule := Evaluate("read", "file.secret", rs)
	if rule.Action != ActionDeny {
		t.Errorf("expected ActionDeny for pattern match, got %s", rule.Action)
	}

	rule = Evaluate("read", "normal_file.go", rs)
	if rule.Action != ActionAllow {
		t.Errorf("expected ActionAllow for non-matching, got %s", rule.Action)
	}
}

func TestFromConfig(t *testing.T) {
	config := map[string]interface{}{
		"read": "allow",
		"edit": "deny",
		"bash": "ask",
		"exec": map[string]interface{}{
			"*":        "deny",
			"safe_cmd": "allow",
		},
	}

	rs := FromConfig(config)

	if len(rs) != 4 {
		t.Fatalf("expected 4 rules, got %d", len(rs))
	}

	for _, rule := range rs {
		switch rule.Permission {
		case "read":
			if rule.Action != ActionAllow {
				t.Errorf("read should be allow, got %s", rule.Action)
			}
		case "edit":
			if rule.Action != ActionDeny {
				t.Errorf("edit should be deny, got %s", rule.Action)
			}
		case "bash":
			if rule.Action != ActionAsk {
				t.Errorf("bash should be ask, got %s", rule.Action)
			}
		case "exec":
			if rule.Action != ActionDeny {
				t.Errorf("exec default should be deny, got %s", rule.Action)
			}
			if rule.Extra == nil {
				t.Error("exec should have Extra map")
			}
		}
	}
}

func TestFromConfigWildcardLast(t *testing.T) {
	config := map[string]interface{}{
		"*":    "ask",
		"bash": "allow",
	}
	rs := FromConfig(config)
	if len(rs) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rs))
	}
	if rs[0].Permission != "bash" || rs[1].Permission != "*" {
		t.Errorf("expected [bash, *] order, got [%s, %s]", rs[0].Permission, rs[1].Permission)
	}
	eval := NewEvaluator(rs)
	if eval.Evaluate("bash", nil) != ActionAllow {
		t.Error("bash should be allow")
	}
	if eval.Evaluate("edit", nil) != ActionAsk {
		t.Error("edit should match * ask")
	}
}

func TestMerge(t *testing.T) {
	rs1 := Ruleset{
		{Permission: "read", Action: ActionAllow},
	}
	rs2 := Ruleset{
		{Permission: "edit", Action: ActionDeny},
	}
	rs3 := Ruleset{
		{Permission: "bash", Action: ActionAsk},
	}

	result := Merge(rs1, rs2, rs3)

	if len(result) != 3 {
		t.Errorf("expected 3 rules, got %d", len(result))
	}
}

func TestMergeEmptyRulesets(t *testing.T) {
	result := Merge()
	if len(result) != 0 {
		t.Errorf("expected 0 rules, got %d", len(result))
	}

	result = Merge(Ruleset{}, Ruleset{{Permission: "test", Action: ActionAllow}})
	if len(result) != 1 {
		t.Errorf("expected 1 rule, got %d", len(result))
	}
}

func TestPermissionRequestString(t *testing.T) {
	req := &PermissionRequest{
		ID:         "req-123",
		SessionID:  "session-456",
		Permission: "edit",
	}

	expected := "PermissionRequest{id=req-123, session=session-456, permission=edit}"
	got := req.String()

	if got != expected {
		t.Errorf("String() = %s, want %s", got, expected)
	}
}

func TestToolMeta(t *testing.T) {
	meta := ToolMeta{
		Title:    "Edit File",
		Snapshot: "snapshot",
		Start:    1000,
		End:      2000,
		Extra:    map[string]interface{}{"key": "value"},
	}

	if meta.Title != "Edit File" {
		t.Errorf("expected Title 'Edit File', got %s", meta.Title)
	}
	if meta.Snapshot != "snapshot" {
		t.Errorf("expected Snapshot 'snapshot', got %s", meta.Snapshot)
	}
	if meta.End != 2000 {
		t.Errorf("expected End 2000, got %d", meta.End)
	}
}

func TestEvaluatorConcurrentAccess(t *testing.T) {
	rs := Ruleset{
		{Permission: "read", Action: ActionAllow},
	}
	evaluator := NewEvaluator(rs)

	done := make(chan bool)

	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				evaluator.Evaluate("read", nil)
				evaluator.Evaluate("unknown", nil)
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
