# P5 evidence matrix

Status: **PASS — P5 completed locally on 2026-08-06; hosted CI remains release evidence after commit/push**

This is the authoritative completion ledger for
[`P5-PLAN.md`](./P5-PLAN.md). A row becomes `PASS` only after every named test
exists and its full gate passes. A skipped live-provider subtest is not evidence.

Provider labels:

- **SQLite live** — a real SQLite database through the generated GraphQL server;
- **PostgreSQL C live** — `GOLEM_TEST_POSTGRES_DSN`;
- **PostgreSQL linguistic live** — `GOLEM_TEST_POSTGRES_LINGUISTIC_DSN`;
- **portable unit** — closed schema/binder/selection/scalar values without a live
  provider claim;
- **compile** — a fresh generated external module compiles, or a deliberately
  invalid application is required not to compile;
- **HTTP** — the standard-library handler with bounded request parsing; and
- **race/repeat** — race detector plus deterministic repeated/shuffled runs.

## 1. Definition-of-done matrix

| # | Required result | Exact planned evidence | Providers/gate | State |
| --- | --- | --- | --- | --- |
| 1 | ContractIR contains every normalized GraphQL fact and remains migration-independent | `TestGraphQLContractNormalizationMaterializesNamesOperationsLimitsEnumsAndExtensions`; `TestGraphQLOnlyChangesAffectContractFingerprintNotModelPhysicalOrMigration`; `TestGraphQLContractRejectsEveryNameExposureTypeAndReservedCollision` | portable unit + compile | **PASS** |
| 2 | SDL, gqlgen execution code, manifest, and fingerprints are deterministic and compile from a fresh module | `TestGeneratedGraphQLArtifactsAreByteIdenticalAcrossShuffledInputAndRepeatedGeneration`; `TestFreshSocialModuleGeneratesCompilesAndConstructsGraphQLServer`; `TestGraphQLGenerationPublishesAtomicallyAndRemovesOwnedStaleArtifacts` | compile + repeat | **PASS** |
| 3 | Outputs, enum values, exposure, operation allowlists, and conditional nullability exactly match the contract | `TestGeneratedGraphQLSDLMatchesExposureOperationAndNullabilityMatrix`; `TestConditionalScalarRelationListAndCountMasksStayLocalToSelectedField`; `TestExcludedRelationTargetFailsGenerationInsteadOfSilentPruning` | portable unit + SQLite/PostgreSQL live | **PASS** |
| 4 | Every exact scalar and enum round-trips or refuses identically independent of provider | `TestGraphQLExactScalarRoundTripAndInvalidCorpus`; `TestGraphQLBigIntDecimalAndJSONNeverPassThroughFloat64`; `TestGraphQLEnumNamesMapExactlyToDeclaredWireValues` | portable unit + fuzz | **PASS** |
| 5 | Query inputs bind every accepted scalar/list/JSON/relation condition and classify every field before engine work | `TestGraphQLWhereBinderCoversEveryAcceptedP2OperatorAndRelationDepth`; `TestGraphQLQueryPositionSpyVisitsWhereOrderCursorDistinctRelationAndCount`; `TestGraphQLRefusedQueryInputDoesNotOpenExecutionOrIssueSQL` | portable unit + SQLite/PostgreSQL statement spies | **PASS** |
| 6 | Unique/compound selectors, ordered orderBy, cursor, distinct, signed paging, and limits lower exactly to P3 | `TestGraphQLSelectorOrderCursorDistinctAndPagingLowerToExactP3Request`; `TestGraphQLRejectsZeroMultipleForgedAndHiddenSelectors`; `TestGraphQLDefaultPageAndExplicitReversePageRespectStricterLimitsWithoutHiddenTruncation` | portable unit + SQLite/PostgreSQL live | **PASS** |
| 7 | Aliases, fragments, directives, repeated fields, and response paths produce correct occurrence-aware projections | `TestGraphQLSelectionCompilerNormalizesFragmentsDirectivesAndCompatibleMerges`; `TestGraphQLAliasedRelationAndCountOccurrencesKeepIndependentArgumentsAndResults`; `TestGraphQLSelectionCompilerRejectsCyclesConflictsAndLimitOverflowBeforeSQL` | portable unit + SQLite/PostgreSQL live | **PASS** |
| 8 | Find-one/find-many and every nested relation/count selection equal the generated Go caller | `TestGraphQLAndGoCallerReadOracleAgreeOnRowsOrderMasksErrorsAndPolicyTrace`; `TestGraphQLNestedRecursiveSocialReadMatchesP3AcrossLoadingStrategies`; `TestGraphQLMissingAndInvisibleUniqueHaveIdenticalDisclosure` | SQLite + PostgreSQL C/linguistic live + race | **PASS** |
| 9 | Six mutation roots and all legal scalar operations execute only through P4 | `TestGraphQLMutationRootsLowerToExactP4RequestsAndResults`; `TestGraphQLCreateOmittedExplicitNullAndDefaultRemainDistinct`; `TestGraphQLCreateUpdateNullIncrementDecrementAndBatchOracle`; `TestGraphQLMutationHooksInvalidationFactsAndTransactionsMatchGoCaller` | SQLite + PostgreSQL C/linguistic live | **PASS** |
| 10 | All eleven nested operations are generated, bounded, independently authorized, and atomic | `TestGraphQLNestedMutationVocabularyExecutesCompleteSocialGraph`; `TestGraphQLNestedInputCompileAndSchemaFixturesExposeOnlyLegalCardinalityOperations`; `TestGraphQLNestedDenialAndLimitOverflowRollBackDataHooksAndFacts` | compile + SQLite/PostgreSQL C/linguistic live | **PASS** |
| 11 | One principal/caller execution exists per operation with no system fallback or cross-request state | `TestGraphQLOperationCreatesOneCallerExecutionAndSharesOnlyWithinOperation`; `TestGraphQLMissingInvalidPrincipalTouchesDatabaseZeroTimes`; `TestGraphQLConcurrentPrincipalIsolationWithSameDocumentsVariablesKeysAndAliases` | HTTP + SQLite live + race | **PASS** |
| 12 | Computed and batched-computed fields receive only masked dependencies and batch only within one execution | `TestGraphQLComputedDependenciesAreSelectedMaskedAndWithheld`; `TestGraphQLBatchedComputedFieldKeysArgumentsLimitsFailuresAndWriteInvalidation`; `TestGraphQLComputedBatchesNeverCrossPrincipalsOperationsOrCancellation` | SQLite/PostgreSQL live + race | **PASS** |
| 13 | Custom roots are typed, collision-safe, caller-only, and explicitly transactional | `TestGeneratedCustomQueryAndMutationUseCallerTypesAndExactSchema`; `TestCustomOperationCannotRequestSystemDBTxRawSQLOrUnknownType`; `TestCustomMutationTransactionCommitsAndRollsBackExactlyOnceWithoutReplay` | compile + SQLite/PostgreSQL live | **PASS** |
| 14 | Parse/validation/engine/internal errors and all public resource limits are stable and sanitized | `TestGraphQLErrorPresenterMapsEveryCodeAndNeverLeaksTrustedCause`; `TestGraphQLHTTPAndDirectExecutionLimitsRefuseAtExactBoundariesBeforeUnboundedWork`; `TestGraphQLUnexpectedPanicAndProviderErrorsReportTrustedCauseButReturnSanitizedShape` | HTTP + portable unit + fuzz | **PASS** |
| 15 | Complete social oracle and repository gates pass on every required provider | `TestP5IndependentSocialGraphQLOracleSQLite`; `TestP5IndependentSocialGraphQLOraclePostgreSQLProfiles`; all named mutations in section 2; all commands in section 3 | all | **PASS** |

