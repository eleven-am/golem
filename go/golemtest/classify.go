package golemtest

import (
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/classify"
	"github.com/eleven-am/golem/go/internal/policy/dependency"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
)

// UseKind names the position a statement puts a field in. The runtime records
// it so a later diagnostic can say where a refusal happened, and so that
// position-specific rules can be added without falling back to strings. Every
// position is judged through the same read lens today.
type UseKind uint8

const (
	// UseProjection is a field returned in the result of a read or mutation.
	UseProjection UseKind = iota + 1
	// UseFilter is a field named inside a caller's where predicate.
	UseFilter
	// UseSelector is a field of the unique key that locates a single row.
	UseSelector
	// UseOrder is a field a statement sorts by.
	UseOrder
	// UseCursor is a field of the selector that anchors cursor pagination.
	UseCursor
	// UseDistinct is a field a statement de-duplicates by.
	UseDistinct
	// UseAggregateMeasure is a field an aggregate measure is computed over.
	UseAggregateMeasure
	// UseGroupDimension is a field a grouped read groups by.
	UseGroupDimension
	// UseHaving is a field named inside a grouped read's having predicate.
	UseHaving
	// UseAggregateOrder is a field a grouped or aggregated read sorts by.
	UseAggregateOrder
	// UseComputedDependency is a field a computed field is derived from.
	UseComputedDependency
)

// Access is the exhaustive static readability of one field for one statement
// reach. It answers what the runtime will do with the field, not whether the
// field can be written.
type Access uint8

const (
	// AccessAlways means every row this statement can reach may expose the
	// field. The runtime applies no mask to it.
	AccessAlways Access = iota + 1
	// AccessConditional means the field is readable only on rows satisfying
	// [FieldClassification.Condition]. The runtime masks it per row unless the
	// statement reach already proves the condition.
	AccessConditional
	// AccessNever means no row exposes the field. A caller filter cannot make
	// it readable, and a statement that requires it is refused.
	AccessNever
)

// DependencyKind distinguishes a dependency that is a persisted value on the
// classified model from one that is a relation whose target must be hydrated.
type DependencyKind uint8

const (
	// DependencyScalar is a persisted value on the model being classified.
	DependencyScalar DependencyKind = iota + 1
	// DependencyRelation is a relation whose target rows must be hydrated
	// before the condition can be decided.
	DependencyRelation
)

// DependencyTree is the immutable hydration plan a conditional field's
// condition requires, rooted at exactly one model. It is the record graph the
// runtime privately fetches to decide whether the field may be exposed on a
// row; it is not part of the caller's result.
//
// Entry order is the first-seen order of the conditions the tree was collected
// from. Every accessor returns a copy.
type DependencyTree struct {
	model   golem.ModelID
	entries []DependencyEntry
}

// DependencyEntry is one immutable dependency of a conditional field. A
// relation entry retains its target model even when its child tree is empty,
// because relation presence and empty quantifiers still require the relation
// itself to be known.
type DependencyEntry struct {
	field    golem.FieldID
	kind     DependencyKind
	target   golem.ModelID
	children DependencyTree
}

// ModelID is the model this tree is rooted at. It is the zero model identity
// for the empty tree of an unconditional field.
func (tree DependencyTree) ModelID() golem.ModelID { return tree.model }

// Entries returns a copy of this tree's dependencies in first-seen order.
func (tree DependencyTree) Entries() []DependencyEntry {
	result := make([]DependencyEntry, len(tree.entries))
	for index := range tree.entries {
		result[index] = tree.entries[index].clone()
	}
	return result
}

// FieldID is the identity of the depended-upon field on the tree's root model.
func (entry DependencyEntry) FieldID() golem.FieldID { return entry.field }

// Kind reports whether this entry is a persisted value or a relation hop.
func (entry DependencyEntry) Kind() DependencyKind { return entry.kind }

// TargetModelID is the model a relation entry points at. The second result is
// false for a scalar entry, whose target is the zero model identity.
func (entry DependencyEntry) TargetModelID() (golem.ModelID, bool) {
	return entry.target, entry.kind == DependencyRelation
}

// Children returns a copy of the subtree that must be hydrated on the target
// of a relation entry. It is empty for a scalar entry, and it may also be empty
// for a relation entry whose condition only tests the relation's presence.
func (entry DependencyEntry) Children() DependencyTree { return entry.children.clone() }

// FieldClassification is the immutable readability of one field, produced by
// the production field classifier for one statement reach.
type FieldClassification[M any] struct {
	field        golem.FieldID
	access       Access
	condition    Constraint[M]
	requires     []golem.FieldID
	dependencies DependencyTree
	discharged   bool
}

