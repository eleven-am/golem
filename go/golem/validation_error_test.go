package golem

import (
	"errors"
	"testing"
)

func TestInputErrorPreservesFieldReasonAndCause(t *testing.T) {
	cause := errors.New("private validation detail")
	err := InputError(ReasonInvalidValue, "saveDraft", "accountId", "accountId is required", cause)
	var failure *Error
	if !errors.As(err, &failure) {
		t.Fatalf("error = %T, want *Error", err)
	}
	if failure.Code != CodeBadUserInput || failure.Reason != ReasonInvalidValue || failure.Operation != "saveDraft" || failure.InputField != "accountId" || failure.Message != "accountId is required" {
		t.Fatalf("failure = %+v", failure)
	}
	if !errors.Is(err, cause) {
		t.Fatal("private cause was not preserved for trusted logging")
	}
}
