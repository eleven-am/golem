package golem

import (
	"strings"
	"testing"
)

func TestEventSelectRejectsEmptyAndCannotEvadeDuplicateDetection(t *testing.T) {
	if _, err := RuntimeFreezeEventOptions(readPostDescriptor, EventSelect[readPost]()); err == nil || !strings.Contains(err.Error(), "GOLEM_SUBSCRIPTION_INVALID: selection is empty") {
		t.Fatalf("empty selection error = %v", err)
	}
	if _, err := RuntimeFreezeEventOptions(readPostDescriptor, EventSelect[readPost](readPosts.Title), EventSelect[readPost]()); err == nil || !strings.Contains(err.Error(), "GOLEM_SUBSCRIPTION_INVALID: selection is declared more than once") {
		t.Fatalf("duplicate selection error = %v", err)
	}
}
