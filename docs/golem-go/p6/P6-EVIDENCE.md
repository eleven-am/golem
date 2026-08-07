# P6 completion evidence

Status: **complete; all definition-of-done and mutation gates passed**

This ledger is normative. P6 is not complete because its plan or public ABI
exists, because generated names compile, or because a mock SQL renderer passes.
Every required row must be backed by the named evidence on live SQLite,
PostgreSQL `C`, and PostgreSQL linguistic profiles where listed.

The immutable implementation commit
`0bbd69a8ca96b21c0a00909ac69457b7ca673109` passed every required command on
2026-08-07. The evidence-only follow-up commit does not alter the verified Go
implementation or generated artifacts.

Evidence labels:

- **portable unit** — no database; closed IR/binder/decoder behavior;
- **compile** — fresh external generated modules plus intentional compile-fail
  fixtures;
- **SQLite live** — file-backed modernc SQLite through the production provider;
- **PostgreSQL C/linguistic live** — both required live PostgreSQL profiles;
- **GraphQL** — active generated gqlgen adapter, not a fallback-only executor;
- **statement spy** — production execution with statement/argument trace;
- **oracle** — expected values produced independently of the P6 planner; and
- **race/repeat** — race detector and repeated/shuffled deterministic runs.

## 1. Definition-of-done matrix

