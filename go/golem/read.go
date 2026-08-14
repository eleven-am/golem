package golem

import (
	"fmt"
	"reflect"
	"strconv"
	"time"
)

// ReadState distinguishes an omitted field from a selected null and a selected
// value. ReadNull intentionally does not disclose whether null came from storage
// or authorization masking.
type ReadState uint8

const (
	ReadUnselected ReadState = iota
	ReadNull
	ReadPresent
)

// ReadValue is an immutable typed result cell.
type ReadValue[V any] struct {
	state ReadState
	value V
	clone func(V) V
}

func (value ReadValue[V]) State() ReadState { return value.state }
func (value ReadValue[V]) IsSelected() bool { return value.state != ReadUnselected }
func (value ReadValue[V]) IsNull() bool     { return value.state == ReadNull }
func (value ReadValue[V]) IsPresent() bool  { return value.state == ReadPresent }
func (value ReadValue[V]) Get() (V, bool) {
	if value.state != ReadPresent {
		var zero V
		return zero, false
	}
	if value.clone != nil {
		return value.clone(value.value), true
	}
	return value.value, true
}

type readCell struct {
	state ReadState
	value any
	clone func(any) any
}

// RuntimeReadCell is the representation-opaque handoff from the P3 decoder to
// the public Row. Application code cannot inspect or mutate its payload.
type RuntimeReadCell struct {
	field FieldID
	cell  readCell
}

func (cell RuntimeReadCell) FieldID() FieldID { return cell.field }

// RuntimeModelRow is the representation-opaque row assembled by the runtime
// before a generated model type is known. It remains model-ID checked when a
// generated accessor turns it into Row[M].
type RuntimeModelRow struct {
	model       ModelID
	cells       map[FieldID]readCell
	counts      map[relationCountKey]readCell
	occurrences map[runtimeOccurrenceKey]readCell
	// cdcExact is set only by RuntimeCDCModelRow. Ordinary public/authorized
	// rows deliberately cannot be reinterpreted as exact database images:
	// ReadNull intentionally conflates stored NULL with authorization masking.
	cdcExact bool
}

func (row RuntimeModelRow) ModelID() ModelID { return row.model }

// RuntimeTransportValue is the representation-opaque GraphQL/code-generation
// view of one already authorized public cell. It preserves unselected versus
// masked/null versus present and clones mutable values on every read.
type RuntimeTransportValue struct{ cell readCell }

func (value RuntimeTransportValue) State() ReadState { return value.cell.state }
func (value RuntimeTransportValue) Get() (any, bool) {
	if value.cell.state != ReadPresent {
		return nil, false
	}
	raw := cloneReadCell(value.cell).value
	if list, ok := raw.(runtimeScalarList); ok {
		return RuntimeScalarListValue{canonical: append([]byte(nil), list.raw...)}, true
	}
	return raw, true
}

type RuntimeScalarListValue struct{ canonical []byte }

func (value RuntimeScalarListValue) CanonicalJSON() []byte {
	return append([]byte(nil), value.canonical...)
}

func RuntimeTransportField(row RuntimeModelRow, field FieldID) RuntimeTransportValue {
	if field == (FieldID{}) {
		return RuntimeTransportValue{}
	}
	return RuntimeTransportValue{cell: cloneReadCell(row.cells[field])}
}

func RuntimeTransportOccurrence(row RuntimeModelRow, field FieldID, occurrence RuntimeOccurrenceID) RuntimeTransportValue {
	if field == (FieldID{}) || occurrence == 0 {
		return RuntimeTransportValue{}
	}
	return RuntimeTransportValue{cell: cloneReadCell(row.occurrences[runtimeOccurrenceKey{field: field, occurrence: uint32(occurrence)}])}
}

func RuntimeTransportRelationCount(row RuntimeModelRow, field FieldID, relation RelationID, occurrence RuntimeOccurrenceID) RuntimeTransportValue {
	if field == (FieldID{}) || relation == (RelationID{}) {
		return RuntimeTransportValue{}
	}
	return RuntimeTransportValue{cell: cloneReadCell(row.counts[relationCountKey{field: field, relation: relation, occurrence: uint32(occurrence)}])}
}

type relationCountKey struct {
	field      FieldID
	relation   RelationID
	occurrence uint32
}

type runtimeOccurrenceKey struct {
	field      FieldID
	occurrence uint32
}

// RuntimeOccurrenceID addresses one GraphQL response-path occurrence. Zero is
// reserved for the ordinary generated Go projection API.
type RuntimeOccurrenceID uint32

type RuntimeOccurrenceCell struct {
	field      FieldID
	occurrence RuntimeOccurrenceID
	cell       readCell
}

// RuntimeRelationCountCell is the representation-opaque handoff for one
// authorized to-many relation count. Counts use relation identities rather
// than field identities so selecting both the relation rows and its count does
// not make the two result cells collide.
type RuntimeRelationCountCell struct {
	field      FieldID
	relation   RelationID
	occurrence RuntimeOccurrenceID
	cell       readCell
}

func RuntimePresentRelationCountCell(field FieldID, relation RelationID, value int64) RuntimeRelationCountCell {
	return RuntimeRelationCountCell{field: field, relation: relation, cell: readCell{state: ReadPresent, value: value}}
}

func RuntimeNullRelationCountCell(field FieldID, relation RelationID) RuntimeRelationCountCell {
	return RuntimeRelationCountCell{field: field, relation: relation, cell: readCell{state: ReadNull}}
}

func RuntimePresentRelationCountOccurrenceCell(field FieldID, relation RelationID, occurrence RuntimeOccurrenceID, value int64) RuntimeRelationCountCell {
	return RuntimeRelationCountCell{field: field, relation: relation, occurrence: occurrence, cell: readCell{state: ReadPresent, value: value}}
}

func RuntimeNullRelationCountOccurrenceCell(field FieldID, relation RelationID, occurrence RuntimeOccurrenceID) RuntimeRelationCountCell {
	return RuntimeRelationCountCell{field: field, relation: relation, occurrence: occurrence, cell: readCell{state: ReadNull}}
}

func RuntimeNullReadCell(field FieldID) RuntimeReadCell {
	return RuntimeReadCell{field: field, cell: readCell{state: ReadNull}}
}

// RuntimePresentReadCell records one already decoded value. Mutable values must
// provide a copier; immutable scalar values may pass nil.
func RuntimePresentReadCell[V any](field FieldID, value V, clone func(V) V) RuntimeReadCell {
	cell := readCell{state: ReadPresent, value: value}
	if clone != nil {
		cell.clone = func(raw any) any { return clone(raw.(V)) }
	}
	return RuntimeReadCell{field: field, cell: cell}
}

type runtimeScalarList struct{ raw []byte }

// RuntimeScalarListReadCell retains an exact canonical JSON array until the
// generated typed accessor supplies its element type.
func RuntimeScalarListReadCell(field FieldID, canonical []byte) RuntimeReadCell {
	value := runtimeScalarList{raw: append([]byte(nil), canonical...)}
	return RuntimePresentReadCell(field, value, func(input runtimeScalarList) runtimeScalarList {
		return runtimeScalarList{raw: append([]byte(nil), input.raw...)}
	})
}

