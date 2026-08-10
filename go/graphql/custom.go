package graphql

import (
	"context"
	"fmt"
	"reflect"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlcustom "github.com/eleven-am/golem/go/internal/graphql/custom"
)

// CustomOperation is the closed public custom-root vocabulary.
type CustomOperation string

const (
	CustomQuery    CustomOperation = "query"
	CustomMutation CustomOperation = "mutation"
)

// CustomBindingSpec is emitted from one statically interpreted AttachedMethod.
// The runtime registry matches every field against canonical ContractIR before
// any resolver becomes callable.
type CustomBindingSpec struct {
	ExtensionID     string
	ResolverPackage string
	ResolverName    string
}

// CustomArgument is one already-coerced exact value in declaration order.
// Generated glue converts it into the statically known application Args field.
type CustomArgument struct {
	name  string
	value any
}

func (argument CustomArgument) Name() string { return argument.name }
func (argument CustomArgument) Value() any   { return cloneCustomValue(argument.value) }

func CustomArgumentValue[T any](arguments []CustomArgument, name string) (T, bool) {
	var zero T
	for _, argument := range arguments {
		if argument.name != name {
			continue
		}
		value, ok := argument.Value().(T)
		return value, ok
	}
	return zero, false
}

type CustomArgumentDecoder[A any] func([]CustomArgument) (A, error)
type CustomResultEncoder[R any] func(R) (any, error)

// CustomBinding is opaque generated glue. Its invocation receives exactly the
// caller object supplied for this execution; no System/DB/Tx/raw-SQL channel is
// present in the registry API. Explicit transactions remain methods on Caller.
type CustomBinding struct {
	extensionID string
	operation   CustomOperation
	resolver    compilerir.AttachedMethodIR
	invoke      func(context.Context, any, []CustomArgument) (any, error)
}

func BindCustomQuery[C, A, R any](spec CustomBindingSpec, decode CustomArgumentDecoder[A], resolver func(context.Context, C, A) (R, error), encode CustomResultEncoder[R]) (CustomBinding, error) {
	return bindCustom(CustomQuery, spec, decode, resolver, encode)
}

func BindCustomMutation[C, A, R any](spec CustomBindingSpec, decode CustomArgumentDecoder[A], resolver func(context.Context, C, A) (R, error), encode CustomResultEncoder[R]) (CustomBinding, error) {
	return bindCustom(CustomMutation, spec, decode, resolver, encode)
}

func bindCustom[C, A, R any](operation CustomOperation, spec CustomBindingSpec, decode CustomArgumentDecoder[A], resolver func(context.Context, C, A) (R, error), encode CustomResultEncoder[R]) (CustomBinding, error) {
	if spec.ExtensionID == "" || spec.ResolverPackage == "" || spec.ResolverName == "" || decode == nil || resolver == nil || encode == nil {
		return CustomBinding{}, fmt.Errorf("GraphQL custom binding requires static identity, decoder, resolver, and result encoder")
	}
	kind := "custom" + string(operation)
	return CustomBinding{
		extensionID: spec.ExtensionID,
		operation:   operation,
		resolver:    compilerir.AttachedMethodIR{PackagePath: spec.ResolverPackage, Name: spec.ResolverName, Kind: kind},
		invoke: func(ctx context.Context, rawCaller any, arguments []CustomArgument) (any, error) {
			if provider, ok := rawCaller.(interface{ GolemGraphQLCustomCallerValue() any }); ok {
				rawCaller = provider.GolemGraphQLCustomCallerValue()
			}
			caller, ok := rawCaller.(C)
			if !ok {
				return nil, fmt.Errorf("GraphQL custom resolver caller type does not match generated binding")
			}
			typed, err := decode(cloneCustomArguments(arguments))
			if err != nil {
				return nil, fmt.Errorf("GraphQL custom arguments: %w", err)
			}
			value, err := resolver(ctx, caller, typed)
			if err != nil {
				return nil, err
			}
			return encode(value)
		},
	}, nil
}

// CustomRegistry is immutable process-wide binding metadata. ForCaller creates
// a request-scoped execution carrying only the generated caller value.
type CustomRegistry struct {
	contract *graphqlcustom.Registry
	bindings map[compilerir.ExtensionID]CustomBinding
}

