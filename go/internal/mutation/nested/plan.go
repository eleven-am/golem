package nested

import (
	"sort"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	"github.com/eleven-am/golem/go/internal/policy/classify"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/normalize"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

func (builder *builder) updateNode(endpoint schema.RelationEndpoint, branch golem.FrozenNestedMutationBranch, branchKind mutationir.Branch, classifyTarget bool, depth uint16) (mutationir.NodeInput, error) {
	input, ok := branch.Input()
	if !ok {
		return mutationir.NodeInput{}, fail(CodeShape, branch.ModelID(), endpoint.FieldID(), "update branch has no input", nil)
	}
	boundInput, err := mutationbind.UpdateInput(input, builder.request.Registry)
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeBinding, branch.ModelID(), endpoint.FieldID(), "nested update input did not bind", err)
	}
	position, condition, err := builder.singlePosition(endpoint, branch)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	if condition != nil && classifyTarget {
		if err := builder.classify(*condition, classify.UseSelector, mutationir.Update, endpoint); err != nil {
			return mutationir.NodeInput{}, err
		}
	}
	node := builder.baseNode(mutationir.Update, policyir.ModelID(branch.ModelID()), endpoint.RelationID(), &position, branchKind)
	node.ScalarOperations = boundInput.Operations()
	children, childErr := builder.buildRelations(policyir.ModelID(branch.ModelID()), mutationir.Update, input.Relations(), depth+1)
	if childErr != nil {
		return mutationir.NodeInput{}, childErr
	}
	node.Children = children
	decorated, err := builder.decorate(node, condition)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	return builder.retainSource(decorated, endpoint, branch.Action(), branch), nil
}

func (builder *builder) updateManyNode(endpoint schema.RelationEndpoint, branch golem.FrozenNestedMutationBranch, depth uint16) (mutationir.NodeInput, error) {
	publicPredicate, predicateOK := branch.Predicate()
	input, inputOK := branch.Input()
	if !predicateOK || !inputOK {
		return mutationir.NodeInput{}, fail(CodeShape, branch.ModelID(), endpoint.FieldID(), "update-many predicate or input is absent", nil)
	}
	predicate, err := mutationbind.BatchPredicate(publicPredicate, branch.ModelID(), builder.request.Registry)
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeBinding, branch.ModelID(), endpoint.FieldID(), "nested update-many predicate did not bind", err)
	}
	boundInput, err := mutationbind.UpdateManyInput(input, builder.request.Registry)
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeBinding, branch.ModelID(), endpoint.FieldID(), "nested update-many input did not bind", err)
	}
	if err := builder.classify(predicate, classify.UseFilter, mutationir.UpdateMany, endpoint); err != nil {
		return mutationir.NodeInput{}, err
	}
	expansion, err := mutationir.NewExpansionRequirement(mutationir.ExpandRelatedPredicate, builder.request.MaxRows)
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeIR, endpoint.ModelID(), endpoint.FieldID(), "update-many expansion is invalid", err)
	}
	position, err := mutationir.NewRelationPosition(mutationir.RelationPositionInput{ParentModel: policyir.ModelID(endpoint.ModelID()), Field: policyir.FieldID(endpoint.FieldID()), Relation: policyir.RelationID(endpoint.RelationID()), TargetModel: policyir.ModelID(endpoint.TargetModelID()), Kind: mutationir.PositionRelatedPredicate, Predicate: &predicate, Expansion: &expansion})
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeIR, endpoint.ModelID(), endpoint.FieldID(), "update-many relation position is invalid", err)
	}
	node := builder.baseNode(mutationir.UpdateMany, policyir.ModelID(branch.ModelID()), endpoint.RelationID(), &position, mutationir.MainBranch)
	node.ScalarOperations = boundInput.Operations()
	children, childErr := builder.buildRelations(policyir.ModelID(branch.ModelID()), mutationir.UpdateMany, input.Relations(), depth+1)
	if childErr != nil {
		return mutationir.NodeInput{}, childErr
	}
	node.Children = children
	decorated, err := builder.decorate(node, &predicate)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	return builder.retainSource(decorated, endpoint, branch.Action(), branch), nil
}

