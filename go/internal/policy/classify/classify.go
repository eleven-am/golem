// Package classify turns frozen read policy into deterministic field-access
// facts for one concrete statement reach.
package classify

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/dependency"
	"github.com/eleven-am/golem/go/internal/policy/imply"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
)

// UseKind records why an operation planner named a field. P2 classifies every
// kind with the same read lens; the distinction is retained so later phases can
// make position-specific permit, mask, and diagnostic decisions without
// falling back to strings.
type UseKind uint8

const (
	UseProjection         UseKind = 1
	UseFilter             UseKind = 2
	UseSelector           UseKind = 3
	UseOrder              UseKind = 4
	UseCursor             UseKind = 5
	UseDistinct           UseKind = 6
	UseAggregateMeasure   UseKind = 7
	UseGroupDimension     UseKind = 8
	UseHaving             UseKind = 9
	UseAggregateOrder     UseKind = 10
	UseComputedDependency UseKind = 11
)

// Access is the exhaustive static readability state of one field.
type Access uint8

const (
	AccessAlways      Access = 1
	AccessConditional Access = 2
	AccessNever       Access = 3
)

// IdentityRegistry is the narrow fingerprinted-schema capability needed at
// the P2/P3 boundary. *schema.Registry is the production implementation.
type IdentityRegistry interface {
	HasModel(golem.ModelID) bool
	HasField(golem.ModelID, golem.FieldID) bool
}

// Request is opaque so callers cannot replace validated identities or pair a
// selecting constraint from one policy with a read constraint from another.
type Request struct {
	policy              ir.Policy
	model               ir.ModelID
	fields              []ir.FieldID
	use                 UseKind
	selectingAction     ir.Action
	selectingConstraint ir.Condition
	readConstraint      ir.Condition
}

// NewRequest validates all identities against the exact runtime schema and
// derives both constraints from one immutable policy.
func NewRequest(registry IdentityRegistry, policy ir.Policy, model ir.ModelID, fields []ir.FieldID, use UseKind, selectingAction ir.Action) (Request, error) {
	return newRequest(registry, policy, model, fields, use, selectingAction, nil)
}

// NewRequestWithConstraint is the P3 boundary for classifying fields against
// the actual statement reach (caller predicate AND row policy). The supplied
// constraint must itself imply the policy row constraint, preventing a caller
// from widening policy reach while still allowing its narrower filter to
// discharge a conditional field lens.
func NewRequestWithConstraint(registry IdentityRegistry, policy ir.Policy, model ir.ModelID, fields []ir.FieldID, use UseKind, selectingAction ir.Action, selecting ir.Condition) (Request, error) {
	return newRequest(registry, policy, model, fields, use, selectingAction, &selecting)
}

