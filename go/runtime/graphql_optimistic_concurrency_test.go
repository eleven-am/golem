package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestOptimisticConcurrencyCallerGraphQLRuntimeDispatchUsesClosedClaims(t *testing.T) {
	ctx := context.Background()
	schema := schematest.NewOptimisticConcurrency(t)
	provider := sqlite.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "graphql-optimistic-concurrency.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, schema.SQLite); err != nil {
		t.Fatal(err)
	}
	fixture := openMutationVocabularyFixture(t, database, golem.SQLite, schema)
	user := golem.GeneratedCreateInput[mutationResultUser](schema.User,
		golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 1}),
		golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "alice"),
	)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, user); err != nil {
		t.Fatal(err)
	}
	decimal, err := golem.ParseDecimal("1.25")
	if err != nil {
		t.Fatal(err)
	}
	decimalField := golem.GeneratedEqualField[mutationResultPost, golem.Decimal](schema.PostDecimal)
	createPost := func(id byte, title string) golem.CreateInput[mutationResultPost] {
		return golem.GeneratedCreateInput(schema.Post,
			golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(schema.Post, fixture.authorID, golem.UUID{15: 1}),
			golem.GeneratedCreateFieldValue(schema.Post, fixture.title, title),
			golem.GeneratedCreateFieldValue(schema.Post, decimalField, decimal),
		)
	}
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, createPost(1, "before")); err != nil {
		t.Fatal(err)
	}
	caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
	execution, err := NewCallerMutationExecution(caller, CallerMutationModel[mutationResultPrincipal, mutationResultActor](fixture.postDescriptor))
	if err != nil {
		t.Fatal(err)
	}
	projection, err := golem.FreezeFindMany(fixture.postDescriptor, golem.Select[mutationResultPost](fixture.title))
	if err != nil {
		t.Fatal(err)
	}
	target, err := golem.RuntimeFreezeMutationTarget(fixture.target(1))
	if err != nil {
		t.Fatal(err)
	}
	update, err := golem.RuntimeFreezeUpdateInput(fixture.updateTitle("after"))
	if err != nil {
		t.Fatal(err)
	}
	expected := golem.ExpectVersion(1)
	request, err := golem.RuntimeFreezeVersionedMutationRequest(golem.RuntimeVersionedMutationRequestInput{
		Request: golem.RuntimeMutationRequestInput{Operation: golem.RuntimeMutationUpdate, Model: schema.Post, Target: &target, Input: &update, Projection: &projection}, ExistingVersion: &expected,
	})
	if err != nil {
		t.Fatal(err)
	}
	expected = golem.ExpectVersion(99)
	frozenExpected, present := request.ExistingVersion()
	if !present || frozenExpected != golem.ExpectVersion(1) {
		t.Fatalf("frozen request claim followed source mutation: %#v/%v", frozenExpected, present)
	}
	if _, err := execution.ExecuteFrozenMutation(ctx, request); err != nil {
		t.Fatal(err)
	}
	assertOptimisticConcurrencyRow(t, database, 1, "after", 2)
	_, err = execution.ExecuteFrozenMutation(ctx, request)
	assertOptimisticConcurrencyError(t, err, golem.CodeConflict, "mutation conflicted")
	assertOptimisticConcurrencyRow(t, database, 1, "after", 2)

	create, err := golem.RuntimeFreezeCreateInput(createPost(2, "created"))
	if err != nil {
		t.Fatal(err)
	}
	unusedUpdate, err := golem.RuntimeFreezeUpdateInput(fixture.updateTitle("unused"))
	if err != nil {
		t.Fatal(err)
	}
	upsertTarget, err := golem.RuntimeFreezeMutationTarget(fixture.target(2))
	if err != nil {
		t.Fatal(err)
	}
	absent := golem.ExpectAbsent()
	upsert, err := golem.RuntimeFreezeVersionedMutationRequest(golem.RuntimeVersionedMutationRequestInput{
		Request: golem.RuntimeMutationRequestInput{Operation: golem.RuntimeMutationUpsert, Model: schema.Post, Target: &upsertTarget, Create: &create, Update: &unusedUpdate, Projection: &projection}, ConcurrencyExpectation: &absent,
	})
	if err != nil {
		t.Fatal(err)
	}
	absent = golem.ExpectExisting(99)
	frozenExpectation, present := upsert.ConcurrencyExpectation()
	if !present || frozenExpectation != golem.ExpectAbsent() {
		t.Fatalf("frozen upsert expectation followed source mutation: %#v/%v", frozenExpectation, present)
	}
	if _, err := execution.ExecuteFrozenMutation(ctx, upsert); err != nil {
		t.Fatal(err)
	}
	assertOptimisticConcurrencyRow(t, database, 2, "created", 1)
}

func TestNonVersionedGraphQLRuntimeRejectsForgedConcurrencyClaim(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := NewCallerMutationExecution(caller, CallerMutationModel[mutationResultPrincipal, mutationResultActor](fixture.postDescriptor))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, fixture.createPost(70, golem.UUID{15: 1}, "claim-before")); err != nil {
		t.Fatal(err)
	}
	projection, err := golem.FreezeFindMany(fixture.postDescriptor, golem.Select[mutationResultPost](fixture.title))
	if err != nil {
		t.Fatal(err)
	}
	target, err := golem.RuntimeFreezeMutationTarget(fixture.target(70))
	if err != nil {
		t.Fatal(err)
	}
	update, err := golem.RuntimeFreezeUpdateInput(fixture.updateTitle("forged-claim"))
	if err != nil {
		t.Fatal(err)
	}
	claim := golem.ExpectVersion(1)
	request, err := golem.RuntimeFreezeVersionedMutationRequest(golem.RuntimeVersionedMutationRequestInput{
		Request: golem.RuntimeMutationRequestInput{Operation: golem.RuntimeMutationUpdate, Model: fixture.schema.Post, Target: &target, Input: &update, Projection: &projection}, ExistingVersion: &claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.ExecuteFrozenMutation(ctx, request); err == nil {
		t.Fatal("non-versioned model accepted a forged concurrency claim")
	}
	assertMutationResultTitleCount(t, fixture, "forged-claim", 0)
	assertMutationResultTitleCount(t, fixture, "claim-before", 1)
}
