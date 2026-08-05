// Package golem exposes compile-time declaration shells and generated-handle
// types. DefineSchema and model declaration calls are statically interpreted by
// the compiler; executing these functions does not construct a runtime schema.
package golem

import "time"

type Schema struct{ _ schemaMarker }
type schemaMarker struct{}

type Provider string

const (
	SQLite     Provider = "sqlite"
	PostgreSQL Provider = "postgresql"
)

func SchemaName(_ *Schema, _ string)     {}
func Actor[A any](_ *Schema)             {}
func Model[M any](_ *Schema)             {}
func Providers(_ *Schema, _ ...Provider) {}

type (
	// Descriptor IDs are fixed-width values rather than string aliases. The
	// compiler IR owns hexadecimal serialization; generated Go emits array
	// literals and application code cannot inject arbitrary string keys.
	ModelID    [16]byte
	FieldID    [16]byte
	RelationID [16]byte
)

// ModelDescriptor is the immutable typed anchor emitted once for each model.
// It stores stable logical scan/write order, identity, and relation metadata;
// relation targets remain IDs and are resolved only after package registries
// are composed, so generated package initialization cannot form pointer cycles.
type ModelDescriptor[M any] struct {
	metadata ModelMetadata
	_        func() M
}

func GeneratedModelDescriptor[M any](id ModelID, shape GeneratedModelShape) ModelDescriptor[M] {
	metadata := ModelMetadata{id: id, scanFields: append([]FieldID(nil), shape.scanFields...), writeFields: append([]FieldID(nil), shape.writeFields...), identities: cloneIdentities(shape.identities), relations: append([]RelationMetadata(nil), shape.relations...)}
	return ModelDescriptor[M]{metadata: metadata}
}

func (descriptor ModelDescriptor[M]) Metadata() ModelMetadata { return descriptor.metadata.clone() }

// Stable scalar declaration types are intentionally representation-opaque in
// P1. Later runtime phases own value parsing and database codecs.
type UUID [16]byte

type Decimal struct {
	coefficient int64
	scale       uint8
}

type Date struct {
	year  int16
	month uint8
	day   uint8
}

type Time struct {
	microseconds int64
}

type JSON[T any] struct {
	raw         []byte
	typeWitness func() T
}

type List[T any] []T

type Null[T any] struct {
	Value T
	Valid bool
}

type ModelSpec[M any] struct{ _ func() M }
type EnumSpec[E ~string] struct{ _ func() E }

type ModelOption[M any] interface{ modelOption(M) }
type modelOption[M any] struct{ _ func() M }

func (modelOption[M]) modelOption(M) {}

func DefineModel[M any](_ ...ModelOption[M]) ModelSpec[M] { return ModelSpec[M]{} }

type EnumValueSpec[E ~string] struct{ _ func() E }

func EnumValue[E ~string](_ E) EnumValueSpec[E] { return EnumValueSpec[E]{} }
func DefineEnum[E ~string](_ ...EnumValueSpec[E]) EnumSpec[E] {
	return EnumSpec[E]{}
}

type ReferentialAction string

const (
	NoAction   ReferentialAction = "noAction"
	Restrict   ReferentialAction = "restrict"
	Cascade    ReferentialAction = "cascade"
	SetNull    ReferentialAction = "setNull"
	SetDefault ReferentialAction = "setDefault"
)

type GeneratedStorage string

const (
	Stored  GeneratedStorage = "stored"
	Virtual GeneratedStorage = "virtual"
)

// Predicate is a type-level shell. P2 owns its representation and semantics.
type Predicate[M any] struct{ _ func() M }

func All[M any]() Predicate[M]                          { return Predicate[M]{} }
func None[M any]() Predicate[M]                         { return Predicate[M]{} }
func And[M any](_ ...Predicate[M]) Predicate[M]         { return Predicate[M]{} }
func Or[M any](_ ...Predicate[M]) Predicate[M]          { return Predicate[M]{} }
func Not[M any](_ Predicate[M]) Predicate[M]            { return Predicate[M]{} }
func (Predicate[M]) And(_ ...Predicate[M]) Predicate[M] { return Predicate[M]{} }
func (Predicate[M]) Or(_ ...Predicate[M]) Predicate[M]  { return Predicate[M]{} }
func (Predicate[M]) Not() Predicate[M]                  { return Predicate[M]{} }

