package migration

import "github.com/eleven-am/golem/go/internal/physical"

func withPlanSnapshotFacts(plan Plan, before, after physical.PhysicalSchema) Plan {
	plan.snapshotFacts = &PlanSnapshotFacts{
		before: clonePlanSnapshot(before),
		after:  clonePlanSnapshot(after),
	}
	return plan
}

// withHistoricalV3PlanSnapshotFacts keeps reviewed v3 validation facts on the
// exact retained normalization profile. In particular, v3 is also the current
// format at its publication boundary, so the generic installer above would
// otherwise route these immutable facts back through mutable current rules.
func withHistoricalV3PlanSnapshotFacts(plan Plan, before, after physical.PhysicalSchema) Plan {
	plan.snapshotFacts = &PlanSnapshotFacts{
		before: cloneHistoricalV3PlanSnapshot(before), after: cloneHistoricalV3PlanSnapshot(after),
	}
	return plan
}

func cloneHistoricalV3PlanSnapshot(snapshot physical.PhysicalSchema) physical.PhysicalSchema {
	cloned, err := physical.NormalizeHistoricalV3(snapshot)
	if err != nil {
		// The reviewed v3 installer is reached only after exact frozen
		// normalization. Keep an impossible corrupted fact fail-closed.
		return physical.PhysicalSchema{}
	}
	return cloned
}

func clonePlanSnapshot(snapshot physical.PhysicalSchema) physical.PhysicalSchema {
	var (
		cloned physical.PhysicalSchema
		err    error
	)
	if snapshot.Version == 3 && snapshot.CanonicalVersion == 3 {
		cloned, err = physical.NormalizeHistoricalV3(snapshot)
	} else if snapshot.Version == physical.SchemaFormatVersion && snapshot.CanonicalVersion == physical.CanonicalFormatVersion {
		cloned, err = physical.Normalize(snapshot)
	} else {
		cloned, err = physical.NormalizeHistorical(snapshot)
	}
	if err != nil {
		// Plan snapshot facts are installed only after successful normalization.
		// Returning the zero value here keeps an impossible corrupted in-memory
		// fact fail-closed at its consumer rather than exposing shared storage.
		return physical.PhysicalSchema{}
	}
	return cloned
}