// CustomCallerCapability is implemented by runtime Caller and by the generated
// application Caller wrapper. System, DB, and transaction handles do not
// implement it and therefore cannot be installed into custom execution.
type CustomCallerCapability interface {
	GolemGraphQLCallerCapability()
}

func NewCustomRegistry(bundle golem.SchemaBundle, supplied ...CustomBinding) (*CustomRegistry, error) {
	compilation, err := compilationFromBundle(bundle)
	if err != nil {
		return nil, err
	}
	contract, err := graphqlcustom.New(compilation)
	if err != nil {
		return nil, err
	}
	bindings := make(map[compilerir.ExtensionID]CustomBinding, len(supplied))
	for _, binding := range supplied {
		id := compilerir.ExtensionID(binding.extensionID)
		if id == "" || binding.invoke == nil {
			return nil, fmt.Errorf("GraphQL custom binding is incomplete")
		}
		if _, duplicate := bindings[id]; duplicate {
			return nil, fmt.Errorf("GraphQL custom binding %s is duplicated", id)
		}
		operation, present := contract.Contract(id)
		if !present {
			return nil, fmt.Errorf("GraphQL custom binding %s is absent from ContractIR", id)
		}
		if compilerir.CustomOperationKind(binding.operation) != operation.Operation || binding.resolver != operation.Resolver {
			return nil, fmt.Errorf("GraphQL custom binding %s does not match its attached resolver", id)
		}
		bindings[id] = binding
	}
	for _, operation := range contract.Operations() {
		if _, present := bindings[operation.ExtensionID]; !present {
			return nil, fmt.Errorf("GraphQL custom operation %s has no generated binding", operation.Name)
		}
	}
	return &CustomRegistry{contract: contract, bindings: bindings}, nil
}

type CallerCustomExecution struct {
	registry *CustomRegistry
	caller   CustomCallerCapability
}

func (registry *CustomRegistry) ForCaller(caller CustomCallerCapability) (*CallerCustomExecution, error) {
	if registry == nil || registry.contract == nil || caller == nil {
		return nil, fmt.Errorf("GraphQL custom execution requires registry and caller")
	}
	value := reflect.ValueOf(caller)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return nil, fmt.Errorf("GraphQL custom execution caller is nil")
	}
	return &CallerCustomExecution{registry: registry, caller: caller}, nil
}

type CustomResult struct{ value any }

func (result CustomResult) Value() any { return cloneCustomValue(result.value) }

// Execute invokes the matched generated resolver exactly once. Mutation
// execution deliberately adds no implicit transaction and performs no replay.
func (execution *CallerCustomExecution) Execute(ctx context.Context, operation CustomOperation, name string, arguments map[string]any) (CustomResult, error) {
	if execution == nil || execution.registry == nil || execution.registry.contract == nil || execution.caller == nil {
		return CustomResult{}, fmt.Errorf("GraphQL custom caller execution is unavailable")
	}
	if ctx == nil {
		return CustomResult{}, fmt.Errorf("GraphQL custom execution context is required")
	}
	request, err := execution.registry.contract.Prepare(compilerir.CustomOperationKind(operation), name, arguments)
	if err != nil {
		return CustomResult{}, err
	}
	binding := execution.registry.bindings[request.ExtensionID()]
	if binding.invoke == nil {
		return CustomResult{}, fmt.Errorf("GraphQL custom binding %s is unavailable", request.ExtensionID())
	}
	internalArguments := request.Arguments()
	publicArguments := make([]CustomArgument, len(internalArguments))
	for index, argument := range internalArguments {
		publicArguments[index] = CustomArgument{name: argument.Name(), value: argument.Value()}
	}
	value, err := binding.invoke(ctx, execution.caller, publicArguments)
	if err != nil {
		return CustomResult{}, err
	}
	finished, err := execution.registry.contract.Finish(request, value)
	if err != nil {
		return CustomResult{}, err
	}
	return CustomResult{value: finished.Value()}, nil
}

func cloneCustomArguments(values []CustomArgument) []CustomArgument {
	result := make([]CustomArgument, len(values))
	for index, value := range values {
		result[index] = CustomArgument{name: value.name, value: cloneCustomValue(value.value)}
	}
	return result
}

func cloneCustomValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return append([]byte(nil), typed...)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneCustomValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = cloneCustomValue(item)
		}
		return result
	default:
		return value
	}
}
