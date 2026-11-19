package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/pipeline"
	"github.com/xichan96/cortex/agent/types"
)

type pipelineFakeTool struct {
	name  string
	calls atomic.Int32
	exec  func(ctx context.Context, input map[string]interface{}) (interface{}, error)
}

func (t *pipelineFakeTool) Name() string        { return t.name }
func (t *pipelineFakeTool) Description() string { return "fake tool for pipeline integration" }
func (t *pipelineFakeTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (t *pipelineFakeTool) Execute(ctx context.Context, input map[string]interface{}) (interface{}, error) {
	t.calls.Add(1)
	return t.exec(ctx, input)
}
func (t *pipelineFakeTool) Metadata() types.ToolMetadata { return types.ToolMetadata{} }

func TestExecuteToolCallWithTimeout_PipelineSuccess(t *testing.T) {
	tool := &pipelineFakeTool{
		name: "demo",
		exec: func(context.Context, map[string]interface{}) (interface{}, error) {
			return "pipeline-ok", nil
		},
	}

	var mu sync.Mutex
	var events []pipeline.Event
	p := pipeline.New(pipeline.Config{
		Authorizer: pipeline.AllowAllAuthorizer{},
		EventSink: pipeline.EventSinkFunc(func(_ context.Context, ev pipeline.Event) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		}),
	})

	ae := NewAgentEngine(nil, types.NewAgentConfig())
	ae.SetToolPipeline(p)

	got, err := ae.executeToolCallWithTimeout(context.Background(), tool, "call-42", map[string]interface{}{}, time.Second)
	if err != nil {
		t.Fatalf("executeToolCallWithTimeout: %v", err)
	}
	if got != "pipeline-ok" {
		t.Fatalf("result = %v, want pipeline-ok", got)
	}
	if tool.calls.Load() != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls.Load())
	}

	mu.Lock()
	defer mu.Unlock()
	var sawStart, sawEnd bool
	for _, ev := range events {
		if ev.CallID != "call-42" {
			continue
		}
		switch ev.Type {
		case pipeline.EventToolExecutionStart:
			sawStart = true
		case pipeline.EventToolExecutionEnd:
			sawEnd = true
			if ev.IsError {
				t.Errorf("end event marked error: %+v", ev)
			}
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("events = %+v, want start and end for call-42", events)
	}
}

func TestExecuteToolCallWithTimeout_PipelineDeny(t *testing.T) {
	tool := &pipelineFakeTool{
		name: "dangerous",
		exec: func(context.Context, map[string]interface{}) (interface{}, error) {
			return "should not run", nil
		},
	}
	p := pipeline.New(pipeline.Config{
		Authorizer: pipeline.DenyAllAuthorizer{Reason: "blocked by test"},
	})
	ae := NewAgentEngine(nil, types.NewAgentConfig())
	ae.SetToolPipeline(p)

	got, err := ae.executeToolCallWithTimeout(context.Background(), tool, "call-deny", map[string]interface{}{}, time.Second)
	if err != nil {
		t.Fatalf("denied call must be a recoverable observation, got error: %v", err)
	}
	if tool.calls.Load() != 0 {
		t.Fatalf("tool calls = %d, want 0", tool.calls.Load())
	}
	obs, ok := got.(map[string]interface{})
	if !ok {
		t.Fatalf("result = %T (%v), want map observation", got, got)
	}
	if obs["ok"] != false {
		t.Fatalf("observation = %+v, want ok=false", obs)
	}
	if msg, _ := obs["error"].(string); msg == "" {
		t.Fatalf("observation = %+v, want a non-empty error message", obs)
	}
}

func TestExecuteToolCallWithTimeout_PipelineFatalPropagates(t *testing.T) {
	tool := &pipelineFakeTool{
		name: "fatal",
		exec: func(context.Context, map[string]interface{}) (interface{}, error) {
			return nil, &types.FatalToolError{Reason: "fatal test error"}
		},
	}
	p := pipeline.New(pipeline.Config{Authorizer: pipeline.AllowAllAuthorizer{}})
	ae := NewAgentEngine(nil, types.NewAgentConfig())
	ae.SetToolPipeline(p)

	_, err := ae.executeToolCallWithTimeout(context.Background(), tool, "call-fatal", map[string]interface{}{}, time.Second)
	if err == nil {
		t.Fatal("fatal tool error must propagate")
	}
	if !types.IsFatalToolError(err) {
		t.Fatalf("err = %v, want FatalToolErrorKind", err)
	}
}

func TestExecuteToolCallWithTimeout_LegacyWithoutPipeline(t *testing.T) {
	tool := &pipelineFakeTool{
		name: "legacy",
		exec: func(context.Context, map[string]interface{}) (interface{}, error) {
			return "legacy-ok", nil
		},
	}
	ae := NewAgentEngine(nil, types.NewAgentConfig())

	got, err := ae.executeToolCallWithTimeout(context.Background(), tool, "call-legacy", map[string]interface{}{}, time.Second)
	if err != nil {
		t.Fatalf("legacy execution: %v", err)
	}
	if got != "legacy-ok" {
		t.Fatalf("result = %v, want legacy-ok", got)
	}
	if tool.calls.Load() != 1 {
		t.Fatalf("tool calls = %d, want 1", tool.calls.Load())
	}
}
