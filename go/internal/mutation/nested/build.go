package nested

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/classify"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

func Build(request Request) (Result, error) {
	if request.Registry == nil || request.Root.Model == (policyir.ModelID{}) || !request.Registry.HasModel(golem.ModelID(request.Root.Model)) {
		return Result{}, fail(CodeInput, golem.ModelID(request.Root.Model), golem.FieldID{}, "root model or active registry is absent", nil)
	}
	if request.Stance != mutationir.Caller && request.Stance != mutationir.System {
		return Result{}, fail(CodeInput, golem.ModelID(request.Root.Model), golem.FieldID{}, "stance is invalid", nil)
	}
	if request.MaxDepth == 0 || request.MaxRows == 0 {
		return Result{}, fail(CodeInput, golem.ModelID(request.Root.Model), golem.FieldID{}, "positive depth and row bounds are required", nil)
	}
	if request.Stance == mutationir.Caller && (request.Policies == nil || request.Policies.GenerationDigest() != request.Registry.GenerationDigest()) {
		return Result{}, fail(CodePolicy, golem.ModelID(request.Root.Model), golem.FieldID{}, "caller policy set is absent or stale", nil)
	}
	authored := make(map[policyir.FieldID]struct{}, len(request.Root.ScalarOperations))
	for _, operation := range request.Root.ScalarOperations {
		if !operation.RuntimeOwned() {
			authored[operation.FieldID()] = struct{}{}
		}
	}
	if err := validateRelationCorrelationOwnership(request.Registry, request.Root.Model, authored, request.Mutations); err != nil {
		return Result{}, err
	}
	builder := builder{request: request, nextSource: request.SourceOffset}
	children, err := builder.buildRelations(request.Root.Model, request.Root.Operation, request.Mutations, 1)
	if err != nil {
		return Result{}, err
	}
	root := request.Root
	root.Children = append(append([]mutationir.NodeInput(nil), root.Children...), children...)
	if request.RuntimeValues != nil {
		root, err = resolveRuntimeValues(root, request.RuntimeValues)
		if err != nil {
			return Result{}, fail(CodeBinding, golem.ModelID(root.Model), golem.FieldID{}, "runtime-owned values could not be resolved", err)
		}
	}
	graph, err := mutationir.NewGraph(root)
	if err != nil {
		return Result{}, fail(CodeIR, golem.ModelID(root.Model), golem.FieldID{}, "nested graph is invalid", err)
	}
	return Result{graph: graph, audits: builder.audits, sources: builder.sources, sourceUpperBound: builder.nextSource}, nil
}

func validateRelationCorrelationOwnership(registry *schema.Registry, parent policyir.ModelID, authored map[policyir.FieldID]struct{}, mutations []golem.FrozenNestedMutation) error {
	toOne := make(map[policyir.FieldID]struct{})
	relationOwned := make(map[policyir.FieldID]policyir.FieldID)
	for _, mutation := range mutations {
		if policyir.ModelID(mutation.ParentModelID()) != parent {
			return fail(CodeRelation, mutation.ParentModelID(), mutation.FieldID(), "nested parent does not match its containing input", nil)
		}
		endpoint, ok := registry.RelationEndpoint(mutation.ParentModelID(), mutation.FieldID(), mutation.RelationID())
		if !ok {
			return fail(CodeRelation, mutation.ParentModelID(), mutation.FieldID(), "relation endpoint identities do not match the active registry", nil)
		}
		fieldID := policyir.FieldID(mutation.FieldID())
		if endpoint.Kind() != compilerir.RelationHasMany {
			if _, duplicate := toOne[fieldID]; duplicate {
				return fail(CodeShape, mutation.ParentModelID(), mutation.FieldID(), "to-one relation accepts exactly one explicit nested mutation value", nil)
			}
			toOne[fieldID] = struct{}{}
		}
		if endpoint.Role() == compilerir.RelationSource && relationActionOwnsCorrelation(mutation.Action()) {
			for _, pair := range endpoint.Correlation() {
				owned := policyir.FieldID(pair.ParentFieldID())
				if _, overlap := authored[owned]; overlap {
					return fail(CodeShape, mutation.ParentModelID(), mutation.FieldID(), "authored scalar operation overlaps a relation-owned correlation field", nil)
				}
				if previous, overlap := relationOwned[owned]; overlap && previous != fieldID {
					return fail(CodeShape, mutation.ParentModelID(), mutation.FieldID(), "sibling relations overlap the same owned correlation field", nil)
				}
				relationOwned[owned] = fieldID
			}
		}
		for _, branch := range mutation.Branches() {
			input, present := branch.Input()
			if !present || len(input.Relations()) == 0 {
				continue
			}
			childAuthored := make(map[policyir.FieldID]struct{}, len(input.Fields()))
			for _, field := range input.Fields() {
				childAuthored[policyir.FieldID(field.FieldID())] = struct{}{}
			}
			if err := validateRelationCorrelationOwnership(registry, policyir.ModelID(input.ModelID()), childAuthored, input.Relations()); err != nil {
				return err
			}
		}
	}
	return nil
}

