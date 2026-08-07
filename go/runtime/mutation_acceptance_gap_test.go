package runtime

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyoperator "github.com/eleven-am/golem/go/internal/policy/operator"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

var mutationBoundaryDriverSequence atomic.Uint64
var mutationOutboxNamespaceSequence atomic.Uint64

type mutationBoundaryCounts struct {
	begins  atomic.Int64
	queries atomic.Int64
	execs   atomic.Int64
}

func (counts *mutationBoundaryCounts) reset() {
	counts.begins.Store(0)
	counts.queries.Store(0)
	counts.execs.Store(0)
}

type mutationBoundaryDriver struct {
	inner  driver.Driver
	counts *mutationBoundaryCounts
}

func (value *mutationBoundaryDriver) Open(name string) (driver.Conn, error) {
	connection, err := value.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &mutationBoundaryConnection{Conn: connection, counts: value.counts}, nil
}

type mutationBoundaryConnection struct {
	driver.Conn
	counts *mutationBoundaryCounts
}

func (connection *mutationBoundaryConnection) Begin() (driver.Tx, error) {
	connection.counts.begins.Add(1)
	return connection.Conn.Begin()
}

func (connection *mutationBoundaryConnection) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	connection.counts.begins.Add(1)
	if beginner, ok := connection.Conn.(driver.ConnBeginTx); ok {
		return beginner.BeginTx(ctx, options)
	}
	return connection.Conn.Begin()
}

func (connection *mutationBoundaryConnection) QueryContext(ctx context.Context, query string, arguments []driver.NamedValue) (driver.Rows, error) {
	queryer, ok := connection.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	connection.counts.queries.Add(1)
	return queryer.QueryContext(ctx, query, arguments)
}

func (connection *mutationBoundaryConnection) ExecContext(ctx context.Context, query string, arguments []driver.NamedValue) (driver.Result, error) {
	executor, ok := connection.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "BEGIN ") {
		// SQLite's required immediate transaction is deliberately opened with
		// BEGIN IMMEDIATE on an owned connection rather than database/sql's
		// BeginTx API. Count both provider transaction mechanisms alike.
		connection.counts.begins.Add(1)
	}
	connection.counts.execs.Add(1)
	return executor.ExecContext(ctx, query, arguments)
}

func openMutationBoundarySQLite(t testing.TB) (*sqlx.DB, *mutationBoundaryCounts) {
	t.Helper()
	registered, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	inner := registered.Driver()
	_ = registered.Close()
	counts := &mutationBoundaryCounts{}
	driverName := fmt.Sprintf("golem_p4_mutation_boundary_%d", mutationBoundaryDriverSequence.Add(1))
	sql.Register(driverName, &mutationBoundaryDriver{inner: inner, counts: counts})
	dsn := "file:" + filepath.Join(t.TempDir(), "classification-boundary.db") + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
	database, err := sqlx.Open(driverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	return database, counts
}

func openMutationBoundaryPostgreSQL(t testing.TB, dsn string) (*sqlx.DB, *mutationBoundaryCounts) {
	t.Helper()
	counts := &mutationBoundaryCounts{}
	driverName := fmt.Sprintf("golem_p4_postgres_mutation_boundary_%d", mutationBoundaryDriverSequence.Add(1))
	sql.Register(driverName, &mutationBoundaryDriver{inner: stdlib.GetDefaultDriver(), counts: counts})
	database, err := sqlx.Open(driverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	return database, counts
}

func TestRefusedMutationDoesNotBeginTransactionOrIssueSQL(t *testing.T) {
	assertRefused := func(t *testing.T, fixture mutationResultFixture, counts *mutationBoundaryCounts) {
		t.Helper()
		caller, err := fixture.app.ForPrincipal(context.Background(), mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}

		// Title is conditionally readable only for posts whose related author is
		// Alice, while the update policy is unconditional. Naming title in this
		// selector guard therefore cannot be discharged over the complete update
		// reach and must be classified/refused before the execution boundary.
		selector := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 11}))
		target := selector.And(fixture.title.Eq("classified-secret"))
		counts.reset()
		_, err = CallerUpdate(context.Background(), caller, fixture.postDescriptor, target, fixture.updateTitle("must-not-run"))
		if err == nil {
			t.Fatal("classification-refused mutation unexpectedly succeeded")
		}
		if begins, queries, execs := counts.begins.Load(), counts.queries.Load(), counts.execs.Load(); begins != 0 || queries != 0 || execs != 0 {
			t.Fatalf("refused mutation crossed SQL boundary: begins=%d queries=%d execs=%d", begins, queries, execs)
		}
		var failure *golem.Error
		if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
			t.Fatalf("refusal=%#v err=%v", failure, err)
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		database, counts := openMutationBoundarySQLite(t)
		fixture := newMutationResultFixtureWithHooksAndDatabase(t, MutationLimits{}, nil, nil, database)
		assertRefused(t, fixture, counts)
	})
	for _, profile := range []struct{ name, namespace, env string }{{"postgresql-c", "c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for PostgreSQL refusal-boundary evidence", profile.env)
			}
			fixture, _, _ := newPostgreSQLMutationOracleFixture(t, dsn, profile.namespace)
			database, counts := openMutationBoundaryPostgreSQL(t, dsn)
			t.Cleanup(func() { _ = database.Close() })
			app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
				DB: database, Provider: golem.PostgreSQL, Bundle: fixture.schema.Bundle,
				Bindings: fixture.app.bindings, Descriptors: fixture.app.descriptors,
				ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
			}))
			if err != nil {
				t.Fatal(err)
			}
			fixture.app = app
			assertRefused(t, fixture, counts)
		})
	}
}

func TestScalarAndRelationCorrelationOverlapRefusesBeforeSQLAcrossProviders(t *testing.T) {
	assertRefused := func(t *testing.T, fixture mutationResultFixture, counts *mutationBoundaryCounts) {
		t.Helper()
		ctx := context.Background()
		userTwo := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 2}))
		input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1}),
			golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, userTwo))
		post := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 11}))
		recursive := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedNestedUpdate[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, post, input))
		user := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range []struct {
			name string
			run  func() error
		}{
			{"caller", func() error {
				_, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(11), input)
				return err
			}},
			{"system", func() error {
				_, err := SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(11), input)
				return err
			}},
			{"recursive-caller", func() error {
				_, err := CallerUpdate(ctx, caller, fixture.userDescriptor, user, recursive)
				return err
			}},
			{"recursive-system", func() error {
				_, err := SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, user, recursive)
				return err
			}},
		} {
			t.Run(operation.name, func(t *testing.T) {
				counts.reset()
				err := operation.run()
				var failure *golem.Error
				if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
					t.Fatalf("overlap refusal=%#v err=%v", failure, err)
				}
				if begins, queries, execs := counts.begins.Load(), counts.queries.Load(), counts.execs.Load(); begins != 0 || queries != 0 || execs != 0 {
					t.Fatalf("overlap crossed SQL boundary: begins=%d queries=%d execs=%d", begins, queries, execs)
				}
			})
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		database, counts := openMutationBoundarySQLite(t)
		fixture := newMutationResultFixtureWithHooksAndDatabase(t, MutationLimits{}, nil, nil, database)
		assertRefused(t, fixture, counts)
	})
	for _, profile := range []struct{ name, namespace, env string }{{"postgresql-c", "c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for correlation-overlap refusal evidence", profile.env)
			}
			fixture, _, _ := newPostgreSQLMutationOracleFixture(t, dsn, profile.namespace)
			database, counts := openMutationBoundaryPostgreSQL(t, dsn)
			t.Cleanup(func() { _ = database.Close() })
			app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
				DB: database, Provider: golem.PostgreSQL, Bundle: fixture.schema.Bundle,
				Bindings: fixture.app.bindings, Descriptors: fixture.app.descriptors,
				ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
			}))
			if err != nil {
				t.Fatal(err)
			}
			fixture.app = app
			assertRefused(t, fixture, counts)
		})
	}
}

func TestDuplicateToOneRelationValuesRefuseBeforeSQLAcrossProviders(t *testing.T) {
	assertRefused := func(t *testing.T, fixture mutationResultFixture, counts *mutationBoundaryCounts) {
		t.Helper()
		ctx := context.Background()
		user := func(id byte) golem.MutationTarget[mutationResultUser] {
			return golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
				golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: id}))
		}
		create := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 3}),
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "duplicate"))
		inputs := []struct {
			name  string
			input golem.UpdateInput[mutationResultPost]
		}{
			{"connect-connect", golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
				golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, user(1)),
				golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, user(2)))},
			{"create-connect", golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
				golem.GeneratedNestedCreate[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, create),
				golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, user(2)))},
		}
		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range inputs {
			input := input
			for _, stance := range []struct {
				name string
				run  func() error
			}{
				{"caller", func() error {
					_, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(11), input.input)
					return err
				}},
				{"system", func() error {
					_, err := SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(11), input.input)
					return err
				}},
			} {
				t.Run(input.name+"-"+stance.name, func(t *testing.T) {
					counts.reset()
					err := stance.run()
					var failure *golem.Error
					if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
						t.Fatalf("duplicate relation refusal=%#v err=%v", failure, err)
					}
					if begins, queries, execs := counts.begins.Load(), counts.queries.Load(), counts.execs.Load(); begins != 0 || queries != 0 || execs != 0 {
						t.Fatalf("duplicate relation crossed SQL boundary: begins=%d queries=%d execs=%d", begins, queries, execs)
					}
				})
			}
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		database, counts := openMutationBoundarySQLite(t)
		fixture := newMutationResultFixtureWithHooksAndDatabase(t, MutationLimits{}, nil, nil, database)
		assertRefused(t, fixture, counts)
	})
	for _, profile := range []struct{ name, namespace, env string }{{"postgresql-c", "c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for duplicate-relation refusal evidence", profile.env)
			}
			fixture, _, _ := newPostgreSQLMutationOracleFixture(t, dsn, profile.namespace)
			database, counts := openMutationBoundaryPostgreSQL(t, dsn)
			t.Cleanup(func() { _ = database.Close() })
			app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
				DB: database, Provider: golem.PostgreSQL, Bundle: fixture.schema.Bundle,
				Bindings: fixture.app.bindings, Descriptors: fixture.app.descriptors,
				ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
			}))
			if err != nil {
				t.Fatal(err)
			}
			fixture.app = app
			assertRefused(t, fixture, counts)
		})
	}
}

func TestTargetlessToManyDisconnectRefusesBeforeSQLAcrossProviders(t *testing.T) {
	assertRefused := func(t *testing.T, fixture mutationResultFixture, counts *mutationBoundaryCounts) {
		t.Helper()
		ctx := context.Background()
		var missing golem.MutationTarget[mutationResultPost]
		input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedNestedDisconnect[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, missing))
		user := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range []struct {
			name string
			run  func() error
		}{
			{"caller", func() error {
				_, err := CallerUpdate(ctx, caller, fixture.userDescriptor, user, input)
				return err
			}},
			{"system", func() error {
				_, err := SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, user, input)
				return err
			}},
		} {
			t.Run(operation.name, func(t *testing.T) {
				counts.reset()
				err := operation.run()
				var failure *golem.Error
				if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
					t.Fatalf("targetless disconnect refusal=%#v err=%v", failure, err)
				}
				if begins, queries, execs := counts.begins.Load(), counts.queries.Load(), counts.execs.Load(); begins != 0 || queries != 0 || execs != 0 {
					t.Fatalf("targetless disconnect crossed SQL boundary: begins=%d queries=%d execs=%d", begins, queries, execs)
				}
			})
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		database, counts := openMutationBoundarySQLite(t)
		fixture := newMutationResultFixtureWithHooksAndDatabase(t, MutationLimits{}, nil, nil, database)
		assertRefused(t, fixture, counts)
	})
	for _, profile := range []struct{ name, namespace, env string }{{"postgresql-c", "c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for targetless disconnect refusal evidence", profile.env)
			}
			fixture, _, _ := newPostgreSQLMutationOracleFixture(t, dsn, profile.namespace)
			database, counts := openMutationBoundaryPostgreSQL(t, dsn)
			t.Cleanup(func() { _ = database.Close() })
			app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
				DB: database, Provider: golem.PostgreSQL, Bundle: fixture.schema.Bundle,
				Bindings: fixture.app.bindings, Descriptors: fixture.app.descriptors,
				ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
			}))
			if err != nil {
				t.Fatal(err)
			}
			fixture.app = app
			assertRefused(t, fixture, counts)
		})
	}
}

