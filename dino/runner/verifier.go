package runner

import (
	"context"

	dinotask "github.com/xichan96/cortex/dino/task"
	dinoverify "github.com/xichan96/cortex/dino/verify"
)

type CompositeVerifier struct{}

func NewCompositeVerifier() *CompositeVerifier {
	return &CompositeVerifier{}
}

func (v *CompositeVerifier) Verify(ctx context.Context, tk *dinotask.Task, snap *dinotask.TurnSnapshot) (bool, string) {
	if tk == nil || tk.Config == nil {
		return true, ""
	}
	ok, reason, code := dinoverify.VerifyShell(ctx, tk.Config.ArtifactDir, tk.Config.VerifyCommand)
	if code != nil && snap != nil {
		snap.VerifyExitCode = code
	}
	return ok, reason
}

type TextMustContainVerifier struct{}

func (TextMustContainVerifier) Verify(ctx context.Context, tk *dinotask.Task, snap *dinotask.TurnSnapshot) (bool, string) {
	_ = ctx
	if tk == nil || tk.Config == nil {
		return true, ""
	}
	text := ""
	if snap != nil {
		text = snap.AssistantText
	}
	return dinoverify.TextContains(text, tk.Config.VerifyTextMustContain)
}

type AndVerifier struct {
	A, B dinotask.Verifier
}

func NewAndVerifier(a, b dinotask.Verifier) *AndVerifier {
	return &AndVerifier{A: a, B: b}
}

var defaultHarnessVerifier dinotask.Verifier = NewAndVerifier(NewCompositeVerifier(), TextMustContainVerifier{})

func (v *AndVerifier) Verify(ctx context.Context, tk *dinotask.Task, snap *dinotask.TurnSnapshot) (bool, string) {
	if v == nil {
		return true, ""
	}
	if v.A != nil {
		ok, reason := v.A.Verify(ctx, tk, snap)
		if !ok {
			return false, reason
		}
	}
	if v.B != nil {
		return v.B.Verify(ctx, tk, snap)
	}
	return true, ""
}
