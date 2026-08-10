// Package decode owns immutable, closed persisted-row images for P4
// mutations. Provider-specific scanning remains in the P3 read decoder; this
// package consumes its exact policy values and validates them against the one
// active schema registry.
package decode

import (
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	readdecode "github.com/eleven-am/golem/go/internal/read/decode"
)

// Error is a fail-closed persisted-image validation failure.
type Error struct {
	Model  policyir.ModelID
	Field  policyir.FieldID
	Detail string
	Cause  error
}

func (failure *Error) Error() string {
	return fmt.Sprintf("P4_MUTATION_DECODE: model=%x field=%x: %s", failure.Model, failure.Field, failure.Detail)
}

func (failure *Error) Unwrap() error { return failure.Cause }

func fail(model policyir.ModelID, field policyir.FieldID, detail string, cause error) error {
	return &Error{Model: model, Field: field, Detail: detail, Cause: cause}
}

// Cell is one exact persisted scalar. A null cell has no policy value.
type Cell struct {
	field policyir.FieldID
	value policyir.Value
	null  bool
}

func Value(field policyir.FieldID, value policyir.Value) Cell {
	return Cell{field: field, value: value}
}

func Null(field policyir.FieldID) Cell { return Cell{field: field, null: true} }

func (cell Cell) FieldID() policyir.FieldID { return cell.field }
func (cell Cell) IsNull() bool              { return cell.null }
func (cell Cell) PolicyValue() (policyir.Value, bool) {
	return cell.value, !cell.null && cell.value.Kind() != 0
}

// Row is a closed persisted scalar image sorted by stable field identity. It
// may be partial: the owning ImageRequirements define which fields an
// operation must project. Relation handles never belong to a row image.
type Row struct {
	model policyir.ModelID
	cells []Cell
}

func (row Row) ModelID() policyir.ModelID { return row.model }
func (row Row) Cells() []Cell             { return append([]Cell(nil), row.cells...) }

func (row Row) Cell(field policyir.FieldID) (Cell, bool) {
	index := sort.Search(len(row.cells), func(index int) bool {
		return string(row.cells[index].field[:]) >= string(field[:])
	})
	if index == len(row.cells) || row.cells[index].field != field {
		return Cell{}, false
	}
	return row.cells[index], true
}

// NewRow validates a closed provider-decoded persisted image. Values are
// normalized only at the declared temporal precision; every other logical
// value stays exact.
func NewRow(registry *schema.Registry, modelID policyir.ModelID, cells []Cell) (Row, error) {
	if registry == nil {
		return Row{}, fail(modelID, policyir.FieldID{}, "active schema registry is required", nil)
	}
	model, ok := registry.Model(golem.ModelID(modelID))
	if modelID == (policyir.ModelID{}) || !ok {
		return Row{}, fail(modelID, policyir.FieldID{}, "model is absent from the active schema", nil)
	}

	want := make(map[policyir.FieldID]schema.Field)
	for _, publicID := range model.Fields() {
		field, found := registry.Field(golem.ModelID(modelID), publicID)
		if !found {
			return Row{}, fail(modelID, policyir.FieldID(publicID), "registry field disappeared", nil)
		}
		if field.Kind() != compilerir.FieldRelation {
			want[policyir.FieldID(publicID)] = field
		}
	}
	result := Row{model: modelID, cells: make([]Cell, len(cells))}
	seen := make(map[policyir.FieldID]struct{}, len(cells))
	for index, input := range cells {
		field, found := want[input.field]
		if input.field == (policyir.FieldID{}) || !found {
			return Row{}, fail(modelID, input.field, "field is zero, foreign, relational, or absent", nil)
		}
		if _, duplicate := seen[input.field]; duplicate {
			return Row{}, fail(modelID, input.field, "field appears more than once", nil)
		}
		seen[input.field] = struct{}{}
		if input.null {
			if !field.Nullable() || input.value.Kind() != 0 {
				return Row{}, fail(modelID, input.field, "invalid NULL persisted value", nil)
			}
			result.cells[index] = Null(input.field)
			continue
		}
		if input.value.Kind() == 0 {
			return Row{}, fail(modelID, input.field, "non-NULL cell has no value", nil)
		}
		normalized, err := normalizeAndValidate(input.value, field.LogicalType())
		if err != nil {
			return Row{}, fail(modelID, input.field, "value does not match the active logical field", err)
		}
		result.cells[index] = Value(input.field, normalized)
	}
	sort.Slice(result.cells, func(i, j int) bool {
		return string(result.cells[i].field[:]) < string(result.cells[j].field[:])
	})
	return result, nil
}

