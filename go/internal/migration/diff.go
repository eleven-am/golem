package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

// Diff compares validated normalized physical schemas by stable IDs. It never
// infers a rename from spelling similarity.
func Diff(before, after physical.PhysicalSchema) (Plan, error) {
	left, err := physical.Normalize(before)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize before: %w", err)
	}
	right, err := physical.Normalize(after)
	if err != nil {
		return Plan{}, fmt.Errorf("normalize after: %w", err)
	}
	if left.Provider.Provider != right.Provider.Provider {
		return Plan{}, fmt.Errorf("cannot diff providers %s and %s", left.Provider.Provider, right.Provider.Provider)
	}
	leftFP, err := physical.PhysicalFingerprint(left)
	if err != nil {
		return Plan{}, err
	}
	rightFP, err := physical.PhysicalFingerprint(right)
	if err != nil {
		return Plan{}, err
	}
	builder := diffBuilder{before: left, after: right}
	beforeSystem, err := physical.SystemFingerprint(left.Provider, left.System)
	if err != nil {
		return Plan{}, err
	}
	afterSystem, err := physical.SystemFingerprint(right.Provider, right.System)
	if err != nil {
		return Plan{}, err
	}
	initial := false
	if beforeSystem != afterSystem {
		if emptySystemSchema(left.System) && len(left.Tables) == 0 && len(left.Extensions) == 0 && len(left.Unmanaged) == 0 && len(right.System.Objects) != 0 {
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
	return plan, nil
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
	before, after physical.PhysicalSchema
	operations    []Operation
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
			risk := RiskSafe
			if !z.Nullable && z.Default.Kind == physical.DefaultNone {
				risk = RiskManual
			}
			if err := b.add(AddColumn, 30, string(id), nil, z, risk); err != nil {
				return err
			}
		case !has:
			if err := b.add(DropColumn, 70, string(id), a, nil, RiskDataLoss); err != nil {
				return err
			}
		default:
			if a.Name != z.Name {
				if err := b.add(RenameColumn, 35, string(id), a.Name, z.Name, RiskSafe); err != nil {
					return err
				}
			}
			if !reflect.DeepEqual(a.Storage, z.Storage) {
				if err := b.add(AlterColumnType, 45, string(id), a.Storage, z.Storage, RiskDataLoss); err != nil {
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
			if !reflect.DeepEqual(a.Generated, z.Generated) || !reflect.DeepEqual(a.Collation, z.Collation) {
				if err := b.add(RebuildTable, 55, string(left.ID), left, right, RiskRewrite); err != nil {
					return err
				}
			}
		}
	}
	return nil
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
	a, err := fragment(before)
	if err != nil {
		return err
	}
	z, err := fragment(after)
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
	objects := map[string]ir.ModelID{}
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
		tableID := columnTable[op.ObjectID]
		if tableID == "" {
			tableID = objects[op.ObjectID]
		}
		if tableID != "" && op.Kind != CreateTable {
			addDep(op, CreateTable, string(tableID))
			addDep(op, RenameTable, string(tableID))
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
			for _, table := range tables {
				for _, object := range dependentObjects(table, ir.FieldID(op.ObjectID)) {
					for _, kind := range []OperationKind{DropForeignKey, DropIndex, DropCheck, DropUnique, DropPrimaryKey} {
						addDep(op, kind, object)
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

func fragment(value any) (Digest, error) {
	if value == nil {
		return "", nil
	}
	encoded, err := physical.CanonicalFragment(value)
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
