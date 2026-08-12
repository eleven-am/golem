package physical

import (
	"fmt"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

// ValidateOptimisticConcurrencyLogical revalidates the complete logical
// eligibility boundary before provider lowering or runtime bootstrap. The
// compiler is not a trust boundary: fingerprinted IR may be forged and then
// re-fingerprinted by a caller.
func ValidateOptimisticConcurrencyLogical(model ir.ModelIR) error {
	for _, declaration := range model.Models {
		if declaration.OptimisticConcurrency == nil {
			continue
		}
		fieldID := *declaration.OptimisticConcurrency
		var selected *ir.FieldIR
		for index := range declaration.Fields {
			if declaration.Fields[index].ID == fieldID {
				if selected != nil {
					return fmt.Errorf("model %s optimistic concurrency field %s is duplicated", declaration.ID, fieldID)
				}
				selected = &declaration.Fields[index]
			}
		}
		if selected == nil || selected.Kind != ir.FieldScalar || selected.Scalar == nil {
			return fmt.Errorf("model %s optimistic concurrency field %s must be one same-model stored scalar", declaration.ID, fieldID)
		}
		scalar := selected.Scalar
		switch {
		case scalar.Type.Kind != ir.TypeInt64:
			return fmt.Errorf("model %s optimistic concurrency field %s must have logical type int64", declaration.ID, fieldID)
		case scalar.Nullable:
			return fmt.Errorf("model %s optimistic concurrency field %s must be non-null", declaration.ID, fieldID)
		case scalar.Default != nil:
			return fmt.Errorf("model %s optimistic concurrency field %s cannot have an authored default", declaration.ID, fieldID)
		case scalar.Generation != nil:
			return fmt.Errorf("model %s optimistic concurrency field %s cannot be generated", declaration.ID, fieldID)
		case scalar.Updated:
			return fmt.Errorf("model %s optimistic concurrency field %s cannot have updated behavior", declaration.ID, fieldID)
		case scalar.DatabaseReadOnly:
			return fmt.Errorf("model %s optimistic concurrency field %s cannot be database-read-only", declaration.ID, fieldID)
		}
		if logicalKeyContains(declaration.PrimaryKey, fieldID) || logicalKeysContain(declaration.Uniques, fieldID) {
			return fmt.Errorf("model %s optimistic concurrency field %s cannot participate in a primary or unique key", declaration.ID, fieldID)
		}
		for _, relation := range model.Relations {
			if relation.ForeignKey == nil {
				continue
			}
			if relation.SourceModel == declaration.ID && logicalFieldsContain(relation.LocalFields, fieldID) || relation.TargetModel == declaration.ID && logicalFieldsContain(relation.RemoteFields, fieldID) {
				return fmt.Errorf("model %s optimistic concurrency field %s cannot participate in a local or referenced foreign key", declaration.ID, fieldID)
			}
		}
	}
	return nil
}

func logicalKeyContains(key *ir.KeyIR, field ir.FieldID) bool {
	return key != nil && logicalFieldsContain(key.Fields, field)
}

func logicalKeysContain(keys []ir.KeyIR, field ir.FieldID) bool {
	for index := range keys {
		if logicalKeyContains(&keys[index], field) {
			return true
		}
	}
	return false
}

func logicalFieldsContain(fields []ir.FieldID, field ir.FieldID) bool {
	for _, candidate := range fields {
		if candidate == field {
			return true
		}
	}
	return false
}
