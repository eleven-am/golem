package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
)

var errTxOperationPanicked = errors.New("P5_CUSTOM_OPERATION: transaction operation panicked")

func RunCallerTxOperation[P, A, T, Args, R any](ctx context.Context, transaction *CallerTx[P, A], operation func(context.Context, T, Args) (R, error), wrap func(*CallerTx[P, A]) T, arguments Args) (R, error) {
	return transactionOperation(ctx, transaction.binding(), func(ctx context.Context) (result R, err error) {
		if operation == nil || wrap == nil {
			return result, fmt.Errorf("P5_CUSTOM_OPERATION: operation and transaction wrapper are required")
		}
		if ctx == nil {
			return result, fmt.Errorf("P5_CUSTOM_OPERATION: context is required")
		}
		if transaction == nil || transaction.caller == nil || transaction.caller.app == nil || transaction.caller.policies == nil || transaction.caller.execution == 0 || transaction.caller.executor == nil || !transaction.caller.executor.scoped {
			return result, fmt.Errorf("P5_CUSTOM_OPERATION: caller transaction is unavailable")
		}
		caller := transaction.caller
		identity, ok := caller.app.bindings.TxOperation(operation)
		if !ok {
			return result, fmt.Errorf("P5_CUSTOM_OPERATION: operation is not a generated transaction operation")
		}
		ctx, observation := beginExecutionObservation(ctx, caller.app, caller.executor, golem.ModelID{}, observe.KindMutation, observe.OperationMutationOperation)
		policies, hide, release := caller.policies.WithinTransaction(identity)
		caller.executor.releaseAfterTransaction(release)
		scopedCaller := *caller
		scopedCaller.policies = policies
		scoped := &CallerTx[P, A]{caller: &scopedCaller, scope: (&usageGate{}).init()}
		finished := false
		defer func() {
			scoped.scope.close()
			hide()
			if finished {
				finishObservation(observation, err)
				return
			}
			finishObservation(observation, errTxOperationPanicked)
		}()
		result, err = operation(ctx, wrap(scoped), arguments)
		finished = true
		return result, err
	})
}
