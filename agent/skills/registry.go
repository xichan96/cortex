package skills

import (
	"context"
	"regexp"
	"strings"
	"sync"

	"github.com/xichan96/cortex/pkg/logger"
)

type regexEntry struct {
	re    *regexp.Regexp
	skill *Skill
}

// Registry manages the registration and lookup of skills
type Registry struct {
	mu       sync.RWMutex
	skills   map[string]*Skill
	commands map[string]*Skill
	keywords map[string]*Skill
	patterns []*regexEntry
}

// NewRegistry creates a new skill registry
func NewRegistry() *Registry {
	return &Registry{
		skills:   make(map[string]*Skill),
		commands: make(map[string]*Skill),
		keywords: make(map[string]*Skill),
	}
}

// Register adds a skill to the registry and indexes its triggers
func (r *Registry) Register(skill *Skill) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.skills[skill.Name] = skill
	for _, t := range skill.Triggers {
		switch t.Type {
		case "command":
			cmd := strings.ToLower(strings.TrimSpace(t.Pattern))
			if !strings.HasPrefix(cmd, "/") {
				cmd = "/" + cmd
			}
			r.commands[cmd] = skill
		case "keyword":
			r.keywords[strings.ToLower(strings.TrimSpace(t.Pattern))] = skill
		case "regex":
			if re, err := regexp.Compile(t.Pattern); err == nil {
				r.patterns = append(r.patterns, &regexEntry{re: re, skill: skill})
			}
		}
	}
}

// Get returns a skill by name
func (r *Registry) Get(name string) (*Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.skills[name]
	return s, ok
}

// LookupCommand finds a skill by command trigger (e.g., "/help")
func (r *Registry) LookupCommand(cmd string) (*Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.commands[strings.ToLower(cmd)]
	return s, ok
}

// LookupKeyword finds a skill by keyword trigger
func (r *Registry) LookupKeyword(text string) (*Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	lower := strings.ToLower(text)
	for kw, skill := range r.keywords {
		if strings.Contains(lower, kw) {
			return skill, true
		}
	}
	return nil, false
}

// MatchRegex finds the first skill matching a regex trigger
func (r *Registry) MatchRegex(text string) (*Skill, []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, entry := range r.patterns {
		if matches := entry.re.FindStringSubmatch(text); matches != nil {
			return entry.skill, matches
		}
	}
	return nil, nil
}

// MatchAll finds all skills matching the input via command, keyword, or regex
func (r *Registry) MatchAll(text string) []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()

	uniqueSkills := make(map[*Skill]struct{})
	lower := strings.ToLower(strings.TrimSpace(text))

	// 1. Check commands
	if strings.HasPrefix(lower, "/") {
		parts := strings.Fields(lower)
		if len(parts) > 0 {
			if s, ok := r.commands[parts[0]]; ok {
				uniqueSkills[s] = struct{}{}
			}
		}
	}

	// 2. Check keywords
	for kw, skill := range r.keywords {
		if strings.Contains(lower, kw) {
			uniqueSkills[skill] = struct{}{}
		}
	}

	// 3. Check regex
	for _, entry := range r.patterns {
		if entry.re.MatchString(text) {
			uniqueSkills[entry.skill] = struct{}{}
		}
	}

	result := make([]*Skill, 0, len(uniqueSkills))
	for s := range uniqueSkills {
		result = append(result, s)
	}
	return result
}

// All returns all registered skills
func (r *Registry) All() []*Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s)
	}
	return out
}

// LoadSkills registers a list of skills
func (r *Registry) LoadSkills(skills []Skill) {
	for i := range skills {
		r.Register(&skills[i])
	}
}

// LoadFromDirs scans dirs for SKILL.md, loads them and registers into the registry.
func (r *Registry) LoadFromDirs(ctx context.Context, l *logger.Logger, dirs []string) error {
	list, err := LoadSkillsFromDirs(ctx, l, dirs)
	if err != nil {
		return err
	}
	r.LoadSkills(list)
	return nil
}
