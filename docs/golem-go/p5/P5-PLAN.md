# P5 generated GraphQL plan

Status: **complete — all gates in P5-EVIDENCE.md passed locally on 2026-08-06**

Authority: [`../BIBLE.md`](../BIBLE.md), especially sections 2, 3, 5, 10–12,
14, 18–21, and 23. P1 owns models, ContractIR, stable identities, generated
artifacts, and fingerprints. P2 owns the condition language, policy resolution,
classification, and exact scalar semantics. P3 owns authorized reads,
projections, masking, exact decoding, and caller/system separation. P4 owns
mutations, nested writes, hooks, transactions, invalidation, and durable facts.
P5 translates GraphQL into those engines. It does not create another data or
authorization engine.

The public schema and Go integration are frozen in
[`PUBLIC-GRAPHQL-ABI.md`](./PUBLIC-GRAPHQL-ABI.md). The executable completion
ledger is [`P5-EVIDENCE.md`](./P5-EVIDENCE.md). P5 is not complete while any
required ledger row is `PENDING`, `FAIL`, skipped on a required live provider,
or supported only by prose.

## 1. Definition of down

P5 is done when a freshly generated social-network application exposes its
ordinary authorized backend through GraphQL without handwritten CRUD resolvers:

- deterministic model, enum, filter, selector, ordering, cursor, distinct,
  pagination, create, update, nested-write, query, and mutation schema;
- one request-to-operation compiler that lowers GraphQL into the existing P3/P4
  request values using generated identities rather than public strings;
- identical authorization, selected data, masks, errors, committed writes,
  hooks, invalidation, and durable facts through GraphQL and the generated Go
  caller;
- exact scalar transport for every P1 logical type accepted by GraphQL;
- model-attached computed and batched-computed fields plus application custom
  query/mutation operations;
- bounded parsing, validation, projection, input, list, page, batch, and
  execution complexity;
- stable sanitized GraphQL errors;
- live SQLite and both required PostgreSQL profiles, race, repeat,
  determinism, compile-pass/fail, and independent cross-caller oracles; and
- every gate and named mutation in `P5-EVIDENCE.md` passing.

At that point the normal social API is generated from models, policies, hooks,
and extension declarations. P6 analytics and P7 subscriptions remain later
phases and are not required to call P5 complete.

## 2. Starting point and mandatory integration gaps

P5 starts from complete P1–P4 implementations, not from a blank GraphQL server.
The reusable assets already exist:

- ModelIR and ContractIR with stable model/field/relation/key identities;
- model and field GraphQL names separated from physical SQL names;
- exact logical scalar metadata and enum declarations;
- generated model namespaces, selectors, read and mutation inputs;
- P3 `FindUnique`, `FindFirst`, `FindMany`, `Count`, projections, nested relation
  arguments, relation counts, masking, limits, and stable errors;
- P4 CRUD, all eleven nested operations, transactions, hooks, facts, and
  invalidation; and
- an application registry carrying immutable schema and contract fingerprints.

Four gaps must be closed before SDL generation is truthful:

1. **ContractIR is incomplete for transport.** It needs materialized root names,
   a validated operation allowlist, enum-value GraphQL names, public GraphQL
   limits, and extension declarations. These are ContractIR-only facts and MUST
   NOT alter ModelIR, provider schemas, migration plans, or model fingerprints.
2. **GraphQL naming is not fully normalized.** Field defaults currently preserve
   Go spelling and roots would require runtime pluralization. P5 materializes
   legal lower-camel field names and every root name during compilation, checks
   all collisions, and fingerprints the result.
3. **P3 rows key a selected relation only by field identity.** GraphQL aliases
   may select the same relation or relation count more than once with different
   arguments. P5 adds internal occurrence IDs to the shared projection plan and
   result carrier. The typed P3 public API keeps rejecting duplicate selections;
   GraphQL does not get a second read planner.
4. **Computed/custom declarations and bindings do not exist.** P5 adds statically
   interpreted declarations and generated typed bindings. Runtime reflection and
   string type lookup are not schema authorities.
5. **P4 create input cannot currently carry explicit null.** GraphQL input
   coercion distinguishes omitted from explicit null, and that distinction is
   observable for a nullable column with a default. P5-E adds one narrow shared
   P4 extension: a generated create-null value legal only for nullable writable
   fields. It uses the existing P4 binder, authorization, transaction, persisted
   result verification, hooks, and facts; GraphQL does not simulate the result.

