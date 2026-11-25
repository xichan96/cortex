package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xichan96/cortex/agent/types"
)

var ErrOutsideWorkspace = errors.New("path outside workspace")

type pathApproveCtxKey struct{}

type prefixApproveCtxKey struct{}

func ContextWithApprovedPrefix(ctx context.Context, dir string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	d := filepath.Clean(dir)
	if d == "" || d == "." {
		return ctx
	}
	var list []string
	if existing, ok := ctx.Value(prefixApproveCtxKey{}).([]string); ok {
		list = append(list, existing...)
	}
	list = append(list, d)
	return context.WithValue(ctx, prefixApproveCtxKey{}, list)
}

func isUnderApprovedPrefix(ctx context.Context, abs string) bool {
	prefixes, ok := ctx.Value(prefixApproveCtxKey{}).([]string)
	if !ok || len(prefixes) == 0 {
		return false
	}
	cleanAbs := filepath.Clean(abs)
	for _, pref := range prefixes {
		pref = filepath.Clean(pref)
		if pref == "" {
			continue
		}
		if cleanAbs == pref {
			return true
		}
		rel, err := filepath.Rel(pref, cleanAbs)
		if err != nil {
			continue
		}
		if rel == "." {
			return true
		}
		if !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func ContextWithApprovedPath(ctx context.Context, abs string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	clean := filepath.Clean(abs)
	var m map[string]struct{}
	if existing, ok := ctx.Value(pathApproveCtxKey{}).(map[string]struct{}); ok {
		m = make(map[string]struct{}, len(existing)+1)
		for k := range existing {
			m[k] = struct{}{}
		}
	} else {
		m = make(map[string]struct{})
	}
	m[clean] = struct{}{}
	return context.WithValue(ctx, pathApproveCtxKey{}, m)
}

func isPathApproved(ctx context.Context, abs string) bool {
	if ctx == nil {
		return false
	}
	m, ok := ctx.Value(pathApproveCtxKey{}).(map[string]struct{})
	if !ok {
		return false
	}
	_, ok = m[filepath.Clean(abs)]
	return ok
}

func IsUnderWorkspaceRoot(absWorkspace, absRequested string) bool {
	rel, err := filepath.Rel(absWorkspace, absRequested)
	if err != nil {
		return false
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return false
	}
	return true
}

func ResolveAbsRequested(workspace, requested string) (absWorkspace string, absRequested string, err error) {
	if workspace == "*" {
		return "", "", nil
	}
	absWorkspace, err = filepath.Abs(workspace)
	if err != nil {
		return "", "", fmt.Errorf("invalid workspace path: %w", err)
	}
	if filepath.IsAbs(requested) {
		absRequested = filepath.Clean(requested)
	} else {
		absRequested = filepath.Clean(filepath.Join(absWorkspace, requested))
	}
	return absWorkspace, absRequested, nil
}

func defaultWorkspace(workspace string) string {
	if workspace == "" {
		return "."
	}
	return workspace
}

func EffectiveWorkingDir(workspace string) string {
	if workspace == "*" {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
		return "."
	}
	return workspace
}

func SafePath(ctx context.Context, workspace, requested string) (string, error) {
	if workspace == "*" {
		if strings.TrimSpace(requested) == "" {
			return "", fmt.Errorf("path required")
		}
		if filepath.IsAbs(requested) {
			return filepath.Clean(requested), nil
		}
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("invalid workspace path: %w", err)
		}
		return filepath.Clean(filepath.Join(wd, requested)), nil
	}
	absWorkspace, absRequested, err := ResolveAbsRequested(workspace, requested)
	if err != nil {
		return "", err
	}
	if IsUnderWorkspaceRoot(absWorkspace, absRequested) {
		return absRequested, nil
	}
	if isPathApproved(ctx, absRequested) || isUnderApprovedPrefix(ctx, absRequested) {
		return absRequested, nil
	}
	return "", fmt.Errorf("%w: %s", ErrOutsideWorkspace, requested)
}

func schemaObject(properties map[string]interface{}, required []string) map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

// pathMutationLocks serializes read-modify-write operations per absolute path
// across concurrent tool calls and subagents in the same process. Reads do not
// take the lock: Edit/Write publish via temp-file rename, so readers only ever
// see the old or the new complete file.
var pathMutationLocks sync.Map // abs path -> *sync.Mutex

func lockPathMutation(absPath string) func() {
	v, _ := pathMutationLocks.LoadOrStore(absPath, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func metadataFS(sourceNodeName string) types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: sourceNodeName,
		IsFromToolkit:  false,
		ToolType:       "fs",
	}
}

// metadataFSNoCache marks a filesystem tool as uncacheable. Read resolves live
// filesystem state, so a cached observation can only go stale after a write.
func metadataFSNoCache(sourceNodeName string) types.ToolMetadata {
	m := metadataFS(sourceNodeName)
	m.Extra = map[string]interface{}{"no_cache": true}
	// Read-only filesystem calls may run concurrently with sibling calls.
	m.ConcurrencySafe = true
	m.ExecutionMode = types.ExecutionModeParallel
	return m
}

// metadataFSInvalidating marks a mutating filesystem tool: it is never cached
// itself and its successful execution invalidates every cached tool result.
func metadataFSInvalidating(sourceNodeName string) types.ToolMetadata {
	m := metadataFS(sourceNodeName)
	m.Extra = map[string]interface{}{
		"no_cache":          true,
		"invalidates_cache": true,
	}
	// Mutating calls are barriers: never run alongside siblings.
	m.ConcurrencySafe = false
	m.ExecutionMode = types.ExecutionModeSequential
	return m
}

// pathArg returns the file path argument, preferring the Claude-aligned
// "file_path" key and falling back to the legacy "path" key for in-process
// callers.
func pathArg(input map[string]interface{}) string {
	if p, ok := input["file_path"].(string); ok && p != "" {
		return p
	}
	if p, ok := input["path"].(string); ok {
		return p
	}
	return ""
}
