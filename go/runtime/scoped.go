package runtime

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	scoped "github.com/eleven-am/golem/go/internal/scoped"
	"github.com/eleven-am/golem/go/observe"
)

func CallerScoped[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], query golem.ScopedQuery[M]) ([]golem.ScopedRow, error) {
	frozen, err := golem.RuntimeFreezeScopedQuery(query)
	if err != nil {
		failure := scopedError(descriptor.Metadata().ModelID(), err)
		if caller != nil && caller.app != nil {
			reportScoped(ctx, caller.app, golem.FrozenScopedQuery{}, caller.auditID, caller.execution, false, "", time.Now(), 0, golem.ScopedOutcomeRefused)
			observeScopedInputRefusal(ctx, caller.app, caller.executor, descriptor.Metadata().ModelID(), failure)
		}
		return nil, failure
	}
	if caller == nil || caller.app == nil || caller.policies == nil || caller.executor == nil {
		return nil, scopedError(descriptor.Metadata().ModelID(), fmt.Errorf("caller execution unavailable"))
	}
	return executeScoped(ctx, caller.app, caller.executor, caller.policies, false, caller.auditID, caller.execution, descriptor.Metadata().ModelID(), frozen)
}

func SystemScoped[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], query golem.ScopedQuery[M]) ([]golem.ScopedRow, error) {
	frozen, err := golem.RuntimeFreezeScopedQuery(query)
	if system.app == nil {
		return nil, scopedError(descriptor.Metadata().ModelID(), fmt.Errorf("system execution unavailable"))
	}
	execution := system.app.nextExecution.Add(1)
	if err != nil {
		failure := scopedError(descriptor.Metadata().ModelID(), err)
		reportScoped(ctx, system.app, golem.FrozenScopedQuery{}, "", execution, true, "", time.Now(), 0, golem.ScopedOutcomeRefused)
		observeScopedInputRefusal(ctx, system.app, system.executor, descriptor.Metadata().ModelID(), failure)
		return nil, failure
	}
	return executeScoped(ctx, system.app, system.executor, nil, true, "", execution, descriptor.Metadata().ModelID(), frozen)
}

func CallerTxScoped[P, A, M any](ctx context.Context, tx *CallerTx[P, A], descriptor golem.ModelDescriptor[M], query golem.ScopedQuery[M]) ([]golem.ScopedRow, error) {
	if tx == nil || tx.caller == nil {
		return nil, scopedError(descriptor.Metadata().ModelID(), fmt.Errorf("caller transaction unavailable"))
	}
	return CallerScoped(ctx, tx.caller, descriptor, query)
}
func SystemTxScoped[P, A, M any](ctx context.Context, tx *SystemTx[P, A], descriptor golem.ModelDescriptor[M], query golem.ScopedQuery[M]) ([]golem.ScopedRow, error) {
	if tx == nil || tx.system.app == nil {
		return nil, scopedError(descriptor.Metadata().ModelID(), fmt.Errorf("system transaction unavailable"))
	}
	frozen, err := golem.RuntimeFreezeScopedQuery(query)
	if err != nil {
		failure := scopedError(descriptor.Metadata().ModelID(), err)
		reportScoped(ctx, tx.system.app, golem.FrozenScopedQuery{}, "", tx.execution, true, "", time.Now(), 0, golem.ScopedOutcomeRefused)
		observeScopedInputRefusal(ctx, tx.system.app, tx.system.executor, descriptor.Metadata().ModelID(), failure)
		return nil, failure
	}
	return executeScoped(ctx, tx.system.app, tx.system.executor, nil, true, "", tx.execution, descriptor.Metadata().ModelID(), frozen)
}

func observeScopedInputRefusal[P, A any](ctx context.Context, app *App[P, A], executor *executionBinding, model golem.ModelID, failure error) {
	_, observation := beginExecutionObservation(ctx, app, executor, model, observe.KindScopedRead, observe.OperationScopedRead)
	finishObservation(observation, failure)
}

type preparedScopedStatement struct {
	plan      scoped.Plan
	statement scoped.Statement
}

