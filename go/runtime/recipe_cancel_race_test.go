package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/queue"
)

func TestRecipeSuccessWriteDoesNotOverwriteAConcurrentCancel(t *testing.T) {
	forEachHookEnqueueProvider(t, false, queue.DefaultLimits(), func(t *testing.T, fixture *hookEnqueueFixture) {
		ctx := context.Background()
		author := golem.UUID{15: 1}
		const id = 77
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, systemAuthorPost(fixture.mutationVocabularyFixture, id, &author, "queued")); err != nil {
			t.Fatal(errorChain(err))
		}
		postgres := fixture.app.provider == policyir.ProviderPostgreSQL
		external := make(chan error, 1)
		var matched int64
		err := SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
			rows, err := SystemTxFindMany(ctx, transaction, fixture.postDescriptor, golem.Where(fixture.postID.Eq(golem.UUID{15: id})), golem.Select[mutationResultPost](fixture.title))
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				t.Fatalf("rows=%d", len(rows))
			}
			if title, _ := golem.Value(rows[0], fixture.title).Get(); title != "queued" {
				return nil
			}
			go func() {
				_, err := SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(id), fixture.updateTitle("canceled"))
				external <- err
			}()
			if postgres {
				if err := <-external; err != nil {
					t.Fatalf("external cancel: %v", errorChain(err))
				}
				external <- nil
			}
			matched, err = SystemTxUpdateMany(ctx, transaction, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: id}).And(fixture.title.Eq("queued")), fixture.updateManyTitle("succeeded"))
			return err
		})
		if err != nil {
			t.Fatal(errorChain(err))
		}
		if err := <-external; err != nil {
			t.Fatalf("external cancel: %v", errorChain(err))
		}
		if postgres && matched != 0 {
			t.Fatalf("guarded write matched %d rows after the cancel committed", matched)
		}
		if final := readPostTitle(t, fixture.mutationResultFixture, id); final != "canceled" {
			t.Fatalf("success write overwrote the concurrent cancel: final=%q", final)
		}
	})
}

func readPostTitle(t *testing.T, fixture mutationResultFixture, id byte) string {
	t.Helper()
	var title string
	query := fixture.app.database.Rebind(`SELECT "title" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id" = ?`)
	if err := fixture.app.database.GetContext(context.Background(), &title, query, mutationResultUUIDText(id)); err != nil {
		t.Fatal(err)
	}
	return title
}
