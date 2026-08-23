package runtime

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationbatch "github.com/eleven-am/golem/go/internal/mutation/batch"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationnested "github.com/eleven-am/golem/go/internal/mutation/nested"
	mutationplan "github.com/eleven-am/golem/go/internal/mutation/plan"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	mutationupsert "github.com/eleven-am/golem/go/internal/mutation/upsert"
	"github.com/eleven-am/golem/go/internal/observeexec"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyoperator "github.com/eleven-am/golem/go/internal/policy/operator"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/observe"
	"github.com/jmoiron/sqlx"
)

// renderNestedScalarNode is the model-erased bridge from one already planned
// nested node to the stable P4-D scalar renderer. Dynamic relation positioning
// has already selected and locked an exact row; the resulting primary target
// is derived only from RuntimeWork, never reopened from public input.
func renderNestedScalarNode[P, A any](app *App[P, A], stance mutationir.Stance, node mutationir.Node, work mutationnested.RuntimeWork, runtimeOperations []mutationir.ScalarOperation, completeResult bool) (mutationsql.Program, error) {
	if app == nil || node.ModelID() == (policyir.ModelID{}) || work.ModelID() != node.ModelID() {
		return mutationsql.Program{}, fmt.Errorf("P4_RUNTIME_NESTED_RENDER: application, node, and exact work are required")
	}
	if node.Operation() != mutationir.Create && node.Operation() != mutationir.Update && node.Operation() != mutationir.Delete {
		return mutationsql.Program{}, fmt.Errorf("P4_RUNTIME_NESTED_RENDER: node operation %d is not scalar", node.Operation())
	}
	input := mutationir.NodeInput{
		Operation: node.Operation(), Model: node.ModelID(), Branch: mutationir.MainBranch,
		ScalarOperations:  append(node.ScalarOperations(), runtimeOperations...),
		InfluencingFields: node.InfluencingFields(), Before: node.BeforeRequirements(), After: node.AfterRequirements(),
		Hooks: node.Hooks(), Fact: node.Fact(), Identity: node.IdentityBehavior(), FieldConditions: node.FieldAuthorizations(),
		BeforeParent: node.ExecutesBeforeParent(),
	}
	if completeResult {
		complete, completeErr := completeHookImageRequirements(app, golem.ModelID(node.ModelID()))
		if completeErr != nil {
			return mutationsql.Program{}, completeErr
		}
		if node.Operation() == mutationir.Update {
			input.Before = complete
		}
		input.After = complete
		hookOperation := mutationir.HookCreate
		switch node.Operation() {
		case mutationir.Update:
			hookOperation = mutationir.HookUpdate
		case mutationir.Delete:
			hookOperation = mutationir.HookDelete
		}
		requirement, hookErr := mutationir.NewHookRequirement(mutationir.TransactionAfterHook, hookOperation)
		if hookErr != nil {
			return mutationsql.Program{}, hookErr
		}
		inserted := false
		for _, existing := range input.Hooks {
			if existing.Phase() == mutationir.TransactionAfterHook && existing.Operation() == hookOperation {
				inserted = true
				break
			}
		}
		if !inserted {
			index := len(input.Hooks)
			for candidate, existing := range input.Hooks {
				if existing.Phase() == mutationir.AfterCommitHook {
					index = candidate
					break
				}
			}
			input.Hooks = append(input.Hooks, mutationir.HookRequirement{})
			copy(input.Hooks[index+1:], input.Hooks[index:])
			input.Hooks[index] = requirement
		}
	}
	if selection, ok := node.SelectionRequirement(); ok {
		input.Selection = &selection
	}
	if (node.Operation() == mutationir.Create || node.Operation() == mutationir.Update) && stance == mutationir.Caller {
		// Nested creates and update row postconditions are authorized against the completed mutation graph.
		// Keep the scalar planner's required create postcondition shape with a
		// tautology and defer the real row/authored-field conditions to
		// FinalizeNested, after every graph write and before any After hook.
		truth, truthErr := policyir.NewConstant(node.ModelID(), true)
		if truthErr != nil {
			return mutationsql.Program{}, truthErr
		}
		input.RowPostcondition = &truth
		if node.Operation() == mutationir.Create {
			input.FieldConditions = nil
		}
	} else if condition, ok := node.RowPostcondition(); ok {
		input.RowPostcondition = &condition
	}
	var result mutationir.ImageRequirements
	var err error
	if completeResult {
		result, err = completeHookImageRequirements(app, golem.ModelID(node.ModelID()))
	} else {
		result, err = mutationir.NewImageRequirements(node.ModelID(), nil, nil)
	}
	if err != nil {
		return mutationsql.Program{}, err
	}
	if completeResult {
		before, after, imageErr := mutationplan.DeriveNodeImages(mutationplan.NodeImageRequest{Registry: app.registry, Node: input, Result: result})
		if imageErr != nil {
			return mutationsql.Program{}, imageErr
		}
		input.Before, input.After = before, after
	}
	if node.Operation() != mutationir.Create {
		identity, ok := work.Identity()
		if !ok {
			return mutationsql.Program{}, fmt.Errorf("P4_RUNTIME_NESTED_RENDER: existing-row node lacks exact identity")
		}
		values := make([]mutationir.SelectorValue, len(identity.Components()))
		for index, component := range identity.Components() {
			if component.IsNull() {
				return mutationsql.Program{}, fmt.Errorf("P4_RUNTIME_NESTED_RENDER: primary identity component is NULL")
			}
			value, present := component.PolicyValue()
			if !present {
				return mutationsql.Program{}, fmt.Errorf("P4_RUNTIME_NESTED_RENDER: primary identity component has no value")
			}
			var err error
			values[index], err = mutationir.NewSelectorValue(component.FieldID(), value)
			if err != nil {
				return mutationsql.Program{}, err
			}
		}
		target, err := mutationir.NewTarget(node.ModelID(), identity.KeyID(), values, nil)
		if err != nil {
			return mutationsql.Program{}, err
		}
		input.Target = &target
		if position, relational := node.RelationPosition(); node.Operation() == mutationir.Delete && relational && position.Kind() == mutationir.PositionCurrentToOne {
			// ExpandRelationSQL already selected, authorized, and locked this
			// current target. Keep the scalar planner's mandatory caller selection
			// shape, but constrain it only by the captured exact identity so the
			// coordinated owner Disconnect cannot invalidate a relation-traversing
			// delete rule between preflight and physical delete.
			exact, selectionErr := nestedExactTargetCondition(app, target)
			if selectionErr == nil {
				selection, requirementErr := mutationir.NewSelectionRequirement(policyir.ActionDelete, exact)
				selectionErr = requirementErr
				input.Selection = &selection
			}
			if selectionErr != nil {
				return mutationsql.Program{}, selectionErr
			}
		}
	}
	graph, err := mutationir.NewGraph(input)
	if err != nil {
		return mutationsql.Program{}, err
	}
	bounds, err := mutationir.NewStatementBounds(uint32(app.mutationLimits.statementParameters), uint32(app.mutationLimits.touchedRows))
	if err != nil {
		return mutationsql.Program{}, err
	}
	requirement, err := mutationir.NewProviderRequirement(policyir.PortableProviders(), mutationir.CapabilityTransaction)
	if err != nil {
		return mutationsql.Program{}, err
	}
	planInput := mutationir.PlanInput{Stance: stance, Graph: graph, Result: result, Providers: []mutationir.ProviderRequirement{requirement}, Retry: mutationir.NoRetry, Bounds: bounds, SemanticIndexed: modelSemanticIndexed(app.registry, node.ModelID())}
	if node.Fact().Enabled() {
		codec, _, _, codecErr := mutationplan.ModelEventSchema(app.registry, node.ModelID())
		if codecErr != nil {
			return mutationsql.Program{}, codecErr
		}
		planInput.FactCodec = &codec
	}
	plan, err := mutationir.NewPlan(planInput)
	if err != nil {
		return mutationsql.Program{}, err
	}
	return mutationsql.Render(plan, app.registry, app.provider, app.capabilities)
}

// executeNestedScalarNode executes a rendered nested scalar on the one
// transaction already owned by the nested graph. It deliberately cannot begin,
// commit, retry, or fall back to App.database.
func executeNestedScalarNode[P, A any](ctx context.Context, app *App[P, A], binding *executionBinding, queryer sqlx.QueryerContext, node mutationir.Node, work mutationnested.RuntimeWork, stance mutationir.Stance, runtimeOperations []mutationir.ScalarOperation) (mutationnested.ApplyResult, error) {
	result, _, err := executeNestedScalarNodeWithHookResult(ctx, app, binding, queryer, node, work, stance, runtimeOperations, false)
	return result, err
}

// executeNestedScalarNodeWithHookResult is the complete-image variant used by
// transaction-bound hook helpers. The nested engine still owns the write and
// verification; this function only exposes the verified root snapshots so the
// caller hook pipeline can preserve its normal transaction-after/after-commit
// ordering. It never invokes hooks itself.
func executeNestedScalarNodeWithHookResult[P, A any](ctx context.Context, app *App[P, A], binding *executionBinding, queryer sqlx.QueryerContext, node mutationir.Node, work mutationnested.RuntimeWork, stance mutationir.Stance, runtimeOperations []mutationir.ScalarOperation, capture bool) (mutationnested.ApplyResult, *golem.RuntimeMutationHookResult, error) {
	program, err := renderNestedScalarNode(app, stance, node, work, runtimeOperations, capture)
	if err != nil {
		return mutationnested.ApplyResult{}, nil, err
	}
	execution, err := executeScalarProgramOnQueryer(ctx, queryer, binding, app.registry, node.ModelID(), app.provider, program, nil)
	if err != nil {
		return mutationnested.ApplyResult{}, nil, err
	}
	before, after, err := nestedScalarImages(app, node, program, execution)
	if err != nil {
		return mutationnested.ApplyResult{}, nil, err
	}
	result := mutationnested.NewApplyResult(before, after)
	if !capture {
		return result, nil, nil
	}
	hookResult, err := scalarHookResult(app.registry, program, execution)
	if err != nil {
		return mutationnested.ApplyResult{}, nil, err
	}
	return result, &hookResult, nil
}

func nestedScalarImages[P, A any](app *App[P, A], node mutationir.Node, program mutationsql.Program, execution scalarMutationExecution) (*mutationdecode.Row, *mutationdecode.Row, error) {
	decode := func(index uint32) (*mutationdecode.Row, error) {
		statement, ok := execution.statement(index)
		if !ok {
			return nil, fmt.Errorf("P4_RUNTIME_NESTED_IMAGE: statement %d is absent", index)
		}
		row, err := mutationdecode.FromReadCells(app.registry, node.ModelID(), statement.cells)
		if err != nil {
			return nil, err
		}
		return &row, nil
	}
	identity := program.IdentityVerification()
	switch node.Operation() {
	case mutationir.Create:
		after, err := decode(identity.AfterStatement())
		return nil, after, err
	case mutationir.Update:
		beforeIndex, ok := identity.BeforeStatement()
		if !ok {
			return nil, nil, fmt.Errorf("P4_RUNTIME_NESTED_IMAGE: update before image is absent")
		}
		before, err := decode(beforeIndex)
		if err != nil {
			return nil, nil, err
		}
		after, err := decode(identity.AfterStatement())
		return before, after, err
	case mutationir.Delete:
		beforeIndex, ok := identity.BeforeStatement()
		if !ok {
			return nil, nil, fmt.Errorf("P4_RUNTIME_NESTED_IMAGE: delete before image is absent")
		}
		before, err := decode(beforeIndex)
		return before, nil, err
	default:
		return nil, nil, fmt.Errorf("P4_RUNTIME_NESTED_IMAGE: unsupported operation %d", node.Operation())
	}
}

func executeNestedBatchNode[P, A any](ctx context.Context, app *App[P, A], binding *executionBinding, queryer sqlx.QueryerContext, node mutationir.Node, work mutationnested.RuntimeWork, stance mutationir.Stance) (mutationnested.ApplyResult, error) {
	rows, ok := work.BatchRows()
	if !ok || (node.Operation() != mutationir.UpdateMany && node.Operation() != mutationir.DeleteMany) {
		return mutationnested.ApplyResult{}, fmt.Errorf("P4_RUNTIME_NESTED_BATCH: exact batch work and operation are required")
	}
	position, ok := node.RelationPosition()
	if !ok {
		return mutationnested.ApplyResult{}, fmt.Errorf("P4_RUNTIME_NESTED_BATCH: relation position is absent")
	}
	predicate, ok := position.Predicate()
	if !ok {
		return mutationnested.ApplyResult{}, fmt.Errorf("P4_RUNTIME_NESTED_BATCH: relation predicate is absent")
	}
	input := mutationir.NodeInput{
		Operation: node.Operation(), Model: node.ModelID(), Branch: mutationir.MainBranch, Predicate: &predicate,
		ScalarOperations: node.ScalarOperations(), InfluencingFields: node.InfluencingFields(), Before: node.BeforeRequirements(), After: node.AfterRequirements(),
		Hooks: node.Hooks(), Fact: node.Fact(), Identity: node.IdentityBehavior(), FieldConditions: node.FieldAuthorizations(),
	}
	if selection, present := node.SelectionRequirement(); present {
		input.Selection = &selection
	}
	if condition, present := node.RowPostcondition(); present {
		input.RowPostcondition = &condition
	}
	graph, err := mutationir.NewGraph(input)
	if err != nil {
		return mutationnested.ApplyResult{}, err
	}
	bounds, err := mutationir.NewStatementBounds(uint32(app.mutationLimits.statementParameters), uint32(app.mutationLimits.touchedRows))
	if err != nil {
		return mutationnested.ApplyResult{}, err
	}
	requirement, err := mutationir.NewProviderRequirement(policyir.PortableProviders(), mutationir.CapabilityTransaction)
	if err != nil {
		return mutationnested.ApplyResult{}, err
	}
	result, _ := mutationir.NewImageRequirements(node.ModelID(), nil, nil)
	planInput := mutationir.PlanInput{Stance: stance, Graph: graph, Result: result, Providers: []mutationir.ProviderRequirement{requirement}, Retry: mutationir.NoRetry, Bounds: bounds, SemanticIndexed: modelSemanticIndexed(app.registry, node.ModelID())}
	if node.Fact().Enabled() {
		codec, _, _, codecErr := mutationplan.ModelEventSchema(app.registry, node.ModelID())
		if codecErr != nil {
			return mutationnested.ApplyResult{}, codecErr
		}
		planInput.FactCodec = &codec
	}
	plan, err := mutationir.NewPlan(planInput)
	if err != nil {
		return mutationnested.ApplyResult{}, err
	}
	program, err := mutationbatch.Render(plan, app.registry, app.provider, app.capabilities)
	if err != nil {
		return mutationnested.ApplyResult{}, err
	}
	prepared, err := program.PrepareCaptured(rows)
	if err != nil {
		return mutationnested.ApplyResult{}, err
	}
	var authorized []mutationbatch.AuthorizedRow
	var applied, after []mutationdecode.Row
	for _, statement := range prepared.Statements() {
		returned, grants, executeErr := executeMutationBatchStatement(ctx, queryer, app.registry, app.provider, node.ModelID(), statement, statement.ExpectedRows())
		if executeErr != nil {
			return mutationnested.ApplyResult{}, executeErr
		}
		switch statement.Role() {
		case mutationbatch.AuthorizePreImage:
			for index, row := range returned {
				value, buildErr := mutationbatch.NewAuthorizedRow(row, grants[index]...)
				if buildErr != nil {
					return mutationnested.ApplyResult{}, buildErr
				}
				authorized = append(authorized, value)
			}
		case mutationbatch.ApplyUpdate, mutationbatch.ApplyDelete:
			applied = append(applied, returned...)
		case mutationbatch.RehydrateAfterImage:
			after = append(after, returned...)
		default:
			return mutationnested.ApplyResult{}, fmt.Errorf("P4_RUNTIME_NESTED_BATCH: unknown statement role %d", statement.Role())
		}
	}
	verification, err := prepared.VerifyAuthorized(authorized, applied, after, node.Ordinal())
	if err != nil {
		return mutationnested.ApplyResult{}, err
	}
	state, err := binding.mutationState()
	if err != nil {
		return mutationnested.ApplyResult{}, err
	}
	if program.SemanticIndexed() {
		if markErr := markBatchSemanticRecords(state, node.ModelID(), program.PrimaryKey(), verification); markErr != nil {
			return mutationnested.ApplyResult{}, markErr
		}
	}
	if err := state.touch(int(verification.Count())); err != nil {
		return mutationnested.ApplyResult{}, err
	}
	for _, fact := range verification.Facts() {
		before := fact.Before()
		var afterRow *mutationdecode.Row
		if value, present := fact.After(); present {
			afterRow = &value
		}
		if _, err := state.buildFact(app.registry, prepared.FactRequirement(), &before, afterRow, time.Now()); err != nil {
			return mutationnested.ApplyResult{}, err
		}
	}
	applyResult := mutationnested.NewApplyResult(nil, nil)
	if stance == mutationir.Caller {
		if node.Operation() == mutationir.UpdateMany {
			applyResult = mutationnested.WithRuntimeHookResult(applyResult, golem.RuntimeUpdateManyMutationHookResult(golem.ModelID(node.ModelID()), int64(verification.Count())))
		} else {
			applyResult = mutationnested.WithRuntimeHookResult(applyResult, golem.RuntimeDeleteManyMutationHookResult(golem.ModelID(node.ModelID()), int64(verification.Count())))
		}
	}
	return applyResult, nil
}