func prepareScopedStatement[P, A any](app *App[P, A], policies scoped.PolicySet, system bool, descriptor golem.ModelID, request golem.FrozenScopedQuery) (preparedScopedStatement, error) {
	if descriptor != request.RootModelID() {
		return preparedScopedStatement{}, scopedError(descriptor, fmt.Errorf("scoped request does not match generated client"))
	}
	if len(request.Joins()) > app.analyticsLimits.MaxScopedJoins || len(request.Selections()) > app.analyticsLimits.MaxScopedSelections || request.PredicateNodeCount() > app.analyticsLimits.MaxScopedPredicateNodes {
		return preparedScopedStatement{}, scopedError(descriptor, fmt.Errorf("scoped query limit exceeded"))
	}
	if take, present := request.Take(); present && (take > app.analyticsLimits.MaxProgrammaticGroups || take < -app.analyticsLimits.MaxProgrammaticGroups) {
		return preparedScopedStatement{}, scopedError(descriptor, fmt.Errorf("scoped result limit exceeded"))
	}
	var planned scoped.Plan
	var err error
	if system {
		planned, err = scoped.System(request, app.registry, app.providers, app.readLimits.plan)
	} else {
		planned, err = scoped.Caller(request, app.registry, app.providers, policies, app.readLimits.plan)
	}
	if err != nil {
		return preparedScopedStatement{}, scopedError(descriptor, err)
	}
	statement, err := scoped.Render(planned, app.registry, app.provider, app.capabilities, scoped.RenderOptions{
		MaxContributionRows:   app.analyticsLimits.MaxContributionRows,
		MaxIntermediateGroups: app.analyticsLimits.MaxIntermediateGroups,
		MaxResultRows:         app.analyticsLimits.MaxProgrammaticGroups,
	})
	if err != nil {
		return preparedScopedStatement{}, scopedError(descriptor, err)
	}
	return preparedScopedStatement{plan: planned, statement: statement}, nil
}

