# P3 public read ABI

Status: **controlling contract for P3 implementation**

## 1. Typed result state

```go
type ReadState uint8

const (
    ReadUnselected ReadState = iota
    ReadNull
    ReadPresent
)

type ReadValue[V any] struct { /* opaque */ }

func (v ReadValue[V]) State() ReadState
func (v ReadValue[V]) Get() (V, bool)
func (v ReadValue[V]) IsSelected() bool
func (v ReadValue[V]) IsNull() bool
```

`ReadNull` covers both database null and authorization masking at the public
boundary. The internal plan retains the reason for observability; the public
value does not disclose whether a null came from storage or policy.

Rows remain opaque and model typed:

```go
type Row[M any] struct { /* opaque */ }

func Value[M, V any](Row[M], ScalarColumn[M, V]) ReadValue[V]
func One[M, R any](Row[M], ToOne[M, R]) ReadValue[Row[R]]
func Many[M, R any](Row[M], ToMany[M, R]) ReadValue[[]Row[R]]
func RelationCount[M, R any](Row[M], ToMany[M, R]) ReadValue[int64]
```

Slices, bytes, JSON, and nested rows returned through accessors are detached from
internal storage. No string-keyed public field accessor exists.

## 2. Selection and request construction

Generated scalar handles satisfy the sealed `Selection[M]` capability. Generated
relation handles construct model-correct nested selections. Generated namespace
methods provide the intended spelling:

```go
Posts.Select(
    Posts.ID,
    Posts.Title,
    Posts.Author.Select(Users.ID, Users.Handle),
    Posts.Comments.Args(
        Comments.Where(Comments.DeletedAt.IsNull()),
        Comments.OrderBy(Comments.CreatedAt.Asc()),
        Comments.Take(20),
        Comments.Select(Comments.ID, Comments.Body),
    ),
)
```

A to-one relation exposes only selection/include/omit construction. It has no
where, order, cursor, skip, take, or distinct methods. A to-many relation may
carry the bounded read arguments.

`select` is an allow-list. `include` starts from the model's default visible
scalar set and adds relations. `omit` subtracts visible scalars. `select` and
`include`/`omit` cannot be combined at the same projection node. Duplicate fields
or relations are invalid rather than last-write-wins.

## 3. Read options

The generated namespace constructs sealed options owned by one model:

```go
Posts.Where(Predicate[Post])
Posts.OrderBy(Posts.CreatedAt.Desc(), Posts.ID.Asc())
Posts.Cursor(Posts.ByID.Value(id))
Posts.Skip(n)
Posts.Take(n)
Posts.Distinct(Posts.AuthorID)
Posts.Select(...)
Posts.Include(...)
Posts.Omit(...)
```

Compound selectors expose generated value constructors whose argument order and
types are fixed by the identity descriptor. A zero or forged selector is rejected
again at runtime.

## 4. Generated clients

Application generation emits an `App`, explicit `System`, `Caller`, and one
model client per generated model. Illustrative final use:

```go
caller, err := app.ForPrincipal(ctx, principal)
if err != nil { return err }

rows, err := caller.Posts.FindMany(ctx,
    Posts.Where(Posts.Published.Eq(true)),
    Posts.OrderBy(Posts.CreatedAt.Desc()),
    Posts.Take(20),
    Posts.Select(Posts.ID, Posts.Title),
)

title := golem.Value(rows[0], Posts.Title)
```

`FindUnique` accepts exactly one generated unique selector and one projection.
`FindFirst` and `FindMany` accept their validated option sets. `Count` accepts a
where constraint only; it does not expose P6 aggregate/grouping options.

The system model clients expose the same typed request vocabulary but do not
construct or apply caller policy and do not run caller hooks.

## 5. Hooks

P3 fills the existing `FindOneHookRequest/Result`, `FindFirstHookRequest/Result`,
and `FindManyHookRequest/Result` shells without renaming generated aliases.
Before hooks may return a replacement immutable read request or veto it; they do
not receive raw mutable SQL. After hooks observe the public decoded result and
may veto return. Read hooks execute inside the caller execution and never on the
system client. Mutation hook payloads remain P4 shells.

## 6. Runtime read limits

Generated application configuration exposes the public runtime limit contract
without copying its definition:

```go
type Config[P any] struct {
    DB               *sqlx.DB
    Provider         golem.Provider
    ReadLimits       runtime.ReadLimits
    ResolvePrincipal func(context.Context, P) (Actor, error)
    SnapshotActor    func(Actor) (Actor, error)
}

type ReadLimits struct {
    MaxTake                int
    MaxRelationFanout      int
    MaxRelationDepth       int
    MaxSelectedFields      int
    MaxStatementParameters int
    MaxStatementBytes      int
    MaxStatementAliases    int
    MaxLoaderKeys          int
}
```

`MaxTake == 0` and `MaxRelationFanout == 0` mean deliberately
unconfigured/unlimited. They do not acquire a hidden default. Zero selects the
safe runtime default for every other field: depth 5, 256 selected/private fields,
999 parameters, 1 MiB statement text, 2,048 aliases, and 90,000 loader keys.
Configured complexity values may lower but cannot raise those portable hard
ceilings.

Root and to-many caps refuse overflow; they do not silently truncate. With no
explicit `Take`, execution fetches one sentinel row beyond the effective cap and
returns `BAD_USER_INPUT` on overflow. Relation overflow is checked per parent for
both loading strategies. An explicit page at or below the cap is returned
normally. A model contract's non-zero `MaxTake` participates in the same check,
and the stricter non-zero runtime/schema value wins.

## 7. Stable errors

```go
type ErrorCode string

const (
    CodeBadUserInput   ErrorCode = "BAD_USER_INPUT"
    CodeNotFound      ErrorCode = "NOT_FOUND"
    CodeUnauthenticated ErrorCode = "UNAUTHENTICATED"
    CodeForbidden     ErrorCode = "FORBIDDEN"
)
```

The public error exposes code, logical operation/model/field where safe, and a
stable message. Provider errors and physical facts remain wrapped for trusted
logging but are not serialized as public detail.
