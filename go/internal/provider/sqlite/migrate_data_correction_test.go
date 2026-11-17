package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	migrationfailpoint "github.com/eleven-am/golem/go/internal/migration/failpoint"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

func sqliteCorrectionFixture(t *testing.T, extra ...physical.PhysicalColumn) (*sqlx.DB, physical.PhysicalSchema, migration.Manifest, map[string][]byte) {
	t.Helper()
	schema, _ := sqliteRequiredBackfillSchemas(t)
	schema.Version = physical.DataIncarnationFormatVersion
	schema.CanonicalVersion = physical.DataIncarnationFormatVersion
	schema.System.Objects = append(schema.System.Objects, physical.DataIncarnationSystemObjectV1(), physical.OutboxSystemObjectV1(), physical.OutboxDeliverySystemObjectV1())
	concurrency := ir.FieldID("20000000000000000000000000000019")
	for index := range schema.Tables {
		if schema.Tables[index].ID == fixtureItemTable {
			table := &schema.Tables[index]
			table.Columns = append(table.Columns, physical.PhysicalColumn{ID: concurrency, Name: "version", Ordinal: uint32(len(table.Columns)), Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
			table.OptimisticConcurrency = &concurrency
			for _, column := range extra {
				column.Ordinal = uint32(len(table.Columns))
				table.Columns = append(table.Columns, column)
			}
		}
	}
	schema = normalizeMigrationFixture(t, schema)
	empty := normalizeMigrationFixture(t, physical.PhysicalSchema{
		Version: schema.Version, CanonicalVersion: schema.CanonicalVersion,
		Provider: schema.Provider, Namespace: schema.Namespace,
	})
	manifest, files := migrationFixtureManifest(t, empty, schema, "0001_initial.sql", nil)
	database, _, err := New().Open(context.Background(), filepath.Join(t.TempDir(), "correction.db"))
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = database.Close() })
	if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`INSERT INTO items(id,name,golem_version,version) VALUES(7,'retired',41,3),(9,'mailbox',57,8),(11,NULL,0,1)`, `INSERT INTO children(id,item_id) VALUES(1,7)`, `UPDATE golem_row_version SET value=57`} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	insertDeliveryFact(t, database, deliveryUUID(1), deliveryUUID(2), 1, 1)
	return database, schema, manifest, files
}

