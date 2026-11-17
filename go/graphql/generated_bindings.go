package graphql

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlcontract "github.com/eleven-am/golem/go/internal/graphql/contract"
	graphqlscalar "github.com/eleven-am/golem/go/internal/graphql/scalar"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/typedvalue"
)

// BindGeneratedComputed is the generated-code bridge from a statically checked
// model method to the closed runtime binding. Its type parameters are inferred
// from the method value, so generated code never has to reconstruct an Args
// type name from source text.
func BindGeneratedComputed[M, A, R any](extensionID string, descriptor golem.ModelDescriptor[M], resolver func(context.Context, golem.Row[M], A) (R, error)) (ComputedBinding, error) {
	if resolver == nil {
		return ComputedBinding{}, fmt.Errorf("GraphQL generated computed resolver is nil")
	}
	return BindComputed(extensionID, func(ctx context.Context, request ComputedRequest) (any, error) {
		parent, err := golem.RuntimeTypedReadRow(descriptor, request.Parent)
		if err != nil {
			return nil, err
		}
		arguments, err := generatedArguments[A](request.Arguments)
		if err != nil {
			return nil, err
		}
		value, err := resolver(ctx, parent, arguments)
		if err != nil {
			return nil, err
		}
		return eraseGeneratedRows(value), nil
	})
}

// BindGeneratedBatchedComputed performs one typed loader invocation per
// runtime batch and erases only the Row model witness from object results.
func BindGeneratedBatchedComputed[M any, K comparable, A, R any](extensionID string, descriptor golem.ModelDescriptor[M], key golem.ScalarColumn[M, K], loader func(context.Context, []K, A) (map[K]R, error), codec func(K) (string, error)) (ComputedBinding, error) {
	if key == nil || loader == nil {
		return ComputedBinding{}, fmt.Errorf("GraphQL generated batched computed binding is incomplete")
	}
	keyFor := func(parent golem.RuntimeModelRow) (K, string, bool, error) {
		var zero K
		row, err := golem.RuntimeTypedReadRow(descriptor, parent)
		if err != nil {
			return zero, "", false, err
		}
		value, present := golem.Value(row, key).Get()
		if !present {
			return zero, "", false, nil
		}
		encoded := ""
		if codec != nil {
			encoded, err = codec(value)
		} else {
			encoded = fmt.Sprintf("%T:%#v", value, value)
		}
		if err != nil || encoded == "" {
			if err == nil {
				err = fmt.Errorf("computed batch cache key is empty")
			}
			return zero, "", false, err
		}
		return value, encoded, true, nil
	}
	return BindBatchedComputed(extensionID,
		func(_ context.Context, request ComputedRequest) (string, bool, error) {
			_, encoded, present, err := keyFor(request.Parent)
			return encoded, present, err
		},
		func(ctx context.Context, parents []ComputedBatchParent, arguments []ComputedArgument) (map[string]ComputedBatchResult, error) {
			typedArguments, err := generatedArguments[A](arguments)
			if err != nil {
				return nil, err
			}
			keys := make([]K, len(parents))
			encoded := make([]string, len(parents))
			for index, parent := range parents {
				value, cacheKey, present, keyErr := keyFor(parent.Parent())
				if keyErr != nil || !present || cacheKey != parent.CacheKey() {
					if keyErr == nil {
						keyErr = fmt.Errorf("computed batch parent cache key changed")
					}
					return nil, keyErr
				}
				keys[index], encoded[index] = value, cacheKey
			}
			loaded, err := loader(ctx, keys, typedArguments)
			if err != nil {
				return nil, err
			}
			result := make(map[string]ComputedBatchResult, len(keys))
			for index, key := range keys {
				value, present := loaded[key]
				if !present {
					return nil, fmt.Errorf("computed batch loader omitted key %q", encoded[index])
				}
				result[encoded[index]] = ComputedBatchResult{Value: eraseGeneratedRows(value)}
			}
			return result, nil
		})
}

func generatedArguments[A any](arguments []ComputedArgument) (A, error) {
	var result A
	target := reflect.ValueOf(&result).Elem()
	if target.Kind() != reflect.Struct {
		return result, fmt.Errorf("GraphQL generated arguments %s are not a struct", target.Type())
	}
	values := make(map[string]any, len(arguments))
	for _, argument := range arguments {
		values[argument.Name] = argument.Value
	}
	for index := 0; index < target.NumField(); index++ {
		field := target.Type().Field(index)
		name := generatedArgumentName(field)
		value, present := values[name]
		if !present {
			continue
		}
		converted, err := coercePreparedValue(value, field.Type)
		if err != nil {
			var overflow *preparedScalarRangeError
			if errors.As(err, &overflow) {
				return result, golem.InputError(golem.ReasonInvalidValue, "graphqlArguments", name, "GraphQL argument "+name+" exceeds its numeric range", err)
			}
			return result, fmt.Errorf("GraphQL argument %s: %w", name, err)
		}
		target.Field(index).Set(converted)
		delete(values, name)
	}
	if len(values) != 0 {
		return result, fmt.Errorf("GraphQL generated arguments contain an unknown field")
	}
	return result, nil
}

