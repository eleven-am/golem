package graphql

import (
	"context"
	"errors"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlbind "github.com/eleven-am/golem/go/internal/graphql/bind"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	"github.com/eleven-am/golem/go/internal/observeexec"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/observe"
)

// CallerExecution is the caller-only runtime capability accepted by generated
// GraphQL. *runtime.Caller implements it directly; System deliberately does
// not. One instance is created and shared by all roots in one operation.
type CallerExecution interface {
	ExecuteFrozenRead(context.Context, golem.FrozenReadRequest) ([]golem.RuntimeModelRow, error)
}

// CallerAnalyticsExecution is required only when the selected operation
// contains an explicitly generated P6 analytics root.
type CallerAnalyticsExecution interface {
	ExecuteFrozenAnalytics(context.Context, golem.FrozenAnalyticsRequest) ([][]golem.RuntimeAnalyticsCell, error)
}

type BeginCaller[P any] func(context.Context, P) (CallerExecution, error)

// AuthenticatePrincipal resolves a principal exactly as BeginCaller does, but
// builds no policy and touches no database. The generated executor uses it for
// operations that select only __schema, __type or __typename roots.
type AuthenticatePrincipal[P any] func(context.Context, P) error

type GeneratedExecutorConfig[P any] struct {
	Bundle                golem.SchemaBundle
	Limits                Limits
	BeginCaller           BeginCaller[P]
	ComputedBindings      []ComputedBinding
	CustomBindings        []CustomBinding
	ReportInternalError   func(context.Context, error)
	AuthenticatePrincipal AuthenticatePrincipal[P]
}

type generatedExecutor[P any] struct {
	compiler         *graphqloperation.Compiler
	begin            BeginCaller[P]
	authenticate     AuthenticatePrincipal[P]
	report           func(context.Context, error)
	compilation      compilerir.CompilationIR
	computed         []ComputedBinding
	maxComputedBatch int
	custom           *CustomRegistry
}

func NewGeneratedExecutor[P any](config GeneratedExecutorConfig[P]) (Executor[P], error) {
	if config.BeginCaller == nil || config.ReportInternalError == nil {
		return nil, fmt.Errorf("GraphQL generated executor requires caller factory and trusted error reporter")
	}
	compilation, err := policyschema.DecodeActive(config.Bundle)
	if err != nil {
		return nil, err
	}
	limits, err := NormalizeLimits(config.Limits)
	if err != nil {
		return nil, err
	}
	compiler, err := graphqloperation.New(compilation, graphqloperation.Limits{
		Bind:  graphqlbind.Limits{MaxInputDepth: limits.MaxInputDepth, MaxInputNodes: limits.MaxInputNodes, MaxListItems: limits.MaxListItems, MaxPageSize: limits.MaxPageSize},
		Depth: limits.MaxDepth, Fields: limits.MaxSelectedFields, Aliases: limits.MaxAliases, ListItems: limits.MaxListItems, MaxGroups: limits.MaxGroups,
	})
	if err != nil {
		return nil, fmt.Errorf("GraphQL operation compiler: %w", err)
	}
	computed, err := newComputedExecution(compilation, config.ComputedBindings, limits.MaxComputedBatchSize)
	if err != nil {
		return nil, err
	}
	computed.close()
	custom, err := NewCustomRegistry(config.Bundle, config.CustomBindings...)
	if err != nil {
		return nil, err
	}
	bindings := append([]ComputedBinding(nil), config.ComputedBindings...)
	return &generatedExecutor[P]{compiler: compiler, begin: config.BeginCaller, authenticate: config.AuthenticatePrincipal, report: config.ReportInternalError, compilation: compilation, computed: bindings, maxComputedBatch: limits.MaxComputedBatchSize, custom: custom}, nil
}

