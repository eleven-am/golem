# P4 public mutation and transaction ABI

Status: **complete — controlling public contract implemented by P4**

This document freezes the application-facing shape for P4. Names shown from a
generated `social` model package and `socialapp` application package are exact
patterns, not a second hand-written API. Internal constructors remain sealed.

## 1. Design rules

1. A generated model namespace owns its fields, relations, selectors, inputs,
   predicates, projections, and update operations.
2. Illegal operations are absent wherever Go's type system can make them
   absent. Runtime validation remains mandatory for zero values, forged values,
   limits, requiredness, and values produced across ABI boundaries.
3. Inputs are immutable values after construction. Runtime and hook boundaries
   clone bytes, lists, JSON, and operation slices.
4. Caller and system clients share request types and result shapes. Only caller
   clients resolve policies and run hooks.
5. Every public mutation executes in a transaction. There is no public raw SQL,
   table name, column name, provider payload, or base connection escape hatch.

## 2. Generated input capabilities

The public core owns sealed marker interfaces and their concrete implementations.
Generated packages expose model-specific methods that return values constructed
through exported, generator-only `Generated...` bridges; they cannot implement
the core's unexported sealing methods because Go scopes those methods to the
declaring package:

```go
type CreateValue[M any] interface { /* sealed model ownership */ }
type UpdateValue[M any] interface { /* sealed model ownership */ }
type UpdateManyValue[M any] interface { /* sealed model ownership */ }
type NestedCreateValue[M any] interface { /* sealed */ }
type NestedUpdateValue[M any] interface { /* sealed */ }
```

Application model packages expose aliases so hook and application code never
spell the generic core types directly:

```go
type PostCreateInput = golem.CreateInput[Post]
type PostUpdateInput = golem.UpdateInput[Post]
type PostUpdateManyInput = golem.UpdateManyInput[Post]
```

Generated namespace constructors produce the inputs:

```go
create := Posts.Create(
    Posts.Title.Create("Hello"),
    Posts.Body.Create("First post"),
    Posts.Author.Connect(Users.ByID.Value(authorID)),
)

update := Posts.Update(
    Posts.Title.Set("Revised"),
    Posts.Views.Increment(1),
    Posts.Subtitle.Null(),
)
```

`Posts.Create` and `Posts.Update` above only build typed values; they do not
perform I/O. Execution occurs on the generated model client.

Each generated field has the narrowest capability type implied by P1 exposure
and logical type:

| Field fact | Generated create/update surface |
| --- | --- |
| hidden, read-only, database-read-only, generated | none |
| immutable writable | `Create`, no update operation |
| ordinary writable | `Create` when legal and `Set` |
| nullable writable | `Create`, `Set`, and `Null` |
| numeric writable | applicable operations plus `Increment`, `Decrement` |
| write-only | mutation methods, no read/filter/projection methods |
| required without default | binder requires one create value exactly once |
| optional/defaulted/runtime-owned | omission is legal |

Duplicate scalar operations for one field are `BAD_USER_INPUT`; their order
does not create an implicit mini-program. Numeric operations are one atomic
database operation against the stored value and are verified from the persisted
after-image.

## 3. Targets and guards

Every generated unique selector value satisfies the sealed target capability:

```go
type MutationTarget[M any] interface { /* sealed selector ownership */ }
```

Examples:

```go
Posts.ByID.Value(postID)
Users.ByTenantAndHandle.Value(tenantID, handle)
```

A target may be narrowed by an ordinary typed predicate:

```go
target := Posts.ByID.Value(postID).And(
    Posts.Version.Eq(expectedVersion),
)
```

`And` never widens the selector. Selector and guard fields are classified as
reads. The guard is conjoined with the action constraint in the locked target
query. A failed guard, missing row, and policy-invisible row all produce the same
single-row `NOT_FOUND` result.

Batch methods take a typed `Predicate[M]`. Mutating every reachable row requires
the explicit value `golem.All[M]()`; an empty or zero predicate is invalid.

## 4. Result projection

P4 makes P3 projection values satisfy an additional sealed capability:

```go
type Projection[M any] interface { /* sealed projection-only read option */ }
```

Generated `Select`, `Include`, and `Omit` return values satisfy both the P3 read
option contract and `Projection[M]`. `Where`, ordering, cursor, skip, take, and
distinct do not satisfy mutation projection.

Single-row mutations return `golem.Row[M]` and accept zero or one projection.
Zero produces an empty typed row. More than one projection is rejected so there
is only one result tree to authorize and execute.

Delete projection is resolved from the locked pre-image. Create and update
projection is resolved from the persisted after-image. The P3 selected/null/
present state and masking behavior is unchanged.

