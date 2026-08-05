# Phase 0 architecture decisions

## Status

This document freezes the semantic architecture needed to end Phase 0. Public
identifier spelling may improve during P1 code generation, but later work must
preserve these boundaries and outcomes or amend this document and its fixtures
explicitly.

## 1. Product definition

Golem for Go is a model-driven backend engine. The application author should be
able to:

1. define Go models and relationships;
2. define actor-specific row and field policy;
3. optionally add hooks, computed fields, and custom operations; and
4. start a generated GraphQL/programmatic backend whose ordinary CRUD,
   authorization, transactions, aggregation, and events are already implemented.

Application authors should not write a resolver, service, repository, DTO, and
authorization query for every model. Custom code is for product behavior, not
repeating database plumbing.

## 2. Fixed technology boundary

- Database execution uses `sqlx` and `database/sql` transactions.
- SQLite and PostgreSQL are equal required providers.
- MySQL is rejected until it has its own complete semantic/compiler/test matrix.
- Golem owns its model compiler, generated descriptors, operation engine, policy
  compiler, GraphQL generation, mutation planner, and event boundary.
- Golem does not build on Storm, GORM, Ent, or another ORM.
- Storm is a reference for parsing Go structs and generating typed descriptors.
  Its PostgreSQL-only runtime, repository implementation, and full tag language
  are not dependencies.
- A GraphQL library is an implementation dependency behind Golem's generated
  schema contract. It is not allowed to define Golem's authorization or mutation
  semantics.

## 3. Model source of truth

### Decision

Application Go structs are the desired logical model source. Golem statically
parses them and their `db`/`golem` tags during generation. Runtime reflection is
not the schema authority.

The source hierarchy is:

```text
Go models + tags
    -> validated versioned model IR
    -> generated Go descriptors and provider migration plan
    -> immutable SQL migration history
    -> migrated live database verified against the expected fingerprint
```

This resolves “tags versus SQL migrations” without creating two competing
schemas:

- tags express the desired model and API facts;
- generated descriptors are the executable compile-time view;
- migration files are the reviewable, immutable history of physical changes;
- migrations run before application code depends on the new schema;
- the application never silently auto-migrates on startup.

The migration history is authoritative for how a deployed database reached its
state. The model is authoritative for the state new code expects. A mismatch is
an error, not an invitation to mutate production automatically.

### Minimal declaration vocabulary

The P1 parser will recognize exported model structs selected by a model marker.
The exact first implementation is intentionally small:

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

Semicolon separates attributes; commas inside one value represent an ordered
field list. Unknown attributes fail generation. Tags are not raw SQL fragments.
Portable logical defaults such as `now`, `uuid`, booleans, numbers, and strings
compile through a provider table. A provider-specific default or type must be
named as provider-specific metadata and cannot masquerade as portable.

### Type and nullability rules

- A value field is required unless explicitly modeled with a supported nullable
  Go type, normally a pointer or a declared nullable wrapper.
- A slice is not automatically a database scalar list. A relation tag makes it a
  relation; a provider-specific scalar-list tag must state its storage capability.
- Relation fields have `db:"-"`; physical foreign-key scalar fields remain
  explicit. Golem never forges a foreign key from a nested object silently.
- Composite IDs and indexes preserve declared field order.
- Logical names, physical table/column names, and GraphQL names are separate IR
  properties.
- Unsupported or ambiguous Go/database type mappings fail generation with the
  model and field named.

### Generated artifacts

For each model, P1 generates:

- a stable model descriptor;
- typed scalar field handles and typed to-one/to-many relation handles;
- ordered primary/unique identity descriptors;
- typed filters, ordering, selections, create/update inputs, and update actions
  as their owning phases arrive;
- scan/write metadata needed by `sqlx` without caller-supplied column strings;
- a model registry consumed by GraphQL, policy SQL, mutations, aggregation, and
  events; and
- a schema fingerprint.

Every subsystem consumes this registry. GraphQL, migrations, authorization, and
events may not maintain separate hand-written model metadata.

Typed descriptors used by model-attached policies are emitted into the model's
Go package (as generated `.go` files). This avoids a package cycle: handwritten
model policy methods can use `Users.ID` and `Posts.AuthorID`, while the generated
application/runtime package imports the model package in only one direction.

