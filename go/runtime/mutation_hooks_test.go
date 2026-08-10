package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/jackc/pgx/v5/pgconn"
)

type mutationHookContextKey uint8

const mutationHookVeto mutationHookContextKey = 1

func TestAfterCommitFailureReportsCommittedSuccess(t *testing.T) {
	ctx := context.Background()
	afterCommitFailure := errors.New("after commit observation failed")
	var beforeCalls, afterCalls, afterCommitCalls, reports atomic.Int64
	fixture := newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		createTitle := golem.GeneratedCreateFieldCapability(schema.Post, title)
		postID := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.PostID)
		authorID := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID)
		before := golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultPost]) error {
			beforeCalls.Add(1)
			return golem.SetCreate(request, createTitle, "hooked")
		})
		after := golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(hookContext context.Context, result golem.CreateHookResult[mutationResultPost]) error {
			afterCalls.Add(1)
			row := result.Row()
			if value, ok := golem.Value(row, title).Get(); !ok || value != "hooked" {
				t.Fatalf("after title=%q present=%t", value, ok)
			}
			if _, ok := golem.Value(row, postID).Get(); !ok {
				t.Fatal("after hook did not receive complete ID")
			}
			if _, ok := golem.Value(row, authorID).Get(); !ok {
				t.Fatal("after hook did not receive complete untouched author ID")
			}
			if hookContext.Value(mutationHookVeto) != nil {
				return errors.New("after veto")
			}
			return nil
		})
		afterCommit := golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[mutationResultPost]) error {
			afterCommitCalls.Add(1)
			if value, ok := golem.Value(result.Row(), title).Get(); !ok || value != "hooked" {
				t.Fatalf("after-commit title=%q present=%t", value, ok)
			}
			return afterCommitFailure
		})
		return []golem.HookBinding[mutationResultActor]{before, after, afterCommit}
	}, func(_ context.Context, failure golem.AfterCommitFailure) {
		reports.Add(1)
		if failure.Operation() != golem.HookCreate || !errors.Is(failure.Cause(), afterCommitFailure) {
			t.Errorf("after-commit report=%#v", failure)
		}
	})

	row, err := CallerCreate(ctx, mustMutationResultCaller(t, fixture), fixture.postDescriptor, fixture.createPost(81, golem.UUID{15: 1}, "original"), golem.Select[mutationResultPost](fixture.title))
	if err != nil {
		t.Fatalf("%v: %v", err, errors.Unwrap(err))
	}
	if value, ok := golem.Value(row, fixture.title).Get(); !ok || value != "hooked" {
		t.Fatalf("public title=%q present=%t", value, ok)
	}
	if beforeCalls.Load() != 1 || afterCalls.Load() != 1 || afterCommitCalls.Load() != 1 || reports.Load() != 1 {
		t.Fatalf("calls before=%d after=%d afterCommit=%d reports=%d", beforeCalls.Load(), afterCalls.Load(), afterCommitCalls.Load(), reports.Load())
	}
	assertMutationResultTitleCount(t, fixture, "hooked", 1)
	assertMutationResultTitleCount(t, fixture, "original", 0)

	_, err = CallerCreate(context.WithValue(ctx, mutationHookVeto, true), mustMutationResultCaller(t, fixture), fixture.postDescriptor, fixture.createPost(82, golem.UUID{15: 1}, "veto"))
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
		t.Fatalf("veto failure=%#v err=%v", failure, err)
	}
	assertMutationResultTitleCount(t, fixture, "hooked", 1)
	if afterCommitCalls.Load() != 1 {
		t.Fatal("after-commit hook ran for rolled-back mutation")
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
			assertPostgresAfterCommitFailureReportsCommittedSuccess(t, profile)
		})
	}
}

func TestAfterCommitHookAndReporterPanicsPreserveCommittedMutationAndFact(t *testing.T) {
	var hookCalls, reports atomic.Int64
	fixture := newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		hook := golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error {
			hookCalls.Add(1)
			panic("after-commit hook panic")
		})
		return []golem.HookBinding[mutationResultActor]{hook}
	}, func(context.Context, golem.AfterCommitFailure) {
		reports.Add(1)
		panic("after-commit reporter panic")
	})
	ctx := context.Background()
	if _, err := CallerCreate(ctx, mustMutationResultCaller(t, fixture), fixture.postDescriptor, fixture.createPost(180, golem.UUID{15: 1}, "panic-committed")); err != nil {
		t.Fatalf("committed create returned an error after post-commit panic: %v", err)
	}
	if hookCalls.Load() != 1 || reports.Load() != 1 {
		t.Fatalf("after-commit hook calls=%d reports=%d", hookCalls.Load(), reports.Load())
	}
	assertMutationResultTitleCount(t, fixture, "panic-committed", 1)
	var facts int
	if err := fixture.app.database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	if facts != 1 {
		t.Fatalf("durable outbox facts=%d, want 1", facts)
	}
}

