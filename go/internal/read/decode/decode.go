// Package decode owns exact, logical-type-directed row scanning for P3 reads.
// Every destination is chosen before rows.Scan from the fingerprinted schema;
// provider return types never choose the public logical type.
package decode

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
)

type Error struct {
	Field  policyir.FieldID
	Detail string
	Cause  error
}

func (failure *Error) Error() string {
	return fmt.Sprintf("P3_DECODE: field=%x: %s", failure.Field, failure.Detail)
}
func (failure *Error) Unwrap() error { return failure.Cause }

type Decoder struct {
	model    policyir.ModelID
	provider policyir.Provider
	registry *schema.Registry
	fields   []readplan.Field
	direct   []directField
	counts   []readplan.RelationCount
}

func New(plan readplan.Plan, registry *schema.Registry, provider policyir.Provider) (Decoder, error) {
	if registry == nil || plan.ModelID() == (policyir.ModelID{}) || (provider != policyir.ProviderSQLite && provider != policyir.ProviderPostgreSQL) {
		return Decoder{}, fmt.Errorf("P3_DECODE_INPUT: plan, registry, and provider are required")
	}
	return Decoder{model: plan.ModelID(), provider: provider, registry: registry, fields: plan.Fields(), counts: plan.RelationCounts()}, nil
}

// NewFields constructs the exact P3 scalar decoder for an explicitly ordered
// internal projection. Mutation RETURNING and locked-image statements use this
// seam because their columns are derived from MutationIR rather than a public
// read plan. Direct fields are always private: callers must apply the explicit
// mutation result projection before constructing a public row.
func NewFields(model policyir.ModelID, registry *schema.Registry, provider policyir.Provider, fields []policyir.FieldID) (Decoder, error) {
	if registry == nil || model == (policyir.ModelID{}) || (provider != policyir.ProviderSQLite && provider != policyir.ProviderPostgreSQL) {
		return Decoder{}, fmt.Errorf("P3_DECODE_INPUT: model, registry, and provider are required")
	}
	if _, ok := registry.Model(golem.ModelID(model)); !ok {
		return Decoder{}, fmt.Errorf("P3_DECODE_INPUT: model is absent from the active registry")
	}
	result := Decoder{model: model, provider: provider, registry: registry, direct: make([]directField, len(fields))}
	seen := make(map[policyir.FieldID]struct{}, len(fields))
	for index, fieldID := range fields {
		field, ok := registry.Field(golem.ModelID(model), golem.FieldID(fieldID))
		if fieldID == (policyir.FieldID{}) || !ok || field.Kind() != compilerir.FieldScalar && field.Kind() != compilerir.FieldEnum && field.Kind() != compilerir.FieldScalarList {
			return Decoder{}, fmt.Errorf("P3_DECODE_INPUT: direct field %x is zero, foreign, absent, or relational", fieldID)
		}
		if _, duplicate := seen[fieldID]; duplicate {
			return Decoder{}, fmt.Errorf("P3_DECODE_INPUT: direct field %x appears more than once", fieldID)
		}
		seen[fieldID] = struct{}{}
		result.direct[index] = directField{id: fieldID, typ: field.LogicalType(), nullable: field.Nullable()}
	}
	return result, nil
}

type plannedField interface {
	FieldID() policyir.FieldID
	LogicalType() compilerir.LogicalTypeIR
	Nullable() bool
	Public() bool
}

type directField struct {
	id       policyir.FieldID
	typ      compilerir.LogicalTypeIR
	nullable bool
}

func (field directField) FieldID() policyir.FieldID             { return field.id }
func (field directField) LogicalType() compilerir.LogicalTypeIR { return field.typ }
func (field directField) Nullable() bool                        { return field.nullable }
func (directField) Public() bool                                { return false }

func (decoder Decoder) fieldCount() int {
	if decoder.direct != nil {
		return len(decoder.direct)
	}
	return len(decoder.fields)
}

func (decoder Decoder) field(index int) plannedField {
	if decoder.direct != nil {
		return decoder.direct[index]
	}
	return decoder.fields[index]
}

type slotKind uint8

