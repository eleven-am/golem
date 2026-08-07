package runtime

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationnested "github.com/eleven-am/golem/go/internal/mutation/nested"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

func TestCallerNestedSelectedChildHooksTransformAndDeliverReverseResults(t *testing.T) {
	ctx := context.Background()
	var before, after, afterCommit atomic.Int64
	fixture := newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		name := golem.GeneratedTextField[mutationResultUser, string](schema.UserName)
		capability := golem.GeneratedCreateFieldCapability(schema.User, name)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultUser, golem.CreateHookRequest[mutationResultUser]](schema.User, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultUser]) error {
				before.Add(1)
				return golem.SetCreate(request, capability, "child-before")
			}),
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultUser, golem.CreateHookResult[mutationResultUser]](schema.User, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[mutationResultUser]) error {
				after.Add(1)
				if value, ok := golem.Value(result.Row(), name).Get(); !ok || value != "child-before" {
					t.Fatalf("child after name=%q present=%t", value, ok)
				}
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultUser, golem.CreateHookResult[mutationResultUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultUser]) error {
				afterCommit.Add(1)
				return nil
			}),
		}
	}, func(context.Context, golem.AfterCommitFailure) {})
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(32, golem.UUID{15: 1}, "root")); err != nil {
		t.Fatal(err)
	}
	caller := mustMutationResultCaller(t, fixture)
	created := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 3}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "untransformed"),
	)
	input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "root-updated"),
		golem.GeneratedNestedCreate[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, created),
	)
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(32), input); err != nil {
		t.Fatal(err)
	}
	if before.Load() != 1 || after.Load() != 1 || afterCommit.Load() != 1 {
		t.Fatalf("nested child hook calls before=%d after=%d afterCommit=%d", before.Load(), after.Load(), afterCommit.Load())
	}
	var name, author string
	if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "name" FROM "users" WHERE "id" = ?`, mutationResultUUIDText(3)).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "author_id" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(32)).Scan(&author); err != nil {
		t.Fatal(err)
	}
	if name != "child-before" || author != mutationResultUUIDText(3) {
		t.Fatalf("persisted child name=%q root author=%q", name, author)
	}
	for _, profile := range postgresAcceptanceProfiles() {
		if profile.name != "c" {
			continue
		}
		profile := profile
		t.Run("postgresql-c", func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			assertPostgresCallerNestedSelectedChildHooksTransform(t, profile)
		})
	}
}

func TestTransactionAfterHookUpsertExecutesSelectedNestedCreateAndUpdateBranches(t *testing.T) {
	ctx := context.Background()
	var hookCalls atomic.Int64
	var fixture mutationResultFixture
	fixture = newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(hookContext context.Context, result golem.CreateHookResult[mutationResultPost]) error {
				value, _ := golem.Value(result.Row(), title).Get()
				if value != "hook-upsert-outer-create" && value != "hook-upsert-outer-update" {
					return nil
				}
				call := hookCalls.Add(1)
				childID := byte(212)
				childTitle := "hook-upsert-nested-create"
				if call == 2 {
					childID, childTitle = 213, "hook-upsert-nested-update"
				}
				child := golem.GeneratedCreateInput[mutationResultPost](schema.Post,
					golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: childID}),
					golem.GeneratedCreateFieldValue(schema.Post, fixture.title, childTitle),
				)
				selector := golem.GeneratedUniqueSelectorValue[mutationResultUser](schema.User, schema.UserKey,
					golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: 211}))
				create := golem.GeneratedCreateInput[mutationResultUser](schema.User,
					golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 211}),
					golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "hook-upsert-created"),
					golem.GeneratedNestedCreate[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, child),
				)
				update := golem.GeneratedUpdateInput[mutationResultUser](schema.User,
					golem.GeneratedSetFieldValue(schema.User, fixture.userName, "hook-upsert-updated"),
					golem.GeneratedNestedCreate[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, child),
				)
				_, err := golem.HookUpsertRow(hookContext, result.Executor(), fixture.userDescriptor, selector, create, update)
				return err
			}),
		}
	}, nil)
	caller := mustMutationResultCaller(t, fixture)
	if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, fixture.createPost(210, golem.UUID{15: 1}, "hook-upsert-outer-create")); err != nil {
		t.Fatal(err)
	}
	if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, fixture.createPost(214, golem.UUID{15: 1}, "hook-upsert-outer-update")); err != nil {
		t.Fatal(err)
	}
	if hookCalls.Load() != 2 {
		t.Fatalf("transaction-after hook calls=%d want=2", hookCalls.Load())
	}
	assertMutationResultUserNameCount(t, fixture, "hook-upsert-created", 0)
	assertMutationResultUserNameCount(t, fixture, "hook-upsert-updated", 1)
	assertMutationResultTitleCount(t, fixture, "hook-upsert-nested-create", 1)
	assertMutationResultTitleCount(t, fixture, "hook-upsert-nested-update", 1)
	for _, profile := range postgresAcceptanceProfiles() {
		if profile.name != "c" {
			continue
		}
		profile := profile
		t.Run("postgresql-c", func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			assertPostgresTransactionAfterHookNestedUpsert(t, profile)
		})
	}
}

func assertPostgresTransactionAfterHookNestedUpsert(t *testing.T, profile postgresAcceptanceProfile) {
	t.Helper()
	ctx := context.Background()
	schema := schematest.NewSubscribedGraph(t)
	var fixture graphMutationFixture
	var hookCalls atomic.Int64
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](schema.Post, golem.HookCreate, func(hookContext context.Context, result golem.CreateHookResult[graphMutationPost]) error {
			value, _ := golem.Value(result.Row(), golem.GeneratedTextField[graphMutationPost, string](schema.PostTitle)).Get()
			if value != "pg-hook-upsert-outer-create" && value != "pg-hook-upsert-outer-update" {
				return nil
			}
			call := hookCalls.Add(1)
			childID := byte(212)
			childTitle := "pg-hook-upsert-nested-create"
			if call == 2 {
				childID, childTitle = 213, "pg-hook-upsert-nested-update"
			}
			child := golem.GeneratedCreateInput[graphMutationPost](schema.Post,
				golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: childID}),
				golem.GeneratedCreateFieldValue(schema.Post, fixture.postTitle, childTitle),
			)
			selector := golem.GeneratedUniqueSelectorValue[graphMutationUser](schema.User, schema.UserKey,
				golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: 211}))
			create := golem.GeneratedCreateInput[graphMutationUser](schema.User,
				golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 211}),
				golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "pg-hook-upsert-created"),
				golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, child),
			)
			update := golem.GeneratedUpdateInput[graphMutationUser](schema.User,
				golem.GeneratedSetFieldValue(schema.User, fixture.userName, "pg-hook-upsert-updated"),
				golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, child),
			)
			_, err := golem.HookUpsertRow(hookContext, result.Executor(), fixture.userDescriptor, selector, create, update)
			return err
		}),
	}
	fixture = newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, hooks)
	author := golem.GeneratedCreateInput[graphMutationUser](schema.User,
		golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 1}),
		golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "author"),
	)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, author); err != nil {
		t.Fatal(err)
	}
	authorID := golem.GeneratedEqualField[graphMutationPost, golem.UUID](schema.AuthorID)
	outer := func(id byte, title string) golem.CreateInput[graphMutationPost] {
		return golem.GeneratedCreateInput(schema.Post,
			golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(schema.Post, authorID, golem.UUID{15: 1}),
			golem.GeneratedCreateFieldValue(schema.Post, fixture.postTitle, title),
		)
	}
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, outer(210, "pg-hook-upsert-outer-create")); err != nil {
		t.Fatal(err)
	}
	if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, outer(214, "pg-hook-upsert-outer-update")); err != nil {
		t.Fatal(err)
	}
	if hookCalls.Load() != 2 {
		t.Fatalf("postgres transaction-after hook calls=%d want=2", hookCalls.Load())
	}
	assertGraphMutationRowsAndFacts(t, fixture, 2, 4, 0, 7)
}

func assertPostgresCallerNestedSelectedChildHooksTransform(t *testing.T, profile postgresAcceptanceProfile) {
	t.Helper()
	ctx := context.Background()
	schema := schematest.NewSubscribedGraph(t)
	name := golem.GeneratedTextField[graphMutationUser, string](schema.UserName)
	capability := golem.GeneratedCreateFieldCapability(schema.User, name)
	var before, after, afterCommit atomic.Int64
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookRequest[graphMutationUser]](schema.User, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[graphMutationUser]) error {
			before.Add(1)
			return golem.SetCreate(request, capability, "pg-child-before")
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationUser]) error {
			after.Add(1)
			if value, ok := golem.Value(result.Row(), name).Get(); !ok || value != "pg-child-before" {
				return fmt.Errorf("postgres child after name=%q present=%t", value, ok)
			}
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
			afterCommit.Add(1)
			return nil
		}),
	}
	fixture := newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, hooks)
	post := golem.GeneratedCreateInput[graphMutationPost](schema.Post,
		golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: 32}),
		golem.GeneratedCreateFieldValue(schema.Post, fixture.postTitle, "root"),
	)
	user := golem.GeneratedCreateInput[graphMutationUser](schema.User,
		golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 1}),
		golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "root-user"),
		golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, post),
	)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, user); err != nil {
		t.Fatal(err)
	}
	created := golem.GeneratedCreateInput[graphMutationUser](schema.User,
		golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 3}),
		golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "untransformed"),
	)
	input := golem.GeneratedUpdateInput[graphMutationPost](schema.Post,
		golem.GeneratedSetFieldValue(schema.Post, fixture.postTitle, "root-updated"),
		golem.GeneratedNestedCreate[graphMutationPost, graphMutationUser](schema.Post, schema.PostAuthor, schema.Authorship, schema.User, created),
	)
	target := golem.GeneratedUniqueSelectorValue[graphMutationPost](schema.Post, schema.PostKey,
		golem.GeneratedSelectorComponent(schema.PostID, golem.UUID{15: 32}))
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, target, input); err != nil {
		t.Fatal(err)
	}
	if before.Load() != 1 || after.Load() != 1 || afterCommit.Load() != 1 {
		t.Fatalf("postgres nested child hooks before=%d after=%d afterCommit=%d", before.Load(), after.Load(), afterCommit.Load())
	}
	users := nestedAcceptanceTable(fixture.app, schema.User)
	posts := nestedAcceptanceTable(fixture.app, schema.Post)
	var persistedName, author string
	query := fixture.app.database.Rebind(`SELECT "name" FROM ` + users + ` WHERE "id"=?`)
	if err := fixture.app.database.GetContext(ctx, &persistedName, query, mutationResultUUIDText(3)); err != nil {
		t.Fatal(err)
	}
	query = fixture.app.database.Rebind(`SELECT "author_id" FROM ` + posts + ` WHERE "id"=?`)
	if err := fixture.app.database.GetContext(ctx, &author, query, mutationResultUUIDText(32)); err != nil {
		t.Fatal(err)
	}
	if persistedName != "pg-child-before" || author != mutationResultUUIDText(3) {
		t.Fatalf("postgres persisted child name=%q root author=%q", persistedName, author)
	}
}

func TestCallerNestedCurrentToOneUpsertUpdateSynthesizesExactHookTarget(t *testing.T) {
	ctx := context.Background()
	var updateBefore, updateAfter atomic.Int64
	fixture := newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		name := golem.GeneratedTextField[mutationResultUser, string](schema.UserName)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultUser, golem.UpdateHookRequest[mutationResultUser]](schema.User, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultUser]) error {
				updateBefore.Add(1)
				request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultUser](schema.User, golem.GeneratedSetFieldValue(schema.User, name, "current-to-one-hook")))
				return nil
			}),
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultUser, golem.UpdateHookResult[mutationResultUser]](schema.User, golem.HookUpdate, func(_ context.Context, result golem.UpdateHookResult[mutationResultUser]) error {
				updateAfter.Add(1)
				if value, ok := golem.Value(result.After(), name).Get(); !ok || value != "current-to-one-hook" {
					t.Fatalf("current-to-one after name=%q present=%t", value, ok)
				}
				return nil
			}),
		}
	}, nil)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(33, golem.UUID{15: 1}, "root")); err != nil {
		t.Fatal(err)
	}
	createFallback := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 7}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "unused-create"),
	)
	update := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "untransformed"),
	)
	input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "root-updated"),
		golem.GeneratedNestedUpsert[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, nil, createFallback, update),
	)
	caller := mustMutationResultCaller(t, fixture)
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(33), input); err != nil {
		t.Fatal(err)
	}
	if updateBefore.Load() != 1 || updateAfter.Load() != 1 {
		t.Fatalf("current-to-one hook calls before=%d after=%d", updateBefore.Load(), updateAfter.Load())
	}
	var name string
	if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "name" FROM "users" WHERE "id" = ?`, mutationResultUUIDText(1)).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "current-to-one-hook" {
		t.Fatalf("current-to-one persisted name=%q", name)
	}
}

