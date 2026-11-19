package pipeline

import (
	"context"
	"errors"
	"sync"

	"github.com/xichan96/cortex/agent/types"
)

// ExecuteBatch runs a batch of calls from one assistant message.
//
// Scheduling rules (§4.4):
//   - resolve/prepare/validate/authorize run serially in source order, so an
//     approval prompt can never race another prompt;
//   - if any tool declares ExecutionModeSequential (or MaxConcurrent <= 1), the
//     whole batch runs serially, otherwise authorized calls run concurrently up
//     to Limits.MaxConcurrent;
//   - tool_execution_end events fire in completion order (emitted by the
//     executing goroutine), while results are returned in source order;
//   - on cancellation or fatal failure, calls that never started get a
//     synthesized "Operation aborted" result so every toolCallID has a result.
func (p *Pipeline) ExecuteBatch(ctx context.Context, calls []*Call) ([]*Result, error) {
	results := make([]*Result, len(calls))
	if len(calls) == 0 {
		return results, nil
	}

	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	prepared := make([]*preparedCall, len(calls))
	var fatal error

	// Phase 1: prepare serially in source order.
	for i, c := range calls {
		if batchCtx.Err() != nil {
			break
		}
		pc, perr := p.prepare(batchCtx, c)
		if pc != nil && pc.blocked != nil {
			results[i] = pc.blocked
			p.finish(batchCtx, c, pc.blocked, perr)
			if perr != nil {
				fatal = perr
				cancel()
				break
			}
			continue
		}
		if perr != nil {
			r := ErrorResult(perr.Error())
			results[i] = r
			p.finish(batchCtx, c, r, perr)
			fatal = perr
			cancel()
			break
		}
		prepared[i] = pc
	}

	// Phase 2: decide serial vs concurrent.
	serial := p.limits.MaxConcurrent <= 1
	for _, pc := range prepared {
		if pc != nil && types.ToolExecutionMode(pc.call.Tool).IsSequential() {
			serial = true
			break
		}
	}

	if serial {
		for i, pc := range prepared {
			if pc == nil {
				continue
			}
			if batchCtx.Err() != nil {
				break
			}
			r, err := p.runPrepared(batchCtx, pc.call)
			results[i] = r
			if err != nil {
				fatal = err
				cancel()
				break
			}
		}
	} else {
		var (
			wg  sync.WaitGroup
			mu  sync.Mutex
			sem = make(chan struct{}, p.limits.MaxConcurrent)
		)
		for i, pc := range prepared {
			if pc == nil {
				continue
			}
			wg.Add(1)
			go func(i int, pc *preparedCall) {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-batchCtx.Done():
					return // filled as aborted below
				}
				defer func() { <-sem }()
				if batchCtx.Err() != nil {
					return // never started
				}
				r, err := p.runPrepared(batchCtx, pc.call)
				results[i] = r
				if err != nil {
					mu.Lock()
					if fatal == nil {
						fatal = err
						cancel()
					}
					mu.Unlock()
				}
			}(i, pc)
		}
		wg.Wait()
	}

	// Phase 3: synthesize results for calls that never started.
	for i, c := range calls {
		if results[i] != nil {
			continue
		}
		cause := batchCtx.Err()
		if cause == nil {
			cause = errors.New("not started")
		}
		r := abortedResult(c, cause)
		results[i] = r
		p.finish(ctx, c, r, nil)
	}

	return results, fatal
}

// abortedResult is the synthesized result for a call that never started.
func abortedResult(c *Call, cause error) *Result {
	r := ErrorResult("Operation aborted")
	details := map[string]any{}
	if c != nil {
		details["callId"] = c.ID
		details["tool"] = c.Name
	}
	if cause != nil {
		details["cause"] = cause.Error()
	}
	r.Details = details
	return r
}
