# P3 evidence matrix

Status: **COMPLETE — every P3 implementation gate passes**

Last reconciled: 2026-08-06. This file records executable evidence, not intended
coverage. A test name counts only after its stated command/provider run passes.
`PENDING` is deliberately not equivalent to a skipped test or a locally green
package with live DSNs absent.

Provider labels:

- **SQLite live** — a real SQLite database created by the provider;
- **PostgreSQL C live** — `GOLEM_TEST_POSTGRES_DSN` on the C/default profile;
- **PostgreSQL linguistic live** —
  `GOLEM_TEST_POSTGRES_LINGUISTIC_DSN`;
- **portable unit** — the same normalized plan/renderer/decoder corpus is checked
  for both provider representations without claiming a live driver;
- **compile** — a generated external module is compiled, or an invalid program is
  required not to compile.

## 1. Definition-of-done matrix

| # | Required result | Exact evidence | Providers/gate | State |
| --- | --- | --- | --- | --- |
| 1 | All four generated caller/system reads execute | `TestGeneratedReadSurfaceExecutesEveryCallerAndSystemOperationFromFreshModule`; `TestFreshExternalModuleBuildsPipelineArtifactsAndExecutesGeneratedReadClients` | compile + SQLite live | **PASS** |
| 2 | Caller row policy precedes order/page | `TestRootReadExecutionDecodesRowsAndAppliesPolicyBeforePaging`; `TestRenderedSQLiteStatementExecutesPolicyBeforePage`; `TestP3ReadOraclePolicyPrecedesPagingAndPrimaryKeyBreaksTies`; `TestP3PostgreSQLLiveAuthorizedReadGraph` | SQLite live + PostgreSQL C live | **PASS** |
| 3 | Every selected/filtered/counted relation applies target policy | `TestRelationExecutionUsesAuthorizedBoundedChildPlans`; `TestRelationCountExecutesTargetPolicyAndWhereInSQLAndStripsDependencies`; `TestRuntimeRecursivelyScopesRootRelationPolicyAndRejectsCyclesBeforeSQL`; `TestImmediateBatchedChildOwnsAuthorizedRelationCountSQLite`; `TestP3PostgreSQLImmediateBatchedChildOwnsAuthorizedRelationCount` | SQLite live + PostgreSQL C/linguistic live | **PASS** |
| 4 | Every value-influencing position is classified before SQL | `TestRequestRejectsNonPublicFieldsInEveryExplicitPosition`; `TestRequestRejectsNonPublicFieldsThroughVisibleRelations`; `TestConditionalFieldDisclosureIsDischargedAtRootNestedAndCountPositions`; generated compile-fail cases in `TestGeneratedReadSurfaceRejectsInvalidProgramsAtCompileTime` | portable unit + compile | **PASS** |
| 5 | Field lens projects, masks, or refuses exactly | `TestConditionalMaskRelationHydrationIsPrivatePolicyScopedAndProjectionInvariant`; `TestP3PostgreSQLConditionalMaskPrivateDependencies`; `TestMASK_THE_DISTINCT_KEYSQLite`; `TestP3PostgreSQLMASK_THE_DISTINCT_KEY`; `TestDefaultProjectionExcludesNonPublicFieldsAtRuntime` | SQLite live + PostgreSQL C/linguistic live | **PASS** |
| 6 | Private dependencies never appear publicly | `TestRelationCountExecutesTargetPolicyAndWhereInSQLAndStripsDependencies`; `TestConditionalMaskRelationHydrationIsPrivatePolicyScopedAndProjectionInvariant`; `TestP3PostgreSQLConditionalMaskPrivateDependencies`; `TestP3PostgreSQLMASK_THE_BATCH_KEY` | SQLite live + PostgreSQL C/linguistic live | **PASS** |
| 7 | Correlated and bounded-batch plans agree in rows/types/order/shape | `TestMutationMASK_ONE_STRATEGYIndexedCorrelatedSQLiteAgreesWithBatchAndCoversToOne`; `TestP3IndependentReferenceOracleSQLite`; `TestRuntimeReadLimitsRefuseOverflowWithoutSilentTruncationAndAreIsolated` (production-correlated and forced-batched) | SQLite live + race | **PASS** |
| 8 | Every declared logical type decodes exactly on both providers | `TestExactDecoderMatrixSQLiteAndPostgreSQLRepresentations`; `TestExactDecoderSQLiteLiveDriverRepresentations`; `TestExactDecoderPostgreSQLLiveDriverRepresentations`; `TestExactDecoderPostgreSQLLiveLinguisticDriverRepresentations`; `TestCorrelatedExactBigIntAndDecimalSQLiteLive`; `TestCorrelatedExactBigIntAndDecimalPostgreSQLProfilesLive` | portable unit + SQLite live + PostgreSQL C/linguistic live | **PASS** |
| 9 | Limits, stable errors, missing/invisible equality, system separation | `TestRuntimeReadLimitsRefuseOverflowWithoutSilentTruncationAndAreIsolated`; `TestConfiguredRowLimitsPlanCapPlusOneAndPreserveStricterSchemaCaps`; `TestConfiguredStatementBoundsAcceptExactBoundaryAndRefuseOverflow`; `TestConfiguredBatchLoaderKeyLimitAcceptsExactBoundaryAndRefusesOverflow`; `TestP3ReadOracleInvisibleAndMissingUniqueHaveSamePublicDisclosure`; `TestForPrincipalFailureCannotBecomeSystem`; `TestCountParameterLimitUsesStablePublicBadUserInput` | SQLite live + portable unit + race | **PASS** |
| 10 | Concurrent callers leak no policy/loader/statement/result state | `TestOpenCreatesIsolatedCallerAndExplicitSystemExecutions`; `TestP3ReadOracleConcurrentPrincipalIsolation`; `TestForPrincipalSnapshotsMutableActorOnceForPoliciesHooksAndConcurrentReads`; `TestRuntimeReadLimitsRefuseOverflowWithoutSilentTruncationAndAreIsolated` | SQLite live + race | **PASS** |
| 11 | Generated artifacts and SQL/bind order are deterministic | `TestEmitApplicationRegistryDeterministic`; `TestBuildIsByteIdenticalUnderShuffledLowererOptionPackageAndArtifactOrder`; `TestRenderIsDeterministicAcrossProvidersAndBindsPaging`; `TestBoundedBatchSQLIsDeterministicPortableAndPerParent`; `TestPostgreSQLCursorPlaceholdersAreDeterministicAndRebased` | compile + portable unit | **PASS** |
| 12 | Complete live oracle, race, mutation, vet, format, and CI-equivalent gates | SQLite oracle: `TestP3IndependentReferenceOracleSQLite`; PostgreSQL oracle: `TestP3IndependentReferenceOraclePostgreSQLProfiles`; magnitude: `TestBatchChunkMagnitudeAndPerParentPagingSQLite`, `TestBatchChunkMagnitudePostgreSQLProfiles`; exact types: item 8; named mutations: §2; repository commands: §3 | all | **PASS** — repository-wide local suite/vet/race and both live PostgreSQL profiles pass; hosted CI is release evidence after commit/push, not an uncommitted implementation gate |