// Row is one immutable, model-typed projected result.
type Row[M any] struct {
	model       ModelID
	cells       map[FieldID]readCell
	counts      map[relationCountKey]readCell
	occurrences map[runtimeOccurrenceKey]readCell
	_           func() M
}

// RuntimeReadRow validates and owns decoded cells at the public runtime
// boundary. Duplicate and zero field identities are always rejected.
func RuntimeReadRow[M any](descriptor ModelDescriptor[M], cells ...RuntimeReadCell) (Row[M], error) {
	model := descriptor.Metadata().ModelID()
	runtime, err := RuntimeModelReadRow(model, cells...)
	if err != nil {
		return Row[M]{}, err
	}
	return RuntimeTypedReadRow(descriptor, runtime)
}

// RuntimeModelReadRow validates and owns decoded cells without requiring the
// application model's Go type. It is intended only for runtime composition.
func RuntimeModelReadRow(model ModelID, cells ...RuntimeReadCell) (RuntimeModelRow, error) {
	return RuntimeModelReadRowWithCounts(model, cells, nil)
}

// RuntimeModelReadRowWithCounts validates and owns decoded scalar/relation
// cells and authorized relation counts without requiring the generated model's
// Go type.
func RuntimeModelReadRowWithCounts(model ModelID, cells []RuntimeReadCell, counts []RuntimeRelationCountCell) (RuntimeModelRow, error) {
	return RuntimeModelReadRowWithOccurrences(model, cells, counts, nil)
}

// RuntimeModelReadRowWithOccurrences retains independently addressed aliased
// relation/count results while ordinary typed projections keep occurrence zero.
func RuntimeModelReadRowWithOccurrences(model ModelID, cells []RuntimeReadCell, counts []RuntimeRelationCountCell, occurrences []RuntimeOccurrenceCell) (RuntimeModelRow, error) {
	if model == (ModelID{}) {
		return RuntimeModelRow{}, fmt.Errorf("read row: model has a zero identity")
	}
	result := RuntimeModelRow{model: model, cells: make(map[FieldID]readCell, len(cells)), counts: make(map[relationCountKey]readCell, len(counts)), occurrences: make(map[runtimeOccurrenceKey]readCell, len(occurrences))}
	for index, value := range cells {
		if value.field == (FieldID{}) {
			return RuntimeModelRow{}, fmt.Errorf("read row: cell %d has a zero field identity", index)
		}
		if value.cell.state != ReadNull && value.cell.state != ReadPresent {
			return RuntimeModelRow{}, fmt.Errorf("read row: cell %d has invalid state %d", index, value.cell.state)
		}
		if _, duplicate := result.cells[value.field]; duplicate {
			return RuntimeModelRow{}, fmt.Errorf("read row: duplicate field identity %x", value.field)
		}
		result.cells[value.field] = cloneReadCell(value.cell)
	}
	for index, value := range counts {
		if value.field == (FieldID{}) || value.relation == (RelationID{}) {
			return RuntimeModelRow{}, fmt.Errorf("read row: count %d has a zero field or relation identity", index)
		}
		if value.cell.state != ReadPresent && value.cell.state != ReadNull {
			return RuntimeModelRow{}, fmt.Errorf("read row: count %d has invalid state %d", index, value.cell.state)
		}
		if value.cell.state == ReadPresent {
			if _, ok := value.cell.value.(int64); !ok {
				return RuntimeModelRow{}, fmt.Errorf("read row: count %d is not int64", index)
			}
		} else if value.cell.value != nil {
			return RuntimeModelRow{}, fmt.Errorf("read row: count %d is not int64", index)
		}
		key := relationCountKey{field: value.field, relation: value.relation, occurrence: uint32(value.occurrence)}
		if _, duplicate := result.counts[key]; duplicate {
			return RuntimeModelRow{}, fmt.Errorf("read row: duplicate relation count identity %x/%x", value.field, value.relation)
		}
		result.counts[key] = cloneReadCell(value.cell)
	}
	for index, value := range occurrences {
		if value.field == (FieldID{}) || value.occurrence == 0 {
			return RuntimeModelRow{}, fmt.Errorf("read row: occurrence %d has a zero field or occurrence identity", index)
		}
		if value.cell.state != ReadPresent && value.cell.state != ReadNull {
			return RuntimeModelRow{}, fmt.Errorf("read row: occurrence %d has invalid state %d", index, value.cell.state)
		}
		key := runtimeOccurrenceKey{field: value.field, occurrence: uint32(value.occurrence)}
		if _, duplicate := result.occurrences[key]; duplicate {
			return RuntimeModelRow{}, fmt.Errorf("read row: duplicate occurrence identity %x/%d", value.field, value.occurrence)
		}
		result.occurrences[key] = cloneReadCell(value.cell)
	}
	return result, nil
}

// RuntimeTypedReadRow performs the final generated-descriptor model check.
func RuntimeTypedReadRow[M any](descriptor ModelDescriptor[M], runtime RuntimeModelRow) (Row[M], error) {
	model := descriptor.Metadata().ModelID()
	if model == (ModelID{}) || runtime.model != model {
		return Row[M]{}, fmt.Errorf("read row: runtime model does not match descriptor")
	}
	return Row[M]{model: model, cells: cloneReadCells(runtime.cells), counts: cloneReadCounts(runtime.counts), occurrences: cloneOccurrences(runtime.occurrences)}, nil
}

// RuntimeToOneReadCell and RuntimeToManyReadCell attach already hydrated
// relation rows while retaining their target model identity.
func RuntimeToOneReadCell(field FieldID, value RuntimeModelRow) RuntimeReadCell {
	return RuntimePresentReadCell(field, cloneRuntimeModelRow(value), cloneRuntimeModelRow)
}

func RuntimeToManyReadCell(field FieldID, values []RuntimeModelRow) RuntimeReadCell {
	return RuntimePresentReadCell(field, cloneRuntimeModelRows(values), cloneRuntimeModelRows)
}

func RuntimeNullOccurrenceCell(field FieldID, occurrence RuntimeOccurrenceID) RuntimeOccurrenceCell {
	return RuntimeOccurrenceCell{field: field, occurrence: occurrence, cell: readCell{state: ReadNull}}
}

func RuntimeToOneOccurrenceCell(field FieldID, occurrence RuntimeOccurrenceID, value RuntimeModelRow) RuntimeOccurrenceCell {
	return RuntimeOccurrenceCell{field: field, occurrence: occurrence, cell: readCell{state: ReadPresent, value: cloneRuntimeModelRow(value), clone: func(raw any) any { return cloneRuntimeModelRow(raw.(RuntimeModelRow)) }}}
}

func RuntimeToManyOccurrenceCell(field FieldID, occurrence RuntimeOccurrenceID, values []RuntimeModelRow) RuntimeOccurrenceCell {
	return RuntimeOccurrenceCell{field: field, occurrence: occurrence, cell: readCell{state: ReadPresent, value: cloneRuntimeModelRows(values), clone: func(raw any) any { return cloneRuntimeModelRows(raw.([]RuntimeModelRow)) }}}
}

func cloneReadCell(cell readCell) readCell {
	if cell.state == ReadPresent && cell.clone != nil {
		cell.value = cell.clone(cell.value)
	}
	return cell
}

func cloneReadCells(cells map[FieldID]readCell) map[FieldID]readCell {
	result := make(map[FieldID]readCell, len(cells))
	for field, cell := range cells {
		result[field] = cloneReadCell(cell)
	}
	return result
}

