package postgresql

import (
	"context"
	"testing"

	migrationfailpoint "github.com/eleven-am/golem/go/internal/migration/failpoint"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func incarnationPostgreSQLSchema(t *testing.T, note bool) physical.PhysicalSchema {
	schema := livePostgreSQLMigrationSchema(t, "incarnation_gate", note, false)
	schema.Version, schema.CanonicalVersion = physical.DataIncarnationFormatVersion, physical.DataIncarnationFormatVersion
	schema.System.Objects = append(schema.System.Objects, physical.DataIncarnationSystemObjectV1())
	return normalizePostgreSQLMigrationSchema(t, schema)
}

func TestPostgreSQLDataIncarnationReviewedInitializationRollbackAndPreservation(t *testing.T) {
	for _, additive := range []bool{false, true} {
		name := "initial"
		if additive {
			name = "additive"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			provider := New()
			after := incarnationPostgreSQLSchema(t, false)
			dsn := testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable)
			database, _, err := provider.Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			before := normalizePostgreSQLMigrationSchema(t, physical.PhysicalSchema{Version: after.Version, CanonicalVersion: after.CanonicalVersion, Provider: after.Provider, Namespace: after.Namespace})
			first := reviewedPostgreSQLEntry(t, "001_initial", before, after, nil)
			if additive {
				plain := livePostgreSQLMigrationSchema(t, after.Namespace.Name, false, false)
				first = reviewedPostgreSQLEntry(t, "001_initial", canonicalEmptyPostgreSQLMigrationSchema(t, after.Namespace.Name), plain, nil)
			}
			first, files := finalizePostgreSQLEntry(t, provider, first)
			manifest := reviewedPostgreSQLManifest(provider, first)
			if additive {
				if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
					t.Fatal(err)
				}
				second := reviewedPostgreSQLEntry(t, "002_incarnation", first.AfterSnapshot, after, &first)
				second, secondFiles := finalizePostgreSQLEntry(t, provider, second)
				manifest.Entries = append(manifest.Entries, second)
				files = mergePostgreSQLMigrationFiles(files, secondFiles)
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
			var absent bool
			if err := database.Get(&absent, `SELECT to_regclass('_golem._golem_data_incarnation') IS NULL`); err != nil || !absent {
				t.Fatalf("partial singleton table survived: %v %v", absent, err)
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
			later := incarnationPostgreSQLSchema(t, true)
			parent := manifest.Entries[len(manifest.Entries)-1]
			next := reviewedPostgreSQLEntry(t, "003_note", after, later, &parent)
			next, nextFiles := finalizePostgreSQLEntry(t, provider, next)
			manifest.Entries = append(manifest.Entries, next)
			files = mergePostgreSQLMigrationFiles(files, nextFiles)
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
			if _, err := database.Exec(`ALTER TABLE _golem._golem_data_incarnation DROP CONSTRAINT _golem_data_incarnation_value_check`); err != nil {
				t.Fatal(err)
			}
			for _, expression := range []string{`'ABCDEF0123456789abcdef0123456789'`, `repeat('a',1048576)`} {
				if _, err := database.Exec(`UPDATE _golem._golem_data_incarnation SET value=` + expression); err != nil {
					t.Fatal(err)
				}
				if _, err := provider.ReadDataIncarnation(ctx, database, later.System); err == nil {
					t.Fatalf("noncanonical stored identity accepted: %s", expression)
				}
			}
			if _, err := database.Exec(`DELETE FROM _golem._golem_data_incarnation`); err != nil {
				t.Fatal(err)
			}
			if _, err := provider.ReadDataIncarnation(ctx, database, later.System); err == nil {
				t.Fatal("missing singleton accepted or repaired")
			}
			var count int
			if err := database.Get(&count, `SELECT count(*) FROM _golem._golem_data_incarnation`); err != nil || count != 0 {
				t.Fatal("read manufactured an identity")
			}
		})
	}
}
