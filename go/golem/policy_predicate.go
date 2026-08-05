package golem

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"time"
	"unicode/utf8"
)

const frozenPolicyVersion uint16 = 1

// FrozenConditionKind is the closed public-view node family. Its numeric values
// are persisted by the versioned public freeze encoding.
type FrozenConditionKind uint8

const (
	FrozenConditionConstant FrozenConditionKind = 1
	FrozenConditionLogical  FrozenConditionKind = 2
	FrozenConditionScalar   FrozenConditionKind = 3
	FrozenConditionList     FrozenConditionKind = 4
	FrozenConditionJSON     FrozenConditionKind = 5
	FrozenConditionRelation FrozenConditionKind = 6
)

// FrozenOperator is the stable public-view operation identity. It is evidence
// for the internal binder, not an author-facing predicate constructor.
type FrozenOperator uint16

const (
	FrozenOperatorNone FrozenOperator = 0

	FrozenOperatorAnd FrozenOperator = 1
	FrozenOperatorOr  FrozenOperator = 2
	FrozenOperatorNot FrozenOperator = 3

	FrozenOperatorEq         FrozenOperator = 10
	FrozenOperatorNe         FrozenOperator = 11
	FrozenOperatorIn         FrozenOperator = 12
	FrozenOperatorNotIn      FrozenOperator = 13
	FrozenOperatorLT         FrozenOperator = 14
	FrozenOperatorLTE        FrozenOperator = 15
	FrozenOperatorGT         FrozenOperator = 16
	FrozenOperatorGTE        FrozenOperator = 17
	FrozenOperatorContains   FrozenOperator = 18
	FrozenOperatorStartsWith FrozenOperator = 19
	FrozenOperatorEndsWith   FrozenOperator = 20
	FrozenOperatorIsNull     FrozenOperator = 21
	FrozenOperatorIsNotNull  FrozenOperator = 22

	FrozenOperatorListHas      FrozenOperator = 30
	FrozenOperatorListHasEvery FrozenOperator = 31
	FrozenOperatorListHasSome  FrozenOperator = 32
	FrozenOperatorListIsEmpty  FrozenOperator = 33
	FrozenOperatorListEq       FrozenOperator = 34

	FrozenOperatorRelationIs        FrozenOperator = 40
	FrozenOperatorRelationIsNot     FrozenOperator = 41
	FrozenOperatorRelationIsNull    FrozenOperator = 42
	FrozenOperatorRelationIsNotNull FrozenOperator = 43
	FrozenOperatorRelationSome      FrozenOperator = 44
	FrozenOperatorRelationEvery     FrozenOperator = 45
	FrozenOperatorRelationNone      FrozenOperator = 46
)

const (
	frozenOperatorAnd               = FrozenOperatorAnd
	frozenOperatorOr                = FrozenOperatorOr
	frozenOperatorNot               = FrozenOperatorNot
	frozenOperatorEq                = FrozenOperatorEq
	frozenOperatorNe                = FrozenOperatorNe
	frozenOperatorIn                = FrozenOperatorIn
	frozenOperatorNotIn             = FrozenOperatorNotIn
	frozenOperatorLT                = FrozenOperatorLT
	frozenOperatorLTE               = FrozenOperatorLTE
	frozenOperatorGT                = FrozenOperatorGT
	frozenOperatorGTE               = FrozenOperatorGTE
	frozenOperatorContains          = FrozenOperatorContains
	frozenOperatorStartsWith        = FrozenOperatorStartsWith
	frozenOperatorEndsWith          = FrozenOperatorEndsWith
	frozenOperatorIsNull            = FrozenOperatorIsNull
	frozenOperatorIsNotNull         = FrozenOperatorIsNotNull
	frozenOperatorListHas           = FrozenOperatorListHas
	frozenOperatorListHasEvery      = FrozenOperatorListHasEvery
	frozenOperatorListHasSome       = FrozenOperatorListHasSome
	frozenOperatorListIsEmpty       = FrozenOperatorListIsEmpty
	frozenOperatorListEq            = FrozenOperatorListEq
	frozenOperatorRelationIs        = FrozenOperatorRelationIs
	frozenOperatorRelationIsNot     = FrozenOperatorRelationIsNot
	frozenOperatorRelationIsNull    = FrozenOperatorRelationIsNull
	frozenOperatorRelationIsNotNull = FrozenOperatorRelationIsNotNull
	frozenOperatorRelationSome      = FrozenOperatorRelationSome
	frozenOperatorRelationEvery     = FrozenOperatorRelationEvery
	frozenOperatorRelationNone      = FrozenOperatorRelationNone
)

type FrozenComparisonMode uint8

const FrozenComparisonSensitive FrozenComparisonMode = 1

type FrozenOperandKind uint8

const (
	FrozenOperandNone FrozenOperandKind = 1
	FrozenOperandOne  FrozenOperandKind = 2
	FrozenOperandMany FrozenOperandKind = 3
	FrozenOperandFlag FrozenOperandKind = 4
)

type FrozenValueKind uint8

const (
	FrozenValueBool     FrozenValueKind = 1
	FrozenValueInt16    FrozenValueKind = 2
	FrozenValueInt32    FrozenValueKind = 3
	FrozenValueInt64    FrozenValueKind = 4
	FrozenValueFloat32  FrozenValueKind = 5
	FrozenValueFloat64  FrozenValueKind = 6
	FrozenValueDecimal  FrozenValueKind = 7
	FrozenValueString   FrozenValueKind = 8
	FrozenValueBytes    FrozenValueKind = 9
	FrozenValueUUID     FrozenValueKind = 10
	FrozenValueDate     FrozenValueKind = 11
	FrozenValueTime     FrozenValueKind = 12
	FrozenValueDateTime FrozenValueKind = 13
)

type FrozenAction uint8

