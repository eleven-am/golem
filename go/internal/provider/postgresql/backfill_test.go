package postgresql

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	migrationfailpoint "github.com/eleven-am/golem/go/internal/migration/failpoint"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

const (
	backfillModel   = 980
	backfillID      = 981
	backfillTitle   = 982
	backfillSlug    = 983
	backfillPrimary = 984
)

func livePostgreSQLBackfillSchema(t *testing.T, namespace physical.PhysicalName, slug bool) physical.PhysicalSchema {
	t.Helper()
	text := physical.StorageType{Kind: physical.StoragePostgreSQLText}
	none := physical.PhysicalDefault{Kind: physical.DefaultNone}
	articles := physical.PhysicalTable{
		ID: ir.ModelID(id(backfillModel)), Name: "articles",
		Columns: []physical.PhysicalColumn{
			{ID: ir.FieldID(id(backfillID)), Name: "id", Ordinal: 0, Storage: physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}, Default: none},
			{ID: ir.FieldID(id(backfillTitle)), Name: "title", Ordinal: 1, Storage: text, Default: none},
		},
		PrimaryKey: &physical.PhysicalKey{ID: ir.KeyID(id(backfillPrimary)), Name: "pk_articles", Columns: []ir.FieldID{ir.FieldID(id(backfillID))}},
	}
	if slug {
		articles.Columns = append(articles.Columns, physical.PhysicalColumn{ID: ir.FieldID(id(backfillSlug)), Name: "slug", Ordinal: 2, Storage: text, Default: none})
	}
	return normalizePostgreSQLMigrationSchema(t, physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: New().Manifest(), Namespace: physical.Namespace{Name: namespace}, System: systemSchema(),
		Tables: []physical.PhysicalTable{articles},
	})
}

func postgresqlBackfillOperation(t *testing.T, entry migration.ManifestEntry) migration.Operation {
	t.Helper()
	for _, operation := range entry.Operations {
		if operation.Kind == migration.BackfillColumn {
			return operation
		}
	}
	t.Fatal("entry contains no backfill operation")
	return migration.Operation{}
}

func sealPostgreSQLBackfillEntry(t *testing.T, entry migration.ManifestEntry, reviewed []byte) (migration.ManifestEntry, map[string][]byte) {
	t.Helper()
	operation := postgresqlBackfillOperation(t, entry)
	owner, resolved := migration.BackfillOwner(entry.AfterSnapshot, ir.FieldID(operation.ObjectID))
	if !resolved {
		t.Fatal("backfill target model is unresolvable")
	}
	companionPath := "migrations/postgresql/" + string(entry.ID) + ".backfill." + string(operation.ID) + ".sql"
	entry.Manual = []migration.ManualCompanion{{
		OperationID:   operation.ID,
		File:          migration.FileChecksum{Path: companionPath, SHA256: migration.Checksum(reviewed)},
		Postcondition: migration.BackfillPostcondition(owner, ir.FieldID(operation.ObjectID)),
	}}
	entry, files := finalizePostgreSQLEntry(t, New(), entry)
	files[companionPath] = reviewed
	return entry, files
}

func postgresqlBackfillHistory(t *testing.T, namespace physical.PhysicalName, reviewed []byte) (physical.PhysicalSchema, physical.PhysicalSchema, migration.Manifest, map[string][]byte) {
	t.Helper()
	empty := canonicalEmptyPostgreSQLMigrationSchema(t, namespace)
	before := livePostgreSQLBackfillSchema(t, namespace, false)
	after := livePostgreSQLBackfillSchema(t, namespace, true)
	first := reviewedPostgreSQLEntry(t, "001_initial", empty, before, nil)
	first, firstFiles := finalizePostgreSQLEntry(t, New(), first)
	second := reviewedPostgreSQLEntry(t, "002_reviewed_backfill", before, after, &first)
	second, secondFiles := sealPostgreSQLBackfillEntry(t, second, reviewed)
	return before, after, reviewedPostgreSQLManifest(New(), first, second), mergePostgreSQLMigrationFiles(firstFiles, secondFiles)
}

