# P6 analytics and scoped reads plan

Status: **complete; exact implementation commit and evidence ledger recorded**

Authority: [`../BIBLE.md`](../BIBLE.md), especially sections 3, 5, 8, 11,
12, 15, 18–21, and 23. P1 owns logical/physical schema, stable identities,
generated descriptors, and migration independence. P2 owns policy resolution,
predicate SQL, implication, and field classification. P3 owns caller/system
executions, exact scalar codecs, authorized row scopes, and ordinary count. P5
owns GraphQL hosting and selection compilation. P6 composes those contracts; it
does not create a second policy, scalar, database, or GraphQL runtime.

The application-facing contract is frozen in
[`PUBLIC-ANALYTICS-ABI.md`](./PUBLIC-ANALYTICS-ABI.md). The mandatory completion
ledger is [`P6-EVIDENCE.md`](./P6-EVIDENCE.md). P6 is not complete while any
required ledger row is `PENDING`, `FAIL`, skipped on a required provider, or
supported only by prose.

## 1. Definition of down

P6 is done when a freshly generated social/metrics application can execute all
of the following without handwritten SQL or resolvers:

- the existing authorized `Count` operation through the shared analytical row
  scope;
- typed local `Aggregate` with count, field-count, sum, average, minimum, and
  maximum;
- typed local `GroupBy` with dimensions, `where`, aggregate `having`, aggregate
  and key ordering, signed `take`, and `skip`;
- configured forward-to-one `RelationGroupBy`, with local and terminal relation
  dimensions and root-model measures;
- generated GraphQL aggregate, group-by, and relation-group-by roots using the
  same runtime operations;
- a Go-native typed, read-only scoped builder for explicit authorized joins,
  filtering, grouping, aggregation, ordering, and paging; and
- exact, bounded, deterministic behavior on live SQLite, PostgreSQL with `C`
  collation, and the required PostgreSQL linguistic profile.

Every operation must begin with the same authorized root-row constraint as an
ordinary P3 read. Every named dimension, measure, field-count, `having`, order,
filter, selected scoped expression, and join must be classified before SQL.
Conditional access is allowed only when P2 proves it for every contributing row.
An ineligible analytical field is refused by public logical name; aggregate
values are never partially masked.

All aggregation and grouping executes in the database. Go may bind immutable
requests, classify them, render parameterized SQL, decode typed result cells,
and enforce a returned overflow sentinel. It may not fetch contributing rows and
count, group, merge, average, or reorder them in memory. SQLite exact integer
and Decimal arithmetic is implemented as provider-owned, capability-probed SQL
aggregate functions executed by the SQLite VM; it is not a post-query row loop.

P6 completion requires every gate and named mutation in `P6-EVIDENCE.md` to
pass. A generated method, SDL name, unit-only renderer, or skipped PostgreSQL
test is not completion evidence.

## 2. Exact boundary

### 2.1 Included

- Caller, System, CallerTx, and SystemTx parity for local analytics.
- The existing P3 count API, verified against the P6 row-scope oracle.
- Local scalar/enum dimensions and the exact measure matrix in the public ABI.
- Null grouping, empty-set results, deterministic ties, and exact transport.
- One configured forward to-one path per relation-group request. A model may
  declare several terminal dimensions only when all share that path.
- Inner-path semantics for an absent or policy-invisible relation target.
- GraphQL-specific group limits distinct from trusted programmatic limits.
- Explicit programmatic limits that do not inherit or silently reuse GraphQL
  limits.
- A scoped builder rooted at a generated model and joined only through declared
  relation handles.
- An application-controlled scoped-query audit identity and mandatory audit
  sink for every enabled scoped execution.
- SQLite and PostgreSQL provider capability probes and live agreement fixtures.

### 2.2 Excluded from P6 completion

- General high-level to-many, reverse, many-to-many, multi-path, or repeated-path
  relation aggregation.
- Measures taken from a related model in `RelationGroupBy`.
- Date buckets, expression dimensions, distinct count, percentile, median,
  statistical aggregates, window functions, cubes, rollups, and grouping sets.
