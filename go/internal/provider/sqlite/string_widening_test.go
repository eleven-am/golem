package sqlite

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
)

func boundedNameFixture(t *testing.T, length uint32) physical.PhysicalSchema {
	t.Helper()
	schema := incrementalFixtureSchema(t, false)
	schema.Tables = append([]physical.PhysicalTable(nil), schema.Tables...)
	columns := append([]physical.PhysicalColumn(nil), schema.Tables[0].Columns...)
	for index := range columns {
		if columns[index].ID == fixtureItemNameField {
			columns[index].Storage.Length = length
		}
	}
	schema.Tables[0].Columns = columns
	return normalizeMigrationFixture(t, schema)
}

func unrenderedFixtureEntry(t *testing.T, before, after physical.PhysicalSchema) migration.ManifestEntry {
	t.Helper()
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	beforeFingerprint, _ := physical.PhysicalFingerprint(before)
	afterFingerprint, _ := physical.PhysicalFingerprint(after)
	entry := migration.ManifestEntry{ID: "001_string_length", Operations: plan.Operations, Phases: plan.Phases, BeforePhysical: migration.Digest(beforeFingerprint.String()), AfterPhysical: migration.Digest(afterFingerprint.String()), BeforeSnapshot: before, AfterSnapshot: after}
	for _, operation := range plan.Operations {
		if migration.PlanRequiresApproval(plan, operation) {
			entry.Approvals = append(entry.Approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	return entry
}

func TestSQLiteStringLengthChangeRebuildsOnlyWhenEveryValueSurvives(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		before, after uint32
		accepted      bool
	}{
		{name: "raised bound", before: 200, after: 500, accepted: true},
		{name: "removed bound", before: 200, after: 0, accepted: true},
		{name: "lowered bound", before: 500, after: 200},
		{name: "added bound", before: 0, after: 200},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			plan, err := New().PlanIncremental(unrenderedFixtureEntry(t, boundedNameFixture(t, testCase.before), boundedNameFixture(t, testCase.after)))
			if testCase.accepted {
				if err != nil || len(plan.Rebuilds) != 1 {
					t.Fatalf("plan=%#v err=%v", plan.Rebuilds, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "requires an explicit reviewed cast") {
				t.Fatalf("narrowing error = %v", err)
			}
		})
	}
}
