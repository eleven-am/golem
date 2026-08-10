package selectset

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	graphqlscalar "github.com/eleven-am/golem/go/internal/graphql/scalar"
	"github.com/vektah/gqlparser/v2/ast"
)

func CoerceExtensionValue(compilation compilerir.CompilationIR, typ compilerir.GraphQLTypeIR, raw any, maxListItems int) (any, error) {
	enums := extensionEnumValues(compilation)
	value, _, err := coerceComputedValue(typ, raw, enums, maxListItems)
	return value, err
}

func bindComputedArguments(compilation compilerir.CompilationIR, field compilerir.ComputedFieldContractIR, supplied ast.ArgumentList, variables map[string]any, maxListItems int) ([]graphqlextension.BoundArgument, string, error) {
	if maxListItems <= 0 {
		maxListItems = 1_000
	}
	declared := make(map[string]compilerir.GraphQLArgumentContractIR, len(field.Arguments))
	for _, argument := range field.Arguments {
		declared[argument.Name] = argument
	}
	raw := make(map[string]any, len(supplied))
	for _, argument := range supplied {
		if _, duplicate := raw[argument.Name]; duplicate {
			return nil, "", fmt.Errorf("P5_COMPUTED_ARGUMENT: %s repeats argument %q", field.Name, argument.Name)
		}
		if _, known := declared[argument.Name]; !known {
			return nil, "", fmt.Errorf("P5_COMPUTED_ARGUMENT: %s has unknown argument %q", field.Name, argument.Name)
		}
		if argument.Value == nil {
			return nil, "", fmt.Errorf("P5_COMPUTED_ARGUMENT: %s.%s has no value", field.Name, argument.Name)
		}
		value, err := argument.Value.Value(variables)
		if err != nil {
			return nil, "", fmt.Errorf("P5_COMPUTED_ARGUMENT: %s.%s: %w", field.Name, argument.Name, err)
		}
		raw[argument.Name] = value
	}

	enums := extensionEnumValues(compilation)
	bound := make([]graphqlextension.BoundArgument, 0, len(field.Arguments))
	canonical := make([]graphqlextension.CanonicalArgument, 0, len(field.Arguments))
	for _, argument := range field.Arguments {
		value, present := raw[argument.Name]
		if !present {
			if !argument.Type.Nullable {
				return nil, "", fmt.Errorf("P5_COMPUTED_ARGUMENT: %s requires argument %q", field.Name, argument.Name)
			}
			value = nil
		}
		coerced, wire, err := coerceComputedValue(argument.Type, value, enums, maxListItems)
		if err != nil {
			return nil, "", fmt.Errorf("P5_COMPUTED_ARGUMENT: %s.%s: %w", field.Name, argument.Name, err)
		}
		encoded, err := json.Marshal(wire)
		if err != nil {
			return nil, "", fmt.Errorf("P5_COMPUTED_ARGUMENT: %s.%s cannot be canonicalized", field.Name, argument.Name)
		}
		bound = append(bound, graphqlextension.BoundArgument{Name: argument.Name, Type: argument.Type, Value: coerced, Canonical: append(json.RawMessage(nil), encoded...)})
		canonical = append(canonical, graphqlextension.CanonicalArgument{Name: argument.Name, Value: encoded})
	}
	key, err := graphqlextension.CanonicalArguments(canonical...)
	if err != nil {
		return nil, "", fmt.Errorf("P5_COMPUTED_ARGUMENT: %s: %w", field.Name, err)
	}
	return bound, key, nil
}

