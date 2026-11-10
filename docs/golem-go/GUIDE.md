# Building with Golem

[QUICKSTART.md](./QUICKSTART.md) gets one model running. This page covers what
you need for a real application: relations, authorization, queries, and
mutations. Its schema, policies and program are executed by
`TestGuideApplicationRuns`, so the code here is code that ran.

## Declaring models

A model is a Go struct. Blank fields carry table-level declarations; real
fields carry column ones.

```go
// notes/schema.go
package notes

import (
	"time"

	"github.com/eleven-am/golem/go/golem"
)

type Actor struct {
	UserID        golem.UUID
	Authenticated bool
}

type Author struct {
	_ struct{} `golem:"model;id=example.notes.Author;table=authors;graphql=Author"`
	_ struct{} `golem:"unique=uq_authors_handle(handle)"`

	ID     golem.UUID `db:"id" golem:"id=example.notes.Author.ID;pk;default=uuid"`
	Handle string     `db:"handle" golem:"type=varchar(40)"`
	Notes  []Note     `db:"-" golem:"relation=has_many;fields=id;references=author_id"`
}

type Note struct {
	_ struct{} `golem:"model;id=example.notes.Note;table=notes;graphql=Note"`
	_ struct{} `golem:"index=idx_notes_author(author_id,id)"`

	ID        golem.UUID `db:"id" golem:"id=example.notes.Note.ID;pk;default=uuid"`
	AuthorID  golem.UUID `db:"author_id" golem:"id=example.notes.Note.AuthorID"`
	Title     string     `db:"title" golem:"type=varchar(200)"`
	Published bool       `db:"published"`
	CreatedAt time.Time  `db:"created_at" golem:"default=now;readonly"`
	Author    *Author    `db:"-" golem:"relation=belongs_to;fields=author_id;references=id"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "notes")
	golem.Actor[Actor](schema)
	golem.Model[Author](schema)
	golem.Model[Note](schema)
	golem.Providers(schema, golem.SQLite)
}
```

### The tag vocabulary

On a blank `_ struct{}` field:

| Key | Meaning |
|---|---|
| `model` | marks the struct as a model |
| `id=` | the canonical identity, stable across renames |
| `renameFrom=` | the identity the model had in the previous reviewed schema, for a model without `id=`; the change is then a rename, not a drop and an add. It cannot be combined with `id=` |
| `table=` | physical table name |
| `graphql=` | GraphQL type name |
| `primary=name(cols)` | composite primary key |
| `unique=name(cols)` | unique constraint |
| `index=name(cols)` | index |

On a real field:

| Key | Meaning |
|---|---|
| `id=` | canonical field identity |
| `renameFrom=` | the field's identity in the previous reviewed schema, for a field without `id=` |
| `pk` | primary key |
| `unique` | single-column unique constraint |
| `default=uuid` / `default=now` | value golem supplies on create when the field is not written; no column `DEFAULT` is emitted |
| `default=identity` | a database identity column; signed integer fields only |
| `default=<literal>` | a column `DEFAULT` in the database, such as `default=0`, `default=false` or `default=PUBLIC` for an enum |
| `readonly` | cannot be written by any caller |
| `updated` | set to the mutation time on every update of the row, including one whose values equal the stored ones |
| `immutable` | writable on create, never on update |
| `system` | written only by hooks and the system client; a caller's input that names it is refused |
| `type=` | physical column type |
| `hidden` | excluded from the generated API |
| `internal` | kept in the Go and system API, absent from GraphQL; valid on columns and relations |
| `writeonly` | writable, never readable |
| `graphql=` | GraphQL field name |
| `relation=has_many` / `has_one` / `belongs_to` | relation kind |
| `fields=` / `references=` | the local and foreign columns joining it |
| `name=` | pairs the two sides of a relation when a model has more than one relation to the same target, such as a self-relation |

Use `hidden` for a column no code should touch through golem, and `internal`
for a column or relation your own Go code must still read, filter or look up by
but no client may see: an API key hash is a non-null `string` field tagged `golem:"unique;internal;system"`,
found with `FindUnique(ctx, Keys.ByKeyHash.Value(hash))` in authentication middleware and absent from every GraphQL type,
input, enum, subscription payload and the client manifest. A `By<Field>` selector is generated only for a non-null unique field, never for a `golem.Null[T]` one. `internal` combines
with everything except `hidden`, and a primary key cannot be `internal`
(`P1_GOLEM_TAG_INTERNAL_PRIMARY_KEY`). On a model with a GraphQL create root, a
required internal field with no default must also be `system` or listed in
`GraphQLHookOwned`, or `golem generate` refuses it
(`P8_GRAPHQL_INTERNAL_CLIENT_REQUIRED`).

`internal` on a relation is field-local, like `hidden`. It removes that one
relation field from the GraphQL object, its `where` and `orderBy` inputs, the
relation inputs of create and update, the event payload and the client
manifest. The generated Go handle, selection, filter and write surface stay.
It does not hide the other side of the relation or the foreign-key columns, and
a relation dimension of an analytics root cannot pass through it. A relation
with no `internal` on its other side is still visible from that side, and its
manifest entry then has no inverse.

To keep a link out of every client, mark both relation fields and the
foreign-key column:

```go
type Child struct {
	_ struct{} `golem:"model;table=children"`

	ID       golem.UUID `db:"id" golem:"pk"`
	ParentID golem.UUID `db:"parent_id" golem:"internal;system"`
	Parent   *Parent    `db:"-" golem:"relation=belongs_to;fields=parent_id;references=id;internal"`
}

type Parent struct {
	_ struct{} `golem:"model;table=parents"`

	ID       golem.UUID `db:"id" golem:"pk"`
	Children []Child    `db:"-" golem:"relation=has_many;fields=id;references=parent_id;internal"`
}
```

`internal` on a relation does not change its foreign key. A visible
`ParentID` stays in `ChildCreateInput`, and an internal, required `ParentID`
with no default follows the rule above (`P8_GRAPHQL_INTERNAL_CLIENT_REQUIRED`
unless it is `system` or hook-owned). `internal` and `hidden` cannot be combined
on a relation (`P1_EXPOSURE_MODE_CONFLICT`).

`db:"-"` marks a field as having no column of its own, which every relation
needs. `relation=many_to_many` is refused
(`P1_RELATION_IMPLICIT_M2M_UNAVAILABLE`): declare the join model and two
`belongs_to` relations instead. The `through=`, `source=` and `target=` keys
parse but have no effect without it.

### Enums and checks

An enum is a string type with a `GolemEnum` method. A field of that type
stores text, and the migration adds a check constraint that admits only the
declared values:

```go
type Visibility string

const (
	VisibilityPublic  Visibility = "PUBLIC"
	VisibilityPrivate Visibility = "PRIVATE"
)

func (Visibility) GolemEnum() golem.EnumSpec[Visibility] {
	return golem.DefineEnum(
		golem.EnumValue(VisibilityPublic),
		golem.EnumValue(VisibilityPrivate),
	)
}
```

`golem.EnumValue(value, golem.GraphQLValue("NAME"))` changes the value's
GraphQL name. Other row invariants are declared as checks in the model's
`GolemModel` method:

```go
func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel(
		golem.Check("ck_posts_views", Posts.Views.Expr().GTE(0)),
		golem.Check("ck_posts_private_unpublished",
			Posts.Visibility.Expr().Ne(VisibilityPrivate).Or(Posts.Published.Expr().Eq(false))),
	)
}
```

A check is a predicate over the row's own fields, built from `Expr()` and
combined with `And`, `Or` and `Not`. The database enforces it, so a write that
violates it fails whoever makes it. PostgreSQL stores some checks, including
enum checks, in a rewritten form; startup and `golem doctor` recognise it, and
still report a check whose values or columns were changed by hand as drift.

## Authorization

Every exposed model must declare a policy, and there is no implicit allow.
A policy receives the resolved actor and grants against predicates.

```go
// notes/policies.go
package notes

import "github.com/eleven-am/golem/go/golem"

func (Author) DefinePolicy(rules *golem.Rules[Author], actor Actor) {
	rules.CanRead(golem.All[Author]())
	if actor.Authenticated {
		rules.CanCreate(golem.All[Author]())
	}
}

func (Note) DefinePolicy(rules *golem.Rules[Note], actor Actor) {
	published := Notes.Published.Eq(true)
	if !actor.Authenticated {
		rules.CanRead(published)
		return
	}
	own := Notes.AuthorID.Eq(actor.UserID)
	rules.CanRead(published.Or(own))
	rules.CanCreate(own)
	rules.CanUpdate(own)
	rules.CanDelete(own)
}
```

`Notes` and `Authors` are generated accessors. A policy is a *predicate*, not a
callback: golem compiles it into the SQL of every query, so an unauthorized row
is never read rather than read and filtered.

`rules.CanReadFields(predicate, Notes.Title)` and `CannotReadFields` narrow
authorization to individual columns.

### Operations that own a write

Some writes belong to one domain operation. Say an invite must have its
address normalised and its status start at `pending`. Callers should reach
that only through an `inviteMember` mutation, never through `createInvite` or
`caller.Invites.Create`. The operation owns the invariant, and the policy
closes the raw surface: grant no `CanCreate` to callers, then grant the create
to the operation alone with `golem.Within`:

```go
func (Invite) DefinePolicy(rules *golem.Rules[Invite], actor Actor) {
	owned := Invites.Owner.Eq(actor.ID)
	rules.CanRead(owned)
	rules.CanUpdate(owned)
	rules.CannotUpdateFields(golem.All[Invite](), Invites.Status)
	golem.Within(rules, InviteMember).CanCreate(owned)
	golem.Within(rules, AcceptInvite).CanUpdateFields(owned, Invites.Status)
}
```

`InviteMember` and `AcceptInvite` are the resolvers of custom mutations
declared with `golem.Mutation` (see [Custom operations](#custom-operations)).
A Within grant applies only while that resolver runs, whether it was called by
GraphQL dispatch or through `Mutate`. Outside it, the generic `createInvite`
root, `caller.Invites.Create`, and a direct call to `InviteMember(ctx, caller,
args)` are refused with exactly the error they get when no Within grant
exists. Nothing in the refusal reveals that a grant exists.

Inside the operation, a write is allowed when a Within grant covering it
allows it, or when the caller's own policy does:

- A model-wide grant (`CanCreate`, `CanUpdate`, `CanDelete`) covers every field
  of the rows its predicate matches.
- A field grant (`CanCreateFields`, `CanUpdateFields`) covers only the fields it
  names.
- Every other write is decided by the caller's policy alone. `AcceptInvite`
  above may set `Status`, but a `CannotUpdateFields` on any other field still
  refuses it.
- Field modes still bind: a `readonly`, `system` or `immutable` field stays
  closed whatever a Within grant says.

A Within grant never widens row reads, and `Within` offers no `Cannot*` verbs.
`CanCount` and `CanAggregate` widen only aggregate results, and only inside the
operation (see [Counts and aggregates an operation owns](#counts-and-aggregates-an-operation-owns)).
The resolver still runs as the caller. Its predicates still bind, its hooks still
run, its events are delivered only to readers the policy allows, and it may
link only to rows the caller can read. Nested writes, upserts and transactions
opened inside the resolver carry the grant. It ends when the resolver returns:
a caller the resolver kept, or a goroutine that outlives it, from then on
writes only what the caller's own policy allows. Two custom mutations in one
GraphQL document each receive only their own grants.

A write made through the operation's caller that reaches its commit after the
operation ended is judged when golem plans it. For every row and field decision
the write needs, golem compares the operation's grants with the caller's own
policy.

- **Refused with CONFLICT.** If the write's authorisation may have depended on a
  Within grant, the write is rolled back and fails with the ordinary retryable
  `CONFLICT` it reports when it conflicts, such as `CONFLICT: mutation
  conflicted` for a create or `CONFLICT: batch mutation conflicted` for an
  `UpdateMany`. This covers every write the caller's own policy refuses, and
  every write where golem cannot prove the grant adds nothing. Only the row
  decides such a case, so golem refuses it. Retry to run the write under the
  caller's own policy.
- **Commits normally.** A write the caller's own policy clearly allows is one
  where the grant is proved to add nothing to any decision it needs.

Ending the operation waits only for a commit already under way. After a refusal
golem runs nothing further: no hook runs again and nothing is re-planned or
written. Hooks that ran inside the rolled-back transaction ran exactly once,
and after-commit hooks never run. Each write attempt is judged on its own, including a
write a hook makes and one nested inside it. A write that returns an error has
no effect. A write a hook makes through its executor runs in its own savepoint.
If it fails, golem rolls back its rows, events and after-commit hooks, so the
hook can handle the error and the transaction stays usable. A failed attempt
therefore takes no part in this rule: it cannot make the write around it, or
its transaction, fail later.

A hook's executor is valid only while that hook runs. When the hook returns,
golem waits for any call already in flight on the executor, then refuses every
later call, from any goroutine. A call through a hook's executor always runs
inside the write that called the hook, whatever context you pass it. A call
through the transaction itself with the hook's own context also runs inside
that write. Both belong to the hook: when the hook returns, golem waits for any
such call already started before the write continues, so finish those calls
before the hook returns if the write depends on them. A call made later with
that context no longer belongs to the hook and runs as a call of its own. The
hook's calls run one at a time, like every call on a transaction (see
[Transactions serve one call chain at a time](#transactions-serve-one-call-chain-at-a-time)):
inside a hook, a call with any other context, or a second call started while
one through the same hook is still running, fails at once with
`P4_RUNTIME_TRANSACTION: transaction used concurrently; a transaction serves
one call chain at a time`. It never waits, so a hook can never block on its
own transaction. A nested hook may call an outer hook's executor: that call is
inside the chain and runs inside the nested write. A caller or system
transaction accepts reads and writes only until its callback returns, or
panics: golem waits for every call already in flight, refuses later ones, and
only then flushes, commits or rolls back.

`Within` must name a generated custom mutation or transaction operation. Any
other function, including a custom query resolver, fails the build of that
model's policy, with an error naming it. Policies are built per model on first
use (see [Policies are cached per actor](#policies-are-cached-per-actor)), so
the caller is created and every other model works; each call that needs the
broken model's policy is refused with `UNAUTHENTICATED: principal policy could
not be established`, and the cause names the function. Prefer `Within` to
`SystemEscape` for a write an operation owns: `SystemEscape` skips every rule,
predicate and hook, while `Within` grants one verb on the rows you name.

### Operations on a transaction

A custom mutation receives a caller and opens its own transaction. Go code that
already holds a caller transaction, and needs a write only an operation may
make, declares a transaction operation instead. It is not a GraphQL root and
works in [kernel mode](#using-golem-from-go-without-graphql-kernel-mode):

```go
type RetitleNoteArgs struct {
	ID    golem.UUID
	Title string
}

