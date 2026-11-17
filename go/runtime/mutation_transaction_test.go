package runtime

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationbatch "github.com/eleven-am/golem/go/internal/mutation/batch"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/jmoiron/sqlx"
)

// This driver exercises the real database/sql BeginTx boundary. It implements
// no row reads or writes and cannot supply any policy or snapshot truth.
type mutationTransactionSpy struct {
	mu         sync.Mutex
	options    []driver.TxOptions
	statements []string
	beginErr   error
}

type mutationTransactionSpyDriver struct{ spy *mutationTransactionSpy }
type mutationTransactionSpyConn struct{ spy *mutationTransactionSpy }
type mutationTransactionSpyTx struct{}

func (value mutationTransactionSpyDriver) Open(string) (driver.Conn, error) {
	return &mutationTransactionSpyConn{spy: value.spy}, nil
}
func (*mutationTransactionSpyConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("transaction spy cannot prepare SQL")
}
func (*mutationTransactionSpyConn) Close() error { return nil }
func (*mutationTransactionSpyConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transaction spy requires ConnBeginTx")
}
func (connection *mutationTransactionSpyConn) BeginTx(_ context.Context, options driver.TxOptions) (driver.Tx, error) {
	connection.spy.mu.Lock()
	defer connection.spy.mu.Unlock()
	connection.spy.options = append(connection.spy.options, options)
	if connection.spy.beginErr != nil {
		return nil, connection.spy.beginErr
	}
	return mutationTransactionSpyTx{}, nil
}
func (connection *mutationTransactionSpyConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	// Only transaction control is supported, for binding-inheritance tests.
	if !strings.HasPrefix(query, "SAVEPOINT ") && !strings.HasPrefix(query, "RELEASE SAVEPOINT ") && !strings.HasPrefix(query, "ROLLBACK TO SAVEPOINT ") {
		return nil, fmt.Errorf("transaction spy refuses data SQL")
	}
	connection.spy.mu.Lock()
	defer connection.spy.mu.Unlock()
	connection.spy.statements = append(connection.spy.statements, query)
	return driver.RowsAffected(0), nil
}
func (mutationTransactionSpyTx) Commit() error   { return nil }
func (mutationTransactionSpyTx) Rollback() error { return nil }

var mutationTransactionSpySequence atomic.Uint64

func newMutationTransactionSpy(t testing.TB, beginErr error) (*sqlx.DB, *mutationTransactionSpy) {
	t.Helper()
	spy := &mutationTransactionSpy{beginErr: beginErr}
	name := fmt.Sprintf("golem_mutation_transaction_spy_%d", mutationTransactionSpySequence.Add(1))
	sql.Register(name, mutationTransactionSpyDriver{spy})
	database, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	return sqlx.NewDb(database, name), spy
}