const (
	FrozenActionRead   FrozenAction = 1
	FrozenActionCreate FrozenAction = 2
	FrozenActionUpdate FrozenAction = 3
	FrozenActionDelete FrozenAction = 4
)

type FrozenEffect uint8

const (
	FrozenEffectGrant FrozenEffect = 1
	FrozenEffectDeny  FrozenEffect = 2
)

const (
	frozenActionRead   = FrozenActionRead
	frozenActionCreate = FrozenActionCreate
	frozenActionUpdate = FrozenActionUpdate
	frozenActionDelete = FrozenActionDelete
	frozenEffectGrant  = FrozenEffectGrant
	frozenEffectDeny   = FrozenEffectDeny
)

// FrozenRelationRef is a copyable endpoint reference. Its fields remain opaque
// so callers cannot mutate a returned condition view.
type FrozenRelationRef struct {
	field    FieldID
	relation RelationID
}

func (reference FrozenRelationRef) FieldID() FieldID       { return reference.field }
func (reference FrozenRelationRef) RelationID() RelationID { return reference.relation }

// FrozenJSONPathView is reserved by the public seam even while JSON policy
// handles remain closed. Present is false for all currently emitted nodes.
type FrozenJSONPathView interface {
	sealedFrozenJSONPathView()
	Present() bool
	Segments() []JSONPathSegment
}

type frozenJSONPathView struct{}

func (frozenJSONPathView) sealedFrozenJSONPathView()   {}
func (frozenJSONPathView) Present() bool               { return false }
func (frozenJSONPathView) Segments() []JSONPathSegment { return nil }

// FrozenValueView exposes one exact, immutable operand value through typed
// accessors. Exactly one accessor succeeds according to Kind.
type FrozenValueView interface {
	sealedFrozenValueView()
	Kind() FrozenValueKind
	Bool() (bool, bool)
	Signed() (value int64, width uint8, ok bool)
	FloatBits() (bits uint64, width uint8, ok bool)
	Decimal() (Decimal, bool)
	String() (string, bool)
	Bytes() ([]byte, bool)
	UUID() (UUID, bool)
	Date() (Date, bool)
	Time() (Time, bool)
	DateTime() (unixSeconds int64, nanosecond uint32, ok bool)
}

// FrozenOperandView is a closed arity-tagged operand. Many returns a fresh
// slice and every contained value view is immutable.
type FrozenOperandView interface {
	sealedFrozenOperandView()
	Kind() FrozenOperandKind
	One() (FrozenValueView, bool)
	Many() []FrozenValueView
	Flag() (bool, bool)
}

type FrozenPredicateView interface {
	sealedFrozenPredicateView()
	Version() uint16
	RootModelID() ModelID
	Root() FrozenConditionView
	CanonicalBytes() []byte
}

type FrozenConditionView interface {
	sealedFrozenConditionView()
	Kind() FrozenConditionKind
	Operator() FrozenOperator
	FieldID() (FieldID, bool)
	Relation() (FrozenRelationRef, bool)
	Mode() FrozenComparisonMode
	Path() FrozenJSONPathView
	Operand() FrozenOperandView
	Children() []FrozenConditionView
}

type FrozenRuleView interface {
	sealedFrozenRuleView()
	Action() FrozenAction
	Effect() FrozenEffect
	ModelID() ModelID
	IsUnconditional() bool
	Condition() (FrozenPredicateView, bool)
	Fields() (fields []FieldID, modelWide bool)
	Position() uint32
}

type FrozenPolicyView interface {
	sealedFrozenPolicyView()
	Version() uint16
	ModelID() ModelID
	Rules() []FrozenRuleView
	CanonicalBytes() []byte
}

type predicateNode struct {
	kind     FrozenConditionKind
	operator FrozenOperator
	mode     FrozenComparisonMode
	truth    bool
	field    FieldID
	relation RelationID
	operand  frozenOperand
	children []*predicateNode
}

type frozenCondition struct {
	kind     FrozenConditionKind
	operator FrozenOperator
	mode     FrozenComparisonMode
	truth    bool
	field    FieldID
	relation RelationID
	operand  frozenOperand
	children []*frozenCondition
}

type frozenOperand struct {
	kind    FrozenOperandKind
	one     frozenValue
	many    []frozenValue
	flag    bool
	invalid string
}

type frozenValue struct {
	kind       FrozenValueKind
	boolean    bool
	signed     int64
	floatBits  uint64
	decimal    Decimal
	text       string
	bytes      []byte
	uuid       UUID
	date       Date
	clock      Time
	seconds    int64
	nanosecond uint32
	invalid    string
}

func predicateConstant[M any](truth bool) Predicate[M] {
	return Predicate[M]{node: &predicateNode{kind: FrozenConditionConstant, truth: truth, operand: flagOperand(truth)}}
}

func predicateLogical[M any](operator FrozenOperator, values []Predicate[M]) Predicate[M] {
	children := make([]*predicateNode, len(values))
	for index, value := range values {
		children[index] = value.node
	}
	return Predicate[M]{node: &predicateNode{kind: FrozenConditionLogical, operator: operator, operand: noOperand(), children: children}}
}

func predicateNot[M any](value Predicate[M]) Predicate[M] {
	return Predicate[M]{node: &predicateNode{kind: FrozenConditionLogical, operator: frozenOperatorNot, operand: noOperand(), children: []*predicateNode{value.node}}}
}

func predicateScalar[M any](field FieldID, operator FrozenOperator, operand frozenOperand) Predicate[M] {
	return Predicate[M]{node: &predicateNode{kind: FrozenConditionScalar, field: field, operator: operator, mode: FrozenComparisonSensitive, operand: operand}}
}

func predicatePresence[M any](field FieldID, operator FrozenOperator) Predicate[M] {
	return predicateScalar[M](field, operator, noOperand())
}

