package ir

import (
	"fmt"
	"sort"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

type ScalarOperationKind uint8

const (
	ScalarSet ScalarOperationKind = iota + 1
	ScalarNull
	ScalarIncrement
	ScalarDecrement
)

type ScalarOperation struct {
	field        policyir.FieldID
	fieldType    policyir.TypeRef
	kind         ScalarOperationKind
	value        policyir.Value
	hasValue     bool
	runtimeOwned bool
}

func NewScalarOperation(field policyir.FieldID, fieldType policyir.TypeRef, kind ScalarOperationKind, value *policyir.Value) (ScalarOperation, error) {
	result := ScalarOperation{field: field, fieldType: fieldType, kind: kind}
	if value != nil {
		result.value, result.hasValue = *value, true
	}
	if err := result.validate(); err != nil {
		return ScalarOperation{}, err
	}
	return result, nil
}

func NewSet(field policyir.FieldID, fieldType policyir.TypeRef, value policyir.Value) (ScalarOperation, error) {
	return NewScalarOperation(field, fieldType, ScalarSet, &value)
}

// NewRuntimeSet constructs a runtime-owned assignment. It is persisted like
// Set but excluded from caller-authored field authorization and exact authored
// diffs. Only the runtime-owned value resolver may call this constructor.
func NewRuntimeSet(field policyir.FieldID, fieldType policyir.TypeRef, value policyir.Value) (ScalarOperation, error) {
	result, err := NewScalarOperation(field, fieldType, ScalarSet, &value)
	if err != nil {
		return ScalarOperation{}, err
	}
	result.runtimeOwned = true
	return result, nil
}

func NewNull(field policyir.FieldID, fieldType policyir.TypeRef) (ScalarOperation, error) {
	return NewScalarOperation(field, fieldType, ScalarNull, nil)
}

func NewIncrement(field policyir.FieldID, fieldType policyir.TypeRef, value policyir.Value) (ScalarOperation, error) {
	return NewScalarOperation(field, fieldType, ScalarIncrement, &value)
}

func NewDecrement(field policyir.FieldID, fieldType policyir.TypeRef, value policyir.Value) (ScalarOperation, error) {
	return NewScalarOperation(field, fieldType, ScalarDecrement, &value)
}

func (operation ScalarOperation) FieldID() policyir.FieldID { return operation.field }
func (operation ScalarOperation) Type() policyir.TypeRef    { return operation.fieldType }
func (operation ScalarOperation) Kind() ScalarOperationKind { return operation.kind }
func (operation ScalarOperation) RuntimeOwned() bool        { return operation.runtimeOwned }
func (operation ScalarOperation) Value() (policyir.Value, bool) {
	return operation.value, operation.hasValue
}
func (operation ScalarOperation) Validate() error { return operation.validate() }

func (operation ScalarOperation) validate() error {
	if operation.field == (policyir.FieldID{}) {
		return fmt.Errorf("P4_MUTATION_IR_SCALAR: field identity is zero")
	}
	if err := operation.fieldType.Validate(); err != nil {
		return fmt.Errorf("P4_MUTATION_IR_SCALAR: invalid field type: %w", err)
	}
	switch operation.kind {
	case ScalarSet:
		if !operation.hasValue {
			return fmt.Errorf("P4_MUTATION_IR_SCALAR: set requires a value")
		}
	case ScalarNull:
		if operation.hasValue || !operation.fieldType.Nullable() {
			return fmt.Errorf("P4_MUTATION_IR_SCALAR: null requires a nullable field and no value")
		}
	case ScalarIncrement, ScalarDecrement:
		if !operation.hasValue || !numeric(operation.fieldType.Kind()) {
			return fmt.Errorf("P4_MUTATION_IR_SCALAR: arithmetic requires a numeric field and value")
		}
	default:
		return fmt.Errorf("P4_MUTATION_IR_SCALAR: unknown operation %d", operation.kind)
	}
	if operation.runtimeOwned && operation.kind != ScalarSet {
		return fmt.Errorf("P4_MUTATION_IR_SCALAR: runtime-owned operation must be set")
	}
	if operation.hasValue {
		if err := operation.value.Validate(); err != nil {
			return fmt.Errorf("P4_MUTATION_IR_SCALAR: invalid value: %w", err)
		}
		if operation.value.Kind() != operation.fieldType.Kind() {
			return fmt.Errorf("P4_MUTATION_IR_SCALAR: value kind %d does not match field kind %d", operation.value.Kind(), operation.fieldType.Kind())
		}
		if err := valueFitsType(operation.value, operation.fieldType); err != nil {
			return fmt.Errorf("P4_MUTATION_IR_SCALAR: value does not fit field type: %w", err)
		}
	}
	return nil
}

func valueFitsType(value policyir.Value, fieldType policyir.TypeRef) error {
	switch fieldType.Kind() {
	case policyir.ValueDecimal:
		coefficient, scale, _ := value.Decimal()
		if uint16(scale) > fieldType.Scale() || decimalDigits(coefficient) > int(fieldType.Precision()) {
			return fmt.Errorf("decimal exceeds declared precision or scale")
		}
	case policyir.ValueEnum:
		enum, _, _ := value.Enum()
		declared, _ := fieldType.EnumID()
		if enum != declared {
			return fmt.Errorf("enum identity mismatch")
		}
	case policyir.ValueTime:
		microseconds, _ := value.Time()
		if microseconds%temporalQuantum(fieldType.Precision()) != 0 {
			return fmt.Errorf("time exceeds declared precision")
		}
	case policyir.ValueDateTime:
		_, nanoseconds, _ := value.DateTime()
		if int64(nanoseconds)%(temporalQuantum(fieldType.Precision())*1_000) != 0 {
			return fmt.Errorf("datetime exceeds declared precision")
		}
	case policyir.ValueScalarList:
		elementType, ok := fieldType.Element()
		if !ok {
			return fmt.Errorf("scalar list has no declared element type")
		}
		values, _ := value.List()
		for index, element := range values {
			if element.Kind() != elementType.Kind() {
				return fmt.Errorf("list element %d kind mismatch", index)
			}
			if err := valueFitsType(element, elementType); err != nil {
				return fmt.Errorf("list element %d: %w", index, err)
			}
		}
	}
	return nil
}

func decimalDigits(value int64) int {
	magnitude := uint64(value)
	if value < 0 {
		magnitude = uint64(-(value + 1)) + 1
	}
	if magnitude == 0 {
		return 1
	}
	digits := 0
	for magnitude != 0 {
		digits++
		magnitude /= 10
	}
	return digits
}

// TypeRef temporal precision is fractional decimal digits. Time values use
// microseconds while datetime exposes nanoseconds but is portable to at most
// microsecond precision.
func temporalQuantum(precision uint16) int64 {
	quantum := int64(1)
	for index := precision; index < 6; index++ {
		quantum *= 10
	}
	return quantum
}

func numeric(kind policyir.ValueKind) bool {
	switch kind {
	case policyir.ValueInt16, policyir.ValueInt32, policyir.ValueInt64, policyir.ValueFloat32, policyir.ValueFloat64, policyir.ValueDecimal:
		return true
	default:
		return false
	}
}

func normalizeScalarOperations(input []ScalarOperation) ([]ScalarOperation, error) {
	result := append([]ScalarOperation(nil), input...)
	for index := range result {
		if err := result[index].validate(); err != nil {
			return nil, fmt.Errorf("scalar operation %d: %w", index, err)
		}
	}
	sort.Slice(result, func(i, j int) bool { return string(result[i].field[:]) < string(result[j].field[:]) })
	for index := 1; index < len(result); index++ {
		if result[index-1].field == result[index].field {
			return nil, fmt.Errorf("P4_MUTATION_IR_SCALAR: field has more than one operation")
		}
	}
	return result, nil
}
