package p8mutation

import "time"

// queryPlanReadAliasMutations remains isolated until the query-plan registry
// slice releases the shared Catalog inventory for aggregation.
func queryPlanReadAliasMutations() []Mutation {
	gate := func(test string) Gate {
		return Gate{Directory: "go", Package: "./internal/read/sql", Test: test}
	}
	return []Mutation{
		{
			Label: "QUERYPLAN_READ_ALIAS_OMITS_ROOT", Summary: "render a root table alias without retaining its stable model identity",
			Patches: []Patch{{Path: "go/internal/read/sql/render.go", Before: "\tplanMap.add(string(rootAlias), plan.ModelID(), policyir.RelationID{}, nil, PlanAliasPhysicalAccess)", After: "\t_ = rootAlias"}},
			Gate:    gate("TestOrdinaryReadStatementPlanMapIsExactDeterministicImmutableAndFailsClosed"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_READ_ALIAS_OMITS_ROOT_POLICY", Summary: "drop policy relation identities when merging the root predicate fragment",
			Patches: []Patch{{Path: "go/internal/read/sql/render.go", Before: "\tplanMap.mergePolicy(fragment.PolicyRelationAliases())\n\trootArgs := fragment.Args()", After: "\t_ = fragment.PolicyRelationAliases()\n\trootArgs := fragment.Args()"}},
			Gate:    gate("TestOrdinaryReadStatementPlanMapMergesPolicyRelationAliasesWithoutChangingRender"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_READ_ALIAS_MISCLASSIFIES_POLICY_ROLE", Summary: "classify a policy traversal as ordinary physical access",
			Patches: []Patch{{Path: "go/internal/read/sql/plan_map.go", Before: "func newPolicyPlanAliasFact(fact policysql.PolicyRelationAliasFact) PlanAliasFact {\n\treturn PlanAliasFact{matcher: fact, modelID: fact.ModelID(), relationID: fact.RelationID(), role: PlanAliasCorrelatedRelation}\n}", After: "func newPolicyPlanAliasFact(fact policysql.PolicyRelationAliasFact) PlanAliasFact {\n\treturn PlanAliasFact{matcher: fact, modelID: fact.ModelID(), relationID: fact.RelationID(), role: PlanAliasPhysicalAccess}\n}"}},
			Gate:    gate("TestOrdinaryReadStatementPlanMapMergesPolicyRelationAliasesWithoutChangingRender"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_READ_ALIAS_RESETS_FRAGMENT_ALLOCATOR", Summary: "reset policy alias numbering for an independently compiled cursor fragment in the same statement",
			Patches: []Patch{{Path: "go/internal/read/sql/render.go", Before: "reverse, policyir.RelationID{}, PlanAliasPhysicalAccess, policyAliases)", After: "reverse, policyir.RelationID{}, PlanAliasPhysicalAccess, policysql.NewPolicyAliasAllocator())"}},
			Gate:    gate("TestOrdinaryReadStatementPlanMapMergesPolicyRelationAliasesWithoutChangingRender"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_READ_ALIAS_OMITS_CURSOR_ROOT", Summary: "render the cursor hydration table without its stable model and field identities",
			Patches: []Patch{{Path: "go/internal/read/sql/render.go", Before: "\tplanMap.add(string(cursorTableAlias), plan.ModelID(), relation, orderFieldIDs(orders), role)", After: "\t_ = orderFieldIDs(orders)"}},
			Gate:    gate("TestOrdinaryReadStatementPlanMapMergesPolicyRelationAliasesWithoutChangingRender"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_READ_ALIAS_OMITS_RELATION_COUNT", Summary: "render a relation-count access without retaining its stable relation identity",
			Patches: []Patch{{Path: "go/internal/read/sql/render.go", Before: "\t\tplanMap.add(string(childAlias), child.ModelID(), count.RelationID(), nil, PlanAliasCorrelatedRelation)", After: "\t\t_ = childAlias"}},
			Gate:    gate("TestRelationCountRendersDeterministicallyAndExecutesAuthorizedTargetBeforeCount"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_READ_ALIAS_OMITS_CORRELATED_ROOT", Summary: "render a correlated relation access without retaining its stable relation identity",
			Patches: []Patch{{Path: "go/internal/read/sql/correlated.go", Before: "\tplanMap.add(string(baseAlias), child.ModelID(), relation.RelationID(), nil, PlanAliasCorrelatedRelation)", After: "\t_ = baseAlias"}},
			Gate:    gate("TestIndexedToManyRendersAuthorizedCorrelatedJSONAcrossProviders"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_READ_ALIAS_OMITS_BATCH_ROOT", Summary: "render a relation hydration batch without its stable relation and correlation-field identities",
			Patches: []Patch{{Path: "go/internal/read/sql/batch.go", Before: "\tplanMap.add(string(alias), plan.ModelID(), policyir.RelationID(endpoint.RelationID()), endpointChildFieldIDs(endpoint), PlanAliasPhysicalAccess)", After: "\t_ = endpointChildFieldIDs(endpoint)"}},
			Gate:    gate("TestBoundedBatchSQLIsDeterministicPortableAndPerParent"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_READ_ALIAS_EXPOSES_FIELD_SLICE", Summary: "return renderer-owned stable field identity storage without copying",
			Patches: []Patch{{Path: "go/internal/read/sql/plan_map.go", Before: "func (fact PlanAliasFact) FieldIDs() []policyir.FieldID {\n\treturn append([]policyir.FieldID(nil), fact.fieldIDs...)\n}", After: "func (fact PlanAliasFact) FieldIDs() []policyir.FieldID {\n\treturn fact.fieldIDs\n}"}},
			Gate:    gate("TestPlanAliasFactRetainsOnlyOpaqueMatcherAndStableSanitizerIdentities"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_READ_ALIAS_GUESSES_UNKNOWN", Summary: "map an unknown provider alias to the first renderer-owned identity",
			Patches: []Patch{{Path: "go/internal/read/sql/plan_map.go", Before: "\tfor _, fact := range plan.aliases {\n\t\tif fact.Matches(candidate) {\n\t\t\tresult = append(result, clonePlanAliasFact(fact))\n\t\t}\n\t}\n\treturn result\n}", After: "\tfor _, fact := range plan.aliases {\n\t\tif fact.Matches(candidate) {\n\t\t\tresult = append(result, clonePlanAliasFact(fact))\n\t\t}\n\t}\n\tif len(result) == 0 && len(plan.aliases) != 0 { return []PlanAliasFact{clonePlanAliasFact(plan.aliases[0])} }\n\treturn result\n}"}},
			Gate:    gate("TestOrdinaryReadStatementPlanMapIsExactDeterministicImmutableAndFailsClosed"), Timeout: 2 * time.Minute,
		},
	}
}