func assertPostgresAfterCommitFailureReportsCommittedSuccess(t *testing.T, profile postgresAcceptanceProfile) {
	t.Helper()
	ctx := context.Background()
	schema := schematest.NewSubscribedGraph(t)
	name := golem.GeneratedTextField[graphMutationUser, string](schema.UserName)
	createName := golem.GeneratedCreateFieldCapability(schema.User, name)
	afterCommitFailure := errors.New("postgres after commit observation failed")
	var before, after, afterCommit, reports atomic.Int64
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookRequest[graphMutationUser]](schema.User, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[graphMutationUser]) error {
			before.Add(1)
			return golem.SetCreate(request, createName, "pg-hooked")
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(hookContext context.Context, result golem.CreateHookResult[graphMutationUser]) error {
			after.Add(1)
			if value, ok := golem.Value(result.Row(), name).Get(); !ok || value != "pg-hooked" {
				return fmt.Errorf("postgres after name=%q present=%t", value, ok)
			}
			if hookContext.Value(mutationHookVeto) != nil {
				return errors.New("postgres after veto")
			}
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationUser]) error {
			afterCommit.Add(1)
			if value, ok := golem.Value(result.Row(), name).Get(); !ok || value != "pg-hooked" {
				return fmt.Errorf("postgres after-commit name=%q present=%t", value, ok)
			}
			return afterCommitFailure
		}),
	}
	fixture := newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, hooks)
	app, err := Open(ctx, withRuntimeTestEvents(t, Config[graphMutationPrincipal, graphMutationActor]{
		Database: p8RuntimeTestDatabase(fixture.app.database, golem.PostgreSQL), Bundle: fixture.schema.Bundle,
		Bindings: fixture.app.bindings, Descriptors: fixture.app.descriptors,
		ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
		AfterCommitError: func(_ context.Context, failure golem.AfterCommitFailure) {
			reports.Add(1)
			if failure.Operation() != golem.HookCreate || !errors.Is(failure.Cause(), afterCommitFailure) {
				t.Errorf("postgres after-commit report=%#v", failure)
			}
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	caller, err := app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	createUser := func(id byte, value string) golem.CreateInput[graphMutationUser] {
		return golem.GeneratedCreateInput[graphMutationUser](schema.User,
			golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(schema.User, fixture.userName, value),
		)
	}
	if _, err := CallerCreate(ctx, caller, fixture.userDescriptor, createUser(81, "original")); err != nil {
		t.Fatalf("postgres committed mutation reported failure: %v", err)
	}
	if before.Load() != 1 || after.Load() != 1 || afterCommit.Load() != 1 || reports.Load() != 1 {
		t.Fatalf("postgres calls before=%d after=%d afterCommit=%d reports=%d", before.Load(), after.Load(), afterCommit.Load(), reports.Load())
	}
	var committed int
	query := fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + nestedAcceptanceTable(fixture.app, schema.User) + ` WHERE "id" = ? AND "name" = ?`)
	if err := fixture.app.database.GetContext(ctx, &committed, query, mutationResultUUIDText(81), "pg-hooked"); err != nil || committed != 1 {
		t.Fatalf("postgres committed rows=%d err=%v", committed, err)
	}
	if _, err := CallerCreate(context.WithValue(ctx, mutationHookVeto, true), caller, fixture.userDescriptor, createUser(82, "veto")); err == nil {
		t.Fatal("postgres after veto unexpectedly committed")
	}
	if afterCommit.Load() != 1 || reports.Load() != 1 {
		t.Fatalf("postgres rolled-back mutation reached after-commit=%d reports=%d", afterCommit.Load(), reports.Load())
	}
}

func TestBeforeHookTransformsOwnedCloneThenRebinds(t *testing.T) {
	ctx := context.Background()
	fixture := newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultPost]) error {
				request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultPost](schema.Post, golem.GeneratedSetFieldValue(schema.Post, title, "owned-clone")))
				return nil
			}),
		}
	}, nil)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(37, golem.UUID{15: 1}, "before")); err != nil {
		t.Fatal(err)
	}
	original := fixture.updateTitle("caller-owned")
	beforeFreeze, err := golem.RuntimeFreezeUpdateInput(original)
	if err != nil {
		t.Fatal(err)
	}
	caller := mustMutationResultCaller(t, fixture)
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(37), original); err != nil {
		t.Fatal(err)
	}
	afterFreeze, err := golem.RuntimeFreezeUpdateInput(original)
	if err != nil {
		t.Fatal(err)
	}
	beforeFields, afterFields := beforeFreeze.Fields(), afterFreeze.Fields()
	if len(beforeFields) != 1 || len(afterFields) != 1 {
		t.Fatalf("owned input field counts before=%d after=%d", len(beforeFields), len(afterFields))
	}
	beforeValue, _ := beforeFields[0].Value()
	afterValue, _ := afterFields[0].Value()
	if beforeValue != "caller-owned" || afterValue != "caller-owned" {
		t.Fatalf("hook mutated caller-owned input before=%v after=%v", beforeValue, afterValue)
	}
	var persisted string
	if err := fixture.app.database.GetContext(ctx, &persisted, `SELECT "title" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(37)); err != nil {
		t.Fatal(err)
	}
	if persisted != "owned-clone" {
		t.Fatalf("transformed/rebound title=%q", persisted)
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
			assertPostgresBeforeHookTransformsOwnedCloneThenRebinds(t, profile)
		})
	}
}

func assertPostgresBeforeHookTransformsOwnedCloneThenRebinds(t *testing.T, profile postgresAcceptanceProfile) {
	t.Helper()
	ctx := context.Background()
	schema := schematest.NewSubscribedGraph(t)
	name := golem.GeneratedTextField[graphMutationUser, string](schema.UserName)
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.UpdateHookRequest[graphMutationUser]](schema.User, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[graphMutationUser]) error {
			request.ReplaceInput(golem.GeneratedUpdateInput[graphMutationUser](schema.User, golem.GeneratedSetFieldValue(schema.User, name, "pg-owned-clone")))
			return nil
		}),
	}
	fixture := newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, hooks)
	userID := golem.UUID{15: 71}
	create := golem.GeneratedCreateInput[graphMutationUser](schema.User,
		golem.GeneratedCreateFieldValue(schema.User, fixture.userID, userID),
		golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "before"),
	)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, create); err != nil {
		t.Fatal(err)
	}
	original := golem.GeneratedUpdateInput[graphMutationUser](schema.User, golem.GeneratedSetFieldValue(schema.User, fixture.userName, "caller-owned"))
	beforeFreeze, err := golem.RuntimeFreezeUpdateInput(original)
	if err != nil {
		t.Fatal(err)
	}
	target := golem.GeneratedUniqueSelectorValue[graphMutationUser](schema.User, schema.UserKey, golem.GeneratedSelectorComponent(schema.UserID, userID))
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, target, original); err != nil {
		t.Fatal(err)
	}
	afterFreeze, err := golem.RuntimeFreezeUpdateInput(original)
	if err != nil {
		t.Fatal(err)
	}
	beforeFields, afterFields := beforeFreeze.Fields(), afterFreeze.Fields()
	if len(beforeFields) != 1 || len(afterFields) != 1 {
		t.Fatalf("postgres owned input field counts before=%d after=%d", len(beforeFields), len(afterFields))
	}
	beforeValue, _ := beforeFields[0].Value()
	afterValue, _ := afterFields[0].Value()
	if beforeValue != "caller-owned" || afterValue != "caller-owned" {
		t.Fatalf("postgres hook mutated caller-owned input before=%v after=%v", beforeValue, afterValue)
	}
	var persisted string
	query := fixture.app.database.Rebind(`SELECT "name" FROM ` + nestedAcceptanceTable(fixture.app, schema.User) + ` WHERE "id" = ?`)
	if err := fixture.app.database.GetContext(ctx, &persisted, query, mutationResultUUIDText(71)); err != nil {
		t.Fatal(err)
	}
	if persisted != "pg-owned-clone" {
		t.Fatalf("postgres transformed/rebound name=%q", persisted)
	}
}

func TestTransactionBoundReadsRelationsLoadersNestedWritesAndHooks(t *testing.T) {
	ctx := context.Background()
	var fixture mutationResultFixture
	var calls atomic.Int64
	fixture = newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		posts := golem.GeneratedToMany[mutationResultUser, mutationResultPost](schema.UserPosts, schema.Authorship, schema.Post)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookResult[mutationResultPost]](schema.Post, golem.HookUpdate, func(hookContext context.Context, result golem.UpdateHookResult[mutationResultPost]) error {
				if calls.Add(1) != 1 {
					return fmt.Errorf("unexpected recursive post update hook")
				}
				rows, err := golem.HookFindManyRows(hookContext, result.Executor(), fixture.userDescriptor,
					golem.Where(fixture.userID.Eq(golem.UUID{15: 1})),
					golem.Select[mutationResultUser](fixture.userName, posts.Select(title)),
				)
				if err != nil {
					return err
				}
				if len(rows) != 1 {
					return fmt.Errorf("transaction-bound relation root rows=%d", len(rows))
				}
				children, present := golem.Many(rows[0], posts).Get()
				if !present || len(children) == 0 {
					return fmt.Errorf("transaction-bound relation loader children=%d present=%t", len(children), present)
				}
				post := golem.GeneratedCreateInput[mutationResultPost](schema.Post,
					golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: 51}),
					golem.GeneratedCreateFieldValue(schema.Post, title, "hook-nested-write"),
				)
				user := golem.GeneratedCreateInput[mutationResultUser](schema.User,
					golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 50}),
					golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "hook-user"),
					golem.GeneratedNestedCreate[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, post),
				)
				_, err = golem.HookCreateRow(hookContext, result.Executor(), fixture.userDescriptor, user)
				return err
			}),
		}
	}, nil)
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(49, golem.UUID{15: 1}, "before")); err != nil {
		t.Fatal(err)
	}
	caller := mustMutationResultCaller(t, fixture)
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(49), fixture.updateTitle("outer-after")); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("transaction-bound hook calls=%d", calls.Load())
	}
	var author, title string
	if err := fixture.app.database.QueryRowxContext(ctx, `SELECT "author_id", "title" FROM "posts" WHERE "id" = ?`, mutationResultUUIDText(51)).Scan(&author, &title); err != nil {
		t.Fatal(err)
	}
	if author != mutationResultUUIDText(50) || title != "hook-nested-write" {
		t.Fatalf("hook nested write author=%q title=%q", author, title)
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
			assertPostgresTransactionBoundReadsRelationsNestedWritesAndHooks(t, profile)
		})
	}
}

func assertPostgresTransactionBoundReadsRelationsNestedWritesAndHooks(t *testing.T, profile postgresAcceptanceProfile) {
	t.Helper()
	ctx := context.Background()
	schema := schematest.NewSubscribedGraph(t)
	var fixture graphMutationFixture
	var calls atomic.Int64
	posts := golem.GeneratedToMany[graphMutationUser, graphMutationPost](schema.UserPosts, schema.Authorship, schema.Post)
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(hookContext context.Context, result golem.CreateHookResult[graphMutationUser]) error {
			if calls.Add(1) != 1 {
				return nil
			}
			rows, err := golem.HookFindManyRows(hookContext, result.Executor(), fixture.userDescriptor,
				golem.Where(fixture.userID.Eq(golem.UUID{15: 61})),
				golem.Select[graphMutationUser](fixture.userName, posts.Select(fixture.postTitle)),
			)
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				return fmt.Errorf("postgres transaction-bound rows=%d", len(rows))
			}
			children, present := golem.Many(rows[0], posts).Get()
			if !present || len(children) != 1 {
				return fmt.Errorf("postgres relation-loader children=%d present=%t", len(children), present)
			}
			nestedPost := golem.GeneratedCreateInput[graphMutationPost](schema.Post,
				golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: 65}),
				golem.GeneratedCreateFieldValue(schema.Post, fixture.postTitle, "pg-hook-post"),
			)
			nestedUser := golem.GeneratedCreateInput[graphMutationUser](schema.User,
				golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 64}),
				golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "pg-hook-user"),
				golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, nestedPost),
			)
			_, err = golem.HookCreateRow(hookContext, result.Executor(), fixture.userDescriptor, nestedUser)
			return err
		}),
	}
	fixture = newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, hooks)
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerCreate(ctx, caller, fixture.userDescriptor, fixture.deepCreate(61, 62, 63)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("postgres transaction-bound nested hook calls=%d", calls.Load())
	}
	assertGraphMutationRowsAndFacts(t, fixture, 2, 2, 1, 5)
}

func TestUpsertHooksRepeatOnlyForEngineAttempts(t *testing.T) {
	ctx := contextWithUpsertAttemptFinishFault(context.Background(), func(ordinal uint32) error {
		if ordinal == 1 {
			return &pgconn.PgError{Code: "40001", Message: "injected provider serialization retry"}
		}
		return nil
	})
	var before, after, afterCommit atomic.Int64
	fixture := newMutationResultFixtureWithHooks(t, MutationLimits{MaxUpsertAttempts: 3}, func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		createTitle := golem.GeneratedCreateFieldCapability(schema.Post, title)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultPost]) error {
				before.Add(1)
				return golem.SetCreate(request, createTitle, "retry-success")
			}),
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error {
				after.Add(1)
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error {
				afterCommit.Add(1)
				return nil
			}),
		}
	}, func(context.Context, golem.AfterCommitFailure) {})
	caller := mustMutationResultCaller(t, fixture)
	if _, err := CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(52), fixture.createPost(52, golem.UUID{15: 1}, "untransformed"), fixture.updateTitle("unused")); err != nil {
		t.Fatal(err)
	}
	// The provider fault is injected at finish, after each attempt's
	// transaction-after phase. P4 permits both Before and transaction-after to
	// repeat for an engine-owned retry; only AfterCommit is committed-once.
	if before.Load() != 2 || after.Load() != 2 || afterCommit.Load() != 1 {
		t.Fatalf("upsert attempt hook calls before=%d after=%d afterCommit=%d", before.Load(), after.Load(), afterCommit.Load())
	}
	var count int
	if err := fixture.app.database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "posts" WHERE "id" = ? AND "title" = ?`, mutationResultUUIDText(52), "retry-success"); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("upsert retry committed rows=%d", count)
	}
	for _, profile := range postgresAcceptanceProfiles() {
		if profile.name != "c" {
			continue
		}
		profile := profile
		t.Run("postgresql-c-live-committed-attempt", func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			assertPostgresUpsertHooksRunOnceForCommittedAttempt(t, profile)
		})
	}
}

