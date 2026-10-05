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

func refuseGraphOffTarget(registry *schema.Registry, root mutationir.NodeInput) error {
	known := knownValues{}
	switch root.Operation {
	case mutationir.Create:
		known = createdValues(registry, root, known)
	case mutationir.Update:
		known = selectedValues(root.Target, known)
	}
	return refuseChildrenOffTarget(registry, root.Children, known)
}

func refuseChildrenOffTarget(registry *schema.Registry, children []mutationir.NodeInput, parent knownValues) error {
	for _, child := range children {
		if err := refuseNodeOffTarget(registry, child, parent); err != nil {
			return err
		}
	}
	return nil
}

func refuseNodeOffTarget(registry *schema.Registry, node mutationir.NodeInput, parent knownValues) error {
	switch node.Operation {
	case mutationir.CreateMany:
		return refuseChildrenOffTarget(registry, node.Children, parent)
	case mutationir.Create:
		return refuseChildrenOffTarget(registry, node.Children, createdValues(registry, node, impliedValues(registry, node.RelationPosition, parent)))
	case mutationir.Update:
		return refuseChildrenOffTarget(registry, node.Children, selectedValues(positionTarget(node.RelationPosition), impliedValues(registry, node.RelationPosition, parent)))
	case mutationir.Upsert, mutationir.ConnectOrCreate:
		implied := impliedValues(registry, node.RelationPosition, parent)
		target := positionTarget(node.RelationPosition)
		for _, branch := range node.Children {
			switch branch.Operation {
			case mutationir.Create:
				written := createdValues(registry, branch, implied)
				if target != nil {
					if err := RefuseOffTarget(node.Operation, *target, written); err != nil {
						return err
					}
				}
				if err := refuseChildrenOffTarget(registry, branch.Children, written); err != nil {
					return err
				}
			case mutationir.Update:
				if err := refuseChildrenOffTarget(registry, branch.Children, selectedValues(target, implied)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func createdValues(registry *schema.Registry, node mutationir.NodeInput, implied knownValues) knownValues {
	written := make(knownValues, len(implied)+len(node.ScalarOperations))
	for field, value := range implied {
		written[field] = value
	}
	for _, child := range node.Children {
		if child.Operation != mutationir.BranchProbe || !child.BeforeParent || child.RelationPosition == nil {
			continue
		}
		target, selected := child.RelationPosition.Target()
		endpoint, ok := positionEndpoint(registry, child.RelationPosition)
		if selected && ok {
			connectedValues(endpoint, target, written)
		}
	}
	authoredValues(node.ScalarOperations, written)
	return written
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
	selected := selectedValues(&target, knownValues{})
	for _, pair := range endpoint.Correlation() {
		if value, present := selected[policyir.FieldID(pair.ChildFieldID())]; present {
			written[policyir.FieldID(pair.ParentFieldID())] = value
		}
	}
}

func impliedValues(registry *schema.Registry, position *mutationir.RelationPosition, parent knownValues) knownValues {
	implied := knownValues{}
	endpoint, ok := positionEndpoint(registry, position)
	if !ok || endpoint.Role() != compilerir.RelationInverse {
		return implied
	}
	for _, pair := range endpoint.Correlation() {
		if value, present := parent[policyir.FieldID(pair.ParentFieldID())]; present {
			implied[policyir.FieldID(pair.ChildFieldID())] = value
		}
	}
	return implied
}

func selectedValues(target *mutationir.Target, implied knownValues) knownValues {
	selected := make(knownValues, len(implied))
	for field, value := range implied {
		selected[field] = value
	}
	if target == nil {
		return selected
	}
	for _, value := range target.Values() {
		selected[value.FieldID()] = value.Value()
	}
	return selected
}

func positionTarget(position *mutationir.RelationPosition) *mutationir.Target {
	if position == nil {
		return nil
	}
	target, selected := position.Target()
	if !selected {
		return nil
	}
	return &target
}

func positionEndpoint(registry *schema.Registry, position *mutationir.RelationPosition) (schema.RelationEndpoint, bool) {
	if position == nil {
		return schema.RelationEndpoint{}, false
	}
	return registry.RelationEndpoint(golem.ModelID(position.ParentModelID()), golem.FieldID(position.FieldID()), golem.RelationID(position.RelationID()))
}
