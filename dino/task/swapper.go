package task

import (
	"context"

	"github.com/xichan96/cortex/dino"
)

type SessionSwapper interface {
	Current() *dino.Session
	Handoff(ctx context.Context, t *Task) error
}