const (
	slotBool slotKind = iota + 1
	slotInteger
	slotFloat
	slotText
	slotBytes
	slotDateTime
)

type slot struct {
	kind     slotKind
	boolean  sql.NullBool
	integer  sql.NullInt64
	floating sql.NullFloat64
	text     sql.NullString
	bytes    []byte
	instant  sql.NullTime
}

func (slot *slot) destination() any {
	switch slot.kind {
	case slotBool:
		return &slot.boolean
	case slotInteger:
		return &slot.integer
	case slotFloat:
		return &slot.floating
	case slotText:
		return &slot.text
	case slotBytes:
		return &slot.bytes
	case slotDateTime:
		return &slot.instant
	default:
		return nil
	}
}
func (slot *slot) value() (any, bool) {
	switch slot.kind {
	case slotBool:
		return slot.boolean.Bool, slot.boolean.Valid
	case slotInteger:
		return slot.integer.Int64, slot.integer.Valid
	case slotFloat:
		return slot.floating.Float64, slot.floating.Valid
	case slotText:
		return slot.text.String, slot.text.Valid
	case slotBytes:
		return append([]byte(nil), slot.bytes...), slot.bytes != nil
	case slotDateTime:
		return slot.instant.Time, slot.instant.Valid
	default:
		return nil, false
	}
}

type Scan struct {
	decoder Decoder
	slots   []slot
	counts  []sql.NullInt64
}

func (decoder Decoder) NewScan() *Scan {
	result := &Scan{decoder: decoder, slots: make([]slot, decoder.fieldCount()), counts: make([]sql.NullInt64, len(decoder.counts))}
	for index := range result.slots {
		field := decoder.field(index)
		result.slots[index].kind = scanKind(decoder.provider, field.LogicalType().Kind)
	}
	return result
}
func (scan *Scan) Destinations() []any {
	result := make([]any, 0, len(scan.slots)+len(scan.counts))
	for index := range scan.slots {
		result = append(result, scan.slots[index].destination())
	}
	for index := range scan.counts {
		result = append(result, &scan.counts[index])
	}
	return result
}

// RawValues returns the provider-facing scalar values produced by the same
// typed scan slots that Decode consumes. Mutation statement programs retain
// these values for prior-result bindings, so identity dataflow never round
// trips through a logical/public value or a provider re-encoder. NULL is
// represented by nil. Mutable byte slices are detached on every call.
func (scan *Scan) RawValues() []any {
	result := make([]any, len(scan.slots))
	for index := range scan.slots {
		value, present := scan.slots[index].value()
		if !present {
			continue
		}
		if bytes, ok := value.([]byte); ok {
			result[index] = append([]byte(nil), bytes...)
			continue
		}
		result[index] = value
	}
	return result
}

type RelationCount struct {
	field      policyir.FieldID
	relation   policyir.RelationID
	value      int64
	occurrence readir.OccurrenceID
}

func (count RelationCount) FieldID() policyir.FieldID         { return count.field }
func (count RelationCount) RelationID() policyir.RelationID   { return count.relation }
func (count RelationCount) Value() int64                      { return count.value }
func (count RelationCount) OccurrenceID() readir.OccurrenceID { return count.occurrence }

// RelationCounts returns the exact signed 64-bit values scanned from SQL
// COUNT(*). A NULL or invalid relation identity is an internal statement/decode
// mismatch; it is never silently coerced to zero.
func (scan *Scan) RelationCounts() ([]RelationCount, error) {
	result := make([]RelationCount, len(scan.counts))
	for index, value := range scan.counts {
		planned := scan.decoder.counts[index]
		if !value.Valid || planned.RelationID() == (policyir.RelationID{}) {
			return nil, &Error{Field: planned.FieldID(), Detail: "relation count returned NULL or has a zero relation identity"}
		}
		result[index] = RelationCount{field: planned.FieldID(), relation: planned.RelationID(), value: value.Int64, occurrence: planned.OccurrenceID()}
	}
	return result, nil
}

type Cell struct {
	field     policyir.FieldID
	public    bool
	runtime   golem.RuntimeReadCell
	evaluator evaluate.Field
	policy    policyir.Value
	null      bool
}

