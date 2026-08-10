package nested

import (
	"context"
	"fmt"
)

// BoundaryFunc adapts a runtime-owned transaction opener without coupling the
// provider-neutral nested engine to sqlx or to one provider's begin primitive.
type BoundaryFunc func(context.Context) (ExecutionTransaction, error)

func (open BoundaryFunc) BeginNested(ctx context.Context) (ExecutionTransaction, error) {
	if open == nil {
		return nil, fmt.Errorf("P4_NESTED_EXEC_ADAPTER: transaction opener is absent")
	}
	return open(ctx)
}

// TransactionAdapter is the explicit integration seam for runtime. Its
// callbacks normally delegate to:
//   - relation expansion/locking SQL for Expand;
//   - the P4-D scalar kernel for single-row Apply calls;
//   - the bounded batch kernel for updateMany/deleteMany;
//   - the upsert kernel for branch selection; and
//   - transaction-local hook/result verification for Verify.
//
// Commit and Rollback remain runtime-owned so the nested package cannot escape
// or replace a caller-supplied transaction.
type TransactionAdapter struct {
	Expand   func(context.Context, ExpansionRequest) (RuntimeExpansion, error)
	Apply    func(context.Context, ApplyRequest) (ApplyResult, error)
	Verify   func(context.Context, AppliedNode) error
	Commit   func(context.Context) error
	Rollback func(context.Context) error
}

func (adapter TransactionAdapter) ExpandNested(ctx context.Context, request ExpansionRequest) (RuntimeExpansion, error) {
	if adapter.Expand == nil {
		return RuntimeExpansion{}, fmt.Errorf("P4_NESTED_EXEC_ADAPTER: expansion callback is absent")
	}
	return adapter.Expand(ctx, request)
}

func (adapter TransactionAdapter) ApplyNested(ctx context.Context, request ApplyRequest) (ApplyResult, error) {
	if adapter.Apply == nil {
		return ApplyResult{}, fmt.Errorf("P4_NESTED_EXEC_ADAPTER: apply callback is absent")
	}
	return adapter.Apply(ctx, request)
}

func (adapter TransactionAdapter) VerifyNested(ctx context.Context, applied AppliedNode) error {
	if adapter.Verify == nil {
		return fmt.Errorf("P4_NESTED_EXEC_ADAPTER: verification callback is absent")
	}
	return adapter.Verify(ctx, applied)
}

func (adapter TransactionAdapter) CommitNested(ctx context.Context) error {
	if adapter.Commit == nil {
		return fmt.Errorf("P4_NESTED_EXEC_ADAPTER: commit callback is absent")
	}
	return adapter.Commit(ctx)
}

func (adapter TransactionAdapter) RollbackNested(ctx context.Context) error {
	if adapter.Rollback == nil {
		return fmt.Errorf("P4_NESTED_EXEC_ADAPTER: rollback callback is absent")
	}
	return adapter.Rollback(ctx)
}