func generatedArgumentName(field reflect.StructField) string {
	if tag := field.Tag.Get("golem"); strings.HasPrefix(tag, "graphql=") {
		return strings.TrimPrefix(tag, "graphql=")
	}
	return graphqlcontract.LowerCamel(field.Name)
}

func eraseGeneratedRows[R any](value R) any {
	if erased, ok := golem.RuntimeEraseRows(value); ok {
		return erased
	}
	return value
}

type GeneratedCustomArgumentConversion func(any) (any, error)

// The Contract variants are emitted by golem generate. They recursively
// normalize typed Go results against the canonical result tree before the
// strict custom registry validates them; no JSON or floating conversion is
// used as a shortcut.
func BindGeneratedCustomQueryContract[C, A, R any](bundle golem.SchemaBundle, spec CustomBindingSpec, resolver func(context.Context, C, A) (R, error), conversions ...GeneratedCustomArgumentConversion) (CustomBinding, error) {
	return bindGeneratedCustomContract[struct{}](bundle, CustomQuery, spec, resolver, false, conversions)
}

func BindGeneratedCustomMutationContract[C, A, R any](bundle golem.SchemaBundle, spec CustomBindingSpec, resolver func(context.Context, C, A) (R, error), conversions ...GeneratedCustomArgumentConversion) (CustomBinding, error) {
	return bindGeneratedCustomContract[struct{}](bundle, CustomMutation, spec, resolver, false, conversions)
}

func BindGeneratedCustomQueryModelContract[M, C, A, R any](bundle golem.SchemaBundle, spec CustomBindingSpec, _ golem.ModelDescriptor[M], resolver func(context.Context, C, A) (R, error), conversions ...GeneratedCustomArgumentConversion) (CustomBinding, error) {
	return bindGeneratedCustomContract[M](bundle, CustomQuery, spec, resolver, true, conversions)
}

func BindGeneratedCustomMutationModelContract[M, C, A, R any](bundle golem.SchemaBundle, spec CustomBindingSpec, _ golem.ModelDescriptor[M], resolver func(context.Context, C, A) (R, error), conversions ...GeneratedCustomArgumentConversion) (CustomBinding, error) {
	return bindGeneratedCustomContract[M](bundle, CustomMutation, spec, resolver, true, conversions)
}

func bindGeneratedCustomContract[M, C, A, R any](bundle golem.SchemaBundle, operation CustomOperation, spec CustomBindingSpec, resolver func(context.Context, C, A) (R, error), modelResult bool, conversions []GeneratedCustomArgumentConversion) (CustomBinding, error) {
	compilation, err := policyschema.DecodeActive(bundle)
	if err != nil {
		return CustomBinding{}, err
	}
	var resultType compilerir.GraphQLTypeIR
	var operationName string
	found := false
	for _, contract := range compilation.Contract.CustomOperations {
		if string(contract.ExtensionID) == spec.ExtensionID && contract.Operation == compilerir.CustomOperationKind(operation) {
			resultType, found = contract.Result, true
			operationName = contract.Name
			break
		}
	}
	if !found {
		return CustomBinding{}, fmt.Errorf("GraphQL custom result contract %s is absent", spec.ExtensionID)
	}
	leaf := resultType
	for leaf.Kind == compilerir.GraphQLTypeList && leaf.Element != nil {
		leaf = *leaf.Element
	}
	if leaf.Kind == compilerir.GraphQLTypeModel && !modelResult {
		return CustomBinding{}, fmt.Errorf("custom model result lacks a generated descriptor witness")
	}
	decode := func(arguments []CustomArgument) (A, error) {
		return generatedCustomArguments[A](arguments, conversions)
	}
	encode := func(value R) (any, error) {
		if err := typedvalue.CheckListBounds(resultType, reflect.ValueOf(value), compilation.Contract.GraphQLTypes); err != nil {
			label := "customQuery"
			if operation == CustomMutation {
				label = "customMutation"
			}
			return nil, typedvalue.ListItemsError(label, "custom "+string(operation)+" "+operationName, err)
		}
		return normalizeGeneratedResult[M](compilation, resultType, any(value), modelResult)
	}
	if operation == CustomQuery {
		return BindCustomQuery(spec, decode, resolver, encode)
	}
	return BindCustomMutation(spec, decode, resolver, encode)
}

