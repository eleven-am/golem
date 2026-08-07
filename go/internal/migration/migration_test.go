package migration

import (
	"encoding/json"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
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
