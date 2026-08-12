package migration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

const (
	historicalParentTable = ir.ModelID("71000000000000000000000000000001")
	historicalChildTable  = ir.ModelID("71000000000000000000000000000002")
	historicalIDField     = ir.FieldID("72000000000000000000000000000001")
	historicalValueField  = ir.FieldID("72000000000000000000000000000002")
	historicalNoteField   = ir.FieldID("72000000000000000000000000000003")
	historicalChildField  = ir.FieldID("72000000000000000000000000000004")
)

func TestHistoricalV1PlannerBranchCompleteGoldenInventory(t *testing.T) {
	base := historicalV1PlannerSchema(t)
	sqliteInitialBefore := mutateHistoricalV1(t, base, func(schema *physical.PhysicalSchema) { schema.Tables = nil })
	sqliteInitialAfter := mutateHistoricalV1(t, sqliteInitialBefore, func(schema *physical.PhysicalSchema) { schema.System = historicalV1SQLiteSystem() })
	sqliteSystemAfter := mutateHistoricalV1(t, sqliteInitialAfter, func(schema *physical.PhysicalSchema) {
		schema.System.Objects = append(schema.System.Objects, physical.OutboxSystemObjectV1())
	})
	postgresInitialBefore := historicalV1PostgreSQLEmpty(t)
	postgresInitialAfter := mutateHistoricalV1(t, postgresInitialBefore, func(schema *physical.PhysicalSchema) { schema.System = historicalV1PostgreSQLSystem() })
	cases := []struct {
		name   string
		before physical.PhysicalSchema
		after  physical.PhysicalSchema
		want   string
		kinds  []OperationKind
	}{
		{name: "sqlite-initial-system", before: sqliteInitialBefore, after: sqliteInitialAfter, want: "8353e81293fdfa6feb95a81041d19794d468cd4a6b1267419dc24e5137b32843", kinds: []OperationKind{BootstrapSystemSchema, RecordSchemaVersion}},
		{name: "postgresql-initial-namespace-system", before: postgresInitialBefore, after: postgresInitialAfter, want: "efd29f7c49c8d0e5f2990474beab3c85eba93c02b304e2ec597c3408f1bb6589", kinds: []OperationKind{CreateNamespace, BootstrapSystemSchema, RecordSchemaVersion}},
		{name: "registered-system-addition", before: sqliteInitialAfter, after: sqliteSystemAfter, want: "04e17777ee645d5ea8e99c6e5223abb8559c4c2dd564ba94361d92c45c05166c", kinds: []OperationKind{AddSystemObject, RecordSchemaVersion}},
		{name: "renames", before: base, after: mutateHistoricalV1(t, base, func(schema *physical.PhysicalSchema) {
			schema.Tables[0].Name = "aaa_parents_v2"
			schema.Tables[0].Columns[1].Name = "value_v2"
			schema.Tables[0].Indexes[0].Name = "idx_parents_value_v2"
		}), want: "f3512d46b7ea2a252efb23fc65e0c0c5f5cc03134ec615ab27b4fe6f05b8fd23", kinds: []OperationKind{RenameTable, RenameColumn, RenameIndex, RecordSchemaVersion}},
		{name: "add-drop-manual", before: base, after: mutateHistoricalV1(t, base, func(schema *physical.PhysicalSchema) {
			schema.Tables = schema.Tables[:1]
			schema.Tables[0].Columns = append(schema.Tables[0].Columns, physical.PhysicalColumn{
				ID: "72000000000000000000000000000005", Name: "required_new", Ordinal: 3,
				Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone},
			})
			schema.Tables = append(schema.Tables, physical.PhysicalTable{
				ID: "71000000000000000000000000000003", Name: "added",
				Columns: []physical.PhysicalColumn{{ID: "72000000000000000000000000000006", Name: "id", Ordinal: 0, Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}},
			})
		}), want: "b99e3df02e6434241ee6ab9ab1dfefc9320683dbc4d30274db82684cb651ca7e", kinds: []OperationKind{CreateTable, AddColumn, DropTable, RecordSchemaVersion}},
		{name: "type-null-default-rebuild", before: base, after: mutateHistoricalV1(t, base, func(schema *physical.PhysicalSchema) {
			value := &schema.Tables[0].Columns[1]
			value.Storage = physical.StorageType{Kind: physical.StorageSQLiteBlob}
			value.Nullable = true
			schema.Tables[0].Checks[0].Expression.Operands[0].Type = value.Storage
			literal := ir.TypedLiteralIR{Kind: ir.LiteralString, Canonical: "fixed"}
			schema.Tables[0].Columns[2].Default = physical.PhysicalDefault{Kind: physical.DefaultLiteral, Literal: &literal}
			source := historicalValueField
			schema.Tables[0].Columns[2].Generated = &physical.GeneratedExpression{Kind: physical.GeneratedStored, Expression: physical.Expression{Kind: physical.ExpressionColumn, Type: value.Storage, Nullable: value.Nullable, Column: &source, Operands: []physical.Expression{}}}
			schema.Tables[0].Columns[2].Storage = value.Storage
			schema.Tables[0].Columns[2].Nullable = value.Nullable
			schema.Tables[0].Columns[2].Default = physical.PhysicalDefault{Kind: physical.DefaultNone}
		}), want: "02adad0bc82dfc47e04a63972461fcd0e1493e52eebae2cee190eeb79b1d6887", kinds: []OperationKind{DropUnique, DropCheck, DropIndex, AlterColumnType, AlterColumnType, AlterColumnNullability, RebuildTable, AddUnique, AddCheck, CreateIndex, RecordSchemaVersion}},
		{name: "set-default", before: base, after: mutateHistoricalV1(t, base, func(schema *physical.PhysicalSchema) {
			literal := ir.TypedLiteralIR{Kind: ir.LiteralString, Canonical: "fixed"}
			schema.Tables[0].Columns[2].Default = physical.PhysicalDefault{Kind: physical.DefaultLiteral, Literal: &literal}
		}), want: "0116cb257da7192af9949a3f58e5e2ece85dfcfa54fba351a364806246e150fc", kinds: []OperationKind{SetColumnDefault, RecordSchemaVersion}},
		{name: "drop-default", before: mutateHistoricalV1(t, base, func(schema *physical.PhysicalSchema) {
			literal := ir.TypedLiteralIR{Kind: ir.LiteralString, Canonical: "fixed"}
			schema.Tables[0].Columns[2].Default = physical.PhysicalDefault{Kind: physical.DefaultLiteral, Literal: &literal}
		}), after: base, want: "ed008b35c64f0141dd1183861b424a7be56a9a35c3ec14922703c0ff2e3825c2", kinds: []OperationKind{DropColumnDefault, RecordSchemaVersion}},
		{name: "replace-keys-fk-check-index", before: base, after: mutateHistoricalV1(t, base, func(schema *physical.PhysicalSchema) {
			schema.Tables[0].PrimaryKey.Columns = []ir.FieldID{historicalValueField}
			schema.Tables[0].Uniques[0].Columns = []ir.FieldID{historicalIDField}
			id := historicalIDField
			schema.Tables[0].Checks[0].Expression.Operands[0].Column = &id
			schema.Tables[0].Checks[0].Expression.Operands[0].Type = schema.Tables[0].Columns[0].Storage
			schema.Tables[0].Indexes[0].Keys[0].Column = &id
			schema.Tables[1].ForeignKeys[0].OnDelete = ir.ActionCascade
		}), want: "152fd34ad3a17c4ab4b0906491c12919dd4c4403e518d5d0840ce4654add486c", kinds: []OperationKind{DropPrimaryKey, AddPrimaryKey, DropUnique, AddUnique, DropForeignKey, AddForeignKey, DropCheck, AddCheck, DropIndex, CreateIndex, RecordSchemaVersion}},
	}

	manualRiskCovered := false
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			plan, err := DiffHistorical(test.before, test.after)
			if err != nil {
				t.Fatal(err)
			}
			gotKinds := make([]OperationKind, len(plan.Operations))
			for index, operation := range plan.Operations {
				gotKinds[index] = operation.Kind
				manualRiskCovered = manualRiskCovered || operation.Risk == RiskManual
			}
			if !sameHistoricalV1KindInventory(gotKinds, test.kinds) {
				t.Fatalf("operation kinds=%v want inventory=%v", gotKinds, test.kinds)
			}
			encoded, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			got := fmt.Sprintf("%x", sha256.Sum256(encoded))
			if test.want == "" {
				t.Fatalf("freeze historical v1 plan golden: %s", got)
			}
			if got != test.want {
				t.Fatalf("historical v1 plan changed: got %s want %s", got, test.want)
			}
			if len(plan.Phases) == 0 || plan.Phases[len(plan.Phases)-1].AfterFingerprint != plan.AfterFingerprint {
				t.Fatal("retained planner did not produce a terminal exact phase")
			}
		})
	}
	if !manualRiskCovered {
		t.Fatal("retained add-column manual-risk branch was not exercised")
	}
}

