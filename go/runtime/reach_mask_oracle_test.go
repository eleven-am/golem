package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

type reachUser struct {
	id   string
	name string
}

type reachPost struct {
	id     string
	author string
	title  string
}

var reachUsers = []reachUser{
	{"00000000-0000-0000-0000-000000000001", "alice"},
	{"00000000-0000-0000-0000-000000000002", "bob"},
	{"00000000-0000-0000-0000-000000000003", "carol"},
	{"00000000-0000-0000-0000-000000000004", "dave"},
}

var reachPosts = []reachPost{
	{"00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "allowed-mask"},
	{"00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000001", "allowed-public"},
	{"00000000-0000-0000-0000-000000000013", "00000000-0000-0000-0000-000000000002", "denied-mask"},
	{"00000000-0000-0000-0000-000000000014", "00000000-0000-0000-0000-000000000004", "allowed-public"},
	{"00000000-0000-0000-0000-000000000015", "00000000-0000-0000-0000-000000000004", "denied-public"},
}

type reachHandles struct {
	users    golem.ModelDescriptor[testUser]
	posts    golem.ModelDescriptor[testPost]
	userID   golem.EqualField[testUser, golem.UUID]
	userName golem.TextField[testUser, string]
	postID   golem.EqualField[testPost, golem.UUID]
	title    golem.TextField[testPost, string]
	author   golem.ToOne[testPost, testUser]
	written  golem.ToMany[testUser, testPost]
}

func newReachHandles(fixture schematest.Fixture) reachHandles {
	return reachHandles{
		users:    golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil)),
		posts:    golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil)),
		userID:   golem.GeneratedEqualField[testUser, golem.UUID](fixture.UserID),
		userName: golem.GeneratedTextField[testUser, string](fixture.UserName),
		postID:   golem.GeneratedEqualField[testPost, golem.UUID](fixture.PostID),
		title:    golem.GeneratedTextField[testPost, string](fixture.PostTitle),
		author:   golem.GeneratedToOne[testPost, testUser](fixture.PostAuthor, fixture.Authorship, fixture.User),
		written:  golem.GeneratedToMany[testUser, testPost](fixture.UserPosts, fixture.Authorship, fixture.Post),
	}
}

type reachPolicies struct {
	user func(*golem.Rules[testUser])
	post func(*golem.Rules[testPost])
}