func prepareNestedCompilation[P, A any](app *App[P, A], policies mutationplan.PolicySet, stance mutationir.Stance, operation mutationir.Operation, input *golem.FrozenMutationInput, target *golem.FrozenMutationTarget, result mutationir.ImageRequirements, snapshots ...*mutationRuntimeValues) (mutationnested.Result, error) {
	return prepareNestedCompilationWithHookOwnedDeferral(app, policies, stance, operation, input, target, result, false, snapshots...)
}

// prepareNestedCompilationWithHookOwnedDeferral is used only by the pure
// caller Create preflight that runs before BeforeCreate. It may defer the
// exact missing fields declared hook-owned by the active ContractIR, and only
// when the generated bindings contain the matching hook. The graph returned
// by that preflight is never executed; the post-hook compilation calls the
// strict entry point above and must bind every required create field.
func prepareNestedCompilationWithHookOwnedDeferral[P, A any](app *App[P, A], policies mutationplan.PolicySet, stance mutationir.Stance, operation mutationir.Operation, input *golem.FrozenMutationInput, target *golem.FrozenMutationTarget, result mutationir.ImageRequirements, deferHookOwned bool, snapshots ...*mutationRuntimeValues) (mutationnested.Result, error) {
	if app == nil || input == nil || len(input.Relations()) == 0 || (operation != mutationir.Create && operation != mutationir.Update) {
		return mutationnested.Result{}, fmt.Errorf("P4_RUNTIME_NESTED_PREPARE: create/update input with relations is required")
	}
	if deferHookOwned && (stance != mutationir.Caller || operation != mutationir.Create) {
		return mutationnested.Result{}, fmt.Errorf("P4_RUNTIME_NESTED_PREPARE: hook-owned completeness can be deferred only for caller create preflight")
	}
	bounds, err := mutationir.NewStatementBounds(uint32(app.mutationLimits.statementParameters), uint32(app.mutationLimits.touchedRows))
	if err != nil {
		return mutationnested.Result{}, err
	}
	// A nested root is also the persisted relation anchor for descendants.
	// Retaining its complete scalar row prevents a later CurrentToOne or
	// composite correlation expansion from discovering that the public result
	// projection omitted an internal anchor column.
	anchorResult, err := completeHookImageRequirements(app, input.ModelID())
	if err != nil {
		return mutationnested.Result{}, err
	}
	request := mutationplan.RootRequest{Stance: stance, Operation: operation, Model: policyir.ModelID(input.ModelID()), Registry: app.registry, Policies: policies, Result: anchorResult, Retry: mutationir.NoRetry, Bounds: bounds}
	runtimeValues := newMutationRuntimeValues()
	if len(snapshots) != 0 && snapshots[0] != nil {
		runtimeValues = snapshots[0]
	}
	if stance == mutationir.Caller {
		request.Hooks = mutationHookInventory(app.bindings, input.ModelID())
	}
	if operation == mutationir.Create {
		ownedFields, ownedErr := nestedRootSourceOwnedFields(*input, app.registry)
		if ownedErr != nil {
			return mutationnested.Result{}, ownedErr
		}
		if deferHookOwned {
			missing, hookErr := callerCreatePreHookOwnedFields(app, *input)
			if hookErr != nil {
				return mutationnested.Result{}, hookErr
			}
			ownedFields = mergeRuntimeOwnedFields(ownedFields, missing)
		}
		bound, _, bindErr := mutationbind.CreateInputWithRuntimeOwnedFields(*input, app.registry, ownedFields)
		if bindErr != nil {
			return mutationnested.Result{}, bindErr
		}
		bound, bindErr = runtimeValues.applyAt(bound, app.registry, 0)
		if bindErr != nil {
			return mutationnested.Result{}, bindErr
		}
		request.Create = &bound
		request.AuthorizedRuntimeFields = make([]policyir.FieldID, len(ownedFields))
		for index, field := range ownedFields {
			request.AuthorizedRuntimeFields[index] = policyir.FieldID(field)
		}
	} else {
		if target == nil {
			return mutationnested.Result{}, fmt.Errorf("P4_RUNTIME_NESTED_PREPARE: update target is absent")
		}
		boundInput, bindErr := mutationbind.UpdateInput(*input, app.registry)
		if bindErr != nil {
			return mutationnested.Result{}, bindErr
		}
		boundInput, bindErr = runtimeValues.applyAt(boundInput, app.registry, 0)
		if bindErr != nil {
			return mutationnested.Result{}, bindErr
		}
		boundTarget, bindErr := mutationbind.Target(*target, input.ModelID(), app.registry)
		if bindErr != nil {
			return mutationnested.Result{}, bindErr
		}
		request.Update, request.Target = &boundInput, &boundTarget
	}
	rootPlan, err := mutationplan.BuildRoot(request)
	if err != nil {
		return mutationnested.Result{}, err
	}
	root, _ := rootPlan.Graph().Root()
	rootInput := nestedNodeInput(root)
	built, err := mutationnested.Build(mutationnested.Request{
		Root: rootInput, Mutations: input.Relations(), Stance: stance, Registry: app.registry, Policies: policies,
		MaxDepth: uint16(app.mutationLimits.nestedDepth), MaxRows: uint32(app.mutationLimits.touchedRows),
		HookInventory: func(model policyir.ModelID) mutationplan.HookInventory {
			if stance != mutationir.Caller {
				return mutationplan.HookInventory{}
			}
			return mutationHookInventory(app.bindings, golem.ModelID(model))
		},
		RuntimeValues: nestedRuntimeValueResolver(runtimeValues, app.registry),
	})
	if err != nil {
		return mutationnested.Result{}, err
	}
	if err := refuseNestedVersionedExistingWrites(app.registry, built.Graph()); err != nil {
		return mutationnested.Result{}, err
	}
	return built, nil
}

// callerCreatePreHookOwnedFields closes the only create-completeness
// exemption available before application code. Field identities come from the
// active ContractIR registry, authored fields are never included, and a
// matching generated BeforeCreate binding is mandatory when anything is
// deferred.
func callerCreatePreHookOwnedFields[P, A any](app *App[P, A], input golem.FrozenMutationInput) ([]golem.FieldID, error) {
	if app == nil || app.registry == nil {
		return nil, fmt.Errorf("P4_RUNTIME_CREATE_PREHOOK: active application registry is required")
	}
	model, present := app.registry.Model(input.ModelID())
	if !present {
		return nil, fmt.Errorf("P4_RUNTIME_CREATE_PREHOOK: create model is absent")
	}
	missing := missingRuntimeOwnedFields(input, model.GraphQLHookOwnedCreateFields())
	if len(missing) != 0 && !hasBeforeCreateHook(app.bindings, input.ModelID()) {
		return nil, fmt.Errorf("P4_RUNTIME_CREATE_PREHOOK: hook-owned create fields require a generated BeforeCreate hook")
	}
	return missing, nil
}

func prepareCallerCreatePreHookInput[P, A any](caller *Caller[P, A], input golem.FrozenMutationInput) error {
	if caller == nil || caller.app == nil || caller.policies == nil {
		return fmt.Errorf("P4_RUNTIME_CREATE_PREHOOK: active caller is required")
	}
	missing, err := callerCreatePreHookOwnedFields(caller.app, input)
	if err != nil {
		return err
	}
	_, _, err = mutationbind.CreateInputWithRuntimeOwnedFields(input, caller.app.registry, missing)
	return err
}

// refuseNestedVersionedExistingWrites is the model-erased expectation gate for
// the nested engine. Its grammar cannot yet carry one exact expectation per
// existing row, so every node that can update/delete an existing versioned row
// (including an FK-owner membership write) must stop during pure preparation.
// Create nodes remain valid and receive their runtime-owned initial token via
// the ordinary nested runtime-value boundary.
func refuseNestedVersionedExistingWrites(registry *schema.Registry, graph mutationir.Graph) error {
	if registry == nil {
		return fmt.Errorf("P4_RUNTIME_NESTED_CONCURRENCY: active schema registry is required")
	}
	for _, node := range graph.Nodes() {
		writesExisting := false
		switch node.Operation() {
		case mutationir.Connect, mutationir.Disconnect, mutationir.SetRelation,
			mutationir.Update, mutationir.UpdateMany,
			mutationir.Delete, mutationir.DeleteMany:
			writesExisting = true
		}
		if !writesExisting {
			continue
		}
		model, ok := registry.Model(golem.ModelID(node.ModelID()))
		if !ok {
			return fmt.Errorf("P4_RUNTIME_NESTED_CONCURRENCY: mutation model is absent")
		}
		if _, enabled := model.OptimisticConcurrency(); enabled {
			return golem.RuntimeOperationError(
				golem.CodeBadUserInput,
				nestedConcurrencyOperationName(node.Operation()),
				golem.ModelID(node.ModelID()),
				golem.FieldID{},
				"mutation request is invalid",
				fmt.Errorf("P4_RUNTIME_NESTED_CONCURRENCY: existing versioned row mutation requires an exact per-row expectation"),
			)
		}
	}
	return nil
}

func nestedConcurrencyOperationName(operation mutationir.Operation) string {
	switch operation {
	case mutationir.Connect:
		return "connect"
	case mutationir.Disconnect:
		return "disconnect"
	case mutationir.SetRelation:
		return "set"
	case mutationir.Update:
		return "update"
	case mutationir.UpdateMany:
		return "updateMany"
	case mutationir.Delete:
		return "delete"
	case mutationir.DeleteMany:
		return "deleteMany"
	default:
		return "mutation"
	}
}

func nestedRootSourceOwnedFields(input golem.FrozenMutationInput, registry *schema.Registry) ([]golem.FieldID, error) {
	var fields []golem.FieldID
	seen := make(map[golem.FieldID]struct{})
	for _, relation := range input.Relations() {
		endpoint, ok := registry.RelationEndpoint(input.ModelID(), relation.FieldID(), relation.RelationID())
		if !ok {
			return nil, fmt.Errorf("P4_RUNTIME_NESTED_PREPARE: root source relation is absent")
		}
		if endpoint.Role() != compilerir.RelationSource || !relationActionOwnsRootCorrelation(relation.Action()) {
			continue
		}
		for _, pair := range endpoint.Correlation() {
			field := pair.ParentFieldID()
			if _, exists := seen[field]; exists {
				continue
			}
			seen[field] = struct{}{}
			fields = append(fields, field)
		}
	}
	return fields, nil
}

func relationActionOwnsRootCorrelation(action golem.MutationRelationAction) bool {
	switch action {
	case golem.MutationRelationCreate, golem.MutationRelationConnect, golem.MutationRelationConnectOrCreate, golem.MutationRelationUpsert:
		return true
	default:
		return false
	}
}

func nestedRuntimeValueResolver(runtimeValues *mutationRuntimeValues, registry *schema.Registry) func(mutationir.NodeInput) (mutationir.NodeInput, error) {
	return func(node mutationir.NodeInput) (mutationir.NodeInput, error) {
		var kind mutationbind.InputKind
		switch node.Operation {
		case mutationir.Create:
			// The root was resolved before row/image planning.
			if node.RuntimeSource == 0 {
				return node, nil
			}
			kind = mutationbind.InputCreate
		case mutationir.Update:
			if node.RuntimeSource == 0 {
				return node, nil
			}
			kind = mutationbind.InputUpdate
		case mutationir.UpdateMany:
			kind = mutationbind.InputUpdateMany
		default:
			return node, nil
		}
		operations, resolveErr := runtimeValues.applyOperations(node.Model, kind, node.ScalarOperations, registry, node.RuntimeSource)
		if resolveErr != nil {
			return mutationir.NodeInput{}, resolveErr
		}
		node.ScalarOperations = operations
		for _, scalar := range operations {
			if !scalar.RuntimeOwned() {
				continue
			}
			present := false
			for _, field := range node.InfluencingFields {
				present = present || field == scalar.FieldID()
			}
			if !present {
				node.InfluencingFields = append(node.InfluencingFields, scalar.FieldID())
			}
		}
		return node, nil
	}
}

