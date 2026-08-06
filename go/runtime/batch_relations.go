package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	readsql "github.com/eleven-am/golem/go/internal/read/sql"
)

// MaxBatchLoaderKeys is an execution-owned memory/statement-complexity guard.
// SQL parameter bounds are separately enforced per chunk by read/sql.
const MaxBatchLoaderKeys = 90_000

type relationLoadStrategy uint8

const (
	relationLoadBatched relationLoadStrategy = iota + 1
	relationLoadCorrelated
	relationLoadCorrelatedOracle
)

type relationLoadStrategyContextKey struct{}

func forcedRelationLoadStrategy(ctx context.Context) (relationLoadStrategy, bool) {
	value, ok := ctx.Value(relationLoadStrategyContextKey{}).(relationLoadStrategy)
	return value, ok && (value == relationLoadBatched || value == relationLoadCorrelatedOracle)
}

func executeToManyBatch[P, A any](ctx context.Context, app *App[P, A], operation golem.ReadOperation, parent readplan.Plan, relation readplan.Relation, endpoint schema.RelationEndpoint, parents []executedRow) ([][]executedRow, error) {
	return executeRelationBatch(ctx, app, operation, parent, relation, endpoint, relation.Child(), parents)
}

// executeToOneBatch preserves cardinality checking without issuing one query
// per parent. A cap of two is enough to distinguish zero, one, and corrupt
// multi-row results; the caller performs the invariant check after attachment.
func executeToOneBatch[P, A any](ctx context.Context, app *App[P, A], operation golem.ReadOperation, parent readplan.Plan, relation readplan.Relation, endpoint schema.RelationEndpoint, parents []executedRow) ([][]executedRow, error) {
	child, err := readplan.WithMaximumTake(relation.Child(), 2)
	if err != nil {
		return nil, err
	}
	return executeRelationBatch(ctx, app, operation, parent, relation, endpoint, child, parents)
}

func executeRelationBatch[P, A any](ctx context.Context, app *App[P, A], operation golem.ReadOperation, parent readplan.Plan, relation readplan.Relation, endpoint schema.RelationEndpoint, child readplan.Plan, parents []executedRow) ([][]executedRow, error) {
	attached := make([][]executedRow, len(parents))
	for index := range attached {
		attached[index] = make([]executedRow, 0)
	}
	if len(parents) == 0 {
		return attached, nil
	}
	if take, ok := child.Take(); ok && take == 0 {
		return attached, nil
	}

	parentIdentities := make([]string, len(parents))
	keys := make([][]policyir.Value, 0, len(parents))
	seen := make(map[string]struct{}, len(parents))
	for index := range parents {
		tuple, absent, err := batchParentTuple(endpoint, parents[index].values)
		if err != nil {
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(parent.ModelID()), golem.FieldID(relation.FieldID()), "batch parent correlation could not be decoded", err)
		}
		if absent {
			continue
		}
		identity, err := canonicalBatchTuple(tuple)
		if err != nil {
			return nil, err
		}
		parentIdentities[index] = identity
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		seen[identity] = struct{}{}
		keys = append(keys, tuple)
	}
	if len(keys) == 0 {
		return attached, nil
	}
	if err := enforceBatchLoaderKeyLimitWith(operation, golem.ModelID(parent.ModelID()), golem.FieldID(relation.FieldID()), len(keys), app.readLimits.loaderKeys); err != nil {
		return nil, err
	}
	capacity, err := readsql.BatchKeyCapacity(child, endpoint, app.registry, app.provider, app.capabilities)
	if err != nil || capacity <= 0 {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(parent.ModelID()), golem.FieldID(relation.FieldID()), "bounded relation batch could not be planned", err)
	}

	buckets := make(map[string][]executedRow, len(keys))
	for start := 0; start < len(keys); start += capacity {
		end := start + capacity
		if end > len(keys) {
			end = len(keys)
		}
		statement, renderErr := readsql.RenderBatch(child, endpoint, keys[start:end], app.registry, app.provider, app.capabilities)
		if renderErr != nil {
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(parent.ModelID()), golem.FieldID(relation.FieldID()), "bounded relation batch did not render", renderErr)
		}
		decoded, decodeErr := executeBatchStatement(ctx, app, operation, child, statement)
		if decodeErr != nil {
			return nil, decodeErr
		}
		decoded, decodeErr = finishPlanRows(ctx, app, operation, child, decoded)
		if decodeErr != nil {
			return nil, decodeErr
		}
		for _, row := range decoded {
			tuple, tupleErr := batchChildTuple(endpoint, row)
			if tupleErr != nil {
				return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(parent.ModelID()), golem.FieldID(relation.FieldID()), "batch child correlation could not be decoded", tupleErr)
			}
			identity, tupleErr := canonicalBatchTuple(tuple)
			if tupleErr != nil {
				return nil, tupleErr
			}
			buckets[identity] = append(buckets[identity], row)
		}
	}

	reverse := false
	if take, ok := child.Take(); ok && take < 0 {
		reverse = true
	}
	if reverse {
		for identity := range buckets {
			rows := buckets[identity]
			for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
				rows[left], rows[right] = rows[right], rows[left]
			}
			buckets[identity] = rows
		}
	}
	for index, identity := range parentIdentities {
		if identity == "" {
			continue
		}
		// Own the slice header for each parent. Runtime rows are immutable, so
		// parents sharing a key may safely share their row values.
		attached[index] = append(make([]executedRow, 0, len(buckets[identity])), buckets[identity]...)
		if err := enforceDecodedResultLimit(operation, child, golem.FieldID(relation.FieldID()), len(attached[index])); err != nil {
			return nil, err
		}
	}
	return attached, nil
}

