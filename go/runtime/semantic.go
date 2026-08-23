package runtime

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyoperator "github.com/eleven-am/golem/go/internal/policy/operator"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
)

// semanticIdentityChunk bounds how many ranked identities one authorized row
// statement fetches. MaximumResults is larger than the provider-neutral
// statement parameter ceiling, so the ranked page is always read in chunks.
const semanticIdentityChunk = 100

type semanticRanker func(ctx context.Context, model ir.ModelID, candidates semanticruntime.Candidates) ([]semanticruntime.Rank, error)

// CallerSearch executes ordinary caller authorization before distance
// evaluation. The authorized predicate is pushed into the ranking statement, so
// only rows the caller may read can occupy a result slot or affect ordering.
func CallerSearch[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, predicates ...golem.Predicate[M]) ([]golem.SemanticResult[M], error) {
	if caller == nil || caller.app == nil || ctx == nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "search", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	options, err := semanticReadOptions(predicates, query, take)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareCallerFindManyRead(ctx, caller, descriptor, options)
	if err != nil {
		return nil, err
	}
	return rankSemanticRows(ctx, caller.app, descriptor, prepared, indexName, take, func(ctx context.Context, model ir.ModelID, candidates semanticruntime.Candidates) ([]semanticruntime.Rank, error) {
		return caller.app.semantic.Query(ctx, model, indexName, query, candidates, take)
	})
}

// CallerSimilar ranks authorized rows against the stored vector of a source row
// the caller is authorized to read. Resolving the source through an ordinary
// authorized unique read is what keeps the neighbourhood of a hidden row from
// becoming a readable projection of it.
func CallerSimilar[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], indexName string, source golem.UniqueSelectorValue[M], take int, predicates ...golem.Predicate[M]) ([]golem.SemanticResult[M], error) {
	if caller == nil || caller.app == nil || ctx == nil {
		return nil, golem.RuntimeReadError(golem.CodeUnauthenticated, "similar", descriptor.Metadata().ModelID(), golem.FieldID{}, "caller execution is unavailable", nil)
	}
	options, err := semanticCandidateOptions(predicates, take)
	if err != nil {
		return nil, err
	}
	sourceRow, err := CallerFindUnique(ctx, caller, descriptor, source)
	if err != nil {
		return nil, err
	}
	sourceKey, err := semanticSourceKey(descriptor, sourceRow)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareCallerFindManyRead(ctx, caller, descriptor, options)
	if err != nil {
		return nil, err
	}
	return rankSemanticRows(ctx, caller.app, descriptor, prepared, indexName, take, func(ctx context.Context, model ir.ModelID, candidates semanticruntime.Candidates) ([]semanticruntime.Rank, error) {
		return caller.app.semantic.QueryByKey(ctx, model, indexName, sourceKey, candidates, take)
	})
}

func SystemSearch[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], indexName, query string, take int, predicates ...golem.Predicate[M]) ([]golem.SemanticResult[M], error) {
	if system.app == nil || ctx == nil {
		return nil, fmt.Errorf("P9_SEMANTIC_RUNTIME: system execution is unavailable")
	}
	options, err := semanticReadOptions(predicates, query, take)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareSystemFindManyRead(system, descriptor, options)
	if err != nil {
		return nil, err
	}
	return rankSemanticRows(ctx, system.app, descriptor, prepared, indexName, take, func(ctx context.Context, model ir.ModelID, candidates semanticruntime.Candidates) ([]semanticruntime.Rank, error) {
		return system.app.semantic.Query(ctx, model, indexName, query, candidates, take)
	})
}

func SystemSimilar[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], indexName string, source golem.UniqueSelectorValue[M], take int, predicates ...golem.Predicate[M]) ([]golem.SemanticResult[M], error) {
	if system.app == nil || ctx == nil {
		return nil, fmt.Errorf("P9_SEMANTIC_RUNTIME: system execution is unavailable")
	}
	options, err := semanticCandidateOptions(predicates, take)
	if err != nil {
		return nil, err
	}
	sourceRow, err := SystemFindUnique(ctx, system, descriptor, source)
	if err != nil {
		return nil, err
	}
	sourceKey, err := semanticSourceKey(descriptor, sourceRow)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareSystemFindManyRead(system, descriptor, options)
	if err != nil {
		return nil, err
	}
	return rankSemanticRows(ctx, system.app, descriptor, prepared, indexName, take, func(ctx context.Context, model ir.ModelID, candidates semanticruntime.Candidates) ([]semanticruntime.Rank, error) {
		return system.app.semantic.QueryByKey(ctx, model, indexName, sourceKey, candidates, take)
	})
}

func prepareSystemFindManyRead[P, A, M any](system System[P, A], descriptor golem.ModelDescriptor[M], options []golem.ReadOption[M]) (PreparedRead, error) {
	frozen, err := golem.FreezeFindMany(descriptor, options...)
	if err != nil {
		return PreparedRead{}, err
	}
	return system.Prepare(frozen)
}

