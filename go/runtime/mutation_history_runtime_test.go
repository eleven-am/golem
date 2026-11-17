package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestMutationHistoryRuntimeCapturesBeforeReentrantVerificationWrites(t *testing.T) {
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) }})
	fixture.mutate(t, golem.EventCreated, 51, 1, "before")
	target := golem.GeneratedUniqueSelectorValue[policyFactRuntimePost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 51}))
	input := func(title string) golem.UpdateInput[policyFactRuntimePost] {
		return golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, title))
	}
	var retained *mutationState
	err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		frozen, err := golem.RuntimeFreezeUpdateInput(input("outer"))
		if err != nil {
			return err
		}
		frozenTarget, err := golem.RuntimeFreezeMutationTarget[policyFactRuntimePost](target)
		if err != nil {
			return err
		}
		images, err := mutationir.NewImageRequirements(policyir.ModelID(fixture.schema.Post), nil, nil)
		if err != nil {
			return err
		}
		program, err := prepareSystemScalarProgram(transaction.system, scalarMutationPrepareRequest{operation: mutationir.Update, model: policyir.ModelID(fixture.schema.Post), input: &frozen, target: &frozenTarget, result: images})
		if err != nil {
			return err
		}
		_, err = transactionOperation(fixture.ctx, transaction.binding(), func(ctx context.Context) (scalarMutationExecution, error) {
			verified := func(ctx context.Context, binding *executionBinding, _ mutationsql.Program, _ scalarMutationExecution) error {
				state, err := binding.mutationState()
				if err != nil {
					return err
				}
				retained = state
				history := state.historyImages()
				if len(history) != 1 {
					t.Fatalf("write image was not journaled before verification: %d", len(history))
				}
				mutationHistoryText(t, history[0].before, fixture.schema.PostTitle, "before")
				mutationHistoryText(t, history[0].after, fixture.schema.PostTitle, "outer")
				_, err = SystemTxUpdate(ctx, transaction, fixture.posts, target, input("inner"))
				return err
			}
			return executeScalarMutationProgramWithObservers(ctx, fixture.app.database, fixture.app.provider, fixture.app.registry, policyir.ModelID(fixture.schema.Post), transaction.system.executor, program, mutationConfig(fixture.app, transaction.system.executor), nil, verified)
		})
		if err != nil {
			return err
		}
		history := retained.historyImages()
		if len(history) != 1 {
			t.Fatalf("same-row history was not folded: %d", len(history))
		}
		mutationHistoryText(t, history[0].before, fixture.schema.PostTitle, "before")
		mutationHistoryText(t, history[0].after, fixture.schema.PostTitle, "inner")
		if len(retained.facts) != 2 {
			t.Fatalf("facts=%d want both writes", len(retained.facts))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if retained == nil || len(retained.historyImages()) != 0 || retained.historyBytes != 0 {
		t.Fatal("committed journal retained transaction images")
	}
	rows, err := SystemFindMany(fixture.ctx, fixture.app.System(), fixture.posts, golem.Where(fixture.postID.Eq(golem.UUID{15: 51})), golem.Select[policyFactRuntimePost](fixture.title))
	if err != nil || len(rows) != 1 {
		t.Fatalf("persisted row count=%d err=%v", len(rows), err)
	}
	if value, present := golem.Value(rows[0], fixture.title).Get(); !present || value != "inner" {
		t.Fatalf("persisted title=%q present=%v", value, present)
	}
}
