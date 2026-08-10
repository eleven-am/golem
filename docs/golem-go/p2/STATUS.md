# Phase 2 status and definition-of-done audit

Audit date: **2026-08-05**

Status: **P2 is complete and verified.** The closed operator registry promotes
the complete 41-entry portable agreement inventory, runtime startup accepts that
proved inventory while refusing unknown or unproved entries, and CI enforces the
full deterministic test, repeat, shuffle, race, vet, formatting, and diff-check
command set. The closure run passed 630 live probes across the Go evaluator,
migrated SQLite, PostgreSQL 17 with C collation, and PostgreSQL 17 with en_US
collation.

This file is the evidence ledger for [`P2-PLAN.md`](./P2-PLAN.md). It distinguishes
three states:

- **implemented** — production code and a directly relevant checked-in test exist;
- **verified** — the checked-in test exists and passed in the P2 closure run; and
- **blocked** — a required production or release-gate step is absent.

## Definition-of-done audit

| # | P2 definition of done | Status | Production owner | Checked-in test owner and evidence |
|---:|---|---|---|---|
| 1 | Complete typed authoring surface, ordered grants/denials, and field rules | verified | `go/golem`; `internal/codegen/model`; `internal/codegen/bindings` | `golem/declaration_test.go`; `golem/policy_predicate_test.go`; `internal/codegen/bindings/bindings_test.go`; `internal/generate/pipeline/pipeline_test.go` |
| 2 | Immutable typed trees with generated identities and canonical exact values | verified | `golem/policy_predicate.go`; `golem/policy_values.go`; `internal/policy/ir`; `internal/policy/bind`; `internal/policy/schema` | public freeze/value tests; `ir/ir_test.go`; `bind/bind_test.go`; `schema/registry_test.go` |
| 3 | Deterministic, fail-closed construction, freeze, normalization, and encoding | verified | `golem`; `internal/policy/normalize`; `internal/policy/ir` | `TestRulesFreezeIsDeterministicAndConcurrentViewsAreRaceFree`; normalization shuffle/idempotence/forbidden-rewrite tests; canonical IR tests |
| 4 | Row and field lenses agree with an independent first-match oracle | verified | `internal/policy/resolve` | `TestResolutionAgreesWithIndependentFirstMatchOracle` exhausts 1,111 chains through length three; `TestNamedPriorityAndScopeMutations`; Phase 0 remains independent historical evidence only |
| 5 | `always`/`conditional`/`never`, deterministic requirements, merged dependencies, conservative discharge | verified | `internal/policy/classify`; `dependency`; `imply` | classification oracle/regression tests; dependency merge/order tests; implication positive, negative, bounded, and instant tests |
| 6 | Same identities in Go, SQLite, and PostgreSQL for the complete portable operator matrix | verified | `evaluate`; `policy/sql`; both provider dialects; `policy/oracle` | evaluator dispatch covers all 41 registry entries; `TestSQLiteProviderAgreementLive`; `TestPostgreSQLProviderAgreementLiveProfiles` on C-default and linguistic-default clusters |
| 7 | Every SQL predicate is two-valued | verified | both provider dialects and the shared oracle protocol | SQLite UDF unknown-count tests, PostgreSQL named live tests, and the shared oracle's `IS NOT TRUE`, negation, and `UnknownCount` controls passed in the closure run |
| 8 | Values are parameters; identifiers come only from validated P1 descriptors | verified | `internal/policy/sql`; `internal/policy/schema`; provider codecs | `policy/sql/compile_test.go` covers aliases, copied arguments, fingerprint/proof drift, and alias capture; schema registry forgery tests; provider renderer goldens |
| 9 | Unsupported provider/storage/operator facts fail before execution and are never post-filtered | verified | compiler/binder/schema/capability proof/runtime set | binding/provider failures, compile capability refusal, provider capability tests, and runtime preflight tests exist. All 41 portable entries are agreement-proved; valid non-constant policies activate, while unknown or unproved entries refuse before execution |
| 10 | Full acceptance and named-mutation suites pass, including live SQLite and both PostgreSQL profiles | verified | repository CI plus all P2 owners | CI provisions both PostgreSQL profiles and runs test, repeat, shuffle, race, vet, formatting, and diff-check gates. Named M1–M30 owners are reconciled below; the 630-probe closure run passed on all required engines/profiles |

## Work-wave status

