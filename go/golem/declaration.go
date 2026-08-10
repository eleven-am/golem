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

// EmbeddingSpace declares one schema-wide embedding space. Dimensions are
// frozen into reviewed provider migrations; runtime configuration must supply
// an embedding.Provider with the same dimensions.
func EmbeddingSpace(_ *Schema, _ string, _ uint16) {}

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

// SemanticIndex declares a named semantic projection over ordered text fields.
// The embedding space is declared once in DefineSchema. Golem owns projection
// storage, refresh, similarity queries, and authorization.
func SemanticIndex[M any](_, _ string, _ ...Column[M]) ModelOption[M] { return modelOption[M]{} }

type GraphQLOperation string

const (
	GraphQLFindOne         GraphQLOperation = "findOne"
	GraphQLFindMany        GraphQLOperation = "findMany"
	GraphQLCreate          GraphQLOperation = "create"
	GraphQLUpdate          GraphQLOperation = "update"
	GraphQLUpsert          GraphQLOperation = "upsert"
	GraphQLDelete          GraphQLOperation = "delete"
	GraphQLUpdateMany      GraphQLOperation = "updateMany"
	GraphQLDeleteMany      GraphQLOperation = "deleteMany"
	GraphQLAggregate       GraphQLOperation = "aggregate"
	GraphQLGroupBy         GraphQLOperation = "groupBy"
	GraphQLRelationGroupBy GraphQLOperation = "relationGroupBy"
)

type GraphQLRootNames struct {
	FindOne         string
	FindMany        string
	Create          string
	Update          string
	Upsert          string
	Delete          string
	UpdateMany      string
	DeleteMany      string
	Aggregate       string
	GroupBy         string
	RelationGroupBy string
	Events          string
}

// Analytics declarations are compile-time-only shells. Their closed generic
// types let the compiler reject foreign-model fields before interpretation.
type AnalyticsOption[M any] interface{ analyticsOption(M) }
type analyticsOption[M any] struct{ _ func() M }

func (analyticsOption[M]) analyticsOption(M) {}

type RelationDimensionSpec[M any] struct{ _ func() M }
type RelationDimensionPath[M any, V any] struct{ _ func(M) V }

func Analytics[M any](_ ...AnalyticsOption[M]) ModelOption[M]      { return modelOption[M]{} }
func AnalyticsDimensions[M any](_ ...Column[M]) AnalyticsOption[M] { return analyticsOption[M]{} }
func AnalyticsMeasures[M any](_ ...Column[M]) AnalyticsOption[M]   { return analyticsOption[M]{} }
func AnalyticsRelationDimensions[M any](_ ...RelationDimensionSpec[M]) AnalyticsOption[M] {
	return analyticsOption[M]{}
}
func NamedRelationDimension[M, V any](_ string, _ RelationDimensionPath[M, V]) RelationDimensionSpec[M] {
	return RelationDimensionSpec[M]{}
}
func DimensionField[M, V any](_ ScalarColumn[M, V]) RelationDimensionPath[M, V] {
	return RelationDimensionPath[M, V]{}
}
func Via[M, R, V any](_ ToOneRelation[M, R], _ RelationDimensionPath[R, V]) RelationDimensionPath[M, V] {
	return RelationDimensionPath[M, V]{}
}
func AnalyticsLimits[M any](_, _ int) AnalyticsOption[M] { return analyticsOption[M]{} }
func ScopedReads[M any]() ModelOption[M]                 { return modelOption[M]{} }

type GraphQLOption struct{ _ graphqlOptionMarker }
type graphqlOptionMarker struct{}

func GraphQL[M any](_ ...GraphQLOption) ModelOption[M]      { return modelOption[M]{} }
func GraphQLOperations(_ ...GraphQLOperation) GraphQLOption { return GraphQLOption{} }
func GraphQLPlural(_ string) GraphQLOption                  { return GraphQLOption{} }
func GraphQLRoots(_ GraphQLRootNames) GraphQLOption         { return GraphQLOption{} }
func GraphQLPageSizes(_, _ int) GraphQLOption               { return GraphQLOption{} }
func GraphQLHidden() GraphQLOption                          { return GraphQLOption{} }

