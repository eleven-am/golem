package runtime_test

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/runtime/testdata/p5extensions"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

type p5ExtensionPrincipalKey struct{}

type p5ExtensionGraphQLResponse struct {
	Data   map[string]any `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

type p5ExtensionProviderProfile struct {
	name, dsn, env string
	provider       golem.Provider
}

var p5ExtensionPostgreSQLFixtureLock sync.Mutex

func p5ExtensionProviderProfiles() []p5ExtensionProviderProfile {
	return []p5ExtensionProviderProfile{
		{name: "sqlite", provider: golem.SQLite},
		{name: "postgresql-c", provider: golem.PostgreSQL, env: "GOLEM_TEST_POSTGRES_DSN", dsn: os.Getenv("GOLEM_TEST_POSTGRES_DSN")},
		{name: "postgresql-linguistic", provider: golem.PostgreSQL, env: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN", dsn: os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN")},
	}
}

func TestGeneratedGraphQLExtensionsUseOneCallerAndOperationLocalLoadersAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			fixture, database, resolutions := newP5ExtensionProviderFixture(t, profile)
			seedP5ExtensionUsers(t, database, profile.provider)
			p5extensions.ResetProbe()

			response := executeP5ExtensionGraphQL(t, fixture, p5extensions.Principal{ID: "alpha", Valid: true}, `query Mixed($prefix: String!) {
  ordinary: users(orderBy: [{id: asc}], take: 10) { id name greeting(prefix: $prefix) batchGreeting(prefix: $prefix) }
  custom: searchUsers(where: {all: true}) { id name greeting(prefix: $prefix) batchGreeting(prefix: $prefix) }
}`, map[string]any{"prefix": "mixed"})
			if len(response.Errors) != 0 {
				t.Fatalf("mixed generated GraphQL errors=%#v", response.Errors)
			}
			ordinary := p5ExtensionRows(t, response.Data["ordinary"])
			custom := p5ExtensionRows(t, response.Data["custom"])
			if fmt.Sprint(ordinary) != fmt.Sprint(custom) {
				t.Fatalf("ordinary/custom results differ\nordinary=%#v\ncustom=%#v", ordinary, custom)
			}
			if len(ordinary) != 3 {
				t.Fatalf("ordinary rows=%d want=3: %#v", len(ordinary), ordinary)
			}
			for index, row := range ordinary {
				if owner, present := row["owner"]; present {
					t.Fatalf("row %d leaked unselected owner=%#v", index, owner)
				}
				name, _ := row["name"].(string)
				greeting, _ := row["greeting"].(string)
				if index == 1 {
					if row["name"] != nil || greeting != "mixed:masked" {
						t.Fatalf("masked dependency row=%#v", row)
					}
				} else if name == "" || greeting != "mixed:"+name {
					t.Fatalf("visible dependency row=%#v", row)
				}
			}
			probe := p5extensions.SnapshotProbe()
			if fmt.Sprint(probe.BatchSizes) != "[2 1]" || fmt.Sprint(probe.BatchPrefixes) != "[mixed mixed]" {
				t.Fatalf("operation-local batch/caching evidence sizes=%v prefixes=%v", probe.BatchSizes, probe.BatchPrefixes)
			}
			if len(probe.CustomCallers) != 1 || probe.CustomCallers[0] == "" {
				t.Fatalf("custom resolver caller evidence=%v", probe.CustomCallers)
			}
			if resolutions.Load() != 1 {
				t.Fatalf("mixed ordinary/custom operation resolved principal %d times, want 1", resolutions.Load())
			}
		})
	}
}

func TestGeneratedCustomMutationTransactionAndWriteInvalidationAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			server, database, resolutions := newP5ExtensionProviderFixture(t, profile)
			p5extensions.ResetProbe()
			createdID := "00000000-0000-0000-0000-000000000101"
			response := executeP5ExtensionGraphQL(t, server, p5extensions.Principal{ID: "alpha", Valid: true}, `mutation TransactionAndInvalidate($id: UUID!) {
  custom: transactionalUser(id: $id, owner: "alpha", name: "before-open", fail: false) {
    id name batchGreeting(prefix: "write")
  }
  ordinary: updateUser(where: {ID: $id}, data: {name: {set: "after-open"}}) {
    id name batchGreeting(prefix: "write")
  }
}`, map[string]any{"id": createdID})
			if len(response.Errors) != 0 {
				t.Fatalf("custom commit/update generated GraphQL errors=%#v", response.Errors)
			}
			custom := p5ExtensionObject(t, response.Data["custom"])
			ordinary := p5ExtensionObject(t, response.Data["ordinary"])
			if custom["id"] != createdID || custom["name"] != "before-open" || ordinary["id"] != createdID || ordinary["name"] != "after-open" {
				t.Fatalf("typed custom/ordinary mutation response custom=%#v ordinary=%#v", custom, ordinary)
			}
			probe := p5extensions.SnapshotProbe()
			if probe.TransactionInvocations != 1 || probe.TransactionCallbacks != 1 || len(probe.CustomCallers) != 1 {
				t.Fatalf("committed custom transaction evidence=%+v", probe)
			}
			if fmt.Sprint(probe.BatchSizes) != "[1 1]" {
				t.Fatalf("write did not invalidate operation-local computed cache, batch sizes=%v", probe.BatchSizes)
			}

			rollbackID := "00000000-0000-0000-0000-000000000102"
			failed := executeP5ExtensionGraphQL(t, server, p5extensions.Principal{ID: "alpha", Valid: true}, `mutation Rollback($id: UUID!) {
  transactionalUser(id: $id, owner: "alpha", name: "rollback-open", fail: true) { id name }
}`, map[string]any{"id": rollbackID})
			if len(failed.Errors) == 0 {
				t.Fatalf("failing custom transaction returned no GraphQL error: %#v", failed)
			}
			probe = p5extensions.SnapshotProbe()
			if probe.TransactionInvocations != 2 || probe.TransactionCallbacks != 2 || len(probe.CustomCallers) != 2 {
				t.Fatalf("custom transaction closure was skipped or replayed: %+v", probe)
			}
			var count int
			if err := database.GetContext(context.Background(), &count, database.Rebind(`SELECT COUNT(*) FROM `+p5ExtensionUsersTable(profile.provider)+` WHERE "id" = ?`), rollbackID); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("custom transaction rollback persisted %s", rollbackID)
			}
			if err := database.GetContext(context.Background(), &count, database.Rebind(`SELECT COUNT(*) FROM `+p5ExtensionUsersTable(profile.provider)+` WHERE "id" = ? AND "name" = ?`), createdID, "after-open"); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("committed custom transaction/update state count=%d", count)
			}
			if resolutions.Load() != 2 {
				t.Fatalf("two custom mutation operations resolved principal %d times, want 2", resolutions.Load())
			}
		})
	}
}

func TestGeneratedGraphQLPrincipalRefusalsIssueZeroSQLAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			server, trace, resolutions := newP5ExtensionTracedProviderFixture(t, profile)
			query := `query Refused { users(where: {all: true}) { id } }`

			trace.reset()
			missing := executeP5ExtensionGraphQLOptional(t, server, nil, query, nil)
			if len(missing.Errors) == 0 || missing.Errors[0].Extensions["code"] != "UNAUTHENTICATED" {
				t.Fatalf("missing principal response=%#v", missing)
			}
			if statements := trace.snapshot(); len(statements) != 0 {
				t.Fatalf("missing principal issued SQL: %v", statements)
			}
			if resolutions.Load() != 0 {
				t.Fatalf("missing principal unexpectedly resolved actor %d times", resolutions.Load())
			}

			trace.reset()
			invalidPrincipal := p5extensions.Principal{ID: "alpha", Valid: false}
			invalid := executeP5ExtensionGraphQLOptional(t, server, &invalidPrincipal, query, nil)
			if len(invalid.Errors) == 0 || invalid.Errors[0].Extensions["code"] != "UNAUTHENTICATED" {
				t.Fatalf("invalid principal response=%#v", invalid)
			}
			if statements := trace.snapshot(); len(statements) != 0 {
				t.Fatalf("invalid principal issued SQL: %v", statements)
			}
			if resolutions.Load() != 1 {
				t.Fatalf("invalid principal actor resolutions=%d want=1", resolutions.Load())
			}
		})
	}
}

func TestSILENT_PAGE_TRUNCATIONGeneratedGraphQLDefaultPageNeverSilentlyTruncatesSQLite(t *testing.T) {
	profile := p5ExtensionProviderProfile{name: "sqlite", provider: golem.SQLite}
	server, database, trace, resolutions := newP5ExtensionTracedProviderFixtureWithLimits(t, profile, p5extensions.GraphQLLimits{MaxPageSize: 2})
	seedP5ExtensionUsers(t, database, profile.provider)

	if !strings.Contains(server.SDL(), "take: Int = 50") {
		t.Fatalf("generated public SDL does not expose the ContractIR page default:\n%s", server.SDL())
	}
	trace.reset()
	omitted := executeP5ExtensionGraphQL(t, server, p5extensions.Principal{ID: "alpha", Valid: true}, `query { users { id } }`, nil)
	if len(omitted.Errors) != 1 || omitted.Errors[0].Extensions["code"] != "BAD_USER_INPUT" {
		t.Fatalf("omitted SDL default above runtime maximum was not visibly refused: %#v", omitted)
	}
	if statements := trace.snapshot(); len(statements) != 0 {
		t.Fatalf("refused SDL default issued SQL: %v", statements)
	}
	if resolutions.Load() != 0 {
		t.Fatalf("refused SDL default opened caller execution %d times", resolutions.Load())
	}

	explicit := executeP5ExtensionGraphQL(t, server, p5extensions.Principal{ID: "alpha", Valid: true}, `query { users(orderBy: [{id: asc}], take: 2) { id } }`, nil)
	if len(explicit.Errors) != 0 || len(p5ExtensionRows(t, explicit.Data["users"])) != 2 {
		t.Fatalf("explicit page at runtime maximum failed: %#v", explicit)
	}
}

func TestGeneratedGraphQLComputedBatchLimitBoundsActiveResolverSQLite(t *testing.T) {
	profile := p5ExtensionProviderProfile{name: "sqlite", provider: golem.SQLite}
	server, database, _, _ := newP5ExtensionTracedProviderFixtureWithLimits(t, profile, p5extensions.GraphQLLimits{MaxComputedBatchSize: 1})
	seedP5ExtensionUsers(t, database, profile.provider)
	p5extensions.ResetProbe()
	response := executeP5ExtensionGraphQL(t, server, p5extensions.Principal{ID: "alpha", Valid: true}, `query { users(orderBy: [{id: asc}], take: 3) { id batchGreeting(prefix: "bounded") } }`, nil)
	if len(response.Errors) != 0 {
		t.Fatalf("bounded computed query errors=%#v", response.Errors)
	}
	probe := p5extensions.SnapshotProbe()
	if fmt.Sprint(probe.BatchSizes) != "[1 1 1]" {
		t.Fatalf("active computed batches=%v want [1 1 1]", probe.BatchSizes)
	}
}

func TestGeneratedGraphQLOperationStateIsPrincipalLocalUnderConcurrencySQLite(t *testing.T) {
	profile := p5ExtensionProviderProfile{name: "sqlite", provider: golem.SQLite}
	server, database, resolutions := newP5ExtensionProviderFixture(t, profile)
	seedP5ExtensionUsers(t, database, profile.provider)
	p5extensions.ResetProbe()
	const operations = 32
	query := `query Isolation($prefix: String!) {
  ordinary: users(orderBy: [{id: asc}], take: 10) { id name batchGreeting(prefix: $prefix) }
  custom: searchUsers(where: {all: true}) { id name batchGreeting(prefix: $prefix) }
}`
	type outcome struct {
		principal string
		response  p5ExtensionGraphQLResponse
		err       error
	}
	start := make(chan struct{})
	results := make(chan outcome, operations)
	for index := range operations {
		principal := "alpha"
		if index%2 != 0 {
			principal = "beta"
		}
		go func(principal string) {
			<-start
			ctx := context.WithValue(context.Background(), p5ExtensionPrincipalKey{}, p5extensions.Principal{ID: principal, Valid: true})
			response, err := executeP5ExtensionGraphQLRaw(ctx, server, query, map[string]any{"prefix": "same"})
			results <- outcome{principal: principal, response: response, err: err}
		}(principal)
	}
	close(start)
	for range operations {
		result := <-results
		if result.err != nil || len(result.response.Errors) != 0 {
			t.Fatalf("concurrent %s response err=%v graphql=%#v", result.principal, result.err, result.response.Errors)
		}
		ordinary := p5ExtensionRows(t, result.response.Data["ordinary"])
		custom := p5ExtensionRows(t, result.response.Data["custom"])
		if len(ordinary) != 3 || fmt.Sprint(ordinary) != fmt.Sprint(custom) {
			t.Fatalf("concurrent %s ordinary/custom=%#v/%#v", result.principal, ordinary, custom)
		}
		for _, row := range ordinary {
			if name, ok := row["name"].(string); ok && name != "" && !bytes.HasPrefix([]byte(name), []byte(result.principal+"-")) {
				t.Fatalf("principal %s observed cross-principal row %#v", result.principal, row)
			}
		}
	}
	probe := p5extensions.SnapshotProbe()
	if len(probe.CustomCallers) != operations {
		t.Fatalf("concurrent custom caller count=%d want=%d", len(probe.CustomCallers), operations)
	}
	if len(probe.BatchSizes) != operations*2 {
		t.Fatalf("concurrent operation-local batch count=%d want=%d sizes=%v", len(probe.BatchSizes), operations*2, probe.BatchSizes)
	}
	if resolutions.Load() != operations {
		t.Fatalf("concurrent principal resolutions=%d want=%d", resolutions.Load(), operations)
	}
	for _, size := range probe.BatchSizes {
		if size < 1 || size > 2 {
			t.Fatalf("concurrent batch exceeded declared maximum: %v", probe.BatchSizes)
		}
	}
}

func TestGeneratedBatchedComputedCancellationReachesLoaderAcrossProviders(t *testing.T) {
	for _, profile := range p5ExtensionProviderProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured")
			}
			server, database, resolutions := newP5ExtensionProviderFixture(t, profile)
			seedP5ExtensionUsers(t, database, profile.provider)
			p5extensions.ResetProbe()
			started := p5extensions.ArmNextLoad()
			t.Cleanup(p5extensions.ReleaseLoad)
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), p5ExtensionPrincipalKey{}, p5extensions.Principal{ID: "alpha", Valid: true}))
			result := make(chan error, 1)
			go func() {
				response, err := executeP5ExtensionGraphQLRaw(ctx, server, `query Cancel { users(orderBy: [{id: asc}], take: 1) { id batchGreeting(prefix: "cancel") } }`, nil)
				if err == nil && len(response.Errors) == 0 {
					err = fmt.Errorf("cancelled computed request returned no GraphQL error: %#v", response.Data)
				}
				result <- err
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("batched computed loader did not start")
			}
			cancel()
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled batched computed request did not terminate")
			}
			probe := p5extensions.SnapshotProbe()
			if len(probe.BatchSizes) != 1 || probe.BatchSizes[0] > 2 || probe.BatchPrefixes[0] != "cancel" {
				t.Fatalf("cancelled loader evidence=%+v", probe)
			}
			if resolutions.Load() != 1 {
				t.Fatalf("cancelled operation resolved principal %d times, want 1", resolutions.Load())
			}
		})
	}
}

func newP5ExtensionProviderFixture(t *testing.T, profile p5ExtensionProviderProfile) (*p5extensions.GraphQLServer, *sqlx.DB, *atomic.Int64) {
	t.Helper()
	ctx := context.Background()
	var database *sqlx.DB
	var apply func(context.Context, *sqlx.DB, physical.PhysicalSchema) error
	if profile.provider == golem.SQLite {
		provider := sqliteprovider.New()
		var err error
		database, _, err = provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "extensions.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		apply = provider.ApplyInitial
	} else {
		p5ExtensionPostgreSQLFixtureLock.Lock()
		t.Cleanup(p5ExtensionPostgreSQLFixtureLock.Unlock)
		provider := postgresprovider.New()
		var err error
		database, _, err = provider.Open(ctx, profile.dsn)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+string(p5ExtensionPostgreSQLNamespace)+`" CASCADE`); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+string(p5ExtensionPostgreSQLSystemNamespace)+`" CASCADE`); err != nil {
			t.Fatal(err)
		}
		apply = provider.ApplyInitial
		t.Cleanup(func() {
			p5CleanupExtensionPostgreSQLSchemas(t, profile.dsn)
		})
	}
	t.Cleanup(func() { _ = database.Close() })
	var encoded []byte
	for _, document := range p5extensions.GolemGeneratedSchemaBundle().Providers() {
		if document.Provider() == profile.provider {
			encoded = document.Schema().Bytes()
		}
	}
	schema, err := physical.CanonicalDecode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	assertP5ExtensionProviderIsolation(t, profile.provider, schema)
	if err := apply(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	databaseHandle := p8AdoptTracedProviderHandle(database, profile)
	resolutions := &atomic.Int64{}
	application, err := p5extensions.Open(ctx, p5extensions.Config[p5extensions.Principal]{
		Database: databaseHandle,
		ResolvePrincipal: func(_ context.Context, principal p5extensions.Principal) (p5extensions.Actor, error) {
			resolutions.Add(1)
			if !principal.Valid {
				return p5extensions.Actor{}, golem.RuntimeReadError(golem.CodeUnauthenticated, "graphql", p5extensions.GolemGeneratedUserDescriptor.Metadata().ModelID(), golem.FieldID{}, "invalid principal", nil)
			}
			return p5extensions.Actor{ID: principal.ID}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := application.GraphQL(p5extensions.GraphQLConfig[p5extensions.Principal]{
		PrincipalFromContext: func(ctx context.Context) (p5extensions.Principal, bool) {
			principal, ok := ctx.Value(p5ExtensionPrincipalKey{}).(p5extensions.Principal)
			return principal, ok
		},
		ReportInternalError: func(_ context.Context, err error) { t.Logf("reported GraphQL error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, database, resolutions
}

func newP5ExtensionTracedProviderFixture(t *testing.T, profile p5ExtensionProviderProfile) (*p5extensions.GraphQLServer, *p5ExtensionSQLTrace, *atomic.Int64) {
	server, _, trace, resolutions := newP5ExtensionTracedProviderFixtureWithLimits(t, profile, p5extensions.GraphQLLimits{})
	return server, trace, resolutions
}

func newP5ExtensionTracedProviderFixtureWithLimits(t *testing.T, profile p5ExtensionProviderProfile, limits p5extensions.GraphQLLimits) (*p5extensions.GraphQLServer, *sqlx.DB, *p5ExtensionSQLTrace, *atomic.Int64) {
	t.Helper()
	ctx := context.Background()
	trace := &p5ExtensionSQLTrace{}
	var database *sqlx.DB
	var apply func(context.Context, *sqlx.DB, physical.PhysicalSchema) error
	if profile.provider == golem.SQLite {
		plainDSN := "file:" + filepath.Join(t.TempDir(), "extensions-traced.sqlite")
		bootstrap, _, err := sqliteprovider.New().Open(ctx, plainDSN)
		if err != nil {
			t.Fatal(err)
		}
		registeredDriver := bootstrap.Driver()
		if err := bootstrap.Close(); err != nil {
			t.Fatal(err)
		}
		base := p5ExtensionDriverConnector{driver: registeredDriver, dsn: plainDSN + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"}
		database = sqlx.NewDb(sql.OpenDB(p5ExtensionTraceConnector{base: base, trace: trace}), "sqlite")
		apply = sqliteprovider.New().ApplyInitial
	} else {
		p5ExtensionPostgreSQLFixtureLock.Lock()
		t.Cleanup(p5ExtensionPostgreSQLFixtureLock.Unlock)
		configuration, err := pgx.ParseConfig(profile.dsn)
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
		database = sqlx.NewDb(sql.OpenDB(p5ExtensionTraceConnector{base: stdlib.GetConnector(*configuration), trace: trace}), "pgx")
		if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+string(p5ExtensionPostgreSQLNamespace)+`" CASCADE`); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "`+string(p5ExtensionPostgreSQLSystemNamespace)+`" CASCADE`); err != nil {
			t.Fatal(err)
		}
		apply = postgresprovider.New().ApplyInitial
		t.Cleanup(func() {
			p5CleanupExtensionPostgreSQLSchemas(t, profile.dsn)
		})
	}
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = database.Close() })
	var encoded []byte
	for _, document := range p5extensions.GolemGeneratedSchemaBundle().Providers() {
		if document.Provider() == profile.provider {
			encoded = document.Schema().Bytes()
		}
	}
	schema, err := physical.CanonicalDecode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	assertP5ExtensionProviderIsolation(t, profile.provider, schema)
	if err := apply(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	databaseHandle := p8AdoptTracedProviderHandle(database, profile)
	resolutions := &atomic.Int64{}
	application, err := p5extensions.Open(ctx, p5extensions.Config[p5extensions.Principal]{
		Database: databaseHandle,
		ResolvePrincipal: func(_ context.Context, principal p5extensions.Principal) (p5extensions.Actor, error) {
			resolutions.Add(1)
			if !principal.Valid {
				return p5extensions.Actor{}, golem.RuntimeReadError(golem.CodeUnauthenticated, "graphql", p5extensions.GolemGeneratedUserDescriptor.Metadata().ModelID(), golem.FieldID{}, "invalid principal", nil)
			}
			return p5extensions.Actor{ID: principal.ID}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := application.GraphQL(p5extensions.GraphQLConfig[p5extensions.Principal]{
		PrincipalFromContext: func(ctx context.Context) (p5extensions.Principal, bool) {
			principal, ok := ctx.Value(p5ExtensionPrincipalKey{}).(p5extensions.Principal)
			return principal, ok
		},
		Limits:              limits,
		ReportInternalError: func(_ context.Context, err error) { t.Logf("reported GraphQL error: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	trace.reset()
	return server, database, trace, resolutions
}

func assertP5ExtensionProviderIsolation(t *testing.T, provider golem.Provider, schema physical.PhysicalSchema) {
	t.Helper()
	if provider != golem.PostgreSQL {
		return
	}
	if schema.Namespace.Name != p5ExtensionPostgreSQLNamespace || schema.System.Namespace.Name != p5ExtensionPostgreSQLSystemNamespace {
		t.Fatalf("generated PostgreSQL fixture is not isolated: application=%q system=%q", schema.Namespace.Name, schema.System.Namespace.Name)
	}
}

func p5CleanupExtensionPostgreSQLSchemas(t *testing.T, dsn string) {
	t.Helper()
	database, _, err := postgresprovider.New().Open(context.Background(), dsn)
	if err != nil {
		t.Errorf("open PostgreSQL cleanup connection: %v", err)
		return
	}
	defer database.Close()
	if _, err := database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(p5ExtensionPostgreSQLNamespace)+`" CASCADE`); err != nil {
		t.Errorf("drop PostgreSQL fixture application schema: %v", err)
	}
	if _, err := database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(p5ExtensionPostgreSQLSystemNamespace)+`" CASCADE`); err != nil {
		t.Errorf("drop PostgreSQL fixture system schema: %v", err)
	}
}

type p5ExtensionSQLTrace struct {
	lock       sync.Mutex
	statements []string
	afterNext  func()
}

func (trace *p5ExtensionSQLTrace) record(statement string) {
	trace.lock.Lock()
	trace.statements = append(trace.statements, statement)
	after := trace.afterNext
	trace.afterNext = nil
	trace.lock.Unlock()
	if after != nil {
		after()
	}
}

func (trace *p5ExtensionSQLTrace) reset() {
	trace.lock.Lock()
	trace.statements = nil
	trace.afterNext = nil
	trace.lock.Unlock()
}

func (trace *p5ExtensionSQLTrace) snapshot() []string {
	trace.lock.Lock()
	defer trace.lock.Unlock()
	return append([]string(nil), trace.statements...)
}

func (trace *p5ExtensionSQLTrace) afterNextStatement(callback func()) {
	trace.lock.Lock()
	trace.afterNext = callback
	trace.lock.Unlock()
}

type p5ExtensionTraceConnector struct {
	base  driver.Connector
	trace *p5ExtensionSQLTrace
}

func (connector p5ExtensionTraceConnector) Connect(ctx context.Context) (driver.Conn, error) {
	connection, err := connector.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &p5ExtensionTraceConn{Conn: connection, trace: connector.trace}, nil
}
func (connector p5ExtensionTraceConnector) Driver() driver.Driver { return connector.base.Driver() }

type p5ExtensionDriverConnector struct {
	driver driver.Driver
	dsn    string
}

func (connector p5ExtensionDriverConnector) Connect(context.Context) (driver.Conn, error) {
	return connector.driver.Open(connector.dsn)
}
func (connector p5ExtensionDriverConnector) Driver() driver.Driver { return connector.driver }

type p5ExtensionTraceConn struct {
	driver.Conn
	trace *p5ExtensionSQLTrace
}

func (connection *p5ExtensionTraceConn) Prepare(query string) (driver.Stmt, error) {
	statement, err := connection.Conn.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &p5ExtensionTraceStmt{Stmt: statement, query: query, trace: connection.trace}, nil
}
func (connection *p5ExtensionTraceConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if contextual, ok := connection.Conn.(driver.ConnPrepareContext); ok {
		statement, err := contextual.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		return &p5ExtensionTraceStmt{Stmt: statement, query: query, trace: connection.trace}, nil
	}
	return connection.Prepare(query)
}
func (connection *p5ExtensionTraceConn) ExecContext(ctx context.Context, query string, arguments []driver.NamedValue) (driver.Result, error) {
	executor, ok := connection.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	connection.trace.record(query)
	return executor.ExecContext(ctx, query, arguments)
}
func (connection *p5ExtensionTraceConn) QueryContext(ctx context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := connection.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	connection.trace.record(query)
	return queryer.QueryContext(ctx, query, arguments)
}
func (connection *p5ExtensionTraceConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	if beginner, ok := connection.Conn.(driver.ConnBeginTx); ok {
		return beginner.BeginTx(ctx, options)
	}
	return connection.Conn.Begin()
}
func (connection *p5ExtensionTraceConn) Ping(ctx context.Context) error {
	if pinger, ok := connection.Conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}
func (connection *p5ExtensionTraceConn) ResetSession(ctx context.Context) error {
	if resetter, ok := connection.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}
func (connection *p5ExtensionTraceConn) IsValid() bool {
	if validator, ok := connection.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}
func (connection *p5ExtensionTraceConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := connection.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

type p5ExtensionTraceStmt struct {
	driver.Stmt
	query string
	trace *p5ExtensionSQLTrace
}

func (statement *p5ExtensionTraceStmt) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := statement.Stmt.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}

func (statement *p5ExtensionTraceStmt) Exec(arguments []driver.Value) (driver.Result, error) {
	statement.trace.record(statement.query)
	return statement.Stmt.Exec(arguments)
}
func (statement *p5ExtensionTraceStmt) Query(arguments []driver.Value) (driver.Rows, error) {
	statement.trace.record(statement.query)
	return statement.Stmt.Query(arguments)
}
func (statement *p5ExtensionTraceStmt) ExecContext(ctx context.Context, arguments []driver.NamedValue) (driver.Result, error) {
	if contextual, ok := statement.Stmt.(driver.StmtExecContext); ok {
		statement.trace.record(statement.query)
		return contextual.ExecContext(ctx, arguments)
	}
	return nil, driver.ErrSkip
}
func (statement *p5ExtensionTraceStmt) QueryContext(ctx context.Context, arguments []driver.NamedValue) (driver.Rows, error) {
	if contextual, ok := statement.Stmt.(driver.StmtQueryContext); ok {
		statement.trace.record(statement.query)
		return contextual.QueryContext(ctx, arguments)
	}
	return nil, driver.ErrSkip
}

func seedP5ExtensionUsers(t *testing.T, database *sqlx.DB, provider golem.Provider) {
	t.Helper()
	for index, row := range []struct{ owner, name string }{
		{"alpha", "alpha-a-open"}, {"alpha", "alpha-hidden"}, {"alpha", "alpha-z-open"},
		{"beta", "beta-a-open"}, {"beta", "beta-hidden"}, {"beta", "beta-z-open"},
	} {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", index+1)
		if _, err := database.ExecContext(context.Background(), database.Rebind(`INSERT INTO `+p5ExtensionUsersTable(provider)+` ("id","owner","name","counter") VALUES (?,?,?,0)`), id, row.owner, row.name); err != nil {
			t.Fatal(err)
		}
	}
}

func p5ExtensionUsersTable(provider golem.Provider) string {
	if provider == golem.PostgreSQL {
		return `"` + string(p5ExtensionPostgreSQLNamespace) + `"."users"`
	}
	return `"users"`
}

func executeP5ExtensionGraphQL(t *testing.T, server *p5extensions.GraphQLServer, principal p5extensions.Principal, query string, variables map[string]any) p5ExtensionGraphQLResponse {
	t.Helper()
	return executeP5ExtensionGraphQLOptional(t, server, &principal, query, variables)
}

func executeP5ExtensionGraphQLOptional(t *testing.T, server *p5extensions.GraphQLServer, principal *p5extensions.Principal, query string, variables map[string]any) p5ExtensionGraphQLResponse {
	t.Helper()
	ctx := context.Background()
	if principal != nil {
		ctx = context.WithValue(ctx, p5ExtensionPrincipalKey{}, *principal)
	}
	response, err := executeP5ExtensionGraphQLRaw(ctx, server, query, variables)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func executeP5ExtensionGraphQLRaw(ctx context.Context, server *p5extensions.GraphQLServer, query string, variables map[string]any) (p5ExtensionGraphQLResponse, error) {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return p5ExtensionGraphQLResponse{}, err
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	var response p5ExtensionGraphQLResponse
	decoder := json.NewDecoder(recorder.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return p5ExtensionGraphQLResponse{}, fmt.Errorf("decode GraphQL status=%d body=%q: %w", recorder.Code, recorder.Body.String(), err)
	}
	return response, nil
}

func p5ExtensionRows(t *testing.T, value any) []map[string]any {
	t.Helper()
	values, ok := value.([]any)
	if !ok {
		t.Fatalf("GraphQL rows=%#v", value)
	}
	result := make([]map[string]any, len(values))
	for index, value := range values {
		row, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("GraphQL row %d=%#v", index, value)
		}
		result[index] = row
	}
	return result
}

func p5ExtensionObject(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("GraphQL object=%#v", value)
	}
	return result
}
