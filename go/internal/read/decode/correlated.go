package decode

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type CorrelatedRow struct {
	cells  []Cell
	counts []RelationCount
}

func (row CorrelatedRow) Cells() []Cell { return append([]Cell(nil), row.cells...) }
func (row CorrelatedRow) RelationCounts() []RelationCount {
	return append([]RelationCount(nil), row.counts...)
}

// Correlated decodes the explicit text-only JSON boundary emitted by the
// correlated renderer. Logical schema types, never JSON numeric inference,
// reconstruct exact policy/runtime values.
func Correlated(plan readplan.Plan, registry *schema.Registry, provider policyir.Provider, payload string) ([]CorrelatedRow, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(payload))
	decoder.UseNumber()
	var objects []map[string]any
	if err := decoder.Decode(&objects); err != nil {
		return nil, fmt.Errorf("P3_DECODE_CORRELATED: malformed relation JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("P3_DECODE_CORRELATED: trailing relation JSON")
	}
	fields := plan.Fields()
	counts := plan.RelationCounts()
	result := make([]CorrelatedRow, len(objects))
	for rowIndex, object := range objects {
		if len(object) != len(fields)+len(counts) {
			return nil, fmt.Errorf("P3_DECODE_CORRELATED: row %d has an invalid key inventory", rowIndex)
		}
		row := CorrelatedRow{cells: make([]Cell, len(fields)), counts: make([]RelationCount, len(counts))}
		for index, field := range fields {
			key := fmt.Sprintf("golem_c%d", index)
			encoded, ok := object[key]
			if !ok {
				return nil, &Error{Field: field.FieldID(), Detail: "correlated JSON field is absent"}
			}
			if encoded == nil {
				if !field.Nullable() {
					return nil, &Error{Field: field.FieldID(), Detail: "correlated JSON returned NULL for a non-null field"}
				}
				row.cells[index] = Cell{field: field.FieldID(), public: field.Public(), runtime: golem.RuntimeNullReadCell(golem.FieldID(field.FieldID())), evaluator: evaluate.NullField(field.FieldID()), null: true}
				continue
			}
			text, ok := encoded.(string)
			if !ok {
				return nil, &Error{Field: field.FieldID(), Detail: fmt.Sprintf("correlated JSON value has non-text type %T", encoded)}
			}
			raw, err := correlatedRaw(provider, field.LogicalType(), text)
			if err != nil {
				return nil, &Error{Field: field.FieldID(), Detail: "correlated JSON scalar conversion failed", Cause: err}
			}
			cell, err := decodeValue(registry, provider, plan.ModelID(), field, raw)
			if err != nil {
				return nil, &Error{Field: field.FieldID(), Detail: "correlated JSON logical decode failed", Cause: err}
			}
			row.cells[index] = cell
		}
		for index, count := range counts {
			key := fmt.Sprintf("golem_count%d", index)
			encoded, ok := object[key]
			if !ok || encoded == nil {
				return nil, &Error{Field: count.FieldID(), Detail: "correlated relation count is absent or NULL"}
			}
			text, ok := encoded.(string)
			if !ok {
				return nil, &Error{Field: count.FieldID(), Detail: "correlated relation count is not text"}
			}
			value, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				return nil, &Error{Field: count.FieldID(), Detail: "correlated relation count is invalid", Cause: err}
			}
			row.counts[index] = RelationCount{field: count.FieldID(), relation: count.RelationID(), value: value, occurrence: count.OccurrenceID()}
		}
		result[rowIndex] = row
	}
	return result, nil
}

func correlatedRaw(provider policyir.Provider, typ compilerir.LogicalTypeIR, text string) (any, error) {
	switch typ.Kind {
	case compilerir.TypeBool:
		value := text == "1" || strings.EqualFold(text, "true") || text == "t"
		if !value && text != "0" && !strings.EqualFold(text, "false") && text != "f" {
			return nil, fmt.Errorf("invalid boolean %q", text)
		}
		if provider == policyir.ProviderSQLite {
			if value {
				return int64(1), nil
			}
			return int64(0), nil
		}
		return value, nil
	case compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64:
		return strconv.ParseInt(text, 10, 64)
	case compilerir.TypeFloat32, compilerir.TypeFloat64:
		return strconv.ParseFloat(text, 64)
	case compilerir.TypeDecimal:
		if provider == policyir.ProviderSQLite {
			return strconv.ParseInt(text, 10, 64)
		}
		return text, nil
	case compilerir.TypeBytes:
		return hex.DecodeString(text)
	case compilerir.TypeDateTime:
		if provider == policyir.ProviderSQLite {
			return strconv.ParseInt(text, 10, 64)
		}
		instant, err := time.Parse("2006-01-02T15:04:05.000000Z", text)
		if err != nil {
			return nil, err
		}
		return instant.UTC(), nil
	case compilerir.TypeString, compilerir.TypeUUID, compilerir.TypeDate, compilerir.TypeTime,
		compilerir.TypeEnum, compilerir.TypeJSON, compilerir.TypeScalarList:
		return text, nil
	default:
		return nil, fmt.Errorf("unsupported correlated logical type %q", typ.Kind)
	}
}
