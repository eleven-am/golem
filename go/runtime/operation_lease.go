package runtime

import (
	"context"
	"sync"

	"github.com/eleven-am/golem/go/golem"
	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
)

type operationLeaseKey struct{}

func operationLeaseFrom(ctx context.Context) *policyruntime.Lease {
	if ctx == nil {
		return nil
	}
	lease, _ := ctx.Value(operationLeaseKey{}).(*policyruntime.Lease)
	return lease
}

func withoutOperationLease(ctx context.Context) context.Context {
	if operationLeaseFrom(ctx) == nil {
		return ctx
	}
	return context.WithValue(ctx, operationLeaseKey{}, (*policyruntime.Lease)(nil))
}

func commitWithinOperation(ctx context.Context, commit func() error) error {
	return operationLeaseFrom(ctx).Commit(commit)
}

func callerWrite[P, A, R any](ctx context.Context, caller *Caller[P, A], run func(context.Context, *Caller[P, A]) (R, error)) (R, error) {
	if ctx == nil || caller == nil || caller.policies == nil {
		return run(ctx, caller)
	}
	if existing := operationLeaseFrom(ctx); existing != nil {
		if policies := caller.policies.WithLease(existing); policies != caller.policies {
			leased := *caller
			leased.policies = policies
			return run(ctx, &leased)
		}
	}
	policies, lease := caller.policies.Lease()
	if lease == nil {
		return run(withoutOperationLease(ctx), caller)
	}
	leased := *caller
	leased.policies = policies
	result, err := run(context.WithValue(ctx, operationLeaseKey{}, lease), &leased)
	if lease.Ended() {
		base := *caller
		base.policies = caller.policies.Base()
		return run(withoutOperationLease(ctx), &base)
	}
	if lease.Used() && caller.executor != nil && caller.executor.operationWrites != nil {
		caller.executor.operationWrites.record(lease, func(ctx context.Context) error {
			return probeCallerWrite(ctx, caller, run)
		})
	}
	return result, err
}

func probeCallerWrite[P, A, R any](ctx context.Context, caller *Caller[P, A], run func(context.Context, *Caller[P, A]) (R, error)) error {
	transaction, err := caller.app.database.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	binding := transactionExecution(caller.app.database, transaction)
	defer binding.close()
	if err := binding.enableMutation(mutationConfig(caller.app, caller.executor)); err != nil {
		_ = transaction.Rollback()
		return err
	}
	base := *caller
	base.policies = caller.policies.Base()
	base.executor = binding
	_, runErr := run(withoutOperationLease(ctx), &base)
	binding.discardMutation()
	_ = transaction.Rollback()
	return runErr
}

type operationWriteLog struct {
	mu     sync.Mutex
	leases []*policyruntime.Lease
	probes []func(context.Context) error
}

func (log *operationWriteLog) record(lease *policyruntime.Lease, probe func(context.Context) error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.leases = append(log.leases, lease)
	log.probes = append(log.probes, probe)
}

func (log *operationWriteLog) commit(commit func() error) error {
	if log == nil {
		return commit()
	}
	log.mu.Lock()
	leases := append([]*policyruntime.Lease(nil), log.leases...)
	log.mu.Unlock()
	return policyruntime.CommitLeases(leases, commit)
}

func (log *operationWriteLog) refusal(ctx context.Context) error {
	log.mu.Lock()
	probes := append([]func(context.Context) error(nil), log.probes...)
	log.mu.Unlock()
	for _, probe := range probes {
		if err := probe(ctx); err != nil {
			return err
		}
	}
	return golem.RuntimeOperationError(golem.CodeConflict, "transaction", golem.ModelID{}, golem.FieldID{}, "transaction could not be committed", nil)
}