func assertPostgresUpsertHooksRunOnceForCommittedAttempt(t *testing.T, profile postgresAcceptanceProfile) {
	t.Helper()
	ctx := context.Background()
	schema := schematest.NewSubscribedGraph(t)
	name := golem.GeneratedTextField[graphMutationUser, string](schema.UserName)
	createName := golem.GeneratedCreateFieldCapability(schema.User, name)
	var before, after, afterCommit atomic.Int64
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookRequest[graphMutationUser]](schema.User, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[graphMutationUser]) error {
			before.Add(1)
			return golem.SetCreate(request, createName, "pg-upsert-hooked")
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationUser]) error {
			after.Add(1)
			if value, ok := golem.Value(result.Row(), name).Get(); !ok || value != "pg-upsert-hooked" {
				return fmt.Errorf("postgres upsert after name=%q present=%t", value, ok)
			}
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[graphMutationUser]) error {
			afterCommit.Add(1)
			if value, ok := golem.Value(result.Row(), name).Get(); !ok || value != "pg-upsert-hooked" {
				return fmt.Errorf("postgres upsert after-commit name=%q present=%t", value, ok)
			}
			return nil
		}),
	}
	fixture := newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, hooks)
	userID := golem.UUID{15: 72}
	target := golem.GeneratedUniqueSelectorValue[graphMutationUser](schema.User, schema.UserKey, golem.GeneratedSelectorComponent(schema.UserID, userID))
	create := golem.GeneratedCreateInput[graphMutationUser](schema.User,
		golem.GeneratedCreateFieldValue(schema.User, fixture.userID, userID),
		golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "untransformed"),
	)
	update := golem.GeneratedUpdateInput[graphMutationUser](schema.User, golem.GeneratedSetFieldValue(schema.User, fixture.userName, "unused"))
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CallerUpsert(ctx, caller, fixture.userDescriptor, target, create, update); err != nil {
		t.Fatal(err)
	}
	if before.Load() != 1 || after.Load() != 1 || afterCommit.Load() != 1 {
		t.Fatalf("postgres committed upsert hook calls before=%d after=%d afterCommit=%d", before.Load(), after.Load(), afterCommit.Load())
	}
	var count int
	query := fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + nestedAcceptanceTable(fixture.app, schema.User) + ` WHERE "id" = ? AND "name" = ?`)
	if err := fixture.app.database.GetContext(ctx, &count, query, mutationResultUUIDText(72), "pg-upsert-hooked"); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("postgres committed upsert rows=%d", count)
	}
}

