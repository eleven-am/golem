package postgresql

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

func TestPlanIncrementalRendersReviewedPostgreSQLBaseline(t *testing.T) {
	provider := New()
	before, err := provider.Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	before = postgresqlMigrationBefore(t, before)
	after := postgresqlMigrationAfter(t, before)
	entry := reviewedPostgreSQLEntry(t, "002_baseline", before, after, nil)
	plan, err := provider.PlanIncremental(entry)
	if err != nil {
		t.Fatal(err)
	}
	sql := plan.SQL()
	for _, fragment := range []string{
		`ALTER TABLE "reviewed"."posts" RENAME TO "posts_v2"`,
		`ALTER TABLE "reviewed"."posts_v2" RENAME COLUMN "title" TO "headline"`,
		`ALTER TABLE "reviewed"."posts_v2" ADD COLUMN "note" text`,
		`ALTER TABLE "reviewed"."posts_v2" ALTER COLUMN "amount" DROP NOT NULL`,
		`ALTER TABLE "reviewed"."posts_v2" ALTER COLUMN "amount" SET DEFAULT 1.0000`,
		`ALTER TABLE "reviewed"."posts_v2" ALTER COLUMN "day" DROP DEFAULT`,
		`ALTER TABLE "reviewed"."posts_v2" DROP COLUMN "metadata"`,
		`ALTER TABLE "reviewed"."posts_v2" ADD CONSTRAINT "uq_posts_headline" UNIQUE ("headline")`,
		`ALTER TABLE "reviewed"."posts_v2" DROP CONSTRAINT`,
		`ALTER TABLE "reviewed"."posts_v2" ADD CONSTRAINT "fk_posts_author" FOREIGN KEY`,
		`CREATE INDEX "idx_posts_headline" ON "reviewed"."posts_v2"`,
		`DROP INDEX "reviewed"."idx_posts_old"`,
		`ALTER INDEX "reviewed"."idx_posts_created" RENAME TO "idx_posts_created_v2"`,
		`CREATE TABLE "reviewed"."audit_log"`,
		`DROP TABLE "reviewed"."obsolete"`,
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("migration SQL missing %q:\n%s", fragment, sql)
		}
	}
	if strings.Contains(sql, `CREATE INDEX "reviewed".`) {
		t.Fatalf("CREATE INDEX name was illegally schema-qualified:\n%s", sql)
	}
	if strings.Contains(strings.ToLower(sql), "search_path") {
		t.Fatalf("migration SQL depends on search_path:\n%s", sql)
	}
}

