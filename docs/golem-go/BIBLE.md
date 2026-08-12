# Golem for Go — the Bible

Status: **normative architecture specification**<br>
Audience: maintainers, implementers, reviewers, and application authors<br>
Scope: the Go implementation of Golem, from model declaration through authorized
programmatic and GraphQL execution

This document is the controlling specification for Golem for Go. It merges the
Phase 0 product and authoring design with the detailed operator, policy,
classification, SQL, and runtime specifications in this directory.

It is deliberately a constitution rather than an implementation diary. A change
that contradicts this document is not an implementation detail: it is a product
or security change and MUST amend this document, its acceptance tests, and the
relevant migration notes in the same review.

---

## 0. Authority, vocabulary, and interpretation

### 0.1 Normative language

`MUST`, `MUST NOT`, `SHOULD`, `SHOULD NOT`, and `MAY` are normative. Unqualified
present-tense statements in algorithms and invariants are also normative.

Security rules fail closed. If two plausible interpretations differ in what they
permit, the implementation MUST use the less permissive interpretation until the
Bible is amended.

### 0.2 Authority order

The authority order is:

1. this Bible;
2. executable acceptance and mutation tests required by this Bible;
3. an accepted phase contract such as `p1/P1-CONTRACT.md`, within that phase;
4. the detailed algorithms in `01-operators.md` through
   `05-surface-and-runtime.md`, after applying the resolutions in section 23;
5. provider-specific implementation notes;
6. historical Phase 0 and TypeScript compatibility documents; and
7. the existing TypeScript implementation as evidence, not as infallible truth.

The detailed chapters remain the expanded specification for their subjects:

- `01-operators.md`: condition validation, evaluation, and SQL rendering;
- `02-policy-resolution.md`: ordered row and field rule resolution;
- `03-classification.md`: prevention of field-name information oracles;
- `04-statement-shape.md`: authorized SQL planning and result decoding; and
- `05-surface-and-runtime.md`: operations, request execution, batching,
  subscriptions, hooks, and errors.

If a detailed chapter conflicts with this Bible, this Bible wins. The supporting
chapters retain some measured TypeScript behavior and open questions as design
evidence; their status notices point back here so implementers MUST NOT choose
whichever text is convenient.

### 0.3 Core terms

- **model**: a logical entity declared by an application Go struct and represented
  at runtime by an immutable generated descriptor;
- **field**: a scalar, enum, list, or relation member identified internally by a
  generated `FieldID`, never by an unvalidated caller string;
- **principal**: the authenticated security identity resolved for an execution;
- **actor**: the application-specific typed value derived from a principal and
  passed to model policy definitions;
- **policy**: the fresh, immutable ordered rules produced for one principal;
- **condition**: a typed, provider-neutral, two-valued predicate tree;
- **row constraint**: the condition selecting rows for which an action is granted;
- **field condition**: the condition selecting rows on which a field is available
  for an action;
- **classification**: `always`, `conditional`, or `never` readability of a field
  within a concrete selecting constraint;
- **execution**: one request, transaction callback, subscription event, worker
  operation, or other isolated unit with a unique execution identity;
- **caller client**: an operation client bound to a principal and policy;
- **system client**: an explicitly acquired unrestricted capability;
- **model IR**: the versioned generated intermediate representation shared by
  every subsystem;
- **provider**: a complete SQLite or PostgreSQL dialect implementation;
- **transport**: GraphQL or another external protocol translating into the same
  operation engine used by the programmatic caller client.

---

## 1. Product promise

Golem is a model-driven backend engine. An application author defines:

1. Go models, keys, indexes, defaults, and relations;
2. model-attached authorization rules;
3. optional model-attached hooks and computed fields; and
4. application composition such as database, principal resolution, and event
   adapters.

From those declarations Golem supplies:

- typed programmatic CRUD and transaction clients;
- generated GraphQL types, inputs, queries, mutations, and subscriptions;
- policy-scoped reads, writes, relations, aggregates, and computed fields;
- SQLite and PostgreSQL SQL planning and execution through `sqlx`;
- deterministic migrations and schema verification;
- mutation hooks, exact field-diff authorization, and truthful upsert behavior;
- per-row commit-derived events, transactional outbox delivery, and subscriptions;
- stable typed errors and GraphQL error mapping; and
- security and provider conformance tests for generated applications.

Application authors SHOULD write custom code for product behavior. They SHOULD
NOT have to reproduce resolvers, repositories, DTOs, authorization queries,
transaction plumbing, or event envelopes for ordinary model operations.

Queue is not part of the Go core product. Render MAY be a later optional module.
Golem does not build on GORM, Ent, Storm, or another ORM. Storm is useful only as
a reference for static Go parsing and generation.

---

## 2. Non-negotiable invariants

Every phase and entry point MUST preserve all of these invariants.

1. Authorization is deny-by-default and fail-closed.
2. A missing or invalid caller scope never becomes system access.
3. Caller policy is applied in SQL before ordering, cursors, distinct, skip,
   take, grouping limits, or mutation target selection.
4. A request filter may narrow policy reach but never widen it.
5. Every relation hop is authorized against the related model.
6. A policy-invisible write target is indistinguishable from a missing target.
7. A condition has identical boolean meaning in the Go evaluator, SQLite, and
   PostgreSQL for every supported operator and value kind.
8. Conditions are two-valued: SQL `TRUE` means true; SQL `FALSE` and `NULL` both
   mean false. Authorization never relies on classical rewrites that are invalid
   under SQL three-valued logic.
9. Values are bound parameters. Physical identifiers come only from generated,
   validated descriptors.
10. Model and field identities in the kernel are typed IDs, not public strings.
11. Every field named in a value-influencing position is classified before SQL
    execution. Projection masking alone is not sufficient.
12. Conditional field discharge is semantic:
    `Implies(selectingConstraint, fieldCondition)`, never a syntactic proxy.
13. Conditional GraphQL outputs are nullable even when the database column is
    non-null. Masking a field does not null-propagate its containing object.
14. GraphQL and the caller client translate into one operation tree and one
    engine. Neither has a privileged authorization implementation.
