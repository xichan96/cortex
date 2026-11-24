package skills

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// MatchPathPatterns reports whether filePath matches at least one doublestar
// Glob pattern. Patterns are trimmed and normalized to slash form; empty
// patterns are ignored. Invalid patterns simply do not match.
func MatchPathPatterns(patterns []string, filePath string) bool {
	if len(patterns) == 0 {
		return false
	}
	fp := filepath.ToSlash(filepath.Clean(filePath))
	for _, pattern := range patterns {
		p := strings.TrimSpace(pattern)
		if p == "" {
			continue
		}
		p = filepath.ToSlash(filepath.Clean(p))
		if ok, _ := doublestar.PathMatch(p, fp); ok {
			return true
		}
	}
	return false
}

func (s *Skill) MatchesPath(filePath string) bool {
	return MatchPathPatterns(s.Paths, filePath)
}

// MatchesPath reports whether filePath matches one of the entry's declared
// paths globs. An entry with no paths never path-matches; callers that treat
// "no paths" as "always applicable" must check that first.
func (e *Entry) MatchesPath(filePath string) bool {
	if e == nil {
		return false
	}
	return MatchPathPatterns(e.Paths(), filePath)
}

func FilterSkillsForActivePaths(skills []Skill, activePaths []string) []Skill {
	if len(activePaths) == 0 {
		out := make([]Skill, 0, len(skills))
		for _, s := range skills {
			if len(s.Paths) == 0 {
				out = append(out, s)
			}
		}
		return out
	}
	out := make([]Skill, 0, len(skills))
	for _, s := range skills {
		if len(s.Paths) == 0 {
			out = append(out, s)
			continue
		}
		for _, ap := range activePaths {
			if s.MatchesPath(ap) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}
