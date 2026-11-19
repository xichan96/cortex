package extensions

import (
	"testing"
)

func TestEventClassification(t *testing.T) {
	cases := []struct {
		event      Event
		classes    []EventClass
		failClosed bool
	}{
		{EventProjectTrust, []EventClass{ClassGate}, false},
		{EventSessionStart, []EventClass{ClassNotification}, false},
		{EventSessionBeforeSwitch, []EventClass{ClassBoundary}, false},
		{EventSessionBeforeFork, []EventClass{ClassBoundary}, false},
		{EventSessionBeforeCompact, []EventClass{ClassTransform, ClassBoundary}, false},
		{EventSessionCompact, []EventClass{ClassNotification}, false},
		{EventSessionShutdown, []EventClass{ClassNotification}, false},
		{EventAgentStart, []EventClass{ClassNotification}, false},
		{EventTurnStart, []EventClass{ClassNotification}, false},
		{EventTurnEnd, []EventClass{ClassBoundary}, false},
		{EventAgentBeforeSettle, []EventClass{ClassBoundary}, false},
		{EventAgentSettled, []EventClass{ClassNotification}, false},
		{EventMessageStart, []EventClass{ClassNotification}, false},
		{EventMessageUpdate, []EventClass{ClassNotification}, false},
		{EventMessageEnd, []EventClass{ClassTransform}, false},
		{EventToolCall, []EventClass{ClassMutation, ClassGate}, true},
		{EventToolResult, []EventClass{ClassMutation, ClassTransform}, false},
		{EventToolExecutionStart, []EventClass{ClassNotification}, false},
		{EventToolExecutionUpdate, []EventClass{ClassNotification}, false},
		{EventToolExecutionEnd, []EventClass{ClassNotification}, false},
		{EventContext, []EventClass{ClassTransform}, false},
		{EventBeforeProviderRequest, []EventClass{ClassTransform}, false},
		{EventAfterProviderResponse, []EventClass{ClassNotification}, false},
		{EventInput, []EventClass{ClassTransform, ClassGate}, false},
		{EventUserBash, []EventClass{ClassGate}, false},
		{EventModelSelect, []EventClass{ClassLastOverride}, false},
	}

	for _, tc := range cases {
		if !tc.event.IsKnown() {
			t.Fatalf("%q is not in the catalogue", tc.event)
		}
		spec := tc.event.Spec()
		if len(spec.Classes) != len(tc.classes) {
			t.Fatalf("%s classes = %v, want %v", tc.event, spec.Classes, tc.classes)
		}
		for i, class := range tc.classes {
			if spec.Classes[i] != class {
				t.Fatalf("%s classes = %v, want %v", tc.event, spec.Classes, tc.classes)
			}
		}
		if spec.FailClosed != tc.failClosed {
			t.Fatalf("%s FailClosed = %v, want %v", tc.event, spec.FailClosed, tc.failClosed)
		}
	}

	if got, want := len(KnownEvents()), len(cases); got != want {
		t.Fatalf("KnownEvents = %d, want %d (catalogue and table drifted)", got, want)
	}
}

func TestUnknownEventDefaultsToNotification(t *testing.T) {
	unknown := Event("future_event")
	if unknown.IsKnown() {
		t.Fatal("unknown event reported as known")
	}
	if !unknown.HasClass(ClassNotification) {
		t.Fatalf("classes = %v, want notification", unknown.Classes())
	}
	if unknown.Spec().FailClosed {
		t.Fatal("unknown event must not fail closed")
	}
}

func TestEventSpecIsCopied(t *testing.T) {
	classes := EventToolCall.Classes()
	classes[0] = ClassNotification
	if !EventToolCall.HasClass(ClassMutation) {
		t.Fatal("mutating the returned Classes slice leaked into the catalogue")
	}
}

func TestKnownEventsSorted(t *testing.T) {
	events := KnownEvents()
	for i := 1; i < len(events); i++ {
		if events[i-1] >= events[i] {
			t.Fatalf("KnownEvents not sorted: %q >= %q", events[i-1], events[i])
		}
	}
}
