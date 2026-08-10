package graphql

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	"github.com/eleven-am/golem/go/internal/observeexec"
	"github.com/eleven-am/golem/go/observe"
)

type computedExecution struct {
	scope    *graphqlextension.Execution
	bindings map[compilerir.ExtensionID]ComputedBinding
	loaders  map[string]*computedLoader
	maxBatch uint32
}

type computedLoader struct {
	loader  *graphqlextension.Loader[string, any]
	parents map[graphqlextension.RequestKey[string]]golem.RuntimeModelRow
}

type computedFieldFailures struct {
	byIndex map[int]error
}

func (failures *computedFieldFailures) Error() string {
	if failures == nil || len(failures.byIndex) == 0 {
		return "computed field failed"
	}
	return fmt.Sprintf("computed field failed for %d row(s)", len(failures.byIndex))
}

func (failures *computedFieldFailures) ComputedFieldFailures() map[int]error {
	result := make(map[int]error, len(failures.byIndex))
	for index, err := range failures.byIndex {
		result[index] = err
	}
	return result
}

func newComputedExecution(compilation compilerir.CompilationIR, supplied []ComputedBinding, maximum int) (*computedExecution, error) {
	bindings := make(map[compilerir.ExtensionID]ComputedBinding, len(supplied))
	for _, binding := range supplied {
		id := compilerir.ExtensionID(binding.extensionID)
		if id == "" || (binding.resolve == nil) == (binding.batch == nil) {
			return nil, fmt.Errorf("GraphQL computed binding is incomplete")
		}
		if _, duplicate := bindings[id]; duplicate {
			return nil, fmt.Errorf("GraphQL computed binding %s is duplicated", id)
		}
		bindings[id] = binding
	}
	wanted := map[compilerir.ExtensionID]bool{}
	for _, contract := range compilation.Contract.Models {
		for _, field := range contract.Computed {
			wanted[field.ExtensionID] = true
			binding, present := bindings[field.ExtensionID]
			if !present {
				return nil, fmt.Errorf("GraphQL computed field %s.%s has no generated binding", contract.GraphQLName, field.Name)
			}
			if (field.Batch != nil) != binding.batched() {
				return nil, fmt.Errorf("GraphQL computed field %s.%s binding mode does not match its contract", contract.GraphQLName, field.Name)
			}
		}
	}
	for id := range bindings {
		if !wanted[id] {
			return nil, fmt.Errorf("GraphQL computed binding %s is absent from the contract", id)
		}
	}
	if maximum <= 0 {
		maximum = int(graphqlextension.DefaultMaxBatchSize)
	}
	return &computedExecution{scope: graphqlextension.NewExecution(), bindings: bindings, loaders: map[string]*computedLoader{}, maxBatch: uint32(maximum)}, nil
}

func (execution *computedExecution) close() {
	if execution != nil && execution.scope != nil {
		execution.scope.Close()
	}
}

func (execution *computedExecution) invalidateAfterWrite() {
	if execution != nil && execution.scope != nil {
		execution.scope.InvalidateAfterWrite()
	}
}

func (execution *computedExecution) resolve(ctx context.Context, _ compilerir.ModelID, rows []golem.RuntimeModelRow, slot selectset.Slot) ([]any, error) {
	if execution == nil || slot.Computed == nil {
		return nil, fmt.Errorf("computed execution metadata is absent")
	}
	binding, ok := execution.bindings[slot.Computed.ExtensionID]
	if !ok {
		return nil, fmt.Errorf("computed binding %s is absent", slot.Computed.ExtensionID)
	}
	arguments := publicComputedArguments(slot.Computed.Arguments)
	parents := make([]golem.RuntimeModelRow, len(rows))
	for index, row := range rows {
		parent, err := maskedComputedParent(row, slot.Computed.Dependencies)
		if err != nil {
			return nil, fmt.Errorf("computed parent %d: %w", index, err)
		}
		parents[index] = parent
	}
	if slot.Computed.Batch == nil {
		values := make([]any, len(rows))
		failures := &computedFieldFailures{byIndex: map[int]error{}}
		for index, parent := range parents {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			computedContext, observation := observeexec.BeginChild(ctx, parent.ModelID(), observe.KindGraphQL, observe.OperationGraphQLComputed, observe.PhaseFinish)
			value, err := binding.resolve(computedContext, ComputedRequest{Parent: parent, Arguments: cloneComputedArguments(arguments)})
			if observation != nil {
				observation.SetAggregateCount(1)
			}
			finishGraphQLChild(observation, err)
			if err != nil {
				failures.byIndex[index] = fmt.Errorf("computed row %d: %w", index, err)
				continue
			}
			values[index] = value
		}
		if len(failures.byIndex) != 0 {
			return values, failures
		}
		return values, nil
	}
	return execution.resolveBatch(ctx, slot, binding, parents, arguments)
}

