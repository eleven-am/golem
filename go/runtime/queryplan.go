package runtime

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
	analytics "github.com/eleven-am/golem/go/internal/analytics"
	"github.com/eleven-am/golem/go/internal/observeexec"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/queryplanbuild"
	"github.com/eleven-am/golem/go/internal/queryplancapture"
	"github.com/eleven-am/golem/go/internal/queryplanreport"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
	"github.com/eleven-am/golem/go/internal/scoped"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/queryplan"
)

// CallerExplainFindMany prepares the exact authorized Caller read and asks the
// provider to plan it without executing the data statement or decoding rows.
func CallerExplainFindMany[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) (queryplan.Report, error) {
	return explainCaller(ctx, caller, descriptor.Metadata().ModelID(), func(explainContext context.Context) (queryplanreport.Report, error) {
		prepared, err := prepareCallerFindMany(explainContext, caller, descriptor, options)
		if err != nil {
			return queryplanreport.Report{}, err
		}
		return explainReadStatement(explainContext, caller.app, prepared)
	})
}

func CallerExplainFindFirst[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) (queryplan.Report, error) {
	return explainCaller(ctx, caller, descriptor.Metadata().ModelID(), func(explainContext context.Context) (queryplanreport.Report, error) {
		prepared, err := prepareCallerFindFirst(explainContext, caller, descriptor, options)
		if err != nil {
			return queryplanreport.Report{}, err
		}
		return explainReadStatement(explainContext, caller.app, prepared)
	})
}

func CallerExplainFindUnique[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], selector golem.UniqueSelectorValue[M], options ...golem.ReadOption[M]) (queryplan.Report, error) {
	return explainCaller(ctx, caller, descriptor.Metadata().ModelID(), func(explainContext context.Context) (queryplanreport.Report, error) {
		prepared, err := prepareCallerFindUnique(explainContext, caller, descriptor, selector, options)
		if err != nil {
			return queryplanreport.Report{}, err
		}
		return explainReadStatement(explainContext, caller.app, prepared)
	})
}

func CallerExplainCount[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], options ...golem.ReadOption[M]) (queryplan.Report, error) {
	return explainCaller(ctx, caller, descriptor.Metadata().ModelID(), func(explainContext context.Context) (queryplanreport.Report, error) {
		prepared, err := prepareCallerCount(caller, descriptor, options)
		if err != nil {
			return queryplanreport.Report{}, err
		}
		return explainReadStatement(explainContext, caller.app, prepared)
	})
}

func CallerExplainAggregate[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], request golem.AggregateRequest[M]) (queryplan.Report, error) {
	return explainCaller(ctx, caller, descriptor.Metadata().ModelID(), func(explainContext context.Context) (queryplanreport.Report, error) {
		frozen, err := golem.RuntimeFreezeAggregateRequest(request)
		if err != nil {
			return queryplanreport.Report{}, analyticsError("aggregate", descriptor, err)
		}
		prepared, err := prepareAnalyticsStatement(caller.app, caller.policies, false, descriptor.Metadata().ModelID(), frozen, true)
		if err != nil {
			return queryplanreport.Report{}, err
		}
		return explainAnalyticsStatement(explainContext, caller.app, prepared)
	})
}

func CallerExplainGroupBy[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], request golem.GroupRequest[M]) (queryplan.Report, error) {
	return explainCaller(ctx, caller, descriptor.Metadata().ModelID(), func(explainContext context.Context) (queryplanreport.Report, error) {
		frozen, err := golem.RuntimeFreezeGroupRequest(request)
		if err != nil {
			return queryplanreport.Report{}, analyticsError("groupBy", descriptor, err)
		}
		prepared, err := prepareAnalyticsStatement(caller.app, caller.policies, false, descriptor.Metadata().ModelID(), frozen, true)
		if err != nil {
			return queryplanreport.Report{}, err
		}
		return explainAnalyticsStatement(explainContext, caller.app, prepared)
	})
}

