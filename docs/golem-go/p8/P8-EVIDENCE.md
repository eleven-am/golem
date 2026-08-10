# P8 mandatory evidence ledger

Status: **implementation in progress; every formal release gate remains
pending until its named mutation and completion audit is recorded**

Authority: [`P8-PLAN.md`](./P8-PLAN.md),
[`PUBLIC-PRODUCTION-ABI.md`](./PUBLIC-PRODUCTION-ABI.md), and
[`../BIBLE.md`](../BIBLE.md). A row becomes `PASS` only when its named evidence
exists, passes on every required profile, and kills the named mutations that
depend on it. An interface, example snippet, local unpublished module, unit-only
provider fake, documentation assertion, or skipped hosted job is not evidence.

## 1. Completion gates

| # | Mandatory claim | Required named evidence | Profiles | State |
| ---: | --- | --- | --- | --- |
| 1 | Public provider packages expose only sealed verified database handles; `UnsafeSQLX` is the sole unmistakably unsafe raw-pool escape; and a clean external consumer imports no Golem internal package | `TestP8PublicProviderAPICompilesFromCleanExternalModule`; `TestP8PublicPackageInventoryHasNoInternalTypeLeak`; `TestP8DatabaseHandleCannotBeForgedOrProviderMismatched`; `TestP8UnsafeSQLXIsOnlyRawPoolEscape` | compile + external module | **PENDING** |
| 2 | SQLite opening owns DSN pragmas, driver, lock mode, pool width, minimum version, functions, probes, cleanup, and closed capability reporting on every connection | `TestP8SQLitePublicOpenAppliesInvariantToEveryPooledConnection`; `TestP8SQLiteRefusesPrivateMemoryAndProviderPragmaOverride`; `TestP8SQLiteOpenFailureClosesAllResourcesAndRedactsDSN` | SQLite file + named shared memory + concurrency | **PENDING** |
| 3 | PostgreSQL opening owns session settings, bounded pool configuration, version/capability proof, cleanup, and redacted failures on every pooled connection | `TestP8PostgreSQLPublicOpenConfiguresEveryPooledConnection`; `TestP8PostgreSQLPoolDefaultsAndHardLimits`; `TestP8PostgreSQLOpenFailureClosesAllResourcesAndRedactsDSN` | PostgreSQL 15+ `C` + linguistic + concurrency | **PENDING** |
| 4 | Generated applications accept the verified handle, reprove live state, require the exact generation/provider-bound reviewed migration ledger, apply no migration, start no worker, borrow rather than close the database, and refuse closed/stale/mismatched handles | `TestP8GeneratedAppOpenUsesVerifiedDatabaseHandle`; `TestP8AppOpenIsReadOnlyAndStartsNoBackgroundWork`; `TestP8AppOpenRefusesClosedStaleCapabilityAndSchemaMismatch`; `TestP8RuntimeRequiresExactReviewedMigrationLedgerSQLite`; `TestP8RuntimeRequiresExactReviewedMigrationLedgerPostgreSQL`; `TestP8ApplicationNeverClosesBorrowedDatabase` | both providers + external module | **PENDING** |
| 5 | `version` and read-only `doctor` have deterministic versioned human/JSON output, use the public provider path, classify all required states, mutate no managed data, and disclose no secrets | `TestP8VersionHumanAndJSONGolden`; `TestP8DoctorStateMatrixBothProviders`; `TestP8DoctorIsReadOnlyAndUsesPublicProviderLifecycle`; `TestP8DoctorOutputRedactionCanary` | CLI + both providers + subprocess | **PENDING** |
| 6 | A clean external-style User/Session/Post/Comment/Tag/PostTag application generates and runs all accepted P1–P7 surfaces without handwritten ordinary backend code or a replace directive | `TestP8ExternalSocialApplicationGenerateCheckBuildAndRun`; `TestP8ExternalSocialApplicationSQLiteJourney`; `TestP8ExternalSocialApplicationPostgreSQLJourney`; `TestP8ExampleContainsNoInternalImportOrOrdinaryResolverClone` | clean module + both providers + HTTP/WS | **PENDING** |
| 7 | Quickstart, migration, deployment, hooks, custom operations, GraphQL, analytics, events, observability, operations, compatibility, and upgrade commands execute exactly as documented | `TestP8DocumentationCommandCorpus`; `TestP8QuickstartFromEmptyDirectory`; `TestP8DeploymentAndRecoveryRunbookDrills`; `TestP8EveryPublicSnippetTypeChecks` | docs + external module + both providers | **PENDING** |
| 8 | Caller, CallerTx, generated GraphQL query roots, and custom query roots have equivalent authorized read results, masks, stable errors, classification, statement behavior, and loader isolation | `TestP8ReadCrossEntryPointIndependentOracle`; `TestP8ReadMaskErrorAndPaginationParity`; `TestP8CustomQueryCannotChangeAuthorizationOrSystemCapability`; `TestP8CallerTransactionReadParity` | both providers + GraphQL + independent oracle | **PENDING** |
| 9 | Caller, CallerTx, generated GraphQL mutation roots, and custom transaction mutations commit or roll back equivalent rows, hooks, changed fields, nested nodes, facts, invalidations, and errors | `TestP8MutationCrossEntryPointIndependentOracle`; `TestP8NestedBatchAndUpsertParity`; `TestP8CustomMutationTransactionParity`; `TestP8MutationDenialAndProviderFailureRollbackParity` | both providers + GraphQL + concurrency + oracle | **PENDING** |
| 10 | Model hooks, computed/batched fields, custom roots, and after-commit behavior use the shared caller/runtime and preserve documented retry, masking, transaction, and failure semantics | `TestP8HookPhaseAndResultCrossSurfaceOracle`; `TestP8ComputedAndBatchedDependencyDisclosureOracle`; `TestP8AfterCommitFailureDoesNotChangeCommittedResult`; `TestP8CustomAndComputedResolverCapabilityInventory` | both providers + GraphQL + race | **PENDING** |
| 11 | Caller and GraphQL analytics plus scoped reads preserve authorized scope, exact values, refusal categories, limits, statements, and audits | `TestP8AnalyticsCrossEntryPointIndependentOracle`; `TestP8ScopedReadAuthorizationAndAuditRedTeam`; `TestP8AnalyticsExactScalarAndLimitParity`; `TestP8UnsupportedRelationAggregationRefusesEveryEntryPoint` | both providers + GraphQL + oracle | **PENDING** |
| 12 | Caller and GraphQL event streams expose equivalent fresh authorization, filters, projections, identities, suppression, overflow, and errors, while configured CDC enters the same path | `TestP8EventCrossEntryPointIndependentOracle`; `TestP8EventFreshAuthorizationAndSuppressionParity`; `TestP8EventOverflowCancellationAndIdentityParity`; `TestP8CDCAdapterUsesReleasedRuntimePath` | both providers + HTTP/WS + transport/CDC harness | **PENDING** |
| 13 | Missing/invisible data, conditional fields, relations, aggregates, hooks, custom roots, computed dependencies, and event snapshots leak no protected canary through any public result or behavior | `TestP8DisclosureCanaryCorpusCallerGraphQLEvents`; `TestP8MissingInvisibleAndMaskedIndistinguishabilityOracle`; `TestP8HookComputedCustomAndAnalyticsDisclosureCorpus`; `FuzzP8PublicInputNeverDisclosesProtectedCanary` | both providers + GraphQL + fuzz + timing buckets | **PENDING** |
| 14 | Errors, doctor/version output, observations, slog/OTel attributes, health endpoints, operator records, and release evidence contain no credential, DSN, SQL/bind, principal, row, private dependency, snapshot, or raw provider error | `TestP8DiagnosticAndTelemetryRedactionCanaryCorpus`; `TestP8HealthEndpointSafeShape`; `TestP8RawProviderErrorNeverReachesPublicOrObservation`; `FuzzP8DiagnosticEncodingIsClosedAndBounded` | both providers + CLI + HTTP + observability + fuzz | **PENDING** |
| 15 | Foreign/forged/stale generated values, database handles, compatibility manifests, codecs, migrations, and release metadata fail before data/worker work with stable closed diagnostics | `TestP8ForgedCapabilityAndGeneratedIdentityRejection`; `TestP8StaleArtifactAndCompatibilityManifestRejection`; `TestP8ReviewedMigrationPreflightRejectsMissingEmptyAndForeignBeforeDatabaseWork`; `TestP8UnsupportedPersistedVersionNeverReinterpreted`; `TestP8RejectionTouchesNoDatabaseOrWorkerWhenPreflightCanDecide` | portable + both providers + mutation | **PENDING** |
| 16 | Connections, SQL statements, transactions, loaders, goroutines, queues, evaluations, publisher attempts, CDC workers, and retained heap obey configured/plan-derived bounds without superlinear growth | `TestP8StatementAndConnectionBudgetMatrix`; `TestP8GoroutineQueueAndEvaluationHardBounds`; `TestP8CardinalityRampNoSuperlinearResourceGrowth`; `BenchmarkP8ReferenceApplicationProfiles` | both providers + load + race + leak + benchmark | **PENDING** |
| 17 | Cancellation, slow clients, pool starvation, lock/conflict contention, hook/computed panic, transport outage, duplicate window, crash/restart, migration interruption, and shutdown recover without leak, partial commit, lost fact, or false success | `TestP8CancellationAndSlowClientRecoveryMatrix`; `TestP8ProviderContentionAndPoolStarvationRecovery`; `TestP8HookComputedAndObserverFailureIsolation`; `TestP8PublisherCDCAndMigrationCrashRecovery`; `TestP8GracefulAndForcedShutdownSubprocessMatrix` | both providers + HTTP/WS + subprocess + race | **PENDING** |
| 18 | Closed observations cover every required runtime family; slog and OTel adapters are bounded, stable, panic-safe, non-authoritative, and semantically equivalent | `TestP8ObservationCoverageManifest`; `TestP8SlogAndOpenTelemetryAdapterAgreement`; `TestP8ObserverPanicBlockAndOutageCannotAlterCorrectness`; `TestP8ObservationCardinalityAndBoundedDispatcher` | portable + both providers + OTel test exporter + race | **PENDING** |
| 19 | Frozen source/generated/schema/migration/data/event corpora upgrade through public tools on both providers without data, authorization, history, identity, or pending-event loss | `TestP8FrozenCompatibilityCorpusLoads`; `TestP8P7ToReleaseUpgradeSQLite`; `TestP8P7ToReleaseUpgradePostgreSQLProfiles`; `TestP8UpgradePreservesAuthorizationMigrationChainAndPendingEvents` | SQLite + PostgreSQL `C`/linguistic + restart | **PENDING** |
| 20 | Semantic version, public API, generated ABI, GraphQL, persisted formats, CLI JSON, and compatibility manifest follow the patch/minor/major contract and machine-detect incompatibility | `TestP8CompatibilityManifestCanonicalAndComplete`; `TestP8PublicGoAPIDiffGate`; `TestP8GeneratedAndGraphQLCompatibilityGate`; `TestP8CLIJSONAndPersistedFormatCompatibilityGate` | release fixtures + compile + schema diff | **PENDING** |
| 21 | Hosted CI runs every required toolchain/provider/security/race/repeat/shuffle/fuzz/mutation/crash/docs/example profile with no hidden skip and retains structured evidence | `TestP8WorkflowContainsRequiredHostedGates`; hosted `p8-release-candidate` workflow; structured test-event audit; required-profile skip detector | hosted Linux + declared compile platforms + supported provider matrix | **PENDING** |
| 22 | A protected `go/vX.Y.Z` candidate resolves through a clean consumer, installs the CLI, reproduces byte-identical archives/checksums/SBOM/provenance, and cannot be republished differently | `TestP8ReleaseTagAndVersionAgreement`; `TestP8CleanConsumerModuleResolutionAndGoInstall`; `TestP8ReleaseArtifactReproducibility`; `TestP8ExistingVersionArtifactReplacementRefused` | release candidate + clean network consumer + artifact audit | **PENDING** |
| 23 | Controlling docs agree on P0–P8 status, supported surfaces, deployment profiles, and intentional boundaries; federation, MySQL, automatic migration, raw SQL, CDC, and transport non-claims are visible | `TestP8DocumentationStatusAndLinkAudit`; `TestP8IntentionalBoundaryDisclosureCorpus`; `TestP8NoCompletedABIStillClaimsUnimplemented`; `TestP8READMEAndReleaseNotesCapabilityAgreement` | docs + generated capability manifest | **PENDING** |
| 24 | The complete release passes an independent external application, provider, conformance, disclosure, resource, recovery, compatibility, package hygiene, docs, and artifact audit without production expectation helpers | `TestP8IndependentReleaseOracleSQLite`; `TestP8IndependentReleaseOraclePostgreSQLProfiles`; `TestP8IndependentPublicPackageAndArtifactAudit`; all mutations below; all commands in section 3 | all + hosted release candidate | **PENDING** |