func postgresqlReviewedSlugBackfill(namespace physical.PhysicalName) []byte {
	return []byte(fmt.Sprintf("UPDATE %q.%q\nSET %q = lower(%q)\nWHERE %q IS NULL;\n", namespace, "articles", "slug", "title", "slug"))
}

func insertPostgreSQLArticles(t *testing.T, database *sqlx.DB, namespace physical.PhysicalName, titles ...string) {
	t.Helper()
	for index, title := range titles {
		if _, err := database.Exec(fmt.Sprintf(`INSERT INTO %q.%q ("id","title") VALUES ($1,$2)`, namespace, "articles"), index+1, title); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgreSQLReviewedBackfillAddsFillsValidatesAndRequiresColumn(t *testing.T) {
	forEachPostgreSQLProfile(t, func(t *testing.T, profile string, database *sqlx.DB) {
		namespace := physical.PhysicalName("golem_backfill_apply_" + profile)
		dropPostgreSQLWideningNamespace(t, database, namespace)
		defer dropPostgreSQLWideningNamespace(t, database, namespace)
		_, after, manifest, files := postgresqlBackfillHistory(t, namespace, postgresqlReviewedSlugBackfill(namespace))
		if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		insertPostgreSQLArticles(t, database, namespace, "First Post", "SECOND Post", "Third")
		sql := string(files["migrations/postgresql/002_reviewed_backfill.sql"])
		for _, fragment := range []string{
			`ALTER TABLE "` + string(namespace) + `"."articles" ADD COLUMN "slug" text;`,
			"-- golem reviewed backfill companion ",
			"-- golem generated postcondition ",
			`ALTER TABLE "` + string(namespace) + `"."articles" ALTER COLUMN "slug" SET NOT NULL;`,
		} {
			if !strings.Contains(sql, fragment) {
				t.Fatalf("reviewed artifact missing %q:\n%s", fragment, sql)
			}
		}
		if strings.Contains(sql, "UPDATE") || strings.Contains(sql, "lower(") {
			t.Fatalf("reviewed artifact copied the companion SQL:\n%s", sql)
		}
		if strings.Index(sql, "ADD COLUMN") > strings.Index(sql, "-- golem reviewed backfill") ||
			strings.Index(sql, "-- golem reviewed backfill") > strings.Index(sql, "-- golem generated postcondition") ||
			strings.Index(sql, "-- golem generated postcondition") > strings.Index(sql, "SET NOT NULL") {
			t.Fatalf("reviewed artifact order is not add/backfill/validate/set-not-null:\n%s", sql)
		}
		if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("backfill: %v", err)
		}
		if err := New().Verify(context.Background(), database, after); err != nil {
			t.Fatalf("backfilled schema does not match the reviewed after snapshot: %v", err)
		}
		var slugs []string
		if err := database.Select(&slugs, fmt.Sprintf(`SELECT "slug" FROM %q.%q ORDER BY "id"`, namespace, "articles")); err != nil {
			t.Fatal(err)
		}
		expected := []string{"first post", "second post", "third"}
		if len(slugs) != len(expected) {
			t.Fatalf("slugs=%#v want=%#v", slugs, expected)
		}
		for index := range expected {
			if slugs[index] != expected[index] {
				t.Fatalf("slug %d=%q want=%q", index, slugs[index], expected[index])
			}
		}
		var notNull bool
		if err := database.Get(&notNull, `SELECT a.attnotnull FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname='articles' AND a.attname='slug'`, string(namespace)); err != nil || !notNull {
			t.Fatalf("slug notnull=%v error=%v", notNull, err)
		}
		if _, err := database.Exec(fmt.Sprintf(`INSERT INTO %q.%q ("id","title") VALUES (99,'no slug')`, namespace, "articles")); err == nil {
			t.Fatal("required column accepted a NULL after the reviewed backfill")
		}
		ledger, err := New().ReadLedger(context.Background(), database)
		if err != nil || len(ledger) != 2 {
			t.Fatalf("ledger=%d error=%v", len(ledger), err)
		}
		if err := migration.VerifyLedger(manifest, ledger); err != nil {
			t.Fatal(err)
		}
		if err := New().ApplyMigration(context.Background(), database, manifest, files); err == nil || !strings.Contains(err.Error(), "no unapplied reviewed entry") {
			t.Fatalf("reapplying a completed backfill error=%v; want an idempotent no-op refusal", err)
		}
	})
}

func TestPostgreSQLReviewedBackfillRejectsTamperMultipleStatementsAndWrongTarget(t *testing.T) {
	namespace := physical.PhysicalName("reviewed")
	before := livePostgreSQLBackfillSchema(t, namespace, false)
	after := livePostgreSQLBackfillSchema(t, namespace, true)
	base := reviewedPostgreSQLEntry(t, "002_reviewed_backfill", before, after, nil)

	t.Run("missing companion", func(t *testing.T) {
		if _, err := New().PlanIncremental(base); err == nil || !strings.Contains(err.Error(), "has no reviewed companion") {
			t.Fatalf("error=%v; want a missing-companion refusal", err)
		}
	})

	t.Run("missing backfill approval", func(t *testing.T) {
		entry, files := sealPostgreSQLBackfillEntry(t, base, postgresqlReviewedSlugBackfill(namespace))
		operation := postgresqlBackfillOperation(t, entry)
		var retained []migration.Approval
		sealed := false
		for _, approval := range entry.Approvals {
			if approval.OperationID == operation.ID {
				sealed = true
				if approval.Risk != operation.Risk || approval.Before != operation.Before || approval.After != operation.After {
					t.Fatal("the backfill approval is not exact and object-scoped")
				}
				continue
			}
			retained = append(retained, approval)
		}
		if !sealed {
			t.Fatal("a reviewed backfill was sealed without an exact object-scoped approval")
		}
		entry.Approvals = retained
		entry.ChainHash = migration.ChainHash(entry)
		if _, err := New().PlanIncremental(entry); err == nil || !strings.Contains(err.Error(), "requires exact object-scoped approval") {
			t.Fatalf("error=%v; want a missing-approval refusal", err)
		}
		manifest := reviewedPostgreSQLManifest(New(), entry)
		manifest.Entries[0].BeforeModel = migration.Checksum([]byte("before"))
		manifest.Entries[0].AfterModel = migration.Checksum([]byte("after"))
		manifest.Entries[0].ChainHash = migration.ChainHash(manifest.Entries[0])
		if err := migration.VerifyManifest(manifest, files); err == nil || !strings.Contains(err.Error(), "approval inventory is inconsistent") {
			t.Fatalf("error=%v; want reviewed history to refuse an unapproved backfill", err)
		}
	})

	t.Run("wrong postcondition", func(t *testing.T) {
		entry, _ := sealPostgreSQLBackfillEntry(t, base, postgresqlReviewedSlugBackfill(namespace))
		entry.Manual[0].Postcondition = migration.Checksum([]byte("not the generated postcondition"))
		if _, err := New().PlanIncremental(entry); err == nil || !strings.Contains(err.Error(), "generated no-NULL postcondition") {
			t.Fatalf("error=%v; want a postcondition refusal", err)
		}
	})

	t.Run("wrong target field", func(t *testing.T) {
		entry, _ := sealPostgreSQLBackfillEntry(t, base, postgresqlReviewedSlugBackfill(namespace))
		entry.Manual[0].Postcondition = migration.BackfillPostcondition(ir.ModelID(id(backfillModel)), ir.FieldID(id(backfillTitle)))
		if _, err := New().PlanIncremental(entry); err == nil || !strings.Contains(err.Error(), "generated no-NULL postcondition") {
			t.Fatalf("error=%v; want a retargeted-postcondition refusal", err)
		}
	})

	t.Run("companion bound to a non-backfill operation", func(t *testing.T) {
		entry, _ := sealPostgreSQLBackfillEntry(t, base, postgresqlReviewedSlugBackfill(namespace))
		for _, operation := range entry.Operations {
			if operation.Kind == migration.AddColumn {
				entry.Manual[0].OperationID = operation.ID
			}
		}
		if _, err := New().PlanIncremental(entry); err == nil || !strings.Contains(err.Error(), "does not bind exactly one reviewed backfill") {
			t.Fatalf("error=%v; want a companion-binding refusal", err)
		}
	})

	t.Run("manifest refuses an unbound or duplicated companion", func(t *testing.T) {
		entry, files := sealPostgreSQLBackfillEntry(t, base, postgresqlReviewedSlugBackfill(namespace))
		manifest := reviewedPostgreSQLManifest(New(), entry)
		manifest.Entries[0].ParentChainHash = ""
		manifest.Entries[0].BeforeModel = migration.Checksum([]byte("before"))
		manifest.Entries[0].AfterModel = migration.Checksum([]byte("after"))
		manifest.Entries[0].ChainHash = migration.ChainHash(manifest.Entries[0])
		if err := migration.VerifyManifest(manifest, files); err != nil {
			t.Fatalf("sealed backfill history was rejected: %v", err)
		}
		stripped := manifest
		stripped.Entries = []migration.ManifestEntry{manifest.Entries[0]}
		stripped.Entries[0].Manual = nil
		stripped.Entries[0].ChainHash = migration.ChainHash(stripped.Entries[0])
		if err := migration.VerifyManifest(stripped, files); err == nil {
			t.Fatal("history without a backfill companion was accepted")
		}
		doubled := manifest
		doubled.Entries = []migration.ManifestEntry{manifest.Entries[0]}
		doubled.Entries[0].Manual = append(append([]migration.ManualCompanion(nil), manifest.Entries[0].Manual...), manifest.Entries[0].Manual[0])
		doubled.Entries[0].ChainHash = migration.ChainHash(doubled.Entries[0])
		if err := migration.VerifyManifest(doubled, files); err == nil {
			t.Fatal("history with two companions for one backfill was accepted")
		}
	})

	artifacts := []struct {
		name     string
		reviewed []byte
		want     string
	}{
		{"crlf endings", []byte("UPDATE \"reviewed\".\"articles\" SET \"slug\" = 'x' WHERE \"slug\" IS NULL;\r\n"), "LF endings"},
		{"no final newline", []byte(`UPDATE "reviewed"."articles" SET "slug" = 'x' WHERE "slug" IS NULL;`), "final newline"},
		{"two final newlines", []byte("UPDATE \"reviewed\".\"articles\" SET \"slug\" = 'x';\n\n"), "final newline"},
		{"empty", []byte(""), "1 byte and 1 MiB"},
		{"oversized", append([]byte("-- "), append(make([]byte, 1<<20), '\n')...), "1 byte and 1 MiB"},
		{"template marker", []byte("UPDATE \"reviewed\".\"articles\" SET \"slug\" = '{{ .Slug }}';\n"), "template or interpolation marker"},
		{"format marker", []byte("UPDATE \"reviewed\".\"articles\" SET \"slug\" = '%s';\n"), "template or interpolation marker"},
		{"parameter", []byte("UPDATE \"reviewed\".\"articles\" SET \"slug\" = $1;\n"), "zero-parameter"},
		{"nul byte", append([]byte("UPDATE \"reviewed\".\"articles\" SET \"slug\" = 'x'"), 0, ';', '\n'), "NUL"},
		{"invalid utf8", append([]byte("UPDATE \"reviewed\".\"articles\" SET \"slug\" = '"), 0xff, '\'', ';', '\n'), "UTF-8"},
	}
	for _, item := range artifacts {
		t.Run("artifact/"+item.name, func(t *testing.T) {
			if err := validateReviewedBackfillArtifact(item.reviewed); err == nil || !strings.Contains(err.Error(), item.want) {
				t.Fatalf("error=%v; want %q", err, item.want)
			}
		})
	}

	t.Run("live tamper and multiple statements", func(t *testing.T) {
		forEachPostgreSQLProfile(t, func(t *testing.T, profile string, database *sqlx.DB) {
			live := physical.PhysicalName("golem_backfill_tamper_" + profile)
			reviewed := postgresqlReviewedSlugBackfill(live)
			for _, item := range []struct {
				name    string
				mutate  func(map[string][]byte, string)
				want    string
				refused bool
			}{
				{name: "rewritten bytes", want: "manual companion is missing, rewritten, or lacks postcondition", mutate: func(files map[string][]byte, path string) {
					files[path] = []byte(strings.Replace(string(reviewed), "lower", "upper", 1))
				}},
				{name: "absent artifact", want: "manual companion is missing, rewritten, or lacks postcondition", mutate: func(files map[string][]byte, path string) {
					delete(files, path)
				}},
			} {
				t.Run(item.name, func(t *testing.T) {
					dropPostgreSQLWideningNamespace(t, database, live)
					defer dropPostgreSQLWideningNamespace(t, database, live)
					_, _, manifest, files := postgresqlBackfillHistory(t, live, reviewed)
					if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
						t.Fatalf("bootstrap: %v", err)
					}
					insertPostgreSQLArticles(t, database, live, "One", "Two")
					item.mutate(files, manifest.Entries[1].Manual[0].File.Path)
					if err := New().ApplyMigration(context.Background(), database, manifest, files); err == nil || !strings.Contains(err.Error(), item.want) {
						t.Fatalf("error=%v; want %q", err, item.want)
					}
					var columns int
					if err := database.Get(&columns, `SELECT count(*) FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname='articles' AND a.attname='slug' AND NOT a.attisdropped`, string(live)); err != nil || columns != 0 {
						t.Fatalf("a rewritten companion still reached database work: columns=%d error=%v", columns, err)
					}
					if ledger, err := New().ReadLedger(context.Background(), database); err != nil || len(ledger) != 1 {
						t.Fatalf("ledger=%d error=%v", len(ledger), err)
					}
				})
			}
			t.Run("two statements", func(t *testing.T) {
				dropPostgreSQLWideningNamespace(t, database, live)
				defer dropPostgreSQLWideningNamespace(t, database, live)
				two := []byte(fmt.Sprintf("UPDATE %q.%q SET %q = lower(%q) WHERE %q IS NULL; DELETE FROM %q.%q WHERE %q = 'one';\n", live, "articles", "slug", "title", "slug", live, "articles", "title"))
				_, _, manifest, files := postgresqlBackfillHistory(t, live, two)
				if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
					t.Fatalf("bootstrap: %v", err)
				}
				insertPostgreSQLArticles(t, database, live, "One", "Two")
				if err := New().ApplyMigration(context.Background(), database, manifest, files); err == nil || !strings.Contains(err.Error(), "reviewed backfill") {
					t.Fatalf("error=%v; want a single-statement refusal", err)
				}
				var rows int
				if err := database.Get(&rows, fmt.Sprintf(`SELECT count(*) FROM %q.%q`, live, "articles")); err != nil || rows != 2 {
					t.Fatalf("rows=%d error=%v", rows, err)
				}
				if ledger, err := New().ReadLedger(context.Background(), database); err != nil || len(ledger) != 1 {
					t.Fatalf("ledger=%d error=%v", len(ledger), err)
				}
			})
		})
	})
}