func (builder *builder) deleteNode(endpoint schema.RelationEndpoint, branch golem.FrozenNestedMutationBranch, branchKind mutationir.Branch) (mutationir.NodeInput, error) {
	position, condition, err := builder.singlePosition(endpoint, branch)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	if condition != nil {
		if err := builder.classify(*condition, classify.UseSelector, mutationir.Delete, endpoint); err != nil {
			return mutationir.NodeInput{}, err
		}
	}
	node := builder.baseNode(mutationir.Delete, policyir.ModelID(branch.ModelID()), endpoint.RelationID(), &position, branchKind)
	decorated, err := builder.decorate(node, condition)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	return builder.retainSource(decorated, endpoint, branch.Action(), branch), nil
}

func (builder *builder) deleteManyNode(endpoint schema.RelationEndpoint, branch golem.FrozenNestedMutationBranch) (mutationir.NodeInput, error) {
	public, ok := branch.Predicate()
	if !ok {
		return mutationir.NodeInput{}, fail(CodeShape, branch.ModelID(), endpoint.FieldID(), "delete-many predicate is absent", nil)
	}
	predicate, err := mutationbind.BatchPredicate(public, branch.ModelID(), builder.request.Registry)
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeBinding, branch.ModelID(), endpoint.FieldID(), "nested delete-many predicate did not bind", err)
	}
	if err := builder.classify(predicate, classify.UseFilter, mutationir.DeleteMany, endpoint); err != nil {
		return mutationir.NodeInput{}, err
	}
	expansion, err := mutationir.NewExpansionRequirement(mutationir.ExpandRelatedPredicate, builder.request.MaxRows)
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeIR, endpoint.ModelID(), endpoint.FieldID(), "delete-many expansion is invalid", err)
	}
	position, err := mutationir.NewRelationPosition(mutationir.RelationPositionInput{ParentModel: policyir.ModelID(endpoint.ModelID()), Field: policyir.FieldID(endpoint.FieldID()), Relation: policyir.RelationID(endpoint.RelationID()), TargetModel: policyir.ModelID(endpoint.TargetModelID()), Kind: mutationir.PositionRelatedPredicate, Predicate: &predicate, Expansion: &expansion})
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeIR, endpoint.ModelID(), endpoint.FieldID(), "delete-many relation position is invalid", err)
	}
	node := builder.baseNode(mutationir.DeleteMany, policyir.ModelID(branch.ModelID()), endpoint.RelationID(), &position, mutationir.MainBranch)
	decorated, err := builder.decorate(node, &predicate)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	return builder.retainSource(decorated, endpoint, branch.Action(), branch), nil
}