func (execution *computedExecution) resolveBatch(ctx context.Context, slot selectset.Slot, binding ComputedBinding, parents []golem.RuntimeModelRow, arguments []ComputedArgument) ([]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity := string(slot.Computed.ExtensionID) + "\x00" + slot.Computed.CanonicalArguments
	entry := execution.loaders[identity]
	if entry == nil {
		entry = &computedLoader{parents: map[graphqlextension.RequestKey[string]]golem.RuntimeModelRow{}}
		maximum := slot.Computed.Batch.MaxBatchSize
		if maximum > execution.maxBatch {
			maximum = execution.maxBatch
		}
		loader, err := graphqlextension.NewLoader[string, any](execution.scope, graphqlextension.LoaderConfig{
			FieldID: slot.Computed.ExtensionID, MaxBatchSize: maximum, MaxPending: graphqlextension.DefaultMaxPending,
		}, func(batchCtx context.Context, keys []graphqlextension.RequestKey[string]) (result map[graphqlextension.RequestKey[string]]graphqlextension.BatchValue[any], resultErr error) {
			parents := make([]ComputedBatchParent, len(keys))
			for index, key := range keys {
				parent, present := entry.parents[key]
				if !present {
					return nil, fmt.Errorf("computed batch parent %q is absent", key.CacheKey)
				}
				parents[index] = ComputedBatchParent{key: key.CacheKey, parent: parent}
			}
			model := golem.ModelID{}
			if len(parents) != 0 {
				model = parents[0].Parent().ModelID()
			}
			batchCtx, observation := observeexec.BeginChild(batchCtx, model, observe.KindGraphQL, observe.OperationGraphQLBatchedComputed, observe.PhaseFinish)
			defer func() {
				if observation != nil {
					observation.SetAggregateCount(int64(len(keys)))
				}
				finishGraphQLChild(observation, resultErr)
			}()
			loaded, loadErr := binding.batch(batchCtx, parents, cloneComputedArguments(arguments))
			if loadErr != nil {
				return nil, loadErr
			}
			if len(loaded) != len(keys) {
				return nil, fmt.Errorf("computed batch returned %d keys for %d requests", len(loaded), len(keys))
			}
			values := make(map[graphqlextension.RequestKey[string]]graphqlextension.BatchValue[any], len(keys))
			for _, key := range keys {
				value, present := loaded[key.CacheKey]
				if !present {
					return nil, fmt.Errorf("computed batch omitted key %q", key.CacheKey)
				}
				values[key] = graphqlextension.BatchValue[any]{Value: value.Value, Err: value.Err}
				delete(entry.parents, key)
			}
			return values, nil
		})
		if err != nil {
			return nil, err
		}
		entry.loader = loader
		execution.loaders[identity] = entry
	}
	values := make([]any, len(parents))
	futures := make([]*graphqlextension.Future[any], len(parents))
	failures := &computedFieldFailures{byIndex: map[int]error{}}
	for index, parent := range parents {
		request := ComputedRequest{Parent: parent, Arguments: cloneComputedArguments(arguments)}
		cacheKey, present, err := binding.cacheKey(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("computed batch key %d: %w", index, err)
		}
		if !present {
			continue
		}
		if cacheKey == "" {
			return nil, fmt.Errorf("computed batch key %d is empty", index)
		}
		key := graphqlextension.RequestKey[string]{Arguments: slot.Computed.CanonicalArguments, CacheKey: cacheKey}
		if _, exists := entry.parents[key]; !exists {
			entry.parents[key] = parent
		}
		future, err := entry.loader.Queue(key)
		if err != nil {
			return nil, err
		}
		futures[index] = future
	}
	for entry.loader.Pending() != 0 {
		if err := entry.loader.Dispatch(ctx); err != nil {
			return nil, err
		}
	}
	for index, future := range futures {
		if future == nil {
			continue
		}
		value, err := future.Await(ctx)
		if err != nil {
			failures.byIndex[index] = fmt.Errorf("computed batch result %d: %w", index, err)
			continue
		}
		values[index] = value
	}
	if len(failures.byIndex) != 0 {
		return values, failures
	}
	return values, nil
}

func maskedComputedParent(row golem.RuntimeModelRow, dependencies []graphqlextension.DependencyAccess) (golem.RuntimeModelRow, error) {
	var scalars []golem.FieldID
	var relations []golem.RuntimeComputedRelationDependency
	for _, dependency := range dependencies {
		field, err := runtimeFieldID(dependency.FieldID)
		if err != nil {
			return golem.RuntimeModelRow{}, err
		}
		switch dependency.Kind {
		case graphqlextension.DependencyMaskedScalar:
			scalars = append(scalars, field)
		case graphqlextension.DependencyMaskedRelation:
			relations = append(relations, golem.RuntimeComputedRelationDependency{Field: field, Occurrence: golem.RuntimeOccurrenceID(dependency.Occurrence)})
		default:
			return golem.RuntimeModelRow{}, fmt.Errorf("computed dependency kind %d is invalid", dependency.Kind)
		}
	}
	return golem.RuntimeComputedDependencyRow(row, scalars, relations)
}

func runtimeFieldID(value compilerir.FieldID) (result golem.FieldID, err error) {
	decoded, err := hex.DecodeString(string(value))
	if err != nil || len(decoded) != len(result) {
		return result, fmt.Errorf("computed field identity %q is invalid", value)
	}
	copy(result[:], decoded)
	return result, nil
}

func publicComputedArguments(values []graphqlextension.BoundArgument) []ComputedArgument {
	result := make([]ComputedArgument, len(values))
	for index, value := range values {
		result[index] = ComputedArgument{Name: value.Name, Value: value.Value}
	}
	return result
}

func cloneComputedArguments(values []ComputedArgument) []ComputedArgument {
	result := make([]ComputedArgument, len(values))
	for index, value := range values {
		result[index] = ComputedArgument{Name: value.Name, Value: cloneComputedArgumentValue(value.Value)}
	}
	return result
}

func cloneComputedArgumentValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return append([]byte(nil), typed...)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneComputedArgumentValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = cloneComputedArgumentValue(item)
		}
		return result
	default:
		return value
	}
}