## 4. Authorization authoring and evaluation

### Authoring shape

Authorization belongs to the model it protects. A model may implement a typed
policy method, and the model compiler emits the binding; it does not receive
unrestricted persisted rows:

```go
func (Post) DefinePolicy(r *golem.Rules[Post], actor Actor) {
    owned := Posts.AuthorID.Eq(actor.ID)

    r.CanRead(Posts.Published.Eq(true).Or(owned))
    r.CanCreate(owned)
    r.CanDelete(owned)
    r.CanUpdateFields(owned, Posts.Title, Posts.Body, Posts.Published)
}
```

Conditional read masking follows the actual TypeScript rule priority:

```go
func (User) DefinePolicy(r *golem.Rules[User], actor Actor) {
    self := Users.ID.Eq(actor.ID)

    r.CanRead(golem.All[User]())
    r.CannotReadFields(golem.All[User](), Users.Email, Users.Phone)
    r.CanReadFields(self, Users.Email, Users.Phone)
}
```

The model grant makes ordinary fields readable. The field denial removes the
sensitive fields. The newer conditional field grant restores them for the actor's
own row.

The receiver may be implemented in a file beside the struct or in a separate
`post.policy.go`; Go still treats it as behavior belonging to `Post`. Shared rule
helpers can be called by several model policies, so a domain does not have to
duplicate common conditions. There is no application-wide policy object,
per-model startup map, or process-global `init()` registry.

The model compiler owns discovery and emits deterministic bindings into the
generated registry. P1/P2 must prove the exact method contract, enforce one actor
type across the application, reject malformed recognized policy methods, and
fail closed when an exposed model has no applicable grant.

This locality matches real TypeScript consumption better than the demo's single
`DemoRules`: Eros defines `UsersAuthorizer` in its auth feature,
`CatalogAuthorizer` beside catalog models, and `ListeningAuthorizer` beside
listening models. The Authorizer runtime discovers all of them and contributes
each `forUser` method to the same ability. A TypeScript authorizer may cover a
deliberate related group; the Go default is one model receiver, with shared
helpers available when several models use the same domain rule.

### Frozen semantics

- Four actions: read, create, update, delete.
- Default deny when no reachable grant exists.
- Last applicable declaration has highest priority.
- Model-wide and field rules share one priority chain for a field.
- A positive field rule grants the model action for matching rows.
- A field denial protects that field without hiding the row.
- One typed predicate AST is validated, evaluated in memory when a persisted row
  check is required, and compiled into provider SQL when query scoping is required.
- Unsupported conditions fail closed.
- Actor/policy resolution is request scoped. A missing actor is unauthenticated,
  never a system bypass.

## 5. Programmatic client contract

Golem exposes two deliberately different clients.

### System client

The system client is explicit and unrestricted. It is intended for migrations,
trusted workers, seeds, and administrative maintenance. It bypasses caller policy
and caller hooks. Its committed writes still create configured event/outbox
records.

There is no implicit fallback from caller mode to system mode.

### Caller client

The caller client is bound to a resolved principal/policy and exposes only
operations whose semantics Golem implements:

```go
caller, err := app.ForPrincipal(principal)
if err != nil { /* unauthenticated */ }

posts, err := caller.Posts.FindMany(ctx,
    Posts.Where(Posts.Published.Eq(true)),
    Posts.OrderBy(Posts.CreatedAt.Desc()),
    Posts.Take(20),
    Posts.Select(Posts.ID, Posts.Title, Posts.Author),
)
```

Generated APIs may refine this spelling, but these properties are fixed:

- normal model and field references are typed generated values;
- request filters can narrow policy but cannot widen it;
- authorization enters SQL before ordering, skip, cursor, and take;
- policy dependencies may be selected internally but are removed before return;
- selected relations are independently authorized at each model hop;
- conditional fields are evaluated per row and masked in caller results;
- raw SQL is absent from the caller client;
- closure transactions return a transaction-bound caller client; and
- GraphQL calls the same operation engine rather than a parallel repository.

## 6. GraphQL contract

### Generation boundary

GraphQL is generated from the P1 model IR plus model exposure configuration,
hooks/extensions, and the authorization-enabled nullability mode. It does not
inspect database tables at runtime.

