package skills

import (
	"context"
	"reflect"
	"testing"
)

func TestSkillPolicyFromEntry(t *testing.T) {
	if got := FromEntry(nil); !got.Empty() {
		t.Errorf("FromEntry(nil) = %+v, want zero policy", got)
	}

	entry := &Entry{
		Name: "pdf",
		Spec: &Spec{
			AllowedTools: []string{"read", "write"},
			Cortex:       CortexMetadata{Paths: []string{"**/*.pdf"}},
		},
	}
	policy := FromEntry(entry)
	if !reflect.DeepEqual(policy.AllowedTools, []string{"read", "write"}) {
		t.Errorf("AllowedTools = %v", policy.AllowedTools)
	}
	if !reflect.DeepEqual(policy.Paths, []string{"**/*.pdf"}) {
		t.Errorf("Paths = %v", policy.Paths)
	}
	if policy.Empty() {
		t.Error("policy with constraints should not be Empty")
	}

	// The policy must not alias the entry's slices.
	policy.AllowedTools[0] = "mutated"
	policy.Paths[0] = "mutated"
	if entry.Spec.AllowedTools[0] != "read" || entry.Spec.Cortex.Paths[0] != "**/*.pdf" {
		t.Error("FromEntry must return copies")
	}

	if !(SkillPolicy{}).Empty() {
		t.Error("zero policy should be Empty")
	}
}

func TestSkillPolicyFilterToolNames(t *testing.T) {
	available := []string{"read", "write", "Bash"}

	filtered, unknown := FilterToolNames(available, nil)
	if !reflect.DeepEqual(filtered, available) {
		t.Errorf("empty allowed = %v, want %v", filtered, available)
	}
	if unknown != nil {
		t.Errorf("empty allowed unknown = %v, want nil", unknown)
	}
	filtered[0] = "mutated"
	if available[0] != "read" {
		t.Error("empty allowed must return a copy, not alias the input")
	}

	filtered, unknown = FilterToolNames(available, []string{"Bash", "read"})
	if !reflect.DeepEqual(filtered, []string{"read", "Bash"}) {
		t.Errorf("subset = %v, want available order [read Bash]", filtered)
	}
	if unknown != nil {
		t.Errorf("subset unknown = %v, want nil", unknown)
	}

	filtered, unknown = FilterToolNames(available, []string{"read", "does-not-exist", "does-not-exist"})
	if !reflect.DeepEqual(filtered, []string{"read"}) {
		t.Errorf("with unknown = %v, want [read]", filtered)
	}
	if !reflect.DeepEqual(unknown, []string{"does-not-exist"}) {
		t.Errorf("unknown = %v, want deduplicated [does-not-exist]", unknown)
	}

	filtered, unknown = FilterToolNames(available, []string{"nope"})
	if len(filtered) != 0 {
		t.Errorf("no match = %v, want empty", filtered)
	}
	if !reflect.DeepEqual(unknown, []string{"nope"}) {
		t.Errorf("no match unknown = %v", unknown)
	}
}

func TestSkillPolicyEntryMatchesPath(t *testing.T) {
	var nilEntry *Entry
	if nilEntry.MatchesPath("main.go") {
		t.Error("nil entry must not match")
	}

	entry := &Entry{Spec: &Spec{Cortex: CortexMetadata{Paths: []string{"**/*.go", "src/**"}}}}
	cases := map[string]bool{
		"main.go":         true,
		"pkg/main.go":     true,
		"src/app/main.go": true,
		"README.md":       false,
		"docs/src/a.txt":  false,
	}
	for path, want := range cases {
		if got := entry.MatchesPath(path); got != want {
			t.Errorf("MatchesPath(%q) = %v, want %v", path, got, want)
		}
	}

	if (&Entry{}).MatchesPath("main.go") {
		t.Error("entry without paths must not match")
	}
	if MatchPathPatterns([]string{"", "  "}, "main.go") {
		t.Error("empty patterns must not match")
	}
}

func TestSkillPolicyAdvertisableForPaths(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "always/SKILL.md", skillDoc("always", "Always applicable.", "body"))
	writeFile(t, root, "go/SKILL.md", "---\nname: go\ndescription: Go files.\nmetadata:\n  cortex:\n    paths: [\"**/*.go\"]\n---\nbody\n")
	writeFile(t, root, "src/SKILL.md", "---\nname: src\ndescription: Source tree.\nmetadata:\n  cortex:\n    paths: [\"src/**\"]\n---\nbody\n")
	writeFile(t, root, "hidden/SKILL.md", "---\nname: hidden\ndescription: Hidden.\ndisable-model-invocation: true\n---\nbody\n")

	ix, _, err := LoadIndex(context.Background(), []string{root}, DiscoverOptions{})
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}

	names := func(entries []*Entry) []string {
		out := make([]string, 0, len(entries))
		for _, entry := range entries {
			out = append(out, entry.Name)
		}
		return out
	}

	if got := AdvertisableForPaths(nil, nil); got != nil {
		t.Errorf("nil index = %v, want nil", got)
	}

	// No active paths: every advertisable skill (hidden excluded).
	got := names(AdvertisableForPaths(ix, nil))
	for _, want := range []string{"always", "go", "src"} {
		if !containsString(got, want) {
			t.Errorf("no active paths missing %q: %v", want, got)
		}
	}
	if containsString(got, "hidden") {
		t.Errorf("hidden must never be advertisable: %v", got)
	}

	// A non-matching active file advertises only unscoped skills.
	if got := names(AdvertisableForPaths(ix, []string{"README.md"})); !reflect.DeepEqual(got, []string{"always"}) {
		t.Errorf("README.md = %v, want [always]", got)
	}

	// Go Glob matches nested files.
	got = names(AdvertisableForPaths(ix, []string{"cmd/main.go"}))
	if !reflect.DeepEqual(got, []string{"always", "go"}) {
		t.Errorf("cmd/main.go = %v, want [always go]", got)
	}

	// Both path-scoped skills match a file under src.
	got = names(AdvertisableForPaths(ix, []string{"src/app/main.go"}))
	if !reflect.DeepEqual(got, []string{"always", "go", "src"}) {
		t.Errorf("src/app/main.go = %v, want [always go src]", got)
	}
}
