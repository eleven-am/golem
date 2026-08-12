package runtime

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/eleven-am/golem/go/golem"
	analytics "github.com/eleven-am/golem/go/internal/analytics"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/observe"
)

func CallerAggregate[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], request golem.AggregateRequest[M]) (golem.AggregateResult[M], error) {
	frozen, err := golem.RuntimeFreezeAggregateRequest(request)
	if err != nil {
		return golem.AggregateResult[M]{}, analyticsError("aggregate", descriptor, err)
	}
	rows, err := executeAnalytics(ctx, caller.app, caller.executor, caller.policies, false, descriptor.Metadata().ModelID(), frozen)
	if err != nil {
		return golem.AggregateResult[M]{}, err
	}
	if len(rows) != 1 {
		return golem.AggregateResult[M]{}, analyticsError("aggregate", descriptor, fmt.Errorf("aggregate returned %d rows", len(rows)))
	}
	return golem.RuntimeAggregateResult[M](frozen.ModelID(), rows[0]...), nil
}
func SystemAggregate[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], request golem.AggregateRequest[M]) (golem.AggregateResult[M], error) {
	frozen, err := golem.RuntimeFreezeAggregateRequest(request)
	if err != nil {
		return golem.AggregateResult[M]{}, analyticsError("aggregate", descriptor, err)
	}
	rows, err := executeAnalytics(ctx, system.app, system.executor, nil, true, descriptor.Metadata().ModelID(), frozen)
	if err != nil {
		return golem.AggregateResult[M]{}, err
	}
	if len(rows) != 1 {
		return golem.AggregateResult[M]{}, analyticsError("aggregate", descriptor, fmt.Errorf("aggregate returned %d rows", len(rows)))
	}
	return golem.RuntimeAggregateResult[M](frozen.ModelID(), rows[0]...), nil
}
func CallerGroupBy[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], request golem.GroupRequest[M]) ([]golem.GroupRow[M], error) {
	frozen, err := golem.RuntimeFreezeGroupRequest(request)
	if err != nil {
		return nil, analyticsError("groupBy", descriptor, err)
	}
	rows, err := executeAnalytics(ctx, caller.app, caller.executor, caller.policies, false, descriptor.Metadata().ModelID(), frozen)
	if err != nil {
		return nil, err
	}
	result := make([]golem.GroupRow[M], len(rows))
	for i, row := range rows {
		result[i] = golem.RuntimeGroupRow[M](frozen.ModelID(), row...)
	}
	return result, nil
}
func SystemGroupBy[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], request golem.GroupRequest[M]) ([]golem.GroupRow[M], error) {
	frozen, err := golem.RuntimeFreezeGroupRequest(request)
	if err != nil {
		return nil, analyticsError("groupBy", descriptor, err)
	}
	rows, err := executeAnalytics(ctx, system.app, system.executor, nil, true, descriptor.Metadata().ModelID(), frozen)
	if err != nil {
		return nil, err
	}
	result := make([]golem.GroupRow[M], len(rows))
	for i, row := range rows {
		result[i] = golem.RuntimeGroupRow[M](frozen.ModelID(), row...)
	}
	return result, nil
}
func CallerRelationGroupBy[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], request golem.RelationGroupRequest[M]) ([]golem.RelationGroupRow[M], error) {
	frozen, err := golem.RuntimeFreezeRelationGroupRequest(request)
	if err != nil {
		return nil, analyticsError("relationGroupBy", descriptor, err)
	}
	rows, err := executeAnalytics(ctx, caller.app, caller.executor, caller.policies, false, descriptor.Metadata().ModelID(), frozen)
	if err != nil {
		return nil, err
	}
	result := make([]golem.RelationGroupRow[M], len(rows))
	for i, row := range rows {
		result[i] = golem.RuntimeRelationGroupRow[M](frozen.ModelID(), row...)
	}
	return result, nil
}
func SystemRelationGroupBy[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], request golem.RelationGroupRequest[M]) ([]golem.RelationGroupRow[M], error) {
	frozen, err := golem.RuntimeFreezeRelationGroupRequest(request)
	if err != nil {
		return nil, analyticsError("relationGroupBy", descriptor, err)
	}
	rows, err := executeAnalytics(ctx, system.app, system.executor, nil, true, descriptor.Metadata().ModelID(), frozen)
	if err != nil {
		return nil, err
	}
	result := make([]golem.RelationGroupRow[M], len(rows))
	for i, row := range rows {
		result[i] = golem.RuntimeRelationGroupRow[M](frozen.ModelID(), row...)
	}
	return result, nil
}

