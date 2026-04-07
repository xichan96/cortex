package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino"
	"github.com/xichan96/cortex/dino/harness"
	dinotask "github.com/xichan96/cortex/dino/task"
	dinoverify "github.com/xichan96/cortex/dino/verify"
)

var harnessDirTrackedBasenames = []string{"progress", "feature_list.json"}

type staticSessionSwapper struct {
	sess *dino.Session
}

func (w staticSessionSwapper) Current() *dino.Session { return w.sess }

func (w staticSessionSwapper) Handoff(context.Context, *dinotask.Task) error { return nil }

type DefaultTaskEngine struct {
	Inner    dinotask.InnerTurnDriver
	Verifier dinotask.Verifier
	Store    dinotask.SessionStore
	Hooks    *dinotask.TaskHooks
}

func NewDefaultTaskEngine() *DefaultTaskEngine {
	return &DefaultTaskEngine{
		Inner:    NewDinoInnerTurnDriver(),
		Verifier: defaultHarnessVerifier,
	}
}

func (e *DefaultTaskEngine) RunTask(ctx context.Context, tk *dinotask.Task, sess *dino.Session) (*dinotask.TaskResult, dinotask.StopReason, error) {
	return e.RunTaskWithSwap(ctx, tk, staticSessionSwapper{sess: sess})
}

func (e *DefaultTaskEngine) RunTaskWithSwap(ctx context.Context, tk *dinotask.Task, swap dinotask.SessionSwapper) (*dinotask.TaskResult, dinotask.StopReason, error) {
	if swap == nil {
		swap = staticSessionSwapper{}
	}
	if tk == nil {
		return nil, dinotask.StopReasonFailed, fmt.Errorf("nil task")
	}
	if tk.Config == nil {
		tk.Config = &dinotask.TaskConfig{}
	}
	if tk.Progress == nil {
		tk.Progress = &dinotask.TaskProgress{}
	}
	if tk.Config.MaxOuterIterations < 1 {
		tk.Config.MaxOuterIterations = 1
	}
	maxOuter := tk.Config.MaxOuterIterations
	e.maybeLoadCheckpoint(ctx, tk, maxOuter)
	if maxOuter <= 1 {
		snap, reason, err := e.runInnerHooked(ctx, tk, swap.Current(), tk.PendingInput)
		if err != nil {
			return nil, dinotask.StopReasonFailed, err
		}
		return packResult(snap), reason, nil
	}
	prog := &harness.OuterProgress{
		OuterIteration:   &tk.Progress.OuterIteration,
		VerifyRetryCount: &tk.Progress.VerifyRetryCount,
	}
	p := harness.OuterParams{
		MaxOuterIterations:    maxOuter,
		StallWindow:           tk.Config.StallWindow,
		RequireStallSecondary: tk.Config.ArtifactDir != "",
		RetryLimit:            tk.Config.RetryLimit,
	}
	snap, out, err := harness.RunOuterLoop(ctx, p, prog,
		func(ctx context.Context) (*dinotask.TurnSnapshot, *harness.OuterOutcome, bool, error) {
			snap, reason, err := e.runInnerHooked(ctx, tk, swap.Current(), tk.PendingInput)
			if err != nil {
				return snap, nil, false, err
			}
			if reason != dinotask.StopReasonAgentIdle && reason != dinotask.StopReasonMaxTurnsReached {
				return snap, &harness.OuterOutcome{Kind: harness.OuterOutcomeInnerStop, InnerTag: int(reason)}, false, nil
			}
			return snap, nil, true, nil
		},
		func(s *dinotask.TurnSnapshot) harness.StallSample {
			var v harness.StallSample
			if s != nil {
				v.Primary = s.AssistantText
				v.Secondary = s.ArtifactFingerprint
			}
			return v
		},
		func(ctx context.Context, s *dinotask.TurnSnapshot) (bool, string) {
			return e.verifierVerify(ctx, tk, s)
		},
		func(s *dinotask.TurnSnapshot) bool {
			return completionMarkerMatches(tk, s)
		},
		func(ctx context.Context, vreason string) error {
			tk.PendingInput = injectContinuation(tk, vreason)
			e.resetSessionOrHandoffIfNeeded(ctx, tk, swap)
			e.saveCheckpointOuter(ctx, tk)
			return nil
		},
	)
	if err != nil {
		return nil, dinotask.StopReasonFailed, err
	}
	return packResult(snap), outerOutcomeToStopReason(out), nil
}

func outerOutcomeToStopReason(o harness.OuterOutcome) dinotask.StopReason {
	switch o.Kind {
	case harness.OuterOutcomeCompleted:
		return dinotask.StopReasonCompleted
	case harness.OuterOutcomeVerificationFailed:
		return dinotask.StopReasonVerificationFailed
	case harness.OuterOutcomeStall:
		return dinotask.StopReasonMaxOuterIterations
	case harness.OuterOutcomeInnerStop:
		return dinotask.StopReason(o.InnerTag)
	default:
		return dinotask.StopReasonFailed
	}
}

func (e *DefaultTaskEngine) maybeLoadCheckpoint(ctx context.Context, tk *dinotask.Task, maxOuter int) {
	if e.Store == nil || tk == nil || tk.ID == "" || maxOuter <= 1 {
		return
	}
	ts, err := e.Store.Load(ctx, tk.ID)
	if err != nil || ts == nil {
		return
	}
	raw, err := checkpointFromTaskSession(ts)
	if err != nil {
		return
	}
	_ = applyTaskCheckpoint(tk, raw)
	if tk.Progress == nil {
		tk.Progress = &dinotask.TaskProgress{}
	}
	if tk.Progress.OuterIteration >= maxOuter {
		tk.Progress.OuterIteration = 0
	}
}

