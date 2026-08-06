# P4 evidence matrix

Status: **COMPLETE — all definition-of-done rows and local completion gates pass**

Completed: 2026-08-06. This is the authoritative completion ledger for
[`P4-PLAN.md`](./P4-PLAN.md). A row becomes `PASS` only after the named evidence
exists and its full provider/gate command passes. A unit test with live-provider
subtests skipped is not evidence for a live-provider row.

Provider labels:

- **SQLite live** — a real SQLite database with multiple independent
  connections and, where required, processes;
- **PostgreSQL C live** — `GOLEM_TEST_POSTGRES_DSN` with C/default collation;
- **PostgreSQL linguistic live** —
  `GOLEM_TEST_POSTGRES_LINGUISTIC_DSN`;
- **portable unit** — provider-neutral IR plus both renderings without claiming
  driver execution;
- **compile** — a fresh generated external module compiles, or a deliberately
  invalid program is required not to compile; and
- **race/process** — Go race execution and an independent helper process where
  in-process scheduling cannot prove the property.

## 1. Definition-of-done matrix

| # | Required result | Exact planned evidence | Providers/gate | State |
| --- | --- | --- | --- | --- |
| 1 | Every caller/system mutation and transaction method executes from a fresh module | `TestGeneratedMutationSurfaceExecutesEveryCallerAndSystemOperationFromFreshModule`; `TestGeneratedTransactionClientsExecuteReadsAndWritesFromFreshModule` | compile + SQLite live | **PASS — 2026-08-06** |
| 2 | Generated inputs expose exactly legal field/relation operations | `TestGeneratedMutationSurfaceAcceptsLegalPrograms`; `TestGeneratedMutationSurfaceRejectsIllegalProgramsAtCompileTime`; `TestMutationBinderRejectsForgedZeroAndDuplicateValues` | compile + portable unit | **PASS — 2026-08-06** |
| 3 | Every root/nested selector and filter is classified before transaction/SQL | `TestMutationClassificationCoversEveryValueInfluencingPositionBeforeTransaction`; `TestNestedMutationClassificationRecursesEveryAcceptedOperation`; `TestRefusedMutationDoesNotBeginTransactionOrIssueSQL` | portable unit + SQLite/PostgreSQL statement spies | **PASS — 2026-08-06** |
| 4 | Create verifies persisted defaults, explicit fields, and nested effects and rolls back denial | `TestCreateOracleAuthorizesPersistedAfterImageAndDefaults`; `TestCreateFieldPolicyUsesAuthoredFieldsAndPersistedDependencies`; `TestCreateNestedDenialRollsBackDataAndFactsAtEveryDepth` | SQLite + PostgreSQL C/linguistic live | **PASS — 2026-08-06** |
| 5 | Update/delete use constrained locked targets; missing/invisible agree | `TestSingleMutationTargetUsesActionConstraintAndLockedImage`; `TestMutationOracleInvisibleAndMissingUniqueHaveSamePublicDisclosure`; `TestDeleteProjectionUsesAuthorizedPreImage` | SQLite + PostgreSQL C/linguistic live | **PASS — 2026-08-06** |
| 6 | Exact diffs authorize only actually changed fields and preserve no-op behavior | `TestUpdateFieldAuthorizationUsesExactPersistedDiff`; `TestNoOpUpdateDoesNotRequireUnchangedFieldPermission`; `TestExactMutationDiffCoversEveryLogicalType` | portable unit + SQLite + PostgreSQL C/linguistic live | **PASS — 2026-08-06** |
| 7 | All nested operations authorize the complete touched graph with deterministic hooks/facts | `TestNestedMutationVocabularyExecutesCompleteSocialGraph`; `TestCreateNestedDenialRollsBackDataAndFactsAtEveryDepth`; `TestNestedCompositeRelationsAndRecursiveComments`; `TestNestedHookAndFactOrderIsDeterministic` | SQLite + PostgreSQL C/linguistic live | **PASS — 2026-08-06** |
| 8 | Batches are bounded, deterministic, atomic, and produce one fact per row | `TestBatchMutationExactBoundaryAndOverflowWithoutTruncation`; `TestBatchMutationCapturesStablePrimaryKeySet`; `TestBatchMutationEmitsOneOrderedFactPerAffectedRow`; `TestBatchIdentityChangeIsRefusedBeforeWrite` | SQLite + PostgreSQL C/linguistic live + interference | **PASS — 2026-08-06** |
| 9 | Upsert uses update reach, one truthful branch, database coordination, and bounded conflict | `TestUpsertProbeUsesUpdateConstraintNotReadConstraint`; `TestUpsertHiddenExistingNeverFallsThroughToUnauthorizedUpdate`; `TestUpsertSameSelectorMultiConnectionAndProcess`; `TestPostgreSQLUpsertSameSelectorMultiConnection`; `TestUpsertRetriesWholeEngineAttemptAndExhaustsAsConflict`; `TestUpsertRunsAndRecordsOnlyCommittedBranch` | SQLite + PostgreSQL C/linguistic live + race/process | **PASS — 2026-08-06** |
| 10 | Tx clients keep all work on the supplied `sqlx.Tx` | `TestCallerAndSystemTxClientsNeverEscapeTransaction`; `TestTransactionBoundReadsRelationsLoadersNestedWritesAndHooks`; `TestTransactionRollbackLeavesNoDataOrFacts`; `TestApplicationTransactionClosureIsNeverReplayed` | SQLite + PostgreSQL C live + spy/failpoint | **PASS — 2026-08-06** |
| 11 | Hooks obey transform/order/retry/rollback/system/after-commit contracts | `TestBeforeHookTransformsOwnedCloneThenRebinds`; `TestTransactionAfterHookUsesSameTransactionAndReverseNodeOrder`; `TestAfterCommitRunsOnlyAfterOutermostCommit`; `TestAfterCommitFailureReportsCommittedSuccess`; `TestSystemMutationsBypassAllHooks`; `TestUpsertHooksRepeatOnlyForEngineAttempts` | SQLite + PostgreSQL C live + failpoint | **PASS — 2026-08-06** |
| 12 | Data and exact outbox facts are atomic for root/nested/batch writes | `TestSystemOutboxV1MigratesIntrospectsAndFingerprintsBothProviders`; `TestMutationDataAndFactCommitOrRollbackTogether`; `TestOutboxExactCodecPreservesEveryLogicalType`; `TestNestedAndBatchFactsHaveStableTransactionOrdinals`; `TestFactLimitsRollBackInsteadOfDrop` | portable unit + SQLite + PostgreSQL C/linguistic live | **PASS — 2026-08-06** |
| 13 | Successful writes invalidate execution state; rollback does not | `TestSuccessfulMutationClearsAllExecutionLoaders`; `TestTransactionCommitClearsOnceAcrossEveryWriteEntryPoint`; `TestRollbackDoesNotPublishInvalidation`; `TestCallerAndSystemReadAfterWriteObserveCommittedState` | SQLite + PostgreSQL C live + race | **PASS — 2026-08-06** |
| 14 | Types, errors, limits, artifacts, SQL, binds, and fact bytes are deterministic | `TestMutationLimitsExactBoundariesAndOpenValidation`; `TestMutationErrorsAreStableAndDoNotLeakProviderFacts`; `TestMutationArtifactsPlansSQLBindsAndFactsAreDeterministicUnderShuffle`; `TestMutationExactValuesAgreeAcrossProviders` | compile + portable unit + SQLite + PostgreSQL C/linguistic live | **PASS — 2026-08-06** |
| 15 | Independent oracle, concurrency, named mutations, race, repeat, vet, format, and CI-equivalent gates pass | `TestP4IndependentMutationOracleSQLite`; `TestP4IndependentMutationOraclePostgreSQLProfiles`; all §2 mutations; all §3 commands | all | **PASS — 2026-08-06** |

