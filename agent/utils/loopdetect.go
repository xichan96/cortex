// loopdetect.go: loop detection for repeated/similar actions.
package utils

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

const (
	LoopDetectWindowSize     = 10
	LoopDetectMaxRepeats     = 5
	LoopDetectWarningRepeats = 3
	LoopDetectExactMatchSize = 5
)

type LoopDetectAction struct {
	Type      string    `json:"type"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

type LoopDetectResult struct {
	IsLoop     bool    `json:"is_loop"`
	IsWarning  bool    `json:"is_warning"`
	Count      int     `json:"count"`
	Similarity float64 `json:"similarity"`
	Suggestion string  `json:"suggestion"`
	Level      string  `json:"level"` // "warning" or "critical"
}

type LoopDetectStats struct {
	TotalActions int            `json:"total_actions"`
	ActionCounts map[string]int `json:"action_counts"`
}

type LoopDetectConfig struct {
	Enabled             bool
	MaxRepeats          int
	WarningRepeats      int
	SimilarityThreshold float64
	TrackResults        bool
	InputMaxRepeats     int
}

func DefaultLoopDetectConfig() *LoopDetectConfig {
	return &LoopDetectConfig{
		Enabled:             true,
		MaxRepeats:          LoopDetectMaxRepeats,
		WarningRepeats:      LoopDetectWarningRepeats,
		SimilarityThreshold: 0.8,
		TrackResults:        true,
		InputMaxRepeats:     10,
	}
}

type LoopDetector interface {
	Detect(ctx context.Context, sessionID string, action LoopDetectAction) *LoopDetectResult
	Record(sessionID string, action LoopDetectAction)
	RecordWithResult(sessionID string, action LoopDetectAction, resultHash string)
	Reset(sessionID string)
	GetStats(sessionID string) LoopDetectStats
}

type toolCallRecord struct {
	Action     LoopDetectAction
	ResultHash string
}

type loopDetector struct {
	config  *LoopDetectConfig
	actions map[string]*sessionActions
	mu      sync.RWMutex
}

type ringBuffer struct {
	data  []toolCallRecord
	size  int
	head  int
	count int
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{
		data: make([]toolCallRecord, size),
		size: size,
	}
}

func (r *ringBuffer) add(record toolCallRecord) {
	r.data[r.head] = record
	r.head = (r.head + 1) % r.size
	if r.count < r.size {
		r.count++
	}
}

func (r *ringBuffer) getAll() []toolCallRecord {
	if r.count == 0 {
		return nil
	}
	result := make([]toolCallRecord, r.count)
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

	records := sa.buffer.getAll()
	windowSize := LoopDetectWindowSize
	if len(records) < windowSize {
		windowSize = len(records)
	}

	recentRecords := records[len(records)-windowSize:]
	counts := make(map[string]int)
	inputCounts := make(map[string]int)

	for _, a := range recentRecords {
		key := loopDetectKey(a.Action.Type, a.Action.Content)
		counts[key]++
		if a.Action.Type == "input" {
			inputCounts[key]++
		}
	}

	maxCount := 0
	maxKey := ""
	for key, count := range counts {
		if count > maxCount {
			maxCount = count
			maxKey = key
		}
	}

	isInputType := action.Type == "input"
	maxRepeats := d.config.MaxRepeats
	warningRepeats := d.config.WarningRepeats

	if isInputType {
		inputMaxRepeats := d.config.InputMaxRepeats
		if inputMaxRepeats <= 0 {
			inputMaxRepeats = 10
		}
		maxRepeats = inputMaxRepeats
		warningRepeats = inputMaxRepeats / 2
		if warningRepeats < 3 {
			warningRepeats = 3
		}
	}

	if warningRepeats <= 0 {
		warningRepeats = LoopDetectWarningRepeats
	}

	if maxCount >= maxRepeats {
		suggestion := loopDetectSuggestion(maxKey)
		return &LoopDetectResult{
			IsLoop:     true,
			IsWarning:  false,
			Count:      maxCount,
			Similarity: float64(maxCount) / float64(windowSize),
			Suggestion: suggestion,
			Level:      "critical",
		}
	}

	if maxCount >= warningRepeats {
		suggestion := loopDetectWarning(maxKey)
		return &LoopDetectResult{
			IsLoop:     false,
			IsWarning:  true,
			Count:      maxCount,
			Similarity: float64(maxCount) / float64(windowSize),
			Suggestion: suggestion,
			Level:      "warning",
		}
	}

	return &LoopDetectResult{IsLoop: false}
}

func (d *loopDetector) Record(sessionID string, action LoopDetectAction) {
	d.RecordWithResult(sessionID, action, "")
}

func (d *loopDetector) RecordWithResult(sessionID string, action LoopDetectAction, resultHash string) {
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

	sa.buffer.add(toolCallRecord{
		Action:     action,
		ResultHash: resultHash,
	})
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

	records := sa.buffer.getAll()
	counts := make(map[string]int)
	for _, a := range records {
		key := loopDetectKey(a.Action.Type, a.Action.Content)
		counts[key]++
	}

	return LoopDetectStats{
		TotalActions: sa.buffer.len(),
		ActionCounts: counts,
	}
}

func loopDetectCommandKey(toolName, cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	parts := strings.Fields(cmd)
	if len(parts) >= 2 {
		return toolName + ":" + parts[0] + " " + parts[1]
	}
	return toolName + ":" + cmd
}

func loopDetectKey(toolName, input string) string {
	if toolName == "execute_command" || toolName == "bash" || toolName == "command" {
		var args struct {
			Command string `json:"command"`
		}
		err := json.Unmarshal([]byte(input), &args)
		if err == nil && args.Command != "" {
			if k := loopDetectCommandKey(toolName, args.Command); k != "" {
				return k
			}
		}
		if err != nil {
			if k := loopDetectCommandKey(toolName, input); k != "" {
				return k
			}
		}
	}
	h := sha256.New()
	h.Write([]byte("tool:" + toolName + ":"))
	h.Write([]byte(input))
	return toolName + ":" + hex.EncodeToString(h.Sum(nil))[:16]
}

func loopDetectWarning(key string) string {
	if strings.Contains(key, "bash") || strings.Contains(key, "execute_command") || strings.HasPrefix(key, "command:") {
		return "Warning: repeated command execution detected. Consider using a different approach or combining commands."
	}
	if strings.Contains(key, "read_file") || strings.Contains(key, "glob") || strings.Contains(key, "grep") {
		return "Warning: repeated file operations detected. Try to be more specific with paths or use different search strategies."
	}
	return "Warning: repeated actions detected. Consider verifying the current state before continuing."
}

func loopDetectSuggestion(key string) string {
	if strings.Contains(key, "bash") || strings.Contains(key, "execute_command") || strings.HasPrefix(key, "command:") {
		return "Detected repeated command execution. Consider trying a different approach."
	}
	if strings.Contains(key, "read_file") || strings.Contains(key, "glob") {
		return "Detected repeated file operations. Try specifying a more specific path."
	}
	return "Detected repeated actions. The agent may be stuck in a loop."
}