func CallerTxAggregate[P, A, M any](ctx context.Context, tx *CallerTx[P, A], descriptor golem.ModelDescriptor[M], request golem.AggregateRequest[M]) (golem.AggregateResult[M], error) {
	if tx == nil || tx.caller == nil {
		return golem.AggregateResult[M]{}, fmt.Errorf("P6_ANALYTICS_TRANSACTION: caller transaction unavailable")
	}
	return CallerAggregate(ctx, tx.caller, descriptor, request)
}
func SystemTxAggregate[P, A, M any](ctx context.Context, tx *SystemTx[P, A], descriptor golem.ModelDescriptor[M], request golem.AggregateRequest[M]) (golem.AggregateResult[M], error) {
	if tx == nil || tx.system.app == nil {
		return golem.AggregateResult[M]{}, fmt.Errorf("P6_ANALYTICS_TRANSACTION: system transaction unavailable")
	}
	return SystemAggregate(ctx, tx.system, descriptor, request)
}
func CallerTxGroupBy[P, A, M any](ctx context.Context, tx *CallerTx[P, A], descriptor golem.ModelDescriptor[M], request golem.GroupRequest[M]) ([]golem.GroupRow[M], error) {
	if tx == nil || tx.caller == nil {
		return nil, fmt.Errorf("P6_ANALYTICS_TRANSACTION: caller transaction unavailable")
	}
	return CallerGroupBy(ctx, tx.caller, descriptor, request)
}
func SystemTxGroupBy[P, A, M any](ctx context.Context, tx *SystemTx[P, A], descriptor golem.ModelDescriptor[M], request golem.GroupRequest[M]) ([]golem.GroupRow[M], error) {
	if tx == nil || tx.system.app == nil {
		return nil, fmt.Errorf("P6_ANALYTICS_TRANSACTION: system transaction unavailable")
	}
	return SystemGroupBy(ctx, tx.system, descriptor, request)
}
func CallerTxRelationGroupBy[P, A, M any](ctx context.Context, tx *CallerTx[P, A], descriptor golem.ModelDescriptor[M], request golem.RelationGroupRequest[M]) ([]golem.RelationGroupRow[M], error) {
	if tx == nil || tx.caller == nil {
		return nil, fmt.Errorf("P6_ANALYTICS_TRANSACTION: caller transaction unavailable")
	}
	return CallerRelationGroupBy(ctx, tx.caller, descriptor, request)
}
func SystemTxRelationGroupBy[P, A, M any](ctx context.Context, tx *SystemTx[P, A], descriptor golem.ModelDescriptor[M], request golem.RelationGroupRequest[M]) ([]golem.RelationGroupRow[M], error) {
	if tx == nil || tx.system.app == nil {
		return nil, fmt.Errorf("P6_ANALYTICS_TRANSACTION: system transaction unavailable")
	}
	return SystemRelationGroupBy(ctx, tx.system, descriptor, request)
}

func executeAnalytics[P, A any](ctx context.Context, app *App[P, A], executor *executionBinding, policies analytics.PolicySet, system bool, descriptor golem.ModelID, request golem.FrozenAnalyticsRequest) ([][]golem.RuntimeAnalyticsCell, error) {
	return executeAnalyticsWithMode(ctx, app, executor, policies, system, descriptor, request, true)
}

type preparedAnalyticsStatement struct {
	plan      analytics.Plan
	statement analytics.Statement
}