func TestPostgreSQLReviewedBackfillFailureRollsBackSchemaDataAndLedger(t *testing.T) {
	forEachPostgreSQLProfile(t, func(t *testing.T, profile string, database *sqlx.DB) {
		namespace := physical.PhysicalName("golem_backfill_rollback_" + profile)
		dropPostgreSQLWideningNamespace(t, database, namespace)
		defer dropPostgreSQLWideningNamespace(t, database, namespace)
		partial := []byte(fmt.Sprintf("UPDATE %q.%q\nSET %q = lower(%q)\nWHERE %q IS NULL AND %q <> 'Skipped';\n", namespace, "articles", "slug", "title", "slug", "title"))
		before, _, manifest, files := postgresqlBackfillHistory(t, namespace, partial)
		if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		insertPostgreSQLArticles(t, database, namespace, "Kept", "Skipped")
		err := New().ApplyMigration(context.Background(), database, manifest, files)
		if err == nil || !strings.Contains(err.Error(), "postcondition") || !strings.Contains(err.Error(), "1 rows remain unset") {
			t.Fatalf("error=%v; want a generated postcondition failure", err)
		}
		if ledger, readErr := New().ReadLedger(context.Background(), database); readErr != nil || len(ledger) != 1 {
			t.Fatalf("ledger=%d error=%v", len(ledger), readErr)
		}
		if verifyErr := New().Verify(context.Background(), database, before); verifyErr != nil {
			t.Fatalf("failed backfill left schema drift: %v", verifyErr)
		}
		var columns int
		if getErr := database.Get(&columns, `SELECT count(*) FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON c.oid=a.attrelid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname='articles' AND a.attname='slug' AND NOT a.attisdropped`, string(namespace)); getErr != nil || columns != 0 {
			t.Fatalf("rolled-back slug columns=%d error=%v", columns, getErr)
		}
		var titles []string
		if selectErr := database.Select(&titles, fmt.Sprintf(`SELECT "title" FROM %q.%q ORDER BY "id"`, namespace, "articles")); selectErr != nil || len(titles) != 2 {
			t.Fatalf("titles=%#v error=%v", titles, selectErr)
		}
	})
}