// NewCompleteRow is the opt-in diagnostic boundary for callers that truly
// require every persisted scalar field, such as bounded batch capture.
func NewCompleteRow(registry *schema.Registry, model policyir.ModelID, cells []Cell) (Row, error) {
	row, err := NewRow(registry, model, cells)
	if err != nil {
		return Row{}, err
	}
	complete, err := row.IsComplete(registry)
	if err != nil {
		return Row{}, err
	}
	if !complete {
		return Row{}, fail(model, policyir.FieldID{}, "persisted image is not complete", nil)
	}
	return row, nil
}

func (row Row) IsComplete(registry *schema.Registry) (bool, error) {
	if registry == nil {
		return false, fail(row.model, policyir.FieldID{}, "active schema registry is required", nil)
	}
	model, ok := registry.Model(golem.ModelID(row.model))
	if !ok {
		return false, fail(row.model, policyir.FieldID{}, "model is absent from the active schema", nil)
	}
	want := 0
	for _, fieldID := range model.Fields() {
		field, found := registry.Field(golem.ModelID(row.model), fieldID)
		if !found {
			return false, fail(row.model, policyir.FieldID(fieldID), "registry field disappeared", nil)
		}
		if field.Kind() != compilerir.FieldRelation {
			want++
		}
	}
	return len(row.cells) == want, nil
}

// RequireFields proves that an operation's explicit field inventory is present
// in this partial image. Extra fields are allowed because a single SQL image
// may satisfy several authorization/hook/result requirements.
func (row Row) RequireFields(fields []policyir.FieldID) error {
	seen := make(map[policyir.FieldID]struct{}, len(fields))
	for _, field := range fields {
		if field == (policyir.FieldID{}) {
			return fail(row.model, field, "required field is zero", nil)
		}
		if _, duplicate := seen[field]; duplicate {
			return fail(row.model, field, "required field appears more than once", nil)
		}
		seen[field] = struct{}{}
		if _, ok := row.Cell(field); !ok {
			return fail(row.model, field, "required field is absent from partial image", nil)
		}
	}
	return nil
}

// Select returns an exact partial image containing only the requested fields.
func (row Row) Select(registry *schema.Registry, fields []policyir.FieldID) (Row, error) {
	if err := row.RequireFields(fields); err != nil {
		return Row{}, err
	}
	cells := make([]Cell, len(fields))
	for index, field := range fields {
		cells[index], _ = row.Cell(field)
	}
	return NewRow(registry, row.model, cells)
}

// FromReadCells is the intended runtime seam. SQL RETURNING or a locked row is
// projected through the ordinary P3 decoder, then frozen here as a complete P4
// image. Completeness is defined by the caller's ImageRequirements. No
// provider return type is interpreted in this package.
func FromReadCells(registry *schema.Registry, model policyir.ModelID, cells []readdecode.Cell) (Row, error) {
	values := make([]Cell, len(cells))
	for index, cell := range cells {
		field := cell.FieldID()
		if cell.IsNull() {
			values[index] = Null(field)
			continue
		}
		value, ok := cell.PolicyValue()
		if !ok {
			return Row{}, fail(model, field, "P3 decoded cell has no exact policy value", nil)
		}
		values[index] = Value(field, value)
	}
	return NewRow(registry, model, values)
}

