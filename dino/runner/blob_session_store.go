package runner

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/xichan96/cortex/dino/harness"
	dinotask "github.com/xichan96/cortex/dino/task"
)

func NewFileSessionStore(dir, keyPrefix string) (dinotask.SessionStore, error) {
	b, err := harness.NewDirBlobStore(dir)
	if err != nil {
		return nil, err
	}
	return NewSessionStoreFromBlob(b, keyPrefix), nil
}

type blobBackedSessionStore struct {
	blob   harness.BlobStore
	prefix string
}

func NewSessionStoreFromBlob(blob harness.BlobStore, keyPrefix string) dinotask.SessionStore {
	if blob == nil {
		return NewNoopSessionStore()
	}
	p := strings.Trim(keyPrefix, "/")
	if p == "" {
		p = "task/checkpoint"
	}
	return &blobBackedSessionStore{blob: blob, prefix: p}
}

func (s *blobBackedSessionStore) key(taskID string) string {
	return s.prefix + "/" + taskID
}

func (s *blobBackedSessionStore) Save(ctx context.Context, session *dinotask.TaskSession) error {
	if session == nil || session.TaskID == "" {
		return nil
	}
	raw, err := json.Marshal(session)
	if err != nil {
		return err
	}
	return s.blob.Save(ctx, s.key(session.TaskID), raw)
}

func (s *blobBackedSessionStore) Load(ctx context.Context, taskID string) (*dinotask.TaskSession, error) {
	if taskID == "" {
		return nil, nil
	}
	raw, err := s.blob.Load(ctx, s.key(taskID))
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	var ts dinotask.TaskSession
	if err := json.Unmarshal(raw, &ts); err != nil {
		return nil, err
	}
	return cloneTaskSession(&ts), nil
}

func (s *blobBackedSessionStore) Delete(ctx context.Context, taskID string) error {
	if taskID == "" {
		return nil
	}
	return s.blob.Delete(ctx, s.key(taskID))
}

type blobKeyLister interface {
	ListKeys(context.Context) ([]string, error)
}

func (s *blobBackedSessionStore) List(ctx context.Context, sessionID string) ([]*dinotask.TaskSession, error) {
	if sessionID == "" {
		return nil, nil
	}
	lister, ok := s.blob.(blobKeyLister)
	if !ok {
		return nil, nil
	}
	all, err := lister.ListKeys(ctx)
	if err != nil || len(all) == 0 {
		return nil, err
	}
	pfx := s.prefix + "/"
	var out []*dinotask.TaskSession
	for _, k := range all {
		if !strings.HasPrefix(k, pfx) {
			continue
		}
		raw, err := s.blob.Load(ctx, k)
		if err != nil || len(raw) == 0 {
			continue
		}
		var ts dinotask.TaskSession
		if err := json.Unmarshal(raw, &ts); err != nil {
			continue
		}
		if ts.SessionID == sessionID {
			out = append(out, cloneTaskSession(&ts))
		}
	}
	return out, nil
}
