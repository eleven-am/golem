package upsert

import (
	"context"
	"errors"
	"fmt"

	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
)

// FrozenValues is the one immutable snapshot of all runtime/default/hook
// values that a branch executor may consume. The kernel calls Freeze once,
// before the first transaction, and gives every retry a private copy.
type FrozenValues struct{ encoded []byte }

func NewFrozenValues(encoded []byte) FrozenValues {
	return FrozenValues{encoded: append([]byte(nil), encoded...)}
}
func (values FrozenValues) Bytes() []byte { return append([]byte(nil), values.encoded...) }

type Freeze func(context.Context) (FrozenValues, error)

// Attempt is one transaction or caller-transaction savepoint. Begin must not
// execute SQL: Run guarantees GuardStatement is the first call to Query.
// Finish/Abort finalize only this attempt boundary; a caller transaction must
// therefore implement them as savepoint release/rollback, never outer replay.
type Attempt interface {
	Query(context.Context, Statement) (uint32, error)
	Finish(context.Context) error
	Abort() error
}

type Backend interface {
	Begin(context.Context, mutationsql.TransactionRequirement, uint32) (Attempt, error)
}

type BranchExecutor interface {
	ExecuteBranch(context.Context, Attempt, mutationir.Node, FrozenValues) (any, error)
}

type SelectedBranch uint8

const (
	CreatedBranch SelectedBranch = iota + 1
	UpdatedBranch
)

type Result struct {
	branch  SelectedBranch
	attempt uint32
	value   any
}

func (result Result) Branch() SelectedBranch { return result.branch }
func (result Result) Attempt() uint32        { return result.attempt }
func (result Result) Value() any             { return result.value }

type Options struct {
	// MaxAttempts includes the first attempt. Engine-owned programs require a
	// positive bound. Caller-transaction programs always execute exactly once.
	MaxAttempts uint32
}

// Run freezes values exactly once, then performs guard -> locked update-reach
// probe -> exactly one truthful branch -> SQLite guard cleanup -> finish.
func Run(ctx context.Context, program Program, backend Backend, freeze Freeze, executor BranchExecutor, options Options) (Result, error) {
	if ctx == nil || backend == nil || freeze == nil || executor == nil {
		return Result{}, fail(CodeInput, "context, backend, freeze, and branch executor are required", nil)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, ctxErr
	}
	maxAttempts := options.MaxAttempts
	if program.retry == mutationir.CallerTransactionNoReplay {
		maxAttempts = 1
	} else if program.retry != mutationir.EngineOwnedUpsertRetry || maxAttempts == 0 {
		return Result{}, fail(CodeInput, "engine-owned upsert requires a positive bounded attempt count", nil)
	}
	frozen, err := freeze(ctx)
	if err != nil {
		return Result{}, fail(CodeFreeze, "upsert values could not be frozen", err)
	}
	// Copy immediately so a Freeze implementation cannot mutate the retry input
	// through an aliased backing array after it returns.
	frozen = NewFrozenValues(frozen.Bytes())

	for ordinal := uint32(1); ordinal <= maxAttempts; ordinal++ {
		attempt, beginErr := backend.Begin(ctx, program.transaction, ordinal)
		if beginErr != nil {
			if retry, resultErr := retryDecision(program.retry, ordinal, maxAttempts, "attempt could not begin", beginErr); retry {
				continue
			} else {
				return Result{}, resultErr
			}
		}
		if attempt == nil {
			return Result{}, fail(CodeInvariant, "backend returned a nil attempt", nil)
		}
		result, attemptErr := runAttemptProtected(ctx, program, attempt, frozen, executor, ordinal)
		if attemptErr == nil {
			return result, nil
		}
		abortErr := attempt.Abort()
		if abortErr != nil {
			return Result{}, fail(CodeExecution, "attempt failed and abort also failed", errors.Join(attemptErr, abortErr))
		}
		if retry, resultErr := retryDecision(program.retry, ordinal, maxAttempts, "upsert attempt failed", attemptErr); retry {
			continue
		} else {
			return Result{}, resultErr
		}
	}
	return Result{}, fail(CodeInvariant, "bounded retry loop ended without a result", nil)
}

// runAttemptProtected preserves Go panic semantics while still guaranteeing
// that an engine-owned transaction or caller savepoint is abandoned.
func runAttemptProtected(ctx context.Context, program Program, attempt Attempt, frozen FrozenValues, executor BranchExecutor, ordinal uint32) (result Result, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = attempt.Abort()
			panic(recovered)
		}
	}()
	return runAttempt(ctx, program, attempt, frozen, executor, ordinal)
}

func runAttempt(ctx context.Context, program Program, attempt Attempt, frozen FrozenValues, executor BranchExecutor, ordinal uint32) (Result, error) {
	rows, err := attempt.Query(ctx, program.GuardStatement())
	if err != nil {
		return Result{}, err
	}
	if rows != 1 {
		return Result{}, fail(CodeInvariant, fmt.Sprintf("selector guard returned %d rows, want one", rows), nil)
	}
	rows, err = attempt.Query(ctx, program.ProbeStatement())
	if err != nil {
		return Result{}, err
	}
	if rows > 1 {
		return Result{}, fail(CodeInvariant, fmt.Sprintf("unique update-reach probe returned %d rows", rows), nil)
	}
	branch := CreatedBranch
	node := program.CreateBranch()
	if rows == 1 {
		branch = UpdatedBranch
		node = program.UpdateBranch()
	}
	value, err := executor.ExecuteBranch(ctx, attempt, node, NewFrozenValues(frozen.Bytes()))
	if err != nil {
		return Result{}, err
	}
	if cleanup, present := program.CleanupStatement(); present {
		rows, err = attempt.Query(ctx, cleanup)
		if err != nil {
			return Result{}, err
		}
		if rows != 1 {
			return Result{}, fail(CodeInvariant, fmt.Sprintf("selector guard cleanup returned %d rows, want one", rows), nil)
		}
	}
	if err := attempt.Finish(ctx); err != nil {
		return Result{}, err
	}
	return Result{branch: branch, attempt: ordinal, value: value}, nil
}

func retryDecision(class mutationir.RetryClass, ordinal, maxAttempts uint32, detail string, cause error) (bool, error) {
	if !retryableInterference(cause) {
		var failure *Error
		if errors.As(cause, &failure) {
			return false, cause
		}
		return false, fail(CodeExecution, detail, cause)
	}
	if class == mutationir.EngineOwnedUpsertRetry && ordinal < maxAttempts {
		return true, nil
	}
	return false, fail(CodeConflict, "concurrent upsert interference", cause)
}

func retryableInterference(err error) bool {
	return !untrustedRetryCause(err) && ClassifyProviderFault(err) != ProviderFaultNone
}

// RetryableInterference is the shared provider classification used by nested
// upsert/connect-or-create whole-attempt retry. It exposes only the decision;
// retry ownership and bounds remain with the calling mutation kernel.
func RetryableInterference(err error) bool { return retryableInterference(err) }

// UniqueCollision identifies only trusted provider unique/primary-key
// collisions. Expectation-aware absent upsert uses it after rolling back its
// create savepoint to reclassify the winner without an existence oracle.
func UniqueCollision(err error) bool {
	return !untrustedRetryCause(err) && ClassifyProviderFault(err) == ProviderFaultUniqueCollision
}