func enforceBatchLoaderKeyLimit(operation golem.ReadOperation, model golem.ModelID, field golem.FieldID, keys int) error {
	return enforceBatchLoaderKeyLimitWith(operation, model, field, keys, MaxBatchLoaderKeys)
}

func enforceBatchLoaderKeyLimitWith(operation golem.ReadOperation, model golem.ModelID, field golem.FieldID, keys, maximum int) error {
	if maximum > 0 && maximum <= MaxBatchLoaderKeys && keys <= maximum {
		return nil
	}
	return golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), model, field, "relation loader key limit exceeded", nil)
}

// executeToManyCorrelatedOracle preserves the pre-batch implementation only as
// an internal semantic oracle. Production never chooses it until a genuine
// single-statement correlated renderer exists; this function intentionally
// makes its per-parent query shape obvious.
func executeToManyCorrelatedOracle[P, A any](ctx context.Context, app *App[P, A], operation golem.ReadOperation, parent readplan.Plan, relation readplan.Relation, endpoint schema.RelationEndpoint, parents []executedRow) ([][]executedRow, error) {
	result := make([][]executedRow, len(parents))
	for index := range result {
		result[index] = make([]executedRow, 0)
		correlation, absent, err := relationCorrelation(app, relation.Child(), endpoint, parents[index].values)
		if err != nil {
			return nil, err
		}
		if absent {
			continue
		}
		child, err := readplan.WithAdditionalWhere(relation.Child(), correlation)
		if err != nil {
			return nil, err
		}
		rows, err := executePlan(ctx, app, operation, child)
		if err != nil {
			return nil, err
		}
		result[index] = rows
	}
	return result, nil
}

func executeBatchStatement[P, A any](ctx context.Context, app *App[P, A], operation golem.ReadOperation, child readplan.Plan, statement readsql.BatchStatement) ([]executedRow, error) {
	decoder, err := readdecode.NewBatch(child, app.registry, app.provider, statement.ExtraCorrelationFields())
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(child.ModelID()), golem.FieldID{}, "batch decoder could not be built", err)
	}
	rows, err := app.database.QueryxContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(child.ModelID()), golem.FieldID{}, "batch relation execution failed", err)
	}
	result := make([]executedRow, 0)
	for rows.Next() {
		scan := decoder.NewScan()
		if err := rows.Scan(scan.Destinations()...); err != nil {
			rows.Close()
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(child.ModelID()), golem.FieldID{}, "batch relation scan failed", err)
		}
		decoded, extraKeys, err := scan.Decode()
		if err != nil {
			rows.Close()
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(child.ModelID()), golem.FieldID{}, "batch relation decode failed", err)
		}
		values := make(map[policyir.FieldID]readdecode.Cell, len(decoded))
		for _, cell := range decoded {
			values[cell.FieldID()] = cell
		}
		decodedCounts, err := scan.RelationCounts()
		if err != nil {
			rows.Close()
			return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(child.ModelID()), golem.FieldID{}, "batch relation-count decode failed", err)
		}
		counts := make(map[policyir.FieldID]int64, len(decodedCounts))
		for _, count := range decodedCounts {
			counts[count.FieldID()] = count.Value()
		}
		result = append(result, executedRow{values: values, counts: counts, batchKeys: extraKeys})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(child.ModelID()), golem.FieldID{}, "batch relation result stream failed", err)
	}
	if err := rows.Close(); err != nil {
		return nil, golem.RuntimeReadError(golem.CodeBadUserInput, operationName(operation), golem.ModelID(child.ModelID()), golem.FieldID{}, "batch relation result stream could not be closed", err)
	}
	return result, nil
}