func cloneReadCounts(counts map[relationCountKey]readCell) map[relationCountKey]readCell {
	result := make(map[relationCountKey]readCell, len(counts))
	for key, cell := range counts {
		result[key] = cloneReadCell(cell)
	}
	return result
}

func cloneOccurrences(values map[runtimeOccurrenceKey]readCell) map[runtimeOccurrenceKey]readCell {
	result := make(map[runtimeOccurrenceKey]readCell, len(values))
	for key, cell := range values {
		result[key] = cloneReadCell(cell)
	}
	return result
}

func cloneRuntimeModelRow(row RuntimeModelRow) RuntimeModelRow {
	return RuntimeModelRow{model: row.model, cells: cloneReadCells(row.cells), counts: cloneReadCounts(row.counts), occurrences: cloneOccurrences(row.occurrences), cdcExact: row.cdcExact}
}

func cloneRuntimeModelRows(rows []RuntimeModelRow) []RuntimeModelRow {
	result := make([]RuntimeModelRow, len(rows))
	for index, row := range rows {
		result[index] = cloneRuntimeModelRow(row)
	}
	return result
}

func cloneRow[M any](row Row[M]) Row[M] {
	result := Row[M]{model: row.model, cells: make(map[FieldID]readCell, len(row.cells)), counts: cloneReadCounts(row.counts), occurrences: cloneOccurrences(row.occurrences)}
	for field, cell := range row.cells {
		result.cells[field] = cloneReadCell(cell)
	}
	return result
}

// Value reads one generated scalar column without exposing a string-keyed row.
func Value[M, V any](row Row[M], field ScalarColumn[M, V]) ReadValue[V] {
	if field == nil {
		return ReadValue[V]{}
	}
	cell, ok := row.cells[field.fieldIdentity()]
	if !ok {
		return ReadValue[V]{}
	}
	if cell.state == ReadNull {
		return ReadValue[V]{state: ReadNull}
	}
	typed, ok := cell.value.(V)
	coerced := false
	if !ok {
		typed, ok = coerceReadValue[V](cell.value)
		if !ok {
			return ReadValue[V]{}
		}
		coerced = true
	}
	result := ReadValue[V]{state: ReadPresent, value: typed}
	if coerced {
		result.clone = cloneCoercedReadValue[V]
	} else if cell.clone != nil {
		result.clone = func(value V) V { return cell.clone(value).(V) }
	}
	return result
}

func cloneCoercedReadValue[V any](value V) V {
	reflected := reflect.ValueOf(value)
	if reflected.IsValid() && reflected.Kind() == reflect.Slice {
		copy := reflect.MakeSlice(reflected.Type(), reflected.Len(), reflected.Len())
		reflect.Copy(copy, reflected)
		return copy.Interface().(V)
	}
	return value
}

func coerceReadValue[V any](raw any) (V, bool) {
	var zero V
	if list, ok := raw.(runtimeScalarList); ok {
		value, err := ParseJSON(list.raw)
		if err != nil {
			return zero, false
		}
		array, ok := value.(JSONArrayValue)
		if !ok {
			return zero, false
		}
		target := reflect.TypeOf((*V)(nil)).Elem()
		if target.Kind() != reflect.Slice {
			return zero, false
		}
		result := reflect.MakeSlice(target, len(array.Values()), len(array.Values()))
		for index, element := range array.Values() {
			converted, valid := coerceJSONScalar(element, target.Elem())
			if !valid {
				return zero, false
			}
			result.Index(index).Set(converted)
		}
		return result.Interface().(V), true
	}
	value := reflect.ValueOf(raw)
	target := reflect.TypeOf((*V)(nil)).Elem()
	if !value.IsValid() || !value.Type().ConvertibleTo(target) {
		return zero, false
	}
	return value.Convert(target).Interface().(V), true
}

func coerceJSONScalar(value JSONValue, target reflect.Type) (reflect.Value, bool) {
	var raw any
	switch typed := value.(type) {
	case JSONStringValue:
		text := typed.Value()
		switch target {
		case reflect.TypeOf(UUID{}):
			value, err := ParseUUID(text)
			if err != nil {
				return reflect.Value{}, false
			}
			raw = value
		case reflect.TypeOf(Date{}):
			value, err := ParseDate(text)
			if err != nil {
				return reflect.Value{}, false
			}
			raw = value
		case reflect.TypeOf(Time{}):
			value, err := ParseTime(text)
			if err != nil {
				return reflect.Value{}, false
			}
			raw = value
		case reflect.TypeOf(time.Time{}):
			value, err := time.Parse(time.RFC3339Nano, text)
			if err != nil || value.Nanosecond()%1_000 != 0 {
				return reflect.Value{}, false
			}
			raw = value.UTC()
		default:
			raw = text
		}
	case JSONBoolValue:
		raw = typed.Value()
	case JSONNumberValue:
		switch target.Kind() {
		case reflect.Float32, reflect.Float64:
			bits := 64
			if target.Kind() == reflect.Float32 {
				bits = 32
			}
			floating, err := strconv.ParseFloat(typed.Canonical(), bits)
			if err != nil {
				return reflect.Value{}, false
			}
			if bits == 32 {
				raw = float32(floating)
			} else {
				raw = floating
			}
		default:
			decimal, err := decimalFromJSONNumber(typed)
			if err != nil {
				return reflect.Value{}, false
			}
			if target == reflect.TypeOf(Decimal{}) {
				raw = decimal
				break
			}
			coefficient, scale := decimal.Coefficient(), decimal.Scale()
			if scale != 0 {
				return reflect.Value{}, false
			}
			raw = coefficient
		}
	default:
		return reflect.Value{}, false
	}
	reflected := reflect.ValueOf(raw)
	if !reflected.IsValid() || !reflected.Type().ConvertibleTo(target) {
		return reflect.Value{}, false
	}
	return reflected.Convert(target), true
}

func decimalFromJSONNumber(value JSONNumberValue) (Decimal, error) {
	negative, digits, exponent, ok := value.Parts()
	if !ok {
		return Decimal{}, fmt.Errorf("invalid JSON number")
	}
	if exponent > 0 {
		if int64(len(digits))+int64(exponent) > 18 {
			return Decimal{}, fmt.Errorf("decimal exceeds portable precision 18")
		}
		digits += string(make([]byte, int(exponent)))
		bytes := []byte(digits)
		for index := len(bytes) - int(exponent); index < len(bytes); index++ {
			bytes[index] = '0'
		}
		digits = string(bytes)
		exponent = 0
	}
	if exponent < -18 {
		return Decimal{}, fmt.Errorf("decimal exceeds portable scale 18")
	}
	if negative {
		digits = "-" + digits
	}
	coefficient, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return Decimal{}, err
	}
	return NewDecimal(coefficient, uint8(-exponent))
}

