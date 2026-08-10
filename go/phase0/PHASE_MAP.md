# Go implementation phase map

## Purpose

This map assigns the classified TypeScript product surface to bounded Go build
phases. It prevents “implement Golem” from becoming one untestable task and
prevents later phases from silently dropping a capability.

The phases are dependency ordered. A later phase may add tests or adapters to an
earlier contract, but it may not redefine the earlier semantics without changing
the contract and its oracle fixtures explicitly.

## Phase summary

| Phase | Deliverable | Definition of done |
|---|---|---|
| P0 | Semantic constitution | TypeScript surface classified; Go authoring shape tested; rule priority resolved against the TypeScript oracle; model, runtime, GraphQL, mutation, event, and provider boundaries written; every parity area owned by a later phase |
| P1 | Model compiler and migration contract | Go structs/tags compile into a validated model IR and generated descriptors; SQLite and PostgreSQL migration artifacts are deterministic; a migrated database matches the IR |
| P2 | Portable policy engine | Complete supported condition table validates, evaluates in memory, and compiles to parameterized SQLite/PostgreSQL SQL with live agreement tests |
| P3 | Authorized read runtime and generated client | System and context-bound clients perform typed find-one/find-first/find-many/count reads; policy is applied before pagination; selections, relation traversal, masking, limits, and stable errors are proven on both providers |
| P4 | Mutation and transaction kernel | Create/update/delete, batches, nested writes, upsert, field diffs, hooks, no-existence leaks, rollback, and commit-aware event records have provider conformance tests |
| P5 | Generated GraphQL API | Model outputs, filters, selectors, CRUD inputs/roots, nullability, custom/computed fields, limits, and error mapping are generated from the same IR and execute only through P3/P4 |
| P6 | Aggregation and scoped reads | Count/aggregate/group-by/relation-group-by and the read-only scoped escape hatch preserve policy and field eligibility on both providers |
| P7 | Events, subscriptions, outbox, and CDC boundary | Commit-derived per-row events, durable outbox delivery, GraphQL subscriptions, bounded fan-out, fresh authorization, codecs, and the explicit external-write/CDC boundary are complete |
| P8 | Hardening and release | Cross-entry-point conformance, load/failure tests, observability, compatibility documentation, migration guides, and release automation meet the production bar |

## Dependency order

```text
P0
 └─ P1 model IR + migrations
     └─ P2 policy evaluator + SQL compiler
         └─ P3 authorized reads
             ├─ P4 mutations + hooks + transactions
             │   ├─ P5 GraphQL mutations
             │   └─ P7 events/outbox/subscriptions
             ├─ P5 GraphQL queries
             └─ P6 aggregation + scoped reads
P5 + P7 ── GraphQL subscription fields
all phases ── P8 hardening/release
```

## P0 — semantic constitution

P0 owns decisions and executable semantic examples, not production persistence.

### In scope

- TypeScript product classification and limitation reconciliation.
- `sqlx` as the database execution boundary.
- SQLite and PostgreSQL as equal supported providers.
- Typed model-specific predicate authoring.
- Four authorization actions and ordered model/field rule semantics.
- A small in-memory evaluator used only as a design oracle.
- The model declaration/generation contract.
- Cross-entry-point runtime, GraphQL, mutation, hook, event, and error invariants.
- Assignment of every remaining capability to P1–P8.

### Not a P0 implementation claim

- No SQL compiler, driver, migrated database, CRUD repository, GraphQL server,
  mutation engine, aggregate, event bus, outbox, or CDC implementation.
- Handwritten fixtures are not generated production models.
- Provider capability declarations are not live provider agreement.

## P1 — model compiler and migrations

### TypeScript surface owned

- All model/field/enum metadata in classification section 2.
- Logical and physical names, Go/database types, nullability, lists, defaults,
  updated fields, IDs, compound primary keys, unique indexes, normal indexes,
  and relation key mappings.
- Model exclusion and per-model/per-field configuration metadata.
- The generator outcomes from `packages/generator`; Prisma DMMF itself is not
  ported.

### Deliverables

- Static Go source parser using Go syntax/type information; no runtime model
  reflection as the schema authority.
- A versioned, provider-neutral model IR.
- Generated typed field/relation handles, identity/select/input descriptors,
  scanners, and model registry.