func sqliteCorrectionHistory(t *testing.T, schema physical.PhysicalSchema, manifest migration.Manifest, files map[string][]byte, reviewed string, fields ...ir.FieldID) (migration.Manifest, map[string][]byte) {
	t.Helper()
	field := fixtureItemNameField
	if len(fields) != 0 {
		field = fields[0]
	}
	plan, err := migration.DataCorrectionPlan(schema, field)
	if err != nil {
		t.Fatal(err)
	}
	previous := manifest.Entries[len(manifest.Entries)-1]
	entry := migration.ManifestEntry{ID: "0002_correct_name", ParentID: previous.ID, ParentChainHash: previous.ChainHash, BeforeModel: previous.AfterModel, AfterModel: previous.AfterModel, BeforePhysical: previous.AfterPhysical, AfterPhysical: previous.AfterPhysical, BeforeSnapshot: schema, AfterSnapshot: schema, UnmanagedAllowlistDigest: previous.UnmanagedAllowlistDigest, Operations: plan.Operations, Phases: plan.Phases}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
		if migration.RequiresApproval(operation) {
			entry.Approvals = append(entry.Approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	entry.Manual = []migration.ManualCompanion{{OperationID: plan.Operations[0].ID, File: migration.FileChecksum{Path: "0002_correct_name.data.sql", SHA256: migration.Checksum([]byte(reviewed))}, Postcondition: migration.DataCorrectionPostcondition(fixtureItemTable, field)}}
	script, err := New().RenderMigration(entry)
	if err != nil {
		t.Fatal(err)
	}
	entry.Files = []migration.FileChecksum{{Path: "0002_correct_name.sql", SHA256: migration.Checksum([]byte(script.SQL()))}}
	entry.ChainHash = migration.ChainHash(entry)
	manifest.Entries = append(manifest.Entries, entry)
	files[entry.Files[0].Path] = []byte(script.SQL())
	files[entry.Manual[0].File.Path] = []byte(reviewed)
	if err := migration.VerifyManifest(manifest, files); err != nil {
		t.Fatal(err)
	}
	return manifest, files
}

func TestSQLiteDataCorrectionChangesOnlyActualRowsAtomically(t *testing.T) {
	database, schema, manifest, files := sqliteCorrectionFixture(t)
	ctx := context.Background()
	provider := New()
	before, err := provider.ReadDataIncarnation(ctx, database, schema.System)
	if err != nil {
		t.Fatal(err)
	}
	var factsBefore []sqliteFactRow
	if err := database.Select(&factsBefore, `SELECT * FROM _golem_outbox`); err != nil {
		t.Fatal(err)
	}
	manifest, files = sqliteCorrectionHistory(t, schema, manifest, files, "UPDATE items SET name=CASE WHEN id=7 THEN 'corrected' WHEN id=11 THEN 'filled' ELSE name END;\n")
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID           int
		Name         string
		GolemVersion int64 `db:"golem_version"`
		Version      int64
	}
	if err := database.Select(&rows, `SELECT id,name,golem_version,version FROM items ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Name != "corrected" || rows[0].GolemVersion != 58 || rows[0].Version != 4 || rows[1].Name != "mailbox" || rows[1].GolemVersion != 57 || rows[1].Version != 8 || rows[2].Name != "filled" || rows[2].GolemVersion != 59 || rows[2].Version != 2 {
		t.Fatalf("incorrect row/version changes: %#v", rows)
	}
	var counter int64
	if err := database.Get(&counter, `SELECT value FROM golem_row_version`); err != nil || counter != 59 {
		t.Fatalf("counter=%d %v", counter, err)
	}
	after, err := provider.ReadDataIncarnation(ctx, database, schema.System)
	if err != nil || after == before {
		t.Fatalf("incarnation not rotated: %v", err)
	}
	if err := provider.Verify(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	assertSQLiteBackfillFTS(t, database, "retired", 0)
	assertSQLiteBackfillFTS(t, database, "corrected", 1)
	assertSQLiteBackfillFTS(t, database, "mailbox", 1)
	var child int
	if err := database.Get(&child, `SELECT item_id FROM children WHERE id=1`); err != nil || child != 7 {
		t.Fatalf("child=%d %v", child, err)
	}
	var factsAfter []sqliteFactRow
	if err := database.Select(&factsAfter, `SELECT * FROM _golem_outbox`); err != nil || !reflect.DeepEqual(factsBefore, factsAfter) {
		t.Fatal("correction changed or emitted immutable facts", err)
	}
	ledger, err := provider.ReadLedger(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.VerifyLedger(manifest, ledger); err != nil {
		t.Fatal(err)
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err == nil {
		t.Fatal("applied correction replayed")
	}
}

func TestSQLiteDataCorrectionRefusalAndCancellationRollback(t *testing.T) {
	for _, scenario := range []string{"wrong-column", "version", "counter", "identity", "commit", "multiple", "returning", "cancel-before-ledger", "cancel-before-commit", "counter-behind", "counter-overflow", "concurrency-overflow", "tamper"} {
		t.Run(scenario, func(t *testing.T) {
			database, schema, manifest, files := sqliteCorrectionFixture(t)
			ctx := context.Background()
			provider := New()
			sql := "UPDATE items SET name='changed';\n"
			switch scenario {
			case "wrong-column":
				sql = "UPDATE items SET id=id+100;\n"
			case "version":
				sql = "UPDATE items SET golem_version=99;\n"
			case "counter":
				sql = "UPDATE golem_row_version SET value=0;\n"
			case "identity":
				sql = "UPDATE _golem_data_incarnation SET value='12345678901234567890123456789012';\n"
			case "commit":
				sql = "COMMIT;\n"
			case "multiple":
				sql = "UPDATE items SET name='changed'; COMMIT;\n"
			case "returning":
				sql = "UPDATE items SET name='changed' RETURNING id;\n"
			case "counter-behind":
				_, _ = database.Exec(`UPDATE golem_row_version SET value=1`)
			case "counter-overflow":
				_, _ = database.Exec(`UPDATE golem_row_version SET value=9223372036854775807`)
			case "concurrency-overflow":
				_, _ = database.Exec(`UPDATE items SET version=9223372036854775807 WHERE id=7`)
			}
			manifest, files = sqliteCorrectionHistory(t, schema, manifest, files, sql)
			if scenario == "tamper" {
				files["0002_correct_name.data.sql"] = []byte("UPDATE items SET name='tampered';\n")
			}
			before, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil {
				t.Fatal(err)
			}
			var versionsBefore []int64
			if err := database.Select(&versionsBefore, `SELECT golem_version FROM items ORDER BY id`); err != nil {
				t.Fatal(err)
			}
			var counterBefore int64
			if err := database.Get(&counterBefore, `SELECT value FROM golem_row_version`); err != nil {
				t.Fatal(err)
			}
			canceled, cancel := context.WithCancel(ctx)
			defer cancel()
			reached := false
			applyCtx := ctx
			if strings.HasPrefix(scenario, "cancel-") {
				applyCtx = migrationfailpoint.WithHook(canceled, func(boundary string) {
					if scenario == "cancel-before-ledger" && boundary == "inside_transaction_before_ledger" || scenario == "cancel-before-commit" && boundary == "inside_transaction_before_commit" {
						reached = true
						cancel()
					}
				})
			}
			err = provider.ApplyMigration(applyCtx, database, manifest, files)
			if err == nil {
				t.Fatal("invalid correction succeeded")
			}
			if strings.HasPrefix(scenario, "cancel-") && (!reached || !errors.Is(err, context.Canceled)) {
				t.Fatalf("cancel boundary reached=%t err=%v", reached, err)
			}
			var names []string
			if err := database.Select(&names, `SELECT coalesce(name,'<NULL>') FROM items ORDER BY id`); err != nil || !reflect.DeepEqual(names, []string{"retired", "mailbox", "<NULL>"}) {
				t.Fatalf("data escaped rollback %v %v", names, err)
			}
			var versions []int64
			if err := database.Select(&versions, `SELECT golem_version FROM items ORDER BY id`); err != nil || !reflect.DeepEqual(versions, versionsBefore) {
				t.Fatal("versions escaped rollback", err)
			}
			var counter int64
			if err := database.Get(&counter, `SELECT value FROM golem_row_version`); err != nil || counter != counterBefore {
				t.Fatal("counter escaped rollback", err)
			}
			after, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil || after != before {
				t.Fatal("identity escaped rollback", err)
			}
			ledger, err := provider.ReadLedger(ctx, database)
			if err != nil || len(ledger) != 1 {
				t.Fatal("ledger escaped rollback", err)
			}
			var residue int
			if err := database.Get(&residue, `SELECT count(*) FROM sqlite_temp_schema WHERE name LIKE '_golem_data_correction_%'`); err != nil || residue != 0 {
				t.Fatalf("temporary state survived rollback: %d %v", residue, err)
			}
			if _, err := database.Exec(`UPDATE items SET name='retired' WHERE id=7`); err != nil {
				t.Fatalf("native authorizer escaped rollback: %v", err)
			}
			assertSQLiteBackfillFTS(t, database, "retired", 1)
		})
	}
}

func TestSQLiteDataCorrectionNoopStillRotatesIdentity(t *testing.T) {
	for _, sql := range []string{"UPDATE items SET name=name;\n", "UPDATE items SET name='unused' WHERE id=-1;\n"} {
		t.Run(sql, func(t *testing.T) {
			database, schema, manifest, files := sqliteCorrectionFixture(t)
			before, err := New().ReadDataIncarnation(context.Background(), database, schema.System)
			if err != nil {
				t.Fatal(err)
			}
			manifest, files = sqliteCorrectionHistory(t, schema, manifest, files, sql)
			if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
				t.Fatal(err)
			}
			var versions []int64
			if err := database.Select(&versions, `SELECT golem_version FROM items ORDER BY id`); err != nil || !reflect.DeepEqual(versions, []int64{41, 57, 0}) {
				t.Fatalf("noop changed versions: %v %v", versions, err)
			}
			var concurrency []int64
			if err := database.Select(&concurrency, `SELECT version FROM items ORDER BY id`); err != nil || !reflect.DeepEqual(concurrency, []int64{3, 8, 1}) {
				t.Fatalf("noop changed concurrency: %v %v", concurrency, err)
			}
			var counter int64
			if err := database.Get(&counter, `SELECT value FROM golem_row_version`); err != nil || counter != 57 {
				t.Fatalf("noop changed counter: %d %v", counter, err)
			}
			after, err := New().ReadDataIncarnation(context.Background(), database, schema.System)
			if err != nil || after == before {
				t.Fatalf("noop did not rotate: %v", err)
			}
			if err := New().ApplyMigration(context.Background(), database, manifest, files); err == nil {
				t.Fatal("noop entry replayed")
			}
			stable, err := New().ReadDataIncarnation(context.Background(), database, schema.System)
			if err != nil || stable != after {
				t.Fatal("replay rotated identity", err)
			}
		})
	}
}

func TestSQLiteDataCorrectionTracksRealValueChanges(t *testing.T) {
	const field ir.FieldID = "20000000000000000000000000000027"
	database, schema, manifest, files := sqliteCorrectionFixture(t, physical.PhysicalColumn{ID: field, Name: "measurement", Storage: physical.StorageType{Kind: physical.StorageSQLiteReal}, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
	const before, after = 1.0000000000000002, 1.0000000000000004
	if _, err := database.Exec(`UPDATE items SET measurement=? WHERE id=7`, before); err != nil {
		t.Fatal(err)
	}
	var distinct bool
	if err := database.QueryRow(`SELECT ? IS NOT ?`, before, after).Scan(&distinct); err != nil || !distinct {
		t.Fatalf("fixture REAL values must be distinct: distinct=%t err=%v", distinct, err)
	}
	manifest, files = sqliteCorrectionHistory(t, schema, manifest, files, "UPDATE items SET measurement=1.0000000000000004 WHERE id=7;\n", field)
	if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
		t.Fatal(err)
	}
	var actual float64
	var version, concurrency, counter int64
	if err := database.QueryRow(`SELECT measurement,golem_version,version FROM items WHERE id=7`).Scan(&actual, &version, &concurrency); err != nil {
		t.Fatal(err)
	}
	if actual != after || version != 58 || concurrency != 4 {
		t.Fatalf("REAL correction lost version advancement: value=%.17g version=%d concurrency=%d", actual, version, concurrency)
	}
	if err := database.Get(&counter, `SELECT value FROM golem_row_version`); err != nil || counter != 58 {
		t.Fatalf("REAL correction counter=%d err=%v", counter, err)
	}
}

func TestSQLiteDataCorrectionRefreshesStoredGeneratedDependent(t *testing.T) {
	source := fixtureItemNameField
	database, schema, manifest, files := sqliteCorrectionFixture(t, physical.PhysicalColumn{ID: "20000000000000000000000000000028", Name: "name_copy", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}, Generated: &physical.GeneratedExpression{Kind: physical.GeneratedStored, Expression: physical.Expression{Kind: physical.ExpressionColumn, Type: physical.StorageType{Kind: physical.StorageSQLiteText}, Nullable: true, Column: &source}}})
	manifest, files = sqliteCorrectionHistory(t, schema, manifest, files, "UPDATE items SET name='generated-change' WHERE id=7;\n")
	if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
		t.Fatal(err)
	}
	var name, derived string
	var version, concurrency int64
	if err := database.QueryRow(`SELECT name,name_copy,golem_version,version FROM items WHERE id=7`).Scan(&name, &derived, &version, &concurrency); err != nil {
		t.Fatal(err)
	}
	if name != "generated-change" || derived != name || version != 58 || concurrency != 4 {
		t.Fatalf("stored generation/version drift: %q %q %d %d", name, derived, version, concurrency)
	}
}