- Aggregate/read hooks. P6 analytics and scoped reads are deliberately hook-free.
- Raw SQL, raw identifiers, custom join predicates, arbitrary expressions,
  subqueries, CTEs, unions, writes, DDL, or connection access in the scoped API.
- In-memory fallback when a provider cannot render or execute an accepted plan.
- MySQL.

Many-to-many analytics remains expressible through the explicit join model as
ordinary model analytics, for example grouping `PostTag`; that does not create
an implicit `Post -> Tag` high-level traversal. The scoped builder may explicitly
join a declared to-many edge, where ordinary SQL row multiplication is visible
in the authored query. Neither fact is advertised as generalized relation
aggregation.

## 3. Deliberate TypeScript compatibility decisions

P6 preserves the useful TypeScript outcomes:

- read policy is conjoined before aggregation;
- count, sum, average, minimum, maximum, local grouping, `having`, ordering, and
  paging are available;
- a conditional field can be discharged by the selecting constraint;
- BigInt, Decimal, time, and null values do not pass through `float64` transport;
- GraphQL refuses an omitted-`take` result only when the complete matching group
  count exceeds its cap, and explicit `take` never silently clamps;
- programmatic grouping is not capped by GraphQL `maxGroups`;
- forward-to-one relation dimensions use inner semantics for missing/invisible
  targets; and
- the scoped escape hatch remains policy-scoped and read-only.

P6 deliberately improves or clarifies these TypeScript implementation details:

1. Local and relation analytics are planned as one SQL statement. There is no
   two-phase relation fetch and Go merge.
2. Integer and Decimal sum/average have a provider-neutral result contract;
   SQLite never converts an exact Decimal to `REAL`.
3. String grouping, min/max, and deterministic key ordering use portable binary
   semantics (`BINARY` on SQLite and `C` on PostgreSQL), independent of the
   database's default collation. Ordinary text filters keep their P2 semantics.
4. Programmatic grouping has its own explicit high ceiling instead of inheriting
   a public GraphQL cap or being unbounded.
5. GraphQL selections determine returned measures; clients do not repeat the
   same measure in both an input object and the output selection.
6. The scoped Go API contains no Kysely-shaped raw/execution escape and accepts
   only generated descriptors and sealed nodes.

## 4. One analytical architecture

The caller path is:

```text
generated typed request or GraphQL selection
  -> freeze and clone values
  -> bind stable model/field/relation identities
  -> combine caller read policy with request where
  -> bind relation-hop policies when present
  -> classify every value-influencing position
  -> prove conditional-field discharge against its contribution scope
  -> normalize immutable AnalyticsIR or ScopedIR
  -> apply contribution/group/result resource guards
  -> render one parameterized SQLite/PostgreSQL statement
  -> execute on the caller connection or existing sqlx.Tx
  -> refuse overflow/limit sentinels before publishing cells
  -> decode exact typed result cells
  -> GraphQL transport encoding when applicable
```

System operations use the same binder, closed IR, limits, SQL renderer, provider
functions, and decoder. They omit caller policy and field authorization. Tx
variants execute on the already-bound `sqlx.Tx`; they never leave the
transaction or open a second connection.

No generated GraphQL resolver renders SQL or authorizes a field. It lowers
GraphQL values/selections into the same public/frozen operation consumed by the
generated Go client.

### 4.1 Package ownership

```text
golem                         sealed public analytical/scoped values and results
internal/compiler/ir          normalized ContractIR analytics declarations
internal/analytics/ir         closed provider-neutral aggregate/group request
internal/analytics/bind       stable identity, type, and request binding
internal/analytics/plan       scopes, authorization, contribution and limits
internal/analytics/sql        deterministic SQLite/PostgreSQL rendering
internal/analytics/decode     exact aggregate/group decoding
internal/scoped/ir            closed read-only join/query representation
internal/scoped/bind          root/join/expression ownership validation
internal/scoped/plan          policy/classification and limit planning
internal/scoped/sql           deterministic provider SQL rendering
internal/graphql/analytics    SDL, selection/input lowering, result encoding
runtime                       execution, transaction binding, audit publication
```