func (builder *builder) connectOrCreateNode(endpoint schema.RelationEndpoint, branches []golem.FrozenNestedMutationBranch, depth uint16, sourceDependency bool) ([]mutationir.NodeInput, error) {
	connect, create := branches[0], branches[1]
	if connect.Branch() != golem.MutationRelationConnectOrCreateConnectBranch {
		connect, create = create, connect
	}
	public, ok := connect.Target()
	if !ok {
		return nil, fail(CodeShape, endpoint.ModelID(), endpoint.FieldID(), "connect-or-create target is absent", nil)
	}
	bound, err := mutationbind.Target(public, endpoint.TargetModelID(), builder.request.Registry)
	if err != nil {
		return nil, fail(CodeBinding, endpoint.TargetModelID(), endpoint.FieldID(), "connect-or-create target did not bind", err)
	}
	condition := completeTargetCondition(bound)
	if err := builder.classify(condition, classify.UseSelector, mutationir.ConnectOrCreate, endpoint); err != nil {
		return nil, err
	}
	target := bound.Target()
	position, err := mutationir.NewRelationPosition(mutationir.RelationPositionInput{ParentModel: policyir.ModelID(endpoint.ModelID()), Field: policyir.FieldID(endpoint.FieldID()), Relation: policyir.RelationID(endpoint.RelationID()), TargetModel: policyir.ModelID(endpoint.TargetModelID()), Kind: mutationir.PositionRelatedTarget, Target: &target})
	if err != nil {
		return nil, fail(CodeIR, endpoint.ModelID(), endpoint.FieldID(), "connect-or-create relation position is invalid", err)
	}
	wrapper, err := builder.decorate(builder.baseNode(mutationir.ConnectOrCreate, policyir.ModelID(endpoint.TargetModelID()), endpoint.RelationID(), &position, mutationir.MainBranch), &condition)
	if err != nil {
		return nil, err
	}
	connectNode, err := builder.decorateMembership(builder.baseNode(mutationir.Connect, policyir.ModelID(endpoint.TargetModelID()), golem.RelationID{}, &position, mutationir.ConnectOrCreateConnectBranch), &condition, endpoint)
	if err != nil {
		return nil, err
	}
	connectNode = builder.retainSource(connectNode, endpoint, connect.Action(), connect)
	createNode, err := builder.createNode(endpoint, create, mutationir.ConnectOrCreateCreateBranch, depth, sourceDependency)
	if err != nil {
		return nil, err
	}
	createNode.Relation = policyir.RelationID{}
	if endpoint.Role() == compilerir.RelationSource {
		probeNode, probeErr := builder.decorate(builder.baseNode(mutationir.BranchProbe, policyir.ModelID(endpoint.TargetModelID()), golem.RelationID{}, &position, mutationir.ConnectOrCreateConnectBranch), &condition)
		if probeErr != nil {
			return nil, probeErr
		}
		if sourceDependency {
			probeNode.BeforeParent = true
			probeNode = builder.retainSource(probeNode, endpoint, connect.Action(), connect)
			wrapper.BeforeParent = true
			wrapper.Children = []mutationir.NodeInput{probeNode, createNode}
			return []mutationir.NodeInput{wrapper}, nil
		}
		branchPosition, branchErr := mutationir.NewRelationPosition(mutationir.RelationPositionInput{
			ParentModel: policyir.ModelID(endpoint.ModelID()), Field: policyir.FieldID(endpoint.FieldID()), Relation: policyir.RelationID(endpoint.RelationID()),
			TargetModel: policyir.ModelID(endpoint.TargetModelID()), Kind: mutationir.PositionBranchResult,
		})
		if branchErr != nil {
			return nil, fail(CodeIR, endpoint.ModelID(), endpoint.FieldID(), "connect-or-create owner effect position is invalid", branchErr)
		}
		ownerEffect := builder.baseNode(mutationir.Connect, policyir.ModelID(endpoint.ModelID()), endpoint.RelationID(), &branchPosition, mutationir.MainBranch)
		ownerEffect, err = builder.decorateMembership(ownerEffect, nil, endpoint)
		if err != nil {
			return nil, err
		}
		// The selected branch result completes the already-running owner
		// mutation and must not become a duplicate logical owner update.
		ownerEffect.Hooks = nil
		ownerEffect.Fact = mutationir.FactRequirement{}
		connectEffect := builder.retainSource(ownerEffect, endpoint, connect.Action(), connect)
		createEffect := builder.retainSource(ownerEffect, endpoint, create.Action(), create)
		probeNode.Children = append(probeNode.Children, connectEffect)
		createNode.Children = append(createNode.Children, createEffect)
		wrapper.Children = []mutationir.NodeInput{probeNode, createNode}
		return []mutationir.NodeInput{wrapper}, nil
	}
	wrapper.Children = []mutationir.NodeInput{connectNode, createNode}
	return []mutationir.NodeInput{wrapper}, nil
}

