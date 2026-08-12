package p8mutation

import "time"

// queryPlanScopedAliasMutations remains isolated until the query-plan registry
// slice releases the shared Catalog inventory for aggregation.
func queryPlanScopedAliasMutations() []Mutation {
	gate := func(test string) Gate {
		return Gate{Directory: "go", Package: "./internal/scoped", Test: test}
	}
	return []Mutation{
		{
			Label: "QUERYPLAN_SCOPED_ALIAS_OMITS_ROOT", Summary: "render a scoped root without retaining its stable model and field identities",
			Patches: []Patch{{Path: "go/internal/scoped/scoped.go", Before: "\tif err := planMap.add(string(aliases[0]), policyir.ModelID(rootOccurrence.model), policyir.RelationID{}, scopedAuthorizedPlanFieldIDs(rootOccurrence.authorized), ScopedPlanAliasPhysicalAccess); err != nil {\n\t\treturn Statement{}, err\n\t}", After: "\t_ = scopedAuthorizedPlanFieldIDs(rootOccurrence.authorized)"}},
			Gate:    gate("TestScopedStatementPlanMapOwnsRootJoinAndStatementUniquePolicyAliases"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_SCOPED_ALIAS_OMITS_JOIN", Summary: "render a scoped join without retaining its stable relation and field identities",
			Patches: []Patch{{Path: "go/internal/scoped/scoped.go", Before: "\t\tif err := planMap.add(string(alias), policyir.ModelID(item.model), policyir.RelationID(join.Relation), scopedAuthorizedPlanFieldIDs(item.authorized), ScopedPlanAliasPhysicalAccess); err != nil {\n\t\t\treturn Statement{}, err\n\t\t}", After: "\t\t_ = scopedAuthorizedPlanFieldIDs(item.authorized)"}},
			Gate:    gate("TestScopedStatementPlanMapOwnsRootJoinAndStatementUniquePolicyAliases"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_SCOPED_ALIAS_RESETS_POLICY_ALLOCATOR", Summary: "reset policy alias numbering for each independently compiled scoped occurrence policy",
			Patches: []Patch{{Path: "go/internal/scoped/scoped.go", Before: "RootAlias: alias}, policyAliases)\n\t\tif compileErr != nil", After: "RootAlias: alias}, policysql.NewPolicyAliasAllocator())\n\t\tif compileErr != nil"}},
			Gate:    gate("TestScopedStatementPlanMapOwnsRootJoinAndStatementUniquePolicyAliases"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_SCOPED_ALIAS_OMITS_OCCURRENCE_POLICY", Summary: "drop policy traversal identities produced by a scoped occurrence predicate",
			Patches: []Patch{{Path: "go/internal/scoped/scoped.go", Before: "\t\tif mergeErr := planMap.mergePolicy(fragment.PolicyRelationAliases()); mergeErr != nil {\n\t\t\treturn \"\", mergeErr\n\t\t}\n\t\tvalue := rebase", After: "\t\t_ = fragment.PolicyRelationAliases()\n\t\tvalue := rebase"}},
			Gate:    gate("TestScopedStatementPlanMapOwnsRootJoinAndStatementUniquePolicyAliases"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_SCOPED_ALIAS_MISCLASSIFIES_POLICY_ROLE", Summary: "classify a scoped policy traversal as ordinary physical access",
			Patches: []Patch{{Path: "go/internal/scoped/plan_map.go", Before: "func newScopedPolicyAliasFact(fact policysql.PolicyRelationAliasFact) ScopedPlanAliasFact {\n\treturn ScopedPlanAliasFact{matcher: fact, modelID: fact.ModelID(), relationID: fact.RelationID(), role: ScopedPlanAliasCorrelatedRelation}\n}", After: "func newScopedPolicyAliasFact(fact policysql.PolicyRelationAliasFact) ScopedPlanAliasFact {\n\treturn ScopedPlanAliasFact{matcher: fact, modelID: fact.ModelID(), relationID: fact.RelationID(), role: ScopedPlanAliasPhysicalAccess}\n}"}},
			Gate:    gate("TestScopedStatementPlanMapOwnsRootJoinAndStatementUniquePolicyAliases"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_SCOPED_ALIAS_OMITS_MASK_POLICY", Summary: "drop policy traversal identities produced by a scoped field mask",
			Patches: []Patch{{Path: "go/internal/scoped/scoped.go", Before: "\t\t\tif err := planMap.mergePolicy(fragment.PolicyRelationAliases()); err != nil {\n\t\t\t\treturn \"\", schema.Field{}, err\n\t\t\t}\n\t\t\tmaskSQL := rebase", After: "\t\t\t_ = fragment.PolicyRelationAliases()\n\t\t\tmaskSQL := rebase"}},
			Gate:    gate("TestScopedStatementPlanMapOwnsRootJoinAndStatementUniquePolicyAliases"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_SCOPED_ALIAS_EXPOSES_FIELD_SLICE", Summary: "return renderer-owned scoped field identity storage without copying",
			Patches: []Patch{{Path: "go/internal/scoped/plan_map.go", Before: "func (fact ScopedPlanAliasFact) FieldIDs() []policyir.FieldID {\n\treturn append([]policyir.FieldID(nil), fact.fieldIDs...)\n}", After: "func (fact ScopedPlanAliasFact) FieldIDs() []policyir.FieldID {\n\treturn fact.fieldIDs\n}"}},
			Gate:    gate("TestScopedPlanMapIsDeepCopiedOpaqueAndRejectsAmbiguousAliases"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_SCOPED_ALIAS_GUESSES_UNKNOWN", Summary: "map an unknown provider alias to the first renderer-owned scoped identity",
			Patches: []Patch{{Path: "go/internal/scoped/plan_map.go", Before: "\tfor _, fact := range plan.aliases {\n\t\tif fact.Matches(candidate) {\n\t\t\tresult = append(result, cloneScopedPlanAliasFact(fact))\n\t\t}\n\t}\n\treturn result\n}", After: "\tfor _, fact := range plan.aliases {\n\t\tif fact.Matches(candidate) {\n\t\t\tresult = append(result, cloneScopedPlanAliasFact(fact))\n\t\t}\n\t}\n\tif len(result) == 0 && len(plan.aliases) != 0 { return []ScopedPlanAliasFact{cloneScopedPlanAliasFact(plan.aliases[0])} }\n\treturn result\n}"}},
			Gate:    gate("TestScopedStatementPlanMapOwnsRootJoinAndStatementUniquePolicyAliases"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_SCOPED_ALIAS_ACCEPTS_AMBIGUITY", Summary: "accept two renderer-owned facts for one flat provider alias",
			Patches: []Patch{{Path: "go/internal/scoped/plan_map.go", Before: "\tif _, duplicate := builder.owned[alias]; duplicate {\n\t\treturn fmt.Errorf(\"P6_SCOPED_PLAN_MAP: renderer alias identity is ambiguous\")\n\t}", After: "\t_, _ = builder.owned[alias]"}},
			Gate:    gate("TestScopedPlanMapIsDeepCopiedOpaqueAndRejectsAmbiguousAliases"), Timeout: 2 * time.Minute,
		},
	}
}