### 1.1 Provider/gate audit closure

No definition-of-done row remains pending. Every live-provider row executed on
SQLite, PostgreSQL C, and PostgreSQL linguistic without provider skips. Section
3 records the final current-tree race, repeat, shuffle, vet, format, and
CI-equivalent local commands. Hosted CI remains release evidence after a later
commit/push; it is not substituted for any local provider gate.

## 2. Named-mutation matrix

These names are deliberate changes that make an incorrect implementation pass
ordinary happy-path tests. Each row must have a test that fails under the named
mutation and passes against production code.

### 2.1 Classification document (03)

| Mutation | Exact planned evidence | State |
| --- | --- | --- |
| M3 — skip nested-write position | `TestM3NestedWritePositionsAreClassifiedBeforeTransaction` | **PASS — 2026-08-06** |
| M4 — discharge a write filter against read reach instead of the action's complete selecting constraint | `TestM4WriteFilterDischargeUsesCompleteSelectingConstraint` | **PASS — 2026-08-06** |
| M5/M5′ — force constraint implication always true or always false | negative and positive branches of `TestM5WriteFieldImplicationCannotFailOpenOrRefuseEverything` | **PASS — 2026-08-06** |
| M6 — fail open on missing classification | `TestM6MutationClassifierFailsClosedForUnknownPositions` | **PASS — 2026-08-06** |
| M8 — pass unknown input keys through | compile-fail corpus plus `TestM8MutationBinderRejectsUnknownAndForgedFacts` | **PASS — 2026-08-06** |
| M9 — classify only after a statement executes | `TestM9RefusedMutationClassifiesBeforeTransactionCompilationOrProbe` | **PASS — 2026-08-06** |
| M10 — omit unique-selector classification | `TestM10SingleMutationUniqueSelectorsAreClassified` | **PASS — 2026-08-06** |
| M11 — widen canonical constraint equality with Go representation equality | P2 owns canonical equality; `TestM11WriteDischargeUsesCanonicalConstraintIdentity` exercises the P4 call path | **PASS — 2026-08-06** |

