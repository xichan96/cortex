package search

import (
	"context"
	_ "embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	cortexfs "github.com/xichan96/cortex/agent/tools/builtin/fs"
	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/pkg/errors"
)

type GlobTool struct {
	workspace string
}

func NewGlobTool(workspace string) types.Tool {
	return &GlobTool{
		workspace: workspace,
	}
}

func (t *GlobTool) Name() string {
	return "glob"
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
				"description": "The glob pattern to match files with.",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GlobTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	pattern, ok := input["pattern"].(string)
	if !ok || pattern == "" {
		return nil, errors.EC_PARAMETER_MISSING.Wrap(fmt.Errorf("pattern is required"))
	}

	searchPath, err := cortexfs.SafePath(ctx, t.workspace, pattern)
	if err != nil {
		return nil, errors.EC_TOOL_PARAMETER_INVALID.Wrap(err)
	}

	var matches []string
	if strings.Contains(pattern, "**") {
		// filepath.Glob 不支持 **（递归通配）。手写：用 ** 切分 pattern，WalkDir
		// 逐段 glob 匹配（P3，tools-codex-eval §8.5）。
		matches = globDoubleStar(searchPath)
	} else {
		matches, err = filepath.Glob(searchPath)
		if err != nil {
			return nil, errors.EC_TOOL_EXECUTION_FAILED.Wrap(fmt.Errorf("glob failed: %w", err))
		}
	}

	var filtered []string
	for _, m := range matches {
		if _, err := cortexfs.SafePath(ctx, t.workspace, m); err == nil {
			filtered = append(filtered, m)
		}
	}

	return filtered, nil
}

// globDoubleStar supports ** in a glob pattern: ** matches zero or more path
// segments (doublestar semantics). filepath.Glob rejects **, so we walk the
// tree and match each relative path against the pattern with a segment-wise
// recursive matcher. Paths and pattern are normalized to '/'.
func globDoubleStar(pattern string) []string {
	pattern = filepath.ToSlash(pattern)
	// Split the pattern at the FIRST **. Everything before ** is the fixed walk
	// root (prefix); `**` plus everything after is matched per descendant via
	// dsMatch (which understands ** segments).
	prefix, after, hasAfter := strings.Cut(pattern, "**")
	root := strings.TrimRight(prefix, "/")
	if root == "" {
		root = "."
	}
	if !hasAfter {
		// Pattern is entirely `**` (or `x/**`): every descendant matches.
		after = "*"
	}

	var out []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable dirs; partial results acceptable
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		// Pattern after ** is `after`; match rel against `**` + after by
		// prepending `**/` (the segment we cut out).
		if dsMatch(relSlash, "**/"+strings.TrimLeft(after, "/")) {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// dsMatch reports whether relPath (segments joined with '/') matches pattern,
// where ** matches zero or more segments and * matches within one segment.
// It is segment-wise to keep ** semantics correct.
//
// `**.ext` is normalized to `**/*.ext` (doublestar convention: ** followed by
// a file extension matches any file with that extension at any depth).
func dsMatch(relPath, pattern string) bool {
	pattern = normalizeDoubleStar(pattern)
	psegs := strings.Split(pattern, "/")
	rsegs := strings.Split(relPath, "/")
	return dsMatchSegs(rsegs, psegs)
}

// normalizeDoubleStar rewrites a `**` that is fused with a file extension into
// its doublestar-segment form. `**.go` → `**/*.go` (any-depth .go file).
func normalizeDoubleStar(p string) string {
	if strings.HasPrefix(p, "**") && !strings.HasPrefix(p, "**/") {
		rest := strings.TrimPrefix(p, "**")
		// `**.go`: rest=".go" → need "*" before ".go" → "**/*.go".
		if strings.HasPrefix(rest, ".") {
			return "**/*" + rest
		}
		return "**/" + strings.TrimLeft(rest, "/")
	}
	return p
}

func dsMatchSegs(rel, pat []string) bool {
	if len(pat) == 0 {
		return len(rel) == 0
	}
	// ** consumes zero or more segments.
	if pat[0] == "**" {
		// Collapse consecutive **.
		i := 0
		for i < len(pat) && pat[i] == "**" {
			i++
		}
		rest := pat[i:]
		// ** matches zero segments.
		if dsMatchSegs(rel, rest) {
			return true
		}
		// ** matches one or more segments.
		for j := 1; j <= len(rel); j++ {
			if dsMatchSegs(rel[j:], rest) {
				return true
			}
		}
		return false
	}
	if len(rel) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], rel[0])
	if err != nil || !ok {
		return false
	}
	return dsMatchSegs(rel[1:], pat[1:])
}

func (t *GlobTool) Metadata() types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: "glob",
		IsFromToolkit:  false,
		ToolType:       "search",
	}
}
