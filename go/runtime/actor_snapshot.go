package runtime

import (
	"fmt"
	"reflect"
)

// snapshotActor establishes the single actor value shared by policy factories
// and read hooks for one Caller. A configured snapshotter is the explicit
// ownership transfer for mutable actor shapes. Without one, only deeply
// immutable value data is accepted.
func snapshotActor[A any](actor A, snapshot func(A) (A, error)) (A, error) {
	if snapshot != nil {
		result, err := snapshot(actor)
		if err != nil {
			var zero A
			return zero, fmt.Errorf("P3_ACTOR_SNAPSHOT: snapshot function failed: %w", err)
		}
		return result, nil
	}
	if err := validateImmutableActor(reflect.ValueOf(actor), "actor"); err != nil {
		var zero A
		return zero, err
	}
	return actor, nil
}

func validateImmutableActor(value reflect.Value, path string) error {
	return validateImmutableValue(value, path, func(path string, kind reflect.Kind, mutable bool) error {
		if mutable {
			return fmt.Errorf("P3_ACTOR_SNAPSHOT: %s has mutable or aliasing kind %s; configure SnapshotActor", path, kind)
		}
		return fmt.Errorf("P3_ACTOR_SNAPSHOT: %s has unsupported kind %s; configure SnapshotActor", path, kind)
	})
}

func validateImmutableValue(value reflect.Value, path string, invalid func(string, reflect.Kind, bool) error) error {
	if !value.IsValid() {
		return nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return validateImmutableValue(value.Elem(), path+".(dynamic)", invalid)
	case reflect.Struct:
		typ := value.Type()
		for index := 0; index < value.NumField(); index++ {
			if err := validateImmutableValue(value.Field(index), path+"."+typ.Field(index).Name, invalid); err != nil {
				return err
			}
		}
		return nil
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateImmutableValue(value.Index(index), fmt.Sprintf("%s[%d]", path, index), invalid); err != nil {
				return err
			}
		}
		return nil
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return invalid(path, value.Kind(), true)
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.String:
		return nil
	default:
		return invalid(path, value.Kind(), false)
	}
}
