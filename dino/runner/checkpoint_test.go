package runner

import (
	"testing"

	"github.com/xichan96/cortex/agent/types"
	dinotask "github.com/xichan96/cortex/dino/task"
)

func TestCheckpointRoundtrip(t *testing.T) {
	tk := &dinotask.Task{
		ID:          "tid",
		SessionID:   "sid",
		Description: "d",
		Progress: &dinotask.TaskProgress{
			VerifyRetryCount: 2,
			OuterIteration:   2,
			ConsumedTokens:   99,
		},
		PendingInput: types.NewAgentInput("continue here"),
	}
	raw, err := marshalTaskCheckpoint(tk)
	if err != nil {
		t.Fatal(err)
	}
	out := &dinotask.Task{ID: "tid", SessionID: "sid"}
	if err := applyTaskCheckpoint(out, raw); err != nil {
		t.Fatal(err)
	}
	if out.Progress.VerifyRetryCount != 2 || out.Progress.OuterIteration != 2 || out.Progress.ConsumedTokens != 99 {
		t.Fatalf("progress %+v", out.Progress)
	}
	if out.PendingInput.String() != "continue here" {
		t.Fatalf("pending %q", out.PendingInput.String())
	}
	if out.Description != "d" {
		t.Fatalf("desc %q", out.Description)
	}
}
