package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xichan96/cortex/agent/question"
	"github.com/xichan96/cortex/agent/tools/schema"
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
	// Legacy input becomes one free-text question with id "question".
	if len(sentinel.Questions) != 1 {
		t.Fatalf("expected 1 converted question, got %d", len(sentinel.Questions))
	}
	if sentinel.Questions[0].ID != question.LegacyQuestionID {
		t.Errorf("legacy id = %q", sentinel.Questions[0].ID)
	}
	if len(sentinel.Questions[0].Options) != 0 {
		t.Error("legacy question must be free text")
	}
}

func TestQuestionTool_MissingQuestion(t *testing.T) {
	tool := NewQuestionTool()
	_, err := tool.Execute(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Fatal("missing question should error")
	}
}

func TestQuestionTool_StructuredSentinel(t *testing.T) {
	tool := NewQuestionTool()
	input := map[string]interface{}{
		"questions": []interface{}{
			map[string]interface{}{
				"id":       "scope",
				"header":   "范围",
				"question": "这次要处理哪些服务？",
				"options": []interface{}{
					map[string]interface{}{"label": "全部（Recommended）", "description": "所有服务"},
					map[string]interface{}{"label": "仅核心", "description": "核心服务"},
				},
			},
			map[string]interface{}{
				"id":          "token",
				"header":      "Token",
				"question":    "部署令牌是什么？",
				"multiSelect": true, // camelCase is accepted
				"isSecret":    true,
			},
		},
	}
	res, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	sentinel := res.(SentinelQuestionResult)
	if len(sentinel.Questions) != 2 {
		t.Fatalf("questions = %d", len(sentinel.Questions))
	}
	first := sentinel.Questions[0]
	if first.ID != "scope" || first.Header != "范围" || len(first.Options) != 2 {
		t.Errorf("unexpected first question: %+v", first)
	}
	if sentinel.Question != "这次要处理哪些服务？" {
		t.Errorf("Question (compat) = %q", sentinel.Question)
	}
	second := sentinel.Questions[1]
	if second.ID != "token" || !second.MultiSelect || !second.IsSecret {
		t.Errorf("camelCase flags not parsed: %+v", second)
	}
	if len(second.Options) != 0 {
		t.Error("second question should be free text")
	}
}

func TestQuestionTool_SnakeCaseFlags(t *testing.T) {
	specs, err := ParseQuestions(map[string]interface{}{
		"questions": []interface{}{map[string]interface{}{
			"id": "a", "header": "A", "question": "Q?",
			"multi_select": true, "is_secret": true,
		}},
	}, 0)
	if err != nil {
		t.Fatalf("ParseQuestions: %v", err)
	}
	if !specs[0].MultiSelect || !specs[0].IsSecret {
		t.Errorf("snake_case flags not parsed: %+v", specs[0])
	}
}

func TestParseQuestions_AcceptsSpecSlice(t *testing.T) {
	specs, err := ParseQuestions(map[string]interface{}{
		"questions": []question.Spec{{ID: "a", Header: "A", Question: "Q?"}},
	}, 0)
	if err != nil {
		t.Fatalf("ParseQuestions: %v", err)
	}
	if len(specs) != 1 || specs[0].ID != "a" {
		t.Errorf("specs = %+v", specs)
	}
}