func TestMissingExactNestedTargetsReturnNotFoundAcrossProviders(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		user := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		missing := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 240}))
		operations := []struct {
			name  string
			value golem.NestedUpdateValue[mutationResultUser]
		}{
			{"update", golem.GeneratedNestedUpdate[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, missing, fixture.updateTitle("missing"))},
			{"delete", golem.GeneratedNestedDelete[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, missing)},
			{"connect", golem.GeneratedNestedConnect[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, missing)},
			{"disconnect", golem.GeneratedNestedDisconnect[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, missing)},
		}
		for _, operation := range operations {
			operation := operation
			for _, stance := range []string{"caller", "system"} {
				stance := stance
				t.Run(operation.name+"-"+stance, func(t *testing.T) {
					input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User, operation.value)
					var err error
					if stance == "caller" {
						_, err = CallerUpdate(ctx, caller, fixture.userDescriptor, user, input)
					} else {
						_, err = SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, user, input)
					}
					var failure *golem.Error
					if !errors.As(err, &failure) || failure.Code != golem.CodeNotFound {
						t.Fatalf("missing nested target failure=%#v err=%v", failure, err)
					}
				})
			}
		}
	})
}

func TestRequiredInverseHasOneDisconnectRefusesBeforeSQLAcrossProviders(t *testing.T) {
	assertRefused := func(t *testing.T, fixture mutationResultFixture, counts *mutationBoundaryCounts) {
		t.Helper()
		ctx := context.Background()
		input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedNestedDisconnectOne[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post))
		user := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range []struct {
			name string
			run  func() error
		}{
			{"caller", func() error {
				_, err := CallerUpdate(ctx, caller, fixture.userDescriptor, user, input)
				return err
			}},
			{"system", func() error {
				_, err := SystemUpdate(ctx, fixture.app.System(), fixture.userDescriptor, user, input)
				return err
			}},
		} {
			t.Run(operation.name, func(t *testing.T) {
				counts.reset()
				err := operation.run()
				var failure *golem.Error
				if !errors.As(err, &failure) || failure.Code != golem.CodeBadUserInput {
					t.Fatalf("required inverse disconnect refusal=%#v err=%v", failure, err)
				}
				if begins, queries, execs := counts.begins.Load(), counts.queries.Load(), counts.execs.Load(); begins != 0 || queries != 0 || execs != 0 {
					t.Fatalf("required inverse disconnect crossed SQL boundary: begins=%d queries=%d execs=%d", begins, queries, execs)
				}
			})
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		database, counts := openMutationBoundarySQLite(t)
		schemaFixture := schematest.NewSubscribedIndexedInverseRequiredHasOne(t)
		if err := sqliteprovider.New().ApplyInitial(context.Background(), database, schemaFixture.SQLite); err != nil {
			t.Fatal(err)
		}
		seedMutationBoundaryUsers(t, database, golem.SQLite, "")
		fixture := mutationResultFixtureForSchema(t, database, golem.SQLite, schemaFixture)
		assertRefused(t, fixture, counts)
	})
	for _, profile := range []struct{ name, namespace, env string }{{"postgresql-c", "c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for required inverse has-one refusal evidence", profile.env)
			}
			sequence := mutationOutboxNamespaceSequence.Add(1)
			applicationNamespace := fmt.Sprintf("golem_p4_inverse_%s_%d_%d", profile.namespace, os.Getpid(), sequence)
			systemNamespace := fmt.Sprintf("golem_p4_inverse_system_%s_%d_%d", profile.namespace, os.Getpid(), sequence)
			schemaFixture := schematest.NewSubscribedIndexedInverseRequiredHasOnePostgreSQLNamespaces(t, physical.PhysicalName(applicationNamespace), physical.PhysicalName(systemNamespace))
			provider := postgresprovider.New()
			setup, _, err := provider.Open(context.Background(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = setup.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(applicationNamespace) + ` CASCADE`)
				_, _ = setup.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(systemNamespace) + ` CASCADE`)
				_ = setup.Close()
			})
			if err := provider.ApplyInitial(context.Background(), setup, schemaFixture.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			seedMutationBoundaryUsers(t, setup, golem.PostgreSQL, applicationNamespace)
			database, counts := openMutationBoundaryPostgreSQL(t, dsn)
			t.Cleanup(func() { _ = database.Close() })
			fixture := mutationResultFixtureForSchema(t, database, golem.PostgreSQL, schemaFixture)
			assertRefused(t, fixture, counts)
		})
	}
}

func TestCallerAlreadyConnectedSourceAndInverseAreZeroWorkAcrossProviders(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		var before, after, afterCommit atomic.Int64
		fixture := mutationResultFixtureWithUpdateProbe(t, profile.fixture, profile.provider, &before, &after, &afterCommit)
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(201, golem.UUID{15: 1}, "already-connected")); err != nil {
			t.Fatal(err)
		}
		var factsBefore int
		if err := fixture.app.database.GetContext(ctx, &factsBefore, `SELECT COUNT(*) FROM `+profile.outbox); err != nil {
			t.Fatal(err)
		}
		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		user := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
			golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
		post := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 201}))
		source := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedNestedConnect[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, user))
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, post, source); err != nil {
			t.Fatalf("source already-connected: %v: %v", err, errors.Unwrap(err))
		}
		// The source root is itself the Post owner. Its ordinary root Update
		// hook/fact remains real; the synthesized membership child must add no
		// second hook/fact/touched row (the per-attempt touched limit is one).
		if before.Load() != 1 || after.Load() != 1 || afterCommit.Load() != 1 {
			t.Fatalf("source no-op child duplicated root hooks before=%d after=%d afterCommit=%d", before.Load(), after.Load(), afterCommit.Load())
		}
		var factsAfterSource int
		if err := fixture.app.database.GetContext(ctx, &factsAfterSource, `SELECT COUNT(*) FROM `+profile.outbox); err != nil {
			t.Fatal(err)
		}
		if factsAfterSource != factsBefore+1 {
			t.Fatalf("source no-op child facts=%d want root-only %d", factsAfterSource, factsBefore+1)
		}
		inverse := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedNestedConnect[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, post))
		if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, user, inverse); err != nil {
			t.Fatalf("inverse already-connected: %v", err)
		}
		if before.Load() != 1 || after.Load() != 1 || afterCommit.Load() != 1 {
			t.Fatalf("inverse no-op membership ran child hooks before=%d after=%d afterCommit=%d", before.Load(), after.Load(), afterCommit.Load())
		}
		var factsAfter int
		if err := fixture.app.database.GetContext(ctx, &factsAfter, `SELECT COUNT(*) FROM `+profile.outbox); err != nil {
			t.Fatal(err)
		}
		if factsAfter != factsAfterSource {
			t.Fatalf("inverse no-op membership facts=%d want unchanged %d", factsAfter, factsAfterSource)
		}
		var author string
		query := `SELECT "author_id" FROM ` + profile.posts + ` WHERE "id"=` + profile.placeholder(1)
		if err := fixture.app.database.GetContext(ctx, &author, query, mutationResultUUIDText(201)); err != nil || author != mutationResultUUIDText(1) {
			t.Fatalf("no-op membership author=%q err=%v", author, err)
		}
	})
}

type relationDeleteHookProbe struct {
	postUpdateBefore, postUpdateAfter, postUpdateCommit atomic.Int64
	userDeleteBefore, userDeleteAfter, userDeleteCommit atomic.Int64
	postDeleteBefore, postDeleteAfter, postDeleteCommit atomic.Int64
}

func TestDirectionSpecificToOneDeleteAcrossProviders(t *testing.T) {
	t.Run("optional-source", func(t *testing.T) {
		runRelationDeleteProviderProfiles(t, "optional_delete", schematest.NewSubscribedIndexedOptionalSource, schematest.NewSubscribedIndexedOptionalSourcePostgreSQLNamespaces, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
			ctx := context.Background()
			probe := &relationDeleteHookProbe{}
			fixture := mutationResultFixtureWithDeleteProbe(t, profile.fixture, profile.provider, probe)
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(202, golem.UUID{15: 1}, "optional-source")); err != nil {
				t.Fatal(err)
			}
			var factsBefore int
			if err := fixture.app.database.GetContext(ctx, &factsBefore, `SELECT COUNT(*) FROM `+profile.outbox); err != nil {
				t.Fatal(err)
			}
			caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
				golem.GeneratedNestedDelete[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, nil))
			if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(202), input); err != nil {
				t.Fatalf("optional source delete: %v: %v", err, errors.Unwrap(err))
			}
			if probe.postUpdateBefore.Load() != 2 || probe.postUpdateAfter.Load() != 2 || probe.postUpdateCommit.Load() != 2 ||
				probe.userDeleteBefore.Load() != 1 || probe.userDeleteAfter.Load() != 1 || probe.userDeleteCommit.Load() != 1 {
				t.Fatalf("optional source hooks post-update=%d/%d/%d user-delete=%d/%d/%d", probe.postUpdateBefore.Load(), probe.postUpdateAfter.Load(), probe.postUpdateCommit.Load(), probe.userDeleteBefore.Load(), probe.userDeleteAfter.Load(), probe.userDeleteCommit.Load())
			}
			var author sql.NullString
			query := `SELECT "author_id" FROM ` + profile.posts + ` WHERE "id"=` + profile.placeholder(1)
			if err := fixture.app.database.GetContext(ctx, &author, query, mutationResultUUIDText(202)); err != nil || author.Valid {
				t.Fatalf("optional source owner author=%#v err=%v", author, err)
			}
			var users int
			usersTable := strings.Replace(profile.posts, "posts", "users", 1)
			if err := fixture.app.database.GetContext(ctx, &users, `SELECT COUNT(*) FROM `+usersTable+` WHERE "id"=`+profile.placeholder(1), mutationResultUUIDText(1)); err != nil || users != 0 {
				t.Fatalf("optional source target rows=%d err=%v", users, err)
			}
			var factsAfter int
			if err := fixture.app.database.GetContext(ctx, &factsAfter, `SELECT COUNT(*) FROM `+profile.outbox); err != nil || factsAfter != factsBefore+2 {
				t.Fatalf("optional source facts=%d want=%d err=%v", factsAfter, factsBefore+2, err)
			}
		})
	})
	t.Run("required-inverse", func(t *testing.T) {
		runRelationDeleteProviderProfiles(t, "inverse_delete", schematest.NewSubscribedIndexedInverseRequiredHasOne, schematest.NewSubscribedIndexedInverseRequiredHasOnePostgreSQLNamespaces, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
			ctx := context.Background()
			probe := &relationDeleteHookProbe{}
			fixture := mutationResultFixtureWithDeleteProbe(t, profile.fixture, profile.provider, probe)
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(203, golem.UUID{15: 1}, "required-inverse")); err != nil {
				t.Fatal(err)
			}
			var factsBefore int
			if err := fixture.app.database.GetContext(ctx, &factsBefore, `SELECT COUNT(*) FROM `+profile.outbox); err != nil {
				t.Fatal(err)
			}
			caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			user := golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey,
				golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
			input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
				golem.GeneratedNestedDelete[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, nil))
			if _, err := CallerUpdate(ctx, caller, fixture.userDescriptor, user, input); err != nil {
				t.Fatalf("required inverse delete: %v: %v", err, errors.Unwrap(err))
			}
			if probe.postDeleteBefore.Load() != 1 || probe.postDeleteAfter.Load() != 1 || probe.postDeleteCommit.Load() != 1 || probe.postUpdateBefore.Load() != 0 {
				t.Fatalf("required inverse hooks post-delete=%d/%d/%d post-update=%d", probe.postDeleteBefore.Load(), probe.postDeleteAfter.Load(), probe.postDeleteCommit.Load(), probe.postUpdateBefore.Load())
			}
			var posts int
			if err := fixture.app.database.GetContext(ctx, &posts, `SELECT COUNT(*) FROM `+profile.posts+` WHERE "id"=`+profile.placeholder(1), mutationResultUUIDText(203)); err != nil || posts != 0 {
				t.Fatalf("required inverse child rows=%d err=%v", posts, err)
			}
			var factsAfter int
			if err := fixture.app.database.GetContext(ctx, &factsAfter, `SELECT COUNT(*) FROM `+profile.outbox); err != nil || factsAfter != factsBefore+1 {
				t.Fatalf("required inverse facts=%d want=%d err=%v", factsAfter, factsBefore+1, err)
			}
		})
	})
}