## 2. Named-mutation matrix

### 2.1 Classification document (03)

P4 write positions and P6 relevance/analytics positions remain owned by their
phases; they are listed so an out-of-scope mutation cannot be mistaken for missing
P3 evidence.

| Mutation | P3 evidence | State |
| --- | --- | --- |
| M1 — misattribute a relation hop | `TestPolicyExpanderRecursivelyScopesEveryRelationTarget`; `TestCallerRootPolicyCannotObserveTargetPolicyInvisibleRows`; `TestRuntimeRecursivelyScopesRootRelationPolicyAndRejectsCyclesBeforeSQL`; relation quantifier cases in `TestRelationEveryIsRewrittenAsNoAuthorizedCounterexample` and `TestRelationNullUsesVisibilityAwareExistence` | **PASS** |
| M2 — skip `distinct` | Root and nested refusal/discharge cases in `TestConditionalFieldDisclosureIsDischargedAtRootNestedAndCountPositions`; bind position in `TestRequestRejectsNonPublicFieldsInEveryExplicitPosition`; live refusal in both `TestMASK_THE_DISTINCT_KEYSQLite` and `TestP3PostgreSQLMASK_THE_DISTINCT_KEY` | **PASS** |
| M3 — skip nested-write position | P4 mutation authorization | **OUT OF SCOPE (P4)** |
| M4/M5/M5′ — wrong selecting constraint/implication for writes | P4 mutation authorization; P3 read discharge is covered by `TestConditionalFieldDisclosureIsDischargedAtRootNestedAndCountPositions` | **OUT OF SCOPE (P4)** |
| M6 — fail open on missing classification | Closed classifier result in `TestFieldsClassifiesAlwaysConditionalAndNever`; P3 refuses non-public/unknown positions in `TestRequestRejectsNonPublicFieldsInEveryExplicitPosition` | **PASS** |
| M7 — skip `_relevance` | P6 ordering/analytics surface | **OUT OF SCOPE (P6)** |
| M8 — pass unknown keys through | Typed/identity refusal in `TestRequestRejectsForgedAndCrossModelFacts`, `TestRequestRejectsNonPublicFieldsThroughVisibleRelations`, `TestRequestRejectsCrossModelWhereAndRelation`, and generated compile-fail cases | **PASS** |