## 2. Named-mutation matrix

Every mutation must make at least one named test fail. Review detection alone is
not evidence.

| Mutation | Required failing evidence | State |
| --- | --- | --- |
| `PUBLIC_PROVIDER_RETURNS_UNVERIFIED_DB` — publish a handle before complete connection/capability proof | rows 1–4 | **PENDING** |
| `SECOND_PROVIDER_ENUM_WINS` — let application config contradict the verified handle | rows 1, 4 | **PENDING** |
| `ADOPT_ARBITRARY_SQLX_POOL` — accept a pool whose future connection invariants cannot be proved | rows 1–4 | **PENDING** |
| `SAFE_NAMED_RAW_SQLX_ESCAPE` — expose the raw pool through a name that does not declare bypass semantics | rows 1, 7, 23 | **PENDING** |
| `SQLITE_SKIP_CONNECTION_PRAGMAS` — configure only the first pooled connection | row 2 | **PENDING** |
| `SQLITE_DEFERRED_DEFAULT` — omit the provider-owned immediate write mode | rows 2, 17 | **PENDING** |
| `POSTGRES_FIRST_CONNECTION_ONLY` — apply required session settings to one connection | row 3 | **PENDING** |
| `POSTGRES_UNBOUNDED_POOL_DEFAULT` — allow unlimited open connections | rows 3, 16–17 | **PENDING** |
| `LEAK_DSN_IN_ERROR` — include a supplied connection string or raw provider error | rows 2–5, 14 | **PENDING** |
| `APP_OPEN_APPLIES_MIGRATION` — mutate schema during runtime startup | rows 4, 7, 17 | **PENDING** |
| `APP_OPEN_STARTS_WORKER` — hide publisher/CDC work in constructor | rows 4, 7, 17 | **PENDING** |
| `APP_CLOSES_BORROWED_DATABASE` — generated runtime takes ownership it was not given | rows 4, 17 | **PENDING** |
| `DOCTOR_REPAIRS_STATE` — make diagnostic command apply/resume/acknowledge anything | rows 5, 14 | **PENDING** |
| `DOCTOR_EMITS_SOURCE_OR_SCHEMA_NAME` — expose uncontrolled path/name data in machine output | rows 5, 14 | **PENDING** |
| `EXAMPLE_USES_LOCAL_REPLACE` — pass only against the repository checkout | rows 6–7, 22, 24 | **PENDING** |
| `EXAMPLE_HANDWRITES_CRUD_RESOLVER` — conceal a missing generated capability in example code | rows 6–7, 24 | **PENDING** |
| `GRAPHQL_SECOND_READ_ENGINE` — calculate GraphQL reads outside the shared caller runtime | row 8 | **PENDING** |
| `GRAPHQL_SECOND_MUTATION_ENGINE` — commit GraphQL writes outside P4 | row 9 | **PENDING** |
| `CUSTOM_ROOT_RECEIVES_SYSTEM_OR_DB` — bypass caller policy from an extension resolver | rows 8–10, 13 | **PENDING** |
| `HOOK_RESULT_BEFORE_VERIFICATION` — expose an unverified persisted image | rows 9–10, 13 | **PENDING** |
| `AFTER_COMMIT_ERROR_REWRITES_SUCCESS` — return failure after the write committed | rows 9–10, 17 | **PENDING** |
| `COMPUTED_PRIVATE_DEPENDENCY_ESCAPES` — serialize privately hydrated data | rows 10, 13–14 | **PENDING** |
| `SCOPED_SQL_SKIPS_HOP_POLICY` — authorize root but not a joined relation | rows 11, 13 | **PENDING** |
| `ANALYTICS_PARTIAL_MASK` — return an aggregate built from conditionally unreadable values | rows 11, 13 | **PENDING** |
| `EVENT_SURFACES_DIVERGE` — authorize caller and GraphQL event streams differently | row 12 | **PENDING** |
| `TELEMETRY_INCLUDES_RAW_ERROR` — attach exception/provider text as an attribute | rows 13–14, 18 | **PENDING** |
| `TELEMETRY_INCLUDES_MODEL_OR_FIELD_NAME` — create uncontrolled/high-cardinality labels | rows 14, 18 | **PENDING** |
| `OBSERVER_PANIC_PROPAGATES` — let instrumentation affect operation correctness | rows 17–18 | **PENDING** |
| `OBSERVER_QUEUE_UNBOUNDED` — retain telemetry indefinitely during exporter outage | rows 16–18 | **PENDING** |
| `RELATION_LOAD_N_PLUS_ONE` — scale statement count linearly with returned parents where batching is required | row 16 | **PENDING** |
| `CANCEL_LEAKS_GOROUTINE_OR_CONNECTION` — retain work after ownership ends | rows 16–17 | **PENDING** |
| `SLOW_SUBSCRIBER_DROPS_AND_CONTINUES` — violate bounded disconnect semantics under release load | rows 12, 16–17 | **PENDING** |
| `UPGRADE_REWRITES_EVENT_ID` — change pending fact/event identity during format upgrade | row 19 | **PENDING** |
| `UPGRADE_ADVANCES_LEDGER_BEFORE_VERIFY` — claim upgrade success before final schema proof | rows 17, 19 | **PENDING** |
| `UNKNOWN_CODEC_BEST_EFFORT_DECODE` — reinterpret unsupported persisted bytes | rows 15, 19–20 | **PENDING** |
| `PATCH_BREAKS_GENERATED_ABI` — accept a patch release with regenerated source breakage | row 20 | **PENDING** |
| `PATCH_BREAKS_GRAPHQL_SCHEMA` — accept a patch release with public schema breakage | row 20 | **PENDING** |
| `REQUIRED_PROVIDER_JOB_SKIPS` — turn absent PostgreSQL/profile evidence into success | rows 21, 24 | **PENDING** |
| `RELEASE_FROM_MOVING_BRANCH` — build public artifacts without a protected tag target | rows 21–22 | **PENDING** |
| `RELEASE_TAG_MODULE_MISMATCH` — publish an unprefixed/wrong-version nested-module tag | row 22 | **PENDING** |
| `REPLACE_EXISTING_RELEASE_BYTES` — overwrite an existing version with different artifacts | row 22 | **PENDING** |
| `DOCUMENT_UNSUPPORTED_FEATURE` — claim federation, MySQL, implicit CDC, or turnkey multi-process transport | row 23 | **PENDING** |
| `DOC_SNIPPET_NOT_COMPILED` — let a published command/example drift silently | rows 7, 23–24 | **PENDING** |

