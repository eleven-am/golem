package migration

import (
	"strconv"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

const (
	reinstatedTable = ir.ModelID("0a000000000000000000000000000001")
	reinstatedID    = ir.FieldID("0b000000000000000000000000000001")
	reinstatedTitle = ir.FieldID("0b000000000000000000000000000002")
	reinstatedKey   = ir.KeyID("0c000000000000000000000000000001")
	reinstatedCheck = ir.CheckID("0d000000000000000000000000000001")
)

func boundedTitleSchema(t *testing.T, table, title, check, key physical.PhysicalName, length uint32, nullable bool) physical.PhysicalSchema {
	t.Helper()
	text := physical.StorageType{Kind: physical.StorageSQLiteText, Length: length}
	integer := physical.StorageType{Kind: physical.StorageSQLiteInteger}
	field := reinstatedTitle
	value := schema()
	value.Tables = []physical.PhysicalTable{{
		ID: reinstatedTable, Name: table,
		Columns: []physical.PhysicalColumn{
			{ID: reinstatedID, Name: "id", Storage: integer, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			{ID: reinstatedTitle, Name: title, Ordinal: 1, Storage: text, Nullable: nullable, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: reinstatedKey, Name: key, Columns: []ir.FieldID{reinstatedID}},
		Checks: []physical.PhysicalCheck{{ID: reinstatedCheck, Name: check, Expression: physical.Expression{
			Kind: physical.ExpressionOperator, Type: integer,
			Symbol: &physical.SemanticSymbol{Identity: "sqlite.check.max-length", Kind: ir.SchemaSymbolOperator, Version: 1, Provider: ir.ProviderScopeSQLite},
			Operands: []physical.Expression{
				{Kind: physical.ExpressionColumn, Type: text, Nullable: nullable, Column: &field, Operands: []physical.Expression{}},
				{Kind: physical.ExpressionLiteral, Type: integer, Literal: &ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: strconv.FormatUint(uint64(length), 10)}, Operands: []physical.Expression{}},
			},
		}}},
	}}
	normalized, err := physical.Normalize(value)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func operationOfKind(t *testing.T, plan Plan, kind OperationKind) Operation {
	t.Helper()
	for _, operation := range plan.Operations {
		if operation.Kind == kind {
			return operation
		}
	}
	t.Fatalf("plan has no %s operation: %#v", kind, plan.Operations)
	return Operation{}
}

func TestReinstatedConstraintsNeedNoApprovalAndAreNotDataLoss(t *testing.T) {
	base := boundedTitleSchema(t, "notes", "title", "ck_notes_title", "pk_notes", 200, false)
	for _, testCase := range []struct {
		name  string
		after physical.PhysicalSchema
		kind  OperationKind
	}{
		{name: "renamed column", after: boundedTitleSchema(t, "notes", "headline", "ck_notes_headline", "pk_notes", 200, false), kind: AddCheck},
		{name: "widened string", after: boundedTitleSchema(t, "notes", "title", "ck_notes_title", "pk_notes", 500, false), kind: AddCheck},
		{name: "optional column", after: boundedTitleSchema(t, "notes", "title", "ck_notes_title", "pk_notes", 200, true), kind: AddCheck},
		{name: "renamed table", after: boundedTitleSchema(t, "writers", "title", "ck_writers_title", "pk_writers", 200, false), kind: AddPrimaryKey},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			plan, err := Diff(base, testCase.after)
			if err != nil {
				t.Fatal(err)
			}
			operation := operationOfKind(t, plan, testCase.kind)
			if operation.Risk != RiskDataLoss {
				t.Fatalf("recorded risk changed to %s; the sealed operation graph must not change", operation.Risk)
			}
			if PlanRequiresApproval(plan, operation) || PlanOperationRisk(plan, operation) != RiskLocking {
				t.Fatalf("reinstated %s approval=%v risk=%s", operation.Kind, PlanRequiresApproval(plan, operation), PlanOperationRisk(plan, operation))
			}
			var approvals []Approval
			for _, candidate := range plan.Operations {
				if PlanRequiresApproval(plan, candidate) {
					approvals = append(approvals, Approval{OperationID: candidate.ID, Risk: candidate.Risk, Before: candidate.Before, After: candidate.After})
				}
			}
			if err := ValidatePlan(plan, approvals); err != nil {
				t.Fatalf("plan without the superseded approval: %v", err)
			}
			legacy := append(approvals, Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
			if err := ValidatePlan(plan, legacy); err != nil {
				t.Fatalf("released entry carrying the former approval: %v", err)
			}
		})
	}
}

func TestStrengthenedOrReshapedConstraintsStillDemandApproval(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		before, after physical.PhysicalSchema
		kind          OperationKind
	}{
		{name: "narrowed string", before: boundedTitleSchema(t, "notes", "title", "ck_notes_title", "pk_notes", 500, false), after: boundedTitleSchema(t, "notes", "title", "ck_notes_title", "pk_notes", 200, false), kind: AddCheck},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			plan, err := Diff(testCase.before, testCase.after)
			if err != nil {
				t.Fatal(err)
			}
			operation := operationOfKind(t, plan, testCase.kind)
			if !PlanRequiresApproval(plan, operation) || PlanOperationRisk(plan, operation) != RiskDataLoss {
				t.Fatalf("strengthened %s approval=%v risk=%s", operation.Kind, PlanRequiresApproval(plan, operation), PlanOperationRisk(plan, operation))
			}
		})
	}
	before := boundedTitleSchema(t, "notes", "title", "ck_notes_title", "pk_notes", 200, false)
	after := boundedTitleSchema(t, "notes", "title", "ck_notes_title", "pk_notes", 200, false)
	after.Tables[0].PrimaryKey = &physical.PhysicalKey{ID: reinstatedKey, Name: "pk_notes", Columns: []ir.FieldID{reinstatedID, reinstatedTitle}}
	plan, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	operation := operationOfKind(t, plan, AddPrimaryKey)
	if !PlanRequiresApproval(plan, operation) {
		t.Fatal("a primary key over different columns was treated as reinstated")
	}
}