func (e *DefaultTaskEngine) saveCheckpointOuter(ctx context.Context, tk *dinotask.Task) {
	if e.Store == nil || tk == nil || tk.ID == "" {
		return
	}
	next := tk.Progress.OuterIteration + 1
	p := *tk.Progress
	p.OuterIteration = next
	t2 := *tk
	t2.Progress = &p
	raw, err := marshalTaskCheckpoint(&t2)
	if err != nil {
		return
	}
	ts, err := taskSessionFromCheckpoint(tk, raw)
	if err != nil {
		return
	}
	_ = e.Store.Save(ctx, ts)
	e.maybePruneCheckpoints(ctx, tk)
}

func (e *DefaultTaskEngine) maybePruneCheckpoints(ctx context.Context, tk *dinotask.Task) {
	if e.Store == nil || tk == nil || tk.Config == nil || tk.ID == "" {
		return
	}
	n := tk.Config.CheckpointKeepVersions
	if n < 1 {
		return
	}
	p, ok := e.Store.(interface {
		PruneTask(context.Context, string, int) error
	})
	if !ok {
		return
	}
	_ = p.PruneTask(ctx, tk.ID, n)
}

func (e *DefaultTaskEngine) runInnerHooked(ctx context.Context, tk *dinotask.Task, sess *dino.Session, in types.AgentInput) (*dinotask.TurnSnapshot, dinotask.StopReason, error) {
	if e.Hooks != nil && e.Hooks.BeforeTurn != nil {
		if err := e.Hooks.BeforeTurn(ctx, tk.Progress); err != nil {
			return nil, dinotask.StopReasonFailed, err
		}
	}
	snap, reason, err := e.runInner(ctx, tk, sess, in)
	if err != nil {
		return snap, reason, err
	}
	if e.Hooks != nil && e.Hooks.AfterTurn != nil {
		if err := e.Hooks.AfterTurn(ctx, tk.Progress, packResult(snap)); err != nil {
			return snap, dinotask.StopReasonFailed, err
		}
	}
	return snap, reason, nil
}

func (e *DefaultTaskEngine) runInner(ctx context.Context, tk *dinotask.Task, sess *dino.Session, in types.AgentInput) (*dinotask.TurnSnapshot, dinotask.StopReason, error) {
	if e.Inner == nil {
		return nil, dinotask.StopReasonFailed, fmt.Errorf("nil Inner")
	}
	if tk.Config != nil && tk.Config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, tk.Config.Timeout)
		defer cancel()
	}
	snap, reason, err := e.Inner.RunOneUserTurn(ctx, sess, in)
	if err != nil {
		return snap, reason, err
	}
	if tk.Config != nil && tk.Config.ArtifactDir != "" {
		fp, ferr := harness.DirTrackedFilesFingerprint(tk.Config.ArtifactDir, harnessDirTrackedBasenames)
		if ferr == nil {
			snap.ArtifactFingerprint = fp
		}
	}
	if snap != nil && snap.Usage != nil && tk.Progress != nil {
		tk.Progress.ConsumedTokens += snap.Usage.TotalTokens
	}
	tk.Progress.CurrentTurn++
	return snap, reason, nil
}

func (e *DefaultTaskEngine) verifierVerify(ctx context.Context, tk *dinotask.Task, snap *dinotask.TurnSnapshot) (bool, string) {
	if e.Verifier == nil {
		return defaultHarnessVerifier.Verify(ctx, tk, snap)
	}
	return e.Verifier.Verify(ctx, tk, snap)
}

func (e *DefaultTaskEngine) resetSessionOrHandoffIfNeeded(ctx context.Context, tk *dinotask.Task, swap dinotask.SessionSwapper) {
	if tk == nil || tk.Config == nil || !tk.Config.PreferContextReset || swap == nil {
		return
	}
	prev := tk.PendingInput.String()
	if err := swap.Handoff(ctx, tk); err != nil {
		return
	}
	note := "\n\n[Context reset: prior chat cleared for this session; rely on task description and artifacts.]"
	if strings.TrimSpace(prev) == "" {
		tk.PendingInput = types.NewAgentInput("[Context reset: prior chat cleared for this session; rely on task description and artifacts.]")
	} else {
		tk.PendingInput = types.NewAgentInput(prev + note)
	}
}

func packResult(snap *dinotask.TurnSnapshot) *dinotask.TaskResult {
	return &dinotask.TaskResult{FinalSnapshot: snap}
}

func injectContinuation(tk *dinotask.Task, vreason string) types.AgentInput {
	msg := "Continue the task. Previous verification did not pass."
	if strings.TrimSpace(vreason) != "" {
		msg += " Reason: " + strings.TrimSpace(vreason)
	}
	if tk != nil && tk.Config != nil && strings.TrimSpace(tk.Config.CompletionMarker) != "" {
		msg += fmt.Sprintf(" Include this marker in your assistant reply when truly done: %q", tk.Config.CompletionMarker)
	}
	return types.NewAgentInput(msg)
}

func completionMarkerMatches(tk *dinotask.Task, snap *dinotask.TurnSnapshot) bool {
	if tk == nil || tk.Config == nil {
		return true
	}
	text := ""
	if snap != nil {
		text = snap.AssistantText
	}
	return dinoverify.OptionalSubstring(text, tk.Config.CompletionMarker)
}