func (cell Cell) FieldID() policyir.FieldID          { return cell.field }
func (cell Cell) Public() bool                       { return cell.public }
func (cell Cell) RuntimeCell() golem.RuntimeReadCell { return cell.runtime }
func (cell Cell) EvaluatorField() evaluate.Field     { return cell.evaluator }
func (cell Cell) PolicyValue() (policyir.Value, bool) {
	return cell.policy, !cell.null && cell.policy.Kind() != 0
}
func (cell Cell) IsNull() bool { return cell.null }

func (scan *Scan) Decode() ([]Cell, error) {
	result := make([]Cell, len(scan.slots))
	for index := range scan.slots {
		field := scan.decoder.field(index)
		raw, present := scan.slots[index].value()
		if !present {
			if !field.Nullable() {
				return nil, &Error{Field: field.FieldID(), Detail: "database returned NULL for a non-null logical field"}
			}
			result[index] = Cell{field: field.FieldID(), public: field.Public(), runtime: golem.RuntimeNullReadCell(golem.FieldID(field.FieldID())), evaluator: evaluate.NullField(field.FieldID()), null: true}
			continue
		}
		cell, err := decodeValue(scan.decoder.registry, scan.decoder.provider, scan.decoder.model, field, raw)
		if err != nil {
			return nil, &Error{Field: field.FieldID(), Detail: "logical value decode failed", Cause: err}
		}
		result[index] = cell
	}
	return result, nil
}

func scanKind(provider policyir.Provider, kind compilerir.LogicalTypeKind) slotKind {
	switch kind {
	case compilerir.TypeBool:
		if provider == policyir.ProviderPostgreSQL {
			return slotBool
		}
		return slotInteger
	case compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64:
		return slotInteger
	case compilerir.TypeFloat32, compilerir.TypeFloat64:
		return slotFloat
	case compilerir.TypeDecimal:
		if provider == policyir.ProviderSQLite {
			return slotInteger
		}
		return slotText
	case compilerir.TypeBytes:
		return slotBytes
	case compilerir.TypeDateTime:
		if provider == policyir.ProviderSQLite {
			return slotInteger
		}
		return slotDateTime
	case compilerir.TypeDate:
		if provider == policyir.ProviderPostgreSQL {
			return slotDateTime
		}
		return slotText
	default:
		return slotText
	}
}

