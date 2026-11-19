package home

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ContextFileCandidates is the per-directory precedence order for project
// instruction files. Exactly one file per directory is loaded.
var ContextFileCandidates = []string{
	"AGENTS.override.md",
	"AGENTS.md",
	"AGENTS.MD",
	"CORTEX.md",
	"CLAUDE.md",
	"CLAUDE.MD",
}

// ContextFile is one loaded instruction file.
type ContextFile struct {
	Path    string
	Scope   string // "user" for the global agent home file, "project" otherwise
	Content string
}

// LoadContextFiles loads project instruction files the way pi does:
//
//  1. one global file from the agent home root;
//  2. then one file per directory from the filesystem root down to cwd.
//
// AGENTS.override.md takes the same directory over the other candidates but
// does not suppress any other directory or the global file. Unreadable files
// produce diagnostics and are skipped, never an error. BOMs are stripped.
//
// Duplicate files (for example a symlink pointing at an already-loaded file)
// are loaded once. Nested git-worktree shadowing is not resolved here; callers
// receive both files if they genuinely differ.
func LoadContextFiles(ctx context.Context, p Paths) ([]ContextFile, []Diagnostic, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	cwd := strings.TrimSpace(p.CWD)
	if cwd == "" {
		return nil, nil, fmt.Errorf("home: context files: empty working directory")
	}
	if resolved, err := ExpandPath(cwd); err == nil {
		cwd = resolved
	}
	agentDir := strings.TrimSpace(p.AgentDir)
	if agentDir == "" {
		agentDir = p.Root
	}
	agentDir = CanonicalPath(agentDir)

	var files []ContextFile
	var diags []Diagnostic
	seen := map[string]bool{}

	appendFile := func(dir, scope string) {
		file, fileDiags := loadContextFileFromDir(ctx, dir, scope)
		diags = append(diags, fileDiags...)
		if file == nil {
			return
		}
		key := CanonicalPath(file.Path)
		if seen[key] {
			diags = append(diags, Diagnostic{
				File:     file.Path,
				Layer:    scopeLayer(scope),
				Severity: SeverityInfo,
				Reason:   "duplicate context file already loaded; skipped",
			})
			return
		}
		seen[key] = true
		files = append(files, ContextFile{Path: file.Path, Scope: scope, Content: file.Content})
	}

	// Global first.
	if agentDir != "" {
		appendFile(agentDir, "user")
	}

	// Ancestors from the filesystem root down to cwd, so nearer files override
	// farther ones when rendered in order.
	dirs := ancestorDirs(cwd)
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return files, diags, err
		}
		if agentDir != "" && samePath(dir, agentDir) {
			continue
		}
		appendFile(dir, "project")
	}
	if err := ctx.Err(); err != nil {
		return files, diags, err
	}

	return files, diags, nil
}

func scopeLayer(scope string) Layer {
	if scope == "user" {
		return LayerUser
	}
	return LayerProject
}

// ancestorDirs returns dirs from the filesystem root to dir inclusive.
func ancestorDirs(dir string) []string {
	dir = filepath.Clean(dir)
	var reversed []string
	current := dir
	for {
		reversed = append(reversed, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	dirs := make([]string, len(reversed))
	for i, d := range reversed {
		dirs[len(reversed)-1-i] = d
	}
	return dirs
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b) || CanonicalPath(a) == CanonicalPath(b)
}

func loadContextFileFromDir(ctx context.Context, dir, scope string) (*ContextFile, []Diagnostic) {
	var diags []Diagnostic
	layer := scopeLayer(scope)
	for _, name := range ContextFileCandidates {
		if err := ctx.Err(); err != nil {
			break
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			diags = append(diags, Diagnostic{
				File:     path,
				Layer:    layer,
				Severity: SeverityWarning,
				Reason:   fmt.Sprintf("stat failed: %v", err),
			})
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			diags = append(diags, Diagnostic{
				File:     path,
				Layer:    layer,
				Severity: SeverityWarning,
				Reason:   fmt.Sprintf("read failed: %v", err),
			})
			continue
		}
		return &ContextFile{
			Path:    path,
			Content: string(stripBOM(data)),
		}, diags
	}
	return nil, diags
}