- Deterministic SQLite/PostgreSQL DDL planning and immutable migration files.
- Validation for unsupported types, ambiguous relations, invalid compound keys,
  conflicting exposure modes, and unsafe provider-specific constructs.
- Schema fingerprint embedded into generated code and migration metadata so a
  stale generator or mismatched database fails clearly.

### Exclusions

- No authorization SQL and no CRUD engine; those start in P2/P3.
- No runtime auto-migration in application startup.
- Storm source code is not imported. Its AST/tag/code-generation approach is a
  useful reference, while its PostgreSQL-only ORM and tag grammar are not the
  Golem architecture.

## P2 — portable policy engine

### TypeScript surface owned

- The full validated condition/operator inventory in classification section 3.
- Ordered rule-chain derivation for rows and fields.
- In-memory and SQL semantic agreement.
- Recursive relation condition planning and dependency discovery.
- Unsupported-condition startup/use failures.
- SQLite/PostgreSQL compiled policy SQL; MySQL remains rejected.

### Deliverables

- One operator registry specifying operand type, null behavior, in-memory
  evaluator, SQLite renderer, and PostgreSQL renderer.
- Complete scalar equality/membership/comparison/text behavior, `AND`/`OR`/`NOT`,
  `is`/`isNot`/`some`/`every`/`none`, exact time/numeric behavior, and explicit
  list capability handling.
- Parameter-only values and descriptor-only identifiers; no caller-provided SQL
  identifiers.
- Property/golden tests that execute every supported condition against the Go
  evaluator, SQLite, and PostgreSQL and compare the same rows.
- Capability errors when the selected provider/model storage cannot support an
  operator, especially scalar lists.

## P3 — authorized reads and generated client

### TypeScript surface owned

- System versus context-bound client stance.
- `findUnique`, `findFirst`, `findMany`, and numeric `count` read paths.
- Request filters, ordering, cursor/skip/take/distinct where supported, selected
  fields, relation selections, and relation counts.
- Policy-before-pagination, relation-hop policy, field classification,
  dependency hydration, masking, aggregate-independent read limits, depth limits,
  and context isolation.
- Read-side `NOT_FOUND`, `BAD_USER_INPUT`, `UNAUTHENTICATED`, and `FORBIDDEN`.

### Deliverables

- Generated model-specific client methods and typed filter/order/select inputs.
- An explicit unrestricted `System` client; caller code never becomes system
  merely because actor resolution failed.
- A context-bound client that owns all authorization and read hooks.
- One selection/read tree used by programmatic and future GraphQL entry points.
- SQLite/PostgreSQL tests proving authorization occurs in SQL before ordering and
  pagination, and that policy-only columns/relations never leak into results.

## P4 — mutations, hooks, and transactions

### TypeScript surface owned

- Create, update, update-many, delete, delete-many, and upsert.
- Nested create/connect/connect-or-create/update/upsert/disconnect/delete and the
  wider programmatic nested action map when explicitly supported.
- Persisted-result create verification, before/after update verification, named
  field diffs, no-op writes, and no-existence leaks.
- Interactive closure transactions and rollback on any denial.
- Before/after hooks shared by programmatic and GraphQL callers.
- Commit-derived event records consumed by P7.

### Deliverables

- One mutation planner that enumerates every touched model, action, identity,
  field, and relation before/after requirement.
- Provider transactions through `sqlx.Tx`; no mutation falls back to an
  unrestricted connection.
- Read/verify/write or write/verify flows selected from the action semantics,
  always within one transaction.
- Bounded conflict retry rules and explicit hook retry semantics.
- Context-aware upsert whose committed branch is truthful. External interference
  may produce a stable conflict; it may not cause an unauthorized fallback branch
  or a false event.
- Model-attached generated hook binding independent of a web framework. External side effects belong
  in an explicit after-commit hook, not a retryable transaction hook.

## P5 — generated GraphQL

### TypeScript surface owned

- All classification section 5 output types, roots, inputs, exposure modes,
  scalar mappings, selectors, limits, computed/batched fields, and custom
  operations.
- Stable GraphQL error extensions.
- Authorized scalar/enum output nullability.
- Query/mutation selection projection into P3/P4.

### Deliverables

