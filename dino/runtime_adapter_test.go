package dino

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/runtime"
)

func TestSessionRuntimePromptAndEvents(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	defer client.CloseAll()
	cs, err := client.CreateSession(context.Background(), "runtime-adapter")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	rt := NewSessionRuntime(cs)
	defer rt.Close(context.Background())

	var mu sync.Mutex
	var events []runtime.Event
	rt.Subscribe(func(ev runtime.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})

	disp, err := rt.Prompt(context.Background(), runtime.PromptInput{Text: "hello runtime"})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if disp != runtime.DispositionStarted {
		t.Fatalf("disposition = %s, want started", disp)
	}

	select {
	case <-rt.Settled():
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not settle")
	}

	mu.Lock()
	got := append([]runtime.Event(nil), events...)
	mu.Unlock()
	if len(got) == 0 {
		t.Fatal("no runtime events emitted")
	}
	seen := make(map[runtime.EventType]bool)
	for _, ev := range got {
		seen[ev.Type] = true
		if ev.Seq <= 0 {
			t.Errorf("event %s has non-positive seq %d", ev.Type, ev.Seq)
		}
	}
	for _, want := range []runtime.EventType{
		runtime.EventAgentStart,
		runtime.EventAgentEnd,
		runtime.EventAgentSettled,
	} {
		if !seen[want] {
			t.Errorf("missing event %s; got %v", want, seen)
		}
	}
	if state := rt.State(); state.IsStreaming {
		t.Error("State().IsStreaming = true after settle")
	}

	entries, err := rt.Entries(context.Background(), runtime.EntryQuery{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries.Entries) == 0 {
		t.Fatal("Entries returned no messages")
	}
}

func TestSessionRuntimePromptBusy(t *testing.T) {
	cfg := getTestConfig()
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	client := NewClient(factory)
	defer client.CloseAll()
	cs, err := client.CreateSession(context.Background(), "runtime-busy")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	rt := NewSessionRuntime(cs)
	defer rt.Close(context.Background())

	if _, err := rt.Prompt(context.Background(), runtime.PromptInput{Text: "first"}); err != nil {
		t.Fatalf("first Prompt: %v", err)
	}
	if _, err := rt.Prompt(context.Background(), runtime.PromptInput{Text: "second"}); err != runtime.ErrBusy {
		t.Fatalf("second Prompt err = %v, want ErrBusy", err)
	}
	select {
	case <-rt.Settled():
	case <-time.After(5 * time.Second):
		t.Fatal("runtime did not settle")
	}
}
