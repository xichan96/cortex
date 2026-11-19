package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xichan96/cortex/agent/skills/prompt"
	"github.com/xichan96/cortex/pkg/logger"
)

const SkillFileName = "SKILL.md"

type loadConfig struct {
	resolveSymlinks bool
	dedupeRealpath  bool
}

func defaultLoadConfig() loadConfig {
	return loadConfig{resolveSymlinks: true, dedupeRealpath: true}
}

type LoadOption func(*loadConfig)

func WithResolveSymlinks(v bool) LoadOption {
	return func(c *loadConfig) { c.resolveSymlinks = v }
}

func WithDedupeByRealpath(v bool) LoadOption {
	return func(c *loadConfig) { c.dedupeRealpath = v }
}

func realpathKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	r, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return r
}

// LoadSkillsFromDirs scans the provided directories for **/SKILL.md files and loads them (opencode-compatible).
//
// It eagerly loads bodies and is kept for backwards compatibility. New code
// should prefer Discover or LoadIndex, which parse metadata only and read
// bodies on demand.
func LoadSkillsFromDirs(ctx context.Context, l *logger.Logger, dirs []string, opts ...LoadOption) ([]Skill, error) {
	cfg := defaultLoadConfig()
	for _, o := range opts {
		o(&cfg)
	}

	var skills []Skill
	seen := make(map[string]struct{})

	tryAppend := func(skill Skill) {
		key := skill.Path
		if cfg.dedupeRealpath {
			key = realpathKey(skill.Path)
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		skills = append(skills, skill)
	}

	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		absDir, err := filepath.Abs(dir)
		if err != nil {
			if l != nil {
				l.Warn(fmt.Sprintf("could not get absolute path for %s: %v", dir, err))
			}
			absDir = dir
		}

		walkRoot := absDir
		if cfg.resolveSymlinks {
			if r, err := filepath.EvalSymlinks(absDir); err == nil {
				walkRoot = r
			}
		}

		err = filepath.Walk(walkRoot, func(path string, info os.FileInfo, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				if l != nil {
					l.Warn(fmt.Sprintf("error accessing path %q: %v", path, err))
				}
				return nil
			}

			if !info.IsDir() && strings.EqualFold(info.Name(), SkillFileName) {
				skill, diags, err := loadSkillFromFile(path)
				if err != nil {
					if l != nil {
						l.Warn(fmt.Sprintf("skipping invalid skill file %s: %v", path, err))
					}
					return nil
				}
				if l != nil {
					for _, d := range diags {
						l.Warn(fmt.Sprintf("skill %s: %s", path, d.Message))
					}
				}
				tryAppend(skill)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("error walking directory %s: %w", dir, err)
		}
	}

	return skills, nil
}

// loadSkillFromFile reads a SKILL.md file and extracts metadata and body.
// Unlike discovery, the legacy loader tolerates a missing description so that
// existing callers keep working unchanged.
func loadSkillFromFile(path string) (Skill, []Diagnostic, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, nil, err
	}
	doc, diags, err := ParseDocument(data, path)
	if err != nil {
		return Skill{}, diags, err
	}
	return doc.Spec.ToSkill(doc.Body), diags, nil
}

func BuildSystemPromptInjectionForActivePaths(skills []Skill, activePaths []string) string {
	return BuildSystemPromptInjection(FilterSkillsForActivePaths(skills, activePaths))
}

func BuildSystemPromptInjection(skills []Skill) string {
	entries := make([]prompt.Entry, 0, len(skills))
	for _, s := range skills {
		entries = append(entries, prompt.Entry{
			Name: s.Name, Description: s.Description, Path: s.Path,
			WhenToUse: s.WhenToUse, Paths: append([]string(nil), s.Paths...),
			AllowedTools: append([]string(nil), s.AllowedTools...),
		})
	}
	return prompt.BuildSystemPromptInjection(entries)
}

func BuildSystemPromptInjectionWithTriggers(skills []*Skill) string {
	entries := make([]*prompt.EntryWithTriggers, 0, len(skills))
	for _, s := range skills {
		e := &prompt.EntryWithTriggers{
			Name: s.Name, Description: s.Description,
			WhenToUse: s.WhenToUse, Paths: append([]string(nil), s.Paths...),
			AllowedTools: append([]string(nil), s.AllowedTools...),
		}
		for _, t := range s.Triggers {
			e.Triggers = append(e.Triggers, prompt.Trigger{Type: t.Type, Pattern: t.Pattern})
		}
		entries = append(entries, e)
	}
	return prompt.BuildSystemPromptInjectionWithTriggers(entries)
}
