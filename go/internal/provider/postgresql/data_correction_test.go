package postgresql

import (
	"context"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	fulltextpostgresql "github.com/eleven-am/golem/go/internal/fulltext/postgresql"
	fulltextstorage "github.com/eleven-am/golem/go/internal/fulltext/storage"
	"github.com/eleven-am/golem/go/internal/migration"
	migrationfailpoint "github.com/eleven-am/golem/go/internal/migration/failpoint"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func postgresqlCorrectionSchema(t *testing.T) physical.PhysicalSchema {
	t.Helper()
	schema := versionedPostgreSQLSchema(t, livePostgreSQLBackfillSchema(t, "correction_gate", false))
	schema.Version, schema.CanonicalVersion = physical.DataIncarnationFormatVersion, physical.DataIncarnationFormatVersion
	schema.System.Objects = append(schema.System.Objects, physical.DataIncarnationSystemObjectV1())
	field := ir.FieldID(id(961))
	schema.Tables[0].Columns = append(schema.Tables[0].Columns, physical.PhysicalColumn{ID: field, Name: "version", Ordinal: uint32(len(schema.Tables[0].Columns)), Storage: physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
	schema.Tables[0].OptimisticConcurrency = &field
	return normalizePostgreSQLMigrationSchema(t, schema)
}

func postgresqlCorrectionHistory(t *testing.T, command string, unmanaged ...physical.UnmanagedObject) (physical.PhysicalSchema, migration.Manifest, map[string][]byte) {
	t.Helper()
	schema := postgresqlCorrectionSchema(t)
	schema.Unmanaged = append(schema.Unmanaged, unmanaged...)
	schema = normalizePostgreSQLMigrationSchema(t, schema)
	return postgresqlCorrectionHistoryForSchema(t, schema, ir.FieldID(id(backfillTitle)), command)
}

func postgresqlCorrectionHistoryForSchema(t *testing.T, schema physical.PhysicalSchema, field ir.FieldID, command string) (physical.PhysicalSchema, migration.Manifest, map[string][]byte) {
	t.Helper()
	provider := New()
	command += "\n"
	empty := normalizePostgreSQLMigrationSchema(t, physical.PhysicalSchema{Version: schema.Version, CanonicalVersion: schema.CanonicalVersion, Provider: schema.Provider, Namespace: schema.Namespace})
	first := reviewedPostgreSQLEntry(t, "001_initial", empty, schema, nil)
	first, files := finalizePostgreSQLEntry(t, provider, first)
	plan, err := migration.DataCorrectionPlan(schema, field)
	if err != nil {
		t.Fatal(err)
	}
	entry := reviewedPostgreSQLEntry(t, "002_correction", schema, schema, &first)
	entry.BeforeModel, entry.AfterModel = first.AfterModel, first.AfterModel
	entry.Operations, entry.Phases = plan.Operations, plan.Phases
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
		entry.Approvals = append(entry.Approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
	}
	path := "migrations/postgresql/002_correction.manual.sql"
	entry.Manual = []migration.ManualCompanion{{OperationID: plan.Operations[0].ID, File: migration.FileChecksum{Path: path, SHA256: migration.Checksum([]byte(command))}, Postcondition: migration.DataCorrectionPostcondition(schema.Tables[0].ID, field)}}
	entry, more := finalizePostgreSQLEntry(t, provider, entry)
	files = mergePostgreSQLMigrationFiles(files, more)
	files[path] = []byte(command)
	return schema, reviewedPostgreSQLManifest(provider, first, entry), files
}

func TestPostgreSQLDataCorrectionChangesOnlyStoredChangesAndRotatesIdentity(t *testing.T) {
	schema, manifest, files := postgresqlCorrectionHistory(t, `UPDATE "correction_gate"."articles" SET "title" = 'fixed';`)
	ctx := context.Background()
	provider := New()
	database, _, err := provider.Open(ctx, testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO correction_gate.articles (id,title,golem_version,version) VALUES (1,'old',10,4),(2,'fixed',11,7)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`SELECT pg_catalog.setval('_golem.golem_row_version',11,true)`); err != nil {
		t.Fatal(err)
	}
	old, err := provider.ReadDataIncarnation(ctx, database, schema.System)
	if err != nil {
		t.Fatal(err)
	}
	// Cancellation after all data/version/identity work must roll it all back.
	canceled, cancel := context.WithCancel(ctx)
	reached := false
	fault := migrationfailpoint.WithHook(canceled, func(boundary string) {
		if boundary == "inside_transaction_before_ledger" {
			reached = true
			cancel()
		}
	})
	if err := provider.ApplyMigration(fault, database, manifest, files); err == nil || !reached {
		t.Fatalf("rollback fault reached=%v error=%v", reached, err)
	}
	cancel()
	actual, err := provider.ReadDataIncarnation(ctx, database, schema.System)
	if err != nil || actual != old {
		t.Fatalf("rollback identity=%v error=%v", actual, err)
	}
	var oldTitle string
	if err := database.Get(&oldTitle, `SELECT title FROM correction_gate.articles WHERE id=1`); err != nil || oldTitle != "old" {
		t.Fatalf("rollback title=%s error=%v", oldTitle, err)
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	actual, err = provider.ReadDataIncarnation(ctx, database, schema.System)
	if err != nil || !actual.Valid() || actual == old {
		t.Fatalf("correction identity=%v error=%v", actual, err)
	}
	var rows []struct {
		ID         int64  `db:"id"`
		Title      string `db:"title"`
		RowVersion int64  `db:"golem_version"`
		Version    int64  `db:"version"`
	}
	if err := database.Select(&rows, `SELECT id,title,golem_version,version FROM correction_gate.articles ORDER BY id`); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != 1 || rows[0].Title != "fixed" || rows[0].RowVersion <= 11 || rows[0].Version != 5 || rows[1].ID != 2 || rows[1].Title != "fixed" || rows[1].RowVersion != 11 || rows[1].Version != 7 {
		t.Fatalf("changed-row ownership violated: %+v", rows)
	}
	ledger, err := provider.ReadLedger(ctx, database)
	if err != nil || len(ledger) != 2 {
		t.Fatalf("ledger=%v error=%v", ledger, err)
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err == nil {
		t.Fatal("correction replayed")
	}
}

func TestPostgreSQLDataCorrectionRejectsAuthorityEscapeAndOverflow(t *testing.T) {
	for _, scenario := range []struct {
		name, command string
		overflow      bool
	}{
		{"other-column", `UPDATE "correction_gate"."articles" SET "id" = 99;`, false},
		{"multiple-columns", `UPDATE "correction_gate"."articles" SET "title" = 'fixed', "version" = 99;`, false},
		{"other-table", `UPDATE "_golem"."_golem_data_incarnation" SET "value" = '11111111111111111111111111111111';`, false},
		{"delete", `DELETE FROM "correction_gate"."articles";`, false},
		{"sequence-function", `UPDATE "correction_gate"."articles" SET "title" = pg_catalog.nextval('_golem.golem_row_version')::text;`, false},
		{"writable-cte", `WITH gone AS (DELETE FROM "correction_gate"."articles" RETURNING *) UPDATE "correction_gate"."articles" SET "title" = 'fixed';`, false},
		{"multiple-statements", `UPDATE "correction_gate"."articles" SET "title" = 'fixed'; DELETE FROM "correction_gate"."articles";`, false},
		{"concurrency-overflow", `UPDATE "correction_gate"."articles" SET "title" = 'fixed';`, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			schema, manifest, files := postgresqlCorrectionHistory(t, scenario.command)
			ctx := context.Background()
			provider := New()
			database, _, err := provider.Open(ctx, testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
				t.Fatal(err)
			}
			version := int64(4)
			if scenario.overflow {
				version = 9223372036854775807
			}
			if _, err := database.Exec(`INSERT INTO correction_gate.articles (id,title,golem_version,version) VALUES (1,'old',11,$1)`, version); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`SELECT pg_catalog.setval('_golem.golem_row_version',11,true)`); err != nil {
				t.Fatal(err)
			}
			old, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil {
				t.Fatal(err)
			}
			if err := provider.ApplyMigration(ctx, database, manifest, files); err == nil {
				t.Fatal("unsafe correction admitted")
			}
			actual, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil || actual != old {
				t.Fatalf("refusal changed identity: %v", err)
			}
			var intact int
			if err := database.Get(&intact, `SELECT count(*) FROM correction_gate.articles WHERE id=1 AND title='old' AND golem_version=11 AND version=$1`, version); err != nil || intact != 1 {
				t.Fatalf("refusal changed row: count=%d error=%v", intact, err)
			}
			ledger, err := provider.ReadLedger(ctx, database)
			if err != nil || len(ledger) != 1 {
				t.Fatalf("refusal changed ledger: %v %v", ledger, err)
			}
			if !scenario.overflow {
				var sequence int64
				if err := database.Get(&sequence, `SELECT last_value FROM _golem.golem_row_version`); err != nil || sequence != 11 {
					t.Fatalf("refused SQL executed sequence side effect: %d %v", sequence, err)
				}
			}
		})
	}
}

func TestPostgreSQLDataCorrectionRefusesInvalidVersionState(t *testing.T) {
	for _, scenario := range []struct {
		name                string
		rowVersion, version int64
	}{
		{"sequence-behind", 41, 4},
		{"zero-concurrency", 11, 0},
		{"negative-concurrency", 11, -1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			schema, manifest, files := postgresqlCorrectionHistory(t, `UPDATE "correction_gate"."articles" SET "title" = 'fixed';`)
			ctx := context.Background()
			provider := New()
			database, _, err := provider.Open(ctx, testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`INSERT INTO correction_gate.articles (id,title,golem_version,version) VALUES (1,'old',$1,$2)`, scenario.rowVersion, scenario.version); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`SELECT pg_catalog.setval('_golem.golem_row_version',11,true)`); err != nil {
				t.Fatal(err)
			}
			old, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil {
				t.Fatal(err)
			}
			if err := provider.ApplyMigration(ctx, database, manifest, files); err == nil {
				t.Fatal("invalid version state corrected instead of refused")
			}
			actual, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil || actual != old {
				t.Fatalf("refusal changed identity: %v", err)
			}
			var intact int
			if err := database.Get(&intact, `SELECT count(*) FROM correction_gate.articles WHERE id=1 AND title='old' AND golem_version=$1 AND version=$2`, scenario.rowVersion, scenario.version); err != nil || intact != 1 {
				t.Fatalf("refusal changed row: count=%d error=%v", intact, err)
			}
			ledger, err := provider.ReadLedger(ctx, database)
			if err != nil || len(ledger) != 1 {
				t.Fatalf("refusal changed ledger: %v %v", ledger, err)
			}
			var sequence int64
			if err := database.Get(&sequence, `SELECT last_value FROM _golem.golem_row_version`); err != nil || sequence != 11 {
				t.Fatalf("refusal consumed sequence: %d %v", sequence, err)
			}
		})
	}
}

func TestPostgreSQLDataCorrectionSequencePositionAndUnchangedRows(t *testing.T) {
	for _, scenario := range []struct {
		name                           string
		last, rowVersion, otherVersion int64
		called, noop, refuse           bool
	}{
		{"uncalled-next-is-ahead", 12, 11, 11, false, false, false},
		{"uncalled-next-repeats-retained", 11, 11, 11, false, false, true},
		{"unchanged-row-ahead", 11, 11, 41, true, false, true},
		{"exhausted-noop", 9223372036854775807, 9223372036854775807, 11, true, true, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			schema, manifest, files := postgresqlCorrectionHistory(t, `UPDATE "correction_gate"."articles" SET "title" = 'fixed';`)
			ctx := context.Background()
			provider := New()
			database, _, err := provider.Open(ctx, testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
				t.Fatal(err)
			}
			title := "old"
			if scenario.noop {
				title = "fixed"
			}
			if _, err := database.Exec(`INSERT INTO correction_gate.articles (id,title,golem_version,version) VALUES (1,$1,$2,4),(2,'fixed',$3,0)`, title, scenario.rowVersion, scenario.otherVersion); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`SELECT pg_catalog.setval('_golem.golem_row_version',$1,$2)`, scenario.last, scenario.called); err != nil {
				t.Fatal(err)
			}
			before, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil {
				t.Fatal(err)
			}
			err = provider.ApplyMigration(ctx, database, manifest, files)
			if (err != nil) != scenario.refuse {
				t.Fatalf("refuse=%v error=%v", scenario.refuse, err)
			}
			after, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil {
				t.Fatal(err)
			}
			if (after == before) != scenario.refuse {
				t.Fatalf("identity did not follow committed correction: before=%v after=%v", before, after)
			}
			var gotTitle string
			var version, concurrency int64
			if err := database.QueryRow(`SELECT title,golem_version,version FROM correction_gate.articles WHERE id=1`).Scan(&gotTitle, &version, &concurrency); err != nil {
				t.Fatal(err)
			}
			if scenario.refuse || scenario.noop {
				if gotTitle != title || version != scenario.rowVersion || concurrency != 4 {
					t.Fatalf("unchanged row drift: %q %d %d", gotTitle, version, concurrency)
				}
			} else if gotTitle != "fixed" || version != 12 || concurrency != 5 {
				t.Fatalf("next uncalled value not used exactly: %q %d %d", gotTitle, version, concurrency)
			}
			var unchanged int
			if err := database.Get(&unchanged, `SELECT count(*) FROM correction_gate.articles WHERE id=2 AND title='fixed' AND golem_version=$1 AND version=0`, scenario.otherVersion); err != nil || unchanged != 1 {
				t.Fatalf("unaffected concurrency state was changed: %d %v", unchanged, err)
			}
			if scenario.noop || scenario.refuse {
				var last int64
				var called bool
				if err := database.QueryRow(`SELECT last_value,is_called FROM _golem.golem_row_version`).Scan(&last, &called); err != nil || last != scenario.last || called != scenario.called {
					t.Fatalf("no draw expected: %d %v error=%v", last, called, err)
				}
			}
		})
	}
}