// GraphQLHookOwned omits the named scalar fields from GraphQL create inputs.
// They remain ordinary typed programmatic create fields so a recognized
// BeforeCreate hook can populate them with SetCreate before mutation planning.
// When a named field participates in a canonical belongs-to key, the complete
// non-null key must be hook-owned and that relation is omitted from create
// inputs too.
func GraphQLHookOwned[M any](_ ...Column[M]) GraphQLOption { return GraphQLOption{} }

// Subscriptions opts one model into durable event capture, generated typed
// events, and its GraphQL subscription root. It is a compile-time declaration;
// models that omit it remain subscription-disabled.
func Subscriptions[M any]() ModelOption[M] { return modelOption[M]{} }

type EnumValueOption struct{ _ enumValueOptionMarker }
type enumValueOptionMarker struct{}
type EnumValueSpec[E ~string] struct{ _ func() E }

func GraphQLValue(_ string) EnumValueOption                           { return EnumValueOption{} }
func EnumValue[E ~string](_ E, _ ...EnumValueOption) EnumValueSpec[E] { return EnumValueSpec[E]{} }
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

// Predicate is an immutable, model-typed policy expression. Its representation
// remains private; only generated handles and the closed combinators below can
// construct nodes.
type Predicate[M any] struct {
	node *predicateNode
	_    func() M
}

func All[M any]() Predicate[M]  { return predicateConstant[M](true) }
func None[M any]() Predicate[M] { return predicateConstant[M](false) }
func And[M any](values ...Predicate[M]) Predicate[M] {
	return predicateLogical(frozenOperatorAnd, values)
}
func Or[M any](values ...Predicate[M]) Predicate[M] {
	return predicateLogical(frozenOperatorOr, values)
}
func Not[M any](value Predicate[M]) Predicate[M] { return predicateNot(value) }
func (value Predicate[M]) And(more ...Predicate[M]) Predicate[M] {
	return And(append([]Predicate[M]{value}, more...)...)
}
func (value Predicate[M]) Or(more ...Predicate[M]) Predicate[M] {
	return Or(append([]Predicate[M]{value}, more...)...)
}
func (value Predicate[M]) Not() Predicate[M] { return Not(value) }

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

// Field is the sealed identity shared by every generated scalar and relation
// handle. Application packages may accept Field[M] values, but cannot invent
// them because both methods are unexported.
type Field[M any] interface {
	fieldModel(M)
	fieldIdentity() FieldID
}

type Column[M any] interface {
	Field[M]
	columnModel(M)
}

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

func (fieldCore[M, V]) columnModel(M)                {}
func (fieldCore[M, V]) fieldModel(M)                 {}
func (field fieldCore[M, V]) fieldIdentity() FieldID { return field.id }
func (fieldCore[M, V]) schemaValue(M, V)             {}
func (fieldCore[M, V]) Expr() SchemaExpr[M, V]       { return SchemaExpr[M, V]{} }
func (field fieldCore[M, V]) readSelection(M) readSelectionNode {
	return readSelectionNode{kind: readSelectionScalar, field: field.id}
}

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

func (field EqualField[M, V]) Eq(value V) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorEq, scalarOperand(value))
}
func (field EqualField[M, V]) Ne(value V) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorNe, scalarOperand(value))
}
func (field EqualField[M, V]) In(values ...V) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorIn, scalarOperands(values))
}
func (field EqualField[M, V]) NotIn(values ...V) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorNotIn, scalarOperands(values))
}
func (field EqualField[M, V]) Asc() OrderTerm[M] {
	return orderTerm[M](field.fieldIdentity(), SortAscending)
}
func (field EqualField[M, V]) Desc() OrderTerm[M] {
	return orderTerm[M](field.fieldIdentity(), SortDescending)
}

type OrderedField[M any, V OrderedValue] struct{ EqualField[M, V] }

func GeneratedOrderedField[M any, V OrderedValue](id FieldID) OrderedField[M, V] {
	return OrderedField[M, V]{EqualField: GeneratedEqualField[M, V](id)}
}

