package p8mutation

import "time"

// queryPlanRuntimeMutations prove the Caller-only orchestration boundary: hook
// transforms are reauthorized, Explain never runs the data statement, database
// resources are released before observation, and provider failures stay closed.
func queryPlanRuntimeMutations() []Mutation {
	gate := func(test string) Gate {
		return Gate{Directory: "go", Package: "./runtime", Test: test}
	}
	return []Mutation{
		{
			Label: "QUERYPLAN_RUNTIME_RUNS_HOOK_BEFORE_INPUT_VALIDATION", Summary: "run a side-effecting before hook before validating the original typed read input",
			Patches: []Patch{{Path: "go/runtime/runtime.go", Before: "\tif _, err := golem.FreezeFindMany(descriptor, options...); err != nil {\n\t\treturn preparedReadStatement{}, err\n\t}\n\thookContext := golem.RuntimeContextWithActor(ctx, caller.actor)", After: "\thookContext := golem.RuntimeContextWithActor(ctx, caller.actor)"}},
			Gate:    gate("TestQueryPlanInvalidOriginalInputRunsNoHookAndNoProviderStatement"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_RUNTIME_USES_PREHOOK_INPUT", Summary: "discard a before-read hook transform before reauthorization and rendering",
			Patches: []Patch{{Path: "go/runtime/runtime.go", Before: "\tfrozen, err := golem.FreezeFindMany(descriptor, hookRequest.Options()...)", After: "\tfrozen, err := golem.FreezeFindMany(descriptor, options...)"}},
			Gate:    gate("TestQueryPlanAuthorizationHooksAndExecutionPreparationAreOneBoundary"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_RUNTIME_EXECUTES_DATA_STATEMENT", Summary: "run the authorized data query before asking the provider for its plan",
			Patches: []Patch{{Path: "go/runtime/queryplan.go", Before: "\tobserveexec.RecordStatement(ctx)\n\tvar captured queryplancapture.Plan", After: "\tobserveexec.RecordStatement(ctx)\n\tdataRows, dataErr := connection.QueryxContext(ctx, statement, arguments...)\n\tif dataErr != nil {\n\t\t_ = connection.Close()\n\t\treturn queryplancapture.Plan{}, publicCaptureError(dataErr)\n\t}\n\t_ = dataRows.Close()\n\tvar captured queryplancapture.Plan"}},
			Gate:    gate("TestQueryPlanAuthorizationHooksAndExecutionPreparationAreOneBoundary"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_RUNTIME_RELEASES_CONNECTION_AFTER_OBSERVATION", Summary: "retain the explicit planning connection while the buffered observer is emitted",
			Patches: []Patch{{Path: "go/runtime/queryplan.go", Before: "\tcloseErr := connection.Close()", After: "\tvar closeErr error"}},
			Gate:    gate("TestQueryPlanReleasesMaxOpenOneConnectionBeforeBlockingOrPanickingObserver"), Timeout: 2 * time.Minute,
		},
		{
			Label: "QUERYPLAN_RUNTIME_RETURNS_RAW_CONNECTION_ERROR", Summary: "return the database connection error across the public query-plan boundary",
			Patches: []Patch{{Path: "go/runtime/queryplan.go", Before: "\tif err != nil {\n\t\treturn queryplancapture.Plan{}, queryplanreport.NewError(queryplanreport.CodeUnavailable)\n\t}", After: "\tif err != nil {\n\t\treturn queryplancapture.Plan{}, err\n\t}"}},
			Gate:    gate("TestQueryPlanProviderFailureIsClosedAndReturnsNoPartialReport"), Timeout: 2 * time.Minute,
		},
	}
}
