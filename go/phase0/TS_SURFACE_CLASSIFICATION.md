# TypeScript surface classification

## Purpose

This document defines what “the Go version can express everything the TypeScript
version can express” means. It classifies the current TypeScript repository by
observable product capability, not by npm package or implementation technique.

It is an inventory and parity boundary. It is not a claim that Phase 0 already
implements these capabilities.

## Classification vocabulary

### Product disposition

| Label | Meaning |
|---|---|
| `CORE PARITY` | The observable behavior is part of Golem's backend-automation product and needs an equivalent Go contract. The Go API may look different. |
| `GO REPLACEMENT` | The behavior is needed, but the TypeScript mechanism is tied to Prisma, NestJS, GraphQL.js, Kysely, CASL, or JavaScript and must be replaced by a Go-native mechanism. |
| `OPTIONAL PACKAGE` | Useful repository functionality, but not required to prove the core Golem backend. It gets a separate scope decision. |
| `EXCLUDED` | Explicitly outside the Go Golem project. |
| `LIMITATION DECISION` | Current TypeScript behavior is a known limitation or asymmetry. It must be explicitly preserved, improved, or rejected; it must not be copied accidentally. |

### Current Go Phase 0 coverage

| Label | Meaning |
|---|---|
| `PROVEN` | An executable Phase 0 test proves the stated narrow behavior. |
| `PARTIAL` | Phase 0 has related vocabulary, but not the complete TypeScript semantics. |
| `UNDESIGNED` | No Go contract exists yet. |
| `NOT APPLICABLE` | The item is excluded or intentionally belongs to a separate package. |

## The parity rule

Go parity means that an application author can describe the same backend facts
and obtain the same security-relevant outcomes:

1. describe models, fields, keys, indexes, enums, and relations;
2. configure generated operations and field exposure;
3. state row and field authorization for reads and writes;
4. obtain policy-enforced CRUD, nested writes, transactions, hooks, aggregates,
   GraphQL, and subscriptions without hand-writing repositories or resolvers;
5. receive stable validation and authorization failures; and
6. get equivalent policy results on SQLite and PostgreSQL.

Parity does **not** require copying Prisma's schema parser, Prisma Client's API,
Nest decorators, CASL objects, GraphQL.js types, Kysely builders, JavaScript
`AsyncLocalStorage`, or npm package boundaries.

## 1. Repository and package boundary

| TypeScript area | What it provides | Disposition | Go Phase 0 | Evidence |
|---|---|---|---|---|
| `packages/core` | Operation engine, authorization enforcement, GraphQL schema, hooks, aggregation, scoped reads, events, subscriptions | `CORE PARITY` | `PARTIAL` authorization AST only | `typescript/packages/core/src/index.ts:1-27` |
| `packages/policy` | One validated condition language with an in-memory evaluator and SQLite/PostgreSQL SQL rendering | `CORE PARITY` | `PARTIAL` | `typescript/packages/policy/src/operators.ts:34-100`; `test/sql-agreement.*.test.ts` |
| `packages/authorizer` | CASL/Authorizer integration and exact rule matcher | `GO REPLACEMENT` for integration; authorization outcomes are `CORE PARITY` | `PARTIAL` | `typescript/packages/authorizer/src/index.ts:51-198`; `policy.ts:58-85` |
| `packages/generator` | Extracts Prisma DMMF and emits model metadata, typed client, and hook/result type maps | `GO REPLACEMENT`; generated outcomes are `CORE PARITY` | `UNDESIGNED` | `typescript/packages/generator/src/emit.ts`; `emit-client.ts`; `emit-types.ts` |
| `packages/nest` | Dependency injection, hook/extension discovery, request boundary, Apollo artifacts, event bus | `GO REPLACEMENT` | `UNDESIGNED` | `typescript/packages/nest/src/index.ts`; `decorators.ts`; `graphql-artifacts.ts` |
| `packages/queue` | Durable Nest job queue | `EXCLUDED` from Go Golem by prior project decision | `NOT APPLICABLE` | `typescript/packages/queue/src` |
| `packages/render` | Nest SPA hosting and link-preview rendering | `OPTIONAL PACKAGE` | `NOT APPLICABLE` | `typescript/packages/render/src` |

## 2. Model and configuration language

These are product facts that the Go model declaration or generator must be able
to represent even if Go uses structs, tags, generated descriptors, or a separate
schema file.