func TestCallerNestedInverseMembershipHookUsesExactOwnerAndPreservesRelationAssignment(t *testing.T) {
	ctx := context.Background()
	var before, after atomic.Int64
	fixture := newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		authorID := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultPost]) error {
				before.Add(1)
				request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultPost](schema.Post,
					golem.GeneratedSetFieldValue(schema.Post, authorID, golem.UUID{15: 1}),
					golem.GeneratedSetFieldValue(schema.Post, title, "inverse-membership-hook"),
				))
				return nil
			}),
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookResult[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, result golem.UpdateHookResult[mutationResultPost]) error {
				after.Add(1)
				if value, ok := golem.Value(result.After(), title).Get(); !ok || value != "inverse-membership-hook" {
					t.Fatalf("inverse membership after title=%q present=%t", value, ok)
				}
				return nil
			}),
		}
	}, nil)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(36, golem.UUID{15: 2}, "before")); err != nil {
		t.Fatal(err)
	}
	postTarget := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
		golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 36}))
	input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "alice-updated"),
		golem.GeneratedNestedConnect[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget),
	)
	userTarget := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
	caller := mustMutationResultCaller(t, fixture)
	if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget, input); err != nil {
		t.Fatal(err)
	}
	if before.Load() != 1 || after.Load() != 1 {
		t.Fatalf("inverse membership hook calls before=%d after=%d", before.Load(), after.Load())
	}
	var title, author string
	if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "title", "author_id" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(36)).Scan(&title, &author); err != nil {
		t.Fatal(err)
	}
	if title != "inverse-membership-hook" || author != mutationResultUUIDText(1) {
		t.Fatalf("inverse membership persisted title=%q author=%q", title, author)
	}
}

