package p8mutation

import "time"

// liveNATSWorkflowMutations remains isolated from the global catalog until the
// Order-7 hosted Core NATS workflow boundary has completed independent review.
func liveNATSWorkflowMutations() []Mutation {
	gate := Gate{Directory: "go", Package: "./internal/workflowaudit", Test: "TestP8WorkflowContainsRequiredHostedGates"}
	workflowGate := Gate{Directory: "go", Package: "./internal/workflowaudit", Test: "TestP8LiveNATSWorkflowBoundaryMutationsAreRejected"}
	mutation := func(label, summary, path, before, after string, selected Gate) Mutation {
		return Mutation{
			Label: label, Summary: summary,
			Patches: []Patch{{Path: path, Before: before, After: after}},
			Gate:    selected, Timeout: 2 * time.Minute,
		}
	}
	return []Mutation{
		mutation(
			"LIVE_NATS_WORKFLOW_OPTIONAL_MODE",
			"allow hosted live NATS evidence to skip when its broker prerequisite is absent",
			".github/workflows/p8-release-candidate.yml",
			`  GOLEM_P8_REQUIRE_NATS: "1"`, `  GOLEM_P8_REQUIRE_NATS: "0"`, gate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_TRUSTS_OTHER_IMAGE",
			"accept an unreviewed Core NATS image reference in hosted evidence",
			"go/internal/workflowaudit/audit.go",
			`const liveNATSImage = "nats:2.14.4@sha256:ecf677bae6a0ae7900bd3217be041c6614d5dcd2cae780000f9cd69462b36541"`,
			`const liveNATSImage = "nats:2.14.4@sha256:0000000000000000000000000000000000000000000000000000000000000000"`, gate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_TRUSTS_OTHER_REPO_DIGEST",
			"accept a pulled image whose repository digest differs from the reviewed authority",
			"go/internal/workflowaudit/audit.go",
			`const liveNATSRepoDigest = "nats@sha256:ecf677bae6a0ae7900bd3217be041c6614d5dcd2cae780000f9cd69462b36541"`,
			`const liveNATSRepoDigest = "nats@sha256:0000000000000000000000000000000000000000000000000000000000000000"`, gate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_OMITS_FIXED_NAME_PREFLIGHT",
			"accept a hosted materialization step without refusing a pre-existing fixed-name container",
			"go/internal/workflowaudit/audit.go",
			`const liveNATSAbsenceCheck = "if docker container inspect golem-p8-order7-nats >/dev/null 2>&1; then\n  exit 1\nfi"`,
			`const liveNATSAbsenceCheck = "docker container inspect other-container >/dev/null 2>&1"`, gate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_CLEANUP_IGNORES_OWNER",
			"accept crash cleanup without proving the harness owner label is nonempty",
			"go/internal/workflowaudit/audit.go",
			"owner := strings.Index(command, `test -n \"${owner}\"`)",
			`owner := inspect + 1`, workflowGate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_CLEANUP_IGNORES_IMAGE",
			"accept crash cleanup without proving the fixed-name container uses the pinned image",
			"go/internal/workflowaudit/audit.go",
			"image := strings.Index(command, `test \"${image}\" = \"`+liveNATSImage+`\"`)",
			`image := owner + 1`, workflowGate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_CLEANUP_IGNORES_ORDER",
			"accept crash cleanup that removes the container before completing owner and image checks",
			"go/internal/workflowaudit/audit.go",
			`inspect >= 0 && owner > inspect && image > owner && remove > image`,
			`inspect >= 0 && owner >= 0 && image >= 0 && remove >= 0`, workflowGate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_OMITS_OUTAGE_C_PROFILE",
			"stop requiring the outage/reconnect oracle on the PostgreSQL C profile",
			"go/internal/workflowaudit/audit.go",
			`const externalNATSOutageCWorkflowIdentity = "github.com/eleven-am/golem/go/internal/p8oracle/natslive:TestOrder7ExternalGeneratedNATSOutageReconnectAndReadiness/postgresql-c"`,
			`const externalNATSOutageCWorkflowIdentity = "github.com/eleven-am/golem/go/internal/p8oracle/natslive:TestOrder7ExternalGeneratedNATSOutageReconnectAndReadiness/renamed-c"`, gate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_OMITS_OUTAGE_LINGUISTIC_PROFILE",
			"stop requiring the outage/reconnect oracle on the PostgreSQL linguistic profile",
			"go/internal/workflowaudit/audit.go",
			`const externalNATSOutageLinguisticWorkflowIdentity = "github.com/eleven-am/golem/go/internal/p8oracle/natslive:TestOrder7ExternalGeneratedNATSOutageReconnectAndReadiness/postgresql-linguistic"`,
			`const externalNATSOutageLinguisticWorkflowIdentity = "github.com/eleven-am/golem/go/internal/p8oracle/natslive:TestOrder7ExternalGeneratedNATSOutageReconnectAndReadiness/renamed-linguistic"`, gate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_OMITS_DUPLICATE_C_PROFILE",
			"stop requiring duplicate identity and no-replay evidence on the PostgreSQL C profile",
			"go/internal/workflowaudit/audit.go",
			`const externalNATSDuplicateCWorkflowIdentity = "github.com/eleven-am/golem/go/internal/p8oracle/natslive:TestOrder7ExternalGeneratedNATSDuplicateIdentityAndCoreNoReplay/postgresql-c"`,
			`const externalNATSDuplicateCWorkflowIdentity = "github.com/eleven-am/golem/go/internal/p8oracle/natslive:TestOrder7ExternalGeneratedNATSDuplicateIdentityAndCoreNoReplay/renamed-c"`, gate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_OMITS_DUPLICATE_LINGUISTIC_PROFILE",
			"stop requiring duplicate identity and no-replay evidence on the PostgreSQL linguistic profile",
			"go/internal/workflowaudit/audit.go",
			`const externalNATSDuplicateLinguisticWorkflowIdentity = "github.com/eleven-am/golem/go/internal/p8oracle/natslive:TestOrder7ExternalGeneratedNATSDuplicateIdentityAndCoreNoReplay/postgresql-linguistic"`,
			`const externalNATSDuplicateLinguisticWorkflowIdentity = "github.com/eleven-am/golem/go/internal/p8oracle/natslive:TestOrder7ExternalGeneratedNATSDuplicateIdentityAndCoreNoReplay/renamed-linguistic"`, gate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_TOOLCHAIN_OMITS_EXECUTION",
			"accept a toolchain job whose evidence command no longer executes the live NATS package",
			"go/internal/workflowaudit/audit.go",
			`"toolchain-suite": {"Complete P0-P8 suite", "Retain structured toolchain evidence", "./..."}`,
			`"toolchain-suite": {"Complete P0-P8 suite", "Retain structured toolchain evidence", "go test"}`, workflowGate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_PROVIDER_OMITS_EXECUTION",
			"accept a provider job whose evidence command no longer executes the live NATS package",
			"go/internal/workflowaudit/audit.go",
			`"provider-matrix": {"SQLite and PostgreSQL C plus linguistic provider matrix", "Retain structured provider evidence", "./internal/p8oracle/..."}`,
			`"provider-matrix": {"SQLite and PostgreSQL C plus linguistic provider matrix", "Retain structured provider evidence", "go test"}`, workflowGate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_HARDENING_OMITS_EXECUTION",
			"accept a hardening job whose evidence command no longer executes the live NATS package",
			"go/internal/workflowaudit/audit.go",
			`"hardening":       {"Race and resource-leak matrix", "Retain structured hardening evidence", "./..."}`,
			`"hardening":       {"Race and resource-leak matrix", "Retain structured hardening evidence", "go test"}`, workflowGate,
		),
		mutation(
			"LIVE_NATS_WORKFLOW_ALLOWS_JOB_OVERRIDE",
			"allow an executing hosted job to shadow mandatory live NATS prerequisites",
			"go/internal/workflowaudit/audit.go",
			`if jobDefinesEnvironment(job, required.key) {
					violations = append(violations, Violation{Code: "P8_WORKFLOW_LIVE_NATS_ENV_OVERRIDE"})`,
			`if false && jobDefinesEnvironment(job, required.key) {
					violations = append(violations, Violation{Code: "P8_WORKFLOW_LIVE_NATS_ENV_OVERRIDE"})`, workflowGate,
		),
	}
}
