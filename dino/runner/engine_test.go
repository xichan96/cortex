package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino"
	"github.com/xichan96/cortex/dino/harness"
	dinotask "github.com/xichan96/cortex/dino/task"
)

type mockInner struct {
	snaps   []*dinotask.TurnSnapshot
	reasons []dinotask.StopReason
	errs    []error
	call    int
}

func (m *mockInner) RunOneUserTurn(ctx context.Context, sess *dino.Session, in types.AgentInput) (*dinotask.TurnSnapshot, dinotask.StopReason, error) {
	i := m.call
	m.call++
	if i >= len(m.snaps) {
		return &dinotask.TurnSnapshot{}, dinotask.StopReasonAgentIdle, nil
	}
	var err error
	if m.errs != nil && i < len(m.errs) {
		err = m.errs[i]
	}
	return m.snaps[i], m.reasons[i], err
}

func TestRunTaskMaxOuterOneSingleInnerCall(t *testing.T) {
	m := &mockInner{
		snaps:   []*dinotask.TurnSnapshot{{AssistantText: "hi"}},
		reasons: []dinotask.StopReason{dinotask.StopReasonAgentIdle},
	}
	e := &DefaultTaskEngine{Inner: m, Verifier: NewCompositeVerifier()}
	tk := &dinotask.Task{
		Config:       &dinotask.TaskConfig{MaxOuterIterations: 1},
		Progress:     &dinotask.TaskProgress{},
		PendingInput: types.NewAgentInput("x"),
	}
	_, reason, err := e.RunTask(context.Background(), tk, nil)
	if err != nil || reason != dinotask.StopReasonAgentIdle {
		t.Fatalf("err=%v reason=%v calls=%d", err, reason, m.call)
	}
	if m.call != 1 {
		t.Fatalf("want 1 inner call, got %d", m.call)
	}
}

