package runtime

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

type atomicityConnection struct {
	commitErr error
	closeErr  error
	commits   int
	closes    int
}

func (connection *atomicityConnection) ExecContext(_ context.Context, statement string, _ ...any) (sql.Result, error) {
	if strings.TrimSpace(statement) == "COMMIT" {
		connection.commits++
		return nil, connection.commitErr
	}
	return nil, nil
}

func (connection *atomicityConnection) Close() error {
	connection.closes++
	return connection.closeErr
}

func TestSQLiteCommittedMutationDoesNotReportCloseFailure(t *testing.T) {
	closeFailure := errors.New("injected close failure")
	connection := &atomicityConnection{closeErr: closeFailure}
	committed := 0
	if err := commitSQLiteImmediate(context.Background(), connection, func() { committed++ }); err != nil {
		t.Fatalf("durable COMMIT must remain successful after Close failure: %v", err)
	}
	if connection.commits != 1 || connection.closes != 1 || committed != 1 {
		t.Fatalf("unexpected finalization counts: commits=%d closes=%d callbacks=%d", connection.commits, connection.closes, committed)
	}
}

func TestSQLiteCommitFailureRemainsAnErrorAndDoesNotRunCommittedWork(t *testing.T) {
	commitFailure := errors.New("injected commit failure")
	connection := &atomicityConnection{commitErr: commitFailure}
	committed := 0
	err := commitSQLiteImmediate(context.Background(), connection, func() { committed++ })
	if !errors.Is(err, commitFailure) {
		t.Fatalf("expected injected COMMIT failure, got %v", err)
	}
	if connection.commits != 1 || connection.closes != 0 || committed != 0 {
		t.Fatalf("unexpected failed-finalization counts: commits=%d closes=%d callbacks=%d", connection.commits, connection.closes, committed)
	}
}

type savepointFailpoint struct {
	errors      []error
	contextErrs []error
	statements  []string
}

func (failpoint *savepointFailpoint) ExecContext(ctx context.Context, statement string, _ ...any) (sql.Result, error) {
	failpoint.contextErrs = append(failpoint.contextErrs, ctx.Err())
	failpoint.statements = append(failpoint.statements, statement)
	index := len(failpoint.statements) - 1
	if index < len(failpoint.errors) {
		return nil, failpoint.errors[index]
	}
	return nil, nil
}

func TestNestedSavepointRollbackUsesNonCanceledContext(t *testing.T) {
	limits, err := normalizeMutationLimits(MutationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	state, err := newMutationState(limits, [16]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := state.beginScope()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	executor := &savepointFailpoint{}
	if err := rollbackNestedSavepoint(ctx, executor, `"golem_nested_1"`, scope, state); err != nil {
		t.Fatalf("rollback with detached cancellation failed: %v", err)
	}
	if len(executor.contextErrs) != 2 || executor.contextErrs[0] != nil || executor.contextErrs[1] != nil {
		t.Fatalf("savepoint recovery inherited canceled context: %#v", executor.contextErrs)
	}
}

func TestNestedSavepointRecoveryFailurePoisonsOuterMutation(t *testing.T) {
	limits, err := normalizeMutationLimits(MutationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	state, err := newMutationState(limits, [16]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := state.beginScope()
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected rollback failure")
	executor := &savepointFailpoint{errors: []error{injected, nil}}
	err = rollbackNestedSavepoint(context.Background(), executor, `"golem_nested_2"`, scope, state)
	if !errors.Is(err, injected) {
		t.Fatalf("expected injected savepoint failure, got %v", err)
	}
	state.mu.Lock()
	failure := state.failure
	state.mu.Unlock()
	if !errors.Is(failure, injected) {
		t.Fatalf("outer mutation was not poisoned: %v", failure)
	}
}
