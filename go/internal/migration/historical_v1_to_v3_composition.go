package migration

// This file owns the closed reviewed physical-v1-to-v3 composition. It does
// not add a third planner: it derives the sole frozen v2 representation of the
// verified v1 head, then composes the retained v1-to-v2 and v2-to-v3 algebras
// into one migration entry and one final schema-version publication.

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

func diffHistoricalV1ToV3Composed(before, after physical.PhysicalSchema) (Plan, error) {
	if before.Version != 1 || before.CanonicalVersion != 1 || after.Version != 3 || after.CanonicalVersion != 3 {
		return Plan{}, fmt.Errorf("reviewed physical v1-to-v3 composition requires exact 1/1 -> 3/3 snapshots")
	}
	left, err := physical.NormalizeHistorical(before)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize historical v1 composition head: %w", err)
	}
	right, err := physical.NormalizeHistoricalV3(after)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize historical v3 composition target: %w", err)
	}
	if left.Provider.Provider != right.Provider.Provider {
		return Plan{}, fmt.Errorf("cannot compose providers %s and %s", left.Provider.Provider, right.Provider.Provider)
	}

	middle, err := projectHistoricalV1HeadToV2(left)
	if err != nil {
		return Plan{}, err
	}
	first, err := DiffPhysicalFormatUpgrade(left, middle)
	if err != nil {
		return Plan{}, fmt.Errorf("compose frozen physical v1-to-v2 leg: %w", err)
	}
	second, err := DiffOptimisticConcurrencyPhysicalUpgrade(middle, right)
	if err != nil {
		return Plan{}, fmt.Errorf("compose frozen physical v2-to-v3 leg: %w", err)
	}

	firstOperations, err := withoutSoleSchemaVersion(first.Operations)
	if err != nil {
		return Plan{}, fmt.Errorf("compose physical v1-to-v2 leg: %w", err)
	}
	secondOperations, err := withoutSoleSchemaVersion(second.Operations)
	if err != nil {
		return Plan{}, fmt.Errorf("compose physical v2-to-v3 leg: %w", err)
	}
	if err := rejectComposedOperationIDCollisions(firstOperations, secondOperations); err != nil {
		return Plan{}, err
	}
	firstLeaves := terminalOperationIDs(firstOperations)
	secondRoots := rootOperationIndexes(secondOperations)
	for _, index := range secondRoots {
		secondOperations[index].Dependencies = append(secondOperations[index].Dependencies, firstLeaves...)
		sort.Slice(secondOperations[index].Dependencies, func(i, j int) bool {
			return secondOperations[index].Dependencies[i] < secondOperations[index].Dependencies[j]
		})
	}

	beforeFingerprint, err := physical.HistoricalPhysicalFingerprint(left)
	if err != nil {
		return Plan{}, err
	}
	afterFingerprint, err := physical.HistoricalV3PhysicalFingerprint(right)
	if err != nil {
		return Plan{}, err
	}
	operations := append(append(make([]Operation, 0, len(firstOperations)+len(secondOperations)+1), firstOperations...), secondOperations...)
	record := Operation{
		Kind: RecordSchemaVersion, Stage: 100, ObjectID: "schema-version",
		Before: Digest(beforeFingerprint.String()), After: Digest(afterFingerprint.String()),
		Mode: Transactional, Risk: RiskSafe, LogicalPath: "schema",
	}
	record.ID = historicalV2StableOperationID(record.Kind, record.ObjectID, record.Before, record.After)
	for _, operation := range operations {
		record.Dependencies = append(record.Dependencies, operation.ID)
	}
	sort.Slice(record.Dependencies, func(i, j int) bool { return record.Dependencies[i] < record.Dependencies[j] })
	for _, operation := range operations {
		if operation.ID == record.ID {
			return Plan{}, fmt.Errorf("reviewed physical v1-to-v3 composition operation ID collision %s", record.ID)
		}
	}
	operations = append(operations, record)

	plan := Plan{
		Provider: right.Provider.Provider, Initial: first.Initial || composedInitial(middle, right),
		BeforeFingerprint: record.Before, AfterFingerprint: record.After, Operations: operations,
	}
	plan.Operations, err = Order(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.Phases, err = BuildPhases(plan.Operations, plan.BeforeFingerprint, plan.AfterFingerprint)
	if err != nil {
		return Plan{}, err
	}
	return withHistoricalV1ToV3PlanSnapshotFacts(plan, left, right), nil
}

