package graphql

import (
	"context"
	"errors"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/observeexec"
	"github.com/eleven-am/golem/go/observe"
)

func finishGraphQLChild(span *observeexec.Span, err error) {
	outcome, reason := graphQLErrorObservationResult(err)
	observeexec.Finish(span, outcome, reason)
}

func graphQLErrorObservationResult(err error) (observe.Outcome, observe.Reason) {
	if err == nil {
		return observe.OutcomeSuccess, observe.ReasonNone
	}
	if errors.Is(err, context.Canceled) {
		return observe.OutcomeCancelled, observe.ReasonNone
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return observe.OutcomeCancelled, observe.ReasonTimeout
	}
	var failure *golem.Error
	if errors.As(err, &failure) {
		switch failure.Code {
		case golem.CodeForbidden, golem.CodeUnauthenticated:
			return observe.OutcomeRefused, observe.ReasonAuthorization
		case golem.CodeNotFound:
			return observe.OutcomeRefused, observe.ReasonNotFound
		case golem.CodeConflict:
			return observe.OutcomeRefused, observe.ReasonConflict
		case golem.CodeBadUserInput:
			return observe.OutcomeRefused, observe.ReasonInvalidInput
		}
	}
	return observe.OutcomeFailure, observe.ReasonProvider
}
