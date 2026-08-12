package p8mutation

import "time"

// queryPlanAnalyticsAliasMutations remains isolated while root owns the shared
// query-plan Catalog inventory. These mutants exercise only the analytics
// renderer's statement-scoped alias facts and policy-fragment allocation.
func queryPlanAnalyticsAliasMutations() []Mutation {
	gate := func(test string) Gate {
		return Gate{Directory: "go", Package: "./internal/analytics", Test: test}
	}
	return []Mutation{
		{
			Label: "QUERYPLAN_ANALYTICS_ALIAS_OMITS_ROOT", Summary: "render the analytics root table without retaining its stable model and field identities",
			Patches: []Patch{{Path: "go/internal/analytics/sql.go", Before: "\tif err := planMap.add(string(rootAlias), policyir.ModelID(request.ModelID()), policyir.RelationID{}, analyticsAuthorizedPlanFieldIDs(authorized), AnalyticsPlanAliasPhysicalAccess); err != nil {\n\t\treturn Statement{}, err\n\t}", After: "\t_ = analyticsAuthorizedPlanFieldIDs(authorized)"}},
			Gate:    gate("TestAnalyticsStatementPlanMapOwnsEveryProviderVisibleAggregateAlias"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ANALYTICS_ALIAS_OMITS_JOIN", Summary: "render an analytics relation join without retaining its stable model, relation, and field identities",
			Patches: []Patch{{Path: "go/internal/analytics/sql.go", Before: "\t\tif err := planMap.add(string(relationAliases[index]), policyir.ModelID(hop.Endpoint.TargetModelID()), policyir.RelationID(hop.Endpoint.RelationID()), analyticsAuthorizedPlanFieldIDs(hop.Authorized), AnalyticsPlanAliasPhysicalAccess); err != nil {\n\t\t\treturn Statement{}, err\n\t\t}", After: "\t\t_ = analyticsAuthorizedPlanFieldIDs(hop.Authorized)"}},
			Gate:    gate("TestAnalyticsRelationAndPolicyAliasesAreTypedAndStatementUnique"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ANALYTICS_ALIAS_OMITS_AGGREGATE_SCOPE", Summary: "render provider-visible aggregate CTEs without retaining their typed identities",
			Patches: []Patch{{Path: "go/internal/analytics/sql.go", Before: "\tif useContributionCTE {\n\t\tif err := addResultAlias(contributionAlias, AnalyticsPlanAliasMaterialize); err != nil {\n\t\t\treturn Statement{}, err\n\t\t}\n\t}\n\tcontributionProjection := []string{}", After: "\tcontributionProjection := []string{}"}},
			Gate:    gate("TestAnalyticsStatementPlanMapOwnsEveryProviderVisibleAggregateAlias"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ANALYTICS_ALIAS_CLAIMS_DERIVED_ACCESS", Summary: "label a materialized aggregate alias as physical model access",
			Patches: []Patch{{Path: "go/internal/analytics/sql.go", Before: "addResultAlias(contributionAlias, AnalyticsPlanAliasMaterialize)", After: "addResultAlias(contributionAlias, AnalyticsPlanAliasPhysicalAccess)"}},
			Gate:    gate("TestAnalyticsStatementPlanMapOwnsEveryProviderVisibleAggregateAlias"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ANALYTICS_ALIAS_OMITS_ROOT_POLICY", Summary: "discard policy-relation alias facts compiled into the analytics root predicate",
			Patches: []Patch{{Path: "go/internal/analytics/sql.go", Before: "\tif err := planMap.mergePolicy(root.PolicyRelationAliases()); err != nil {\n\t\treturn Statement{}, err\n\t}", After: "\t_ = root.PolicyRelationAliases()"}},
			Gate:    gate("TestAnalyticsRelationAndPolicyAliasesAreTypedAndStatementUnique"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ANALYTICS_ALIAS_MISCLASSIFIES_POLICY_ROLE", Summary: "classify an analytics policy traversal as ordinary physical access",
			Patches: []Patch{{Path: "go/internal/analytics/plan_map.go", Before: "func newAnalyticsPolicyAliasFact(fact policysql.PolicyRelationAliasFact) AnalyticsPlanAliasFact {\n\treturn AnalyticsPlanAliasFact{matcher: fact, modelID: fact.ModelID(), relationID: fact.RelationID(), role: AnalyticsPlanAliasCorrelatedRelation}\n}", After: "func newAnalyticsPolicyAliasFact(fact policysql.PolicyRelationAliasFact) AnalyticsPlanAliasFact {\n\treturn AnalyticsPlanAliasFact{matcher: fact, modelID: fact.ModelID(), relationID: fact.RelationID(), role: AnalyticsPlanAliasPhysicalAccess}\n}"}},
			Gate:    gate("TestAnalyticsRelationAndPolicyAliasesAreTypedAndStatementUnique"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ANALYTICS_ALIAS_RESETS_JOIN_POLICY_SCOPE", Summary: "reuse golem_p1 by compiling the join policy with a fresh allocator",
			Patches: []Patch{{Path: "go/internal/analytics/sql.go", Before: "\t\tpolicy, policyErr := policysql.CompileWithPolicyAliasAllocator(policysql.Request{Condition: hop.Authorized.Where(), Provider: provider, Resolver: resolver, Dialect: dialect, Capabilities: capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: targetAlias}, policyAliases)", After: "\t\tpolicy, policyErr := policysql.CompileWithPolicyAliasAllocator(policysql.Request{Condition: hop.Authorized.Where(), Provider: provider, Resolver: resolver, Dialect: dialect, Capabilities: capabilities, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: targetAlias}, policysql.NewPolicyAliasAllocator())"}},
			Gate:    gate("TestAnalyticsRelationAndPolicyAliasesAreTypedAndStatementUnique"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ANALYTICS_ALIAS_OMITS_JOIN_POLICY", Summary: "discard policy-relation alias facts compiled into an analytics join predicate",
			Patches: []Patch{{Path: "go/internal/analytics/sql.go", Before: "\t\tif policyErr := planMap.mergePolicy(policy.PolicyRelationAliases()); policyErr != nil {\n\t\t\treturn Statement{}, policyErr\n\t\t}", After: "\t\t_ = policy.PolicyRelationAliases()"}},
			Gate:    gate("TestAnalyticsRelationAndPolicyAliasesAreTypedAndStatementUnique"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ANALYTICS_ALIAS_EXPOSES_FIELD_SLICE", Summary: "return renderer-owned stable field identity storage without copying",
			Patches: []Patch{{Path: "go/internal/analytics/plan_map.go", Before: "func (fact AnalyticsPlanAliasFact) FieldIDs() []policyir.FieldID {\n\treturn append([]policyir.FieldID(nil), fact.fieldIDs...)\n}", After: "func (fact AnalyticsPlanAliasFact) FieldIDs() []policyir.FieldID {\n\treturn fact.fieldIDs\n}"}},
			Gate:    gate("TestAnalyticsPlanAliasFactIsPrivateImmutableAndAmbiguityFailsClosed"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ANALYTICS_ALIAS_GUESSES_UNKNOWN", Summary: "map an unknown provider alias to the first renderer-owned analytics identity",
			Patches: []Patch{{Path: "go/internal/analytics/plan_map.go", Before: "\tfor _, fact := range plan.aliases {\n\t\tif fact.Matches(candidate) {\n\t\t\tresult = append(result, cloneAnalyticsPlanAliasFact(fact))\n\t\t}\n\t}\n\treturn result\n}", After: "\tfor _, fact := range plan.aliases {\n\t\tif fact.Matches(candidate) {\n\t\t\tresult = append(result, cloneAnalyticsPlanAliasFact(fact))\n\t\t}\n\t}\n\tif len(result) == 0 && len(plan.aliases) != 0 { return []AnalyticsPlanAliasFact{cloneAnalyticsPlanAliasFact(plan.aliases[0])} }\n\treturn result\n}"}},
			Gate:    gate("TestAnalyticsPlanAliasFactIsPrivateImmutableAndAmbiguityFailsClosed"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ANALYTICS_ALIAS_ACCEPTS_AMBIGUITY", Summary: "allow two stable identities to claim one provider-visible analytics alias",
			Patches: []Patch{{Path: "go/internal/analytics/plan_map.go", Before: "\tif _, duplicate := builder.owned[alias]; duplicate {", After: "\tif _, duplicate := builder.owned[alias]; duplicate && false {"}},
			Gate:    gate("TestAnalyticsPlanAliasFactIsPrivateImmutableAndAmbiguityFailsClosed"), Timeout: 2 * time.Minute,
		},
	}
}
