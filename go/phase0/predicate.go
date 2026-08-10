package phase0

// Operator identifies one node in the provider-neutral policy expression tree.
// Phase 0 intentionally exposes only the vocabulary it proves.
type Operator string

const (
	OpAll           Operator = "all"
	OpNone          Operator = "none"
	OpEqual         Operator = "equal"
	OpNotEqual      Operator = "notEqual"
	OpIn            Operator = "in"
	OpAnd           Operator = "and"
	OpOr            Operator = "or"
	OpNot           Operator = "not"
	OpRelationIs    Operator = "relationIs"
	OpRelationIsNot Operator = "relationIsNot"
	OpRelationSome  Operator = "relationSome"
	OpRelationEvery Operator = "relationEvery"
	OpRelationNone  Operator = "relationNone"
)

// ScalarKind records the semantic type of a generated field. It is metadata
// for validation and future SQL compilation; Go's type parameter enforces the
// operand type while a policy is authored.
type ScalarKind string

const (
	ScalarString  ScalarKind = "string"
	ScalarBoolean ScalarKind = "boolean"
	ScalarInteger ScalarKind = "integer"
	ScalarTime    ScalarKind = "time"
)

// FieldDescriptor is the provider-neutral identity of a generated model field.
type FieldDescriptor struct {
	Model string     `json:"model"`
	Name  string     `json:"name"`
	Kind  ScalarKind `json:"kind"`
}

// ModelField lets rule methods accept generated fields of different scalar
// types while retaining their common model type.
type ModelField[M any] interface {
	Descriptor() FieldDescriptor
	modelType() *M
}

// Field is the generated-style, typed handle used to build scalar predicates.
type Field[M any, V comparable] struct {
	descriptor FieldDescriptor
}

func NewField[M any, V comparable](model, name string, kind ScalarKind) Field[M, V] {
	return Field[M, V]{descriptor: FieldDescriptor{Model: model, Name: name, Kind: kind}}
}

func (f Field[M, V]) Descriptor() FieldDescriptor {
	return f.descriptor
}

// modelType carries the otherwise-phantom model parameter into ModelField's
// method set. Without it, Go's structural interfaces would allow a User field
// to satisfy ModelField[Post] because Descriptor alone has the same signature.
func (f Field[M, V]) modelType() *M {
	return nil
}

func (f Field[M, V]) Eq(value V) Predicate[M] {
	return Predicate[M]{node: node{Operator: OpEqual, Field: &f.descriptor, Value: value}}
}

func (f Field[M, V]) Ne(value V) Predicate[M] {
	return Predicate[M]{node: node{Operator: OpNotEqual, Field: &f.descriptor, Value: value}}
}

func (f Field[M, V]) In(values ...V) Predicate[M] {
	operands := make([]any, len(values))
	for index, value := range values {
		operands[index] = value
	}
	return Predicate[M]{node: node{Operator: OpIn, Field: &f.descriptor, Value: operands}}
}

type RelationCardinality string

const (
	ToOne  RelationCardinality = "one"
	ToMany RelationCardinality = "many"
)

type RelationDescriptor struct {
	From        string              `json:"from"`
	Name        string              `json:"name"`
	To          string              `json:"to"`
	Cardinality RelationCardinality `json:"cardinality"`
}

type ToOneRelation[P any, C any] struct {
	descriptor RelationDescriptor
}

func NewToOneRelation[P any, C any](from, name, to string) ToOneRelation[P, C] {
	return ToOneRelation[P, C]{descriptor: RelationDescriptor{
		From: from, Name: name, To: to, Cardinality: ToOne,
	}}
}

func (r ToOneRelation[P, C]) Is(predicate Predicate[C]) Predicate[P] {
	return Predicate[P]{node: node{Operator: OpRelationIs, Relation: &r.descriptor, Children: []node{predicate.node}}}
}

func (r ToOneRelation[P, C]) IsNot(predicate Predicate[C]) Predicate[P] {
	return Predicate[P]{node: node{Operator: OpRelationIsNot, Relation: &r.descriptor, Children: []node{predicate.node}}}
}

type ToManyRelation[P any, C any] struct {
	descriptor RelationDescriptor
}

func NewToManyRelation[P any, C any](from, name, to string) ToManyRelation[P, C] {
	return ToManyRelation[P, C]{descriptor: RelationDescriptor{
		From: from, Name: name, To: to, Cardinality: ToMany,
	}}
}

func (r ToManyRelation[P, C]) Some(predicate Predicate[C]) Predicate[P] {
	return Predicate[P]{node: node{Operator: OpRelationSome, Relation: &r.descriptor, Children: []node{predicate.node}}}
}

func (r ToManyRelation[P, C]) Every(predicate Predicate[C]) Predicate[P] {
	return Predicate[P]{node: node{Operator: OpRelationEvery, Relation: &r.descriptor, Children: []node{predicate.node}}}
}

func (r ToManyRelation[P, C]) None(predicate Predicate[C]) Predicate[P] {
	return Predicate[P]{node: node{Operator: OpRelationNone, Relation: &r.descriptor, Children: []node{predicate.node}}}
}

// Predicate is typed by its root model. Predicates for different models
// cannot be combined except through a generated relation descriptor.
type Predicate[M any] struct {
	node node
}

type node struct {
	Operator Operator            `json:"operator"`
	Field    *FieldDescriptor    `json:"field,omitempty"`
	Relation *RelationDescriptor `json:"relation,omitempty"`
	Value    any                 `json:"value,omitempty"`
	Children []node              `json:"children,omitempty"`
}

func All[M any]() Predicate[M] {
	return Predicate[M]{node: node{Operator: OpAll}}
}

func None[M any]() Predicate[M] {
	return Predicate[M]{node: node{Operator: OpNone}}
}

func And[M any](predicates ...Predicate[M]) Predicate[M] {
	children := make([]node, len(predicates))
	for index, predicate := range predicates {
		children[index] = predicate.node
	}
	return Predicate[M]{node: node{Operator: OpAnd, Children: children}}
}

func Or[M any](predicates ...Predicate[M]) Predicate[M] {
	children := make([]node, len(predicates))
	for index, predicate := range predicates {
		children[index] = predicate.node
	}
	return Predicate[M]{node: node{Operator: OpOr, Children: children}}
}

func Not[M any](predicate Predicate[M]) Predicate[M] {
	return Predicate[M]{node: node{Operator: OpNot, Children: []node{predicate.node}}}
}

func (p Predicate[M]) And(others ...Predicate[M]) Predicate[M] {
	return And(append([]Predicate[M]{p}, others...)...)
}

func (p Predicate[M]) Or(others ...Predicate[M]) Predicate[M] {
	return Or(append([]Predicate[M]{p}, others...)...)
}

func (p Predicate[M]) Not() Predicate[M] {
	return Not(p)
}
