package golem

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const maxExactDigits = 128

// ExactInteger is an immutable, canonical arbitrary-precision analytical
// integer. Its representation is deliberately not exposed as math/big state.
type ExactInteger struct{ canonical string }

func NewExactInteger(value int64) ExactInteger {
	return ExactInteger{canonical: strconv.FormatInt(value, 10)}
}
func ParseExactInteger(text string) (ExactInteger, error) {
	integer := new(big.Int)
	if strings.HasPrefix(text, "+") || text == "" {
		return ExactInteger{}, fmt.Errorf("P6_EXACT_INTEGER: non-canonical integer")
	}
	if _, ok := integer.SetString(text, 10); !ok {
		return ExactInteger{}, fmt.Errorf("P6_EXACT_INTEGER: invalid integer")
	}
	canonical := integer.String()
	digits := strings.TrimPrefix(canonical, "-")
	if len(digits) > maxExactDigits {
		return ExactInteger{}, fmt.Errorf("P6_ANALYTICAL_OVERFLOW: integer exceeds %d digits", maxExactDigits)
	}
	return ExactInteger{canonical: canonical}, nil
}
func MustParseExactInteger(text string) ExactInteger {
	value, err := ParseExactInteger(text)
	if err != nil {
		panic(err)
	}
	return value
}
func (value ExactInteger) String() string {
	if value.canonical == "" {
		return "0"
	}
	return value.canonical
}
func (value ExactInteger) Int64() (int64, bool) {
	result, err := strconv.ParseInt(value.String(), 10, 64)
	return result, err == nil
}
func (value ExactInteger) Cmp(other ExactInteger) int {
	left, _ := new(big.Int).SetString(value.String(), 10)
	right, _ := new(big.Int).SetString(other.String(), 10)
	return left.Cmp(right)
}

// ExactDecimal stores canonical base-ten text. Parsing accepts at most scale 18
// and 128 significant digits; arithmetic providers may return coefficients
// larger than the portable Decimal envelope without losing precision.
type ExactDecimal struct{ canonical string }

func ExactDecimalFrom(value Decimal) ExactDecimal { return ExactDecimal{canonical: value.String()} }
func ParseExactDecimal(text string) (ExactDecimal, error) {
	if text == "" || strings.HasPrefix(text, "+") || strings.ContainsAny(text, "eE") {
		return ExactDecimal{}, fmt.Errorf("P6_EXACT_DECIMAL: invalid decimal")
	}
	negative := strings.HasPrefix(text, "-")
	unsigned := strings.TrimPrefix(text, "-")
	parts := strings.Split(unsigned, ".")
	if len(parts) > 2 || parts[0] == "" {
		return ExactDecimal{}, fmt.Errorf("P6_EXACT_DECIMAL: invalid decimal")
	}
	for _, part := range parts {
		for _, r := range part {
			if r < '0' || r > '9' {
				return ExactDecimal{}, fmt.Errorf("P6_EXACT_DECIMAL: invalid decimal")
			}
		}
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > 18 {
		return ExactDecimal{}, fmt.Errorf("P6_ANALYTICAL_OVERFLOW: decimal scale exceeds 18")
	}
	whole := strings.TrimLeft(parts[0], "0")
	if whole == "" {
		whole = "0"
	}
	fraction = strings.TrimRight(fraction, "0")
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		digits = "0"
	}
	if len(digits) > maxExactDigits {
		return ExactDecimal{}, fmt.Errorf("P6_ANALYTICAL_OVERFLOW: decimal exceeds %d digits", maxExactDigits)
	}
	canonical := whole
	if fraction != "" {
		canonical += "." + fraction
	}
	if negative && canonical != "0" {
		canonical = "-" + canonical
	}
	return ExactDecimal{canonical: canonical}, nil
}
func MustParseExactDecimal(text string) ExactDecimal {
	value, err := ParseExactDecimal(text)
	if err != nil {
		panic(err)
	}
	return value
}
func (value ExactDecimal) String() string {
	if value.canonical == "" {
		return "0"
	}
	return value.canonical
}
func (value ExactDecimal) Decimal() (Decimal, bool) {
	parsed, err := ParseDecimal(value.String())
	return parsed, err == nil
}
func (value ExactDecimal) Cmp(other ExactDecimal) int {
	left, _ := new(big.Rat).SetString(value.String())
	right, _ := new(big.Rat).SetString(other.String())
	return left.Cmp(right)
}

type AggregateOperator uint8

const (
	AggregateCountAll AggregateOperator = iota + 1
	AggregateCountField
	AggregateSum
	AggregateAverage
	AggregateMinimum
	AggregateMaximum
)

type aggregateIdentity struct {
	model    ModelID
	field    FieldID
	operator AggregateOperator
	relation string
	path     []RelationID
}

func (identity aggregateIdentity) key() string {
	return fmt.Sprintf("%x:%x:%d:%s:%s", identity.model, identity.field, identity.operator, identity.relation, relationPathKey(identity.path))
}

type Dimension[M, V any] struct {
	identity aggregateIdentity
	ordered  bool
	_        func(M) V
}
type RelationDimension[M, V any] struct {
	identity aggregateIdentity
	path     []RelationID
	ordered  bool
	_        func(M) V
}
type Measure[M, V any] struct {
	identity aggregateIdentity
	_        func(M) V
}