func TestPostgreSQLReviewedBackfillCrashBoundariesRecoverExactlyOnce(t *testing.T) {
	forEachPostgreSQLProfile(t, func(t *testing.T, profile string, database *sqlx.DB) {
		for _, boundary := range []string{"before_first_phase", "inside_transaction_before_ledger", "inside_transaction_before_commit"} {
			t.Run(boundary, func(t *testing.T) {
				namespace := physical.PhysicalName("golem_backfill_crash_" + profile)
				dropPostgreSQLWideningNamespace(t, database, namespace)
				defer dropPostgreSQLWideningNamespace(t, database, namespace)
				before, after, manifest, files := postgresqlBackfillHistory(t, namespace, postgresqlReviewedSlugBackfill(namespace))
				if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
					t.Fatalf("bootstrap: %v", err)
				}
				insertPostgreSQLArticles(t, database, namespace, "One", "Two")
				type abort struct{ boundary string }
				func() {
					defer func() {
						if recovered := recover(); recovered == nil {
							t.Fatal("migration was not interrupted at the boundary")
						} else if _, ok := recovered.(abort); !ok {
							panic(recovered)
						}
					}()
					ctx := migrationfailpoint.WithHook(context.Background(), func(reached string) {
						if reached == boundary {
							panic(abort{boundary})
						}
					})
					_ = New().ApplyMigration(ctx, database, manifest, files)
				}()
				if ledger, err := New().ReadLedger(context.Background(), database); err != nil || len(ledger) != 1 {
					t.Fatalf("interrupted migration advanced the ledger to %d: %v", len(ledger), err)
				}
				if err := New().Verify(context.Background(), database, before); err != nil {
					t.Fatalf("interrupted migration left schema drift: %v", err)
				}
				if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
					t.Fatalf("recovery: %v", err)
				}
				if err := New().Verify(context.Background(), database, after); err != nil {
					t.Fatalf("recovered schema mismatch: %v", err)
				}
				var slugs []string
				if err := database.Select(&slugs, fmt.Sprintf(`SELECT "slug" FROM %q.%q ORDER BY "id"`, namespace, "articles")); err != nil || len(slugs) != 2 || slugs[0] != "one" || slugs[1] != "two" {
					t.Fatalf("slugs=%#v error=%v", slugs, err)
				}
				if err := New().ApplyMigration(context.Background(), database, manifest, files); err == nil || !strings.Contains(err.Error(), "no unapplied reviewed entry") {
					t.Fatalf("error=%v; want an idempotent no-op refusal", err)
				}
			})
		}
	})
}

