package sqlite

import (
	"database/sql/driver"
	"fmt"

	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	sqlite3 "github.com/ncruces/go-sqlite3"
)

const sqliteFunctionFlags = sqlite3.DETERMINISTIC | sqlite3.INNOCUOUS

func init() {
	sqlite3.AutoExtension(registerSQLiteFunctions)
}

func registerSQLiteFunctions(connection *sqlite3.Conn) error {
	if connection == nil {
		return fmt.Errorf("sqlite functions: connection is nil")
	}
	for _, function := range []struct {
		name  string
		arity int
		call  func([]driver.Value) (driver.Value, error)
	}{
		{policyASCIIFoldFunction, 1, sqliteASCIIFold},
		{policyListFunction, 4, sqlitePolicyList},
		{policyJSONFunction, 6, sqlitePolicyJSON},
		{AnalyticsNumericCompareFunction, 2, analyticsNumericCompare},
	} {
		fn := function
		if err := connection.CreateFunction(fn.name, fn.arity, sqliteFunctionFlags, func(context sqlite3.Context, arguments ...sqlite3.Value) {
			value, err := fn.call(sqliteDriverValues(arguments))
			if err != nil {
				context.ResultError(err)
				return
			}
			setSQLiteResult(context, value)
		}); err != nil {
			return fmt.Errorf("sqlite register function %s: %w", fn.name, err)
		}
	}
	if err := connection.CreateWindowFunction(AnalyticsIntegerSumFunction, 1, sqliteFunctionFlags, func() sqlite3.AggregateFunction {
		return &ncrucesIntegerAggregate{}
	}); err != nil {
		return fmt.Errorf("sqlite register aggregate %s: %w", AnalyticsIntegerSumFunction, err)
	}
	if err := connection.CreateWindowFunction(AnalyticsDecimalSumFunction, 2, sqliteFunctionFlags, func() sqlite3.AggregateFunction {
		return &ncrucesDecimalAggregate{}
	}); err != nil {
		return fmt.Errorf("sqlite register aggregate %s: %w", AnalyticsDecimalSumFunction, err)
	}
	if err := connection.CreateWindowFunction(AnalyticsDecimalAvgFunction, 2, sqliteFunctionFlags, func() sqlite3.AggregateFunction {
		return &ncrucesDecimalAggregate{value: exactDecimalAggregate{average: true}}
	}); err != nil {
		return fmt.Errorf("sqlite register aggregate %s: %w", AnalyticsDecimalAvgFunction, err)
	}
	if err := connection.CreateCollation(AnalyticsNumericCollation, func(left, right []byte) int {
		return compareAnalyticsNumbers(string(left), string(right))
	}); err != nil {
		return fmt.Errorf("sqlite register collation %s: %w", AnalyticsNumericCollation, err)
	}
	return nil
}

func sqliteDriverValues(arguments []sqlite3.Value) []driver.Value {
	values := make([]driver.Value, len(arguments))
	for index, argument := range arguments {
		switch argument.Type() {
		case sqlite3.NULL:
			values[index] = nil
		case sqlite3.INTEGER:
			values[index] = argument.Int64()
		case sqlite3.FLOAT:
			values[index] = argument.Float()
		case sqlite3.TEXT:
			values[index] = argument.Text()
		case sqlite3.BLOB:
			values[index] = argument.Blob(nil)
		}
	}
	return values
}

func setSQLiteResult(context sqlite3.Context, value driver.Value) {
	switch typed := value.(type) {
	case nil:
		context.ResultNull()
	case int64:
		context.ResultInt64(typed)
	case float64:
		context.ResultFloat(typed)
	case string:
		context.ResultText(typed)
	case []byte:
		context.ResultBlob(typed)
	case bool:
		context.ResultBool(typed)
	default:
		context.ResultError(fmt.Errorf("sqlite function returned unsupported type %T", value))
	}
}

type ncrucesIntegerAggregate struct{ value exactIntegerAggregate }

func (aggregate *ncrucesIntegerAggregate) Step(context sqlite3.Context, arguments ...sqlite3.Value) {
	if err := aggregate.value.Step(sqliteDriverValues(arguments)); err != nil {
		context.ResultError(err)
	}
}

func (aggregate *ncrucesIntegerAggregate) Value(context sqlite3.Context) {
	value, err := aggregate.value.Value()
	if err != nil {
		context.ResultError(err)
		return
	}
	setSQLiteResult(context, value)
}

type ncrucesDecimalAggregate struct{ value exactDecimalAggregate }

func (aggregate *ncrucesDecimalAggregate) Step(context sqlite3.Context, arguments ...sqlite3.Value) {
	if err := aggregate.value.Step(sqliteDriverValues(arguments)); err != nil {
		context.ResultError(err)
	}
}

func (aggregate *ncrucesDecimalAggregate) Value(context sqlite3.Context) {
	value, err := aggregate.value.Value()
	if err != nil {
		context.ResultError(err)
		return
	}
	setSQLiteResult(context, value)
}