func relationActionOwnsCorrelation(action golem.MutationRelationAction) bool {
	switch action {
	case golem.MutationRelationCreate, golem.MutationRelationCreateMany, golem.MutationRelationConnect,
		golem.MutationRelationConnectOrCreate, golem.MutationRelationDisconnect, golem.MutationRelationSet,
		golem.MutationRelationUpsert, golem.MutationRelationDelete:
		return true
	default:
		return false
	}
}

func resolveRuntimeValues(node mutationir.NodeInput, resolve func(mutationir.NodeInput) (mutationir.NodeInput, error)) (mutationir.NodeInput, error) {
	node.Children = append([]mutationir.NodeInput(nil), node.Children...)
	for index := range node.Children {
		child, err := resolveRuntimeValues(node.Children[index], resolve)
		if err != nil {
			return mutationir.NodeInput{}, err
		}
		node.Children[index] = child
	}
	return resolve(node)
}

type builder struct {
	request    Request
	audits     []PositionAudit
	sources    map[uint32]HookSource
	nextSource uint32
}

func (builder *builder) retainSource(node mutationir.NodeInput, endpoint schema.RelationEndpoint, action golem.MutationRelationAction, branch golem.FrozenNestedMutationBranch) mutationir.NodeInput {
	if builder.sources == nil {
		builder.sources = make(map[uint32]HookSource)
	}
	builder.nextSource++
	node.RuntimeSource = builder.nextSource
	builder.sources[builder.nextSource] = HookSource{
		parent: endpoint.ModelID(), field: endpoint.FieldID(), relation: endpoint.RelationID(),
		target: endpoint.TargetModelID(), action: action, branch: branch,
		hasBranch: branch.ModelID() != (golem.ModelID{}),
	}
	return node
}

func (builder *builder) buildRelations(parent policyir.ModelID, parentOperation mutationir.Operation, mutations []golem.FrozenNestedMutation, depth uint16) ([]mutationir.NodeInput, error) {
	if len(mutations) == 0 {
		return nil, nil
	}
	if depth > builder.request.MaxDepth {
		return nil, fail(CodeDepth, golem.ModelID(parent), golem.FieldID{}, "nested mutation depth exceeds configured bound", nil)
	}
	ordered := append([]golem.FrozenNestedMutation(nil), mutations...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i], ordered[j]
		leftField, rightField := left.FieldID(), right.FieldID()
		if leftField != rightField {
			return bytes.Compare(leftField[:], rightField[:]) < 0
		}
		return left.Action() < right.Action()
	})
	var result []mutationir.NodeInput
	for _, frozen := range ordered {
		nodes, err := builder.buildRelation(parent, parentOperation, frozen, depth)
		if err != nil {
			return nil, err
		}
		result = append(result, nodes...)
	}
	return result, nil
}

