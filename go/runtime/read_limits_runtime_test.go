package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type limitHandles struct {
	users     golem.ModelDescriptor[testUser]
	userID    golem.EqualField[testUser, golem.UUID]
	userName  golem.TextField[testUser, string]
	postTitle golem.TextField[testPost, string]
	posts     golem.ToMany[testUser, testPost]
}

func TestRuntimeReadLimitsRefuseOverflowWithoutSilentTruncationAndAreIsolated(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.NewIndexed(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "read-limits.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	seedReadLimitRows(t, database)

	limited, handles := openReadLimitApp(t, ctx, database, fixture, ReadLimits{MaxTake: 2, MaxRelationFanout: 2})
	unlimited, _ := openReadLimitApp(t, ctx, database, fixture, ReadLimits{})
	if limited.readLimits.plan.MaxTake != 2 || limited.readLimits.plan.MaxRelationFanout != 2 || unlimited.readLimits.plan.MaxTake != 0 || unlimited.readLimits.plan.MaxRelationFanout != 0 {
		t.Fatalf("per-app normalized limits leaked: limited=%+v unlimited=%+v", limited.readLimits.plan, unlimited.readLimits.plan)
	}
	rootOptions := []golem.ReadOption[testUser]{golem.OrderBy(handles.userID.Asc()), golem.Select[testUser](handles.userName)}

	rows, err := SystemFindMany(ctx, limited.System(), handles.users, rootOptions...)
	assertReadLimitFailure(t, rows, err, golem.FieldID{}, "root read limit exceeded")
	rows, err = SystemFindMany(ctx, limited.System(), handles.users, golem.OrderBy(handles.userID.Asc()), golem.Take[testUser](2), golem.Select[testUser](handles.userName))
	if err != nil || len(rows) != 2 {
		t.Fatalf("explicit exact root take rows=%d err=%v", len(rows), err)
	}
	rows, err = SystemFindMany(ctx, unlimited.System(), handles.users, rootOptions...)
	if err != nil || len(rows) != 3 {
		t.Fatalf("unlimited isolated app rows=%d err=%v", len(rows), err)
	}

	strategies := []struct {
		name string
		ctx  context.Context
	}{
		{name: "production_correlated", ctx: ctx},
		{name: "forced_batched", ctx: context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadBatched)},
	}
	for _, strategy := range strategies {
		strategy := strategy
		t.Run(strategy.name, func(t *testing.T) {
			// Bob owns exactly two posts, so cap+1 probing must preserve the
			// exact boundary without returning a sentinel row.
			exact, exactErr := SystemFindMany(strategy.ctx, limited.System(), handles.users,
				golem.Where(handles.userName.Eq("bob")),
				golem.Select[testUser](handles.userName, handles.posts.Select(handles.postTitle)),
			)
			if exactErr != nil || len(exact) != 1 {
				t.Fatalf("exact parent rows=%d err=%v", len(exact), exactErr)
			}
			children, present := golem.Many(exact[0], handles.posts).Get()
			if !present || len(children) != 2 {
				t.Fatalf("exact fanout=%d present=%t", len(children), present)
			}

			// Alice owns three posts. An unpaged relation must fail rather than
			// looking like a successful two-row page.
			overflow, overflowErr := SystemFindMany(strategy.ctx, limited.System(), handles.users,
				golem.Where(handles.userName.Eq("alice")),
				golem.Select[testUser](handles.userName, handles.posts.Select(handles.postTitle)),
			)
			assertReadLimitFailure(t, overflow, overflowErr, fixture.UserPosts, "relation fanout limit exceeded")

			// An explicit page at the configured boundary is intentionally a
			// page, not an overflow probe.
			paged, pagedErr := SystemFindMany(strategy.ctx, limited.System(), handles.users,
				golem.Where(handles.userName.Eq("alice")),
				golem.Select[testUser](handles.posts.Args(golem.Take[testPost](2), golem.Select[testPost](handles.postTitle))),
			)
			if pagedErr != nil || len(paged) != 1 {
				t.Fatalf("paged parent rows=%d err=%v", len(paged), pagedErr)
			}
			children, present = golem.Many(paged[0], handles.posts).Get()
			if !present || len(children) != 2 {
				t.Fatalf("explicit relation page=%d present=%t", len(children), present)
			}
		})
	}
}

func assertReadLimitFailure[M any](t *testing.T, rows []golem.Row[M], err error, field golem.FieldID, message string) {
	t.Helper()
	var failure *golem.Error
	if len(rows) != 0 || !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput || failure.Field != field || failure.Message != message {
		t.Fatalf("rows=%d error=%v", len(rows), err)
	}
}

func openReadLimitApp(t *testing.T, ctx context.Context, database *sqlx.DB, fixture schematest.Fixture, limits ReadLimits) (*App[testPrincipal, testActor], limitHandles) {
	t.Helper()
	users := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), users.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	allowUser := golem.GeneratedPolicyBinding[testActor, testUser](fixture.User, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testUser]()
		rules.CanRead(golem.All[testUser]())
		return rules.Freeze(fixture.User)
	})
	allowPost := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		rules.CanRead(golem.All[testPost]())
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{allowUser, allowPost}, nil)
	bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[testPrincipal, testActor]{
		DB: database, Provider: golem.SQLite, Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors, ReadLimits: limits,
		ResolvePrincipal: func(context.Context, testPrincipal) (testActor, error) { return testActor{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, limitHandles{
		users:  users,
		userID: golem.GeneratedEqualField[testUser, golem.UUID](fixture.UserID), userName: golem.GeneratedTextField[testUser, string](fixture.UserName),
		postTitle: golem.GeneratedTextField[testPost, string](fixture.PostTitle),
		posts:     golem.GeneratedToMany[testUser, testPost](fixture.UserPosts, fixture.Authorship, fixture.Post),
	}
}

func seedReadLimitRows(t *testing.T, database *sqlx.DB) {
	t.Helper()
	ctx := context.Background()
	for _, row := range [][2]string{
		{"00000000-0000-0000-0000-000000000001", "alice"},
		{"00000000-0000-0000-0000-000000000002", "bob"},
		{"00000000-0000-0000-0000-000000000003", "carol"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "users"("id","name") VALUES (?,?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range [][3]string{
		{"00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "a-1"},
		{"00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000001", "a-2"},
		{"00000000-0000-0000-0000-000000000013", "00000000-0000-0000-0000-000000000001", "a-3"},
		{"00000000-0000-0000-0000-000000000021", "00000000-0000-0000-0000-000000000002", "b-1"},
		{"00000000-0000-0000-0000-000000000022", "00000000-0000-0000-0000-000000000002", "b-2"},
	} {
		if _, err := database.ExecContext(ctx, `INSERT INTO "posts"("id","author_id","title") VALUES (?,?,?)`, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
}