15. Actor resolution and policy construction are execution-scoped. Cached policy
    decisions never cross an execution boundary.
16. Rolled-back or denied mutations emit no committed event.
17. Successful top-level batch mutations emit one event fact per affected row.
18. Database uniqueness and transaction isolation are the final concurrency
    authorities. In-process locks are optimizations only.
19. SQLite and PostgreSQL are equal required providers. A feature is portable
    only after live agreement tests pass on both.
20. Unsupported semantics fail during generation or planning where possible;
    they are never silently approximated or delegated to an unspecified fallback.
21. Generated artifacts are deterministic and carry a schema fingerprint.
22. Arbitrary out-of-process writes are invisible unless CDC is configured; no
    API or documentation may imply otherwise.

---

## 3. System architecture

```text
application Go structs + tags
             │
             ▼
static model compiler ──────────────── policy/hook method validation
             │
             ▼
versioned model IR + schema fingerprint
       │             │              │
       ▼             ▼              ▼
typed descriptors  migrations   generated bindings
       └─────────────┬───────────────┘
                     ▼
         policy + classification kernel
                     ▼
            operation/mutation planner
                     ▼
        SQLite/PostgreSQL SQL compiler
                     ▼
               sqlx/database/sql
                     │
        ┌────────────┴────────────┐
        ▼                         ▼
programmatic caller API     generated GraphQL
        │                         │
        └──────── one engine ─────┘
                     │
                     ▼
          outbox → events → subscriptions
```

There is one model registry, one policy language, one operation tree, one
authorization kernel, and one mutation engine. GraphQL, events, aggregation, and
migrations MUST NOT grow independent handwritten copies of model metadata.

---

## 4. Technology and provider boundary

### 4.1 Database execution

- Runtime execution uses `database/sql` and `sqlx`.
- Every transaction-bound operation uses `sqlx.Tx`; it MUST NOT escape to the
  unrestricted database handle.
- SQLite and PostgreSQL have independent renderers and live conformance suites.
- Placeholder rebinding alone does not constitute a dialect implementation.
- MySQL is unsupported until it has a complete operator, planner, mutation,
  migration, and acceptance matrix of its own.

### 4.2 Capability model

The operator registry declares, per value kind and provider:

- input validation;
- null behavior;
- Go evaluation;
- SQL rendering;
- parameter encoding;
- result decoding; and
- required provider capabilities.

A provider-specific storage feature, such as a scalar-list representation, MAY
be exposed only when the model declares that capability. A missing capability is
a generation or planning error naming the model, field, operator, and provider.

### 4.3 No fallback hole

Every planned operation has exactly one outcome:

1. compile and execute through a specified provider plan;
2. execute through another fully specified and equivalently authorized plan; or
3. return a stable unsupported-operation error before partial work.

“Run some other way” is not an implementation strategy.

---

## 5. Model source of truth

### 5.1 Schema hierarchy

```text
Go models + db/golem tags
    → validated versioned model IR
    → generated typed Go artifacts + provider migration plan
    → immutable reviewed SQL migration history
    → migrated database verified against the expected fingerprint
```

Go models state the desired logical schema. Migration history states how a
deployed database reached its physical schema. Startup verifies their agreement;
it never silently auto-migrates production.

Runtime reflection is not the schema authority. Unknown tags, ambiguous types,
invalid relations, incompatible exposure modes, and provider-specific constructs
pretending to be portable MUST fail generation.

### 5.2 Minimal declaration form

```go
type User struct {
    _ struct{} `golem:"model;table=users"`

    ID        string    `db:"id" golem:"pk;default=uuid"`
    Email     string    `db:"email" golem:"unique"`
    Password  string    `db:"password" golem:"writeonly;immutable"`
    Name      string    `db:"name"`
    CreatedAt time.Time `db:"created_at" golem:"default=now;readonly"`

    Posts []Post `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
}