- Deterministic SDL or schema artifacts generated from the P1 IR.
- Resolver bindings that only translate GraphQL values into generated P3/P4
  requests; resolvers contain no duplicate authorization or persistence logic.
- CRUD naming and nullability compatibility fixtures derived from the TypeScript
  schema tests.
- Compound identity/unique inputs and recursive nested inputs.
- Request-scoped batching for computed fields.
- Startup collision and invalid-type validation.

### Version-one boundary

- Match the current TypeScript nested-write vocabulary first.
- Add relation/scalar-list public filters and additional nested operations only
  after P2/P4 semantics and complexity limits exist; they are not exposed merely
  because the internal AST can represent them.
- Subscriptions are wired when P7 exists; P5 reserves their generated types and
  naming contract.

## P6 — aggregation and scoped reads

### TypeScript surface owned

- Policy-scoped count, aggregate, local group-by, and relation group-by.
- Field eligibility and exact BigInt/Decimal/null result handling.
- GraphQL group caps versus trusted programmatic behavior.
- One-path forward to-one relation aggregation compatibility.
- Read-only scoped joins/query escape hatch replacing Kysely.

### Deliverables

- Provider agreement tests for every aggregate scalar/result type.
- Bounded intermediate/final relation-group execution with no silent truncation.
- A Go-native typed read builder whose roots and joins automatically receive
  policy constraints and whose output fields are authorized.
- Structural rejection of insert/update/delete/raw execution through the scoped
  builder.
- Generalized to-many/multi-path analytics remains a separately accepted
  extension, not hidden inside P6 completion.

## P7 — events, subscriptions, outbox, and CDC boundary

### TypeScript surface owned

- Created/updated/deleted events, scalar/composite identity, pre-delete snapshot,
  exact codec, batch envelopes, per-row top-level batch events, and commit
  buffering.
- Per-model GraphQL subscriptions, selected entity, fresh authorization,
  filtering, bounded consumer queues, overflow, fan-out grouping, and observer
  metrics.

### Deliverables

- Transactional outbox rows written by P4 in the same transaction as data.
- An idempotent publisher with explicit at-least-once delivery and event IDs;
  subscriber consumers cannot assume exactly once.
- Versioned codec for all supported identity/scalar values.
- Bounded subscription hub with cancellation and backpressure.
- Fresh actor/policy resolution per delivered event and no cross-caller result
  sharing.
- Optional CDC adapter boundary for external writers. Without an installed CDC
  adapter, external writes are explicitly unobservable; the system never implies
  otherwise.

## P8 — hardening and release

### Surface owned

- Cross-entry-point behavioral equivalence: generated GraphQL and context-bound
  programmatic calls must traverse the same runtime.
- Request isolation, red-team scoped SQL tests, concurrency, overflow, recovery,
  observability, documentation, upgrade migrations, and release artifacts.
- Optional render functionality may become a separate module after core release.
- Queue remains excluded.

## Classification ownership ledger

This ledger maps every capability group in `TS_SURFACE_CLASSIFICATION.md` to an
owning phase. Cross-cutting rows name the first phase that must implement the
behavior; later entry points reuse it.

