package runner

import (
	"context"
	"testing"

	agenttypes "github.com/xichan96/cortex/agent/types"
	dinotask "github.com/xichan96/cortex/dino/task"
)

func TestMapStopReasonMaxIterationsFromEngine(t *testing.T) {
	snap := &dinotask.TurnSnapshot{EngineStopCause: string(agenttypes.AgentStopCauseMaxIterations)}
	r, err := mapStopReason(snap, true, context.Background())
	if err != nil || r != dinotask.StopReasonMaxTurnsReached {
		t.Fatalf("got %v %v", r, err)
	}
}

func TestMapStopReasonContextFromEngine(t *testing.T) {
	snap := &dinotask.TurnSnapshot{EngineStopCause: string(agenttypes.AgentStopCauseContextWindow)}
	r, err := mapStopReason(snap, true, context.Background())
	if err != nil || r != dinotask.StopReasonContextOverflow {
		t.Fatalf("got %v %v", r, err)
	}
}