// SchemaExpr and SchemaPredicate are compile-time-only advanced schema nodes.
// They are deliberately distinct from Predicate, which belongs to P2 policy.
type SchemaExpr[M any, V any] struct{ _ func(M) V }
type SchemaPredicate[M any] struct{ _ func() M }

// SchemaValue is sealed to generated scalar handles and schema expressions.
// It lets closed functions such as Lower accept either without accepting raw
// strings, SQL, callbacks, or arbitrary application values.
type SchemaValue[M any, V any] interface{ schemaValue(M, V) }

func (SchemaExpr[M, V]) schemaValue(M, V)                              {}
func (SchemaExpr[M, V]) Eq(_ V) SchemaPredicate[M]                     { return SchemaPredicate[M]{} }
func (SchemaExpr[M, V]) Ne(_ V) SchemaPredicate[M]                     { return SchemaPredicate[M]{} }
func (SchemaExpr[M, V]) LT(_ V) SchemaPredicate[M]                     { return SchemaPredicate[M]{} }
func (SchemaExpr[M, V]) LTE(_ V) SchemaPredicate[M]                    { return SchemaPredicate[M]{} }
func (SchemaExpr[M, V]) GT(_ V) SchemaPredicate[M]                     { return SchemaPredicate[M]{} }
func (SchemaExpr[M, V]) GTE(_ V) SchemaPredicate[M]                    { return SchemaPredicate[M]{} }
func (SchemaExpr[M, V]) IsNull() SchemaPredicate[M]                    { return SchemaPredicate[M]{} }
func (SchemaExpr[M, V]) IsNotNull() SchemaPredicate[M]                 { return SchemaPredicate[M]{} }
func (SchemaExpr[M, V]) Add(_ SchemaValue[M, V]) SchemaExpr[M, V]      { return SchemaExpr[M, V]{} }
func (SchemaExpr[M, V]) Sub(_ SchemaValue[M, V]) SchemaExpr[M, V]      { return SchemaExpr[M, V]{} }
func (SchemaExpr[M, V]) Mul(_ SchemaValue[M, V]) SchemaExpr[M, V]      { return SchemaExpr[M, V]{} }
func (SchemaExpr[M, V]) Div(_ SchemaValue[M, V]) SchemaExpr[M, V]      { return SchemaExpr[M, V]{} }
func (SchemaExpr[M, V]) Mod(_ SchemaValue[M, V]) SchemaExpr[M, V]      { return SchemaExpr[M, V]{} }
func (SchemaPredicate[M]) Or(_ SchemaPredicate[M]) SchemaPredicate[M]  { return SchemaPredicate[M]{} }
func (SchemaPredicate[M]) And(_ SchemaPredicate[M]) SchemaPredicate[M] { return SchemaPredicate[M]{} }
func (SchemaPredicate[M]) Not() SchemaPredicate[M]                     { return SchemaPredicate[M]{} }

type SchemaCast[From any, To any] struct{ _ func(From) To }

var (
	Int16ToInt32  SchemaCast[int16, int32]
	Int16ToInt64  SchemaCast[int16, int64]
	Int32ToInt64  SchemaCast[int32, int64]
	Int64ToString SchemaCast[int64, string]
)

func SchemaValueOf[M any, V any](_ V) SchemaExpr[M, V]             { return SchemaExpr[M, V]{} }
func Lower[M any, V ~string](_ SchemaValue[M, V]) SchemaExpr[M, V] { return SchemaExpr[M, V]{} }
func Upper[M any, V ~string](_ SchemaValue[M, V]) SchemaExpr[M, V] { return SchemaExpr[M, V]{} }
func Length[M any, V ~string | ~[]byte](_ SchemaValue[M, V]) SchemaExpr[M, int64] {
	return SchemaExpr[M, int64]{}
}
func Coalesce[M any, V any](_ ...SchemaValue[M, V]) SchemaExpr[M, V] {
	return SchemaExpr[M, V]{}
}
func Cast[M any, From any, To any](_ SchemaValue[M, From], _ SchemaCast[From, To]) SchemaExpr[M, To] {
	return SchemaExpr[M, To]{}
}

