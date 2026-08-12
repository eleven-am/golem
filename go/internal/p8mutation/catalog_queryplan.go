package p8mutation

import "time"

func queryPlanCoreMutations() []Mutation {
	gate := func(pkg, test string) Gate { return Gate{Directory: "go", Package: pkg, Test: test} }
	return []Mutation{
		{
			Label: "QUERYPLAN_PUBLIC_JSON_WIRE", Summary: "add an unaccepted public JSON report encoder",
			Patches: []Patch{{Path: "go/queryplan/types.go", Before: "type Report internalreport.Report\n", After: "type Report internalreport.Report\nfunc (report Report) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }\n"}},
			Gate:    gate("./queryplan", "TestQueryPlanPublicCoreSurfaceMatchesAcceptedContract"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ACCEPT_ZERO_ROOT_ID", Summary: "accept the zero model identity as a report root",
			Patches: []Patch{{Path: "go/internal/queryplanreport/build.go", Before: "if !validProvider(input.Provider) || !validOperation(input.Operation) || input.RootModelID == (golem.ModelID{}) {", After: "if !validProvider(input.Provider) || !validOperation(input.Operation) {"}},
			Gate:    gate("./internal/queryplanreport", "TestBuildRejectsEveryNonCanonicalReportShape"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ACCEPT_ORDINAL_GAP", Summary: "trust supplied statement ordinals instead of canonical positions",
			Patches: []Patch{{Path: "go/internal/queryplanreport/build.go", Before: "if candidate.Ordinal != uint32(index) || !validPurpose(candidate.Purpose) {", After: "if !validPurpose(candidate.Purpose) {"}},
			Gate:    gate("./internal/queryplanreport", "TestBuildRejectsEveryNonCanonicalReportShape"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_COLLAPSES_ANALYTICS_PURPOSE", Summary: "mislabel aggregate and grouping provider statements as ordinary read roots",
			Patches: []Patch{{Path: "go/internal/queryplanreport/build.go", Before: "case operationAggregate, operationGroupBy, operationRelationGroupBy:\n\t\treturn purposeAnalytics, true", After: "case operationAggregate, operationGroupBy, operationRelationGroupBy:\n\t\treturn purposeRoot, true"}},
			Gate:    gate("./internal/queryplanreport", "TestBuildRequiresTheExactPrimaryPurposeForEachOperation"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_DEFERRED_CLAIMS_ACCESS", Summary: "allow a deferred batch to claim a provider full-scan path",
			Patches: []Patch{{Path: "go/internal/queryplanreport/build.go", Before: "case kindJoin, kindSort, kindAggregate, kindMaterialize, kindCorrelatedRelation, kindDeferredBatch:\n\t\treturn access == accessNone", After: "case kindDeferredBatch:\n\t\treturn access == accessNone || access == accessFullScan\n\tcase kindJoin, kindSort, kindAggregate, kindMaterialize, kindCorrelatedRelation:\n\t\treturn access == accessNone"}},
			Gate:    gate("./internal/queryplanreport", "TestBuildDeferredBatchFactsBoundsAndNoProviderClaim"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_TRUSTS_FALSE_BOUNDS", Summary: "publish statement bounds not proven by the typed plan",
			Patches: []Patch{{Path: "go/internal/queryplanreport/build.go", Before: "if uint32(minimum) != input.MinimumExecutionStatements || uint32(maximum) != input.MaximumExecutionStatements {\n\t\treturn Report{}, fail(CodeInvalid)\n\t}", After: "_ = minimum\n\t_ = maximum"}},
			Gate:    gate("./internal/queryplanreport", "TestBuildRejectsEveryNonCanonicalReportShape"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ACCEPTS_DUPLICATE_FIELD", Summary: "accept duplicate or zero field identities",
			Patches: []Patch{{Path: "go/internal/queryplanreport/build.go", Before: "if hasDuplicateOrZeroFields(input.FieldIDs) {\n\t\treturn Node{}, fail(CodeInvalid)\n\t}", After: "_ = hasDuplicateOrZeroFields"}},
			Gate:    gate("./internal/queryplanreport", "TestBuildRejectsEveryNonCanonicalReportShape"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_OMITS_MULTI_WARNING", Summary: "omit the warning for a multi-statement execution shape",
			Patches: []Patch{{Path: "go/internal/queryplanreport/build.go", Before: "if maximum > 1 {\n\t\tcounts.reportFlags[warningMultiStatement] = true\n\t}", After: "_ = maximum"}},
			Gate:    gate("./internal/queryplanreport", "TestBuildDerivesCanonicalWarningsAndDigest"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_OMITS_UNKNOWN_ACCESS_WARNING", Summary: "omit unknown warnings when only access registration is missing",
			Patches: []Patch{{Path: "go/internal/queryplanreport/build.go", Before: "if kind == kindUnknown || access == accessUnknown {", After: "if kind == kindUnknown {"}},
			Gate:    gate("./internal/queryplanreport", "TestBuildDerivesUnknownWarningFromUnknownAccessWithoutGuessingIdentity"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_DIGEST_OMITS_BATCH_FACTS", Summary: "omit deferred capacity and bounds from the canonical digest",
			Patches: []Patch{{Path: "go/internal/queryplanreport/build.go", Before: "if node.hasBatch {\n\t\tcapacity, minimum, maximum := node.batchCapacity, node.batchMinimum, node.batchMaximum\n\t\tdocument.BatchCapacity, document.BatchMinimum, document.BatchMaximum = &capacity, &minimum, &maximum\n\t}", After: "_ = node.hasBatch"}},
			Gate:    gate("./internal/queryplanreport", "TestCanonicalDigestCoversEveryClosedFactClass"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_EXPOSES_FIELD_SLICE", Summary: "return immutable report field storage without copying",
			Patches: []Patch{{Path: "go/internal/queryplanreport/types.go", Before: "return append([]golem.FieldID(nil), value.fieldIDs...)", After: "return value.fieldIDs"}},
			Gate:    gate("./queryplan", "TestPublicReportAccessorsAreExactImmutableAndBatchTruthful"), Timeout: 2 * time.Minute,
		},
	}
}
