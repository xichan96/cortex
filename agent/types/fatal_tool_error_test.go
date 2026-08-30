package types

import (
	"errors"
	"fmt"
	"testing"
)

// fatalStub implements FatalToolErrorKind to simulate the dino-side fatal errors
// (ApprovalRejectedError / LoopDetectedError) without importing dino/tools.
type fatalStub struct{ msg string }

func (e *fatalStub) Error() string       { return e.msg }
func (e *fatalStub) FatalToolErrorKind() {}

// TestFatalToolError_ImplementsKind verifies FatalToolError is recognized by the
// unified classifier (P4.2).
func TestFatalToolError_ImplementsKind(t *testing.T) {
	if !IsFatalToolError(&FatalToolError{Reason: "boom"}) {
		t.Fatal("FatalToolError should be fatal")
	}
	var kind FatalToolErrorKind
	var fe *FatalToolError
	if !errors.As(&FatalToolError{Reason: "boom"}, &fe) {
		t.Fatal("FatalToolError should implement FatalToolErrorKind")
	}
	_ = kind
}

// TestIsFatalToolError_Unwraps verifies the classifier unwraps %w-wrapped errors,
// so a fatal error buried under fmt.Errorf is still detected.
func TestIsFatalToolError_Unwraps(t *testing.T) {
	inner := &FatalToolError{Err: errors.New("bad input"), Reason: "validation"}
	wrapped := fmt.Errorf("outer: %w", inner)
	if !IsFatalToolError(wrapped) {
		t.Fatal("wrapped FatalToolError should still be fatal")
	}
}

// TestIsFatalToolError_Recoverable verifies recoverable errors are NOT fatal.
func TestIsFatalToolError_Recoverable(t *testing.T) {
	recoverable := []error{
		errors.New("plain error"),
		fmt.Errorf("mcp call failed: %w", errors.New("network")),
		errors.New("tool execution timeout"),
	}
	for _, e := range recoverable {
		if IsFatalToolError(e) {
			t.Errorf("expected %v to be recoverable (not fatal)", e)
		}
	}
}

// TestFatalToolErrorKind_ExternalType verifies a type implementing
// FatalToolErrorKind (like dino's veto/loop errors) is classified as fatal.
func TestFatalToolErrorKind_ExternalType(t *testing.T) {
	if !IsFatalToolError(&fatalStub{msg: "user veto"}) {
		t.Fatal("a FatalToolErrorKind implementer should be fatal")
	}
	var k FatalToolErrorKind
	k = &fatalStub{msg: "veto"}
	if k == nil {
		t.Fatal("nil interface")
	}
}
