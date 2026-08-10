package operation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlscalar "github.com/eleven-am/golem/go/internal/graphql/scalar"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
)

type ComputedResolver func(context.Context, compilerir.ModelID, []golem.RuntimeModelRow, selectset.Slot) ([]any, error)

// ComputedFieldFailure is an occurrence-local resolver or result-encoding
// failure. Path is the exact GraphQL response path, including aliases and list
// indices. The public transport turns these into prepared gqlgen field errors;
// structural encoder failures still use the ordinary error return.
type ComputedFieldFailure struct {
	Path             []any
	Err              error
	PropagatesToRoot bool
}

// indexedComputedFailures is implemented by the execution-scoped computed
// runner when individual sibling rows fail independently.
type indexedComputedFailures interface {
	ComputedFieldFailures() map[int]error
}

// EncodeMutationWithComputedPartial encodes a P4 row result through the same
// occurrence-aware computed path as reads. Batch mutation payloads have no
// model selections and therefore retain the ordinary mutation encoder.
func (c *Compiler) EncodeMutationWithComputedPartial(ctx context.Context, root MutationRoot, result golem.RuntimeMutationResult, resolve ComputedResolver) (any, []ComputedFieldFailure, error) {
	if c == nil || resolve == nil {
		return nil, nil, fmt.Errorf("P5_MUTATION_COMPUTED_ENCODE: compiler and resolver are required")
	}
	if _, ok := result.Count(); ok {
		encoded, err := c.EncodeMutation(root, result)
		return encoded, nil, err
	}
	row, ok := result.Row()
	if !ok {
		return nil, nil, fmt.Errorf("P5_MUTATION_COMPUTED_ENCODE: row result is absent")
	}
	encoded, failures, err := c.encodeComputedRowsPartial(ctx, root.Model, []golem.RuntimeModelRow{row}, root.Slots, resolve, [][]any{{root.ResponseName}})
	if err != nil {
		return nil, nil, err
	}
	if len(encoded) != 1 {
		return nil, nil, fmt.Errorf("P5_MUTATION_COMPUTED_ENCODE: row result encoded with cardinality %d", len(encoded))
	}
	return encoded[0], failures, nil
}

func (c *Compiler) EncodeCustomWithComputed(ctx context.Context, root CustomRoot, value any, resolve ComputedResolver) (any, error) {
	if c == nil || resolve == nil {
		return nil, fmt.Errorf("P5_CUSTOM_ENCODE: compiler and resolver are required")
	}
	encoded, failures, err := c.EncodeCustomWithComputedPartial(ctx, root, value, resolve)
	if err != nil {
		return nil, err
	}
	if len(failures) != 0 {
		return nil, failures[0].Err
	}
	return encoded, nil
}

// EncodeCustomWithComputedPartial preserves authorized sibling data when a
// computed occurrence fails. gqlgen later applies the declared nullability.
func (c *Compiler) EncodeCustomWithComputedPartial(ctx context.Context, root CustomRoot, value any, resolve ComputedResolver) (any, []ComputedFieldFailure, error) {
	if c == nil || resolve == nil {
		return nil, nil, fmt.Errorf("P5_CUSTOM_ENCODE: compiler and resolver are required")
	}
	return c.encodeCustomPreparedValuePartial(ctx, root.Result, value, root.Slots, resolve, []any{root.ResponseName})
}