func predicateList[M any](field FieldID, operator FrozenOperator, operand frozenOperand) Predicate[M] {
	return Predicate[M]{node: &predicateNode{kind: FrozenConditionList, field: field, operator: operator, operand: operand}}
}

func predicateJSONPresence[M any](field FieldID, operator FrozenOperator) Predicate[M] {
	return Predicate[M]{node: &predicateNode{kind: FrozenConditionJSON, field: field, operator: operator, mode: FrozenComparisonSensitive, operand: noOperand()}}
}

func predicateRelation[M any](field FieldID, relation RelationID, operator FrozenOperator, child *predicateNode) Predicate[M] {
	children := []*predicateNode(nil)
	if child != nil {
		children = []*predicateNode{child}
	}
	return Predicate[M]{node: &predicateNode{kind: FrozenConditionRelation, field: field, relation: relation, operator: operator, operand: noOperand(), children: children}}
}

func noOperand() frozenOperand { return frozenOperand{kind: FrozenOperandNone} }
func flagOperand(value bool) frozenOperand {
	return frozenOperand{kind: FrozenOperandFlag, flag: value}
}
func stringOperand(value string) frozenOperand {
	return frozenOperand{kind: FrozenOperandOne, one: frozenValue{kind: FrozenValueString, text: value}}
}
func bytesOperand(value []byte) frozenOperand {
	return frozenOperand{kind: FrozenOperandOne, one: frozenValue{kind: FrozenValueBytes, bytes: append([]byte{}, value...)}}
}
func bytesOperands(values [][]byte) frozenOperand {
	result := make([]frozenValue, len(values))
	for index, value := range values {
		result[index] = frozenValue{kind: FrozenValueBytes, bytes: append([]byte{}, value...)}
	}
	return frozenOperand{kind: FrozenOperandMany, many: result}
}
func scalarOperand[V EqualValue](value V) frozenOperand {
	return frozenOperand{kind: FrozenOperandOne, one: scalarValue(value)}
}
func scalarOperands[V EqualValue](values []V) frozenOperand {
	result := make([]frozenValue, len(values))
	for index, value := range values {
		result[index] = scalarValue(value)
	}
	return frozenOperand{kind: FrozenOperandMany, many: result}
}

func scalarValue[V EqualValue](value V) frozenValue {
	switch exact := any(value).(type) {
	case UUID:
		return frozenValue{kind: FrozenValueUUID, uuid: exact}
	case Decimal:
		return frozenValue{kind: FrozenValueDecimal, decimal: exact}
	case Date:
		return frozenValue{kind: FrozenValueDate, date: exact}
	case Time:
		return frozenValue{kind: FrozenValueTime, clock: exact}
	case time.Time:
		normalized := exact.UTC()
		return frozenValue{kind: FrozenValueDateTime, seconds: normalized.Unix(), nanosecond: uint32(normalized.Nanosecond())}
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Bool:
		return frozenValue{kind: FrozenValueBool, boolean: reflected.Bool()}
	case reflect.Int16:
		return frozenValue{kind: FrozenValueInt16, signed: reflected.Int()}
	case reflect.Int32:
		return frozenValue{kind: FrozenValueInt32, signed: reflected.Int()}
	case reflect.Int64:
		return frozenValue{kind: FrozenValueInt64, signed: reflected.Int()}
	case reflect.Float32:
		value := float32(reflected.Float())
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return frozenValue{kind: FrozenValueFloat32, invalid: "non-finite float32 operand"}
		}
		if value == 0 {
			value = 0
		}
		return frozenValue{kind: FrozenValueFloat32, floatBits: uint64(math.Float32bits(value))}
	case reflect.Float64:
		value := reflected.Float()
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return frozenValue{kind: FrozenValueFloat64, invalid: "non-finite float64 operand"}
		}
		if value == 0 {
			value = 0
		}
		return frozenValue{kind: FrozenValueFloat64, floatBits: math.Float64bits(value)}
	case reflect.String:
		return frozenValue{kind: FrozenValueString, text: reflected.String()}
	default:
		return frozenValue{invalid: fmt.Sprintf("unsupported operand type %T", value)}
	}
}

type FreezeErrorCode string

const (
	FreezeInvalidModel     FreezeErrorCode = "P2_FREEZE_INVALID_MODEL"
	FreezeInvalidPredicate FreezeErrorCode = "P2_FREEZE_INVALID_PREDICATE"
	FreezeInvalidValue     FreezeErrorCode = "P2_FREEZE_INVALID_VALUE"
	FreezeInvalidField     FreezeErrorCode = "P2_FREEZE_INVALID_FIELD"
	FreezeInvalidRelation  FreezeErrorCode = "P2_FREEZE_INVALID_RELATION"
	FreezeInvalidRule      FreezeErrorCode = "P2_FREEZE_INVALID_RULE"
)

type FreezeError struct {
	Code         FreezeErrorCode
	RulePosition uint32
	HasRule      bool
	Detail       string
}

func (failure *FreezeError) Error() string {
	if failure == nil {
		return ""
	}
	if failure.HasRule {
		return fmt.Sprintf("%s at rule %d: %s", failure.Code, failure.RulePosition, failure.Detail)
	}
	return fmt.Sprintf("%s: %s", failure.Code, failure.Detail)
}

func freezeFailure(code FreezeErrorCode, detail string) error {
	return &FreezeError{Code: code, Detail: detail}
}

// FrozenPredicate is an opaque, immutable public freeze product.
type FrozenPredicate struct {
	rootModel ModelID
	root      *frozenCondition
	canonical []byte
}

func (predicate Predicate[M]) Freeze(root ModelDescriptor[M]) (FrozenPredicate, error) {
	model := root.Metadata().ModelID()
	if model == (ModelID{}) {
		return FrozenPredicate{}, freezeFailure(FreezeInvalidModel, "model descriptor has a zero identity")
	}
	condition, err := freezePredicateNode(predicate.node, make(map[*predicateNode]bool), 0)
	if err != nil {
		return FrozenPredicate{}, err
	}
	canonical, err := encodeFrozenPredicate(model, condition)
	if err != nil {
		return FrozenPredicate{}, freezeFailure(FreezeInvalidPredicate, err.Error())
	}
	return FrozenPredicate{rootModel: model, root: condition, canonical: canonical}, nil
}