func prepareAnalyticsStatement[P, A any](app *App[P, A], policies analytics.PolicySet, system bool, descriptor golem.ModelID, request golem.FrozenAnalyticsRequest, enforceProgrammaticGroupLimit bool) (preparedAnalyticsStatement, error) {
	if descriptor != request.ModelID() {
		return preparedAnalyticsStatement{}, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", descriptor, golem.FieldID{}, "request model does not match generated client", nil)
	}
	if len(request.Measures()) > app.analyticsLimits.MaxMeasures || len(request.Dimensions()) > app.analyticsLimits.MaxDimensions {
		return preparedAnalyticsStatement{}, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", descriptor, golem.FieldID{}, "analytics shape limit exceeded", nil)
	}
	if take, present := request.Take(); enforceProgrammaticGroupLimit && present && (take > app.analyticsLimits.MaxProgrammaticGroups || take < -app.analyticsLimits.MaxProgrammaticGroups) {
		return preparedAnalyticsStatement{}, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", descriptor, golem.FieldID{}, "programmatic group limit exceeded", nil)
	}
	var planned analytics.Plan
	var err error
	if system {
		planned, err = analytics.System(request, app.registry, app.providers, app.readLimits.plan)
	} else {
		planned, err = analytics.Caller(request, app.registry, app.providers, policies, app.readLimits.plan)
	}
	if err != nil {
		var fieldFailure *analytics.FieldAuthorizationError
		if errors.As(err, &fieldFailure) {
			return preparedAnalyticsStatement{}, golem.RuntimeReadError(golem.CodeForbidden, "analytics", descriptor, fieldFailure.FieldID(), fmt.Sprintf("analytics field %q is not permitted", fieldFailure.LogicalName()), err)
		}
		return preparedAnalyticsStatement{}, golem.RuntimeReadError(golem.CodeForbidden, "analytics", descriptor, golem.FieldID{}, "analytics request is not permitted", err)
	}
	if depth := len(planned.RelationPath()); depth > app.analyticsLimits.MaxRelationDepth {
		return preparedAnalyticsStatement{}, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", descriptor, golem.FieldID{}, "analytics relation depth limit exceeded", nil)
	}
	maxResultRows := 0
	if enforceProgrammaticGroupLimit {
		maxResultRows = app.analyticsLimits.MaxProgrammaticGroups
	}
	statement, err := analytics.Render(planned, app.registry, app.provider, app.capabilities, analytics.RenderOptions{
		MaxContributionRows:   app.analyticsLimits.MaxContributionRows,
		MaxIntermediateGroups: app.analyticsLimits.MaxIntermediateGroups,
		MaxResultRows:         maxResultRows,
	})
	if err != nil {
		return preparedAnalyticsStatement{}, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", descriptor, golem.FieldID{}, "analytics statement could not be rendered", err)
	}
	return preparedAnalyticsStatement{plan: planned, statement: statement}, nil
}

