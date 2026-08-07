package golem

import (
	"crypto/sha256"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Scope is one schema-owned model occurrence in a scoped read. Its query
// identity, occurrence number, and join path are intentionally opaque.
type Scope[M any] struct {
	queryID    uint64
	occurrence uint32
	model      ModelID
	joins      []FrozenScopedJoin
	next       *atomic.Uint32
	_          func() M
}

var nextScopedQueryIdentity atomic.Uint64

// GeneratedScope is emitted only for models whose contract enables scoped
// reads. Application code cannot supply a model identity through the generated
// surface.
func GeneratedScope[M any](model ModelID) Scope[M] {
	identity := nextScopedQueryIdentity.Add(1)
	if identity == 0 || model == (ModelID{}) {
		return Scope[M]{}
	}
	return Scope[M]{queryID: identity, model: model, next: &atomic.Uint32{}}
}

type scopedRelationIdentity struct {
	field       FieldID
	relation    RelationID
	target      ModelID
	cardinality ScopedRelationCardinality
}

// Relation is sealed to generated readable relation handles.
type Relation[M, R any] interface {
	scopedRelation(M, R) scopedRelationIdentity
}

type ScopedRelationCardinality uint8

const (
	ScopedRelationToOne ScopedRelationCardinality = iota + 1
	ScopedRelationToMany
)

func (field ToOne[M, R]) scopedRelation(M, R) scopedRelationIdentity {
	return scopedRelationIdentity{field: field.fieldID, relation: field.relationID, target: field.targetModel, cardinality: ScopedRelationToOne}
}
func (field ToMany[M, R]) scopedRelation(M, R) scopedRelationIdentity {
	return scopedRelationIdentity{field: field.fieldID, relation: field.relationID, target: field.targetModel, cardinality: ScopedRelationToMany}
}

type ScopedJoinKind uint8

const (
	ScopedInnerJoin ScopedJoinKind = iota + 1
	ScopedLeftJoin
)

func InnerJoin[M, R any](from Scope[M], relation Relation[M, R]) Scope[R] {
	return deriveScope(from, relation, ScopedInnerJoin)
}
func LeftJoin[M, R any](from Scope[M], relation Relation[M, R]) Scope[R] {
	return deriveScope(from, relation, ScopedLeftJoin)
}
func deriveScope[M, R any](from Scope[M], relation Relation[M, R], kind ScopedJoinKind) Scope[R] {
	if from.queryID == 0 || from.model == (ModelID{}) || relation == nil {
		return Scope[R]{}
	}
	identity := relation.scopedRelation(*new(M), *new(R))
	if identity.field == (FieldID{}) || identity.relation == (RelationID{}) || identity.target == (ModelID{}) {
		return Scope[R]{}
	}
	if from.next == nil {
		return Scope[R]{}
	}
	occurrence := from.next.Add(1)
	if occurrence == 0 {
		return Scope[R]{}
	}
	join := FrozenScopedJoin{Occurrence: occurrence, ParentOccurrence: from.occurrence, ParentModel: from.model, Model: identity.target, Field: identity.field, Relation: identity.relation, Kind: kind, Cardinality: identity.cardinality}
	joins := append(append([]FrozenScopedJoin(nil), from.joins...), join)
	return Scope[R]{queryID: from.queryID, occurrence: occurrence, model: identity.target, joins: joins, next: from.next}
}

type ScopedExpressionKind uint8

const (
	ScopedExpressionField ScopedExpressionKind = iota + 1
	ScopedExpressionCountAll
	ScopedExpressionCountField
	ScopedExpressionSum
	ScopedExpressionAverage
	ScopedExpressionMinimum
	ScopedExpressionMaximum
)

type scopedExpression struct {
	queryID    uint64
	occurrence uint32
	model      ModelID
	field      FieldID
	kind       ScopedExpressionKind
}

func (value scopedExpression) key() string {
	return fmt.Sprintf("%d:%x:%x:%d", value.occurrence, value.model, value.field, value.kind)
}

type ScopedSelection interface{ scopedSelection() scopedExpression }
type ScopedDimension interface{ scopedDimension() scopedExpression }

// ScopedJoin is sealed to Scope values returned by GeneratedScope,
// InnerJoin, and LeftJoin.
type ScopedJoin interface {
	runtimeScopedIdentity() (uint64, []FrozenScopedJoin)
}
type ScopedResult[V any] interface {
	ScopedSelection
	scopedResult(V) scopedExpression
}

type scopedField[V any] struct {
	expression scopedExpression
	_          func() V
}

func (field scopedField[V]) scopedSelection() scopedExpression { return field.expression }
func (field scopedField[V]) scopedDimension() scopedExpression { return field.expression }
func (field scopedField[V]) scopedResult(V) scopedExpression   { return field.expression }
func (field scopedField[V]) Asc() ScopedOrder                  { return ScopedOrder{expression: field.expression} }
func (field scopedField[V]) Desc() ScopedOrder {
	return ScopedOrder{expression: field.expression, descending: true}
}
func (field scopedField[V]) Count() ScopedMeasure[int64] {
	return scopedMeasure[int64](field.expression, ScopedExpressionCountField)
}

type ScopedEqualField[V EqualValue] struct{ scopedField[V] }
type ScopedOrderedField[V OrderedValue] struct{ ScopedEqualField[V] }
type ScopedTextField[V ~string] struct{ ScopedOrderedField[V] }
type ScopedIntegerField[V ~int16 | ~int32 | ~int64] struct{ ScopedOrderedField[V] }
type ScopedFloatField[V ~float32 | ~float64] struct{ ScopedOrderedField[V] }
type ScopedDecimalField struct{ ScopedOrderedField[Decimal] }

type ScopedNullableEqualField[V EqualValue] struct{ ScopedEqualField[V] }
type ScopedNullableOrderedField[V OrderedValue] struct{ ScopedOrderedField[V] }
type ScopedNullableTextField[V ~string] struct{ ScopedTextField[V] }
type ScopedNullableIntegerField[V ~int16 | ~int32 | ~int64] struct{ ScopedIntegerField[V] }
type ScopedNullableFloatField[V ~float32 | ~float64] struct{ ScopedFloatField[V] }
type ScopedNullableDecimalField struct{ ScopedDecimalField }

func scopedFieldAt[M, V any](scope Scope[M], field ScalarColumn[M, V]) scopedField[V] {
	if scope.queryID == 0 || scope.model == (ModelID{}) || field == nil {
		return scopedField[V]{}
	}
	return scopedField[V]{expression: scopedExpression{queryID: scope.queryID, occurrence: scope.occurrence, model: scope.model, field: field.fieldIdentity(), kind: ScopedExpressionField}}
}

func GeneratedScopedEqualField[M any, V EqualValue](scope Scope[M], field ScalarColumn[M, V]) ScopedEqualField[V] {
	return ScopedEqualField[V]{scopedField: scopedFieldAt(scope, field)}
}
func GeneratedScopedOrderedField[M any, V OrderedValue](scope Scope[M], field ScalarColumn[M, V]) ScopedOrderedField[V] {
	return ScopedOrderedField[V]{ScopedEqualField: GeneratedScopedEqualField(scope, field)}
}
func GeneratedScopedTextField[M any, V ~string](scope Scope[M], field ScalarColumn[M, V]) ScopedTextField[V] {
	return ScopedTextField[V]{ScopedOrderedField: GeneratedScopedOrderedField(scope, field)}
}
func GeneratedScopedIntegerField[M any, V ~int16 | ~int32 | ~int64](scope Scope[M], field ScalarColumn[M, V]) ScopedIntegerField[V] {
	return ScopedIntegerField[V]{ScopedOrderedField: GeneratedScopedOrderedField(scope, field)}
}
func GeneratedScopedFloatField[M any, V ~float32 | ~float64](scope Scope[M], field ScalarColumn[M, V]) ScopedFloatField[V] {
	return ScopedFloatField[V]{ScopedOrderedField: GeneratedScopedOrderedField(scope, field)}
}
func GeneratedScopedDecimalField[M any](scope Scope[M], field ScalarColumn[M, Decimal]) ScopedDecimalField {
	return ScopedDecimalField{ScopedOrderedField: GeneratedScopedOrderedField(scope, field)}
}

func GeneratedScopedNullableEqualField[M any, V EqualValue](scope Scope[M], field ScalarColumn[M, V]) ScopedNullableEqualField[V] {
	return ScopedNullableEqualField[V]{ScopedEqualField: GeneratedScopedEqualField(scope, field)}
}
func GeneratedScopedNullableOrderedField[M any, V OrderedValue](scope Scope[M], field ScalarColumn[M, V]) ScopedNullableOrderedField[V] {
	return ScopedNullableOrderedField[V]{ScopedOrderedField: GeneratedScopedOrderedField(scope, field)}
}
func GeneratedScopedNullableTextField[M any, V ~string](scope Scope[M], field ScalarColumn[M, V]) ScopedNullableTextField[V] {
	return ScopedNullableTextField[V]{ScopedTextField: GeneratedScopedTextField(scope, field)}
}
func GeneratedScopedNullableIntegerField[M any, V ~int16 | ~int32 | ~int64](scope Scope[M], field ScalarColumn[M, V]) ScopedNullableIntegerField[V] {
	return ScopedNullableIntegerField[V]{ScopedIntegerField: GeneratedScopedIntegerField(scope, field)}
}
func GeneratedScopedNullableFloatField[M any, V ~float32 | ~float64](scope Scope[M], field ScalarColumn[M, V]) ScopedNullableFloatField[V] {
	return ScopedNullableFloatField[V]{ScopedFloatField: GeneratedScopedFloatField(scope, field)}
}
func GeneratedScopedNullableDecimalField[M any](scope Scope[M], field ScalarColumn[M, Decimal]) ScopedNullableDecimalField {
	return ScopedNullableDecimalField{ScopedDecimalField: GeneratedScopedDecimalField(scope, field)}
}

type ScopedPredicateOperator uint8

const (
	ScopedPredicateEqual ScopedPredicateOperator = iota + 1
	ScopedPredicateNotEqual
	ScopedPredicateIn
	ScopedPredicateNotIn
	ScopedPredicateLessThan
	ScopedPredicateLessThanOrEqual
	ScopedPredicateGreaterThan
	ScopedPredicateGreaterThanOrEqual
	ScopedPredicateContains
	ScopedPredicateStartsWith
	ScopedPredicateEndsWith
	ScopedPredicateIsNull
	ScopedPredicateIsNotNull
	ScopedPredicateAnd
	ScopedPredicateOr
	ScopedPredicateNot
)

type scopedPredicateNode struct {
	operator   ScopedPredicateOperator
	expression scopedExpression
	values     []any
	children   []*scopedPredicateNode
}
type ScopedPredicate struct{ node *scopedPredicateNode }

func scopedComparison[V any](expression scopedExpression, operator ScopedPredicateOperator, values ...V) ScopedPredicate {
	operands := make([]any, len(values))
	for index := range values {
		operands[index] = values[index]
	}
	return ScopedPredicate{node: &scopedPredicateNode{operator: operator, expression: expression, values: operands}}
}
func (field ScopedEqualField[V]) Eq(value V) ScopedPredicate {
	return scopedComparison(field.expression, ScopedPredicateEqual, value)
}
func (field ScopedEqualField[V]) Ne(value V) ScopedPredicate {
	return scopedComparison(field.expression, ScopedPredicateNotEqual, value)
}
func (field ScopedEqualField[V]) In(values ...V) ScopedPredicate {
	return scopedComparison(field.expression, ScopedPredicateIn, values...)
}
func (field ScopedEqualField[V]) NotIn(values ...V) ScopedPredicate {
	return scopedComparison(field.expression, ScopedPredicateNotIn, values...)
}
func (field ScopedOrderedField[V]) LT(value V) ScopedPredicate {
	return scopedComparison(field.expression, ScopedPredicateLessThan, value)
}
func (field ScopedOrderedField[V]) LTE(value V) ScopedPredicate {
	return scopedComparison(field.expression, ScopedPredicateLessThanOrEqual, value)
}
func (field ScopedOrderedField[V]) GT(value V) ScopedPredicate {
	return scopedComparison(field.expression, ScopedPredicateGreaterThan, value)
}
func (field ScopedOrderedField[V]) GTE(value V) ScopedPredicate {
	return scopedComparison(field.expression, ScopedPredicateGreaterThanOrEqual, value)
}
func (field ScopedTextField[V]) Contains(value V) ScopedPredicate {
	return scopedComparison(field.expression, ScopedPredicateContains, value)
}
func (field ScopedTextField[V]) StartsWith(value V) ScopedPredicate {
	return scopedComparison(field.expression, ScopedPredicateStartsWith, value)
}
func (field ScopedTextField[V]) EndsWith(value V) ScopedPredicate {
	return scopedComparison(field.expression, ScopedPredicateEndsWith, value)
}

func scopedPresence[V any](field scopedField[V], operator ScopedPredicateOperator) ScopedPredicate {
	return ScopedPredicate{node: &scopedPredicateNode{operator: operator, expression: field.expression}}
}
func (field ScopedNullableEqualField[V]) IsNull() ScopedPredicate {
	return scopedPresence(field.scopedField, ScopedPredicateIsNull)
}
func (field ScopedNullableEqualField[V]) IsNotNull() ScopedPredicate {
	return scopedPresence(field.scopedField, ScopedPredicateIsNotNull)
}
func (field ScopedNullableOrderedField[V]) IsNull() ScopedPredicate {
	return scopedPresence(field.scopedField, ScopedPredicateIsNull)
}
func (field ScopedNullableOrderedField[V]) IsNotNull() ScopedPredicate {
	return scopedPresence(field.scopedField, ScopedPredicateIsNotNull)
}
func (field ScopedNullableTextField[V]) IsNull() ScopedPredicate {
	return scopedPresence(field.scopedField, ScopedPredicateIsNull)
}
func (field ScopedNullableTextField[V]) IsNotNull() ScopedPredicate {
	return scopedPresence(field.scopedField, ScopedPredicateIsNotNull)
}
func (field ScopedNullableIntegerField[V]) IsNull() ScopedPredicate {
	return scopedPresence(field.scopedField, ScopedPredicateIsNull)
}
func (field ScopedNullableIntegerField[V]) IsNotNull() ScopedPredicate {
	return scopedPresence(field.scopedField, ScopedPredicateIsNotNull)
}
func (field ScopedNullableFloatField[V]) IsNull() ScopedPredicate {
	return scopedPresence(field.scopedField, ScopedPredicateIsNull)
}
func (field ScopedNullableFloatField[V]) IsNotNull() ScopedPredicate {
	return scopedPresence(field.scopedField, ScopedPredicateIsNotNull)
}
func (field ScopedNullableDecimalField) IsNull() ScopedPredicate {
	return scopedPresence(field.scopedField, ScopedPredicateIsNull)
}
func (field ScopedNullableDecimalField) IsNotNull() ScopedPredicate {
	return scopedPresence(field.scopedField, ScopedPredicateIsNotNull)
}

func AndScoped(first ScopedPredicate, rest ...ScopedPredicate) ScopedPredicate {
	return scopedLogical(ScopedPredicateAnd, first, rest...)
}
func OrScoped(first ScopedPredicate, rest ...ScopedPredicate) ScopedPredicate {
	return scopedLogical(ScopedPredicateOr, first, rest...)
}
func NotScoped(value ScopedPredicate) ScopedPredicate {
	return ScopedPredicate{node: &scopedPredicateNode{operator: ScopedPredicateNot, children: []*scopedPredicateNode{value.node}}}
}
func scopedLogical(operator ScopedPredicateOperator, first ScopedPredicate, rest ...ScopedPredicate) ScopedPredicate {
	values := append([]ScopedPredicate{first}, rest...)
	children := make([]*scopedPredicateNode, len(values))
	for index := range values {
		children[index] = values[index].node
	}
	return ScopedPredicate{node: &scopedPredicateNode{operator: operator, children: children}}
}

type ScopedMeasure[V any] struct {
	expression scopedExpression
	_          func() V
}

func scopedMeasure[V any](source scopedExpression, kind ScopedExpressionKind) ScopedMeasure[V] {
	source.kind = kind
	return ScopedMeasure[V]{expression: source}
}
func (measure ScopedMeasure[V]) scopedSelection() scopedExpression { return measure.expression }
func (measure ScopedMeasure[V]) scopedResult(V) scopedExpression   { return measure.expression }
func (measure ScopedMeasure[V]) Asc() ScopedOrder                  { return ScopedOrder{expression: measure.expression} }
func (measure ScopedMeasure[V]) Desc() ScopedOrder {
	return ScopedOrder{expression: measure.expression, descending: true}
}
func (measure ScopedMeasure[V]) Eq(value V) ScopedGroupPredicate {
	return scopedGroupComparison(measure.expression, ScopedPredicateEqual, value)
}
func (measure ScopedMeasure[V]) Ne(value V) ScopedGroupPredicate {
	return scopedGroupComparison(measure.expression, ScopedPredicateNotEqual, value)
}
func (measure ScopedMeasure[V]) LT(value V) ScopedGroupPredicate {
	return scopedGroupComparison(measure.expression, ScopedPredicateLessThan, value)
}
func (measure ScopedMeasure[V]) LTE(value V) ScopedGroupPredicate {
	return scopedGroupComparison(measure.expression, ScopedPredicateLessThanOrEqual, value)
}
func (measure ScopedMeasure[V]) GT(value V) ScopedGroupPredicate {
	return scopedGroupComparison(measure.expression, ScopedPredicateGreaterThan, value)
}
func (measure ScopedMeasure[V]) GTE(value V) ScopedGroupPredicate {
	return scopedGroupComparison(measure.expression, ScopedPredicateGreaterThanOrEqual, value)
}
func (measure ScopedMeasure[V]) IsNull() ScopedGroupPredicate {
	return scopedGroupComparison(measure.expression, ScopedPredicateIsNull, false)
}
func (measure ScopedMeasure[V]) IsNotNull() ScopedGroupPredicate {
	return scopedGroupComparison(measure.expression, ScopedPredicateIsNotNull, false)
}

func (scope Scope[M]) Count() ScopedMeasure[int64] {
	return scopedMeasure[int64](scopedExpression{queryID: scope.queryID, occurrence: scope.occurrence, model: scope.model}, ScopedExpressionCountAll)
}
func (field ScopedIntegerField[V]) Sum() ScopedMeasure[ExactInteger] {
	return scopedMeasure[ExactInteger](field.expression, ScopedExpressionSum)
}
func (field ScopedIntegerField[V]) Avg() ScopedMeasure[float64] {
	return scopedMeasure[float64](field.expression, ScopedExpressionAverage)
}
func (field ScopedFloatField[V]) Sum() ScopedMeasure[float64] {
	return scopedMeasure[float64](field.expression, ScopedExpressionSum)
}
func (field ScopedFloatField[V]) Avg() ScopedMeasure[float64] {
	return scopedMeasure[float64](field.expression, ScopedExpressionAverage)
}
func (field ScopedDecimalField) Sum() ScopedMeasure[ExactDecimal] {
	return scopedMeasure[ExactDecimal](field.expression, ScopedExpressionSum)
}
func (field ScopedDecimalField) Avg() ScopedMeasure[ExactDecimal] {
	return scopedMeasure[ExactDecimal](field.expression, ScopedExpressionAverage)
}
func (field ScopedOrderedField[V]) Min() ScopedMeasure[V] {
	return scopedMeasure[V](field.expression, ScopedExpressionMinimum)
}
func (field ScopedOrderedField[V]) Max() ScopedMeasure[V] {
	return scopedMeasure[V](field.expression, ScopedExpressionMaximum)
}

type ScopedGroupPredicate struct{ node *scopedPredicateNode }

func scopedGroupComparison[V any](expression scopedExpression, operator ScopedPredicateOperator, value V) ScopedGroupPredicate {
	return ScopedGroupPredicate{node: &scopedPredicateNode{operator: operator, expression: expression, values: []any{value}}}
}
func AndScopedGroup(first ScopedGroupPredicate, rest ...ScopedGroupPredicate) ScopedGroupPredicate {
	return scopedGroupLogical(ScopedPredicateAnd, first, rest...)
}
func OrScopedGroup(first ScopedGroupPredicate, rest ...ScopedGroupPredicate) ScopedGroupPredicate {
	return scopedGroupLogical(ScopedPredicateOr, first, rest...)
}
func NotScopedGroup(value ScopedGroupPredicate) ScopedGroupPredicate {
	return ScopedGroupPredicate{node: &scopedPredicateNode{operator: ScopedPredicateNot, children: []*scopedPredicateNode{value.node}}}
}
func scopedGroupLogical(operator ScopedPredicateOperator, first ScopedGroupPredicate, rest ...ScopedGroupPredicate) ScopedGroupPredicate {
	values := append([]ScopedGroupPredicate{first}, rest...)
	children := make([]*scopedPredicateNode, len(values))
	for index := range values {
		children[index] = values[index].node
	}
	return ScopedGroupPredicate{node: &scopedPredicateNode{operator: operator, children: children}}
}

type ScopedOrder struct {
	expression scopedExpression
	descending bool
}

type ScopedQuery[M any] struct {
	root       Scope[M]
	joins      []FrozenScopedJoin
	where      *scopedPredicateNode
	groupBy    []scopedExpression
	having     *scopedPredicateNode
	selections []scopedExpression
	orders     []ScopedOrder
	take       *int
	skip       *int
	_          func() M
}

func From[M any](root Scope[M]) ScopedQuery[M] { return ScopedQuery[M]{root: root} }
func (query ScopedQuery[M]) Join(scope ScopedJoin) ScopedQuery[M] {
	identity, joins := runtimeScopeIdentity(scope)
	if identity == 0 || identity != query.root.queryID {
		query.joins = append(query.joins, FrozenScopedJoin{})
		return query
	}
	present := make(map[uint32]bool, len(query.joins))
	for _, join := range query.joins {
		present[join.Occurrence] = true
	}
	for _, join := range joins {
		if !present[join.Occurrence] {
			query.joins = append(query.joins, join)
			present[join.Occurrence] = true
		}
	}
	sort.SliceStable(query.joins, func(i, j int) bool { return query.joins[i].Occurrence < query.joins[j].Occurrence })
	return query
}
func runtimeScopeIdentity(value ScopedJoin) (uint64, []FrozenScopedJoin) {
	if value == nil {
		return 0, nil
	}
	return value.runtimeScopedIdentity()
}
func (scope Scope[M]) runtimeScopedIdentity() (uint64, []FrozenScopedJoin) {
	return scope.queryID, append([]FrozenScopedJoin(nil), scope.joins...)
}
func (query ScopedQuery[M]) Where(predicate ScopedPredicate) ScopedQuery[M] {
	query.where = predicate.node
	return query
}
func (query ScopedQuery[M]) GroupBy(first ScopedDimension, rest ...ScopedDimension) ScopedQuery[M] {
	values := append([]ScopedDimension{first}, rest...)
	query.groupBy = make([]scopedExpression, len(values))
	for i, value := range values {
		query.groupBy[i] = value.scopedDimension()
	}
	return query
}
func (query ScopedQuery[M]) Having(predicate ScopedGroupPredicate) ScopedQuery[M] {
	query.having = predicate.node
	return query
}
func (query ScopedQuery[M]) Select(first ScopedSelection, rest ...ScopedSelection) ScopedQuery[M] {
	values := append([]ScopedSelection{first}, rest...)
	query.selections = make([]scopedExpression, len(values))
	for i, value := range values {
		query.selections[i] = value.scopedSelection()
	}
	return query
}
func (query ScopedQuery[M]) OrderBy(first ScopedOrder, rest ...ScopedOrder) ScopedQuery[M] {
	query.orders = append([]ScopedOrder{first}, rest...)
	return query
}
func (query ScopedQuery[M]) Take(value int) ScopedQuery[M] { query.take = &value; return query }
func (query ScopedQuery[M]) Skip(value int) ScopedQuery[M] { query.skip = &value; return query }

// FrozenScopedJoin and the frozen request are immutable-by-copy runtime bridge
// values. They contain stable logical IDs only, never SQL identifiers.
type FrozenScopedJoin struct {
	Occurrence       uint32
	ParentOccurrence uint32
	ParentModel      ModelID
	Model            ModelID
	Field            FieldID
	Relation         RelationID
	Kind             ScopedJoinKind
	Cardinality      ScopedRelationCardinality
}
type FrozenScopedExpression struct {
	Occurrence uint32
	Model      ModelID
	Field      FieldID
	Kind       ScopedExpressionKind
}
type FrozenScopedPredicate struct {
	Operator   ScopedPredicateOperator
	Expression FrozenScopedExpression
	Values     []any
	Children   []FrozenScopedPredicate
}
type FrozenScopedOrder struct {
	Expression FrozenScopedExpression
	Descending bool
}
type FrozenScopedQuery struct {
	root       ModelID
	joins      []FrozenScopedJoin
	where      *FrozenScopedPredicate
	groupBy    []FrozenScopedExpression
	having     *FrozenScopedPredicate
	selections []FrozenScopedExpression
	orders     []FrozenScopedOrder
	take       *int
	skip       *int
}

func (query FrozenScopedQuery) RootModelID() ModelID { return query.root }
func (query FrozenScopedQuery) Joins() []FrozenScopedJoin {
	return append([]FrozenScopedJoin(nil), query.joins...)
}
func (query FrozenScopedQuery) Where() (FrozenScopedPredicate, bool) {
	if query.where == nil {
		return FrozenScopedPredicate{}, false
	}
	return cloneFrozenScopedPredicate(*query.where), true
}
func (query FrozenScopedQuery) GroupBy() []FrozenScopedExpression {
	return append([]FrozenScopedExpression(nil), query.groupBy...)
}
func (query FrozenScopedQuery) Having() (FrozenScopedPredicate, bool) {
	if query.having == nil {
		return FrozenScopedPredicate{}, false
	}
	return cloneFrozenScopedPredicate(*query.having), true
}
func (query FrozenScopedQuery) Selections() []FrozenScopedExpression {
	return append([]FrozenScopedExpression(nil), query.selections...)
}
func (query FrozenScopedQuery) Orders() []FrozenScopedOrder {
	return append([]FrozenScopedOrder(nil), query.orders...)
}
func (query FrozenScopedQuery) Take() (int, bool) {
	if query.take == nil {
		return 0, false
	}
	return *query.take, true
}
func (query FrozenScopedQuery) Skip() (int, bool) {
	if query.skip == nil {
		return 0, false
	}
	return *query.skip, true
}
func (query FrozenScopedQuery) PredicateNodeCount() int {
	return frozenScopedPredicateNodes(query.where) + frozenScopedPredicateNodes(query.having)
}
func frozenScopedPredicateNodes(value *FrozenScopedPredicate) int {
	if value == nil {
		return 0
	}
	result := 1
	for index := range value.Children {
		result += frozenScopedPredicateNodes(&value.Children[index])
	}
	return result
}

func RuntimeFreezeScopedQuery[M any](query ScopedQuery[M]) (FrozenScopedQuery, error) {
	if query.root.queryID == 0 || query.root.model == (ModelID{}) || len(query.selections) == 0 {
		return FrozenScopedQuery{}, fmt.Errorf("P6_SCOPED_SHAPE: root and selection are required")
	}
	known := map[uint32]ModelID{0: query.root.model}
	seenOccurrences := map[uint32]bool{}
	for index, join := range query.joins {
		_ = index
		if join.Occurrence == 0 || seenOccurrences[join.Occurrence] || known[join.ParentOccurrence] != join.ParentModel || join.Model == (ModelID{}) || join.Field == (FieldID{}) || join.Relation == (RelationID{}) || (join.Kind != ScopedInnerJoin && join.Kind != ScopedLeftJoin) {
			return FrozenScopedQuery{}, fmt.Errorf("P6_SCOPED_JOIN: invalid or foreign join")
		}
		seenOccurrences[join.Occurrence] = true
		known[join.Occurrence] = join.Model
	}
	freezeExpression := func(value scopedExpression) (FrozenScopedExpression, error) {
		if value.queryID != query.root.queryID || known[value.occurrence] != value.model || value.kind == 0 || (value.kind != ScopedExpressionCountAll && value.field == (FieldID{})) {
			return FrozenScopedExpression{}, fmt.Errorf("P6_SCOPED_EXPRESSION: mixed, omitted, or forged scope")
		}
		return FrozenScopedExpression{Occurrence: value.occurrence, Model: value.model, Field: value.field, Kind: value.kind}, nil
	}
	freezeList := func(values []scopedExpression) ([]FrozenScopedExpression, error) {
		result := make([]FrozenScopedExpression, len(values))
		seen := map[string]bool{}
		for i, value := range values {
			frozen, err := freezeExpression(value)
			if err != nil {
				return nil, err
			}
			key := value.key()
			if seen[key] {
				return nil, fmt.Errorf("P6_SCOPED_EXPRESSION: duplicate expression")
			}
			seen[key] = true
			result[i] = frozen
		}
		return result, nil
	}
	selections, err := freezeList(query.selections)
	if err != nil {
		return FrozenScopedQuery{}, err
	}
	groups, err := freezeList(query.groupBy)
	if err != nil {
		return FrozenScopedQuery{}, err
	}
	freezePredicate := func(node *scopedPredicateNode) (*FrozenScopedPredicate, error) {
		return freezeScopedPredicate(node, query.root.queryID, known, freezeExpression, 0)
	}
	where, err := freezePredicate(query.where)
	if err != nil {
		return FrozenScopedQuery{}, err
	}
	having, err := freezePredicate(query.having)
	if err != nil {
		return FrozenScopedQuery{}, err
	}
	orders := make([]FrozenScopedOrder, len(query.orders))
	for i, order := range query.orders {
		expression, freezeErr := freezeExpression(order.expression)
		if freezeErr != nil {
			return FrozenScopedQuery{}, freezeErr
		}
		orders[i] = FrozenScopedOrder{Expression: expression, Descending: order.descending}
	}
	if query.skip != nil && *query.skip < 0 {
		return FrozenScopedQuery{}, fmt.Errorf("P6_SCOPED_SKIP: skip cannot be negative")
	}
	if query.take != nil && *query.take == 0 {
		return FrozenScopedQuery{}, fmt.Errorf("P6_SCOPED_TAKE: take cannot be zero")
	}
	return FrozenScopedQuery{root: query.root.model, joins: append([]FrozenScopedJoin(nil), query.joins...), where: where, groupBy: groups, having: having, selections: selections, orders: orders, take: cloneInt(query.take), skip: cloneInt(query.skip)}, nil
}
func freezeScopedPredicate(node *scopedPredicateNode, queryID uint64, known map[uint32]ModelID, freezeExpression func(scopedExpression) (FrozenScopedExpression, error), depth int) (*FrozenScopedPredicate, error) {
	if node == nil {
		return nil, nil
	}
	if depth > 8192 || node.operator == 0 {
		return nil, fmt.Errorf("P6_SCOPED_PREDICATE: invalid predicate")
	}
	result := &FrozenScopedPredicate{Operator: node.operator, Values: cloneScopedOperands(node.values)}
	if node.operator == ScopedPredicateAnd || node.operator == ScopedPredicateOr || node.operator == ScopedPredicateNot {
		if len(node.children) == 0 || (node.operator == ScopedPredicateNot && len(node.children) != 1) {
			return nil, fmt.Errorf("P6_SCOPED_PREDICATE: invalid logical arity")
		}
		result.Children = make([]FrozenScopedPredicate, len(node.children))
		for i, child := range node.children {
			frozen, err := freezeScopedPredicate(child, queryID, known, freezeExpression, depth+1)
			if err != nil {
				return nil, err
			}
			result.Children[i] = *frozen
		}
		return result, nil
	}
	expression, err := freezeExpression(node.expression)
	if err != nil {
		return nil, err
	}
	result.Expression = expression
	return result, nil
}
func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
func cloneFrozenScopedPredicate(value FrozenScopedPredicate) FrozenScopedPredicate {
	value.Values = cloneScopedOperands(value.Values)
	value.Children = append([]FrozenScopedPredicate(nil), value.Children...)
	for i := range value.Children {
		value.Children[i] = cloneFrozenScopedPredicate(value.Children[i])
	}
	return value
}
func cloneScopedOperands(values []any) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = cloneScopedOperand(value)
	}
	return result
}
func cloneScopedOperand(value any) any {
	if value == nil {
		return nil
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Slice {
		return value
	}
	clone := reflect.MakeSlice(reflected.Type(), reflected.Len(), reflected.Len())
	reflect.Copy(clone, reflected)
	return clone.Interface()
}

type ScopedRow struct{ cells map[string]readCell }

func ScopedValue[V any](row ScopedRow, expression ScopedResult[V]) ReadValue[V] {
	if expression == nil {
		return ReadValue[V]{}
	}
	cell, ok := row.cells[expression.scopedResult(*new(V)).key()]
	if !ok {
		return ReadValue[V]{}
	}
	return typedAnalyticsCell[V](cell)
}

type RuntimeScopedCell struct {
	key   string
	state ReadState
	value any
}

func RuntimePresentScopedCell(expression FrozenScopedExpression, value any) RuntimeScopedCell {
	return RuntimeScopedCell{key: RuntimeScopedExpressionKey(expression), state: ReadPresent, value: value}
}
func RuntimeNullScopedCell(expression FrozenScopedExpression) RuntimeScopedCell {
	return RuntimeScopedCell{key: RuntimeScopedExpressionKey(expression), state: ReadNull}
}
func RuntimeScopedRow(cells ...RuntimeScopedCell) ScopedRow {
	result := ScopedRow{cells: make(map[string]readCell, len(cells))}
	for key, cell := range cells {
		_ = key
		result.cells[cell.key] = readCell{state: cell.state, value: cell.value}
	}
	return result
}
func RuntimeScopedExpressionKey(expression FrozenScopedExpression) string {
	return fmt.Sprintf("%d:%x:%x:%d", expression.Occurrence, expression.Model, expression.Field, expression.Kind)
}

type ScopedOutcome uint8

const (
	ScopedOutcomeSucceeded ScopedOutcome = iota + 1
	ScopedOutcomeRefused
	ScopedOutcomeCancelled
	ScopedOutcomeFailed
)

// ScopedAuditRecord is immutable and intentionally contains inventories and
// fingerprints only. The runtime bridge accepts rendered SQL solely to hash it;
// no SQL text, binds, principal values, actors, identifiers, or errors survive.
type ScopedAuditRecord struct {
	models      []ModelID
	relations   []RelationID
	fields      []FieldID
	joins       []ScopedJoinKind
	expressions []ScopedExpressionKind
	principal   string
	execution   uint64
	system      bool
	provider    Provider
	shape       SchemaDigest
	sql         SchemaDigest
	duration    time.Duration
	rows        int64
	outcome     ScopedOutcome
}

func (record ScopedAuditRecord) Models() []ModelID { return append([]ModelID(nil), record.models...) }
func (record ScopedAuditRecord) Relations() []RelationID {
	return append([]RelationID(nil), record.relations...)
}
func (record ScopedAuditRecord) Fields() []FieldID { return append([]FieldID(nil), record.fields...) }
func (record ScopedAuditRecord) JoinKinds() []ScopedJoinKind {
	return append([]ScopedJoinKind(nil), record.joins...)
}
func (record ScopedAuditRecord) ExpressionKinds() []ScopedExpressionKind {
	return append([]ScopedExpressionKind(nil), record.expressions...)
}
func (record ScopedAuditRecord) PrincipalAuditID() string       { return record.principal }
func (record ScopedAuditRecord) ExecutionID() uint64            { return record.execution }
func (record ScopedAuditRecord) IsSystem() bool                 { return record.system }
func (record ScopedAuditRecord) Provider() Provider             { return record.provider }
func (record ScopedAuditRecord) ShapeFingerprint() SchemaDigest { return record.shape }
func (record ScopedAuditRecord) SQLFingerprint() SchemaDigest   { return record.sql }
func (record ScopedAuditRecord) Duration() time.Duration        { return record.duration }
func (record ScopedAuditRecord) RowCount() int64                { return record.rows }
func (record ScopedAuditRecord) Outcome() ScopedOutcome         { return record.outcome }

func RuntimeScopedAuditRecord(query FrozenScopedQuery, principal string, execution uint64, system bool, provider Provider, sqlText string, duration time.Duration, rows int64, outcome ScopedOutcome) ScopedAuditRecord {
	models := []ModelID{query.root}
	relations := make([]RelationID, 0, len(query.joins))
	joins := make([]ScopedJoinKind, 0, len(query.joins))
	modelSeen := map[ModelID]bool{query.root: true}
	fieldSeen := map[FieldID]bool{}
	expressionSeen := map[ScopedExpressionKind]bool{}
	for _, join := range query.joins {
		if !modelSeen[join.Model] {
			models = append(models, join.Model)
			modelSeen[join.Model] = true
		}
		relations = append(relations, join.Relation)
		joins = append(joins, join.Kind)
		fieldSeen[join.Field] = true
	}
	visitExpression := func(value FrozenScopedExpression) {
		if value.Field != (FieldID{}) {
			fieldSeen[value.Field] = true
		}
		expressionSeen[value.Kind] = true
	}
	for _, value := range query.selections {
		visitExpression(value)
	}
	for _, value := range query.groupBy {
		visitExpression(value)
	}
	for _, value := range query.orders {
		visitExpression(value.Expression)
	}
	visitPredicateInventory(query.where, visitExpression)
	visitPredicateInventory(query.having, visitExpression)
	fields := make([]FieldID, 0, len(fieldSeen))
	for value := range fieldSeen {
		fields = append(fields, value)
	}
	sort.Slice(fields, func(i, j int) bool { return string(fields[i][:]) < string(fields[j][:]) })
	expressions := make([]ScopedExpressionKind, 0, len(expressionSeen))
	for value := range expressionSeen {
		expressions = append(expressions, value)
	}
	sort.Slice(expressions, func(i, j int) bool { return expressions[i] < expressions[j] })
	shapeText := fmt.Sprintf("%x|%v|%v|%v|%v|take=%s|skip=%s|where=%s|having=%s", query.root, query.joins, query.selections, query.groupBy, query.orders, scopedPageShape(query.take), scopedPageShape(query.skip), scopedPredicateShape(query.where), scopedPredicateShape(query.having))
	return ScopedAuditRecord{models: models, relations: relations, fields: fields, joins: joins, expressions: expressions, principal: principal, execution: execution, system: system, provider: provider, shape: SchemaDigest(sha256.Sum256([]byte(shapeText))), sql: SchemaDigest(sha256.Sum256([]byte(sqlText))), duration: duration, rows: rows, outcome: outcome}
}
func scopedPageShape(value *int) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprint(*value)
}
func visitPredicateInventory(value *FrozenScopedPredicate, visit func(FrozenScopedExpression)) {
	if value == nil {
		return
	}
	if value.Expression.Kind != 0 {
		visit(value.Expression)
	}
	for index := range value.Children {
		visitPredicateInventory(&value.Children[index], visit)
	}
}
func scopedPredicateShape(value *FrozenScopedPredicate) string {
	if value == nil {
		return "-"
	}
	children := make([]string, len(value.Children))
	for index := range value.Children {
		children[index] = scopedPredicateShape(&value.Children[index])
	}
	return fmt.Sprintf("%d:%d:%x:%x:%d[%s]", value.Operator, value.Expression.Occurrence, value.Expression.Model, value.Expression.Field, value.Expression.Kind, strings.Join(children, ","))
}