func TestPostgreSQLDataCorrectionRefusesAllowlistedTargetBehavior(t *testing.T) {
	for _, kind := range []string{"trigger", "rule"} {
		t.Run(kind, func(t *testing.T) {
			const behavior = "correction_external_behavior"
			schema, manifest, files := postgresqlCorrectionHistory(t, `UPDATE "correction_gate"."articles" SET "title" = 'fixed';`, physical.UnmanagedObject{Kind: kind, Name: behavior}, physical.UnmanagedObject{Kind: "table", Name: "correction_audit"})
			ctx := context.Background()
			provider := New()
			database, _, err := provider.Open(ctx, testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
				t.Fatal(err)
			}
			statements := []string{
				`INSERT INTO correction_gate.articles (id,title,golem_version,version) VALUES (1,'old',11,4)`,
				`SELECT pg_catalog.setval('_golem.golem_row_version',11,true)`,
				`CREATE TABLE correction_gate.correction_audit (id bigint PRIMARY KEY, value bigint NOT NULL)`,
				`INSERT INTO correction_gate.correction_audit VALUES (1,0)`,
			}
			if kind == "trigger" {
				statements = append(statements,
					`CREATE FUNCTION correction_gate.correction_external_function() RETURNS trigger LANGUAGE plpgsql AS $test$ BEGIN UPDATE correction_gate.correction_audit SET value=pg_catalog.nextval('_golem.golem_row_version') WHERE id=1; RETURN NEW; END; $test$`,
					`CREATE TRIGGER correction_external_behavior AFTER UPDATE OF title ON correction_gate.articles FOR EACH ROW EXECUTE FUNCTION correction_gate.correction_external_function()`,
				)
			} else {
				statements = append(statements, `CREATE RULE correction_external_behavior AS ON UPDATE TO correction_gate.articles DO ALSO UPDATE correction_gate.correction_audit SET value=pg_catalog.nextval('_golem.golem_row_version') WHERE id=1`)
			}
			for _, statement := range statements {
				if _, err := database.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			// This is explicitly reviewed unmanaged behavior: ordinary startup admits
			// it. Correction must impose its narrower one-field write authority.
			if _, err := provider.Introspect(ctx, database, schema); err != nil {
				t.Fatalf("allowlisted starting catalog rejected: %v", err)
			}
			before, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil {
				t.Fatal(err)
			}
			if err := provider.ApplyMigration(ctx, database, manifest, files); err == nil {
				t.Fatal("correction executed allowlisted external behavior")
			}
			after, err := provider.ReadDataIncarnation(ctx, database, schema.System)
			if err != nil || after != before {
				t.Fatalf("refusal changed identity: %v", err)
			}
			var intact int
			if err := database.Get(&intact, `SELECT count(*) FROM correction_gate.articles WHERE id=1 AND title='old' AND golem_version=11 AND version=4`); err != nil || intact != 1 {
				t.Fatalf("refusal changed target: %d %v", intact, err)
			}
			var value int64
			if err := database.Get(&value, `SELECT value FROM correction_gate.correction_audit WHERE id=1`); err != nil || value != 0 {
				t.Fatalf("refusal changed protected row: %d %v", value, err)
			}
			// Sequence draws are not rolled back, so this also proves refusal precedes
			// execution, rather than relying on a failed final catalog comparison.
			if err := database.Get(&value, `SELECT last_value FROM _golem.golem_row_version`); err != nil || value != 11 {
				t.Fatalf("behavior executed before refusal: %d %v", value, err)
			}
			ledger, err := provider.ReadLedger(ctx, database)
			if err != nil || len(ledger) != 1 {
				t.Fatalf("refusal changed ledger: %v %v", ledger, err)
			}
		})
	}
}

func TestPostgreSQLDataCorrectionPreservesProviderFullTextBehavior(t *testing.T) {
	// Use the real normalized full-text schema/extension owner, its generated
	// trigger functions, and the same phrase-query renderer as provider FTS tests.
	schema := versionedPostgreSQLSchema(t, normalizationFullTextSchema(t, "correction_gate", fulltextcontract.NormalizationNFCLower))
	schema.Version, schema.CanonicalVersion = physical.DataIncarnationFormatVersion, physical.DataIncarnationFormatVersion
	schema.System.Objects = append(schema.System.Objects, physical.DataIncarnationSystemObjectV1())
	schema = normalizePostgreSQLMigrationSchema(t, schema)
	schema, manifest, files := postgresqlCorrectionHistoryForSchema(t, schema, ir.FieldID(id(952)), `UPDATE "correction_gate"."items" SET "name" = 'corrected';`)
	ctx := context.Background()
	provider := New()
	database, _, err := provider.Open(ctx, testenv.DisposablePostgreSQL(t, testenv.PostgreSQLDSNVariable))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO correction_gate.items (id,name,golem_version) VALUES (1,'original',11)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`SELECT pg_catalog.setval('_golem.golem_row_version',11,true)`); err != nil {
		t.Fatal(err)
	}
	descriptor, err := fulltextstorage.Decode(schema.Extensions[0])
	if err != nil {
		t.Fatal(err)
	}
	names := postgresqlFullTextNames(descriptor)
	matches := func(query string) int {
		t.Helper()
		var count int
		if err := database.Get(&count, `SELECT count(*) FROM `+qualified(schema.Namespace.Name, names.table)+` WHERE "document" @@ `+fulltextpostgresql.PhraseQueryFor("$1", fulltextcontract.FoldingNone, fulltextcontract.NormalizationNFCLower, false), query); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if matches("original") != 1 || matches("corrected") != 0 {
		t.Fatal("initial FTS fixture did not index source")
	}
	before, err := provider.ReadDataIncarnation(ctx, database, schema.System)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatal(err)
	}
	after, err := provider.ReadDataIncarnation(ctx, database, schema.System)
	if err != nil || after == before {
		t.Fatalf("correction identity did not rotate: %v", err)
	}
	if matches("original") != 0 || matches("corrected") != 1 {
		t.Fatal("provider FTS triggers did not reconcile corrected field")
	}
	var version int64
	if err := database.Get(&version, `SELECT golem_version FROM correction_gate.items WHERE id=1`); err != nil || version != 12 {
		t.Fatalf("FTS correction version=%d error=%v", version, err)
	}
	if _, err := provider.Introspect(ctx, database, schema); err != nil {
		t.Fatalf("provider FTS catalog changed: %v", err)
	}
}
