package loop

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

const (
	WindowSize = 10
	MaxRepeats = 5
)

type Action struct {
	Type      string    `json:"type"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type Result struct {
	IsLoop     bool    `json:"is_loop"`
	Count      int     `json:"count"`
	Similarity float64 `json:"similarity"`
	Suggestion string  `json:"suggestion"`
}

type Stats struct {
	TotalActions int            `json:"total_actions"`
	ActionCounts map[string]int `json:"action_counts"`
}

type Config struct {
	Enabled             bool    `yaml:"enabled"`
	MaxRepeats          int     `yaml:"max_repeats"`
	SimilarityThreshold float64 `yaml:"similarity_threshold"`
}

func DefaultConfig() *Config {
	return &Config{
		Enabled:             true,
		MaxRepeats:          MaxRepeats,
		SimilarityThreshold: 0.8,
	}
}

type Detector interface {
	Detect(ctx context.Context, sessionID string, action Action) *Result
	Record(sessionID string, action Action)
	Reset(sessionID string)
	GetStats(sessionID string) Stats
}

type detector struct {
	config  *Config
	actions map[string][]Action
	mu      sync.RWMutex
}

func NewDetector(cfg *Config) Detector {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	return &detector{
		config:  cfg,
		actions: make(map[string][]Action),
	}
}

func (d *detector) Detect(ctx context.Context, sessionID string, action Action) *Result {
	if !d.config.Enabled {
		return &Result{IsLoop: false}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	actions, exists := d.actions[sessionID]
	if !exists || len(actions) == 0 {
		return &Result{IsLoop: false}
	}

	windowSize := WindowSize
	if len(actions) < windowSize {
		windowSize = len(actions)
	}

	recentActions := actions[len(actions)-windowSize:]
	counts := make(map[string]int)

	for _, a := range recentActions {
		key := generateKey(a.Type, a.Content)
		counts[key]++
	}

	maxCount := 0
	maxKey := ""
	for key, count := range counts {
		if count > maxCount {
			maxCount = count
			maxKey = key
		}
	}

	if maxCount >= d.config.MaxRepeats {
		suggestion := getSuggestion(maxKey)
		return &Result{
			IsLoop:     true,
			Count:      maxCount,
			Similarity: float64(maxCount) / float64(windowSize),
			Suggestion: suggestion,
		}
	}

	return &Result{IsLoop: false}
}

func (d *detector) Record(sessionID string, action Action) {
	if !d.config.Enabled {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.actions[sessionID]; !exists {
		d.actions[sessionID] = make([]Action, 0)
	}

	d.actions[sessionID] = append(d.actions[sessionID], action)

	if len(d.actions[sessionID]) > WindowSize*2 {
		d.actions[sessionID] = d.actions[sessionID][len(d.actions[sessionID])-WindowSize:]
	}
}

func (d *detector) Reset(sessionID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.actions, sessionID)
}

func (d *detector) GetStats(sessionID string) Stats {
	d.mu.RLock()
	defer d.mu.RUnlock()

	actions, exists := d.actions[sessionID]
	if !exists {
		return Stats{
			ActionCounts: make(map[string]int),
		}
	}

	counts := make(map[string]int)
	for _, a := range actions {
		key := generateKey(a.Type, a.Content)
		counts[key]++
	}

	return Stats{
		TotalActions: len(actions),
		ActionCounts: counts,
	}
}

func generateKey(toolName, input string) string {
	if toolName == "execute_command" || toolName == "bash" {
		var args struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(input), &args) == nil && args.Command != "" {
			cmd := strings.TrimSpace(args.Command)
			parts := strings.Fields(cmd)
			if len(parts) >= 2 {
				return toolName + ":" + parts[0] + " " + parts[1]
			}
			return toolName + ":" + cmd
		}
	}
	h := md5.New()
	h.Write([]byte("tool:" + toolName + ":"))
	h.Write([]byte(input))
	return toolName + ":" + hex.EncodeToString(h.Sum(nil))
}

func getSuggestion(key string) string {
	if strings.Contains(key, "bash") || strings.Contains(key, "execute_command") {
		return "Detected repeated command execution. Consider trying a different approach."
	}
	if strings.Contains(key, "read_file") || strings.Contains(key, "glob") {
		return "Detected repeated file operations. Try specifying a more specific path."
	}
	return "Detected repeated actions. The agent may be stuck in a loop."
}
