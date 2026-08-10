package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	gqlgengraphql "github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"
)

// PreparedObject is the generated executable's opaque view of one already
// authorized and selected Golem result object. Generated resolvers are the only
// intended consumers. Values remain in their exact, provider-neutral wire
// representation; in particular, they are never JSON-round-tripped.
type PreparedObject map[string]any

// PreparedInput is the gqlgen-only typed wrapper for already validated input
// objects. Golem binds the original operation AST before this representation is
// decoded, so generated resolvers never treat it as backend authority.
type PreparedInput map[string]any

type preparedOperation struct {
	data   map[string]any
	errors map[string]Error
}

type preparedOperationKey struct{}

func withPreparedOperation(ctx context.Context, response Response) (context.Context, error) {
	errorsByPath := make(map[string]Error, len(response.Errors))
	for _, failure := range response.Errors {
		if len(failure.Path) == 0 {
			continue
		}
		key, err := preparedPathKey(failure.Path)
		if err != nil {
			return nil, err
		}
		errorsByPath[key] = failure
	}
	if response.Data == nil {
		return context.WithValue(ctx, preparedOperationKey{}, preparedOperation{errors: errorsByPath}), nil
	}
	data, ok := response.Data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("prepared GraphQL operation has data %T", response.Data)
	}
	return context.WithValue(ctx, preparedOperationKey{}, preparedOperation{data: data, errors: errorsByPath}), nil
}

type preparedResolutionError struct{ path []any }

func (err preparedResolutionError) Error() string { return "prepared GraphQL field failed" }

// ResolvePreparedRoot is used only by generated gqlgen root resolvers. It
// returns the value which Golem already compiled, authorized, and executed for
// the current response occurrence.
func ResolvePreparedRoot[T any](ctx context.Context) (T, error) {
	var zero T
	operation, ok := ctx.Value(preparedOperationKey{}).(preparedOperation)
	if !ok {
		return zero, fmt.Errorf("prepared GraphQL operation is absent")
	}
	name, err := preparedResponseName(ctx)
	if err != nil {
		return zero, err
	}
	if failurePath, failed := preparedFailurePath(ctx, operation); failed {
		return zero, preparedResolutionError{path: failurePath}
	}
	value, present := operation.data[name]
	if !present {
		return zero, fmt.Errorf("prepared GraphQL root %q is absent", name)
	}
	return coercePrepared[T](value)
}

// ResolvePreparedSubscriptionRoot presents one already prepared event through
// gqlgen's native subscription resolver contract. The protocol loop invokes a
// fresh executable response handler per event, so this channel contains exactly
// one value and never owns the long-lived application event stream.
func ResolvePreparedSubscriptionRoot[T any](ctx context.Context) (<-chan T, error) {
	value, err := ResolvePreparedRoot[T](ctx)
	if err != nil {
		return nil, err
	}
	result := make(chan T, 1)
	result <- value
	close(result)
	return result, nil
}

// ResolvePreparedField is used only by generated gqlgen object resolvers. The
// parent contains occurrence-aware response names, so aliases and repeated
// relation selections remain independent.
func ResolvePreparedField[T any](ctx context.Context, parent PreparedObject) (T, error) {
	var zero T
	name, err := preparedResponseName(ctx)
	if err != nil {
		return zero, err
	}
	operation, _ := ctx.Value(preparedOperationKey{}).(preparedOperation)
	if failurePath, failed := preparedFailurePath(ctx, operation); failed {
		return zero, preparedResolutionError{path: failurePath}
	}
	value, present := parent[name]
	if !present {
		return zero, fmt.Errorf("prepared GraphQL field %q is absent", name)
	}
	return coercePrepared[T](value)
}

func preparedFailurePath(ctx context.Context, operation preparedOperation) ([]any, bool) {
	path := preparedCurrentPath(ctx)
	key, err := preparedPathKey(path)
	if err != nil {
		return nil, false
	}
	_, failed := operation.errors[key]
	return path, failed
}

