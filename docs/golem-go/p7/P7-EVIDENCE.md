# P7 mandatory evidence ledger

Status: **complete; all implementation and verification gates pass**

Authority: [`P7-PLAN.md`](./P7-PLAN.md),
[`PUBLIC-EVENT-ABI.md`](./PUBLIC-EVENT-ABI.md), and
[`../BIBLE.md`](../BIBLE.md). A row becomes `PASS` only when its named evidence
exists, passes on every required profile, and kills the named mutations that
depend on it. A generated symbol, code review, interface implementation, mock
transport, or skipped live-provider case is not completion evidence.

## 1. Completion gates

| # | Mandatory claim | Required named evidence | Profiles | State |
| ---: | --- | --- | --- | --- |
| 1 | Subscription authoring, event roots, identity metadata, complete stored-scalar snapshot inventory, limits, and event-schema fingerprints normalize deterministically into closed ContractIR | `TestP7ContractNormalizesSubscriptionsRootsIdentitiesSnapshotsAndLimits`; `TestP7ContractRejectsHiddenKeylessCollidingAndUncapturableModels`; `TestP7EventSchemaDigestIgnoresGraphQLOnlyChangesButTracksLogicalEventShape` | compile + portable unit | **PASS** |
| 2 | Generated typed event values, caller streams, registries, SDL, gqlgen artifacts, and manifests are deterministic and compile from a clean external module | `TestP7GeneratedArtifactsByteIdenticalAcrossShuffleAndRepeat`; `TestFreshP7SocialModuleGeneratesCompilesAndConstructsApp`; `TestP7GenerationPublishesAtomicallyAndRemovesOwnedStaleArtifacts` | compile + repeat | **PASS** |
| 3 | Subscription enablement/names affect only ContractIR/generated artifacts; the additive P7 platform object is the only physical change | `TestP7SubscriptionToggleChangesContractNotModelOrApplicationDDL`; `TestP7PlatformUpgradeAddsOnlyDeliverySystemObject`; `TestP7ContractRenameNeverProducesApplicationMigration` | both providers + fingerprint | **PASS** |
| 4 | `_golem_outbox_delivery` renders, migrates, introspects, fingerprints, and drift-checks exactly on SQLite and PostgreSQL while preserving immutable Outbox V1 | `TestP7DeliverySystemObjectMigratesIntrospectsAndFingerprintsBothProviders`; `TestP7P6ToP7UpgradePreservesExistingOutboxFacts`; `TestP7SystemDriftRejectsDeliveryShapeMutation` | SQLite + PostgreSQL C/linguistic live | **PASS** |
| 5 | New causal delivery rows commit atomically with data/facts and missing legacy rows backfill/materialize idempotently | `TestP7DataFactsAndCausalDeliveryCommitOrRollbackTogether`; `TestP7LegacyFactBackfillCrashRestartIsIdempotent`; `TestP7AbsentDeliveryRowRemainsDiscoverableAndClaimable` | SQLite + PostgreSQL live + crash | **PASS** |
| 6 | V1 historical decoding and V2 event-schema decoding preserve every supported exact type, identity, snapshot, and duplicated-column invariant | `TestP7FactV1HistoricalAndV2EventSchemaCodecMatrix`; `TestP7ScalarCompositeIdentityAndDeleteSnapshotRoundTrip`; `TestP7StoredFactCrossChecksEveryDuplicatedColumn`; `FuzzP7EventCodecRejectsMalformedAndOversizedInput` | portable + both providers + fuzz | **PASS** |
| 7 | Contract-only deployment changes cannot strand pending facts; missing or incompatible historical schemas block visibly and resume after repair | `TestP7PendingV2FactSurvivesGraphQLOnlyRegeneration`; `TestP7PendingV1FactUsesHistoricalBundleAfterRestart`; `TestP7MissingHistoricalSchemaBlocksWithoutAckAndResumes` | both providers + restart | **PASS** |
| 8 | Workers claim complete causation groups exclusively, verify ordinals `1..N`, and never split one outer transaction | `TestP7WholeCausationClaimAndOrdinalValidation`; `TestP7MaximumP4CausalGroupIsOneClaimDespiteClaimRowLimit`; `TestP7ConcurrentWorkersNeverOwnSameLiveLease` | SQLite multi-process + PostgreSQL multi-worker live | **PASS** |
| 9 | Lease acquisition/renewal/ack/retry/block is token-fenced and uses database time; stale workers cannot mutate a re-owned lease | `TestP7LeaseUsesDatabaseClockUnderWorkerClockSkew`; `TestP7StaleLeaseTokenCannotAckRetryBlockOrRenew`; `TestP7SQLiteImmediateAndPostgresSkipLockedClaimAgreement` | SQLite + PostgreSQL live + concurrency | **PASS** |
| 10 | Every publisher crash window gives zero loss and only the documented duplicate window | `TestP7CrashBeforeClaimCommit`; `TestP7CrashAfterClaimBeforePublish`; `TestP7PartialTransportAcceptanceRetriesWholeBatch`; `TestP7CrashAfterTransportBeforeAckDuplicatesSameIDs`; `TestP7AckCommitPreventsRepublish` | both providers + subprocess restart | **PASS** |
| 11 | Transient errors retry indefinitely with bounded backoff; corrupt/unsupported facts block durably; operator resume/retire is explicit and audited | `TestP7TransientFailureNeverDropsAtArbitraryAttemptCount`; `TestP7CorruptFactBlocksAndRemainsInspectable`; `TestP7OperatorResumeAndRetireAreCausationSpecificAndAudited` | both providers + fake time/transport | **PASS** |
| 12 | Retention is disabled by default and can delete only delivered causal groups older than the floor, atomically and in bounded chunks | `TestP7RetentionDefaultDoesNothing`; `TestP7RetentionNeverDeletesPendingLeasedRetryingOrBlocked`; `TestP7RetentionDeletesDeliveredFactsAndStateAtomicallyAtBoundary` | both providers + crash | **PASS** |
| 13 | Transport preserves complete causal batches, exact event IDs/bytes, at-least-once duplicates, and clearly reports memory transport as process-local | `TestP7TransportCausalBatchConformance`; `TestP7RetryReusesExactIDsAndBytes`; `TestP7MemoryTransportIsBoundedAndCapabilityIsProcessLocal`; common transport adapter harness | portable + every installed transport | **PASS** |
| 14 | Generated caller stream and GraphQL subscribe-time binding refuse invalid/unauthorized requests before hub registration | `TestP7CallerAndGraphQLSubscribeAuthorizeReadBeforeRegistration`; `TestP7SubscriptionFilterSelectionClassificationBeforeSourceWork`; `TestP7InvalidForeignOrForgedEventOptionsTouchHubZeroTimes` | portable + both providers | **PASS** |
| 15 | Every delivered event re-resolves principal, snapshots actor, rebuilds policy, creates a new execution/loaders, and observes grant/revocation/change immediately | `TestP7FreshPrincipalActorPolicyAndExecutionPerEvent`; `TestP7RevocationSuppressesNextEventAndGrantPermitsNext`; `TestP7SecondAndFiveHundredthEventSeeFreshComputedValues`; `TestP7RevalidationFailureDisconnectsWithStableError` | both providers + race | **PASS** |
| 16 | Created/updated events re-read current authorized state with exact identity and filter; absent/filter/invisible cases suppress without disclosure | `TestP7CreatedUpdatedCurrentStateRereadOracle`; `TestP7FilterAndReadPolicyAreConjoinedBeforeProjection`; `TestP7MissingFilteredInvisibleSuppressionIsPubliclyIndistinguishable` | SQLite/PostgreSQL C/linguistic + oracle | **PASS** |
| 17 | Delete facts capture sufficient private policy dependencies across root/nested/batch/system paths; where-deletes and unverifiable deletes suppress; snapshot/entity never leak through ordinary typed Go events, GraphQL, errors, observers, or Notice metadata accessors. The configured transport remains the explicitly trusted data-processing boundary for opaque encoded bytes. | `TestP7DeleteSnapshotDependencyInventoryAndEveryMutationPath`; `TestP7DeleteAuthorizationFromPrivatePreImageOracle`; `TestP7DeleteWithWhereSuppressesAndEntityAlwaysNull`; `TestP7PrivateSnapshotAbsentFromGoGraphQLErrorsObserversAndNoticeMetadata` | both providers + GraphQL + red-team | **PASS** |
| 18 | Scalar/composite identities, nested declared order, and every batch-affected row reach caller and GraphQL streams | `TestP7ScalarAndCompositeIdentityProviderAgreement`; `TestP7NestedFactsDeliverInDeclaredOrdinalOrder`; `TestP7UpdateManyDeleteManyPublishEveryCommittedRow` | both providers + GraphQL | **PASS** |
| 19 | Fan-out sharing keys include security identity, policy version, canonical filter, selection, and event; no actor-specific result crosses principal/execution boundaries | `TestP7EquivalentSubscriberGroupingOracle`; `TestP7DifferentPrincipalsNeverShareEvaluation`; `TestP7DifferentSelectionFilterOrPolicyVersionNeverShareResult`; `TestP7AuditPrincipalCollisionDoesNotAuthorizeSharing` | portable + both providers + race | **PASS** |
| 20 | Subscriber/hub queues and evaluation concurrency are bounded; overflow disconnects rather than drops; cancellation/source close/shutdown leak nothing | `TestP7SubscriberQueueExactBoundaryAndOverflowDisconnect`; `TestP7EvaluationConcurrencyHardBound`; `TestP7CancelDuringEvaluationUnwinds`; `TestP7SourceCloseLastMemberAndApplicationShutdownNoLeak` | race + goleak + stress | **PASS** |
| 21 | Generated GraphQL event SDL and executable use active gqlgen serialization and the same evaluator as caller streams | `TestP7GeneratedGraphQLSubscriptionSDLGolden`; `TestP7GraphQLAndCallerEventEvaluationPolicySQLAndPayloadOracle`; `TestP7GQLGenSerializesAliasesFragmentsDirectivesScalarsRelationsAndComputedFields`; `TestP7NoSecondGraphQLEventEncoder` | both providers + GraphQL | **PASS** |
| 22 | Real `graphql-transport-ws` handles authentication, init, subscribe/next/error/complete, ping/pong, limits, duplicate IDs, close, and ordinary HTTP non-regression | `TestP7GraphQLTransportWSProtocolCorpus`; `TestP7WebSocketAuthInitAndOperationLimits`; `TestP7UnsupportedLegacyProtocolAndSSEAreNotSilentlyAccepted`; `TestP7HTTPQueriesMutationsRemainP5Equivalent` | network/in-process HTTP + race | **PASS** |
| 23 | Public/close errors and observers are sanitized; observer panic cannot alter delivery, acknowledgement, suppression, or lifecycle | `TestP7PublisherSubscriptionAndWebSocketErrorsAreSanitized`; `TestP7ObserverShapeContainsOnlyClosedSafeLabels`; `TestP7ObserverPanicDoesNotStopPublisherOrSubscriber` | portable + both providers | **PASS** |
| 24 | Without CDC, direct out-of-process writes are proved invisible and capabilities state this explicitly | `TestP7ExternalInsertUpdateDeleteInvisibleWithoutCDC`; `TestP7CapabilitiesReportExternalWritesObservedFalse`; `TestP7NoImplicitSQLiteOrPostgresCDC` | SQLite separate process + PostgreSQL separate connection | **PASS** |
| 25 | CDC SPI derives stable replay IDs and canonical bytes from stable source time, preserves checkpoint/ack order, validates exact pre-images, suppresses Golem-write duplication, and enters the shared authorization path | `TestP7CDCCommonAdapterConformance`; `TestP7CDCReplayDerivesStableEventIDs`; `TestP7ProductionCDCReplayKeepsExactEventIDAndCanonicalBytes`; `TestP7CDCCheckpointAdvancesOnlyAfterTransportAcceptance`; `TestP7CDCGolemTransactionCorrelationAvoidsSecondEvent`; `TestP7CDCUsesSameFreshSubscriptionAuthorization` | portable harness + every installed adapter live | **PASS** |
| 26 | Publisher/subscriber lifecycle is explicitly owned, cancellable, restartable on a new instance, and never replays application mutation closures/hooks | `TestP7PublisherRunOwnershipAndShutdownGrace`; `TestP7RestartNewWorkerDrainsOutstandingBacklog`; `TestP7PublicationRetryNeverRunsMutationHooksOrClosure` | both providers + subprocess + race | **PASS** |
| 27 | Ordering claims are exact: ordinal order within causation, possible duplicate prefix/batch, and no global transaction/commit order | `TestP7CausationOrderAndDuplicateOracle`; `TestP7ConcurrentCausationsMayInterleaveWithoutCorruption`; `TestP7RecordedAtIsNeverUsedAsCommitTimestampOrGlobalOrder` | both providers + concurrency | **PASS** |
| 28 | Complete social application passes independent provider, cross-entry-point, migration, process, race, repeat, shuffle, fuzz, vet, format, and mutation gates | `TestP7IndependentSocialEventOracleSQLite`; `TestP7IndependentSocialEventOraclePostgreSQLProfiles`; all mutations below; all commands in section 3 | all | **PASS** |