// FieldID is the stable identity of the classified field.
func (value FieldClassification[M]) FieldID() golem.FieldID { return value.field }

// Access is this field's readability for the reach the plan was built for.
func (value FieldClassification[M]) Access() Access { return value.access }

// Condition is the rows on which this field may be exposed. The second result
// is true only for [AccessConditional]: an always-readable field has no
// condition to state and a never-readable field has no condition that could
// make it readable.
//
// The returned constraint carries the same model descriptor as the policy it
// came from, so [Equivalent] and [Implies] can prove statements about it.
func (value FieldClassification[M]) Condition() (Constraint[M], bool) {
	return value.condition, value.access == AccessConditional
}

// RequiredFields returns a copy of the fields on this model that the runtime
// must load to decide the condition. It stops at a relation hop; the relation's
// own subtree is described by [FieldClassification.Dependencies].
func (value FieldClassification[M]) RequiredFields() []golem.FieldID {
	return append([]golem.FieldID(nil), value.requires...)
}

// Dependencies returns a copy of the full hydration plan the condition
// requires, including relation subtrees.
func (value FieldClassification[M]) Dependencies() DependencyTree {
	return value.dependencies.clone()
}

// DischargedByConstraint reports whether the statement reach already proves the
// condition, so the runtime can expose the field without masking it per row.
//
// It is meaningful only for [AccessConditional] and is false otherwise: an
// always-readable field needs no proof, and a never-readable field cannot be
// made readable by a caller's filter.
func (value FieldClassification[M]) DischargedByConstraint() bool { return value.discharged }

// ReadPlan is the immutable classification of every requested field for one
// model, one use position, and one statement reach. It preserves the caller's
// first-seen field order and drops duplicates after their first occurrence.
type ReadPlan[M any] struct {
	model           golem.ModelID
	use             UseKind
	selectingAction golem.FrozenAction
	fields          []FieldClassification[M]
}

// ModelID is the model whose fields this plan classifies.
func (plan ReadPlan[M]) ModelID() golem.ModelID { return plan.model }

// UseKind is the statement position the fields were classified for.
func (plan ReadPlan[M]) UseKind() UseKind { return plan.use }

// SelectingAction is the action whose row constraint defined the statement
// reach. Fields are always judged through the read policy regardless of it.
func (plan ReadPlan[M]) SelectingAction() golem.FrozenAction { return plan.selectingAction }

// Fields returns a copy of every classification in the caller's field order.
func (plan ReadPlan[M]) Fields() []FieldClassification[M] {
	result := make([]FieldClassification[M], len(plan.fields))
	for index := range plan.fields {
		result[index] = plan.fields[index].clone()
	}
	return result
}

// Field returns the classification of one requested field. The second result is
// false when the handle is not a generated field of this model or was not among
// the fields the plan was built for.
func (plan ReadPlan[M]) Field(field golem.Field[M]) (FieldClassification[M], bool) {
	identity, valid := golem.FieldIdentity(field)
	if !valid {
		return FieldClassification[M]{}, false
	}
	for _, classification := range plan.fields {
		if classification.field == identity {
			return classification.clone(), true
		}
	}
	return FieldClassification[M]{}, false
}

// ClassifyReadFields classifies how the requested fields will be returned to
// this actor when a statement reaches exactly the rows selectingAction grants.
//
// Classification is a read question. selectingAction only says which action's
// resolved row constraint defines the statement's reach — the rows a read, an
// update, or a delete is allowed to touch — while the fields themselves are
// always judged through the read policy, exactly as the runtime does when it
// returns rows from a read or from a mutation.
//
// The caller's field order is preserved and duplicates after the first
// occurrence are dropped. At least one field is required, every field must be a
// generated handle of this model, and a field the model does not declare is
// rejected rather than silently ignored.
func (policy ModelPolicy[M]) ClassifyReadFields(use UseKind, selectingAction golem.FrozenAction, fields ...golem.Field[M]) (ReadPlan[M], error) {
	return policy.classifyFields(use, selectingAction, nil, fields)
}

