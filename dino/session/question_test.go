package session

import (
	"testing"
)

// TestQuestionFromOutput_Struct 验证 struct 形态的 SentinelQuestionResult 被识别。
func TestQuestionFromOutput_Struct(t *testing.T) {
	out := struct {
		Ok       bool   `json:"ok"`
		Question string `json:"question"`
		AskUser  bool   `json:"ask_user"`
	}{Ok: true, Question: "请确认是否继续？", AskUser: true}
	if q := questionFromOutput(out); q != "请确认是否继续？" {
		t.Errorf("expected question text, got %q", q)
	}
}

// TestQuestionFromOutput_Map 验证 map 形态（JSON 反序列化后）被识别。
func TestQuestionFromOutput_Map(t *testing.T) {
	out := map[string]interface{}{
		"ok":       true,
		"question": "Are you sure?",
		"ask_user": true,
	}
	if q := questionFromOutput(out); q != "Are you sure?" {
		t.Errorf("expected question text, got %q", q)
	}
}

// TestQuestionFromOutput_NotQuestion 验证非 question sentinel 返回空。
func TestQuestionFromOutput_NotQuestion(t *testing.T) {
	// ask_user=false 的 sentinel 不触发。
	out := map[string]interface{}{
		"ok":       true,
		"question": "not a real question",
		"ask_user": false,
	}
	if q := questionFromOutput(out); q != "" {
		t.Errorf("expected empty for ask_user=false, got %q", q)
	}
	// 普通工具输出（map 无 ask_user）不触发。
	if q := questionFromOutput(map[string]interface{}{"result": "ok"}); q != "" {
		t.Errorf("expected empty for non-question output, got %q", q)
	}
	// nil 不触发。
	if q := questionFromOutput(nil); q != "" {
		t.Errorf("expected empty for nil, got %q", q)
	}
}

// TestEventTypeQuestion_Constant 验证事件类型常量定义。
func TestEventTypeQuestion_Constant(t *testing.T) {
	if EventTypeQuestion != "question" {
		t.Errorf("EventTypeQuestion = %q, want %q", EventTypeQuestion, "question")
	}
}
