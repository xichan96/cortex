package fs

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xichan96/cortex/agent/types"
)

func defaultWorkspace(workspace string) string {
	if workspace == "" {
		return "."
	}
	return workspace
}

func SafePath(workspace, requested string) (string, error) {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("invalid workspace path: %w", err)
	}
	var absRequested string
	if filepath.IsAbs(requested) {
		absRequested = filepath.Clean(requested)
	} else {
		absRequested = filepath.Join(absWorkspace, requested)
	}
	absRequested = filepath.Clean(absRequested)
	rel, err := filepath.Rel(absWorkspace, absRequested)
	if err != nil {
		return "", fmt.Errorf("access denied: path %s is outside workspace %s", requested, workspace)
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("access denied: path %s is outside workspace %s", requested, workspace)
	}
	return absRequested, nil
}

func schemaObject(properties map[string]interface{}, required []string) map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

func metadataFS(sourceNodeName string) types.ToolMetadata {
	return types.ToolMetadata{
		SourceNodeName: sourceNodeName,
		IsFromToolkit:  false,
		ToolType:       "fs",
	}
}