func TestOptionalSourceDeleteAuthorizesCapturedRelationBeforeDisconnectAcrossProviders(t *testing.T) {
	runRelationDeleteProviderProfiles(t, "delete_relation_policy", schematest.NewSubscribedIndexedOptionalSource, schematest.NewSubscribedIndexedOptionalSourcePostgreSQLNamespaces, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := mutationResultFixtureWithUserDeleteRelationPolicy(t, profile.fixture, profile.provider, "relation-allow")
		for _, row := range []struct {
			id, author byte
			title      string
		}{{206, 1, "relation-allow"}, {207, 2, "relation-deny"}} {
			if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(row.id, golem.UUID{15: row.author}, row.title)); err != nil {
				t.Fatal(err)
			}
		}
		var factsBefore int
		if err := fixture.app.database.GetContext(ctx, &factsBefore, `SELECT COUNT(*) FROM `+profile.outbox); err != nil {
			t.Fatal(err)
		}
		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		deleteAuthor := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedNestedDelete[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, nil))
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(206), deleteAuthor); err != nil {
			t.Fatalf("relation-authorized optional source delete: %v cause=%v", err, errors.Unwrap(err))
		}
		var allowedAuthor sql.NullString
		query := `SELECT "author_id" FROM ` + profile.posts + ` WHERE "id"=` + profile.placeholder(1)
		if err := fixture.app.database.GetContext(ctx, &allowedAuthor, query, mutationResultUUIDText(206)); err != nil || allowedAuthor.Valid {
			t.Fatalf("relation-authorized owner author=%#v err=%v", allowedAuthor, err)
		}
		usersTable := strings.Replace(profile.posts, "posts", "users", 1)
		var allowedUser int
		if err := fixture.app.database.GetContext(ctx, &allowedUser, `SELECT COUNT(*) FROM `+usersTable+` WHERE "id"=`+profile.placeholder(1), mutationResultUUIDText(1)); err != nil || allowedUser != 0 {
			t.Fatalf("relation-authorized target rows=%d err=%v", allowedUser, err)
		}
		var factsAfterAllow int
		if err := fixture.app.database.GetContext(ctx, &factsAfterAllow, `SELECT COUNT(*) FROM `+profile.outbox); err != nil || factsAfterAllow != factsBefore+2 {
			t.Fatalf("relation-authorized facts=%d want=%d err=%v", factsAfterAllow, factsBefore+2, err)
		}

		_, err = CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(207), deleteAuthor)
		var failure *golem.Error
		if !errors.As(err, &failure) || failure.Code != golem.CodeNotFound {
			t.Fatalf("relation-denied optional source delete failure=%#v err=%v", failure, err)
		}
		var deniedAuthor string
		if err := fixture.app.database.GetContext(ctx, &deniedAuthor, query, mutationResultUUIDText(207)); err != nil || deniedAuthor != mutationResultUUIDText(2) {
			t.Fatalf("relation-denied owner author=%q err=%v", deniedAuthor, err)
		}
		var deniedUser int
		if err := fixture.app.database.GetContext(ctx, &deniedUser, `SELECT COUNT(*) FROM `+usersTable+` WHERE "id"=`+profile.placeholder(1), mutationResultUUIDText(2)); err != nil || deniedUser != 1 {
			t.Fatalf("relation-denied target rows=%d err=%v", deniedUser, err)
		}
		var factsAfterDeny int
		if err := fixture.app.database.GetContext(ctx, &factsAfterDeny, `SELECT COUNT(*) FROM `+profile.outbox); err != nil || factsAfterDeny != factsAfterAllow {
			t.Fatalf("relation-denied facts=%d want unchanged %d err=%v", factsAfterDeny, factsAfterAllow, err)
		}
	})
}

func TestMissingOptionalCurrentToOneReturnsNotFoundAndDisconnectIsNoOpAcrossProviders(t *testing.T) {
	runRelationDeleteProviderProfiles(t, "optional_missing", schematest.NewSubscribedIndexedOptionalSource, schematest.NewSubscribedIndexedOptionalSourcePostgreSQLNamespaces, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		insert := `INSERT INTO ` + profile.posts + `("id","author_id","title") VALUES (` + profile.placeholder(1) + `,NULL,` + profile.placeholder(2) + `)`
		if _, err := fixture.app.database.ExecContext(ctx, insert, mutationResultUUIDText(204), "no-current-author"); err != nil {
			t.Fatal(err)
		}
		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		updateUser := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedSetFieldValue(fixture.schema.User, fixture.userName, "must-not-update"))
		operations := []struct {
			name  string
			value golem.NestedUpdateValue[mutationResultPost]
		}{
			{"update", golem.GeneratedNestedUpdate[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, nil, updateUser)},
			{"delete", golem.GeneratedNestedDelete[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, nil)},
		}
		for _, operation := range operations {
			operation := operation
			for _, stance := range []string{"caller", "system"} {
				stance := stance
				t.Run(operation.name+"-"+stance, func(t *testing.T) {
					input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post, operation.value)
					var err error
					if stance == "caller" {
						_, err = CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(204), input)
					} else {
						_, err = SystemUpdate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.target(204), input)
					}
					var failure *golem.Error
					if !errors.As(err, &failure) || failure.Code != golem.CodeNotFound {
						t.Fatalf("missing current to-one failure=%#v err=%v", failure, err)
					}
				})
			}
		}
		disconnect := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedNestedDisconnectOne[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User))
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(204), disconnect); err != nil {
			t.Fatalf("already-null optional disconnect: %v", err)
		}
		var author sql.NullString
		query := `SELECT "author_id" FROM ` + profile.posts + ` WHERE "id"=` + profile.placeholder(1)
		if err := fixture.app.database.GetContext(ctx, &author, query, mutationResultUUIDText(204)); err != nil || author.Valid {
			t.Fatalf("already-null optional disconnect author=%#v err=%v", author, err)
		}
		insertConnected := `INSERT INTO ` + profile.posts + `("id","author_id","title") VALUES (` + profile.placeholder(1) + `,` + profile.placeholder(2) + `,` + profile.placeholder(3) + `)`
		if _, err := fixture.app.database.ExecContext(ctx, insertConnected, mutationResultUUIDText(205), mutationResultUUIDText(1), "invisible-current-author"); err != nil {
			t.Fatal(err)
		}
		denied := reopenMutationResultWithUserWriteDenials(t, fixture)
		deniedCaller, err := denied.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		for _, operation := range operations {
			operation := operation
			t.Run("invisible-"+operation.name, func(t *testing.T) {
				input := golem.GeneratedUpdateInput[mutationResultPost](denied.schema.Post, operation.value)
				_, err := CallerUpdate(ctx, deniedCaller, denied.postDescriptor, denied.target(205), input)
				var failure *golem.Error
				if !errors.As(err, &failure) || failure.Code != golem.CodeNotFound {
					t.Fatalf("invisible current to-one failure=%#v err=%v", failure, err)
				}
			})
		}
	})
}

func reopenMutationResultWithUserWriteDenials(t testing.TB, fixture mutationResultFixture) mutationResultFixture {
	t.Helper()
	userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](fixture.schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		return rules.Freeze(fixture.schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](fixture.schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.All[mutationResultPost]())
		rules.CanDelete(golem.All[mutationResultPost]())
		return rules.Freeze(fixture.schema.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, nil))
	if err != nil {
		t.Fatal(err)
	}
	provider := golem.SQLite
	if fixture.app.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		DB: fixture.app.database, Provider: provider, Bundle: fixture.schema.Bundle, Bindings: bindings, Descriptors: fixture.app.descriptors,
		ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}