## 3. Required completion commands

Exact package paths may grow during implementation, but the recorded release
candidate run must include at least:

```text
go test -count=1 ./provider/... ./observe/... ./cmd/golem ./runtime ./graphql ./events
go test -count=1 ./internal/p8oracle ./internal/p8verify ./examples/...
go test -p=1 -count=1 ./...
go test -p=1 -count=2 -timeout=45m ./...
go test -race -p=1 -count=1 -timeout=45m ./...
go test -shuffle=on -count=10 ./provider/... ./observe/... ./runtime ./graphql ./events

GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go test -race -count=1 -timeout=45m \
  ./provider/... ./observe/... ./runtime ./graphql ./events ./internal/p8oracle

go test -run='^$' -fuzz='^FuzzP8PublicInputNeverDisclosesProtectedCanary$' \
  -fuzztime=60s ./internal/p8oracle
go test -run='^$' -fuzz='^FuzzP8DiagnosticEncodingIsClosedAndBounded$' \
  -fuzztime=60s ./cmd/golem

GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go run ./internal/cmd/p8failure -module .
GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go run ./internal/cmd/p8mutation -module .
go run ./internal/cmd/p8docs -module .
go run ./internal/cmd/p8compat -module .
go run ./internal/cmd/p8release -mode verify --tag go/vX.Y.Z --module .

govulncheck ./...
go vet ./...
test -z "$(find . -type f -name '*.go' -print0 | xargs -0 gofmt -l)"
git diff --check
```