func TestMutationTransactionPlanIsolationMatrix(t *testing.T) {
	for _, provider := range []policyir.Provider{policyir.ProviderPostgreSQL, policyir.ProviderSQLite} {
		for _, required := range []bool{false, true} {
			for _, isolation := range []golem.IsolationLevel{golem.ReadCommitted, golem.Serializable} {
				plan, err := resolveMutationTransactionPlan(provider, isolation, required)
				refused := provider == policyir.ProviderPostgreSQL && required && isolation == golem.ReadCommitted
				if refused {
					if err == nil || plan.valid {
						t.Fatalf("required ReadCommitted accepted: %#v %v", plan, err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				serializable := provider == policyir.ProviderPostgreSQL && isolation == golem.Serializable
				if !plan.valid || plan.hasOptions != serializable || plan.serializable != serializable || serializable && (plan.options.Isolation != sql.LevelSerializable || plan.options.ReadOnly) {
					t.Fatalf("provider=%v required=%t isolation=%s plan=%#v", provider, required, isolation, plan)
				}
			}
		}
	}
	for _, invalid := range []struct {
		provider  policyir.Provider
		isolation golem.IsolationLevel
	}{{policyir.Provider(0), golem.Serializable}, {policyir.ProviderPostgreSQL, "repeatable_read"}} {
		if plan, err := resolveMutationTransactionPlan(invalid.provider, invalid.isolation, true); err == nil || plan.valid {
			t.Fatalf("invalid plan=%#v err=%v", plan, err)
		}
	}
}

func TestMutationTransactionBeginPassesOptionsAndOnlyThenCertifies(t *testing.T) {
	for _, provider := range []policyir.Provider{policyir.ProviderPostgreSQL, policyir.ProviderSQLite} {
		for _, isolation := range []golem.IsolationLevel{golem.ReadCommitted, golem.Serializable} {
			database, spy := newMutationTransactionSpy(t, nil)
			plan, err := resolveMutationTransactionPlan(provider, isolation, false)
			if err != nil {
				t.Fatal(err)
			}
			binding, err := beginMutationTransaction(context.Background(), database, plan)
			if err != nil {
				t.Fatal(err)
			}
			certified := provider == policyir.ProviderPostgreSQL && isolation == golem.Serializable
			want := driver.IsolationLevel(sql.LevelDefault)
			if certified {
				want = driver.IsolationLevel(sql.LevelSerializable)
			}
			if len(spy.options) != 1 || spy.options[0].Isolation != want || spy.options[0].ReadOnly || binding.historySerializable != certified || binding.transaction == nil {
				t.Fatalf("provider=%v isolation=%s options=%#v certified=%t", provider, isolation, spy.options, binding.historySerializable)
			}
			_ = binding.transaction.Rollback()
			binding.close()
		}
	}
	failed := errors.New("driver refused Serializable")
	database, spy := newMutationTransactionSpy(t, failed)
	plan, err := resolveMutationTransactionPlan(policyir.ProviderPostgreSQL, golem.Serializable, true)
	if err != nil {
		t.Fatal(err)
	}
	if binding, err := beginMutationTransaction(context.Background(), database, plan); binding != nil || !errors.Is(err, failed) || len(spy.options) != 1 {
		t.Fatalf("failed begin issued binding=%v err=%v options=%#v", binding, err, spy.options)
	}
	if binding, err := beginMutationTransaction(context.Background(), database, mutationTransactionPlan{}); binding != nil || err == nil || len(spy.options) != 1 {
		t.Fatalf("unresolved plan reached driver: binding=%v err=%v begins=%d", binding, err, len(spy.options))
	}
}

func TestMutationTransactionRequiredHistoryRejectsExplicitReadCommittedBeforeBegin(t *testing.T) {
	for _, options := range [][]golem.TransactionOption{nil, {golem.Isolation(golem.ReadCommitted)}, {golem.Isolation(golem.Serializable), golem.RetryConflicts(3)}} {
		database, spy := newMutationTransactionSpy(t, nil)
		isolation, attempts, err := golem.RuntimeTransactionOptionsWithDefault(options, golem.Serializable)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := resolveMutationTransactionPlan(policyir.ProviderPostgreSQL, isolation, true)
		if isolation == golem.ReadCommitted {
			if err == nil || len(spy.options) != 0 {
				t.Fatalf("explicit ReadCommitted err=%v begins=%d", err, len(spy.options))
			}
			continue
		}
		if err != nil || attempts != 1 && attempts != 3 {
			t.Fatalf("plan err=%v attempts=%d", err, attempts)
		}
		binding, err := beginMutationTransaction(context.Background(), database, plan)
		if err != nil || !binding.historySerializable {
			t.Fatalf("binding=%v err=%v", binding, err)
		}
		_ = binding.transaction.Rollback()
		binding.close()
	}
}

func TestMutationTransactionPublicExplicitIsolationUsesRequiredHistoryRule(t *testing.T) {
	fixture := schematest.NewPolicyCaptureGraph(t)
	for _, isolation := range []golem.IsolationLevel{golem.ReadCommitted, golem.Serializable} {
		failed := errors.New("stop at public transaction begin")
		database, spy := newMutationTransactionSpy(t, failed)
		app := &App[struct{}, struct{}]{database: database, provider: policyir.ProviderPostgreSQL, registry: fixture.Registry}
		callback := false
		err := SystemTransaction(context.Background(), System[struct{}, struct{}]{app: app}, func(*SystemTx[struct{}, struct{}]) error { callback = true; return nil }, golem.Isolation(isolation), golem.RetryConflicts(3))
		if callback || err == nil {
			t.Fatalf("callback=%t err=%v", callback, err)
		}
		if mutationHistoryWriterVersion == mutationfact.HistoryFactVersion && isolation == golem.ReadCommitted {
			if len(spy.options) != 0 || errors.Is(err, failed) {
				t.Fatalf("required explicit ReadCommitted reached driver: err=%v options=%#v", err, spy.options)
			}
			continue
		}
		want := driver.IsolationLevel(sql.LevelDefault)
		if isolation == golem.Serializable {
			want = driver.IsolationLevel(sql.LevelSerializable)
		}
		if !errors.Is(err, failed) || len(spy.options) != 1 || spy.options[0].Isolation != want {
			t.Fatalf("explicit isolation=%s err=%v options=%#v", isolation, err, spy.options)
		}
	}
}

func TestMutationTransactionOwnedEntryPointsUseBinaryWriterAndWholeRegistry(t *testing.T) {
	fixtures := map[string]schematest.GraphFixture{"no templates": schematest.NewSubscribedGraph(t), "registered templates": schematest.NewPolicyCaptureGraph(t)}
	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			required := mutationHistoryWriterVersion == mutationfact.HistoryFactVersion && len(fixture.Registry.PolicyCaptureRoots()) != 0
			if mutationHistoryWriterRequired(fixture.Registry) != required || mutationHistoryWriterRequired(nil) {
				t.Fatal("history requirement disagrees with immutable writer version and registry inventory")
			}
			wantIsolation := driver.IsolationLevel(sql.LevelDefault)
			if required {
				wantIsolation = driver.IsolationLevel(sql.LevelSerializable)
			}
			program := mutationTransactionScalarProgram(t, fixture)
			for _, owner := range []string{"caller", "system", "scalar", "batch", "nested", "upsert"} {
				t.Run(owner, func(t *testing.T) {
					failed := errors.New("stop at actual driver begin")
					database, spy := newMutationTransactionSpy(t, failed)
					app := &App[struct{}, struct{}]{database: database, provider: policyir.ProviderPostgreSQL, registry: fixture.Registry}
					binding := databaseExecution(database)
					defer binding.close()
					ctx := context.Background()
					var err error
					callback := false
					switch owner {
					case "caller":
						err = callerTransactionAttempt(ctx, &Caller[struct{}, struct{}]{app: app}, func(*CallerTx[struct{}, struct{}]) error { callback = true; return nil }, mutationTransactionDefaultIsolation(app.provider, app.registry))
					case "system":
						err = SystemTransaction(ctx, System[struct{}, struct{}]{app: app}, func(*SystemTx[struct{}, struct{}]) error { callback = true; return nil })
					case "scalar":
						_, err = executeScalarMutationProgramConfigured(ctx, database, app.provider, fixture.Registry, policyir.ModelID(fixture.Post), binding, program, executionMutationConfig{}, nil)
					case "batch":
						_, err = beginBatchExecution(ctx, database, app.provider, binding, mutationbatch.PostgreSQLTransaction, fixture.Registry)
					case "nested":
						boundary := systemNestedBoundary[struct{}, struct{}]{app: app, source: binding}
						_, err = boundary.BeginNested(ctx)
					case "upsert":
						backend := sqlxUpsertBackend{database: database, provider: app.provider, registry: fixture.Registry, binding: binding}
						_, err = backend.Begin(ctx, mutationsql.PostgreSQLTransaction, 1)
					}
					if !errors.Is(err, failed) || callback || len(spy.options) != 1 || spy.options[0].Isolation != wantIsolation || spy.options[0].ReadOnly {
						t.Fatalf("err=%v callback=%t options=%#v; want one begin at isolation=%d", err, callback, spy.options, wantIsolation)
					}
				})
			}
		})
	}
}

