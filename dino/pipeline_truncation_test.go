package dino

import (
	"context"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/pipeline"
	"github.com/xichan96/cortex/agent/types"
)

type bigOutputTool struct{ n int }

func (t *bigOutputTool) Name() string        { return "big" }
func (t *bigOutputTool) Description() string { return "returns a large text result" }
func (t *bigOutputTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object"}
}
func (t *bigOutputTool) Execute(context.Context, map[string]interface{}) (interface{}, error) {
	return strings.Repeat("x", t.n), nil
}
func (t *bigOutputTool) Metadata() types.ToolMetadata { return types.ToolMetadata{} }

// TestPipelineTruncationDefaultsToResultLimiter 开启 pipeline 后，工具结果不能
// 被 pipeline 的 2048 字节默认预算砍掉：API/JSON 输出动辄几十 KB，砍到 2KB
// 会让模型永远读不全、反复重试。
func TestPipelineTruncationDefaultsToResultLimiter(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Provider.Type = "mock"
	cfg.ToolPipeline = ToolPipelineConfig{
		Enabled:    true,
		Authorizer: ToolPipelineAuthorizerAllowAll,
	}
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	pipe := factory.GetToolPipeline()
	if pipe == nil {
		t.Fatal("pipeline not installed")
	}
	res, err := pipe.ExecuteOne(context.Background(), &pipeline.Call{
		ID:   "c1",
		Name: "big",
		Args: map[string]interface{}{},
		Tool: &bigOutputTool{n: 5000},
	})
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	text := res.Text()
	if len(text) < 4000 {
		t.Fatalf("result truncated to %d bytes; want ~5000 (pipeline must use the 120KB default, not 2048)", len(text))
	}
}

// TestPipelineTruncationHonorsConfig 显式配置仍然生效。
func TestPipelineTruncationHonorsConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Provider.Type = "mock"
	cfg.ToolPipeline = ToolPipelineConfig{
		Enabled:            true,
		Authorizer:         ToolPipelineAuthorizerAllowAll,
		TruncationMaxBytes: 1000,
	}
	factory, err := NewDinoFactory(cfg)
	if err != nil {
		t.Fatalf("NewDinoFactory: %v", err)
	}
	defer factory.Shutdown(context.Background())

	res, err := factory.GetToolPipeline().ExecuteOne(context.Background(), &pipeline.Call{
		ID:   "c2",
		Name: "big",
		Args: map[string]interface{}{},
		Tool: &bigOutputTool{n: 5000},
	})
	if err != nil {
		t.Fatalf("ExecuteOne: %v", err)
	}
	if len(res.Text()) > 2000 {
		t.Fatalf("result not truncated to configured 1000-byte budget: %d", len(res.Text()))
	}
}
