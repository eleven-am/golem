package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestCallerMutationExecutionInvokesFrozenP4RowAndBatchPaths(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewCallerMutationExecution(caller,
		CallerMutationModel[mutationResultPrincipal, mutationResultActor](fixture.postDescriptor),
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := golem.FreezeFindMany(fixture.postDescriptor, golem.Select[mutationResultPost](fixture.postID, fixture.title))
	if err != nil {
		t.Fatal(err)
	}
	create, err := golem.RuntimeFreezeCreateInput(fixture.createPost(70, golem.UUID{15: 1}, "graphql-create"))
	if err != nil {
		t.Fatal(err)
	}
	createRequest, err := golem.RuntimeFreezeMutationRequest(golem.RuntimeMutationRequestInput{
		Operation: golem.RuntimeMutationCreate, Model: fixture.schema.Post, Input: &create, Projection: &projection,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := adapter.ExecuteFrozenMutation(ctx, createRequest)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := created.Row()
	if !ok {
		t.Fatal("create returned no runtime row")
	}
	if value, present := golem.RuntimeTransportField(row, fixture.schema.PostTitle).Get(); !present || value != "graphql-create" {
		t.Fatalf("created title = %#v/%v", value, present)
	}

	predicate, err := fixture.postID.Eq(golem.UUID{15: 70}).Freeze(fixture.postDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	update := fixture.updateManyTitle("graphql-batch")
	frozenUpdate, err := golem.RuntimeFreezeUpdateManyInput(update)
	if err != nil {
		t.Fatal(err)
	}
	batchRequest, err := golem.RuntimeFreezeMutationRequest(golem.RuntimeMutationRequestInput{
		Operation: golem.RuntimeMutationUpdateMany, Model: fixture.schema.Post, Where: &predicate, Input: &frozenUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := adapter.ExecuteFrozenMutation(ctx, batchRequest)
	if err != nil {
		t.Fatal(err)
	}
	if count, ok := batch.Count(); !ok || count != 1 {
		t.Fatalf("batch count = %d/%v", count, ok)
	}
	assertMutationResultTitleCount(t, fixture, "graphql-batch", 1)
}

func TestCallerMutationExecutionRefusesUnknownModelWithoutP4Work(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := NewCallerMutationExecution(caller,
		CallerMutationModel[mutationResultPrincipal, mutationResultActor](fixture.postDescriptor),
	)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := golem.FreezeFindMany(fixture.userDescriptor, golem.Select[mutationResultUser](fixture.userID))
	if err != nil {
		t.Fatal(err)
	}
	// A structurally valid request for the generated User model is refused by
	// the adapter inventory because only Post was registered.
	userCreate := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User)
	frozenUser, err := golem.RuntimeFreezeCreateInput(userCreate)
	if err != nil {
		t.Fatal(err)
	}
	request, err := golem.RuntimeFreezeMutationRequest(golem.RuntimeMutationRequestInput{
		Operation: golem.RuntimeMutationCreate, Model: fixture.schema.User, Input: &frozenUser, Projection: &projection,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ExecuteFrozenMutation(ctx, request); err == nil {
		t.Fatal("unregistered model was executed")
	}
	var count int
	if err := fixture.app.database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "users"`); err != nil || count != 2 {
		t.Fatalf("refused adapter changed rows: count=%d err=%v", count, err)
	}
}
