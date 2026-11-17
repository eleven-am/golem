package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/eleven-am/golem/go/internal/migration"
	migrationfailpoint "github.com/eleven-am/golem/go/internal/migration/failpoint"
	"github.com/eleven-am/golem/go/internal/physical"
)

func incarnationFixtureSchema(t *testing.T, note bool) physical.PhysicalSchema {
	schema := incrementalFixtureSchema(t, note)
	schema.Version, schema.CanonicalVersion = physical.DataIncarnationFormatVersion, physical.DataIncarnationFormatVersion
	schema.System.Objects = append(schema.System.Objects, physical.DataIncarnationSystemObjectV1())
	return normalizeMigrationFixture(t, schema)
}

func TestSQLiteDataIncarnationReviewedInitializationRollbackAndPreservation(t *testing.T) {
	for _, additive := range []bool{false, true} {
		name := "initial"
		if additive {
			name = "additive"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			provider := New()
			after := incarnationFixtureSchema(t, false)
			database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "incarnation.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			database.SetMaxOpenConns(1)
			database.SetMaxIdleConns(1)
			before := normalizeMigrationFixture(t, physical.PhysicalSchema{Version: after.Version, CanonicalVersion: after.CanonicalVersion, Provider: after.Provider, Namespace: after.Namespace})
			var manifest migration.Manifest
			var files map[string][]byte
			if additive {
				plain := incrementalFixtureSchema(t, false)
				manifest, files = migrationFixtureManifest(t, canonicalEmptyMigrationSchema(t, plain), plain, "001_initial.sql", nil)
				if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
					t.Fatal(err)
				}
				before = plain
				next, nextFiles := migrationFixtureManifest(t, before, after, "002_incarnation.sql", &manifest.Entries[0])
				manifest.Entries = append(manifest.Entries, next.Entries[0])
				for path, data := range nextFiles {
					files[path] = data
				}
			} else {
				manifest, files = migrationFixtureManifest(t, before, after, "001_initial.sql", nil)
			}
			canceled, cancel := context.WithCancel(ctx)
			reached := false
			fault := migrationfailpoint.WithHook(canceled, func(boundary string) {
				if boundary == "inside_transaction_before_ledger" {
					reached = true
					cancel()
				}
			})
			err = provider.ApplyMigration(fault, database, manifest, files)
			cancel()
			if err == nil || !reached {
				t.Fatalf("fault reached=%v error=%v", reached, err)
			}
			var count int
			if err := database.Get(&count, `SELECT count(*) FROM sqlite_schema WHERE name='_golem_data_incarnation'`); err != nil || count != 0 {
				t.Fatalf("partial singleton table survived: %d %v", count, err)
			}
			if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
				t.Fatal(err)
			}
			value, err := provider.ReadDataIncarnation(ctx, database, after.System)
			if err != nil || !value.Valid() {
				t.Fatalf("identity=%v error=%v", value, err)
			}
			if err := provider.ApplyMigration(ctx, database, manifest, files); err == nil {
				t.Fatal("reapplied initialized migration")
			}
			later := incarnationFixtureSchema(t, true)
			parent := manifest.Entries[len(manifest.Entries)-1]
			next, nextFiles := migrationFixtureManifest(t, after, later, "003_note.sql", &parent)
			manifest.Entries = append(manifest.Entries, next.Entries[0])
			for path, data := range nextFiles {
				files[path] = data
			}
			if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
				t.Fatal(err)
			}
			unchanged, err := provider.ReadDataIncarnation(ctx, database, later.System)
			if err != nil || unchanged != value {
				t.Fatalf("ordinary migration changed identity: %v", err)
			}
			if _, err := provider.Introspect(ctx, database, later); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
				t.Fatal(err)
			}
			for _, expression := range []string{
				`'ABCDEF0123456789abcdef0123456789'`,
				`CAST('abcdef0123456789abcdef0123456789' AS BLOB)`,
				`replace(hex(zeroblob(524288)),'0','a')`,
			} {
				if _, err := database.Exec(`UPDATE _golem_data_incarnation SET value=` + expression); err != nil {
					t.Fatal(err)
				}
				if _, err := provider.ReadDataIncarnation(ctx, database, later.System); err == nil {
					t.Fatalf("noncanonical stored identity accepted: %s", expression)
				}
			}
			if _, err := database.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`DELETE FROM _golem_data_incarnation`); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.ReadDataIncarnation(ctx, database, later.System); err == nil {
				t.Fatal("missing singleton accepted or repaired")
			}
			if err := database.Get(&count, `SELECT count(*) FROM _golem_data_incarnation`); err != nil || count != 0 {
				t.Fatal("read manufactured an identity")
			}
		})
	}
}