func (field OrderedField[M, V]) LT(value V) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorLT, scalarOperand(value))
}
func (field OrderedField[M, V]) LTE(value V) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorLTE, scalarOperand(value))
}
func (field OrderedField[M, V]) GT(value V) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorGT, scalarOperand(value))
}
func (field OrderedField[M, V]) GTE(value V) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorGTE, scalarOperand(value))
}

type TextField[M any, V ~string] struct{ OrderedField[M, V] }

func GeneratedTextField[M any, V ~string](id FieldID) TextField[M, V] {
	return TextField[M, V]{OrderedField: GeneratedOrderedField[M, V](id)}
}

func (field TextField[M, V]) Contains(value V) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorContains, stringOperand(string(value)))
}
func (field TextField[M, V]) StartsWith(value V) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorStartsWith, stringOperand(string(value)))
}
func (field TextField[M, V]) EndsWith(value V) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorEndsWith, stringOperand(string(value)))
}

type ComparisonMode interface{ comparisonMode() FrozenComparisonMode }
type comparisonModeValue FrozenComparisonMode

func (mode comparisonModeValue) comparisonMode() FrozenComparisonMode {
	return FrozenComparisonMode(mode)
}
func DefaultComparison() ComparisonMode { return comparisonModeValue(FrozenComparisonSensitive) }
func ASCIIInsensitive() ComparisonMode  { return comparisonModeValue(FrozenComparisonASCIIInsensitive) }

type ModeTextField[M any, V ~string] struct{ TextField[M, V] }
type NullableModeTextField[M any, V ~string] struct{ NullableTextField[M, V] }
type TextComparison[M any, V ~string] struct {
	field FieldID
	mode  FrozenComparisonMode
}

func GeneratedModeTextField[M any, V ~string](id FieldID) ModeTextField[M, V] {
	return ModeTextField[M, V]{TextField: GeneratedTextField[M, V](id)}
}
func GeneratedNullableModeTextField[M any, V ~string](id FieldID) NullableModeTextField[M, V] {
	return NullableModeTextField[M, V]{NullableTextField: GeneratedNullableTextField[M, V](id)}
}
func (field ModeTextField[M, V]) Compare(mode ComparisonMode) TextComparison[M, V] {
	return textComparison[M, V](field.fieldIdentity(), mode)
}
func (field NullableModeTextField[M, V]) Compare(mode ComparisonMode) TextComparison[M, V] {
	return textComparison[M, V](field.fieldIdentity(), mode)
}
func textComparison[M any, V ~string](field FieldID, mode ComparisonMode) TextComparison[M, V] {
	if mode == nil {
		return TextComparison[M, V]{field: field}
	}
	return TextComparison[M, V]{field: field, mode: mode.comparisonMode()}
}
func (field TextComparison[M, V]) Eq(value V) Predicate[M] {
	return predicateScalarMode[M](field.field, frozenOperatorEq, field.mode, stringOperand(string(value)))
}
func (field TextComparison[M, V]) Ne(value V) Predicate[M] {
	return predicateScalarMode[M](field.field, frozenOperatorNe, field.mode, stringOperand(string(value)))
}
func (field TextComparison[M, V]) In(values ...V) Predicate[M] {
	return predicateScalarMode[M](field.field, frozenOperatorIn, field.mode, scalarOperands(values))
}
func (field TextComparison[M, V]) NotIn(values ...V) Predicate[M] {
	return predicateScalarMode[M](field.field, frozenOperatorNotIn, field.mode, scalarOperands(values))
}
func (field TextComparison[M, V]) LT(value V) Predicate[M] {
	return predicateScalarMode[M](field.field, frozenOperatorLT, field.mode, stringOperand(string(value)))
}
func (field TextComparison[M, V]) LTE(value V) Predicate[M] {
	return predicateScalarMode[M](field.field, frozenOperatorLTE, field.mode, stringOperand(string(value)))
}
func (field TextComparison[M, V]) GT(value V) Predicate[M] {
	return predicateScalarMode[M](field.field, frozenOperatorGT, field.mode, stringOperand(string(value)))
}
func (field TextComparison[M, V]) GTE(value V) Predicate[M] {
	return predicateScalarMode[M](field.field, frozenOperatorGTE, field.mode, stringOperand(string(value)))
}
func (field TextComparison[M, V]) Contains(value V) Predicate[M] {
	return predicateScalarMode[M](field.field, frozenOperatorContains, field.mode, stringOperand(string(value)))
}
func (field TextComparison[M, V]) StartsWith(value V) Predicate[M] {
	return predicateScalarMode[M](field.field, frozenOperatorStartsWith, field.mode, stringOperand(string(value)))
}
func (field TextComparison[M, V]) EndsWith(value V) Predicate[M] {
	return predicateScalarMode[M](field.field, frozenOperatorEndsWith, field.mode, stringOperand(string(value)))
}

