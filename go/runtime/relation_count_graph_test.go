package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type graphActor struct{}
type graphUser struct{}
type graphPost struct{}
type graphComment struct{}

func TestImmediateBatchedChildOwnsAuthorizedRelationCountSQLite(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.NewGraph(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "nested-batch-count.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	seedGraphRows(t, database, `"users"`, `"posts"`, `"comments"`)
	app, handles := openGraphApp(t, ctx, database, golem.SQLite, fixture.Bundle, fixture, true)
	assertImmediateBatchedChildCounts(t, ctx, app, handles)
}

type graphHandles struct {
	users        golem.ModelDescriptor[graphUser]
	userName     golem.TextField[graphUser, string]
	postTitle    golem.TextField[graphPost, string]
	commentBody  golem.TextField[graphComment, string]
	userPosts    golem.ToMany[graphUser, graphPost]
	postComments golem.ToMany[graphPost, graphComment]
}

func openGraphApp(t *testing.T, ctx context.Context, database *sqlx.DB, provider golem.Provider, bundle golem.SchemaBundle, fixture schematest.GraphFixture, scopeComments bool) (*App[struct{}, graphActor], graphHandles) {
	t.Helper()
	users := golem.GeneratedModelDescriptor[graphUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	posts := golem.GeneratedModelDescriptor[graphPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	comments := golem.GeneratedModelDescriptor[graphComment](fixture.Comment, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.CommentID, fixture.CommentPostID, fixture.CommentBody}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), users.Metadata(), posts.Metadata(), comments.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userName := golem.GeneratedTextField[graphUser, string](fixture.UserName)
	postTitle := golem.GeneratedTextField[graphPost, string](fixture.PostTitle)
	commentBody := golem.GeneratedTextField[graphComment, string](fixture.CommentBody)
	allowUser := golem.GeneratedPolicyBinding[graphActor, graphUser](fixture.User, func(graphActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[graphUser]()
		rules.CanRead(golem.All[graphUser]())
		return rules.Freeze(fixture.User)
	})
	allowPost := golem.GeneratedPolicyBinding[graphActor, graphPost](fixture.Post, func(graphActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[graphPost]()
		rules.CanRead(golem.All[graphPost]())
		return rules.Freeze(fixture.Post)
	})
	commentPolicy := golem.GeneratedPolicyBinding[graphActor, graphComment](fixture.Comment, func(graphActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[graphComment]()
		if scopeComments {
			rules.CanRead(commentBody.StartsWith("visible-"))
		} else {
			rules.CanRead(golem.All[graphComment]())
		}
		return rules.Freeze(fixture.Comment)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[graphActor]{allowUser, allowPost, commentPolicy}, nil)
	bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[struct{}, graphActor]{DB: database, Provider: provider, Bundle: bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(context.Context, struct{}) (graphActor, error) { return graphActor{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return app, graphHandles{
		users: users, userName: userName, postTitle: postTitle, commentBody: commentBody,
		userPosts:    golem.GeneratedToMany[graphUser, graphPost](fixture.UserPosts, fixture.Authorship, fixture.Post),
		postComments: golem.GeneratedToMany[graphPost, graphComment](fixture.PostComments, fixture.Commenting, fixture.Comment),
	}
}

func seedGraphRows(t *testing.T, database *sqlx.DB, usersTable, postsTable, commentsTable string) {
	t.Helper()
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := database.ExecContext(ctx, database.Rebind(query), args...); err != nil {
			t.Fatalf("seed %s: %v", query, err)
		}
	}
	exec(`INSERT INTO `+usersTable+`("id","name") VALUES (?,?)`, "00000000-0000-0000-0000-000000000001", "owner")
	exec(`INSERT INTO `+postsTable+`("id","author_id","title") VALUES (?,?,?)`, "00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "alpha")
	exec(`INSERT INTO `+postsTable+`("id","author_id","title") VALUES (?,?,?)`, "00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000001", "beta")
	for _, row := range [][3]string{
		{"00000000-0000-0000-0000-000000000021", "00000000-0000-0000-0000-000000000011", "visible-one"},
		{"00000000-0000-0000-0000-000000000022", "00000000-0000-0000-0000-000000000011", "visible-two"},
		{"00000000-0000-0000-0000-000000000023", "00000000-0000-0000-0000-000000000011", "hidden"},
		{"00000000-0000-0000-0000-000000000024", "00000000-0000-0000-0000-000000000012", "visible-three"},
	} {
		exec(`INSERT INTO `+commentsTable+`("id","post_id","body") VALUES (?,?,?)`, row[0], row[1], row[2])
	}
}

func assertImmediateBatchedChildCounts(t *testing.T, ctx context.Context, app *App[struct{}, graphActor], handles graphHandles) {
	t.Helper()
	caller, err := app.ForPrincipal(ctx, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := CallerFindMany(ctx, caller, handles.users,
		golem.Where(handles.userName.Eq("owner")),
		golem.Select[graphUser](handles.userName, handles.userPosts.Args(
			golem.OrderBy(handles.postTitle.Asc()),
			golem.Select[graphPost](handles.postTitle, handles.postComments.Count()),
		)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("users=%d", len(rows))
	}
	posts, present := golem.Many(rows[0], handles.userPosts).Get()
	if !present || len(posts) != 2 {
		t.Fatalf("batched posts=%d present=%t", len(posts), present)
	}
	for index, want := range []int64{2, 1} {
		count, countPresent := golem.RelationCount(posts[index], handles.postComments).Get()
		if !countPresent || count != want {
			t.Fatalf("post %d authorized comment count=%d present=%t want=%d", index, count, countPresent, want)
		}
		if golem.Many(posts[index], handles.postComments).IsSelected() {
			t.Fatalf("post %d count leaked comment rows", index)
		}
	}
}