func executeScoped[P, A any](ctx context.Context, app *App[P, A], executor *executionBinding, policies scoped.PolicySet, system bool, auditID string, execution uint64, descriptor golem.ModelID, request golem.FrozenScopedQuery) (result []golem.ScopedRow, resultErr error) {
	ctx, observation := beginExecutionObservation(ctx, app, executor, descriptor, observe.KindScopedRead, observe.OperationScopedRead)
	defer func() {
		if observation != nil {
			observation.SetAggregateCount(int64(len(result)))
		}
		finishObservation(observation, resultErr)
	}()
	started := time.Now()
	statementSQL := ""
	outcome := golem.ScopedOutcomeFailed
	defer func() {
		reportScoped(ctx, app, request, auditID, execution, system, statementSQL, started, int64(len(result)), outcome)
	}()
	if ctx == nil || descriptor != request.RootModelID() || executor == nil {
		outcome = golem.ScopedOutcomeRefused
		return nil, scopedError(descriptor, fmt.Errorf("scoped request does not match generated client"))
	}
	prepared, err := prepareScopedStatement(app, policies, system, descriptor, request)
	if err != nil {
		outcome = golem.ScopedOutcomeRefused
		return nil, err
	}
	statement := prepared.statement
	statementSQL = statement.SQL()
	queryer, err := executor.queryerFor(app.database)
	if err != nil {
		return nil, scopedError(descriptor, err)
	}
	rows, err := queryer.QueryxContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		if contextErr := ctx.Err(); errors.Is(contextErr, context.Canceled) || errors.Is(contextErr, context.DeadlineExceeded) {
			outcome = golem.ScopedOutcomeCancelled
			// Providers can surface a connection or transaction error after an
			// in-flight cancellation. Preserve the stable public failure while
			// retaining the context cause for errors.Is and audit consumers.
			err = contextErr
		}
		return nil, scopedError(descriptor, err)
	}
	defer rows.Close()
	columns := statement.Columns()
	var contributionRows, intermediateGroups int64
	for rows.Next() {
		raw := make([]any, statement.ScanColumnCount())
		targets := make([]any, len(raw))
		for index := range raw {
			targets[index] = &raw[index]
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, scopedError(descriptor, err)
		}
		if statement.Guarded() {
			contributionRows, err = scopedInt64(raw[len(columns)])
			if err != nil {
				return nil, scopedError(descriptor, fmt.Errorf("scoped contribution guard decode failed: %w", err))
			}
			intermediateGroups, err = scopedInt64(raw[len(columns)+1])
			if err != nil {
				return nil, scopedError(descriptor, fmt.Errorf("scoped intermediate guard decode failed: %w", err))
			}
			if statement.HasGuardRow() {
				marker, markerErr := scopedInt64(raw[len(columns)+2])
				if markerErr != nil || marker != 0 && marker != 1 {
					return nil, scopedError(descriptor, fmt.Errorf("scoped guard marker decode failed: %w", markerErr))
				}
				if marker == 1 {
					continue
				}
			}
		}
		cells := make([]golem.RuntimeScopedCell, len(columns))
		for index, column := range columns {
			if raw[index] == nil {
				cells[index] = golem.RuntimeNullScopedCell(column.Expression)
				continue
			}
			value, decodeErr := decodeScopedValue(raw[index], column.Expression, column.Logical, app.provider)
			if decodeErr != nil {
				return nil, scopedError(descriptor, decodeErr)
			}
			cells[index] = golem.RuntimePresentScopedCell(column.Expression, value)
		}
		result = append(result, golem.RuntimeScopedRow(cells...))
	}
	if err := rows.Err(); err != nil {
		if contextErr := ctx.Err(); errors.Is(contextErr, context.Canceled) || errors.Is(contextErr, context.DeadlineExceeded) {
			outcome = golem.ScopedOutcomeCancelled
			err = contextErr
		}
		return nil, scopedError(descriptor, err)
	}
	if detail := statement.LimitOverflow(contributionRows, intermediateGroups, len(result)); detail != "" {
		result = nil
		outcome = golem.ScopedOutcomeRefused
		return nil, scopedError(descriptor, fmt.Errorf("%s", detail))
	}
	outcome = golem.ScopedOutcomeSucceeded
	return result, nil
}

func reportScoped[P, A any](ctx context.Context, app *App[P, A], request golem.FrozenScopedQuery, auditID string, execution uint64, system bool, sqlText string, started time.Time, rows int64, outcome golem.ScopedOutcome) {
	if app == nil || app.reportScopedQuery == nil {
		return
	}
	publicProvider := golem.SQLite
	if app.provider == policyir.ProviderPostgreSQL {
		publicProvider = golem.PostgreSQL
	}
	record := golem.RuntimeScopedAuditRecord(request, auditID, execution, system, publicProvider, sqlText, time.Since(started), rows, outcome)
	defer func() { _ = recover() }()
	app.reportScopedQuery(ctx, record)
}

func scopedError(model golem.ModelID, cause error) error {
	return golem.RuntimeReadError(golem.CodeBadUserInput, "scoped", model, golem.FieldID{}, "scoped query could not be executed", cause)
}