| # | Required result | Exact planned evidence | Providers/gate | State |
| --- | --- | --- | --- | --- |
| 1 | ContractIR v3 contains every normalized analytics/scoped fact while ModelIR, physical schema, and migrations remain unchanged | `TestP6ContractNormalizationMaterializesOperationsAllowlistsPathsLimitsAndScopedRoots`; `TestAnalyticsOnlyChangesContractFingerprintAndNeverModelOrMigration`; `TestP6ContractRejectsNamesTypesPathsLimitsAndCollisions` | portable unit + compile | **PASS** |
| 2 | Generated declarations, handles, clients, GraphQL artifacts, manifest, and fingerprints are deterministic and compile from a clean module | `TestP6GeneratedArtifactsAreByteIdenticalAcrossShuffleAndRepeat`; `TestFreshP6SocialModuleGeneratesCompilesAndConstructsApp`; `TestP6GenerationPublishesAtomicallyAndRemovesOwnedStaleArtifacts` | compile + repeat | **PASS** |
| 3 | Illegal field/operator/path/request combinations are absent at compile time where possible and all zero/foreign/duplicate values fail before SQL | `TestP6PublicABICapabilityCompileMatrix`; `TestP6TextMeasureHavingFunctionsFreezeExactModeAndOperator`; `TestP6InvalidProgrammaticRequestCorpusTouchesDatabaseZeroTimes`; `TestP6FrozenRequestClonesMutableValuesAndRejectsForeignSchemaNodes` | compile + portable unit + statement spy | **PASS** |
| 4 | Existing Count, aggregate count-all, and field-count share the authorized root scope without disclosing field/null information improperly | `TestP6CountAndAggregateCountAuthorizedScopeOracle`; `TestP6CountFieldClassifiesNullDistributionButCountAllDoesNot`; `TestP6CountMissingInvisibleAndSystemStances` | SQLite/PostgreSQL C/linguistic live + oracle | **PASS** |
| 5 | Every accepted aggregate type/operator has exact provider-neutral empty, null, overflow, and decode semantics | `TestP6GeneratedMetricExactNullAndScalarMatrixAcrossProviders`; `TestP6AggregateScalarResultMatrixProviderAgreement`; `TestP6AggregateScalarResultMatrixAndStableOverflowDecode`; `TestP6EmptyAndAllNullAggregateCells`; `TestP6ExactIntegerDecimalAndTemporalNeverPassThroughFloat`; `TestP6AggregateOverflowIsStableAndNeverCoerced` | SQLite/PostgreSQL C/linguistic live + fuzz | **PASS** |
| 6 | SQLite exact functions are installed/probed and PostgreSQL numeric SQL agrees without a Go row-evaluation fallback | `TestP6SQLiteExactAggregateCapabilityProbeAndLoss`; `TestP6SQLiteDecimalAverageNeverUsesReal`; `TestP6PostgreSQLExactNumericRenderer`; `TestP6PostgreSQLExactNumericAndBinaryAnalyticsProfiles`; `TestP6ProviderExactRendererQualifiesCollatesClassifiesTiesAndReverses`; `TestP6AnalyticsStatementCountIsOneAndNoContributionRowsAreDecoded` | SQLite/PostgreSQL live + structural + statement spy | **PASS** |
| 7 | Conditional fields are accepted only when the complete contribution predicate proves access; every analytical position is classified | `TestP6ConditionalMeasureDimensionHavingAndOrderDischarge`; `TestP6UndischargedFieldRefusesByLogicalNameBeforeSQL`; `TestP6ClassificationPositionSpyCoversWhereCountMeasureDimensionHavingOrderAndGraphQLSelection` | portable unit + SQLite/PostgreSQL live + statement spy | **PASS** |
| 8 | Local group-by preserves null grouping, dimension order, private having/order measures, signed paging, and deterministic ties | `TestP6LocalGroupByCompleteSemanticOracle`; `TestP6NullKeyAndNullableMeasureGroups`; `TestP6HavingAndOrderPrivateMeasureIsAuthorizedButNotReturned`; `TestP6SignedTakeSkipAndCanonicalTieBreakAgreement` | SQLite/PostgreSQL C/linguistic live + oracle | **PASS** |
| 9 | Contribution, intermediate-group, programmatic-result, and GraphQL-result bounds refuse visibly and never return a partial prefix | `TestP6ContributionAndIntermediateOverflowReturnNoRows`; `TestP6ProgrammaticGroupLimitIsIndependentOfGraphQL`; `TestP6GraphQLMissingTakeProbesPlusOneAndExplicitTakeNeverClamps`; `TestP6Programmatic34424GroupsAreComplete` | SQLite/PostgreSQL live + GraphQL + scale | **PASS** |
| 10 | Binary string grouping/min/max/order agrees on SQLite and both PostgreSQL collation profiles while ordinary P2 text filters retain their semantics | `TestP6BinaryAnalyticalStringSemanticsAcrossProviderCollations`; `TestP6TextWhereStillUsesDeclaredP2ComparisonMode`; `TestP6TextMeasureHavingDefaultAndASCIIInsensitiveAcrossProviders`; `TestP6GraphQLTextMeasureHavingComparisonModesAcrossProviders`; `TestP6StringNullAndUnicodeCorpus` | SQLite/PostgreSQL C/linguistic live + GraphQL | **PASS** |
| 11 | Forward-to-one relation grouping applies policy at every hop, contributes each authorized root once, and uses inner semantics for absent/invisible targets | `TestP6ForwardToOneRelationGroupProviderOracle`; `TestP6RelationAbsentAndInvisibleTargetsAreIndistinguishable`; `TestP6RelationHopPolicyAndConditionalTerminalDischarge`; `TestP6RelationAverageUsesOneSQLContributionSet` | SQLite/PostgreSQL C/linguistic live + oracle + statement spy | **PASS** |
| 12 | Unsupported to-many/reverse/multiple-path/related-measure requests fail explicitly and explicit join models remain ordinary local analytics | `TestP6RelationDeclarationRejectsToManyReverseAndMultiplePaths`; `TestP6RelationRequestRejectsRelatedMeasuresAndForeignPaths`; `TestP6PostTagExplicitJoinModelAnalytics` | compile + portable unit + SQLite/PostgreSQL live | **PASS** |
| 13 | Generated GraphQL roots/types expose only configured capabilities and lower selection/input into the same runtime plan as Go | `TestP6GeneratedGraphQLAnalyticsSDLGolden`; `TestP6GraphQLSelectionDrivesMeasuresAndRejectsUngroupedKeys`; `TestP6GraphQLAndGoAnalyticsPlanPolicySQLAndResultOracle`; `TestP6GraphQLErrorSanitizationAndPrincipalIsolation` | GraphQL + SQLite/PostgreSQL C/linguistic live + race | **PASS** |
| 14 | Scoped roots/joins/fields are schema-owned, every caller scope is authorized/classified, and left joins put target policy in ON | `TestP6ScopedAuthorizedInnerAndLeftJoinOracle`; `TestP6ScopedClassificationPositionSpy`; `TestP6ScopedLeftJoinMissingAndInvisibleTargetAreIndistinguishable`; `TestP6ScopedSystemAndTransactionParity` | SQLite/PostgreSQL C/linguistic live + statement spy | **PASS** |
| 15 | Scoped grouping/aggregation has explicit SQL row-multiplication semantics, exact results, deterministic ordering, and all normal limits | `TestP6ScopedAggregateAndGroupProviderOracle`; `TestP6ScopedToManyJoinCountsAuthorizedPairsWithoutImplicitDeduplication`; `TestP6ScopedLimitAndCancellationCorpus` | SQLite/PostgreSQL live + oracle | **PASS** |
| 16 | Raw SQL, identifiers, custom ON, writes, DDL, connection access, forged roots, mixed scopes, and unsupported AST nodes cannot execute | `TestP6ScopedPublicCompileFailRedTeam`; `TestP6ScopedRuntimeForgeryAndMixedRootCorpusTouchesDatabaseZeroTimes`; `TestP6ScopedIRStructuralAllowlist` | compile + portable unit + statement spy | **PASS** |
| 17 | Every enabled scoped execution emits one sanitized complete audit record and no record leaks SQL/values/principal/driver details | `TestP6ScopedAuditStartupRequirements`; `TestP6ScopedAuditSuccessFailureCancellationAndTx`; `TestP6ScopedAuditContainsStableInventoryAndFingerprintsOnly`; `TestP6ScopedAuditShapeExcludesValuesButIncludesSignedPaging`; `TestP6ConcurrentAuditPrincipalIsolation` | portable unit + SQLite/PostgreSQL live + race | **PASS** |
| 18 | The complete social/metrics application passes independent provider, cross-entry-point, race, repeat, fuzz, vet, format, and determinism gates | `TestP6IndependentSocialAnalyticsOracleSQLite`; `TestP6IndependentSocialAnalyticsOraclePostgreSQLProfiles`; `TestP6PostgreSQLProfilesAreLiveDistinctAndCollationVerified`; `TestP6AnalyticsCallerSystemAndTransactionParity`; `TestP6AnalyticsTransactionFamiliesBindToTxAndRollbackAcrossProviders`; `TestP6AnalyticsCancelledContextReturnsNoResultAfterOneStatementAcrossProviders`; `TestP6AnalyticsAndScopedNeverInvokeOrdinaryReadHooks`; all mutations below; all commands in section 3 | all | **PASS** |

