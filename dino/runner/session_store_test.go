package runner

import (
	"context"
	"testing"
	"time"

	dinotask "github.com/xichan96/cortex/dino/task"
)

func TestNoopSessionStore(t *testing.T) {
	s := NewNoopSessionStore()
	ctx := context.Background()
	if err := s.Save(ctx, &dinotask.TaskSession{TaskID: "t"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx, "t")
	if err != nil || got != nil {
		t.Fatalf("load: %v %v", got, err)
	}
	if err := s.Delete(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, "sid")
	if err != nil || len(list) != 0 {
		t.Fatalf("list: %v %v", list, err)
	}
}

func TestMemorySessionStoreKeepVersions(t *testing.T) {
	m := NewMemorySessionStore(2)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = m.Save(ctx, &dinotask.TaskSession{
			TaskID:    "t1",
			SessionID: "s1",
			Messages:  []string{string(rune('a' + i))},
			UpdatedAt: time.Now().UTC(),
		})
	}
	got, err := m.Load(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Messages) == 0 || got.Messages[0] != "e" {
		t.Fatalf("want latest checkpoint, got %+v", got)
	}
	list, err := m.List(ctx, "s1")
	if err != nil || len(list) != 2 {
		t.Fatalf("list len %d err %v", len(list), err)
	}
}

func TestMemorySessionStorePruneTask(t *testing.T) {
	m := NewMemorySessionStore(10)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = m.Save(ctx, &dinotask.TaskSession{TaskID: "t1", SessionID: "s1", Messages: []string{string(rune('0' + i))}})
	}
	_ = m.PruneTask(ctx, "t1", 2)
	got, err := m.Load(ctx, "t1")
	if err != nil || got == nil || len(got.Messages) == 0 || got.Messages[0] != "4" {
		t.Fatalf("want latest after prune, got %+v err %v", got, err)
	}
}
