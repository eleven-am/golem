package plan

import (
	"fmt"
	"sort"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/policy/classify"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/resolve"
)

// BuildRoot completes all binding-independent authorization planning before an
// execution boundary can be reached. Its output is a validated immutable IR
// plan; it never opens a transaction, renders SQL, or evaluates a row.
func BuildRoot(request RootRequest) (mutationir.Plan, error) {
	if err := validateRequest(request); err != nil {
		return mutationir.Plan{}, err
	}
	facts, err := deriveFactInventory(request.Registry, request.Model)
	if err != nil {
		return mutationir.Plan{}, fail(CodeRequirements, request, policyir.FieldID{}, "durable capture requirements could not be derived", err)
	}
	request.facts = facts

	var policy policyir.Policy
	if request.Stance == mutationir.Caller {
		var ok bool
		policy, ok = request.Policies.Policy(request.Model)
		if !ok {
			return mutationir.Plan{}, fail(CodePolicy, request, policyir.FieldID{}, "caller policy is absent for model", nil)
		}
	}

	root, scalar, err := buildRootNode(request, policy)
	if err != nil {
		return mutationir.Plan{}, err
	}
	graph, err := mutationir.NewGraph(root)
	if err != nil {
		return mutationir.Plan{}, fail(CodeIR, request, policyir.FieldID{}, "mutation graph validation failed", err)
	}
	providers, err := providerRequirements(request.Registry, request.Operation, scalar)
	if err != nil {
		return mutationir.Plan{}, fail(CodeRequirements, request, policyir.FieldID{}, "provider requirements could not be derived", err)
	}
	input := mutationir.PlanInput{
		Stance: request.Stance, Graph: graph, Result: request.Result,
		Providers: providers, Retry: request.Retry, Bounds: request.Bounds,
		SemanticIndexed: request.facts.semantic,
	}
	if request.facts.capture {
		codec := request.facts.codec
		input.FactCodec = &codec
	}
	result, err := mutationir.NewPlan(input)
	if err != nil {
		return mutationir.Plan{}, fail(CodeIR, request, policyir.FieldID{}, "mutation plan validation failed", err)
	}
	return result, nil
}