func TestCallerNestedSourceMembershipHookRejectsChangedExactOwnerAndRollsBack(t *testing.T) {
	ctx := context.Background()
	var before, after atomic.Int64
	var fixture mutationResultFixture
	fixture = newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultPost]) error {
				if before.Add(1) == 2 {
					request.ReplaceTarget(fixture.target(35))
				}
				return nil
			}),
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookResult[mutationResultPost]](schema.Post, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[mutationResultPost]) error {
				after.Add(1)
				return nil
			}),
		}
	}, nil)
	for _, id := range []byte{34, 35} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "before")); err != nil {
			t.Fatal(err)
		}
	}
	userTarget := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 2}))
	input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "must-roll-back"),
		golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userTarget),
	)
	caller := mustMutationResultCaller(t, fixture)
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(34), input); err == nil {
		t.Fatal("changed membership owner target unexpectedly succeeded")
	}
	if before.Load() != 2 || after.Load() != 0 {
		t.Fatalf("source membership hook calls before=%d after=%d", before.Load(), after.Load())
	}
	var title, author string
	if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "title", "author_id" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(34)).Scan(&title, &author); err != nil {
		t.Fatal(err)
	}
	if title != "before" || author != mutationResultUUIDText(1) {
		t.Fatalf("source membership rollback title=%q author=%q", title, author)
	}
}

func TestTransactionAfterHookUsesSameTransactionAndReverseNodeOrder(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		schema := schematest.NewSubscribedGraph(t)
		assertTransactionAfterHookUsesSameTransactionAndReverseNodeOrder(t, schema, func(hooks []golem.HookBinding[graphMutationActor]) graphMutationFixture {
			return newGraphMutationFixtureWithHooks(t, schema, golem.ModelID{}, hooks)
		})
	})
	for _, profile := range postgresAcceptanceProfiles() {
		if profile.name != "c" {
			continue
		}
		profile := profile
		t.Run("postgresql-c", func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			schema := schematest.NewSubscribedGraph(t)
			assertTransactionAfterHookUsesSameTransactionAndReverseNodeOrder(t, schema, func(hooks []golem.HookBinding[graphMutationActor]) graphMutationFixture {
				return newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, hooks)
			})
		})
	}
}