func CallerExplainRelationGroupBy[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], request golem.RelationGroupRequest[M]) (queryplan.Report, error) {
	return explainCaller(ctx, caller, descriptor.Metadata().ModelID(), func(explainContext context.Context) (queryplanreport.Report, error) {
		frozen, err := golem.RuntimeFreezeRelationGroupRequest(request)
		if err != nil {
			return queryplanreport.Report{}, analyticsError("relationGroupBy", descriptor, err)
		}
		prepared, err := prepareAnalyticsStatement(caller.app, caller.policies, false, descriptor.Metadata().ModelID(), frozen, true)
		if err != nil {
			return queryplanreport.Report{}, err
		}
		return explainAnalyticsStatement(explainContext, caller.app, prepared)
	})
}

func CallerExplainScoped[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], request golem.ScopedQuery[M]) (queryplan.Report, error) {
	return explainCaller(ctx, caller, descriptor.Metadata().ModelID(), func(explainContext context.Context) (queryplanreport.Report, error) {
		frozen, err := golem.RuntimeFreezeScopedQuery(request)
		if err != nil {
			return queryplanreport.Report{}, scopedError(descriptor.Metadata().ModelID(), err)
		}
		prepared, err := prepareScopedStatement(caller.app, caller.policies, false, descriptor.Metadata().ModelID(), frozen)
		if err != nil {
			return queryplanreport.Report{}, err
		}
		return explainScopedStatement(explainContext, caller.app, prepared)
	})
}

func explainCaller[P, A any](ctx context.Context, caller *Caller[P, A], model golem.ModelID, run func(context.Context) (queryplanreport.Report, error)) (result queryplan.Report, resultErr error) {
	if caller == nil || caller.app == nil || caller.policies == nil || caller.executor == nil || caller.execution == 0 || ctx == nil || run == nil {
		return queryplan.Report{}, golem.RuntimeReadError(golem.CodeUnauthenticated, "explain", model, golem.FieldID{}, "caller execution is unavailable", nil)
	}
	if _, err := caller.executor.queryerFor(caller.app.database); err != nil {
		return queryplan.Report{}, queryplanreport.NewError(queryplanreport.CodeInvalid)
	}
	explainContext, span, deferred := beginDeferredExecutionObservation(ctx, caller.app, caller.executor, model, observe.KindQueryPlan, observe.OperationQueryPlanExplain)
	var internal queryplanreport.Report
	defer func() {
		if span != nil && resultErr == nil {
			span.SetAggregateCount(queryPlanNodeCount(internal))
		}
		finishQueryPlanObservation(span, resultErr)
		if deferred != nil {
			deferred.Flush()
		}
	}()
	internal, resultErr = run(explainContext)
	if resultErr != nil {
		return queryplan.Report{}, resultErr
	}
	return queryplan.Report(internal), nil
}

func explainReadStatement[P, A any](ctx context.Context, app *App[P, A], prepared preparedReadStatement) (queryplanreport.Report, error) {
	aliases, err := readQueryPlanAliases(prepared.statement.PlanMap())
	if err != nil {
		return queryplanreport.Report{}, err
	}
	providerPlan, err := captureQueryPlan(ctx, app, prepared.statement.SQL(), prepared.statement.Args(), aliases)
	if err != nil {
		return queryplanreport.Report{}, err
	}
	return queryplanbuild.BuildRead(queryplanbuild.ReadInput{Provider: app.provider, Plan: prepared.plan, ProviderPlan: providerPlan, Registry: app.registry, Capabilities: app.capabilities})
}