func DefineOperations(operations *golem.Operations) {
	golem.TxOperation(operations, "retitleNote", RetitleNote)
}

func RetitleNote(ctx context.Context, tx *CallerTx[Principal], args RetitleNoteArgs) (string, error) {
	updated, err := tx.Notes.Update(ctx, Notes.ByID.Value(args.ID),
		Notes.Update(Notes.Title.Set(args.Title)), Notes.Select(Notes.Title))
	if err != nil {
		return "", err
	}
	title, _ := golem.Value(updated, Notes.Title).Get()
	return title, nil
}

func (Note) DefinePolicy(rules *golem.Rules[Note], actor Actor) {
	own := Notes.AuthorID.Eq(actor.UserID)
	rules.CanRead(own)
	golem.Within(rules, RetitleNote).CanUpdateFields(own, Notes.Title)
}
```

`Principal` here is your application's principal type, declared in the schema
package. `DefineOperations` lives in the schema package and holds only
`golem.TxOperation` calls. Each names a package-level function of the schema
package with the signature `func(context.Context, *CallerTx[P], Args) (R,
error)` and an operation name that is a Go identifier, unique in the schema.
Mistakes fail `golem generate` with a `P11_OPERATION_*` diagnostic.

Run one on a caller transaction with the generated `Run`:

```go
err := caller.Transaction(ctx, func(tx *notes.CallerTx[Principal]) error {
	_, err := notes.Run(ctx, tx, notes.RetitleNote, notes.RetitleNoteArgs{ID: id, Title: title})
	return err
})
```

- The operation runs on the transaction you pass, so its writes commit or roll
  back with everything else in it. An error the operation returns does not roll
  anything back by itself; return it from the callback to do that.
- The grant is visible only while the operation runs, and only through the
  transaction handle the operation receives. The outer `tx` never gains it.
- Writes made inside the operation keep their grant until the transaction
  commits or rolls back, so they are not refused at commit as late custom
  mutation writes can be.
- A handle the operation kept, a call made with a fresh context, or a
  goroutine that outlives it is refused once it returns, with
  `P5_CUSTOM_OPERATION: operation has ended`.
- A `Run` nested inside another operation receives only its own grants; grants
  never accumulate. A panic in the operation closes it and is re-raised.
- `Run` refuses a custom mutation and `Mutate` refuses a transaction operation,
  both with `P5_CUSTOM_OPERATION`. Runs are observed as `mutation.operation`.

### Counts and aggregates an operation owns

A quota or budget check often has to count rows the caller cannot read: every
asset uploaded to an artifact the caller owns, or every invite the caller has
sent. Grant that count to a transaction operation, declared with
`golem.TxOperation`, instead of reaching for `SystemEscape`:

```go
func (Asset) DefinePolicy(rules *golem.Rules[Asset], actor Actor) {
	rules.CanRead(Assets.Owner.Eq(actor.ID))
	golem.Within(rules, CommitAssetBudget).CanAggregate(Assets.Artifact.Is(Artifacts.Owner.Eq(actor.ID)), Assets.Size, Assets.ArtifactID)
}

