package dino

import (
	"context"
	"reflect"
	"strings"
	"testing"

	agentskills "github.com/xichan96/cortex/agent/skills"
	"github.com/xichan96/cortex/agent/types"
)

func toolNames(tools []types.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name())
	}
	return out
}

func TestApplySkillPolicy(t *testing.T) {
	read := &mockTool{name: "read"}
	write := &mockTool{name: "write"}
	Bash := &mockTool{name: "Bash"}
	tools := []types.Tool{read, write, Bash}

	// Empty allowed-tools means "no restriction" and returns the input as-is.
	unrestricted := ApplySkillPolicy(tools, agentskills.SkillPolicy{})
	if len(unrestricted) != 3 || unrestricted[0] != tools[0] {
		t.Errorf("unrestricted = %v, want the original slice", toolNames(unrestricted))
	}

	// A subset keeps input order, regardless of declaration order.
	got := ApplySkillPolicy(tools, agentskills.SkillPolicy{AllowedTools: []string{"Bash", "read"}})
	if !reflect.DeepEqual(toolNames(got), []string{"read", "Bash"}) {
		t.Errorf("subset = %v, want [read Bash]", toolNames(got))
	}

	// Unknown declared names are ignored; nil tools are skipped.
	got = ApplySkillPolicy([]types.Tool{nil, read, nil, write}, agentskills.SkillPolicy{AllowedTools: []string{"read", "does-not-exist"}})
	if !reflect.DeepEqual(toolNames(got), []string{"read"}) {
		t.Errorf("unknown allowed = %v, want [read]", toolNames(got))
	}

	// No match yields an empty (non-nil) list.
	got = ApplySkillPolicy(tools, agentskills.SkillPolicy{AllowedTools: []string{"nope"}})
	if got == nil || len(got) != 0 {
		t.Errorf("no match = %v, want empty non-nil", toolNames(got))
	}

	// Paths do not filter tools.
	got = ApplySkillPolicy(tools, agentskills.SkillPolicy{Paths: []string{"**/*.go"}})
	if len(got) != 3 {
		t.Errorf("paths-only policy = %v, want all tools", toolNames(got))
	}
}

func TestPolicyForSkill(t *testing.T) {
	root := t.TempDir()
	writeLazySkill(t, root, "pdf", "pdf", "PDF tools.",
		"allowed-tools: [read, write]\nmetadata:\n  cortex:\n    paths: [\"**/*.pdf\"]\n",
		"# PDF-BODY\n")
	writeLazySkill(t, root, "plain", "plain", "Plain skill.", "", "# PLAIN-BODY\n")

	ix, _, err := agentskills.LoadIndex(context.Background(), []string{root}, agentskills.DiscoverOptions{})
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}

	policy, ok := PolicyForSkill(ix, "pdf")
	if !ok {
		t.Fatal("pdf not found")
	}
	if !reflect.DeepEqual(policy.AllowedTools, []string{"read", "write"}) {
		t.Errorf("AllowedTools = %v", policy.AllowedTools)
	}
	if !reflect.DeepEqual(policy.Paths, []string{"**/*.pdf"}) {
		t.Errorf("Paths = %v", policy.Paths)
	}

	// Name lookup matches the skill tool's case-insensitive behavior.
	if _, ok := PolicyForSkill(ix, "PDF"); !ok {
		t.Error("case-insensitive lookup failed")
	}

	// A skill without constraints is found but has an empty policy.
	if policy, ok := PolicyForSkill(ix, "plain"); !ok || !policy.Empty() {
		t.Errorf("plain policy = %+v, %v; want empty, true", policy, ok)
	}

	if _, ok := PolicyForSkill(ix, "missing"); ok {
		t.Error("unknown skill should report ok=false")
	}
	if _, ok := PolicyForSkill(nil, "pdf"); ok {
		t.Error("nil index should report ok=false")
	}
	if _, ok := PolicyForSkill(ix, "  "); ok {
		t.Error("blank name should report ok=false")
	}
}

// TestLazySkillToolReturnsPolicy pins requirement 2: the skill tool output
// carries machine-readable allowed_tools/paths metadata while keeping the
// skill_name/input parameters and body format intact.
func TestLazySkillToolReturnsPolicy(t *testing.T) {
	root := t.TempDir()
	writeLazySkill(t, root, "pdf", "pdf", "PDF tools.",
		"allowed-tools: [read, write]\nmetadata:\n  cortex:\n    paths: [\"**/*.pdf\"]\n",
		"# PDF-BODY\n")
	writeLazySkill(t, root, "plain", "plain", "Plain skill.", "", "# PLAIN-BODY\n")

	factory := newSkillsFactory(t, root)
	tool := findSkillTool(t, factory, "skill")

	out, err := executeSkillTool(t, tool, map[string]interface{}{"skill_name": "pdf", "input": "extract text"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{
		"Policy: ",
		`"allowed_tools":["read","write"]`,
		`"paths":["**/*.pdf"]`,
		"# PDF-BODY",
		"<args>extract text</args>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("skill output missing %q:\n%s", want, out)
		}
	}

	// A skill without constraints keeps the previous output shape (no Policy
	// line) and still loads its body.
	plain, err := executeSkillTool(t, tool, map[string]interface{}{"skill_name": "plain"})
	if err != nil {
		t.Fatalf("Execute plain: %v", err)
	}
	if strings.Contains(plain, "Policy:") {
		t.Errorf("unrestricted skill should not emit a Policy line:\n%s", plain)
	}
	if !strings.Contains(plain, "# PLAIN-BODY") {
		t.Errorf("plain body missing:\n%s", plain)
	}
}
