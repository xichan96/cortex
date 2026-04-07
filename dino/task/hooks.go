package task

import "context"

type TaskHooks struct {
	BeforeTurn func(ctx context.Context, p *TaskProgress) error
	AfterTurn  func(ctx context.Context, p *TaskProgress, res *TaskResult) error
}
