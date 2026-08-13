package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type optimisticConcurrencyNestedHookProbe struct {
	inject          atomic.Bool
	rootBefore      atomic.Int64
	rootAfter       atomic.Int64
	rootAfterCommit atomic.Int64
	versionedBefore atomic.Int64
	versionedAfter  atomic.Int64
	versionedCommit atomic.Int64
}

func (probe *optimisticConcurrencyNestedHookProbe) reset() {
	probe.inject.Store(false)
	probe.rootBefore.Store(0)
	probe.rootAfter.Store(0)
	probe.rootAfterCommit.Store(0)
	probe.versionedBefore.Store(0)
	probe.versionedAfter.Store(0)
	probe.versionedCommit.Store(0)
}

func (probe *optimisticConcurrencyNestedHookProbe) assertNoHooks(t testing.TB) {
	t.Helper()
	if rootBefore, rootAfter, rootCommit := probe.rootBefore.Load(), probe.rootAfter.Load(), probe.rootAfterCommit.Load(); rootBefore != 0 || rootAfter != 0 || rootCommit != 0 {
		t.Fatalf("refused nested request ran root hooks: before=%d after=%d afterCommit=%d", rootBefore, rootAfter, rootCommit)
	}
	if before, after, commit := probe.versionedBefore.Load(), probe.versionedAfter.Load(), probe.versionedCommit.Load(); before != 0 || after != 0 || commit != 0 {
		t.Fatalf("refused nested request ran versioned-row hooks: before=%d after=%d afterCommit=%d", before, after, commit)
	}
}

