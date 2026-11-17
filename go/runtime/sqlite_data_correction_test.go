package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	eventoutbox "github.com/eleven-am/golem/go/internal/event/outbox"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	internalsqlite "github.com/eleven-am/golem/go/internal/provider/sqlite"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	publicsqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

func TestSQLiteDataCorrectionRequiresFreshReviewedGenerationAndPreservesWork(t *testing.T) {
	source := sqliteRestoreFixture(t)
	source.mutate(t, golem.EventCreated, 91, 1, "needs-correction")
	source.mutate(t, golem.EventUpdated, 91, 1, "needs-correction")
	source.mutate(t, golem.EventCreated, 92, 1, "unchanged")
	store := sqliteRestoreQueue(t, source.app.database)
	for _, id := range []string{"pending", "leased"} {
		if _, err := store.Enqueue(source.ctx, nil, queueprovider.EnqueueRequest{ID: id, Type: id, Payload: []byte(`{"effect":"retain"}`), MaxAttempts: 5}); err != nil {
			t.Fatal(err)
		}
	}
	leases, err := store.Claim(source.ctx, queueprovider.ClaimOptions{Types: []string{"leased"}, Limit: 1, LeaseDuration: time.Minute})
	if err != nil || len(leases) != 1 {
		t.Fatalf("queue lease fixture: %v", err)
	}
	before := sqliteSnapshotRuntimeState(t, source.app.database)
	workBefore := sqliteRestoreQueueEvidence(t, source.app.database)
	oldIdentity := source.app.DataIncarnation()
	bundle, manifest, files := sqliteRuntimeCorrectionManifest(t, source)
	if bundle.GenerationDigest() == source.schema.Bundle.GenerationDigest() || bundle.Contract().Fingerprint() != source.schema.Bundle.Contract().Fingerprint() || bundle.Model().Fingerprint() != source.schema.Bundle.Model().Fingerprint() {
		t.Fatal("correction must change reviewed generation without changing model or client contract")
	}
	// Build every descriptor, binding, event registry and factory under the new
	// generation before fixture Open. Never transplant a generation into a live app.
	target := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture {
		graph := source.schema
		graph.Bundle = bundle
		graph.Registry, err = schema.New(bundle)
		if err != nil {
			t.Fatal(err)
		}
		return graph
	}})
	if !reflect.DeepEqual(source.schema.SQLite, target.schema.SQLite) {
		t.Fatal("data correction changed physical schema")
	}
	if source.app.EventCapabilities().PublisherRunning() || target.app.EventCapabilities().PublisherRunning() || source.applicationConfig.Queue != nil || target.applicationConfig.Queue != nil {
		t.Fatal("offline correction fixture started application work")
	}
	// App has no Close: stop its context and close the actual provider handle.
	// The deployment owner supplies this exclusion; migration does not prove death.
	source.stop()
	target.stop()
	if err := source.applicationConfig.Database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := target.applicationConfig.Database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := source.app.database.PingContext(context.Background()); err == nil {
		t.Fatal("old app still holds an open database")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	database, err := publicsqlite.Open(ctx, source.databaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	engine := internalsqlite.New()
	prefix, err := engine.ReadLedger(ctx, database.UnsafeSQLX())
	if err != nil || len(prefix) != 1 {
		t.Fatalf("starting ledger: %v", err)
	}
	if err := engine.ApplyMigration(ctx, database.UnsafeSQLX(), manifest, files); err != nil {
		t.Fatal(err)
	}
	newIdentity, err := engine.ReadDataIncarnation(ctx, database.UnsafeSQLX(), source.schema.SQLite.System)
	if err != nil || !newIdentity.Valid() || newIdentity == oldIdentity {
		t.Fatalf("data correction did not establish a fresh identity: %v", err)
	}
	ledger, err := engine.ReadLedger(ctx, database.UnsafeSQLX())
	if err != nil || len(ledger) != 2 || !reflect.DeepEqual(prefix, ledger[:1]) {
		t.Fatalf("correction rewrote reviewed prefix: %v", err)
	}
	if err := migration.VerifyLedger(manifest, ledger); err != nil {
		t.Fatal(err)
	}
	oldConfig := source.applicationConfig
	oldConfig.Database = database
	startup, err := prepareReviewedMigrationStartup(database, oldConfig.Bundle, golem.SQLite, source.schema.SQLite)
	if err != nil || startup == nil {
		t.Fatalf("old reviewed bundle cannot prepare startup: %v", err)
	}
	if err := verifyReviewedMigrationLedger(ctx, database.UnsafeSQLX(), golem.SQLite, source.schema.SQLite.System, startup); err == nil || !strings.Contains(err.Error(), "live migration ledger does not exactly match") {
		t.Fatalf("old reviewed ledger refused at unrelated boundary: %v", err)
	}
	if _, err := Open(ctx, oldConfig); err == nil || err.Error() != "P8_RUNTIME_MIGRATION: live migration state is incompatible" {
		t.Fatalf("old generated application did not fail at exact reviewed history: %v", err)
	}
	config := target.applicationConfig
	config.Database = database
	config.Bundle = bundle
	config.HistoricalEventBundles = []golem.SchemaBundle{source.schema.Bundle}
	fresh, err := Open(ctx, config)
	if err != nil {
		t.Fatalf("fresh reviewed startup with historical producer bundle: %v", err)
	}
	if fresh == source.app || fresh.registry == source.app.registry || fresh.eventSchemas == source.app.eventSchemas || fresh.DataIncarnation() != newIdentity || fresh.EventCapabilities().PublisherRunning() {
		t.Fatal("fresh startup reused old application state or enabled work")
	}
	if source.app.DataIncarnation() != oldIdentity {
		t.Fatal("old admitted identity was silently rebound")
	}
	after := sqliteSnapshotRuntimeState(t, database.UnsafeSQLX())
	if after.counter != before.counter+1 || len(after.versions) != 2 || after.versions[0] != after.counter || after.versions[1] != before.versions[1] {
		t.Fatalf("changed/unchanged row versions: before=%+v after=%+v", before, after)
	}
	if !reflect.DeepEqual(before.facts, after.facts) || !reflect.DeepEqual(before.deliveries, after.deliveries) {
		t.Fatal("offline correction emitted or rewrote request evidence")
	}
	if !bytes.Equal(workBefore, sqliteRestoreQueueEvidence(t, database.UnsafeSQLX())) {
		t.Fatal("offline correction altered archived queue attempts, payloads, state or lease")
	}
	rows, err := SystemFindMany(ctx, fresh.System(), target.posts, golem.OrderBy(target.postID.Asc()), golem.Select[policyFactRuntimePost](target.title))
	if err != nil || len(rows) != 2 {
		t.Fatalf("fresh generated read: rows=%d error=%v", len(rows), err)
	}
	for index, want := range []string{"corrected", "unchanged"} {
		actual, present := golem.Value(rows[index], target.title).Get()
		if !present || actual != want {
			t.Fatalf("row %d title=%q present=%t", index, actual, present)
		}
	}
	groups := 0
	if err := engine.WalkRestoreFacts(ctx, database.UnsafeSQLX(), source.schema.SQLite.System, func(group eventprovider.Lease) error {
		groups++
		for _, fact := range group.Facts {
			if fact.GenerationFingerprint != source.schema.Bundle.GenerationDigest().String() {
				t.Fatal("retained fact lost its producer generation")
			}
		}
		_, err := eventoutbox.ValidateDeliveryCausation(group.Delivery.CausationID, group.Facts, fresh.eventSchemas, fresh.eventSchemas, eventoutbox.ValidationLimits{})
		return err
	}); err != nil || groups != 3 {
		t.Fatalf("historical evidence no longer deliverable: groups=%d error=%v", groups, err)
	}
	if err := engine.ApplyMigration(ctx, database.UnsafeSQLX(), manifest, files); err == nil {
		t.Fatal("applied data-only entry replayed")
	}
	stable, err := engine.ReadDataIncarnation(ctx, database.UnsafeSQLX(), source.schema.SQLite.System)
	if err != nil || stable != newIdentity {
		t.Fatal("replay changed incarnation", err)
	}
}

func sqliteRuntimeCorrectionManifest(t *testing.T, source policyFactRuntimeFixture) (golem.SchemaBundle, migration.Manifest, map[string][]byte) {
	t.Helper()
	encoded, err := source.schema.Bundle.MigrationManifest(golem.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := migration.ParseManifest(encoded)
	if err != nil || len(manifest.Entries) != 1 {
		t.Fatalf("source manifest: %v", err)
	}
	initial := manifest.Entries[0]
	engine := internalsqlite.New()
	script, err := engine.RenderMigration(initial)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Files) != 1 || len(initial.Manual) != 0 {
		t.Fatal("unexpected source fixture history")
	}
	files := map[string][]byte{initial.Files[0].Path: []byte(script.SQL())}
	if migration.Checksum(files[initial.Files[0].Path]) != initial.Files[0].SHA256 {
		t.Fatal("source reviewed SQL does not reproduce")
	}
	field := compilerir.FieldID(hex.EncodeToString(source.schema.PostTitle[:]))
	plan, err := migration.DataCorrectionPlan(initial.AfterSnapshot, field)
	if err != nil {
		t.Fatal(err)
	}
	entry := migration.ManifestEntry{ID: "0002_correct_title", ParentID: initial.ID, ParentChainHash: initial.ChainHash, BeforeModel: initial.AfterModel, AfterModel: initial.AfterModel, BeforePhysical: initial.AfterPhysical, AfterPhysical: initial.AfterPhysical, BeforeSnapshot: initial.AfterSnapshot, AfterSnapshot: initial.AfterSnapshot, UnmanagedAllowlistDigest: initial.UnmanagedAllowlistDigest, Operations: plan.Operations, Phases: plan.Phases}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
		if migration.RequiresApproval(operation) {
			entry.Approvals = append(entry.Approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	const companion = "migrations/sqlite/0002_correct_title.data.sql"
	files[companion] = []byte("UPDATE posts SET title=CASE WHEN title='needs-correction' THEN 'corrected' ELSE title END;\n")
	entry.Manual = []migration.ManualCompanion{{OperationID: plan.Operations[0].ID, File: migration.FileChecksum{Path: companion, SHA256: migration.Checksum(files[companion])}, Postcondition: migration.DataCorrectionPostcondition(compilerir.ModelID(hex.EncodeToString(source.schema.Post[:])), field)}}
	script, err = engine.RenderMigration(entry)
	if err != nil {
		t.Fatal(err)
	}
	const path = "migrations/sqlite/0002_correct_title.sql"
	files[path] = []byte(script.SQL())
	entry.Files = []migration.FileChecksum{{Path: path, SHA256: migration.Checksum(files[path])}}
	entry.ChainHash = migration.ChainHash(entry)
	manifest.Entries = append(manifest.Entries, entry)
	encoded, err = migration.EncodeManifest(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	generation := golem.SchemaDigest(sha256.Sum256(append([]byte("sqlite-runtime-data-correction:"), encoded...)))
	previous := source.schema.Bundle
	providers := previous.Providers()
	for index, document := range providers {
		if document.Provider() == golem.SQLite {
			providers[index] = golem.GeneratedProviderSchemaDocumentWithMigration(golem.SQLite, document.SystemFingerprint(), document.Schema(), golem.GeneratedMigrationManifestDocument(generation, golem.SQLite, encoded))
		}
	}
	bundle := golem.GeneratedSchemaBundle(generation, previous.GeneratorVersion(), previous.TemplateABIVersion(), previous.Model(), previous.Contract(), providers...)
	return bundle, manifest, files
}