## 3. Scope and explicit exclusions

### 3.1 In scope

- A versioned ContractIR evolution for complete GraphQL metadata.
- Deterministic SDL and Go execution artifacts produced by `golem generate`.
- A pinned `github.com/99designs/gqlgen` parser/executor/code-generation
  dependency behind Golem-owned generated code.
- Generated model and enum outputs, field exposure, selectors, conditions,
  ordering, cursors, distinct, pages, relation arguments, and relation counts.
- Generated create/update/update-many inputs and the complete P4 nested-write
  vocabulary.
- Generated find-one/find-many and six mutation roots controlled by the model
  operation allowlist.
- Conditional nullability for every output occurrence P3 can mask.
- Exact scalar parsing and serialization.
- Selection-set lowering across aliases, fragments, inline fragments,
  directives, and repeated compatible fields.
- Model-attached computed fields, execution-scoped batched computed fields, and
  application custom queries/mutations.
- A standard-library `http.Handler`, an immutable SDL accessor, request principal
  extraction, request/execution isolation, complexity limits, and error mapping.
- Cross-caller and provider agreement against the existing P3/P4 engines.

### 3.2 Not in P5

- Aggregate, group-by, having, aggregate ordering, relation analytics, or the
  policy-scoped read builder; P6 owns them. P3 numeric count remains available to
  Go callers, but P5 does not add a public count root ahead of P6.
- Event publication, GraphQL subscriptions, websocket/SSE event transport,
  subscriber fan-out, outbox workers, or CDC; P7 owns them. P5 reserves and
  collision-checks those names but emits no subscription root.
- Top-level `CreateMany`; P4 does not expose it.
- File-upload/multipart semantics, federation, schema stitching, live queries,
  persisted-query storage, or arbitrary executable directives.
- A GraphQL system/unrestricted stance. Generated GraphQL is caller-only.
- Raw SQL, physical names, `*sqlx.DB`, or `*sqlx.Tx` in any resolver contract.
- MySQL.

## 4. Compatibility decisions

### 4.1 Preserved TypeScript behavior

P5 preserves the useful observable TypeScript contract:

- lower-camel singular and plural query roots plus `createX`, `updateX`,
  `upsertX`, `deleteX`, `updateManyXs`, and `deleteManyXs` mutations;
- model outputs, enum outputs, scalar filters, unique and compound selectors,
  ordering, create/update inputs, `BatchPayload`, and recursive nested inputs;
- the normal/immutable/read-only/write-only/hidden exposure matrix;
- find-one as a nullable GraphQL result and find-many as a non-null list;
- conditional field masking represented as GraphQL null;
- BigInt and Decimal serialized exactly rather than as JSON numbers;
- computed fields with declared dependencies, request batching, custom roots,
  and stable `extensions.code`; and
- generated operation allowlists and startup collision failures.

### 4.2 Deliberate Go improvements

P5 does not reproduce known TypeScript transport gaps:

1. Relation filters and portable scalar-list/JSON filters are emitted when the
   P2 registry and both providers accept them. The classifier still visits every
   field at every depth.
2. `cursor` and `distinct` are exposed on root and legal to-many relation reads
   because P3 already owns and proves their semantics.
3. All eleven P4 nested operations are expressible when cardinality, direction,
   exposure, and input availability permit them. They are never forwarded as an
   opaque provider payload.
4. Numeric update operations use a one-operation input envelope, preserving
   omission versus explicit null and mapping exactly to P4 `Set`, `Null`,
   `Increment`, and `Decrement`. Nullable create values preserve omitted versus
   explicit null through the narrow P4 create-null extension described above.
5. Excluded or unexposed relation targets cause a generation diagnostic rather
   than silent relation pruning.
6. Aliased repeated relations retain independent arguments and results.
7. GraphQL cache invalidation remains in P4, so writes made by custom operations
   or Go callers invalidate the same execution loaders.
8. GraphQL has a finite explicit default page size even when the trusted Go
   caller has no cap. The SDL default makes this pagination behavior visible.
9. Exact scalar codecs are Golem-owned and provider-neutral; gqlgen defaults do
   not choose logical types or precision.

`findFirst` and a standalone count root are not emitted in P5. They remain typed
P3 operations, and P6 owns the final public count/analytics naming so the schema
does not acquire two incompatible count surfaces.

## 5. One GraphQL architecture