type Column[M any] interface{ columnModel(M) }

// ScalarColumn is sealed to generated scalar, list, bytes, and opaque field
// handles. The value type is retained for schema expressions and later typed
// operation payloads without reflection.
type ScalarColumn[M any, V any] interface {
	Column[M]
	schemaValue(M, V)
	Expr() SchemaExpr[M, V]
}

type fieldCore[M any, V any] struct {
	id FieldID
	_  func(M) V
}

func (fieldCore[M, V]) columnModel(M)          {}
func (fieldCore[M, V]) schemaValue(M, V)       {}
func (fieldCore[M, V]) Expr() SchemaExpr[M, V] { return SchemaExpr[M, V]{} }

// EqualValue is the closed portable V1 operand set for equality and membership.
// Bytes and scalar lists have distinct handles because slices are not scalar
// values and require different operator semantics.
type EqualValue interface {
	~bool | ~int16 | ~int32 | ~int64 | ~float32 | ~float64 | ~string |
		UUID | Decimal | Date | Time | time.Time
}

// OrderedValue is the portable V1 operand set with a total provider-neutral
// ordering. Boolean and UUID fields are deliberately excluded.
type OrderedValue interface {
	~int16 | ~int32 | ~int64 | ~float32 | ~float64 | ~string |
		Decimal | Date | Time | time.Time
}

type ListElement interface {
	~bool | ~int16 | ~int32 | ~int64 | ~float32 | ~float64 | ~string |
		UUID | Decimal | Date | Time | time.Time
}

type EqualField[M any, V EqualValue] struct{ fieldCore[M, V] }

func GeneratedEqualField[M any, V EqualValue](id FieldID) EqualField[M, V] {
	return EqualField[M, V]{fieldCore: fieldCore[M, V]{id: id}}
}

func (EqualField[M, V]) Eq(_ V) Predicate[M]       { return Predicate[M]{} }
func (EqualField[M, V]) Ne(_ V) Predicate[M]       { return Predicate[M]{} }
func (EqualField[M, V]) In(_ ...V) Predicate[M]    { return Predicate[M]{} }
func (EqualField[M, V]) NotIn(_ ...V) Predicate[M] { return Predicate[M]{} }

type OrderedField[M any, V OrderedValue] struct{ EqualField[M, V] }

func GeneratedOrderedField[M any, V OrderedValue](id FieldID) OrderedField[M, V] {
	return OrderedField[M, V]{EqualField: GeneratedEqualField[M, V](id)}
}

func (OrderedField[M, V]) LT(_ V) Predicate[M]  { return Predicate[M]{} }
func (OrderedField[M, V]) LTE(_ V) Predicate[M] { return Predicate[M]{} }
func (OrderedField[M, V]) GT(_ V) Predicate[M]  { return Predicate[M]{} }
func (OrderedField[M, V]) GTE(_ V) Predicate[M] { return Predicate[M]{} }

type TextField[M any, V ~string] struct{ OrderedField[M, V] }

func GeneratedTextField[M any, V ~string](id FieldID) TextField[M, V] {
	return TextField[M, V]{OrderedField: GeneratedOrderedField[M, V](id)}
}

func (TextField[M, V]) Contains(_ V) Predicate[M]   { return Predicate[M]{} }
func (TextField[M, V]) StartsWith(_ V) Predicate[M] { return Predicate[M]{} }
func (TextField[M, V]) EndsWith(_ V) Predicate[M]   { return Predicate[M]{} }

type ListField[M any, E ListElement] struct{ fieldCore[M, List[E]] }

func GeneratedListField[M any, E ListElement](id FieldID) ListField[M, E] {
	return ListField[M, E]{fieldCore: fieldCore[M, List[E]]{id: id}}
}

