package pipeline

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/types"
)

// activeTracker records the peak number of concurrently executing calls.
type activeTracker struct {
	active atomic.Int32
	peak   atomic.Int32
}

func (a *activeTracker) enter() {
	n := a.active.Add(1)
	for {
		peak := a.peak.Load()
		if n <= peak || a.peak.CompareAndSwap(peak, n) {
			return
		}
	}
}

func (a *activeTracker) leave() { a.active.Add(-1) }

func TestBatchRunsConcurrentlyAndReturnsSourceOrder(t *testing.T) {
	var tracker activeTracker
	var mu sync.Mutex
	var endOrder []string

	tool := func(name string, d time.Duration) *mockTool {
		return newTool(name, func(context.Context, map[string]interface{}) (interface{}, error) {
			tracker.enter()
			time.Sleep(d)
			tracker.leave()
			mu.Lock()
			endOrder = append(endOrder, name)
			mu.Unlock()
			return name, nil
		})
	}
	slow := tool("slow", 80*time.Millisecond)
	fast := tool("fast", 10*time.Millisecond)

	var eventMu sync.Mutex
	var endEvents []string
	p := newTestPipeline(t, Config{EventSink: EventSinkFunc(func(_ context.Context, ev Event) {
		if ev.Type != EventToolExecutionEnd {
			return
		}
		eventMu.Lock()
		endEvents = append(endEvents, ev.Name)
		eventMu.Unlock()
	})})

	results, err := p.ExecuteBatch(context.Background(), []*Call{
		testCall("c1", "slow", slow, nil),
		testCall("c2", "fast", fast, nil),
	})
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if len(results) != 2 || results[0].Text() != "slow" || results[1].Text() != "fast" {
		t.Fatalf("results not in source order: %+v", results)
	}
	if tracker.peak.Load() < 2 {
		t.Fatalf("peak concurrency = %d, want >= 2", tracker.peak.Load())
	}
	if len(endOrder) != 2 || endOrder[0] != "fast" {
		t.Fatalf("completion order = %v, want fast first", endOrder)
	}
	if len(endEvents) != 2 || endEvents[0] != "fast" {
		t.Fatalf("end event order = %v, want fast first", endEvents)
	}
}

