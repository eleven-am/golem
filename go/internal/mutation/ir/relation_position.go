package ir

import (
	"bytes"
	"fmt"
	"sort"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

// RelationPositionKind is the closed description of how a nested node locates
// rows relative to its parent. In particular CurrentToOne is not an absent
// target and SetDifference with zero desired targets is not an absent filter.
type RelationPositionKind uint8

const (
	PositionEndpoint RelationPositionKind = iota + 1
	PositionCurrentToOne
	PositionRelatedTarget
	PositionRelatedPredicate
	PositionEntireMembership
	PositionSetDifference
	// PositionBranchResult identifies the persisted target produced by the
	// selected connect-or-create branch. It prevents the owner effect from
	// substituting the authored selector for the truthful created row.
	PositionBranchResult
)

type ExpansionKind uint8

const (
	ExpandCurrentToOne ExpansionKind = iota + 1
	ExpandRelatedPredicate
	ExpandEntireMembership
	ExpandSetDifference
)

// ExpansionRequirement records database-discovered identity work without
// pretending the reusable plan already knows those rows.
type ExpansionRequirement struct {
	kind    ExpansionKind
	maxRows uint32
}

func NewExpansionRequirement(kind ExpansionKind, maxRows uint32) (ExpansionRequirement, error) {
	if kind < ExpandCurrentToOne || kind > ExpandSetDifference || maxRows == 0 {
		return ExpansionRequirement{}, fmt.Errorf("P4_MUTATION_IR_EXPANSION: valid kind and positive row bound are required")
	}
	if kind == ExpandCurrentToOne && maxRows != 1 {
		return ExpansionRequirement{}, fmt.Errorf("P4_MUTATION_IR_EXPANSION: current-to-one expansion is bounded to one row")
	}
	return ExpansionRequirement{kind: kind, maxRows: maxRows}, nil
}

func (requirement ExpansionRequirement) Kind() ExpansionKind { return requirement.kind }
func (requirement ExpansionRequirement) MaxRows() uint32     { return requirement.maxRows }

type RelationPositionInput struct {
	ParentModel policyir.ModelID
	Field       policyir.FieldID
	Relation    policyir.RelationID
	TargetModel policyir.ModelID
	Kind        RelationPositionKind
	Target      *Target
	Predicate   *policyir.Condition
	Desired     []Target
	Expansion   *ExpansionRequirement
}

type RelationPosition struct {
	parentModel policyir.ModelID
	field       policyir.FieldID
	relation    policyir.RelationID
	targetModel policyir.ModelID
	kind        RelationPositionKind
	target      *Target
	predicate   *policyir.Condition
	desired     []Target
	expansion   *ExpansionRequirement
}

func NewRelationPosition(input RelationPositionInput) (RelationPosition, error) {
	result := RelationPosition{parentModel: input.ParentModel, field: input.Field, relation: input.Relation, targetModel: input.TargetModel, kind: input.Kind}
	if input.Target != nil {
		value := input.Target.clone()
		result.target = &value
	}
	if input.Predicate != nil {
		value := *input.Predicate
		result.predicate = &value
	}
	if input.Expansion != nil {
		value := *input.Expansion
		result.expansion = &value
	}
	result.desired = make([]Target, len(input.Desired))
	for index := range input.Desired {
		result.desired[index] = input.Desired[index].clone()
	}
	sort.Slice(result.desired, func(i, j int) bool {
		return bytes.Compare(targetCanonicalKey(result.desired[i]), targetCanonicalKey(result.desired[j])) < 0
	})
	if err := result.validate(); err != nil {
		return RelationPosition{}, err
	}
	return result, nil
}

func (position RelationPosition) ParentModelID() policyir.ModelID { return position.parentModel }
func (position RelationPosition) FieldID() policyir.FieldID       { return position.field }
func (position RelationPosition) RelationID() policyir.RelationID { return position.relation }
func (position RelationPosition) TargetModelID() policyir.ModelID { return position.targetModel }
func (position RelationPosition) Kind() RelationPositionKind      { return position.kind }
func (position RelationPosition) Target() (Target, bool) {
	if position.target == nil {
		return Target{}, false
	}
	return position.target.clone(), true
}
func (position RelationPosition) Predicate() (policyir.Condition, bool) {
	if position.predicate == nil {
		return policyir.Condition{}, false
	}
	return *position.predicate, true
}
func (position RelationPosition) DesiredTargets() []Target {
	result := make([]Target, len(position.desired))
	for index := range position.desired {
		result[index] = position.desired[index].clone()
	}
	return result
}
func (position RelationPosition) Expansion() (ExpansionRequirement, bool) {
	if position.expansion == nil {
		return ExpansionRequirement{}, false
	}
	return *position.expansion, true
}

func (position RelationPosition) clone() RelationPosition {
	result := RelationPosition{parentModel: position.parentModel, field: position.field, relation: position.relation, targetModel: position.targetModel, kind: position.kind}
	if position.target != nil {
		value := position.target.clone()
		result.target = &value
	}
	if position.predicate != nil {
		value := *position.predicate
		result.predicate = &value
	}
	if position.expansion != nil {
		value := *position.expansion
		result.expansion = &value
	}
	result.desired = make([]Target, len(position.desired))
	for index := range position.desired {
		result.desired[index] = position.desired[index].clone()
	}
	return result
}

func (position RelationPosition) validate() error {
	if position.parentModel == (policyir.ModelID{}) || position.field == (policyir.FieldID{}) || position.relation == (policyir.RelationID{}) || position.targetModel == (policyir.ModelID{}) {
		return fmt.Errorf("P4_MUTATION_IR_RELATION_POSITION: endpoint identities must be non-zero")
	}
	if position.kind < PositionEndpoint || position.kind > PositionBranchResult {
		return fmt.Errorf("P4_MUTATION_IR_RELATION_POSITION: invalid position kind")
	}
	if position.target != nil {
		if err := position.target.validate(); err != nil || position.target.model != position.targetModel {
			return fmt.Errorf("P4_MUTATION_IR_RELATION_POSITION: target is invalid or belongs to another model")
		}
	}
	if position.predicate != nil {
		if err := position.predicate.Validate(); err != nil || position.predicate.ModelID() != position.targetModel {
			return fmt.Errorf("P4_MUTATION_IR_RELATION_POSITION: predicate is invalid or belongs to another model")
		}
	}
	for index, target := range position.desired {
		if err := target.validate(); err != nil || target.model != position.targetModel {
			return fmt.Errorf("P4_MUTATION_IR_RELATION_POSITION: desired target %d is invalid or foreign", index)
		}
		if index > 0 && bytes.Equal(targetCanonicalKey(position.desired[index-1]), targetCanonicalKey(target)) {
			return fmt.Errorf("P4_MUTATION_IR_RELATION_POSITION: desired target is duplicate")
		}
	}
	hasTarget, hasPredicate, hasDesired, hasExpansion := position.target != nil, position.predicate != nil, len(position.desired) != 0, position.expansion != nil
	switch position.kind {
	case PositionEndpoint:
		if hasTarget || hasPredicate || hasDesired || hasExpansion {
			return fmt.Errorf("P4_MUTATION_IR_RELATION_POSITION: endpoint-only position carries selection data")
		}
	case PositionCurrentToOne:
		if hasTarget || hasPredicate || hasDesired || !hasExpansion || position.expansion.kind != ExpandCurrentToOne {
			return fmt.Errorf("P4_MUTATION_IR_RELATION_POSITION: current-to-one shape is invalid")
		}
	case PositionRelatedTarget:
		if !hasTarget || hasPredicate || hasDesired || hasExpansion {
			return fmt.Errorf("P4_MUTATION_IR_RELATION_POSITION: related-target shape is invalid")
		}
	case PositionRelatedPredicate:
		if hasTarget || !hasPredicate || hasDesired || !hasExpansion || position.expansion.kind != ExpandRelatedPredicate {
			return fmt.Errorf("P4_MUTATION_IR_RELATION_POSITION: related-predicate shape is invalid")
		}
	case PositionEntireMembership:
		if hasTarget || hasPredicate || hasDesired || !hasExpansion || position.expansion.kind != ExpandEntireMembership {
			return fmt.Errorf("P4_MUTATION_IR_RELATION_POSITION: entire-membership shape is invalid")
		}
	case PositionSetDifference:
		if hasTarget || hasPredicate || !hasExpansion || position.expansion.kind != ExpandSetDifference {
			return fmt.Errorf("P4_MUTATION_IR_RELATION_POSITION: set-difference shape is invalid")
		}
	case PositionBranchResult:
		if hasTarget || hasPredicate || hasDesired || hasExpansion {
			return fmt.Errorf("P4_MUTATION_IR_RELATION_POSITION: branch-result shape is invalid")
		}
	}
	return nil
}

func targetCanonicalKey(target Target) []byte {
	encoder := canonicalEncoder{}
	encoder.target(target)
	return append([]byte(nil), encoder.buffer.Bytes()...)
}
