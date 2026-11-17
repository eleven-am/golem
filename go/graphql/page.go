package graphql

import (
	"context"
	"fmt"
	"reflect"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"

	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/typedvalue"
)

type pageDispatcher interface {
	GolemGraphQLDispatchPage(context.Context, string, any, map[string]any, golem.PageInput, func(context.Context, golem.RuntimePageState, map[string]any) (golem.PageBatch[any], error)) (golem.RuntimePageResult, error)
}

type pageCustomValue struct {
	value   map[string]any
	pending golem.RuntimePageResult
}

// BindGeneratedPageQueryContract connects the generated typed resolver to the
// registered runtime page dispatcher. The ordinary custom-query binder cannot
// manufacture a continuation or substitute a different pagination contract.
func BindGeneratedPageQueryContract[C, A, I any](bundle golem.SchemaBundle, spec CustomBindingSpec, resolver func(context.Context, C, golem.PageRequest[A]) (golem.PageBatch[I], error)) (CustomBinding, error) {
	if resolver == nil || spec.ExtensionID == "" || spec.ResolverPackage == "" || spec.ResolverName == "" {
		return CustomBinding{}, fmt.Errorf("GraphQL page binding requires static identity and resolver")
	}
	compilation, err := policyschema.DecodeActive(bundle)
	if err != nil {
		return CustomBinding{}, err
	}
	var operation compilerir.CustomOperationContractIR
	for _, candidate := range compilation.Contract.CustomOperations {
		if string(candidate.ExtensionID) == spec.ExtensionID {
			operation = candidate
			break
		}
	}
	if operation.Pagination == nil || operation.Operation != compilerir.CustomOperationQuery || operation.Resolver.PackagePath != spec.ResolverPackage || operation.Resolver.Name != spec.ResolverName || operation.Resolver.Kind != "customquery" {
		return CustomBinding{}, fmt.Errorf("GraphQL page binding differs from its registered query contract")
	}
	page := *operation.Pagination
	itemList := compilerir.GraphQLTypeIR{Kind: compilerir.GraphQLTypeList, Element: &page.Item, MaxItems: page.MaxTake}
	return CustomBinding{extensionID: spec.ExtensionID, operation: CustomQuery, resolver: operation.Resolver, page: true,
		invoke: func(ctx context.Context, rawCaller any, arguments []CustomArgument) (any, error) {
			dispatcher, ok := rawCaller.(pageDispatcher)
			if !ok {
				return nil, fmt.Errorf("GraphQL page execution requires its registered runtime dispatcher")
			}
			input, ordinary, err := pageInputArguments(operation.Name, arguments)
			if err != nil {
				return nil, err
			}
			pending, err := dispatcher.GolemGraphQLDispatchPage(ctx, spec.ExtensionID, resolver, ordinary, input, func(ctx context.Context, state golem.RuntimePageState, owned map[string]any) (golem.PageBatch[any], error) {
				var zero golem.PageBatch[any]
				// Decode the same prepared values the runtime canonicalized for
				// scope binding, not the caller's original mutable argument map.
				ordered := make([]CustomArgument, 0, len(operation.Arguments)-2)
				for _, declaration := range operation.Arguments {
					if declaration.Name == "take" || declaration.Name == "after" {
						continue
					}
					value, present := owned[declaration.Name]
					if !present && declaration.PresenceAware {
						value = golem.RuntimeOmittedInputValue()
					}
					ordered = append(ordered, CustomArgument{name: declaration.Name, value: value, present: present})
				}
				typed, err := generatedCustomArguments[A](ordered, nil)
				if err != nil {
					return zero, fmt.Errorf("GraphQL page arguments: %w", err)
				}
				callerValue := rawCaller
				if provider, ok := rawCaller.(interface{ GolemGraphQLCustomCallerValue() any }); ok {
					callerValue = provider.GolemGraphQLCustomCallerValue()
				}
				caller, ok := callerValue.(C)
				if !ok {
					return zero, fmt.Errorf("GraphQL page resolver caller type does not match generated binding")
				}
				request := golem.RuntimePageRequestValue(typed, state)
				batch, err := resolver(ctx, caller, request)
				if err != nil {
					return zero, err
				}
				items, consumed, position, more := golem.RuntimePageBatchParts(batch)
				// Bound traversal by this execution's admitted take before any
				// item is traversed. Runtime separately owns candidate/progress
				// checks for this exact execution.
				bounded := itemList
				if request.Take() < bounded.MaxItems {
					bounded.MaxItems = request.Take()
				}
				if err := typedvalue.CheckListBounds(bounded, reflect.ValueOf(items), compilation.Contract.GraphQLTypes); err != nil {
					return zero, &CustomResultError{cause: typedvalue.ListItemsError("customQuery", "custom page "+operation.Name, err)}
				}
				encoded := make([]any, len(items))
				for index, item := range items {
					value, err := normalizeGeneratedResult[any](compilation, page.Item, item, false)
					if err != nil {
						return zero, &CustomResultError{cause: fmt.Errorf("GraphQL page item %d: %w", index, err)}
					}
					encoded[index] = value
				}
				if !more {
					position = nil
				}
				return golem.PageItems(encoded, consumed, position), nil
			})
			if err != nil {
				return nil, err
			}
			return pageCustomValue{value: map[string]any{"items": pending.Items(), "next": nil}, pending: pending}, nil
		},
	}, nil
}

func pageInputArguments(operation string, arguments []CustomArgument) (golem.PageInput, map[string]any, error) {
	var input golem.PageInput
	ordinary := make(map[string]any, len(arguments))
	for _, argument := range arguments {
		switch argument.name {
		case "take":
			if argument.value != nil {
				value, ok := argument.value.(int32)
				if !ok || value <= 0 {
					return input, nil, golem.InputError(golem.ReasonInvalidValue, operation, "take", "page take must be a positive Int", nil)
				}
				input.Take = uint32(value)
			}
		case "after":
			if argument.value != nil {
				value, ok := argument.value.(string)
				if !ok {
					return input, nil, fmt.Errorf("GraphQL page continuation is not a String")
				}
				input.After = value
			}
		default:
			if argument.present {
				ordinary[argument.name] = argument.Value()
			}
		}
	}
	return input, ordinary, nil
}

func (result CustomResult) finalizeValue(compiler *graphqloperation.Compiler, root graphqloperation.CustomRoot, value any) (any, error) {
	if result.page == nil {
		return value, nil
	}
	return compiler.FinalizePageValue(root, value, result.page.Finalize)
}

func (result CustomResult) prepareValue(compiler *graphqloperation.Compiler, root graphqloperation.CustomRoot, value any) (any, error) {
	if result.page == nil {
		return value, nil
	}
	return compiler.PreparePageValue(root, value)
}
