# P3 authorized reads and generated client plan

Status: **complete — the executable evidence gate in
[`P3-EVIDENCE.md`](./P3-EVIDENCE.md) passes**

Authority: [`../BIBLE.md`](../BIBLE.md), especially sections 8–11, 17–20,
and 21. [`../04-statement-shape.md`](../04-statement-shape.md) remains detailed
supporting research after the conflict resolutions below. P1 owns schema facts
and generated identities. P2 owns policy resolution, classification, exact
evaluation, and provider predicate fragments. P3 consumes those contracts and
does not reimplement them.

## 1. Outcome

P3 turns a typed read request into an authorized, executed, exactly decoded
object graph. It provides explicit generated caller and system clients for:

- `FindUnique`;
- `FindFirst`;
- `FindMany`; and
- numeric `Count` plus selected to-many relation counts.

The request vocabulary includes typed filters, unique selectors, ordering,
cursor, skip, signed take/reversal, distinct, scalar projection, nested relation
projection, and per-to-many relation arguments. General aggregate and group-by
operations remain P6.

P3 is not complete when request types or SQL builders merely exist. Completion
requires live SQLite and PostgreSQL agreement for the same authorized decoded
graphs, plans, errors, and isolation behavior.

## 2. Fixed public ABI decisions

The complete surface is specified in [`PUBLIC-READ-ABI.md`](./PUBLIC-READ-ABI.md).
The central decisions are:

1. Generated model namespaces construct typed immutable read requests. Caller
   code never supplies a model, field, relation, key, table, column, alias, or
   SQL identifier as a string.
2. Results are opaque `golem.Row[M]` values. Typed top-level accessors accept
   generated scalar or relation handles and return a three-state `ReadValue`:
   unselected, selected-null/masked, or selected-present. Ordinary model structs
   cannot represent those states without lying with zero values.
3. The same request tree is the future P5 GraphQL execution input. GraphQL may
   translate into it but never owns another read planner.
4. Read hooks (`FindOne`, `FindFirst`, `FindMany`) activate in P3. Mutation hook
   execution remains P4. This resolves the Phase 0 map/Bible ambiguity by
   assigning hooks to the phase that owns their operation while retaining the
   already generated P1 aliases.
5. `System` is an explicit generated capability. Missing or invalid principal
   resolution never becomes system access.

## 3. Internal read pipeline

One immutable normalized `ReadIR` owns the complete operation:

```text
typed request
  -> validate identities, shape, and limits
  -> build fresh execution policy set
  -> resolve root and every-hop read constraints
  -> classify every value-influencing field
  -> construct public projection plus private dependencies
  -> build deterministic provider-neutral ReadPlan
  -> render complete provider statements
  -> execute on the execution-bound sqlx connection
  -> exact decode and attach bounded relations
  -> apply deferred output masks
  -> strip private values
  -> return immutable typed rows
```

The caller filter and row constraint are always conjoined before ordering,
distinct, cursor, skip, or take. Row policy is never applied by post-filtering a
page in Go.

## 4. Field security

Every field in `where`, unique selectors, ordering, cursor, or distinct is a
read. `never` is forbidden. `conditional` is forbidden unless P2 proves that the
selecting constraint implies the field condition. Projection uses the separate
field lens: `always` is projected, `conditional` is masked per row, and `never`
is forbidden.

Private scalar and relation dependencies required by a conditional mask are
injected into the physical projection, retained until the mask has been
evaluated, and removed before the public row is built. Missing dependency data
fails closed. Mask deferral is output processing only and may never defer a row
constraint.

There is one planned-column construction function and one child-plan
construction function shared by every loading strategy. An index change must
not be able to add or remove masking.

## 5. Relations and bounded execution

Every related selection and related filter applies the target model's read
policy. Relation filtering uses correlated `EXISTS`/`NOT EXISTS`; it does not use
a cardinality-changing join.

The planner may choose a correlated or batched relation plan using P1 index
metadata, correlation shape, provider capability, limits, and fixed cost rules.
Both plans must return the same authorized ordered graph.

The older supporting document's unbounded “fetch every child, then slice per
parent” path is rejected. A batched per-parent page uses provider-supported
windowing or another bounded plan. When no bounded provider plan exists, planning
refuses with a stable limit/unsupported error. Batches chunk parameters below the
provider ceiling and never apply one global child limit to all parents.

## 6. Exact decoding

Root and row-shaped batch columns retain native driver values. At a JSON
aggregation boundary, BigInt and Decimal are encoded as text and reconstructed
exactly. Decoding preserves declared logical type, SQL null, selected-null,
unselected, empty non-nil to-many relations, composite identities, timestamps,
bytes, enums, lists, and JSON.