## 2. Named-mutation matrix

Every mutation below must make at least one named test fail.

| Mutation | Required failing evidence | State |
| --- | --- | --- |
| `SCHEMA_FROM_DATABASE` — derive GraphQL from live tables instead of ContractIR | contract-only rename/fingerprint and no-database generation tests in rows 1–2 | **PASS** |
| `AUTHORIZE_IN_RESOLVER` — add a resolver-local policy decision | AST structural test requires capability-free prepared root/field projections; cross-caller policy trace agrees in row 8 | **PASS** |
| `DATABASE_IN_RESOLVER` — query DB directly from a generated resolver | P3/P4 statement-trace equality and generated-resolver AST capability test in rows 8–9 | **PASS** |
| `NONNULL_MASKABLE_OUTPUT` — mark a maskable scalar/relation/count non-null | local mask/null-propagation test in row 3 | **PASS** |
| `DROP_INPUT_PRESENCE` — conflate omitted, zero, and explicit null | scalar update and nested input tests in rows 9–10 | **PASS** |
| `COERCE_EXACT_TO_FLOAT` — decode BigInt/Decimal/JSON number through float64 | exact scalar corpus in row 4 | **PASS** |
| `SKIP_GRAPHQL_POSITION_CLASSIFICATION` — omit nested relation/count/filter positions | position-spy and active no-SQL refusal tests in row 5 | **PASS** |
| `COLLAPSE_ALIAS_BY_FIELD_ID` — overwrite repeated relation aliases | independent occurrence result test in row 7 | **PASS** |
| `SKIP_FRAGMENT_OR_DIRECTIVE` — inspect only direct fields | normalized selection corpus in row 7 | **PASS** |
| `CACHE_CALLER_ON_SERVER` — share a caller/policy set across requests | concurrent same-document principal isolation in row 11 | **PASS** |
| `BATCH_COMPUTED_GLOBALLY` — share computed batches across executions | computed batch isolation in row 12 | **PASS** |
| `COMPUTED_GETS_PRIVATE_DEPENDENCY` — expose an unmasked hydrated dependency | computed masking/withholding test in row 12 | **PASS** |
| `CUSTOM_GETS_SYSTEM` — inject unrestricted capability into a custom root | custom compile-fail tests in row 13 | **PASS** |
| `GRAPHQL_REPLAYS_TRANSACTION` — replay a custom transaction closure | explicit transaction counter test in row 13 | **PASS** |
| `RETURN_RAW_ERROR` — serialize driver/policy/stack details | error corpus in row 14 | **PASS** |
| `UNBOUNDED_FRAGMENT_EXPANSION` — expand cyclic/repeated fragments without limit | AST/fragment limit tests in rows 7 and 14 | **PASS** |
| `SILENT_PAGE_TRUNCATION` — cap a list without an SDL default or error | binder and active gqlgen tests require exact SDL default or visible zero-SQL refusal | **PASS** |
| `EMIT_P6_P7_EARLY` — expose analytics/subscriptions before their engines exist | schema exclusion/collision golden in rows 1–3 | **PASS** |