type ListField[M any, E ListElement] struct{ fieldCore[M, List[E]] }

func GeneratedListField[M any, E ListElement](id FieldID) ListField[M, E] {
	return ListField[M, E]{fieldCore: fieldCore[M, List[E]]{id: id}}
}

func (field ListField[M, E]) Has(value E) Predicate[M] {
	return predicateList[M](field.fieldIdentity(), frozenOperatorListHas, scalarOperand(value))
}
func (field ListField[M, E]) HasEvery(values ...E) Predicate[M] {
	return predicateList[M](field.fieldIdentity(), frozenOperatorListHasEvery, scalarOperands(values))
}
func (field ListField[M, E]) HasSome(values ...E) Predicate[M] {
	return predicateList[M](field.fieldIdentity(), frozenOperatorListHasSome, scalarOperands(values))
}
func (field ListField[M, E]) IsEmpty(value bool) Predicate[M] {
	return predicateList[M](field.fieldIdentity(), frozenOperatorListIsEmpty, flagOperand(value))
}
func (field ListField[M, E]) Eq(values List[E]) Predicate[M] {
	return predicateList[M](field.fieldIdentity(), frozenOperatorListEq, scalarOperands([]E(values)))
}

type BytesField[M any] struct{ fieldCore[M, []byte] }

func GeneratedBytesField[M any](id FieldID) BytesField[M] {
	return BytesField[M]{fieldCore: fieldCore[M, []byte]{id: id}}
}

func (field BytesField[M]) Eq(value []byte) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorEq, bytesOperand(value))
}
func (field BytesField[M]) Ne(value []byte) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorNe, bytesOperand(value))
}
func (field BytesField[M]) In(values ...[]byte) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorIn, bytesOperands(values))
}
func (field BytesField[M]) NotIn(values ...[]byte) Predicate[M] {
	return predicateScalar[M](field.fieldIdentity(), frozenOperatorNotIn, bytesOperands(values))
}

type OpaqueField[M any, V any] struct{ fieldCore[M, V] }

func GeneratedOpaqueField[M any, V any](id FieldID) OpaqueField[M, V] {
	return OpaqueField[M, V]{fieldCore: fieldCore[M, V]{id: id}}
}

type JSONField[M any] struct{ fieldCore[M, JSON[any]] }
type NullableJSONField[M any] struct{ JSONField[M] }
type ModeJSONField[M any] struct{ JSONField[M] }
type NullableModeJSONField[M any] struct{ ModeJSONField[M] }
type JSONTarget[M any] struct {
	field FieldID
	path  JSONPath
}
type ModeJSONTarget[M any] struct{ JSONTarget[M] }
type JSONStringComparison[M any] struct {
	target JSONTarget[M]
	mode   FrozenComparisonMode
}