func TestSystemMutationsBypassAllHooks(t *testing.T) {
	var calls atomic.Int64
	fixture := newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		before := golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[mutationResultPost]) error {
			calls.Add(1)
			return errors.New("must bypass")
		})
		after := golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error {
			calls.Add(1)
			return errors.New("must bypass")
		})
		return []golem.HookBinding[mutationResultActor]{before, after}
	}, func(context.Context, golem.AfterCommitFailure) {})
	if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.postDescriptor, fixture.createPost(83, golem.UUID{15: 1}, "system")); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("system invoked %d hooks", calls.Load())
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
			assertPostgresSystemMutationsBypassAllHooks(t, profile)
		})
	}
}

func TestDeleteAndDeleteManyHookFamiliesAcrossProviders(t *testing.T) {
	schema := schematest.NewSubscribedGraph(t)
	t.Run("sqlite", func(t *testing.T) {
		hooks, counters := graphDeleteHookBindings(schema)
		assertGraphDeleteHookFamilies(t, newGraphMutationFixtureWithHooks(t, schema, golem.ModelID{}, hooks), counters)
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
			hooks, counters := graphDeleteHookBindings(schema)
			assertGraphDeleteHookFamilies(t, newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, hooks), counters)
		})
	}
}

type graphDeleteHookCounters struct {
	deleteBefore, deleteAfter, deleteCommit             atomic.Int64
	deleteManyBefore, deleteManyAfter, deleteManyCommit atomic.Int64
}

