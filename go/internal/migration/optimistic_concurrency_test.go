package migration

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

const (
	concurrencyTable ir.ModelID = "71000000000000000000000000000001"
	concurrencyID    ir.FieldID = "72000000000000000000000000000001"
	concurrencyToken ir.FieldID = "72000000000000000000000000000002"
)

func TestPhysicalV3MustBeFrozenBeforeCurrentVersionAdvances(t *testing.T) {
	if physical.SchemaFormatVersion != 3 || physical.CanonicalFormatVersion != 3 {
		t.Fatalf("physical current advanced beyond unpublished v3 without freezing v3 normalization/canonical replay: %d/%d", physical.SchemaFormatVersion, physical.CanonicalFormatVersion)
	}
}

func TestReviewedTransitionEntrypointsDoNotRouteThroughMutablePlannerSource(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration source")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(source), "diff.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, call := range []string{"return diffHistoricalV1ToV2Tagged(before, after)", "return diffOptimisticConcurrencyV2ToV3Tagged(before, after)"} {
		if strings.Count(text, call) != 1 {
			t.Fatalf("reviewed transition entrypoint lost exact retained call %q", call)
		}
	}
	if strings.Count(text, "return DiffHistoricalV3(before, after)") != 2 {
		t.Fatal("reviewed current/historical v3 dispatch is not pinned to the retained v3 planner")
	}
	v3Start := strings.Index(text, "func DiffHistoricalV3(")
	if v3Start < 0 {
		t.Fatal("cannot isolate retained v3 planner entrypoint")
	}
	v3End := strings.Index(text[v3Start:], "// DiffPhysicalFormatUpgrade")
	if v3End < 0 {
		t.Fatal("cannot isolate retained v3 planner entrypoint")
	}
	v3Body := text[v3Start : v3Start+v3End]
	if strings.Contains(v3Body, "withPlanSnapshotFacts(") || strings.Count(v3Body, "withHistoricalV3PlanSnapshotFacts(") != 1 {
		t.Fatal("retained v3 planner facts route through mutable current normalization")
	}
	frozenRaw, err := os.ReadFile(filepath.Join(filepath.Dir(source), "historical_v3_diff_frozen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(frozenRaw), "withPlanSnapshotFacts(") {
		t.Fatal("retained v3 tagged planner installs facts through mutable current normalization")
	}
	snapshotRaw, err := os.ReadFile(filepath.Join(filepath.Dir(source), "plan_snapshot.go"))
	if err != nil {
		t.Fatal(err)
	}
	snapshotText := string(snapshotRaw)
	cloneStart := strings.Index(snapshotText, "func cloneHistoricalV3PlanSnapshot(")
	if cloneStart < 0 {
		t.Fatal("retained v3 snapshot clone is absent")
	}
	cloneEnd := strings.Index(snapshotText[cloneStart:], "func clonePlanSnapshot(")
	if cloneEnd < 0 {
		t.Fatal("cannot isolate retained v3 snapshot clone")
	}
	cloneBody := snapshotText[cloneStart : cloneStart+cloneEnd]
	if strings.Count(cloneBody, "physical.NormalizeHistoricalV3(snapshot)") != 1 || strings.Contains(cloneBody, "physical.Normalize(snapshot)") {
		t.Fatal("retained v3 snapshot clone routes through mutable current normalization")
	}
}

func TestHistoricalV3ReviewedPlanFactsAreFrozenAndDetached(t *testing.T) {
	before, after := optimisticConcurrencyUpgradeSchemas(t, ir.SQLite)
	before.Version, before.CanonicalVersion = 3, 3
	before, err := physical.NormalizeHistoricalV3(before)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := DiffHistoricalV3(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if plan.snapshotFacts == nil {
		t.Fatal("reviewed v3 plan lacks typed snapshot facts")
	}
	wantBeforeName := plan.snapshotFacts.before.Tables[0].Columns[0].Name
	wantAfterName := plan.snapshotFacts.after.Tables[0].Columns[0].Name
	wantConcurrency := *plan.snapshotFacts.after.Tables[0].OptimisticConcurrency

	before.Tables[0].Columns[0].Name = "mutated-before"
	after.Tables[0].Columns[0].Name = "mutated-after"
	*after.Tables[0].OptimisticConcurrency = concurrencyID
	if plan.snapshotFacts.before.Tables[0].Columns[0].Name != wantBeforeName ||
		plan.snapshotFacts.after.Tables[0].Columns[0].Name != wantAfterName ||
		*plan.snapshotFacts.after.Tables[0].OptimisticConcurrency != wantConcurrency {
		t.Fatal("reviewed v3 plan facts retain caller-owned nested storage")
	}
}

func TestFrozenV3PlannerMatchesReviewedCurrentPlannerAndOwnsConcurrencyRules(t *testing.T) {
	for _, provider := range []ir.Provider{ir.SQLite, ir.PostgreSQL} {
		t.Run(string(provider), func(t *testing.T) {
			v2, after := optimisticConcurrencyUpgradeSchemas(t, provider)
			before := v2
			before.Version, before.CanonicalVersion = 3, 3
			before, err := physical.NormalizeHistoricalV3(before)
			if err != nil {
				t.Fatal(err)
			}

			current, err := Diff(before, after)
			if err != nil {
				t.Fatal(err)
			}
			frozen, err := DiffHistoricalV3(before, after)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(current, frozen) {
				t.Fatalf("frozen v3 planner differs from reviewed current v3:\ncurrent=%#v\nfrozen=%#v", current, frozen)
			}
			reviewed, err := DiffReviewed(before, after)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reviewed, frozen) {
				t.Fatal("reviewed v3 dispatch bypassed retained v3 planner")
			}

			removed := after
			removed.Tables = append([]physical.PhysicalTable(nil), after.Tables...)
			removed.Tables[0] = after.Tables[0]
			removed.Tables[0].OptimisticConcurrency = nil
			if _, err := DiffHistoricalV3(after, removed); err == nil || !strings.Contains(err.Error(), "cannot be removed") {
				t.Fatalf("frozen v3 planner removal error=%v", err)
			}
		})
	}
}

func TestOptimisticConcurrencyPhysicalUpgradeHasExactClosedInitializationGraph(t *testing.T) {
	before, after := optimisticConcurrencyUpgradeSchemas(t, ir.SQLite)
	afterFingerprint, err := physical.PhysicalFingerprint(after)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := DiffReviewed(before, after)
	if err != nil {
		t.Fatal(err)
	}
	want := []OperationKind{AddColumn, InitializeConcurrencyColumn, ValidateConstraint, AlterColumnNullability, RecordSchemaVersion}
	got := make([]OperationKind, len(plan.Operations))
	for index, operation := range plan.Operations {
		got[index] = operation.Kind
		if operation.Transform != nil || operation.Kind == BackfillColumn || operation.Kind == ManualStep {
			t.Fatalf("operation invented application/manual SQL: %#v", operation)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operation graph = %v; want %v", got, want)
	}
	unchangedFingerprint, err := physical.PhysicalFingerprint(after)
	if err != nil {
		t.Fatal(err)
	}
	if unchangedFingerprint != afterFingerprint || after.Tables[0].OptimisticConcurrency == nil || *after.Tables[0].OptimisticConcurrency != concurrencyToken {
		t.Fatal("retained v2-to-v3 planner mutated its target snapshot while projecting the v2 core")
	}
	assertFrozenPlanDigest(t, plan, "91ba63bbeb582600299e14f15845e18e62026444caf15bcce87f074e6830b2f5")
	initialize := plan.Operations[1]
	if initialize.ObjectID != string(concurrencyToken) || initialize.Risk != RiskRewrite || !RequiresApproval(initialize) {
		t.Fatalf("initialize operation = %#v", initialize)
	}
	if err := ValidatePlan(plan, nil); err == nil || !strings.Contains(err.Error(), "requires exact object-scoped approval") {
		t.Fatalf("unapproved initialization accepted: %v", err)
	}
	approval := Approval{OperationID: initialize.ID, Risk: initialize.Risk, Before: initialize.Before, After: initialize.After}
	if err := ValidatePlan(plan, []Approval{approval}); err != nil {
		t.Fatal(err)
	}
	assertDependsOnKind(t, plan.Operations, plan.Operations[1], AddColumn)
	assertDependsOnKind(t, plan.Operations, plan.Operations[2], InitializeConcurrencyColumn)
	assertDependsOnKind(t, plan.Operations, plan.Operations[3], ValidateConstraint)
}

func TestOptimisticConcurrencyReviewedPhysicalVersionMatrixIsClosed(t *testing.T) {
	v2, v3 := optimisticConcurrencyUpgradeSchemas(t, ir.SQLite)
	v1 := v2
	v1.Version, v1.CanonicalVersion = 1, 1
	v1.Provider.Driver = physical.DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}
	v1, err := physical.NormalizeHistoricalV1(v1)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		name          string
		before, after physical.PhysicalSchema
	}{
		{name: "v1-v1", before: v1, after: v1},
		{name: "v1-v2", before: v1, after: v2},
		{name: "v1-v3", before: v1, after: v3},
		{name: "v2-v2", before: v2, after: v2},
		{name: "v2-v3", before: v2, after: v3},
		{name: "v3-v3", before: v3, after: v3},
	} {
		t.Run(pair.name, func(t *testing.T) {
			if _, err := DiffReviewed(pair.before, pair.after); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, pair := range []struct{ before, after physical.PhysicalSchema }{{v2, v1}, {v3, v2}, {v3, v1}} {
		if _, err := DiffReviewed(pair.before, pair.after); err == nil {
			t.Fatalf("unsupported reviewed pair %d/%d -> %d/%d accepted", pair.before.Version, pair.before.CanonicalVersion, pair.after.Version, pair.after.CanonicalVersion)
		}
	}
}

func TestOptimisticConcurrencyCannotAdoptRemoveOrSwitchField(t *testing.T) {
	v2, v3 := optimisticConcurrencyUpgradeSchemas(t, ir.SQLite)
	existing := v2
	existing.Tables[0].Columns = append(existing.Tables[0].Columns, v3.Tables[0].Columns[1])
	existing.Tables[0].Columns[1].Nullable = false
	existing.Tables[0].Columns[1].Ordinal = 1
	existing, err := physical.NormalizeHistoricalV2(existing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DiffOptimisticConcurrencyPhysicalUpgrade(existing, v3); err == nil || !strings.Contains(err.Error(), "cannot adopt existing field") {
		t.Fatalf("existing-field adoption error = %v", err)
	}
	removed := v3
	removed.Tables = append([]physical.PhysicalTable(nil), v3.Tables...)
	removed.Tables[0] = v3.Tables[0]
	removed.Tables[0].OptimisticConcurrency = nil
	if _, err := Diff(v3, removed); err == nil || !strings.Contains(err.Error(), "cannot be removed") {
		t.Fatalf("removal error = %v", err)
	}
}

func optimisticConcurrencyUpgradeSchemas(t *testing.T, provider ir.Provider) (physical.PhysicalSchema, physical.PhysicalSchema) {
	t.Helper()
	manifest := physical.SQLiteManifest()
	namespace := physical.Namespace{Name: "main"}
	oldStorage, tokenStorage := physical.StorageType{Kind: physical.StorageSQLiteText}, physical.StorageType{Kind: physical.StorageSQLiteInteger}
	if provider == ir.PostgreSQL {
		manifest = physical.PostgreSQLManifest()
		namespace = physical.Namespace{Name: "public"}
		oldStorage, tokenStorage = physical.StorageType{Kind: physical.StoragePostgreSQLText}, physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
	}
	before := physical.PhysicalSchema{Version: 2, CanonicalVersion: 2, Provider: manifest, Namespace: namespace, Tables: []physical.PhysicalTable{{ID: concurrencyTable, Name: "records", Columns: []physical.PhysicalColumn{{ID: concurrencyID, Name: "id", Ordinal: 0, Storage: oldStorage, Nullable: false, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}}}}}
	var err error
	before, err = physical.NormalizeHistoricalV2(before)
	if err != nil {
		t.Fatal(err)
	}
	after := before
	after.Version, after.CanonicalVersion = physical.SchemaFormatVersion, physical.CanonicalFormatVersion
	after.Tables = append([]physical.PhysicalTable(nil), before.Tables...)
	after.Tables[0] = before.Tables[0]
	after.Tables[0].Columns = append([]physical.PhysicalColumn(nil), before.Tables[0].Columns...)
	after.Tables[0].Columns = append(after.Tables[0].Columns, physical.PhysicalColumn{ID: concurrencyToken, Name: "version", Ordinal: 1, Storage: tokenStorage, Nullable: false, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
	field := concurrencyToken
	after.Tables[0].OptimisticConcurrency = &field
	after, err = physical.Normalize(after)
	if err != nil {
		t.Fatal(err)
	}
	return before, after
}
