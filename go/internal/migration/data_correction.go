package migration

import (
	"fmt"
	"reflect"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
	semanticstorage "github.com/eleven-am/golem/go/internal/semantic/storage"
)

// DataCorrectionPlan authorizes a separate offline data-only history entry.
// The schema stays identical; only one existing non-key stored field may be
// corrected. Its checksummed companion is sealed separately by the workflow.
func DataCorrectionPlan(snapshot physical.PhysicalSchema, field ir.FieldID) (Plan, error) {
	snapshot, err := physical.NormalizeHistorical(snapshot)
	if err != nil {
		return Plan{}, err
	}
	if snapshot.Version != physical.DataIncarnationFormatVersion || !physical.HasDataIncarnation(snapshot.System) {
		return Plan{}, fmt.Errorf("data correction requires reviewed durable data identity storage")
	}
	model, found := BackfillOwner(snapshot, field)
	if !found {
		return Plan{}, fmt.Errorf("data correction field is not an existing stored field")
	}
	var target physical.PhysicalColumn
	for _, table := range snapshot.Tables {
		if table.ID != model {
			continue
		}
		for _, column := range table.Columns {
			if column.ID == field {
				target = column
			}
		}
		if target.Generated != nil || target.Name == physical.RowVersionColumn || table.OptimisticConcurrency != nil && *table.OptimisticConcurrency == field {
			return Plan{}, fmt.Errorf("data correction cannot target generated or version fields")
		}
		affected := physical.GeneratedFieldClosure(table, field)
		var keys []ir.FieldID
		if table.PrimaryKey == nil || len(table.PrimaryKey.Columns) == 0 {
			return Plan{}, fmt.Errorf("data correction requires a stable primary identity")
		}
		keys = append(keys, table.PrimaryKey.Columns...)
		for _, key := range table.Uniques {
			keys = append(keys, key.Columns...)
		}
		for _, index := range table.Indexes {
			if index.Unique {
				if index.Predicate != nil && physical.ExpressionReferencesAny(*index.Predicate, affected) {
					return Plan{}, fmt.Errorf("data correction cannot change unique-index membership")
				}
				for _, key := range index.Keys {
					if key.Expression != nil && physical.ExpressionReferencesAny(*key.Expression, affected) {
						return Plan{}, fmt.Errorf("data correction cannot change an expression-indexed unique key")
					}
					if key.Column != nil {
						keys = append(keys, *key.Column)
					}
				}
			}
		}
		for _, foreign := range table.ForeignKeys {
			keys = append(keys, foreign.Columns...)
		}
		for _, key := range keys {
			if affected[key] {
				return Plan{}, fmt.Errorf("data correction cannot target identity, unique or foreign-key fields")
			}
		}
		for _, extension := range snapshot.Extensions {
			if extension.Kind != semanticcontract.IndexKind {
				continue
			}
			index, err := semanticstorage.Decode(extension)
			if err != nil {
				return Plan{}, err
			}
			if index.ModelID != model {
				continue
			}
			for _, input := range index.Fields {
				if affected[input] {
					return Plan{}, fmt.Errorf("data correction affects a semantic input and requires explicit semantic invalidation")
				}
			}
		}
	}
	fingerprint, err := physical.HistoricalPhysicalFingerprint(snapshot)
	if err != nil {
		return Plan{}, err
	}
	fragment, err := fragmentVersion(target, snapshot.CanonicalVersion)
	if err != nil {
		return Plan{}, err
	}
	operation := Operation{Kind: CorrectDataColumn, Stage: 31, ObjectID: string(field), Before: fragment, After: fragment, Mode: Transactional, Risk: RiskManual, LogicalPath: string(field)}
	operation.ID = stableOperationID(operation.Kind, operation.ObjectID, fragment, fragment)
	digest := Digest(fingerprint.String())
	plan := Plan{Provider: snapshot.Provider.Provider, BeforeFingerprint: digest, AfterFingerprint: digest,
		Operations: []Operation{operation}, Phases: []Phase{{Ordinal: 0, Mode: Transactional, Operations: []OperationID{operation.ID}, BeforeFingerprint: digest, AfterFingerprint: digest}}}
	return withPlanSnapshotFacts(plan, snapshot, snapshot), nil
}

// DataCorrectionPostcondition binds the provider-owned correction invariants
// to the exact stable target. It is never executable author SQL.
func DataCorrectionPostcondition(model ir.ModelID, field ir.FieldID) Digest {
	return Checksum([]byte("golem.migration.postcondition.data-correction.v1\x00" + string(model) + "\x00" + string(field)))
}

func IsDataCorrection(entry ManifestEntry) bool {
	for _, operation := range entry.Operations {
		if operation.Kind == CorrectDataColumn {
			return true
		}
	}
	return false
}

// ReviewedEntryPlan is the sole dispatcher for reviewed schema and data-only
// operation graphs. Data corrections cannot borrow authority from a schema
// change or be smuggled into an otherwise ordinary migration.
func ReviewedEntryPlan(entry ManifestEntry) (Plan, error) {
	var plan Plan
	var err error
	if IsDataCorrection(entry) {
		if len(entry.Operations) != 1 || entry.BeforeModel != entry.AfterModel || !reflect.DeepEqual(entry.BeforeSnapshot, entry.AfterSnapshot) {
			return Plan{}, fmt.Errorf("data correction requires identical model and physical snapshots and exactly one operation")
		}
		plan, err = DataCorrectionPlan(entry.BeforeSnapshot, ir.FieldID(entry.Operations[0].ObjectID))
	} else {
		plan, err = DiffReviewed(entry.BeforeSnapshot, entry.AfterSnapshot)
	}
	if err != nil {
		return Plan{}, err
	}
	if plan.Provider != entry.AfterSnapshot.Provider.Provider || plan.BeforeFingerprint != entry.BeforePhysical || plan.AfterFingerprint != entry.AfterPhysical ||
		!reflect.DeepEqual(plan.Operations, entry.Operations) || !reflect.DeepEqual(plan.Phases, entry.Phases) {
		return Plan{}, fmt.Errorf("reviewed migration snapshot lacks the exact sealed typed plan")
	}
	return plan, nil
}

func matchesExactDataCorrectionPlan(plan Plan) bool {
	if plan.snapshotFacts == nil || len(plan.Operations) != 1 || plan.Operations[0].Kind != CorrectDataColumn || !reflect.DeepEqual(plan.snapshotFacts.before, plan.snapshotFacts.after) {
		return false
	}
	expected, err := DataCorrectionPlan(plan.snapshotFacts.before, ir.FieldID(plan.Operations[0].ObjectID))
	return err == nil && plan.Provider == expected.Provider && !plan.Initial && plan.BeforeFingerprint == expected.BeforeFingerprint && plan.AfterFingerprint == expected.AfterFingerprint && reflect.DeepEqual(plan.Operations, expected.Operations) && reflect.DeepEqual(plan.Phases, expected.Phases)
}
