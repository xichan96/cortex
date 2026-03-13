package skills

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/xichan96/cortex/pkg/logger"
)

// Planner handles skill intention recognition and execution planning
type Planner struct {
	registry *Registry
}

// NewPlanner creates a new skill planner
func NewPlanner(r *Registry) *Planner {
	return &Planner{registry: r}
}

// Plan creates an execution plan based on the input text
func (p *Planner) Plan(ctx context.Context, input string) (*ExecutionPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// 1. Identify skills based on input
	matchedSkills := p.registry.MatchAll(input)
	if len(matchedSkills) == 0 {
		return &ExecutionPlan{}, nil
	}

	// 2. Resolve dependencies
	// We need to ensure all dependencies of matched skills are also included
	allSkillsMap, err := p.resolveDependencies(matchedSkills)
	if err != nil {
		return nil, err
	}

	// 3. Create execution steps
	steps := p.createExecutionSteps(allSkillsMap)

	return &ExecutionPlan{Steps: steps}, nil
}

// resolveDependencies recursively finds all required skills
func (p *Planner) resolveDependencies(initialSkills []*Skill) (map[string]*Skill, error) {
	resolved := make(map[string]*Skill)
	queue := make([]*Skill, len(initialSkills))
	copy(queue, initialSkills)

	for len(queue) > 0 {
		skill := queue[0]
		queue = queue[1:]

		if _, exists := resolved[skill.Name]; exists {
			continue
		}
		resolved[skill.Name] = skill

		for _, depName := range skill.Dependencies {
			depSkill, found := p.registry.Get(depName)
			if !found {
				return nil, fmt.Errorf("dependency skill %q not found for skill %q", depName, skill.Name)
			}
			if _, exists := resolved[depName]; !exists {
				queue = append(queue, depSkill)
			}
		}
	}
	return resolved, nil
}

// createExecutionSteps groups skills into parallel/serial steps
func (p *Planner) createExecutionSteps(skillsMap map[string]*Skill) []ExecutionStep {
	var steps []ExecutionStep
	remaining := make(map[string]*Skill)
	for k, v := range skillsMap {
		remaining[k] = v
	}
	completed := make(map[string]bool)

	// Simple topological sort-like execution grouping
	for len(remaining) > 0 {
		var currentBatch []*Skill

		// Find skills whose dependencies are all met
		for _, skill := range remaining {
			allDepsMet := true
			for _, dep := range skill.Dependencies {
				if _, ok := completed[dep]; !ok {
					allDepsMet = false
					break
				}
			}
			if allDepsMet {
				currentBatch = append(currentBatch, skill)
			}
		}

		if len(currentBatch) == 0 {
			// Circular dependency or missing dependency detected
			// For robustness, we might want to return what we have or error.
			// Here we just stop to avoid infinite loop.
			// In a real system, we should probably log an error.
			logger.Warn("Circular dependency detected among skills", slog.Any("skills", getSkillNames(remaining)))
			break
		}

		// Determine mode
		mode := ModeSerial
		if len(currentBatch) > 1 {
			mode = ModeParallel
		}

		steps = append(steps, ExecutionStep{
			Mode:   mode,
			Skills: currentBatch,
		})

		// Mark as completed
		for _, s := range currentBatch {
			completed[s.Name] = true
			delete(remaining, s.Name)
		}
	}

	return steps
}

func getSkillNames(m map[string]*Skill) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
