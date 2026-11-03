package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
)

type identityGuardPrincipal struct{}
type identityGuardActor struct{}
type identityGuardUser struct{}
type identityGuardPost struct{}

type identityGuardHarness struct {
	profile   exactValueProfile
	app       *App[identityGuardPrincipal, identityGuardActor]
	schema    schematest.Fixture
	users     golem.ModelDescriptor[identityGuardUser]
	posts     golem.ModelDescriptor[identityGuardPost]
	userID    golem.EqualField[identityGuardUser, golem.UUID]
	userName  golem.TextField[identityGuardUser, string]
	postID    golem.EqualField[identityGuardPost, golem.UUID]
	authorID  golem.EqualField[identityGuardPost, golem.UUID]
	postTitle golem.TextField[identityGuardPost, string]
	userPosts golem.ToMany[identityGuardUser, identityGuardPost]
}

func runIdentityGuard(t *testing.T, operation func(*testing.T, *identityGuardHarness)) {
	t.Helper()
	runIdentityGuardWithKeyMask(t, false, operation)
}

func runIdentityGuardWithKeyMask(t *testing.T, maskKey bool, operation func(*testing.T, *identityGuardHarness)) {
	t.Helper()
	runExactValueProfilesFor(t, schematest.NewIndexed, func(t *testing.T, profile exactValueProfile) {
		harness := openIdentityGuard(t, profile, maskKey)
		harness.seed(t)
		operation(t, harness)
	})
}

func guardProvider(profile exactValueProfile) golem.Provider {
	if profile.provider == policyir.ProviderPostgreSQL {
		return golem.PostgreSQL
	}
	return golem.SQLite
}