type LocalGroupDimension[M any] interface {
	localGroupDimension(M)
	analyticsIdentity() aggregateIdentity
}
type RelationGroupDimension[M any] interface {
	relationGroupDimension(M)
	analyticsIdentity() aggregateIdentity
}
type OrderedGroupDimension[M, V any] interface {
	orderedGroupDimension(M, V)
	analyticsIdentity() aggregateIdentity
}

// OrderedAnalyticsValue is the closed set accepted by ordered dimension
// predicates. In particular Bool and UUID cannot type-check here even though
// both remain valid equality/grouping dimensions.
type OrderedAnalyticsValue interface {
	~int16 | ~int32 | ~int64 | ~float32 | ~float64 | ~string | Decimal | Date | Time | time.Time
}
type AggregateMeasure[M any] interface {
	aggregateMeasure(M)
	analyticsIdentity() aggregateIdentity
}
type GroupCell[M, V any] interface {
	groupCell(M, V)
	analyticsIdentity() aggregateIdentity
}
type RelationGroupCell[M, V any] interface {
	relationGroupCell(M, V)
	analyticsIdentity() aggregateIdentity
}

func (value Dimension[M, V]) analyticsIdentity() aggregateIdentity         { return value.identity }
func (Dimension[M, V]) localGroupDimension(M)                              {}
func (Dimension[M, V]) relationGroupDimension(M)                           {}
func (Dimension[M, V]) orderedGroupDimension(M, V)                         {}
func (Dimension[M, V]) groupCell(M, V)                                     {}
func (Dimension[M, V]) relationGroupCell(M, V)                             {}
func (value RelationDimension[M, V]) analyticsIdentity() aggregateIdentity { return value.identity }
func (RelationDimension[M, V]) relationGroupDimension(M)                   {}
func (RelationDimension[M, V]) orderedGroupDimension(M, V)                 {}
func (RelationDimension[M, V]) relationGroupCell(M, V)                     {}
func (value Measure[M, V]) analyticsIdentity() aggregateIdentity           { return value.identity }
func (Measure[M, V]) aggregateMeasure(M)                                   {}
func (Measure[M, V]) groupCell(M, V)                                       {}
func (Measure[M, V]) relationGroupCell(M, V)                               {}

func GeneratedDimension[M, V any](model ModelID, field ScalarColumn[M, V], ordered bool) Dimension[M, V] {
	return Dimension[M, V]{identity: aggregateIdentity{model: model, field: field.fieldIdentity()}, ordered: ordered}
}
func GeneratedRelationDimension[M, V any](model ModelID, name string, path []RelationID, terminal FieldID, ordered bool) RelationDimension[M, V] {
	clonedPath := append([]RelationID(nil), path...)
	return RelationDimension[M, V]{identity: aggregateIdentity{model: model, field: terminal, relation: name, path: clonedPath}, path: append([]RelationID(nil), clonedPath...), ordered: ordered}
}
func GeneratedMeasure[M, Input, Output any](model ModelID, field ScalarColumn[M, Input], operator AggregateOperator) Measure[M, Output] {
	return Measure[M, Output]{identity: aggregateIdentity{model: model, field: field.fieldIdentity(), operator: operator}}
}
func GeneratedCountAll[M any](model ModelID) Measure[M, int64] {
	return Measure[M, int64]{identity: aggregateIdentity{model: model, operator: AggregateCountAll}}
}

type groupPredicateKind uint8

const (
	groupCompare groupPredicateKind = iota + 1
	groupAnd
	groupOr
	groupNot
)

type GroupPredicate[M any] struct {
	kind     groupPredicateKind
	identity aggregateIdentity
	operator string
	value    any
	mode     FrozenComparisonMode
	children []GroupPredicate[M]
}
type GroupOrder[M any] struct {
	identity   aggregateIdentity
	descending bool
}

func groupComparison[M, V any](identity aggregateIdentity, operator string, value V) GroupPredicate[M] {
	return GroupPredicate[M]{kind: groupCompare, identity: identity, operator: operator, value: value, mode: FrozenComparisonSensitive}
}

func textMeasureComparison[M any, V ~string](measure Measure[M, V], value V, mode ComparisonMode, operator string) GroupPredicate[M] {
	comparison := FrozenComparisonMode(0)
	if mode != nil {
		comparison = mode.comparisonMode()
	}
	return GroupPredicate[M]{kind: groupCompare, identity: measure.identity, operator: operator, value: value, mode: comparison}
}

