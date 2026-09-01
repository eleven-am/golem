package runtime

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyoperator "github.com/eleven-am/golem/go/internal/policy/operator"
	policyresolve "github.com/eleven-am/golem/go/internal/policy/resolve"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	queueprovider "github.com/eleven-am/golem/go/internal/queue/provider"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
	semanticruntime "github.com/eleven-am/golem/go/internal/semantic/runtime"
	"github.com/eleven-am/golem/go/queue"
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
	return rankSemanticRows(ctx, caller.app, descriptor, prepared, "search", indexName, take, 3, func(ctx context.Context, model ir.ModelID, candidates semanticruntime.Candidates) ([]semanticruntime.Rank, error) {
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
	sourceRow, _, sourcePrepared, err := callerFindUniquePreparedExecuted(ctx, caller, descriptor, source)
	if err != nil {
		return nil, err
	}
	sourceKey, err := semanticSourceKey(descriptor, sourceRow.values)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareCallerFindManyRead(ctx, caller, descriptor, options)
	if err != nil {
		return nil, err
	}
	sourceCandidates, err := renderSemanticCandidates(caller.app, sourcePrepared.prepared, sourcePrepared.plan, indexName, 2)
	if err != nil {
		return nil, err
	}
	return rankSemanticRows(ctx, caller.app, descriptor, prepared, "similar", indexName, take, 4, func(ctx context.Context, model ir.ModelID, candidates semanticruntime.Candidates) ([]semanticruntime.Rank, error) {
		return caller.app.semantic.QueryByKey(ctx, model, indexName, sourceKey, sourceCandidates.candidates, candidates, take)
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
	return rankSemanticRows(ctx, system.app, descriptor, prepared, "search", indexName, take, 3, func(ctx context.Context, model ir.ModelID, candidates semanticruntime.Candidates) ([]semanticruntime.Rank, error) {
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
	sourceRow, _, sourcePrepared, err := systemFindUniquePreparedExecuted(ctx, system, descriptor, source)
	if err != nil {
		return nil, err
	}
	sourceKey, err := semanticSourceKey(descriptor, sourceRow.values)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareSystemFindManyRead(system, descriptor, options)
	if err != nil {
		return nil, err
	}
	sourceCandidates, err := renderSemanticCandidates(system.app, sourcePrepared.prepared, sourcePrepared.plan, indexName, 2)
	if err != nil {
		return nil, err
	}
	return rankSemanticRows(ctx, system.app, descriptor, prepared, "similar", indexName, take, 4, func(ctx context.Context, model ir.ModelID, candidates semanticruntime.Candidates) ([]semanticruntime.Rank, error) {
		return system.app.semantic.QueryByKey(ctx, model, indexName, sourceKey, sourceCandidates.candidates, candidates, take)
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
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic query text is empty")
	}
	if _, err := embedding.NewInput("query", query); err != nil {
		return nil, embedding.Failf(embedding.CodeInvalidInput, err, "semantic query text of %d bytes is not a valid embedding input", len(query))
	}
	return semanticCandidateOptions(predicates, take)
}

func semanticCandidateOptions[M any](predicates []golem.Predicate[M], take int) ([]golem.ReadOption[M], error) {
	if take < 1 || take > semanticruntime.MaximumResults {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic result limit is %d, outside 1..%d", take, semanticruntime.MaximumResults)
	}
	if len(predicates) > 1 {
		return nil, embedding.Failf(embedding.CodeInvalidInput, nil, "semantic search accepts at most one predicate, got %d", len(predicates))
	}
	options := make([]golem.ReadOption[M], 0, 2)
	if len(predicates) == 1 {
		options = append(options, golem.Where(predicates[0]))
	}
	options = append(options, golem.Take[M](take))
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

// semanticSourceKey reports the source row's record key from the executor's
// private decoded cells, and refuses with the findUnique refusal when the row
// cannot carry a semantic identity. The key remains internal; the public row
// still excludes hidden, write-only, and masked primary fields.
func semanticSourceKey[M any](descriptor golem.ModelDescriptor[M], values map[policyir.FieldID]readdecode.Cell) (string, error) {
	primary, err := semanticPrimaryIdentity(descriptor)
	if err != nil {
		return "", err
	}
	fields := make([]policyir.FieldID, len(primary))
	for index, field := range primary {
		fields[index] = policyir.FieldID(field)
	}
	key, err := semanticHydrationRecordKey(values, fields)
	if err != nil {
		return "", golem.RuntimeReadError(golem.CodeNotFound, "findUnique", descriptor.Metadata().ModelID(), golem.FieldID{}, "record not found", nil)
	}
	return key, nil
}

func rankSemanticRows[P, A, M any](ctx context.Context, app *App[P, A], descriptor golem.ModelDescriptor[M], prepared PreparedRead, operation, indexName string, take, rankParameters int, rank semanticRanker) ([]golem.SemanticResult[M], error) {
	planned, err := preparePlan(prepared, app.registry, app.readLimits.plan)
	if err != nil {
		return nil, publicPlanError(prepared, err)
	}
	if err := validateSemanticPlanTake(prepared, planned, operation, take); err != nil {
		return nil, err
	}
	candidates, err := renderSemanticCandidates(app, prepared, planned, indexName, rankParameters)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operation, prepared.ModelID(), golem.FieldID{}, "semantic candidate statement could not be rendered", err)
	}
	model := semanticModelID(descriptor.Metadata())
	ranks, err := rank(ctx, model, candidates.candidates)
	if err != nil {
		return nil, err
	}
	if len(ranks) == 0 {
		return []golem.SemanticResult[M]{}, nil
	}
	base, err := readsql.Render(planned, app.registry, app.provider, app.capabilities)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operation, prepared.ModelID(), golem.FieldID{}, "semantic row statement could not be rendered", err)
	}
	rows, err := fetchSemanticRows(ctx, app, descriptor, prepared, planned, operation, candidates.decoder, candidates.fields, len(base.Args()), ranks)
	if err != nil {
		return nil, err
	}
	return assembleSemanticResults(ranks, rows)
}

type renderedSemanticCandidates struct {
	candidates semanticruntime.Candidates
	decoder    readdecode.Decoder
	fields     []policyir.FieldID
}

func renderSemanticCandidates[P, A any](app *App[P, A], prepared PreparedRead, planned readplan.Plan, indexName string, enclosingParameters int) (renderedSemanticCandidates, error) {
	fields, ok := app.semantic.IndexFields(semanticModelIDFromPlan(planned), indexName)
	if !ok {
		return renderedSemanticCandidates{}, embedding.Failf(embedding.CodeInvalidInput, nil, "the requested model has no semantic index with the requested name")
	}
	conditions := make([]policyir.Condition, 0, len(fields))
	if !prepared.system {
		policy, present := prepared.policies.Policy(planned.ModelID())
		if !present {
			return renderedSemanticCandidates{}, golem.RuntimeReadError(golem.CodeForbidden, operationName(prepared.Operation()), prepared.ModelID(), golem.FieldID{}, "read is not permitted", nil)
		}
		for _, field := range fields {
			policyField, fieldErr := semanticPolicyFieldID(field)
			if fieldErr != nil {
				return renderedSemanticCandidates{}, fieldErr
			}
			condition, err := policyresolve.FieldCondition(policy, policyir.ActionRead, planned.ModelID(), policyField)
			if err != nil {
				return renderedSemanticCandidates{}, golem.RuntimeReadError(golem.CodeForbidden, operationName(prepared.Operation()), prepared.ModelID(), golem.FieldID(policyField), "semantic index field is not readable", err)
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
	for index, column := range statement.Columns() {
		columns[index] = string(column)
	}
	value := semanticruntime.Candidates{
		SQL: statement.SQL(), Args: statement.Args(), Columns: columns, Model: planned.ModelID(),
		MaxStatementBytes: planned.Limits().MaxStatementBytes, MaxStatementAliases: planned.Limits().MaxStatementAliases,
		NewScan: func() semanticruntime.IdentityScan { return decoder.NewScan() },
	}
	return renderedSemanticCandidates{candidates: value, decoder: decoder, fields: statement.Fields()}, nil
}

func semanticModelIDFromPlan(planned readplan.Plan) ir.ModelID {
	return semanticIndexModel(golem.ModelID(planned.ModelID()))
}

func validateSemanticPlanTake(prepared PreparedRead, planned readplan.Plan, operation string, requested int) error {
	maximum := 0
	if limit := planned.ResultLimit(); limit > 0 {
		maximum = limit
	}
	if take, present := planned.Take(); present {
		if take < 0 {
			take = -take
		}
		if maximum == 0 || take < maximum {
			maximum = take
		}
	}
	if maximum > 0 && requested > maximum {
		return golem.RuntimeReadError(golem.CodeBadUserInput, operation, prepared.ModelID(), golem.FieldID{}, "semantic result limit exceeds the planned row maximum", nil)
	}
	return nil
}

// assembleSemanticResults returns the rows still readable after ranking in
// distance order. Rows deleted or newly unauthorized before hydration vanish
// from the page rather than failing the request.
type semanticHydratedRow[M any] struct {
	row      golem.Row[M]
	identity golem.FrozenPredicate
	fields   []golem.FieldID
}

func assembleSemanticResults[M any](ranks []semanticruntime.Rank, rows map[string]semanticHydratedRow[M]) ([]golem.SemanticResult[M], error) {
	result := make([]golem.SemanticResult[M], 0, len(ranks))
	for _, ranked := range ranks {
		hydrated, ok := rows[ranked.Key]
		if !ok {
			continue
		}
		item, err := golem.RuntimeSemanticResultWithIdentity(hydrated.row, ranked.Distance, hydrated.identity, hydrated.fields, ranked.Key)
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
func fetchSemanticRows[P, A, M any](ctx context.Context, app *App[P, A], descriptor golem.ModelDescriptor[M], prepared PreparedRead, planned readplan.Plan, operation string, decoder readdecode.Decoder, fields []policyir.FieldID, baseArguments int, ranks []semanticruntime.Rank) (map[string]semanticHydratedRow[M], error) {
	chunk := semanticIdentityChunkSize(planned, len(fields), baseArguments)
	if chunk < 1 {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operation, prepared.ModelID(), golem.FieldID{}, "semantic row statement has no identity capacity", nil)
	}
	var err error
	result := make(map[string]semanticHydratedRow[M], len(ranks))
	publicFields := make([]golem.FieldID, len(fields))
	for index, field := range fields {
		publicFields[index] = golem.FieldID(field)
	}
	for start := 0; start < len(ranks); {
		end := start + chunk
		if end > len(ranks) {
			end = len(ranks)
		}
		var chunkPlan readplan.Plan
		var statement readsql.Statement
		for {
			identities := make([]policyir.Condition, 0, end-start)
			for _, ranked := range ranks[start:end] {
				cells, decodeErr := decoder.Values(ranked.Identity)
				if decodeErr != nil {
					return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operation, prepared.ModelID(), golem.FieldID{}, "ranked identity did not decode", decodeErr)
				}
				condition, conditionErr := semanticIdentityCondition(app, planned.ModelID(), fields, cells)
				if conditionErr != nil {
					return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operation, prepared.ModelID(), golem.FieldID{}, "ranked identity predicate could not be built", conditionErr)
				}
				identities = append(identities, condition)
			}
			selector := identities[0]
			if len(identities) > 1 {
				selector, err = policyir.NewLogical(planned.ModelID(), policyir.LogicalOr, identities)
				if err != nil {
					return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operation, prepared.ModelID(), golem.FieldID{}, "ranked identity predicate could not be merged", err)
				}
			}
			chunkPlan, err = readplan.WithAdditionalWhere(planned, selector)
			if err != nil {
				return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operation, prepared.ModelID(), golem.FieldID{}, "ranked identity predicate could not be authorized", err)
			}
			statement, err = readsql.Render(chunkPlan, app.registry, app.provider, app.capabilities)
			if err == nil {
				break
			}
			reduced, retry := reduceSemanticHydrationChunk(start, end, err)
			if !retry {
				return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operation, prepared.ModelID(), golem.FieldID{}, "semantic row statement could not be rendered", err)
			}
			end = reduced
		}
		executed, err := executeRenderedPlan(ctx, app, prepared.executor, prepared.Operation(), chunkPlan, statement)
		if err != nil {
			return nil, err
		}
		for _, item := range executed {
			row, rowErr := golem.RuntimeTypedReadRow(descriptor, item.row)
			if rowErr != nil {
				return nil, rowErr
			}
			key, keyErr := semanticHydrationRecordKey(item.values, fields)
			if keyErr != nil {
				continue
			}
			identityCells := make([]readdecode.Cell, len(fields))
			for index, field := range fields {
				identityCells[index] = item.values[field]
			}
			condition, conditionErr := semanticIdentityCondition(app, planned.ModelID(), fields, identityCells)
			if conditionErr != nil {
				return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operation, prepared.ModelID(), golem.FieldID{}, "semantic hydration identity predicate could not be rebuilt", conditionErr)
			}
			identity, freezeErr := policybind.FreezeCondition(golem.ModelID(planned.ModelID()), condition, policybind.RegistryEnumLabels(app.registry))
			if freezeErr != nil {
				return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operation, prepared.ModelID(), golem.FieldID{}, "semantic hydration identity predicate could not be frozen", freezeErr)
			}
			result[key] = semanticHydratedRow[M]{row: row, identity: identity, fields: append([]golem.FieldID(nil), publicFields...)}
		}
		if size := end - start; size < chunk {
			chunk = size
		}
		start = end
	}
	return result, nil
}