func (Invite) DefinePolicy(rules *golem.Rules[Invite], actor Actor) {
	rules.CanRead(Invites.Email.Eq(actor.Email))
	golem.Within(rules, CountPendingInvites).CanCount(Invites.Owner.Eq(actor.ID).And(Invites.Status.Eq("pending")))
}
```

Inside `Run(ctx, tx, CommitAssetBudget, args)`, `Count` and `Aggregate` on the
operation's transaction see the caller's own rows plus the rows the grant
predicate matches. Relation hops inside the statement use the same operation's
aggregate grants on the target model, so a grant on `Collections` widens a
`Documents` count that reaches through it.

- `CanCount(predicate)` widens rows only. A count over those rows may not filter
  or measure any field the caller could not already use on its own rows.
- `CanAggregate(predicate, field, more...)` takes one or more fields and also lets them be measured
  and filtered on. Naming a field discloses its values over the granted rows:
  `MIN` and `MAX` return them exactly, and filters on named fields can single
  out rows. Name only the fields the operation must aggregate.
- A filter or measure on any field the grant does not name is judged by the
  caller's own field policy. It is refused with `FORBIDDEN` unless the
  statement provably reaches only rows where the caller may already read that
  field.

The grant never applies to `FindMany`, `FindFirst`, `FindUnique`, includes,
`GroupBy` (group keys are values), events, query-plan explanations, writes or
link checks, inside the operation or out. An `Explain` call made inside `Run`
describes the caller's own, narrower statement, not the widened count. A base
`Cannot*` rule does not narrow an aggregate grant; put the exclusion in the
grant predicate. Outside `Run`, and once the operation
returns, counts are exactly the caller's own. `CanCount` and `CanAggregate`
must name a generated transaction operation: naming a custom mutation or any
other function fails the policy build. The safety rests on the operation's code
choosing what to count; the caller supplies only arguments, so write the
operation so its arguments cannot turn a count into a probe.

### Unique keys, cascades and concurrent writes

A unique constraint is the one place a write can reveal a row you cannot read.
Suppose you create or update a row and the value collides with a unique key or
primary key held by a row outside your read policy. The database still rejects
the write, and golem reports it as `CONFLICT`, which tells you the value is
taken. That is inherent to the constraint. The collision is checked across
every row, and hiding it would mean accepting a write that cannot be stored. If
the taken values themselves are secret, such as emails or handles, make the key
opaque, scope it with a composite key that includes the owner, or route the
write through a system-client flow that answers the same way either way.
Foreign keys do not leak like this: you can link only to a row you can read,
and golem reports a row you cannot read exactly as a row that does not exist.

`RelationOptions(...).OnDelete(golem.Cascade)` tells golem that a dependent
row's lifetime belongs to its parent. If you may delete the parent, that
permission covers every row the cascade removes, including rows owned by other
users and rows you cannot read. `SetNull` works the same way: deleting the
parent clears the reference on each dependent without checking a policy on
that row. A dependent that another write removes first is simply no longer
affected. If another write adds a row the cascade would remove while the delete
runs, the delete fails with `CONFLICT` and nothing changes; retry it. It emits
a change event for each affected row: a deleted event for every cascaded row and an
updated event for every cleared reference, in the same transaction as the
delete. Each subscriber still receives only the events its own read policy
allows. A delete event is judged against the deleted row's stored snapshot
and, for each related parent its policy reaches, against that parent's
current state, unless the same transaction changed it. A parent the same
transaction also deleted is judged from its own delete snapshot. A parent the same transaction created
or updated, or whose state cannot be established exactly, ends that
subscriber's stream with `GOLEM_SUBSCRIPTION_RESYNC` instead of guessing;
reload and subscribe again. A parent changed by a later transaction, before
the event was delivered, is judged on its state at delivery, as created and
updated events are; when the event publisher falls behind, that window grows
with the backlog. A CDC source's batches are recorded in the outbox before they
are published, so a delete delivered from a CDC source is decided the same way,
against the other changes of its source transaction. If retention has already
removed that batch's facts, a policy whose verdict depends on a related parent
ends the stream with `GOLEM_SUBSCRIPTION_RESYNC`, while a policy decided by the
row's own fields still decides it exactly. A delete whose cascade would
touch more rows than `MutationLimits.MaxTouchedRows` allows (1,000 by default) is refused.

On PostgreSQL, concurrent writes that touch the same rows do not deadlock. One
completes, and the other either waits for it or fails at once with `CONFLICT`.
Retry a write that fails with `CONFLICT`. An upsert, or a nested write
containing an upsert or connectOrCreate, retries by itself before reporting
`CONFLICT`, except inside your own transaction, which golem never replays.

An `UpdateMany` or `DeleteMany`, including one nested in another write, changes
exactly the rows that match its filter while it holds them, as a plain SQL
`UPDATE` or `DELETE` does. A row another write deleted first, or changed so
that it no longer matches, is left out. A row that starts to match after the
write selected its rows is left alone. The returned count, the hooks and the
change events cover exactly the rows changed. A single update, delete or upsert
target that another write deleted first, or changed so that it no longer
matches, is treated as missing: `NOT_FOUND` for an update or delete, and the
create branch for an upsert.

`CONFLICT` is most likely between transactions that each write several of the
same rows, or that both reference a parent row and then both update it. If your
transaction writes several rows that other writers also write, write them in
the same order in every transaction, and retry the whole transaction when it
fails with `CONFLICT`. Two transactions that write the same rows in different
orders do not wait for each other: one fails at once with `CONFLICT`. SQLite runs one writer at a time, so none of this applies there.

## How policies are proved

### Relation checks see only rows the caller can read

A relation check in a policy or a filter ranges only over target rows the
caller can read. Before planning, golem rewrites every hop `Rel.Is(X)` to
`Rel.Is(readable ∧ X)`, where `readable` is the target model's read rule for
the same actor, expanded the same way. This holds everywhere a policy is
applied: read rules, field rules, create, update and delete rules, a caller's
`Where`, SQL field masks, and the evaluation of delete events.

So a rule does not have to restate the target's read rule:

```go
func (Comment) DefinePolicy(rules *golem.Rules[Comment], actor Actor) {
	rules.CanRead(Comments.Post.Is(Posts.Priority.Eq(0)))
}
```

matches comments whose post the caller can read and whose priority is 0. Each
form of relation check reads accordingly:

| Check | True when |
|---|---|
| `Rel.Is(X)`, `Rel.Some(X)` | a readable related row matches `X` |
| `Rel.IsNotNull()` | a readable related row exists |
| `Rel.IsNull()` | no readable related row exists, including when the reference points at a row the caller cannot read |
| `Rel.IsNot(X)`, `Rel.None(X)` | no readable related row matches `X` |
| `Rel.Every(X)` | every readable related row matches `X` |

A row the caller cannot read is indistinguishable from one that does not
exist, in every position. That is what lets golem compile a negative check
without revealing anything through it.

### Field masks

`CanReadFields` and `CannotReadFields` make a field readable on some rows and
masked on others. The mask is evaluated in SQL when the row is read, and its
relation checks follow the same rule: a mask that tests a related row the
caller cannot read sees no row there. The SQL and the in-process evaluation
agree, and a masked value is absent from the result exactly as an unselected
one is.

A field the caller can never read cannot be used to filter, order, page or
select a unique row. A field the caller can read only on some rows can be used
when the statement provably reaches only those rows. Otherwise the read is
refused with `FORBIDDEN` and reason `FIELD_NOT_READABLE`.

### Write rules

An update, delete or upsert targets rows through a selector or a filter. golem
plans it over the action rule, the selector, any guard, and the read
condition of the rows it touches, and skips a condition the selection already
proves. That has four consequences:

- **Writes narrow to readable rows.** An update or delete rule does not need to
  repeat the read rule. A rule wider than the read rule, such as
  `CanDelete(golem.All[Note]())`, is narrowed to the rows the caller can read.
  A single update or delete of a row the caller cannot read, or one its rule
  does not match, fails with `NOT_FOUND`, as for a row that does not exist.
  `UpdateMany` and `DeleteMany` change only readable, matching rows, and count
  only those. Nested writes follow the same rule.
- **A missing grant is refused before planning.** A caller with no
  `CanUpdate` or `CanDelete` grant at all on the model is refused with
  `FORBIDDEN` and reason `NOT_GRANTED`, whichever row it names. When golem
  cannot establish that the rows a write locates are readable, for example
  because the caller has a delete grant and no read grant on the model, the
  write fails with `FORBIDDEN` and reason `AUTHORIZATION_UNPROVEN`.
- **An upsert's create branch needs a create grant.** When the target does not
  exist, or the caller cannot read it, the upsert takes its create branch. A
  caller with no create grant is refused there with `FORBIDDEN` and
  `NOT_GRANTED`, before any hook runs or any row is inserted. A missing row
  and an unreadable one therefore answer alike, so an upsert cannot be used to
  test whether a row exists. `ConnectOrCreate` follows the same rule.
- **Relation checks in write rules see only readable rows.** A rule such as
  `CanDelete(Notes.Author.Is(Authors.Handle.Eq("ada")))` cannot be used to
  learn anything about an author the caller cannot read: the outcome is the
  same whatever that author's handle is.

### When a policy cannot be applied

A policy can be valid Go and still unusable for a request. golem refuses such
a request with `FORBIDDEN` and reason `POLICY_INVALID`, before any data is
read:

- **A cycle.** Expanding a relation check reaches a model whose own read rule
  is already being expanded on that path, and that rule contains a relation
  check. A `Post` rule that reads through `Author`, whose rule reads back
  through `Posts`, is a cycle. A model re-entered with a rule that has no
  relation checks, which is common with self-relations, is fine.
- **Depth.** The expansion needs more relation hops than
  `ReadLimits.MaxRelationDepth` (5 by default).
- **Size.** The fields the policy alone needs exceed a statement limit, such as
  `ReadLimits.MaxSelectedFields`. A limit your own request exceeds is
  `LIMIT_EXCEEDED` instead (see [Limits and error codes](#limits-and-error-codes)).

Break a cycle by testing a foreign key directly, such as
`Posts.AuthorID.Eq(actor.UserID)`, rather than a relation whose target rule
leads back.

## Callers and the system client

```go
caller, err := application.ForPrincipal(ctx, Principal{UserID: id, Authenticated: true})
system := application.System()
```

`ForPrincipal` resolves your principal into an `Actor` and applies policy to
everything it does. `System()` bypasses policy entirely — use it for seeding,
migrations and background work, never for a request.

### Policies are cached per actor

`ForPrincipal` runs `ResolvePrincipal` on every call, so revoking a principal
takes effect on the next call. The policies built from the resulting actor are
cached: two callers whose actors are equal share one built policy set, and
each model's `DefinePolicy` runs at most once per distinct actor, on the first
call that needs that model.

That makes a contract of something golem could not previously rely on:
**`DefinePolicy` must be a pure function of the actor.** It must not read the
clock, the database, a global, or anything the actor does not carry. Put
whatever the policy depends on into the actor, through `ResolvePrincipal`.
Code that breaks the contract is frozen at its first result for that actor.

- **The key.** By default the cache key is the actor's value, compared field by
  field with exact float bits and dynamic interface types. An actor holding a
  pointer, slice, map, channel or function cannot be keyed that way, and its
  policies are built on every call. When `Config.SnapshotActor` is set, the
  actor may be a copy the default key cannot vouch for, so golem caches only
  when you also set `Config.PolicyCacheKey`, a `func(Actor) (string, bool)`
  that returns a key, or `false` to skip the cache for that actor.
- **Bounds.** `Config.PolicyCache` takes `MaxActors` (1,024 by default), an
  optional `TTL` (none by default) and `Disabled`. Least recently used entries
  are evicted first.
- **Invalidation.** `application.InvalidatePolicies()` drops every cached set.
  Call it when something a policy legitimately depends on changes outside the
  actor, such as a deploy-time feature flag. It also ends every open event
  stream with `GOLEM_SUBSCRIPTION_RESYNC`, so clients reopen them under the new
  policies.
- **Failures.** A model whose policy cannot be built for an actor, because its
  `DefinePolicy` declares something invalid or panics, affects only that
  model. `ForPrincipal` still succeeds and other models work. Calls that need
  that model's policy are refused with `UNAUTHENTICATED: principal policy could
  not be established`, and an open event stream on that model ends with
  `GOLEM_SUBSCRIPTION_REVALIDATION` instead of silently dropping the events;
  reopen it once the policy builds. A panic is re-raised once,
  in the first call that needs the model. The failure stays with the cached
  set until that set is evicted or invalidated.

## Reading

```go
all, err := caller.Notes.FindMany(ctx, notes.Notes.Where(golem.All[notes.Note]()))
drafts, err := caller.Notes.FindMany(ctx, notes.Notes.Where(notes.Notes.Published.Eq(false)))
one, err := caller.Notes.FindUnique(ctx, notes.Notes.ByID.Value(id))
```

Predicates compose with `.Or(...)`, `.And(...)`, and `golem.All[T]()` matches
everything the policy already permits. `Where` requires a predicate — there is
no argument-free form, because "no filter" and "everything I may see" are
different statements and golem makes you write the second one.

## Writing

```go
author, err := system.Authors.Create(ctx,
	notes.Authors.Create(notes.Authors.Handle.Create("ada")),
	notes.Authors.Select(notes.Authors.ID),
)
```

Each field is set through its own builder, so a field that is `readonly` or
absent from the schema cannot be written by construction.

### Transactions serve one call chain at a time

A transaction serves one call chain at a time. The chain is one call made
through the transaction and everything nested inside it: the hooks that call
runs, calls made with a hook's own context, calls through a hook's executor,
and the hooks those run in turn. A call holds the transaction from start to
finish, as one operation: preparation, every statement, and its before and
after hooks, for reads as well as writes. A callback that returns while a call
is still in a hook waits for that call before golem commits. Nested calls run
inside their parent: each
hook write gets its own savepoint, and a hook's calls finish before the write
that ran the hook continues. Calls made one after another always proceed.

A call that would overlap the active call without belonging to its chain fails
immediately with `P4_RUNTIME_TRANSACTION: transaction used concurrently; a
transaction serves one call chain at a time`. That covers a sibling goroutine
started from the callback, a call inside a hook with a context that is not the
hook's own, a second executor call started while the hook's first is still
running, and an escaped executor used while its hook's call is busy. Such a
call never waits, so it can never deadlock, and the call already running is
unaffected.

Don't share a transaction across goroutines. A loop is just as fast: the
transaction holds one connection, and a connection runs one statement at a
time. Do slow work that doesn't touch the database before you open the
transaction, and hand background work to the queue, or to a fresh caller after
commit.

### Lock keys serialize check-then-write

A transaction that counts or reads rows and then writes based on what it saw,
such as "insert only while fewer than N exist", races another transaction doing
the same: both read the old state and both write. Take a lock key first:

```go
err := caller.Transaction(ctx, func(tx *notes.CallerTx[Principal]) error {
	if err := tx.LockKeys(ctx, golem.MustLockKey("note-quota", authorID.String())); err != nil {
		return err
	}
	count, err := tx.Notes.Count(ctx, notes.Notes.Where(notes.Notes.AuthorID.Eq(authorID)))
	if err != nil || count >= limit {
		return err
	}
	_, err = tx.Notes.Create(ctx, input)
	return err
})
```

`golem.NewLockKey(namespace, parts...)` builds a key from a non-empty namespace
and non-empty parts, all valid UTF-8 and at most 256 bytes together;
`MustLockKey` panics instead of returning the error. `LockKeys` is on both
`CallerTx` and `SystemTx`, applies no policy, and holds the keys until the
transaction commits or rolls back. On PostgreSQL it uses transaction advisory
locks, so it works across replicas; a second transaction locking the same key
waits until the first finishes. Keys passed in one call are taken in a fixed
order, so two transactions passing the same keys in different orders do not
deadlock. On SQLite, where one writer runs at a time, `LockKeys` only validates
the keys.

Take lock keys first, before any write in the transaction. Taken after an
update, delete or upsert has locked a row, a key cannot wait: it fails at once
with `CONFLICT` when another transaction holds it, for the same reason as rows
written out of order. Taken after a plain create, a key may still wait, and
that wait can deadlock with another transaction blocked on the row you
inserted; PostgreSQL aborts one of the two, and if it is yours, `LockKeys`
returns `CONFLICT`. A cancelled context while waiting also returns `CONFLICT`,
wrapping the context's error. After a cancelled wait, or a deadlock that
PostgreSQL resolved against you, the transaction cannot be used any further, so
return the error from your callback and retry the whole transaction.

### Serializable transactions and retry

Instead of a lock key, a transaction can ask PostgreSQL to detect the race:

```go
err := caller.Transaction(ctx, func(tx *notes.CallerTx[Principal]) error {
	count, err := tx.Notes.Count(ctx, notes.Notes.Where(notes.Notes.AuthorID.Eq(authorID)))
	if err != nil || count >= limit {
		return err
	}
	_, err = tx.Notes.Create(ctx, input)
	return err
}, golem.Isolation(golem.Serializable), golem.RetryConflicts(3))
```

`Transaction` on `Caller` and `System` takes optional `golem.TransactionOption`
values. `golem.Isolation` accepts `golem.ReadCommitted`, the default, and
`golem.Serializable`; no other level is offered. Under `Serializable`, of two
transactions that both count and then both insert, one fails with `CONFLICT`,
at a statement or at commit. A serialization failure (SQLSTATE 40001) or a
deadlock (40P01) is `CONFLICT` under either level, at commit as well as at a
statement. On SQLite, where one writer runs at a time, every transaction is
already serializable, and the option changes nothing.

The race is caught only when every transaction that checks and writes the
invariant uses `Serializable`: PostgreSQL detects conflicts between
serializable transactions, not with transactions at the default level.

Under `Serializable`, a transaction sees the database as it was when the
transaction began, and policy is judged against that view. A permission revoked
by a transaction that commits after yours began is not seen. If the revoking
transaction was also `Serializable`, your write is ordered before the
revocation, or one of the two fails with `CONFLICT`. If it ran at the default
level, as every other golem transaction and every standalone mutation does,
the revocation is not detected and your write commits. Use `Serializable` for
invariants like count-check-insert, not to get fresher authorization: at the
default level golem re-reads a permission after waiting for a row lock. A row
another transaction changed after yours began cannot be updated or deleted;
the write fails with `CONFLICT`.

Serializable transactions can also fail with `CONFLICT` when they touch
unrelated rows. PostgreSQL tracks what each one read by index page, and every
write golem makes also inserts into its shared outbox and queue tables, so two
transactions can collide on a shared page. Pass `RetryConflicts` whenever you
use `Serializable`.

Without `RetryConflicts`, golem runs your callback exactly once and never
replays it. `golem.RetryConflicts(n)`, with `n` from 1 to 10, runs it up to `n`
times in all, starting again only after `CONFLICT`, with a short randomized
pause between attempts that grows each time and ends early if the context is
cancelled. Every attempt is a new transaction; after-commit hooks run once,
after the attempt that commits. Any other error ends the transaction at once.
Only use it with a callback that is safe to run again: it must not change state
outside the transaction, such as sending an email or appending to a slice it
does not reset.

`RetryConflicts` replays every `CONFLICT`, including ones that will happen
again on every attempt: an upsert that expects the row to be absent when it
exists, a version mismatch from `ExpectVersion`, or a write refused because
its operation has ended. It is for contention between transactions. For those
deterministic conflicts, a retry only repeats the failure `n` times; handle
them in your code instead.

A SQLite BEGIN refused because another connection holds the write lock is a
`CONFLICT` and is replayed.

### Mutations return only what you select

This is the API's sharpest edge. A mutation returns a `Row` containing
**nothing** unless you pass a projection:

```go
author, _ := system.Authors.Create(ctx, notes.Authors.Create(notes.Authors.Handle.Create("ada")))
_, present := golem.Value(author, notes.Authors.ID).Get()
// present == false — not even the identity is there
```

Pass `notes.Authors.Select(notes.Authors.ID)` and it is. The same applies to
`Update`, `Upsert` and `Delete`.

`Get()` returns the value **and whether it is present**. Absent-because-not-
selected and absent-because-policy-masked are deliberately indistinguishable,
so a caller cannot discover a field exists by watching it vanish. Ignore the
second return and you get a zero value you cannot explain.

### Nested writes on one relation run in a fixed order

When one write gives a relation field several nested operations, they run in
three steps, whatever order you list them in: first `Delete`, `DeleteMany`,
`Disconnect` and `Set`; then `Update`, `UpdateMany` and `Upsert`; then
`Create`, `CreateMany`, `Connect` and `ConnectOrCreate`. So `DeleteMany` with
`Create` replaces links in one call, and `UpdateMany` or `DeleteMany` never
touches the rows the same write creates or connects. An `Update`, `Delete` or
`Disconnect` naming a row the same write creates or connects fails with
`NOT_FOUND`, because that row is not there yet: make it a second call.

If a later operation in a caller's write removes a row the write created or
updated, for example a `DeleteMany` on another relation field of the same
model, the write fails with `CONFLICT` and nothing is written.

### Upserts and connect-or-create

An upsert's create input must set every field of its unique target to the
target's value, whichever branch you expect to run. One that does not is
refused with `BAD_USER_INPUT` (`upsert create input does not set the target
selector`) before either branch runs; a nested upsert is checked once its
parent row is written. A nested `ConnectOrCreate` follows the same rule. A
foreign-key field of the target is satisfied by a `Connect` on its relation to
the row holding that value, or by the value the nested parent gives it. A
`Before` create hook that rewrites the create input away from the target is
refused before the row is written.

The create branch also needs a create grant; see [Write rules](#write-rules).

`Increment` and `Decrement` on a field that is part of a relation's foreign
key are refused with `BAD_USER_INPUT: foreign key <Field> cannot be changed by
arithmetic`, for every caller. Set the new value instead.

### Datetimes

PostgreSQL and SQLite store a datetime to the microsecond at most, and a field
may declare a coarser precision. Go's `time.Now()` carries nanoseconds on
Linux. golem handles the difference as follows:

- **Writes truncate.** A datetime or time value you write, including each
  element of a list, is truncated toward the past to the field's precision.
  `2026-10-26T10:00:00.123456789Z` is stored as `…00.123456Z` in a
  microsecond field and `…00.123Z` in a millisecond one. Values golem
  supplies itself, through `default=now` and `updated`, are truncated the
  same way.
- **Filters rewrite exactly.** A filter or policy operand finer than a
  microsecond is rewritten so it selects exactly the stored values the
  original comparison would select. With `f` the operand truncated to the
  microsecond:

  | You write | golem compares |
  |---|---|
  | `LT(v)`, `LTE(v)` | `LTE(f)` |
  | `GT(v)`, `GTE(v)` | `GT(f)` |
  | `Eq(v)` | nothing matches |
  | `Ne(v)` | every row matches, including rows where the field is NULL |
  | `In(…)`, `NotIn(…)` | the list without its off-grid values; if none is left, `In` matches nothing and `NotIn` matches every row |

- **Selectors refuse.** A unique selector, a cursor, a list operator, a scoped
  read or an analytics request with an operand finer than a microsecond is
  refused with `BAD_USER_INPUT`, reason `VALUE_PRECISION`, and `Field` set to
  the field. Such an operand names a row that cannot exist.

A value you wrote and then compare with `Eq` matches only if it was already on
the field's grid. Filter on the value golem returns from the write, or
truncate with `time.Time.Truncate` to the field's precision before you write.

## Hooks

A hook is a method on the model type, found by its name when you run `golem
generate`. The name is a phase followed by an operation:

- phases: `Before`, `After` and, for writes, `AfterCommit`;
- operations: `FindOne`, `FindFirst`, `FindMany`, `Create`, `Update`,
  `Delete`, `UpdateMany` and `DeleteMany`.

Generation emits a request and a result type for each pair, named after the
model, such as `NoteCreateRequest` and `NoteCreateResult`:

```go
func (Note) BeforeCreate(ctx context.Context, request *NoteCreateRequest) error {
	actor := golem.ActorFrom[Actor](ctx)
	return golem.SetCreate(request, Notes.AuthorID, actor.UserID)
}

func (Note) AfterCreate(ctx context.Context, result NoteCreateResult) error {
	id, _ := golem.Value(result.Row(), Notes.ID).Get()
	_, err := golem.HookCreateRow(ctx, result.Executor(), GolemGeneratedAuditEntryDescriptor,
		AuditEntries.Create(AuditEntries.NoteID.Create(id)))
	return err
}