func validateRequest(request RootRequest) error {
	if request.Registry == nil || request.Model == (policyir.ModelID{}) || !request.Registry.HasModel(golem.ModelID(request.Model)) {
		return fail(CodeRequest, request, policyir.FieldID{}, "active model registry is absent or does not own model", nil)
	}
	if request.Operation != mutationir.Create && request.Operation != mutationir.Update && request.Operation != mutationir.Delete && request.Operation != mutationir.Upsert && request.Operation != mutationir.UpdateMany && request.Operation != mutationir.DeleteMany {
		return fail(CodeRequest, request, policyir.FieldID{}, "operation is not a root mutation", nil)
	}
	if request.Stance != mutationir.Caller && request.Stance != mutationir.System {
		return fail(CodeRequest, request, policyir.FieldID{}, "stance is invalid", nil)
	}
	if request.Bounds.MaxParameters() == 0 || request.Bounds.MaxRows() == 0 {
		return fail(CodeRequest, request, policyir.FieldID{}, "statement bounds are absent", nil)
	}
	if request.Result.ModelID() != (policyir.ModelID{}) && request.Result.ModelID() != request.Model {
		return fail(CodeRequest, request, policyir.FieldID{}, "result requirements belong to another model", nil)
	}
	if err := validateImageAgainstRegistry(request.Registry, request.Model, request.Result); err != nil {
		return fail(CodeRequest, request, policyir.FieldID{}, "result requirements do not match active schema", err)
	}
	if request.Stance == mutationir.Caller {
		if request.Policies == nil || request.Policies.GenerationDigest() != request.Registry.GenerationDigest() {
			return fail(CodePolicy, request, policyir.FieldID{}, "policy set is absent or belongs to another generation", nil)
		}
		providers, err := providerSet(request.Registry)
		if err != nil || !providers.Contains(request.Policies.Provider()) {
			return fail(CodePolicy, request, policyir.FieldID{}, "policy provider is not declared by schema", err)
		}
	}
	if request.Operation == mutationir.Upsert {
		if request.Retry != mutationir.EngineOwnedUpsertRetry && request.Retry != mutationir.CallerTransactionNoReplay {
			return fail(CodeRequest, request, policyir.FieldID{}, "upsert must declare retry ownership", nil)
		}
	} else if request.Retry != mutationir.NoRetry && request.Retry != mutationir.CallerTransactionNoReplay {
		return fail(CodeRequest, request, policyir.FieldID{}, "retry class is invalid for operation", nil)
	}
	if request.ConcurrencyPrecheck && (request.Stance != mutationir.Caller || request.Operation != mutationir.Update) {
		return fail(CodeRequest, request, policyir.FieldID{}, "concurrency precheck inventory is valid only for caller update", nil)
	}

	wantTarget := request.Operation == mutationir.Update || request.Operation == mutationir.Delete || request.Operation == mutationir.Upsert
	wantPredicate := request.Operation == mutationir.UpdateMany || request.Operation == mutationir.DeleteMany
	wantCreate := request.Operation == mutationir.Create || request.Operation == mutationir.Upsert
	wantUpdate := request.Operation == mutationir.Update || request.Operation == mutationir.UpdateMany || request.Operation == mutationir.Upsert
	if (request.Target != nil) != wantTarget || (request.Predicate != nil) != wantPredicate || (request.Create != nil) != wantCreate || (request.Update != nil) != wantUpdate {
		return fail(CodeRequest, request, policyir.FieldID{}, "operation target, predicate, or scalar input shape is invalid", nil)
	}
	if request.Target != nil && request.Target.Target().ModelID() != request.Model || request.Predicate != nil && request.Predicate.ModelID() != request.Model {
		return fail(CodeRequest, request, policyir.FieldID{}, "target or predicate belongs to another model", nil)
	}
	if request.Create != nil && (request.Create.ModelID() != request.Model || request.Create.Kind() != mutationbind.InputCreate) {
		return fail(CodeRequest, request, policyir.FieldID{}, "create input kind or model is invalid", nil)
	}
	if request.Update != nil {
		want := mutationbind.InputUpdate
		if request.Operation == mutationir.UpdateMany {
			want = mutationbind.InputUpdateMany
		}
		if request.Update.ModelID() != request.Model || request.Update.Kind() != want {
			return fail(CodeRequest, request, policyir.FieldID{}, "update input kind or model is invalid", nil)
		}
	}
	if request.Stance == mutationir.Caller {
		if err := refuseSystemOwnedWrites(request, request.Create); err != nil {
			return err
		}
		if err := refuseSystemOwnedWrites(request, request.Update); err != nil {
			return err
		}
	}
	return nil
}

func buildRootNode(request RootRequest, policy policyir.Policy) (mutationir.NodeInput, []mutationir.ScalarOperation, error) {
	switch request.Operation {
	case mutationir.Create:
		node, err := writeNode(request, policy, mutationir.Create, mutationir.MainBranch, request.Create.Operations(), nil, nil)
		return node, request.Create.Operations(), err
	case mutationir.Update:
		target := request.Target.Target()
		node, err := writeNode(request, policy, mutationir.Update, mutationir.MainBranch, request.Update.Operations(), &target, nil)
		return node, request.Update.Operations(), err
	case mutationir.Delete:
		target := request.Target.Target()
		node, err := writeNode(request, policy, mutationir.Delete, mutationir.MainBranch, nil, &target, nil)
		return node, nil, err
	case mutationir.UpdateMany:
		node, err := writeNode(request, policy, mutationir.UpdateMany, mutationir.MainBranch, request.Update.Operations(), nil, request.Predicate)
		return node, request.Update.Operations(), err
	case mutationir.DeleteMany:
		node, err := writeNode(request, policy, mutationir.DeleteMany, mutationir.MainBranch, nil, nil, request.Predicate)
		return node, nil, err
	case mutationir.Upsert:
		return upsertNode(request, policy)
	default:
		return mutationir.NodeInput{}, nil, fail(CodeRequest, request, policyir.FieldID{}, "unsupported root operation", nil)
	}
}