func semanticHydrationRecordKey(values map[policyir.FieldID]readdecode.Cell, fields []policyir.FieldID) (string, error) {
	identity := make([]any, len(fields))
	for index, field := range fields {
		cell, ok := values[field]
		if !ok {
			return "", fmt.Errorf("semantic hydration identity field %x is absent", field)
		}
		value, ok := cell.PolicyValue()
		if !ok {
			return "", fmt.Errorf("semantic hydration identity field %x is unavailable", field)
		}
		identity[index], ok = semanticRecordKeyValue(value)
		if !ok {
			return "", fmt.Errorf("semantic hydration identity field %x has unsupported kind %d", field, value.Kind())
		}
	}
	return semantickey.Encode(identity)
}

func reduceSemanticHydrationChunk(start, end int, err error) (int, bool) {
	if end-start <= 1 || !readsql.StatementCapacityExceeded(err) {
		return end, false
	}
	return start + (end-start)/2, true
}

// semanticIdentityChunkSize keeps one ranked page inside both the plan's own
// row ceiling and the provider-neutral statement parameter ceiling.
func semanticIdentityChunkSize(planned readplan.Plan, width, baseArguments int) int {
	chunk := semanticIdentityChunk
	if width > 0 {
		remaining := planned.Limits().MaxStatementParameters - baseArguments
		if remaining < 0 {
			remaining = 0
		}
		if capacity := remaining / width; capacity < chunk {
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
	return semanticIndexModel(metadata.ModelID())
}

func semanticIndexModel(model golem.ModelID) ir.ModelID {
	return ir.ModelID(hex.EncodeToString(model[:]))
}

func semanticPolicyFieldID(field ir.FieldID) (policyir.FieldID, error) {
	decoded, err := hex.DecodeString(string(field))
	if err != nil || len(decoded) != len(policyir.FieldID{}) {
		return policyir.FieldID{}, fmt.Errorf("P9_SEMANTIC_SCHEMA: semantic index field has a non-canonical identifier")
	}
	var result policyir.FieldID
	copy(result[:], decoded)
	return result, nil
}

const (
	semanticDrainJobType     = semanticruntime.DrainJobType
	semanticReconcileJobType = semanticruntime.ReconcileJobType
)

type semanticJob struct {
	Model string `json:"model"`
	Index string `json:"index"`
}

func semanticJobKey(prefix string, job semanticJob) string {
	return prefix + ":" + job.Model + ":" + job.Index
}

func semanticJobExclusiveKey(job semanticJob) string {
	return "semantic.index:" + job.Model + ":" + job.Index
}

// registerSemanticJobs publishes Golem's own job types into the application's
// registry. Keeping the index current is Golem's obligation, not something an
// application opts into, so the application never writes these handlers and
// never names them.
func (app *App[P, A]) registerSemanticJobs(registry *queue.Registry) error {
	drain, err := queue.Register(registry, queue.Definition[semanticJob]{
		Type:        semanticDrainJobType,
		Handle:      app.runSemanticDrain,
		ExclusiveBy: semanticJobExclusiveKey,
	})
	if err != nil {
		return err
	}
	reconcile, err := queue.Register(registry, queue.Definition[semanticJob]{
		Type:        semanticReconcileJobType,
		Handle:      app.runSemanticReconcile,
		ExclusiveBy: semanticJobExclusiveKey,
	})
	if err != nil {
		return err
	}
	app.semanticDrain, app.semanticReconcile = drain, reconcile
	return nil
}

func (app *App[P, A]) runSemanticDrain(ctx context.Context, job queue.Job[semanticJob]) error {
	if err := app.semanticJobTarget(job.Payload); err != nil {
		return err
	}
	pending, err := app.semantic.Drain(ctx, ir.ModelID(job.Payload.Model), job.Payload.Index)
	if err != nil {
		return err
	}
	if !pending {
		return nil
	}
	// A mark committed while this job held its lease produced an enqueue the
	// queue coalesced into this very job, so it carries no wakeup of its own.
	// Chain a successor under a key this job cannot swallow.
	chained := semanticJobKey(semanticDrainJobType, job.Payload) + ":" + string(job.ID)
	_, err = app.enqueueSemanticJob(ctx, nil, app.semanticDrain, job.Payload, chained)
	return err
}

func (app *App[P, A]) runSemanticReconcile(ctx context.Context, job queue.Job[semanticJob]) error {
	if err := app.semanticJobTarget(job.Payload); err != nil {
		return err
	}
	interval := app.semanticReconcileInterval
	if interval != 0 {
		chained := semanticJobKey(semanticReconcileJobType, job.Payload) + ":" + string(job.ID)
		if _, err := app.enqueueSemanticJobWith(ctx, nil, app.semanticReconcile, job.Payload, chained, queue.After(interval)); err != nil {
			return err
		}
	}
	return app.semantic.Refresh(ctx, ir.ModelID(job.Payload.Model), job.Payload.Index)
}

// semanticJobTarget refuses a job left behind by a schema that no longer
// declares its index. Retrying cannot make the index reappear, so the attempt
// budget is not spent on it.
func (app *App[P, A]) semanticJobTarget(payload semanticJob) error {
	for _, reference := range app.semantic.IndexRefs() {
		if string(reference.Model) == payload.Model && reference.Name == payload.Index {
			return nil
		}
	}
	return queue.Terminal(queue.Fail(queue.CodePayloadInvalid, "semantic index is absent"))
}

// startSemanticJobs refuses an application that declares a semantic index
// without a durable queue, then hands every index one reconcile. Golem can see
// that no queue is configured; it cannot see whether a worker will ever run,
// so this refusal covers the configuration only.
func (app *App[P, A]) startSemanticJobs(ctx context.Context) error {
	references := app.semantic.IndexRefs()
	if len(references) == 0 {
		return nil
	}
	if app.queueStore == nil {
		return fmt.Errorf("P9_SEMANTIC_CONFIG: a semantic index requires the durable job queue; set Config.Queue")
	}
	if app.semanticReconcileInterval == 0 {
		return nil
	}
	for _, reference := range references {
		payload := semanticJob{Model: string(reference.Model), Index: reference.Name}
		if _, err := app.enqueueSemanticJob(ctx, nil, app.semanticReconcile, payload, semanticJobKey(semanticReconcileJobType, payload)); err != nil {
			return err
		}
	}
	return nil
}

// enqueueSemanticDrains records one deduped drain per semantic index declared
// on the mutated model. Passing a transaction executor is what makes the job
// row commit and roll back with the write that marked the records.
func (app *App[P, A]) enqueueSemanticDrains(ctx context.Context, executor queueprovider.Executor, model golem.ModelID) error {
	if app == nil || app.semantic == nil {
		return nil
	}
	identity := semanticIndexModel(model)
	for _, reference := range app.semantic.IndexRefs() {
		if reference.Model != identity {
			continue
		}
		payload := semanticJob{Model: string(reference.Model), Index: reference.Name}
		base := semanticJobKey(semanticDrainJobType, payload)
		stored, err := app.enqueueSemanticJob(ctx, executor, app.semanticDrain, payload, base)
		if err != nil {
			return err
		}
		if !stored.Inserted && stored.State == queueprovider.StateLeased {
			if _, err := app.enqueueSemanticJob(ctx, executor, app.semanticDrain, payload, base+":"+stored.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (app *App[P, A]) semanticIndexed(model golem.ModelID) bool {
	if app == nil || app.semantic == nil {
		return false
	}
	identity := semanticIndexModel(model)
	for _, reference := range app.semantic.IndexRefs() {
		if reference.Model == identity {
			return true
		}
	}
	return false
}

func (app *App[P, A]) enqueueSemanticJob(ctx context.Context, executor queueprovider.Executor, jobType queue.Type[semanticJob], payload semanticJob, dedupe string) (queueprovider.EnqueueResult, error) {
	return app.enqueueSemanticJobWith(ctx, executor, jobType, payload, dedupe)
}

func (app *App[P, A]) enqueueSemanticJobWith(ctx context.Context, executor queueprovider.Executor, jobType queue.Type[semanticJob], payload semanticJob, dedupe string, options ...queue.Option) (queueprovider.EnqueueResult, error) {
	options = append(options, queue.Dedupe(dedupe))
	pending, err := jobType.New(payload, options...)
	if err != nil {
		return queueprovider.EnqueueResult{}, err
	}
	stored, err := app.enqueueOn(ctx, executor, pending)
	if err != nil {
		return queueprovider.EnqueueResult{}, err
	}
	if executor == nil {
		app.queueWorker.Wake()
	}
	return stored, nil
}
