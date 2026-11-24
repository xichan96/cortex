package search

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// noiseDirs are directory names that are never worth searching: VCS metadata,
// dependency trees and build output. They mirror the --exclude-dir list the old
// Grep-backed implementation used.
var noiseDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
	".next":        {},
	"dist":         {},
	"build":        {},
	"target":       {},
}

func shouldSkipDir(name string) bool {
	_, ok := noiseDirs[name]
	return ok
}

// ignoreRule is one parsed line of a .gitignore file.
type ignoreRule struct {
	pattern  string
	negate   bool
	dirOnly  bool
	anchored bool
}

// gitignoreMatcher applies the workspace-root .gitignore to relative paths.
// Nested .gitignore files are not parsed; the old Grep implementation also only
// passed the root file via --exclude-from.
type gitignoreMatcher []ignoreRule

// loadGitignore reads <workspace>/.gitignore. A missing file (or an unbound
// "*" workspace) yields an empty matcher.
func loadGitignore(workspace string) gitignoreMatcher {
	if workspace == "*" || strings.TrimSpace(workspace) == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".gitignore"))
	if err != nil {
		return nil
	}
	var rules gitignoreMatcher
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			r.negate = true
			line = strings.TrimPrefix(line, "!")
		}
		if strings.HasSuffix(line, "/") {
			r.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		if strings.HasPrefix(line, "/") {
			r.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		// A pattern with an inner slash is anchored to the root even without a
		// leading slash (git semantics).
		if strings.Contains(line, "/") {
			r.anchored = true
		}
		if line == "" {
			continue
		}
		r.pattern = line
		rules = append(rules, r)
	}
	return rules
}

// Match reports whether rel (slash-separated, relative to the workspace root)
// is ignored. Later rules win so `!reinclude` lines work.
func (m gitignoreMatcher) Match(rel string) bool {
	if len(m) == 0 {
		return false
	}
	rel = filepath.ToSlash(strings.TrimPrefix(rel, "./"))
	ignored := false
	for _, r := range m {
		if r.matches(rel) {
			ignored = !r.negate
		}
	}
	return ignored
}

func (r ignoreRule) matches(rel string) bool {
	pat := r.pattern
	if r.anchored || strings.Contains(pat, "/") {
		if ok, _ := doublestar.Match(pat, rel); ok {
			return true
		}
		// Directory-only patterns also cover the directory's current path even
		// when the caller checks a descendant that was not pruned yet.
		if r.dirOnly {
			return strings.HasPrefix(rel, pat+"/")
		}
		return false
	}
	// Unanchored basename pattern: matches at any depth.
	for _, seg := range strings.Split(rel, "/") {
		if ok, _ := doublestar.Match(pat, seg); ok {
			return true
		}
	}
	return false
}

// relToWorkspace renders p relative to the workspace root (slash-separated).
// Paths outside the workspace are returned absolute so they stay unambiguous.
func relToWorkspace(workspace, root, p string) string {
	base := root
	if workspace != "" && workspace != "*" {
		if abs, err := filepath.Abs(workspace); err == nil {
			base = abs
		}
	}
	rel, err := filepath.Rel(base, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(filepath.Clean(p))
	}
	return filepath.ToSlash(rel)
}

// inputString / inputBool / inputInt accept the loosely typed values that can
// arrive from JSON decoding as well as native Go values.
func inputString(input map[string]interface{}, key string) string {
	if v, ok := input[key].(string); ok {
		return v
	}
	return ""
}

func inputBool(input map[string]interface{}, key string) bool {
	switch v := input[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	}
	return false
}

func inputInt(input map[string]interface{}, key string) int {
	switch v := input[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	}
	return 0
}