### 2.2 Surface/runtime and P4 architecture mutations

| Mutation | Exact planned evidence | State |
| --- | --- | --- |
| `MakeUpsertProbeUseReadConstraint` | `TestUpsertProbeUsesUpdateConstraintNotReadConstraint` | **PASS — 2026-08-06** |
| `ClassifyBatchWhereAsWriteOnly` | `TestBatchWhereFieldsAreClassifiedThroughReadLens` | **PASS — 2026-08-06** |
| `MakeLoaderInvalidationPerKey` | `TestSuccessfulMutationClearsAllExecutionLoaders` | **PASS — 2026-08-06** |
| `TreatContextWithNoScopeAsUnrestricted` | `TestMutationPrincipalFailureCannotBecomeSystem`; explicit caller/system compile contract | **PASS — 2026-08-06** |
| `MUTATION_WITHOUT_TX` | `TestEveryMutationEntryPointBeginsOrJoinsTransaction` | **PASS — 2026-08-06** |
| `TX_CONNECTION_ESCAPE` | `TestTransactionBoundReadsRelationsLoadersNestedWritesAndHooks` | **PASS — 2026-08-06** |
| `SKIP_CREATE_RESULT_VERIFICATION` | `TestCreateOracleAuthorizesPersistedAfterImageAndDefaults` | **PASS — 2026-08-06** |
| `AUTHORIZE_REQUESTED_NOT_CHANGED_FIELDS` | `TestNoOpUpdateDoesNotRequireUnchangedFieldPermission` | **PASS — 2026-08-06** |
| `SKIP_AFTER_IMAGE_POSTCONDITION` | `TestUpdateAfterImageMustRemainInsideUpdatePolicy` | **PASS — 2026-08-06** |
| `SKIP_NESTED_TARGET` | `TestCreateNestedDenialRollsBackDataAndFactsAtEveryDepth` | **PASS — 2026-08-06** |
| `OPAQUE_NESTED_PAYLOAD` | `TestEveryNestedWriteExpandsToTypedMutationNodes` | **PASS — 2026-08-06** |
| `TRUNCATE_BATCH_AT_LIMIT` | `TestBatchMutationExactBoundaryAndOverflowWithoutTruncation` | **PASS — 2026-08-06** |
| `BATCH_SINGLE_EVENT` | `TestBatchMutationEmitsOneOrderedFactPerAffectedRow` | **PASS — 2026-08-06** |
| `RUN_AFTER_COMMIT_BEFORE_COMMIT` | `TestAfterCommitRunsOnlyAfterOutermostCommit` | **PASS — 2026-08-06** |
| `RUN_SYSTEM_HOOKS` | `TestSystemMutationsBypassAllHooks` | **PASS — 2026-08-06** |
| `RETRY_CALLER_TRANSACTION` | `TestApplicationTransactionClosureIsNeverReplayed` | **PASS — 2026-08-06** |
| `COOPERATIVE_UPSERT_ONLY` | `TestUpsertSameSelectorMultiConnectionAndProcess` | **PASS — 2026-08-06** |
| `DROP_OUTBOX_ON_NESTED` | `TestNestedMutationEmitsFactsForEveryChangedRow` | **PASS — 2026-08-06** |
| `FACT_OUTSIDE_TRANSACTION` | `TestMutationDataAndFactCommitOrRollbackTogether` | **PASS — 2026-08-06** |
| `LOSSY_FACT_CODEC` | `TestOutboxExactCodecPreservesEveryLogicalType` | **PASS — 2026-08-06** |
| `ALLOW_BATCH_IDENTITY_CHANGE` | `TestBatchIdentityChangeIsRefusedBeforeWrite` | **PASS — 2026-08-06** |
| `RETRY_WITH_NEW_RUNTIME_VALUES` | `TestUpsertRetryFreezesRuntimeOwnedValuesOnce` | **PASS — 2026-08-06** |
| `RETURN_AFTER_COMMIT_HOOK_ERROR` | `TestAfterCommitFailureReportsCommittedSuccess` | **PASS — 2026-08-06** |
| `CLEAR_LOADERS_ON_ROLLBACK` | `TestRollbackDoesNotPublishInvalidation` | **PASS — 2026-08-06** |