func upsertNode(request RootRequest, policy policyir.Policy) (mutationir.NodeInput, []mutationir.ScalarOperation, error) {
	target := request.Target.Target()
	var selection *mutationir.SelectionRequirement
	var influencing []policyir.FieldID
	var err error
	if request.Stance == mutationir.Caller {
		selection, influencing, err = selectExisting(request, policy, policyir.ActionUpdate)
		if err != nil {
			return mutationir.NodeInput{}, nil, err
		}
	} else {
		position, positionErr := targetPosition(request)
		if positionErr != nil {
			return mutationir.NodeInput{}, nil, positionErr
		}
		influencing, err = rootPositionFields(position)
		if err != nil {
			return mutationir.NodeInput{}, nil, fail(CodeRequest, request, policyir.FieldID{}, "relation-traversing mutation positions are not in the root-scalar planner", err)
		}
	}
	create, err := writeNode(request, policy, mutationir.Create, mutationir.UpsertCreateBranch, request.Create.Operations(), nil, nil)
	if err != nil {
		return mutationir.NodeInput{}, nil, err
	}
	update, err := writeNode(request, policy, mutationir.Update, mutationir.UpsertUpdateBranch, request.Update.Operations(), &target, nil)
	if err != nil {
		return mutationir.NodeInput{}, nil, err
	}
	return mutationir.NodeInput{
		Operation: mutationir.Upsert, Model: request.Model, Target: &target,
		InfluencingFields: influencing, Selection: selection,
		Identity: mutationir.IdentityUnchanged,
		Children: []mutationir.NodeInput{create, update},
	}, append(request.Create.Operations(), request.Update.Operations()...), nil
}

func writeNode(request RootRequest, policy policyir.Policy, operation mutationir.Operation, branch mutationir.Branch, scalar []mutationir.ScalarOperation, target *mutationir.Target, predicate *policyir.Condition) (mutationir.NodeInput, error) {
	node := mutationir.NodeInput{Operation: operation, Model: request.Model, Branch: branch, Target: target, Predicate: predicate, ScalarOperations: scalar}
	node.Identity = identityBehavior(request, operation, scalar)
	fields := scalarFields(scalar)
	authoredFields := authoredScalarFields(scalar)
	if request.ConcurrencyPrecheck && operation == mutationir.Update {
		authoredFields = mergeFields(authoredFields, concurrencyHookMutableFields(request))
	}
	if operation == mutationir.Create {
		authoredFields = mergeFields(authoredFields, request.AuthorizedRuntimeFields)
	}
	node.InfluencingFields = append([]policyir.FieldID(nil), fields...)

	var row policyir.Condition
	var selection *mutationir.SelectionRequirement
	if request.Stance == mutationir.Caller {
		action := actionFor(operation)
		var err error
		row, err = resolve.RowConstraint(policy, action, request.Model)
		if err != nil {
			return mutationir.NodeInput{}, fail(CodePolicy, request, policyir.FieldID{}, "row constraint could not be resolved", err)
		}
		if operation != mutationir.Create {
			selection, node.InfluencingFields, err = selectExisting(request, policy, action)
			if err != nil {
				return mutationir.NodeInput{}, err
			}
			node.InfluencingFields = mergeFields(node.InfluencingFields, fields)
			node.Selection = selection
		} else {
			node.RowPostcondition = &row
		}
		if operation == mutationir.Update || operation == mutationir.UpdateMany {
			node.RowPostcondition = &row
		}
		for _, field := range authoredFields {
			condition, err := resolve.FieldCondition(policy, action, request.Model, field)
			if err != nil {
				return mutationir.NodeInput{}, fail(CodePolicy, request, field, "field condition could not be resolved", err)
			}
			if operation == mutationir.Create && request.Operation == mutationir.Create {
				allowed, gateErr := resolve.ActionAllowed(condition)
				if gateErr != nil || !allowed {
					return mutationir.NodeInput{}, fail(CodePolicy, request, field, "create field has no grant", gateErr)
				}
			}
			authorization, err := mutationir.NewFieldAuthorization(field, condition)
			if err != nil {
				return mutationir.NodeInput{}, fail(CodeIR, request, field, "field authorization is invalid", err)
			}
			node.FieldConditions = append(node.FieldConditions, authorization)
		}
		if operation == mutationir.Create && request.Operation == mutationir.Create {
			allowed, gateErr := resolve.ActionAllowed(row)
			if gateErr != nil || !allowed {
				return mutationir.NodeInput{}, fail(CodePolicy, request, policyir.FieldID{}, "create action has no grant", gateErr)
			}
		}
	} else if operation != mutationir.Create {
		position, positionErr := targetPosition(request)
		if positionErr != nil {
			return mutationir.NodeInput{}, positionErr
		}
		positionFields, err := rootPositionFields(position)
		if err != nil {
			return mutationir.NodeInput{}, fail(CodeRequest, request, policyir.FieldID{}, "relation-traversing mutation positions are not in the root-scalar planner", err)
		}
		node.InfluencingFields = mergeFields(node.InfluencingFields, positionFields)
	}

	hooks, hookErr := hooksFor(request, operation)
	if hookErr != nil {
		return mutationir.NodeInput{}, hookErr
	}
	node.Hooks = hooks
	fact, err := factFor(request, operation)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	node.Fact = fact
	before, after, err := imagesFor(request, node, row)
	if err != nil {
		return mutationir.NodeInput{}, fail(CodeRequirements, request, policyir.FieldID{}, "image requirements could not be derived", err)
	}
	node.Before, node.After = before, after
	return node, nil
}