func (c *Compiler) encodeCustomPreparedValuePartial(ctx context.Context, typ compilerir.GraphQLTypeIR, value any, children []selectset.Slot, resolve ComputedResolver, path []any) (any, []ComputedFieldFailure, error) {
	if value == nil {
		return c.encodeComputedValuePartial(ctx, typ, nil, children, resolve, path)
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return c.encodeComputedValuePartial(ctx, typ, nil, children, resolve, path)
		}
		value, reflected = reflected.Elem().Interface(), reflected.Elem()
	}
	if typ.Kind == compilerir.GraphQLTypeList {
		if typ.Element == nil || reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
			return nil, nil, fmt.Errorf("custom list has value %T", value)
		}
		result := make([]any, reflected.Len())
		var failures []ComputedFieldFailure
		for index := range result {
			encoded, childFailures, err := c.encodeCustomPreparedValuePartial(ctx, *typ.Element, reflected.Index(index).Interface(), children, resolve, appendPath(path, index))
			if err != nil {
				result[index] = nil
				failures = append(failures, ComputedFieldFailure{Path: appendPath(path, index), Err: fmt.Errorf("custom list item %d: %w", index, err), PropagatesToRoot: !typ.Element.Nullable && !typ.Nullable})
				continue
			}
			result[index] = encoded
			failures = append(failures, propagateThroughList(childFailures, *typ.Element, typ)...)
		}
		return result, failures, nil
	}
	if typ.Kind == compilerir.GraphQLTypeEnum {
		name, ok := value.(string)
		if !ok {
			if reflected.IsValid() && reflected.Kind() == reflect.String {
				name, ok = reflected.String(), true
			}
		}
		if !ok || !c.graphQLEnumNameDeclared(typ.Name, name) {
			return nil, nil, fmt.Errorf("custom enum %s has invalid GraphQL value", typ.Name)
		}
		return name, nil, nil
	}
	return c.encodeComputedValuePartial(ctx, typ, value, children, resolve, path)
}

// EncodeReadWithComputed performs column-wise encoding so one computed slot can
// batch all sibling rows before values are serialized.
func (c *Compiler) EncodeReadWithComputed(ctx context.Context, root ReadRoot, rows []golem.RuntimeModelRow, resolve ComputedResolver) (any, error) {
	if c == nil || resolve == nil {
		return nil, fmt.Errorf("P5_COMPUTED_ENCODE: compiler and resolver are required")
	}
	encoded, failures, err := c.EncodeReadWithComputedPartial(ctx, root, rows, resolve)
	if err != nil {
		return nil, err
	}
	if len(failures) != 0 {
		return nil, failures[0].Err
	}
	return encoded, nil
}

// EncodeReadWithComputedPartial returns prepared data plus occurrence-local
// computed failures. It never places an error sentinel in the public data.
func (c *Compiler) EncodeReadWithComputedPartial(ctx context.Context, root ReadRoot, rows []golem.RuntimeModelRow, resolve ComputedResolver) (any, []ComputedFieldFailure, error) {
	if c == nil || resolve == nil {
		return nil, nil, fmt.Errorf("P5_COMPUTED_ENCODE: compiler and resolver are required")
	}
	paths := make([][]any, len(rows))
	for index := range rows {
		paths[index] = []any{root.ResponseName}
		if root.Operation != readir.FindUnique {
			paths[index] = append(paths[index], index)
		}
	}
	encoded, failures, err := c.encodeComputedRowsPartial(ctx, root.Model, rows, root.Slots, resolve, paths)
	if err != nil {
		return nil, nil, err
	}
	if root.Operation == readir.FindUnique {
		if len(encoded) == 0 {
			return nil, failures, nil
		}
		if len(encoded) != 1 {
			return nil, nil, fmt.Errorf("P5_ENCODE_CARDINALITY: find-one returned %d rows", len(encoded))
		}
		return encoded[0], failures, nil
	}
	result := make([]any, len(encoded))
	for index := range encoded {
		result[index] = encoded[index]
	}
	return result, failures, nil
}

func (c *Compiler) encodeComputedRows(ctx context.Context, modelID compilerir.ModelID, rows []golem.RuntimeModelRow, slots []selectset.Slot, resolve ComputedResolver) ([]map[string]any, error) {
	paths := make([][]any, len(rows))
	encoded, failures, err := c.encodeComputedRowsPartial(ctx, modelID, rows, slots, resolve, paths)
	if err != nil {
		return nil, err
	}
	if len(failures) != 0 {
		return nil, failures[0].Err
	}
	return encoded, nil
}