## 2. Named-mutation matrix

Every mutation must make at least one named test fail. Review detection is not
evidence.

| Mutation | Required failing evidence | State |
| --- | --- | --- |
| `MUTATE_FACT_ON_CLAIM` — write lease/attempt state into immutable outbox facts | rows 4, 8–9 | **PASS** |
| `ACK_BEFORE_TRANSPORT_SUCCESS` — mark delivered before transport acceptance | row 10 | **PASS** |
| `NEW_EVENT_ID_ON_RETRY` — generate new IDs/bytes for a retry | rows 10, 13 | **PASS** |
| `ACK_FOREIGN_OR_EXPIRED_LEASE` — acknowledge without the current token | row 9 | **PASS** |
| `LEASE_BY_WORKER_NAME` — use diagnostic worker identity instead of fencing token | row 9 | **PASS** |
| `LEASE_WITH_WORKER_CLOCK` — decide expiry from process time | row 9 | **PASS** |
| `SQLITE_DEFERRED_CLAIM` — use a deferred transaction that cannot own the write claim | row 9 | **PASS** |
| `POSTGRES_NO_SKIP_LOCKED` — permit workers to block/double-own the same candidates | rows 8–9 | **PASS** |
| `SPLIT_CAUSATION_CLAIM` — claim/publish one transaction through separate workers | rows 8, 10, 27 | **PASS** |
| `ORDER_BY_EVENT_ID` — ignore transaction ordinal within a causation | rows 8, 18, 27 | **PASS** |
| `CLAIM_GLOBAL_COMMIT_ORDER` — advertise recorded time as total commit order | row 27 | **PASS** |
| `TRUST_DUPLICATE_COLUMNS` — publish metadata despite outbox-column mismatch | row 6 | **PASS** |
| `CURRENT_GENERATION_ONLY` — reject pending facts after any deployment digest change | row 7 | **PASS** |
| `EVENT_SCHEMA_INCLUDES_GRAPHQL_NAME` — strand V2 facts on contract-only rename | rows 1, 7 | **PASS** |
| `ACCEPT_UNKNOWN_CODEC` — guess how to decode an unknown version | rows 6–7 | **PASS** |
| `DROP_AFTER_MAX_ATTEMPTS` — discard a transiently failing causal group | rows 10–11 | **PASS** |
| `SILENTLY_DROP_CORRUPT_FACT` — ack/delete malformed durable facts | rows 6, 11 | **PASS** |
| `DELETE_PENDING_ON_RETENTION` — age-delete a nondelivered group | row 12 | **PASS** |
| `PARTIAL_BATCH_ACK` — acknowledge a partially accepted causal batch | rows 10, 13 | **PASS** |
| `SKIP_DELETE_SNAPSHOT` — emit delete facts without compiled dependencies | row 17 | **PASS** |
| `EXPOSE_DELETE_SNAPSHOT` — serialize private pre-image through any public/observer path | rows 17, 23 | **PASS** |
| `FILTER_DELETED_SNAPSHOT_IN_GO` — invent an in-memory substitute for SQL filter semantics | row 17 | **PASS** |
| `REUSE_SUBSCRIBE_TIME_POLICY` — retain the initial policy for lifetime | row 15 | **PASS** |
| `REUSE_SUBSCRIBE_TIME_ACTOR` — retain initial resolved actor for lifetime | row 15 | **PASS** |
| `REUSE_EVENT_LOADERS` — let one event's row/computed cache answer the next | row 15 | **PASS** |
| `SUBSCRIBE_WITHOUT_INITIAL_READ_AUTH` — register before model authorization | row 14 | **PASS** |
| `AUTH_ONLY_WHEN_ENTITY_SELECTED` — deliver identity without fresh authorization | rows 15–17 | **PASS** |
| `GROUP_ACROSS_PRINCIPALS` — share actor-specific result across callers | row 19 | **PASS** |
| `GROUP_WITHOUT_SELECTION_IN_KEY` — reuse a differently shaped result | row 19 | **PASS** |
| `AUDIT_ID_IS_SECURITY_ID` — treat colliding audit strings as authorization equivalence | row 19 | **PASS** |
| `SHARE_HOOKED_OR_COMPUTED_EVALUATION` — reuse execution-dependent resolver work | rows 19–21 | **PASS** |
| `DROP_ON_QUEUE_OVERFLOW` — discard one event and keep the subscriber alive | row 20 | **PASS** |
| `UNBOUNDED_EVALUATION_GOROUTINES` — spawn without concurrency ownership | row 20 | **PASS** |
| `START_EVALUATION_AFTER_CANCEL` — begin DB work after cancellation is visible | row 20 | **PASS** |
| `GRAPHQL_SECOND_EVENT_ENGINE` — authorize/encode entity outside shared runtime/gqlgen | row 21 | **PASS** |
| `EMIT_DISABLED_SUBSCRIPTION` — emit a root because its reserved name exists | rows 1–2, 21 | **PASS** |
| `HAND_SERIALIZE_WS_ENTITY` — bypass active gqlgen scalar/field serialization | row 21 | **PASS** |
| `ACCEPT_LEGACY_WS_AS_NEW` — silently treat obsolete protocol as `graphql-transport-ws` | row 22 | **PASS** |
| `OBSERVER_PANIC_PROPAGATES` — let observer behavior change correctness | row 23 | **PASS** |
| `CLAIM_EXTERNAL_WRITES_VISIBLE_WITHOUT_CDC` — imply direct SQL observation | row 24 | **PASS** |
| `CDC_RANDOM_EVENT_ID` — allocate a new ID when replaying one source record | row 25 | **PASS** |
| `CDC_CHECKPOINT_BEFORE_ACCEPT` — advance durable cursor before transport acceptance | row 25 | **PASS** |
| `CDC_SECOND_CODEC_OR_AUTH_PATH` — bypass normal validation/fresh authorization | row 25 | **PASS** |
| `CDC_DUPLICATES_GOLEM_OUTBOX_WRITE` — emit both outbox and CDC copies for one Golem transaction | row 25 | **PASS** |
| `RUN_HOOK_ON_PUBLISH_RETRY` — replay mutation hook/application closure | row 26 | **PASS** |
| `SKIP_REQUIRED_POSTGRES_LIVE_GATE` — pass with a skipped required provider/profile | row 28 | **PASS** |