func runRelationDeleteProviderProfiles(t *testing.T, prefix string, sqliteFixture func(testing.TB) schematest.Fixture, postgresFixture func(testing.TB, physical.PhysicalName, physical.PhysicalName) schematest.Fixture, operation func(*testing.T, mutationProviderAcceptanceFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		provider := sqliteprovider.New()
		database, _, err := provider.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), prefix+".db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		schemaFixture := sqliteFixture(t)
		if err := provider.ApplyInitial(context.Background(), database, schemaFixture.SQLite); err != nil {
			t.Fatal(err)
		}
		seedMutationBoundaryUsers(t, database, golem.SQLite, "")
		fixture := mutationResultFixtureForSchema(t, database, golem.SQLite, schemaFixture)
		operation(t, mutationProviderAcceptanceFixture{fixture: fixture, provider: golem.SQLite, posts: `"posts"`, outbox: `"_golem_outbox"`, placeholder: func(int) string { return "?" }})
	})
	for _, value := range []struct{ name, profile, env string }{{"postgresql-c", "c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		value := value
		t.Run(value.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(value.env))
			if dsn == "" {
				t.Skipf("%s is required for direction-specific delete evidence", value.env)
			}
			sequence := mutationOutboxNamespaceSequence.Add(1)
			applicationNamespace := fmt.Sprintf("golem_p4_%s_%s_%d_%d", prefix, value.profile, os.Getpid(), sequence)
			systemNamespace := fmt.Sprintf("golem_p4_%s_system_%s_%d_%d", prefix, value.profile, os.Getpid(), sequence)
			schemaFixture := postgresFixture(t, physical.PhysicalName(applicationNamespace), physical.PhysicalName(systemNamespace))
			provider := postgresprovider.New()
			database, _, err := provider.Open(context.Background(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(applicationNamespace) + ` CASCADE`)
				_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(systemNamespace) + ` CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(context.Background(), database, schemaFixture.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			posts := quoteAcceptanceIdentifier(applicationNamespace) + `."posts"`
			seedMutationBoundaryUsers(t, database, golem.PostgreSQL, applicationNamespace)
			fixture := mutationResultFixtureForSchema(t, database, golem.PostgreSQL, schemaFixture)
			operation(t, mutationProviderAcceptanceFixture{fixture: fixture, provider: golem.PostgreSQL, posts: posts, outbox: quoteAcceptanceIdentifier(systemNamespace) + `."_golem_outbox"`, placeholder: func(index int) string { return fmt.Sprintf("$%d", index) }})
		})
	}
}

func mutationResultFixtureWithUpdateProbe(t *testing.T, fixture mutationResultFixture, provider golem.Provider, before, after, afterCommit *atomic.Int64) mutationResultFixture {
	t.Helper()
	allowUsers := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](fixture.schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		rules.CanDelete(golem.All[mutationResultUser]())
		return rules.Freeze(fixture.schema.User)
	})
	allowPosts := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](fixture.schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.All[mutationResultPost]())
		rules.CanDelete(golem.All[mutationResultPost]())
		return rules.Freeze(fixture.schema.Post)
	})
	hooks := []golem.HookBinding[mutationResultActor]{
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](fixture.schema.Post, golem.HookUpdate, func(context.Context, *golem.UpdateHookRequest[mutationResultPost]) error { before.Add(1); return nil }),
		golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookResult[mutationResultPost]](fixture.schema.Post, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[mutationResultPost]) error { after.Add(1); return nil }),
		golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookResult[mutationResultPost]](fixture.schema.Post, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[mutationResultPost]) error {
			afterCommit.Add(1)
			return nil
		}),
	}
	packages := golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{allowUsers, allowPosts}, hooks)
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), packages)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		DB: fixture.app.database, Provider: provider, Bundle: fixture.schema.Bundle, Bindings: bindings, Descriptors: fixture.app.descriptors,
		MutationLimits: MutationLimits{MaxTouchedRows: 1}, ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}

func mutationResultFixtureWithDeleteProbe(t *testing.T, fixture mutationResultFixture, provider golem.Provider, probe *relationDeleteHookProbe) mutationResultFixture {
	t.Helper()
	allowUsers := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](fixture.schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		rules.CanDelete(golem.All[mutationResultUser]())
		return rules.Freeze(fixture.schema.User)
	})
	allowPosts := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](fixture.schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.All[mutationResultPost]())
		rules.CanDelete(golem.All[mutationResultPost]())
		return rules.Freeze(fixture.schema.Post)
	})
	var hooks []golem.HookBinding[mutationResultActor]
	endpoint, _ := fixture.schema.Registry.RelationEndpoint(fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship)
	if endpoint.Kind() == compilerir.RelationHasOne {
		hooks = []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.DeleteHookRequest[mutationResultPost]](fixture.schema.Post, golem.HookDelete, func(context.Context, *golem.DeleteHookRequest[mutationResultPost]) error {
				probe.postDeleteBefore.Add(1)
				return nil
			}),
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.DeleteHookResult[mutationResultPost]](fixture.schema.Post, golem.HookDelete, func(context.Context, golem.DeleteHookResult[mutationResultPost]) error {
				probe.postDeleteAfter.Add(1)
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.DeleteHookResult[mutationResultPost]](fixture.schema.Post, golem.HookDelete, func(context.Context, golem.DeleteHookResult[mutationResultPost]) error {
				probe.postDeleteCommit.Add(1)
				return nil
			}),
		}
	} else {
		hooks = []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](fixture.schema.Post, golem.HookUpdate, func(context.Context, *golem.UpdateHookRequest[mutationResultPost]) error {
				probe.postUpdateBefore.Add(1)
				return nil
			}),
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookResult[mutationResultPost]](fixture.schema.Post, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[mutationResultPost]) error {
				probe.postUpdateAfter.Add(1)
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookResult[mutationResultPost]](fixture.schema.Post, golem.HookUpdate, func(context.Context, golem.UpdateHookResult[mutationResultPost]) error {
				probe.postUpdateCommit.Add(1)
				return nil
			}),
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultUser, golem.DeleteHookRequest[mutationResultUser]](fixture.schema.User, golem.HookDelete, func(context.Context, *golem.DeleteHookRequest[mutationResultUser]) error {
				probe.userDeleteBefore.Add(1)
				return nil
			}),
			golem.GeneratedAfterHookBinding[mutationResultActor, mutationResultUser, golem.DeleteHookResult[mutationResultUser]](fixture.schema.User, golem.HookDelete, func(context.Context, golem.DeleteHookResult[mutationResultUser]) error {
				probe.userDeleteAfter.Add(1)
				return nil
			}),
			golem.GeneratedAfterCommitHookBinding[mutationResultActor, mutationResultUser, golem.DeleteHookResult[mutationResultUser]](fixture.schema.User, golem.HookDelete, func(context.Context, golem.DeleteHookResult[mutationResultUser]) error {
				probe.userDeleteCommit.Add(1)
				return nil
			}),
		}
	}
	packages := golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{allowUsers, allowPosts}, hooks)
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), packages)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		DB: fixture.app.database, Provider: provider, Bundle: fixture.schema.Bundle, Bindings: bindings, Descriptors: fixture.app.descriptors,
		ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
		AfterCommitError: func(context.Context, golem.AfterCommitFailure) {},
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}

func mutationResultFixtureWithUserDeleteRelationPolicy(t *testing.T, fixture mutationResultFixture, provider golem.Provider, allowedTitle string) mutationResultFixture {
	t.Helper()
	posts := golem.GeneratedToMany[mutationResultUser, mutationResultPost](fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post)
	usersPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](fixture.schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		rules.CanDelete(posts.Some(fixture.title.Eq(allowedTitle)))
		return rules.Freeze(fixture.schema.User)
	})
	postsPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](fixture.schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.All[mutationResultPost]())
		rules.CanDelete(golem.All[mutationResultPost]())
		return rules.Freeze(fixture.schema.Post)
	})
	hooks := []golem.HookBinding[mutationResultActor]{
		golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultUser, golem.DeleteHookRequest[mutationResultUser]](fixture.schema.User, golem.HookDelete, func(context.Context, *golem.DeleteHookRequest[mutationResultUser]) error { return nil }),
	}
	packages := golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{usersPolicy, postsPolicy}, hooks)
	bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), packages)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		DB: fixture.app.database, Provider: provider, Bundle: fixture.schema.Bundle, Bindings: bindings, Descriptors: fixture.app.descriptors,
		ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
	}))
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	return fixture
}

func mutationResultFixtureForSchema(t *testing.T, database *sqlx.DB, provider golem.Provider, schemaFixture schematest.Fixture) mutationResultFixture {
	t.Helper()
	base := newMutationResultFixture(t)
	userIdentity := golem.GeneratedIdentityMetadata(schemaFixture.User, schemaFixture.UserKey, golem.PrimaryIdentity, schemaFixture.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schemaFixture.Post, schemaFixture.PostKey, golem.PrimaryIdentity, schemaFixture.PostID)
	userCardinality := golem.RelationToOne
	if endpoint, ok := schemaFixture.Registry.RelationEndpoint(schemaFixture.User, schemaFixture.UserPosts, schemaFixture.Authorship); ok && endpoint.Kind() == compilerir.RelationHasMany {
		userCardinality = golem.RelationToMany
	}
	userRelation := golem.GeneratedRelationMetadata(schemaFixture.User, schemaFixture.Post, schemaFixture.UserPosts, schemaFixture.Authorship, golem.RelationInverse, userCardinality)
	postRelation := golem.GeneratedRelationMetadata(schemaFixture.Post, schemaFixture.User, schemaFixture.PostAuthor, schemaFixture.Authorship, golem.RelationSource, golem.RelationToOne)
	userDescriptor := golem.GeneratedModelDescriptor[mutationResultUser](schemaFixture.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.UserID, schemaFixture.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation}))
	postDescriptor := golem.GeneratedModelDescriptor[mutationResultPost](schemaFixture.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schemaFixture.PostID, schemaFixture.AuthorID, schemaFixture.PostTitle}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation}))
	packages := golem.GeneratedStampedPackageDescriptors(schemaFixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(schemaFixture.Bundle.GenerationDigest(), packages)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		DB: database, Provider: provider, Bundle: schemaFixture.Bundle, Bindings: base.app.bindings, Descriptors: descriptors,
		ResolvePrincipal: base.app.resolvePrincipal, SnapshotActor: base.app.snapshotActor,
	}))
	if err != nil {
		t.Fatal(err)
	}
	base.app, base.schema, base.userDescriptor, base.postDescriptor = app, schemaFixture, userDescriptor, postDescriptor
	return base
}

func seedMutationBoundaryUsers(t *testing.T, database *sqlx.DB, provider golem.Provider, namespace string) {
	t.Helper()
	table, first, second := `"users"`, "?", "?"
	if provider == golem.PostgreSQL {
		table, first, second = quoteAcceptanceIdentifier(namespace)+`."users"`, "$1", "$2"
	}
	for _, user := range [][2]string{{mutationResultUUIDText(1), "alice"}, {mutationResultUUIDText(2), "bob"}} {
		if _, err := database.Exec(`INSERT INTO `+table+`("id","name") VALUES (`+first+`,`+second+`)`, user[0], user[1]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNestedUpsertPreflightsBothBranchesBeforeBeginOrSQL(t *testing.T) {
	database, counts := openMutationBoundarySQLite(t)
	t.Cleanup(func() { _ = database.Close() })
	schemaFixture := schematest.NewSubscribedGraph(t)
	if err := sqliteprovider.New().ApplyInitial(context.Background(), database, schemaFixture.SQLite); err != nil {
		t.Fatal(err)
	}
	fixture := openGraphMutationFixtureWithHooks(t, database, golem.SQLite, schemaFixture, golem.ModelID{}, nil)
	user := func(id byte, name string) golem.CreateInput[graphMutationUser] {
		return golem.GeneratedCreateInput(schemaFixture.User,
			golem.GeneratedCreateFieldValue(schemaFixture.User, fixture.userID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(schemaFixture.User, fixture.userName, name),
		)
	}
	target := func(id byte) golem.UniqueSelectorValue[graphMutationUser] {
		return golem.GeneratedUniqueSelectorValue[graphMutationUser](schemaFixture.User, schemaFixture.UserKey, golem.GeneratedSelectorComponent(schemaFixture.UserID, golem.UUID{15: id}))
	}
	if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.userDescriptor, user(230, "existing")); err != nil {
		t.Fatal(err)
	}
	caller, err := fixture.app.ForPrincipal(context.Background(), graphMutationPrincipal{})
	if err != nil {
		t.Fatal(err)
	}
	plainUpdate := golem.GeneratedUpdateInput(schemaFixture.User, golem.GeneratedSetFieldValue(schemaFixture.User, fixture.userName, "must-not-run"))
	invalidPost := func(id byte) golem.CreateInput[graphMutationPost] {
		return golem.GeneratedCreateInput(schemaFixture.Post, golem.GeneratedCreateFieldValue(schemaFixture.Post, fixture.postID, golem.UUID{15: id}))
	}
	invalidCreate := golem.GeneratedCreateInput(schemaFixture.User,
		golem.GeneratedCreateFieldValue(schemaFixture.User, fixture.userID, golem.UUID{15: 230}),
		golem.GeneratedCreateFieldValue(schemaFixture.User, fixture.userName, "invalid-unselected-create"),
		golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](schemaFixture.User, schemaFixture.UserPosts, schemaFixture.Authorship, schemaFixture.Post, invalidPost(231)),
	)
	counts.reset()
	if _, err := CallerUpsert(context.Background(), caller, fixture.userDescriptor, target(230), invalidCreate, plainUpdate); err == nil {
		t.Fatal("invalid unselected create branch was not preflighted")
	}
	if counts.begins.Load() != 0 || counts.queries.Load() != 0 || counts.execs.Load() != 0 {
		t.Fatalf("denied create branch crossed boundary: begins=%d queries=%d execs=%d", counts.begins.Load(), counts.queries.Load(), counts.execs.Load())
	}
	nestedUpdate := golem.GeneratedUpdateInput(schemaFixture.User,
		golem.GeneratedSetFieldValue(schemaFixture.User, fixture.userName, "must-not-run"),
		golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](schemaFixture.User, schemaFixture.UserPosts, schemaFixture.Authorship, schemaFixture.Post, invalidPost(234)),
	)
	counts.reset()
	if _, err := CallerUpsert(context.Background(), caller, fixture.userDescriptor, target(233), user(233, "valid-selected-create"), nestedUpdate); err == nil {
		t.Fatal("denied unselected update branch was not preflighted")
	}
	if counts.begins.Load() != 0 || counts.queries.Load() != 0 || counts.execs.Load() != 0 {
		t.Fatalf("denied update branch crossed boundary: begins=%d queries=%d execs=%d", counts.begins.Load(), counts.queries.Load(), counts.execs.Load())
	}

	validPost := golem.GeneratedCreateInput(schemaFixture.Post,
		golem.GeneratedCreateFieldValue(schemaFixture.Post, fixture.postID, golem.UUID{15: 235}),
		golem.GeneratedCreateFieldValue(schemaFixture.Post, fixture.postTitle, "forged-shape"),
	)
	// These low-level generated values deliberately pair User with the Post's
	// comments field/relation. The public Go types alone cannot authenticate
	// generated identity constants, so the runtime compiler must reject the
	// forged relation shape before probing the selector or opening a transaction.
	forgedRelation := golem.GeneratedNestedCreate[graphMutationUser, graphMutationPost](
		schemaFixture.User, schemaFixture.PostComments, schemaFixture.Commenting, schemaFixture.Post, validPost,
	)
	forgedCreate := golem.GeneratedCreateInput(schemaFixture.User,
		golem.GeneratedCreateFieldValue(schemaFixture.User, fixture.userID, golem.UUID{15: 230}),
		golem.GeneratedCreateFieldValue(schemaFixture.User, fixture.userName, "forged-unselected-create"),
		forgedRelation,
	)
	counts.reset()
	if _, err := CallerUpsert(context.Background(), caller, fixture.userDescriptor, target(230), forgedCreate, plainUpdate); err == nil {
		t.Fatal("forged unselected create branch was not preflighted")
	}
	if counts.begins.Load() != 0 || counts.queries.Load() != 0 || counts.execs.Load() != 0 {
		t.Fatalf("forged create branch crossed boundary: begins=%d queries=%d execs=%d", counts.begins.Load(), counts.queries.Load(), counts.execs.Load())
	}
	forgedUpdate := golem.GeneratedUpdateInput(schemaFixture.User,
		golem.GeneratedSetFieldValue(schemaFixture.User, fixture.userName, "forged-unselected-update"),
		forgedRelation,
	)
	counts.reset()
	if _, err := CallerUpsert(context.Background(), caller, fixture.userDescriptor, target(236), user(236, "valid-selected-create"), forgedUpdate); err == nil {
		t.Fatal("forged unselected update branch was not preflighted")
	}
	if counts.begins.Load() != 0 || counts.queries.Load() != 0 || counts.execs.Load() != 0 {
		t.Fatalf("forged update branch crossed boundary: begins=%d queries=%d execs=%d", counts.begins.Load(), counts.queries.Load(), counts.execs.Load())
	}
}

func TestCreateOracleAuthorizesPersistedAfterImageAndDefaults(t *testing.T) {
	ctx := context.Background()
	fixture, database, proof := scalarMutationSQLite(t, `CREATE TABLE "posts" ("id" TEXT PRIMARY KEY, "author_id" TEXT NOT NULL, "title" TEXT NOT NULL DEFAULT 'database-default', "big_int" INTEGER NOT NULL DEFAULT 0, "decimal_value" INTEGER)`)
	allowed := createDefaultAwarePlan(t, fixture, 131, "database-default", "")
	if _, err := executeScalarMutationProgram(ctx, database, policyir.ProviderSQLite, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), scalarMutationRender(t, fixture, proof, allowed)); err != nil {
		t.Fatalf("persisted database default did not satisfy create oracle: %v", err)
	}
	var title string
	if err := database.GetContext(ctx, &title, `SELECT "title" FROM "posts" WHERE "id"=?`, mutationResultUUIDText(131)); err != nil || title != "database-default" {
		t.Fatalf("persisted title=%q err=%v", title, err)
	}

	denied := createDefaultAwarePlan(t, fixture, 132, "not-the-default", "")
	if _, err := executeScalarMutationProgram(ctx, database, policyir.ProviderSQLite, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), scalarMutationRender(t, fixture, proof, denied)); err == nil {
		t.Fatal("create whose persisted default violates the row oracle committed")
	}
	var deniedRows int
	if err := database.GetContext(ctx, &deniedRows, `SELECT COUNT(*) FROM "posts" WHERE "id"=?`, mutationResultUUIDText(132)); err != nil || deniedRows != 0 {
		t.Fatalf("denied create rows=%d err=%v", deniedRows, err)
	}
	runCreateDefaultPostgreSQLProfiles(t, false)
}

func TestCreateFieldPolicyUsesAuthoredFieldsAndPersistedDependencies(t *testing.T) {
	ctx := context.Background()
	fixture, database, proof := scalarMutationSQLite(t, `CREATE TABLE "posts" ("id" TEXT PRIMARY KEY, "author_id" TEXT NOT NULL, "title" TEXT NOT NULL DEFAULT 'database-default', "big_int" INTEGER NOT NULL DEFAULT 0, "decimal_value" INTEGER)`)
	allowed := createDefaultAwarePlan(t, fixture, 133, "database-default", "database-default")
	if _, err := executeScalarMutationProgram(ctx, database, policyir.ProviderSQLite, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), scalarMutationRender(t, fixture, proof, allowed)); err != nil {
		t.Fatalf("authored-field condition could not use persisted default dependency: %v", err)
	}
	denied := createDefaultAwarePlan(t, fixture, 134, "database-default", "not-the-default")
	if _, err := executeScalarMutationProgram(ctx, database, policyir.ProviderSQLite, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), scalarMutationRender(t, fixture, proof, denied)); err == nil {
		t.Fatal("authored-field condition ignored its persisted dependency")
	}
	var deniedRows int
	if err := database.GetContext(ctx, &deniedRows, `SELECT COUNT(*) FROM "posts" WHERE "id"=?`, mutationResultUUIDText(134)); err != nil || deniedRows != 0 {
		t.Fatalf("field-denied create rows=%d err=%v", deniedRows, err)
	}
	runCreateDefaultPostgreSQLProfiles(t, true)
}

func TestMutationExactValuesAgreeAcrossProviders(t *testing.T) {
	type exactResult struct {
		values map[golem.FieldID]policyir.Value
		null   bool
	}
	ctx := context.Background()
	wantBigInt, err := policyir.SignedValue(policyir.ValueInt64, int64(9_007_199_254_740_993))
	if err != nil {
		t.Fatal(err)
	}
	wantDecimal, err := policyir.NewDecimalValue(999_999_999_999_999_999, 13)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := policyir.BytesValue([]byte{0, 1, 2, 127, 128, 255})
	exactNumber, err := policyir.NewJSONNumber(false, []byte("9007199254740993"), 0)
	if err != nil {
		t.Fatal(err)
	}
	numberJSON, err := policyir.JSONNumberValueOf(exactNumber)
	if err != nil {
		t.Fatal(err)
	}
	numberMember, _ := policyir.NewJSONMember("exact", numberJSON)
	textJSON, _ := policyir.JSONStringValue("é🚀json")
	textMember, _ := policyir.NewJSONMember("text", textJSON)
	objectJSON, err := policyir.JSONObjectValue([]policyir.JSONMember{textMember, numberMember})
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := policyir.NewJSONValue(objectJSON)
	if err != nil {
		t.Fatal(err)
	}
	listFirst, _ := policyir.StringValue("alpha")
	listSecond, _ := policyir.StringValue("é")
	wantList, err := policyir.NewListValue([]policyir.Value{listFirst, listSecond})
	if err != nil {
		t.Fatal(err)
	}
	wantDateTime, err := policyir.NewDateTimeValue(1_775_555_555, 123_456_000)
	if err != nil {
		t.Fatal(err)
	}

	execute := func(t *testing.T, fixture schematest.Fixture, database *sqlx.DB, provider policyir.Provider, proof policysql.CapabilityProof, posts string, placeholder func(int) string) exactResult {
		t.Helper()
		users := strings.TrimSuffix(posts, `"posts"`) + `"users"`
		if _, err := database.ExecContext(ctx, `INSERT INTO `+users+` ("id","name") VALUES (`+placeholder(1)+`,`+placeholder(2)+`)`, mutationResultUUIDText(201), "exact-author"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(ctx, `INSERT INTO `+posts+` ("id","author_id","title","big_int","decimal_value","nullable_text") VALUES (`+placeholder(1)+`,`+placeholder(2)+`,`+placeholder(3)+`,`+placeholder(4)+`,`+placeholder(5)+`,`+placeholder(6)+`)`, mutationResultUUIDText(202), mutationResultUUIDText(201), "exact", int64(0), int64(0), "before-null"); err != nil {
			t.Fatal(err)
		}

		apply := func(field golem.FieldID, value policyir.Value) policyir.Value {
			t.Helper()
			plan := exactMutationUpdatePlan(t, fixture, field, value)
			program, err := mutationsql.Render(plan, fixture.Registry, provider, proof)
			if err != nil {
				t.Fatal(err)
			}
			execution, err := executeScalarMutationProgram(ctx, database, provider, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), program)
			if err != nil {
				chain := make([]string, 0, 4)
				for cause := err; cause != nil; cause = errors.Unwrap(cause) {
					chain = append(chain, cause.Error())
				}
				t.Fatalf("exact mutation field=%x failed: %q", field, chain)
			}
			for statementIndex := len(execution.statements) - 1; statementIndex >= 0; statementIndex-- {
				cells := execution.statements[statementIndex].cells
				for cellIndex := len(cells) - 1; cellIndex >= 0; cellIndex-- {
					if cells[cellIndex].FieldID() != policyir.FieldID(field) {
						continue
					}
					decoded, ok := cells[cellIndex].PolicyValue()
					if !ok {
						t.Fatalf("field %x did not exact-decode", field)
					}
					return decoded
				}
			}
			t.Fatalf("field %x is absent from mutation result", field)
			return policyir.Value{}
		}
		applyNull := func(field golem.FieldID) bool {
			t.Helper()
			plan := exactMutationNullUpdatePlan(t, fixture, field)
			program, err := mutationsql.Render(plan, fixture.Registry, provider, proof)
			if err != nil {
				t.Fatal(err)
			}
			execution, err := executeScalarMutationProgram(ctx, database, provider, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), program)
			if err != nil {
				t.Fatal(err)
			}
			for statementIndex := len(execution.statements) - 1; statementIndex >= 0; statementIndex-- {
				for _, cell := range execution.statements[statementIndex].cells {
					if cell.FieldID() == policyir.FieldID(field) {
						return cell.IsNull()
					}
				}
			}
			t.Fatalf("nullable field %x is absent from mutation result", field)
			return false
		}

		result := exactResult{
			values: map[golem.FieldID]policyir.Value{
				fixture.PostBigInt:   apply(fixture.PostBigInt, wantBigInt),
				fixture.PostDecimal:  apply(fixture.PostDecimal, wantDecimal),
				fixture.PostBytes:    apply(fixture.PostBytes, wantBytes),
				fixture.PostJSON:     apply(fixture.PostJSON, wantJSON),
				fixture.PostList:     apply(fixture.PostList, wantList),
				fixture.PostDateTime: apply(fixture.PostDateTime, wantDateTime),
			},
			null: applyNull(fixture.PostNullableText),
		}
		for field, want := range map[golem.FieldID]policyir.Value{
			fixture.PostBigInt: wantBigInt, fixture.PostDecimal: wantDecimal, fixture.PostBytes: wantBytes,
			fixture.PostJSON: wantJSON, fixture.PostList: wantList, fixture.PostDateTime: wantDateTime,
		} {
			if !mutationdecode.EqualValue(result.values[field], want) {
				t.Fatalf("decoded mutation field %x differs: got=%#v want=%#v", field, result.values[field], want)
			}
		}
		if !result.null {
			t.Fatal("nullable mutation value did not persist/decode as NULL")
		}
		return result
	}

	var reference exactResult
	t.Run("sqlite", func(t *testing.T) {
		fixture := schematest.NewMutationExactValues(t)
		provider := sqliteprovider.New()
		database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "exact-mutation-values.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		if err := provider.ApplyInitial(ctx, database, fixture.SQLite); err != nil {
			t.Fatal(err)
		}
		proof, err := provider.PolicyCapabilityProof(ctx, database, [32]byte(fixture.Registry.ModelFingerprint()))
		if err != nil {
			t.Fatal(err)
		}
		reference = execute(t, fixture, database, policyir.ProviderSQLite, proof, `"posts"`, func(int) string { return "?" })
		var stored struct {
			BigInt  int64 `db:"big_int"`
			Decimal int64 `db:"decimal_value"`
		}
		if err := database.GetContext(ctx, &stored, `SELECT "big_int","decimal_value" FROM "posts" WHERE "id"=?`, mutationResultUUIDText(202)); err != nil || stored.BigInt != 9_007_199_254_740_993 || stored.Decimal != 999_999_999_999_999_999 {
			t.Fatalf("SQLite physical exact values=%+v err=%v", stored, err)
		}
	})

	for _, profile := range []struct{ name, env string }{{"postgresql-c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for exact mutation parity evidence", profile.env)
			}
			fixture := schematest.NewMutationExactValues(t)
			sequence := mutationOutboxNamespaceSequence.Add(1)
			applicationNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_exact_%d", sequence))
			systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_exact_system_%d", sequence))
			schema := fixture.PostgreSQL
			schema.Namespace.Name, schema.System.Namespace.Name = applicationNamespace, systemNamespace
			bundle := postgresRuntimeBundle(t, fixture, schema)
			registry, err := policyschema.New(bundle)
			if err != nil {
				t.Fatal(err)
			}
			fixture.Bundle, fixture.Registry, fixture.PostgreSQL = bundle, registry, schema
			provider := postgresprovider.New()
			database, _, err := provider.Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(string(applicationNamespace)) + ` CASCADE`)
				_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(string(systemNamespace)) + ` CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(ctx, database, schema); err != nil {
				t.Fatal(err)
			}
			proof, err := provider.PolicyCapabilityProof(ctx, database, [32]byte(registry.ModelFingerprint()))
			if err != nil {
				t.Fatal(err)
			}
			prefix := quoteAcceptanceIdentifier(string(applicationNamespace)) + `.`
			got := execute(t, fixture, database, policyir.ProviderPostgreSQL, proof, prefix+`"posts"`, func(index int) string { return fmt.Sprintf("$%d", index) })
			for field, referenceValue := range reference.values {
				if !mutationdecode.EqualValue(got.values[field], referenceValue) {
					t.Fatalf("provider mutation field %x differs from SQLite: got=%#v reference=%#v", field, got.values[field], referenceValue)
				}
			}
			if !got.null || !reference.null {
				t.Fatalf("provider NULL parity got=%t reference=%t", got.null, reference.null)
			}
			var stored struct {
				BigInt  int64  `db:"big_int"`
				Decimal string `db:"decimal_value"`
			}
			if err := database.GetContext(ctx, &stored, `SELECT "big_int","decimal_value"::text AS "decimal_value" FROM `+prefix+`"posts" WHERE "id"=$1`, mutationResultUUIDText(202)); err != nil || stored.BigInt != 9_007_199_254_740_993 || stored.Decimal != "99999.9999999999999" {
				t.Fatalf("PostgreSQL physical exact values=%+v err=%v", stored, err)
			}
		})
	}
}