func (builder *builder) buildRelation(parent policyir.ModelID, parentOperation mutationir.Operation, frozen golem.FrozenNestedMutation, depth uint16) ([]mutationir.NodeInput, error) {
	if policyir.ModelID(frozen.ParentModelID()) != parent {
		return nil, fail(CodeRelation, frozen.ParentModelID(), frozen.FieldID(), "nested parent does not match its containing input", nil)
	}
	field, ok := builder.request.Registry.Field(frozen.ParentModelID(), frozen.FieldID())
	if !ok || field.Kind() != compilerir.FieldRelation {
		return nil, fail(CodeRelation, frozen.ParentModelID(), frozen.FieldID(), "relation field is absent or not relational", nil)
	}
	endpoint, ok := builder.request.Registry.RelationEndpoint(frozen.ParentModelID(), frozen.FieldID(), frozen.RelationID())
	if !ok || endpoint.TargetModelID() != frozen.TargetModelID() {
		return nil, fail(CodeRelation, frozen.ParentModelID(), frozen.FieldID(), "relation endpoint identities do not match the active registry", nil)
	}
	if reason := relationExposure(field, parentOperation); reason != "" {
		return nil, fail(CodeExposure, frozen.ParentModelID(), frozen.FieldID(), reason, nil)
	}
	many := endpoint.Kind() == compilerir.RelationHasMany
	required, err := requiredToOne(builder.request.Registry, endpoint)
	if err != nil {
		return nil, fail(CodeRelation, frozen.ParentModelID(), frozen.FieldID(), "relation requiredness is invalid", err)
	}
	branches := frozen.Branches()
	if err := validatePublicShape(frozen.Action(), branches, many, required, endpoint.Role() == compilerir.RelationSource); err != nil {
		return nil, fail(CodeShape, frozen.ParentModelID(), frozen.FieldID(), "nested operation shape is invalid", err)
	}
	switch frozen.Action() {
	case golem.MutationRelationCreate:
		result := make([]mutationir.NodeInput, len(branches))
		for index, branch := range branches {
			result[index], err = builder.createNode(endpoint, branch, mutationir.MainBranch, depth, parentOperation == mutationir.Create && endpoint.Role() == compilerir.RelationSource)
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	case golem.MutationRelationCreateMany:
		if uint32(len(branches)) > builder.request.MaxRows {
			return nil, fail(CodeShape, frozen.ParentModelID(), frozen.FieldID(), "nested create-many exceeds configured row bound", nil)
		}
		position, positionErr := mutationir.NewRelationPosition(mutationir.RelationPositionInput{ParentModel: parent, Field: policyir.FieldID(frozen.FieldID()), Relation: policyir.RelationID(frozen.RelationID()), TargetModel: policyir.ModelID(frozen.TargetModelID()), Kind: mutationir.PositionEndpoint})
		if positionErr != nil {
			return nil, fail(CodeIR, frozen.ParentModelID(), frozen.FieldID(), "create-many relation position is invalid", positionErr)
		}
		wrapper := builder.baseNode(mutationir.CreateMany, policyir.ModelID(frozen.TargetModelID()), frozen.RelationID(), &position, mutationir.MainBranch)
		for _, branch := range branches {
			child, buildErr := builder.createNode(endpoint, branch, mutationir.BatchItemBranch, depth, false)
			if buildErr != nil {
				return nil, buildErr
			}
			child.Relation = policyir.RelationID{}
			wrapper.Children = append(wrapper.Children, child)
		}
		return []mutationir.NodeInput{wrapper}, nil
	case golem.MutationRelationConnect, golem.MutationRelationDisconnect:
		result := make([]mutationir.NodeInput, len(branches))
		for index, branch := range branches {
			if parentOperation == mutationir.Create && endpoint.Role() == compilerir.RelationSource && frozen.Action() == golem.MutationRelationConnect {
				result[index], err = builder.sourceDependencyProbe(endpoint, branch)
			} else {
				result[index], err = builder.targetMembershipNode(endpoint, frozen.Action(), branch, depth)
			}
			if err != nil {
				return nil, err
			}
		}
		return result, nil
	case golem.MutationRelationSet:
		var desired []mutationir.Target
		for _, branch := range branches {
			if target, ok := branch.Target(); ok {
				bound, bindErr := mutationbind.Target(target, frozen.TargetModelID(), builder.request.Registry)
				if bindErr != nil {
					return nil, fail(CodeBinding, frozen.TargetModelID(), frozen.FieldID(), "set target did not bind", bindErr)
				}
				desired = append(desired, bound.Target())
				if err := builder.classify(completeTargetCondition(bound), classify.UseSelector, mutationir.SetRelation, endpoint); err != nil {
					return nil, err
				}
			}
		}
		expansion, expansionErr := mutationir.NewExpansionRequirement(mutationir.ExpandSetDifference, builder.request.MaxRows)
		if expansionErr != nil {
			return nil, fail(CodeIR, frozen.ParentModelID(), frozen.FieldID(), "set expansion is invalid", expansionErr)
		}
		position, positionErr := mutationir.NewRelationPosition(mutationir.RelationPositionInput{ParentModel: parent, Field: policyir.FieldID(frozen.FieldID()), Relation: policyir.RelationID(frozen.RelationID()), TargetModel: policyir.ModelID(frozen.TargetModelID()), Kind: mutationir.PositionSetDifference, Desired: desired, Expansion: &expansion})
		if positionErr != nil {
			return nil, fail(CodeIR, frozen.ParentModelID(), frozen.FieldID(), "set position is invalid", positionErr)
		}
		owner := policyir.ModelID(frozen.TargetModelID())
		if endpoint.Role() == compilerir.RelationSource {
			owner = policyir.ModelID(frozen.ParentModelID())
		}
		node, decorateErr := builder.decorateMembership(builder.baseNode(mutationir.SetRelation, owner, frozen.RelationID(), &position, mutationir.MainBranch), nil, endpoint)
		if decorateErr != nil {
			return nil, decorateErr
		}
		return []mutationir.NodeInput{builder.retainSource(node, endpoint, frozen.Action(), branches[0])}, nil
	case golem.MutationRelationUpdate:
		node, buildErr := builder.updateNode(endpoint, branches[0], mutationir.MainBranch, true, depth)
		if buildErr != nil {
			return nil, buildErr
		}
		return []mutationir.NodeInput{node}, nil
	case golem.MutationRelationUpdateMany:
		node, buildErr := builder.updateManyNode(endpoint, branches[0], depth)
		if buildErr != nil {
			return nil, buildErr
		}
		return []mutationir.NodeInput{node}, nil
	case golem.MutationRelationDelete:
		if !many && endpoint.Role() == compilerir.RelationSource {
			// Optional source to-one Delete has two distinct authorized effects:
			// first clear the FK-owning parent (with update hooks/fact), then
			// delete the formerly related target. Required source relations were
			// rejected by shape validation; inverse has-one deletes the owning
			// child directly and needs no disconnect.
			disconnect, disconnectErr := builder.membershipNode(endpoint, golem.MutationRelationDisconnect, mutationir.PositionCurrentToOne, nil)
			if disconnectErr != nil {
				return nil, disconnectErr
			}
			disconnect = builder.retainSource(disconnect, endpoint, golem.MutationRelationDisconnect, branches[0])
			node, buildErr := builder.deleteNode(endpoint, branches[0], mutationir.MainBranch)
			if buildErr != nil {
				return nil, buildErr
			}
			return []mutationir.NodeInput{disconnect, node}, nil
		}
		node, buildErr := builder.deleteNode(endpoint, branches[0], mutationir.MainBranch)
		if buildErr != nil {
			return nil, buildErr
		}
		return []mutationir.NodeInput{node}, nil
	case golem.MutationRelationDeleteMany:
		node, buildErr := builder.deleteManyNode(endpoint, branches[0])
		if buildErr != nil {
			return nil, buildErr
		}
		return []mutationir.NodeInput{node}, nil
	case golem.MutationRelationConnectOrCreate:
		return builder.connectOrCreateNode(endpoint, branches, depth, parentOperation == mutationir.Create && endpoint.Role() == compilerir.RelationSource)
	case golem.MutationRelationUpsert:
		return builder.upsertNode(endpoint, branches, depth)
	default:
		return nil, fail(CodeShape, frozen.ParentModelID(), frozen.FieldID(), "unknown nested action", nil)
	}
}

func (builder *builder) sourceDependencyProbe(endpoint schema.RelationEndpoint, branch golem.FrozenNestedMutationBranch) (mutationir.NodeInput, error) {
	public, ok := branch.Target()
	if !ok {
		return mutationir.NodeInput{}, fail(CodeShape, endpoint.ModelID(), endpoint.FieldID(), "source dependency connect target is absent", nil)
	}
	bound, err := mutationbind.Target(public, endpoint.TargetModelID(), builder.request.Registry)
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeBinding, endpoint.TargetModelID(), endpoint.FieldID(), "source dependency target did not bind", err)
	}
	condition := completeTargetCondition(bound)
	if err := builder.classify(condition, classify.UseSelector, mutationir.Connect, endpoint); err != nil {
		return mutationir.NodeInput{}, err
	}
	target := bound.Target()
	position, err := mutationir.NewRelationPosition(mutationir.RelationPositionInput{
		ParentModel: policyir.ModelID(endpoint.ModelID()), Field: policyir.FieldID(endpoint.FieldID()), Relation: policyir.RelationID(endpoint.RelationID()),
		TargetModel: policyir.ModelID(endpoint.TargetModelID()), Kind: mutationir.PositionRelatedTarget, Target: &target,
	})
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeIR, endpoint.ModelID(), endpoint.FieldID(), "source dependency position is invalid", err)
	}
	node, err := builder.decorate(builder.baseNode(mutationir.BranchProbe, policyir.ModelID(endpoint.TargetModelID()), endpoint.RelationID(), &position, mutationir.MainBranch), &condition)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	node.BeforeParent = true
	return builder.retainSource(node, endpoint, branch.Action(), branch), nil
}

