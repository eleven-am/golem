package selectset

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/golem"
	compileerr "github.com/eleven-am/golem/go/internal/bindingerr"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"

	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	graphqlscalar "github.com/eleven-am/golem/go/internal/graphql/scalar"
	"github.com/eleven-am/golem/go/internal/typedvalue"
	"github.com/vektah/gqlparser/v2/ast"
)

func CoerceExtensionValue(compilation compilerir.CompilationIR, typ compilerir.GraphQLTypeIR, raw any, maxListItems int) (any, error) {
	enums := extensionEnumValues(compilation)
	value, _, err := coerceComputedValue(typ, raw, enums, compilation.Contract.GraphQLTypes, maxListItems)
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
	for _, argument := range supplied {
		if _, known := declared[argument.Name]; !known {
			return nil, "", compileerr.New(compileerr.KindFieldUnavailable, compileerr.UnavailableArgument, compileerr.Path(argument.Name), compileerr.Model(field.Name))
		}
		if argument.Value == nil {
			return nil, "", compileerr.Invariantf("computed argument %s.%s has no value", field.Name, argument.Name)
		}
	}
	raw, err := ArgumentValues(supplied, variables)
	if err != nil {
		return nil, "", compileerr.On(err, field.Name)
	}

	enums := extensionEnumValues(compilation)
	bound := make([]graphqlextension.BoundArgument, 0, len(field.Arguments))
	canonical := make([]graphqlextension.CanonicalArgument, 0, len(field.Arguments))
	for _, argument := range field.Arguments {
		value, present := raw[argument.Name]
		if !present {
			if !argument.Type.Nullable {
				return nil, "", compileerr.New(compileerr.KindRequired, compileerr.DetailNone, compileerr.Path(argument.Name), compileerr.Model(field.Name))
			}
			value = nil
		}
		coerced, wire, err := coerceComputedValue(argument.Type, value, enums, compilation.Contract.GraphQLTypes, maxListItems)
		if err != nil {
			return nil, "", compileerr.On(compileerr.At(err, argument.Name), field.Name)
		}
		if argument.PresenceAware {
			wire = struct {
				Present bool `json:"present"`
				Value   any  `json:"value"`
			}{Present: present, Value: wire}
			if !present {
				coerced = golem.RuntimeOmittedInputValue()
			}
		}
		encoded, err := json.Marshal(wire)
		if err != nil {
			return nil, "", compileerr.Invariantf("computed argument %s.%s cannot be canonicalized: %w", field.Name, argument.Name, err)
		}
		bound = append(bound, graphqlextension.BoundArgument{Name: argument.Name, Type: argument.Type, Value: coerced, Canonical: append(json.RawMessage(nil), encoded...)})
		canonical = append(canonical, graphqlextension.CanonicalArgument{Name: argument.Name, Value: encoded})
	}
	key, err := graphqlextension.CanonicalArguments(canonical...)
	if err != nil {
		return nil, "", compileerr.Invariant(err)
	}
	return bound, key, nil
}

func coerceComputedValue(typ compilerir.GraphQLTypeIR, raw any, enums map[string]map[string]string, definitions []compilerir.GraphQLTypeDefinitionIR, maxListItems int) (any, any, error) {
	if raw == nil {
		if !typ.Nullable {
			return nil, nil, compileerr.New(compileerr.KindNullNotAllowed, compileerr.DetailNone)
		}
		return nil, nil, nil
	}
	if typ.Kind == compilerir.GraphQLTypeList {
		if typ.Element == nil {
			return nil, nil, compileerr.Invariantf("list type has no element")
		}
		items := reflect.ValueOf(raw)
		if items.Kind() != reflect.Slice {
			// GraphQL permits a non-null singleton at each list level. The
			// variable validator can also supply typed slices after promotion.
			items = reflect.ValueOf([]any{raw})
		}
		if items.Len() > maxListItems {
			return nil, nil, compileerr.New(compileerr.KindInputLimit, compileerr.DetailNone, compileerr.Limit("MaxListItems", maxListItems))
		}
		values := make([]any, items.Len())
		wire := make([]any, items.Len())
		for index := 0; index < items.Len(); index++ {
			value, encoded, err := coerceComputedValue(*typ.Element, items.Index(index).Interface(), enums, definitions, maxListItems)
			if err != nil {
				return nil, nil, compileerr.At(err, compileerr.Index("", index))
			}
			values[index], wire[index] = value, encoded
		}
		return values, wire, nil
	}
	if typ.Kind == compilerir.GraphQLTypeEnum {
		value, ok := raw.(string)
		wire, known := enums[typ.Name][value]
		if !ok || !known {
			return nil, nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueUnknownEnum, compileerr.Type(typ.Name))
		}
		return wire, value, nil
	}
	if typ.Kind == compilerir.GraphQLTypeInputObject {
		var definition *compilerir.GraphQLTypeDefinitionIR
		for index := range definitions {
			if definitions[index].Name == typ.Name && definitions[index].Kind == typ.Kind {
				definition = &definitions[index]
				break
			}
		}
		if definition == nil {
			return nil, nil, compileerr.Invariantf("input definition %s is absent", typ.Name)
		}
		fields, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongShape)
		}
		if err := typedvalue.CheckOneOfInput(*definition, fields); err != nil {
			return nil, nil, err
		}
		values, wire := make(map[string]any, len(fields)), make(map[string]any, len(fields))
		for _, field := range definition.Fields {
			rawField, present := fields[field.Name]
			if !present {
				if !field.Type.Nullable {
					return nil, nil, compileerr.New(compileerr.KindRequired, compileerr.DetailNone, compileerr.Path(field.Name))
				}
				continue
			}
			value, encoded, err := coerceComputedValue(field.Type, rawField, enums, definitions, maxListItems)
			if err != nil {
				return nil, nil, compileerr.At(err, field.Name)
			}
			values[field.Name], wire[field.Name] = value, encoded
		}
		for name := range fields {
			if _, known := values[name]; !known {
				return nil, nil, compileerr.New(compileerr.KindFieldUnavailable, compileerr.UnavailableInput, compileerr.Path(name))
			}
		}
		return values, wire, nil
	}
	if typ.Kind != compilerir.GraphQLTypeScalar {
		return nil, nil, compileerr.Invariantf("unsupported computed argument type %s", typ.Kind)
	}
	switch typ.Name {
	case "Boolean":
		value, ok := raw.(bool)
		if !ok {
			return nil, nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongKind, compileerr.Type("Boolean"))
		}
		return value, value, nil
	case "Int":
		value, err := computedInteger(raw)
		if err != nil {
			return nil, nil, err
		}
		if value < math.MinInt32 || value > math.MaxInt32 {
			return nil, nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueOutOfRange, compileerr.Type("Int"))
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
			return nil, nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongKind, compileerr.Type("String"))
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
			return nil, nil, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueMalformed, compileerr.Type("JSON"))
		}
		value, err := graphqlscalar.JSON(encoded, graphqlscalar.JSONLimits{})
		if err != nil {
			return nil, nil, err
		}
		return value, value, nil
	default:
		return nil, nil, compileerr.Invariantf("unknown scalar %q", typ.Name)
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
		parsed, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return 0, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueOutOfRange, compileerr.Type("Int"))
		}
		return parsed, nil
	default:
		return 0, compileerr.New(compileerr.KindInvalidValue, compileerr.ValueWrongKind, compileerr.Type("Int"))
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