func semanticReadOptions[M any](predicates []golem.Predicate[M], query string, take int) ([]golem.ReadOption[M], error) {
	if query == "" {
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic query, result limit, or predicate is invalid"))
	}
	if _, err := embedding.NewInput("query", query); err != nil {
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic query is invalid"))
	}
	return semanticCandidateOptions(predicates, take)
}

func semanticCandidateOptions[M any](predicates []golem.Predicate[M], take int) ([]golem.ReadOption[M], error) {
	if take < 1 || take > semanticruntime.MaximumResults || len(predicates) > 1 {
		return nil, embedding.NewError(embedding.CodeInvalidInput, fmt.Errorf("semantic query, result limit, or predicate is invalid"))
	}
	options := make([]golem.ReadOption[M], 0, 1)
	if len(predicates) == 1 {
		options = append(options, golem.Where(predicates[0]))
	}
	return options, nil
}

func semanticPrimaryIdentity[M any](descriptor golem.ModelDescriptor[M]) ([]golem.FieldID, error) {
	for _, identity := range descriptor.Metadata().Identities() {
		if identity.Kind() == golem.PrimaryIdentity {
			return identity.Fields(), nil
		}
	}
	return nil, fmt.Errorf("P9_SEMANTIC_SCHEMA: model has no primary identity")
}

// semanticSourceKey reports the source row's record key, and refuses with the
// findUnique refusal when the row cannot carry a semantic identity. A masked
// primary key must not be a distinguishable third state: search excludes such
// rows from candidacy, but a source that cannot be identified is
// indistinguishable from a source the caller may not read.
func semanticSourceKey[M any](descriptor golem.ModelDescriptor[M], row golem.Row[M]) (string, error) {
	primary, err := semanticPrimaryIdentity(descriptor)
	if err != nil {
		return "", err
	}
	key, err := golem.RuntimeSemanticRecordKey(row, primary)
	if err != nil {
		return "", golem.RuntimeReadError(golem.CodeNotFound, "findUnique", descriptor.Metadata().ModelID(), golem.FieldID{}, "record not found", nil)
	}
	return key, nil
}

func rankSemanticRows[P, A, M any](ctx context.Context, app *App[P, A], descriptor golem.ModelDescriptor[M], prepared PreparedRead, indexName string, take int, rank semanticRanker) ([]golem.SemanticResult[M], error) {
	planned, err := preparePlan(prepared, app.registry, app.readLimits.plan)
	if err != nil {
		return nil, publicPlanError(prepared, err)
	}
	candidates, err := readsql.RenderSemanticCandidates(planned, app.registry, app.provider, app.capabilities)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "search", prepared.ModelID(), golem.FieldID{}, "semantic candidate statement could not be rendered", err)
	}
	decoder, err := readdecode.NewFields(planned.ModelID(), app.registry, app.provider, candidates.Fields())
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "search", prepared.ModelID(), golem.FieldID{}, "semantic identity decoder could not be built", err)
	}
	model := semanticModelID(descriptor.Metadata())
	if err := app.semantic.Refresh(ctx, model, indexName); err != nil {
		return nil, err
	}
	identity := candidates.Columns()
	columns := make([]string, len(identity))
	for index, column := range identity {
		columns[index] = string(column)
	}
	ranks, err := rank(ctx, model, semanticruntime.Candidates{
		SQL: candidates.SQL(), Args: candidates.Args(), Columns: columns,
		NewScan: func() semanticruntime.IdentityScan { return decoder.NewScan() },
	})
	if err != nil {
		return nil, err
	}
	if len(ranks) == 0 {
		return []golem.SemanticResult[M]{}, nil
	}
	rows, err := fetchSemanticRows(ctx, app, descriptor, prepared, planned, decoder, candidates.Fields(), ranks)
	if err != nil {
		return nil, err
	}
	return assembleSemanticResults(ranks, rows)
}