func TestBatchAnySequentialToolForcesSerial(t *testing.T) {
	var tracker activeTracker
	mk := func(name string) *mockTool {
		return newTool(name, func(context.Context, map[string]interface{}) (interface{}, error) {
			tracker.enter()
			time.Sleep(20 * time.Millisecond)
			tracker.leave()
			return name, nil
		})
	}
	plain := mk("plain")
	seq := &modeTool{mockTool: mk("seq"), mode: types.ExecutionModeSequential}

	p := newTestPipeline(t, Config{})
	start := time.Now()
	results, err := p.ExecuteBatch(context.Background(), []*Call{
		testCall("c1", "plain", plain, nil),
		testCall("c2", "seq", seq, nil),
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if tracker.peak.Load() != 1 {
		t.Fatalf("peak concurrency = %d, want 1", tracker.peak.Load())
	}
	if elapsed < 35*time.Millisecond {
		t.Fatalf("batch did not run serially: %v", elapsed)
	}
	if results[0].Text() != "plain" || results[1].Text() != "seq" {
		t.Fatalf("results = %+v", results)
	}
}

func TestBatchRespectsMaxConcurrent(t *testing.T) {
	var tracker activeTracker
	var executed atomic.Int32
	tools := make([]*mockTool, 4)
	calls := make([]*Call, 4)
	for i := range tools {
		tools[i] = newTool("t", func(context.Context, map[string]interface{}) (interface{}, error) {
			executed.Add(1)
			tracker.enter()
			time.Sleep(20 * time.Millisecond)
			tracker.leave()
			return "ok", nil
		})
		calls[i] = testCall(string(rune('a'+i)), "t", tools[i], nil)
	}
	p := newTestPipeline(t, Config{Limits: Limits{MaxConcurrent: 2}})
	if _, err := p.ExecuteBatch(context.Background(), calls); err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if got := tracker.peak.Load(); got > 2 {
		t.Fatalf("peak concurrency = %d, want <= 2", got)
	}
	if executed.Load() != 4 {
		t.Fatalf("executed = %d, want 4", executed.Load())
	}
}

func TestBatchPrepareIsSerialInSourceOrder(t *testing.T) {
	var orderMu sync.Mutex
	var order []string
	var executedCount atomic.Int32

	mk := func(name string) *preparingTool {
		return &preparingTool{
			mockTool: newTool(name, func(context.Context, map[string]interface{}) (interface{}, error) {
				executedCount.Add(1)
				return name, nil
			}),
			prepare: func(args map[string]interface{}) (map[string]interface{}, error) {
				if executedCount.Load() != 0 {
					// Every call must be prepared before any execution starts.
					return nil, errors.New("prepare ran after execution began")
				}
				orderMu.Lock()
				order = append(order, name)
				orderMu.Unlock()
				return args, nil
			},
		}
	}
	p := newTestPipeline(t, Config{})
	results, err := p.ExecuteBatch(context.Background(), []*Call{
		testCall("c1", "a", mk("a"), nil),
		testCall("c2", "b", mk("b"), nil),
	})
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	for i, r := range results {
		if r.IsError {
			t.Fatalf("result %d error: %s", i, r.Text())
		}
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("prepare order = %v, want [a b]", order)
	}
}

func TestBatchCancelledBeforeStartSynthesizesResults(t *testing.T) {
	var executed atomic.Int32
	tool := newTool("t", func(context.Context, map[string]interface{}) (interface{}, error) {
		executed.Add(1)
		return "ok", nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := newTestPipeline(t, Config{})
	results, err := p.ExecuteBatch(ctx, []*Call{
		testCall("c1", "t", tool, nil),
		testCall("c2", "t", tool, nil),
	})
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if executed.Load() != 0 {
		t.Fatalf("executed = %d, want 0", executed.Load())
	}
	for i, r := range results {
		if r == nil || !r.IsError || r.Text() != "Operation aborted" {
			t.Fatalf("result %d = %+v, want synthesized abort", i, r)
		}
	}
}

func TestBatchCancelMidFlightAbortsQueuedCalls(t *testing.T) {
	blocking := newTool("blocking", func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	p := newTestPipeline(t, Config{Limits: Limits{MaxConcurrent: 1}})
	results, err := p.ExecuteBatch(ctx, []*Call{
		testCall("c1", "blocking", blocking, nil),
		testCall("c2", "blocking", blocking, nil),
	})
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if results[0] == nil || !results[0].IsError {
		t.Fatalf("first call should observe cancellation: %+v", results[0])
	}
	if results[1] == nil || !results[1].IsError || results[1].Text() != "Operation aborted" {
		t.Fatalf("second call should be synthesized aborted: %+v", results[1])
	}
}

func TestBatchFatalCancelsSiblings(t *testing.T) {
	fatalErr := &types.FatalToolError{Reason: "boom"}
	fatalTool := newTool("fatal", func(context.Context, map[string]interface{}) (interface{}, error) {
		return nil, fatalErr
	})
	slow := newTool("slow", func(ctx context.Context, _ map[string]interface{}) (interface{}, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
			return "late", nil
		}
	})
	p := newTestPipeline(t, Config{})
	results, err := p.ExecuteBatch(context.Background(), []*Call{
		testCall("c1", "fatal", fatalTool, nil),
		testCall("c2", "slow", slow, nil),
	})
	if err == nil || !errors.Is(err, fatalErr) {
		t.Fatalf("err = %v, want fatal", err)
	}
	if len(results) != 2 || results[0] == nil || results[1] == nil {
		t.Fatalf("every toolCallID must have a result: %+v", results)
	}
}

func TestNestedCallRunsThroughPipeline(t *testing.T) {
	inner := newTool("inner", func(context.Context, map[string]interface{}) (interface{}, error) {
		return "inner-ok", nil
	})

	var mu sync.Mutex
	var childEvents []Event

	var p *Pipeline
	p = newTestPipeline(t, Config{
		Executor: ExecutorFunc(func(ctx context.Context, c *Call) (interface{}, error) {
			if c.Name != "outer" {
				return inner.Execute(ctx, c.Args)
			}
			child := &Call{
				ID:        "child-1",
				Name:      "inner",
				Tool:      inner,
				SessionID: c.SessionID,
				TurnID:    c.TurnID,
				ParentID:  c.ID,
				Internal:  true,
			}
			r, err := p.ExecuteOne(ctx, child)
			if err != nil {
				return nil, err
			}
			if r.IsError {
				return nil, errors.New(r.Text())
			}
			return "outer+" + r.Text(), nil
		}),
		EventSink: EventSinkFunc(func(_ context.Context, ev Event) {
			if ev.CallID == "child-1" {
				mu.Lock()
				childEvents = append(childEvents, ev)
				mu.Unlock()
			}
		}),
	})

	outer := newTool("outer", nil)
	r, err := p.ExecuteOne(context.Background(), testCall("c1", "outer", outer, nil))
	if err != nil {
		t.Fatalf("unexpected fatal: %v", err)
	}
	if r.IsError {
		t.Fatalf("nested call failed: %s", r.Text())
	}
	if got := r.Text(); got != "outer+inner-ok" {
		t.Fatalf("text = %q", got)
	}
	if len(childEvents) != 2 {
		t.Fatalf("child events = %+v", childEvents)
	}
	for _, ev := range childEvents {
		if ev.ParentID != "c1" || ev.SessionID != "s1" || ev.TurnID != "t1" {
			t.Fatalf("child call lost parent context: %+v", ev)
		}
	}
}