| Wave | Status | Notes |
|---|---|---|
| P2-A | complete | Historical typed baseline. |
| P2-B | complete | Complete portable public ABI is generated and verified. |
| P2-C | complete | Runtime schema decoding, IR, binder, normalization, and canonical identity are verified. |
| P2-D | complete | Ordered row and field resolution plus the independent-chain oracle are verified. |
| P2-E | complete | Exact evaluator and dependency collection are verified. |
| P2-F | complete | Conservative implication and classification are verified. |
| P2-G | complete | Shared SQL compiler and both provider dialects/codecs are verified. |
| P2-H | verified | Social corpus, SQLite live adapter, and both PostgreSQL collation profiles passed. |
| P2-I | complete | Fresh actor-scoped construction and preflight are verified; all 41 portable entries are agreement-promoted and unknown/unproved additions remain fail-closed. |
| P2-J | complete | The definition of done, named mutation owners, provider boundaries, Phase 0 quarantine, and P3+ exclusions are recorded without overclaiming later phases. |

## Provider extensions and limits

### SQLite

SQLite support is not an approximation of PostgreSQL syntax. The provider
registers deterministic modernc scalar functions for ASCII-only folding,
scalar-list predicates, and exact JSON predicates. They run inside the SQL
`WHERE` fragment and return integer `0` or `1`; authorization is not evaluated on
rows after selection or pagination. Startup probes the functions on pooled
connections and binds the capability proof to the exact physical-schema
fingerprint.

Scalar lists use P1's JSON-array storage. SQLite JSON1 is still used for physical
validation and ordinary provider operations, but policy equality, exact numeric
comparison, list element typing, and JSON path semantics do not rely on JSON1
float coercion or raw JSON text equality.

### PostgreSQL

PostgreSQL uses `COLLATE "C"` where byte-order semantics require it, guarded
`jsonb` expressions, correlated `EXISTS`, and JSON-array scalar-list storage. It
does not use PostgreSQL native arrays for the portable list ABI.

PostgreSQL `jsonb` represents JSON numbers through PostgreSQL `numeric`. The
policy codec rejects a canonical exact JSON number whose coefficient/exponent is
outside that physical range before SQL execution. This is an explicit provider
limit, not rounding and not a JavaScript safe-integer ceiling.

### Go exact JSON numbers

The public value layer and evaluator retain JSON numbers as a canonical sign,
coefficient, and base-10 exponent. Parsing uses exact tokens, rejects duplicate
object keys and trailing data, and never routes through `float64`. Values above
`2^53-1` therefore remain exact. A value representable in Go but not in an active
provider fails closed at provider encoding/planning.

There are currently no PostgreSQL-only or SQLite-only public operator handles.
The deterministic SQLite functions and PostgreSQL physical-range refusal are
provider implementations/limits of the same portable method vocabulary.

## Named-mutation audit

The generic agreement corpus has a primary and negated probe for every registry
entry, and `TestSocialCorpusHasCanonicalShapeAndOperatorBijection` prevents an
operator from being added without both. That is broad semantic coverage, but it
does not by itself satisfy the contract's stronger requirement that every M1–M30
anchor have the named test owner recorded in `PROVIDER-AGREEMENT.md`.

The owner ledger is now reconciled to concrete test functions/subtests in
`PROVIDER-AGREEMENT.md` section 8. M1–M30 each map to an execution result,
truth-table result, identity/unknown-count protocol, registry bijection, or
pre-render refusal. Several anchors share one table-driven test, but each retains
its M-number in the subtest name or an explicit row in the controlling ledger.

M22's original projection/decoder mutation remains a P3 obligation. P2 may prove
only that its own oracle and codecs are exact; it must not claim the future P3
read-decoding test.

## Closure verification

- The registry inventory contains 41 portable, agreement-proved entries; runtime
  construction accepts a non-constant proved policy and rejects unproved facts.
- The agreement corpus executes 630 probes, including the complete scalar,
  scalar-list, JSON, relation, adversarial, and generated-handle cells.
- The closure run passed the evaluator, SQLite, PostgreSQL 17 C-default, and
  PostgreSQL 17 en_US-default profiles, including provider-migrated
  `Lower` -> `ApplyInitial` -> `Verify` schemas.
- CI runs the full suite, a repeated full suite, policy shuffle iterations, race,
  vet, `gofmt`, and `git diff --check`. Both PostgreSQL DSNs are mandatory in the
  completion profile; absence is a hard failure there.

## P3+ exclusions

P2 ends at immutable policies, row/field constraints, classification,
dependencies, exact evaluation, and parameterized predicate fragments. It does
not claim:

- complete read statements, projection, ordering, pagination, relation loading,
  result decoding, or applying field masks — P3;
- mutation planning/execution, diffs, hooks, transactions, upsert, or outbox
  facts — P4;
- GraphQL schema generation, resolvers, selection/nullability behavior, or error
  mapping — P5;
- aggregates, group-by, relation aggregation, or aggregate caps — P6;
- events, subscriptions, delivery fan-out, outbox consumption, or CDC — P7; or
- the later adversarial hardening/release phases.

Later phases must consume P2 constraints and fragments and may not reimplement
authorization semantics in an operation-specific layer.