func (executor *generatedExecutor[P]) Execute(ctx context.Context, principal P, operation Operation) Response {
	if executor == nil || executor.compiler == nil || executor.begin == nil {
		return Response{Errors: []Error{publicError("INTERNAL_SERVER_ERROR", "internal server error")}}
	}
	compiled, err := executor.compiler.Compile(operation.Document, operation.Definition, operation.Variables)
	if err != nil {
		return Response{Errors: []Error{PresentError(ctx, err, nil, executor.report)}}
	}
	if len(compiled.Order) == 0 && compiled.Event == nil && executor.authenticate != nil {
		if err := executor.authenticate(ctx, principal); err != nil {
			return Response{Errors: []Error{PresentError(ctx, err, nil, executor.report)}}
		}
		return Response{Data: map[string]any{}}
	}
	caller, err := executor.begin(ctx, principal)
	if err != nil || caller == nil {
		return Response{Errors: []Error{PresentError(ctx, errOr(err, "caller execution is unavailable"), nil, executor.report)}}
	}
	computed, err := newComputedExecution(executor.compilation, executor.computed, executor.maxComputedBatch)
	if err != nil {
		return Response{Errors: []Error{PresentError(ctx, err, nil, executor.report)}}
	}
	defer computed.close()
	hydrator := resultHydrator{compiler: executor.compiler, caller: caller}
	computed.hydrator = &hydrator
	var customCaller *CallerCustomExecution
	if len(compiled.Custom) != 0 {
		capability, ok := caller.(CustomCallerCapability)
		if !ok {
			return Response{Errors: []Error{publicError("INTERNAL_SERVER_ERROR", "internal server error")}}
		}
		customCaller, err = executor.custom.ForCaller(capability)
		if err != nil {
			return Response{Errors: []Error{PresentError(ctx, err, nil, executor.report)}}
		}
	}
	var analyticsCaller CallerAnalyticsExecution
	if len(compiled.Analytics) != 0 {
		capability, ok := caller.(CallerAnalyticsExecution)
		if !ok {
			return Response{Errors: []Error{publicError("INTERNAL_SERVER_ERROR", "internal server error")}}
		}
		analyticsCaller = capability
	}
	if operation.Definition.Operation == "mutation" {
		mutationCaller, ok := caller.(CallerMutationExecution)
		if !ok && len(compiled.Mutations) != 0 {
			return Response{Errors: []Error{publicError("INTERNAL_SERVER_ERROR", "internal server error")}}
		}
		return executor.executeMutationOrder(ctx, compiled, hydrator, mutationCaller, customCaller, computed)
	}
	data := make(map[string]any, len(compiled.Reads))
	response := Response{Data: data}
	for _, reference := range compiled.Order {
		if reference.Kind == graphqloperation.RootAnalytics {
			root := compiled.Analytics[reference.Index]
			rows, executeErr := analyticsCaller.ExecuteFrozenAnalytics(ctx, root.Request)
			if executeErr != nil {
				response.Errors = append(response.Errors, PresentError(ctx, executeErr, []any{root.ResponseName}, executor.report))
				data[root.ResponseName] = nil
				continue
			}
			encoded, encodeErr := executor.compiler.EncodeAnalytics(root, rows)
			if encodeErr != nil {
				response.Errors = append(response.Errors, PresentError(ctx, encodeErr, []any{root.ResponseName}, executor.report))
				data[root.ResponseName] = nil
				continue
			}
			data[root.ResponseName] = encoded
			continue
		}
		if reference.Kind == graphqloperation.RootCustomQuery {
			root := compiled.Custom[reference.Index]
			customContext, observation := observeexec.BeginChild(ctx, golem.ModelID{}, observe.KindGraphQL, observe.OperationGraphQLCustomQuery, observe.PhaseFinish)
			result, executeErr := customCaller.Execute(customContext, CustomQuery, root.Name, root.Arguments)
			if executeErr != nil {
				finishGraphQLChild(observation, executeErr)
				response.Errors = append(response.Errors, PresentError(ctx, executeErr, []any{root.ResponseName}, executor.report))
				data[root.ResponseName] = nil
			} else {
				value, _, hydrateErr := hydrator.root(customContext, root, result.Value())
				if hydrateErr != nil {
					finishGraphQLChild(observation, hydrateErr)
					response.Errors = append(response.Errors, PresentError(ctx, hydrateErr, []any{root.ResponseName}, executor.report))
					data[root.ResponseName] = nil
					continue
				}
				encoded, failures, encodeErr := executor.compiler.EncodeCustomWithComputedPartial(customContext, root, value, computed.resolve)
				finishGraphQLChild(observation, encodeErr)
				if encodeErr != nil {
					response.Errors = append(response.Errors, PresentError(ctx, encodeErr, []any{root.ResponseName}, executor.report))
					data[root.ResponseName] = nil
				} else {
					data[root.ResponseName] = encoded
					response.Errors = appendComputedFailures(ctx, response.Errors, failures, executor.report)
				}
			}
			continue
		}
		if reference.Kind != graphqloperation.RootRead {
			continue
		}
		root := compiled.Reads[reference.Index]
		rows, executeErr := caller.ExecuteFrozenRead(ctx, root.Frozen)
		if executeErr != nil {
			var failure *golem.Error
			if errors.As(executeErr, &failure) && failure.Code == golem.CodeNotFound {
				data[root.ResponseName] = nil
				continue
			}
			response.Errors = append(response.Errors, PresentError(ctx, executeErr, []any{root.ResponseName}, executor.report))
			data[root.ResponseName] = nil
			continue
		}
		encoded, failures, encodeErr := executor.compiler.EncodeReadWithComputedPartial(ctx, root, rows, computed.resolve)
		if encodeErr != nil {
			response.Errors = append(response.Errors, PresentError(ctx, encodeErr, []any{root.ResponseName}, executor.report))
			data[root.ResponseName] = nil
			continue
		}
		data[root.ResponseName] = encoded
		response.Errors = appendComputedFailures(ctx, response.Errors, failures, executor.report)
	}
	return response
}

