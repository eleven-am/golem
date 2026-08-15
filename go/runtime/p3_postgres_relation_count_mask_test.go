package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	"github.com/jmoiron/sqlx"
)

type postgresAcceptanceProfile struct {
	name string
	env  string
	dsn  string
}

func postgresAcceptanceProfiles() []postgresAcceptanceProfile {
	return []postgresAcceptanceProfile{
		{name: "c", env: "GOLEM_TEST_POSTGRES_DSN", dsn: strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN"))},
		{name: "linguistic", env: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN", dsn: strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"))},
	}
}

func TestPostgreSQLImmediateBatchedChildOwnsAuthorizedRelationCount(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			ctx := context.Background()
			fixture := schematest.NewGraph(t)
			database, bundle, namespace := openPostgresAcceptanceSchema(t, ctx, profile, fixture.Bundle, fixture.PostgreSQL, "relation_count")
			seedGraphRows(t, database, qualifiedPostgresTable(namespace, "users"), qualifiedPostgresTable(namespace, "posts"), qualifiedPostgresTable(namespace, "comments"))
			app, handles := openGraphApp(t, ctx, database, golem.PostgreSQL, bundle, fixture, true)
			assertImmediateBatchedChildCounts(t, ctx, app, handles)
		})
	}
}

// TestPostgreSQLConditionalMaskPrivateDependencies proves on both live
// PostgreSQL collation profiles that relation predicates used only to decide a
// field mask remain private and are evaluated through the target row policy.
func TestPostgreSQLConditionalMaskPrivateDependencies(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			ctx := context.Background()
			fixture := schematest.New(t)
			database, bundle, namespace := openPostgresAcceptanceSchema(t, ctx, profile, fixture.Bundle, fixture.PostgreSQL, "mask")
			seedPostgresMaskRows(t, database, namespace)
			app, users, userID, userName, _, _, posts := openMaskApp(t, ctx, database, golem.PostgreSQL, bundle, fixture, postgresMaskRelationPolicy)
			caller, err := app.ForPrincipal(ctx, testPrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			rows, err := CallerFindMany(ctx, caller, users, golem.OrderBy(userID.Asc()), golem.Select[testUser](userName))
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 {
				t.Fatalf("rows=%d", len(rows))
			}
			if name, present := golem.Value(rows[0], userName).Get(); !present || name != "alice" {
				t.Fatalf("alice name=%q present=%t", name, present)
			}
			if name := golem.Value(rows[1], userName); !name.IsSelected() || !name.IsNull() {
				t.Fatalf("bob mask state=%d", name.State())
			}
			for index := range rows {
				if golem.Value(rows[index], userID).IsSelected() {
					t.Fatalf("row %d leaked private order key", index)
				}
				if golem.Many(rows[index], posts).IsSelected() {
					t.Fatalf("row %d leaked policy-only relation", index)
				}
			}
		})
	}
}

// MASK_THE_BATCH_KEY is a mutation-shaped test: masking AuthorID before the
// loader partitions children would detach or misattach rows. The exact key is
// private until attachment and only then receives its public field mask.
func TestPostgreSQLMASK_THE_BATCH_KEY(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			ctx := context.Background()
			fixture := schematest.NewIndexed(t)
			database, bundle, namespace := openPostgresAcceptanceSchema(t, ctx, profile, fixture.Bundle, fixture.PostgreSQL, "batch_key")
			seedPostgresMaskRows(t, database, namespace)
			app, users, userID, userName, authorID, postTitle, posts := openMaskApp(t, ctx, database, golem.PostgreSQL, bundle, fixture, postgresMaskBatchKeyPolicy)
			caller, err := app.ForPrincipal(ctx, testPrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			assertMaskedBatchKeyStrategies(t, ctx, app, caller, users, userID, userName, authorID, postTitle, posts)
		})
	}
}

// MASK_THE_DISTINCT_KEY is a mutation-shaped refusal test. The distinct key
// is not selected, but its conditional mask still makes SQL distinct unsafe;
// the planner must refuse it instead of grouping on data the caller cannot
// uniformly read.
func TestPostgreSQLMASK_THE_DISTINCT_KEY(t *testing.T) {
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			ctx := context.Background()
			fixture := schematest.New(t)
			database, bundle, namespace := openPostgresAcceptanceSchema(t, ctx, profile, fixture.Bundle, fixture.PostgreSQL, "distinct_key")
			seedPostgresMaskRows(t, database, namespace)
			app, users, userID, userName, _, _, _ := openMaskApp(t, ctx, database, golem.PostgreSQL, bundle, fixture, postgresMaskRelationPolicy)
			caller, err := app.ForPrincipal(ctx, testPrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			rows, err := CallerFindMany(ctx, caller, users, golem.Distinct[testUser](userName), golem.Select[testUser](userID))
			var failure *golem.Error
			if len(rows) != 0 || !errors.As(err, &failure) || failure.Code != golem.CodeForbidden || failure.Field != fixture.UserName {
				t.Fatalf("rows=%d error=%v", len(rows), err)
			}
		})
	}
}

