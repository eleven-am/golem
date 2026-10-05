package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestPostgreSQLTransactionCreatingAChildRacingACascadeDeleteOfItsParentTakesRowLocksInOneOrder(t *testing.T) {
	assertParentLockedInOrder(t, "lock_fk_create", func(ctx context.Context, fixture mutationResultFixture, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		_, err := CallerTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(20, golem.UUID{15: 2}, "new child"))
		return err
	})
}

func TestPostgreSQLTransactionRelinkingRowsRacingACascadeDeleteOfTheirParentTakesRowLocksInOneOrder(t *testing.T) {
	assertParentLockedInOrder(t, "lock_fk_batch", func(ctx context.Context, fixture mutationResultFixture, transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		_, err := CallerTxUpdateMany(ctx, transaction, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 12}),
			golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2})))
		return err
	})
}

func assertParentLockedInOrder(t *testing.T, prefix string, reference func(context.Context, mutationResultFixture, *CallerTx[mutationResultPrincipal, mutationResultActor]) error) {
	runRelationDeleteProviderProfiles(t, prefix, func(t testing.TB) schematest.Fixture {
		return schematest.NewSubscribedIndexedOptionalSourceOnDelete(t, compilerir.ActionCascade)
	}, schematest.NewSubscribedIndexedOptionalSourceOnDeletePostgreSQLNamespaces(compilerir.ActionCascade), func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		if profile.provider != golem.PostgreSQL {
			t.Skip("row-lock order is a PostgreSQL property; SQLite serializes writers")
		}
		ctx, fixture := context.Background(), profile.fixture
		for _, post := range []byte{10, 11} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post, golem.UUID{15: 2}, "child")); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(12, golem.UUID{15: 1}, "other parent")); err != nil {
			t.Fatal(err)
		}
		caller := mustMutationResultCaller(t, fixture)
		marker := lockOrderMarker(profile.posts)
		results := make(chan lockOrderOutcome, 1)
		transactionErr := CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, err := CallerTxUpdate(ctx, transaction, fixture.postDescriptor, fixture.target(10), fixture.updateTitle("touched")); err != nil {
				return err
			}
			go func() {
				_, err := CallerDelete(ctx, caller, fixture.userDescriptor, cascadeUserTarget(fixture, 2))
				results <- lockOrderOutcome{name: "cascade delete of the parent", err: err}
			}()
			deadline := time.Now().Add(15 * time.Second)
			for lockWaiters(t, fixture.app.database, marker)+len(results) < 1 {
				if time.Now().After(deadline) {
					return errors.New("the cascade delete neither waited on a row lock nor finished")
				}
				time.Sleep(5 * time.Millisecond)
			}
			return reference(ctx, fixture, transaction)
		})
		outcomes := []lockOrderOutcome{{name: "transaction touching a child then referencing the parent", err: transactionErr}}
		select {
		case outcome := <-results:
			outcomes = append(outcomes, outcome)
		case <-time.After(30 * time.Second):
			t.Fatal("the cascade delete did not finish")
		}
		t.Logf("outcomes=%v", outcomes)
		assertSerializedOrOneConflict(t, outcomes, nil)
	})
}
