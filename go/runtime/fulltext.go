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
	if caller == nil || caller.app == nil || ctx == nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "textSearch", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	options, err := fullTextReadOptions(predicates, query, take)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "textSearch", descriptor.Metadata().ModelID(), golem.FieldID{}, "full-text query is invalid", err)
	}
	prepared, err := prepareCallerFindManyRead(ctx, caller, descriptor, options)
	if err != nil {
		return nil, err
	}
	return rankFullTextRows(ctx, caller.app, descriptor, prepared, indexName, query, take)
}

// SystemTextSearch executes the same ranked query without caller policy.
func SystemTextSearch[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, predicates ...golem.Predicate[M]) ([]golem.FullTextResult[M], error) {
	if system.app == nil || ctx == nil {
		return nil, fmt.Errorf("P9_FULLTEXT_RUNTIME: system execution is unavailable")
	}
	options, err := fullTextReadOptions(predicates, query, take)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "textSearch", descriptor.Metadata().ModelID(), golem.FieldID{}, "full-text query is invalid", err)
	}
	prepared, err := prepareSystemFindManyRead(system, descriptor, options)
	if err != nil {
		return nil, err
	}
	return rankFullTextRows(ctx, system.app, descriptor, prepared, indexName, query, take)
}

func fullTextReadOptions[M any](predicates []golem.Predicate[M], query string, take int) ([]golem.ReadOption[M], error) {
	if query == "" {
		return nil, fmt.Errorf("P9_FULLTEXT_QUERY: query is empty")
	}
	if take < 1 || take > fulltextruntime.MaximumResults {
		return nil, fmt.Errorf("P9_FULLTEXT_QUERY: result limit %d is outside 1..%d", take, fulltextruntime.MaximumResults)
	}
	if len(predicates) > 1 {
		return nil, fmt.Errorf("P9_FULLTEXT_QUERY: at most one predicate is accepted")
	}
	options := make([]golem.ReadOption[M], 0, 2)
	if len(predicates) == 1 {
		options = append(options, golem.Where(predicates[0]))
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
	candidates, err := renderFullTextCandidates(app, prepared, planned, indexName, 2)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "textSearch", prepared.ModelID(), golem.FieldID{}, "full-text candidate statement could not be rendered", err)
	}
	ranks, err := app.fulltext.Query(ctx, semanticModelID(descriptor.Metadata()), indexName, query, candidates.candidates, take)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "textSearch", prepared.ModelID(), golem.FieldID{}, "full-text query failed", err)
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

func renderFullTextCandidates[P, A any](app *App[P, A], prepared PreparedRead, planned readplan.Plan, indexName string, enclosingParameters int) (renderedSemanticCandidates, error) {
	fields, ok := app.fulltext.IndexFields(semanticModelIDFromPlan(planned), indexName)
	if !ok {
		return renderedSemanticCandidates{}, fmt.Errorf("P9_FULLTEXT_SCHEMA: requested index is absent")
	}
	if app.provider == policyir.ProviderSQLite {
		enclosingParameters += len(fields)
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