These packages consume the P1 registry, P2 policy/classification services, P3
execution binding, and P5 GraphQL host. They do not parse tags, rediscover
relations, accept physical strings, or maintain another model registry.

## 5. ContractIR and generation

P6 bumps ContractIR format version from 2 to 3 and GraphQL ABI version from 1
to 2. It adds normalized, canonical-sorted metadata for:

- enabled GraphQL analytics operations and roots;
- local dimension and measure allowlists;
- named relation dimensions with ordered relation IDs and terminal field ID;
- GraphQL group and relation-intermediate limits;
- scoped-root enablement; and
- the analytical result type for every accepted field/operator pair.

These are contract facts only. Changing an analytics allowlist, root name,
limit, relation-dimension name, or scoped-root setting changes the ContractIR
fingerprint and generated Go/GraphQL artifacts. It must not change ModelIR,
physical schema, migration SQL, or the model fingerprint.

The compiler statically interprets `golem.Analytics` and `golem.ScopedReads`.
It validates model ownership, logical types, exposure, unique dimension names,
one-path relation shape, depth, enabled GraphQL roots, limits, and collisions.
No declaration function executes application code.

Local programmatic analytics is generated for every readable stored scalar/enum
with an accepted capability. GraphQL allowlists may narrow that surface.
Relation dimensions exist only when declared. `Scope()` exists only for models
explicitly enabled as scoped roots.

## 6. Closed analytical IR

An immutable analytical request records at least:

- operation kind: count, aggregate, local group, or relation group;
- root model ID and caller/system stance;
- normalized root predicate and bound parameters;
- ordered dimension identities and output ordinals;
- ordered measures with operator, field ID when applicable, logical input type,
  result type, nullability, and output ordinal;
- `having` as a closed tree over bound dimensions/measures;
- ordered result terms and canonical tie-break terms;
- signed take, skip, and effective programmatic/GraphQL limits;
- relation path IDs, hop model IDs, terminal field IDs, and inner semantics;
- row policy and field-classification proof inventory for every model scope;
- contribution, intermediate-group, and final-group guards;
- provider requirements and deterministic alias/bind inventory; and
- a stable request fingerprint that excludes values but includes shape.

Zero values, duplicate dimensions/measures, duplicate output identities,
ungrouped selected keys, `having` or ordering references to absent capabilities,
foreign-model handles, paths not declared for the root, and forged nodes are
rejected before SQL.

Generated handles and core constructors are sealed. Application code cannot
construct an IR node, field ID, relation ID, SQL alias, or result cell directly.

## 7. Authorization and contribution semantics

### 7.1 Local operations

The contribution set is:

```text
root read-policy predicate AND request where predicate
```

`Count` and aggregate `count(*)` classify no scalar merely to count a row.
`count(field)` classifies the field because its null distribution is observable.
Every sum/avg/min/max field, dimension, `having` expression, order expression,
and GraphQL-selected key is classified.

Classification occurs after the combined predicate is normalized. A conditional
field is accepted only if P2 proves the combined contribution scope implies its
read condition for every contributing row. Failure to prove is refusal; P6 does
not sample rows or mask an aggregate.

### 7.2 Forward-to-one relation grouping

The contribution unit remains one authorized root row. The path is a declared
ordered chain of forward to-one relations. Every hop is an inner join whose
target scope is independently constrained by that target model's read policy.
If a relation is absent or the target policy makes it invisible, the root row
does not contribute. These two cases are intentionally indistinguishable.

Local dimensions and root-model measures may accompany relation dimensions.
All relation dimensions in one request must use the same configured path.
Measures from the terminal or intermediate model are absent. Duplicate physical
matches for a logically to-one edge are schema corruption and fail execution;
they are not double-counted.

