package compatibility

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/dataincarnation"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	publicsqlite "github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

// The current target is explicit metadata for offline publication, lowered from
// the verified released logical model. This is not generated application code.
// Fresh generated-app admission has its separate public runtime acceptance.
func releasedRestoreTarget(t *testing.T, wire releasedBackupWire, source golem.SchemaBundle) (golem.SchemaBundle, migration.Manifest, map[string][]byte) {
	t.Helper()
	if _, err := schema.NewHistorical(source); err != nil {
		t.Fatal(err)
	}
	var model ir.ModelIR
	if err := json.Unmarshal(source.Model().Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	contract, err := ir.DecodeHistoricalContractV7(source.Contract().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	// Author new current target documents only after the original bytes and
	// fingerprints have been verified. The archive's documents remain unchanged.
	contract.FormatVersion = ir.ContractFormatVersion
	contract.DataIncarnation = &ir.DataIncarnationContractIR{Kind: "provider", Version: dataincarnation.Version}
	for _, provider := range model.Providers {
		identity, diagnostic := ir.NewIDRegistry().Register(ir.ObjectExtension, ir.OwnedIdentity(string(model.Schema.ID), dataincarnation.Kind+"\x00"+string(provider)), ir.SourceSpan{})
		if diagnostic != nil {
			t.Fatal(diagnostic)
		}
		model.Extensions = append(model.Extensions, ir.ProviderExtensionIR{ID: ir.ExtensionIDFrom(identity), Provider: provider, Version: dataincarnation.Version, Owner: ir.ObjectID(model.Schema.ID), Kind: dataincarnation.Kind, Payload: "{}"})
	}
	if err := ir.ValidateDataIncarnation(ir.CompilationIR{Model: model, Contract: contract}); err != nil {
		t.Fatal(err)
	}
	modelBytes, err := ir.CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	modelFingerprint, err := ir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	contractBytes, err := ir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := ir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	modelDocument := golem.GeneratedSchemaDocument(uint32(ir.ModelFormatVersion), uint32(ir.CanonicalFormatVersion), compatibilityDigest(t, string(modelFingerprint)), modelBytes)
	contractDocument := golem.GeneratedSchemaDocument(uint32(ir.ContractFormatVersion), uint32(ir.CanonicalFormatVersion), compatibilityDigest(t, string(contractFingerprint)), contractBytes)
	var documents []golem.ProviderSchemaDocument
	var targetSQLite physical.PhysicalSchema
	for _, lowerer := range []physical.Lowerer{sqliteprovider.New(), postgresprovider.New()} {
		snapshot, err := lowerer.Lower(context.Background(), model, physical.LowerOptions{})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := physical.CanonicalEncode(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, err := physical.PhysicalFingerprint(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		system, err := physical.SystemFingerprint(snapshot.Provider, snapshot.System)
		if err != nil {
			t.Fatal(err)
		}
		document := golem.GeneratedSchemaDocument(snapshot.Version, snapshot.CanonicalVersion, golem.SchemaDigest(fingerprint), encoded)
		documents = append(documents, golem.GeneratedProviderSchemaDocument(golem.Provider(snapshot.Provider.Provider), golem.SchemaDigest(system), document))
		if snapshot.Provider.Provider == ir.SQLite {
			targetSQLite = snapshot
		}
	}
	manifestBytes, err := source.MigrationManifest(golem.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := migration.ParseManifest(manifestBytes)
	if err != nil || len(manifest.Entries) != 1 {
		t.Fatalf("released source history: %v", err)
	}
	files := make(map[string][]byte, len(wire.MigrationFiles)+1)
	for path, value := range wire.MigrationFiles {
		files[path] = bytes.Clone(value)
	}
	if err := migration.VerifyManifest(manifest, files); err != nil {
		t.Fatal(err)
	}
	initial := manifest.Entries[0]
	plan, err := migration.DiffReviewed(initial.AfterSnapshot, targetSQLite)
	if err != nil {
		t.Fatal(err)
	}
	allowlist, err := physical.UnmanagedAllowlistFingerprint(targetSQLite)
	if err != nil {
		t.Fatal(err)
	}
	entry := migration.ManifestEntry{ID: "0002_restore_identity", ParentID: initial.ID, ParentChainHash: initial.ChainHash, BeforeModel: initial.AfterModel, AfterModel: migration.Digest(modelFingerprint), BeforePhysical: plan.BeforeFingerprint, AfterPhysical: plan.AfterFingerprint, BeforeSnapshot: initial.AfterSnapshot, AfterSnapshot: targetSQLite, UnmanagedAllowlistDigest: migration.Digest(allowlist.String()), Operations: plan.Operations, Phases: plan.Phases}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
		if migration.RequiresApproval(operation) {
			entry.Approvals = append(entry.Approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	script, err := sqliteprovider.New().RenderMigration(entry)
	if err != nil {
		t.Fatal(err)
	}
	const filename = "0002_restore_identity.sql"
	files[filename] = []byte(script.SQL())
	entry.Files = []migration.FileChecksum{{Path: filename, SHA256: migration.Checksum(files[filename])}}
	entry.ChainHash = migration.ChainHash(entry)
	manifest.Entries = append(manifest.Entries, entry)
	manifest.Provider = targetSQLite.Provider
	manifestBytes, err = migration.EncodeManifest(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	generationInput := append(append(bytes.Clone(modelBytes), contractBytes...), manifestBytes...)
	generation := golem.SchemaDigest(sha256.Sum256(generationInput))
	for index, document := range documents {
		if document.Provider() == golem.SQLite {
			documents[index] = golem.GeneratedProviderSchemaDocumentWithMigration(golem.SQLite, document.SystemFingerprint(), document.Schema(), golem.GeneratedMigrationManifestDocument(generation, golem.SQLite, manifestBytes))
		}
	}
	target := golem.GeneratedSchemaBundle(generation, "compatibility-target", source.TemplateABIVersion(), modelDocument, contractDocument, documents...)
	if _, err := schema.New(target); err != nil {
		t.Fatal(err)
	}
	return target, manifest, files
}

func releasedSQLiteEvidence(t *testing.T, database *sqlx.DB, tables map[string][]map[string]any) map[string][]map[string]any {
	t.Helper()
	result := make(map[string][]map[string]any, len(tables))
	for table := range tables {
		rows, err := database.Queryx(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		result[table] = []map[string]any{}
		for rows.Next() {
			row := map[string]any{}
			if err := rows.MapScan(row); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for key, value := range row {
				if value, ok := value.([]byte); ok {
					row[key] = bytes.Clone(value)
				}
			}
			result[table] = append(result[table], row)
			if len(result[table]) > 64 {
				rows.Close()
				t.Fatal("released fixture exceeded its row bound")
			}
		}
		readErr, closeErr := rows.Err(), rows.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("released evidence read=%v close=%v", readErr, closeErr)
		}
	}
	return result
}

func requireReleasedEvidence(t *testing.T, expected, actual any) {
	t.Helper()
	left, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	right, err := json.Marshal(actual)
	if err != nil || !bytes.Equal(left, right) {
		t.Fatal("released database evidence changed", err)
	}
}

func exerciseReleasedRestore(t *testing.T, directory string, wire releasedBackupWire, source golem.SchemaBundle) {
	t.Helper()
	ctx := context.Background()
	target, manifest, files := releasedRestoreTarget(t, wire, source)
	archive := filepath.Join(directory, "backup.db")
	immutable, err := os.ReadFile(archive)
	if err != nil || len(immutable) > 8<<20 {
		t.Fatal("released backup is missing or exceeds the fixture bound", err)
	}
	reader, err := sqliteprovider.New().OpenRestoreArchive(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	requireReleasedEvidence(t, wire.Evidence, releasedSQLiteEvidence(t, reader, wire.Evidence))
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []publicsqlite.RestoreMode{publicsqlite.RestoreResumeAtLeastOnce, publicsqlite.RestoreAbandonAndResync} {
		t.Run(string(mode), func(t *testing.T) {
			directory := t.TempDir()
			destination := filepath.Join(directory, "restored.db")
			if restore, err := publicsqlite.PrepareRestore(ctx, publicsqlite.RestoreConfig{Archive: archive, Destination: destination, ArchiveBundle: source, TargetBundle: target, Mode: mode}); err == nil || restore != nil || !strings.Contains(err.Error(), "archived queue validation") {
				t.Fatalf("old queue must require explicit upgrade: %v", err)
			}
			extracted := filepath.Join(directory, "extracted.db")
			if err := os.WriteFile(extracted, immutable, 0600); err != nil {
				t.Fatal(err)
			}
			database, err := publicsqlite.Open(ctx, publicsqlite.Config{DataSourceName: extracted})
			if err != nil {
				t.Fatal(err)
			}
			engine := sqliteprovider.New()
			store, err := engine.QueueStore(database.UnsafeSQLX(), nil)
			if err != nil {
				t.Fatal(err)
			}
			maintenance := store.(queueprovider.StorageMaintenance)
			before, err := maintenance.InspectStorage(ctx)
			if err != nil || before.Version != queueprovider.StorageLegacyV088 {
				t.Fatalf("actual released queue inspection: %+v %v", before, err)
			}
			after, err := maintenance.UpgradeStorage(ctx)
			if err != nil || after.Version != queueprovider.StoragePriorityV1 {
				t.Fatalf("explicit extracted-copy upgrade: %+v %v", after, err)
			}
			upgraded := releasedSQLiteEvidence(t, database.UnsafeSQLX(), wire.Evidence)
			for _, row := range upgraded["golem_queue"] {
				if row["priority"] != int64(0) {
					t.Fatal("released jobs did not retain default priority")
				}
				delete(row, "priority")
			}
			requireReleasedEvidence(t, wire.Evidence, upgraded)
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			if err := publicsqlite.CheckpointForBackup(ctx, database); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(extracted, 0400); err != nil {
				t.Fatal(err)
			}
			restore, err := publicsqlite.PrepareRestore(ctx, publicsqlite.RestoreConfig{Archive: extracted, Destination: destination, ArchiveBundle: source, TargetBundle: target, Mode: mode})
			if err != nil {
				t.Fatal(err)
			}
			defer restore.Close()
			workTables := map[string][]map[string]any{"golem_queue": nil, "_golem_outbox_delivery": nil}
			prepared := releasedSQLiteEvidence(t, restore.Database().UnsafeSQLX(), workTables)
			for table, key := range map[string]string{"golem_queue": "id", "_golem_outbox_delivery": "causation_id"} {
				if len(prepared[table]) != len(wire.Evidence[table]) || len(wire.Evidence[table]) == 0 {
					t.Fatalf("restored released work lost rows: table=%s", table)
				}
				identities := map[string]bool{}
				for _, row := range wire.Evidence[table] {
					id, ok := row[key].(string)
					if !ok || id == "" || identities[id] {
						t.Fatal("released source work identity is missing or duplicated")
					}
					identities[id] = true
				}
				for _, row := range prepared[table] {
					id, ok := row[key].(string)
					if !ok || !identities[id] {
						t.Fatalf("restored released work identity changed: table=%s", table)
					}
					delete(identities, id)
				}
				if len(identities) != 0 {
					t.Fatal("restored released work identity disappeared")
				}
			}
			for _, row := range prepared["golem_queue"] {
				want := "pending"
				if mode == publicsqlite.RestoreAbandonAndResync {
					want = "canceled"
				}
				if row["status"] != want || row["lease_token"] != nil || row["priority"] != int64(0) {
					t.Fatal("restored released work has incorrect state, lease or priority")
				}
				for _, original := range wire.Evidence["golem_queue"] {
					if original["id"] != row["id"] {
						continue
					}
					mutable := map[string]bool{"status": true, "lease_token": true, "lease_until": true, "available_at": true, "resource_name": true, "resource_cost": true, "resource_capacity": true, "updated_at": true, "finished_at": true, "last_code": true}
					for field, value := range original {
						if !mutable[field] {
							requireReleasedEvidence(t, value, row[field])
						}
					}
				}
			}
			for _, row := range prepared["_golem_outbox_delivery"] {
				want := "pending"
				if mode == publicsqlite.RestoreAbandonAndResync {
					want = "retired"
				}
				if row["status"] != want {
					t.Fatal("restored released delivery has incorrect state")
				}
			}
			stagePath := restore.StagePath()
			if err := restore.Database().Close(); err != nil {
				t.Fatal(err)
			}
			if identity, err := restore.Publish(ctx); err == nil || identity.Valid() || !strings.Contains(err.Error(), "final catalog or reviewed history") {
				t.Fatalf("unmigrated released image was accepted: %v", err)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("unmigrated image refusal published a destination")
			}
			stage, err := publicsqlite.Open(ctx, publicsqlite.Config{DataSourceName: stagePath})
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.ApplyMigration(ctx, stage.UnsafeSQLX(), manifest, files); err != nil {
				t.Fatal(err)
			}
			initialized, err := engine.ReadDataIncarnation(ctx, stage.UnsafeSQLX(), manifest.Entries[1].AfterSnapshot.System)
			if err != nil || !initialized.Valid() {
				t.Fatal("reviewed identity migration did not initialize identity", err)
			}
			if err := stage.Close(); err != nil {
				t.Fatal(err)
			}
			identity, err := restore.Publish(ctx)
			if err != nil || !identity.Valid() || identity == initialized {
				t.Fatal("released archive publication did not establish fresh identity", err)
			}
			fresh, err := publicsqlite.Open(ctx, publicsqlite.Config{DataSourceName: destination})
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			if err := engine.Verify(ctx, fresh.UnsafeSQLX(), manifest.Entries[1].AfterSnapshot); err != nil {
				t.Fatal(err)
			}
			ledger, err := engine.ReadLedger(ctx, fresh.UnsafeSQLX())
			if err != nil || migration.VerifyLedger(manifest, ledger) != nil {
				t.Fatal("restored released archive lost reviewed history", err)
			}
			actual := releasedSQLiteEvidence(t, fresh.UnsafeSQLX(), wire.Evidence)
			for table, expected := range wire.Evidence {
				if table != "_golem_migrations" && table != "golem_queue" && table != "_golem_outbox_delivery" {
					requireReleasedEvidence(t, expected, actual[table])
				}
			}
			requireReleasedEvidence(t, prepared, releasedSQLiteEvidence(t, fresh.UnsafeSQLX(), workTables))
			storedIdentity, err := engine.ReadDataIncarnation(ctx, fresh.UnsafeSQLX(), manifest.Entries[1].AfterSnapshot.System)
			if err != nil || storedIdentity != identity {
				t.Fatal("fresh restored provider did not retain publication identity", err)
			}
			if got, err := os.ReadFile(archive); err != nil || !reflect.DeepEqual(got, immutable) {
				t.Fatal("restore modified the actual released archive")
			}
		})
	}
}