func refuseSystemOwnedWrites(request RootRequest, input *mutationbind.ScalarInput) error {
	if input == nil {
		return nil
	}
	for _, operation := range input.Operations() {
		if operation.RuntimeOwned() {
			continue
		}
		field, ok := request.Registry.Field(golem.ModelID(request.Model), golem.FieldID(operation.FieldID()))
		if !ok {
			continue
		}
		if compilerir.HasMode(field.Modes(), compilerir.ModeSystem) {
			return fail(CodeExposure, request, operation.FieldID(), "system field is not caller writable", nil)
		}
	}
	return nil
}

func concurrencyHookMutableFields(request RootRequest) []policyir.FieldID {
	model, ok := request.Registry.Model(golem.ModelID(request.Model))
	if !ok {
		return nil
	}
	concurrency, concurrencyEnabled := model.OptimisticConcurrency()
	result := make([]policyir.FieldID, 0, len(model.Fields()))
	for _, publicField := range model.Fields() {
		if concurrencyEnabled && publicField == concurrency {
			continue
		}
		field, ok := request.Registry.Field(model.ID(), publicField)
		if !ok || field.Kind() == compilerir.FieldRelation || field.Updated() || field.DatabaseReadOnly() {
			continue
		}
		if _, generated := field.Generation(); generated {
			continue
		}
		writable := true
		for _, mode := range field.Modes() {
			if mode == compilerir.ModeHidden || mode == compilerir.ModeReadOnly || mode == compilerir.ModeImmutable {
				writable = false
				break
			}
		}
		if writable {
			result = append(result, policyir.FieldID(publicField))
		}
	}
	return result
}

func selectExisting(request RootRequest, policy policyir.Policy, action policyir.Action) (*mutationir.SelectionRequirement, []policyir.FieldID, error) {
	row, err := resolve.RowConstraint(policy, action, request.Model)
	if err != nil {
		return nil, nil, fail(CodePolicy, request, policyir.FieldID{}, "selecting constraint could not be resolved", err)
	}
	conditions := []policyir.Condition{row}
	var position policyir.Condition
	use := classify.UseSelector
	if request.Target != nil {
		position = request.Target.SelectorPredicate()
		conditions = append(conditions, position)
		if guard, ok := request.Target.Target().Guard(); ok {
			conditions = append(conditions, guard)
			position, err = conjoin(request.Model, position, guard)
			if err != nil {
				return nil, nil, fail(CodeRequirements, request, policyir.FieldID{}, "selector and guard could not be combined", err)
			}
		}
	} else if request.Predicate != nil {
		position = *request.Predicate
		conditions = append(conditions, position)
		use = classify.UseFilter
	} else {
		return nil, nil, fail(CodeRequest, request, policyir.FieldID{}, "existing-row operation lacks target position", nil)
	}
	complete, err := conjoin(request.Model, conditions...)
	if err != nil {
		return nil, nil, fail(CodeRequirements, request, policyir.FieldID{}, "complete selecting constraint could not be constructed", err)
	}
	fields, err := rootPositionFields(position)
	if err != nil {
		return nil, nil, fail(CodeRequest, request, policyir.FieldID{}, "relation-traversing mutation positions are not in the root-scalar planner", err)
	}
	if err := classifyPosition(request, policy, action, use, complete, fields); err != nil {
		return nil, nil, err
	}
	selection, err := mutationir.NewSelectionRequirement(action, complete)
	if err != nil {
		return nil, nil, fail(CodeIR, request, policyir.FieldID{}, "selection requirement is invalid", err)
	}
	return &selection, fields, nil
}

