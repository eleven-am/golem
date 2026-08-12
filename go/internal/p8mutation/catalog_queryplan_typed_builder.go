package p8mutation

import "time"

// queryPlanTypedBuilderMutations stays isolated until the shared catalog owner
// aggregates the completed query-plan assembly slice.
func queryPlanTypedBuilderMutations() []Mutation {
	gate := func(test string) Gate {
		return Gate{Directory: "go", Package: "./internal/queryplanbuild", Test: test}
	}
	return []Mutation{
		{
			Label: "QUERYPLAN_TYPED_BUILDER_ACCEPTS_UNBOUNDED_PARENT", Summary: "treat an absent typed root row bound as a zero-row bound",
			Patches: []Patch{{Path: "go/internal/queryplanbuild/build.go", Before: "\t\tif !ok {\n\t\t\treturn 0, false\n\t\t}", After: "\t\tif !ok {\n\t\t\treturn 0, true\n\t\t}"}},
			Gate:    gate("TestDeferredBatchZeroParentAndUnboundedParentRefusalReturnNoPartialReport"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_TYPED_BUILDER_COUNTS_CAP_PROBE_ROW", Summary: "use the planner's cap-plus-one overflow probe as a successful parent row",
			Patches: []Patch{{Path: "go/internal/queryplanbuild/build.go", Before: "\t\tif limit := plan.ResultLimit(); limit > 0 {\n\t\t\treturn uint64(limit), true\n\t\t}", After: "\t\t_ = plan.ResultLimit()"}},
			Gate:    gate("TestConfiguredRootLimitUsesSuccessfulRowBoundNotCapPlusOneProbe"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_TYPED_BUILDER_INVENTS_BATCH_CAPACITY", Summary: "increase the exact renderer-owned batch key capacity",
			Patches: []Patch{{Path: "go/internal/queryplanbuild/build.go", Before: "\tcapacity, err := readsql.BatchKeyCapacity(childPlan, endpoint, input.Registry, input.Provider, input.Capabilities)", After: "\tcapacity, err := readsql.BatchKeyCapacity(childPlan, endpoint, input.Registry, input.Provider, input.Capabilities)\n\tcapacity++"}},
			Gate:    gate("TestCorrelatedProviderFactAndDeferredNestedHydrationAreTruthful"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_TYPED_BUILDER_OMITS_PRIVATE_HYDRATIONS", Summary: "walk public relation branches but omit private policy hydration branches",
			Patches: []Patch{{Path: "go/internal/queryplanbuild/build.go", Before: "\t\tfor _, hydrationCursor := range current.cursor.Hydrations() {", After: "\t\tfor _, hydrationCursor := range []readplan.QueryPlanRelation{} {"}},
			Gate:    gate("TestTypedTraversalWalksPublicRelationsAndPrivateHydrationsIteratively"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_TYPED_BUILDER_CLAIMS_DEFERRED_ACCESS", Summary: "claim a physical access path for a branch that cannot be provider-planned without parent keys",
			Patches: []Patch{{Path: "go/internal/queryplanbuild/build.go", Before: "\t\t\tKind: \"deferredBatch\", Access: \"none\",", After: "\t\t\tKind: \"deferredBatch\", Access: \"fullScan\","}},
			Gate:    gate("TestCorrelatedProviderFactAndDeferredNestedHydrationAreTruthful"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_TYPED_BUILDER_REPLANS_CORRELATED_BRANCH", Summary: "replace a provider-tree correlated branch with an invented deferred batch statement",
			Patches: []Patch{{Path: "go/internal/queryplanbuild/build.go", Before: "\t\t\tif strategy == readsql.RelationBatch {", After: "\t\t\tif true || strategy == readsql.RelationBatch {"}},
			Gate:    gate("TestCorrelatedProviderFactAndDeferredNestedHydrationAreTruthful"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_TYPED_BUILDER_DRIFTS_ANALYTICS_OPERATION", Summary: "report an aggregate typed plan as groupBy",
			Patches: []Patch{{Path: "go/internal/queryplanbuild/build.go", Before: "\tcase golem.AnalyticsAggregate:\n\t\treturn \"aggregate\", \"analytics\", true", After: "\tcase golem.AnalyticsAggregate:\n\t\treturn \"groupBy\", \"analytics\", true"}},
			Gate:    gate("TestAnalyticsAndScopedTypedPlansOwnOperationRootAndPrimaryPurpose"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_TYPED_BUILDER_RAISES_DEPTH_BOUND", Summary: "permit a typed relation frame beyond the accepted depth-32 report bound",
			Patches: []Patch{{Path: "go/internal/queryplanbuild/build.go", Before: "\tmaxTypedPlanDepth = 32", After: "\tmaxTypedPlanDepth = 33"}},
			Gate:    gate("TestTypedTraversalRefusesDepthBeyondThirtyTwoWithoutRecursiveFrameCopies"), Timeout: 2 * time.Minute,
		},
	}
}
