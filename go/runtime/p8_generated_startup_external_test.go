package runtime_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/testenv"
)

func TestGeneratedAppOpenUsesVerifiedDatabaseHandleInExternalModule(t *testing.T) {
	p8RunExternalGeneratedStartupCase(t, "TestGeneratedAppOpenUsesVerifiedDatabaseHandle", true)
}

func TestAppOpenIsReadOnlyAndStartsNoBackgroundWorkInExternalModule(t *testing.T) {
	p8RunExternalGeneratedStartupCase(t, "TestAppOpenIsReadOnlyAndStartsNoBackgroundWork", true)
}

func TestAppOpenRefusesClosedStaleCapabilityAndSchemaMismatchInExternalModule(t *testing.T) {
	p8RunExternalGeneratedStartupCase(t, "TestAppOpenRefusals", false)
}

func TestApplicationNeverClosesBorrowedDatabaseInExternalModule(t *testing.T) {
	p8RunExternalGeneratedStartupCase(t, "TestApplicationNeverClosesBorrowedDatabase", true)
}

func TestGeneratedAppOpenRejectsEveryPoisonedPoolSlotInExternalModule(t *testing.T) {
	p8RunExternalGeneratedStartupCase(t, "TestGeneratedAppOpenRejectsEveryPoisonedPoolSlot", true)
}