func (builder *builder) baseNode(operation mutationir.Operation, model policyir.ModelID, relation golem.RelationID, position *mutationir.RelationPosition, branch mutationir.Branch) mutationir.NodeInput {
	identity := mutationir.IdentityUnchanged
	if operation == mutationir.Create || operation == mutationir.CreateMany {
		identity = mutationir.IdentityProduced
	}
	if operation == mutationir.UpdateMany || operation == mutationir.DeleteMany {
		identity = mutationir.IdentityBatchChangeRefused
	}
	return mutationir.NodeInput{Operation: operation, Model: model, Relation: policyir.RelationID(relation), RelationPosition: position, Branch: branch, Identity: identity}
}

func (builder *builder) createNode(endpoint schema.RelationEndpoint, branch golem.FrozenNestedMutationBranch, branchKind mutationir.Branch, depth uint16, sourceDependency bool) (mutationir.NodeInput, error) {
	input, ok := branch.Input()
	if !ok {
		return mutationir.NodeInput{}, fail(CodeShape, branch.ModelID(), endpoint.FieldID(), "create branch has no input", nil)
	}
	runtimeFields, runtimeFieldsErr := runtimeOwnedNestedCreateFields(input, endpoint, builder.request.Registry)
	if runtimeFieldsErr != nil {
		return mutationir.NodeInput{}, fail(CodeRelation, branch.ModelID(), endpoint.FieldID(), "nested create runtime-owned fields could not be resolved", runtimeFieldsErr)
	}
	bound, runtimeInventory, err := mutationbind.CreateInputWithRuntimeOwnedFields(input, builder.request.Registry, runtimeFields)
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeBinding, branch.ModelID(), endpoint.FieldID(), "nested create input did not bind", err)
	}
	position, positionErr := mutationir.NewRelationPosition(mutationir.RelationPositionInput{ParentModel: policyir.ModelID(endpoint.ModelID()), Field: policyir.FieldID(endpoint.FieldID()), Relation: policyir.RelationID(endpoint.RelationID()), TargetModel: policyir.ModelID(endpoint.TargetModelID()), Kind: mutationir.PositionEndpoint})
	if positionErr != nil {
		return mutationir.NodeInput{}, fail(CodeIR, endpoint.ModelID(), endpoint.FieldID(), "create relation position is invalid", positionErr)
	}
	node := builder.baseNode(mutationir.Create, policyir.ModelID(branch.ModelID()), endpoint.RelationID(), &position, branchKind)
	node.ScalarOperations = bound.Operations()
	children, childErr := builder.buildRelations(policyir.ModelID(branch.ModelID()), mutationir.Create, input.Relations(), depth+1)
	if childErr != nil {
		return mutationir.NodeInput{}, childErr
	}
	node.Children = children
	decorated, decorateErr := builder.decorate(node, nil)
	if decorateErr != nil {
		return mutationir.NodeInput{}, decorateErr
	}
	decorated, decorateErr = builder.decorateOwnedFields(decorated, runtimeInventory, policyir.ActionCreate, endpoint.FieldID())
	if decorateErr != nil {
		return mutationir.NodeInput{}, decorateErr
	}
	if endpoint.Role() == compilerir.RelationSource && sourceDependency {
		// The target row is a dependency of the FK-owning parent. Execution
		// produces it first and injects its correlation tuple into the parent
		// scalar write, avoiding a transient NULL/invalid required FK.
		decorated.BeforeParent = true
	} else if endpoint.Role() == compilerir.RelationSource && (branchKind == mutationir.MainBranch || branchKind == mutationir.BatchItemBranch || branchKind == mutationir.UpsertCreateBranch) {
		position, positionErr := mutationir.NewRelationPosition(mutationir.RelationPositionInput{
			ParentModel: policyir.ModelID(endpoint.ModelID()), Field: policyir.FieldID(endpoint.FieldID()), Relation: policyir.RelationID(endpoint.RelationID()),
			TargetModel: policyir.ModelID(endpoint.TargetModelID()), Kind: mutationir.PositionBranchResult,
		})
		if positionErr != nil {
			return mutationir.NodeInput{}, fail(CodeIR, endpoint.ModelID(), endpoint.FieldID(), "source create owner effect position is invalid", positionErr)
		}
		effect := builder.baseNode(mutationir.Connect, policyir.ModelID(endpoint.ModelID()), endpoint.RelationID(), &position, mutationir.MainBranch)
		effect, decorateErr = builder.decorateMembership(effect, nil, endpoint)
		if decorateErr != nil {
			return mutationir.NodeInput{}, decorateErr
		}
		// A branch-result owner effect completes the containing row mutation;
		// it is not a second logical owner mutation. Keep its update policy and
		// field authorization, but do not emit duplicate owner lifecycle work
		// for the physical FK assignment.
		effect.Hooks = nil
		effect.Fact = mutationir.FactRequirement{}
		effect = builder.retainSource(effect, endpoint, golem.MutationRelationConnect, branch)
		decorated.Children = append(decorated.Children, effect)
	}
	return builder.retainSource(decorated, endpoint, branch.Action(), branch), nil
}