func assertTransactionAfterHookUsesSameTransactionAndReverseNodeOrder(t *testing.T, schema schematest.GraphFixture, open func([]golem.HookBinding[graphMutationActor]) graphMutationFixture) {
	t.Helper()
	ctx := context.Background()
	var lock sync.Mutex
	order := make([]string, 0, 3)
	appendOrder := func(value string) {
		lock.Lock()
		defer lock.Unlock()
		order = append(order, value)
	}
	var fixture graphMutationFixture
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
			appendOrder("user")
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationPost]) error {
			appendOrder("post")
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationComment, golem.CreateHookResult[graphMutationComment]](schema.Comment, golem.HookCreate, func(hookContext context.Context, result golem.CreateHookResult[graphMutationComment]) error {
			rows, err := golem.HookFindManyRows(hookContext, result.Executor(), fixture.userDescriptor,
				golem.Where(fixture.userID.Eq(golem.UUID{15: 41})), golem.Select[graphMutationUser](fixture.userName))
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				return fmt.Errorf("same-transaction root rows=%d", len(rows))
			}
			appendOrder("comment")
			return nil
		}),
	}
	fixture = open(hooks)
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerCreate(ctx, caller, fixture.userDescriptor, fixture.deepCreate(41, 42, 43)); err != nil {
		t.Fatal(err)
	}
	if want := []string{"comment", "post", "user"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("transaction-after reverse order=%v want=%v", order, want)
	}
}

