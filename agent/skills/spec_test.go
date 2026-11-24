package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readTestdata(t *testing.T, rel string) ([]byte, string) {
	t.Helper()
	path := filepath.Join("testdata", rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data, path
}

func hasDiagnostic(diags []Diagnostic, kind string) bool {
	for _, d := range diags {
		if d.Kind == kind {
			return true
		}
	}
	return false
}

func countDiagnostics(diags []Diagnostic, kind string) int {
	n := 0
	for _, d := range diags {
		if d.Kind == kind {
			n++
		}
	}
	return n
}

func TestParseSpecStandardFields(t *testing.T) {
	data, path := readTestdata(t, "standard/SKILL.md")
	spec, diags, err := ParseSpec(data, path)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}

	if spec.Name != "pdf-tools" {
		t.Errorf("Name = %q, want pdf-tools", spec.Name)
	}
	if !strings.Contains(spec.Description, "Extract text") {
		t.Errorf("Description = %q", spec.Description)
	}
	if spec.License != "Apache-2.0" {
		t.Errorf("License = %q", spec.License)
	}
	if spec.Compatibility != "Requires pdftotext on PATH" {
		t.Errorf("Compatibility = %q", spec.Compatibility)
	}
	if got, want := spec.AllowedTools, []string{"read", "Bash"}; !equalStrings(got, want) {
		t.Errorf("AllowedTools = %v, want %v", got, want)
	}
	if spec.DisableModelInvocation {
		t.Error("DisableModelInvocation = true, want false")
	}
	if spec.Metadata["author"] != "cortex" {
		t.Errorf("Metadata[author] = %v", spec.Metadata["author"])
	}
	if hasDiagnostic(diags, DiagnosticDeprecated) {
		t.Errorf("unexpected deprecation diagnostics: %v", diags)
	}

	doc, _, err := ParseDocument(data, path)
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	if !strings.Contains(doc.Body, "# PDF Tools") {
		t.Errorf("body = %q", doc.Body)
	}
	if strings.Contains(doc.Body, "name: pdf-tools") {
		t.Error("body still contains frontmatter")
	}
}

func TestParseSpecCortexMetadata(t *testing.T) {
	data, path := readTestdata(t, "cortex/SKILL.md")
	spec, diags, err := ParseSpec(data, path)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}

	if spec.Name != "cortex-ext" {
		t.Errorf("Name = %q", spec.Name)
	}
	c := spec.Cortex
	if len(c.Triggers) != 2 || c.Triggers[0].Type != "keyword" || c.Triggers[0].Pattern != "pdf" {
		t.Errorf("Triggers = %+v", c.Triggers)
	}
	if !equalStrings(c.Tags, []string{"docs", "pdf"}) {
		t.Errorf("Tags = %v", c.Tags)
	}
	if c.Priority != 42 {
		t.Errorf("Priority = %d", c.Priority)
	}
	if !equalStrings(c.Dependencies, []string{"pdf-tools"}) {
		t.Errorf("Dependencies = %v", c.Dependencies)
	}
	if !equalStrings(c.Paths, []string{"**/*.pdf", "docs/**"}) {
		t.Errorf("Paths = %v", c.Paths)
	}
	if c.WhenToUse == "" {
		t.Error("WhenToUse is empty")
	}
	if c.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v", c.Timeout)
	}
	if c.Retries != 2 {
		t.Errorf("Retries = %d", c.Retries)
	}
	if c.Fallback != "pdf-tools" {
		t.Errorf("Fallback = %q", c.Fallback)
	}
	if hasDiagnostic(diags, DiagnosticDeprecated) {
		t.Errorf("cortex metadata should not be deprecated: %v", diags)
	}
	if hasDiagnostic(diags, DiagnosticUnknownField) {
		t.Errorf("unexpected unknown-field diagnostics: %v", diags)
	}
}

func TestParseSpecLegacyTopLevelFields(t *testing.T) {
	data, path := readTestdata(t, "legacy/SKILL.md")
	spec, diags, err := ParseSpec(data, path)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}

	c := spec.Cortex
	if len(c.Triggers) != 1 || c.Triggers[0].Pattern != "/legacy" {
		t.Errorf("Triggers = %+v", c.Triggers)
	}
	if !equalStrings(c.Tags, []string{"legacy", "old"}) {
		t.Errorf("Tags = %v", c.Tags)
	}
	if c.Priority != 7 {
		t.Errorf("Priority = %d", c.Priority)
	}
	if c.Timeout != 45*time.Second {
		t.Errorf("Timeout = %v", c.Timeout)
	}
	if c.Retries != 1 || c.Fallback != "other-skill" {
		t.Errorf("Retries/Fallback = %d/%q", c.Retries, c.Fallback)
	}
	if !equalStrings(c.Dependencies, []string{"dep-skill"}) {
		t.Errorf("Dependencies = %v", c.Dependencies)
	}
	if !equalStrings(c.Paths, []string{"*.go"}) {
		t.Errorf("Paths = %v", c.Paths)
	}
	if c.WhenToUse != "When testing legacy compatibility." {
		t.Errorf("WhenToUse = %q", c.WhenToUse)
	}
	if !equalStrings(spec.AllowedTools, []string{"read", "Grep"}) {
		t.Errorf("AllowedTools = %v", spec.AllowedTools)
	}

	// One deprecation per legacy field: 9 extension keys + allowed_tools.
	if got := countDiagnostics(diags, DiagnosticDeprecated); got != len(legacyExtensionKeys)+1 {
		t.Errorf("deprecated diagnostics = %d, want %d (%v)", got, len(legacyExtensionKeys)+1, diags)
	}
}