## 5. Generated model clients

The generated caller and system model clients have this operation family:

```go
func (c CallerPostClient[P]) Create(
    ctx context.Context,
    input social.PostCreateInput,
    projection ...golem.Projection[social.Post],
) (golem.Row[social.Post], error)

func (c CallerPostClient[P]) Update(
    ctx context.Context,
    target golem.MutationTarget[social.Post],
    input social.PostUpdateInput,
    projection ...golem.Projection[social.Post],
) (golem.Row[social.Post], error)

func (c CallerPostClient[P]) Delete(
    ctx context.Context,
    target golem.MutationTarget[social.Post],
    projection ...golem.Projection[social.Post],
) (golem.Row[social.Post], error)

func (c CallerPostClient[P]) Upsert(
    ctx context.Context,
    target golem.MutationTarget[social.Post],
    create social.PostCreateInput,
    update social.PostUpdateInput,
    projection ...golem.Projection[social.Post],
) (golem.Row[social.Post], error)

func (c CallerPostClient[P]) UpdateMany(
    ctx context.Context,
    where golem.Predicate[social.Post],
    input social.PostUpdateManyInput,
) (golem.BatchResult, error)

func (c CallerPostClient[P]) DeleteMany(
    ctx context.Context,
    where golem.Predicate[social.Post],
) (golem.BatchResult, error)
```

`SystemPostClient` has the same methods and signatures. It bypasses caller
policy and hooks, but retains schema validation, transactionality, limits,
provider verification, facts, and invalidation.

```go
type BatchResult struct { /* opaque */ }
func (r BatchResult) Count() int64
```

P4 does not return batch rows. The exact bounded row set is internal and, when
configured, represented by one durable fact per row.

Illustrative use:

```go
post, err := caller.Posts.Create(ctx,
    Posts.Create(
        Posts.Title.Create("Hello"),
        Posts.Author.Connect(Users.ByID.Value(authorID)),
        Posts.Tags.Connect(
            Tags.ByName.Value("go"),
            Tags.ByName.Value("sqlite"),
        ),
    ),
    Posts.Select(Posts.ID, Posts.Title, Posts.Author.Select(Users.Handle)),
)
```

## 6. Nested relation surface

Relation handles construct sealed parent-model input values. The generator
removes methods that are impossible for relation direction, cardinality,
requiredness, exposure, or target model input availability.

The accepted vocabulary is:

```go
Posts.Author.Connect(Users.ByID.Value(userID))
Posts.Author.ConnectOrCreate(selector, Users.Create(...))
Posts.Author.Disconnect()                         // optional to-one only
Posts.Author.Update(Users.Update(...))
Posts.Author.Upsert(Users.Create(...), Users.Update(...))
Posts.Author.Delete()

Posts.Tags.Create(Tags.Create(...), Tags.Create(...))
Posts.Tags.CreateMany(Tags.Create(...), Tags.Create(...))
Posts.Tags.Connect(selectorA, selectorB)
Posts.Tags.ConnectOrCreate(selector, Tags.Create(...))
Posts.Tags.Disconnect(selectorA, selectorB)
Posts.Tags.Set(selectorA, selectorB)
Posts.Tags.Update(selector, Tags.Update(...))
Posts.Tags.UpdateMany(predicate, Tags.UpdateMany(...))
Posts.Tags.Upsert(selector, Tags.Create(...), Tags.Update(...))
Posts.Tags.Delete(selector)
Posts.Tags.DeleteMany(predicate)
```

For a to-one relation, selector arguments are omitted where the already-linked
row is the only possible target. For a to-many relation, row-specific update,
upsert, and delete require a target selector owned by the related model.

`CreateMany` is nested-only and is not a provider bulk-payload escape. Each row
is a real mutation node. `Set` computes exact relation membership removals and
additions. Connect/disconnect/set authorize the model whose persisted foreign
key changes; a relation grant alone never authorizes either endpoint.

## 7. Closure transactions

Generated applications expose callback transactions on callers and systems:

```go
err := caller.Transaction(ctx, func(tx *socialapp.CallerTx[Principal]) error {
    post, err := tx.Posts.Create(ctx, Posts.Create(...), Posts.Select(Posts.ID))
    if err != nil {
        return err
    }
    _, err = tx.Comments.Create(ctx, Comments.Create(...))
    return err
})

err = app.System().Transaction(ctx, func(tx *socialapp.SystemTx[Principal]) error {
    // same typed read and mutation families, no caller policy or hooks
    return nil
})
```