func openIdentityGuard(t *testing.T, profile exactValueProfile, maskKey bool) *identityGuardHarness {
	t.Helper()
	fixture := profile.fixture
	userIdentity := golem.GeneratedIdentityMetadata(fixture.User, fixture.UserKey, golem.PrimaryIdentity, fixture.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(fixture.Post, fixture.PostKey, golem.PrimaryIdentity, fixture.PostID)
	userRelation := golem.GeneratedRelationMetadata(fixture.User, fixture.Post, fixture.UserPosts, fixture.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(fixture.Post, fixture.User, fixture.PostAuthor, fixture.Authorship, golem.RelationSource, golem.RelationToOne)
	users := golem.GeneratedModelDescriptor[identityGuardUser](fixture.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.UserID, fixture.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation}))
	posts := golem.GeneratedModelDescriptor[identityGuardPost](fixture.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation}))
	descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(),
		golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), users.Metadata(), posts.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	allowUsers := golem.GeneratedPolicyBinding[identityGuardActor, identityGuardUser](fixture.User, func(identityGuardActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[identityGuardUser]()
		rules.CanRead(golem.All[identityGuardUser]())
		return rules.Freeze(fixture.User)
	})
	allowPosts := golem.GeneratedPolicyBinding[identityGuardActor, identityGuardPost](fixture.Post, func(identityGuardActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[identityGuardPost]()
		rules.CanRead(golem.All[identityGuardPost]())
		if maskKey {
			rules.CannotReadFields(golem.All[identityGuardPost](), golem.GeneratedEqualField[identityGuardPost, golem.UUID](fixture.PostID))
			rules.CanReadFields(golem.GeneratedTextField[identityGuardPost, string](fixture.PostTitle).StartsWith("p-a"), golem.GeneratedEqualField[identityGuardPost, golem.UUID](fixture.PostID))
		}
		return rules.Freeze(fixture.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(),
		golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[identityGuardActor]{allowUsers, allowPosts}, nil))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config[identityGuardPrincipal, identityGuardActor]{
		Database: p8RuntimeTestDatabase(profile.database, guardProvider(profile)), Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(context.Context, identityGuardPrincipal) (identityGuardActor, error) {
			return identityGuardActor{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &identityGuardHarness{
		profile: profile, app: app, schema: fixture, users: users, posts: posts,
		userID:    golem.GeneratedEqualField[identityGuardUser, golem.UUID](fixture.UserID),
		userName:  golem.GeneratedTextField[identityGuardUser, string](fixture.UserName),
		postID:    golem.GeneratedEqualField[identityGuardPost, golem.UUID](fixture.PostID),
		authorID:  golem.GeneratedEqualField[identityGuardPost, golem.UUID](fixture.AuthorID),
		postTitle: golem.GeneratedTextField[identityGuardPost, string](fixture.PostTitle),
		userPosts: golem.GeneratedToMany[identityGuardUser, identityGuardPost](fixture.UserPosts, fixture.Authorship, fixture.Post),
	}
}

func (h *identityGuardHarness) seed(t *testing.T) {
	t.Helper()
	profile := h.profile
	for _, user := range []struct {
		id   byte
		name string
	}{{1, "alice"}, {2, "bob"}, {3, "carol"}, {4, "dave"}, {5, "erin"}} {
		if _, err := profile.database.ExecContext(context.Background(), `INSERT INTO `+profile.prefix+`"users" ("id","name") VALUES (`+profile.placeholder(1)+`,`+profile.placeholder(2)+`)`, mutationResultUUIDText(user.id), user.name); err != nil {
			t.Fatal(err)
		}
	}
	for _, post := range []struct {
		id, author byte
		title      string
	}{{11, 1, "p-a"}, {12, 1, "p-b"}, {13, 2, "p-c"}, {14, 1, "p-d"}, {15, 3, "p-a"}, {16, 1, "p-a"}} {
		if _, err := profile.database.ExecContext(context.Background(), `INSERT INTO `+profile.prefix+`"posts" ("id","author_id","title") VALUES (`+profile.placeholder(1)+`,`+profile.placeholder(2)+`,`+profile.placeholder(3)+`)`, mutationResultUUIDText(post.id), mutationResultUUIDText(post.author), post.title); err != nil {
			t.Fatal(err)
		}
	}
}

type guardReader struct {
	name  string
	posts func(context.Context, ...golem.ReadOption[identityGuardPost]) ([]golem.Row[identityGuardPost], error)
	users func(context.Context, ...golem.ReadOption[identityGuardUser]) ([]golem.Row[identityGuardUser], error)
}

func (h *identityGuardHarness) readers(t *testing.T) []guardReader {
	t.Helper()
	caller, err := h.app.ForPrincipal(context.Background(), identityGuardPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	return []guardReader{
		{
			name: "system",
			posts: func(ctx context.Context, options ...golem.ReadOption[identityGuardPost]) ([]golem.Row[identityGuardPost], error) {
				return SystemFindMany(ctx, h.app.System(), h.posts, options...)
			},
			users: func(ctx context.Context, options ...golem.ReadOption[identityGuardUser]) ([]golem.Row[identityGuardUser], error) {
				return SystemFindMany(ctx, h.app.System(), h.users, options...)
			},
		},
		{
			name: "caller",
			posts: func(ctx context.Context, options ...golem.ReadOption[identityGuardPost]) ([]golem.Row[identityGuardPost], error) {
				return CallerFindMany(ctx, caller, h.posts, options...)
			},
			users: func(ctx context.Context, options ...golem.ReadOption[identityGuardUser]) ([]golem.Row[identityGuardUser], error) {
				return CallerFindMany(ctx, caller, h.users, options...)
			},
		},
	}
}

func (h *identityGuardHarness) postCells(t *testing.T, rows []golem.Row[identityGuardPost]) [][3]string {
	t.Helper()
	cells := make([][3]string, len(rows))
	for index, row := range rows {
		id, idPresent := golem.Value(row, h.postID).Get()
		author, authorPresent := golem.Value(row, h.authorID).Get()
		title, titlePresent := golem.Value(row, h.postTitle).Get()
		if idPresent {
			cells[index][0] = id.String()
		}
		if authorPresent {
			cells[index][1] = author.String()
		}
		if titlePresent {
			cells[index][2] = title
		}
	}
	return cells
}

func (h *identityGuardHarness) postTitles(t *testing.T, rows []golem.Row[identityGuardPost]) []string {
	t.Helper()
	titles := make([]string, len(rows))
	for index, row := range rows {
		title, present := golem.Value(row, h.postTitle).Get()
		if !present {
			t.Fatalf("row %d has no selected title", index)
		}
		titles[index] = title
	}
	return titles
}

func (h *identityGuardHarness) postIDs(t *testing.T, rows []golem.Row[identityGuardPost]) []string {
	t.Helper()
	ids := make([]string, len(rows))
	for index, row := range rows {
		id, present := golem.Value(row, h.postID).Get()
		if !present {
			t.Fatalf("row %d has no selected id", index)
		}
		ids[index] = id.String()
	}
	return ids
}

func uuidTexts(values ...byte) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = golem.UUID{15: value}.String()
	}
	return result
}

func TestDistinctResultsUnchangedByHiddenIdentity(t *testing.T) {
	runIdentityGuard(t, func(t *testing.T, h *identityGuardHarness) {
		ctx := context.Background()
		for _, reader := range h.readers(t) {
			reader := reader
			t.Run(reader.name, func(t *testing.T) {
				byAuthor, err := reader.posts(ctx, golem.Distinct[identityGuardPost](h.authorID), golem.OrderBy(h.postID.Asc()),
					golem.Select[identityGuardPost](h.postID, h.authorID, h.postTitle))
				if err != nil {
					t.Fatal(err)
				}
				wantAuthors := [][3]string{
					{golem.UUID{15: 11}.String(), golem.UUID{15: 1}.String(), "p-a"},
					{golem.UUID{15: 13}.String(), golem.UUID{15: 2}.String(), "p-c"},
					{golem.UUID{15: 15}.String(), golem.UUID{15: 3}.String(), "p-a"},
				}
				if got := h.postCells(t, byAuthor); !reflect.DeepEqual(got, wantAuthors) {
					t.Fatalf("distinct author rows=%v want %v", got, wantAuthors)
				}

				byTitle, err := reader.posts(ctx, golem.Distinct[identityGuardPost](h.postTitle), golem.OrderBy(h.postID.Asc()),
					golem.Select[identityGuardPost](h.postID, h.postTitle))
				if err != nil {
					t.Fatal(err)
				}
				if got, want := h.postIDs(t, byTitle), uuidTexts(11, 12, 13, 14); !reflect.DeepEqual(got, want) {
					t.Fatalf("distinct title representative ids=%v want %v", got, want)
				}
				if got, want := h.postTitles(t, byTitle), []string{"p-a", "p-b", "p-c", "p-d"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("distinct title titles=%v want %v", got, want)
				}

				withoutKey, err := reader.posts(ctx, golem.Distinct[identityGuardPost](h.postTitle), golem.OrderBy(h.postTitle.Asc()),
					golem.Select[identityGuardPost](h.postTitle))
				if err != nil {
					t.Fatal(err)
				}
				if got, want := h.postTitles(t, withoutKey), []string{"p-a", "p-b", "p-c", "p-d"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("distinct title without the key selected=%v want %v", got, want)
				}

				paged, err := reader.posts(ctx, golem.Distinct[identityGuardPost](h.postTitle), golem.OrderBy(h.postTitle.Asc()),
					golem.Skip[identityGuardPost](1), golem.Take[identityGuardPost](2), golem.Select[identityGuardPost](h.postTitle))
				if err != nil {
					t.Fatal(err)
				}
				if got, want := h.postTitles(t, paged), []string{"p-b", "p-c"}; !reflect.DeepEqual(got, want) {
					t.Fatalf("distinct page=%v want %v", got, want)
				}
			})
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
			t.Run("relation_"+strategy.name, func(t *testing.T) {
				for _, reader := range h.readers(t) {
					users, err := reader.users(strategy.ctx, golem.OrderBy(h.userID.Asc()), golem.Select[identityGuardUser](h.userName,
						h.userPosts.Args(golem.OrderBy(h.postID.Asc()), golem.Distinct[identityGuardPost](h.postTitle), golem.Select[identityGuardPost](h.postID, h.postTitle))))
					if err != nil {
						t.Fatalf("%s: %v", reader.name, err)
					}
					want := map[string][]string{
						"alice": {"p-a", "p-b", "p-d"}, "bob": {"p-c"}, "carol": {"p-a"}, "dave": {}, "erin": {},
					}
					wantIDs := map[string][]string{
						"alice": uuidTexts(11, 12, 14), "bob": uuidTexts(13), "carol": uuidTexts(15), "dave": {}, "erin": {},
					}
					if len(users) != 5 {
						t.Fatalf("%s: users=%d", reader.name, len(users))
					}
					for _, user := range users {
						name, _ := golem.Value(user, h.userName).Get()
						children, present := golem.Many(user, h.userPosts).Get()
						if !present {
							t.Fatalf("%s: %s relation is absent", reader.name, name)
						}
						titles, ids := h.postTitles(t, children), h.postIDs(t, children)
						if !reflect.DeepEqual(titles, want[name]) || !reflect.DeepEqual(ids, wantIDs[name]) {
							t.Fatalf("%s: %s distinct children titles=%v ids=%v want %v %v", reader.name, name, titles, ids, want[name], wantIDs[name])
						}
					}
				}
			})
		}
	})
}

func TestCursorPagesUnchangedByHiddenIdentity(t *testing.T) {
	runIdentityGuard(t, func(t *testing.T, h *identityGuardHarness) {
		ctx := context.Background()
		cursor := golem.Cursor(golem.GeneratedUniqueSelectorValue[identityGuardUser](h.schema.User, h.schema.UserKey, golem.GeneratedSelectorComponent(h.schema.UserID, golem.UUID{15: 3})))
		pages := []struct {
			name      string
			options   []golem.ReadOption[identityGuardUser]
			wantNames []string
			wantIDs   []string
		}{
			{"forward-exclusive", []golem.ReadOption[identityGuardUser]{golem.Skip[identityGuardUser](1), golem.Take[identityGuardUser](2)}, []string{"dave", "erin"}, uuidTexts(4, 5)},
			{"forward-inclusive", []golem.ReadOption[identityGuardUser]{golem.Take[identityGuardUser](2)}, []string{"carol", "dave"}, uuidTexts(3, 4)},
			{"backward-exclusive", []golem.ReadOption[identityGuardUser]{golem.Skip[identityGuardUser](1), golem.Take[identityGuardUser](-2)}, []string{"alice", "bob"}, uuidTexts(1, 2)},
			{"backward-inclusive", []golem.ReadOption[identityGuardUser]{golem.Take[identityGuardUser](-2)}, []string{"bob", "carol"}, uuidTexts(2, 3)},
		}
		for _, reader := range h.readers(t) {
			reader := reader
			t.Run(reader.name, func(t *testing.T) {
				for _, page := range pages {
					base := append([]golem.ReadOption[identityGuardUser]{golem.OrderBy(h.userName.Asc()), cursor}, page.options...)
					withKey := append(append([]golem.ReadOption[identityGuardUser](nil), base...), golem.Select[identityGuardUser](h.userID, h.userName))
					withoutKey := append(append([]golem.ReadOption[identityGuardUser](nil), base...), golem.Select[identityGuardUser](h.userName))
					keyed, err := reader.users(ctx, withKey...)
					if err != nil {
						t.Fatalf("%s with key: %v", page.name, err)
					}
					unkeyed, err := reader.users(ctx, withoutKey...)
					if err != nil {
						t.Fatalf("%s without key: %v", page.name, err)
					}
					names := func(rows []golem.Row[identityGuardUser]) []string {
						result := make([]string, len(rows))
						for index, row := range rows {
							result[index], _ = golem.Value(row, h.userName).Get()
						}
						return result
					}
					ids := make([]string, len(keyed))
					for index, row := range keyed {
						id, _ := golem.Value(row, h.userID).Get()
						ids[index] = id.String()
					}
					if got := names(keyed); !reflect.DeepEqual(got, page.wantNames) {
						t.Fatalf("%s with key names=%v want %v", page.name, got, page.wantNames)
					}
					if got := names(unkeyed); !reflect.DeepEqual(got, page.wantNames) {
						t.Fatalf("%s without key names=%v want %v", page.name, got, page.wantNames)
					}
					if !reflect.DeepEqual(ids, page.wantIDs) {
						t.Fatalf("%s ids=%v want %v", page.name, ids, page.wantIDs)
					}
					frozenWith, err := golem.FreezeFindMany(h.users, withKey...)
					if err != nil {
						t.Fatal(err)
					}
					frozenWithout, err := golem.FreezeFindMany(h.users, withoutKey...)
					if err != nil {
						t.Fatal(err)
					}
					tokenWith, withPresent := frozenWith.Cursor()
					tokenWithout, withoutPresent := frozenWithout.Cursor()
					if !withPresent || !withoutPresent || !reflect.DeepEqual(tokenWith, tokenWithout) {
						t.Fatalf("%s cursor tokens differ: with=%#v without=%#v", page.name, tokenWith, tokenWithout)
					}
				}
			})
		}
	})
}

func TestSystemAndCallerReadsReturnSameRowsAndIdentity(t *testing.T) {
	runIdentityGuard(t, func(t *testing.T, h *identityGuardHarness) {
		ctx := context.Background()
		readers := h.readers(t)
		results := make(map[string][][3]string, len(readers))
		for _, reader := range readers {
			rows, err := reader.posts(ctx, golem.OrderBy(h.postID.Asc()), golem.Select[identityGuardPost](h.postID, h.authorID, h.postTitle))
			if err != nil {
				t.Fatalf("%s: %v", reader.name, err)
			}
			results[reader.name] = h.postCells(t, rows)
		}
		if len(results["system"]) != 6 || !reflect.DeepEqual(results["system"], results["caller"]) {
			t.Fatalf("system rows=%v caller rows=%v", results["system"], results["caller"])
		}

		caller, err := h.app.ForPrincipal(ctx, identityGuardPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		selector := golem.GeneratedUniqueSelectorValue[identityGuardUser](h.schema.User, h.schema.UserKey, golem.GeneratedSelectorComponent(h.schema.UserID, golem.UUID{15: 2}))
		projection := golem.Select[identityGuardUser](h.userName)
		systemRow, err := SystemFindUnique(ctx, h.app.System(), h.users, selector, projection)
		if err != nil {
			t.Fatal(err)
		}
		callerRow, err := CallerFindUnique(ctx, caller, h.users, selector, projection)
		if err != nil {
			t.Fatal(err)
		}
		systemName, systemPresent := golem.Value(systemRow, h.userName).Get()
		callerName, callerPresent := golem.Value(callerRow, h.userName).Get()
		if !systemPresent || !callerPresent || systemName != "bob" || callerName != "bob" {
			t.Fatalf("unique name system=%q/%t caller=%q/%t", systemName, systemPresent, callerName, callerPresent)
		}
		if golem.Value(systemRow, h.userID).IsSelected() || golem.Value(callerRow, h.userID).IsSelected() {
			t.Fatal("an unselected key became a visible cell")
		}
	})
}

func TestConditionallyMaskedKeyStaysMasked(t *testing.T) {
	runIdentityGuardWithKeyMask(t, true, func(t *testing.T, h *identityGuardHarness) {
		ctx := context.Background()
		caller, err := h.app.ForPrincipal(ctx, identityGuardPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		rows, err := CallerFindMany(ctx, caller, h.posts, golem.Select[identityGuardPost](h.postTitle))
		var failure *golem.Error
		if len(rows) != 0 || !errors.As(err, &failure) || failure.Code != golem.CodeForbidden || failure.Reason != golem.ReasonFieldNotReadable || failure.Message != "field is not readable in this position" {
			t.Fatalf("caller read whose key mask is not discharged rows=%d failure=%#v err=%v", len(rows), failure, err)
		}

		discharged, err := CallerFindMany(ctx, caller, h.posts, golem.Where(h.postTitle.StartsWith("p-a")), golem.OrderBy(h.postID.Asc()),
			golem.Select[identityGuardPost](h.postID, h.postTitle))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := h.postIDs(t, discharged), uuidTexts(11, 15, 16); !reflect.DeepEqual(got, want) {
			t.Fatalf("discharged caller ids=%v want %v", got, want)
		}

		system, err := SystemFindMany(ctx, h.app.System(), h.posts, golem.OrderBy(h.postID.Asc()), golem.Select[identityGuardPost](h.postID, h.postTitle))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := h.postIDs(t, system), uuidTexts(11, 12, 13, 14, 15, 16); !reflect.DeepEqual(got, want) {
			t.Fatalf("system ids=%v want %v", got, want)
		}
	})
}

type reversedCompositeFixture struct {
	compositeMutationFixture
	itemsOf golem.ToMany[compositeMutationTenant, compositeMutationItem]
	prefix  string
}

func openReversedComposite(t *testing.T, profile postgresAcceptanceProfile, provider golem.Provider) reversedCompositeFixture {
	t.Helper()
	ctx := context.Background()
	if provider == golem.SQLite {
		schema := schematest.NewCompositeRelationReversedItemKey(t, "public")
		sqlite := sqliteprovider.New()
		database, _, err := sqlite.Open(ctx, "file:"+filepath.Join(t.TempDir(), "reversed-composite.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		if err := sqlite.ApplyInitial(ctx, database, schema.SQLite); err != nil {
			t.Fatal(err)
		}
		fixture := openCompositeMutationFixture(t, database, golem.SQLite, schema)
		return reversedCompositeFixture{compositeMutationFixture: fixture, itemsOf: golem.GeneratedToMany[compositeMutationTenant, compositeMutationItem](schema.TenantItems, schema.Ownership, schema.Item)}
	}
	if profile.dsn == "" {
		testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
	}
	namespace := physical.PhysicalName(fmt.Sprintf("golem_w1a_composite_%s_%d_%d", profile.name, os.Getpid(), time.Now().UnixNano()))
	schema := schematest.NewCompositeRelationReversedItemKey(t, namespace)
	postgres := postgresprovider.New()
	database, _, err := postgres.Open(ctx, profile.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
		_ = database.Close()
	})
	if err := postgres.ApplyInitial(ctx, database, schema.PostgreSQL); err != nil {
		t.Fatal(err)
	}
	fixture := openCompositeMutationFixture(t, database, golem.PostgreSQL, schema)
	return reversedCompositeFixture{compositeMutationFixture: fixture, itemsOf: golem.GeneratedToMany[compositeMutationTenant, compositeMutationItem](schema.TenantItems, schema.Ownership, schema.Item)}
}

func TestCompositeKeyRowsUnchangedByHiddenIdentity(t *testing.T) {
	check := func(t *testing.T, fixture reversedCompositeFixture) {
		ctx := context.Background()
		system := fixture.app.System()
		for _, tenant := range [][2]byte{{21, 22}, {23, 24}} {
			input := golem.GeneratedCreateInput[compositeMutationTenant](fixture.schema.Tenant,
				golem.GeneratedCreateFieldValue(fixture.schema.Tenant, fixture.tenantRegion, golem.UUID{15: tenant[0]}),
				golem.GeneratedCreateFieldValue(fixture.schema.Tenant, fixture.tenantID, golem.UUID{15: tenant[1]}),
			)
			if _, err := SystemCreate(ctx, system, fixture.tenantDescriptor, input); err != nil {
				t.Fatal(err)
			}
		}
		for _, item := range [][4]byte{{11, 12, 21, 22}, {13, 14, 21, 22}, {15, 16, 23, 24}} {
			input := golem.GeneratedCreateInput[compositeMutationItem](fixture.schema.Item,
				golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.itemRegion, golem.UUID{15: item[0]}),
				golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.itemID, golem.UUID{15: item[1]}),
				golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.ownerRegion, golem.UUID{15: item[2]}),
				golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.ownerID, golem.UUID{15: item[3]}),
			)
			if _, err := SystemCreate(ctx, system, fixture.itemDescriptor, input); err != nil {
				t.Fatal(err)
			}
		}
		cells := func(rows []golem.Row[compositeMutationItem], fields ...golem.EqualField[compositeMutationItem, golem.UUID]) [][]string {
			result := make([][]string, len(rows))
			for index, row := range rows {
				for _, field := range fields {
					value, present := golem.Value(row, field).Get()
					if !present {
						t.Fatalf("row %d is missing a selected cell", index)
					}
					result[index] = append(result[index], value.String())
				}
			}
			return result
		}
		withoutKey, err := SystemFindMany(ctx, system, fixture.itemDescriptor, golem.OrderBy(fixture.itemID.Asc()),
			golem.Select[compositeMutationItem](fixture.ownerRegion, fixture.ownerID))
		if err != nil {
			t.Fatal(err)
		}
		wantOwners := [][]string{{golem.UUID{15: 21}.String(), golem.UUID{15: 22}.String()}, {golem.UUID{15: 21}.String(), golem.UUID{15: 22}.String()}, {golem.UUID{15: 23}.String(), golem.UUID{15: 24}.String()}}
		if got := cells(withoutKey, fixture.ownerRegion, fixture.ownerID); !reflect.DeepEqual(got, wantOwners) {
			t.Fatalf("rows without the key=%v want %v", got, wantOwners)
		}
		withKey, err := SystemFindMany(ctx, system, fixture.itemDescriptor, golem.OrderBy(fixture.itemID.Asc()),
			golem.Select[compositeMutationItem](fixture.itemRegion, fixture.itemID, fixture.ownerRegion))
		if err != nil {
			t.Fatal(err)
		}
		wantKeyed := [][]string{
			{golem.UUID{15: 11}.String(), golem.UUID{15: 12}.String(), golem.UUID{15: 21}.String()},
			{golem.UUID{15: 13}.String(), golem.UUID{15: 14}.String(), golem.UUID{15: 21}.String()},
			{golem.UUID{15: 15}.String(), golem.UUID{15: 16}.String(), golem.UUID{15: 23}.String()},
		}
		if got := cells(withKey, fixture.itemRegion, fixture.itemID, fixture.ownerRegion); !reflect.DeepEqual(got, wantKeyed) {
			t.Fatalf("rows with the key=%v want %v", got, wantKeyed)
		}
		strategies := []struct {
			name string
			ctx  context.Context
		}{
			{"production_correlated", ctx},
			{"forced_batched", context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadBatched)},
		}
		for _, strategy := range strategies {
			tenants, err := SystemFindMany(strategy.ctx, system, fixture.tenantDescriptor, golem.OrderBy(fixture.tenantID.Asc()),
				golem.Select[compositeMutationTenant](fixture.itemsOf.Args(golem.OrderBy(fixture.itemID.Asc()), golem.Select[compositeMutationItem](fixture.ownerID))))
			if err != nil {
				t.Fatalf("%s: %v", strategy.name, err)
			}
			counts := make([]int, len(tenants))
			for index, tenant := range tenants {
				children, present := golem.Many(tenant, fixture.itemsOf).Get()
				if !present {
					t.Fatalf("%s: tenant %d has no items relation", strategy.name, index)
				}
				counts[index] = len(children)
			}
			if !reflect.DeepEqual(counts, []int{2, 1}) {
				t.Fatalf("%s: composite relation children per tenant=%v", strategy.name, counts)
			}
		}
	}
	t.Run("sqlite", func(t *testing.T) {
		check(t, openReversedComposite(t, postgresAcceptanceProfile{}, golem.SQLite))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			check(t, openReversedComposite(t, profile, golem.PostgreSQL))
		})
	}
}

