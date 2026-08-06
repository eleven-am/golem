package golem

import (
	"fmt"
	"time"
)

// GraphQLModel is the compile-time destination for GraphQL declarations owned
// by one model. DefineGraphQL methods are statically interpreted; values of
// this type are never used to build a schema at runtime.
type GraphQLModel[M any] struct{ _ func() M }

// GraphQLSchema is the compile-time destination for application-owned query
// and mutation roots. Its closed declaration functions deliberately provide no
// database, transaction, System, or raw-SQL capability.
type GraphQLSchema struct{ _ graphqlSchemaMarker }
type graphqlSchemaMarker struct{}

// GraphQLResultType is sealed to the constructors in this package. A schema
// declaration therefore cannot smuggle an arbitrary named GraphQL type into
// the compiler.
type GraphQLResultType interface{ graphqlResultType() }

// GraphQLType retains its Go result witness while keeping the transport
// representation private. The compiler derives its ContractIR type tree from
// the constructor call and its type arguments, never from runtime reflection.
type GraphQLType[T any] struct {
	nonNull bool
	_       func() T
}

func (GraphQLType[T]) graphqlResultType() {}

// NonNull returns the same closed GraphQL type with explicit non-null output
// semantics. Types are nullable unless this method is present in source.
func (value GraphQLType[T]) NonNull() GraphQLType[T] {
	value.nonNull = true
	return value
}

func GraphQLBoolean() GraphQLType[bool]       { return GraphQLType[bool]{} }
func GraphQLInt() GraphQLType[int32]          { return GraphQLType[int32]{} }
func GraphQLFloat() GraphQLType[float64]      { return GraphQLType[float64]{} }
func GraphQLString() GraphQLType[string]      { return GraphQLType[string]{} }
func GraphQLBigInt() GraphQLType[int64]       { return GraphQLType[int64]{} }
func GraphQLDecimal() GraphQLType[Decimal]    { return GraphQLType[Decimal]{} }
func GraphQLUUID() GraphQLType[UUID]          { return GraphQLType[UUID]{} }
func GraphQLDate() GraphQLType[Date]          { return GraphQLType[Date]{} }
func GraphQLTime() GraphQLType[Time]          { return GraphQLType[Time]{} }
func GraphQLDateTime() GraphQLType[time.Time] { return GraphQLType[time.Time]{} }
func GraphQLBytes() GraphQLType[[]byte]       { return GraphQLType[[]byte]{} }
func GraphQLJSON() GraphQLType[JSON[any]]     { return GraphQLType[JSON[any]]{} }

// GraphQLObject, GraphQLEnum, and GraphQLList form the closed composite output
// vocabulary. Registration and enum declarations are validated by the static
// compiler before these become ContractIR.
func GraphQLObject[M any]() GraphQLType[Row[M]] { return GraphQLType[Row[M]]{} }
func GraphQLEnum[E ~string]() GraphQLType[E]    { return GraphQLType[E]{} }
func GraphQLList[T any](_ GraphQLType[T]) GraphQLType[[]T] {
	return GraphQLType[[]T]{}
}

// ComputedOption is sealed to dependency declarations. Batch configuration is
// expressed by BatchedComputedField so a scalar key and typed loader are always
// present together.
type ComputedOption[M any] interface{ computedOption(M) }
type computedOption[M any] struct{ _ func() M }

func (computedOption[M]) computedOption(M) {}

// Requires declares the exact scalar or relation handles that must be added to
// the authorized projection when (and only when) this computed field is
// selected.
func Requires[M any](_ Field[M], _ ...Field[M]) ComputedOption[M] {
	return computedOption[M]{}
}

// ComputedField declares a model-attached field. The compiler validates the
// resolver's context, public Row[M], typed argument struct, result, and error
// signature; the shell intentionally performs no runtime registration.
func ComputedField[M, Resolver any](_ *GraphQLModel[M], _ string, _ GraphQLResultType, _ Resolver, _ ...ComputedOption[M]) {
}

// BatchedComputedField declares an executor-dispatched computed field. Key is
// constrained to a generated scalar column and Loader remains statically typed;
// the compiler validates its []K input and exact key-to-result output contract.
func BatchedComputedField[M any, K comparable, Loader any](_ *GraphQLModel[M], _ string, _ GraphQLResultType, _ ScalarColumn[M, K], _ Loader, _ uint32, _ ...ComputedOption[M]) {
}

// BatchedComputedFieldWithCacheKey is the explicit-codec form used when K's
// Go equality is not the desired canonical cache identity. The codec is typed
// to K and statically required to return a stable string or an error.
func BatchedComputedFieldWithCacheKey[M any, K comparable, Loader any](_ *GraphQLModel[M], _ string, _ GraphQLResultType, _ ScalarColumn[M, K], _ Loader, _ func(K) (string, error), _ uint32, _ ...ComputedOption[M]) {
}

// Query and Mutation are the only custom-root constructors. Their resolver
// signatures are statically required to receive context.Context followed by
// the generated *Caller[Principal] and a typed argument struct. In particular,
// no declaration API exists for System, DB, Tx, or raw SQL capabilities.
func Query[Resolver any](_ *GraphQLSchema, _ string, _ Resolver)    {}
func Mutation[Resolver any](_ *GraphQLSchema, _ string, _ Resolver) {}

type maskedDependencyError struct {
	field      string
	dependency string
}

func (value maskedDependencyError) Error() string {
	return fmt.Sprintf("computed field %s cannot read masked dependency %s", value.field, value.dependency)
}

// MaskedDependency is the stable resolver error for a dependency which the
// caller's field policy withheld. It does not grant access to the private row.
func MaskedDependency(field, dependency string) error {
	return maskedDependencyError{field: field, dependency: dependency}
}
