package migration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestHistoricalV1HeadProjectionReproducesPublishedV2BoundaryExactly(t *testing.T) {
	for _, provider := range []string{"sqlite", "postgresql"} {
		t.Run(provider, func(t *testing.T) {
			entry := publishedSocialMigrationEntry(t, provider, "0003_physical_v2")
			projected, err := projectHistoricalV1HeadToV2(entry.BeforeSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			want, err := physical.NormalizeHistoricalV2(entry.AfterSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(projected, want) {
				t.Fatal("before-derived v2 projection differs from published retained v1-to-v2 boundary")
			}
			beforeCopy := entry.BeforeSnapshot
			if _, err := projectHistoricalV1HeadToV2(beforeCopy); err != nil || !reflect.DeepEqual(beforeCopy, entry.BeforeSnapshot) {
				t.Fatalf("projection mutated source or became unstable: %v", err)
			}
		})
	}
}

func TestHistoricalV1ToV3CompositionOwnsOnePublicationAndPreservesFrozenLegs(t *testing.T) {
	v2, v3 := optimisticConcurrencyUpgradeSchemas(t, ir.SQLite)
	v1 := v2
	v1.Version, v1.CanonicalVersion = 1, 1
	v1.Provider.Driver = physical.DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}
	v1, err := physical.NormalizeHistoricalV1(v1)
	if err != nil {
		t.Fatal(err)
	}
	middle, err := projectHistoricalV1HeadToV2(v1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := DiffPhysicalFormatUpgrade(v1, middle)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DiffOptimisticConcurrencyPhysicalUpgrade(middle, v3)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := DiffReviewed(v1, v3)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePlanShape(plan); err != nil {
		t.Fatal(err)
	}
	if countOperationKind(plan.Operations, RecordSchemaVersion) != 1 || plan.Operations[len(plan.Operations)-1].Kind != RecordSchemaVersion {
		t.Fatalf("composed publication operations=%#v", plan.Operations)
	}
	wantCount := len(first.Operations) + len(second.Operations) - 1
	if len(plan.Operations) != wantCount {
		t.Fatalf("composed operations=%d want=%d", len(plan.Operations), wantCount)
	}
	facts, exists := plan.SnapshotFacts()
	if !exists || facts.Before().Version != 1 || facts.Before().CanonicalVersion != 1 || facts.After().Version != 3 || facts.After().CanonicalVersion != 3 {
		t.Fatalf("composed snapshot facts absent or relabeled: %#v exists=%t", facts, exists)
	}
	if plan.BeforeFingerprint != first.BeforeFingerprint || plan.AfterFingerprint != second.AfterFingerprint {
		t.Fatalf("composed fingerprints=%s..%s want=%s..%s", plan.BeforeFingerprint, plan.AfterFingerprint, first.BeforeFingerprint, second.AfterFingerprint)
	}
	if !allOperationsReachFinalRecord(plan.Operations) {
		t.Fatal("composed operation graph contains work outside the final publication dependency closure")
	}
	firstWork, _ := withoutSoleSchemaVersion(first.Operations)
	secondWork, _ := withoutSoleSchemaVersion(second.Operations)
	firstLeaves := terminalOperationIDs(firstWork)
	for _, rootIndex := range rootOperationIndexes(secondWork) {
		composed := operationByID(t, plan.Operations, secondWork[rootIndex].ID)
		for _, leaf := range firstLeaves {
			if !containsOperationID(composed.Dependencies, leaf) {
				t.Fatalf("second-leg root %s is not ordered after first-leg leaf %s", composed.ID, leaf)
			}
		}
	}
}

func TestHistoricalV1ToV3CompositionKeepsNewVersionedTableInSecondLeg(t *testing.T) {
	v2, v3 := optimisticConcurrencyUpgradeSchemas(t, ir.SQLite)
	v1 := v2
	v1.Version, v1.CanonicalVersion = 1, 1
	v1.Provider.Driver = physical.DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}
	v1, _ = physical.NormalizeHistoricalV1(v1)
	version := ir.FieldID("d1000000000000000000000000000002")
	id := ir.FieldID("d1000000000000000000000000000001")
	v3.Tables = append(v3.Tables, physical.PhysicalTable{
		ID: "d0000000000000000000000000000001", Name: "new_versioned",
		OptimisticConcurrency: &version,
		Columns: []physical.PhysicalColumn{
			{ID: id, Name: "id", Ordinal: 0, Storage: physical.StorageType{Kind: physical.StorageSQLiteBlob}, Nullable: false, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			{ID: version, Name: "version", Ordinal: 1, Storage: physical.StorageType{Kind: physical.StorageSQLiteInteger}, Nullable: false, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "d2000000000000000000000000000001", Name: "pk_new_versioned", Columns: []ir.FieldID{id}},
	})
	v3, err := physical.NormalizeHistoricalV3(v3)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := DiffReviewed(v1, v3)
	if err != nil {
		t.Fatal(err)
	}
	if operationByObjectKind(plan, "d0000000000000000000000000000001", CreateTable) == nil {
		t.Fatalf("new versioned table is absent from composed plan: %#v", plan.Operations)
	}
	if operationByObjectKind(plan, string(version), InitializeConcurrencyColumn) != nil {
		t.Fatal("new versioned table incorrectly received an existing-row concurrency initialization")
	}
}

func TestHistoricalV1ToV3CompositionRejectsExistingFieldConcurrencyAdoption(t *testing.T) {
	v2, v3 := optimisticConcurrencyUpgradeSchemas(t, ir.SQLite)
	v1 := v2
	v1.Version, v1.CanonicalVersion = 1, 1
	v1.Provider.Driver = physical.DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}
	token := *v3.Tables[0].OptimisticConcurrency
	column, exists := transitionColumn(v3.Tables[0], token)
	if !exists {
		t.Fatal("fixture concurrency column is absent")
	}
	v1.Tables[0].Columns = append(v1.Tables[0].Columns, column)
	v1, err := physical.NormalizeHistoricalV1(v1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DiffReviewed(v1, v3); err == nil || !strings.Contains(err.Error(), "cannot adopt existing field") {
		t.Fatalf("existing-field adoption error=%v", err)
	}
}

func TestHistoricalV1ToV3CompositionOrdersNonemptyFrozenLegs(t *testing.T) {
	beforeEntry := publishedSocialMigrationEntry(t, "postgresql", "0003_physical_v2")
	afterEntry := publishedSocialMigrationEntry(t, "postgresql", "0005_add_versioned_notes")
	before, after := beforeEntry.BeforeSnapshot, afterEntry.AfterSnapshot
	middle, err := projectHistoricalV1HeadToV2(before)
	if err != nil {
		t.Fatal(err)
	}
	first, err := DiffPhysicalFormatUpgrade(before, middle)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DiffOptimisticConcurrencyPhysicalUpgrade(middle, after)
	if err != nil {
		t.Fatal(err)
	}
	firstWork, err := withoutSoleSchemaVersion(first.Operations)
	if err != nil {
		t.Fatal(err)
	}
	secondWork, err := withoutSoleSchemaVersion(second.Operations)
	if err != nil {
		t.Fatal(err)
	}
	leaves, roots := terminalOperationIDs(firstWork), rootOperationIndexes(secondWork)
	if len(leaves) == 0 || len(roots) == 0 {
		t.Fatalf("barrier fixture is not adversarial: first leaves=%v second roots=%v", leaves, roots)
	}
	plan, err := DiffReviewed(before, after)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range firstWork {
		if got := operationByID(t, plan.Operations, operation.ID); !reflect.DeepEqual(got, operation) {
			t.Fatalf("first-leg operation %s changed: got=%#v want=%#v", operation.ID, got, operation)
		}
	}
	rootSet := map[int]bool{}
	for _, root := range roots {
		rootSet[root] = true
	}
	for index, operation := range secondWork {
		got := operationByID(t, plan.Operations, operation.ID)
		if !rootSet[index] {
			if !reflect.DeepEqual(got, operation) {
				t.Fatalf("non-root second-leg operation %s changed: got=%#v want=%#v", operation.ID, got, operation)
			}
			continue
		}
		want := operation
		want.Dependencies = sortedUniqueOperationIDs(append(append([]OperationID(nil), operation.Dependencies...), leaves...))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("second-leg root %s has non-barrier drift: got=%#v want=%#v", operation.ID, got, want)
		}
	}
	for _, root := range roots {
		composed := operationByID(t, plan.Operations, secondWork[root].ID)
		for _, leaf := range leaves {
			if !containsOperationID(composed.Dependencies, leaf) {
				t.Fatalf("second-leg root %s is not ordered after first-leg leaf %s", composed.ID, leaf)
			}
		}
	}
	beforeFingerprint, _ := physical.HistoricalPhysicalFingerprint(before)
	afterFingerprint, _ := physical.HistoricalV3PhysicalFingerprint(after)
	record := plan.Operations[len(plan.Operations)-1]
	wantRecord := Operation{Kind: RecordSchemaVersion, Stage: 100, ObjectID: "schema-version", Before: Digest(beforeFingerprint.String()), After: Digest(afterFingerprint.String()), Mode: Transactional, Risk: RiskSafe, LogicalPath: "schema"}
	wantRecord.ID = historicalV2StableOperationID(wantRecord.Kind, wantRecord.ObjectID, wantRecord.Before, wantRecord.After)
	for _, operation := range append(append([]Operation(nil), firstWork...), secondWork...) {
		wantRecord.Dependencies = append(wantRecord.Dependencies, operation.ID)
	}
	wantRecord.Dependencies = sortedUniqueOperationIDs(wantRecord.Dependencies)
	if !reflect.DeepEqual(record, wantRecord) {
		t.Fatalf("final record changed: got=%#v want=%#v", record, wantRecord)
	}
}

func publishedSocialMigrationEntry(t *testing.T, provider, id string) ManifestEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "social", "migrations", provider, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Entries {
		if entry.ID == MigrationID(id) {
			return entry
		}
	}
	t.Fatalf("published migration %s/%s is absent", provider, id)
	return ManifestEntry{}
}

func countOperationKind(operations []Operation, kind OperationKind) int {
	count := 0
	for _, operation := range operations {
		if operation.Kind == kind {
			count++
		}
	}
	return count
}

func allOperationsReachFinalRecord(operations []Operation) bool {
	if len(operations) == 0 || operations[len(operations)-1].Kind != RecordSchemaVersion {
		return false
	}
	seen := map[OperationID]bool{}
	byID := map[OperationID]Operation{}
	for _, operation := range operations {
		byID[operation.ID] = operation
	}
	var visit func(OperationID)
	visit = func(id OperationID) {
		if seen[id] {
			return
		}
		seen[id] = true
		for _, dependency := range byID[id].Dependencies {
			visit(dependency)
		}
	}
	visit(operations[len(operations)-1].ID)
	return len(seen) == len(operations)
}

func operationByID(t *testing.T, operations []Operation, id OperationID) Operation {
	t.Helper()
	for _, operation := range operations {
		if operation.ID == id {
			return operation
		}
	}
	t.Fatalf("operation %s is absent", id)
	return Operation{}
}

func containsOperationID(values []OperationID, target OperationID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func sortedUniqueOperationIDs(values []OperationID) []OperationID {
	result := append([]OperationID(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	if len(result) == 0 {
		return result
	}
	end := 1
	for _, value := range result[1:] {
		if value != result[end-1] {
			result[end] = value
			end++
		}
	}
	return result[:end]
}
