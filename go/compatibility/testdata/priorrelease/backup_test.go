package priorrelease

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	providerhandle "github.com/eleven-am/golem/go/internal/provider/handle"
	native "github.com/eleven-am/golem/go/internal/provider/sqlite"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	publicsqlite "github.com/eleven-am/golem/go/provider/sqlite"
	golemruntime "github.com/eleven-am/golem/go/runtime"
	fixture "github.com/eleven-am/golem/go/runtime/testdata/p10operations"
)

// These documents come from the cached release's generated fixture unchanged.
// Digests are lowercase hexadecimal; byte slices use JSON's base64 encoding.
type releasedBackupDocument struct {
	FormatVersion    uint32 `json:"formatVersion"`
	CanonicalVersion uint32 `json:"canonicalVersion"`
	Fingerprint      string `json:"fingerprint"`
	Bytes            []byte `json:"bytes"`
}

type releasedBackupProvider struct {
	Provider          string                 `json:"provider"`
	SystemFingerprint string                 `json:"systemFingerprint"`
	Schema            releasedBackupDocument `json:"schema"`
	Manifest          []byte                 `json:"manifest,omitempty"`
}

type releasedBackupWire struct {
	Evidence           map[string][]map[string]any `json:"evidence,omitempty"`
	FormatVersion      uint16                      `json:"formatVersion"`
	GenerationDigest   string                      `json:"generationDigest"`
	GeneratorVersion   string                      `json:"generatorVersion"`
	TemplateABIVersion string                      `json:"templateABIVersion"`
	Model              releasedBackupDocument      `json:"model"`
	Contract           releasedBackupDocument      `json:"contract"`
	Providers          []releasedBackupProvider    `json:"providers"`
	MigrationFiles     map[string][]byte           `json:"migrationFiles,omitempty"`
}

func releasedBackupDocumentOf(document golem.SchemaDocument) releasedBackupDocument {
	return releasedBackupDocument{document.FormatVersion(), document.CanonicalVersion(), document.Fingerprint().String(), document.Bytes()}
}

func releasedBackupBundleOf(bundle golem.SchemaBundle) releasedBackupWire {
	wire := releasedBackupWire{
		FormatVersion:    1,
		GenerationDigest: bundle.GenerationDigest().String(),
		GeneratorVersion: bundle.GeneratorVersion(), TemplateABIVersion: bundle.TemplateABIVersion(),
		Model: releasedBackupDocumentOf(bundle.Model()), Contract: releasedBackupDocumentOf(bundle.Contract()),
	}
	for _, document := range bundle.Providers() {
		provider := releasedBackupProvider{Provider: string(document.Provider()), SystemFingerprint: document.SystemFingerprint().String(), Schema: releasedBackupDocumentOf(document.Schema())}
		if manifest, err := bundle.MigrationManifest(document.Provider()); err == nil {
			provider.Manifest = manifest
		}
		wire.Providers = append(wire.Providers, provider)
	}
	return wire
}