func TestPostgreSQLReviewedBackfillNeverEntersApplicationRuntimeAuthority(t *testing.T) {
	namespace := physical.PhysicalName("reviewed")
	reviewed := postgresqlReviewedSlugBackfill(namespace)
	before := livePostgreSQLBackfillSchema(t, namespace, false)
	after := livePostgreSQLBackfillSchema(t, namespace, true)
	entry := reviewedPostgreSQLEntry(t, "002_reviewed_backfill", before, after, nil)
	entry, files := sealPostgreSQLBackfillEntry(t, entry, reviewed)

	plan, err := New().PlanIncremental(entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.SQL(), "UPDATE") || strings.Contains(plan.SQL(), string(reviewed)) {
		t.Fatalf("the reviewed plan exposed companion SQL:\n%s", plan.SQL())
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "UPDATE") || strings.Contains(string(encoded), "lower(") {
		t.Fatalf("the sealed manifest entry embedded companion SQL:\n%s", encoded)
	}
	record := migration.LedgerEntry{MigrationID: entry.ID, Files: entry.Files}
	fileBytes, phaseBytes, err := encodePostgreSQLLedger(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(fileBytes)+string(phaseBytes), "UPDATE") {
		t.Fatal("the ledger row embedded companion SQL")
	}
	for _, path := range []string{entry.Manual[0].File.Path} {
		if _, bound := files[path]; !bound {
			t.Fatalf("companion path %s is not bound to reviewed bytes", path)
		}
		for _, file := range entry.Files {
			if file.Path == path {
				t.Fatal("the companion is bound as an ordinary rendered artifact")
			}
		}
	}
	if err := executeReviewedPostgreSQLBackfill(context.Background(), nil, entry, map[string][]byte{}, postgresqlBackfillOperation(t, entry).ID); err == nil {
		t.Fatal("a backfill executed without reviewed bytes")
	}
	if err := executeReviewedPostgreSQLBackfill(context.Background(), nil, entry, files, "not-an-operation"); err == nil {
		t.Fatal("a backfill executed for an unbound operation")
	}
}