func TestMASK_THE_BATCH_KEYSQLite(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.NewIndexed(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "mask-batch-key.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	seedMaskRows(t, database, `"users"`, `"posts"`)
	app, users, userID, userName, authorID, postTitle, posts := openMaskApp(t, ctx, database, golem.SQLite, fixture.Bundle, fixture, postgresMaskBatchKeyPolicy)
	caller, err := app.ForPrincipal(ctx, testPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	assertMaskedBatchKeyStrategies(t, ctx, app, caller, users, userID, userName, authorID, postTitle, posts)
}

func assertMaskedBatchKeyStrategies(t *testing.T, ctx context.Context, app *App[testPrincipal, testActor], caller *Caller[testPrincipal, testActor], users golem.ModelDescriptor[testUser], userID golem.EqualField[testUser, golem.UUID], userName golem.TextField[testUser, string], authorID golem.EqualField[testPost, golem.UUID], postTitle golem.TextField[testPost, string], posts golem.ToMany[testUser, testPost]) {
	t.Helper()
	options := []golem.ReadOption[testUser]{
		golem.OrderBy(userID.Asc()),
		golem.Select[testUser](userName, posts.Args(golem.OrderBy(postTitle.Asc()), golem.Select[testPost](postTitle, authorID))),
	}
	frozen, err := golem.FreezeFindMany(users, options...)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := caller.Prepare(frozen)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := preparePlan(prepared, app.registry, app.readLimits.plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Relations()) != 1 {
		t.Fatalf("planned relations=%d want=1", len(planned.Relations()))
	}
	if strategy := readsql.ChooseRelationStrategy(planned, planned.Relations()[0], app.registry, app.provider); strategy != readsql.RelationCorrelated {
		t.Fatalf("production strategy=%d want correlated=%d", strategy, readsql.RelationCorrelated)
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
			if strategy.name == "forced_batched" {
				forced, ok := forcedRelationLoadStrategy(strategy.ctx)
				if !ok || forced != relationLoadBatched {
					t.Fatalf("forced strategy=%d active=%t", forced, ok)
				}
			}
			rows, err := CallerFindMany(strategy.ctx, caller, users, options...)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 {
				t.Fatalf("users=%d", len(rows))
			}
			wantTitles := [][]string{{"allowed-mask", "allowed-public"}, {"denied-mask"}}
			for userIndex, want := range wantTitles {
				children, present := golem.Many(rows[userIndex], posts).Get()
				if !present || len(children) != len(want) {
					t.Fatalf("user %d children=%d present=%t want=%d", userIndex, len(children), present, len(want))
				}
				for childIndex, titleWant := range want {
					title, titlePresent := golem.Value(children[childIndex], postTitle).Get()
					if !titlePresent || title != titleWant {
						t.Fatalf("user %d child %d title=%q present=%t", userIndex, childIndex, title, titlePresent)
					}
					key := golem.Value(children[childIndex], authorID)
					if userIndex == 0 && childIndex == 0 {
						value, keyPresent := key.Get()
						if !keyPresent || value.String() != "00000000-0000-0000-0000-000000000001" {
							t.Fatalf("allowed public correlation key=%s present=%t", value.String(), keyPresent)
						}
					} else if !key.IsSelected() || !key.IsNull() {
						t.Fatalf("masked public correlation key user=%d child=%d state=%d", userIndex, childIndex, key.State())
					}
				}
			}
		})
	}
}