### 2.2 Statement-shape document (04)

| Mutation | Exact evidence/providers | State |
| --- | --- | --- |
| `MASK_ONE_STRATEGY` | `TestMutationMASK_ONE_STRATEGYIndexedCorrelatedSQLiteAgreesWithBatchAndCoversToOne`; race; indexed production-correlated versus batch | **PASS** |
| `NO_CHUNK` | `TestBatchChunkMagnitudeAndPerParentPagingSQLite` proves 33,000 parents, exact `1 + ceil(n/900)` statements, and chunk-boundary rows; `TestBatchChunkMagnitudePostgreSQLProfiles` proves 70,000 parents on both PostgreSQL profiles | **PASS (SQLite + PostgreSQL C/linguistic live)** |
| `BATCH_WIDE_LIMIT` | `TestBatchChunkMagnitudeAndPerParentPagingSQLite` proves `skip:1,take:2` and `skip:1,take:-2` over 950 parents × 3 children, including the 900-key boundary; `TestBoundedBatchSQLIsDeterministicPortableAndPerParent` pins both dialect renderings | **PASS** |
| `JOIN_FOR_RELATION_FILTER` | `TestMutationJOIN_FOR_RELATION_FILTERUsesExistsWithoutChangingCardinality` | **PASS (SQLite live)** |
| `NO_TEXT_CAST_BIGINT` | `TestCorrelatedJSONExactTextAndEmptyArrayBoundariesArePinned`; `TestCorrelatedExactBigIntAndDecimalSQLiteLive`; `TestCorrelatedExactBigIntAndDecimalPostgreSQLProfilesLive`; values include `±9007199254740993`, legal `Decimal(18,13)` `±12345.6789012345678`, and `0.0000000000001` | **PASS (SQLite + PostgreSQL C/linguistic live, race)** |
| `DROP_COALESCE` | `TestCorrelatedJSONExactTextAndEmptyArrayBoundariesArePinned`; live exact correlated tests assert empty non-nil relation; SQLite also asserts one correlated statement | **PASS** |
| `MASK_THE_DISTINCT_KEY` | `TestMASK_THE_DISTINCT_KEYSQLite`; `TestP3PostgreSQLMASK_THE_DISTINCT_KEY` | **PASS (SQLite + PostgreSQL C/linguistic live, race)** |
| `MASK_THE_BATCH_KEY` | `TestMASK_THE_BATCH_KEYSQLite`; `TestP3PostgreSQLMASK_THE_BATCH_KEY`; each explicitly runs production-correlated and forced-batched and verifies the chooser/forced strategy | **PASS (SQLite + PostgreSQL C/linguistic live, race)** |
| `IGNORE_INDEX_METADATA` | `TestMutationIGNORE_INDEX_METADATAChangesThePhysicalPlan`; answer parity in `TestMutationMASK_ONE_STRATEGYIndexedCorrelatedSQLiteAgreesWithBatchAndCoversToOne` | **PASS** |
| `FORGE_SCOPED_ROOT` | Scoped/raw SQL is P6 | **OUT OF SCOPE (P6)** |
| `TYPE_IS_THE_BOUNDARY` | Scoped/raw SQL is P6 | **OUT OF SCOPE (P6)** |