func TestHistoricalV1IdenticalReviewedEntryRetainsRecordSchemaVersion(t *testing.T) {
	schema := historicalV1PlannerSchema(t)
	plan, err := DiffHistorical(schema, schema)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 || plan.Operations[0].Kind != RecordSchemaVersion {
		t.Fatalf("identical reviewed v1 operation graph=%v; want sealed RecordSchemaVersion", plan.Operations)
	}
	operation := plan.Operations[0]
	if operation.Before != operation.After || operation.Before != plan.BeforeFingerprint || plan.BeforeFingerprint != plan.AfterFingerprint {
		t.Fatalf("identical reviewed v1 fingerprints changed: operation=%#v plan=%#v", operation, plan)
	}
	if len(plan.Phases) != 1 || len(plan.Phases[0].Operations) != 1 || plan.Phases[0].Operations[0] != operation.ID {
		t.Fatalf("identical reviewed v1 phase graph=%#v", plan.Phases)
	}
	if err := ValidatePlanShape(plan); err != nil {
		t.Fatalf("exact frozen v1 identity graph failed validation: %v", err)
	}
	forged := plan
	forged.Operations = append([]Operation(nil), plan.Operations...)
	forged.Operations[0].LogicalPath = "forged"
	if err := ValidatePlanShape(forged); err == nil {
		t.Fatal("non-frozen v1 identity operation graph passed validation")
	}
}

