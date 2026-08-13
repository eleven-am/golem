package migration

import (
	"encoding/json"
	"fmt"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
	"math/rand"
	"strings"
	"testing"
)

func TestDeterministicTopologicalOrder(t *testing.T) {
	ops := []Operation{{ID: "c", Kind: AddForeignKey, Stage: 50, ObjectID: "c", Dependencies: []OperationID{"a", "b"}}, {ID: "b", Kind: AddColumn, Stage: 30, ObjectID: "b"}, {ID: "a", Kind: CreateTable, Stage: 20, ObjectID: "a"}}
	var baseline string
	for seed := int64(0); seed < 50; seed++ {
		values := append([]Operation(nil), ops...)
		rand.New(rand.NewSource(seed)).Shuffle(len(values), func(i, j int) { values[i], values[j] = values[j], values[i] })
		ordered, err := Order(Plan{Operations: values})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(ordered)
		if seed == 0 {
			baseline = string(encoded)
		} else if string(encoded) != baseline {
			t.Fatalf("seed %d changed order", seed)
		}
	}
}
func TestDependencyCycleRejected(t *testing.T) {
	_, err := Order(Plan{Operations: []Operation{{ID: "a", Dependencies: []OperationID{"b"}}, {ID: "b", Dependencies: []OperationID{"a"}}}})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error=%v", err)
	}
}
func TestApprovalIsExactAndObjectScoped(t *testing.T) {
	before, after, wrong := Checksum([]byte("before")), Checksum([]byte("after")), Checksum([]byte("wrong"))
	planBefore, planAfter := Checksum([]byte("schema-before")), Checksum([]byte("schema-after"))
	op := Operation{ID: "drop", Kind: DropColumn, ObjectID: "field", Before: before, After: after, Mode: Transactional, Risk: RiskDataLoss}
	plan := Plan{
		Provider:          ir.SQLite,
		BeforeFingerprint: planBefore,
		AfterFingerprint:  planAfter,
		Operations:        []Operation{op},
		Phases: []Phase{{
			Ordinal: 0, Mode: Transactional, Operations: []OperationID{op.ID},
			BeforeFingerprint: planBefore, AfterFingerprint: planAfter,
		}},
	}
	if ValidatePlan(plan, nil) == nil {
		t.Fatal("unapproved data loss accepted")
	}
	if ValidatePlan(plan, []Approval{{OperationID: "drop", Risk: RiskDataLoss, Before: wrong, After: after}}) == nil {
		t.Fatal("stale approval accepted")
	}
	if err := ValidatePlan(plan, []Approval{{OperationID: "drop", Risk: RiskDataLoss, Before: before, After: after}}); err != nil {
		t.Fatal(err)
	}
}
func TestTypedDiffInitialRenameAndTypeChange(t *testing.T) {
	empty := schema()
	desired := schema()
	desired.Tables = []physical.PhysicalTable{{
		ID: "0123456789abcdef0123456789abcdef", Name: "users",
		Columns: []physical.PhysicalColumn{{ID: "1123456789abcdef0123456789abcdef", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}},
	}}
	initial, err := Diff(empty, desired)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Initial || initial.Operations[0].Kind != CreateTable {
		t.Fatalf("initial=%#v", initial)
	}
	renamed := desired
	renamed.Tables = append([]physical.PhysicalTable(nil), desired.Tables...)
	renamed.Tables[0].Name = "members"
	rename, err := Diff(desired, renamed)
	if err != nil {
		t.Fatal(err)
	}
	if operationKinds(rename.Operations, RecordSchemaVersion)[0] != RenameTable || rename.Operations[len(rename.Operations)-1].Kind != RecordSchemaVersion {
		t.Fatalf("rename=%#v", rename)
	}
	changed := renamed
	changed.Tables = append([]physical.PhysicalTable(nil), renamed.Tables...)
	changed.Tables[0].Columns = append([]physical.PhysicalColumn(nil), renamed.Tables[0].Columns...)
	changed.Tables[0].Columns[0].Storage.Kind = physical.StorageSQLiteText
	plan, err := Diff(renamed, changed)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Operations[0].Kind != AlterColumnType || plan.Operations[0].Risk != RiskDataLoss {
		t.Fatalf("type change=%#v", plan.Operations)
	}
}