func (c *Compiler) encodeComputedRowsPartial(ctx context.Context, modelID compilerir.ModelID, rows []golem.RuntimeModelRow, slots []selectset.Slot, resolve ComputedResolver, paths [][]any) ([]map[string]any, []ComputedFieldFailure, error) {
	model, contract, ok := c.model(modelID)
	if !ok {
		return nil, nil, fmt.Errorf("model %s is absent or unexposed", modelID)
	}
	publicModel, err := publicModelID(modelID)
	if err != nil {
		return nil, nil, err
	}
	if len(paths) != len(rows) {
		return nil, nil, fmt.Errorf("computed response paths do not match rows")
	}
	fields := make(map[compilerir.FieldID]compilerir.FieldIR, len(model.Fields))
	for _, field := range model.Fields {
		fields[field.ID] = field
	}
	result := make([]map[string]any, len(rows))
	for index, row := range rows {
		if row.ModelID() != publicModel {
			return nil, nil, fmt.Errorf("runtime row %d model does not match %s", index, modelID)
		}
		result[index] = make(map[string]any, len(slots))
	}
	var failures []ComputedFieldFailure
	for _, slot := range slots {
		switch slot.Kind {
		case selectset.SlotTypename:
			for index := range result {
				result[index][slot.ResponseName] = contract.GraphQLName
			}
		case selectset.SlotScalar:
			field, present := fields[slot.FieldID]
			if !present || field.Scalar == nil || field.Kind == compilerir.FieldRelation {
				return nil, nil, fmt.Errorf("scalar slot %s has no scalar field", slot.ResponseName)
			}
			fieldID, idErr := publicFieldID(slot.FieldID)
			if idErr != nil {
				return nil, nil, idErr
			}
			for index, row := range rows {
				value, valueErr := c.encodeCell(golem.RuntimeTransportField(row, fieldID), field.Scalar.Type)
				if valueErr != nil {
					return nil, nil, fmt.Errorf("field %s row %d: %w", slot.ResponseName, index, valueErr)
				}
				result[index][slot.ResponseName] = value
			}
		case selectset.SlotRelation:
			field, present := fields[slot.FieldID]
			if !present || field.Relation == nil {
				return nil, nil, fmt.Errorf("relation slot %s has no relation field", slot.ResponseName)
			}
			target, targetErr := c.relationTarget(modelID, field)
			if targetErr != nil {
				return nil, nil, targetErr
			}
			fieldID, idErr := publicFieldID(slot.FieldID)
			if idErr != nil {
				return nil, nil, idErr
			}
			type relationRange struct {
				start, count int
				many, null   bool
			}
			ranges := make([]relationRange, len(rows))
			var flattened []golem.RuntimeModelRow
			var flattenedPaths [][]any
			for index, row := range rows {
				cell := golem.RuntimeTransportOccurrence(row, fieldID, golem.RuntimeOccurrenceID(slot.Occurrence))
				if cell.State() == golem.ReadUnselected {
					return nil, nil, fmt.Errorf("selected relation %s is absent from row %d", slot.ResponseName, index)
				}
				if cell.State() == golem.ReadNull {
					ranges[index].null = true
					continue
				}
				raw, _ := cell.Get()
				switch value := raw.(type) {
				case golem.RuntimeModelRow:
					ranges[index] = relationRange{start: len(flattened), count: 1}
					flattened = append(flattened, value)
					flattenedPaths = append(flattenedPaths, appendPath(paths[index], slot.ResponseName))
				case []golem.RuntimeModelRow:
					ranges[index] = relationRange{start: len(flattened), count: len(value), many: true}
					flattened = append(flattened, value...)
					for child := range value {
						flattenedPaths = append(flattenedPaths, appendPath(paths[index], slot.ResponseName, child))
					}
				default:
					return nil, nil, fmt.Errorf("relation %s has runtime value %T", slot.ResponseName, raw)
				}
			}
			encoded, childFailures, encodeErr := c.encodeComputedRowsPartial(ctx, target, flattened, slot.Children, resolve, flattenedPaths)
			if encodeErr != nil {
				return nil, nil, encodeErr
			}
			// Ordinary relation output fields are nullable authorization
			// boundaries, so a child non-null failure cannot erase its parent.
			failures = append(failures, stopFailurePropagation(childFailures)...)
			for index, span := range ranges {
				if span.null {
					result[index][slot.ResponseName] = nil
					continue
				}
				if !span.many {
					if span.count != 1 {
						return nil, nil, fmt.Errorf("relation %s has no to-one row", slot.ResponseName)
					}
					result[index][slot.ResponseName] = encoded[span.start]
					continue
				}
				items := make([]any, span.count)
				for child := 0; child < span.count; child++ {
					items[child] = encoded[span.start+child]
				}
				result[index][slot.ResponseName] = items
			}
		case selectset.SlotRelationCount:
			for index, row := range rows {
				counts := make(map[string]any, len(slot.Children))
				for _, child := range slot.Children {
					fieldID, idErr := publicFieldID(child.FieldID)
					if idErr != nil {
						return nil, nil, idErr
					}
					relationID, idErr := publicRelationID(child.RelationID)
					if idErr != nil {
						return nil, nil, idErr
					}
					cell := golem.RuntimeTransportRelationCount(row, fieldID, relationID, golem.RuntimeOccurrenceID(child.Occurrence))
					if cell.State() == golem.ReadUnselected {
						return nil, nil, fmt.Errorf("selected relation count %s is absent", child.ResponseName)
					}
					if cell.State() == golem.ReadNull {
						counts[child.ResponseName] = nil
						continue
					}
					value, _ := cell.Get()
					count, valid := value.(int64)
					if !valid || count < 0 || count > math.MaxInt32 {
						return nil, nil, fmt.Errorf("relation count %s is outside GraphQL Int", child.ResponseName)
					}
					counts[child.ResponseName] = int32(count)
				}
				result[index][slot.ResponseName] = counts
			}
		case selectset.SlotComputed:
			values, resolveErr := resolve(ctx, modelID, rows, slot)
			if resolveErr != nil {
				indexed, ok := resolveErr.(indexedComputedFailures)
				if !ok {
					for index := range rows {
						result[index][slot.ResponseName] = nil
						failures = append(failures, ComputedFieldFailure{Path: appendPath(paths[index], slot.ResponseName), Err: fmt.Errorf("computed field %s: %w", slot.ResponseName, resolveErr), PropagatesToRoot: !slot.Computed.Result.Nullable})
					}
					continue
				}
				for index, failure := range indexed.ComputedFieldFailures() {
					if index < 0 || index >= len(rows) {
						return nil, nil, fmt.Errorf("computed field %s returned invalid failed row %d", slot.ResponseName, index)
					}
					result[index][slot.ResponseName] = nil
					failures = append(failures, ComputedFieldFailure{Path: appendPath(paths[index], slot.ResponseName), Err: failure, PropagatesToRoot: !slot.Computed.Result.Nullable})
				}
			}
			if len(values) != len(rows) {
				return nil, nil, fmt.Errorf("computed field %s returned %d values for %d rows", slot.ResponseName, len(values), len(rows))
			}
			for index, value := range values {
				if _, failed := result[index][slot.ResponseName]; failed {
					continue
				}
				encoded, childFailures, encodeErr := c.encodeComputedValuePartial(ctx, slot.Computed.Result, value, slot.Children, resolve, appendPath(paths[index], slot.ResponseName))
				if encodeErr != nil {
					result[index][slot.ResponseName] = nil
					failures = append(failures, ComputedFieldFailure{Path: appendPath(paths[index], slot.ResponseName), Err: fmt.Errorf("computed field %s row %d: %w", slot.ResponseName, index, encodeErr), PropagatesToRoot: !slot.Computed.Result.Nullable})
					continue
				}
				result[index][slot.ResponseName] = encoded
				failures = append(failures, childFailures...)
			}
		default:
			return nil, nil, fmt.Errorf("slot %s has invalid kind %d", slot.ResponseName, slot.Kind)
		}
	}
	return result, failures, nil
}