// projectHistoricalV1HeadToV2 advances only retained physical representation
// facts discoverable from the v1 head itself. In particular, it never consults
// the v3 target: new tables and concurrency fields must remain changes owned by
// the frozen v2-to-v3 leg.
func projectHistoricalV1HeadToV2(head physical.PhysicalSchema) (physical.PhysicalSchema, error) {
	left, err := physical.NormalizeHistorical(head)
	if err != nil {
		return physical.PhysicalSchema{}, fmt.Errorf("normalize historical v1 projection source: %w", err)
	}
	projected := left
	projected.Version, projected.CanonicalVersion = 2, 2
	if projected.Provider.Provider == ir.PostgreSQL {
		for tableIndex := range projected.Tables {
			if err := projectHistoricalV1PostgreSQLTableToV2(&projected.Tables[tableIndex]); err != nil {
				return physical.PhysicalSchema{}, err
			}
		}
	}
	projected, err = physical.NormalizeHistoricalV2(projected)
	if err != nil {
		return physical.PhysicalSchema{}, fmt.Errorf("normalize projected historical v2 head: %w", err)
	}
	return projected, nil
}

func projectHistoricalV1PostgreSQLTableToV2(table *physical.PhysicalTable) error {
	checks := make(map[ir.CheckID]physical.PhysicalCheck, len(table.Checks))
	for _, check := range table.Checks {
		if _, duplicate := checks[check.ID]; duplicate {
			return fmt.Errorf("historical v1 table %s repeats check %s", table.ID, check.ID)
		}
		checks[check.ID] = check
	}
	remove := make(map[ir.CheckID]bool)
	converted := make(map[ir.FieldID]physical.StorageType)
	for columnIndex := range table.Columns {
		column := &table.Columns[columnIndex]
		checkID, checkName := physical.HistoricalV1MaxLengthCheckIdentity(table.ID, column.ID)
		check, exists := checks[checkID]
		if !exists {
			continue
		}
		length, err := historicalV1BoundedTextLength(*table, *column, check, checkName)
		if err != nil {
			return err
		}
		column.Storage = physical.StorageType{Kind: physical.StoragePostgreSQLVarchar, Length: length}
		converted[column.ID] = column.Storage
		remove[checkID] = true
	}
	retained := make([]physical.PhysicalCheck, 0, len(table.Checks)-len(remove))
	for _, check := range table.Checks {
		if !remove[check.ID] {
			retained = append(retained, check)
		}
	}
	table.Checks = retained
	for columnIndex := range table.Columns {
		column := &table.Columns[columnIndex]
		owner, convertedOwner := converted[column.ID]
		if column.Generated != nil {
			column.Generated.Expression = rewriteHistoricalV1ExpressionStorage(column.Generated.Expression, converted, owner, convertedOwner)
		}
		if column.Default.Expression != nil {
			expression := rewriteHistoricalV1ExpressionStorage(*column.Default.Expression, converted, owner, convertedOwner)
			column.Default.Expression = &expression
		}
	}
	for checkIndex := range table.Checks {
		table.Checks[checkIndex].Expression = rewriteHistoricalV1ExpressionStorage(table.Checks[checkIndex].Expression, converted, physical.StorageType{}, false)
	}
	for indexIndex := range table.Indexes {
		index := &table.Indexes[indexIndex]
		for keyIndex := range index.Keys {
			if index.Keys[keyIndex].Expression != nil {
				expression := rewriteHistoricalV1ExpressionStorage(*index.Keys[keyIndex].Expression, converted, physical.StorageType{}, false)
				index.Keys[keyIndex].Expression = &expression
			}
		}
		if index.Predicate != nil {
			expression := rewriteHistoricalV1ExpressionStorage(*index.Predicate, converted, physical.StorageType{}, false)
			index.Predicate = &expression
		}
	}
	return nil
}

