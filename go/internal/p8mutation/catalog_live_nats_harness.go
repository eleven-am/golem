package p8mutation

import "time"

// liveNATSHarnessMutations remains isolated from the global catalog until the
// Order-7 external Core NATS workflow and documentation are reviewed.
func liveNATSHarnessMutations() []Mutation {
	gate := func(pkg, test string) Gate {
		return Gate{Directory: "go", Package: pkg, Test: test}
	}
	mutation := func(label, summary, path, before, after, pkg, test string) Mutation {
		return Mutation{
			Label: label, Summary: summary,
			Patches: []Patch{{Path: path, Before: before, After: after}},
			Gate:    gate(pkg, test), Timeout: 2 * time.Minute,
		}
	}
	multiPatch := func(label, summary, pkg, test string, patches ...Patch) Mutation {
		return Mutation{Label: label, Summary: summary, Patches: patches, Gate: gate(pkg, test), Timeout: 2 * time.Minute}
	}
	return []Mutation{
		multiPatch(
			"LIVE_NATS_HARNESS_REPLACES_PINNED_IMAGE_DIGEST",
			"replace both the runnable image reference and local-image allowlist with an unreviewed digest",
			"./internal/p8oracle", "TestOrder7NATSLiveAuthorityIsExact",
			Patch{
				Path:   "go/internal/p8oracle/runner_nats.go",
				Before: `order7NATSImage         = "nats:2.14.4@sha256:ecf677bae6a0ae7900bd3217be041c6614d5dcd2cae780000f9cd69462b36541"`,
				After:  `order7NATSImage         = "nats:2.14.4@sha256:0000000000000000000000000000000000000000000000000000000000000000"`,
			},
			Patch{
				Path:   "go/internal/p8oracle/runner_nats.go",
				Before: `order7NATSImageDigest   = "nats@sha256:ecf677bae6a0ae7900bd3217be041c6614d5dcd2cae780000f9cd69462b36541"`,
				After:  `order7NATSImageDigest   = "nats@sha256:0000000000000000000000000000000000000000000000000000000000000000"`,
			},
		),
		mutation(
			"LIVE_NATS_HARNESS_LOWERS_BROKER_MAX_PAYLOAD",
			"start the broker with a payload ceiling below the reviewed two-MiB boundary",
			"go/internal/p8oracle/runner_nats.go",
			`order7NATSConfig        = "port: 4222\nmax_payload: 2097152\n"`,
			`order7NATSConfig        = "port: 4222\nmax_payload: 1048576\n"`,
			"./internal/p8oracle", "TestOrder7NATSLiveAuthorityIsExact",
		),
		mutation(
			"LIVE_NATS_HARNESS_REMOVES_CONTAINER_OWNERSHIP",
			"allow cleanup to remove a container owned by another process",
			"go/internal/p8oracle/runner_nats.go",
			`return expected != "" && strings.TrimSpace(actual) == expected`,
			`return true`,
			"./internal/p8oracle", "TestOrder7NATSContainerOwnershipIsExact",
		),
		mutation(
			"LIVE_NATS_HARNESS_ALLOWS_OPTIONAL_POSTGRESQL",
			"start mandatory live NATS evidence while PostgreSQL profiles may still skip",
			"go/internal/p8oracle/runner_nats.go",
			`if required == "1" && postgresRequired != "1" {`,
			`if false && required == "1" && postgresRequired != "1" {`,
			"./internal/p8oracle", "TestOrder7RequiredNATSRequiresMandatoryPostgreSQL",
		),
		mutation(
			"LIVE_NATS_HARNESS_INCLUDES_SQLITE_PROFILE",
			"run the cross-process NATS oracle against the process-local SQLite profile",
			"go/internal/p8oracle/runner_nats.go",
			`if profile.provider == "postgresql" {`,
			`if true || profile.provider == "postgresql" {`,
			"./internal/p8oracle", "TestOrder7NATSLiveAuthorityIsExact",
		),
		mutation(
			"LIVE_NATS_ORACLE_OMITS_BOUNDARY_PLUS_ONE",
			"test the accepted payload boundary twice instead of proving boundary-plus-one refusal",
			"go/internal/p8oracle/natslive/testdata/oracle_test.go",
			`MaxInboundPayloadBytes: livePayloadLimit + 1`,
			`MaxInboundPayloadBytes: livePayloadLimit`,
			"./internal/p8oracle/natslive", "TestOrder7LiveNATSOracleSourceAuthority",
		),
		mutation(
			"LIVE_NATS_ORACLE_IGNORES_OUTAGE_UNAVAILABILITY",
			"expect the external transport to remain ready after every active connection is cut",
			"go/internal/p8oracle/natslive/testdata/oracle_test.go",
			`fixture.awaitAvailability(false)`,
			`fixture.awaitAvailability(true)`,
			"./internal/p8oracle/natslive", "TestOrder7LiveNATSOracleSourceAuthority",
		),
		mutation(
			"LIVE_NATS_ORACLE_IGNORES_DUPLICATE_BYTES",
			"allow a duplicate delivery to change its authenticated bytes while retaining object identity",
			"go/internal/p8oracle/natslive/testdata/oracle_test.go",
			`if !bytes.Equal(encoded, duplicateRaw.Data) || duplicateEvent.ID() != firstEvent.ID() || duplicateEvent.Metadata().EventID() != firstEvent.Metadata().EventID() {`,
			`if false || duplicateEvent.ID() != firstEvent.ID() || duplicateEvent.Metadata().EventID() != firstEvent.Metadata().EventID() {`,
			"./internal/p8oracle/natslive", "TestOrder7LiveNATSOracleSourceAuthority",
		),
		multiPatch(
			"LIVE_NATS_ORACLE_ROUTES_BY_GENERATION",
			"route by the authenticated generation digest in addition to logical event schema and model",
			"./internal/p8oracle/natslive", "TestOrder7LiveNATSOracleSourceAuthority",
			Patch{
				Path:   "go/internal/p8oracle/natslive/testdata/oracle_test.go",
				Before: `subject := fmt.Sprintf("%s.g1.%x.%x", fixture.prefix, metadata.EventSchemaDigest(), metadata.ModelID())`,
				After:  `subject := fmt.Sprintf("%s.g1.%x.%x.%x", fixture.prefix, metadata.EventSchemaDigest(), metadata.ModelID(), social.GolemGeneratedEventModels().GenerationDigest())`,
			},
			Patch{
				Path:   "go/internal/p8oracle/natslive/testdata/oracle_test.go",
				Before: `if subject != fixture.prefix+".g1."+fmt.Sprintf("%x", metadata.EventSchemaDigest())+"."+fmt.Sprintf("%x", metadata.ModelID()) || strings.Contains(subject, generationText) {`,
				After:  `if subject != fixture.prefix+".g1."+fmt.Sprintf("%x", metadata.EventSchemaDigest())+"."+fmt.Sprintf("%x", metadata.ModelID())+"."+generationText || !strings.Contains(subject, generationText) {`,
			},
		),
		multiPatch(
			"LIVE_NATS_ORACLE_ACCEPTS_CORE_REPLAY",
			"accept historical delivery to both raw and generated late subscribers",
			"./internal/p8oracle/natslive", "TestOrder7LiveNATSOracleSourceAuthority",
			Patch{
				Path:   "go/internal/p8oracle/natslive/testdata/oracle_test.go",
				Before: `if _, err := lateRaw.NextMsg(250 * time.Millisecond); !errors.Is(err, natsclient.ErrTimeout) {`,
				After:  `if _, err := lateRaw.NextMsg(250 * time.Millisecond); false && !errors.Is(err, natsclient.ErrTimeout) {`,
			},
			Patch{
				Path:   "go/internal/p8oracle/natslive/testdata/oracle_test.go",
				Before: `} else if eventCode(err) != events.CodeSubscriptionCancelled {`,
				After:  `} else if false && eventCode(err) != events.CodeSubscriptionCancelled {`,
			},
		),
	}
}
