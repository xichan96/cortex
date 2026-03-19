// loopdetect.go: loop detection for repeated/similar actions.
package utils

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
	LoopDetectWindowSize = 10
	LoopDetectMaxRepeats = 5
)

type LoopDetectAction struct {
	Type      string    `json:"type"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type LoopDetectResult struct {
	IsLoop     bool    `json:"is_loop"`
	Count      int     `json:"count"`
	Similarity float64 `json:"similarity"`
	Suggestion string  `json:"suggestion"`
}

type LoopDetectStats struct {
	TotalActions int            `json:"total_actions"`
	ActionCounts map[string]int `json:"action_counts"`
}

type LoopDetectConfig struct {
	Enabled             bool
	MaxRepeats          int
	SimilarityThreshold float64
}

func DefaultLoopDetectConfig() *LoopDetectConfig {
	return &LoopDetectConfig{
		Enabled:             true,
		MaxRepeats:          LoopDetectMaxRepeats,
		SimilarityThreshold: 0.8,
	}
}

type LoopDetector interface {
	Detect(ctx context.Context, sessionID string, action LoopDetectAction) *LoopDetectResult
	Record(sessionID string, action LoopDetectAction)
	Reset(sessionID string)
	GetStats(sessionID string) LoopDetectStats
}

type loopDetector struct {
	config  *LoopDetectConfig
	actions map[string]*sessionActions
	mu      sync.RWMutex
}

type ringBuffer struct {
	data  []LoopDetectAction
	size  int
	head  int
	count int
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{
		data: make([]LoopDetectAction, size),
		size: size,
	}
}

func (r *ringBuffer) add(action LoopDetectAction) {
	r.data[r.head] = action
	r.head = (r.head + 1) % r.size
	if r.count < r.size {
		r.count++
	}
}

func (r *ringBuffer) getAll() []LoopDetectAction {
	if r.count == 0 {
		return nil
	}
	result := make([]LoopDetectAction, r.count)
	for i := 0; i < r.count; i++ {
		idx := (r.head - r.count + i + r.size) % r.size
		result[i] = r.data[idx]
	}
	return result
}

func (r *ringBuffer) len() int {
	return r.count
}

type sessionActions struct {
	buffer *ringBuffer
}

func NewLoopDetector(cfg *LoopDetectConfig) LoopDetector {
	if cfg == nil {
		cfg = DefaultLoopDetectConfig()
	}
	return &loopDetector{
		config:  cfg,
		actions: make(map[string]*sessionActions),
	}
}

func (d *loopDetector) Detect(ctx context.Context, sessionID string, action LoopDetectAction) *LoopDetectResult {
	if !d.config.Enabled {
		return &LoopDetectResult{IsLoop: false}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	sa, exists := d.actions[sessionID]
	if !exists || sa.buffer.len() == 0 {
		return &LoopDetectResult{IsLoop: false}
	}

	actions := sa.buffer.getAll()
	windowSize := LoopDetectWindowSize
	if len(actions) < windowSize {
		windowSize = len(actions)
	}

	recentActions := actions[len(actions)-windowSize:]
	counts := make(map[string]int)

	for _, a := range recentActions {
		key := loopDetectKey(a.Type, a.Content)
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
		suggestion := loopDetectSuggestion(maxKey)
		return &LoopDetectResult{
			IsLoop:     true,
			Count:      maxCount,
			Similarity: float64(maxCount) / float64(windowSize),
			Suggestion: suggestion,
		}
	}

	return &LoopDetectResult{IsLoop: false}
}

func (d *loopDetector) Record(sessionID string, action LoopDetectAction) {
	if !d.config.Enabled {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	sa, exists := d.actions[sessionID]
	if !exists {
		sa = &sessionActions{
			buffer: newRingBuffer(LoopDetectWindowSize),
		}
		d.actions[sessionID] = sa
	}

	sa.buffer.add(action)
}

func (d *loopDetector) Reset(sessionID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.actions, sessionID)
}

func (d *loopDetector) GetStats(sessionID string) LoopDetectStats {
	d.mu.RLock()
	defer d.mu.RUnlock()

	sa, exists := d.actions[sessionID]
	if !exists {
		return LoopDetectStats{
			ActionCounts: make(map[string]int),
		}
	}

	actions := sa.buffer.getAll()
	counts := make(map[string]int)
	for _, a := range actions {
		key := loopDetectKey(a.Type, a.Content)
		counts[key]++
	}

	return LoopDetectStats{
		TotalActions: sa.buffer.len(),
		ActionCounts: counts,
	}
}

func loopDetectKey(toolName, input string) string {
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

func loopDetectSuggestion(key string) string {
	if strings.Contains(key, "bash") || strings.Contains(key, "execute_command") {
		return "Detected repeated command execution. Consider trying a different approach."
	}
	if strings.Contains(key, "read_file") || strings.Contains(key, "glob") {
		return "Detected repeated file operations. Try specifying a more specific path."
	}
	return "Detected repeated actions. The agent may be stuck in a loop."
}
