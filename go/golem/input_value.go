package golem

import "fmt"

// InputValue distinguishes an omitted input from explicit null and a value.
// Its zero value is omitted. Use a non-pointer T: this wrapper owns nullability.
// Golem decodes it in resolver argument structs and registered input objects.
type InputValue[T any] struct {
	value T
	set   bool
	null  bool
}

// IsSet includes both explicit null and non-null values.
func (value InputValue[T]) IsSet() bool  { return value.set }
func (value InputValue[T]) IsNull() bool { return value.set && value.null }

// Get reports a value only when the input was present and non-null.
func (value InputValue[T]) Get() (T, bool) { return value.value, value.set && !value.null }

// InputValueOf constructs a present value for calls made directly in Go.
func InputValueOf[T any](value T) InputValue[T] { return InputValue[T]{value: value, set: true} }
func NullInputValue[T any]() InputValue[T]      { return InputValue[T]{set: true, null: true} }

type omittedInputValue struct{}

// RuntimeOmittedInputValue is the opaque omission marker used by generated
// input binding. It cannot be constructed by JSON or GraphQL input values.
func RuntimeOmittedInputValue() any { return omittedInputValue{} }

type inputValueTarget interface {
	decodeInputValue(any, func(any, any) error) error
}

func (target *InputValue[T]) decodeInputValue(raw any, decode func(any, any) error) error {
	if target == nil {
		return fmt.Errorf("input value target is nil")
	}
	if _, omitted := raw.(omittedInputValue); omitted {
		*target = InputValue[T]{}
		return nil
	}
	if raw == nil {
		*target = NullInputValue[T]()
		return nil
	}
	if decode == nil {
		return fmt.Errorf("input value decoder is nil")
	}
	var value T
	if err := decode(raw, &value); err != nil {
		return err
	}
	*target = InputValueOf(value)
	return nil
}

// RuntimeDecodeInputValue recognizes the sealed InputValue target and delegates
// conversion of its element to the existing typed input decoder. The callback's
// second argument is a non-nil pointer to T. False means this is another type.
func RuntimeDecodeInputValue(target, raw any, decode func(any, any) error) (bool, error) {
	value, ok := target.(inputValueTarget)
	if !ok {
		return false, nil
	}
	return true, value.decodeInputValue(raw, decode)
}