func TextMeasureContains[M any, V ~string](measure Measure[M, V], value V, mode ComparisonMode) GroupPredicate[M] {
	return textMeasureComparison(measure, value, mode, "contains")
}
func TextMeasureStartsWith[M any, V ~string](measure Measure[M, V], value V, mode ComparisonMode) GroupPredicate[M] {
	return textMeasureComparison(measure, value, mode, "startsWith")
}
func TextMeasureEndsWith[M any, V ~string](measure Measure[M, V], value V, mode ComparisonMode) GroupPredicate[M] {
	return textMeasureComparison(measure, value, mode, "endsWith")
}
func (m Measure[M, V]) Eq(v V) GroupPredicate[M]  { return groupComparison[M](m.identity, "eq", v) }
func (m Measure[M, V]) Ne(v V) GroupPredicate[M]  { return groupComparison[M](m.identity, "ne", v) }
func (m Measure[M, V]) LT(v V) GroupPredicate[M]  { return groupComparison[M](m.identity, "lt", v) }
func (m Measure[M, V]) LTE(v V) GroupPredicate[M] { return groupComparison[M](m.identity, "lte", v) }
func (m Measure[M, V]) GT(v V) GroupPredicate[M]  { return groupComparison[M](m.identity, "gt", v) }
func (m Measure[M, V]) GTE(v V) GroupPredicate[M] { return groupComparison[M](m.identity, "gte", v) }
func (m Measure[M, V]) IsNull() GroupPredicate[M] {
	return groupComparison[M](m.identity, "isNull", true)
}
func (m Measure[M, V]) IsNotNull() GroupPredicate[M] {
	return groupComparison[M](m.identity, "isNotNull", true)
}
func (d Dimension[M, V]) Eq(v V) GroupPredicate[M] { return groupComparison[M](d.identity, "eq", v) }
func (d Dimension[M, V]) Ne(v V) GroupPredicate[M] { return groupComparison[M](d.identity, "ne", v) }
func (d Dimension[M, V]) IsNull() GroupPredicate[M] {
	return groupComparison[M](d.identity, "isNull", true)
}
func (d Dimension[M, V]) IsNotNull() GroupPredicate[M] {
	return groupComparison[M](d.identity, "isNotNull", true)
}
func (d RelationDimension[M, V]) Eq(v V) GroupPredicate[M] {
	return groupComparison[M](d.identity, "eq", v)
}
func (d RelationDimension[M, V]) Ne(v V) GroupPredicate[M] {
	return groupComparison[M](d.identity, "ne", v)
}
func (d RelationDimension[M, V]) IsNull() GroupPredicate[M] {
	return groupComparison[M](d.identity, "isNull", true)
}
func (d RelationDimension[M, V]) IsNotNull() GroupPredicate[M] {
	return groupComparison[M](d.identity, "isNotNull", true)
}
func DimensionLT[M any, V OrderedAnalyticsValue](d OrderedGroupDimension[M, V], v V) GroupPredicate[M] {
	return groupComparison[M](d.analyticsIdentity(), "lt", v)
}
func DimensionLTE[M any, V OrderedAnalyticsValue](d OrderedGroupDimension[M, V], v V) GroupPredicate[M] {
	return groupComparison[M](d.analyticsIdentity(), "lte", v)
}
func DimensionGT[M any, V OrderedAnalyticsValue](d OrderedGroupDimension[M, V], v V) GroupPredicate[M] {
	return groupComparison[M](d.analyticsIdentity(), "gt", v)
}
func DimensionGTE[M any, V OrderedAnalyticsValue](d OrderedGroupDimension[M, V], v V) GroupPredicate[M] {
	return groupComparison[M](d.analyticsIdentity(), "gte", v)
}
func AndGroup[M any](first GroupPredicate[M], rest ...GroupPredicate[M]) GroupPredicate[M] {
	return GroupPredicate[M]{kind: groupAnd, children: append([]GroupPredicate[M]{first}, rest...)}
}
func OrGroup[M any](first GroupPredicate[M], rest ...GroupPredicate[M]) GroupPredicate[M] {
	return GroupPredicate[M]{kind: groupOr, children: append([]GroupPredicate[M]{first}, rest...)}
}
func NotGroup[M any](value GroupPredicate[M]) GroupPredicate[M] {
	return GroupPredicate[M]{kind: groupNot, children: []GroupPredicate[M]{value}}
}
func (m Measure[M, V]) Asc() GroupOrder[M] { return GroupOrder[M]{identity: m.identity} }
func (m Measure[M, V]) Desc() GroupOrder[M] {
	return GroupOrder[M]{identity: m.identity, descending: true}
}
func (d Dimension[M, V]) Asc() GroupOrder[M] { return GroupOrder[M]{identity: d.identity} }
func (d Dimension[M, V]) Desc() GroupOrder[M] {
	return GroupOrder[M]{identity: d.identity, descending: true}
}
func (d RelationDimension[M, V]) Asc() GroupOrder[M] { return GroupOrder[M]{identity: d.identity} }
func (d RelationDimension[M, V]) Desc() GroupOrder[M] {
	return GroupOrder[M]{identity: d.identity, descending: true}
}

type aggregateOptionKind uint8

const (
	aggregateWhere aggregateOptionKind = iota + 1
	aggregateSelect
)

type AggregateOption[M any] interface {
	aggregateOption(M) aggregateOptionValue[M]
}
type aggregateOptionValue[M any] struct {
	kind     aggregateOptionKind
	where    *Predicate[M]
	measures []aggregateIdentity
}

func (value aggregateOptionValue[M]) aggregateOption(M) aggregateOptionValue[M] { return value }

type AggregateRequest[M any] struct {
	model    ModelID
	where    *Predicate[M]
	measures []aggregateIdentity
	err      error
}
type AggregateResult[M any] struct {
	model ModelID
	cells map[string]readCell
}

