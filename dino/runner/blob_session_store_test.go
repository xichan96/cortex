package runner

import (
	"context"
	"testing"

	"github.com/xichan96/cortex/dino/harness"
	dinotask "github.com/xichan96/cortex/dino/task"
)

func TestFileSessionStoreListBySessionID(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewFileSessionStore(dir, "ckpt")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, &dinotask.TaskSession{TaskID: "a", SessionID: "s1", Messages: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(ctx, &dinotask.TaskSession{TaskID: "b", SessionID: "s2", Messages: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].TaskID != "a" {
		t.Fatalf("list=%+v", list)
	}
}

func TestSessionStoreFromMemoryBlobList(t *testing.T) {
	ctx := context.Background()
	s := NewSessionStoreFromBlob(harness.NewMemoryBlobStore(), "p")
	if err := s.Save(ctx, &dinotask.TaskSession{TaskID: "t1", SessionID: "sid", Messages: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, "sid")
	if err != nil || len(list) != 1 || list[0].TaskID != "t1" {
		t.Fatalf("list=%v err=%v", list, err)
	}
}
