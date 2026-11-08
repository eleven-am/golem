package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/queue"
)

type wakeCountingPublisher struct{ wakes atomic.Int64 }

func (*wakeCountingPublisher) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

func (publisher *wakeCountingPublisher) Wake() { publisher.wakes.Add(1) }

func TestCommittedFactsWakeTheEventPublisherOncePerCommit(t *testing.T) {
	forEachHookEnqueueProvider(t, false, queue.DefaultLimits(), func(t *testing.T, fixture *hookEnqueueFixture) {
		ctx := context.Background()
		publisher := &wakeCountingPublisher{}
		fixture.app.eventPublisher = publisher
		requireWakes := func(label string, want int64) {
			t.Helper()
			if got := publisher.wakes.Load(); got != want {
				t.Fatalf("%s: publisher wakes=%d want=%d", label, got, want)
			}
		}
		author := golem.UUID{15: 1}
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, systemAuthorPost(fixture.mutationVocabularyFixture, 10, &author, "single")); err != nil {
			t.Fatal(errorChain(err))
		}
		requireWakes("single-row create", 1)

		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		if _, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 10}), fixture.updateManyTitle("batched")); err != nil {
			t.Fatal(errorChain(err))
		}
		requireWakes("batch update", 2)

		fixture.hook = func(hookContext context.Context, executor golem.HookExecutor) error {
			hookCreatePost(t, fixture, hookContext, executor)
			if got := publisher.wakes.Load(); got != 2 {
				t.Errorf("publisher woke inside the hook's transaction: wakes=%d", got)
			}
			return nil
		}
		if err := fixture.trigger(t); err != nil {
			t.Fatal(errorChain(err))
		}
		requireWakes("update whose hook writes", 3)

		err := SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
			_, createErr := SystemTxCreate(ctx, transaction, fixture.postDescriptor, systemAuthorPost(fixture.mutationVocabularyFixture, 11, &author, "transaction"))
			return createErr
		})
		if err != nil {
			t.Fatal(errorChain(err))
		}
		requireWakes("system transaction", 4)

		refusal := errors.New("batch hook refused")
		fixture.hook = func(context.Context, golem.HookExecutor) error { return refusal }
		if _, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 10}), fixture.updateManyTitle("refused")); !errors.Is(err, refusal) {
			t.Fatalf("refused batch reported %v", err)
		}
		requireWakes("rolled-back batch", 4)

		err = SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, createErr := SystemTxCreate(ctx, transaction, fixture.postDescriptor, systemAuthorPost(fixture.mutationVocabularyFixture, 12, &author, "rolled back")); createErr != nil {
				return createErr
			}
			return refusal
		})
		if !errors.Is(err, refusal) {
			t.Fatalf("rolled-back transaction reported %v", err)
		}
		requireWakes("rolled-back transaction", 4)
	})
}