## 3. Required command ledger

P4-I records the exact command, date, and result actually run. The completed
ledger below is authoritative; the focused commands recorded afterward remain
supporting evidence rather than substitutes for these full gates.

```text
PASS 2026-08-06: go test -count=1 ./golem ./internal/mutation/... ./runtime
PASS 2026-08-06: go test -count=1 ./internal/codegen/... ./internal/generate/...
PASS 2026-08-06: go test -race -count=1 ./internal/mutation/... ./runtime
PASS 2026-08-06: GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go test -shuffle=on -count=10 ./internal/mutation/... ./runtime
PASS 2026-08-06: GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go test -race -count=1 ./internal/mutation/... ./runtime
PASS 2026-08-06: go test -count=1 ./...
PASS 2026-08-06: go test -p=1 -count=2 ./...
PASS 2026-08-06: go test -p=1 -race -count=1 ./...
PASS 2026-08-06: go vet ./...
PASS 2026-08-06: test -z "$(find . -type f -name '*.go' -print0 | xargs -0 gofmt -l)"
PASS 2026-08-06: git diff --check
```

Hosted CI is release evidence after commit/push, not a substitute for the local
live-provider matrix. It must execute both PostgreSQL profiles without skipped
provider subtests before this file can claim that gate.

### Incremental P4-I evidence recorded on 2026-08-06

