package p8mutation

import "time"

// natsTransportMutations remains isolated from the global release catalog
// until the Order 7 live Core NATS evidence is reviewed.
func natsTransportMutations() []Mutation {
	gate := func(test string) Gate { return Gate{Directory: "go", Package: "./events/nats", Test: test} }
	mutation := func(label, summary, path, before, after, test string) Mutation {
		return Mutation{Label: label, Summary: summary, Patches: []Patch{{Path: path, Before: before, After: after}}, Gate: gate(test), Timeout: 2 * time.Minute}
	}
	return []Mutation{
		mutation("NATS_TRANSPORT_DIALS_SQLITE", "dial NATS before proving a live PostgreSQL handle", "go/events/nats/transport.go",
			"database.Provider() != golem.PostgreSQL || database.UnsafeSQLX() == nil", "database.Provider() == \"\" || database.UnsafeSQLX() == nil", "TestOrder7OpenRejectsSQLiteBeforeDial"),
		mutation("NATS_TRANSPORT_DEFAULTS_SUBJECT_PREFIX", "accept an absent deployment-unique subject prefix", "go/events/nats/config.go",
			"if len(output.SubjectPrefix) == 0 || len(output.SubjectPrefix) > maximumSubjectPrefixBytes || !canonicalSubjectPrefix.MatchString(output.SubjectPrefix) {", "if output.SubjectPrefix == \"\" {\n\t\toutput.SubjectPrefix = \"golem\"\n\t}\n\tif len(output.SubjectPrefix) > maximumSubjectPrefixBytes || !canonicalSubjectPrefix.MatchString(output.SubjectPrefix) {", "TestOrder7ConfigIsClosedBoundedAndRedacted"),
		mutation("NATS_TRANSPORT_ROUTES_BY_GENERATION", "route publication by immutable generation instead of logical event schema", "go/events/nats/transport.go",
			"transport.subject(notice.EventSchemaDigest(), notice.ModelID())", "transport.subject(golem.EventSchemaDigest(notice.GenerationDigest()), notice.ModelID())", "TestOrder7SubjectRoutesByEventSchemaAndModelNotGeneration"),
		mutation("NATS_TRANSPORT_OMITS_PUBLISH_FLUSH", "acknowledge a causal batch without a context-bound broker flush", "go/events/nats/transport.go",
			"if err := client.FlushWithContext(flushCtx); err != nil {\n\t\treturn events.Failure(events.CodeEventTransport)\n\t}\n\treturn nil\n}\n\nfunc (transport *Transport) Subscribe", "_ = flushCtx // mutant: batch not flushed\n\treturn nil\n}\n\nfunc (transport *Transport) Subscribe", "TestOrder7PublishEnqueuesWholeBatchThenFlushesOnceWithDeadline"),
		mutation("NATS_TRANSPORT_OMITS_SUBSCRIBE_FLUSH", "return a stream before the exact subscription reaches the broker", "go/events/nats/transport.go",
			"if err := client.FlushWithContext(flushCtx); err != nil {\n\t\t_ = result.closeWith(events.CodeEventTransport)\n\t\treturn nil, events.Failure(events.CodeEventTransport)\n\t}\n\treturn result, nil", "_ = flushCtx // mutant: subscription registration not flushed\n\treturn result, nil", "TestOrder7SubscribeFlushesRegistrationBeforeSuccess"),
		mutation("NATS_TRANSPORT_REBINDS_DECODER", "allow the sealed runtime decoder to be replaced", "go/events/nats/transport.go",
			"if transport.bound || transport.closed {", "if transport.closed {", "TestOrder7BindExactlyOnceAndStartsNoBrokerWork"),
		mutation("NATS_TRANSPORT_ACCEPTS_FOREIGN_EVENT_SCHEMA", "accept decoded bytes for a different logical event schema", "go/events/nats/stream.go",
			"notice.EventSchemaDigest() != stream.requested.EventSchemaDigest() || notice.ModelID() != stream.requested.ModelID()", "false || notice.ModelID() != stream.requested.ModelID()", "TestOrder7ForeignDecodedRouteAndOverflowCloseOnlyStream/foreign_decoded_identity"),
		mutation("NATS_TRANSPORT_ACCEPTS_FOREIGN_MODEL", "accept decoded bytes for a different model", "go/events/nats/stream.go",
			"notice.EventSchemaDigest() != stream.requested.EventSchemaDigest() || notice.ModelID() != stream.requested.ModelID()", "notice.EventSchemaDigest() != stream.requested.EventSchemaDigest() || false", "TestOrder7ForeignDecodedRouteAndOverflowCloseOnlyStream/foreign_decoded_model"),
		mutation("NATS_TRANSPORT_USES_UNBOUNDED_STREAM_CAPACITY", "replace the configured stream message bound with the hard maximum", "go/events/nats/transport.go",
			"newStream(transport, requested, transport.config.StreamBuffer)", "newStream(transport, requested, maximumStreamBuffer)", "TestOrder7ForeignDecodedRouteAndOverflowCloseOnlyStream/bounded_queue"),
		mutation("NATS_TRANSPORT_IGNORES_STREAM_BYTE_BOUND", "allow queued encoded bytes to exceed the configured pending-byte limit", "go/events/nats/stream.go",
			"if stream.queuedBytes+len(payload) > stream.transport.config.PendingBytes {", "if false && stream.queuedBytes+len(payload) > stream.transport.config.PendingBytes {", "TestOrder7ForeignDecodedRouteAndOverflowCloseOnlyStream/bounded_bytes"),
		mutation("NATS_TRANSPORT_GROWS_OBSERVER_QUEUE", "raise the fixed reconnect observation bound under blocked instrumentation", "go/events/nats/config.go",
			"maximumReconnectObservations = 64", "maximumReconnectObservations = 1024", "TestOrder7BlockedObserverCannotGrowReconnectQueueWithoutBound"),
		mutation("NATS_TRANSPORT_REORDERS_OBSERVATIONS", "allow a second observation drainer to overtake a blocked transition", "go/events/nats/transport.go",
			"if transport.observing {\n\t\treturn false\n\t}", "if transport.observing {\n\t\treturn true\n\t}", "TestOrder7ReconnectObservationsSerializeTransitionsAndSuppressDuplicates"),
		mutation("NATS_TRANSPORT_OBSERVES_UNDER_CALLBACK_LOCK", "invoke untrusted reconnect instrumentation while holding the callback lock", "go/events/nats/transport.go",
			"transport.available.Store(false)\n\ttransport.mu.Unlock()\n\tdrain := transport.queueReconnectObservation(events.OutcomeFailure)\n\ttransport.callbackMu.Unlock()\n\tif drain {\n\t\ttransport.drainReconnectObservations()\n\t}", "transport.available.Store(false)\n\ttransport.mu.Unlock()\n\tdrain := transport.queueReconnectObservation(events.OutcomeFailure)\n\tif drain {\n\t\ttransport.drainReconnectObservations()\n\t}\n\ttransport.callbackMu.Unlock()", "TestOrder7ReconnectObserverCanReenterCloseWithoutDeadlock"),
		mutation("NATS_TRANSPORT_IGNORES_CALLER_CANCELLATION", "leave a connected socket open while the initial protocol handshake ignores cancellation", "go/events/nats/transport.go",
			"stop := context.AfterFunc(dialer.ctx, func() {", "stop := context.AfterFunc(context.Background(), func() {", "TestOrder7CancellationDuringProtocolHandshakeReturnsPromptlyAndClosesConnection"),
		mutation("NATS_TRANSPORT_REVIVES_TERMINAL_CLOSE", "allow reconnect to restore availability after terminal closure", "go/events/nats/transport.go",
			"if transport.closed {\n\t\ttransport.mu.Unlock()\n\t\ttransport.callbackMu.Unlock()\n\t\treturn false\n\t}", "if false && transport.closed {\n\t\ttransport.mu.Unlock()\n\t\ttransport.callbackMu.Unlock()\n\t\treturn false\n\t}", "TestOrder7AvailabilityCallbacksAreClosedAndTerminalCloseUnblocksStreams"),
		mutation("NATS_TRANSPORT_IGNORES_RECONNECT_PAYLOAD", "accept a reconnect whose broker payload ceiling is below the reported limit", "go/events/nats/transport.go",
			"if maxPayload < int64(transport.config.MaxInboundPayloadBytes) {", "if false && maxPayload < int64(transport.config.MaxInboundPayloadBytes) {", "TestOrder7ReconnectRejectsBrokerWithReducedPayloadCeiling"),
		mutation("NATS_TRANSPORT_IGNORES_INITIAL_PAYLOAD", "accept an initial broker payload ceiling below the configured envelope", "go/events/nats/transport.go",
			"if err != nil || client == nil || !client.IsConnected() || client.MaxPayload() < int64(normalized.MaxInboundPayloadBytes) {", "if err != nil || client == nil || !client.IsConnected() {", "TestOrder7BrokerPayloadCeilingMustCoverConfiguredEnvelope"),
		mutation("NATS_TRANSPORT_MISREPORTS_PAYLOAD_LIMIT", "report a default rather than the configured and broker-verified payload ceiling", "go/events/nats/transport.go",
			"return transport.config.MaxInboundPayloadBytes", "return defaultMaxInboundPayload", "TestOrder7PayloadReporterReturnsConfiguredVerifiedCeiling"),
		mutation("NATS_TRANSPORT_DROPS_CONCURRENT_DISCONNECT", "drop a disconnect callback racing initial client installation", "go/events/nats/transport.go",
			"func (transport *Transport) markDisconnected() {\n\ttransport.callbackMu.Lock()\n\ttransport.mu.Lock()", "func (transport *Transport) markDisconnected() {\n\tif !transport.mu.TryLock() {\n\t\treturn\n\t}", "TestOrder7DisconnectCannotBeOverwrittenByOpenCompletion"),
		mutation("NATS_TRANSPORT_DISCONNECT_REMAINS_AVAILABLE", "leave readiness true after an established client disconnects", "go/events/nats/transport.go",
			"transport.available.Store(false)\n\ttransport.mu.Unlock()\n\tdrain := transport.queueReconnectObservation(events.OutcomeFailure)", "transport.available.Store(true)\n\ttransport.mu.Unlock()\n\tdrain := transport.queueReconnectObservation(events.OutcomeFailure)", "TestOrder7AvailabilityCallbacksAreClosedAndTerminalCloseUnblocksStreams"),
		mutation("NATS_TRANSPORT_OBSERVER_GAINS_SUPPRESSION_LABEL", "attach an unrelated suppression label to reconnect telemetry", "go/events/nats/transport.go",
			"events.ObservationTransportReconnect, outcome, \"\", 0, 0, 0, 0, 1", "events.ObservationTransportReconnect, outcome, events.SuppressionFiltered, 0, 0, 0, 0, 1", "TestOrder7ReconnectObservationsAreExactClosedAndPanicSafe"),
		mutation("NATS_TRANSPORT_LEAKS_BROKER_ERROR", "return an upstream broker error containing deployment secrets", "go/events/nats/transport.go",
			"if err := client.FlushWithContext(flushCtx); err != nil {\n\t\treturn events.Failure(events.CodeEventTransport)\n\t}", "if err := client.FlushWithContext(flushCtx); err != nil {\n\t\treturn err\n\t}", "TestOrder7BrokerFailuresAreSealedAndNeverEchoConfiguration"),
		mutation("NATS_TRANSPORT_CLOSE_IS_NOT_IDEMPOTENT", "reset close ownership before each caller close", "go/events/nats/transport.go",
			"func (transport *Transport) Close() error {\n\tif transport == nil {\n\t\treturn nil\n\t}\n\ttransport.once.Do(func() {\n\t\ttransport.callbackMu.Lock()\n\t\ttransport.mu.Lock()\n\t\ttransport.closed = true\n\t\tclient := transport.client\n\t\ttransport.mu.Unlock()\n\t\ttransport.closeStreams(events.CodeEventSourceClosed)\n\t\ttransport.callbackMu.Unlock()\n\t\tif client != nil {\n\t\t\tclient.Close()\n\t\t}\n\t})\n\treturn nil\n}", "func (transport *Transport) Close() error {\n\tif transport == nil {\n\t\treturn nil\n\t}\n\ttransport.callbackMu.Lock()\n\ttransport.mu.Lock()\n\ttransport.closed = true\n\tclient := transport.client\n\ttransport.mu.Unlock()\n\ttransport.closeStreams(events.CodeEventSourceClosed)\n\ttransport.callbackMu.Unlock()\n\tif client != nil {\n\t\tclient.Close()\n\t}\n\treturn nil\n}", "TestOrder7CloseIsConcurrentAndIdempotent"),
	}
}
