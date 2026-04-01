package agent

import (
	"context"

	"github.com/xichan96/cortex/agent/types"
)

type parentMemoryCtxKey struct{}

type ParentMemoryContext struct {
	SessionID string
	Memory    types.MemoryProvider
}

func WithParentMemory(ctx context.Context, p *ParentMemoryContext) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, parentMemoryCtxKey{}, p)
}

func ParentMemoryFromContext(ctx context.Context) *ParentMemoryContext {
	v, _ := ctx.Value(parentMemoryCtxKey{}).(*ParentMemoryContext)
	return v
}
