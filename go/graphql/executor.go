package graphql

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlbind "github.com/eleven-am/golem/go/internal/graphql/bind"
	graphqloperation "github.com/eleven-am/golem/go/internal/graphql/operation"
	"github.com/eleven-am/golem/go/internal/observeexec"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
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

type GeneratedExecutorConfig[P any] struct {
	Bundle              golem.SchemaBundle
	Limits              Limits
	BeginCaller         BeginCaller[P]
	ComputedBindings    []ComputedBinding
	CustomBindings      []CustomBinding
	ReportInternalError func(context.Context, error)
}

type generatedExecutor[P any] struct {
	compiler         *graphqloperation.Compiler
	begin            BeginCaller[P]
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
	compilation, err := compilationFromBundle(config.Bundle)
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
	return &generatedExecutor[P]{compiler: compiler, begin: config.BeginCaller, report: config.ReportInternalError, compilation: compilation, computed: bindings, maxComputedBatch: limits.MaxComputedBatchSize, custom: custom}, nil
}

func (executor *generatedExecutor[P]) Execute(ctx context.Context, principal P, operation Operation) Response {
	if executor == nil || executor.compiler == nil || executor.begin == nil {
		return Response{Errors: []Error{publicError("INTERNAL_SERVER_ERROR", "internal server error")}}
	}
	compiled, err := executor.compiler.Compile(operation.Document, operation.Definition, operation.Variables)
	if err != nil {
		return Response{Errors: []Error{publicError("BAD_USER_INPUT", "GraphQL operation could not be bound")}}
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
		return executor.executeMutationOrder(ctx, compiled, mutationCaller, customCaller, computed)
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
				value := result.Value()
				hydration, semantic, hydrateErr := executor.compiler.PrepareSemanticCustomHydration(root, value)
				if hydrateErr == nil && semantic {
					value = []any{}
					if hydration.Len() != 0 {
						rows := make([]golem.RuntimeModelRow, 0, hydration.Len())
						chunk := hydration.Len()
						for start := 0; start < hydration.Len() && hydrateErr == nil; {
							end := start + chunk
							if end > hydration.Len() {
								end = hydration.Len()
							}
							var request golem.FrozenReadRequest
							request, hydrateErr = executor.compiler.SemanticCustomHydrationRequest(hydration, start, end)
							if hydrateErr != nil {
								break
							}
							var batch []golem.RuntimeModelRow
							batch, hydrateErr = caller.ExecuteFrozenRead(customContext, request)
							if hydrateErr != nil {
								reduced, retry := reduceSemanticHydrationChunk(start, end, hydrateErr)
								if retry {
									chunk, hydrateErr = reduced-start, nil
									continue
								}
								break
							}
							rows = append(rows, batch...)
							start = end
						}
						if hydrateErr == nil {
							value, hydrateErr = executor.compiler.FinishSemanticCustomHydration(root, hydration.Order(), rows)
						}
					}
				}
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

func reduceSemanticHydrationChunk(start, end int, err error) (int, bool) {
	if end-start <= 1 || !readsql.StatementCapacityExceeded(err) {
		return end, false
	}
	return start + (end-start)/2, true
}

func (executor *generatedExecutor[P]) executeMutationOrder(ctx context.Context, compiled graphqloperation.Result, mutationCaller CallerMutationExecution, custom *CallerCustomExecution, computed *computedExecution) Response {
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
			if encodeErr != nil {
				response.Errors = append(response.Errors, PresentError(ctx, encodeErr, []any{root.ResponseName}, executor.report))
				response.Data = nil
				return response
			}
			data[root.ResponseName] = encoded
			response.Errors = appendComputedFailures(ctx, response.Errors, failures, executor.report)
			if computedFailurePropagatesToRoot(failures) {
				response.Data = nil
				return response
			}
		case graphqloperation.RootCustomMutation:
			root := compiled.Custom[reference.Index]
			customContext, observation := observeexec.BeginChild(ctx, golem.ModelID{}, observe.KindGraphQL, observe.OperationGraphQLCustomMutation, observe.PhaseFinish)
			result, err := custom.Execute(customContext, CustomMutation, root.Name, root.Arguments)
			if err != nil {
				finishGraphQLChild(observation, err)
				response.Errors = append(response.Errors, PresentError(ctx, err, []any{root.ResponseName}, executor.report))
				data[root.ResponseName] = nil
				if !root.Result.Nullable {
					response.Data = nil
					return response
				}
				continue
			}
			// A successful custom mutation may have committed writes even when its
			// result later fails GraphQL encoding. Invalidate before encoding so
			// subsequent serial mutation roots cannot observe stale loader state.
			computed.invalidateAfterWrite()
			encoded, failures, encodeErr := executor.compiler.EncodeCustomWithComputedPartial(customContext, root, result.Value(), computed.resolve)
			finishGraphQLChild(observation, encodeErr)
			if encodeErr != nil {
				response.Errors = append(response.Errors, PresentError(ctx, encodeErr, []any{root.ResponseName}, executor.report))
				data[root.ResponseName] = nil
				if !root.Result.Nullable {
					response.Data = nil
					return response
				}
				continue
			}
			data[root.ResponseName] = encoded
			response.Errors = appendComputedFailures(ctx, response.Errors, failures, executor.report)
			if computedFailurePropagatesToRoot(failures) {
				response.Data = nil
				return response
			}
		}
	}
	return response
}

