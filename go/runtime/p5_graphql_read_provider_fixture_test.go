package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	publicgraphql "github.com/eleven-am/golem/go/graphql"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

type p5ReadProviderProfile struct {
	name     string
	provider golem.Provider
	dsn      string
	env      string
}

func p5ReadProviderProfiles() []p5ReadProviderProfile {
	return []p5ReadProviderProfile{
		{name: "sqlite", provider: golem.SQLite},
		{name: "postgresql-c", provider: golem.PostgreSQL, dsn: strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN")), env: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "postgresql-linguistic", provider: golem.PostgreSQL, dsn: strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN")), env: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	}
}

type p5ReadProviderHarness struct {
	app              *App[oraclePrincipal, oracleActor]
	server           *publicgraphql.Server[oraclePrincipal]
	disclosureServer *publicgraphql.Server[oraclePrincipal]
	fixture          schematest.Fixture
	trace            *p5SQLTrace
	begins           atomic.Int64
	captured         []golem.FrozenReadRequest
}

type p5ReadProviderExecution struct {
	caller  *Caller[oraclePrincipal, oracleActor]
	harness *p5ReadProviderHarness
}

func (execution p5ReadProviderExecution) ExecuteFrozenRead(ctx context.Context, request golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error) {
	execution.harness.captured = append(execution.harness.captured, request)
	return execution.caller.ExecuteFrozenRead(ctx, request)
}