## 2. Named-mutation matrix

Every mutation below must make at least one named test fail. A code review that
would notice the mutation is not sufficient.

| Mutation | Required failing evidence | State |
| --- | --- | --- |
| `AGGREGATE_IN_GO` — fetch contributing rows and aggregate/group/merge them in Go | one-statement/no-contribution-decode test in row 6 and relation statement spy in row 11 | **PASS** |
| `POLICY_AFTER_GROUP` — aggregate first and filter authorized rows afterward | count/local/relation independent oracles in rows 4, 8, and 11 | **PASS** |
| `SKIP_MEASURE_CLASSIFICATION` — omit a selected or private measure from classification | position spy and undischarged refusal in row 7 | **PASS** |
| `COUNT_FIELD_AS_COUNT_ALL` — expose nullable field distribution without field authorization | field-count distinction in row 4 | **PASS** |
| `MASK_AGGREGATE` — return null/partial aggregate for an unauthorized field | no-SQL named refusal in row 7 | **PASS** |
| `DISCHARGE_BY_SAMPLE` — inspect current rows instead of proving the contribution predicate | conditional implication corpus in row 7 | **PASS** |
| `DECIMAL_TO_REAL` — render SQLite Decimal sum/avg through REAL | exact SQL/function tests in rows 5–6 | **PASS** |
| `INTEGER_SUM_INT64` — overflow integer sum instead of exact arbitrary precision | exact/overflow corpus in row 5 | **PASS** |
| `NATIVE_COLLATION_GROUP` — inherit provider/default collation for analytical strings | dual-collation oracle in row 10 | **PASS** |
| `NULL_SUM_ZERO` — coalesce empty/all-null sum to zero | null result matrix in row 5 | **PASS** |
| `SILENT_PROGRAMMATIC_CAP` — apply GraphQL maxGroups to generated Go GroupBy | 34,424-group and independent-limit tests in row 9 | **PASS** |
| `SILENT_GRAPHQL_TRUNCATION` — return the first maxGroups when take is omitted | plus-one overflow test in row 9 | **PASS** |
| `LIMIT_BEFORE_HAVING` — test final cap on pre-having groups only | complete local oracle and cap boundary tests in rows 8–9 | **PASS** |
| `DROP_ORDER_TIEBREAK` — omit canonical complete-key tie terms | signed paging/tie provider test in row 8 | **PASS** |
| `RELATION_TWO_PHASE_MERGE` — fetch terminal rows and merge relation groups in Go | one-statement relation average test in row 11 | **PASS** |
| `RELATION_TARGET_UNSCOPED` — omit a target-hop read policy | hop-policy/absent-invisible tests in row 11 | **PASS** |
| `LEFT_POLICY_IN_WHERE` — turn a scoped left join into an inner join by placing target policy in WHERE | scoped left-join test in row 14 | **PASS** |
| `IMPLICIT_RELATION_DEDUP` — deduplicate explicit scoped to-many pairs | explicit pair-count test in row 15 | **PASS** |
| `ALLOW_RAW_NODE` — accept raw SQL/identifier/custom ON/write node | compile and runtime red-team tests in row 16 | **PASS** |
| `MIX_SCOPE_NONCE` — accept a field/join scope from another query | mixed-root zero-SQL corpus in row 16 | **PASS** |
| `AUDIT_ONLY_SUCCESS` — omit failed/cancelled scoped executions from audit | audit outcome corpus in row 17 | **PASS** |
| `AUDIT_RAW_SQL_OR_VALUES` — include sensitive statement/bind/principal data | audit sanitization test in row 17 | **PASS** |
| `GRAPHQL_SECOND_ENGINE` — authorize or query directly inside analytics resolvers | Go/GraphQL plan/policy/SQL equality in row 13 | **PASS** |
| `EMIT_ANALYTICS_BY_RESERVED_NAME` — expose P6 roots without explicit operation/configuration | SDL allowlist/collision golden in rows 1 and 13 | **PASS** |
| `RUN_AGGREGATE_HOOKS` — infer analytics behavior from ordinary read hooks | hook-spy portion of complete application oracle in row 18 | **PASS** |

