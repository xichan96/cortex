package session

import "testing"

type resetBudgetFake struct {
	resets int
}

func (f *resetBudgetFake) CanExecute(string) bool { return false }
func (f *resetBudgetFake) Reset(string)           { f.resets++ }

// 长会话不能因为上一轮累计 tokens 超过 Budget 而永久停在 Budget exceeded：
// 每个新输入开始前要清掉「一次运行」的预算。
func TestResetTurnBudget(t *testing.T) {
	f := &resetBudgetFake{}
	resetTurnBudget(f, "s1")
	if f.resets != 1 {
		t.Fatalf("resets = %d, want 1", f.resets)
	}

	// 只实现 CanExecute 的旧 fake 不应 panic。
	resetTurnBudget(onlyCanExecute{}, "s1")
}

type onlyCanExecute struct{}

func (onlyCanExecute) CanExecute(string) bool { return true }