func (predicate FrozenPredicate) View() FrozenPredicateView {
	return frozenPredicateView{predicate: predicate}
}

func (predicate FrozenPredicate) Version() uint16 { return frozenPolicyVersion }
func (predicate FrozenPredicate) CanonicalBytes() []byte {
	return append([]byte(nil), predicate.canonical...)
}

func freezePredicateNode(node *predicateNode, active map[*predicateNode]bool, depth int) (*frozenCondition, error) {
	if node == nil {
		return nil, freezeFailure(FreezeInvalidPredicate, "zero predicate")
	}
	if depth > 256 {
		return nil, freezeFailure(FreezeInvalidPredicate, "predicate nesting exceeds 256 nodes")
	}
	if active[node] {
		return nil, freezeFailure(FreezeInvalidPredicate, "predicate contains a cycle")
	}
	active[node] = true
	defer delete(active, node)

	switch node.kind {
	case FrozenConditionConstant:
		if node.operator != FrozenOperatorNone || node.mode != 0 || node.field != (FieldID{}) || node.relation != (RelationID{}) || len(node.children) != 0 {
			return nil, freezeFailure(FreezeInvalidPredicate, "malformed constant node")
		}
		return &frozenCondition{kind: FrozenConditionConstant, truth: node.truth, operand: flagOperand(node.truth)}, nil
	case FrozenConditionLogical:
		if node.mode != 0 || node.field != (FieldID{}) || node.relation != (RelationID{}) || node.operand.kind != FrozenOperandNone {
			return nil, freezeFailure(FreezeInvalidPredicate, "malformed logical node")
		}
		children := make([]*frozenCondition, len(node.children))
		for index, child := range node.children {
			frozen, err := freezePredicateNode(child, active, depth+1)
			if err != nil {
				return nil, err
			}
			children[index] = frozen
		}
		switch node.operator {
		case frozenOperatorAnd, frozenOperatorOr:
			return normalizeLogical(node.operator, children), nil
		case frozenOperatorNot:
			if len(children) != 1 {
				return nil, freezeFailure(FreezeInvalidPredicate, "Not requires exactly one child")
			}
			return normalizeNot(children[0]), nil
		default:
			return nil, freezeFailure(FreezeInvalidPredicate, "unknown logical operator")
		}
	case FrozenConditionScalar:
		if node.field == (FieldID{}) {
			return nil, freezeFailure(FreezeInvalidField, "scalar handle has a zero field identity")
		}
		if node.mode != FrozenComparisonSensitive || node.relation != (RelationID{}) || len(node.children) != 0 || !validScalarShape(node.operator, node.operand.kind) {
			return nil, freezeFailure(FreezeInvalidPredicate, "malformed scalar node")
		}
		if err := validateFrozenOperand(node.operand); err != nil {
			return nil, err
		}
		return &frozenCondition{kind: node.kind, operator: node.operator, mode: node.mode, field: node.field, operand: cloneFrozenOperand(node.operand)}, nil
	case FrozenConditionList:
		if node.field == (FieldID{}) {
			return nil, freezeFailure(FreezeInvalidField, "list handle has a zero field identity")
		}
		if node.mode != 0 || node.relation != (RelationID{}) || len(node.children) != 0 || !validListShape(node.operator, node.operand.kind) {
			return nil, freezeFailure(FreezeInvalidPredicate, "malformed list node")
		}
		if err := validateFrozenOperand(node.operand); err != nil {
			return nil, err
		}
		return &frozenCondition{kind: node.kind, operator: node.operator, field: node.field, operand: cloneFrozenOperand(node.operand)}, nil
	case FrozenConditionJSON:
		if node.field == (FieldID{}) {
			return nil, freezeFailure(FreezeInvalidField, "JSON handle has a zero field identity")
		}
		if node.mode != FrozenComparisonSensitive || node.relation != (RelationID{}) || len(node.children) != 0 || (node.operator != FrozenOperatorIsNull && node.operator != FrozenOperatorIsNotNull) || node.operand.kind != FrozenOperandNone {
			return nil, freezeFailure(FreezeInvalidPredicate, "malformed JSON node")
		}
		return &frozenCondition{kind: node.kind, operator: node.operator, mode: node.mode, field: node.field, operand: noOperand()}, nil
	case FrozenConditionRelation:
		if node.field == (FieldID{}) {
			return nil, freezeFailure(FreezeInvalidField, "relation handle has a zero field identity")
		}
		if node.relation == (RelationID{}) {
			return nil, freezeFailure(FreezeInvalidRelation, "relation handle has a zero relation identity")
		}
		if node.mode != 0 || node.operand.kind != FrozenOperandNone || !validRelationShape(node.operator, len(node.children)) {
			return nil, freezeFailure(FreezeInvalidPredicate, "malformed relation node")
		}
		children := make([]*frozenCondition, len(node.children))
		for index, child := range node.children {
			frozen, err := freezePredicateNode(child, active, depth+1)
			if err != nil {
				return nil, err
			}
			children[index] = frozen
		}
		return &frozenCondition{kind: node.kind, operator: node.operator, field: node.field, relation: node.relation, operand: noOperand(), children: children}, nil
	default:
		return nil, freezeFailure(FreezeInvalidPredicate, "unknown condition kind")
	}
}