```text
HTTP request or direct GraphQL execution
  -> bounded body decode and GraphQL parse/validation
  -> resolve exactly one principal and create one caller execution
  -> coerce generated exact scalars and presence-aware inputs
  -> normalize operation, aliases, fragments, directives, and response paths
  -> bind public names to generated ContractIR identities
  -> lower query selection to the shared P3 request/projection tree
     OR lower mutation input and selection to the shared P4 request tree
  -> execute P3/P4 through the same caller, policy set, hooks, tx, and limits
  -> resolve computed fields from masked public dependencies
  -> serialize exact values and map sanitized stable errors
```

Generated root resolvers only translate, call the existing engine, and encode.
They may not query the database, resolve policy, run hooks, publish facts, clear
loaders, or implement mutation transactions themselves.

One GraphQL operation owns one caller execution. Multiple top-level query fields
share that execution's immutable policy set and loaders. Each generated
top-level mutation is one P4 transaction; GraphQL's serial mutation-field order
does not make several top-level mutation fields one atomic transaction. A custom
mutation may explicitly use the P4 closure transaction API.

### 5.1 Package boundaries

P5 adds packages with closed responsibilities:

```text
internal/graphql/contract    ContractIR normalization, names, collisions
internal/graphql/schema      provider-neutral GraphQL schema model and SDL
internal/graphql/bind        coerced inputs -> P2/P3/P4 typed values
internal/graphql/select      operation AST -> occurrence-aware P3 projections
internal/graphql/scalar      exact parse/serialize codecs
internal/graphql/extension   computed/custom declarations and binding metadata
internal/graphql/codegen     deterministic gqlgen config and generated adapters
graphql                      public handler, limits, server and error contracts
runtime                      execution-scoped caller/computed-loader integration
```

Only `internal/graphql/schema` reads ContractIR names. Runtime binding resolves a
name once to a stable ID and passes IDs thereafter. No GraphQL package reads Go
tags, database catalogs, or physical schema documents.

## 6. ContractIR and naming evolution

P5 increments the ContractIR format and adds normalized transport facts:

- exact model output name and singular/plural root base names;
- materialized root names for every supported operation, including reserved P6
  and P7 names for collision detection;
- a closed enabled-operation set;
- exact field GraphQL names and exposure modes;
- enum GraphQL name plus an ordered wire-value-to-GraphQL-value map;
- ordered selector names and components;
- public page/complexity overrides;
- computed-field and custom-operation declarations; and
- schema/extension ABI version.

ModelIR remains byte-identical when only these facts change. Tests must prove a
GraphQL rename changes the contract fingerprint and generated GraphQL artifacts,
but not the model fingerprint, physical fingerprints, DDL, or migration diff.

Default field names use a frozen lower-camel conversion: `ID -> id`,
`URLValue -> urlValue`, `CreatedAt -> createdAt`. Explicit `graphql=` names are
exact after GraphQL-name validation. Root pluralization is a versioned rule table
compatible with the existing TypeScript `pluralize` corpus; every materialized
root can be explicitly overridden for irregular/domain names. Runtime code never
pluralizes.

Generation rejects duplicate type, enum value, field, argument, selector,
computed field, generated root, custom root, and reserved future root names. It
also rejects GraphQL introspection prefixes, illegal names, empty input objects,
unexposed selector components, unreachable relation targets, and unknown or
provider-ineligible operations.

## 7. Generated artifacts and gqlgen boundary

`golem generate` is the only command application authors run. It emits and
manifests, at minimum:

- canonical `zz_golem_graphql.schema.graphqls`;
- Golem-owned output/input/scalar adapter types;
- Golem-owned root and field resolver bindings;
- a pinned gqlgen executable-schema artifact generated in-process with a fixed
  configuration; and
- the SDL fingerprint and ContractIR fingerprint in the application registry.

Applications do not maintain `gqlgen.yml`, invoke gqlgen separately, or edit a
resolver stub. The pinned gqlgen version and Golem GraphQL ABI version participate
in generation identity. Updating gqlgen is a deliberate compatibility change
with golden, compile, execution, and determinism gates.

Generation happens in memory before publication, compiles the prospective
module, and publishes atomically through the existing manifest pipeline. A
failure leaves the last complete generation untouched. Shuffled model/package/
extension declaration order must produce byte-identical artifacts.

The generated application exposes an opaque Golem GraphQL server and a standard
`http.Handler`; gqlgen implementation interfaces are not required in ordinary
application code.

## 8. Selection and projection lowering

The selection compiler consumes the validated operation AST, not raw query text.
It:

