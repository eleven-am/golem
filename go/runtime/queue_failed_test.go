package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	"github.com/eleven-am/golem/go/queue"
	"github.com/jmoiron/sqlx"
)

type failedFixture struct {
	app      *App[testPrincipal, testActor]
	database *sqlx.DB
	insert   string
	count    string
	rename   string
	lookup   string
}

func openFailedFixture(t *testing.T, kind string, config *QueueConfig, failed []JobFailed[testPrincipal, testActor]) failedFixture {
	t.Helper()
	fixture, err := openFailedApp(t, kind, config, failed)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func openFailedApp(t *testing.T, kind string, config *QueueConfig, failed []JobFailed[testPrincipal, testActor]) (failedFixture, error) {
	t.Helper()
	ctx := context.Background()
	var schemaFixture schematest.Fixture
	var database *sqlx.DB
	var provider golem.Provider
	var insert, count, rename, lookup string
	if kind == "postgresql" {
		dsn := strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN"))
		if dsn == "" {
			t.Skip("GOLEM_TEST_POSTGRES_DSN is not configured")
		}
		suffix := time.Now().UnixNano()
		namespace := physical.PhysicalName(fmt.Sprintf("golem_failed_%d_%d", os.Getpid(), suffix))
		systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_failed_system_%d_%d", os.Getpid(), suffix))
		schemaFixture = schematest.NewSubscribedIndexedPostgreSQLNamespaces(t, namespace, systemNamespace)
		postgres := postgresprovider.New()
		var err error
		database, _, err = postgres.Open(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(namespace)+`" CASCADE`)
			_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(systemNamespace)+`" CASCADE`)
			_ = database.Close()
		})
		if err := postgres.ApplyInitial(ctx, database, schemaFixture.PostgreSQL); err != nil {
			t.Fatal(err)
		}
		provider = golem.PostgreSQL
		insert = `INSERT INTO "` + string(namespace) + `"."users"("id","name") VALUES ($1,$2)`
		count = `SELECT COUNT(*) FROM "` + string(namespace) + `"."users" WHERE "name"=$1`
		rename = `UPDATE "` + string(namespace) + `"."users" SET "name"=$1 WHERE "id"=$2 AND "name"<>$3`
		lookup = `SELECT "name" FROM "` + string(namespace) + `"."users" WHERE "id"=$1`
	} else {
		schemaFixture = schematest.New(t)
		var err error
		database, _, err = sqlite.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "failed.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		if err := sqlite.New().ApplyInitial(ctx, database, schemaFixture.SQLite); err != nil {
			t.Fatal(err)
		}
		provider = golem.SQLite
		insert = `INSERT INTO "users"("id","name") VALUES (?,?)`
		count = `SELECT COUNT(*) FROM "users" WHERE "name"=?`
		rename = `UPDATE "users" SET "name"=? WHERE "id"=? AND "name"<>?`
		lookup = `SELECT "name" FROM "users" WHERE "id"=?`
	}
	userDescriptor := golem.GeneratedModelDescriptor[testUser](schemaFixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{schemaFixture.UserID, schemaFixture.UserName}, nil, nil, nil))
	postDescriptor := golem.GeneratedModelDescriptor[testPost](schemaFixture.Post, golem.GeneratedDescriptorShape([]golem.FieldID{schemaFixture.PostID, schemaFixture.AuthorID, schemaFixture.PostTitle}, nil, nil, nil))
	descriptorPackage := golem.GeneratedStampedPackageDescriptors(schemaFixture.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata())
	descriptors, err := golem.GeneratedApplicationDescriptors(schemaFixture.Bundle.GenerationDigest(), descriptorPackage)
	if err != nil {
		t.Fatal(err)
	}
	userPolicy := golem.GeneratedPolicyBinding[testActor, testUser](schemaFixture.User, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testUser]()
		rules.CanRead(golem.All[testUser]())
		return rules.Freeze(schemaFixture.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[testActor, testPost](schemaFixture.Post, func(testActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[testPost]()
		rules.CanRead(golem.All[testPost]())
		return rules.Freeze(schemaFixture.Post)
	})
	bindingPackage := golem.GeneratedStampedPackageBindings(schemaFixture.Bundle.GenerationDigest(), []golem.PolicyBinding[testActor]{userPolicy, postPolicy}, nil)
	bindings, err := golem.GeneratedApplicationBindings(schemaFixture.Bundle.GenerationDigest(), bindingPackage)
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, withRuntimeTestEvents(t, Config[testPrincipal, testActor]{
		Database: p8RuntimeTestDatabase(database, provider), Bundle: schemaFixture.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(_ context.Context, principal testPrincipal) (testActor, error) {
			return testActor{Allow: principal.Allow}, nil
		},
		Queue: config, QueueFailed: failed,
	}))
	if err != nil {
		return failedFixture{}, err
	}
	return failedFixture{app: app, database: database, insert: insert, count: count, rename: rename, lookup: lookup}, nil
}