### 2.3 Surface/runtime document (05)

| Mutation | P3 evidence | State |
| --- | --- | --- |
| `CachePolicyOnEngine` | `TestOpenCreatesIsolatedCallerAndExplicitSystemExecutions`; `TestP3ReadOracleConcurrentPrincipalIsolation`; fresh policy construction in `TestBuildGeneratedPolicySetIsFreshAndConcurrentActorScoped` | **PASS (race)** |
| `DropExecutionKeyFromLoader` | Subscription/event reuse is P7. P3 relation loaders are execution-local and non-retained, covered structurally by caller isolation. | **OUT OF SCOPE (P7 named mutation)** |
| `MakeLoaderInvalidationPerKey` | Write invalidation is P4/P7 | **OUT OF SCOPE (P4/P7)** |
| `TreatContextWithNoScopeAsUnrestricted` | Go P3 replaced implicit context scope with unforgeable explicit `Caller` and `System`; `TestForPrincipalFailureCannotBecomeSystem` and nil-caller entry checks prove failure cannot become unrestricted | **PASS (architecture resolution)** |
| `LetSubscriptionIgnoreCtxDone` | P7 subscriptions | **OUT OF SCOPE (P7)** |
| `SerialiseLoaderArgsWithFmt` | `TestCanonicalBatchTupleIsTypedAndCollisionFree` covers typed canonical relation-loader tuples and collision pairs | **PASS** |
| `AllowRelationWhereOnToOne` | compile-fail case `bounded arguments on to-one` in `TestGeneratedReadSurfaceRejectsInvalidProgramsAtCompileTime` | **PASS (compile)** |
| `SkipFilterClassificationInsideProjection` | nested filter/order/distinct/count cases in `TestConditionalFieldDisclosureIsDischargedAtRootNestedAndCountPositions`, `TestRequestRejectsNonPublicFieldsThroughVisibleRelations`, and `TestRequestBindsRelationCountInsideNestedRelationProjection` | **PASS** |
| `DedupeSubscriptionEvaluationAcrossCallers` | P7 subscriptions | **OUT OF SCOPE (P7)** |

## 3. Recorded commands

Recorded green commands relevant to the matrix:

```text
go test -count=1 ./internal/read/... ./runtime ./internal/codegen/registry
go test -count=1 ./internal/generate/pipeline
go test -race -count=1 ./runtime -run 'TestRuntimeReadLimits|TestReadLimits|TestConfiguredBatchLoader'
GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go test -race -count=1 -v ./runtime \
  -run 'TestCorrelatedExactBigIntAndDecimal|TestMutationMASK_ONE_STRATEGY'
GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go test -count=1 -v ./runtime \
  -run '^TestP3IndependentReferenceOraclePostgreSQLProfiles$'
GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go test -count=1 -v ./runtime \
  -run '^TestBatchChunkMagnitudePostgreSQLProfiles$'
GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go test -count=1 -v ./internal/read/decode \
  -run 'TestExactDecoderPostgreSQLLiveDriverRepresentations|TestExactDecoderPostgreSQLLiveLinguisticDriverRepresentations'
go test -race -count=1 -v ./internal/read/sql \
  -run 'TestCorrelatedJSONExactTextAndEmptyArrayBoundariesArePinned|TestIndexedToManyRendersAuthorizedCorrelatedJSONAcrossProviders|TestMutationIGNORE_INDEX'
go test -count=1 ./...
go test -p=1 -count=1 ./...
go test -p=1 -count=2 ./...
go test -shuffle=on -count=10 ./internal/policy/...
go test -p=1 -race -count=1 ./...
go test -race -count=1 ./golem ./internal/read/... ./runtime
go vet ./...
test -z "$(find . -type f -name '*.go' -print0 | xargs -0 gofmt -l)"
git diff --check
```

The repository-wide local commands are the executable CI-equivalent gate for
this uncommitted phase. No hosted GitHub run is claimed. Hosted CI becomes
release evidence after commit and push; when run, its PostgreSQL jobs must use
both required profiles rather than passing through skipped live subtests.