func TestSQLiteStringWideningIsARewriteThatStillNeedsReview(t *testing.T) {
	plan, err := Diff(boundedTitleSchema(t, "notes", "title", "ck_notes_title", "pk_notes", 200, false), boundedTitleSchema(t, "notes", "title", "ck_notes_title", "pk_notes", 500, false))
	if err != nil {
		t.Fatal(err)
	}
	operation := operationOfKind(t, plan, AlterColumnType)
	if !PlanRequiresApproval(plan, operation) || PlanOperationRisk(plan, operation) != RiskRewrite {
		t.Fatalf("sqlite widening approval=%v risk=%s", PlanRequiresApproval(plan, operation), PlanOperationRisk(plan, operation))
	}
	narrowed, err := Diff(boundedTitleSchema(t, "notes", "title", "ck_notes_title", "pk_notes", 500, false), boundedTitleSchema(t, "notes", "title", "ck_notes_title", "pk_notes", 200, false))
	if err != nil {
		t.Fatal(err)
	}
	if risk := PlanOperationRisk(narrowed, operationOfKind(t, narrowed, AlterColumnType)); risk != RiskDataLoss {
		t.Fatalf("sqlite narrowing risk=%s", risk)
	}
}

func TestReviewedHistoryAcceptsReinstatedConstraintsWithOrWithoutTheFormerApproval(t *testing.T) {
	before := boundedTitleSchema(t, "notes", "title", "ck_notes_title", "pk_notes", 200, false)
	after := boundedTitleSchema(t, "writers", "headline", "ck_writers_headline", "pk_writers", 200, true)
	plan, err := Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	entry := ManifestEntry{Operations: plan.Operations, Phases: plan.Phases, BeforePhysical: plan.BeforeFingerprint, AfterPhysical: plan.AfterFingerprint, BeforeSnapshot: before, AfterSnapshot: after}
	var legacy []Approval
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
		if RequiresApproval(operation) {
			legacy = append(legacy, Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	if len(legacy) != 2 {
		t.Fatalf("released policy approvals = %d, want the check and the primary key", len(legacy))
	}
	if err := validateEntrySemantics(entry); err != nil {
		t.Fatalf("entry without superseded approvals: %v", err)
	}
	entry.Approvals = legacy
	if err := validateEntrySemantics(entry); err != nil {
		t.Fatalf("released entry with its former approvals: %v", err)
	}
}