func openReachApp(t *testing.T, ctx context.Context, database *sqlx.DB, provider golem.Provider, bundle golem.SchemaBundle, fixture schematest.Fixture, handles reachHandles, policies reachPolicies) *App[testPrincipal, testActor] {
	t.Helper()
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), handles.users.Metadata(), handles.posts.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userBinding := golem.GeneratedPolicyBinding[testActor, testUser](fixture.User, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testUser]()
		policies.user(rules)
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		policies.post(rules)
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{userBinding, postBinding}, nil)
	bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[testPrincipal, testActor]{Database: p8RuntimeTestDatabase(database, provider), Bundle: bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(context.Context, testPrincipal) (testActor, error) { return testActor{}, nil }, AuditPrincipal: func(testPrincipal) string { return "reach" }, ReportScopedQuery: func(context.Context, golem.ScopedAuditRecord) {}})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func seedReachRows(t *testing.T, database *sqlx.DB, users, posts string) {
	t.Helper()
	ctx := context.Background()
	for _, row := range reachUsers {
		if _, err := database.ExecContext(ctx, database.Rebind(`INSERT INTO `+users+`("id","name") VALUES (?,?)`), row.id, row.name); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range reachPosts {
		if _, err := database.ExecContext(ctx, database.Rebind(`INSERT INTO `+posts+`("id","author_id","title","big_int","decimal_value") VALUES (?,?,?,?,?)`), row.id, row.author, row.title, int64(0), "0"); err != nil {
			t.Fatal(err)
		}
	}
}

type reachDatabase struct {
	name     string
	database *sqlx.DB
	provider golem.Provider
	bundle   golem.SchemaBundle
}

func reachDatabases(t *testing.T, purpose string, fixture schematest.Fixture) []func(*testing.T) reachDatabase {
	t.Helper()
	opens := []func(*testing.T) reachDatabase{
		func(t *testing.T) reachDatabase {
			database, _, err := sqlite.New().Open(context.Background(), "file:"+filepath.Join(t.TempDir(), purpose+".db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			if err := sqlite.New().ApplyInitial(context.Background(), database, fixture.SQLite); err != nil {
				t.Fatal(err)
			}
			seedReachRows(t, database, `"users"`, `"posts"`)
			return reachDatabase{name: "sqlite", database: database, provider: golem.SQLite, bundle: fixture.Bundle}
		},
	}
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		opens = append(opens, func(t *testing.T) reachDatabase {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			database, bundle, namespace := openPostgresAcceptanceSchema(t, context.Background(), profile, fixture.Bundle, fixture.PostgreSQL, purpose)
			seedReachRows(t, database, qualifiedPostgresTable(namespace, "users"), qualifiedPostgresTable(namespace, "posts"))
			return reachDatabase{name: "postgres_" + profile.name, database: database, provider: golem.PostgreSQL, bundle: bundle}
		})
	}
	return opens
}

func reachDatabaseName(index int) string {
	if index == 0 {
		return "sqlite"
	}
	return "postgres_" + postgresAcceptanceProfiles()[index-1].name
}

func readablePostTitle(title string) bool { return strings.HasPrefix(title, "allowed-") }

func visiblePostsOf(user reachUser) []reachPost {
	result := make([]reachPost, 0)
	for _, post := range reachPosts {
		if post.author == user.id && readablePostTitle(post.title) {
			result = append(result, post)
		}
	}
	return result
}

func reachAny(posts []reachPost, match func(reachPost) bool) bool {
	for _, post := range posts {
		if match(post) {
			return true
		}
	}
	return false
}

type reachMaskCase struct {
	name     string
	mask     func(reachHandles) golem.Predicate[testUser]
	expected func(reachUser) bool
}

func reachMaskCases() []reachMaskCase {
	containsMask := func(post reachPost) bool { return strings.Contains(post.title, "mask") }
	endsPublic := func(post reachPost) bool { return strings.HasSuffix(post.title, "public") }
	return []reachMaskCase{
		{
			name:     "SomeContains",
			mask:     func(h reachHandles) golem.Predicate[testUser] { return h.written.Some(h.title.Contains("mask")) },
			expected: func(user reachUser) bool { return reachAny(visiblePostsOf(user), containsMask) },
		},
		{
			name:     "NoneContains",
			mask:     func(h reachHandles) golem.Predicate[testUser] { return h.written.None(h.title.Contains("mask")) },
			expected: func(user reachUser) bool { return !reachAny(visiblePostsOf(user), containsMask) },
		},
		{
			name: "EveryAllowed",
			mask: func(h reachHandles) golem.Predicate[testUser] { return h.written.Every(h.title.StartsWith("allowed-")) },
			expected: func(user reachUser) bool {
				return !reachAny(visiblePostsOf(user), func(post reachPost) bool { return !strings.HasPrefix(post.title, "allowed-") })
			},
		},
		{
			name:     "SomeAll",
			mask:     func(h reachHandles) golem.Predicate[testUser] { return h.written.Some(golem.All[testPost]()) },
			expected: func(user reachUser) bool { return len(visiblePostsOf(user)) != 0 },
		},
		{
			name: "NotSomePublic",
			mask: func(h reachHandles) golem.Predicate[testUser] {
				return golem.Not(h.written.Some(h.title.EndsWith("public")))
			},
			expected: func(user reachUser) bool { return !reachAny(visiblePostsOf(user), endsPublic) },
		},
		{
			name: "ScalarOrRelation",
			mask: func(h reachHandles) golem.Predicate[testUser] {
				return h.userName.Eq("carol").Or(h.written.Some(h.title.Contains("mask")))
			},
			expected: func(user reachUser) bool { return user.name == "carol" || reachAny(visiblePostsOf(user), containsMask) },
		},
	}
}

func reachMaskPolicies(handles reachHandles, mask golem.Predicate[testUser]) reachPolicies {
	return reachPolicies{
		user: func(rules *golem.Rules[testUser]) {
			rules.CanRead(golem.All[testUser]())
			rules.CannotReadFields(golem.All[testUser](), handles.userName)
			rules.CanReadFields(mask, handles.userName)
		},
		post: func(rules *golem.Rules[testPost]) {
			rules.CanRead(handles.title.StartsWith("allowed-"))
		},
	}
}

func inProcessNameVisibility(t *testing.T, ctx context.Context, caller *Caller[testPrincipal, testActor], handles reachHandles) map[string]bool {
	t.Helper()
	rows, err := CallerFindMany(ctx, caller, handles.users, golem.Select[testUser](handles.userID, handles.userName))
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]bool, len(rows))
	for _, row := range rows {
		id, present := golem.Value(row, handles.userID).Get()
		if !present {
			t.Fatal("in-process row lost its identity")
		}
		name := golem.Value(row, handles.userName)
		if !name.IsSelected() {
			t.Fatal("in-process row lost the selected name")
		}
		result[id.String()] = !name.IsNull()
	}
	return result
}

func scopedNameVisibility(t *testing.T, ctx context.Context, caller *Caller[testPrincipal, testActor], fixture schematest.Fixture, handles reachHandles) (map[string]bool, map[string]string) {
	t.Helper()
	scope := golem.GeneratedScope[testUser](fixture.User)
	id := golem.GeneratedScopedEqualField(scope, handles.userID)
	name := golem.GeneratedScopedTextField(scope, handles.userName)
	rows, err := CallerScoped(ctx, caller, handles.users, golem.From(scope).Select(id, name))
	if err != nil {
		t.Fatal(err)
	}
	visible := make(map[string]bool, len(rows))
	values := make(map[string]string, len(rows))
	for _, row := range rows {
		identity, present := golem.ScopedValue(row, id).Get()
		if !present {
			t.Fatal("scoped row lost its identity")
		}
		value, shown := golem.ScopedValue(row, name).Get()
		visible[identity.String()] = shown
		if shown {
			values[identity.String()] = value
		}
	}
	return visible, values
}

