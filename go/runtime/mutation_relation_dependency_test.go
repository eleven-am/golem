package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestRelationTraversingMutationAuthorizationExecutesInProviderSQL(t *testing.T) {
	forEachMutationResultProvider(t, MutationLimits{}, func(t testing.TB, base mutationResultFixture) {
		fixture := mutationResultFixtureWithRelationPolicies(t, base)
		ctx := context.Background()
		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}

		if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, fixture.createPost(101, golem.UUID{15: 1}, "relation-create")); err != nil {
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("relation create failure: %T: %v", cause, cause)
			}
			t.Fatalf("authorized relation create: %v", err)
		}
		if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, fixture.createPost(102, golem.UUID{15: 2}, "relation-create-denied")); err == nil {
			t.Fatal("relation-denied create committed")
		}
		assertMutationResultTitleCount(t, fixture, "relation-create-denied", 0)

		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(103, golem.UUID{15: 2}, "relation-update-denied")); err != nil {
			t.Fatal(err)
		}
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(101), relationUpdateTitle(fixture, "relation-updated")); err != nil {
			t.Fatalf("authorized relation update: %v", err)
		}
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(103), relationUpdateTitle(fixture, "relation-update-leaked")); err == nil {
			t.Fatal("relation-denied update committed")
		}
		assertMutationResultTitleCount(t, fixture, "relation-updated", 1)
		assertMutationResultTitleCount(t, fixture, "relation-update-leaked", 0)

		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(106, golem.UUID{15: 1}, "relation-nested")); err != nil {
			t.Fatal(err)
		}
		bob := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 2}))
		alice := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		connect := func(target golem.MutationTarget[mutationResultUser]) golem.UpdateInput[mutationResultPost] {
			return golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
				golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "relation-nested-updated"),
				golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, target),
			)
		}
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(106), connect(bob)); err != nil {
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("relation nested failure: %T: %v", cause, cause)
			}
			t.Fatalf("relation-authorized nested membership update: %v", err)
		}
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(106), connect(alice)); err == nil {
			t.Fatal("relation-denied nested membership update committed")
		}
		var nestedAuthor string
		queryAuthor := fixture.app.database.Rebind(`SELECT "author_id" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id" = ?`)
		if err := fixture.app.database.GetContext(ctx, &nestedAuthor, queryAuthor, mutationResultUUIDText(106)); err != nil || nestedAuthor != mutationResultUUIDText(2) {
			t.Fatalf("nested membership author=%q err=%v", nestedAuthor, err)
		}

		for _, row := range []struct {
			id     byte
			author byte
		}{{104, 1}, {105, 2}} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(row.id, golem.UUID{15: row.author}, "relation-batch")); err != nil {
				t.Fatal(err)
			}
		}
		count, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.title.Eq("relation-batch"), fixture.updateManyTitle("relation-batch-updated"))
		if err == nil || count != 0 {
			t.Fatalf("relation field-denied batch count=%d err=%v", count, err)
		}
		assertMutationResultTitleCount(t, fixture, "relation-batch-updated", 0)
		assertMutationResultTitleCount(t, fixture, "relation-batch", 2)
		count, err = CallerUpdateMany(ctx, caller, fixture.postDescriptor,
			fixture.title.Eq("relation-batch").And(fixture.authorID.Eq(golem.UUID{15: 1})), fixture.updateManyTitle("relation-batch-updated"))
		if err != nil || count != 1 {
			t.Fatalf("relation field-authorized batch count=%d err=%v", count, err)
		}
		assertMutationResultTitleCount(t, fixture, "relation-batch-updated", 1)
		assertMutationResultTitleCount(t, fixture, "relation-batch", 1)

		if _, err := CallerDelete(ctx, caller, fixture.postDescriptor, fixture.target(101)); err != nil {
			t.Fatalf("authorized relation delete: %v", err)
		}
		if _, err := CallerDelete(ctx, caller, fixture.postDescriptor, fixture.target(103)); err == nil {
			t.Fatal("relation-denied delete committed")
		}
		assertMutationResultTitleCount(t, fixture, "relation-updated", 0)
		assertMutationResultTitleCount(t, fixture, "relation-update-denied", 1)
	})
}