func writeReleasedBackupWire(t *testing.T, wire releasedBackupWire) {
	t.Helper()
	dir := os.Getenv("GOLEM_COMPAT_FIXTURES")
	if dir == "" {
		t.Fatal("run through the parent compatibility test")
	}
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 2<<20 {
		t.Fatal("released backup metadata exceeds 2 MiB")
	}
	file, err := os.OpenFile(filepath.Join(dir, "backup.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestReleasedBackup runs entirely against the released module and its generated
// application owners. No publisher or worker is started: durable work remains
// pending when the sole database owner is closed for the offline copy.
func TestReleasedBackup(t *testing.T) {
	dir := os.Getenv("GOLEM_COMPAT_FIXTURES")
	if dir == "" {
		t.Fatal("run through the parent compatibility test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	bundle := fixture.GolemGeneratedSchemaBundle()
	var desired physical.PhysicalSchema
	for _, document := range bundle.Providers() {
		if document.Provider() == golem.SQLite {
			var err error
			desired, err = physical.CanonicalDecodeVerified(document.Schema().Bytes(), physical.Digest(document.Schema().Fingerprint()), physical.Digest(document.SystemFingerprint()))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if desired.Version != 3 || bundle.Contract().FormatVersion() != 7 {
		t.Fatal("expected unchanged released physical3/contract7")
	}
	empty, err := physical.Normalize(physical.PhysicalSchema{Version: desired.Version, CanonicalVersion: desired.CanonicalVersion, Provider: desired.Provider, Namespace: desired.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := migration.Diff(empty, desired)
	if err != nil {
		t.Fatal(err)
	}
	before, err := physical.PhysicalFingerprint(empty)
	if err != nil {
		t.Fatal(err)
	}
	after, err := physical.PhysicalFingerprint(desired)
	if err != nil {
		t.Fatal(err)
	}
	allowlist, err := physical.UnmanagedAllowlistFingerprint(desired)
	if err != nil {
		t.Fatal(err)
	}
	entry := migration.ManifestEntry{ID: "0001_initial", BeforeModel: migration.Digest(compilerir.EmptyModelFingerprint()), AfterModel: migration.Digest(bundle.Model().Fingerprint().String()), BeforePhysical: migration.Digest(before.String()), AfterPhysical: migration.Digest(after.String()), BeforeSnapshot: empty, AfterSnapshot: desired, UnmanagedAllowlistDigest: migration.Digest(allowlist.String()), Operations: plan.Operations, Phases: plan.Phases}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
		if migration.RequiresApproval(operation) {
			entry.Approvals = append(entry.Approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	nativeProvider := native.New()
	script, err := nativeProvider.RenderMigration(entry)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"0001_initial.sql": []byte(script.SQL())}
	entry.Files = []migration.FileChecksum{{Path: "0001_initial.sql", SHA256: migration.Checksum(files["0001_initial.sql"])}}
	entry.ChainHash = migration.ChainHash(entry)
	manifest := migration.Manifest{FormatVersion: migration.ManifestFormatVersion, CanonicalVersion: migration.ManifestCanonicalVersion, HashAlgorithm: "sha256", GeneratorVersion: "released-backup-fixture", Provider: desired.Provider, Entries: []migration.ManifestEntry{entry}}
	manifestBytes, err := migration.EncodeManifest(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	documents := bundle.Providers()
	for i, document := range documents {
		if document.Provider() == golem.SQLite {
			documents[i] = golem.GeneratedProviderSchemaDocumentWithMigration(golem.SQLite, document.SystemFingerprint(), document.Schema(), golem.GeneratedMigrationManifestDocument(bundle.GenerationDigest(), golem.SQLite, manifestBytes))
		}
	}
	bundle = golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), bundle.Contract(), documents...)
	livePath := filepath.Join(dir, "released-source.db")
	database, err := publicsqlite.Open(ctx, publicsqlite.Config{DataSourceName: livePath})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	raw := (*providerhandle.Database)(database).UnsafeSQLX()
	if err := nativeProvider.ApplyMigration(ctx, raw, manifest, files); err != nil {
		t.Fatal(err)
	}
	bindings, err := fixture.GolemGeneratedApplicationBindings()
	if err != nil {
		t.Fatal(err)
	}
	descriptors, err := fixture.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := fixture.GolemGeneratedApplicationEventRegistry()
	if err != nil {
		t.Fatal(err)
	}
	factories, err := fixture.GolemGeneratedApplicationEventFactories()
	if err != nil {
		t.Fatal(err)
	}
	transport, err := events.NewMemoryTransport(events.MemoryLimits{Buffer: 16})
	if err != nil {
		t.Fatal(err)
	}
	app, err := golemruntime.Open(ctx, golemruntime.Config[fixture.Actor, fixture.Actor]{Database: database, Bundle: bundle, Bindings: bindings, Descriptors: descriptors, EventRegistry: registry, EventFactories: factories, EventTransport: transport,
		ReportEventOperator: func(_ context.Context, record events.OperatorAuditRecord) {
			t.Errorf("unexpected released event operator audit: action=%s outcome=%s", record.Action(), record.Outcome())
		},
		AfterCommitError: func(_ context.Context, _ golem.AfterCommitFailure) {
			t.Error("unexpected released after-commit failure")
		},
		ResolvePrincipal: func(_ context.Context, actor fixture.Actor) (fixture.Actor, error) { return actor, nil }})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []byte{7, 8} {
		if _, err := golemruntime.SystemCreate(ctx, app.System(), fixture.GolemGeneratedTeamDescriptor, fixture.Teams.Create(fixture.Teams.ID.Create(golem.UUID{15: id}), fixture.Teams.Owner.Create("alice"))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := golemruntime.SystemUpdate(ctx, app.System(), fixture.GolemGeneratedTeamDescriptor, fixture.Teams.ByID.Value(golem.UUID{15: 7}), fixture.Teams.Update(fixture.Teams.Owner.Set("alice-updated"))); err != nil {
		t.Fatal(err)
	}
	store, err := nativeProvider.QueueStore(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"pending", "leased"} {
		if _, err := store.Enqueue(ctx, nil, queueprovider.EnqueueRequest{ID: id, Type: "compat." + id, Payload: []byte(`{"released":true}`), MaxAttempts: 3}); err != nil {
			t.Fatal(err)
		}
	}
	leases, err := store.Claim(ctx, queueprovider.ClaimOptions{Types: []string{"compat.leased"}, Limit: 1, LeaseDuration: time.Minute})
	if err != nil || len(leases) != 1 {
		t.Fatalf("released claim: %v, leases=%d", err, len(leases))
	}
	var factCount, pendingGroups, pendingJobs, leasedJobs, priorityColumns int
	for _, check := range []struct {
		target *int
		query  string
	}{
		{&factCount, `SELECT count(*) FROM _golem_outbox WHERE fact_version=3`},
		{&pendingGroups, `SELECT count(*) FROM _golem_outbox_delivery WHERE status='pending'`},
		{&pendingJobs, `SELECT count(*) FROM golem_queue WHERE status='pending' AND attempt_count=0`},
		{&leasedJobs, `SELECT count(*) FROM golem_queue WHERE status='leased' AND attempt_count=1 AND lease_token IS NOT NULL`},
		{&priorityColumns, `SELECT count(*) FROM pragma_table_info('golem_queue') WHERE name='priority'`},
	} {
		if err := raw.GetContext(ctx, check.target, check.query); err != nil {
			t.Fatal(err)
		}
	}
	if factCount != 3 || pendingGroups != 3 || pendingJobs != 1 || leasedJobs != 1 || priorityColumns != 0 {
		t.Fatalf("released archive state: V3 facts=%d pending groups=%d pending jobs=%d leased jobs=%d priority columns=%d", factCount, pendingGroups, pendingJobs, leasedJobs, priorityColumns)
	}
	wire := releasedBackupBundleOf(bundle)
	wire.MigrationFiles = files
	wire.Evidence = make(map[string][]map[string]any)
	var tables []string
	if err := raw.SelectContext(ctx, &tables, `SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if table == "_golem_data_incarnation" {
			t.Fatal("released backup unexpectedly has data incarnation")
		}
		rows, err := raw.QueryxContext(ctx, `SELECT * FROM "`+strings.ReplaceAll(table, `"`, `""`)+`" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		wire.Evidence[table] = []map[string]any{}
		for rows.Next() {
			row := make(map[string]any)
			if err := rows.MapScan(row); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			// Detach driver-owned byte buffers before advancing the cursor.
			for key, value := range row {
				if bytes, ok := value.([]byte); ok {
					row[key] = append([]byte(nil), bytes...)
				}
			}
			wire.Evidence[table] = append(wire.Evidence[table], row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if len(wire.Evidence["_golem_outbox"]) != 3 || len(wire.Evidence["golem_queue"]) != 2 {
		t.Fatal("released runtime did not retain expected facts and jobs")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := publicsqlite.CheckpointForBackup(ctx, database); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(livePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	backup, err := os.OpenFile(filepath.Join(dir, "backup.db"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if _, err := io.Copy(backup, source); err != nil {
		t.Fatal(err)
	}
	if err := backup.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "backup.db"), 0400); err != nil {
		t.Fatal(err)
	}
	writeReleasedBackupWire(t, wire)
}