func TestReachMaskInProcessAgreesWithSQLMaskAcrossProviders(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	handles := newReachHandles(fixture)
	for index, open := range reachDatabases(t, "reach_oracle", fixture) {
		open := open
		t.Run(reachDatabaseName(index), func(t *testing.T) {
			target := open(t)
			for _, candidate := range reachMaskCases() {
				t.Run(candidate.name, func(t *testing.T) {
					ctx := context.Background()
					app := openReachApp(t, ctx, target.database, target.provider, target.bundle, fixture, handles, reachMaskPolicies(handles, candidate.mask(handles)))
					caller, err := app.ForPrincipal(ctx, testPrincipal{})
					if err != nil {
						t.Fatal(err)
					}
					inProcess := inProcessNameVisibility(t, ctx, caller, handles)
					inSQL, _ := scopedNameVisibility(t, ctx, caller, fixture, handles)
					if len(inProcess) != len(reachUsers) || len(inSQL) != len(reachUsers) {
						t.Fatalf("rows in-process=%d sql=%d want=%d", len(inProcess), len(inSQL), len(reachUsers))
					}
					for _, user := range reachUsers {
						want := candidate.expected(user)
						if inProcess[user.id] != want {
							t.Errorf("%s in-process visible=%v want=%v", user.name, inProcess[user.id], want)
						}
						if inSQL[user.id] != want {
							t.Errorf("%s SQL mask visible=%v want=%v", user.name, inSQL[user.id], want)
						}
					}
				})
			}
		})
	}
}

func TestSQLFieldMaskOverUnreadableRelationTargetDoesNotRevealIt(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	handles := newReachHandles(fixture)
	for index, open := range reachDatabases(t, "reach_leak", fixture) {
		open := open
		t.Run(reachDatabaseName(index), func(t *testing.T) {
			target := open(t)
			ctx := context.Background()
			app := openReachApp(t, ctx, target.database, target.provider, target.bundle, fixture, handles, reachMaskPolicies(handles, handles.written.Some(handles.title.Contains("mask"))))
			caller, err := app.ForPrincipal(ctx, testPrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			_, values := scopedNameVisibility(t, ctx, caller, fixture, handles)
			if name, revealed := values[reachUsers[1].id]; revealed {
				t.Fatalf("the SQL mask revealed %q through a post the caller cannot read", name)
			}
			if values[reachUsers[0].id] != "alice" {
				t.Fatalf("the SQL mask hid a name its readable post grants: %q", values[reachUsers[0].id])
			}
		})
	}
}

func TestCallerReadWithNonRestatingRelationPolicySucceeds(t *testing.T) {
	fixture := schematest.NewIndexedExactScoped(t)
	handles := newReachHandles(fixture)
	policies := reachPolicies{
		user: func(rules *golem.Rules[testUser]) {
			rules.CanRead(handles.userName.StartsWith("a").Or(handles.userName.StartsWith("b")))
		},
		post: func(rules *golem.Rules[testPost]) {
			rules.CanRead(handles.author.Is(handles.userName.EndsWith("e")))
		},
	}
	for index, open := range reachDatabases(t, "reach_caller", fixture) {
		open := open
		t.Run(reachDatabaseName(index), func(t *testing.T) {
			target := open(t)
			ctx := context.Background()
			app := openReachApp(t, ctx, target.database, target.provider, target.bundle, fixture, handles, policies)
			caller, err := app.ForPrincipal(ctx, testPrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			rows, err := CallerFindMany(ctx, caller, handles.posts, golem.Where(handles.title.StartsWith("allowed-")), golem.OrderBy(handles.postID.Asc()), golem.Select[testPost](handles.postID, handles.title))
			if err != nil {
				t.Fatalf("caller read under a non-restating relation policy refused: %v", err)
			}
			got := make([]string, 0, len(rows))
			for _, row := range rows {
				id, _ := golem.Value(row, handles.postID).Get()
				got = append(got, id.String())
			}
			want := []string{reachPosts[0].id, reachPosts[1].id}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("rows=%v want=%v; dave matches the authored rule but is not readable, bob is readable but does not match it", got, want)
			}
			count, err := CallerCount(ctx, caller, handles.posts, golem.Where(handles.author.Is(handles.userName.Contains("a"))))
			if err != nil {
				t.Fatalf("caller count with a relation filter refused: %v", err)
			}
			if count != 2 {
				t.Fatalf("count=%d want=2", count)
			}
		})
	}
}
