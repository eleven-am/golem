package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
)

func TestPresentErrorCarriesReasonAndLimitExtensions(t *testing.T) {
	trusted := errors.New("P4_MUTATION_LIMIT: private detail")
	tests := []struct {
		name    string
		failure error
		want    string
	}{
		{
			name:    "limit",
			failure: golem.RuntimeLimitError("delete", golem.ModelID{1}, golem.LimitOutboxBytes, 4096, "request exceeds the outbox_bytes limit of 4096", trusted),
			want:    `{"message":"request exceeds the outbox_bytes limit of 4096","path":["alias"],"extensions":{"code":"LIMIT_EXCEEDED","limit":"outbox_bytes","max":4096,"reason":"LIMIT_EXCEEDED"}}`,
		},
		{
			name:    "value",
			failure: golem.RuntimeOperationErrorWithReason(golem.CodeBadUserInput, golem.ReasonValueTooLong, "create", golem.ModelID{1}, golem.FieldID{2}, "value for Asset.Name exceeds maximum length 255", trusted),
			want:    `{"message":"value for Asset.Name exceeds maximum length 255","path":["alias"],"extensions":{"code":"BAD_USER_INPUT","reason":"VALUE_TOO_LONG"}}`,
		},
		{
			name:    "not granted",
			failure: golem.RuntimeOperationErrorWithReason(golem.CodeForbidden, golem.ReasonNotGranted, "findMany", golem.ModelID{1}, golem.FieldID{}, "operation is not granted", trusted),
			want:    `{"message":"operation is not granted","path":["alias"],"extensions":{"code":"FORBIDDEN","reason":"NOT_GRANTED"}}`,
		},
		{
			name:    "authorization unproven",
			failure: golem.RuntimeOperationErrorWithReason(golem.CodeForbidden, golem.ReasonAuthorizationUnproven, "update", golem.ModelID{1}, golem.FieldID{}, "authorization could not be proven", trusted),
			want:    `{"message":"authorization could not be proven","path":["alias"],"extensions":{"code":"FORBIDDEN","reason":"AUTHORIZATION_UNPROVEN"}}`,
		},
		{
			name:    "policy invalid",
			failure: golem.RuntimeOperationErrorWithReason(golem.CodeForbidden, golem.ReasonPolicyInvalid, "count", golem.ModelID{1}, golem.FieldID{}, "policy cannot be applied to this request", trusted),
			want:    `{"message":"policy cannot be applied to this request","path":["alias"],"extensions":{"code":"FORBIDDEN","reason":"POLICY_INVALID"}}`,
		},
		{
			name:    "no reason",
			failure: golem.RuntimeReadError(golem.CodeNotFound, "findUnique", golem.ModelID{1}, golem.FieldID{}, "record not found", trusted),
			want:    `{"message":"record not found","path":["alias"],"extensions":{"code":"NOT_FOUND"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reported := 0
			presented := PresentError(context.Background(), fmt.Errorf("outer: %w", test.failure), []any{"alias"}, func(context.Context, error) { reported++ })
			encoded, err := json.Marshal(presented)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != test.want || reported != 0 {
				t.Fatalf("presented=%s reported=%d\nwant=%s", encoded, reported, test.want)
			}
		})
	}
}

func TestLimitExceededObservesAsALimitRefusalOverGraphQL(t *testing.T) {
	limited := golem.RuntimeLimitError("findMany", golem.ModelID{1}, golem.LimitTake, 50, "request exceeds the take limit of 50", nil)
	if outcome, reason := graphQLErrorObservationResult(limited); outcome != observe.OutcomeRefused || reason != observe.ReasonLimit {
		t.Fatalf("child outcome=%s reason=%s", outcome, reason)
	}
	response := Response{Errors: []Error{PresentError(context.Background(), limited, []any{"items"}, nil)}}
	if outcome, reason := graphQLObservationResult(response); outcome != observe.OutcomeRefused || reason != observe.ReasonLimit {
		t.Fatalf("response outcome=%s reason=%s", outcome, reason)
	}
}
