package migration

import (
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
)

const correctionField ir.FieldID = "00000000000000000000000000000012"

func dataCorrectionTestSchema(t *testing.T) physical.PhysicalSchema {
	t.Helper()
	snapshot := relatedSchema()
	snapshot.Tables = snapshot.Tables[:1]
	snapshot.Version, snapshot.CanonicalVersion = 4, 4
	snapshot.System = physical.SystemSchema{Version: 1, Namespace: snapshot.Namespace, Objects: []physical.SystemObject{
		{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"},
		{ID: physical.MigrationLockObjectIDV1, Kind: physical.SystemMigrationLock, Version: 1, Name: "_golem_migration_lock"},
		physical.DataIncarnationSystemObjectV1(),
	}}
	snapshot.Tables[0].Columns = append(snapshot.Tables[0].Columns, physical.PhysicalColumn{ID: correctionField, Name: "name", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
	snapshot, err := physical.NormalizeHistorical(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestDataCorrectionPlanRequiresExactAuthorityAndPreservesSchema(t *testing.T) {
	snapshot := dataCorrectionTestSchema(t)
	plan, err := DataCorrectionPlan(snapshot, correctionField)
	if err != nil {
		t.Fatal(err)
	}
	operation := plan.Operations[0]
	approval := []Approval{{OperationID: operation.ID, Risk: RiskManual, Before: operation.Before, After: operation.After}}
	if err := ValidatePlan(plan, nil); err == nil {
		t.Fatal("data correction accepted without exact approval")
	}
	if err := ValidatePlan(plan, approval); err != nil {
		t.Fatal(err)
	}
	facts, ok := plan.SnapshotFacts()
	if !ok || !reflect.DeepEqual(facts.Before(), facts.After()) || plan.BeforeFingerprint != plan.AfterFingerprint || len(plan.Phases) != 1 {
		t.Fatal("data correction changed schema or lacks one transactional phase")
	}
	entry := ManifestEntry{BeforeSnapshot: snapshot, AfterSnapshot: snapshot, BeforePhysical: plan.BeforeFingerprint, AfterPhysical: plan.AfterFingerprint, Operations: plan.Operations, Phases: plan.Phases}
	if _, err := ReviewedEntryPlan(entry); err != nil {
		t.Fatal(err)
	}
	entry.Operations = append([]Operation(nil), entry.Operations...)
	entry.Operations[0].Stage++
	if _, err := ReviewedEntryPlan(entry); err == nil {
		t.Fatal("altered correction graph accepted")
	}
	for _, field := range []ir.FieldID{"missing", snapshot.Tables[0].PrimaryKey.Columns[0]} {
		if _, err := DataCorrectionPlan(snapshot, field); err == nil {
			t.Fatalf("invalid correction target accepted: %s", field)
		}
	}
	plain := snapshot
	plain.Version, plain.CanonicalVersion = 3, 3
	plain.System.Objects = plain.System.Objects[:2]
	if _, err := DataCorrectionPlan(plain, correctionField); err == nil {
		t.Fatal("data correction accepted without durable identity")
	}
}

func TestDataCorrectionManifestBindsSQLAndGeneratedPostcondition(t *testing.T) {
	snapshot := dataCorrectionTestSchema(t)
	plan, err := DataCorrectionPlan(snapshot, correctionField)
	if err != nil {
		t.Fatal(err)
	}
	op := plan.Operations[0]
	sql := []byte("UPDATE parents SET name='corrected';\n")
	files := map[string][]byte{"002.data.sql": sql}
	entry := ManifestEntry{ID: "002_correction", BeforeModel: Checksum([]byte("model")), AfterModel: Checksum([]byte("model")), BeforeSnapshot: snapshot, AfterSnapshot: snapshot, BeforePhysical: plan.BeforeFingerprint, AfterPhysical: plan.AfterFingerprint,
		Operations: plan.Operations, Phases: plan.Phases, Risks: []OperationRisk{{OperationID: op.ID, Risk: op.Risk}}, Approvals: []Approval{{OperationID: op.ID, Risk: op.Risk, Before: op.Before, After: op.After}},
		Manual: []ManualCompanion{{OperationID: op.ID, File: FileChecksum{Path: "002.data.sql", SHA256: Checksum(sql)}, Postcondition: DataCorrectionPostcondition(snapshot.Tables[0].ID, correctionField)}}, UnmanagedAllowlistDigest: allowlistDigest(snapshot)}
	entry.ChainHash = ChainHash(entry)
	manifest := testManifest(entry)
	if err := VerifyManifest(manifest, files); err != nil {
		t.Fatal(err)
	}
	files["002.data.sql"] = []byte("UPDATE parents SET name='different';\n")
	if err := VerifyManifest(manifest, files); err == nil {
		t.Fatal("rewritten correction SQL accepted")
	}
	files["002.data.sql"] = sql
	manifest.Entries[0].Manual[0].Postcondition = Checksum([]byte("wrong target"))
	manifest.Entries[0].ChainHash = ChainHash(manifest.Entries[0])
	if err := VerifyManifest(manifest, files); err == nil {
		t.Fatal("rehashed incorrect generated postcondition accepted")
	}
}

func TestDataCorrectionRefusesUniqueExpressionDependency(t *testing.T) {
	snapshot := dataCorrectionTestSchema(t)
	field := correctionField
	expression := physical.Expression{Kind: physical.ExpressionColumn, Type: physical.StorageType{Kind: physical.StorageSQLiteText}, Nullable: true, Column: &field}
	snapshot.Tables[0].Indexes = []physical.PhysicalIndex{{ID: "00000000000000000000000000000055", Name: "unique_name_expression", Unique: true, Method: physical.IndexBTree, Keys: []physical.IndexKey{{Expression: &expression, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, CreationMode: physical.IndexTransactional}}
	snapshot.Provider.Capabilities = append(snapshot.Provider.Capabilities, physical.CapabilityFact{ID: "sqlite.expression_indexes", Version: 1, Verification: physical.VerificationVersionFloor})
	snapshot.Tables[0].Indexes[0].RequiredCapabilities = []physical.CapabilityRequirement{{Capability: "sqlite.expression_indexes", Owner: physical.ObjectRef{Kind: ir.ObjectIndex, ModelID: snapshot.Tables[0].ID, ObjectID: "00000000000000000000000000000055"}}}
	var err error
	snapshot, err = physical.NormalizeHistorical(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DataCorrectionPlan(snapshot, correctionField); err == nil {
		t.Fatal("correction of an expression-indexed unique key was accepted")
	}
}

func generatedCorrectionTestSchema(t *testing.T) (physical.PhysicalSchema, ir.FieldID) {
	t.Helper()
	snapshot := dataCorrectionTestSchema(t)
	previous := correctionField
	for index, field := range []ir.FieldID{"00000000000000000000000000000061", "00000000000000000000000000000062"} {
		source := previous
		snapshot.Tables[0].Columns = append(snapshot.Tables[0].Columns, physical.PhysicalColumn{ID: field, Name: physical.PhysicalName([]string{"derived_one", "derived_two"}[index]), Ordinal: uint32(index + 2), Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}, Generated: &physical.GeneratedExpression{Kind: physical.GeneratedStored, Expression: physical.Expression{Kind: physical.ExpressionColumn, Type: physical.StorageType{Kind: physical.StorageSQLiteText}, Nullable: true, Column: &source}}})
		previous = field
	}
	return snapshot, previous
}

func TestDataCorrectionRefusesGeneratedKeyAndSemanticDependencies(t *testing.T) {
	snapshot, derived := generatedCorrectionTestSchema(t)
	key := snapshot
	key.Tables = append([]physical.PhysicalTable(nil), snapshot.Tables...)
	key.Tables[0].Uniques = []physical.PhysicalKey{{ID: "00000000000000000000000000000063", Name: "unique_derived", Columns: []ir.FieldID{derived}}}
	if _, err := physical.NormalizeHistorical(key); err != nil {
		t.Fatal(err)
	}
	if _, err := DataCorrectionPlan(key, correctionField); err == nil {
		t.Fatal("correction of a transitive generated key source accepted")
	}
	for _, input := range []ir.FieldID{correctionField, derived, snapshot.Tables[0].PrimaryKey.Columns[0]} {
		payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "meaning", Space: "documents", Dimensions: 3, Fields: []string{string(input)}, Metric: "cosine"})
		if err != nil {
			t.Fatal(err)
		}
		extension, err := semanticstorage.Lower(ir.ProviderExtensionIR{ID: "00000000000000000000000000000064", Provider: ir.SQLite, Kind: semanticcontract.IndexKind, Version: semanticcontract.Version, Owner: ir.ObjectID(snapshot.Tables[0].ID), Payload: payload}, snapshot.Tables[0])
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Extensions = []physical.Extension{extension}
		if _, err := physical.NormalizeHistorical(snapshot); err != nil {
			t.Fatal(err)
		}
		_, err = DataCorrectionPlan(snapshot, correctionField)
		if input == correctionField || input == derived {
			if err == nil || !strings.Contains(err.Error(), "semantic input") {
				t.Fatalf("semantic dependency correction: input=%s error=%v", input, err)
			}
		} else if err != nil {
			t.Fatalf("unrelated semantic index prevents correction: %v", err)
		}
	}
}
