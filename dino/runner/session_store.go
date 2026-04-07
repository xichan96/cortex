package runner

import (
	"context"
	"sync"

	dinotask "github.com/xichan96/cortex/dino/task"
)

type noopSessionStore struct{}

func NewNoopSessionStore() dinotask.SessionStore {
	return noopSessionStore{}
}

func (noopSessionStore) Save(context.Context, *dinotask.TaskSession) error { return nil }

func (noopSessionStore) Load(context.Context, string) (*dinotask.TaskSession, error) { return nil, nil }

func (noopSessionStore) Delete(context.Context, string) error { return nil }

func (noopSessionStore) List(context.Context, string) ([]*dinotask.TaskSession, error) { return nil, nil }

type MemorySessionStore struct {
	mu      sync.RWMutex
	byTask  map[string][]*dinotask.TaskSession
	MaxKeep int
}

func NewMemorySessionStore(maxKeep int) *MemorySessionStore {
	if maxKeep < 1 {
		maxKeep = 5
	}
	return &MemorySessionStore{
		byTask:  make(map[string][]*dinotask.TaskSession),
		MaxKeep: maxKeep,
	}
}

func (m *MemorySessionStore) Save(ctx context.Context, s *dinotask.TaskSession) error {
	_ = ctx
	if s == nil || s.TaskID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sl := append(m.byTask[s.TaskID], cloneTaskSession(s))
	if len(sl) > m.MaxKeep {
		sl = sl[len(sl)-m.MaxKeep:]
	}
	m.byTask[s.TaskID] = sl
	return nil
}

func (m *MemorySessionStore) Load(ctx context.Context, taskID string) (*dinotask.TaskSession, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	sl := m.byTask[taskID]
	if len(sl) == 0 {
		return nil, nil
	}
	return cloneTaskSession(sl[len(sl)-1]), nil
}

func (m *MemorySessionStore) Delete(ctx context.Context, taskID string) error {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byTask, taskID)
	return nil
}

func (m *MemorySessionStore) PruneTask(ctx context.Context, taskID string, keep int) error {
	_ = ctx
	if keep < 1 {
		keep = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sl := m.byTask[taskID]
	if len(sl) <= keep {
		return nil
	}
	m.byTask[taskID] = append([]*dinotask.TaskSession(nil), sl[len(sl)-keep:]...)
	return nil
}

func (m *MemorySessionStore) List(ctx context.Context, sessionID string) ([]*dinotask.TaskSession, error) {
	_ = ctx
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*dinotask.TaskSession
	for _, sl := range m.byTask {
		for _, ts := range sl {
			if ts.SessionID == sessionID {
				out = append(out, cloneTaskSession(ts))
			}
		}
	}
	return out, nil
}

func cloneTaskSession(s *dinotask.TaskSession) *dinotask.TaskSession {
	if s == nil {
		return nil
	}
	cp := *s
	if len(s.Messages) > 0 {
		cp.Messages = append([]string(nil), s.Messages...)
	}
	return &cp
}