type Post struct {
    _ struct{} `golem:"model;table=posts"`
    _ struct{} `golem:"index=idx_posts_author_created(author_id,created_at)"`

    ID        string    `db:"id" golem:"pk;default=uuid"`
    AuthorID  string    `db:"author_id"`
    Title     string    `db:"title"`
    Body      *string   `db:"body"`
    Published bool      `db:"published" golem:"default=false"`
    CreatedAt time.Time `db:"created_at" golem:"default=now;readonly"`

    Author *User `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
}
```

Semicolons separate attributes. Commas inside an attribute value preserve an
ordered field list. Tags are declarative metadata, never raw SQL fragments.

### 5.3 Type and identity rules

- Required versus nullable state follows supported Go types, normally a value
  versus pointer or declared nullable wrapper.
- Relation fields use `db:"-"`; physical foreign-key scalar fields remain
  explicit.
- A slice is a relation only with a relation declaration. Scalar-list storage is
  an explicit capability.
- Composite primary and unique identities preserve declared field order and are
  supported consistently in caller clients, GraphQL, events, cursors, outbox,
  transactions, and subscriptions.
- Logical, Go, GraphQL, table, and column names are separate IR properties.
- Big integers, exact decimals, times, bytes, enums, and JSON have explicit
  logical scalar descriptors and codecs.

### 5.4 Generated artifacts

The compiler generates, deterministically:

- stable `ModelID` and `FieldID` identities;
- model, scalar, relation, key, index, and exposure descriptors;
- typed scalar field and to-one/to-many relation handles;
- typed conditions, ordering, selections, create/update inputs, and update actions;
- scan/write metadata for `sqlx`;
- model-attached policy and hook bindings;
- GraphQL schema and resolver bindings;
- provider migration plans and immutable migration files; and
- an embedded schema fingerprint.

Generated descriptors needed by policy methods live in the model package. The
generated application package imports the model package in one direction, which
prevents an import cycle.

---

## 6. Application authoring contract

### 6.1 Policies belong to models

Authorization is attached to the model it protects:

```go
func (Post) DefinePolicy(r *golem.Rules[Post], actor Actor) {
    owned := Posts.AuthorID.Eq(actor.ID)

    r.CanRead(Posts.Published.Eq(true).Or(owned))
    r.CanCreate(owned)
    r.CanDelete(owned)
    r.CanUpdateFields(owned, Posts.Title, Posts.Body, Posts.Published)
}
```

A policy method constructs typed conditions. It does not receive unrestricted
persisted rows and is not an arbitrary boolean callback.

The compiler discovers and validates the method. There is no process-global
`init` registry and no application startup map such as `Post: PostPolicy{}`.
Shared helpers MAY be called from several model methods. An application uses one
actor type unless an explicitly designed multi-actor adapter is added later.

Conceptually, the compiler emits the internal bridge rather than asking the
application author to register one:

```go
func buildPostPolicy(actor Actor) (golem.Policy, error) {
    rules := golem.NewRules[Post]()
    Post{}.DefinePolicy(rules, actor)
    return rules.Build()
}
```

The actual generated bridge uses the model registry and stable identities, but
this lifecycle is fixed: resolve actor, construct a fresh builder, invoke the
model method, validate, and freeze an immutable execution-scoped policy.

An exposed model without a reachable grant is denied by default. A malformed
recognized method is a generation error, not a silently ignored policy.

### 6.2 Conditional field access

```go
func (User) DefinePolicy(r *golem.Rules[User], actor Actor) {
    self := Users.ID.Eq(actor.ID)

    r.CanRead(golem.All[User]())
    r.CannotReadFields(golem.All[User](), Users.Email, Users.Phone)
    r.CanReadFields(self, Users.Email, Users.Phone)
}
```

The row remains visible. `Email` and `Phone` are readable only on the actor's own
row. Programmatic output uses the generated nullable/masked representation;
GraphQL exposes those output fields as nullable.

### 6.3 Hooks belong to models

Recognized typed methods attach hooks to their model:

```go
func (Post) BeforeCreate(
    ctx context.Context,
    request *PostCreateRequest,
) error {
    actor := golem.ActorFrom[Actor](ctx)
    request.AuthorID.Set(actor.ID)
    return nil
}
```

The compiler emits the internal middleware binding. Application authors do not
maintain a parallel hooks registry.

### 6.4 Composition

Generated application composition contains infrastructure, not duplicated model
behavior:

```go
app, err := socialapp.Open(ctx, socialapp.Config{
    DB:               db,
    Provider:         golem.PostgreSQL,
    ResolvePrincipal: auth.ResolvePrincipal,
    Outbox:           events.OutboxConfig,
})
```

No `Policies` or `Hooks` fields are required because their bindings are generated
from the model packages.

---

## 7. Typed condition language

### 7.1 Representation

`Predicate[M]` owns a validated provider-neutral condition tree. Model type
parameters prevent predicates for unrelated roots from being combined. A
generated `ToOne[M, R]` or `ToMany[M, R]` handle is required to cross from model
`M` to related model `R`.

The tree is the semantic boundary. Golem does not parse arbitrary Go closures or
source expressions into SQL.

### 7.2 Constants and combinators

The language contains typed `All[M]` and `None[M]`, plus `And`, `Or`, and `Not`.
Construction normalizes empty logical forms to defined constants, rejects invalid
empty condition nodes, flattens safe associative forms, removes identities and
duplicates, and produces deterministic canonical shape. It MUST NOT apply a
classical simplification that can change meaning under SQL three-valued logic.

### 7.3 Operators

The portable registry covers the validated forms specified in
`01-operators.md`, including:

- scalar `equals`, `not`, `in`, `notIn`, `lt`, `lte`, `gt`, and `gte`;
- string `contains`, `startsWith`, and `endsWith` with explicit comparison mode;
- to-one bare/is/is-not and to-many some/every/none relation conditions;
- scalar-list `has`, `hasEvery`, `hasSome`, `isEmpty`, and exact list equality
  where the declared provider storage supports them;
- JSON path, type-guarded equality, ordering, string, and array operations; and
- logical combinators at every valid condition position.

Operators validate against the declared field kind. Cross-kind ordering is
rejected. Unknown operators and unknown fields are rejected. Null operands follow
one documented rule per operator; inconsistent accidental TypeScript behavior is
not preserved.

### 7.4 Exact values

- Integer and decimal comparison is exact and does not pass through `float64`.
- The Go programmatic API supports its declared integer/decimal types; it does not
  inherit JavaScript's `2^53` safe-number limit.
- GraphQL and JSON use string-backed exact custom scalars where ordinary JSON
  numbers would lose precision.
- Date/time parsing accepts one documented canonical format set, not
  implementation-dependent JavaScript or database parsing.
- String ordering uses one specified Unicode/byte collation whose SQLite,
  PostgreSQL, and Go results are proven by the agreement corpus, including
  astral-plane and private-use characters.

### 7.5 Two-valued SQL

Every rendered condition is forced to a two-valued result. `NOT`, `NOT IN`,
nullable comparisons, relations, lists, and JSON type guards MUST preserve the
exact table in `01-operators.md`. Tests explicitly probe SQL `NULL`; visual SQL
inspection is not acceptance.

---

## 8. Policy rule model and resolution

### 8.1 Rule shape

An internal rule contains:

- action: read, create, update, or delete;
- effect: grant or deny;
- optional condition, where `nil` means unconditional;
- optional ordered field identities, where `nil` means model-wide; and
- declaration position.

An empty non-nil condition and an empty non-nil field list are invalid. Field
patterns and string field names are not supported in the kernel.

### 8.2 Priority

Rules are declared oldest to newest and resolved newest to oldest. The last
applicable declaration has the highest priority:

1. a conditional denial excludes matching rows from every older grant;
2. a conditional grant contributes rows after newer denials are excluded;
3. the first reachable unconditional grant or denial terminates the chain; and
4. no reachable grant produces `None`.

Declaration order is semantic. Canonicalizing a derived condition MUST NOT reorder
the rule chain.

### 8.3 Row and field lenses

The same ordered rules produce two distinct views:

- The **row lens** uses model-wide rules and positive field grants. A positive
  field grant grants the model action for matching rows. A field denial does not
  hide the row.
- The **field lens** uses model-wide rules and rules naming that field. A field
  denial protects the field without hiding the row.

The algorithms in `02-policy-resolution.md` are normative. They MUST remain
separate even if a current invariant makes some algebraic outputs equivalent;
their inputs and security purposes differ.

The result is merged with the caller condition using `AND`; it never replaces the
caller condition. No reachable grant fails the action gate.

### 8.4 Classification

For a field and selecting constraint, classification returns:

- `always`: readable for every selected row;
- `conditional`: readable for some selected rows, with its condition and ordered
  dependency plan; or
- `never`: unavailable for every selected row.

Discharge is computed only as:

```text
Implies(selectingConstraint, fieldCondition)
```

It is never inferred because the field's local chain looks unconditional. This
closes the sibling field-grant filter oracle recorded in `KNOWN-ISSUES.md`.

The implication checker is conservative. If it cannot prove implication, the
field remains conditional and a value-influencing reference is refused. A more
powerful implication engine MAY replace it only with equivalence/security tests.

### 8.5 Classified positions

Classification occurs before execution for every field name that can influence
observable output, including:

- filters at every relation depth;
- unique and compound selectors;
- ordering and relevance fields;
- cursors and distinct fields;
- projections and relation selections;
- aggregate measures, group dimensions, `having`, and aggregate ordering;
- batch update/delete selectors;
- upsert selectors and branch-relevant selectors;
- connect, disconnect, set, connect-or-create, nested update/upsert/delete, and
  every nested write selector; and
- computed-field dependencies.

Unknown names are refused before provider execution. No security property may be
borrowed from a downstream GraphQL or database validator.

Write data fields additionally pass action-specific field authorization. A
write-only field can be supplied as data but cannot be named in a selector,
filter, order, cursor, distinct, group, or return projection unless separately
readable.

### 8.6 Dependency hydration and masking

Conditional fields declare ordered scalar requirements and a merged relation
dependency tree. The read planner hydrates these privately, replans if required,
evaluates the field condition per row, and removes all policy-only dependencies
before returning the result.

Missing dependency data fails closed by masking or refusing; it never guesses.
Masking returns a present nullable field with `null`/none semantics, not an omitted
field whose shape reveals authorization.

---

## 9. Principal, scope, and client capabilities

### 9.1 Caller execution

Normal work begins with explicit caller binding:

```go
caller, err := app.ForPrincipal(ctx, principal)
```

Principal resolution and all model policies are evaluated fresh for the
execution. Policy objects and decision memoization live inside that execution.
The application-wide engine MAY cache immutable metadata and compiled templates;
it MUST NOT cache an actor-specific policy or result across executions.

Every execution has an unforgeable identity used by request loaders and caches.
Context values alone are not treated as a safe global cache key.

### 9.2 System capability

Trusted code may explicitly acquire:

```go
system := app.System()
```

The system client is a distinct unrestricted capability for migrations, seeds,
trusted workers, and administrative maintenance. It cannot be constructed by a
missing principal or inferred from an empty context.

System operations bypass caller policy and caller hooks. Successful configured
writes still create truthful event/outbox facts. Raw SQL, if offered at all, is a
separate explicit system-only API and never appears on a caller client.

### 9.3 Transactions

Caller and system transaction closures receive clients bound to the transaction:

```go
err := caller.Transaction(ctx, func(tx *socialapp.CallerTx) error {
    // Every operation uses this sqlx.Tx and this caller execution.
    return nil
})
```

Nested operations cannot escape to the base connection. Read-after-write loader
invalidation occurs in the engine for both programmatic and GraphQL mutations.

---

## 10. Operation surface

### 10.1 One engine, two principal surfaces

Generated model clients expose typed equivalents of:

- `findUnique`, `findFirst`, `findMany`, and `count`;
- `create`, `update`, `upsert`, `delete`, `updateMany`, and `deleteMany`;
- nested relation writes supported by the mutation planner;
- aggregate, group-by, and relation aggregate operations;
- explicit closure transactions; and
- model events/subscriptions when configured.

GraphQL translates its arguments and selection set into the same internal
requests. A resolver does not contain independent repository or authorization
logic.

### 10.2 Typed caller API

```go
posts, err := caller.Posts.FindMany(ctx,
    Posts.Where(Posts.Published.Eq(true)),
    Posts.OrderBy(Posts.CreatedAt.Desc()),
    Posts.Take(20),
    Posts.Select(
        Posts.ID,
        Posts.Title,
        Posts.Author.Select(Users.ID, Users.Name),
    ),
)
```

Normal filters, orderings, selectors, projections, and relations use generated
typed values. The caller cannot provide a physical table, column, join, or SQL
fragment.

### 10.3 Opt-outs and extensions

Per-model operation exposure can be disabled in generation configuration. A
disabled surface is absent; it is not emitted and rejected later.

Custom operations declare typed arguments and results against the model registry.
They receive caller context but do not receive system database access unless the
application explicitly passes a system client. Name and type collisions fail
generation or startup.

---

## 11. Authorized read planning

### 11.1 Plan order

An authorized read performs, conceptually:

1. validate the typed request and limits;
2. resolve caller policy for the execution;
3. derive the row constraint for every model hop;
4. collect and classify all referenced fields;
5. plan requested and private dependency selections;
6. compile provider SQL with policy in the base relation;
7. apply ordering, distinct, cursor, skip, and take after authorization;
8. execute through the execution-bound connection or transaction;
9. decode exact values;
10. evaluate conditional masks and computed fields; and
11. remove private dependencies and return the public shape.

### 11.2 SQL shape

- Relation filtering uses correlated `EXISTS`/`NOT EXISTS`, not joins that alter
  root cardinality.
- Every related selection or filter applies the related model's policy.
- To-many JSON aggregation uses provider-correct empty-array coalescing.
- `distinct` semantics preserve requested order and root identity; a naive
  `SELECT DISTINCT` is not substituted when it changes results.
- Alias allocation is deterministic and descriptors are the only identifier
  source.
- Reversal, pagination, and per-parent relation limits retain their documented
  semantics.

### 11.3 Relation loading

The planner chooses correlated or batched relation loading from declared index
metadata, provider capability, limits, and cost rules. Both paths MUST return the
same authorized ordered result.

Batching chunks by the provider's parameter limit and applies limit/offset per
parent, not to the combined child set. An implementation MUST NOT fetch an
unbounded child population merely to slice it in memory. If an efficient
per-parent plan is unavailable, planning fails or uses a bounded documented plan.

### 11.4 Mask construction

There is one mask construction site. Mask conditions are derived from the field
lens and evaluated against hydrated authorized rows. Policy dependency columns
and relations are withheld from public decoding even when selected internally.

### 11.5 Exact decoding

Native driver values remain native at ordinary/root and batched boundaries.
BigInt and Decimal are cast to text only where a JSON aggregation boundary would
otherwise lose their exact representation. The public decoder reconstructs the
declared logical type.

---

## 12. Generated GraphQL contract

### 12.1 Generation

GraphQL is generated from the model IR, exposure metadata, operation registry,
and authorization-aware nullability. It never introspects live tables as its
schema authority.

For a model `Article`, the conventional roots are:

```text
article(where: ArticleWhereUniqueInput!): Article
articles(where, orderBy, cursor, distinct, take, skip): [Article!]!
createArticle(data: ArticleCreateInput!): Article!
updateArticle(where, data): Article!
upsertArticle(where, create, update): Article!
deleteArticle(where): Article!
updateManyArticles(where, data): BatchPayload!
deleteManyArticles(where): BatchPayload!
aggregateArticles(...): ArticleAggregate!
groupByArticles(...): [ArticleGroup!]!
articleEvents(where): ArticleEvent!
```

Only operations implemented by the shared engine and enabled for the model are
emitted. `findFirst` and `count` MAY have explicit roots when enabled; their
absence from the old TypeScript schema is not a runtime limitation.

### 12.2 Exposure matrix

| Mode | Output | Filter/order/selector | Create | Update |
|---|---:|---:|---:|---:|
| normal | yes | yes | yes | yes |
| immutable | yes | yes | yes | no |
| read-only | yes | yes | no | no |
| write-only | no | no | yes | yes |
| write-only + immutable | no | no | yes | no |
| hidden | no | no | no | no |

The matrix applies recursively to nested inputs. Hidden or write-only identities,
invalid overlaps, unknown configured fields, and empty generated input objects
fail generation.

#### 12.2.1 Hook-owned GraphQL create fields

`golem.GraphQLHookOwned(Fields...)` removes trusted server-populated scalars
from root create, upsert-create, and every recursive nested-create GraphQL input
without removing their typed programmatic `CreateFieldCapability`. A recognized
`BeforeCreate` hook is mandatory and can populate each field with
`golem.SetCreate` before planning. The field must be writable for create and
must not participate in an identity key.

Hook ownership is valid for ordinary scalars such as generated slugs or tenant
metadata. If an owned scalar participates in a canonical belongs-to key, the
entire composite key must be owned, non-null, and unambiguous; the canonical
relation is then omitted from create inputs too. Partial, nullable, or ambiguous
relation ownership is a compiler error. Output/filter exposure is unchanged,
and update exposure is governed independently by the normal field modes (for
example, `immutable`). This is ContractIR-only metadata: it changes the contract
fingerprint and GraphQL ABI, never ModelIR or the migration plan. If create or
upsert is enabled, at least one client-owned create input position must remain;
otherwise generation fails with `P8_GRAPHQL_HOOK_OWNED_EMPTY_CREATE` rather than
silently deleting an authored root.

### 12.3 Conditional nullability

When a visible scalar, enum, relation, or relation count can be conditionally
masked, that GraphQL output occurrence is nullable regardless of database
nullability or relation cardinality. A present to-many relation remains a
non-null list of non-null authorized rows. Inputs keep model/default requiredness.
Masked values become `null`; the containing object, enclosing list, and event
remain present if otherwise authorized.

### 12.4 Filters and complexity

GraphQL condition inputs are generated from the same typed condition registry.
Scalar, relation, list, and JSON filters MAY be exposed only when their provider
semantics, classification walk, and complexity limits have passed acceptance on
both providers. The final target includes relation filters; the generator MUST
not expose an operator merely because the internal AST can represent it.

Depth, `take`, nested write cardinality, batch size, and public grouping limits
are explicit configuration with safe defaults and hard maximums.

### 12.5 Scalars

IDs, composite selectors, BigInt, Decimal, DateTime, bytes, enums, and JSON use
declared scalar codecs. GraphQL transport limits do not reduce the exact type
range of the Go programmatic API.

---

## 13. Mutation and transaction kernel

Every caller mutation uses one planner and one transaction. The planner enumerates
every touched model, action, identity, field, relation, before-image need,
after-image need, hook, and event fact.

### 13.1 Create

1. Validate and normalize the request.
2. Run sequential before-create middleware/hooks.
3. Expand nested writes into touched nodes.
4. Authorize the create action and candidate writable fields.
5. Execute inside a transaction.
6. Load the actual persisted result, including defaults and required relations.
7. Verify row and field policy against that result.
8. Run transaction-local after-create hooks.
9. Write per-row event/outbox facts.
10. Commit, then run explicit after-commit effects.

Any denial or before/transaction-local hook failure rolls back data and outbox
facts. An after-commit failure is reported through the configured trusted error
handler and cannot turn an already committed mutation into a returned failure.

### 13.2 Update and delete

1. Combine the caller selector with the action constraint.
2. Resolve and lock/read the required before image inside the transaction.
3. Return `NOT_FOUND` for both absent and policy-invisible targets.
4. Authorize every actually changed field using before/after values. A no-op is
   not treated as a forbidden change.
5. Authorize every nested touched model independently.
6. Load and verify the after image where applicable.
7. Run transaction-local after hooks and write event/outbox facts.
8. Commit, invalidate execution loaders, and run after-commit effects.

### 13.3 Batch mutations

- Policy is part of the target SQL.
- The planner captures a bounded deterministic identity set in the transaction.
- Each affected row is independently verified when required.
- Each affected row produces its own truthful event fact.
- Exceeding row or payload limits rejects the whole mutation before modification;
  Golem never silently truncates.
- Nested batch operations remain unavailable until their entire touched set can
  be enumerated and authorized.

### 13.4 Upsert

Upsert correctness is database-backed:

- The unique constraint is the final existence authority.
- Branch probing and branch execution occur in one transaction.
- The update branch uses the update reach, not the read reach, and existence
  outcomes do not disclose an inaccessible row.
- Provider-native locking or conflict clauses MAY reduce races.
- Cooperative striped locks MAY reduce same-process contention but are never a
  correctness dependency.
- Serialization and unique interference cause a bounded retry of the complete
  attempt or a stable `CONFLICT`.
- Transaction-local hooks may repeat during a complete-attempt retry and must be
  retry-safe; irreversible external effects are after-commit only.
- Only the committed branch emits an event naming its truthful action.

### 13.5 Nested writes

Connect, disconnect, set, create, create-many, connect-or-create, update,
update-many, upsert, delete, and delete-many are exposed only when the planner
can enumerate and authorize every affected row and field, enforce
depth/cardinality limits, and produce correct event facts. A parent grant never
implies a child grant.

---

## 14. Hooks, middleware, and computed fields

### 14.1 Internal middleware

Generated model methods compile into an ordered middleware pipeline around the
shared operation engine. Ordering is deterministic and validated at generation.

- Before hooks may transform or veto a request sequentially.
- Transaction-local after hooks observe the verified result and may add
  transaction work but cannot replace the authorized result.
- After-commit hooks are the only home for irreversible queues, email, webhooks,
  and external effects.
- Upsert runs the hooks of the branch actually attempted and committed; there is
  no fictional generic upsert hook.
- System operations bypass caller hooks by design.

Aggregate hooks are not inferred from row hooks. They require a separate typed
contract before being added.

### 14.2 Computed fields

A computed field declares generated field/relation dependencies. Dependencies
are classified and privately hydrated. The result is masked/refused consistently
with its declared authorization.

Batched computed fields are execution-scoped. Their cache key includes execution
identity, model, field, arguments, selection, and ordered typed identity. Batch
size is bounded and scheduling is executor-driven, not timer-dependent.

Engine-level successful writes clear all affected execution loaders, regardless
of whether the write originated in GraphQL or the programmatic client.

---

## 15. Aggregation and scoped reads

### 15.1 Aggregate authorization

- Every aggregate starts from the same authorized row scope as an ordinary read.
- Each measure, dimension, `having`, and order field must be readable for every
  row that can contribute.
- A conditional field is accepted only when semantically discharged by the
  selecting constraint.
- Aggregates are never partially masked; an ineligible field is refused by name.
- Exact BigInt, Decimal, time, and null result semantics are provider-tested.

GraphQL public `maxGroups` and trusted programmatic limits are separate. The
programmatic API is not silently capped by GraphQL configuration, but it remains
subject to explicit resource limits.

### 15.2 Relation-traversing aggregation

Relation traversal is a required target, not an accidental unsupported case.
Its semantics are explicit:

- Forward to-one chains contribute once per authorized root whose entire required
  path is present and authorized; absent or invisible targets use documented
  inner-path semantics.
- To-many or mixed paths MUST use an explicit relation expansion/aggregate root
  whose contribution unit is an authorized root-related-row pair. They are never
  silently interpreted as ordinary root grouping.
- Policy is applied independently at every hop.
- Duplicate contribution, null grouping, ordering, limits, and exact result types
  have provider agreement fixtures.
- If a requested path shape has no accepted planner, it is rejected explicitly.

Version-one completion requires forward to-one traversal. General to-many/mixed
path traversal is a separately gated extension, but its absence is visible and
must not be described as full relation aggregation support.

### 15.3 Scoped read escape hatch

The scoped builder is typed and read-only. Every root and join receives policy;
every output, filter, order, group, and join field is classified. Physical
identifiers come from descriptors. Insert, update, delete, DDL, arbitrary raw SQL,
and forged roots are structurally unavailable.

The builder carries an audit record of its model roots, joins, selected fields,
principal, execution, and generated SQL fingerprint.

---

## 16. Events, outbox, subscriptions, and CDC

### 16.1 Event facts

Events describe committed mutation facts, not resolver intent. Each fact includes:

- globally unique event ID and version;
- model and created/updated/deleted type;
- ordered scalar or composite identity;
- recorded-time provenance, causation ID, and transaction ordinal; recorded time
  is not represented as a globally ordered commit timestamp;
- exact scalar codec metadata; and
- a private pre-delete snapshot when required for later authorization.

Top-level batch mutations create one fact per affected row. Nested event policy is
declared by the mutation contract and cannot silently omit rows merely because a
batch API was used.

### 16.2 Transactional outbox

Data and outbox records commit in the same transaction. Publication is
at-least-once, event IDs support deduplication, and a process crash after data
commit does not lose an outbox-backed event.

The codec is versioned and lossless for BigInt, Decimal, time, bytes, enums, JSON,
composite identity, delete snapshots, and batch causation.

### 16.3 Subscription delivery

- A bounded local hub consumes each configured model stream.
- Every event creates a fresh execution. The subscriber's authenticated identity
  or revalidation handle may be retained as input, but actor derivation and model
  policy construction run again; an actor-specific policy result is not retained
  for the subscription lifetime.
- The event entity is re-read or evaluated from a sufficient private snapshot and
  re-authorized before delivery.
- Subscriber filter and selection are classified and authorized.
- Work MAY be shared only for subscribers with proven-equivalent security
  identity, policy version, filter, selection, and execution-independent result.
- Delivery queues are bounded. Overflow disconnects the slow consumer with a
  stable reason; it does not silently drop an event and continue.
- Cancellation tears down loaders, goroutines, and queue membership.
- Delete snapshots are authorization input and are never exposed as the public
  deleted entity unless the public contract explicitly permits a field.

### 16.4 CDC boundary

The transactional outbox observes all Golem writes, including explicit system
writes. It cannot observe arbitrary SQL issued by another process.

Provider-specific CDC adapters MAY translate external database changes into the
same event envelope and the same subscription authorization path. Without such
an adapter, configuration, diagnostics, and documentation state plainly that
out-of-process writes are invisible.

CDC does not bypass policy, create a second event schema, or promise exactly-once
delivery.

---

## 17. Batching and cache isolation

Loaders are execution-owned and keyed by typed model identity, relation/computed
field, canonical arguments, selection, and connection/transaction identity.

- No loader or actor-specific memo lives on the application-wide engine.
- Ordinary requests and each subscription event have distinct execution keys.
- Batch results preserve input ordering and distinguish missing from denied only
  where the public contract allows that distinction.
- Provider parameter ceilings determine chunking; constants have documented
  derivations and tests.
- A write clears all affected loader entries in the operation engine, including
  writes made through the programmatic API.
- Cache invalidation is all relevant keys or a proven complete dependency set;
  partial best-effort clearing is forbidden.

---

## 18. Error and information-disclosure contract

The engine owns stable typed errors. GraphQL maps them deterministically:

| Internal category | GraphQL code |
|---|---|
| invalid input, unsupported operation, limit, hook veto | `BAD_USER_INPUT` |
| absent or policy-invisible target | `NOT_FOUND` |
| uniqueness, serialization, bounded retry conflict | `CONFLICT` |
| unresolved/invalid principal | `UNAUTHENTICATED` |
| resolved principal lacks action/field permission | `FORBIDDEN` |

Refusals name logical public model/field/operation information only. Driver
messages, SQL, stack traces, policy internals, physical names, existence of hidden
rows, and provider details are not exposed.

For operations whose existence result is sensitive, nonexistent and unauthorized
targets have the same code, message shape, GraphQL null/error shape, and event
behavior. Timing SHOULD be made comparable where practical and MUST NOT include
an intentional distinguishing probe.

---

## 19. Limits and denial-of-service boundaries

The runtime has explicit configurable ceilings for:

- read `take`, relation depth, selected field count, and relation fan-out;
- batch mutation rows and payload bytes;
- nested write depth and touched-node count;
- aggregate groups, relation expansion, and intermediate rows;
- SQL parameters and statement/alias complexity;
- computed-field batch size and pending loader keys;
- subscription queue depth and evaluation concurrency; and
- transaction retry count and duration.

Exceeding a limit refuses before mutation or unbounded allocation. No operation
silently truncates a result whose contract does not explicitly define pagination.

---

## 20. Acceptance and proof obligations

Implementation is complete only when behavior is measured, not merely present.

### 20.1 Model compiler and migrations

- golden generation for models, descriptors, GraphQL, and both provider plans;
- deterministic output under repeated generation;
- invalid tag/type/relation/exposure fixtures;
- composite identity fixtures across every generated surface;
- schema fingerprint mismatch and immutable migration tests; and
- live migrate-up verification on SQLite and PostgreSQL.

### 20.2 Operator agreement oracle

For every supported condition and value kind, the same corpus is evaluated by:

1. the Go evaluator;
2. compiled SQLite SQL; and
3. compiled PostgreSQL SQL.

The exact same row identities must result. Separate probes assert that every SQL
predicate returns only true or false at the authorization boundary, including
JSON, null, Unicode, exact numeric, list, and relation cases.

Every named mutation in `01-operators.md` must make a test fail.

### 20.3 Policy/classification oracle

- ordered row and field results are checked against independent oracle fixtures;
- semantic discharge includes the sibling field-grant regression;
- every classified position has a spy test proving it was visited;
- every field-reference mutation in `02` and `03` makes a disclosure test fail;
- dependency ordering and merged relation hydration are deterministic; and
- the exported kernel contains no string-keyed field API.

### 20.4 SQL and runtime oracle

- policy-before-pagination and policy-at-every-hop tests;
- correlated/batched relation agreement;
- exact masking and withholding tests;
- deterministic SQL and bind ordering;
- read-after-write cache invalidation through GraphQL and caller clients;
- execution isolation under concurrent principals;
- transaction connection-escape mutation tests; and
- every named mutation in `04` and `05` must make a test fail.

### 20.5 Mutation/concurrency proof

- create persisted-result verification and rollback;
- update before/after field diffs and no-op changes;
- nonexistent versus invisible result equality;
- batch per-row verification and event facts;
- concurrent same-key upsert across separate processes/connections;
- bounded retry tests proving transaction hooks may repeat, failed-attempt
  after-commit hooks do not run, and reliable external effects use the outbox;
- nested touched-set completeness.

### 20.6 Event/subscription proof

- data/outbox atomicity under failures at every transaction boundary;
- publisher crash/restart and consumer deduplication;
- exact scalar/composite identity codec round trips;
- fresh per-event authorization after permission changes;
- equivalent-subscriber grouping without cross-principal leakage;
- bounded overflow, cancellation, and goroutine leak tests;
- per-row batch event delivery; and
- external-write invisibility without CDC plus delivery through each installed CDC
  adapter.

### 20.7 Cross-caller conformance

The same logical operation through the generated caller client and GraphQL must
produce the same selected data, masks, refusal category, committed mutation,
event facts, and policy trace, modulo transport encoding.

---

## 21. Delivery phases and definitions of done

| Phase | Definition of done |
|---|---|
| P0 — constitution | This Bible, source classification, semantic oracle fixtures, and explicit ownership of every capability are accepted. No production backend is claimed. |
| P1 — model compiler | Go models/tags compile to validated versioned IR, typed descriptors, deterministic SQLite/PostgreSQL migrations, generated policy/hook bindings, and verified fingerprints. |
| P2 — policy kernel | Full accepted operator registry, ordered policy resolution, classification, dependency planning, implication/discharge, and live evaluator/SQLite/PostgreSQL agreement pass. |
| P3 — reads/client | Explicit system and caller clients execute authorized typed reads, relations, selection, batching, masking, pagination, limits, and stable errors on both providers. |
| P4 — mutations | CRUD, batches, nested writes, upsert, transactions, field diffs, model-attached hooks, loader invalidation, and commit-derived outbox facts pass both-provider and concurrency tests. |
| P5 — GraphQL | Generated schema and resolvers cover accepted model/query/mutation inputs, conditional nullability, scalars, computed/custom fields, limits, and error mapping through the shared engine. |
| P6 — analytics | Count, aggregate, group-by, accepted relation traversal, and scoped read builder preserve policy, exact types, and resource limits on both providers. |
| P7 — events | Transactional outbox, publisher, per-row events, subscriptions, fresh authorization, bounded fan-out, codecs, and optional CDC adapters pass failure and isolation tests. |
| P8 — hardening | Cross-entry-point conformance, red-team disclosure tests, load/failure recovery, observability, compatibility docs, release automation, and production examples are complete. |

The supported production and release surface is defined by
[`p8/PUBLIC-PRODUCTION-ABI.md`](./p8/PUBLIC-PRODUCTION-ABI.md), the compatibility
manifest, and the published module release.

---

## 22. Intentional boundaries

The following are honest boundaries, not hidden promises:

- Queue is excluded from the core Go product.
- MySQL is unsupported.
- Runtime startup does not auto-migrate production databases.
- Arbitrary raw SQL is not available to caller clients.
- Out-of-process writes are invisible without an installed CDC adapter.
- Aggregate hooks do not exist until separately specified.
- General to-many/mixed relation aggregation is not claimed until its explicit
  expansion semantics and provider tests pass.
- Provider-specific scalar-list/JSON capabilities are exposed only when declared
  and accepted.
- Programmatic grouping is not governed by GraphQL `maxGroups`, but it is still
  governed by explicit programmatic resource limits.

These boundaries require clear generation/configuration diagnostics. They MUST
NOT appear as silent missing methods, empty results, or partial behavior.

---

## 23. Resolved conflicts from the source designs

This section records the merge decisions so later work does not reopen them by
accident.

| Conflict | Normative resolution |
|---|---|
| PostgreSQL-first versus dual provider | SQLite and PostgreSQL are equal required providers. |
| Central policy/hook registration versus locality | Policies and hooks are model-attached methods; the compiler generates internal bindings. |
| No system constructor versus explicit maintenance access | `app.System()` is an explicit separate capability; missing caller scope always refuses. |
| String model/field names versus identities | The kernel uses generated `ModelID`/`FieldID`; GraphQL strings resolve at the boundary. |
| Request policy reused for subscription lifetime versus fresh policy | Each event gets a fresh execution and policy resolution. |
| Cast exact numbers everywhere versus only JSON boundaries | Preserve native types; cast/reconstruct only at lossy JSON aggregation/transport boundaries. |
| JavaScript safe-number limit versus Go exact values | Go uses declared exact types; GraphQL/JSON use exact custom scalar codecs. |
| Unspecified SQL fallback | Compile through a specified plan or refuse before execution. |
| Cooperative striped upsert lock versus atomic correctness | Database uniqueness/isolation plus bounded retry is authoritative; stripes are optional contention reduction. |
| In-process events versus durable delivery | Mutation facts and outbox rows commit atomically; publication is at-least-once. |
| Per-request subscription policy versus per-event policy wording | Principal may persist, policy result may not; re-resolve and re-authorize every event. |
| Conditional field syntactic discharge | Only semantic implication against the actual selecting constraint discharges. |
| GraphQL-only loader clearing | The operation engine invalidates loaders for every successful write entry point. |
| Batch mutations without per-row events | Top-level batches emit per-row committed event facts. |
| Composite identity limited to programmatic context | Composite identity is a model-IR primitive supported across all surfaces. |
| Relation filters absent from old GraphQL | They are a gated final target once operator, classifier, provider, and complexity acceptance pass. |
| Relation aggregation absent | Forward to-one is required; to-many/mixed traversal uses an explicit expansion contract and is not falsely claimed early. |

---

## 24. Traceability to the merged sources

| Bible subject | Detailed source | Phase 0 source |
|---|---|---|
| product promise and technology | `05-surface-and-runtime.md` | `P0_DECISIONS.md` §§1–2 |
| models, compiler, migrations | statement metadata requirements | `P0_DECISIONS.md` §3; `PHASE_MAP.md` P1 |
| author-facing policy and hooks | internal policy/hook injection points | `P0_DECISIONS.md` §§4, 8; `SOCIAL_NETWORK_EXAMPLE.md` |
| operators and two-valued SQL | `01-operators.md` | `CONTRACT.md`; `DESIGN.md` |
| ordered row/field rules | `02-policy-resolution.md` | `CONTRACT.md`; Phase 0 oracle fixtures |
| field classification/discharge | `02-policy-resolution.md`; `03-classification.md`; `KNOWN-ISSUES.md` | `P0_DECISIONS.md` §§4, 6 |
| reads, SQL, masking | `04-statement-shape.md` | `P0_DECISIONS.md` §§5–6 |
| clients, batching, errors | `05-surface-and-runtime.md` | `P0_DECISIONS.md` §§5, 11 |
| mutations/upsert/hooks | runtime operation contracts | `P0_DECISIONS.md` §§7–8; `PHASE_MAP.md` P4 |
| GraphQL | `05-surface-and-runtime.md` | `P0_DECISIONS.md` §6; `TS_SURFACE_CLASSIFICATION.md` §5 |
| aggregates/scoped reads | statement and classification rules | `P0_DECISIONS.md` §10; `PHASE_MAP.md` P6 |
| events/subscriptions/CDC | `05-surface-and-runtime.md` subscriptions | `P0_DECISIONS.md` §9; `PHASE_MAP.md` P7 |
| delivery sequence | acceptance sections 01–05 | `PHASE_MAP.md` P0–P8 |

---

## 25. Final implementation rule

Golem's public authoring surface should feel small because its internal contract
is exact, not because security and database behavior were omitted.

When choosing between a convenient implementation and the invariants in this
Bible, the invariants win. When the invariants cannot be implemented for both
providers, the feature remains unavailable and the refusal is explicit. When a
new feature changes what a caller can observe, it requires classification,
provider agreement, cross-caller behavior, and mutation tests before it becomes
part of the generated surface.

That is the definition of “Golem handles the backend”: the application author
states models, policy, and product-specific behavior once, while the generated
system supplies the repetitive API machinery without weakening authorization,
transactionality, type fidelity, or event truth.