// TestOptimisticConcurrencyNestedEveryWrittenRowRequiresExpectation pins the
// model-erased nested boundary. The generated v1 nested grammar cannot carry
// an exact expectation for each existing Post owner, so neither Caller nor
// System may reach hooks, SQL, or fact emission through a non-versioned User
// root. Creates are the deliberate exception: the runtime owns their initial
// token and stores exactly 1.
func TestOptimisticConcurrencyNestedEveryWrittenRowRequiresExpectation(t *testing.T) {
	t.Run("existing-versioned-owner-refuses-before-hooks-sql-and-facts", func(t *testing.T) {
		ctx := context.Background()
		database, counts := openMutationBoundarySQLite(t)
		schema := schematest.NewOptimisticConcurrency(t)
		if err := sqlite.New().ApplyInitial(ctx, database, schema.SQLite); err != nil {
			t.Fatal(err)
		}
		probe := &optimisticConcurrencyNestedHookProbe{}
		fixture := openOptimisticConcurrencyNestedFixture(t, database, schema, probe)
		seedOptimisticConcurrencyNestedRows(t, fixture)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)

		postTarget := func(id byte) golem.MutationTarget[mutationResultPost] {
			return golem.GeneratedUniqueSelectorValue[mutationResultPost](schema.Post, schema.PostKey,
				golem.GeneratedSelectorComponent(schema.PostID, golem.UUID{15: id}))
		}
		decimal := optimisticConcurrencyNestedDecimal(t, "1.25")
		postCreate := func(id byte, title string) golem.CreateInput[mutationResultPost] {
			return golem.GeneratedCreateInput[mutationResultPost](schema.Post,
				golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: id}),
				golem.GeneratedCreateFieldValue(schema.Post, fixture.title, title),
				golem.GeneratedCreateFieldValue(schema.Post, golem.GeneratedEqualField[mutationResultPost, golem.Decimal](schema.PostDecimal), decimal),
			)
		}
		userTarget := func(id byte) golem.MutationTarget[mutationResultUser] {
			return golem.GeneratedUniqueSelectorValue[mutationResultUser](schema.User, schema.UserKey,
				golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: id}))
		}
		updateMany := golem.GeneratedUpdateManyInput[mutationResultPost](schema.Post,
			golem.GeneratedSetFieldValue(schema.Post, fixture.title, "must-not-update"))

		inverse := []struct {
			name  string
			owner byte
			value golem.NestedUpdateValue[mutationResultUser]
		}{
			{"inverse-update", 1, golem.GeneratedNestedUpdate[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, postTarget(11), fixture.updateTitle("must-not-update"))},
			{"inverse-delete", 1, golem.GeneratedNestedDelete[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, postTarget(11))},
			{"inverse-upsert", 1, golem.GeneratedNestedUpsert[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, postTarget(11), postCreate(11, "must-not-create"), fixture.updateTitle("must-not-upsert"))},
			{"inverse-update-many", 1, golem.GeneratedNestedUpdateMany[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, fixture.title.Eq("seed-11"), updateMany)},
			{"inverse-delete-many", 1, golem.GeneratedNestedDeleteMany[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, fixture.title.Eq("seed-11"))},
			{"inverse-connect", 2, golem.GeneratedNestedConnect[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, postTarget(11))},
			{"inverse-disconnect", 1, golem.GeneratedNestedDisconnect[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, postTarget(11))},
			{"inverse-set", 2, golem.GeneratedNestedSet[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, postTarget(11))},
		}
		for _, operation := range inverse {
			operation := operation
			for _, stance := range []string{"caller", "system"} {
				stance := stance
				t.Run(operation.name+"-"+stance, func(t *testing.T) {
					probe.reset()
					counts.reset()
					input := golem.GeneratedUpdateInput[mutationResultUser](schema.User, operation.value)
					var err error
					if stance == "caller" {
						_, err = CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget(operation.owner), input)
					} else {
						_, err = SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, userTarget(operation.owner), input)
					}
					assertOptimisticConcurrencyNestedRefusal(t, err, true)
					probe.assertNoHooks(t)
					assertOptimisticConcurrencyNestedNoSQL(t, counts)
					assertOptimisticConcurrencyNestedState(t, fixture, 0)
				})
			}
		}

		// Source-side relation values write the Post root itself. The ordinary
		// root ABI has no expectation slot, so it must fail closed before the
		// application hook/SQL boundary just as the inverse owner paths do.
		source := []struct {
			name  string
			value golem.NestedUpdateValue[mutationResultPost]
		}{
			{"source-connect", golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](schema.Post, schema.PostAuthor, schema.Authorship, schema.User, userTarget(2))},
			{"source-disconnect", golem.GeneratedNestedDisconnect[mutationResultPost, mutationResultUser](schema.Post, schema.PostAuthor, schema.Authorship, schema.User, userTarget(1))},
			{"source-set", golem.GeneratedNestedSet[mutationResultPost, mutationResultUser](schema.Post, schema.PostAuthor, schema.Authorship, schema.User, userTarget(2))},
		}
		for _, operation := range source {
			operation := operation
			for _, stance := range []string{"caller", "system"} {
				stance := stance
				t.Run(operation.name+"-"+stance, func(t *testing.T) {
					probe.reset()
					counts.reset()
					input := golem.GeneratedUpdateInput[mutationResultPost](schema.Post, operation.value)
					var err error
					if stance == "caller" {
						_, err = CallerUpdate(ctx, caller, fixture.postDescriptor, postTarget(11), input)
					} else {
						_, err = SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, postTarget(11), input)
					}
					assertOptimisticConcurrencyNestedRefusal(t, err, false)
					probe.assertNoHooks(t)
					assertOptimisticConcurrencyNestedNoSQL(t, counts)
					assertOptimisticConcurrencyNestedState(t, fixture, 0)
				})
			}
		}

		t.Run("caller-before-hook-cannot-inject-owner-write", func(t *testing.T) {
			probe.reset()
			probe.inject.Store(true)
			counts.reset()
			input := golem.GeneratedUpdateInput[mutationResultUser](schema.User,
				golem.GeneratedSetFieldValue(schema.User, fixture.userName, "must-not-update"))
			_, err := CallerUpdate(ctx, caller, fixture.userDescriptor, userTarget(2), input)
			assertOptimisticConcurrencyNestedRefusal(t, err, true)
			if before := probe.rootBefore.Load(); before != 1 {
				t.Fatalf("hook injection before calls=%d want=1", before)
			}
			if rootAfter, rootCommit := probe.rootAfter.Load(), probe.rootAfterCommit.Load(); rootAfter != 0 || rootCommit != 0 {
				t.Fatalf("hook injection ran post-write root hooks: after=%d afterCommit=%d", rootAfter, rootCommit)
			}
			if before, after, commit := probe.versionedBefore.Load(), probe.versionedAfter.Load(), probe.versionedCommit.Load(); before != 0 || after != 0 || commit != 0 {
				t.Fatalf("hook injection ran versioned hooks: before=%d after=%d afterCommit=%d", before, after, commit)
			}
			assertOptimisticConcurrencyNestedNoSQL(t, counts)
			assertOptimisticConcurrencyNestedState(t, fixture, 0)
		})
	})

	for _, stance := range []string{"caller", "system"} {
		stance := stance
		t.Run("nested-create-initializes-one-"+stance, func(t *testing.T) {
			ctx := context.Background()
			database, _, err := sqlite.New().Open(ctx, "file:"+t.TempDir()+"/nested-create.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			schema := schematest.NewOptimisticConcurrency(t)
			if err := sqlite.New().ApplyInitial(ctx, database, schema.SQLite); err != nil {
				t.Fatal(err)
			}
			probe := &optimisticConcurrencyNestedHookProbe{}
			fixture := openOptimisticConcurrencyNestedFixture(t, database, schema, probe)
			seedOptimisticConcurrencyNestedUsers(t, fixture)
			caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
			childID := byte(31)
			decimal := optimisticConcurrencyNestedDecimal(t, "1.25")
			child := golem.GeneratedCreateInput[mutationResultPost](schema.Post,
				golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: childID}),
				golem.GeneratedCreateFieldValue(schema.Post, fixture.title, "nested-created"),
				golem.GeneratedCreateFieldValue(schema.Post, golem.GeneratedEqualField[mutationResultPost, golem.Decimal](schema.PostDecimal), decimal),
			)
			input := golem.GeneratedUpdateInput[mutationResultUser](schema.User,
				golem.GeneratedNestedCreate[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, child))
			owner := golem.GeneratedUniqueSelectorValue[mutationResultUser](schema.User, schema.UserKey,
				golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: 1}))
			if stance == "caller" {
				_, err = CallerUpdate(ctx, caller, fixture.userDescriptor, owner, input)
			} else {
				_, err = SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, owner, input)
			}
			if err != nil {
				t.Fatalf("allowed nested create: %v: %v", err, errors.Unwrap(err))
			}
			var author, title string
			var version int64
			query := database.Rebind(`SELECT "author_id","title","big_int" FROM ` + nestedAcceptanceTable(fixture.app, schema.Post) + ` WHERE "id"=?`)
			if err := database.QueryRowxContext(ctx, query, mutationResultUUIDText(childID)).Scan(&author, &title, &version); err != nil {
				t.Fatal(err)
			}
			if author != mutationResultUUIDText(1) || title != "nested-created" || version != 1 {
				t.Fatalf("nested create author=%q title=%q version=%d", author, title, version)
			}
			var facts int
			if err := database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil || facts != 1 {
				t.Fatalf("nested create facts=%d err=%v", facts, err)
			}
		})
	}
}