## 3. Required completion commands

Exact package paths may grow during implementation, but the final recorded run
must include at least:

```text
go test -count=1 ./internal/event/... ./internal/subscription/... ./internal/graphql/... ./events ./graphql ./runtime
go test -count=1 ./internal/compiler/... ./internal/generate/pipeline ./internal/codegen/... ./internal/migration/... ./internal/provider/...
go test -race -count=1 -timeout=30m ./internal/event/... ./internal/subscription/... ./internal/graphql/... ./events ./graphql ./runtime
go test -p=1 -count=1 ./...
go test -p=1 -count=2 -timeout=30m ./...
go test -shuffle=on -count=10 ./internal/event/... ./internal/subscription/... ./internal/graphql/... ./events ./graphql
GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go test -race -count=1 -timeout=30m ./internal/event/... ./internal/subscription/... ./internal/graphql/... ./events ./graphql ./runtime
go test -run='^$' -fuzz='^FuzzP7EventCodecRejectsMalformedAndOversizedInput$' -fuzztime=30s ./internal/event/codec
go test -run='^$' -fuzz='^FuzzP7FrozenEventRequestRejectionAndClone$' -fuzztime=30s ./golem
go test -run='^$' -fuzz='^FuzzP7GraphQLWSMessageAndSubscriptionInput$' -fuzztime=30s ./graphql
GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
go vet ./...
test -z "$(find . -type f -name '*.go' -print0 | xargs -0 gofmt -l)"
git diff --check
```

