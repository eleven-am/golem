package postgresql

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/migration"
	migrationfailpoint "github.com/eleven-am/golem/go/internal/migration/failpoint"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

func TestPostgreSQLReviewedBackfillCommitCannotEscapeMigrationTransaction(t *testing.T) {
	forEachPostgreSQLProfile(t, func(t *testing.T, profile string, database *sqlx.DB) {
		testPostgreSQLReviewedBackfillAtomicRollback(t, profile, database, func(physical.PhysicalName) []byte { return []byte("COMMIT;\n") }, nil, false)
	})
}

func TestPostgreSQLReviewedBackfillTransactionAndUtilityCommandsRollBack(t *testing.T) {
	commands := []struct {
		name    string
		command func(physical.PhysicalName) []byte
		setup   func(*testing.T, *sqlx.DB, physical.PhysicalName)
	}{
		{name: "begin", command: func(physical.PhysicalName) []byte { return []byte("BEGIN;\n") }},
		{name: "rollback", command: func(physical.PhysicalName) []byte { return []byte("ROLLBACK;\n") }},
		{name: "commit and chain", command: func(physical.PhysicalName) []byte { return []byte("COMMIT AND CHAIN;\n") }},
		{name: "savepoint", command: func(physical.PhysicalName) []byte { return []byte("SAVEPOINT golem_reviewed;\n") }},
		{name: "prepare transaction", command: func(physical.PhysicalName) []byte { return []byte("PREPARE TRANSACTION 'golem_reviewed_atomic';\n") }},
		{name: "vacuum", command: func(namespace physical.PhysicalName) []byte {
			return []byte(fmt.Sprintf("VACUUM %q.articles;\n", namespace))
		}},
		{name: "concurrent index", command: func(namespace physical.PhysicalName) []byte {
			return []byte(fmt.Sprintf("CREATE INDEX CONCURRENTLY golem_reviewed_atomic ON %q.articles(title);\n", namespace))
		}},
		{name: "nested do", command: func(namespace physical.PhysicalName) []byte {
			return []byte(fmt.Sprintf("DO $nested$ BEGIN UPDATE %q.articles SET title='Changed',slug=lower(title); COMMIT; END; $nested$;\n", namespace))
		}},
		{name: "nested call", setup: func(t *testing.T, database *sqlx.DB, namespace physical.PhysicalName) {
			t.Helper()
			if _, err := database.Exec(fmt.Sprintf("CREATE PROCEDURE %q.commit_attempt() LANGUAGE plpgsql AS $nested$ BEGIN UPDATE %q.articles SET title='Changed',slug=lower(title); COMMIT; END; $nested$", namespace, namespace)); err != nil {
				t.Fatal(err)
			}
		}, command: func(namespace physical.PhysicalName) []byte {
			return []byte(fmt.Sprintf("CALL %q.commit_attempt();\n", namespace))
		}},
	}
	forEachPostgreSQLProfile(t, func(t *testing.T, profile string, database *sqlx.DB) {
		for _, command := range commands {
			t.Run(command.name, func(t *testing.T) {
				testPostgreSQLReviewedBackfillAtomicRollback(t, profile, database, command.command, command.setup, false)
			})
		}
	})
}

func TestPostgreSQLReviewedBackfillAtomicCancellationBeforeLedgerRollsBack(t *testing.T) {
	forEachPostgreSQLProfile(t, func(t *testing.T, profile string, database *sqlx.DB) {
		testPostgreSQLReviewedBackfillAtomicRollback(t, profile, database, postgresqlReviewedSlugBackfill, nil, true)
	})
}