func TestSemanticExtensionDiffIsAdditiveAndDropsOnlyWithDataLoss(t *testing.T) {
	modelID := ir.ModelID("0123456789abcdef0123456789abcdef")
	fieldID := ir.FieldID("1123456789abcdef0123456789abcdef")
	desired := schema()
	desired.Tables = []physical.PhysicalTable{{
		ID: modelID, Name: "posts",
		Columns:    []physical.PhysicalColumn{{ID: fieldID, Name: "title", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}},
		PrimaryKey: &physical.PhysicalKey{ID: "2123456789abcdef0123456789abcdef", Name: "pk_posts", Columns: []ir.FieldID{fieldID}},
	}}
	payload, _ := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: 3, Fields: []string{string(fieldID)}, Metric: "cosine"})
	extension, err := semanticstorage.Lower(ir.ProviderExtensionIR{ID: "3123456789abcdef0123456789abcdef", Provider: ir.SQLite, Version: 1, Owner: ir.ObjectID(modelID), Kind: semanticcontract.IndexKind, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	desired.Extensions = []physical.Extension{extension}
	plan, err := Diff(schema(), desired)
	if err != nil {
		t.Fatal(err)
	}
	kinds := operationKinds(plan.Operations, RecordSchemaVersion)
	if len(kinds) != 2 || kinds[0] != CreateTable || kinds[1] != CreateProviderExtension {
		t.Fatalf("semantic add operations=%v", kinds)
	}
	var create Operation
	for _, operation := range plan.Operations {
		if operation.Kind == CreateProviderExtension {
			create = operation
		}
	}
	if len(create.Dependencies) != 1 {
		t.Fatalf("semantic extension lacks owner-table dependency: %#v", create)
	}

	without := desired
	without.Extensions = nil
	drop, err := Diff(desired, without)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range drop.Operations {
		if operation.Kind == DropProviderExtension && operation.Risk == RiskDataLoss {
			return
		}
	}
	t.Fatalf("semantic drop is not explicit data loss: %#v", drop.Operations)
}

func TestSemanticExtensionChangeIsReviewedOrderedRewrite(t *testing.T) {
	modelID := ir.ModelID("0123456789abcdef0123456789abcdef")
	identityID := ir.FieldID("1123456789abcdef0123456789abcdef")
	titleID := ir.FieldID("1223456789abcdef0123456789abcdef")
	bodyID := ir.FieldID("1323456789abcdef0123456789abcdef")
	base := schema()
	base.Tables = []physical.PhysicalTable{{
		ID: modelID, Name: "posts",
		Columns: []physical.PhysicalColumn{
			{ID: identityID, Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			{ID: titleID, Name: "title", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			{ID: bodyID, Name: "body", Ordinal: 2, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "2123456789abcdef0123456789abcdef", Name: "pk_posts", Columns: []ir.FieldID{identityID}},
	}}
	extensionID := ir.ExtensionID("3123456789abcdef0123456789abcdef")
	semanticExtension := func(dimensions uint16, fields ...ir.FieldID) physical.Extension {
		t.Helper()
		encodedFields := make([]string, len(fields))
		for index := range fields {
			encodedFields[index] = string(fields[index])
		}
		payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "related", Space: "content", Dimensions: dimensions, Fields: encodedFields, Metric: "cosine"})
		if err != nil {
			t.Fatal(err)
		}
		extension, err := semanticstorage.Lower(ir.ProviderExtensionIR{ID: extensionID, Provider: ir.SQLite, Version: 1, Owner: ir.ObjectID(modelID), Kind: semanticcontract.IndexKind, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		return extension
	}
	before := base
	before.Extensions = []physical.Extension{semanticExtension(3, titleID)}
	after := base
	after.Extensions = []physical.Extension{semanticExtension(4, titleID, bodyID)}
	plan, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	var drop, create Operation
	for _, operation := range plan.Operations {
		switch operation.Kind {
		case DropProviderExtension:
			drop = operation
		case CreateProviderExtension:
			create = operation
		}
	}
	if drop.ID == "" || create.ID == "" || drop.Risk != RiskRewrite || create.Risk != RiskRewrite {
		t.Fatalf("semantic rewrite operations=%#v", plan.Operations)
	}
	if len(create.Dependencies) != 1 || create.Dependencies[0] != drop.ID {
		t.Fatalf("semantic recreate does not depend on drop: drop=%#v create=%#v", drop, create)
	}
	ordered, err := Order(plan)
	if err != nil {
		t.Fatal(err)
	}
	positions := map[OperationKind]int{}
	for index, operation := range ordered {
		positions[operation.Kind] = index
	}
	if positions[DropProviderExtension] >= positions[CreateProviderExtension] {
		t.Fatalf("semantic rewrite order=%#v", ordered)
	}
	if err := ValidatePlan(plan, nil); err != nil {
		t.Fatalf("derived semantic rewrite required destructive approval: %v", err)
	}
}

func TestUnknownProviderExtensionChangeRemainsClosed(t *testing.T) {
	base := schema()
	owner := physical.ObjectRef{Kind: ir.ObjectModel, ModelID: "0123456789abcdef0123456789abcdef"}
	before := base
	before.Extensions = []physical.Extension{{
		ID: "3123456789abcdef0123456789abcdef", Provider: ir.SQLite,
		Kind: "example.unknown", Version: 1, Owner: owner,
	}}
	after := base
	after.Extensions = []physical.Extension{{
		ID: "3123456789abcdef0123456789abcdef", Provider: ir.SQLite,
		Kind: "example.unknown", Version: 1, Owner: owner,
		Attributes: []physical.Attribute{{Name: "changed", Value: physical.SemanticValue{Kind: physical.ValueBool, Bool: true}}},
	}}
	if _, err := Diff(before, after); err == nil || !strings.Contains(err.Error(), "cannot change in place") {
		t.Fatalf("unknown same-ID provider extension change error=%v", err)
	}
}

func TestSQLiteRuntimeTransitionIsReviewedAsMetadataOnly(t *testing.T) {
	before := schema()
	before.Provider.Driver = physical.DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}
	after := schema()
	after.Provider = physical.SQLiteManifest(physical.CapabilityFact{ID: "sqlite.vec0.v1", Version: 1, Verification: physical.VerificationRuntimeProbe})
	plan, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 || plan.Operations[0].Kind != RecordSchemaVersion || plan.BeforeFingerprint == plan.AfterFingerprint {
		t.Fatalf("provider-runtime transition plan=%#v", plan)
	}
}

func TestDiffEmitsExplicitFirstSystemBootstrap(t *testing.T) {
	before := schema()
	after := schema()
	after.System = physical.SystemSchema{Version: 1, Namespace: physical.Namespace{Name: "main"}, Objects: []physical.SystemObject{
		{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"},
		{ID: physical.MigrationLockObjectIDV1, Kind: physical.SystemMigrationLock, Version: 1, Name: "_golem_migration_lock"},
	}}
	plan, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Initial || len(plan.Operations) != 2 || plan.Operations[0].Kind != BootstrapSystemSchema || plan.Operations[1].Kind != RecordSchemaVersion {
		t.Fatalf("bootstrap plan=%#v", plan)
	}
	if len(plan.Operations[1].Dependencies) != 1 || plan.Operations[1].Dependencies[0] != plan.Operations[0].ID {
		t.Fatalf("record is not terminal after bootstrap: %#v", plan.Operations)
	}
}

func TestDiffAllowsOnlyRegisteredOutboxSystemUpgrade(t *testing.T) {
	before := schema()
	before.System = physical.SystemSchema{Version: 1, Namespace: physical.Namespace{Name: "main"}, Objects: []physical.SystemObject{
		{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"},
		{ID: physical.MigrationLockObjectIDV1, Kind: physical.SystemMigrationLock, Version: 1, Name: "_golem_migration_lock"},
	}}
	after := before
	after.System.Objects = append(append([]physical.SystemObject(nil), before.System.Objects...), physical.OutboxSystemObjectV1())
	beforePhysical, _ := physical.PhysicalFingerprint(before)
	afterPhysical, _ := physical.PhysicalFingerprint(after)
	beforeSystem, _ := physical.SystemFingerprint(before.Provider, before.System)
	afterSystem, _ := physical.SystemFingerprint(after.Provider, after.System)
	if beforePhysical != afterPhysical || beforeSystem == afterSystem {
		t.Fatal("system upgrade did not preserve physical and change system fingerprint domains")
	}
	plan, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Initial || len(plan.Operations) != 2 || plan.Operations[0].Kind != AddSystemObject || plan.Operations[0].ObjectID != string(physical.OutboxObjectIDV1) || plan.Operations[1].Kind != RecordSchemaVersion {
		t.Fatalf("outbox upgrade plan=%#v", plan)
	}
	forged := after
	forged.System.Objects = append([]physical.SystemObject(nil), after.System.Objects...)
	forged.System.Objects[2].Name = "_golem_other"
	if _, err := Diff(before, forged); err == nil {
		t.Fatal("forged system addition was accepted")
	}
	if _, err := Diff(after, before); err == nil {
		t.Fatal("system object removal was accepted")
	}
}

func TestDiffAllowsRegisteredUpsertGuardSystemUpgrade(t *testing.T) {
	before := schema()
	before.System = physical.SystemSchema{Version: 1, Namespace: physical.Namespace{Name: "main"}, Objects: []physical.SystemObject{
		{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"},
		{ID: physical.MigrationLockObjectIDV1, Kind: physical.SystemMigrationLock, Version: 1, Name: "_golem_migration_lock"},
		physical.OutboxSystemObjectV1(),
	}}
	after := before
	after.System.Objects = append(append([]physical.SystemObject(nil), before.System.Objects...), physical.UpsertGuardSystemObjectV1())
	plan, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Initial || len(plan.Operations) != 2 || plan.Operations[0].Kind != AddSystemObject || plan.Operations[0].ObjectID != string(physical.UpsertGuardObjectIDV1) || plan.Operations[1].Kind != RecordSchemaVersion {
		t.Fatalf("upsert guard upgrade plan=%#v", plan)
	}
}

func TestDiffAllowsRegisteredOutboxDeliverySystemUpgrade(t *testing.T) {
	before := schema()
	before.System = physical.SystemSchema{Version: 1, Namespace: physical.Namespace{Name: "main"}, Objects: []physical.SystemObject{
		{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"},
		{ID: physical.MigrationLockObjectIDV1, Kind: physical.SystemMigrationLock, Version: 1, Name: "_golem_migration_lock"},
		physical.OutboxSystemObjectV1(),
		physical.UpsertGuardSystemObjectV1(),
	}}
	after := before
	after.System.Objects = append(append([]physical.SystemObject(nil), before.System.Objects...), physical.OutboxDeliverySystemObjectV1())
	beforePhysical, _ := physical.PhysicalFingerprint(before)
	afterPhysical, _ := physical.PhysicalFingerprint(after)
	beforeSystem, _ := physical.SystemFingerprint(before.Provider, before.System)
	afterSystem, _ := physical.SystemFingerprint(after.Provider, after.System)
	if beforePhysical != afterPhysical || beforeSystem == afterSystem {
		t.Fatal("delivery upgrade did not preserve physical and change system fingerprint domains")
	}
	plan, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Initial || len(plan.Operations) != 2 || plan.Operations[0].Kind != AddSystemObject || plan.Operations[0].ObjectID != string(physical.OutboxDeliveryObjectIDV1) || plan.Operations[1].Kind != RecordSchemaVersion {
		t.Fatalf("outbox delivery upgrade plan=%#v", plan)
	}
	forged := after
	forged.System.Objects = append([]physical.SystemObject(nil), after.System.Objects...)
	forged.System.Objects[len(forged.System.Objects)-1].Version = 2
	if _, err := Diff(before, forged); err == nil {
		t.Fatal("forged outbox delivery system object was accepted")
	}
}

func TestTypeChangeDropsAndRestoresUnchangedIndexAndForeignKey(t *testing.T) {
	before := relatedSchema()
	after := relatedSchema()
	after.Tables[0].Columns[0].Storage.Kind = physical.StorageSQLiteInteger
	after.Tables[1].Columns[0].Storage.Kind = physical.StorageSQLiteInteger

	plan, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[OperationKind][]Operation{}
	for _, operation := range plan.Operations {
		byKind[operation.Kind] = append(byKind[operation.Kind], operation)
	}
	if len(byKind[AlterColumnType]) != 2 || len(byKind[DropForeignKey]) != 1 || len(byKind[AddForeignKey]) != 1 || len(byKind[DropIndex]) != 1 || len(byKind[CreateIndex]) != 1 {
		t.Fatalf("destructive subgraph kinds = %#v", byKind)
	}
	assertDependsOnKind(t, plan.Operations, byKind[AlterColumnType][0], DropForeignKey)
	assertDependsOnKind(t, plan.Operations, byKind[AddForeignKey][0], AlterColumnType)
	assertDependsOnKind(t, plan.Operations, byKind[CreateIndex][0], AlterColumnType)
	assertDependsOnKind(t, plan.Operations, byKind[AddForeignKey][0], DropForeignKey)
	assertDependsOnKind(t, plan.Operations, byKind[CreateIndex][0], DropIndex)
}
func TestManifestRewriteStaleSnapshotAndChainHash(t *testing.T) {
	content := []byte("reviewed migration")
	before, after := schema(), schema()
	after.Unmanaged = []physical.UnmanagedObject{{Kind: "view", Name: "legacy"}}
	bf, _ := physical.PhysicalFingerprint(before)
	af, _ := physical.PhysicalFingerprint(after)
	entry := ManifestEntry{
		ID: "001", Files: []FileChecksum{{Path: "001.sql", SHA256: Checksum(content)}},
		BeforeModel: Checksum([]byte("model-before")), AfterModel: Checksum([]byte("model-after")), BeforePhysical: Digest(bf.String()), AfterPhysical: Digest(af.String()), BeforeSnapshot: before, AfterSnapshot: after,
		UnmanagedAllowlistDigest: allowlistDigest(after),
		Operations:               []Operation{{ID: "apply-001", Kind: RecordSchemaVersion, Mode: Transactional, Risk: RiskSafe}},
		Phases:                   []Phase{{Ordinal: 0, Mode: Transactional, Operations: []OperationID{"apply-001"}, BeforeFingerprint: Digest(bf.String()), AfterFingerprint: Digest(af.String())}},
		Risks:                    []OperationRisk{{OperationID: "apply-001", Risk: RiskSafe}},
	}
	entry.ChainHash = ChainHash(entry)
	manifest := testManifest(entry)
	if err := VerifyManifest(manifest, map[string][]byte{"001.sql": content}); err != nil {
		t.Fatal(err)
	}
	if VerifyManifest(manifest, map[string][]byte{"001.sql": []byte("rewritten")}) == nil {
		t.Fatal("rewrite accepted")
	}
	stale := manifest
	stale.Entries = append([]ManifestEntry(nil), manifest.Entries...)
	stale.Entries[0].AfterPhysical = Checksum([]byte("stale"))
	if VerifyManifest(stale, map[string][]byte{"001.sql": content}) == nil {
		t.Fatal("stale snapshot accepted")
	}
	changed := manifest
	changed.Entries = append([]ManifestEntry(nil), manifest.Entries...)
	changed.Entries[0].Risks = []OperationRisk{{OperationID: "apply-001", Risk: RiskLocking}}
	if VerifyManifest(changed, map[string][]byte{"001.sql": content}) == nil {
		t.Fatal("immutable metadata rewrite accepted")
	}
	staleAllowlist := manifest
	staleAllowlist.Entries = append([]ManifestEntry(nil), manifest.Entries...)
	staleAllowlist.Entries[0].UnmanagedAllowlistDigest = Checksum([]byte("stale allowlist"))
	staleAllowlist.Entries[0].ChainHash = ChainHash(staleAllowlist.Entries[0])
	if err := VerifyManifest(staleAllowlist, map[string][]byte{"001.sql": content}); err == nil || !strings.Contains(err.Error(), "allowlist digest") {
		t.Fatalf("stale allowlist digest error=%v", err)
	}
}

func TestEmbeddedManifestVerificationDoesNotRequireReviewedFileBytes(t *testing.T) {
	content := []byte("SELECT 1;")
	snapshot := schema()
	fingerprint, err := physical.PhysicalFingerprint(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	entry := ManifestEntry{
		ID: "001", Files: []FileChecksum{{Path: "001.sql", SHA256: Checksum(content)}},
		BeforeModel: Checksum([]byte("model")), AfterModel: Checksum([]byte("model")),
		BeforePhysical: Digest(fingerprint.String()), AfterPhysical: Digest(fingerprint.String()),
		BeforeSnapshot: snapshot, AfterSnapshot: snapshot, UnmanagedAllowlistDigest: allowlistDigest(snapshot),
	}
	entry.ChainHash = ChainHash(entry)
	manifest := testManifest(entry)
	if err := VerifyManifest(manifest, map[string][]byte{"001.sql": content}); err != nil {
		t.Fatal(err)
	}
	if err := VerifyEmbeddedManifest(manifest); err != nil {
		t.Fatalf("embedded manifest rejected: %v", err)
	}
	tampered := manifest
	tampered.Entries = append([]ManifestEntry(nil), manifest.Entries...)
	tampered.Entries[0].Files = append([]FileChecksum(nil), manifest.Entries[0].Files...)
	tampered.Entries[0].Files[0].SHA256 = Checksum([]byte("rewritten"))
	if err := VerifyEmbeddedManifest(tampered); err == nil {
		t.Fatal("rewritten embedded checksum was accepted")
	}
}

func TestManifestRejectsUnsupportedSystemSchemaChange(t *testing.T) {
	snapshot := schema()
	snapshot.System = physical.SystemSchema{Version: 1, Namespace: physical.Namespace{Name: "main"}, Objects: []physical.SystemObject{{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"}}}
	snapshot, err := physical.Normalize(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, _ := physical.PhysicalFingerprint(snapshot)
	entry := ManifestEntry{ID: "001", BeforeModel: Checksum([]byte("model")), AfterModel: Checksum([]byte("model")), BeforePhysical: Digest(fingerprint.String()), AfterPhysical: Digest(fingerprint.String()), BeforeSnapshot: snapshot, AfterSnapshot: snapshot, UnmanagedAllowlistDigest: allowlistDigest(snapshot)}
	entry.AfterSnapshot.System.Objects = append([]physical.SystemObject(nil), snapshot.System.Objects...)
	entry.AfterSnapshot.System.Objects[0].Version = 2
	entry.ChainHash = ChainHash(entry)
	err = VerifyManifest(testManifest(entry), map[string][]byte{})
	if err == nil || !strings.Contains(err.Error(), "system schema") {
		t.Fatalf("error=%v, want unsupported system-schema change", err)
	}
}
func TestLedgerFailureNeverLooksApplied(t *testing.T) {
	snapshot := schema()
	fp, _ := physical.PhysicalFingerprint(snapshot)
	entry := ManifestEntry{ID: "001", BeforeModel: Checksum([]byte("model")), AfterModel: Checksum([]byte("model")), BeforePhysical: Digest(fp.String()), AfterPhysical: Digest(fp.String()), BeforeSnapshot: snapshot, AfterSnapshot: snapshot, UnmanagedAllowlistDigest: allowlistDigest(snapshot)}
	entry.ChainHash = ChainHash(entry)
	manifest := testManifest(entry)
	ledger := []LedgerEntry{{MigrationID: "001", ChainHash: entry.ChainHash, BeforePhysical: entry.BeforePhysical, AfterPhysical: entry.AfterPhysical, Phases: []LedgerPhase{{Ordinal: 0, Status: PhaseFailed, AfterFingerprint: entry.AfterPhysical}}}}
	if VerifyLedger(manifest, ledger) == nil {
		t.Fatal("failed phase accepted")
	}
}

func TestManifestRejectsProviderPhasePathAndDuplicateIDTampering(t *testing.T) {
	snapshot := schema()
	fingerprint, _ := physical.PhysicalFingerprint(snapshot)
	entry := ManifestEntry{ID: "001", BeforeModel: Checksum([]byte("model")), AfterModel: Checksum([]byte("model")), BeforePhysical: Digest(fingerprint.String()), AfterPhysical: Digest(fingerprint.String()), BeforeSnapshot: snapshot, AfterSnapshot: snapshot, UnmanagedAllowlistDigest: allowlistDigest(snapshot)}
	entry.ChainHash = ChainHash(entry)

	tests := []struct {
		name  string
		edit  func(*Manifest, map[string][]byte)
		match string
	}{
		{"provider", func(manifest *Manifest, _ map[string][]byte) { manifest.Provider = physical.PostgreSQLManifest() }, "provider"},
		{"phase", func(manifest *Manifest, _ map[string][]byte) {
			manifest.Entries[0].Phases = []Phase{{Ordinal: 1, Mode: Transactional, Operations: []OperationID{"op"}, BeforeFingerprint: Digest(fingerprint.String()), AfterFingerprint: Digest(fingerprint.String())}}
			manifest.Entries[0].Risks = []OperationRisk{{OperationID: "op", Risk: RiskSafe}}
			manifest.Entries[0].ChainHash = ChainHash(manifest.Entries[0])
		}, "phase"},
		{"unsafe path", func(manifest *Manifest, files map[string][]byte) {
			files["../001.sql"] = []byte("x")
			manifest.Entries[0].Files = []FileChecksum{{Path: "../001.sql", SHA256: Checksum([]byte("x"))}}
			manifest.Entries[0].ChainHash = ChainHash(manifest.Entries[0])
		}, "path"},
		{"duplicate ID", func(manifest *Manifest, _ map[string][]byte) {
			second := manifest.Entries[0]
			second.ParentID, second.ParentChainHash = manifest.Entries[0].ID, manifest.Entries[0].ChainHash
			second.ChainHash = ChainHash(second)
			manifest.Entries = append(manifest.Entries, second)
		}, "duplicate ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := testManifest(entry)
			files := map[string][]byte{}
			test.edit(&manifest, files)
			err := VerifyManifest(manifest, files)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("error = %v; want %q", err, test.match)
			}
		})
	}
}

func TestManifestRejectsStaleApprovalDigestsAfterChainRehash(t *testing.T) {
	snapshot := schema()
	fingerprint, _ := physical.PhysicalFingerprint(snapshot)
	operation := Operation{ID: "drop", Kind: DropColumn, ObjectID: "field", Before: Checksum([]byte("object-before")), After: Checksum([]byte("object-after")), Mode: Transactional, Risk: RiskDataLoss}
	entry := ManifestEntry{
		ID: "001", BeforeModel: Checksum([]byte("model")), AfterModel: Checksum([]byte("model")), BeforePhysical: Digest(fingerprint.String()), AfterPhysical: Digest(fingerprint.String()), BeforeSnapshot: snapshot, AfterSnapshot: snapshot, UnmanagedAllowlistDigest: allowlistDigest(snapshot),
		Operations: []Operation{operation},
		Phases:     []Phase{{Ordinal: 0, Mode: Transactional, Operations: []OperationID{operation.ID}, BeforeFingerprint: Digest(fingerprint.String()), AfterFingerprint: Digest(fingerprint.String())}},
		Risks:      []OperationRisk{{OperationID: operation.ID, Risk: operation.Risk}},
		Approvals:  []Approval{{OperationID: operation.ID, Risk: operation.Risk, Before: Checksum([]byte("stale")), After: operation.After}},
	}
	entry.ChainHash = ChainHash(entry)
	err := VerifyManifest(testManifest(entry), map[string][]byte{})
	if err == nil || !strings.Contains(err.Error(), "not exact") {
		t.Fatalf("error = %v; want stale exact approval rejection", err)
	}
}

func TestManifestRejectsDiscontinuousModelHistory(t *testing.T) {
	snapshot := schema()
	fingerprint, _ := physical.PhysicalFingerprint(snapshot)
	first := ManifestEntry{ID: "001", BeforeModel: Checksum([]byte("model-a")), AfterModel: Checksum([]byte("model-b")), BeforePhysical: Digest(fingerprint.String()), AfterPhysical: Digest(fingerprint.String()), BeforeSnapshot: snapshot, AfterSnapshot: snapshot, UnmanagedAllowlistDigest: allowlistDigest(snapshot)}
	first.ChainHash = ChainHash(first)
	second := ManifestEntry{ID: "002", ParentID: first.ID, ParentChainHash: first.ChainHash, BeforeModel: Checksum([]byte("stale-model")), AfterModel: Checksum([]byte("model-c")), BeforePhysical: Digest(fingerprint.String()), AfterPhysical: Digest(fingerprint.String()), BeforeSnapshot: snapshot, AfterSnapshot: snapshot, UnmanagedAllowlistDigest: allowlistDigest(snapshot)}
	second.ChainHash = ChainHash(second)
	err := VerifyManifest(testManifest(first, second), map[string][]byte{})
	if err == nil || !strings.Contains(err.Error(), "model history") {
		t.Fatalf("error = %v; want model continuity rejection", err)
	}
}

func TestDiffReviewedAcceptsOnlyClosedPhysicalVersionPairs(t *testing.T) {
	v3 := physical.PhysicalSchema{Version: 3, CanonicalVersion: 3, Provider: physical.SQLiteManifest(), Namespace: physical.Namespace{Name: "main"}}
	v2 := v3
	v2.Version, v2.CanonicalVersion = 2, 2
	v1 := v2
	v1.Version, v1.CanonicalVersion = 1, 1
	v1.Provider.Driver = physical.DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}
	mixedBefore := v3
	mixedBefore.Version = 1
	mixedAfter := v3
	mixedAfter.CanonicalVersion = 1
	future := v3
	future.Version, future.CanonicalVersion = 4, 4
	for _, test := range []struct {
		name          string
		before, after physical.PhysicalSchema
		accepted      bool
	}{
		{name: "v1 same", before: v1, after: v1, accepted: true},
		{name: "v1 to v2", before: v1, after: v2, accepted: true},
		{name: "v2 same", before: v2, after: v2, accepted: true},
		{name: "v2 to v3", before: v2, after: v3, accepted: true},
		{name: "v3 same", before: v3, after: v3, accepted: true},
		{name: "v1 to v3 composed", before: v1, after: v3, accepted: true},
		{name: "v3 downgrade", before: v3, after: v2},
		{name: "v2 downgrade", before: v2, after: v1},
		{name: "mixed before axis", before: mixedBefore, after: v3},
		{name: "mixed after axis", before: v1, after: mixedAfter},
		{name: "future", before: v3, after: future},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := DiffReviewed(test.before, test.after)
			if test.accepted && err != nil {
				t.Fatal(err)
			}
			if !test.accepted && (err == nil || !strings.Contains(err.Error(), "unsupported reviewed physical version pair")) {
				t.Fatalf("error = %v; want closed pair refusal", err)
			}
		})
	}
	if _, err := DiffHistorical(v3, v3); err == nil || !strings.Contains(err.Error(), "exact v1/v1") {
		t.Fatalf("DiffHistorical current-version error = %v", err)
	}
}

func TestPostgreSQLSafeWideningRecreatesSharedSourceGeneratedAndRemoteDependents(t *testing.T) {
	before := generatedWideningSchema(160)
	after := generatedWideningSchema(320)
	plan, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	const (
		source  = "10000000000000000000000000000001"
		first   = "10000000000000000000000000000002"
		second  = "10000000000000000000000000000003"
		primary = "20000000000000000000000000000000"
		unique  = "20000000000000000000000000000001"
		check   = "20000000000000000000000000000002"
		index   = "20000000000000000000000000000003"
		remote  = "30000000000000000000000000000001"
	)
	positions := map[string]int{}
	operations := map[string]Operation{}
	for index, operation := range plan.Operations {
		key := string(operation.Kind) + "\x00" + operation.ObjectID
		positions[key] = index
		operations[key] = operation
	}
	key := func(kind OperationKind, id string) string { return string(kind) + "\x00" + id }
	for _, required := range []string{
		key(AlterColumnType, source), key(DropColumn, first), key(AddColumn, first),
		key(DropColumn, second), key(AddColumn, second), key(DropPrimaryKey, primary), key(AddPrimaryKey, primary),
		key(DropUnique, unique), key(AddUnique, unique), key(DropCheck, check), key(AddCheck, check),
		key(DropIndex, index), key(CreateIndex, index), key(DropForeignKey, remote), key(AddForeignKey, remote),
	} {
		if _, exists := operations[required]; !exists {
			t.Fatalf("generated widening plan lacks %q: %#v", required, plan.Operations)
		}
	}
	for _, forbidden := range []string{key(AlterColumnType, first), key(AlterColumnType, second)} {
		if _, exists := operations[forbidden]; exists {
			t.Fatalf("generated widening retained forbidden direct AlterColumnType %q", forbidden)
		}
	}
	for _, generated := range []string{first, second} {
		if operations[key(DropColumn, generated)].Risk != RiskRewrite || operations[key(AddColumn, generated)].Risk != RiskRewrite {
			t.Fatalf("generated recreation is not a derived rewrite: drop=%#v add=%#v", operations[key(DropColumn, generated)], operations[key(AddColumn, generated)])
		}
	}
	for _, edge := range [][2]string{
		{key(DropUnique, unique), key(DropColumn, first)},
		{key(DropCheck, check), key(DropColumn, first)},
		{key(DropIndex, index), key(DropColumn, first)},
		{key(DropForeignKey, remote), key(DropPrimaryKey, primary)},
		{key(DropPrimaryKey, primary), key(DropColumn, second)},
		{key(DropColumn, first), key(AlterColumnType, source)},
		{key(DropColumn, second), key(AlterColumnType, source)},
		{key(AlterColumnType, source), key(AddColumn, first)},
		{key(AlterColumnType, source), key(AddColumn, second)},
		{key(AddColumn, first), key(AddUnique, unique)},
		{key(AddColumn, first), key(AddCheck, check)},
		{key(AddColumn, first), key(CreateIndex, index)},
		{key(AddColumn, second), key(AddPrimaryKey, primary)},
		{key(AddColumn, second), key(AddForeignKey, remote)},
	} {
		if positions[edge[0]] >= positions[edge[1]] {
			t.Fatalf("generated dependency order %q=%d must precede %q=%d: %#v", edge[0], positions[edge[0]], edge[1], positions[edge[1]], plan.Operations)
		}
	}
	var approvals []Approval
	for _, operation := range plan.Operations {
		if RequiresApproval(operation) {
			approvals = append(approvals, Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	if err := ValidatePlan(plan, approvals); err != nil {
		t.Fatalf("typed generated recreation plan invalid: %v", err)
	}
}

func TestPostgreSQLPhysicalFormatUpgradeRequiresGeneratedOutputBoundProof(t *testing.T) {
	after := generatedWideningSchema(160)
	after.Version, after.CanonicalVersion = 2, 2
	var err error
	after, err = physical.NormalizeHistoricalV2(after)
	if err != nil {
		t.Fatal(err)
	}
	before := historicalV1GeneratedWideningSchema(t, after)
	if _, err := DiffPhysicalFormatUpgrade(before, after); err != nil {
		t.Fatalf("exact v1 generated-output bound proof rejected: %v", err)
	}

	generatedID := ir.FieldID("10000000000000000000000000000002")
	parentIndex := findPhysicalTableIndex(t, before, ir.ModelID("00000000000000000000000000000001"))
	checkID, _ := physical.HistoricalV1MaxLengthCheckIdentity(before.Tables[parentIndex].ID, generatedID)
	for _, mutation := range []struct {
		name string
		edit func(*physical.PhysicalSchema)
	}{
		{name: "missing", edit: func(schema *physical.PhysicalSchema) {
			table := &schema.Tables[parentIndex]
			checks := table.Checks[:0]
			for _, check := range table.Checks {
				if check.ID != checkID {
					checks = append(checks, check)
				}
			}
			table.Checks = checks
		}},
		{name: "tampered", edit: func(schema *physical.PhysicalSchema) {
			table := &schema.Tables[parentIndex]
			for index := range table.Checks {
				if table.Checks[index].ID == checkID {
					table.Checks[index].Expression.Operands[1].Literal.Canonical = "159"
				}
			}
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			forged := clonePhysicalSchema(t, before)
			mutation.edit(&forged)
			if _, err := DiffPhysicalFormatUpgrade(forged, after); err == nil || !strings.Contains(err.Error(), "generated field") {
				t.Fatalf("generated output without exact legacy proof error = %v", err)
			}
		})
	}
}

func TestPostgreSQLWideningOrdersRemovedAndNewGeneratedColumns(t *testing.T) {
	for _, formatUpgrade := range []bool{false, true} {
		name := "v2"
		before := generatedWideningSchema(160)
		after := generatedWideningSchema(320)
		if formatUpgrade {
			name = "v1-to-v2"
			after = generatedWideningSchema(160)
			after.Version, after.CanonicalVersion = 2, 2
			var normalizeErr error
			after, normalizeErr = physical.NormalizeHistoricalV2(after)
			if normalizeErr != nil {
				t.Fatal(normalizeErr)
			}
			before = historicalV1GeneratedWideningSchema(t, after)
		}
		t.Run(name, func(t *testing.T) {
			const oldID = ir.FieldID("10000000000000000000000000000002")
			const newID = ir.FieldID("10000000000000000000000000000005")
			table := &after.Tables[findPhysicalTableIndex(t, after, ir.ModelID("00000000000000000000000000000001"))]
			var replacement physical.PhysicalColumn
			for _, column := range table.Columns {
				if column.ID == oldID {
					replacement = column
				}
			}
			replacement.ID, replacement.Name = newID, "new_derived"
			replacement.Ordinal = 1
			table.Columns = append(removePhysicalColumn(table.Columns, oldID), replacement)
			table.Uniques = nil
			table.Checks = nil
			table.Indexes = nil
			var plan Plan
			var err error
			if formatUpgrade {
				plan, err = DiffPhysicalFormatUpgrade(before, after)
			} else {
				plan, err = Diff(before, after)
			}
			if err != nil {
				t.Fatal(err)
			}
			drop := findOperationPosition(t, plan, DropColumn, string(oldID))
			alter := findOperationPosition(t, plan, AlterColumnType, "10000000000000000000000000000001")
			add := findOperationPosition(t, plan, AddColumn, string(newID))
			if !(drop < alter && alter < add) {
				t.Fatalf("removed/new generated ordering drop=%d alter=%d add=%d: %#v", drop, alter, add, plan.Operations)
			}
		})
	}
}

func TestPostgreSQLWideningGeneratedNameOnlyChangeUsesReviewedRecreation(t *testing.T) {
	before := generatedWideningSchema(160)
	after := generatedWideningSchema(320)
	field := ir.FieldID("10000000000000000000000000000002")
	for index := range after.Tables[0].Columns {
		if after.Tables[0].Columns[index].ID == field {
			after.Tables[0].Columns[index].Name = "renamed_derived"
		}
	}
	plan, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	drop := findOperationPosition(t, plan, DropColumn, string(field))
	add := findOperationPosition(t, plan, AddColumn, string(field))
	if drop >= add {
		t.Fatalf("generated rename recreation order drop=%d add=%d", drop, add)
	}
	for _, operation := range plan.Operations {
		if operation.Kind == RenameColumn && operation.ObjectID == string(field) {
			t.Fatalf("detached generated rename retained a separate RenameColumn: %#v", operation)
		}
	}
}

func TestPostgreSQLAddAndDropSourceGeneratedColumnsAreDependencyOrdered(t *testing.T) {
	base := generatedWideningSchema(160)
	base.Tables[0].Uniques, base.Tables[0].Checks, base.Tables[0].Indexes = nil, nil, nil
	source := ir.FieldID("10000000000000000000000000000001")
	generated := ir.FieldID("10000000000000000000000000000002")
	secondGenerated := ir.FieldID("10000000000000000000000000000003")

	without := clonePhysicalSchema(t, base)
	without.Tables = without.Tables[:1]
	without.Tables[0].Columns = removePhysicalColumn(removePhysicalColumn(removePhysicalColumn(without.Tables[0].Columns, source), generated), secondGenerated)
	without.Tables[0].PrimaryKey = nil
	for _, direction := range []struct {
		name          string
		before, after physical.PhysicalSchema
		first, second OperationKind
	}{
		{name: "add", before: without, after: base, first: AddColumn, second: AddColumn},
		{name: "drop", before: base, after: without, first: DropColumn, second: DropColumn},
	} {
		t.Run(direction.name, func(t *testing.T) {
			plan, err := Diff(direction.before, direction.after)
			if err != nil {
				t.Fatal(err)
			}
			var first, second int
			if direction.name == "add" {
				first = findOperationPosition(t, plan, AddColumn, string(source))
				second = findOperationPosition(t, plan, AddColumn, string(generated))
			} else {
				first = findOperationPosition(t, plan, DropColumn, string(generated))
				second = findOperationPosition(t, plan, DropColumn, string(source))
			}
			if first >= second {
				t.Fatalf("source/generated %s order first=%d second=%d: %#v", direction.name, first, second, plan.Operations)
			}
		})
	}
}

func removePhysicalColumn(columns []physical.PhysicalColumn, field ir.FieldID) []physical.PhysicalColumn {
	result := make([]physical.PhysicalColumn, 0, len(columns))
	for _, column := range columns {
		if column.ID != field {
			result = append(result, column)
		}
	}
	return result
}

func findOperationPosition(t *testing.T, plan Plan, kind OperationKind, objectID string) int {
	t.Helper()
	for index, operation := range plan.Operations {
		if operation.Kind == kind && operation.ObjectID == objectID {
			return index
		}
	}
	t.Fatalf("operation %s/%s missing: %#v", kind, objectID, plan.Operations)
	return -1
}

func historicalV1GeneratedWideningSchema(t *testing.T, current physical.PhysicalSchema) physical.PhysicalSchema {
	t.Helper()
	schema := clonePhysicalSchema(t, current)
	schema.Version, schema.CanonicalVersion = 1, 1
	var convertExpression func(*physical.Expression)
	convertStorage := func(storage *physical.StorageType) {
		if storage.Kind == physical.StoragePostgreSQLVarchar {
			*storage = physical.StorageType{Kind: physical.StoragePostgreSQLText}
		}
	}
	convertExpression = func(expression *physical.Expression) {
		if expression == nil {
			return
		}
		convertStorage(&expression.Type)
		for index := range expression.Operands {
			convertExpression(&expression.Operands[index])
		}
	}
	for tableIndex := range schema.Tables {
		table := &schema.Tables[tableIndex]
		lengths := map[ir.FieldID]uint32{}
		for columnIndex := range table.Columns {
			original := current.Tables[tableIndex].Columns[columnIndex]
			column := &table.Columns[columnIndex]
			if original.Storage.Kind == physical.StoragePostgreSQLVarchar {
				lengths[column.ID] = original.Storage.Length
			}
			convertStorage(&column.Storage)
			convertExpression(column.Default.Expression)
			if column.Generated != nil {
				convertExpression(&column.Generated.Expression)
			}
		}
		for checkIndex := range table.Checks {
			convertExpression(&table.Checks[checkIndex].Expression)
		}
		for indexIndex := range table.Indexes {
			index := &table.Indexes[indexIndex]
			convertExpression(index.Predicate)
			for keyIndex := range index.Keys {
				convertExpression(index.Keys[keyIndex].Expression)
			}
		}
		for field, lengthValue := range lengths {
			column := findPhysicalColumn(*table, field)
			checkID, checkName := physical.HistoricalV1MaxLengthCheckIdentity(table.ID, field)
			fieldCopy := field
			integer := physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
			literal := ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: fmt.Sprint(lengthValue)}
			columnExpression := physical.Expression{Kind: physical.ExpressionColumn, Type: column.Storage, Nullable: column.Nullable, Column: &fieldCopy, Operands: []physical.Expression{}}
			lengthExpression := physical.Expression{Kind: physical.ExpressionFunction, Type: integer, Nullable: column.Nullable, Symbol: &physical.SemanticSymbol{Identity: "golem.schema.function.length.v1", Kind: ir.SchemaSymbolFunction, Version: 1, Provider: ir.ProviderScopePortable}, Operands: []physical.Expression{columnExpression}}
			table.Checks = append(table.Checks, physical.PhysicalCheck{ID: checkID, Name: checkName, Expression: physical.Expression{Kind: physical.ExpressionOperator, Type: physical.StorageType{Kind: physical.StoragePostgreSQLBoolean}, Nullable: column.Nullable, Symbol: &physical.SemanticSymbol{Identity: "golem.schema.predicate.less-equal.v1", Kind: ir.SchemaSymbolOperator, Version: 1, Provider: ir.ProviderScopePortable}, Operands: []physical.Expression{lengthExpression, {Kind: physical.ExpressionLiteral, Type: integer, Literal: &literal, Operands: []physical.Expression{}}}}})
		}
	}
	return schema
}

func clonePhysicalSchema(t *testing.T, schema physical.PhysicalSchema) physical.PhysicalSchema {
	t.Helper()
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var clone physical.PhysicalSchema
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func findPhysicalColumn(table physical.PhysicalTable, field ir.FieldID) physical.PhysicalColumn {
	for _, column := range table.Columns {
		if column.ID == field {
			return column
		}
	}
	panic("physical test column not found: " + string(field))
}

func findPhysicalTableIndex(t *testing.T, schema physical.PhysicalSchema, model ir.ModelID) int {
	t.Helper()
	for index := range schema.Tables {
		if schema.Tables[index].ID == model {
			return index
		}
	}
	t.Fatalf("physical test table %s not found", model)
	return -1
}

func generatedWideningSchema(sourceLength uint32) physical.PhysicalSchema {
	const (
		parent  = ir.ModelID("00000000000000000000000000000001")
		child   = ir.ModelID("00000000000000000000000000000002")
		source  = ir.FieldID("10000000000000000000000000000001")
		first   = ir.FieldID("10000000000000000000000000000002")
		second  = ir.FieldID("10000000000000000000000000000003")
		childID = ir.FieldID("10000000000000000000000000000004")
	)
	bounded := func(length uint32) physical.StorageType {
		return physical.StorageType{Kind: physical.StoragePostgreSQLVarchar, Length: length}
	}
	lower := func(input ir.FieldID, inputType, outputType physical.StorageType) *physical.GeneratedExpression {
		field := input
		return &physical.GeneratedExpression{Kind: physical.GeneratedStored, Expression: physical.Expression{
			Kind: physical.ExpressionFunction, Type: outputType,
			Symbol:   &physical.SemanticSymbol{Identity: "golem.schema.function.lower.v1", Kind: ir.SchemaSymbolFunction, Version: 1, Provider: ir.ProviderScopePortable},
			Operands: []physical.Expression{{Kind: physical.ExpressionColumn, Type: inputType, Column: &field, Operands: []physical.Expression{}}},
		}}
	}
	stable := bounded(160)
	firstField := first
	notNullFirst := physical.Expression{
		Kind: physical.ExpressionOperator, Type: physical.StorageType{Kind: physical.StoragePostgreSQLBoolean},
		Symbol:   &physical.SemanticSymbol{Identity: "golem.schema.predicate.is-not-null.v1", Kind: ir.SchemaSymbolOperator, Version: 1, Provider: ir.ProviderScopePortable},
		Operands: []physical.Expression{{Kind: physical.ExpressionColumn, Type: stable, Column: &firstField, Operands: []physical.Expression{}}},
	}
	return physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: physical.PostgreSQLManifest(), Namespace: physical.Namespace{Name: "public"},
		Tables: []physical.PhysicalTable{
			{ID: parent, Name: "parents", Columns: []physical.PhysicalColumn{
				{ID: source, Name: "source", Ordinal: 0, Storage: bounded(sourceLength), Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
				{ID: first, Name: "first_derived", Ordinal: 1, Storage: stable, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}, Generated: lower(source, bounded(sourceLength), stable)},
				{ID: second, Name: "second_derived", Ordinal: 2, Storage: stable, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}, Generated: lower(source, bounded(sourceLength), stable)},
			},
				PrimaryKey: &physical.PhysicalKey{ID: "20000000000000000000000000000000", Name: "pk_parents_second", Columns: []ir.FieldID{second}},
				Uniques:    []physical.PhysicalKey{{ID: "20000000000000000000000000000001", Name: "uq_parents_first", Columns: []ir.FieldID{first}}},
				Checks:     []physical.PhysicalCheck{{ID: "20000000000000000000000000000002", Name: "ck_parents_first", Expression: notNullFirst}},
				Indexes:    []physical.PhysicalIndex{{ID: "20000000000000000000000000000003", Name: "idx_parents_first", Method: physical.IndexBTree, Keys: []physical.IndexKey{{Column: &firstField, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, CreationMode: physical.IndexTransactional}},
			},
			{ID: child, Name: "children", Columns: []physical.PhysicalColumn{
				{ID: childID, Name: "parent_derived", Ordinal: 0, Storage: stable, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			}, ForeignKeys: []physical.PhysicalForeignKey{{ID: "30000000000000000000000000000001", Name: "fk_children_parent", Columns: []ir.FieldID{childID}, ReferencedTable: parent, ReferencedColumns: []ir.FieldID{second}, OnUpdate: ir.ActionNoAction, OnDelete: ir.ActionRestrict, Deferrable: ir.NotDeferrable}}},
		},
	}
}

func schema() physical.PhysicalSchema {
	return physical.PhysicalSchema{Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion, Provider: physical.SQLiteManifest(), Namespace: physical.Namespace{Name: "main"}}
}

func testManifest(entries ...ManifestEntry) Manifest {
	return Manifest{
		FormatVersion: ManifestFormatVersion, CanonicalVersion: ManifestCanonicalVersion,
		HashAlgorithm: "sha256", GeneratorVersion: "test", Provider: physical.SQLiteManifest(), Entries: entries,
	}
}

func allowlistDigest(snapshot physical.PhysicalSchema) Digest {
	fingerprint, err := physical.UnmanagedAllowlistFingerprint(snapshot)
	if err != nil {
		panic(err)
	}
	return Digest(fingerprint.String())
}

func relatedSchema() physical.PhysicalSchema {
	parentTable := ir.ModelID("00000000000000000000000000000001")
	childTable := ir.ModelID("00000000000000000000000000000002")
	parentID := ir.FieldID("00000000000000000000000000000011")
	childParentID := ir.FieldID("00000000000000000000000000000021")
	return physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion,
		Provider: physical.SQLiteManifest(), Namespace: physical.Namespace{Name: "main"},
		Tables: []physical.PhysicalTable{
			{
				ID: parentTable, Name: "parents",
				Columns:    []physical.PhysicalColumn{{ID: parentID, Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}},
				PrimaryKey: &physical.PhysicalKey{ID: ir.KeyID("00000000000000000000000000000031"), Name: "pk_parents", Columns: []ir.FieldID{parentID}},
			},
			{
				ID: childTable, Name: "children",
				Columns: []physical.PhysicalColumn{{ID: childParentID, Name: "parent_id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}},
				ForeignKeys: []physical.PhysicalForeignKey{{
					ID: "00000000000000000000000000000041", Name: "fk_children_parent", Columns: []ir.FieldID{childParentID},
					ReferencedTable: parentTable, ReferencedColumns: []ir.FieldID{parentID}, OnUpdate: ir.ActionNoAction, OnDelete: ir.ActionCascade, Deferrable: ir.NotDeferrable,
				}},
				Indexes: []physical.PhysicalIndex{{
					ID: "00000000000000000000000000000042", Name: "idx_children_parent", Method: physical.IndexBTree,
					Keys: []physical.IndexKey{{Column: fieldIDPointer(childParentID), Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, CreationMode: physical.IndexTransactional,
				}},
			},
		},
	}
}

func fieldIDPointer(value ir.FieldID) *ir.FieldID { return &value }

func operationKinds(operations []Operation, exclude OperationKind) []OperationKind {
	var kinds []OperationKind
	for _, operation := range operations {
		if operation.Kind != exclude {
			kinds = append(kinds, operation.Kind)
		}
	}
	return kinds
}

func assertDependsOnKind(t *testing.T, operations []Operation, operation Operation, dependencyKind OperationKind) {
	t.Helper()
	byID := map[OperationID]Operation{}
	for _, value := range operations {
		byID[value.ID] = value
	}
	for _, dependency := range operation.Dependencies {
		if byID[dependency].Kind == dependencyKind {
			return
		}
	}
	t.Fatalf("%s does not depend on %s: %#v", operation.Kind, dependencyKind, operation.Dependencies)
}