func appendPath(base []any, values ...any) []any {
	result := append([]any(nil), base...)
	return append(result, values...)
}

func stopFailurePropagation(values []ComputedFieldFailure) []ComputedFieldFailure {
	result := append([]ComputedFieldFailure(nil), values...)
	for index := range result {
		result[index].PropagatesToRoot = false
	}
	return result
}

func propagateThroughBoundary(values []ComputedFieldFailure, nullable bool) []ComputedFieldFailure {
	if nullable {
		return stopFailurePropagation(values)
	}
	return values
}

func propagateThroughList(values []ComputedFieldFailure, element, list compilerir.GraphQLTypeIR) []ComputedFieldFailure {
	if element.Nullable || list.Nullable {
		return stopFailurePropagation(values)
	}
	return values
}

func (c *Compiler) encodeComputedValue(ctx context.Context, typ compilerir.GraphQLTypeIR, value any, children []selectset.Slot, resolve ComputedResolver) (any, error) {
	encoded, failures, err := c.encodeComputedValuePartial(ctx, typ, value, children, resolve, nil)
	if err != nil {
		return nil, err
	}
	if len(failures) != 0 {
		return nil, failures[0].Err
	}
	return encoded, nil
}

func (c *Compiler) encodeComputedValuePartial(ctx context.Context, typ compilerir.GraphQLTypeIR, value any, children []selectset.Slot, resolve ComputedResolver, path []any) (any, []ComputedFieldFailure, error) {
	if value != nil {
		reflected := reflect.ValueOf(value)
		if reflected.Kind() == reflect.Pointer {
			if reflected.IsNil() {
				value = nil
			} else {
				value = reflected.Elem().Interface()
			}
		}
	}
	if value == nil {
		if !typ.Nullable {
			return nil, nil, fmt.Errorf("non-null computed result is null")
		}
		return nil, nil, nil
	}
	if typ.Kind == compilerir.GraphQLTypeList {
		if typ.Element == nil {
			return nil, nil, fmt.Errorf("computed list has no element type")
		}
		reflected := reflect.ValueOf(value)
		if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
			return nil, nil, fmt.Errorf("computed list has value %T", value)
		}
		if typ.Element.Kind == compilerir.GraphQLTypeModel {
			rows := make([]golem.RuntimeModelRow, reflected.Len())
			for index := range rows {
				row, ok := reflected.Index(index).Interface().(golem.RuntimeModelRow)
				if !ok {
					return nil, nil, fmt.Errorf("computed model list item %d has value %T", index, reflected.Index(index).Interface())
				}
				rows[index] = row
			}
			model, ok := c.modelByGraphQLName(typ.Element.Name)
			if !ok {
				return nil, nil, fmt.Errorf("computed model type %s is absent", typ.Element.Name)
			}
			paths := make([][]any, len(rows))
			for index := range paths {
				paths[index] = appendPath(path, index)
			}
			encoded, failures, err := c.encodeComputedRowsPartial(ctx, model, rows, children, resolve, paths)
			if err != nil {
				return nil, nil, err
			}
			result := make([]any, len(encoded))
			for index := range encoded {
				result[index] = encoded[index]
			}
			return result, propagateThroughList(failures, *typ.Element, typ), nil
		}
		result := make([]any, reflected.Len())
		var failures []ComputedFieldFailure
		for index := 0; index < reflected.Len(); index++ {
			encoded, childFailures, err := c.encodeComputedValuePartial(ctx, *typ.Element, reflected.Index(index).Interface(), children, resolve, appendPath(path, index))
			if err != nil {
				result[index] = nil
				failures = append(failures, ComputedFieldFailure{Path: appendPath(path, index), Err: fmt.Errorf("computed list item %d: %w", index, err), PropagatesToRoot: !typ.Element.Nullable && !typ.Nullable})
				continue
			}
			result[index] = encoded
			failures = append(failures, propagateThroughList(childFailures, *typ.Element, typ)...)
		}
		return result, propagateThroughBoundary(failures, typ.Nullable), nil
	}
	if typ.Kind == compilerir.GraphQLTypeModel {
		row, ok := value.(golem.RuntimeModelRow)
		if !ok {
			return nil, nil, fmt.Errorf("computed model has value %T", value)
		}
		model, ok := c.modelByGraphQLName(typ.Name)
		if !ok {
			return nil, nil, fmt.Errorf("computed model type %s is absent", typ.Name)
		}
		encoded, failures, err := c.encodeComputedRowsPartial(ctx, model, []golem.RuntimeModelRow{row}, children, resolve, [][]any{path})
		if err != nil {
			return nil, nil, err
		}
		return encoded[0], propagateThroughBoundary(failures, typ.Nullable), nil
	}
	if typ.Kind == compilerir.GraphQLTypeEnum {
		name, ok := value.(string)
		if !ok {
			reflected := reflect.ValueOf(value)
			if reflected.IsValid() && reflected.Kind() == reflect.String {
				name, ok = reflected.String(), true
			}
		}
		graphQLName, valid := c.computedEnumName(typ.Name, name)
		if !ok || !valid {
			return nil, nil, fmt.Errorf("computed enum %s has invalid value", typ.Name)
		}
		return graphQLName, nil, nil
	}
	if typ.Kind != compilerir.GraphQLTypeScalar {
		return nil, nil, fmt.Errorf("computed result type %s is unsupported", typ.Kind)
	}
	encoded, err := encodeComputedScalar(typ.Name, value)
	return encoded, nil, err
}

