package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readbind "github.com/eleven-am/golem/go/internal/read/bind"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
)

// CallerVectors returns cells aligned with the supplied selectors. An absent,
// unreadable, unready or obsolete-space source has a null cell. Each statement
// joins source identities, row/field authorization and managed vector state.
func CallerVectors[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], indexName string, sources ...golem.UniqueSelectorValue[M]) ([]golem.ReadValue[embedding.QueryVector], error) {
	if caller == nil || caller.app == nil || ctx == nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "vectors", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	return readSemanticVectors(ctx, caller.app, descriptor, indexName, sources, func(options []golem.ReadOption[M]) (PreparedRead, error) {
		return prepareCallerFindManyRead(ctx, caller, descriptor, options)
	})
}

func SystemVectors[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], indexName string, sources ...golem.UniqueSelectorValue[M]) ([]golem.ReadValue[embedding.QueryVector], error) {
	if system.app == nil || ctx == nil {
		return nil, fmt.Errorf("P9_SEMANTIC_RUNTIME: system execution is unavailable")
	}
	return readSemanticVectors(ctx, system.app, descriptor, indexName, sources, func(options []golem.ReadOption[M]) (PreparedRead, error) {
		return prepareSystemFindManyRead(system, descriptor, options)
	})
}

func CallerTxVectors[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], indexName string, sources ...golem.UniqueSelectorValue[M]) ([]golem.ReadValue[embedding.QueryVector], error) {
	return transactionOperation(ctx, transaction.binding(), func(ctx context.Context) ([]golem.ReadValue[embedding.QueryVector], error) {
		if transaction == nil || transaction.caller == nil {
			return nil, fmt.Errorf("P4_RUNTIME_TRANSACTION: caller transaction is unavailable")
		}
		return CallerVectors(ctx, transaction.caller, descriptor, indexName, sources...)
	})
}

func SystemTxVectors[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], indexName string, sources ...golem.UniqueSelectorValue[M]) ([]golem.ReadValue[embedding.QueryVector], error) {
	return transactionOperation(ctx, transaction.binding(), func(ctx context.Context) ([]golem.ReadValue[embedding.QueryVector], error) {
		if transaction == nil || transaction.system.app == nil {
			return nil, fmt.Errorf("P4_RUNTIME_TRANSACTION: system transaction is unavailable")
		}
		return SystemVectors(ctx, transaction.system, descriptor, indexName, sources...)
	})
}