## 3. Required completion commands

The exact package paths may grow during implementation, but the final ledger
must record green output for at least:

```text
go test -count=1 ./internal/graphql/... ./graphql ./runtime
go test -count=1 ./internal/generate/pipeline ./internal/codegen/...
go test -race -count=1 ./internal/graphql/... ./graphql ./runtime
go test -p=1 -count=1 ./...
go test -p=1 -count=2 ./...
go test -shuffle=on -count=10 ./internal/graphql/... ./graphql
GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go test -race -count=1 ./internal/graphql/... ./graphql ./runtime
go vet ./...
test -z "$(find . -type f -name '*.go' -print0 | xargs -0 gofmt -l)"
git diff --check
```

Hosted CI is release evidence after commit/push. Local P5 completion still
requires both live PostgreSQL profiles; a green run with skipped provider tests
does not satisfy row 15.

## 4. Recorded completion run

Local completion was recorded on 2026-08-06 from `go/` with live PostgreSQL C
at port 55433 and PostgreSQL linguistic at port 55432. Every provider-required
command supplied both DSNs; no required live-provider subtest skipped.

- `go test -count=1 ./internal/graphql/... ./graphql ./runtime` with both DSNs:
  **PASS**; runtime completed in 60.470s on the final run.
- `go test -count=1 ./internal/generate/pipeline ./internal/codegen/...`:
  **PASS**; pipeline completed in 60.087s.
- `go test -race -count=1 ./internal/graphql/... ./graphql ./runtime` with both
  DSNs: **PASS**; runtime completed in 593.744s.
- `go test -p=1 -count=1 ./...` with both DSNs: **PASS**.
- `go test -p=1 -count=2 ./...` with both DSNs: **PASS**; runtime completed in
  122.694s.
- `go test -shuffle=on -count=10 ./internal/graphql/... ./graphql`: **PASS**.
- `go vet ./...`: **PASS**.
- repository-wide `gofmt -l`: **PASS**, no files reported.
- `git diff --check`: **PASS**.

The active-adapter provider proof is carried by committed generated fixtures,
not only the fallback executor:

- `p5social` attaches the pinned generated gqlgen executable and proves the
  complete six-model read graph, recursive relations, conditional masks,
  selectors, positions, aliases, fragments, directives, repeated relation and
  count occurrences, generated Go-caller parity, and hidden/missing disclosure
  on SQLite and both PostgreSQL profiles under normal, race, and five-repeat
  runs.
- `p5socialactive` attaches the same generated path and proves all six mutation
  roots, scalar presence/default/null/numeric operations, all eleven nested
  operations, independent denial, bounded touched-graph rollback, hooks, and
  generated Go-caller parity on all three providers under normal, race, and
  three-repeat runs.
- `p5extensions` proves one operation-scoped caller, computed batching and
  cancellation, masked dependencies, custom caller-only operations, explicit
  transaction commit/rollback without replay, write invalidation, and zero-SQL
  principal refusal on all three providers.
- Four bounded five-second fuzz campaigns covered direct execution, HTTP
  envelopes, all fifteen limit fields, and sanitized error presentation.

Hosted CI remains to be collected as release evidence after these changes are
committed and pushed; it is not substituted for the completed local provider
matrix.
