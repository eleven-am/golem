package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

func TestRuntimeRequiresExactReviewedMigrationLedgerPostgreSQL(t *testing.T) {
	profiles := []struct{ name, environment string }{
		{name: "c", environment: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "linguistic", environment: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	}
	ran := false
	for profileIndex, profile := range profiles {
		dsn := strings.TrimSpace(os.Getenv(profile.environment))
		if dsn == "" {
			continue
		}
		ran = true
		t.Run(profile.name, func(t *testing.T) {
			configuration, err := pgx.ParseConfig(dsn)
			if err != nil {
				t.Fatal(err)
			}
			database := sqlx.NewDb(sql.OpenDB(stdlib.GetConnector(*configuration)), "pgx")
			defer database.Close()
			namespace := fmt.Sprintf("golem_p8_ledger_%d_%d_%d", os.Getpid(), profileIndex, time.Now().UnixNano())
			qualifiedNamespace := pgx.Identifier{namespace}.Sanitize()
			if _, err := database.ExecContext(context.Background(), "CREATE SCHEMA "+qualifiedNamespace); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := database.ExecContext(context.Background(), "DROP SCHEMA "+qualifiedNamespace+" CASCADE"); err != nil {
					t.Errorf("drop isolated ledger schema: %v", err)
				}
			}()
			createLedger := "CREATE TABLE " + qualifiedNamespace + `."_golem_migrations" (
				migration_id text PRIMARY KEY, parent_chain_hash text NOT NULL, chain_hash text NOT NULL,
				file_checksums jsonb NOT NULL, before_physical_fingerprint text NOT NULL,
				after_physical_fingerprint text NOT NULL, phases jsonb NOT NULL, applied_at timestamptz NOT NULL
			)`

			hash := func(character string) migration.Digest { return migration.Digest(strings.Repeat(character, 64)) }
			first := migration.ManifestEntry{ID: "0001_initial", ChainHash: hash("1"), Files: []migration.FileChecksum{{Path: "postgresql/0001.sql", SHA256: hash("a")}}, BeforePhysical: hash("2"), AfterPhysical: hash("3"), Phases: []migration.Phase{{Ordinal: 0, AfterFingerprint: hash("3")}}}
			second := migration.ManifestEntry{ID: "0002_post", ParentID: first.ID, ParentChainHash: first.ChainHash, ChainHash: hash("4"), Files: []migration.FileChecksum{{Path: "postgresql/0002.sql", SHA256: hash("b")}}, BeforePhysical: first.AfterPhysical, AfterPhysical: hash("5"), Phases: []migration.Phase{{Ordinal: 0, AfterFingerprint: hash("5")}}}
			manifest := migration.Manifest{Entries: []migration.ManifestEntry{first, second}}
			system := physical.SystemSchema{Namespace: physical.Namespace{Name: physical.PhysicalName(namespace)}, Objects: []physical.SystemObject{{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"}}}
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
				{name: "incomplete", rows: []migration.LedgerEntry{ledgerRecord(first, migration.PhaseApplied), ledgerRecord(second, migration.PhaseFailed)}},
			}
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					if _, err := database.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+qualifiedNamespace+`."_golem_migrations"`); err != nil {
						t.Fatal(err)
					}
					if !test.noTable {
						if _, err := database.ExecContext(context.Background(), createLedger); err != nil {
							t.Fatal(err)
						}
						installPostgreSQLLedgerRows(t, database, namespace, test.rows)
					}
					err := verifyReviewedMigrationLedger(context.Background(), database, golem.PostgreSQL, system, &reviewedMigrationStartup{manifest: manifest})
					if test.ok && err != nil {
						t.Fatalf("exact ledger rejected: %v", err)
					}
					if !test.ok && (err == nil || !strings.Contains(err.Error(), "P8_RUNTIME_MIGRATION")) {
						t.Fatalf("invalid ledger error = %v", err)
					}
				})
			}
		})
	}
	if !ran {
		t.Skip("PostgreSQL profile DSNs are not configured")
	}
}

func installPostgreSQLLedgerRows(t *testing.T, database *sqlx.DB, namespace string, rows []migration.LedgerEntry) {
	t.Helper()
	statement := "INSERT INTO " + pgx.Identifier{namespace}.Sanitize() + `."_golem_migrations" (migration_id,parent_chain_hash,chain_hash,file_checksums,before_physical_fingerprint,after_physical_fingerprint,phases,applied_at) VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7::jsonb,$8)`
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
			phaseValues[index] = struct {
				Ordinal          uint32                `json:"ordinal"`
				Status           migration.PhaseStatus `json:"status"`
				AfterFingerprint migration.Digest      `json:"afterFingerprint"`
			}{Ordinal: phase.Ordinal, Status: phase.Status, AfterFingerprint: phase.AfterFingerprint}
		}
		phases, err := json.Marshal(phaseValues)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(context.Background(), statement, row.MigrationID, row.ParentChainHash, row.ChainHash, files, row.BeforePhysical, row.AfterPhysical, phases, time.Unix(1, 0).UTC()); err != nil {
			t.Fatal(err)
		}
	}
}
