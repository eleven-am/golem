package runtime

import (
	"context"
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

func TestCorrelatedExactBigIntAndDecimalSQLiteLive(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.NewIndexedExact(t)
	database, statements, err := openP3CountingSQLite("file:" + filepath.Join(t.TempDir(), "correlated-exact.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqlite.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	app, userDescriptor, options := exactCorrelatedApp(t, ctx, database, golem.SQLite, fixture.Bundle, fixture)
	seedExactCorrelatedRows(t, ctx, database, "", golem.SQLite)
	assertExactCorrelatedPlan(t, app, userDescriptor, options)
	statements.Store(0)
	rows, err := SystemFindMany(ctx, app.System(), userDescriptor, options...)
	if err != nil {
		t.Fatal(err)
	}
	if got := statements.Load(); got != 1 {
		t.Fatalf("SQLite exact correlated read statements=%d want=1", got)
	}
	assertExactCorrelatedRows(t, rows, fixture)
}

func TestCorrelatedExactBigIntAndDecimalPostgreSQLProfilesLive(t *testing.T) {
	profiles := []struct{ name, dsn string }{
		{"c", strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN"))},
		{"linguistic", strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"))},
	}
	if profiles[0].dsn == "" || profiles[1].dsn == "" {
		t.Skip("both PostgreSQL profile DSNs are required")
	}
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := schematest.NewIndexedExact(t)
			provider := postgresprovider.New()
			database, _, err := provider.Open(ctx, profile.dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			namespace := physical.PhysicalName(fmt.Sprintf("golem_p3_exact_%s_%d", profile.name, time.Now().UnixNano()))
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
			app, userDescriptor, options := exactCorrelatedApp(t, ctx, database, golem.PostgreSQL, bundle, fixture)
			seedExactCorrelatedRows(t, ctx, database, `"`+string(namespace)+`".`, golem.PostgreSQL)
			assertExactCorrelatedPlan(t, app, userDescriptor, options)
			rows, err := SystemFindMany(ctx, app.System(), userDescriptor, options...)
			if err != nil {
				t.Fatal(err)
			}
			assertExactCorrelatedRows(t, rows, fixture)
		})
	}
}

func exactCorrelatedApp(t *testing.T, ctx context.Context, database *sqlx.DB, provider golem.Provider, bundle golem.SchemaBundle, fixture schematest.Fixture) (*App[testPrincipal, testActor], golem.ModelDescriptor[testUser], []golem.ReadOption[testUser]) {
	t.Helper()
	userDescriptor := golem.GeneratedModelDescriptor[testUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](fixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle, fixture.PostBigInt, fixture.PostDecimal}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), descriptorPackage)
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
	bindingPackage := golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{allowUser, allowPost}, nil)
	bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[testPrincipal, testActor]{DB: database, Provider: provider, Bundle: bundle, Bindings: bindings, Descriptors: descriptors, ResolvePrincipal: func(context.Context, testPrincipal) (testActor, error) { return testActor{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	userName := golem.GeneratedTextField[testUser, string](fixture.UserName)
	postID := golem.GeneratedEqualField[testPost, golem.UUID](fixture.PostID)
	bigInt := golem.GeneratedEqualField[testPost, int64](fixture.PostBigInt)
	decimal := golem.GeneratedEqualField[testPost, golem.Decimal](fixture.PostDecimal)
	posts := golem.GeneratedToMany[testUser, testPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	options := []golem.ReadOption[testUser]{
		golem.OrderBy(userName.Asc()),
		golem.Select[testUser](userName, posts.Args(golem.OrderBy(postID.Asc()), golem.Select[testPost](bigInt, decimal))),
	}
	return app, userDescriptor, options
}

func assertExactCorrelatedPlan(t *testing.T, app *App[testPrincipal, testActor], descriptor golem.ModelDescriptor[testUser], options []golem.ReadOption[testUser]) {
	t.Helper()
	frozen, err := golem.FreezeFindMany(descriptor, options...)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := app.System().Prepare(frozen)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := preparePlan(prepared, app.registry, app.readLimits.plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Relations()) != 1 || readsql.ChooseRelationStrategy(planned, planned.Relations()[0], app.registry, app.provider) != readsql.RelationCorrelated {
		t.Fatal("exact-value relation did not choose the production correlated strategy")
	}
	statement, err := readsql.Render(planned, app.registry, app.provider, app.capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if len(statement.CorrelatedColumns()) != 1 {
		t.Fatalf("correlated columns=%d want=1", len(statement.CorrelatedColumns()))
	}
}

func seedExactCorrelatedRows(t *testing.T, ctx context.Context, database *sqlx.DB, qualifier string, provider golem.Provider) {
	t.Helper()
	placeholder := func(index int) string { return "?" }
	if provider == golem.PostgreSQL {
		placeholder = func(index int) string { return fmt.Sprintf("$%d", index) }
	}
	for _, row := range [][2]string{{"00000000-0000-0000-0000-000000000001", "alice"}, {"00000000-0000-0000-0000-000000000002", "bob"}} {
		statement := `INSERT INTO ` + qualifier + `"users"("id","name") VALUES (` + placeholder(1) + `,` + placeholder(2) + `)`
		if _, err := database.ExecContext(ctx, statement, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	type exactSeed struct {
		id      string
		bigInt  int64
		decimal any
	}
	decimalValues := []any{int64(123456789012345678), int64(-123456789012345678), int64(1)}
	if provider == golem.PostgreSQL {
		// P1's portable decimal ceiling is 18 coefficient digits, so the
		// requested 1234567890.1234567890123 (23 digits) is intentionally
		// replaced by the maximum-shape legal Decimal(18,13) value below.
		decimalValues = []any{"12345.6789012345678", "-12345.6789012345678", "0.0000000000001"}
	}
	seeds := []exactSeed{
		{"00000000-0000-0000-0000-000000000011", 9007199254740993, decimalValues[0]},
		{"00000000-0000-0000-0000-000000000012", -9007199254740993, decimalValues[1]},
		{"00000000-0000-0000-0000-000000000013", 42, decimalValues[2]},
	}
	for _, seed := range seeds {
		statement := `INSERT INTO ` + qualifier + `"posts"("id","author_id","title","big_int","decimal_value") VALUES (` + placeholder(1) + `,` + placeholder(2) + `,` + placeholder(3) + `,` + placeholder(4) + `,` + placeholder(5) + `)`
		if _, err := database.ExecContext(ctx, statement, seed.id, "00000000-0000-0000-0000-000000000001", "exact", seed.bigInt, seed.decimal); err != nil {
			t.Fatal(err)
		}
	}
}

func assertExactCorrelatedRows(t *testing.T, rows []golem.Row[testUser], fixture schematest.Fixture) {
	t.Helper()
	if len(rows) != 2 {
		t.Fatalf("parents=%d want=2", len(rows))
	}
	posts := golem.GeneratedToMany[testUser, testPost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	bigInt := golem.GeneratedEqualField[testPost, int64](fixture.PostBigInt)
	decimal := golem.GeneratedEqualField[testPost, golem.Decimal](fixture.PostDecimal)
	children, present := golem.Many(rows[0], posts).Get()
	if !present || len(children) != 3 {
		t.Fatalf("exact children=%v present=%t", children, present)
	}
	wantBigInts := []int64{9007199254740993, -9007199254740993, 42}
	wantDecimals := make([]golem.Decimal, 3)
	for index, text := range []string{"12345.6789012345678", "-12345.6789012345678", "0.0000000000001"} {
		value, err := golem.ParseDecimal(text)
		if err != nil {
			t.Fatal(err)
		}
		wantDecimals[index] = value
	}
	for index, child := range children {
		gotBigInt, bigIntPresent := golem.Value(child, bigInt).Get()
		gotDecimal, decimalPresent := golem.Value(child, decimal).Get()
		if !bigIntPresent || gotBigInt != wantBigInts[index] {
			t.Fatalf("child=%d bigint=%d present=%t want=%d", index, gotBigInt, bigIntPresent, wantBigInts[index])
		}
		if !decimalPresent || gotDecimal != wantDecimals[index] {
			t.Fatalf("child=%d decimal=%v present=%t want=%v", index, gotDecimal, decimalPresent, wantDecimals[index])
		}
	}
	empty, present := golem.Many(rows[1], posts).Get()
	if !present || empty == nil || len(empty) != 0 {
		t.Fatalf("empty relation=%v present=%t; want nonnil []", empty, present)
	}
}