| Capability | Disposition | Go Phase 0 | Required Go contract | Evidence |
|---|---|---|---|---|
| Scalar, enum, and object/relation field kinds | `CORE PARITY` | `PARTIAL` handwritten scalar/relation handles only | Typed field and relation metadata | `core/src/datamodel.ts:3-22` |
| Field database name, model table name, and native database type | `CORE PARITY` | `UNDESIGNED` | Preserve logical/physical names and provider types without guessing | `datamodel.ts:5-22,43-50` |
| Required, list, unique, ID, default, read-only, and updated-at properties | `CORE PARITY` | `UNDESIGNED` | Schema generation and mutation requiredness must consume one descriptor | `datamodel.ts:5-22` |
| Single and composite primary keys | `CORE PARITY` | `UNDESIGNED` | Ordered identity values for queries, nested writes, and events | `datamodel.ts:24-27`; `schema.ts:718-780,1241-1271` |
| Named/unnamed compound unique selectors | `CORE PARITY` | `UNDESIGNED` | Stable selector names and typed component values | `datamodel.ts:29-31`; `schema.ts:729-777` |
| Normal, unique, ID, and full-text index metadata | `CORE PARITY` for metadata; full-text query semantics need a later decision | `UNDESIGNED` | Preserve index order and physical names | `datamodel.ts:34-50` |
| Relation name and ordered from/to key fields | `CORE PARITY` | `PARTIAL` typed handles lack physical key metadata | Required for joins, policy traversal, nested writes, and relation aggregation | `datamodel.ts:19-21` |
| Enum names and values | `CORE PARITY` | `UNDESIGNED` | Typed Go values plus GraphQL enum generation | `datamodel.ts:68-76`; `schema.ts:528-535` |
| Datasource provider | `CORE PARITY` | `PARTIAL` SQLite/PostgreSQL capability declaration only | Provider is mandatory when compiling SQL | `datamodel.ts:73-77`; `scoped.ts:243-255` |
| Per-model exclusion | `CORE PARITY` with a `LIMITATION DECISION` about silent relation pruning | `UNDESIGNED` | Explicit model exposure and deterministic relation handling | `datamodel.ts:104-106`; `schema.ts:339-346,651-656` |
| Per-model operation allowlist | `CORE PARITY` | `UNDESIGNED` | Controls generated GraphQL CRUD fields | `datamodel.ts:93-102`; `schema.ts:263-278,876-1239` |
| Per-model subscriptions, aggregation, take, group, and intermediate-group limits | `CORE PARITY` | `UNDESIGNED` | Validated defaults with per-model overrides | `datamodel.ts:85-118`; `schema.ts:269-303` |
| `hidden`, `immutable`, `readOnly`, and `writeOnly` fields | `CORE PARITY` | `UNDESIGNED` | One validated field-surface matrix applied recursively to generated GraphQL inputs | `datamodel.ts:93-102`; `schema.ts:210-261`; `inputs.ts:60-90` |
| Authorization depth and result/read-field verification switches | `CORE PARITY` | `UNDESIGNED` | Secure defaults; explicit opt-out only | `datamodel.ts:108-118`; `schema.ts:433-453` |
| Upsert guard stripe count | `LIMITATION DECISION` | `UNDESIGNED` | Do not expose this exact mechanism until Go mutation atomicity is designed | `datamodel.ts:108-118`; `upsert-guard.ts` |

### Current GraphQL scalar boundary

The generated TypeScript GraphQL schema accepts `String`, `Int`, `Float`,
`Boolean`, `DateTime`, `BigInt`, and `Decimal`. `BigInt` and `Decimal` serialize
as strings. An unknown scalar causes schema construction to fail. This is the
current GraphQL boundary, not proof that other database scalar types should stay
unsupported in Go.

Evidence: `typescript/packages/core/src/schema.ts:90-178,550-555`.

## 3. Authorization semantics

Authorization is a core semantic contract. CASL is not.

