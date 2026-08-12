package p8mutation

import "time"

// queryPlanPolicyAliasMutations is intentionally isolated from Catalog while
// the query-plan registry slice owns the shared catalog inventory files.
func queryPlanPolicyAliasMutations() []Mutation {
	return []Mutation{
		{
			Label:   "QUERYPLAN_POLICY_ALIAS_REGISTRATION_OMITTED",
			Summary: "allocate policy relation SQL aliases without retaining their stable sanitizer facts",
			Patches: []Patch{{
				Path:   "go/internal/policy/sql/compile.go",
				Before: "\tcompiler.policyRelationAliases = append(compiler.policyRelationAliases, newPolicyRelationAliasFact(alias, model, relation))",
				After:  "\t_ = newPolicyRelationAliasFact(alias, model, relation)",
			}},
			Gate: Gate{
				Directory: "go",
				Package:   "./internal/policy/sql",
				Test:      "TestCompilePolicyAliasFactsSingleAndNestedRelationsAreExactDeterministicAndImmutable",
			},
			Timeout: 2 * time.Minute,
		},
	}
}
