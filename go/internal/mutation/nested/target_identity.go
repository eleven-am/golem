package nested

import (
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

type knownValues map[policyir.FieldID]policyir.Value

func RefuseOffTarget(operation mutationir.Operation, target mutationir.Target, written map[policyir.FieldID]policyir.Value) error {
	for _, selector := range target.Values() {
		value, present := written[selector.FieldID()]
		if !present || !mutationdecode.EqualValue(value, selector.Value()) {
			return &TargetIdentityError{Operation: operation, Model: target.ModelID(), Field: selector.FieldID()}
		}
	}
	return nil
}

func RootCreatedValues(registry *schema.Registry, operations []mutationir.ScalarOperation, relations []golem.FrozenNestedMutation) (map[policyir.FieldID]policyir.Value, error) {
	written := knownValues{}
	for _, mutation := range relations {
		if mutation.Action() != golem.MutationRelationConnect {
			continue
		}
		endpoint, ok := registry.RelationEndpoint(mutation.ParentModelID(), mutation.FieldID(), mutation.RelationID())
		if !ok {
			return nil, fail(CodeRelation, mutation.ParentModelID(), mutation.FieldID(), "relation endpoint identities do not match the active registry", nil)
		}
		for _, branch := range mutation.Branches() {
			public, selected := branch.Target()
			if !selected {
				continue
			}
			bound, err := mutationbind.Target(public, endpoint.TargetModelID(), registry)
			if err != nil {
				return nil, fail(CodeBinding, endpoint.TargetModelID(), endpoint.FieldID(), "connect target did not bind", err)
			}
			connectedValues(endpoint, bound.Target(), written)
		}
	}
	authoredValues(operations, written)
	return written, nil
}

func refuseNestedCreateOffTarget(registry *schema.Registry, nodes []mutationir.Node, identity targetIdentity, create mutationir.Node, anchor *AppliedNode) error {
	written, err := impliedValues(registry, create, anchor)
	if err != nil {
		return err
	}
	for _, ordinal := range create.ChildOrdinals() {
		if int(ordinal) >= len(nodes) {
			continue
		}
		child := nodes[ordinal]
		position, positioned := child.RelationPosition()
		if child.Operation() != mutationir.BranchProbe || !child.ExecutesBeforeParent() || !positioned {
			continue
		}
		target, selected := position.Target()
		endpoint, found := registry.RelationEndpoint(golem.ModelID(position.ParentModelID()), golem.FieldID(position.FieldID()), golem.RelationID(position.RelationID()))
		if selected && found {
			connectedValues(endpoint, target, written)
		}
	}
	authoredValues(create.ScalarOperations(), written)
	return RefuseOffTarget(identity.operation, identity.target, written)
}

func impliedValues(registry *schema.Registry, create mutationir.Node, anchor *AppliedNode) (knownValues, error) {
	implied := knownValues{}
	position, positioned := create.RelationPosition()
	if !positioned || anchor == nil {
		return implied, nil
	}
	endpoint, found := registry.RelationEndpoint(golem.ModelID(position.ParentModelID()), golem.FieldID(position.FieldID()), golem.RelationID(position.RelationID()))
	if !found || endpoint.Role() != compilerir.RelationInverse {
		return implied, nil
	}
	row, err := appliedRow(*anchor)
	if err != nil {
		return nil, err
	}
	for _, pair := range endpoint.Correlation() {
		cell, present := row.Cell(policyir.FieldID(pair.ParentFieldID()))
		if !present || cell.IsNull() {
			continue
		}
		if value, valued := cell.PolicyValue(); valued {
			implied[policyir.FieldID(pair.ChildFieldID())] = value
		}
	}
	return implied, nil
}

func authoredValues(operations []mutationir.ScalarOperation, written knownValues) {
	for _, operation := range operations {
		value, present := operation.Value()
		if operation.Kind() == mutationir.ScalarSet && present && !operation.RuntimeOwned() {
			written[operation.FieldID()] = value
		}
	}
}

func connectedValues(endpoint schema.RelationEndpoint, target mutationir.Target, written knownValues) {
	if endpoint.Role() != compilerir.RelationSource {
		return
	}
	selected := make(knownValues, len(target.Values()))
	for _, value := range target.Values() {
		selected[value.FieldID()] = value.Value()
	}
	for _, pair := range endpoint.Correlation() {
		if value, present := selected[policyir.FieldID(pair.ChildFieldID())]; present {
			written[policyir.FieldID(pair.ParentFieldID())] = value
		}
	}
}