func historicalV1SQLiteSystem() physical.SystemSchema {
	return physical.SystemSchema{Version: 1, Namespace: physical.Namespace{Name: "main"}, Objects: []physical.SystemObject{
		{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"},
		{ID: physical.MigrationLockObjectIDV1, Kind: physical.SystemMigrationLock, Version: 1, Name: "_golem_migration_lock"},
	}}
}

func historicalV1PostgreSQLSystem() physical.SystemSchema {
	return physical.SystemSchema{Version: 1, Namespace: physical.Namespace{Name: "_golem"}, Objects: []physical.SystemObject{
		{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"},
		{ID: physical.MigrationLockObjectIDV1, Kind: physical.SystemMigrationLock, Version: 1, Name: "_golem_migration_lock"},
	}}
}

func historicalV1PostgreSQLEmpty(t *testing.T) physical.PhysicalSchema {
	t.Helper()
	schema := physical.PhysicalSchema{Version: 1, CanonicalVersion: 1, Provider: physical.ProviderManifest{Provider: ir.PostgreSQL, Driver: physical.DriverIdentity{Module: "github.com/jackc/pgx/v5/stdlib", Adapter: "sqlx"}, MinimumVersion: physical.Version{Major: 15}}, Namespace: physical.Namespace{Name: "public"}}
	normalized, err := physical.NormalizeHistoricalV1(schema)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func sameHistoricalV1KindInventory(got, want []OperationKind) bool {
	a := append([]OperationKind(nil), got...)
	b := append([]OperationKind(nil), want...)
	sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func historicalV1PlannerSchema(t *testing.T) physical.PhysicalSchema {
	t.Helper()
	value := historicalValueField
	child := historicalChildField
	check := physical.Expression{Kind: physical.ExpressionOperator, Type: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Symbol: &physical.SemanticSymbol{Identity: "golem.schema.predicate.is-not-null.v1", Kind: ir.SchemaSymbolOperator, Version: 1, Provider: ir.ProviderScopePortable}, Operands: []physical.Expression{{Kind: physical.ExpressionColumn, Type: physical.StorageType{Kind: physical.StorageSQLiteText}, Column: &value, Operands: []physical.Expression{}}}}
	schema := physical.PhysicalSchema{
		Version: 1, CanonicalVersion: 1,
		Provider:  physical.ProviderManifest{Provider: ir.SQLite, Driver: physical.DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}, MinimumVersion: physical.Version{Major: 3, Minor: 38}},
		Namespace: physical.Namespace{Name: "main"},
		Tables: []physical.PhysicalTable{
			{ID: historicalParentTable, Name: "aaa_parents", Columns: []physical.PhysicalColumn{
				{ID: historicalIDField, Name: "id", Ordinal: 0, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
				{ID: historicalValueField, Name: "value", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
				{ID: historicalNoteField, Name: "note", Ordinal: 2, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			}, PrimaryKey: &physical.PhysicalKey{ID: "73000000000000000000000000000001", Name: "pk_parents", Columns: []ir.FieldID{historicalIDField}}, Uniques: []physical.PhysicalKey{{ID: "73000000000000000000000000000002", Name: "uq_parents_value", Columns: []ir.FieldID{historicalValueField}}}, Checks: []physical.PhysicalCheck{{ID: "73000000000000000000000000000003", Name: "ck_parents_value", Expression: check}}, Indexes: []physical.PhysicalIndex{{ID: "73000000000000000000000000000004", Name: "idx_parents_value", Method: physical.IndexBTree, Keys: []physical.IndexKey{{Column: &value, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, CreationMode: physical.IndexTransactional}}},
			{ID: historicalChildTable, Name: "zzz_children", Columns: []physical.PhysicalColumn{{ID: historicalChildField, Name: "parent_id", Ordinal: 0, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}}, ForeignKeys: []physical.PhysicalForeignKey{{ID: "73000000000000000000000000000005", Name: "fk_children_parent", Columns: []ir.FieldID{historicalChildField}, ReferencedTable: historicalParentTable, ReferencedColumns: []ir.FieldID{historicalIDField}, OnUpdate: ir.ActionNoAction, OnDelete: ir.ActionRestrict, Deferrable: ir.NotDeferrable}}, Indexes: []physical.PhysicalIndex{{ID: "73000000000000000000000000000006", Name: "idx_children_parent", Method: physical.IndexBTree, Keys: []physical.IndexKey{{Column: &child, Direction: ir.SortAsc, Nulls: ir.NullsDefault}}, CreationMode: physical.IndexTransactional}}},
		},
	}
	normalized, err := physical.NormalizeHistoricalV1(schema)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func mutateHistoricalV1(t *testing.T, schema physical.PhysicalSchema, edit func(*physical.PhysicalSchema)) physical.PhysicalSchema {
	t.Helper()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var clone physical.PhysicalSchema
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	edit(&clone)
	normalized, err := physical.NormalizeHistoricalV1(clone)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}