| Capability | Disposition | Go Phase 0 | Required parity statement | Evidence |
|---|---|---|---|---|
| Four actions: read, create, update, delete | `CORE PARITY` | `PROVEN` as authoring vocabulary | Every entry point maps to these actions consistently | `core/src/authorization.ts:1-26` |
| Default denial and action-level authorization | `CORE PARITY` | `PARTIAL` | Absence of a grant must fail closed | `authorizer/src/index.ts:78-92` |
| Row constraints merged into reads before order/pagination | `CORE PARITY` | `PROVEN` in memory | The SQL execution path must prove the same ordering | `core/src/authorization.ts:63-70`; `operations.ts:1009-1048` |
| Relation-hop authorization | `CORE PARITY` | `PARTIAL` expression vocabulary only | Every selected or policy-traversed relation is scoped as its target model | `core/src/readtree.ts`; `verify.ts`; authorization tests |
| Update/delete no-existence-leak behavior | `CORE PARITY` | `UNDESIGNED` | A missing row and a policy-invisible row return the same `NOT_FOUND` result | `operations.ts:1136-1205,1273-1295`; root README:186-190 |
| Create result verification | `CORE PARITY` | `UNDESIGNED` | Verify the persisted result, including defaults and nested effects, inside the write transaction | `operations.ts:1078-1133`; `verify.ts` |
| Update before/after verification | `CORE PARITY` | `UNDESIGNED` | Field and row rules evaluate against truthful before/after rows in one transaction | `operations.ts:1136-1205`; `verify.ts` |
| Field classification: always, conditional, never | `CORE PARITY` | `PARTIAL` | Used for early rejection, dependency hydration, masking, and aggregate eligibility | `core/src/authorization.ts:29-38`; `authorizer/src/index.ts:132-193` |
| Conditional read masking | `CORE PARITY` | `PARTIAL` evaluator only | Allowed row remains; denied field becomes null; policy-only dependencies are stripped | root README:190,198; `readfields.test.ts`; `mask-nullability.test.ts` |
| Field-level write checks by actual diff; no-op allowed | `CORE PARITY` | `UNDESIGNED` | A named field denial depends on whether persistence changed it | root README:188-190; `verify.ts` |
| Recursive condition dependency hydration | `CORE PARITY` | `UNDESIGNED` | `AND`, `OR`, `NOT`, and relation operators must not under-select data | `authorizer/src/index.ts:200-272`; `core/src/field-references.ts` |
| Nested write authorization per touched model/action | `CORE PARITY` | `UNDESIGNED` | Relation envelopes cannot bypass target-model policy | `core/src/authorization.ts:49-61`; `nested-writes.ts`; `operations.ts:1083-1085,1145` |
| Policy denials roll back writes and publish no events | `CORE PARITY` | `UNDESIGNED` | Authorization, persistence, and deferred events share a transaction outcome | root README:192-196; `event-buffer.ts`; transaction tests |
| Fresh authorization on subscription events | `CORE PARITY` | `UNDESIGNED` | Revocation affects an existing connection | `schema.ts:1284-1384`; `authorizer/src/transport.ts:15-29` |
| Aggregate field eligibility | `CORE PARITY` | `UNDESIGNED` | Reject a field not readable for every row in the effective aggregate scope | root README:141-143,464 |
| Unsupported policy conditions fail closed at startup/use | `CORE PARITY` | `PARTIAL` unknown operators fail in the Phase 0 evaluator | Validation must cover both SQL and in-memory interpreters | `authorizer/src/policy.ts:20-68` |
| Authenticated-only surface when authorization is configured | `CORE PARITY` | `UNDESIGNED` | Missing actor/context becomes `UNAUTHENTICATED`, not an anonymous system call | root README:197 |

### Rule ordering is resolved by the Phase 0 oracle

The TypeScript adapter consumes CASL's ordered `rulesFor` chain. Field
constraints explicitly account for earlier matching rules and inverted rules
(`authorizer/src/field-constraint.ts:23-40`). Phase 0 originally proposed an
unordered algebra:

```text
OR(all allows) AND NOT OR(all denials)
```

Those contracts are not equivalent. Phase 0 now preserves the ordered outcome in
its Go-native builder: the newest applicable rule has priority, conditional
denials exclude rows from older grants, and the first unconditional rule ends the
reachable chain. Model-wide and field-scoped rules also share the same chain.
This is locked by named Go oracle fixtures plus 259 generated row-rule chains and
259 generated model/field-rule chains evaluated by the TypeScript CASL ability;
the unordered deny-overrides proposal has been removed. The TypeScript digest is
reproducible with `testdata/generate_rule_oracle.mjs`.

### Condition/operator inventory

The TypeScript policy language has one validated operator table shared by its
in-memory matcher and SQL renderer:

- combinators: `AND`, `OR`, `NOT`;
- equality/membership: `equals`, `not`, `in`, `notIn`;
- comparisons: `lt`, `lte`, `gt`, `gte`;
- text: `contains`, `startsWith`, `endsWith`, including the supported
  case-insensitive mode;
- relation quantifiers: `is`, `isNot`, `some`, `every`, `none`;
- scalar-list operators: `has`, `hasEvery`, `hasSome`, `isEmpty`, `equals`;
- explicit null, two-valued, type, `BigInt`, `Decimal`, and date semantics.

Evidence: `typescript/packages/policy/src/operators.ts:34-109` and the operator
definitions in that file; agreement tests under
`typescript/packages/policy/test/`.

Phase 0 currently covers equality, inequality, `in`, boolean combinators, and the
five relation predicates. It does not yet cover the full scalar operator table,
scalar lists, insensitive text, or the TypeScript null/type semantics. SQLite and
PostgreSQL are only declared as required capabilities; Go has no SQL agreement
tests yet.