func (builder *builder) targetMembershipNode(endpoint schema.RelationEndpoint, action golem.MutationRelationAction, branch golem.FrozenNestedMutationBranch, depth uint16) (mutationir.NodeInput, error) {
	target, ok := branch.Target()
	if !ok {
		kind := mutationir.PositionCurrentToOne
		if endpoint.Role() == compilerir.RelationSource {
			return builder.sourceMembershipNode(endpoint, action, branch, kind, nil)
		}
		node, err := builder.membershipNode(endpoint, action, kind, nil)
		if err != nil {
			return mutationir.NodeInput{}, err
		}
		return builder.retainSource(node, endpoint, action, branch), nil
	}
	bound, err := mutationbind.Target(target, endpoint.TargetModelID(), builder.request.Registry)
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeBinding, endpoint.TargetModelID(), endpoint.FieldID(), "nested membership target did not bind", err)
	}
	if err := builder.classify(completeTargetCondition(bound), classify.UseSelector, operationFor(action), endpoint); err != nil {
		return mutationir.NodeInput{}, err
	}
	if endpoint.Role() == compilerir.RelationSource {
		return builder.sourceMembershipNode(endpoint, action, branch, mutationir.PositionRelatedTarget, &bound)
	}
	node, err := builder.membershipNode(endpoint, action, mutationir.PositionRelatedTarget, &bound)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	return builder.retainSource(node, endpoint, action, branch), nil
}