func GeneratedJSONField[M any](id FieldID) JSONField[M] {
	return JSONField[M]{fieldCore: fieldCore[M, JSON[any]]{id: id}}
}
func GeneratedNullableJSONField[M any](id FieldID) NullableJSONField[M] {
	return NullableJSONField[M]{JSONField: GeneratedJSONField[M](id)}
}
func GeneratedModeJSONField[M any](id FieldID) ModeJSONField[M] {
	return ModeJSONField[M]{JSONField: GeneratedJSONField[M](id)}
}
func GeneratedNullableModeJSONField[M any](id FieldID) NullableModeJSONField[M] {
	return NullableModeJSONField[M]{ModeJSONField: GeneratedModeJSONField[M](id)}
}
func (field JSONField[M]) Root() JSONTarget[M] {
	return JSONTarget[M]{field: field.fieldIdentity(), path: jsonRootPath()}
}
func (field JSONField[M]) At(path JSONPath) JSONTarget[M] {
	return JSONTarget[M]{field: field.fieldIdentity(), path: path}
}
func (field ModeJSONField[M]) Root() ModeJSONTarget[M] {
	return ModeJSONTarget[M]{JSONTarget: field.JSONField.Root()}
}
func (field ModeJSONField[M]) At(path JSONPath) ModeJSONTarget[M] {
	return ModeJSONTarget[M]{JSONTarget: field.JSONField.At(path)}
}
func (field NullableJSONField[M]) IsNull() Predicate[M] {
	return predicateJSONPresence[M](field.fieldIdentity(), frozenOperatorIsNull)
}
func (field NullableJSONField[M]) IsNotNull() Predicate[M] {
	return predicateJSONPresence[M](field.fieldIdentity(), frozenOperatorIsNotNull)
}
func (field NullableModeJSONField[M]) IsNull() Predicate[M] {
	return predicateJSONPresence[M](field.fieldIdentity(), frozenOperatorIsNull)
}
func (field NullableModeJSONField[M]) IsNotNull() Predicate[M] {
	return predicateJSONPresence[M](field.fieldIdentity(), frozenOperatorIsNotNull)
}
func (target JSONTarget[M]) Eq(value JSONEqualityOperand) Predicate[M] {
	return predicateJSON[M](target.field, target.path, frozenOperatorJSONEq, FrozenComparisonSensitive, jsonEqualityOperand(value))
}
func (target JSONTarget[M]) Ne(value JSONEqualityOperand) Predicate[M] {
	return predicateJSON[M](target.field, target.path, frozenOperatorJSONNe, FrozenComparisonSensitive, jsonEqualityOperand(value))
}
func (target JSONTarget[M]) LT(value JSONOrderedValue) Predicate[M] {
	return predicateJSON[M](target.field, target.path, frozenOperatorJSONLT, FrozenComparisonSensitive, jsonValueOperand(value))
}
func (target JSONTarget[M]) LTE(value JSONOrderedValue) Predicate[M] {
	return predicateJSON[M](target.field, target.path, frozenOperatorJSONLTE, FrozenComparisonSensitive, jsonValueOperand(value))
}
func (target JSONTarget[M]) GT(value JSONOrderedValue) Predicate[M] {
	return predicateJSON[M](target.field, target.path, frozenOperatorJSONGT, FrozenComparisonSensitive, jsonValueOperand(value))
}
func (target JSONTarget[M]) GTE(value JSONOrderedValue) Predicate[M] {
	return predicateJSON[M](target.field, target.path, frozenOperatorJSONGTE, FrozenComparisonSensitive, jsonValueOperand(value))
}
func (target JSONTarget[M]) StringContains(value string) Predicate[M] {
	return predicateJSON[M](target.field, target.path, frozenOperatorJSONStringContains, FrozenComparisonSensitive, jsonValueOperand(JSONString(value)))
}
func (target JSONTarget[M]) StringStartsWith(value string) Predicate[M] {
	return predicateJSON[M](target.field, target.path, frozenOperatorJSONStringStartsWith, FrozenComparisonSensitive, jsonValueOperand(JSONString(value)))
}
func (target JSONTarget[M]) StringEndsWith(value string) Predicate[M] {
	return predicateJSON[M](target.field, target.path, frozenOperatorJSONStringEndsWith, FrozenComparisonSensitive, jsonValueOperand(JSONString(value)))
}
func (target JSONTarget[M]) ArrayContains(value JSONValue) Predicate[M] {
	return predicateJSON[M](target.field, target.path, frozenOperatorJSONArrayContains, FrozenComparisonSensitive, jsonValueOperand(value))
}
func (target JSONTarget[M]) ArrayStartsWith(value JSONValue) Predicate[M] {
	return predicateJSON[M](target.field, target.path, frozenOperatorJSONArrayStartsWith, FrozenComparisonSensitive, jsonValueOperand(value))
}
func (target JSONTarget[M]) ArrayEndsWith(value JSONValue) Predicate[M] {
	return predicateJSON[M](target.field, target.path, frozenOperatorJSONArrayEndsWith, FrozenComparisonSensitive, jsonValueOperand(value))
}
func (target ModeJSONTarget[M]) Compare(mode ComparisonMode) JSONStringComparison[M] {
	if mode == nil {
		return JSONStringComparison[M]{target: target.JSONTarget}
	}
	return JSONStringComparison[M]{target: target.JSONTarget, mode: mode.comparisonMode()}
}
func (target JSONStringComparison[M]) Contains(value string) Predicate[M] {
	return predicateJSON[M](target.target.field, target.target.path, frozenOperatorJSONStringContains, target.mode, jsonValueOperand(JSONString(value)))
}
func (target JSONStringComparison[M]) StartsWith(value string) Predicate[M] {
	return predicateJSON[M](target.target.field, target.target.path, frozenOperatorJSONStringStartsWith, target.mode, jsonValueOperand(JSONString(value)))
}
func (target JSONStringComparison[M]) EndsWith(value string) Predicate[M] {
	return predicateJSON[M](target.target.field, target.target.path, frozenOperatorJSONStringEndsWith, target.mode, jsonValueOperand(JSONString(value)))
}