Scalar-list support is provider-sensitive: the policy engine can render it for
PostgreSQL, while SQLite does not provide Prisma scalar-list model fields. Go
must either define a portable storage representation and prove it on both
providers, or mark scalar-list fields/operators as an explicit PostgreSQL-only
model capability. It may not silently claim cross-provider parity.

## 4. Programmatic runtime surface

| Capability | Disposition | Go Phase 0 | Exact TypeScript boundary | Evidence |
|---|---|---|---|---|
| System/unrestricted client stance | `CORE PARITY` | `PROVEN` only as an in-memory bypass option | Plain generated delegate bypasses policy/hooks but still publishes configured events | root README:129-154 |
| Context-bound caller stance | `CORE PARITY` | `PARTIAL` policy built per actor | `forContext(ctx)` exposes only operations whose policy semantics Golem owns | `generator/src/emit-client.ts:117-179,181-269` |
| `findUnique`, `findFirst`, `findMany` | `CORE PARITY` | `PARTIAL` only `findMany` fixture | Context-scoped filters, ordering, pagination, cursor/distinct, selection/include/omit as supported by each request type | `core/src/operations.ts:89-112,155-167`; `emit-client.ts:117-126` |
| `create`, `update`, `updateMany`, `upsert`, `delete`, `deleteMany` | `CORE PARITY` | `PARTIAL` rule vocabulary only | Policy-enforced writes and batch count results | `operations.ts:114-178`; `emit-client.ts:127-144` |
| Numeric `count` | `CORE PARITY` | `UNDESIGNED` | Field-selected count is intentionally absent from the bound surface | `operations.ts:180-184`; `emit-client.ts:145-147` |
| `aggregate`, local `groupBy`, relation `relationGroupBy` | `CORE PARITY` | `UNDESIGNED` | Typed, policy-scoped analytical operations | `operations.ts:186-217`; `relation-aggregation.ts:42-99`; `emit-client.ts:148-156` |
| Selection narrows result type | `CORE PARITY` outcome; Prisma type machinery is `GO REPLACEMENT` | `UNDESIGNED` | Go needs a comparably safe generated projection/query API, not TypeScript conditional types | `emit-client.ts:108-156` |
| Interactive callback transaction | `CORE PARITY` | `UNDESIGNED` | All bound operations use one transaction; denial rolls back; events publish after commit | `operations.ts:2062-2098`; `emit-client.ts:169-179,223-251` |
| Sequential-array transaction | `LIMITATION DECISION` | `UNDESIGNED` | Not present on `forContext`; Go need not inherit this JavaScript-specific omission | root README:145,463 |
| Raw writes/queries on caller stance | Current omission is security-relevant | `UNDESIGNED` | TypeScript excludes raw calls; Go should only expose a safe scoped-read escape hatch and explicit system DB access | root README:141; `scoped.ts` |

The Go API does not need to imitate Prisma delegate method signatures. It does
need a generated, discoverable route from a model to these operations with no
stringly typed model or field names in normal application code.

## 5. Generated GraphQL surface

GraphQL is a first-class parity area. It is not merely a transport adapter,
because Golem generates its types, nullability, operation set, nested inputs,
limits, selection projection, errors, and subscriptions.

### 5.1 Root operations and names

For model `Article`, the current naming contract produces:

| TypeScript engine operation | Generated GraphQL field | Shape | Disposition | Go Phase 0 |
|---|---|---|---|---|
| `findOne` | `article(where: ArticleWhereUniqueInput!): Article` | nullable result | `CORE PARITY` | `UNDESIGNED` |
| `findMany` | `articles(where, orderBy, take, skip): [Article!]!` | no GraphQL cursor or distinct argument | `CORE PARITY`; argument gap is a `LIMITATION DECISION` | `UNDESIGNED` |
| `create` | `createArticle(data: ArticleCreateInput!): Article!` | selected entity | `CORE PARITY` | `UNDESIGNED` |
| `update` | `updateArticle(where, data): Article!` | policy-invisible target is `NOT_FOUND` | `CORE PARITY` | `UNDESIGNED` |
| `upsert` | `upsertArticle(where, create, update): Article!` | selected branch semantics | `CORE PARITY` | `UNDESIGNED` |
| `delete` | `deleteArticle(where): Article!` | selected deleted entity | `CORE PARITY` | `UNDESIGNED` |
| `updateMany` | `updateManyArticles(where, data): BatchPayload!` | `{ count }` | `CORE PARITY` | `UNDESIGNED` |
| `deleteMany` | `deleteManyArticles(where): BatchPayload!` | `{ count }` | `CORE PARITY` | `UNDESIGNED` |
| aggregation | `articlesAggregate`, `articlesGrouped`, optionally `articlesRelationGrouped` | configured analytical schema | `CORE PARITY` | `UNDESIGNED` |
| subscription | `articleEvents(where): ArticleEvent!` | `CREATED/UPDATED/DELETED`, identity, selected entity | `CORE PARITY` | `UNDESIGNED` |