These focused commands passed. They do not satisfy or replace any still-pending
full command-ledger entry above.

```text
PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test ./runtime -run 'TestP4IndependentMutationOracle(PostgreSQLProfiles|SQLite)' -count=1 -v
  (SQLite, PostgreSQL C, and PostgreSQL linguistic subtests all executed; no provider skip)

PASS: go test ./runtime -run \
  'Test(P4IndependentMutationOracle|ApplicationTransactionClosureIsNeverReplayed|UpsertProbeUsesUpdateConstraintNotReadConstraint|BatchMutationExactBoundaryAndOverflowWithoutTruncation|BatchMutationCapturesStablePrimaryKeySet|BatchMutationEmitsOneOrderedFactPerAffectedRow|UpdateAfterImageMustRemainInsideUpdatePolicy|NoOpUpdateDoesNotRequireUnchangedFieldPermission|MutationDataAndFactCommitOrRollbackTogether|FactLimitsRollBackInsteadOfDrop|AfterCommitFailureReportsCommittedSuccess|SystemMutationsBypassAllHooks)' \
  -count=1 -v

PASS: go test ./runtime -run \
  'Test(RollbackDoesNotPublishInvalidation|UpsertRunsAndRecordsOnlyCommittedBranch|TransactionCommitClearsOnceAcrossEveryWriteEntryPoint)' \
  -count=1 -v

PASS: go test -count=1 ./internal/codegen/registry -run \
  'TestGenerated(MutationSurface|TransactionClients)' -v
  (fresh external module compiled and executed all caller/system and CallerTx/SystemTx mutation methods on SQLite;
   legal nested relation vocabulary compiled and illegal mutation capabilities failed compilation)

PASS: go test -count=1 ./internal/mutation/bind ./internal/mutation/plan \
  ./internal/mutation/nested ./internal/mutation/batch ./runtime -run \
  'Test(MutationBinderRejectsForgedZeroAndDuplicateValues|MutationClassificationCoversEveryValueInfluencingPositionBeforeTransaction|NestedMutationClassificationRecursesEveryAcceptedOperation|RefusedMutationDoesNotBeginTransactionOrIssueSQL|BatchIdentityChangeIsRefusedBeforeWrite|UpsertSameSelectorMultiConnectionAndProcess|M3NestedWritePositionsAreClassifiedBeforeTransaction|M4WriteFilterDischargeUsesCompleteSelectingConstraint|M5WriteFieldImplicationCannotFailOpenOrRefuseEverything|M6MutationClassifierFailsClosedForUnknownPositions|M8MutationBinderRejectsUnknownAndForgedFacts|M9RefusedMutationClassifiesBeforeTransactionCompilationOrProbe|M10SingleMutationUniqueSelectorsAreClassified|M11WriteDischargeUsesCanonicalConstraintIdentity)' -v
  (the upsert race used two synchronized helper processes; the refused mutation used a counting driver and observed zero begins/queries/execs)

PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -count=1 ./runtime -run \
  'Test(CreateOracleAuthorizesPersistedAfterImageAndDefaults|CreateFieldPolicyUsesAuthoredFieldsAndPersistedDependencies|SystemOutboxV1MigratesIntrospectsAndFingerprintsBothProviders)' -v
  (SQLite, PostgreSQL C, and PostgreSQL linguistic subtests all executed; no provider skip)

PASS: go test -count=1 ./internal/mutation/fact -run \
  'Test(OutboxExactCodecPreservesEveryLogicalType|ExactMutationDiffCoversEveryLogicalType)' -v

PASS: go test -count=1 ./runtime -run \
  'Test(CallerAndSystemTxClientsNeverEscapeTransaction|TransactionRollbackLeavesNoDataOrFacts|SuccessfulMutationClearsAllExecutionLoaders|CallerAndSystemReadAfterWriteObserveCommittedState|MutationLimitsExactBoundariesAndOpenValidation|MutationErrorsAreStableAndDoNotLeakProviderFacts|MutationArtifactsPlansSQLBindsAndFactsAreDeterministicUnderShuffle)' -v

PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -count=1 ./runtime -run \
  'Test(MutationExactValuesAgreeAcrossProviders|UpdateFieldAuthorizationUsesExactPersistedDiff|UpsertHiddenExistingNeverFallsThroughToUnauthorizedUpdate)' -v
  (SQLite, PostgreSQL C, and PostgreSQL linguistic subtests all executed; no provider skip)

PASS: go test -count=1 ./internal/mutation/upsert ./runtime -run \
  'Test(UpsertProbeUsesUpdateConstraintNotReadConstraint|UpsertRetriesWholeEngineAttemptAndExhaustsAsConflict|UpsertRetryFreezesRuntimeOwnedValuesOnce|EveryMutationEntryPointBeginsOrJoinsTransaction|MutationPrincipalFailureCannotBecomeSystem)' -v
  (every ordinary caller/system scalar and batch entry point opened exactly one provider transaction;
   every CallerTx/SystemTx mutation joined its one outer transaction without an inner begin)

PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -race -count=1 ./runtime -run \
  'Test(DeleteProjectionUsesAuthorizedPreImage|MutationOracleInvisibleAndMissingUniqueHaveSamePublicDisclosure|SingleMutationTargetUsesActionConstraintAndLockedImage|UpdateFieldAuthorizationUsesExactPersistedDiff|NoOpUpdateDoesNotRequireUnchangedFieldPermission|SuccessfulMutationClearsAllExecutionLoaders|TransactionCommitClearsOnceAcrossEveryWriteEntryPoint|RollbackDoesNotPublishInvalidation|CallerAndSystemReadAfterWriteObserveCommittedState)'
  (SQLite, PostgreSQL C, and PostgreSQL linguistic subtests all executed under the race detector; no provider skip)

PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -race -count=1 ./runtime -run \
  'Test(RefusedMutationDoesNotBeginTransactionOrIssueSQL|PostgreSQLUpsertSameSelectorMultiConnection|UpsertRetriesWholeEngineAttemptAndExhaustsAsConflict|UpsertHiddenExistingNeverFallsThroughToUnauthorizedUpdate|UpsertSameSelectorMultiConnectionAndProcess|UpsertRunsAndRecordsOnlyCommittedBranch)'
  (the refusal spies observed zero begins/queries/execs on SQLite and both PostgreSQL profiles;
   SQLite used two synchronized helper processes; PostgreSQL used four independent one-connection pools per profile;
   deterministic SQLSTATE 40001 failpoints exhausted three real PostgreSQL attempts with zero data/facts)

PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -count=1 ./runtime -run \
  'Test(CallerAndSystemTxClientsNeverEscapeTransaction|TransactionBoundReadsRelationsLoadersNestedWritesAndHooks|TransactionRollbackLeavesNoDataOrFacts|ApplicationTransactionClosureIsNeverReplayed|BeforeHookTransformsOwnedCloneThenRebinds|TransactionAfterHookUsesSameTransactionAndReverseNodeOrder|AfterCommitRunsOnlyAfterOutermostCommit|AfterCommitFailureReportsCommittedSuccess|SystemMutationsBypassAllHooks|UpsertHooksRepeatOnlyForEngineAttempts)$' -v
  (all required SQLite and PostgreSQL C transaction/hook paths executed without skips;
   the provider-neutral transaction profiles also executed PostgreSQL linguistic;
   rollback left no data/facts, transaction clients did not escape the outer transaction,
   and live PostgreSQL hook transforms, transaction-bound reads/writes, outermost after-commit,
   system bypass, committed upsert, and failure reporting all passed)

PASS: go test -race -count=1 ./runtime -run \
  'Test(BeforeHookTransformsOwnedCloneThenRebinds|TransactionAfterHookUsesSameTransactionAndReverseNodeOrder|TransactionBoundReadsRelationsLoadersNestedWritesAndHooks|UpsertHooksRepeatOnlyForEngineAttempts|NestedHookAndFactOrderIsDeterministic|CallerNested(CurrentToOne|InverseMembership|SourceMembership|SelectedChild)|CallerAndSystemTxClientsNeverEscapeTransaction|TransactionRollbackLeavesNoDataOrFacts|ApplicationTransactionClosureIsNeverReplayed|AfterCommitRunsOnlyAfterOutermostCommit|AfterCommitFailureReportsCommittedSuccess|SystemMutationsBypassAllHooks)$'
  (focused transaction, hook, recursive nested-hook, rollback, and retry paths passed under the race detector)

PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -shuffle=on -count=10 ./runtime -run \
  'Test(MutationLimitsExactBoundariesAndOpenValidation|MutationErrorsAreStableAndDoNotLeakProviderFacts|MutationArtifactsPlansSQLBindsAndFactsAreDeterministicUnderShuffle|MutationExactValuesAgreeAcrossProviders)'
  (limits opened/refused on every provider profile; deterministic SQL/binds rendered for SQLite and PostgreSQL;
   exact Int64/Decimal values executed on SQLite and both live PostgreSQL profiles)

PASS: go test -run '^$' ./...
  (every Go package compiled after the row-14 provider and shuffle changes)

PASS: go test -count=1 ./golem
  (the public declaration, mutation-input, hook, and generated-facing capability package passed independently)

PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -count=1 ./runtime -run \
  'Test(NestedMutationVocabularyExecutesCompleteSocialGraph|CreateNestedDenialRollsBackDataAndFactsAtEveryDepth|NestedCompositeRelationsAndRecursiveComments|NestedHookAndFactOrderIsDeterministic|NestedMutationEmitsFactsForEveryChangedRow|NestedAndBatchFactsHaveStableTransactionOrdinals)' -v
  (all eleven public nested operations, depth-one/depth-two denial rollback, ordered composite keys,
   true self-recursive comments, hook/fact order, and nested/batch ordinals executed on SQLite and both live PostgreSQL profiles)

PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -count=1 ./runtime -run \
  'Test(BatchMutationExactBoundaryAndOverflowWithoutTruncation|BatchMutationCapturesStablePrimaryKeySet|BatchMutationEmitsOneOrderedFactPerAffectedRow|BatchIdentityChangeIsRefusedBeforeWrite)' -v
  (the exact boundary, overflow rollback, deterministic captured set, one fact per row, and pre-write
   identity refusal executed on SQLite and both live PostgreSQL profiles; provider interference rolled back without truncation)

PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -count=1 ./runtime -run \
  'Test(MutationDataAndFactCommitOrRollbackTogether|FactLimitsRollBackInsteadOfDrop|NestedAndBatchFactsHaveStableTransactionOrdinals)' -v
  (root and batch data/fact commit-or-rollback, fact-byte overflow rollback, and nested/batch transaction ordinals
   executed on SQLite and both live PostgreSQL profiles; nested denial rollback is recorded in the row-7 command above)

PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -count=1 ./runtime -run \
  'Test(MutableSingleRowIdentityUpdateRebindsResultAndFactAcrossProviders|ReferencedIdentityUpdateFailsBeforeWriteAcrossProviders|PublicSetNullIncrementDecrementAndExactAuthorizationAcrossProviders)$' -v
  (unreferenced mutable primary identity updates returned and verified the persisted after identity and emitted exact
   before/after fact identities; relation-referenced identity updates failed before write; public Set, Null, Increment,
   and Decrement plus exact changed-field authorization executed on SQLite and both live PostgreSQL profiles without skips)

PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> \
  go test -count=1 ./runtime -run \
  'Test(DeleteAndDeleteManyHookFamiliesAcrossProviders|CallerNestedSelectedChildHooksTransformAndDeliverReverseResults)$' -v
  (Delete and DeleteMany Before/After/AfterCommit transformations, rollback veto, and system bypass executed on SQLite
   and live PostgreSQL C; a dynamic nested-child Before transform was rebound, authorized, persisted, and delivered to
   After/AfterCommit on live PostgreSQL C)

PASS: go test -race -count=1 ./runtime -run \
  'Test(MutableSingleRowIdentityUpdateRebindsResultAndFactAcrossProviders|ReferencedIdentityUpdateFailsBeforeWriteAcrossProviders|PublicSetNullIncrementDecrementAndExactAuthorizationAcrossProviders|DeleteAndDeleteManyHookFamiliesAcrossProviders|CallerNestedSelectedChildHooksTransformAndDeliverReverseResults)$'
  (the new identity, scalar-vocabulary, hook-family, rollback, and dynamic-child paths passed under the race detector)

PASS: go test -count=1 ./golem ./internal/policy/schema ./internal/policy/schematest ./internal/mutation/... ./runtime
PASS: go vet ./golem ./internal/policy/schema ./internal/policy/schematest ./internal/mutation/... ./runtime
PASS: git diff --check
```

