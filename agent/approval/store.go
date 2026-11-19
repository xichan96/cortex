package approval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// ErrStoreUnavailable is returned when a scope asks for persistence but no
// store is configured for it.
var ErrStoreUnavailable = errors.New("approval store unavailable")

// PermissionRecord is one remembered decision. Records are keyed by
// (Tool, Pattern); an empty pattern is normalized to "*" by stores.
type PermissionRecord struct {
	Tool      string        `json:"tool"`
	Pattern   string        `json:"pattern,omitempty"`
	Scope     ApprovalScope `json:"scope"`
	Decision  Action        `json:"decision"`
	CreatedAt time.Time     `json:"created_at"`
	Source    string        `json:"source,omitempty"`
}

// RecordKey is the stable lookup key for a (tool, pattern) pair.
func RecordKey(tool, pattern string) string {
	if pattern == "" {
		pattern = "*"
	}
	return tool + ":" + pattern
}

// Key returns the record's stable lookup key.
func (r PermissionRecord) Key() string { return RecordKey(r.Tool, r.Pattern) }

// Store is the remembered-permission interface. Lookup must be safe for
// concurrent readers; Put/Remove must be safe for concurrent writers.
type Store interface {
	Lookup(tool, pattern string) (PermissionRecord, bool)
	Put(rec PermissionRecord) error
	Remove(tool, pattern string) error
	List() []PermissionRecord
}

// MemoryStore is an in-memory Store used for session-scoped permissions and in
// tests.
type MemoryStore struct {
	mu      sync.RWMutex
	records map[string]PermissionRecord
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]PermissionRecord)}
}

// Lookup implements Store.
func (s *MemoryStore) Lookup(tool, pattern string) (PermissionRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.records[RecordKey(tool, pattern)]
	return rec, ok
}

// Put implements Store. CreatedAt is filled in when zero.
func (s *MemoryStore) Put(rec PermissionRecord) error {
	if rec.Tool == "" {
		return errors.New("approval: record has no tool")
	}
	if rec.Pattern == "" {
		rec.Pattern = "*"
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = make(map[string]PermissionRecord)
	}
	s.records[rec.Key()] = rec
	return nil
}

// Remove implements Store.
func (s *MemoryStore) Remove(tool, pattern string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, RecordKey(tool, pattern))
	return nil
}

// List implements Store, sorted by key.
func (s *MemoryStore) List() []PermissionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PermissionRecord, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// permissionsFile is the on-disk permissions.json document.
type permissionsFile struct {
	Version int                `json:"version"`
	Records []PermissionRecord `json:"records"`
}

const permissionsFileVersion = 1

// FileStore persists permissions as JSON and writes atomically (temp file +
// rename) so a crash cannot leave a half-written permissions.json.
type FileStore struct {
	mu      sync.Mutex
	path    string
	records map[string]PermissionRecord
	loaded  bool
	loadErr error
	now     func() time.Time
}

// NewFileStore returns a FileStore backed by path. The file is read lazily on
// first use; a missing file is an empty store.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path, now: func() time.Time { return time.Now().UTC() }}
}

// Path returns the backing file path.
func (s *FileStore) Path() string { return s.path }

func (s *FileStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	s.records = make(map[string]PermissionRecord)
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.loadErr = err
		}
		return
	}
	if len(data) == 0 {
		return
	}
	var doc permissionsFile
	if err := json.Unmarshal(data, &doc); err != nil {
		s.loadErr = fmt.Errorf("approval: parse %s: %w", s.path, err)
		return
	}
	for _, rec := range doc.Records {
		if rec.Tool == "" {
			continue
		}
		if rec.Pattern == "" {
			rec.Pattern = "*"
		}
		s.records[rec.Key()] = rec
	}
}

// Lookup implements Store.
func (s *FileStore) Lookup(tool, pattern string) (PermissionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	rec, ok := s.records[RecordKey(tool, pattern)]
	return rec, ok
}

// Put implements Store and persists the file atomically.
func (s *FileStore) Put(rec PermissionRecord) error {
	if rec.Tool == "" {
		return errors.New("approval: record has no tool")
	}
	if rec.Pattern == "" {
		rec.Pattern = "*"
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = s.now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if s.loadErr != nil {
		return s.loadErr
	}
	if s.records == nil {
		s.records = make(map[string]PermissionRecord)
	}
	s.records[rec.Key()] = rec
	return s.saveLocked()
}

// Remove implements Store and persists the file atomically.
func (s *FileStore) Remove(tool, pattern string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	if s.loadErr != nil {
		return s.loadErr
	}
	delete(s.records, RecordKey(tool, pattern))
	return s.saveLocked()
}

// List implements Store, sorted by key.
func (s *FileStore) List() []PermissionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	out := make([]PermissionRecord, 0, len(s.records))
	for _, rec := range s.records {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

func (s *FileStore) saveLocked() error {
	recs := make([]PermissionRecord, 0, len(s.records))
	for _, rec := range s.records {
		recs = append(recs, rec)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Key() < recs[j].Key() })
	data, err := json.MarshalIndent(permissionsFile{Version: permissionsFileVersion, Records: recs}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := writeFileAtomic(s.path, data); err != nil {
		s.loadErr = err
		return err
	}
	s.loadErr = nil
	return nil
}

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by rename. Permissions files are 0600 because they may record
// sensitive paths.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".permissions-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = "" // renamed: nothing to clean up
	return nil
}
