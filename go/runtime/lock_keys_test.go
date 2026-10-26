package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func requireLockKeyCode(t testing.TB, err error, code golem.ErrorCode) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("err=%v; want %s", err, code)
	}
}

func TestSQLiteLockKeysValidateAndTakeNoLock(t *testing.T) {
	fixture := newSocialMutationFixture(t, golem.ModelID{}, nil)
	ctx := context.Background()
	key := golem.MustLockKey("sqlite", "budget")
	if err := SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[graphMutationPrincipal, graphMutationActor]) error {
		if err := SystemTxLockKeys(ctx, transaction, key, key); err != nil {
			return err
		}
		requireLockKeyCode(t, SystemTxLockKeys(ctx, transaction, key, golem.LockKey{}), golem.CodeBadUserInput)
		if err := SystemTxLockKeys(ctx, transaction); err != nil {
			return err
		}
		_, err := SystemTxCreate(ctx, transaction, fixture.userDescriptor, fixture.userCreate(1, "after-lock"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if err := CallerTransaction(ctx, caller, func(transaction *CallerTx[graphMutationPrincipal, graphMutationActor]) error {
		if err := CallerTxLockKeys(ctx, transaction, key); err != nil {
			return err
		}
		requireLockKeyCode(t, CallerTxLockKeys(ctx, transaction, golem.LockKey{}), golem.CodeBadUserInput)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := socialCount(t, fixture, fixture.schema.User); got != 1 {
		t.Fatalf("users=%d; want the write after the lock committed", got)
	}
}

func TestLockKeysOutsideATransactionAreRefused(t *testing.T) {
	ctx := context.Background()
	key := golem.MustLockKey("detached")
	if err := CallerTxLockKeys(ctx, (*CallerTx[graphMutationPrincipal, graphMutationActor])(nil), key); err == nil {
		t.Fatal("a nil caller transaction took a lock key")
	}
	if err := SystemTxLockKeys(ctx, (*SystemTx[graphMutationPrincipal, graphMutationActor])(nil), key); err == nil {
		t.Fatal("a nil system transaction took a lock key")
	}
	fixture := newSocialMutationFixture(t, golem.ModelID{}, nil)
	var escaped *SystemTx[graphMutationPrincipal, graphMutationActor]
	if err := SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[graphMutationPrincipal, graphMutationActor]) error {
		escaped = transaction
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := SystemTxLockKeys(ctx, escaped, key); err == nil {
		t.Fatal("a transaction handle used after its callback returned took a lock key")
	}
}
