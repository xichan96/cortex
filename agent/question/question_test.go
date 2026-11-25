package question

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSpecUnmarshalJSON_AcceptsBothSpellings(t *testing.T) {
	var camel Spec
	if err := json.Unmarshal([]byte(`{"id":"a","header":"A","question":"Q?","multiSelect":true,"isSecret":true}`), &camel); err != nil {
		t.Fatalf("camelCase: %v", err)
	}
	if !camel.MultiSelect || !camel.IsSecret {
		t.Errorf("camelCase flags = %+v", camel)
	}

	var snake Spec
	if err := json.Unmarshal([]byte(`{"id":"a","header":"A","question":" Q? ","multi_select":true,"is_secret":true}`), &snake); err != nil {
		t.Fatalf("snake_case: %v", err)
	}
	if !snake.MultiSelect || !snake.IsSecret {
		t.Errorf("snake_case flags = %+v", snake)
	}
	if snake.Question != "Q?" {
		t.Errorf("question should be trimmed, got %q", snake.Question)
	}
}

func TestFirstText(t *testing.T) {
	if got := FirstText(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
	specs := []Spec{{Question: " "}, {Question: "second"}}
	if got := FirstText(specs); got != "second" {
		t.Errorf("FirstText = %q", got)
	}
}

func TestNormalizeAnswers(t *testing.T) {
	in := Answers{
		" a ": {" x ", "", "y"},
		"  ":  {"dropped"},
		"b":   {" "},
	}
	out := NormalizeAnswers(in)
	want := Answers{"a": {"x", "y"}, "b": nil}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("NormalizeAnswers = %#v, want %#v", out, want)
	}
	if NormalizeAnswers(nil) != nil {
		t.Error("nil should stay nil")
	}
}

func TestFormatAnswers(t *testing.T) {
	specs := []Spec{{ID: "scope"}, {ID: "note"}}
	answers := Answers{"scope": {"仅核心"}, "note": {"user_note: 先别动生产"}, "extra": {"host-added"}}
	text := FormatAnswers(specs, answers)
	for _, want := range []string{"scope: 仅核心", "note: user_note: 先别动生产", "extra: host-added"} {
		if !strings.Contains(text, want) {
			t.Errorf("FormatAnswers missing %q in:\n%s", want, text)
		}
	}
	unanswered := FormatAnswers([]Spec{{ID: "a"}}, nil)
	if !strings.Contains(unanswered, "(not answered)") {
		t.Errorf("unanswered = %q", unanswered)
	}
}

func TestContextHelpers(t *testing.T) {
	ctx := context.Background()
	var nilAsker Asker
	ctx = WithAsker(ctx, nilAsker) // nil is ignored
	if _, ok := AskerFromContext(ctx); ok {
		t.Error("nil asker must not be installed")
	}

	asked := false
	ctx = WithAsker(ctx, AskerFunc(func(context.Context, Request) (Answers, error) {
		asked = true
		return Answers{"a": {"b"}}, nil
	}))
	asker, ok := AskerFromContext(ctx)
	if !ok {
		t.Fatal("asker not found")
	}
	if _, err := asker.Ask(ctx, Request{}); err != nil || !asked {
		t.Errorf("ask err=%v asked=%v", err, asked)
	}

	ctx = WithToolCallID(ctx, "call_1")
	if got := ToolCallIDFromContext(ctx); got != "call_1" {
		t.Errorf("tool call id = %q", got)
	}
	if got := ToolCallIDFromContext(context.Background()); got != "" {
		t.Errorf("missing tool call id = %q", got)
	}
}

func TestContextError(t *testing.T) {
	if err := ContextError("q1", context.DeadlineExceeded); !errors.Is(err, ErrQuestionTimeout) {
		t.Errorf("deadline: %v", err)
	}
	if err := ContextError("q1", context.Canceled); !errors.Is(err, ErrQuestionCancelled) {
		t.Errorf("cancel: %v", err)
	}
	if err := ContextError("q1", nil); err != nil {
		t.Errorf("nil: %v", err)
	}
}