func executeAnalyticsWithMode[P, A any](ctx context.Context, app *App[P, A], executor *executionBinding, policies analytics.PolicySet, system bool, descriptor golem.ModelID, request golem.FrozenAnalyticsRequest, enforceProgrammaticGroupLimit bool) (result [][]golem.RuntimeAnalyticsCell, resultErr error) {
	if ctx == nil || app == nil || executor == nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", descriptor, golem.FieldID{}, "analytics execution unavailable", nil)
	}
	ctx, observation := beginExecutionObservation(ctx, app, executor, descriptor, observe.KindAnalytics, analyticsObservationOperation(request.Operation()))
	defer func() {
		if observation != nil {
			observation.SetAggregateCount(int64(len(result)))
		}
		finishObservation(observation, resultErr)
	}()
	prepared, err := prepareAnalyticsStatement(app, policies, system, descriptor, request, enforceProgrammaticGroupLimit)
	if err != nil {
		return nil, err
	}
	statement := prepared.statement
	queryer, err := executor.queryerFor(app.database)
	if err != nil {
		return nil, err
	}
	rows, err := queryer.QueryxContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		// Some providers report a connection/transaction error after an
		// in-flight cancellation instead of returning ctx.Err directly. Keep
		// the public classification stable while preserving the context cause
		// for errors.Is and trusted diagnostics.
		if contextErr := ctx.Err(); contextErr != nil {
			err = contextErr
		}
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", descriptor, golem.FieldID{}, "analytics query failed", err)
	}
	defer rows.Close()
	columns := statement.Columns()
	var contributionRows, intermediateGroups int64
	for rows.Next() {
		raw := make([]any, statement.ScanColumnCount())
		dest := make([]any, len(raw))
		for i := range raw {
			dest[i] = &raw[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		guardRow := false
		if statement.Guarded() {
			var guardErr error
			contributionRows, guardErr = toInt64(raw[len(columns)])
			if guardErr != nil {
				return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", descriptor, golem.FieldID{}, "analytics contribution guard decode failed", guardErr)
			}
			intermediateGroups, guardErr = toInt64(raw[len(columns)+1])
			if guardErr != nil {
				return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", descriptor, golem.FieldID{}, "analytics intermediate guard decode failed", guardErr)
			}
			if statement.ScanColumnCount() == len(columns)+3 {
				marker, markerErr := toInt64(raw[len(columns)+2])
				if markerErr != nil || marker != 0 && marker != 1 {
					return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", descriptor, golem.FieldID{}, "analytics guard-row marker decode failed", markerErr)
				}
				guardRow = marker == 1
			}
		}
		if guardRow {
			continue
		}
		cells := make([]golem.RuntimeAnalyticsCell, len(columns))
		for i, column := range columns {
			key := golem.RuntimeAnalyticsTermKey(column.Term)
			if raw[i] == nil {
				cells[i] = golem.RuntimeNullAnalyticsCell(key)
				continue
			}
			field, _ := prepared.plan.Field(column.Term.Field)
			value, decodeErr := decodeAnalyticsValue(raw[i], column.Term, field.LogicalType(), app.provider)
			if decodeErr != nil {
				return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", descriptor, column.Term.Field, "analytics result decode failed", decodeErr)
			}
			cells[i] = golem.RuntimePresentAnalyticsCell(key, value)
		}
		result = append(result, cells)
	}
	if err := rows.Err(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			err = contextErr
		}
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", descriptor, golem.FieldID{}, "analytics query failed", err)
	}
	if detail := statement.LimitOverflow(contributionRows, intermediateGroups, len(result)); detail != "" {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, "analytics", descriptor, golem.FieldID{}, detail, nil)
	}
	return result, nil
}