func GeneratedAggregateWhere[M any](predicate Predicate[M]) AggregateOption[M] {
	return aggregateOptionValue[M]{kind: aggregateWhere, where: &predicate}
}
func GeneratedAggregateSelect[M any](first AggregateMeasure[M], rest ...AggregateMeasure[M]) AggregateOption[M] {
	values := []AggregateMeasure[M]{first}
	values = append(values, rest...)
	ids := make([]aggregateIdentity, len(values))
	for i, v := range values {
		ids[i] = v.analyticsIdentity()
	}
	return aggregateOptionValue[M]{kind: aggregateSelect, measures: ids}
}
func GeneratedAggregate[M any](model ModelID, options ...AggregateOption[M]) AggregateRequest[M] {
	result := AggregateRequest[M]{model: model}
	seen := map[aggregateOptionKind]bool{}
	for _, option := range options {
		if option == nil {
			result.err = fmt.Errorf("P6_AGGREGATE_OPTION: nil option")
			continue
		}
		value := option.aggregateOption(*new(M))
		if seen[value.kind] {
			result.err = fmt.Errorf("P6_AGGREGATE_OPTION: duplicate option")
		}
		seen[value.kind] = true
		if value.kind == aggregateWhere {
			result.where = value.where
		} else if value.kind == aggregateSelect {
			result.measures = append([]aggregateIdentity(nil), value.measures...)
		}
	}
	if len(result.measures) == 0 {
		result.err = fmt.Errorf("P6_AGGREGATE_SELECT: non-empty select is required")
	}
	return result
}
func AggregateValue[M, V any](result AggregateResult[M], measure Measure[M, V]) ReadValue[V] {
	if result.model != measure.identity.model {
		return ReadValue[V]{}
	}
	return typedAnalyticsCell[V](result.cells[measure.identity.key()])
}

type groupOptionKind uint8

const (
	groupDimensions groupOptionKind = iota + 1
	groupMeasures
	groupWhere
	groupHaving
	groupOrdering
	groupTake
	groupSkip
)

type GroupOption[M any] interface{ groupOption(M) groupOptionValue[M] }
type groupOptionValue[M any] struct {
	kind                 groupOptionKind
	dimensions, measures []aggregateIdentity
	where                *Predicate[M]
	having               *GroupPredicate[M]
	orders               []GroupOrder[M]
	number               int
}

func (value groupOptionValue[M]) groupOption(M) groupOptionValue[M] { return value }

type GroupRequest[M any] struct {
	model                ModelID
	dimensions, measures []aggregateIdentity
	where                *Predicate[M]
	having               *GroupPredicate[M]
	orders               []GroupOrder[M]
	take, skip           *int
	err                  error
}
type GroupRow[M any] struct {
	model ModelID
	cells map[string]readCell
}

func GeneratedGroupDimensions[M any](first LocalGroupDimension[M], rest ...LocalGroupDimension[M]) GroupOption[M] {
	values := []LocalGroupDimension[M]{first}
	values = append(values, rest...)
	ids := make([]aggregateIdentity, len(values))
	for i, v := range values {
		ids[i] = v.analyticsIdentity()
	}
	return groupOptionValue[M]{kind: groupDimensions, dimensions: ids}
}
func GeneratedGroupMeasures[M any](values ...AggregateMeasure[M]) GroupOption[M] {
	ids := make([]aggregateIdentity, len(values))
	for i, v := range values {
		ids[i] = v.analyticsIdentity()
	}
	return groupOptionValue[M]{kind: groupMeasures, measures: ids}
}
func GeneratedGroupWhere[M any](value Predicate[M]) GroupOption[M] {
	return groupOptionValue[M]{kind: groupWhere, where: &value}
}
func GeneratedGroupHaving[M any](value GroupPredicate[M]) GroupOption[M] {
	return groupOptionValue[M]{kind: groupHaving, having: &value}
}
func GeneratedGroupOrderBy[M any](first GroupOrder[M], rest ...GroupOrder[M]) GroupOption[M] {
	return groupOptionValue[M]{kind: groupOrdering, orders: append([]GroupOrder[M]{first}, rest...)}
}
func GeneratedGroupTake[M any](value int) GroupOption[M] {
	return groupOptionValue[M]{kind: groupTake, number: value}
}
func GeneratedGroupSkip[M any](value int) GroupOption[M] {
	return groupOptionValue[M]{kind: groupSkip, number: value}
}
func GeneratedGroupBy[M any](model ModelID, options ...GroupOption[M]) GroupRequest[M] {
	result := GroupRequest[M]{model: model}
	seen := map[groupOptionKind]bool{}
	for _, option := range options {
		if option == nil {
			result.err = fmt.Errorf("P6_GROUP_OPTION: nil option")
			continue
		}
		value := option.groupOption(*new(M))
		if seen[value.kind] {
			result.err = fmt.Errorf("P6_GROUP_OPTION: duplicate option")
		}
		seen[value.kind] = true
		switch value.kind {
		case groupDimensions:
			result.dimensions = append([]aggregateIdentity(nil), value.dimensions...)
		case groupMeasures:
			result.measures = append([]aggregateIdentity(nil), value.measures...)
		case groupWhere:
			result.where = value.where
		case groupHaving:
			result.having = value.having
		case groupOrdering:
			result.orders = append([]GroupOrder[M](nil), value.orders...)
		case groupTake:
			v := value.number
			result.take = &v
		case groupSkip:
			v := value.number
			result.skip = &v
		}
	}
	if len(result.dimensions) == 0 {
		result.err = fmt.Errorf("P6_GROUP_DIMENSIONS: non-empty dimensions required")
	}
	if result.take != nil && *result.take == 0 {
		result.err = fmt.Errorf("P6_GROUP_TAKE: zero take")
	}
	if result.skip != nil && *result.skip < 0 {
		result.err = fmt.Errorf("P6_GROUP_SKIP: negative skip")
	}
	return result
}
func GroupValue[M, V any](row GroupRow[M], cell GroupCell[M, V]) ReadValue[V] {
	identity := cell.analyticsIdentity()
	if row.model != identity.model {
		return ReadValue[V]{}
	}
	return typedAnalyticsCell[V](row.cells[identity.key()])
}