func graphDeleteHookBindings(schema schematest.GraphFixture) ([]golem.HookBinding[graphMutationActor], *graphDeleteHookCounters) {
	counters := &graphDeleteHookCounters{}
	postID := golem.GeneratedEqualField[graphMutationPost, golem.UUID](schema.PostID)
	postTitle := golem.GeneratedTextField[graphMutationPost, string](schema.PostTitle)
	target := func(id byte) golem.MutationTarget[graphMutationPost] {
		return golem.GeneratedUniqueSelectorValue[graphMutationPost](schema.Post, schema.PostKey,
			golem.GeneratedSelectorComponent(schema.PostID, golem.UUID{15: id}))
	}
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.DeleteHookRequest[graphMutationPost]](schema.Post, golem.HookDelete, func(ctx context.Context, request *golem.DeleteHookRequest[graphMutationPost]) error {
			counters.deleteBefore.Add(1)
			if ctx.Value(mutationHookVeto) == nil {
				request.ReplaceTarget(target(212))
			}
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationPost, golem.DeleteHookResult[graphMutationPost]](schema.Post, golem.HookDelete, func(ctx context.Context, result golem.DeleteHookResult[graphMutationPost]) error {
			counters.deleteAfter.Add(1)
			if ctx.Value(mutationHookVeto) != nil {
				return errors.New("delete after veto")
			}
			if id, ok := golem.Value(result.Before(), postID).Get(); !ok || id != (golem.UUID{15: 212}) {
				return fmt.Errorf("delete before snapshot id=%v present=%t", id, ok)
			}
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationPost, golem.DeleteHookResult[graphMutationPost]](schema.Post, golem.HookDelete, func(context.Context, golem.DeleteHookResult[graphMutationPost]) error {
			counters.deleteCommit.Add(1)
			return nil
		}),
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.DeleteManyHookRequest[graphMutationPost]](schema.Post, golem.HookDeleteMany, func(ctx context.Context, request *golem.DeleteManyHookRequest[graphMutationPost]) error {
			counters.deleteManyBefore.Add(1)
			if ctx.Value(mutationHookVeto) == nil {
				request.ReplaceWhere(postTitle.Eq("batch-transform"))
			}
			return nil
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationPost, golem.DeleteManyHookResult[graphMutationPost]](schema.Post, golem.HookDeleteMany, func(ctx context.Context, result golem.DeleteManyHookResult[graphMutationPost]) error {
			counters.deleteManyAfter.Add(1)
			if ctx.Value(mutationHookVeto) != nil {
				return errors.New("deleteMany after veto")
			}
			if result.Count() != 2 {
				return fmt.Errorf("deleteMany transformed count=%d", result.Count())
			}
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationPost, golem.DeleteManyHookResult[graphMutationPost]](schema.Post, golem.HookDeleteMany, func(context.Context, golem.DeleteManyHookResult[graphMutationPost]) error {
			counters.deleteManyCommit.Add(1)
			return nil
		}),
	}
	return hooks, counters
}

func assertGraphDeleteHookFamilies(t testing.TB, fixture graphMutationFixture, counters *graphDeleteHookCounters) {
	t.Helper()
	ctx := context.Background()
	create := func(userID, postID byte, title string) {
		t.Helper()
		post := golem.GeneratedCreateInput[graphMutationPost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: postID}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, title),
		)
		user := golem.GeneratedCreateInput[graphMutationUser](fixture.schema.User,
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: userID}),
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, fmt.Sprintf("user-%d", userID)),
			golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, post),
		)
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, user); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		user, post byte
		title      string
	}{
		{201, 211, "requested"}, {202, 212, "transformed"}, {203, 213, "veto"}, {204, 214, "system"},
		{221, 221, "batch-transform"}, {222, 222, "batch-transform"}, {223, 223, "batch-veto"}, {224, 224, "batch-system"},
	} {
		create(row.user, row.post, row.title)
	}
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	target := func(id byte) golem.MutationTarget[graphMutationPost] {
		return golem.GeneratedUniqueSelectorValue[graphMutationPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: id}))
	}
	if _, err := CallerDelete(ctx, caller, fixture.postDescriptor, target(211)); err != nil {
		t.Fatal(err)
	}
	if _, err := CallerDelete(context.WithValue(ctx, mutationHookVeto, true), caller, fixture.postDescriptor, target(213)); err == nil {
		t.Fatal("delete after veto unexpectedly committed")
	}
	if _, err := SystemDelete(ctx, fixture.app.System(), fixture.postDescriptor, target(214)); err != nil {
		t.Fatal(err)
	}
	if count, err := CallerDeleteMany(ctx, caller, fixture.postDescriptor, fixture.postTitle.Eq("ignored")); err != nil || count != 2 {
		t.Fatalf("transformed deleteMany count=%d err=%v", count, err)
	}
	if _, err := CallerDeleteMany(context.WithValue(ctx, mutationHookVeto, true), caller, fixture.postDescriptor, fixture.postTitle.Eq("batch-veto")); err == nil {
		t.Fatal("deleteMany after veto unexpectedly committed")
	}
	if count, err := SystemDeleteMany(ctx, fixture.app.System(), fixture.postDescriptor, fixture.postTitle.Eq("batch-system")); err != nil || count != 1 {
		t.Fatalf("system deleteMany count=%d err=%v", count, err)
	}
	if counters.deleteBefore.Load() != 2 || counters.deleteAfter.Load() != 2 || counters.deleteCommit.Load() != 1 {
		t.Fatalf("delete hook calls before=%d after=%d commit=%d", counters.deleteBefore.Load(), counters.deleteAfter.Load(), counters.deleteCommit.Load())
	}
	if counters.deleteManyBefore.Load() != 2 || counters.deleteManyAfter.Load() != 2 || counters.deleteManyCommit.Load() != 1 {
		t.Fatalf("deleteMany hook calls before=%d after=%d commit=%d", counters.deleteManyBefore.Load(), counters.deleteManyAfter.Load(), counters.deleteManyCommit.Load())
	}
	posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	for _, row := range []struct {
		id   byte
		want int
	}{{211, 1}, {212, 0}, {213, 1}, {214, 0}, {221, 0}, {222, 0}, {223, 1}, {224, 0}} {
		var count int
		query := fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + posts + ` WHERE "id"=?`)
		if err := fixture.app.database.GetContext(ctx, &count, query, mutationResultUUIDText(row.id)); err != nil || count != row.want {
			t.Fatalf("post %d rows=%d want=%d err=%v", row.id, count, row.want, err)
		}
	}
}

