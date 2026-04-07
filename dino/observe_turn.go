package dino

import (
	"context"

	"github.com/xichan96/cortex/agent/types"
	"github.com/xichan96/cortex/dino/session"
)

type TurnObservation = session.TurnObservation

type TurnCanceledError = session.TurnCanceledError

type TurnSessionClosedError = session.TurnSessionClosedError

func ObserveOneUserTurn(ctx context.Context, s *Session, in types.AgentInput) (*TurnObservation, bool, error) {
	return session.ObserveOneUserTurn(ctx, s, in)
}
