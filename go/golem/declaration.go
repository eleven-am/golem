// Package golem exposes compile-time declaration shells and generated-handle
// types. DefineSchema and model declaration calls are statically interpreted by
// the compiler; executing these functions does not construct a runtime schema.
package golem

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
// It stores only a stable ID; relation resolution belongs to the registry built
// after every package bootstrap has been decoded.
type ModelDescriptor[M any] struct {
	id ModelID
	_  func() M
}

func GeneratedModelDescriptor[M any](id ModelID) ModelDescriptor[M] {
	return ModelDescriptor[M]{id: id}
}

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

type ScalarField[M any, V any] struct {
	id FieldID
	_  func(M) V
}

func (ScalarField[M, V]) columnModel(M)          {}
func (ScalarField[M, V]) schemaValue(M, V)       {}
func (ScalarField[M, V]) Expr() SchemaExpr[M, V] { return SchemaExpr[M, V]{} }

func GeneratedScalarField[M any, V any](id FieldID) ScalarField[M, V] {
	return ScalarField[M, V]{id: id}
}

func (ScalarField[M, V]) Eq(_ V) Predicate[M]        { return Predicate[M]{} }
func (ScalarField[M, V]) Ne(_ V) Predicate[M]        { return Predicate[M]{} }
func (ScalarField[M, V]) GTE(_ V) Predicate[M]       { return Predicate[M]{} }
func (ScalarField[M, V]) IsNull() Predicate[M]       { return Predicate[M]{} }
func (Predicate[M]) Or(_ Predicate[M]) Predicate[M]  { return Predicate[M]{} }
func (Predicate[M]) And(_ Predicate[M]) Predicate[M] { return Predicate[M]{} }

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

func (ToOne[M, R]) Is(_ Predicate[R]) Predicate[M]    { return Predicate[M]{} }
func (ToMany[M, R]) Some(_ Predicate[R]) Predicate[M] { return Predicate[M]{} }

type IndexKey[M any] struct{ _ func() M }

func IndexColumn[M any, V any](_ ScalarField[M, V]) IndexKey[M] {
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

func Generated[M any, V any](_ ScalarField[M, V], _ SchemaExpr[M, V], _ GeneratedStorage) ModelOption[M] {
	return modelOption[M]{}
}

// Rules is an opaque type-checking shell. P2 owns ordered rule behavior.
type Rules[M any] struct{ _ func() M }

func NewRules[M any]() *Rules[M]           { return &Rules[M]{} }
func (*Rules[M]) CanRead(_ Predicate[M])   {}
func (*Rules[M]) CanCreate(_ Predicate[M]) {}
func (*Rules[M]) CanUpdate(_ Predicate[M]) {}
func (*Rules[M]) CanDelete(_ Predicate[M]) {}