func decodeValue(registry *schema.Registry, provider policyir.Provider, model policyir.ModelID, field plannedField, raw any) (Cell, error) {
	typ, id := field.LogicalType(), field.FieldID()
	cell := Cell{field: id, public: field.Public()}
	var value any
	var policyValue policyir.Value
	var err error
	switch typ.Kind {
	case compilerir.TypeBool:
		boolean, ok := raw.(bool)
		if !ok {
			integer, valid := raw.(int64)
			if !valid || (integer != 0 && integer != 1) {
				return cell, fmt.Errorf("invalid boolean %T", raw)
			}
			boolean = integer == 1
		}
		value, policyValue = boolean, policyir.BoolValue(boolean)
	case compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64:
		integer, ok := raw.(int64)
		if !ok {
			return cell, fmt.Errorf("invalid integer %T", raw)
		}
		kind := mapKind(typ.Kind)
		policyValue, err = policyir.SignedValue(kind, integer)
		if err != nil {
			return cell, err
		}
		switch typ.Kind {
		case compilerir.TypeInt16:
			value = int16(integer)
		case compilerir.TypeInt32:
			value = int32(integer)
		default:
			value = integer
		}
	case compilerir.TypeFloat32:
		floating, ok := raw.(float64)
		if !ok || math.IsNaN(floating) || math.IsInf(floating, 0) || float64(float32(floating)) != floating {
			return cell, fmt.Errorf("invalid exact float32")
		}
		value = float32(floating)
		policyValue, err = policyir.Float32Value(value.(float32))
	case compilerir.TypeFloat64:
		floating, ok := raw.(float64)
		if !ok {
			return cell, fmt.Errorf("invalid float64 %T", raw)
		}
		value = floating
		policyValue, err = policyir.Float64Value(floating)
	case compilerir.TypeDecimal:
		var decimal golem.Decimal
		if provider == policyir.ProviderSQLite {
			integer, ok := raw.(int64)
			if !ok {
				return cell, fmt.Errorf("invalid SQLite decimal")
			}
			scale := uint8(0)
			if typ.Scale != nil {
				scale = uint8(*typ.Scale)
			}
			decimal, err = golem.NewDecimal(integer, scale)
		} else {
			decimal, err = golem.ParseDecimal(raw.(string))
		}
		if err != nil {
			return cell, err
		}
		value = decimal
		policyValue, err = policyir.NewDecimalValue(decimal.Coefficient(), decimal.Scale())
	case compilerir.TypeString:
		text, ok := raw.(string)
		if !ok {
			return cell, fmt.Errorf("invalid string %T", raw)
		}
		value = text
		policyValue, err = policyir.StringValue(text)
	case compilerir.TypeBytes:
		bytes, ok := raw.([]byte)
		if !ok {
			return cell, fmt.Errorf("invalid bytes %T", raw)
		}
		value = append([]byte(nil), bytes...)
		policyValue = policyir.BytesValue(bytes)
	case compilerir.TypeUUID:
		uuid, parseErr := golem.ParseUUID(raw.(string))
		if parseErr != nil {
			return cell, parseErr
		}
		value = uuid
		policyValue = policyir.UUIDValue(uuid.Bytes())
	case compilerir.TypeDate:
		var date golem.Date
		var parseErr error
		if instant, ok := raw.(time.Time); ok {
			instant = instant.UTC()
			date, parseErr = golem.NewDate(instant.Year(), instant.Month(), instant.Day())
		} else {
			date, parseErr = golem.ParseDate(strings.TrimSpace(raw.(string)))
		}
		if parseErr != nil {
			return cell, parseErr
		}
		value = date
		policyValue, err = policyir.NewDateValue(int16(date.Year()), uint8(date.Month()), uint8(date.Day()))
	case compilerir.TypeTime:
		clock, parseErr := golem.ParseTime(strings.TrimSpace(raw.(string)))
		if parseErr != nil {
			return cell, parseErr
		}
		value = clock
		policyValue, err = policyir.NewTimeValue(clock.Microseconds())
	case compilerir.TypeDateTime:
		var instant time.Time
		if provider == policyir.ProviderSQLite {
			micros := raw.(int64)
			seconds := floorDiv(micros, 1_000_000)
			remainder := micros - seconds*1_000_000
			instant = time.Unix(seconds, remainder*1_000).UTC()
		} else {
			instant = raw.(time.Time).UTC()
		}
		if instant.Nanosecond()%1_000 != 0 {
			return cell, fmt.Errorf("datetime exceeds microsecond precision")
		}
		value = instant
		policyValue, err = policyir.NewDateTimeValue(instant.Unix(), uint32(instant.Nanosecond()))
	case compilerir.TypeEnum:
		if typ.EnumID == nil {
			return cell, fmt.Errorf("enum identity is absent")
		}
		wire := raw.(string)
		member, ok := registry.EnumValue(*typ.EnumID, wire)
		if !ok {
			return cell, fmt.Errorf("unknown enum wire value")
		}
		enumID, parseErr := fixedID(string(*typ.EnumID))
		if parseErr != nil {
			return cell, parseErr
		}
		memberID, parseErr := fixedID(string(member))
		if parseErr != nil {
			return cell, parseErr
		}
		value = wire
		policyValue, err = policyir.NewEnumValue(policyir.EnumID(enumID), policyir.EnumValueID(memberID))
	case compilerir.TypeJSON:
		bytes := []byte(raw.(string))
		document, parseErr := golem.NewJSONDocument[any](bytes)
		if parseErr != nil {
			return cell, parseErr
		}
		parsed, _ := golem.ParseJSON(document.Bytes())
		jsonValue, parseErr := policyJSON(parsed)
		if parseErr != nil {
			return cell, parseErr
		}
		value = document
		policyValue, err = policyir.NewJSONValue(jsonValue)
	case compilerir.TypeScalarList:
		if typ.Element == nil {
			return cell, fmt.Errorf("scalar-list element type is absent")
		}
		parsed, parseErr := golem.ParseJSON([]byte(raw.(string)))
		if parseErr != nil {
			return cell, parseErr
		}
		array, ok := parsed.(golem.JSONArrayValue)
		if !ok {
			return cell, fmt.Errorf("scalar-list storage is not an array")
		}
		elements := make([]evaluate.ListElement, len(array.Values()))
		policyValues := make([]policyir.Value, len(array.Values()))
		for index, item := range array.Values() {
			element, itemErr := policyListElement(registry, *typ.Element, item)
			if itemErr != nil {
				return cell, itemErr
			}
			policyValues[index] = element
			elements[index], itemErr = evaluate.ValidListElement(element)
			if itemErr != nil {
				return cell, itemErr
			}
		}
		eval, listErr := evaluate.ListField(id, elements...)
		if listErr != nil {
			return cell, listErr
		}
		canonical, _ := golem.CanonicalJSON(array)
		cell.runtime = golem.RuntimeScalarListReadCell(golem.FieldID(id), canonical)
		cell.evaluator = eval
		cell.policy, listErr = policyir.NewListValue(policyValues)
		if listErr != nil {
			return cell, listErr
		}
		return cell, nil
	default:
		return cell, fmt.Errorf("unsupported logical type %q", typ.Kind)
	}
	if err != nil {
		return cell, err
	}
	evaluator, err := evaluate.ValueField(id, policyValue)
	if err != nil {
		return cell, err
	}
	cell.evaluator = evaluator
	cell.policy = policyValue
	switch typed := value.(type) {
	case []byte:
		cell.runtime = golem.RuntimePresentReadCell(golem.FieldID(id), typed, func(input []byte) []byte { return append([]byte(nil), input...) })
	case golem.JSON[any]:
		cell.runtime = golem.RuntimePresentReadCell(golem.FieldID(id), typed, func(input golem.JSON[any]) golem.JSON[any] {
			copy, _ := golem.NewJSONDocument[any](input.Bytes())
			return copy
		})
	default:
		cell.runtime = golem.RuntimePresentReadCell[any](golem.FieldID(id), value, nil)
	}
	return cell, nil
}

