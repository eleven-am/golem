package runtime

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
)

func TestGraphQLMutationAndNestedOraclePostgreSQLProfiles(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			t.Run("six-root-scalar-parity", func(t *testing.T) {
				assertP5GraphQLPostgresRootMutations(t, profile)
			})
			t.Run("complete-eleven-operation-social-graph", func(t *testing.T) {
				log := &socialHookLog{}
				fixture := newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, log)
				assertP5GraphQLPostgresNestedComplete(t, fixture, log)
			})
			t.Run("nested-independent-denial-rollback", func(t *testing.T) {
				log := &socialHookLog{}
				fixture := newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, log)
				fixture = p5ReopenPostgresSocialMutation(t, fixture, fixture.schema.Post, log, MutationLimits{})
				assertP5GraphQLNestedRollback(t, fixture, log, golem.CodeForbidden)
			})
			t.Run("nested-limit-rollback", func(t *testing.T) {
				log := &socialHookLog{}
				fixture := newPostgresSocialMutationFixture(t, profile, golem.ModelID{}, log)
				fixture = p5ReopenPostgresSocialMutation(t, fixture, golem.ModelID{}, log, MutationLimits{MaxTouchedRows: 1})
				assertP5GraphQLNestedRollback(t, fixture, log, golem.CodeBadUserInput)
			})
		})
	}
}

