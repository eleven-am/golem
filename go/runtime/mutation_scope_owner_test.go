package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
)

func scopeOwnerState(t *testing.T) (*mutationState, context.Context, context.Context) {
	t.Helper()
	limits, err := normalizeMutationLimits(MutationLimits{MaxTouchedRows: 10, MaxFacts: 10, MaxOutboxBytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	state, err := newMutationState(limits, mutationfact.CausationID{9})
	if err != nil {
		t.Fatal(err)
	}
	binding := &executionBinding{scoped: true}
	state.binding = binding
	parent := &heldWrite{binding: binding}
	child := &heldWrite{scope: parent, binding: binding, owner: parent}
	return state, context.WithValue(context.Background(), heldWriteKey{}, parent), context.WithValue(context.Background(), heldWriteKey{}, child)
}

func TestMutationScopeRollbackKeepsWorkAnotherScopeAppendedMeanwhile(t *testing.T) {
	state, parentCtx, childCtx := scopeOwnerState(t)
	model := golem.ModelID{7}
	errChild := errors.New("child failed")
	scope, err := state.beginScope(childCtx)
	if err != nil {
		t.Fatal(err)
	}
	steps := []error{
		state.touch(childCtx, 2),
		state.markSemantic(childCtx, model, "shared", []any{"shared"}),
		state.markSemantic(childCtx, model, "child", []any{"child"}),
		state.addAfterCommit(childCtx, golem.HookCreate, model, func(context.Context) error { return nil }),
		state.touch(parentCtx, 1),
		state.appendOutboxRow(parentCtx, runtimeStateRow(state, 1, []byte{1, 2})),
		state.markSemantic(parentCtx, model, "shared", []any{"shared"}),
		state.addAfterCommit(parentCtx, golem.HookUpdate, model, func(context.Context) error { return nil }),
	}
	for index, err := range steps {
		if err != nil {
			t.Fatalf("step %d: %v", index, err)
		}
	}
	state.poison(childCtx, errChild)
	if err := scope.rollback(); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.touched != 1 {
		t.Fatalf("touched=%d, want only the parent's row", state.touched)
	}
	if len(state.facts) != 1 || state.ordinal != 1 || state.bytes != state.facts[0].EncodedBytes() {
		t.Fatalf("facts=%d ordinal=%d bytes=%d, want the parent's fact", len(state.facts), state.ordinal, state.bytes)
	}
	if len(state.marks) != 1 || state.marks[0].key != "shared" {
		t.Fatalf("marks=%v, want the mark the parent also requested", state.marks)
	}
	if len(state.after) != 1 || state.after[0].operation != golem.HookUpdate {
		t.Fatalf("after-commit work=%d, want the parent's", len(state.after))
	}
	if state.failure != nil {
		t.Fatalf("failure=%v survived its scope's rollback", state.failure)
	}
	if !state.dirty {
		t.Fatal("the parent's write no longer marks the transaction dirty")
	}
}

func TestMutationScopeRollbackKeepsAFailureRecordedOutsideIt(t *testing.T) {
	state, parentCtx, childCtx := scopeOwnerState(t)
	errChild, errParent := errors.New("child failed"), errors.New("parent failed")
	scope, err := state.beginScope(childCtx)
	if err != nil {
		t.Fatal(err)
	}
	state.poison(childCtx, errChild)
	state.poison(parentCtx, errParent)
	if err := scope.rollback(); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !errors.Is(state.failure, errParent) {
		t.Fatalf("failure=%v, want the parent's failure kept", state.failure)
	}
}

func TestMutationScopeRollbackNeverLeavesACommittableOrdinalGap(t *testing.T) {
	state, parentCtx, childCtx := scopeOwnerState(t)
	scope, err := state.beginScope(childCtx)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.appendOutboxRow(childCtx, runtimeStateRow(state, 1, []byte{1})); err != nil {
		t.Fatal(err)
	}
	if err := state.appendOutboxRow(parentCtx, runtimeStateRow(state, 2, []byte{2})); err != nil {
		t.Fatal(err)
	}
	if err := scope.rollback(); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.facts) != 1 || string(state.facts[0].Metadata) != string([]byte{2}) {
		t.Fatalf("facts=%v, want only the parent's", state.facts)
	}
	if state.facts[0].TransactionOrdinal != 1 && state.failure == nil {
		t.Fatalf("the parent's fact kept ordinal %d and the transaction can still commit", state.facts[0].TransactionOrdinal)
	}
}

func TestMutationScopesCloseOnlyInOrder(t *testing.T) {
	state, _, childCtx := scopeOwnerState(t)
	outer, err := state.beginScope(childCtx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.beginScope(childCtx); err != nil {
		t.Fatal(err)
	}
	if err := outer.release(); err == nil {
		t.Fatal("an outer scope released while an inner one was open")
	}
}