func (builder *builder) upsertNode(endpoint schema.RelationEndpoint, branches []golem.FrozenNestedMutationBranch, depth uint16) ([]mutationir.NodeInput, error) {
	create, update := branches[0], branches[1]
	if create.Branch() != golem.MutationRelationUpsertCreateBranch {
		create, update = update, create
	}
	position, condition, err := builder.singlePosition(endpoint, update)
	if err != nil {
		return nil, err
	}
	if condition != nil {
		if err := builder.classify(*condition, classify.UseSelector, mutationir.Upsert, endpoint); err != nil {
			return nil, err
		}
	}
	wrapper, err := builder.decorate(builder.baseNode(mutationir.Upsert, policyir.ModelID(endpoint.TargetModelID()), endpoint.RelationID(), &position, mutationir.MainBranch), condition)
	if err != nil {
		return nil, err
	}
	createNode, err := builder.createNode(endpoint, create, mutationir.UpsertCreateBranch, depth, false)
	if err != nil {
		return nil, err
	}
	createNode.Relation = policyir.RelationID{}
	updateNode, err := builder.updateNode(endpoint, update, mutationir.UpsertUpdateBranch, false, depth)
	if err != nil {
		return nil, err
	}
	updateNode.Relation = policyir.RelationID{}
	wrapper.Children = []mutationir.NodeInput{createNode, updateNode}
	return []mutationir.NodeInput{wrapper}, nil
}

func (builder *builder) singlePosition(endpoint schema.RelationEndpoint, branch golem.FrozenNestedMutationBranch) (mutationir.RelationPosition, *policyir.Condition, error) {
	input := mutationir.RelationPositionInput{ParentModel: policyir.ModelID(endpoint.ModelID()), Field: policyir.FieldID(endpoint.FieldID()), Relation: policyir.RelationID(endpoint.RelationID()), TargetModel: policyir.ModelID(endpoint.TargetModelID())}
	if public, ok := branch.Target(); ok {
		bound, err := mutationbind.Target(public, endpoint.TargetModelID(), builder.request.Registry)
		if err != nil {
			return mutationir.RelationPosition{}, nil, fail(CodeBinding, endpoint.TargetModelID(), endpoint.FieldID(), "nested target did not bind", err)
		}
		target := bound.Target()
		input.Kind, input.Target = mutationir.PositionRelatedTarget, &target
		condition := completeTargetCondition(bound)
		position, positionErr := mutationir.NewRelationPosition(input)
		return position, &condition, positionErr
	}
	expansion, expansionErr := mutationir.NewExpansionRequirement(mutationir.ExpandCurrentToOne, 1)
	if expansionErr != nil {
		return mutationir.RelationPosition{}, nil, fail(CodeIR, endpoint.ModelID(), endpoint.FieldID(), "current relation expansion is invalid", expansionErr)
	}
	input.Kind, input.Expansion = mutationir.PositionCurrentToOne, &expansion
	position, err := mutationir.NewRelationPosition(input)
	return position, nil, err
}

