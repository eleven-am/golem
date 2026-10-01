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

func withinOperationAttempt[P, A, R any](ctx context.Context, caller *Caller[P, A], run func(context.Context, *Caller[P, A]) (R, error)) (R, error) {
	lease := caller.policies.CurrentLease()
	if lease == nil {
		return run(ctx, caller)
	}
	attempt := lease.Attempt()
	scoped := *caller
	scoped.policies = caller.policies.WithLease(attempt)
	result, err := run(context.WithValue(ctx, operationLeaseKey{}, attempt), &scoped)
	attempt.Finish(err)
	return result, err
}

func commitWithinOperation(ctx context.Context, commit func() error) error {
	return operationLeaseFrom(ctx).Commit(commit)
}

func operationConflict(model golem.ModelID, operation, message string) error {
	return golem.RuntimeOperationError(golem.CodeConflict, operation, model, golem.FieldID{}, message, nil)
}

func callerWrite[P, A, R any](ctx context.Context, caller *Caller[P, A], conflict error, run func(context.Context, *Caller[P, A]) (R, error)) (R, error) {
	if caller == nil {
		return run(ctx, caller)
	}
	ctx, endCall, callErr := caller.executor.beginCall(ctx)
	if callErr != nil {
		var zero R
		return zero, callErr
	}
	defer endCall()
	if ctx == nil || caller.policies == nil {
		return run(ctx, caller)
	}
	if existing := operationLeaseFrom(ctx); existing != nil {
		if caller.policies.WithLease(existing) != caller.policies {
			attempt := existing.Attempt()
			leased := *caller
			leased.policies = caller.policies.WithLease(attempt)
			result, err := run(context.WithValue(ctx, operationLeaseKey{}, attempt), &leased)
			attempt.Finish(err)
			if attempt.Ended() {
				var zero R
				return zero, conflict
			}
			return result, err
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
	if err == nil && caller.executor != nil && caller.executor.operationWrites != nil {
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
	for index, lease := range log.leases {
		if lease.Used() && lease.OperationEnded() {
			return log.conflicts[index]
		}
	}
	return policyruntime.ErrOperationEnded
}