func One[M, R any](row Row[M], field ToOne[M, R]) ReadValue[Row[R]] {
	cell, ok := row.cells[field.fieldIdentity()]
	if !ok {
		return ReadValue[Row[R]]{}
	}
	if cell.state == ReadNull {
		return ReadValue[Row[R]]{state: ReadNull}
	}
	value, ok := cell.value.(RuntimeModelRow)
	if ok {
		if value.model != field.targetModel || field.targetModel == (ModelID{}) {
			return ReadValue[Row[R]]{}
		}
		typed := Row[R]{model: value.model, cells: cloneReadCells(value.cells), counts: cloneReadCounts(value.counts), occurrences: cloneOccurrences(value.occurrences)}
		return ReadValue[Row[R]]{state: ReadPresent, value: typed, clone: cloneRow[R]}
	}
	// Retain compatibility with runtime cells constructed before relation rows
	// acquired their type-erased representation.
	typed, ok := cell.value.(Row[R])
	if !ok || typed.model != field.targetModel || field.targetModel == (ModelID{}) {
		return ReadValue[Row[R]]{}
	}
	return ReadValue[Row[R]]{state: ReadPresent, value: cloneRow(typed), clone: cloneRow[R]}
}

func Many[M, R any](row Row[M], field ToMany[M, R]) ReadValue[[]Row[R]] {
	cell, ok := row.cells[field.fieldIdentity()]
	if !ok {
		return ReadValue[[]Row[R]]{}
	}
	if cell.state == ReadNull {
		return ReadValue[[]Row[R]]{state: ReadNull}
	}
	value, ok := cell.value.([]RuntimeModelRow)
	var typed []Row[R]
	if ok {
		typed = make([]Row[R], len(value))
		for index, item := range value {
			if item.model != field.targetModel || field.targetModel == (ModelID{}) {
				return ReadValue[[]Row[R]]{}
			}
			typed[index] = Row[R]{model: item.model, cells: cloneReadCells(item.cells), counts: cloneReadCounts(item.counts), occurrences: cloneOccurrences(item.occurrences)}
		}
	} else {
		legacy, legacyOK := cell.value.([]Row[R])
		if !legacyOK || field.targetModel == (ModelID{}) {
			return ReadValue[[]Row[R]]{}
		}
		typed = legacy
		for _, item := range typed {
			if item.model != field.targetModel {
				return ReadValue[[]Row[R]]{}
			}
		}
	}
	clone := func(rows []Row[R]) []Row[R] {
		result := make([]Row[R], len(rows))
		for index, item := range rows {
			result[index] = cloneRow(item)
		}
		return result
	}
	return ReadValue[[]Row[R]]{state: ReadPresent, value: clone(typed), clone: clone}
}

// RelationCount reads one explicitly selected, policy-scoped to-many relation
// count. An unselected count remains distinguishable from a selected zero.
func RelationCount[M, R any](row Row[M], field ToMany[M, R]) ReadValue[int64] {
	if field.relationID == (RelationID{}) || field.targetModel == (ModelID{}) {
		return ReadValue[int64]{}
	}
	cell, ok := row.counts[relationCountKey{field: field.fieldID, relation: field.relationID}]
	if !ok {
		return ReadValue[int64]{}
	}
	if cell.state == ReadNull {
		return ReadValue[int64]{state: ReadNull}
	}
	if cell.state != ReadPresent {
		return ReadValue[int64]{}
	}
	value, ok := cell.value.(int64)
	if !ok {
		return ReadValue[int64]{}
	}
	return ReadValue[int64]{state: ReadPresent, value: value}
}

func RuntimeOccurrenceToOne[M, R any](row Row[M], field ToOne[M, R], occurrence RuntimeOccurrenceID) ReadValue[Row[R]] {
	cell, ok := row.occurrences[runtimeOccurrenceKey{field: field.fieldID, occurrence: uint32(occurrence)}]
	if !ok {
		return ReadValue[Row[R]]{}
	}
	if cell.state == ReadNull {
		return ReadValue[Row[R]]{state: ReadNull}
	}
	value, ok := cell.value.(RuntimeModelRow)
	if !ok || value.model != field.targetModel || field.targetModel == (ModelID{}) {
		return ReadValue[Row[R]]{}
	}
	typed := Row[R]{model: value.model, cells: cloneReadCells(value.cells), counts: cloneReadCounts(value.counts), occurrences: cloneOccurrences(value.occurrences)}
	return ReadValue[Row[R]]{state: ReadPresent, value: typed, clone: cloneRow[R]}
}

func RuntimeOccurrenceToMany[M, R any](row Row[M], field ToMany[M, R], occurrence RuntimeOccurrenceID) ReadValue[[]Row[R]] {
	cell, ok := row.occurrences[runtimeOccurrenceKey{field: field.fieldID, occurrence: uint32(occurrence)}]
	if !ok {
		return ReadValue[[]Row[R]]{}
	}
	if cell.state == ReadNull {
		return ReadValue[[]Row[R]]{state: ReadNull}
	}
	values, ok := cell.value.([]RuntimeModelRow)
	if !ok {
		return ReadValue[[]Row[R]]{}
	}
	typed := make([]Row[R], len(values))
	for index, value := range values {
		if value.model != field.targetModel || field.targetModel == (ModelID{}) {
			return ReadValue[[]Row[R]]{}
		}
		typed[index] = Row[R]{model: value.model, cells: cloneReadCells(value.cells), counts: cloneReadCounts(value.counts), occurrences: cloneOccurrences(value.occurrences)}
	}
	return ReadValue[[]Row[R]]{state: ReadPresent, value: cloneRows(typed), clone: cloneRows[R]}
}

func RuntimeOccurrenceRelationCount[M, R any](row Row[M], field ToMany[M, R], occurrence RuntimeOccurrenceID) ReadValue[int64] {
	if field.relationID == (RelationID{}) || field.targetModel == (ModelID{}) {
		return ReadValue[int64]{}
	}
	cell, ok := row.counts[relationCountKey{field: field.fieldID, relation: field.relationID, occurrence: uint32(occurrence)}]
	if !ok {
		return ReadValue[int64]{}
	}
	if cell.state == ReadNull {
		return ReadValue[int64]{state: ReadNull}
	}
	value, ok := cell.value.(int64)
	if !ok {
		return ReadValue[int64]{}
	}
	return ReadValue[int64]{state: ReadPresent, value: value}
}

type readSelectionKind uint8

const (
	readSelectionScalar readSelectionKind = iota + 1
	readSelectionRelation
	readSelectionRelationCount
)

type readSelectionNode struct {
	kind       readSelectionKind
	field      FieldID
	relation   RelationID
	target     ModelID
	occurrence RuntimeOccurrenceID
	options    []readOptionNode
}

// Selection is sealed to generated scalar handles and validated generated
// relation selections.
type Selection[M any] interface {
	readSelection(M) readSelectionNode
}

// RelationInclusion is sealed to generated relation selections. Include starts
// from the target model's default visible scalar projection and adds only the
// explicitly named relations.
type RelationInclusion[M any] interface {
	readRelationInclusion(M) readSelectionNode
}

func (field ToOne[M, R]) readRelationInclusion(M) readSelectionNode {
	return readSelectionNode{kind: readSelectionRelation, field: field.fieldID, relation: field.relationID, target: field.targetModel}
}

func (field ToMany[M, R]) readRelationInclusion(M) readSelectionNode {
	return readSelectionNode{kind: readSelectionRelation, field: field.fieldID, relation: field.relationID, target: field.targetModel}
}

type RelationSelection[M, R any] struct {
	node readSelectionNode
	_    func(M) R
}