func validScalarShape(operator FrozenOperator, operand FrozenOperandKind) bool {
	switch operator {
	case FrozenOperatorEq, FrozenOperatorNe, FrozenOperatorLT, FrozenOperatorLTE, FrozenOperatorGT, FrozenOperatorGTE,
		FrozenOperatorContains, FrozenOperatorStartsWith, FrozenOperatorEndsWith:
		return operand == FrozenOperandOne
	case FrozenOperatorIn, FrozenOperatorNotIn:
		return operand == FrozenOperandMany
	case FrozenOperatorIsNull, FrozenOperatorIsNotNull:
		return operand == FrozenOperandNone
	default:
		return false
	}
}

func validListShape(operator FrozenOperator, operand FrozenOperandKind) bool {
	switch operator {
	case FrozenOperatorListHas:
		return operand == FrozenOperandOne
	case FrozenOperatorListHasEvery, FrozenOperatorListHasSome, FrozenOperatorListEq:
		return operand == FrozenOperandMany
	case FrozenOperatorListIsEmpty:
		return operand == FrozenOperandFlag
	case FrozenOperatorIsNull, FrozenOperatorIsNotNull:
		return operand == FrozenOperandNone
	default:
		return false
	}
}

func validRelationShape(operator FrozenOperator, childCount int) bool {
	switch operator {
	case FrozenOperatorRelationIs, FrozenOperatorRelationIsNot, FrozenOperatorRelationSome, FrozenOperatorRelationEvery, FrozenOperatorRelationNone:
		return childCount == 1
	case FrozenOperatorRelationIsNull, FrozenOperatorRelationIsNotNull:
		return childCount == 0
	default:
		return false
	}
}

func validateFrozenOperand(operand frozenOperand) error {
	if operand.invalid != "" {
		return freezeFailure(FreezeInvalidValue, operand.invalid)
	}
	values := operand.many
	if operand.kind == FrozenOperandOne {
		values = []frozenValue{operand.one}
	}
	for _, value := range values {
		if value.invalid != "" {
			return freezeFailure(FreezeInvalidValue, value.invalid)
		}
		switch value.kind {
		case FrozenValueBool, FrozenValueInt16, FrozenValueInt32, FrozenValueInt64,
			FrozenValueFloat32, FrozenValueFloat64, FrozenValueUUID:
		case FrozenValueDecimal:
			if value.decimal.scale > 18 || magnitude(value.decimal.coefficient) > maxDecimalCoefficient {
				return freezeFailure(FreezeInvalidValue, "decimal operand is outside the portable precision ceiling")
			}
		case FrozenValueString:
			if !utf8.ValidString(value.text) {
				return freezeFailure(FreezeInvalidValue, "string operand is not valid UTF-8")
			}
		case FrozenValueBytes:
		case FrozenValueDate:
			if _, err := NewDate(int(value.date.year), time.Month(value.date.month), int(value.date.day)); err != nil {
				return freezeFailure(FreezeInvalidValue, "date operand is invalid")
			}
		case FrozenValueTime:
			if value.clock.microseconds < 0 || value.clock.microseconds >= 24*60*60*1_000_000 {
				return freezeFailure(FreezeInvalidValue, "time operand is outside [00:00:00, 24:00:00)")
			}
		case FrozenValueDateTime:
			if value.nanosecond >= 1_000_000_000 {
				return freezeFailure(FreezeInvalidValue, "datetime nanosecond is invalid")
			}
		default:
			return freezeFailure(FreezeInvalidValue, "operand has an unknown value kind")
		}
	}
	return nil
}

func normalizeLogical(operator FrozenOperator, children []*frozenCondition) *frozenCondition {
	flattened := make([]*frozenCondition, 0, len(children))
	for _, child := range children {
		if child.kind == FrozenConditionConstant {
			if operator == FrozenOperatorAnd && !child.truth {
				return &frozenCondition{kind: FrozenConditionConstant, truth: false, operand: flagOperand(false)}
			}
			if operator == FrozenOperatorOr && child.truth {
				return &frozenCondition{kind: FrozenConditionConstant, truth: true, operand: flagOperand(true)}
			}
			if (operator == FrozenOperatorAnd && child.truth) || (operator == FrozenOperatorOr && !child.truth) {
				continue
			}
		}
		if child.kind == FrozenConditionLogical && child.operator == operator {
			flattened = append(flattened, child.children...)
			continue
		}
		flattened = append(flattened, child)
	}
	if len(flattened) == 0 {
		truth := operator == FrozenOperatorAnd
		return &frozenCondition{kind: FrozenConditionConstant, truth: truth, operand: flagOperand(truth)}
	}
	if len(flattened) == 1 {
		return flattened[0]
	}
	return &frozenCondition{kind: FrozenConditionLogical, operator: operator, operand: noOperand(), children: append([]*frozenCondition(nil), flattened...)}
}

func normalizeNot(child *frozenCondition) *frozenCondition {
	if child.kind == FrozenConditionConstant {
		return &frozenCondition{kind: FrozenConditionConstant, truth: !child.truth, operand: flagOperand(!child.truth)}
	}
	if child.kind == FrozenConditionLogical && child.operator == FrozenOperatorNot && len(child.children) == 1 {
		return child.children[0]
	}
	return &frozenCondition{kind: FrozenConditionLogical, operator: FrozenOperatorNot, operand: noOperand(), children: []*frozenCondition{child}}
}

func cloneFrozenOperand(operand frozenOperand) frozenOperand {
	result := operand
	result.one = cloneFrozenValue(operand.one)
	result.many = make([]frozenValue, len(operand.many))
	for index, value := range operand.many {
		result.many[index] = cloneFrozenValue(value)
	}
	return result
}

func cloneFrozenValue(value frozenValue) frozenValue {
	value.bytes = append([]byte(nil), value.bytes...)
	return value
}

type frozenPredicateView struct{ predicate FrozenPredicate }

