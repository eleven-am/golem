package golem

import (
	"errors"
	"testing"
)

func TestCommittedResultErrorHasItsOwnCodeAndReason(t *testing.T) {
	cause := errors.New("hydration failed")
	err := RuntimeCommittedResultError("customMutation", ReasonResultNotReadable, 2, "", 0, "result is unavailable", cause)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeCommittedResultUnavailable || failure.Reason != ReasonResultNotReadable || failure.Unreadable != 2 {
		t.Fatalf("committed error = %#v", err)
	}
	if !errors.Is(err, CodeCommittedResultUnavailable) || !errors.Is(err, ReasonResultNotReadable) || errors.Is(err, CodeNotFound) {
		t.Fatal("committed error does not match its own code and reason")
	}
	if !errors.Is(err, cause) {
		t.Fatal("the cause is not retained for trusted logging")
	}
	if got := err.Error(); got != "COMMITTED_RESULT_UNAVAILABLE: result is unavailable" {
		t.Fatalf("public text = %q", got)
	}
}

func TestCommittedResultCodeCannotBeForgedThroughTheOperationConstructor(t *testing.T) {
	err := RuntimeOperationError(CodeCommittedResultUnavailable, "customMutation", ModelID{}, FieldID{}, "forged", nil)
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != CodeBadUserInput {
		t.Fatalf("forged committed error = %#v", err)
	}
}