func exactMutationUpdatePlan(t *testing.T, fixture schematest.Fixture, field golem.FieldID, value policyir.Value) mutationir.Plan {
	t.Helper()
	operation, err := mutationir.NewSet(policyir.FieldID(field), scalarMutationType(t, fixture, field), value)
	if err != nil {
		t.Fatal(err)
	}
	return exactMutationOperationPlan(t, fixture, field, operation)
}

func exactMutationNullUpdatePlan(t *testing.T, fixture schematest.Fixture, field golem.FieldID) mutationir.Plan {
	t.Helper()
	operation, err := mutationir.NewNull(policyir.FieldID(field), scalarMutationType(t, fixture, field))
	if err != nil {
		t.Fatal(err)
	}
	return exactMutationOperationPlan(t, fixture, field, operation)
}

func exactMutationOperationPlan(t *testing.T, fixture schematest.Fixture, field golem.FieldID, operation mutationir.ScalarOperation) mutationir.Plan {
	t.Helper()
	model := policyir.ModelID(fixture.Post)
	target := scalarMutationTarget(t, fixture, scalarMutationUUID(202))
	truth, err := policyir.NewConstant(model, true)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := mutationir.NewSelectionRequirement(policyir.ActionUpdate, truth)
	if err != nil {
		t.Fatal(err)
	}
	images, err := mutationir.NewImageRequirements(model, []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(field)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	authorization, err := mutationir.NewFieldAuthorization(policyir.FieldID(field), truth)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := mutationir.NewGraph(mutationir.NodeInput{
		Operation: mutationir.Update, Model: model, Target: &target,
		ScalarOperations: []mutationir.ScalarOperation{operation}, Before: images, After: images,
		Selection: &selection, RowPostcondition: &truth, FieldConditions: []mutationir.FieldAuthorization{authorization},
		Identity: mutationir.IdentityUnchanged,
	})
	if err != nil {
		t.Fatal(err)
	}
	return scalarMutationPlan(t, graph, images)
}

func runCreateDefaultPostgreSQLProfiles(t *testing.T, fieldPolicy bool) {
	t.Helper()
	profiles := []struct {
		name string
		env  string
	}{{name: "postgresql-c", env: "GOLEM_TEST_POSTGRES_DSN"}, {name: "postgresql-linguistic", env: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}}
	for _, profile := range profiles {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for persisted-default create evidence", profile.env)
			}
			sequence := mutationOutboxNamespaceSequence.Add(1)
			applicationNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_create_%d", sequence))
			systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_create_system_%d", sequence))
			fixture := schematest.NewSubscribedIndexedPostgreSQLNamespaces(t, applicationNamespace, systemNamespace)
			provider := postgresprovider.New()
			database, _, err := provider.Open(context.Background(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(string(applicationNamespace)) + ` CASCADE`)
				_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(string(systemNamespace)) + ` CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(context.Background(), database, fixture.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			posts := quoteAcceptanceIdentifier(string(applicationNamespace)) + `."posts"`
			if _, err := database.Exec(`ALTER TABLE ` + posts + ` ALTER COLUMN "title" SET DEFAULT 'database-default'`); err != nil {
				t.Fatal(err)
			}
			proof, err := provider.PolicyCapabilityProof(context.Background(), database, [32]byte(fixture.Registry.ModelFingerprint()))
			if err != nil {
				t.Fatal(err)
			}
			fieldExpected := ""
			if fieldPolicy {
				fieldExpected = "database-default"
			}
			allowed := createDefaultAwarePlan(t, fixture, 161, "database-default", fieldExpected)
			program, err := mutationsql.Render(allowed, fixture.Registry, policyir.ProviderPostgreSQL, proof)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := executeScalarMutationProgram(context.Background(), database, policyir.ProviderPostgreSQL, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), program); err != nil {
				t.Fatalf("PostgreSQL persisted-default create: %v", err)
			}
			var title string
			if err := database.Get(&title, `SELECT "title" FROM `+posts+` WHERE "id"=$1`, mutationResultUUIDText(161)); err != nil || title != "database-default" {
				t.Fatalf("PostgreSQL persisted title=%q err=%v", title, err)
			}
			deniedField := ""
			if fieldPolicy {
				deniedField = "not-the-default"
			}
			deniedRow := "not-the-default"
			if fieldPolicy {
				deniedRow = "database-default"
			}
			denied := createDefaultAwarePlan(t, fixture, 162, deniedRow, deniedField)
			program, err = mutationsql.Render(denied, fixture.Registry, policyir.ProviderPostgreSQL, proof)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := executeScalarMutationProgram(context.Background(), database, policyir.ProviderPostgreSQL, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), program); err == nil {
				t.Fatal("PostgreSQL denied persisted-default create committed")
			}
			var count int
			if err := database.Get(&count, `SELECT COUNT(*) FROM `+posts+` WHERE "id"=$1`, mutationResultUUIDText(162)); err != nil || count != 0 {
				t.Fatalf("PostgreSQL denied create rows=%d err=%v", count, err)
			}
		})
	}
}

func createDefaultAwarePlan(t *testing.T, fixture schematest.Fixture, id byte, rowExpected, fieldExpected string) mutationir.Plan {
	t.Helper()
	model := policyir.ModelID(fixture.Post)
	idOperation, err := mutationir.NewSet(policyir.FieldID(fixture.PostID), scalarMutationType(t, fixture, fixture.PostID), scalarMutationUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	authorOperation, err := mutationir.NewSet(policyir.FieldID(fixture.AuthorID), scalarMutationType(t, fixture, fixture.AuthorID), scalarMutationUUID(1))
	if err != nil {
		t.Fatal(err)
	}
	after, err := mutationir.NewImageRequirements(model, []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.AuthorID), policyir.FieldID(fixture.PostTitle)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rowCondition := scalarMutationTextEqual(t, fixture, rowExpected)
	var authorizations []mutationir.FieldAuthorization
	if fieldExpected != "" {
		fieldCondition := scalarMutationTextEqual(t, fixture, fieldExpected)
		for _, field := range []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.AuthorID)} {
			authorization, authErr := mutationir.NewFieldAuthorization(field, fieldCondition)
			if authErr != nil {
				t.Fatal(authErr)
			}
			authorizations = append(authorizations, authorization)
		}
	}
	graph, err := mutationir.NewGraph(mutationir.NodeInput{
		Operation: mutationir.Create, Model: model,
		ScalarOperations: []mutationir.ScalarOperation{idOperation, authorOperation},
		After:            after, RowPostcondition: &rowCondition, FieldConditions: authorizations,
		Identity: mutationir.IdentityProduced,
	})
	if err != nil {
		t.Fatal(err)
	}
	return scalarMutationPlan(t, graph, after)
}

func scalarMutationTextEqual(t *testing.T, fixture schematest.Fixture, expected string) policyir.Condition {
	t.Helper()
	typ := scalarMutationType(t, fixture, fixture.PostTitle)
	operand, err := policyir.OneOperand(scalarMutationString(expected))
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := policyoperator.ValidateShape(policyir.OperatorEqual, policyoperator.Shape{Node: policyir.ConditionScalar, FieldType: typ, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: policyir.PortableProviders()})
	if err != nil {
		t.Fatal(err)
	}
	condition, err := policyir.NewScalar(policyir.ModelID(fixture.Post), policyir.FieldID(fixture.PostTitle), typ, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
	if err != nil {
		t.Fatal(err)
	}
	return condition
}

func TestSystemOutboxV1MigratesIntrospectsAndFingerprintsBothProviders(t *testing.T) {
	ctx := context.Background()
	t.Run("sqlite", func(t *testing.T) {
		fixture := schematest.NewSubscribedIndexed(t)
		assertOutboxV1Inventory(t, fixture.SQLite)
		provider := sqliteprovider.New()
		database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "outbox-introspection.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		if err := provider.ApplyInitial(ctx, database, fixture.SQLite); err != nil {
			t.Fatal(err)
		}
		actual, err := provider.Introspect(ctx, database, fixture.SQLite)
		if err != nil {
			t.Fatal(err)
		}
		assertSystemFingerprintEqual(t, fixture.SQLite, actual)
	})

	profiles := []struct {
		name string
		env  string
	}{
		{name: "postgresql-c", env: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "postgresql-linguistic", env: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	}
	for _, profile := range profiles {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for live outbox migration/introspection evidence", profile.env)
			}
			sequence := mutationOutboxNamespaceSequence.Add(1)
			applicationNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_outbox_%d", sequence))
			systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_outbox_system_%d", sequence))
			fixture := schematest.NewSubscribedIndexedPostgreSQLNamespaces(t, applicationNamespace, systemNamespace)
			assertOutboxV1Inventory(t, fixture.PostgreSQL)
			provider := postgresprovider.New()
			database, _, err := provider.Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(string(applicationNamespace)) + ` CASCADE`)
				_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(string(systemNamespace)) + ` CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(ctx, database, fixture.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			actual, err := provider.Introspect(ctx, database, fixture.PostgreSQL)
			if err != nil {
				t.Fatal(err)
			}
			assertSystemFingerprintEqual(t, fixture.PostgreSQL, actual)
		})
	}
}