`CallerTx` and `SystemTx` expose model clients with P3 reads and P4 mutations.
They do not expose `Transaction`, `System`, `ForPrincipal`, `DB`, `sqlx.Tx`, or
an arbitrary executor. An inner Golem mutation joins the supplied transaction.
Only the outer closure commits or rolls back.

A closure error, panic, cancellation, denial, hook failure, fact failure, or
provider failure rolls back. The runtime converts a recovered callback panic
into rollback and then re-panics; it does not silently turn programmer panic
into a public validation error.

The runtime never replays an application transaction closure. Retriable upsert
interference inside a caller-owned closure is returned as `CONFLICT`.

## 8. Hook ABI

P4 fills the existing generated aliases:

```go
type PostCreateHookRequest = golem.CreateHookRequest[Post]
type PostCreateHookResult = golem.CreateHookResult[Post]
// corresponding update, delete, update-many, and delete-many aliases
```

Before hooks receive a pointer to an owned mutable request facade. Generated
helpers are type-checked and return validation errors:

```go
func (PostHooks) BeforeCreate(ctx context.Context, req *social.PostCreateHookRequest) error {
    return golem.SetCreate(req, Posts.Slug, makeSlug(req))
}
```

The complete helper families mirror legal input operations; a helper cannot
write a field or relation whose generated handle lacks that capability. The
runtime rebinds and reclassifies the transformed request before SQL.

Transaction-after results expose immutable snapshots and metadata:

```go
func (r CreateHookResult[M]) Row() Row[M]
func (r UpdateHookResult[M]) Before() Row[M]
func (r UpdateHookResult[M]) After() Row[M]
func (r DeleteHookResult[M]) Before() Row[M]
func (r UpdateManyHookResult[M]) Count() int64
func (r DeleteManyHookResult[M]) Count() int64
```

Typed helper functions accept the result's opaque transaction executor for
additional Golem operations. No method returns `*sqlx.Tx` or accepts SQL.
Hook-started writes inherit the actor, policy set, limits, touched graph, fact
buffer, and transaction.

After-commit hooks receive the same immutable result shape without a transaction
executor. When such hooks are registered, application configuration must supply:

```go
AfterCommitError func(context.Context, golem.AfterCommitFailure)
```

```go
type AfterCommitFailure struct { /* opaque */ }
func (f AfterCommitFailure) Operation() HookOperation
func (f AfterCommitFailure) Model() ModelID
func (f AfterCommitFailure) Cause() error
```

The failure handler is trusted application observability. Hook failure does not
change a committed mutation into an error result. System mutations run no
hooks. There is no distinct upsert hook; only the committed branch's create or
update hook family applies.

## 9. Limits and configuration

P4 adds this runtime value to generated application configuration:

```go
type MutationLimits struct {
    MaxNestedDepth       int
    MaxTouchedRows       int
    MaxFacts             int
    MaxOutboxBytes       int
    MaxStatementParameters int
    MaxUpsertAttempts    int
}
```

Zero selects the portable defaults and hard ceilings: depth 5, 1,000 touched
rows, 1,000 facts, 1 MiB encoded facts, 999 statement parameters, and three
engine-owned upsert attempts. Applications may lower but not raise them.

## 10. Stable errors and public outcomes

P4 adds `CodeConflict` with serialized value `CONFLICT` to the P3 error family.
The public distinctions are:

| Outcome | Code |
| --- | --- |
| invalid input/shape/limit/hook veto | `BAD_USER_INPUT` |
| missing, guarded-out, or caller-invisible single target | `NOT_FOUND` |
| principal resolution failure | `UNAUTHENTICATED` |
| row or field authorization denial | `FORBIDDEN` |
| uniqueness/interference/retry exhaustion/set instability | `CONFLICT` |

Provider messages, SQL, physical identifiers, constraint names, policy
structure, hidden-row existence, and selector values are never public details.

## 11. Compile contract

Fresh generated-module fixtures must compile accepted social-model programs and
must fail to compile programs that attempt any of the following:

- assign a field or selector from another model;
- mutate hidden, read-only, database-read-only, or generated fields;
- update immutable fields;
- null a non-null field;
- apply numeric operations to non-numeric fields;
- use relation operations forbidden by cardinality or requiredness;
- pass a to-many shape to a to-one relation or omit the required to-many target;
- use a read option other than projection as a mutation projection;
- pass a zero/ordinary struct as a generated input or target;
- call transaction/system/base-database methods through a Tx client; or
- use arbitrary SQL through a hook result.

Runtime tests separately prove that copied zero values, malformed generated
values, duplicate operations, required omissions, and limit violations fail
before partial effects.