The crash harness must use subprocess termination at every named durability
boundary, not only injected returned errors. The multi-worker SQLite gate must
use a file database and independent connections/processes. PostgreSQL gates must
supply both the `C` and required linguistic profile; a skip is failure.

The final provider run must retain a structured `go test -json` stream and prove
that every expected package reached package-level `PASS`, with no failed or
skipped required test event. It must record durations, profile endpoints without
credentials, exact commit, worker/process counts, mutation kills, fuzz execution
counts, and leak/race results.

## 4. Independent oracle rules

The P7 oracle may not call the production fact/event decoder, delivery planner,
claim renderer, authorization evaluator, hub grouping key, GraphQL event
lowering, or serializer to calculate its expected answer.

It uses:

- hand-constructed canonical bytes and independent exact-value decoders;
- direct provider SQL in test-only packages for state/lease/retention truth;
- subprocess crash points and restart inspection of durable tables;
- explicit causation/ordinal/event-ID fixtures rather than sorting production
  output into the expected order;
- independently enumerated caller visibility/filter/delete outcomes;
- distinct principals with colliding audit strings and changing backing actor
  records;
- exact expected GraphQL frames and active gqlgen scalar results; and
- external direct SQL writers for the no-CDC boundary.

The social fixture contains User, Post, Comment, Friendship, Tag, and PostTag;
recursive comments; scalar and composite primary keys; root/nested/batch/system
writes; every exact logical scalar; conditional fields; relations/counts;
computed fields; revocable actors; and delete policies needing both local and
fresh relation information.