func assertPostgresSystemMutationsBypassAllHooks(t *testing.T, profile postgresAcceptanceProfile) {
	t.Helper()
	schema := schematest.NewSubscribedGraph(t)
	var calls atomic.Int64
	hooks := []golem.HookBinding[graphMutationActor]{
		golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookRequest[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationUser]) error {
			calls.Add(1)
			return errors.New("postgres system must bypass before")
		}),
		golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
			calls.Add(1)
			return errors.New("postgres system must bypass after")
		}),
		golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
			calls.Add(1)
			return errors.New("postgres system must bypass after-commit")
		}),
	}
	fixture := newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, hooks)
	input := golem.GeneratedCreateInput[graphMutationUser](schema.User,
		golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 83}),
		golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "pg-system"),
	)
	if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.userDescriptor, input); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("postgres system invoked %d hooks", calls.Load())
	}
}

func TestBatchMutationHooksTransformVetoAfterCommitAndSystemBypass(t *testing.T) {
	ctx := context.Background()
	var beforeCalls, afterCalls, afterCommitCalls atomic.Int64
	fixture := newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		before := golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateManyHookRequest[mutationResultPost]](schema.Post, golem.HookUpdateMany, func(_ context.Context, request *golem.UpdateManyHookRequest[mutationResultPost]) error {
			beforeCalls.Add(1)
			request.ReplaceInput(golem.GeneratedUpdateManyInput[mutationResultPost](schema.Post, golem.GeneratedSetFieldValue(schema.Post, title, "batch-hooked")))
			return nil
		})
		after := golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.UpdateManyHookResult[mutationResultPost]](schema.Post, golem.HookUpdateMany, func(hookContext context.Context, result golem.UpdateManyHookResult[mutationResultPost]) error {
			afterCalls.Add(1)
			if result.Count() != 2 && hookContext.Value(mutationHookVeto) == nil {
				t.Fatalf("after batch count=%d", result.Count())
			}
			if hookContext.Value(mutationHookVeto) != nil {
				return errors.New("batch after veto")
			}
			return nil
		})
		afterCommit := golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.UpdateManyHookResult[mutationResultPost]](schema.Post, golem.HookUpdateMany, func(_ context.Context, result golem.UpdateManyHookResult[mutationResultPost]) error {
			afterCommitCalls.Add(1)
			if result.Count() != 2 {
				t.Fatalf("after-commit batch count=%d", result.Count())
			}
			return nil
		})
		return []golem.HookBinding[mutationResultActor]{before, after, afterCommit}
	}, func(context.Context, golem.AfterCommitFailure) {})
	for _, row := range []struct {
		id    byte
		title string
	}{{91, "batch-before"}, {92, "batch-before"}, {93, "batch-veto"}, {94, "batch-system"}} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(row.id, golem.UUID{15: 1}, row.title)); err != nil {
			t.Fatal(err)
		}
	}
	caller := mustMutationResultCaller(t, fixture)
	count, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 91}, golem.UUID{15: 92}), fixture.updateManyTitle("ignored"))
	if err != nil || count != 2 {
		t.Fatalf("hooked batch count=%d err=%v cause=%v", count, err, errors.Unwrap(err))
	}
	assertMutationResultTitleCount(t, fixture, "batch-hooked", 2)
	if beforeCalls.Load() != 1 || afterCalls.Load() != 1 || afterCommitCalls.Load() != 1 {
		t.Fatalf("batch calls before=%d after=%d afterCommit=%d", beforeCalls.Load(), afterCalls.Load(), afterCommitCalls.Load())
	}

	count, err = CallerUpdateMany(context.WithValue(ctx, mutationHookVeto, true), caller, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 93}), fixture.updateManyTitle("ignored"))
	var failure *golem.Error
	if count != 0 || !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
		t.Fatalf("batch veto count=%d failure=%#v err=%v", count, failure, err)
	}
	assertMutationResultTitleCount(t, fixture, "batch-veto", 1)
	if afterCommitCalls.Load() != 1 {
		t.Fatal("batch after-commit ran for rolled-back mutation")
	}

	count, err = SystemUpdateMany(ctx, fixture.app.System(), fixture.postDescriptor, fixture.title.Eq("batch-system"), fixture.updateManyTitle("system-bypassed"))
	if err != nil || count != 1 {
		t.Fatalf("system batch count=%d err=%v", count, err)
	}
	if beforeCalls.Load() != 2 || afterCalls.Load() != 2 || afterCommitCalls.Load() != 1 {
		t.Fatal("system batch invoked caller hooks")
	}
}

