package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

// Diff compares validated normalized physical schemas by stable IDs. It never
// infers a rename from spelling similarity.
func Diff(before, after physical.PhysicalSchema) (Plan, error) {
	return diffSchemas(before, after, false, false)
}

// DiffHistorical reproduces the exact v1 operation graph for immutable
// migration snapshots. It is a verification boundary, never an authoring path.
func DiffHistorical(before, after physical.PhysicalSchema) (Plan, error) {
	if before.Version != 1 || before.CanonicalVersion != 1 || after.Version != 1 || after.CanonicalVersion != 1 {
		return Plan{}, fmt.Errorf("historical diff requires exact v1/v1 snapshots")
	}
	left, err := physical.NormalizeHistorical(before)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize historical before: %w", err)
	}
	right, err := physical.NormalizeHistorical(after)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize historical after: %w", err)
	}
	plan, err := diffHistoricalV1Tagged(left, right)
	if err != nil {
		return Plan{}, err
	}
	return withPlanSnapshotFacts(plan, left, right), nil
}

// DiffHistoricalV2 reproduces only the frozen v2 operation algebra. It never
// routes through the mutable current planner.
func DiffHistoricalV2(before, after physical.PhysicalSchema) (Plan, error) {
	if before.Version != 2 || before.CanonicalVersion != 2 || after.Version != 2 || after.CanonicalVersion != 2 {
		return Plan{}, fmt.Errorf("historical v2 diff requires exact v2/v2 snapshots")
	}
	left, err := physical.NormalizeHistoricalV2(before)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize historical v2 before: %w", err)
	}
	right, err := physical.NormalizeHistoricalV2(after)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize historical v2 after: %w", err)
	}
	plan, err := diffHistoricalV2Tagged(left, right)
	if err != nil {
		return Plan{}, err
	}
	return withPlanSnapshotFacts(plan, left, right), nil
}

// DiffHistoricalV3 reproduces only the frozen v3 operation algebra. It never
// routes through the mutable current planner, even while v3 is current.
func DiffHistoricalV3(before, after physical.PhysicalSchema) (Plan, error) {
	if before.Version != 3 || before.CanonicalVersion != 3 || after.Version != 3 || after.CanonicalVersion != 3 {
		return Plan{}, fmt.Errorf("historical v3 diff requires exact v3/v3 snapshots")
	}
	left, err := physical.NormalizeHistoricalV3(before)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize historical v3 before: %w", err)
	}
	right, err := physical.NormalizeHistoricalV3(after)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize historical v3 after: %w", err)
	}
	plan, err := diffHistoricalV3Tagged(left, right)
	if err != nil {
		return Plan{}, err
	}
	return withHistoricalV3PlanSnapshotFacts(plan, left, right), nil
}

// DiffPhysicalFormatUpgrade plans the frozen v1-to-v2 transition. It never
// targets mutable current format dynamically.
func DiffPhysicalFormatUpgrade(before, after physical.PhysicalSchema) (Plan, error) {
	if before.Version != 1 || before.CanonicalVersion != 1 || after.Version != 2 || after.CanonicalVersion != 2 {
		return Plan{}, fmt.Errorf("unsupported physical format upgrade %d/%d -> %d/%d", before.Version, before.CanonicalVersion, after.Version, after.CanonicalVersion)
	}
	return diffHistoricalV1ToV2Tagged(before, after)
}

// DiffOptimisticConcurrencyPhysicalUpgrade plans the sole frozen v2-to-v3
// transition. Current-only optimistic-concurrency metadata is validated before
// any operation is emitted.
func DiffOptimisticConcurrencyPhysicalUpgrade(before, after physical.PhysicalSchema) (Plan, error) {
	if before.Version != 2 || before.CanonicalVersion != 2 || after.Version != 3 || after.CanonicalVersion != 3 {
		return Plan{}, fmt.Errorf("unsupported optimistic-concurrency physical upgrade %d/%d -> %d/%d", before.Version, before.CanonicalVersion, after.Version, after.CanonicalVersion)
	}
	return diffOptimisticConcurrencyV2ToV3Tagged(before, after)
}

// DiffReviewed selects the exact retained canonical algebra for an immutable
// reviewed entry. Mixed physical versions are always refused.
func DiffReviewed(before, after physical.PhysicalSchema) (Plan, error) {
	switch {
	case before.Version == physical.SchemaFormatVersion && before.CanonicalVersion == physical.CanonicalFormatVersion && after.Version == physical.SchemaFormatVersion && after.CanonicalVersion == physical.CanonicalFormatVersion:
		if physical.SchemaFormatVersion != 3 || physical.CanonicalFormatVersion != 3 {
			return Plan{}, fmt.Errorf("current physical format %d/%d has no reviewed planner dispatch", physical.SchemaFormatVersion, physical.CanonicalFormatVersion)
		}
		return DiffHistoricalV3(before, after)
	case before.Version == 1 && before.CanonicalVersion == 1 && after.Version == 1 && after.CanonicalVersion == 1:
		return DiffHistorical(before, after)
	case before.Version == 1 && before.CanonicalVersion == 1 && after.Version == 2 && after.CanonicalVersion == 2:
		return DiffPhysicalFormatUpgrade(before, after)
	case before.Version == 1 && before.CanonicalVersion == 1 && after.Version == 3 && after.CanonicalVersion == 3:
		return diffHistoricalV1ToV3Composed(before, after)
	case before.Version == 2 && before.CanonicalVersion == 2 && after.Version == 2 && after.CanonicalVersion == 2:
		return DiffHistoricalV2(before, after)
	case before.Version == 2 && before.CanonicalVersion == 2 && after.Version == 3 && after.CanonicalVersion == 3:
		return DiffOptimisticConcurrencyPhysicalUpgrade(before, after)
	case before.Version == 3 && before.CanonicalVersion == 3 && after.Version == 3 && after.CanonicalVersion == 3:
		return DiffHistoricalV3(before, after)
	default:
		return Plan{}, fmt.Errorf("unsupported reviewed physical version pair %d/%d -> %d/%d", before.Version, before.CanonicalVersion, after.Version, after.CanonicalVersion)
	}
}