func (fixture failedFixture) users(t *testing.T, name string) int {
	t.Helper()
	var total int
	if err := fixture.database.QueryRowContext(context.Background(), fixture.count, name).Scan(&total); err != nil {
		t.Fatal(err)
	}
	return total
}

func (fixture failedFixture) record(transaction *SystemTx[testPrincipal, testActor], name string) error {
	_, err := transaction.system.executor.transaction.ExecContext(context.Background(), fixture.insert, "00000000-0000-4000-8000-"+fmt.Sprintf("%012d", time.Now().UnixNano()%1000000000000), name)
	return err
}

func failedLimits() queue.Limits {
	limits := queue.DefaultLimits()
	limits.LeaseDuration = time.Second
	limits.PollInterval = 10 * time.Millisecond
	return limits
}

func failedRegistry(t *testing.T, name string, attempts int, handle func(context.Context, queue.Job[queueGatePayload]) error) (*queue.Registry, queue.Type[queueGatePayload]) {
	t.Helper()
	registry := queue.NewRegistry()
	jobType, err := queue.Register(registry, queue.Definition[queueGatePayload]{Type: name, MaxAttempts: attempts, Handle: handle})
	if err != nil {
		t.Fatal(err)
	}
	return registry, jobType
}

func runFailedWorker(t *testing.T, app *App[testPrincipal, testActor]) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.RunQueueWorker(ctx) }()
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("worker ended with %v", err)
			}
		})
	})
}

