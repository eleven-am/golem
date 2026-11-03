package contract

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

func CheckListBounds(typ ir.GraphQLTypeIR, value reflect.Value) error {
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if typ.Kind != ir.GraphQLTypeList || !value.IsValid() {
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
	if typ.Element == nil || typ.Element.Kind != ir.GraphQLTypeList {
		return nil
	}
	for index := 0; index < value.Len(); index++ {
		if err := CheckListBounds(*typ.Element, value.Index(index)); err != nil {
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