func (frozenPredicateView) sealedFrozenPredicateView() {}
func (view frozenPredicateView) Version() uint16       { return frozenPolicyVersion }
func (view frozenPredicateView) RootModelID() ModelID  { return view.predicate.rootModel }
func (view frozenPredicateView) Root() FrozenConditionView {
	return frozenConditionView{condition: view.predicate.root}
}
func (view frozenPredicateView) CanonicalBytes() []byte {
	return append([]byte(nil), view.predicate.canonical...)
}

type frozenConditionView struct{ condition *frozenCondition }

func (frozenConditionView) sealedFrozenConditionView() {}
func (view frozenConditionView) Kind() FrozenConditionKind {
	if view.condition == nil {
		return 0
	}
	return view.condition.kind
}
func (view frozenConditionView) Operator() FrozenOperator {
	if view.condition == nil {
		return FrozenOperatorNone
	}
	return view.condition.operator
}
func (view frozenConditionView) FieldID() (FieldID, bool) {
	if view.condition == nil || (view.condition.kind != FrozenConditionScalar && view.condition.kind != FrozenConditionList && view.condition.kind != FrozenConditionJSON && view.condition.kind != FrozenConditionRelation) {
		return FieldID{}, false
	}
	return view.condition.field, true
}
func (view frozenConditionView) Relation() (FrozenRelationRef, bool) {
	if view.condition == nil || view.condition.kind != FrozenConditionRelation {
		return FrozenRelationRef{}, false
	}
	return FrozenRelationRef{field: view.condition.field, relation: view.condition.relation}, true
}
func (view frozenConditionView) Mode() FrozenComparisonMode {
	if view.condition != nil {
		return view.condition.mode
	}
	return 0
}
func (frozenConditionView) Path() FrozenJSONPathView { return frozenJSONPathView{} }
func (view frozenConditionView) Operand() FrozenOperandView {
	if view.condition == nil {
		return frozenOperandView{operand: noOperand()}
	}
	return frozenOperandView{operand: cloneFrozenOperand(view.condition.operand)}
}
func (view frozenConditionView) Children() []FrozenConditionView {
	if view.condition == nil {
		return nil
	}
	result := make([]FrozenConditionView, len(view.condition.children))
	for index, child := range view.condition.children {
		result[index] = frozenConditionView{condition: child}
	}
	return result
}

type frozenOperandView struct{ operand frozenOperand }

func (frozenOperandView) sealedFrozenOperandView()     {}
func (view frozenOperandView) Kind() FrozenOperandKind { return view.operand.kind }
func (view frozenOperandView) One() (FrozenValueView, bool) {
	if view.operand.kind != FrozenOperandOne {
		return nil, false
	}
	return frozenValueView{value: cloneFrozenValue(view.operand.one)}, true
}
func (view frozenOperandView) Many() []FrozenValueView {
	if view.operand.kind != FrozenOperandMany {
		return nil
	}
	result := make([]FrozenValueView, len(view.operand.many))
	for index, value := range view.operand.many {
		result[index] = frozenValueView{value: cloneFrozenValue(value)}
	}
	return result
}
func (view frozenOperandView) Flag() (bool, bool) {
	return view.operand.flag, view.operand.kind == FrozenOperandFlag
}

type frozenValueView struct{ value frozenValue }

func (frozenValueView) sealedFrozenValueView()     {}
func (view frozenValueView) Kind() FrozenValueKind { return view.value.kind }
func (view frozenValueView) Bool() (bool, bool) {
	return view.value.boolean, view.value.kind == FrozenValueBool
}
func (view frozenValueView) Signed() (int64, uint8, bool) {
	switch view.value.kind {
	case FrozenValueInt16:
		return view.value.signed, 16, true
	case FrozenValueInt32:
		return view.value.signed, 32, true
	case FrozenValueInt64:
		return view.value.signed, 64, true
	default:
		return 0, 0, false
	}
}
func (view frozenValueView) FloatBits() (uint64, uint8, bool) {
	switch view.value.kind {
	case FrozenValueFloat32:
		return view.value.floatBits, 32, true
	case FrozenValueFloat64:
		return view.value.floatBits, 64, true
	default:
		return 0, 0, false
	}
}
func (view frozenValueView) Decimal() (Decimal, bool) {
	return view.value.decimal, view.value.kind == FrozenValueDecimal
}
func (view frozenValueView) String() (string, bool) {
	return view.value.text, view.value.kind == FrozenValueString
}
func (view frozenValueView) Bytes() ([]byte, bool) {
	if view.value.kind != FrozenValueBytes {
		return nil, false
	}
	return append([]byte(nil), view.value.bytes...), true
}
func (view frozenValueView) UUID() (UUID, bool) {
	return view.value.uuid, view.value.kind == FrozenValueUUID
}
func (view frozenValueView) Date() (Date, bool) {
	return view.value.date, view.value.kind == FrozenValueDate
}
func (view frozenValueView) Time() (Time, bool) {
	return view.value.clock, view.value.kind == FrozenValueTime
}
func (view frozenValueView) DateTime() (int64, uint32, bool) {
	return view.value.seconds, view.value.nanosecond, view.value.kind == FrozenValueDateTime
}

type ruleBuilder struct {
	action    FrozenAction
	effect    FrozenEffect
	condition *predicateNode
	fields    []FieldID
}

type rulesState struct {
	mu    sync.RWMutex
	rules []ruleBuilder
}

func (rules *Rules[M]) appendModelRule(action FrozenAction, effect FrozenEffect, condition Predicate[M]) {
	rules.state.mu.Lock()
	defer rules.state.mu.Unlock()
	rules.state.rules = append(rules.state.rules, ruleBuilder{action: action, effect: effect, condition: condition.node})
}

func (rules *Rules[M]) appendFieldRule(action FrozenAction, effect FrozenEffect, condition Predicate[M], first Field[M], rest []Field[M]) {
	fields := make([]FieldID, 0, len(rest)+1)
	fields = append(fields, fieldIdentity(first))
	for _, field := range rest {
		fields = append(fields, fieldIdentity(field))
	}
	rules.state.mu.Lock()
	defer rules.state.mu.Unlock()
	rules.state.rules = append(rules.state.rules, ruleBuilder{action: action, effect: effect, condition: condition.node, fields: fields})
}