func targetPosition(request RootRequest) (policyir.Condition, error) {
	if request.Target != nil {
		position := request.Target.SelectorPredicate()
		if guard, ok := request.Target.Target().Guard(); ok {
			combined, err := conjoin(request.Model, position, guard)
			if err != nil {
				return policyir.Condition{}, fail(CodeRequirements, request, policyir.FieldID{}, "selector and guard could not be combined", err)
			}
			return combined, nil
		}
		return position, nil
	}
	if request.Predicate != nil {
		return *request.Predicate, nil
	}
	return policyir.Condition{}, fail(CodeRequest, request, policyir.FieldID{}, "existing-row operation lacks target position", nil)
}

func classifyPosition(request RootRequest, policy policyir.Policy, action policyir.Action, use classify.UseKind, complete policyir.Condition, fields []policyir.FieldID) error {
	if len(fields) == 0 {
		return fail(CodeClassification, request, policyir.FieldID{}, "mutation position contains no classifiable fields", nil)
	}
	classifier := request.Classifier
	if classifier == nil {
		classifier = defaultClassifier{}
	}
	classificationRequest, err := classify.NewRequestWithConstraint(request.Registry, policy, request.Model, fields, use, action, complete)
	if err != nil {
		return fail(CodeClassification, request, policyir.FieldID{}, "classification request is invalid", err)
	}
	classified, err := classifier.Fields(classificationRequest)
	if err != nil {
		return fail(CodeClassification, request, policyir.FieldID{}, "mutation position classification failed", err)
	}
	if classified.ModelID() != request.Model || classified.UseKind() != use || classified.SelectingAction() != action {
		return fail(CodeClassification, request, policyir.FieldID{}, "classifier returned facts for another question", nil)
	}
	for _, field := range fields {
		result, ok := classified.Field(field)
		if !ok {
			return fail(CodeClassification, request, field, "classifier omitted a mutation position", nil)
		}
		if result.Access() == classify.AccessNever || result.Access() == classify.AccessConditional && !result.DischargedByConstraint() {
			return fail(CodeClassification, request, field, "mutation position is not readable over complete selecting reach", nil)
		}
	}
	return nil
}

func rootPositionFields(condition policyir.Condition) ([]policyir.FieldID, error) {
	seen := make(map[policyir.FieldID]struct{})
	var result []policyir.FieldID
	var walk func(policyir.Condition) error
	walk = func(current policyir.Condition) error {
		switch current.Kind() {
		case policyir.ConditionConstant:
			return nil
		case policyir.ConditionLogical:
			_, children, _ := current.Logical()
			for _, child := range children {
				if err := walk(child); err != nil {
					return err
				}
			}
			return nil
		case policyir.ConditionScalar, policyir.ConditionList, policyir.ConditionJSON:
			field, _ := current.Field()
			if _, ok := seen[field]; !ok {
				seen[field] = struct{}{}
				result = append(result, field)
			}
			return nil
		case policyir.ConditionRelation:
			return fmt.Errorf("relation position")
		default:
			return fmt.Errorf("unknown condition kind")
		}
	}
	return result, walk(condition)
}

func actionFor(operation mutationir.Operation) policyir.Action {
	switch operation {
	case mutationir.Create:
		return policyir.ActionCreate
	case mutationir.Delete, mutationir.DeleteMany:
		return policyir.ActionDelete
	default:
		return policyir.ActionUpdate
	}
}

