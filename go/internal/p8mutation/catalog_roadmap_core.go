package p8mutation

import "time"

func roadmapCoreMutations() []Mutation {
	return []Mutation{
		{
			Label:   "QUERYPLAN_EXPOSES_POINTER_AUTHORITY",
			Summary: "add a pointer-only raw provider-plan accessor outside the accepted public report API",
			Patches: []Patch{{
				Path:   "go/queryplan/types.go",
				Before: "type Report internalreport.Report\n",
				After:  "type Report internalreport.Report\nfunc (*Report) RawProviderPlan() string { return \"\" }\n",
			}},
			Gate: Gate{Directory: "go", Package: "./queryplan", Test: "TestQueryPlanPublicAPIContainsOnlyAcceptedTypesConstantsAndAccessors"}, Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_CLAIM_ACCEPTS_ZERO_VERSION",
			Summary: "treat the invalid zero existing-version token as a valid runtime claim",
			Patches: []Patch{{
				Path:   "go/internal/concurrencyclaim/claim.go",
				Before: "if claim.value <= 0 {",
				After:  "if claim.value < 0 {",
			}},
			Gate: Gate{Directory: "go", Package: "./internal/concurrencyclaim", Test: "TestPublicExistingVersionConvertsToClosedInternalInspection"}, Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_CLAIM_ABSENT_IS_INVALID",
			Summary: "collapse the one valid absent upsert expectation into the invalid zero state",
			Patches: []Patch{{
				Path:   "go/internal/concurrencyclaim/claim.go",
				Before: "return ConcurrencyExpectation{state: expectationAbsent}",
				After:  "return ConcurrencyExpectation{}",
			}},
			Gate: Gate{Directory: "go", Package: "./internal/concurrencyclaim", Test: "TestPublicExpectationConvertsToClosedInternalDiscrimination"}, Timeout: 2 * time.Minute,
		},
		{
			Label:   "CONCURRENCY_CLAIM_EXPOSES_VALUE_METHOD",
			Summary: "expose a public raw-value method on the otherwise opaque existing-version token",
			Patches: []Patch{{
				Path:   "go/golem/concurrency.go",
				Before: "type ExistingVersion concurrencyclaim.ExistingVersion\n",
				After:  "type ExistingVersion concurrencyclaim.ExistingVersion\nfunc (ExistingVersion) Value() int64 { return 0 }\n",
			}},
			Gate: Gate{Directory: "go", Package: "./golem", Test: "TestConcurrencyExpectationDistinguishesAbsentExistingAndInvalid"}, Timeout: 2 * time.Minute,
		},
	}
}