func mapKind(kind compilerir.LogicalTypeKind) policyir.ValueKind {
	switch kind {
	case compilerir.TypeInt16:
		return policyir.ValueInt16
	case compilerir.TypeInt32:
		return policyir.ValueInt32
	case compilerir.TypeInt64:
		return policyir.ValueInt64
	default:
		return 0
	}
}
func floorDiv(value, divisor int64) int64 {
	quotient := value / divisor
	if value%divisor < 0 {
		quotient--
	}
	return quotient
}
func fixedID(value string) ([16]byte, error) {
	var result [16]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 {
		return result, fmt.Errorf("invalid fixed ID")
	}
	copy(result[:], decoded)
	return result, nil
}

func policyListElement(registry *schema.Registry, typ compilerir.LogicalTypeIR, value golem.JSONValue) (policyir.Value, error) {
	switch typ.Kind {
	case compilerir.TypeBool:
		typed, ok := value.(golem.JSONBoolValue)
		if !ok {
			return policyir.Value{}, fmt.Errorf("list boolean type mismatch")
		}
		return policyir.BoolValue(typed.Value()), nil
	case compilerir.TypeString:
		typed, ok := value.(golem.JSONStringValue)
		if !ok {
			return policyir.Value{}, fmt.Errorf("list string type mismatch")
		}
		if typ.MaxLength != nil && uint32(utf8.RuneCountInString(typed.Value())) > *typ.MaxLength {
			return policyir.Value{}, fmt.Errorf("list string exceeds maximum rune length %d", *typ.MaxLength)
		}
		return policyir.StringValue(typed.Value())
	case compilerir.TypeInt16, compilerir.TypeInt32, compilerir.TypeInt64:
		typed, ok := value.(golem.JSONNumberValue)
		if !ok {
			return policyir.Value{}, fmt.Errorf("list integer type mismatch")
		}
		decimal, err := golem.ParseDecimal(typed.Canonical())
		if err != nil || decimal.Scale() != 0 {
			return policyir.Value{}, fmt.Errorf("list integer is not exact")
		}
		return policyir.SignedValue(mapKind(typ.Kind), decimal.Coefficient())
	case compilerir.TypeFloat32, compilerir.TypeFloat64:
		typed, ok := value.(golem.JSONNumberValue)
		if !ok {
			return policyir.Value{}, fmt.Errorf("list floating type mismatch")
		}
		bits := 64
		if typ.Kind == compilerir.TypeFloat32 {
			bits = 32
		}
		floating, err := strconv.ParseFloat(typed.Canonical(), bits)
		if err != nil || math.IsNaN(floating) || math.IsInf(floating, 0) {
			return policyir.Value{}, fmt.Errorf("list floating value is invalid")
		}
		if bits == 32 {
			return policyir.Float32Value(float32(floating))
		}
		return policyir.Float64Value(floating)
	case compilerir.TypeDecimal:
		typed, ok := value.(golem.JSONNumberValue)
		if !ok {
			return policyir.Value{}, fmt.Errorf("list decimal type mismatch")
		}
		decimal, err := decimalFromJSONNumber(typed)
		if err != nil {
			return policyir.Value{}, err
		}
		if err := validateListDecimal(decimal, typ); err != nil {
			return policyir.Value{}, err
		}
		return policyir.NewDecimalValue(decimal.Coefficient(), decimal.Scale())
	case compilerir.TypeUUID:
		typed, ok := value.(golem.JSONStringValue)
		if !ok {
			return policyir.Value{}, fmt.Errorf("list UUID type mismatch")
		}
		uuid, err := golem.ParseUUID(typed.Value())
		if err != nil {
			return policyir.Value{}, err
		}
		return policyir.UUIDValue(uuid.Bytes()), nil
	case compilerir.TypeDate:
		typed, ok := value.(golem.JSONStringValue)
		if !ok {
			return policyir.Value{}, fmt.Errorf("list date type mismatch")
		}
		date, err := golem.ParseDate(typed.Value())
		if err != nil {
			return policyir.Value{}, err
		}
		return policyir.NewDateValue(int16(date.Year()), uint8(date.Month()), uint8(date.Day()))
	case compilerir.TypeTime:
		typed, ok := value.(golem.JSONStringValue)
		if !ok {
			return policyir.Value{}, fmt.Errorf("list time type mismatch")
		}
		clock, err := golem.ParseTime(typed.Value())
		if err != nil {
			return policyir.Value{}, err
		}
		if err := validateListTemporalPrecision(typed.Value(), typ, "time"); err != nil {
			return policyir.Value{}, err
		}
		return policyir.NewTimeValue(clock.Microseconds())
	case compilerir.TypeDateTime:
		typed, ok := value.(golem.JSONStringValue)
		if !ok {
			return policyir.Value{}, fmt.Errorf("list datetime type mismatch")
		}
		instant, err := time.Parse(time.RFC3339Nano, typed.Value())
		if err != nil || instant.Nanosecond()%1_000 != 0 {
			return policyir.Value{}, fmt.Errorf("list datetime is invalid")
		}
		if err := validateListTemporalPrecision(typed.Value(), typ, "datetime"); err != nil {
			return policyir.Value{}, err
		}
		return policyir.NewDateTimeValue(instant.Unix(), uint32(instant.Nanosecond()))
	case compilerir.TypeEnum:
		typed, ok := value.(golem.JSONStringValue)
		if !ok || typ.EnumID == nil {
			return policyir.Value{}, fmt.Errorf("list enum type mismatch")
		}
		member, ok := registry.EnumValue(*typ.EnumID, typed.Value())
		if !ok {
			return policyir.Value{}, fmt.Errorf("unknown list enum")
		}
		enumID, _ := fixedID(string(*typ.EnumID))
		memberID, _ := fixedID(string(member))
		return policyir.NewEnumValue(policyir.EnumID(enumID), policyir.EnumValueID(memberID))
	default:
		return policyir.Value{}, fmt.Errorf("unsupported list element %q", typ.Kind)
	}
}