func historicalV1BoundedTextLength(table physical.PhysicalTable, column physical.PhysicalColumn, check physical.PhysicalCheck, wantName physical.PhysicalName) (uint32, error) {
	if column.Storage != (physical.StorageType{Kind: physical.StoragePostgreSQLText}) || check.Name != wantName || len(check.RequiredCapabilities) != 0 {
		return 0, fmt.Errorf("historical v1 bounded-string check %s on field %s is not the frozen representation", check.ID, column.ID)
	}
	expression := check.Expression
	if expression.Kind != physical.ExpressionOperator || expression.Symbol == nil || expression.Symbol.Identity != "golem.schema.predicate.less-equal.v1" || expression.Symbol.Kind != ir.SchemaSymbolOperator || expression.Symbol.Version != 1 || expression.Symbol.Provider != ir.ProviderScopePortable || len(expression.Operands) != 2 {
		return 0, fmt.Errorf("historical v1 bounded-string check %s has an invalid predicate", check.ID)
	}
	length, literal := expression.Operands[0], expression.Operands[1]
	if length.Kind != physical.ExpressionFunction || length.Symbol == nil || length.Symbol.Identity != "golem.schema.function.length.v1" || length.Symbol.Kind != ir.SchemaSymbolFunction || length.Symbol.Version != 1 || length.Symbol.Provider != ir.ProviderScopePortable || len(length.Operands) != 1 || literal.Kind != physical.ExpressionLiteral || literal.Literal == nil || literal.Literal.Kind != ir.LiteralInteger {
		return 0, fmt.Errorf("historical v1 bounded-string check %s has an invalid length proof", check.ID)
	}
	reference := length.Operands[0]
	field := column.ID
	integer := physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
	boolean := physical.StorageType{Kind: physical.StoragePostgreSQLBoolean}
	if reference.Kind != physical.ExpressionColumn || reference.Column == nil || *reference.Column != field || reference.Type != column.Storage || reference.Nullable != column.Nullable || len(reference.Operands) != 0 || length.Type != integer || length.Nullable != column.Nullable || literal.Type != integer || literal.Nullable || len(literal.Operands) != 0 || expression.Type != boolean || expression.Nullable != column.Nullable || expression.Column != nil || expression.Literal != nil {
		return 0, fmt.Errorf("historical v1 bounded-string check %s does not exactly bind field %s", check.ID, field)
	}
	parsed, err := strconv.ParseUint(literal.Literal.Canonical, 10, 32)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != literal.Literal.Canonical {
		return 0, fmt.Errorf("historical v1 bounded-string check %s has an invalid maximum length", check.ID)
	}
	return uint32(parsed), nil
}

func rewriteHistoricalV1ExpressionStorage(value physical.Expression, converted map[ir.FieldID]physical.StorageType, owner physical.StorageType, owningResult bool) physical.Expression {
	result := value
	result.Operands = make([]physical.Expression, len(value.Operands))
	for index := range value.Operands {
		result.Operands[index] = rewriteHistoricalV1ExpressionStorage(value.Operands[index], converted, physical.StorageType{}, false)
	}
	if result.Column != nil {
		if storage, exists := converted[*result.Column]; exists {
			result.Type = storage
		}
	} else if owningResult && result.Type.Kind == physical.StoragePostgreSQLText {
		result.Type = owner
	}
	return result
}

func withoutSoleSchemaVersion(operations []Operation) ([]Operation, error) {
	result := make([]Operation, 0, len(operations)-1)
	found := 0
	for _, operation := range operations {
		if operation.Kind == RecordSchemaVersion {
			found++
			continue
		}
		result = append(result, operation)
	}
	if found != 1 {
		return nil, fmt.Errorf("frozen leg has %d schema-version operations; want exactly one", found)
	}
	return result, nil
}

func rejectComposedOperationIDCollisions(first, second []Operation) error {
	seen := make(map[OperationID]bool, len(first)+len(second))
	for _, operation := range append(append([]Operation(nil), first...), second...) {
		if seen[operation.ID] {
			return fmt.Errorf("reviewed physical v1-to-v3 composition operation ID collision %s", operation.ID)
		}
		seen[operation.ID] = true
	}
	return nil
}

func terminalOperationIDs(operations []Operation) []OperationID {
	referenced := make(map[OperationID]bool, len(operations))
	for _, operation := range operations {
		for _, dependency := range operation.Dependencies {
			referenced[dependency] = true
		}
	}
	var result []OperationID
	for _, operation := range operations {
		if !referenced[operation.ID] {
			result = append(result, operation.ID)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func rootOperationIndexes(operations []Operation) []int {
	var result []int
	for index := range operations {
		if len(operations[index].Dependencies) == 0 {
			result = append(result, index)
		}
	}
	return result
}

func composedInitial(before, after physical.PhysicalSchema) bool {
	return historicalV2EmptySystemSchema(before.System) && len(before.Tables) == 0 && len(before.Extensions) == 0 && len(before.Unmanaged) == 0 && len(after.System.Objects) != 0
}

func withHistoricalV1ToV3PlanSnapshotFacts(plan Plan, before, after physical.PhysicalSchema) Plan {
	left, leftErr := physical.NormalizeHistorical(before)
	right, rightErr := physical.NormalizeHistoricalV3(after)
	if leftErr != nil || rightErr != nil {
		plan.snapshotFacts = &PlanSnapshotFacts{}
		return plan
	}
	plan.snapshotFacts = &PlanSnapshotFacts{before: left, after: right}
	return plan
}