The relation occurrence, join key positions, terminal dimension, hop-policy
dependencies, `having`, and order positions are classified. Conditional
terminal access is proven against the target contribution scope. Equality facts
may be transferred across declared key mappings; any implication that cannot be
proved by P2 is refused conservatively.

### 7.3 Scoped reads

Every caller root and join gets its own model read policy. Every selected,
filtered, grouped, aggregated, ordered, and explicit join position is
classified. A left-joined target policy is placed in `ON`, not in the outer
`WHERE`, so an invisible target is indistinguishable from an absent target.

Joining a to-many relation explicitly multiplies rows according to ordinary SQL
semantics. The query author chose that contribution shape; P6 does not reinterpret
or deduplicate it. The high-level relation-group operation remains limited to
forward to-one paths.

## 8. SQL and provider semantics

### 8.1 Statement form

Each operation renders one statement built only from descriptor-owned physical
identifiers and bound values. A conceptual grouped statement is:

```sql
WITH authorized_contributions AS MATERIALIZED (
  SELECT ...
  FROM root
  [INNER JOIN authorized_target ...]
  WHERE caller_policy AND request_where
  LIMIT :max_contributions_plus_one
), grouped AS MATERIALIZED (
  SELECT dimensions, measures, count(*) AS contribution_count
  FROM authorized_contributions
  GROUP BY dimensions
  LIMIT :max_intermediate_groups_plus_one
)
SELECT ..., overflow_sentinels
FROM grouped
WHERE normalized_having
ORDER BY requested_terms, canonical_key_ties
LIMIT :effective_result_limit_plus_one OFFSET :skip
```

The renderer uses provider-valid nested subqueries where SQLite cannot express
the PostgreSQL CTE form exactly. Limit sentinels are returned privately and are
checked before any public result. A bounded prefix may be computed internally
only when an overflow sentinel causes the entire operation to fail; it is never
returned as a partial answer.

Explicit `take` is intentional pagination, not silent truncation. Missing
`take` uses the applicable limit plus one and refuses if another result exists.
Intermediate and contribution overflows always refuse, even when a final `take`
would have hidden them.

### 8.2 Exact result rules

- SQLite Decimal columns are scaled integers. P6 registers and probes versioned
  exact SQL aggregates for arbitrary-precision integer sum and fixed-scale
  Decimal sum/average. Native SQLite `REAL` is never used for Decimal.
- PostgreSQL uses `sum(bigint)::text` and `numeric` arithmetic with an explicit
  result scale/rounding contract matching SQLite.
- Decimal average rounds half away from zero to the declared field scale.
  Decimal sum operates at that scale. Canonical transport may remove trailing
  fractional zeros. Values exceeding the public exact-result envelope return a
  stable overflow error; they are never coerced.
- Integer sum is decoded as immutable arbitrary-precision `ExactInteger`.
- Public exact aggregate numbers are bounded to 128 significant decimal digits
  and Decimal scale 18. The contribution limit makes valid P1 sums far smaller;
  the envelope primarily bounds corrupted/provider input and exact-number
  parsing. Exceeding it is a stable overflow, never truncation.
- Integer average and floating aggregates are finite `float64`. Fixtures use
  exactly representable inputs for provider equality; NaN and infinities are
  rejected by the existing scalar boundary.
- Date, Time, DateTime, UUID, enum, Boolean, and string grouping use their P1
  canonical encodings. String grouping/min/max/tie ordering is binary.
- SQL `NULL` keys form one group. Empty/all-null sum, average, minimum, and
  maximum decode as `ReadNull`; count and field-count decode as zero.

Provider capability startup probes prove the installed SQLite aggregate
function versions and the required PostgreSQL numeric/collation behavior. A
missing capability fails startup/use with a stable capability error; there is no
fallback renderer or Go row evaluation.

### 8.3 Determinism

Stable IDs determine aliases, output ordinals, bind order, and implicit ties.
Requested order terms retain author order. The complete grouped key in declared
dimension order is appended as a deterministic tie-breaker. With no explicit
order, the grouped key ascending is used. Null placement is provider-neutral and
explicit. Repeated and shuffled compilation produces byte-identical SQL and
artifacts.

