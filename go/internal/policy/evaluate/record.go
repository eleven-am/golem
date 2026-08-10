package evaluate

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/policy/dependency"
	"github.com/eleven-am/golem/go/internal/policy/ir"
)

type fieldKind uint8

const (
	fieldNull     fieldKind = 1
	fieldValue    fieldKind = 2
	fieldRelation fieldKind = 3
	fieldList     fieldKind = 4
)

type listElement struct {
	valid bool
	value ir.Value
}

// ListElement represents a physically stored scalar-list element. Invalid list
// elements are retained so evaluators can conservatively treat them as
// non-matches without confusing them with an absent field or empty list.
type ListElement struct{ value listElement }

// Field is one explicitly loaded record field. A field omitted from NewRecord
// is missing, which is different from NullField and from a loaded empty
// relation.
type Field struct {
	id          ir.FieldID
	kind        fieldKind
	value       ir.Value
	target      ir.ModelID
	cardinality ir.RelationCardinality
	rows        []Record
	list        []listElement
}

// Record is an immutable evaluator input rooted at one typed model identity.
// Its map is keyed only by fixed-width generated field identities.
type Record struct {
	model  ir.ModelID
	fields map[ir.FieldID]Field
}

func NullField(id ir.FieldID) Field {
	return Field{id: id, kind: fieldNull}
}

func ValueField(id ir.FieldID, value ir.Value) (Field, error) {
	if err := value.Validate(); err != nil {
		return Field{}, fmt.Errorf("policy evaluate record: invalid field value: %w", err)
	}
	return Field{id: id, kind: fieldValue, value: value}, nil
}

func ValidListElement(value ir.Value) (ListElement, error) {
	if err := value.Validate(); err != nil {
		return ListElement{}, fmt.Errorf("policy evaluate record: invalid list element: %w", err)
	}
	if value.Kind() == ir.ValueBytes || value.Kind() == ir.ValueJSON || value.Kind() == ir.ValueScalarList {
		return ListElement{}, fmt.Errorf("policy evaluate record: unsupported scalar-list element kind %d", value.Kind())
	}
	return ListElement{value: listElement{valid: true, value: value}}, nil
}

// InvalidListElement represents a stored JSON null or wrong-typed element.
// Such an element counts toward list length but never matches a typed operand.
func InvalidListElement() ListElement { return ListElement{value: listElement{}} }

func ListField(id ir.FieldID, elements ...ListElement) (Field, error) {
	if id == (ir.FieldID{}) {
		return Field{}, fmt.Errorf("policy evaluate record: scalar list requires a non-zero field ID")
	}
	copyElements := make([]listElement, len(elements))
	for index, element := range elements {
		if element.value.valid {
			if err := element.value.value.Validate(); err != nil {
				return Field{}, fmt.Errorf("policy evaluate record: list element %d: %w", index, err)
			}
		}
		copyElements[index] = element.value
	}
	return Field{id: id, kind: fieldList, list: copyElements}, nil
}

// ToOneField records a loaded to-one relation. Zero rows means a genuine
// absent related row; one row means present. More than one is rejected.
func ToOneField(id ir.FieldID, target ir.ModelID, rows ...Record) (Field, error) {
	if len(rows) > 1 {
		return Field{}, fmt.Errorf("policy evaluate record: to-one relation has %d rows", len(rows))
	}
	return relationField(id, target, ir.RelationToOne, rows)
}

// ToManyField records a loaded to-many relation. A non-nil zero-length input is
// not special: construction itself is the evidence that the relation was
// loaded, so zero rows is a genuine loaded empty relation.
func ToManyField(id ir.FieldID, target ir.ModelID, rows ...Record) (Field, error) {
	return relationField(id, target, ir.RelationToMany, rows)
}

func relationField(id ir.FieldID, target ir.ModelID, cardinality ir.RelationCardinality, rows []Record) (Field, error) {
	if id == (ir.FieldID{}) || target == (ir.ModelID{}) {
		return Field{}, fmt.Errorf("policy evaluate record: relation requires non-zero field and target IDs")
	}
	copyRows := make([]Record, len(rows))
	for index, row := range rows {
		if row.model != target {
			return Field{}, fmt.Errorf("policy evaluate record: relation row %d model mismatch", index)
		}
		copyRows[index] = row.clone()
	}
	return Field{id: id, kind: fieldRelation, target: target, cardinality: cardinality, rows: copyRows}, nil
}

func NewRecord(model ir.ModelID, fields ...Field) (Record, error) {
	if model == (ir.ModelID{}) {
		return Record{}, fmt.Errorf("policy evaluate record: zero model ID")
	}
	result := Record{model: model, fields: make(map[ir.FieldID]Field, len(fields))}
	for index, field := range fields {
		if field.id == (ir.FieldID{}) {
			return Record{}, fmt.Errorf("policy evaluate record: field %d has zero identity", index)
		}
		if _, duplicate := result.fields[field.id]; duplicate {
			return Record{}, fmt.Errorf("policy evaluate record: duplicate field %x", field.id)
		}
		if field.kind != fieldNull && field.kind != fieldValue && field.kind != fieldRelation && field.kind != fieldList {
			return Record{}, fmt.Errorf("policy evaluate record: field %x has invalid state", field.id)
		}
		result.fields[field.id] = field.clone()
	}
	return result, nil
}

func (record Record) ModelID() ir.ModelID { return record.model }

func (field Field) clone() Field {
	field.rows = cloneRecords(field.rows)
	field.list = append([]listElement(nil), field.list...)
	return field
}

func (record Record) clone() Record {
	copy := Record{model: record.model, fields: make(map[ir.FieldID]Field, len(record.fields))}
	for id, field := range record.fields {
		copy.fields[id] = field.clone()
	}
	return copy
}

func cloneRecords(rows []Record) []Record {
	result := make([]Record, len(rows))
	for index := range rows {
		result[index] = rows[index].clone()
	}
	return result
}

func (record Record) checkDependencies(tree dependency.Tree) error {
	if record.model != tree.ModelID() {
		return &Error{Code: CodeModel, ModelID: record.model, Detail: "dependency tree model mismatch"}
	}
	for _, entry := range tree.Entries() {
		field, loaded := record.fields[entry.FieldID()]
		if !loaded {
			return &Error{Code: CodeMissing, ModelID: record.model, FieldID: entry.FieldID(), Detail: "required dependency is not loaded"}
		}
		switch entry.Kind() {
		case dependency.Scalar:
			if field.kind != fieldNull && field.kind != fieldValue && field.kind != fieldList {
				return &Error{Code: CodeType, ModelID: record.model, FieldID: entry.FieldID(), Detail: "scalar dependency was loaded as a relation"}
			}
		case dependency.Relation:
			target, _ := entry.TargetModel()
			if field.kind != fieldRelation || field.target != target {
				return &Error{Code: CodeType, ModelID: record.model, FieldID: entry.FieldID(), Detail: "relation dependency shape mismatch"}
			}
			children := entry.Children()
			for index := range field.rows {
				if err := field.rows[index].checkDependencies(children); err != nil {
					return err
				}
			}
		default:
			return &Error{Code: CodeInternal, ModelID: record.model, FieldID: entry.FieldID(), Detail: "unknown dependency kind"}
		}
	}
	return nil
}
