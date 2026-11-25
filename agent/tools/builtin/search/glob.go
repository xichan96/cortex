package search

import (
	"context"
	_ "embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	cortexfs "github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type GlobTool struct {
	workspace string
}

func NewGlobTool(workspace string) types.Tool {
	return &GlobTool{workspace: workspace}
}

func (t *GlobTool) Name() string {
	return "Glob"
}

//go:embed glob.txt
var globDescription string

func (t *GlobTool) Description() string {
	return globDescription
}

func (t *GlobTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "The Glob pattern to match files against, e.g. \"**/*.go\".",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Directory to search in (defaults to the workspace root).",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GlobTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	pattern := inputString(input, "pattern")
	if pattern == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("pattern is required"))
	}

	root := inputString(input, "path")
	if root == "" {
		if t.workspace == "*" {
			root = "."
		} else {
			root = t.workspace
		}
	}
	absRoot, err := cortexfs.SafePath(ctx, t.workspace, root)
	if err != nil {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(err)
	}

	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("path %q: %w", absRoot, err))
	}

	// An absolute pattern is split into its fixed prefix (search root) and the
	// remaining relative pattern, preserving the old call style.
	walkRoot := absRoot
	relPattern := filepath.ToSlash(pattern)
	if filepath.IsAbs(pattern) {
		// An absolute pattern may point outside the workspace; validate it with
		// the same confinement rules as `path`.
		if _, err := cortexfs.SafePath(ctx, t.workspace, pattern); err != nil {
			return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(err)
		}
		// Cut the pattern at its first wildcard segment: everything before it is
		// the walk root, everything from it on is the relative pattern.
		segs := strings.Split(filepath.ToSlash(pattern), "/")
		cut := len(segs)
		for i, seg := range segs {
			if strings.ContainsAny(seg, "*?[") {
				cut = i
				break
			}
		}
		fixed := strings.Join(segs[:cut], "/")
		walkRoot = fixed
		if walkRoot == "" {
			walkRoot = "/"
		}
		relPattern = strings.Join(segs[cut:], "/")
		if relPattern == "" {
			relPattern = filepath.Base(pattern)
		}
	}

	if !info.IsDir() {
		absRoot = filepath.Dir(absRoot)
		walkRoot = absRoot
		relPattern = filepath.Base(pattern)
	}

	matcher := loadGitignore(t.workspace)
	workspaceBase := absRoot
	if filepath.IsAbs(pattern) {
		workspaceBase = walkRoot
	}
	var out []string
	err = filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(walkRoot, p)
		if relErr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if d.IsDir() {
			if p != walkRoot && shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			if p != walkRoot && matcher.Match(relToWorkspace(t.workspace, workspaceBase, p)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if rel == "." {
			return nil
		}
		if matcher.Match(relToWorkspace(t.workspace, workspaceBase, p)) {
			return nil
		}
		if !globPatternMatches(relPattern, relSlash, d.Name()) {
			return nil
		}
		out = append(out, relToWorkspace(t.workspace, workspaceBase, p))
		return nil
	})
	if err != nil {
		return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(err)
	}

	sort.Strings(out)
	return strings.Join(out, "\n"), nil
}

// globPatternMatches matches rel (root-relative) and base (filename) against a
// doublestar pattern. A bare pattern such as "*.go" matches at any depth.
func globPatternMatches(pattern, rel, base string) bool {
	if !strings.Contains(pattern, "/") {
		if ok, _ := doublestar.Match(pattern, base); ok {
			return true
		}
	}
	ok, _ := doublestar.Match(pattern, rel)
	if ok {
		return true
	}
	// `**.go` is accepted as shorthand for `**/*.go`.
	if strings.HasPrefix(pattern, "**") && !strings.HasPrefix(pattern, "**/") {
		if ok, _ := doublestar.Match("**/*"+strings.TrimPrefix(pattern, "**"), rel); ok {
			return true
		}
	}
	return false
}

func (t *GlobTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "Glob",
		IsFromToolkit:  false,
		ToolType:       "search",
		Extra: map[string]interface{}{
			"no_cache": true,
		},
	}
}
