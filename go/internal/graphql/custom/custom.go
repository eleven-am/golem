// Package custom validates and dispatches statically declared custom GraphQL
// roots. It owns only ContractIR identity/type matching. It does not expose a
// database, System, transaction, SQL, authorization, or execution capability.
package custom

import (
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlscalar "github.com/eleven-am/golem/go/internal/graphql/scalar"
)

type Argument struct {
	name  string
	type_ compilerir.GraphQLTypeIR
	value any
}

func (argument Argument) Name() string                   { return argument.name }
func (argument Argument) Type() compilerir.GraphQLTypeIR { return cloneType(argument.type_) }
func (argument Argument) Value() any                     { return cloneValue(argument.value) }

type Request struct {
	operation compilerir.CustomOperationContractIR
	arguments []Argument
}

func (request Request) ExtensionID() compilerir.ExtensionID   { return request.operation.ExtensionID }
func (request Request) Kind() compilerir.CustomOperationKind  { return request.operation.Operation }
func (request Request) Name() string                          { return request.operation.Name }
func (request Request) Resolver() compilerir.AttachedMethodIR { return request.operation.Resolver }
func (request Request) ResultType() compilerir.GraphQLTypeIR {
	return cloneType(request.operation.Result)
}
func (request Request) Arguments() []Argument {
	result := make([]Argument, len(request.arguments))
	for index, argument := range request.arguments {
		result[index] = Argument{name: argument.name, type_: cloneType(argument.type_), value: cloneValue(argument.value)}
	}
	return result
}

type Result struct {
	type_ compilerir.GraphQLTypeIR
	value any
}

func (result Result) Type() compilerir.GraphQLTypeIR { return cloneType(result.type_) }
func (result Result) Value() any                     { return cloneValue(result.value) }

type Registry struct {
	compilation compilerir.CompilationIR
	byRoot      map[rootKey]compilerir.CustomOperationContractIR
	byID        map[compilerir.ExtensionID]compilerir.CustomOperationContractIR
	models      map[string]golem.ModelID
	versioned   map[string]bool
	enums       map[string]enumValues
}

type enumValues struct {
	graphQL map[string]bool
	wire    map[string]bool
}

type rootKey struct {
	kind compilerir.CustomOperationKind
	name string
}

func New(compilation compilerir.CompilationIR) (*Registry, error) {
	registry := &Registry{
		compilation: compilation,
		byRoot:      make(map[rootKey]compilerir.CustomOperationContractIR, len(compilation.Contract.CustomOperations)),
		byID:        make(map[compilerir.ExtensionID]compilerir.CustomOperationContractIR, len(compilation.Contract.CustomOperations)),
		models:      map[string]golem.ModelID{},
		versioned:   map[string]bool{},
		enums:       map[string]enumValues{},
	}
	for _, contract := range compilation.Contract.Models {
		if !contract.Exposed {
			continue
		}
		model, err := publicModelID(contract.ModelID)
		if err != nil {
			return nil, err
		}
		if _, duplicate := registry.models[contract.GraphQLName]; duplicate {
			return nil, fmt.Errorf("P5_CUSTOM_MODEL: GraphQL model %q is duplicated", contract.GraphQLName)
		}
		registry.models[contract.GraphQLName] = model
		if contract.OptimisticConcurrency != nil {
			registry.versioned[contract.GraphQLName] = true
		}
	}
	for _, enum := range compilation.Contract.Enums {
		values := enumValues{graphQL: make(map[string]bool, len(enum.Values)), wire: make(map[string]bool, len(enum.Values))}
		wireByID := map[compilerir.EnumValueID]string{}
		for _, model := range compilation.Model.Enums {
			if model.ID == enum.EnumID {
				for _, value := range model.Values {
					wireByID[value.ID] = value.WireValue
				}
			}
		}
		for _, value := range enum.Values {
			values.graphQL[value.GraphQLName] = true
			values.wire[wireByID[value.ValueID]] = true
		}
		registry.enums[enum.GraphQLName] = values
	}
	for _, operation := range compilation.Contract.CustomOperations {
		if err := registry.validateOperation(operation); err != nil {
			return nil, err
		}
		key := rootKey{kind: operation.Operation, name: operation.Name}
		if _, duplicate := registry.byRoot[key]; duplicate {
			return nil, fmt.Errorf("P5_CUSTOM_ROOT: %s %q is duplicated", operation.Operation, operation.Name)
		}
		if _, duplicate := registry.byID[operation.ExtensionID]; duplicate {
			return nil, fmt.Errorf("P5_CUSTOM_ID: extension %q is duplicated", operation.ExtensionID)
		}
		registry.byRoot[key], registry.byID[operation.ExtensionID] = cloneOperation(operation), cloneOperation(operation)
	}
	return registry, nil
}

