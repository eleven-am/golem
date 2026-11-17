package runtime

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	publicsqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

func TestSQLiteRestoreReviewedPhysical3ArchiveMigratesPrivatelyBeforePublication(t *testing.T) {
	source := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true})
	target := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true,
		schemaTransform: func(graph schematest.GraphFixture) schematest.GraphFixture {
			graph = schematest.WithDataIncarnation(t, graph)
			// Bind the generated descriptors, event registry and migration metadata
			// to the target generation before the runtime fixture creates them.
			bundle := graph.Bundle
			generation := golem.SchemaDigest(sha256.Sum256(append([]byte("restore-physical-history-target:"), bundle.Contract().Bytes()...)))
			graph.Bundle = golem.GeneratedSchemaBundle(generation, bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), bundle.Contract(), bundle.Providers()...)
			var err error
			graph.Registry, err = schema.New(graph.Bundle)
			if err != nil {
				t.Fatal(err)
			}
			return graph
		},
	})
	if source.schema.SQLite.Version != 3 || target.schema.SQLite.Version != 4 || source.app.DataIncarnation().Valid() || !target.app.DataIncarnation().Valid() {
		t.Fatal("fixture did not provide actual physical3 and identity-enabled physical4 schemas")
	}
	if source.schema.Bundle.GenerationDigest() == target.schema.Bundle.GenerationDigest() {
		t.Fatal("source and target generation are identical")
	}
	source.mutate(t, golem.EventCreated, 91, 1, "retained-create")
	source.mutate(t, golem.EventUpdated, 91, 1, "retained-update")
	var versions []int64
	if err := source.app.database.Select(&versions, `SELECT fact_version FROM _golem_outbox ORDER BY transaction_ordinal,event_id`); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(versions, []int64{3, 3}) {
		t.Fatalf("archive does not contain unchanged default V3 facts: %v", versions)
	}
	before := sqliteSnapshotRuntimeState(t, source.app.database)
	provider := internalsqlite.New()
	prefix, err := provider.ReadLedger(source.ctx, source.app.database)
	if err != nil || len(prefix) != 1 {
		t.Fatalf("real source ledger=%v error=%v", prefix, err)
	}
	targetBundle, manifest, files := sqliteRestoreHistoryManifest(t, source, target)
	dir := t.TempDir()
	archive, destination := filepath.Join(dir, "archive.db"), filepath.Join(dir, "live.db")
	if err := publicsqlite.Snapshot(source.ctx, source.applicationConfig.Database, source.schema.Bundle, archive); err != nil {
		t.Fatal(err)
	}
	archiveBytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	// An existing destination must survive an omitted migration refusal.
	if err := os.WriteFile(destination, archiveBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := source.applicationConfig.Database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := target.applicationConfig.Database.Close(); err != nil {
		t.Fatal(err)
	}
	sourcePath := strings.TrimPrefix(source.databaseConfig.DataSourceName, "file:")
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	restore, err := publicsqlite.PrepareRestore(source.ctx, publicsqlite.RestoreConfig{
		Archive: archive, Destination: destination, ArchiveBundle: source.schema.Bundle, TargetBundle: targetBundle, Mode: publicsqlite.RestoreResumeAtLeastOnce,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restore.Close()
	stagePath := restore.StagePath()
	if err := restore.Database().Close(); err != nil {
		t.Fatal(err)
	}
	identity, err := restore.Publish(source.ctx)
	if err == nil || identity.Valid() || !strings.Contains(err.Error(), "final catalog or reviewed history") {
		t.Fatalf("unmigrated stage accepted or refused at wrong boundary: %v", err)
	}
	if got, err := os.ReadFile(destination); err != nil || !bytes.Equal(got, archiveBytes) {
		t.Fatal("missing migration refusal changed destination")
	}

	stage, err := publicsqlite.Open(source.ctx, publicsqlite.Config{DataSourceName: stagePath})
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	stagePrefix, err := provider.ReadLedger(source.ctx, stage.UnsafeSQLX())
	if err != nil || !reflect.DeepEqual(prefix, stagePrefix) {
		t.Fatalf("copy altered source ledger prefix: %v", err)
	}
	if err := provider.ApplyMigration(source.ctx, stage.UnsafeSQLX(), manifest, files); err != nil {
		t.Fatalf("reviewed private 3-to4 migration: %v", err)
	}
	initialized, err := provider.ReadDataIncarnation(source.ctx, stage.UnsafeSQLX(), target.schema.SQLite.System)
	if err != nil || !initialized.Valid() {
		t.Fatalf("reviewed migration did not initialize identity: %v", err)
	}
	migratedLedger, err := provider.ReadLedger(source.ctx, stage.UnsafeSQLX())
	if err != nil || len(migratedLedger) != 2 || !reflect.DeepEqual(prefix, migratedLedger[:1]) {
		t.Fatalf("migration lost exact source ledger prefix: %v", err)
	}
	if err := migration.VerifyLedger(manifest, migratedLedger); err != nil {
		t.Fatal(err)
	}
	if actual := sqliteSnapshotRuntimeState(t, stage.UnsafeSQLX()); !reflect.DeepEqual(before, actual) {
		t.Fatal("reviewed identity migration altered retained mutation state")
	}
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	identity, err = restore.Publish(source.ctx)
	if err != nil || !identity.Valid() || identity == initialized {
		t.Fatalf("publication did not freshly rotate reviewed identity: %v", err)
	}

	database, err := publicsqlite.Open(source.ctx, publicsqlite.Config{DataSourceName: destination})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	config := target.applicationConfig
	config.Database, config.Bundle = database, targetBundle
	config.HistoricalEventBundles = []golem.SchemaBundle{source.schema.Bundle}
	fresh, err := Open(source.ctx, config)
	if err != nil {
		t.Fatalf("fresh target startup: %v", err)
	}
	if fresh.DataIncarnation() != identity || fresh.EventCapabilities().PublisherRunning() {
		t.Fatal("fresh startup has wrong identity or started traffic")
	}
	if actual := sqliteSnapshotRuntimeState(t, database.UnsafeSQLX()); !reflect.DeepEqual(before, actual) {
		t.Fatal("restore changed historical rows, versions, facts or pending deliveries")
	}
	var title string
	if err := database.UnsafeSQLX().Get(&title, `SELECT title FROM posts WHERE id=?`, (golem.UUID{15: 91}).String()); err != nil || title != "retained-update" {
		t.Fatalf("restored row changed: title=%s error=%v", title, err)
	}
	finalLedger, err := provider.ReadLedger(source.ctx, database.UnsafeSQLX())
	if err != nil || !reflect.DeepEqual(migratedLedger, finalLedger) {
		t.Fatalf("publication altered reviewed ledger: %v", err)
	}
	for path, want := range map[string][]byte{archive: archiveBytes, sourcePath: sourceBytes} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("restore modified physical3 source/archive %s: %v", filepath.Base(path), err)
		}
		old, err := provider.OpenRestoreArchive(source.ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		var count int
		err = old.Get(&count, `SELECT count(*) FROM sqlite_schema WHERE name='_golem_data_incarnation'`)
		closeErr := old.Close()
		if err != nil || closeErr != nil || count != 0 {
			t.Fatalf("physical3 archive acquired identity: count=%d error=%v close=%v", count, err, closeErr)
		}
	}
}

func sqliteRestoreHistoryManifest(t *testing.T, source, target policyFactRuntimeFixture) (golem.SchemaBundle, migration.Manifest, map[string][]byte) {
	t.Helper()
	encoded, err := source.schema.Bundle.MigrationManifest(golem.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := migration.ParseManifest(encoded)
	if err != nil || len(manifest.Entries) != 1 {
		t.Fatalf("source reviewed manifest: %v", err)
	}
	initial := manifest.Entries[0]
	provider := internalsqlite.New()
	files := map[string][]byte{}
	if len(initial.Files) != 1 || len(initial.Manual) != 0 {
		t.Fatal("unexpected source fixture migration artifacts")
	}
	initialSQL, err := provider.RenderMigration(initial)
	if err != nil {
		t.Fatal(err)
	}
	files[initial.Files[0].Path] = []byte(initialSQL.SQL())
	if migration.Checksum(files[initial.Files[0].Path]) != initial.Files[0].SHA256 {
		t.Fatal("original reviewed initial SQL did not reproduce exactly")
	}
	after, err := physical.Normalize(target.schema.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := migration.DiffReviewed(initial.AfterSnapshot, after)
	if err != nil {
		t.Fatal(err)
	}
	allowlist, err := physical.UnmanagedAllowlistFingerprint(after)
	if err != nil {
		t.Fatal(err)
	}
	entry := migration.ManifestEntry{
		ID: "0002_data_incarnation", ParentID: initial.ID, ParentChainHash: initial.ChainHash,
		Operations: plan.Operations, Phases: plan.Phases,
		BeforeModel: initial.AfterModel, AfterModel: migration.Digest(target.schema.Bundle.Model().Fingerprint().String()),
		BeforePhysical: plan.BeforeFingerprint, AfterPhysical: plan.AfterFingerprint,
		BeforeSnapshot: initial.AfterSnapshot, AfterSnapshot: after,
		UnmanagedAllowlistDigest: migration.Digest(allowlist.String()),
	}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
		if migration.RequiresApproval(operation) {
			entry.Approvals = append(entry.Approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	script, err := provider.RenderMigration(entry)
	if err != nil {
		t.Fatal(err)
	}
	path := "migrations/sqlite/0002_data_incarnation.sql"
	files[path] = []byte(script.SQL())
	entry.Files = []migration.FileChecksum{{Path: path, SHA256: migration.Checksum(files[path])}}
	entry.ChainHash = migration.ChainHash(entry)
	manifest.Entries = append(manifest.Entries, entry)
	encoded, err = migration.EncodeManifest(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := migration.ParseManifest(encoded)
	if err != nil || !reflect.DeepEqual(initial, verified.Entries[0]) {
		t.Fatalf("appending history rewrote initial entry: %v", err)
	}
	bundle := target.schema.Bundle
	providers := bundle.Providers()
	for index, document := range providers {
		if document.Provider() == golem.SQLite {
			providers[index] = golem.GeneratedProviderSchemaDocumentWithMigration(golem.SQLite, document.SystemFingerprint(), document.Schema(), golem.GeneratedMigrationManifestDocument(bundle.GenerationDigest(), golem.SQLite, encoded))
		}
	}
	bundle = golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), bundle.Contract(), providers...)
	return bundle, manifest, files
}