func TestRunTaskRetriesUntilMaxOuter(t *testing.T) {
	m := &mockInner{
		snaps: []*dinotask.TurnSnapshot{
			{AssistantText: "a"},
			{AssistantText: "b"},
		},
		reasons: []dinotask.StopReason{dinotask.StopReasonAgentIdle, dinotask.StopReasonAgentIdle},
	}
	e := &DefaultTaskEngine{
		Inner: m,
		Verifier: verifierFunc(func(ctx context.Context, tk *dinotask.Task, snap *dinotask.TurnSnapshot) (bool, string) {
			return false, "fail"
		}),
	}
	tk := &dinotask.Task{
		Config: &dinotask.TaskConfig{
			MaxOuterIterations: 2,
			RetryLimit:         10,
			StallWindow:        3,
		},
		Progress:     &dinotask.TaskProgress{},
		PendingInput: types.NewAgentInput("x"),
	}
	res, reason, err := e.RunTask(context.Background(), tk, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reason != dinotask.StopReasonVerificationFailed {
		t.Fatalf("reason=%v", reason)
	}
	if res == nil || res.FinalSnapshot.AssistantText != "b" {
		t.Fatalf("bad result %+v", res)
	}
	if m.call != 2 {
		t.Fatalf("want 2 calls, got %d", m.call)
	}
}

func TestStallAccumulate(t *testing.T) {
	var ring [][2]uint64
	snap := &dinotask.TurnSnapshot{AssistantText: "same"}
	v := harness.StallSample{Primary: snap.AssistantText, Secondary: snap.ArtifactFingerprint}
	for i := 0; i < 2; i++ {
		if harness.StallAccumulate(&ring, v, 3, false) {
			t.Fatal("stall too early")
		}
	}
	if !harness.StallAccumulate(&ring, v, 3, false) {
		t.Fatal("expected stall after 3 identical non-empty snaps")
	}
}

func TestStallSkipsEmptySnap(t *testing.T) {
	var ring [][2]uint64
	v := harness.StallSample{}
	if harness.StallAccumulate(&ring, v, 3, false) {
		t.Fatal("empty should not stall")
	}
	if len(ring) != 0 {
		t.Fatalf("ring=%v", ring)
	}
}

func TestHooksBeforeTurnStopsRun(t *testing.T) {
	hooks := &dinotask.TaskHooks{
		BeforeTurn: func(ctx context.Context, p *dinotask.TaskProgress) error {
			return errors.New("blocked")
		},
	}
	m := &mockInner{
		snaps:   []*dinotask.TurnSnapshot{{AssistantText: "x"}},
		reasons: []dinotask.StopReason{dinotask.StopReasonAgentIdle},
	}
	e := &DefaultTaskEngine{Inner: m, Verifier: NewCompositeVerifier(), Hooks: hooks}
	tk := &dinotask.Task{
		Config:       &dinotask.TaskConfig{MaxOuterIterations: 1},
		Progress:     &dinotask.TaskProgress{},
		PendingInput: types.NewAgentInput("x"),
	}
	_, _, err := e.RunTask(context.Background(), tk, nil)
	if err == nil || err.Error() != "blocked" {
		t.Fatalf("err=%v", err)
	}
	if m.call != 0 {
		t.Fatalf("inner should not run, calls=%d", m.call)
	}
}

func TestCheckpointResumeOuterIteration(t *testing.T) {
	store := NewMemorySessionStore(5)
	ctx := context.Background()
	task0 := &dinotask.Task{
		ID: "tid", SessionID: "sid",
		Config: &dinotask.TaskConfig{MaxOuterIterations: 5, RetryLimit: 5, StallWindow: 3},
		Progress: &dinotask.TaskProgress{
			VerifyRetryCount: 1,
			OuterIteration:   2,
		},
		PendingInput: types.NewAgentInput("resume"),
	}
	raw, _ := marshalTaskCheckpoint(task0)
	ts, _ := taskSessionFromCheckpoint(task0, raw)
	_ = store.Save(ctx, ts)

	tk := &dinotask.Task{
		ID: "tid", SessionID: "sid",
		Config:       &dinotask.TaskConfig{MaxOuterIterations: 5, RetryLimit: 5, StallWindow: 3},
		Progress:     &dinotask.TaskProgress{},
		PendingInput: types.NewAgentInput("ignored"),
	}
	m := &mockInner{
		snaps: []*dinotask.TurnSnapshot{
			{AssistantText: "a"},
			{AssistantText: "b"},
			{AssistantText: "c"},
		},
		reasons: []dinotask.StopReason{dinotask.StopReasonAgentIdle, dinotask.StopReasonAgentIdle, dinotask.StopReasonAgentIdle},
	}
	e := &DefaultTaskEngine{
		Inner: m,
		Verifier: verifierFunc(func(ctx context.Context, tk *dinotask.Task, snap *dinotask.TurnSnapshot) (bool, string) {
			return false, "no"
		}),
		Store: store,
	}
	_, _, err := e.RunTask(ctx, tk, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.call != 3 {
		t.Fatalf("want 3 inner calls (outer 2..4), got %d", m.call)
	}
}

type countingSessionSwapper struct {
	sess *dino.Session
	n    int
}

func (c *countingSessionSwapper) Current() *dino.Session { return c.sess }

func (c *countingSessionSwapper) Handoff(context.Context, *dinotask.Task) error {
	c.n++
	return nil
}

func TestPreferContextResetCallsHandoff(t *testing.T) {
	sw := &countingSessionSwapper{}
	m := &mockInner{
		snaps: []*dinotask.TurnSnapshot{
			{AssistantText: "a"},
			{AssistantText: "b"},
			{AssistantText: "c"},
		},
		reasons: []dinotask.StopReason{dinotask.StopReasonAgentIdle, dinotask.StopReasonAgentIdle, dinotask.StopReasonAgentIdle},
	}
	e := &DefaultTaskEngine{
		Inner: m,
		Verifier: verifierFunc(func(ctx context.Context, tk *dinotask.Task, snap *dinotask.TurnSnapshot) (bool, string) {
			return false, "no"
		}),
	}
	tk := &dinotask.Task{
		Config: &dinotask.TaskConfig{
			MaxOuterIterations: 3,
			RetryLimit:         10,
			StallWindow:        3,
			PreferContextReset: true,
		},
		Progress:     &dinotask.TaskProgress{},
		PendingInput: types.NewAgentInput("go"),
	}
	_, _, err := e.RunTaskWithSwap(context.Background(), tk, sw)
	if err != nil {
		t.Fatal(err)
	}
	if sw.n != 2 {
		t.Fatalf("want 2 handoffs, got %d", sw.n)
	}
}

type verifierFunc func(ctx context.Context, tk *dinotask.Task, snap *dinotask.TurnSnapshot) (bool, string)

func (f verifierFunc) Verify(ctx context.Context, tk *dinotask.Task, snap *dinotask.TurnSnapshot) (bool, string) {
	return f(ctx, tk, snap)
}
