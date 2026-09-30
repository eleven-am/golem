package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
)

var errCustomOperationPanicked = errors.New("P5_CUSTOM_OPERATION: resolver panicked")

// RunCallerOperation runs one generated custom mutation resolver as its
// operation. The resolver receives the same principal, execution and hooks as
// caller, with that resolver's Within grants added to the caller's policy; the
// grants end when the resolver returns or panics, and a caller retained past
// that point answers exactly as caller does. A resolver that is not a
// registered custom mutation is refused before anything is observed or run.
func RunCallerOperation[P, A, C, Args, R any](ctx context.Context, caller *Caller[P, A], resolver func(context.Context, C, Args) (R, error), wrap func(*Caller[P, A]) C, arguments Args) (R, error) {
	var zero R
	if resolver == nil || wrap == nil {
		return zero, fmt.Errorf("P5_CUSTOM_OPERATION: resolver and caller wrapper are required")
	}
	return runCallerOperation(ctx, caller, resolver, func(ctx context.Context, scoped *Caller[P, A]) (R, error) {
		return resolver(ctx, wrap(scoped), arguments)
	})
}

// DispatchCallerOperation is RunCallerOperation for generated GraphQL
// dispatch, whose resolver caller and argument types are erased: run invokes
// resolver with the operation's caller.
func DispatchCallerOperation[P, A any](ctx context.Context, caller *Caller[P, A], resolver any, run func(context.Context, *Caller[P, A]) (any, error)) (any, error) {
	if run == nil {
		return nil, fmt.Errorf("P5_CUSTOM_OPERATION: resolver dispatch is required")
	}
	return runCallerOperation(ctx, caller, resolver, run)
}

func runCallerOperation[P, A, R any](ctx context.Context, caller *Caller[P, A], resolver any, run func(context.Context, *Caller[P, A]) (R, error)) (R, error) {
	var zero R
	if ctx == nil {
		return zero, fmt.Errorf("P5_CUSTOM_OPERATION: context is required")
	}
	if caller == nil || caller.app == nil || caller.policies == nil || caller.execution == 0 {
		return zero, fmt.Errorf("P5_CUSTOM_OPERATION: caller execution is unavailable")
	}
	operation, ok := caller.app.bindings.Operation(resolver)
	if !ok {
		return zero, fmt.Errorf("P5_CUSTOM_OPERATION: resolver is not a generated custom mutation")
	}
	ctx, observation := beginExecutionObservation(ctx, caller.app, caller.executor, golem.ModelID{}, observe.KindMutation, observe.OperationMutationCustom)
	policies, release := caller.policies.Within(operation)
	scoped := *caller
	scoped.policies = policies
	finished := false
	defer func() {
		if !finished {
			release()
			finishObservation(observation, errCustomOperationPanicked)
		}
	}()
	result, err := run(ctx, &scoped)
	finished = true
	release()
	finishObservation(observation, err)
	return result, err
}