func (Note) AfterCommitCreate(ctx context.Context, result NoteCreateResult) error {
	return notify(ctx, result.Row())
}
```

Here `AuditEntry` is another model in the schema, and `notify` is your own
code.

- **`Before`** receives the request and may change it: `golem.SetCreate` sets
  a create field, including a `system` field, and a read request's
  `ReplaceOptions` adds options. A read hook cannot remove the policy, which is
  applied after it. An error refuses the operation.
- **`After`** runs inside the write's transaction, after the rows are written,
  and sees the result: `Row()`, `Before()` and `After()`, or `Count()` for
  `UpdateMany` and `DeleteMany`. An error rolls the write back. Its
  `Executor()` reads and writes on that same transaction through
  `golem.HookFindManyRows`, `HookCreateRow`, `HookUpdateRow`, `HookDeleteRow`,
  `HookUpsertRow`, `HookUpdateManyRows` and `HookDeleteManyRows`. Each executor
  write runs in its own savepoint, and the executor works only while the hook
  runs (see [Operations that own a write](#operations-that-own-a-write) for
  how its calls are ordered).
  `golem.HookEnqueue(ctx, result.Executor(), pending)` also enqueues a durable
  job on that transaction: the job exists if and only if the write commits, and
  an error the hook returns rolls the job back with the write. The call runs in
  its own savepoint, so a hook may handle a refusal (an oversize payload, say)
  and let the write commit without the job. It returns the existing job's ID
  when a `queue.Dedupe` key matches an active job. Without `Config.Queue` it
  returns `CodeConfigInvalid`, which rolls the write back unless the hook
  swallows it. Open cannot refuse this, because which hooks enqueue is not
  known statically. The in-process worker is woken after commit; other
  processes find the job when they poll, as with `tx.Enqueue`.
- **`AfterCommit`** runs once the transaction has committed, so its error
  cannot undo anything. golem passes it to `Config.AfterCommitError`, which
  `Open` requires when any after-commit hook exists. Its result has no
  executor.

A `Before` hook that reads a write's input (`golem.RuntimeFreezeCreateInput`,
`RuntimeFreezeUpdateInput`, then `FrozenMutationField.Value()`) sees the same Go
value whether the write came from a GraphQL mutation, the generated Go API or a
nested write: the field's Go type (`string`, `int16`, `int32`, `int64`,
`float32`, `float64`, `bool`, `golem.Decimal`, `golem.UUID`, `golem.Date`,
`golem.Time`, `time.Time`, `[]byte`), an enum as its wire `string`, and a scalar
list as a plain `[]T` of the element's type (a list of enums is `[]string`). A
field written to null reports `MutationFieldNull` and no value. The value is a
copy, so changing it does not change the write.

Names such as `BeforeUpsert` and `BeforeAggregate` are refused. A panic in a
hook becomes an error, `generated hook panicked`, and its value is not kept.
`golem.ActorFrom[Actor](ctx)` returns the actor of the caller the hook runs
for; a custom query or mutation resolver receives the same actor in its
`ctx`: a custom query over GraphQL or through the generated `Query`, and a custom mutation over GraphQL or through the generated `Mutate`. Hooks run for callers' operations, including those inside custom
mutations and transaction operations. Writes through `System()` or
`SystemEscape` run no hooks.

## What it does

```go
// cmd/notes/main.go
package main

import (
	"context"
	"fmt"
	"log"

	"example.com/notes/notes"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider/sqlite"
)

type Principal struct {
	UserID        golem.UUID
	Authenticated bool
}