func newRequest(registry IdentityRegistry, policy ir.Policy, model ir.ModelID, fields []ir.FieldID, use UseKind, selectingAction ir.Action, actual *ir.Condition) (Request, error) {
	if registry == nil {
		return Request{}, fmt.Errorf("policy classify: nil schema registry")
	}
	if model == (ir.ModelID{}) || policy.ModelID() != model {
		return Request{}, fmt.Errorf("policy classify: policy model mismatch")
	}
	if !validUse(use) {
		return Request{}, fmt.Errorf("policy classify: invalid use kind %d", use)
	}
	if !validAction(selectingAction) {
		return Request{}, fmt.Errorf("policy classify: invalid selecting action %d", selectingAction)
	}
	if len(fields) == 0 {
		return Request{}, fmt.Errorf("policy classify: at least one field is required")
	}
	if !registry.HasModel(golem.ModelID(model)) {
		return Request{}, fmt.Errorf("policy classify: unknown model %x", model)
	}

	validatedFields := make([]ir.FieldID, 0, len(fields))
	seen := make(map[ir.FieldID]struct{}, len(fields))
	for index, field := range fields {
		if field == (ir.FieldID{}) {
			return Request{}, fmt.Errorf("policy classify: field %d has zero identity", index)
		}
		if !registry.HasField(golem.ModelID(model), golem.FieldID(field)) {
			return Request{}, fmt.Errorf("policy classify: field %x does not belong to model %x", field, model)
		}
		if _, duplicate := seen[field]; duplicate {
			continue
		}
		seen[field] = struct{}{}
		validatedFields = append(validatedFields, field)
	}

	policyCopy, err := ir.NewPolicy(model, policy.Rules())
	if err != nil {
		return Request{}, fmt.Errorf("policy classify: invalid policy: %w", err)
	}
	readConstraint, err := resolve.RowConstraint(policyCopy, ir.ActionRead, model)
	if err != nil {
		return Request{}, fmt.Errorf("policy classify: resolve read constraint: %w", err)
	}
	selectingConstraint, err := resolve.RowConstraint(policyCopy, selectingAction, model)
	if err != nil {
		return Request{}, fmt.Errorf("policy classify: resolve selecting constraint: %w", err)
	}
	if actual != nil {
		if actual.ModelID() != model {
			return Request{}, fmt.Errorf("policy classify: actual selecting constraint model mismatch")
		}
		proved, implyErr := imply.Condition(*actual, selectingConstraint)
		if implyErr != nil {
			return Request{}, fmt.Errorf("policy classify: validate actual selecting constraint: %w", implyErr)
		}
		if !proved {
			return Request{}, fmt.Errorf("policy classify: actual selecting constraint does not preserve policy reach")
		}
		selectingConstraint = *actual
	}
	return Request{
		policy:              policyCopy,
		model:               model,
		fields:              validatedFields,
		use:                 use,
		selectingAction:     selectingAction,
		selectingConstraint: selectingConstraint,
		readConstraint:      readConstraint,
	}, nil
}

func (request Request) ModelID() ir.ModelID               { return request.model }
func (request Request) Fields() []ir.FieldID              { return append([]ir.FieldID(nil), request.fields...) }
func (request Request) UseKind() UseKind                  { return request.use }
func (request Request) SelectingAction() ir.Action        { return request.selectingAction }
func (request Request) SelectingConstraint() ir.Condition { return request.selectingConstraint }
func (request Request) ReadConstraint() ir.Condition      { return request.readConstraint }

// Classification is one immutable field result. DischargedByConstraint is
// meaningful only for AccessConditional; AccessAlways needs no proof and
// AccessNever cannot be made readable by a caller constraint.
type Classification struct {
	field        ir.FieldID
	access       Access
	condition    ir.Condition
	requires     []ir.FieldID
	dependencies dependency.Tree
	discharged   bool
}

func (classification Classification) FieldID() ir.FieldID { return classification.field }
func (classification Classification) Access() Access      { return classification.access }
func (classification Classification) FieldCondition() (ir.Condition, bool) {
	return classification.condition, classification.access == AccessConditional
}
func (classification Classification) Requires() []ir.FieldID {
	return append([]ir.FieldID(nil), classification.requires...)
}
func (classification Classification) Dependencies() dependency.Tree {
	return classification.dependencies
}
func (classification Classification) DischargedByConstraint() bool {
	return classification.discharged
}

// Plan preserves the caller's first-seen field order.
type Plan struct {
	model           ir.ModelID
	use             UseKind
	selectingAction ir.Action
	classifications []Classification
}

func (plan Plan) ModelID() ir.ModelID        { return plan.model }
func (plan Plan) UseKind() UseKind           { return plan.use }
func (plan Plan) SelectingAction() ir.Action { return plan.selectingAction }
func (plan Plan) Classifications() []Classification {
	result := make([]Classification, len(plan.classifications))
	for index := range plan.classifications {
		result[index] = plan.classifications[index].clone()
	}
	return result
}
func (plan Plan) Field(field ir.FieldID) (Classification, bool) {
	for _, classification := range plan.classifications {
		if classification.field == field {
			return classification.clone(), true
		}
	}
	return Classification{}, false
}