type RelationGroupOption[M any] interface{ relationGroupOption(M) groupOptionValue[M] }
type relationGroupOptionValue[M any] struct{ groupOptionValue[M] }

func (value relationGroupOptionValue[M]) relationGroupOption(M) groupOptionValue[M] {
	return value.groupOptionValue
}

type RelationGroupRequest[M any] struct{ request GroupRequest[M] }
type RelationGroupRow[M any] struct {
	model ModelID
	cells map[string]readCell
}

func GeneratedRelationGroupDimensions[M any](first RelationGroupDimension[M], rest ...RelationGroupDimension[M]) RelationGroupOption[M] {
	values := []RelationGroupDimension[M]{first}
	values = append(values, rest...)
	ids := make([]aggregateIdentity, len(values))
	for i, v := range values {
		ids[i] = v.analyticsIdentity()
	}
	return relationGroupOptionValue[M]{groupOptionValue[M]{kind: groupDimensions, dimensions: ids}}
}
func GeneratedRelationGroupMeasures[M any](values ...AggregateMeasure[M]) RelationGroupOption[M] {
	ids := make([]aggregateIdentity, len(values))
	for i, v := range values {
		ids[i] = v.analyticsIdentity()
	}
	return relationGroupOptionValue[M]{groupOptionValue[M]{kind: groupMeasures, measures: ids}}
}
func GeneratedRelationGroupWhere[M any](v Predicate[M]) RelationGroupOption[M] {
	return relationGroupOptionValue[M]{groupOptionValue[M]{kind: groupWhere, where: &v}}
}
func GeneratedRelationGroupHaving[M any](v GroupPredicate[M]) RelationGroupOption[M] {
	return relationGroupOptionValue[M]{groupOptionValue[M]{kind: groupHaving, having: &v}}
}
func GeneratedRelationGroupOrderBy[M any](first GroupOrder[M], rest ...GroupOrder[M]) RelationGroupOption[M] {
	return relationGroupOptionValue[M]{groupOptionValue[M]{kind: groupOrdering, orders: append([]GroupOrder[M]{first}, rest...)}}
}
func GeneratedRelationGroupTake[M any](v int) RelationGroupOption[M] {
	return relationGroupOptionValue[M]{groupOptionValue[M]{kind: groupTake, number: v}}
}
func GeneratedRelationGroupSkip[M any](v int) RelationGroupOption[M] {
	return relationGroupOptionValue[M]{groupOptionValue[M]{kind: groupSkip, number: v}}
}
func GeneratedRelationGroupBy[M any](model ModelID, options ...RelationGroupOption[M]) RelationGroupRequest[M] {
	ordinary := make([]GroupOption[M], 0, len(options))
	for _, option := range options {
		ordinary = append(ordinary, optionAdapter[M]{option})
	}
	return RelationGroupRequest[M]{request: GeneratedGroupBy(model, ordinary...)}
}

type optionAdapter[M any] struct{ RelationGroupOption[M] }

func (value optionAdapter[M]) groupOption(model M) groupOptionValue[M] {
	return value.relationGroupOption(model)
}
func RelationGroupValue[M, V any](row RelationGroupRow[M], cell RelationGroupCell[M, V]) ReadValue[V] {
	identity := cell.analyticsIdentity()
	if row.model != identity.model {
		return ReadValue[V]{}
	}
	return typedAnalyticsCell[V](row.cells[identity.key()])
}

func typedAnalyticsCell[V any](cell readCell) ReadValue[V] {
	if cell.state == ReadNull {
		return ReadValue[V]{state: ReadNull}
	}
	typed, ok := coerceReadValue[V](cell.value)
	if !ok {
		return ReadValue[V]{}
	}
	return ReadValue[V]{state: ReadPresent, value: typed}
}

// Runtime constructors are the representation-safe decoder handoff.
type RuntimeAnalyticsCell struct {
	key   string
	state ReadState
	value any
}

