package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	eventprovidertest "github.com/eleven-am/golem/go/internal/event/provider/providertest"
	"github.com/eleven-am/golem/go/internal/physical"
	queueprovidertest "github.com/eleven-am/golem/go/internal/queue/provider/providertest"
	"github.com/jmoiron/sqlx"
)

func TestNetworkFilesystemWALDataSourceNameSetsExclusiveLockingBeforeEveryCallerPragma(t *testing.T) {
	configured, err := configureDataSourceNameForProfile("file:network.db?_pragma=cache_size(-4000)&mode=rwc", DurabilityNetworkFilesystemWAL)
	if err != nil {
		t.Fatal(err)
	}
	want := "file:network.db?_pragma=locking_mode(EXCLUSIVE)&_pragma=cache_size(-4000)&mode=rwc&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
	if configured != want {
		t.Fatalf("configured=%q, want %q", configured, want)
	}
	plain, err := configureDataSourceNameForProfile("network.db", DurabilityNetworkFilesystemWAL)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "file:network.db?_pragma=locking_mode(EXCLUSIVE)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate" {
		t.Fatalf("configured=%q", plain)
	}
	for _, durability := range []DurabilityProfile{DurabilityWAL, DurabilityNetworkFilesystem} {
		other, err := configureDataSourceNameForProfile("file:network.db?mode=rwc", durability)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(other, "locking_mode") {
			t.Fatalf("profile %d configured exclusive locking: %q", durability, other)
		}
	}
}

func TestNetworkFilesystemWALRefusesCallerLockingModeAndMemoryDatabases(t *testing.T) {
	for _, durability := range []DurabilityProfile{DurabilityWAL, DurabilityNetworkFilesystem, DurabilityNetworkFilesystemWAL} {
		for _, dataSourceName := range []string{
			"file:test.db?_pragma=locking_mode(EXCLUSIVE)",
			"file:test.db?_pragma=LOCKING%5FMODE%3dnormal",
			"file:test.db?_pragma=main.locking_mode(normal)",
		} {
			if _, err := configureDataSourceNameForProfile(dataSourceName, durability); err == nil || !strings.Contains(err.Error(), "locking_mode") {
				t.Fatalf("profile %d accepted caller locking mode %q: %v", durability, dataSourceName, err)
			}
		}
	}
	if _, err := configureDataSourceNameForProfile("file:shared_wal?mode=memory&cache=shared", DurabilityNetworkFilesystemWAL); err == nil {
		t.Fatal("network-filesystem WAL accepted a shared in-memory database")
	}
	if _, _, err := New().OpenWithDurability(context.Background(), "file:shared_wal_open?mode=memory&cache=shared", DurabilityNetworkFilesystemWAL); err == nil {
		t.Fatal("network-filesystem WAL opened a shared in-memory database")
	}
}

func TestNetworkFilesystemWALOpenVerifiesExclusiveSingleConnection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "exclusive.sqlite")
	database, _, err := New().OpenWithDurability(ctx, "file:"+path, DurabilityNetworkFilesystemWAL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if got := database.Stats().MaxOpenConnections; got != NetworkFilesystemPoolWidth {
		t.Fatalf("maximum open=%d, want %d", got, NetworkFilesystemPoolWidth)
	}
	if err := verifyProviderOwnedDurabilityForProfile(ctx, database, NetworkFilesystemPoolWidth, DurabilityNetworkFilesystemWAL); err != nil {
		t.Fatal(err)
	}
	var lockingMode, journalMode string
	if err := database.GetContext(ctx, &lockingMode, "PRAGMA locking_mode"); err != nil {
		t.Fatal(err)
	}
	if err := database.GetContext(ctx, &journalMode, "PRAGMA journal_mode"); err != nil {
		t.Fatal(err)
	}
	if lockingMode != lockingModeExclusive || journalMode != journalModeWAL {
		t.Fatalf("locking_mode=%q journal_mode=%q", lockingMode, journalMode)
	}
	database.SetMaxIdleConns(0)
	database.SetMaxIdleConns(NetworkFilesystemPoolWidth)
	if _, err := database.ExecContext(ctx, "CREATE TABLE recycled (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("recycled connection write: %v", err)
	}
	if err := database.GetContext(ctx, &lockingMode, "PRAGMA locking_mode"); err != nil {
		t.Fatal(err)
	}
	if lockingMode != lockingModeExclusive {
		t.Fatalf("recycled connection locking_mode=%q, want exclusive", lockingMode)
	}
	if _, err := os.Stat(path + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("shared-memory index exists after recycled connection: %v", err)
	}
}

func TestNetworkFilesystemWALRefusesNonEmptySharedMemoryIndex(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "stale.sqlite")
	if err := os.WriteFile(path+"-shm", []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	database, _, err := New().OpenWithDurability(ctx, "file:"+path, DurabilityNetworkFilesystemWAL)
	if database != nil {
		_ = database.Close()
		t.Fatal("non-empty shared-memory index was accepted")
	}
	if err == nil || !strings.Contains(err.Error(), "shared-memory index") || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("error=%v, want a non-empty shared-memory index refusal", err)
	}
	if err := os.Truncate(path+"-shm", 0); err != nil {
		t.Fatal(err)
	}
	database, _, err = New().OpenWithDurability(ctx, "file:"+path, DurabilityNetworkFilesystemWAL)
	if err != nil {
		t.Fatalf("empty shared-memory leftover refused: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkFilesystemWALRunsEventDeliveryContract(t *testing.T) {
	provider := New()
	database := openNetworkFilesystemWALFixture(t, provider)
	schema, err := provider.Lower(context.Background(), socialModelIR(), physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.ApplyInitial(context.Background(), database, schema); err != nil {
		t.Fatal(err)
	}
	cause := deliveryUUID(7)
	insertDeliveryFact(t, database, cause, deliveryUUID(71), 1, 71)
	insertDeliveryFact(t, database, cause, deliveryUUID(72), 2, 72)
	coordinator, err := provider.EventCoordinator(database)
	if err != nil {
		t.Fatal(err)
	}
	eventprovidertest.RunCoordinatorContract(t, coordinator, cause, 2)
}

func TestNetworkFilesystemWALRunsQueueContracts(t *testing.T) {
	for name, run := range map[string]func(testing.TB, queueprovidertest.Fixture){
		"ClaimIsExclusiveUnderConcurrency":                  queueprovidertest.ClaimIsExclusiveUnderConcurrency,
		"StaleTokenCannotTransition":                        queueprovidertest.StaleTokenCannotTransition,
		"TransactionalEnqueueIsAtomicWithCallerTransaction": queueprovidertest.TransactionalEnqueueIsAtomicWithCallerTransaction,
		"DedupeCoalescesActiveAndReleasesOnTerminal":        queueprovidertest.DedupeCoalescesActiveAndReleasesOnTerminal,
	} {
		t.Run(name, func(t *testing.T) {
			provider := New()
			database := openNetworkFilesystemWALFixture(t, provider)
			store, err := provider.QueueStore(database, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.EnsureSchema(context.Background()); err != nil {
				t.Fatal(err)
			}
			run(t, queueprovidertest.Fixture{Store: store, Database: database})
		})
	}
}

func openNetworkFilesystemWALFixture(t *testing.T, provider *Provider) *sqlx.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "network-wal.db")
	database, _, err := provider.OpenWithDurability(context.Background(), "file:"+path, DurabilityNetworkFilesystemWAL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		if _, err := os.Stat(path + "-shm"); !os.IsNotExist(err) {
			t.Errorf("shared-memory index exists: %v", err)
		}
	})
	return database
}