func openOptimisticConcurrencyNestedFixture(t testing.TB, database *sqlx.DB, schema schematest.Fixture, probe *optimisticConcurrencyNestedHookProbe) mutationVocabularyFixture {
	t.Helper()
	ctx := context.Background()
	userIdentity := golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schema.Post, schema.PostKey, golem.PrimaryIdentity, schema.PostID)
	userRelation := golem.GeneratedRelationMetadata(schema.User, schema.Post, schema.UserPosts, schema.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(schema.Post, schema.User, schema.PostAuthor, schema.Authorship, golem.RelationSource, golem.RelationToOne)
	userDescriptor := golem.GeneratedModelDescriptor[mutationResultUser](schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.UserID, schema.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation}))
	postDescriptor := golem.GeneratedModelDescriptor[mutationResultPost](schema.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.PostID, schema.AuthorID, schema.PostTitle, schema.PostBigInt, schema.PostDecimal, schema.PostOptionalInt}, nil,
		[]golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation}))
	descriptors, err := golem.GeneratedApplicationDescriptors(schema.Bundle.GenerationDigest(),
		golem.GeneratedStampedPackageDescriptors(schema.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	userID := golem.GeneratedEqualField[mutationResultUser, golem.UUID](schema.UserID)
	userName := golem.GeneratedTextField[mutationResultUser, string](schema.UserName)
	postID := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.PostID)
	authorID := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID)
	title := golem.GeneratedTextField[mutationResultPost, string](schema.PostTitle)
	bigInt := golem.GeneratedOrderedField[mutationResultPost, int64](schema.PostBigInt)
	optionalInt := golem.GeneratedNullableOrderedField[mutationResultPost, int64](schema.PostOptionalInt)
	injectedPost := golem.GeneratedUniqueSelectorValue[mutationResultPost](schema.Post, schema.PostKey,
		golem.GeneratedSelectorComponent(schema.PostID, golem.UUID{15: 11}))

	userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		rules.CanDelete(golem.All[mutationResultUser]())
		return rules.Freeze(schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.All[mutationResultPost]())
		rules.CanDelete(golem.All[mutationResultPost]())
		return rules.Freeze(schema.Post)
	})

	hooks := []golem.HookBinding[mutationResultActor]{
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultUser, golem.UpdateHookRequest[mutationResultUser]](schema.User, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultUser]) error {
			probe.rootBefore.Add(1)
			if probe.inject.Load() {
				request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultUser](schema.User,
					golem.GeneratedNestedConnect[mutationResultUser, mutationResultPost](schema.User, schema.UserPosts, schema.Authorship, schema.Post, injectedPost)))
			}
			return nil
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultUser, golem.UpdateHookResult[mutationResultUser]](schema.User, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[mutationResultUser]) error {
			probe.rootAfter.Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultUser, golem.UpdateHookResult[mutationResultUser]](schema.User, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[mutationResultUser]) error {
			probe.rootAfterCommit.Add(1)
			return nil
		}),
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[mutationResultPost]) error {
			probe.versionedBefore.Add(1)
			return nil
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error {
			probe.versionedAfter.Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookResult[mutationResultPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[mutationResultPost]) error {
			probe.versionedCommit.Add(1)
			return nil
		}),
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(context.Context, *golem.UpdateHookRequest[mutationResultPost]) error {
			probe.versionedBefore.Add(1)
			return nil
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookResult[mutationResultPost]](schema.Post, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[mutationResultPost]) error {
			probe.versionedAfter.Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookResult[mutationResultPost]](schema.Post, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[mutationResultPost]) error {
			probe.versionedCommit.Add(1)
			return nil
		}),
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.DeleteHookRequest[mutationResultPost]](schema.Post, golem.HookDelete, func(context.Context, *golem.DeleteHookRequest[mutationResultPost]) error {
			probe.versionedBefore.Add(1)
			return nil
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.DeleteHookResult[mutationResultPost]](schema.Post, golem.HookDelete, func(context.Context, golem.DeleteHookResult[mutationResultPost]) error {
			probe.versionedAfter.Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.DeleteHookResult[mutationResultPost]](schema.Post, golem.HookDelete, func(context.Context, golem.DeleteHookResult[mutationResultPost]) error {
			probe.versionedCommit.Add(1)
			return nil
		}),
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateManyHookRequest[mutationResultPost]](schema.Post, golem.HookUpdateMany, func(context.Context, *golem.UpdateManyHookRequest[mutationResultPost]) error {
			probe.versionedBefore.Add(1)
			return nil
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.UpdateManyHookResult[mutationResultPost]](schema.Post, golem.HookUpdateMany, func(context.Context, golem.UpdateManyHookResult[mutationResultPost]) error {
			probe.versionedAfter.Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.UpdateManyHookResult[mutationResultPost]](schema.Post, golem.HookUpdateMany, func(context.Context, golem.UpdateManyHookResult[mutationResultPost]) error {
			probe.versionedCommit.Add(1)
			return nil
		}),
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.DeleteManyHookRequest[mutationResultPost]](schema.Post, golem.HookDeleteMany, func(context.Context, *golem.DeleteManyHookRequest[mutationResultPost]) error {
			probe.versionedBefore.Add(1)
			return nil
		}),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.DeleteManyHookResult[mutationResultPost]](schema.Post, golem.HookDeleteMany, func(context.Context, golem.DeleteManyHookResult[mutationResultPost]) error {
			probe.versionedAfter.Add(1)
			return nil
		}),
		golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.DeleteManyHookResult[mutationResultPost]](schema.Post, golem.HookDeleteMany, func(context.Context, golem.DeleteManyHookResult[mutationResultPost]) error {
			probe.versionedCommit.Add(1)
			return nil
		}),
	}
	bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(),
		golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, hooks))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		Database: p8RuntimeTestDatabase(database, golem.SQLite), Bundle: schema.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(context.Context, mutationResultPrincipal) (mutationResultActor, error) {
			return mutationResultActor{}, nil
		},
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
	}))
	if err != nil {
		t.Fatal(err)
	}
	postAuthor := golem.GeneratedToOne[mutationResultPost, mutationResultUser](schema.PostAuthor, schema.Authorship, schema.User)
	base := mutationResultFixture{
		app: app, schema: schema, userDescriptor: userDescriptor, postDescriptor: postDescriptor,
		userID: userID, userName: userName, postID: postID, authorID: authorID, title: title, author: postAuthor,
	}
	return mutationVocabularyFixture{mutationResultFixture: base, bigInt: bigInt, optionalInt: optionalInt}
}