func RuntimePresentAnalyticsCell(key string, value any) RuntimeAnalyticsCell {
	return RuntimeAnalyticsCell{key: key, state: ReadPresent, value: value}
}
func RuntimeNullAnalyticsCell(key string) RuntimeAnalyticsCell {
	return RuntimeAnalyticsCell{key: key, state: ReadNull}
}
func RuntimeAggregateResult[M any](model ModelID, cells ...RuntimeAnalyticsCell) AggregateResult[M] {
	return AggregateResult[M]{model: model, cells: runtimeAnalyticsCells(cells)}
}
func RuntimeGroupRow[M any](model ModelID, cells ...RuntimeAnalyticsCell) GroupRow[M] {
	return GroupRow[M]{model: model, cells: runtimeAnalyticsCells(cells)}
}
func RuntimeRelationGroupRow[M any](model ModelID, cells ...RuntimeAnalyticsCell) RelationGroupRow[M] {
	return RelationGroupRow[M]{model: model, cells: runtimeAnalyticsCells(cells)}
}
func runtimeAnalyticsCells(values []RuntimeAnalyticsCell) map[string]readCell {
	result := map[string]readCell{}
	for _, v := range values {
		result[v.key] = readCell{state: v.state, value: v.value}
	}
	return result
}

type AnalyticsOperation uint8

const (
	AnalyticsAggregate AnalyticsOperation = iota + 1
	AnalyticsGroupBy
	AnalyticsRelationGroupBy
)

type FrozenAnalyticsTerm struct {
	Model        ModelID
	Field        FieldID
	Operator     AggregateOperator
	RelationName string
	RelationPath []RelationID
}

func RuntimeAnalyticsTermKey(term FrozenAnalyticsTerm) string {
	return aggregateIdentity{model: term.Model, field: term.Field, operator: term.Operator, relation: term.RelationName, path: term.RelationPath}.key()
}

type FrozenAnalyticsOrder struct {
	Term       FrozenAnalyticsTerm
	Descending bool
}
type FrozenGroupPredicate struct {
	Kind     uint8
	Term     FrozenAnalyticsTerm
	Operator string
	Value    any
	Mode     FrozenComparisonMode
	Children []FrozenGroupPredicate
}
type FrozenAnalyticsRequest struct {
	operation            AnalyticsOperation
	model                ModelID
	where                *FrozenPredicate
	dimensions, measures []FrozenAnalyticsTerm
	having               *FrozenGroupPredicate
	orders               []FrozenAnalyticsOrder
	take, skip           *int
}

func (r FrozenAnalyticsRequest) Operation() AnalyticsOperation { return r.operation }
func (r FrozenAnalyticsRequest) ModelID() ModelID              { return r.model }
func (r FrozenAnalyticsRequest) Where() (FrozenPredicate, bool) {
	if r.where == nil {
		return FrozenPredicate{}, false
	}
	return *r.where, true
}
func (r FrozenAnalyticsRequest) Dimensions() []FrozenAnalyticsTerm {
	return cloneFrozenAnalyticsTerms(r.dimensions)
}
func (r FrozenAnalyticsRequest) Measures() []FrozenAnalyticsTerm {
	return cloneFrozenAnalyticsTerms(r.measures)
}
func (r FrozenAnalyticsRequest) Having() (FrozenGroupPredicate, bool) {
	if r.having == nil {
		return FrozenGroupPredicate{}, false
	}
	return cloneFrozenGroupPredicate(*r.having), true
}
func (r FrozenAnalyticsRequest) OrderBy() []FrozenAnalyticsOrder {
	result := make([]FrozenAnalyticsOrder, len(r.orders))
	for index, order := range r.orders {
		result[index] = order
		result[index].Term = cloneFrozenAnalyticsTerm(order.Term)
	}
	return result
}
func (r FrozenAnalyticsRequest) Take() (int, bool) {
	if r.take == nil {
		return 0, false
	}
	return *r.take, true
}
func (r FrozenAnalyticsRequest) Skip() (int, bool) {
	if r.skip == nil {
		return 0, false
	}
	return *r.skip, true
}