func assertP5GraphQLPostgresRootMutations(t *testing.T, profile postgresAcceptanceProfile) {
	t.Helper()
	fixture := newP5PostgresMutationVocabularyFixture(t, profile)
	server := newP5PostgresMutationServer(t, fixture)
	ctx := context.Background()
	users := nestedAcceptanceTable(fixture.app, fixture.schema.User)
	if _, err := fixture.app.database.ExecContext(ctx, `INSERT INTO `+users+`("id","name") VALUES ($1,$2)`, mutationResultUUIDText(1), "owner"); err != nil {
		t.Fatal(err)
	}
	initialFacts := p5PostgresFactCount(t, fixture)

	graphqlCreate := func(id byte, title string) {
		t.Helper()
		p5ExecutePostgresMutation(t, server, fmt.Sprintf(`mutation {
  createPost(data: {
    id: %q, title: %q, author: { connect: { id: %q } },
    bigInt: "10", decimal: "1.25", optionalInt: "5"
  }) { id title bigInt decimal optionalInt }
}`, mutationResultUUIDText(id), title, mutationResultUUIDText(1)))
	}
	graphqlCreate(101, "graphql-create")
	updated := p5ExecutePostgresMutation(t, server, fmt.Sprintf(`mutation {
  updatePost(where: { id: %q }, data: {
    title: { set: "graphql-set" }, bigInt: { increment: "2" }, optionalInt: { setNull: true }
  }) { title bigInt optionalInt }
}`, mutationResultUUIDText(101)))["updatePost"].(map[string]any)
	if updated["title"] != "graphql-set" || updated["bigInt"] != "12" || updated["optionalInt"] != nil {
		t.Fatalf("PostgreSQL GraphQL set/increment/null result=%#v", updated)
	}
	p5ExecutePostgresMutation(t, server, fmt.Sprintf(`mutation {
  updatePost(where: { id: %q }, data: { bigInt: { decrement: "3" } }) { id bigInt }
}`, mutationResultUUIDText(101)))
	p5ExecutePostgresMutation(t, server, fmt.Sprintf(`mutation {
  upsertPost(
    where: { id: %q }
    create: { id: %q, title: "unused", author: { connect: { id: %q } }, bigInt: "10", decimal: "1.25", optionalInt: "5" }
    update: { title: { set: "graphql-upserted" } }
  ) { id title bigInt optionalInt }
}`, mutationResultUUIDText(101), mutationResultUUIDText(101), mutationResultUUIDText(1)))
	p5ExecutePostgresMutation(t, server, fmt.Sprintf(`mutation {
  upsertPost(
    where: { id: %q }
    create: { id: %q, title: "graphql-upsert-create", author: { connect: { id: %q } }, bigInt: "10", decimal: "1.25", optionalInt: "5" }
    update: { title: { set: "unused" } }
  ) { id title }
}`, mutationResultUUIDText(102), mutationResultUUIDText(102), mutationResultUUIDText(1)))
	p5ExecutePostgresMutation(t, server, fmt.Sprintf(`mutation { deletePost(where: { id: %q }) { id title } }`, mutationResultUUIDText(102)))
	graphqlCreate(103, "graphql-batch")
	graphqlCreate(104, "graphql-batch")
	batch := p5ExecutePostgresMutation(t, server, `mutation {
  updateManyPosts(where: { title: { equals: "graphql-batch" } }, data: { title: { set: "graphql-batched" } }) { count }
  deleteManyPosts(where: { title: { equals: "graphql-batched" } }) { count }
}`)
	if batch["updateManyPosts"].(map[string]any)["count"] != int32(2) || batch["deleteManyPosts"].(map[string]any)["count"] != int32(2) {
		t.Fatalf("PostgreSQL GraphQL batch counts=%#v", batch)
	}
	graphqlFacts := p5PostgresFactCount(t, fixture) - initialFacts
	assertP5PostgresMutationState(t, fixture, 101, "graphql-upserted", 9, nil)
	assertP5PostgresMutationAbsent(t, fixture, 102, 103, 104)

	caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
	beforeGoFacts := p5PostgresFactCount(t, fixture)
	if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, p5PostgresExactCreate(t, fixture, 111, "go-create")); err != nil {
		t.Fatal(err)
	}
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(111), golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "go-set"),
		golem.GeneratedIncrementFieldValue(fixture.schema.Post, fixture.bigInt, int64(2)),
		golem.GeneratedNullFieldValue(fixture.schema.Post, fixture.optionalInt),
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(111), golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedDecrementFieldValue(fixture.schema.Post, fixture.bigInt, int64(3)),
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(111), p5PostgresExactCreate(t, fixture, 111, "unused"), fixture.updateTitle("go-upserted")); err != nil {
		t.Fatal(err)
	}
	if _, err := CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(112), p5PostgresExactCreate(t, fixture, 112, "go-upsert-create"), fixture.updateTitle("unused")); err != nil {
		t.Fatal(err)
	}
	if _, err := CallerDelete(ctx, caller, fixture.postDescriptor, fixture.target(112)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []byte{113, 114} {
		if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, p5PostgresExactCreate(t, fixture, id, "go-batch")); err != nil {
			t.Fatal(err)
		}
	}
	updatedCount, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.title.Eq("go-batch"), golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "go-batched"),
	))
	if err != nil {
		t.Fatal(err)
	}
	deletedCount, err := CallerDeleteMany(ctx, caller, fixture.postDescriptor, fixture.title.Eq("go-batched"))
	if err != nil {
		t.Fatal(err)
	}
	if updatedCount != 2 || deletedCount != 2 {
		t.Fatalf("PostgreSQL Go caller batch counts update=%d delete=%d", updatedCount, deletedCount)
	}
	goFacts := p5PostgresFactCount(t, fixture) - beforeGoFacts
	if goFacts != graphqlFacts {
		t.Fatalf("PostgreSQL GraphQL/Go durable facts differ graphql=%d go=%d", graphqlFacts, goFacts)
	}
	assertP5PostgresMutationState(t, fixture, 111, "go-upserted", 9, nil)
	assertP5PostgresMutationAbsent(t, fixture, 112, 113, 114)
}

func newP5PostgresMutationVocabularyFixture(t *testing.T, profile postgresAcceptanceProfile) mutationVocabularyFixture {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	applicationNamespace := physical.PhysicalName(fmt.Sprintf("golem_p5_mutation_%s_%d_%d", profile.name, os.Getpid(), suffix))
	systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_p5_mutation_system_%s_%d_%d", profile.name, os.Getpid(), suffix))
	schema := schematest.NewMutationVocabularyPostgreSQLNamespaces(t, applicationNamespace, systemNamespace)
	provider := postgresprovider.New()
	database, _, err := provider.Open(ctx, profile.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(applicationNamespace)+`" CASCADE`)
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(systemNamespace)+`" CASCADE`)
		_ = database.Close()
	})
	if err := provider.ApplyInitial(ctx, database, schema.PostgreSQL); err != nil {
		t.Fatal(err)
	}
	return openMutationVocabularyFixture(t, database, golem.PostgreSQL, schema)
}