func main() {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: "file:notes.db"})
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	application, err := notes.Open(ctx, notes.Config[Principal]{
		Database: database,
		ResolvePrincipal: func(_ context.Context, principal Principal) (notes.Actor, error) {
			return notes.Actor{UserID: principal.UserID, Authenticated: principal.Authenticated}, nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	system := application.System()
	author, err := system.Authors.Create(ctx,
		notes.Authors.Create(notes.Authors.Handle.Create("ada")),
		notes.Authors.Select(notes.Authors.ID),
	)
	if err != nil {
		log.Fatal(err)
	}
	authorID, ok := golem.Value(author, notes.Authors.ID).Get()
	if !ok {
		log.Fatal("create returned no identity")
	}

	caller, err := application.ForPrincipal(ctx, Principal{UserID: authorID, Authenticated: true})
	if err != nil {
		log.Fatal(err)
	}
	for _, note := range []struct {
		title     string
		published bool
	}{{"published note", true}, {"draft note", false}} {
		if _, err := caller.Notes.Create(ctx, notes.Notes.Create(
			notes.Notes.AuthorID.Create(authorID),
			notes.Notes.Title.Create(note.title),
			notes.Notes.Published.Create(note.published),
		)); err != nil {
			log.Fatal(err)
		}
	}

	mine, err := caller.Notes.FindMany(ctx, notes.Notes.Where(golem.All[notes.Note]()))
	if err != nil {
		log.Fatal(err)
	}
	anonymous, err := application.ForPrincipal(ctx, Principal{})
	if err != nil {
		log.Fatal(err)
	}
	visible, err := anonymous.Notes.FindMany(ctx, notes.Notes.Where(golem.All[notes.Note]()))
	if err != nil {
		log.Fatal(err)
	}
	drafts, err := caller.Notes.FindMany(ctx, notes.Notes.Where(notes.Notes.Published.Eq(false)))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("author=%d anonymous=%d drafts=%d\n", len(mine), len(visible), len(drafts))
}
```

```
author=2 anonymous=1 drafts=1
```

The author sees their published note and their draft. An anonymous caller sees
only the published one — not because the program filtered, but because the
policy predicate was compiled into the query. The draft filter finds one row.

## GraphQL

### Introspection

Introspection is off by default. Turn it on in the generated config:

```go
server, err := application.GraphQL(notes.GraphQLConfig[Principal]{
	PrincipalFromContext: principalFromContext,
	ReportInternalError:  reportInternalError,
	Introspection:        true,
})
```

With `Introspection: true`, `__schema` and `__type(name:)` are answered on
`Query`. You can select them alone or beside ordinary roots in the same
operation. They describe exactly the SDL that `server.SDL()` returns:

- A model, relation or field marked hidden is absent from both.
- An operation you did not generate is absent from both.
- A field that policy can mask is present in both, because masking happens per
  row at read time, not in the schema.
- A type no operation can reach is absent from both. The generated schema keeps
  the types reachable from `Query`, `Mutation` and `Subscription` through
  fields, arguments and input fields. Turning an operation off, such as
  `create` on a model or analytics, removes the inputs only that operation
  used. For example, a model without `create` has no `PostCreateInput` unless
  another input nests it.

An operation that selects only `__schema`, `__type` or `__typename` roots
still resolves the principal through your `ResolvePrincipal`. A principal it
refuses gets exactly the error a data query would get.

Such an operation does not consult policy: it builds no model policy, opens no
transaction and issues no SQL. So every caller whose principal resolves sees
the same schema. When meta roots sit beside data roots, the operation runs the
ordinary caller setup for the data.

With `Introspection: false`, any operation that selects `__schema` or `__type`,
including through a fragment, is refused whole with
`GRAPHQL_VALIDATION_FAILED`.

`__typename` is not introspection and works regardless of the flag:

- `{ __typename }` returns `"Query"`, and `mutation { __typename }` returns
  `"Mutation"`.
- Inside any object selection, including a subscription event and its
  `entity`, it returns the object's type name.
- It is not allowed as the root field of a subscription, because GraphQL
  requires a subscription's single root field to be a real field. There it is
  refused with `GRAPHQL_VALIDATION_FAILED`.

A generated application always gives its server an executable schema. A
server you build directly with `graphql.NewServer` and no `ExecutableSchema`
cannot answer `__schema`, `__type` or a root `__typename`. It refuses them
with `GRAPHQL_VALIDATION_FAILED` rather than omit them from the response.

Introspection is limited separately from data:

- **Cost.** Every field under `__schema` or `__type` costs 1 toward
  `MaxComplexity`. Introspection lists such as `types`, `fields` and `args` are
  bounded by the schema, not by stored rows, so they are never multiplied by
  `MaxPageSize`.
- **Depth.** Selections under `__schema` or `__type` do not count toward
  `MaxDepth`.
- **Recursion.** Nesting `fields`, `inputFields`, `interfaces` or
  `possibleTypes` three deep, as in `types { fields { type { fields { type {
  fields ...` , is refused with `GRAPHQL_VALIDATION_FAILED`. This matches
  graphql-js's `MaxIntrospectionDepthRule`.
- **Repetition.** One operation may select at most one `__schema` root and
  at most eight `__type` roots, counted by response name after `@skip` and
  `@include`. A ninth `__type` or a second aliased `__schema` is refused with
  `QUERY_LIMIT_EXCEEDED`, because each alias would serialise the schema
  again. Two selections under the same response name merge and count once.
- **Still enforced.** Introspection selections still count toward
  `MaxSelectedFields`, `MaxAliases`, `MaxASTNodes`, `MaxFragments`,
  `MaxTokens` and `MaxRequestBytes`.

The standard graphql-js `IntrospectionQuery`, the one GraphiQL and code
generators send, passes at the default limits whatever the schema's size.

Data selections in the same operation are costed and depth-limited exactly as
they would be alone. Placing them beside `__schema`, or inside a fragment that
also selects it, does not exempt them.

### Custom operations

A schema package may declare its own query and mutation roots:

For an action that outlives the request (an export, an import), see
[Recipe: durable actions](QUEUE.md#recipe-durable-actions).

```go
func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Query(graphql, "searchInvites", SearchInvites, golem.MaxItems(20))
	golem.Mutation(graphql, "inviteMember", InviteMember)
}

func InviteMember(ctx context.Context, caller *Caller[Principal], args InviteArgs) (string, error) {
	email, err := normalizedEmail(args.Email)
	if err != nil {
		return "", err
	}
	created, err := caller.Invites.Create(ctx, Invites.Create(
		Invites.ID.Create(args.ID), Invites.TeamID.Create(args.TeamID),
		Invites.Owner.Create(args.Owner), Invites.Email.Create(email),
		Invites.Status.Create("pending"),
	), Invites.Select(Invites.ID))
	if err != nil {
		return "", err
	}
	id, _ := golem.Value(created, Invites.ID).Get()
	return id.String(), nil
}
```

A resolver receives only the caller, never `System`, a transaction or a
database. It may open `caller.Transaction` itself. A custom mutation can be
granted writes the caller cannot perform directly; see
[Operations that own a write](#operations-that-own-a-write).

Inside `caller.Transaction`, `tx.Enqueue` adds a durable job and `tx.CancelJob(ctx, jobID)`
cancels one, both committing or rolling back with the transaction. Cancel only a
job ID you read, in the same transaction, from a row the caller's policy allowed,
never an ID taken from arguments. See [the queue](QUEUE.md#inspecting-and-operating).

#### What a custom operation returns

A custom query, a custom mutation or a computed field may return model rows
(`golem.Row[Invite]`, `[]golem.Row[Invite]`, lists of lists). golem never
encodes the rows your resolver built. It reads every returned row again through
the caller, in one read per root (per computed field, for computed fields),
with the selection the client asked for. The client therefore sees exactly what
the generated roots would show it:

- Relations, `_count` and computed fields work under a custom root as they do
  under `invites` or `teams`.
- Read policy and field masks are the caller's. A row you read with `System`
  inside the resolver, or one the caller cannot read, is not disclosed, and a
  field masked for the caller is `null`, whichever fields the client selected.
- Order, duplicates and nested list shape are kept.
- A row built by hand (`golem.RuntimeReadRow`) has no identity to read again
  and is an internal fault: `INTERNAL_SERVER_ERROR`, reported through
  `ReportInternalError`.

For queries and computed fields, a row that was deleted before the read or that
the caller cannot read is left out of its list. A single result that is gone is
`null` when the field is nullable and `NOT_FOUND` when it is not.

A custom mutation has already committed when its resolver returns, so golem
never hides that. If anything fails after the resolver returned, because the
returned row cannot be read by the caller, vanished, or could not be read or
encoded, the root fails with `COMMITTED_RESULT_UNAVAILABLE`:

```json
{
  "message": "custom mutation submitInvite committed, but its result is unavailable",
  "path": ["submitInvite"],
  "extensions": {
    "code": "COMMITTED_RESULT_UNAVAILABLE",
    "reason": "RESULT_NOT_READABLE",
    "committed": true,
    "unreadable": 1
  }
}
```

`committed: true` is always present. `reason` is `RESULT_NOT_READABLE`, with the
number of rows in `unreadable`, when the caller cannot read the result, and
`LIMIT_EXCEEDED` when a list passed its limit. It is absent for any other
failure. A limit failure and an unclassified failure are also reported through
`ReportInternalError`; a failure that is already a public `golem.Error`, such as
`FORBIDDEN` from the hydration read, is returned without being reported. A list is never
shortened: if one row is unreadable the whole root is `null` with this error. A
nullable root is `null` and later mutation roots in the same document still
run; a non-null root makes `data` `null` and later roots do not run. A resolver
that returns `null` from a nullable root is not an error. An error returned by
the resolver itself is presented as before, because nothing committed.

A resolver that creates a row its caller is not allowed to read, for example
through a `golem.Within` grant, still gets a `golem.Row` back with no readable
cells. It carries the new row's primary key privately, so golem can report it as
`RESULT_NOT_READABLE` instead of treating it as a fault. A create without a
projection returns a row with no key and cannot be returned as a model result.

#### How many items a list may hold

Every list a custom operation or a computed field returns declares its
maximum size. A custom root states it after its resolver, one value for each
list level, outermost first:

```go
golem.Query(graphql, "searchInvites", SearchInvites, golem.MaxItems(20))
golem.Query(graphql, "groupedInvites", GroupedInvites, golem.MaxItems(10, 2)) // [][]golem.Row[Invite]
```

A computed field states it where it writes the list type, once for each
`golem.GraphQLList`:

```go
golem.ComputedField(graphql, "openInvites", golem.GraphQLList(golem.GraphQLObject[Invite]().NonNull(), 20).NonNull(), Team{}.OpenInvites)
```

Each value is a constant of at least 1. `golem generate` fails when a list level
has no maximum (`P5_GRAPHQL_LIST_MAX_REQUIRED`), when `MaxItems` gives a
different number of values than the result has list levels
(`P5_GRAPHQL_LIST_MAX_ARITY`), when a value is 0 (`P5_GRAPHQL_LIST_MAX_ZERO`),
when it is not a constant (`P5_GRAPHQL_LIST_MAX_CONSTANT`), or when the result is
not a list (`P5_GRAPHQL_LIST_MAX_SCALAR`). A custom operation fails when an option
is anything other than a direct `golem.MaxItems` call (`P5_CUSTOM_OPTION`) or
when it declares `MaxItems` more than once (`P5_CUSTOM_OPTION_DUPLICATE`). `[]byte` is a `Bytes` scalar, not a
list.

The maximum is the most the resolver may return. It is not a page size and is
independent of `MaxPageSize`, which stays the ceiling for a `take` argument. A
resolver that returns more fails, and the list is never shortened:

```json
{
  "message": "custom query searchInvites returned more than 20 items",
  "path": ["searchInvites"],
  "extensions": { "code": "LIMIT_EXCEEDED", "reason": "LIMIT_EXCEEDED", "limit": "list_items", "max": 20 }
}
```

The error is also passed to `ReportInternalError`, because the resolver, not the
caller, broke the contract. The check runs on the resolver's own return value,
before golem reads the rows again. For a computed field it runs for each parent
row, and `computed field Team.openInvites returned more than 20 items` is the
error at that field's path. For a custom mutation the writes are already
committed, so the root is `COMMITTED_RESULT_UNAVAILABLE` with `committed: true`,
`reason: LIMIT_EXCEEDED`, `limit: list_items` and `max`. `golem.LimitListItems`
names the limit in Go.

The maximum also sets the list's query cost. Generated semantic and full-text
search roots declare their own maximum: the smallest of the search engines'
ceiling, the model's `MaxPageSize` and its `MaxTake`. Their `take` argument
defaults to the model's default page size, narrowed to that maximum.

When a custom operation owns the public search semantics, add
`golem.GraphQLWithoutIndexSearch()` to the model's `golem.GraphQL` options. It
suppresses every generated full-text and semantic GraphQL search root for that
model without removing the indexes or their typed caller, system and
transaction Go methods.

To run a custom mutation from Go, call the generated `Mutate`:

```go
id, err := notes.Mutate(ctx, caller, notes.InviteMember, notes.InviteArgs{ /* ... */ })
```

`Mutate` runs the resolver exactly as GraphQL dispatch does. It receives the
same Within grants, runs the same hooks, uses the same transaction behaviour,
and is observed as `mutation.custom`. Its types are inferred from the
resolver. A function that is not a generated custom mutation, such as a custom
query resolver or a wrapper around a mutation resolver, is refused with
`P5_CUSTOM_OPERATION` before anything runs. `Mutate` is generated only for a
schema that declares a custom mutation.

To run a custom query from Go, call the generated `Query`:

```go
rows, err := notes.Query(ctx, caller, notes.SearchInvites, notes.SearchArgs{ /* ... */ })
```

`Query` installs the caller's actor on `ctx`, so `golem.ActorFrom` works in
the resolver as it does over GraphQL, and calls the resolver with `caller`. It
adds no grants and no transaction. Calling a query resolver directly skips the
actor. `Query` is generated only for a schema that declares a custom query.

### Partial commits are never reported as clean failures

A mutation document can commit some of its work and then fail: the first root
commits and the second is refused, or a custom mutation commits one write and
returns an error. A response with errors alone would look like a clean failure,
and a client that retries would repeat a write that is already persisted.

When a response has errors and the operation committed at least one write, the
response carries an extension at its top level:

```json
{ "errors": [ ... ], "extensions": { "golem": { "commit": "PARTIAL" } } }
```

- `PARTIAL`: at least one write was committed. Do not retry the document as a
  whole.
- `UNKNOWN`: a `COMMIT` failed in a way that does not say whether the database
  applied it, such as a connection lost while the commit was in flight. Read the
  data back before retrying. `UNKNOWN` wins when both apply.

A response with no errors, a failure that committed nothing, and an update that
touched no rows carry no `extensions.golem.commit`; their bytes are unchanged.
A custom mutation that commits and then cannot return its result reports
`COMMITTED_RESULT_UNAVAILABLE` with `committed: true` on its error and
`commit: "PARTIAL"` on the response. The two always agree. A serialization
failure or a commit on a finished transaction is a definite rollback and is not
`UNKNOWN`. The extension is written for HTTP, SSE and WebSocket operations.

## Using golem from Go without GraphQL (kernel mode)

An application that calls golem only from Go can leave GraphQL out entirely:

```go
func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "notes")
	golem.WithoutGraphQL(schema)
	golem.Actor[Actor](schema)
	golem.Model[Author](schema)
	golem.Model[Note](schema)
	golem.Providers(schema, golem.SQLite)
}
```

With `WithoutGraphQL`, `golem generate` writes the model, binding, event and
registry files (`zz_golem_models.gen.go`, `zz_golem_bindings.gen.go`,
`zz_golem_events.gen.go`, `zz_golem_registry.gen.go`) and nothing else. It
writes no SDL, no `zz_golem_graphql.gen.go` and no `golemgqlgen` package, and
it never runs gqlgen. The generated `App` has no `GraphQL` method. Nothing your
application compiles imports gqlgen, gqlparser or golem's `graphql` package, so
`go mod tidy` removes them from your module.

Everything else is unchanged: callers, the system client, transactions,
hooks, events, the queue, and both kinds of operation.

- `golem.Mutation` is still accepted. Its resolver is not a GraphQL root, but
  `Mutate` runs it with its `Within` grants, as in
  [Custom operations](#custom-operations).
  [Transaction operations](#operations-on-a-transaction) work the same in both
  modes.
- `golem.Query`, `golem.ComputedField` and their batched variants exist only to
  extend GraphQL, and fail generation with `P5_GRAPHQL_DISABLED_EXTENSION`.
  Call the query's resolver as an ordinary function instead.
- `graphql=` tags and `golem.GraphQL[M](...)` options are still accepted, so
  switching modes needs no change to the models.

Switching modes is a regeneration, not a migration. Adding `WithoutGraphQL` and
running `golem generate` removes exactly the GraphQL files: the SDL,
`zz_golem_client.gen.ts`, `zz_golem_graphql.gen.go` and the three files in
`golemgqlgen/`. The model,
contract, physical and migration fingerprints are identical in both modes, so
`golem migration plan` reports no changes, and `golem check` and `golem doctor`
report the generation as current. Removing the declaration and regenerating
restores the GraphQL output byte for byte; add gqlgen and gqlparser back to
`go.mod` with `go mod tidy`.

## Subscriptions and events

A model declares that it has events in its `GolemModel` method:

```go
func (Note) GolemModel() golem.ModelSpec[Note] {
	return golem.DefineModel(golem.Subscriptions[Note]())
}
```

Every write to the model then records a fact in golem's outbox table, in the
same transaction as the write. `application.RunEventPublisher(ctx)` claims
committed facts and publishes them through the configured `EventTransport`.
Each subscriber's process judges each event against that subscriber's policy
when the event arrives. A model hidden from GraphQL with `GraphQLHidden()` may
have events too; GraphQL still never exposes it.

A caller subscribes through the generated client:

```go
stream, err := caller.Notes.Events(ctx,
	golem.EventWhere(notes.Notes.Published.Eq(true)),
	golem.EventSelect[notes.Note](notes.Notes.Title, notes.Notes.Published),
)
if err != nil {
	return err
}
defer stream.Close()
for {
	event, err := stream.Recv(ctx)
	if err != nil {
		return err
	}
	switch event.Metadata().Action() {
	case golem.EventCreated, golem.EventUpdated:
		if row, ok := event.Entity(); ok {
			apply(event.ID(), row)
		}
	case golem.EventDeleted, golem.EventExited:
		forget(event.ID())
	}
}
```

### What an event carries

- **Metadata.** The action, the event's ID, the causation ID shared by every
  event of one transaction, the event's ordinal within that transaction, and
  when it was recorded.
- **`ID()`.** The row's primary key, typed.
- **`Entity()`.** Present only when the stream selects fields with
  `EventSelect`, and only for created and updated events. It is the row as it
  is when the event reaches you, read under your policy, filter and read hooks,
  with masked fields absent. It is not the values the write wrote: two updates
  in quick succession can both deliver the later state. When the row no longer
  exists or no longer matches by then, the event is not delivered.
- **Deleted and exited events** carry only `ID()`. Over GraphQL their `entity`
  is null. An exited event (`golem.EventExited`, `EXITED`) means the row left
  your stream because of an update: the row still exists, but it no longer
  matches your filter or your read rule. See "Rows that leave a list".

An event that your policy or your filter excludes is not delivered, and
nothing tells you it existed.

### Who receives a created or updated event

Delivery reads the changed row **once per event**, not once per subscriber.
Before it reads anything for a subscriber, golem judges the subscriber's read
rule and stream filter against that one shared row, with the same three
results as a delete:

- **false:** the event is not delivered, and golem runs nothing for that
  subscriber: no principal resolution, no read hooks, no read of its own;
- **true or unknown:** golem runs the full path for that subscriber exactly as
  before. It resolves the principal afresh, runs the read hooks, and reads the
  row under the subscriber's policy, filter and hooks. That read, and only
  that read, decides what is delivered and what the payload holds.

The shared read takes the row and the to-one relations (at any depth within
the relation limit) that your policies and filters reference, in a bounded
number of statements that does not grow with the number of subscribers: one
statement for the row plus one per level of to-one relation the policies and
filters reach. An event therefore costs those shared statements plus one full
path (principal resolution, hooks and one exact read) per **candidate**, a
subscriber the shared read could not exclude. Before, it cost one full path per
subscriber. A model with an owner rule and 128 subscribers, of which one is the
owner, runs 2 statements per event (1 shared, 1 exact); a rule through one
to-one relation runs 4 (2 shared, 2 exact). The judgement itself runs in memory
over the shared row, once per subscriber, and reads nothing.

A condition is **unknown**, and goes to the full path, when the shared read
cannot decide it:

- a to-many relation (`Some`, `Every`, `None`);
- a field the shared read did not load, or any condition the policy evaluator
  refuses;
- a comparison over a column kind that has not been proven to agree with the
  database: only boolean, 32 and 64-bit integer, decimal, datetime, enum, UUID
  and string columns are judged. Bytes, JSON, scalar lists, date, time of day,
  floating point and 16-bit integer columns, and any to-one relation whose
  condition reaches one, are always unknown;
- a set of subscribers whose combined relations the planner cannot read in one
  bounded plan (for example one past the relation limit): every subscriber of
  that event is then unknown and takes the full path;
- a policy for the model that is unavailable (the full path then ends the
  stream with `GOLEM_SUBSCRIPTION_REVALIDATION`).

Unknown never costs correctness, only the saving: the event costs what it cost
before for that subscriber.

Two consequences are deliberate:

- **The decision uses the policy as it was when the stream opened.** A
  subscriber whose rule was false on the row at that time stays excluded for
  that row until the stream is reopened (at its deadline, or when
  `InvalidatePolicies` ends it), even if the policy has since broadened. Revocation is never late: a subscriber the frozen rule would admit
  still goes through the full path, which resolves the principal afresh, so a
  revoked actor never receives a row. Reopening the stream picks up
  broadened access.
- **A read hook does not run for a subscriber the shared read excludes.** A
  hook used for audit or counting sees one call per candidate subscriber of an
  event, not one per subscriber. A policy that is constant false already
  behaved this way.

If the shared read itself fails, the streams that needed it end with
`GOLEM_SUBSCRIPTION_REVALIDATION`. A row that no longer exists when the event
is judged is not delivered to anyone.

### Rows that leave a list

An update can move a row out of a stream: the title no longer matches the
`EventWhere`, or the row no longer passes the caller's read rule (a post was
unpublished, a row changed owner). The exact read for that update then finds no
row. golem used to say nothing, and a subscriber that held the row kept it. It
now delivers an **exited** event: action `golem.EventExited` (`EXITED` in the
GraphQL `type` field), the row's key, the version of the update, and no entity.
Remove the row from the list you hold exactly as for a delete, but the row has
not been deleted and can come back (it then arrives as a created or updated
event, or as `RESYNC`).

Every update fact stores a **before snapshot**: the row as it was before the
update, with the same field set as the delete snapshot (see "The delete
snapshot") and under the same size limit. When an updated event finds no row
for a subscriber, golem judges the subscriber's read rule and stream filter
against that before snapshot, with the results of a delete:

| Before snapshot, rule and filter | Result |
|---|---|
| both true | `EXITED` is delivered |
| either false | nothing: the subscriber never held the row |
| either unknown | the stream ends with `GOLEM_SUBSCRIPTION_RESYNC` |

Unlike a delete, a filter that cannot be decided (a filter or read hook on a
field outside the snapshot) ends the stream with `RESYNC` and does not deliver:
an exit names a row the caller's hooks may hide, so golem does not guess.
Unknown also covers a to-many relation, a before snapshot that was too large to
store (`events.ObservationDeleteSnapshotOversize`, now reported for updates too,
with action `updated`), and a change recorded from an external source without a
`Before` image. A to-one relation in the rule is answered from the same shared
read as a created or updated event when the update did not change the row's key
to it; when the update did change that key, it is decided with the delete
path's checks against the relation's old target.

A subscriber that never held the row costs no read and no principal
resolution: the judgement runs in memory over the shared read and the before
snapshot. Only a subscriber that may have held the row resolves its principal
afresh and runs the delete path's checks.

### A stream that reads another model

A stream's read rule or `where` can reach another model: `Comments.Post.Is(...)`
in a filter, or a rule such as "readable when the post is published". When access
to that other model changes, rows the stream already told the client about may
stop qualifying, and no event for the stream's own model says so. golem ends the
stream instead of leaving stale rows visible.

- **What ends it.** The stream watches each model its rule or filter reaches, and
  the target's own read rule is part of that reach. A change to a row of that
  model ends the stream with `GOLEM_SUBSCRIPTION_RESYNC` and reason
  `DEPENDENCY_CHANGED` (`events.ReasonOf`; over GraphQL `extensions.reason`) when
  both hold: the update changed a field the stream's rule or filter reads, and
  the subscriber could read that row before the change or can read it now. The
  client reloads what it holds and subscribes again; a reload after the end
  shows the revoked row gone.
- **What does not.** An update that changes only fields the stream does not read
  is ignored. So is a change to a row the subscriber could read neither before
  nor after, because an unreadable row is indistinguishable from a missing one.
  A row that is created or deleted ends the stream only when the stream reaches
  that model through a to-many relation; for a to-one relation the child rows
  that referenced it have their own events. An update whose before-image was not
  kept (an oversize snapshot, or a change-data-capture update without a
  before-image) cannot be narrowed and ends the stream when the subscriber can
  read the row. The stream's own model is not watched this way.
- **Every model it reaches needs `Subscriptions`.** A stream whose rule or filter
  reaches a model without `golem.Subscriptions` is refused when it is opened with
  `GOLEM_SUBSCRIPTION_INVALID` and reason `DEPENDENCY_UNOBSERVABLE`; the message
  names the model. Add `Subscriptions` to that model or change the rule.
- **Cost.** One internal member per model reached sits on that model's hub with
  no output of its own. Each event of that model costs one judgement per such
  member and shares the one snapshot read of the event (see [Who receives a
  created or updated event](#who-receives-a-created-or-updated-event)).
- **If it cannot be watched.** A source that fails or a read that fails ends the
  stream with the code that failure carries (`RESYNC`, `SOURCE_CLOSED` or
  `REVALIDATION`) rather than leaving it open and unwatched.
- **`InvalidatePolicies`.** `application.InvalidatePolicies()` drops the policy
  cache and ends every open event stream with `GOLEM_SUBSCRIPTION_RESYNC` and no
  reason. A client reopens its streams and so picks up the new policies; this is
  also how access that broadened reaches a stream.

### Row versions

Every model with `Subscriptions` has a golem-owned version on each row. A write
that goes through golem gives the row a version higher than every version that
row key has ever committed: an update, a nested update, `UpdateMany` and the
clearing of a reference by a delete action each draw a new one, and a row
created after its key was deleted starts above the version the deleted row
last held. A delete keeps the row's last version.

- **Reading it.** Over GraphQL the model type has `_version: String!`, a decimal
  string. In Go the model's namespace has a read-only `GolemVersion` handle that
  works in `Select` and in predicates, including inside hooks. It is readable
  whenever the row is readable: no field rule grants it and none can name it.
  It is not in `where`, `orderBy` or `distinct` over GraphQL.
- **Not writable.** No GraphQL input, Go create or update, hook, or nested write
  accepts it. A field or column of your own named `GolemVersion`, `_version`,
  `golem_version` or `_golem_version` on any model is refused when you
  generate.
- **One sequence for the database.** Versions come from a single source, not
  one per model. A caller comparing two versions of a row it can read learns
  roughly how many versioned writes the whole database made in between. Rows
  that existed before the upgrade keep version 0 until their next write.
- **Storage.** The model's table gets a `golem_version bigint NOT NULL DEFAULT
  0` column. On PostgreSQL the source is the sequence `golem_row_version` in
  golem's system schema, created with `CACHE 1`: with a larger cache two
  sessions could draw out of commit order, so any other cache setting is
  drift and the application refuses to start. On SQLite it is the one-row
  table `golem_row_version`, advanced once per write statement; a missing
  table or a second row is drift.
- **Writes that bypass golem.** Only writes golem issues stamp a version. A row
  changed by SQL outside golem keeps the version it had.
- **Events.** Every event type has `version: String!`. It is always the version of
  the write that produced the event, taken from the stored fact (for a delete,
  the row's last version, which the fact keeps even when its delete snapshot was
  too large to keep; for an exited event, the version of the update that moved the
  row out). A later write can land before the event is delivered; the
  event still carries its own version, and the entity you select carries the
  `_version` it was read at. Compare versions as integers. `golem.EventMetadata`
  has `Version() int64`.
- **CDC.** A change recorded from an external source is ordered from the moment
  golem records it: the emitter writes a new version into the changed row in the
  same transaction as the fact, and the event carries it. Between the external
  commit and the recording, a client can hold the external content at the old
  version. The images an adapter hands to `Emit` hold `golem_version` like any
  other stored scalar. A delete takes its version from the `Before` image. An
  update's `Before` image is optional: with it the event stores a before
  snapshot and subscribers that held the row receive `EXITED`; without it,
  every subscriber that cannot be ruled out from the new row ends with
  `RESYNC`. A row
  that was inserted and deleted outside golem between two batches was never
  stamped, so its delete is published at version 0, which hides nothing a client
  held. A `Before` image without the `golem_version` column fails with
  `GOLEM_CDC_INVALID`.
- **What an adapter must do.** The stamp is an `UPDATE` of the changed row, so
  your source log shows it as another change. It runs in the same database
  transaction as the fact inserts into `_golem_outbox`, and
  `CorrelatesGolemTransaction` must answer true for that source transaction
  (for example because it wrote to `_golem_outbox`); the emitter then records
  and publishes nothing for it. An adapter that answers false for it receives
  its own stamp as a new change, which is stamped again, and so on without end.

### Who receives a delete

A deleted row cannot be read back, so golem judges a delete against the
row's **delete snapshot**, a set of its fields stored with the fact, and
evaluates your read rule, expanded as for a read (see
[How policies are proved](#how-policies-are-proved)), with three possible
results:

- a condition on the row's own fields is evaluated from the snapshot, and is
  **unknown** if the field is not in it;
- a forward to-one relation, such as `Notes.Author.Is(...)`, is decided by a
  query against the parent row, and is **unknown** if the parent no longer
  exists;
- a relation from the deleted row to its children, or any to-many relation,
  is **unknown**, because the delete has already cascaded, nulled or
  restricted those rows;
- `AND`, `OR` and `NOT` combine the results with three-valued logic, so a
  false condition on the row's own fields settles the answer without a query.

Your stream's filter, which is the `EventWhere` plus any filter your read
hooks add, is evaluated the same way. The outcome:

| Read rule \ filter | true, or no filter | false | unknown |
|---|---|---|---|
| **true** | delivered | not delivered | delivered |
| **false** | not delivered | not delivered | not delivered |
| **unknown** | stream ends with `GOLEM_SUBSCRIPTION_RESYNC` | not delivered | stream ends with `GOLEM_SUBSCRIPTION_RESYNC` |

When your read rule allows the row but the filter cannot be decided, golem
delivers the delete. You were allowed to see the row, so a delete for an ID you
do not hold reveals nothing; ignore it. When golem cannot decide whether you
were allowed to see the row, it does not guess: your stream ends with `RESYNC`,
and only yours. Reload what you hold and subscribe again. A subscriber whose
rule is already false on the row's own fields receives nothing, not even
`RESYNC`.

**Parents changed in the same transaction.** Before it trusts the query
against a parent, golem looks in the outbox for facts on that parent from the
same transaction:

- A parent deleted in the same transaction, as in a cascade, is judged from
  its own delete snapshot, and its own parents the same way.
- A parent the same transaction created or updated, or one with more than one
  fact, is unknown, so the stream ends with `RESYNC`.
- A relation check inside a parent's condition that reaches a model the
  transaction changed, or a model without subscriptions, whose changes golem
  cannot see, is unknown.

**The polarity rule.** A relation check nested inside a parent's condition
that is tested negatively is unknown: `IsNot`, `IsNull`, `None`, `Every`, or
any relation check under `NOT`. Its target may have been removed in the same
transaction, which would make the negative test true by accident. The
readable-target expansion can introduce such checks, because a target's read
rule may itself contain them.

**Later transactions.** A parent changed by a later transaction, before the
delete reaches you, is judged on its state at delivery, as created and updated
events are.

### The delete snapshot

Every update fact stores the same snapshot, taken from the row before the
update (see "Rows that leave a list"). Everything below, the field set, the
`DeleteSnapshot` declaration and the size limit, applies to updates as well.

The snapshot always holds the primary key and the foreign keys of the model's
forward to-one relations. When the model declares nothing else, it also holds
every bounded scalar: booleans, numbers, decimals, dates, times, datetimes,
UUIDs, enums, and strings of at most 1,024 characters. Unbounded text, JSON,
bytes and lists are left out, so bulk deletes of large rows fit in the outbox.
Declare the set to change it:

```go
golem.Subscriptions[Note](golem.DeleteSnapshot[Note](Notes.Published))
```

A read rule or filter that tests a field outside the snapshot makes deletes
unknown for that subscriber, which ends the stream with `RESYNC`. When such a
stream opens, golem reports the missing fields once to the observer
(`events.ObservationDeleteCoverage`); tests and staging logs show it early.

A row whose snapshot exceeds `MutationLimits.MaxDeleteSnapshotBytes` (64 KiB by
default, 1 MiB at most) is stored with its identity only, and reported as
`events.ObservationDeleteSnapshotOversize`. The delete itself succeeds.
Subscribers can then decide it only from identity fields, so most of them get
`RESYNC`.

### When to use a change-log model

Events tell a subscriber that a row changed and let it read the current state.
They do not carry old values, a deleted row's contents, or every intermediate
state. If a consumer needs those, or needs deletes it can always authorize,
write a change-log model, such as `DocumentChange`, from a hook or an
operation in the same transaction, and subscribe to its created events. Its
rows never change, and they carry whatever the consumer needs, including a
deleted row's contents. Give it a read rule on its own fields, such as an
owner ID copied into each entry, so that who sees an entry never depends on a
parent row the same transaction removed.

### Streams end with a code

`Recv` returns an error when the stream ends. `events.CodeOf(err)` gives its
code:

| Code | Why | What the client does |
|---|---|---|
| `GOLEM_SUBSCRIPTION_CANCELLED` | you called `Close`, or the context passed to subscribe ended | nothing |
| `GOLEM_SUBSCRIPTION_RESYNC` | events may have been missed (the transport dropped, or a delete or an exit could not be judged), `InvalidatePolicies()` was called, or access to a model the stream reads changed (reason `DEPENDENCY_CHANGED`, see [A stream that reads another model](#a-stream-that-reads-another-model)) | reload the state you hold, then subscribe again |
| `GOLEM_SUBSCRIPTION_OVERFLOW` | the subscriber fell behind and its queue (`EventLimits.SubscriberQueue`) filled | reload and subscribe again; read faster or raise the queue |
| `GOLEM_SUBSCRIPTION_REVALIDATION` | the principal no longer resolves, or authorization could not be checked | re-authenticate, then subscribe again |
| `GOLEM_SUBSCRIPTION_FORBIDDEN` | a re-check found the caller may no longer read this stream | stop; do not resubscribe with the same principal |
| `GOLEM_SUBSCRIPTION_LIMIT` | the caller already holds the most streams allowed; returned when subscribing | close another stream first |
| `GOLEM_SUBSCRIPTION_SOURCE_CLOSED` | the server is shutting down, the event source failed, or an event did not match this application's event schema, such as one recorded by an older version | reconnect with backoff, reload, subscribe again |

`GOLEM_SUBSCRIPTION_INVALID` refuses a malformed subscription when it is
opened. With reason `DEPENDENCY_UNOBSERVABLE` it refuses a stream whose rule or
filter reaches a model that has no `Subscriptions`.

A deadline or cancellation on the context passed to one `Recv` call does not
end the stream. That `Recv` returns an error that wraps the context's error,
so `errors.Is(err, context.DeadlineExceeded)` holds, and the next `Recv`
receives the next event. Use a per-call deadline to wake up periodically; use
`Close`, or cancel the context you subscribed with, to end the stream.

### Re-checking long-lived streams

An event is judged by the subscriber's policy when it arrives, but a stream
that receives nothing is never judged. Two settings end streams whose caller
has lost access:

- **`EventLimits.RevalidateEvery`** (off by default, at least 1s, at most 24h) re-resolves
  the principal, re-checks the read grant and re-runs the read hooks of every
  stream on a jittered interval. A principal that no longer resolves ends the
  stream with `REVALIDATION`; a read grant that is gone ends it with
  `FORBIDDEN`.
- **`golem.EventRecheck[M](interval, check)`** adds your own check to one
  stream, on an interval of at least 1s, for revocation the policy cannot see, such as removal from a team
  through a relation:

  ```go
  stream, err := caller.Notes.Events(ctx, golem.EventRecheck[notes.Note](time.Minute,
  	func(ctx context.Context) error { return stillMember(ctx, teamID) }))
  ```

  An error ends the stream with `FORBIDDEN`, unless it is an `events` error
  carrying `REVALIDATION`, `RESYNC` or `SOURCE_CLOSED`, which is passed on. A
  panic ends it with `SOURCE_CLOSED`.

For GraphQL subscriptions, `GraphQLConfig.SubscriptionRecheck` takes an
`Interval` of at least 1s and a `Check(ctx, principal, operation) error`, set
both or neither. A failing check ends only that operation.

### Caps

`EventLimits.MaxSubscriptionsPerConnection` (32 by default, at most 256)
bounds the subscriptions on one GraphQL WebSocket connection. One more is
refused as an error on that operation, with `GOLEM_SUBSCRIPTION_LIMIT`, and the
connection stays open.

`EventLimits.MaxSubscriptionsPerPrincipal` (at most 65,536) bounds the streams one principal
holds across Go, WebSocket and SSE. It needs `Config.SubscriptionPrincipalKey`,
a `func(P) (string, bool)` that names the principal; returning `false` leaves
that principal uncounted. Subscribing beyond the cap fails with
`GOLEM_SUBSCRIPTION_LIMIT`. Both caps count streams in one process: with
several replicas, a principal can hold the cap on each.

### GraphQL subscriptions over WebSocket and SSE

The GraphQL handler serves subscriptions over `graphql-transport-ws` on a
WebSocket upgrade, and over Server-Sent Events in graphql-sse's
distinct-connections mode on `POST`. The `Accept` header chooses:

- `text/event-stream` preferred over every JSON type, by quality: every
  operation is answered as an event stream, a query or mutation with one `next`
  then `complete`;
- `text/event-stream` at the same or lower quality than `application/json`,
  `application/graphql-response+json` or a matching wildcard: subscriptions are
  streamed, queries and mutations get plain JSON;
- no `text/event-stream`, or quality 0: plain JSON, and a subscription is
  refused with `SUBSCRIPTION_TRANSPORT_REQUIRED`.

A stream sends a `next` frame per event and `complete` when it ends. A terminal
error is sent as a final `next` carrying `errors` with the code in
`extensions.code`, then `complete`. Refusals before the stream starts are
ordinary JSON errors. Keep-alive comments are sent every
`EventLimits.KeepAlive` (30s by default), which applies to WebSocket pings too;
`WebSocketKeepAlive` is its deprecated alias, and setting both to different
values is refused with `GOLEM_EVENT_CONFIG`. A client disconnect ends the
subscription, and `Shutdown` ends every stream.

#### The live handshake and the subscription ack

A subscription is served only to a client that declares the live protocol it
speaks and the contract it was generated from. A client that does not is
refused; there is no fallback live path. Plain HTTP queries and mutations need
no declaration.

The declaration is the `golem` block `{"protocol": 1, "contract": "<hex>"}`.
`contract` is the server's contract fingerprint, the lowercase hex of
`Server.ContractFingerprint()`, which a generated client carries.

- **WebSocket.** Put the block in `connection_init.payload` under the key
  `golem`. golem removes the key before it passes the rest of the payload to
  `WebSocketInit`, so `WebSocketInit` sees only your own fields. The block is
  checked before `WebSocketInit` runs. A `subscribe` message must not carry
  `extensions`.
- **SSE.** Put the block in the request's `extensions.golem`. The envelope
  accepts no other extension key.
- **Plain HTTP.** A query or mutation without the block runs unchanged. When
  the block is present it is checked, and a mismatch is refused before the
  operation runs.

A block that is absent, not an object, has an unknown field, or names another
protocol is a protocol mismatch. A well-formed block that names another
contract is a contract mismatch, which means the client was generated from a
different schema than this server and must be regenerated.

| Mismatch | WebSocket | HTTP and SSE |
|---|---|---|
| protocol | close `4426`, reason `golem:protocol` | `GOLEM_PROTOCOL_MISMATCH` |
| contract | close `4426`, reason `golem:contract` | `GOLEM_CONTRACT_MISMATCH` |

SSE answers a mismatch with status 400 and a JSON error, like every refusal
before a stream starts.

Once a subscription is registered, the server sends one `next` frame before any
event: `{"data":null,"extensions":{"golem":{"live":true}}}`. It is sent exactly
once per operation, on WebSocket and SSE alike. A subscription that is refused
(validation, authorization, the subscription cap) gets its error and no ack. A
client that needs a consistent view subscribes, waits for the ack, and then
fetches: a commit after the ack arrives as an event, and a commit before it is
in the fetch.

#### The client manifest

`golem generate` writes `zz_golem_client.gen.ts` beside the SDL, from the same
compiled contract, in every GraphQL-mode application. It is the one file a
generated client reads to know the shape of your API without parsing the SDL,
and it is listed in `.golem/generated-manifest.json` like every other generated
file, so `golem check` fails when it is edited by hand or falls behind the
schema. Regenerate it; never edit it.

It exports `golemClientManifest` (`as const`) and its type
`GolemClientManifest`, and holds only these facts:

- `contract`: the contract fingerprint, the value a client sends as the
  `contract` of the `golem` block (see the handshake above).
- `models`, keyed by GraphQL type name. Each model has:
  - `primaryKey`: the primary-key fields, in key order;
  - `cursorSelector`: the name of the unique input a `cursor` uses for the
    primary key, a plain value for a one-field key and an object keyed by the
    `primaryKey` fields for a compound key;
  - `uniqueInput`: the GraphQL type name of the model's unique input, read from
    the `cursor` argument of the list root (or the `where` argument of the
    single-row root), or `null` when the model has neither root; a client
    declares a cursor variable with it;
  - `events`: the name of the model's events subscription root, or `null`;
  - `relations`, keyed by relation field: its `target` type, whether it is a
    `list`, whether the field is `nullable` in the schema, and its `inverse` field
    on the target, or `null` when the other side is not exposed;
  - `computed`, keyed by computed field: the models whose rows its result holds.
- `roots`, keyed by every Query and Mutation field in the SDL: `kind` is
  `generated` for the roots golem builds from a model and `opaque` for custom,
  semantic and full-text roots. `models` means "the models whose changes this
  root's result depends on": the owning model for a generated root (aggregate,
  groupBy and the many-row mutations included), and for an opaque root the
  models whose rows the result holds, possibly none. `shape` is the result
  shape the client patches the root with:
  - `rows`: a list of model rows (`findMany`, and a custom root returning a list
    of a model);
  - `row`: one model row (`findOne`, `create`, `update`, `upsert`, and
    a custom root returning one model);
  - `deletedRow`: the generated `delete` root, which returns the row it removes;
  - `aggregate`: the `aggregate` root's result;
  - `groupBy`: the `groupBy` and `relationGroupBy` roots' result;
  - `updateMany` and `deleteMany`: the many-row mutations' counts;
  - `value`: a custom root whose result is neither a model nor a list of one.

  `nullable` is `true` when the root's schema type is nullable (`findOne` is, a
  mutation's row is not). A client sets a single-row slot to `null` only when it
  is `nullable`.

The file is absent in kernel mode (`WithoutGraphQL`), which has no GraphQL layer.

To use it from a front end, see [REACT.md](./REACT.md) and the
[client README](../../client/README.md#how-live-queries-work).

#### The socket deadline

A live socket closes at a deadline, and a reconnect resolves the principal
again. This is how a revoked credential or a changed role stops reaching an
open socket: no event judges it, the deadline does. One rule derives the
deadline from the context of the socket's principal, which is the context
`WebSocketInit` returns on WebSocket and the request context on SSE:

1. **The context has a deadline.** That is the credential expiry, so set it
   where you resolve the principal: `context.WithDeadline(ctx, expiresAt)` with
   the token's or the session's expiry. A principal that is also marked (below)
   still uses the deadline.
2. **No deadline, and the context is marked, and `EventLimits.RevalidateEvery`
   is set.** The deadline is the open time plus `RevalidateEvery`. This is for
   principals whose credential never expires, such as API keys.
3. **Otherwise the live handshake is refused** with
   `GOLEM_PRINCIPAL_DEADLINE_REQUIRED`: WebSocket close `4426` with reason
   `golem:GOLEM_PRINCIPAL_DEADLINE_REQUIRED`; SSE status 400 with that code in
   a GraphQL error whose message names the fix. HTTP queries and mutations are
   never refused for want of a deadline.

When the deadline passes the server closes the WebSocket with `4401` (an SSE
stream ends). The client gets fresh credentials and reconnects; the server
resolves the principal from those credentials, and the client refetches its
live queries after the acks. Nothing is carried over from the old socket.

**The marker.** `golem.WithoutCredentialExpiry(ctx)` declares that the principal
this context carries has no credential expiry, so `RevalidateEvery` bounds its
socket instead. `golem.HasNoCredentialExpiry(ctx)` reads it. Set it only in
your own server-side code that resolves the principal, for example in the
function that turns an API key into a principal, or in `WebSocketInit` for a
socket authenticated that way. golem reads it only from the context value, never
from a client: a field in the `golem` block is a protocol mismatch, and a
payload field or a header such as `X-Golem-No-Credential-Expiry` has no effect.
Do not mark a principal that has an expiry; give it the deadline.

```go
func resolveAPIKey(ctx context.Context, key string) (context.Context, error) {
	principal, err := lookup(ctx, key)
	if err != nil {
		return nil, err
	}
	return golem.WithoutCredentialExpiry(withPrincipal(ctx, principal)), nil
}
```

`RevalidateEvery` has no default; unset, a marked principal is refused.

## Event transports

`EventTransport` decides which processes hear an event:

- **`events.NewMemoryTransport`** is process-local. Only subscribers in the
  process that runs `RunEventPublisher` receive events.
- **`events/nats`** (`nats.Open(ctx, database, nats.Config{...})`) publishes
  over Core NATS to every process subscribed with the same `SubjectPrefix`.
  Give each database its own prefix.

With the memory transport, run the publisher and every subscriber in one
process. Other processes may write to the database, but must not run a
publisher or subscribe. A second process-local publisher on the same database
claims part of the outbox, and the events it claims never reach the first
process's subscribers.

golem watches for this on PostgreSQL. `RunEventPublisher` with a
process-local transport tries to hold a session advisory lock for as long as
it runs.

- **By default it observes.** A publisher that cannot take the lock reports an
  `events.ObservationTopology` failure to the observer, keeps publishing, and
  keeps trying to take the lock.
- **Strict mode refuses.** With
  `EventLimits.RequireExclusiveProcessLocalPublisher`, a publisher that cannot
  take the lock returns `GOLEM_EVENT_CONFIG` and publishes nothing. If the
  lock's connection drops later, it tries to take the lock again and stops
  with the same error after three failures.
- A subscriber in a process that runs no local publisher is reported once per
  application.

SQLite takes no lock. A cross-process transport such as `events/nats` needs
none: several processes may each run a publisher and claim part of the outbox,
because every subscriber hears every publisher.

## Changing a schema

Editing a model means a new migration before regeneration:

```
golem migration new --schema ./notes --name add-author
golem generate --schema ./notes --app-out ./notes
golem migration apply --provider sqlite --dsn "file:notes.db"
```

`golem migration plan` shows what a migration will do before it runs, and
`golem check --schema ./notes --app-out ./notes` fails when generated code no longer matches
the schema — run it in CI.

### Approvals

Every operation carries a risk label: `safe`, `locking`, `rewrite`,
`dataLoss` or `manual`. `migration new` refuses until each operation that
needs review is approved with `--approve <operation-id>`. When several are
missing, it lists all of them in one error, with a ready-made
`--approve ... --approve ...` line to rerun with. Approval is required for:

- every column type change (`alterColumnType`), including a value-preserving
  widening, which is labelled `rewrite`. Raising or removing a string's length
  limit is such a widening on both providers: SQLite rebuilds the table and
  PostgreSQL alters the column type, and either way the operation is labelled
  `rewrite` and needs `--approve`;
- every `dataLoss` operation: dropping a table or column, making a column
  required, adding a unique key, primary key or check that existing rows may
  violate, and any type change that is not a widening;
- a reviewed backfill (`manual`) and the initialization of a new optimistic
  concurrency column.

A constraint that the migration drops and re-adds while still accepting every
value it accepted before needs no approval and is labelled `locking`, because
the database re-validates it. That covers renaming a table or column (golem
derives constraint names from both) and making a column optional.

### What each provider changes in place

- Renaming a table or column keeps its data. When another table's foreign
  key refers to a renamed table or key column, SQLite rebuilds the referring
  table so the reference follows the new name, and PostgreSQL renames the
  referenced key constraint rather than dropping it.
- Raising or removing a string's length limit (`varchar(200)` to
  `varchar(500)`, or to an unbounded string) works on both providers. SQLite
  rebuilds the table; PostgreSQL alters the column type. On both it is an
  `alterColumnType` operation labelled `rewrite`, so `migration new` needs
  `--approve <operation-id>` for it. Lowering or adding a limit is refused on
  both, because existing values might not fit.
- Field order. SQLite keeps columns in declared order: reordering fields, or
  inserting a field anywhere but at the end, rebuilds the table. PostgreSQL
  cannot reorder columns in place, so a new field is always appended
  physically and a reorder changes no DDL. Physical column order is not part
  of PostgreSQL drift checking; the set of columns and their definitions is.
- A required field with no default cannot be added while SQLite is a
  provider: declare a default or make the field optional. Only when
  PostgreSQL is the schema's sole provider can one required field per
  migration be added with a reviewed backfill.

### Adding row versions to an existing database

The first migration after upgrading to a golem that has row versions adds the
`golem_version` column to every model with `Subscriptions` and creates the
version source (the `CACHE 1` sequence on PostgreSQL, the counter table on
SQLite) before the first column is added. Adding the column is an ordinary
additive change and needs no approval. On PostgreSQL it sets a constant default
and does not rewrite the table; on SQLite it is an `ALTER TABLE ... ADD
COLUMN`. Existing rows keep version 0.

### Events written before a regeneration

Each queued event records the schema that wrote it. After a regeneration that
changes a model's event schema, or moves the generated contract to a newer
format, the new build cannot decode the undelivered events written under the old
schema. The publisher blocks each one with
`last_failure_code = 'schema-unavailable'`, visible through
`EventOperator().Inspect`, and its decode error is
`GOLEM_EVENT_SCHEMA_UNAVAILABLE`. Retire it with `EventOperator().Retire`, or
deliver it first: run the publisher on the old build until
`SELECT COUNT(DISTINCT o."causation_id") FROM "_golem_outbox" o LEFT JOIN
"_golem_outbox_delivery" d ON d."causation_id" = o."causation_id" WHERE
d."causation_id" IS NULL OR d."status" IN ('pending', 'leased', 'blocked')`
returns 0. On PostgreSQL, qualify both tables with the system namespace. A
bundle generated by an older golem is refused in `HistoricalEventBundles` when
its contract format is no longer supported; the release notes of the release
that drops a format give the exact drain steps.

The stored event format itself is versioned. go/v0.8.1 reads only the current
format, which stores the row version with every fact; facts written by go/v0.8.0
or earlier (formats V1 and V2) are blocked with
`last_failure_code = 'format-retired'` and the decode error
`GOLEM_EVENT_FORMAT_RETIRED`, and nothing converts them. Drain on go/v0.8.0
before upgrading, or retire the blocked delivery and lose its events.

### Drift

Application startup and `golem doctor` compare the live database with the
reviewed migrations and report any difference as drift. Temporal columns
must match exactly, including the time zone: a `timestamp with time zone`
column altered to `timestamp without time zone` (or a `time` column gaining a
time zone) is drift.

## Limits and error codes

Every refusal golem makes to a caller is a `*golem.Error`. Its `Error()` text
is `CODE: message`, and the message never echoes a value you sent. `Unwrap`
returns the internal cause, for your logs only.

| Field | Holds |
|---|---|
| `Code` | one of the codes below |
| `Reason` | why, when golem can say; empty otherwise |
| `Operation`, `Model` | the operation and the model it ran on |
| `Field` | the field the refusal is about, under the rules below |
| `InputField` | the name of the caller's input the refusal is about, set only by `golem.InputError`; sent to GraphQL clients as `extensions.field` |
| `Limit`, `LimitMax` | for `LIMIT_EXCEEDED`, the limit's name and the maximum in force |
| `Message` | the public message |

### Codes

| Code | Meaning |
|---|---|
| `BAD_USER_INPUT` | the request is malformed, or a value is invalid |
| `NOT_FOUND` | the target does not exist, or the caller cannot read it; the two are indistinguishable |
| `UNAUTHENTICATED` | the principal did not resolve, or the caller's policy could not be built |
| `FORBIDDEN` | policy refuses the request |
| `CONFLICT` | a concurrent write, a lock or a unique key got in the way; retrying may succeed |
| `LIMIT_EXCEEDED` | the request exceeds a configured limit |

### Reasons

| Reason | Code | Meaning |
|---|---|---|
| `VALUE_TOO_LONG` | `BAD_USER_INPUT` | a value is longer than the field allows |
| `VALUE_PRECISION` | `BAD_USER_INPUT` | a decimal or time value is finer than the field's precision, or a datetime operand is finer than a microsecond where it cannot be rewritten (see [Datetimes](#datetimes)) |
| `VALUE_RANGE` | `BAD_USER_INPUT` | a value is outside the range the field supports |
| `INVALID_VALUE` | `BAD_USER_INPUT` | a value is invalid for its field in some other way |
| `LIMIT_EXCEEDED` | `LIMIT_EXCEEDED` | always set with `LIMIT_EXCEEDED` |
| `NOT_GRANTED` | `FORBIDDEN` | the caller has no grant at all for this action on this model |
| `FIELD_NOT_READABLE` | `FORBIDDEN` | the request uses a field where the caller cannot read it |
| `AUTHORIZATION_UNPROVEN` | `FORBIDDEN` | golem cannot establish that the rows a write locates are readable |
| `POLICY_INVALID` | `FORBIDDEN` | the policy cannot be applied to this request (see [When a policy cannot be applied](#when-a-policy-cannot-be-applied)) |

`NOT_GRANTED`, `AUTHORIZATION_UNPROVEN` and `POLICY_INVALID` are decided from
the policy and the request alone, before any row is read, so they reveal
nothing about stored data. A `FORBIDDEN` or `CONFLICT` without a reason, such
as `FORBIDDEN: mutation is not authorized`, is still possible.

### Limits

| `Limit` | Set by | Default |
|---|---|---|
| `touched_rows` | `MutationLimits.MaxTouchedRows`: rows one mutation writes, cascades included | 1,000 |
| `batch_rows` | the rows an `UpdateMany` or `DeleteMany` captures, or a nested `CreateMany` creates, bounded by the touched-row budget | 1,000 |
| `facts` | `MutationLimits.MaxFacts`: change events one transaction records | 1,000 |
| `outbox_bytes` | `MutationLimits.MaxOutboxBytes`: the encoded size of those events | 1 MiB |
| `nested_depth` | `MutationLimits.MaxNestedDepth` | 5 |
| `statement_parameters` | `MaxStatementParameters` on `MutationLimits` and `ReadLimits` | 999 |
| `statement_size` | `ReadLimits.MaxStatementBytes` | 1 MiB |
| `statement_aliases` | `ReadLimits.MaxStatementAliases` | 2,048 |
| `selected_fields` | `ReadLimits.MaxSelectedFields` | 256 |
| `depth` | `ReadLimits.MaxRelationDepth` | 5 |
| `take` | `ReadLimits.MaxTake`, or a model's own maximum | none |
| `fanout` | `ReadLimits.MaxRelationFanout` | none |
| `transaction_ordinal` | the number of events one transaction can number | 2³² − 1 |
| `list_items` | the `golem.MaxItems` or `golem.GraphQLList` maximum a custom operation or computed field declares for its list result | declared |

The mutation defaults are also ceilings: you may lower them, never raise them.
A limit that only the fields or relation hops your policy needs would exceed is
`POLICY_INVALID`, not `LIMIT_EXCEEDED`, because the caller cannot fix it.

Deletes and other writes are sized differently in the outbox. A created or
updated event records only the row's identity, because subscribers read the
row when the event is delivered. A deleted event also records the row's
[delete snapshot](#the-delete-snapshot). A snapshot over
`MutationLimits.MaxDeleteSnapshotBytes` does not fail the delete: it is stored
as identity only. The transaction as a whole still fails with `outbox_bytes`
once its events exceed `MaxOutboxBytes`.

### `Field`

- A value refusal names the field whose value was refused.
- A read refusal names a field only when your request named it: in a filter,
  an order, `Distinct`, `Omit`, a selector, a cursor or a selection. A field
  that only the policy uses is never named, so the error cannot reveal how the
  policy is written.
- A create refused because one of the fields you set has no create grant
  names that field.
- `fanout` and nested `batch_rows` limits name the relation field you used.
  Other limits name no field.

### Testing errors

`ErrorCode` and `Reason` are errors themselves, and `*golem.Error` matches
both with `errors.Is`:

```go
switch {
case errors.Is(err, golem.CodeLimitExceeded):
	var failure *golem.Error
	if errors.As(err, &failure) && failure.Limit == golem.LimitTouchedRows {
		return fmt.Errorf("split the delete: at most %d rows", failure.LimitMax)
	}
case errors.Is(err, golem.ReasonNotGranted):
	return errPermission
case errors.Is(err, golem.CodeConflict):
	return retry()
}
```

Event stream errors are `*events.Error`. `events.CodeOf(err)` returns the
code, and `errors.Unwrap` returns the cause, such as the context error of an
ended `Recv`.

### Over GraphQL

A `golem.Error` becomes a GraphQL error whose message is the public message,
with `extensions.code`, plus `extensions.reason` when there is a reason and
`extensions.limit` and `extensions.max` for a limit, plus `extensions.field`
when the error carries an `InputField`:

```json
{"message": "request exceeds the touched_rows limit of 1000",
 "extensions": {"code": "LIMIT_EXCEEDED", "reason": "LIMIT_EXCEEDED", "limit": "touched_rows", "max": 1000}}
```

`Field` and `Model` are not sent. To refuse an input your own code validated,
return `golem.InputError(reason, operation, field, message, cause)`: it is a
`BAD_USER_INPUT` error whose `field` reaches the client as `extensions.field`,
and `cause` stays in your logs. Any error that is not a golem or events
error is reported to `ReportInternalError` and sent as
`INTERNAL_SERVER_ERROR`. The GraphQL layer's own codes, such as
`GRAPHQL_VALIDATION_FAILED`, `QUERY_LIMIT_EXCEEDED` and `INPUT_LIMIT_EXCEEDED`,
are described below.

### Errors for a request the server refuses

A request that fails before it reaches an operation gets the same errors over
HTTP, server-sent events and `graphql-transport-ws`. Over WebSocket they arrive
as the payload of the `error` message. An error never repeats text from the
request: its message is fixed by golem, so a field, argument, variable or
literal you sent cannot come back in it.

**Validation.** Each problem the GraphQL validator finds is one error with code
`GRAPHQL_VALIDATION_FAILED`, a fixed message for its rule, `extensions.rule`
with the rule's name, and `locations`, the line and column in your document
(`graphql.Error.Locations`, a list of `graphql.Location{Line, Column}`, at most
four per error). At most eight errors are returned; when there are more, a last
error reads `and 4 more validation errors`.

```json
{"message": "GraphQL field does not exist on the selected type",
 "locations": [{"line": 1, "column": 3}],
 "extensions": {"code": "GRAPHQL_VALIDATION_FAILED", "rule": "FieldsOnCorrectType"}}
```

These errors are the same whether `Introspection` is on or off. The flag only
decides whether an operation that selects `__schema` or `__type` is allowed; if
not, it is refused with `GRAPHQL_VALIDATION_FAILED` and the message
`GraphQL introspection is disabled`.

**Limits.** A limit error names the limit that tripped and its value, in the
message and in `extensions.limit` and `extensions.max`, the same keys a runtime
limit uses:

```json
{"message": "GraphQL query exceeds MaxDepth (12)",
 "extensions": {"code": "QUERY_LIMIT_EXCEEDED", "limit": "MaxDepth", "max": 12}}
```

`INPUT_LIMIT_EXCEEDED` covers `MaxVariableBytes`, `MaxInputNodes`,
`MaxInputDepth` and `MaxListItems`. `QUERY_LIMIT_EXCEEDED` covers
`MaxRequestBytes`, `MaxTokens`, `MaxASTNodes`, `MaxFragments`, `MaxDepth`,
`MaxSelectedFields`, `MaxAliases`, `MaxComplexity`, and the introspection
repetition limits `MaxSchemaRoots` (1) and `MaxTypeRoots` (8). A complexity
error also carries the operation's cost:

```json
{"message": "GraphQL query complexity 22 exceeds MaxComplexity (20)",
 "extensions": {"code": "QUERY_LIMIT_EXCEEDED", "limit": "MaxComplexity", "max": 20, "cost": 22}}
```

A list field multiplies the cost of what it selects by the number of items it
can return:

- Its `take` argument, by magnitude, when the request gives one.
- Otherwise its `take` default in the schema. Relation lists and `findMany`
  roots advertise the listed model's default page size, and generated semantic
  and full-text searches advertise that default narrowed by their maximum.
- Otherwise its declared bound: the product of the `MaxItems` levels of a custom
  or computed list, or `MaxGroups` for `groupBy` and `relationGroupBy` (the
  smaller of the model's maximum and the runtime `MaxGroups`).

`MaxPageSize` is not part of the cost. It is the ceiling for `take`, so a query
that selects `me { ... }` over a list declared `MaxItems(1)` costs as one row.
A `groupBy` without `take` costs `MaxGroups` rows, so give it a `take` when the
query would otherwise exceed `MaxComplexity`.

A server you build directly with `graphql.NewServer` needs the same facts. Its
`Executor` must return them from `ListBounds()`, keyed `"Type.field"`, for every
list of objects that has no `take` default. `NewServer` refuses an executor that
omits one, that declares one for any other field, or that declares a bound below
1. Generated executors provide the table. A generated executor also refuses to
start when a model's default page size is above `MaxPageSize`, because the
schema would advertise a `take` the runtime rejects.

The cost is exact; it stops at the largest integer if the operation's fan-out
overflows it. A request with no `query` is `BAD_REQUEST`
(`GraphQL request requires a query`), not a limit error.

### Errors for a value the operation cannot use

A document can be valid and still carry a value that cannot be bound: a `take`
past the model's maximum, a UUID that is not canonical, an empty `where`. Such a
read is refused before any SQL runs, with code `BAD_USER_INPUT`:

```json
{"message": "Argument take on User must be between -500 and 500, excluding 0",
 "path": ["users"],
 "extensions": {"code": "BAD_USER_INPUT", "reason": "INVALID_VALUE", "argument": "take", "min": -500, "max": 500}}
```

`path` is the response name of the root field that was refused. `argument` is
the dotted input path, `where.id.equals` or `orderBy[0].id`. `reason` is
`INVALID_VALUE` for a value that is out of range or malformed, and
`LIMIT_EXCEEDED` for a bound on the input or selection (`limit` names it:
`MaxInputNodes`, `MaxInputDepth`, `MaxListItems`, `MaxDepth`,
`MaxSelectedFields`, `MaxAliases`), with `max` for its value. `min` and `max`
carry a numeric range.

| Message form | Cause |
|---|---|
| `Argument x on M cannot be null` | an argument or input member is `null`, or an omitted `$variable` was given to it |
| `Argument x on M must not be empty` | an empty `where`, `AND`, `OR`, `NOT`, `orderBy` or `distinct` |
| `Input x on M must set exactly one member` | a unique selector or an `orderBy` entry names none or several |
| `Input x on M is required` | a compound selector lacks a component |
| `Argument take on M must be between -N and N, excluding 0`, `... must not be 0`, `Argument skip on M must be at least 0` | paging outside the allowed range |
| `Argument take on M is negative and requires orderBy` | a negative `take` without an `orderBy` |
| `Input x on M is not a valid T`, `... is not a canonical T`, `... is outside the T range`, `... must be of type T`, `... exceeds the maximum length of N`, `... must not contain NUL characters` | a scalar value that is malformed, not in its canonical text, out of range, of the wrong type, too long, or holds NUL |
| `Input x on M is not supported for this field or provider` | an operator the field or the provider does not support |
| `Argument x on M names a field more than once` | a repeated `orderBy` or `distinct` field |
| `Input x on M is not available for this operation` | an unknown or unreadable field |
| `Field f on M requires a selection set`, `... must not have a selection set` | a selection that does not match the field's type |
| `Directive @skip requires a Boolean if` | a `@skip` or `@include` without a Boolean `if` |
| `Input x on M is only allowed on numeric fields`, `... must be true`, `Argument where on M is not accepted by create` | a write operation the field, the relation or the root does not allow |
| `Input x on M is not allowed while creating`, `... is not allowed on a to-one relation`, `... requires an expectation because the target model is versioned` | a nested write the relation or the target model does not allow |
| `Argument expectedVersion on M must be at least 1`, `Argument expectation on M must set exactly one member` | an optimistic-concurrency claim that is missing a version or names none or several |
| `Batch writes are not available on M` | `updateMany` or `deleteMany` on an optimistic-concurrency model |
| `An aggregate on M must select at least one measure` | an `aggregate` that selects only `__typename` |
| `Input having.….mode on M is not supported for this field or provider` | `mode` set on `equals`, `not`, `lt`, `lte`, `gt` or `gte`; it applies only to `contains`, `startsWith` and `endsWith` |
| `Key x on M must be listed in by` | a selected key, an `orderBy` key or a `having` key that `by` does not group |
| `Relation grouping on M requires a relation dimension in by` | `relationGroupBy` whose `by` names only local fields |
| `Input exceeds L (N)`, `Selection exceeds L (N)` | the input or selection bound named by `extensions.limit` |

`M` is a model, operation or computed field name from your schema. The message
never contains text from your request. If the operation is valid and golem still
cannot compile it, because its own contract is inconsistent, the response is
`INTERNAL_SERVER_ERROR` with `internal server error`, and the cause goes to
`ReportInternalError`. A subscription that cannot be bound returns the same typed
error from `Subscribe`; `graphql.PresentError` turns it into this response.

The same forms cover writes and analytics. A mutation names the input it
refused (`Input data.name on User must set exactly one member`,
`Input data.posts.create[1].id on Post is not a valid UUID`); a list
position is shown as `[n]`. An optimistic-concurrency write adds
`Argument expectedVersion on M must be at least 1`. `aggregate`, `groupBy` and
`relationGroupBy` add the forms below. A request refused here has run no SQL, so
a refused mutation has no effect.

## Errors you will meet early

| Error | Cause |
|---|---|
| `P1_BINDING_POLICY_REQUIRED` | an exposed model has no `DefinePolicy` |
| `generated applications require a reviewed non-empty migration history` | `generate` ran before `migration new` |
| `CONFLICT: mutation conflicted` | a unique constraint rejected the write, or on PostgreSQL a concurrent write touched the same rows, which a retry resolves |
| `FORBIDDEN: operation is not granted` | the caller's policy grants no update, delete or create on the model at all (see [Write rules](#write-rules)) |
| `LIMIT_EXCEEDED: request exceeds the touched_rows limit of 1000` | a write, with its cascades, touches more rows than `MutationLimits.MaxTouchedRows` |
| `GOLEM_SUBSCRIPTION_RESYNC` | an event stream may have missed events; reload and subscribe again (see [Streams end with a code](#streams-end-with-a-code)) |
