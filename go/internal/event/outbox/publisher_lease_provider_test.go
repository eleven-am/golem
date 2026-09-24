package outbox

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

type leaseProviderStore struct {
	database    *sqlx.DB
	delivery    string
	coordinator func(*testing.T) eventprovider.Coordinator
	causations  map[golem.CausationID]string
}

func (store leaseProviderStore) leased(t *testing.T) int {
	t.Helper()
	var held int
	if err := store.database.Get(&held, `SELECT COUNT(*) FROM `+store.delivery+` WHERE "status"='leased'`); err != nil {
		t.Fatal(err)
	}
	return held
}

func (store leaseProviderStore) expireLeasesExcept(t *testing.T, renewed []string, past any) {
	t.Helper()
	statement := store.database.Rebind(`UPDATE ` + store.delivery + ` SET "available_at"=?,"lease_until"=? WHERE "status"='leased' AND "causation_id" NOT IN (?,?)`)
	if _, err := store.database.Exec(statement, past, past, renewed[0], renewed[1]); err != nil {
		t.Fatal(err)
	}
}

type observedLeaseCoordinator struct {
	eventprovider.Coordinator
	claimed chan struct{}
	acked   chan string
	onClaim func()
}

func observeLeaseCoordinator(coordinator eventprovider.Coordinator) *observedLeaseCoordinator {
	return &observedLeaseCoordinator{Coordinator: coordinator, claimed: make(chan struct{}, 4096), acked: make(chan string, 4096)}
}

func (coordinator *observedLeaseCoordinator) Claim(ctx context.Context, options eventprovider.ClaimOptions) ([]eventprovider.Lease, error) {
	leases, err := coordinator.Coordinator.Claim(ctx, options)
	select {
	case coordinator.claimed <- struct{}{}:
	default:
	}
	if err == nil && coordinator.onClaim != nil {
		coordinator.onClaim()
	}
	return leases, err
}

func (coordinator *observedLeaseCoordinator) Acknowledge(ctx context.Context, causation, token string) (bool, error) {
	changed, err := coordinator.Coordinator.Acknowledge(ctx, causation, token)
	if changed {
		coordinator.acked <- causation
	}
	return changed, err
}