func newP5PostgresMutationServer(t *testing.T, fixture mutationVocabularyFixture) *publicgraphql.Server[mutationResultPrincipal] {
	t.Helper()
	bundle, compilation := p5GraphQLBundle(t, fixture.schema.Bundle)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := publicgraphql.NewGeneratedExecutor(publicgraphql.GeneratedExecutorConfig[mutationResultPrincipal]{
		Bundle: bundle,
		BeginCaller: func(ctx context.Context, principal mutationResultPrincipal) (publicgraphql.CallerExecution, error) {
			caller, callerErr := fixture.app.ForPrincipal(ctx, principal)
			if callerErr != nil {
				return nil, callerErr
			}
			return NewCallerMutationExecution(caller,
				CallerMutationModel[mutationResultPrincipal, mutationResultActor](fixture.userDescriptor),
				CallerMutationModel[mutationResultPrincipal, mutationResultActor](fixture.postDescriptor),
			)
		},
		ReportInternalError: func(_ context.Context, report error) {
			t.Errorf("unexpected PostgreSQL GraphQL executor error: %v", report)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := publicgraphql.NewServer(document.SDL, publicgraphql.Config[mutationResultPrincipal]{
		PrincipalFromContext: func(context.Context) (mutationResultPrincipal, bool) { return mutationResultPrincipal{}, true },
		ContractFingerprint:  bundle.Contract().Fingerprint(),
		ReportInternalError: func(_ context.Context, report error) {
			t.Errorf("unexpected PostgreSQL GraphQL server error: %v", report)
		},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func p5ExecutePostgresMutation(t *testing.T, server *publicgraphql.Server[mutationResultPrincipal], query string) map[string]any {
	t.Helper()
	response := server.Execute(context.Background(), mutationResultPrincipal{}, publicgraphql.Request{Query: query})
	if len(response.Errors) != 0 || response.Data == nil {
		t.Fatalf("PostgreSQL GraphQL mutation failed: response=%#v\n%s", response, query)
	}
	return response.Data.(map[string]any)
}

func p5PostgresExactCreate(t *testing.T, fixture mutationVocabularyFixture, id byte, title string) golem.CreateInput[mutationResultPost] {
	t.Helper()
	decimal, err := golem.ParseDecimal("1.25")
	if err != nil {
		t.Fatal(err)
	}
	decimalField := golem.GeneratedEqualField[mutationResultPost, golem.Decimal](fixture.schema.PostDecimal)
	userTarget := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
		golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}),
	)
	return golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, title),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.bigInt, int64(10)),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, decimalField, decimal),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.optionalInt, int64(5)),
		golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userTarget),
	)
}

func p5PostgresFactCount(t *testing.T, fixture mutationVocabularyFixture) int {
	t.Helper()
	var count int
	if err := fixture.app.database.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertP5PostgresMutationState(t *testing.T, fixture mutationVocabularyFixture, id byte, title string, big int64, optional *int64) {
	t.Helper()
	var gotTitle string
	var gotBig int64
	var gotOptional *int64
	query := fixture.app.database.Rebind(`SELECT "title","big_int","optional_int" FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id"=?`)
	if err := fixture.app.database.QueryRowxContext(context.Background(), query, mutationResultUUIDText(id)).Scan(&gotTitle, &gotBig, &gotOptional); err != nil {
		t.Fatal(err)
	}
	if gotTitle != title || gotBig != big || (gotOptional == nil) != (optional == nil) || gotOptional != nil && *gotOptional != *optional {
		t.Fatalf("PostgreSQL post %d title=%q big=%d optional=%v want title=%q big=%d optional=%v", id, gotTitle, gotBig, gotOptional, title, big, optional)
	}
}

func assertP5PostgresMutationAbsent(t *testing.T, fixture mutationVocabularyFixture, ids ...byte) {
	t.Helper()
	for _, id := range ids {
		var count int
		query := fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + nestedAcceptanceTable(fixture.app, fixture.schema.Post) + ` WHERE "id"=?`)
		if err := fixture.app.database.GetContext(context.Background(), &count, query, mutationResultUUIDText(id)); err != nil || count != 0 {
			t.Fatalf("PostgreSQL post %d rows=%d err=%v", id, count, err)
		}
	}
}