func TestPostgreSQLReviewedBackfillErrorsAndObservationsNeverExposeSQLOrRows(t *testing.T) {
	forEachPostgreSQLProfile(t, func(t *testing.T, profile string, database *sqlx.DB) {
		namespace := physical.PhysicalName("golem_backfill_privacy_" + profile)
		const sqlCanary = "canary_statement_marker"
		const rowCanary = "canary_row_value"
		dropPostgreSQLWideningNamespace(t, database, namespace)
		defer dropPostgreSQLWideningNamespace(t, database, namespace)
		broken := []byte(fmt.Sprintf("UPDATE %q.%q\nSET %q = %q || '%s'\nWHERE %q IS NULL AND %q <> '%s';\n", namespace, "articles", "slug", "title", sqlCanary, "slug", "title", rowCanary))
		_, _, manifest, files := postgresqlBackfillHistory(t, namespace, broken)
		if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		insertPostgreSQLArticles(t, database, namespace, rowCanary, "kept")
		err := New().ApplyMigration(context.Background(), database, manifest, files)
		if err == nil {
			t.Fatal("a backfill that left a NULL row committed")
		}
		message := err.Error()
		for _, canary := range []string{sqlCanary, rowCanary, "UPDATE", "lower(", "articles", "slug", string(namespace)} {
			if strings.Contains(message, canary) {
				t.Fatalf("migration error exposed %q: %s", canary, message)
			}
		}
		if !strings.Contains(message, "postcondition") || !strings.Contains(message, string(manifest.Entries[1].ID)) {
			t.Fatalf("migration error is not a closed, identified diagnostic: %s", message)
		}
		const columnCanary = "canary_absent_column"
		failing := []byte(fmt.Sprintf("UPDATE %q.%q SET %q = %q WHERE %q IS NULL;\n", namespace, "articles", "slug", columnCanary, "slug"))
		dropPostgreSQLWideningNamespace(t, database, namespace)
		_, _, manifest, files = postgresqlBackfillHistory(t, namespace, failing)
		if err := New().ApplyMigration(context.Background(), database, manifest, files); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
		insertPostgreSQLArticles(t, database, namespace, "one")
		err = New().ApplyMigration(context.Background(), database, manifest, files)
		if err == nil {
			t.Fatal("an invalid reviewed backfill committed")
		}
		for _, canary := range []string{columnCanary, "UPDATE", "articles", string(namespace), "does not exist"} {
			if strings.Contains(err.Error(), canary) {
				t.Fatalf("driver error leaked %q: %s", canary, err.Error())
			}
		}
		if ledger, readErr := New().ReadLedger(context.Background(), database); readErr != nil || len(ledger) != 1 {
			t.Fatalf("ledger=%d error=%v", len(ledger), readErr)
		}
	})
}