// ClassifyReadFieldsWithReach classifies the requested fields against a
// statement whose reach is narrowed by an additional caller predicate, the way
// production combines a caller's filter with the actor's row policy.
//
// The supplied reach must itself be within the actor's row constraint for
// selectingAction. A reach that would widen that constraint is refused with an
// [ErrorPolicyAnalysis] error rather than accepted, so a filter can never buy
// access the policy does not grant. A reach that genuinely narrows the
// statement may discharge a conditional field: the runtime can then return the
// field unmasked because every row the statement can reach already satisfies
// the field's condition.
//
// The reach predicate is frozen against the model descriptor this policy
// retains, so it cannot be judged against a different model.
func (policy ModelPolicy[M]) ClassifyReadFieldsWithReach(use UseKind, selectingAction golem.FrozenAction, reach golem.Predicate[M], fields ...golem.Field[M]) (ReadPlan[M], error) {
	return policy.classifyFields(use, selectingAction, &reach, fields)
}

func (policy ModelPolicy[M]) classifyFields(use UseKind, selectingAction golem.FrozenAction, reach *golem.Predicate[M], fields []golem.Field[M]) (ReadPlan[M], error) {
	if policy.registry == nil || policy.model == (golem.ModelID{}) || policy.generation == (golem.SchemaDigest{}) {
		return ReadPlan[M]{}, fail(ErrorInvalidInput, "model policy was not produced by Model")
	}
	boundUseKind, knownUse := boundUse(use)
	if !knownUse {
		return ReadPlan[M]{}, fail(ErrorInvalidInput, "the requested use is not one of the declared statement positions")
	}
	action, knownAction := boundAction(selectingAction)
	if !knownAction {
		return ReadPlan[M]{}, fail(ErrorInvalidInput, "the requested action is not one of the four declared actions")
	}
	if len(fields) == 0 {
		return ReadPlan[M]{}, failModel(ErrorInvalidInput, "read-field classification requires at least one field", policy.model)
	}

	requested := make([]ir.FieldID, 0, len(fields))
	seen := make(map[golem.FieldID]struct{}, len(fields))
	for _, field := range fields {
		identity, valid := golem.FieldIdentity(field)
		if !valid {
			return ReadPlan[M]{}, failModel(ErrorInvalidInput, "a requested field is not a usable generated field handle", policy.model)
		}
		if !policy.registry.HasField(policy.model, identity) {
			return ReadPlan[M]{}, failField(ErrorGenerationMismatch, "a requested field is not declared by this model in this generation", policy.model, identity)
		}
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		seen[identity] = struct{}{}
		requested = append(requested, ir.FieldID(identity))
	}

	request, err := policy.classifyRequest(boundUseKind, action, reach, requested)
	if err != nil {
		return ReadPlan[M]{}, err
	}
	classified, err := classify.Fields(request)
	if err != nil {
		return ReadPlan[M]{}, failModel(ErrorPolicyAnalysis, "the policy field classifier could not classify these fields", policy.model)
	}
	if classified.ModelID() != ir.ModelID(policy.model) {
		return ReadPlan[M]{}, failModel(ErrorPolicyAnalysis, "the policy field classifier answered for a different model", policy.model)
	}

	classifications := classified.Classifications()
	adapted := make([]FieldClassification[M], 0, len(classifications))
	for _, classification := range classifications {
		value, adaptErr := policy.adaptClassification(classification)
		if adaptErr != nil {
			return ReadPlan[M]{}, adaptErr
		}
		adapted = append(adapted, value)
	}
	return ReadPlan[M]{model: policy.model, use: use, selectingAction: selectingAction, fields: adapted}, nil
}

func (policy ModelPolicy[M]) classifyRequest(use classify.UseKind, action ir.Action, reach *golem.Predicate[M], fields []ir.FieldID) (classify.Request, error) {
	if reach == nil {
		request, err := classify.NewRequest(policy.registry, policy.bound, ir.ModelID(policy.model), fields, use, action)
		if err != nil {
			return classify.Request{}, failModel(ErrorPolicyAnalysis, "the policy field classifier refused this classification request", policy.model)
		}
		return request, nil
	}

	frozen, err := reach.Freeze(policy.descriptor)
	if err != nil {
		return classify.Request{}, failModel(ErrorInvalidInput, "the reach predicate cannot be frozen against the model descriptor this policy retains", policy.model)
	}
	condition, err := bind.Predicate(frozen, policy.registry, policy.providers)
	if err != nil {
		return classify.Request{}, failModel(ErrorPolicyAnalysis, "the reach predicate does not bind against the generated schema", policy.model)
	}
	if condition.ModelID() != ir.ModelID(policy.model) {
		return classify.Request{}, failModel(ErrorPolicyAnalysis, "the reach predicate resolved to a different model than this policy", policy.model)
	}
	narrowed, err := normalize.Condition(condition)
	if err != nil {
		return classify.Request{}, failModel(ErrorPolicyAnalysis, "the reach predicate could not be normalized", policy.model)
	}
	request, err := classify.NewRequestWithConstraint(policy.registry, policy.bound, ir.ModelID(policy.model), fields, use, action, narrowed)
	if err != nil {
		return classify.Request{}, failModel(ErrorPolicyAnalysis, "the supplied reach does not preserve the row constraint this actor holds for the selecting action", policy.model)
	}
	return request, nil
}