// RelationCountSelection is a typed projection of one to-many relation count.
// Its child accepts only Where; the regular count request validator rejects all
// ordering, paging, distinct, cursor, and projection options.
type RelationCountSelection[M, R any] struct {
	node readSelectionNode
	_    func(M) R
}

func (selection RelationCountSelection[M, R]) readSelection(M) readSelectionNode {
	return cloneReadSelection(selection.node)
}

func (selection RelationCountSelection[M, R]) readRelationInclusion(M) readSelectionNode {
	return cloneReadSelection(selection.node)
}

func (field ToMany[M, R]) Count(options ...ReadOption[R]) RelationCountSelection[M, R] {
	nodes := make([]readOptionNode, len(options))
	var witness R
	for index, option := range options {
		if option != nil {
			nodes[index] = option.readOption(witness)
		}
	}
	return RelationCountSelection[M, R]{node: readSelectionNode{
		kind: readSelectionRelationCount, field: field.fieldID, relation: field.relationID, target: field.targetModel, options: nodes,
	}}
}

func (selection RelationSelection[M, R]) readSelection(M) readSelectionNode {
	return cloneReadSelection(selection.node)
}

func (selection RelationSelection[M, R]) readRelationInclusion(M) readSelectionNode {
	return cloneReadSelection(selection.node)
}

func (field ToOne[M, R]) Select(fields ...Selection[R]) RelationSelection[M, R] {
	return RelationSelection[M, R]{node: readSelectionNode{
		kind: readSelectionRelation, field: field.fieldID, relation: field.relationID, target: field.targetModel,
		options: []readOptionNode{projectionOption(fields)},
	}}
}

func (field ToOne[M, R]) Include(relations ...RelationInclusion[R]) RelationSelection[M, R] {
	return RelationSelection[M, R]{node: readSelectionNode{
		kind: readSelectionRelation, field: field.fieldID, relation: field.relationID, target: field.targetModel,
		options: []readOptionNode{includeOption(relations)},
	}}
}

func (field ToOne[M, R]) Omit(fields ...Column[R]) RelationSelection[M, R] {
	return RelationSelection[M, R]{node: readSelectionNode{
		kind: readSelectionRelation, field: field.fieldID, relation: field.relationID, target: field.targetModel,
		options: []readOptionNode{omitOption(fields)},
	}}
}

func (field ToMany[M, R]) Select(fields ...Selection[R]) RelationSelection[M, R] {
	return field.Args(Select(fields...))
}

func (field ToMany[M, R]) Args(options ...ReadOption[R]) RelationSelection[M, R] {
	nodes := make([]readOptionNode, len(options))
	var witness R
	for index, option := range options {
		if option != nil {
			nodes[index] = option.readOption(witness)
		}
	}
	return RelationSelection[M, R]{node: readSelectionNode{
		kind: readSelectionRelation, field: field.fieldID, relation: field.relationID, target: field.targetModel, options: nodes,
	}}
}

func cloneReadSelection(value readSelectionNode) readSelectionNode {
	value.options = cloneReadOptionNodes(value.options)
	return value
}

type readOptionKind uint8

const (
	readOptionWhere readOptionKind = iota + 1
	readOptionOrderBy
	readOptionTake
	readOptionSkip
	readOptionDistinct
	readOptionSelect
	readOptionCursor
	readOptionInclude
	readOptionOmit
)

type readOptionNode struct {
	kind            readOptionKind
	freezePredicate func(ModelID) (FrozenPredicate, error)
	orders          []readOrderNode
	integer         int
	fields          []FieldID
	selection       []readSelectionNode
	selectorModel   ModelID
	selectorKey     KeyID
	selectorValues  []selectorComponent
}

type selectorComponent struct {
	field   FieldID
	operand frozenOperand
}

// UniqueSelectorValue is an opaque generated, typed identity value.
type UniqueSelectorValue[M any] struct {
	model      ModelID
	key        KeyID
	components []selectorComponent
	_          func() M
}

// GeneratedSelectorComponent is used by generated selector Value methods. The
// generated method fixes V to the declared field type; runtime freezing still
// rejects unsupported or malformed values.
func GeneratedSelectorComponent[V any](field FieldID, value V) selectorComponent {
	var operand frozenOperand
	if bytes, ok := any(value).([]byte); ok {
		operand = bytesOperand(bytes)
	} else {
		operand = frozenOperand{kind: FrozenOperandOne, one: scalarValueAny(value)}
	}
	return selectorComponent{field: field, operand: operand}
}

func GeneratedNullableSelectorComponent[V any](field FieldID, value Null[V]) selectorComponent {
	if !value.Valid {
		return selectorComponent{field: field, operand: noOperand()}
	}
	return GeneratedSelectorComponent(field, value.Value)
}

func GeneratedUniqueSelectorValue[M any](model ModelID, key KeyID, components ...selectorComponent) UniqueSelectorValue[M] {
	result := UniqueSelectorValue[M]{model: model, key: key, components: make([]selectorComponent, len(components))}
	for index, component := range components {
		result.components[index] = selectorComponent{field: component.field, operand: cloneFrozenOperand(component.operand)}
	}
	return result
}

type ReadOption[M any] interface {
	readOption(M) readOptionNode
}

// Projection is the projection-only subset of ReadOption accepted by
// single-row mutation results. It is additive to the P3 read ABI: every
// Projection is still a ReadOption, while filters, ordering, pagination,
// cursors, and distinct values cannot satisfy this sealed interface.
type Projection[M any] interface {
	ReadOption[M]
	mutationProjection(M) readOptionNode
}

type readOptionValue[M any] struct {
	node readOptionNode
	_    func() M
}

func (option readOptionValue[M]) readOption(M) readOptionNode { return cloneReadOption(option.node) }

type projectionValue[M any] struct {
	node readOptionNode
	_    func() M
}

func (option projectionValue[M]) readOption(M) readOptionNode {
	return cloneReadOption(option.node)
}

func (option projectionValue[M]) mutationProjection(M) readOptionNode {
	return cloneReadOption(option.node)
}

func Where[M any](predicate Predicate[M]) ReadOption[M] {
	return readOptionValue[M]{node: readOptionNode{kind: readOptionWhere, freezePredicate: predicate.freezeForModel}}
}

type SortDirection uint8

const (
	SortAscending SortDirection = iota + 1
	SortDescending
)

type readOrderNode struct {
	field     FieldID
	direction SortDirection
}

type OrderTerm[M any] struct {
	node readOrderNode
	_    func() M
}

func orderTerm[M any](field FieldID, direction SortDirection) OrderTerm[M] {
	return OrderTerm[M]{node: readOrderNode{field: field, direction: direction}}
}

func OrderBy[M any](terms ...OrderTerm[M]) ReadOption[M] {
	orders := make([]readOrderNode, len(terms))
	for index, term := range terms {
		orders[index] = term.node
	}
	return readOptionValue[M]{node: readOptionNode{kind: readOptionOrderBy, orders: orders}}
}

func Take[M any](value int) ReadOption[M] {
	return readOptionValue[M]{node: readOptionNode{kind: readOptionTake, integer: value}}
}

func Skip[M any](value int) ReadOption[M] {
	return readOptionValue[M]{node: readOptionNode{kind: readOptionSkip, integer: value}}
}

