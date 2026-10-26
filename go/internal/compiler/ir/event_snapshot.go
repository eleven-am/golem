package ir

import "fmt"

const MaxBoundedDeleteSnapshotStringLength uint32 = 1024

func RequiredDeleteSnapshotFields(model ModelDeclIR, relations []RelationIR) map[FieldID]bool {
	required := map[FieldID]bool{}
	if model.PrimaryKey != nil {
		for _, field := range model.PrimaryKey.Fields {
			required[field] = true
		}
	}
	for _, relation := range relations {
		if relation.SourceModel != model.ID || relation.Cardinality != RelationOne || relation.Through != nil {
			continue
		}
		for _, field := range relation.LocalFields {
			required[field] = true
		}
	}
	return required
}

func BoundedDeleteSnapshotType(value LogicalTypeIR) bool {
	switch value.Kind {
	case TypeBool, TypeInt16, TypeInt32, TypeInt64, TypeFloat32, TypeFloat64,
		TypeDecimal, TypeUUID, TypeDate, TypeTime, TypeDateTime, TypeEnum:
		return true
	case TypeString:
		return value.MaxLength != nil && *value.MaxLength <= MaxBoundedDeleteSnapshotStringLength
	default:
		return false
	}
}

func DeleteSnapshotFields(model ModelDeclIR, relations []RelationIR, declared *[]FieldID) ([]FieldID, error) {
	required := RequiredDeleteSnapshotFields(model, relations)
	selected := make(map[FieldID]bool, len(required))
	for field := range required {
		selected[field] = true
	}
	stored := make(map[FieldID]FieldIR, len(model.Fields))
	for _, field := range model.Fields {
		if field.Kind != FieldRelation && field.Scalar != nil {
			stored[field.ID] = field
		}
	}
	for field := range required {
		if _, exists := stored[field]; !exists {
			return nil, fmt.Errorf("required delete snapshot field %s is not a stored scalar of model %s", field, model.ID)
		}
	}
	if declared != nil {
		for _, fieldID := range *declared {
			if _, exists := stored[fieldID]; !exists {
				return nil, fmt.Errorf("delete snapshot field %s is not a stored scalar of model %s", fieldID, model.ID)
			}
			selected[fieldID] = true
		}
	} else {
		for fieldID, field := range stored {
			if BoundedDeleteSnapshotType(field.Scalar.Type) {
				selected[fieldID] = true
			}
		}
	}
	result := make([]FieldID, 0, len(selected))
	for _, field := range model.Fields {
		if _, exists := stored[field.ID]; exists && selected[field.ID] {
			result = append(result, field.ID)
		}
	}
	return result, nil
}

func ValidateDeleteSnapshotFields(model ModelDeclIR, relations []RelationIR, snapshot []FieldID) error {
	stored := make(map[FieldID]bool, len(model.Fields))
	for _, field := range model.Fields {
		if field.Kind != FieldRelation && field.Scalar != nil {
			stored[field.ID] = true
		}
	}
	present := make(map[FieldID]bool, len(snapshot))
	for _, fieldID := range snapshot {
		if !stored[fieldID] {
			return fmt.Errorf("delete snapshot field %s is not a stored scalar of model %s", fieldID, model.ID)
		}
		if present[fieldID] {
			return fmt.Errorf("delete snapshot of model %s repeats field %s", model.ID, fieldID)
		}
		present[fieldID] = true
	}
	for field := range RequiredDeleteSnapshotFields(model, relations) {
		if !present[field] {
			return fmt.Errorf("delete snapshot of model %s omits required identity or foreign-key field %s", model.ID, field)
		}
	}
	return nil
}

func DeleteSnapshotIsFull(model ModelDeclIR, snapshot []FieldID) bool {
	stored := 0
	for _, field := range model.Fields {
		if field.Kind != FieldRelation && field.Scalar != nil {
			stored++
		}
	}
	return len(snapshot) == stored
}
