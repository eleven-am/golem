package migration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestFrozenV1ToV2PlannerReproducesPublishedSocial0003Graph(t *testing.T) {
	wantChains := map[string]Digest{
		"sqlite":     "164a5de13865f9e2d4c8f424e494c764519f77b367b637510982e79f95d501d3",
		"postgresql": "19c9b8114ba2f7cf7853e13bd2970d56aaab06db749f715f6442e9b45e9512b6",
	}
	for provider, wantChain := range wantChains {
		t.Run(provider, func(t *testing.T) {
			path := filepath.Join("..", "..", "examples", "social", "migrations", provider, "manifest.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest Manifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			var published *ManifestEntry
			for index := range manifest.Entries {
				if manifest.Entries[index].ID == "0003_physical_v2" {
					published = &manifest.Entries[index]
					break
				}
			}
			if published == nil {
				t.Fatal("published 0003_physical_v2 entry is absent")
			}
			if published.ChainHash != wantChain {
				t.Fatalf("published chain=%s want=%s", published.ChainHash, wantChain)
			}
			plan, err := DiffPhysicalFormatUpgrade(published.BeforeSnapshot, published.AfterSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			if plan.BeforeFingerprint != published.BeforePhysical || plan.AfterFingerprint != published.AfterPhysical || !reflect.DeepEqual(plan.Operations, published.Operations) || !reflect.DeepEqual(plan.Phases, published.Phases) {
				t.Fatalf("frozen v1-to-v2 replay diverged from published %s 0003 graph", provider)
			}
		})
	}
}

func TestFrozenV2PlannerRetainsWideningBackfillAndGeneratedDependencyBranches(t *testing.T) {
	base := frozenV2PlannerSchema(t)
	t.Run("safe widening", func(t *testing.T) {
		after := frozenV2Clone(t, base)
		after.Tables[0].Columns[0].Storage = physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
		after = frozenV2Normalize(t, after)
		plan, err := DiffHistoricalV2(base, after)
		if err != nil {
			t.Fatal(err)
		}
		operation := operationByKind(t, plan, AlterColumnType)
		if operation.Risk != RiskRewrite {
			t.Fatalf("safe widening risk=%s want rewrite", operation.Risk)
		}
		assertFrozenPlanDigest(t, plan, "27a733b2fbc87f5e6b86cbf4ce2a6293c1cda632aec5e5b90a299f9d775a9f58")
	})
	t.Run("required column backfill", func(t *testing.T) {
		after := frozenV2Clone(t, base)
		after.Tables[0].Columns = append(after.Tables[0].Columns, physical.PhysicalColumn{ID: "92000000000000000000000000000003", Name: "required", Ordinal: 2, Storage: physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
		after = frozenV2Normalize(t, after)
		plan, err := DiffHistoricalV2(base, after)
		if err != nil {
			t.Fatal(err)
		}
		want := []OperationKind{AddColumn, BackfillColumn, ValidateConstraint, AlterColumnNullability, RecordSchemaVersion}
		got := make([]OperationKind, len(plan.Operations))
		for index := range plan.Operations {
			got[index] = plan.Operations[index].Kind
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("frozen required-column graph=%v want=%v", got, want)
		}
		assertFrozenPlanDigest(t, plan, "dd82d181e1a855fe95a21afd4322105666fa73117b59ec91347d7233bbc075e2")
	})
	t.Run("generated detach recreate", func(t *testing.T) {
		before := frozenV2Clone(t, base)
		after := frozenV2Clone(t, base)
		before.Tables[0].Columns[1].Generated = &physical.GeneratedExpression{Kind: physical.GeneratedStored, Expression: frozenColumnExpression(before.Tables[0].Columns[0])}
		before.Tables[0].Columns[1].Storage = before.Tables[0].Columns[0].Storage
		after.Tables[0].Columns[0].Storage = physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
		after.Tables[0].Columns[1].Storage = physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
		after.Tables[0].Columns[1].Generated = &physical.GeneratedExpression{Kind: physical.GeneratedStored, Expression: frozenColumnExpression(after.Tables[0].Columns[0])}
		before, after = frozenV2Normalize(t, before), frozenV2Normalize(t, after)
		plan, err := DiffHistoricalV2(before, after)
		if err != nil {
			t.Fatal(err)
		}
		if operationByObjectKind(plan, "92000000000000000000000000000002", DropColumn) == nil || operationByObjectKind(plan, "92000000000000000000000000000002", AddColumn) == nil {
			t.Fatalf("generated widening omitted detach/recreate: %#v", plan.Operations)
		}
		assertFrozenPlanDigest(t, plan, "a36ada534e5a8e8447ff2afedb53a45b14f1cb1a6c72afc0befb3822e433a38a")
	})
}

func frozenV2PlannerSchema(t *testing.T) physical.PhysicalSchema {
	t.Helper()
	source := ir.FieldID("92000000000000000000000000000001")
	derived := ir.FieldID("92000000000000000000000000000002")
	return frozenV2Normalize(t, physical.PhysicalSchema{Version: 2, CanonicalVersion: 2, Provider: physical.PostgreSQLManifest(), Namespace: physical.Namespace{Name: "public"}, Tables: []physical.PhysicalTable{{ID: "91000000000000000000000000000001", Name: "metrics", Columns: []physical.PhysicalColumn{
		{ID: source, Name: "source", Ordinal: 0, Storage: physical.StorageType{Kind: physical.StoragePostgreSQLInteger}, Nullable: false, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
		{ID: derived, Name: "derived", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StoragePostgreSQLInteger}, Nullable: false, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
	}}}})
}

func frozenV2Clone(t *testing.T, schema physical.PhysicalSchema) physical.PhysicalSchema {
	t.Helper()
	return frozenV2Normalize(t, schema)
}

func frozenV2Normalize(t *testing.T, schema physical.PhysicalSchema) physical.PhysicalSchema {
	t.Helper()
	normalized, err := physical.NormalizeHistoricalV2(schema)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func frozenColumnExpression(column physical.PhysicalColumn) physical.Expression {
	field := column.ID
	return physical.Expression{Kind: physical.ExpressionColumn, Type: column.Storage, Nullable: column.Nullable, Column: &field, Operands: []physical.Expression{}}
}

func operationByKind(t *testing.T, plan Plan, kind OperationKind) Operation {
	t.Helper()
	for _, operation := range plan.Operations {
		if operation.Kind == kind {
			return operation
		}
	}
	t.Fatalf("operation kind %s absent", kind)
	return Operation{}
}

func operationByObjectKind(plan Plan, object string, kind OperationKind) *Operation {
	for index := range plan.Operations {
		if plan.Operations[index].ObjectID == object && plan.Operations[index].Kind == kind {
			return &plan.Operations[index]
		}
	}
	return nil
}

func assertFrozenPlanDigest(t *testing.T, plan Plan, want string) {
	t.Helper()
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	got := string(Checksum(raw))
	if got != want {
		t.Fatalf("frozen full-plan digest=%s want=%s\n%s", got, want, fmt.Sprintf("%s", raw))
	}
}
