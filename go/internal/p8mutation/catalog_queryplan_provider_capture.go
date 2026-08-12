package p8mutation

import "time"

// queryPlanProviderCaptureMutations remains isolated until the root owner
// aggregates the Phase-6 inventory into the shared catalog.
func queryPlanProviderCaptureMutations() []Mutation {
	gate := func(pkg, test string) Gate { return Gate{Directory: "go", Package: pkg, Test: test} }
	return []Mutation{
		{
			Label: "QUERYPLAN_SQLITE_USES_BYTECODE_EXPLAIN", Summary: "use SQLite bytecode EXPLAIN instead of the bounded structural query-plan form",
			Patches: []Patch{{Path: "go/internal/provider/sqlite/query_plan_capture.go", Before: `const sqliteQueryPlanPrefix = "EXPLAIN QUERY PLAN "`, After: `const sqliteQueryPlanPrefix = "EXPLAIN "`}},
			Gate:    gate("./internal/provider/sqlite", "TestQueryPlanSQLiteNeverExecutesDataQueryAndClosesRows"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_POSTGRES_ANALYZE_TRUE", Summary: "execute the PostgreSQL data statement while obtaining its plan",
			Patches: []Patch{{Path: "go/internal/provider/postgresql/query_plan_capture.go", Before: `const postgresQueryPlanPrefix = "EXPLAIN (FORMAT JSON, ANALYZE FALSE, VERBOSE FALSE, COSTS FALSE, SETTINGS FALSE, BUFFERS FALSE, WAL FALSE, SUMMARY FALSE) "`, After: `const postgresQueryPlanPrefix = "EXPLAIN (FORMAT JSON, ANALYZE TRUE, VERBOSE FALSE, COSTS FALSE, SETTINGS FALSE, BUFFERS FALSE, WAL FALSE, SUMMARY FALSE) "`}},
			Gate:    gate("./internal/provider/postgresql", "TestQueryPlanPostgreSQLMapsScanJoinSortAndIndexWithoutRawJSON"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_SQLITE_RETURNS_RAW_PROVIDER_ERROR", Summary: "return an untrusted SQLite diagnostic through the query-plan boundary",
			Patches: []Patch{{Path: "go/internal/provider/sqlite/query_plan_capture.go", Before: "rows, err := connection.QueryxContext(ctx, sqliteQueryPlanPrefix+statement, arguments...)\n\tif err != nil {\n\t\treturn queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)\n\t}", After: "rows, err := connection.QueryxContext(ctx, sqliteQueryPlanPrefix+statement, arguments...)\n\tif err != nil {\n\t\treturn queryplancapture.Plan{}, err\n\t}"}},
			Gate:    gate("./internal/provider/sqlite", "TestQueryPlanSQLiteOversizeAndProviderFailureAreSanitized"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_POSTGRES_RETURNS_RAW_PROVIDER_ERROR", Summary: "return an untrusted PostgreSQL diagnostic through the query-plan boundary",
			Patches: []Patch{{Path: "go/internal/provider/postgresql/query_plan_capture.go", Before: "rows, err := connection.QueryxContext(ctx, postgresQueryPlanPrefix+statement, arguments...)\n\tif err != nil {\n\t\treturn queryplancapture.Plan{}, queryplancapture.Refuse(queryplancapture.ErrorUnavailable)\n\t}", After: "rows, err := connection.QueryxContext(ctx, postgresQueryPlanPrefix+statement, arguments...)\n\tif err != nil {\n\t\treturn queryplancapture.Plan{}, err\n\t}"}},
			Gate:    gate("./internal/provider/postgresql", "TestQueryPlanPostgreSQLUnknownOversizeAndDepthFailClosed"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ALIAS_MAP_GUESSES_AMBIGUITY", Summary: "choose one renderer fact when an exact provider alias is ambiguous",
			Patches: []Patch{{Path: "go/internal/queryplancapture/capture.go", Before: "if found {\n\t\t\treturn AliasIdentity{}, MatchAmbiguous\n\t\t}", After: "if found {\n\t\t\treturn result, MatchExact\n\t\t}"}},
			Gate:    gate("./internal/queryplancapture", "TestAliasMapIsOpaqueExactAndAmbiguityFailClosed"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_SQLITE_LEAKS_ROWS_ON_REFUSAL", Summary: "return an oversized SQLite plan while provider rows remain held",
			Patches: []Patch{{Path: "go/internal/provider/sqlite/query_plan_capture.go", Before: "closeErr := rows.Close()", After: "var closeErr error"}},
			Gate:    gate("./internal/provider/sqlite", "TestQueryPlanSQLiteOversizeAndProviderFailureAreSanitized"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_POSTGRES_LEAKS_ROWS_ON_REFUSAL", Summary: "return an oversized PostgreSQL plan while provider rows remain held",
			Patches: []Patch{{Path: "go/internal/provider/postgresql/query_plan_capture.go", Before: "closeErr := rows.Close()", After: "var closeErr error"}},
			Gate:    gate("./internal/provider/postgresql", "TestQueryPlanPostgreSQLUnknownOversizeAndDepthFailClosed"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_POSTGRES_ACCEPTS_OVERSIZED_RAW_PLAN", Summary: "allocate and sanitize a PostgreSQL plan larger than the raw provider ceiling",
			Patches: []Patch{
				{Path: "go/internal/provider/postgresql/query_plan_capture.go", Before: "if len(raw) > queryplancapture.MaxRawBytes {", After: "if false && len(raw) > queryplancapture.MaxRawBytes {"},
				{Path: "go/internal/provider/postgresql/query_plan_capture.go", Before: "if len(raw) == 0 || len(raw) > queryplancapture.MaxRawBytes {", After: "if len(raw) == 0 {"},
			},
			Gate: gate("./internal/provider/postgresql", "TestQueryPlanPostgreSQLUnknownOversizeAndDepthFailClosed"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_SQLITE_DERIVED_ALIAS_CLAIMS_ACCESS", Summary: "turn a derived analytics alias into a physical SQLite full scan",
			Patches: []Patch{{Path: "go/internal/provider/sqlite/query_plan_capture.go", Before: "if identity.Role() != queryplancapture.AliasPhysicalAccess && identity.Role() != queryplancapture.AliasCorrelatedRelation {\n\t\treturn structuralForAlias(identity, children)\n\t}", After: "if false && identity.Role() != queryplancapture.AliasPhysicalAccess && identity.Role() != queryplancapture.AliasCorrelatedRelation {\n\t\treturn structuralForAlias(identity, children)\n\t}"}},
			Gate:    gate("./internal/provider/sqlite", "TestQueryPlanSQLiteDerivedAliasCannotBecomePhysicalAccess"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_POSTGRES_DERIVED_ALIAS_CLAIMS_ACCESS", Summary: "turn a derived analytics alias into a physical PostgreSQL full scan",
			Patches: []Patch{{Path: "go/internal/provider/postgresql/query_plan_capture.go", Before: "if identity.Role() != queryplancapture.AliasPhysicalAccess && identity.Role() != queryplancapture.AliasCorrelatedRelation {\n\t\treturn postgresDerivedOrUnknown(identity, status, children)\n\t}", After: "if false && identity.Role() != queryplancapture.AliasPhysicalAccess && identity.Role() != queryplancapture.AliasCorrelatedRelation {\n\t\treturn postgresDerivedOrUnknown(identity, status, children)\n\t}"}},
			Gate:    gate("./internal/provider/postgresql", "TestQueryPlanPostgreSQLDerivedAliasCannotBecomePhysicalAccess"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_POSTGRES_ACCEPTS_DUPLICATE_JSON_KEY", Summary: "let a later duplicate provider key reinterpret an already parsed plan fact",
			Patches: []Patch{{Path: "go/internal/provider/postgresql/query_plan_capture.go", Before: "if _, duplicate := frame.keys[key]; duplicate {\n\t\t\t\treturn queryplancapture.Refuse(queryplancapture.ErrorUnavailable)\n\t\t\t}", After: "if _, duplicate := frame.keys[key]; duplicate && false {\n\t\t\t\treturn queryplancapture.Refuse(queryplancapture.ErrorUnavailable)\n\t\t\t}"}},
			Gate:    gate("./internal/provider/postgresql", "TestQueryPlanPostgreSQLUnknownOversizeAndDepthFailClosed"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_ACCEPTS_ADDITIONAL_RENDERED_STATEMENT", Summary: "allow a second statement through the renderer-owned read boundary",
			Patches: []Patch{{Path: "go/internal/queryplancapture/capture.go", Before: `strings.ContainsAny(trimmed, ";\x00")`, After: `strings.ContainsAny(trimmed, "\x00")`}},
			Gate:    gate("./internal/queryplancapture", "TestRenderedReadSQLBoundaryRejectsAdditionalStatementsAndComments"), Timeout: 2 * time.Minute,
		},
	}
}