Hosted completion additionally requires:

- the declared Go toolchain compatibility matrix;
- SQLite on each declared compile/runtime platform;
- every supported PostgreSQL major with required `C` and linguistic profiles;
- structured `go test -json` proof that required packages passed with no skip;
- clean consumer resolution with no workspace or replace directive;
- release-candidate CLI installation through the public module path;
- two independent artifact builds with byte-identical archives, checksums,
  SBOM, provenance subject digests, compatibility manifest, and generated output;
  and
- a protected tag/dry-run release followed by immutable artifact inspection.

The failure harness uses real subprocess termination, connection starvation,
provider contention, network/transport interruption, and forced shutdown—not
only injected returned errors. Load tests record hardware, OS, Go version,
provider version, dataset, shape, concurrency, configured limits, statement
counts, pool bounds, goroutine/heap baselines, and benchmark samples.

## 4. Independent oracle rules

P8 independent evidence may invoke public Golem entry points but may not use the
production policy evaluator, implication engine, read/mutation/analytics planner,
GraphQL compiler, hook dispatcher, event authorizer/decoder, observation encoder,
compatibility comparer, or release-manifest builder to calculate expected
answers.

It uses:

- hand-enumerated social rows, grants, masks, mutations, hook traces, facts, and
  event outcomes;