func (registry *Registry) Operations() []compilerir.CustomOperationContractIR {
	if registry == nil {
		return nil
	}
	result := make([]compilerir.CustomOperationContractIR, 0, len(registry.compilation.Contract.CustomOperations))
	for _, operation := range registry.compilation.Contract.CustomOperations {
		result = append(result, cloneOperation(operation))
	}
	return result
}

func (registry *Registry) Contract(id compilerir.ExtensionID) (compilerir.CustomOperationContractIR, bool) {
	if registry == nil {
		return compilerir.CustomOperationContractIR{}, false
	}
	operation, ok := registry.byID[id]
	return cloneOperation(operation), ok
}

// Prepare accepts values only after GraphQL coercion/name binding. Scalar
// values must already use their exact Go representation; model-derived values
// must already be frozen P3/P4 values with the declared stable model identity.
func (registry *Registry) Prepare(kind compilerir.CustomOperationKind, name string, supplied map[string]any) (Request, error) {
	if registry == nil {
		return Request{}, fmt.Errorf("P5_CUSTOM_REGISTRY: registry is absent")
	}
	operation, ok := registry.byRoot[rootKey{kind: kind, name: name}]
	if !ok {
		return Request{}, fmt.Errorf("P5_CUSTOM_ROOT: %s %q is not statically declared", kind, name)
	}
	declared := make(map[string]compilerir.GraphQLArgumentContractIR, len(operation.Arguments))
	for _, argument := range operation.Arguments {
		declared[argument.Name] = argument
	}
	for name := range supplied {
		if _, known := declared[name]; !known {
			return Request{}, fmt.Errorf("P5_CUSTOM_ARGUMENT: %s has unknown argument %q", operation.Name, name)
		}
	}
	request := Request{operation: cloneOperation(operation), arguments: make([]Argument, len(operation.Arguments))}
	for index, argument := range operation.Arguments {
		value, present := supplied[argument.Name]
		if !present {
			if !argument.Type.Nullable {
				return Request{}, fmt.Errorf("P5_CUSTOM_ARGUMENT: %s requires argument %q", operation.Name, argument.Name)
			}
			value = nil
		}
		if err := registry.validateValue(argument.Type, value, true); err != nil {
			return Request{}, fmt.Errorf("P5_CUSTOM_ARGUMENT: %s.%s: %w", operation.Name, argument.Name, err)
		}
		request.arguments[index] = Argument{name: argument.Name, type_: cloneType(argument.Type), value: cloneValue(value)}
	}
	return request, nil
}

func (registry *Registry) Finish(request Request, value any) (Result, error) {
	if registry == nil {
		return Result{}, fmt.Errorf("P5_CUSTOM_REGISTRY: registry is absent")
	}
	contract, ok := registry.byID[request.operation.ExtensionID]
	if !ok || contract.Operation != request.operation.Operation || contract.Name != request.operation.Name || contract.Resolver != request.operation.Resolver {
		return Result{}, fmt.Errorf("P5_CUSTOM_REQUEST: request is not owned by this registry")
	}
	if err := registry.validateValue(contract.Result, value, false); err != nil {
		return Result{}, fmt.Errorf("P5_CUSTOM_RESULT: %s: %w", contract.Name, err)
	}
	return Result{type_: cloneType(contract.Result), value: cloneValue(value)}, nil
}