type NullableEqualField[M any, V EqualValue] struct{ EqualField[M, V] }

func GeneratedNullableEqualField[M any, V EqualValue](id FieldID) NullableEqualField[M, V] {
	return NullableEqualField[M, V]{EqualField: GeneratedEqualField[M, V](id)}
}

func (field NullableEqualField[M, V]) IsNull() Predicate[M] {
	return predicatePresence[M](field.fieldIdentity(), frozenOperatorIsNull)
}
func (field NullableEqualField[M, V]) IsNotNull() Predicate[M] {
	return predicatePresence[M](field.fieldIdentity(), frozenOperatorIsNotNull)
}

type NullableOrderedField[M any, V OrderedValue] struct{ OrderedField[M, V] }

func GeneratedNullableOrderedField[M any, V OrderedValue](id FieldID) NullableOrderedField[M, V] {
	return NullableOrderedField[M, V]{OrderedField: GeneratedOrderedField[M, V](id)}
}

func (field NullableOrderedField[M, V]) IsNull() Predicate[M] {
	return predicatePresence[M](field.fieldIdentity(), frozenOperatorIsNull)
}
func (field NullableOrderedField[M, V]) IsNotNull() Predicate[M] {
	return predicatePresence[M](field.fieldIdentity(), frozenOperatorIsNotNull)
}

type NullableTextField[M any, V ~string] struct{ TextField[M, V] }

func GeneratedNullableTextField[M any, V ~string](id FieldID) NullableTextField[M, V] {
	return NullableTextField[M, V]{TextField: GeneratedTextField[M, V](id)}
}

func (field NullableTextField[M, V]) IsNull() Predicate[M] {
	return predicatePresence[M](field.fieldIdentity(), frozenOperatorIsNull)
}
func (field NullableTextField[M, V]) IsNotNull() Predicate[M] {
	return predicatePresence[M](field.fieldIdentity(), frozenOperatorIsNotNull)
}

type NullableListField[M any, E ListElement] struct{ ListField[M, E] }

func GeneratedNullableListField[M any, E ListElement](id FieldID) NullableListField[M, E] {
	return NullableListField[M, E]{ListField: GeneratedListField[M, E](id)}
}

func (field NullableListField[M, E]) IsNull() Predicate[M] {
	return predicateList[M](field.fieldIdentity(), frozenOperatorIsNull, noOperand())
}
func (field NullableListField[M, E]) IsNotNull() Predicate[M] {
	return predicateList[M](field.fieldIdentity(), frozenOperatorIsNotNull, noOperand())
}

