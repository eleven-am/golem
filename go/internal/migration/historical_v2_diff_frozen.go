package migration

// Frozen physical-v2 migration algebra. It is intentionally independent of
// the mutable current planner.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

const (
	historicalV2SemanticIndexKind = "golem.semantic-index"
	historicalV2SemanticVersion   = uint16(1)
	historicalV2TemporalPrecision = uint32(6)
)

// diffHistoricalV2Tagged compares validated normalized physical schemas by stable IDs. It never
// infers a rename from spelling similarity.
func diffHistoricalV2Tagged(before, after physical.PhysicalSchema) (Plan, error) {
	left, err := physical.NormalizeHistoricalV2(before)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize before: %w", err)
	}
	right, err := physical.NormalizeHistoricalV2(after)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize after: %w", err)
	}
	if left.Provider.Provider != right.Provider.Provider {
		return Plan{}, fmt.Errorf("cannot diff providers %s and %s", left.Provider.Provider, right.Provider.Provider)
	}
	leftFP, err := physical.HistoricalPhysicalFingerprint(left)
	if err != nil {
		return Plan{}, err
	}
	rightFP, err := physical.HistoricalPhysicalFingerprint(right)
	if err != nil {
		return Plan{}, err
	}
	if reflect.DeepEqual(left, right) {
		return withPlanSnapshotFacts(Plan{Provider: right.Provider.Provider, BeforeFingerprint: Digest(leftFP.String()), AfterFingerprint: Digest(rightFP.String())}, left, right), nil
	}
	builder := historicalV2DiffBuilder{before: left, after: right, beforeCanonicalVersion: 2, afterCanonicalVersion: 2}
	if err := builder.identifyGeneratedWideningRecreation(); err != nil {
		return Plan{}, err
	}
	beforeSystem, err := physical.HistoricalSystemFingerprint(left)
	if err != nil {
		return Plan{}, err
	}
	afterSystem, err := physical.HistoricalSystemFingerprint(right)
	if err != nil {
		return Plan{}, err
	}
	initial := false
	if beforeSystem != afterSystem {
		if reflect.DeepEqual(left.System, right.System) {
			// Released v2 provider-runtime transitions changed the physical and
			// system fingerprints without changing a database system object.
		} else if historicalV2EmptySystemSchema(left.System) && len(left.Tables) == 0 && len(left.Extensions) == 0 && len(left.Unmanaged) == 0 && len(right.System.Objects) != 0 {
			initial = true
		} else {
			additions, upgradeErr := historicalV2SystemObjectAdditions(left.System, right.System)
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
	if err := builder.extensions(); err != nil {
		return Plan{}, err
	}
	if err := builder.tables(); err != nil {
		return Plan{}, err
	}
	if err := builder.recreateDestructiveDependents(); err != nil {
		return Plan{}, err
	}
	recordBefore, recordAfter := Digest(leftFP.String()), Digest(rightFP.String())
	record := Operation{Kind: RecordSchemaVersion, Stage: 100, ObjectID: "schema-version", Before: recordBefore, After: recordAfter, Mode: Transactional, Risk: RiskSafe, LogicalPath: "schema"}
	record.ID = historicalV2StableOperationID(record.Kind, record.ObjectID, record.Before, record.After)
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
	return plan, nil
}

// diffHistoricalV1ToV2Tagged owns the immutable format-transition algebra
// published with physical v2. It deliberately shares only this frozen source
// file with v2 replay, never the mutable current planner.
func diffHistoricalV1ToV2Tagged(before, after physical.PhysicalSchema) (Plan, error) {
	leftInput := before
	restoreSQLiteDriver := false
	if leftInput.Provider.Provider == ir.SQLite && leftInput.Provider.Driver == (physical.DriverIdentity{Module: "github.com/ncruces/go-sqlite3", Adapter: "sqlx"}) {
		leftInput.Provider.Driver = physical.DriverIdentity{Module: "modernc.org/sqlite", Adapter: "sqlx"}
		restoreSQLiteDriver = true
	}
	left, err := physical.NormalizeHistoricalV1(leftInput)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize historical v1 before: %w", err)
	}
	if restoreSQLiteDriver {
		left.Provider.Driver = before.Provider.Driver
	}
	right, err := physical.NormalizeHistoricalV2(after)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize historical v2 after: %w", err)
	}
	if left.Provider.Provider != right.Provider.Provider {
		return Plan{}, fmt.Errorf("cannot diff providers %s and %s", left.Provider.Provider, right.Provider.Provider)
	}
	leftFP, err := physical.HistoricalPhysicalFingerprint(left)
	if err != nil {
		return Plan{}, err
	}
	rightFP, err := physical.HistoricalPhysicalFingerprint(right)
	if err != nil {
		return Plan{}, err
	}
	builder := historicalV2DiffBuilder{before: left, after: right, beforeCanonicalVersion: 1, afterCanonicalVersion: 2, formatUpgrade: true}
	if err := builder.identifyGeneratedWideningRecreation(); err != nil {
		return Plan{}, err
	}
	beforeSystem, err := physical.HistoricalSystemFingerprint(left)
	if err != nil {
		return Plan{}, err
	}
	afterSystem, err := physical.HistoricalSystemFingerprint(right)
	if err != nil {
		return Plan{}, err
	}
	initial := false
	if beforeSystem != afterSystem {
		if reflect.DeepEqual(left.System, right.System) {
			// Driver/canonical metadata changed without a database system object.
		} else if historicalV2EmptySystemSchema(left.System) && len(left.Tables) == 0 && len(left.Extensions) == 0 && len(left.Unmanaged) == 0 && len(right.System.Objects) != 0 {
			initial = true
		} else {
			additions, upgradeErr := historicalV2SystemObjectAdditions(left.System, right.System)
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
	if err := builder.extensions(); err != nil {
		return Plan{}, err
	}
	if err := builder.tables(); err != nil {
		return Plan{}, err
	}
	if err := builder.recreateDestructiveDependents(); err != nil {
		return Plan{}, err
	}
	recordBefore, recordAfter := Digest(leftFP.String()), Digest(rightFP.String())
	record := Operation{Kind: RecordSchemaVersion, Stage: 100, ObjectID: "schema-version", Before: recordBefore, After: recordAfter, Mode: Transactional, Risk: RiskSafe, LogicalPath: "schema"}
	record.ID = historicalV2StableOperationID(record.Kind, record.ObjectID, record.Before, record.After)
	builder.operations = append(builder.operations, record)
	builder.dependencies()
	plan := Plan{Provider: right.Provider.Provider, Initial: initial, BeforeFingerprint: recordBefore, AfterFingerprint: recordAfter, Operations: builder.operations}
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

func historicalV2SystemObjectAdditions(before, after physical.SystemSchema) ([]physical.SystemObject, error) {
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
	if len(additions) != 1 || !historicalV2RegisteredAdditiveSystemObject(additions[0]) {
		return nil, fmt.Errorf("system schema change is not a registered additive system object")
	}
	return additions, nil
}

func historicalV2RegisteredAdditiveSystemObject(object physical.SystemObject) bool {
	empty := len(object.Attributes) == 0 && len(object.RequiredCapabilities) == 0
	return empty && (object.ID == "14b2d0b9de583fe675fa72de1d1c78c8" && object.Kind == physical.SystemOutbox && object.Version == 1 && object.Name == "_golem_outbox" ||
		object.ID == "51aa5c96fd5d24e27e182ed85f7bcbf2" && object.Kind == physical.SystemOutboxDelivery && object.Version == 1 && object.Name == "_golem_outbox_delivery" ||
		object.ID == "076704f0bfb30b5fed47137811a6dd18" && object.Kind == physical.SystemUpsertGuard && object.Version == 1 && object.Name == "_golem_upsert_guard")
}

func historicalV2EmptySystemSchema(system physical.SystemSchema) bool {
	return system.Version == 0 && system.Namespace.Name == "" && len(system.Objects) == 0
}

func (b *historicalV2DiffBuilder) extensions() error {
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
			if left.Kind != historicalV2SemanticIndexKind || right.Kind != historicalV2SemanticIndexKind || left.Version != historicalV2SemanticVersion || right.Version != historicalV2SemanticVersion || left.Owner != right.Owner || left.Provider != right.Provider {
				return fmt.Errorf("provider extension %s cannot change in place", id)
			}
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

type historicalV2DiffBuilder struct {
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
func (b *historicalV2DiffBuilder) identifyGeneratedWideningRecreation() error {
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
				if historicalV2PhysicalWideningRepresentation(before.Storage, column.Storage, legacy) {
					changing[column.ID] = true
				}
			}
		}
		for field, beforeColumn := range beforeColumns {
			afterColumn, retained := afterColumns[field]
			if beforeColumn.Generated == nil || retained && afterColumn.Generated != nil {
				continue
			}
			inputs := historicalV2UniqueFields(historicalV2ExpressionFields(beforeColumn.Generated.Expression))
			if historicalV2FieldsIntersect(inputs, changing) {
				b.generatedDrops[field] = beforeTable.ID
				b.generatedInputs[field] = inputs
			}
		}
		for field, afterColumn := range afterColumns {
			beforeColumn, retained := beforeColumns[field]
			if afterColumn.Generated == nil {
				continue
			}
			inputs := historicalV2UniqueFields(historicalV2ExpressionFields(afterColumn.Generated.Expression))
			if !historicalV2FieldsIntersect(inputs, changing) {
				continue
			}
			if !retained || beforeColumn.Generated == nil {
				b.generatedAdds[field] = beforeTable.ID
				b.generatedInputs[field] = inputs
				continue
			}
			if beforeColumn.Generated.Kind != physical.GeneratedStored || afterColumn.Generated.Kind != physical.GeneratedStored || !historicalV2SameGeneratedColumnAcrossWidening(beforeColumn, afterColumn, b.formatUpgrade) {
				return fmt.Errorf("PostgreSQL widening generated field %s cannot be recreated exactly", field)
			}
			// A bounded input does not prove that an arbitrary generated
			// expression has a bounded output. The released v1 representation
			// owned an independent registered max-length CHECK for every bounded
			// field, including generated fields; require that exact proof before
			// translating the generated output from text to varchar.
			if b.formatUpgrade && beforeColumn.Storage.Kind == physical.StoragePostgreSQLText && afterColumn.Storage.Kind == physical.StoragePostgreSQLVarchar && !historicalV2LegacyVarcharRepresentation(beforeTable, afterTable, field, beforeColumn, afterColumn) {
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

func historicalV2FieldsIntersect(fields []ir.FieldID, selected map[ir.FieldID]bool) bool {
	for _, field := range fields {
		if selected[field] {
			return true
		}
	}
	return false
}

func historicalV2UniqueFields(values []ir.FieldID) []ir.FieldID {
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

func historicalV2SameGeneratedColumnAcrossWidening(before, after physical.PhysicalColumn, formatUpgrade bool) bool {
	left, right := before, after
	// Stable FieldID owns identity. The reviewed detach/add envelope can apply a
	// simultaneous physical rename without a separate RenameColumn operation.
	left.Name, right.Name = "", ""
	left.Storage, right.Storage = physical.StorageType{}, physical.StorageType{}
	leftDefault, rightDefault := left.Default.Expression, right.Default.Expression
	left.Default.Expression, right.Default.Expression = nil, nil
	leftGenerated, rightGenerated := left.Generated, right.Generated
	left.Generated, right.Generated = nil, nil
	if !reflect.DeepEqual(left, right) || !historicalV2SameWideningExpression(leftDefault, rightDefault, formatUpgrade) || leftGenerated == nil || rightGenerated == nil || leftGenerated.Kind != rightGenerated.Kind {
		return false
	}
	return historicalV2PhysicalWideningRepresentation(before.Storage, after.Storage, formatUpgrade) && historicalV2SameWideningExpression(&leftGenerated.Expression, &rightGenerated.Expression, formatUpgrade)
}

func historicalV2SameWideningExpression(before, after *physical.Expression, formatUpgrade bool) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	left, right := *before, *after
	leftOperands, rightOperands := left.Operands, right.Operands
	left.Operands, right.Operands = nil, nil
	leftType, rightType := left.Type, right.Type
	left.Type, right.Type = physical.StorageType{}, physical.StorageType{}
	if !reflect.DeepEqual(left, right) || !historicalV2PhysicalWideningRepresentation(leftType, rightType, formatUpgrade) || len(leftOperands) != len(rightOperands) {
		return false
	}
	for index := range leftOperands {
		if !historicalV2SameWideningExpression(&leftOperands[index], &rightOperands[index], formatUpgrade) {
			return false
		}
	}
	return true
}

func historicalV2PhysicalWideningRepresentation(before, after physical.StorageType, formatUpgrade bool) bool {
	if reflect.DeepEqual(before, after) {
		return true
	}
	if historicalV2SafeWidening(ir.PostgreSQL, before, after) {
		return true
	}
	return formatUpgrade && before.Kind == physical.StoragePostgreSQLText && before.Precision == 0 && before.Scale == 0 && before.Length == 0 && before.Symbol == nil && after.Kind == physical.StoragePostgreSQLVarchar && after.Precision == 0 && after.Scale == 0 && after.Length > 0 && after.Symbol == nil
}

func (b *historicalV2DiffBuilder) tables() error {
	old := map[ir.ModelID]physical.PhysicalTable{}
	current := map[ir.ModelID]physical.PhysicalTable{}
	for _, v := range b.before.Tables {
		old[v.ID] = v
	}
	for _, v := range b.after.Tables {
		current[v.ID] = v
	}
	ids := historicalV2ModelIDs(old, current)
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

func (b *historicalV2DiffBuilder) columns(left, right physical.PhysicalTable) error {
	old := map[ir.FieldID]physical.PhysicalColumn{}
	cur := map[ir.FieldID]physical.PhysicalColumn{}
	for _, v := range left.Columns {
		old[v.ID] = v
	}
	for _, v := range right.Columns {
		cur[v.ID] = v
	}
	ids := historicalV2FieldIDs(old, cur)
	for _, id := range ids {
		a, had := old[id]
		z, has := cur[id]
		switch {
		case !had:
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
					if !historicalV2LegacyVarcharRepresentation(left, right, id, a, z) {
						return fmt.Errorf("physical v1->v2 field %s lacks the exact legacy bounded-string representation", id)
					}
					formatRepresentation = true
					risk = RiskRewrite
				} else if historicalV2SafeWidening(b.after.Provider.Provider, a.Storage, z.Storage) {
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

func (b *historicalV2DiffBuilder) requiredColumnBackfill(column physical.PhysicalColumn) error {
	id := string(column.ID)
	if err := b.add(AddColumn, 30, id, nil, column, RiskManual); err != nil {
		return err
	}
	if err := b.add(BackfillColumn, 31, id, nil, column, RiskManual); err != nil {
		return err
	}
	if err := b.add(ValidateConstraint, 32, id, nil, column, RiskSafe); err != nil {
		return err
	}
	return b.add(AlterColumnNullability, 33, id, true, false, RiskDataLoss)
}

func historicalV2LegacyVarcharRepresentation(beforeTable, afterTable physical.PhysicalTable, field ir.FieldID, before, after physical.PhysicalColumn) bool {
	if before.Storage.Kind != physical.StoragePostgreSQLText || before.Storage.Length != 0 || after.Storage.Kind != physical.StoragePostgreSQLVarchar || after.Storage.Length == 0 {
		return false
	}
	if !historicalV2SameColumnAcrossV1V2Representation(before, after) {
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

func historicalV2SameColumnAcrossV1V2Representation(before, after physical.PhysicalColumn) bool {
	left, right := before, after
	left.Storage, right.Storage = physical.StorageType{}, physical.StorageType{}
	leftDefault, rightDefault := left.Default.Expression, right.Default.Expression
	left.Default.Expression, right.Default.Expression = nil, nil
	leftGenerated, rightGenerated := left.Generated, right.Generated
	left.Generated, right.Generated = nil, nil
	if !reflect.DeepEqual(left, right) || !historicalV2SameRepresentationExpression(leftDefault, rightDefault) {
		return false
	}
	if leftGenerated == nil || rightGenerated == nil {
		return leftGenerated == nil && rightGenerated == nil
	}
	return leftGenerated.Kind == rightGenerated.Kind && historicalV2SameRepresentationExpression(&leftGenerated.Expression, &rightGenerated.Expression)
}

func historicalV2SameRepresentationExpression(before, after *physical.Expression) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	left, right := *before, *after
	leftOperands, rightOperands := left.Operands, right.Operands
	left.Operands, right.Operands = nil, nil
	leftType, rightType := left.Type, right.Type
	left.Type, right.Type = physical.StorageType{}, physical.StorageType{}
	if !reflect.DeepEqual(left, right) || !historicalV2SameRepresentationStorage(leftType, rightType) || len(leftOperands) != len(rightOperands) {
		return false
	}
	for index := range leftOperands {
		if !historicalV2SameRepresentationExpression(&leftOperands[index], &rightOperands[index]) {
			return false
		}
	}
	return true
}

func historicalV2SameRepresentationStorage(before, after physical.StorageType) bool {
	return reflect.DeepEqual(before, after) || (before.Kind == physical.StoragePostgreSQLText && before.Precision == 0 && before.Scale == 0 && before.Length == 0 && before.Symbol == nil && after.Kind == physical.StoragePostgreSQLVarchar && after.Precision == 0 && after.Scale == 0 && after.Length > 0 && after.Symbol == nil)
}

func (b *historicalV2DiffBuilder) objects(left, right physical.PhysicalTable) error {
	if err := b.key(left.ID, left.PrimaryKey, right.PrimaryKey, AddPrimaryKey, DropPrimaryKey, RiskDataLoss); err != nil {
		return err
	}
	if err := historicalV2DiffByID(left.Uniques, right.Uniques, func(v physical.PhysicalKey) string { return string(v.ID) }, func(v any, add bool) error {
		if add {
			return b.add(AddUnique, 40, historicalV2ObjectID(v), nil, v, RiskDataLoss)
		}
		return b.add(DropUnique, 60, historicalV2ObjectID(v), v, nil, RiskSafe)
	}, b); err != nil {
		return err
	}
	if err := historicalV2DiffByID(left.ForeignKeys, right.ForeignKeys, func(v physical.PhysicalForeignKey) string { return string(v.ID) }, func(v any, add bool) error {
		if add {
			return b.add(AddForeignKey, 50, historicalV2ObjectID(v), nil, v, RiskLocking)
		}
		return b.add(DropForeignKey, 60, historicalV2ObjectID(v), v, nil, RiskSafe)
	}, b); err != nil {
		return err
	}
	if err := historicalV2DiffByID(left.Checks, right.Checks, func(v physical.PhysicalCheck) string { return string(v.ID) }, func(v any, add bool) error {
		if add {
			return b.add(AddCheck, 40, historicalV2ObjectID(v), nil, v, RiskDataLoss)
		}
		return b.add(DropCheck, 60, historicalV2ObjectID(v), v, nil, RiskSafe)
	}, b); err != nil {
		return err
	}
	return b.indexes(left.Indexes, right.Indexes)
}

func (b *historicalV2DiffBuilder) indexes(before, after []physical.PhysicalIndex) error {
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
func (b *historicalV2DiffBuilder) recreateDestructiveDependents() error {
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

func (b *historicalV2DiffBuilder) recreateTableDependents(before, after physical.PhysicalTable, field ir.FieldID) error {
	if before.PrimaryKey != nil && historicalV2ObjectUsesField(before, *before.PrimaryKey, field) {
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
		if !historicalV2ObjectUsesField(before, value, field) {
			continue
		}
		if err := b.add(DropUnique, 60, string(value.ID), value, nil, RiskSafe); err != nil {
			return err
		}
		if restored, ok := historicalV2FindKey(after.Uniques, value.ID); ok {
			if err := b.add(AddUnique, 40, string(restored.ID), nil, restored, RiskDataLoss); err != nil {
				return err
			}
		}
	}
	for _, value := range before.ForeignKeys {
		if !historicalV2ObjectUsesField(before, value, field) {
			continue
		}
		if err := b.add(DropForeignKey, 60, string(value.ID), value, nil, RiskSafe); err != nil {
			return err
		}
		if restored, ok := historicalV2FindForeignKey(after.ForeignKeys, value.ID); ok {
			if err := b.add(AddForeignKey, 50, string(restored.ID), nil, restored, RiskLocking); err != nil {
				return err
			}
		}
	}
	for _, value := range before.Checks {
		if !historicalV2ObjectUsesField(before, value, field) {
			continue
		}
		if err := b.add(DropCheck, 60, string(value.ID), value, nil, RiskSafe); err != nil {
			return err
		}
		if restored, ok := historicalV2FindCheck(after.Checks, value.ID); ok {
			if err := b.add(AddCheck, 40, string(restored.ID), nil, restored, RiskDataLoss); err != nil {
				return err
			}
		}
	}
	for _, value := range before.Indexes {
		if !historicalV2ObjectUsesField(before, value, field) {
			continue
		}
		if err := b.add(DropIndex, 60, string(value.ID), value, nil, RiskSafe); err != nil {
			return err
		}
		if restored, ok := historicalV2FindIndex(after.Indexes, value.ID); ok {
			if err := b.add(CreateIndex, 40, string(restored.ID), nil, restored, RiskSafe); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *historicalV2DiffBuilder) key(table ir.ModelID, a, z *physical.PhysicalKey, add, drop OperationKind, risk Risk) error {
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

func (b *historicalV2DiffBuilder) add(kind OperationKind, stage uint16, id string, before, after any, risk Risk) error {
	a, err := historicalV2FragmentVersion(before, b.beforeCanonicalVersion)
	if err != nil {
		return err
	}
	z, err := historicalV2FragmentVersion(after, b.afterCanonicalVersion)
	if err != nil {
		return err
	}
	op := Operation{Kind: kind, Stage: stage, ObjectID: id, Before: a, After: z, Mode: Transactional, Risk: risk, LogicalPath: id}
	op.ID = historicalV2StableOperationID(kind, id, a, z)
	for _, existing := range b.operations {
		if existing.ID == op.ID {
			return nil
		}
	}
	b.operations = append(b.operations, op)
	return nil
}

func (b *historicalV2DiffBuilder) dependencies() {
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
		if !b.historicalReplay && op.Kind == AddColumn {
			if column := afterColumns[ir.FieldID(op.ObjectID)]; column.Generated != nil {
				for _, field := range historicalV2ExpressionFields(column.Generated.Expression) {
					addDep(op, AddColumn, string(field))
				}
			}
		}
		if op.Kind == ValidateConstraint {
			addDep(op, BackfillColumn, op.ObjectID)
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
				for _, field := range historicalV2ReferencedFields(table, op.ObjectID) {
					addDep(op, AddColumn, string(field))
					addDep(op, AlterColumnType, string(field))
					addDep(op, AlterColumnNullability, string(field))
				}
			}
		}
		if op.Kind == DropPrimaryKey || op.Kind == DropUnique {
			if owner, ok := tables[tableID]; ok {
				columns := historicalV2ReferencedFields(owner, op.ObjectID)
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
				for _, object := range historicalV2DependentObjects(table, ir.FieldID(op.ObjectID)) {
					for _, kind := range []OperationKind{DropForeignKey, DropIndex, DropCheck, DropUnique, DropPrimaryKey} {
						addDep(op, kind, object)
					}
				}
			}
		}
		if !b.historicalReplay && op.Kind == DropColumn {
			field := ir.FieldID(op.ObjectID)
			for generated, column := range beforeColumns {
				if column.Generated != nil && historicalV2ContainsField(historicalV2ExpressionFields(column.Generated.Expression), field) {
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
						if owner == tableID && historicalV2ContainsField(b.generatedInputs[dependent], field) {
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

func historicalV2ReferencedFields(table physical.PhysicalTable, id string) []ir.FieldID {
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
					out = append(out, historicalV2ExpressionFields(*key.Expression)...)
				}
			}
			out = append(out, v.Include...)
			if v.Predicate != nil {
				out = append(out, historicalV2ExpressionFields(*v.Predicate)...)
			}
			return out
		}
	}
	for _, v := range table.Checks {
		if string(v.ID) == id {
			return historicalV2ExpressionFields(v.Expression)
		}
	}
	return nil
}
func historicalV2DependentObjects(table physical.PhysicalTable, field ir.FieldID) []string {
	var out []string
	if table.PrimaryKey != nil && historicalV2ContainsField(table.PrimaryKey.Columns, field) {
		out = append(out, string(table.PrimaryKey.ID))
	}
	for _, v := range table.Uniques {
		if historicalV2ContainsField(v.Columns, field) {
			out = append(out, string(v.ID))
		}
	}
	for _, v := range table.ForeignKeys {
		if historicalV2ContainsField(v.Columns, field) || historicalV2ContainsField(v.ReferencedColumns, field) {
			out = append(out, string(v.ID))
		}
	}
	for _, v := range table.Indexes {
		if historicalV2ContainsField(v.Include, field) {
			out = append(out, string(v.ID))
			continue
		}
		for _, key := range v.Keys {
			if (key.Column != nil && *key.Column == field) || (key.Expression != nil && historicalV2ContainsField(historicalV2ExpressionFields(*key.Expression), field)) {
				out = append(out, string(v.ID))
				break
			}
		}
		if v.Predicate != nil && historicalV2ContainsField(historicalV2ExpressionFields(*v.Predicate), field) {
			out = append(out, string(v.ID))
		}
	}
	for _, v := range table.Checks {
		if historicalV2ContainsField(historicalV2ExpressionFields(v.Expression), field) {
			out = append(out, string(v.ID))
		}
	}
	return out
}

func historicalV2ExpressionFields(expression physical.Expression) []ir.FieldID {
	var out []ir.FieldID
	if expression.Column != nil {
		out = append(out, *expression.Column)
	}
	for _, operand := range expression.Operands {
		out = append(out, historicalV2ExpressionFields(operand)...)
	}
	return out
}

func historicalV2ObjectUsesField(_ physical.PhysicalTable, object any, field ir.FieldID) bool {
	switch value := object.(type) {
	case physical.PhysicalKey:
		return historicalV2ContainsField(value.Columns, field)
	case physical.PhysicalForeignKey:
		return historicalV2ContainsField(value.Columns, field) || historicalV2ContainsField(value.ReferencedColumns, field)
	case physical.PhysicalCheck:
		return historicalV2ContainsField(historicalV2ExpressionFields(value.Expression), field)
	case physical.PhysicalIndex:
		if historicalV2ContainsField(value.Include, field) {
			return true
		}
		if value.Predicate != nil && historicalV2ContainsField(historicalV2ExpressionFields(*value.Predicate), field) {
			return true
		}
		for _, key := range value.Keys {
			if key.Column != nil && *key.Column == field {
				return true
			}
			if key.Expression != nil && historicalV2ContainsField(historicalV2ExpressionFields(*key.Expression), field) {
				return true
			}
		}
	}
	return false
}

func historicalV2FindKey(values []physical.PhysicalKey, id ir.KeyID) (physical.PhysicalKey, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return physical.PhysicalKey{}, false
}

func historicalV2FindForeignKey(values []physical.PhysicalForeignKey, id ir.ForeignKeyID) (physical.PhysicalForeignKey, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return physical.PhysicalForeignKey{}, false
}

func historicalV2FindCheck(values []physical.PhysicalCheck, id ir.CheckID) (physical.PhysicalCheck, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return physical.PhysicalCheck{}, false
}

func historicalV2FindIndex(values []physical.PhysicalIndex, id ir.IndexID) (physical.PhysicalIndex, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return physical.PhysicalIndex{}, false
}
func historicalV2ContainsField(values []ir.FieldID, field ir.FieldID) bool {
	for _, value := range values {
		if value == field {
			return true
		}
	}
	return false
}

func historicalV2Fragment(value any) (Digest, error) {
	return historicalV2FragmentVersion(value, 2)
}

func historicalV2FragmentVersion(value any, version uint32) (Digest, error) {
	if value == nil {
		return "", nil
	}
	var encoded []byte
	var err error
	switch version {
	case 1:
		encoded, err = physical.HistoricalV1CanonicalFragment(value)
	case 2:
		encoded, err = physical.HistoricalV2CanonicalFragment(value)
	default:
		return "", fmt.Errorf("frozen physical fragment version %d is unsupported", version)
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return Digest(hex.EncodeToString(sum[:])), nil
}
func historicalV2StableOperationID(kind OperationKind, id string, before, after Digest) OperationID {
	sum := sha256.Sum256([]byte(string(kind) + "\x00" + id + "\x00" + string(before) + "\x00" + string(after)))
	return OperationID(hex.EncodeToString(sum[:16]))
}

func historicalV2DiffByID[T any](a, z []T, id func(T) string, emit func(any, bool) error, _ *historicalV2DiffBuilder) error {
	old := map[string]T{}
	cur := map[string]T{}
	for _, v := range a {
		old[id(v)] = v
	}
	for _, v := range z {
		cur[id(v)] = v
	}
	ids := historicalV2StringIDs(old, cur)
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
func historicalV2ObjectID(v any) string {
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
func historicalV2ModelIDs[A, B any](a map[ir.ModelID]A, b map[ir.ModelID]B) []ir.ModelID {
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
func historicalV2FieldIDs[A, B any](a map[ir.FieldID]A, b map[ir.FieldID]B) []ir.FieldID {
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
func historicalV2StringIDs[T any](a, b map[string]T) []string {
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

func historicalV2SafeWidening(provider ir.Provider, before, after physical.StorageType) bool {
	if provider != ir.PostgreSQL || before.Symbol != nil || after.Symbol != nil || reflect.DeepEqual(before, after) {
		return false
	}
	switch before.Kind {
	case physical.StoragePostgreSQLSmallInt:
		return historicalV2UnparameterizedPair(before, after) && (after.Kind == physical.StoragePostgreSQLInteger || after.Kind == physical.StoragePostgreSQLBigInt)
	case physical.StoragePostgreSQLInteger:
		return historicalV2UnparameterizedPair(before, after) && after.Kind == physical.StoragePostgreSQLBigInt
	case physical.StoragePostgreSQLReal:
		return historicalV2UnparameterizedPair(before, after) && after.Kind == physical.StoragePostgreSQLDouble
	case physical.StoragePostgreSQLVarchar:
		if before.Precision != 0 || before.Scale != 0 || before.Length == 0 || after.Precision != 0 || after.Scale != 0 {
			return false
		}
		return after.Kind == physical.StoragePostgreSQLText && after.Length == 0 || after.Kind == physical.StoragePostgreSQLVarchar && after.Length >= before.Length
	case physical.StoragePostgreSQLNumeric:
		if after.Kind != physical.StoragePostgreSQLNumeric || before.Length != 0 || after.Length != 0 || before.Precision == 0 || after.Precision == 0 || before.Scale > before.Precision {
			return false
		}
		return after.Scale == before.Scale && after.Precision >= before.Precision
	case physical.StoragePostgreSQLTime, physical.StoragePostgreSQLTimestampTZ:
		if after.Kind != before.Kind || before.Precision != 0 || after.Precision != 0 || before.Scale != 0 || after.Scale != 0 {
			return false
		}
		return after.Length >= before.Length && after.Length <= historicalV2TemporalPrecision
	default:
		return false
	}
}

func historicalV2UnparameterizedPair(values ...physical.StorageType) bool {
	for _, value := range values {
		if value.Precision != 0 || value.Scale != 0 || value.Length != 0 {
			return false
		}
	}
	return true
}