Evidence: `typescript/packages/core/src/naming.ts:10-65` and
`schema.ts:862-1388`.

There is no generated GraphQL `findFirst` or standalone `count` root field.
Those are programmatic operations. Aggregation is separately configured.

### 5.2 Outputs, filters, selectors, and ordering

| Capability | Current TypeScript behavior | Disposition | Go Phase 0 | Evidence |
|---|---|---|---|---|
| Model output | Visible scalar/enum fields plus surviving relations; to-many relations are non-null lists of non-null targets | `CORE PARITY` | `UNDESIGNED` | `schema.ts:644-677` |
| Relation count | `_count`-style output for visible to-many relations | `CORE PARITY` | `UNDESIGNED` | `schema.ts:620-637,672-677` |
| Authorized scalar nullability | Every visible scalar/enum output becomes nullable when field masking is enabled | `CORE PARITY` security contract | `UNDESIGNED` | `schema.ts:662-669`; `mask-nullability.test.ts` |
| Where combinators | `AND`, `OR`, `NOT` | `CORE PARITY` | `UNDESIGNED` for GraphQL | `schema.ts:696-715` |
| GraphQL where fields | Current generator includes visible, non-list scalar/enum fields only; relation and scalar-list request filters are absent | `LIMITATION DECISION` | `UNDESIGNED` | `schema.ts:707-712` |
| Scalar filters | `equals`, `in`, `notIn`, `not`, plus type-appropriate comparisons/text operators | `CORE PARITY` | `UNDESIGNED` | `schema.ts:574-615` |
| Order by | Visible, non-list scalar/enum fields, ascending/descending | `CORE PARITY` | `UNDESIGNED` | `schema.ts:783-797` |
| Unique selectors | Visible scalar ID/unique fields and named/unnamed compound ID/unique inputs | `CORE PARITY` | `UNDESIGNED` | `schema.ts:718-780`; `composite-schema.test.ts` |
| Selected-field projection | GraphQL selection becomes the database select; required computed/policy dependencies are hydrated then stripped | `CORE PARITY` | `UNDESIGNED` | `schema.ts:884-915`; `select.ts`; `readtree.ts` |
| Excluded relation target | Relation output is silently omitted if its target model is excluded | `LIMITATION DECISION` | `UNDESIGNED` | `schema.ts:651-656`; root README:475 |

The GraphQL request-filter language is therefore smaller than the internal
policy language and smaller than the programmatic Prisma-shaped filter surface.
The Go design must not conflate these three languages.

### 5.3 Generated mutation inputs

The current GraphQL generator supports this exact mutation vocabulary:

| Location | Current operations | Evidence |
|---|---|---|
| Create scalar/enum fields | Direct values; required when the database field is required and has no default/update timestamp | `inputs.ts:106-157` |
| Create to-many relation | `create`, `connect`, `connectOrCreate` | `inputs.ts:160-200` |
| Create to-one relation | `create`, `connect`, `connectOrCreate`; envelope required for a required relation | `inputs.ts:202-213` |
| Update scalar/enum fields | Direct replacement values; immutable/read-only/hidden fields omitted | `inputs.ts:215-263` |
| Update to-many relation | `update`, `upsert`, `connectOrCreate`, `connect`, `disconnect`, `delete` | `inputs.ts:265-334` |
| Update required to-one relation | `update`, `upsert`, `connectOrCreate`, `connect` | `inputs.ts:336-361` |
| Update optional to-one relation | `update`, `upsert`, `connectOrCreate`, `connect`, boolean `disconnect`, boolean `delete` | `inputs.ts:363-389` |
| Top-level update-many data | Scalar/enum fields only | `inputs.ts:392-410` |

The generated GraphQL surface does not currently expose Prisma's complete nested
write vocabulary (`createMany`, nested `updateMany`, nested `deleteMany`, `set`,
or direct nested `create` during update), nor scalar arithmetic envelopes such as
`increment`. The programmatic context-bound client can accept supported Prisma
write shapes and the authorization engine recognizes a broader nested action map.
Whether Go GraphQL should preserve the narrower schema or close these gaps is a
`LIMITATION DECISION`.

### 5.4 Field exposure matrix

| Mode | Output/read | Filter/order/unique | Create input | Update input | Disposition |
|---|---:|---:|---:|---:|---|
| normal | yes | yes | yes | yes | `CORE PARITY` |
| immutable | yes | yes | yes | no | `CORE PARITY` |
| read-only | yes | yes | no | no | `CORE PARITY` |
| write-only | no | no | yes | yes | `CORE PARITY` |
| write-only + immutable | no | no | yes | no | `CORE PARITY` |
| hidden | no | no | no | no | `CORE PARITY` |