func forEachLeaseProvider(t *testing.T, groups int, check func(*testing.T, schematest.Fixture, leaseProviderStore, any)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		fixture := schematest.NewSubscribedIndexed(t)
		provider := sqlite.New()
		database, _, err := provider.Open(context.Background(), "file:"+filepath.Join(t.TempDir(), "leases.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		if err := provider.ApplyInitial(context.Background(), database, fixture.SQLite); err != nil {
			t.Fatal(err)
		}
		store := seedLeaseProviderStore(t, fixture, database, policyir.ProviderSQLite, "main", groups)
		store.coordinator = func(t *testing.T) eventprovider.Coordinator {
			coordinator, err := provider.EventCoordinator(database)
			if err != nil {
				t.Fatal(err)
			}
			return coordinator
		}
		check(t, fixture, store, int64(0))
	})
	for _, profile := range []struct{ name, environment string }{
		{name: "postgresql-c", environment: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "postgresql-linguistic", environment: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	} {
		t.Run(profile.name, func(t *testing.T) {
			fixture := schematest.NewSubscribedIndexed(t)
			provider := postgresql.New()
			database, _, err := provider.Open(context.Background(), testenv.DisposablePostgreSQL(t, profile.environment))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			if err := provider.ApplyInitial(context.Background(), database, fixture.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			store := seedLeaseProviderStore(t, fixture, database, policyir.ProviderPostgreSQL, "_golem", groups)
			store.coordinator = func(t *testing.T) eventprovider.Coordinator {
				coordinator, err := provider.EventCoordinator(database)
				if err != nil {
					t.Fatal(err)
				}
				return coordinator
			}
			check(t, fixture, store, time.Unix(0, 0).UTC())
		})
	}
}

func seedLeaseProviderStore(t *testing.T, fixture schematest.Fixture, database *sqlx.DB, provider policyir.Provider, namespace string, groups int) leaseProviderStore {
	t.Helper()
	store := leaseProviderStore{database: database, delivery: `"` + namespace + `"."_golem_outbox_delivery"`, causations: map[golem.CausationID]string{}}
	var rows []mutationfact.OutboxRow
	for index := 1; index <= groups; index++ {
		lease := p7EvidenceLease(t, fixture, byte(index), time.Unix(1_700_000_000+int64(index), 0).UTC(), 1)
		store.causations[golem.CausationID{15: byte(index)}] = lease.Delivery.CausationID
		rows = append(rows, p7OutboxRowFromLease(lease.Facts[0]))
	}
	statements, err := mutationfact.RenderInsertsAt(provider, namespace, rows, 999)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement.SQL(), statement.Args()...); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range rows {
		delivery, err := mutationfact.RenderDeliveryInsertAt(provider, namespace, []mutationfact.OutboxRow{row})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(delivery.SQL(), delivery.Args()...); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestPublisherNeverHoldsLeasesItCannotRenewOnProviders(t *testing.T) {
	forEachLeaseProvider(t, 4, func(t *testing.T, fixture schematest.Fixture, store leaseProviderStore, past any) {
		recorder := &leaseDeliveryRecorder{}
		gate := make(chan struct{})
		opened := make(chan struct{})
		close(opened)
		first, second := observeLeaseCoordinator(store.coordinator(t)), observeLeaseCoordinator(store.coordinator(t))
		limits := leaseLifecycleLimits()
		limits.LeaseDuration = 10 * time.Minute
		firstTransport := newGatedLeaseTransport("first", recorder, gate)
		firstPublisher, err := NewPublisher(first, publisherTestResolver{fixture.Registry}, firstTransport, limits)
		if err != nil {
			t.Fatal(err)
		}
		secondPublisher, err := NewPublisher(second, publisherTestResolver{fixture.Registry}, newGatedLeaseTransport("second", recorder, opened), limits)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		firstDone, secondDone := make(chan error, 1), make(chan error, 1)
		go func() { firstDone <- firstPublisher.Run(ctx) }()
		<-first.claimed
		if held := store.leased(t); held != 2 {
			t.Fatalf("publisher with concurrency 2 holds %d leases", held)
		}
		var inFlight []string
		for seen := map[golem.CausationID]bool{}; len(seen) < 2; {
			causation := <-firstTransport.entered
			if !seen[causation] {
				seen[causation] = true
				inFlight = append(inFlight, store.causations[causation])
			}
		}
		store.expireLeasesExcept(t, inFlight, past)
		go func() { secondDone <- secondPublisher.Run(ctx) }()
		for acknowledged := 0; acknowledged < 2; acknowledged++ {
			<-second.acked
		}
		close(gate)
		<-first.claimed
		cancel()
		if err := <-firstDone; err != nil {
			t.Fatal(err)
		}
		if err := <-secondDone; err != nil {
			t.Fatal(err)
		}
		published := recorder.snapshot()
		if len(published) != 4 {
			t.Fatalf("published causations=%d want 4: %v", len(published), published)
		}
		for causation, publishers := range published {
			if len(publishers) != 1 {
				t.Fatalf("causation %x was delivered by %v", causation, publishers)
			}
		}
	})
}

func TestPublisherReleasesClaimedLeasesThatNeverStartedOnProviders(t *testing.T) {
	forEachLeaseProvider(t, 2, func(t *testing.T, fixture schematest.Fixture, store leaseProviderStore, _ any) {
		coordinator := observeLeaseCoordinator(store.coordinator(t))
		transport := newGatedLeaseTransport("first", &leaseDeliveryRecorder{}, make(chan struct{}))
		limits := leaseLifecycleLimits()
		limits.ClaimGroups, limits.LeaseDuration = 2, 10*time.Minute
		publisher, err := NewPublisher(coordinator, publisherTestResolver{fixture.Registry}, transport, limits)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		coordinator.onClaim = cancel
		if err := publisher.Run(ctx); err != nil {
			t.Fatal(err)
		}
		reclaimed, err := store.coordinator(t).Claim(context.Background(), eventprovider.ClaimOptions{Groups: 2, LeaseDuration: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if len(reclaimed) != 2 {
			t.Fatalf("claimable after shutdown=%d want 2", len(reclaimed))
		}
		if entered := len(transport.entered); entered != 0 {
			t.Fatalf("transport entered %d times after shutdown was requested", entered)
		}
	})
}
