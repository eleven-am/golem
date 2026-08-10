package p8mutation

import "time"

func observationWorkflowMutations() []Mutation {
	gate := func(pkg, test string) Gate { return Gate{Directory: "go", Package: pkg, Test: test} }
	providerGate := func(pkg, test string) Gate {
		return Gate{Directory: "go", Package: pkg, Test: test, Required: []string{"GOLEM_TEST_POSTGRES_DSN", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}}
	}
	return []Mutation{
		{
			Label: "TELEMETRY_INCLUDES_RAW_ERROR", Summary: "attach an uncontrolled raw error attribute to slog observations",
			Patches: []Patch{{Path: "go/observe/slog/slog.go", Before: "\t\tstandardslog.Int64(adapterinternal.AttributeNames[12], record.AggregateCount),\n", After: "\t\tstandardslog.Int64(adapterinternal.AttributeNames[12], record.AggregateCount),\n\t\tstandardslog.String(\"error\", \"private-driver-token\"),\n"}},
			Gate:    providerGate("./runtime", "TestP8SlogAndOpenTelemetryAdapterAgreement"), Timeout: 5 * time.Minute,
		},
		{
			Label: "TELEMETRY_INCLUDES_MODEL_OR_FIELD_NAME", Summary: "attach a high-cardinality model name to slog observations",
			Patches: []Patch{{Path: "go/observe/slog/slog.go", Before: "\t\tstandardslog.Int64(adapterinternal.AttributeNames[12], record.AggregateCount),\n", After: "\t\tstandardslog.Int64(adapterinternal.AttributeNames[12], record.AggregateCount),\n\t\tstandardslog.String(\"golem.model_name\", \"Post\"),\n"}},
			Gate:    providerGate("./runtime", "TestP8SlogAndOpenTelemetryAdapterAgreement"), Timeout: 5 * time.Minute,
		},
		{
			Label: "OBSERVER_PANIC_PROPAGATES", Summary: "remove the public observation emitter panic boundary",
			Patches: []Patch{{Path: "go/observe/observe.go", Before: "\t\tfunc() {\n\t\t\tdefer func() { _ = recover() }()\n\t\t\tobserver.ObserveGolem(ctx, observation)\n\t\t}()\n", After: "\t\tobserver.ObserveGolem(ctx, observation)\n"}},
			Gate:    providerGate("./runtime", "TestP8ObserverPanicBlockAndOutageCannotAlterCorrectness"), Timeout: 5 * time.Minute,
		},
		{
			Label: "OBSERVER_QUEUE_UNBOUNDED", Summary: "ignore configured dispatcher capacity and allocate the hard maximum",
			Patches: []Patch{{Path: "go/observe/dispatcher.go", Before: "\t\tqueue:  make(chan Observation, capacity),\n", After: "\t\tqueue:  make(chan Observation, MaximumQueueCapacity),\n"}},
			Gate:    gate("./observe", "TestP8ObservationCardinalityAndBoundedDispatcher"), Timeout: 2 * time.Minute,
		},
		{
			Label: "REQUIRED_PROVIDER_JOB_SKIPS", Summary: "delete the required PostgreSQL linguistic event identity from hosted evidence",
			Patches: []Patch{{Path: "go/internal/workflowaudit/required-tests.json", Before: "    \"provider\": {\n      \"required\": [\n        \"github.com/eleven-am/golem/go/internal/provider/sqlite:TestP8SQLiteClaimDepthSnapshotIsExactAndSerialized\",\n        \"github.com/eleven-am/golem/go/internal/provider/postgresql:TestP8PostgreSQLClaimDepthSnapshotLiveProfiles/c\",\n        \"github.com/eleven-am/golem/go/internal/provider/postgresql:TestP8PostgreSQLClaimDepthSnapshotLiveProfiles/linguistic\"\n      ],\n", After: "    \"provider\": {\n      \"required\": [\n        \"github.com/eleven-am/golem/go/internal/provider/sqlite:TestP8SQLiteClaimDepthSnapshotIsExactAndSerialized\",\n        \"github.com/eleven-am/golem/go/internal/provider/postgresql:TestP8PostgreSQLClaimDepthSnapshotLiveProfiles/c\",\n        \"github.com/eleven-am/golem/go/internal/provider/postgresql:TestP8PostgreSQLClaimDepthSnapshotLiveProfiles/renamed\"\n      ],\n"}},
			Gate:    gate("./internal/workflowaudit", "TestP8WorkflowContainsRequiredHostedGates"), Timeout: 2 * time.Minute,
		},
	}
}