func explainAnalyticsStatement[P, A any](ctx context.Context, app *App[P, A], prepared preparedAnalyticsStatement) (queryplanreport.Report, error) {
	aliases, err := analyticsQueryPlanAliases(prepared.statement.PlanMap())
	if err != nil {
		return queryplanreport.Report{}, err
	}
	providerPlan, err := captureQueryPlan(ctx, app, prepared.statement.SQL(), prepared.statement.Args(), aliases)
	if err != nil {
		return queryplanreport.Report{}, err
	}
	return queryplanbuild.BuildAnalytics(queryplanbuild.AnalyticsInput{Provider: app.provider, Plan: prepared.plan, ProviderPlan: providerPlan})
}

func explainScopedStatement[P, A any](ctx context.Context, app *App[P, A], prepared preparedScopedStatement) (queryplanreport.Report, error) {
	aliases, err := scopedQueryPlanAliases(prepared.statement.PlanMap())
	if err != nil {
		return queryplanreport.Report{}, err
	}
	providerPlan, err := captureQueryPlan(ctx, app, prepared.statement.SQL(), prepared.statement.Args(), aliases)
	if err != nil {
		return queryplanreport.Report{}, err
	}
	return queryplanbuild.BuildScoped(queryplanbuild.ScopedInput{Provider: app.provider, Plan: prepared.plan, ProviderPlan: providerPlan})
}

func captureQueryPlan[P, A any](ctx context.Context, app *App[P, A], statement string, arguments []any, aliases queryplancapture.AliasMap) (queryplancapture.Plan, error) {
	connection, err := app.database.Connx(ctx)
	if err != nil {
		return queryplancapture.Plan{}, queryplanreport.NewError(queryplanreport.CodeUnavailable)
	}
	observeexec.RecordStatement(ctx)
	var captured queryplancapture.Plan
	switch app.provider {
	case policyir.ProviderSQLite:
		captured, err = sqliteprovider.CaptureQueryPlan(ctx, connection, statement, arguments, app.registry, aliases)
	case policyir.ProviderPostgreSQL:
		captured, err = postgresprovider.CaptureQueryPlan(ctx, connection, statement, arguments, app.registry, aliases)
	default:
		err = queryplancapture.Refuse(queryplancapture.ErrorInvalid)
	}
	closeErr := connection.Close()
	if err != nil {
		return queryplancapture.Plan{}, publicCaptureError(err)
	}
	if closeErr != nil {
		return queryplancapture.Plan{}, queryplanreport.NewError(queryplanreport.CodeUnavailable)
	}
	return captured, nil
}

func publicCaptureError(err error) error {
	code, ok := queryplancapture.CodeOf(err)
	if !ok {
		return queryplanreport.NewError(queryplanreport.CodeInvalid)
	}
	switch code {
	case queryplancapture.ErrorUnavailable:
		return queryplanreport.NewError(queryplanreport.CodeUnavailable)
	case queryplancapture.ErrorTooComplex:
		return queryplanreport.NewError(queryplanreport.CodeTooComplex)
	default:
		return queryplanreport.NewError(queryplanreport.CodeInvalid)
	}
}

func readQueryPlanAliases(plan readsql.PlanMap) (queryplancapture.AliasMap, error) {
	facts := plan.AliasFacts()
	result := make([]queryplancapture.AliasFact, 0, len(facts))
	for _, source := range facts {
		fact := source
		role := queryplancapture.AliasPhysicalAccess
		if fact.Role() == readsql.PlanAliasCorrelatedRelation {
			role = queryplancapture.AliasCorrelatedRelation
		} else if fact.Role() != readsql.PlanAliasPhysicalAccess {
			return queryplancapture.AliasMap{}, queryplanreport.NewError(queryplanreport.CodeInvalid)
		}
		relation, _ := fact.RelationID()
		converted, err := queryplancapture.NewAliasFact(fact.Matches, golem.ModelID(fact.ModelID()), golem.RelationID(relation), golemFieldIDs(fact.FieldIDs()), role)
		if err != nil {
			return queryplancapture.AliasMap{}, queryplanreport.NewError(queryplanreport.CodeInvalid)
		}
		result = append(result, converted)
	}
	return queryplancapture.NewAliasMap(result...), nil
}