1. expands named and inline fragments with cycle protection;
2. applies `@skip` and `@include` using already-coerced variables;
3. groups fields by response name using GraphQL field-merging rules;
4. binds schema field names to stable model/field/relation identities;
5. assigns a deterministic occurrence ID to each response-path selection;
6. folds identical repeated occurrences while retaining different aliases or
   arguments as independent slots;
7. lowers to-one and to-many selections, relation counts, and their legal
   arguments into P3 projection nodes;
8. injects computed dependencies without making them response fields;
9. applies GraphQL depth/field/alias/complexity bounds; and
10. hands the complete tree to P3 before any SQL runs.

Occurrence IDs affect only internal result addressing. Authorization and
classification still use stable `FieldID`/`RelationID`; SQL planning, policy
constraints, masking, exact decoding, and loading strategies remain P3. The
typed Go row/request ABI keeps its simpler one-selection-per-field rule.

`__typename` is resolved from generated type metadata and never causes a
database projection. Introspection fields are handled by the GraphQL executor
and cannot introduce model identities or database work.

## 9. Query inputs and roots

P5 emits the accepted P3 condition vocabulary by logical type and provider
capability:

- `AND`, `OR`, and `NOT` trees;
- equality, membership, null, ordered, text, bytes, JSON, scalar-list, and
  relation predicates accepted by P2/P3;
- `is`/`isNot` for to-one and `some`/`every`/`none` for to-many relations;
- one-of primary/named unique selectors with ordered compound components;
- ordered one-field `orderBy` entries;
- selector-backed cursors, `skip`, signed `take`, and scalar `distinct` fields;
- to-many relation arguments matching `FindMany`; and
- relation-count `where` arguments matching P3 Count.

Exposure is recursive. Hidden/write-only fields never appear. A condition or
ordering operator appears only when its exact P2 operator and provider storage
capability pass both-provider acceptance. The binder rejects zero/empty unique
selectors, multiple selector arms, empty condition objects whose semantics would
be ambiguous, duplicate order fields where disallowed, invalid signed paging,
and every forged/unknown key before P3 execution.

The ordinary query roots are find-one and find-many. Root and to-many list fields
carry an SDL-visible default `take`; explicit values may be positive or negative
within the public maximum. The stricter non-zero model, GraphQL, and P3 cap wins.

## 10. Mutation inputs and roots

Generated mutation roots map one-for-one to P4:

- create, update, upsert, delete, update-many, and delete-many;
- create and update result selections compiled through P3;
- delete result selection compiled against the locked pre-image; and
- `BatchPayload { count: Int! }`, safe because P4's public touched-row hard
  maximum is below GraphQL's 32-bit Int ceiling.

Create scalars are direct presence-aware values. For a nullable field, explicit
GraphQL null becomes the P4 create-null operation and omission remains omission.
Update scalars use an exactly-one operation envelope. Nullable fields expose
`setNull: true` as the P4 null operation; omission remains no operation. Numeric
envelopes additionally expose `increment` and `decrement` when P4 and the logical
type allow them. Update-many uses the same scalar operations but no relations.

Relation inputs expose exactly the P4 operations legal for their cardinality,
direction, requiredness, writable FK ownership, exposure, and available child
input:

```text
create  createMany  connect  connectOrCreate  disconnect  set
update  updateMany  upsert   delete           deleteMany
```

Every nested selector/filter is lowered to the same independently authorized P4
node. GraphQL input recursion is bounded before allocation and P4 then enforces
its own nested depth, touched-node, row, byte, parameter, transaction, and retry
limits. No GraphQL resolver replays a transaction closure or publishes an event.

## 11. Output nullability and masking

The schema must describe every state P3 may return:

- a visible scalar/enum is nullable when its model has caller policy capable of
  field masking or the database value is nullable;
- a to-one relation is nullable because absence, related-row policy, or field
  masking may produce null;
- a to-many relation is nullable when the relation field itself can be masked;
  when present, it is a non-null list of non-null authorized rows;
- a relation-count field is nullable when it can be masked;
- computed-field nullability is explicitly declared and its resolver receives
  only masked public dependencies; and
- mutation/query containers are never made non-null in a way that lets a masked
  child null-propagate away an otherwise authorized parent.

Because policy methods are actor-dependent, generation may conservatively mark
all visible outputs on a policy-bound model nullable. It may emit stronger
non-null types only from a static proof recorded in ContractIR; runtime optimism
is forbidden. Masked and database-null values share the public `null` shape, but
trusted tracing may retain the internal reason.