func TestUpsertRunsOnlySelectedBranchHookFamily(t *testing.T) {
	ctx := context.Background()
	var createBefore, createAfter, createCommit atomic.Int64
	var updateBefore, updateAfter, updateCommit atomic.Int64
	fixture := newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		createField := golem.GeneratedCreateFieldCapability(schema.Post, title)
		beforeCreate := golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultPost]) error {
			createBefore.Add(1)
			return golem.SetCreate(request, createField, "upsert-created-hook")
		})
		afterCreate := golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(_ context.Context, result golem.CreateHookResult[mutationResultPost]) error {
			createAfter.Add(1)
			if value, ok := golem.Value(result.Row(), title).Get(); !ok || value != "upsert-created-hook" {
				t.Fatalf("upsert create after title=%q present=%t", value, ok)
			}
			return nil
		})
		commitCreate := golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error {
			createCommit.Add(1)
			return nil
		})
		beforeUpdate := golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultPost]) error {
			updateBefore.Add(1)
			request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultPost](schema.Post, golem.GeneratedSetFieldValue(schema.Post, title, "upsert-updated-hook")))
			return nil
		})
		afterUpdate := golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookResult[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, result golem.UpdateHookResult[mutationResultPost]) error {
			updateAfter.Add(1)
			if value, ok := golem.Value(result.After(), title).Get(); !ok || value != "upsert-updated-hook" {
				t.Fatalf("upsert update after title=%q present=%t", value, ok)
			}
			return nil
		})
		commitUpdate := golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookResult[mutationResultPost]](schema.Post, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[mutationResultPost]) error {
			updateCommit.Add(1)
			return nil
		})
		return []golem.HookBinding[mutationResultActor]{beforeCreate, afterCreate, commitCreate, beforeUpdate, afterUpdate, commitUpdate}
	}, func(context.Context, golem.AfterCommitFailure) {})
	caller := mustMutationResultCaller(t, fixture)
	if _, err := CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(95), fixture.createPost(95, golem.UUID{15: 1}, "ignored-create"), fixture.updateTitle("wrong-update")); err != nil {
		t.Fatal(err)
	}
	if createBefore.Load() != 1 || createAfter.Load() != 1 || createCommit.Load() != 1 || updateBefore.Load() != 0 || updateAfter.Load() != 0 || updateCommit.Load() != 0 {
		t.Fatalf("create branch calls create=%d/%d/%d update=%d/%d/%d", createBefore.Load(), createAfter.Load(), createCommit.Load(), updateBefore.Load(), updateAfter.Load(), updateCommit.Load())
	}
	assertMutationResultTitleCount(t, fixture, "upsert-created-hook", 1)

	if _, err := CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(95), fixture.createPost(95, golem.UUID{15: 1}, "wrong-create"), fixture.updateTitle("ignored-update")); err != nil {
		t.Fatal(err)
	}
	if createBefore.Load() != 1 || createAfter.Load() != 1 || createCommit.Load() != 1 || updateBefore.Load() != 1 || updateAfter.Load() != 1 || updateCommit.Load() != 1 {
		t.Fatalf("update branch calls create=%d/%d/%d update=%d/%d/%d", createBefore.Load(), createAfter.Load(), createCommit.Load(), updateBefore.Load(), updateAfter.Load(), updateCommit.Load())
	}
	assertMutationResultTitleCount(t, fixture, "upsert-updated-hook", 1)
}

func TestTransactionAfterHookUsesOpaqueSameTransactionExecutor(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int64
	var batchBeforeCalls atomic.Int64
	var fixture mutationResultFixture
	fixture = newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		userIDField := golem.GeneratedEqualField[mutationResultUser, golem.UUID](schema.UserID)
		userNameField := golem.GeneratedTextField[mutationResultUser, string](schema.UserName)
		beforeUserBatch := golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultUser, golem.UpdateManyHookRequest[mutationResultUser]](schema.User, golem.HookUpdateMany, func(_ context.Context, request *golem.UpdateManyHookRequest[mutationResultUser]) error {
			batchBeforeCalls.Add(1)
			request.ReplaceInput(golem.GeneratedUpdateManyInput[mutationResultUser](schema.User, golem.GeneratedSetFieldValue(schema.User, userNameField, "same-tx-batch")))
			return nil
		})
		after := golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(hookContext context.Context, result golem.CreateHookResult[mutationResultPost]) error {
			calls.Add(1)
			innerID := byte(102)
			if hookContext.Value(mutationHookVeto) != nil {
				innerID = 104
			}
			id := golem.UUID{15: innerID}
			input := golem.GeneratedCreateInput[mutationResultUser](schema.User,
				golem.GeneratedCreateFieldValue(schema.User, userIDField, id),
				golem.GeneratedCreateFieldValue(schema.User, userNameField, "same-tx-inner"),
			)
			selector := golem.GeneratedUniqueSelectorValue[mutationResultUser](schema.User, schema.UserKey, golem.GeneratedSelectorComponent(schema.UserID, id))
			update := golem.GeneratedUpdateInput[mutationResultUser](schema.User, golem.GeneratedSetFieldValue(schema.User, userNameField, "same-tx-updated"))
			row, err := golem.HookUpsertRow(hookContext, result.Executor(), fixture.userDescriptor, selector, input, update)
			if err != nil {
				return err
			}
			if value, ok := golem.Value(row, userNameField).Get(); !ok || value != "same-tx-inner" {
				return errors.New("hook-started create did not return its complete row")
			}
			count, err := golem.HookUpdateManyRows(hookContext, result.Executor(), fixture.userDescriptor, userIDField.Eq(id), golem.GeneratedUpdateManyInput[mutationResultUser](schema.User, golem.GeneratedSetFieldValue(schema.User, userNameField, "ignored")))
			if err != nil || count != 1 {
				return errors.Join(err, fmt.Errorf("hook-started batch count=%d", count))
			}
			rows, err := golem.HookFindManyRows(hookContext, result.Executor(), fixture.userDescriptor, golem.Where(userIDField.Eq(id)), golem.Select[mutationResultUser](userNameField))
			if err != nil || len(rows) != 1 {
				return errors.Join(err, errors.New("hook-started read did not observe the uncommitted write"))
			}
			if value, ok := golem.Value(rows[0], userNameField).Get(); !ok || value != "same-tx-batch" {
				return errors.New("hook-started batch before hook was bypassed")
			}
			if hookContext.Value(mutationHookVeto) != nil {
				return errors.New("rollback outer and hook-started writes")
			}
			return nil
		})
		return []golem.HookBinding[mutationResultActor]{beforeUserBatch, after}
	}, nil)
	caller := mustMutationResultCaller(t, fixture)
	if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, fixture.createPost(101, golem.UUID{15: 1}, "same-tx-outer")); err != nil {
		t.Fatal(err)
	}
	assertMutationResultTitleCount(t, fixture, "same-tx-outer", 1)
	assertMutationResultUserNameCount(t, fixture, "same-tx-batch", 1)
	if calls.Load() != 1 {
		t.Fatalf("same-tx hook calls=%d want=1", calls.Load())
	}
	if batchBeforeCalls.Load() != 1 {
		t.Fatalf("same-tx batch before calls=%d want=1", batchBeforeCalls.Load())
	}

	_, err := CallerCreate(context.WithValue(ctx, mutationHookVeto, true), caller, fixture.postDescriptor, fixture.createPost(103, golem.UUID{15: 1}, "same-tx-veto"))
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
		t.Fatalf("same-tx veto failure=%#v err=%v", failure, err)
	}
	assertMutationResultTitleCount(t, fixture, "same-tx-veto", 0)
	// The hook creates the same primary identity on the veto path, proving it
	// was rolled back with the outer operation rather than committed elsewhere.
	assertMutationResultUserNameCount(t, fixture, "same-tx-batch", 1)
	if batchBeforeCalls.Load() != 2 {
		t.Fatalf("veto same-tx batch before calls=%d want=2", batchBeforeCalls.Load())
	}
}