func normalizeAndValidate(value policyir.Value, typ compilerir.LogicalTypeIR) (policyir.Value, error) {
	if err := value.Validate(); err != nil {
		return policyir.Value{}, err
	}
	want := valueKind(typ.Kind)
	if want == 0 || value.Kind() != want {
		return policyir.Value{}, fmt.Errorf("value kind %d does not match logical kind %q", value.Kind(), typ.Kind)
	}
	switch typ.Kind {
	case compilerir.TypeDecimal:
		coefficient, scale, _ := value.Decimal()
		if typ.Precision == nil || typ.Scale == nil || uint16(scale) > *typ.Scale || decimalDigits(coefficient) > int(*typ.Precision) {
			return policyir.Value{}, fmt.Errorf("decimal exceeds declared precision or scale")
		}
	case compilerir.TypeString:
		text, _ := value.Text()
		if typ.MaxLength != nil && uint32(utf8.RuneCountInString(text)) > *typ.MaxLength {
			return policyir.Value{}, fmt.Errorf("string exceeds maximum length")
		}
	case compilerir.TypeBytes:
		data, _ := value.Bytes()
		if typ.MaxLength != nil && uint32(len(data)) > *typ.MaxLength {
			return policyir.Value{}, fmt.Errorf("bytes exceed maximum length")
		}
	case compilerir.TypeEnum:
		if typ.EnumID == nil {
			return policyir.Value{}, fmt.Errorf("enum identity is absent")
		}
		actual, _, _ := value.Enum()
		expected, err := fixedID(string(*typ.EnumID))
		if err != nil || actual != policyir.EnumID(expected) {
			return policyir.Value{}, fmt.Errorf("enum identity mismatch")
		}
	case compilerir.TypeTime:
		microseconds, _ := value.Time()
		quantum := temporalQuantum(precision(typ))
		return policyir.NewTimeValue(microseconds / quantum * quantum)
	case compilerir.TypeDateTime:
		seconds, nanos, _ := value.DateTime()
		quantum := uint32(temporalQuantum(precision(typ)) * 1_000)
		return policyir.NewDateTimeValue(seconds, nanos/quantum*quantum)
	case compilerir.TypeScalarList:
		if typ.Element == nil {
			return policyir.Value{}, fmt.Errorf("scalar list element type is absent")
		}
		items, _ := value.List()
		normalized := make([]policyir.Value, len(items))
		for index, item := range items {
			var err error
			normalized[index], err = normalizeAndValidate(item, *typ.Element)
			if err != nil {
				return policyir.Value{}, fmt.Errorf("list element %d: %w", index, err)
			}
		}
		return policyir.NewListValue(normalized)
	}
	return value, nil
}

func valueKind(kind compilerir.LogicalTypeKind) policyir.ValueKind {
	switch kind {
	case compilerir.TypeBool:
		return policyir.ValueBool
	case compilerir.TypeInt16:
		return policyir.ValueInt16
	case compilerir.TypeInt32:
		return policyir.ValueInt32
	case compilerir.TypeInt64:
		return policyir.ValueInt64
	case compilerir.TypeFloat32:
		return policyir.ValueFloat32
	case compilerir.TypeFloat64:
		return policyir.ValueFloat64
	case compilerir.TypeDecimal:
		return policyir.ValueDecimal
	case compilerir.TypeString:
		return policyir.ValueString
	case compilerir.TypeBytes:
		return policyir.ValueBytes
	case compilerir.TypeUUID:
		return policyir.ValueUUID
	case compilerir.TypeDate:
		return policyir.ValueDate
	case compilerir.TypeTime:
		return policyir.ValueTime
	case compilerir.TypeDateTime:
		return policyir.ValueDateTime
	case compilerir.TypeEnum:
		return policyir.ValueEnum
	case compilerir.TypeJSON:
		return policyir.ValueJSON
	case compilerir.TypeScalarList:
		return policyir.ValueScalarList
	default:
		return 0
	}
}

func precision(typ compilerir.LogicalTypeIR) uint16 {
	if typ.Precision == nil {
		return 0
	}
	return *typ.Precision
}

func temporalQuantum(precision uint16) int64 {
	quantum := int64(1)
	for current := precision; current < 6; current++ {
		quantum *= 10
	}
	return quantum
}

func decimalDigits(value int64) int {
	magnitude := uint64(value)
	if value < 0 {
		magnitude = uint64(-(value + 1)) + 1
	}
	if magnitude == 0 {
		return 1
	}
	result := 0
	for magnitude != 0 {
		result++
		magnitude /= 10
	}
	return result
}

func fixedID(value string) ([16]byte, error) {
	var result [16]byte
	if len(value) != 32 {
		return result, fmt.Errorf("fixed identity has invalid length")
	}
	for index := 0; index < 16; index++ {
		high, low := hexNibble(value[index*2]), hexNibble(value[index*2+1])
		if high < 0 || low < 0 {
			return result, fmt.Errorf("fixed identity is not canonical lowercase hex")
		}
		result[index] = byte(high<<4 | low)
	}
	return result, nil
}

func hexNibble(value byte) int {
	if value >= '0' && value <= '9' {
		return int(value - '0')
	}
	if value >= 'a' && value <= 'f' {
		return int(value-'a') + 10
	}
	return -1
}
