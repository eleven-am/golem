package p8mutation

import "time"

// optimisticConcurrencyGeneratedGoMutations is isolated from the release-wide
// Catalog. It freezes only the generated public Go surface and the private
// declaration-discovery shell; runtime CAS, GraphQL, physical, migrations, and
// compatibility remain owned by their separate slices.
func optimisticConcurrencyGeneratedGoMutations() []Mutation {
	modelGate := Gate{Directory: "go", Package: "./internal/codegen/model", Test: "TestOptimisticConcurrencyModelAuthoredBatchAndUnsafeNestedSurfacesAreAbsent"}
	registryGate := Gate{Directory: "go", Package: "./internal/codegen/registry", Test: "TestOptimisticConcurrencyDeclarationAndGeneratedSurfaceAreExact"}
	bootstrapGate := Gate{Directory: "go", Package: "./internal/generate/pipeline", Test: "TestOptimisticConcurrencyDeclarationDiscoveryAndFinalExactRegistryBothCompile"}
	return []Mutation{
		{
			Label: "CONCURRENCY_GENERATED_REEMITS_AUTHORED_SETTER", Summary: "restore authored create/set/arithmetic authority on the runtime-owned concurrency field",
			Patches: []Patch{{Path: "go/internal/codegen/model/emit.go", Before: "if model.OptimisticConcurrency != nil && *model.OptimisticConcurrency == field.ID {\n\t\treturn result\n\t}", After: "if model.OptimisticConcurrency != nil && *model.OptimisticConcurrency == field.ID && false {\n\t\treturn result\n\t}"}},
			Gate:    modelGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "CONCURRENCY_GENERATED_REEMITS_ROOT_BATCH", Summary: "restore versioned UpdateMany/DeleteMany input and hook aliases together with the root batch constructor",
			Patches: []Patch{
				{Path: "go/internal/codegen/model/emit.go", Before: "if !modelUsesOptimisticConcurrency(model) {\n\t\tfmt.Fprintf(body, \"type %sUpdateManyInput = golem.UpdateManyInput[%s]\\n\", model.Go.Name, model.Go.Name)", After: "if true {\n\t\tfmt.Fprintf(body, \"type %sUpdateManyInput = golem.UpdateManyInput[%s]\\n\", model.Go.Name, model.Go.Name)"},
				{Path: "go/internal/codegen/model/emit.go", Before: "if !modelUsesOptimisticConcurrency(model) {\n\t\t\tfmt.Fprintf(&body, \"func (%s) UpdateMany(first golem.UpdateManyValue[%s]", After: "if true {\n\t\t\tfmt.Fprintf(&body, \"func (%s) UpdateMany(first golem.UpdateManyValue[%s]"},
			},
			Gate: modelGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "CONCURRENCY_GENERATED_ONE_FAMILY_OMITS_EXPECTATION", Summary: "emit the classic System mutation ABI for a versioned model while the other three families remain versioned",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: "emitMutationClientMethods(source, \"System\", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor, createInput, updateInput, updateManyInput, versioned)", After: "emitMutationClientMethods(source, \"System\", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor, createInput, updateInput, updateManyInput, false)"}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "CONCURRENCY_GENERATED_CALLS_LEGACY_RUNTIME", Summary: "keep the expected-version parameter but route generated updates through the legacy unversioned runtime entrypoint",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: "{ return %s.%sUpdateVersioned(ctx, client.runtime, %s, target, expected, input, projection...) }", After: "{ _ = expected; return %s.%sUpdate(ctx, client.runtime, %s, target, input, projection...) }"}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "CONCURRENCY_GENERATED_REEMITS_UNSAFE_NESTED_TARGET_UPDATE", Summary: "allow a non-versioned parent to mutate an existing versioned nested target without a per-node expectation",
			Patches: []Patch{{Path: "go/internal/codegen/model/emit.go", Before: "targetExistingSafe := !modelUsesOptimisticConcurrency(target)", After: "targetExistingSafe := true"}},
			Gate:    modelGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "CONCURRENCY_GENERATED_REEMITS_VERSIONED_ROOT_RELATION_UPDATE", Summary: "restore relation update values on a versioned root even though the runtime rejects relation-bearing versioned input",
			Patches: []Patch{{Path: "go/internal/codegen/model/emit.go", Before: "if !capabilities.update || modelUsesOptimisticConcurrency(model) {\n\t\treturn nil\n\t}", After: "if !capabilities.update {\n\t\treturn nil\n\t}"}},
			Gate:    modelGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "CONCURRENCY_GENERATED_DISCOVERY_SHELL_LOSES_ESCAPE", Summary: "use the classic pre-declaration shell and reject same-package helpers that already use the versioned final ABI",
			Patches: []Patch{{Path: "go/internal/compiler/compile/compile.go", Before: "Model: resolved.Compilation.Model, Contract: resolved.Compilation.Contract, DeclarationDiscovery: true", After: "Model: resolved.Compilation.Model, Contract: resolved.Compilation.Contract, DeclarationDiscovery: false"}},
			Gate:    bootstrapGate, Timeout: 3 * time.Minute,
		},
	}
}