func RuntimeFreezeAggregateRequest[M any](request AggregateRequest[M]) (FrozenAnalyticsRequest, error) {
	if request.err != nil {
		return FrozenAnalyticsRequest{}, request.err
	}
	result := FrozenAnalyticsRequest{operation: AnalyticsAggregate, model: request.model, measures: freezeAnalyticsTerms(request.measures)}
	if request.where != nil {
		value, err := request.where.freezeForModel(request.model)
		if err != nil {
			return FrozenAnalyticsRequest{}, err
		}
		result.where = &value
	}
	if err := validateFrozenAnalytics(result); err != nil {
		return FrozenAnalyticsRequest{}, err
	}
	return result, nil
}
func RuntimeFreezeGroupRequest[M any](request GroupRequest[M]) (FrozenAnalyticsRequest, error) {
	return freezeGroupRequest(request, AnalyticsGroupBy)
}
func RuntimeFreezeRelationGroupRequest[M any](request RelationGroupRequest[M]) (FrozenAnalyticsRequest, error) {
	return freezeGroupRequest(request.request, AnalyticsRelationGroupBy)
}
func freezeGroupRequest[M any](request GroupRequest[M], operation AnalyticsOperation) (FrozenAnalyticsRequest, error) {
	if request.err != nil {
		return FrozenAnalyticsRequest{}, request.err
	}
	result := FrozenAnalyticsRequest{operation: operation, model: request.model, dimensions: freezeAnalyticsTerms(request.dimensions), measures: freezeAnalyticsTerms(request.measures), take: request.take, skip: request.skip}
	if request.where != nil {
		value, err := request.where.freezeForModel(request.model)
		if err != nil {
			return FrozenAnalyticsRequest{}, err
		}
		result.where = &value
	}
	if request.having != nil {
		value := freezeGroupPredicateTyped(*request.having)
		result.having = &value
	}
	for _, order := range request.orders {
		result.orders = append(result.orders, FrozenAnalyticsOrder{Term: freezeAnalyticsTerm(order.identity), Descending: order.descending})
	}
	if err := validateFrozenAnalytics(result); err != nil {
		return FrozenAnalyticsRequest{}, err
	}
	return result, nil
}
func freezeAnalyticsTerm(value aggregateIdentity) FrozenAnalyticsTerm {
	return FrozenAnalyticsTerm{Model: value.model, Field: value.field, Operator: value.operator, RelationName: value.relation, RelationPath: append([]RelationID(nil), value.path...)}
}
func freezeAnalyticsTerms(values []aggregateIdentity) []FrozenAnalyticsTerm {
	result := make([]FrozenAnalyticsTerm, len(values))
	for i, v := range values {
		result[i] = freezeAnalyticsTerm(v)
	}
	return result
}
func cloneFrozenAnalyticsTerm(value FrozenAnalyticsTerm) FrozenAnalyticsTerm {
	value.RelationPath = append([]RelationID(nil), value.RelationPath...)
	return value
}
func cloneFrozenAnalyticsTerms(values []FrozenAnalyticsTerm) []FrozenAnalyticsTerm {
	result := make([]FrozenAnalyticsTerm, len(values))
	for index, value := range values {
		result[index] = cloneFrozenAnalyticsTerm(value)
	}
	return result
}
func freezeGroupPredicateTyped[M any](value GroupPredicate[M]) FrozenGroupPredicate {
	result := FrozenGroupPredicate{Kind: uint8(value.kind), Term: freezeAnalyticsTerm(value.identity), Operator: value.operator, Value: value.value, Mode: value.mode}
	for _, child := range value.children {
		result.Children = append(result.Children, freezeGroupPredicateTyped(child))
	}
	return result
}
func cloneFrozenGroupPredicate(value FrozenGroupPredicate) FrozenGroupPredicate {
	result := value
	result.Term = cloneFrozenAnalyticsTerm(value.Term)
	result.Children = make([]FrozenGroupPredicate, len(value.Children))
	for i, c := range value.Children {
		result.Children[i] = cloneFrozenGroupPredicate(c)
	}
	return result
}
func validateFrozenAnalytics(value FrozenAnalyticsRequest) error {
	if value.model == (ModelID{}) {
		return fmt.Errorf("P6_ANALYTICS_MODEL: zero model identity")
	}
	switch value.operation {
	case AnalyticsAggregate:
		if len(value.dimensions) != 0 || len(value.measures) == 0 || value.having != nil || len(value.orders) != 0 || value.take != nil || value.skip != nil {
			return fmt.Errorf("P6_ANALYTICS_SHAPE: invalid aggregate request")
		}
	case AnalyticsGroupBy, AnalyticsRelationGroupBy:
		if len(value.dimensions) == 0 {
			return fmt.Errorf("P6_GROUP_DIMENSIONS: non-empty dimensions required")
		}
	default:
		return fmt.Errorf("P6_ANALYTICS_OPERATION: invalid operation")
	}
	if value.take != nil && *value.take == 0 {
		return fmt.Errorf("P6_GROUP_TAKE: zero take")
	}
	if value.skip != nil && *value.skip < 0 {
		return fmt.Errorf("P6_GROUP_SKIP: negative skip")
	}
	dimensions := map[string]bool{}
	seenRelation := false
	for _, term := range value.dimensions {
		if err := validateFrozenTerm(value.model, term, false); err != nil {
			return err
		}
		if term.Operator != 0 {
			return fmt.Errorf("P6_ANALYTICS_DIMENSION: dimension has an aggregate operator")
		}
		if value.operation == AnalyticsGroupBy && term.RelationName != "" {
			return fmt.Errorf("P6_ANALYTICS_RELATION: relation dimension in local group request")
		}
		seenRelation = seenRelation || term.RelationName != ""
		key := frozenTermKey(term)
		if dimensions[key] {
			return fmt.Errorf("P6_ANALYTICS_DUPLICATE: duplicate dimension")
		}
		dimensions[key] = true
	}
	if value.operation == AnalyticsRelationGroupBy && !seenRelation {
		return fmt.Errorf("P6_ANALYTICS_RELATION: relation group requires a relation dimension")
	}
	measures := map[string]bool{}
	for _, term := range value.measures {
		if err := validateFrozenTerm(value.model, term, true); err != nil {
			return err
		}
		key := frozenTermKey(term)
		if measures[key] {
			return fmt.Errorf("P6_ANALYTICS_DUPLICATE: duplicate measure")
		}
		measures[key] = true
	}
	if value.having != nil {
		if err := validateFrozenHaving(value.model, *value.having); err != nil {
			return err
		}
		var havingTerms []FrozenAnalyticsTerm
		collectFrozenHavingTerms(*value.having, &havingTerms)
		for _, term := range havingTerms {
			if term.Operator == 0 && !dimensions[frozenTermKey(term)] {
				return fmt.Errorf("P6_ANALYTICS_HAVING: dimension is not grouped")
			}
		}
	}
	orders := map[string]bool{}
	for _, order := range value.orders {
		if err := validateFrozenTerm(value.model, order.Term, order.Term.Operator != 0); err != nil {
			return err
		}
		key := frozenTermKey(order.Term)
		if orders[key] {
			return fmt.Errorf("P6_ANALYTICS_ORDER: duplicate order term")
		}
		orders[key] = true
		if order.Term.Operator == 0 && !dimensions[key] {
			return fmt.Errorf("P6_ANALYTICS_ORDER: ordered dimension is not grouped")
		}
	}
	return nil
}