## 3. Required completion commands

Exact package paths may grow during implementation, but the final recorded run
must include at least:

```text
go test -count=1 ./internal/analytics/... ./internal/scoped/... ./internal/graphql/... ./graphql ./runtime
go test -count=1 ./internal/compiler/... ./internal/generate/pipeline ./internal/codegen/...
go test -race -count=1 -timeout=20m ./internal/analytics/... ./internal/scoped/... ./internal/graphql/... ./graphql ./runtime
go test -p=1 -count=1 ./...
go test -p=1 -count=2 -timeout=20m ./...
go test -shuffle=on -count=10 ./internal/analytics/... ./internal/scoped/... ./internal/graphql/...
GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go test -race -count=1 -timeout=20m ./internal/analytics/... ./internal/scoped/... ./internal/graphql/... ./graphql ./runtime
go test -run='^$' -fuzz='^FuzzP6ExactAnalyticsResultDecode$' -fuzztime=30s ./runtime
go test -run='^$' -fuzz='^FuzzP6FrozenAnalyticsRequestRejectionAndClone$' -fuzztime=30s ./golem
go test -run='^$' -fuzz='^FuzzP6FrozenScopedRequestRejectionAndClone$' -fuzztime=30s ./golem
go test -run='^$' -fuzz='^FuzzGraphQLExactScalarTextRoundTrip$' -fuzztime=30s ./internal/graphql/scalar
go test -run='^$' -fuzz='^FuzzGraphQLDirectParseValidationAndInputLimits$' -fuzztime=30s ./graphql
GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go run ./internal/cmd/p6mutation -module .
go vet ./...
test -z "$(find . -type f -name '*.go' -print0 | xargs -0 gofmt -l)"
git diff --check
```