For `Article`, version one preserves the current TypeScript root contract:

- `article(where: ArticleWhereUniqueInput!): Article`
- `articles(where, orderBy, take, skip): [Article!]!`
- `createArticle(data: ArticleCreateInput!): Article!`
- `updateArticle(where, data): Article!`
- `upsertArticle(where, create, update): Article!`
- `deleteArticle(where): Article!`
- `updateManyArticles(where, data): BatchPayload!`
- `deleteManyArticles(where): BatchPayload!`
- configured aggregate/group roots; and
- configured `articleEvents(where): ArticleEvent!` after P7.

### Field exposure

The generated surface preserves this matrix recursively, including nested inputs:

| Mode | Output | Filter/order/unique | Create | Update |
|---|---:|---:|---:|---:|
| normal | yes | yes | yes | yes |
| immutable | yes | yes | yes | no |
| read-only | yes | yes | no | no |
| write-only | no | no | yes | yes |
| write-only + immutable | no | no | yes | no |
| hidden | no | no | no | no |

Invalid overlaps, hidden identities, write-only identities/relations, unknown
configured fields, and empty generated input types fail generation/startup.

### Authorization and nullability

- When conditional field checks are enabled, visible scalar and enum outputs are
  nullable even when the database column is required.
- Masking returns null for the field without null-propagating away the containing
  object, relation list, or event.
- Input requiredness continues to follow model/default semantics.
- Event identities remain non-null after event authorization.
- A field classified `never` is rejected by name before database execution.
- Filter/order/group fields must pass their own readability rules; selecting a
  field is not the only way to observe it.

### Filters and nested writes

Version one matches the TypeScript public vocabulary recorded in
`TS_SURFACE_CLASSIFICATION.md`. Relation filters, scalar-list filters, and
additional nested batch operations are added only with:

1. P2 operator/provider agreement;
2. P4 per-touched-row authorization and event semantics;
3. depth/cardinality limits; and
4. GraphQL schema fixtures.

The internal policy or programmatic language being able to represent something
does not automatically make it safe to expose anonymously over GraphQL.

### Extensions

- Computed fields declare their required persisted fields/relations.
- Batched computed fields are request scoped and have explicit cache keys and
  maximum batch size.
- Custom queries/mutations declare argument and result types against the generated
  registry.
- Name/type collisions fail startup.
- Extension functions receive caller context but do not gain unrestricted DB
  access unless the application explicitly injects the system client.

## 7. Mutation transaction contract

Every caller mutation runs through one planner and one database transaction.

### Create

1. Run before-create hooks to transform/validate the request.
2. Resolve every nested write into touched model/action nodes.
3. Authorize action and candidate writable fields.
4. Execute the write in a transaction.
5. Load the actual persisted result and required relations/defaults.
6. Verify create row/field policy against that result.
7. Run transaction-local after hooks.
8. Write outbox/event records from committed identities.
9. Commit; only then run explicit after-commit effects.

A failed verification rolls back data and outbox records.

### Update and delete

1. Merge caller selector with the action constraint and resolve the target.
2. If no authorized target exists, return `NOT_FOUND` whether the physical row is
   missing or merely invisible.
3. Lock/read the required before image in the mutation transaction.
4. Apply the mutation.
5. Load the actual after image where applicable.
6. Verify changed fields by before/after value, so a no-op does not become a
   forbidden change.
7. Verify nested touched models independently.
8. Buffer event/outbox records and commit atomically.

### Batch mutations

- The authorization constraint is part of the target set.
- If per-row verification or events are enabled, the planner captures a bounded,
  deterministic identity set inside the transaction.
- Top-level batches produce per-row events from version one.
- Exceeding row/payload limits rejects before mutation and never truncates.
- Nested batch operations remain unexposed until the planner can enumerate and
  verify their complete touched set.

### Upsert

- The database unique constraint is the final existence authority.
- Branch selection and branch execution occur in one transaction.
- Participating Golem calls may use a provider-specific selector lock to reduce
  conflicts, but correctness may not depend solely on cooperative locking.
- Serialization or unique interference causes a bounded full-attempt retry or a
  stable `CONFLICT`; it may not silently execute a branch the caller cannot
  authorize.