func newP5ReadProviderHarness(t *testing.T, profile p5ReadProviderProfile) *p5ReadProviderHarness {
	t.Helper()
	ctx := context.Background()
	fixture := schematest.NewIndexed(t)
	trace := &p5SQLTrace{}
	var database *sqlx.DB
	var bundle golem.SchemaBundle
	if profile.provider == golem.SQLite {
		plainDSN := "file:" + filepath.Join(t.TempDir(), "p5-read-provider.db")
		bootstrap, _, err := sqliteprovider.New().Open(ctx, plainDSN)
		if err != nil {
			t.Fatal(err)
		}
		registeredDriver := bootstrap.Driver()
		if err := bootstrap.Close(); err != nil {
			t.Fatal(err)
		}
		dsn := plainDSN + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
		base := p5DriverConnector{driver: registeredDriver, dsn: dsn}
		database = sqlx.NewDb(sql.OpenDB(p5TraceConnector{base: base, trace: trace}), "sqlite")
		database.SetMaxOpenConns(4)
		database.SetMaxIdleConns(4)
		if err := sqliteprovider.New().ApplyInitial(ctx, database, fixture.SQLite); err != nil {
			t.Fatal(err)
		}
		bundle = fixture.Bundle
	} else {
		configuration, err := pgx.ParseConfig(testenv.DisposablePostgreSQL(t, profile.env))
		if err != nil {
			t.Fatal(err)
		}
		if configuration.RuntimeParams == nil {
			configuration.RuntimeParams = map[string]string{}
		}
		configuration.RuntimeParams["timezone"] = "UTC"
		configuration.RuntimeParams["datestyle"] = "ISO, YMD"
		configuration.RuntimeParams["intervalstyle"] = "iso_8601"
		configuration.RuntimeParams["standard_conforming_strings"] = "on"
		base := stdlib.GetConnector(*configuration)
		database = sqlx.NewDb(sql.OpenDB(p5TraceConnector{base: base, trace: trace}), "pgx")
		suffix := fmt.Sprintf("%d_%d", os.Getpid(), time.Now().UnixNano())
		applicationNamespace := physical.PhysicalName("golem_p5_read_" + suffix)
		systemNamespace := physical.PhysicalName("golem_p5_read_system_" + suffix)
		physicalSchema := fixture.PostgreSQL
		physicalSchema.Namespace.Name = applicationNamespace
		physicalSchema.System.Namespace.Name = systemNamespace
		t.Cleanup(func() {
			_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(applicationNamespace)+`" CASCADE`)
			_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(systemNamespace)+`" CASCADE`)
		})
		if err := postgresprovider.New().ApplyInitial(ctx, database, physicalSchema); err != nil {
			t.Fatal(err)
		}
		bundle = postgresRuntimeBundle(t, fixture, physicalSchema)
	}
	t.Cleanup(func() { _ = database.Close() })
	bundle, compilation := p5GraphQLBundle(t, bundle)

	userIdentity := golem.GeneratedIdentityMetadata(fixture.User, fixture.UserKey, golem.PrimaryIdentity, fixture.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(fixture.Post, fixture.PostKey, golem.PrimaryIdentity, fixture.PostID)
	userRelation := golem.GeneratedRelationMetadata(fixture.User, fixture.Post, fixture.UserPosts, fixture.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(fixture.Post, fixture.User, fixture.PostAuthor, fixture.Authorship, golem.RelationSource, golem.RelationToOne)
	users := golem.GeneratedModelDescriptor[oracleUser](fixture.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.UserID, fixture.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation},
	))
	posts := golem.GeneratedModelDescriptor[oraclePost](fixture.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{fixture.PostID, fixture.AuthorID, fixture.PostTitle}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation},
	))
	descriptors, err := golem.GeneratedApplicationDescriptors(bundle.GenerationDigest(), golem.GeneratedStampedPackageDescriptors(bundle.GenerationDigest(), users.Metadata(), posts.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	userID := golem.GeneratedEqualField[oracleUser, golem.UUID](fixture.UserID)
	userName := golem.GeneratedTextField[oracleUser, string](fixture.UserName)
	userPosts := golem.GeneratedToMany[oracleUser, oraclePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	postID := golem.GeneratedEqualField[oraclePost, golem.UUID](fixture.PostID)
	postTitle := golem.GeneratedTextField[oraclePost, string](fixture.PostTitle)
	postAuthor := golem.GeneratedToOne[oraclePost, oracleUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	userBinding := golem.GeneratedPolicyBinding[oracleActor, oracleUser](fixture.User, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oracleUser]()
		if actor.UserPrefix == "" {
			rules.CanRead(golem.All[oracleUser]())
		} else {
			rules.CanRead(userName.StartsWith(actor.UserPrefix))
		}
		rules.CanReadFields(golem.All[oracleUser](), userID)
		rules.CannotReadFields(golem.All[oracleUser](), userName, userPosts)
		rules.CanReadFields(userName.EndsWith("-open"), userName, userPosts)
		return rules.Freeze(fixture.User)
	})
	postBinding := golem.GeneratedPolicyBinding[oracleActor, oraclePost](fixture.Post, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oraclePost]()
		if actor.PostPrefix == "" {
			rules.CanRead(golem.All[oraclePost]())
			rules.CanReadFields(golem.All[oraclePost](), postID, postTitle)
		} else {
			reach := postTitle.StartsWith(actor.PostPrefix)
			rules.CanRead(reach)
			rules.CanReadFields(reach, postID, postTitle)
		}
		rules.CannotReadFields(golem.All[oraclePost](), postAuthor)
		rules.CanReadFields(postTitle.EndsWith("-open"), postAuthor)
		return rules.Freeze(fixture.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[oracleActor]{userBinding, postBinding}, nil))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, Config[oraclePrincipal, oracleActor]{
		Database: p8RuntimeTestDatabase(database, profile.provider), Bundle: bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(_ context.Context, principal oraclePrincipal) (oracleActor, error) { return principal, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	harness := &p5ReadProviderHarness{app: app, fixture: fixture, trace: trace}
	executor, err := publicgraphql.NewGeneratedExecutor(publicgraphql.GeneratedExecutorConfig[oraclePrincipal]{
		Bundle: bundle,
		BeginCaller: func(operationContext context.Context, principal oraclePrincipal) (publicgraphql.CallerExecution, error) {
			harness.begins.Add(1)
			caller, callerErr := app.ForPrincipal(operationContext, principal)
			if callerErr != nil {
				return nil, callerErr
			}
			return p5ReadProviderExecution{caller: caller, harness: harness}, nil
		},
		ReportInternalError: func(_ context.Context, report error) {
			t.Errorf("unexpected provider GraphQL executor error: %v", report)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := publicgraphql.NewServer(document.SDL, publicgraphql.Config[oraclePrincipal]{
		PrincipalFromContext: func(context.Context) (oraclePrincipal, bool) { return oraclePrincipal{}, true },
		ContractFingerprint:  bundle.Contract().Fingerprint(),
		ReportInternalError: func(_ context.Context, report error) {
			t.Errorf("unexpected provider GraphQL server error: %v", report)
		},
	}, executor)
	if err != nil {
		t.Fatal(err)
	}
	harness.server = server
	disclosureUserBinding := golem.GeneratedPolicyBinding[oracleActor, oracleUser](fixture.User, func(oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oracleUser]()
		rules.CanRead(golem.All[oracleUser]())
		return rules.Freeze(fixture.User)
	})
	disclosurePostBinding := golem.GeneratedPolicyBinding[oracleActor, oraclePost](fixture.Post, func(actor oracleActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[oraclePost]()
		if actor.PostPrefix == "" {
			rules.CanRead(golem.All[oraclePost]())
		} else {
			rules.CanRead(postTitle.StartsWith(actor.PostPrefix))
		}
		return rules.Freeze(fixture.Post)
	})
	disclosureBindings, err := golem.GeneratedApplicationBindings(bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(bundle.GenerationDigest(), []golem.PolicyBinding[oracleActor]{disclosureUserBinding, disclosurePostBinding}, nil))
	if err != nil {
		t.Fatal(err)
	}
	disclosureApp, err := Open(ctx, Config[oraclePrincipal, oracleActor]{
		Database: p8RuntimeTestDatabase(database, profile.provider), Bundle: bundle, Bindings: disclosureBindings, Descriptors: descriptors,
		ResolvePrincipal: func(_ context.Context, principal oraclePrincipal) (oracleActor, error) { return principal, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	disclosureExecutor, err := publicgraphql.NewGeneratedExecutor(publicgraphql.GeneratedExecutorConfig[oraclePrincipal]{
		Bundle: bundle,
		BeginCaller: func(operationContext context.Context, principal oraclePrincipal) (publicgraphql.CallerExecution, error) {
			return disclosureApp.ForPrincipal(operationContext, principal)
		},
		ReportInternalError: func(_ context.Context, report error) {
			t.Errorf("unexpected disclosure GraphQL executor error: %v", report)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	harness.disclosureServer, err = publicgraphql.NewServer(document.SDL, publicgraphql.Config[oraclePrincipal]{
		PrincipalFromContext: func(context.Context) (oraclePrincipal, bool) { return oraclePrincipal{}, true },
		ContractFingerprint:  bundle.Contract().Fingerprint(),
		ReportInternalError: func(_ context.Context, report error) {
			t.Errorf("unexpected disclosure GraphQL server error: %v", report)
		},
	}, disclosureExecutor)
	if err != nil {
		t.Fatal(err)
	}
	return harness
}

func (h *p5ReadProviderHarness) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	users := nestedAcceptanceTable(h.app, h.fixture.User)
	posts := nestedAcceptanceTable(h.app, h.fixture.Post)
	for _, row := range [][2]string{{mutationResultUUIDText(1), "alpha-open"}, {mutationResultUUIDText(2), "beta-closed"}, {mutationResultUUIDText(3), "gamma-open"}} {
		query := h.app.database.Rebind(`INSERT INTO ` + users + `("id","name") VALUES (?,?)`)
		if _, err := h.app.database.ExecContext(ctx, query, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range [][3]string{
		{mutationResultUUIDText(11), mutationResultUUIDText(1), "one-open"},
		{mutationResultUUIDText(12), mutationResultUUIDText(1), "two-closed"},
		{mutationResultUUIDText(13), mutationResultUUIDText(2), "three-open"},
		{mutationResultUUIDText(14), mutationResultUUIDText(3), "four-closed"},
	} {
		query := h.app.database.Rebind(`INSERT INTO ` + posts + `("id","author_id","title") VALUES (?,?,?)`)
		if _, err := h.app.database.ExecContext(ctx, query, row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
	h.trace.reset()
	h.captured = nil
	h.begins.Store(0)
}