func batchParentTuple(endpoint schema.RelationEndpoint, values map[policyir.FieldID]readdecode.Cell) ([]policyir.Value, bool, error) {
	pairs := endpoint.Correlation()
	tuple := make([]policyir.Value, len(pairs))
	for index, pair := range pairs {
		cell, ok := values[policyir.FieldID(pair.ParentFieldID())]
		if !ok {
			return nil, false, fmt.Errorf("parent field %x was not loaded", pair.ParentFieldID())
		}
		if cell.IsNull() {
			return nil, true, nil
		}
		value, ok := cell.PolicyValue()
		if !ok {
			return nil, false, fmt.Errorf("parent field %x has no exact scalar value", pair.ParentFieldID())
		}
		tuple[index] = value
	}
	return tuple, false, nil
}

func batchChildTuple(endpoint schema.RelationEndpoint, row executedRow) ([]policyir.Value, error) {
	pairs := endpoint.Correlation()
	tuple := make([]policyir.Value, len(pairs))
	for index, pair := range pairs {
		field := policyir.FieldID(pair.ChildFieldID())
		if cell, ok := row.values[field]; ok {
			value, present := cell.PolicyValue()
			if !present {
				return nil, fmt.Errorf("child field %x is NULL or non-scalar", field)
			}
			tuple[index] = value
			continue
		}
		value, ok := row.batchKeys[field]
		if !ok {
			return nil, fmt.Errorf("child field %x was not loaded", field)
		}
		tuple[index] = value
	}
	return tuple, nil
}

// canonicalBatchTuple is a length-delimited, type-tagged encoding of the
// closed exact policy scalar vocabulary. It never uses fmt/string coercion,
// so e.g. int(1), string("1"), float(1), and enum labels cannot collide.
func canonicalBatchTuple(values []policyir.Value) (string, error) {
	var buffer bytes.Buffer
	writeU32(&buffer, uint32(len(values)))
	for _, value := range values {
		buffer.WriteByte(byte(value.Kind()))
		switch value.Kind() {
		case policyir.ValueBool:
			item, _ := value.Bool()
			if item {
				buffer.WriteByte(1)
			} else {
				buffer.WriteByte(0)
			}
		case policyir.ValueInt16, policyir.ValueInt32, policyir.ValueInt64:
			item, _ := value.Signed()
			writeU64(&buffer, uint64(item))
		case policyir.ValueFloat32:
			item, _ := value.Float32Bits()
			writeU32(&buffer, item)
		case policyir.ValueFloat64:
			item, _ := value.Float64Bits()
			writeU64(&buffer, item)
		case policyir.ValueDecimal:
			coefficient, scale, _ := value.Decimal()
			writeU64(&buffer, uint64(coefficient))
			buffer.WriteByte(scale)
		case policyir.ValueString:
			item, _ := value.Text()
			writeBytes(&buffer, []byte(item))
		case policyir.ValueBytes:
			item, _ := value.Bytes()
			writeBytes(&buffer, item)
		case policyir.ValueUUID:
			item, _ := value.UUID()
			buffer.Write(item[:])
		case policyir.ValueDate:
			year, month, day, _ := value.Date()
			writeU32(&buffer, uint32(int32(year)))
			buffer.WriteByte(month)
			buffer.WriteByte(day)
		case policyir.ValueTime:
			item, _ := value.Time()
			writeU64(&buffer, uint64(item))
		case policyir.ValueDateTime:
			seconds, nanos, _ := value.DateTime()
			writeU64(&buffer, uint64(seconds))
			writeU32(&buffer, nanos)
		case policyir.ValueEnum:
			enum, member, _ := value.Enum()
			buffer.Write(enum[:])
			buffer.Write(member[:])
		default:
			return "", fmt.Errorf("P3_RUNTIME_BATCH_KEY: unsupported correlation value kind %d", value.Kind())
		}
	}
	return buffer.String(), nil
}

func writeBytes(buffer *bytes.Buffer, value []byte) {
	writeU32(buffer, uint32(len(value)))
	buffer.Write(value)
}

func writeU32(buffer *bytes.Buffer, value uint32) {
	var data [4]byte
	binary.BigEndian.PutUint32(data[:], value)
	buffer.Write(data[:])
}

func writeU64(buffer *bytes.Buffer, value uint64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	buffer.Write(data[:])
}
