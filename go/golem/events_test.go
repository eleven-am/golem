package golem

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestEventRecheckRequiresACheckAndAnIntervalOfAtLeastOneSecond(t *testing.T) {
	check := func(context.Context) error { return nil }
	for name, option := range map[string]EventOption[readPost]{
		"nanosecond":     EventRecheck[readPost](time.Nanosecond, check),
		"below a second": EventRecheck[readPost](999*time.Millisecond, check),
		"zero":           EventRecheck[readPost](0, check),
		"nil check":      EventRecheck[readPost](time.Second, nil),
	} {
		if _, err := RuntimeFreezeEventOptions(readPostDescriptor, option); err == nil || !strings.Contains(err.Error(), "GOLEM_SUBSCRIPTION_INVALID: recheck requires") {
			t.Fatalf("%s: error = %v", name, err)
		}
	}
	frozen, err := RuntimeFreezeEventOptions(readPostDescriptor, EventRecheck[readPost](time.Second, check))
	if err != nil {
		t.Fatal(err)
	}
	if interval, retained, present := frozen.Recheck(); !present || retained == nil || interval != time.Second {
		t.Fatalf("recheck interval=%s present=%t", interval, present)
	}
	if _, err := RuntimeFreezeEventOptions(readPostDescriptor, EventRecheck[readPost](time.Second, check), EventRecheck[readPost](time.Second, check)); err == nil || !strings.Contains(err.Error(), "recheck is declared more than once") {
		t.Fatalf("duplicate recheck error = %v", err)
	}
}

func TestEventSelectRejectsEmptyAndCannotEvadeDuplicateDetection(t *testing.T) {
	if _, err := RuntimeFreezeEventOptions(readPostDescriptor, EventSelect[readPost]()); err == nil || !strings.Contains(err.Error(), "GOLEM_SUBSCRIPTION_INVALID: selection is empty") {
		t.Fatalf("empty selection error = %v", err)
	}
	if _, err := RuntimeFreezeEventOptions(readPostDescriptor, EventSelect[readPost](readPosts.Title), EventSelect[readPost]()); err == nil || !strings.Contains(err.Error(), "GOLEM_SUBSCRIPTION_INVALID: selection is declared more than once") {
		t.Fatalf("duplicate selection error = %v", err)
	}
}
