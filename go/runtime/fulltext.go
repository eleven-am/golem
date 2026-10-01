package runtime

import (
	"context"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	fulltextruntime "github.com/eleven-am/golem/go/internal/fulltext/runtime"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyresolve "github.com/eleven-am/golem/go/internal/policy/resolve"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
)

// CallerTextSearch ranks rows only after applying caller row policy, caller
// predicates, and the read condition of every indexed field.
func CallerTextSearch[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, predicates ...golem.Predicate[M]) ([]golem.FullTextResult[M], error) {
	return callerTextSearch(ctx, caller, descriptor, indexName, query, take, nil, predicates)
}

func CallerTextSearchSelect[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, projection golem.Projection[M], predicates ...golem.Predicate[M]) ([]golem.FullTextResult[M], error) {
	return callerTextSearch(ctx, caller, descriptor, indexName, query, take, []golem.Projection[M]{projection}, predicates)
}

func callerTextSearch[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, projections []golem.Projection[M], predicates []golem.Predicate[M]) ([]golem.FullTextResult[M], error) {
	if caller == nil || caller.app == nil || ctx == nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "textSearch", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	readOptions, err := fullTextReadOptions(predicates, query, take, projections...)
	if err != nil {
		return nil, publicFullTextQueryError(descriptor.Metadata().ModelID(), err)
	}
	prepared, err := prepareCallerFindManyRead(ctx, caller, descriptor, readOptions)
	if err != nil {
		return nil, err
	}
	return rankFullTextRows(ctx, caller.app, descriptor, prepared, indexName, query, take)
}

// SystemTextSearch executes the same ranked query without caller policy.
func SystemTextSearch[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, predicates ...golem.Predicate[M]) ([]golem.FullTextResult[M], error) {
	return systemTextSearch(ctx, system, descriptor, indexName, query, take, nil, predicates)
}

func SystemTextSearchSelect[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, projection golem.Projection[M], predicates ...golem.Predicate[M]) ([]golem.FullTextResult[M], error) {
	return systemTextSearch(ctx, system, descriptor, indexName, query, take, []golem.Projection[M]{projection}, predicates)
}

func systemTextSearch[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, projections []golem.Projection[M], predicates []golem.Predicate[M]) ([]golem.FullTextResult[M], error) {
	if system.app == nil || ctx == nil {
		return nil, fmt.Errorf("P9_FULLTEXT_RUNTIME: system execution is unavailable")
	}
	readOptions, err := fullTextReadOptions(predicates, query, take, projections...)
	if err != nil {
		return nil, publicFullTextQueryError(descriptor.Metadata().ModelID(), err)
	}
	prepared, err := prepareSystemFindManyRead(system, descriptor, readOptions)
	if err != nil {
		return nil, err
	}
	return rankFullTextRows(ctx, system.app, descriptor, prepared, indexName, query, take)
}

func CallerTxTextSearch[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, predicates ...golem.Predicate[M]) ([]golem.FullTextResult[M], error) {
	if transaction == nil || transaction.caller == nil {
		return nil, fmt.Errorf("P4_RUNTIME_TRANSACTION: caller transaction is unavailable")
	}
	return CallerTextSearch(ctx, transaction.caller, descriptor, indexName, query, take, predicates...)
}