func TestRequiredSourceNestedCreateExecutesDependencyBeforeRoot(t *testing.T) {
	for _, stance := range []string{"system", "caller"} {
		t.Run(stance, func(t *testing.T) {
			schema := schematest.NewSubscribedGraph(t)
			fixture := newGraphMutationFixture(t, schema, golem.ModelID{})
			author := golem.GeneratedCreateInput[graphMutationUser](schema.User,
				golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 242}),
				golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "dependency-author"),
			)
			post := golem.GeneratedCreateInput[graphMutationPost](schema.Post,
				golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: 241}),
				golem.GeneratedCreateFieldValue(schema.Post, fixture.postTitle, "dependency-post"),
				golem.GeneratedNestedCreate[graphMutationPost, graphMutationUser](schema.Post, schema.PostAuthor, schema.Authorship, schema.User, author),
			)
			var err error
			if stance == "system" {
				_, err = SystemCreate(context.Background(), fixture.app.System(), fixture.postDescriptor, post)
			} else {
				caller, callerErr := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
				if callerErr != nil {
					t.Fatal(callerErr)
				}
				_, err = CallerCreate(context.Background(), caller, fixture.postDescriptor, post)
			}
			if err != nil {
				for cause := err; cause != nil; cause = errors.Unwrap(cause) {
					t.Logf("required source connect failure: %T: %v", cause, cause)
				}
				t.Fatal(err)
			}
			var authorID string
			query := fixture.app.database.Rebind(`SELECT "author_id" FROM ` + nestedAcceptanceTable(fixture.app, schema.Post) + ` WHERE "id"=?`)
			if err := fixture.app.database.GetContext(context.Background(), &authorID, query, mutationResultUUIDText(241)); err != nil || authorID != mutationResultUUIDText(242) {
				t.Fatalf("required source author=%q err=%v", authorID, err)
			}
			assertGraphMutationRowsAndFacts(t, fixture, 1, 1, 0, 2)

			existing := golem.GeneratedCreateInput(schema.User,
				golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 243}),
				golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "connected-author"),
			)
			if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.userDescriptor, existing); err != nil {
				t.Fatal(err)
			}
			existingTarget := golem.GeneratedUniqueSelectorValue[graphMutationUser](schema.User, schema.UserKey,
				golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: 243}))
			connectedPost := golem.GeneratedCreateInput[graphMutationPost](schema.Post,
				golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: 244}),
				golem.GeneratedCreateFieldValue(schema.Post, fixture.postTitle, "connected-post"),
				golem.GeneratedNestedConnect[graphMutationPost, graphMutationUser](schema.Post, schema.PostAuthor, schema.Authorship, schema.User, existingTarget),
			)
			if stance == "system" {
				_, err = SystemCreate(context.Background(), fixture.app.System(), fixture.postDescriptor, connectedPost)
			} else {
				caller, callerErr := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
				if callerErr != nil {
					t.Fatal(callerErr)
				}
				_, err = CallerCreate(context.Background(), caller, fixture.postDescriptor, connectedPost)
			}
			if err != nil {
				for cause := err; cause != nil; cause = errors.Unwrap(cause) {
					t.Logf("required source connect failure: %T: %v", cause, cause)
				}
				t.Fatal(err)
			}
			if err := fixture.app.database.GetContext(context.Background(), &authorID, query, mutationResultUUIDText(244)); err != nil || authorID != mutationResultUUIDText(243) {
				t.Fatalf("required source connected author=%q err=%v", authorID, err)
			}

			createWithCOC := func(postID, targetID byte, targetName string) golem.CreateInput[graphMutationPost] {
				target := golem.GeneratedUniqueSelectorValue[graphMutationUser](schema.User, schema.UserKey,
					golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: targetID}))
				fallback := golem.GeneratedCreateInput(schema.User,
					golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: targetID}),
					golem.GeneratedCreateFieldValue(schema.User, fixture.userName, targetName),
				)
				return golem.GeneratedCreateInput[graphMutationPost](schema.Post,
					golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: postID}),
					golem.GeneratedCreateFieldValue(schema.Post, fixture.postTitle, "connect-or-create-post"),
					golem.GeneratedNestedConnectOrCreate[graphMutationPost, graphMutationUser](schema.Post, schema.PostAuthor, schema.Authorship, schema.User, target, fallback),
				)
			}
			for _, branch := range []struct {
				name           string
				postID, userID byte
			}{{"existing", 247, 243}, {"create", 249, 248}} {
				t.Run("connect-or-create-"+branch.name, func(t *testing.T) {
					request := createWithCOC(branch.postID, branch.userID, "coc-created-author")
					if stance == "system" {
						_, err = SystemCreate(context.Background(), fixture.app.System(), fixture.postDescriptor, request)
					} else {
						caller, callerErr := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
						if callerErr != nil {
							t.Fatal(callerErr)
						}
						_, err = CallerCreate(context.Background(), caller, fixture.postDescriptor, request)
					}
					if err != nil {
						for cause := err; cause != nil; cause = errors.Unwrap(cause) {
							t.Logf("source connect-or-create failure: %T: %v", cause, cause)
						}
						t.Fatal(err)
					}
					if err := fixture.app.database.GetContext(context.Background(), &authorID, query, mutationResultUUIDText(branch.postID)); err != nil || authorID != mutationResultUUIDText(branch.userID) {
						t.Fatalf("source connect-or-create author=%q want=%q err=%v", authorID, mutationResultUUIDText(branch.userID), err)
					}
				})
			}
		})
	}
	t.Run("caller-child-before-hook", func(t *testing.T) {
		schema := schematest.NewSubscribedGraph(t)
		name := golem.GeneratedTextField[graphMutationUser, string](schema.UserName)
		capability := golem.GeneratedCreateFieldCapability(schema.User, name)
		order := make([]string, 0, 6)
		hooks := []golem.HookBinding[graphMutationActor]{
			golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookRequest[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[graphMutationPost]) error {
				order = append(order, "before-post")
				return nil
			}),
			golem.GeneratedBeforeHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookRequest[graphMutationUser]](schema.User, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[graphMutationUser]) error {
				order = append(order, "before-user")
				return golem.SetCreate(request, capability, "pre-parent-hooked")
			}),
			golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationPost]) error {
				order = append(order, "after-post")
				return nil
			}),
			golem.GeneratedAfterHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
				order = append(order, "after-user")
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationPost, golem.CreateHookResult[graphMutationPost]](schema.Post, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationPost]) error {
				order = append(order, "commit-post")
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[graphMutationActor, graphMutationUser, golem.CreateHookResult[graphMutationUser]](schema.User, golem.HookCreate, func(context.Context, golem.CreateHookResult[graphMutationUser]) error {
				order = append(order, "commit-user")
				return nil
			}),
		}
		fixture := newGraphMutationFixtureWithHooks(t, schema, golem.ModelID{}, hooks)
		author := golem.GeneratedCreateInput(schema.User,
			golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: 246}),
			golem.GeneratedCreateFieldValue(schema.User, fixture.userName, "untransformed"),
		)
		post := golem.GeneratedCreateInput[graphMutationPost](schema.Post,
			golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: 245}),
			golem.GeneratedCreateFieldValue(schema.Post, fixture.postTitle, "hooked-dependency"),
			golem.GeneratedNestedCreate[graphMutationPost, graphMutationUser](schema.Post, schema.PostAuthor, schema.Authorship, schema.User, author),
		)
		caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CallerCreate(context.Background(), caller, fixture.postDescriptor, post); err != nil {
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("pre-parent hook failure: %T: %v", cause, cause)
			}
			t.Fatal(err)
		}
		var stored string
		query := fixture.app.database.Rebind(`SELECT "name" FROM ` + nestedAcceptanceTable(fixture.app, schema.User) + ` WHERE "id"=?`)
		if err := fixture.app.database.GetContext(context.Background(), &stored, query, mutationResultUUIDText(246)); err != nil || stored != "pre-parent-hooked" {
			t.Fatalf("pre-parent transformed name=%q err=%v", stored, err)
		}
		wantOrder := []string{"before-post", "before-user", "after-user", "after-post", "commit-user", "commit-post"}
		if !reflect.DeepEqual(order, wantOrder) {
			t.Fatalf("pre-parent hook order=%v want=%v", order, wantOrder)
		}
		var facts []struct {
			Model     string `db:"model_id"`
			Action    string `db:"action"`
			Causation string `db:"causation_id"`
			Ordinal   int64  `db:"transaction_ordinal"`
		}
		if err := fixture.app.database.SelectContext(context.Background(), &facts, `SELECT "model_id","action","causation_id","transaction_ordinal" FROM `+nestedAcceptanceOutbox(fixture.app)+` ORDER BY "transaction_ordinal"`); err != nil {
			t.Fatal(err)
		}
		if len(facts) != 2 || facts[0].Model != fmt.Sprintf("%x", schema.Post) || facts[1].Model != fmt.Sprintf("%x", schema.User) || facts[0].Action != "created" || facts[1].Action != "created" || facts[0].Ordinal != 1 || facts[1].Ordinal != 2 || facts[0].Causation == "" || facts[1].Causation != facts[0].Causation {
			t.Fatalf("pre-parent facts=%#v", facts)
		}
	})
}