## 9. Limits

P6 adds normalized `AnalyticsLimits` to runtime configuration. Zero values use
safe defaults; applications may lower them and may raise only to hard maxima.

| Limit | Programmatic default | Hard maximum |
| --- | ---: | ---: |
| selected measures | 64 | 256 |
| local dimensions | 16 | 64 |
| relation path depth | 4 | 8 |
| contribution rows | 1,000,000 | 10,000,000 |
| intermediate groups | 250,000 | 1,000,000 |
| returned programmatic groups | 100,000 | 1,000,000 |
| scoped joins | 16 | 64 |
| scoped selected expressions | 128 | 512 |
| scoped predicate nodes | 2,048 | 8,192 |
| SQL parameters | provider/P2 stricter limit | provider/P2 stricter limit |

The default programmatic group limit deliberately admits the TypeScript Eros
34,424-group acceptance corpus. It is not derived from GraphQL configuration.

GraphQL has a per-model generated `maxGroups` default of 100 and a hard maximum
of 10,000. The server runtime may lower it. An explicit positive or negative
`take` whose absolute value exceeds that limit is refused before SQL. When
`take` is absent, the engine requests `maxGroups + 1` and refuses only if the
complete post-`having`, post-`skip` result exceeds the cap. It never returns the
first `maxGroups` as if complete.

## 10. GraphQL integration

P6 extends the P5 ContractIR and generated gqlgen schema. Analytics operations
are opt-in and remain absent merely because conventional root names exist.
The roots are `aggregatePosts`, `groupByPosts`, and, when configured,
`relationGroupByPosts`, subject to exact ContractIR overrides.

GraphQL selection determines returned measures. `having` and order may require
private measures that are computed and classified but not serialized. The
selection compiler rejects a key field absent from `by`, duplicate/empty `by`,
unsupported measure/type pairs, hidden fields, unconfigured relation dimensions,
and every limit overflow before engine work where possible.

GraphQL and generated Go calls freeze to the same AnalyticsIR and must produce
the same policy trace, SQL shape, decoded cells, errors, and ordering. P5's
principal isolation, sanitized error presenter, request bounds, and active
gqlgen adapter remain authoritative.

## 11. Scoped builder security model

The scoped builder is a sealed query AST, not a callback around `sqlx` and not a
SQL string builder. A generated root scope has an opaque query identity. Join
scopes can only be derived from that root or another joined scope through a
generated relation handle. Every field/expression is bound to exactly one scope.
Mixing roots, reusing a scope in another query, omitting a referenced join, or
forging a zero node fails before SQL.

The accepted v1 nodes are:

- one generated root;
- declared inner or left relation joins;
- scalar/enum field references;
- the closed P2 scalar predicate operators over scoped fields;
- count, field-count, sum, average, minimum, and maximum;
- select, where, group-by, having, order-by, signed take, and skip.

There is no public node for raw SQL, physical name, arbitrary alias, custom
`ON`, write, DDL, connection, statement execution, subquery, CTE, union, window,
or unsupported expression. Attempts to reach internals through reflection/zero
values are also runtime-rejected.

Scoped roots are explicitly enabled in ContractIR. An enabled application must
provide `AuditPrincipal(P) string` and `ReportScopedQuery(context.Context,
golem.ScopedAuditRecord)` at startup. The record contains stable model/relation/
field identities, join types, selected expression kinds, caller/system stance,
the application-supplied principal audit ID, execution ID, query-shape
fingerprint, provider, SQL fingerprint, duration, row count, and outcome. It
contains no bound values, raw SQL, driver error, or private actor fields.

## 12. Work waves and parallelization

### P6-A — contracts, IR version, and compiler declarations

- Implement the public declaration shells and exact result type matrix.
- Add ContractIR v3/GraphQL ABI v2 analytics facts and canonical validation.
- Add compile-pass/fail fixtures for allowlists, paths, operations, roots,
  limits, scoped enablement, collisions, and migration independence.