- Only the committed branch produces its truthful event and after-commit effects.
- Retryable transaction hooks must not perform irreversible external effects;
  those belong to after-commit hooks.

## 8. Hooks

Hooks are framework-independent typed methods attached to the model receiver and
discovered by the model compiler. For example,
`func (Post) BeforeCreate(context.Context, *Posts.CreateRequest) error` belongs to
Post's generated before-create pipeline. The author does not repeat
`Post: PostHooks{}` in a startup registry. P4 must prove deterministic ordering,
signature validation, and that malformed recognized hook methods fail generation
rather than being silently ignored.

- Before hooks execute sequentially and may transform or veto a request.
- Transaction after hooks observe the verified result but cannot replace it.
- After-commit hooks are the place for queues, email, webhooks, and other
  irreversible effects.
- Create/update branch hooks, not a fictional generic upsert hook, run for upsert.
- Aggregate hooks are excluded from version one because their row/result semantics
  differ; adding them requires a separate typed contract.
- System client operations bypass caller hooks by design.

## 9. Events, outbox, subscriptions, and CDC

### Event source

P4 emits event records from committed mutation facts, not from resolver intent.
Every record contains a unique event ID, event type, model, ordered scalar or
composite identity, commit metadata, and any private pre-delete snapshot required
for later authorization.

### Delivery

- Data and outbox record commit together.
- Publication is at-least-once; consumers receive an event ID for deduplication.
- A versioned codec preserves BigInt, Decimal, time, bytes, composite identities,
  delete snapshots, and batch envelopes.
- Process failure after data commit does not lose an outbox-backed event.

### Subscriptions

- One bounded local hub consumes a model stream.
- Every delivery uses fresh actor/policy resolution and the subscriber's filter
  and selection.
- Evaluation may be shared only when the security identity, policy version,
  filter, and selection are proven equivalent.
- A slow consumer is disconnected on bounded-queue overflow; events are not
  silently dropped from that consumer queue.
- Delete delivery requires a verifiable pre-delete snapshot and never exposes that
  private snapshot as the public entity.

### External writers

The transactional outbox observes Golem writes, including explicit system-client
writes. It does not observe arbitrary SQL from another process.

P7 defines an optional CDC adapter that converts database changes into the same
versioned event envelope. Without that adapter, startup/configuration and docs
state that external writes are invisible. CDC is an extension of the event source,
not a second subscription authorization path.

## 10. Aggregation and scoped reads

- All aggregates start from the same authorized row scope as ordinary reads.
- A measure/dimension is accepted only if readable for every row in that scope.
- Conditional/inverted field rules not discharged by the row constraint fail by
  field name; aggregate values are never partially masked.
- GraphQL public group limits and programmatic trusted limits are separate options.
- Relation grouping version one supports the current bounded common forward
  to-one path. Missing/invisible targets use inner-join semantics.
- The scoped escape hatch is read-only, applies policy to every root/join, checks
  selected fields, and accepts no caller-provided physical identifiers.

## 11. Stable error taxonomy

The engine owns typed errors that map deterministically to GraphQL:

| Internal category | GraphQL extension code |
|---|---|
| invalid input, unsupported operation, hook veto, depth/take/group limit | `BAD_USER_INPUT` |
| missing or policy-invisible mutation/read target | `NOT_FOUND` |
| unique, serialization, or bounded concurrency conflict | `CONFLICT` |
| actor cannot be resolved | `UNAUTHENTICATED` |
| resolved actor lacks action/field permission | `FORBIDDEN` |

Database driver messages, SQL text, physical names not already public, and
stack/internal details are not exposed through GraphQL.

## 12. Intentional version-one boundaries

- Queue is excluded.
- Render remains an optional later package.
- MySQL is unsupported.
- Programmatic local group-by is not automatically capped by GraphQL's public
  `maxGroups`; applications can configure a separate trusted cap.
- Scalar lists are exposed only where the selected provider/storage capability
  has complete evaluator/compiler tests.
- General to-many/multi-path relation aggregation is later than the one-path P6
  compatibility target.
- Nested batch events require a complete P4 mutation plan before exposure.
- CDC is optional; its absence is explicit.
- No feature is considered implemented because its name exists in generated code.
  Its provider and cross-entry-point conformance tests are part of completion.
