package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

func TestP8RuntimeRequiresExactReviewedMigrationLedgerSQLite(t *testing.T) {
	hash := func(character string) migration.Digest { return migration.Digest(strings.Repeat(character, 64)) }
	first := migration.ManifestEntry{
		ID: "0001_initial", ChainHash: hash("1"), Files: []migration.FileChecksum{{Path: "sqlite/0001.sql", SHA256: hash("a")}},
		BeforePhysical: hash("2"), AfterPhysical: hash("3"), Phases: []migration.Phase{{Ordinal: 0, AfterFingerprint: hash("3")}},
	}
	second := migration.ManifestEntry{
		ID: "0002_post", ParentID: first.ID, ParentChainHash: first.ChainHash, ChainHash: hash("4"), Files: []migration.FileChecksum{{Path: "sqlite/0002.sql", SHA256: hash("b")}},
		BeforePhysical: first.AfterPhysical, AfterPhysical: hash("5"), Phases: []migration.Phase{{Ordinal: 0, AfterFingerprint: hash("5")}},
	}
	manifest := migration.Manifest{Entries: []migration.ManifestEntry{first, second}}
	system := physical.SystemSchema{Namespace: physical.Namespace{Name: "main"}, Objects: []physical.SystemObject{{Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"}}}

	tests := []struct {
		name    string
		rows    []migration.LedgerEntry
		noTable bool
		ok      bool
	}{
		{name: "valid exact", rows: []migration.LedgerEntry{ledgerRecord(first, migration.PhaseApplied), ledgerRecord(second, migration.PhaseApplied)}, ok: true},
		{name: "missing", noTable: true},
		{name: "shorter", rows: []migration.LedgerEntry{ledgerRecord(first, migration.PhaseApplied)}},
		{name: "longer ahead", rows: []migration.LedgerEntry{ledgerRecord(first, migration.PhaseApplied), ledgerRecord(second, migration.PhaseApplied), {MigrationID: "0003_ahead", ParentChainHash: second.ChainHash, ChainHash: hash("6"), BeforePhysical: second.AfterPhysical, AfterPhysical: hash("7")}}},
		{name: "rewritten", rows: func() []migration.LedgerEntry {
			rows := []migration.LedgerEntry{ledgerRecord(first, migration.PhaseApplied), ledgerRecord(second, migration.PhaseApplied)}
			rows[1].Files[0].SHA256 = hash("c")
			return rows
		}()},
		{name: "reordered", rows: func() []migration.LedgerEntry {
			rows := []migration.LedgerEntry{ledgerRecord(first, migration.PhaseApplied), ledgerRecord(second, migration.PhaseApplied)}
			rows[0].ChainHash, rows[1].ChainHash = rows[1].ChainHash, rows[0].ChainHash
			return rows
		}()},
		{name: "incomplete", rows: []migration.LedgerEntry{ledgerRecord(first, migration.PhaseApplied), ledgerRecord(second, migration.PhasePending)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := sqlx.MustOpen("sqlite", "file:"+strings.ReplaceAll(test.name, " ", "_")+"?mode=memory&cache=shared")
			defer database.Close()
			if !test.noTable {
				installLedgerRows(t, database, test.rows)
			}
			err := verifyReviewedMigrationLedger(context.Background(), database, golem.SQLite, system, &reviewedMigrationStartup{manifest: manifest})
			if test.ok && err != nil {
				t.Fatalf("exact ledger rejected: %v", err)
			}
			if !test.ok && (err == nil || !strings.Contains(err.Error(), "P8_RUNTIME_MIGRATION")) {
				t.Fatalf("invalid ledger error = %v", err)
			}
		})
	}
}

func TestP8ReviewedMigrationPreflightRejectsMissingEmptyAndForeignBeforeDatabaseWork(t *testing.T) {
	expected, err := physical.Normalize(physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: physical.SQLiteManifest(), Namespace: physical.Namespace{Name: "main"},
		System: physical.SystemSchema{Version: 1, Namespace: physical.Namespace{Name: "main"}, Objects: []physical.SystemObject{{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	generation := golem.SchemaDigest{1}
	model := golem.GeneratedSchemaDocument(1, 1, golem.SchemaDigest{2}, []byte("model"))
	contract := golem.GeneratedSchemaDocument(1, 1, golem.SchemaDigest{3}, []byte("contract"))
	physicalDocument := golem.GeneratedSchemaDocument(1, 1, golem.SchemaDigest{4}, []byte("physical"))
	systemFingerprint, err := physical.SystemFingerprint(expected.Provider, expected.System)
	if err != nil {
		t.Fatal(err)
	}
	var systemDigest golem.SchemaDigest
	decodedSystem, _ := hex.DecodeString(systemFingerprint.String())
	copy(systemDigest[:], decodedSystem)
	missing := golem.GeneratedSchemaBundle(generation, "generator", "p8-go-abi-v5", model, contract,
		golem.GeneratedProviderSchemaDocument(golem.SQLite, systemDigest, physicalDocument))
	if _, err := prepareReviewedMigrationStartup(nil, missing, golem.SQLite, expected); err == nil || !strings.Contains(err.Error(), "manifest is missing") {
		t.Fatalf("missing preflight error = %v", err)
	}

	emptyManifest := migration.Manifest{
		FormatVersion: migration.ManifestFormatVersion, CanonicalVersion: migration.ManifestCanonicalVersion,
		HashAlgorithm: "sha256", GeneratorVersion: "test", Provider: expected.Provider,
	}
	emptyBytes, err := migration.EncodeManifest(emptyManifest, map[string][]byte{})
	if err != nil {
		t.Fatal(err)
	}
	emptyDocument := golem.GeneratedMigrationManifestDocument(generation, golem.SQLite, emptyBytes)
	empty := golem.GeneratedSchemaBundle(generation, "generator", "p8-go-abi-v5", model, contract,
		golem.GeneratedProviderSchemaDocumentWithMigration(golem.SQLite, systemDigest, physicalDocument, emptyDocument))
	if _, err := prepareReviewedMigrationStartup(nil, empty, golem.SQLite, expected); err == nil || !strings.Contains(err.Error(), "manifest is empty") {
		t.Fatalf("empty preflight error = %v", err)
	}

	foreignDocument := golem.GeneratedMigrationManifestDocument(golem.SchemaDigest{9}, golem.SQLite, emptyBytes)
	foreign := golem.GeneratedSchemaBundle(generation, "generator", "p8-go-abi-v5", model, contract,
		golem.GeneratedProviderSchemaDocumentWithMigration(golem.SQLite, systemDigest, physicalDocument, foreignDocument))
	if _, err := prepareReviewedMigrationStartup(nil, foreign, golem.SQLite, expected); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("foreign preflight error = %v", err)
	}
}

func ledgerRecord(entry migration.ManifestEntry, status migration.PhaseStatus) migration.LedgerEntry {
	result := migration.LedgerEntry{
		MigrationID: entry.ID, ParentChainHash: entry.ParentChainHash, ChainHash: entry.ChainHash,
		Files: append([]migration.FileChecksum(nil), entry.Files...), BeforePhysical: entry.BeforePhysical,
		AfterPhysical: entry.AfterPhysical, AppliedAt: time.Unix(1, 0).UTC(),
	}
	for _, phase := range entry.Phases {
		result.Phases = append(result.Phases, migration.LedgerPhase{Ordinal: phase.Ordinal, Status: status, AfterFingerprint: phase.AfterFingerprint})
	}
	return result
}

func installLedgerRows(t *testing.T, database *sqlx.DB, rows []migration.LedgerEntry) {
	t.Helper()
	database.MustExec(`CREATE TABLE _golem_migrations (
		migration_id TEXT PRIMARY KEY, parent_chain_hash TEXT NOT NULL, chain_hash TEXT NOT NULL,
		file_checksums TEXT NOT NULL, before_physical_fingerprint TEXT NOT NULL,
		after_physical_fingerprint TEXT NOT NULL, phases TEXT NOT NULL, applied_at TEXT NOT NULL
	)`)
	for _, row := range rows {
		files, err := json.Marshal(row.Files)
		if err != nil {
			t.Fatal(err)
		}
		phaseValues := make([]struct {
			Ordinal          uint32                `json:"ordinal"`
			Status           migration.PhaseStatus `json:"status"`
			AfterFingerprint migration.Digest      `json:"afterFingerprint"`
		}, len(row.Phases))
		for index, phase := range row.Phases {
			phaseValues[index].Ordinal = phase.Ordinal
			phaseValues[index].Status = phase.Status
			phaseValues[index].AfterFingerprint = phase.AfterFingerprint
		}
		phases, err := json.Marshal(phaseValues)
		if err != nil {
			t.Fatal(err)
		}
		database.MustExec(`INSERT INTO _golem_migrations VALUES (?,?,?,?,?,?,?,?)`, row.MigrationID, row.ParentChainHash, row.ChainHash, string(files), row.BeforePhysical, row.AfterPhysical, string(phases), row.AppliedAt.Format(time.RFC3339Nano))
	}
}
