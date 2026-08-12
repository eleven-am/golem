package p8mutation

import "time"

// eventTransportTopologyMutations is isolated from the global catalog until
// the Order-7 runtime topology and readiness seam is independently reviewed.
func eventTransportTopologyMutations() []Mutation {
	gate := func(test string) Gate {
		return Gate{Directory: "go", Package: "./runtime", Test: test}
	}
	mutation := func(label, summary, path, before, after, test string) Mutation {
		return Mutation{
			Label: label, Summary: summary,
			Patches: []Patch{{Path: path, Before: before, After: after}},
			Gate:    gate(test), Timeout: 2 * time.Minute,
		}
	}
	return []Mutation{
		mutation(
			"EVENT_TOPOLOGY_ACCEPTS_SQLITE_CROSS_PROCESS",
			"allow a cross-process transport to pretend SQLite is a multi-node database profile",
			"go/runtime/runtime.go",
			"if providerIdentity != golem.PostgreSQL {",
			"if providerIdentity == \"\" {",
			"TestOrder7SQLiteRejectsCrossProcessTransportBeforeRuntimeBinding",
		),
		mutation(
			"EVENT_TOPOLOGY_ACCEPTS_CROSS_PROCESS_WITHOUT_BINDING",
			"accept a cross-process transport that cannot receive the sealed runtime decoder capability",
			"go/runtime/runtime.go",
			"if _, ok := transport.(events.RuntimeBindableTransport); !ok {",
			"if _, ok := transport.(events.RuntimeBindableTransport); false && !ok {",
			"TestP7CrossProcessTransportWithoutRuntimeBindingIsRejected",
		),
		mutation(
			"EVENT_TOPOLOGY_IGNORES_STARTUP_UNAVAILABILITY",
			"accept a required transport whose external dependency reports unavailable",
			"go/runtime/runtime.go",
			"if !events.AvailabilityOf(config.EventTransport) {",
			"if false && !events.AvailabilityOf(config.EventTransport) {",
			"TestOrder7UnavailableCrossProcessTransportRefusesBeforeRuntimeBinding",
		),
		mutation(
			"EVENT_TOPOLOGY_IGNORES_PREBIND_AVAILABILITY_CHANGE",
			"bind a transport that became unavailable after configuration validation",
			"go/runtime/event_lifecycle.go",
			"if !events.AvailabilityOf(app.eventTransport) {",
			"if false && !events.AvailabilityOf(app.eventTransport) {",
			"TestOrder7TransportBecomingUnavailableBeforeBindRetainsNoRuntimeCapability",
		),
		mutation(
			"EVENT_TOPOLOGY_REPORTS_STALE_AVAILABILITY",
			"report a transport as available after its external dependency became unavailable",
			"go/runtime/event_lifecycle.go",
			"events.AvailabilityOf(app.eventTransport),",
			"true,",
			"TestOrder7PostgreSQLAcceptsAvailableCrossProcessTransportAndBindsOnce",
		),
		mutation(
			"EVENT_TOPOLOGY_SKIPS_RUNTIME_BINDING",
			"publish a PostgreSQL application without binding its cross-process transport exactly once",
			"go/runtime/event_lifecycle.go",
			"if bindableOK {",
			"if false && bindableOK {",
			"TestOrder7PostgreSQLAcceptsAvailableCrossProcessTransportAndBindsOnce",
		),
		mutation(
			"EVENT_TOPOLOGY_ACCEPTS_MISSING_PAYLOAD_LIMIT",
			"accept an external cross-process transport without a closed encoded-payload limit",
			"go/runtime/runtime.go",
			"if !ok {\n\t\treturn fmt.Errorf(\"GOLEM_EVENT_CONFIG: cross-process event transport must report a positive encoded payload limit\")\n\t}",
			"if false && !ok {\n\t\treturn fmt.Errorf(\"GOLEM_EVENT_CONFIG: cross-process event transport must report a positive encoded payload limit\")\n\t}",
			"TestOrder7CrossProcessTransportWithoutPayloadLimitRefusesBeforeBinding",
		),
		mutation(
			"EVENT_TOPOLOGY_ACCEPTS_UNDERSIZED_PAYLOAD_LIMIT",
			"accept an external cross-process transport whose payload ceiling is below the configured event maximum",
			"go/runtime/runtime.go",
			"if limit < required {",
			"if false && limit < required {",
			"TestOrder7CrossProcessPayloadLimitBelowConfiguredMaximumRefusesBeforeBinding",
		),
	}
}