func testPostgreSQLReviewedBackfillAtomicRollback(t *testing.T, profile string, database *sqlx.DB, command func(physical.PhysicalName) []byte, setup func(*testing.T, *sqlx.DB, physical.PhysicalName), cancelBeforeLedger bool) {
	t.Helper()
	ctx := context.Background()
	provider := New()
	namespace := physical.PhysicalName("golem_backfill_atomic_" + profile)
	dropPostgreSQLWideningNamespace(t, database, namespace)
	defer dropPostgreSQLWideningNamespace(t, database, namespace)

	before := livePostgreSQLBackfillSchema(t, namespace, false)
	after := livePostgreSQLBackfillSchema(t, namespace, true)
	for _, snapshot := range []*physical.PhysicalSchema{&before, &after} {
		snapshot.Version, snapshot.CanonicalVersion = physical.DataIncarnationFormatVersion, physical.DataIncarnationFormatVersion
		snapshot.System.Objects = append(snapshot.System.Objects, physical.DataIncarnationSystemObjectV1())
		*snapshot = normalizePostgreSQLMigrationSchema(t, *snapshot)
	}
	empty := normalizePostgreSQLMigrationSchema(t, physical.PhysicalSchema{Version: before.Version, CanonicalVersion: before.CanonicalVersion, Provider: before.Provider, Namespace: before.Namespace})
	first := reviewedPostgreSQLEntry(t, "001_initial", empty, before, nil)
	first, firstFiles := finalizePostgreSQLEntry(t, provider, first)
	second := reviewedPostgreSQLEntry(t, "002_reviewed_backfill", before, after, &first)
	reviewed := command(namespace)
	if err := migration.ValidateReviewedBackfillArtifact(reviewed); err != nil {
		t.Fatalf("command must reach execution rather than artifact refusal: %v", err)
	}
	second, secondFiles := sealPostgreSQLBackfillEntry(t, second, reviewed)
	manifest := reviewedPostgreSQLManifest(provider, first, second)
	files := mergePostgreSQLMigrationFiles(firstFiles, secondFiles)
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if setup != nil {
		setup(t, database, namespace)
	}
	// Native preparation proves the command reaches the reviewed execution
	// boundary rather than failing artifact validation or statement parsing.
	if !cancelBeforeLedger {
		prepared, err := database.PrepareContext(ctx, string(reviewed))
		if err != nil {
			t.Fatalf("reviewed command was not preparable: %v", err)
		}
		if err := prepared.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// A real row ensures the generated no-NULL postcondition fails after a
	// top-level COMMIT, exposing staged DDL that rollback can no longer undo.
	insertPostgreSQLArticles(t, database, namespace, "Original Title")
	if err := provider.Verify(ctx, database, before); err != nil {
		t.Fatalf("initial schema: %v", err)
	}
	originalLedger, err := provider.ReadLedger(ctx, database)
	if err != nil || len(originalLedger) != 1 {
		t.Fatalf("initial ledger=%#v error=%v", originalLedger, err)
	}
	originalIncarnation, err := provider.ReadDataIncarnation(ctx, database, before.System)
	if err != nil {
		t.Fatal(err)
	}
	type article struct {
		ID    int64  `db:"id"`
		Title string `db:"title"`
	}
	readArticles := func() []article {
		t.Helper()
		var rows []article
		if err := database.SelectContext(ctx, &rows, fmt.Sprintf(`SELECT "id","title" FROM %q."articles" ORDER BY "id"`, namespace)); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	originalRows := readArticles()

	applyContext := ctx
	reached := false
	if cancelBeforeLedger {
		canceled, cancel := context.WithCancel(ctx)
		defer cancel()
		applyContext = migrationfailpoint.WithHook(canceled, func(boundary string) {
			if boundary == "inside_transaction_before_ledger" {
				reached = true
				cancel()
			}
		})
	}
	applyErr := provider.ApplyMigration(applyContext, database, manifest, files)
	if cancelBeforeLedger {
		if applyErr == nil || !reached {
			t.Fatalf("before-ledger cancellation reached=%v error=%v", reached, applyErr)
		}
	} else if applyErr == nil || (!strings.Contains(applyErr.Error(), "reviewed backfill") && !(string(reviewed) == "COMMIT;\n" && strings.Contains(applyErr.Error(), "postcondition"))) {
		t.Fatalf("command must be refused by atomic companion execution: %v", applyErr)
	}
	if err := provider.Verify(ctx, database, before); err != nil {
		t.Fatalf("reviewed companion escaped the migration transaction and changed the original schema: %v", err)
	}
	if rows := readArticles(); !reflect.DeepEqual(rows, originalRows) {
		t.Fatalf("failed companion changed original rows: got=%#v want=%#v", rows, originalRows)
	}
	if ledger, err := provider.ReadLedger(ctx, database); err != nil || !reflect.DeepEqual(ledger, originalLedger) {
		t.Fatalf("failed companion changed ledger prefix: got=%#v want=%#v error=%v", ledger, originalLedger, err)
	}
	if incarnation, err := provider.ReadDataIncarnation(ctx, database, before.System); err != nil || incarnation != originalIncarnation {
		t.Fatalf("failed companion changed incarnation: error=%v", err)
	}

	// Replace only the unapplied reviewed entry with an ordinary backfill.
	// Its unchanged applied parent must remain a valid retry boundary.
	second = reviewedPostgreSQLEntry(t, "002_reviewed_backfill", before, after, &first)
	second, secondFiles = sealPostgreSQLBackfillEntry(t, second, postgresqlReviewedSlugBackfill(namespace))
	manifest = reviewedPostgreSQLManifest(provider, first, second)
	files = mergePostgreSQLMigrationFiles(firstFiles, secondFiles)
	if err := provider.ApplyMigration(ctx, database, manifest, files); err != nil {
		t.Fatalf("normal reviewed retry: %v", err)
	}
	if err := provider.Verify(ctx, database, after); err != nil {
		t.Fatalf("retried schema: %v", err)
	}
	if rows := readArticles(); !reflect.DeepEqual(rows, originalRows) {
		t.Fatalf("normal retry changed original rows: got=%#v want=%#v", rows, originalRows)
	}
	var slug string
	if err := database.GetContext(ctx, &slug, fmt.Sprintf(`SELECT "slug" FROM %q."articles" WHERE "id"=1`, namespace)); err != nil || slug != "original title" {
		t.Fatalf("retried slug=%q error=%v", slug, err)
	}
	ledger, err := provider.ReadLedger(ctx, database)
	if err != nil || len(ledger) != 2 || !reflect.DeepEqual(ledger[:1], originalLedger) {
		t.Fatalf("retry ledger=%#v error=%v", ledger, err)
	}
	if err := migration.VerifyLedger(manifest, ledger); err != nil {
		t.Fatalf("retry ledger chain: %v", err)
	}
	if incarnation, err := provider.ReadDataIncarnation(ctx, database, after.System); err != nil || incarnation != originalIncarnation {
		t.Fatalf("normal retry changed incarnation: error=%v", err)
	}
}