func assertP5GraphQLPostgresNestedComplete(t *testing.T, fixture socialMutationFixture, log *socialHookLog) {
	t.Helper()
	ctx := context.Background()
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, fixture.userCreate(2, "friend")); err != nil {
		t.Fatal(err)
	}
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.tagDescriptor, fixture.tagCreate(30, "deep-tag")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	log.before, log.after, log.afterCommit = nil, nil, nil
	harness := newP5SocialGraphQLHarness(t, fixture)
	user, post, comment := harness.model(t, "User"), harness.model(t, "Post"), harness.model(t, "Comment")
	userID, postID := p5UUID(1), p5UUID(10)
	commentID, replyID, friendID := p5UUID(20), p5UUID(21), p5UUID(2)

	harness.mutate(t, `mutation {
  `+user.Roots.Create+`(data: {
    id: "`+userID+`" name: "owner"
    posts: { create: [{
      id: "`+postID+`" title: "post"
      comments: { create: [{
        id: "`+commentID+`" body: "comment"
        author: { connect: { id: "`+userID+`" } }
        replies: { create: [{
          id: "`+replyID+`" body: "reply"
          post: { connect: { id: "`+postID+`" } }
          author: { connect: { id: "`+userID+`" } }
        }] }
      }] }
      postTags: { create: [{ tag: { connect: { name: "deep-tag" } } }] }
    }] }
    friendshipsFrom: { create: [{ friend: { connect: { id: "`+friendID+`" } } }] }
  }) { id name }
}`)
	assertSocialCounts(t, fixture, map[golem.ModelID]int{
		fixture.schema.User: 2, fixture.schema.Post: 1, fixture.schema.Comment: 2,
		fixture.schema.Friendship: 1, fixture.schema.Tag: 1, fixture.schema.PostTag: 1,
	}, 6)
	wantBefore := []string{"user", "post", "comment", "comment", "postTag", "friendship"}
	wantReverse := []string{"friendship", "postTag", "comment", "comment", "post", "user"}
	if !reflect.DeepEqual(log.before, wantBefore) || !reflect.DeepEqual(log.after, wantReverse) || !reflect.DeepEqual(log.afterCommit, wantReverse) {
		t.Fatalf("PostgreSQL GraphQL social hooks before=%v after=%v afterCommit=%v", log.before, log.after, log.afterCommit)
	}
	assertSocialFactModels(t, fixture, []golem.ModelID{
		fixture.schema.User, fixture.schema.Post, fixture.schema.Comment, fixture.schema.Comment,
		fixture.schema.PostTag, fixture.schema.Friendship,
	})

	ownerID := p5UUID(4)
	harness.mutate(t, `mutation { `+user.Roots.Create+`(data: { id: "`+ownerID+`", name: "vocabulary-owner" }) { id } }`)
	for _, seed := range []struct{ id, title string }{{p5UUID(53), "connect-before"}, {p5UUID(54), "connect-or-create-before"}} {
		harness.mutate(t, `mutation { `+post.Roots.Create+`(data: {
      id: "`+seed.id+`", title: "`+seed.title+`", author: { connect: { id: "`+friendID+`" } }
    }) { id } }`)
	}
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `create: [{ id: "`+p5UUID(50)+`", title: "create" }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `createMany: [{ id: "`+p5UUID(51)+`", title: "many-a" }, { id: "`+p5UUID(52)+`", title: "many-b" }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `connect: [{ id: "`+p5UUID(53)+`" }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `connectOrCreate: [{ where: { id: "`+p5UUID(54)+`" }, create: { id: "`+p5UUID(54)+`", title: "unused" } }]`))
	harness.mutate(t, `mutation { `+comment.Roots.Update+`(where: { id: "`+commentID+`" }, data: { replies: { disconnect: [{ id: "`+replyID+`" }] } }) { id body } }`)
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `set: [{ id: "`+p5UUID(50)+`" }, { id: "`+p5UUID(51)+`" }, { id: "`+p5UUID(52)+`" }, { id: "`+p5UUID(53)+`" }, { id: "`+p5UUID(54)+`" }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `update: [{ where: { id: "`+p5UUID(50)+`" }, data: { title: { set: "updated" } } }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `updateMany: [{
    where: { id: { in: ["`+p5UUID(51)+`", "`+p5UUID(52)+`"] } }
    data: { title: { set: "bulk-updated" } }
  }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `upsert: [{ where: { id: "`+p5UUID(50)+`" }, create: { id: "`+p5UUID(50)+`", title: "unused" }, update: { title: { set: "upsert-updated" } } }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `upsert: [{ where: { id: "`+p5UUID(55)+`" }, create: { id: "`+p5UUID(55)+`", title: "upsert-created" }, update: { title: { set: "unused" } } }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `delete: [{ id: "`+p5UUID(55)+`" }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `deleteMany: [{ id: { in: ["`+p5UUID(51)+`", "`+p5UUID(52)+`"] } }]`))

	posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	var title, author string
	query := fixture.app.database.Rebind(`SELECT "title","author_id" FROM ` + posts + ` WHERE "id"=?`)
	if err := fixture.app.database.QueryRowxContext(ctx, query, mutationResultUUIDText(50)).Scan(&title, &author); err != nil || title != "upsert-updated" || author != mutationResultUUIDText(4) {
		t.Fatalf("PostgreSQL GraphQL nested survivor title=%q author=%q err=%v", title, author, err)
	}
	var removed int
	query = fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + posts + ` WHERE "id" IN (?, ?, ?)`)
	if err := fixture.app.database.GetContext(ctx, &removed, query, mutationResultUUIDText(51), mutationResultUUIDText(52), mutationResultUUIDText(55)); err != nil || removed != 0 {
		t.Fatalf("PostgreSQL GraphQL nested deletes remaining=%d err=%v", removed, err)
	}
	var parentID *string
	comments := nestedAcceptanceTable(fixture.app, fixture.schema.Comment)
	query = fixture.app.database.Rebind(`SELECT "parent_id" FROM ` + comments + ` WHERE "id"=?`)
	if err := fixture.app.database.GetContext(ctx, &parentID, query, mutationResultUUIDText(21)); err != nil || parentID != nil {
		t.Fatalf("PostgreSQL GraphQL nested disconnect parent=%v err=%v", parentID, err)
	}
}

func p5ReopenPostgresSocialMutation(t *testing.T, fixture socialMutationFixture, denied golem.ModelID, log *socialHookLog, limits MutationLimits) socialMutationFixture {
	t.Helper()
	policies := []golem.PolicyBinding[graphMutationActor]{
		allowSocialMutationPolicy[socialMutationUser](fixture.schema.User, denied),
		allowSocialMutationPolicy[socialMutationPost](fixture.schema.Post, denied),
		allowSocialMutationPolicy[socialMutationComment](fixture.schema.Comment, denied),
		allowSocialMutationPolicy[socialMutationFriendship](fixture.schema.Friendship, denied),
		allowSocialMutationPolicy[socialMutationTag](fixture.schema.Tag, denied),
		allowSocialMutationPolicy[socialMutationPostTag](fixture.schema.PostTag, denied),
	}
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(),
		golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), policies, socialMutationHooks(fixture.schema, log)))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[graphMutationPrincipal, graphMutationActor]{
		Database: p8RuntimeTestDatabase(fixture.app.database, golem.PostgreSQL), Bundle: fixture.schema.Bundle,
		Bindings: bindings, Descriptors: fixture.app.descriptors, MutationLimits: limits,
		ResolvePrincipal: fixture.app.resolvePrincipal,
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}
