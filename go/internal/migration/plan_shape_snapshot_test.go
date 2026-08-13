package migration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestDiffOwnsDetachedNonPersistedSnapshotFactsAndRepresentsNoChangeExactly(t *testing.T) {
	modelID := ir.ModelID("81000000000000000000000000000001")
	fieldID := ir.FieldID("81000000000000000000000000000002")
	unchanged := schema()
	unchanged.Tables = []physical.PhysicalTable{{
		ID: modelID, Name: "physical_private_posts",
		Columns: []physical.PhysicalColumn{{
			ID: fieldID, Name: "physical_private_title",
			Storage: physical.StorageType{Kind: physical.StorageSQLiteText},
			Default: physical.PhysicalDefault{Kind: physical.DefaultNone},
		}},
	}}

	plan, err := Diff(unchanged, unchanged)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 0 || len(plan.Phases) != 0 || plan.BeforeFingerprint != plan.AfterFingerprint {
		t.Fatalf("no-change plan invented work: %#v", plan)
	}
	if err := ValidatePlanShape(plan); err != nil {
		t.Fatalf("no-change shape: %v", err)
	}
	facts, ok := plan.SnapshotFacts()
	if !ok {
		t.Fatal("Diff omitted its typed snapshot facts")
	}
	first := facts.Before()
	first.Tables[0].Name = "mutated_by_consumer"
	unchanged.Tables[0].Name = "mutated_by_caller"
	second := facts.Before()
	if second.Tables[0].Name != "physical_private_posts" {
		t.Fatalf("snapshot facts share mutable storage: %q", second.Tables[0].Name)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "beforeSnapshot") || strings.Contains(string(encoded), "physical_private_posts") {
		t.Fatalf("non-persisted facts entered Plan JSON: %s", encoded)
	}
}

func TestValidatePlanShapeRejectsReorderedPlanAndPhaseOperations(t *testing.T) {
	after := schema()
	after.Tables = []physical.PhysicalTable{{
		ID: "82000000000000000000000000000001", Name: "items",
		Columns: []physical.PhysicalColumn{{
			ID: "82000000000000000000000000000002", Name: "id",
			Storage: physical.StorageType{Kind: physical.StorageSQLiteText},
			Default: physical.PhysicalDefault{Kind: physical.DefaultNone},
		}},
	}}
	plan, err := Diff(schema(), after)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) < 2 || len(plan.Phases) != 1 {
		t.Fatalf("fixture lacks ordered work: %#v", plan)
	}
	if err := ValidatePlanShape(plan); err != nil {
		t.Fatal(err)
	}

	reordered := plan
	reordered.Operations = append([]Operation(nil), plan.Operations...)
	reordered.Operations[0], reordered.Operations[1] = reordered.Operations[1], reordered.Operations[0]
	if err := ValidatePlanShape(reordered); err == nil {
		t.Fatal("reordered operation inventory accepted")
	}

	reordered = plan
	reordered.Phases = append([]Phase(nil), plan.Phases...)
	reordered.Phases[0].Operations = append([]OperationID(nil), plan.Phases[0].Operations...)
	reordered.Phases[0].Operations[0], reordered.Phases[0].Operations[1] = reordered.Phases[0].Operations[1], reordered.Phases[0].Operations[0]
	if err := ValidatePlanShape(reordered); err == nil {
		t.Fatal("reordered phase operation inventory accepted")
	}

	forgedProvider := plan
	forgedProvider.Provider = ir.Provider("future")
	if err := ValidatePlanShape(forgedProvider); err == nil {
		t.Fatal("unknown plan provider accepted")
	}
	noFacts := Plan{
		Provider:          ir.Provider("future"),
		BeforeFingerprint: Checksum([]byte("same")),
		AfterFingerprint:  Checksum([]byte("same")),
	}
	if err := ValidatePlanShape(noFacts); err == nil {
		t.Fatal("unknown provider accepted at the standalone shape boundary")
	}

	forgedInitial := plan
	forgedInitial.Initial = !plan.Initial
	if err := ValidatePlanShape(forgedInitial); err == nil {
		t.Fatal("initial classification differing from typed snapshots accepted")
	}
}