func TestPlanIncrementalCreatesSemanticPgvectorStorage(t *testing.T) {
	provider := New()
	model := fixtureModel()
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 384, Fields: []string{id(29)}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	model.Extensions = append(model.Extensions, ir.ProviderExtensionIR{ID: ir.ExtensionID(id(73)), Provider: ir.PostgreSQL, Version: 1, Owner: ir.ObjectID(id(2)), Kind: semanticcontract.IndexKind, Payload: payload})
	after, err := provider.Lower(context.Background(), model, physical.LowerOptions{Namespace: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	before := after
	before.Extensions = nil
	before = normalizePostgreSQLMigrationSchema(t, before)
	entry := reviewedPostgreSQLEntry(t, "002_semantic", before, after, nil)
	plan, err := provider.PlanIncremental(entry)
	if err != nil {
		t.Fatal(err)
	}
	base := "_golem_semantic_" + id(73)
	for _, fragment := range []string{"CREATE EXTENSION IF NOT EXISTS vector", `CREATE TABLE "reviewed"."` + base + `_state"`, `CREATE TABLE "reviewed"."` + base + `_vec"`, `USING hnsw ("embedding" vector_cosine_ops)`} {
		if !strings.Contains(plan.SQL(), fragment) {
			t.Fatalf("semantic incremental SQL missing %q:\n%s", fragment, plan.SQL())
		}
	}
}

func TestPlanIncrementalRebuildsChangedSemanticPgvectorStorageInOrder(t *testing.T) {
	provider := New()
	model := fixtureModel()
	extensionID := ir.ExtensionID(id(73))
	setSemantic := func(dimensions uint16) {
		t.Helper()
		payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: dimensions, Fields: []string{id(29)}, Metric: "cosine"})
		if err != nil {
			t.Fatal(err)
		}
		model.Extensions = []ir.ProviderExtensionIR{{ID: extensionID, Provider: ir.PostgreSQL, Version: 1, Owner: ir.ObjectID(id(2)), Kind: semanticcontract.IndexKind, Payload: payload}}
	}
	setSemantic(3)
	before, err := provider.Lower(context.Background(), model, physical.LowerOptions{Namespace: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	setSemantic(4)
	after, err := provider.Lower(context.Background(), model, physical.LowerOptions{Namespace: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	entry := reviewedPostgreSQLEntry(t, "002_semantic_rewrite", before, after, nil)
	plan, err := provider.PlanIncremental(entry)
	if err != nil {
		t.Fatal(err)
	}
	sql := plan.SQL()
	base := "_golem_semantic_" + string(extensionID)
	drop := `DROP TABLE "reviewed"."` + base + `_vec"`
	create := `CREATE TABLE "reviewed"."` + base + `_vec"`
	dropPosition, createPosition := strings.Index(sql, drop), strings.Index(sql, create)
	if dropPosition < 0 || createPosition < 0 || dropPosition >= createPosition {
		t.Fatalf("semantic rewrite did not drop before recreate:\n%s", sql)
	}
	if !strings.Contains(sql[createPosition:], `vector(4)`) || !strings.Contains(sql[createPosition:], `USING hnsw ("embedding" vector_cosine_ops)`) {
		t.Fatalf("semantic rewrite omitted new pgvector contract:\n%s", sql)
	}
}

func TestSystemOutboxV1IncrementalPlanPostgreSQL(t *testing.T) {
	provider := New()
	after, err := provider.Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	legacyAfter := after
	legacyAfter.System.Objects = nil
	for _, object := range after.System.Objects {
		if object.Kind != physical.SystemOutboxDelivery {
			legacyAfter.System.Objects = append(legacyAfter.System.Objects, object)
		}
	}
	legacyAfter = normalizePostgreSQLMigrationSchema(t, legacyAfter)
	before := legacyAfter
	before.System.Objects = nil
	for _, object := range legacyAfter.System.Objects {
		if object.Kind != physical.SystemOutbox {
			before.System.Objects = append(before.System.Objects, object)
		}
	}
	before = normalizePostgreSQLMigrationSchema(t, before)
	entry := reviewedPostgreSQLEntry(t, "002_outbox_v1", before, legacyAfter, nil)
	plan, err := provider.PlanIncremental(entry)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`CREATE TABLE "_golem"."_golem_outbox"`, `"before_identity" bytea`, `"delete_snapshot" bytea`, `UNIQUE ("causation_id", "transaction_ordinal")`, `CREATE INDEX "_golem_outbox_pending"`} {
		if !strings.Contains(plan.SQL(), fragment) {
			t.Fatalf("outbox migration missing %q:\n%s", fragment, plan.SQL())
		}
	}
}

func TestSystemUpsertGuardV1IncrementalPlanPostgreSQLHasNoRelationDDL(t *testing.T) {
	provider := New()
	after, err := provider.Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	before := after
	before.System.Objects = nil
	for _, object := range after.System.Objects {
		if object.Kind != physical.SystemUpsertGuard {
			before.System.Objects = append(before.System.Objects, object)
		}
	}
	before = normalizePostgreSQLMigrationSchema(t, before)
	entry := reviewedPostgreSQLEntry(t, "003_upsert_guard_v1", before, after, nil)
	plan, err := provider.PlanIncremental(entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.SQL(), `_golem_upsert_guard`) {
		t.Fatalf("PostgreSQL upsert guard rendered a relation:\n%s", plan.SQL())
	}
}

func TestP7DeliverySystemObjectUpgradePlanPostgreSQL(t *testing.T) {
	provider := New()
	after, err := provider.Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	before := after
	before.System.Objects = nil
	for _, object := range after.System.Objects {
		if object.Kind != physical.SystemOutboxDelivery {
			before.System.Objects = append(before.System.Objects, object)
		}
	}
	before = normalizePostgreSQLMigrationSchema(t, before)
	entry := reviewedPostgreSQLEntry(t, "004_p7_delivery", before, after, nil)
	plan, err := provider.PlanIncremental(entry)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`CREATE TABLE "_golem"."_golem_outbox_delivery"`,
		`"first_recorded_at" timestamptz(6) NOT NULL`,
		`"attempt_count" bigint NOT NULL`,
		`"status" IN ('pending','leased','delivered','blocked','retired')`,
		`CREATE INDEX "_golem_outbox_delivery_pending"`,
		`INSERT INTO "_golem"."_golem_outbox_delivery"`,
		`FROM "_golem"."_golem_outbox" GROUP BY "causation_id" ON CONFLICT ("causation_id") DO NOTHING`,
	} {
		if !strings.Contains(plan.SQL(), fragment) {
			t.Fatalf("delivery migration missing %q:\n%s", fragment, plan.SQL())
		}
	}
}

func TestPlanIncrementalRefusesUnregisteredCastAndRequiredAdd(t *testing.T) {
	provider := New()
	base, err := provider.Lower(context.Background(), fixtureModel(), physical.LowerOptions{Namespace: "reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("cast", func(t *testing.T) {
		after, normalizeErr := physical.Normalize(base)
		if normalizeErr != nil {
			t.Fatal(normalizeErr)
		}
		posts := postgresqlTablePointer(&after, ir.ModelID(id(2)))
		for index := range posts.Columns {
			if posts.Columns[index].ID == ir.FieldID(id(23)) {
				posts.Columns[index].Storage = physical.StorageType{Kind: physical.StoragePostgreSQLDouble}
			}
		}
		after = normalizePostgreSQLMigrationSchema(t, after)
		entry := reviewedPostgreSQLEntry(t, "cast", base, after, nil)
		if _, err := provider.PlanIncremental(entry); err == nil || !strings.Contains(err.Error(), "AlterColumnType") && !strings.Contains(err.Error(), "alterColumnType") {
			t.Fatalf("error=%v; want unregistered cast refusal", err)
		}
	})
	t.Run("required add", func(t *testing.T) {
		after, normalizeErr := physical.Normalize(base)
		if normalizeErr != nil {
			t.Fatal(normalizeErr)
		}
		posts := postgresqlTablePointer(&after, ir.ModelID(id(2)))
		posts.Columns = append(posts.Columns, physical.PhysicalColumn{ID: ir.FieldID(id(901)), Name: "required", Ordinal: uint32(len(posts.Columns)), Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Nullable: false, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
		after = normalizePostgreSQLMigrationSchema(t, after)
		entry := reviewedPostgreSQLEntry(t, "required", base, after, nil)
		if _, err := provider.PlanIncremental(entry); err == nil || !strings.Contains(err.Error(), "no reviewed default") {
			t.Fatalf("error=%v; want required-add refusal", err)
		}
	})
}

func TestIdentityTransitionDropsExistingDefaultFirst(t *testing.T) {
	table := physical.PhysicalTable{ID: ir.ModelID(id(910)), Name: "items"}
	literal := ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: "7"}
	before := physical.PhysicalColumn{ID: ir.FieldID(id(911)), Name: "id", Storage: physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}, Default: physical.PhysicalDefault{Kind: physical.DefaultLiteral, Literal: &literal}}
	identityStorage := physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
	identity := physical.Expression{Kind: physical.ExpressionFunction, Type: identityStorage, Symbol: postgresSymbol("golem.postgresql.default.identity.v1", ir.SchemaSymbolFunction)}
	after := before
	after.Default = physical.PhysicalDefault{Kind: physical.DefaultProvider, Expression: &identity}
	renderer := ddlRenderer{schema: physical.PhysicalSchema{Namespace: physical.Namespace{Name: "reviewed"}}}
	statements, err := renderer.setColumnDefault(table, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(statements) != 2 || !strings.HasSuffix(statements[0], "DROP DEFAULT") || !strings.HasSuffix(statements[1], "ADD GENERATED BY DEFAULT AS IDENTITY") {
		t.Fatalf("identity transition statements=%#v", statements)
	}
}

func TestPostgreSQLMigrationArtifactMustEqualTypedRendering(t *testing.T) {
	entry := migration.ManifestEntry{ID: "002", Files: []migration.FileChecksum{{Path: "postgresql/002.sql"}, {Path: "postgresql/002.before.snapshot.json"}, {Path: "postgresql/002.after.snapshot.json"}}}
	files := map[string][]byte{"postgresql/002.sql": []byte("SELECT typed;\n")}
	if err := verifyPostgreSQLMigrationArtifact(entry, files, "SELECT typed;\n"); err != nil {
		t.Fatal(err)
	}
	files["postgresql/002.sql"] = []byte("SELECT decorative;\n")
	if err := verifyPostgreSQLMigrationArtifact(entry, files, "SELECT typed;\n"); err == nil {
		t.Fatal("decorative/tampered SQL artifact was accepted")
	}
	entry.Files = append(entry.Files, migration.FileChecksum{Path: "postgresql/duplicate.sql"})
	if err := verifyPostgreSQLMigrationArtifact(entry, files, "SELECT typed;\n"); err == nil {
		t.Fatal("multiple SQL artifacts were accepted")
	}
}

func TestPostgreSQLLedgerJSONRoundTripAndUnknownFieldRefusal(t *testing.T) {
	record := migration.LedgerEntry{MigrationID: "001", Files: []migration.FileChecksum{{Path: "postgresql/001.sql", SHA256: migration.Checksum([]byte("sql"))}}, Phases: []migration.LedgerPhase{{Ordinal: 0, Status: migration.PhaseApplied, AfterFingerprint: migration.Checksum([]byte("after"))}}}
	files, phases, err := encodePostgreSQLLedger(record)
	if err != nil {
		t.Fatal(err)
	}
	row := postgresqlLedgerRow{migrationID: "001", fileChecksums: files, phases: phases}
	decoded, err := decodePostgreSQLLedgerRow(row)
	if err != nil || len(decoded.Files) != 1 || len(decoded.Phases) != 1 {
		t.Fatalf("decoded=%#v error=%v", decoded, err)
	}
	row.fileChecksums = []byte(`[{"path":"postgresql/001.sql","sha256":"` + record.Files[0].SHA256 + `","extra":true}]`)
	if _, err := decodePostgreSQLLedgerRow(row); err == nil {
		t.Fatal("unknown ledger JSON field was accepted")
	}
}

func postgresqlMigrationBefore(t *testing.T, schema physical.PhysicalSchema) physical.PhysicalSchema {
	t.Helper()
	posts := postgresqlTablePointer(&schema, ir.ModelID(id(2)))
	day, ok := postgresqlColumn(*posts, ir.FieldID(id(24)))
	if !ok {
		t.Fatal("day field missing")
	}
	date := ir.TypedLiteralIR{Kind: ir.LiteralDate, Canonical: "2026-08-05"}
	for index := range posts.Columns {
		if posts.Columns[index].ID == day.ID {
			posts.Columns[index].Default = physical.PhysicalDefault{Kind: physical.DefaultLiteral, Literal: &date}
		}
	}
	extra := posts.Indexes[0]
	extra.ID = ir.IndexID(id(150))
	extra.Name = "idx_posts_old"
	posts.Indexes = append(posts.Indexes, extra)
	schema.Tables = append(schema.Tables, simplePostgreSQLTable(ir.ModelID(id(800)), ir.FieldID(id(801)), ir.KeyID(id(802)), "obsolete"))
	return normalizePostgreSQLMigrationSchema(t, schema)
}

func postgresqlMigrationAfter(t *testing.T, before physical.PhysicalSchema) physical.PhysicalSchema {
	t.Helper()
	after, err := physical.Normalize(before)
	if err != nil {
		t.Fatal(err)
	}
	filtered := after.Tables[:0]
	for _, table := range after.Tables {
		if table.ID != ir.ModelID(id(800)) {
			filtered = append(filtered, table)
		}
	}
	after.Tables = filtered
	posts := postgresqlTablePointer(&after, ir.ModelID(id(2)))
	posts.Name = "posts_v2"
	var columns []physical.PhysicalColumn
	for _, column := range posts.Columns {
		if column.ID == ir.FieldID(id(27)) {
			continue
		}
		if column.ID == ir.FieldID(id(29)) {
			column.Name = "headline"
		}
		if column.ID == ir.FieldID(id(23)) {
			value := ir.TypedLiteralIR{Kind: ir.LiteralDecimal, Canonical: "1.0000"}
			column.Nullable = true
			column.Default = physical.PhysicalDefault{Kind: physical.DefaultLiteral, Literal: &value}
		}
		if column.ID == ir.FieldID(id(24)) {
			column.Default = physical.PhysicalDefault{Kind: physical.DefaultNone}
		}
		column.Ordinal = uint32(len(columns))
		columns = append(columns, column)
	}
	columns = append(columns, physical.PhysicalColumn{ID: ir.FieldID(id(160)), Name: "note", Ordinal: uint32(len(columns)), Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
	posts.Columns = columns
	posts.Uniques = append(posts.Uniques, physical.PhysicalKey{ID: ir.KeyID(id(161)), Name: "uq_posts_headline", Columns: []ir.FieldID{ir.FieldID(id(29))}})
	posts.ForeignKeys[0].OnDelete = ir.ActionRestrict
	var plainCheck *physical.PhysicalCheck
	for index := range posts.Checks {
		if len(posts.Checks[index].RequiredCapabilities) == 0 {
			copy := posts.Checks[index]
			plainCheck = &copy
			posts.Checks = append(posts.Checks[:index], posts.Checks[index+1:]...)
			break
		}
	}
	if plainCheck == nil {
		t.Fatal("fixture has no portable check")
	}
	plainCheck.ID = ir.CheckID(id(162))
	plainCheck.Name = "ck_posts_reviewed"
	posts.Checks = append(posts.Checks, *plainCheck)
	var indexes []physical.PhysicalIndex
	for _, index := range posts.Indexes {
		if index.ID == ir.IndexID(id(150)) {
			continue
		}
		if index.ID == ir.IndexID(id(50)) {
			index.Name = "idx_posts_created_v2"
			indexes = append(indexes, index)
			created := index
			created.ID = ir.IndexID(id(163))
			created.Name = "idx_posts_headline"
			field := ir.FieldID(id(29))
			created.Keys = []physical.IndexKey{{Column: &field, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}
			indexes = append(indexes, created)
		}
	}
	posts.Indexes = indexes
	after.Tables = append(after.Tables, simplePostgreSQLTable(ir.ModelID(id(820)), ir.FieldID(id(821)), ir.KeyID(id(822)), "audit_log"))
	return normalizePostgreSQLMigrationSchema(t, after)
}

func simplePostgreSQLTable(tableID ir.ModelID, fieldID ir.FieldID, keyID ir.KeyID, name physical.PhysicalName) physical.PhysicalTable {
	return physical.PhysicalTable{ID: tableID, Name: name, Columns: []physical.PhysicalColumn{{ID: fieldID, Name: "id", Ordinal: 0, Storage: physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}, Nullable: false, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}}, PrimaryKey: &physical.PhysicalKey{ID: keyID, Name: physical.PhysicalName("pk_" + string(name)), Columns: []ir.FieldID{fieldID}}}
}

func postgresqlTablePointer(schema *physical.PhysicalSchema, id ir.ModelID) *physical.PhysicalTable {
	for index := range schema.Tables {
		if schema.Tables[index].ID == id {
			return &schema.Tables[index]
		}
	}
	panic("table missing: " + string(id))
}

func normalizePostgreSQLMigrationSchema(t *testing.T, schema physical.PhysicalSchema) physical.PhysicalSchema {
	t.Helper()
	normalized, err := physical.Normalize(schema)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func reviewedPostgreSQLEntry(t *testing.T, migrationID migration.MigrationID, before, after physical.PhysicalSchema, parent *migration.ManifestEntry) migration.ManifestEntry {
	t.Helper()
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	beforeFingerprint, _ := physical.PhysicalFingerprint(before)
	afterFingerprint, _ := physical.PhysicalFingerprint(after)
	allowlistFingerprint, _ := physical.UnmanagedAllowlistFingerprint(after)
	entry := migration.ManifestEntry{ID: migrationID, Operations: plan.Operations, Phases: plan.Phases, BeforeModel: migration.Checksum([]byte("model:" + beforeFingerprint.String())), AfterModel: migration.Checksum([]byte("model:" + afterFingerprint.String())), BeforePhysical: migration.Digest(beforeFingerprint.String()), AfterPhysical: migration.Digest(afterFingerprint.String()), BeforeSnapshot: before, AfterSnapshot: after, UnmanagedAllowlistDigest: migration.Digest(allowlistFingerprint.String())}
	if parent != nil {
		entry.ParentID = parent.ID
		entry.ParentChainHash = parent.ChainHash
		entry.BeforeModel = parent.AfterModel
	}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
		if operation.Risk == migration.RiskDataLoss || operation.Risk == migration.RiskManual {
			entry.Approvals = append(entry.Approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	return entry
}

func TestLiveReviewedPostgreSQLMigration(t *testing.T) {
	dsn := os.Getenv("GOLEM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GOLEM_TEST_POSTGRES_DSN is not set")
	}
	provider := New()
	database, _, err := provider.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	cleanup := func(namespace string) {
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS "` + namespace + `" CASCADE`)
		_, _ = database.Exec(`DROP SCHEMA IF EXISTS "_golem" CASCADE`)
	}

	t.Run("incremental rollback and tampered ledger", func(t *testing.T) {
		const namespace = "golem_p1_migrate_live"
		cleanup(namespace)
		defer cleanup(namespace)
		empty := canonicalEmptyPostgreSQLMigrationSchema(t, namespace)
		initial := livePostgreSQLMigrationSchema(t, namespace, false, false)
		first := reviewedPostgreSQLEntry(t, "001_initial", empty, initial, nil)
		first, firstFiles := finalizePostgreSQLEntry(t, provider, first)
		secondSchema := livePostgreSQLMigrationSchema(t, namespace, true, true)
		second := reviewedPostgreSQLEntry(t, "002_incremental", initial, secondSchema, &first)
		second, secondFiles := finalizePostgreSQLEntry(t, provider, second)
		files := mergePostgreSQLMigrationFiles(firstFiles, secondFiles)
		manifest := reviewedPostgreSQLManifest(provider, first, second)
		if err := provider.ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		if err := provider.ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("incremental: %v", err)
		}
		ledger, err := provider.ReadLedger(context.Background(), database)
		if err != nil || len(ledger) != 2 {
			t.Fatalf("ledger=%#v error=%v", ledger, err)
		}
		if err := migration.VerifyLedger(manifest, ledger); err != nil {
			t.Fatal(err)
		}
		var identity string
		if err := database.Get(&identity, `SELECT a.attidentity::text FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname='items' AND a.attname='id'`, namespace); err != nil || identity != "d" {
			t.Fatalf("identity=%q error=%v", identity, err)
		}
		if _, err := database.Exec(`INSERT INTO "` + namespace + `"."items" ("name") VALUES ('duplicate'),('duplicate')`); err != nil {
			t.Fatal(err)
		}
		uniqueSchema, normalizeErr := physical.Normalize(secondSchema)
		if normalizeErr != nil {
			t.Fatal(normalizeErr)
		}
		items := postgresqlTablePointer(&uniqueSchema, ir.ModelID(id(950)))
		items.Uniques = append(items.Uniques, physical.PhysicalKey{ID: ir.KeyID(id(954)), Name: "uq_items_name", Columns: []ir.FieldID{ir.FieldID(id(952))}})
		uniqueSchema = normalizePostgreSQLMigrationSchema(t, uniqueSchema)
		third := reviewedPostgreSQLEntry(t, "003_unique", secondSchema, uniqueSchema, &second)
		third, thirdFiles := finalizePostgreSQLEntry(t, provider, third)
		failedManifest := reviewedPostgreSQLManifest(provider, first, second, third)
		failedFiles := mergePostgreSQLMigrationFiles(files, thirdFiles)
		if err := provider.ApplyMigration(context.Background(), database, failedManifest, failedFiles); err == nil {
			t.Fatal("duplicate-data unique migration unexpectedly succeeded")
		}
		ledger, err = provider.ReadLedger(context.Background(), database)
		if err != nil || len(ledger) != 2 {
			t.Fatalf("failed migration advanced ledger=%#v error=%v", ledger, err)
		}
		if err := provider.Verify(context.Background(), database, secondSchema); err != nil {
			t.Fatalf("failed migration did not roll back schema: %v", err)
		}
		if _, err := database.Exec(`UPDATE "_golem"."_golem_migrations" SET parent_chain_hash='tampered' WHERE migration_id='001_initial'`); err != nil {
			t.Fatal(err)
		}
		if err := provider.ApplyMigration(context.Background(), database, failedManifest, failedFiles); err == nil {
			t.Fatal("tampered ledger was accepted")
		}
	})

	t.Run("concurrent next entry applies once", func(t *testing.T) {
		const namespace = "golem_p1_migrate_concurrent"
		cleanup(namespace)
		defer cleanup(namespace)
		empty := canonicalEmptyPostgreSQLMigrationSchema(t, namespace)
		initial := livePostgreSQLMigrationSchema(t, namespace, false, false)
		first := reviewedPostgreSQLEntry(t, "001_initial", empty, initial, nil)
		first, firstFiles := finalizePostgreSQLEntry(t, provider, first)
		secondSchema := livePostgreSQLMigrationSchema(t, namespace, true, true)
		second := reviewedPostgreSQLEntry(t, "002_incremental", initial, secondSchema, &first)
		second, secondFiles := finalizePostgreSQLEntry(t, provider, second)
		manifest := reviewedPostgreSQLManifest(provider, first, second)
		files := mergePostgreSQLMigrationFiles(firstFiles, secondFiles)
		if err := provider.ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		start := make(chan struct{})
		errors := make(chan error, 2)
		var wait sync.WaitGroup
		for runner := 0; runner < 2; runner++ {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				errors <- provider.ApplyMigration(context.Background(), database, manifest, files)
			}()
		}
		close(start)
		wait.Wait()
		close(errors)
		successes := 0
		for applyErr := range errors {
			if applyErr == nil {
				successes++
			} else if !strings.Contains(applyErr.Error(), "no unapplied reviewed entry") {
				t.Fatalf("unexpected concurrent result: %v", applyErr)
			}
		}
		if successes != 1 {
			t.Fatalf("successful concurrent runners=%d want=1", successes)
		}
		ledger, err := provider.ReadLedger(context.Background(), database)
		if err != nil || len(ledger) != 2 {
			t.Fatalf("ledger=%#v error=%v", ledger, err)
		}
	})

	t.Run("P6 to P7 delivery upgrade preserves and backfills facts", func(t *testing.T) {
		const namespace = "golem_p7_delivery_upgrade"
		cleanup(namespace)
		defer cleanup(namespace)
		empty := canonicalEmptyPostgreSQLMigrationSchema(t, namespace)
		p7 := livePostgreSQLMigrationSchema(t, namespace, false, false)
		p6 := p7
		p6.System.Objects = nil
		for _, object := range p7.System.Objects {
			if object.Kind != physical.SystemOutboxDelivery {
				p6.System.Objects = append(p6.System.Objects, object)
			}
		}
		p6 = normalizePostgreSQLMigrationSchema(t, p6)
		first := reviewedPostgreSQLEntry(t, "001_p6", empty, p6, nil)
		first, firstFiles := finalizePostgreSQLEntry(t, provider, first)
		second := reviewedPostgreSQLEntry(t, "002_p7_delivery", p6, p7, &first)
		second, secondFiles := finalizePostgreSQLEntry(t, provider, second)
		manifest := reviewedPostgreSQLManifest(provider, first, second)
		files := mergePostgreSQLMigrationFiles(firstFiles, secondFiles)
		if err := provider.ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("P6 bootstrap: %v", err)
		}
		const causation = "00000000-0000-4000-8000-000000000001"
		for ordinal, recorded := range []string{"2026-08-07T10:00:02Z", "2026-08-07T10:00:01Z"} {
			_, err := database.Exec(`INSERT INTO "_golem"."_golem_outbox" ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","after_identity","causation_id","transaction_ordinal","metadata","recorded_at") VALUES ($1,1,'golem.fact.v1','fingerprint','model','created',$2,$3,$4,$5,$6::timestamptz)`,
				fmt.Sprintf("00000000-0000-4000-8000-%012d", ordinal+1), []byte{1}, causation, ordinal+1, []byte{byte(ordinal + 1)}, recorded)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := provider.ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("P7 delivery upgrade: %v", err)
		}
		if err := provider.Verify(context.Background(), database, p7); err != nil {
			t.Fatal(err)
		}
		var status string
		var facts, deliveries int
		if err := database.Get(&facts, `SELECT count(*) FROM "_golem"."_golem_outbox"`); err != nil || facts != 2 {
			t.Fatalf("facts=%d error=%v", facts, err)
		}
		if err := database.Get(&deliveries, `SELECT count(*) FROM "_golem"."_golem_outbox_delivery"`); err != nil || deliveries != 1 {
			t.Fatalf("deliveries=%d error=%v", deliveries, err)
		}
		if err := database.Get(&status, `SELECT "status" FROM "_golem"."_golem_outbox_delivery" WHERE "causation_id"=$1`, causation); err != nil || status != "pending" {
			t.Fatalf("status=%q error=%v", status, err)
		}
	})
}

func canonicalEmptyPostgreSQLMigrationSchema(t *testing.T, namespace physical.PhysicalName) physical.PhysicalSchema {
	t.Helper()
	return normalizePostgreSQLMigrationSchema(t, physical.PhysicalSchema{Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion, Provider: New().Manifest(), Namespace: physical.Namespace{Name: namespace}})
}

func livePostgreSQLMigrationSchema(t *testing.T, namespace physical.PhysicalName, note, identity bool) physical.PhysicalSchema {
	t.Helper()
	idStorage := physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
	defaultValue := ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: "7"}
	idDefault := physical.PhysicalDefault{Kind: physical.DefaultLiteral, Literal: &defaultValue}
	if identity {
		expression := physical.Expression{Kind: physical.ExpressionFunction, Type: idStorage, Symbol: postgresSymbol("golem.postgresql.default.identity.v1", ir.SchemaSymbolFunction)}
		idDefault = physical.PhysicalDefault{Kind: physical.DefaultProvider, Expression: &expression}
	}
	table := physical.PhysicalTable{ID: ir.ModelID(id(950)), Name: "items", Columns: []physical.PhysicalColumn{
		{ID: ir.FieldID(id(951)), Name: "id", Ordinal: 0, Storage: idStorage, Nullable: false, Default: idDefault},
		{ID: ir.FieldID(id(952)), Name: "name", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
	}, PrimaryKey: &physical.PhysicalKey{ID: ir.KeyID(id(953)), Name: "pk_items", Columns: []ir.FieldID{ir.FieldID(id(951))}}}
	if note {
		table.Columns = append(table.Columns, physical.PhysicalColumn{ID: ir.FieldID(id(955)), Name: "note", Ordinal: 2, Storage: physical.StorageType{Kind: physical.StoragePostgreSQLText}, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
	}
	return normalizePostgreSQLMigrationSchema(t, physical.PhysicalSchema{Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion, Provider: New().Manifest(), Namespace: physical.Namespace{Name: namespace}, System: systemSchema(), Tables: []physical.PhysicalTable{table}})
}

func finalizePostgreSQLEntry(t *testing.T, provider *Provider, entry migration.ManifestEntry) (migration.ManifestEntry, map[string][]byte) {
	t.Helper()
	plan, err := migration.Diff(entry.BeforeSnapshot, entry.AfterSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	var sqlBytes []byte
	if plan.Initial {
		script, renderErr := provider.RenderInitial(entry.AfterSnapshot)
		if renderErr != nil {
			t.Fatal(renderErr)
		}
		sqlBytes = []byte(script.SQL())
	} else {
		rendered, renderErr := provider.PlanIncremental(entry)
		if renderErr != nil {
			t.Fatal(renderErr)
		}
		sqlBytes = []byte(rendered.SQL())
	}
	beforeBytes, err := json.Marshal(entry.BeforeSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, err := json.Marshal(entry.AfterSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	base := "migrations/postgresql/" + string(entry.ID)
	files := map[string][]byte{base + ".sql": sqlBytes, base + ".before.snapshot.json": beforeBytes, base + ".after.snapshot.json": afterBytes}
	entry.Files = []migration.FileChecksum{{Path: base + ".sql", SHA256: migration.Checksum(sqlBytes)}, {Path: base + ".before.snapshot.json", SHA256: migration.Checksum(beforeBytes)}, {Path: base + ".after.snapshot.json", SHA256: migration.Checksum(afterBytes)}}
	entry.ChainHash = migration.ChainHash(entry)
	return entry, files
}

func reviewedPostgreSQLManifest(provider *Provider, entries ...migration.ManifestEntry) migration.Manifest {
	manifestProvider := provider.Manifest()
	if len(entries) != 0 {
		manifestProvider = entries[0].BeforeSnapshot.Provider
	}
	return migration.Manifest{FormatVersion: migration.ManifestFormatVersion, CanonicalVersion: migration.ManifestCanonicalVersion, HashAlgorithm: "sha256", GeneratorVersion: "postgresql-live-test-v1", Provider: manifestProvider, Entries: entries}
}

func mergePostgreSQLMigrationFiles(groups ...map[string][]byte) map[string][]byte {
	result := map[string][]byte{}
	for _, group := range groups {
		for path, content := range group {
			result[path] = content
		}
	}
	return result
}
