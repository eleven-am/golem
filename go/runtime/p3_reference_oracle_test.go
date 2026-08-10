package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

// referenceSocialRead deliberately knows nothing about Golem requests, policy
// IR, plans, SQL, or decoders. It is the executable specification for one
// fixed social read: authorize roots and children, order, page, and mask.
type referenceSocialUser struct {
	ID, Name string
}

type referenceSocialPost struct {
	ID, AuthorID, Title string
}

type referenceSocialActor struct {
	AllowedUserPrefix, PostPrefix string
}

type referenceSocialRow struct {
	Name  *string
	Posts []referenceSocialPost
}

var referenceSocialUsers = []referenceSocialUser{
	{ID: "00000000-0000-0000-0000-000000000001", Name: "hidden-open"},
	{ID: "00000000-0000-0000-0000-000000000002", Name: "visible-closed"},
	{ID: "00000000-0000-0000-0000-000000000003", Name: "visible-open"},
	{ID: "00000000-0000-0000-0000-000000000004", Name: "visible-later-open"},
}

var referenceSocialPosts = []referenceSocialPost{
	{ID: "00000000-0000-0000-0000-000000000011", AuthorID: "00000000-0000-0000-0000-000000000002", Title: "allowed-a"},
	{ID: "00000000-0000-0000-0000-000000000012", AuthorID: "00000000-0000-0000-0000-000000000002", Title: "allowed-z"},
	{ID: "00000000-0000-0000-0000-000000000013", AuthorID: "00000000-0000-0000-0000-000000000002", Title: "denied-top"},
	{ID: "00000000-0000-0000-0000-000000000014", AuthorID: "00000000-0000-0000-0000-000000000003", Title: "allowed-b"},
	{ID: "00000000-0000-0000-0000-000000000015", AuthorID: "00000000-0000-0000-0000-000000000004", Title: "allowed-later"},
}

func referenceSocialRead(users []referenceSocialUser, posts []referenceSocialPost, actor referenceSocialActor) []referenceSocialRow {
	authorized := make([]referenceSocialUser, 0, len(users))
	for _, user := range users {
		if strings.HasPrefix(user.Name, actor.AllowedUserPrefix) {
			authorized = append(authorized, user)
		}
	}
	sort.Slice(authorized, func(i, j int) bool { return authorized[i].ID < authorized[j].ID })
	if len(authorized) > 2 {
		authorized = authorized[:2]
	}
	result := make([]referenceSocialRow, 0, len(authorized))
	for _, user := range authorized {
		children := make([]referenceSocialPost, 0)
		for _, post := range posts {
			if post.AuthorID == user.ID && strings.HasPrefix(post.Title, actor.PostPrefix) {
				children = append(children, post)
			}
		}
		sort.Slice(children, func(i, j int) bool {
			if children[i].Title == children[j].Title {
				return children[i].ID > children[j].ID
			}
			return children[i].Title > children[j].Title
		})
		if len(children) > 1 {
			children = children[:1]
		}
		projectedChildren := make([]referenceSocialPost, len(children))
		for index, child := range children {
			projectedChildren[index] = referenceSocialPost{ID: child.ID, Title: child.Title}
		}
		row := referenceSocialRow{Posts: projectedChildren}
		if strings.HasSuffix(user.Name, "-open") {
			name := user.Name
			row.Name = &name
		}
		result = append(result, row)
	}
	return result
}

func TestP3IndependentReferenceOracleSQLite(t *testing.T) {
	fixture := schematest.New(t)
	database, _, err := sqliteprovider.New().Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "independent-reference.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqliteprovider.New().ApplyInitial(context.Background(), database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	assertIndependentReferenceAgreement(t, database, golem.SQLite, fixture.Bundle, fixture, `"users"`, `"posts"`)
}

func TestP3IndependentReferenceOraclePostgreSQLProfiles(t *testing.T) {
	profiles := []struct {
		name, environment string
	}{
		{name: "c", environment: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "linguistic", environment: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	}
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.environment))
			if dsn == "" {
				t.Skip(profile.environment + " is not configured")
			}
			for _, indexed := range []bool{false, true} {
				strategy := "batched"
				namespaceStrategy := "b"
				if indexed {
					strategy = "indexed-correlated"
					namespaceStrategy = "c"
				}
				t.Run(strategy, func(t *testing.T) {
					ctx := context.Background()
					fixture := schematest.New(t)
					if indexed {
						fixture = schematest.NewIndexed(t)
					}
					provider := postgresprovider.New()
					database, _, err := provider.Open(ctx, dsn)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = database.Close() })
					profileNamespace := profile.name[:1]
					namespace := physical.PhysicalName(fmt.Sprintf("golem_p3_ref_%s_%s_%d", profileNamespace, namespaceStrategy, time.Now().UnixNano()))
					schema := fixture.PostgreSQL
					schema.Namespace.Name = namespace
					_, _ = database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
					_, _ = database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "_golem" CASCADE`)
					t.Cleanup(func() {
						_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
						_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "_golem" CASCADE`)
					})
					if err := provider.ApplyInitial(ctx, database, schema); err != nil {
						t.Fatal(err)
					}
					bundle := postgresRuntimeBundle(t, fixture, schema)
					prefix := `"` + string(namespace) + `".`
					assertIndependentReferenceAgreement(t, database, golem.PostgreSQL, bundle, fixture, prefix+`"users"`, prefix+`"posts"`)
				})
			}
		})
	}
}

