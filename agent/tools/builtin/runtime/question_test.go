package runtime

import (
	"context"
	"testing"
)

func TestQuestionTool_ReturnsSentinel(t *testing.T) {
	tool := NewQuestionTool()
	res, err := tool.Execute(context.Background(), map[string]interface{}{"question": "Is it safe to delete this file?"})
	if err != nil {
		t.Fatalf("question tool must not hard-error, got: %v", err)
	}
	sentinel, ok := res.(SentinelQuestionResult)
	if !ok {
		t.Fatalf("expected SentinelQuestionResult, got %T", res)
	}
	if !sentinel.Ok {
		t.Error("expected Ok=true")
	}
	if !sentinel.AskUser {
		t.Error("expected AskUser=true")
	}
	if sentinel.Question != "Is it safe to delete this file?" {
		t.Errorf("question mismatch: %q", sentinel.Question)
	}
}

func TestQuestionTool_MissingQuestion(t *testing.T) {
	tool := NewQuestionTool()
	_, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Fatal("missing question should error")
	}
}