type NullableBytesField[M any] struct{ BytesField[M] }

func GeneratedNullableBytesField[M any](id FieldID) NullableBytesField[M] {
	return NullableBytesField[M]{BytesField: GeneratedBytesField[M](id)}
}

func (field NullableBytesField[M]) IsNull() Predicate[M] {
	return predicatePresence[M](field.fieldIdentity(), frozenOperatorIsNull)
}
func (field NullableBytesField[M]) IsNotNull() Predicate[M] {
	return predicatePresence[M](field.fieldIdentity(), frozenOperatorIsNotNull)
}

type NullableOpaqueField[M any, V any] struct{ OpaqueField[M, V] }

func GeneratedNullableOpaqueField[M any, V any](id FieldID) NullableOpaqueField[M, V] {
	return NullableOpaqueField[M, V]{OpaqueField: GeneratedOpaqueField[M, V](id)}
}

func (field NullableOpaqueField[M, V]) IsNull() Predicate[M] {
	return predicateJSONPresence[M](field.fieldIdentity(), frozenOperatorIsNull)
}
func (field NullableOpaqueField[M, V]) IsNotNull() Predicate[M] {
	return predicateJSONPresence[M](field.fieldIdentity(), frozenOperatorIsNotNull)
}

type ToOne[M any, R any] struct {
	fieldID     FieldID
	relationID  RelationID
	targetModel ModelID
	_           func(M) R
}

// ToOneRelationOption is the sealed schema-time capability accepted by
// RelationOptions. Read-capable ToOne fields and generated mutation-only
// wrappers both carry it, without giving write-only relations any predicate,
// selection, or inclusion methods.
type ToOneRelationOption[M any, R any] interface {
	relationOption(M, R)
}

// ToOneRelation is the readable, sealed forward-to-one capability accepted by
// analytical relation paths. Mutation-only schema wrappers do not implement it.
type ToOneRelation[M any, R any] interface {
	relationOption(M, R)
	toOneRelation(M, R)
}

// ToOneSchemaField is embedded only in generated mutation-only to-one
// wrappers. It exists so schema declarations can still name the relation in
// RelationOptions while the runtime field surface remains non-readable.
type ToOneSchemaField[M any, R any] struct{ _ func(M) R }

func GeneratedToOneSchemaField[M any, R any]() ToOneSchemaField[M, R] {
	return ToOneSchemaField[M, R]{}
}

func (ToOne[M, R]) relationOption(M, R)            {}
func (ToOne[M, R]) toOneRelation(M, R)             {}
func (ToOneSchemaField[M, R]) relationOption(M, R) {}

type ToMany[M any, R any] struct {
	fieldID     FieldID
	relationID  RelationID
	targetModel ModelID
	_           func(M) R
}

func GeneratedToOne[M any, R any](fieldID FieldID, relationID RelationID, target ...ModelID) ToOne[M, R] {
	var targetModel ModelID
	if len(target) == 1 {
		targetModel = target[0]
	}
	return ToOne[M, R]{fieldID: fieldID, relationID: relationID, targetModel: targetModel}
}

func GeneratedToMany[M any, R any](fieldID FieldID, relationID RelationID, target ...ModelID) ToMany[M, R] {
	var targetModel ModelID
	if len(target) == 1 {
		targetModel = target[0]
	}
	return ToMany[M, R]{fieldID: fieldID, relationID: relationID, targetModel: targetModel}
}

func (ToOne[M, R]) fieldModel(M)                  {}
func (field ToOne[M, R]) fieldIdentity() FieldID  { return field.fieldID }
func (ToMany[M, R]) fieldModel(M)                 {}
func (field ToMany[M, R]) fieldIdentity() FieldID { return field.fieldID }