func decodeScopedValue(raw any, expression golem.FrozenScopedExpression, logical compilerir.LogicalTypeIR, provider policyir.Provider) (any, error) {
	text := func() string {
		switch value := raw.(type) {
		case string:
			return value
		case []byte:
			return string(value)
		default:
			return fmt.Sprint(value)
		}
	}
	if expression.Kind == golem.ScopedExpressionCountAll || expression.Kind == golem.ScopedExpressionCountField {
		return scopedInt64(raw)
	}
	if expression.Kind == golem.ScopedExpressionSum && (logical.Kind == compilerir.TypeInt16 || logical.Kind == compilerir.TypeInt32 || logical.Kind == compilerir.TypeInt64) {
		return golem.ParseExactInteger(text())
	}
	if (expression.Kind == golem.ScopedExpressionSum || expression.Kind == golem.ScopedExpressionAverage) && logical.Kind == compilerir.TypeDecimal {
		return golem.ParseExactDecimal(text())
	}
	if expression.Kind == golem.ScopedExpressionAverage {
		return finiteScopedFloat64(raw)
	}
	switch logical.Kind {
	case compilerir.TypeBool:
		value, err := scopedInt64(raw)
		if err == nil {
			return value != 0, nil
		}
		if boolean, ok := raw.(bool); ok {
			return boolean, nil
		}
		return nil, err
	case compilerir.TypeInt16:
		value, err := scopedInt64(raw)
		if err == nil && (value < math.MinInt16 || value > math.MaxInt16) {
			return nil, fmt.Errorf("P6_SCOPED_OVERFLOW: Int16 result is out of range")
		}
		return int16(value), err
	case compilerir.TypeInt32:
		value, err := scopedInt64(raw)
		if err == nil && (value < math.MinInt32 || value > math.MaxInt32) {
			return nil, fmt.Errorf("P6_SCOPED_OVERFLOW: Int32 result is out of range")
		}
		return int32(value), err
	case compilerir.TypeInt64:
		return scopedInt64(raw)
	case compilerir.TypeFloat32:
		value, err := scopedFloat64(raw)
		converted := float32(value)
		if err == nil && (math.IsNaN(float64(converted)) || math.IsInf(float64(converted), 0)) {
			err = fmt.Errorf("P6_SCOPED_DECODE: non-finite float32")
		}
		return converted, err
	case compilerir.TypeFloat64:
		return finiteScopedFloat64(raw)
	case compilerir.TypeDecimal:
		if provider == policyir.ProviderSQLite {
			coefficient, err := scopedInt64(raw)
			if err != nil {
				return nil, err
			}
			scale := uint16(0)
			if logical.Scale != nil {
				scale = *logical.Scale
			}
			if scale > 18 {
				return nil, fmt.Errorf("P6_SCOPED_DECODE: decimal scale exceeds portable envelope")
			}
			return golem.NewDecimal(coefficient, uint8(scale))
		}
		return golem.ParseDecimal(text())
	case compilerir.TypeDate:
		return golem.ParseDate(text())
	case compilerir.TypeTime:
		return golem.ParseTime(text())
	case compilerir.TypeDateTime:
		switch value := raw.(type) {
		case time.Time:
			return value.UTC(), nil
		default:
			microseconds, err := scopedInt64(raw)
			if err != nil {
				return nil, err
			}
			return time.UnixMicro(microseconds).UTC(), nil
		}
	case compilerir.TypeUUID:
		return golem.ParseUUID(text())
	case compilerir.TypeString, compilerir.TypeEnum:
		return text(), nil
	}
	return raw, nil
}
func scopedInt64(raw any) (int64, error) {
	switch value := raw.(type) {
	case int64:
		return value, nil
	case int:
		return int64(value), nil
	case []byte:
		return strconv.ParseInt(string(value), 10, 64)
	case string:
		return strconv.ParseInt(value, 10, 64)
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < -9223372036854775808 || value >= 9223372036854775808 {
			return 0, fmt.Errorf("P6_SCOPED_DECODE: non-integral integer")
		}
		return int64(value), nil
	default:
		return 0, fmt.Errorf("P6_SCOPED_DECODE: cannot decode %T as integer", raw)
	}
}
func scopedFloat64(raw any) (float64, error) {
	switch value := raw.(type) {
	case float64:
		return value, nil
	case float32:
		return float64(value), nil
	case int64:
		return float64(value), nil
	case []byte:
		return strconv.ParseFloat(string(value), 64)
	case string:
		return strconv.ParseFloat(value, 64)
	default:
		return 0, fmt.Errorf("P6_SCOPED_DECODE: cannot decode %T as float", raw)
	}
}
func finiteScopedFloat64(raw any) (float64, error) {
	value, err := scopedFloat64(raw)
	if err == nil && (math.IsNaN(value) || math.IsInf(value, 0)) {
		err = fmt.Errorf("P6_SCOPED_DECODE: non-finite float64")
	}
	return value, err
}
