package runtime

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
)

type p5SocialGraphQLHarness struct {
	server   *publicgraphql.Server[graphMutationPrincipal]
	contract compilerir.ContractIR
}

func newP5SocialGraphQLHarness(t testing.TB, fixture socialMutationFixture) p5SocialGraphQLHarness {
	t.Helper()
	bundle, compilation := p5GraphQLBundle(t, fixture.schema.Bundle)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := publicgraphql.NewGeneratedExecutor(publicgraphql.GeneratedExecutorConfig[graphMutationPrincipal]{
		Bundle: bundle,
		BeginCaller: func(ctx context.Context, principal graphMutationPrincipal) (publicgraphql.CallerExecution, error) {
			caller, callerErr := fixture.app.ForPrincipal(ctx, principal)
			if callerErr != nil {
				return nil, callerErr
			}
			return NewCallerMutationExecution(caller,
				CallerMutationModel[graphMutationPrincipal, graphMutationActor](fixture.userDescriptor),
				CallerMutationModel[graphMutationPrincipal, graphMutationActor](fixture.postDescriptor),
				CallerMutationModel[graphMutationPrincipal, graphMutationActor](fixture.commentDescriptor),
				CallerMutationModel[graphMutationPrincipal, graphMutationActor](fixture.friendshipDescriptor),
				CallerMutationModel[graphMutationPrincipal, graphMutationActor](fixture.tagDescriptor),
				CallerMutationModel[graphMutationPrincipal, graphMutationActor](fixture.postTagDescriptor),
			)
		},
		ReportInternalError: func(_ context.Context, err error) {
			t.Errorf("unexpected internal GraphQL error: %v", err)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := publicgraphql.NewServer(document.SDL, publicgraphql.Config[graphMutationPrincipal]{
		PrincipalFromContext: func(context.Context) (graphMutationPrincipal, bool) {
			return graphMutationPrincipal{}, true
		},
		ContractFingerprint: bundle.Contract().Fingerprint(),
		ReportInternalError: func(_ context.Context, err error) {
			t.Errorf("unexpected internal GraphQL server error: %v", err)
		},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	return p5SocialGraphQLHarness{server: server, contract: compilation.Contract}
}

func (h p5SocialGraphQLHarness) mutate(t testing.TB, query string) publicgraphql.Response {
	t.Helper()
	response := h.server.Execute(context.Background(), graphMutationPrincipal{}, publicgraphql.Request{Query: query})
	if len(response.Errors) != 0 {
		t.Fatalf("GraphQL mutation failed: %#v\n%s", response.Errors, query)
	}
	if response.Data == nil {
		t.Fatalf("GraphQL mutation returned no data\n%s", query)
	}
	return response
}

func (h p5SocialGraphQLHarness) mutationError(t testing.TB, query string, code golem.ErrorCode) publicgraphql.Response {
	t.Helper()
	response := h.server.Execute(context.Background(), graphMutationPrincipal{}, publicgraphql.Request{Query: query})
	if response.Data != nil || len(response.Errors) != 1 {
		t.Fatalf("GraphQL refusal = %#v, want one non-null-root error and nil data\n%s", response, query)
	}
	if got := response.Errors[0].Extensions["code"]; got != string(code) {
		t.Fatalf("GraphQL refusal code = %#v, want %q", got, code)
	}
	return response
}

func (h p5SocialGraphQLHarness) model(t testing.TB, name string) compilerir.ModelContractIR {
	t.Helper()
	for _, model := range h.contract.Models {
		if model.GraphQLName == name {
			return model
		}
	}
	t.Fatalf("GraphQL contract has no %s model", name)
	return compilerir.ModelContractIR{}
}

func p5UUID(value byte) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012x", value)
}

func TestGraphQLNestedMutationVocabularyExecutesCompleteSocialGraph(t *testing.T) {
	log := &socialHookLog{}
	fixture := newSocialMutationFixture(t, golem.ModelID{}, log)
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
	user, post := harness.model(t, "User"), harness.model(t, "Post")
	comment := harness.model(t, "Comment")
	userID, postID := p5UUID(1), p5UUID(10)
	commentID, replyID, friendID := p5UUID(20), p5UUID(21), p5UUID(2)

	harness.mutate(t, `mutation {
  `+user.Roots.Create+`(data: {
    id: "`+userID+`"
    name: "owner"
    posts: { create: [{
      id: "`+postID+`"
      title: "post"
      comments: { create: [{
        id: "`+commentID+`"
        body: "comment"
        author: { connect: { id: "`+userID+`" } }
        replies: { create: [{
          id: "`+replyID+`"
          body: "reply"
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
		t.Fatalf("GraphQL social hooks before=%v after=%v afterCommit=%v", log.before, log.after, log.afterCommit)
	}
	assertSocialFactModels(t, fixture, []golem.ModelID{
		fixture.schema.User, fixture.schema.Post, fixture.schema.Comment, fixture.schema.Comment,
		fixture.schema.PostTag, fixture.schema.Friendship,
	})

	ownerID := p5UUID(4)
	harness.mutate(t, `mutation { `+user.Roots.Create+`(data: { id: "`+ownerID+`", name: "vocabulary-owner" }) { id } }`)
	for _, seed := range []struct {
		id, title string
	}{{p5UUID(53), "connect-before"}, {p5UUID(54), "connect-or-create-before"}} {
		harness.mutate(t, `mutation { `+post.Roots.Create+`(data: {
      id: "`+seed.id+`", title: "`+seed.title+`"
      author: { connect: { id: "`+friendID+`" } }
    }) { id } }`)
	}

	// These roots exercise the complete eleven-operation nested vocabulary.
	// The operations are kept in separate GraphQL requests so each assertion
	// also proves the P4 transaction boundary is one root, never one document.
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `create: [{ id: "`+p5UUID(50)+`", title: "create" }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `createMany: [
    { id: "`+p5UUID(51)+`", title: "many-a" },
    { id: "`+p5UUID(52)+`", title: "many-b" }
  ]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `connect: [{ id: "`+p5UUID(53)+`" }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `connectOrCreate: [{
    where: { id: "`+p5UUID(54)+`" }
    create: { id: "`+p5UUID(54)+`", title: "unused" }
  }]`))
	harness.mutate(t, `mutation { `+comment.Roots.Update+`(
    where: { id: "`+commentID+`" }
    data: { replies: { disconnect: [{ id: "`+replyID+`" }] } }
  ) { id body } }`)
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `set: [
    { id: "`+p5UUID(50)+`" }, { id: "`+p5UUID(51)+`" }, { id: "`+p5UUID(52)+`" },
    { id: "`+p5UUID(53)+`" }, { id: "`+p5UUID(54)+`" }
  ]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `update: [{
    where: { id: "`+p5UUID(50)+`" }, data: { title: { set: "updated" } }
  }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `updateMany: [{
    where: { id: { in: ["`+p5UUID(51)+`", "`+p5UUID(52)+`"] } }
    data: { title: { set: "bulk-updated" } }
  }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `upsert: [{
    where: { id: "`+p5UUID(50)+`" }
    create: { id: "`+p5UUID(50)+`", title: "unused" }
    update: { title: { set: "upsert-updated" } }
  }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `upsert: [{
    where: { id: "`+p5UUID(55)+`" }
    create: { id: "`+p5UUID(55)+`", title: "upsert-created" }
    update: { title: { set: "unused" } }
  }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `delete: [{ id: "`+p5UUID(55)+`" }]`))
	harness.mutate(t, p5UserPostsUpdate(user, ownerID, `deleteMany: [{ id: { in: ["`+p5UUID(51)+`", "`+p5UUID(52)+`"] } }]`))

	posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	var title, author string
	query := fixture.app.database.Rebind(`SELECT "title","author_id" FROM ` + posts + ` WHERE "id"=?`)
	if err := fixture.app.database.QueryRowxContext(ctx, query, mutationResultUUIDText(50)).Scan(&title, &author); err != nil || title != "upsert-updated" || author != mutationResultUUIDText(4) {
		t.Fatalf("GraphQL nested survivor title=%q author=%q err=%v", title, author, err)
	}
	var removed int
	query = fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + posts + ` WHERE "id" IN (?, ?, ?)`)
	if err := fixture.app.database.GetContext(ctx, &removed, query, mutationResultUUIDText(51), mutationResultUUIDText(52), mutationResultUUIDText(55)); err != nil || removed != 0 {
		t.Fatalf("GraphQL nested deletes remaining=%d err=%v", removed, err)
	}
	var parentID *string
	comments := nestedAcceptanceTable(fixture.app, fixture.schema.Comment)
	query = fixture.app.database.Rebind(`SELECT "parent_id" FROM ` + comments + ` WHERE "id"=?`)
	if err := fixture.app.database.GetContext(ctx, &parentID, query, mutationResultUUIDText(21)); err != nil || parentID != nil {
		t.Fatalf("GraphQL nested disconnect parent=%v err=%v", parentID, err)
	}
}

func p5UserPostsUpdate(user compilerir.ModelContractIR, ownerID, relationBody string) string {
	return `mutation { ` + user.Roots.Update + `(
    where: { id: "` + ownerID + `" }
    data: { posts: { ` + relationBody + ` } }
  ) { id name } }`
}

func TestGraphQLNestedDenialAndLimitOverflowRollBackDataHooksAndFacts(t *testing.T) {
	t.Run("independent-child-authorization-denial", func(t *testing.T) {
		log := &socialHookLog{}
		fixture := newSocialMutationFixture(t, golem.ModelID{}, log)
		fixture = p5ReopenSocialMutation(t, fixture, fixture.schema.Post, log, MutationLimits{})
		assertP5GraphQLNestedRollback(t, fixture, log, golem.CodeForbidden)
	})
	t.Run("touched-graph-limit-overflow", func(t *testing.T) {
		log := &socialHookLog{}
		fixture := newSocialMutationFixture(t, golem.ModelID{}, log)
		fixture = p5ReopenSocialMutation(t, fixture, golem.ModelID{}, log, MutationLimits{MaxTouchedRows: 1})
		assertP5GraphQLNestedRollback(t, fixture, log, golem.CodeBadUserInput)
	})
}

func p5ReopenSocialMutation(t testing.TB, fixture socialMutationFixture, denied golem.ModelID, log *socialHookLog, limits MutationLimits) socialMutationFixture {
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
	app, err := Open(context.Background(), Config[graphMutationPrincipal, graphMutationActor]{
		DB: fixture.app.database, Provider: golem.SQLite, Bundle: fixture.schema.Bundle,
		Bindings: bindings, Descriptors: fixture.app.descriptors, MutationLimits: limits,
		ResolvePrincipal: fixture.app.resolvePrincipal,
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}

func assertP5GraphQLNestedRollback(t testing.TB, fixture socialMutationFixture, log *socialHookLog, wantCode golem.ErrorCode) {
	t.Helper()
	ctx := context.Background()
	if _, err := SystemCreate(ctx, fixture.app.System(), fixture.userDescriptor, fixture.userCreate(1, "before")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.database.ExecContext(ctx, `DELETE FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil {
		t.Fatal(err)
	}
	log.before, log.after, log.afterCommit = nil, nil, nil
	harness := newP5SocialGraphQLHarness(t, fixture)
	user := harness.model(t, "User")
	query := `mutation { ` + user.Roots.Update + `(
    where: { id: "` + p5UUID(1) + `" }
    data: {
      name: { set: "must-roll-back" }
      posts: { create: [{ id: "` + p5UUID(60) + `", title: "must-roll-back" }] }
    }
  ) { id name } }`
	response := harness.mutationError(t, query, wantCode)
	if len(response.Errors[0].Path) != 1 || response.Errors[0].Path[0] != user.Roots.Update {
		t.Fatalf("GraphQL nested refusal path = %#v", response.Errors[0].Path)
	}
	var name string
	users := nestedAcceptanceTable(fixture.app, fixture.schema.User)
	databaseQuery := fixture.app.database.Rebind(`SELECT "name" FROM ` + users + ` WHERE "id"=?`)
	if err := fixture.app.database.GetContext(ctx, &name, databaseQuery, mutationResultUUIDText(1)); err != nil || name != "before" {
		t.Fatalf("rolled-back root name=%q err=%v", name, err)
	}
	var posts, facts int
	if err := fixture.app.database.GetContext(ctx, &posts, `SELECT COUNT(*) FROM `+nestedAcceptanceTable(fixture.app, fixture.schema.Post)); err != nil || posts != 0 {
		t.Fatalf("rolled-back child rows=%d err=%v", posts, err)
	}
	if err := fixture.app.database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+nestedAcceptanceOutbox(fixture.app)); err != nil || facts != 0 {
		t.Fatalf("rolled-back outbox facts=%d err=%v", facts, err)
	}
	if !reflect.DeepEqual(log.before, []string{"post"}) || len(log.after) != 0 || len(log.afterCommit) != 0 {
		t.Fatalf("rolled-back hook trace before=%v after=%v afterCommit=%v", log.before, log.after, log.afterCommit)
	}
}