func seedOptimisticConcurrencyNestedUsers(t testing.TB, fixture mutationVocabularyFixture) {
	t.Helper()
	ctx := context.Background()
	users := nestedAcceptanceTable(fixture.app, fixture.schema.User)
	insert := fixture.app.database.Rebind(`INSERT INTO ` + users + `("id","name") VALUES (?,?)`)
	for _, user := range []struct {
		id   byte
		name string
	}{{1, "alice"}, {2, "bob"}} {
		if _, err := fixture.app.database.ExecContext(ctx, insert, mutationResultUUIDText(user.id), user.name); err != nil {
			t.Fatal(err)
		}
	}
}

func seedOptimisticConcurrencyNestedRows(t testing.TB, fixture mutationVocabularyFixture) {
	t.Helper()
	seedOptimisticConcurrencyNestedUsers(t, fixture)
	posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	insert := fixture.app.database.Rebind(`INSERT INTO ` + posts + `("id","author_id","title","big_int","decimal_value") VALUES (?,?,?,?,?)`)
	if _, err := fixture.app.database.ExecContext(context.Background(), insert,
		mutationResultUUIDText(11), mutationResultUUIDText(1), "seed-11", int64(1), int64(0)); err != nil {
		t.Fatal(err)
	}
}

func assertOptimisticConcurrencyNestedRefusal(t testing.TB, err error, nestedGate bool) {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput || failure.Message != "mutation request is invalid" {
		t.Fatalf("nested concurrency refusal=%#v err=%v", failure, err)
	}
	marker := "optimistic-concurrency mutation requires an explicit expectation"
	if nestedGate {
		marker = "P4_RUNTIME_NESTED_CONCURRENCY"
	}
	if !optimisticConcurrencyNestedErrorContains(err, marker) {
		t.Fatalf("nested concurrency refusal lacks %q: %v", marker, err)
	}
}

