package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestPostgreSQLUpgradingAReferencedParentLockNeverDeadlocks(t *testing.T) {
	runRelationDeleteProviderProfiles(t, "lock_upgrade", func(t testing.TB) schematest.Fixture {
		return schematest.NewSubscribedIndexedOptionalSourceOnDelete(t, compilerir.ActionCascade)
	}, schematest.NewSubscribedIndexedOptionalSourceOnDeletePostgreSQLNamespaces(compilerir.ActionCascade), func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		if profile.provider != golem.PostgreSQL {
			t.Skip("row-lock order is a PostgreSQL property; SQLite serializes writers")
		}
		ctx, fixture := context.Background(), profile.fixture
		caller := mustMutationResultCaller(t, fixture)
		marker := lockOrderMarker(profile.posts)
		rename := func(name string) golem.UpdateInput[mutationResultUser] {
			return golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User, golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, name))
		}
		created := make(chan struct{})
		var firstDone atomic.Bool
		first := make(chan lockOrderOutcome, 1)
		second := make(chan lockOrderOutcome, 1)
		go func() {
			err := CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
				if _, err := CallerTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(30, golem.UUID{15: 2}, "first child")); err != nil {
					return err
				}
				go func() {
					err := CallerTransaction(ctx, caller, func(other *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
						if _, err := CallerTxCreate(ctx, other, fixture.postDescriptor, fixture.createPost(31, golem.UUID{15: 2}, "second child")); err != nil {
							return err
						}
						close(created)
						deadline := time.Now().Add(15 * time.Second)
						for lockWaiters(t, fixture.app.database, marker) < 1 && !firstDone.Load() {
							if time.Now().After(deadline) {
								return errors.New("the first transaction neither waited nor finished")
							}
							time.Sleep(5 * time.Millisecond)
						}
						_, err := CallerTxUpdate(ctx, other, fixture.userDescriptor, cascadeUserTarget(fixture, 2), rename("second"))
						return err
					})
					second <- lockOrderOutcome{name: "second transaction", err: err}
				}()
				select {
				case <-created:
				case <-time.After(15 * time.Second):
					return errors.New("the second transaction could not reference the parent while the first held it")
				}
				_, err := CallerTxUpdate(ctx, transaction, fixture.userDescriptor, cascadeUserTarget(fixture, 2), rename("first"))
				return err
			})
			firstDone.Store(true)
			first <- lockOrderOutcome{name: "first transaction", err: err}
		}()
		var outcomes []lockOrderOutcome
		for _, results := range []chan lockOrderOutcome{first, second} {
			select {
			case outcome := <-results:
				outcomes = append(outcomes, outcome)
			case <-time.After(30 * time.Second):
				t.Fatal("the transactions did not finish")
			}
		}
		t.Logf("outcomes=%v", outcomes)
		assertSerializedOrOneConflict(t, outcomes, nil)
	})
}