func (builder *builder) decorate(node mutationir.NodeInput, position *policyir.Condition) (mutationir.NodeInput, error) {
	model, modelOK := builder.request.Registry.Model(golem.ModelID(node.Model))
	if !modelOK {
		return mutationir.NodeInput{}, fail(CodeIR, golem.ModelID(node.Model), golem.FieldID{}, "nested model is absent", nil)
	}
	primary := make([]policyir.FieldID, len(model.PrimaryKey()))
	for index, field := range model.PrimaryKey() {
		primary[index] = policyir.FieldID(field)
	}
	if model.SubscriptionsEnabled() {
		var action mutationir.FactAction
		var before, after []policyir.FieldID
		switch node.Operation {
		case mutationir.Create:
			action, after = mutationir.FactCreated, primary
		case mutationir.Update, mutationir.UpdateMany, mutationir.Connect, mutationir.Disconnect, mutationir.SetRelation:
			action, before, after = mutationir.FactUpdated, primary, primary
		case mutationir.Delete, mutationir.DeleteMany:
			action, before = mutationir.FactDeleted, primary
		}
		if action != 0 {
			fact, factErr := mutationir.NewFactRequirement(action, before, after, nil)
			if factErr != nil {
				return mutationir.NodeInput{}, fail(CodeIR, golem.ModelID(node.Model), golem.FieldID{}, "nested fact requirement is invalid", factErr)
			}
			node.Fact = fact
		}
	}
	if builder.request.Stance == mutationir.System {
		return builder.finalizeImages(node)
	}
	policy, ok := builder.request.Policies.Policy(node.Model)
	if !ok {
		return mutationir.NodeInput{}, fail(CodePolicy, golem.ModelID(node.Model), golem.FieldID{}, "nested model policy is absent", nil)
	}
	action := actionFor(node.Operation)
	row, err := resolve.RowConstraint(policy, action, node.Model)
	if err != nil {
		return mutationir.NodeInput{}, fail(CodePolicy, golem.ModelID(node.Model), golem.FieldID{}, "nested row constraint could not resolve", err)
	}
	if node.Operation == mutationir.Create || node.Operation == mutationir.Update || node.Operation == mutationir.UpdateMany || node.Operation == mutationir.Connect || node.Operation == mutationir.Disconnect || node.Operation == mutationir.SetRelation {
		value := row
		node.RowPostcondition = &value
	}
	if existing(node.Operation) {
		complete := row
		if position != nil {
			complete, err = conjoin(node.Model, row, *position)
			if err != nil {
				return mutationir.NodeInput{}, fail(CodePolicy, golem.ModelID(node.Model), golem.FieldID{}, "nested selecting condition could not combine", err)
			}
		}
		selection, selectionErr := mutationir.NewSelectionRequirement(action, complete)
		if selectionErr != nil {
			return mutationir.NodeInput{}, fail(CodeIR, golem.ModelID(node.Model), golem.FieldID{}, "nested selection requirement is invalid", selectionErr)
		}
		node.Selection = &selection
	}
	for _, operation := range node.ScalarOperations {
		condition, conditionErr := resolve.FieldCondition(policy, action, node.Model, operation.FieldID())
		if conditionErr != nil {
			return mutationir.NodeInput{}, fail(CodePolicy, golem.ModelID(node.Model), golem.FieldID(operation.FieldID()), "nested field condition could not resolve", conditionErr)
		}
		authorization, authorizationErr := mutationir.NewFieldAuthorization(operation.FieldID(), condition)
		if authorizationErr != nil {
			return mutationir.NodeInput{}, fail(CodeIR, golem.ModelID(node.Model), golem.FieldID(operation.FieldID()), "nested field authorization is invalid", authorizationErr)
		}
		node.FieldConditions = append(node.FieldConditions, authorization)
	}
	hooks, hookErr := builder.hooksFor(node.Operation, node.Model)
	if hookErr != nil {
		return mutationir.NodeInput{}, hookErr
	}
	node.Hooks = hooks
	return builder.finalizeImages(node)
}

func (builder *builder) decorateMembership(node mutationir.NodeInput, position *policyir.Condition, endpoint schema.RelationEndpoint) (mutationir.NodeInput, error) {
	decorated, err := builder.decorate(node, position)
	if err != nil || builder.request.Stance == mutationir.System {
		return decorated, err
	}
	return builder.decorateOwnedFields(decorated, membershipOwnedFields(endpoint), policyir.ActionUpdate, endpoint.FieldID())
}

func (builder *builder) decorateOwnedFields(node mutationir.NodeInput, fields []policyir.FieldID, action policyir.Action, relationField golem.FieldID) (mutationir.NodeInput, error) {
	if builder.request.Stance == mutationir.System || len(fields) == 0 {
		return node, nil
	}
	policy, ok := builder.request.Policies.Policy(node.Model)
	if !ok {
		return mutationir.NodeInput{}, fail(CodePolicy, golem.ModelID(node.Model), relationField, "runtime-owned field policy is absent", nil)
	}
	for _, field := range fields {
		condition, conditionErr := resolve.FieldCondition(policy, action, node.Model, field)
		if conditionErr != nil {
			return mutationir.NodeInput{}, fail(CodePolicy, golem.ModelID(node.Model), golem.FieldID(field), "runtime-owned field condition could not resolve", conditionErr)
		}
		authorization, authorizationErr := mutationir.NewFieldAuthorization(field, condition)
		if authorizationErr != nil {
			return mutationir.NodeInput{}, fail(CodeIR, golem.ModelID(node.Model), golem.FieldID(field), "runtime-owned field authorization is invalid", authorizationErr)
		}
		node.FieldConditions = append(node.FieldConditions, authorization)
	}
	return builder.finalizeImages(node)
}