All provider-required commands must supply both PostgreSQL DSNs. A green run
that reports a skipped live provider does not satisfy a row. The final evidence
must record durations, provider endpoints/profiles without credentials, skipped
test count, and the exact commit under test.

The final provider run must also preserve the structured test stream and prove
that every requested package reached package-level `PASS`, with no failed or
skipped test event. One reproducible verification is:

```text
go list ./internal/analytics/... ./internal/scoped/... ./internal/graphql/... ./graphql ./runtime | sort -u > /tmp/p6-provider-expected.txt
set -o pipefail
GOLEM_TEST_POSTGRES_DSN=... GOLEM_TEST_POSTGRES_LINGUISTIC_DSN=... \
  go test -json -race -count=1 -timeout=20m ./internal/analytics/... ./internal/scoped/... ./internal/graphql/... ./graphql ./runtime \
  | tee /tmp/p6-provider-test.json
jq -r 'select(.Action == "pass" and (.Test == null)) | .Package' /tmp/p6-provider-test.json | sort -u > /tmp/p6-provider-passed.txt
diff -u /tmp/p6-provider-expected.txt /tmp/p6-provider-passed.txt
! jq -e 'select(.Action == "fail" or .Action == "skip")' /tmp/p6-provider-test.json >/dev/null
```

The JSON stream and the two package inventories belong in the completion
artifact. A terminal `ok` line or zero exit status alone is not evidence that
the two live PostgreSQL profiles actually ran.

## 4. Independent oracle rules

The P6 oracle may not call the production binder, planner, renderer, decoder, or
GraphQL lowering code to calculate its expected answer. It uses:

- small hand-enumerated fixtures for policy, null, relation, and exact scalar
  cases;
- descriptor-independent, test-only direct SQL for provider cross-checks;
- known canonical arithmetic strings for exact integer/Decimal cases;
- explicit expected row/key order rather than sorting production output in the
  test; and
- generated Go versus active GraphQL comparison only as secondary evidence,
  never as the sole oracle because both share the production engine.

The social/metrics fixture includes User, Post, Comment, Friendship, Tag,
PostTag, and a Metric model containing every accepted logical aggregate type,
nullable values, Unicode/binary-order strings, large integers, fixed Decimal,
and temporal boundaries. At least two actors have overlapping but unequal row,
field, and relation reach.

## 5. Completion record

Verified implementation commit:
`0bbd69a8ca96b21c0a00909ac69457b7ca673109`.

Provider profiles were SQLite, PostgreSQL `C` on `127.0.0.1:55433`, and
PostgreSQL linguistic (`en_US.utf8`) on `127.0.0.1:55432`. Credentials were not
recorded. Required command results:

- provider package count-one: **PASS**, 426 seconds;
- compiler/generator/codegen: **PASS**, 83 seconds;
- provider package race: **PASS**, 880 seconds;
- full serial module count-one: **PASS**, 417 seconds;
- full serial module count-two: **PASS**, 797 seconds;
- ten shuffled analytics/scoped/GraphQL iterations: **PASS**, 190 seconds;
- structured provider race: **PASS**, 901 seconds, 16/16 packages, 1,546 test
  passes, 452 named PostgreSQL-profile subtest passes, zero skips, zero
  failures;
- exact decode fuzz: **PASS**, 30,243 executions in 32.222 seconds;
- frozen analytics-request fuzz: **PASS**, 2,131,016 executions in 30.503
  seconds;
- frozen scoped-request fuzz: **PASS**, 1,260,857 executions in 30.435
  seconds;
- GraphQL exact-scalar fuzz: **PASS**, 2,018,068 executions in 32.456 seconds;
- GraphQL parse/input-limit fuzz: **PASS**, 77,015 executions in 32.249
  seconds;
- mutation verification: **PASS**, 341 seconds, 25 killed, zero survived,
  invalid, or skipped; and
- `go vet`, gofmt cleanliness, `git diff --check`, exact HEAD, and clean tracked
  and untracked tree checks: **PASS**.

The structured JSON stream and expected/passed inventories were retained during
verification as `/tmp/p6-0bbd69a-provider-test.json`,
`/tmp/p6-0bbd69a-provider-expected.txt`, and
`/tmp/p6-0bbd69a-provider-passed.txt`.