func TestMutationTransactionSavepointsRetainOuterCertificate(t *testing.T) {
	for _, isolation := range []golem.IsolationLevel{golem.ReadCommitted, golem.Serializable} {
		database, spy := newMutationTransactionSpy(t, nil)
		plan, err := resolveMutationTransactionPlan(policyir.ProviderPostgreSQL, isolation, false)
		if err != nil {
			t.Fatal(err)
		}
		binding, err := beginMutationTransaction(context.Background(), database, plan)
		if err != nil {
			t.Fatal(err)
		}
		limits, _ := normalizeMutationLimits(MutationLimits{})
		if err := binding.enableMutation(executionMutationConfig{enabled: true, provider: policyir.ProviderPostgreSQL, limits: limits}); err != nil {
			t.Fatal(err)
		}
		batch, err := beginBatchExecution(context.Background(), database, policyir.ProviderPostgreSQL, binding, mutationbatch.PostgreSQLTransaction, nil)
		if err != nil || batch.binding != binding {
			t.Fatalf("batch err=%v binding=%v", err, batch.binding)
		}
		if err := batch.abort(); err != nil {
			t.Fatal(err)
		}
		backend := sqlxUpsertBackend{database: database, provider: policyir.ProviderPostgreSQL, binding: binding}
		attempt, err := backend.Begin(context.Background(), mutationsql.PostgreSQLTransaction, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := attempt.Abort(); err != nil {
			t.Fatal(err)
		}
		boundary := systemNestedBoundary[struct{}, struct{}]{app: &App[struct{}, struct{}]{database: database, provider: policyir.ProviderPostgreSQL}, source: binding}
		nested, err := boundary.BeginNested(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := nested.RollbackNested(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(spy.options) != 1 || binding.historySerializable != (isolation == golem.Serializable) || len(spy.statements) == 0 {
			t.Fatalf("savepoint changed ownership: begins=%d certified=%t statements=%#v", len(spy.options), binding.historySerializable, spy.statements)
		}
		binding.discardMutation()
		_ = binding.transaction.Rollback()
		binding.close()
	}
}

func mutationTransactionScalarProgram(t testing.TB, fixture schematest.GraphFixture) mutationsql.Program {
	t.Helper()
	model := policyir.ModelID(fixture.Post)
	resolver := policysql.SchemaResolver(fixture.Registry)
	var operations []mutationir.ScalarOperation
	for index, fieldID := range []golem.FieldID{fixture.PostID, fixture.AuthorID} {
		field, ok := resolver.Field(policyir.ProviderPostgreSQL, model, policyir.FieldID(fieldID))
		if !ok {
			t.Fatal("fixture field is missing")
		}
		var id [16]byte
		id[15] = byte(index + 1)
		value := policyir.UUIDValue(id)
		operation, err := mutationir.NewSet(policyir.FieldID(fieldID), field.Type, value)
		if err != nil {
			t.Fatal(err)
		}
		operations = append(operations, operation)
	}
	image, err := mutationir.NewImageRequirements(model, []policyir.FieldID{policyir.FieldID(fixture.PostID)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Create, Model: model, ScalarOperations: operations, After: image, Identity: mutationir.IdentityProduced})
	if err != nil {
		t.Fatal(err)
	}
	requirement, _ := mutationir.NewProviderRequirement(policyir.PortableProviders(), mutationir.CapabilityTransaction)
	bounds, _ := mutationir.NewStatementBounds(999, 1000)
	plan, err := mutationir.NewPlan(mutationir.PlanInput{Stance: mutationir.System, Graph: graph, Result: image, Providers: []mutationir.ProviderRequirement{requirement}, Retry: mutationir.NoRetry, Bounds: bounds})
	if err != nil {
		t.Fatal(err)
	}
	// This proof is used solely to render a program; the spy never executes it.
	proof, err := policysql.NewCapabilityProof(policyir.ProviderPostgreSQL, resolver.SchemaFingerprint(), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	program, err := mutationsql.Render(plan, fixture.Registry, policyir.ProviderPostgreSQL, proof)
	if err != nil {
		t.Fatal(err)
	}
	return program
}