| Classification area | Owner | Notes |
|---|---|---|
| Section 1 core package semantics | P1–P7 | Split by the specific capability below |
| Section 1 policy semantics | P2 | Full evaluator/compiler agreement |
| Section 1 authorizer outcomes | P2/P3 | Generated model-attached Go policies replace CASL/Nest discovery; principal resolution remains application-scoped |
| Section 1 generator outcomes | P1 | P3–P7 extend the same generator templates/IR |
| Section 1 Nest integration outcomes | P3/P5/P7 | Go composition, GraphQL host, and event host replace Nest |
| Section 1 queue | excluded | No Go phase |
| Section 1 render | optional after P8 | Separate module, not a core release gate |
| Section 2 field/model/enum/key/index/relation metadata | P1 | Includes physical names and provider type metadata |
| Section 2 model exclusion and configuration | P1/P5 | P1 validates/stores; P5 applies GraphQL exposure |
| Section 2 limits and verification switches | P3/P5/P6 | Enforced by the operation that consumes each limit |
| Section 2 upsert stripes | P4 | Mechanism may change; P4 owns the concurrency outcome |
| Section 2 GraphQL scalar boundary | P1/P5 | P1 scalar registry, P5 transport representation |
| Section 3 actions, ordered rules, operator validation | P0/P2 | P0 oracle; P2 complete implementation |
| Section 3 row constraints and relation-hop policy | P2/P3 | P2 plans SQL; P3 executes reads |
| Section 3 create/update/delete verification and no-existence leaks | P4 | One mutation transaction kernel |
| Section 3 field classification/dependencies/masking | P2/P3 | P2 derives; P3 hydrates and masks |
| Section 3 nested authorization and denied-event rollback | P4 | P7 only publishes committed outbox records |
| Section 3 fresh subscription authorization | P7 | Uses P2/P3 policy primitives |
| Section 3 aggregate eligibility | P6 | Uses P2 field classification |
| Section 3 authenticated-only surface | P3 | Shared principal boundary used by P5/P7 |
| Section 4 system/caller stance and read operations | P3 | Generated client foundation |
| Section 4 mutation operations and transactions | P4 | Includes selection-aware returned entities |
| Section 4 aggregates/relation group-by | P6 | Generated client gains methods in P6 |
| Section 4 typed projection machinery | P1/P3 | Generated descriptors then read API |
| Section 4 raw/scoped boundary | P6 | System SQL remains explicit outside caller client |
| Section 5.1 GraphQL query/mutation roots | P5 | Execute through P3/P4 |
| Section 5.1 GraphQL aggregate roots | P5/P6 | Types reserved in P5, execution arrives with P6 |
| Section 5.1 GraphQL subscription root | P5/P7 | Type/naming in P5, execution in P7 |
| Section 5.2 outputs, filters, ordering, selectors, selection | P5 | P3 supplies read tree and masking |
| Section 5.2 excluded-target diagnostic improvement | P1/P5 | Fail or warn explicitly; never silently drift |
| Section 5.3 nested GraphQL mutation inputs | P5 | Only operations proven by P4 are emitted |
| Section 5.4 exposure matrix | P1/P5 | P1 validates metadata, P5 materializes schema |
| Section 5.5 computed/batched/custom operations | P5 | Framework discovery replaced by Go registration |
| Section 6 hooks | P4 | P5 calls the same engine; no GraphQL-only hook path |
| Section 6 aggregate-hook limitation | P6 | Version one keeps aggregates hook-free |
| Section 7 count/aggregate/group/relation aggregate | P6 | Count-only read optimization may begin in P3 |
| Section 7 scoped reads and dialects | P6 | P2 supplies policy SQL fragments |
| Section 7 MySQL | rejected | No phase until separately approved |
| Section 8 mutation event facts and batch events | P4/P7 | P4 writes facts/outbox; P7 publishes |
| Section 8 codec, hub, queues, evaluation, observers | P7 | Includes composite identities and overflow |
| Section 8 CDC absence | P7 | Optional adapter with explicit disabled behavior |
| Section 9 error taxonomy | P3 | P4/P6/P7 add category mappings, P5 maps GraphQL |
| Section 9 take/depth/config/policy validation | P1/P2/P3 | Fail at the earliest owning boundary |
| Section 9 request isolation | P3/P8 | Built in P3, adversarially tested in P8 |
| Section 10 resolved historical limitations | owning phases above | They are current baseline, not deferred old defects |
| Section 11 retained/fix decisions | P4, P5, P6, P7 | Each limitation is named in the corresponding phase boundary |

No `CORE PARITY` group is left without an owner. A phase's issue plan must expand
its ledger rows into acceptance tests before implementation begins.

## Cross-phase invariants

Every phase must preserve these rules:

1. Authorization is fail-closed and actor resolution failure never grants system
   access.
2. Values are bound parameters and physical identifiers come only from validated
   generated descriptors.
3. SQLite and PostgreSQL are tested implementations, not names in a capability
   map.
4. GraphQL and programmatic caller operations use one engine.
5. Policy restrictions happen before pagination, grouping limits, or mutation.
6. A policy-invisible write target is indistinguishable from a missing target.
7. Denied or rolled-back writes produce no committed event record.
8. Conditional field masking cannot null-propagate away an otherwise visible
   GraphQL object.
9. Generated artifacts are deterministic and carry a source-schema fingerprint.
10. Unsupported semantics fail during generation/startup where possible, never
    through a silent approximation.