func optimisticConcurrencyNestedErrorContains(err error, marker string) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), marker) {
		return true
	}
	type joined interface{ Unwrap() []error }
	if value, ok := err.(joined); ok {
		for _, cause := range value.Unwrap() {
			if optimisticConcurrencyNestedErrorContains(cause, marker) {
				return true
			}
		}
		return false
	}
	return optimisticConcurrencyNestedErrorContains(errors.Unwrap(err), marker)
}

func assertOptimisticConcurrencyNestedNoSQL(t testing.TB, counts *mutationBoundaryCounts) {
	t.Helper()
	if begins, queries, execs := counts.begins.Load(), counts.queries.Load(), counts.execs.Load(); begins != 0 || queries != 0 || execs != 0 {
		t.Fatalf("refused nested concurrency request crossed SQL boundary: begins=%d queries=%d execs=%d", begins, queries, execs)
	}
}

func assertOptimisticConcurrencyNestedState(t testing.TB, fixture mutationVocabularyFixture, wantFacts int) {
	t.Helper()
	ctx := context.Background()
	posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	var author, title string
	var version int64
	query := fixture.app.database.Rebind(`SELECT "author_id","title","big_int" FROM ` + posts + ` WHERE "id"=?`)
	if err := fixture.app.database.QueryRowxContext(ctx, query, mutationResultUUIDText(11)).Scan(&author, &title, &version); err != nil {
		t.Fatal(err)
	}
	if author != mutationResultUUIDText(1) || title != "seed-11" || version != 1 {
		t.Fatalf("refused nested request changed owner: author=%q title=%q version=%d", author, title, version)
	}
	users := nestedAcceptanceTable(fixture.app, fixture.schema.User)
	var bob string
	userQuery := fixture.app.database.Rebind(`SELECT "name" FROM ` + users + ` WHERE "id"=?`)
	if err := fixture.app.database.GetContext(ctx, &bob, userQuery, mutationResultUUIDText(2)); err != nil || bob != "bob" {
		t.Fatalf("refused nested request changed root: name=%q err=%v", bob, err)
	}
	var facts int
	if err := fixture.app.database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil || facts != wantFacts {
		t.Fatalf("refused nested request facts=%d want=%d err=%v", facts, wantFacts, err)
	}
}

func optimisticConcurrencyNestedDecimal(t testing.TB, text string) golem.Decimal {
	t.Helper()
	value, err := golem.ParseDecimal(text)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