func normalizeGeneratedResult[M any](compilation compilerir.CompilationIR, typ compilerir.GraphQLTypeIR, value any, modelResult bool) (any, error) {
	if value == nil {
		if !typ.Nullable {
			return nil, fmt.Errorf("non-null custom result is null")
		}
		return nil, nil
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			if !typ.Nullable {
				return nil, fmt.Errorf("non-null custom result is null")
			}
			return nil, nil
		}
		if !typ.Nullable && typ.Kind != compilerir.GraphQLTypeObject {
			return nil, fmt.Errorf("non-null custom result unexpectedly uses a pointer")
		}
		value = reflected.Elem().Interface()
		reflected = reflect.ValueOf(value)
	}
	if typ.Kind == compilerir.GraphQLTypeList {
		if typ.Element == nil || reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
			return nil, fmt.Errorf("custom list result has value %T", value)
		}
		result := make([]any, reflected.Len())
		for index := range result {
			item, err := normalizeGeneratedResult[M](compilation, *typ.Element, reflected.Index(index).Interface(), modelResult)
			if err != nil {
				return nil, fmt.Errorf("custom list item %d: %w", index, err)
			}
			result[index] = item
		}
		return result, nil
	}
	if typ.Kind == compilerir.GraphQLTypeModel {
		if !modelResult {
			// DTO struct identity and field types were checked statically. Erase
			// only the sealed Row witness; supplied cells are never response data.
			erased, ok := golem.RuntimeEraseRows(value)
			row, rowOK := erased.(golem.RuntimeModelRow)
			if !ok || !rowOK {
				return nil, fmt.Errorf("embedded model %s has value %T", typ.Name, value)
			}
			for _, contract := range compilation.Contract.Models {
				if contract.Exposed && contract.GraphQLName == typ.Name && string(contract.ModelID) == fmt.Sprintf("%x", row.ModelID()) {
					return row, nil
				}
			}
			return nil, fmt.Errorf("embedded row is not the declared model %s", typ.Name)
		}
		if semantic, ok := value.(golem.SemanticResult[M]); ok {
			row, err := golem.RuntimeSemanticRowFromResult(semantic)
			if err != nil {
				return nil, err
			}
			return row, nil
		}
		if fulltext, ok := value.(golem.FullTextResult[M]); ok {
			row, err := golem.RuntimeFullTextRowFromResult(fulltext)
			if err != nil {
				return nil, err
			}
			return row, nil
		}
		row, ok := value.(golem.Row[M])
		if !ok {
			return nil, fmt.Errorf("custom model result has value %T", value)
		}
		return golem.RuntimeModelRowFromTyped(row), nil
	}
	if typ.Kind == compilerir.GraphQLTypeEnum {
		if reflected.Kind() != reflect.String {
			return nil, fmt.Errorf("custom enum %s has value %T", typ.Name, value)
		}
		name, ok := generatedEnumGraphQLName(compilation, typ.Name, reflected.String())
		if !ok {
			return nil, fmt.Errorf("custom enum %s has an undeclared wire value", typ.Name)
		}
		return name, nil
	}
	if typ.Kind == compilerir.GraphQLTypeObject {
		for _, definition := range compilation.Contract.GraphQLTypes {
			if definition.Name != typ.Name || definition.Kind != typ.Kind {
				continue
			}
			if reflected.Kind() != reflect.Struct || reflected.Type().PkgPath() != definition.Go.PackagePath || reflected.Type().Name() != definition.Go.Name {
				return nil, fmt.Errorf("custom object %s has value %T, expected %s.%s", typ.Name, value, definition.Go.PackagePath, definition.Go.Name)
			}
			result := make(map[string]any, len(definition.Fields))
			for _, field := range definition.Fields {
				item := reflected.FieldByName(field.GoName)
				if !item.IsValid() || !item.CanInterface() {
					return nil, fmt.Errorf("custom object %s field %s is unavailable", typ.Name, field.GoName)
				}
				normalized, err := normalizeGeneratedResult[M](compilation, field.Type, item.Interface(), modelResult)
				if err != nil {
					return nil, fmt.Errorf("custom object %s.%s: %w", typ.Name, field.Name, err)
				}
				result[field.Name] = normalized
			}
			return result, nil
		}
		return nil, fmt.Errorf("custom object definition %s is absent", typ.Name)
	}
	if typ.Kind != compilerir.GraphQLTypeScalar {
		return nil, fmt.Errorf("custom result type %s is unsupported", typ.Kind)
	}
	switch typ.Name {
	case "Boolean":
		if result, ok := value.(bool); ok {
			return result, nil
		}
	case "Int":
		switch result := value.(type) {
		case int16:
			return int32(result), nil
		case int32:
			return result, nil
		}
	case "Float":
		var result float64
		switch number := value.(type) {
		case float32:
			result = float64(number)
		case float64:
			result = number
		default:
			return nil, fmt.Errorf("custom Float has value %T", value)
		}
		if !math.IsNaN(result) && !math.IsInf(result, 0) {
			return result, nil
		}
	case "String":
		if result, ok := value.(string); ok && utf8.ValidString(result) {
			return result, nil
		}
	case "BigInt":
		if result, ok := value.(int64); ok {
			return result, nil
		}
	case "Decimal":
		if result, ok := value.(golem.Decimal); ok {
			return result, nil
		}
	case "UUID":
		if result, ok := value.(golem.UUID); ok {
			return result, nil
		}
	case "Date":
		if result, ok := value.(golem.Date); ok {
			return result, nil
		}
	case "Time":
		if result, ok := value.(golem.Time); ok {
			return result, nil
		}
	case "DateTime":
		if result, ok := value.(time.Time); ok && result.Year() >= 1 && result.Year() <= 9999 && result.Nanosecond()%1_000 == 0 {
			return result, nil
		}
	case "Bytes":
		if result, ok := value.([]byte); ok {
			return bytes.Clone(result), nil
		}
	case "JSON":
		if result, ok := value.(interface{ Bytes() []byte }); ok {
			if _, err := graphqlscalar.JSON(result.Bytes(), graphqlscalar.JSONLimits{}); err == nil {
				return value, nil
			}
		}
	}
	return nil, fmt.Errorf("custom scalar %s has value %T", typ.Name, value)
}

