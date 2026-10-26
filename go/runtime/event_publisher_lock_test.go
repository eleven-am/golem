package runtime_test

import (
	"context"
	"hash/fnv"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/eleven-am/golem/go/observe"
	providerapi "github.com/eleven-am/golem/go/provider"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	"github.com/eleven-am/golem/go/runtime/testdata/p10operations"
	"github.com/jmoiron/sqlx"
)

type publisherLockObserver struct {
	mu       sync.Mutex
	topology []observe.Observation
}

func (observer *publisherLockObserver) ObserveGolem(_ context.Context, value observe.Observation) {
	if value.Operation() != observe.OperationEventTopology {
		return
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.topology = append(observer.topology, value)
}

func (observer *publisherLockObserver) count() int {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return len(observer.topology)
}

func (observer *publisherLockObserver) all() []observe.Observation {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]observe.Observation(nil), observer.topology...)
}

type publisherLockApp struct {
	app      *golemruntime.App[p10operations.Principal, p10operations.Actor]
	observer *publisherLockObserver
	database *sqlx.DB
	done     chan error
	cancel   context.CancelFunc
}

func (fixture *publisherLockApp) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	fixture.cancel = cancel
	fixture.done = make(chan error, 1)
	go func() { fixture.done <- fixture.app.RunEventPublisher(ctx) }()
	t.Cleanup(func() { fixture.stop(t) })
}

func (fixture *publisherLockApp) stop(t *testing.T) error {
	t.Helper()
	if fixture.cancel == nil {
		return nil
	}
	fixture.cancel()
	fixture.cancel = nil
	select {
	case err := <-fixture.done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("event publisher did not stop")
		return nil
	}
}

func publisherLockLimits(strict bool) events.Limits {
	return events.Limits{RetryBase: 200 * time.Millisecond, RetryCap: 200 * time.Millisecond, ShutdownGrace: time.Second, RequireExclusiveProcessLocalPublisher: strict}
}

func publisherLockSchema(t *testing.T, provider golem.Provider) physical.PhysicalSchema {
	t.Helper()
	for _, document := range p10operations.GolemGeneratedSchemaBundle().Providers() {
		if document.Provider() == provider {
			schema, err := physical.CanonicalDecode(document.Schema().Bytes())
			if err != nil {
				t.Fatal(err)
			}
			return schema
		}
	}
	t.Fatalf("schema for %s is absent", provider)
	return physical.PhysicalSchema{}
}

func openPublisherLockPostgreSQL(t *testing.T) string {
	t.Helper()
	dsn := testenv.DisposablePostgreSQL(t, "GOLEM_TEST_POSTGRES_DSN")
	database := openPublisherLockPool(t, dsn)
	if err := postgresprovider.New().ApplyInitial(context.Background(), database, publisherLockSchema(t, golem.PostgreSQL)); err != nil {
		t.Fatal(err)
	}
	return dsn
}