func scalarFields(operations []mutationir.ScalarOperation) []policyir.FieldID {
	result := make([]policyir.FieldID, len(operations))
	for index, operation := range operations {
		result[index] = operation.FieldID()
	}
	return result
}

func authoredScalarFields(operations []mutationir.ScalarOperation) []policyir.FieldID {
	result := make([]policyir.FieldID, 0, len(operations))
	for _, operation := range operations {
		if !operation.RuntimeOwned() {
			result = append(result, operation.FieldID())
		}
	}
	return result
}

func mergeFields(groups ...[]policyir.FieldID) []policyir.FieldID {
	seen := make(map[policyir.FieldID]struct{})
	var result []policyir.FieldID
	for _, group := range groups {
		for _, field := range group {
			if _, ok := seen[field]; ok {
				continue
			}
			seen[field] = struct{}{}
			result = append(result, field)
		}
	}
	return result
}

func identityBehavior(request RootRequest, operation mutationir.Operation, scalar []mutationir.ScalarOperation) mutationir.IdentityBehavior {
	switch operation {
	case mutationir.Create:
		return mutationir.IdentityProduced
	case mutationir.UpdateMany, mutationir.DeleteMany:
		return mutationir.IdentityBatchChangeRefused
	}
	model, _ := request.Registry.Model(golem.ModelID(request.Model))
	identity := make(map[policyir.FieldID]struct{})
	for _, key := range model.Identities() {
		for _, field := range key.Fields() {
			identity[policyir.FieldID(field)] = struct{}{}
		}
	}
	for _, operation := range scalar {
		if _, ok := identity[operation.FieldID()]; ok {
			return mutationir.IdentityMayChange
		}
	}
	return mutationir.IdentityUnchanged
}

