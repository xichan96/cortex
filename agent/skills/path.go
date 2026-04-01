package skills

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

func (s *Skill) MatchesPath(filePath string) bool {
	if len(s.Paths) == 0 {
		return false
	}
	fp := filepath.ToSlash(filepath.Clean(filePath))
	for _, pattern := range s.Paths {
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