func readSemanticVectors[P, A, M any](ctx context.Context, app *App[P, A], descriptor golem.ModelDescriptor[M], indexName string, sources []golem.UniqueSelectorValue[M], prepare func([]golem.ReadOption[M]) (PreparedRead, error)) ([]golem.ReadValue[embedding.QueryVector], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(sources) > semanticruntime.MaximumResults {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic vector source count is %d, above %d", len(sources), semanticruntime.MaximumResults)
	}
	if _, ok := app.semantic.IndexFields(semanticModelID(descriptor.Metadata()), indexName); !ok {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "the requested model has no semantic index with the requested name")
	}
	projection, err := golem.RuntimeSemanticIdentityProjection(descriptor)
	if err != nil {
		return nil, err
	}
	conditions := make([]policyir.Condition, len(sources))
	predicates := make([]golem.Predicate[M], len(sources))
	for i, source := range sources {
		frozen, err := golem.FreezeFindUnique(descriptor, source, projection)
		if err != nil {
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "vectors", descriptor.Metadata().ModelID(), golem.FieldID{}, "semantic source selector is invalid", err)
		}
		bound, err := readbind.Request(frozen, app.registry, app.providers)
		if err != nil {
			if public, ok := publicBindReasonError(app.registry, "vectors", frozen.ModelID(), err); ok {
				return nil, public
			}
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "vectors", frozen.ModelID(), golem.FieldID{}, "semantic source selector is invalid", err)
		}
		var present bool
		conditions[i], present = bound.Where()
		if !present {
			return nil, fmt.Errorf("P9_SEMANTIC_SCHEMA: source selector has no predicate")
		}
		where, _ := frozen.Where()
		predicates[i], err = golem.RuntimeTypedPredicate[M](frozen.ModelID(), where)
		if err != nil {
			return nil, err
		}
	}
	null, _ := golem.RuntimeSemanticVectorCell(embedding.QueryVector{}, false)
	result := make([]golem.ReadValue[embedding.QueryVector], len(sources))
	for i := range result {
		result[i] = null
	}
	// The existing semantic identity chunk also stays below SQLite's compound
	// SELECT term ceiling. Capacity failures can split a batch further.
	var readChunk func(int, int) error
	readChunk = func(start, end int) error {
		prepared, err := prepare([]golem.ReadOption[M]{golem.Where(golem.Or(predicates[start:end]...)), projection, golem.Take[M](len(sources))})
		if err != nil {
			return err
		}
		vectors, err := readSemanticVectorBatch(ctx, app, indexName, prepared, conditions[start:end], len(sources))
		if err != nil {
			if end-start > 1 && semanticVectorCapacityExceeded(err) {
				middle := start + (end-start)/2
				if err := readChunk(start, middle); err != nil {
					return err
				}
				return readChunk(middle, end)
			}
			var rendered *readsql.Error
			if errors.As(err, &rendered) {
				return publicReadRenderError("vectors", prepared.ModelID(), "semantic vector sources could not be read", err)
			}
			var public *golem.Error
			if errors.As(err, &public) && public.Operation != "vectors" {
				return rebasePublicError(public, "vectors", prepared.ModelID(), public.Message, err)
			}
			return err
		}
		for position, vector := range vectors {
			cell, err := golem.RuntimeSemanticVectorCell(vector, true)
			if err != nil {
				return err
			}
			result[start+position] = cell
		}
		return nil
	}
	for start := 0; start < len(sources); start += semanticIdentityChunk {
		if err := readChunk(start, min(start+semanticIdentityChunk, len(sources))); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func readSemanticVectorBatch[P, A any](ctx context.Context, app *App[P, A], indexName string, prepared PreparedRead, sources []policyir.Condition, total int) (map[int]embedding.QueryVector, error) {
	if err := prepared.executor.requireAdmitted(ctx); err != nil {
		return nil, err
	}
	planned, err := preparePlan(prepared, app.registry, app.readLimits.plan)
	if err != nil {
		return nil, publicPlanError(prepared, err)
	}
	if err := validateSemanticPlanTake(prepared, planned, "vectors", total); err != nil {
		return nil, err
	}
	requests, err := readsql.RenderSemanticSources(planned, app.registry, app.provider, app.capabilities, sources)
	if err != nil {
		return nil, err
	}
	candidates, err := renderSemanticCandidates(app, prepared, planned, indexName, len(requests.Args())+1)
	if err != nil {
		return nil, err
	}
	queryer, err := prepared.executor.queryerFor(app.database)
	if err != nil {
		return nil, err
	}
	columns := requests.Columns()
	names := make([]string, len(columns))
	for i, column := range columns {
		names[i] = string(column)
	}
	return app.semantic.VectorsOn(ctx, queryer, semanticModelIDFromPlan(planned), indexName, candidates.candidates, semanticruntime.VectorSources{SQL: requests.SQL(), Args: requests.Args(), Columns: names, PositionColumn: string(requests.PositionColumn()), Count: requests.Count()})
}

func semanticVectorCapacityExceeded(err error) bool {
	if readsql.StatementCapacityExceeded(err) {
		return true
	}
	var failure *golem.Error
	return errors.As(err, &failure) && failure.Code == golem.CodeLimitExceeded && (failure.Limit == golem.LimitStatementParameters || failure.Limit == golem.LimitStatementSize)
}