func validateListDecimal(value golem.Decimal, typ compilerir.LogicalTypeIR) error {
	if typ.Precision == nil || typ.Scale == nil {
		return fmt.Errorf("list decimal lacks declared precision or scale")
	}
	if uint16(value.Scale()) > *typ.Scale {
		return fmt.Errorf("list decimal scale %d exceeds declared scale %d", value.Scale(), *typ.Scale)
	}
	digits := strings.TrimPrefix(strconv.FormatInt(value.Coefficient(), 10), "-")
	integerDigits := len(digits) - int(value.Scale())
	if value.Coefficient() == 0 || integerDigits < 0 {
		integerDigits = 0
	}
	if integerDigits+int(*typ.Scale) > int(*typ.Precision) {
		return fmt.Errorf("list decimal exceeds declared precision %d", *typ.Precision)
	}
	return nil
}

func validateListTemporalPrecision(value string, typ compilerir.LogicalTypeIR, name string) error {
	if typ.Precision == nil {
		return fmt.Errorf("list %s lacks declared fractional precision", name)
	}
	if digits := fractionalDigits(value); digits > int(*typ.Precision) {
		return fmt.Errorf("list %s fractional precision %d exceeds declared precision %d", name, digits, *typ.Precision)
	}
	return nil
}

func fractionalDigits(value string) int {
	dot := strings.IndexByte(value, '.')
	if dot < 0 {
		return 0
	}
	end := dot + 1
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	return end - dot - 1
}