func (ListField[M, E]) Has(_ E) Predicate[M]         { return Predicate[M]{} }
func (ListField[M, E]) HasEvery(_ ...E) Predicate[M] { return Predicate[M]{} }
func (ListField[M, E]) HasSome(_ ...E) Predicate[M]  { return Predicate[M]{} }
func (ListField[M, E]) IsEmpty(_ bool) Predicate[M]  { return Predicate[M]{} }
func (ListField[M, E]) Eq(_ List[E]) Predicate[M]    { return Predicate[M]{} }

type BytesField[M any] struct{ fieldCore[M, []byte] }

func GeneratedBytesField[M any](id FieldID) BytesField[M] {
	return BytesField[M]{fieldCore: fieldCore[M, []byte]{id: id}}
}

func (BytesField[M]) Eq(_ []byte) Predicate[M]       { return Predicate[M]{} }
func (BytesField[M]) Ne(_ []byte) Predicate[M]       { return Predicate[M]{} }
func (BytesField[M]) In(_ ...[]byte) Predicate[M]    { return Predicate[M]{} }
func (BytesField[M]) NotIn(_ ...[]byte) Predicate[M] { return Predicate[M]{} }

type OpaqueField[M any, V any] struct{ fieldCore[M, V] }

func GeneratedOpaqueField[M any, V any](id FieldID) OpaqueField[M, V] {
	return OpaqueField[M, V]{fieldCore: fieldCore[M, V]{id: id}}
}

type NullableEqualField[M any, V EqualValue] struct{ EqualField[M, V] }

func GeneratedNullableEqualField[M any, V EqualValue](id FieldID) NullableEqualField[M, V] {
	return NullableEqualField[M, V]{EqualField: GeneratedEqualField[M, V](id)}
}

func (NullableEqualField[M, V]) IsNull() Predicate[M]    { return Predicate[M]{} }
func (NullableEqualField[M, V]) IsNotNull() Predicate[M] { return Predicate[M]{} }

type NullableOrderedField[M any, V OrderedValue] struct{ OrderedField[M, V] }

func GeneratedNullableOrderedField[M any, V OrderedValue](id FieldID) NullableOrderedField[M, V] {
	return NullableOrderedField[M, V]{OrderedField: GeneratedOrderedField[M, V](id)}
}

func (NullableOrderedField[M, V]) IsNull() Predicate[M]    { return Predicate[M]{} }
func (NullableOrderedField[M, V]) IsNotNull() Predicate[M] { return Predicate[M]{} }

type NullableTextField[M any, V ~string] struct{ TextField[M, V] }

func GeneratedNullableTextField[M any, V ~string](id FieldID) NullableTextField[M, V] {
	return NullableTextField[M, V]{TextField: GeneratedTextField[M, V](id)}
}

func (NullableTextField[M, V]) IsNull() Predicate[M]    { return Predicate[M]{} }
func (NullableTextField[M, V]) IsNotNull() Predicate[M] { return Predicate[M]{} }

type NullableListField[M any, E ListElement] struct{ ListField[M, E] }

func GeneratedNullableListField[M any, E ListElement](id FieldID) NullableListField[M, E] {
	return NullableListField[M, E]{ListField: GeneratedListField[M, E](id)}
}

func (NullableListField[M, E]) IsNull() Predicate[M]    { return Predicate[M]{} }
func (NullableListField[M, E]) IsNotNull() Predicate[M] { return Predicate[M]{} }

type NullableBytesField[M any] struct{ BytesField[M] }

func GeneratedNullableBytesField[M any](id FieldID) NullableBytesField[M] {
	return NullableBytesField[M]{BytesField: GeneratedBytesField[M](id)}
}

func (NullableBytesField[M]) IsNull() Predicate[M]    { return Predicate[M]{} }
func (NullableBytesField[M]) IsNotNull() Predicate[M] { return Predicate[M]{} }

type NullableOpaqueField[M any, V any] struct{ OpaqueField[M, V] }

func GeneratedNullableOpaqueField[M any, V any](id FieldID) NullableOpaqueField[M, V] {
	return NullableOpaqueField[M, V]{OpaqueField: GeneratedOpaqueField[M, V](id)}
}