func TestCallerNestedHookScalarReturnsCompleteVerifiedRootImagesOnSameBinding(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(26, golem.UUID{15: 1}, "before")); err != nil {
		t.Fatal(err)
	}
	caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	selector := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 2}))
	input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "after"),
		golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, selector),
	)
	frozenInput, err := golem.RuntimeFreezeUpdateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(fixture.target(26))
	if err != nil {
		t.Fatal(err)
	}
	if err := CallerTransaction(ctx, caller, func(tx *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		result, executeErr := executeCallerNestedHookScalar(ctx, tx.caller, mutationir.Update, fixture.schema.Post, &frozenInput, &frozenTarget)
		if executeErr != nil {
			return executeErr
		}
		beforeRuntime, ok := result.Before()
		if !ok {
			t.Fatal("complete before image is absent")
		}
		afterRuntime, ok := result.After()
		if !ok {
			t.Fatal("complete after image is absent")
		}
		before, convertErr := golem.RuntimeTypedReadRow(fixture.postDescriptor, beforeRuntime)
		if convertErr != nil {
			return convertErr
		}
		after, convertErr := golem.RuntimeTypedReadRow(fixture.postDescriptor, afterRuntime)
		if convertErr != nil {
			return convertErr
		}
		if title, present := golem.Value(before, fixture.title).Get(); !present || title != "before" {
			t.Fatalf("before title=%q present=%t", title, present)
		}
		if title, present := golem.Value(after, fixture.title).Get(); !present || title != "after" {
			t.Fatalf("after title=%q present=%t", title, present)
		}
		if author, present := golem.Value(after, fixture.authorID).Get(); !present || author != (golem.UUID{15: 2}) {
			t.Fatalf("after author=%v present=%t", author, present)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var title, author string
	if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "title", "author_id" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(26)).Scan(&title, &author); err != nil {
		t.Fatal(err)
	}
	if title != "after" || author != mutationResultUUIDText(2) {
		t.Fatalf("persisted title=%q author=%q", title, author)
	}
}

func TestNestedBoundaryVerifyObserverRunsReverseBeforeCommitOnActiveBinding(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(27, golem.UUID{15: 1}, "before")); err != nil {
		t.Fatal(err)
	}
	selector := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 2}))
	input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "after"),
		golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, selector),
	)
	frozenInput, err := golem.RuntimeFreezeUpdateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(fixture.target(27))
	if err != nil {
		t.Fatal(err)
	}
	result, err := mutationir.NewImageRequirements(policyir.ModelID(fixture.schema.Post), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := prepareNestedGraph(fixture.app, nil, mutationir.System, mutationir.Update, &frozenInput, &frozenTarget, result)
	if err != nil {
		t.Fatal(err)
	}
	var verified []uint32
	boundary := &systemNestedBoundary[mutationResultPrincipal, mutationResultActor]{
		app: fixture.app, source: fixture.app.System().executor, graph: graph, stance: mutationir.System,
		verify: func(observerContext context.Context, binding *executionBinding, applied mutationnested.AppliedNode) error {
			verified = append(verified, applied.Node().Ordinal())
			if applied.Node().Ordinal() != 0 {
				return nil
			}
			queryer, queryErr := binding.queryerFor(fixture.app.database)
			if queryErr != nil {
				return queryErr
			}
			var author string
			if queryErr := queryer.QueryRowxContext(observerContext, `SELECT "author_id" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(27)).Scan(&author); queryErr != nil {
				return queryErr
			}
			if author != mutationResultUUIDText(2) {
				t.Fatalf("pre-commit root author=%q", author)
			}
			return nil
		},
	}
	if _, err := mutationnested.Execute(ctx, graph, uint32(fixture.app.mutationLimits.touchedRows), boundary); err != nil {
		t.Fatal(err)
	}
	if len(verified) < 2 || verified[len(verified)-1] != 0 {
		t.Fatalf("reverse verification ordinals=%v", verified)
	}
}

func TestNestedMutationProjectionMaterializesFinalRelationAndCallerMaskBeforeCommit(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	for _, id := range []byte{28, 29} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "before")); err != nil {
			t.Fatal(err)
		}
	}
	selector := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 2}))
	input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "after"),
		golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, selector),
	)
	projection := golem.Select[mutationResultPost](fixture.title, fixture.author.Select(fixture.userName))
	systemRow, err := SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(28), input, projection)
	if err != nil {
		t.Fatal(err)
	}
	if title, present := golem.Value(systemRow, fixture.title).Get(); !present || title != "after" {
		t.Fatalf("system projected title=%q present=%t", title, present)
	}
	systemAuthor, present := golem.One(systemRow, fixture.author).Get()
	if !present {
		t.Fatal("system projected final author is absent")
	}
	if name, ok := golem.Value(systemAuthor, fixture.userName).Get(); !ok || name != "bob" {
		t.Fatalf("system projected author=%q present=%t", name, ok)
	}
	caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	callerRow, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(29), input, projection)
	if err != nil {
		t.Fatal(err)
	}
	if title := golem.Value(callerRow, fixture.title); !title.IsSelected() || !title.IsNull() {
		t.Fatalf("caller final-state title mask=%d", title.State())
	}
	callerAuthor, present := golem.One(callerRow, fixture.author).Get()
	if !present {
		t.Fatal("caller projected final author is absent")
	}
	if name, ok := golem.Value(callerAuthor, fixture.userName).Get(); !ok || name != "bob" {
		t.Fatalf("caller projected author=%q present=%t", name, ok)
	}
}

func TestNestedConnectOrCreateUsesSelectorGuardAndCleansSQLiteGuardRows(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	for _, id := range []byte{30, 31} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "before")); err != nil {
			t.Fatal(err)
		}
	}
	connectTarget := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 2}))
	unusedCreate := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 9}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "unused"),
	)
	connectInput := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "connected"),
		golem.GeneratedNestedConnectOrCreate[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, connectTarget, unusedCreate),
	)
	if _, err := SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(30), connectInput); err != nil {
		t.Fatal(err)
	}
	createTarget := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 8}))
	create := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 8}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "created-by-coc"),
	)
	createInput := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "created"),
		golem.GeneratedNestedConnectOrCreate[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, createTarget, create),
	)
	if _, err := SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(31), createInput); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct {
		post, author byte
	}{{30, 2}, {31, 8}} {
		var author string
		if err := fixture.app.database.GetContext(ctx, &author, `SELECT "author_id" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(expected.post)); err != nil {
			t.Fatal(err)
		}
		if author != mutationResultUUIDText(expected.author) {
			t.Fatalf("post %d author=%q", expected.post, author)
		}
	}
	var guards int
	if err := fixture.app.database.GetContext(ctx, &guards, `SELECT COUNT(*) FROM "_golem_upsert_guard"`); err != nil || guards != 0 {
		t.Fatalf("selector guard rows=%d err=%v", guards, err)
	}
}

func TestEngineOwnedNestedBranchRetriesWholeAttemptAfterInterference(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(32, golem.UUID{15: 1}, "before")); err != nil {
		t.Fatal(err)
	}
	target := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 2}))
	create := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 9}),
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "unused"),
	)
	input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "after-retry"),
		golem.GeneratedNestedConnectOrCreate[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, target, create),
	)
	frozenInput, err := golem.RuntimeFreezeUpdateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	frozenTarget, err := golem.RuntimeFreezeMutationTarget(fixture.target(32))
	if err != nil {
		t.Fatal(err)
	}
	result, err := mutationir.NewImageRequirements(policyir.ModelID(fixture.schema.Post), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := prepareNestedGraph(fixture.app, nil, mutationir.System, mutationir.Update, &frozenInput, &frozenTarget, result)
	if err != nil {
		t.Fatal(err)
	}
	system := fixture.app.System()
	attempts := 0
	err = executeNestedGraphWithRetry(ctx, graph, system.executor, fixture.app.mutationLimits, func() mutationnested.ExecutionBoundary {
		attempts++
		inner := &systemNestedBoundary[mutationResultPrincipal, mutationResultActor]{app: fixture.app, source: system.executor, graph: graph, stance: mutationir.System}
		if attempts == 1 {
			return retryFaultNestedBoundary{inner: inner, failApply: 2, err: &pgconn.PgError{Code: "40001", Message: "injected serialization interference"}}
		}
		return inner
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("nested attempts=%d want 2", attempts)
	}
	var title, author string
	if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "title", "author_id" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(32)).Scan(&title, &author); err != nil {
		t.Fatal(err)
	}
	if title != "after-retry" || author != mutationResultUUIDText(2) {
		t.Fatalf("retried nested result title=%q author=%q", title, author)
	}
}

