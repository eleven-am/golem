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

func operationConflict(model golem.ModelID, operation, message string) error {
	return golem.RuntimeOperationError(golem.CodeConflict, operation, model, golem.FieldID{}, message, nil)
}

func callerWrite[P, A, R any](ctx context.Context, caller *Caller[P, A], conflict error, run func(context.Context, *Caller[P, A]) (R, error)) (R, error) {
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
		var zero R
		return zero, conflict
	}
	if lease.Used() && caller.executor != nil && caller.executor.operationWrites != nil {
		caller.executor.operationWrites.record(lease, conflict)
	}
	return result, err
}

type operationWriteLog struct {
	mu        sync.Mutex
	leases    []*policyruntime.Lease
	conflicts []error
}

func (log *operationWriteLog) record(lease *policyruntime.Lease, conflict error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.leases = append(log.leases, lease)
	log.conflicts = append(log.conflicts, conflict)
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

func (log *operationWriteLog) refusal() error {
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.conflicts[0]
}