func TestOptionalSourceNestedUpsertCreateBranchAssignsOwnerAcrossProviders(t *testing.T) {
	runRelationDeleteProviderProfiles(t, "source_upsert_create", schematest.NewSubscribedIndexedOptionalSource, schematest.NewSubscribedIndexedOptionalSourcePostgreSQLNamespaces, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		post := golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 252}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "source-upsert-root"),
		)
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, post); err != nil {
			t.Fatal(err)
		}
		create := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 251}),
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "source-upsert-created"),
		)
		update := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "must-not-update"),
		)
		input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedNestedUpsert[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, nil, create, update),
		)
		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(252), input); err != nil {
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("source upsert create failure: %T: %v", cause, cause)
			}
			t.Fatal(err)
		}
		var author string
		query := fixture.app.database.Rebind(`SELECT "author_id" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id"=?`)
		if err := fixture.app.database.GetContext(ctx, &author, query, mutationResultUUIDText(252)); err != nil || author != mutationResultUUIDText(251) {
			t.Fatalf("source upsert owner=%q err=%v", author, err)
		}
	})
}

func TestPostgreSQLRequiredSourceCreateAndConnectOrCreateDependencies(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			fixture := newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, nil)
			schema := fixture.schema
			caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			userInput := func(id byte, name string) golem.CreateInput[graphMutationUser] {
				return golem.GeneratedCreateInput(schema.User,
					golem.GeneratedCreateFieldValue(schema.User, fixture.userID, golem.UUID{15: id}),
					golem.GeneratedCreateFieldValue(schema.User, fixture.userName, name))
			}
			postInput := func(id byte, nested golem.NestedValue[graphMutationPost]) golem.CreateInput[graphMutationPost] {
				return golem.GeneratedCreateInput(schema.Post,
					golem.GeneratedCreateFieldValue(schema.Post, fixture.postID, golem.UUID{15: id}),
					golem.GeneratedCreateFieldValue(schema.Post, fixture.postTitle, "postgres-dependency"), nested)
			}
			created := userInput(231, "postgres-created")
			if _, err := CallerCreate(context.Background(), caller, fixture.postDescriptor, postInput(230,
				golem.GeneratedNestedCreate[graphMutationPost, graphMutationUser](schema.Post, schema.PostAuthor, schema.Authorship, schema.User, created))); err != nil {
				t.Fatal(err)
			}
			missing := golem.GeneratedUniqueSelectorValue[graphMutationUser](schema.User, schema.UserKey,
				golem.GeneratedSelectorComponent(schema.UserID, golem.UUID{15: 233}))
			coc := golem.GeneratedNestedConnectOrCreate[graphMutationPost, graphMutationUser](schema.Post, schema.PostAuthor, schema.Authorship, schema.User, missing, userInput(233, "postgres-coc"))
			if _, err := CallerCreate(context.Background(), caller, fixture.postDescriptor, postInput(232, coc)); err != nil {
				t.Fatal(err)
			}
			var owners []string
			query := fixture.app.database.Rebind(`SELECT "author_id" FROM ` + nestedAcceptanceTable(fixture.app, schema.Post) + ` WHERE "id" IN (?,?) ORDER BY "id"`)
			if err := fixture.app.database.SelectContext(context.Background(), &owners, query, mutationResultUUIDText(230), mutationResultUUIDText(232)); err != nil || !reflect.DeepEqual(owners, []string{mutationResultUUIDText(231), mutationResultUUIDText(233)}) {
				t.Fatalf("postgres dependency owners=%v err=%v", owners, err)
			}
		})
	}
}