- direct provider SQL from test-only code to inspect committed state, locks,
  ledger rows, and resource truth;
- independent GraphQL requests and response normalization;
- protected random canaries placed in rows, principals, errors, DSNs, SQL
  operands, private dependencies, and event snapshots;
- OS/process inspection for file descriptors, goroutines, subprocesses, and
  shutdown ownership;
- external-module builds with an empty module/cache boundary where practical;
- frozen artifact bytes and independently computed SHA-256 digests; and
- workflow/event audits that treat missing or skipped expected profiles as
  failure.

The complete application includes User, Session, Post, recursive Comment, Tag,
and PostTag; scalar and composite identities; conditional fields; every exact
logical scalar; root/nested/batch/system operations; read and mutation hooks;
custom query and transaction mutation; ordinary and batched computed fields;
analytics and scoped reads; durable facts; caller and GraphQL subscriptions;
revocation; and process restart.

## 5. Completion record

P8-A implementation completed on 2026-08-09. The current-source semantic suites
for rows 1–5 pass on SQLite, PostgreSQL `C`, and PostgreSQL linguistic profiles,
including public-package inventory, provider lifecycle and cleanup, every-slot
session proof, immediate SQLite locking, exact reviewed-ledger startup,
external generated application startup, read-only/no-worker behavior, CLI
state matrices, and disclosure canaries. Full `cmd/golem`, `runtime`, registry,
pipeline, provider, schema-bundle, generated-manifest, and migration regressions
also pass.

P8-B local implementation completed on 2026-08-09. The clean nested social
module passes fresh zero-artifact inspect/migration/generate/check/build/run
journeys on SQLite, PostgreSQL `C`, and PostgreSQL linguistic profiles. All four
row-7 named documentation gates pass locally. The recovery gate uses disposable
databases, generated System writes, exact managed-row/ledger/outbox snapshots,
SQLite checkpointed backup, PostgreSQL dump/restore, forced doctor-visible
drift, restored pending facts, and post-restore public publisher delivery. An
independent agent repeated the mandatory two-profile suite with no skips.

Rows 1–7 deliberately remain `PENDING`: required named-mutation records, hosted
completion, clean resolution from an immutable public tag without the local
pre-release workspace, and P8-I release-candidate audit have not been
fabricated. No release-candidate tag has been created and no Go module release
is implied by these local implementation milestones.
