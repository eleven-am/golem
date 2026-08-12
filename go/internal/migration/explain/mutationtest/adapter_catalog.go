package mutationtest

import (
	"time"

	"github.com/eleven-am/golem/go/internal/p8mutation"
)

// AdapterCatalog is isolated from the original report-core catalog so the
// Plan/Diff integration evidence can be executed and reviewed independently.
func AdapterCatalog() []p8mutation.Mutation {
	gate := func(pkg, test string) p8mutation.Gate {
		return p8mutation.Gate{Directory: "go", Package: pkg, Test: test}
	}
	mutation := func(label, summary string, patch p8mutation.Patch, target p8mutation.Gate) p8mutation.Mutation {
		return p8mutation.Mutation{Label: label, Summary: summary, Patches: []p8mutation.Patch{patch}, Gate: target, Timeout: 2 * time.Minute}
	}
	return []p8mutation.Mutation{
		mutation("MIGRATION_PLAN_DIFF_OMITS_SNAPSHOT_FACTS", "discard the Diff-owned typed snapshot evidence", p8mutation.Patch{
			Path:   "go/internal/migration/plan_snapshot.go",
			Before: "func withPlanSnapshotFacts(plan Plan, before, after physical.PhysicalSchema) Plan {\n\tplan.snapshotFacts = &PlanSnapshotFacts{\n\t\tbefore: clonePlanSnapshot(before),\n\t\tafter:  clonePlanSnapshot(after),\n\t}\n\treturn plan\n}",
			After:  "func withPlanSnapshotFacts(plan Plan, before, after physical.PhysicalSchema) Plan {\n\treturn plan\n}",
		}, gate("./internal/migration", "TestDiffOwnsDetachedNonPersistedSnapshotFactsAndRepresentsNoChangeExactly")),
		mutation("MIGRATION_PLAN_NO_CHANGE_INVENTS_RECORD", "retain the synthetic schema-version operation for identical normalized snapshots", p8mutation.Patch{
			Path:   "go/internal/migration/diff.go",
			Before: "\trightFP, err := afterFingerprint(right)\n\tif err != nil {\n\t\treturn Plan{}, err\n\t}\n\tif reflect.DeepEqual(left, right) {",
			After:  "\trightFP, err := afterFingerprint(right)\n\tif err != nil {\n\t\treturn Plan{}, err\n\t}\n\tif false && reflect.DeepEqual(left, right) {",
		}, gate("./internal/migration", "TestDiffOwnsDetachedNonPersistedSnapshotFactsAndRepresentsNoChangeExactly")),
		mutation("MIGRATION_PLAN_SHAPE_ACCEPTS_REORDERED_OPERATIONS", "accept an operation inventory outside the deterministic DAG order", p8mutation.Patch{
			Path:   "go/internal/migration/plan.go",
			Before: "\tif !sameOrderedOperations(ordered, plan.Operations) {\n\t\treturn fmt.Errorf(\"migration operation inventory is not in deterministic DAG order\")\n\t}",
			After:  "\tif false && !sameOrderedOperations(ordered, plan.Operations) {\n\t\treturn fmt.Errorf(\"migration operation inventory is not in deterministic DAG order\")\n\t}",
		}, gate("./internal/migration", "TestValidatePlanShapeRejectsReorderedPlanAndPhaseOperations")),
		mutation("MIGRATION_PLAN_SHAPE_ACCEPTS_UNKNOWN_PROVIDER", "accept a provider outside the closed portable migration set", p8mutation.Patch{
			Path:   "go/internal/migration/plan.go",
			Before: "\tif plan.Provider != ir.SQLite && plan.Provider != ir.PostgreSQL {\n\t\treturn fmt.Errorf(\"migration plan provider is invalid\")\n\t}",
			After:  "\tif false && plan.Provider != ir.SQLite && plan.Provider != ir.PostgreSQL {\n\t\treturn fmt.Errorf(\"migration plan provider is invalid\")\n\t}",
		}, gate("./internal/migration", "TestValidatePlanShapeRejectsReorderedPlanAndPhaseOperations")),
		mutation("MIGRATION_PLAN_SHAPE_ACCEPTS_FALSE_INITIAL", "accept an initial classification that differs from the Diff-owned snapshots", p8mutation.Patch{
			Path:   "go/internal/migration/plan.go",
			Before: "\t\tif plan.Initial != expectedInitial {\n\t\t\treturn fmt.Errorf(\"migration plan initial classification differs from typed snapshot facts\")\n\t\t}",
			After:  "\t\tif false && plan.Initial != expectedInitial {\n\t\t\treturn fmt.Errorf(\"migration plan initial classification differs from typed snapshot facts\")\n\t\t}",
		}, gate("./internal/migration", "TestValidatePlanShapeRejectsReorderedPlanAndPhaseOperations")),
		mutation("MIGRATION_PLAN_SHAPE_ACCEPTS_INVENTED_HISTORICAL_IDENTITY", "accept invented work for identical v1 snapshots instead of the exact frozen graph", p8mutation.Patch{
			Path:   "go/internal/migration/plan.go",
			Before: "\t\treflect.DeepEqual(plan.Operations, expected.Operations) &&\n\t\treflect.DeepEqual(plan.Phases, expected.Phases)",
			After:  "\t\ttrue",
		}, gate("./internal/migration", "TestHistoricalV1IdenticalReviewedEntryRetainsRecordSchemaVersion")),
		mutation("PHYSICAL_HISTORICAL_ACCEPTS_UNREVIEWED_SQLITE_DRIVER", "accept an arbitrary SQLite driver as the reviewed v1 runtime transition", p8mutation.Patch{
			Path:   "go/internal/physical/normalize.go",
			Before: "\tif schema.Provider.Provider == ir.SQLite && schema.Provider.Driver == (DriverIdentity{Module: \"github.com/ncruces/go-sqlite3\", Adapter: \"sqlx\"}) {",
			After:  "\tif schema.Provider.Provider == ir.SQLite {",
		}, gate("./internal/physical", "TestReviewedHistoricalV1NormalizationAcceptsOnlyReleasedSQLiteDriverTransition")),
		mutation("MIGRATION_EXPLAIN_ADAPTER_ACCEPTS_FORGED_PLAN", "trust a mutated Plan after its Diff-owned facts were attached", p8mutation.Patch{
			Path:   "go/internal/migration/explain/adapter.go",
			Before: "\t\tif !planMatchesSnapshots(plan, before, after) {\n\t\t\treturn Report{}, unavailable()\n\t\t}",
			After:  "\t\tif false && !planMatchesSnapshots(plan, before, after) {\n\t\t\treturn Report{}, unavailable()\n\t\t}",
		}, gate("./internal/migration/explain", "TestMigrationExplainProspectiveAdapterUsesOnlyValidatedPlanSnapshotFacts")),
		mutation("MIGRATION_EXPLAIN_RECREATION_USES_WHOLE_SNAPSHOT_PRESENCE", "replace sealed operation-local recreation presence with whole-snapshot membership", p8mutation.Patch{
			Path:   "go/internal/migration/explain/adapter.go",
			Before: "func operationLocalPresence(operation migration.Operation, beforePresent, afterPresent bool) (bool, bool, bool) {\n\tswitch operation.Kind {",
			After:  "func operationLocalPresence(operation migration.Operation, beforePresent, afterPresent bool) (bool, bool, bool) {\n\tif true {\n\t\treturn beforePresent, afterPresent, true\n\t}\n\tswitch operation.Kind {",
		}, gate("./internal/migration/explain", "TestMigrationExplainProspectiveAdapterUsesOperationLocalPresenceForRecreations")),
		mutation("MIGRATION_EXPLAIN_REVIEWED_SKIPS_FILE_CHECKSUMS", "render a reviewed entry without validating every referenced artifact", p8mutation.Patch{
			Path:   "go/internal/migration/explain/adapter.go",
			Before: "\tif !validateReviewedRisks(entry, plan) || !validateReviewedFiles(entry, files) {\n\t\treturn providerInput{}, unavailable()\n\t}",
			After:  "\tif !validateReviewedRisks(entry, plan) {\n\t\treturn providerInput{}, unavailable()\n\t}",
		}, gate("./internal/migration/explain", "TestMigrationExplainReviewedAdapterValidatesSealedEntryAndArtifactsBeforeRendering")),
		mutation("MIGRATION_EXPLAIN_ADAPTER_LABELS_UNSAFE_TYPE_SAFE", "label a current text-to-varchar narrowing as value preserving", p8mutation.Patch{
			Path:   "go/internal/migration/explain/adapter.go",
			Before: "\t\t\tformatUpgrade := before.version == 1 && before.canonical == 1 && after.version == 2 && after.canonical == 2\n",
			After:  "\t\t\tformatUpgrade := true\n",
		}, gate("./internal/migration/explain", "TestMigrationExplainProspectiveAdapterDoesNotCallUnsafeCurrentTypeChangePreserving")),
	}
}
