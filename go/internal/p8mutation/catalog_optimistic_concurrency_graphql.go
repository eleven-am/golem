package p8mutation

import "time"

// optimisticConcurrencyGraphQLMutations stays isolated until the complete
// GraphQL/generated ABI publication is coordinated. These mutants attack only
// the GraphQL claim, authored-input, custom-input, nested-surface, and frozen
// dispatch boundaries; Catalog deliberately does not aggregate them yet.
func optimisticConcurrencyGraphQLMutations() []Mutation {
	schemaGate := Gate{Directory: "go", Package: "./internal/graphql/schema", Test: "TestOptimisticConcurrencyGraphQLSchemaIsExactAndUnsafeSurfacesAreAbsent"}
	emptyNestedGate := Gate{Directory: "go", Package: "./internal/graphql/schema", Test: "TestVersionedInverseRelationWithNoLegalCreateBranchIsOmittedAtomically"}
	expectationGate := Gate{Directory: "go", Package: "./internal/graphql/operation", Test: "TestOptimisticConcurrencyGraphQLExpectationOneOfRejectsEveryInvalidShape"}
	selectorGate := Gate{Directory: "go", Package: "./internal/graphql/operation", Test: "TestVersionedCustomSelectorBindsWithoutManufacturingDeleteAuthority"}
	nestedMapGate := Gate{Directory: "go", Package: "./internal/graphql/operation", Test: "TestOldGraphQLNestedMapCannotMutateVersionedInverseOwner"}
	dispatchGate := Gate{Directory: "go", Package: "./runtime", Test: "TestOptimisticConcurrencyCallerGraphQLRuntimeDispatchUsesClosedClaims"}
	forgedGate := Gate{Directory: "go", Package: "./runtime", Test: "TestNonVersionedGraphQLRuntimeRejectsForgedConcurrencyClaim"}
	legacyGate := Gate{Directory: "go", Package: "./runtime", Test: "TestOptimisticConcurrencyCreateUpdateDeleteAndLegacyBypassSQLite"}
	extensionGate := Gate{Directory: "go", Package: "./internal/graphql/extension", Test: "TestOptimisticConcurrencyCustomUpdateManyInputIsRejectedThroughLists"}
	customGate := Gate{Directory: "go", Package: "./internal/graphql/custom", Test: "TestCustomRegistryRejectsVersionedUpdateManyInputNestedInList"}
	mutation := func(label, summary string, gate Gate, patches ...Patch) Mutation {
		return Mutation{Label: label, Summary: summary, Patches: patches, Gate: gate, Timeout: 3 * time.Minute}
	}
	return []Mutation{
		mutation("CONCURRENCY_GRAPHQL_EXPOSES_AUTHORED_TOKEN", "restore the runtime-owned token to create and update-shaped GraphQL inputs", schemaGate,
			Patch{Path: "go/internal/graphql/schema/schema.go", Before: "if view.model.OptimisticConcurrency != nil && field.ID == *view.model.OptimisticConcurrency {", After: "if false && view.model.OptimisticConcurrency != nil && field.ID == *view.model.OptimisticConcurrency {"},
			Patch{Path: "go/internal/graphql/schema/schema.go", Before: "if containsFieldID(names.excluded, field.ID) {\n\t\t\tcontinue\n\t\t}\n\t\tif model.OptimisticConcurrency != nil && field.ID == *model.OptimisticConcurrency {", After: "if containsFieldID(names.excluded, field.ID) {\n\t\t\tcontinue\n\t\t}\n\t\tif false && model.OptimisticConcurrency != nil && field.ID == *model.OptimisticConcurrency {"}),
		mutation("CONCURRENCY_GRAPHQL_UPDATE_OMITS_EXPECTED_VERSION", "publish a versioned update root without its required BigInt claim", schemaGate,
			Patch{Path: "go/internal/graphql/schema/schema.go", Before: `fmt.Fprintf(&mutation, "  %s(where: %sWhereUniqueInput!, expectedVersion: BigInt!, data: %sUpdateInput!): %s!\n", roots.Update, name, name, name)`, After: `fmt.Fprintf(&mutation, "  %s(where: %sWhereUniqueInput!, data: %sUpdateInput!): %s!\n", roots.Update, name, name, name)`}),
		mutation("CONCURRENCY_GRAPHQL_DELETE_OMITS_EXPECTED_VERSION", "publish a versioned delete root without its required BigInt claim", schemaGate,
			Patch{Path: "go/internal/graphql/schema/schema.go", Before: `fmt.Fprintf(&mutation, "  %s(where: %sWhereUniqueInput!, expectedVersion: BigInt!): %s!\n", roots.Delete, name, name)`, After: `fmt.Fprintf(&mutation, "  %s(where: %sWhereUniqueInput!): %s!\n", roots.Delete, name, name)`}),
		mutation("CONCURRENCY_GRAPHQL_REEMITS_BATCH_ROOTS", "restore updateMany and deleteMany roots for a versioned model", schemaGate,
			Patch{Path: "go/internal/graphql/schema/schema.go", Before: "if !versioned && enabled[ir.OperationUpdateMany] && hasUpdateMany {", After: "if enabled[ir.OperationUpdateMany] && hasUpdateMany {"},
			Patch{Path: "go/internal/graphql/schema/schema.go", Before: "if !versioned && enabled[ir.OperationDeleteMany] {", After: "if enabled[ir.OperationDeleteMany] {"}),
		mutation("CONCURRENCY_GRAPHQL_ACCEPTS_MALFORMED_EXPECTATION", "accept a multi-member or empty upsert expectation object", expectationGate,
			Patch{Path: "go/internal/graphql/mutation/input_map.go", Before: "if !ok || len(object) != 1 {", After: "if !ok {"}),
		mutation("CONCURRENCY_GRAPHQL_ACCEPTS_AUTHORED_TOKEN_MAP", "accept the runtime-owned token through a custom create/update map", expectationGate,
			Patch{Path: "go/internal/graphql/mutation/input_map.go", Before: "if concurrency, enabled := state.binder.versioned[modelID]; enabled && field.ID == concurrency {", After: "if concurrency, enabled := state.binder.versioned[modelID]; false && enabled && field.ID == concurrency {"}),
		mutation("CONCURRENCY_GRAPHQL_REQUEST_ALIASES_EXISTING_CLAIM", "retain the caller's mutable claim pointer instead of cloning its value", dispatchGate,
			Patch{Path: "go/golem/mutation_execution_bridge.go", Before: "value := *existing\n\t\tresult.existing = &value", After: "result.existing = existing"}),
		mutation("CONCURRENCY_GRAPHQL_REQUEST_ALIASES_UPSERT_EXPECTATION", "retain the caller's mutable upsert-expectation pointer instead of cloning its value", dispatchGate,
			Patch{Path: "go/golem/mutation_execution_bridge.go", Before: "value := *expectation\n\t\tresult.expectation = &value", After: "result.expectation = expectation"}),
		mutation("CONCURRENCY_GRAPHQL_NONVERSIONED_CLAIM_GAINS_AUTHORITY", "accept a forged concurrency claim on a non-versioned model", forgedGate,
			Patch{Path: "go/runtime/graphql_mutation.go", Before: "if hasExisting || hasExpectation {", After: "if false && (hasExisting || hasExpectation) {"}),
		mutation("CONCURRENCY_GRAPHQL_CUSTOM_LIST_ACCEPTS_UPDATE_MANY", "allow a custom list element to name a versioned UpdateManyInput", extensionGate,
			Patch{Path: "go/internal/graphql/extension/normalize.go", Before: "if value.Kind == ir.GraphQLTypeUpdateManyInput && context.versioned[value.Name] {", After: "if false && value.Kind == ir.GraphQLTypeUpdateManyInput && context.versioned[value.Name] {"}),
		mutation("CONCURRENCY_GRAPHQL_FORGED_CUSTOM_ACCEPTS_UPDATE_MANY", "allow a pre-normalized custom contract to name a versioned UpdateManyInput", customGate,
			Patch{Path: "go/internal/graphql/custom/custom.go", Before: "if typ.Kind == compilerir.GraphQLTypeUpdateManyInput && registry.versioned[typ.Name] {", After: "if false && typ.Kind == compilerir.GraphQLTypeUpdateManyInput && registry.versioned[typ.Name] {"}),
		mutation("CONCURRENCY_GRAPHQL_REEMITS_UNSAFE_INVERSE_MEMBERSHIP", "restore connect/disconnect membership writes for a versioned inverse owner", nestedMapGate,
			Patch{Path: "go/internal/graphql/mutation/input_map.go", Before: "if targetVersioned && (writesExistingTarget || writesVersionedInverseOwner) {", After: "if false && targetVersioned && (writesExistingTarget || writesVersionedInverseOwner) {"}),
		mutation("CONCURRENCY_GRAPHQL_CUSTOM_SELECTOR_MANUFACTURES_DELETE", "bind a custom selector by manufacturing a versioned delete request", selectorGate,
			Patch{Path: "go/internal/graphql/operation/compile.go", Before: "return c.mutation.Target(modelID, raw)", After: "request, err := c.mutation.LowerValues(graphqlmutation.Delete, modelID, map[string]any{\"where\": raw}, nil)\n\t\tif err != nil {\n\t\t\treturn nil, err\n\t\t}\n\t\tvalue, ok := request.Target()\n\t\tif !ok {\n\t\t\treturn nil, fmt.Errorf(\"selector target is absent\")\n\t\t}\n\t\treturn value, nil"}),
		mutation("CONCURRENCY_GRAPHQL_EMITS_EMPTY_NESTED_HELPER", "retain an inverse relation whose versioned owner has no legal create branch", emptyNestedGate,
			Patch{Path: "go/internal/graphql/schema/schema.go", Before: "capabilities.create = capabilities.create && (targetCreate || membershipAvailable)", After: "capabilities.create = capabilities.create"}),
		mutation("CONCURRENCY_GRAPHQL_EMITS_ORPHAN_ROOT_UPDATE_HELPERS", "emit unreachable relation-update helper types for a versioned root", schemaGate,
			Patch{Path: "go/internal/graphql/schema/schema.go", Before: "capabilities.update = capabilities.update && model.OptimisticConcurrency == nil && (targetCreate || membershipAvailable || !targetVersioned && (targetUpdate || targetUpdateMany || len(targetContract.Selectors) != 0))", After: "capabilities.update = capabilities.update && (targetCreate || membershipAvailable || !targetVersioned && (targetUpdate || targetUpdateMany || len(targetContract.Selectors) != 0))"}),
		mutation("CONCURRENCY_GRAPHQL_LEGACY_REQUEST_REGAINS_DISPATCH", "route an old model-erased request through generic mutation execution on a versioned model", legacyGate,
			Patch{Path: "go/runtime/graphql_mutation.go", Before: "return true, golem.RuntimeOperationError(golem.CodeBadUserInput, operation, model, golem.FieldID{}, \"mutation request is invalid\", fmt.Errorf(\"optimistic-concurrency request has no exact operation claim\"))", After: "return false, nil // mutant: old request regains generic dispatch"},
			Patch{Path: "go/runtime/mutation_concurrency.go", Before: "if _, enabled := fact.OptimisticConcurrency(); !enabled {\n\t\treturn nil\n\t}\n\treturn golem.RuntimeOperationError(golem.CodeBadUserInput, operation, model, golem.FieldID{}, \"mutation request is invalid\", fmt.Errorf(\"optimistic-concurrency mutation requires an explicit expectation\"))", After: "if _, enabled := fact.OptimisticConcurrency(); enabled {\n\t\treturn nil // mutant: generic mutation gains versioned authority\n\t}\n\treturn nil"},
			Patch{Path: "go/internal/mutation/sql/render.go", Before: "if !versioned {\n\t\t\t\treturn Program{}, fail(CodeUnsupported, node.ModelID(), value, \"optimistic-concurrency mutation requires an explicit expectation\", nil)\n\t\t\t}", After: "if false && !versioned {\n\t\t\t\treturn Program{}, fail(CodeUnsupported, node.ModelID(), value, \"optimistic-concurrency mutation requires an explicit expectation\", nil)\n\t\t\t}"}),
	}
}
