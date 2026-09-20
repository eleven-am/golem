package runtime

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func batchSystemAuthorFixture(t *testing.T, modes []compilerir.FieldMode, author func(schematest.Fixture) golem.UUID) mutationResultFixture {
	t.Helper()
	schemaFixture := schematest.NewWithContractModes(t, schematest.ContractModes{AuthorID: modes})
	var hooks func(schematest.Fixture, golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor]
	if author != nil {
		hooks = func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
			authorField := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID)
			return []golem.HookBinding[mutationResultActor]{
				golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateManyHookRequest[mutationResultPost]](schema.Post, golem.HookUpdateMany, func(_ context.Context, request *golem.UpdateManyHookRequest[mutationResultPost]) error {
					request.ReplaceInput(golem.GeneratedUpdateManyInput[mutationResultPost](schema.Post,
						golem.GeneratedSetFieldValue(schema.Post, title, "hook kept this"),
						golem.GeneratedSetFieldValue(schema.Post, authorField, author(schema)),
					))
					return nil
				}),
			}
		}
	}
	return openMutationResultFixture(t, schemaFixture, MutationLimits{}, hooks, nil, nil, true)
}

func seedBatchSystemAuthorPosts(t *testing.T, ctx context.Context, fixture mutationResultFixture, author golem.UUID, ids ...byte) {
	t.Helper()
	if _, err := fixture.app.database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, mutationResultUUIDText(3), "carol"); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		seedSystemAuthorPost(t, ctx, fixture, id, author)
	}
}

func assertBatchSystemAuthorRowsUnchanged(t *testing.T, ctx context.Context, fixture mutationResultFixture, author golem.UUID, ids ...byte) {
	t.Helper()
	for _, id := range ids {
		if got := readSystemAuthor(t, ctx, fixture, id); got != author {
			t.Fatalf("row %d persisted author=%v, want the seeded %v", id, got, author)
		}
	}
	assertMutationResultTitleCount(t, fixture, "seeded", len(ids))
}

func TestUpdateManyBeforeHookWritesASystemFieldAcrossTheMatchedRows(t *testing.T) {
	ctx := context.Background()
	alice, bob := golem.UUID{15: 1}, golem.UUID{15: 2}
	fixture := batchSystemAuthorFixture(t, []compilerir.FieldMode{compilerir.ModeSystem}, func(schematest.Fixture) golem.UUID { return bob })
	seedBatchSystemAuthorPosts(t, ctx, fixture, alice, 71, 72, 73)
	caller := mustMutationResultCaller(t, fixture)
	count, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 71}, golem.UUID{15: 72}), fixture.updateManyTitle("caller wrote this"))
	if err != nil || count != 2 {
		t.Fatalf("hook-authored update-many system write count=%d err=%v chain=%s", count, err, errorChain(err))
	}
	for _, id := range []byte{71, 72} {
		if got := readSystemAuthor(t, ctx, fixture, id); got != bob {
			t.Fatalf("row %d persisted author=%v, want the value the hook authored", id, got)
		}
	}
	if got := readSystemAuthor(t, ctx, fixture, 73); got != alice {
		t.Fatalf("unmatched row persisted author=%v, want it untouched", got)
	}
	assertMutationResultTitleCount(t, fixture, "hook kept this", 2)
}

func TestUpdateManyCallerSystemFieldStaysRefusedWhenAHookRewritesItToAThirdValue(t *testing.T) {
	ctx := context.Background()
	alice, bob, carol := golem.UUID{15: 1}, golem.UUID{15: 2}, golem.UUID{15: 3}
	fixture := batchSystemAuthorFixture(t, []compilerir.FieldMode{compilerir.ModeSystem}, func(schematest.Fixture) golem.UUID { return carol })
	seedBatchSystemAuthorPosts(t, ctx, fixture, alice, 74, 75)
	caller := mustMutationResultCaller(t, fixture)
	forged := golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"),
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, bob),
	)
	count, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 74}, golem.UUID{15: 75}), forged)
	if err == nil || count != 0 {
		t.Fatalf("a hook rewriting the caller's own system-field value laundered it into an update-many: count=%d err=%v", count, err)
	}
	assertBatchSystemAuthorRowsUnchanged(t, ctx, fixture, alice, 74, 75)
}

func TestUpdateManyBeforeHookCannotWriteASystemImmutableField(t *testing.T) {
	ctx := context.Background()
	alice, bob := golem.UUID{15: 1}, golem.UUID{15: 2}
	fixture := batchSystemAuthorFixture(t, []compilerir.FieldMode{compilerir.ModeSystem, compilerir.ModeImmutable}, func(schematest.Fixture) golem.UUID { return bob })
	seedBatchSystemAuthorPosts(t, ctx, fixture, alice, 76, 77)
	caller := mustMutationResultCaller(t, fixture)
	count, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 76}, golem.UUID{15: 77}), fixture.updateManyTitle("caller wrote this"))
	if err == nil || count != 0 {
		t.Fatalf("an update-many hook wrote a system;immutable field: count=%d err=%v", count, err)
	}
	assertBatchSystemAuthorRowsUnchanged(t, ctx, fixture, alice, 76, 77)
}

func TestUpdateManyCallerCannotWriteASystemFieldWithNoHookInPlay(t *testing.T) {
	ctx := context.Background()
	alice, bob := golem.UUID{15: 1}, golem.UUID{15: 2}
	fixture := batchSystemAuthorFixture(t, []compilerir.FieldMode{compilerir.ModeSystem}, nil)
	seedBatchSystemAuthorPosts(t, ctx, fixture, alice, 78, 79)
	caller := mustMutationResultCaller(t, fixture)
	forged := golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, bob),
	)
	count, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 78}, golem.UUID{15: 79}), forged)
	if err == nil || count != 0 {
		t.Fatalf("caller update-many wrote a system field: count=%d err=%v", count, err)
	}
	assertBatchSystemAuthorRowsUnchanged(t, ctx, fixture, alice, 78, 79)
}