func TestRootUpsertCreateBranchSupportsRequiredSourceDependencies(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		assertRootUpsertRequiredSourceDependency(t, newGraphMutationFixture(t, schematest.NewSubscribedGraph(t), golem.ModelID{}))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			assertRootUpsertRequiredSourceDependency(t, newPostgresGraphMutationFixtureWithHooks(t, profile, golem.ModelID{}, nil))
		})
	}
}

func assertRootUpsertRequiredSourceDependency(t testing.TB, fixture graphMutationFixture) {
	t.Helper()
	ctx := context.Background()
	caller, err := fixture.app.ForPrincipal(ctx, graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	userInput := func(id byte) golem.CreateInput[graphMutationUser] {
		return golem.GeneratedCreateInput(fixture.schema.User,
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, fmt.Sprintf("root-upsert-author-%d", id)))
	}
	userTarget := func(id byte) golem.MutationTarget[graphMutationUser] {
		return golem.GeneratedUniqueSelectorValue[graphMutationUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: id}))
	}
	for _, test := range []struct {
		name           string
		caller         bool
		postID, userID byte
		nested         func() golem.NestedValue[graphMutationPost]
		seedUser       bool
	}{
		{name: "caller-create", caller: true, postID: 220, userID: 221, nested: func() golem.NestedValue[graphMutationPost] {
			return golem.GeneratedNestedCreate[graphMutationPost, graphMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userInput(221))
		}},
		{name: "system-create", postID: 222, userID: 223, nested: func() golem.NestedValue[graphMutationPost] {
			return golem.GeneratedNestedCreate[graphMutationPost, graphMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userInput(223))
		}},
		{name: "caller-connect", caller: true, postID: 225, userID: 224, seedUser: true, nested: func() golem.NestedValue[graphMutationPost] {
			return golem.GeneratedNestedConnect[graphMutationPost, graphMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userTarget(224))
		}},
		{name: "system-connect", postID: 227, userID: 226, seedUser: true, nested: func() golem.NestedValue[graphMutationPost] {
			return golem.GeneratedNestedConnect[graphMutationPost, graphMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userTarget(226))
		}},
		{name: "caller-connect-or-create-existing", caller: true, postID: 229, userID: 228, seedUser: true, nested: func() golem.NestedValue[graphMutationPost] {
			return golem.GeneratedNestedConnectOrCreate[graphMutationPost, graphMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userTarget(228), userInput(228))
		}},
		{name: "system-connect-or-create-create", postID: 231, userID: 230, nested: func() golem.NestedValue[graphMutationPost] {
			return golem.GeneratedNestedConnectOrCreate[graphMutationPost, graphMutationUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userTarget(230), userInput(230))
		}},
	} {
		if test.seedUser {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, userInput(test.userID)); err != nil {
				t.Fatalf("%s seed user: %v", test.name, err)
			}
		}
		create := golem.GeneratedCreateInput[graphMutationPost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: test.postID}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postTitle, "root-upsert-post"), test.nested())
		target := golem.GeneratedUniqueSelectorValue[graphMutationPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: test.postID}))
		update := golem.GeneratedUpdateInput[graphMutationPost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.postTitle, "must-not-update"))
		if test.caller {
			_, err = CallerUpsert(ctx, caller, fixture.postDescriptor, target, create, update)
		} else {
			_, err = SystemUpsert(ctx, fixture.app.System(), fixture.postDescriptor, target, create, update)
		}
		if err != nil {
			for cause := err; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("%s root upsert source dependency failure: %T: %v", test.name, cause, cause)
			}
			t.Fatalf("%s: %v", test.name, err)
		}
		var owner string
		query := fixture.app.database.Rebind(`SELECT "author_id" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id"=?`)
		if err := fixture.app.database.GetContext(ctx, &owner, query, mutationResultUUIDText(test.postID)); err != nil || owner != mutationResultUUIDText(test.userID) {
			t.Fatalf("%s root upsert source owner=%q want=%q err=%v", test.name, owner, mutationResultUUIDText(test.userID), err)
		}
	}
}