Unknown fields, write-only identities/relations, and conflicting access modes
fail schema construction. Evidence: `typescript/packages/core/src/schema.ts:210-261`
and root `README.md:421-434`.

### 5.5 Custom GraphQL behavior

| Capability | Disposition | Go Phase 0 | Evidence |
|---|---|---|---|
| Computed fields with declared source dependencies and arguments | `CORE PARITY` | `UNDESIGNED` | `core/src/extensions.ts:11-19,29-73` |
| Request-scoped batched computed fields with cache key and max batch size | `CORE PARITY` | `UNDESIGNED` | `extensions.ts:4-9,49-64`; `computed-batch.test.ts` |
| Custom queries and mutations with named GraphQL type references | `CORE PARITY` | `UNDESIGNED` | `extensions.ts:21-27`; `schema.ts:1391-1405` |
| Nest decorators and provider discovery for those extensions | `GO REPLACEMENT` | `UNDESIGNED` | `nest/src/decorators.ts`; `nest/src/extensions.ts` |
| Apollo resolver transformation artifacts | `GO REPLACEMENT` | `UNDESIGNED` | `nest/src/graphql-artifacts.ts` |

## 6. Hooks

| Capability | Disposition | Go Phase 0 | Exact behavior | Evidence |
|---|---|---|---|---|
| Before and after hooks per model/operation | `CORE PARITY` | `UNDESIGNED` | Before may transform/veto; after observes and cannot replace | `core/src/hooks.ts:24-65`; root README:200-240 |
| Sequential deterministic execution | `CORE PARITY` | `UNDESIGNED` | Each before receives the prior result; after runs after read processing | root README:227-229 |
| Shared by GraphQL and context-bound programmatic calls | `CORE PARITY` | `UNDESIGNED` | Plain system client bypasses hooks | root README:202,154 |
| Typed request/result model | `CORE PARITY` outcome; TS type generation is `GO REPLACEMENT` | `UNDESIGNED` | Go generator should expose model-specific request/result types | `generator/src/emit-types.ts` |
| Upsert branch hooks | `CORE PARITY` | `UNDESIGNED` | Upsert itself has no hook pair; exactly the chosen create or update pipeline runs | `core/src/hooks.ts:1-22`; root README:242 |
| Nest decorators and DI construction | `GO REPLACEMENT` | `UNDESIGNED` | Go model methods carry policy/hook behavior; generation emits bindings instead of runtime decorator/DI discovery | `nest/src/decorators.ts`; `hooks-explorer.ts`; Eros feature authorizers |

Aggregates currently run no hooks. That is a documented `LIMITATION DECISION`,
not an omission to overlook (`README.md:464`).

## 7. Aggregation and scoped reads

| Capability | Disposition | Go Phase 0 | Required behavior | Evidence |
|---|---|---|---|---|
| Policy-scoped numeric count | `CORE PARITY` | `UNDESIGNED` | Same row constraint as ordinary reads | `operations.ts:1393-1401` |
| Sum, average, min, max, count | `CORE PARITY` | `UNDESIGNED` | Preserve null and exact scalar result semantics | `operations.ts:1402-1446`; `aggregations.ts` |
| Local-column group by with having/order/skip/take | `CORE PARITY` | `UNDESIGNED` | Deterministic grouping and policy field eligibility | `operations.ts:1447-1501`; `groupby.test.ts` |
| Relation dimensions | `CORE PARITY` for the useful outcome | `UNDESIGNED` | Authorized root facts, forward to-one lookup, inner-join invisibility, exact average rebuilding | `relation-aggregation.ts`; root README:286-325 |
| Relation aggregation caps | `CORE PARITY` guardrail | `UNDESIGNED` | Inspect complete intermediate set; never silently truncate | `relation-aggregation.ts:10-35,168-225` |
| Multiple relation paths, to-many traversal, related-model measures | `LIMITATION DECISION` | `UNDESIGNED` | Currently unsupported | `relation-aggregation.ts:128-145,190-196`; root README:474 |
| Read-only policy-scoped query builder | `CORE PARITY` escape hatch; Kysely API is `GO REPLACEMENT` | `UNDESIGNED` | All roots/joins scoped, selected fields checked, mutation/unsafe escape nodes rejected | `scoped.ts:28-116,329-340` and scoped red-team tests |
| SQLite and PostgreSQL SQL dialects | `CORE PARITY` | `PARTIAL` declaration only | Live agreement tests are mandatory | `scoped.ts:209-255`; policy SQL agreement tests |
| MySQL | Not in the current product boundary | `NOT APPLICABLE` | Reject explicitly rather than compile approximate SQL | `scoped.ts:243-255` |