func (NullableOpaqueField[M, V]) IsNull() Predicate[M]    { return Predicate[M]{} }
func (NullableOpaqueField[M, V]) IsNotNull() Predicate[M] { return Predicate[M]{} }

type ToOne[M any, R any] struct {
	id RelationID
	_  func(M) R
}

type ToMany[M any, R any] struct {
	id RelationID
	_  func(M) R
}

func GeneratedToOne[M any, R any](id RelationID) ToOne[M, R] {
	return ToOne[M, R]{id: id}
}

func GeneratedToMany[M any, R any](id RelationID) ToMany[M, R] {
	return ToMany[M, R]{id: id}
}

func (ToOne[M, R]) Is(_ Predicate[R]) Predicate[M]     { return Predicate[M]{} }
func (ToMany[M, R]) Some(_ Predicate[R]) Predicate[M]  { return Predicate[M]{} }
func (ToOne[M, R]) IsNot(_ Predicate[R]) Predicate[M]  { return Predicate[M]{} }
func (ToOne[M, R]) IsNull() Predicate[M]               { return Predicate[M]{} }
func (ToOne[M, R]) IsNotNull() Predicate[M]            { return Predicate[M]{} }
func (ToMany[M, R]) Every(_ Predicate[R]) Predicate[M] { return Predicate[M]{} }
func (ToMany[M, R]) None(_ Predicate[R]) Predicate[M]  { return Predicate[M]{} }

type IndexKey[M any] struct{ _ func() M }

func IndexColumn[M any, V any](_ ScalarColumn[M, V]) IndexKey[M] {
	return IndexKey[M]{}
}

func IndexExpr[M any, V any](_ SchemaValue[M, V]) IndexKey[M] { return IndexKey[M]{} }

func (key IndexKey[M]) Desc() IndexKey[M] { return key }

type IndexSpec[M any] struct{ _ func() M }

func Index[M any](_ string) IndexSpec[M]                          { return IndexSpec[M]{} }
func (spec IndexSpec[M]) Keys(_ ...IndexKey[M]) IndexSpec[M]      { return spec }
func (spec IndexSpec[M]) Where(_ SchemaPredicate[M]) IndexSpec[M] { return spec }
func (IndexSpec[M]) modelOption(M)                                {}

func PrimaryKey[M any](_ string, _ ...Column[M]) ModelOption[M]  { return modelOption[M]{} }
func Unique[M any](_ string, _ ...Column[M]) ModelOption[M]      { return modelOption[M]{} }
func Check[M any](_ string, _ SchemaPredicate[M]) ModelOption[M] { return modelOption[M]{} }

// ForProvider scopes otherwise typed model declarations to one registered
// provider. The compiler interprets and validates its body; this P1 shell does
// not execute provider-specific behavior.
func ForProvider[M any](_ Provider, _ ...ModelOption[M]) ModelOption[M] {
	return modelOption[M]{}
}

type RelationOptionSpec[M any] struct{ _ func() M }

func RelationOptions[M any, R any](_ ToOne[M, R]) RelationOptionSpec[M] {
	return RelationOptionSpec[M]{}
}

func (spec RelationOptionSpec[M]) OnUpdate(_ ReferentialAction) RelationOptionSpec[M] {
	return spec
}
func (spec RelationOptionSpec[M]) OnDelete(_ ReferentialAction) RelationOptionSpec[M] {
	return spec
}
func (RelationOptionSpec[M]) modelOption(M) {}

type GeneratedSpec[M any] struct{ _ func() M }

func Generated[M any, V any](_ ScalarColumn[M, V], _ SchemaExpr[M, V], _ GeneratedStorage) ModelOption[M] {
	return modelOption[M]{}
}

// Rules is an opaque type-checking shell. P2 owns ordered rule behavior.
type Rules[M any] struct{ _ func() M }

func NewRules[M any]() *Rules[M]           { return &Rules[M]{} }
func (*Rules[M]) CanRead(_ Predicate[M])   {}
func (*Rules[M]) CanCreate(_ Predicate[M]) {}
func (*Rules[M]) CanUpdate(_ Predicate[M]) {}
func (*Rules[M]) CanDelete(_ Predicate[M]) {}
