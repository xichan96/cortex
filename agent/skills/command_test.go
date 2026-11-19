package skills

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandSkillCommand(t *testing.T) {
	baseDir := filepath.Join("testdata", "command")
	skillPath := filepath.Join(baseDir, SkillFileName)

	got, err := ExpandSkillCommand("command-skill", "please do X", baseDir)
	if err != nil {
		t.Fatalf("ExpandSkillCommand: %v", err)
	}

	want := fmt.Sprintf(
		"<skill name=\"command-skill\" location=\"%s\">\n"+
			"References are relative to %s.\n\n"+
			"# Command Skill\n\nDo the thing carefully.\n"+
			"</skill>\n\nplease do X",
		skillPath, baseDir,
	)
	if got != want {
		t.Errorf("ExpandSkillCommand mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "description:") {
		t.Error("expanded block should not contain frontmatter")
	}
}

func TestExpandSkillCommandNoArgs(t *testing.T) {
	got, err := ExpandSkillCommand("command-skill", "", filepath.Join("testdata", "command"))
	if err != nil {
		t.Fatalf("ExpandSkillCommand: %v", err)
	}
	if !strings.HasSuffix(got, "</skill>") {
		t.Errorf("block without args should end at </skill>:\n%s", got)
	}
}

func TestExpandSkillCommandFromFilePath(t *testing.T) {
	skillPath := filepath.Join("testdata", "command", SkillFileName)
	got, err := ExpandSkillCommand("command-skill", "", skillPath)
	if err != nil {
		t.Fatalf("ExpandSkillCommand: %v", err)
	}
	if !strings.Contains(got, fmt.Sprintf("location=\"%s\"", skillPath)) {
		t.Errorf("location missing from:\n%s", got)
	}
	if !strings.Contains(got, "References are relative to "+filepath.Dir(skillPath)+".") {
		t.Errorf("baseDir missing from:\n%s", got)
	}
}

func TestExpandSkillCommandUnknownDir(t *testing.T) {
	if _, err := ExpandSkillCommand("missing", "", t.TempDir()); err == nil {
		t.Fatal("expected an error when SKILL.md is absent")
	}
}

func TestIndexExpandSkillCommand(t *testing.T) {
	ix, _, err := LoadIndex(context.Background(), []string{"testdata"}, DiscoverOptions{})
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}

	got, err := ix.ExpandSkillCommand("command-skill", "with args")
	if err != nil {
		t.Fatalf("ExpandSkillCommand: %v", err)
	}
	if !strings.HasPrefix(got, "<skill name=\"command-skill\"") {
		t.Errorf("unexpected block:\n%s", got)
	}
	if !strings.HasSuffix(got, "with args") {
		t.Errorf("args not appended:\n%s", got)
	}

	if _, err := ix.ExpandSkillCommand("does-not-exist", ""); err == nil {
		t.Fatal("expected an error for an unknown skill")
	}
}

func TestExplicitInvocationIgnoresDisableModelInvocation(t *testing.T) {
	ix, _, err := LoadIndex(context.Background(), []string{"testdata"}, DiscoverOptions{})
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}

	entry, ok := ix.Get("private-skill")
	if !ok {
		t.Fatal("private-skill not indexed")
	}
	if !entry.DisableModelInvocation {
		t.Fatal("private-skill should have disable-model-invocation")
	}

	// Not advertised...
	for _, e := range ix.Advertisable() {
		if e.Name == "private-skill" {
			t.Fatal("private-skill should not be advertisable")
		}
	}

	// ...but explicit invocation still expands.
	got, err := ix.ExpandSkillCommand("private-skill", "")
	if err != nil {
		t.Fatalf("ExpandSkillCommand: %v", err)
	}
	if !strings.Contains(got, "Explicit invocations still expand this body.") {
		t.Errorf("body missing:\n%s", got)
	}
}

func TestFormatSkillBlockEscapesAttributes(t *testing.T) {
	got := FormatSkillBlock(`a"b`, `/tmp/a&b/SKILL.md`, "/tmp/a&b", "body", "")
	if !strings.Contains(got, `name="a&quot;b"`) {
		t.Errorf("name not escaped: %s", got)
	}
	if !strings.Contains(got, `location="/tmp/a&amp;b/SKILL.md"`) {
		t.Errorf("location not escaped: %s", got)
	}
}
