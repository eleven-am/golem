package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func systemAuthorFixture(t *testing.T, hooks func(schematest.Fixture, golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor]) mutationResultFixture {
	t.Helper()
	schemaFixture := schematest.NewWithContractModes(t, schematest.ContractModes{AuthorID: []compilerir.FieldMode{compilerir.ModeSystem}})
	return openMutationResultFixture(t, schemaFixture, MutationLimits{}, hooks, nil, nil, true)
}

func seedSystemAuthorPost(t *testing.T, ctx context.Context, fixture mutationResultFixture, id byte, author golem.UUID) {
	t.Helper()
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, author, "seeded")); err != nil {
		t.Fatal(err)
	}
}

func readSystemAuthor(t *testing.T, ctx context.Context, fixture mutationResultFixture, id byte) golem.UUID {
	t.Helper()
	selector := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
		golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: id}))
	row, err := SystemFindUnique(ctx, fixture.app.System(), fixture.postDescriptor, selector, golem.RuntimeProjectionReadOption(golem.Select[mutationResultPost](fixture.authorID)))
	if err != nil {
		t.Fatal(err)
	}
	value, present := golem.Value(row, fixture.authorID).Get()
	if !present {
		t.Fatal("author identity is absent from the persisted row")
	}
	return value
}

func TestBeforeHookWritesASystemFieldTheCallerCannot(t *testing.T) {
	ctx := context.Background()
	alice, bob := golem.UUID{15: 1}, golem.UUID{15: 2}
	fixture := systemAuthorFixture(t, func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		author := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultPost]) error {
				request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultPost](schema.Post,
					golem.GeneratedSetFieldValue(schema.Post, title, "hook kept this"),
					golem.GeneratedSetFieldValue(schema.Post, author, bob),
				))
				return nil
			}),
		}
	})
	seedSystemAuthorPost(t, ctx, fixture, 51, alice)
	caller := mustMutationResultCaller(t, fixture)
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(51), fixture.updateTitle("caller wrote this")); err != nil {
		t.Fatalf("hook-authored system write was refused: %v", err)
	}
	if got := readSystemAuthor(t, ctx, fixture, 51); got != bob {
		t.Fatalf("persisted author=%v, want the value the hook authored", got)
	}
}

func TestCallerCannotWriteASystemFieldEvenThroughAHookThatEchoesIt(t *testing.T) {
	ctx := context.Background()
	alice, bob := golem.UUID{15: 1}, golem.UUID{15: 2}
	echo := func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		author := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultPost]) error {
				request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultPost](schema.Post,
					golem.GeneratedSetFieldValue(schema.Post, title, "hook kept this"),
					golem.GeneratedSetFieldValue(schema.Post, author, bob),
				))
				return nil
			}),
		}
	}
	fixture := systemAuthorFixture(t, echo)
	seedSystemAuthorPost(t, ctx, fixture, 52, alice)
	caller := mustMutationResultCaller(t, fixture)
	forged := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"),
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, bob),
	)
	_, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(52), forged)
	if err == nil || !strings.Contains(err.Error(), "system") && !strings.Contains(errorChain(err), "system") {
		t.Fatalf("caller-authored system field survived a hook that reauthored it: %v", err)
	}
	if got := readSystemAuthor(t, ctx, fixture, 52); got != alice {
		t.Fatalf("persisted author=%v, want the refused mutation to have changed nothing", got)
	}
}

func TestCallerCannotWriteASystemFieldEvenThroughAHookThatRewritesItToAThirdValue(t *testing.T) {
	ctx := context.Background()
	alice, bob, carol := golem.UUID{15: 1}, golem.UUID{15: 2}, golem.UUID{15: 3}
	fixture := systemAuthorFixture(t, func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		author := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultPost]) error {
				request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultPost](schema.Post,
					golem.GeneratedSetFieldValue(schema.Post, title, "hook kept this"),
					golem.GeneratedSetFieldValue(schema.Post, author, carol),
				))
				return nil
			}),
		}
	})
	if _, err := fixture.app.database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, mutationResultUUIDText(3), "carol"); err != nil {
		t.Fatal(err)
	}
	seedSystemAuthorPost(t, ctx, fixture, 54, alice)
	caller := mustMutationResultCaller(t, fixture)
	forged := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"),
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, bob),
	)
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(54), forged); err == nil {
		t.Fatal("a hook rewriting the caller's own system-field value laundered it into a write")
	}
	if got := readSystemAuthor(t, ctx, fixture, 54); got != alice {
		t.Fatalf("persisted author=%v, want the seeded value: the caller named the field and the hook rewrote it, so neither may reach the row", got)
	}
}

func TestCallerCannotWriteASystemFieldWithNoHookInPlay(t *testing.T) {
	ctx := context.Background()
	alice, bob := golem.UUID{15: 1}, golem.UUID{15: 2}
	fixture := systemAuthorFixture(t, nil)
	seedSystemAuthorPost(t, ctx, fixture, 53, alice)
	caller := mustMutationResultCaller(t, fixture)
	forged := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, bob),
	)
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(53), forged); err == nil {
		t.Fatal("caller wrote a system field")
	}
	if got := readSystemAuthor(t, ctx, fixture, 53); got != alice {
		t.Fatalf("persisted author=%v, want the refused mutation to have changed nothing", got)
	}
}

func errorChain(err error) string {
	var parts []string
	for err != nil {
		parts = append(parts, err.Error())
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = unwrapped.Unwrap()
	}
	return strings.Join(parts, " | ")
}

func TestBeforeHookWritesASystemFieldOnAMutationThatAlsoCarriesRelations(t *testing.T) {
	ctx := context.Background()
	schemaFixture := schematest.NewWithContractModes(t, schematest.ContractModes{UserName: []compilerir.FieldMode{compilerir.ModeSystem}})
	fixture := openMutationResultFixture(t, schemaFixture, MutationLimits{}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		name := golem.GeneratedTextField[mutationResultUser, string](schema.UserName)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultUser, golem.UpdateHookRequest[mutationResultUser]](schema.User, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultUser]) error {
				target := golem.GeneratedUniqueSelectorValue[mutationResultPost](schema.Post, schema.PostKey,
					golem.GeneratedSelectorComponent(schema.PostID, golem.UUID{15: 61}))
				request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultUser](schema.User,
					golem.GeneratedSetFieldValue(schema.User, name, "renamed by the hook"),
					golem.GeneratedNestedConnect[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, target),
				))
				return nil
			}),
		}
	}, nil, nil, true)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(61, golem.UUID{15: 2}, "before")); err != nil {
		t.Fatal(err)
	}
	postTarget := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
		golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 61}))
	userTarget := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
	input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedNestedConnect[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget),
	)
	caller := mustMutationResultCaller(t, fixture)
	if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget, input); err != nil {
		t.Fatalf("hook-authored system write on a relation-carrying mutation was refused: %v: %v", err, errorChain(err))
	}
	var name, author string
	if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "name" FROM "users" WHERE "id" = ?`, mutationResultUUIDText(1)).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "author_id" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(61)).Scan(&author); err != nil {
		t.Fatal(err)
	}
	if name != "renamed by the hook" || author != mutationResultUUIDText(1) {
		t.Fatalf("persisted name=%q author=%q", name, author)
	}
}
