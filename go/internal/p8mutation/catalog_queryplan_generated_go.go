package p8mutation

import "time"

// queryPlanGeneratedGoMutations is intentionally isolated from Catalog. It
// freezes only the generated Caller convenience surface and its private
// declaration-discovery shell. Compatibility corpus ownership remains with
// the release compatibility slice.
func queryPlanGeneratedGoMutations() []Mutation {
	registryGate := Gate{Directory: "go", Package: "./internal/codegen/registry", Test: "TestGeneratedQueryPlanSurfaceIsCallerOnlyAndExact"}
	bootstrapGate := Gate{Directory: "go", Package: "./internal/generate/pipeline", Test: "TestQueryPlanDeclarationDiscoverySupersetAndFinalExactRegistryBothCompile"}
	return []Mutation{
		{
			Label: "QUERYPLAN_GENERATED_OMITS_CALLER_FIND_MANY", Summary: "omit the universal generated Caller ExplainFindMany convenience method",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: `fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainFindMany`, After: `fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainFindManyOmitted`}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_FIND_MANY_CALLS_COUNT", Summary: "route generated ExplainFindMany through the count planner",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: "return %s.CallerExplainFindMany(ctx, client.runtime, %s, options...)", After: "return %s.CallerExplainCount(ctx, client.runtime, %s, options...)"}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_FIND_FIRST_CALLS_FIND_MANY", Summary: "route generated ExplainFindFirst through the find-many planner",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: "return %s.CallerExplainFindFirst(ctx, client.runtime, %s, options...)", After: "return %s.CallerExplainFindMany(ctx, client.runtime, %s, options...)"}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_FIND_UNIQUE_RETURNS_ZERO_REPORT", Summary: "replace generated ExplainFindUnique routing with a compiling zero report body",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: `fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainFindUnique(ctx %s.Context, selector %s.UniqueSelectorValue[%s], options ...%s.ReadOption[%s]) (%s.Report, error) { return %s.CallerExplainFindUnique(ctx, client.runtime, %s, selector, options...) }\n", goName, contextAlias, golemAlias, modelType, golemAlias, modelType, queryplanAlias, runtimeAlias, descriptor)`, After: `fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainFindUnique(ctx %s.Context, selector %s.UniqueSelectorValue[%s], options ...%s.ReadOption[%s]) (%s.Report, error) { return %s.Report{}, nil }\n", goName, contextAlias, golemAlias, modelType, golemAlias, modelType, queryplanAlias, queryplanAlias)`}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_COUNT_CALLS_FIND_MANY", Summary: "route generated ExplainCount through the find-many planner",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: "return %s.CallerExplainCount(ctx, client.runtime, %s, options...)", After: "return %s.CallerExplainFindMany(ctx, client.runtime, %s, options...)"}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_AGGREGATE_RETURNS_ZERO_REPORT", Summary: "replace generated ExplainAggregate routing with a compiling zero report body",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: `fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainAggregate(ctx %s.Context, request %s.AggregateRequest[%s]) (%s.Report, error) { return %s.CallerExplainAggregate(ctx, client.runtime, %s, request) }\n", goName, contextAlias, golemAlias, modelType, queryplanAlias, runtimeAlias, descriptor)`, After: `fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainAggregate(ctx %s.Context, request %s.AggregateRequest[%s]) (%s.Report, error) { return %s.Report{}, nil }\n", goName, contextAlias, golemAlias, modelType, queryplanAlias, queryplanAlias)`}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_GROUP_BY_RETURNS_ZERO_REPORT", Summary: "replace generated ExplainGroupBy routing with a compiling zero report body",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: `fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainGroupBy(ctx %s.Context, request %s.GroupRequest[%s]) (%s.Report, error) { return %s.CallerExplainGroupBy(ctx, client.runtime, %s, request) }\n", goName, contextAlias, golemAlias, modelType, queryplanAlias, runtimeAlias, descriptor)`, After: `fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainGroupBy(ctx %s.Context, request %s.GroupRequest[%s]) (%s.Report, error) { return %s.Report{}, nil }\n", goName, contextAlias, golemAlias, modelType, queryplanAlias, queryplanAlias)`}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_EXPOSES_SYSTEM", Summary: "expose ExplainFindMany on the generated System client",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: `fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainFindMany`, After: `fmt.Fprintf(source, "func (client System%sClient[P]) ExplainFindMany`}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_EXPOSES_CALLER_TX", Summary: "expose ExplainFindMany on the generated Caller transaction client",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: `fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainFindMany`, After: `fmt.Fprintf(source, "func (client CallerTx%sClient[P]) ExplainFindMany`}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_EXPOSES_MUTATION_EXPLAIN", Summary: "invent an ExplainCreate mutation convenience method",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: `fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainFindMany`, After: `fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainCreate`}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_OMITS_RELATION_GROUP", Summary: "omit ExplainRelationGroupBy despite a reviewed relation dimension",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: "descriptor, contractHasRelationDimensions(contract), contract.ScopedReads)", After: "descriptor, false, contract.ScopedReads)"}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_UNCONDITIONAL_RELATION_GROUP", Summary: "expose ExplainRelationGroupBy without a reviewed relation dimension",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: "descriptor, contractHasRelationDimensions(contract), contract.ScopedReads)", After: "descriptor, true, contract.ScopedReads)"}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_OMITS_SCOPED", Summary: "omit ExplainScoped despite scoped reads being enabled",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: "descriptor, contractHasRelationDimensions(contract), contract.ScopedReads)", After: "descriptor, contractHasRelationDimensions(contract), false)"}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_UNCONDITIONAL_SCOPED", Summary: "expose ExplainScoped without scoped reads being enabled",
			Patches: []Patch{{Path: "go/internal/codegen/registry/registry.go", Before: "descriptor, contractHasRelationDimensions(contract), contract.ScopedReads)", After: "descriptor, contractHasRelationDimensions(contract), true)"}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_DISCOVERY_SHELL_LOSES_SUPERSET", Summary: "use the final conditional shell before model declarations have been interpreted",
			Patches: []Patch{{Path: "go/internal/compiler/compile/compile.go", Before: "Contract: resolved.Compilation.Contract, DeclarationDiscovery: true", After: "Contract: resolved.Compilation.Contract, DeclarationDiscovery: false"}},
			Gate:    bootstrapGate, Timeout: 3 * time.Minute,
		},
		{
			Label: "QUERYPLAN_GENERATED_DISCOVERY_SHELL_LEAKS_CALLER_TX", Summary: "expose query-plan methods on CallerTx during declaration discovery",
			Patches: []Patch{{Path: "go/internal/codegen/registry/bootstrap.go", Before: `if prefix == "Caller" {`, After: `if prefix == "Caller" || declarationDiscovery {`}},
			Gate:    registryGate, Timeout: 3 * time.Minute,
		},
	}
}