func (registry *Registry) validateOperation(operation compilerir.CustomOperationContractIR) error {
	if operation.ExtensionID == "" || operation.Name == "" || (operation.Operation != compilerir.CustomOperationQuery && operation.Operation != compilerir.CustomOperationMutation) {
		return fmt.Errorf("P5_CUSTOM_CONTRACT: custom operation identity, kind, and name are required")
	}
	if operation.Capability != compilerir.CustomOperationCallerOnly {
		return fmt.Errorf("P5_CUSTOM_CAPABILITY: %s may request only Caller", operation.Name)
	}
	expectedKind := "custom" + string(operation.Operation)
	if operation.Resolver.PackagePath == "" || operation.Resolver.PackagePath != registry.compilation.Model.Schema.PackagePath || operation.Resolver.Name == "" || operation.Resolver.Kind != expectedKind || operation.Resolver.Receiver != (compilerir.GoNamedTypeIR{}) || operation.Resolver.ModelID != nil || operation.Resolver.Actor != nil {
		return fmt.Errorf("P5_CUSTOM_RESOLVER: %s has invalid attached resolver metadata", operation.Name)
	}
	seen := map[string]bool{}
	for _, argument := range operation.Arguments {
		if argument.Name == "" || seen[argument.Name] {
			return fmt.Errorf("P5_CUSTOM_ARGUMENT: %s has an empty or duplicate argument", operation.Name)
		}
		seen[argument.Name] = true
		if err := registry.validateType(argument.Type, true); err != nil {
			return fmt.Errorf("P5_CUSTOM_ARGUMENT: %s.%s: %w", operation.Name, argument.Name, err)
		}
	}
	if err := registry.validateType(operation.Result, false); err != nil {
		return fmt.Errorf("P5_CUSTOM_RESULT: %s: %w", operation.Name, err)
	}
	return nil
}

func (registry *Registry) validateType(typ compilerir.GraphQLTypeIR, argument bool) error {
	if typ.Kind == compilerir.GraphQLTypeList {
		if typ.Element == nil {
			return fmt.Errorf("list has no element type")
		}
		return registry.validateType(*typ.Element, argument)
	}
	if typ.Element != nil {
		return fmt.Errorf("non-list type has an element")
	}
	if typ.Kind == compilerir.GraphQLTypeUpdateManyInput && registry.versioned[typ.Name] {
		return fmt.Errorf("optimistic-concurrency model %q has no update-many input", typ.Name)
	}
	switch typ.Kind {
	case compilerir.GraphQLTypeScalar:
		if !compilerir.IsScalarGraphQLName(typ.Name) {
			return fmt.Errorf("scalar %q is unknown", typ.Name)
		}
	case compilerir.GraphQLTypeEnum:
		if registry.enums[typ.Name].graphQL == nil {
			return fmt.Errorf("enum %q is unknown", typ.Name)
		}
	case compilerir.GraphQLTypeModel:
		if argument || registry.models[typ.Name] == (golem.ModelID{}) {
			return fmt.Errorf("model %q is not a registered result type", typ.Name)
		}
	case compilerir.GraphQLTypePredicate, compilerir.GraphQLTypeSelector, compilerir.GraphQLTypeCreateInput, compilerir.GraphQLTypeUpdateInput, compilerir.GraphQLTypeUpdateManyInput:
		if !argument || registry.models[typ.Name] == (golem.ModelID{}) {
			return fmt.Errorf("model-derived input %q is not registered", typ.Name)
		}
	default:
		return fmt.Errorf("type kind %q is unsupported", typ.Kind)
	}
	return nil
}