func TestParseQuestions_Validation(t *testing.T) {
	twoOptions := func() []interface{} {
		return []interface{}{
			map[string]interface{}{"label": "A"},
			map[string]interface{}{"label": "B"},
		}
	}
	valid := func() map[string]interface{} {
		return map[string]interface{}{
			"questions": []interface{}{map[string]interface{}{
				"id": "scope", "header": "范围", "question": "Q?",
			}},
		}
	}
	mutate := func(fn func(map[string]interface{})) map[string]interface{} {
		in := valid()
		q := in["questions"].([]interface{})[0].(map[string]interface{})
		fn(q)
		return in
	}

	cases := []struct {
		name  string
		input map[string]interface{}
		want  string
	}{
		{"nil input", nil, "required"},
		{"empty array", map[string]interface{}{"questions": []interface{}{}}, "required"},
		{"too many", map[string]interface{}{"questions": []interface{}{
			map[string]interface{}{"id": "a", "header": "A", "question": "Q"},
			map[string]interface{}{"id": "b", "header": "B", "question": "Q"},
			map[string]interface{}{"id": "c", "header": "C", "question": "Q"},
			map[string]interface{}{"id": "d", "header": "D", "question": "Q"},
		}}, "too many questions"},
		{"missing id", mutate(func(q map[string]interface{}) { delete(q, "id") }), "id is required"},
		{"bad id", mutate(func(q map[string]interface{}) { q["id"] = "NotSnake" }), "snake_case"},
		{"missing header", mutate(func(q map[string]interface{}) { delete(q, "header") }), "header is required"},
		{"long header", mutate(func(q map[string]interface{}) { q["header"] = "abcdefghijklm" }), "max 12"},
		{"missing question", mutate(func(q map[string]interface{}) { delete(q, "question") }), "question is required"},
		{"one option", mutate(func(q map[string]interface{}) {
			q["options"] = []interface{}{map[string]interface{}{"label": "A"}}
		}), "2-4 entries"},
		{"five options", mutate(func(q map[string]interface{}) {
			q["options"] = []interface{}{
				map[string]interface{}{"label": "A"}, map[string]interface{}{"label": "B"},
				map[string]interface{}{"label": "C"}, map[string]interface{}{"label": "D"},
				map[string]interface{}{"label": "E"},
			}
		}), "2-4 entries"},
		{"empty label", mutate(func(q map[string]interface{}) {
			q["options"] = twoOptions()
			q["options"].([]interface{})[0].(map[string]interface{})["label"] = " "
		}), "label is required"},
		{"duplicate label", mutate(func(q map[string]interface{}) {
			q["options"] = []interface{}{
				map[string]interface{}{"label": "A"}, map[string]interface{}{"label": "A"},
			}
		}), "duplicates"},
		{"question not a string", map[string]interface{}{"question": 42}, "must be a string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseQuestions(tc.input, 0)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.want)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}

	// Duplicate ids across questions.
	_, err := ParseQuestions(map[string]interface{}{"questions": []interface{}{
		map[string]interface{}{"id": "a", "header": "A", "question": "Q"},
		map[string]interface{}{"id": "a", "header": "B", "question": "Q"},
	}}, 0)
	if err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Errorf("duplicate id error = %v", err)
	}
}

func TestQuestionTool_Schema(t *testing.T) {
	tool := NewQuestionTool()
	schema := tool.Schema()
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("schema properties missing: %#v", schema)
	}
	if _, ok := props["questions"]; !ok {
		t.Fatal("schema must expose questions[]")
	}
	if _, ok := props["question"]; !ok {
		t.Fatal("schema must keep the legacy question field")
	}
	questions := props["questions"].(map[string]interface{})
	items := questions["items"].(map[string]interface{})
	iprops := items["properties"].(map[string]interface{})
	for _, key := range []string{"id", "header", "question", "options", "multiSelect", "isSecret"} {
		if _, ok := iprops[key]; !ok {
			t.Errorf("question schema missing %q", key)
		}
	}
	opts := iprops["options"].(map[string]interface{})
	optItems := opts["items"].(map[string]interface{})
	oprops := optItems["properties"].(map[string]interface{})
	for _, key := range []string{"label", "description", "preview"} {
		if _, ok := oprops[key]; !ok {
			t.Errorf("option schema missing %q", key)
		}
	}
}

