package golem

import (
	"strings"
	"testing"
)

func TestP7EventSelectRejectsEmptyAndCannotEvadeDuplicateDetection(t *testing.T) {
	if _, err := RuntimeFreezeEventOptions(readPostDescriptor, EventSelect[readPost]()); err == nil || !strings.Contains(err.Error(), "selection is empty") {
		t.Fatalf("empty selection error = %v", err)
	}
	if _, err := RuntimeFreezeEventOptions(readPostDescriptor, EventSelect[readPost](readPosts.Title), EventSelect[readPost]()); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("duplicate selection error = %v", err)
	}
}