func (registry *Registry) validateValue(typ compilerir.GraphQLTypeIR, value any, argument bool) error {
	if value == nil {
		if !typ.Nullable {
			return fmt.Errorf("non-null value is null")
		}
		return nil
	}
	if typ.Kind == compilerir.GraphQLTypeList {
		items, ok := value.([]any)
		if !ok || typ.Element == nil {
			return fmt.Errorf("list requires []any exact values, got %T", value)
		}
		for index, item := range items {
			if err := registry.validateValue(*typ.Element, item, argument); err != nil {
				return fmt.Errorf("element %d: %w", index, err)
			}
		}
		return nil
	}
	if typ.Kind == compilerir.GraphQLTypeEnum {
		name, ok := value.(string)
		values := registry.enums[typ.Name].graphQL
		if argument {
			values = registry.enums[typ.Name].wire
		}
		if !ok || !values[name] {
			return fmt.Errorf("value is not a declared %s enum", typ.Name)
		}
		return nil
	}
	if typ.Kind == compilerir.GraphQLTypeModel {
		row, ok := value.(golem.RuntimeModelRow)
		if !ok || row.ModelID() != registry.models[typ.Name] {
			return fmt.Errorf("value is not a %s runtime row", typ.Name)
		}
		return nil
	}
	model := registry.models[typ.Name]
	switch typ.Kind {
	case compilerir.GraphQLTypePredicate:
		predicate, ok := value.(golem.FrozenPredicate)
		if !ok || predicate.View().RootModelID() != model {
			return fmt.Errorf("value is not a %s predicate", typ.Name)
		}
		return nil
	case compilerir.GraphQLTypeSelector:
		target, ok := value.(golem.FrozenMutationTarget)
		if !ok || target.Selector().ModelID() != model {
			return fmt.Errorf("value is not a %s selector", typ.Name)
		}
		return nil
	case compilerir.GraphQLTypeCreateInput, compilerir.GraphQLTypeUpdateInput, compilerir.GraphQLTypeUpdateManyInput:
		input, ok := value.(golem.RuntimeCustomMutationInput)
		kind := map[compilerir.GraphQLTypeKind]golem.RuntimeMutationInputKind{
			compilerir.GraphQLTypeCreateInput: golem.RuntimeMutationCreateInput, compilerir.GraphQLTypeUpdateInput: golem.RuntimeMutationUpdateInput,
			compilerir.GraphQLTypeUpdateManyInput: golem.RuntimeMutationUpdateManyInput,
		}[typ.Kind]
		if !ok || input.Kind() != kind || input.Frozen().ModelID() != model {
			return fmt.Errorf("value is not a %s mutation input", typ.Name)
		}
		return nil
	case compilerir.GraphQLTypeScalar:
		return validateScalar(typ.Name, value)
	default:
		return fmt.Errorf("value kind %q is unsupported", typ.Kind)
	}
}

func validateScalar(name string, value any) error {
	valid := false
	switch name {
	case "Boolean":
		_, valid = value.(bool)
	case "Int":
		_, valid = value.(int32)
	case "Float":
		number, ok := value.(float64)
		valid = ok && !math.IsNaN(number) && !math.IsInf(number, 0)
	case "String":
		text, ok := value.(string)
		valid = ok && utf8.ValidString(text)
	case "BigInt":
		_, valid = value.(int64)
	case "Decimal":
		_, valid = value.(golem.Decimal)
	case "UUID":
		_, valid = value.(golem.UUID)
	case "Date":
		_, valid = value.(golem.Date)
	case "Time":
		_, valid = value.(golem.Time)
	case "DateTime":
		_, valid = value.(time.Time)
	case "Bytes":
		_, valid = value.([]byte)
	case "JSON":
		document, ok := value.(interface{ Bytes() []byte })
		if ok {
			_, err := graphqlscalar.JSON(document.Bytes(), graphqlscalar.JSONLimits{})
			valid = err == nil
		}
	}
	if !valid {
		return fmt.Errorf("%s requires its exact Go value, got %T", name, value)
	}
	return nil
}

func cloneOperation(value compilerir.CustomOperationContractIR) compilerir.CustomOperationContractIR {
	value.Arguments = append([]compilerir.GraphQLArgumentContractIR(nil), value.Arguments...)
	for index := range value.Arguments {
		value.Arguments[index].Type = cloneType(value.Arguments[index].Type)
	}
	value.Result = cloneType(value.Result)
	if value.Resolver.ModelID != nil {
		model := *value.Resolver.ModelID
		value.Resolver.ModelID = &model
	}
	if value.Resolver.Actor != nil {
		actor := *value.Resolver.Actor
		value.Resolver.Actor = &actor
	}
	return value
}

func cloneType(value compilerir.GraphQLTypeIR) compilerir.GraphQLTypeIR {
	if value.Element != nil {
		element := cloneType(*value.Element)
		value.Element = &element
	}
	return value
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return append([]byte(nil), typed...)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = cloneValue(item)
		}
		return result
	default:
		return value
	}
}

func publicModelID(value compilerir.ModelID) (result golem.ModelID, err error) {
	if len(value) != 32 {
		return result, fmt.Errorf("P5_CUSTOM_MODEL: identity %q is invalid", value)
	}
	for index := 0; index < 16; index++ {
		high, highOK := hexDigit(value[index*2])
		low, lowOK := hexDigit(value[index*2+1])
		if !highOK || !lowOK {
			return golem.ModelID{}, fmt.Errorf("P5_CUSTOM_MODEL: identity %q is invalid", value)
		}
		result[index] = high<<4 | low
	}
	return result, nil
}

func hexDigit(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	default:
		return 0, false
	}
}