func TestBytesKeyRowsUnchangedByHiddenIdentity(t *testing.T) {
	runExactValueProfilesFor(t, schematest.NewBytesKeyed, func(t *testing.T, profile exactValueProfile) {
		ctx := context.Background()
		seedBytesKeyedRows(t, profile.database, profile.provider, profile.prefix)
		reader := newBytesKeyedReader(t, profile.database, guardProvider(profile), profile.fixture, nil)
		system := reader.fixture.app.System()

		keyed, err := SystemFindMany(ctx, system, reader.posts, golem.OrderBy(reader.title.Asc()), golem.Select[mutationResultPost](reader.postID, reader.title))
		if err != nil || len(keyed) != 2 {
			t.Fatalf("keyed rows=%d err=%v", len(keyed), err)
		}
		for index, want := range []string{"", "\x01"} {
			id, present := golem.Value(keyed[index], reader.postID).Get()
			title, titlePresent := golem.Value(keyed[index], reader.title).Get()
			if !present || id == nil || string(id) != want || !titlePresent || title != "alice" {
				t.Fatalf("keyed row %d id=%q present=%t title=%q", index, id, present, title)
			}
		}
		unkeyed, err := SystemFindMany(ctx, system, reader.posts, golem.OrderBy(reader.title.Asc()), golem.Select[mutationResultPost](reader.title))
		if err != nil || len(unkeyed) != 2 {
			t.Fatalf("unkeyed rows=%d err=%v", len(unkeyed), err)
		}
		for index, row := range unkeyed {
			title, present := golem.Value(row, reader.title).Get()
			if !present || title != "alice" || golem.Value(row, reader.postID).IsSelected() {
				t.Fatalf("unkeyed row %d title=%q present=%t", index, title, present)
			}
		}
		batched := context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadBatched)
		for _, strategy := range []context.Context{ctx, batched} {
			users, err := SystemFindMany(strategy, system, reader.users, golem.Select[mutationResultUser](reader.written.Args(golem.OrderBy(reader.title.Asc()), golem.Select[mutationResultPost](reader.title))))
			if err != nil || len(users) != 1 {
				t.Fatalf("relation users=%d err=%v", len(users), err)
			}
			children, present := golem.Many(users[0], reader.written).Get()
			if !present || len(children) != 2 {
				t.Fatalf("relation children=%d present=%t", len(children), present)
			}
			for _, child := range children {
				if title, titlePresent := golem.Value(child, reader.title).Get(); !titlePresent || title != "alice" {
					t.Fatalf("relation child title=%q present=%t", title, titlePresent)
				}
			}
		}
	})
}