func preparedCurrentPath(ctx context.Context) []any {
	path := gqlgengraphql.GetPath(ctx)
	result := make([]any, 0, len(path))
	for _, element := range path {
		switch value := element.(type) {
		case ast.PathName:
			result = append(result, string(value))
		case ast.PathIndex:
			result = append(result, int(value))
		}
	}
	return result
}

func preparedPathKey(path []any) (string, error) {
	encoded, err := json.Marshal(path)
	if err != nil {
		return "", fmt.Errorf("prepared GraphQL path is invalid: %w", err)
	}
	return string(encoded), nil
}

func preparedResponseName(ctx context.Context) (string, error) {
	field := gqlgengraphql.GetFieldContext(ctx)
	if field == nil || field.Field.Alias == "" {
		return "", fmt.Errorf("gqlgen field response name is absent")
	}
	return field.Field.Alias, nil
}

func coercePrepared[T any](value any) (T, error) {
	var result T
	target := reflect.TypeOf((*T)(nil)).Elem()
	converted, err := coercePreparedValue(value, target)
	if err != nil {
		return result, err
	}
	if !converted.IsValid() {
		return result, nil
	}
	reflect.ValueOf(&result).Elem().Set(converted)
	return result, nil
}

func coercePreparedValue(value any, target reflect.Type) (reflect.Value, error) {
	if value == nil {
		switch target.Kind() {
		case reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
			return reflect.Zero(target), nil
		default:
			return reflect.Value{}, fmt.Errorf("prepared non-null %s is null", target)
		}
	}
	source := reflect.ValueOf(value)
	if source.Type().AssignableTo(target) {
		return source, nil
	}
	if target.Kind() == reflect.Interface && source.Type().Implements(target) {
		return source, nil
	}
	if target.Kind() == reflect.Pointer {
		item, err := coercePreparedValue(value, target.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		pointer := reflect.New(target.Elem())
		pointer.Elem().Set(item)
		return pointer, nil
	}
	if target.Kind() == reflect.Slice && (source.Kind() == reflect.Slice || source.Kind() == reflect.Array) {
		result := reflect.MakeSlice(target, source.Len(), source.Len())
		for index := 0; index < source.Len(); index++ {
			item, err := coercePreparedValue(source.Index(index).Interface(), target.Elem())
			if err != nil {
				return reflect.Value{}, fmt.Errorf("prepared list item %d: %w", index, err)
			}
			result.Index(index).Set(item)
		}
		return result, nil
	}
	if target.Kind() == reflect.Map && source.Kind() == reflect.Map && target.Key().Kind() == reflect.String {
		result := reflect.MakeMapWithSize(target, source.Len())
		iterator := source.MapRange()
		for iterator.Next() {
			key, err := coercePreparedValue(iterator.Key().Interface(), target.Key())
			if err != nil {
				return reflect.Value{}, err
			}
			item, err := coercePreparedValue(iterator.Value().Interface(), target.Elem())
			if err != nil {
				return reflect.Value{}, err
			}
			result.SetMapIndex(key, item)
		}
		return result, nil
	}
	if source.Type().ConvertibleTo(target) && samePreparedScalarFamily(source.Kind(), target.Kind()) {
		return source.Convert(target), nil
	}
	return reflect.Value{}, fmt.Errorf("prepared GraphQL value %T cannot become %s", value, target)
}

func samePreparedScalarFamily(left, right reflect.Kind) bool {
	if left == right && (left == reflect.String || left == reflect.Bool) {
		return true
	}
	signed := func(value reflect.Kind) bool { return value >= reflect.Int && value <= reflect.Int64 }
	unsigned := func(value reflect.Kind) bool { return value >= reflect.Uint && value <= reflect.Uint64 }
	floating := func(value reflect.Kind) bool { return value == reflect.Float32 || value == reflect.Float64 }
	return signed(left) && signed(right) || unsigned(left) && unsigned(right) || floating(left) && floating(right)
}
