package runtime

import (
	"fmt"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/invalidationcontract"
	"github.com/eleven-am/golem/go/internal/operationargs"
	"github.com/eleven-am/golem/go/internal/policy/schema"
)

type invalidationRuntime struct {
	registry   *operationargs.Registry
	operations map[string]compilerir.CustomOperationContractIR
}

func newInvalidationRuntime[A any](bundle golem.SchemaBundle, bindings golem.ApplicationBindings[A]) (*invalidationRuntime, error) {
	compilation, err := schema.DecodeActive(bundle)
	if err != nil {
		return nil, err
	}
	if err := compilerir.ValidateGraphQLInvalidations(compilation.Contract); err != nil {
		return nil, fmt.Errorf("P7_INVALIDATION_CONFIG: %w", err)
	}
	operations := map[string]compilerir.CustomOperationContractIR{}
	expected := map[golem.RuntimeInvalidationBindingIdentity]bool{}
	for _, operation := range compilation.Contract.CustomOperations {
		if operation.Invalidation == nil {
			continue
		}
		operations[string(operation.ExtensionID)] = operation
		for _, dependency := range operation.Invalidation.Dependencies {
			id := golem.RuntimeInvalidationBindingIdentity{OperationID: string(operation.ExtensionID), DependencyID: string(dependency.ID), BinderName: dependency.Binder.PackagePath + "." + dependency.Binder.Name}
			expected[id] = true
			binding, present := bindings.Invalidation(id.OperationID, id.DependencyID)
			if !present || binding.Identity() != id {
				return nil, fmt.Errorf("P7_INVALIDATION_CONFIG: registered binder does not match the declared dependency")
			}
		}
	}
	inventory := bindings.InvalidationInventory()
	if len(inventory) != len(expected) {
		return nil, fmt.Errorf("P7_INVALIDATION_CONFIG: invalidation binding inventory differs from the contract")
	}
	for _, id := range inventory {
		if !expected[id] {
			return nil, fmt.Errorf("P7_INVALIDATION_CONFIG: undeclared invalidation binder")
		}
	}
	if len(operations) == 0 {
		return nil, nil
	}
	registry, err := operationargs.New(compilation)
	if err != nil {
		return nil, err
	}
	// Retain the registry's owned contracts, not borrowed input IR slices.
	for id := range operations {
		operation, _ := registry.Contract(compilerir.ExtensionID(id))
		operations[id] = operation
	}
	return &invalidationRuntime{registry: registry, operations: operations}, nil
}

func (runtime *invalidationRuntime) prepare(operationID string, supplied map[string]any) (compilerir.CustomOperationContractIR, operationargs.Request, []byte, error) {
	operation, present := runtime.operations[operationID]
	if !present || operation.Invalidation == nil || len(operation.Invalidation.Dependencies) > invalidationcontract.MaxDependencies {
		return operation, operationargs.Request{}, nil, fmt.Errorf("P7_INVALIDATION_OPERATION: operation has no registered dependencies")
	}
	request, err := runtime.registry.PrepareInvalidation(operation.ExtensionID, supplied)
	if err != nil {
		return operation, request, nil, err
	}
	var excluded []string
	if operation.Pagination != nil {
		excluded = []string{"take", "after"}
	}
	canonical, err := runtime.registry.CanonicalArguments(request, excluded...)
	return operation, request, canonical, err
}

func invalidationArguments(request operationargs.Request, page bool) map[string]any {
	result := map[string]any{}
	for _, argument := range request.Arguments() {
		if argument.Present() && (!page || argument.Name() != "take" && argument.Name() != "after") {
			result[argument.Name()] = argument.Value()
		}
	}
	return result
}
