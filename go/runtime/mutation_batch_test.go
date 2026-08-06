package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestPublicBatchMutationCallerAndSystemExecuteExactSet(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id    byte
		title string
	}{{41, "caller-batch"}, {42, "caller-batch"}, {43, "system-batch"}} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(row.id, golem.UUID{15: 1}, row.title)); err != nil {
			t.Fatal(err)
		}
	}

	callerSet := fixture.postID.In(golem.UUID{15: 41}, golem.UUID{15: 42})
	count, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, callerSet, fixture.updateManyTitle("caller-updated"))
	if err != nil || count != 2 {
		t.Fatalf("caller updateMany count=%d err=%v cause=%v", count, err, errors.Unwrap(err))
	}
	count, err = CallerDeleteMany(ctx, caller, fixture.postDescriptor, callerSet)
	if err != nil || count != 2 {
		t.Fatalf("caller deleteMany count=%d err=%v cause=%v", count, err, errors.Unwrap(err))
	}
	count, err = SystemUpdateMany(ctx, fixture.app.System(), fixture.postDescriptor, fixture.title.Eq("system-batch"), fixture.updateManyTitle("system-updated"))
	if err != nil || count != 1 {
		t.Fatalf("system updateMany count=%d err=%v cause=%v", count, err, errors.Unwrap(err))
	}
	count, err = SystemDeleteMany(ctx, fixture.app.System(), fixture.postDescriptor, fixture.title.Eq("system-updated"))
	if err != nil || count != 1 {
		t.Fatalf("system deleteMany count=%d err=%v cause=%v", count, err, errors.Unwrap(err))
	}
}

func TestBatchMutationExactBoundaryAndOverflowWithoutTruncation(t *testing.T) {
	forEachMutationResultProvider(t, MutationLimits{MaxTouchedRows: 2}, assertBatchMutationExactBoundary)
}

func assertBatchMutationExactBoundary(t testing.TB, fixture mutationResultFixture) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []byte{49, 50} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "boundary")); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := SystemUpdateMany(ctx, fixture.app.System(), fixture.postDescriptor, fixture.title.Eq("boundary"), fixture.updateManyTitle("boundary-written")); err != nil || count != 2 {
		t.Fatalf("exact boundary count=%d err=%v", count, err)
	}
	assertMutationResultTitleCount(t, fixture, "boundary-written", 2)
	for _, id := range []byte{51, 52, 53} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "overflow")); err != nil {
			t.Fatal(err)
		}
	}
	count, err := SystemUpdateMany(ctx, fixture.app.System(), fixture.postDescriptor, fixture.title.Eq("overflow"), fixture.updateManyTitle("written"))
	var failure *golem.Error
	if count != 0 || !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
		t.Fatalf("overflow count=%d failure=%#v err=%v cause=%v", count, failure, err, errors.Unwrap(err))
	}
	assertMutationResultTitleCount(t, fixture, "overflow", 3)
	assertMutationResultTitleCount(t, fixture, "written", 0)
}

func TestBatchInvalidationEpochAdvancesOnCommitNotOuterRollback(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(58, golem.UUID{15: 1}, "batch-epoch")); err != nil {
		t.Fatal(err)
	}
	system := fixture.app.System()
	if count, err := SystemUpdateMany(ctx, system, fixture.postDescriptor, fixture.title.Eq("batch-epoch"), fixture.updateManyTitle("batch-committed")); err != nil || count != 1 {
		t.Fatalf("batch commit count=%d err=%v", count, err)
	}
	if system.executor.invalidationEpoch() != 1 {
		t.Fatalf("batch commit epoch=%d", system.executor.invalidationEpoch())
	}
	sentinel := errors.New("rollback batch epoch")
	err := SystemTransaction(ctx, system, func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
		count, err := SystemTxUpdateMany(ctx, transaction, fixture.postDescriptor, fixture.title.Eq("batch-committed"), fixture.updateManyTitle("batch-rolled-back"))
		if err != nil || count != 1 {
			return errors.Join(err, fmt.Errorf("batch rollback count=%d", count))
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("outer rollback err=%v", err)
	}
	if system.executor.invalidationEpoch() != 1 {
		t.Fatalf("batch rollback changed epoch=%d", system.executor.invalidationEpoch())
	}
}

func TestBatchMutationEmitsOneOrderedFactPerAffectedRow(t *testing.T) {
	forEachMutationResultProvider(t, MutationLimits{}, assertBatchMutationOrderedFacts)
}

