package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/mutation/rowlock"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

const lockKeysOperation = "lockKeys"

func CallerTxLockKeys[P, A any](ctx context.Context, transaction *CallerTx[P, A], keys ...golem.LockKey) error {
	return operationErr(transactionOperation(ctx, transaction.binding(), func(ctx context.Context) (struct{}, error) {
		if transaction == nil || transaction.caller == nil {
			return struct{}{}, fmt.Errorf("P4_RUNTIME_TRANSACTION: caller transaction is unavailable")
		}
		return struct{}{}, lockKeys(ctx, transaction.caller.app, transaction.caller.executor, keys)
	}))
}

func SystemTxLockKeys[P, A any](ctx context.Context, transaction *SystemTx[P, A], keys ...golem.LockKey) error {
	return operationErr(transactionOperation(ctx, transaction.binding(), func(ctx context.Context) (struct{}, error) {
		if transaction == nil || transaction.system.app == nil {
			return struct{}{}, fmt.Errorf("P4_RUNTIME_TRANSACTION: system transaction is unavailable")
		}
		return struct{}{}, lockKeys(ctx, transaction.system.app, transaction.system.executor, keys)
	}))
}

func operationErr(_ struct{}, err error) error { return err }

func lockKeys[P, A any](ctx context.Context, app *App[P, A], binding *executionBinding, keys []golem.LockKey) error {
	ordered := make([]rowlock.Key, len(keys))
	for index, key := range keys {
		identity, err := golem.RuntimeLockKeyIdentity(key)
		if err != nil {
			return golem.RuntimeOperationError(golem.CodeBadUserInput, lockKeysOperation, golem.ModelID{}, golem.FieldID{}, "lock key is invalid", err)
		}
		ordered[index] = rowlock.AppKey(identity)
	}
	if callErr := binding.requireAdmitted(ctx); callErr != nil {
		return callErr
	}
	if binding.rowLocks() == nil {
		return fmt.Errorf("P4_RUNTIME_TRANSACTION: lock keys require a transaction-bound executor")
	}
	if app.provider != policyir.ProviderPostgreSQL || len(ordered) == 0 {
		return nil
	}
	queryer, err := binding.queryerFor(app.database)
	if err != nil {
		return err
	}
	err = binding.lockSession(queryer, app.registry, app.provider, app.mutationLimits.statementParameters).LockApp(ctx, ordered)
	if err == nil {
		return nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return golem.RuntimeOperationError(golem.CodeConflict, lockKeysOperation, golem.ModelID{}, golem.FieldID{}, "lock key wait was cancelled", contextErr)
	}
	var contention *rowlock.ContentionError
	if errors.As(err, &contention) {
		return golem.RuntimeOperationError(golem.CodeConflict, lockKeysOperation, golem.ModelID{}, golem.FieldID{}, "lock key is held by another transaction", err)
	}
	return golem.RuntimeOperationError(golem.CodeBadUserInput, lockKeysOperation, golem.ModelID{}, golem.FieldID{}, "lock keys could not be acquired", err)
}