func diffSchemas(before, after physical.PhysicalSchema, historicalBefore, historicalAfter bool) (Plan, error) {
	normalizeBefore := physical.Normalize
	if historicalBefore {
		normalizeBefore = physical.NormalizeHistorical
	}
	normalizeAfter := physical.Normalize
	if historicalAfter {
		normalizeAfter = physical.NormalizeHistorical
	}
	left, err := normalizeBefore(before)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize before: %w", err)
	}
	right, err := normalizeAfter(after)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize after: %w", err)
	}
	if left.Provider.Provider != right.Provider.Provider {
		return Plan{}, fmt.Errorf("cannot diff providers %s and %s", left.Provider.Provider, right.Provider.Provider)
	}
	beforeFingerprint := physical.PhysicalFingerprint
	if historicalBefore {
		beforeFingerprint = physical.HistoricalPhysicalFingerprint
	}
	afterFingerprint := physical.PhysicalFingerprint
	if historicalAfter {
		afterFingerprint = physical.HistoricalPhysicalFingerprint
	}
	leftFP, err := beforeFingerprint(left)
	if err != nil {
		return Plan{}, err
	}
	rightFP, err := afterFingerprint(right)
	if err != nil {
		return Plan{}, err
	}
	if reflect.DeepEqual(left, right) {
		return withPlanSnapshotFacts(Plan{
			Provider: right.Provider.Provider, BeforeFingerprint: Digest(leftFP.String()), AfterFingerprint: Digest(rightFP.String()),
		}, left, right), nil
	}
	builder := diffBuilder{before: left, after: right, beforeCanonicalVersion: left.CanonicalVersion, afterCanonicalVersion: right.CanonicalVersion, formatUpgrade: left.Version == 1 && right.Version == 2, historicalReplay: left.Version == right.Version && left.Version < physical.SchemaFormatVersion}
	if err := builder.identifyGeneratedWideningRecreation(); err != nil {
		return Plan{}, err
	}
	beforeSystem, err := physical.SystemFingerprint(left.Provider, left.System)
	if historicalBefore {
		beforeSystem, err = physical.HistoricalSystemFingerprint(left)
	}
	if err != nil {
		return Plan{}, err
	}
	afterSystem, err := physical.SystemFingerprint(right.Provider, right.System)
	if historicalAfter {
		afterSystem, err = physical.HistoricalSystemFingerprint(right)
	}
	if err != nil {
		return Plan{}, err
	}
	initial := false
	if beforeSystem != afterSystem {
		if reflect.DeepEqual(left.System, right.System) {
			// Provider runtime/capability transitions alter the physical and
			// system fingerprints without changing a database system object. The
			// reviewed before/after snapshots and RecordSchemaVersion operation
			// own this metadata-only transition.
		} else if emptySystemSchema(left.System) && len(left.Tables) == 0 && len(left.Extensions) == 0 && len(left.Unmanaged) == 0 && len(right.System.Objects) != 0 {
			initial = true
		} else {
			additions, upgradeErr := systemObjectAdditions(left.System, right.System)
			if upgradeErr != nil {
				return Plan{}, upgradeErr
			}
			for _, object := range additions {
				if err := builder.add(AddSystemObject, 15, string(object.ID), nil, object, RiskSafe); err != nil {
					return Plan{}, err
				}
			}
		}
	}
	if initial && right.Provider.Provider == ir.PostgreSQL {
		if err := builder.add(CreateNamespace, 10, string(right.Namespace.Name), nil, right.Namespace, RiskSafe); err != nil {
			return Plan{}, err
		}
	}
	if initial {
		if err := builder.add(BootstrapSystemSchema, 15, "system-schema", left.System, right.System, RiskSafe); err != nil {
			return Plan{}, err
		}
	}
	if err := builder.tables(); err != nil {
		return Plan{}, err
	}
	if err := builder.recreateDestructiveDependents(); err != nil {
		return Plan{}, err
	}
	if err := builder.extensions(); err != nil {
		return Plan{}, err
	}
	recordBefore, recordAfter := Digest(leftFP.String()), Digest(rightFP.String())
	record := Operation{Kind: RecordSchemaVersion, Stage: 100, ObjectID: "schema-version", Before: recordBefore, After: recordAfter, Mode: Transactional, Risk: RiskSafe, LogicalPath: "schema"}
	record.ID = stableOperationID(record.Kind, record.ObjectID, record.Before, record.After)
	builder.operations = append(builder.operations, record)
	builder.dependencies()
	plan := Plan{Provider: right.Provider.Provider, Initial: initial, BeforeFingerprint: Digest(leftFP.String()), AfterFingerprint: Digest(rightFP.String()), Operations: builder.operations}
	ordered, err := Order(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.Operations = ordered
	plan.Phases, err = BuildPhases(ordered, plan.BeforeFingerprint, plan.AfterFingerprint)
	if err != nil {
		return Plan{}, err
	}
	return withPlanSnapshotFacts(plan, left, right), nil
}

func (b *diffBuilder) extensions() error {
	before := make(map[ir.ExtensionID]physical.Extension, len(b.before.Extensions))
	after := make(map[ir.ExtensionID]physical.Extension, len(b.after.Extensions))
	for _, extension := range b.before.Extensions {
		before[extension.ID] = extension
	}
	for _, extension := range b.after.Extensions {
		after[extension.ID] = extension
	}
	ids := make([]ir.ExtensionID, 0, len(before)+len(after))
	seen := map[ir.ExtensionID]bool{}
	for id := range before {
		seen[id] = true
		ids = append(ids, id)
	}
	for id := range after {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		left, had := before[id]
		right, has := after[id]
		switch {
		case !had:
			if err := b.add(CreateProviderExtension, 45, string(id), nil, right, RiskSafe); err != nil {
				return err
			}
		case !has:
			if err := b.add(DropProviderExtension, 75, string(id), left, nil, RiskDataLoss); err != nil {
				return err
			}
		case !reflect.DeepEqual(left, right):
			if left.Kind != semanticcontract.IndexKind || right.Kind != semanticcontract.IndexKind || left.Version != semanticcontract.Version || right.Version != semanticcontract.Version || left.Owner != right.Owner || left.Provider != right.Provider {
				return fmt.Errorf("provider extension %s cannot change in place", id)
			}
			// Semantic shadow state is derived entirely from the unchanged owner
			// rows. A reviewed projection or dimension change therefore rebuilds
			// the same stable extension identity by dropping its old state/vector
			// tables and recreating the new physical contract. It is a rewrite, not
			// owner-row data loss; runtime refresh repopulates the empty shadow
			// storage from the source table.
			if err := b.add(DropProviderExtension, 44, string(id), left, nil, RiskRewrite); err != nil {
				return err
			}
			if err := b.add(CreateProviderExtension, 45, string(id), nil, right, RiskRewrite); err != nil {
				return err
			}
		}
	}
	return nil
}

func systemObjectAdditions(before, after physical.SystemSchema) ([]physical.SystemObject, error) {
	if before.Version != after.Version || before.Namespace != after.Namespace {
		return nil, fmt.Errorf("system schema changes may only add a registered system object in the existing namespace")
	}
	old := make(map[ir.ObjectID]physical.SystemObject, len(before.Objects))
	for _, object := range before.Objects {
		old[object.ID] = object
	}
	var additions []physical.SystemObject
	for _, object := range after.Objects {
		if previous, exists := old[object.ID]; exists {
			if !reflect.DeepEqual(previous, object) {
				return nil, fmt.Errorf("system object %s cannot be changed in place", object.ID)
			}
			delete(old, object.ID)
			continue
		}
		additions = append(additions, object)
	}
	if len(old) != 0 {
		return nil, fmt.Errorf("system objects cannot be removed")
	}
	if len(additions) != 1 || !registeredAdditiveSystemObject(additions[0]) {
		return nil, fmt.Errorf("system schema change is not a registered additive system object")
	}
	return additions, nil
}

