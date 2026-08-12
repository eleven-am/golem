package p8mutation

import "time"

// socialNATSHostMutations remains isolated from the global catalog until the
// Order-7 social host lifecycle and live Core NATS evidence are complete.
func socialNATSHostMutations() []Mutation {
	gate := func(test string) Gate {
		return Gate{
			Directory: "go/examples/social", Package: "./cmd/social", Test: test,
			WorkspaceModules: []string{"go", "go/examples/social"},
		}
	}
	mutation := func(label, summary, before, after, test string) Mutation {
		return Mutation{
			Label: label, Summary: summary,
			Patches: []Patch{{Path: "go/examples/social/cmd/social/main.go", Before: before, After: after}},
			Gate:    gate(test), Timeout: 4 * time.Minute,
		}
	}
	multiPatch := func(label, summary, test string, patches ...Patch) Mutation {
		return Mutation{Label: label, Summary: summary, Patches: patches, Gate: gate(test), Timeout: 4 * time.Minute}
	}
	return []Mutation{
		mutation(
			"SOCIAL_HOST_DEFAULTS_NATS",
			"default the single-process host to NATS rather than the process-local memory transport",
			"string(hostEventTransportMemory)", "string(hostEventTransportNATS)",
			"TestOrder7SocialHostTransportConfigurationIsProviderClosed",
		),
		mutation(
			"SOCIAL_HOST_IGNORES_STRAY_NATS_CONFIG",
			"silently ignore NATS settings while the memory transport is selected",
			"if urlsValue != \"\" || prefix != \"\" {", "if false && (urlsValue != \"\" || prefix != \"\") {",
			"TestOrder7SocialHostTransportConfigurationIsProviderClosed",
		),
		mutation(
			"SOCIAL_HOST_ALLOWS_SQLITE_NATS",
			"allow SQLite to reach the NATS transport opener",
			"if providerKind != golem.PostgreSQL || urlsValue == \"\" || prefix == \"\" {", "if false && providerKind != golem.PostgreSQL || urlsValue == \"\" || prefix == \"\" {",
			"TestOrder7SocialHostRejectsSQLiteNATSBeforeOpeningTransport",
		),
		mutation(
			"SOCIAL_HOST_OMITS_NATS_OBSERVER",
			"open NATS without the host-owned closed reconnect observation sink",
			"SubjectPrefix: prefix, Observer: hostEventObserver{}", "SubjectPrefix: prefix",
			"TestOrder7SocialHostTransportConfigurationIsProviderClosed",
		),
		multiPatch(
			"SOCIAL_HOST_LEAKS_RECONNECT_CONTEXT",
			"log an untrusted reconnect context value instead of only closed observation fields",
			"TestOrder7SocialHostObserverEmitsOnlyClosedReconnectFields",
			Patch{Path: "go/examples/social/cmd/social/main.go", Before: "func (hostEventObserver) ObserveEvent(_ context.Context, observation events.Observation) {", After: "func (hostEventObserver) ObserveEvent(ctx context.Context, observation events.Observation) {"},
			Patch{Path: "go/examples/social/cmd/social/main.go", Before: "log.Printf(\"event_transport kind=%s outcome=%s count=%d\", observation.Kind(), observation.Outcome(), observation.AggregateCount())", After: "log.Printf(\"event_transport kind=%s outcome=%s count=%d context=%v\", observation.Kind(), observation.Outcome(), observation.AggregateCount(), ctx)"},
		),
		mutation(
			"SOCIAL_HOST_IGNORES_TRANSPORT_READINESS",
			"admit traffic while the configured external event transport is unavailable",
			"capabilities.PublisherRunning() && capabilities.TransportAvailable()", "capabilities.PublisherRunning()",
			"TestOrder7SocialReadinessTracksDynamicTransportAvailability",
		),
		mutation(
			"SOCIAL_HOST_LISTENS_BEFORE_READINESS",
			"treat every startup state as ready and create the HTTP server before publisher readiness",
			"if ready(startupCtx, database, application) {", "if true || ready(startupCtx, database, application) {",
			"TestOrder7SocialHostStartsListeningOnlyAfterBoundedReadiness",
		),
		mutation(
			"SOCIAL_HOST_IGNORES_EARLY_PUBLISHER_EXIT",
			"ignore a publisher that exits before startup readiness",
			"case err := <-publisherStopped:", "case err := <-make(chan error):",
			"TestOrder7SocialHostPropagatesTerminalHTTPAndPublisherFailures/publisher",
		),
		mutation(
			"SOCIAL_HOST_CREATES_SERVER_AFTER_READINESS_TIMEOUT",
			"accept a timed-out readiness barrier as successful admission",
			"return false, errors.New(\"host readiness timed out\")", "return false, nil",
			"TestOrder7SocialHostReadinessTimeoutNeverCreatesServer",
		),
		mutation(
			"SOCIAL_HOST_IGNORES_HTTP_TERMINATION",
			"discard an unexpected HTTP listener termination",
			"terminalError = errors.New(\"HTTP server stopped\")", "terminalError = nil",
			"TestOrder7SocialHostPropagatesTerminalHTTPAndPublisherFailures/http",
		),
		mutation(
			"SOCIAL_HOST_IGNORES_PUBLISHER_TERMINATION",
			"discard an unexpected publisher termination during shutdown",
			"failures = append(failures, errors.New(\"event publisher stopped\"))", "_ = err",
			"TestOrder7SocialHostPropagatesPublisherExitAfterReadiness",
		),
		mutation(
			"SOCIAL_HOST_CLOSES_AFTER_HTTP_SHUTDOWN_FAILURE",
			"continue closing downstream owners when HTTP has not relinquished request ownership",
			"return errors.New(\"HTTP shutdown failed\")", "failures = append(failures, errors.New(\"HTTP shutdown failed\"))",
			"TestOrder7SocialHostShutdownFailuresStopAtOwnershipBarriersAndStayRedacted/http",
		),
		mutation(
			"SOCIAL_HOST_CLOSES_AFTER_GRAPHQL_SHUTDOWN_FAILURE",
			"continue closing downstream owners when GraphQL has not relinquished connection ownership",
			"return errors.New(\"GraphQL shutdown failed\")", "failures = append(failures, errors.New(\"GraphQL shutdown failed\"))",
			"TestOrder7SocialHostShutdownFailuresStopAtOwnershipBarriersAndStayRedacted/graphql",
		),
		mutation(
			"SOCIAL_HOST_CLOSES_AFTER_PUBLISHER_TIMEOUT",
			"close the transport and database while the publisher may still own them",
			"failures = append(failures, errors.New(\"event publisher shutdown timed out\"))\n\t\t\treturn errors.Join(failures...)", "failures = append(failures, errors.New(\"event publisher shutdown timed out\"))",
			"TestOrder7SocialHostPublisherTimeoutDoesNotRaceOwnedDependencies",
		),
		mutation(
			"SOCIAL_HOST_CLOSES_DATABASE_AFTER_TRANSPORT_FAILURE",
			"close the database after the transport failed to relinquish ownership",
			"failures = append(failures, errors.New(\"event transport shutdown failed\"))\n\t\t\treturn errors.Join(failures...)", "failures = append(failures, errors.New(\"event transport shutdown failed\"))",
			"TestOrder7SocialHostShutdownFailuresStopAtOwnershipBarriersAndStayRedacted/transport",
		),
		mutation(
			"SOCIAL_HOST_LEAKS_CLEANUP_ERROR",
			"return an upstream cleanup error that may contain database or broker secrets",
			"failures = append(failures, errors.New(\"database shutdown failed\"))", "failures = append(failures, err)",
			"TestOrder7SocialHostShutdownFailuresStopAtOwnershipBarriersAndStayRedacted/database",
		),
	}
}