func Distinct[M any](fields ...Column[M]) ReadOption[M] {
	identities := make([]FieldID, len(fields))
	for index, field := range fields {
		if field != nil {
			identities[index] = field.fieldIdentity()
		}
	}
	return readOptionValue[M]{node: readOptionNode{kind: readOptionDistinct, fields: identities}}
}

// Cursor positions a findFirst or findMany request at one generated unique
// selector. The active schema binder verifies the selector identity again.
func Cursor[M any](selector UniqueSelectorValue[M]) ReadOption[M] {
	components := make([]selectorComponent, len(selector.components))
	for index, component := range selector.components {
		components[index] = selectorComponent{field: component.field, operand: cloneFrozenOperand(component.operand)}
	}
	return readOptionValue[M]{node: readOptionNode{kind: readOptionCursor, selectorModel: selector.model, selectorKey: selector.key, selectorValues: components}}
}

func Select[M any](fields ...Selection[M]) Projection[M] {
	return projectionValue[M]{node: projectionOption(fields)}
}

func Include[M any](relations ...RelationInclusion[M]) Projection[M] {
	return projectionValue[M]{node: includeOption(relations)}
}

func includeOption[M any](relations []RelationInclusion[M]) readOptionNode {
	selection := make([]readSelectionNode, len(relations))
	var witness M
	for index, relation := range relations {
		if relation != nil {
			selection[index] = relation.readRelationInclusion(witness)
		}
	}
	return readOptionNode{kind: readOptionInclude, selection: selection}
}

func Omit[M any](fields ...Column[M]) Projection[M] {
	return projectionValue[M]{node: omitOption(fields)}
}

// RuntimeProjectionReadOption is the narrow bridge used by the mutation
// runtime to reuse P3's projection binder and result planner.
func RuntimeProjectionReadOption[M any](projection Projection[M]) ReadOption[M] {
	if projection == nil {
		return nil
	}
	var witness M
	return readOptionValue[M]{node: projection.mutationProjection(witness)}
}

func omitOption[M any](fields []Column[M]) readOptionNode {
	identities := make([]FieldID, len(fields))
	for index, field := range fields {
		if field != nil {
			identities[index] = field.fieldIdentity()
		}
	}
	return readOptionNode{kind: readOptionOmit, fields: identities}
}

func projectionOption[M any](fields []Selection[M]) readOptionNode {
	selection := make([]readSelectionNode, len(fields))
	var witness M
	for index, field := range fields {
		if field != nil {
			selection[index] = field.readSelection(witness)
		}
	}
	return readOptionNode{kind: readOptionSelect, selection: selection}
}

func cloneReadOption(value readOptionNode) readOptionNode {
	value.orders = append([]readOrderNode(nil), value.orders...)
	value.fields = append([]FieldID(nil), value.fields...)
	components := value.selectorValues
	value.selectorValues = make([]selectorComponent, len(components))
	for index, component := range components {
		value.selectorValues[index] = selectorComponent{field: component.field, operand: cloneFrozenOperand(component.operand)}
	}
	selection := value.selection
	value.selection = make([]readSelectionNode, len(selection))
	for index, selection := range selection {
		value.selection[index] = cloneReadSelection(selection)
	}
	return value
}

func cloneReadOptionNodes(values []readOptionNode) []readOptionNode {
	result := make([]readOptionNode, len(values))
	for index, value := range values {
		result[index] = cloneReadOption(value)
	}
	return result
}

type ErrorCode string

const (
	CodeBadUserInput    ErrorCode = "BAD_USER_INPUT"
	CodeNotFound        ErrorCode = "NOT_FOUND"
	CodeUnauthenticated ErrorCode = "UNAUTHENTICATED"
	CodeForbidden       ErrorCode = "FORBIDDEN"
	CodeConflict        ErrorCode = "CONFLICT"
)

// Error is the transport-neutral public operation error. Cause is retained for
// trusted logs through Unwrap but is never part of Error's public text.
type Error struct {
	Code      ErrorCode
	Operation string
	Model     ModelID
	Field     FieldID
	Message   string
	cause     error
}

// RuntimeReadError is the narrow trusted-runtime constructor for a stable
// public read failure. The wrapped cause remains available to trusted logging
// through errors.Unwrap but is never included in Error().
func RuntimeReadError(code ErrorCode, operation string, model ModelID, field FieldID, message string, cause error) error {
	return RuntimeOperationError(code, operation, model, field, message, cause)
}

// RuntimeOperationError is the transport-neutral trusted-runtime constructor
// shared by reads and mutations. RuntimeReadError remains as the P3-compatible
// spelling.
func RuntimeOperationError(code ErrorCode, operation string, model ModelID, field FieldID, message string, cause error) error {
	if code != CodeBadUserInput && code != CodeNotFound && code != CodeUnauthenticated && code != CodeForbidden && code != CodeConflict {
		code = CodeBadUserInput
	}
	return &Error{Code: code, Operation: operation, Model: model, Field: field, Message: message, cause: cause}
}

func (failure *Error) Error() string {
	if failure == nil {
		return ""
	}
	return string(failure.Code) + ": " + failure.Message
}

func (failure *Error) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

type ReadOperation uint8

const (
	ReadFindUnique ReadOperation = iota + 1
	ReadFindFirst
	ReadFindMany
	ReadCount
)

type FrozenReadOrder struct {
	field     FieldID
	direction SortDirection
}

func (order FrozenReadOrder) FieldID() FieldID         { return order.field }
func (order FrozenReadOrder) Direction() SortDirection { return order.direction }

type FrozenReadSelection struct {
	kind       readSelectionKind
	field      FieldID
	relation   RelationID
	target     ModelID
	occurrence RuntimeOccurrenceID
	request    *FrozenReadRequest
}

type ReadProjectionMode uint8

const (
	ProjectionDefault ReadProjectionMode = iota + 1
	ProjectionSelect
	ProjectionInclude
)

func (selection FrozenReadSelection) FieldID() FieldID       { return selection.field }
func (selection FrozenReadSelection) RelationID() RelationID { return selection.relation }
func (selection FrozenReadSelection) TargetModelID() ModelID { return selection.target }
func (selection FrozenReadSelection) OccurrenceID() RuntimeOccurrenceID {
	return selection.occurrence
}
func (selection FrozenReadSelection) IsRelation() bool {
	return selection.kind == readSelectionRelation
}
func (selection FrozenReadSelection) IsRelationCount() bool {
	return selection.kind == readSelectionRelationCount
}
func (selection FrozenReadSelection) Request() (FrozenReadRequest, bool) {
	if selection.request == nil {
		return FrozenReadRequest{}, false
	}
	return selection.request.clone(), true
}

// FrozenReadRequest is a schema-agnostic immutable read request. The P3 binder
// validates every identity against the active fingerprinted schema.
type FrozenReadRequest struct {
	operation  ReadOperation
	model      ModelID
	where      *FrozenPredicate
	orders     []FrozenReadOrder
	take       *int
	skip       *int
	distinct   []FieldID
	selection  []FrozenReadSelection
	projection ReadProjectionMode
	omit       []FieldID
	selector   *FrozenUniqueSelector
	cursor     *FrozenReadCursor
}

type FrozenUniqueSelector struct {
	model  ModelID
	key    KeyID
	fields []FieldID
}