func TestNestedUpsertExecutesTruthfulInverseUpdateAndCreateBranches(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixture(t)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(33, golem.UUID{15: 1}, "before")); err != nil {
		t.Fatal(err)
	}
	userTarget := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
	postTarget := func(id byte) golem.MutationTarget[mutationResultPost] {
		return golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: id}))
	}
	createPost := func(id byte, title string) golem.CreateInput[mutationResultPost] {
		return golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, title),
		)
	}
	for _, branch := range []struct {
		id         byte
		createName string
		updateName string
		want       string
	}{{33, "unused", "updated-upsert", "updated-upsert"}, {34, "created-upsert", "unused", "created-upsert"}} {
		input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "owner"),
			golem.GeneratedNestedUpsert[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post,
				postTarget(branch.id), createPost(branch.id, branch.createName), fixture.updateTitle(branch.updateName)),
		)
		if _, err := SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, userTarget, input); err != nil {
			t.Fatal(err)
		}
		var author, title string
		if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "author_id", "title" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(branch.id)).Scan(&author, &title); err != nil {
			t.Fatal(err)
		}
		if author != mutationResultUUIDText(1) || title != branch.want {
			t.Fatalf("upsert %d author=%q title=%q", branch.id, author, title)
		}
	}
}

func TestNestedGuardedBranchesPostgreSQLProfiles(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			ctx := context.Background()
			fixture, namespace := newMutationResultPostgresFixture(t, ctx, profile)
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(35, golem.UUID{15: 1}, "before")); err != nil {
				t.Fatal(err)
			}
			userTarget := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
				golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 2}))
			unused := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
				golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 9}),
				golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "unused"),
			)
			connect := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
				golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "pg-connected"),
				golem.GeneratedNestedConnectOrCreate[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userTarget, unused),
			)
			projection := golem.Select[mutationResultPost](fixture.title, fixture.author.Select(fixture.userName))
			row, err := SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(35), connect, projection)
			if err != nil {
				t.Fatal(err)
			}
			author, present := golem.One(row, fixture.author).Get()
			if title, ok := golem.Value(row, fixture.title).Get(); !ok || title != "pg-connected" || !present {
				t.Fatalf("PostgreSQL projected title=%q titlePresent=%t authorPresent=%t", title, ok, present)
			}
			if name, ok := golem.Value(author, fixture.userName).Get(); !ok || name != "bob" {
				t.Fatalf("PostgreSQL projected author=%q present=%t", name, ok)
			}

			owner := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
				golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
			postTarget := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
				golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 36}))
			postCreate := golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post,
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 36}),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "pg-upsert-created"),
			)
			ownerUpdate := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
				golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "alice"),
				golem.GeneratedNestedUpsert[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post,
					postTarget, postCreate, fixture.updateTitle("unused")),
			)
			if _, err := SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, owner, ownerUpdate); err != nil {
				t.Fatal(err)
			}
			var persistedAuthor, persistedTitle string
			posts := `"` + string(namespace) + `"."posts"`
			if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "author_id", "title" FROM `+posts+` WHERE "id" = $1`, mutationResultUUIDText(36)).Scan(&persistedAuthor, &persistedTitle); err != nil {
				t.Fatal(err)
			}
			if persistedAuthor != mutationResultUUIDText(1) || persistedTitle != "pg-upsert-created" {
				t.Fatalf("PostgreSQL nested upsert author=%q title=%q", persistedAuthor, persistedTitle)
			}
		})
	}
}

func TestTwoModelNestedMutationVocabularyExecutesEveryOperationAcrossProviders(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		assertPublicNestedMutationVocabulary(t, newMutationResultFixture(t))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			fixture, _ := newMutationResultPostgresFixture(t, context.Background(), profile)
			assertPublicNestedMutationVocabulary(t, fixture)
		})
	}
}

func TestCallerNestedMutationVocabularyExecutesCompleteSocialGraphAcrossProviders(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		assertNestedMutationVocabulary(t, profile.fixture, true)
	})
}

func assertPublicNestedMutationVocabulary(t *testing.T, fixture mutationResultFixture) {
	assertNestedMutationVocabulary(t, fixture, false)
}

func assertNestedMutationVocabulary(t *testing.T, fixture mutationResultFixture, callerMode bool) {
	t.Helper()
	ctx := context.Background()
	system := fixture.app.System()
	var caller *Caller[mutationResultPrincipal, mutationResultActor]
	if callerMode {
		var err error
		caller, err = fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
	}
	createPostRoot := func(input golem.CreateInput[mutationResultPost]) error {
		if callerMode {
			_, err := CallerCreate(ctx, caller, fixture.postDescriptor, input)
			return err
		}
		_, err := SystemCreate(ctx, system, fixture.postDescriptor, input)
		return err
	}
	updateUserRoot := func(target golem.MutationTarget[mutationResultUser], input golem.UpdateInput[mutationResultUser]) error {
		if callerMode {
			_, err := CallerUpdate(ctx, caller, fixture.userDescriptor, target, input)
			return err
		}
		_, err := SystemUpdate(ctx, system, fixture.userDescriptor, target, input)
		return err
	}
	updatePostRoot := func(target golem.MutationTarget[mutationResultPost], input golem.UpdateInput[mutationResultPost]) error {
		if callerMode {
			_, err := CallerUpdate(ctx, caller, fixture.postDescriptor, target, input)
			return err
		}
		_, err := SystemUpdate(ctx, system, fixture.postDescriptor, target, input)
		return err
	}
	userTarget := func(id byte) golem.MutationTarget[mutationResultUser] {
		return golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: id}))
	}
	postTarget := func(id byte) golem.MutationTarget[mutationResultPost] {
		return golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: id}))
	}
	postCreate := func(id byte, title string) golem.CreateInput[mutationResultPost] {
		return golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, title))
	}
	userUpdate := func(name string, nested ...golem.NestedUpdateValue[mutationResultUser]) golem.UpdateInput[mutationResultUser] {
		values := []golem.UpdateValue[mutationResultUser]{golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, name)}
		for _, relation := range nested {
			values = append(values, relation)
		}
		return golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User, values...)
	}

	create := golem.GeneratedNestedCreate[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postCreate(50, "create"))
	if err := updateUserRoot(userTarget(1), userUpdate("create-owner", create)); err != nil {
		t.Fatalf("nested Create: %v", err)
	}
	createMany := golem.GeneratedNestedCreateMany[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postCreate(51, "many-a"), postCreate(52, "many-b"))
	if err := updateUserRoot(userTarget(1), userUpdate("many-owner", createMany)); err != nil {
		t.Fatalf("nested CreateMany: %v", err)
	}
	if err := createPostRoot(fixture.createPost(53, golem.UUID{15: 1}, "connect-before")); err != nil {
		t.Fatal(err)
	}
	connect := golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userTarget(2))
	if err := updatePostRoot(fixture.target(53), golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "connect"), connect)); err != nil {
		t.Fatalf("nested Connect: %v", err)
	}
	if err := createPostRoot(fixture.createPost(54, golem.UUID{15: 1}, "coc-before")); err != nil {
		t.Fatal(err)
	}
	coc := golem.GeneratedNestedConnectOrCreate[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userTarget(2), golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 9}), golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "unused")))
	if err := updatePostRoot(fixture.target(54), golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "coc"), coc)); err != nil {
		t.Fatalf("nested ConnectOrCreate: %v", err)
	}
	disconnect := golem.GeneratedNestedDisconnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userTarget(2))
	if err := updatePostRoot(fixture.target(53), golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "must-rollback"), disconnect)); err == nil {
		t.Fatal("nested Disconnect unexpectedly cleared a required relation")
	}
	set := golem.GeneratedNestedSet[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(50), postTarget(51), postTarget(52), postTarget(53))
	if err := updateUserRoot(userTarget(1), userUpdate("set-owner", set)); err != nil {
		t.Fatalf("nested Set: %v", err)
	}
	if err := updatePostRoot(fixture.target(53), fixture.updateTitle("set")); err != nil {
		t.Fatal(err)
	}
	update := golem.GeneratedNestedUpdate[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(50), fixture.updateTitle("updated"))
	if err := updateUserRoot(userTarget(1), userUpdate("update-owner", update)); err != nil {
		t.Fatalf("nested Update: %v", err)
	}
	updateMany := golem.GeneratedNestedUpdateMany[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, fixture.postID.In(golem.UUID{15: 51}, golem.UUID{15: 52}), fixture.updateManyTitle("bulk-updated"))
	if err := updateUserRoot(userTarget(1), userUpdate("update-many-owner", updateMany)); err != nil {
		t.Fatalf("nested UpdateMany: %v", err)
	}
	upsertUpdate := golem.GeneratedNestedUpsert[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(50), postCreate(50, "unused"), fixture.updateTitle("upsert-updated"))
	if err := updateUserRoot(userTarget(1), userUpdate("upsert-update-owner", upsertUpdate)); err != nil {
		t.Fatalf("nested Upsert update: %v", err)
	}
	upsertCreate := golem.GeneratedNestedUpsert[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(55), postCreate(55, "upsert-created"), fixture.updateTitle("unused"))
	if err := updateUserRoot(userTarget(1), userUpdate("upsert-create-owner", upsertCreate)); err != nil {
		t.Fatalf("nested Upsert create: %v", err)
	}
	deleteOne := golem.GeneratedNestedDelete[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, postTarget(55))
	if err := updateUserRoot(userTarget(1), userUpdate("delete-owner", deleteOne)); err != nil {
		t.Fatalf("nested Delete: %v", err)
	}
	deleteMany := golem.GeneratedNestedDeleteMany[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, fixture.postID.In(golem.UUID{15: 51}, golem.UUID{15: 52}))
	if err := updateUserRoot(userTarget(1), userUpdate("delete-many-owner", deleteMany)); err != nil {
		t.Fatalf("nested DeleteMany: %v", err)
	}

	posts := mutationVocabularyTable(fixture, fixture.schema.Post)
	var title, author string
	query := fixture.app.database.Rebind(`SELECT "title", "author_id" FROM ` + posts + ` WHERE "id" = ?`)
	if err := fixture.app.database.QueryRowxContext(ctx, query, mutationResultUUIDText(53)).Scan(&title, &author); err != nil {
		t.Fatal(err)
	}
	if title != "set" || author != mutationResultUUIDText(1) {
		t.Fatalf("required Disconnect rollback/Set result title=%q author=%q", title, author)
	}
	var deleted int
	query = fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + posts + ` WHERE "id" = ? OR "title" = ?`)
	if err := fixture.app.database.GetContext(ctx, &deleted, query, mutationResultUUIDText(55), "bulk-updated"); err != nil || deleted != 0 {
		t.Fatalf("nested delete/delete-many remaining=%d err=%v", deleted, err)
	}
}

func mutationVocabularyTable(fixture mutationResultFixture, model golem.ModelID) string {
	provider := golem.SQLite
	if fixture.app.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	physicalModel, _ := fixture.app.registry.PhysicalModel(provider, model)
	if provider == golem.SQLite {
		return `"` + string(physicalModel.Name()) + `"`
	}
	namespace, _ := fixture.app.registry.PhysicalNamespace(provider)
	return `"` + string(namespace) + `"."` + string(physicalModel.Name()) + `"`
}

func newMutationResultPostgresFixture(t *testing.T, ctx context.Context, profile postgresAcceptanceProfile) (mutationResultFixture, physical.PhysicalName) {
	return newMutationResultPostgresFixtureWithLimits(t, ctx, profile, MutationLimits{})
}

func newMutationResultPostgresFixtureWithLimits(t *testing.T, ctx context.Context, profile postgresAcceptanceProfile, limits MutationLimits) (mutationResultFixture, physical.PhysicalName) {
	t.Helper()
	suffix := time.Now().UnixNano()
	namespace := physical.PhysicalName(fmt.Sprintf("golem_p4_nested_%s_%d_%d", profile.name, os.Getpid(), suffix))
	systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_nested_system_%s_%d_%d", profile.name, os.Getpid(), suffix))
	schemaFixture := schematest.NewSubscribedIndexedPostgreSQLNamespaces(t, namespace, systemNamespace)
	provider := postgresprovider.New()
	database, _, err := provider.Open(ctx, profile.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(systemNamespace)+`" CASCADE`)
		_ = database.Close()
	})
	if err := provider.ApplyInitial(ctx, database, schemaFixture.PostgreSQL); err != nil {
		t.Fatal(err)
	}
	users := `"` + string(namespace) + `"."users"`
	for _, user := range [][2]string{{mutationResultUUIDText(1), "alice"}, {mutationResultUUIDText(2), "bob"}} {
		if _, err := database.ExecContext(ctx, `INSERT INTO `+users+`("id","name") VALUES ($1,$2)`, user[0], user[1]); err != nil {
			t.Fatal(err)
		}
	}
	return openMutationResultPostgresAppWithLimits(t, ctx, database, schemaFixture, limits), namespace
}