// sourceMembershipNode keeps the two independent P4 authorization subjects
// explicit in the graph. The probe selects the existing related target under
// that target model's update reach. Only an authorized probe result can reach
// the child effect, which separately updates the actual FK-owning parent under
// the parent's update reach and correlation-field grants.
func (builder *builder) sourceMembershipNode(endpoint schema.RelationEndpoint, action golem.MutationRelationAction, branch golem.FrozenNestedMutationBranch, kind mutationir.RelationPositionKind, bound *mutationbind.BoundTarget) (mutationir.NodeInput, error) {
	probePosition, err := builder.membershipPosition(endpoint, kind, bound)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	probe, err := builder.decorate(builder.baseNode(mutationir.BranchProbe, policyir.ModelID(endpoint.TargetModelID()), endpoint.RelationID(), &probePosition, mutationir.MainBranch), conditionOf(bound))
	if err != nil {
		return mutationir.NodeInput{}, err
	}

	branchPosition, err := builder.membershipPosition(endpoint, mutationir.PositionBranchResult, nil)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	operation := mutationir.Connect
	if action == golem.MutationRelationDisconnect {
		operation = mutationir.Disconnect
	}
	owner := builder.baseNode(operation, policyir.ModelID(endpoint.ModelID()), endpoint.RelationID(), &branchPosition, mutationir.MainBranch)
	owner, err = builder.decorateMembership(owner, nil, endpoint)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	probe.Children = append(probe.Children, builder.retainSource(owner, endpoint, action, branch))
	return probe, nil
}