## 12. Exact scalar contract

P5 defines Golem-owned codecs:

| P1 logical kind | GraphQL type | Transport rule |
| --- | --- | --- |
| Boolean | `Boolean` | GraphQL boolean only |
| Int16/Int32 | `Int` | checked signed 32-bit |
| Int64 | `BigInt` | decimal string output; exact string or INT literal input |
| Float32/Float64 | `Float` | finite values only; checked narrowing |
| String | `String` | valid UTF-8 GraphQL string |
| UUID | `UUID` | canonical lowercase hyphenated string |
| Decimal | `Decimal` | canonical exact decimal string; no JSON-number input |
| Date | `Date` | canonical ISO calendar date |
| Time | `Time` | canonical declared-precision local time |
| DateTime | `DateTime` | RFC3339 with declared precision/offset normalization |
| Bytes | `Bytes` | canonical base64 string |
| JSON | `JSON` | bounded JSON value preserving `json.Number` tokens |
| Enum | generated enum | explicit GraphQL-name to wire-value map |
| Scalar list | `[T!]` shape | element codec plus declared field nullability |

ID and composite-selector components retain their declared logical scalar type;
GraphQL `ID` does not silently turn every database key into a string. Parse and
serialize errors are `BAD_USER_INPUT` or sanitized internal errors as appropriate
and never include a driver value dump.

## 13. Computed fields, batching, and custom operations

Computed fields are declared on the model they extend. Their declaration fixes:

- GraphQL name, result type, and nullability;
- a typed argument struct and exact GraphQL argument names;
- required scalar/relation handles;
- a typed resolver binding; and
- optional batch key, cache-key codec, and maximum batch size.

Dependencies are added to the P3 projection only when the computed field is
selected. P3 classifies and masks them before the resolver runs. The resolver
receives a typed public row and can distinguish unselected, null/masked, and
present values; it never receives the private unmasked dependency row.

Batched computed fields are keyed by execution, field identity, canonical
arguments, and typed cache key. Dispatch is executor-driven, not timer-driven.
One batch never crosses operations, callers, or principals. Writes clear the
execution loaders through P4. Cancellation unwinds waiters and loader goroutines.

Application custom queries and mutations are statically declared against
generated scalar, enum, model, predicate, selector, and mutation-input types.
Their typed binding receives the current generated caller, never System, DB, or
Tx. A custom mutation can call `caller.Transaction`; Golem does not infer
transactionality around arbitrary user code. Custom outputs pass through the same
model row serialization and masking rules. Unknown types and every generated,
custom, or reserved-name collision fail generation.

## 14. Errors and disclosure

Engine errors map exactly:

| Internal category | `extensions.code` |
| --- | --- |
| invalid input, unsupported operation, limit, hook veto | `BAD_USER_INPUT` |
| absent or policy-invisible target | `NOT_FOUND` |
| uniqueness, serialization, bounded retry conflict | `CONFLICT` |
| absent/invalid principal | `UNAUTHENTICATED` |
| action/field refusal | `FORBIDDEN` |

GraphQL parse and validation failures use stable transport codes
`GRAPHQL_PARSE_FAILED` and `GRAPHQL_VALIDATION_FAILED`. Unexpected failures use
`INTERNAL_SERVER_ERROR`, report the trusted wrapped error through the configured
server reporter, and expose no SQL, driver text, stack, file path, physical name,
policy predicate, hidden-row fact, or provider detail.

Missing and policy-invisible unique targets retain the same message, code,
GraphQL error path, data/null shape, fact behavior, and number of constrained
engine probes. Aliases change only the GraphQL error path, never the underlying
public message.

## 15. GraphQL resource limits

P5 adds finite public defaults and portable hard ceilings for:

- HTTP body and variable bytes;
- GraphQL tokens, AST nodes, fragment count, and fragment expansion;
- operations per request (exactly one selected operation);
- selection depth, selected occurrences, aliases, and response paths;
- input depth, input nodes, list items, and string/JSON/bytes payload size;
- query complexity with list fan-out multipliers;
- generated default and runtime maximum root/to-many page size;
- computed-field batch size and pending keys; and
- total resolver concurrency.

Limits normalize once when the GraphQL server is constructed and cannot raise
P3/P4 hard ceilings. GraphQL validation refuses before caller engine/database work
where possible. P3/P4 revalidate the lowered request, so bypassing the HTTP
handler or forging generated input values cannot bypass runtime limits.

