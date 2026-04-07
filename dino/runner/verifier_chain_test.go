package runner

import (
	"context"
	"testing"

	dinotask "github.com/xichan96/cortex/dino/task"
)

func TestTextMustContainVerifier(t *testing.T) {
	v := TextMustContainVerifier{}
	tk := &dinotask.Task{Config: &dinotask.TaskConfig{VerifyTextMustContain: "DONE"}}
	ok, _ := v.Verify(context.Background(), tk, &dinotask.TurnSnapshot{AssistantText: "ok DONE now"})
	if !ok {
		t.Fatal("expected ok")
	}
	ok2, r := v.Verify(context.Background(), tk, &dinotask.TurnSnapshot{AssistantText: "nope"})
	if ok2 || r == "" {
		t.Fatalf("want fail, got ok=%v r=%q", ok2, r)
	}
}

func TestAndVerifierShortCircuit(t *testing.T) {
	v := NewAndVerifier(
		verifierFunc(func(ctx context.Context, tk *dinotask.Task, snap *dinotask.TurnSnapshot) (bool, string) {
			return false, "first"
		}),
		verifierFunc(func(ctx context.Context, tk *dinotask.Task, snap *dinotask.TurnSnapshot) (bool, string) {
			t.Fatal("second should not run")
			return true, ""
		}),
	)
	ok, r := v.Verify(context.Background(), &dinotask.Task{}, &dinotask.TurnSnapshot{})
	if ok || r != "first" {
		t.Fatalf("got ok=%v r=%q", ok, r)
	}
}