func p8RunExternalGeneratedStartupCase(t *testing.T, name string, bothProviders bool) {
	t.Helper()
	module := t.TempDir()
	_, filename, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("locate runtime test source")
	}
	golemModule := filepath.Dir(filepath.Dir(filename))
	digest := sha256.Sum256([]byte(name))
	schemaName := fmt.Sprintf("p8_startup_%x", digest[:6])
	providers := "golem.SQLite"
	if bothProviders {
		providers = "golem.SQLite, golem.PostgreSQL"
	}
	p8WriteExternalFile(t, module, "go.mod", fmt.Sprintf("module example.test/p8startup\n\ngo 1.25.0\n\nrequire (\n\tgithub.com/eleven-am/golem/go v0.0.0\n\tgithub.com/99designs/gqlgen v0.17.70\n\tgithub.com/vektah/gqlparser/v2 v2.5.23\n)\nreplace github.com/eleven-am/golem/go => %s\n", filepath.ToSlash(golemModule)))
	p8WriteExternalFile(t, module, "schema.go", fmt.Sprintf(`package startup

import "github.com/eleven-am/golem/go/golem"

type Actor struct{ ID int64 }

type User struct {
	_ struct{} %[1]sgolem:"model;id=p8.User;table=users"%[1]s
	ID int64 %[1]sdb:"id" golem:"id=p8.User.ID;pk"%[1]s
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, %q)
	golem.Actor[Actor](schema)
	golem.Model[User](schema)
	golem.Providers(schema, %s)
}

func (User) DefinePolicy(rules *golem.Rules[User], actor Actor) {
	_ = rules
	_ = actor
}

func (User) GolemModel() golem.ModelSpec[User] {
	return golem.DefineModel(golem.Subscriptions[User]())
}
`, "`", schemaName, providers))
	p8WriteExternalFile(t, module, "tools.go", `//go:build tools

package startup

import (
	_ "github.com/99designs/gqlgen/graphql"
	_ "github.com/vektah/gqlparser/v2/ast"
)
`)

	binary := filepath.Join(module, "golem-test")
	p8RunExternalCommand(t, golemModule, nil, "go", "build", "-o", binary, "./cmd/golem")
	p8RunExternalCommand(t, module, nil, "go", "mod", "tidy")
	p8RunExternalCommand(t, module, nil, binary, "migration", "new", "--name", "initial")
	p8RunExternalCommand(t, module, nil, binary, "generate", "--app-out", "./app")
	p8RunExternalCommand(t, module, nil, "go", "mod", "tidy")
	databasePath := filepath.Join(module, "startup.sqlite")
	p8RunExternalCommand(t, module, nil, binary, "migration", "apply", "--provider", "sqlite", "--dsn", databasePath)
	environment := []string{"GOLEM_P8_SQLITE_DSN=" + databasePath, "GOLEM_P8_SCHEMA=" + schemaName}
	for _, profile := range []struct{ name, environment string }{
		{name: "c", environment: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "linguistic", environment: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	} {
		administrativeDSN := strings.TrimSpace(os.Getenv(profile.environment))
		if administrativeDSN == "" {
			testenv.FailMissingPostgreSQLIfRequired(t, profile.environment+" is not configured")
			continue
		}
		dsn := testenv.DisposablePostgreSQLFrom(t, administrativeDSN)
		environment = append(environment, profile.environment+"="+dsn)
		if bothProviders {
			p8RunExternalCommand(t, module, environment, binary, "migration", "apply", "--provider", "postgresql", "--dsn", dsn)
		}
	}

	p8WriteExternalFile(t, module, "app/p8_startup_external_test.go", p8ExternalGeneratedStartupSource)
	err := filepath.WalkDir(module, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".go" {
			return err
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(content, []byte("github.com/eleven-am/golem/go/internal/")) {
			return fmt.Errorf("consumer-facing source %s imports a Golem internal package", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p8RunExternalCommand(t, module, environment, "go", "test", "./app", "-run", "^"+name+"$", "-count=1")
}

func p8WriteExternalFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func p8RunExternalCommand(t *testing.T, directory string, extraEnvironment []string, name string, arguments ...string) {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), extraEnvironment...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(arguments, " "), err, output)
	}
}

const p8ExternalGeneratedStartupSource = `package app

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	startup "example.test/p8startup"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider"
	"github.com/eleven-am/golem/go/provider/postgresql"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type startupTarget struct {
	name string
	open func(*testing.T) *provider.Database
	userCountSQL string
	ledgerCountSQL string
	catalogSQL string
	dataSQL string
	ledgerSQL string
	poisonSQL string
}

func startupTargets() []startupTarget {
	targets := []startupTarget{{
		name: "sqlite",
		open: func(t *testing.T) *provider.Database {
			database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: "file:" + os.Getenv("GOLEM_P8_SQLITE_DSN")})
			if err != nil { t.Fatal(err) }
			return database
		},
		userCountSQL: ` + "`SELECT COUNT(*) FROM \"users\"`" + `,
		ledgerCountSQL: ` + "`SELECT COUNT(*) FROM \"_golem_migrations\"`" + `,
		catalogSQL: ` + "`SELECT type || ':' || name || ':' || COALESCE(sql,'') FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name`" + `,
		dataSQL: ` + "`SELECT CAST(id AS TEXT) FROM \"users\" ORDER BY id`" + `,
		ledgerSQL: ` + "`SELECT migration_id || ':' || parent_chain_hash || ':' || chain_hash || ':' || file_checksums || ':' || before_physical_fingerprint || ':' || after_physical_fingerprint || ':' || phases || ':' || applied_at FROM \"_golem_migrations\" ORDER BY migration_id`" + `,
		poisonSQL: "PRAGMA foreign_keys = OFF",
	}}
	for _, profile := range []struct{name, dsn string}{
		{name: "postgresql-c", dsn: strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN"))},
		{name: "postgresql-linguistic", dsn: strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"))},
	} {
		profile := profile
		if profile.dsn == "" { continue }
		targets = append(targets, startupTarget{
			name: profile.name,
			open: func(t *testing.T) *provider.Database {
				database, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: profile.dsn, Pool: postgresql.PoolConfig{MaximumOpen: 4, MaximumIdle: 4}})
				if err != nil { t.Fatal(err) }
				return database
			},
			userCountSQL: ` + "`SELECT COUNT(*) FROM \"public\".\"users\"`" + `,
			ledgerCountSQL: ` + "`SELECT COUNT(*) FROM \"_golem\".\"_golem_migrations\"`" + `,
			catalogSQL: ` + "`SELECT n.nspname || ':' || c.relkind::text || ':' || c.relname || ':' || COALESCE(a.attnum::text,'') || ':' || COALESCE(a.attname,'') || ':' || COALESCE(pg_catalog.format_type(a.atttypid,a.atttypmod),'') FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped WHERE n.nspname IN ('public','_golem') ORDER BY n.nspname,c.relname,a.attnum`" + `,
			dataSQL: ` + "`SELECT id::text FROM \"public\".\"users\" ORDER BY id`" + `,
			ledgerSQL: ` + "`SELECT migration_id || ':' || parent_chain_hash || ':' || chain_hash || ':' || file_checksums::text || ':' || before_physical_fingerprint || ':' || after_physical_fingerprint || ':' || phases::text || ':' || applied_at::text FROM \"_golem\".\"_golem_migrations\" ORDER BY migration_id`" + `,
			poisonSQL: "SET timezone = 'Europe/Paris'",
		})
	}
	return targets
}

type managedSnapshot struct { Catalog, Data, Ledger []string }

func snapshotManaged(t *testing.T, database *provider.Database, target startupTarget) managedSnapshot {
	t.Helper()
	result := managedSnapshot{Catalog: []string{}, Data: []string{}, Ledger: []string{}}
	if err := database.UnsafeSQLX().Select(&result.Catalog, target.catalogSQL); err != nil { t.Fatal(err) }
	if err := database.UnsafeSQLX().Select(&result.Data, target.dataSQL); err != nil { t.Fatal(err) }
	if err := database.UnsafeSQLX().Select(&result.Ledger, target.ledgerSQL); err != nil { t.Fatal(err) }
	return result
}

type countingTransport struct { publish, subscribe atomic.Int64 }
func (transport *countingTransport) Publish(context.Context, events.EventBatch) error { transport.publish.Add(1); return nil }
func (transport *countingTransport) Subscribe(context.Context, events.Subscription) (events.Stream, error) { transport.subscribe.Add(1); return nil, events.Failure(events.CodeEventSourceClosed) }
func (*countingTransport) TransportCapabilities() events.TransportCapabilities { value, _ := events.NewTransportCapabilities("p8.row4.transport", events.TransportScopeProcessLocal, false); return value }

type countingCDC struct { provider golem.Provider; runs, correlations atomic.Int64 }
func (adapter *countingCDC) Identity() events.CDCIdentity { return events.CDCIdentity{Name:"p8-row4", Version:"1", Provider:adapter.provider} }
func (adapter *countingCDC) CorrelatesGolemTransaction(context.Context, events.CDCCorrelationInput) (bool,error) { adapter.correlations.Add(1); return false,nil }
func (adapter *countingCDC) Run(context.Context, events.CDCEmitter) error { adapter.runs.Add(1); return nil }

func startupConfig(database *provider.Database) Config[startup.Actor] {
	return Config[startup.Actor]{
		Database: database,
		ResolvePrincipal: func(_ context.Context, actor startup.Actor) (startup.Actor, error) { return actor, nil },
		AuditPrincipal: func(actor startup.Actor) string { return fmt.Sprint(actor.ID) },
		ReportScopedQuery: func(context.Context, golem.ScopedAuditRecord) {},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		EventTransport: &countingTransport{},
	}
}

func openStartupSQLiteCopy(t *testing.T) *provider.Database {
	t.Helper()
	content, err := os.ReadFile(os.Getenv("GOLEM_P8_SQLITE_DSN"))
	if err != nil { t.Fatal(err) }
	path := t.TempDir() + "/startup.sqlite"
	if err := os.WriteFile(path, content, 0600); err != nil { t.Fatal(err) }
	database, err := sqlite.Open(context.Background(), sqlite.Config{DataSourceName: "file:" + path})
	if err != nil { t.Fatal(err) }
	return database
}

func TestGeneratedAppOpenUsesVerifiedDatabaseHandle(t *testing.T) {
	for _, target := range startupTargets() {
		t.Run(target.name, func(t *testing.T) {
			database := target.open(t)
			defer database.Close()
			application, err := Open(context.Background(), startupConfig(database))
			if err != nil { t.Fatal(err) }
			if application == nil || database.UnsafeSQLX() == nil || database.Provider() != database.Capabilities().Provider() {
				t.Fatal("generated Open did not retain the verified provider handle identity")
			}
		})
	}
}

func TestAppOpenIsReadOnlyAndStartsNoBackgroundWork(t *testing.T) {
	for _, target := range startupTargets() {
		t.Run(target.name, func(t *testing.T) {
			database := target.open(t)
			defer database.Close()
			before := snapshotManaged(t, database, target)
			goroutinesBefore := runtime.NumGoroutine()
			transport := &countingTransport{}
			adapter := &countingCDC{provider:database.Provider()}
			config := startupConfig(database)
			config.EventTransport = transport
			config.CDCAdapters = []events.CDCAdapter{adapter}
			application, err := Open(context.Background(), config)
			if err != nil { t.Fatal(err) }
			runtime.Gosched()
			after := snapshotManaged(t, database, target)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("generated Open mutated managed catalog/system/data state\nbefore=%#v\nafter=%#v", before, after)
			}
			if application.EventCapabilities().PublisherRunning() {
				t.Fatal("generated Open started the event publisher")
			}
			if after := runtime.NumGoroutine(); after > goroutinesBefore {
				t.Fatalf("generated Open added a background goroutine: before=%d after=%d", goroutinesBefore, after)
			}
			if transport.publish.Load()!=0 || transport.subscribe.Load()!=0 || adapter.runs.Load()!=0 || adapter.correlations.Load()!=0 {
				t.Fatalf("generated Open performed background transport/CDC work: publish=%d subscribe=%d run=%d correlate=%d", transport.publish.Load(), transport.subscribe.Load(), adapter.runs.Load(), adapter.correlations.Load())
			}
		})
	}
}

func TestGeneratedAppOpenRejectsEveryPoisonedPoolSlot(t *testing.T) {
	for _, target := range startupTargets() {
		t.Run(target.name, func(t *testing.T) {
			probe := target.open(t)
			width := probe.Pool().MaximumOpen()
			if err := probe.Close(); err != nil { t.Fatal(err) }
			for poisoned := 0; poisoned < width; poisoned++ {
				database := target.open(t)
				connections := make([]*sqlx.Conn, width)
				for index := range connections {
					connection, err := database.UnsafeSQLX().Connx(context.Background())
					if err != nil { t.Fatal(err) }
					connections[index] = connection
				}
				if _, err := connections[poisoned].ExecContext(context.Background(), target.poisonSQL); err != nil { t.Fatal(err) }
				for _, connection := range connections { if err := connection.Close(); err != nil { t.Fatal(err) } }
				if _, err := Open(context.Background(), startupConfig(database)); err == nil {
					t.Fatalf("generated Open accepted poisoned pool slot %d of %d", poisoned, width)
				}
				if err := database.Close(); err != nil { t.Fatal(err) }
			}
			database := target.open(t)
			connection, err := database.UnsafeSQLX().Connx(context.Background())
			if err != nil { t.Fatal(err) }
			if _, err := Open(context.Background(), startupConfig(database)); err == nil { t.Fatal("generated Open accepted an in-use pool") }
			_ = connection.Close()
			_ = database.Close()
		})
	}
}

func TestAppOpenRefusals(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if _, err := Open(context.Background(), startupConfig(nil)); err == nil {
			t.Fatal("generated Open accepted a nil database handle")
		}
	})
	t.Run("closed", func(t *testing.T) {
		database := openStartupSQLiteCopy(t)
		if err := database.Close(); err != nil { t.Fatal(err) }
		if _, err := Open(context.Background(), startupConfig(database)); err == nil {
			t.Fatal("generated Open accepted a closed database handle")
		}
	})
	t.Run("stale-capability", func(t *testing.T) {
		database := openStartupSQLiteCopy(t)
		defer database.Close()
		database.UnsafeSQLX().SetMaxOpenConns(1)
		database.UnsafeSQLX().SetMaxIdleConns(1)
		connection, err := database.UnsafeSQLX().Connx(context.Background())
		if err != nil { t.Fatal(err) }
		if _, err := connection.ExecContext(context.Background(), "PRAGMA foreign_keys = OFF"); err != nil { t.Fatal(err) }
		if err := connection.Close(); err != nil { t.Fatal(err) }
		if _, err := Open(context.Background(), startupConfig(database)); err == nil {
			t.Fatal("generated Open accepted stale provider-owned capabilities")
		}
	})
	t.Run("schema-mismatch", func(t *testing.T) {
		database := openStartupSQLiteCopy(t)
		defer database.Close()
		if _, err := database.UnsafeSQLX().ExecContext(context.Background(), ` + "`DROP TABLE \"users\"`" + `); err != nil { t.Fatal(err) }
		if _, err := Open(context.Background(), startupConfig(database)); err == nil {
			t.Fatal("generated Open accepted physical schema drift")
		}
	})
	t.Run("ledger-mismatch", func(t *testing.T) {
		database := openStartupSQLiteCopy(t)
		defer database.Close()
		if _, err := database.UnsafeSQLX().ExecContext(context.Background(), ` + "`DELETE FROM \"_golem_migrations\"`" + `); err != nil { t.Fatal(err) }
		if _, err := Open(context.Background(), startupConfig(database)); err == nil {
			t.Fatal("generated Open accepted a live ledger mismatch")
		}
	})
	for _, profile := range []struct{name, dsn string}{
		{name: "postgresql-c", dsn: strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN"))},
		{name: "postgresql-linguistic", dsn: strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"))},
	} {
		profile := profile
		if profile.dsn == "" { continue }
		t.Run("provider-mismatch-"+profile.name, func(t *testing.T) {
			database, err := postgresql.Open(context.Background(), postgresql.Config{DataSourceName: profile.dsn})
			if err != nil { t.Fatal(err) }
			defer database.Close()
			if _, err := Open(context.Background(), startupConfig(database)); err == nil {
				t.Fatal("SQLite-only generated application accepted a PostgreSQL handle")
			}
		})
	}
}

func TestApplicationNeverClosesBorrowedDatabase(t *testing.T) {
	for _, target := range startupTargets() {
		t.Run(target.name, func(t *testing.T) {
			database := target.open(t)
			defer database.Close()
			if _, err := Open(context.Background(), startupConfig(database)); err != nil { t.Fatal(err) }
			if database.UnsafeSQLX() == nil { t.Fatal("application Open closed its borrowed handle") }
			if err := database.UnsafeSQLX().PingContext(context.Background()); err != nil {
				t.Fatalf("borrowed database is unusable after generated Open: %v", err)
			}
		})
	}
}
`
