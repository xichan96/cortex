package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type BlobStore interface {
	Save(ctx context.Context, key string, data []byte) error
	Load(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

type MemoryBlobStore struct {
	mu sync.RWMutex
	m  map[string][]byte
}

func NewMemoryBlobStore() *MemoryBlobStore {
	return &MemoryBlobStore{m: make(map[string][]byte)}
}

func (s *MemoryBlobStore) Save(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := append([]byte(nil), data...)
	s.m[key] = cp
	return nil
}

func (s *MemoryBlobStore) Load(_ context.Context, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.m[key]
	if !ok {
		return nil, nil
	}
	return append([]byte(nil), b...), nil
}

func (s *MemoryBlobStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}

func (s *MemoryBlobStore) ListKeys(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.m))
	for k := range s.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

type DirBlobStore struct {
	root string
	mu   sync.Mutex
}

func NewDirBlobStore(root string) (*DirBlobStore, error) {
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0750); err != nil {
		return nil, err
	}
	return &DirBlobStore{root: root}, nil
}

func (s *DirBlobStore) blobPath(key string) string {
	h := sha256.Sum256([]byte(key))
	return filepath.Join(s.root, hex.EncodeToString(h[:])+".blob")
}

func (s *DirBlobStore) Save(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dest := s.blobPath(key)
	f, err := os.CreateTemp(s.root, ".w*.tmp")
	if err != nil {
		return err
	}
	tmpName := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	return s.addKeyIndexLocked(key)
}

func (s *DirBlobStore) indexPath() string {
	return filepath.Join(s.root, "_blob_keys.json")
}

type blobKeyIndex struct {
	Keys []string `json:"keys"`
}

func (s *DirBlobStore) readIndexLocked() ([]string, error) {
	b, err := os.ReadFile(s.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var idx blobKeyIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	return idx.Keys, nil
}

func uniqueSortedStrings(xs []string) []string {
	if len(xs) == 0 {
		return xs
	}
	sort.Strings(xs)
	out := []string{xs[0]}
	for i := 1; i < len(xs); i++ {
		if xs[i] != xs[i-1] {
			out = append(out, xs[i])
		}
	}
	return out
}

func (s *DirBlobStore) writeIndexLocked(keys []string) error {
	keys = uniqueSortedStrings(append([]string(nil), keys...))
	raw, err := json.Marshal(blobKeyIndex{Keys: keys})
	if err != nil {
		return err
	}
	tmp := s.indexPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.indexPath())
}

func (s *DirBlobStore) addKeyIndexLocked(key string) error {
	cur, err := s.readIndexLocked()
	if err != nil {
		return err
	}
	cur = append(cur, key)
	return s.writeIndexLocked(cur)
}

func (s *DirBlobStore) removeKeyIndexLocked(key string) error {
	cur, err := s.readIndexLocked()
	if err != nil || len(cur) == 0 {
		return err
	}
	next := cur[:0]
	for _, k := range cur {
		if k != key {
			next = append(next, k)
		}
	}
	return s.writeIndexLocked(next)
}

func (s *DirBlobStore) ListKeys(ctx context.Context) ([]string, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readIndexLocked()
}

func (s *DirBlobStore) Load(_ context.Context, key string) ([]byte, error) {
	p := s.blobPath(key)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return b, nil
}

func (s *DirBlobStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.blobPath(key))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return s.removeKeyIndexLocked(key)
}