func TestAfterCommitRunsOnlyAfterOutermostCommit(t *testing.T) {
	ctx := context.Background()
	var afterCommitCalls atomic.Int64
	fixture := newMutationResultFixtureWithHooks(t, MutationLimits{}, func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string]) []golem.HookBinding[mutationResultActor] {
		hook := golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error {
			afterCommitCalls.Add(1)
			return nil
		})
		return []golem.HookBinding[mutationResultActor]{hook}
	}, func(context.Context, golem.AfterCommitFailure) {})
	caller := mustMutationResultCaller(t, fixture)
	err := CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		if _, err := CallerTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(111, golem.UUID{15: 1}, "outer-commit")); err != nil {
			return err
		}
		if afterCommitCalls.Load() != 0 {
			return errors.New("after-commit ran at an inner mutation/savepoint boundary")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if afterCommitCalls.Load() != 1 {
		t.Fatalf("after outer commit calls=%d", afterCommitCalls.Load())
	}

	rollback := errors.New("outer rollback")
	err = CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
		if _, err := CallerTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(112, golem.UUID{15: 1}, "outer-rollback")); err != nil {
			return err
		}
		if afterCommitCalls.Load() != 1 {
			return errors.New("after-commit ran before outer rollback")
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("outer rollback err=%v", err)
	}
	if afterCommitCalls.Load() != 1 {
		t.Fatalf("rolled-back after-commit calls=%d", afterCommitCalls.Load())
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
			assertPostgresAfterCommitRunsOnlyAfterOutermostCommit(t, profile)
		})
	}
}

func assertPostgresAfterCommitRunsOnlyAfterOutermostCommit(t *testing.T, profile postgresAcceptanceProfile) {
	t.Helper()
	ctx := context.Background()
	schema := schematest.NewSubscribedGraph(t)
	var calls atomic.Int64
	hook := golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
		calls.Add(1)
		return nil
	})
	fixture := newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, []golem.HookBinding[graphMutationActor]{hook})
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	createUser := func(id byte, name string) golem.CreateInput[graphMutationUser] {
		return golem.GeneratedCreateInput[graphMutationUser](schema.User,
			golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(schema.User, fixture.userName, name),
		)
	}
	err = CallerTransaction(ctx, caller, func(transaction *CallerTx[graphMutationPrincipal, graphMutationActor]) error {
		if _, err := CallerTxCreate(ctx, transaction, fixture.userDescriptor, createUser(111, "pg-outer-commit")); err != nil {
			return err
		}
		if calls.Load() != 0 {
			return errors.New("postgres after-commit ran at inner savepoint")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("postgres after outer commit calls=%d", calls.Load())
	}
	rollback := errors.New("postgres outer rollback")
	err = CallerTransaction(ctx, caller, func(transaction *CallerTx[graphMutationPrincipal, graphMutationActor]) error {
		if _, err := CallerTxCreate(ctx, transaction, fixture.userDescriptor, createUser(112, "pg-outer-rollback")); err != nil {
			return err
		}
		if calls.Load() != 1 {
			return errors.New("postgres after-commit ran before outer rollback")
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("postgres outer rollback err=%v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("postgres rolled-back after-commit calls=%d", calls.Load())
	}
}

func assertMutationResultUserNameCount(t testing.TB, fixture mutationResultFixture, name string, want int) {
	t.Helper()
	var count int
	if err := fixture.app.database.GetContext(context.Background(), &count, "SELECT COUNT(*) FROM users WHERE name = ?", name); err != nil || count != want {
		t.Fatalf("user name %q count=%d want=%d err=%v", name, count, want, err)
	}
}

func mustMutationResultCaller(t testing.TB, fixture mutationResultFixture) *Caller[mutationResultPrincipal, mutationResultActor] {
	t.Helper()
	caller, err := fixture.app.ForPrincipal(context.Background(), mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	return caller
}