func relationUpdateTitle(fixture mutationResultFixture, title string) golem.UpdateInput[mutationResultPost] {
	return golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, title))
}

func mutationResultFixtureWithRelationPolicies(t testing.TB, fixture mutationResultFixture) mutationResultFixture {
	t.Helper()
	schemaFixture := fixture.schema
	ownerAlice := fixture.author.Is(fixture.userName.Eq("alice"))
	users := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schemaFixture.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		rules.CanDelete(golem.All[mutationResultUser]())
		return rules.Freeze(schemaFixture.User)
	})
	posts := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schemaFixture.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(ownerAlice)
		rules.CanUpdate(golem.All[mutationResultPost]())
		rules.CannotUpdateFields(golem.All[mutationResultPost](), fixture.title, fixture.authorID)
		rules.CanUpdateFields(ownerAlice, fixture.title, fixture.authorID)
		rules.CanDelete(ownerAlice)
		return rules.Freeze(schemaFixture.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(schemaFixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(
		schemaFixture.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{users, posts}, nil,
	))
	if err != nil {
		t.Fatal(err)
	}
	provider := golem.SQLite
	if fixture.app.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		Database: p8RuntimeTestDatabase(fixture.app.database, provider), Bundle: schemaFixture.Bundle, Bindings: bindings, Descriptors: fixture.app.descriptors,
		ResolvePrincipal: func(context.Context, mutationResultPrincipal) (mutationResultActor, error) {
			return mutationResultActor{}, nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}