func assertIndependentReferenceAgreement(t *testing.T, database *sqlx.DB, provider golem.Provider, bundle golem.SchemaBundle, fixture schematest.Fixture, usersTable, postsTable string) {
	t.Helper()
	ctx := context.Background()
	for _, user := range referenceSocialUsers {
		query := database.Rebind(`INSERT INTO ` + usersTable + `("id","name") VALUES (?,?)`)
		if _, err := database.ExecContext(ctx, query, user.ID, user.Name); err != nil {
			t.Fatal(err)
		}
	}
	for _, post := range referenceSocialPosts {
		query := database.Rebind(`INSERT INTO ` + postsTable + `("id","author_id","title") VALUES (?,?,?)`)
		if _, err := database.ExecContext(ctx, query, post.ID, post.AuthorID, post.Title); err != nil {
			t.Fatal(err)
		}
	}

	users := golem.GeneratedModelDescriptor[oracleUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	posts := golem.GeneratedModelDescriptor[oraclePost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), users.Metadata(), posts.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userName := golem.GeneratedTextField[oracleUser, string](fixture.UserName)
	postID := golem.GeneratedEqualField[oraclePost, golem.UUID](fixture.PostID)
	postTitle := golem.GeneratedTextField[oraclePost, string](fixture.PostTitle)
	userPosts := golem.GeneratedToMany[oracleUser, oraclePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	userBinding := golem.GeneratedPolicyBinding[oracleActor, oracleUser](fixture.User, func(oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oracleUser]()
		rules.CanRead(golem.All[oracleUser]())
		rules.CanReadFields(golem.All[oracleUser](), golem.GeneratedEqualField[oracleUser, golem.UUID](fixture.UserID), userPosts)
		rules.CannotReadFields(golem.All[oracleUser](), userName)
		rules.CanReadFields(userName.EndsWith("-open"), userName)
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[oracleActor, oraclePost](fixture.Post, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oraclePost]()
		rules.CanRead(postTitle.StartsWith(actor.PostPrefix))
		return rules.Freeze(fixture.Post)
	})
	bindingsPackage := golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[oracleActor]{userBinding, postBinding}, nil)
	bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), bindingsPackage)
	if err != nil {
		t.Fatal(err)
	}
	application, err := Open(ctx, Config[oraclePrincipal, oracleActor]{
		Database: p8RuntimeTestDatabase(database, provider), Bundle: bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(_ context.Context, principal oraclePrincipal) (oracleActor, error) { return principal, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	actor := referenceSocialActor{PostPrefix: "allowed-"}
	want := referenceSocialRead(referenceSocialUsers, referenceSocialPosts, actor)
	caller, err := application.ForPrincipal(ctx, oraclePrincipal{UserPrefix: actor.AllowedUserPrefix, PostPrefix: actor.PostPrefix})
	if err != nil {
		t.Fatal(err)
	}
	for _, strategy := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "production", ctx: ctx},
		{name: "forced-batched", ctx: context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadBatched)},
	} {
		rows, err := CallerFindMany(strategy.ctx, caller, users,
			golem.Take[oracleUser](2),
			golem.Select[oracleUser](userName, userPosts.Args(
				golem.OrderBy(postTitle.Desc()), golem.Take[oraclePost](1), golem.Select[oraclePost](postID, postTitle),
			)),
		)
		if err != nil {
			t.Fatalf("reference runtime read (%s): %v: %v", strategy.name, err, errors.Unwrap(err))
		}
		got := make([]referenceSocialRow, len(rows))
		for index, row := range rows {
			name := golem.Value(row, userName)
			if value, present := name.Get(); present {
				got[index].Name = &value
			} else if !name.IsSelected() || !name.IsNull() {
				t.Fatalf("%s row %d masked name state=%d", strategy.name, index, name.State())
			}
			children, present := golem.Many(row, userPosts).Get()
			if !present || children == nil {
				t.Fatalf("%s row %d posts are absent or nil", strategy.name, index)
			}
			got[index].Posts = make([]referenceSocialPost, len(children))
			for childIndex, child := range children {
				id, idPresent := golem.Value(child, postID).Get()
				title, titlePresent := golem.Value(child, postTitle).Get()
				if !idPresent || !titlePresent {
					t.Fatalf("%s row %d child %d is incomplete", strategy.name, index, childIndex)
				}
				got[index].Posts[childIndex] = referenceSocialPost{ID: id.String(), Title: title}
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s independent reference disagreement\ngot:  %#v\nwant: %#v", strategy.name, got, want)
		}
	}
}