func (selector FrozenUniqueSelector) ModelID() ModelID { return selector.model }
func (selector FrozenUniqueSelector) KeyID() KeyID     { return selector.key }
func (selector FrozenUniqueSelector) Fields() []FieldID {
	return append([]FieldID(nil), selector.fields...)
}

type FrozenReadCursor struct {
	selector  FrozenUniqueSelector
	predicate FrozenPredicate
}

func (cursor FrozenReadCursor) Selector() FrozenUniqueSelector {
	value := cursor.selector
	value.fields = value.Fields()
	return value
}

func (cursor FrozenReadCursor) Predicate() FrozenPredicate { return cursor.predicate }

func (request FrozenReadRequest) Operation() ReadOperation { return request.operation }
func (request FrozenReadRequest) ModelID() ModelID         { return request.model }
func (request FrozenReadRequest) Where() (FrozenPredicate, bool) {
	if request.where == nil {
		return FrozenPredicate{}, false
	}
	return *request.where, true
}
func (request FrozenReadRequest) OrderBy() []FrozenReadOrder {
	return append([]FrozenReadOrder(nil), request.orders...)
}
func (request FrozenReadRequest) Take() (int, bool) {
	if request.take == nil {
		return 0, false
	}
	return *request.take, true
}
func (request FrozenReadRequest) Skip() (int, bool) {
	if request.skip == nil {
		return 0, false
	}
	return *request.skip, true
}
func (request FrozenReadRequest) Distinct() []FieldID {
	return append([]FieldID(nil), request.distinct...)
}
func (request FrozenReadRequest) Selection() []FrozenReadSelection {
	result := make([]FrozenReadSelection, len(request.selection))
	for index, value := range request.selection {
		result[index] = value.clone()
	}
	return result
}
func (request FrozenReadRequest) ProjectionMode() ReadProjectionMode { return request.projection }
func (request FrozenReadRequest) Omitted() []FieldID                 { return append([]FieldID(nil), request.omit...) }
func (request FrozenReadRequest) Cursor() (FrozenReadCursor, bool) {
	if request.cursor == nil {
		return FrozenReadCursor{}, false
	}
	value := *request.cursor
	value.selector.fields = value.selector.Fields()
	return value, true
}
func (request FrozenReadRequest) Selector() (FrozenUniqueSelector, bool) {
	if request.selector == nil {
		return FrozenUniqueSelector{}, false
	}
	value := *request.selector
	value.fields = value.Fields()
	return value, true
}

func (request FrozenReadRequest) clone() FrozenReadRequest {
	result := request
	result.orders = request.OrderBy()
	result.distinct = request.Distinct()
	result.selection = request.Selection()
	result.omit = request.Omitted()
	if request.take != nil {
		value := *request.take
		result.take = &value
	}
	if request.skip != nil {
		value := *request.skip
		result.skip = &value
	}
	if request.where != nil {
		value := *request.where
		result.where = &value
	}
	if request.selector != nil {
		value := *request.selector
		value.fields = value.Fields()
		result.selector = &value
	}
	if request.cursor != nil {
		value := *request.cursor
		value.selector.fields = value.selector.Fields()
		result.cursor = &value
	}
	return result
}

func (selection FrozenReadSelection) clone() FrozenReadSelection {
	if selection.request != nil {
		request := selection.request.clone()
		selection.request = &request
	}
	return selection
}

func FreezeFindMany[M any](descriptor ModelDescriptor[M], options ...ReadOption[M]) (FrozenReadRequest, error) {
	return freezeReadRequest(ReadFindMany, descriptor, options)
}

func FreezeFindUnique[M any](descriptor ModelDescriptor[M], selector UniqueSelectorValue[M], options ...ReadOption[M]) (FrozenReadRequest, error) {
	model := descriptor.Metadata().ModelID()
	metadata, predicate, err := freezeUniqueSelector[M](ReadFindUnique, model, selector.model, selector.key, selector.components)
	if err != nil {
		return FrozenReadRequest{}, err
	}
	where := Where(predicate)
	allOptions := make([]ReadOption[M], 0, len(options)+1)
	allOptions = append(allOptions, where)
	allOptions = append(allOptions, options...)
	request, err := freezeReadRequest(ReadFindUnique, descriptor, allOptions)
	if err != nil {
		return FrozenReadRequest{}, err
	}
	request.selector = &metadata
	return request, nil
}

func freezeUniqueSelector[M any](operation ReadOperation, model, selectorModel ModelID, key KeyID, components []selectorComponent) (FrozenUniqueSelector, Predicate[M], error) {
	if model == (ModelID{}) || selectorModel != model || key == (KeyID{}) || len(components) == 0 {
		return FrozenUniqueSelector{}, Predicate[M]{}, invalidRead(operation, model, FieldID{}, "unique selector is incomplete or belongs to another model")
	}
	seen := make(map[FieldID]bool, len(components))
	nodes := make([]*predicateNode, len(components))
	fields := make([]FieldID, len(components))
	for index, component := range components {
		if component.field == (FieldID{}) || seen[component.field] {
			return FrozenUniqueSelector{}, Predicate[M]{}, invalidRead(operation, model, component.field, "unique selector has a zero or duplicate field")
		}
		if err := validateFrozenOperand(component.operand); err != nil || component.operand.kind != FrozenOperandOne {
			return FrozenUniqueSelector{}, Predicate[M]{}, invalidReadCause(operation, model, component.field, "unique selector value is invalid", err)
		}
		seen[component.field], fields[index] = true, component.field
		nodes[index] = &predicateNode{kind: FrozenConditionScalar, field: component.field, operator: frozenOperatorEq, mode: FrozenComparisonSensitive, operand: cloneFrozenOperand(component.operand)}
	}
	root := nodes[0]
	if len(nodes) > 1 {
		root = &predicateNode{kind: FrozenConditionLogical, operator: FrozenOperatorAnd, operand: noOperand(), children: nodes}
	}
	return FrozenUniqueSelector{model: model, key: key, fields: fields}, Predicate[M]{node: root}, nil
}

func FreezeFindFirst[M any](descriptor ModelDescriptor[M], options ...ReadOption[M]) (FrozenReadRequest, error) {
	return freezeReadRequest(ReadFindFirst, descriptor, options)
}

func FreezeCount[M any](descriptor ModelDescriptor[M], options ...ReadOption[M]) (FrozenReadRequest, error) {
	return freezeReadRequest(ReadCount, descriptor, options)
}

func freezeReadRequest[M any](operation ReadOperation, descriptor ModelDescriptor[M], options []ReadOption[M]) (FrozenReadRequest, error) {
	model := descriptor.Metadata().ModelID()
	if model == (ModelID{}) {
		return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "model descriptor has a zero identity")
	}
	nodes := make([]readOptionNode, len(options))
	var witness M
	for index, option := range options {
		if option == nil {
			return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, fmt.Sprintf("option %d is nil", index))
		}
		nodes[index] = option.readOption(witness)
	}
	return freezeReadNodes(operation, model, nodes, 0)
}