func analyticsQueryPlanAliases(plan analytics.AnalyticsPlanMap) (queryplancapture.AliasMap, error) {
	facts := plan.AliasFacts()
	result := make([]queryplancapture.AliasFact, 0, len(facts))
	for _, source := range facts {
		fact := source
		var role queryplancapture.AliasRole
		switch fact.Role() {
		case analytics.AnalyticsPlanAliasPhysicalAccess:
			role = queryplancapture.AliasPhysicalAccess
		case analytics.AnalyticsPlanAliasCorrelatedRelation:
			role = queryplancapture.AliasCorrelatedRelation
		case analytics.AnalyticsPlanAliasAggregate:
			role = queryplancapture.AliasAggregate
		case analytics.AnalyticsPlanAliasMaterialize:
			role = queryplancapture.AliasMaterialize
		case analytics.AnalyticsPlanAliasStructural:
			role = queryplancapture.AliasStructural
		default:
			return queryplancapture.AliasMap{}, queryplanreport.NewError(queryplanreport.CodeInvalid)
		}
		relation, _ := fact.RelationID()
		converted, err := queryplancapture.NewAliasFact(fact.Matches, golem.ModelID(fact.ModelID()), golem.RelationID(relation), golemFieldIDs(fact.FieldIDs()), role)
		if err != nil {
			return queryplancapture.AliasMap{}, queryplanreport.NewError(queryplanreport.CodeInvalid)
		}
		result = append(result, converted)
	}
	return queryplancapture.NewAliasMap(result...), nil
}

func scopedQueryPlanAliases(plan scoped.ScopedPlanMap) (queryplancapture.AliasMap, error) {
	facts := plan.AliasFacts()
	result := make([]queryplancapture.AliasFact, 0, len(facts))
	for _, source := range facts {
		fact := source
		role := queryplancapture.AliasPhysicalAccess
		if fact.Role() == scoped.ScopedPlanAliasCorrelatedRelation {
			role = queryplancapture.AliasCorrelatedRelation
		} else if fact.Role() != scoped.ScopedPlanAliasPhysicalAccess {
			return queryplancapture.AliasMap{}, queryplanreport.NewError(queryplanreport.CodeInvalid)
		}
		relation, _ := fact.RelationID()
		converted, err := queryplancapture.NewAliasFact(fact.Matches, golem.ModelID(fact.ModelID()), golem.RelationID(relation), golemFieldIDs(fact.FieldIDs()), role)
		if err != nil {
			return queryplancapture.AliasMap{}, queryplanreport.NewError(queryplanreport.CodeInvalid)
		}
		result = append(result, converted)
	}
	return queryplancapture.NewAliasMap(result...), nil
}

func golemFieldIDs(values []policyir.FieldID) []golem.FieldID {
	result := make([]golem.FieldID, len(values))
	for index, value := range values {
		result[index] = golem.FieldID(value)
	}
	return result
}

func queryPlanNodeCount(report queryplanreport.Report) int64 {
	var count int64
	stack := make([]queryplanreport.Node, 0)
	for _, statement := range report.Statements() {
		stack = append(stack, statement.Root())
	}
	for len(stack) != 0 {
		last := len(stack) - 1
		node := stack[last]
		stack = stack[:last]
		count++
		stack = append(stack, node.Children()...)
	}
	return count
}

func finishQueryPlanObservation(span *observeexec.Span, err error) {
	outcome, reason := observationResult(err)
	if code, ok := queryplanreport.CodeOf(err); ok {
		outcome = observe.OutcomeRefused
		switch code {
		case queryplanreport.CodeTooComplex:
			reason = observe.ReasonLimit
		case queryplanreport.CodeUnavailable:
			reason = observe.ReasonProvider
		default:
			reason = observe.ReasonInvalidInput
		}
	}
	observeexec.Finish(span, outcome, reason)
}
