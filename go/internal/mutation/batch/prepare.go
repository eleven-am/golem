package batch

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
)

// PrepareCaptured is intentionally a value-receiving phase boundary. If the
// capture contains the +1 sentinel it returns LIMIT_EXCEEDED and no Prepared
// value, so no caller can obtain a truncated write program.
func (program Program) PrepareCaptured(rows []mutationdecode.Row) (Prepared, error) {
	context := program.context
	if uint64(len(rows)) > uint64(program.maxRows) {
		return Prepared{}, fail(CodeLimit, context.node.ModelID(), policyir.FieldID{}, fmt.Sprintf("captured %d rows; maximum is %d", len(rows), program.maxRows), nil)
	}
	identities := make([][]any, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for index, row := range rows {
		if row.ModelID() != context.node.ModelID() {
			return Prepared{}, fail(CodeSet, context.node.ModelID(), policyir.FieldID{}, fmt.Sprintf("captured row %d belongs to another model", index), nil)
		}
		complete, completeErr := row.IsComplete(context.registry)
		if completeErr != nil || !complete {
			return Prepared{}, fail(CodeSet, context.node.ModelID(), policyir.FieldID{}, fmt.Sprintf("captured row %d is not a complete scalar image", index), completeErr)
		}
		values, key, err := context.encodedPrimary(row)
		if err != nil {
			return Prepared{}, err
		}
		if _, duplicate := seen[key]; duplicate {
			return Prepared{}, fail(CodeSet, context.node.ModelID(), policyir.FieldID{}, "captured primary identity appears more than once", nil)
		}
		seen[key] = struct{}{}
		identities[index] = values
	}
	prepared := Prepared{operation: context.node.Operation(), before: append([]mutationdecode.Row(nil), rows...), context: context}
	if len(rows) == 0 {
		return prepared, nil
	}

	chunkSize, err := context.chunkSize()
	if err != nil {
		return Prepared{}, err
	}
	if context.plan.Stance() == mutationir.Caller {
		for start := 0; start < len(rows); start += chunkSize {
			end := start + chunkSize
			if end > len(rows) {
				end = len(rows)
			}
			statement, renderErr := context.renderAuthorize(identities[start:end])
			if renderErr != nil {
				return Prepared{}, renderErr
			}
			prepared.statements = append(prepared.statements, statement)
		}
	}
	for start := 0; start < len(rows); start += chunkSize {
		end := start + chunkSize
		if end > len(rows) {
			end = len(rows)
		}
		var statement Statement
		if context.node.Operation() == mutationir.UpdateMany {
			statement, err = context.renderUpdate(identities[start:end])
		} else {
			statement, err = context.renderDelete(identities[start:end])
		}
		if err != nil {
			return Prepared{}, err
		}
		prepared.statements = append(prepared.statements, statement)
	}
	if context.node.Operation() == mutationir.UpdateMany {
		for start := 0; start < len(rows); start += chunkSize {
			end := start + chunkSize
			if end > len(rows) {
				end = len(rows)
			}
			statement, renderErr := context.renderRehydrate(identities[start:end])
			if renderErr != nil {
				return Prepared{}, renderErr
			}
			prepared.statements = append(prepared.statements, statement)
		}
	}
	return prepared, nil
}

// renderAuthorize rechecks the captured action constraint and precomputes every
// authored-field grant against a complete locked pre-image before the first
// write statement. Exact logical before/after decoding later decides which
// grants are required; provider physical equality is never authorization
// truth. Running all authorization chunks first prevents an earlier batch
// write from becoming another row's authorization image.
func (context renderContext) renderAuthorize(identities [][]any) (Statement, error) {
	bindings := make([]Binding, 0)
	identitySQL, identityBindings, err := context.identitiesWhere(identities, 0)
	if err != nil {
		return Statement{}, err
	}
	bindings = append(bindings, identityBindings...)
	constraint, err := context.selectingConstraint()
	if err != nil {
		return Statement{}, err
	}
	selection, err := context.compile(constraint, len(bindings))
	if err != nil {
		return Statement{}, err
	}
	bindings = append(bindings, selection.bindings...)
	parts := []string{"(" + identitySQL + ")", "(" + selection.text + ")"}
	selects, columns, err := context.completeColumns()
	if err != nil {
		return Statement{}, err
	}
	authorizations := make([]AuthorizationColumn, 0, len(context.node.FieldAuthorizations()))
	for _, authorization := range context.node.FieldAuthorizations() {
		condition, compileErr := context.compile(authorization.Condition(), len(bindings))
		if compileErr != nil {
			return Statement{}, compileErr
		}
		bindings = append(bindings, condition.bindings...)
		alias := fmt.Sprintf("golem_auth_%x", authorization.FieldID())
		selects = append(selects, "CASE WHEN ("+condition.text+") THEN 1 ELSE 0 END AS "+context.dialect.Quote(physical.PhysicalName(alias)))
		authorizations = append(authorizations, AuthorizationColumn{field: authorization.FieldID(), alias: alias})
	}
	lock := ""
	if context.provider == policyir.ProviderPostgreSQL {
		lock = " FOR UPDATE"
	}
	statement := Statement{role: AuthorizePreImage, text: "SELECT " + strings.Join(selects, ", ") + " FROM " + context.dialect.Table(context.model) + " AS " + context.dialect.Quote(context.alias) + " WHERE " + strings.Join(parts, " AND ") + " ORDER BY " + context.orderBy() + lock, bindings: bindings, columns: columns, authorizations: authorizations, cardinality: ExactlyCapturedRows, expected: uint32(len(identities))}
	return context.check(statement)
}

func (context renderContext) chunkSize() (int, error) {
	max := int(context.plan.Bounds().MaxParameters())
	fixed := 0
	if context.node.Operation() == mutationir.UpdateMany {
		for _, operation := range context.node.ScalarOperations() {
			if operation.Kind() != mutationir.ScalarNull {
				fixed++
			}
		}
	}
	constraint, err := context.selectingConstraint()
	if err != nil {
		return 0, err
	}
	fragment, err := context.compile(constraint, fixed)
	if err != nil {
		return 0, err
	}
	updateFixed := fixed + len(fragment.bindings)
	for _, authorization := range context.node.FieldAuthorizations() {
		fragment, compileErr := context.compile(authorization.Condition(), updateFixed)
		if compileErr != nil {
			return 0, compileErr
		}
		updateFixed += len(fragment.bindings)
	}
	postFixed := 0
	if condition, ok := context.node.RowPostcondition(); ok {
		fragment, compileErr := context.compile(condition, 0)
		if compileErr != nil {
			return 0, compileErr
		}
		postFixed = len(fragment.bindings)
	}
	if postFixed > updateFixed {
		fixed = postFixed
	} else {
		fixed = updateFixed
	}
	if len(context.primary) == 0 || max <= fixed {
		return 0, fail(CodeLimit, context.node.ModelID(), policyir.FieldID{}, "parameter bound cannot hold one captured identity", nil)
	}
	rows := (max - fixed) / len(context.primary)
	if rows < 1 {
		return 0, fail(CodeLimit, context.node.ModelID(), policyir.FieldID{}, "parameter bound cannot hold one captured identity", nil)
	}
	if rows > int(context.plan.Bounds().MaxRows()) {
		rows = int(context.plan.Bounds().MaxRows())
	}
	return rows, nil
}

func (context renderContext) renderUpdate(identities [][]any) (Statement, error) {
	operations := context.node.ScalarOperations()
	assignments := make([]string, len(operations))
	bindings := make([]Binding, 0)
	for index, operation := range operations {
		field, _ := context.resolver.Field(context.provider, context.node.ModelID(), operation.FieldID())
		column := context.dialect.Quote(field.Column)
		expression, err := context.scalarExpression(operation, column, &bindings)
		if err != nil {
			return Statement{}, err
		}
		assignments[index] = column + " = " + expression
	}
	if len(assignments) == 0 {
		return Statement{}, fail(CodeInput, context.node.ModelID(), policyir.FieldID{}, "update-many has no scalar operations", nil)
	}
	identitySQL, identityBindings, err := context.identitiesWhere(identities, len(bindings))
	if err != nil {
		return Statement{}, err
	}
	bindings = append(bindings, identityBindings...)
	returning, columns, err := context.returning()
	if err != nil {
		return Statement{}, err
	}
	statement := Statement{role: ApplyUpdate, text: "UPDATE " + context.dialect.Table(context.model) + " AS " + context.dialect.Quote(context.alias) + " SET " + strings.Join(assignments, ", ") + " WHERE " + identitySQL + " RETURNING " + returning, bindings: bindings, columns: columns, cardinality: ExactlyCapturedRows, expected: uint32(len(identities))}
	return context.check(statement)
}

func (context renderContext) renderDelete(identities [][]any) (Statement, error) {
	identitySQL, bindings, err := context.identitiesWhere(identities, 0)
	if err != nil {
		return Statement{}, err
	}
	returning, columns, err := context.returning()
	if err != nil {
		return Statement{}, err
	}
	statement := Statement{role: ApplyDelete, text: "DELETE FROM " + context.dialect.Table(context.model) + " AS " + context.dialect.Quote(context.alias) + " WHERE " + identitySQL + " RETURNING " + returning, bindings: bindings, columns: columns, cardinality: ExactlyCapturedRows, expected: uint32(len(identities))}
	return context.check(statement)
}

func (context renderContext) renderRehydrate(identities [][]any) (Statement, error) {
	identitySQL, bindings, err := context.identitiesWhere(identities, 0)
	if err != nil {
		return Statement{}, err
	}
	parts := []string{"(" + identitySQL + ")"}
	if condition, ok := context.node.RowPostcondition(); ok {
		postcondition, compileErr := context.compile(condition, len(bindings))
		if compileErr != nil {
			return Statement{}, compileErr
		}
		bindings = append(bindings, postcondition.bindings...)
		parts = append(parts, "("+postcondition.text+")")
	}
	fields, columns, err := context.completeColumns()
	if err != nil {
		return Statement{}, err
	}
	statement := Statement{role: RehydrateAfterImage, text: "SELECT " + strings.Join(fields, ", ") + " FROM " + context.dialect.Table(context.model) + " AS " + context.dialect.Quote(context.alias) + " WHERE " + strings.Join(parts, " AND ") + " ORDER BY " + context.orderBy(), bindings: bindings, columns: columns, cardinality: ExactlyCapturedRows, expected: uint32(len(identities))}
	return context.check(statement)
}

func (context renderContext) selectingConstraint() (policyir.Condition, error) {
	if selection, ok := context.node.SelectionRequirement(); ok {
		return selection.Constraint(), nil
	}
	if context.plan.Stance() == mutationir.System {
		if predicate, ok := context.node.Predicate(); ok {
			return predicate, nil
		}
	}
	return policyir.Condition{}, fail(CodeInput, context.node.ModelID(), policyir.FieldID{}, "complete selecting constraint is absent", nil)
}

func (context renderContext) scalarExpression(operation mutationir.ScalarOperation, column string, bindings *[]Binding) (string, error) {
	if operation.Kind() == mutationir.ScalarNull {
		return "NULL", nil
	}
	value, ok := operation.Value()
	if !ok {
		return "", fail(CodeInput, context.node.ModelID(), operation.FieldID(), "scalar operand is absent", nil)
	}
	encoded, err := context.encode(value, operation.Type())
	if err != nil {
		return "", fail(CodeInput, context.node.ModelID(), operation.FieldID(), "scalar operand cannot be encoded", err)
	}
	*bindings = append(*bindings, Binding{value: encoded})
	placeholder := context.dialect.Placeholder(len(*bindings))
	switch operation.Kind() {
	case mutationir.ScalarSet:
		return placeholder, nil
	case mutationir.ScalarIncrement:
		return "(" + column + " + " + placeholder + ")", nil
	case mutationir.ScalarDecrement:
		return "(" + column + " - " + placeholder + ")", nil
	default:
		return "", fail(CodeInput, context.node.ModelID(), operation.FieldID(), "unknown scalar operation", nil)
	}
}

func (context renderContext) identitiesWhere(identities [][]any, offset int) (string, []Binding, error) {
	if len(identities) == 0 {
		return "", nil, fail(CodeSet, context.node.ModelID(), policyir.FieldID{}, "identity chunk is empty", nil)
	}
	bindings := make([]Binding, 0, len(identities)*len(context.primary))
	rows := make([]string, len(identities))
	for rowIndex, values := range identities {
		if len(values) != len(context.primary) {
			return "", nil, fail(CodeSet, context.node.ModelID(), policyir.FieldID{}, "captured identity width changed", nil)
		}
		parts := make([]string, len(values))
		for index, value := range values {
			field, _ := context.resolver.Field(context.provider, context.node.ModelID(), context.primary[index])
			bindings = append(bindings, Binding{value: value})
			parts[index] = context.qualified(field.Column) + " = " + context.dialect.Placeholder(offset+len(bindings))
		}
		rows[rowIndex] = "(" + strings.Join(parts, " AND ") + ")"
	}
	return strings.Join(rows, " OR "), bindings, nil
}

func (context renderContext) encodedPrimary(row mutationdecode.Row) ([]any, string, error) {
	values := make([]any, len(context.primary))
	var key strings.Builder
	for index, fieldID := range context.primary {
		cell, ok := row.Cell(fieldID)
		if !ok || cell.IsNull() {
			return nil, "", fail(CodeSet, context.node.ModelID(), fieldID, "captured primary-key component is absent or NULL", nil)
		}
		value, ok := cell.PolicyValue()
		if !ok {
			return nil, "", fail(CodeSet, context.node.ModelID(), fieldID, "captured primary-key component has no exact value", nil)
		}
		field, ok := context.resolver.Field(context.provider, context.node.ModelID(), fieldID)
		if !ok {
			return nil, "", fail(CodeSchema, context.node.ModelID(), fieldID, "primary-key physical field is absent", nil)
		}
		encoded, err := context.encode(value, field.Type)
		if err != nil {
			return nil, "", fail(CodeSet, context.node.ModelID(), fieldID, "captured primary-key value cannot be encoded", err)
		}
		values[index] = encoded
		appendKey(&key, encoded)
	}
	return values, key.String(), nil
}

func (context renderContext) encode(value policyir.Value, typ policyir.TypeRef) (any, error) {
	bound := policysql.BoundValue{Value: value, Type: typ}
	if typ.Kind() == policyir.ValueEnum {
		enum, member, _ := value.Enum()
		wire, ok := context.resolver.EnumWire(enum, member)
		if !ok {
			return nil, fmt.Errorf("enum wire is absent")
		}
		bound.EnumWires = []string{wire}
	} else if typ.Kind() == policyir.ValueScalarList {
		element, _ := typ.Element()
		if element.Kind() == policyir.ValueEnum {
			items, _ := value.List()
			bound.EnumWires = make([]string, len(items))
			for index, item := range items {
				enum, member, ok := item.Enum()
				if !ok {
					return nil, fmt.Errorf("enum list contains another kind")
				}
				wire, found := context.resolver.EnumWire(enum, member)
				if !found {
					return nil, fmt.Errorf("enum list wire is absent")
				}
				bound.EnumWires[index] = wire
			}
		}
	}
	return context.dialect.Encode(bound)
}

func (context renderContext) returning() (string, []ResultColumn, error) {
	model, _ := context.registry.Model(golem.ModelID(context.node.ModelID()))
	var fields []string
	var columns []ResultColumn
	for _, publicID := range model.Fields() {
		fieldID := policyir.FieldID(publicID)
		field, ok := context.resolver.Field(context.provider, context.node.ModelID(), fieldID)
		if !ok {
			continue
		}
		alias := fmt.Sprintf("golem_f_%x", fieldID)
		fields = append(fields, context.dialect.Quote(field.Column)+" AS "+context.dialect.Quote(physical.PhysicalName(alias)))
		columns = append(columns, ResultColumn{field: fieldID, alias: alias})
	}
	if len(fields) == 0 {
		return "", nil, fail(CodeSchema, context.node.ModelID(), policyir.FieldID{}, "model has no persisted scalar fields", nil)
	}
	return strings.Join(fields, ", "), columns, nil
}

func (context renderContext) orderBy() string {
	parts := make([]string, len(context.primary))
	for index, fieldID := range context.primary {
		field, _ := context.resolver.Field(context.provider, context.node.ModelID(), fieldID)
		parts[index] = context.qualified(field.Column) + " ASC"
	}
	return strings.Join(parts, ", ")
}

func (context renderContext) check(statement Statement) (Statement, error) {
	if uint32(len(statement.bindings)) > context.plan.Bounds().MaxParameters() {
		return Statement{}, fail(CodeLimit, context.node.ModelID(), policyir.FieldID{}, "prepared statement exceeds parameter bound", nil)
	}
	return statement, nil
}

func appendKey(builder *strings.Builder, value any) {
	builder.WriteString(reflect.TypeOf(value).String())
	builder.WriteByte(0)
	var data [8]byte
	switch typed := value.(type) {
	case bool:
		if typed {
			builder.WriteByte(1)
		} else {
			builder.WriteByte(0)
		}
	case int16:
		binary.BigEndian.PutUint16(data[:2], uint16(typed))
		builder.Write(data[:2])
	case int32:
		binary.BigEndian.PutUint32(data[:4], uint32(typed))
		builder.Write(data[:4])
	case int64:
		binary.BigEndian.PutUint64(data[:], uint64(typed))
		builder.Write(data[:])
	case float32:
		binary.BigEndian.PutUint32(data[:4], math.Float32bits(typed))
		builder.Write(data[:4])
	case float64:
		binary.BigEndian.PutUint64(data[:], math.Float64bits(typed))
		builder.Write(data[:])
	case string:
		binary.BigEndian.PutUint64(data[:], uint64(len(typed)))
		builder.Write(data[:])
		builder.WriteString(typed)
	case []byte:
		binary.BigEndian.PutUint64(data[:], uint64(len(typed)))
		builder.Write(data[:])
		builder.Write(typed)
	case time.Time:
		builder.WriteString(typed.UTC().Format(time.RFC3339Nano))
	default:
		builder.WriteString(fmt.Sprintf("%v", typed))
	}
	builder.WriteByte(0xff)
}