func freezeReadNodes(operation ReadOperation, model ModelID, nodes []readOptionNode, depth int) (FrozenReadRequest, error) {
	if depth > 64 {
		return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "relation selection exceeds the structural depth limit")
	}
	result := FrozenReadRequest{operation: operation, model: model, projection: ProjectionDefault}
	seen := make(map[readOptionKind]bool)
	for index, node := range nodes {
		if node.kind < readOptionWhere || node.kind > readOptionOmit {
			return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, fmt.Sprintf("option %d has an invalid kind", index))
		}
		if seen[node.kind] {
			return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, fmt.Sprintf("option kind %d appears more than once", node.kind))
		}
		seen[node.kind] = true
		switch node.kind {
		case readOptionWhere:
			if node.freezePredicate == nil {
				return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "where has no predicate")
			}
			predicate, err := node.freezePredicate(model)
			if err != nil {
				return FrozenReadRequest{}, invalidReadCause(operation, model, FieldID{}, "where predicate is invalid", err)
			}
			result.where = &predicate
		case readOptionOrderBy:
			if len(node.orders) == 0 {
				return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "orderBy is empty")
			}
			ordered := make(map[FieldID]bool, len(node.orders))
			for _, order := range node.orders {
				if order.field == (FieldID{}) || (order.direction != SortAscending && order.direction != SortDescending) || ordered[order.field] {
					return FrozenReadRequest{}, invalidRead(operation, model, order.field, "orderBy contains a zero, duplicate, or invalid term")
				}
				ordered[order.field] = true
				result.orders = append(result.orders, FrozenReadOrder{field: order.field, direction: order.direction})
			}
		case readOptionTake:
			value := node.integer
			result.take = &value
		case readOptionSkip:
			if node.integer < 0 {
				return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "skip must be non-negative")
			}
			value := node.integer
			result.skip = &value
		case readOptionDistinct:
			if len(node.fields) == 0 {
				return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "distinct is empty")
			}
			fields := make(map[FieldID]bool, len(node.fields))
			for _, field := range node.fields {
				if field == (FieldID{}) || fields[field] {
					return FrozenReadRequest{}, invalidRead(operation, model, field, "distinct contains a zero or duplicate field")
				}
				fields[field] = true
				result.distinct = append(result.distinct, field)
			}
		case readOptionSelect:
			selection, err := freezeReadSelection(operation, model, node.selection, depth)
			if err != nil {
				return FrozenReadRequest{}, err
			}
			result.selection = selection
			result.projection = ProjectionSelect
		case readOptionCursor:
			metadata, predicate, err := freezeUniqueSelector[struct{}](operation, model, node.selectorModel, node.selectorKey, node.selectorValues)
			if err != nil {
				return FrozenReadRequest{}, err
			}
			frozen, err := predicate.freezeForModel(model)
			if err != nil {
				return FrozenReadRequest{}, invalidReadCause(operation, model, FieldID{}, "cursor selector is invalid", err)
			}
			result.cursor = &FrozenReadCursor{selector: metadata, predicate: frozen}
		case readOptionInclude:
			selection, err := freezeReadSelection(operation, model, node.selection, depth)
			if err != nil {
				return FrozenReadRequest{}, err
			}
			for _, item := range selection {
				if !item.IsRelation() && !item.IsRelationCount() {
					return FrozenReadRequest{}, invalidRead(operation, model, item.FieldID(), "include accepts relations and relation counts only")
				}
			}
			result.selection = selection
			result.projection = ProjectionInclude
		case readOptionOmit:
			if len(node.fields) == 0 {
				return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "omit is empty")
			}
			fields := make(map[FieldID]bool, len(node.fields))
			for _, field := range node.fields {
				if field == (FieldID{}) || fields[field] {
					return FrozenReadRequest{}, invalidRead(operation, model, field, "omit contains a zero or duplicate field")
				}
				fields[field] = true
				result.omit = append(result.omit, field)
			}
		}
	}
	if seen[readOptionSelect] && seen[readOptionInclude] {
		return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "select and include cannot be combined")
	}
	if result.projection == ProjectionSelect && len(result.omit) != 0 {
		return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "select and omit cannot be combined")
	}
	if result.take != nil && *result.take < 0 && len(result.orders) == 0 {
		return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "negative take requires orderBy")
	}
	if operation == ReadCount {
		if len(result.orders) != 0 || result.take != nil || result.skip != nil || len(result.distinct) != 0 || len(result.selection) != 0 || len(result.omit) != 0 || result.cursor != nil {
			return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "count accepts only where")
		}
	}
	if operation == ReadFindUnique && (len(result.orders) != 0 || result.take != nil || result.skip != nil || len(result.distinct) != 0 || result.cursor != nil) {
		return FrozenReadRequest{}, invalidRead(operation, model, FieldID{}, "findUnique accepts only its selector and projection")
	}
	return result, nil
}

func freezeReadSelection(operation ReadOperation, model ModelID, nodes []readSelectionNode, depth int) ([]FrozenReadSelection, error) {
	if len(nodes) == 0 {
		return nil, invalidRead(operation, model, FieldID{}, "select is empty")
	}
	type selectionIdentity struct {
		field      FieldID
		kind       readSelectionKind
		occurrence RuntimeOccurrenceID
	}
	seen := make(map[selectionIdentity]bool, len(nodes))
	result := make([]FrozenReadSelection, 0, len(nodes))
	for _, node := range nodes {
		identity := selectionIdentity{field: node.field, kind: node.kind, occurrence: node.occurrence}
		if node.field == (FieldID{}) || seen[identity] {
			return nil, invalidRead(operation, model, node.field, "select contains a zero or duplicate field")
		}
		seen[identity] = true
		selection := FrozenReadSelection{kind: node.kind, field: node.field, occurrence: node.occurrence}
		switch node.kind {
		case readSelectionScalar:
			if node.relation != (RelationID{}) || node.target != (ModelID{}) || len(node.options) != 0 {
				return nil, invalidRead(operation, model, node.field, "scalar selection carries relation data")
			}
		case readSelectionRelation:
			if node.relation == (RelationID{}) || node.target == (ModelID{}) {
				return nil, invalidRead(operation, model, node.field, "relation selection has incomplete generated identity")
			}
			child, err := freezeReadNodes(ReadFindMany, node.target, node.options, depth+1)
			if err != nil {
				return nil, err
			}
			selection.relation, selection.target, selection.request = node.relation, node.target, &child
		case readSelectionRelationCount:
			if node.relation == (RelationID{}) || node.target == (ModelID{}) {
				return nil, invalidRead(operation, model, node.field, "relation-count selection has incomplete generated identity")
			}
			child, err := freezeReadNodes(ReadCount, node.target, node.options, depth+1)
			if err != nil {
				return nil, err
			}
			selection.relation, selection.target, selection.request = node.relation, node.target, &child
		default:
			return nil, invalidRead(operation, model, node.field, "selection has an invalid kind")
		}
		result = append(result, selection)
	}
	return result, nil
}

func invalidRead(operation ReadOperation, model ModelID, field FieldID, message string) error {
	return &Error{Code: CodeBadUserInput, Operation: readOperationName(operation), Model: model, Field: field, Message: message}
}

func invalidReadCause(operation ReadOperation, model ModelID, field FieldID, message string, cause error) error {
	failure := invalidRead(operation, model, field, message).(*Error)
	failure.cause = cause
	return failure
}

func readOperationName(operation ReadOperation) string {
	switch operation {
	case ReadFindUnique:
		return "findUnique"
	case ReadFindFirst:
		return "findFirst"
	case ReadFindMany:
		return "findMany"
	case ReadCount:
		return "count"
	default:
		return "read"
	}
}