func hooksFor(request RootRequest, operation mutationir.Operation) ([]mutationir.HookRequirement, error) {
	if request.Stance == mutationir.System {
		return nil, nil
	}
	var phases []mutationir.HookPhase
	var hookOperation mutationir.HookOperation
	switch operation {
	case mutationir.Create:
		phases, hookOperation = request.Hooks.Create, mutationir.HookCreate
	case mutationir.Update:
		phases, hookOperation = request.Hooks.Update, mutationir.HookUpdate
	case mutationir.Delete:
		phases, hookOperation = request.Hooks.Delete, mutationir.HookDelete
	case mutationir.UpdateMany:
		phases, hookOperation = request.Hooks.UpdateMany, mutationir.HookUpdateMany
	case mutationir.DeleteMany:
		phases, hookOperation = request.Hooks.DeleteMany, mutationir.HookDeleteMany
	}
	ordered := append([]mutationir.HookPhase(nil), phases...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	result := make([]mutationir.HookRequirement, 0, len(ordered))
	for index, phase := range ordered {
		if index > 0 && ordered[index-1] == phase {
			continue
		}
		requirement, err := mutationir.NewHookRequirement(phase, hookOperation)
		if err != nil {
			return nil, fail(CodeRequest, request, policyir.FieldID{}, "hook inventory contains an invalid phase", err)
		}
		result = append(result, requirement)
	}
	return result, nil
}

func factFor(request RootRequest, operation mutationir.Operation) (mutationir.FactRequirement, error) {
	if !request.facts.capture {
		return mutationir.NoFact(), nil
	}
	model, _ := request.Registry.Model(golem.ModelID(request.Model))
	primary := model.PrimaryKey()
	identity := make([]policyir.FieldID, len(primary))
	for index, field := range primary {
		identity[index] = policyir.FieldID(field)
	}
	if len(identity) == 0 {
		return mutationir.FactRequirement{}, fail(CodeRequirements, request, policyir.FieldID{}, "fact capture requires a primary identity", nil)
	}
	var action mutationir.FactAction
	var before, after []policyir.FieldID
	switch operation {
	case mutationir.Create:
		action, after = mutationir.FactCreated, identity
	case mutationir.Update, mutationir.UpdateMany:
		action, before, after = mutationir.FactUpdated, identity, identity
	case mutationir.Delete, mutationir.DeleteMany:
		action, before = mutationir.FactDeleted, identity
	default:
		return mutationir.NoFact(), nil
	}
	var fact mutationir.FactRequirement
	var err error
	if action == mutationir.FactDeleted {
		snapshot := request.facts.deleteSnapshotFor(operation)
		state := mutationir.DeleteSnapshotUnverifiable
		if snapshot != nil {
			state = mutationir.DeleteSnapshotStoredScalars
		}
		fact, err = mutationir.NewDeleteFactRequirement(before, state, snapshot)
	} else {
		fact, err = mutationir.NewFactRequirement(action, before, after, nil)
	}
	if err != nil {
		return mutationir.FactRequirement{}, fail(CodeIR, request, policyir.FieldID{}, "fact requirement is invalid", err)
	}
	if request.facts.eventSchema != ([32]byte{}) {
		fact, err = fact.WithEventSchema(request.facts.eventSchema)
		if err != nil {
			return mutationir.FactRequirement{}, fail(CodeIR, request, policyir.FieldID{}, "fact event schema is invalid", err)
		}
	}
	return fact, nil
}

func imagesFor(request RootRequest, node mutationir.NodeInput, row policyir.Condition) (mutationir.ImageRequirements, mutationir.ImageRequirements, error) {
	before := newImageBuilder(request.Model, request.Registry)
	after := newImageBuilder(request.Model, request.Registry)
	model, _ := request.Registry.Model(golem.ModelID(request.Model))
	primary := make([]policyir.FieldID, len(model.PrimaryKey()))
	for index, field := range model.PrimaryKey() {
		primary[index] = policyir.FieldID(field)
	}
	before.addFields(primary...)
	after.addFields(primary...)
	needsHookSnapshot := false
	if request.ConcurrencyPrecheck && node.Operation == mutationir.Update {
		needsHookSnapshot = true
	}
	for _, hook := range node.Hooks {
		if hook.Phase() == mutationir.TransactionAfterHook || hook.Phase() == mutationir.AfterCommitHook {
			needsHookSnapshot = true
			break
		}
	}
	if needsHookSnapshot {
		for _, publicField := range model.Fields() {
			field, ok := request.Registry.Field(golem.ModelID(request.Model), publicField)
			if ok && field.Kind() != compilerir.FieldRelation {
				if node.Operation == mutationir.Update || node.Operation == mutationir.Delete {
					before.addFields(policyir.FieldID(publicField))
				}
				if node.Operation == mutationir.Create || node.Operation == mutationir.Update {
					after.addFields(policyir.FieldID(publicField))
				}
			}
		}
	}
	for _, operation := range node.ScalarOperations {
		before.addFields(operation.FieldID())
		after.addFields(operation.FieldID())
	}
	if node.Selection != nil {
		if err := before.addCondition(node.Selection.Constraint()); err != nil {
			return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
		}
	}
	for _, authorization := range node.FieldConditions {
		if node.Operation == mutationir.Create {
			if err := after.addCondition(authorization.Condition()); err != nil {
				return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
			}
		} else if err := before.addCondition(authorization.Condition()); err != nil {
			return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
		}
	}
	if node.Operation == mutationir.Create || node.Operation == mutationir.Update || node.Operation == mutationir.UpdateMany {
		if row.ModelID() != (policyir.ModelID{}) {
			if err := after.addCondition(row); err != nil {
				return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
			}
		}
	}
	if node.Operation == mutationir.Delete || node.Operation == mutationir.DeleteMany {
		before.addFields(request.facts.deleteSnapshotFor(node.Operation)...)
	}
	if node.Operation == mutationir.Delete {
		if err := before.addImage(request.Result); err != nil {
			return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
		}
	} else if node.Operation == mutationir.Create || node.Operation == mutationir.Update {
		if err := after.addImage(request.Result); err != nil {
			return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
		}
	}
	beforeResult, err := before.build()
	if err != nil {
		return mutationir.ImageRequirements{}, mutationir.ImageRequirements{}, err
	}
	afterResult, err := after.build()
	return beforeResult, afterResult, err
}
