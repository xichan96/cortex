package skills

import (
	"sort"
	"testing"
)

func TestPlanner_Plan(t *testing.T) {
	registry := NewRegistry()

	// Define skills with unique patterns to avoid accidental matches
	skillA := &Skill{Name: "SkillA", Triggers: []Trigger{{Type: "keyword", Pattern: "TriggerA"}}}
	skillB := &Skill{Name: "SkillB", Triggers: []Trigger{{Type: "keyword", Pattern: "TriggerB"}}}
	skillC := &Skill{Name: "SkillC", Triggers: []Trigger{{Type: "keyword", Pattern: "TriggerC"}}, Dependencies: []string{"SkillB"}}
	skillD := &Skill{Name: "SkillD", Triggers: []Trigger{{Type: "keyword", Pattern: "TriggerD"}}, Dependencies: []string{"SkillB"}}
	skillE := &Skill{Name: "SkillE", Triggers: []Trigger{{Type: "keyword", Pattern: "TriggerE"}}, Dependencies: []string{"SkillA"}}

	// Register skills
	registry.Register(skillA)
	registry.Register(skillB)
	registry.Register(skillC)
	registry.Register(skillD)
	registry.Register(skillE)

	planner := NewPlanner(registry)

	tests := []struct {
		name     string
		input    string
		expected []ExecutionStep
	}{
		{
			name:  "Single Skill Independent",
			input: "Run TriggerA",
			expected: []ExecutionStep{
				{Mode: ModeSerial, Skills: []*Skill{skillA}},
			},
		},
		{
			name:  "Two Independent Skills",
			input: "Run TriggerA and TriggerB",
			expected: []ExecutionStep{
				{Mode: ModeParallel, Skills: []*Skill{skillA, skillB}},
			},
		},
		{
			name:  "Skill with Dependency (Input matches Child)",
			input: "Run TriggerC", // C depends on B
			expected: []ExecutionStep{
				{Mode: ModeSerial, Skills: []*Skill{skillB}},
				{Mode: ModeSerial, Skills: []*Skill{skillC}},
			},
		},
		{
			name:  "Complex Dependency (B->C, B->D)",
			input: "Run TriggerC and TriggerD", // Both depend on B
			expected: []ExecutionStep{
				{Mode: ModeSerial, Skills: []*Skill{skillB}},           // B must run first
				{Mode: ModeParallel, Skills: []*Skill{skillC, skillD}}, // Then C and D can run in parallel
			},
		},
		{
			name:  "Independent and Dependent Mixed",
			input: "Run TriggerA and TriggerC", // A is independent, C depends on B
			expected: []ExecutionStep{
				{Mode: ModeParallel, Skills: []*Skill{skillA, skillB}}, // A and B (dep of C) can run
				{Mode: ModeSerial, Skills: []*Skill{skillC}},           // Then C
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := planner.Plan(tt.input)
			if err != nil {
				t.Fatalf("Plan() error = %v", err)
			}

			if len(plan.Steps) != len(tt.expected) {
				t.Fatalf("Plan() steps count = %d, want %d", len(plan.Steps), len(tt.expected))
			}

			for i, step := range plan.Steps {
				expStep := tt.expected[i]
				if step.Mode != expStep.Mode {
					t.Errorf("Step[%d] Mode = %v, want %v", i, step.Mode, expStep.Mode)
				}
				if !compareSkills(step.Skills, expStep.Skills) {
					t.Errorf("Step[%d] Skills = %v, want %v", i, getSkillNamesList(step.Skills), getSkillNamesList(expStep.Skills))
				}
			}
		})
	}
}

func compareSkills(a, b []*Skill) bool {
	if len(a) != len(b) {
		return false
	}
	// Sort by name for comparison
	sort.Slice(a, func(i, j int) bool { return a[i].Name < a[j].Name })
	sort.Slice(b, func(i, j int) bool { return b[i].Name < b[j].Name })

	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

func getSkillNamesList(skills []*Skill) []string {
	names := make([]string, len(skills))
	for i, s := range skills {
		names[i] = s.Name
	}
	return names
}
