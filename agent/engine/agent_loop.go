package engine

import (
	"strings"

	"github.com/xichan96/cortex/agent/types"
)

const maxRecentKeys = 20

type doomLoopState struct {
	recentKeys   []string
	recentInputs []map[string]interface{}
}

func (s *doomLoopState) appendSteps(steps []types.ToolCallData) {
	for _, step := range steps {
		toolInput, _ := step.Action.ToolInput.(map[string]interface{})
		s.recentKeys = append(s.recentKeys, generateLoopKey(step.Action.Tool, toolInput))
		s.recentInputs = append(s.recentInputs, toolInput)
	}
	if len(s.recentKeys) > maxRecentKeys {
		s.recentKeys = s.recentKeys[len(s.recentKeys)-maxRecentKeys:]
		s.recentInputs = s.recentInputs[len(s.recentInputs)-maxRecentKeys:]
	}
}

func (s *doomLoopState) shouldStop(threshold int, onDoomLoop func(string, map[string]interface{}) bool) bool {
	doom, lastKey := checkDoomLoop(s.recentKeys, threshold)
	if !doom {
		return false
	}
	toolName := lastKey
	if idx := strings.Index(lastKey, ":"); idx > 0 {
		toolName = lastKey[:idx]
	}
	var lastInput map[string]interface{}
	for i := len(s.recentKeys) - 1; i >= 0; i-- {
		if s.recentKeys[i] == lastKey {
			lastInput = s.recentInputs[i]
			break
		}
	}
	return onDoomLoop == nil || !onDoomLoop(toolName, lastInput)
}

func generateLoopKey(toolName string, args map[string]interface{}) string {
	if toolName == "execute_command" {
		if cmd, ok := args["command"].(string); ok {
			cmd = strings.TrimSpace(cmd)
			parts := strings.Fields(cmd)
			if len(parts) >= 2 {
				return toolName + ":" + parts[0] + " " + parts[1]
			}
			return toolName + ":" + cmd
		}
	}
	return generateToolCacheKey(toolName, args)
}

func checkDoomLoop(recentKeys []string, threshold int) (doom bool, lastKey string) {
	if threshold <= 0 || len(recentKeys) < threshold {
		return false, ""
	}
	counts := make(map[string]int)
	for _, key := range recentKeys {
		counts[key]++
	}
	for _, key := range recentKeys {
		if counts[key] >= threshold {
			return true, key
		}
	}
	return false, ""
}