func CallerTxTextSearchSelect[P, A, M any](ctx context.Context, transaction *CallerTx[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, projection golem.Projection[M], predicates ...golem.Predicate[M]) ([]golem.FullTextResult[M], error) {
	if transaction == nil || transaction.caller == nil {
		return nil, fmt.Errorf("P4_RUNTIME_TRANSACTION: caller transaction is unavailable")
	}
	return CallerTextSearchSelect(ctx, transaction.caller, descriptor, indexName, query, take, projection, predicates...)
}

func SystemTxTextSearch[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, predicates ...golem.Predicate[M]) ([]golem.FullTextResult[M], error) {
	if transaction == nil || transaction.system.app == nil {
		return nil, fmt.Errorf("P4_RUNTIME_TRANSACTION: system transaction is unavailable")
	}
	return SystemTextSearch(ctx, transaction.system, descriptor, indexName, query, take, predicates...)
}

func SystemTxTextSearchSelect[P, A, M any](ctx context.Context, transaction *SystemTx[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, projection golem.Projection[M], predicates ...golem.Predicate[M]) ([]golem.FullTextResult[M], error) {
	if transaction == nil || transaction.system.app == nil {
		return nil, fmt.Errorf("P4_RUNTIME_TRANSACTION: system transaction is unavailable")
	}
	return SystemTextSearchSelect(ctx, transaction.system, descriptor, indexName, query, take, projection, predicates...)
}

func fullTextReadOptions[M any](predicates []golem.Predicate[M], query string, take int, projections ...golem.Projection[M]) ([]golem.ReadOption[M], error) {
	if query == "" {
		return nil, fulltextruntime.InvalidQuery("full-text query is empty")
	}
	if take < 1 || take > fulltextruntime.MaximumResults {
		return nil, fulltextruntime.InvalidQuery("full-text result limit is %d, outside 1..%d", take, fulltextruntime.MaximumResults)
	}
	options, err := golem.RuntimeTextSearchReadOptions(predicates, projections...)
	if err != nil {
		return nil, fulltextruntime.InvalidQuery("%s", err.Error())
	}
	options = append(options, golem.Take[M](take))
	return options, nil
}

func rankFullTextRows[P, A, M any](ctx context.Context, app *App[P, A], descriptor golem.ModelDescriptor[M], prepared PreparedRead, indexName, query string, take int) ([]golem.FullTextResult[M], error) {
	planned, err := preparePlan(prepared, app.registry, app.readLimits.plan)
	if err != nil {
		return nil, publicPlanError(prepared, err)
	}
	if err := validateSemanticPlanTake(prepared, planned, "textSearch", take); err != nil {
		return nil, err
	}
	rankingParameters, err := app.fulltext.QueryParameters(semanticModelID(descriptor.Metadata()), indexName, query)
	if err != nil {
		return nil, publicFullTextQueryError(prepared.ModelID(), err)
	}
	candidates, err := renderFullTextCandidates(app, prepared, planned, indexName, rankingParameters)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "textSearch", prepared.ModelID(), golem.FieldID{}, "full-text candidate statement could not be rendered", err)
	}
	ctx, releaseWrites := prepared.executor.lockWrites(ctx, nil)
	defer releaseWrites()
	queryer, err := prepared.executor.queryerFor(app.database)
	if err != nil {
		return nil, err
	}
	ranks, err := app.fulltext.QueryOn(ctx, queryer, semanticModelID(descriptor.Metadata()), indexName, query, candidates.candidates, take)
	if err != nil {
		return nil, publicFullTextQueryError(prepared.ModelID(), err)
	}
	if len(ranks) == 0 {
		return []golem.FullTextResult[M]{}, nil
	}
	base, err := readsql.Render(planned, app.registry, app.provider, app.capabilities)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "textSearch", prepared.ModelID(), golem.FieldID{}, "full-text row statement could not be rendered", err)
	}
	hydrationRanks := make([]semanticruntime.Rank, len(ranks))
	for position, rank := range ranks {
		hydrationRanks[position] = semanticruntime.Rank{Key: rank.Key, Identity: rank.Identity}
	}
	rows, err := fetchSemanticRows(ctx, app, descriptor, prepared, planned, "textSearch", candidates.decoder, candidates.fields, len(base.Args()), hydrationRanks)
	if err != nil {
		return nil, err
	}
	result := make([]golem.FullTextResult[M], 0, len(ranks))
	for _, ranked := range ranks {
		hydrated, ok := rows[ranked.Key]
		if !ok {
			continue
		}
		item, err := golem.RuntimeFullTextResultWithIdentity(hydrated.row, ranked.Score, hydrated.identity, hydrated.fields, ranked.Key)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func publicFullTextQueryError(model golem.ModelID, cause error) error {
	if reason, ok := fulltextruntime.QueryValidationReason(cause); ok {
		return golem.RuntimeReadError(golem.CodeBadUserInput, "textSearch", model, golem.FieldID{}, reason, cause)
	}
	return cause
}

func renderFullTextCandidates[P, A any](app *App[P, A], prepared PreparedRead, planned readplan.Plan, indexName string, enclosingParameters int) (renderedSemanticCandidates, error) {
	fields, ok := app.fulltext.IndexFields(semanticModelIDFromPlan(planned), indexName)
	if !ok {
		return renderedSemanticCandidates{}, fmt.Errorf("P9_FULLTEXT_SCHEMA: requested index is absent")
	}
	conditions := make([]policyir.Condition, 0, len(fields))
	if !prepared.system {
		policy, present := prepared.policies.Policy(planned.ModelID())
		if !present {
			return renderedSemanticCandidates{}, golem.RuntimeReadError(golem.CodeForbidden, operationName(prepared.Operation()), prepared.ModelID(), golem.FieldID{}, "read is not permitted", nil)
		}
		for _, field := range fields {
			policyField, err := semanticPolicyFieldID(field)
			if err != nil {
				return renderedSemanticCandidates{}, err
			}
			condition, err := policyresolve.FieldCondition(policy, policyir.ActionRead, planned.ModelID(), policyField)
			if err != nil {
				return renderedSemanticCandidates{}, golem.RuntimeReadError(golem.CodeForbidden, operationName(prepared.Operation()), prepared.ModelID(), golem.FieldID(policyField), "full-text index field is not readable", err)
			}
			conditions = append(conditions, condition)
		}
	}
	statement, err := readsql.RenderSemanticCandidates(planned, app.registry, app.provider, app.capabilities, enclosingParameters, conditions...)
	if err != nil {
		return renderedSemanticCandidates{}, err
	}
	decoder, err := readdecode.NewFields(planned.ModelID(), app.registry, app.provider, statement.Fields())
	if err != nil {
		return renderedSemanticCandidates{}, err
	}
	columns := make([]string, len(statement.Columns()))
	for position, column := range statement.Columns() {
		columns[position] = string(column)
	}
	value := semanticruntime.Candidates{SQL: statement.SQL(), Args: statement.Args(), Columns: columns, Model: planned.ModelID(), MaxStatementParameters: planned.Limits().MaxStatementParameters, MaxStatementBytes: planned.Limits().MaxStatementBytes, MaxStatementAliases: planned.Limits().MaxStatementAliases, NewScan: func() semanticruntime.IdentityScan { return decoder.NewScan() }}
	return renderedSemanticCandidates{candidates: value, decoder: decoder, fields: statement.Fields()}, nil
}