func (policy ModelPolicy[M]) adaptClassification(classification classify.Classification) (FieldClassification[M], error) {
	field := golem.FieldID(classification.FieldID())
	access, known := publicAccess(classification.Access())
	if !known {
		return FieldClassification[M]{}, failField(ErrorPolicyAnalysis, "the policy field classifier reported a readability this kit does not recognise", policy.model, field)
	}
	dependencies, err := policy.adaptDependencies(classification.Dependencies())
	if err != nil {
		return FieldClassification[M]{}, err
	}
	inner := classification.Requires()
	requires := make([]golem.FieldID, 0, len(inner))
	for _, required := range inner {
		requires = append(requires, golem.FieldID(required))
	}

	result := FieldClassification[M]{field: field, access: access, requires: requires, dependencies: dependencies}
	condition, conditional := classification.FieldCondition()
	if conditional {
		result.condition = Constraint[M]{
			model:      policy.model,
			descriptor: policy.descriptor,
			registry:   policy.registry,
			providers:  policy.providers,
			condition:  condition,
			resolved:   true,
		}
		result.discharged = classification.DischargedByConstraint()
	}
	return result, nil
}

func (policy ModelPolicy[M]) adaptDependencies(tree dependency.Tree) (DependencyTree, error) {
	result := DependencyTree{model: golem.ModelID(tree.ModelID())}
	entries := tree.Entries()
	result.entries = make([]DependencyEntry, 0, len(entries))
	for _, entry := range entries {
		kind, known := publicDependencyKind(entry.Kind())
		if !known {
			return DependencyTree{}, failField(ErrorPolicyAnalysis, "the policy dependency collector reported a dependency kind this kit does not recognise", policy.model, golem.FieldID(entry.FieldID()))
		}
		children, err := policy.adaptDependencies(entry.Children())
		if err != nil {
			return DependencyTree{}, err
		}
		target, relation := entry.TargetModel()
		if relation != (kind == DependencyRelation) {
			return DependencyTree{}, failField(ErrorPolicyAnalysis, "a relation dependency does not carry the target model it must hydrate", policy.model, golem.FieldID(entry.FieldID()))
		}
		result.entries = append(result.entries, DependencyEntry{
			field:    golem.FieldID(entry.FieldID()),
			kind:     kind,
			target:   golem.ModelID(target),
			children: children,
		})
	}
	return result, nil
}

func boundUse(use UseKind) (classify.UseKind, bool) {
	switch use {
	case UseProjection:
		return classify.UseProjection, true
	case UseFilter:
		return classify.UseFilter, true
	case UseSelector:
		return classify.UseSelector, true
	case UseOrder:
		return classify.UseOrder, true
	case UseCursor:
		return classify.UseCursor, true
	case UseDistinct:
		return classify.UseDistinct, true
	case UseAggregateMeasure:
		return classify.UseAggregateMeasure, true
	case UseGroupDimension:
		return classify.UseGroupDimension, true
	case UseHaving:
		return classify.UseHaving, true
	case UseAggregateOrder:
		return classify.UseAggregateOrder, true
	case UseComputedDependency:
		return classify.UseComputedDependency, true
	default:
		return 0, false
	}
}

func publicAccess(access classify.Access) (Access, bool) {
	switch access {
	case classify.AccessAlways:
		return AccessAlways, true
	case classify.AccessConditional:
		return AccessConditional, true
	case classify.AccessNever:
		return AccessNever, true
	default:
		return 0, false
	}
}

func publicDependencyKind(kind dependency.Kind) (DependencyKind, bool) {
	switch kind {
	case dependency.Scalar:
		return DependencyScalar, true
	case dependency.Relation:
		return DependencyRelation, true
	default:
		return 0, false
	}
}

func (tree DependencyTree) clone() DependencyTree {
	return DependencyTree{model: tree.model, entries: tree.Entries()}
}

func (entry DependencyEntry) clone() DependencyEntry {
	entry.children = entry.children.clone()
	return entry
}

func (value FieldClassification[M]) clone() FieldClassification[M] {
	value.requires = value.RequiredFields()
	value.dependencies = value.dependencies.clone()
	return value
}