func prepareNestedGraph[P, A any](app *App[P, A], policies mutationplan.PolicySet, stance mutationir.Stance, operation mutationir.Operation, input *golem.FrozenMutationInput, target *golem.FrozenMutationTarget, result mutationir.ImageRequirements, snapshots ...*mutationRuntimeValues) (mutationir.Graph, error) {
	built, err := prepareNestedCompilation(app, policies, stance, operation, input, target, result, snapshots...)
	if err != nil {
		return mutationir.Graph{}, err
	}
	return built.Graph(), nil
}

func nestedNodeInput(node mutationir.Node) mutationir.NodeInput {
	input := mutationir.NodeInput{
		Operation: node.Operation(), Model: node.ModelID(), Relation: node.RelationID(), Branch: node.Branch(),
		ScalarOperations: node.ScalarOperations(), InfluencingFields: node.InfluencingFields(), Before: node.BeforeRequirements(), After: node.AfterRequirements(),
		Hooks: node.Hooks(), Fact: node.Fact(), Identity: node.IdentityBehavior(), FieldConditions: node.FieldAuthorizations(),
	}
	if target, ok := node.Target(); ok {
		input.Target = &target
	}
	if predicate, ok := node.Predicate(); ok {
		input.Predicate = &predicate
	}
	if position, ok := node.RelationPosition(); ok {
		input.RelationPosition = &position
	}
	if selection, ok := node.SelectionRequirement(); ok {
		input.Selection = &selection
	}
	if condition, ok := node.RowPostcondition(); ok {
		input.RowPostcondition = &condition
	}
	return input
}

// executeSystemNestedScalar is called by the public System Create/Update route
// after the frozen input has been validated. Caller routing remains disabled
// until relation membership SQL carries caller selection and postconditions.
func executeSystemNestedScalar[P, A, M any](ctx context.Context, system System[P, A], descriptor golem.ModelDescriptor[M], operation mutationir.Operation, input *golem.FrozenMutationInput, target *golem.FrozenMutationTarget, projection scalarMutationProjection) (golem.Row[M], error) {
	if system.app == nil || system.executor == nil {
		return golem.Row[M]{}, fmt.Errorf("P4_RUNTIME_NESTED: system execution is unavailable")
	}
	model := policyir.ModelID(descriptor.Metadata().ModelID())
	result, err := projection.requirements(model)
	if err != nil {
		return golem.Row[M]{}, err
	}
	runtimeValues := newMutationRuntimeValues()
	graph, err := prepareNestedGraph(system.app, nil, mutationir.System, operation, input, target, result, runtimeValues)
	if err != nil {
		return golem.Row[M]{}, err
	}
	var projected golem.Row[M]
	materialized := !projection.active
	boundary := &systemNestedBoundary[P, A]{app: system.app, source: system.executor, graph: graph, stance: mutationir.System, runtimeValues: runtimeValues}
	if projection.active {
		boundary.verify = nestedProjectionObserver(system.app, descriptor, projection, &projected, &materialized)
	}
	if err := executeNestedGraphWithRetry(ctx, graph, system.executor, system.app.mutationLimits, func() mutationnested.ExecutionBoundary {
		// Each attempt owns fresh guard/projection/mutation transaction state.
		boundary.rootResult = nil
		return boundary
	}); err != nil {
		return golem.Row[M]{}, err
	}
	if projection.active {
		if !materialized {
			return golem.Row[M]{}, fmt.Errorf("P4_RUNTIME_NESTED_PROJECTION: root projection was not materialized")
		}
		return projected, nil
	}
	return golem.RuntimeReadRow(descriptor)
}

func executeCallerNestedScalar[P, A, M any](ctx context.Context, caller *Caller[P, A], descriptor golem.ModelDescriptor[M], operation mutationir.Operation, input *golem.FrozenMutationInput, target *golem.FrozenMutationTarget, projection scalarMutationProjection, snapshots ...*mutationRuntimeValues) (golem.Row[M], error) {
	if caller == nil || caller.app == nil || caller.executor == nil || caller.policies == nil {
		return golem.Row[M]{}, fmt.Errorf("P4_RUNTIME_NESTED: caller execution is unavailable")
	}
	result, err := projection.requirements(policyir.ModelID(descriptor.Metadata().ModelID()))
	if err != nil {
		return golem.Row[M]{}, err
	}
	runtimeValues := newMutationRuntimeValues()
	if len(snapshots) != 0 && snapshots[0] != nil {
		runtimeValues = snapshots[0]
	}
	compiled, err := prepareNestedCompilation(caller.app, caller.policies, mutationir.Caller, operation, input, target, result, runtimeValues)
	if err != nil {
		return golem.Row[M]{}, err
	}
	graph := compiled.Graph()
	var projected golem.Row[M]
	materialized := !projection.active
	hooks := &callerMutationHookExecution[A]{bindings: caller.app.bindings, actor: caller.actor, executor: func(binding *executionBinding) golem.HookExecutor {
		return newCallerHookExecutor(caller, binding)
	}}
	boundary := &systemNestedBoundary[P, A]{app: caller.app, source: caller.executor, graph: graph, compiled: &compiled, stance: mutationir.Caller, policies: caller.policies, actor: caller.actor, hooks: hooks, runtimeValues: runtimeValues}
	if projection.active {
		boundary.verify = nestedProjectionObserver(caller.app, descriptor, projection, &projected, &materialized)
	}
	if err := executeNestedGraphWithRetry(ctx, graph, caller.executor, caller.app.mutationLimits, func() mutationnested.ExecutionBoundary {
		boundary.rootResult = nil
		return boundary
	}); err != nil {
		return golem.Row[M]{}, err
	}
	if projection.active {
		if !materialized {
			return golem.Row[M]{}, fmt.Errorf("P4_RUNTIME_NESTED_PROJECTION: root projection was not materialized")
		}
		return projected, nil
	}
	return golem.RuntimeReadRow(descriptor)
}

func executeNestedGraphWithRetry(ctx context.Context, graph mutationir.Graph, source *executionBinding, limits normalizedMutationLimits, boundary func() mutationnested.ExecutionBoundary) error {
	if source == nil || boundary == nil {
		return fmt.Errorf("P4_RUNTIME_NESTED_RETRY: execution binding and boundary factory are required")
	}
	attempts := 1
	guardedBranch := false
	for _, node := range graph.Nodes() {
		if node.Operation() == mutationir.Upsert || node.Operation() == mutationir.ConnectOrCreate {
			guardedBranch = true
			break
		}
	}
	if guardedBranch && !source.scoped {
		attempts = limits.upsertAttempts
	}
	for ordinal := 1; ordinal <= attempts; ordinal++ {
		_, err := mutationnested.Execute(ctx, graph, uint32(limits.touchedRows), boundary())
		if err == nil {
			return nil
		}
		retryable := guardedBranch && mutationupsert.RetryableInterference(err)
		if retryable && (source.scoped || ordinal == attempts) {
			// Preserve retry ownership as typed runtime provenance. Public error
			// mapping must not infer that a raw provider error is an exhausted
			// guarded mutation: the same provider-shaped error may have been
			// returned by application hook code and is intentionally untrusted.
			return &nestedMutationRetryConflict{cause: err}
		}
		if !retryable {
			return err
		}
	}
	return fmt.Errorf("P4_RUNTIME_NESTED_RETRY: bounded attempt loop ended without a result")
}

type nestedMutationRetryConflict struct{ cause error }

func (failure *nestedMutationRetryConflict) Error() string {
	return "P4_RUNTIME_NESTED_RETRY: guarded mutation interference exhausted its retry owner"
}

func (failure *nestedMutationRetryConflict) Unwrap() error { return failure.cause }