- Update generated artifact compatibility diagnostics.

This is the serial foundation. No later lane merges before P6-A is integrated.

### P6-B — typed Go analytics ABI and frozen IR

- Implement sealed dimensions, measures, results, having/order nodes, and
  generated local/relation handles.
- Extend all four generated client families.
- Implement clone/freeze/thaw, zero/foreign/duplicate rejection, and fresh
  external-module compile fixtures.

### P6-C — authorized count and local aggregate SQL

- Reuse P2/P3 scopes and classification.
- Implement count, field-count, sum, average, minimum, and maximum planning.
- Implement SQLite exact functions, PostgreSQL arithmetic, capability probes,
  overflow sentinels, and exact decoding.
- Route P3 Count through/prove it against the shared scope without changing its
  public signature.

### P6-D — local group-by

- Implement dimensions, null groups, `having`, requested and canonical order,
  signed take, skip, contribution/intermediate/final limits, and transaction
  parity.
- Add provider agreement and large-group tests.

P6-B and provider-function groundwork in P6-C may run in parallel after P6-A,
but the authorization/SQL integration remains owned by one integrator. P6-D
depends on both.

### P6-E — forward-to-one relation grouping

- Bind configured paths and terminal dimensions.
- Apply policy/classification at every hop and inner-path semantics.
- Render one joined SQL statement and prove one-root-row contribution.
- Reject multiple paths, to-many/reverse paths, and related measures by name.

### P6-F — generated GraphQL analytics

- Generate the frozen SDL types/roots and active gqlgen bindings.
- Lower selection, `by`, `having`, order, signed take, skip, and limits into the
  same runtime operation.
- Prove Go/GraphQL parity and sanitized errors.

### P6-G — typed scoped builder

- Implement scope/join/expression ownership, sealed IR, classification, SQL,
  decoding, Tx parity, and audit publication.
- Add compile-fail and structural red-team tests proving writes/raw/forged roots
  are unavailable or rejected.

P6-F and P6-G may run in parallel after P6-D. P6-F relation roots also require
P6-E. They must not both rewrite shared compiler/codegen files without a named
file owner and integration order.

### P6-H — independent provider and cross-entry-point oracle

- Run the complete social/metrics corpus on SQLite, PostgreSQL C, and PostgreSQL
  linguistic profiles.
- Compare generated Go, GraphQL, direct independent SQL fixtures, Caller/System,
  and transaction paths.
- Prove conditional discharge, relation invisibility, nulls, exact types,
  string collation, ordering, and limits.

### P6-I — adversarial, scale, race, and final audit

- Run every named mutation in `P6-EVIDENCE.md`.
- Run race, repeat, shuffle, deterministic generation/SQL, fuzzed binder input,
  cancellation, capability loss, and resource-bound tests.
- Record exact completion commands and provider evidence. Only then change the
  three P6 documents from planned/pending to complete/pass.

## 13. Integration discipline

Parallel agents receive one bounded wave or test lane and may not redefine this
plan. Shared public ABI, compiler IR, generator registry, and runtime entry files
have one named owner at a time. Each lane must report changed files, exact tests,
known exclusions, and any proposed contract deviation. The root integrator:

1. reviews every diff against the public ABI and Bible;
2. rejects in-memory aggregation, raw identifiers, alternate policy paths, or
   provider skips;
3. integrates in dependency order;
4. runs the narrower gate after each merge; and
5. runs the complete P6 evidence matrix before claiming completion.

No agent may mark an evidence row pass from code inspection, a mock-only test,
or another agent's statement.

## 14. Completion claim

When P6 is complete, the honest product claim is:

> Golem generates typed policy-scoped local analytics, bounded configured
> forward-to-one relation grouping, equivalent GraphQL analytics, and an
> auditable typed read-only join builder. SQLite and PostgreSQL execute the
> aggregate work in SQL with exact declared result semantics and visible limits.

It is not:

> Golem supports arbitrary OLAP, raw SQL, or general relation aggregation.

Those claims require later separately planned extensions and evidence.