func openMutationResultPostgresApp(t *testing.T, ctx context.Context, database *sqlx.DB, schemaFixture schematest.Fixture) mutationResultFixture {
	return openMutationResultPostgresAppWithLimits(t, ctx, database, schemaFixture, MutationLimits{})
}

func openMutationResultPostgresAppWithLimits(t *testing.T, ctx context.Context, database *sqlx.DB, schemaFixture schematest.Fixture, limits MutationLimits) mutationResultFixture {
	t.Helper()
	userIdentity := golem.GeneratedIdentityMetadata(schemaFixture.User, schemaFixture.UserKey, golem.PrimaryIdentity, schemaFixture.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schemaFixture.Post, schemaFixture.PostKey, golem.PrimaryIdentity, schemaFixture.PostID)
	userRelation := golem.GeneratedRelationMetadata(schemaFixture.User, schemaFixture.Post, schemaFixture.UserPosts, schemaFixture.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(schemaFixture.Post, schemaFixture.User, schemaFixture.PostAuthor, schemaFixture.Authorship, golem.RelationSource, golem.RelationToOne)
	userDescriptor := golem.GeneratedModelDescriptor[mutationResultUser](schemaFixture.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.UserID, schemaFixture.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation}))
	postDescriptor := golem.GeneratedModelDescriptor[mutationResultPost](schemaFixture.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.PostID, schemaFixture.AuthorID, schemaFixture.PostTitle}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation}))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(schemaFixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(schemaFixture.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userName := golem.GeneratedTextField[mutationResultUser, string](schemaFixture.UserName)
	postTitle := golem.GeneratedTextField[mutationResultPost, string](schemaFixture.PostTitle)
	postAuthor := golem.GeneratedToOne[mutationResultPost, mutationResultUser](schemaFixture.PostAuthor, schemaFixture.Authorship, schemaFixture.User)
	allowUsers := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schemaFixture.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		rules.CanDelete(golem.All[mutationResultUser]())
		return rules.Freeze(schemaFixture.User)
	})
	allowPosts := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schemaFixture.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.All[mutationResultPost]())
		rules.CanDelete(golem.All[mutationResultPost]())
		return rules.Freeze(schemaFixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(schemaFixture.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{allowUsers, allowPosts}, []golem.HookBinding[mutationResultActor]{})
	bindings, err := golem.GeneratedApplicationBindings(schemaFixture.Bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		DB: database, Provider: golem.PostgreSQL, Bundle: schemaFixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		MutationLimits: limits,
		ResolvePrincipal: func(context.Context, mutationResultPrincipal) (mutationResultActor, error) {
			return mutationResultActor{}, nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return mutationResultFixture{
		app: app, userDescriptor: userDescriptor, postDescriptor: postDescriptor,
		userID: golem.GeneratedEqualField[mutationResultUser, golem.UUID](schemaFixture.UserID), userName: userName,
		postID:   golem.GeneratedEqualField[mutationResultPost, golem.UUID](schemaFixture.PostID),
		authorID: golem.GeneratedEqualField[mutationResultPost, golem.UUID](schemaFixture.AuthorID),
		title:    postTitle,
		author:   postAuthor, schema: schemaFixture,
	}
}

type retryFaultNestedBoundary struct {
	inner     mutationnested.ExecutionBoundary
	failApply int
	err       error
}

func (boundary retryFaultNestedBoundary) BeginNested(ctx context.Context) (mutationnested.ExecutionTransaction, error) {
	transaction, err := boundary.inner.BeginNested(ctx)
	if err != nil {
		return nil, err
	}
	return &retryFaultNestedTransaction{ExecutionTransaction: transaction, failApply: boundary.failApply, err: boundary.err}, nil
}

type retryFaultNestedTransaction struct {
	mutationnested.ExecutionTransaction
	failApply int
	applies   int
	err       error
}

func (transaction *retryFaultNestedTransaction) ApplyNested(ctx context.Context, request mutationnested.ApplyRequest) (mutationnested.ApplyResult, error) {
	transaction.applies++
	if transaction.applies == transaction.failApply {
		return mutationnested.ApplyResult{}, transaction.err
	}
	return transaction.ExecutionTransaction.ApplyNested(ctx, request)
}