## 4. Final closure evidence recorded on 2026-08-06

The final semantic audit added adversarial coverage beyond the originally named
matrix and then reran every completion gate against the stabilized tree:

```text
PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -count=1 ./runtime -run \
  'Test(CallerCreateRowAuthorizationUsesCompletedInverseRelationGraph|CallerCreateAuthoredFieldAuthorizationUsesCompletedRelationGraph|CallerUpdateAuthorizationUsesCompletedRelationGraphAndFieldPreimage|RequiredSourceChildCreateAuthorizationTraversesBackToCompletedParent|OptionalSourceToOneNestedUpsertCreateAssignsParentAndObeysHooksFactsAndRollback|NestedChildBeforeReplacementIntroducesDependencyAheadOfLowerFieldOrdinaryChildAndPreservesRootProjection|RootUpsertSelectedCreateSupportsEveryRequiredSourceDependencyAcrossProviders|CallerUpsertSelectedUpdateBranchPreservesNestedExactErrorCodesAcrossProviders)' -v
  (completed-graph authorization, required/optional source ownership, dynamic runtime replacement ordering,
   active root projection, root-Upsert branch authorization, and stable exact error codes passed on all three providers)

PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -count=1 ./runtime -run \
  '^TestCallerSource(ExactConnect|CurrentDisconnect)AuthorizesSelectedTargetUpdateReachAcrossProviders$' -v
  (source-owned Connect and Disconnect independently authorized target-model update reach and parent-model
   update/field grants; allowed, absent, and conditional-invisible cases passed on all three providers)

PASS: go test -count=1 ./runtime -run \
  'Test(OrderNestedFactsMissingAppliedFactIsInvariantAndRollsBack|RepeatedIdenticalFactCandidatesAreEquivalentOrRefused)$' -v
  (missing graph-owned facts rolled back data and outbox state; repeated signatures were accepted only when
   their complete V1 semantic payloads were equivalent)

PASS: go test -count=1 ./...
PASS: go test -p=1 -count=2 ./...
PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -shuffle=on -count=10 ./internal/mutation/... ./runtime
PASS: go test -race -count=1 ./internal/mutation/... ./runtime
PASS: GOLEM_TEST_POSTGRES_DSN=<C-profile> GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=<ICU-en-US-profile> \
  go test -race -count=1 ./internal/mutation/... ./runtime
PASS: go test -p=1 -race -count=1 ./...
PASS: go vet ./...
PASS: test -z "$(find . -type f -name '*.go' -print0 | xargs -0 gofmt -l)"
PASS: git diff --check
```

The final read-only semantic audit found no remaining concrete P4 blocker after
the source-membership correction. No provider subtest was skipped in the live
completion matrices.