func coerceComputedValue(typ compilerir.GraphQLTypeIR, raw any, enums map[string]map[string]string, maxListItems int) (any, any, error) {
	if raw == nil {
		if !typ.Nullable {
			return nil, nil, fmt.Errorf("non-null value is null")
		}
		return nil, nil, nil
	}
	if typ.Kind == compilerir.GraphQLTypeList {
		if typ.Element == nil {
			return nil, nil, fmt.Errorf("list type has no element")
		}
		items, ok := raw.([]any)
		if !ok {
			return nil, nil, fmt.Errorf("expected list, got %T", raw)
		}
		if len(items) > maxListItems {
			return nil, nil, fmt.Errorf("list exceeds item limit %d", maxListItems)
		}
		values := make([]any, len(items))
		wire := make([]any, len(items))
		for index, item := range items {
			value, encoded, err := coerceComputedValue(*typ.Element, item, enums, maxListItems)
			if err != nil {
				return nil, nil, fmt.Errorf("element %d: %w", index, err)
			}
			values[index], wire[index] = value, encoded
		}
		return values, wire, nil
	}
	if typ.Kind == compilerir.GraphQLTypeEnum {
		value, ok := raw.(string)
		wire, known := enums[typ.Name][value]
		if !ok || !known {
			return nil, nil, fmt.Errorf("unknown %s enum value", typ.Name)
		}
		return wire, value, nil
	}
	if typ.Kind != compilerir.GraphQLTypeScalar {
		return nil, nil, fmt.Errorf("unsupported computed argument type %s", typ.Kind)
	}
	switch typ.Name {
	case "Boolean":
		value, ok := raw.(bool)
		if !ok {
			return nil, nil, fmt.Errorf("expected Boolean, got %T", raw)
		}
		return value, value, nil
	case "Int":
		value, err := computedInteger(raw)
		if err != nil || value < math.MinInt32 || value > math.MaxInt32 {
			return nil, nil, fmt.Errorf("Int is outside signed 32-bit range")
		}
		return int32(value), value, nil
	case "Float":
		value, err := graphqlscalar.Float(normalizeComputedNumber(raw), 64)
		if err != nil {
			return nil, nil, err
		}
		if value == 0 {
			value = 0 // collapse negative zero into the GraphQL numeric value.
		}
		return value, value, nil
	case "String":
		value, ok := raw.(string)
		if !ok || !utf8.ValidString(value) {
			return nil, nil, fmt.Errorf("expected UTF-8 String, got %T", raw)
		}
		return value, value, nil
	case "BigInt":
		value, err := graphqlscalar.BigInt(raw)
		if err != nil {
			return nil, nil, err
		}
		return value, graphqlscalar.SerializeBigInt(value), nil
	case "Decimal":
		value, err := graphqlscalar.Decimal(raw)
		if err != nil {
			return nil, nil, err
		}
		return value, value.String(), nil
	case "UUID":
		value, err := graphqlscalar.UUID(raw)
		if err != nil {
			return nil, nil, err
		}
		return value, value.String(), nil
	case "Date":
		value, err := graphqlscalar.Date(raw)
		if err != nil {
			return nil, nil, err
		}
		return value, value.String(), nil
	case "Time":
		value, err := graphqlscalar.Time(raw)
		if err != nil {
			return nil, nil, err
		}
		return value, value.String(), nil
	case "DateTime":
		value, err := graphqlscalar.DateTime(raw)
		if err != nil {
			return nil, nil, err
		}
		return value, graphqlscalar.SerializeDateTime(value), nil
	case "Bytes":
		value, err := graphqlscalar.Bytes(raw)
		if err != nil {
			return nil, nil, err
		}
		return value, base64.StdEncoding.EncodeToString(value), nil
	case "JSON":
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid JSON")
		}
		value, err := graphqlscalar.JSON(encoded, graphqlscalar.JSONLimits{})
		if err != nil {
			return nil, nil, err
		}
		return value, value, nil
	default:
		return nil, nil, fmt.Errorf("unknown scalar %q", typ.Name)
	}
}

func extensionEnumValues(compilation compilerir.CompilationIR) map[string]map[string]string {
	wireByID := make(map[compilerir.EnumValueID]string)
	for _, enum := range compilation.Model.Enums {
		for _, value := range enum.Values {
			wireByID[value.ID] = value.WireValue
		}
	}
	result := make(map[string]map[string]string, len(compilation.Contract.Enums))
	for _, enum := range compilation.Contract.Enums {
		values := make(map[string]string, len(enum.Values))
		for _, value := range enum.Values {
			values[value.GraphQLName] = wireByID[value.ValueID]
		}
		result[enum.GraphQLName] = values
	}
	return result
}

func computedInteger(raw any) (int64, error) {
	switch value := raw.(type) {
	case int:
		return int64(value), nil
	case int32:
		return int64(value), nil
	case int64:
		return value, nil
	case json.Number:
		return strconv.ParseInt(string(value), 10, 64)
	default:
		return 0, fmt.Errorf("expected exact integer, got %T", raw)
	}
}

func normalizeComputedNumber(raw any) any {
	switch value := raw.(type) {
	case int32:
		return int64(value)
	default:
		return raw
	}
}