func TestQuestionTool_BlockingAsker(t *testing.T) {
	var gotRequest question.Request
	asker := question.AskerFunc(func(ctx context.Context, req question.Request) (question.Answers, error) {
		gotRequest = req
		return question.Answers{
			"scope": {"仅核心"},
			"note":  {"user_note: 先不要动生产"},
		}, nil
	})
	ctx := question.WithAsker(context.Background(), asker)
	ctx = question.WithToolCallID(ctx, "call_42")

	tool := NewQuestionTool()
	res, err := tool.Execute(ctx, map[string]interface{}{
		"questions": []interface{}{map[string]interface{}{
			"id": "scope", "header": "范围", "question": "处理哪些服务？",
			"options": []interface{}{
				map[string]interface{}{"label": "全部"},
				map[string]interface{}{"label": "仅核心"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	answered, ok := res.(AnsweredQuestionResult)
	if !ok {
		t.Fatalf("expected AnsweredQuestionResult, got %T", res)
	}
	if !answered.Answered || !answered.Ok {
		t.Errorf("answered result flags = %+v", answered)
	}
	if gotRequest.ToolCallID != "call_42" {
		t.Errorf("tool_call_id = %q", gotRequest.ToolCallID)
	}
	if len(gotRequest.Questions) != 1 || gotRequest.Questions[0].ID != "scope" {
		t.Errorf("questions passed to asker = %+v", gotRequest.Questions)
	}
	if got := answered.Answers["scope"]; len(got) != 1 || got[0] != "仅核心" {
		t.Errorf("answers = %+v", answered.Answers)
	}
	if got := answered.Answers["note"]; len(got) != 1 || got[0] != "user_note: 先不要动生产" {
		t.Errorf("free-text answer = %+v", answered.Answers)
	}
	// A blocking result is not a sentinel: the engine must not halt the turn.
	if s := SentinelFromOutput(answered); s != nil {
		t.Errorf("answered result must not be detected as sentinel: %+v", s)
	}
}

func TestQuestionTool_BlockingAskerError(t *testing.T) {
	asker := question.AskerFunc(func(context.Context, question.Request) (question.Answers, error) {
		return nil, question.ErrQuestionTimeout
	})
	ctx := question.WithAsker(context.Background(), asker)
	tool := NewQuestionTool()
	_, err := tool.Execute(ctx, map[string]interface{}{"question": "proceed?"})
	if !errors.Is(err, question.ErrQuestionTimeout) {
		t.Fatalf("error = %v, want ErrQuestionTimeout", err)
	}
}

func TestQuestionTool_ValidationPrecedesAsker(t *testing.T) {
	called := false
	asker := question.AskerFunc(func(context.Context, question.Request) (question.Answers, error) {
		called = true
		return nil, nil
	})
	ctx := question.WithAsker(context.Background(), asker)
	tool := NewQuestionTool()
	if _, err := tool.Execute(ctx, map[string]interface{}{"questions": []interface{}{}}); err == nil {
		t.Fatal("expected validation error")
	}
	if called {
		t.Error("asker must not be called for invalid input")
	}
}

func TestSentinelFromOutput(t *testing.T) {
	structured := SentinelQuestionResult{
		Ok:        true,
		AskUser:   true,
		Question:  "first",
		Questions: []question.Spec{{ID: "a", Header: "A", Question: "first"}},
	}
	got := SentinelFromOutput(structured)
	if got == nil || got.Question != "first" || len(got.Questions) != 1 {
		t.Fatalf("struct: %+v", got)
	}
	if got := SentinelFromOutput(&structured); got == nil {
		t.Fatal("pointer: nil")
	}

	m := map[string]interface{}{
		"ok": true, "ask_user": true, "question": "map q",
		"questions": []interface{}{map[string]interface{}{
			"id": "a", "header": "A", "question": "map q",
		}},
	}
	got = SentinelFromOutput(m)
	if got == nil || got.Question != "map q" || len(got.Questions) != 1 {
		t.Fatalf("map: %+v", got)
	}

	raw := `{"ok": true, "ask_user": true, "question": "json q"}`
	if got := SentinelFromOutput(raw); got == nil || got.Question != "json q" {
		t.Fatalf("json string: %+v", got)
	}

	if got := SentinelFromOutput(map[string]interface{}{"ask_user": false, "question": "x"}); got != nil {
		t.Errorf("ask_user=false must not be a sentinel: %+v", got)
	}
	if got := SentinelFromOutput(map[string]interface{}{"result": "ok"}); got != nil {
		t.Errorf("non-question output must not be a sentinel: %+v", got)
	}
	if got := SentinelFromOutput(nil); got != nil {
		t.Error("nil must not be a sentinel")
	}
	if got := SentinelFromOutput(AnsweredQuestionResult{Ok: true, Answered: true}); got != nil {
		t.Error("answered result must not be a sentinel")
	}
}

// TestQuestionTool_SchemaValidationAcceptsBothForms 保证引擎的 schema 预校验
// （在 Execute 之前）不会拒绝结构化输入：`question` 不能是 required。
func TestQuestionTool_SchemaValidationAcceptsBothForms(t *testing.T) {
	tool := NewQuestionTool()
	structured := map[string]interface{}{
		"questions": []interface{}{map[string]interface{}{
			"id": "scope", "header": "范围", "question": "Q?",
		}},
	}
	if err := schema.ValidateInput(tool.Schema(), structured, "question"); err != nil {
		t.Errorf("structured input rejected by schema: %v", err)
	}
	if err := schema.ValidateInput(tool.Schema(), map[string]interface{}{"question": "legacy?"}, "question"); err != nil {
		t.Errorf("legacy input rejected by schema: %v", err)
	}
}