func (field ToOne[M, R]) Is(value Predicate[R]) Predicate[M] {
	return predicateRelation[M](field.fieldID, field.relationID, frozenOperatorRelationIs, value.node)
}
func (field ToMany[M, R]) Some(value Predicate[R]) Predicate[M] {
	return predicateRelation[M](field.fieldID, field.relationID, frozenOperatorRelationSome, value.node)
}
func (field ToOne[M, R]) IsNot(value Predicate[R]) Predicate[M] {
	return predicateRelation[M](field.fieldID, field.relationID, frozenOperatorRelationIsNot, value.node)
}
func (field ToOne[M, R]) IsNull() Predicate[M] {
	return predicateRelation[M](field.fieldID, field.relationID, frozenOperatorRelationIsNull, nil)
}
func (field ToOne[M, R]) IsNotNull() Predicate[M] {
	return predicateRelation[M](field.fieldID, field.relationID, frozenOperatorRelationIsNotNull, nil)
}
func (field ToMany[M, R]) Every(value Predicate[R]) Predicate[M] {
	return predicateRelation[M](field.fieldID, field.relationID, frozenOperatorRelationEvery, value.node)
}
func (field ToMany[M, R]) None(value Predicate[R]) Predicate[M] {
	return predicateRelation[M](field.fieldID, field.relationID, frozenOperatorRelationNone, value.node)
}

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

func RelationOptions[M any, R any](_ ToOneRelationOption[M, R]) RelationOptionSpec[M] {
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

// Rules records policy declarations in exact source execution order. It is
// safe to snapshot through Freeze without exposing its mutable builder state.
type Rules[M any] struct {
	state rulesState
	_     func() M
}

func NewRules[M any]() *Rules[M] { return &Rules[M]{} }
func (rules *Rules[M]) CanRead(value Predicate[M]) {
	rules.appendModelRule(frozenActionRead, frozenEffectGrant, value)
}
func (rules *Rules[M]) CannotRead(value Predicate[M]) {
	rules.appendModelRule(frozenActionRead, frozenEffectDeny, value)
}
func (rules *Rules[M]) CanCreate(value Predicate[M]) {
	rules.appendModelRule(frozenActionCreate, frozenEffectGrant, value)
}
func (rules *Rules[M]) CannotCreate(value Predicate[M]) {
	rules.appendModelRule(frozenActionCreate, frozenEffectDeny, value)
}
func (rules *Rules[M]) CanUpdate(value Predicate[M]) {
	rules.appendModelRule(frozenActionUpdate, frozenEffectGrant, value)
}
func (rules *Rules[M]) CannotUpdate(value Predicate[M]) {
	rules.appendModelRule(frozenActionUpdate, frozenEffectDeny, value)
}
func (rules *Rules[M]) CanDelete(value Predicate[M]) {
	rules.appendModelRule(frozenActionDelete, frozenEffectGrant, value)
}
func (rules *Rules[M]) CannotDelete(value Predicate[M]) {
	rules.appendModelRule(frozenActionDelete, frozenEffectDeny, value)
}

func (rules *Rules[M]) CanReadFields(value Predicate[M], first Field[M], rest ...Field[M]) {
	rules.appendFieldRule(frozenActionRead, frozenEffectGrant, value, first, rest)
}
func (rules *Rules[M]) CannotReadFields(value Predicate[M], first Field[M], rest ...Field[M]) {
	rules.appendFieldRule(frozenActionRead, frozenEffectDeny, value, first, rest)
}
func (rules *Rules[M]) CanCreateFields(value Predicate[M], first Field[M], rest ...Field[M]) {
	rules.appendFieldRule(frozenActionCreate, frozenEffectGrant, value, first, rest)
}
func (rules *Rules[M]) CannotCreateFields(value Predicate[M], first Field[M], rest ...Field[M]) {
	rules.appendFieldRule(frozenActionCreate, frozenEffectDeny, value, first, rest)
}
func (rules *Rules[M]) CanUpdateFields(value Predicate[M], first Field[M], rest ...Field[M]) {
	rules.appendFieldRule(frozenActionUpdate, frozenEffectGrant, value, first, rest)
}
func (rules *Rules[M]) CannotUpdateFields(value Predicate[M], first Field[M], rest ...Field[M]) {
	rules.appendFieldRule(frozenActionUpdate, frozenEffectDeny, value, first, rest)
}