func registeredAdditiveSystemObject(object physical.SystemObject) bool {
	return physical.IsOutboxSystemObjectV1(object) || physical.IsOutboxDeliverySystemObjectV1(object) || physical.IsUpsertGuardSystemObjectV1(object)
}

func emptySystemSchema(system physical.SystemSchema) bool {
	return system.Version == 0 && system.Namespace.Name == "" && len(system.Objects) == 0
}

type diffBuilder struct {
	before, after          physical.PhysicalSchema
	operations             []Operation
	beforeCanonicalVersion uint32
	afterCanonicalVersion  uint32
	formatUpgrade          bool
	historicalReplay       bool
	detachedGenerated      map[ir.FieldID]ir.ModelID
	generatedDrops         map[ir.FieldID]ir.ModelID
	generatedAdds          map[ir.FieldID]ir.ModelID
	generatedInputs        map[ir.FieldID][]ir.FieldID
}

// identifyGeneratedWideningRecreation closes PostgreSQL's generated
// column dependency rule at the provider-neutral plan boundary. PostgreSQL
// cannot alter a source column's type while a stored generated column depends
// on it, and the minimum supported server cannot restore a dropped expression
// in place. The reviewed DAG therefore detaches and recreates exact derived
// columns; their source data remains authoritative.
func (b *diffBuilder) identifyGeneratedWideningRecreation() error {
	if b.historicalReplay || b.after.Provider.Provider != ir.PostgreSQL {
		return nil
	}
	b.detachedGenerated = map[ir.FieldID]ir.ModelID{}
	b.generatedDrops = map[ir.FieldID]ir.ModelID{}
	b.generatedAdds = map[ir.FieldID]ir.ModelID{}
	b.generatedInputs = map[ir.FieldID][]ir.FieldID{}
	afterTables := make(map[ir.ModelID]physical.PhysicalTable, len(b.after.Tables))
	for _, table := range b.after.Tables {
		afterTables[table.ID] = table
	}
	for _, beforeTable := range b.before.Tables {
		afterTable, exists := afterTables[beforeTable.ID]
		if !exists {
			continue
		}
		beforeColumns := make(map[ir.FieldID]physical.PhysicalColumn, len(beforeTable.Columns))
		afterColumns := make(map[ir.FieldID]physical.PhysicalColumn, len(afterTable.Columns))
		changing := map[ir.FieldID]bool{}
		for _, column := range beforeTable.Columns {
			beforeColumns[column.ID] = column
		}
		for _, column := range afterTable.Columns {
			afterColumns[column.ID] = column
			before, had := beforeColumns[column.ID]
			if had && !reflect.DeepEqual(before.Storage, column.Storage) {
				legacy := b.formatUpgrade && before.Storage.Kind == physical.StoragePostgreSQLText && column.Storage.Kind == physical.StoragePostgreSQLVarchar
				if physicalWideningRepresentation(before.Storage, column.Storage, legacy) {
					changing[column.ID] = true
				}
			}
		}
		for field, beforeColumn := range beforeColumns {
			afterColumn, retained := afterColumns[field]
			if beforeColumn.Generated == nil || retained && afterColumn.Generated != nil {
				continue
			}
			inputs := uniqueFields(expressionFields(beforeColumn.Generated.Expression))
			if fieldsIntersect(inputs, changing) {
				b.generatedDrops[field] = beforeTable.ID
				b.generatedInputs[field] = inputs
			}
		}
		for field, afterColumn := range afterColumns {
			beforeColumn, retained := beforeColumns[field]
			if afterColumn.Generated == nil {
				continue
			}
			inputs := uniqueFields(expressionFields(afterColumn.Generated.Expression))
			if !fieldsIntersect(inputs, changing) {
				continue
			}
			if !retained || beforeColumn.Generated == nil {
				b.generatedAdds[field] = beforeTable.ID
				b.generatedInputs[field] = inputs
				continue
			}
			if beforeColumn.Generated.Kind != physical.GeneratedStored || afterColumn.Generated.Kind != physical.GeneratedStored || !sameGeneratedColumnAcrossWidening(beforeColumn, afterColumn, b.formatUpgrade) {
				return fmt.Errorf("PostgreSQL widening generated field %s cannot be recreated exactly", field)
			}
			// A bounded input does not prove that an arbitrary generated
			// expression has a bounded output. The released v1 representation
			// owned an independent registered max-length CHECK for every bounded
			// field, including generated fields; require that exact proof before
			// translating the generated output from text to varchar.
			if b.formatUpgrade && beforeColumn.Storage.Kind == physical.StoragePostgreSQLText && afterColumn.Storage.Kind == physical.StoragePostgreSQLVarchar && !legacyVarcharRepresentation(beforeTable, afterTable, field, beforeColumn, afterColumn) {
				return fmt.Errorf("physical v1->v2 generated field %s lacks the exact legacy bounded-string representation", field)
			}
			b.detachedGenerated[field] = beforeTable.ID
			b.generatedDrops[field] = beforeTable.ID
			b.generatedAdds[field] = beforeTable.ID
			b.generatedInputs[field] = inputs
		}
	}
	return nil
}

func fieldsIntersect(fields []ir.FieldID, selected map[ir.FieldID]bool) bool {
	for _, field := range fields {
		if selected[field] {
			return true
		}
	}
	return false
}