func (builder *builder) membershipNode(endpoint schema.RelationEndpoint, action golem.MutationRelationAction, kind mutationir.RelationPositionKind, bound *mutationbind.BoundTarget) (mutationir.NodeInput, error) {
	position, err := builder.membershipPosition(endpoint, kind, bound)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	operation := mutationir.Connect
	if action == golem.MutationRelationDisconnect {
		operation = mutationir.Disconnect
	}
	owner := policyir.ModelID(endpoint.TargetModelID())
	condition := conditionOf(bound)
	if endpoint.Role() == compilerir.RelationSource {
		owner, condition = policyir.ModelID(endpoint.ModelID()), nil
	}
	node := builder.baseNode(operation, owner, endpoint.RelationID(), &position, mutationir.MainBranch)
	return builder.decorateMembership(node, condition, endpoint)
}

func (builder *builder) membershipPosition(endpoint schema.RelationEndpoint, kind mutationir.RelationPositionKind, bound *mutationbind.BoundTarget) (mutationir.RelationPosition, error) {
	input := mutationir.RelationPositionInput{ParentModel: policyir.ModelID(endpoint.ModelID()), Field: policyir.FieldID(endpoint.FieldID()), Relation: policyir.RelationID(endpoint.RelationID()), TargetModel: policyir.ModelID(endpoint.TargetModelID()), Kind: kind}
	if bound != nil {
		target := bound.Target()
		input.Target = &target
	}
	if kind == mutationir.PositionCurrentToOne {
		expansion, err := mutationir.NewExpansionRequirement(mutationir.ExpandCurrentToOne, 1)
		if err != nil {
			return mutationir.RelationPosition{}, fail(CodeIR, endpoint.ModelID(), endpoint.FieldID(), "current relation expansion is invalid", err)
		}
		input.Expansion = &expansion
	}
	if kind == mutationir.PositionEntireMembership {
		expansion, err := mutationir.NewExpansionRequirement(mutationir.ExpandEntireMembership, builder.request.MaxRows)
		if err != nil {
			return mutationir.RelationPosition{}, fail(CodeIR, endpoint.ModelID(), endpoint.FieldID(), "membership expansion is invalid", err)
		}
		input.Expansion = &expansion
	}
	position, err := mutationir.NewRelationPosition(input)
	if err != nil {
		return mutationir.RelationPosition{}, fail(CodeIR, endpoint.ModelID(), endpoint.FieldID(), "membership relation position is invalid", err)
	}
	return position, nil
}

func conditionOf(bound *mutationbind.BoundTarget) *policyir.Condition {
	if bound == nil {
		return nil
	}
	value := completeTargetCondition(*bound)
	return &value
}

func operationFor(action golem.MutationRelationAction) mutationir.Operation {
	return mutationir.Operation(action)
}

func relationExposure(field schema.Field, parentOperation mutationir.Operation) string {
	for _, mode := range field.Modes() {
		if mode == compilerir.ModeHidden {
			return "hidden relation is not writable"
		}
		if mode == compilerir.ModeReadOnly {
			return "read-only relation is not writable"
		}
		if mode == compilerir.ModeImmutable && parentOperation != mutationir.Create {
			return "immutable relation is writable only during create"
		}
	}
	return ""
}

func requiredToOne(registry *schema.Registry, endpoint schema.RelationEndpoint) (bool, error) {
	if endpoint.Kind() == compilerir.RelationHasMany {
		return false, nil
	}
	if len(endpoint.Correlation()) == 0 {
		return true, nil
	}
	required := true
	for _, pair := range endpoint.Correlation() {
		model, fieldID := endpoint.ModelID(), pair.ParentFieldID()
		if endpoint.Role() == compilerir.RelationInverse {
			model, fieldID = endpoint.TargetModelID(), pair.ChildFieldID()
		}
		field, ok := registry.Field(model, fieldID)
		if !ok || field.Kind() == compilerir.FieldRelation {
			return false, fmt.Errorf("correlation parent field is absent or relational")
		}
		if field.Nullable() {
			required = false
		}
	}
	return required, nil
}

func runtimeOwnedChildFields(endpoint schema.RelationEndpoint) []golem.FieldID {
	if endpoint.Role() != compilerir.RelationInverse {
		return nil
	}
	result := make([]golem.FieldID, len(endpoint.Correlation()))
	for index, pair := range endpoint.Correlation() {
		result[index] = pair.ChildFieldID()
	}
	return result
}

