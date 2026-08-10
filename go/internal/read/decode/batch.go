package decode

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

// BatchDecoder appends exact private correlation-key destinations after the
// ordinary child projection. Keys already present in the child plan are not
// duplicated by the batch renderer and therefore do not appear in extra.
type BatchDecoder struct {
	base     Decoder
	registry *schema.Registry
	provider policyir.Provider
	model    policyir.ModelID
	extra    []policyir.FieldID
}

func NewBatch(plan readplan.Plan, registry *schema.Registry, provider policyir.Provider, extra []policyir.FieldID) (BatchDecoder, error) {
	base, err := New(plan, registry, provider)
	if err != nil {
		return BatchDecoder{}, err
	}
	seen := make(map[policyir.FieldID]bool, len(extra))
	for _, field := range extra {
		fact, ok := registry.Field(golem.ModelID(plan.ModelID()), golem.FieldID(field))
		if !ok || fact.Kind() != compilerir.FieldScalar || seen[field] {
			return BatchDecoder{}, fmt.Errorf("P3_DECODE_BATCH: invalid or duplicate correlation field %x", field)
		}
		seen[field] = true
	}
	return BatchDecoder{base: base, registry: registry, provider: provider, model: plan.ModelID(), extra: append([]policyir.FieldID(nil), extra...)}, nil
}

type BatchScan struct {
	decoder BatchDecoder
	base    *Scan
	extra   []slot
}

func (decoder BatchDecoder) NewScan() *BatchScan {
	result := &BatchScan{decoder: decoder, base: decoder.base.NewScan(), extra: make([]slot, len(decoder.extra))}
	for index, field := range decoder.extra {
		fact, _ := decoder.registry.Field(golem.ModelID(decoder.model), golem.FieldID(field))
		result.extra[index].kind = scanKind(decoder.provider, fact.LogicalType().Kind)
	}
	return result
}

func (scan *BatchScan) Destinations() []any {
	result := scan.base.Destinations()
	for index := range scan.extra {
		result = append(result, scan.extra[index].destination())
	}
	return result
}

func (scan *BatchScan) Decode() ([]Cell, map[policyir.FieldID]policyir.Value, error) {
	cells, err := scan.base.Decode()
	if err != nil {
		return nil, nil, err
	}
	keys := make(map[policyir.FieldID]policyir.Value, len(scan.extra))
	for index := range scan.extra {
		raw, present := scan.extra[index].value()
		field := scan.decoder.extra[index]
		if !present {
			return nil, nil, &Error{Field: field, Detail: "batch correlation key returned NULL"}
		}
		fact, _ := scan.decoder.registry.Field(golem.ModelID(scan.decoder.model), golem.FieldID(field))
		value, valueErr := decodeBatchPolicyValue(scan.decoder.registry, scan.decoder.provider, fact.LogicalType(), raw)
		if valueErr != nil {
			return nil, nil, &Error{Field: field, Detail: "batch correlation key decode failed", Cause: valueErr}
		}
		keys[field] = value
	}
	return cells, keys, nil
}

func (scan *BatchScan) RelationCounts() ([]RelationCount, error) { return scan.base.RelationCounts() }

