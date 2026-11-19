package dino

import (
	"context"
	"testing"
	"time"
)

func TestNewDinoFactory_ToolPipelineWiring(t *testing.T) {
	cfg := getTestConfig()
	cfg.ToolPipeline = ToolPipelineConfig{
		Enabled:        true,
		MaxConcurrent:  3,
		DefaultTimeout: 2 * time.Second,
		Retries:        1,
	}

	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	p := factory.GetToolPipeline()
	if p == nil {
		t.Fatal("GetToolPipeline returned nil with ToolPipeline.Enabled")
	}
	limits := p.Limits()
	if limits.MaxConcurrent != 3 || limits.DefaultTimeout != 2*time.Second || limits.Retries != 1 {
		t.Fatalf("limits = %+v, want MaxConcurrent=3 DefaultTimeout=2s Retries=1", limits)
	}

	sess, err := factory.CreateSession(context.Background(), "pipeline-wiring")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if got := sess.GetAgent().ToolPipeline(); got != p {
		t.Fatalf("session engine pipeline = %p, want factory pipeline %p", got, p)
	}
}

func TestNewDinoFactory_ToolPipelineDisabledByDefault(t *testing.T) {
	factory, err := NewDinoFactory(getTestConfig())
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	if got := factory.GetToolPipeline(); got != nil {
		t.Fatalf("GetToolPipeline = %p, want nil when disabled", got)
	}
	sess, err := factory.CreateSession(context.Background(), "pipeline-disabled")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if got := sess.GetAgent().ToolPipeline(); got != nil {
		t.Fatalf("session engine pipeline = %p, want nil when disabled", got)
	}
}