// runtimeOwnedNestedCreateFields closes the full inventory of required create
// columns supplied by relation execution rather than authored scalar input.
// It includes both the containing inverse endpoint (whose persisted parent
// supplies child correlation values) and source relations declared inside the
// child input (whose selected/created dependencies supply local FK values).
// Computing this before CreateInput binding is essential for runtime hook
// replacements: a replacement may introduce a new required source relation
// while correctly removing the now relation-owned scalar FK.
func runtimeOwnedNestedCreateFields(input golem.FrozenMutationInput, containing schema.RelationEndpoint, registry *schema.Registry) ([]golem.FieldID, error) {
	fields := runtimeOwnedChildFields(containing)
	seen := make(map[golem.FieldID]struct{}, len(fields))
	for _, field := range fields {
		seen[field] = struct{}{}
	}
	for _, relation := range input.Relations() {
		endpoint, ok := registry.RelationEndpoint(input.ModelID(), relation.FieldID(), relation.RelationID())
		if !ok || endpoint.TargetModelID() != relation.TargetModelID() {
			return nil, fmt.Errorf("nested create source relation endpoint is absent or mismatched")
		}
		if endpoint.Role() != compilerir.RelationSource || !relationActionSuppliesCreateCorrelation(relation.Action()) {
			continue
		}
		for _, pair := range endpoint.Correlation() {
			field := pair.ParentFieldID()
			if _, duplicate := seen[field]; duplicate {
				continue
			}
			seen[field] = struct{}{}
			fields = append(fields, field)
		}
	}
	return fields, nil
}

func relationActionSuppliesCreateCorrelation(action golem.MutationRelationAction) bool {
	switch action {
	case golem.MutationRelationCreate, golem.MutationRelationConnect, golem.MutationRelationConnectOrCreate, golem.MutationRelationUpsert:
		return true
	default:
		return false
	}
}

func membershipOwnedFields(endpoint schema.RelationEndpoint) []policyir.FieldID {
	result := make([]policyir.FieldID, len(endpoint.Correlation()))
	for index, pair := range endpoint.Correlation() {
		if endpoint.Role() == compilerir.RelationSource {
			result[index] = policyir.FieldID(pair.ParentFieldID())
		} else {
			result[index] = policyir.FieldID(pair.ChildFieldID())
		}
	}
	return result
}

func validatePublicShape(action golem.MutationRelationAction, branches []golem.FrozenNestedMutationBranch, many, required, source bool) error {
	if len(branches) == 0 {
		return fmt.Errorf("operation has no branches")
	}
	switch action {
	case golem.MutationRelationCreate, golem.MutationRelationCreateMany, golem.MutationRelationConnect:
		if !many && len(branches) != 1 {
			return fmt.Errorf("to-one operation requires one branch")
		}
	case golem.MutationRelationConnectOrCreate:
		if len(branches) != 2 {
			return fmt.Errorf("connect-or-create requires two branches")
		}
	case golem.MutationRelationDisconnect:
		if required {
			return fmt.Errorf("required to-one relation cannot disconnect")
		}
		for _, branch := range branches {
			_, target := branch.Target()
			if target != many {
				return fmt.Errorf("disconnect selector presence does not match cardinality")
			}
		}
	case golem.MutationRelationSet, golem.MutationRelationUpdateMany, golem.MutationRelationDeleteMany:
		if !many || action != golem.MutationRelationSet && len(branches) != 1 {
			return fmt.Errorf("operation requires to-many relation and one branch")
		}
	case golem.MutationRelationUpdate, golem.MutationRelationDelete:
		if len(branches) != 1 {
			return fmt.Errorf("single-row operation requires one branch")
		}
		_, target := branches[0].Target()
		if target != many {
			return fmt.Errorf("selector presence does not match cardinality")
		}
		if action == golem.MutationRelationDelete && required && source {
			return fmt.Errorf("required source to-one relation cannot delete through optional-only shape")
		}
	case golem.MutationRelationUpsert:
		if len(branches) != 2 {
			return fmt.Errorf("upsert requires two branches")
		}
		_, target := branches[1].Target()
		if target != many {
			return fmt.Errorf("upsert selector presence does not match cardinality")
		}
	default:
		return fmt.Errorf("unknown action")
	}
	return nil
}