GraphQL `maxGroups` caps the generated local `groupBy`; the programmatic client
is deliberately uncapped. Relation grouping has its own mandatory final and
intermediate caps. These are separate contracts (`README.md:466` and
`relation-aggregation.ts:10-35`).

## 8. Events and subscriptions

| Capability | Disposition | Go Phase 0 | Exact current behavior | Evidence |
|---|---|---|---|---|
| `CREATED`, `UPDATED`, `DELETED` event payloads | `CORE PARITY` | `UNDESIGNED` | Model, scalar/composite identity, pre-delete auth snapshot | `events.ts:3-24` |
| Event bus abstraction | `CORE PARITY` boundary; concrete transport is replaceable | `UNDESIGNED` | Publish, optional publish-many, async model topic iterator | `events.ts:26-34` |
| Transaction buffering | `CORE PARITY` | `UNDESIGNED` | Publish after commit, discard on rollback | `event-buffer.ts`; transaction tests |
| Plain generated system writes also emit | `CORE PARITY` | `UNDESIGNED` | Instrumented generated client owns write interception | `generator/src/emit-client.ts:34-102`; `publisher.ts` |
| Per-row top-level batch events | `CORE PARITY` current behavior | `UNDESIGNED` | Deterministic identities, max 1,000 rows and 1 MiB by default, all-or-nothing | `publisher.ts:39-45,154-238` |
| Composite event identities | `CORE PARITY` | `UNDESIGNED` | Ordered non-null identity object in GraphQL | `events.ts:5-9`; `schema.ts:1241-1271` |
| JSON-like event codec | `CORE PARITY` outcome | `UNDESIGNED` | Versioned exact encoding of BigInt, Decimal, Date, bytes, snapshots, composite IDs, batches | `event-codec.ts`; event codec tests |
| One local hub per schema/model | `CORE PARITY` operational behavior | `UNDESIGNED` | Opens first/last subscriber source by reference count | `subscription-hub.ts:125-175,248-266` |
| Per-consumer bounded queue | `CORE PARITY` guardrail | `UNDESIGNED` | Default 64; overflow disconnects with a stable subscription code; no silent drop | `subscription-hub.ts:125-157,193-245`; root README:452 |
| Per-event authorization/filter/selection re-evaluation | `CORE PARITY` | `UNDESIGNED` | Shared only for exact context object plus canonical evaluation key | `subscription-hub.ts:202-245`; `schema.ts:1284-1384` |
| Subscription observability | `CORE PARITY` operational surface | `UNDESIGNED` | Active count, receipt, evaluation latency, delivery/suppression, depth, overflow | `subscription-hub.ts`; root README:452 |

CDC is not present in TypeScript. Writes from another process or a SQL console
are invisible. Go must decide whether CDC/outbox support is part of a later event
phase; it is not required to claim exact parity with TypeScript, but it is needed
to remove this limitation.

## 9. Errors and guardrails

| Capability | Disposition | Go Phase 0 | Evidence |
|---|---|---|---|
| Stable error categories: `BAD_USER_INPUT`, `NOT_FOUND`, `CONFLICT`, `UNAUTHENTICATED`, `FORBIDDEN` | `CORE PARITY` | `UNDESIGNED` | `core/src/errors.ts:1-45`; `schema.ts:313-325` |
| No raw Prisma internals in GraphQL errors | `CORE PARITY` | `UNDESIGNED` | `operations.ts:279-391`; root README:436-446 |
| Per-model take limit | `CORE PARITY` | `UNDESIGNED` | `datamodel.ts:93-118`; `operations.ts:1012` |
| Query depth limit | `CORE PARITY` | `UNDESIGNED` | `datamodel.ts:108-118`; read-tree tests |
| Schema configuration validation | `CORE PARITY` | `UNDESIGNED` | `schema.ts:210-278` |
| Fail-closed policy validation | `CORE PARITY` | `PARTIAL` | `authorizer/src/policy.ts:58-80`; policy validation tests |
| Request-context isolation | `CORE PARITY` security invariant | `UNDESIGNED` | root README:104; `nest/src/request-boundary.ts` |

The transport spelling of these codes may differ outside GraphQL, but Go needs
one internal typed error taxonomy that maps deterministically to GraphQL.

## 10. Reconciliation with the earlier limitation list

The repository has moved since the earlier list was written. The current source
must be the baseline:

| Earlier issue | Current TypeScript classification | Evidence |
|---|---|---|
| Context-aware upsert is branch-correct but non-atomic | **Partially improved.** Participating Golem upserts using the same canonical model/selector are serialized by a database guard. It remains cooperative: external/plain/differently addressed writes do not participate. | root README:242,465; `upsert-guard.ts`; PostgreSQL upsert guard tests |
| Subscription fan-out performs work per subscriber | **Still present, but grouped.** Work is per distinct context/filter/selection evaluation group, not necessarily per socket. | `subscription-hub.ts:202-245`; root README:462 |
| Composite-primary-key models are forContext-only | **No longer true.** GraphQL compound selectors and composite event identities are generated. | `schema.ts:729-777,1241-1271`; `composite-schema.test.ts` |
| Conditional masked GraphQL fields should be nullable | **Fixed and now a contract.** Visible scalar/enum outputs become nullable when field checks can mask. | `schema.ts:662-669`; `mask-nullability.test.ts`; root README:434 |
| Batch mutations produce no per-row events | **Fixed for top-level batches.** GraphQL, context-bound, plain generated delegates, and generated transactions emit bounded per-row events. Nested and out-of-process batches remain uncaptured. | `publisher.ts:154-238`; root README:456,473 |
| Relation-traversing aggregation is not implemented | **Partially fixed.** One configured common forward to-one path with terminal dimensions is supported. To-many, multiple paths, and related measures remain unsupported. | `relation-aggregation.ts:118-225`; root README:474 |

This table prevents the Go design from preserving limitations that the current
TypeScript code has already removed.

## 11. Current intentional limitations and decisions required

| Current TypeScript limitation/asymmetry | Go decision required before implementation |
|---|---|
| Subscription evaluation remains local and per distinct policy/filter/selection group | Preserve initially, or design a trusted shared evaluation/cache boundary without cross-user leakage |
| Context-bound transaction supports callback form only | Use Go's closure transaction idiom; do not invent an array analogue merely for parity |
| Aggregates are read-only and hook-free | Decide whether hooks have useful, non-ambiguous aggregate request/result types |
| Upsert serialization is cooperative | Design database-native branch atomicity or document the exact participation boundary |
| GraphQL local `maxGroups` does not cap programmatic `groupBy` | Preserve the trusted-programmatic vs public-transport distinction, or introduce a separate programmatic cap |
| BigInt/Decimal use string GraphQL representations | Preserve exactness unless adopting standardized custom scalar contracts with equivalent precision |
| Out-of-process writes are invisible | Add outbox/CDC later if Golem must observe non-Golem writers |
| Actor retrieval runs per request/event | Define request cache and event freshness explicitly |
| Nested/out-of-process batch events are not captured | Decide whether nested mutation planning can emit complete per-row event sets |
| Relation aggregation supports one common forward to-one path only | Generalize in a later analytical phase; do not mix it into the authorization kernel |
| Excluded models silently prune incoming GraphQL relations | Prefer startup diagnostics or explicit exposure configuration in Go |
| GraphQL filters omit relations and scalar lists | Decide whether to close the public-query gap or preserve the narrower attack surface |
| GraphQL nested writes are narrower than the programmatic write surface | Decide operation-by-operation and attach authorization/event semantics before exposing more |

## 12. Honest Phase 0 coverage summary

Phase 0 currently proves only this narrow subset:

- a typed Go expression tree for one model at a time;
- equality, inequality, membership, boolean combinators, and relation predicates;
- four action authoring methods plus field-rule vocabulary;
- request filter intersected with policy before in-memory pagination;
- explicit system bypass and per-actor policy construction;
- deterministic normalization; and
- a declared requirement that future SQL work support SQLite and PostgreSQL.

It does **not** currently prove:

- rule-chain combinations beyond the Phase 0 depth-three row/field oracle sweep;
- the complete operator/null/type matrix;
- model/schema generation;
- SQL compilation or live provider agreement;
- CRUD or nested-write transaction semantics;
- write verification and no-existence-leak behavior;
- field dependency hydration and result masking;
- hooks or extensions;
- aggregation or scoped reads;
- events, subscriptions, outbox, or CDC;
- GraphQL schema generation, execution, nullability, or errors; or
- a production Go public API.

Therefore the correct answer to “can the current Go Phase 0 express everything
TypeScript can?” is **no**. It can express part of the authorization predicate
vocabulary. This classification is the prerequisite for designing the missing
contracts without confusing a prototype with the product.

## 13. Gate for the next design step

Before Phase 0 can be called complete as a product-design phase, the design must:

1. assign every `CORE PARITY` row to a named implementation phase;
2. retain the generated ordered-rule oracle as the rule builder evolves;
3. define the Go model declaration and generation boundary;
4. define the Go GraphQL contract, including the current public-filter and nested-write gaps;
5. define mutation before/after/transaction/event semantics;
6. define which current limitations are preserved in version one and which are fixed; and
7. turn each security-relevant parity statement into fixtures that can run against
   both SQLite and PostgreSQL when SQL exists.

No later phase may claim TypeScript parity merely because it has a similarly
named Go method.
