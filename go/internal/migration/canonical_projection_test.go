package migration

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestHistoricalMigrationEntryV1SourceProvenance(t *testing.T) {
	const (
		upstreamCommit = "6babdef35497aea4cd968d3260587ded05d117c0"
		upstreamSHA256 = "97bf7a3e7727bc19410f595e5fb05c675fb817e90e2f429f30c750d0b10ab291"
		upstreamLines  = 73
		adaptedSHA256  = "ac5c58245824f9e86561c00a2c095d412172af6e61ef4348dc5a173a9ca60131"
	)
	if historicalMigrationEntryV1UpstreamCommit != upstreamCommit ||
		historicalMigrationEntryV1UpstreamSHA256 != upstreamSHA256 ||
		historicalMigrationEntryV1UpstreamLines != upstreamLines {
		t.Fatal("historical migration entry v1 source provenance changed")
	}
	_, current, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(current), "canonical.go"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != adaptedSHA256 {
		t.Fatalf("historical migration entry v1 adaptation changed: got %s want %s", got, adaptedSHA256)
	}
}

func TestHistoricalMigrationEntryV1ProjectionShape(t *testing.T) {
	if err := validateHistoricalMigrationEntryV1Shape(historicalMigrationEntryV1StructFields, historicalMigrationEntryV1ShapeSHA256); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalMigrationEntryV1FutureFieldsAreIgnoredOnlyWhileZero(t *testing.T) {
	type released struct {
		ID string
	}
	type future struct {
		ID     string
		Future string
	}
	projection := map[reflect.Type][]string{
		reflect.TypeOf(released{}): {"ID"},
		reflect.TypeOf(future{}):   {"ID"},
	}
	encode := func(value any) ([]byte, error) {
		var out bytes.Buffer
		err := encodeHistoricalMigrationEntryV1Value(&out, reflect.ValueOf(value), projection)
		return out.Bytes(), err
	}
	want, err := encode(released{ID: "0001_initial"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := encode(future{ID: "0001_initial"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("future zero field changed released bytes: got %x want %x", got, want)
	}
	if _, err := encode(future{ID: "0001_initial", Future: "authority"}); err == nil {
		t.Fatal("future nonzero field entered the frozen migration-entry-v1 domain")
	}
}

func TestHistoricalMigrationEntryV1ReproducesReleasedSocialChains(t *testing.T) {
	for _, provider := range []string{"sqlite", "postgresql"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "social", "migrations", provider, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var manifest Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		for _, entry := range manifest.Entries {
			if got := ChainHash(entry); got == "" || got != entry.ChainHash {
				t.Fatalf("provider %s migration %s chain=%s want released %s", provider, entry.ID, got, entry.ChainHash)
			}
		}
	}
}

func TestHistoricalMigrationEntryV1RejectsCurrentOnlyPreV3SnapshotFacts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "social", "migrations", "sqlite", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	var entry ManifestEntry
	for _, candidate := range manifest.Entries {
		if candidate.AfterSnapshot.Version == 2 {
			entry = candidate
			break
		}
	}
	if entry.ID == "" || len(entry.AfterSnapshot.Tables) == 0 {
		t.Fatal("released physical-v2 entry with a table is absent")
	}
	field := ir.FieldID("ffffffffffffffffffffffffffffffff")
	entry.AfterSnapshot.Tables[0].OptimisticConcurrency = &field
	if got := ChainHash(entry); got != "" {
		t.Fatalf("invalid current-only pre-v3 fact produced chain hash %s", got)
	}
}

func TestHistoricalMigrationEntryV1CommitsValidV3ConcurrencyIdentity(t *testing.T) {
	v2, withConcurrency := optimisticConcurrencyUpgradeSchemas(t, ir.SQLite)
	withoutConcurrency := withConcurrency
	withoutConcurrency.Tables = append([]physical.PhysicalTable(nil), withConcurrency.Tables...)
	withoutConcurrency.Tables[0] = withConcurrency.Tables[0]
	withoutConcurrency.Tables[0].OptimisticConcurrency = nil
	var err error
	withoutConcurrency, err = physical.NormalizeHistoricalV3(withoutConcurrency)
	if err != nil {
		t.Fatal(err)
	}

	entry := ManifestEntry{ID: "0004_physical_v3", BeforeSnapshot: v2, AfterSnapshot: withoutConcurrency}
	withoutHash := ChainHash(entry)
	if withoutHash == "" {
		t.Fatal("valid mixed v2-to-v3 entry without concurrency identity failed to hash")
	}
	entry.AfterSnapshot = withConcurrency
	wantField := *withConcurrency.Tables[0].OptimisticConcurrency
	withHash := ChainHash(entry)
	if withHash == "" || withHash == withoutHash {
		t.Fatalf("v3 concurrency identity is not committed: without=%s with=%s", withoutHash, withHash)
	}
	if got := ChainHash(entry); got != withHash {
		t.Fatalf("stable v3 entry rehash=%s want=%s", got, withHash)
	}
	if entry.AfterSnapshot.Tables[0].OptimisticConcurrency == nil ||
		*entry.AfterSnapshot.Tables[0].OptimisticConcurrency != wantField {
		t.Fatal("chain hashing mutated the caller-owned v3 concurrency identity")
	}
}