func (builder *builder) hooksFor(operation mutationir.Operation, model policyir.ModelID) ([]mutationir.HookRequirement, error) {
	if builder.request.Stance == mutationir.System || builder.request.HookInventory == nil {
		return nil, nil
	}
	inventory := builder.request.HookInventory(model)
	var phases []mutationir.HookPhase
	var hookOperation mutationir.HookOperation
	switch operation {
	case mutationir.Create:
		phases, hookOperation = inventory.Create, mutationir.HookCreate
	case mutationir.Update, mutationir.Connect, mutationir.Disconnect, mutationir.SetRelation:
		phases, hookOperation = inventory.Update, mutationir.HookUpdate
	case mutationir.Delete:
		phases, hookOperation = inventory.Delete, mutationir.HookDelete
	case mutationir.UpdateMany:
		phases, hookOperation = inventory.UpdateMany, mutationir.HookUpdateMany
	case mutationir.DeleteMany:
		phases, hookOperation = inventory.DeleteMany, mutationir.HookDeleteMany
	default:
		return nil, nil
	}
	ordered := append([]mutationir.HookPhase(nil), phases...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	result := make([]mutationir.HookRequirement, 0, len(ordered))
	for index, phase := range ordered {
		if index != 0 && ordered[index-1] == phase {
			continue
		}
		requirement, err := mutationir.NewHookRequirement(phase, hookOperation)
		if err != nil {
			return nil, fail(CodeIR, golem.ModelID(model), golem.FieldID{}, "nested hook inventory is invalid", err)
		}
		result = append(result, requirement)
	}
	return result, nil
}

func (builder *builder) finalizeImages(node mutationir.NodeInput) (mutationir.NodeInput, error) {
	before, after, err := mutationplan.DeriveNodeImages(mutationplan.NodeImageRequest{Registry: builder.request.Registry, Node: node})
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeIR, golem.ModelID(node.Model), golem.FieldID{}, "nested image requirements are invalid", err)
	}
	node.Before, node.After = before, after
	return node, nil
}

func (builder *builder) classify(condition policyir.Condition, use classify.UseKind, operation mutationir.Operation, endpoint schema.RelationEndpoint) error {
	if err := builder.classifyOne(condition, use, operation, actionFor(operation), endpoint); err != nil {
		return err
	}
	return builder.classifyRelatedChildren(condition, operation, endpoint)
}