func fieldIdentity[M any](field Field[M]) FieldID {
	if field == nil {
		return FieldID{}
	}
	return field.fieldIdentity()
}

type frozenRule struct {
	action        FrozenAction
	effect        FrozenEffect
	model         ModelID
	condition     *frozenCondition
	fields        []FieldID
	position      uint32
	unconditional bool
}

type FrozenPolicy struct {
	model     ModelID
	rules     []frozenRule
	canonical []byte
}

func (rules *Rules[M]) Freeze(model ModelID) (FrozenPolicy, error) {
	if rules == nil {
		return FrozenPolicy{}, freezeFailure(FreezeInvalidRule, "nil rules builder")
	}
	if model == (ModelID{}) {
		return FrozenPolicy{}, freezeFailure(FreezeInvalidModel, "policy model has a zero identity")
	}
	rules.state.mu.RLock()
	snapshot := make([]ruleBuilder, len(rules.state.rules))
	for index, rule := range rules.state.rules {
		snapshot[index] = rule
		snapshot[index].fields = append([]FieldID(nil), rule.fields...)
	}
	rules.state.mu.RUnlock()

	frozen := make([]frozenRule, len(snapshot))
	for index, rule := range snapshot {
		condition, err := freezePredicateNode(rule.condition, make(map[*predicateNode]bool), 0)
		if err != nil {
			var failure *FreezeError
			if errors.As(err, &failure) {
				copy := *failure
				copy.RulePosition = uint32(index)
				copy.HasRule = true
				return FrozenPolicy{}, &copy
			}
			return FrozenPolicy{}, err
		}
		fields, err := freezeRuleFields(rule.fields)
		if err != nil {
			failure := err.(*FreezeError)
			failure.RulePosition = uint32(index)
			failure.HasRule = true
			return FrozenPolicy{}, failure
		}
		if !validAction(rule.action) || !validEffect(rule.effect) || (rule.action == FrozenActionDelete && fields != nil) {
			return FrozenPolicy{}, &FreezeError{Code: FreezeInvalidRule, RulePosition: uint32(index), HasRule: true, Detail: "invalid action, effect, or delete field rule"}
		}
		unconditional := condition.kind == FrozenConditionConstant && condition.truth
		if unconditional {
			condition = nil
		}
		frozen[index] = frozenRule{action: rule.action, effect: rule.effect, model: model, condition: condition, fields: fields, position: uint32(index), unconditional: unconditional}
	}
	canonical, err := encodeFrozenPolicy(model, frozen)
	if err != nil {
		return FrozenPolicy{}, freezeFailure(FreezeInvalidRule, err.Error())
	}
	return FrozenPolicy{model: model, rules: frozen, canonical: canonical}, nil
}

func freezeRuleFields(fields []FieldID) ([]FieldID, error) {
	if fields == nil {
		return nil, nil
	}
	seen := make(map[FieldID]struct{}, len(fields))
	result := make([]FieldID, 0, len(fields))
	for _, field := range fields {
		if field == (FieldID{}) {
			return nil, freezeFailure(FreezeInvalidField, "field rule contains a zero field identity")
		}
		if _, duplicate := seen[field]; duplicate {
			continue
		}
		seen[field] = struct{}{}
		result = append(result, field)
	}
	if len(result) == 0 {
		return nil, freezeFailure(FreezeInvalidRule, "field rule contains no fields")
	}
	return result, nil
}

func validAction(action FrozenAction) bool {
	return action >= FrozenActionRead && action <= FrozenActionDelete
}
func validEffect(effect FrozenEffect) bool {
	return effect == FrozenEffectGrant || effect == FrozenEffectDeny
}

func (policy FrozenPolicy) View() FrozenPolicyView { return frozenPolicyView{policy: policy} }
func (policy FrozenPolicy) Version() uint16        { return frozenPolicyVersion }
func (policy FrozenPolicy) CanonicalBytes() []byte {
	return append([]byte(nil), policy.canonical...)
}

type frozenPolicyView struct{ policy FrozenPolicy }

func (frozenPolicyView) sealedFrozenPolicyView() {}
func (frozenPolicyView) Version() uint16         { return frozenPolicyVersion }
func (view frozenPolicyView) ModelID() ModelID   { return view.policy.model }
func (view frozenPolicyView) Rules() []FrozenRuleView {
	result := make([]FrozenRuleView, len(view.policy.rules))
	for index, rule := range view.policy.rules {
		result[index] = frozenRuleView{rule: cloneFrozenRule(rule)}
	}
	return result
}
func (view frozenPolicyView) CanonicalBytes() []byte {
	return append([]byte(nil), view.policy.canonical...)
}

type frozenRuleView struct{ rule frozenRule }

func (frozenRuleView) sealedFrozenRuleView()      {}
func (view frozenRuleView) Action() FrozenAction  { return view.rule.action }
func (view frozenRuleView) Effect() FrozenEffect  { return view.rule.effect }
func (view frozenRuleView) ModelID() ModelID      { return view.rule.model }
func (view frozenRuleView) IsUnconditional() bool { return view.rule.unconditional }
func (view frozenRuleView) Condition() (FrozenPredicateView, bool) {
	if view.rule.condition == nil {
		return nil, false
	}
	canonical, _ := encodeFrozenPredicate(view.rule.model, view.rule.condition)
	return frozenPredicateView{predicate: FrozenPredicate{rootModel: view.rule.model, root: view.rule.condition, canonical: canonical}}, true
}
func (view frozenRuleView) Fields() ([]FieldID, bool) {
	if view.rule.fields == nil {
		return nil, true
	}
	return append([]FieldID(nil), view.rule.fields...), false
}
func (view frozenRuleView) Position() uint32 { return view.rule.position }