No overflow is silently truncated. The only automatic page is the explicit SDL
default argument. Batch mutations retain P4's exact bounded identity set and
all-or-nothing refusal.

## 16. Work waves

### P5-A — ContractIR, public ABI, and schema model

Add complete ContractIR transport metadata, normalized naming, enum-value maps,
operation exposure, public limits, extension declarations, collision validation,
and a closed provider-neutral GraphQL schema model. Prove every contract-only
change leaves database artifacts untouched.

### P5-B — deterministic SDL and gqlgen integration

Emit canonical SDL, exact scalar declarations, generated GraphQL Go types, a
pinned gqlgen executable schema, resolver interfaces satisfied entirely by
Golem-generated bindings, manifest entries, fingerprints, atomic publication,
and fresh-module compile/determinism tests.

### P5-C — query input binder

Generate and bind exact scalar filters, logical trees, relation/list/JSON
filters, unique/composite selectors, ordered `orderBy`, cursors, distinct, skip,
signed take, and relation-count predicates into P2/P3 values. Add position spies
and refusal-before-engine tests.

### P5-D — selection compiler and query execution

Normalize aliases/fragments/directives, add occurrence-aware projection slots,
lower every root/nested selection into P3, execute find-one/find-many, serialize
masked exact rows, and prove GraphQL/Go cross-caller equivalence on both
providers.

### P5-E — mutation and nested-input execution

Add the narrow P4 nullable-create operation, then generate presence-aware
create/update/update-many and relation envelopes for all eleven nested
operations. Lower six mutation roots into P4, preserve result projection,
transactions, hooks, invalidation, facts, no-existence leakage, and
provider/concurrency semantics.

### P5-F — server lifecycle, scalars, errors, and limits

Add the public server/handler API, principal extraction, one caller execution per
operation, exact scalar codecs, sanitized error presenter, trusted error reporter,
bounded request/AST/input/complexity/concurrency controls, cancellation, and
direct-execution tests.

### P5-G — computed and batched-computed fields

Implement static model-attached declarations, generated typed bindings,
dependency projection, masked resolver rows, argument codecs, execution-scoped
batch dispatch, cache keys, write invalidation, failures, cancellation, and
cross-principal isolation.

### P5-H — custom operations and complete social application

Implement statically declared custom query/mutation bindings with caller-only
capability, generated type resolution, collision checks, explicit custom
transactions, and the complete User/Friendship/Post/Comment/Tag/PostTag schema
and execution corpus.

### P5-I — independent audit and completion gates

Run a GraphQL reference oracle independent of the generated binders, cross-caller
trace comparison, malformed/fuzzed document and variable corpora, live SQLite and
both PostgreSQL profiles, concurrency/race, repeat/shuffle, deterministic
generation, named mutations, vet, format, and CI-equivalent commands. Only then
may the P5 status change from planned to complete.

## 17. Dependency and parallelization map

```text
P5-A contract/schema model
  -> P5-B SDL/gqlgen generation
  -> P5-C input binder -----------+
  -> P5-D selection/query --------+--> P5-F server/scalars/errors/limits
  -> P5-E mutations --------------+             |
                                                v
P5-A -> P5-G computed/batching -> P5-H custom/social integration
P5-B..H ---------------------------------------> P5-I audit
```

After P5-A freezes the internal schema model, P5-C, P5-D, and P5-E can proceed in
parallel with disjoint packages. P5-G can build its loader and declaration layer
in parallel but integrates only after P5-D's occurrence-aware projection exists.
P5-F owns shared request lifecycle and should integrate the binders, not redefine
them. P5-I is deliberately independent and begins oracle fixtures early, but its
completion run follows every implementation wave.

## 18. Non-deviation rules

During implementation:

1. No resolver may add authorization, SQL, hooks, transactions, invalidation, or
   fact behavior outside P2–P4.
2. No GraphQL name may become a kernel identity; all names bind to generated IDs
   at the boundary.
3. No ContractIR-only change may create a migration.
4. No TypeScript limitation may be copied unless this plan explicitly preserves
   it.
5. No P6 or P7 surface may be emitted early merely because a name or type exists.
6. No provider test may pass by skipping a required live DSN.
7. No evidence row may be marked `PASS` before its named test exists and its full
   gate command passes.
8. Any required semantic change first updates this plan, the public ABI, the
   evidence gate, and the controlling Bible when applicable; implementation does
   not silently drift.
