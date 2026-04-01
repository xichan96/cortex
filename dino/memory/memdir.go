package memory

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/xichan96/cortex/agent/tools/prompts"
)

type MemoryType string

const (
	MemoryTypeUser      MemoryType = "user"
	MemoryTypeFeedback  MemoryType = "feedback"
	MemoryTypeProject   MemoryType = "project"
	MemoryTypeReference MemoryType = "reference"
)

type MemoryEntry struct {
	ID        string     `json:"id"`
	Type      MemoryType `json:"type"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	Hook      string     `json:"hook"`
	Tags      []string   `json:"tags,omitempty"`
	SourceID  string     `json:"source_id,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type MemDirProvider interface {
	GetIndex(maxLines int) (string, error)
	GetEntry(id string) (*MemoryEntry, error)
	AddEntry(entry *MemoryEntry) error
	UpdateEntry(entry *MemoryEntry) error
	Search(query string, limit int) ([]MemoryEntry, error)
	DeleteEntry(id string) error
}

type MemDir struct {
	mu      sync.RWMutex
	entries map[string]*MemoryEntry
}

func NewMemDir() *MemDir {
	return &MemDir{entries: make(map[string]*MemoryEntry)}
}

func DirectoryWriteGuidance() string {
	return prompts.DirExistsGuidance
}

func (m *MemDir) GetIndex(maxLines int) (string, error) {
	if maxLines <= 0 {
		maxLines = 200
	}
	m.mu.RLock()
	list := make([]*MemoryEntry, 0, len(m.entries))
	for _, e := range m.entries {
		list = append(list, e)
	}
	m.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool {
		if list[i].UpdatedAt.Equal(list[j].UpdatedAt) {
			return list[i].Title < list[j].Title
		}
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
	lines := []string{"# MEMORY", ""}
	for _, g := range strings.Split(DirectoryWriteGuidance(), "\n") {
		lines = append(lines, g)
	}
	lines = append(lines, "")
	for _, e := range list {
		if len(lines) >= maxLines {
			lines = append(lines, "… (truncated)")
			break
		}
		lines = append(lines, fmt.Sprintf("- [%s](%s.md) — %s (%s)", e.Title, e.ID, e.Hook, e.Type))
	}
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, "… (truncated)")
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func (m *MemDir) GetEntry(id string) (*MemoryEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[id]
	if !ok {
		return nil, fmt.Errorf("memory entry not found: %s", id)
	}
	out := *e
	return &out, nil
}

func (m *MemDir) AddEntry(entry *MemoryEntry) error {
	if entry == nil {
		return fmt.Errorf("nil entry")
	}
	if entry.Title == "" || entry.Hook == "" {
		return fmt.Errorf("title and hook are required")
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = now
	}
	entry.UpdatedAt = now
	if entry.Type == "" {
		entry.Type = MemoryTypeProject
	}
	cp := *entry
	m.entries[entry.ID] = &cp
	return nil
}

func (m *MemDir) UpdateEntry(entry *MemoryEntry) error {
	if entry == nil || entry.ID == "" {
		return fmt.Errorf("entry id required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.entries[entry.ID]
	if !ok {
		return fmt.Errorf("memory entry not found: %s", entry.ID)
	}
	if entry.Title != "" {
		existing.Title = entry.Title
	}
	if entry.Content != "" {
		existing.Content = entry.Content
	}
	if entry.Hook != "" {
		existing.Hook = entry.Hook
	}
	if entry.Type != "" {
		existing.Type = entry.Type
	}
	if len(entry.Tags) > 0 {
		existing.Tags = append([]string(nil), entry.Tags...)
	}
	if entry.SourceID != "" {
		existing.SourceID = entry.SourceID
	}
	existing.UpdatedAt = time.Now()
	return nil
}

func (m *MemDir) Search(query string, limit int) ([]MemoryEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	q := strings.ToLower(strings.TrimSpace(query))
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []MemoryEntry
	for _, e := range m.entries {
		if q == "" ||
			strings.Contains(strings.ToLower(e.Title), q) ||
			strings.Contains(strings.ToLower(e.Hook), q) ||
			strings.Contains(strings.ToLower(e.Content), q) {
			cp := *e
			out = append(out, cp)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *MemDir) DeleteEntry(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[id]; !ok {
		return fmt.Errorf("memory entry not found: %s", id)
	}
	delete(m.entries, id)
	return nil
}

var _ MemDirProvider = (*MemDir)(nil)