// Fields classifies every validated request field under the read policy while
// judging discharge against the statement's actual selecting reach.
func Fields(request Request) (Plan, error) {
	if err := request.validate(); err != nil {
		return Plan{}, err
	}
	results := make([]Classification, 0, len(request.fields))
	for _, field := range request.fields {
		fieldCondition, err := resolve.FieldCondition(request.policy, ir.ActionRead, request.model, field)
		if err != nil {
			return Plan{}, fmt.Errorf("policy classify: resolve field %x: %w", field, err)
		}
		empty, err := dependency.Empty(request.model)
		if err != nil {
			return Plan{}, err
		}
		classification := Classification{field: field, dependencies: empty}
		if truth, constant := fieldCondition.Constant(); constant {
			if truth {
				classification.access = AccessAlways
			} else {
				classification.access = AccessNever
			}
			results = append(results, classification)
			continue
		}

		conditions := supportingConditions(request.policy, field)
		if len(conditions) == 0 {
			return Plan{}, fmt.Errorf("policy classify: conditional field %x has no supporting condition", field)
		}
		dependencies, err := dependency.Collect(conditions...)
		if err != nil {
			return Plan{}, fmt.Errorf("policy classify: dependencies for field %x: %w", field, err)
		}
		discharged, err := imply.Condition(request.selectingConstraint, fieldCondition)
		if err != nil {
			return Plan{}, fmt.Errorf("policy classify: discharge field %x: %w", field, err)
		}
		classification.access = AccessConditional
		classification.condition = fieldCondition
		classification.requires = dependencies.Requires()
		classification.dependencies = dependencies.Dependencies()
		classification.discharged = discharged
		results = append(results, classification)
	}
	return Plan{
		model:           request.model,
		use:             request.use,
		selectingAction: request.selectingAction,
		classifications: results,
	}, nil
}

func (request Request) validate() error {
	if request.model == (ir.ModelID{}) || request.policy.ModelID() != request.model || len(request.fields) == 0 || !validUse(request.use) || !validAction(request.selectingAction) {
		return fmt.Errorf("policy classify: invalid request")
	}
	if request.selectingConstraint.ModelID() != request.model || request.readConstraint.ModelID() != request.model {
		return fmt.Errorf("policy classify: request constraint model mismatch")
	}
	return nil
}

func supportingConditions(policy ir.Policy, field ir.FieldID) []ir.Condition {
	rules := policy.Rules()
	chain := make([]ir.Rule, 0, len(rules))
	for index := len(rules) - 1; index >= 0; index-- {
		rule := rules[index]
		if rule.Action() != ir.ActionRead || rule.ModelID() != policy.ModelID() {
			continue
		}
		fields, modelWide := rule.Fields()
		if modelWide || contains(fields, field) {
			chain = append(chain, rule)
		}
	}

	conditions := make([]ir.Condition, 0, len(chain))
	for index, rule := range chain {
		condition, conditional := rule.Condition()
		if conditional {
			conditions = append(conditions, condition)
			continue
		}
		if len(conditions) == 0 {
			return nil
		}
		// Deliberately include conditional rules below the deciding
		// unconditional rule. They are unreachable, but retaining their
		// dependencies is the documented conservative compatibility behavior.
		for _, later := range chain[index+1:] {
			if tail, ok := later.Condition(); ok {
				conditions = append(conditions, tail)
			}
		}
		return conditions
	}
	return conditions
}

func contains(fields []ir.FieldID, wanted ir.FieldID) bool {
	for _, field := range fields {
		if field == wanted {
			return true
		}
	}
	return false
}

func validUse(use UseKind) bool {
	return use >= UseProjection && use <= UseComputedDependency
}

func validAction(action ir.Action) bool {
	return action >= ir.ActionRead && action <= ir.ActionDelete
}

func (classification Classification) clone() Classification {
	classification.requires = append([]ir.FieldID(nil), classification.requires...)
	return classification
}