Provider return types never determine logical type. The plan carries the decode
kind for every output. This is the P3 owner of the M22 projection/decoder
obligation left open by P2.

## 7. Errors, limits, and isolation

Stable categories are `BAD_USER_INPUT`, `NOT_FOUND`, `UNAUTHENTICATED`, and
`FORBIDDEN`. Public errors name logical model/field/operation data only. They do
not expose SQL, driver text, policy internals, physical identifiers, or whether a
policy-invisible target exists.

The public `runtime.ReadLimits` value is copied and normalized when an
application opens. `MaxTake` and `MaxRelationFanout` deliberately use zero to
mean unconfigured/unlimited; Golem does not invent a default row cap. The
zero-value safety defaults are relation depth 5, 256 selected/private fields,
999 statement parameters, 1 MiB of statement text, 2,048 aliases, and 90,000
loader keys. Applications may lower, but not raise, the provider-neutral hard
ceilings. Negative values and unsafe raised ceilings fail during `Open`.

A configured root or to-many fan-out cap is not a truncation instruction. When
the caller did not provide an explicit `take`, the planner fetches `cap + 1` and
execution returns `BAD_USER_INPUT` if the sentinel row exists. Batched relations
check this per parent bucket; correlated relations check each decoded parent
payload. An explicit `take` at or below the cap remains an intentional page.
Schema `MaxTake` is combined with the runtime value and the stricter non-zero
cap wins.

Caller executions own their actor policy set, decision memoization, loaders,
connection identity, and decoded data. Application-wide state contains immutable
metadata and templates only.

## 8. Work waves

### P3-A — contract and immutable request ABI

Freeze public rows, selection/request builders, exact selector values, read
errors, limits, execution inputs, read-hook payloads, and closed internal ReadIR.

### P3-B — generated read surface

Emit model namespace request helpers and application-level caller/system/model
clients. Add compile-pass and compile-fail fixtures for cross-model fields,
invalid to-one arguments, forged identities, and unsupported methods.

### P3-C — validation and authorization planning

Validate every field position, merge caller and policy constraints, classify
projection fields, hydrate dependencies, enforce depth/shape limits, and produce
deterministic provider-neutral plans.

### P3-D — statement rendering

Implement root reads, selectors, ordering, cursors, skip/take/reversal, distinct,
relation counts, correlated relations, and bounded batched relations for SQLite
and PostgreSQL. Reuse P2 policy fragments and the same descriptor/capability
proofs.

### P3-E — execution and decoding

Execute through sqlx, decode exact roots and JSON relations, attach batches,
apply SQL and deferred masks, remove private dependencies, and build immutable
typed rows.

### P3-F — generated runtime integration

Implement explicit application `System` and `ForPrincipal`, execution-owned
loaders, read hook invocation, startup fingerprint/capability validation, and
deterministic inspect inventory.

### P3-G — acceptance and audit

Run an independent reference read implementation against live SQLite and both
PostgreSQL collation profiles. Prove policy-before-pagination, policy at every
hop, exact output types/states, loading-strategy agreement, mask/withhold behavior,
limits, stable errors, concurrent principal isolation, deterministic SQL/binds,
and every P3-owned named mutation from documents 03–05.

Implementation waves P3-A through P3-F are integrated. P3-G is a factual gate,
not a prose status: only rows marked `PASS` in `P3-EVIDENCE.md` count, and P3 may
not be declared complete while any required row is `PENDING` or `FAIL`.

## 9. Definition of done

P3 is complete only when all of the following are evidenced. The authoritative
test/provider mapping and current state for each item is
[`P3-EVIDENCE.md`](./P3-EVIDENCE.md):

1. all four generated read operations execute on caller and system clients;
2. every caller row restriction is in SQL before ordering and pagination;
3. every selected or filtered relation applies its target policy;
4. every value-influencing field position is classified before provider work;
5. output fields are projected, masked, or refused exactly by their field lens;
6. private dependencies never appear in the public row;
7. correlated and bounded batched plans agree on rows, types, order, and shape;
8. SQLite and PostgreSQL decoding preserve every declared logical type exactly;
9. request limits, errors, missing/invisible equality, and system separation pass;
10. concurrent callers have no policy, loader, statement, or result leakage;
11. generated code and statement text/bind order are deterministic; and
12. the complete live oracle, race, mutation, vet, format, and CI gates pass.

P3 does not claim mutations/transactions (P4), GraphQL (P5), full analytics or
scoped SQL (P6), events/subscriptions/CDC (P7), or final hardening (P8).
