package typedvalue

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

type ListBoundError struct{ Max uint32 }

func (failure *ListBoundError) Error() string {
	return fmt.Sprintf("list holds more than %d items", failure.Max)
}

// CheckListBounds traverses typed resolver values and their normalized maps
// through the same named contract. Bounds are checked before reading list items.
func CheckListBounds(typ ir.GraphQLTypeIR, value reflect.Value, definitions []ir.GraphQLTypeDefinitionIR) error {
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return nil
	}
	if typ.Kind == ir.GraphQLTypeObject {
		for _, definition := range definitions {
			if definition.Name != typ.Name || definition.Kind != typ.Kind {
				continue
			}
			for _, field := range definition.Fields {
				var item reflect.Value
				switch value.Kind() {
				case reflect.Struct:
					item = value.FieldByName(field.GoName)
				case reflect.Map:
					if value.Type().Key() != reflect.TypeFor[string]() {
						return fmt.Errorf("object result has non-string keys")
					}
					item = value.MapIndex(reflect.ValueOf(field.Name))
				default:
					return fmt.Errorf("object result is %s, not an object", value.Kind())
				}
				if err := CheckListBounds(field.Type, item, definitions); err != nil {
					return fmt.Errorf("%s.%s: %w", typ.Name, field.Name, err)
				}
			}
			return nil
		}
		return fmt.Errorf("object result definition %s is absent", typ.Name)
	}
	if typ.Kind != ir.GraphQLTypeList {
		return nil
	}
	if typ.MaxItems == 0 {
		return fmt.Errorf("list result declares no maximum item count")
	}
	if value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
		return fmt.Errorf("list result is %s, not a list", value.Kind())
	}
	if uint64(value.Len()) > uint64(typ.MaxItems) {
		return &ListBoundError{Max: typ.MaxItems}
	}
	if typ.Element == nil || typ.Element.Kind != ir.GraphQLTypeList && typ.Element.Kind != ir.GraphQLTypeObject {
		return nil
	}
	for index := 0; index < value.Len(); index++ {
		if err := CheckListBounds(*typ.Element, value.Index(index), definitions); err != nil {
			return err
		}
	}
	return nil
}

func ListItemsError(operation, owner string, err error) error {
	var exceeded *ListBoundError
	if !errors.As(err, &exceeded) {
		return err
	}
	return golem.RuntimeLimitError(operation, golem.ModelID{}, golem.LimitListItems, int(exceeded.Max), fmt.Sprintf("%s returned more than %d items", owner, exceeded.Max), err)
}