func decodeAnalyticsValue(raw any, term golem.FrozenAnalyticsTerm, logical compilerir.LogicalTypeIR, provider policyir.Provider) (any, error) {
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
	if term.Operator == golem.AggregateCountAll || term.Operator == golem.AggregateCountField {
		return toInt64(raw)
	}
	if term.Operator == golem.AggregateSum && (logical.Kind == compilerir.TypeInt16 || logical.Kind == compilerir.TypeInt32 || logical.Kind == compilerir.TypeInt64) {
		return golem.ParseExactInteger(text())
	}
	if (term.Operator == golem.AggregateSum || term.Operator == golem.AggregateAverage) && logical.Kind == compilerir.TypeDecimal {
		return golem.ParseExactDecimal(text())
	}
	if term.Operator == golem.AggregateAverage {
		return toFloat64(raw)
	}
	switch logical.Kind {
	case compilerir.TypeBool:
		switch value := raw.(type) {
		case bool:
			return value, nil
		default:
			integer, err := toInt64(value)
			return integer != 0, err
		}
	case compilerir.TypeInt16:
		value, err := toInt64(raw)
		if err == nil && (value < math.MinInt16 || value > math.MaxInt16) {
			return nil, fmt.Errorf("P6_ANALYTICAL_OVERFLOW: Int16 result is out of range")
		}
		return int16(value), err
	case compilerir.TypeInt32:
		value, err := toInt64(raw)
		if err == nil && (value < math.MinInt32 || value > math.MaxInt32) {
			return nil, fmt.Errorf("P6_ANALYTICAL_OVERFLOW: Int32 result is out of range")
		}
		return int32(value), err
	case compilerir.TypeInt64:
		return toInt64(raw)
	case compilerir.TypeFloat32:
		value, err := toFloat64(raw)
		if err == nil && (value > math.MaxFloat32 || value < -math.MaxFloat32) {
			return nil, fmt.Errorf("P6_ANALYTICAL_OVERFLOW: Float32 result is out of range")
		}
		return float32(value), err
	case compilerir.TypeFloat64:
		return toFloat64(raw)
	case compilerir.TypeDecimal:
		if provider == policyir.ProviderSQLite {
			coefficient, err := toInt64(raw)
			if err != nil {
				return nil, err
			}
			scale := uint8(0)
			if logical.Scale != nil {
				scale = uint8(*logical.Scale)
			}
			return golem.NewDecimal(coefficient, scale)
		}
		return golem.ParseDecimal(text())
	case compilerir.TypeDate:
		if value, ok := raw.(time.Time); ok {
			return golem.DateFromTime(value)
		}
		return golem.ParseDate(text())
	case compilerir.TypeTime:
		return golem.ParseTime(text())
	case compilerir.TypeDateTime:
		if provider == policyir.ProviderSQLite {
			micros, err := toInt64(raw)
			if err != nil {
				return nil, err
			}
			seconds := floorAnalyticsDiv(micros, 1_000_000)
			return time.Unix(seconds, (micros-seconds*1_000_000)*1_000).UTC(), nil
		}
		if value, ok := raw.(time.Time); ok {
			if value.Nanosecond()%1_000 != 0 {
				return nil, fmt.Errorf("datetime exceeds microsecond precision")
			}
			return value.UTC(), nil
		}
		value, err := time.Parse(time.RFC3339Nano, text())
		if err != nil {
			return nil, err
		}
		if value.Nanosecond()%1_000 != 0 {
			return nil, fmt.Errorf("datetime exceeds microsecond precision")
		}
		return value.UTC(), nil
	case compilerir.TypeString, compilerir.TypeEnum:
		return text(), nil
	case compilerir.TypeUUID:
		return golem.ParseUUID(text())
	default:
		return raw, nil
	}
}

func floorAnalyticsDiv(value, divisor int64) int64 {
	quotient := value / divisor
	if value < 0 && value%divisor != 0 {
		quotient--
	}
	return quotient
}
func toInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed < -9223372036854775808 || typed >= 9223372036854775808 {
			return 0, fmt.Errorf("non-integral count")
		}
		return int64(typed), nil
	case []byte:
		return strconv.ParseInt(string(typed), 10, 64)
	case string:
		return strconv.ParseInt(typed, 10, 64)
	default:
		return strconv.ParseInt(fmt.Sprint(value), 10, 64)
	}
}
func toFloat64(value any) (float64, error) {
	var result float64
	var err error
	switch typed := value.(type) {
	case float64:
		result = typed
	case float32:
		result = float64(typed)
	case int64:
		result = float64(typed)
	case []byte:
		result, err = strconv.ParseFloat(string(typed), 64)
	case string:
		result, err = strconv.ParseFloat(typed, 64)
	default:
		result, err = strconv.ParseFloat(fmt.Sprint(value), 64)
	}
	if err != nil {
		return 0, err
	}
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return 0, fmt.Errorf("P6_ANALYTICAL_OVERFLOW: non-finite aggregate result")
	}
	return result, nil
}
func analyticsError[M any](operation string, descriptor golem.ModelDescriptor[M], cause error) error {
	return golem.RuntimeReadError(golem.CodeBadUserInput, operation, descriptor.Metadata().ModelID(), golem.FieldID{}, "analytics request is invalid", cause)
}