func decodeBatchPolicyValue(registry *schema.Registry, provider policyir.Provider, typ compilerir.LogicalTypeIR, raw any) (policyir.Value, error) {
	switch typ.Kind {
	case compilerir.TypeBool:
		if value, ok := raw.(bool); ok {
			return policyir.BoolValue(value), nil
		}
		value, ok := raw.(int64)
		if !ok || value < 0 || value > 1 {
			return policyir.Value{}, fmt.Errorf("invalid boolean %T", raw)
		}
		return policyir.BoolValue(value == 1), nil
	case compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64:
		value, ok := raw.(int64)
		if !ok {
			return policyir.Value{}, fmt.Errorf("invalid integer %T", raw)
		}
		return policyir.SignedValue(mapKind(typ.Kind), value)
	case compilerir.TypeFloat32:
		value, ok := raw.(float64)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) || float64(float32(value)) != value {
			return policyir.Value{}, fmt.Errorf("invalid exact float32")
		}
		return policyir.Float32Value(float32(value))
	case compilerir.TypeFloat64:
		value, ok := raw.(float64)
		if !ok {
			return policyir.Value{}, fmt.Errorf("invalid float64 %T", raw)
		}
		return policyir.Float64Value(value)
	case compilerir.TypeDecimal:
		var decimal golem.Decimal
		var err error
		if provider == policyir.ProviderSQLite {
			value, ok := raw.(int64)
			if !ok {
				return policyir.Value{}, fmt.Errorf("invalid SQLite decimal")
			}
			scale := uint8(0)
			if typ.Scale != nil {
				scale = uint8(*typ.Scale)
			}
			decimal, err = golem.NewDecimal(value, scale)
		} else {
			text, ok := raw.(string)
			if !ok {
				return policyir.Value{}, fmt.Errorf("invalid PostgreSQL decimal")
			}
			decimal, err = golem.ParseDecimal(text)
		}
		if err != nil {
			return policyir.Value{}, err
		}
		return policyir.NewDecimalValue(decimal.Coefficient(), decimal.Scale())
	case compilerir.TypeString:
		value, ok := raw.(string)
		if !ok {
			return policyir.Value{}, fmt.Errorf("invalid string %T", raw)
		}
		return policyir.StringValue(value)
	case compilerir.TypeBytes:
		value, ok := raw.([]byte)
		if !ok {
			return policyir.Value{}, fmt.Errorf("invalid bytes %T", raw)
		}
		return policyir.BytesValue(value), nil
	case compilerir.TypeUUID:
		value, ok := raw.(string)
		if !ok {
			return policyir.Value{}, fmt.Errorf("invalid UUID %T", raw)
		}
		uuid, err := golem.ParseUUID(value)
		if err != nil {
			return policyir.Value{}, err
		}
		return policyir.UUIDValue(uuid.Bytes()), nil
	case compilerir.TypeDate:
		var date golem.Date
		var err error
		if instant, ok := raw.(time.Time); ok {
			instant = instant.UTC()
			date, err = golem.NewDate(instant.Year(), instant.Month(), instant.Day())
		} else if text, ok := raw.(string); ok {
			date, err = golem.ParseDate(strings.TrimSpace(text))
		} else {
			return policyir.Value{}, fmt.Errorf("invalid date %T", raw)
		}
		if err != nil {
			return policyir.Value{}, err
		}
		return policyir.NewDateValue(int16(date.Year()), uint8(date.Month()), uint8(date.Day()))
	case compilerir.TypeTime:
		text, ok := raw.(string)
		if !ok {
			return policyir.Value{}, fmt.Errorf("invalid time %T", raw)
		}
		clock, err := golem.ParseTime(strings.TrimSpace(text))
		if err != nil {
			return policyir.Value{}, err
		}
		return policyir.NewTimeValue(clock.Microseconds())
	case compilerir.TypeDateTime:
		var instant time.Time
		if provider == policyir.ProviderSQLite {
			micros, ok := raw.(int64)
			if !ok {
				return policyir.Value{}, fmt.Errorf("invalid SQLite datetime")
			}
			seconds := floorDiv(micros, 1_000_000)
			instant = time.Unix(seconds, (micros-seconds*1_000_000)*1_000).UTC()
		} else {
			var ok bool
			instant, ok = raw.(time.Time)
			if !ok {
				return policyir.Value{}, fmt.Errorf("invalid PostgreSQL datetime")
			}
			instant = instant.UTC()
		}
		return policyir.NewDateTimeValue(instant.Unix(), uint32(instant.Nanosecond()))
	case compilerir.TypeEnum:
		if typ.EnumID == nil {
			return policyir.Value{}, fmt.Errorf("enum identity is absent")
		}
		wire, ok := raw.(string)
		if !ok {
			return policyir.Value{}, fmt.Errorf("invalid enum %T", raw)
		}
		member, ok := registry.EnumValue(*typ.EnumID, wire)
		if !ok {
			return policyir.Value{}, fmt.Errorf("unknown enum wire value")
		}
		enumID, err := fixedID(string(*typ.EnumID))
		if err != nil {
			return policyir.Value{}, err
		}
		memberID, err := fixedID(string(member))
		if err != nil {
			return policyir.Value{}, err
		}
		return policyir.NewEnumValue(policyir.EnumID(enumID), policyir.EnumValueID(memberID))
	default:
		return policyir.Value{}, fmt.Errorf("logical type %q cannot be a batch correlation key", typ.Kind)
	}
}
