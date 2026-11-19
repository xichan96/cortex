package engine

import (
	"context"
	"testing"
	"time"

	"github.com/xichan96/cortex/agent/pipeline"
	"github.com/xichan96/cortex/agent/types"
)

// TestExecuteViaPipelineCarriesSessionID verifies that the engine stamps every
// pipeline.Call with the id installed by SetSessionID. Approval brokers route
// per session off this field; an empty value would make a session's approval
// requests unattributable.
func TestExecuteViaPipelineCarriesSessionID(t *testing.T) {
	tool := &pipelineFakeTool{
		name: "demo",
		exec: func(context.Context, map[string]interface{}) (interface{}, error) {
			return "ok", nil
		},
	}

	var gotSessionID string
	var gotCallID string
	p := pipeline.New(pipeline.Config{
		Authorizer: pipeline.AuthorizerFunc(func(_ context.Context, c *pipeline.Call) (pipeline.Decision, error) {
			gotSessionID = c.SessionID
			gotCallID = c.ID
			return pipeline.Continue(), nil
		}),
	})

	ae := NewAgentEngine(nil, types.NewAgentConfig())
	ae.SetSessionID("session-abc")
	ae.SetToolPipeline(p)

	got, err := ae.executeToolCallWithTimeout(context.Background(), tool, "call-7", map[string]interface{}{}, time.Second)
	if err != nil {
		t.Fatalf("executeToolCallWithTimeout: %v", err)
	}
	if got != "ok" {
		t.Fatalf("result = %v, want ok", got)
	}
	if gotSessionID != "session-abc" {
		t.Fatalf("Call.SessionID = %q, want %q", gotSessionID, "session-abc")
	}
	if gotCallID != "call-7" {
		t.Fatalf("Call.ID = %q, want %q", gotCallID, "call-7")
	}
	if ae.SessionID() != "session-abc" {
		t.Fatalf("SessionID() = %q", ae.SessionID())
	}
}

// TestExecuteViaPipelineSessionIDUpdates verifies the id is read per call, not
// captured when the pipeline was installed.
func TestExecuteViaPipelineSessionIDUpdates(t *testing.T) {
	tool := &pipelineFakeTool{
		name: "demo",
		exec: func(context.Context, map[string]interface{}) (interface{}, error) {
			return "ok", nil
		},
	}

	var seen []string
	p := pipeline.New(pipeline.Config{
		Authorizer: pipeline.AuthorizerFunc(func(_ context.Context, c *pipeline.Call) (pipeline.Decision, error) {
			seen = append(seen, c.SessionID)
			return pipeline.Continue(), nil
		}),
	})
	ae := NewAgentEngine(nil, types.NewAgentConfig())
	ae.SetToolPipeline(p)

	ae.SetSessionID("first")
	if _, err := ae.executeToolCallWithTimeout(context.Background(), tool, "c1", map[string]interface{}{}, time.Second); err != nil {
		t.Fatalf("first call: %v", err)
	}
	ae.SetSessionID("second")
	if _, err := ae.executeToolCallWithTimeout(context.Background(), tool, "c2", map[string]interface{}{}, time.Second); err != nil {
		t.Fatalf("second call: %v", err)
	}

	if len(seen) != 2 || seen[0] != "first" || seen[1] != "second" {
		t.Fatalf("session ids = %v, want [first second]", seen)
	}
}