func computedFailurePropagatesToRoot(failures []graphqloperation.ComputedFieldFailure) bool {
	for _, failure := range failures {
		if failure.PropagatesToRoot {
			return true
		}
	}
	return false
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

func compilationFromBundle(bundle golem.SchemaBundle) (compilerir.CompilationIR, error) {
	if bundle.FormatVersion() != golem.SchemaBundleFormatVersion || bundle.GenerationDigest() == (golem.SchemaDigest{}) {
		return compilerir.CompilationIR{}, fmt.Errorf("GraphQL schema bundle is absent or unsupported")
	}
	modelDocument, contractDocument := bundle.Model(), bundle.Contract()
	if modelDocument.FormatVersion() != uint32(compilerir.ModelFormatVersion) || contractDocument.FormatVersion() != uint32(compilerir.ContractFormatVersion) || modelDocument.CanonicalVersion() != uint32(compilerir.CanonicalFormatVersion) || contractDocument.CanonicalVersion() != uint32(compilerir.CanonicalFormatVersion) {
		return compilerir.CompilationIR{}, fmt.Errorf("GraphQL schema bundle document version is unsupported")
	}
	var compilation compilerir.CompilationIR
	if err := json.Unmarshal(modelDocument.Bytes(), &compilation.Model); err != nil {
		return compilerir.CompilationIR{}, fmt.Errorf("GraphQL model document is invalid: %w", err)
	}
	if err := json.Unmarshal(contractDocument.Bytes(), &compilation.Contract); err != nil {
		return compilerir.CompilationIR{}, fmt.Errorf("GraphQL contract document is invalid: %w", err)
	}
	modelCanonical, err := compilerir.CanonicalModel(compilation.Model)
	if err != nil || string(modelCanonical) != string(modelDocument.Bytes()) {
		return compilerir.CompilationIR{}, fmt.Errorf("GraphQL model document is not canonical")
	}
	contractCanonical, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil || string(contractCanonical) != string(contractDocument.Bytes()) {
		return compilerir.CompilationIR{}, fmt.Errorf("GraphQL contract document is not canonical")
	}
	modelFingerprint, err := compilerir.ModelFingerprint(compilation.Model)
	if err != nil || schemaDigest(modelFingerprint) != modelDocument.Fingerprint() {
		return compilerir.CompilationIR{}, fmt.Errorf("GraphQL model fingerprint does not match its document")
	}
	contractFingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil || schemaDigest(contractFingerprint) != contractDocument.Fingerprint() {
		return compilerir.CompilationIR{}, fmt.Errorf("GraphQL contract fingerprint does not match its document")
	}
	return compilation, nil
}

func schemaDigest(fingerprint compilerir.Fingerprint) (result golem.SchemaDigest) {
	decoded, err := hex.DecodeString(string(fingerprint))
	if err == nil && len(decoded) == len(result) {
		copy(result[:], decoded)
	}
	return result
}

func errOr(err error, message string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("%s", message)
}