// assembleSemanticResults returns the ranked page in distance order. A ranked
// identity that the authorized row statement did not return is a backend that
// escaped the candidate set, and closes the request rather than the result.
func assembleSemanticResults[M any](ranks []semanticruntime.Rank, rows map[string]golem.Row[M]) ([]golem.SemanticResult[M], error) {
	result := make([]golem.SemanticResult[M], 0, len(ranks))
	for _, ranked := range ranks {
		row, ok := rows[ranked.Key]
		if !ok {
			return nil, fmt.Errorf("P9_SEMANTIC_QUERY: ranked identity escaped authorized candidates")
		}
		item, err := golem.RuntimeSemanticResult(row, ranked.Distance)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

// fetchSemanticRows re-reads every ranked identity through the ordinary
// authorized row statement. Ranking already restricted candidacy to the
// authorized predicate; reading the projected rows through the same plan is
// what keeps masks, relations, and row policy identical to a plain findMany.
func fetchSemanticRows[P, A, M any](ctx context.Context, app *App[P, A], descriptor golem.ModelDescriptor[M], prepared PreparedRead, planned readplan.Plan, decoder readdecode.Decoder, fields []policyir.FieldID, ranks []semanticruntime.Rank) (map[string]golem.Row[M], error) {
	primary, err := semanticPrimaryIdentity(descriptor)
	if err != nil {
		return nil, err
	}
	chunk := semanticIdentityChunkSize(planned, len(fields))
	if chunk < 1 {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "search", prepared.ModelID(), golem.FieldID{}, "semantic row statement has no identity capacity", nil)
	}
	result := make(map[string]golem.Row[M], len(ranks))
	for start := 0; start < len(ranks); start += chunk {
		end := start + chunk
		if end > len(ranks) {
			end = len(ranks)
		}
		identities := make([]policyir.Condition, 0, end-start)
		for _, ranked := range ranks[start:end] {
			cells, decodeErr := decoder.Values(ranked.Identity)
			if decodeErr != nil {
				return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "search", prepared.ModelID(), golem.FieldID{}, "ranked identity did not decode", decodeErr)
			}
			condition, conditionErr := semanticIdentityCondition(app, planned.ModelID(), fields, cells)
			if conditionErr != nil {
				return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "search", prepared.ModelID(), golem.FieldID{}, "ranked identity predicate could not be built", conditionErr)
			}
			identities = append(identities, condition)
		}
		selector := identities[0]
		if len(identities) > 1 {
			selector, err = policyir.NewLogical(planned.ModelID(), policyir.LogicalOr, identities)
			if err != nil {
				return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "search", prepared.ModelID(), golem.FieldID{}, "ranked identity predicate could not be merged", err)
			}
		}
		chunkPlan, err := readplan.WithAdditionalWhere(planned, selector)
		if err != nil {
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "search", prepared.ModelID(), golem.FieldID{}, "ranked identity predicate could not be authorized", err)
		}
		executed, err := executePlan(ctx, app, prepared.executor, prepared.Operation(), chunkPlan)
		if err != nil {
			return nil, err
		}
		for _, item := range executed {
			row, rowErr := golem.RuntimeTypedReadRow(descriptor, item.row)
			if rowErr != nil {
				return nil, rowErr
			}
			key, keyErr := golem.RuntimeSemanticRecordKey(row, primary)
			if keyErr != nil {
				continue
			}
			result[key] = row
		}
	}
	return result, nil
}

// semanticIdentityChunkSize keeps one ranked page inside both the plan's own
// row ceiling and the provider-neutral statement parameter ceiling. Half the
// parameter budget is reserved for the authorized predicate itself.
func semanticIdentityChunkSize(planned readplan.Plan, width int) int {
	chunk := semanticIdentityChunk
	if width > 0 {
		if capacity := planned.Limits().MaxStatementParameters / (2 * width); capacity < chunk {
			chunk = capacity
		}
	}
	if take, present := planned.Take(); present {
		if take < 0 {
			take = -take
		}
		if take < chunk {
			chunk = take
		}
	}
	if limit := planned.ResultLimit(); limit > 0 && limit < chunk {
		chunk = limit
	}
	return chunk
}

func semanticIdentityCondition[P, A any](app *App[P, A], model policyir.ModelID, fields []policyir.FieldID, cells []readdecode.Cell) (policyir.Condition, error) {
	if len(cells) != len(fields) || len(fields) == 0 {
		return policyir.Condition{}, fmt.Errorf("ranked identity width does not match the primary key")
	}
	resolver := policysql.SchemaResolver(app.registry)
	conditions := make([]policyir.Condition, len(fields))
	for index, field := range fields {
		cell := cells[index]
		value, ok := cell.PolicyValue()
		if !ok || cell.IsNull() || cell.FieldID() != field {
			return policyir.Condition{}, fmt.Errorf("ranked identity field %x has no scalar value", field)
		}
		resolved, ok := resolver.Field(app.provider, model, field)
		if !ok {
			return policyir.Condition{}, fmt.Errorf("ranked identity field %x is absent", field)
		}
		operand, err := policyir.OneOperand(value)
		if err != nil {
			return policyir.Condition{}, err
		}
		requirements, err := policyoperator.ValidateShape(policyir.OperatorEqual, policyoperator.Shape{Node: policyir.ConditionScalar, FieldType: resolved.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: resolver.Providers()})
		if err != nil {
			return policyir.Condition{}, err
		}
		conditions[index], err = policyir.NewScalar(model, field, resolved.Type, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
		if err != nil {
			return policyir.Condition{}, err
		}
	}
	if len(conditions) == 1 {
		return conditions[0], nil
	}
	return policyir.NewLogical(model, policyir.LogicalAnd, conditions)
}

func semanticModelID(metadata golem.ModelMetadata) ir.ModelID {
	model := metadata.ModelID()
	return ir.ModelID(hex.EncodeToString(model[:]))
}
