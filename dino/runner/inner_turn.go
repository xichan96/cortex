package runner

import (
	"context"
	"errors"
	"strings"

	agenttypes "github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino"
	dinotask "github.com/xichan96/cortex/dino/task"
)

type DinoInnerTurnDriver struct{}

func NewDinoInnerTurnDriver() *DinoInnerTurnDriver {
	return &DinoInnerTurnDriver{}
}

func (d *DinoInnerTurnDriver) RunOneUserTurn(ctx context.Context, sess *dino.Session, in agenttypes.AgentInput) (*dinotask.TurnSnapshot, dinotask.StopReason, error) {
	obs, sawDone, err := dino.ObserveOneUserTurn(ctx, sess, in)
	if err != nil {
		var ts dino.TurnSessionClosedError
		if errors.As(err, &ts) {
			return nil, dinotask.StopReasonFailed, err
		}
		var tc dino.TurnCanceledError
		if errors.As(err, &tc) {
			return nil, dinotask.StopReasonUserCancelled, err
		}
		return nil, dinotask.StopReasonFailed, err
	}
	snap := &dinotask.TurnSnapshot{
		AssistantText:   obs.AssistantText,
		Usage:           obs.Usage,
		HadError:        obs.HadError,
		ErrorMessage:    obs.ErrorMessage,
		EngineStopCause: obs.EngineStopCause,
	}
	reason, err := mapStopReason(snap, sawDone, ctx)
	return snap, reason, err
}

func mapStopReason(snap *dinotask.TurnSnapshot, sawDone bool, ctx context.Context) (dinotask.StopReason, error) {
	if err := ctx.Err(); err != nil {
		return dinotask.StopReasonUserCancelled, err
	}
	if snap.EngineStopCause != "" {
		switch agenttypes.AgentStopCause(snap.EngineStopCause) {
		case agenttypes.AgentStopCauseMaxIterations:
			return dinotask.StopReasonMaxTurnsReached, nil
		case agenttypes.AgentStopCauseContextWindow:
			return dinotask.StopReasonContextOverflow, nil
		case agenttypes.AgentStopCauseDoomLoop:
			return dinotask.StopReasonFailed, nil
		}
	}
	msg := strings.ToLower(snap.ErrorMessage)
	if snap.HadError && !sawDone {
		if strings.Contains(msg, "budget exceeded") {
			return dinotask.StopReasonMaxBudgetReached, nil
		}
		if snap.ErrorMessage != "" {
			if c := agenttypes.StopCauseFromChatError(errors.New(snap.ErrorMessage)); c == agenttypes.AgentStopCauseContextWindow {
				return dinotask.StopReasonContextOverflow, nil
			}
		}
		return dinotask.StopReasonFailed, nil
	}
	return dinotask.StopReasonAgentIdle, nil
}