func decimalFromJSONNumber(value golem.JSONNumberValue) (golem.Decimal, error) {
	negative, digits, exponent, ok := value.Parts()
	if !ok {
		return golem.Decimal{}, fmt.Errorf("invalid JSON number")
	}
	if exponent > 0 {
		if int64(len(digits))+int64(exponent) > 18 {
			return golem.Decimal{}, fmt.Errorf("decimal exceeds portable precision 18")
		}
		digits += strings.Repeat("0", int(exponent))
		exponent = 0
	}
	if exponent < -18 {
		return golem.Decimal{}, fmt.Errorf("decimal exceeds portable scale 18")
	}
	if negative {
		digits = "-" + digits
	}
	coefficient, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return golem.Decimal{}, err
	}
	return golem.NewDecimal(coefficient, uint8(-exponent))
}

func policyJSON(value golem.JSONValue) (policyir.JSONValue, error) {
	switch typed := value.(type) {
	case golem.JSONStringValue:
		return policyir.JSONStringValue(typed.Value())
	case golem.JSONBoolValue:
		return policyir.JSONBoolValue(typed.Value()), nil
	case golem.JSONNumberValue:
		negative, coefficient, exponent, ok := typed.Parts()
		if !ok {
			return policyir.JSONValue{}, fmt.Errorf("invalid JSON number")
		}
		number, err := policyir.NewJSONNumber(negative, []byte(coefficient), exponent)
		if err != nil {
			return policyir.JSONValue{}, err
		}
		return policyir.JSONNumberValueOf(number)
	case golem.JSONArrayValue:
		values := typed.Values()
		result := make([]policyir.JSONValue, len(values))
		for index, item := range values {
			converted, err := policyJSON(item)
			if err != nil {
				return policyir.JSONValue{}, err
			}
			result[index] = converted
		}
		return policyir.JSONArrayValue(result)
	case golem.JSONObjectValue:
		values := typed.Values()
		members := make([]policyir.JSONMember, 0, len(values))
		for key, item := range values {
			converted, err := policyJSON(item)
			if err != nil {
				return policyir.JSONValue{}, err
			}
			member, err := policyir.NewJSONMember(key, converted)
			if err != nil {
				return policyir.JSONValue{}, err
			}
			members = append(members, member)
		}
		return policyir.JSONObjectValue(members)
	default:
		return policyir.JSONNullValue(), nil
	}
}