func uniqueFields(values []ir.FieldID) []ir.FieldID {
	seen := map[ir.FieldID]bool{}
	result := make([]ir.FieldID, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func sameGeneratedColumnAcrossWidening(before, after physical.PhysicalColumn, formatUpgrade bool) bool {
	left, right := before, after
	// Stable FieldID owns identity. The reviewed detach/add envelope can apply a
	// simultaneous physical rename without a separate RenameColumn operation.
	left.Name, right.Name = "", ""
	left.Storage, right.Storage = physical.StorageType{}, physical.StorageType{}
	leftDefault, rightDefault := left.Default.Expression, right.Default.Expression
	left.Default.Expression, right.Default.Expression = nil, nil
	leftGenerated, rightGenerated := left.Generated, right.Generated
	left.Generated, right.Generated = nil, nil
	if !reflect.DeepEqual(left, right) || !sameWideningExpression(leftDefault, rightDefault, formatUpgrade) || leftGenerated == nil || rightGenerated == nil || leftGenerated.Kind != rightGenerated.Kind {
		return false
	}
	return physicalWideningRepresentation(before.Storage, after.Storage, formatUpgrade) && sameWideningExpression(&leftGenerated.Expression, &rightGenerated.Expression, formatUpgrade)
}

func sameWideningExpression(before, after *physical.Expression, formatUpgrade bool) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	left, right := *before, *after
	leftOperands, rightOperands := left.Operands, right.Operands
	left.Operands, right.Operands = nil, nil
	leftType, rightType := left.Type, right.Type
	left.Type, right.Type = physical.StorageType{}, physical.StorageType{}
	if !reflect.DeepEqual(left, right) || !physicalWideningRepresentation(leftType, rightType, formatUpgrade) || len(leftOperands) != len(rightOperands) {
		return false
	}
	for index := range leftOperands {
		if !sameWideningExpression(&leftOperands[index], &rightOperands[index], formatUpgrade) {
			return false
		}
	}
	return true
}

func physicalWideningRepresentation(before, after physical.StorageType, formatUpgrade bool) bool {
	if reflect.DeepEqual(before, after) {
		return true
	}
	return PostgreSQLAutomaticTypeTransition(before, after, formatUpgrade)
}

func (b *diffBuilder) tables() error {
	old := map[ir.ModelID]physical.PhysicalTable{}
	current := map[ir.ModelID]physical.PhysicalTable{}
	for _, v := range b.before.Tables {
		old[v.ID] = v
	}
	for _, v := range b.after.Tables {
		current[v.ID] = v
	}
	ids := modelIDs(old, current)
	for _, id := range ids {
		left, had := old[id]
		right, has := current[id]
		switch {
		case !had:
			base := right
			base.ForeignKeys = nil
			base.Indexes = nil
			if err := b.add(CreateTable, 20, string(id), nil, base, RiskSafe); err != nil {
				return err
			}
			for _, fk := range right.ForeignKeys {
				if err := b.add(AddForeignKey, 50, string(fk.ID), nil, fk, RiskLocking); err != nil {
					return err
				}
			}
			for _, index := range right.Indexes {
				if err := b.add(CreateIndex, 40, string(index.ID), nil, index, RiskSafe); err != nil {
					return err
				}
			}
		case !has:
			if err := b.add(DropTable, 80, string(id), left, nil, RiskDataLoss); err != nil {
				return err
			}
		default:
			if err := validateConcurrencyTransition(left, right); err != nil {
				return err
			}
			if left.Name != right.Name {
				if err := b.add(RenameTable, 25, string(id), left.Name, right.Name, RiskSafe); err != nil {
					return err
				}
			}
			if err := b.columns(left, right); err != nil {
				return err
			}
			if err := b.objects(left, right); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateConcurrencyTransition(before, after physical.PhysicalTable) error {
	if before.OptimisticConcurrency != nil {
		if after.OptimisticConcurrency == nil {
			return fmt.Errorf("optimistic concurrency cannot be removed from model %s", before.ID)
		}
		if *before.OptimisticConcurrency != *after.OptimisticConcurrency {
			return fmt.Errorf("optimistic concurrency field cannot change on model %s", before.ID)
		}
		return nil
	}
	if after.OptimisticConcurrency == nil {
		return nil
	}
	for _, column := range before.Columns {
		if column.ID == *after.OptimisticConcurrency {
			return fmt.Errorf("optimistic concurrency cannot adopt existing field %s on model %s", column.ID, before.ID)
		}
	}
	return nil
}

func (b *diffBuilder) columns(left, right physical.PhysicalTable) error {
	old := map[ir.FieldID]physical.PhysicalColumn{}
	cur := map[ir.FieldID]physical.PhysicalColumn{}
	for _, v := range left.Columns {
		old[v.ID] = v
	}
	for _, v := range right.Columns {
		cur[v.ID] = v
	}
	ids := fieldIDs(old, cur)
	for _, id := range ids {
		a, had := old[id]
		z, has := cur[id]
		switch {
		case !had:
			if right.OptimisticConcurrency != nil && *right.OptimisticConcurrency == id {
				if err := b.requiredConcurrencyColumn(z); err != nil {
					return err
				}
				continue
			}
			if b.generatedAdds[id] == right.ID {
				if err := b.add(AddColumn, 47, string(id), nil, z, RiskRewrite); err != nil {
					return err
				}
				continue
			}
			if !z.Nullable && z.Default.Kind == physical.DefaultNone {
				if z.Generated == nil {
					if err := b.requiredColumnBackfill(z); err != nil {
						return err
					}
					continue
				}
				if err := b.add(AddColumn, 30, string(id), nil, z, RiskManual); err != nil {
					return err
				}
				continue
			}
			if err := b.add(AddColumn, 30, string(id), nil, z, RiskSafe); err != nil {
				return err
			}
		case !has:
			if b.generatedDrops[id] == left.ID {
				if err := b.add(DropColumn, 43, string(id), a, nil, RiskRewrite); err != nil {
					return err
				}
				continue
			}
			if err := b.add(DropColumn, 70, string(id), a, nil, RiskDataLoss); err != nil {
				return err
			}
		default:
			if b.detachedGenerated[id] == left.ID {
				if err := b.add(DropColumn, 43, string(id), a, nil, RiskRewrite); err != nil {
					return err
				}
				if err := b.add(AddColumn, 47, string(id), nil, z, RiskRewrite); err != nil {
					return err
				}
				continue
			}
			formatRepresentation := false
			if a.Name != z.Name {
				if err := b.add(RenameColumn, 35, string(id), a.Name, z.Name, RiskSafe); err != nil {
					return err
				}
			}
			if !reflect.DeepEqual(a.Storage, z.Storage) {
				risk := RiskDataLoss
				if b.formatUpgrade && a.Storage.Kind == physical.StoragePostgreSQLText && z.Storage.Kind == physical.StoragePostgreSQLVarchar {
					if !legacyVarcharRepresentation(left, right, id, a, z) {
						return fmt.Errorf("physical v1->v2 field %s lacks the exact legacy bounded-string representation", id)
					}
					formatRepresentation = true
					risk = RiskRewrite
				} else if SafeWidening(b.after.Provider.Provider, a.Storage, z.Storage) {
					risk = RiskRewrite
				}
				if err := b.add(AlterColumnType, 45, string(id), a.Storage, z.Storage, risk); err != nil {
					return err
				}
			}
			if a.Nullable != z.Nullable {
				risk := RiskSafe
				if !z.Nullable {
					risk = RiskDataLoss
				}
				if err := b.add(AlterColumnNullability, 45, string(id), a.Nullable, z.Nullable, risk); err != nil {
					return err
				}
			}
			if !reflect.DeepEqual(a.Default, z.Default) {
				kind := SetColumnDefault
				if z.Default.Kind == physical.DefaultNone {
					kind = DropColumnDefault
				}
				if err := b.add(kind, 45, string(id), a.Default, z.Default, RiskSafe); err != nil {
					return err
				}
			}
			generatedOrCollationChanged := !reflect.DeepEqual(a.Generated, z.Generated) || !reflect.DeepEqual(a.Collation, z.Collation)
			if generatedOrCollationChanged && !formatRepresentation {
				if err := b.add(RebuildTable, 55, string(left.ID), left, right, RiskRewrite); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (b *diffBuilder) requiredConcurrencyColumn(column physical.PhysicalColumn) error {
	id := string(column.ID)
	if err := b.add(AddColumn, 30, id, nil, column, RiskSafe); err != nil {
		return err
	}
	if err := b.add(InitializeConcurrencyColumn, 31, id, nil, column, RiskRewrite); err != nil {
		return err
	}
	if err := b.add(ValidateConstraint, 32, id, nil, column, RiskSafe); err != nil {
		return err
	}
	return b.add(AlterColumnNullability, 33, id, true, false, RiskSafe)
}

func legacyVarcharRepresentation(beforeTable, afterTable physical.PhysicalTable, field ir.FieldID, before, after physical.PhysicalColumn) bool {
	if before.Storage.Kind != physical.StoragePostgreSQLText || before.Storage.Length != 0 || after.Storage.Kind != physical.StoragePostgreSQLVarchar || after.Storage.Length == 0 {
		return false
	}
	if !sameColumnAcrossV1V2Representation(before, after) {
		return false
	}
	checkID, checkName := physical.HistoricalV1MaxLengthCheckIdentity(beforeTable.ID, field)
	fieldCopy := field
	integer := physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
	literal := ir.TypedLiteralIR{Kind: ir.LiteralInteger, Canonical: fmt.Sprint(after.Storage.Length)}
	column := physical.Expression{Kind: physical.ExpressionColumn, Type: before.Storage, Nullable: before.Nullable, Column: &fieldCopy, Operands: []physical.Expression{}}
	length := physical.Expression{Kind: physical.ExpressionFunction, Type: integer, Nullable: before.Nullable, Symbol: &physical.SemanticSymbol{Identity: "golem.schema.function.length.v1", Kind: ir.SchemaSymbolFunction, Version: 1, Provider: ir.ProviderScopePortable}, Operands: []physical.Expression{column}}
	expected := physical.PhysicalCheck{ID: checkID, Name: checkName, Expression: physical.Expression{Kind: physical.ExpressionOperator, Type: physical.StorageType{Kind: physical.StoragePostgreSQLBoolean}, Nullable: before.Nullable, Symbol: &physical.SemanticSymbol{Identity: "golem.schema.predicate.less-equal.v1", Kind: ir.SchemaSymbolOperator, Version: 1, Provider: ir.ProviderScopePortable}, Operands: []physical.Expression{length, physical.Expression{Kind: physical.ExpressionLiteral, Type: integer, Literal: &literal, Operands: []physical.Expression{}}}}}
	found := 0
	for _, check := range beforeTable.Checks {
		if check.ID == expected.ID {
			found++
			if !reflect.DeepEqual(check, expected) {
				return false
			}
		}
	}
	if found != 1 {
		return false
	}
	for _, check := range afterTable.Checks {
		if check.ID == expected.ID {
			return false
		}
	}
	return true
}

func sameColumnAcrossV1V2Representation(before, after physical.PhysicalColumn) bool {
	left, right := before, after
	left.Storage, right.Storage = physical.StorageType{}, physical.StorageType{}
	leftDefault, rightDefault := left.Default.Expression, right.Default.Expression
	left.Default.Expression, right.Default.Expression = nil, nil
	leftGenerated, rightGenerated := left.Generated, right.Generated
	left.Generated, right.Generated = nil, nil
	if !reflect.DeepEqual(left, right) || !sameRepresentationExpression(leftDefault, rightDefault) {
		return false
	}
	if leftGenerated == nil || rightGenerated == nil {
		return leftGenerated == nil && rightGenerated == nil
	}
	return leftGenerated.Kind == rightGenerated.Kind && sameRepresentationExpression(&leftGenerated.Expression, &rightGenerated.Expression)
}

func sameRepresentationExpression(before, after *physical.Expression) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	left, right := *before, *after
	leftOperands, rightOperands := left.Operands, right.Operands
	left.Operands, right.Operands = nil, nil
	leftType, rightType := left.Type, right.Type
	left.Type, right.Type = physical.StorageType{}, physical.StorageType{}
	if !reflect.DeepEqual(left, right) || !sameRepresentationStorage(leftType, rightType) || len(leftOperands) != len(rightOperands) {
		return false
	}
	for index := range leftOperands {
		if !sameRepresentationExpression(&leftOperands[index], &rightOperands[index]) {
			return false
		}
	}
	return true
}

func sameRepresentationStorage(before, after physical.StorageType) bool {
	return reflect.DeepEqual(before, after) || (before.Kind == physical.StoragePostgreSQLText && before.Precision == 0 && before.Scale == 0 && before.Length == 0 && before.Symbol == nil && after.Kind == physical.StoragePostgreSQLVarchar && after.Precision == 0 && after.Scale == 0 && after.Length > 0 && after.Symbol == nil)
}

func (b *diffBuilder) objects(left, right physical.PhysicalTable) error {
	if err := b.key(left.ID, left.PrimaryKey, right.PrimaryKey, AddPrimaryKey, DropPrimaryKey, RiskDataLoss); err != nil {
		return err
	}
	if err := diffByID(left.Uniques, right.Uniques, func(v physical.PhysicalKey) string { return string(v.ID) }, func(v any, add bool) error {
		if add {
			return b.add(AddUnique, 40, objectID(v), nil, v, RiskDataLoss)
		}
		return b.add(DropUnique, 60, objectID(v), v, nil, RiskSafe)
	}, b); err != nil {
		return err
	}
	if err := diffByID(left.ForeignKeys, right.ForeignKeys, func(v physical.PhysicalForeignKey) string { return string(v.ID) }, func(v any, add bool) error {
		if add {
			return b.add(AddForeignKey, 50, objectID(v), nil, v, RiskLocking)
		}
		return b.add(DropForeignKey, 60, objectID(v), v, nil, RiskSafe)
	}, b); err != nil {
		return err
	}
	if err := diffByID(left.Checks, right.Checks, func(v physical.PhysicalCheck) string { return string(v.ID) }, func(v any, add bool) error {
		if add {
			return b.add(AddCheck, 40, objectID(v), nil, v, RiskDataLoss)
		}
		return b.add(DropCheck, 60, objectID(v), v, nil, RiskSafe)
	}, b); err != nil {
		return err
	}
	return b.indexes(left.Indexes, right.Indexes)
}

func (b *diffBuilder) indexes(before, after []physical.PhysicalIndex) error {
	old := map[ir.IndexID]physical.PhysicalIndex{}
	current := map[ir.IndexID]physical.PhysicalIndex{}
	for _, index := range before {
		old[index.ID] = index
	}
	for _, index := range after {
		current[index.ID] = index
	}
	ids := make([]string, 0, len(old)+len(current))
	seen := map[ir.IndexID]bool{}
	for id := range old {
		seen[id] = true
		ids = append(ids, string(id))
	}
	for id := range current {
		if !seen[id] {
			ids = append(ids, string(id))
		}
	}
	sort.Strings(ids)
	for _, rawID := range ids {
		id := ir.IndexID(rawID)
		left, had := old[id]
		right, has := current[id]
		switch {
		case !had:
			if err := b.add(CreateIndex, 40, rawID, nil, right, RiskSafe); err != nil {
				return err
			}
		case !has:
			if err := b.add(DropIndex, 60, rawID, left, nil, RiskSafe); err != nil {
				return err
			}
		case reflect.DeepEqual(left, right):
			continue
		default:
			leftWithoutName, rightWithoutName := left, right
			leftWithoutName.Name, rightWithoutName.Name = "", ""
			if reflect.DeepEqual(leftWithoutName, rightWithoutName) {
				if err := b.add(RenameIndex, 45, rawID, left.Name, right.Name, RiskSafe); err != nil {
					return err
				}
				continue
			}
			if err := b.add(DropIndex, 60, rawID, left, nil, RiskSafe); err != nil {
				return err
			}
			if err := b.add(CreateIndex, 40, rawID, nil, right, RiskSafe); err != nil {
				return err
			}
		}
	}
	return nil
}

// recreateDestructiveDependents makes implicit provider requirements explicit
// in the semantic DAG. A type/nullability rewrite cannot leave an unchanged
// index or constraint installed around the altered column: it is dropped from
// its exact before definition and restored from its exact after definition.
func (b *diffBuilder) recreateDestructiveDependents() error {
	destructiveFields := map[ir.FieldID]bool{}
	for _, operation := range b.operations {
		if operation.Kind == AlterColumnType || operation.Kind == AlterColumnNullability {
			destructiveFields[ir.FieldID(operation.ObjectID)] = true
		}
	}
	if len(destructiveFields) == 0 {
		return nil
	}

	afterTables := map[ir.ModelID]physical.PhysicalTable{}
	for _, table := range b.after.Tables {
		afterTables[table.ID] = table
	}
	for _, beforeTable := range b.before.Tables {
		afterTable, tableStillExists := afterTables[beforeTable.ID]
		if !tableStillExists {
			continue
		}
		for field := range destructiveFields {
			if err := b.recreateTableDependents(beforeTable, afterTable, field); err != nil {
				return err
			}
		}
		for field := range b.detachedGenerated {
			// Referencing foreign keys live in a different table but carry the
			// detached target FieldID in ReferencedColumns. Recreate dependents
			// across the complete schema, not only the generated field's owner.
			if err := b.recreateTableDependents(beforeTable, afterTable, field); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *diffBuilder) recreateTableDependents(before, after physical.PhysicalTable, field ir.FieldID) error {
	if before.PrimaryKey != nil && objectUsesField(before, *before.PrimaryKey, field) {
		if err := b.add(DropPrimaryKey, 60, string(before.PrimaryKey.ID), *before.PrimaryKey, nil, RiskSafe); err != nil {
			return err
		}
		if after.PrimaryKey != nil && after.PrimaryKey.ID == before.PrimaryKey.ID {
			if err := b.add(AddPrimaryKey, 40, string(after.PrimaryKey.ID), nil, *after.PrimaryKey, RiskDataLoss); err != nil {
				return err
			}
		}
	}
	for _, value := range before.Uniques {
		if !objectUsesField(before, value, field) {
			continue
		}
		if err := b.add(DropUnique, 60, string(value.ID), value, nil, RiskSafe); err != nil {
			return err
		}
		if restored, ok := findKey(after.Uniques, value.ID); ok {
			if err := b.add(AddUnique, 40, string(restored.ID), nil, restored, RiskDataLoss); err != nil {
				return err
			}
		}
	}
	for _, value := range before.ForeignKeys {
		if !objectUsesField(before, value, field) {
			continue
		}
		if err := b.add(DropForeignKey, 60, string(value.ID), value, nil, RiskSafe); err != nil {
			return err
		}
		if restored, ok := findForeignKey(after.ForeignKeys, value.ID); ok {
			if err := b.add(AddForeignKey, 50, string(restored.ID), nil, restored, RiskLocking); err != nil {
				return err
			}
		}
	}
	for _, value := range before.Checks {
		if !objectUsesField(before, value, field) {
			continue
		}
		if err := b.add(DropCheck, 60, string(value.ID), value, nil, RiskSafe); err != nil {
			return err
		}
		if restored, ok := findCheck(after.Checks, value.ID); ok {
			if err := b.add(AddCheck, 40, string(restored.ID), nil, restored, RiskDataLoss); err != nil {
				return err
			}
		}
	}
	for _, value := range before.Indexes {
		if !objectUsesField(before, value, field) {
			continue
		}
		if err := b.add(DropIndex, 60, string(value.ID), value, nil, RiskSafe); err != nil {
			return err
		}
		if restored, ok := findIndex(after.Indexes, value.ID); ok {
			if err := b.add(CreateIndex, 40, string(restored.ID), nil, restored, RiskSafe); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *diffBuilder) key(table ir.ModelID, a, z *physical.PhysicalKey, add, drop OperationKind, risk Risk) error {
	if reflect.DeepEqual(a, z) {
		return nil
	}
	if a != nil {
		if err := b.add(drop, 60, string(a.ID), *a, nil, RiskSafe); err != nil {
			return err
		}
	}
	if z != nil {
		return b.add(add, 40, string(z.ID), nil, *z, risk)
	}
	return nil
}

func (b *diffBuilder) add(kind OperationKind, stage uint16, id string, before, after any, risk Risk) error {
	a, err := fragmentVersion(before, b.beforeCanonicalVersion)
	if err != nil {
		return err
	}
	z, err := fragmentVersion(after, b.afterCanonicalVersion)
	if err != nil {
		return err
	}
	op := Operation{Kind: kind, Stage: stage, ObjectID: id, Before: a, After: z, Mode: Transactional, Risk: risk, LogicalPath: id}
	op.ID = stableOperationID(kind, id, a, z)
	for _, existing := range b.operations {
		if existing.ID == op.ID {
			return nil
		}
	}
	b.operations = append(b.operations, op)
	return nil
}

func (b *diffBuilder) dependencies() {
	lookup := map[string]OperationID{}
	for _, op := range b.operations {
		lookup[string(op.Kind)+"\x00"+op.ObjectID] = op.ID
	}
	addDep := func(op *Operation, kind OperationKind, id string) {
		if dependency := lookup[string(kind)+"\x00"+id]; dependency != "" && dependency != op.ID {
			for _, existing := range op.Dependencies {
				if existing == dependency {
					return
				}
			}
			op.Dependencies = append(op.Dependencies, dependency)
		}
	}
	tables := map[ir.ModelID]physical.PhysicalTable{}
	for _, table := range b.after.Tables {
		tables[table.ID] = table
	}
	for _, table := range b.before.Tables {
		if _, ok := tables[table.ID]; !ok {
			tables[table.ID] = table
		}
	}
	columnTable := map[string]ir.ModelID{}
	beforeColumns := map[ir.FieldID]physical.PhysicalColumn{}
	afterColumns := map[ir.FieldID]physical.PhysicalColumn{}
	objects := map[string]ir.ModelID{}
	extensionModels := map[string]ir.ModelID{}
	for _, table := range tables {
		for _, column := range table.Columns {
			columnTable[string(column.ID)] = table.ID
		}
		if table.PrimaryKey != nil {
			objects[string(table.PrimaryKey.ID)] = table.ID
		}
		for _, key := range table.Uniques {
			objects[string(key.ID)] = table.ID
		}
		for _, fk := range table.ForeignKeys {
			objects[string(fk.ID)] = table.ID
		}
		for _, check := range table.Checks {
			objects[string(check.ID)] = table.ID
		}
		for _, index := range table.Indexes {
			objects[string(index.ID)] = table.ID
		}
	}
	for _, table := range b.before.Tables {
		for _, column := range table.Columns {
			beforeColumns[column.ID] = column
		}
	}
	for _, table := range b.after.Tables {
		for _, column := range table.Columns {
			afterColumns[column.ID] = column
		}
	}
	for _, extension := range b.after.Extensions {
		extensionModels[string(extension.ID)] = extension.Owner.ModelID
	}
	for _, extension := range b.before.Extensions {
		if extensionModels[string(extension.ID)] == "" {
			extensionModels[string(extension.ID)] = extension.Owner.ModelID
		}
	}
	for index := range b.operations {
		op := &b.operations[index]
		if op.Kind == BootstrapSystemSchema {
			addDep(op, CreateNamespace, string(b.after.Namespace.Name))
		}
		if op.Kind != BootstrapSystemSchema && op.Kind != AddSystemObject && op.Kind != CreateNamespace && op.Kind != RecordSchemaVersion {
			addDep(op, BootstrapSystemSchema, "system-schema")
		}
		if op.Kind == CreateTable {
			addDep(op, CreateNamespace, string(b.after.Namespace.Name))
		}
		if op.Kind == CreateProviderExtension {
			addDep(op, CreateTable, string(extensionModels[op.ObjectID]))
			addDep(op, DropProviderExtension, op.ObjectID)
		}
		if op.Kind == DropTable {
			for extensionID, modelID := range extensionModels {
				if string(modelID) == op.ObjectID {
					addDep(op, DropProviderExtension, extensionID)
				}
			}
		}
		tableID := columnTable[op.ObjectID]
		if tableID == "" {
			tableID = objects[op.ObjectID]
		}
		if tableID != "" && op.Kind != CreateTable {
			addDep(op, CreateTable, string(tableID))
			addDep(op, RenameTable, string(tableID))
		}
		if op.Kind == BackfillColumn {
			addDep(op, AddColumn, op.ObjectID)
		}
		if op.Kind == InitializeConcurrencyColumn {
			addDep(op, AddColumn, op.ObjectID)
		}
		if !b.historicalReplay && op.Kind == AddColumn {
			if column := afterColumns[ir.FieldID(op.ObjectID)]; column.Generated != nil {
				for _, field := range expressionFields(column.Generated.Expression) {
					addDep(op, AddColumn, string(field))
				}
			}
		}
		if op.Kind == ValidateConstraint {
			addDep(op, BackfillColumn, op.ObjectID)
			addDep(op, InitializeConcurrencyColumn, op.ObjectID)
		}
		if op.Kind == AlterColumnNullability {
			addDep(op, ValidateConstraint, op.ObjectID)
		}
		for add, drop := range map[OperationKind]OperationKind{AddPrimaryKey: DropPrimaryKey, AddUnique: DropUnique, AddForeignKey: DropForeignKey, AddCheck: DropCheck, CreateIndex: DropIndex} {
			if op.Kind == add {
				addDep(op, drop, op.ObjectID)
			}
		}
		if op.Kind == AddPrimaryKey || op.Kind == AddUnique || op.Kind == AddForeignKey || op.Kind == AddCheck || op.Kind == CreateIndex {
			if table, ok := tables[tableID]; ok {
				for _, field := range referencedFields(table, op.ObjectID) {
					addDep(op, AddColumn, string(field))
					addDep(op, AlterColumnType, string(field))
					addDep(op, AlterColumnNullability, string(field))
				}
			}
		}
		if op.Kind == DropPrimaryKey || op.Kind == DropUnique {
			if owner, ok := tables[tableID]; ok {
				columns := referencedFields(owner, op.ObjectID)
				for _, table := range tables {
					for _, fk := range table.ForeignKeys {
						if fk.ReferencedTable == owner.ID && reflect.DeepEqual(fk.ReferencedColumns, columns) {
							addDep(op, DropForeignKey, string(fk.ID))
						}
					}
				}
			}
		}
		if op.Kind == AddForeignKey {
			for _, table := range tables {
				for _, fk := range table.ForeignKeys {
					if string(fk.ID) != op.ObjectID {
						continue
					}
					addDep(op, CreateTable, string(fk.ReferencedTable))
					for _, field := range fk.ReferencedColumns {
						addDep(op, AddColumn, string(field))
						addDep(op, AlterColumnType, string(field))
						addDep(op, AlterColumnNullability, string(field))
					}
					if target, ok := tables[fk.ReferencedTable]; ok {
						if target.PrimaryKey != nil && reflect.DeepEqual(target.PrimaryKey.Columns, fk.ReferencedColumns) {
							addDep(op, AddPrimaryKey, string(target.PrimaryKey.ID))
						}
						for _, key := range target.Uniques {
							if reflect.DeepEqual(key.Columns, fk.ReferencedColumns) {
								addDep(op, AddUnique, string(key.ID))
							}
						}
					}
				}
			}
		}
		if op.Kind == DropColumn || op.Kind == AlterColumnType || op.Kind == AlterColumnNullability {
			for _, table := range append(append([]physical.PhysicalTable(nil), b.before.Tables...), b.after.Tables...) {
				for _, object := range dependentObjects(table, ir.FieldID(op.ObjectID)) {
					for _, kind := range []OperationKind{DropForeignKey, DropIndex, DropCheck, DropUnique, DropPrimaryKey} {
						addDep(op, kind, object)
					}
				}
			}
		}
		if !b.historicalReplay && op.Kind == DropColumn {
			field := ir.FieldID(op.ObjectID)
			for generated, column := range beforeColumns {
				if column.Generated != nil && containsField(expressionFields(column.Generated.Expression), field) {
					addDep(op, DropColumn, string(generated))
				}
			}
		}
		// The generated-column dependency algebra was introduced with physical
		// format v2. Exact v1/v1 replay is owned by the retained v1 planner and
		// must never acquire dependencies invented by the current planner.
		if !b.historicalReplay && (len(b.generatedDrops) != 0 || len(b.generatedAdds) != 0) {
			switch op.Kind {
			case DropColumn:
				field := ir.FieldID(op.ObjectID)
				if tableID, generated := b.generatedDrops[field]; generated {
					for dependent, owner := range b.generatedDrops {
						if owner == tableID && containsField(b.generatedInputs[dependent], field) {
							addDep(op, DropColumn, string(dependent))
						}
					}
				}
			case AlterColumnType:
				tableID := columnTable[op.ObjectID]
				for field, owner := range b.generatedDrops {
					if owner == tableID {
						addDep(op, DropColumn, string(field))
					}
				}
			case AddColumn:
				field := ir.FieldID(op.ObjectID)
				tableID, generated := b.generatedAdds[field]
				if generated {
					if b.generatedDrops[field] == tableID {
						addDep(op, DropColumn, op.ObjectID)
					}
					for _, candidate := range b.operations {
						if candidate.Kind == AlterColumnType && columnTable[candidate.ObjectID] == tableID {
							addDep(op, AlterColumnType, candidate.ObjectID)
						}
					}
					for _, input := range b.generatedInputs[field] {
						if b.generatedAdds[input] == tableID {
							addDep(op, AddColumn, string(input))
						}
					}
				}
			}
		}
		if op.Kind == DropTable {
			for _, table := range tables {
				if table.ID == ir.ModelID(op.ObjectID) {
					continue
				}
				for _, fk := range table.ForeignKeys {
					if fk.ReferencedTable == ir.ModelID(op.ObjectID) {
						addDep(op, DropForeignKey, string(fk.ID))
					}
				}
			}
		}
		if op.Kind == RecordSchemaVersion {
			for _, other := range b.operations {
				if other.ID != op.ID {
					addDep(op, other.Kind, other.ObjectID)
				}
			}
		}
		sort.Slice(op.Dependencies, func(i, j int) bool { return op.Dependencies[i] < op.Dependencies[j] })
	}
}

func referencedFields(table physical.PhysicalTable, id string) []ir.FieldID {
	if table.PrimaryKey != nil && string(table.PrimaryKey.ID) == id {
		return table.PrimaryKey.Columns
	}
	for _, v := range table.Uniques {
		if string(v.ID) == id {
			return v.Columns
		}
	}
	for _, v := range table.ForeignKeys {
		if string(v.ID) == id {
			return v.Columns
		}
	}
	for _, v := range table.Indexes {
		if string(v.ID) == id {
			var out []ir.FieldID
			for _, key := range v.Keys {
				if key.Column != nil {
					out = append(out, *key.Column)
				}
				if key.Expression != nil {
					out = append(out, expressionFields(*key.Expression)...)
				}
			}
			out = append(out, v.Include...)
			if v.Predicate != nil {
				out = append(out, expressionFields(*v.Predicate)...)
			}
			return out
		}
	}
	for _, v := range table.Checks {
		if string(v.ID) == id {
			return expressionFields(v.Expression)
		}
	}
	return nil
}
func dependentObjects(table physical.PhysicalTable, field ir.FieldID) []string {
	var out []string
	if table.PrimaryKey != nil && containsField(table.PrimaryKey.Columns, field) {
		out = append(out, string(table.PrimaryKey.ID))
	}
	for _, v := range table.Uniques {
		if containsField(v.Columns, field) {
			out = append(out, string(v.ID))
		}
	}
	for _, v := range table.ForeignKeys {
		if containsField(v.Columns, field) || containsField(v.ReferencedColumns, field) {
			out = append(out, string(v.ID))
		}
	}
	for _, v := range table.Indexes {
		if containsField(v.Include, field) {
			out = append(out, string(v.ID))
			continue
		}
		for _, key := range v.Keys {
			if (key.Column != nil && *key.Column == field) || (key.Expression != nil && containsField(expressionFields(*key.Expression), field)) {
				out = append(out, string(v.ID))
				break
			}
		}
		if v.Predicate != nil && containsField(expressionFields(*v.Predicate), field) {
			out = append(out, string(v.ID))
		}
	}
	for _, v := range table.Checks {
		if containsField(expressionFields(v.Expression), field) {
			out = append(out, string(v.ID))
		}
	}
	return out
}

func expressionFields(expression physical.Expression) []ir.FieldID {
	var out []ir.FieldID
	if expression.Column != nil {
		out = append(out, *expression.Column)
	}
	for _, operand := range expression.Operands {
		out = append(out, expressionFields(operand)...)
	}
	return out
}

func objectUsesField(_ physical.PhysicalTable, object any, field ir.FieldID) bool {
	switch value := object.(type) {
	case physical.PhysicalKey:
		return containsField(value.Columns, field)
	case physical.PhysicalForeignKey:
		return containsField(value.Columns, field) || containsField(value.ReferencedColumns, field)
	case physical.PhysicalCheck:
		return containsField(expressionFields(value.Expression), field)
	case physical.PhysicalIndex:
		if containsField(value.Include, field) {
			return true
		}
		if value.Predicate != nil && containsField(expressionFields(*value.Predicate), field) {
			return true
		}
		for _, key := range value.Keys {
			if key.Column != nil && *key.Column == field {
				return true
			}
			if key.Expression != nil && containsField(expressionFields(*key.Expression), field) {
				return true
			}
		}
	}
	return false
}

func findKey(values []physical.PhysicalKey, id ir.KeyID) (physical.PhysicalKey, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return physical.PhysicalKey{}, false
}

func findForeignKey(values []physical.PhysicalForeignKey, id ir.ForeignKeyID) (physical.PhysicalForeignKey, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return physical.PhysicalForeignKey{}, false
}

func findCheck(values []physical.PhysicalCheck, id ir.CheckID) (physical.PhysicalCheck, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return physical.PhysicalCheck{}, false
}

func findIndex(values []physical.PhysicalIndex, id ir.IndexID) (physical.PhysicalIndex, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return physical.PhysicalIndex{}, false
}
func containsField(values []ir.FieldID, field ir.FieldID) bool {
	for _, value := range values {
		if value == field {
			return true
		}
	}
	return false
}

func fragmentVersion(value any, version uint32) (Digest, error) {
	if value == nil {
		return "", nil
	}
	encoded, err := physical.CanonicalFragmentVersion(value, version)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return Digest(hex.EncodeToString(sum[:])), nil
}
func stableOperationID(kind OperationKind, id string, before, after Digest) OperationID {
	sum := sha256.Sum256([]byte(string(kind) + "\x00" + id + "\x00" + string(before) + "\x00" + string(after)))
	return OperationID(hex.EncodeToString(sum[:16]))
}

func diffByID[T any](a, z []T, id func(T) string, emit func(any, bool) error, _ *diffBuilder) error {
	old := map[string]T{}
	cur := map[string]T{}
	for _, v := range a {
		old[id(v)] = v
	}
	for _, v := range z {
		cur[id(v)] = v
	}
	ids := stringIDs(old, cur)
	for _, key := range ids {
		left, had := old[key]
		right, has := cur[key]
		if had && has && reflect.DeepEqual(left, right) {
			continue
		}
		if had {
			if err := emit(left, false); err != nil {
				return err
			}
		}
		if has {
			if err := emit(right, true); err != nil {
				return err
			}
		}
	}
	return nil
}
func objectID(v any) string {
	switch x := v.(type) {
	case physical.PhysicalKey:
		return string(x.ID)
	case physical.PhysicalForeignKey:
		return string(x.ID)
	case physical.PhysicalCheck:
		return string(x.ID)
	case physical.PhysicalIndex:
		return string(x.ID)
	}
	return ""
}
func modelIDs[A, B any](a map[ir.ModelID]A, b map[ir.ModelID]B) []ir.ModelID {
	seen := map[ir.ModelID]bool{}
	var out []ir.ModelID
	for id := range a {
		seen[id] = true
		out = append(out, id)
	}
	for id := range b {
		if !seen[id] {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
func fieldIDs[A, B any](a map[ir.FieldID]A, b map[ir.FieldID]B) []ir.FieldID {
	seen := map[ir.FieldID]bool{}
	var out []ir.FieldID
	for id := range a {
		seen[id] = true
		out = append(out, id)
	}
	for id := range b {
		if !seen[id] {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
func stringIDs[T any](a, b map[string]T) []string {
	seen := map[string]bool{}
	var out []string
	for id := range a {
		seen[id] = true
		out = append(out, id)
	}
	for id := range b {
		if !seen[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