func TestUpsertSameSelectorMultiConnectionAndProcess(t *testing.T) {
	if os.Getenv("GOLEM_P4_UPSERT_HELPER") == "1" {
		runUpsertProcessHelper(t)
		return
	}

	root := t.TempDir()
	dsn := "file:" + filepath.Join(root, "process-upsert.db")
	provider := sqliteprovider.New()
	database, _, err := provider.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newMutationResultFixtureWithHooksAndDatabaseMode(t, MutationLimits{}, nil, nil, database, true)
	startPath := filepath.Join(root, "start")
	commands := make([]*exec.Cmd, 2)
	outputs := make([]bytes.Buffer, len(commands))
	for worker := range commands {
		readyPath := filepath.Join(root, fmt.Sprintf("ready-%d", worker))
		command := exec.Command(os.Args[0], "-test.run=^TestUpsertSameSelectorMultiConnectionAndProcess$", "-test.v")
		command.Env = append(os.Environ(),
			"GOLEM_P4_UPSERT_HELPER=1",
			"GOLEM_P4_UPSERT_DSN="+dsn,
			"GOLEM_P4_UPSERT_READY="+readyPath,
			"GOLEM_P4_UPSERT_START="+startPath,
		)
		command.Stdout, command.Stderr = &outputs[worker], &outputs[worker]
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands[worker] = command
	}

	deadline := time.Now().Add(10 * time.Second)
	for worker := range commands {
		readyPath := filepath.Join(root, fmt.Sprintf("ready-%d", worker))
		for {
			if _, err := os.Stat(readyPath); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("helper %d did not reach synchronized start; output:\n%s", worker, outputs[worker].String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if err := os.WriteFile(startPath, []byte("start\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for worker, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("helper %d failed: %v\n%s", worker, err, outputs[worker].String())
		}
	}

	var count int
	if err := fixture.app.database.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM "posts" WHERE "id"=?`, mutationResultUUIDText(117)); err != nil || count != 1 {
		t.Fatalf("same-selector process race rows=%d want=1 err=%v", count, err)
	}
	var title string
	if err := fixture.app.database.GetContext(context.Background(), &title, `SELECT "title" FROM "posts" WHERE "id"=?`, mutationResultUUIDText(117)); err != nil || title != "updated-by-process" {
		t.Fatalf("same-selector committed title=%q err=%v", title, err)
	}
}

func TestPostgreSQLUpsertSameSelectorMultiConnection(t *testing.T) {
	ctx := context.Background()
	for _, profile := range []struct{ name, namespace, env string }{{"postgresql-c", "c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for PostgreSQL same-selector connection evidence", profile.env)
			}
			fixture, applicationNamespace, _ := newPostgreSQLMutationOracleFixture(t, dsn, profile.namespace)
			const workers = 4
			systems := make([]System[mutationResultPrincipal, mutationResultActor], workers)
			for worker := range workers {
				database, _, err := postgresprovider.New().Open(ctx, dsn)
				if err != nil {
					t.Fatal(err)
				}
				database.SetMaxOpenConns(1)
				database.SetMaxIdleConns(1)
				t.Cleanup(func() { _ = database.Close() })
				app, err := Open(ctx, withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
					DB: database, Provider: golem.PostgreSQL, Bundle: fixture.schema.Bundle,
					Bindings: fixture.app.bindings, Descriptors: fixture.app.descriptors,
					ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
				}))
				if err != nil {
					t.Fatal(err)
				}
				systems[worker] = app.System()
			}

			start := make(chan struct{})
			errorsByWorker := make([]error, workers)
			var wait sync.WaitGroup
			for worker := range workers {
				wait.Add(1)
				go func(worker int) {
					defer wait.Done()
					<-start
					_, errorsByWorker[worker] = SystemUpsert(ctx, systems[worker], fixture.postDescriptor, fixture.target(211),
						fixture.createPost(211, golem.UUID{15: 1}, "created-once"), fixture.updateTitle("updated-after-lock"))
				}(worker)
			}
			close(start)
			wait.Wait()
			for worker, err := range errorsByWorker {
				if err != nil {
					t.Fatalf("connection worker %d: %v", worker, err)
				}
			}
			posts := oracleQualified(applicationNamespace, "posts")
			var stored struct {
				Count int    `db:"count"`
				Title string `db:"title"`
			}
			if err := fixture.app.database.GetContext(ctx, &stored, `SELECT COUNT(*) AS "count", MAX("title") AS "title" FROM `+posts+` WHERE "id"=$1`, mutationResultUUIDText(211)); err != nil || stored.Count != 1 || stored.Title != "updated-after-lock" {
				t.Fatalf("same-selector PostgreSQL result=%+v err=%v", stored, err)
			}
		})
	}
}

func TestUpsertRetriesWholeEngineAttemptAndExhaustsAsConflict(t *testing.T) {
	ctx := context.Background()
	for _, profile := range []struct{ name, namespace, env string }{{"postgresql-c", "c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for PostgreSQL retry-exhaustion evidence", profile.env)
			}
			fixture, applicationNamespace, systemNamespace := newPostgreSQLMutationOracleFixture(t, dsn, profile.namespace)
			var attempts atomic.Int64
			userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](fixture.schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[mutationResultUser]()
				rules.CanRead(golem.All[mutationResultUser]())
				return rules.Freeze(fixture.schema.User)
			})
			postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](fixture.schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
				rules := golem.NewRules[mutationResultPost]()
				rules.CanRead(golem.All[mutationResultPost]())
				rules.CanCreate(golem.All[mutationResultPost]())
				rules.CanUpdate(golem.All[mutationResultPost]())
				return rules.Freeze(fixture.schema.Post)
			})
			hook := golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](fixture.schema.Post, golem.HookCreate, func(context.Context, *golem.CreateHookRequest[mutationResultPost]) error {
				attempts.Add(1)
				return nil
			})
			bindings, err := golem.GeneratedApplicationBindings(fixture.schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, []golem.HookBinding[mutationResultActor]{hook}))
			if err != nil {
				t.Fatal(err)
			}
			app, err := Open(ctx, withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
				DB: fixture.app.database, Provider: golem.PostgreSQL, Bundle: fixture.schema.Bundle,
				Bindings: bindings, Descriptors: fixture.app.descriptors, MutationLimits: MutationLimits{MaxUpsertAttempts: 3},
				ResolvePrincipal: fixture.app.resolvePrincipal, SnapshotActor: fixture.app.snapshotActor,
			}))
			if err != nil {
				t.Fatal(err)
			}
			caller, err := app.ForPrincipal(ctx, mutationResultPrincipal{})
			if err != nil {
				t.Fatal(err)
			}
			retryContext := contextWithUpsertAttemptFinishFault(ctx, func(uint32) error {
				return &pgconn.PgError{Code: "40001", Message: "deterministic injected provider serialization interference"}
			})
			_, err = CallerUpsert(retryContext, caller, fixture.postDescriptor, fixture.target(212),
				fixture.createPost(212, golem.UUID{15: 1}, "must-not-commit"), fixture.updateTitle("unreachable"))
			var failure *golem.Error
			if !errors.As(err, &failure) || failure.Code != golem.CodeConflict || failure.Error() != "CONFLICT: mutation conflicted" {
				t.Fatalf("exhausted live retry failure=%#v err=%v", failure, err)
			}
			if attempts.Load() != 3 {
				t.Fatalf("live PostgreSQL attempts=%d want=3", attempts.Load())
			}
			var rows, facts int
			if err := fixture.app.database.GetContext(ctx, &rows, `SELECT COUNT(*) FROM `+oracleQualified(applicationNamespace, "posts")+` WHERE "id"=$1`, mutationResultUUIDText(212)); err != nil || rows != 0 {
				t.Fatalf("exhausted live retry rows=%d err=%v", rows, err)
			}
			if err := fixture.app.database.GetContext(ctx, &facts, `SELECT COUNT(*) FROM `+oracleQualified(systemNamespace, "_golem_outbox")); err != nil || facts != 0 {
				t.Fatalf("exhausted live retry facts=%d err=%v", facts, err)
			}
		})
	}
}

func TestCallerAndSystemTxClientsNeverEscapeTransaction(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		sentinel := errors.New("acceptance rollback")
		err = CallerTransaction(ctx, caller, func(tx *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, err := CallerTxCreate(ctx, tx, fixture.postDescriptor, fixture.createPost(141, golem.UUID{15: 1}, "caller-tx")); err != nil {
				return err
			}
			if _, err := CallerTxUpdate(ctx, tx, fixture.postDescriptor, fixture.target(141), fixture.updateTitle("caller-updated")); err != nil {
				return err
			}
			if _, err := CallerTxUpsert(ctx, tx, fixture.postDescriptor, fixture.target(142), fixture.createPost(142, golem.UUID{15: 1}, "caller-upsert"), fixture.updateTitle("wrong")); err != nil {
				return err
			}
			if count, err := CallerTxUpdateMany(ctx, tx, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 141}, golem.UUID{15: 142}), fixture.updateManyTitle("caller-batch")); err != nil || count != 2 {
				return errors.Join(err, fmt.Errorf("caller transaction batch count=%d", count))
			}
			if _, err := CallerTxDelete(ctx, tx, fixture.postDescriptor, fixture.target(141)); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("caller transaction rollback=%v", err)
		}

		system := fixture.app.System()
		err = SystemTransaction(ctx, system, func(tx *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, err := SystemTxCreate(ctx, tx, fixture.postDescriptor, fixture.createPost(143, golem.UUID{15: 1}, "system-tx")); err != nil {
				return err
			}
			if _, err := SystemTxUpdate(ctx, tx, fixture.postDescriptor, fixture.target(143), fixture.updateTitle("system-updated")); err != nil {
				return err
			}
			if _, err := SystemTxUpsert(ctx, tx, fixture.postDescriptor, fixture.target(144), fixture.createPost(144, golem.UUID{15: 1}, "system-upsert"), fixture.updateTitle("wrong")); err != nil {
				return err
			}
			if count, err := SystemTxDeleteMany(ctx, tx, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 143}, golem.UUID{15: 144})); err != nil || count != 2 {
				return errors.Join(err, fmt.Errorf("system transaction batch count=%d", count))
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("system transaction rollback=%v", err)
		}
		assertMutationProviderAcceptanceCounts(t, profile, []byte{141, 142, 143, 144}, 0)
	})
}

func TestEveryMutationEntryPointBeginsOrJoinsTransaction(t *testing.T) {
	ctx := context.Background()
	database, counts := openMutationBoundarySQLite(t)
	fixture := newMutationResultFixtureWithHooksAndDatabase(t, MutationLimits{}, nil, nil, database)
	system := fixture.app.System()
	caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
	if err != nil {
		t.Fatal(err)
	}

	assertOwnsTransaction := func(name string, operation func() error) {
		t.Helper()
		counts.reset()
		if err := operation(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if begins := counts.begins.Load(); begins != 1 {
			t.Fatalf("%s began %d transactions; want exactly one", name, begins)
		}
	}

	assertOwnsTransaction("SystemCreate", func() error {
		_, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(160, golem.UUID{15: 1}, "system-create"))
		return err
	})
	assertOwnsTransaction("SystemUpdate", func() error {
		_, err := SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(160), fixture.updateTitle("system-update"))
		return err
	})
	assertOwnsTransaction("SystemUpsert", func() error {
		_, err := SystemUpsert(ctx, system, fixture.postDescriptor, fixture.target(161), fixture.createPost(161, golem.UUID{15: 1}, "system-upsert"), fixture.updateTitle("unreachable"))
		return err
	})
	assertOwnsTransaction("SystemUpdateMany", func() error {
		count, err := SystemUpdateMany(ctx, system, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 160}, golem.UUID{15: 161}), fixture.updateManyTitle("system-batch"))
		if err == nil && count != 2 {
			err = fmt.Errorf("updated %d rows; want 2", count)
		}
		return err
	})
	assertOwnsTransaction("SystemDelete", func() error {
		_, err := SystemDelete(ctx, system, fixture.postDescriptor, fixture.target(160))
		return err
	})
	assertOwnsTransaction("SystemDeleteMany", func() error {
		count, err := SystemDeleteMany(ctx, system, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 161}))
		if err == nil && count != 1 {
			err = fmt.Errorf("deleted %d rows; want 1", count)
		}
		return err
	})

	assertOwnsTransaction("CallerCreate", func() error {
		_, err := CallerCreate(ctx, caller, fixture.postDescriptor, fixture.createPost(162, golem.UUID{15: 1}, "caller-create"))
		return err
	})
	assertOwnsTransaction("CallerUpdate", func() error {
		_, err := CallerUpdate(ctx, caller, fixture.postDescriptor, fixture.target(162), fixture.updateTitle("caller-update"))
		return err
	})
	assertOwnsTransaction("CallerUpsert", func() error {
		_, err := CallerUpsert(ctx, caller, fixture.postDescriptor, fixture.target(163), fixture.createPost(163, golem.UUID{15: 1}, "caller-upsert"), fixture.updateTitle("unreachable"))
		return err
	})
	assertOwnsTransaction("CallerUpdateMany", func() error {
		count, err := CallerUpdateMany(ctx, caller, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 162}, golem.UUID{15: 163}), fixture.updateManyTitle("caller-batch"))
		if err == nil && count != 2 {
			err = fmt.Errorf("updated %d rows; want 2", count)
		}
		return err
	})
	assertOwnsTransaction("CallerDelete", func() error {
		_, err := CallerDelete(ctx, caller, fixture.postDescriptor, fixture.target(162))
		return err
	})
	assertOwnsTransaction("CallerDeleteMany", func() error {
		count, err := CallerDeleteMany(ctx, caller, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 163}))
		if err == nil && count != 1 {
			err = fmt.Errorf("deleted %d rows; want 1", count)
		}
		return err
	})

	for _, transactionCase := range []struct {
		name string
		run  func(func()) error
	}{
		{
			name: "SystemTx mutation clients",
			run: func(observe func()) error {
				return SystemTransaction(ctx, system, func(tx *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
					observe()
					if _, err := SystemTxCreate(ctx, tx, fixture.postDescriptor, fixture.createPost(164, golem.UUID{15: 1}, "system-tx")); err != nil {
						return err
					}
					if _, err := SystemTxUpdate(ctx, tx, fixture.postDescriptor, fixture.target(164), fixture.updateTitle("system-tx-updated")); err != nil {
						return err
					}
					if _, err := SystemTxUpsert(ctx, tx, fixture.postDescriptor, fixture.target(165), fixture.createPost(165, golem.UUID{15: 1}, "system-tx-upsert"), fixture.updateTitle("unreachable")); err != nil {
						return err
					}
					if _, err := SystemTxUpdateMany(ctx, tx, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 164}, golem.UUID{15: 165}), fixture.updateManyTitle("system-tx-batch")); err != nil {
						return err
					}
					if _, err := SystemTxDelete(ctx, tx, fixture.postDescriptor, fixture.target(164)); err != nil {
						return err
					}
					if _, err := SystemTxDeleteMany(ctx, tx, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 165})); err != nil {
						return err
					}
					observe()
					return nil
				})
			},
		},
		{
			name: "CallerTx mutation clients",
			run: func(observe func()) error {
				return CallerTransaction(ctx, caller, func(tx *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
					observe()
					if _, err := CallerTxCreate(ctx, tx, fixture.postDescriptor, fixture.createPost(166, golem.UUID{15: 1}, "caller-tx")); err != nil {
						return err
					}
					if _, err := CallerTxUpdate(ctx, tx, fixture.postDescriptor, fixture.target(166), fixture.updateTitle("caller-tx-updated")); err != nil {
						return err
					}
					if _, err := CallerTxUpsert(ctx, tx, fixture.postDescriptor, fixture.target(167), fixture.createPost(167, golem.UUID{15: 1}, "caller-tx-upsert"), fixture.updateTitle("unreachable")); err != nil {
						return err
					}
					if _, err := CallerTxUpdateMany(ctx, tx, fixture.postDescriptor, fixture.postID.In(golem.UUID{15: 166}, golem.UUID{15: 167}), fixture.updateManyTitle("caller-tx-batch")); err != nil {
						return err
					}
					if _, err := CallerTxDelete(ctx, tx, fixture.postDescriptor, fixture.target(166)); err != nil {
						return err
					}
					if _, err := CallerTxDeleteMany(ctx, tx, fixture.postDescriptor, fixture.postID.Eq(golem.UUID{15: 167})); err != nil {
						return err
					}
					observe()
					return nil
				})
			},
		},
	} {
		t.Run(transactionCase.name, func(t *testing.T) {
			counts.reset()
			observe := func() {
				t.Helper()
				if begins := counts.begins.Load(); begins != 1 {
					t.Fatalf("transaction-bound operations observed %d begins; want the one outer transaction", begins)
				}
			}
			if err := transactionCase.run(observe); err != nil {
				t.Fatal(err)
			}
			observe()
		})
	}
}

func TestTransactionRollbackLeavesNoDataOrFacts(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		sentinel := errors.New("rollback data and facts")
		err := SystemTransaction(ctx, fixture.app.System(), func(tx *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
			for _, id := range []byte{145, 146} {
				if _, err := SystemTxCreate(ctx, tx, fixture.postDescriptor, fixture.createPost(id, golem.UUID{15: 1}, "fact-rollback")); err != nil {
					return err
				}
			}
			if count, err := SystemTxUpdateMany(ctx, tx, fixture.postDescriptor, fixture.title.Eq("fact-rollback"), fixture.updateManyTitle("fact-buffered")); err != nil || count != 2 {
				return errors.Join(err, fmt.Errorf("rollback batch count=%d", count))
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("rollback err=%v", err)
		}
		assertMutationProviderAcceptanceCounts(t, profile, []byte{145, 146}, 0)
	})
}

func TestSuccessfulMutationClearsAllExecutionLoaders(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		system := fixture.app.System()
		if system.executor.invalidationEpoch() != 0 {
			t.Fatal("fresh system execution has a non-zero loader generation")
		}
		if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(151, golem.UUID{15: 1}, "system-loader")); err != nil {
			t.Fatal(err)
		}
		if system.executor.invalidationEpoch() != 1 {
			t.Fatalf("system loader generation=%d want=1", system.executor.invalidationEpoch())
		}
		if err := SystemTransaction(ctx, system, func(tx *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, err := SystemTxUpdate(ctx, tx, fixture.postDescriptor, fixture.target(151), fixture.updateTitle("system-loader-updated")); err != nil {
				return err
			}
			_, err := SystemTxCreate(ctx, tx, fixture.postDescriptor, fixture.createPost(152, golem.UUID{15: 1}, "same-outer-transaction"))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if system.executor.invalidationEpoch() != 2 {
			t.Fatalf("multi-write outer commit invalidated %d times; generation=%d", system.executor.invalidationEpoch()-1, system.executor.invalidationEpoch())
		}

		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CallerCreate(ctx, caller, fixture.postDescriptor, fixture.createPost(153, golem.UUID{15: 1}, "caller-loader")); err != nil {
			t.Fatal(err)
		}
		if caller.executor.invalidationEpoch() != 1 {
			t.Fatalf("caller loader generation=%d want=1", caller.executor.invalidationEpoch())
		}
	})
}

func TestCallerAndSystemReadAfterWriteObserveCommittedState(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		system := fixture.app.System()
		if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(154, golem.UUID{15: 1}, "created")); err != nil {
			t.Fatal(err)
		}
		selector := golem.GeneratedUniqueSelectorValue[mutationResultPost](fixture.schema.Post, fixture.schema.PostKey,
			golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: 154}))
		projection := golem.Select[mutationResultPost](fixture.title)
		row, err := SystemFindUnique(ctx, system, fixture.postDescriptor, selector, golem.RuntimeProjectionReadOption(projection))
		if err != nil {
			t.Fatal(err)
		}
		if title, present := golem.Value(row, fixture.title).Get(); !present || title != "created" {
			t.Fatalf("system read-after-write title=%q present=%t", title, present)
		}

		caller, err := fixture.app.ForPrincipal(ctx, mutationResultPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CallerUpdate(ctx, caller, fixture.postDescriptor, selector, fixture.updateTitle("caller-committed")); err != nil {
			t.Fatal(err)
		}
		row, err = CallerFindUnique(ctx, caller, fixture.postDescriptor, selector, golem.RuntimeProjectionReadOption(projection))
		if err != nil {
			t.Fatal(err)
		}
		if title, present := golem.Value(row, fixture.title).Get(); !present || title != "caller-committed" {
			t.Fatalf("caller read-after-write title=%q present=%t", title, present)
		}
	})
}

func TestMutationErrorsAreStableAndDoNotLeakProviderFacts(t *testing.T) {
	model := golem.ModelID{15: 1}
	tests := []struct {
		name    string
		kind    scalarMutationFailureKind
		code    golem.ErrorCode
		message string
	}{
		{name: "not found", kind: scalarMutationNotFound, code: golem.CodeNotFound, message: "NOT_FOUND: record not found"},
		{name: "forbidden", kind: scalarMutationForbidden, code: golem.CodeForbidden, message: "FORBIDDEN: mutation is not authorized"},
		{name: "conflict", kind: scalarMutationConflict, code: golem.CodeConflict, message: "CONFLICT: mutation conflicted"},
		{name: "invalid", kind: scalarMutationInvalid, code: golem.CodeBadUserInput, message: "BAD_USER_INPUT: mutation could not be completed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			internal := scalarMutationError(mutationir.Update, test.kind, 0, 1, `private table "tenant_secret" column "password"`, errors.New(`driver detail: SQLSTATE 23505 at tenant_secret.password`))
			public := publicScalarMutationError(model, internal)
			var failure *golem.Error
			if !errors.As(public, &failure) || failure.Code != test.code || failure.Error() != test.message {
				t.Fatalf("public error=%#v %v", failure, public)
			}
			text := strings.ToLower(public.Error())
			for _, secret := range []string{"tenant_secret", "password", "sqlstate", "23505", "driver"} {
				if strings.Contains(text, secret) {
					t.Fatalf("public error leaked provider fact %q: %s", secret, text)
				}
			}
		})
	}
}

func TestMutationArtifactsPlansSQLBindsAndFactsAreDeterministicUnderShuffle(t *testing.T) {
	fixture, _, _ := scalarMutationSQLite(t, `CREATE TABLE "posts" ("id" TEXT PRIMARY KEY, "author_id" TEXT NOT NULL, "title" TEXT NOT NULL DEFAULT 'database-default', "big_int" INTEGER NOT NULL DEFAULT 0, "decimal_value" INTEGER)`)
	left := deterministicCreatePlan(t, fixture, false)
	right := deterministicCreatePlan(t, fixture, true)
	leftCanonical, err := mutationir.CanonicalPlan(left)
	if err != nil {
		t.Fatal(err)
	}
	rightCanonical, err := mutationir.CanonicalPlan(right)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leftCanonical, rightCanonical) {
		t.Fatal("scalar input shuffle changed canonical mutation plan")
	}
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		provider := provider
		t.Run(fmt.Sprintf("provider-%d", provider), func(t *testing.T) {
			proof, err := policysql.NewCapabilityProof(provider, [32]byte(fixture.Registry.ModelFingerprint()),
				policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON,
				policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation,
			)
			if err != nil {
				t.Fatal(err)
			}
			leftProgram, err := mutationsql.Render(left, fixture.Registry, provider, proof)
			if err != nil {
				t.Fatal(err)
			}
			rightProgram, err := mutationsql.Render(right, fixture.Registry, provider, proof)
			if err != nil {
				t.Fatal(err)
			}
			leftStatements, rightStatements := leftProgram.Statements(), rightProgram.Statements()
			if len(leftStatements) != len(rightStatements) {
				t.Fatalf("statement counts differ: %d != %d", len(leftStatements), len(rightStatements))
			}
			for index := range leftStatements {
				if leftStatements[index].SQL() != rightStatements[index].SQL() || !reflect.DeepEqual(leftStatements[index].Bindings(), rightStatements[index].Bindings()) {
					t.Fatalf("statement %d SQL/binds changed under input shuffle", index)
				}
			}
		})
	}

	model := policyir.ModelID(fixture.Post)
	id := policyir.UUIDValue([16]byte{15: 160})
	author := policyir.UUIDValue([16]byte{15: 1})
	title, _ := policyir.StringValue("deterministic")
	cells := []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.PostID), id),
		mutationdecode.Value(policyir.FieldID(fixture.AuthorID), author),
		mutationdecode.Value(policyir.FieldID(fixture.PostTitle), title),
	}
	before, err := mutationdecode.NewRow(fixture.Registry, model, cells)
	if err != nil {
		t.Fatal(err)
	}
	reversed := []mutationdecode.Cell{cells[2], cells[1], cells[0]}
	after, err := mutationdecode.NewRow(fixture.Registry, model, reversed)
	if err != nil {
		t.Fatal(err)
	}
	requirement, _ := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	first, err := mutationfact.New(fixture.Registry, mutationfact.EventID{1}, requirement, mutationfact.CausationID{2}, 1, nil, &before)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mutationfact.New(fixture.Registry, mutationfact.EventID{1}, requirement, mutationfact.CausationID{2}, 1, nil, &after)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, _ := mutationfact.Encode(first)
	secondBytes, _ := mutationfact.Encode(second)
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("persisted-cell shuffle changed canonical fact bytes")
	}
}

func deterministicCreatePlan(t *testing.T, fixture schematest.Fixture, reverse bool) mutationir.Plan {
	t.Helper()
	model := policyir.ModelID(fixture.Post)
	idOperation, _ := mutationir.NewSet(policyir.FieldID(fixture.PostID), scalarMutationType(t, fixture, fixture.PostID), scalarMutationUUID(160))
	authorOperation, _ := mutationir.NewSet(policyir.FieldID(fixture.AuthorID), scalarMutationType(t, fixture, fixture.AuthorID), scalarMutationUUID(1))
	operations := []mutationir.ScalarOperation{idOperation, authorOperation}
	if reverse {
		operations[0], operations[1] = operations[1], operations[0]
	}
	afterFields := []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.AuthorID), policyir.FieldID(fixture.PostTitle)}
	if reverse {
		afterFields[0], afterFields[2] = afterFields[2], afterFields[0]
	}
	after, _ := mutationir.NewImageRequirements(model, afterFields, nil)
	truth, _ := policyir.NewConstant(model, true)
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Create, Model: model, ScalarOperations: operations, After: after, RowPostcondition: &truth, Identity: mutationir.IdentityProduced})
	if err != nil {
		t.Fatal(err)
	}
	return scalarMutationPlan(t, graph, after)
}

func assertMutationAcceptanceCounts(t testing.TB, fixture mutationResultFixture, ids []byte, wantFacts int) {
	t.Helper()
	for _, id := range ids {
		var count int
		if err := fixture.app.database.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM "posts" WHERE "id"=?`, mutationResultUUIDText(id)); err != nil || count != 0 {
			t.Fatalf("rolled-back id=%d rows=%d err=%v", id, count, err)
		}
	}
	var facts int
	if err := fixture.app.database.GetContext(context.Background(), &facts, `SELECT COUNT(*) FROM "_golem_outbox"`); err != nil || facts != wantFacts {
		t.Fatalf("outbox facts=%d want=%d err=%v", facts, wantFacts, err)
	}
}

func assertMutationProviderAcceptanceCounts(t testing.TB, profile mutationProviderAcceptanceFixture, ids []byte, wantFacts int) {
	t.Helper()
	for _, id := range ids {
		var count int
		query := `SELECT COUNT(*) FROM ` + profile.posts + ` WHERE "id"=` + profile.placeholder(1)
		if err := profile.fixture.app.database.GetContext(context.Background(), &count, query, mutationResultUUIDText(id)); err != nil || count != 0 {
			t.Fatalf("rolled-back id=%d rows=%d err=%v", id, count, err)
		}
	}
	var facts int
	if err := profile.fixture.app.database.GetContext(context.Background(), &facts, `SELECT COUNT(*) FROM `+profile.outbox); err != nil || facts != wantFacts {
		t.Fatalf("outbox facts=%d want=%d err=%v", facts, wantFacts, err)
	}
}

func runUpsertProcessHelper(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("GOLEM_P4_UPSERT_DSN")
	readyPath := os.Getenv("GOLEM_P4_UPSERT_READY")
	startPath := os.Getenv("GOLEM_P4_UPSERT_START")
	if dsn == "" || readyPath == "" || startPath == "" {
		t.Fatal("upsert helper environment is incomplete")
	}
	provider := sqliteprovider.New()
	database, _, err := provider.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	fixture := newMutationResultFixtureWithHooksAndDatabaseMode(t, MutationLimits{}, nil, nil, database, false)
	if err := os.WriteFile(readyPath, []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(startPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for synchronized upsert start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, err = SystemUpsert(context.Background(), fixture.app.System(), fixture.postDescriptor, fixture.target(117),
		fixture.createPost(117, golem.UUID{15: 1}, "created-by-process"), fixture.updateTitle("updated-by-process"))
	if err != nil {
		t.Fatal(err)
	}
}

func assertOutboxV1Inventory(t testing.TB, schema physical.PhysicalSchema) {
	t.Helper()
	var found int
	for _, object := range schema.System.Objects {
		if object.Kind != physical.SystemOutbox {
			continue
		}
		found++
		if !physical.IsOutboxSystemObjectV1(object) {
			t.Fatalf("outbox inventory is not the closed SystemOutbox V1 object: %#v", object)
		}
	}
	if found != 1 {
		t.Fatalf("SystemOutbox V1 inventory count=%d want=1", found)
	}
}

func assertSystemFingerprintEqual(t testing.TB, expected, actual physical.PhysicalSchema) {
	t.Helper()
	want, err := physical.SystemFingerprint(expected.Provider, expected.System)
	if err != nil {
		t.Fatal(err)
	}
	got, err := physical.SystemFingerprint(actual.Provider, actual.System)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("introspected SystemOutbox fingerprint=%s want=%s", got, want)
	}
}

func quoteAcceptanceIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
