package harness

import (
	"context"
	"fmt"
)

type OuterParams struct {
	MaxOuterIterations      int
	StallWindow             int
	RequireStallSecondary   bool
	RetryLimit              int
}

type OuterOutcomeKind int

const (
	OuterOutcomeCompleted OuterOutcomeKind = iota
	OuterOutcomeVerificationFailed
	OuterOutcomeStall
	OuterOutcomeInnerStop
)

type OuterOutcome struct {
	Kind     OuterOutcomeKind
	InnerTag int
}

type OuterProgress struct {
	OuterIteration   *int
	VerifyRetryCount *int
}

func RunOuterLoop[S any](ctx context.Context, p OuterParams, prog *OuterProgress,
	runInner func(context.Context) (snap *S, early *OuterOutcome, idleLike bool, err error),
	stallSample func(*S) StallSample,
	verify func(context.Context, *S) (ok bool, reason string),
	completionOK func(*S) bool,
	onVerifyFailedContinue func(context.Context, string) error,
) (*S, OuterOutcome, error) {
	if prog == nil || prog.OuterIteration == nil || prog.VerifyRetryCount == nil {
		return nil, OuterOutcome{}, fmt.Errorf("harness: nil OuterProgress fields")
	}
	maxOuter := p.MaxOuterIterations
	if maxOuter < 1 {
		maxOuter = 1
	}
	stallN := p.StallWindow
	if stallN <= 0 {
		stallN = 3
	}
	var stallRing [][2]uint64
	for *prog.OuterIteration < maxOuter {
		snap, early, idleLike, err := runInner(ctx)
		if err != nil {
			return nil, OuterOutcome{}, err
		}
		if early != nil {
			return snap, *early, nil
		}
		if !idleLike {
			return snap, OuterOutcome{Kind: OuterOutcomeInnerStop}, nil
		}
		var st StallSample
		if stallSample != nil {
			st = stallSample(snap)
		}
		if StallAccumulate(&stallRing, st, stallN, p.RequireStallSecondary) {
			return snap, OuterOutcome{Kind: OuterOutcomeStall}, nil
		}
		ok, vreason := false, ""
		if verify != nil {
			ok, vreason = verify(ctx, snap)
		}
		cmOK := true
		if completionOK != nil {
			cmOK = completionOK(snap)
		}
		if ok && cmOK {
			return snap, OuterOutcome{Kind: OuterOutcomeCompleted}, nil
		}
		if p.RetryLimit > 0 && *prog.VerifyRetryCount >= p.RetryLimit {
			return snap, OuterOutcome{Kind: OuterOutcomeVerificationFailed}, nil
		}
		if *prog.OuterIteration+1 >= maxOuter {
			return snap, OuterOutcome{Kind: OuterOutcomeVerificationFailed}, nil
		}
		*prog.VerifyRetryCount++
		if onVerifyFailedContinue != nil {
			if err := onVerifyFailedContinue(ctx, vreason); err != nil {
				return snap, OuterOutcome{}, err
			}
		}
		*prog.OuterIteration++
	}
	panic("harness: outer loop fell through")
}