func TestMASK_THE_DISTINCT_KEYSQLite(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.New(t)
	database, _, err := sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "mask-distinct-key.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	seedMaskRows(t, database, `"users"`, `"posts"`)
	app, users, userID, userName, _, _, _ := openMaskApp(t, ctx, database, golem.SQLite, fixture.Bundle, fixture, postgresMaskRelationPolicy)
	caller, err := app.ForPrincipal(ctx, testPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := CallerFindMany(ctx, caller, users, golem.Distinct[testUser](userName), golem.Select[testUser](userID))
	var failure *golem.Error
	if len(rows) != 0 || !errors.As(err, &failure) || failure.Code != golem.CodeForbidden || failure.Field != fixture.UserName {
		t.Fatalf("rows=%d error=%v", len(rows), err)
	}
}

type postgresMaskPolicy int

const (
	postgresMaskRelationPolicy postgresMaskPolicy = iota
	postgresMaskBatchKeyPolicy
)

func openMaskApp(t *testing.T, ctx context.Context, database *sqlx.DB, provider golem.Provider, bundle golem.SchemaBundle, fixture schematest.Fixture, policy postgresMaskPolicy) (*App[testPrincipal, testActor], golem.ModelDescriptor[testUser], golem.EqualField[testUser, golem.UUID], golem.TextField[testUser, string], golem.EqualField[testPost, golem.UUID], golem.TextField[testPost, string], golem.ToMany[testUser, testPost]) {
	t.Helper()
	users := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), users.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userID := golem.GeneratedEqualField[testUser, golem.UUID](fixture.UserID)
	userName := golem.GeneratedTextField[testUser, string](fixture.UserName)
	authorID := golem.GeneratedEqualField[testPost, golem.UUID](fixture.AuthorID)
	postTitle := golem.GeneratedTextField[testPost, string](fixture.PostTitle)
	posts := golem.GeneratedToMany[testUser, testPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	userBinding := golem.GeneratedPolicyBinding[testActor, testUser](fixture.User, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testUser]()
		rules.CanRead(golem.All[testUser]())
		if policy == postgresMaskRelationPolicy {
			rules.CannotReadFields(golem.All[testUser](), userName)
			rules.CanReadFields(posts.Some(postTitle.Contains("mask")), userName)
		}
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[testActor, testPost](fixture.Post, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		if policy == postgresMaskRelationPolicy {
			rules.CanRead(postTitle.StartsWith("allowed-"))
		} else {
			rules.CanRead(golem.All[testPost]())
			rules.CannotReadFields(golem.All[testPost](), authorID)
			rules.CanReadFields(postTitle.Eq("allowed-mask"), authorID)
		}
		return rules.Freeze(fixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{userBinding, postBinding}, nil)
	bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[testPrincipal, testActor]{Database: p8RuntimeTestDatabase(database, provider), Bundle: bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(context.Context, testPrincipal) (testActor, error) { return testActor{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return app, users, userID, userName, authorID, postTitle, posts
}

func seedPostgresMaskRows(t *testing.T, database *sqlx.DB, namespace physical.PhysicalName) {
	t.Helper()
	seedMaskRows(t, database, qualifiedPostgresTable(namespace, "users"), qualifiedPostgresTable(namespace, "posts"))
}

func seedMaskRows(t *testing.T, database *sqlx.DB, users, posts string) {
	t.Helper()
	ctx := context.Background()
	for _, row := range [][2]string{
		{"00000000-0000-0000-0000-000000000001", "alice"},
		{"00000000-0000-0000-0000-000000000002", "bob"},
	} {
		if _, err := database.ExecContext(ctx, database.Rebind(`INSERT INTO `+users+`("id","name") VALUES (?,?)`), row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range [][3]string{
		{"00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000001", "allowed-mask"},
		{"00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000001", "allowed-public"},
		{"00000000-0000-0000-0000-000000000013", "00000000-0000-0000-0000-000000000002", "denied-mask"},
	} {
		if _, err := database.ExecContext(ctx, database.Rebind(`INSERT INTO `+posts+`("id","author_id","title") VALUES (?,?,?)`), row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
}

func openPostgresAcceptanceSchema(t *testing.T, ctx context.Context, profile postgresAcceptanceProfile, bundle golem.SchemaBundle, postgresSchema physical.PhysicalSchema, purpose string) (*sqlx.DB, golem.SchemaBundle, physical.PhysicalName) {
	t.Helper()
	provider := postgresprovider.New()
	database, _, err := provider.Open(ctx, profile.dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	namespace := physical.PhysicalName(fmt.Sprintf("golem_p3_%s_%s_%d", purpose, profile.name, time.Now().UnixNano()))
	postgresSchema.Namespace.Name = namespace
	if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "_golem" CASCADE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	})
	if err := provider.ApplyInitial(ctx, database, postgresSchema); err != nil {
		t.Fatal(err)
	}
	return database, postgresRuntimeBundleFrom(t, bundle, postgresSchema), namespace
}

func postgresRuntimeBundleFrom(t *testing.T, bundle golem.SchemaBundle, postgresSchema physical.PhysicalSchema) golem.SchemaBundle {
	t.Helper()
	payload, err := physical.CanonicalEncode(postgresSchema)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := physical.PhysicalFingerprint(postgresSchema)
	if err != nil {
		t.Fatal(err)
	}
	system, err := physical.SystemFingerprint(postgresSchema.Provider, postgresSchema.System)
	if err != nil {
		t.Fatal(err)
	}
	postgresDocument := golem.GeneratedProviderSchemaDocument(golem.PostgreSQL, golem.SchemaDigest(system), golem.GeneratedSchemaDocument(postgresSchema.Version, postgresSchema.CanonicalVersion, golem.SchemaDigest(fingerprint), payload))
	providers := bundle.Providers()
	for index := range providers {
		if providers[index].Provider() == golem.PostgreSQL {
			providers[index] = postgresDocument
		}
	}
	return golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), bundle.Contract(), providers...)
}

func qualifiedPostgresTable(namespace physical.PhysicalName, table string) string {
	return `"` + string(namespace) + `"."` + table + `"`
}