func openPublisherLockPool(t *testing.T, dsn string) *sqlx.DB {
	t.Helper()
	database, _, err := postgresprovider.New().Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func openPublisherLockApp(t *testing.T, database *sqlx.DB, provider golem.Provider, limits events.Limits) *publisherLockApp {
	t.Helper()
	ctx := context.Background()
	p10operations.Reset(nil)
	t.Cleanup(func() { p10operations.Reset(nil) })
	var features []string
	if provider == golem.PostgreSQL {
		features = []string{string(providerapi.FeatureAdvisoryLocks)}
	}
	handle := (*providerapi.Database)(providerhandle.AdoptUnverifiedForTest(database, providerhandle.TestMetadata{Provider: provider, Features: features, MaximumOpen: 4, MaximumIdle: 4}))
	bindings, err := p10operations.GolemGeneratedApplicationBindings()
	if err != nil {
		t.Fatal(err)
	}
	descriptors, err := p10operations.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := p10operations.GolemGeneratedApplicationEventRegistry()
	if err != nil {
		t.Fatal(err)
	}
	factories, err := p10operations.GolemGeneratedApplicationEventFactories()
	if err != nil {
		t.Fatal(err)
	}
	transport, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	observer := &publisherLockObserver{}
	app, err := golemruntime.Open(ctx, golemruntime.Config[p10operations.Principal, p10operations.Actor]{
		Database: handle, Bundle: p10operations.GolemGeneratedSchemaBundle(), Bindings: bindings, Descriptors: descriptors,
		EventRegistry: registry, EventFactories: factories, EventLimits: limits, EventTransport: transport, Observer: observer,
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		AfterCommitError:    func(context.Context, golem.AfterCommitFailure) {},
		ResolvePrincipal: func(_ context.Context, principal p10operations.Principal) (p10operations.Actor, error) {
			return p10operations.Actor{ID: principal.ID}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &publisherLockApp{app: app, observer: observer, database: database}
}

func publisherLockKey() int64 {
	digest := fnv.New64a()
	_, _ = digest.Write([]byte("golem_process_local_publisher\x00" + string(p10OperationSystemNamespace)))
	return int64(digest.Sum64())
}

func publisherLockHolders(t *testing.T, database *sqlx.DB) []int {
	t.Helper()
	key := uint64(publisherLockKey())
	var holders []int
	if err := database.SelectContext(context.Background(), &holders, `SELECT pid FROM pg_catalog.pg_locks WHERE locktype='advisory' AND granted AND database=(SELECT oid FROM pg_catalog.pg_database WHERE datname=current_database()) AND classid=$1::bigint::oid AND objid=$2::bigint::oid AND objsubid=1`, int64(key>>32), int64(key&0xffffffff)); err != nil {
		t.Fatal(err)
	}
	return holders
}

func waitPublisherLock(t *testing.T, label string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", label)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitPublisherLockHolder(t *testing.T, database *sqlx.DB, except int) int {
	t.Helper()
	var holder int
	waitPublisherLock(t, "a publisher lock holder", func() bool {
		holders := publisherLockHolders(t, database)
		if len(holders) == 1 && holders[0] != except {
			holder = holders[0]
			return true
		}
		return false
	})
	return holder
}

func TestSecondProcessLocalPublisherOnSharedPostgreSQLReportsTopology(t *testing.T) {
	dsn := openPublisherLockPostgreSQL(t)
	first := openPublisherLockApp(t, openPublisherLockPool(t, dsn), golem.PostgreSQL, publisherLockLimits(false))
	second := openPublisherLockApp(t, openPublisherLockPool(t, dsn), golem.PostgreSQL, publisherLockLimits(false))
	first.start(t)
	holder := waitPublisherLockHolder(t, first.database, 0)
	second.start(t)
	waitPublisherLock(t, "the topology observation", func() bool { return second.observer.count() != 0 })
	observation := second.observer.all()[0]
	if observation.Kind() != observe.KindEvent || observation.Outcome() != observe.OutcomeFailure || observation.Provider() != golem.PostgreSQL {
		t.Fatalf("topology observation=%s/%s/%s", observation.Kind(), observation.Outcome(), observation.Provider())
	}
	if first.observer.count() != 0 {
		t.Fatalf("lock holder reported %d topology observations", first.observer.count())
	}
	if holders := publisherLockHolders(t, first.database); len(holders) != 1 || holders[0] != holder {
		t.Fatalf("lock holders=%v want [%d]", holders, holder)
	}
	if err := first.stop(t); err != nil {
		t.Fatal(err)
	}
	waitPublisherLockHolder(t, second.database, holder)
	if count := second.observer.count(); count != 1 {
		t.Fatalf("waiting publisher reported %d topology observations, want 1", count)
	}
	if err := second.stop(t); err != nil {
		t.Fatal(err)
	}
	if holders := publisherLockHolders(t, first.database); len(holders) != 0 {
		t.Fatalf("stopped publishers still hold the lock: %v", holders)
	}
	for _, fixture := range []*publisherLockApp{first, second} {
		if inUse := fixture.database.Stats().InUse; inUse != 0 {
			t.Fatalf("stopped publisher left %d pinned connections in use", inUse)
		}
	}
}

func TestStrictProcessLocalPublisherRefusesASecondPublisherAndAcquiresAfterRelease(t *testing.T) {
	dsn := openPublisherLockPostgreSQL(t)
	first := openPublisherLockApp(t, openPublisherLockPool(t, dsn), golem.PostgreSQL, publisherLockLimits(false))
	second := openPublisherLockApp(t, openPublisherLockPool(t, dsn), golem.PostgreSQL, publisherLockLimits(true))
	first.start(t)
	holder := waitPublisherLockHolder(t, first.database, 0)
	err := second.app.RunEventPublisher(context.Background())
	if code, ok := events.CodeOf(err); !ok || code != events.CodeEventConfig {
		t.Fatalf("strict second publisher error=%v", err)
	}
	if !strings.Contains(err.Error(), string(p10OperationSystemNamespace)) {
		t.Fatalf("strict refusal does not name the namespace: %v", err)
	}
	if second.app.EventCapabilities().PublisherRunning() {
		t.Fatal("refused publisher reports running")
	}
	if err := first.stop(t); err != nil {
		t.Fatal(err)
	}
	second.start(t)
	waitPublisherLockHolder(t, second.database, holder)
	select {
	case err := <-second.done:
		t.Fatalf("strict publisher stopped after acquiring the lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := second.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestProcessLocalPublisherReacquiresTheLockAfterItsConnectionDrops(t *testing.T) {
	dsn := openPublisherLockPostgreSQL(t)
	first := openPublisherLockApp(t, openPublisherLockPool(t, dsn), golem.PostgreSQL, publisherLockLimits(true))
	first.start(t)
	holder := waitPublisherLockHolder(t, first.database, 0)
	var terminated bool
	if err := first.database.GetContext(context.Background(), &terminated, `SELECT pg_catalog.pg_terminate_backend($1)`, holder); err != nil || !terminated {
		t.Fatalf("terminate pinned backend terminated=%t err=%v", terminated, err)
	}
	waitPublisherLockHolder(t, first.database, holder)
	select {
	case err := <-first.done:
		t.Fatalf("publisher stopped after re-acquiring the lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := first.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestStrictProcessLocalPublisherStopsWhenTheLockIsLostToAnotherSession(t *testing.T) {
	dsn := openPublisherLockPostgreSQL(t)
	first := openPublisherLockApp(t, openPublisherLockPool(t, dsn), golem.PostgreSQL, publisherLockLimits(true))
	first.start(t)
	holder := waitPublisherLockHolder(t, first.database, 0)
	rival := openPublisherLockPool(t, dsn)
	connection, err := rival.Connx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var terminated bool
	if err := rival.GetContext(context.Background(), &terminated, `SELECT pg_catalog.pg_terminate_backend($1)`, holder); err != nil || !terminated {
		t.Fatalf("terminate pinned backend terminated=%t err=%v", terminated, err)
	}
	waitPublisherLock(t, "the rival session to take the lock", func() bool {
		var held bool
		if err := connection.GetContext(context.Background(), &held, `SELECT pg_try_advisory_lock($1)`, publisherLockKey()); err != nil {
			t.Fatal(err)
		}
		return held
	})
	select {
	case err := <-first.done:
		first.cancel = nil
		if code, ok := events.CodeOf(err); !ok || code != events.CodeEventConfig || !strings.Contains(err.Error(), string(p10OperationSystemNamespace)) {
			t.Fatalf("strict publisher error after losing the lock=%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("strict publisher kept running without the lock")
	}
}

func TestSubscriberWithoutLocalPublisherReportsTopologyOnce(t *testing.T) {
	ctx := context.Background()
	database, _, err := sqliteprovider.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "subscriber.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqliteprovider.New().ApplyInitial(ctx, database, publisherLockSchema(t, golem.SQLite)); err != nil {
		t.Fatal(err)
	}
	fixture := openPublisherLockApp(t, database, golem.SQLite, publisherLockLimits(false))
	caller, err := fixture.app.ForPrincipal(ctx, p10operations.Principal{ID: "alpha", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		streamContext, cancel := context.WithCancel(ctx)
		stream, err := golemruntime.CallerEvents[p10operations.Principal, p10operations.Actor, p10operations.Invite, p10operations.InviteEvent](streamContext, caller, p10operations.GolemGeneratedInviteDescriptor)
		if err != nil {
			t.Fatal(err)
		}
		_ = stream.Close()
		cancel()
	}
	if count := fixture.observer.count(); count != 1 {
		t.Fatalf("topology observations=%d want 1", count)
	}
	observation := fixture.observer.all()[0]
	if observation.Kind() != observe.KindEvent || observation.Outcome() != observe.OutcomeFailure {
		t.Fatalf("topology observation=%s/%s", observation.Kind(), observation.Outcome())
	}

	running := openPublisherLockApp(t, database, golem.SQLite, publisherLockLimits(true))
	running.start(t)
	waitPublisherLock(t, "the SQLite publisher to run", func() bool { return running.app.EventCapabilities().PublisherRunning() })
	runningCaller, err := running.app.ForPrincipal(ctx, p10operations.Principal{ID: "alpha", Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := golemruntime.CallerEvents[p10operations.Principal, p10operations.Actor, p10operations.Invite, p10operations.InviteEvent](ctx, runningCaller, p10operations.GolemGeneratedInviteDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Close()
	if count := running.observer.count(); count != 0 {
		t.Fatalf("subscriber with a running publisher reported %d topology observations", count)
	}
}

func TestSQLiteProcessLocalPublisherIsUnaffectedByTheLock(t *testing.T) {
	ctx := context.Background()
	database, _, err := sqliteprovider.New().Open(ctx, "file:"+filepath.Join(t.TempDir(), "publisher.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := sqliteprovider.New().ApplyInitial(ctx, database, publisherLockSchema(t, golem.SQLite)); err != nil {
		t.Fatal(err)
	}
	fixture := openPublisherLockApp(t, database, golem.SQLite, publisherLockLimits(true))
	fixture.start(t)
	waitPublisherLock(t, "the SQLite publisher to run", func() bool { return fixture.app.EventCapabilities().PublisherRunning() })
	select {
	case err := <-fixture.done:
		t.Fatalf("strict SQLite publisher stopped: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := fixture.stop(t); err != nil {
		t.Fatal(err)
	}
	if count := fixture.observer.count(); count != 0 {
		t.Fatalf("SQLite publisher reported %d topology observations", count)
	}
}
