package migration

import (
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

// diffOptimisticConcurrencyV2ToV3Tagged is the retained candidate for the sole
// v2-to-v3 physical transition. All ordinary schema work is replayed by the
// frozen v2 algebra; the v3 delta is closed here. Physical v3 is not published
// yet, so the release gate forbids advancing current to v4 until v3 receives
// its own frozen normalization/canonical source.
func diffOptimisticConcurrencyV2ToV3Tagged(before, after physical.PhysicalSchema) (Plan, error) {
	left, err := physical.NormalizeHistoricalV2(before)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize historical v2 before: %w", err)
	}
	right, err := physical.NormalizeHistorical(after)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize physical v3 after: %w", err)
	}
	if left.Provider.Provider != right.Provider.Provider {
		return Plan{}, fmt.Errorf("cannot diff providers %s and %s", left.Provider.Provider, right.Provider.Provider)
	}

	beforeTables := make(map[ir.ModelID]physical.PhysicalTable, len(left.Tables))
	for _, table := range left.Tables {
		beforeTables[table.ID] = table
	}
	core, err := physical.NormalizeHistorical(right)
	if err != nil {
		return Plan{}, err
	}
	core.Version, core.CanonicalVersion = 2, 2
	for tableIndex := range core.Tables {
		table := &core.Tables[tableIndex]
		field := table.OptimisticConcurrency
		table.OptimisticConcurrency = nil
		if field == nil {
			continue
		}
		beforeTable, existed := beforeTables[table.ID]
		if !existed {
			continue // CreateTable owns every field; no existing rows need initialization.
		}
		for _, column := range beforeTable.Columns {
			if column.ID == *field {
				return Plan{}, fmt.Errorf("optimistic concurrency cannot adopt existing field %s on model %s", column.ID, table.ID)
			}
		}
		columns := table.Columns[:0:0]
		for _, column := range table.Columns {
			if column.ID != *field {
				columns = append(columns, column)
			}
		}
		table.Columns = columns
	}
	core, err = physical.NormalizeHistoricalV2(core)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize frozen v2 transition core: %w", err)
	}
	base, err := diffHistoricalV2Tagged(left, core)
	if err != nil {
		return Plan{}, err
	}
	operations := make([]Operation, 0, len(base.Operations)+8)
	for _, operation := range base.Operations {
		if operation.Kind != RecordSchemaVersion {
			operations = append(operations, operation)
		}
	}
	builder := historicalV2DiffBuilder{before: left, after: right, beforeCanonicalVersion: 2, afterCanonicalVersion: 2, operations: operations}
	for _, table := range right.Tables {
		if table.OptimisticConcurrency == nil {
			continue
		}
		if _, existed := beforeTables[table.ID]; !existed {
			continue
		}
		field := *table.OptimisticConcurrency
		column, exists := transitionColumn(table, field)
		if !exists {
			return Plan{}, fmt.Errorf("optimistic concurrency field %s is absent from model %s", field, table.ID)
		}
		id := string(field)
		if err := builder.add(AddColumn, 30, id, nil, column, RiskSafe); err != nil {
			return Plan{}, err
		}
		if err := builder.add(InitializeConcurrencyColumn, 31, id, nil, column, RiskRewrite); err != nil {
			return Plan{}, err
		}
		if err := builder.add(ValidateConstraint, 32, id, nil, column, RiskSafe); err != nil {
			return Plan{}, err
		}
		if err := builder.add(AlterColumnNullability, 33, id, true, false, RiskSafe); err != nil {
			return Plan{}, err
		}
	}
	builder.dependencies()
	transitionConcurrencyDependencies(builder.operations)
	beforeFingerprint, err := physical.HistoricalPhysicalFingerprint(left)
	if err != nil {
		return Plan{}, err
	}
	afterFingerprint, err := physical.HistoricalPhysicalFingerprint(right)
	if err != nil {
		return Plan{}, err
	}
	record := Operation{Kind: RecordSchemaVersion, Stage: 100, ObjectID: "schema-version", Before: Digest(beforeFingerprint.String()), After: Digest(afterFingerprint.String()), Mode: Transactional, Risk: RiskSafe, LogicalPath: "schema"}
	record.ID = historicalV2StableOperationID(record.Kind, record.ObjectID, record.Before, record.After)
	for _, operation := range builder.operations {
		record.Dependencies = append(record.Dependencies, operation.ID)
	}
	sort.Slice(record.Dependencies, func(i, j int) bool { return record.Dependencies[i] < record.Dependencies[j] })
	builder.operations = append(builder.operations, record)
	plan := Plan{Provider: right.Provider.Provider, BeforeFingerprint: record.Before, AfterFingerprint: record.After, Operations: builder.operations}
	plan.Operations, err = Order(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.Phases, err = BuildPhases(plan.Operations, plan.BeforeFingerprint, plan.AfterFingerprint)
	if err != nil {
		return Plan{}, err
	}
	return withPlanSnapshotFacts(plan, left, right), nil
}

func transitionColumn(table physical.PhysicalTable, field ir.FieldID) (physical.PhysicalColumn, bool) {
	for _, column := range table.Columns {
		if column.ID == field {
			return column, true
		}
	}
	return physical.PhysicalColumn{}, false
}

func transitionConcurrencyDependencies(operations []Operation) {
	lookup := make(map[string]OperationID, len(operations))
	for _, operation := range operations {
		lookup[string(operation.Kind)+"\x00"+operation.ObjectID] = operation.ID
	}
	add := func(operation *Operation, kind OperationKind) {
		dependency := lookup[string(kind)+"\x00"+operation.ObjectID]
		if dependency == "" || dependency == operation.ID {
			return
		}
		for _, current := range operation.Dependencies {
			if current == dependency {
				return
			}
		}
		operation.Dependencies = append(operation.Dependencies, dependency)
		sort.Slice(operation.Dependencies, func(i, j int) bool { return operation.Dependencies[i] < operation.Dependencies[j] })
	}
	for index := range operations {
		switch operations[index].Kind {
		case InitializeConcurrencyColumn:
			add(&operations[index], AddColumn)
		case ValidateConstraint:
			add(&operations[index], InitializeConcurrencyColumn)
		case AlterColumnNullability:
			add(&operations[index], ValidateConstraint)
		}
	}
}