func collectFrozenHavingTerms(value FrozenGroupPredicate, result *[]FrozenAnalyticsTerm) {
	if groupPredicateKind(value.Kind) == groupCompare {
		*result = append(*result, value.Term)
	}
	for _, child := range value.Children {
		collectFrozenHavingTerms(child, result)
	}
}

func validateFrozenTerm(model ModelID, term FrozenAnalyticsTerm, measure bool) error {
	if term.Model != model {
		return fmt.Errorf("P6_ANALYTICS_MODEL: foreign-model term")
	}
	if measure {
		if term.RelationName != "" {
			return fmt.Errorf("P6_ANALYTICS_RELATION: measures over related models are not supported")
		}
		if term.Operator < AggregateCountAll || term.Operator > AggregateMaximum {
			return fmt.Errorf("P6_ANALYTICS_MEASURE: invalid aggregate operator")
		}
		if term.Operator == AggregateCountAll {
			if term.Field != (FieldID{}) || term.RelationName != "" {
				return fmt.Errorf("P6_ANALYTICS_MEASURE: malformed count-all")
			}
			return nil
		}
	}
	if term.RelationName == "" && len(term.RelationPath) != 0 {
		return fmt.Errorf("P6_ANALYTICS_RELATION: local term carries a relation path")
	}
	if term.RelationName != "" {
		if len(term.RelationPath) == 0 {
			return fmt.Errorf("P6_ANALYTICS_RELATION: relation dimension has an empty path")
		}
		for _, relation := range term.RelationPath {
			if relation == (RelationID{}) {
				return fmt.Errorf("P6_ANALYTICS_RELATION: relation dimension has a zero path identity")
			}
		}
	}
	if term.Field == (FieldID{}) {
		return fmt.Errorf("P6_ANALYTICS_FIELD: zero field identity")
	}
	return nil
}

func validateFrozenHaving(model ModelID, value FrozenGroupPredicate) error {
	switch groupPredicateKind(value.Kind) {
	case groupCompare:
		if len(value.Children) != 0 {
			return fmt.Errorf("P6_ANALYTICS_HAVING: comparison cannot have children")
		}
		if err := validateFrozenTerm(model, value.Term, value.Term.Operator != 0); err != nil {
			return err
		}
		switch value.Operator {
		case "eq", "ne", "lt", "lte", "gt", "gte":
			if value.Value == nil {
				return fmt.Errorf("P6_ANALYTICS_HAVING: comparison value is nil")
			}
			if value.Mode != 0 && value.Mode != FrozenComparisonSensitive {
				return fmt.Errorf("P6_ANALYTICS_HAVING: ordered/equality comparison mode is invalid")
			}
		case "contains", "startsWith", "endsWith":
			if value.Value == nil || (value.Mode != FrozenComparisonSensitive && value.Mode != FrozenComparisonASCIIInsensitive) {
				return fmt.Errorf("P6_ANALYTICS_HAVING: text comparison value or mode is invalid")
			}
		case "isNull", "isNotNull":
		default:
			return fmt.Errorf("P6_ANALYTICS_HAVING: invalid operator")
		}
	case groupAnd, groupOr:
		if len(value.Children) == 0 {
			return fmt.Errorf("P6_ANALYTICS_HAVING: logical predicate has no children")
		}
		for _, child := range value.Children {
			if err := validateFrozenHaving(model, child); err != nil {
				return err
			}
		}
	case groupNot:
		if len(value.Children) != 1 {
			return fmt.Errorf("P6_ANALYTICS_HAVING: not requires one child")
		}
		return validateFrozenHaving(model, value.Children[0])
	default:
		return fmt.Errorf("P6_ANALYTICS_HAVING: invalid predicate kind")
	}
	return nil
}

func frozenTermKey(term FrozenAnalyticsTerm) string {
	return fmt.Sprintf("%x:%x:%d:%s:%s", term.Model, term.Field, term.Operator, term.RelationName, relationPathKey(term.RelationPath))
}

func relationPathKey(path []RelationID) string {
	if len(path) == 0 {
		return "-"
	}
	parts := make([]string, len(path))
	for index, relation := range path {
		parts[index] = fmt.Sprintf("%x", relation)
	}
	return strings.Join(parts, "/")
}