func cloneFrozenRule(rule frozenRule) frozenRule {
	rule.fields = append([]FieldID(nil), rule.fields...)
	rule.condition = cloneFrozenCondition(rule.condition)
	return rule
}

func cloneFrozenCondition(condition *frozenCondition) *frozenCondition {
	if condition == nil {
		return nil
	}
	result := *condition
	result.operand = cloneFrozenOperand(condition.operand)
	result.children = make([]*frozenCondition, len(condition.children))
	for index, child := range condition.children {
		result.children[index] = cloneFrozenCondition(child)
	}
	return &result
}

func encodeFrozenPredicate(model ModelID, condition *frozenCondition) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString("golem:public-policy-condition:v1\x00")
	writeUint16(&output, frozenPolicyVersion)
	output.Write(model[:])
	if err := encodeFrozenCondition(&output, condition); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func encodeFrozenPolicy(model ModelID, rules []frozenRule) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString("golem:public-policy:v1\x00")
	writeUint16(&output, frozenPolicyVersion)
	output.Write(model[:])
	writeUint32(&output, uint32(len(rules)))
	for index, rule := range rules {
		if rule.position != uint32(index) {
			return nil, errors.New("rule positions are not contiguous")
		}
		output.WriteByte(byte(rule.action))
		output.WriteByte(byte(rule.effect))
		writeUint32(&output, rule.position)
		if rule.condition == nil {
			output.WriteByte(0)
		} else {
			output.WriteByte(1)
			if err := encodeFrozenCondition(&output, rule.condition); err != nil {
				return nil, err
			}
		}
		if rule.fields == nil {
			output.WriteByte(0)
		} else {
			output.WriteByte(1)
			writeUint32(&output, uint32(len(rule.fields)))
			for _, field := range rule.fields {
				output.Write(field[:])
			}
		}
	}
	return output.Bytes(), nil
}

func encodeFrozenCondition(output *bytes.Buffer, condition *frozenCondition) error {
	if condition == nil {
		return errors.New("nil condition")
	}
	output.WriteByte(byte(condition.kind))
	writeUint16(output, uint16(condition.operator))
	switch condition.kind {
	case FrozenConditionConstant:
		writeBool(output, condition.truth)
	case FrozenConditionLogical:
		writeUint32(output, uint32(len(condition.children)))
		for _, child := range condition.children {
			if err := encodeFrozenCondition(output, child); err != nil {
				return err
			}
		}
	case FrozenConditionScalar, FrozenConditionList, FrozenConditionJSON:
		output.Write(condition.field[:])
		output.WriteByte(byte(condition.mode))
		if err := encodeFrozenOperand(output, condition.operand); err != nil {
			return err
		}
	case FrozenConditionRelation:
		output.Write(condition.field[:])
		output.Write(condition.relation[:])
		writeUint32(output, uint32(len(condition.children)))
		for _, child := range condition.children {
			if err := encodeFrozenCondition(output, child); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unknown condition kind %d", condition.kind)
	}
	return nil
}

func encodeFrozenOperand(output *bytes.Buffer, operand frozenOperand) error {
	output.WriteByte(byte(operand.kind))
	switch operand.kind {
	case FrozenOperandNone:
		return nil
	case FrozenOperandOne:
		return encodeFrozenValue(output, operand.one)
	case FrozenOperandMany:
		writeUint32(output, uint32(len(operand.many)))
		for _, value := range operand.many {
			if err := encodeFrozenValue(output, value); err != nil {
				return err
			}
		}
		return nil
	case FrozenOperandFlag:
		writeBool(output, operand.flag)
		return nil
	default:
		return fmt.Errorf("unknown operand kind %d", operand.kind)
	}
}

func encodeFrozenValue(output *bytes.Buffer, value frozenValue) error {
	output.WriteByte(byte(value.kind))
	switch value.kind {
	case FrozenValueBool:
		writeBool(output, value.boolean)
	case FrozenValueInt16:
		writeUint16(output, uint16(int16(value.signed)))
	case FrozenValueInt32:
		writeUint32(output, uint32(int32(value.signed)))
	case FrozenValueInt64:
		writeUint64(output, uint64(value.signed))
	case FrozenValueFloat32:
		writeUint32(output, uint32(value.floatBits))
	case FrozenValueFloat64:
		writeUint64(output, value.floatBits)
	case FrozenValueDecimal:
		writeUint64(output, uint64(value.decimal.coefficient))
		output.WriteByte(value.decimal.scale)
	case FrozenValueString:
		writeBytes(output, []byte(value.text))
	case FrozenValueBytes:
		writeBytes(output, value.bytes)
	case FrozenValueUUID:
		output.Write(value.uuid[:])
	case FrozenValueDate:
		writeUint16(output, uint16(value.date.year))
		output.WriteByte(value.date.month)
		output.WriteByte(value.date.day)
	case FrozenValueTime:
		writeUint64(output, uint64(value.clock.microseconds))
	case FrozenValueDateTime:
		writeUint64(output, uint64(value.seconds))
		writeUint32(output, value.nanosecond)
	default:
		return fmt.Errorf("unknown value kind %d", value.kind)
	}
	return nil
}

func writeBool(output *bytes.Buffer, value bool) {
	if value {
		output.WriteByte(1)
	} else {
		output.WriteByte(0)
	}
}

func writeUint16(output *bytes.Buffer, value uint16) {
	var encoded [2]byte
	binary.BigEndian.PutUint16(encoded[:], value)
	output.Write(encoded[:])
}

func writeUint32(output *bytes.Buffer, value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	output.Write(encoded[:])
}

func writeUint64(output *bytes.Buffer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	output.Write(encoded[:])
}

func writeBytes(output *bytes.Buffer, value []byte) {
	writeUint32(output, uint32(len(value)))
	output.Write(value)
}