func generatedEnumGraphQLName(compilation compilerir.CompilationIR, enumName, wire string) (string, bool) {
	for _, contract := range compilation.Contract.Enums {
		if contract.GraphQLName != enumName {
			continue
		}
		wireByID := map[compilerir.EnumValueID]string{}
		for _, model := range compilation.Model.Enums {
			if model.ID == contract.EnumID {
				for _, value := range model.Values {
					wireByID[value.ID] = value.WireValue
				}
			}
		}
		for _, value := range contract.Values {
			if wireByID[value.ValueID] == wire {
				return value.GraphQLName, true
			}
		}
	}
	return "", false
}

func generatedCustomArguments[A any](arguments []CustomArgument, conversions []GeneratedCustomArgumentConversion) (A, error) {
	values := make([]ComputedArgument, len(arguments))
	for index, argument := range arguments {
		value := argument.Value()
		if index < len(conversions) && conversions[index] != nil && value != nil {
			converted, err := conversions[index](value)
			if err != nil {
				var zero A
				return zero, fmt.Errorf("GraphQL custom argument %s: %w", argument.Name(), err)
			}
			value = converted
		}
		values[index] = ComputedArgument{Name: argument.Name(), Value: value}
	}
	return generatedArguments[A](values)
}

func GeneratedCustomPredicateArgument[M any](descriptor golem.ModelDescriptor[M]) GeneratedCustomArgumentConversion {
	return func(value any) (any, error) {
		frozen, ok := value.(golem.FrozenPredicate)
		if !ok {
			return nil, fmt.Errorf("predicate has value %T", value)
		}
		return golem.RuntimeTypedPredicate[M](descriptor.Metadata().ModelID(), frozen)
	}
}

func GeneratedCustomSelectorArgument[M any](descriptor golem.ModelDescriptor[M]) GeneratedCustomArgumentConversion {
	return func(value any) (any, error) {
		frozen, ok := value.(golem.FrozenMutationTarget)
		if !ok {
			return nil, fmt.Errorf("selector has value %T", value)
		}
		return golem.RuntimeTypedUniqueSelector[M](descriptor.Metadata().ModelID(), frozen)
	}
}

func GeneratedCustomMutationInputArgument[M any](descriptor golem.ModelDescriptor[M], kind golem.RuntimeMutationInputKind) GeneratedCustomArgumentConversion {
	return func(value any) (any, error) {
		input, ok := value.(golem.RuntimeCustomMutationInput)
		if !ok {
			return nil, fmt.Errorf("mutation input has value %T", value)
		}
		switch kind {
		case golem.RuntimeMutationCreateInput:
			return golem.RuntimeTypedCreateInput[M](descriptor.Metadata().ModelID(), input)
		case golem.RuntimeMutationUpdateInput:
			return golem.RuntimeTypedUpdateInput[M](descriptor.Metadata().ModelID(), input)
		case golem.RuntimeMutationUpdateManyInput:
			return golem.RuntimeTypedUpdateManyInput[M](descriptor.Metadata().ModelID(), input)
		default:
			return nil, fmt.Errorf("mutation input kind is invalid")
		}
	}
}
