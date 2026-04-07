package runner

import "testing"

func TestAppendSprintContractPrompt(t *testing.T) {
	s := AppendSprintContractPrompt("ship feature X")
	if s == "" || len(s) < 20 {
		t.Fatal(s)
	}
}
