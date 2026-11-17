package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	publicsqlite "github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

func sqliteRestoreFixture(t *testing.T) policyFactRuntimeFixture {
	t.Helper()
	return newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true,
		schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture {
			return schematest.WithDataIncarnation(t, schematest.NewPolicyCaptureGraph(t))
		},
	})
}

func sqliteRestoreArchive(t *testing.T, fixture policyFactRuntimeFixture, path string) []byte {
	t.Helper()
	if err := publicsqlite.Snapshot(fixture.ctx, fixture.applicationConfig.Database, fixture.schema.Bundle, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Seal through the existing transaction owner; automatic writers remain V3.
func sqliteRestoreHistoricalGroup(t *testing.T, fixture policyFactRuntimeFixture) {
	t.Helper()
	if err := SystemTransaction(fixture.ctx, fixture.app.System(), func(tx *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		for _, id := range []byte{93, 94} {
			_, err := SystemTxCreate(fixture.ctx, tx, fixture.posts, golem.GeneratedCreateInput[policyFactRuntimePost](fixture.schema.Post,
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1}),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "historical")))
			if err != nil {
				return err
			}
		}
		snapshot, err := newMutationHistorySnapshot(tx.system.executor, fixture.app.registry)
		if err != nil {
			return err
		}
		graph, err := newMutationHistoryGraph(fixture.ctx, snapshot)
		if err != nil {
			return err
		}
		state, err := tx.system.executor.mutationState()
		if err != nil {
			return err
		}
		return state.sealHistory(fixture.ctx, graph, fixture.app.eventSchemas)
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fixture.app.database.Get(&count, `SELECT count(*) FROM _golem_outbox WHERE fact_version=4`); err != nil || count != 2 {
		t.Fatalf("explicit complete V4 fixture: count=%d error=%v", count, err)
	}
}

func sqliteRestoreQueue(t *testing.T, database *sqlx.DB) queueprovider.Store {
	t.Helper()
	store, err := internalsqlite.New().QueueStore(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSQLiteRestorePublicModesPreserveEvidenceAndReconcileOnce(t *testing.T) {
	for _, mode := range []publicsqlite.RestoreMode{publicsqlite.RestoreResumeAtLeastOnce, publicsqlite.RestoreAbandonAndResync} {
		t.Run(string(mode), func(t *testing.T) {
			fixture := sqliteRestoreFixture(t)
			fixture.mutate(t, golem.EventCreated, 91, 1, "archive")
			fixture.mutate(t, golem.EventUpdated, 91, 1, "archive-version")
			sqliteRestoreHistoricalGroup(t, fixture)
			store := sqliteRestoreQueue(t, fixture.app.database)
			for _, id := range []string{"pending", "leased"} {
				if _, err := store.Enqueue(fixture.ctx, nil, queueprovider.EnqueueRequest{ID: id, Type: id, Payload: []byte(`{"effect":"must-reconcile"}`), MaxAttempts: 5}); err != nil {
					t.Fatal(err)
				}
			}
			jobs, err := store.Claim(fixture.ctx, queueprovider.ClaimOptions{Types: []string{"leased"}, Limit: 1, LeaseDuration: time.Minute})
			if err != nil || len(jobs) != 1 {
				t.Fatalf("claim=%v error=%v", jobs, err)
			}
			before := sqliteSnapshotRuntimeState(t, fixture.app.database)
			archiveID := fixture.app.DataIncarnation()
			dir := t.TempDir()
			archive, destination := filepath.Join(dir, "archive.db"), filepath.Join(dir, "live.db")
			archiveBytes := sqliteRestoreArchive(t, fixture, archive)
			previous := sqliteRestoreFixture(t)
			previous.mutate(t, golem.EventCreated, 92, 1, "old-destination")
			previousID := previous.app.DataIncarnation()
			sqliteRestoreArchive(t, previous, destination)
			oldFile, err := os.Stat(destination)
			if err != nil {
				t.Fatal(err)
			}
			// No old app is reused and no worker/publisher was started. Closing
			// these handles represents the caller-owned offline stop precondition.
			if err := fixture.applicationConfig.Database.Close(); err != nil {
				t.Fatal(err)
			}
			if err := previous.applicationConfig.Database.Close(); err != nil {
				t.Fatal(err)
			}
			restore, err := publicsqlite.PrepareRestore(fixture.ctx, publicsqlite.RestoreConfig{Archive: archive, Destination: destination, ArchiveBundle: fixture.schema.Bundle, TargetBundle: fixture.schema.Bundle, Mode: mode})
			if err != nil {
				t.Fatal(err)
			}
			defer restore.Close()
			stage := restore.Database()
			config := fixture.applicationConfig
			config.Database = stage
			app, err := Open(fixture.ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			if app.EventCapabilities().PublisherRunning() {
				t.Fatal("restore started publisher")
			}
			prepared := sqliteSnapshotRuntimeState(t, stage.UnsafeSQLX())
			if !reflect.DeepEqual(before.facts, prepared.facts) || !reflect.DeepEqual(before.versions, prepared.versions) || before.counter != prepared.counter {
				t.Fatal("preparation changed immutable facts or row versions")
			}
			if _, err := restore.Publish(fixture.ctx); err == nil {
				t.Fatal("published with live staging handle")
			}
			var archivedStatus string
			wantStatus, wantDelivery := "pending", "pending"
			if mode == publicsqlite.RestoreAbandonAndResync {
				wantStatus, wantDelivery = "canceled", "retired"
			}
			if err := stage.UnsafeSQLX().Get(&archivedStatus, `SELECT status FROM golem_queue WHERE id='leased'`); err != nil || archivedStatus != wantStatus {
				t.Fatalf("archived status=%s error=%v", archivedStatus, err)
			}
			var count int
			if err := stage.UnsafeSQLX().Get(&count, `SELECT count(*) FROM golem_queue WHERE id='leased' AND attempt_count=1 AND lease_token IS NULL AND lease_until IS NULL`); err != nil || count != 1 {
				t.Fatalf("lease reconciliation count=%d error=%v", count, err)
			}
			if err := stage.UnsafeSQLX().Get(&count, `SELECT count(*) FROM _golem_outbox_delivery WHERE status<>?`, wantDelivery); err != nil || count != 0 {
				t.Fatalf("delivery mode count=%d error=%v", count, err)
			}
			// Explicit domain/external-effect reconciliation uses a fresh staged
			// application. New work must survive the already-applied policy.
			reconciled := fixture
			reconciled.app = app
			reconciled.mutate(t, golem.EventUpdated, 91, 1, "external-effect-reconciled")
			stageStore := sqliteRestoreQueue(t, stage.UnsafeSQLX())
			if _, err := stageStore.Enqueue(fixture.ctx, nil, queueprovider.EnqueueRequest{ID: "new", Type: "new", Payload: []byte(`{"new":true}`), MaxAttempts: 3}); err != nil {
				t.Fatal(err)
			}
			expected := sqliteSnapshotRuntimeState(t, stage.UnsafeSQLX())
			expectedJobs := sqliteRestoreQueueEvidence(t, stage.UnsafeSQLX())
			if err := stage.Close(); err != nil {
				t.Fatal(err)
			}
			identity, err := restore.Publish(fixture.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !identity.Valid() || identity == archiveID || identity == previousID {
				t.Fatal("restore did not establish fresh identity")
			}
			newFile, err := os.Stat(destination)
			if err != nil || os.SameFile(oldFile, newFile) {
				t.Fatalf("destination not replaced: %v", err)
			}
			data, err := os.ReadFile(archive)
			if err != nil || !bytes.Equal(data, archiveBytes) {
				t.Fatal("restore modified archive")
			}
			database, err := publicsqlite.Open(fixture.ctx, publicsqlite.Config{DataSourceName: destination})
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			config.Database = database
			fresh, err := Open(fixture.ctx, config)
			if err != nil {
				t.Fatalf("same-schema fresh startup: %v", err)
			}
			if fresh.DataIncarnation() != identity || fresh.EventCapabilities().PublisherRunning() {
				t.Fatal("fresh startup identity or lifecycle mismatch")
			}
			actual := sqliteSnapshotRuntimeState(t, database.UnsafeSQLX())
			if !bytes.Equal(expectedJobs, sqliteRestoreQueueEvidence(t, database.UnsafeSQLX())) {
				t.Fatal("publication changed complete queue rows")
			}
			if !reflect.DeepEqual(expected, actual) {
				t.Fatal("publication altered reconciled facts, delivery state or versions")
			}
			if err := database.UnsafeSQLX().Get(&count, `SELECT count(*) FROM golem_queue WHERE id='new' AND status='pending' AND attempt_count=0`); err != nil || count != 1 {
				t.Fatalf("new work abandoned: count=%d error=%v", count, err)
			}
			var title string
			if err := database.UnsafeSQLX().Get(&title, `SELECT title FROM posts WHERE id=?`, (golem.UUID{15: 91}).String()); err != nil || title != "external-effect-reconciled" {
				t.Fatalf("domain reconciliation lost: %s %v", title, err)
			}
			select {
			case <-fixture.transport.published:
				t.Fatal("restore emitted traffic")
			default:
			}
		})
	}
}

func TestSQLiteRestoreRefusesChangedStageAndDestination(t *testing.T) {
	fixture := sqliteRestoreFixture(t)
	fixture.mutate(t, golem.EventCreated, 91, 1, "archive")
	sqliteRestoreHistoricalGroup(t, fixture)
	store := sqliteRestoreQueue(t, fixture.app.database)
	if _, err := store.Enqueue(fixture.ctx, nil, queueprovider.EnqueueRequest{ID: "must-survive", Type: "restore.pending", Payload: []byte(`{"durable":true}`), MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "archive.db")
	archiveBytes := sqliteRestoreArchive(t, fixture, archive)
	if err := fixture.applicationConfig.Database.Close(); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"unsupported-codec", "truncated-history", "catalog", "ledger", "queue-index", "queue-drop", "queue-payload", "destination-changed", "destination-sidecar", "canceled", "discard"} {
		t.Run(scenario, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "live.db")
			if err := os.WriteFile(destination, archiveBytes, 0600); err != nil {
				t.Fatal(err)
			}
			restore, err := publicsqlite.PrepareRestore(fixture.ctx, publicsqlite.RestoreConfig{Archive: archive, Destination: destination, ArchiveBundle: fixture.schema.Bundle, TargetBundle: fixture.schema.Bundle, Mode: publicsqlite.RestoreResumeAtLeastOnce})
			if err != nil {
				t.Fatal(err)
			}
			defer restore.Close()
			stagePath := restore.StagePath()
			database := restore.Database()
			var statement string
			switch scenario {
			case "unsupported-codec":
				statement = `UPDATE _golem_outbox SET codec_identity='unsupported-test-codec' WHERE fact_version=3`
			case "truncated-history":
				statement = `DELETE FROM _golem_outbox WHERE fact_version=4 AND transaction_ordinal=2`
			case "catalog":
				statement = `ALTER TABLE posts ADD COLUMN unreviewed TEXT`
			case "ledger":
				statement = `DELETE FROM _golem_migrations`
			case "queue-index":
				statement = `DROP INDEX golem_queue_claim`
			case "queue-drop":
				statement = `DROP TABLE golem_queue`
			case "queue-payload":
				statement = `UPDATE golem_queue SET payload=zeroblob(1048577)`
			}
			if statement != "" {
				if _, err := database.UnsafeSQLX().Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "discard" {
				if err := restore.Close(); err != nil {
					t.Fatal(err)
				}
				if err := restore.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(stagePath); !os.IsNotExist(err) {
					t.Fatal("discard retained staging")
				}
			} else {
				if err := database.Close(); err != nil {
					t.Fatal(err)
				}
			}
			wantDestination := archiveBytes
			if scenario == "destination-changed" {
				wantDestination = []byte("concurrent-owner")
				if err := os.WriteFile(destination, wantDestination, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "destination-sidecar" {
				if err := os.WriteFile(destination+"-wal", []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx := fixture.ctx
			if scenario == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			identity, err := restore.Publish(ctx)
			if err == nil || identity.Valid() {
				t.Fatalf("unsafe publication accepted: identity=%v error=%v", identity, err)
			}
			if scenario == "queue-payload" && err.Error() != "sqlite restore final queue verification failed" {
				t.Fatalf("publication failed for an unrelated reason: %v", err)
			}
			if scenario == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
			got, err := os.ReadFile(destination)
			if err != nil || !bytes.Equal(got, wantDestination) {
				t.Fatal("refusal changed destination")
			}
			got, err = os.ReadFile(archive)
			if err != nil || !bytes.Equal(got, archiveBytes) {
				t.Fatal("refusal changed archive")
			}
		})
	}
}

func TestSQLiteRestorePrepareRejectsInvalidArchivedFactsAndSidecars(t *testing.T) {
	for _, scenario := range []string{"unsupported-codec", "truncated-history", "sidecar", "queue-payload"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := sqliteRestoreFixture(t)
			fixture.mutate(t, golem.EventCreated, 91, 1, "archive")
			sqliteRestoreHistoricalGroup(t, fixture)
			if scenario == "queue-payload" {
				store := sqliteRestoreQueue(t, fixture.app.database)
				if _, err := store.Enqueue(fixture.ctx, nil, queueprovider.EnqueueRequest{ID: "archive-pending", Type: "restore.pending", Payload: []byte(`{"durable":true}`), MaxAttempts: 3}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "unsupported-codec" {
				if _, err := fixture.app.database.Exec(`UPDATE _golem_outbox SET codec_identity='unsupported-test-codec' WHERE fact_version=3`); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "truncated-history" {
				if _, err := fixture.app.database.Exec(`DELETE FROM _golem_outbox WHERE fact_version=4 AND transaction_ordinal=2`); err != nil {
					t.Fatal(err)
				}
			}
			dir := t.TempDir()
			archive, destination := filepath.Join(dir, "archive.db"), filepath.Join(dir, "live.db")
			original := sqliteRestoreArchive(t, fixture, archive)
			if err := fixture.applicationConfig.Database.Close(); err != nil {
				t.Fatal(err)
			}
			if scenario == "queue-payload" {
				// Corrupt only the disposable, standalone archive after the source
				// runtime has closed. The real producer supplied the original job.
				archiveDatabase, _, err := internalsqlite.New().Open(fixture.ctx, archive)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := archiveDatabase.Exec(`UPDATE golem_queue SET payload=zeroblob(?)`, queueprovider.MaximumPayloadBytes+1); err != nil {
					_ = archiveDatabase.Close()
					t.Fatal(err)
				}
				if err := archiveDatabase.Close(); err != nil {
					t.Fatal(err)
				}
				for _, suffix := range []string{"-wal", "-shm", "-journal"} {
					if _, err := os.Stat(archive + suffix); !os.IsNotExist(err) {
						t.Fatalf("corrupted archive is not standalone: %s: %v", suffix, err)
					}
				}
				corrupted, err := os.ReadFile(archive)
				if err != nil {
					t.Fatal(err)
				}
				original = corrupted
			}
			if scenario == "sidecar" {
				if err := os.WriteFile(archive+"-journal", []byte("ambiguous"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, mode := range []publicsqlite.RestoreMode{publicsqlite.RestoreResumeAtLeastOnce, publicsqlite.RestoreAbandonAndResync} {
				restore, err := publicsqlite.PrepareRestore(fixture.ctx, publicsqlite.RestoreConfig{Archive: archive, Destination: destination, ArchiveBundle: fixture.schema.Bundle, TargetBundle: fixture.schema.Bundle, Mode: mode})
				if err == nil || restore != nil {
					if restore != nil {
						_ = restore.Close()
					}
					t.Fatal("invalid archive accepted")
				}
				if scenario == "queue-payload" && err.Error() != "sqlite restore archived queue validation failed" {
					t.Fatalf("prepare failed for an unrelated reason: %v", err)
				}
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("refused archive created destination")
			}
			got, err := os.ReadFile(archive)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatal("archive refusal modified source")
			}
		})
	}
}

// This contract-only change keeps the real reviewed physical archive intact.
// Its distinct generation admits historical Post codecs but cannot deliver Post.
func sqliteRestoreWithoutPostEvents(t *testing.T, fixture policyFactRuntimeFixture) golem.SchemaBundle {
	t.Helper()
	bundle := fixture.schema.Bundle
	compilation, err := schema.DecodeActive(bundle)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for index := range compilation.Contract.Models {
		model := &compilation.Contract.Models[index]
		// This target intentionally retires the fixture's named policy surface.
		// Its ready fact traverses Post and cannot survive disabling Post events;
		// remove its authority declarations too, then let the canonical schema
		// owner derive the empty capture inventory from these declarations.
		model.FactTemplates = nil
		model.ScopeTemplates = nil
		if string(model.ModelID) == hex.EncodeToString(fixture.schema.Post[:]) {
			model.Subscriptions = false
			model.Event = nil
			model.Roots.Events = ""
			found = true
		}
	}
	if !found {
		t.Fatal("Post declaration not found")
	}
	if err := scopecontract.Validate(compilation.Model, compilation.Contract); err != nil {
		t.Fatal(err)
	}
	payload, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(encoded) != 32 {
		t.Fatal("contract fingerprint invalid")
	}
	var digest golem.SchemaDigest
	copy(digest[:], encoded)
	contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest, payload)
	generation := golem.SchemaDigest(sha256.Sum256(append([]byte("restore-no-post-events:"), payload...)))
	providers := bundle.Providers()
	for index, document := range providers {
		if document.Provider() == golem.SQLite {
			manifest, err := bundle.MigrationManifest(golem.SQLite)
			if err != nil {
				t.Fatal(err)
			}
			providers[index] = golem.GeneratedProviderSchemaDocumentWithMigration(document.Provider(), document.SystemFingerprint(), document.Schema(), golem.GeneratedMigrationManifestDocument(generation, golem.SQLite, manifest))
		}
	}
	target := golem.GeneratedSchemaBundle(generation, bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), contract, providers...)
	registry, err := schema.New(target)
	if err != nil {
		t.Fatal(err)
	}
	post, _ := registry.Model(fixture.schema.Post)
	if _, _, enabled := post.EventSchema(); enabled {
		t.Fatal("target still exposes Post events")
	}
	for _, id := range []golem.ModelID{fixture.schema.User, fixture.schema.Comment} {
		model, _ := registry.Model(id)
		if _, _, enabled := model.EventSchema(); !enabled {
			t.Fatal("target lost unrelated events")
		}
	}
	return target
}

func TestSQLiteRestoreAbandonStillValidatesNewPendingDeliveryCompatibility(t *testing.T) {
	fixture := sqliteRestoreFixture(t)
	fixture.mutate(t, golem.EventCreated, 91, 1, "archive")
	dir := t.TempDir()
	archive, destination := filepath.Join(dir, "archive.db"), filepath.Join(dir, "live.db")
	sqliteRestoreArchive(t, fixture, archive)
	target := sqliteRestoreWithoutPostEvents(t, fixture)
	if err := fixture.applicationConfig.Database.Close(); err != nil {
		t.Fatal(err)
	}
	// Control proves this target/catalog/history combination is publishable
	// when only explicitly abandoned historical Post evidence remains.
	control, err := publicsqlite.PrepareRestore(fixture.ctx, publicsqlite.RestoreConfig{Archive: archive, Destination: filepath.Join(dir, "control.db"), ArchiveBundle: fixture.schema.Bundle, TargetBundle: target, Mode: publicsqlite.RestoreAbandonAndResync})
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if err := control.Database().Close(); err != nil {
		t.Fatal(err)
	}
	if identity, err := control.Publish(fixture.ctx); err != nil || !identity.Valid() {
		t.Fatalf("abandoned historical compatibility control: %v", err)
	}
	restore, err := publicsqlite.PrepareRestore(fixture.ctx, publicsqlite.RestoreConfig{Archive: archive, Destination: destination, ArchiveBundle: fixture.schema.Bundle, TargetBundle: target, Mode: publicsqlite.RestoreAbandonAndResync})
	if err != nil {
		t.Fatalf("abandon must admit valid historical incompatible facts: %v", err)
	}
	defer restore.Close()
	config := fixture.applicationConfig
	config.Database = restore.Database()
	staged, err := Open(fixture.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	reconciled := fixture
	reconciled.app = staged
	reconciled.mutate(t, golem.EventUpdated, 91, 1, "new-pending-event")
	var count int
	if err := restore.Database().UnsafeSQLX().Get(&count, `SELECT count(*) FROM _golem_outbox_delivery WHERE status='pending'`); err != nil || count != 1 {
		t.Fatalf("new pending evidence count=%d error=%v", count, err)
	}
	if err := restore.Database().Close(); err != nil {
		t.Fatal(err)
	}
	identity, err := restore.Publish(fixture.ctx)
	if err == nil || identity.Valid() || !strings.Contains(err.Error(), "final evidence validation") {
		t.Fatalf("abandon must refuse final incompatible delivery evidence: %v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("incompatible pending event reached destination")
	}
}

func sqliteRestoreQueueEvidence(t *testing.T, database *sqlx.DB) []byte {
	t.Helper()
	rows, err := database.Queryx(`SELECT * FROM golem_queue ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var evidence []map[string]any
	for rows.Next() {
		value := map[string]any{}
		if err := rows.MapScan(value); err != nil {
			t.Fatal(err)
		}
		for key, field := range value {
			if data, ok := field.([]byte); ok {
				value[key] = bytes.Clone(data)
			}
		}
		evidence = append(evidence, value)
		if len(evidence) > 10 {
			t.Fatal("unexpected restore fixture queue growth")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil || len(encoded) > 32<<10 {
		t.Fatalf("queue evidence encoding: %v", err)
	}
	return encoded
}
