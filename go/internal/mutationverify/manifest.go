package mutationverify

// Catalog is the complete P6 named-mutation inventory. Covered entries have a
// mechanically applicable patch and an exact named test. Remaining entries are
// retained here so the harness cannot silently redefine P6-I as a smaller set.
func Catalog() []Mutation {
	providerEnv := []string{"GOLEM_TEST_POSTGRES_DSN", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}
	runtimeProvider := func(name string) []Test {
		return []Test{{Package: "./runtime", Name: name, Env: providerEnv}}
	}
	return []Mutation{
		{
			Label: "AGGREGATE_IN_GO", Summary: "replace Post aggregate SQL with an authorized row fetch and Go fold",
			Patches: []Patch{{Path: "runtime/testdata/p5social/zz_golem_registry.gen.go", Before: "func (client CallerPostClient[P]) Aggregate(ctx context.Context, request golem.AggregateRequest[Post]) (golem.AggregateResult[Post], error) {\n\treturn golemruntime.CallerAggregate(ctx, client.runtime, GolemGeneratedPostDescriptor, request)\n}\n", After: "func (client CallerPostClient[P]) Aggregate(ctx context.Context, request golem.AggregateRequest[Post]) (golem.AggregateResult[Post], error) {\n\tfrozen, err := golem.RuntimeFreezeAggregateRequest(request)\n\tif err != nil { return golem.AggregateResult[Post]{}, err }\n\trows, err := client.FindMany(ctx)\n\tif err != nil { return golem.AggregateResult[Post]{}, err }\n\tcells := make([]golem.RuntimeAnalyticsCell, 0, len(frozen.Measures()))\n\tfor _, term := range frozen.Measures() {\n\t\tkey := golem.RuntimeAnalyticsTermKey(term)\n\t\tswitch term.Operator {\n\t\tcase golem.AggregateCountAll:\n\t\t\tcells = append(cells, golem.RuntimePresentAnalyticsCell(key, int64(len(rows))))\n\t\tcase golem.AggregateCountField:\n\t\t\tvar count int64\n\t\t\tfor _, row := range rows { if _, ok := golem.Value(row, Posts.Title).Get(); ok { count++ } }\n\t\t\tcells = append(cells, golem.RuntimePresentAnalyticsCell(key, count))\n\t\tcase golem.AggregateMinimum:\n\t\t\tminimum, present := \"\", false\n\t\t\tfor _, row := range rows { if value, ok := golem.Value(row, Posts.Title).Get(); ok && (!present || value < minimum) { minimum, present = value, true } }\n\t\t\tif present { cells = append(cells, golem.RuntimePresentAnalyticsCell(key, minimum)) } else { cells = append(cells, golem.RuntimeNullAnalyticsCell(key)) }\n\t\tdefault:\n\t\t\treturn golemruntime.CallerAggregate(ctx, client.runtime, GolemGeneratedPostDescriptor, request)\n\t\t}\n\t}\n\treturn golem.RuntimeAggregateResult[Post](frozen.ModelID(), cells...), nil\n}\n"}},
			Tests:   runtimeProvider("TestP6AnalyticsStatementCountIsOneAndNoContributionRowsAreDecoded"),
		},
		{
			Label: "POLICY_AFTER_GROUP", Summary: "move the authorized root predicate from WHERE to HAVING",
			Patches: []Patch{
				{Path: "internal/analytics/sql.go", Before: "\tuseContributionCTE := maxContributionRows > 0\n", After: "\tuseContributionCTE := false // mutant: aggregate before policy\n"},
				{Path: "internal/analytics/sql.go", Before: "\tfromText += \" WHERE \" + rootSQL\n", After: "\tfromText += \" WHERE TRUE\"\n"},
				{Path: "internal/analytics/sql.go", Before: "\tif len(groups) > 0 {\n\t\ttext += \" GROUP BY \" + strings.Join(groups, \", \")\n\t}\n\tgroupedCTE := \"\"\n", After: "\tif len(groups) > 0 {\n\t\ttext += \" GROUP BY \" + strings.Join(groups, \", \")\n\t}\n\ttext += \" HAVING \" + rootSQL\n\tgroupedCTE := \"\"\n"},
			},
			Tests: runtimeProvider("TestP6CountMissingInvisibleAndSystemStances"),
		},
		{
			Label: "SKIP_MEASURE_CLASSIFICATION", Summary: "omit measures from read classification",
			Patches: []Patch{{Path: "internal/analytics/plan.go", Before: "\tterms := append(request.Dimensions(), request.Measures()...)\n", After: "\tterms := request.Dimensions()\n"}},
			Tests:   runtimeProvider("TestP6ClassificationPositionSpyCoversWhereCountMeasureDimensionHavingOrderAndGraphQLSelection"),
		},
		{
			Label: "COUNT_FIELD_AS_COUNT_ALL", Summary: "render field-count as count-all",
			Patches: []Patch{{Path: "internal/analytics/sql.go", Before: "\tcase golem.AggregateCountField:\n\t\treturn \"COUNT(\" + column + \")\"\n", After: "\tcase golem.AggregateCountField:\n\t\treturn \"COUNT(*)\"\n"}},
			Tests:   runtimeProvider("TestP6CountFieldClassifiesNullDistributionButCountAllDoesNot"),
		},
		{
			Label: "MASK_AGGREGATE", Summary: "mask an undischarged conditional analytical field",
			Patches: []Patch{
				{Path: "internal/analytics/plan.go", Before: "\t\tif field.Conditional() && !field.DischargedByConstraint() {\n\t\t\treturn &FieldAuthorizationError{field: term.Field, logicalName: logicalName, reason: \"is conditional and is not discharged by the contribution predicate\"}\n\t\t}\n", After: "\t\t_ = field // mutant: conditional aggregate is masked instead of refused\n"},
				{Path: "internal/analytics/sql.go", Before: "\t\tif classified.Conditional() && !classified.DischargedByConstraint() {\n\t\t\treturn \"\", fmt.Errorf(\"P6_ANALYTICS_SQL_AUTHORIZATION: conditional analytics field is not discharged\")\n\t\t}\n", After: "\t\tif classified.Conditional() && !classified.DischargedByConstraint() {\n\t\t\tcolumn = \"NULL\"\n\t\t}\n"},
			},
			Tests: runtimeProvider("TestP6UndischargedFieldRefusesByLogicalNameBeforeSQL"),
		},
		{
			Label: "DISCHARGE_BY_SAMPLE", Summary: "sample authorized User rows and infer Name aggregate access in Go",
			Patches: []Patch{{Path: "runtime/testdata/p5social/zz_golem_registry.gen.go", Before: "func (client CallerUserClient[P]) Aggregate(ctx context.Context, request golem.AggregateRequest[User]) (golem.AggregateResult[User], error) {\n\treturn golemruntime.CallerAggregate(ctx, client.runtime, GolemGeneratedUserDescriptor, request)\n}\n", After: "func (client CallerUserClient[P]) Aggregate(ctx context.Context, request golem.AggregateRequest[User]) (golem.AggregateResult[User], error) {\n\tfrozen, err := golem.RuntimeFreezeAggregateRequest(request)\n\tif err != nil { return golem.AggregateResult[User]{}, err }\n\trows, err := client.FindMany(ctx)\n\tif err != nil { return golem.AggregateResult[User]{}, err }\n\tcells := make([]golem.RuntimeAnalyticsCell, 0, len(frozen.Measures()))\n\tfor _, term := range frozen.Measures() {\n\t\tif term.Operator != golem.AggregateCountField { return golemruntime.CallerAggregate(ctx, client.runtime, GolemGeneratedUserDescriptor, request) }\n\t\tvar count int64\n\t\tfor _, row := range rows { if _, visibleInSample := golem.Value(row, Users.Name).Get(); visibleInSample { count++ } }\n\t\tcells = append(cells, golem.RuntimePresentAnalyticsCell(golem.RuntimeAnalyticsTermKey(term), count))\n\t}\n\treturn golem.RuntimeAggregateResult[User](frozen.ModelID(), cells...), nil\n}\n"}},
			Tests:   runtimeProvider("TestP6UndischargedFieldRefusesByLogicalNameBeforeSQL"),
		},
		{
			Label: "DECIMAL_TO_REAL", Summary: "use SQLite REAL for Decimal average",
			Patches: []Patch{{Path: "internal/analytics/sql.go", Before: "\t\t\tif provider == policyir.ProviderSQLite {\n\t\t\t\treturn fmt.Sprintf(\"%s(%s, %d)\", sqliteprovider.AnalyticsDecimalAvgFunction, column, scale)\n\t\t\t}\n", After: "\t\t\tif provider == policyir.ProviderSQLite {\n\t\t\t\treturn \"AVG(CAST(\" + column + \" AS REAL))\"\n\t\t\t}\n"}},
			Tests:   []Test{{Package: "./internal/analytics", Name: "TestP6ProviderExactRendererQualifiesCollatesClassifiesTiesAndReverses"}},
		},
		{
			Label: "INTEGER_SUM_INT64", Summary: "use native fixed-width SQLite SUM",
			Patches: []Patch{{Path: "internal/analytics/sql.go", Before: "\t\t\tif provider == policyir.ProviderSQLite {\n\t\t\t\treturn sqliteprovider.AnalyticsIntegerSumFunction + \"(\" + column + \")\"\n\t\t\t}\n", After: "\t\t\tif provider == policyir.ProviderSQLite {\n\t\t\t\treturn \"SUM(\" + column + \")\"\n\t\t\t}\n"}},
			Tests:   runtimeProvider("TestP6ExactIntegerDecimalAndTemporalNeverPassThroughFloat"),
		},
		{
			Label: "NATIVE_COLLATION_GROUP", Summary: "inherit provider-native collation for analytical strings",
			Patches: []Patch{{Path: "internal/analytics/sql.go", Before: "\tif typ.Kind != compilerir.TypeString {\n\t\treturn expression\n\t}\n\tif provider == policyir.ProviderPostgreSQL {\n\t\treturn \"(\" + expression + \" COLLATE \\\"C\\\")\"\n\t}\n\treturn \"(\" + expression + \" COLLATE BINARY)\"\n", After: "\treturn expression\n"}},
			Tests:   runtimeProvider("TestP6BinaryAnalyticalStringSemanticsAcrossProviderCollations"),
		},
		{
			Label: "NULL_SUM_ZERO", Summary: "coalesce null sum to zero",
			Patches: []Patch{{Path: "internal/analytics/sql.go", Before: "\t\tif typ.Kind == compilerir.TypeInt16 || typ.Kind == compilerir.TypeInt32 || typ.Kind == compilerir.TypeInt64 {\n\t\t\tif provider == policyir.ProviderSQLite {\n\t\t\t\treturn sqliteprovider.AnalyticsIntegerSumFunction + \"(\" + column + \")\"\n\t\t\t}\n\t\t\treturn \"SUM(\" + column + \")::text\"\n\t\t}\n", After: "\t\tif typ.Kind == compilerir.TypeInt16 || typ.Kind == compilerir.TypeInt32 || typ.Kind == compilerir.TypeInt64 {\n\t\t\tif provider == policyir.ProviderSQLite {\n\t\t\t\treturn \"COALESCE(\" + sqliteprovider.AnalyticsIntegerSumFunction + \"(\" + column + \"), '0')\"\n\t\t\t}\n\t\t\treturn \"COALESCE(SUM(\" + column + \")::text, '0')\"\n\t\t}\n"}},
			Tests:   runtimeProvider("TestP6EmptyAndAllNullAggregateCells"),
		},
		{
			Label: "SILENT_PROGRAMMATIC_CAP", Summary: "apply a GraphQL-sized group cap to Go GroupBy",
			Patches: []Patch{{Path: "runtime/analytics.go", Before: "\tif enforceProgrammaticGroupLimit {\n\t\tmaxResultRows = app.analyticsLimits.MaxProgrammaticGroups\n\t}\n", After: "\tif enforceProgrammaticGroupLimit {\n\t\tmaxResultRows = 2 // mutant: GraphQL-sized cap leaks into Go GroupBy\n\t}\n"}},
			Tests:   runtimeProvider("TestP6Programmatic34424GroupsAreComplete"),
		},
		{
			Label: "SILENT_GRAPHQL_TRUNCATION", Summary: "truncate omitted-take GraphQL results",
			Patches: []Patch{{Path: "internal/graphql/analytics/analytics.go", Before: "\tif !root.ExplicitTake && len(rows) > root.MaxGroups {\n\t\treturn nil, golem.RuntimeReadError(golem.CodeBadUserInput, \"analytics\", root.Request.ModelID(), golem.FieldID{}, fmt.Sprintf(\"analytics result exceeds %d groups\", root.MaxGroups), nil)\n\t}\n", After: "\tif !root.ExplicitTake && len(rows) > root.MaxGroups {\n\t\trows = rows[:root.MaxGroups]\n\t}\n"}},
			Tests:   runtimeProvider("TestP6GraphQLMissingTakeProbesPlusOneAndExplicitTakeNeverClamps"),
		},
		{
			Label: "LIMIT_BEFORE_HAVING", Summary: "push requested group take into grouped SQL before outer HAVING",
			Patches: []Patch{{Path: "internal/analytics/sql.go", Before: "\tif len(groups) > 0 {\n\t\ttext += \" GROUP BY \" + strings.Join(groups, \", \")\n\t}\n\tgroupedCTE := \"\"\n", After: "\tif len(groups) > 0 {\n\t\ttext += \" GROUP BY \" + strings.Join(groups, \", \")\n\t}\n\tif takePresent {\n\t\tpremature := take\n\t\tif premature < 0 { premature = -premature }\n\t\targs = append(args, int64(premature))\n\t\ttext += \" LIMIT \" + dialect.Placeholder(len(args))\n\t}\n\tgroupedCTE := \"\"\n"}},
			Tests:   runtimeProvider("TestP6LocalGroupByCompleteSemanticOracle"),
		},
		{
			Label: "DROP_ORDER_TIEBREAK", Summary: "omit canonical dimension tie terms",
			Patches: []Patch{{Path: "internal/analytics/sql.go", Before: "\tfor _, term := range dimensions {\n\t\tkey := termKey(term)\n\t\tif ordered[key] {\n\t\t\tcontinue\n\t\t}\n\t\texpr, renderErr := expression(term)\n\t\tif renderErr != nil {\n\t\t\treturn Statement{}, renderErr\n\t\t}\n\t\torderSpecs = append(orderSpecs, analyticsOrderSpec{term: term, expression: expr, alias: publicAliases[key], typ: analyticsTermType(plan, term), exactNumeric: analyticsExactNumeric(plan, term)})\n\t}\n", After: "\t// mutant: canonical dimension tie terms omitted\n"}},
			Tests:   runtimeProvider("TestP6SignedTakeSkipAndCanonicalTieBreakAgreement"),
		},
		{
			Label: "RELATION_TWO_PHASE_MERGE", Summary: "fetch relation targets, query groups separately, and merge by key in Go",
			Patches: []Patch{{Path: "runtime/testdata/p6metrics/zz_golem_registry.gen.go", Before: "func (client CallerMetricClient[P]) RelationGroupBy(ctx context.Context, request golem.RelationGroupRequest[Metric]) ([]golem.RelationGroupRow[Metric], error) {\n\treturn golemruntime.CallerRelationGroupBy(ctx, client.runtime, GolemGeneratedMetricDescriptor, request)\n}\n", After: "func (client CallerMetricClient[P]) RelationGroupBy(ctx context.Context, request golem.RelationGroupRequest[Metric]) ([]golem.RelationGroupRow[Metric], error) {\n\ttargets, err := (CallerCategoryClient[P]{runtime: client.runtime}).FindMany(ctx)\n\tif err != nil { return nil, err }\n\tvisible := map[string]bool{}\n\tfor _, target := range targets { if name, ok := golem.Value(target, Categories.Name).Get(); ok { visible[name] = true } }\n\tgroups, err := golemruntime.CallerRelationGroupBy(ctx, client.runtime, GolemGeneratedMetricDescriptor, request)\n\tif err != nil { return nil, err }\n\tmerged := make([]golem.RelationGroupRow[Metric], 0, len(groups))\n\tfor _, group := range groups { if key, ok := golem.RelationGroupValue(group, Metrics.CategoryParentName).Get(); ok && visible[key] { merged = append(merged, group) } }\n\treturn merged, nil\n}\n"}},
			Tests:   runtimeProvider("TestP6RelationAverageUsesOneSQLContributionSet"),
		},
		{
			Label: "RELATION_TARGET_UNSCOPED", Summary: "omit relation target-hop read policy",
			Patches: []Patch{{Path: "internal/analytics/sql.go", Before: "\t\tconditions = append(conditions, rebase(policy.SQL(), len(args), provider))\n\t\targs = append(args, policy.Args()...)\n", After: "\t\t_ = policy\n"}},
			Tests:   runtimeProvider("TestP6RelationAbsentAndInvisibleTargetsAreIndistinguishable"),
		},
		{
			Label: "LEFT_POLICY_IN_WHERE", Summary: "place scoped left-target policy in WHERE",
			Patches: []Patch{
				{Path: "internal/scoped/scoped.go", Before: "\tfromText := dialect.Table(rootPhysical) + \" AS \" + dialect.Quote(aliases[0])\n", After: "\tfromText := dialect.Table(rootPhysical) + \" AS \" + dialect.Quote(aliases[0])\n\tleftPolicies := []string{}\n"},
				{Path: "internal/scoped/scoped.go", Before: "\t\tconditions = append(conditions, policy)\n\t\tkeyword := \" INNER JOIN \"\n\t\tif join.Kind == golem.ScopedLeftJoin {\n\t\t\tkeyword = \" LEFT JOIN \"\n\t\t}\n", After: "\t\tkeyword := \" INNER JOIN \"\n\t\tif join.Kind == golem.ScopedLeftJoin {\n\t\t\tkeyword = \" LEFT JOIN \"\n\t\t\tleftPolicies = append(leftPolicies, policy)\n\t\t} else {\n\t\t\tconditions = append(conditions, policy)\n\t\t}\n"},
				{Path: "internal/scoped/scoped.go", Before: "\twhere := []string{rootPolicy}\n", After: "\twhere := append([]string{rootPolicy}, leftPolicies...)\n"},
			},
			Tests: runtimeProvider("TestP6ScopedLeftJoinMissingAndInvisibleTargetAreIndistinguishable"),
		},
		{
			Label: "IMPLICIT_RELATION_DEDUP", Summary: "deduplicate scoped to-many pairs",
			Patches: []Patch{{Path: "internal/scoped/scoped.go", Before: "\tif value.Kind == golem.ScopedExpressionCountAll {\n\t\treturn \"COUNT(*)\", nil\n\t}\n", After: "\tif value.Kind == golem.ScopedExpressionCountAll {\n\t\treturn \"COUNT(DISTINCT 1)\", nil\n\t}\n"}},
			Tests:   runtimeProvider("TestP6ScopedToManyJoinCountsAuthorizedPairsWithoutImplicitDeduplication"),
		},
		{
			Label: "ALLOW_RAW_NODE", Summary: "accept raw SQL/identifier/custom ON/write nodes",
			Patches: []Patch{{Path: "golem/scoped.go", Before: "func From[M any](root Scope[M]) ScopedQuery[M] { return ScopedQuery[M]{root: root} }\n", After: "func From[M any](root Scope[M]) ScopedQuery[M] { return ScopedQuery[M]{root: root} }\nfunc (query ScopedQuery[M]) Raw(string) ScopedQuery[M] { return query }\nfunc (query ScopedQuery[M]) Exec() {}\nfunc (query ScopedQuery[M]) DB() {}\nfunc (query ScopedQuery[M]) Delete() {}\n"}},
			Tests:   []Test{{Package: "./golem", Name: "TestP6ScopedPublicCompileFailRedTeam"}},
		},
		{
			Label: "MIX_SCOPE_NONCE", Summary: "accept a field from another scoped query",
			Patches: []Patch{{Path: "golem/scoped.go", Before: "\t\tif value.queryID != query.root.queryID || known[value.occurrence] != value.model || value.kind == 0 || (value.kind != ScopedExpressionCountAll && value.field == (FieldID{})) {\n", After: "\t\tif known[value.occurrence] != value.model || value.kind == 0 || (value.kind != ScopedExpressionCountAll && value.field == (FieldID{})) {\n"}},
			Tests:   []Test{{Package: "./golem", Name: "TestP6ScopedRuntimeForgeryAndMixedRootCorpusTouchesDatabaseZeroTimes"}},
		},
		{
			Label: "AUDIT_ONLY_SUCCESS", Summary: "omit failed/cancelled scoped audits",
			Patches: []Patch{{Path: "runtime/scoped.go", Before: "\tdefer func() {\n\t\treportScoped(ctx, app, request, auditID, execution, system, statementSQL, started, int64(len(result)), outcome)\n\t}()\n", After: "\tdefer func() {\n\t\tif outcome == golem.ScopedOutcomeSucceeded {\n\t\t\treportScoped(ctx, app, request, auditID, execution, system, statementSQL, started, int64(len(result)), outcome)\n\t\t}\n\t}()\n"}},
			Tests:   runtimeProvider("TestP6ScopedAuditSuccessFailureCancellationAndTx"),
		},
		{
			Label: "AUDIT_RAW_SQL_OR_VALUES", Summary: "leak predicate values and principal correlation into audit shape",
			Patches: []Patch{{Path: "golem/scoped.go", Before: "\tshapeText := fmt.Sprintf(\"%x|%v|%v|%v|%v|take=%s|skip=%s|where=%s|having=%s\", query.root, query.joins, query.selections, query.groupBy, query.orders, scopedPageShape(query.take), scopedPageShape(query.skip), scopedPredicateShape(query.where), scopedPredicateShape(query.having))\n", After: "\tshapeText := fmt.Sprintf(\"%x|%v|%v|%v|%v|take=%s|skip=%s|where=%#v|having=%#v|principal=%s\", query.root, query.joins, query.selections, query.groupBy, query.orders, scopedPageShape(query.take), scopedPageShape(query.skip), query.where, query.having, principal)\n"}},
			Tests:   []Test{{Package: "./golem", Name: "TestP6ScopedAuditShapeExcludesValuesButIncludesSignedPaging"}},
		},
		{
			Label: "GRAPHQL_SECOND_ENGINE", Summary: "authorize, render, and query directly in the GraphQL bridge before shared execution",
			Patches: []Patch{
				{Path: "runtime/graphql_analytics.go", Before: "\t\"github.com/eleven-am/golem/go/golem\"\n", After: "\t\"github.com/eleven-am/golem/go/golem\"\n\tanalytics \"github.com/eleven-am/golem/go/internal/analytics\"\n"},
				{Path: "runtime/graphql_analytics.go", Before: "\treturn executeAnalyticsWithMode(ctx, caller.app, caller.executor, caller.policies, false, request.ModelID(), request, false)\n", After: "\tplanned, err := analytics.Caller(request, caller.app.registry, caller.app.providers, caller.policies, caller.app.readLimits.plan)\n\tif err != nil { return nil, err }\n\tstatement, err := analytics.Render(planned, caller.app.registry, caller.app.provider, caller.app.capabilities)\n\tif err != nil { return nil, err }\n\tqueryer, err := caller.executor.queryerFor(caller.app.database)\n\tif err != nil { return nil, err }\n\trows, err := queryer.QueryxContext(ctx, statement.SQL(), statement.Args()...)\n\tif err != nil { return nil, err }\n\t_ = rows.Close()\n\treturn executeAnalyticsWithMode(ctx, caller.app, caller.executor, caller.policies, false, request.ModelID(), request, false)\n"},
			},
			Tests: runtimeProvider("TestP6GraphQLAndGoAnalyticsPlanPolicySQLAndResultOracle"),
		},
		{
			Label: "EMIT_ANALYTICS_BY_RESERVED_NAME", Summary: "emit aggregate root whenever its reserved name exists",
			Patches: []Patch{{Path: "internal/graphql/schema/schema.go", Before: "\t\tif enabled[ir.OperationAggregate] {\n\t\t\tfmt.Fprintf(&query, \"  %s(where: %sWhereInput): %sAggregate!\\n\", roots.Aggregate, name, name)\n\t\t\tqueryCount++\n\t\t}\n", After: "\t\tif roots.Aggregate != \"\" {\n\t\t\tfmt.Fprintf(&query, \"  %s(where: %sWhereInput): %sAggregate!\\n\", roots.Aggregate, name, name)\n\t\t\tqueryCount++\n\t\t}\n"}},
			Tests:   []Test{{Package: "./internal/graphql/schema", Name: "TestP6GeneratedGraphQLAnalyticsSDLGolden"}},
		},
		{
			Label: "RUN_AGGREGATE_HOOKS", Summary: "route aggregate through an ordinary hooked FindMany before analytics",
			Patches: []Patch{{Path: "runtime/analytics.go", Before: "\tfrozen, err := golem.RuntimeFreezeAggregateRequest(request)\n\tif err != nil {\n\t\treturn golem.AggregateResult[M]{}, analyticsError(\"aggregate\", descriptor, err)\n\t}\n\trows, err := executeAnalytics(ctx, caller.app, caller.executor, caller.policies, false, descriptor.Metadata().ModelID(), frozen)\n", After: "\tfrozen, err := golem.RuntimeFreezeAggregateRequest(request)\n\tif err != nil {\n\t\treturn golem.AggregateResult[M]{}, analyticsError(\"aggregate\", descriptor, err)\n\t}\n\tif _, err := CallerFindMany(ctx, caller, descriptor); err != nil {\n\t\treturn golem.AggregateResult[M]{}, err\n\t}\n\trows, err := executeAnalytics(ctx, caller.app, caller.executor, caller.policies, false, descriptor.Metadata().ModelID(), frozen)\n"}},
			Tests:   runtimeProvider("TestP6AnalyticsAndScopedNeverInvokeOrdinaryReadHooks"),
		},
	}
}

func Find(label string) (Mutation, bool) {
	for _, mutation := range Catalog() {
		if mutation.Label == label {
			return mutation, true
		}
	}
	return Mutation{}, false
}