func assertBatchMutationOrderedFacts(t testing.TB, fixture mutationResultFixture) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []byte{81, 82, 83} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "fact-order")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	if count, err := SystemUpdateMany(ctx, fixture.app.System(), fixture.postDescriptor, fixture.title.Eq("fact-order"), fixture.updateManyTitle("fact-ordered")); err != nil || count != 3 {
		t.Fatalf("batch count=%d err=%v", count, err)
	}
	rows, err := fixture.app.database.QueryxContext(ctx, `SELECT "action", "causation_id", "transaction_ordinal" FROM `+nestedAcceptanceOutbox(fixture.app)+` ORDER BY "transaction_ordinal"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var causation string
	ordinal := int64(0)
	for rows.Next() {
		var action, currentCausation string
		var currentOrdinal int64
		if err := rows.Scan(&action, &currentCausation, &currentOrdinal); err != nil {
			t.Fatal(err)
		}
		ordinal++
		if action != "updated" || currentOrdinal != ordinal || causation != "" && currentCausation != causation {
			t.Fatalf("fact action=%q causation=%q ordinal=%d want=%d", action, currentCausation, currentOrdinal, ordinal)
		}
		causation = currentCausation
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if ordinal != 3 {
		t.Fatalf("batch fact count=%d", ordinal)
	}
}

func TestPublicBatchMutationJoinsOuterTransactionAndRollsBack(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	for _, id := range []byte{61, 62} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "transactional")); err != nil {
			t.Fatal(err)
		}
	}
	rollback := errors.New("rollback batch")
	err := SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
		count, updateErr := SystemTxUpdateMany(ctx, transaction, fixture.postDescriptor, fixture.title.Eq("transactional"), fixture.updateManyTitle("temporary"))
		if updateErr != nil || count != 2 {
			return errors.Join(updateErr, errors.New("unexpected transaction batch count"))
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("transaction error=%v", err)
	}
	assertMutationResultTitleCount(t, fixture, "transactional", 2)
	assertMutationResultTitleCount(t, fixture, "temporary", 0)
}

func TestBatchMutationCapturesStablePrimaryKeySet(t *testing.T) {
	forEachMutationResultProvider(t, MutationLimits{}, assertBatchMutationStablePrimaryKeySet)
}

func assertBatchMutationStablePrimaryKeySet(t testing.TB, fixture mutationResultFixture) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []byte{71, 72} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "interference")); err != nil {
			t.Fatal(err)
		}
	}
	if err := installBatchInterferenceTrigger(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	count, err := SystemUpdateMany(ctx, fixture.app.System(), fixture.postDescriptor, fixture.title.Eq("interference"), fixture.updateManyTitle("should-rollback"))
	var failure *golem.Error
	if count != 0 || !errors.As(err, &failure) || failure.Code != golem.CodeConflict {
		t.Fatalf("interference count=%d failure=%#v err=%v cause=%v", count, failure, err, errors.Unwrap(err))
	}
	assertMutationResultTitleCount(t, fixture, "interference", 2)
	assertMutationResultTitleCount(t, fixture, "should-rollback", 0)
}

func TestBatchIdentityChangeIsRefusedBeforeWrite(t *testing.T) {
	forEachMutationResultProvider(t, MutationLimits{}, func(t testing.TB, fixture mutationResultFixture) {
		ctx := context.Background()
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(91, golem.UUID{15: 1}, "identity-refusal")); err != nil {
			t.Fatal(err)
		}
		input := golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 92}),
		)
		count, err := SystemUpdateMany(ctx, fixture.app.System(), fixture.postDescriptor, fixture.title.Eq("identity-refusal"), input)
		var public *golem.Error
		if count != 0 || !errors.As(err, &public) || public.Code != golem.CodeBadUserInput {
			t.Fatalf("identity change count=%d public=%#v err=%v", count, public, err)
		}
		assertMutationResultTitleCount(t, fixture, "identity-refusal", 1)
	})
}

func forEachMutationResultProvider(t *testing.T, limits MutationLimits, run func(testing.TB, mutationResultFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		run(t, newMutationResultFixtureWithLimits(t, limits))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			fixture, _ := newMutationResultPostgresFixtureWithLimits(t, context.Background(), profile, limits)
			run(t, fixture)
		})
	}
}

func installBatchInterferenceTrigger(ctx context.Context, fixture mutationResultFixture) error {
	table := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	if fixture.app.provider != policyir.ProviderPostgreSQL {
		_, err := fixture.app.database.ExecContext(ctx, `CREATE TRIGGER "golem_batch_interference" BEFORE UPDATE ON `+table+`
			WHEN OLD."title" = 'interference'
			BEGIN
				DELETE FROM `+table+` WHERE "title" = 'interference' AND "id" <> OLD."id";
			END`)
		return err
	}
	namespace, _ := fixture.app.registry.PhysicalNamespace(golem.PostgreSQL)
	function := `"` + string(namespace) + `"."golem_batch_interference_fn"`
	if _, err := fixture.app.database.ExecContext(ctx, `CREATE FUNCTION `+function+`() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			DELETE FROM `+table+` WHERE "title" = 'interference' AND "id" <> OLD."id";
			RETURN OLD;
		END $$`); err != nil {
		return err
	}
	_, err := fixture.app.database.ExecContext(ctx, `CREATE TRIGGER "golem_batch_interference" BEFORE UPDATE ON `+table+`
		FOR EACH ROW WHEN (OLD."title" = 'interference') EXECUTE FUNCTION `+function+`()`)
	return err
}

func (fixture mutationResultFixture) updateManyTitle(title string) golem.UpdateManyInput[mutationResultPost] {
	return golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, title))
}

func assertMutationResultTitleCount(t testing.TB, fixture mutationResultFixture, title string, want int) {
	t.Helper()
	var count int
	query := fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "title" = ?`)
	if err := fixture.app.database.GetContext(context.Background(), &count, query, title); err != nil || count != want {
		t.Fatalf("title %q count=%d want=%d err=%v", title, count, want, err)
	}
}