func (builder *builder) classifyOne(condition policyir.Condition, use classify.UseKind, operation mutationir.Operation, selectingAction policyir.Action, endpoint schema.RelationEndpoint) error {
	fields := directFields(condition)
	builder.audits = append(builder.audits, PositionAudit{parent: policyir.ModelID(endpoint.ModelID()), field: policyir.FieldID(endpoint.FieldID()), model: condition.ModelID(), action: operation, use: use, fields: fields})
	if builder.request.Stance == mutationir.System || len(fields) == 0 {
		return nil
	}
	policy, ok := builder.request.Policies.Policy(condition.ModelID())
	if !ok {
		return fail(CodePolicy, golem.ModelID(condition.ModelID()), endpoint.FieldID(), "position model policy is absent", nil)
	}
	row, err := resolve.RowConstraint(policy, selectingAction, condition.ModelID())
	if err != nil {
		return fail(CodePolicy, golem.ModelID(condition.ModelID()), endpoint.FieldID(), "position action constraint could not resolve", err)
	}
	complete, err := conjoin(condition.ModelID(), row, condition)
	if err != nil {
		return fail(CodeClassification, golem.ModelID(condition.ModelID()), endpoint.FieldID(), "position constraint could not combine", err)
	}
	request, err := classify.NewRequestWithConstraint(builder.request.Registry, policy, condition.ModelID(), fields, use, selectingAction, complete)
	if err != nil {
		return fail(CodeClassification, golem.ModelID(condition.ModelID()), endpoint.FieldID(), "position classification request is invalid", err)
	}
	classifier := builder.request.Classifier
	if classifier == nil {
		classifier = defaultClassifier{}
	}
	result, err := classifier.Fields(request)
	if err != nil {
		return fail(CodeClassification, golem.ModelID(condition.ModelID()), endpoint.FieldID(), "position classification failed", err)
	}
	for _, field := range fields {
		value, present := result.Field(field)
		if !present || value.Access() == classify.AccessNever || value.Access() == classify.AccessConditional && !value.DischargedByConstraint() {
			return fail(CodeClassification, golem.ModelID(condition.ModelID()), golem.FieldID(field), "nested position is not readable over its complete selecting reach", nil)
		}
	}
	return nil
}

func (builder *builder) classifyRelatedChildren(condition policyir.Condition, operation mutationir.Operation, endpoint schema.RelationEndpoint) error {
	switch condition.Kind() {
	case policyir.ConditionLogical:
		_, children, _ := condition.Logical()
		for _, child := range children {
			if err := builder.classifyRelatedChildren(child, operation, endpoint); err != nil {
				return err
			}
		}
	case policyir.ConditionRelation:
		_, _, _, _, child, _ := condition.Relation()
		if child != nil {
			if err := builder.classifyOne(*child, classify.UseFilter, operation, policyir.ActionRead, endpoint); err != nil {
				return err
			}
			return builder.classifyRelatedChildren(*child, operation, endpoint)
		}
	}
	return nil
}

func completeTargetCondition(bound mutationbind.BoundTarget) policyir.Condition {
	selector := bound.SelectorPredicate()
	target := bound.Target()
	if guard, ok := target.Guard(); ok {
		combined, err := conjoin(selector.ModelID(), selector, guard)
		if err == nil {
			return combined
		}
	}
	return selector
}

func directFields(condition policyir.Condition) []policyir.FieldID {
	seen := map[policyir.FieldID]struct{}{}
	var result []policyir.FieldID
	var walk func(policyir.Condition)
	walk = func(current policyir.Condition) {
		switch current.Kind() {
		case policyir.ConditionLogical:
			_, children, _ := current.Logical()
			for _, child := range children {
				walk(child)
			}
		case policyir.ConditionScalar, policyir.ConditionList, policyir.ConditionJSON:
			field, _ := current.Field()
			if _, ok := seen[field]; !ok {
				seen[field] = struct{}{}
				result = append(result, field)
			}
		case policyir.ConditionRelation:
			field, _, _, _, _, _ := current.Relation()
			if _, ok := seen[field]; !ok {
				seen[field] = struct{}{}
				result = append(result, field)
			}
		}
	}
	walk(condition)
	return result
}

func conjoin(model policyir.ModelID, conditions ...policyir.Condition) (policyir.Condition, error) {
	if len(conditions) == 1 {
		return normalize.Condition(conditions[0])
	}
	value, err := policyir.NewLogical(model, policyir.LogicalAnd, conditions)
	if err != nil {
		return policyir.Condition{}, err
	}
	return normalize.Condition(value)
}

func actionFor(operation mutationir.Operation) policyir.Action {
	if operation == mutationir.Create {
		return policyir.ActionCreate
	}
	if operation == mutationir.Delete || operation == mutationir.DeleteMany {
		return policyir.ActionDelete
	}
	return policyir.ActionUpdate
}
func existing(operation mutationir.Operation) bool {
	switch operation {
	case mutationir.Create, mutationir.CreateMany:
		return false
	default:
		return true
	}
}