func (executor *generatedExecutor[P]) executeMutationOrder(ctx context.Context, compiled graphqloperation.Result, hydrator resultHydrator, mutationCaller CallerMutationExecution, custom *CallerCustomExecution, computed *computedExecution) Response {
	response := Response{Data: map[string]any{}}
	data := response.Data.(map[string]any)
	for _, reference := range compiled.Order {
		switch reference.Kind {
		case graphqloperation.RootMutation:
			root := compiled.Mutations[reference.Index]
			result, err := (invalidatingMutationCaller{CallerMutationExecution: mutationCaller, invalidate: computed.invalidateAfterWrite}).ExecuteFrozenMutation(ctx, root.Frozen)
			if err != nil {
				response.Errors = append(response.Errors, PresentError(ctx, err, []any{root.ResponseName}, executor.report))
				response.Data = nil
				return response
			}
			encoded, failures, encodeErr := executor.compiler.EncodeMutationWithComputedPartial(ctx, root, result, computed.resolve)
			if encodeErr == nil {
				encodeErr = propagatedComputedFailure(failures)
			}
			if encodeErr != nil {
				response.Errors = append(response.Errors, presentCommittedResult(ctx, "mutation "+root.ResponseName, encodeErr, []any{root.ResponseName}, executor.report))
				response.Data = nil
				return response
			}
			data[root.ResponseName] = encoded
			response.Errors = appendComputedFailures(ctx, response.Errors, failures, executor.report)
		case graphqloperation.RootCustomMutation:
			root := compiled.Custom[reference.Index]
			customContext, observation := observeexec.BeginChild(ctx, golem.ModelID{}, observe.KindGraphQL, observe.OperationGraphQLCustomMutation, observe.PhaseFinish)
			path := []any{root.ResponseName}
			result, err := custom.Execute(customContext, CustomMutation, root.Name, root.Arguments)
			var encoded any
			var failures []graphqloperation.ComputedFieldFailure
			var resultFailure *CustomResultError
			var presented Error
			if err != nil && !errors.As(err, &resultFailure) {
				presented = PresentError(ctx, err, path, executor.report)
			} else {
				computed.invalidateAfterWrite()
				if err == nil {
					var value any
					var absent int
					value, absent, err = hydrator.root(customContext, root, result.Value())
					if err == nil && absent != 0 {
						err = &unreadableResult{count: absent}
					}
					if err == nil {
						encoded, failures, err = executor.compiler.EncodeCustomWithComputedPartial(customContext, root, value, computed.resolve)
						if err == nil {
							err = propagatedComputedFailure(failures)
						}
					}
				}
				if err != nil {
					presented = presentCommittedResult(ctx, "custom mutation "+root.Name, err, path, executor.report)
				}
			}
			finishGraphQLChild(observation, err)
			if err != nil {
				response.Errors = append(response.Errors, presented)
				data[root.ResponseName] = nil
				if !root.Result.Nullable {
					response.Data = nil
					return response
				}
				continue
			}
			data[root.ResponseName] = encoded
			response.Errors = appendComputedFailures(ctx, response.Errors, failures, executor.report)
		}
	}
	return response
}

func propagatedComputedFailure(failures []graphqloperation.ComputedFieldFailure) error {
	for _, failure := range failures {
		if failure.PropagatesToRoot {
			return failure.Err
		}
	}
	return nil
}

func appendComputedFailures(ctx context.Context, target []Error, failures []graphqloperation.ComputedFieldFailure, report func(context.Context, error)) []Error {
	for _, failure := range failures {
		target = append(target, PresentError(ctx, failure.Err, failure.Path, report))
	}
	return target
}

type invalidatingMutationCaller struct {
	CallerMutationExecution
	invalidate func()
}

func (caller invalidatingMutationCaller) ExecuteFrozenMutation(ctx context.Context, request golem.RuntimeMutationRequest) (golem.RuntimeMutationResult, error) {
	result, err := caller.CallerMutationExecution.ExecuteFrozenMutation(ctx, request)
	if err == nil && caller.invalidate != nil {
		caller.invalidate()
	}
	return result, err
}

func errOr(err error, message string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("%s", message)
}