func TestParseSpecCortexWinsOverLegacy(t *testing.T) {
	data := []byte(`---
name: both
description: Both spellings present.
priority: 5
metadata:
  cortex:
    priority: 99
---
body
`)
	spec, diags, err := ParseSpec(data, "both/SKILL.md")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.Cortex.Priority != 99 {
		t.Errorf("Priority = %d, want 99 (metadata.cortex wins)", spec.Cortex.Priority)
	}
	if !hasDiagnostic(diags, DiagnosticDeprecated) {
		t.Error("expected a deprecation diagnostic for the legacy field")
	}
}

func TestParseSpecValidation(t *testing.T) {
	data, path := readTestdata(t, "badname/SKILL.md")
	spec, diags, err := ParseSpec(data, path)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.Name != "Bad_Name" {
		t.Errorf("Name = %q, want invalid name preserved", spec.Name)
	}
	if !hasDiagnostic(diags, DiagnosticInvalidName) {
		t.Errorf("expected invalid-name diagnostic, got %v", diags)
	}

	data, path = readTestdata(t, "nodesc/SKILL.md")
	_, diags, err = ParseSpec(data, path)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if !hasDiagnostic(diags, DiagnosticMissingDesc) {
		t.Errorf("expected missing-description diagnostic, got %v", diags)
	}
}

func TestParseSpecDerivesNameFromPath(t *testing.T) {
	data := []byte("---\ndescription: no name here\n---\nbody\n")
	spec, _, err := ParseSpec(data, filepath.Join("root", "derived-name", "SKILL.md"))
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.Name != "derived-name" {
		t.Errorf("Name = %q, want derived-name", spec.Name)
	}

	spec, _, err = ParseSpec(data, filepath.Join("root", "loose.md"))
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if spec.Name != "loose" {
		t.Errorf("Name = %q, want loose", spec.Name)
	}
}

func TestParseSpecUnclosedFrontmatter(t *testing.T) {
	_, _, err := ParseSpec([]byte("---\nname: broken\n"), "broken/SKILL.md")
	if err == nil {
		t.Fatal("expected an error for unclosed frontmatter")
	}
}

func TestValidateSkillName(t *testing.T) {
	tests := []struct {
		name    string
		valid   bool
		wantSub string
	}{
		{name: "pdf-tools", valid: true},
		{name: "a", valid: true},
		{name: "a1-b2-c3", valid: true},
		{name: strings.Repeat("a", MaxNameLength), valid: true},
		{name: "", valid: false, wantSub: "required"},
		{name: "PDF-tools", valid: false, wantSub: "lowercase"},
		{name: "pdf_tools", valid: false, wantSub: "lowercase"},
		{name: "-pdf", valid: false, wantSub: "start or end"},
		{name: "pdf-", valid: false, wantSub: "start or end"},
		{name: "pdf--tools", valid: false, wantSub: "consecutive"},
		{name: strings.Repeat("a", MaxNameLength+1), valid: false, wantSub: "exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := ValidateSkillName(tt.name)
			if tt.valid && len(problems) != 0 {
				t.Fatalf("ValidateSkillName(%q) = %v, want valid", tt.name, problems)
			}
			if !tt.valid {
				if len(problems) == 0 {
					t.Fatalf("ValidateSkillName(%q) = valid, want problems", tt.name)
				}
				if tt.wantSub != "" && !strings.Contains(strings.Join(problems, "; "), tt.wantSub) {
					t.Fatalf("ValidateSkillName(%q) = %v, want substring %q", tt.name, problems, tt.wantSub)
				}
			}
		})
	}
}

func TestValidateSkillDescription(t *testing.T) {
	if problems := ValidateSkillDescription(""); len(problems) == 0 {
		t.Error("empty description should be invalid")
	}
	if problems := ValidateSkillDescription("   "); len(problems) == 0 {
		t.Error("whitespace description should be invalid")
	}
	if problems := ValidateSkillDescription(strings.Repeat("x", MaxDescriptionLength)); len(problems) != 0 {
		t.Errorf("max-length description should be valid, got %v", problems)
	}
	if problems := ValidateSkillDescription(strings.Repeat("x", MaxDescriptionLength+1)); len(problems) == 0 {
		t.Error("over-length description should be invalid")
	}
}

func TestToSkillAppliesLegacyDefaults(t *testing.T) {
	data := []byte("---\nname: defaults\ndescription: defaults test\n---\nbody\n")
	spec, _, err := ParseSpec(data, "defaults/SKILL.md")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	skill := spec.ToSkill("body")
	if skill.Priority != 100 {
		t.Errorf("Priority = %d, want 100", skill.Priority)
	}
	if skill.Timeout != 60*time.Second {
		t.Errorf("Timeout = %v, want 60s", skill.Timeout)
	}
	if skill.Content != "body" {
		t.Errorf("Content = %q", skill.Content)
	}
	if skill.DisableModelInvocation {
		t.Error("DisableModelInvocation = true")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