func awaitJobState(t *testing.T, app *App[testPrincipal, testActor], identity queue.JobID, state queue.State) queue.Status {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last queue.Status
	for time.Now().Before(deadline) {
		status, err := app.QueueOperator().Inspect(context.Background(), identity)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == state {
			return status
		}
		last = status
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never became %s, last %#v", identity, state, last)
	return last
}

func eachFailedProvider(t *testing.T, run func(*testing.T, string)) {
	for _, kind := range []string{"sqlite", "postgresql"} {
		t.Run(kind, func(t *testing.T) { run(t, kind) })
	}
}

func TestFailureCallbackCommitsAtomicallyWithFailed(t *testing.T) {
	eachFailedProvider(t, func(t *testing.T, kind string) {
		registry, jobType := failedRegistry(t, "failed.atomic", 5, func(context.Context, queue.Job[queueGatePayload]) error {
			return queue.Terminal(errors.New("poison"))
		})
		var fixture failedFixture
		var seen atomic.Pointer[queue.Failure[queueGatePayload]]
		callback := OnJobFailed[testPrincipal, testActor](jobType, func(ctx context.Context, transaction *SystemTx[testPrincipal, testActor], failure queue.Failure[queueGatePayload]) error {
			seen.Store(&failure)
			return fixture.record(transaction, "failed-"+string(failure.ID))
		})
		fixture = openFailedFixture(t, kind, &QueueConfig{Registry: registry, Limits: failedLimits()}, []JobFailed[testPrincipal, testActor]{callback})
		identity, err := fixture.app.Enqueue(context.Background(), newQueueGatePending(t, jobType))
		if err != nil {
			t.Fatal(err)
		}
		runFailedWorker(t, fixture.app)
		status := awaitJobState(t, fixture.app, identity, queue.StateFailed)
		if fixture.users(t, "failed-"+string(identity)) != 1 {
			t.Fatal("failed job committed without its status row")
		}
		failure := seen.Load()
		if failure == nil || failure.ID != identity || failure.Code != queue.FailureTerminal || failure.Payload.Title != "draft" || failure.Attempt != 1 || failure.MaxAttempts != 5 {
			t.Fatalf("failure=%#v status=%#v", failure, status)
		}
	})
}

func TestFailureCallbackErrorRollsBackAndRetriesAfterLeaseExpiry(t *testing.T) {
	eachFailedProvider(t, func(t *testing.T, kind string) {
		var executions atomic.Int64
		registry, jobType := failedRegistry(t, "failed.rollback", 5, func(context.Context, queue.Job[queueGatePayload]) error {
			executions.Add(1)
			return queue.Terminal(errors.New("poison"))
		})
		var fixture failedFixture
		var calls atomic.Int64
		refused := make(chan struct{})
		callback := OnJobFailed[testPrincipal, testActor](jobType, func(ctx context.Context, transaction *SystemTx[testPrincipal, testActor], failure queue.Failure[queueGatePayload]) error {
			if err := fixture.record(transaction, "rollback-"+string(failure.ID)); err != nil {
				return err
			}
			if calls.Add(1) == 1 {
				defer close(refused)
				return errors.New("callback refused")
			}
			return nil
		})
		fixture = openFailedFixture(t, kind, &QueueConfig{Registry: registry, Limits: failedLimits()}, []JobFailed[testPrincipal, testActor]{callback})
		identity, err := fixture.app.Enqueue(context.Background(), newQueueGatePending(t, jobType))
		if err != nil {
			t.Fatal(err)
		}
		runFailedWorker(t, fixture.app)
		select {
		case <-refused:
		case <-time.After(30 * time.Second):
			t.Fatal("the callback never ran")
		}
		name := "rollback-" + string(identity)
		status, err := fixture.app.QueueOperator().Inspect(context.Background(), identity)
		if err != nil || status.State == queue.StateFailed || fixture.users(t, name) != 0 {
			t.Fatalf("a refused callback leaked state=%s users=%d error=%v", status.State, fixture.users(t, name), err)
		}
		awaitJobState(t, fixture.app, identity, queue.StateFailed)
		if fixture.users(t, name) != 1 || calls.Load() != 2 || executions.Load() != 1 {
			t.Fatalf("users=%d calls=%d executions=%d", fixture.users(t, name), calls.Load(), executions.Load())
		}
	})
}

func TestFailureCallbackRunsForACrashOnTheLastAttempt(t *testing.T) {
	eachFailedProvider(t, func(t *testing.T, kind string) {
		var executions atomic.Int64
		registry, jobType := failedRegistry(t, "failed.crash", 1, func(context.Context, queue.Job[queueGatePayload]) error {
			executions.Add(1)
			return nil
		})
		var fixture failedFixture
		var code atomic.Value
		callback := OnJobFailed[testPrincipal, testActor](jobType, func(ctx context.Context, transaction *SystemTx[testPrincipal, testActor], failure queue.Failure[queueGatePayload]) error {
			code.Store(failure.Code)
			return fixture.record(transaction, "crash-"+string(failure.ID))
		})
		fixture = openFailedFixture(t, kind, &QueueConfig{Registry: registry, Limits: failedLimits()}, []JobFailed[testPrincipal, testActor]{callback})
		identity, err := fixture.app.Enqueue(context.Background(), newQueueGatePending(t, jobType))
		if err != nil {
			t.Fatal(err)
		}
		crashed, err := fixture.app.queueStore.Claim(context.Background(), queueprovider.ClaimOptions{Types: []string{"failed.crash"}, Limit: 1, LeaseDuration: 100 * time.Millisecond})
		if err != nil || len(crashed) != 1 {
			t.Fatalf("crashed claim %#v error=%v", crashed, err)
		}
		time.Sleep(250 * time.Millisecond)
		runFailedWorker(t, fixture.app)
		awaitJobState(t, fixture.app, identity, queue.StateFailed)
		if fixture.users(t, "crash-"+string(identity)) != 1 || executions.Load() != 0 || code.Load() != queue.FailureAttemptsExhausted {
			t.Fatalf("users=%d executions=%d code=%v", fixture.users(t, "crash-"+string(identity)), executions.Load(), code.Load())
		}
	})
}

func TestFailureCallbackReportsAnUndecodablePayload(t *testing.T) {
	registry, jobType := failedRegistry(t, "failed.payload", 5, func(context.Context, queue.Job[queueGatePayload]) error { return nil })
	var fixture failedFixture
	var seen atomic.Pointer[queue.Failure[queueGatePayload]]
	callback := OnJobFailed[testPrincipal, testActor](jobType, func(ctx context.Context, transaction *SystemTx[testPrincipal, testActor], failure queue.Failure[queueGatePayload]) error {
		seen.Store(&failure)
		return nil
	})
	fixture = openFailedFixture(t, "sqlite", &QueueConfig{Registry: registry, Limits: failedLimits()}, []JobFailed[testPrincipal, testActor]{callback})
	raw, err := queueprovider.NewIdentifier()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.queueStore.Enqueue(context.Background(), nil, queueprovider.EnqueueRequest{ID: raw, Type: "failed.payload", Payload: []byte(`[1,2]`), MaxAttempts: 5}); err != nil {
		t.Fatal(err)
	}
	identity := queue.JobID(raw)
	runFailedWorker(t, fixture.app)
	awaitJobState(t, fixture.app, identity, queue.StateFailed)
	failure := seen.Load()
	if failure == nil || failure.Code != queue.FailurePayloadInvalid || failure.Payload.Title != "" {
		t.Fatalf("failure=%#v", failure)
	}
}

func TestQueueFailedRefusalsAtOpen(t *testing.T) {
	registry, jobType := failedRegistry(t, "failed.open", 1, func(context.Context, queue.Job[queueGatePayload]) error { return nil })
	_, stranger := failedRegistry(t, "failed.stranger", 1, func(context.Context, queue.Job[queueGatePayload]) error { return nil })
	callback := func(jobType queue.Type[queueGatePayload]) JobFailed[testPrincipal, testActor] {
		return OnJobFailed[testPrincipal, testActor](jobType, func(context.Context, *SystemTx[testPrincipal, testActor], queue.Failure[queueGatePayload]) error {
			return nil
		})
	}
	config := &QueueConfig{Registry: registry, Limits: failedLimits()}
	rows := []struct {
		name   string
		config *QueueConfig
		failed []JobFailed[testPrincipal, testActor]
	}{
		{"without a queue", nil, []JobFailed[testPrincipal, testActor]{callback(jobType)}},
		{"zero value", config, []JobFailed[testPrincipal, testActor]{{}}},
		{"nil callback", config, []JobFailed[testPrincipal, testActor]{OnJobFailed[testPrincipal, testActor, queueGatePayload](jobType, nil)}},
		{"unregistered type", config, []JobFailed[testPrincipal, testActor]{callback(stranger)}},
		{"duplicate type", config, []JobFailed[testPrincipal, testActor]{callback(jobType), callback(jobType)}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			_, err := openFailedApp(t, "sqlite", row.config, row.failed)
			if code, ok := queue.CodeOf(err); !ok || code != queue.CodeConfigInvalid {
				t.Fatalf("Open reported %v", err)
			}
		})
	}
}