func encodeComputedScalar(name string, value any) (any, error) {
	switch name {
	case "Boolean":
		if result, ok := value.(bool); ok {
			return result, nil
		}
	case "Int":
		if result, ok := exactSigned(value, 32); ok {
			return int32(result), nil
		}
	case "Float":
		if narrow, ok := value.(float32); ok {
			value = float64(narrow)
		}
		if result, err := graphqlscalar.Float(value, 64); err == nil {
			return result, nil
		}
	case "String":
		if result, ok := value.(string); ok && utf8.ValidString(result) {
			return result, nil
		}
	case "BigInt":
		if result, ok := exactSigned(value, 64); ok {
			return graphqlscalar.SerializeBigInt(result), nil
		}
	case "Decimal":
		if result, ok := value.(golem.Decimal); ok {
			return result.String(), nil
		}
	case "UUID":
		if result, ok := value.(golem.UUID); ok {
			return result.String(), nil
		}
	case "Date":
		if result, ok := value.(golem.Date); ok {
			return result.String(), nil
		}
	case "Time":
		if result, ok := value.(golem.Time); ok {
			return result.String(), nil
		}
	case "DateTime":
		if result, ok := value.(time.Time); ok && result.Year() >= 1 && result.Year() <= 9999 && result.Nanosecond()%1_000 == 0 {
			return graphqlscalar.SerializeDateTime(result), nil
		}
	case "Bytes":
		if result, ok := value.([]byte); ok {
			return base64.StdEncoding.EncodeToString(result), nil
		}
	case "JSON":
		if document, ok := value.(interface{ Bytes() []byte }); ok {
			return graphqlscalar.JSON(document.Bytes(), graphqlscalar.JSONLimits{})
		}
		encoded, err := json.Marshal(value)
		if err == nil {
			return graphqlscalar.JSON(encoded, graphqlscalar.JSONLimits{})
		}
	}
	return nil, fmt.Errorf("computed scalar %s cannot serialize %T", name, value)
}

func (c *Compiler) modelByGraphQLName(name string) (compilerir.ModelID, bool) {
	for _, contract := range c.compilation.Contract.Models {
		if contract.Exposed && contract.GraphQLName == name {
			return contract.ModelID, true
		}
	}
	return "", false
}

func (c *Compiler) computedEnumName(enumName, wire string) (string, bool) {
	var valueID compilerir.EnumValueID
	found := false
	for _, contract := range c.compilation.Contract.Enums {
		if contract.GraphQLName != enumName {
			continue
		}
		for _, model := range c.compilation.Model.Enums {
			if model.ID != contract.EnumID {
				continue
			}
			for _, value := range model.Values {
				if value.WireValue == wire {
					valueID, found = value.ID, true
					break
				}
			}
		}
		if found {
			for _, value := range contract.Values {
				if value.ValueID == valueID {
					return value.GraphQLName, true
				}
			}
		}
	}
	return "", false
}

func (c *Compiler) graphQLEnumNameDeclared(enumName, name string) bool {
	for _, contract := range c.compilation.Contract.Enums {
		if contract.GraphQLName == enumName {
			for _, value := range contract.Values {
				if value.GraphQLName == name {
					return true
				}
			}
		}
	}
	return false
}