## 5. Completion record

Functional verification completed on 2026-08-07 on branch
`codex/go-phase-0`. The exact verified P7 implementation commit is
`5f420c8d16be9817a62914d315d5babed0f0bca5`.

Provider profiles:

- SQLite: file-backed databases with independent connections/processes.
- PostgreSQL `C`: `127.0.0.1:55433`.
- PostgreSQL linguistic/default: `127.0.0.1:55432`.
- No credentials were printed or recorded.

Completion results:

- Both required package commands passed; notable durations were
  `runtime 93.933s` and generator pipeline `70.173s`.
- Portable race passed with `runtime 884.088s`; the dual-PostgreSQL race
  passed with `runtime 978.467s`. There were zero race or leak failures.
- Serial whole-repository `-count=1` passed with `runtime 96.364s`.
- Serial whole-repository `-count=2` passed with `cmd/golem 251.635s`,
  generator pipeline `129.774s`, and `runtime 192.056s`.
- Ten shuffled repetitions of event/subscription/GraphQL packages passed.
- Fuzz gates passed for 30 seconds each: codec 1,913,150 executions, frozen
  request 7,117,509 executions, and WebSocket/subscription 361,277 executions.
- The real crash harness passed all 15 provider/boundary cases. It used five
  boundaries across three profiles and one killed plus one recovery process per
  case: 30 subprocesses total.
- The mutation harness killed 46/46 named mutations with zero survivors and
  zero invalid mutations.
- Concurrency evidence exercised up to four competing publisher workers, plus
  the two-worker lifecycle/restart cases.
- `go vet ./...`, repository-wide `gofmt`, and `git diff --check` passed.
- The final structured `go test -json` provider stream at
  `2026-08-07T14:29:15+02:00` emitted explicit pass events for SQLite,
  PostgreSQL `C`, PostgreSQL linguistic, and package
  `github.com/eleven-am/golem/go/internal/p7oracle` in `0.679s`, with zero fail
  or skip events.
- All 94 P7 named test/fuzz functions referenced by this ledger exist. No
  generator-owned temporary module, lock, or fuzz corpus artifact remains.

Commit `5f420c8d16be9817a62914d315d5babed0f0bca5` contains the verified P7
implementation. The following documentation-only closure commit records that
identifier and changes no tested Go code.