// executeCallerNestedHookScalar is the model-erased, same-binding execution
// seam for HookCreateRow and HookUpdateRow. The caller hook pipeline has
// already applied and validated before-hook transformations. This seam runs
// only the authorized nested graph and returns complete verified root images;
// its caller remains responsible for transaction-after and after-commit hooks.
func executeCallerNestedHookScalar[P, A any](ctx context.Context, caller *Caller[P, A], operation mutationir.Operation, model golem.ModelID, input *golem.FrozenMutationInput, target *golem.FrozenMutationTarget, snapshots ...*mutationRuntimeValues) (golem.RuntimeMutationHookResult, error) {
	if caller == nil || caller.app == nil || caller.executor == nil || !caller.executor.scoped || caller.policies == nil || input == nil {
		return golem.RuntimeMutationHookResult{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK_EXECUTOR: active caller transaction and input are required")
	}
	if model == (golem.ModelID{}) || input.ModelID() != model || len(input.Relations()) == 0 || operation != mutationir.Create && operation != mutationir.Update {
		return golem.RuntimeMutationHookResult{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK_EXECUTOR: nested create/update request is invalid")
	}
	if operation == mutationir.Create && target != nil || operation == mutationir.Update && target == nil {
		return golem.RuntimeMutationHookResult{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK_EXECUTOR: nested request target shape is invalid")
	}
	requirements, err := completeHookImageRequirements(caller.app, model)
	if err != nil {
		return golem.RuntimeMutationHookResult{}, err
	}
	runtimeValues := newMutationRuntimeValues()
	if len(snapshots) != 0 && snapshots[0] != nil {
		runtimeValues = snapshots[0]
	}
	compiled, err := prepareNestedCompilation(caller.app, caller.policies, mutationir.Caller, operation, input, target, requirements, runtimeValues)
	if err != nil {
		return golem.RuntimeMutationHookResult{}, err
	}
	graph := compiled.Graph()
	hooks := &callerMutationHookExecution[A]{bindings: caller.app.bindings, actor: caller.actor, executor: func(binding *executionBinding) golem.HookExecutor {
		return newCallerHookExecutor(caller, binding)
	}}
	boundary := &systemNestedBoundary[P, A]{app: caller.app, source: caller.executor, graph: graph, compiled: &compiled, stance: mutationir.Caller, policies: caller.policies, actor: caller.actor, hooks: hooks, captureRoot: true, runtimeValues: runtimeValues}
	if _, err := mutationnested.Execute(ctx, graph, uint32(caller.app.mutationLimits.touchedRows), boundary); err != nil {
		return golem.RuntimeMutationHookResult{}, err
	}
	if boundary.rootResult == nil {
		return golem.RuntimeMutationHookResult{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK_EXECUTOR: verified complete root result is absent")
	}
	return *boundary.rootResult, nil
}

// prepareCallerNestedGraph is a pure bind/authorize/plan validator used after
// every caller before-hook transform. It performs no SQL and acquires no
// transaction, so the final transformed request is the only graph executed.
func prepareCallerNestedGraph[P, A, M any](caller *Caller[P, A], descriptor golem.ModelDescriptor[M], operation mutationir.Operation, input *golem.FrozenMutationInput, target *golem.FrozenMutationTarget, projection scalarMutationProjection, snapshots ...*mutationRuntimeValues) (mutationir.Graph, error) {
	if caller == nil || caller.app == nil || caller.policies == nil || input == nil {
		return mutationir.Graph{}, fmt.Errorf("P4_RUNTIME_NESTED_PREPARE: caller and frozen input are required")
	}
	result, err := projection.requirements(policyir.ModelID(descriptor.Metadata().ModelID()))
	if err != nil {
		return mutationir.Graph{}, err
	}
	return prepareNestedGraph(caller.app, caller.policies, mutationir.Caller, operation, input, target, result, snapshots...)
}

// prepareCallerNestedCreatePreHookGraph preserves the pre-hook refusal gate
// for model-erased nested existing-row writes while allowing only generated
// BeforeCreate-owned required fields to remain absent until that hook runs.
func prepareCallerNestedCreatePreHookGraph[P, A, M any](caller *Caller[P, A], descriptor golem.ModelDescriptor[M], input *golem.FrozenMutationInput, projection scalarMutationProjection, snapshots ...*mutationRuntimeValues) (mutationir.Graph, error) {
	if caller == nil || caller.app == nil || caller.policies == nil || input == nil {
		return mutationir.Graph{}, fmt.Errorf("P4_RUNTIME_NESTED_PREPARE: caller and frozen input are required")
	}
	result, err := projection.requirements(policyir.ModelID(descriptor.Metadata().ModelID()))
	if err != nil {
		return mutationir.Graph{}, err
	}
	built, err := prepareNestedCompilationWithHookOwnedDeferral(caller.app, caller.policies, mutationir.Caller, mutationir.Create, input, nil, result, true, snapshots...)
	if err != nil {
		return mutationir.Graph{}, err
	}
	return built.Graph(), nil
}

func nestedProjectionObserver[P, A, M any](app *App[P, A], descriptor golem.ModelDescriptor[M], projection scalarMutationProjection, output *golem.Row[M], materialized *bool) func(context.Context, *executionBinding, mutationnested.AppliedNode) error {
	return func(ctx context.Context, binding *executionBinding, applied mutationnested.AppliedNode) error {
		node := applied.Node()
		if node.Ordinal() != 0 || node.IsRuntimeReplacement() || node.ModelID() != policyir.ModelID(descriptor.Metadata().ModelID()) {
			return nil
		}
		row, err := materializeNestedMutationProjection(ctx, app, binding, descriptor, projection.planned, applied)
		if err != nil {
			return err
		}
		*output, *materialized = row, true
		return nil
	}
}

// materializeNestedMutationProjection performs the authorized P3 projection
// during reverse verification. At this point every selected descendant and
// membership effect has completed, while the exact transaction is still open.
func materializeNestedMutationProjection[P, A, M any](ctx context.Context, app *App[P, A], binding *executionBinding, descriptor golem.ModelDescriptor[M], planned readplan.Plan, applied mutationnested.AppliedNode) (golem.Row[M], error) {
	image, present := applied.Result().After()
	if !present {
		return golem.Row[M]{}, fmt.Errorf("P4_RUNTIME_NESTED_PROJECTION: root after image is absent")
	}
	condition, err := nestedMutationIdentityCondition(app, image)
	if err != nil {
		return golem.Row[M]{}, err
	}
	constrained, err := readplan.WithAdditionalWhere(planned, condition)
	if err != nil {
		return golem.Row[M]{}, err
	}
	rows, err := executePlan(ctx, app, binding, golem.ReadFindMany, constrained)
	if err != nil {
		return golem.Row[M]{}, err
	}
	if len(rows) == 0 {
		// An authorized mutation does not imply caller read reach for its result.
		return golem.RuntimeReadRow(descriptor)
	}
	if len(rows) != 1 {
		return golem.Row[M]{}, fmt.Errorf("P4_RUNTIME_NESTED_PROJECTION: identity-constrained projection returned %d rows", len(rows))
	}
	return golem.RuntimeTypedReadRow(descriptor, rows[0].row)
}

func nestedMutationIdentityCondition[P, A any](app *App[P, A], image mutationdecode.Row) (policyir.Condition, error) {
	identity, err := mutationdecode.PrimaryIdentity(app.registry, image)
	if err != nil {
		return policyir.Condition{}, err
	}
	model := image.ModelID()
	resolver := policysql.SchemaResolver(app.registry)
	conditions := make([]policyir.Condition, 0, len(identity.Components()))
	for _, component := range identity.Components() {
		value, present := component.PolicyValue()
		if !present {
			return policyir.Condition{}, fmt.Errorf("P4_RUNTIME_NESTED_PROJECTION: primary identity is NULL")
		}
		field, present := resolver.Field(app.provider, model, component.FieldID())
		if !present {
			return policyir.Condition{}, fmt.Errorf("P4_RUNTIME_NESTED_PROJECTION: primary field is absent")
		}
		operand, err := policyir.OneOperand(value)
		if err != nil {
			return policyir.Condition{}, err
		}
		requirements, err := policyoperator.ValidateShape(policyir.OperatorEqual, policyoperator.Shape{Node: policyir.ConditionScalar, FieldType: field.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: resolver.Providers()})
		if err != nil {
			return policyir.Condition{}, err
		}
		condition, err := policyir.NewScalar(model, component.FieldID(), field.Type, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
		if err != nil {
			return policyir.Condition{}, err
		}
		conditions = append(conditions, condition)
	}
	if len(conditions) == 1 {
		return conditions[0], nil
	}
	return policyir.NewLogical(model, policyir.LogicalAnd, conditions)
}

func nestedExactTargetCondition[P, A any](app *App[P, A], target mutationir.Target) (policyir.Condition, error) {
	resolver := policysql.SchemaResolver(app.registry)
	conditions := make([]policyir.Condition, 0, len(target.Values()))
	for _, selected := range target.Values() {
		field, present := resolver.Field(app.provider, target.ModelID(), selected.FieldID())
		if !present {
			return policyir.Condition{}, fmt.Errorf("P4_RUNTIME_NESTED_RENDER: exact target field is absent")
		}
		operand, err := policyir.OneOperand(selected.Value())
		if err != nil {
			return policyir.Condition{}, err
		}
		requirements, err := policyoperator.ValidateShape(policyir.OperatorEqual, policyoperator.Shape{Node: policyir.ConditionScalar, FieldType: field.Type, Operand: operand, Mode: policyir.ComparisonSensitive, Providers: resolver.Providers()})
		if err != nil {
			return policyir.Condition{}, err
		}
		condition, err := policyir.NewScalar(target.ModelID(), selected.FieldID(), field.Type, policyir.OperatorEqual, policyir.ComparisonSensitive, operand, requirements)
		if err != nil {
			return policyir.Condition{}, err
		}
		conditions = append(conditions, condition)
	}
	if len(conditions) == 1 {
		return conditions[0], nil
	}
	return policyir.NewLogical(target.ModelID(), policyir.LogicalAnd, conditions)
}

type systemNestedBoundary[P, A any] struct {
	app           *App[P, A]
	source        *executionBinding
	graph         mutationir.Graph
	compiled      *mutationnested.Result
	stance        mutationir.Stance
	policies      mutationplan.PolicySet
	actor         A
	hooks         *callerMutationHookExecution[A]
	runtimeValues *mutationRuntimeValues
	reuseBinding  bool
	// verify runs during the nested engine's reverse verification pass, while
	// the one transaction/savepoint and mutation binding are still active.
	// Typed root projection and model-erased transaction-after hook adapters
	// attach here; they cannot observe a post-commit or independently re-read
	// image.
	verify func(context.Context, *executionBinding, mutationnested.AppliedNode) error
	// captureRoot is enabled only by the model-erased hook-executor seam. The
	// ordinary public nested route does not require complete root snapshots.
	captureRoot bool
	rootResult  *golem.RuntimeMutationHookResult
}

func (boundary *systemNestedBoundary[P, A]) rootModel() policyir.ModelID {
	root, ok := boundary.graph.Root()
	if !ok {
		return policyir.ModelID{}
	}
	return root.ModelID()
}

func (boundary *systemNestedBoundary[P, A]) compilationState() ([]mutationnested.Result, uint32) {
	if boundary.compiled == nil {
		return nil, 0
	}
	return []mutationnested.Result{*boundary.compiled}, boundary.compiled.SourceUpperBound()
}

func (boundary *systemNestedBoundary[P, A]) BeginNested(ctx context.Context) (mutationnested.ExecutionTransaction, error) {
	if boundary.runtimeValues == nil {
		boundary.runtimeValues = newMutationRuntimeValues()
	}
	compilations, nextSource := boundary.compilationState()
	if boundary.reuseBinding {
		queryer, err := boundary.source.queryerFor(boundary.app.database)
		if err != nil {
			return nil, err
		}
		return &systemNestedTransaction[P, A]{app: boundary.app, binding: boundary.source, queryer: queryer, stance: boundary.stance, policies: boundary.policies, actor: boundary.actor, hooks: boundary.hooks, runtimeValues: boundary.runtimeValues, suppressRootHooks: boundary.captureRoot, captureRoot: boundary.rootCapture(), rootModel: boundary.rootModel(), verify: boundary.verify, compilations: compilations, nextSource: nextSource, commit: func(context.Context) error { return nil }, rollback: func(context.Context) error { return nil }}, nil
	}
	if boundary.source.transaction != nil {
		state, err := boundary.source.mutationState()
		if err != nil {
			return nil, err
		}
		scope, err := state.beginScope()
		if err != nil {
			return nil, err
		}
		sequence, err := boundary.source.nextSavepoint()
		if err != nil {
			_ = scope.rollback()
			return nil, err
		}
		name := fmt.Sprintf(`"golem_nested_%d"`, sequence)
		if _, err := boundary.source.transaction.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
			_ = scope.rollback()
			return nil, err
		}
		queryer, err := boundary.source.queryerFor(boundary.app.database)
		if err != nil {
			_ = rollbackNestedSavepoint(context.Background(), boundary.source.transaction, name, scope, state)
			return nil, err
		}
		return &systemNestedTransaction[P, A]{app: boundary.app, binding: boundary.source, queryer: queryer, stance: boundary.stance, policies: boundary.policies, actor: boundary.actor, hooks: boundary.hooks, runtimeValues: boundary.runtimeValues, suppressRootHooks: boundary.captureRoot, captureRoot: boundary.rootCapture(), rootModel: boundary.rootModel(), verify: boundary.verify, compilations: compilations, nextSource: nextSource,
			commit: func(ctx context.Context) error {
				_, err := boundary.source.transaction.ExecContext(ctx, "RELEASE SAVEPOINT "+name)
				if err == nil {
					err = scope.release()
				}
				return err
			},
			rollback: func(ctx context.Context) error {
				return rollbackNestedSavepoint(ctx, boundary.source.transaction, name, scope, state)
			}}, nil
	}
	if boundary.app.provider == policyir.ProviderSQLite {
		connection, err := boundary.app.database.Connx(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			_ = connection.Close()
			return nil, err
		}
		binding := scopedExecution(boundary.app.database, connection)
		if err := binding.enableMutation(mutationConfig(boundary.app, boundary.source)); err != nil {
			_, _ = connection.ExecContext(ctx, "ROLLBACK")
			_ = connection.Close()
			return nil, err
		}
		queryer, err := binding.queryerFor(boundary.app.database)
		if err != nil {
			binding.close()
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
			_ = connection.Close()
			return nil, err
		}
		return &systemNestedTransaction[P, A]{app: boundary.app, binding: binding, queryer: queryer, stance: boundary.stance, policies: boundary.policies, actor: boundary.actor, hooks: boundary.hooks, runtimeValues: boundary.runtimeValues, suppressRootHooks: boundary.captureRoot, captureRoot: boundary.rootCapture(), rootModel: boundary.rootModel(), verify: boundary.verify, compilations: compilations, nextSource: nextSource,
			commit: func(ctx context.Context) error {
				if err := flushMutationBinding(ctx, connection, binding); err != nil {
					return err
				}
				return commitSQLiteImmediate(ctx, connection, func() {
					commitMutationBinding(ctx, binding)
					binding.close()
				})
			},
			rollback: func(ctx context.Context) error {
				_, err := connection.ExecContext(ctx, "ROLLBACK")
				binding.discardMutation()
				binding.close()
				return errors.Join(err, connection.Close())
			}}, nil
	}
	transaction, err := boundary.app.database.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	binding := transactionExecution(boundary.app.database, transaction)
	if err := binding.enableMutation(mutationConfig(boundary.app, boundary.source)); err != nil {
		_ = transaction.Rollback()
		return nil, err
	}
	queryer, err := binding.queryerFor(boundary.app.database)
	if err != nil {
		binding.close()
		_ = transaction.Rollback()
		return nil, err
	}
	return &systemNestedTransaction[P, A]{app: boundary.app, binding: binding, queryer: queryer, stance: boundary.stance, policies: boundary.policies, actor: boundary.actor, hooks: boundary.hooks, runtimeValues: boundary.runtimeValues, suppressRootHooks: boundary.captureRoot, captureRoot: boundary.rootCapture(), rootModel: boundary.rootModel(), verify: boundary.verify, compilations: compilations, nextSource: nextSource,
		commit: func(ctx context.Context) error {
			if err := flushMutationBinding(ctx, transaction, binding); err != nil {
				return err
			}
			if err := transaction.Commit(); err != nil {
				return err
			}
			commitMutationBinding(ctx, binding)
			binding.close()
			return nil
		},
		rollback: func(context.Context) error {
			binding.discardMutation()
			binding.close()
			return ignoreTransactionDone(transaction.Rollback())
		}}, nil
}

func rollbackNestedSavepoint(ctx context.Context, executor sqlx.ExecerContext, name string, scope *mutationScope, state *mutationState) error {
	rollbackContext := context.Background()
	if ctx != nil {
		rollbackContext = context.WithoutCancel(ctx)
	}
	_, rollbackErr := executor.ExecContext(rollbackContext, "ROLLBACK TO SAVEPOINT "+name)
	_, releaseErr := executor.ExecContext(rollbackContext, "RELEASE SAVEPOINT "+name)
	scopeErr := scope.rollback()
	failure := errors.Join(rollbackErr, releaseErr, scopeErr)
	if failure != nil {
		// A failed savepoint recovery leaves the SQL transaction's contents or
		// state uncertain. Poisoning the outer mutation makes its final flush
		// fail, forcing the owning transaction boundary to roll back even when
		// application code swallows the nested operation error.
		state.poison(failure)
	}
	return failure
}

func (boundary *systemNestedBoundary[P, A]) capture(result golem.RuntimeMutationHookResult) {
	if !boundary.captureRoot {
		return
	}
	if boundary.rootResult != nil {
		final, finalOK := result.After()
		if finalOK {
			switch boundary.rootResult.Operation() {
			case golem.HookCreate:
				result = golem.RuntimeCreateMutationHookResult(boundary.rootResult.ModelID(), final)
			case golem.HookUpdate:
				if original, ok := boundary.rootResult.Before(); ok {
					result = golem.RuntimeUpdateMutationHookResult(boundary.rootResult.ModelID(), original, final)
				}
			}
		}
	}
	value := result
	boundary.rootResult = &value
}

func (boundary *systemNestedBoundary[P, A]) rootCapture() func(golem.RuntimeMutationHookResult) {
	if !boundary.captureRoot {
		return nil
	}
	return boundary.capture
}

type systemNestedTransaction[P, A any] struct {
	app               *App[P, A]
	binding           *executionBinding
	queryer           sqlx.QueryerContext
	stance            mutationir.Stance
	policies          mutationplan.PolicySet
	actor             A
	hooks             *callerMutationHookExecution[A]
	runtimeValues     *mutationRuntimeValues
	suppressRootHooks bool
	captureRoot       func(golem.RuntimeMutationHookResult)
	rootModel         policyir.ModelID
	verify            func(context.Context, *executionBinding, mutationnested.AppliedNode) error
	compilations      []mutationnested.Result
	nextSource        uint32
	guards            map[[32]byte]mutationupsert.SelectorGuard
	guardOrder        [][32]byte
	commit            func(context.Context) error
	rollback          func(context.Context) error
	dependencyFacts   []beforeParentFactCheckpoint
	graphFactOrder    *beforeParentFactCheckpoint
}

type beforeParentFactCheckpoint struct {
	start   int
	ordinal uint32
}

func (transaction *systemNestedTransaction[P, A]) BeginBeforeParent(_ context.Context, _ mutationir.Node) error {
	state, err := transaction.binding.mutationState()
	if err != nil {
		return err
	}
	start, ordinal, err := state.beforeParentFactCheckpoint()
	if err != nil {
		return err
	}
	transaction.dependencyFacts = append(transaction.dependencyFacts, beforeParentFactCheckpoint{start: start, ordinal: ordinal})
	if transaction.graphFactOrder == nil {
		checkpoint := beforeParentFactCheckpoint{start: start, ordinal: ordinal}
		transaction.graphFactOrder = &checkpoint
	}
	return nil
}

// ensureGraphFactOrder captures the graph boundary before its first physical
// apply. Waiting until BeginBeforeParent is too late when a descendant's
// runtime replacement introduces the graph's first source dependency: the
// already-applied root fact would sit before that dependency checkpoint even
// though FinalizeNested receives the complete applied graph.
func (transaction *systemNestedTransaction[P, A]) ensureGraphFactOrder() error {
	if transaction.graphFactOrder != nil {
		return nil
	}
	state, err := transaction.binding.mutationState()
	if err != nil {
		return err
	}
	start, ordinal, err := state.beforeParentFactCheckpoint()
	if err != nil {
		return err
	}
	transaction.graphFactOrder = &beforeParentFactCheckpoint{start: start, ordinal: ordinal}
	return nil
}

func (transaction *systemNestedTransaction[P, A]) CompleteBeforeParent(_ context.Context, parent mutationir.Node) error {
	if len(transaction.dependencyFacts) == 0 {
		return fmt.Errorf("P4_RUNTIME_NESTED: dependency fact checkpoint is absent")
	}
	index := len(transaction.dependencyFacts) - 1
	checkpoint := transaction.dependencyFacts[index]
	transaction.dependencyFacts = transaction.dependencyFacts[:index]
	state, err := transaction.binding.mutationState()
	if err != nil {
		return err
	}
	return state.completeBeforeParentFacts(checkpoint.start, checkpoint.ordinal, parent.Fact().Enabled(), transaction.app.registry)
}

func (transaction *systemNestedTransaction[P, A]) hookSource(node mutationir.Node) (mutationnested.HookSource, bool) {
	for index := len(transaction.compilations) - 1; index >= 0; index-- {
		if source, ok := transaction.compilations[index].HookSource(node); ok {
			return source, true
		}
	}
	return mutationnested.HookSource{}, false
}

func nestedBeforeHookRequest(node mutationir.Node, source mutationnested.HookSource) (golem.RuntimeMutationHookRequest, error) {
	branch, ok := source.Branch()
	if !ok {
		return golem.RuntimeMutationHookRequest{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: selected branch provenance is absent")
	}
	model := golem.ModelID(node.ModelID())
	switch node.Operation() {
	case mutationir.Create:
		input, present := branch.Input()
		if !present {
			return golem.RuntimeMutationHookRequest{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: create branch input is absent")
		}
		return golem.RuntimeCreateMutationHookRequest(model, input), nil
	case mutationir.Update:
		input, inputOK := branch.Input()
		target, targetOK := branch.Target()
		if !inputOK || !targetOK {
			return golem.RuntimeMutationHookRequest{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: update branch target or input is absent")
		}
		return golem.RuntimeUpdateMutationHookRequest(model, target, input), nil
	case mutationir.Delete:
		target, present := branch.Target()
		if !present {
			return golem.RuntimeMutationHookRequest{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: delete branch target is absent")
		}
		return golem.RuntimeDeleteMutationHookRequest(model, target), nil
	case mutationir.UpdateMany:
		input, inputOK := branch.Input()
		predicate, predicateOK := branch.Predicate()
		if !inputOK || !predicateOK {
			return golem.RuntimeMutationHookRequest{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: update-many predicate or input is absent")
		}
		return golem.RuntimeUpdateManyMutationHookRequest(model, predicate, input), nil
	case mutationir.DeleteMany:
		predicate, present := branch.Predicate()
		if !present {
			return golem.RuntimeMutationHookRequest{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: delete-many predicate is absent")
		}
		return golem.RuntimeDeleteManyMutationHookRequest(model, predicate), nil
	default:
		return golem.RuntimeMutationHookRequest{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: operation %d has no direct before-hook request", node.Operation())
	}
}

func runtimePublicPolicyValue[P, A any](app *App[P, A], value policyir.Value) (any, error) {
	switch value.Kind() {
	case policyir.ValueBool:
		result, _ := value.Bool()
		return result, nil
	case policyir.ValueInt16:
		result, _ := value.Signed()
		return int16(result), nil
	case policyir.ValueInt32:
		result, _ := value.Signed()
		return int32(result), nil
	case policyir.ValueInt64:
		result, _ := value.Signed()
		return result, nil
	case policyir.ValueFloat32:
		result, _ := value.Float32Bits()
		return math.Float32frombits(result), nil
	case policyir.ValueFloat64:
		result, _ := value.Float64Bits()
		return math.Float64frombits(result), nil
	case policyir.ValueDecimal:
		coefficient, scale, _ := value.Decimal()
		return golem.NewDecimal(coefficient, scale)
	case policyir.ValueString:
		result, _ := value.Text()
		return result, nil
	case policyir.ValueBytes:
		result, _ := value.Bytes()
		return result, nil
	case policyir.ValueUUID:
		result, _ := value.UUID()
		return golem.NewUUID(result), nil
	case policyir.ValueDate:
		year, month, day, _ := value.Date()
		return golem.NewDate(int(year), time.Month(month), int(day))
	case policyir.ValueTime:
		microseconds, _ := value.Time()
		hour := int(microseconds / 3_600_000_000)
		microseconds %= 3_600_000_000
		minute := int(microseconds / 60_000_000)
		microseconds %= 60_000_000
		second := int(microseconds / 1_000_000)
		return golem.NewTime(hour, minute, second, int(microseconds%1_000_000))
	case policyir.ValueDateTime:
		seconds, nanos, _ := value.DateTime()
		return time.Unix(seconds, int64(nanos)).UTC(), nil
	case policyir.ValueEnum:
		enum, member, _ := value.Enum()
		wire, ok := policysql.SchemaResolver(app.registry).EnumWire(enum, member)
		if !ok {
			return nil, fmt.Errorf("enum identity has no active wire value")
		}
		return wire, nil
	default:
		return nil, fmt.Errorf("identity value kind %d is not a portable unique-selector scalar", value.Kind())
	}
}

func nestedExactTarget[P, A any](app *App[P, A], work mutationnested.RuntimeWork) (golem.FrozenMutationTarget, error) {
	identity, ok := work.Identity()
	if !ok {
		return golem.FrozenMutationTarget{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: exact work identity is absent")
	}
	values := make([]golem.RuntimeSelectorValue, len(identity.Components()))
	for index, component := range identity.Components() {
		value, present := component.PolicyValue()
		if !present {
			return golem.FrozenMutationTarget{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: exact identity contains NULL")
		}
		public, err := runtimePublicPolicyValue(app, value)
		if err != nil {
			return golem.FrozenMutationTarget{}, err
		}
		values[index] = golem.RuntimeSelectorValue{Field: golem.FieldID(component.FieldID()), Value: public}
	}
	return golem.RuntimeMutationTargetFromIdentity(golem.ModelID(work.ModelID()), golem.KeyID(identity.KeyID()), values)
}

func exactNestedTargetMatchesWork(target golem.FrozenMutationTarget, work mutationnested.RuntimeWork, registry *schema.Registry) error {
	bound, err := mutationbind.Target(target, golem.ModelID(work.ModelID()), registry)
	if err != nil {
		return err
	}
	identity, ok := work.Identity()
	if !ok || bound.Target().KeyID() != identity.KeyID() {
		return fmt.Errorf("P4_RUNTIME_NESTED_HOOK: transformed target changed exact owner identity")
	}
	actual := bound.Target().Values()
	expected := identity.Components()
	if len(actual) != len(expected) {
		return fmt.Errorf("P4_RUNTIME_NESTED_HOOK: transformed target changed exact owner identity")
	}
	for index := range actual {
		value, present := expected[index].PolicyValue()
		if !present || actual[index].FieldID() != expected[index].FieldID() || !mutationdecode.EqualValue(actual[index].Value(), value) {
			return fmt.Errorf("P4_RUNTIME_NESTED_HOOK: transformed target changed exact owner identity")
		}
	}
	return nil
}

func (transaction *systemNestedTransaction[P, A]) compileNestedHookReplacement(request mutationnested.TransformRequest, source mutationnested.HookSource, transformed golem.RuntimeMutationHookRequest) (mutationnested.Result, mutationnested.SubtreeReplacement, error) {
	if transformed.ModelID() != source.TargetModelID() {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: transformed request changed model identity")
	}
	operation, ok := mutationHookOperation(request.Node().Operation())
	if !ok || transformed.Operation() != operation {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: transformed request changed operation family")
	}
	frozen, err := golem.RuntimeNestedMutationFromHookRequest(source.ParentModelID(), source.FieldID(), source.RelationID(), source.TargetModelID(), transformed)
	if err != nil {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, err
	}
	var owner mutationnested.AppliedNode
	var ownerOK bool
	if value, present := request.RelationAnchor(); present && golem.ModelID(value.Node().ModelID()) == source.ParentModelID() {
		owner, ownerOK = value, true
	}
	if !ownerOK {
		if value, present := request.Parent(); present && golem.ModelID(value.Node().ModelID()) == source.ParentModelID() {
			owner, ownerOK = value, true
		}
	}
	if !ownerOK {
		if request.Node().ExecutesBeforeParent() && transformed.Operation() == golem.HookCreate {
			return transaction.compileBeforeParentHookReplacement(request.Node(), transformed)
		}
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: selected branch owner context is absent")
	}
	root, err := nestedReplacementAnchorInput(owner, policyir.ModelID(source.ParentModelID()))
	if err != nil {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, err
	}
	built, err := mutationnested.Build(mutationnested.Request{
		Root: root, Mutations: []golem.FrozenNestedMutation{frozen}, Stance: mutationir.Caller,
		Registry: transaction.app.registry, Policies: transaction.policies,
		HookInventory: func(model policyir.ModelID) mutationplan.HookInventory {
			return mutationHookInventory(transaction.app.bindings, golem.ModelID(model))
		},
		SourceOffset: transaction.nextSource,
		MaxDepth:     uint16(transaction.app.mutationLimits.nestedDepth), MaxRows: uint32(transaction.app.mutationLimits.touchedRows),
		RuntimeValues: nestedRuntimeValueResolver(transaction.runtimeValues, transaction.app.registry),
	})
	if err != nil {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, err
	}
	builtRoot, ok := built.Graph().Root()
	if !ok || len(builtRoot.ChildOrdinals()) != 1 {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: replacement did not produce one selected entry")
	}
	replacement, err := mutationnested.NewSubtreeReplacement(built.Graph(), builtRoot.ChildOrdinals()[0])
	return built, replacement, err
}

func (transaction *systemNestedTransaction[P, A]) compileBeforeParentHookReplacement(original mutationir.Node, transformed golem.RuntimeMutationHookRequest) (mutationnested.Result, mutationnested.SubtreeReplacement, error) {
	input, ok := transformed.Input()
	if !ok || transformed.ModelID() != golem.ModelID(original.ModelID()) || transformed.Operation() != golem.HookCreate {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: pre-parent replacement changed create shape")
	}
	ownedFields, err := nestedRootSourceOwnedFields(input, transaction.app.registry)
	if err != nil {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, err
	}
	bound, _, err := mutationbind.CreateInputWithRuntimeOwnedFields(input, transaction.app.registry, ownedFields)
	if err != nil {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, err
	}
	slot, _ := original.RuntimeSourceID()
	bound, err = transaction.runtimeValues.applyAt(bound, transaction.app.registry, slot)
	if err != nil {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, err
	}
	result, err := completeHookImageRequirements(transaction.app, transformed.ModelID())
	if err != nil {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, err
	}
	request := mutationplan.RootRequest{
		Stance: mutationir.Caller, Operation: mutationir.Create, Model: original.ModelID(), Registry: transaction.app.registry, Policies: transaction.policies,
		Create: &bound, Result: result, Retry: mutationir.NoRetry, Hooks: mutationHookInventory(transaction.app.bindings, transformed.ModelID()),
	}
	request.AuthorizedRuntimeFields = make([]policyir.FieldID, len(ownedFields))
	for index, field := range ownedFields {
		request.AuthorizedRuntimeFields[index] = policyir.FieldID(field)
	}
	request.Bounds, err = mutationir.NewStatementBounds(uint32(transaction.app.mutationLimits.statementParameters), uint32(transaction.app.mutationLimits.touchedRows))
	if err != nil {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, err
	}
	plan, err := mutationplan.BuildRoot(request)
	if err != nil {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, err
	}
	planned, _ := plan.Graph().Root()
	root := nestedNodeInput(planned)
	root.Relation = original.RelationID()
	if position, present := original.RelationPosition(); present {
		root.RelationPosition = &position
	}
	root.BeforeParent, root.RuntimeReplacement, root.RuntimeSource = true, true, slot
	built, err := mutationnested.Build(mutationnested.Request{
		Root: root, Mutations: input.Relations(), Stance: mutationir.Caller, Registry: transaction.app.registry, Policies: transaction.policies,
		HookInventory: func(model policyir.ModelID) mutationplan.HookInventory {
			return mutationHookInventory(transaction.app.bindings, golem.ModelID(model))
		},
		SourceOffset: transaction.nextSource, MaxDepth: uint16(transaction.app.mutationLimits.nestedDepth), MaxRows: uint32(transaction.app.mutationLimits.touchedRows),
		RuntimeValues: nestedRuntimeValueResolver(transaction.runtimeValues, transaction.app.registry),
	})
	if err != nil {
		return mutationnested.Result{}, mutationnested.SubtreeReplacement{}, err
	}
	replacement, err := mutationnested.NewSubtreeReplacement(built.Graph(), 0)
	return built, replacement, err
}

func nestedReplacementAnchorInput(owner mutationnested.AppliedNode, model policyir.ModelID) (mutationir.NodeInput, error) {
	if owner.Node().ModelID() != model {
		return mutationir.NodeInput{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: replacement anchor model mismatch")
	}
	before, err := mutationir.NewImageRequirements(model, nil, nil)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	after, err := mutationir.NewImageRequirements(model, nil, nil)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	operation := owner.Node().Operation()
	if operation != mutationir.Create {
		operation = mutationir.Update
	}
	input := mutationir.NodeInput{Operation: operation, Model: model, Branch: mutationir.MainBranch, Before: before, After: after}
	if operation == mutationir.Create {
		input.Identity = mutationir.IdentityProduced
		return input, nil
	}
	identity, ok := owner.Work().Identity()
	if !ok {
		return mutationir.NodeInput{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: existing replacement anchor identity is absent")
	}
	values := make([]mutationir.SelectorValue, len(identity.Components()))
	for index, component := range identity.Components() {
		value, present := component.PolicyValue()
		if !present {
			return mutationir.NodeInput{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: replacement anchor identity contains NULL")
		}
		values[index], err = mutationir.NewSelectorValue(component.FieldID(), value)
		if err != nil {
			return mutationir.NodeInput{}, err
		}
	}
	target, err := mutationir.NewTarget(model, identity.KeyID(), values, nil)
	if err != nil {
		return mutationir.NodeInput{}, err
	}
	input.Target, input.Identity = &target, mutationir.IdentityUnchanged
	return input, nil
}

func (transaction *systemNestedTransaction[P, A]) compileExactHookReplacement(source mutationnested.HookSource, work mutationnested.RuntimeWork, transformed golem.RuntimeMutationHookRequest, preauthorizedCurrentDelete bool) (*mutationnested.Result, mutationnested.SubtreeReplacement, error) {
	target, ok := transformed.Target()
	if !ok {
		return nil, mutationnested.SubtreeReplacement{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: exact-row transformed target is absent")
	}
	if err := exactNestedTargetMatchesWork(target, work, transaction.app.registry); err != nil {
		return nil, mutationnested.SubtreeReplacement{}, err
	}
	model := policyir.ModelID(transformed.ModelID())
	result, err := mutationir.NewImageRequirements(model, nil, nil)
	if err != nil {
		return nil, mutationnested.SubtreeReplacement{}, err
	}
	request := mutationplan.RootRequest{
		Stance: mutationir.Caller, Model: model, Registry: transaction.app.registry, Policies: transaction.policies,
		Result: result, Retry: mutationir.NoRetry,
		Hooks: mutationHookInventory(transaction.app.bindings, transformed.ModelID()),
	}
	request.Bounds, err = mutationir.NewStatementBounds(uint32(transaction.app.mutationLimits.statementParameters), uint32(transaction.app.mutationLimits.touchedRows))
	if err != nil {
		return nil, mutationnested.SubtreeReplacement{}, err
	}
	boundTarget, err := mutationbind.Target(target, transformed.ModelID(), transaction.app.registry)
	if err != nil {
		return nil, mutationnested.SubtreeReplacement{}, err
	}
	request.Target = &boundTarget
	var relations []golem.FrozenNestedMutation
	switch transformed.Operation() {
	case golem.HookUpdate:
		input, present := transformed.Input()
		if !present {
			return nil, mutationnested.SubtreeReplacement{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: exact update input is absent")
		}
		boundInput, bindErr := mutationbind.UpdateInput(input, transaction.app.registry)
		if bindErr != nil {
			return nil, mutationnested.SubtreeReplacement{}, bindErr
		}
		boundInput, bindErr = transaction.runtimeValues.applyAt(boundInput, transaction.app.registry, transaction.nextSource+1)
		if bindErr != nil {
			return nil, mutationnested.SubtreeReplacement{}, bindErr
		}
		request.Operation, request.Update = mutationir.Update, &boundInput
		relations = input.Relations()
	case golem.HookDelete:
		request.Operation = mutationir.Delete
	default:
		return nil, mutationnested.SubtreeReplacement{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: exact replacement operation %q is unsupported", transformed.Operation())
	}
	rootPlan, err := mutationplan.BuildRoot(request)
	if err != nil {
		return nil, mutationnested.SubtreeReplacement{}, err
	}
	plannedRoot, _ := rootPlan.Graph().Root()
	rootInput := nestedNodeInput(plannedRoot)
	if preauthorizedCurrentDelete && transformed.Operation() == golem.HookDelete {
		exact, exactErr := nestedExactTargetCondition(transaction.app, boundTarget.Target())
		if exactErr != nil {
			return nil, mutationnested.SubtreeReplacement{}, exactErr
		}
		selection, selectionErr := mutationir.NewSelectionRequirement(policyir.ActionDelete, exact)
		if selectionErr != nil {
			return nil, mutationnested.SubtreeReplacement{}, selectionErr
		}
		rootInput.Selection = &selection
	}
	rootInput.RuntimeReplacement = true
	graph, err := mutationir.NewGraph(rootInput)
	if err != nil {
		return nil, mutationnested.SubtreeReplacement{}, err
	}
	var compiled *mutationnested.Result
	if len(relations) != 0 {
		built, buildErr := mutationnested.Build(mutationnested.Request{
			Root: rootInput, Mutations: relations, Stance: mutationir.Caller,
			Registry: transaction.app.registry, Policies: transaction.policies,
			HookInventory: func(model policyir.ModelID) mutationplan.HookInventory {
				return mutationHookInventory(transaction.app.bindings, golem.ModelID(model))
			},
			SourceOffset: transaction.nextSource,
			MaxDepth:     uint16(transaction.app.mutationLimits.nestedDepth), MaxRows: uint32(transaction.app.mutationLimits.touchedRows),
			RuntimeValues: nestedRuntimeValueResolver(transaction.runtimeValues, transaction.app.registry),
		})
		if buildErr != nil {
			return nil, mutationnested.SubtreeReplacement{}, buildErr
		}
		compiled, graph = &built, built.Graph()
	}
	replacement, err := mutationnested.NewSubtreeReplacement(graph, 0)
	return compiled, replacement, err
}

func (transaction *systemNestedTransaction[P, A]) exactPositionBeforeHookRequest(node mutationir.Node, source mutationnested.HookSource, work mutationnested.RuntimeWork) (golem.RuntimeMutationHookRequest, error) {
	branch, ok := source.Branch()
	if !ok {
		return golem.RuntimeMutationHookRequest{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: exact branch provenance is absent")
	}
	target, err := nestedExactTarget(transaction.app, work)
	if err != nil {
		return golem.RuntimeMutationHookRequest{}, err
	}
	model := golem.ModelID(node.ModelID())
	switch node.Operation() {
	case mutationir.Update:
		input, present := branch.Input()
		if !present {
			return golem.RuntimeMutationHookRequest{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: exact update input is absent")
		}
		return golem.RuntimeUpdateMutationHookRequest(model, target, input), nil
	case mutationir.Delete:
		return golem.RuntimeDeleteMutationHookRequest(model, target), nil
	default:
		return golem.RuntimeMutationHookRequest{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: exact branch operation %d is unsupported", node.Operation())
	}
}

func (transaction *systemNestedTransaction[P, A]) transformExactPosition(ctx context.Context, request mutationnested.TransformRequest, source mutationnested.HookSource, work mutationnested.RuntimeWork, original golem.RuntimeMutationHookRequest) (mutationnested.SubtreeReplacement, bool, error) {
	operation := original.Operation()
	position, positioned := request.Node().RelationPosition()
	preauthorizedCurrentDelete := request.Node().Operation() == mutationir.Delete && positioned && position.Kind() == mutationir.PositionCurrentToOne
	var candidate *mutationnested.Result
	var replacement mutationnested.SubtreeReplacement
	validate := func(transformed golem.RuntimeMutationHookRequest) error {
		if transformed.ModelID() != original.ModelID() || transformed.Operation() != operation {
			return fmt.Errorf("P4_RUNTIME_NESTED_HOOK: transformed exact request changed model or operation")
		}
		var compileErr error
		candidate, replacement, compileErr = transaction.compileExactHookReplacement(source, work, transformed, preauthorizedCurrentDelete)
		return compileErr
	}
	hookContext := golem.RuntimeContextWithActor(ctx, transaction.actor)
	transformed, err := golem.RuntimeInvokeMutationBeforeHooks(hookContext, transaction.app.bindings, original, validate)
	if err != nil {
		return mutationnested.SubtreeReplacement{}, false, &mutationHookFailure{operation: operation, phase: golem.HookBefore, cause: err}
	}
	if err := validate(transformed); err != nil {
		return mutationnested.SubtreeReplacement{}, false, err
	}
	if candidate != nil {
		transaction.compilations = append(transaction.compilations, *candidate)
		transaction.nextSource = candidate.SourceUpperBound()
	}
	return replacement, true, nil
}

func (transaction *systemNestedTransaction[P, A]) TransformNested(ctx context.Context, request mutationnested.TransformRequest) (mutationnested.SubtreeReplacement, bool, error) {
	if transaction.stance != mutationir.Caller {
		return mutationnested.SubtreeReplacement{}, false, nil
	}
	node := request.Node()
	if position, ok := node.RelationPosition(); ok && position.Kind() == mutationir.PositionBranchResult && len(node.Hooks()) == 0 && !node.Fact().Enabled() {
		// Branch-result membership is the internal physical completion of its
		// containing owner mutation. The owner already passed through the public
		// before-hook pipeline; replaying Update hooks here would expose one
		// logical mutation twice.
		return mutationnested.SubtreeReplacement{}, false, nil
	}
	if node.Ordinal() == 0 && !node.IsRuntimeReplacement() {
		if _, relational := node.RelationPosition(); !relational {
			// The public scalar boundary already transformed and revalidated the
			// original root before this graph entered the transaction. Only
			// selected descendants (and explicitly marked local replacements)
			// belong to the dynamic nested hook pipeline.
			return mutationnested.SubtreeReplacement{}, false, nil
		}
	}
	operation, ok := mutationHookOperation(node.Operation())
	if !ok || !hasMutationHook(transaction.app.bindings, golem.ModelID(node.ModelID()), operation, golem.HookBefore) {
		return mutationnested.SubtreeReplacement{}, false, nil
	}
	source, ok := transaction.hookSource(node)
	if !ok {
		return mutationnested.SubtreeReplacement{}, false, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: selected child node=%d operation=%d model=%x lacks frozen source provenance", node.Ordinal(), node.Operation(), node.ModelID())
	}
	if request.Stage() == mutationnested.TransformPostExpand {
		work, present := request.Work()
		if !present {
			return mutationnested.SubtreeReplacement{}, false, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: post-expansion exact work is absent")
		}
		if !nestedMembershipOperation(node.Operation()) {
			original, buildErr := transaction.exactPositionBeforeHookRequest(node, source, work)
			if buildErr != nil {
				return mutationnested.SubtreeReplacement{}, false, buildErr
			}
			return transaction.transformExactPosition(ctx, request, source, work, original)
		}
		return transaction.transformMembership(ctx, request, source, work)
	}
	if request.Stage() != mutationnested.TransformPreExpand {
		return mutationnested.SubtreeReplacement{}, false, nil
	}
	original, err := nestedBeforeHookRequest(node, source)
	if err != nil {
		return mutationnested.SubtreeReplacement{}, false, err
	}
	var candidate mutationnested.Result
	var replacement mutationnested.SubtreeReplacement
	validate := func(transformed golem.RuntimeMutationHookRequest) error {
		var compileErr error
		candidate, replacement, compileErr = transaction.compileNestedHookReplacement(request, source, transformed)
		return compileErr
	}
	hookContext := golem.RuntimeContextWithActor(ctx, transaction.actor)
	transformed, err := golem.RuntimeInvokeMutationBeforeHooks(hookContext, transaction.app.bindings, original, validate)
	if err != nil {
		return mutationnested.SubtreeReplacement{}, false, &mutationHookFailure{operation: operation, phase: golem.HookBefore, cause: err}
	}
	if err := validate(transformed); err != nil {
		return mutationnested.SubtreeReplacement{}, false, err
	}
	transaction.compilations = append(transaction.compilations, candidate)
	transaction.nextSource = candidate.SourceUpperBound()
	return replacement, true, nil
}

func nestedMembershipOperation(operation mutationir.Operation) bool {
	return operation == mutationir.Connect || operation == mutationir.Disconnect || operation == mutationir.SetRelation
}

func membershipFrozenInput(model policyir.ModelID, operations []mutationir.ScalarOperation) (golem.FrozenMutationInput, error) {
	fields := make([]golem.RuntimeMutationFieldValue, len(operations))
	for index, operation := range operations {
		value, present := operation.Value()
		kind := golem.MutationFieldSet
		switch operation.Kind() {
		case mutationir.ScalarSet:
			kind = golem.MutationFieldSet
		case mutationir.ScalarNull:
			kind = golem.MutationFieldNull
		default:
			return golem.FrozenMutationInput{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: membership assignment is not set/null")
		}
		fields[index] = golem.RuntimeMutationFieldValue{Field: golem.FieldID(operation.FieldID()), Operation: kind, Value: value, HasValue: present}
	}
	return golem.RuntimeMutationInputFromFields(golem.ModelID(model), fields)
}

func validateMembershipTransform(input golem.FrozenMutationInput, required []mutationir.ScalarOperation, registry *schema.Registry) error {
	bound, err := mutationbind.UpdateInput(input, registry)
	if err != nil {
		return err
	}
	actual := make(map[policyir.FieldID]mutationir.ScalarOperation)
	for _, operation := range bound.Operations() {
		actual[operation.FieldID()] = operation
	}
	for _, expected := range required {
		operation, present := actual[expected.FieldID()]
		if !present || operation.Kind() != expected.Kind() {
			return fmt.Errorf("P4_RUNTIME_NESTED_HOOK: transformed membership input removed or changed its relation assignment")
		}
		expectedValue, expectedOK := expected.Value()
		actualValue, actualOK := operation.Value()
		if expectedOK != actualOK || expectedOK && !mutationdecode.EqualValue(expectedValue, actualValue) {
			return fmt.Errorf("P4_RUNTIME_NESTED_HOOK: transformed membership input changed its relation assignment")
		}
	}
	return nil
}

func (transaction *systemNestedTransaction[P, A]) transformMembership(ctx context.Context, request mutationnested.TransformRequest, source mutationnested.HookSource, work mutationnested.RuntimeWork) (mutationnested.SubtreeReplacement, bool, error) {
	anchorApplied, ok := request.RelationAnchor()
	if !ok {
		return mutationnested.SubtreeReplacement{}, false, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: membership relation anchor is absent")
	}
	anchor, err := nestedAppliedRow(anchorApplied)
	if err != nil {
		return mutationnested.SubtreeReplacement{}, false, err
	}
	related, ok := work.ResolvedRelationRow()
	if !ok {
		return mutationnested.SubtreeReplacement{}, false, fmt.Errorf("P4_RUNTIME_NESTED_HOOK: membership related row is absent")
	}
	effect := work.MembershipEffect()
	if effect == 0 {
		if request.Node().Operation() == mutationir.Disconnect {
			effect = mutationnested.MembershipDisconnect
		} else {
			effect = mutationnested.MembershipConnect
		}
	}
	_, required, err := transaction.membershipUpdateNode(request.Node(), work, anchor, related, effect)
	if err != nil {
		return mutationnested.SubtreeReplacement{}, false, err
	}
	// Runtime-owned values are persisted with the synthesized owner update but
	// are not caller-authored hook capabilities. The exact replacement compiler
	// resolves them again from the same frozen snapshot after the hook returns.
	authoredRequired := make([]mutationir.ScalarOperation, 0, len(required))
	for _, operation := range required {
		if !operation.RuntimeOwned() {
			authoredRequired = append(authoredRequired, operation)
		}
	}
	input, err := membershipFrozenInput(work.ModelID(), authoredRequired)
	if err != nil {
		return mutationnested.SubtreeReplacement{}, false, err
	}
	target, err := nestedExactTarget(transaction.app, work)
	if err != nil {
		return mutationnested.SubtreeReplacement{}, false, err
	}
	original := golem.RuntimeUpdateMutationHookRequest(golem.ModelID(work.ModelID()), target, input)
	var candidate *mutationnested.Result
	var replacement mutationnested.SubtreeReplacement
	validate := func(transformed golem.RuntimeMutationHookRequest) error {
		if transformed.ModelID() != original.ModelID() || transformed.Operation() != golem.HookUpdate {
			return fmt.Errorf("P4_RUNTIME_NESTED_HOOK: transformed membership request changed model or operation")
		}
		updated, present := transformed.Input()
		if !present {
			return fmt.Errorf("P4_RUNTIME_NESTED_HOOK: transformed membership input is absent")
		}
		if err := validateMembershipTransform(updated, authoredRequired, transaction.app.registry); err != nil {
			return err
		}
		var compileErr error
		candidate, replacement, compileErr = transaction.compileExactHookReplacement(source, work, transformed, false)
		return compileErr
	}
	hookContext := golem.RuntimeContextWithActor(ctx, transaction.actor)
	transformed, err := golem.RuntimeInvokeMutationBeforeHooks(hookContext, transaction.app.bindings, original, validate)
	if err != nil {
		return mutationnested.SubtreeReplacement{}, false, &mutationHookFailure{operation: golem.HookUpdate, phase: golem.HookBefore, cause: err}
	}
	if err := validate(transformed); err != nil {
		return mutationnested.SubtreeReplacement{}, false, err
	}
	if candidate != nil {
		transaction.compilations = append(transaction.compilations, *candidate)
		transaction.nextSource = candidate.SourceUpperBound()
	}
	return replacement, true, nil
}

func (transaction *systemNestedTransaction[P, A]) ExpandNested(ctx context.Context, request mutationnested.ExpansionRequest) (mutationnested.RuntimeExpansion, error) {
	node := request.Node()
	if _, relational := node.RelationPosition(); relational {
		if node.Operation() == mutationir.Upsert || node.Operation() == mutationir.ConnectOrCreate {
			position, _ := node.RelationPosition()
			if _, selected := position.Target(); selected {
				if err := transaction.acquireNestedSelectorGuard(ctx, node); err != nil {
					return mutationnested.RuntimeExpansion{}, err
				}
			}
		}
		return mutationnested.ExpandRelationSQL(ctx, mutationnested.SQLExpansionRequest{Expansion: request, Queryer: transaction.queryer, Registry: transaction.app.registry, Provider: transaction.app.provider, Capabilities: transaction.app.capabilities, MaxRows: uint32(transaction.app.mutationLimits.touchedRows), MaxParameters: uint32(transaction.app.mutationLimits.statementParameters)})
	}
	if node.Operation() == mutationir.Create {
		model := node.ModelID()
		key := append([]byte("root-create:"), model[:]...)
		work, err := mutationnested.NewCreateWork(node.ModelID(), key)
		if err != nil {
			return mutationnested.RuntimeExpansion{}, err
		}
		return mutationnested.NewRuntimeExpansion([]mutationnested.RuntimeWork{work}, 0)
	}
	target, ok := node.Target()
	if !ok {
		return mutationnested.RuntimeExpansion{}, fmt.Errorf("P4_RUNTIME_NESTED: root target is absent")
	}
	components := make([]mutationdecode.IdentityComponent, len(target.Values()))
	for index, value := range target.Values() {
		components[index], _ = mutationdecode.IdentityValue(value.FieldID(), value.Value())
	}
	identity, err := mutationdecode.NewIdentity(target.KeyID(), components)
	if err != nil {
		return mutationnested.RuntimeExpansion{}, err
	}
	key, err := mutationfact.EncodeIdentity(identity)
	if err != nil {
		return mutationnested.RuntimeExpansion{}, err
	}
	work, err := mutationnested.NewExistingWork(node.ModelID(), identity, key)
	if err != nil {
		return mutationnested.RuntimeExpansion{}, err
	}
	return mutationnested.NewRuntimeExpansion([]mutationnested.RuntimeWork{work}, 0)
}

func (transaction *systemNestedTransaction[P, A]) acquireNestedSelectorGuard(ctx context.Context, node mutationir.Node) error {
	position, present := node.RelationPosition()
	if !present {
		return fmt.Errorf("P4_RUNTIME_NESTED_GUARD: branch node has no relation position")
	}
	target, present := position.Target()
	if !present {
		return fmt.Errorf("P4_RUNTIME_NESTED_GUARD: branch selector target is absent")
	}
	guard, err := mutationupsert.PrepareSelectorGuard(target, transaction.app.registry, transaction.app.provider, transaction.app.capabilities)
	if err != nil {
		return err
	}
	token := guard.Token()
	if transaction.guards != nil {
		if _, acquired := transaction.guards[token]; acquired {
			return nil
		}
	} else {
		transaction.guards = make(map[[32]byte]mutationupsert.SelectorGuard)
	}
	if err := executeNestedGuardStatement(ctx, transaction.queryer, guard.AcquireStatement()); err != nil {
		return err
	}
	transaction.guards[token] = guard
	transaction.guardOrder = append(transaction.guardOrder, token)
	return nil
}

func executeNestedGuardStatement(ctx context.Context, queryer sqlx.QueryerContext, statement mutationupsert.Statement) error {
	recordQueryerStatement(ctx, queryer)
	rows, err := queryer.QueryxContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var ignored any
		if err := rows.Scan(&ignored); err != nil {
			return err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("P4_RUNTIME_NESTED_GUARD: statement role %d returned %d rows; expected 1", statement.Role(), count)
	}
	return nil
}

func (transaction *systemNestedTransaction[P, A]) ApplyNested(ctx context.Context, request mutationnested.ApplyRequest) (result mutationnested.ApplyResult, resultErr error) {
	if err := transaction.ensureGraphFactOrder(); err != nil {
		return mutationnested.ApplyResult{}, err
	}
	node, work := request.Node(), request.Work()
	var observation *observeexec.Span
	if node.Ordinal() != 0 && node.Operation() != mutationir.BranchProbe {
		ctx, observation = observeexec.BeginChild(ctx, golem.ModelID(node.ModelID()), observe.KindMutation, mutationObservationOperation(node.Operation()), observe.PhaseFinish)
		defer func() { finishObservation(observation, resultErr) }()
	}
	switch node.Operation() {
	case mutationir.BranchProbe:
		row, ok := work.ResolvedRelationRow()
		if !ok {
			return mutationnested.ApplyResult{}, fmt.Errorf("P4_RUNTIME_NESTED: branch probe row is absent")
		}
		return mutationnested.NewApplyResult(nil, &row), nil
	case mutationir.Connect, mutationir.Disconnect, mutationir.SetRelation:
		anchorApplied, ok := request.RelationAnchor()
		if !ok {
			return mutationnested.ApplyResult{}, fmt.Errorf("P4_RUNTIME_NESTED: membership anchor is absent")
		}
		anchor, err := nestedAppliedRow(anchorApplied)
		if err != nil {
			return mutationnested.ApplyResult{}, err
		}
		related, ok := work.ResolvedRelationRow()
		if !ok {
			return mutationnested.ApplyResult{}, fmt.Errorf("P4_RUNTIME_NESTED: related row is absent")
		}
		effect := work.MembershipEffect()
		if effect == 0 {
			if node.Operation() == mutationir.Disconnect {
				effect = mutationnested.MembershipDisconnect
			} else {
				effect = mutationnested.MembershipConnect
			}
		}
		update, operations, err := transaction.membershipUpdateNode(node, work, anchor, related, effect)
		if err != nil {
			return mutationnested.ApplyResult{}, err
		}
		capture := transaction.captureHookResult(node.ModelID(), golem.HookUpdate)
		position, positioned := node.RelationPosition()
		internalOwnerEffect := positioned && position.Kind() == mutationir.PositionBranchResult && len(node.Hooks()) == 0 && !node.Fact().Enabled()
		if internalOwnerEffect {
			capture = false
		}
		if transaction.captureRoot != nil && node.ModelID() == transaction.rootModel && !internalOwnerEffect {
			capture = true
		}
		if capture {
			result, hookResult, err := executeNestedScalarNodeWithHookResult(ctx, transaction.app, transaction.binding, transaction.queryer, update, work, transaction.stance, operations, true)
			if err != nil {
				return mutationnested.ApplyResult{}, err
			}
			if hookResult == nil {
				return mutationnested.ApplyResult{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK_RESULT: membership snapshots are absent")
			}
			if transaction.captureRoot != nil && node.ModelID() == transaction.rootModel && !internalOwnerEffect {
				transaction.captureRoot(*hookResult)
			}
			return mutationnested.WithRuntimeHookResult(result, *hookResult), nil
		}
		return executeNestedScalarNode(ctx, transaction.app, transaction.binding, transaction.queryer, update, work, transaction.stance, operations)
	case mutationir.UpdateMany, mutationir.DeleteMany:
		return executeNestedBatchNode(ctx, transaction.app, transaction.binding, transaction.queryer, node, work, transaction.stance)
	case mutationir.Create, mutationir.Update, mutationir.Delete:
		operations, err := transaction.runtimeOwnedOperations(node, request)
		if err != nil {
			return mutationnested.ApplyResult{}, err
		}
		hookOperation, _ := mutationHookOperation(node.Operation())
		capture := transaction.captureHookResult(node.ModelID(), hookOperation)
		if node.Ordinal() == 0 && !node.IsRuntimeReplacement() && transaction.captureRoot != nil {
			capture = true
		}
		if capture {
			result, hookResult, err := executeNestedScalarNodeWithHookResult(ctx, transaction.app, transaction.binding, transaction.queryer, node, work, transaction.stance, operations, true)
			if err != nil {
				return mutationnested.ApplyResult{}, err
			}
			if hookResult == nil {
				return mutationnested.ApplyResult{}, fmt.Errorf("P4_RUNTIME_NESTED_HOOK_RESULT: complete root snapshots are absent")
			}
			if node.Ordinal() == 0 && !node.IsRuntimeReplacement() && transaction.captureRoot != nil {
				transaction.captureRoot(*hookResult)
			}
			return mutationnested.WithRuntimeHookResult(result, *hookResult), nil
		}
		return executeNestedScalarNode(ctx, transaction.app, transaction.binding, transaction.queryer, node, work, transaction.stance, operations)
	default:
		return mutationnested.ApplyResult{}, fmt.Errorf("P4_RUNTIME_NESTED: unsupported apply operation %d", node.Operation())
	}
}

func (transaction *systemNestedTransaction[P, A]) captureHookResult(model policyir.ModelID, operation golem.HookOperation) bool {
	if transaction.stance != mutationir.Caller || operation == "" {
		return false
	}
	external := golem.ModelID(model)
	return hasMutationHook(transaction.app.bindings, external, operation, golem.HookAfter) || hasMutationHook(transaction.app.bindings, external, operation, golem.HookAfterCommit)
}

func (transaction *systemNestedTransaction[P, A]) membershipUpdateNode(node mutationir.Node, work mutationnested.RuntimeWork, anchor, related mutationdecode.Row, effect mutationnested.MembershipEffect) (mutationir.Node, []mutationir.ScalarOperation, error) {
	position, _ := node.RelationPosition()
	endpoint, ok := transaction.app.registry.RelationEndpoint(golem.ModelID(position.ParentModelID()), golem.FieldID(position.FieldID()), golem.RelationID(position.RelationID()))
	if !ok {
		return mutationir.Node{}, nil, fmt.Errorf("P4_RUNTIME_NESTED: membership endpoint is absent")
	}
	resolver := policysql.SchemaResolver(transaction.app.registry)
	operations := make([]mutationir.ScalarOperation, len(endpoint.Correlation()))
	for index, pair := range endpoint.Correlation() {
		ownerField, valueField, valueRow := policyir.FieldID(pair.ParentFieldID()), policyir.FieldID(pair.ChildFieldID()), related
		if endpoint.Role() == compilerir.RelationInverse {
			ownerField, valueField, valueRow = policyir.FieldID(pair.ChildFieldID()), policyir.FieldID(pair.ParentFieldID()), anchor
		}
		field, present := resolver.Field(transaction.app.provider, node.ModelID(), ownerField)
		if !present {
			return mutationir.Node{}, nil, fmt.Errorf("P4_RUNTIME_NESTED: membership owner field is absent")
		}
		var err error
		if effect == mutationnested.MembershipDisconnect {
			if !field.Nullable {
				return mutationir.Node{}, nil, fmt.Errorf("P4_RUNTIME_NESTED: required membership cannot disconnect")
			}
			operations[index], err = mutationir.NewNull(ownerField, field.Type)
		} else {
			cell, present := valueRow.Cell(valueField)
			if !present || cell.IsNull() {
				return mutationir.Node{}, nil, fmt.Errorf("P4_RUNTIME_NESTED: membership value is absent")
			}
			value, _ := cell.PolicyValue()
			operations[index], err = mutationir.NewSet(ownerField, field.Type, value)
		}
		if err != nil {
			return mutationir.Node{}, nil, err
		}
	}
	input := nestedNodeInput(node)
	input.Operation, input.Relation, input.RelationPosition, input.Branch = mutationir.Update, policyir.RelationID{}, nil, mutationir.MainBranch
	input.ScalarOperations = nil
	slot := node.Ordinal()
	if source, ok := node.RuntimeSourceID(); ok {
		slot = source
	}
	operations, err := transaction.runtimeValues.applyOperations(node.ModelID(), mutationbind.InputUpdate, operations, transaction.app.registry, slot)
	if err != nil {
		return mutationir.Node{}, nil, err
	}
	for _, operation := range operations {
		if !operation.RuntimeOwned() {
			continue
		}
		present := false
		for _, field := range input.InfluencingFields {
			present = present || field == operation.FieldID()
		}
		if !present {
			input.InfluencingFields = append(input.InfluencingFields, operation.FieldID())
		}
	}
	identity, present := work.Identity()
	if !present {
		return mutationir.Node{}, nil, fmt.Errorf("P4_RUNTIME_NESTED: membership owner identity is absent")
	}
	selectors := make([]mutationir.SelectorValue, len(identity.Components()))
	for index, component := range identity.Components() {
		value, ok := component.PolicyValue()
		if !ok {
			return mutationir.Node{}, nil, fmt.Errorf("P4_RUNTIME_NESTED: membership identity value is absent")
		}
		selectors[index], _ = mutationir.NewSelectorValue(component.FieldID(), value)
	}
	target, err := mutationir.NewTarget(node.ModelID(), identity.KeyID(), selectors, nil)
	if err != nil {
		return mutationir.Node{}, nil, err
	}
	input.Target = &target
	graph, err := mutationir.NewGraph(input)
	if err != nil {
		return mutationir.Node{}, nil, err
	}
	value, _ := graph.Root()
	return value, operations, nil
}

func (transaction *systemNestedTransaction[P, A]) runtimeOwnedOperations(node mutationir.Node, request mutationnested.ApplyRequest) ([]mutationir.ScalarOperation, error) {
	dependencyOperations, err := transaction.sourceDependencyOperations(node, request.Dependencies())
	if err != nil {
		return nil, err
	}
	position, ok := node.RelationPosition()
	if !ok || node.Operation() != mutationir.Create {
		return dependencyOperations, nil
	}
	anchorApplied, ok := request.RelationAnchor()
	if !ok {
		return nil, nil
	}
	anchor, err := nestedAppliedRow(anchorApplied)
	if err != nil {
		return nil, err
	}
	endpoint, ok := transaction.app.registry.RelationEndpoint(golem.ModelID(position.ParentModelID()), golem.FieldID(position.FieldID()), golem.RelationID(position.RelationID()))
	if !ok || endpoint.Role() != compilerir.RelationInverse {
		return nil, nil
	}
	resolver := policysql.SchemaResolver(transaction.app.registry)
	result := append([]mutationir.ScalarOperation(nil), dependencyOperations...)
	for _, pair := range endpoint.Correlation() {
		cell, present := anchor.Cell(policyir.FieldID(pair.ParentFieldID()))
		if !present || cell.IsNull() {
			return nil, fmt.Errorf("P4_RUNTIME_NESTED: anchor correlation is absent")
		}
		value, _ := cell.PolicyValue()
		field, present := resolver.Field(transaction.app.provider, node.ModelID(), policyir.FieldID(pair.ChildFieldID()))
		if !present {
			return nil, fmt.Errorf("P4_RUNTIME_NESTED: child correlation field is absent")
		}
		operation, err := mutationir.NewSet(policyir.FieldID(pair.ChildFieldID()), field.Type, value)
		if err != nil {
			return nil, err
		}
		result = append(result, operation)
	}
	return result, nil
}

func (transaction *systemNestedTransaction[P, A]) sourceDependencyOperations(node mutationir.Node, dependencies []mutationnested.AppliedNode) ([]mutationir.ScalarOperation, error) {
	if len(dependencies) == 0 {
		return nil, nil
	}
	resolver := policysql.SchemaResolver(transaction.app.registry)
	var result []mutationir.ScalarOperation
	seen := make(map[policyir.FieldID]struct{})
	for _, dependency := range dependencies {
		position, ok := dependency.Node().RelationPosition()
		if !ok || position.ParentModelID() != node.ModelID() {
			return nil, fmt.Errorf("P4_RUNTIME_NESTED: source dependency position does not belong to its parent")
		}
		endpoint, ok := transaction.app.registry.RelationEndpoint(golem.ModelID(position.ParentModelID()), golem.FieldID(position.FieldID()), golem.RelationID(position.RelationID()))
		if !ok || endpoint.Role() != compilerir.RelationSource {
			return nil, fmt.Errorf("P4_RUNTIME_NESTED: source dependency endpoint is absent or inverse")
		}
		row, err := nestedAppliedRow(dependency)
		if err != nil {
			return nil, err
		}
		for _, pair := range endpoint.Correlation() {
			local, remote := policyir.FieldID(pair.ParentFieldID()), policyir.FieldID(pair.ChildFieldID())
			if _, duplicate := seen[local]; duplicate {
				return nil, fmt.Errorf("P4_RUNTIME_NESTED: source dependencies overlap field %x", local)
			}
			cell, present := row.Cell(remote)
			if !present || cell.IsNull() {
				return nil, fmt.Errorf("P4_RUNTIME_NESTED: source dependency correlation value is absent")
			}
			field, present := resolver.Field(transaction.app.provider, node.ModelID(), local)
			if !present {
				return nil, fmt.Errorf("P4_RUNTIME_NESTED: source dependency local field is absent")
			}
			value, _ := cell.PolicyValue()
			operation, operationErr := mutationir.NewRuntimeSet(local, field.Type, value)
			if operationErr != nil {
				return nil, operationErr
			}
			seen[local] = struct{}{}
			result = append(result, operation)
		}
	}
	return result, nil
}

func nestedAppliedRow(applied mutationnested.AppliedNode) (mutationdecode.Row, error) {
	if row, ok := applied.Result().After(); ok {
		return row, nil
	}
	if row, ok := applied.Result().Before(); ok {
		return row, nil
	}
	return mutationdecode.Row{}, fmt.Errorf("P4_RUNTIME_NESTED: applied row image is absent")
}

func (transaction *systemNestedTransaction[P, A]) orderNestedFacts(applied []mutationnested.AppliedNode) error {
	if transaction.graphFactOrder == nil {
		return nil
	}
	checkpoint := *transaction.graphFactOrder
	state, err := transaction.binding.mutationState()
	if err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if checkpoint.start > len(state.facts) {
		return fmt.Errorf("P4_RUNTIME_NESTED_FACT: graph fact checkpoint is stale")
	}
	rows := append([]mutationfact.OutboxRow(nil), state.facts[checkpoint.start:]...)
	envelopes := make([]mutationfact.Envelope, len(rows))
	for index := range rows {
		envelopes[index], err = decodeRuntimeMutationFact(transaction.app.registry, rows[index])
		if err != nil {
			return err
		}
	}
	used := make([]bool, len(rows))
	ordered := make([]mutationfact.OutboxRow, 0, len(rows))
	for _, value := range applied {
		requirement := value.Node().Fact()
		action, enabled := requirement.Action()
		if !enabled {
			continue
		}
		if value.Node().Operation() == mutationir.UpdateMany || value.Node().Operation() == mutationir.DeleteMany {
			batchRows, ok := value.Work().BatchRows()
			if !ok {
				return fmt.Errorf("P4_RUNTIME_NESTED_FACT: batch fact rows are absent")
			}
			identityFields := requirement.BeforeIdentity()
			for _, batchRow := range batchRows {
				expected, identityErr := mutationdecode.ExtractOrderedIdentity(transaction.app.registry, batchRow, identityFields)
				if identityErr != nil {
					return identityErr
				}
				index, matched, matchErr := nextNestedFactIndex(envelopes, used, value.Node().ModelID(), action, expected)
				if matchErr != nil {
					return fmt.Errorf("P4_RUNTIME_NESTED_FACT: batch node %d: %w", value.Node().Ordinal(), matchErr)
				}
				if !matched {
					return fmt.Errorf("P4_RUNTIME_NESTED_FACT: batch node %d has no buffered fact for an applied row", value.Node().Ordinal())
				}
				ordered, used[index] = append(ordered, rows[index]), true
			}
			continue
		}
		expected, identityErr := nestedFactIdentity(transaction.app.registry, value, requirement, action)
		if identityErr != nil {
			return identityErr
		}
		index, matched, matchErr := nextNestedFactIndex(envelopes, used, value.Node().ModelID(), action, expected)
		if matchErr != nil {
			return fmt.Errorf("P4_RUNTIME_NESTED_FACT: node %d: %w", value.Node().Ordinal(), matchErr)
		}
		if !matched {
			return fmt.Errorf("P4_RUNTIME_NESTED_FACT: node %d has no buffered fact for its applied row", value.Node().Ordinal())
		}
		ordered, used[index] = append(ordered, rows[index]), true
	}
	// Unused rows are legitimate when a before hook starts an additional
	// mutation in the same transaction. They are not members of this graph's
	// applied-node list and retain their relative production order after the
	// graph-owned rows. Repeated model/action/identity facts are intentionally
	// matched first-unused only after nextNestedFactIndex proves that every
	// matching candidate has equivalent V1 semantic payload. Event identity,
	// allocation time, and the ordinal being rewritten are allocation metadata,
	// not a hidden association with a logical graph node.
	for index := range rows {
		if !used[index] {
			ordered = append(ordered, rows[index])
		}
	}
	for index := range ordered {
		envelope, decodeErr := decodeRuntimeMutationFact(transaction.app.registry, ordered[index])
		if decodeErr != nil {
			return decodeErr
		}
		envelope, decodeErr = envelope.WithTransactionOrdinal(checkpoint.ordinal + uint32(index) + 1)
		if decodeErr != nil {
			return decodeErr
		}
		metadata, encodeErr := mutationfact.Encode(envelope)
		if encodeErr != nil {
			return encodeErr
		}
		ordered[index].TransactionOrdinal = int64(checkpoint.ordinal) + int64(index) + 1
		ordered[index].Metadata = metadata
	}
	copy(state.facts[checkpoint.start:], ordered)
	state.ordinal = checkpoint.ordinal + uint32(len(ordered))
	return nil
}

// nextNestedFactIndex selects the stable first unused fact for one applied row.
// Multiple candidates are permitted only when their complete V1 semantic
// payloads are equivalent. That makes repeated model/action/identity facts
// deterministic without inventing a node tag in the public fact ABI, while a
// future codec payload (or a private delete snapshot) cannot be silently
// attached to the wrong logical node.
func nextNestedFactIndex(envelopes []mutationfact.Envelope, used []bool, model policyir.ModelID, action mutationir.FactAction, expected mutationdecode.Identity) (int, bool, error) {
	if len(envelopes) != len(used) {
		return 0, false, fmt.Errorf("fact inventory and usage bitmap differ")
	}
	selected := -1
	for index, envelope := range envelopes {
		if used[index] || envelope.ModelID() != model || envelope.Action() != action || !envelopeMatchesIdentity(envelope, action, expected) {
			continue
		}
		if selected < 0 {
			selected = index
			continue
		}
		if !nestedFactOrderingEquivalent(envelopes[selected], envelope) {
			return 0, false, fmt.Errorf("ambiguous non-equivalent buffered facts share model, action, and ordered identity")
		}
	}
	return selected, selected >= 0, nil
}

// nestedFactOrderingEquivalent compares everything in the V1 fact that
// describes the mutation itself. Fresh event IDs, transaction ordinals, and
// recorded-at timestamps are allocation/transport metadata; the latter is not
// part of Envelope. All identity and private snapshot content must agree.
func nestedFactOrderingEquivalent(left, right mutationfact.Envelope) bool {
	if left.Generation() != right.Generation() || left.ModelID() != right.ModelID() || left.Action() != right.Action() || left.CausationID() != right.CausationID() {
		return false
	}
	leftBefore, leftBeforeOK := left.BeforeIdentity()
	rightBefore, rightBeforeOK := right.BeforeIdentity()
	if leftBeforeOK != rightBeforeOK || leftBeforeOK && !nestedFactIdentityEqual(leftBefore, rightBefore) {
		return false
	}
	leftAfter, leftAfterOK := left.AfterIdentity()
	rightAfter, rightAfterOK := right.AfterIdentity()
	if leftAfterOK != rightAfterOK || leftAfterOK && !nestedFactIdentityEqual(leftAfter, rightAfter) {
		return false
	}
	leftFields, rightFields := left.PrivateDeleteSnapshotFields(), right.PrivateDeleteSnapshotFields()
	if len(leftFields) != len(rightFields) {
		return false
	}
	for index := range leftFields {
		if leftFields[index] != rightFields[index] {
			return false
		}
	}
	leftSnapshot, leftSnapshotOK := left.PrivateDeleteSnapshot()
	rightSnapshot, rightSnapshotOK := right.PrivateDeleteSnapshot()
	return leftSnapshotOK == rightSnapshotOK && (!leftSnapshotOK || mutationdecode.EqualRow(leftSnapshot, rightSnapshot))
}

func nestedFactIdentity(registry *schema.Registry, applied mutationnested.AppliedNode, requirement mutationir.FactRequirement, action mutationir.FactAction) (mutationdecode.Identity, error) {
	var row mutationdecode.Row
	var ok bool
	fields := requirement.AfterIdentity()
	if action == mutationir.FactDeleted {
		row, ok = applied.Result().Before()
		fields = requirement.BeforeIdentity()
	} else {
		row, ok = applied.Result().After()
	}
	if !ok {
		return mutationdecode.Identity{}, fmt.Errorf("P4_RUNTIME_NESTED_FACT: scalar fact image is absent")
	}
	return mutationdecode.ExtractOrderedIdentity(registry, row, fields)
}

func envelopeMatchesIdentity(envelope mutationfact.Envelope, action mutationir.FactAction, expected mutationdecode.Identity) bool {
	actual, ok := envelope.AfterIdentity()
	if action == mutationir.FactDeleted {
		actual, ok = envelope.BeforeIdentity()
	}
	return ok && nestedFactIdentityEqual(actual, expected)
}

func nestedFactIdentityEqual(left, right mutationdecode.Identity) bool {
	if left.KeyID() != right.KeyID() {
		return false
	}
	leftComponents, rightComponents := left.Components(), right.Components()
	if len(leftComponents) != len(rightComponents) {
		return false
	}
	for index := range leftComponents {
		if leftComponents[index].FieldID() != rightComponents[index].FieldID() || leftComponents[index].IsNull() != rightComponents[index].IsNull() {
			return false
		}
		leftValue, leftOK := leftComponents[index].PolicyValue()
		rightValue, rightOK := rightComponents[index].PolicyValue()
		if leftOK != rightOK || leftOK && !mutationdecode.EqualValue(leftValue, rightValue) {
			return false
		}
	}
	return true
}

func (transaction *systemNestedTransaction[P, A]) FinalizeNested(ctx context.Context, applied []mutationnested.AppliedNode) error {
	if err := transaction.orderNestedFacts(applied); err != nil {
		return err
	}
	if transaction.stance != mutationir.Caller {
		return nil
	}
	for _, value := range applied {
		node := value.Node()
		if node.Operation() != mutationir.Create && node.Operation() != mutationir.Update {
			continue
		}
		conditions := make([]policyir.Condition, 0, len(node.FieldAuthorizations())+1)
		if condition, ok := node.RowPostcondition(); ok {
			conditions = append(conditions, condition)
		}
		if node.Operation() == mutationir.Create {
			for _, authorization := range node.FieldAuthorizations() {
				conditions = append(conditions, authorization.Condition())
			}
		}
		row, ok := value.Result().After()
		if !ok {
			return fmt.Errorf("P4_RUNTIME_NESTED_POLICY: created row image is absent")
		}
		verification, err := mutationsql.RenderPersistedVerification(node, row, conditions, transaction.app.registry, transaction.app.provider, transaction.app.capabilities)
		if err != nil {
			return err
		}
		recordQueryerStatement(ctx, transaction.queryer, transaction.binding.observation)
		rows, err := transaction.queryer.QueryxContext(ctx, verification.SQL(), verification.Args()...)
		if err != nil {
			return fmt.Errorf("P4_RUNTIME_NESTED_POLICY: verify completed create graph: %w", err)
		}
		allowed := rows.Next()
		iterationErr := rows.Err()
		if closeErr := rows.Close(); closeErr != nil {
			return closeErr
		}
		if iterationErr != nil {
			return fmt.Errorf("P4_RUNTIME_NESTED_POLICY: read completed graph verification: %w", iterationErr)
		}
		if !allowed {
			operation := scalarMutationOperationName(node.Operation())
			cause := fmt.Errorf("P4_RUNTIME_NESTED_POLICY: completed %s graph is not authorized", operation)
			return golem.RuntimeOperationError(golem.CodeForbidden, operation, golem.ModelID(node.ModelID()), golem.FieldID{}, "mutation is not authorized", cause)
		}
	}
	return nil
}

func (transaction *systemNestedTransaction[P, A]) VerifyNested(ctx context.Context, applied mutationnested.AppliedNode) error {
	if result, ok := applied.Result().RuntimeHookResult(); ok && transaction.hooks != nil {
		rootSuppressed := transaction.suppressRootHooks && applied.Node().Ordinal() == 0 && !applied.Node().IsRuntimeReplacement() && applied.Node().ModelID() == transaction.rootModel
		if !rootSuppressed {
			if err := transaction.hooks.observeResult(ctx, transaction.binding, result); err != nil {
				return err
			}
		}
	}
	if transaction.verify != nil {
		return transaction.verify(ctx, transaction.binding, applied)
	}
	return nil
}
func (transaction *systemNestedTransaction[P, A]) CommitNested(ctx context.Context) error {
	for index := len(transaction.guardOrder) - 1; index >= 0; index-- {
		guard := transaction.guards[transaction.guardOrder[index]]
		if cleanup, present := guard.CleanupStatement(); present {
			if err := executeNestedGuardStatement(ctx, transaction.queryer, cleanup); err != nil {
				return err
			}
		}
	}
	return transaction.commit(ctx)
}
func (transaction *systemNestedTransaction[P, A]) RollbackNested(ctx context.Context) error {
	return transaction.rollback(ctx)
}
