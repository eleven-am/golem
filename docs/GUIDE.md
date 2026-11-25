# Golem user guide

**Go module:** `go/v0.9.2`
**TypeScript / React client:** `0.3.1`

Golem has two independently released backend implementations. Start with the Go
chapters for a Go application. The Prisma/NestJS backend has its own section,
packages and upgrade requirements; its APIs and guarantees are not interchangeable
with the Go module. The browser client described here targets the Go live protocol.

Use Go 1.26.11 or later and matching CLI/library versions. Install the Go module:

```sh
go install github.com/eleven-am/golem/go/cmd/golem@v0.9.2
go get github.com/eleven-am/golem/go@v0.9.2
```

Automatic event writing remains V3. Named-fact declarations and V3/V4 readers ship
in this release; named-fact runtime admission and V4 writer activation remain
deferred. NAS performance and durability remain unverified.

## Contents

- [Go quickstart](#quickstart)
- [Working safely](#working-safely)
- [Go application guide](#application)
- [Durable jobs and actions](#queue)
- [Full-text search](#fulltext)
- [Semantic search](#semantic)
- [Serving a frontend](#render)
- [SQLite durability and storage](#sqlite)
- [Preparing a Go server for React](#react-server)
- [TypeScript client](#client)
- [React setup and hooks](#react)
- [Observability and application testing](#observability)
- [Public API and compatibility](#public-api)
- [Upgrading Go applications](#upgrading)
- [TypeScript / NestJS backend](#typescript-backend)
- [NestJS integration](#typescript-nest)
- [TypeScript engine contracts](#typescript-core)
- [Prisma generator contracts](#typescript-generator)
- [CASL adapter](#typescript-authorizer)
- [Condition evaluator](#typescript-policy)
- [TypeScript durable queue](#typescript-queue)
- [TypeScript frontend serving](#typescript-render)
- [TypeScript upgrade requirements](#typescript-upgrades)

For source layout, internals, repository verification and releases, see [development](DEVELOPMENT.md).

<!-- chapter:quickstart -->
<a id="quickstart"></a>

## Go quickstart

An empty directory becomes a running Golem application in four `golem`
commands. `TestQuickstartFromEmptyDirectory` exercises those four commands and the application program against the checkout, using a disposable module with a local replacement for Golem.

The example is a note-taking application on SQLite.

<a id="quickstart-1-a-module-and-a-schema"></a>

### 1. A module and a schema

```
go install github.com/eleven-am/golem/go/cmd/golem@v0.9.2
mkdir notes-app && cd notes-app
go mod init example.com/notes
go get github.com/eleven-am/golem/go@v0.9.2
```

A schema is ordinary Go. One file declares the actor, the models, and the
providers the application supports.

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

type Note struct {
	_ struct{} `golem:"model;id=example.notes.Note;table=notes;graphql=Note"`

	ID        golem.UUID `db:"id" golem:"id=example.notes.Note.ID;pk;default=uuid"`
	Title     string     `db:"title" golem:"type=varchar(200)"`
	CreatedAt time.Time  `db:"created_at" golem:"default=now;readonly"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "notes")
	golem.Actor[Actor](schema)
	golem.Model[Note](schema)
	golem.Providers(schema, golem.SQLite)
}
```

<a id="quickstart-2-a-policy-for-every-exposed-model"></a>

### 2. A policy for every exposed model

Golem refuses to generate an application whose models have no authorization.
Skip this and `golem inspect` exits 1 with a diagnostics document on stdout:

```
{
  "formatVersion": 1,
  "diagnostics": [
    {
      "code": "P1_BINDING_POLICY_REQUIRED",
      "severity": "error",
      "message": "exposed model Note has no DefinePolicy binding",
      ...
    }
  ]
}
```

A policy is a method on the model. It receives the resolved actor and records
what that actor may do.

```go
// notes/policies.go
package notes

import "github.com/eleven-am/golem/go/golem"

func (Note) DefinePolicy(rules *golem.Rules[Note], actor Actor) {
	if !actor.Authenticated {
		rules.CanRead(golem.All[Note]())
		return
	}
	rules.CanRead(golem.All[Note]())
	rules.CanCreate(golem.All[Note]())
	rules.CanUpdate(golem.All[Note]())
	rules.CanDelete(golem.All[Note]())
}
```

An unauthenticated caller may read and nothing else. There is no implicit
allow: what the policy does not grant, no caller can do.

<a id="quickstart-3-inspect-the-model-golem-derived"></a>

### 3. Inspect the model golem derived

```
golem inspect --schema ./notes
```

It prints the logical model as JSON — identities, table names, fields, and the
providers it will target. Read it once. It is what every later command
compiles against, and errors here are cheaper than errors after generation.

<a id="quickstart-4-create-the-initial-migration-then-generate"></a>

### 4. Create the initial migration, then generate

Order matters, and golem enforces it:

```
generated applications require a reviewed non-empty migration history for
every declared provider; create the initial migration before generating
```

Schema history comes first so generated code can never describe a database
that no migration produces.

```
golem migration new --schema ./notes --name init
golem generate --schema ./notes --app-out ./notes
```

`migration new` writes `migrations/`: the SQL, a manifest, and before/after
snapshots for review. `generate` writes the `zz_golem_*.gen.go` files and a
`zz_golem_graphql.schema.graphqls` beside your source, a `golemgqlgen/`
subpackage holding the GraphQL executable, and a `.golem/` directory holding
the manifest and snapshots it will check against later.

Both are reviewable artifacts. Commit them.

<a id="quickstart-5-apply-the-migration"></a>

### 5. Apply the migration

```
golem migration apply --provider sqlite --dsn "file:notes.db"
```

<a id="quickstart-6-use-the-application"></a>

### 6. Use the application

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

type Principal struct{ Authenticated bool }

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
			return notes.Actor{Authenticated: principal.Authenticated}, nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	caller, err := application.ForPrincipal(ctx, Principal{Authenticated: true})
	if err != nil {
		log.Fatal(err)
	}

	if _, err := caller.Notes.Create(ctx, notes.Notes.Create(
		notes.Notes.Title.Create("first note"),
	)); err != nil {
		log.Fatal(err)
	}

	all, err := caller.Notes.FindMany(ctx, notes.Notes.Where(golem.All[notes.Note]()))
	if err != nil {
		log.Fatal(err)
	}
	title, present := golem.Value(all[0], notes.Notes.Title).Get()
	fmt.Printf("%d note(s), first title=%q present=%t\n", len(all), title, present)
}
```

```
go run ./cmd/notes
1 note(s), first title="first note" present=true
```

`Principal` is yours: whatever your authentication produces. `ResolvePrincipal`
turns it into the `Actor` your policies are written against. Golem never sees
your credentials.

<a id="quickstart-reading-values"></a>

### Reading values

`golem.Value(row, field).Get()` returns the value **and whether it is present**.
That second return is not decoration:

```go
created, _ := caller.Notes.Create(ctx, notes.Notes.Create(notes.Notes.Title.Create("x")))
title, present := golem.Value(created, notes.Notes.Title).Get()
// title == "", present == false
```

A mutation returns nothing you did not select — not the fields you wrote, and
not the identity. Pass `notes.Notes.Select(notes.Notes.ID)` as a second
argument to get the identity back. A field is absent when it was not selected,
and absent when policy masks it — the two are deliberately indistinguishable,
so a caller cannot learn that a field exists by watching it disappear.

Ignoring `present` and printing the value gives you `""` and no indication
whether the note is untitled or the field was never fetched.

<a id="quickstart-where-to-go-next"></a>

### Where to go next

- Declare a second model and a relation between them; `golem inspect` shows
  what golem derived before you generate.
- `golem check --schema ./notes --app-out ./notes` verifies generated code still matches the
  schema. Run it in CI: it fails when someone edits a `zz_golem_*.gen.go`
  file or changes the schema without regenerating.
- `golem doctor --schema ./notes --provider sqlite --dsn "file:notes.db"` reports whether a
  live database matches the migrations.
- [REACT.md](#react-server) builds a live React front end on the application you
  just made.
<!-- /chapter:quickstart -->

<!-- chapter:working-safely -->
<a id="working-safely"></a>

## Working safely

1. **Every exposed model needs a policy.** There is no implicit allow; a model
   without `DefinePolicy` fails `inspect` and `generate` with
   `P1_BINDING_POLICY_REQUIRED`.
2. **`DefinePolicy` must be a pure function of the actor.** Built policies are
   cached per actor ([authorization](#application-authorization)). A policy that reads the clock, a global or the
   database is frozen at its first result. Put such inputs on the actor in
   `ResolvePrincipal`.
3. **A mutation returns nothing you did not select**, not even the ID. Pass
   `X.Select(X.ID)` as the last argument ([writing](#application-writing)). This bites custom
   mutations too: a resolver that returns the row of an `Upsert` or `Create`
   made without `Select` fails over GraphQL with
   `COMMITTED_RESULT_UNAVAILABLE` ("result row has no identity") after its
   writes committed. Verified.
4. **`Get()` returns `(value, present)`.** It reports `false` for an
   unselected field, a stored null and a policy-masked field. Never ignore the
   second return.
5. **Migration history before generation, and regenerate after every schema
   change.** `golem generate` refuses until `golem migration new` has written a
   history for every declared provider. Regenerating also rewrites the client
   manifest; ship it to the front end, or every live request fails with
   `GOLEM_CONTRACT_MISMATCH`. Run `golem check` in CI.
6. **Pass `--schema` to every command and run from the module root.**
   `migrations/` and `.golem/` are written relative to the working directory.
   Run `go mod tidy` after the first `generate` (the generated `golemgqlgen/`
   imports gqlgen).
7. **Use `internal`, not `hidden`, for server-only fields and relations** such
   as API-key hashes. `hidden` removes the field from the generated Go API as
   well; `internal` keeps it for Go and system code and removes it from GraphQL,
   introspection, event payloads and the manifest. On a relation it is
   field-local: mark both relation fields (and the foreign-key columns) to keep
   a link out of every client.
8. **A required field that only a `BeforeCreate` hook sets must be declared
   `golem.GraphQLHookOwned(...)`**, or the create is refused as
   `BAD_USER_INPUT` before the hook runs.
9. **Use `golem.HookEnqueue` or `tx.Enqueue`, never `AfterCommit`, for a job
   that must exist.** An after-commit hook runs after the commit is durable; a
   crash between the two loses the job.
10. **`System()` bypasses every policy and runs no hooks.** A hook is never the
    owner of an invariant that must hold for system writes, and a
    `HookEnqueue` in a hook does not fire for rows `System()` writes. Never put
    `System()` behind a request.
11. **One call chain per transaction.** Never use one `tx` from several
    goroutines; an overlapping call fails at once with `P4_RUNTIME_TRANSACTION`.
    Inside a hook, call with the hook's own context or its executor.
12. **Never cancel a job by an ID taken from arguments.** Read the job ID in
    the same transaction from a row the caller's policy allowed, then call
    `tx.CancelJob`. The same authorization rule applies to the go/v0.9.0
    `tx.PromoteJob`; also check that the business action permits promotion.
13. **Guard status writes against a concurrent cancel or failure.** A handler's
    success write is a conditional `UpdateMany` that matches the status it
    expects (and the job ID for a failure callback); zero rows means someone
    else won. Reading the status first is not enough on PostgreSQL.
14. **Upgrade every worker before relying on `OnJobFailed`.** A go/v0.8.2
    worker fails exhausted leases without calling it. Every process that
    registers the job type must register its callback, and the callback may run
    more than once.
15. **Any `AfterCommit*` hook makes `Config.AfterCommitError` mandatory**;
    `Open` fails with `P4_RUNTIME_CONFIG` without it.
16. **A model with `golem.Subscriptions` makes `EventTransport` and
    `ReportEventOperator` mandatory** (`GOLEM_EVENT_CONFIG` at `Open`), and
    events flow only while
    `RunEventPublisher` runs. A stream whose rule or filter reaches another
    model needs `Subscriptions` on that model too, or it is refused with
    `GOLEM_SUBSCRIPTION_INVALID` / `DEPENDENCY_UNOBSERVABLE`.
17. **Every live socket needs a deadline.** Set the credential expiry as the
    principal context's deadline, or mark an expiry-free principal with
    `golem.WithoutCredentialExpiry` and set `EventLimits.RevalidateEvery`.
    Otherwise subscriptions are refused with `GOLEM_PRINCIPAL_DEADLINE_REQUIRED`.
18. **Streams end, and rows leave.** Handle `GOLEM_SUBSCRIPTION_RESYNC` and
    `GOLEM_SUBSCRIPTION_OVERFLOW` by refetching and resubscribing, and handle
    `golem.EventExited` like a delete for the list you hold.
19. **Drain the event outbox before an upgrade that changes event formats.**
    0.6→0.7, 0.7→0.8.0 and 0.8.0→0.8.1 each need it ([upgrading](#upgrading)).
20. **You can link only to rows you can read.** An unreadable target behaves
    exactly like a missing one.
21. **An upsert's or `ConnectOrCreate`'s create input must set every field of
    its unique target**, whichever branch you expect to run.
22. **A write touching more than `MutationLimits.MaxTouchedRows` rows (1,000,
    also the ceiling) is refused whole** with `LIMIT_EXCEEDED`.
23. **Every list a custom operation or computed field returns declares its
    maximum** with `golem.MaxItems` or `golem.GraphQLList(type, max)`, or
    `generate` fails with `P5_GRAPHQL_LIST_MAX_REQUIRED`.
24. **`golem.ActorFrom` works in hooks and in custom operations.** Since
    go/v0.8.6 a custom query receives the caller's actor over GraphQL or from
    Go through the generated `Query`, and a custom mutation over GraphQL or
    through `Mutate`. Before go/v0.8.6 a custom query got the zero actor and
    could not be called from Go with one, so a server older than that must let
    the caller's policy scope the query instead of filtering on the actor.
    Calling a resolver function directly skips the actor.
25. **Never hand-edit `zz_golem_*`, and never change a `golem:"id=..."`
    identity.**
26. **On SQLite, writes to a full-text-indexed table need a connection opened
    by golem's SQLite provider.**
27. **Queue handlers must honour `ctx.Done()` and be idempotent.** Delivery is
    at least once.
28. **Golem owns `golem_queue`.** An index you add to it stops startup.
29. **Branch on codes and reasons, never on message text.**

---

<!-- /chapter:working-safely -->

<!-- chapter:application -->
<a id="application"></a>

## Go application guide

[The quickstart](#quickstart) gets one model running. This page covers what
you need for a real application: relations, authorization, queries, and
mutations. Its schema, policies and program are executed by
`TestGuideApplicationRuns`, so the code here is code that ran.

<a id="application-declaring-models"></a>

### Declaring models

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

<a id="application-the-tag-vocabulary"></a>

#### The tag vocabulary

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

<a id="application-enums-and-checks"></a>

#### Enums and checks

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

<a id="application-authorization"></a>

### Authorization

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

<a id="application-operations-that-own-a-write"></a>

#### Operations that own a write

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
declared with `golem.Mutation` (see [Custom operations](#application-custom-operations)).
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
operation (see [Counts and aggregates an operation owns](#application-counts-and-aggregates-an-operation-owns)).
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
[Transactions serve one call chain at a time](#application-transactions-serve-one-call-chain-at-a-time)):
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
use (see [Policies are cached per actor](#application-policies-are-cached-per-actor)), so
the caller is created and every other model works; each call that needs the
broken model's policy is refused with `UNAUTHENTICATED: principal policy could
not be established`, and the cause names the function. Prefer `Within` to
`SystemEscape` for a write an operation owns: `SystemEscape` skips every rule,
predicate and hook, while `Within` grants one verb on the rows you name.

<a id="application-operations-on-a-transaction"></a>

#### Operations on a transaction

A custom mutation receives a caller and opens its own transaction. Go code that
already holds a caller transaction, and needs a write only an operation may
make, declares a transaction operation instead. It is not a GraphQL root and
works in [kernel mode](#application-using-golem-from-go-without-graphql-kernel-mode):

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
- `golem.ActorFrom` receives the actor snapshot of the transaction's caller.
  Passing a context carrying another actor does not replace that snapshot.
  Cancellation and deadlines still follow the context you pass to `Run`.
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

<a id="application-counts-and-aggregates-an-operation-owns"></a>

#### Counts and aggregates an operation owns

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

<a id="application-unique-keys-cascades-and-concurrent-writes"></a>

#### Unique keys, cascades and concurrent writes

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

<a id="application-how-policies-are-proved"></a>

### How policies are proved

<a id="application-relation-checks-see-only-rows-the-caller-can-read"></a>

#### Relation checks see only rows the caller can read

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

<a id="application-named-policy-facts-and-their-release-boundary"></a>

#### Named policy facts and their release boundary

go/v0.9.0 includes the declaration/compiler contract and V3/V4 history readers,
but its automatic writer remains V3. Named-fact policies require both a
registered capture inventory and the actual V4 writer; the ordinary release
refuses their runtime admission. This includes definitions that are unused or
have a scalar-only body. Do not enable them by changing application settings or
by fabricating capture metadata. Writer activation is a later release step
after deploying and verifying compatible readers.

The declaration API is distinct from ordinary readable relations:

| Declaration | Meaning |
| --- | --- |
| `rules.FactScope(predicate)` | Declares the policy's one actor domain, including negative facts, field rules and operation grants. Scalar predicates and positive `Is`/`Some` witnesses define the supported scope vocabulary. It grants no permission to read dependency rows. |
| `rules.Fact(name, predicate)` | Defines a named physical predicate and returns a policy-only reference owned by that rules builder. Its relationship hops do not require target readability. |
| `golem.ScopeTemplate` / `golem.FactTemplate` | Register static scope/capture descriptions in a model's subscription declaration. Generated model scope/fact helpers bind their scalar or set arguments to that registered description. |
| `rules.BindFactScope(scope)` / `rules.BindFact(fact)` | Bind the generated template instances to the policy. A bound scope/fact is not an ordinary caller predicate. |

Facts must have distinct names and an acyclic dependency graph. A reference
cannot be used in caller `where`, ordering, selection or count expressions, or
borrowed from another rules builder. Visibility of the source can disclose the
Boolean result of a hidden dependency, so that disclosure must be intentional.
Ordinary relation predicates retain the target-readable semantics above.

Historical observation must retain complete committed before/after evidence
and recheck current actor scope. Missing, partial, stale or unsupported evidence
fails closed; a current relationship cannot replace a historical audience.
See the [compiled fact example](../go/internal/compiler/compile/testdata/policyfacts/schema.go),
the [scope/template example](../go/internal/compiler/compile/testdata/policyscopes/schema.go)
and the [reader-first release requirements](#upgrading-gov090).

<a id="application-field-masks"></a>

#### Field masks

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

<a id="application-write-rules"></a>

#### Write rules

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

<a id="application-when-a-policy-cannot-be-applied"></a>

#### When a policy cannot be applied

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
  `LIMIT_EXCEEDED` instead (see [Limits and error codes](#application-limits-and-error-codes)).

Break a cycle by testing a foreign key directly, such as
`Posts.AuthorID.Eq(actor.UserID)`, rather than a relation whose target rule
leads back.

<a id="application-callers-and-the-system-client"></a>

### Callers and the system client

```go
caller, err := application.ForPrincipal(ctx, Principal{UserID: id, Authenticated: true})
system := application.System()
```

`ForPrincipal` resolves your principal into an `Actor` and applies policy to
everything it does. `System()` bypasses policy entirely — use it for seeding,
migrations and background work, never for a request.

<a id="application-policies-are-cached-per-actor"></a>

#### Policies are cached per actor

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
  Your key must distinguish every policy input: account, active profile,
  tenant, roles and grants, or revisions that change whenever those values
  change. A principal ID alone is insufficient when two profiles or grant
  revisions can authorize different rows. `SnapshotActor` must copy mutable
  grant slices and maps; retaining the resolver's storage lets later changes
  alter an existing caller. Existing callers retain their actor snapshot;
  resolve a new caller to apply changed grants.
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

<a id="application-reading"></a>

### Reading

```go
all, err := caller.Notes.FindMany(ctx, notes.Notes.Where(golem.All[notes.Note]()))
drafts, err := caller.Notes.FindMany(ctx, notes.Notes.Where(notes.Notes.Published.Eq(false)))
one, err := caller.Notes.FindUnique(ctx, notes.Notes.ByID.Value(id))
```

Predicates compose with `.Or(...)`, `.And(...)`, and `golem.All[T]()` matches
everything the policy already permits. `Where` requires a predicate — there is
no argument-free form, because "no filter" and "everything I may see" are
different statements and golem makes you write the second one.

<a id="application-large-scalar-sets-and-relation-existence"></a>

#### Large scalar sets and relation existence

The existing typed `In` and `NotIn` operators compact supported scalar values
into one bounded provider parameter. SQLite uses `json_each`; PostgreSQL uses
typed set conversion. The original physical scalar codecs and indexed-column
comparisons are preserved, including full-width integers, decimals, UUIDs,
dates/times, enum wire values and text comparison modes. Empty, duplicate,
nullable and negated sets retain their ordinary predicate semantics.

Binary values keep individual binds because JSON base64 is not a native binary
comparison value. Unsupported compact encodings retain the existing binding
path and its capacity checks. Compaction does not raise SQLite's statement
parameter ceiling or SQL-text budget. One encoded compact set is limited to
1 MiB (`scalar_set_bytes`); large actor grants still need explicit bounded
inputs and the correct actor snapshot/cache identity.

In generated GraphQL filters, `attachments: {some: {}}` means that a related
attachment exists. The related row still has to be readable by the caller;
no invented scalar condition is needed, so rows with null scalar values are
not silently excluded. Empty `none`/`every` use their corresponding quantified
semantics. This exception does not allow an empty root `where`, logical list,
or arbitrary empty scalar condition.

<a id="application-writing"></a>

### Writing

```go
author, err := system.Authors.Create(ctx,
	notes.Authors.Create(notes.Authors.Handle.Create("ada")),
	notes.Authors.Select(notes.Authors.ID),
)
```

Each field is set through its own builder, so a field that is `readonly` or
absent from the schema cannot be written by construction.

<a id="application-transactions-serve-one-call-chain-at-a-time"></a>

#### Transactions serve one call chain at a time

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

<a id="application-lock-keys-serialize-check-then-write"></a>

#### Lock keys serialize check-then-write

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

<a id="application-serializable-transactions-and-retry"></a>

#### Serializable transactions and retry

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

<a id="application-mutations-return-only-what-you-select"></a>

#### Mutations return only what you select

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

<a id="application-nested-writes-on-one-relation-run-in-a-fixed-order"></a>

#### Nested writes on one relation run in a fixed order

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

<a id="application-upserts-and-connect-or-create"></a>

#### Upserts and connect-or-create

An upsert's create input must set every field of its unique target to the
target's value, whichever branch you expect to run. One that does not is
refused with `BAD_USER_INPUT` (`upsert create input does not set the target
selector`) before either branch runs; a nested upsert is checked once its
parent row is written. A nested `ConnectOrCreate` follows the same rule. A
foreign-key field of the target is satisfied by a `Connect` on its relation to
the row holding that value, or by the value the nested parent gives it. A
`Before` create hook that rewrites the create input away from the target is
refused before the row is written.

The create branch also needs a create grant; see [Write rules](#application-write-rules).

`Increment` and `Decrement` on a field that is part of a relation's foreign
key are refused with `BAD_USER_INPUT: foreign key <Field> cannot be changed by
arithmetic`, for every caller. Set the new value instead.

<a id="application-datetimes"></a>

#### Datetimes

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

<a id="application-hooks"></a>

### Hooks

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
  runs (see [Operations that own a write](#application-operations-that-own-a-write) for
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

<a id="application-what-it-does"></a>

### What it does

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

<a id="application-sqlite-copies-and-backups"></a>

#### SQLite copies and backups

For the existing offline file-copy procedure, stop every reader and writer,
close the verified provider handle, call `sqlite.CheckpointForBackup(ctx, db)`,
then copy the database. Do not copy a live database file or its journal
separately.

**go/v0.9.0 — Snapshot Work A (copy only).** The API is
`sqlite.Snapshot(ctx, db, expectedBundle, destination) error`, where `db` is an
open verified `*provider.Database` from local `sqlite.Open` and
`expectedBundle` is the expected generated `golem.SchemaBundle` with its exact
reviewed migration history. Both network-filesystem profiles are refused.
This is not a NAS support or online latency guarantee.

Snapshot borrows one source connection for the incremental native copy and
releases it before verification. Other work may wait for pool capacity or
SQLite locks; copy and verification cost depend on database size. SQLite
normalizes only the owned private clone to DELETE journal mode, then checks
integrity, foreign keys, the physical schema and the exact reviewed ledger.
Verification uses an RW handle with `query_only` enabled: physical read-only
opening skips CHECK validation in the supported engine, whereas this handle
checks constraints and refuses ordinary SQL writes. The source profile, rows,
versions, pending facts and delivery records are not rewritten.

The destination must be new in an existing trusted local directory. Its main
name and `-wal`, `-shm` and `-journal` names must all be unused, including empty
files, symlinks and directories. Snapshot checks before copying and again
before no-overwrite publication; these checks are not atomic with sidecar
creation. The caller must exclude concurrent writers to that namespace.
Existing destination objects are never removed. Pre-publication cancellation
cleans private staging. Use `provider.CodeOf(err)` to distinguish
`provider.CodeMaintenance` from `provider.CodeSnapshotPublished`: the latter
means the destination exists but final synchronization or cleanup failed, so
do not retry as though nothing was published.

Snapshot does not restore or replace a database, rotate client identity, or
reconcile copied jobs and events. It starts no workers or publishers. Opening
the copy for application use remains an explicit operation; safely restoring
it or running copied background work requires a separate operational contract.
For a schema using the go/v0.9.0 durable data identity below, verification
also requires the copied singleton to be valid and preserves its exact value.
A copy therefore shares its source identity. Snapshot never manufactures a
missing identity and does not establish a fresh identity for a restored copy.

<a id="application-offline-sqlite-restore-unreleased"></a>

<a id="application-offline-sqlite-restore-gov090"></a>

#### Offline SQLite restore (go/v0.9.0)

`sqlite.PrepareRestore` creates a private local copy for reviewed migrations
and explicit application reconciliation. Stop the old fleet first and prevent
restart: traffic, workers, publishers, CDC adapters, other readers and writers,
and processes that may still finish external work must all be stopped. A
worker's shutdown grace returning, or one database handle closing, does not
establish that condition. The deployment owns process shutdown, archive
extraction and application assets. Restore owns the SQLite staging and
publication sequence. This API has no NAS support guarantee.

Pass immutable standalone archive and destination file paths, an explicit
`ArchiveBundle`, an explicit `TargetBundle`, and one of these modes:

- `sqlite.RestoreResumeAtLeastOnce` readies work leased by the old fleet while
  preserving attempts, cancellation/finalization intent and pending backoff.
  External effects that completed before the archive recorded completion may
  run again. Existing blocked or retired events remain operator decisions.
- `sqlite.RestoreAbandonAndResync` cancels archived unfinished jobs and retires
  archived undelivered events, including legacy groups without delivery rows.
  Jobs and immutable facts remain stored. Reconcile domain status and external
  effects explicitly; clients start from fresh reads after identity discovery.

Preparation verifies the archive's catalog, exact reviewed ledger, integrity,
foreign keys, supported fact codecs and complete causation groups before
exposing the copy. Work reconciliation runs once during preparation, so new
jobs and facts deliberately created by application reconciliation survive.
Ordinary row versions and counters are never reset. Unsupported queue layouts
require an explicit queue storage upgrade on the extracted offline copy first.
Preparation and publication also validate stored queue envelopes, including
payload bounds and lease/resource consistency. Application reconciliation owns
decoding job payloads with the registered job codec and reconciling their
domain meaning; storage validation alone does not establish either.

Use `restore.StagePath()` for existing reviewed migration commands. Use
`restore.Database()` with the generated application for domain reconciliation
and required derived-state rebuilds. Close the initial staging handle before
opening it separately for migration, and close every staging handle and
operation before publication. Archive and target bundles may differ; the
archive bundle is registered automatically for historical fact decoding.
`HistoricalEventBundles` supplies any other retained producer generations and
must not duplicate the archive or target bundle.

`restore.Publish(ctx)` verifies the target schema and ledger, queue storage and
retained evidence, and checks that every pending group can be delivered under
the target event schema. It then rotates the existing valid durable identity
with provider-owned randomness, closes and synchronizes the private image,
and publishes it atomically in the owned local destination namespace. The
target must have reviewed durable identity storage; publication never repairs
a missing or malformed singleton. An existing destination must be closed and
checkpointed through SQLite, without sidecars. Separate namespace checks do
not replace the caller's exclusion of concurrent destination writers.

Before publication, failure preserves the destination and `restore.Close()`
discards private staging. After publication, synchronization or cleanup failure
returns `provider.CodeRestorePublished` and the new identity. Keep serving
stopped and inspect the destination; do not assume that result rolled back.
On successful publication, open a fresh provider handle and generated app and
pass normal startup verification before enabling workers, publishers or traffic.
Do not reuse an app that retained the archive's old identity. Existing client
discovery resets cached rows, merge state and continuation chains.

A killed restore process can leave a private staging directory. Start a new
restore session rather than admitting or publishing that orphan automatically.
Local process-interruption tests cover both new and replacement destinations,
immediately before publication and after publication but before directory sync.
They establish visible atomic publication, not power-loss durability. SQLite
page-capacity exhaustion and cancellation after actual incremental backup
progress are also covered locally. The cancellation tests exercise public
snapshot and restore preparation, require staging cleanup and unchanged source
evidence, and successfully retry; snapshot also reuses its sole source
connection. Physical filesystem ENOSPC and NAS failure behavior still require
their own rehearsal.

<a id="application-graphql"></a>

### GraphQL

<a id="application-introspection"></a>

#### Introspection

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

<a id="application-custom-operations"></a>

#### Custom operations

A schema package may declare its own query and mutation roots:

For an action that outlives the request (an export, an import), see
[Recipe: durable actions](#queue-recipe-durable-actions).

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
[Operations that own a write](#application-operations-that-own-a-write).

Inside `caller.Transaction`, `tx.Enqueue` adds a durable job and `tx.CancelJob(ctx, jobID)`
cancels one, both committing or rolling back with the transaction. Cancel only a
job ID you read, in the same transaction, from a row the caller's policy allowed,
never an ID taken from arguments. See [the queue](#queue-inspecting-and-operating).

**go/v0.9.0 P10 — bounded maintenance application reference.** For a removal
that exceeds one transaction's limits, see [bounded removal](#queue-application-reference-bounded-removal-gov090-p10).
The generated reference commits logical removal with a task and first job,
then uses 32-leaf pages, transactional progress/successors and a shared root
compare-and-write fence for every caller/System ingest or reparent. Its
`IntoTx` functions belong to the application, not the Golem API. The existing
1,000-row/1 MiB limits remain in force; progress visibility and retained task
identity are explicit application decisions. Local acceptance verifies ordinary
cleanup on both providers and semantic rollback/convergence on SQLite and
PostgreSQL with pgvector. NAS latency remains unmeasured.

**go/v0.9.0 P11 — queue priority and promotion.** go/v0.9.0
adds `queue.WithPriority(queue.PriorityInteractive)`; the default remains
`queue.PriorityNormal`. Generated caller/system transactions expose
`tx.PromoteJob(ctx, jobID, queue.PriorityInteractive)`, with
`queue.ReadyNow()` as an explicit option to remove remaining delay. Promotion
is monotonic and changes pending jobs only. Authorize the business action and
read its stored job ID through the caller in the same transaction;
`PromoteJob` does not authorize ownership of an arbitrary job ID. Dedupe never
implicitly promotes existing work, and retries preserve its lane.

The scheduling bound belongs to one continuously running worker: three
Interactive admissions then a Normal opportunity when capacity permits.
`queue.Limits.ReservedInteractive` reserves local slots only (default `0`,
strictly below `Concurrency`); it never preempts handlers or bypasses shared
resource caps. These APIs and new public struct fields require the matching
v0.9.0 library/CLI and regeneration; prefer keyed literals. Existing
queue storage requires an explicit offline upgrade after stopping all
producers and workers. See [priority and promotion](#queue-priority-and-promotion-gov090-p11)
and [storage inspection and upgrade](#queue-explicit-storage-upgrade-gov090-p11)
for exact commands, supported shapes and the limits of old-worker refusal.

<a id="application-what-a-custom-operation-returns"></a>

##### What a custom operation returns

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

<a id="application-named-output-objects-and-embedded-model-rows"></a>

##### Named output objects and embedded model rows

Register each published named struct in `DefineGraphQL` with
`golem.Output[T](schema, "GraphQLName", options...)`. Custom queries/mutations
can then return that struct, a pointer, or bounded lists of it. Register nested
output structs explicitly too. Scalar/enum fields, nullable pointer members,
nested objects/lists and embedded `golem.Row[M]` members share the same generated
contract. `golem:"graphql=name"` selects a field's wire name. Recursive DTO
definitions, maps, interfaces and unsupported embedded/generic struct shapes
are refused during generation.

Every DTO list field declares its bound with
`golem.ObjectFieldMaxItems("GoFieldName", outerMaximum, nestedMaximum...)`;
the option uses the Go field name and requires one positive maximum per list
level. Root list results still use `golem.MaxItems`. A resolver argument named
`take` does not enforce a DTO list's output bound. Total response and model
hydration budgets also apply across the operation before descendant work.

| Member or option | Authorization and result behavior |
| --- | --- |
| Ordinary scalar/enum/DTO member | Resolver-owned output. Golem does not infer its sensitivity from a neighboring model row. DTO fields named `id` or `_version` remain ordinary DTO values, not model identity/version metadata. |
| `golem.Row[M]` member | Reloaded through the caller with its exact selected projection and policy scope. Rows are batched/deduplicated for hydration and then restored to resolver order; supplied model cells are never trusted for serialization. |
| Nullable `*golem.Row[M]` member | Missing and unreadable rows both become null. A required unreadable row produces an error and normal GraphQL non-null propagation. |
| `golem.ObjectRowGate("GoRowField")` | Omits the containing item when that row is missing or unreadable, even when the client does not select the gate field. Use this when sibling DTO scalars must disappear with the row. |

Computed and batched computed fields reuse the registered DTO through
`golem.GraphQLOutput[T]()`, optionally `.NonNull()` and/or a bounded
`golem.GraphQLList`. A non-null computed DTO cannot return nil. Existing custom
mutation commit reporting still applies: a post-commit hydration/encoding
failure must not look like an uncommitted mutation.

The [compiled DTO/input schema](../go/internal/compiler/compile/testdata/objecttypes/schema.go)
and [embedded-row/gate schema](../go/internal/compiler/compile/testdata/objectrows/schema.go)
are exercised by the generated HTTP acceptance fixtures.

<a id="application-named-inputs-omission-and-exactly-one-choices"></a>

##### Named inputs, omission and exactly-one choices

Register nested input structs with `golem.Input[T](schema, "GraphQLName")`.
Use `golem.InputValue[T]` when a resolver must distinguish omission from an
explicit null. Its zero value is omitted; `T` is non-pointer because the
wrapper owns nullability. This works in root resolver arguments, nested input
objects and computed-field arguments.

| Input state | `IsSet()` | `IsNull()` | `Get()` has a value |
| --- | --- | --- | --- |
| Omitted | false | false | false |
| Explicit null | true | true | false |
| Present non-null value, including zero/false | true | false | true |

Direct Go callers construct these states with the zero value,
`golem.NullInputValue[T]()` or `golem.InputValueOf(value)`. Do not collapse
omission and explicit null while adapting arguments or choosing defaults.

Add `golem.InputOneOf()` to a registered input to require exactly one supplied,
non-null member. All members must be nullable; zero choices, multiple choices
and a null choice are rejected before the resolver runs. This is separate from
ordinary nullable input semantics. The compiled DTO/input schema linked above
includes nested patches and an exactly-one playback target.

<a id="application-how-many-items-a-list-may-hold"></a>

##### How many items a list may hold

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
not a list (`P5_GRAPHQL_LIST_MAX_SCALAR`). Custom options must be direct supported
declarations: `golem.MaxItems`, or the query-only invalidation declarations
described below. Unsupported options fail with `P5_CUSTOM_OPTION`; declaring
`MaxItems` more than once fails with `P5_CUSTOM_OPTION_DUPLICATE`. `[]byte` is a `Bytes` scalar, not a
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

<a id="application-durable-data-identity-unreleased"></a>

<a id="application-durable-data-identity-gov090"></a>

#### Durable data identity (go/v0.9.0)

This API requires matching CLI and library builds and regenerated
application code and client manifests. Opt in explicitly in `DefineSchema`:

```go
golem.WithDataIncarnation(schema)
```

The opted schema uses Physical 4 and a provider-owned durable identity. Create
and apply a reviewed initial migration, or a reviewed additive migration from
Physical 3, through Golem's migration runner. The runner derives initialization
from the sealed migration operation, generates the identity with Go's
`crypto/rand` and writes it with a bound INSERT in the migration transaction.
Executing the rendered SQL alone cannot initialize it. Startup checks the exact
reviewed ledger, physical catalog and singleton; it does not repair missing or
malformed identity data. Unopted schemas retain Physical 3, and their existing
history and generated request bytes remain unchanged.

The generated `application.DataIncarnation()` getter exposes a read-only opaque
`golem.DataIncarnation`. Its canonical wire form is 32 lowercase hexadecimal
characters with a nonzero value; applications supply no constructor or config
value. Reopening the same database preserves the identity. Independently
initializing a new database generates a different identity. Continuation tokens
bind the identity admitted by the application at startup.

Opted GraphQL contracts require manifest 3 and `data-incarnation-v1`, alongside
`operation-invalidation-v1` when declared. Every business HTTP request,
WebSocket initialization and subscribe payload must carry the exact admitted
identity. The current protocol discovers it through the same HTTP JSON endpoint
using the static protocol/contract/manifest/capability handshake and a nonce.
Discovery accepts no query, variables, operation name or identity field, even
when null or empty. It authenticates the principal through the generated
authentication path without beginning a caller or building business policies.
The response echoes the nonce and static metadata with `data: null` and the
identity; discovery and its refusals use `Cache-Control: no-store`. Discovery
over SSE is refused. Ordinary responses and acknowledgements carry identity
only for comparison; clients never adopt identity from them.

Call `await client.ready()` on a `GolemClient` or `GolemTransport` before
starting mutations. Reads initialize automatically. When discovery finds a
different identity, the client clears cached queries and owned React mutation
results before admitting new work. An already sent mutation that loses its
identity boundary has an unknown outcome and is never replayed automatically;
explicit server commit errors retain their commit evidence. Per-call mutation
callbacks detach from the new UI after a reset, while the returned promise and
hook-global commit reconciliation retain responsibility for the sent work.

The [offline restore workflow](#application-offline-sqlite-restore-gov090) establishes a
fresh identity in a verified private copy after caller-owned fleet shutdown.
This prerequisite does not fence old running processes or implement data-only
corrections. It provides no NAS support guarantee.

<a id="application-custom-continuation-pages"></a>

#### Custom continuation pages

`golem.PageQuery` declares a custom read whose resolver accepts
`golem.PageRequest[Args]` and returns `(golem.PageBatch[Item], error)`.
Declare both `golem.PageLimits(defaultTake, maxTake, maxCandidates)` and
`golem.PageRevision(revision)`. The generated GraphQL result contains `items`
and nullable `next`; the root adds `take` and `after` arguments. The page item
can be a registered output object or a model row. Page declarations are
separate from ordinary custom queries and cannot carry `Within` grants.
Page declarations require `golem.WithDataIncarnation(schema)` and the matching
go/v0.9.0 migration, regeneration and client setup described above.

The request exposes admitted `Arguments()`, `Take()`, `CandidateLimit()`,
cumulative `Consumed()`, private `State()` and the chain's absolute `Expires()`.
Return `golem.PageItems(items, consumed, nextState)`: `consumed` is the number
of traversal candidates examined on this request, including denied or deleted
rows. It must cover the returned items and stay within the candidate budget.
A nil checkpoint ends traversal. A nonnil checkpoint must be nonempty,
different from the previous checkpoint and accompanied by positive
consumption. An empty or short item list can still have a continuation; only
`next: null` ends the chain. No total count of denied candidates is exposed.

The resolver owns a stable traversal contract. Use an immutable keyset with a
defined population boundary, or retain an immutable ranked snapshot through
the chain deadline. Include a deterministic tie breaker and validate the
checkpoint against that ordering. A random seed alone does not freeze a
changing ranked population. Increasing a counter or changing opaque bytes
does not prove that a resolver advanced its application ordering.

A schema with pages requires the generated application's `Config.Pages`, a
`runtime.PageConfig[Actor]`. The new field extends `runtime.Config`; use keyed
configuration literals when upgrading. Supply a deployment-owned AES-256 keyring, the
active key ID, an explicit whole-second `MaxAge` from one second through
24 hours, and a `Scope` callback. The callback receives
the admitted actor and must identify its durable account, tenant, selected
profile and authorization revision. A process-local policy-cache key is not
a durable scope. Every serving instance shares the keyring. Retain retiring
decrypt keys through the maximum token lifetime and rotate before 2^32 token
encryptions across instances sharing a key. There is no startup-generated key
or default expiry. `PageConfig.Incarnation` has been removed: the runtime uses
the provider identity admitted at startup. Stop old instances before using the
[offline restore workflow](#application-offline-sqlite-restore-gov090), then open fresh
instances against the newly published identity.

Tokens bind the contract, operation, page revision, data incarnation, actor
scope and canonical arguments, including effective `take`. `after` is excluded
from the argument binding. A request authenticates these before calling the
resolver, then reauthorizes returned rows and selected fields. Tokens are
retryable and do not confer data access. The chain deadline is fixed on its
first request. Hydration or output encoding errors issue no continuation.
The protocol ceilings are 1,000 returned items, 10,000 consumed candidates per
request, a 2 KiB private checkpoint and an 8 KiB token; declarations can be
tighter. The runtime read limit also bounds admitted `take`.

On the client, `staticContinuationQueryOptions` and React's
`useGolemStaticContinuationQuery` use opaque `string | null` page parameters.
Select one page root directly and leave `after` to the client. These static
APIs preserve authored model field selections and require no event stream.
Discard the whole accumulated chain after expiry, an authentication/profile
change or an application invalidation. `staticInfiniteQueryOptions` and
`useGolemStaticInfiniteQuery` retain the separate generated-list cursor API.

<a id="application-declared-operation-invalidation"></a>

#### Declared operation invalidation

Add `golem.InvalidateRows[M](name, binder, field, ...)` or
`golem.InvalidateFacts[M](name, scopeTemplate, binder, factName, ...)` directly
to a `golem.Query` or `golem.PageQuery` declaration. Mutations cannot declare
these dependencies. This supports scalar results, output objects and model
rows; the observed source need not occur in the response. It does not authorize
arbitrary scalar values computed by the resolver: those remain application
output whose sensitivity the resolver must enforce.

Each binder is a named, non-generic free function in the schema package. Its
exact signature is `func(context.Context, Actor, Args) (golem.Predicate[M], error)`
for readable rows, or `func(context.Context, Actor, Args) (golem.BoundFactScope[M], error)`
for facts. `Actor` is the schema actor by value and `Args` is the resolver's
ordinary argument type. A page binder receives `Args`, not `PageRequest[Args]`;
its subscription excludes the compiler-owned `take` and `after`. An ordinary
query's application arguments named `take` or `after` retain their meaning.
Binders run with a freshly admitted actor at opening and revalidation, including
event evaluation. Their predicates narrow caller authority and grant no access.

Readable dependencies watch stored scalar fields, with row authorization,
watched-field masks and before-read hook filters applied. The source must enable
`golem.Subscriptions`. Public primary-key access is not required. Declared
before-image coverage must include every watched field and every local scalar
used by the row policy, binder/hook filter or watched-field mask. Use
`golem.DeleteSnapshot[M](fields...)` inside `golem.Subscriptions[M](...)` when
the default coverage is insufficient; it omits unbounded text fields. Include
all required fields in an explicit declaration; primary-key fields are added
automatically. Missing coverage refuses subscription admission with
`P7_INVALIDATION_COVERAGE`; it must not silently miss a row leaving the filter.
Capture is declared independently of which subscribers happen to be connected.
Readable operation invalidation currently refuses admission when CDC adapters
are configured: they cannot certify complete before images. A retained update
or delete with missing or partial evidence ends the stream with
`GOLEM_SUBSCRIPTION_SOURCE_CLOSED`, without attributing dependency activity.
The client clears stale results and recovers with its normal stream backoff.

Ordinary relations in a readable source's row policy, binder filter, or watched-field
mask currently fail admission with `P7_INVALIDATION_COVERAGE`. The runtime does
not infer an old source audience from a currently readable related row: deletes
and reparenting can change that audience. Declare a separate related source
whose binder and ordinary authority are local, or use genuine registered fact
authority with certified historical capture. This restriction does not change
ordinary query execution; general historical routing for ordinary relation
invalidation remains unsupported.

Fact dependencies use registered fact names on the declared source and scope
template. The returned bound scope must match the active policy's template,
template fingerprint, arguments and predicate exactly. The policy must bind
each requested fact. That authority can observe a hidden fact owner without
granting `CanRead` on its row. It uses certified historical capture, including
owner activity and scope loss. The default writer remains V3; fact invalidation
requires the certified V4 historical writer and is refused when that contract
is unavailable. An invalidation declaration does not activate V4. Named facts
used by readable row rules or field masks keep this same historical authority;
they are not converted into ordinary related-row streams.

An operation declares at most 32 dependencies, each with 1–64 distinct scalar
fields or fact names. Dependency names are unique within the operation,
nonempty constants with at most 256 bytes. Ordinary arguments use canonical
scalars, enums, lists and registered inputs; JSON and model-derived predicate,
unique and mutation inputs are rejected recursively. Canonical argument work
is limited to 128 KiB, 16,384 nodes and depth 32. Runtime planning also caps the
combined member streams at 64 and observation paths at 256; existing subscription
and read-planning limits still apply, so declaration limits do not guarantee
admission.

Generation adds a Boolean subscription named `<queryName>Changes` with ordinary
query arguments. It sends acknowledgement and terminal control signals, never
Boolean data, rows or IDs. Authorized activity ends it with
`GOLEM_SUBSCRIPTION_RESYNC` / `DEPENDENCY_CHANGED`. The client subscribes again,
waits for acknowledgement, then refetches. Live continuation pages discard the
entire accumulated chain and restart at `after: null`. Use `queryOptions` /
`useGolemQuery` for declared ordinary queries, and `continuationQueryOptions` /
`useGolemContinuationQuery` for declared pages. Static query and continuation
APIs open no subscriptions and remain available when observation is unsupported.
They still enforce ordinary query authorization and continuation checks.

Opted schemas emit manifest format 3 with the required capability
`operation-invalidation-v1`. The server checks it for **all HTTP requests and
WebSocket initialization against that schema**, including static queries and
mutations. Older clients are rejected globally, rather than substituting
returned-model streams. Upgrade the client and regenerate its manifest together
with the server. Schemas without durable data identity or invalidation retain
their previous capability requirements and request bytes. Page schemas require
durable data identity and therefore also require `data-incarnation-v1`.
Generated servers supply the exact
active `graphql.Config.Bundle`; manual wrappers must preserve that bundle so
the server can validate its fingerprint and capability requirements. The new
field extends `graphql.Config`; use keyed configuration literals when upgrading.

<a id="application-partial-commits-are-never-reported-as-clean-failures"></a>

#### Partial commits are never reported as clean failures

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

<a id="application-using-golem-from-go-without-graphql-kernel-mode"></a>

### Using golem from Go without GraphQL (kernel mode)

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
  [Custom operations](#application-custom-operations).
  [Transaction operations](#application-operations-on-a-transaction) work the same in both
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

<a id="application-subscriptions-and-events"></a>

### Subscriptions and events

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

<a id="application-what-an-event-carries"></a>

#### What an event carries

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

<a id="application-who-receives-a-created-or-updated-event"></a>

#### Who receives a created or updated event

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

<a id="application-rows-that-leave-a-list"></a>

#### Rows that leave a list

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

<a id="application-a-stream-that-reads-another-model"></a>

#### A stream that reads another model

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
  created or updated event](#application-who-receives-a-created-or-updated-event)).
- **If it cannot be watched.** A source that fails or a read that fails ends the
  stream with the code that failure carries (`RESYNC`, `SOURCE_CLOSED` or
  `REVALIDATION`) rather than leaving it open and unwatched.
- **`InvalidatePolicies`.** `application.InvalidatePolicies()` drops the policy
  cache and ends every open event stream with `GOLEM_SUBSCRIPTION_RESYNC` and no
  reason. A client reopens its streams and so picks up the new policies; this is
  also how access that broadened reaches a stream.

<a id="application-row-versions"></a>

#### Row versions

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

<a id="application-who-receives-a-delete"></a>

#### Who receives a delete

A deleted row cannot be read back, so golem judges a delete against the
row's **delete snapshot**, a set of its fields stored with the fact, and
evaluates your read rule, expanded as for a read (see
[How policies are proved](#application-how-policies-are-proved)), with three possible
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

<a id="application-the-delete-snapshot"></a>

#### The delete snapshot

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

<a id="application-when-to-use-a-change-log-model"></a>

#### When to use a change-log model

Events tell a subscriber that a row changed and let it read the current state.
They do not carry old values, a deleted row's contents, or every intermediate
state. If a consumer needs those, or needs deletes it can always authorize,
write a change-log model, such as `DocumentChange`, from a hook or an
operation in the same transaction, and subscribe to its created events. Its
rows never change, and they carry whatever the consumer needs, including a
deleted row's contents. Give it a read rule on its own fields, such as an
owner ID copied into each entry, so that who sees an entry never depends on a
parent row the same transaction removed.

<a id="application-streams-end-with-a-code"></a>

#### Streams end with a code

`Recv` returns an error when the stream ends. `events.CodeOf(err)` gives its
code:

| Code | Why | What the client does |
|---|---|---|
| `GOLEM_SUBSCRIPTION_CANCELLED` | you called `Close`, or the context passed to subscribe ended | nothing |
| `GOLEM_SUBSCRIPTION_RESYNC` | events may have been missed (the transport dropped, or a delete or an exit could not be judged), `InvalidatePolicies()` was called, or access to a model the stream reads changed (reason `DEPENDENCY_CHANGED`, see [A stream that reads another model](#application-a-stream-that-reads-another-model)) | reload the state you hold, then subscribe again |
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

<a id="application-re-checking-long-lived-streams"></a>

#### Re-checking long-lived streams

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

<a id="application-caps"></a>

#### Caps

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

<a id="application-graphql-subscriptions-over-websocket-and-sse"></a>

#### GraphQL subscriptions over WebSocket and SSE

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

<a id="application-the-live-handshake-and-the-subscription-ack"></a>

##### The live handshake and the subscription ack

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

<a id="application-the-client-manifest"></a>

##### The client manifest

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
  - `object`: a registered DTO result, whose `result` type tree and the manifest's
    named `types` describe its nested object/model occurrences.

  `nullable` is `true` when the root's schema type is nullable (`findOne` is, a
  mutation's row is not). A client sets a single-row slot to `null` only when it
  is `nullable`.

go/v0.9.0 also emits versioned transport-type metadata when required:

| Manifest format | Metadata and compatibility |
| --- | --- |
| 1, including omitted `formatVersion` | The legacy row manifest. |
| 2 | Registered output/input `types`, typed root `result` and model `computedResults`. A type definition has named `fields` and object/input-object kind; exactly-one inputs also set `oneOf`. Recursive result trees retain nullability and list `maxItems`. |
| 3 | Continuation `pagination` metadata. Required `operation-invalidation-v1` or `data-incarnation-v1` capabilities also use this format. The client must recognize and send every required capability. |

These manifest formats are independent of event fact codecs: format 3 does not
activate the V4 writer. Reject unknown formats/capabilities rather than ignoring
them. The generated manifest is the authority; do not manufacture or edit its
type, page or invalidation metadata.

Mutation roots can additionally carry `mutationScope`, which tells
the client which caches may need refreshing after its own write. The closed
choices are `{ kind: "models", models: ["Invite", "Team"] }`, with nonempty,
sorted public model names, and `{ kind: "unscoped" }`. Generated roots use a
conservative relation closure that includes nested writes, cascades and
set-null effects. A reachable hook, private model or unknown target makes the
scope unscoped. Custom mutations are always unscoped because their return type
does not bound their writes: an operation returning one `Invite` may also
create a `Team`. The root's existing `models` still describes its response and
read dependencies. An absent scope also means unscoped. This optional hint is
additive across manifest formats and requires no new server capability or live
connection; ordinary HTTP operations remain supported.

The file is absent in kernel mode (`WithoutGraphQL`), which has no GraphQL layer.

To use it from a front end, see [REACT.md](#react-server) and the
[client README](#client-how-live-queries-work).

<a id="application-the-socket-deadline"></a>

##### The socket deadline

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

<a id="application-event-transports"></a>

### Event transports

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

<a id="application-changing-a-schema"></a>

### Changing a schema

Editing a model means a new migration before regeneration:

```
golem migration new --schema ./notes --name add-author
golem generate --schema ./notes --app-out ./notes
golem migration apply --provider sqlite --dsn "file:notes.db"
```

`golem migration plan` shows what a migration will do before it runs, and
`golem check --schema ./notes --app-out ./notes` fails when generated code no longer matches
the schema — run it in CI.

<a id="application-approvals"></a>

#### Approvals

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

<a id="application-what-each-provider-changes-in-place"></a>

#### What each provider changes in place

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
- A single-provider SQLite or PostgreSQL schema can add one required field
  without a default per reviewed backfill migration. SQLite uses the restricted
  contract below; multi-provider companions remain unsupported.

**go/v0.9.0 — SQLite required-column backfills.** go/v0.9.0
extends the existing `migration new` → `migration backfill attach` →
`migration apply` workflow to a schema whose sole provider is SQLite. It
stages one new column as nullable, fills it from the reviewed SQL companion,
checks that no NULLs remain, and rebuilds the table with the required column.
These steps and the migration ledger commit together; failure rolls them all
back. Existing primary keys, row versions, the version counter, foreign keys,
indexes and managed full-text search survive the rebuild.

Stop application writers, background workers and live streams before applying
this offline migration. The SQLite companion must be one parameter-free
`UPDATE` that writes only the new column and returns no rows. It may read
reviewed application tables and call functions SQLite marks innocuous.
Transaction control, schema changes, PRAGMAs, attached databases, system
metadata access and writes to existing columns are refused. A target used by a
semantic index is refused because this workflow does not perform semantic
invalidation. It does not run application hooks or emit ordinary mutation
events. These execution restrictions describe the new SQLite path; they do
not change PostgreSQL's existing companion execution contract.

Only one required column and one provider are supported per reviewed
backfill. Multi-provider companion publication remains unsupported. Data-only
corrections use the separate workflow below; neither workflow is an online
backfill or a historical Go callback.

**go/v0.9.0 — PostgreSQL reviewed backfill execution.** Companions remain
one parameter-free, preparable statement. Golem executes that statement in
PostgreSQL's atomic procedural context, preserving the outer migration
transaction as the sole commit owner. Direct transaction commands and nested
`DO`/`CALL` transaction endings cannot commit staged changes. Utilities that
require execution outside a transaction, such as `VACUUM` and
`CREATE INDEX CONCURRENTLY`, are refused. Installed trusted PL/pgSQL and its
`USAGE` privilege are required; the executor does not create functions or
elevate privileges. Failure rolls back transactional schema/data changes,
the ledger update, and any change to the admitted durable data identity.
The companion remains operator-reviewed SQL: this boundary does not make
sequence changes or external effects from privileged functions rollbackable.
This backfill execution rule is separate from reviewed data corrections below
and the offline SQLite restore workflow described above.

**go/v0.9.0 — reviewed data-only corrections.** A schema whose sole declared
provider is SQLite or PostgreSQL can append a correction without changing its
model or physical schema. First enable reviewed durable data identity with
`golem.WithDataIncarnation`; the correction refuses older physical profiles.
Use `golem migration data new --name correct_title --field Note.Title --file
correction.sql --schema ./notes`. The first run reports the exact operation ID
requiring `--approve`; review the companion and rerun with that ID. The command
seals a separate immutable entry and checksummed SQL companion. Existing
`migration plan --migration <id>`, `generate`, and `migration apply` consume that
same reviewed history. Ordinary `migration new` still refuses unchanged schemas.

Stop the application fleet and prevent restart before applying: readers,
writers, live streams, queue workers, publishers and CDC adapters. The deployment
owns that exclusion; migration locks serialize migrations. Regenerate the app
for the new reviewed head, retain the previous event bundle when historical
facts require it, and open a fresh provider and app after applying. The schema
and contract can stay identical while the generation changes. Never resume an
app that retained the previous data identity.
Retained historical bundles permit decoding; they do not grant current
hidden-fact scope authority across generations. Such evidence fails closed
through the existing subscription recovery boundary.

Each correction updates one existing stored scalar field. Identity, unique and
foreign-key fields, version fields, and sources of generated keys are refused.
Semantic inputs, including generated dependencies, are refused until explicit
semantic invalidation is supported. Other fields' semantic state remains intact.
The companion is bounded at 1 MiB, parameter-free, and ends with one newline.
Its values must use the field's canonical provider representation; application
reconciliation owns domain invariants beyond the reviewed storage contract.
SQLite permits one restricted `UPDATE`, reviewed table reads and innocuous
functions. PostgreSQL requires a schema-qualified target and accepts a closed
`UPDATE` grammar: same-table columns,
literal values, scalar arithmetic/comparisons, Boolean expressions, `CASE`, and
an optional `WHERE`. It rejects functions, casts, subqueries, CTEs, comments,
multiple assignments and utilities; accepted expressions are rendered by Golem.
There is no fallback to executing arbitrary PostgreSQL correction SQL.
Unmanaged target triggers, rules and policies are refused even if allowlisted
for ordinary startup; only verified provider-owned full-text behavior may run.

Only rows whose stored value actually changes receive new row versions and
optimistic-concurrency versions. Unchanged and unmatched rows retain theirs.
The source must be ahead of retained row versions, and exhausted or invalid
version state refuses the correction. SQLite rebuilds managed full-text search
in the migration transaction; PostgreSQL retains its provider-owned generated
projection. Keys, foreign keys, queue records and immutable facts remain stored.
No application hooks or ordinary request events run.

Corrections, row-version writes, a fresh data identity and the terminal ledger
entry commit together. Failure rolls back their transactional changes;
PostgreSQL sequence draws can leave gaps after a later rollback, as with normal
mutations. Never reset that sequence as a repair. Successful no-op corrections
still establish a fresh identity; retrying an already applied entry does not
execute it or rotate identity again. NAS duration remains unmeasured.

<a id="application-adding-row-versions-to-an-existing-database"></a>

#### Adding row versions to an existing database

The first migration after upgrading to a golem that has row versions adds the
`golem_version` column to every model with `Subscriptions` and creates the
version source (the `CACHE 1` sequence on PostgreSQL, the counter table on
SQLite) before the first column is added. Adding the column is an ordinary
additive change and needs no approval. On PostgreSQL it sets a constant default
and does not rewrite the table; on SQLite it is an `ALTER TABLE ... ADD
COLUMN`. Existing rows keep version 0.

<a id="application-events-written-before-a-regeneration"></a>

#### Events written before a regeneration

Each queued event records the schema that wrote it. After a regeneration that
changes a model's event schema, or moves the generated contract to a newer
format, the new build cannot decode the undelivered events written under the old
schema. The publisher blocks each one with
`last_failure_code = 'schema-unavailable'`, visible through
`EventOperator().Inspect`, and its decode error is
`GOLEM_EVENT_SCHEMA_UNAVAILABLE`. Retire it with `EventOperator().Retire`, or
deliver it first: follow the [outbox-drain runbook](#upgrading-draining-the-event-outbox)
on the old compatible build. A bundle generated by an older golem is refused
in `HistoricalEventBundles` when its contract format is no longer supported.

**go/v0.9.0 retained-reader window:** go/v0.9.0 verifies
v0.8.8 Contract7 metadata through a frozen historical reader for event bundles
and offline archives. Active apps still require current Contract8 documents
and regeneration. The tested SQLite archive path retains V3 facts and explicitly
upgrades a recognized old queue on an extracted copy before restore; see the
[prior-release acceptance](DEVELOPMENT.md#compatibility). Contract formats
1–6 and fact codecs V1/V2 remain retired. This historical reader does not make
old generated app code an active current build, or grant current hidden-fact
scope authority to an old generation. Automatic writers still produce V3;
the current dual reader accepts V3/V4 without activating V4 deployment writers.

The stored event format itself is versioned. go/v0.8.1 reads only the current
format, which stores the row version with every fact; facts written by go/v0.8.0
or earlier (formats V1 and V2) are blocked with
`last_failure_code = 'format-retired'` and the decode error
`GOLEM_EVENT_FORMAT_RETIRED`, and nothing converts them. Drain on go/v0.8.0
before upgrading, or retire the blocked delivery and lose its events.

<a id="application-drift"></a>

#### Drift

Application startup and `golem doctor` compare the live database with the
reviewed migrations and report any difference as drift. Temporal columns
must match exactly, including the time zone: a `timestamp with time zone`
column altered to `timestamp without time zone` (or a `time` column gaining a
time zone) is drift.

<a id="application-limits-and-error-codes"></a>

### Limits and error codes

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

<a id="application-codes"></a>

#### Codes

| Code | Meaning |
|---|---|
| `BAD_USER_INPUT` | the request is malformed, or a value is invalid |
| `NOT_FOUND` | the target does not exist, or the caller cannot read it; the two are indistinguishable |
| `UNAUTHENTICATED` | the principal did not resolve, or the caller's policy could not be built |
| `FORBIDDEN` | policy refuses the request |
| `CONFLICT` | a concurrent write, a lock or a unique key got in the way; retrying may succeed |
| `LIMIT_EXCEEDED` | the request exceeds a configured limit |

<a id="application-reasons"></a>

#### Reasons

| Reason | Code | Meaning |
|---|---|---|
| `VALUE_TOO_LONG` | `BAD_USER_INPUT` | a value is longer than the field allows |
| `VALUE_PRECISION` | `BAD_USER_INPUT` | a decimal or time value is finer than the field's precision, or a datetime operand is finer than a microsecond where it cannot be rewritten (see [Datetimes](#application-datetimes)) |
| `VALUE_RANGE` | `BAD_USER_INPUT` | a value is outside the range the field supports |
| `INVALID_VALUE` | `BAD_USER_INPUT` | a value is invalid for its field in some other way |
| `LIMIT_EXCEEDED` | `LIMIT_EXCEEDED` | always set with `LIMIT_EXCEEDED` |
| `NOT_GRANTED` | `FORBIDDEN` | the caller has no grant at all for this action on this model |
| `FIELD_NOT_READABLE` | `FORBIDDEN` | the request uses a field where the caller cannot read it |
| `AUTHORIZATION_UNPROVEN` | `FORBIDDEN` | golem cannot establish that the rows a write locates are readable |
| `POLICY_INVALID` | `FORBIDDEN` | the policy cannot be applied to this request (see [When a policy cannot be applied](#application-when-a-policy-cannot-be-applied)) |

`NOT_GRANTED`, `AUTHORIZATION_UNPROVEN` and `POLICY_INVALID` are decided from
the policy and the request alone, before any row is read, so they reveal
nothing about stored data. A `FORBIDDEN` or `CONFLICT` without a reason, such
as `FORBIDDEN: mutation is not authorized`, is still possible.

<a id="application-limits"></a>

#### Limits

| `Limit` | Set by | Default |
|---|---|---|
| `touched_rows` | `MutationLimits.MaxTouchedRows`: rows one mutation writes, cascades included | 1,000 |
| `batch_rows` | the rows an `UpdateMany` or `DeleteMany` captures, or a nested `CreateMany` creates, bounded by the touched-row budget | 1,000 |
| `facts` | `MutationLimits.MaxFacts`: change events one transaction records | 1,000 |
| `outbox_bytes` | `MutationLimits.MaxOutboxBytes`: the encoded size of those events | 1 MiB |
| `nested_depth` | `MutationLimits.MaxNestedDepth` | 5 |
| `statement_parameters` | `MaxStatementParameters` on `MutationLimits` and `ReadLimits` | 999 |
| `statement_size` | `ReadLimits.MaxStatementBytes` | 1 MiB |
| `scalar_set_bytes` | encoded bytes of one compact scalar `In`/`NotIn` parameter; a fixed ceiling at the same scale as the SQL statement-size ceiling | 1 MiB |
| `statement_aliases` | `ReadLimits.MaxStatementAliases` | 2,048 |
| `selected_fields` | `ReadLimits.MaxSelectedFields` | 256 |
| `depth` | `ReadLimits.MaxRelationDepth` | 5 |
| `take` | `ReadLimits.MaxTake`, or a model's own maximum | none |
| `fanout` | `ReadLimits.MaxRelationFanout` | none |
| `transaction_ordinal` | the number of events one transaction can number | 2³² − 1 |
| `list_items` | the `golem.MaxItems` or `golem.GraphQLList` maximum a custom operation or computed field declares for its list result | declared |
| `hydrated_rows` | `GraphQLConfig.Limits.MaxHydratedRows`: identities requested when reloading custom or computed model results across one operation, or one subscription response | 1,000; ceiling 10,000 |
| `result_bytes` | `GraphQLConfig.Limits.MaxResultBytes`: canonical JSON bytes of the rendered `data` value in one response | 16 MiB; also the ceiling |

Hydration counts identities after deduplication within each model/projection
group, including missing and denied rows. Distinct projections and separate
result groups consume their own allowance. Chunk retries do not count twice.
The budget resets for each request or subscription response; mutation roots
share it even when a preceding write commits. Exceeding it after a successful
write returns `COMMITTED_RESULT_UNAVAILABLE` with `committed: true`,
`reason: LIMIT_EXCEEDED` and `limit: hydrated_rows`.

The result byte limit counts aliases, object/list syntax and escaped string
bytes after GraphQL null propagation. It applies to HTTP, WebSocket, SSE and
direct server execution. An oversized result omits `data`; errors and protocol
envelopes remain available to explain the refusal. If writes have confirmed
commits, the error is `COMMITTED_RESULT_UNAVAILABLE` with `committed: true`,
`reason: LIMIT_EXCEEDED` and `limit: result_bytes`. This is a rendered-result
limit: resolver work and writes may already have completed when it is reached.

The mutation defaults are also ceilings: you may lower them, never raise them.
A limit that only the fields or relation hops your policy needs would exceed is
`POLICY_INVALID`, not `LIMIT_EXCEEDED`, because the caller cannot fix it.

Deletes and other writes are sized differently in the outbox. A created or
updated event records only the row's identity, because subscribers read the
row when the event is delivered. A deleted event also records the row's
[delete snapshot](#application-the-delete-snapshot). A snapshot over
`MutationLimits.MaxDeleteSnapshotBytes` does not fail the delete: it is stored
as identity only. The transaction as a whole still fails with `outbox_bytes`
once its events exceed `MaxOutboxBytes`.

<a id="application-field"></a>

#### `Field`

- A value refusal names the field whose value was refused.
- A read refusal names a field only when your request named it: in a filter,
  an order, `Distinct`, `Omit`, a selector, a cursor or a selection. A field
  that only the policy uses is never named, so the error cannot reveal how the
  policy is written.
- A create refused because one of the fields you set has no create grant
  names that field.
- `fanout` and nested `batch_rows` limits name the relation field you used.
  Other limits name no field.

<a id="application-testing-errors"></a>

#### Testing errors

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

<a id="application-over-graphql"></a>

#### Over GraphQL

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

<a id="application-errors-for-a-request-the-server-refuses"></a>

#### Errors for a request the server refuses

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

Input node and depth limits cover both the raw variable object and the
operation's combined argument shape, each measured with one enclosing object.
Inline values, schema and variable defaults, and every use of a repeated
variable count toward the argument budget. Singleton values promoted to lists
count the resulting list containers too. These checks run before a resolver
or caller operation executes, using the same limits across HTTP, WebSocket,
SSE and direct server execution.

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

<a id="application-errors-for-a-value-the-operation-cannot-use"></a>

#### Errors for a value the operation cannot use

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

Wrong-shape and enum diagnostics also include schema-owned hints when available:
`extensions.expectedType`, `extensions.example` and `extensions.allowedValues`.
For example, an object supplied to `orderBy` is refused with its list type and
a valid example such as `[{id: asc}]`. An invalid enum value identifies the
nested argument path and the enum's actual GraphQL wire members; renamed enum
members must use those wire names. These hints never echo caller values or
private schema members. The [input-diagnostic acceptance](../go/runtime/graphql_relation_empty_test.go)
executes the suggested `orderBy` example and checks that invalid input runs no SQL.

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

<a id="application-errors-you-will-meet-early"></a>

### Errors you will meet early

| Error | Cause |
|---|---|
| `P1_BINDING_POLICY_REQUIRED` | an exposed model has no `DefinePolicy` |
| `generated applications require a reviewed non-empty migration history` | `generate` ran before `migration new` |
| `CONFLICT: mutation conflicted` | a unique constraint rejected the write, or on PostgreSQL a concurrent write touched the same rows, which a retry resolves |
| `FORBIDDEN: operation is not granted` | the caller's policy grants no update, delete or create on the model at all (see [Write rules](#application-write-rules)) |
| `LIMIT_EXCEEDED: request exceeds the touched_rows limit of 1000` | a write, with its cascades, touches more rows than `MutationLimits.MaxTouchedRows` |
| `GOLEM_SUBSCRIPTION_RESYNC` | an event stream may have missed events; reload and subscribe again (see [Streams end with a code](#application-streams-end-with-a-code)) |
<!-- /chapter:application -->

<!-- chapter:queue -->
<a id="queue"></a>

## Durable jobs and actions

Background work that survives a restart. Jobs live in your application's own
database, so enqueueing can share a transaction with the write that caused it —
no broker, and no window where the row committed but the job did not.

The program on this page is executed by `TestQueueApplicationRuns`.

**go/v0.9.0 P11:** go/v0.9.0 adds the priority, promotion and
explicit storage-upgrade sections below. These APIs and commands are not in
go/v0.8.8. Use matching v0.9.0 CLI/library builds and regenerate the
application for `PromoteJob`; the existing runnable recipe remains separate.

<a id="queue-registering-a-job-type"></a>

### Registering a job type

A job type binds a payload struct to a handler. Registration happens before the
application opens, so a misconfigured type fails at startup rather than when
the first job runs.

```go
registry := queue.NewRegistry()

welcome, err := queue.Register(registry, queue.Definition[Welcome]{
	Type:        "welcome",
	MaxAttempts: 3,
	Timeout:     5 * time.Second,
	Handle: func(_ context.Context, job queue.Job[Welcome]) error {
		fmt.Printf("welcoming %s\n", job.Payload.Handle)
		return nil
	},
})
```

| Field | Meaning |
|---|---|
| `Type` | stable name, stored on the row; lower-case `[a-z0-9._-]`, at most 128 bytes |
| `Handle` | the work; returning an error schedules a retry |
| `MaxAttempts` | attempts before the job is dead-lettered; zero means 5 |
| `Timeout` | per-attempt limit; the handler's context is cancelled and the attempt is retried; zero means 10 minutes |
| `Backoff` | `Base` and `Cap` for the retry delay, exponential with full jitter; zero means 5 seconds and 5 minutes |
| `MaxConcurrent` | ceiling on leases of this type held at once by one worker process; zero means unbounded. Fleet-wide budgets are `QueueConfig.Resources` |
| `ExclusiveBy` | derives a key; jobs sharing one never run concurrently, across every worker |

Pass the registry when opening:

```go
application, err := notes.Open(ctx, notes.Config[Principal]{
	Database: database,
	Queue:    &golemruntime.QueueConfig{Registry: registry},
	...
})
```

Without `Queue`, `Enqueue` and `RunQueueWorker` refuse rather than silently
doing nothing.

<a id="queue-enqueue-and-run"></a>

### Enqueue and run

```go
pending, err := welcome.New(Welcome{Handle: "ada"})
jobID, err := application.Enqueue(ctx, pending)
```

`New` marshals and validates the payload against the registered type;
`Enqueue` writes the row. A worker claims and executes:

```go
go application.RunQueueWorker(ctx)
```

`RunQueueWorker` blocks until its context is cancelled. Run one per
application: a second concurrent call on the same application returns
`CodeWorkerRunning`. Several processes may each run one against the same
database, and claims are exclusive.

<a id="queue-leases"></a>

#### Leases

A claim leases the job for `LeaseDuration` (30 seconds by default) and the
worker renews the lease every third of that while the handler runs. A renewal
that fails while the lease is still valid is tried again at the next renewal.
A renewal the store refuses, or one that fails once the lease has expired,
cancels the handler's context and discards its outcome; once the lease expires
another worker may claim the job, and that claim counts a new attempt. Renewals also carry `Cancel` requests to a running handler.

<a id="queue-enqueue-inside-your-own-transaction"></a>

#### Enqueue inside your own transaction

```go
err := caller.Transaction(ctx, func(tx *notes.CallerTx[Principal]) error {
	if _, err := tx.Notes.Create(ctx, notes.Notes.Create(notes.Notes.Title.Create("hello"))); err != nil {
		return err
	}
	_, err := tx.Enqueue(ctx, pending)
	return err
})
```

The job row commits with the note or neither does. This is the reason the queue
lives in your database rather than a broker.

<a id="queue-retries"></a>

### Retries

A handler that returns an error is retried until `MaxAttempts`, with the
backoff you configured. The program below registers a job that fails once:

```
welcome=succeeded attempt=1
flaky=succeeded attempt=2
```

`Attempt` counts executions, so a job that succeeded second time reports 2.
After the final attempt the job becomes `failed` with
`LastCode = attempts_exhausted`.

`queue.Terminal(err)` fails the job at once with `LastCode = terminal`, leaving
its remaining attempts unspent. `queue.CompletedWith(code, err)` records
success together with a code describing what degraded. A handler that panics
is recovered and retried like an error, with `LastCode = handler_panic`.

Return `queue.RetryIn(delay, err)` to override the backoff, or
`queue.RetryInWithoutAttempt(delay, err)` to defer **without** consuming an
attempt — for a dependency that is unavailable rather than a failure. Both
delays are clamped to 24 hours. That second form never exhausts attempts, so a
handler that always returns it produces a job that never dead-letters and never
ages out. Bound it on something other than the attempt count.

<a id="queue-options"></a>

### Options

```go
welcome.New(payload, queue.After(30*time.Second))
welcome.New(payload, queue.Dedupe("welcome:ada"))
```

`After` delays first execution by up to 24 hours. `Dedupe` coalesces:
enqueueing an identical key while a job carrying it is pending or leased
returns that job's ID rather than creating a second; once the job is terminal
the key is free again. Keys are at most 256 bytes. `New` returns the error for
an option it refuses.

<a id="queue-priority-and-promotion-unreleased-p11"></a>

<a id="queue-priority-and-promotion-gov090-p11"></a>

### Priority and promotion (go/v0.9.0 P11)

There are exactly two lanes: `queue.PriorityNormal` (`0`, the default) and
`queue.PriorityInteractive` (`1`). Set a job's lane when constructing it:

```go
pending, err := welcome.New(payload, queue.WithPriority(queue.PriorityInteractive))
```

`pending.Priority()` reports the requested lane. `queue.Job[T]`, `queue.Meta`,
`queue.Status` and `queue.Failure[T]` carry a `Priority` field. Prefer keyed
struct literals when updating: these fields and `Limits.ReservedInteractive`
are additions to public structs. Custom `queue.Operator` implementations and
mocks must also implement the new `Promote` method.

An enqueue that coalesces through `queue.Dedupe` returns the existing job
unchanged, even if the new request asks for Interactive or a shorter delay.
Retries, lease release/reclaim and operator requeue preserve the stored lane.
Promotion is a separate, explicit operation:

```go
changed, err := tx.PromoteJob(ctx, jobID, queue.PriorityInteractive)
// Explicitly remove remaining delay as well:
changed, err = tx.PromoteJob(ctx, jobID, queue.PriorityInteractive, queue.ReadyNow())
// Trusted operator equivalent, outside a caller transaction:
changed, err = application.QueueOperator().Promote(ctx, jobID, queue.PriorityInteractive)
```

The generated `CallerTx` and `SystemTx` both expose `PromoteJob`. It commits
or rolls back with the transaction and wakes the local worker after a changed
transaction commits. `Promote` on the operator changes durable state; workers
observe it through their normal polling.

**Authorize the business row before deriving its job ID.** In a caller
transaction, read the action through the caller's policy, check that the
business action permits promotion, and obtain `jobID` from that row in the
same transaction. Never promote an ID taken directly from request arguments.
`PromoteJob` does not apply a job ownership policy. A queue mutation and any
associated status write must share the transaction; avoid slow work while
holding its locks.

Only `pending` rows change. Promotion never lowers priority or resets
attempts, payload, dedupe or exclusivity keys. It preserves `AvailableAt`
unless `queue.ReadyNow()` is explicit; that option can accelerate an already
Interactive pending job without moving an already-due job later. A lower
priority request changes nothing, including its delay. Missing, leased
(including expired leases), canceled, failed and succeeded jobs return
`changed == false`. Repeating an already-applied request also returns false.

<a id="queue-scheduling-scope-and-reserved-slots"></a>

#### Scheduling scope and reserved slots

One continuously running worker gives Normal a scheduling opportunity after
at most three Interactive handler admissions when capacity for Normal is
available. With both lanes eligible, a mixed-work round admits up to three
Interactive jobs and one Normal job; an empty Interactive lane allows Normal
batching. The worker rotates capped/uncapped type cohorts and resource groups
within each lane. This is a per-worker opportunity bound, not a FIFO,
completion-order, per-job waiting-time or fleet-wide ratio guarantee. A
restart starts a new scheduling history.

```go
limits := queue.DefaultLimits()
limits.Concurrency = 4
limits.ReservedInteractive = 1
```

`ReservedInteractive` defaults to `0`. It must be nonnegative and strictly
less than the resolved `Concurrency`. In this example, Normal can occupy at
most three local slots; Interactive can use any free slot. A reservation can
leave capacity idle when only Normal work is waiting. It is fixed at startup,
not a `queue.Plan` field, and a live concurrency reduction to the reservation
or below is refused.

Priority and reservations never interrupt an existing handler. They do not
reserve shared resource capacity or bypass `MaxConcurrent`, weighted
`QueueConfig.Resources`, exclusivity, delays or lease fencing. Busy workers
or an exhausted resource may still delay Interactive work.

<a id="queue-inspecting-and-operating"></a>

### Inspecting and operating

```go
operator := application.QueueOperator()
status, err := operator.Inspect(ctx, jobID)
```

`Status` carries `ID`, `Type`, `State`, `Attempt`, `MaxAttempts`,
`AvailableAt`, `LastCode`, `CancelRequested`, `EnqueuedAt` and `FinishedAt`,
and never the payload. The operator also offers `List`, `ListFailed`,
`CountByState`, `Cancel`, `CancelMany`, `Requeue`, `RequeueFailed` and
`RunRetention`. Pages and bulk actions are bounded at 256 jobs.

To cancel from inside your own transaction, call `tx.CancelJob(ctx, jobID)` on a
`CallerTx` or `SystemTx`. It returns the same `(bool, error)` as `Cancel` and
commits or rolls back with your other writes. Cancel only a job ID you read, in
the same transaction, from a row your policy allowed; never an ID taken from
arguments. Do no slow work after it in the same transaction: on PostgreSQL the
job row stays locked until commit.

`Cancel` is immediate for a pending job and cooperative for a leased one: the
request is recorded, the running handler's context is cancelled at its next
lease renewal, and completion may win first.

A handler returning is not the same as the job being recorded succeeded. If you
poll for a terminal state immediately after your handler runs, you will observe
`leased` before you observe `succeeded`.

<a id="queue-worker-limits"></a>

### Worker limits

`QueueConfig.Limits` bounds the worker. Zero-valued fields take these
defaults:

| Field | Default |
|---|---|
| `Concurrency` | 4 handlers at once |
| `ClaimBatch` | 16 jobs per claim |
| `LeaseDuration` | 30s |
| `PollInterval` | 250ms |
| `ShutdownGrace` | 15s |
| `AbandonGrace` | 5s |
| `MaxPayloadBytes` | 1 MiB, which is also the ceiling |
| `RetentionAge` | 30 days |
| `RetentionEvery` | 1 minute |
| `RetentionRows` | 256 per pass |

`Open` refuses limits outside their ranges with `CodeConfigInvalid`:
`LeaseDuration` 1s..10m, `PollInterval` at most 24h, `AbandonGrace` at most
2m, `RetentionAge` 1h..10y, `RetentionEvery` 1m..24h or `RetentionDisabled`,
and `RetentionRows` at most 4096.

<a id="queue-changing-limits-without-a-restart"></a>

#### Changing limits without a restart

`app.ReconfigureQueue(ctx, queue.Plan{...})` changes the live worker's
`Concurrency`, `ClaimBatch`, per-type `MaxConcurrent` and resource capacities.
`app.QueuePlan()` reports what is active.

```go
status, err := app.ReconfigureQueue(ctx, queue.Plan{
	MaxConcurrent: map[string]int{"analyze": 8},
	Capacities:    map[string]int{"analyze_pool": 8},
})
```

- **A plan states only the fields it changes.** A zero `Concurrency` or
  `ClaimBatch`, and any type or resource the plan leaves out, keeps the value
  in the active plan, so an earlier change survives a later one. A
  `MaxConcurrent` of 0 for a type means uncapped.
- **A refused plan changes nothing.** A negative value, an unregistered type,
  an unknown resource, a `ClaimBatch` above the maximum or a capacity below a
  cost returns `CodeConfigInvalid` and the old plan stays active.
- **Raising applies on the next claim, and lowering never interrupts.** There
  is no drain: running handlers finish, and nothing new is claimed until the
  live count is under the new value. When `ReconfigureQueue` returns, no claim
  computed from the old plan is still to start.
- **It works before `RunQueueWorker` starts and while it runs.**
- **`PlanStatus.Generation`** starts at 1 and rises by one for every accepted
  plan; `AppliedAt` is wall time. The plan lives in the process. It is not
  stored, so after a restart the application applies its own saved plan again
  after `Open`.
- **Resource capacity is fleet-wide.** Each claim uses the smaller of its own
  capacity and the capacity recorded on every live lease. After you raise a
  resource from 2 to 8, jobs already leased under 2 hold the budget at 2 until
  they finish; the new 8 takes effect as they end. Replicas that disagree on
  a capacity never exceed the smallest value held by a live lease.
  `Concurrency`, `ClaimBatch` and `MaxConcurrent` are per process.
- **Not changeable live:** resource costs and membership, `LeaseDuration`,
  `PollInterval`, `ShutdownGrace`, `AbandonGrace`, `MaxPayloadBytes` and the
  retention settings. Change those with a restart, and drain workers first
  for costs and membership.
- **Observation.** Each plan outcome emits one `PhaseApply` queue observation
  with the type name `plan`.

<a id="queue-retention"></a>

### Retention

Workers delete terminal job rows older than `RetentionAge` every
`RetentionEvery`, at most `RetentionRows` per pass. **This is on by default** —
30 days, checked every minute. Live rows are never deleted.

To keep history indefinitely:

```go
limits := queue.DefaultLimits()
limits.RetentionEvery = queue.RetentionDisabled
```

and pass `Limits: limits` in `QueueConfig`. History then survives until an
operator calls `RunRetention`.

<a id="queue-the-queues-own-tables"></a>

### The queue's own tables

The following describes the released storage checks. The go/v0.9.0 P11
upgrade below requires a complete recognized shape before startup proceeds.

Golem creates `golem_queue` and its indexes when the application opens with
`Queue` set, and checks then, and again each time `RunQueueWorker` starts,
that the primary key is exactly `id` and that **every index on `golem_queue`
is one golem itself defined, with golem's exact definition**. A same-named
index with a different shape, or any other index on the table, stops startup
with an error naming the object. So does a golem index that is missing because
an object elsewhere in the database already carries its name. Drop it and let
golem recreate its own; accepting a wrong-shaped `golem_queue_dedupe` would
quietly turn off deduplication.

On SQLite the table is `main.golem_queue`. On PostgreSQL it lives in the
system namespace, `_golem` unless your schema names another.

If you added your own index to `golem_queue`, startup now refuses it. Golem
owns this table, so move that index to a table you own.

<a id="queue-explicit-storage-upgrade-unreleased-p11"></a>

<a id="queue-explicit-storage-upgrade-gov090-p11"></a>

#### Explicit storage upgrade (go/v0.9.0 P11)

The go/v0.9.0 worker requires the `priority-v1` storage shape. Startup
creates current storage only when the queue is absent; it refuses recognized
legacy storage with `QUEUE_STORAGE_UPGRADE_REQUIRED`. It does not add the
priority column or replace the claim index automatically. Unknown or partial
shapes are refused as `QUEUE_STORAGE_UNRECOGNIZED`; do not manually drop
objects to make an incomplete shape look current.

Fresh creation and installation of newly admitted operator-history indexes
commit in one serialized provider transaction. A failed creation leaves the
previous complete shape intact and can be retried. PostgreSQL startup and
explicit upgrades share the same namespace lock; SQLite uses its immediate
write transaction. This does not repair storage that was already partial.

Use the provider-owned maintenance commands against an existing database:

```sh
golem queue storage inspect --provider sqlite --dsn 'file:notes.db'
# Stop all queue producers and workers before this command:
golem queue storage upgrade --provider sqlite --dsn 'file:notes.db'

golem queue storage inspect --provider postgresql --dsn "$DATABASE_URL" --system-namespace _golem
golem queue storage upgrade --provider postgresql --dsn "$DATABASE_URL" --system-namespace _golem
```

Both commands emit JSON with `version` and `historyIndexes`. `inspect` reads
catalogs without upgrading or repairing storage. SQLite inspection uses a
read-only connection rather than the application opener; neither command
creates a missing SQLite database. Memory databases and create-enabled SQLite
DSNs are refused. `upgrade` also refuses an existing database with no queue.

SQLite accepts `--sqlite-profile local` (default), `network`, or `network-wal`;
choose the profile used by the application. Upgrade uses the corresponding
verified provider opener. Inspection does not switch journal mode, and
`network-wal` inspection requests exclusive locking. SQLite's namespace is
`main`. PostgreSQL defaults `--system-namespace` to `_golem`; supply the actual
generated system namespace when it differs. SQLite network-profile flags do
not apply to PostgreSQL. Errors do not echo the supplied DSN.

The supported shapes form a closed set:

| Reported version | Meaning and upgrade behavior |
|---|---|
| `absent` | no queue objects; inspection succeeds, upgrade refuses |
| `pre-resource` | complete legacy queue before all three resource-lease snapshot columns; explicit upgrade adds them and priority |
| `v0.8.8` | complete legacy queue with resource snapshots and the old claim index; explicit upgrade adds priority |
| `priority-v1` | complete current shape; repeated upgrade preserves it |

These labels describe exact provider-owned shapes, not arbitrary historical
tables or a version string supplied by the operator. Optional operator-history
indexes must be either the complete known set or absent. Upgrade preserves
that choice, existing job rows, payloads, attempts, availability, lease tokens
and resource snapshots. Existing jobs receive Normal priority. The column and
claim-index changes commit together; a failure rolls them back.

**Drain and stop every producer and worker, including old binaries, before
upgrade.** Stop processes that can enqueue through requests, hooks or other
background work too. Keep them stopped until the command succeeds and the
matching new application is deployed. The command's database locks serialize
the storage change; they do not certify a fleet drain or prevent an old
process from resuming afterward. An old worker's next startup rejects the new
canonical claim index, but an already-running old worker is not fenced out.

<a id="queue-operator-history-indexes"></a>

#### Operator-history indexes

Three indexes serve the operator surface rather than the claim loop:

| Index | Columns | Serves |
| --- | --- | --- |
| `golem_queue_enqueued` | `(enqueued_at, id)` | `List` with no state filter |
| `golem_queue_history` | `(status, enqueued_at, id)` | `List` filtered by state |
| `golem_queue_terminal` | `(status, finished_at, id)` | `ListFailed` and retention |

On SQLite the store names them with `INDEXED BY`; on PostgreSQL the planner
chooses.

**Golem creates them only when your generated schema admits them.** That
happens when you regenerate and author the migration that records them, so a
released library upgrade for these history indexes on its own changes nothing
and stays reversible. This is separate from the go/v0.9.0 priority storage
upgrade above. Until you
regenerate, the queue behaves exactly as it did before, with no speedup. The
same regeneration also delivers the semantic strike column, so one action
delivers both.

<a id="queue-timeouts-and-abandonment"></a>

### Timeouts and abandonment

When a handler exceeds its `Timeout`, its context is cancelled. Go cannot kill
a goroutine, so if the handler ignores cancellation and keeps running past
`AbandonGrace` (5 seconds by default), the worker records the retry anyway,
counting the attempt; on the last attempt the job becomes `failed`. The job
becomes claimable while the abandoned goroutine is still executing — it can
run twice, concurrently.

On shutdown a running handler gets `ShutdownGrace` to finish before its
context is cancelled. An error it returns after that is not recorded, and the
job is reclaimed once its lease expires; a success it returns within
`AbandonGrace` of the cancellation is still recorded.

Handlers must honour `ctx.Done()`. This is the one place the queue cannot
protect you.

<a id="queue-not-included"></a>

### Not included

- **Recurring or cron scheduling.** A loop calling `Enqueue` with `Dedupe` is a
  few lines; building it in drags timezones, drift and leader election into a
  queue that has none of them.
- **Arbitrary priority levels or preemption.** The go/v0.9.0 two-lane policy
  above does not interrupt busy workers or provide a fleet scheduler.

<a id="queue-application-reference-bounded-removal-unreleased-p10"></a>

<a id="queue-application-reference-bounded-removal-gov090-p10"></a>

### Application reference: bounded removal (go/v0.9.0 P10)

The [bounded maintenance application](../go/internal/compiler/compile/testdata/boundedmaintenance/schema.go)
and its [generated acceptance tests](../go/internal/generate/pipeline/maintenance_acceptance_test.go)
show how to remove a large root using existing transactions and the durable
queue. `Maintenance`, `RequestRemoval`, `ProcessPage`, `IngestCaller`,
`IngestSystem` and their `IntoTx` variants are **application functions in that
fixture**, not new public Golem APIs. This reference requires matching
v0.9.0 CLI/library builds and fresh generation.

The caller command authorizes the root, marks it logically removed, creates
a persistent removal task, and enqueues its first page in one transaction.
Read policies immediately hide the root and its episodes. The task survives
physical root deletion and records the owner, generation, expected sequence,
phase, deleted count and completion status. The example deliberately grants
the owner access to that progress, including counts of private physical
leaves. An application that must hide those counts must expose a coarser
status or apply appropriate field policy; background authority does not grant
progress disclosure automatically.

Every ingest, reparent and removal entry point must share the root fence.
The reference orders locks for both old and new roots, checks their removal
state, and conditionally increments a root `Fence` value inside the same
transaction as the descendant write. The actual compare-and-write matters:
`LockKeys` alone does not refresh an older database snapshot, and is a no-op
on SQLite. Caller and System commands share one implementation; standalone
commands wrap it in a transaction, while `IntoTx` composes it with an existing
transaction whose owner controls retry and commit. Route **all** application
writers through these commands. A direct generated caller/System write can
bypass this application invariant; System does not gain universal hooks.

Each cleanup transaction verifies the persisted task generation/sequence and
root fence, then performs one phase:

| Phase | Bounded work |
|---|---|
| Private leaves | Read up to 32 Asset IDs in primary-key order and delete exactly those leaves. |
| Episodes | First prove no Asset remains under the root, then delete up to 32 childless Episodes in primary-key order. |
| Root | Prove no Episode remains, physically remove the root and mark the task complete together. |

The next page, progress update and deletion facts commit together. Empty
pages advance the phase. Pages select from the remaining rows, without an
offset; the shared ingest fence prevents new descendants from entering a
removed root. The fixed page size bounds physical effects, not just the
number of parents selected: a normal page has at most 32 deletions plus the
root-fence and task updates, and final removal has three facts. Titles and
bodies use `varchar(1024)` (a character bound, not a 1024-byte promise). Adding
larger fields or more descendant types requires reassessing page effects and
snapshot size. The 1,000 touched-row and 1 MiB causation limits stay unchanged;
this workflow does not exempt cleanup from them.

Job dedupe keys include task, generation and sequence. A single root-wide key
could coalesce a successor into the still-leased current job. Task progress
also fences the acknowledgement gap: if a page commits and the worker dies
before acknowledging it, replay observes committed progress and does no more
work. A concurrent stale attempt can be refused by the final task CAS and
retried. Keep the task record and do not reuse root identities: the retained
unique root/task association is part of this reference's idempotency model.
Generate command identities before retryable work, and perform no external
I/O inside transaction retries.

`TestFreshGeneratedBoundedMaintenance` runs the real generated application on
SQLite and PostgreSQL: the unbounded cascade over 511 episodes and 1,022
private assets refuses and rolls back, while real worker pages converge. It
also covers enqueue faults, replay, caller/System ingest and reparent commit
orders, duplicate pages, surviving row versions and complete fact ordinals.
PostgreSQL version-sequence gaps are allowed. Its byte assertions measure
**raw stored BLOB payloads**, not canonical encoder accounting or a general
proof that arbitrary content fits the limit.

`TestFreshGeneratedBoundedMaintenanceSemantic` adds a real Episode index to a
separate 33-episode variant on SQLite and PostgreSQL with pgvector. It verifies
that logical removal hides already-ready vectors immediately, an episode-page
semantic-drain enqueue
failure rolls back rows, versions, facts, marks, progress and queue work, and
the real worker eventually removes owned vectors and shadow state.
Both providers run the same assertions; PostgreSQL uses the explicitly
configured disposable pgvector fixture. NAS latency and performance have not
been measured by either acceptance; bounded transactions are not a latency or
preemption guarantee.

<a id="queue-recipe-durable-actions"></a>

### Recipe: durable actions

An action that takes longer than a request (an export, a render, an import) has
the same shape every time. The caller asks, gets a handle back at once, and
watches the handle change. The work happens in a job, and the job may run more
than once, fail, or be cancelled. This recipe wires that shape from parts
golem already has, and the program at the end of it runs as written.

<a id="queue-the-status-row-is-the-handle"></a>

#### The status row is the handle

Model the action's state as an ordinary model, keyed by the action's domain
identity (here, the export's name) rather than by the job ID. It carries
`golem.Subscriptions`, so a client watches it with `useGolemQuery` and sees
`queued` become `succeeded` without polling, and a policy, so only its owner
reads or writes it. Expose only the read operations over GraphQL: the status
changes through your mutations and your handler, never through a generic
`updateExport`.

<a id="queue-accepting-the-action"></a>

#### Accepting the action

`requestExport` is a [custom mutation](#application-custom-operations). In one
`caller.Transaction` it enqueues the job and upserts the status row as
`queued`. The job carries `queue.Dedupe` set to the same domain key, so asking
twice while a job is still active returns the first job's ID instead of
starting a second. The status row and the job commit together or not at all,
and the mutation returns the row as the handle. The owner is not an argument:
the mutation reads it from `golem.ActorFrom[Actor](ctx)`, so a client cannot
name someone else. When you call the mutation from Go, go through the
generated `Mutate`, as the program below does; calling the resolver function
directly skips the actor. The policy is the ownership check: an upsert over someone else's row is refused as an update the caller
may not make.

<a id="queue-the-acknowledgement-window"></a>

#### The acknowledgement window

The handler's transaction and the queue's acknowledgement are separate commits,
so delivery is at-least-once and there are two windows where "handler
committed, queue not yet acknowledged". A request can arrive after `RunExport`
committed `succeeded` but before the worker recorded the job as done; `Dedupe`
still sees the active job and returns its ID. `requestExport` therefore compares
the returned job ID with the row's stored one: the same ID is a coalesced
request and the row is returned untouched, a different ID is a new job (the
previous one is terminal) and the row is reset to `queued`. That reset is the
recipe's reprocess rule: asking again after a job has finished runs the export
again. Likewise, lease recovery can fail a job whose handler already committed
`succeeded`, so the failure callback updates only the row whose job ID equals
`failure.ID` and whose status is neither `succeeded` nor `canceled`.

<a id="queue-every-new-row-gets-a-job"></a>

#### Every new row gets a job

When the rule is "every new post gets a job" there is no mutation to put the
call in. Enqueue from an `AfterCreate` hook:

```go
func (Post) AfterCreate(ctx context.Context, result PostCreateResult) error {
	pending, err := thumbnailJob.New(Thumbnail{PostID: result.Row().ID})
	if err != nil {
		return err
	}
	_, err = golem.HookEnqueue(ctx, result.Executor(), pending)
	return err
}
```

`HookEnqueue` writes the job on the hook's own transaction, so the job exists
if and only if the post commits. That is the reason not to use `AfterCommit`
here: an after-commit hook runs once the transaction is already durable, and a
crash or an error between the commit and the enqueue loses the job with
nothing left to retry it from. See the
[Hooks](#application-hooks) section for the full contract.

<a id="queue-the-handler-runs-at-least-once"></a>

#### The handler runs at least once

The queue delivers a job at least once. A worker that dies after the handler
finished but before the job was recorded succeeded leaves a job another worker
will claim and run again, and a lease that expires mid-run does the same. The
handler must therefore be safe to run twice:

1. Read the status row first. If it is already `succeeded` or `canceled`,
   return: the work is done or no longer wanted.
2. Do the work.
3. In one `SystemTx`, write the result, set the status to `succeeded`, and
   enqueue the next stage if there is one.

Step 3 is the whole trick. The result, the status and the next job commit
together, so a second run either sees `succeeded` at step 1 and stops, or
finds nothing committed and redoes the work. It never finds a result without
its status, or a status without its next stage. `RunExport` below keeps its
work small: it reads the row and writes the result in the same transaction. A
handler with slow work reads the row first, does the work outside any
transaction, and writes in a second `SystemTx`.

When a stage has several parents, the last parent to finish enqueues the
child. Each parent marks itself done and counts the parents still open under a
lock key on the action, and only the one that sees none left enqueues the child:

```go
err := system.Transaction(ctx, func(tx *SystemTx[Principal]) error {
	if err := tx.LockKeys(ctx, golem.MustLockKey("export", exportID)); err != nil {
		return err
	}
	if _, err := tx.Parts.Update(ctx, Parts.ByID.Value(partID), Parts.Update(Parts.Done.Set(true))); err != nil {
		return err
	}
	open, err := tx.Parts.Count(ctx, Parts.Where(Parts.ExportID.Eq(exportID).And(Parts.Done.Eq(false))))
	if err != nil || open > 0 {
		return err
	}
	_, err = tx.Enqueue(ctx, assemble)
	return err
})
```

The child's `queue.Dedupe` on the action key is the second guard: if two
parents both see none left, the second enqueue returns the first job.

<a id="queue-failure"></a>

#### Failure

A handler that returns an error is retried until its attempts run out. The
last failure moves the job to `failed`, and a handler that crashes on its last
attempt never gets to write anything. `OnJobFailed` closes that gap. golem runs
the callback in the transaction that marks the job failed, so the status row
becomes `failed` with a safe code (`attempts_exhausted`, `terminal`) if and
only if the job does. Pass the code, never the error text: the status row is
read by the owner's client.

<a id="queue-cancel"></a>

#### Cancel

`cancelExport` sets `cancel_requested` on the row and calls `tx.CancelJob` on
the job in the same transaction, so the job's cancellation commits or rolls back
with the `canceled` status. Cancel only a job ID you read, in that transaction,
from a row the caller's policy allowed; never cancel an ID taken from the
arguments. Cancellation is cooperative, not a
rollback. A job that has not started is cancelled at once. A job that is
running is told at its next lease renewal, and may finish first. Work already
done stays done. That is why the handler's success write is conditional on the row still being
`queued`: a handler that finishes after the cancel must not overwrite `canceled`
with `succeeded`. Reading the status first is not enough. Under PostgreSQL's
default Read Committed isolation a cancel or failure can commit between the read
and an unconditional write, so the write itself carries the status predicate and
zero affected rows means the export was cancelled or failed meanwhile. A
handler with several steps repeats the check between steps. In the program
below the mutation marks a queued row `canceled` in its own transaction, and
`RunExport` declines any row that is no longer `queued`.

<a id="queue-retry-or-reprocess"></a>

#### Retry or reprocess

A custom mutation resets one stage's row to `queued` and enqueues it again,
exactly as `requestExport` does: the same transaction, the same `Dedupe` key.
Reprocessing a succeeded export is the same call. The handler's first check
looks at the row, finds `queued`, and does the work again.

<a id="queue-the-program"></a>

#### The program

Four files and three commands. The schema holds the status model and its
policy:

```go
// durable/schema.go
package durable

import "github.com/eleven-am/golem/go/golem"

type Principal struct{ UserID golem.UUID }

type Actor struct {
	UserID        golem.UUID
	Authenticated bool
}

type Status string

const (
	StatusQueued    Status = "queued"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

func (Status) GolemEnum() golem.EnumSpec[Status] {
	return golem.DefineEnum(
		golem.EnumValue(StatusQueued),
		golem.EnumValue(StatusSucceeded),
		golem.EnumValue(StatusFailed),
		golem.EnumValue(StatusCanceled),
	)
}

type Export struct {
	_ struct{} `golem:"model;id=example.durable.Export;table=exports;graphql=Export"`

	ID              string     `db:"id" golem:"id=example.durable.Export.ID;pk;type=varchar(64)"`
	OwnerID         golem.UUID `db:"owner_id"`
	JobID           string     `db:"job_id" golem:"type=varchar(64)"`
	Status          Status     `db:"status" golem:"default=queued"`
	CancelRequested bool       `db:"cancel_requested" golem:"default=false"`
	ErrorCode       string     `db:"error_code" golem:"type=varchar(64)"`
	Result          string     `db:"result" golem:"type=varchar(200)"`
}

func (Export) GolemModel() golem.ModelSpec[Export] {
	return golem.DefineModel(
		golem.GraphQL[Export](golem.GraphQLOperations(golem.GraphQLFindOne, golem.GraphQLFindMany)),
		golem.Subscriptions[Export](),
	)
}

func (Export) DefinePolicy(rules *golem.Rules[Export], actor Actor) {
	if !actor.Authenticated {
		return
	}
	owned := Exports.OwnerID.Eq(actor.UserID)
	rules.CanRead(owned)
	rules.CanCreate(owned)
	rules.CanUpdate(owned)
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "durable")
	golem.Actor[Actor](schema)
	golem.Model[Export](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}
```

The actions, the handler and the failure callback live in one file. `ExportJob`
is a package variable because a custom operation has no handle to the queue's
job type; `main` sets it once, before the application opens. The generated `Caller`, `System`,
`Exports` and `Open` come from the next command:

```go
// durable/exports.go
package durable

import (
	"context"
	"errors"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/queue"
)

type ExportPayload struct {
	ID   string `json:"id"`
	Fail bool   `json:"fail"`
}

var ExportJob queue.Type[ExportPayload]

type RequestExportArgs struct {
	ID   string `golem:"graphql=id"`
	Fail bool   `golem:"graphql=fail"`
}

type CancelExportArgs struct {
	ID string `golem:"graphql=id"`
}

func DefineGraphQL(graphql *golem.GraphQLSchema) {
	golem.Mutation(graphql, "requestExport", RequestExport)
	golem.Mutation(graphql, "cancelExport", CancelExport)
}

func RequestExport(ctx context.Context, caller *Caller[Principal], args RequestExportArgs) (golem.Row[Export], error) {
	pending, err := ExportJob.New(ExportPayload{ID: args.ID, Fail: args.Fail}, queue.Dedupe("export:"+args.ID))
	if err != nil {
		return golem.Row[Export]{}, err
	}
	var handle golem.Row[Export]
	err = caller.Transaction(ctx, func(tx *CallerTx[Principal]) error {
		jobID, err := tx.Enqueue(ctx, pending)
		if err != nil {
			return err
		}
		existing, err := tx.Exports.FindMany(ctx, Exports.Where(Exports.ID.Eq(args.ID)))
		if err != nil {
			return err
		}
		if len(existing) == 1 {
			if stored, _ := golem.Value(existing[0], Exports.JobID).Get(); stored == string(jobID) {
				handle = existing[0]
				return nil
			}
		}
		handle, err = tx.Exports.Upsert(ctx, Exports.ByID.Value(args.ID),
			Exports.Create(Exports.ID.Create(args.ID), Exports.OwnerID.Create(golem.ActorFrom[Actor](ctx).UserID), Exports.JobID.Create(string(jobID)), Exports.ErrorCode.Create(""), Exports.Result.Create("")),
			Exports.Update(Exports.JobID.Set(string(jobID)), Exports.Status.Set(StatusQueued), Exports.CancelRequested.Set(false), Exports.ErrorCode.Set("")),
			Exports.Select(Exports.ID),
		)
		return err
	}, golem.RetryConflicts(3))
	return handle, err
}

func RunExport(ctx context.Context, system System[Principal], payload ExportPayload) error {
	return system.Transaction(ctx, func(tx *SystemTx[Principal]) error {
		current, err := tx.Exports.FindUnique(ctx, Exports.ByID.Value(payload.ID))
		if err != nil {
			return err
		}
		if status, _ := golem.Value(current, Exports.Status).Get(); status != StatusQueued {
			return nil
		}
		if payload.Fail {
			return errors.New("renderer unavailable")
		}
		_, err = tx.Exports.UpdateMany(ctx,
			Exports.ID.Eq(payload.ID).And(Exports.Status.Eq(StatusQueued)),
			Exports.UpdateMany(Exports.Status.Set(StatusSucceeded), Exports.Result.Set("export-"+payload.ID+".zip")),
		)
		return err
	})
}

func MarkExportFailed(ctx context.Context, tx *SystemTx[Principal], failure queue.Failure[ExportPayload]) error {
	_, err := tx.Exports.UpdateMany(ctx,
		Exports.ID.Eq(failure.Payload.ID).
			And(Exports.JobID.Eq(string(failure.ID))).
			And(Exports.Status.Ne(StatusSucceeded)).
			And(Exports.Status.Ne(StatusCanceled)),
		Exports.UpdateMany(Exports.Status.Set(StatusFailed), Exports.ErrorCode.Set(string(failure.Code))),
	)
	return err
}

func CancelExport(ctx context.Context, caller *Caller[Principal], args CancelExportArgs) (golem.Row[Export], error) {
	var handle golem.Row[Export]
	err := caller.Transaction(ctx, func(tx *CallerTx[Principal]) error {
		observed, err := tx.Exports.FindUnique(ctx, Exports.ByID.Value(args.ID))
		if err != nil {
			return err
		}
		jobID, _ := golem.Value(observed, Exports.JobID).Get()
		if _, err = tx.CancelJob(ctx, queue.JobID(jobID)); err != nil {
			return err
		}
		if _, err = tx.Exports.UpdateMany(ctx,
			Exports.ID.Eq(args.ID).And(Exports.JobID.Eq(jobID)).And(Exports.Status.Eq(StatusQueued)),
			Exports.UpdateMany(Exports.CancelRequested.Set(true), Exports.Status.Set(StatusCanceled)),
		); err != nil {
			return err
		}
		handle, err = tx.Exports.FindUnique(ctx, Exports.ByID.Value(args.ID))
		return err
	}, golem.RetryConflicts(3))
	return handle, err
}
```

`CancelExport` calls `tx.CancelJob` before it touches the status row because
the failure callback locks the job row first and its status row second; taking
them in the other order deadlocks on PostgreSQL. The status update also
matches the job ID it read. If a failure and a new `requestExport` replaced the
job between the read and the update, the update matches no row, the new job
stays queued, and the row read back at the end shows that. The mutation returns
that row, so the caller sees the cancelled status and not the pre-cancel values.
Both mutations also pass `golem.RetryConflicts(3)`, since a conflict is a normal
outcome there.

`main` passes `events.NewMemoryTransport` and a no-op operator audit because it
is one process with no audit sink. `Open` requires both on purpose: a memory
transport reaches no second replica, and a silent audit default would drop
operator actions unrecorded.

The program accepts three exports, cancels one before the worker starts, makes
one fail, and prints each final status:

```go
// cmd/durable/main.go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"example.com/notes/durable"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/eleven-am/golem/go/queue"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

func main() {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: "file:durable.db"})
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	transport, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		log.Fatal(err)
	}

	var application *durable.App[durable.Principal]
	registry := queue.NewRegistry()
	durable.ExportJob, err = queue.Register(registry, queue.Definition[durable.ExportPayload]{
		Type:        "export",
		MaxAttempts: 2,
		Timeout:     5 * time.Second,
		Backoff:     queue.Backoff{Base: 10 * time.Millisecond, Cap: 50 * time.Millisecond},
		Handle: func(ctx context.Context, job queue.Job[durable.ExportPayload]) error {
			return durable.RunExport(ctx, application.System(), job.Payload)
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	application, err = durable.Open(ctx, durable.Config[durable.Principal]{
		Database:            database,
		EventTransport:      transport,
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		Queue:               &golemruntime.QueueConfig{Registry: registry},
		QueueFailed:         []golemruntime.JobFailed[durable.Principal, durable.Actor]{durable.OnJobFailed(durable.ExportJob, durable.MarkExportFailed)},
		ResolvePrincipal: func(_ context.Context, principal durable.Principal) (durable.Actor, error) {
			return durable.Actor{UserID: principal.UserID, Authenticated: true}, nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	owner := golem.NewUUID([16]byte{1})
	caller, err := application.ForPrincipal(ctx, durable.Principal{UserID: owner})
	if err != nil {
		log.Fatal(err)
	}
	for _, request := range []durable.RequestExportArgs{
		{ID: "report"},
		{ID: "failing", Fail: true},
		{ID: "canceled"},
	} {
		if _, err := durable.Mutate(ctx, caller, durable.RequestExport, request); err != nil {
			log.Fatal(err)
		}
	}
	cancelled, err := durable.Mutate(ctx, caller, durable.CancelExport, durable.CancelExportArgs{ID: "canceled"})
	if err != nil {
		log.Fatal(err)
	}
	returned, _ := golem.Value(cancelled, durable.Exports.Status).Get()
	if returned != durable.StatusCanceled {
		log.Fatalf("cancelExport returned status %s", returned)
	}

	worker, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		if err := application.RunQueueWorker(worker); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("worker stopped: %v", err)
		}
	}()

	deadline := time.Now().Add(20 * time.Second)
	for {
		rows, err := caller.Exports.FindMany(ctx, durable.Exports.Where(durable.Exports.Status.Ne(durable.StatusQueued)))
		if err != nil {
			log.Fatal(err)
		}
		if len(rows) == 3 {
			break
		}
		if time.Now().After(deadline) {
			log.Fatalf("stalled with %d of 3 exports finished", len(rows))
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, id := range []string{"report", "failing", "canceled"} {
		row, err := caller.Exports.FindUnique(ctx, durable.Exports.ByID.Value(id))
		if err != nil {
			log.Fatal(err)
		}
		status, _ := golem.Value(row, durable.Exports.Status).Get()
		code, _ := golem.Value(row, durable.Exports.ErrorCode).Get()
		fmt.Printf("export=%s status=%s code=%s\n", id, status, code)
	}
}
```

```sh
golem migration new --schema ./durable --name init
golem generate --schema ./durable --app-out ./durable
golem migration apply --provider sqlite --dsn "file:durable.db"
```

It prints:

```
export=report status=succeeded code=
export=failing status=failed code=attempts_exhausted
export=canceled status=canceled code=
```

The program does not run the event publisher, so nothing reaches a client
watching `Export`. A server runs `application.RunEventPublisher(ctx)` beside
the queue worker; see [Event transports](#application-event-transports).

<a id="queue-the-whole-program"></a>

### The whole program

A minimal schema, so the application has a database to keep jobs in:

```go
// notes/schema.go
package notes

import "github.com/eleven-am/golem/go/golem"

type Actor struct {
	UserID        golem.UUID
	Authenticated bool
}

type Note struct {
	_ struct{} `golem:"model;id=example.notes.Note;table=notes;graphql=Note"`

	ID    golem.UUID `db:"id" golem:"id=example.notes.Note.ID;pk;default=uuid"`
	Title string     `db:"title" golem:"type=varchar(200)"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "notes")
	golem.Actor[Actor](schema)
	golem.Model[Note](schema)
	golem.Providers(schema, golem.SQLite)
}
```

```go
// notes/policies.go
package notes

import "github.com/eleven-am/golem/go/golem"

func (Note) DefinePolicy(rules *golem.Rules[Note], actor Actor) {
	rules.CanRead(golem.All[Note]())
	if actor.Authenticated {
		rules.CanCreate(golem.All[Note]())
	}
}
```

And the program itself:

```go
// cmd/notes/main.go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"example.com/notes/notes"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/eleven-am/golem/go/queue"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

type Principal struct{ Authenticated bool }

type Welcome struct {
	Handle string `json:"handle"`
}

type Flaky struct {
	SucceedOnAttempt int `json:"succeedOnAttempt"`
}

func main() {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: "file:notes.db"})
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	registry := queue.NewRegistry()
	welcome, err := queue.Register(registry, queue.Definition[Welcome]{
		Type:        "welcome",
		MaxAttempts: 3,
		Timeout:     5 * time.Second,
		Handle: func(_ context.Context, job queue.Job[Welcome]) error {
			fmt.Printf("welcoming %s\n", job.Payload.Handle)
			return nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	runs := 0
	flaky, err := queue.Register(registry, queue.Definition[Flaky]{
		Type:        "flaky",
		MaxAttempts: 3,
		Timeout:     5 * time.Second,
		Backoff:     queue.Backoff{Base: 10 * time.Millisecond, Cap: 50 * time.Millisecond},
		Handle: func(_ context.Context, job queue.Job[Flaky]) error {
			runs++
			if runs < job.Payload.SucceedOnAttempt {
				return errors.New("downstream not ready")
			}
			return nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	application, err := notes.Open(ctx, notes.Config[Principal]{
		Database: database,
		Queue:    &golemruntime.QueueConfig{Registry: registry},
		ResolvePrincipal: func(_ context.Context, principal Principal) (notes.Actor, error) {
			return notes.Actor{Authenticated: principal.Authenticated}, nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	welcomePending, err := welcome.New(Welcome{Handle: "ada"})
	if err != nil {
		log.Fatal(err)
	}
	welcomeJob, err := application.Enqueue(ctx, welcomePending)
	if err != nil {
		log.Fatal(err)
	}
	flakyPending, err := flaky.New(Flaky{SucceedOnAttempt: 2})
	if err != nil {
		log.Fatal(err)
	}
	flakyJob, err := application.Enqueue(ctx, flakyPending)
	if err != nil {
		log.Fatal(err)
	}

	worker, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		if err := application.RunQueueWorker(worker); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("worker stopped: %v", err)
		}
	}()

	operator := application.QueueOperator()
	deadline := time.Now().Add(20 * time.Second)
	for {
		first, err := operator.Inspect(ctx, welcomeJob)
		if err != nil {
			log.Fatal(err)
		}
		second, err := operator.Inspect(ctx, flakyJob)
		if err != nil {
			log.Fatal(err)
		}
		if first.State == queue.StateSucceeded && second.State == queue.StateSucceeded {
			fmt.Printf("welcome=%s attempt=%d\n", first.State, first.Attempt)
			fmt.Printf("flaky=%s attempt=%d\n", second.State, second.Attempt)
			return
		}
		if time.Now().After(deadline) {
			log.Fatalf("stalled welcome=%s flaky=%s", first.State, second.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

```
<!-- /chapter:queue -->

<!-- chapter:fulltext -->
<a id="fulltext"></a>

## Full-text search

Full-text indexes provide ranked keyword and phrase search without an
embedding provider. Golem owns their storage, migrations, authorization and
generated query surface on SQLite and PostgreSQL.

The complete program on this page is executed by
`TestFullTextApplicationRuns`.

<a id="fulltext-declaration"></a>

### Declaration

```go
func (Message) GolemModel() golem.ModelSpec[Message] {
	return golem.DefineModel(
		golem.FullTextIndex("content",
			golem.FullTextField(Messages.Subject, 3),
			golem.FullTextField(Messages.Participants, 2),
			golem.FullTextField(Messages.Body, 1),
			golem.TextFolding(golem.FoldDiacritics),
			golem.TextPrefix(2, 3),
		),
	)
}
```

The index name is a constant matching `[a-z][a-z0-9_-]{0,62}` and names the
generated methods (`content` becomes `TextSearchContent`). Fields must be
local `String` fields. Weights are positive constants within the `float32`
range. `TextPrefix` lists distinct token lengths in 1..32; SQLite builds FTS5
prefix indexes for them, PostgreSQL ignores them, and prefix queries work on
both providers without it. A model cannot carry a full-text index when, on
SQLite, it has a column named `rowid` or a primary or unique key column named
`docid` or `_golem_owner_rowid`, or, on PostgreSQL, a primary key column named
`document`.

SQLite uses a contentless FTS5 index; PostgreSQL uses a trigger-maintained
`tsvector` side table and GIN index. Both are updated in the same database
transaction as the model row, including cascades. PostgreSQL maintains the
index for direct SQL writes. On SQLite, inserts and updates that touch an
indexed field, a primary key column or a unique key column require a
connection opened through Golem's SQLite provider, because the maintenance
trigger calls a Golem function: Unicode folding under the default
`FoldDiacritics` mode, and NFC normalization under `FoldNone`. A plain
`sqlite3` connection does not have those functions and cannot perform those
writes. Deletes, and updates that touch none of those columns, do not require
it. A `FoldNone` index generated by go/v0.5.3 or earlier calls no Golem
function until it is upgraded as described below. Golem's SQLite provider
removes planner statistics for the FTS5 shadow tables when it opens or closes
the database, after each migration and every four hours while it is open, so
indexed writes stay flat as the index grows; running `ANALYZE` on the whole
database yourself records them again until Golem next removes them.

`FoldDiacritics` is the default. Both providers use the same canonical Unicode
decomposition and remove Unicode mark characters. Use `FoldNone` when accents
must remain distinct. In both modes, indexed text and queries are normalized to
Unicode NFC and matched case-insensitively, so text stored decomposed is found
by a composed query. On PostgreSQL, lower-casing uses the ICU collation rather
than the database `LC_CTYPE`, so `καφές` finds `Καφές` on a database created
with the `C` locale. PostgreSQL requires UTF-8 and the deterministic
`pg_catalog."und-x-icu"` collation, checked when a migration is applied, and
supports at most four distinct weights because `tsvector` has four weight
classes.

Indexes generated by go/v0.5.3 or earlier keep their original normalization,
and every query against them is folded the same way, so an existing database
opens and searches exactly as before. On that older normalization, PostgreSQL
lower-cases non-ASCII text only as the database `LC_CTYPE` allows, which makes
`C`-locale matching case-sensitive outside ASCII, and `FoldNone` does not
normalize to NFC. Regenerating the schema moves each full-text index to the
current normalization through one reviewed rewrite migration. The migration
drops the index's storage and triggers, recreates them and backfills every
existing row, inside the migration transaction. Apply it before using the
regenerated client.

The default ranking does not use corpus-wide statistics. SQLite scores each
distinct query term once per matching field and multiplies it by that field's
weight. PostgreSQL uses per-document `ts_rank_cd`. Equal scores are ordered by
the model identity, not by recency.

For a single-principal corpus that needs statistical relevance, add
`golem.TextRanking(golem.RankBM25)` to the index. SQLite then uses weighted
FTS5 `bm25`; PostgreSQL uses normalized `ts_rank_cd`. SQLite BM25 uses inverse
document frequency and average document length from the complete index. Rows
the caller cannot read are never returned, but their aggregate statistics can
change scores, ordering and top-result membership among readable rows. Do not
enable it when that aggregate influence would cross a security boundary.

Changing an existing index between the default and BM25 is a reviewed rewrite:
regenerate and apply the migration before using the changed generated client.

<a id="fulltext-querying"></a>

### Querying

Generation adds methods to caller and system clients:

```go
results, err := caller.Messages.TextSearchContent(ctx, `invoice "service fee" ref*`, 20)
for _, result := range results {
	row := result.Row()
	score := result.Score()
	_, _ = row, score
}
```

Predicates may narrow the search and are combined with `AND`. Use the additive
`TextSearchContentSelect` form when a programmatic caller only needs selected
fields instead of hydrating the complete row:

```go
results, err := caller.Messages.TextSearchContentSelect(
	ctx,
	`invoice "service fee"`,
	20,
	golem.Select(Messages.ID, Messages.Subject),
	Messages.MailboxID.Eq(mailboxID),
)
```

`TextSearchContentWith` takes one `golem.TextSearchOptions[M]` for everything
beyond the query and `take`; the zero value behaves like `TextSearchContent`:

```go
results, err := caller.Messages.TextSearchContentWith(ctx, `invoice "service fee"`, 20,
	golem.TextSearchOptions[Message]{
		AllTerms: true,
		Where:    []golem.Predicate[Message]{Messages.MailboxID.Eq(mailboxID)},
		OrderBy:  []golem.OrderTerm[Message]{Messages.ReceivedAt.Desc()},
		Select:   golem.Select(Messages.ID, Messages.Subject),
	})
```

`AllTerms` requires every parsed term, phrases and prefixes counting as one
term each, to occur in at least one indexed field, so the terms may be spread
across fields. `OrderBy` sorts the matches by ordinary model fields with the
same ordering as `FindMany`: byte-wise string comparison on every provider,
nulls first when ascending and last when descending, and a conditionally
readable order field refused unless `Where` makes it readable. `Select` is
the same projection as `TextSearchContentSelect`.

The candidate set is the rows that pass row policy, `Where` and the read
condition of every indexed field. Without `OrderBy`, matches are sorted by
score, highest first, then by primary key, and the first `take` are returned.
With `OrderBy`, all matches are sorted by the order terms, then by primary
key, and the first `take` are returned: the search matches first, orders
second and limits last, with no relevance cutoff. `Score()` is still the
relevance score either way. The GraphQL root has no `allTerms` or `orderBy`
arguments.

Caller and system transaction clients expose the same search methods; their
candidate query, ranking and hydration all use the transaction-bound executor,
so uncommitted writes are visible and rollback removes them. The generated
GraphQL root is
`textSearchMessagesByContent(query:, take:, where:)`; it returns rows without
scores.

If a custom operation owns the public search contract, suppress generated
index-search roots for the model while keeping this index and every typed Go
method:

```go
func (Message) GolemModel() golem.ModelSpec[Message] {
	return golem.DefineModel(
		golem.GraphQL[Message](golem.GraphQLWithoutIndexSearch()),
		golem.FullTextIndex("content",
			golem.FullTextField(Messages.Subject, 3),
			golem.FullTextField(Messages.Body, 1),
		),
	)
}
```

`GraphQLWithoutIndexSearch()` also suppresses semantic search roots declared on
the same model. It changes only the generated GraphQL contract and needs
regeneration, not a migration.

Terms are OR-joined unless `AllTerms` is set. Double quotes form a phrase. A trailing `*` on a term or
directly after a closing quote makes the final word a prefix of at least two
letters or digits: `ref*` or `"service fe"*`. Whitespace or the end of the
query must follow `"…"*`, so `"service fe"*charges` is rejected rather than
split into two terms. Punctuation separates words on both providers, so
`"user@example.com"*` is the phrase `user example com*` and matches the same
rows on SQLite and PostgreSQL. Golem parses this syntax and
quotes every lexeme; raw FTS5 or `tsquery` syntax is never accepted. Queries
are limited to 32 terms and 10,000 bytes of valid UTF-8, and results to 1,000
rows.

<a id="fulltext-authorization-and-consistency"></a>

### Authorization and consistency

Golem applies the caller's row policy, supplied predicate and read condition
for every indexed field before a row can occupy a ranked result. A masked
field therefore cannot be searched as an oracle. Ranked rows are hydrated
through the ordinary read path, so field masks and relation selection match a
normal `FindMany`.

Full-text maintenance is synchronous and transactional. A successful write is
immediately searchable; rollback restores both the row and the index. Adding
an index through a reviewed migration backfills existing rows before the
migration commits.

Scores are only for ordering within one query. They are not comparable across
providers or indexes. Under the default ranking, hidden rows cannot appear,
alter visible ordering or influence a returned score. The opt-in SQLite BM25
exception is described above.

**Hidden rows do affect how long a search takes.** Both providers match the
query against the whole index before narrowing to the rows the caller may
read, so a query that matches many hidden rows is measurably slower than one
that matches none, even though the results are identical. Measured with 20
readable rows: SQLite answered in 0.22 ms when 50,000 hidden rows did not
match and 37 ms when they did; PostgreSQL in 1.5 ms and 9.6 ms with 30,000.
Treat the response time of a search as revealing roughly how common a term is
across the whole index, including rows the caller cannot see.

Full-text search does not provide semantic similarity, stemming, synonyms,
snippets or highlighting. Use a semantic index for meaning-based search and
`Similar…`; use a full-text index for exact terms, phrases and prefixes.

<a id="fulltext-complete-program"></a>

### Complete program

```go
// notes/schema.go
package notes

import "github.com/eleven-am/golem/go/golem"

type Actor struct {
	Authenticated bool
}

type Note struct {
	_ struct{} `golem:"model;id=example.notes.Note;table=notes;graphql=Note"`

	ID    golem.UUID `db:"id" golem:"id=example.notes.Note.ID;pk;default=uuid"`
	Title string     `db:"title" golem:"type=varchar(200)"`
	Body  string     `db:"body" golem:"type=varchar(2000)"`
}

func (Note) GolemModel() golem.ModelSpec[Note] {
	return golem.DefineModel(
		golem.FullTextIndex("content",
			golem.FullTextField(Notes.Title, 3),
			golem.FullTextField(Notes.Body, 1),
			golem.TextFolding(golem.FoldDiacritics),
			golem.TextPrefix(2, 3),
		),
	)
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "notes")
	golem.Actor[Actor](schema)
	golem.Model[Note](schema)
	golem.Providers(schema, golem.SQLite)
}
```

```go
// notes/policies.go
package notes

import "github.com/eleven-am/golem/go/golem"

func (Note) DefinePolicy(rules *golem.Rules[Note], actor Actor) {
	rules.CanRead(golem.All[Note]())
	if actor.Authenticated {
		rules.CanCreate(golem.All[Note]())
	}
}
```

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

type Principal struct{ Authenticated bool }

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
			return notes.Actor{Authenticated: principal.Authenticated}, nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	caller, err := application.ForPrincipal(ctx, Principal{Authenticated: true})
	if err != nil {
		log.Fatal(err)
	}
	for _, note := range []struct{ title, body string }{
		{"Renée's invoice", "service fee for September"},
		{"Meeting notes", "roadmap and staffing"},
	} {
		if _, err := caller.Notes.Create(ctx, notes.Notes.Create(
			notes.Notes.Title.Create(note.title),
			notes.Notes.Body.Create(note.body),
		), notes.Notes.Select(notes.Notes.ID)); err != nil {
			log.Fatal(err)
		}
	}
	results, err := caller.Notes.TextSearchContentWith(ctx, `renee "service fee"`, 10, golem.TextSearchOptions[notes.Note]{
		AllTerms: true,
		OrderBy:  []golem.OrderTerm[notes.Note]{notes.Notes.Title.Asc()},
	})
	if err != nil {
		log.Fatal(err)
	}
	title, _ := golem.Value(results[0].Row(), notes.Notes.Title).Get()
	fmt.Printf("search returned %d: %s\n", len(results), title)
}
```

```
search returned 1: Renée's invoice
```
<!-- /chapter:fulltext -->

<!-- chapter:semantic -->
<a id="semantic"></a>

## Semantic search

Declaring a semantic index gives a model search by text, search by a prepared
vector, rows like this row, and authorized stored-vector reads. None needs a
resolver.

The program on this page is executed by `TestSemanticApplicationRuns`.

<a id="semantic-declaring-one"></a>

### Declaring one

An embedding space names a vector width. A semantic index names the columns
whose text is embedded into that space.

```go
func (Note) GolemModel() golem.ModelSpec[Note] {
	return golem.DefineModel(golem.SemanticIndex("related", "content", Notes.Title, Notes.Body))
}

func DefineSchema(schema *golem.Schema) {
	...
	golem.EmbeddingSpace(schema, "content", 3)
}
```

Generation emits four methods per index, on the caller, system and transaction
clients:

```go
caller.Notes.SearchRelated(ctx, "flour water", 3)
caller.Notes.SimilarRelated(ctx, notes.Notes.ByID.Value(id), 2)
caller.Notes.SearchByVectorRelated(ctx, queryVector, 3)
caller.Notes.VectorsRelated(ctx, notes.Notes.ByID.Value(id))
```

The three search methods accept optional predicates, take a `take` between 1
and 1000, and return `SemanticResult` values carrying `Row()`, `Distance()` and
`Similarity()`. `VectorsRelated` accepts unique selectors, as described below.

GraphQL normally receives the corresponding search and similarity roots. When
a custom operation owns the public search semantics, keep the semantic index
and typed Go methods but suppress all generated index-search roots for the
model:

```go
func (Note) GolemModel() golem.ModelSpec[Note] {
	return golem.DefineModel(
		golem.GraphQL[Note](golem.GraphQLWithoutIndexSearch()),
		golem.SemanticIndex("related", "content", Notes.Title, Notes.Body),
	)
}
```

`GraphQLWithoutIndexSearch()` also suppresses full-text search roots declared on
the same model. It changes only the generated GraphQL contract and needs
regeneration, not a migration.

<a id="semantic-supplying-an-embedding-provider"></a>

### Supplying an embedding provider

Golem never calls a model vendor for you. You implement two methods:

```go
type Provider interface {
	Specification() Specification
	Embed(ctx context.Context, inputs []Input) ([]Vector, error)
}
```

and register one per space:

```go
registry, err := embedding.NewRegistry(map[string]embedding.Provider{"content": provider})
```

Each input carries a `Purpose()`: `embedding.PurposeDocument` for indexed
content and `embedding.PurposeQuery` for search text. Providers whose vendor
API distinguishes `search_document` from `search_query` (or equivalent) must
map that purpose instead of guessing from the key or text. `NewInput` remains
the compatibility spelling for a document input. The document text is the
indexed values separated by newlines; Golem's binary-safe change-detection
framing is never sent to the provider.

`Specification` declares provider, model, revision, dimensions and maximum
batch. The revision participates in a fingerprint, so changing it marks every
row stale rather than silently mixing vectors from two different models in one
index.

<a id="semantic-embedding-happens-on-the-queue"></a>

### Embedding happens on the queue

A semantic index requires `Queue` to be configured. Writes mark rows stale;
a worker embeds them.

```go
application, err := notes.Open(ctx, notes.Config[Principal]{
	Database:   database,
	Embeddings: registry,
	Queue:      &golemruntime.QueueConfig{Registry: queue.NewRegistry()},
	...
})
go application.RunQueueWorker(ctx)
```

Rows are therefore searchable *shortly* after they are written, not
immediately. A program that creates a row and searches for it in the next
statement will not find it. The example below polls.

<a id="semantic-search-authorizes-before-it-ranks"></a>

### Search authorizes before it ranks

The candidate set is the rows the caller may read, and ranking happens within
it. This is not an optimisation: ranking first and filtering afterwards would
turn vector distance into an oracle over rows the caller cannot see — their
existence, and roughly what they are about, leaking through their effect on
the neighbourhood.

Predicates you pass narrow that set further; they cannot widen it.

<a id="semantic-searching-with-a-prepared-vector"></a>

### Searching with a prepared vector

`SearchByVectorRelated` accepts an `embedding.QueryVector`. Build it with
`embedding.NewQueryVector(specification, vector)`, where `specification` is the
registered embedding provider's `Specification` and `vector` is an immutable
`embedding.Vector`. The full specification must match the selected index:
provider, model, revision, dimensions and batch contract. A matching width alone
is insufficient. The application is responsible for this provenance claim when
it builds a derived vector, such as the mean of previously read vectors.

The constructor rejects missing specifications, dimension mismatches, nonfinite
components and zero cosine norm. Ranking also applies the existing exact-cosine
norm bounds. It authorizes rows and every indexed source field through the same
candidate planner as text search, then hydrates the ranked rows through ordinary
authorized reads. Row readability alone does not authorize hidden indexed text.
Wholly denied fields can refuse the request; conditionally unreadable indexed
fields exclude those rows from candidacy.

Prepared-vector ranking makes no embedding-provider call, including on caller
and system transaction clients. Prepare textual queries before opening the
transaction: construct an `embedding.NewQueryInput`, call your provider, check
its result with `embedding.ValidateResult`, and wrap the resulting vector with
`embedding.NewQueryVector`. Pass that value to the transaction client's
`SearchByVectorRelated`. Text search refuses to call a provider while a
single-connection transaction is open, since the provider could need the same
connection. Ordinary text search outside a transaction is unchanged.

These Go methods require regenerated clients. Vector input is not exposed as a
generated GraphQL search root.

<a id="semantic-reading-stored-vectors"></a>

### Reading stored vectors

`VectorsRelated(ctx, selectors...)` returns a
`[]golem.ReadValue[embedding.QueryVector]` with one cell per selector, in input
order. Duplicate selectors retain their own cells. Use `Get()` to check whether
a vector is available. Missing rows, unreadable rows, denied
indexed fields, pending vectors, and obsolete embedding-space revisions expose
no vector. These unavailable cases have the same null cell; they do not disclose
which check failed. Normal model/selector authorization refusals, invalid
selectors and ambiguous unique lookups remain errors.

The caller must be allowed to read the source row, its identity and every
indexed source field. Other fields are not selected. An internal field is
hidden from GraphQL, but still needs its own Go read permission before its
derived vector can be disclosed. There is no separate permission that bypasses
these checks.

The source count uses the existing semantic result ceiling of 1,000. Normal
model/read `take` limits can lower it. Retrieval uses bounded SQL batches and
can split them further to fit parameter or statement-size limits. A policy that
cannot fit even one source still fails with the applicable capacity error.
Each statement joins source selection, authorization, readiness and the current
embedding-space fingerprint in one database snapshot. Use a transaction client
when every batch must share that transaction's snapshot.

Vector reads make no embedding-provider call and do not wait for indexing.
Present cells carry the registered specification and can be passed directly to
`SearchByVectorRelated`, or used to prepare a derived vector in that same space.
These methods are Go APIs; no generated GraphQL vector-read root is added.

<a id="semantic-similarity-requires-reading-the-source"></a>

### Similarity requires reading the source

`SimilarRelated` resolves its source through an ordinary authorized read
first. A caller who cannot read row 47 cannot ask what row 47 is like.

Without that rule the parameter itself becomes the oracle: answering for a
hidden row discloses that it exists, that it is indexed, and — through its
neighbours — approximately what it contains. Unlike a query string, a key
enumerates.

So an absent source, an unreadable source, and a source whose identity is
masked all produce the identical `CodeNotFound` error. Never an empty list,
which would be a second existence probe with different semantics.

**The source is excluded from its own results.** "Rows like this row" is
irreflexive, and the exclusion happens before ranking, so no predicate can
reintroduce it. `take` means "up to `take` rows other than the source".

**A similarity request makes no embedding-provider call.** The query vector is
the source row's stored vector; composing its field text and embedding that
would produce a vector under a different encoding than everything it is
compared against, and pay a round trip to do it.

<a id="semantic-freshness"></a>

### Freshness

Golem marks a row stale on every write that touches it, whatever the write
changed. The drain recomputes the content hash of the embedded columns and
re-embeds only a row whose hash differs; an unchanged row is flipped back to
ready without a provider call.

Inside an open transaction, semantic reads also exclude rows in that
transaction's pending mark buffer. Ranking, similarity and vector retrieval
therefore cannot use the previous ready vector after the transaction changes
its source row. Durable marks and drain jobs still flush with the transaction's
other commit work. A rollback leaves the previous ready vector available.

For a document derived from related credits or companies, maintain an explicit
internal text column on the indexed model. The application command that changes
those related rows must recompute that column from its source facts and update
it in the same transaction. Golem then marks the indexed owner stale atomically;
the drain embeds its new document after commit. Updating only a related row does
not automatically recompute or invalidate this application-owned document.
Granting read access to the document column authorizes its composed content.
Reflect any restrictions on the contributing fields in that column's policy,
or keep differently authorized content in separate documents/indexes. Golem
cannot infer the field permissions of application-owned text composition.

**A stale row does not rank.** It is excluded until re-embedded, and a
similarity request whose source is stale fails with the same "unavailable"
error as a source that was never embedded. Ranking on a vector known to be out
of date would answer confidently with a stale neighbourhood; failing closed
makes the staleness visible.

**No full reconcile runs by default.** `QueueConfig.SemanticReconcileInterval` is zero
unless you set it, so golem repairs drift introduced through its own write
path and nothing else. Rows changed by raw SQL, a restore, or another writer
are never noticed. Set an interval, between one minute and 24 hours, if
anything writes to your database that is not golem.

Startup checks the private state table for vectors from another embedding-space
fingerprint. Only when it finds one does it mark those rows pending and enqueue
a deduplicated drain; an unchanged startup remains read-only. This is distinct
from the optional full owner-table reconcile.

If you add a semantic index to a model whose rows already exist, there are no
state rows for startup's fingerprint check to find. Run
`RefreshSemanticIndexes` once after deploying that schema change, or configure
`SemanticReconcileInterval`, to create the initial vectors.

<a id="semantic-when-the-provider-fails"></a>

### When the provider fails

**Only a refusal the provider marks as invalid input quarantines a row.** A
batch refused that way is retried one row at a time, so the row it cannot
embed is quarantined and its neighbours are stored. A document golem itself
cannot frame as an input, over 16 MiB or not valid UTF-8, is quarantined the
same way without a provider call.

**Every other failure leaves the rows pending and the drain retries later
without spending an attempt.** That covers an outage, a rate limit, a malformed
response and an error golem cannot classify; the last two are the unclassified
refusals the strike rule below bounds. The retry waits for the time the job has
already waited, between five seconds and five minutes, so an outage of any
length never dead-letters the drain.

**One row the provider keeps refusing does not hold up the rest.** A batch
refused for an unclassified reason is retried one row at a time, so the rows
around the culprit are stored rather than left pending with it. This holds
wherever the culprit falls: the rows after it in its own batch, and every later
batch in the page, are still attempted. Isolation spends at most eight
single-row calls per pass; a refused batch larger than that is finished by
later passes.

**A row the provider keeps refusing without saying why is quarantined after
five strikes.** The count lives in `ambiguous_strikes` on the shadow state
table and resets to zero whenever the row embeds successfully or is marked
stale again. At the fifth strike the row's `status` becomes `failed` and its
`error_code` becomes `EMBEDDING_REFUSED_UNCLASSIFIED`. To bring it back, change
the indexed source row: that write marks it stale and clears the strike count,
and the next pass tries again. A reconcile retries it as well, because its
stored hash no longer matches its content.

**A strike is only ever charged in a pass where a provider call succeeded.**
An outage cannot spend strikes a healthy pass earned earlier. When nothing else
in the pass embedded, golem re-embeds documents it already stored to prove the
provider is answering, and charges nothing if they all fail. It tries up to
three distinct ones and never the same one twice in a pass; each pass draws
further along the index, and a candidate that failed is not drawn first again,
so a document the provider has stopped accepting cannot pin the probe. Those
re-embeds are discarded, and the rows they borrow are left exactly as they
were. A provider that reports `EMBEDDING_UNAVAILABLE` never strikes at all.

**The strike column requires the migration.** Regenerate and apply it before
the bound can take effect; until then an unclassified refusal is retried
forever, as before. That is the same regeneration the queue's operator-history
indexes need.

**Known limit: an index whose only document is the one the provider refuses
cannot charge a strike.** Because a strike is only charged when a provider call
succeeded in the same pass, the pass needs either another row that embedded or
a stored document the liveness probe can borrow. An index whose only document
is the one the provider refuses, with nothing else ever stored, has neither, so
it never reaches the bound and is retried forever.

**An outage costs at most five provider calls per pass once the index holds
documents, and at most ten while it is still empty.** Two batch calls failing
with nothing stored between them stop the pass for the rest of its page, and
proving liveness costs up to three re-embeds of documents already stored: two
plus three is the five. Documents here means rows currently `ready`, or rows
in the page whose content proved unchanged; a pass in which every stored row
is itself marked stale has nothing to borrow and pays the empty-index cost.

The ten is the empty index. Finding which document in a refused batch is at
fault normally waits until the provider has answered, so it costs nothing while
the provider is down — but a pass with nothing stored has no document to probe
with, and there isolating the batch is the only way to learn anything, so it
runs anyway, bounded at eight calls. Two plus eight is the ten. That regime
ends as soon as one document embeds, because from then on the index has
something to probe.

Neither count grows with the number of batches in the page. Each
deferred pass is observed as a `semantic.refresh` retry whose aggregate count
is the number of rows it left pending.

<a id="semantic-ranking-is-exact"></a>

### Ranking is exact

Both providers rank exactly. PostgreSQL creates no approximate vector index,
neither HNSW nor IVFFlat, and the rank statement orders by an expression the
planner cannot serve from one, because an approximate scan returns a full page
of plausible neighbours while silently omitting nearer ones — and a page that
is confidently wrong is worse than a slow one for a feature whose whole purpose
is "these are the closest".

SQLite stores current vectors in a strict, dimension-checked BLOB table and
drives ranking from the authorized candidate query before calculating cosine
distance. The previous `vec0` virtual-table shape forced the opposite join
order and made a selective policy scan the entire vector table. Distance is
SQLite's bundled `vec1` extension (`vec1_cos_distance`), computed in single
precision. Beyond a Euclidean norm of `[1e-9, 1e9]` the single-precision norm
product underflows or overflows and the distance stops being exact, so every
provider refuses to store or query a vector outside that range; a record
therefore embeds or fails identically on SQLite and PostgreSQL. Distances
within `1e-6` below zero are single-precision rounding of an identical
direction and rank as `0`.

A database that still holds sqlite-vec `vec0` vector tables (semantic state
below 3) must be opened and migrated with go/v0.7.1, the last release that
ships sqlite-vec, before this release can use it; this release refuses to
open or migrate such a database, and refuses an application whose current
schema is still below state 3. Reviewed migration history is unaffected: a
fresh database bootstraps through history entries that created state 1 or 2
`vec0` tables by executing an empty `record_key`/`embedding` placeholder table
in their place, which the reviewed upgrade to state 3 then replaces with the
exact vector table. The reviewed SQL files keep their original `vec0` text.
PostgreSQL
keeps `pgvector` storage but uses an exact distance expression rather than
HNSW, because a full approximate page does not reveal which true neighbours it
missed.

<a id="semantic-cost"></a>

### Cost

No new tables beyond the index's own storage. A search costs one provider call,
one authorized ranking statement, and the authorized row read that hydrates the
ranked identities; a similarity request costs one authorized source read, one
source-vector read, one ranking statement and that same hydration read, with
no provider call. Exact ranking work grows
with the authorized candidate set. Predicates therefore improve SQLite cost as
well as narrowing results; large public corpora trade latency for the exactness
guarantee on both providers.

<a id="semantic-the-whole-program"></a>

### The whole program

```go
// notes/schema.go
package notes

import "github.com/eleven-am/golem/go/golem"

type Actor struct {
	Authenticated bool
}

type Note struct {
	_ struct{} `golem:"model;id=example.notes.Note;table=notes;graphql=Note"`

	ID    golem.UUID `db:"id" golem:"id=example.notes.Note.ID;pk;default=uuid"`
	Title string     `db:"title" golem:"type=varchar(200)"`
	Body  string     `db:"body" golem:"type=varchar(2000)"`
}

func (Note) GolemModel() golem.ModelSpec[Note] {
	return golem.DefineModel(golem.SemanticIndex("related", "content", Notes.Title, Notes.Body))
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "notes")
	golem.Actor[Actor](schema)
	golem.Model[Note](schema)
	golem.Providers(schema, golem.SQLite)
	golem.EmbeddingSpace(schema, "content", 3)
}
```

```go
// notes/policies.go
package notes

import "github.com/eleven-am/golem/go/golem"

func (Note) DefinePolicy(rules *golem.Rules[Note], actor Actor) {
	rules.CanRead(golem.All[Note]())
	if actor.Authenticated {
		rules.CanCreate(golem.All[Note]())
		rules.CanUpdate(golem.All[Note]())
	}
}
```

```go
// cmd/notes/main.go
package main

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"time"

	"example.com/notes/notes"
	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/provider/sqlite"
	"github.com/eleven-am/golem/go/queue"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

type Principal struct{ Authenticated bool }

type wordProvider struct{ specification embedding.Specification }

func newWordProvider() (wordProvider, error) {
	specification, err := embedding.NewSpecification("example", "word-count", "v1", 3, 32)
	if err != nil {
		return wordProvider{}, err
	}
	return wordProvider{specification: specification}, nil
}

func (provider wordProvider) Specification() embedding.Specification { return provider.specification }

func (provider wordProvider) Embed(_ context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
	vectors := make([]embedding.Vector, 0, len(inputs))
	for _, input := range inputs {
		digest := fnv.New32a()
		_, _ = digest.Write([]byte(input.Text()))
		sum := digest.Sum32()
		vector, err := embedding.NewVector([]float32{
			float32(sum%97) / 97,
			float32((sum/97)%89) / 89,
			float32((sum/8633)%83) / 83,
		})
		if err != nil {
			return nil, err
		}
		vectors = append(vectors, vector)
	}
	return vectors, nil
}

func main() {
	ctx := context.Background()
	database, err := sqlite.Open(ctx, sqlite.Config{DataSourceName: "file:notes.db"})
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()

	provider, err := newWordProvider()
	if err != nil {
		log.Fatal(err)
	}
	registry, err := embedding.NewRegistry(map[string]embedding.Provider{"content": provider})
	if err != nil {
		log.Fatal(err)
	}

	application, err := notes.Open(ctx, notes.Config[Principal]{
		Database:   database,
		Embeddings: registry,
		Queue:      &golemruntime.QueueConfig{Registry: queue.NewRegistry()},
		ResolvePrincipal: func(_ context.Context, principal Principal) (notes.Actor, error) {
			return notes.Actor{Authenticated: principal.Authenticated}, nil
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	caller, err := application.ForPrincipal(ctx, Principal{Authenticated: true})
	if err != nil {
		log.Fatal(err)
	}
	var sourdoughID golem.UUID
	for _, note := range []struct{ title, body string }{
		{"sourdough starter", "flour water and patience"},
		{"rye bread", "flour water salt and time"},
		{"engine tuning", "carburettor timing and spark"},
	} {
		created, err := caller.Notes.Create(ctx, notes.Notes.Create(
			notes.Notes.Title.Create(note.title),
			notes.Notes.Body.Create(note.body),
		), notes.Notes.Select(notes.Notes.ID))
		if err != nil {
			log.Fatal(err)
		}
		if note.title == "sourdough starter" {
			id, ok := golem.Value(created, notes.Notes.ID).Get()
			if !ok {
				log.Fatal("create returned no identity")
			}
			sourdoughID = id
		}
	}

	worker, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		if err := application.RunQueueWorker(worker); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("worker stopped: %v", err)
		}
	}()

	deadline := time.Now().Add(30 * time.Second)
	for {
		results, err := caller.Notes.SearchRelated(ctx, "flour water", 3)
		if err == nil && len(results) == 3 {
			fmt.Printf("search returned %d\n", len(results))
			similar, err := caller.Notes.SimilarRelated(ctx, notes.Notes.ByID.Value(sourdoughID), 2)
			if err != nil {
				log.Fatal(err)
			}
			fmt.Printf("similar returned %d\n", len(similar))
			for _, result := range similar {
				title, _ := golem.Value(result.Row(), notes.Notes.Title).Get()
				if title == "sourdough starter" {
					log.Fatal("similarity returned its own source")
				}
			}
			return
		}
		if time.Now().After(deadline) {
			log.Fatalf("embeddings never became searchable: err=%v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
```

```
search returned 3
similar returned 2
```

<a id="semantic-local-exact-ranking-characterization-unreleased"></a>

<a id="semantic-local-exact-ranking-characterization-gov090"></a>

### Local exact-ranking characterization (go/v0.9.0)

The [generated public-API benchmark](../go/internal/semantic/runtime/rank_public_benchmark_test.go)
measured 3,864 rows with 1,024-dimensional vectors on 2026-11-15. The fixture's
generated-code identity check passed in the same run. Environment: Apple M3 Pro,
18 GiB RAM, macOS/arm64, Go 1.26.10, `GOMAXPROCS=1`, low CPU priority, one SQLite
connection, DELETE journal, and the network-filesystem durability policy on
local temporary storage. The embedding provider is deterministic; no remote
embedding request is included.

All durations below are milliseconds. Each case retains 25 warm-query samples;
percentiles use nearest rank, so p99 equals the observed maximum at this sample
size. The first observed query is recorded separately. Neither the OS nor
SQLite caches were flushed.

| Operation | First observed | Warm p50 | Warm p95 | Warm p99 / max |
|---|---:|---:|---:|---:|
| Search, unfiltered | 30.364 | 23.120 | 38.996 | 47.275 |
| Search, 0.1% selective predicate | 0.806 | 0.543 | 0.991 | 1.130 |
| Similar, unfiltered | 30.069 | 27.969 | 47.125 | 51.133 |
| Similar, 0.1% selective predicate | 0.578 | 0.633 | 0.896 | 0.942 |

This is a synthetic local measurement. Cold-cache latency, NAS latency, the
application baseline, and the combined scan/job/playback workload remain
unmeasured; these results establish no production latency guarantee.
<!-- /chapter:semantic -->

<!-- chapter:render -->
<a id="render"></a>

## Serving a frontend

A single-page application returns the same HTML shell for every route, so a
crawler asking for `/n/42` sees the shell's metadata rather than the note's.
`render` serves the shell and rewrites its metadata per route, server-side,
before the bytes leave.

The program on this page is executed by `TestRenderApplicationRuns`.

<a id="render-a-renderer"></a>

### A renderer

```go
renderer, err := render.New(render.Config{
	Dir:     "public",
	Index:   "index.html",
	BaseURL: "https://notes.example",
	Defaults: render.Meta{
		Title:       "Notes",
		Description: "A place for notes",
	},
	Routes: []render.Route{{
		Patterns: []string{"/n/{id}"},
		Resolve: func(ctx context.Context, link render.Link) (*render.Meta, error) {
			return &render.Meta{Title: "Note " + link.Values["id"]}, nil
		},
	}},
})

http.ListenAndServe(":8080", renderer.Handler())
```

| Field | Meaning |
|---|---|
| `Dir` / `Files` | where the built assets live; set exactly one |
| `Index` | the shell, relative to the bundle; `index.html` when unset |
| `Defaults` | metadata every response starts from; a resolver's non-empty fields override it |
| `BaseURL` | absolute origin that `og:url` and image URLs are resolved against |
| `Reserved` | prefixes answered with 404 instead of the shell; `/api` and `/graphql` when nil |
| `Routes` | patterns bound to resolvers |
| `ReportResolverError` | where resolver errors and panics are reported |

Patterns are `http.ServeMux` patterns, so `/n/{id}` puts `id` in
`link.Values`.

<a id="render-resolvers-fail-safe"></a>

### Resolvers fail safe

A resolver returns metadata for one matched route:

```go
func(ctx context.Context, link render.Link) (*render.Meta, error)
```

Returning `nil`, returning an error, **or panicking** renders the configured
defaults. A resolver is code that runs on an unauthenticated request from a
crawler; a bug in it degrades the preview rather than the page. Errors reach
`ReportResolverError` so a silent degradation is still observable; a panic
arrives there as an error carrying the recovered value.

`Link` carries the path, the pattern wildcards, the query and the absolute
URL — and deliberately no headers. Nothing in a crawler's headers is worth
trusting, and a resolver that cannot read them cannot accidentally
authenticate one.

<a id="render-what-it-serves"></a>

### What it serves

```
/n/42      title="Note 42"  cache="no-cache"
/n/missing title="Notes"    cache="no-cache"
```

`/n/42` gets its resolver's metadata. `/n/missing` returns nil, so the
defaults are used — the page still renders.

Only `GET` and `HEAD` are served. A path that names a file in the bundle
gets that file. Anything else that looks like an asset — by extension, under
`/assets/`, or by a subresource `Sec-Fetch-Dest` — or whose `Accept` excludes
HTML gets a 404 rather than the shell, so a missing script is not answered
with a page.

<a id="render-caching"></a>

### Caching

Every file and shell response carries `Cache-Control: no-cache`; a 404
carries no cache directive. Assets are not fingerprinted
by the renderer, and it cannot tell a content-hashed filename from one that
merely looks like it, so it does not guess. A wrong guess serves a stale asset
for as long as the directive says — put a CDN in front and set the policy where
the filenames are known.

<a id="render-reserved-prefixes"></a>

### Reserved prefixes

```go
Reserved: []string{"/api", "/graphql"}
```

The renderer answers a reserved path with 404 itself, so a mistyped API path
that reaches it gets a 404 rather than an HTML page a client will try to parse
as JSON. `/api` and `/graphql` are reserved when `Reserved` is nil; an empty
slice reserves nothing. A route pattern under a reserved prefix is refused by
`New` with `RENDER_ROUTE_INVALID`.

<a id="render-it-is-independent"></a>

### It is independent

`render` needs no schema, no database and no generated application. It is an
`http.Handler`. Mount it beside your API, or run it alone.

<a id="render-the-whole-program"></a>

### The whole program

```go
// cmd/site/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/eleven-am/golem/go/render"
)

func main() {
	renderer, err := render.New(render.Config{
		Dir:     "public",
		Index:   "index.html",
		BaseURL: "https://notes.example",
		Defaults: render.Meta{
			Title:       "Notes",
			Description: "A place for notes",
		},
		Routes: []render.Route{{
			Patterns: []string{"/n/{id}"},
			Resolve: func(_ context.Context, link render.Link) (*render.Meta, error) {
				id := link.Values["id"]
				if id == "missing" {
					return nil, nil
				}
				return &render.Meta{
					Title:       "Note " + id,
					Description: "shared note " + id,
				}, nil
			},
		}},
	})
	if err != nil {
		log.Fatal(err)
	}

	server := httptest.NewServer(renderer.Handler())
	defer server.Close()

	for _, path := range []string{"/n/42", "/n/missing"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			log.Fatal(err)
		}
		body := make([]byte, 4096)
		read, _ := response.Body.Read(body)
		response.Body.Close()
		page := string(body[:read])
		title := "Notes"
		if strings.Contains(page, "Note 42") {
			title = "Note 42"
		}
		fmt.Printf("%s title=%q cache=%q\n", path, title, response.Header.Get("Cache-Control"))
	}
}
```

with a shell at `public/index.html`:

```html
<!doctype html>
<html>
<head><title>Notes</title></head>
<body><div id="root"></div></body>
</html>
```
<!-- /chapter:render -->

<!-- chapter:sqlite -->
<a id="sqlite"></a>

## SQLite durability and storage

Golem continues to use and enforce write-ahead logging by default. Local-disk
deployments do not need to change their configuration.

SQLite databases deliberately placed on a network filesystem can opt into the
rollback-journal profile:

```go
database, err := sqlite.OpenNetworkFilesystem(ctx, sqlite.Config{
    DataSourceName: "file:/data/application.sqlite",
})
```

This profile enforces `journal_mode=DELETE`, `synchronous=FULL`, foreign keys,
a 5-second busy timeout, immediate transactions, and a single open/idle
connection. It is explicit rather than filesystem-detected. Named in-memory
databases and caller overrides of provider-owned pragmas remain rejected.

<a id="sqlite-write-ahead-logging-on-a-network-filesystem"></a>

### Write-ahead logging on a network filesystem

Ordinary WAL keeps its index in a `<database>-shm` file that every connection
memory-maps and grows. Before go/v0.7.3 the driver grew that file with
`fallocate(2)`, which Linux NFS 4.1 mounts reject with `EOPNOTSUPP`, so
`sqlite.Open` failed there; the current driver falls back to growing the file
another way, so `sqlite.Open` works on such a mount when a single node opens
the database. SQLite's own guidance is that shared-memory WAL is not safe when
processes on different hosts open the same file over a network filesystem.
`sqlite.OpenNetworkFilesystem` avoids WAL altogether. SQLite can also run WAL
without the `-shm` file when the connection uses exclusive locking: the WAL
index then lives in the process's heap memory. `sqlite.OpenNetworkFilesystemWAL`
opts into exactly that:

```go
database, err := sqlite.OpenNetworkFilesystemWAL(ctx, sqlite.Config{
    DataSourceName: "file:/data/application.sqlite",
})
```

Every connection the provider creates sets `locking_mode=EXCLUSIVE` as its
first statement, before it can read the database or enter WAL, and the profile
then enforces and verifies `journal_mode=WAL`, `locking_mode=EXCLUSIVE`,
`synchronous=FULL`, foreign keys, a 5-second busy timeout, immediate
transactions, and a single open/idle connection that never expires. Caller
overrides of `locking_mode` are rejected for every profile. A database that is
not file-backed is rejected. A non-empty `<database>-shm` next to the database
is refused, because another process may be using it in ordinary WAL mode; an
empty leftover is tolerated.

The trade-off is exclusive access. While the application holds the handle, no
other process can read or write the database: not another replica, not the
`sqlite3` command-line tool, and not a backup job. They fail with a busy error
until the handle is closed. Choose this profile over the rollback-journal one
when one process owns the database and you want WAL's cheaper commits, which
append to the log instead of rewriting database pages through a rollback
journal. The pool holds a single connection, so the profile adds no read
concurrency inside the process. Choose
`sqlite.OpenNetworkFilesystem` when anything else must open the file while the
application runs.

A rollback-journal database opened with this profile is converted to WAL. The
reverse needs no manual step. On a filesystem that rejects the `-shm` file, a
connection without exclusive locking cannot read a WAL database at all, so
`sqlite.OpenNetworkFilesystem` converts it before the normal open. When the
database file's header declares WAL, the provider opens one dedicated
connection with `locking_mode=EXCLUSIVE` set before its first database access,
verifies exclusive locking, runs `PRAGMA wal_checkpoint(TRUNCATE)` and
`PRAGMA journal_mode=DELETE`, requires `PRAGMA integrity_check` to report `ok`,
and closes the connection before opening the rollback-journal pool. The same
conversion recovers a database left in WAL by `sqlite.Open` or by a crash,
including frames that were only in the `-wal` file; SQLite removes that file
when the conversion completes. The conversion fails closed and changes nothing
when a non-empty `<database>-shm` exists, because another process may be using
the database in ordinary WAL mode, and when another process holds the database
so the exclusive lock cannot be obtained. An empty `-shm` leftover is
tolerated. The provider never deletes or truncates a sidecar file itself. A
database already in rollback-journal mode opens exactly as before, and the WAL
profiles never convert. The integrity check reads the whole database, so the
open that converts a large database can take minutes on a network filesystem;
allow for it in startup probes. When several processes open the same
WAL-declared database at once, one converts and the others fail their open
after the busy timeout; restart them once the first is up.

<a id="sqlite-recovering-from-a-stale--shm"></a>

#### Recovering from a stale `-shm`

A non-empty `<database>-shm` blocks both network profiles: the WAL profile
refuses to open the database and the rollback profile refuses to convert it.
Golem never deletes that file automatically, because a live process may still
be using it. If the file is stale, for example after a crash of a process that
used ordinary WAL, recover it by hand:

1. Stop every process that uses the database.
2. Confirm that none still holds it: open the database with the `sqlite3`
   command-line tool and check that `BEGIN IMMEDIATE; ROLLBACK;` does not
   report a busy or locked error.
3. Only then remove the `-shm` file, and open the database with the profile
   you need.

<a id="sqlite-backups-and-replicas"></a>

### Backups and replicas

Before copying the database file, stop all application readers and writers,
close the verified handle, and call `sqlite.CheckpointForBackup`. For the
default WAL profile this performs and verifies a truncating checkpoint. For the
network-filesystem profile it verifies rollback-journal mode and returns: a
closed, committed rollback-journal database has no WAL frames to checkpoint.
For the network-filesystem WAL profile it performs and verifies the truncating
checkpoint through a connection that also uses exclusive locking.
Do not copy a live database or copy a leftover journal independently.

The network profile reduces concurrency to make one-process access predictable;
it does not make SQLite a distributed database. Do not run multiple application
replicas against the same database file unless the filesystem and operational
model provide SQLite's required locking and consistency semantics.
<!-- /chapter:sqlite -->

<!-- chapter:react-server -->
<a id="react-server"></a>

## Preparing a Go server for React

You have a golem server and want a React front end that stays live: a list
that gains, changes and loses rows as other people write, with no polling and
no cache code of your own.

This page walks through it with the social example
(`go/examples/social`): `Post` and `Comment` have events, `User` does not.
Every name below exists in that example's generated schema.

<a id="react-server-1-give-the-model-events"></a>

### 1. Give the model events

A model is live when it declares `golem.Subscriptions`:

```go
func (Post) GolemModel() golem.ModelSpec[Post] {
	return golem.DefineModel(
		golem.Subscriptions[Post](),
	)
}
```

Then regenerate:

```sh
golem generate --schema ./social --app-out ./social
```

`generate` writes `zz_golem_client.gen.ts` beside the SDL. It is the
**client manifest**: the facts about your API that GraphQL does not carry. Copy
or import it into your front end, and regenerate it whenever the schema
changes. Never edit it. See
[The client manifest](#application-the-client-manifest) and
[Subscriptions and events](#application-subscriptions-and-events).

Every model a live query reaches needs `Subscriptions`. A query over a model
without events is not live; use a static query for it (step 7).


See [client setup](#client) and [React setup and hooks](#react) for installation, credentials, providers and live queries.

<a id="react-server-7-counts-and-aggregates"></a>

### 7. Counts and aggregates

Aggregates, group-by and relation group-by are live through `useGolemQuery`.
They hold no rows, so the client never patches them: any event on a model the
result comes from refetches the whole document, through the same coalescer as
every other refetch. An aggregate's `where` scopes its stream to the rows the
count covers, and the server's policy filter still decides which events arrive.

```tsx
import { useGolemQuery } from '@eleven-am/golem-react'

const PUBLISHED_COUNT = `
  query PublishedCount {
    aggregatePosts(where: { published: { equals: true } }) {
      count
    }
  }
`

export function PublishedCount() {
  const result = useGolemQuery(PUBLISHED_COUNT)
  const count = (result.data as { aggregatePosts: { count: string | number } } | undefined)?.aggregatePosts.count
  return <span>{String(count)}</span>
}
```

Put counts in their own query. Every event on a derived stream refetches the
whole document, so a count beside a long list refetches the list too.

Custom query roots whose result holds model rows behave the same way: they
refetch on any event and are never patched. The many-row mutations
(`updateMany`, `deleteMany`) are mutations, not queries, so no hook applies.

`useGolemStaticQuery` runs without event streams; ordinary TanStack refetch
options and own-write invalidation still apply. Automatically inferred row
streams require models with events. A scalar/DTO custom root can instead
declare operation invalidation; without an applicable observation contract,
`useGolemQuery` throws `GolemNotLiveError` naming the root and reason.

<a id="react-server-8-what-the-server-must-be"></a>

### 8. What the server must be

- **golem go/v0.8.3 or later.** The generated manifest carries `nullable` on
  every root and relation and the `deletedRow` root shape (since go/v0.8.2),
  and lists every model a `relationGroupBy` root groups by (since go/v0.8.3),
  so renaming a related row refreshes the groups. The client reads these facts
  and does not guess. With a go/v0.8.2 manifest a `relationGroupBy` root
  refetches only on events of its own model.
- **Regenerate the manifest after every schema change.** The manifest holds the
  contract fingerprint, and every live request carries it. A client built from
  an older manifest is refused with `GOLEM_CONTRACT_MISMATCH`, which surfaces
  as `GolemContractMismatchError`. `golem check` fails when the generated
  manifest falls behind the schema, so run it in CI. See
  [The live handshake and the subscription ack](#application-the-live-handshake-and-the-subscription-ack).
- **A socket deadline for every principal.** A principal with no deadline is
  refused with `GOLEM_PRINCIPAL_DEADLINE_REQUIRED` (`GolemDeadlineRequiredError`).
  That is a server fix: see [The socket deadline](#application-the-socket-deadline).
- **`golem.Subscriptions` on every model a live query reaches.**

The full error list is in the
[client README](#client-errors).
<!-- /chapter:react-server -->

<!-- chapter:client -->
<a id="client"></a>

## TypeScript client

Live GraphQL for golem servers, without a UI framework: `GolemClient`, the live protocol, the HTTP and WebSocket transport, the TanStack Query cache patcher and typed errors. Works with TanStack Query core, so any framework adapter can sit on top. For React use [`@eleven-am/golem-react`](https://www.npmjs.com/package/@eleven-am/golem-react), which wraps this package in hooks.

<a id="client-install"></a>

### Install

```sh
npm install @eleven-am/golem-client @tanstack/query-core graphql graphql-ws
```

Peer dependencies: `@tanstack/query-core` ^5, `graphql` ^16, `graphql-ws` ^6.

<a id="client-golemclient"></a>

### GolemClient

```ts
import { QueryClient } from '@tanstack/query-core'
import { GolemClient } from '@eleven-am/golem-client'
import { golemClientManifest } from './zz_golem_client.gen'

export const queryClient = new QueryClient()

export const golem = new GolemClient({
  queryClient,
  url: 'https://api.example.com/graphql',
  manifest: golemClientManifest,
  credentials: () => ({
    headers: { authorization: `Bearer ${session.token()}` },
    connectionParams: { token: session.token() },
  }),
})
```

| Option | Meaning |
|---|---|
| `queryClient` | Required. Your application's TanStack `QueryClient`. A second `GolemClient` on a `QueryClient` that already has one throws `GolemConfigurationError`. |
| `url` | The GraphQL endpoint. HTTP requests go to it; the WebSocket connects to the same URL. |
| `manifest` | The generated `zz_golem_client.gen.ts` of your golem server (go/v0.8.3 or later). |
| `credentials` | Optional function returning `{ headers, connectionParams }`. Called again for every request and every connection, including after the server closes a socket at its credential deadline, so return the current token each time. |
| `createSocket` | Optional `(url, protocols, credentials) => socket` factory, for a WebSocket implementation of your own (for example in Node). |
| `fetch` | Optional `fetch` implementation for HTTP requests. Defaults to `globalThis.fetch`. |

<a id="client-without-react"></a>

### Without React

```ts
import { QueryObserver } from '@tanstack/query-core'

const POSTS = `
  query PublishedPosts {
    posts(where: { published: { equals: true } }) { id title }
  }
`

const stop = golem.live.start()
const observer = new QueryObserver(queryClient, golem.queryOptions(POSTS))
const unsubscribe = observer.subscribe((result) => render(result.data))
```

- `golem.live.start()` starts live sync and returns a function that stops it. Start it before you observe a query, so no query is fetched before its subscriptions are open.
- `golem.queryOptions(document, variables?)` returns TanStack options for a live query. It throws `GolemNotLiveError` for a document that cannot be live.
- `golem.infiniteQueryOptions(document, variables?)` returns options for `InfiniteQueryObserver` over a list query with a `take`. Give `take` a variable with a default, or a literal; never write `cursor` or `skip`.
- `golem.staticInfiniteQueryOptions(document, variables?)` uses the same generated-list cursor planner without live sync or subscriptions. It works for models without events, forwards cancellation to HTTP, and uses a separate static cache entry. Normal TanStack refetch options still apply. Call `resetSession()` after changing the authenticated principal or selected profile; it clears cached pages and starts a new chain. Custom DTO continuation pages require a separate page contract; this method currently accepts generated list roots.
- `golem.continuationQueryOptions(document, variables?)` is the live counterpart for a custom page root with generated `invalidation` metadata. Its operation subscription acknowledges before the first fetch and emits no row/data packets. A terminal invalidation clears the whole chain, waits for replacement acknowledgement and restarts at `null`; old in-flight pages cannot append. TanStack invalidation (including committed generic mutations) also resets the chain. `refetchType: 'none'` clears without making a request; explicit ordinary refetch retains the current traversal semantics. A quick remount can reuse open coverage, but reopening after a subscription gap starts a fresh population.
- `golem.staticContinuationQueryOptions(document, variables?)` pages a registered custom `PageQuery` from a format 3 manifest. It uses opaque `string | null` page parameters and adds the continuation selection automatically. Select one pageable root directly and leave `after` to the client. Empty or short pages continue when the server supplies `next`; only `next: null` ends traversal. Expiry and invalid tokens remain errors: discard accumulated pages before starting a new chain. Call `resetSession()` on principal/profile changes too.
- `golem.staticQueryOptions(document, variables?)` returns ordinary TanStack query options without event streams, including for a model without events or a custom root with no model rows. Static queries and continuation pages preserve authored model field selections and carry SDK read metadata for own-write invalidation without creating live entries. Normal TanStack refetch options still apply. Counts, aggregates, group-by and custom roots with model rows can be live through `golem.queryOptions`: any event on a model they come from refetches the document through the coalescer, and they are never patched.
- `golem.mutate(document, variables?)` runs a mutation over HTTP and returns its data. A document holds exactly one operation; one with several is refused with `GolemMultipleOperationsError`.

```ts
const data = await golem.mutate(
  `mutation Retitle($id: UUID!, $title: String!) {
    updatePost(where: { ID: $id }, data: { title: { set: $title } }) { id title }
  }`,
  { id, title: 'New title' },
)
```

<a id="client-transport-status-and-dispose"></a>

### Transport status and dispose

`golem.transport.status` is the state of the WebSocket: `idle`, `connecting`, `open`, `reconnecting`, `unauthenticated`, `forbidden`, `invalid`, `protocol-mismatch`, `contract-mismatch` or `deadline-required`. `golem.transport.onStatus(listener)` calls you on every change and returns an unsubscribe function.

```ts
const off = golem.transport.onStatus((status) => console.log(status))
```

The status of one query (below) is separate from this connection state.

`golem.dispose()` stops live sync, cancels reads, clears cached Golem rows and page chains, closes the transport and releases the `QueryClient` for another `GolemClient`. Unrelated cache entries remain intact.

Socket credentials are prepared again for each connection attempt. If that
preparation rejects, the stream ends with the original error and no socket is
created for that attempt; a fresh subscription or session reset can retry with
current credentials. Reset and disposal fence pending preparation and late
socket opens, including asynchronous cleanup failures from the old session.

<a id="client-live-status"></a>

### Live status

`golem.live.statusOf(query)` returns `{ state, error }` for a TanStack `Query`, or `undefined` before live sync tracks it. `golem.live.subscribeStatus(listener)` calls you on every change.

| State | Meaning |
|---|---|
| `CONNECTING` | Subscriptions are being opened; the first fetch waits for their acks. |
| `LIVE` | Every stream is acknowledged and any recovery fetch has committed for the current generation. |
| `RECOVERING` | Coverage was lost. Cached data is cleared; replacement streams must acknowledge and a current-generation fetch must succeed before returning to LIVE. |
| `LIMIT` | The connection's subscription cap was reached. The data is still fetched, but not live until a slot frees. |
| `FORBIDDEN` | The server stopped a stream because the principal lost access. Cached data is cleared immediately and streams remain stopped until an explicit session reset. |
| `NOT_FOUND` | A row held in a single result slot left the result; the query refetches. |
| `FAILED` | A stream failed for good (`error` holds the cause). |

TanStack's own `isFetching`, `isError` and `data` keep their usual meaning.

<a id="client-initialization-and-authenticated-data-incarnation"></a>

#### Initialization and authenticated data incarnation

A manifest requiring `data-incarnation-v1` needs authenticated discovery before
its first mutation. Call `await golem.ready()` during initialization. Concurrent
calls share one discovery; reads initialize lazily. An unready mutation fails
with `GOLEM_CLIENT_NOT_READY` before credentials or HTTP dispatch. Discovery is
an authenticated POST with a fresh nonce and no business operation. Its result
binds later HTTP requests and WebSocket subscriptions to that database identity.
The supported opted entry points are `GolemClient` and `Transport`; a standalone
`Session` does not own authenticated HTTP discovery.
The capability can coexist with `operation-invalidation-v1`. Manifests without
it need no discovery and preserve their existing wire contract.

Only a fresh discovery can adopt an identity. A business response or socket
acknowledgement claiming another identity clears Golem data and starts fresh
authenticated discovery. The client remains unready until that discovery
succeeds. Active reads restart from their initial page after adoption; discovery
or authentication failure appears as a query error and `FAILED` live status.
Raw `Transport` subscribers receive a typed terminal session refusal and can
subscribe again after readiness. A reset also performs discovery before its
promise resolves. The old socket is disposed
before the replacement session becomes ready.

<a id="client-principal-profile-and-logout-boundaries"></a>

#### Principal, profile and logout boundaries

Update the state read by `credentials`, then call and await `golem.resetSession()`:

```ts
session.set(nextSession) // a new principal/profile, or logout
await golem.resetSession()
```

Reset clears all cached Golem rows and page chains immediately, including inactive queries and static/continuation pages. It cancels outstanding reads, fences late responses even if an HTTP implementation ignores abort, renews transport credentials and reopens active live streams. Live refetches wait for the new acknowledgements; active static queries refetch from their initial page. Unrelated TanStack cache entries are preserved. Reset does not detect changes to arbitrary state captured by your credentials callback: call it explicitly for every principal/profile switch and logout. Raw `transport` requests share the same readiness and session fence; they do not write query-cache data. Detaching live sync closes live streams without changing the principal or cancelling independent static reads. Disposal fences all outstanding Golem reads. Core consumers must retain the returned `queryFn` and `structuralSharing` options: together they own the request and cache-commit fences. Overriding either leaves the supported recovery boundary. React hooks reserve both options.

Mutations capture their session and JSON arguments at invocation. A mutation
queued before reset or disposal fails before dispatch. If it was already sent,
a stale success becomes `GolemCommitUnknownError`; read back authoritative
state and do not replay it automatically. Explicit server PARTIAL/UNKNOWN
errors retain their commit evidence. Old outcomes never invalidate the new
session's cache. Session boundaries also clear SDK-owned completed TanStack
mutation records. Pending records retain scope ordering until settlement and
are then removed if stale; unrelated mutations are preserved.
`golem.onSessionReset(listener)` provides a read-only lifecycle notification
for adapters that retain operation results. It runs on reset, authenticated
identity adoption and disposal and returns an unsubscribe function. `golem.mutations.prepare(document, variables, options)` offers
the same invocation capture for an external scheduler and returns a single-use
operation. Preparing requires readiness too.

Recovery uses a query generation that survives stream replacement. Resyncs and loss of network coverage clear uncertain data immediately; an older response cannot restore it. Replacement opens share a fixed 250 ms cooldown plus 0–50 ms jitter within each query. The deadline does not move when more invalidations arrive. After all replacement acknowledgements, recovery refetches immediately; the ordinary event coalescer cannot delay it. Client cooldown scheduling adds at most 300 ms before reopening, excluding acknowledgements and network time. Ordinary event refetches retain their configured coalescing window and maximum wait. Ordinary connection failures retain exponential backoff up to 30 seconds. Permission and credential invalidation never wait to hide data. These cooldown values are provisional; bounded fake-clock tests exercise the coordination, while real workload latency measurements remain required.

Own successful mutations and explicit PARTIAL/UNKNOWN commit errors invalidate
SDK reads in the same session, including static reads and models without event
streams. Generated reads use their public model dependencies, including selected
nested rows and public relation filters. Opaque/custom roots and computed
results conservatively depend on every own write; the browser does not infer
hidden policy dependencies. Unscoped mutations invalidate all SDK reads.
Write reach comes only from generated `mutationScope` metadata, separately from
the root's response `models`. Generated nested writes include their conservative
model reach; custom writes are unscoped. Older manifests without the additive
hint remain supported and conservatively invalidate all SDK reads. The hint
requires no new manifest version or protocol capability.
Inactive matching reads become stale; active ones refetch under normal TanStack
policy. Unrelated generated-model reads and application cache entries stay
unchanged. Static mode opens no subscriptions.

Invalidating a custom continuation query, static or live, discards its pages and
restarts at `null`. A held or already-settled obsolete result cannot refill the
chain. Ordinary explicit refetch retains the current traversal semantics.
Generated cursor pagination retains its normal refetch traversal.

<a id="client-declared-operation-invalidation"></a>

### Declared operation invalidation

Custom DTO/scalar queries become live only when their generated root declares `invalidation: { kind: 'operation', subscriptionField: '…Changes' }`. The client observes that operation, independently of returned model rows. It preserves authored DTO/model projections and does not add public IDs, versions or typenames to those results. An unexpected data packet on the invalidation stream is a protocol failure; acknowledgement and terminal recovery use the same coordinator as generated lists.

Such manifests remain format 3 and require `operation-invalidation-v1`. Matching clients advertise that capability on HTTP and WebSocket initialization, including static requests. Unknown required capabilities are a configuration error; an old client cannot silently infer returned-row streams against an opted server. Manifests without required capabilities retain their existing handshake bytes. Manually wrapped generated Go servers must pass the exact active `Config.Bundle` so capability requirements cannot be lost behind an executor wrapper.

The capability requirement applies to the whole opted server schema: older clients are rejected for ordinary/static queries and mutations too, not only for the new subscription. Upgrade the client and regenerate its manifest with that server contract. These APIs require the matching go/v0.9.0 contract; the minimum server version listed below for older client features does not imply support for operation invalidation.

Live admission remains a server authorization and coverage check. Readable dependencies currently require local ordinary row/filter/field-mask conditions and declared before-image coverage of the watched and controlling scalars. Ordinary related-row paths are refused; hidden policy facts require genuine bound scope authority and the certified historical writer, which the default V3 writer does not supply. The client does not fall back to broader model streams or infer authority from DTO values. Use `staticQueryOptions` or `staticContinuationQueryOptions` when live observation is unavailable; static mode skips subscriptions but still sends required capabilities. See [the supported server contract](#application-declared-operation-invalidation).

<a id="client-the-manifest"></a>

### The manifest

GraphQL does not say which subscription root carries a model's events, what its primary key is, whether a root returns rows or a count, whether a single-row root can be `null`, which unique input a cursor uses, or which contract the server was built from. The manifest carries those facts. It has one owner, the server's generator: `golem generate` writes `zz_golem_client.gen.ts` beside your schema, and `golem check` fails when it falls behind. The client reads it and never guesses. Regenerate it after every schema change; never edit it. See [The client manifest](#application-the-client-manifest).

<a id="client-typed-dto-results-and-nested-inputs"></a>

### Typed DTO results and nested inputs

go/v0.9.0 registers named output/input structs and generates their GraphQL
shapes and manifest metadata. Ordinary GraphQL selections and gql.tada typing
apply to nested DTO fields too; select the nested fields your UI needs. DTO
members named `id` or `_version` are ordinary result values, not model patch
identities. Embedded model rows are authorized and hydrated by the server.
The resolver remains responsible for the sensitivity of ordinary DTO scalars.

JavaScript object omission and explicit `null` keep their GraphQL meanings.
Server argument fields using `golem.InputValue[T]` can distinguish them; do not
replace omitted members with null while preparing variables. An exactly-one
input accepts one supplied non-null choice and is validated before the server
resolver runs. Client document typing does not replace that runtime check.

Static APIs work without event streams. A scalar/DTO result becomes live only
through declared operation invalidation, rather than by guessing dependencies
from its fields. Custom infinite pages use the continuation APIs, not the
generated-list cursor APIs. See the [server DTO/input contract](#application-named-output-objects-and-embedded-model-rows)
and [React continuation hooks](#react-usegolemcontinuationquery).

<a id="client-how-live-queries-work"></a>

### How live queries work

<a id="client-connections"></a>

##### Connections

A `GolemClient` opens one WebSocket (graphql-ws), and only when the first stream needs it. The socket is lazy and reconnects forever, with backoff, except when the server refuses the handshake for good (protocol or contract mismatch, a missing deadline). Queries, refetches and mutations are plain HTTP `fetch` requests.

<a id="client-subscriptions"></a>

##### Subscriptions

The client reads your query document and opens one subscription per (model, filter) pair in it.

- A literal or variable `where` gives an `exact`-scoped stream: the server sends only events for rows that match.
- No `where` gives a `policy-only` stream: the server sends every event the caller's policy allows for that model.
- A nested relation is a pair of its own. When the parent has a `where` and the manifest knows the relation's inverse, the child stream is scoped by that parent filter. Otherwise it is `policy-only` unless the relation has its own `where`.
- Two selections with the same model and filter share one stream. Different filters do not. (Two selections that select the same response key differently get separate streams.)
- Components that run the same query with the same variables share one TanStack query, and so one set of streams.

| Document | Streams |
|---|---|
| `posts(where: { published: { equals: true } }) { id title }` | 1 (`Post`, exact) |
| `posts { id comments { id } }` | 2 (`Post` and `Comment`, both policy-only) |
| Two lists, both `posts(where: { published: { equals: true } })` | 1 |
| `posts(where: { published: { equals: true } })` and `posts(where: { published: { equals: false } })` | 2 |

Automatically inferred row streams require `golem.Subscriptions` on their models. Custom roots can instead use declared operation invalidation; without an applicable observation contract, `useGolemQuery` throws `GolemNotLiveError`.

<a id="client-filtering-is-automatic"></a>

##### Filtering is automatic

You never filter events in the client. The server judges each event against the caller's policy and the stream's filter, and sends one of four event types: `CREATED` and `UPDATED` for a row that matches, `EXITED` for a row that stops matching or that the caller may no longer read, and `DELETED` for a removed row. A row that starts to match arrives as `CREATED` or `UPDATED`. See [Rows that leave a list](#application-rows-that-leave-a-list).

<a id="client-patch-or-refetch"></a>

##### Patch or refetch

- `UPDATED` is patched into the cache in place only when the stream's selection is plain: a top-level list or single-row root, arguments limited to `where` (or the unique selector of a single-row root), no `orderBy`, `distinct`, `take`, `skip` or cursor, and scalar fields only. Every other `UPDATED` refetches the query.
- `CREATED` refetches.
- `DELETED` and `EXITED` remove the held row from the cache at once. The query then refetches, because a removal changes a list or empties a slot.
- A response that carries an older row version than the cache already holds is discarded, and the query refetches.
- A mutation result is never written into the query cache. A committed mutation invalidates the cached queries over the models it touches (TanStack marks them stale and refetches the ones in use).
- Refetches that arrive close together are coalesced into one.

<a id="client-row-versions"></a>

##### Row versions

The client adds `_version` and `__typename` to every selection of a model with events, and the primary key fields (`id` for most models) when you left them out. It compares `_version` to decide which copy of a row is newer. You can select them yourself, but not alias another field to those names. See [Row versions](#application-row-versions).

<a id="client-status-is-per-query"></a>

##### Status is per query

`golem.live.statusOf` reports one query, keyed by its document and variables. It is not a global connection state. For that, read the transport:

- `golem.transport.status`: `idle`, `connecting`, `open`, `reconnecting`, `unauthenticated`, `forbidden`, `invalid`, `protocol-mismatch`, `contract-mismatch` or `deadline-required`.
- `golem.transport.onStatus(listener)`: calls you on every change and returns an unsubscribe function.

Wrap them in your framework's external-store primitive. In React, `useGolemConnection()` from `@eleven-am/golem-react` does it.

<a id="client-why-there-is-a-manifest"></a>

##### Why there is a manifest

GraphQL does not say which subscription root carries a model's events, what its primary key is, whether a root returns rows or a count, whether a single-row root can be `null`, which unique input a cursor uses, or which contract the server was built from. The manifest carries those facts. It has one owner, the server's generator (`golem generate` writes `zz_golem_client.gen.ts`), and `golem check` fails when it falls behind the schema. The client reads it and never guesses. See [The client manifest](#application-the-client-manifest).

<a id="client-server-concepts"></a>

##### Server concepts

- [Subscriptions and events](#application-subscriptions-and-events)
- [Row versions](#application-row-versions)
- [Rows that leave a list](#application-rows-that-leave-a-list)
- [The live handshake and the subscription ack](#application-the-live-handshake-and-the-subscription-ack)
- [The socket deadline](#application-the-socket-deadline)

For a walkthrough from a model to a live component, see [Using golem from React](#react-server).

<a id="client-mutations-on-page-unload"></a>

### Mutations on page unload

Browsers cancel ordinary fetches while a page unloads. To send a last mutation on `pagehide`, pass `keepalive`; only that request is sent with `fetch(..., { keepalive: true })`.

```ts
addEventListener('pagehide', () => {
  void golem.mutate(END_SESSION, { id: sessionId }, { keepalive: true }).catch(() => {})
})
```

The third argument is either an `AbortSignal` or `{ signal?, keepalive? }`. The response may never be processed, so treat the mutation as possibly committed; the next page load refetches the authoritative state. Browsers cap keepalive request bodies at 64 KiB in total, so a larger body throws `GolemKeepaliveTooLargeError` before anything is sent.

<a id="client-errors"></a>

### Errors

All server errors are `GolemError` (`code`, `path`, `extensions`, `retryable`) or a subclass:

- `GolemProtocolMismatchError`, `GolemContractMismatchError`: the client and server disagree on the live protocol or the contract. Regenerate the manifest.
- `GolemDeadlineRequiredError` (`GOLEM_PRINCIPAL_DEADLINE_REQUIRED`): the server refused a live socket because the principal has no deadline. This is a server fix.
- `GolemUnauthenticatedError`, `GolemHttpError`.
- `GolemPartialCommitError`, `GolemCommitUnknownError`: a mutation whose commit state is partial or unknown. Affected live queries are invalidated in the same session, as after every committed mutation; stale-session outcomes do not invalidate replacement data.

Client-side failures: `GolemConfigurationError` (bad setup), `GolemDocumentError` (a document the client cannot run, for example one that sets `cursor` or `skip` itself) and its subclass `GolemMultipleOperationsError` (a document with more than one operation), `GolemNotLiveError`, `GolemNotFoundError`, `GolemProtocolError`.

<a id="client-requirements"></a>

### Requirements

- golem **go/v0.8.3 or later** on the server, with `golem.Subscriptions` on every model a live query reaches.
- `@tanstack/query-core` 5.
- A socket deadline for every principal. See [The socket deadline](#application-the-socket-deadline).

<!-- /chapter:client -->

<!-- chapter:react -->
<a id="react"></a>

## React setup and hooks

Live React queries for golem servers. You write an ordinary GraphQL query; the hooks open the matching subscriptions behind it, and creates, updates, deletes and rows leaving a result patch the TanStack Query cache by themselves. Your components never write to the cache.

Built on `@eleven-am/golem-client` and `@tanstack/react-query`.

<a id="react-install"></a>

### Install

```sh
npm install @eleven-am/golem-react @eleven-am/golem-client @tanstack/react-query react gql.tada graphql graphql-ws
```

Peer dependencies: `@eleven-am/golem-client` ^0.3.0, `@tanstack/react-query` ^5, `react` >=18, `gql.tada` ^1.8. The client in turn needs `graphql` ^16 and `graphql-ws` ^6 (and `@tanstack/query-core` ^5, which `@tanstack/react-query` brings).

<a id="react-setup"></a>

### Setup

The client lives on your application's `QueryClient`. It does not make a second one.

The manifest is `zz_golem_client.gen.ts`, written by `golem generate` beside your GraphQL schema on the server (go/v0.8.3 or later). Copy or import it into your front end, and regenerate it whenever the schema changes. Never edit it.

```tsx
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { GolemClient } from '@eleven-am/golem-client'
import { GolemProvider } from '@eleven-am/golem-react'
import { golemClientManifest } from './zz_golem_client.gen'

export const queryClient = new QueryClient()

export const golem = new GolemClient({
  queryClient,
  url: 'https://api.example.com/graphql',
  manifest: golemClientManifest,
  credentials: () => ({
    headers: { authorization: `Bearer ${session.token()}` },
    connectionParams: { token: session.token() },
  }),
})

export function Root() {
  return (
    <QueryClientProvider client={queryClient}>
      <GolemProvider client={golem}>
        <App />
      </GolemProvider>
    </QueryClientProvider>
  )
}
```

- `credentials` is called again whenever a request or a connection is made, including after the server closes a socket at its credential deadline, so return the current token each time.
- `GolemProvider` throws `GolemConfigurationError` when the surrounding `QueryClientProvider` does not hold `golem.queryClient`. It attaches live sync with `useSyncExternalStore` (no effects) and renders its children once live sync is running, so no query is fetched before its subscriptions are open. It renders nothing on the server.
- A second `GolemClient` on a `QueryClient` that already has one throws `GolemConfigurationError`. Call `golem.dispose()` to release it.

<a id="react-hooks"></a>

### Hooks

Every hook takes a GraphQL document, as a string or as a gql.tada typed document (the result and variable types come from the document; a plain string gives `unknown` data). The examples use the social example schema.

<a id="react-usegolemquery"></a>

#### useGolemQuery

`useQuery` over `client.queryOptions`. The query is live: its streams are open while the component is mounted.

```tsx
const POSTS = `
  query PublishedPosts {
    posts(where: { published: { equals: true } }) { id title }
  }
`

function Posts() {
  const posts = useGolemQuery(POSTS)
  const rows = (posts.data as { posts: Array<{ id: string; title: string }> } | undefined)?.posts ?? []
  return <ul>{rows.map((post) => <li key={post.id}>{post.title}</li>)}</ul>
}
```

Aggregates, group-by, relation group-by and custom roots that return model rows are live: any event on a model they come from refetches the document through the coalescer, and they are never patched. Put counts in their own query. It throws `GolemNotLiveError` (naming the reason and the root) for a document that reaches a model without events or a custom scalar/DTO root without declared operation invalidation. A declared invalidation stream can make a rowless custom result live; otherwise use `useGolemStaticQuery`. Relation group-by needs a go/v0.8.3 manifest to refetch on events of the models it groups by.

<a id="react-usegolemstatus"></a>

#### useGolemStatus

The live status of a query, or `undefined` before the query exists. It takes the same document and variables as the query; pass `{ infinite: true }` as the third argument for an infinite query. It is per query, not global.

```tsx
const status = useGolemStatus(POSTS)
return <small>{status?.state}</small>
```

<a id="react-usegolemconnection"></a>

#### useGolemConnection

The connection state of the whole client, as one of the `transport.status` values: `idle` before any live query has opened a connection, then `connecting`, `open`, `reconnecting` or one of the fatal states. It is global, unlike `useGolemStatus`.

<a id="react-initialization-and-session-changes"></a>

#### Initialization and session changes

When the generated manifest requires `data-incarnation-v1`, await
`client.ready()` before offering mutation actions. Queries initialize lazily.
Update the state read by `credentials`, then await `client.resetSession()` for
each principal/profile switch or logout. Reset clears every Golem query and
page chain, disposes the old socket and discovers the current data identity;
other TanStack cache entries remain available. `operation-invalidation-v1` can
be required alongside the incarnation capability.

<a id="react-usegolemmutation"></a>

#### useGolemMutation

A TanStack mutation over `client.mutate`. A committed mutation invalidates every cached query over the models it touches, so the live event or the refetch brings it up to date. The mutation result is never written into the query cache. A document holds exactly one operation.

```tsx
const RETITLE = `
  mutation Retitle($id: UUID!, $title: String!) {
    updatePost(where: { ID: $id }, data: { title: { set: $title } }) { id title }
  }
`

const retitle = useGolemMutation(RETITLE)
retitle.mutate({ id, title: 'New title' })
```

The hook captures the session and JSON arguments when `mutate` or
`mutateAsync` is called, before async `onMutate`, offline pauses or scope queues.
A queued old-session intent cannot send after reset. Sent stale outcomes report
`GolemCommitUnknownError`; explicit PARTIAL/UNKNOWN evidence is preserved.
Retries are disabled even when hook or application defaults request them.
Original variables (including `undefined`), typed optimistic context, global
MutationCache callbacks and per-call callbacks keep their TanStack shapes.
Hook and global cache callbacks run for each invocation. Per-call callbacks
belong to the latest observed invocation and stop after unmount or a session
boundary. Reset, identity adoption and disposal reset mounted mutation results
and clear completed Golem MutationCache records. Pending records preserve
TanStack scope ordering until settlement, then stale records are removed;
`mutateAsync` still rejects with the original explicit commit evidence.
Other mutation records remain available. `meta.golem` is reserved for SDK
ownership; other application metadata is preserved. Read back uncertain writes
instead of replaying them automatically.

Own successful mutations and explicit PARTIAL/UNKNOWN commit errors invalidate
SDK reads in the same session, including static reads and models without event
streams. Generated reads use their public model dependencies, including selected
nested rows and public relation filters. Opaque/custom roots and computed
results conservatively depend on every own write; the browser does not infer
hidden policy dependencies. Unscoped mutations invalidate all SDK reads.
Write reach uses the generated manifest's `mutationScope`, independently of
response models. Custom mutations and older manifests without this metadata
conservatively invalidate all SDK reads; generated nested writes use the
server's conservative model scope.
Inactive matching reads become stale; active ones refetch under normal TanStack
policy. Unrelated generated-model reads and application cache entries stay
unchanged. Static mode opens no subscriptions.

Invalidating a custom continuation query, static or live, discards its pages and
restarts at `null`. A held or already-settled obsolete result cannot refill the
chain. Ordinary explicit refetch retains the current traversal semantics.
Generated cursor pagination retains its normal refetch traversal.

<a id="react-usegoleminfinitequery"></a>

#### useGolemInfiniteQuery

`useInfiniteQuery` over a list query with a `take`. Pages stay live. Give `take` a variable with a default, or a literal; never write `cursor` or `skip`, the client adds them from the manifest. `hasNextPage` is true while the last page is full.

```tsx
const FEED = `
  query Feed($take: Int = 20) {
    posts(take: $take, orderBy: [{ createdAt: desc }]) { id title }
  }
`

const feed = useGolemInfiniteQuery(FEED)
const pages = feed.data?.pages ?? []
// feed.hasNextPage, feed.fetchNextPage()
```

<a id="react-usegolemstaticinfinitequery"></a>

#### useGolemStaticInfiniteQuery

The same generated-list cursor pagination without event streams. It works for
models without subscriptions and preserves typed document results, variables
and `Cursor | null` page parameters. Normal TanStack refetch options still apply;
clear cached pages when the authenticated principal or selected profile changes.
Custom `PageQuery` roots use `useGolemStaticContinuationQuery` below.

```tsx
const feed = useGolemStaticInfiniteQuery(FEED)
const pages = feed.data?.pages ?? []
// feed.hasNextPage, feed.fetchNextPage()
```

<a id="react-usegolemstaticcontinuationquery"></a>

#### useGolemStaticContinuationQuery

Pages a custom server `PageQuery` using opaque `string | null` page parameters.
The server's generated manifest must use format 3. Select one pageable root
directly; the hook obtains `next` automatically and owns the `after` argument.
Your model field selections stay as authored, including title-only selections
whose row identities are private.

For a `homeFeed` page whose items have a `title` field:

```tsx
const HOME = `
  query Home($take: Int = 25) {
    homeFeed(take: $take) { items { title } }
  }
`
const feed = useGolemStaticContinuationQuery(HOME)
const pages = feed.data?.pages ?? []
// feed.hasNextPage, feed.fetchNextPage()
```

A short or empty page can have a next token. Only `next: null` ends traversal.
Expired tokens fail with reason `CONTINUATION_EXPIRED`; discard the accumulated
chain and start at its first page. Clear cached pages when the principal or
selected profile changes. Normal TanStack refetch options still apply.

<a id="react-declared-live-continuation-pages"></a>

<a id="react-usegolemcontinuationquery"></a>

#### useGolemContinuationQuery

`useGolemContinuationQuery(document, variables, options)` exposes the same generated result and `string | null` page types as the static continuation hook. It requires a generated operation invalidation subscription; it never infers observability from the returned rows. Invalidations discard accumulated pages and restart after replacement acknowledgement. Use `useGolemStaticContinuationQuery` when the root has no invalidation declaration. Query functions, continuation parameters and structural sharing remain coordinator-owned options.

The server may refuse live admission when the declared source lacks snapshot coverage or uses unsupported ordinary relation paths. `useGolemStaticContinuationQuery` remains available without subscribing; it does not bypass query authorization or the schema's required capability handshake. Operation-invalidation schemas require a matching client for every request, including static queries and mutations. See [the server's supported invalidation contract](#application-declared-operation-invalidation).

<a id="react-usegolemstaticquery"></a>

#### useGolemStaticQuery

`useQuery` without event streams, including for a model without events or a custom root with no model rows. Authored model field selections are preserved; normal TanStack refetch options still apply. Counts and aggregates can be live through `useGolemQuery`.

```tsx
const COUNT = `
  query PublishedCount {
    aggregatePosts(where: { published: { equals: true } }) { count }
  }
`

const total = useGolemStaticQuery(COUNT)
const liveTotal = useGolemQuery(COUNT)
```


Live status, transport behavior, patch/refetch decisions, session isolation, errors and peer requirements are shared with the [core client](#client). Use [the server setup](#react-server) to enable events and credential deadlines.
<!-- /chapter:react -->

<!-- chapter:observability -->
<a id="observability"></a>

## Observability and application testing

`Config.Observer` takes an `observe.Observer`
(`ObserveGolem(ctx, observe.Observation)`). Adapters: `observe/slog` and
`observe/otel`, both queued; `observe.NewDispatcher` wraps your own.
Observations are payload-free: `Kind()`, `Operation()`, `Phase()`,
`Outcome()`, `Reason()`, `Provider()`, `ModelID()`, `Duration()`,
`StatementCount()`, `Attempt()`, `QueueType()` and more. `Open` does not log:
wire an observer to see the `runtime.open` record of a refused start.

Worth alerting on: `runtime.maintenance` failures (SQLite statistics refresh,
`Attempt()` = consecutive failures), `transaction.system_escape`,
`subscription.overflow`, `event.transport_reconnect`, `queue.execute`
failures, `events.ObservationTopology`, `events.ObservationDeleteCoverage`,
`events.ObservationDeleteSnapshotOversize`. Custom operations are observed as
`mutation.custom` and transaction operations as `mutation.operation`; each
`ReconfigureQueue` outcome emits one queue observation with type `plan`.

---

<a id="observability-testing-an-application"></a>

### Testing an application

- SQLite needs nothing: a file in `t.TempDir()`, migrations applied before
  `Open`.
- PostgreSQL needs a real server and one database per test run: golem's system
  tables live in that database's `_golem` namespace.
- Run `golem check --schema ./p --app-out ./p` in CI; it also guards the
  client manifest.
- Write a regression test that writes through `System()` for every invariant
  you own: hooks do not run there.
- A test that skips because a database address was missing did not pass.

---

<!-- /chapter:observability -->

<!-- chapter:public-api -->
<a id="public-api"></a>

## Public API and compatibility

Eighteen packages are public. Everything under `internal/` is not, and Go
enforces that — an application cannot import it.

| Package | What it is for |
|---|---|
| `golem` | schema declaration, policies, values, rows |
| `runtime` | opening an application, limits, queue configuration |
| `provider`, `provider/sqlite`, `provider/postgresql` | opening a database |
| `graphql` | serving the generated GraphQL API |
| `queue` | durable jobs |
| `render` | serving a single-page application |
| `embedding` | supplying an embedding provider |
| `events`, `events/nats` | event transport and its NATS adapter |
| `observe`, `observe/otel`, `observe/slog` | observation |
| `queryplan` | reading a query plan |
| `golemtest`, `events/cdctest`, `events/transporttest` | testing against golem |

<a id="public-api-what-is-guaranteed"></a>

### What is guaranteed

**The shape of the surface is recorded and enforced.**
`internal/publicapi/surface.txt` holds the type-checked API of those
packages, one entry per line. It is read from `go/types`, not from source
text, so it records what the compiler sees:

- **functions and methods** with their full signature, including type
  parameters and their constraints. Methods are recorded per method set:
  `(T).M` for a method callable on a value and `(*T).M` for one callable on a
  pointer, so moving a method from a value to a pointer receiver removes an
  entry. Methods promoted through an embedded field are included.
- **types** as either an alias (`= target`) or a defined type with its
  underlying type and type parameters. A defined non-interface type that is
  comparable has a `comparable` entry, so losing comparability — which breaks `==` and use
  as a map key — is a removal.
- **struct fields** in each exported field's own entry with its type, whether
  it is embedded, and its tag. Fields promoted through an embedded field are
  recorded as `promoted field`. When every field of an exported struct is
  exported, an `unkeyed literal {A, B, C}` entry records their order, because
  an unkeyed literal `T{a, b, c}` breaks when a field is added, removed, or
  reordered.
- **interfaces** with the list of their methods in their header, so adding
  a method (which breaks every implementation outside golem) is a removal of
  the old header, not only an addition. An interface with an unexported
  method is marked `sealed`. Each method has its own entry with its
  signature, and each type-set element (`~int | ~string`, `comparable`) has
  an `element` entry.
- **constants** with their type and evaluated value, so reordering an `iota`
  block is caught.
- **variables** with their type.
- **types golem does not export but a caller can still reach** — the target
  of a public alias to an `internal/` type, or an unexported type returned by
  an exported function — are recorded the same way under their own path,
  because a caller can use their fields and methods.

Parameter names, declaration order, comments, and the choice between
equivalent spellings (`byte` and `uint8`, `any` and `interface{}`, an alias
and its target) are not API and produce no difference.

`TestPublicSurfaceMatchesItsRecord` compares the code against the record and
fails when they differ, separating what was **removed** from what was added,
because removal is what breaks an application. A changed entry shows as a
removal of the old line and an addition of the new one.

Unexported struct fields are not recorded individually: a caller cannot name
them. What they can change for a caller is recorded instead — whether the
type is comparable, and whether an unkeyed literal is possible at all (it
is not once any field is unexported, so the `unkeyed literal` entry
disappears).

Changing the surface is allowed. Changing it silently is not: the record has
to be updated in the same commit, which puts the change in the diff a
reviewer reads. A failing run writes the current surface to
`internal/publicapi/surface.actual.txt`, which git ignores; regenerating the
record is copying that file over `surface.txt` and saying why in the commit.

**Behaviour is guaranteed by tests, not by prose.** Earlier releases carried
seven `PUBLIC-*-ABI` documents describing behaviour in words. Words drift
from code without failing anything, and those documents were removed. The
contracts they described are enforced where they can fail: authorization is
pinned by the policy oracles, provider parity by the shared provider
harness, error classification by the code tests, and the documented
workflows by the pages in this directory, each executed by a test.

<a id="public-api-what-is-not-guaranteed"></a>

### What is not guaranteed

**This is a 0.x module.** A minor version may remove or change a public
symbol. When it does, the change appears in the surface record and in the
release notes, and the release notes say what to do about it.

**The surface record sees shape, not everything a caller can depend on.**
It does not see:

- behaviour — what a function does with the same signature;
- additions that break only in unusual code: a new method or promoted field
  can make a selector ambiguous in a caller's type that embeds two golem
  types, and a new exported field in a struct that also has unexported fields
  is reported only as added;
- field order, size, and alignment of structs with unexported fields, which
  only `unsafe` and reflection observe;
- declarations behind build constraints for another platform, since the
  record is taken on the platform the test runs on;
- changes inside standard-library or third-party types that golem's
  signatures mention by name.

It also reports some changes that are not breaking: renaming a type
parameter, or rewriting a constraint as a different but equivalent type set,
shows as a change.

**Generated code is not the public surface.** The `zz_golem_*.gen.go` files
in your own package are yours; their shape follows your schema. What golem
promises is the *generator*, and `golem check` tells you when generated code
no longer matches the schema it came from.

**A declaration can change what is generated.** Declaring a version token
removes nested relation methods, because nested inputs cannot express a
per-row version expectation. Changes of that kind are stated in the release
notes rather than discovered from a compile error.

**Kernel mode changes what is generated, not the surface.** An application
that declares `golem.WithoutGraphQL` gets no GraphQL files and no `GraphQL`
method on its `App`. The `graphql` package is still public and unchanged; such
an application simply never imports it.

**The credential-expiry marker is part of the surface.**
`golem.WithoutCredentialExpiry(ctx)` and `golem.HasNoCredentialExpiry(ctx)` let
server-side code declare that a principal's credential never expires, so that
`EventLimits.RevalidateEvery` bounds its live socket (GUIDE, "The socket
deadline"). The marker is a context value under an unexported key; golem never
reads it from client input, and that is part of the contract.

<!-- /chapter:public-api -->

<!-- chapter:upgrading -->
<a id="upgrading"></a>

## Upgrading Go applications

Read the release boundary below before upgrading. Use matching CLI and library builds, review migrations, regenerate Go/GraphQL/client bindings, and deploy the generated manifest with the client. Stop incompatible workers before queue storage changes; drain an outbox before changing its wire format.

<a id="upgrading-gov092"></a>

### go/v0.9.2

This patch recognizes PostgreSQL's `NOT NULL` catalog entries introduced in
PostgreSQL 18. Schema verification and Golem-owned queue, migration, outbox and
data-incarnation storage accept validated nullability that matches their
columns. Unvalidated or unenforced constraints and unexpected storage shapes
remain errors. Install matching v0.9.2 CLI and library builds. No database
migration or browser-package update is required; client and React 0.3.1 remain
current. Applications upgrading from before v0.9.1 must also follow its
regeneration instructions below.

<a id="upgrading-gov091"></a>

### go/v0.9.1

This dependency maintenance patch updates the Go runtime, providers and generator
together with browser client and React packages 0.3.1. Install matching v0.9.1 CLI
and library builds, run `go mod tidy`, regenerate application bindings and the
client manifest, then build and test your application. Gqlgen's executable
interface changed; previously generated executables must be regenerated against
the updated library. The patch introduces no database migration, event-format
change or V4 writer activation.

Client 0.3.1 supports GraphQL 16 and 17 and React 18 and 19. Node 20.19 remains
supported with GraphQL 16; GraphQL 17 requires Node 22, 24 or 26 and later. Use a Node version satisfying
all installed peer packages. Application-facing Golem contracts remain those of
0.9.0 / client 0.3.0 below.

<a id="upgrading-gov090"></a>

### go/v0.9.0

Use matching v0.9.0 CLI and library builds, regenerate application bindings,
and use client 0.3.0 for the new manifest capabilities. Active contracts use
Contract8; old generated applications must regenerate rather than bypass
startup verification. Build and serve with Go 1.26.11 or later: the toolchain
pin includes the standard-library fix for
[GO-2026-6859](https://pkg.go.dev/vuln/GO-2026-6859).

**Typed custom results and inputs.** Named nested DTOs, computed objects,
caller-hydrated embedded model rows, explicit item gates, input presence and
exactly-one inputs share generated GraphQL and client metadata. Nested result
and total hydration budgets apply before descendant work. Denied embedded rows
follow their declared nullability; resolver-owned scalar disclosures remain
explicit.

**Custom continuation pages and invalidation.** Runtime-owned encrypted tokens
bind the contract, operation revision, arguments, actor scope and durable data
identity with an absolute expiry. Empty visible pages may still have a next
candidate batch. Declared operation dependencies support whole-result
invalidation, and client 0.3.0 adds static and live continuation query APIs.

**Authorization, vectors and query inputs.** Compact scalar sets preserve exact
provider semantics without widening parameter budgets. Caller/system and both
transaction clients can rank prepared vectors and retrieve authorized ready
vectors without calling the embedding provider in a transaction. Input errors
include argument paths and expected shapes; empty relation `some: {}` expresses
existence. Required, proven-visible local order keys avoid a redundant null sort.

**Reader-first historical policy evidence.** This release reads V3 and V4 facts
and retains the automatic V3 writer. V4 writing and named-fact activation are
not enabled by this tag; their complete producer/consumer path is exercised
only by the explicit staged acceptance. Deploy this dual-reader release before
activating a later V4 writer. After V4 commits, rollback must retain V4 readers;
v0.8.8 cannot decode those facts. Ordinary target-readable relation semantics
remain unchanged.

**Priority queues and bounded maintenance.** Two priority lanes, reserved
interactive admission, monotonic transactional pending promotion and bounded
normal-service opportunities preserve dedupe, fencing and attempts. Stop old
workers and run the explicit reviewed queue-storage upgrade before startup
against legacy storage; the new worker does not silently upgrade it. The
bounded-removal reference demonstrates logical hiding followed by resumable
cleanup, including semantic state, on both providers.

**Offline transformations and restore.** Reviewed SQLite required-column fills
and data-only corrections preserve transaction and ledger ownership. Local
snapshot and restore APIs verify schema/history, facts and queue storage.
Restore explicitly resumes at least once or abandons/resynchronizes archived
work and establishes a fresh durable identity. Client discovery fences old
reads, mutations, cached rows and continuations across that boundary. These
APIs require the documented offline ownership conditions.

The guides document these APIs as go/v0.9.0 features. V4 writer activation
remains deferred as described above. Local
Go/provider/race and actual-client acceptance results are recorded in
[`go/compatibility/README.md`](DEVELOPMENT.md#compatibility). NAS performance
and durability are explicitly unverified. This tag does not certify a deployment.

<a id="upgrading-earlier-go-upgrade-boundaries"></a>

### Earlier Go upgrade boundaries

Read this upgrade chapter before every upgrade. Upgrade paths that need action:

- **0.6.x → 0.7.0**: drain the outbox on 0.6.x (every event schema changed);
  update; regenerate; fix the few signature breaks (keyed literals for
  `events.Limits`, `Config`, `GraphQLConfig`, `MutationLimits`; function values
  of `Transaction` and `Subscriptions`); review error handling and policy
  purity; handle `RESYNC` everywhere. The versioned migration guide below lists each change.
- **0.7.x → 0.8.0**: drain the outbox on 0.7.x, regenerate (contract format 7),
  add `golem.MaxItems` to every custom or computed list, update
  `golem.GraphQLList(type, max)` calls, delete any pending
  `.golem-pending-backfill.json`.
- **0.8.0 → 0.8.1**: drain on 0.8.0 (fact codec V3), regenerate, apply the row
  version migration before starting, rename fields called `GolemVersion` or
  `_version`, add the `golem` block to any hand-written live client, give every
  live principal a deadline, handle `EXITED`.
- **→ 0.8.3**: upgrade every worker before relying on `OnJobFailed`.

| Release | What changed |
|---|---|
| go/v0.9.0 | Contract8; named DTOs and presence/one-of inputs; embedded row gates; compact scalar sets and readable existence; prepared vector ranking/read; authenticated custom pages; declared invalidation and recovery/session fences; queue priority/promotion and explicit storage upgrade; reviewed transforms and local snapshot/restore; V3/V4 readers with V3 automatic writing; client 0.3.0; Go 1.26.11 toolchain |
| go/v0.8.8 | `GraphQLWithoutIndexSearch()` keeps a model's index search out of GraphQL; `golem.InputError` puts `extensions.field` on BAD_USER_INPUT errors; SQLite BEGIN contention is a `CONFLICT` that `RetryConflicts` replays; `TextSearch<Index>With(ctx, q, take, golem.TextSearchOptions[M]{AllTerms, Where, OrderBy, Select})`; `internal` on relations (field-local) |
| go/v0.8.7 | no public Go API change; the durable-actions recipe is tested on PostgreSQL over GraphQL HTTP with a deterministic lock-order probe; the social example streams `PostTag` (migration 0010); client 0.2.1 adds `useGolemConnection()` |
| go/v0.8.6 | custom queries receive the caller's actor over GraphQL and from Go through the new generated `Query` (the read-side twin of `Mutate`); generated GraphQL Go code is gofmt-clean and generation fails if gofmt does not converge; doc fixes: `FindUnique(ctx, Keys.ByKeyHash.Value(hash))`, the durable-actions `requestExport` selects `Exports.ID`, `By<Field>` only for non-null unique fields |
| go/v0.8.5 | publisher wakes on commit (~20 ms); recipe race guards (cancel order, job-ID match, conditional success write) |
| go/v0.8.4 | `tx.CancelJob`; serialization failures in `CancelJob`/`Enqueue` are `CONFLICT`; partial queue plans; failure intent only for types with `OnJobFailed`; `internal` primary key refused at compile time; one `HookEnqueue` path |
| go/v0.8.3 | `HookEnqueue`; `OnJobFailed`; `ReconfigureQueue`/`QueuePlan`; `ActorFrom` in custom mutations; `internal`; `relationGroupBy` models in the manifest; durable-actions recipe |
| go/v0.8.2 | manifest `shape`, `nullable`, `uniqueInput`; `4401` with no error frame first |
| go/v0.8.1 | row versions; `EXITED`; live protocol and ack; client manifest; socket deadlines and `WithoutCredentialExpiry`; commit report; one shared read per event; `DEPENDENCY_CHANGED`; `InvalidatePolicies` ends streams; CDC `golem_version` and optional update `Before` |
| go/v0.8.0 | contract format 7; custom results re-read through the caller; `COMMITTED_RESULT_UNAVAILABLE`; `MaxItems`; cost follows `take`; hooks see Go values; typed GraphQL errors; reachable-only SDL; CDC batches recorded first |
| go/v0.7.3 | Go 1.26; SQLite driver v0.36.0; `vec1`; `OpenNetworkFilesystemWAL`; rollback profile converts WAL databases |
| go/v0.7.1 | `sqlite.OpenNetworkFilesystem` |
| go/v0.7.0 | readable-only relation checks; narrowing write rules; `NOT_GRANTED`; policy cache; `Reason`/`Limit`/`Is` on errors; `LIMIT_EXCEEDED`; datetime truncation; `LockKeys`; `Isolation`/`RetryConflicts`; `TxOperation`/`Run`/`CanCount`/`CanAggregate`; kernel mode; `DeleteSnapshot`; events on hidden models; re-checks and caps; SSE; publisher topology |
| go/v0.6.8 | PostgreSQL enum and `IsNull` checks apply |
| go/v0.6.7 | fixed order for nested operations on one relation |
| go/v0.6.6 | batch writes no longer fail under concurrent writers; a target that stops matching is `NOT_FOUND` |
| go/v0.6.5 | upsert/`ConnectOrCreate` must name their target; no FK arithmetic; PostgreSQL writes no longer deadlock |

---



<a id="upgrading-draining-the-event-outbox"></a>

### Draining the event outbox

Before crossing a retired event-format boundary, stop event-producing writes
and keep a publisher running on the old compatible binary. On SQLite use the
table names below. On PostgreSQL qualify both tables with the system namespace
from the generated migration.

```sql
SELECT COUNT(DISTINCT o."causation_id") AS undelivered
FROM "_golem_outbox" o
LEFT JOIN "_golem_outbox_delivery" d ON d."causation_id" = o."causation_id"
WHERE d."causation_id" IS NULL OR d."status" IN ('pending', 'leased', 'blocked');
```

Wait for zero before replacing the compatible publisher. Blocked deliveries
count as undelivered and do not retry automatically. Inspect their failure codes
with `SELECT "causation_id", "last_failure_code" FROM "_golem_outbox_delivery" WHERE "status" = 'blocked';`,
then use `EventOperator().Resume(ctx, causation)` to retry or explicitly
`Retire(ctx, causation)` to abandon delivery. Retirement is an operator decision,
not successful delivery. Retain required historical bundles until delivery is
complete; remove bundles whose contract format the new runtime has retired.
An unreadable fact blocks with `schema-unavailable`; deploying a new binary
does not repair or deliver it. `TestDrainRunbookQueryCountsUndelivered` executes
this query against pending, leased, blocked, delivered and retired cases.

<a id="upgrading-applications-older-than-gov070"></a>

### Applications older than go/v0.7.0

Upgrade policies from raw SQL to typed predicates, replace message matching with codes/reasons, review transaction isolation/retry behavior, and configure the required event limits, principal expiry and operator reporting. The detailed migration remains available in the [versioned Go 0.6-to-0.7 migration guide](https://github.com/eleven-am/golem/blob/0b58da0a881fb39a464fe7cd51262524c5d7c749/docs/golem-go/MIGRATION.md). Follow its required steps before applying the later boundaries above.

<a id="upgrading-versioned-release-history"></a>

### Versioned release history

Historical release records are retained in Git, rather than copied into the current manual: [Go release history](https://github.com/eleven-am/golem/blob/0b58da0a881fb39a464fe7cd51262524c5d7c749/docs/golem-go/RELEASE-NOTES.md) and [TypeScript release history](https://github.com/eleven-am/golem/blob/0b58da0a881fb39a464fe7cd51262524c5d7c749/RELEASE_NOTES.md). These links pin the last documentation revision before consolidation. They describe those releases, not later runtime behavior.
<!-- /chapter:upgrading -->

<!-- chapter:typescript-backend -->
<a id="typescript-backend"></a>

## TypeScript / NestJS backend

This is the Prisma-based backend, released independently of the Go module. Its APIs, policies and event guarantees differ from the Go APIs above. The Go TypeScript/React client is a separate package family.

**Write your Prisma schema. The backend comes alive, and it defends itself.**

Golem builds a complete GraphQL API from your Prisma schema at runtime. You write no resolvers, no DTOs, no services, and there are no generated classes to maintain. Every model gets queries with filtering and pagination, mutations with nested writes, and live subscriptions. If you connect an authorization adapter, a single set of CASL rules is enforced on every row, every column, and every relation hop, across every entry point.

<a id="typescript-backend-why-golem"></a>

### Why Golem

The typical NestJS + Prisma + GraphQL app repeats the same four layers per model: a resolver that calls a service that calls a repository that calls Prisma, plus input types for each operation. None of that code is your product. Golem replaces all of it with one generator line and one module import, and keeps the parts that are your product (business rules, custom operations, access policy) in first-class, typed extension points.

What you get out of the box:

- Queries: `user(where)`, `users(where, orderBy, take, skip)` with Prisma-style filter inputs
- Mutations: `createUser`, `updateUser`, `deleteUser`, `updateManyUsers`, `deleteManyUsers`, with relation-first nested writes (`connect`, `create`, `disconnect`)
- Subscriptions: a per-model event stream with the subscriber's own field selection
- One Prisma client with two stances: plain calls act as the system, `forContext(ctx)` calls act as the caller with policy enforced
- An authorization kernel: row constraints compiled into queries, transactional write verification, field-level write permissions, per-row read masking
- Typed hooks and typed custom operations
- Guardrails on by default: query depth limits, take limits, no foreign-key forgery, no existence leaks

<a id="typescript-backend-packages"></a>

### Packages

| Package | Role |
|---|---|
| `@eleven-am/golem` | The NestJS module. This is the one you import. |
| `@eleven-am/golem-core` | Engine, policy kernel, and schema builder. Framework-free. |
| `@eleven-am/golem-generator` | The Prisma generator (`provider = "golem"`). |
| `@eleven-am/golem-authorizer` | Authorization adapter for `@eleven-am/authorizer` (CASL). Optional. |
| `@eleven-am/golem-queue` | Durable NestJS job queue with leases, retries, cancellation, and Prisma persistence. Optional. |
| `@eleven-am/golem-render` | Nest-native SPA hosting and route-specific link-preview metadata. Optional. |

Upgrading an existing application? Follow the [migration guide](#typescript-upgrades).

Golem supports the `postgresql` and `sqlite` datasource providers. The generator refuses any other `datasource` provider, and the engine refuses one at startup, naming it; there is no fallback path for other databases.

<a id="typescript-backend-quickstart"></a>

### Quickstart

This setup assumes a NestJS application with Prisma 7 and the PostgreSQL driver adapter configured. Set the datasource URL in `prisma.config.ts`; the checked-in [demo configuration](../typescript/apps/demo/prisma.config.ts) shows the Prisma configuration shape for SQLite. Use your PostgreSQL URL for this example.

**1. Install**

```bash
npm i @eleven-am/golem @eleven-am/golem-core
npm i -D @eleven-am/golem-generator
```

**2. Add the generator and required internal model to `schema.prisma`**

```prisma
datasource db {
  provider = "postgresql"
}

generator client {
  provider = "prisma-client"
  output   = "../src/generated/prisma"
}

generator golem {
  provider = "golem"
  output   = "../src/generated/golem"
}

// Required internal coordination table; keep this shape in your Prisma schema.
model GolemUpsertGuard {
  stripe Int    @id
  seq    BigInt @default(0)

  @@map("_golem_upsert_guard")
}

model User {
  id       String    @id @default(cuid())
  email    String    @unique
  articles Article[]
}

model Article {
  id        String    @id @default(cuid())
  title     String
  content   String?
  published Boolean   @default(false)
  savedAt   DateTime  @default(now())
  author    User      @relation(fields: [authorId], references: [id])
  authorId  String
}
```

**3. Migrate and generate**

Apply the provider-specific upsert-guard migration shipped under `@eleven-am/golem-core/prisma/` before startup. Include it in your application migration history; `prisma generate` does not create the table. PostgreSQL startup prepares its stripe rows. See [TypeScript upgrades](#typescript-upgrades) for the artifacts and deployment order.

```bash
npx prisma generate
```

Three artifacts land in `src/generated/golem`: the datamodel, a fully typed instrumented Prisma client (`GolemPrismaService`), and a type map for hooks and programmatic calls.

**4. Wire the module**

```typescript
@Module({
  imports: [
    GolemModule.forRoot({
      client: GolemPrismaService,
      prismaOptions: { adapter: new PrismaPg({ connectionString: process.env.DATABASE_URL }) },
      datamodel: getDatamodel(),
      defaults: { maxTake: 100 },
      models: {
        Article: { subscriptions: true },
      },
    }),
    GraphQLModule.forRootAsync<ApolloDriverConfig>({
      driver: ApolloDriver,
      inject: [GOLEM_GRAPHQL],
      useFactory: (golem: GolemGraphQLArtifacts) => ({
        typeDefs: golem.typeDefs,
        transformResolvers: golem.transformResolvers,
        fieldResolverEnhancers: golem.fieldResolverEnhancers,
        subscriptions: { 'graphql-ws': true },
      }),
    }),
  ],
})
export class AppModule {}
```

If you set `GraphQLModule`'s `context`, it must be a function — `context: ({ req }) => ({ req })` — never a static object. `@nestjs/apollo` reuses a static object across requests with the first caller's `req` still attached, so every later caller would be served as the first one. Golem detects a context that belongs to another request and fails the operation instead of answering with the wrong caller's rows.

**5. Use the API**

```graphql
mutation {
  createArticle(data: {
    title: "Hello Golem"
    author: { connect: { id: "u1" } }
  }) { id savedAt }
}

query {
  articles(
    where: { published: { equals: true } }
    orderBy: [{ savedAt: desc }]
    take: 20
  ) { title author { email } }
}

subscription {
  articleEvents { type id entity { title published } }
}
```

<a id="typescript-backend-the-client-one-object-two-stances"></a>

### The client: one object, two stances

The generated `GolemPrismaService` is a real Prisma client. It carries the full Prisma API and full Prisma typing, and every write through it publishes subscription events automatically.

```typescript
// Act as the system. Full Prisma, no policy. For workers, jobs, seeds.
await this.prisma.article.update({ where: { id }, data: { status: 'READY' } });

// Act as the caller. Same typing, policy enforced.
await this.prisma.forContext(ctx).article.update({ where: { id }, data: { title } });
```

`forContext(ctx)` returns a generated, Prisma-inferred policy delegate: selections still narrow result types, but only arguments whose semantics Golem implements are present. The supported operations are `findUnique`, `findFirst`, `findMany`, `create`, `update`, `updateMany`, `upsert`, `delete`, `deleteMany`, numeric `count`, `aggregate`, and `groupBy`, plus an interactive `$transaction`. Unsupported forms such as field-selected `count`, batch mutation `limit`, raw queries, and newly added Prisma arguments remain compile errors until Golem gives them explicit policy semantics. If an argument type-checks on this surface, Golem forwards it after applying policy rather than silently ignoring it.

All three read operations merge the caller's read constraint into the query exactly as `findMany` does. A field is aggregable when it is readable on every row the merged constraint matches: under a model-level scoped grant every field qualifies, because the constraint already excludes every row the caller cannot read. Conditions the constraint does not discharge — a field-level rule or an inverted rule — are rejected by name, since a single aggregate value cannot carry per-row masking.

`forContext(ctx).$transaction(fn)` runs an interactive transaction in which every operation is the caller's policy-enforced op bound to the transaction connection. A policy denial anywhere in the callback aborts and rolls the whole transaction back, and buffered subscription events publish only if it commits. Only the callback form exists; there is no sequential-array form on the bound client.

```typescript
await this.prisma.forContext(ctx).$transaction(async (tx) => {
  const author = await tx.user.create({ data: { email } });
  await tx.post.create({ data: { title, author: { connect: { id: author.id } } } });
});
```

This boundary also applies to hooks and field configuration. Generated GraphQL operations and `forContext(ctx)` enter `GolemEngine`, so they run hooks and caller authorization. Plain delegate calls intentionally bypass both as system-level Prisma access, although their writes still publish configured subscription events. `hidden`, `readOnly`, and `writeOnly` control the generated GraphQL schema; they do not remove fields from Prisma's generated types or impose field restrictions on `forContext(ctx)` or plain delegates. Use CASL when programmatic callers also need field-level enforcement.

<a id="typescript-backend-authorization"></a>

### Authorization

Golem does not implement its own permission language. It enforces [CASL](https://casl.js.org) rules provided through `@eleven-am/authorizer`, so your access policy lives in one rules provider and reads like a specification:

```typescript
@Authorizer()
export class AppRules implements WillAuthorize {
  forUser(user: SessionUser, { can, cannot }: AbilityBuilder<ResolvedAbility>) {
    can(['read', 'create', 'update', 'delete'], 'Article', { userId: user.id });
    can('create', 'Article', { type: 'PERSONAL' });
    can('update', 'Post', ['published']);
    cannot('read', 'User', ['phone']);
    can('read', 'User', ['phone'], { id: user.id });
  }
}
```

```typescript
GolemModule.forRoot({
  // ...
  authorization: GolemAuthorizationAdapter,
})
```

When authorization is configured, transactional write verification and read-field enforcement are enabled by default. Either can still be disabled explicitly for a deliberately row-policy-only integration.

Each rule shape maps to a specific enforcement mechanism:

| Rule | Enforcement |
|---|---|
| `can('read', 'Article', { userId })` | Compiled into the SQL `where` of every read, including relation traversals. Rows outside the ability do not exist as far as the caller can tell. |
| `can('update', 'Article', { userId })` | The update or delete statement itself carries the constraint, so authorisation and write are one atomic statement and a concurrent change cannot move the row out of policy in between. Updating someone else's row returns `NOT_FOUND`, identical to a missing row. No existence leaks. |
| `can('create', 'Article', { type: 'PERSONAL' })` | Transactional verification. The write executes, the real resulting row is read back and checked, and a denial rolls everything back. Dynamic defaults, `{ increment }`, and connect-by-any-unique-key are all handled exactly, because nothing is simulated. |
| `can('update', 'Post', ['published'])` | Field-level write permission by before/after column diff. Changing any other column is rejected with the column named. A no-op write to a restricted column passes. |
| `can('read', 'User', ['phone'], { id })` | Per-row read masking. Your own row shows the value, other rows show `null`. A field the caller could never read is rejected at request time by name. |

Additional guarantees:

- Nested writes are verified per touched model. A forbidden row cannot be smuggled through a relation envelope.
- A caller can link only to a row it can read. Every foreign key a caller writes, whether as a scalar such as `authorId` on `create`, `update`, `updateMany` or `upsert`, or through nested `connect`, `connectOrCreate`, `set` and `disconnect` at any depth, must name a row inside the caller's `read` ability. A target the caller cannot read is reported exactly like one that does not exist: `NOT_FOUND` with `<Model> not found`, so comparing an outcome with a random id reveals nothing. `connect`, `set` and `disconnect` require `read` on the target, not `update`, and a `set` or a to-one `disconnect: true` also requires `read` on every row it detaches, checked in the same transaction as the write: a linked row the caller cannot read is reported as not found and nothing changes; A write containing `connectOrCreate` runs in a transaction and decides its branch before it writes (see upsert below): an existing row is connected by its identity and must be readable by the caller, or the write is refused with `NOT_FOUND`; a missing row is created explicitly and, after the write, must be one the caller can read, or the whole write rolls back with `NOT_FOUND`; a row a concurrent writer inserts in the meantime makes that create fail with `CONFLICT` rather than be linked unchecked. A context-bound write that sets any link runs in a transaction, and each link is judged against the stored state after the write, inside that transaction, so a concurrent change to a target cannot slip a link through; on PostgreSQL a concurrent write that changes a linked target waits for the write to commit, or fails with `CONFLICT` where waiting could deadlock, while a write that only links to the same target is not blocked. Writes without links keep their usual path. Clients without a caller context and foreign-key values written by before hooks are not checked. For a caller with a context, a missing link target is now always `NOT_FOUND` as well; before, it surfaced as `BAD_USER_INPUT` for a foreign-key scalar, a raw provider error for a to-many `connect`, and silent success for `set`. Writing only some fields of a compound foreign key is refused with `BAD_USER_INPUT`, because the target cannot be identified; a compound foreign key with any member set to `null` references nothing and is not checked. Every caller, the unscoped client included, must write a foreign key as a value or `{ set: value }`; `increment`, `decrement`, `multiply` and `divide` on a foreign-key field are refused with `BAD_USER_INPUT` ("foreign key <Model>.<field> must be set to a value, not changed arithmetically"), and so is a write to only some fields of a compound foreign key, because Golem cannot identify, and so cannot lock, the row such a write links to. A nested `connectOrCreate` or `upsert` decides its branch under lock, outermost first, and Golem then writes the branch it decided explicitly: a `create` when the row is missing, and an `update` (or, for `connectOrCreate`, a `connect`) by the row's identity when it is present. Prisma therefore never re-decides a branch Golem did not check. Only the links of the branch that runs are checked, a branch nested under one that does not run is never decided or checked, and a row that appears or disappears around the decision makes the write fail with `CONFLICT`.
- Denied writes publish no events. Event publishing is transaction-aware.
- Subscriptions re-check the ability on every delivered event. Revoking a user takes effect mid-connection, without a reconnect.
- Enabling authorization makes the entire surface authenticated-only. Unauthenticated requests receive `UNAUTHENTICATED`.
- Conditional and inverted field rules are hydrated from their exact recursive condition trees, including `is`, `isNot`, `some`, `every`, `none`, `AND`, `OR`, and `NOT`. Policy-only fields and relations are stripped before results return. A dependency or condition shape that cannot be resolved through the generated datamodel fails closed before Prisma or an in-memory check can under-select it.

<a id="typescript-backend-hooks"></a>

### Hooks

Hooks run inside the engine, below the transport, so the same hook applies to GraphQL calls and `forContext` calls alike. Before-hooks can transform the request or veto it; after-hooks observe results.

```typescript
@GolemHooks('Article')
@Injectable()
export class ArticleHooks {
  constructor(private readonly sessions: SessionService, private readonly queue: ExtractionQueue) {}

  @BeforeCreate()
  async prepare(request: GolemRequest<'Article', 'create'>): Promise<GolemRequest<'Article', 'create'>> {
    const url = normalizeUrl(request.data.url);
    if (!isSafePublicUrl(url)) {
      throw new GolemValidationError('That URL cannot be saved');
    }
    const user = await this.sessions.userFromContext(request.context);
    return { ...request, data: { ...request.data, url, user: { connect: { id: user.id } } } };
  }

  @AfterCreate()
  async enqueue(article: GolemResult<'Article', 'create'>) {
    await this.queue.add({ articleId: article.id!, url: article.url! });
  }
}
```

`GolemRequest<'Article', 'create'>` resolves to Prisma's own input types, so `request.data` autocompletes and a misspelled field is a compile error. Hook classes are ordinary providers with full dependency injection, discovered automatically.

Before-hooks run sequentially in provider discovery order. Each hook receives the request returned by the preceding hook; returning `undefined` preserves the current request, and throwing stops the operation before Prisma runs. The final transformed request is used for authorization, nested-write checks, field checks, and the Prisma call. After-hooks run sequentially after the database operation and read-field processing succeed. They observe results and do not replace them.

| Engine operation | Before decorator | After decorator |
|---|---|---|
| `findUnique` / `findOne` | `@BeforeFindOne()` | `@AfterFindOne()` |
| `findFirst` | `@BeforeFindFirst()` | `@AfterFindFirst()` |
| `findMany` | `@BeforeFindMany()` | `@AfterFindMany()` |
| `create` | `@BeforeCreate()` | `@AfterCreate()` |
| `update` | `@BeforeUpdate()` | `@AfterUpdate()` |
| `delete` | `@BeforeDelete()` | `@AfterDelete()` |
| `updateMany` | `@BeforeUpdateMany()` | `@AfterUpdateMany()` |
| `deleteMany` | `@BeforeDeleteMany()` | `@AfterDeleteMany()` |

Every `upsert` decides its branch in one place, whether it is top-level or nested (including `connectOrCreate`) and whether it comes from GraphQL, `forContext`, the engine or the unscoped client. It takes Golem's bounded internal guard and reads the row its `where` selects; if another write creates or deletes that row while the upsert decides, the write is refused with `CONFLICT`. The decided branch is then written as an explicit create or update, never as a native Prisma upsert, so its nested writes and link checks are exactly the ones that branch needs, and a concurrent insert makes the create fail with `CONFLICT` instead of silently switching branches. A typed canonical form of the model and selector is hashed into one of 4,096 stripes by default (`defaults.upsertGuardStripes`); the selector itself is never persisted. On PostgreSQL every stripe row is created once when the module starts (`prepareUpsertGuard`, which applications running the engine outside Nest must call themselves), and a stripe that was never prepared is refused rather than skipped. The guard table is addressed through the datamodel the generator emits, so with `multiSchema` it is qualified with its model's `@@schema` exactly like every other Golem table. On SQLite the guard is a write, and SQLite serializes writers. Two upserts of the same absent row therefore serialize: one creates, the other updates. Context-aware upserts additionally run exactly one create or update pipeline, so exactly that branch's hooks and its truthful `CREATED`/`UPDATED` event run once after commit. SQLite caller-owned transactions that already read before acquiring the guard may be rejected with stable `CONFLICT`. Writers outside the generated client do not take the guard.

An upsert's create input must carry the target's identity: every unique selector in `where` must be set in `create` to the same value, as a scalar or through a relation `connect` on that key, or the upsert is refused up front with `BAD_USER_INPUT` before any query runs, whichever branch would have run. The same check runs again on the create data a before-create hook returns, immediately before the create executes. This is deliberately stricter than refusing only when the create branch runs: it rejects malformed input at the boundary instead of depending on whether the row exists. `upsert({ where: { id: 5 }, create: { id: 6 } })` and a create input that leaves the id to a default are both refused. The same rule applies to nested `upsert` and to `connectOrCreate` at any depth inside a create or update: a `connectOrCreate` whose `create` does not reproduce its `where`, such as `where: { id: "requested" }` with the id left to `@default(cuid())`, is refused with `BAD_USER_INPUT` ("connectOrCreate create input does not set the target selector id on Post") before any query, because it would create a new row on every call. A selector field may also be satisfied by the parent the write is nested under, when the foreign key it names points back at that parent and the parent's value is known from its create data or its `where`. A to-one nested upsert has no selector and nothing to check. It covers every Golem path (GraphQL, `forContext`, interactive transactions and the engine itself); a plain Prisma `upsert` on the unscoped client is raw Prisma and keeps Prisma's own semantics.

<a id="typescript-backend-credentials-and-write-only-fields"></a>

#### Credentials and write-only fields

Use `writeOnly` when GraphQL must accept a secret without ever exposing it through generated read surfaces. The hook can replace the accepted value before authorization and persistence:

```typescript
GolemModule.forRoot({
  // ...
  models: {
    User: { writeOnly: ['password'], immutable: ['password'] },
  },
});

@GolemHooks('User')
@Injectable()
export class UserHooks {
  constructor(private readonly passwords: PasswordHasher) {}

  @BeforeCreate()
  async hashPassword(request: GolemRequest<'User', 'create'>) {
    return {
      ...request,
      data: {
        ...request.data,
        password: await this.passwords.hash(request.data.password),
      },
    };
  }
}
```

Here `password` exists in `UserCreateInput` but not in `User`, filters, ordering, or unique selectors. Because it is also `immutable`, generated update inputs omit it. CASL must still permit the transformed write. This is a GraphQL schema guarantee, not encryption by itself and not a restriction on `forContext()` or system-level Prisma access.

<a id="typescript-backend-aggregation-and-analytical-dimensions"></a>

### Aggregation and analytical dimensions

Models that opt into `aggregations` receive policy-scoped `aggregate` and `groupBy` operations through GraphQL and `forContext(ctx)`. The engine merges the same read constraint used by ordinary reads before Prisma aggregates. GraphQL generates separate sum, average, minimum, and maximum value objects because Prisma's result type depends on both the source scalar and the operation:

- `BigInt` sum/min/max use the exact `BigInt` scalar and serialize as strings; Prisma's BigInt average is a `Float`.
- `Decimal` measures use the exact `Decimal` scalar and serialize the Prisma Decimal value as a string.
- `Int` min/max remain `Int`, while Int sum/average use `Float` because a sum can exceed GraphQL Int's 32-bit range.
- `forContext()` returns Prisma-native `bigint`, `Decimal`, and `number` values unchanged.
- Empty and nullable measure results stay `null`; they are never changed to zero.

Ordinary `groupBy` retains Prisma's local-column shape. Relation dimensions use a separately named, deliberately bounded operation and must be configured explicitly:

```ts
models: {
  Play: {
    aggregations: {
      dimensions: ['albumId'],
      relationDimensions: {
        artistCountry: {
          path: ['track', 'primaryArtist'],
          field: 'country',
        },
      },
      measures: ['msPlayed'],
      maxIntermediateGroups: 10_000,
      maxGroups: 100,
    },
  },
}
```

GraphQL exposes this as `playsRelationGrouped`, separately from `playsGrouped`:

```graphql
query {
  playsRelationGrouped(
    by: [albumId, artistCountry]
    measures: { count: true, sum: [msPlayed], avg: [msPlayed] }
    orderBy: { sum: { msPlayed: desc } }
    take: 20
  ) {
    key { albumId artistCountry }
    count
    sum { msPlayed }
    avg { msPlayed }
  }
}
```

The programmatic counterpart is `forContext(ctx).play.relationGroupBy()` (also present on its transaction view and `GolemEngine`), typed separately from Prisma-shaped `groupBy`. Golem first groups authorized root facts by local dimensions and relation keys, then policy-fetches every configured to-one hop and merges by the terminal value. A root fact whose target is missing or not visible uses inner-join semantics and contributes nothing. Averages are rebuilt from total sum and total non-null count, never from averages. The complete intermediate set is inspected against `maxIntermediateGroups` before final `having`, ordering, `skip`, or `take`; final ordering receives deterministic key tie-breakers and output is capped by `maxGroups`. Paths must be one or more explicit forward to-one relations. To-many, many-to-many, reverse-only, distinct relation paths, related-model measures, unreadable keys, and unreadable terminal fields fail at configuration or evaluation. Use `$scoped()` for analytical shapes outside this contract.

<a id="typescript-backend-extensions"></a>

### Extensions

Extensions add what the generator cannot know about: computed fields and custom operations. They are declared explicitly in `forRoot` because they change the schema shape.

```typescript
@Injectable()
export class ArticleExtension {
  constructor(private readonly prisma: GolemPrismaService) {}

  @ComputedField('Article', { type: 'String!', requires: ['url'] })
  domain(@Parent() article: Pick<Article, 'url'>): string {
    return new URL(article.url).hostname;
  }

  @UseGuards(AuthorizationGuard)
  @CanPerform({ action: 'read', subject: 'Article' })
  @CustomQuery({ type: '[Article!]!', args: { term: 'String!' } })
  searchArticles(@Args() args: { term: string }, @Context() ctx: unknown) {
    return this.prisma.forContext(ctx).article.findMany({
      where: { title: { contains: args.term } },
    });
  }
}
```

Import `ComputedField` from the generated Golem module so the model and every entry in `requires` are checked against the Prisma datamodel. The `requires` list feeds the query planner: those columns are fetched only when the computed field is requested. Computed fields and custom operations are mounted as real Nest GraphQL resolvers, so Nest guards, pipes, interceptors, filters, request-scoped providers, and parameter decorators apply normally. Pass `golem.fieldResolverEnhancers` into `GraphQLModule` alongside `typeDefs` and `transformResolvers`; Nest disables guards, interceptors, and filters on field resolvers unless that option is enabled. Existing computed fields written as positional callbacks must migrate from `method(parent)` to `method(@Parent() parent)`. They inherit row and field policy when they use `forContext`.

A computed field that queries for other rows costs one query per parent row. Declare it with `@BatchedComputedField` instead and it costs one query per page: the decorated method is handed every parent key resolved in the same tick and returns a map from key to value.

```typescript
@BatchedComputedField('Article', { type: 'Int!', key: 'id' })
async commentCount(keys: readonly string[], ctx: unknown): Promise<Map<string, number>> {
  const groups = await this.prisma.forContext(ctx).comment.groupBy({
    by: ['articleId'],
    where: { articleId: { in: [...keys] } },
    _count: true,
  });
  const counts = new Map(keys.map((key) => [key, 0]));
  for (const group of groups) {
    counts.set(group.articleId, group._count);
  }
  return counts;
}
```

`key` names the parent column that identifies the row (or a function of the parent for a compound key); it is added to `requires`, so the planner fetches it. The method may also return an array aligned with `keys`, with an `Error` in any slot that failed. It receives the same `ctx` the per-row form receives, so `forContext(ctx)` enforces the caller's policy exactly as before, and the declared field `args` arrive as its third parameter — each distinct set of arguments batches on its own.

Batching is scoped to one request and, within it, to one execution. The loader is keyed by the GraphQL context object, so two requests never share a batch or a cached value and nothing survives the response; it is keyed again by the execution's root value, so a subscription — which holds one context open for the life of the connection — loads afresh for every event instead of serving the first event's answer forever. A parent whose key is null resolves to null without joining the batch. If the batch throws, every parent waiting on it receives that error. A computed field without a batch loader is untouched and still resolves per row.

<a id="typescript-backend-configuration-reference"></a>

### Configuration reference

```typescript
GolemModule.forRoot({
  client: GolemPrismaService,          // the generated client class
  prismaOptions: { adapter },          // passed to the PrismaClient constructor
  datamodel: getDatamodel(),           // from the generated artifacts
  pubSub: REDIS_PUBSUB,                // any graphql-subscriptions PubSubEngine; optional
  authorization: GolemAuthorizationAdapter,  // optional
  subscription: { queueCapacity: 64, observer }, // bounded local fan-out
  batchEvents: { maxRows: 1_000, maxPayloadBytes: 1_048_576 },
  extensions: [ArticleExtension],      // optional
  defaults: { /* global posture */ },
  models: { /* per-model overrides */ },
})
```

**`defaults`**

| Option | Default | Meaning |
|---|---|---|
| `operations` | all eight | Which CRUD/read operations exist, globally |
| `subscriptions` | `false` | Event streams per model |
| `maxTake` | unlimited | `take` above this is rejected with `BAD_USER_INPUT`, never silently clamped |
| `maxGroups` | relation aggregation: `100` | Final relation-aware group output cap; also the optional GraphQL-local `groupBy` cap |
| `maxIntermediateGroups` | `10,000` | Complete first-phase cap for relation-aware aggregation |
| `maxDepth` | `5` | Maximum relation nesting per query, rejected beyond |
| `checkWriteResults` | `true` with authorization | Transactional write verification and field-level write permissions |
| `checkReadFields` | `true` with authorization | Read-side field rejection and per-row masking |
| `upsertGuardStripes` | `4,096` | Bounded serialization stripes for context-aware upsert |

**`models`** (per model, overrides `defaults`)

| Option | Meaning |
|---|---|
| `false` | Model is removed from the generated GraphQL surface, but stays reachable under policy through `forContext(ctx)` and plain delegate access. Relation fields on other models that point at it are pruned with it — see below |
| `operations: [...]` | Allowlist; disabled operations do not exist in the schema |
| `hidden: [...]` | Field removed from every schema surface: types, filters, inputs |
| `immutable: [...]` | Field accepted on create, absent from update inputs |
| `readOnly: [...]` | Field remains readable, filterable, orderable, and uniquely selectable, but is absent from create and update inputs |
| `writeOnly: [...]` | Field is accepted by create and update inputs but absent from outputs, filters, ordering, and unique selectors |
| `subscriptions` | Event stream for this model |
| `maxTake` | Per-model take limit |
| `aggregations` | `true`, or local `dimensions`/`measures` plus optional named `relationDimensions`, `maxIntermediateGroups`, and `maxGroups`; relation dimensions add a separately named operation |

Field behavior is explicit across every generated GraphQL surface, including nested inputs:

| Configuration | Output/read | Filter/order/unique | Create input | Update input |
|---|---:|---:|---:|---:|
| `normal` | yes | yes | yes | yes |
| `immutable` | yes | yes | yes | no |
| `readOnly` | yes | yes | no | no |
| `writeOnly` | no | no | yes | yes |
| `writeOnly` + `immutable` | no | no | yes | no |
| `hidden` | no | no | no | no |

Configuration is validated while the schema is built. Unknown fields, write-only primary keys or relations, and conflicting access modes fail startup with the model and field named. `writeOnly` plus `immutable` is the supported combined mode; other overlapping access modes are rejected rather than resolved implicitly.

When authorization is present and `checkReadFields` is enabled, every visible scalar and enum output field is nullable even when its database column is required. A field check may truthfully mask that value to `null`; the containing object, relation list, and event remain intact. Input requiredness still follows Prisma, relation list structure is unchanged, and event identities remain non-null after event authorization. Disable field checks explicitly if you need the old Prisma-required output nullability, then regenerate GraphQL client types.

<a id="typescript-backend-errors"></a>

### Errors

All failures surface as GraphQL errors with stable extension codes and no Prisma internals:

| Code | Meaning |
|---|---|
| `BAD_USER_INPUT` | Validation, hook veto, take or depth limit exceeded, relation constraint violation, a string containing a NUL byte, an upsert create input that does not set its target |
| `NOT_FOUND` | Row missing, or existing but outside the caller's ability |
| `CONFLICT` | Unique constraint violation |
| `UNAUTHENTICATED` | No resolvable user while authorization is enabled |
| `FORBIDDEN` | The ability denies the action, row, or named field |

A context-bound update may not change the fields that identify a row (its primary key, or the unique field Golem identifies it by when it has none), at the root or in a nested update; such an update is refused with `BAD_USER_INPUT` before any query. When a foreign key is part of the identity, a write through its relation is refused only if it can reassign or clear that key (`connect`, `disconnect`, `create`, `connectOrCreate`, `upsert`, `delete`) or if a nested `update` rewrites the related row's referenced key, directly or through a foreign key of its own at any depth (for example a `connect` that moves the related row's key, which `onUpdate: Cascade` would carry back); a nested `update` that changes other fields along the chain is allowed. A string or JSON object key containing a NUL byte (`\u0000`) is refused with `BAD_USER_INPUT` before the statement that carries it runs, wherever it appears: a write payload, a filter, a JSON value or a raw query parameter, on the context-bound client, through GraphQL, and on the unscoped client. On SQLite such strings used to be stored and then matched predicates incorrectly, because SQLite's string functions stop at NUL; PostgreSQL rejected them with a provider error. Rows already stored that way still read.

<a id="typescript-backend-subscriptions-in-detail"></a>

### Subscriptions in detail

Each opted-in model gets `articleEvents(where?)` emitting `{ type: CREATED | UPDATED | DELETED, id, entity }`. A single reference-counted local hub owns the event-bus iterator for each model/schema instance, opening it for the first subscriber and closing it after the last. Delivery still re-fetches with the subscriber's current context, selection, filter, and ability. Evaluation is shared only within one event when the exact context object and canonical filter/selection match; it is never shared across callers.

Each consumer queue holds 64 events by default. Set `subscription.queueCapacity` to change it. A slow consumer that fills the queue is disconnected with `GOLEM_SUBSCRIPTION_OVERFLOW`; no event is silently dropped. `GolemSubscriptionObserver` reports active subscriptions, received events, evaluations and latency, deliveries, suppression reasons, queue depth, and overflow disconnects. The event transport is versioned and safely round-trips BigInt, Decimal, Date, bytes, deletion snapshots, composite identities, and batch envelopes through JSON-like buses.

Single-column models keep a scalar event `id`. Composite-`@@id` models expose a model-specific non-null identity object containing the ordered key components. Named and unnamed compound `@@id` and `@@unique` selectors are available in generated find-one, update, delete, upsert, connect, connect-or-create, and nested update/delete inputs.

Top-level `updateMany` and `deleteMany` emit deterministic per-row events for subscribable models across GraphQL, `forContext`, plain generated delegates, and generated interactive transactions. The default limits are 1,000 rows and 1 MiB encoded payload; exceeding either rejects before mutation and never truncates. Eventful `updateMany` refuses primary-key updates. `deleteMany` snapshots and deletes exactly the selected identities and rolls back on a count mismatch. Events remain buffered until commit and are discarded on rollback. Delivery is commit-aware and in-process, not durable or exactly-once: a process crash after the database commit but before publication can still lose events, and out-of-process writes remain invisible.

Cascaded deletes produce change events. A `deleteMany` honours its `limit`, deleting no more rows than it names (in primary-key order), and an argument Golem cannot honour is refused with `BAD_USER_INPUT` rather than ignored. Every delete through the generated client, whether `delete`, `deleteMany`, or a nested `delete`/`deleteMany` inside an `update` or `upsert`, and whether it comes from GraphQL, `forContext`, a transaction or the unscoped client, runs one path. It finds the rows it removes and every dependent the database will change through `onDelete: Cascade`, `SetNull` or `SetDefault`, in the same transaction, then deletes; if another write changes that set while the delete runs, the delete is refused with `CONFLICT` rather than acting on rows it did not check. On PostgreSQL, concurrent Golem writes over the same rows do not deadlock: `create`, `createMany`, `update`, `updateMany`, `upsert`, nested writes and deletes, single-row writes included, from GraphQL, `forContext` or the unscoped client. One completes, and the other waits for it or, where waiting could deadlock, is refused with `CONFLICT`; retry it. A write outside a transaction opens one for this, and an array `$transaction([...])` on the generated client runs its operations in order inside one interactive transaction, so they commit or roll back together. A nested `update`, `updateMany`, `delete` or `deleteMany` is refused with `CONFLICT` if another write changes the rows it matched while it runs. A dependent inserted while a delete of its parent is under way waits for the delete and then fails, instead of being removed unseen. SQLite already serializes writers. Each cascaded row then emits `DELETED` with its snapshot, and each row whose foreign key the database clears or resets emits `UPDATED`. Permission to delete the parent covers its dependents, as before, and subscribers still receive only the events their own read policy allows. A delete whose rows plus dependents would exceed `batchEvents.maxRows` (1,000 by default) is refused whole with `BAD_USER_INPUT`, before anything is deleted. The count includes every dependent, including rows the caller cannot read and rows of models nobody subscribes to, so the refusal can tell a caller that more dependents exist than the limit allows. Rows are identified by their primary key or, for a model without one, by its first required `@unique` field, else its first `@@unique` over required fields; every Golem path (link checks, upsert, verification, events and deletes) uses this one identity, and a delete from a model with neither is refused. On PostgreSQL with `multiSchema`, the cascade reads qualify each table with its model's `@@schema`. A subscribable model whose identity includes a foreign key declared `onDelete: SetNull` or `SetDefault` is refused when the module starts, because the cascade would rewrite the identity its change event must name, and the new value of a database default cannot be known. Regenerate to emit each relation's `onDelete` into the datamodel; Prisma's defaults (`SetNull` for an optional relation, `Restrict` for a required one) are emitted when none is declared.

<a id="typescript-backend-known-limitations"></a>

### Known limitations

Stated here because you will hit them eventually, and finding them in a README beats finding them in production:

- **Subscription evaluation remains policy-local.** One event-bus iterator fans out locally, but each distinct context/filter/selection group still performs its own policy-scoped evaluation. Golem deliberately does not share unrestricted rows or results across users.
- **Transactions.** Writes through the generated client are buffered until `prisma.$transaction` commits and discarded on rollback. `forContext(ctx).$transaction(fn)` extends this to policy-enforced callers with the callback form only — there is no sequential-array (`$transaction([...])`) form on the bound client. Out-of-process transactions remain outside Golem's event boundary.
- **Aggregates are read-only and hook-free.** `count`, `aggregate`, and `groupBy` merge the caller's read constraint but run no `before`/`after` hooks. They fail closed on any field that is not readable on every row the merged constraint matches — a `never` field, a field-level condition, or an inverted rule — rejected by name, since one aggregate value cannot carry per-row masking. A model-level scoped grant discharges its own conditions, so every field on such a model aggregates normally.
- **Serialized upsert is cooperative.** The upsert guard serializes every Golem upsert using the same canonical model/selector. Writers outside the generated client and differently addressed selectors do not participate; a row they insert or delete while a Golem upsert decides is refused with `CONFLICT` rather than switching the branch. SQLite caller-owned transactions that already established an incompatible snapshot receive stable `CONFLICT`.
- **`maxGroups` bounds the generated GraphQL surface only.** With it set, a `groupBy` that supplies no `take` is fetched with a `take` of `maxGroups + 1` and refused if the extra group appears — bounded, and never silently truncated. An explicit `take` above the cap is refused outright. The programmatic `forContext` client is deliberately uncapped: a developer asking for every distinct group is not an anonymous caller asking for one. Note that Prisma requires any `orderBy` on a `groupBy` to use only fields present in `by`; when a `take` is in play and you supplied no ordering, Golem orders by the grouping keys so the page is deterministic.
- **Authorized schemas make scalar outputs nullable.** With field checks enabled, required database scalar/enum columns are nullable in GraphQL so a genuine per-row mask does not null-propagate through the containing object. Inputs remain Prisma-required where applicable.
- **`BigInt` columns serialize as strings** over GraphQL, since values can exceed `2^53` and JSON cannot carry a raw bigint. Inputs accept an integer string or an integer literal; fractional numbers, unsafe integer numbers, and non-numeric strings are rejected.
- **`Decimal` columns and Decimal aggregates serialize as strings** over GraphQL. Golem preserves the exact Prisma Decimal value; database/provider precision remains the database's responsibility (for example, SQLite numeric aggregation can already be approximate before Prisma returns it).
- **BigInt policy conditions are exact by default.** The default ability — used when your `Authenticator` leaves `abilityFactory` off — compares mixed `BigInt`/number operands exactly (fail-closed on non-numeric and `NaN` operands), so the in-memory checks — read field masking, transactional write verification, and the subscription delete re-check — are exact at any magnitude, with no `2^53` ceiling. If you override `abilityFactory`, build it with `createGolemAbility` from `@eleven-am/golem-authorizer`; the adapter verifies that its condition matcher agrees with Golem's operator table and refuses to boot when it does not. The row-level query filter compiles to SQL and was always exact. List-membership operators (`has`, `hasSome`, `hasEvery`) are BigInt-exact for mixed `BigInt`/number element pairs; non-numeric elements keep JavaScript `Array.includes` (`SameValueZero`) semantics.
- **Out-of-process writes** (another service, a SQL console) are invisible to the event stream.
- **`retrieveUser` runs per request** and per delivered subscription event. Verify a JWT or cache the lookup; only hit the database on purpose.
- **Nested/out-of-process batches are not captured.** Per-row batch events cover top-level generated GraphQL, `forContext`, plain generated delegates, and generated interactive transactions, for `createMany`, `createManyAndReturn`, `updateMany` and `updateManyAndReturn` as well as single-row writes. Every intercepted operation returns exactly what Prisma returns for it, with the caller's `select`, `include` and `omit` applied: a row, an array of rows, or `{ count }`. Nested creates and updates, and writes made outside the generated client, remain outside the event boundary. Nested deletes, and every row a delete cascades to, are captured.
- **Relation-aware aggregation is bounded to one forward to-one path.** It supports local measures and local plus terminal dimensions. To-many/many-to-many traversal, multiple relation paths, and related-model measures remain `$scoped()` territory. Ordinary programmatic local `groupBy` remains deliberately uncapped by GraphQL's `maxGroups`.
- **Excluding a model prunes the relations pointing at it.** A relation field cannot reference a GraphQL type that does not exist, so `Artist.genres` disappears from the surface when the join model `ArtistGenre` is `false`. This is silent, and you notice it as an absence rather than an error. Expose what you actually want through a `@ComputedField` — `Artist.genreNames: [String!]!` — which is the better API in any case, since a join table is a storage decision and not something an API should promise. Computed fields resolve per row, so a computed field backed by its own query costs one query per row of the parent list unless it is declared with `@BatchedComputedField`.

<a id="typescript-backend-license"></a>

### License

GPL-3.0
<!-- /chapter:typescript-backend -->

<!-- chapter:typescript-nest -->
<a id="typescript-nest"></a>

## NestJS integration

If you set `GraphQLModule`'s `context`, it must be a function — `context: ({ req }) => ({ req })` — never a static object. `@nestjs/apollo` reuses a static object across requests with the first caller's `req` still attached, so every later caller is served as the first one. Golem stamps each request through a middleware it registers itself and fails any operation whose context belongs to another request; a context deliberately shared across requests must carry `[golemSharedContext]: true` (the symbol is exported from `@eleven-am/golem`).

Since 0.4, computed fields are real Nest field resolvers. Import the typed `ComputedField` helper from the generated Golem module, use `@Parent()`/`@Context()`/`@Args()`, and pass `golem.fieldResolverEnhancers` to `GraphQLModule`. This enables ordinary Nest pipes, guards, interceptors, filters, and request-scoped providers. A computed field that queries for other rows can be declared with `BatchedComputedField` instead, which loads every parent key resolved in the same tick in one query, per request and through the caller's context. See the root migration guide for the positional-parent breaking change.

Production event bounds are configured at the module boundary:

```ts
GolemModule.forRoot({
  // client, datamodel, authorization, models ...
  subscription: { queueCapacity: 64, observer },
  batchEvents: { maxRows: 1_000, maxPayloadBytes: 1_048_576 },
});
```

One local hub owns each model's event-bus iterator and disconnects a full consumer with `GOLEM_SUBSCRIPTION_OVERFLOW`. Top-level batch events remain buffered until commit. `batchEvents.maxRows` also bounds every delete, counting the dependents its `onDelete` actions cascade to; each cascaded row emits its own event. Delivery is not durable or exactly-once. Startup validates the reserved `_golem_upsert_guard` table/delegate whether or not authorization is enabled and, on PostgreSQL, creates every guard stripe row; apply the provider artifact from `@eleven-am/golem-core` before deploying. Only the `postgresql` and `sqlite` datasource providers are accepted; any other is refused at startup.
<!-- /chapter:typescript-nest -->

<!-- chapter:typescript-core -->
<a id="typescript-core"></a>

## TypeScript engine contracts

The framework-free core of [Golem](https://github.com/eleven-am/golem): datamodel contracts, the operation engine, the policy kernel (row constraints, transactional write verification, field permissions, read masking), the schema builder, and the event machinery.

Install it alongside `@eleven-am/golem` rather than using it directly. See the [full guide](#typescript-backend).

The public core contracts include:

- `GolemEngine.relationGroupBy()` and `RelationGroupByRequest` for configured, bounded forward-to-one dimensions. Ordinary `groupBy` remains Prisma-shaped; `$scoped()` covers more complex analytics.
- `GolemSubscriptionOptions`/`GolemSubscriptionObserver` for bounded local fan-out (64 queued events per subscriber by default) and queue/evaluation/delivery metrics.
- `GolemBatchEventOptions` for the default 1,000-row/1-MiB top-level batch-event bounds.
- Versioned `encodeGolemEventMessage`/`decodeGolemEventMessage` transport support for BigInt, Decimal, Date, bytes, composite identities, snapshots, and batch envelopes.
- `GolemUpsertGuard` migration artifacts under `prisma/`; every application must apply the provider migration before startup, because every upsert takes the guard. On PostgreSQL the guard's stripe rows must exist before the first upsert: `@eleven-am/golem` creates them at startup, and an application running the engine without it calls `prepareUpsertGuard(client, provider, stripes)` itself.

Golem supports the `postgresql` and `sqlite` datasource providers only; the engine refuses any other.

When field read checks are active, generated scalar/enum outputs are nullable so a per-row mask is GraphQL-valid. Composite primary-key models use nested compound selectors and object event identities. See the root migration guide for codegen and migration steps.
<!-- /chapter:typescript-core -->

<!-- chapter:typescript-generator -->
<a id="typescript-generator"></a>

## Prisma generator contracts

The Prisma generator for [Golem](https://github.com/eleven-am/golem).

```prisma
generator golem {
  provider = "golem"
  output   = "../src/generated/golem"
}
```

Running `npx prisma generate` emits three artifacts next to your Prisma client: the serialized datamodel and its typed `ComputedField` decorator, the instrumented `GolemPrismaService` (commit-aware event publishing plus the policy-bound `forContext`), and the type map behind `GolemRequest` and `GolemResult`. The generated computed-field decorator rejects unknown models and unknown `requires` fields. Plain delegate calls intentionally act as the system; only `forContext(ctx)` enters Golem's policy and hook pipeline. See the [full guide](#typescript-backend).

The generated client wraps interactive transactions so top-level `updateMany`/`deleteMany` auxiliary selection, mutation, verification, and event buffering stay on the transaction connection. Commit publishes deterministic per-row events; rollback publishes none. An array `$transaction([...])` runs its operations in order inside one interactive transaction. Regenerate after every Golem upgrade. The generator refuses a schema whose `datasource` provider is not `postgresql` or `sqlite`, and one that lacks the `GolemUpsertGuard` model. The reserved `GolemUpsertGuard` Prisma model is intentionally omitted from generated Golem model typings and public policy surfaces even though its Prisma delegate remains available for startup validation and serialized context-aware upsert.
<!-- /chapter:typescript-generator -->

<!-- chapter:typescript-authorizer -->
<a id="typescript-authorizer"></a>

## CASL adapter

The authorization adapter connecting [Golem](https://github.com/eleven-am/golem) to [@eleven-am/authorizer](https://www.npmjs.com/package/@eleven-am/authorizer) (CASL).

One rules provider enforces every axis: row constraints compiled into queries, transactional write verification against real rows, field-level write permissions by column diff, and per-row read masking. The default ability (leave `abilityFactory` off your `Authenticator`) is exact for `BigInt` columns at any magnitude. If you override `abilityFactory`, build it with `createGolemAbility` from `@eleven-am/golem-authorizer` — the adapter verifies that its condition matcher agrees with Golem's operator table and refuses to boot otherwise. Rule conditions use Prisma `WhereInput` syntax and are validated before Golem evaluates them in memory or renders them as SQL. See the [full guide](#typescript-backend).
<!-- /chapter:typescript-authorizer -->

<!-- chapter:typescript-policy -->
<a id="typescript-policy"></a>

## Condition evaluator

Golem's own evaluator for CASL rule conditions, replacing the `@casl/prisma` interpreter.

One operator table defines every supported operator once: its JavaScript predicate, its parameterised SQL rendering, its null semantics, and its operand constraints. Both backends are generated from the same entry, so the in-memory check and the database filter cannot drift apart. Only the JavaScript side is wired up today; the SQL renderings are declarative and dialect-neutral until a SQL backend consumes them.

Conditions are two-valued: an object matches a condition or it does not, and there is no third "unknown" outcome. Null and absent values are ordinary values that fail to equal things, so `AND`, `OR` and `NOT` are classical boolean algebra over the leaves. Numeric comparison is exact across `number`, `bigint` and `Decimal`, including magnitudes past `Number.MAX_SAFE_INTEGER`.

Unsupported operators are rejected, never approximated. `validateConditions` reports every offending operator with its path so an application can refuse a policy at boot instead of silently under- or over-granting at request time.

```ts
import { compileConditions, validateConditions } from '@eleven-am/golem-policy';

const result = validateConditions({ title: { contains: 'x' } });
if (!result.supported) {
  throw new Error(result.issues[0].message);
}

const compiled = compileConditions({ post: { is: { authorId: 'u1' } } });
compiled.test({ post: { authorId: 'u1' } });
```

See the [full guide](#typescript-backend).
<!-- /chapter:typescript-policy -->

<!-- chapter:typescript-queue -->
<a id="typescript-queue"></a>

## TypeScript durable queue

Durable job queue for Golem apps. Lease-based claiming, retries with exponential backoff, per-handler concurrency, timeouts, and cooperative cancellation.

Built for the case where background work outlives a request: extraction pipelines, media processing, LLM calls — anything that must survive a restart and be retried honestly.

<a id="typescript-queue-install"></a>

### Install

```bash
npm install @eleven-am/golem-queue
```

<a id="typescript-queue-1-add-the-schema"></a>

### 1. Add the schema

Prisma has no package-level models, so the `Job` model lives in your schema. Copy `prisma/golem-queue.prisma` into your `schema.prisma` and migrate:

```prisma
enum JobStatus { PENDING RUNNING SUCCEEDED FAILED }

model Job {
  id             String    @id @default(cuid())
  type           String
  payload        String
  scopeType      String?
  scopeId        String?
  status         JobStatus @default(PENDING)
  runAt          DateTime  @default(now())
  attempts       Int       @default(0)
  maxAttempts    Int       @default(3)
  lastError      String?
  dedupeKey      String?   @unique
  leaseOwner     String?
  leaseExpiresAt DateTime?
  startedAt      DateTime?
  createdAt      DateTime  @default(now())
  updatedAt      DateTime  @updatedAt

  @@index([status, runAt])
  @@index([status, leaseExpiresAt])
  @@index([scopeType, scopeId, status])
  @@index([type, status, runAt])
  @@index([status, updatedAt])
}

model JobGuard {
  key         String    @id
  seq         BigInt    @default(0)
  windowStart DateTime?
  spent       BigInt    @default(0)
}
```

`JobGuard` is the serialization point for claim guards; rows are created as needed. `startedAt` records when a job most recently entered RUNNING and is written on every claim, so both are required even if you never declare a guard.

`[type, status, runAt]` serves the claim poll, which every handler runs every `pollIntervalMs`; `[status, updatedAt]` serves the retention sweep. Without them both scan every due job, or every row, on each pass.

<a id="typescript-queue-upgrading-an-existing-schema"></a>

#### Upgrading an existing schema

Schemas copied from 0.4.2 or earlier lack two indexes. Add `@@index([type, status, runAt])` and `@@index([status, updatedAt])` to `Job`, generate the migration with `prisma migrate dev`, and apply it with `prisma migrate deploy`. The queue runs correctly without them, only slower. A plain `CREATE INDEX` blocks writes to `Job` while it builds, so on a large Postgres table either apply the migration in a quiet window or build the indexes with `CREATE INDEX CONCURRENTLY`, which must run outside a transaction. Copies of an older README also carried `@@index([type, startedAt])`; nothing queries it, and it can be dropped.

<a id="typescript-queue-2-declare-your-job-types"></a>

### 2. Declare your job types

Job types are yours, not Prisma's, so nothing can generate them. Declare them once — anywhere in your program — and both ends of the queue are checked against it:

```ts
declare global {
  interface GolemRegister {
    jobs: {
      'article.extract': { articleId: string };
      'article.audio': { articleId: string; voiceId: string };
    };
  }
}

export {};
```

This is optional. Without it `add` accepts any string and any payload, exactly as before.

<a id="typescript-queue-3-write-a-handler"></a>

### 3. Write a handler

The decorator registers the handler; the interface types it — the same split as `@Authorizer` and `WillAuthorize` in `@eleven-am/authorizer`.

```ts
import { Injectable } from '@nestjs/common';
import { QueueHandler, RetryableJobError, TerminalJobError, type JobEvent, type JobWork } from '@eleven-am/golem-queue';

@QueueHandler({ type: 'article.extract', concurrency: 2, timeoutMs: 60_000 })
@Injectable()
export class ExtractHandler implements JobWork<'article.extract'> {
  async handle({ payload, signal, attempt, maxAttempts }: JobEvent<'article.extract'>) {
    const res = await fetch(url, { signal });          // honour the signal
    if (res.status === 429 && attempt < maxAttempts) throw new RetryableJobError('rate limited', 60_000, false);
    if (res.status === 404) throw new TerminalJobError('gone');
  }
}
```

`payload` is typed from the registration. `attempt` and `maxAttempts` let a handler behave differently on its last try — notify, degrade, or write a tombstone — rather than failing silently into the dead-letter state.

A `JobEvent` carries `id`, `type`, `payload`, `attempt`, `maxAttempts`, `scope`, and `signal`.

- **`TerminalJobError`** — fail now, don't retry.
- **`RetryableJobError(message, retryAfterMs?, consumesAttempt?)`** — `retryAfterMs` is a *floor* on the backoff; `consumesAttempt: false` retries without burning an attempt (right for capacity/rate limits).
- Anything else retries with exponential backoff + jitter until `maxAttempts`.
- `signal` aborts on timeout, cancellation, and shutdown — pass it to your I/O.

<a id="typescript-queue-one-job-at-a-time-per-scope"></a>

#### One job at a time per scope

Set `serializeByScope` when two jobs sharing a scope must never run together — one send per account, one export per workspace:

```ts
@QueueHandler({ type: 'message.send', concurrency: 8, serializeByScope: true })
```

Concurrency still applies across scopes: eight accounts send in parallel, one job per account at a time. Jobs with no scope are unaffected.

This is enforced in the store, not in the process, so it holds across N workers. A candidate is skipped when another job in its scope holds a **live** lease — `status = 'RUNNING' AND leaseExpiresAt > now`. A crashed worker's stranded row is not a blocker, or it would freeze its whole scope until lease recovery ran, which is precisely what recovery exists to fix.

`PrismaJobStore` needs `$transaction` and a `jobGuard` delegate on the client for this: the predicate and the claim have to be one transaction, and the guard row provides the serialization point that makes the predicate and claim atomic. Check-then-claim without that serialization races on every engine — SQLite's single writer serializes statements, not transactions with reads interleaved. If your `Job` model is `@@map`ped, no additional option is needed: `PrismaJobStore` uses Prisma delegates, so Prisma resolves the physical table name.

`InMemoryJobStore` enforces the same rule, so tests exercise production semantics.

<a id="typescript-queue-4-register-the-module"></a>

### 4. Register the module

```ts
import { GolemQueueModule, PrismaJobStore } from '@eleven-am/golem-queue';

GolemQueueModule.forRootAsync({
  imports: [PrismaModule],
  inject: [PrismaService, ConfigService],
  useFactory: (prisma: PrismaService, config: ConfigService) => ({
    store: new PrismaJobStore(prisma),
    pollIntervalMs: config.get('QUEUE_POLL_MS'),
    leaseDurationMs: config.get('QUEUE_LEASE_MS'),
  }),
  handlers: [ExtractHandler],
  observers: [PipelineStatusObserver],
})
```

The factory may return a bare store, or `{ store, ...options }` when the options themselves come from injected config. Anything it returns wins over statically supplied options.

`handlers` is optional, so a process that only enqueues — an API role in a split deployment — registers none and runs no dispatcher work.

<a id="typescript-queue-5-enqueue"></a>

### 5. Enqueue

```ts
await queue.add('article.extract', { articleId }, {
  scope: { type: 'Article', id: articleId },   // enables scope-wide cancellation
  dedupeKey: `article.extract:${articleId}`,   // returns false if already queued
});
```

Job types, dedupe keys, and supplied scope components must be non-empty; `maxAttempts` must be a positive integer and `runAt` a valid `Date`. Payloads must be JSON-safe objects. BigInt, circular references, non-finite numbers, functions, symbols, `undefined`, and unsupported object instances are rejected with `QueuePayloadError`; the error names the job type and failure category without printing the payload. Convert BigInt to a string before enqueueing.

Payloads can contain credentials, document content, or other sensitive application data. The persisted `Job.payload` and lifecycle observer transition `payload` contain the serialized value, so secure database access, logs, metrics, and observer destinations accordingly.

<a id="typescript-queue-enqueueing-inside-your-own-transaction"></a>

#### Enqueueing inside your own transaction

Queueing work usually has to be atomic with a change to your own tables — mark a row "processing" *and* enqueue the job, or neither. Pass a `store` bound to your transaction:

```ts
await prisma.$transaction(async (tx) => {
  await tx.article.update({ where: { id }, data: { audioStatus: 'PENDING' } });
  await queue.add('article.audio', { articleId: id }, {
    scope: { type: 'Article', id },
    store: jobStore.withClient(tx),
  });
});
```

Without this the two writes can diverge — the job runs while your row still says idle, or your row says "processing" for work that was never queued.

Cancellation:

```ts
await queue.cancelForScope({ type: 'Article', id }, 'article deleted');  // aborts in-flight + drops queued
await queue.cancelPendingForScope({ type: 'Article', id }, 'superseded'); // leaves running work alone
await queue.cancelByDedupeKeys('article.audio', keys, 'voice changed');
```

`cancelForScope` aborts work running **on this process** synchronously and deletes the rows before returning. A job running on *another* worker is not stopped instantly: that worker notices the row is gone on its next reconcile pass and aborts then, so the bound is one `pollIntervalMs`, not zero.

<a id="typescript-queue-long-running-jobs-lease-renewal"></a>

### Long-running jobs: lease renewal

By default a lease is written once at claim time and lasts `timeoutMs + leaseGraceMs`. That makes `timeoutMs` control two unrelated things — how long a job may run, and how long a crashed worker's job stays stuck — with no value that is right for both. A short timeout expires the lease *while the job is still running* and another worker picks it up, executing it twice; a long one means slow recovery after a crash.

Set `leaseDurationMs` and the dispatcher heartbeats instead:

```ts
GolemQueueModule.forRootAsync({
  useFactory: (prisma: PrismaService) => new PrismaJobStore(prisma),
  leaseDurationMs: 60_000,          // how fast a crash is detected
  // leaseRenewIntervalMs defaults to leaseDurationMs / 3
  handlers: [ExtractHandler],
})
```

Now the lease is short regardless of job length, and `timeoutMs` is purely a cap on total duration. A job may run for an hour while a crashed worker is still recovered in a minute.

**If the lease is lost, the handler is aborted.** The lease is lost when a renewal is refused — the row is no longer this worker's running job — or when the lease reaches its expiry without a renewal landing. A worker that lost its lease — deposed, paused past expiry, partitioned — stops working, because another worker may already own the job. Its `signal` fires exactly as it does for a timeout or a cancellation, so honouring the signal is what makes this safe. Without that abort, renewal would turn a bounded double-execution window into an unbounded one.

A renewal that errors — a dropped connection, a busy database — is not a lost lease. The lease it failed to extend is still valid, so the handler keeps running and the next interval tries again. Only if the errors last until the lease expires is the handler aborted.

Renewal engages only when `leaseDurationMs` is set **and** the store implements `renewLease`. Both bundled stores do. A custom store without it keeps the write-once behaviour, unchanged.

Renewal is fenced like every other lease write: it updates only a `RUNNING` row still owned by this worker whose lease has **not** already expired. An expired lease cannot be renewed even by its original owner, because another worker may have claimed it in the meantime.

<a id="typescript-queue-sharing-a-rate-limited-dependency"></a>

### Sharing a rate-limited dependency

Handler concurrency bounds one type. When several types draw on the same third-party API, what matters is the budget they share, and per-handler numbers cannot express it: four handlers at 30, 3, 1, and 1 permit 35 simultaneous callers against one budget, and each number looks defensible on its own.

Declare the pool in module config:

```ts
GolemQueueModule.forRootAsync({
  resources: {
    spotify: {
      concurrency: 4,                    // how many may run at once
      ratePerMinute: 180,                // how many may start per minute
      types: ['spotify-sync', 'track-hydrate', 'artist-enrich'],
      costs:     { 'spotify-sync': 2 },  // slots held while running, default 1
      rateCosts: { 'track-hydrate': 50 },// budget spent per start, default 1
    },
  },
  handlers: [SyncHandler, HydrateHandler, EnrichHandler],
})
```

Declare either dimension or both. They bound different things and are not interchangeable: **concurrency** limits how much runs at once, **rate** limits how much starts over time. A handler at `concurrency: 1` issuing batched calls in a tight loop respects a parallelism limit while exhausting a per-minute budget on its own, which is why the second exists.

The two costs are separate for the same reason. A job that holds one connection while making fifty calls occupies one slot and spends fifty units.

The budget is a counter on the pool's guard row, not a query over jobs. It records what was spent whether or not the job that spent it still exists, so finishing, retrying, pruning, and cancelling a job all leave the spend in place. **Every attempt of a retrying job is charged**, because every attempt called.

The window is fixed rather than sliding: it opens on the first start and resets once the minute has passed. A fixed window admits up to twice the budget across a boundary — 180 late in one window and 180 early in the next. Size it for the burst you can tolerate, not the average.

No handler changes. A job is claimed only when the summed cost of pool members already holding a live lease leaves room for it, evaluated inside the claiming transaction behind the pool's guard row — so two workers cannot both take the last slot.

**Membership is declared in config, not on the handler, because a worker counts only the types it is told about.** If `spotify-sync` runs in another process and is missing from `types`, every worker bounds the members it knows and the pool silently admits more than its limit. List every participating type, including those handled elsewhere; a type with no local handler is normal and accepted.

Occupancy is derived, never held. Nothing is acquired, so nothing leaks: a worker that dies frees its slots when its lease expires, through the same recovery that reclaims its job. A blocked job stays PENDING and unleased rather than sitting RUNNING and idle.

Each poll visits handlers in a rotating order, so a pool mate with high concurrency cannot take the whole pool every tick and starve a smaller one.

<a id="typescript-queue-what-a-pool-cannot-do"></a>

#### What a pool cannot do

A pool bounds jobs, not the requests made inside them. A handler that runs for hours polling in a loop is one job to the queue: a concurrency pool of four would run four of them while each still calls the API as often as it likes, and a rate budget counts its single start and nothing after. Neither dimension can see calls made inside a job.

Two ways out. Rate-limit those calls in the client that makes them, where they are visible. Or **model recurring work as recurring jobs** — if each poll is its own short job rather than one long loop, every call crosses a claim boundary and both dimensions bound it exactly. The second is usually the better shape: it also gives you retries, cancellation, and visibility per poll instead of per watcher.

<a id="typescript-queue-ordering-between-job-types"></a>

### Ordering between job types

Handlers are independent by default: any two types can run at the same time. Two different constraints are available when that is not safe, and they are not interchangeable.

<a id="typescript-queue-notwhilerunning--never-overlap"></a>

#### `notWhileRunning` — never overlap

```ts
@QueueHandler({ type: 'track-hydrate', notWhileRunning: ['history-import'] })
@QueueHandler({ type: 'history-import', notWhileRunning: ['track-hydrate'] })
```

Neither type is claimed while the other holds a live lease. **Declare it on both sides** — one-sided is almost always a bug, because the undeclared side is free to start while the declared one is already running, which is the overlap you were trying to prevent. When both handlers run in the same process a one-sided declaration is warned about at startup; across processes nothing can see it, so this is on you.

It is safe in both directions: whichever claims first runs, the other waits, and progress is always possible. An expired lease does not block, for the same reason it does not block a scope: that job is not finished, it is about to be reclaimed, and treating it as a blocker would stop the recovery that resolves it.

<a id="typescript-queue-waitsfor--drain-a-backlog-first"></a>

#### `waitsFor` — drain a backlog first

```ts
@QueueHandler({ type: 'nightly-rollup', waitsFor: ['event-ingest'] })
```

Blocks while the named types have *outstanding* work — a job that is due now, or one already running. It is a priority relation rather than an overlap rule: the rollup yields until ingestion has caught up.

Because it blocks on queued work and not just running work, it is **one-way by nature**. Two types that wait for each other could never start, so that is refused at startup. A job scheduled for later, or parked in retry backoff, does not block until it comes due.

<a id="typescript-queue-which-one"></a>

#### Which one

If your invariant is "these must not run at the same time", use `notWhileRunning` on both. If it is "do not start until that queue is empty", use `waitsFor`.

**Prefer an idempotent handler to either.** Both make an assumption enforceable, but a handler that can safely run at any time needs no assumption at all. Ordering constrains *when* work may run; idempotence removes the constraint, and it covers the case no ordering rule can — work arriving long after the job that would have blocked it.

If you do write a repair or backfill handler that processes a pending set in chunks, **the pending predicate must exclude rows the work cannot change.** A predicate that re-selects rows every pass without shrinking the set spins until the job times out. The symptom is a hung job rather than an error, and it does not reproduce against small fixtures.

<a id="typescript-queue-starvation"></a>

#### Starvation

Neither constraint can deadlock: `waitsFor` cycles are refused at startup, and `notWhileRunning` always lets one side proceed. Indefinite one-way starvation is still possible — a type waiting on work that never stops arriving never runs — so a handler blocked for many consecutive polls logs a warning naming what is blocking it, whether that is another type or a full resource pool.

The startup cycle check only sees handlers registered in **this** process. If two workers each register one side of a cycle, neither can detect it; `notWhileRunning` is the safe choice whenever the types are split across processes.

<a id="typescript-queue-inspecting-the-queue"></a>

### Inspecting the queue

The queue is the source of truth for "is this still running?" — useful for driving UI state and for operator tooling.

```ts
await queue.findForScope({ type: 'Article', id });        // JobSummary[]
await queue.find({ types: ['article.audio'], statuses: ['FAILED'], limit: 50 });
await queue.countByStatus();                              // { PENDING, RUNNING, SUCCEEDED, FAILED }
```

A `JobSummary` carries `id, type, scope, status, attempts, maxAttempts, lastError, runAt, createdAt, updatedAt`.

<a id="typescript-queue-dead-letters-and-retention"></a>

### Dead letters and retention

```ts
await queue.retryFailed({ scopeType: 'Article', scopeId: id });  // requeue with a fresh attempt budget
await queue.prune({ olderThan: new Date(Date.now() - WEEK) });   // drop old SUCCEEDED/FAILED rows
```

`retryFailed` resets `attempts` to 0 and clears `lastError`, so a dead job gets a full budget rather than immediately re-failing.
Without `limit`/`skip`, it retries every failed job matching the filter; it does not inherit the 100-row listing default. Supplying `limit` and/or `skip` makes the administrative operation explicitly paginated.

Terminal jobs accumulate forever unless you prune them. Pruning can also run automatically — **opt-in**, because a library should not silently delete your rows:

```ts
retention: { olderThanMs: 7 * 24 * 60 * 60 * 1000, sweepIntervalMs: 60_000 }
```

<a id="typescript-queue-delivery-semantics"></a>

### Delivery semantics

**Jobs run at least once.** A job can execute more than once when:

- a worker dies mid-job and its lease expires, so another worker recovers it;
- a handler ignores its `AbortSignal` past `abandonGraceMs` — the slot is freed and the job retried while the original work may still be running;
- the process is killed after the handler finished but before the completion write landed;
- the application shuts down while a handler is still running when `shutdownGraceMs` ends — its `signal` is aborted and the job is retried, even if the handler then resolves, because a handler that cleans up and returns on abort cannot be told apart from one that finished. Only a handler that resolved before the grace ran out is recorded as succeeded.

**Write your handlers to be idempotent.** `dedupeKey` only prevents duplicate *enqueues*; it does not make execution exactly-once.

<a id="typescript-queue-observing-lifecycle"></a>

### Observing lifecycle

Implement `JobLifecycleObserver` to react to work finishing — updating a status column, publishing a subscription event, emitting metrics:

```ts
onTransition({ jobId, type, scope, outcome, attempts, error, durationMs }) { ... }
```

Outcomes: `started`, `succeeded`, `retry-scheduled`, `failed-terminal`, `cancelled`.

<a id="typescript-queue-options"></a>

### Options

| Option | Default | Meaning |
|---|---|---|
| `pollIntervalMs` | `500` | claim loop interval |
| `baseBackoffMs` | `2000` | retry backoff base (`base * 2^attempts` + jitter) |
| `maxBackoffMs` | `300_000` | ceiling on that growth — an explicit `retryAfterMs` still wins above it |
| `leaseGraceMs` | `30_000` | lease headroom beyond `timeoutMs` |
| `abandonGraceMs` | `5000` | how long to wait for an aborted handler to exit before freeing its slot |
| `shutdownGraceMs` | `15_000` | how long in-flight work may finish on shutdown before being aborted |
| `defaultMaxAttempts` | `3` | attempts when not set per job |
| `workerId` | `randomUUID()` | lease owner identity |
| `retention` | *off* | opt-in pruning of terminal jobs |

Options are validated when the module is configured. Durations that represent intervals must be positive, grace/backoff durations cannot be negative, `maxBackoffMs` must be at least `baseBackoffMs`, attempts must be a positive integer, worker IDs cannot be blank, and an enabled retention policy must have a useful positive age/interval and non-empty status set.

<a id="typescript-queue-how-claiming-works"></a>

### How claiming works

Jobs are claimed with a compare-and-set `updateMany` guarded on `status` (and `leaseOwner` for completion), so **multiple workers are safe** — only one wins each job. A claim writes `leaseOwner` + `leaseExpiresAt` (`timeoutMs + leaseGraceMs`). If a worker dies, its lease expires and another worker recovers the job, incrementing `attempts`; once attempts are exhausted the job fails terminally rather than looping forever.

Cancellation across processes works through the database: the row is deleted, and each worker's reconcile pass aborts in-flight jobs it no longer owns. The in-process `JobCancellationRegistry` handles immediate aborts.

<a id="typescript-queue-testing-handlers"></a>

### Testing handlers

`InMemoryJobStore` implements the full `JobStore` port with no database:

```ts
const store = new InMemoryJobStore();
```

<a id="typescript-queue-custom-persistence"></a>

### Custom persistence

Implement the full `JobStore` port to back the queue with something other than Prisma. Its terminal writes — `complete`, `fail`, `retry` — and `findOwned` must match only a row that is `RUNNING`, owned by the worker, and whose `leaseExpiresAt` is still in the future, checked in the statement that writes, so a worker whose lease expired cannot record an outcome for a job another worker may have reclaimed.

The bundled `PrismaJobStore` claims work by polling for due candidates and winning each one with a compare-and-set update, through Prisma's own query API rather than hand-written SQL. That keeps it portable, and it is the right default for most apps. SQLite and Postgres are both exercised against real engines — see `test/sqlite` and `test/postgres`. MySQL is not supported.

Claims that carry a guard serialize on a shared row, which bounds their throughput. A pool serializes every claim of its member types on one row; `serializeByScope` serializes on the **scope type**, not the individual scope, so all scopes of a type share one claim serialization point across the fleet; `waitsFor` and `notWhileRunning` serialize per declared pair. Execution is unaffected — this bounds how fast work is *handed out*, not how much runs at once.

A claim that carries a guard — `serializeByScope`, `waitsFor`, `notWhileRunning`, or a resource pool — needs a serialization point, because the condition it tests is about *other* rows and engines do not serialize a read against a concurrent uncommitted write. Every competing claimer for a guard therefore writes one shared `JobGuard` row before reading anything, inside the transaction that claims. Writing first is what makes it work: competitors queue on a row lock rather than racing, and on SQLite it takes the writer lock up front instead of leaving a deferred reader that cannot upgrade. Guards are acquired in sorted order, so a claim needing several cannot deadlock against one acquiring the same guards in another order.

The trade-off is a round-trip per candidate, so claim throughput is bounded by the poll interval and the number of workers. If an app outgrows that, the escape hatch is a Postgres-native `JobStore` that claims a whole batch in one statement with `SELECT … FOR UPDATE SKIP LOCKED`. Nothing else has to change: the dispatcher only talks to the port.

<a id="typescript-queue-license"></a>

### License

GPL-3.0
<!-- /chapter:typescript-queue -->

<!-- chapter:typescript-render -->
<a id="typescript-render"></a>

## TypeScript frontend serving

Nest-native hosting for a compiled single-page application, with optional per-route Open Graph and Twitter metadata. It serves the frontend shell without taking over backend routes and lets ordinary Nest controllers resolve link-preview metadata.

<a id="typescript-render-install"></a>

### Install

```bash
npm install @eleven-am/golem-render
```

The package currently targets Nest's Express adapter.

<a id="typescript-render-host-a-frontend"></a>

### Host a frontend

```ts
import { Module } from '@nestjs/common';
import { GolemRenderModule } from '@eleven-am/golem-render';

@Module({
  imports: [
    GolemRenderModule.forRoot({
      client: 'dist-web/client',
    }),
  ],
})
export class AppModule {}
```

`client` can use any bundle layout; it only needs to contain `index.html`. The module:

- serves existing static files without directory indexes, redirects, or dotfiles;
- gives content-hashed assets a one-year immutable cache and other files `no-cache`;
- serves the shell for deep browser routes and HEAD requests;
- returns real 404s for missing assets, non-HTML requests, and non-GET requests;
- leaves `/api` and `/graphql` to backend handlers by default;
- fails at startup with the resolved absolute path when the bundle is missing.

Change the reserved prefixes or index filename explicitly when necessary:

```ts
GolemRenderModule.forRoot({
  client: 'frontend/output',
  index: 'shell.html',
  reserved: ['/api', '/graphql', '/health'],
})
```

<a id="typescript-render-default-metadata"></a>

### Default metadata

```ts
GolemRenderModule.forRoot({
  client: 'dist-web/client',
  defaults: {
    title: 'Readable',
    description: 'Save anything. Read it later. Listen anywhere.',
    image: '/og-default.png',
  },
})
```

The index file is read and parsed once at startup but is never modified. Each shell response is rendered in memory and sent with `Cache-Control: no-cache`. Relative URLs pass through unchanged. Set `baseUrl` only when they should be expanded to absolute URLs.

<a id="typescript-render-route-metadata"></a>

### Route metadata

`@RenderRoute` is a real Nest GET route. Put it on a method in an ordinary controller and use Nest's own parameter decorators, pipes, guards, interceptors, filters, and request-scoped dependencies.

```ts
import { Controller, Headers, Param } from '@nestjs/common';
import { GolemRenderModule, RenderRoute } from '@eleven-am/golem-render';

@Controller()
export class ArticleRenderController {
  constructor(private readonly prisma: PrismaService) {}

  @RenderRoute('/article/:id')
  async article(
    @Param('id') id: string,
    @Headers('user-agent') userAgent: string,
  ) {
    const article = await this.prisma.article.findUnique({
      where: { id },
      select: { title: true, excerpt: true, image: true, wideImage: true },
    });
    if (!article) return null;
    return {
      title: article.title,
      description: article.excerpt,
      image: /Twitterbot/i.test(userAgent ?? '') ? article.wideImage : article.image,
    };
  }
}
```

The application owns the query and its disclosure policy. Metadata routes do not add caller authorization or Golem policy scoping. This makes anonymous link unfurling possible; return only information that is safe for anyone holding the URL.

Returning `null` or `undefined`, or throwing, renders the configured defaults. Errors are logged and never turn the shell into a 500 response. The SPA shell renders without authentication and handles its own application authentication after boot.

Multiple URL patterns can share one method:

```ts
@RenderRoute('/m=:name')
@RenderRoute('/movie/:name')
movie(@Param('name') name: string) {
  return { title: name };
}
```

Nest owns route ordering. Declare wildcard routes last, as with any other Nest controller.

<a id="typescript-render-custom-metadata"></a>

### Custom metadata

```ts
return {
  title: article.title,
  description: article.excerpt,
  image: article.image,
  meta: {
    'og:type': 'article',
    'article:published_time': article.publishedAt.toISOString(),
    'twitter:card': 'summary',
    'og:description': null,
  },
};
```

`meta` is merged over the shorthand-generated tags. A `null` value deletes a tag. All names and values are HTML-escaped before interpolation. URL values are never derived from the request `Host` header.

<a id="typescript-render-license"></a>

### License

GPL-3.0

<a id="typescript-render-caching-is-the-resolvers-business"></a>

### Caching is the resolver's business

Metadata resolvers run on every matching request, and crawlers hit these URLs harder than users do. Golem does not cache them, and will not.

A resolver may branch on `@Query()`, on `Accept-Language`, or on `User-Agent` to serve a different image to different unfurlers. Only its author knows which inputs matter, so any cache key Golem chose would be wrong for someone — and wrong here means serving the wrong preview, which is worse than an extra query.

Because a render route is an ordinary Nest route, cache it however you already cache anything else: memoize inside the resolver, apply an interceptor, or reach for `cache-manager`. Golem stays out of the way.
<!-- /chapter:typescript-render -->

<!-- chapter:typescript-upgrades -->
<a id="typescript-upgrades"></a>

## TypeScript upgrade requirements

<a id="typescript-upgrades-migrating-to-070"></a>

### Migrating to 0.7.0

Upgrade `@eleven-am/golem-core`, `@eleven-am/golem-generator`,
`@eleven-am/golem` and `@eleven-am/golem-authorizer` together, to 0.7.0. A
mixed install does not fail: core is a regular dependency of the other three,
so leaving your own `@eleven-am/golem-core` at `^0.6.1` installs a second,
nested copy of core 0.7.0, and your app and the module then run different
cores. After upgrading, check that `npm ls @eleven-am/golem-core` lists a
single version. `@eleven-am/golem-policy` stays at
0.6.0 and `@eleven-am/golem-render` at 0.1.0. `@eleven-am/golem-queue` 0.5.0
is independent of these; see its own section below.

<a id="typescript-upgrades-required-steps"></a>

#### Required steps

**1. Use PostgreSQL or SQLite.** The generator refuses any other `datasource`
provider, and the engine refuses one at startup, naming it.

**2. Keep `GolemUpsertGuard` in the schema.** The generator now refuses a
schema without it, whether or not authorization is enabled. If you added it
for 0.6.x, nothing changes; otherwise copy it from
`typescript/packages/core/prisma/golem-core.prisma` (shipped in the package as
`@eleven-am/golem-core/prisma/golem-core.prisma`) or apply the provider SQL
under `prisma/migrations/`, and migrate.

**3. Regenerate.** `npx prisma generate` must run with generator 0.7.0 so the
datamodel carries `upsertGuard`, each model's `@@schema` and each relation's
`onDelete` action. With a 0.6.x datamodel, upserts can fail asking you to
regenerate, and deletes do not account for or publish events for their
cascaded dependents.

**4. Prepare the guard outside Nest.** Nest validates the guard table at
startup, with or without authorization, and on PostgreSQL creates every stripe
row. If you run the engine without `GolemModule`, call this once before
serving writes:

```ts
import { prepareUpsertGuard } from '@eleven-am/golem-core';

await prepareUpsertGuard(
  prisma as unknown as Record<string, unknown>,
  'postgresql',
  4_096,
);
```

Pass the same stripe count as `defaults.upsertGuardStripes`. On SQLite it does
nothing. On PostgreSQL an unprepared stripe is refused when an upsert reaches
it.

<a id="typescript-upgrades-what-fails-at-startup"></a>

#### What fails at startup

- A provider other than `postgresql` or `sqlite`, or a datamodel with none.
- A subscribable model whose identity includes a foreign key declared
  `onDelete: SetNull` or `SetDefault`: deleting the parent would rewrite the
  identity its change event names. Make the relation `Cascade` or `Restrict`,
  or stop subscribing to the model.

<a id="typescript-upgrades-what-fails-on-traffic"></a>

#### What fails on traffic

These are refused with `BAD_USER_INPUT` before any query runs. Search your
callers for them.

- **An upsert or `connectOrCreate` whose `create` does not reproduce its
  `where`.** Every unique selector in `where` must be set in `create` to the
  same value, directly, through a `connect` on that key's relation, or through
  the parent it is nested under. Copy the selector into `create`.
- **Arithmetic on a foreign key, or a partial compound foreign key**, from any
  caller including the unscoped client. Write the key as a value or
  `{ set: value }`, and write every field of a compound key together.
- **A context-bound update that changes a row's identity** (its primary key,
  or the unique field Golem identifies it by), at the root or nested.
- **A NUL byte** (`\u0000`) in any string or JSON object key, anywhere in an
  operation's arguments or a raw query parameter.
- **A `deleteMany` argument Golem cannot honour.** `where` and `limit` are
  accepted.
- **A delete whose rows plus cascaded dependents exceed
  `batchEvents.maxRows`** (1,000 by default), and an eventful `createMany` or
  `createManyAndReturn` with more rows than that. Raise the bound or split the
  write.

<a id="typescript-upgrades-behaviour-that-changes-without-failing"></a>

#### Behaviour that changes without failing

- **`connect`, `set` and `disconnect` need `read` on the target, not
  `update`.** Every foreign key a context-bound caller writes must name a row
  it can read, and a `set` or to-one `disconnect: true` also needs `read` on
  the rows it detaches. Grant `read` where a caller links rows. An ability
  that granted `update` on the target and nothing else now gets `NOT_FOUND`.
- **A missing or unreadable link target is always `NOT_FOUND`.** It used to be
  `BAD_USER_INPUT` for a foreign-key scalar, a raw provider error for a
  to-many `connect`, and silent success for `set`. Error handling keyed on the
  old codes needs updating.
- **Cascaded deletes publish events.** Each row removed by `onDelete: Cascade`
  emits `DELETED`; each row whose foreign key is cleared or reset emits
  `UPDATED`. Subscribers see more events than before.
- **`createMany`, `createManyAndReturn` and `updateManyAndReturn` publish
  per-row events** on subscribable models.
- **`deleteMany` honours `limit`.** It used to be ignored.
- **Array `$transaction([...])` runs as one interactive transaction** on the
  generated client, in order, through Golem's interception.
- **Concurrent writes can answer `CONFLICT`.** An upsert whose row appears or
  disappears while it decides, a delete or nested write whose rows another
  write changes while it runs, and on PostgreSQL a write that would otherwise
  deadlock with another all refuse with `CONFLICT` rather than wait or act on
  rows Golem did not check. Retry the whole operation where that is safe.
- **Every upsert takes the guard**, including those on the unscoped client and
  nested ones, and is written as an explicit create, update or connect, never a
  native Prisma upsert.
- **Writes cost a little more.** A single-row engine update on PostgreSQL takes
  about 1.2 ms longer; a simple delete on SQLite about 0.15–0.2 ms longer.

Full detail is in [the versioned TypeScript release history](https://github.com/eleven-am/golem/blob/0b58da0a881fb39a464fe7cd51262524c5d7c749/RELEASE_NOTES.md).

---

<a id="typescript-upgrades-migrating-to-060"></a>

### Migrating to 0.6.0

Most of this release fails loudly at startup, which is the easy kind. Four
things do not: they fail on traffic that works today. Check those first.

`@eleven-am/golem-queue` and `@eleven-am/golem-render` carry no golem
dependency, so upgrading the golem packages does not require upgrading them.
The queue has its own migration sections below.

<a id="typescript-upgrades-hardening-upgrade-required-schema-and-client-changes"></a>

#### Hardening upgrade: required schema and client changes

This upgrade closes the context-aware upsert race, bounds subscription and batch-event memory, exposes compound identities through GraphQL, makes masked output nullability truthful, and adds configured relation dimensions. Apply these steps before deploying the regenerated client.

<a id="typescript-upgrades-1-add-the-internal-upsert-guard-migration"></a>

##### 1. Add the internal upsert guard migration

Every database used by an authorization-enabled Golem application must contain this reserved model (from 0.7.0, every Golem application; see above):

```prisma
model GolemUpsertGuard {
  stripe Int    @id
  seq    BigInt @default(0)

  @@map("_golem_upsert_guard")
}
```

Copy the model from `typescript/packages/core/prisma/golem-core.prisma` or apply the provider SQL in `typescript/packages/core/prisma/migrations/sqlite/001_golem_upsert_guard.sql` or `typescript/packages/core/prisma/migrations/postgresql/001_golem_upsert_guard.sql`, then run your normal Prisma migration and generation workflow. Golem reserves `GolemUpsertGuard`, removes it from generated GraphQL and `forContext` model surfaces, and validates the delegate/table during Nest startup. Missing infrastructure now fails deployment rather than waiting for the first upsert.

The table stays bounded by `defaults.upsertGuardStripes` (4,096 by default). It stores only a stripe and monotonic sequence; model names and unique selector values are hashed and never persisted. All participating context-aware upserts for the same canonical model/selector serialize before the policy branch probe. Plain Prisma/external writers and differently addressed selectors do not participate. A caller-owned SQLite transaction that established an incompatible snapshot before the guard write now returns stable `CONFLICT`; retry only by repeating the complete transaction when that is safe.

<a id="typescript-upgrades-2-regenerate-graphql-and-typescript-clients"></a>

##### 2. Regenerate GraphQL and TypeScript clients

With authorization and `checkReadFields` enabled, every visible scalar/enum model output is now nullable, including required database columns. Inputs retain their existing Prisma requiredness, lists keep their list/item structure, and event identities stay non-null. Re-run GraphQL codegen and fix consumers that assumed a selected scalar could never be `null`.

Compound `@@id` and `@@unique` selectors now appear as Prisma-shaped nested input objects across find-one, update, delete, upsert, connect, connect-or-create, and nested update/delete. A composite subscription event changes `id` from an unsupported scalar to a model-specific object in declared key order:

```graphql
subscription {
  postTagEvents {
    type
    id { postId tagId }
  }
}
```

Single-column event schemas are unchanged. Regenerate typed operations for any newly exposed composite model or subscription.

<a id="typescript-upgrades-3-set-operational-event-bounds-deliberately"></a>

##### 3. Set operational event bounds deliberately

One reference-counted iterator now serves each model/schema's local subscribers. Every subscriber has a bounded queue (`subscription.queueCapacity`, default 64). A full queue disconnects that subscriber with `GOLEM_SUBSCRIPTION_OVERFLOW`; events are not dropped. Wire `subscription.observer` if queue depth, suppressions, evaluation latency, delivery counts, or overflow must feed your metrics system.

Top-level `updateMany` and `deleteMany` now publish deterministic per-row events for subscribable models. The generated client performs the auxiliary work on the active transaction, buffers until commit, and discards on rollback. Defaults are 1,000 rows and 1 MiB encoded payload:

```ts
GolemModule.forRoot({
  // ...
  batchEvents: {
    maxRows: 1_000,
    maxPayloadBytes: 1_048_576,
  },
  subscription: {
    queueCapacity: 64,
    observer,
  },
});
```

Over-limit batches reject before mutation and never truncate. Eventful `updateMany` rejects writes to primary-key components. `deleteMany` snapshots and deletes the exact selected identities and rolls back on a concurrency mismatch. Nested batches and out-of-process writes are still not captured. Publication is commit-aware but neither durable nor exactly-once: a process crash after commit and before publication can lose events.

<a id="typescript-upgrades-4-configure-relation-dimensions-explicitly"></a>

##### 4. Configure relation dimensions explicitly

Prisma-shaped `groupBy` is unchanged. A model with `relationDimensions` gains a separate `<models>RelationGrouped` GraphQL field and the separately typed `forContext(ctx).model.relationGroupBy()` method (including its transaction view and the engine):

```ts
aggregations: {
  dimensions: ['albumId'],
  relationDimensions: {
    artistCountry: {
      path: ['track', 'primaryArtist'],
      field: 'country',
    },
  },
  measures: ['msPlayed'],
  maxIntermediateGroups: 10_000,
  maxGroups: 100,
}
```

Only one explicit forward to-one path is accepted. Reverse-only, to-many/many-to-many, multiple paths, related-model measures, hidden/write-only keys, and unreadable terminal fields fail closed. Root facts whose terminal target is absent or policy-invisible use inner-join semantics and do not contribute. Final `having`, ordering, `skip`, and `take` run after merging; averages are reconstructed from sum/non-null count; the complete first-phase set is capped before pagination. Keep `$scoped()` for shapes outside these constraints.

<a id="typescript-upgrades-check-these-four-before-you-deploy"></a>

#### Check these four before you deploy

**1. Your GraphQL `context` must be a function.**

```diff
  GraphQLModule.forRoot<ApolloDriverConfig>({
-   context: { pubSub },
+   context: ({ req }) => ({ req, pubSub }),
  })
```

Given an object, `@nestjs/apollo` attaches the first request's `req` to it and
hands the same object to every request afterwards. Every later caller then
executes with the first caller's identity. Golem now refuses this rather than
letting it run, so the failure is visible instead of silent — but it arrives on
request #2, so a single smoke test passes and the deploy does not.

If you share a context deliberately, opt out with the exported symbol:

```ts
import { golemSharedContext } from '@eleven-am/golem';
const context = { [golemSharedContext]: true, pubSub };
```

**2. Do you write field-scoped CASL rules?**

Search your ability for a rule carrying a field list:

```ts
can('read', 'User', ['phone'], { id: me });
cannot('read', 'User', ['phone']);
```

If you have none, skip to step 3 — nothing here reaches you. If you do, queries
that **filter, order, paginate or deduplicate** by one of those fields are now
refused where they previously returned rows:

```
Cannot filter or order by field "phone" on User: readability depends on id,
which the query constraint does not discharge
```

This is deliberate. Masking the projection was never enough: a caller who saw
`phone: null` could still write `users(where: { phone: { startsWith: "+44" } })`
and read the value out of which rows came back, one character at a time. The
refusal now covers `where`, `orderBy`, `cursor` and `distinct`, at the root of a
read, inside a relation entry in `select`/`include`, and in the filter a batch
or nested write selects rows with.

Grep your callers for those field names in a filter position. A field the caller
may **always** read is unaffected.

**3. Does any ability grant `update` or `delete` without `read`?**

```ts
can('update', 'Post');   // and no can('read', 'Post')
```

Such a caller can no longer filter `updateMany` or `deleteMany` at all — every
field, including `id`, is refused. A `where` interrogates the database and its
count discloses which rows matched, so it is a read, and a caller who may not
read the model is told nothing by one. Grant `read` alongside the write if the
caller legitimately needs to select rows.

**4. Re-run GraphQL codegen.**

`min` and `max` now accept `DateTime` and `String`, so on models whose
aggregatable columns are not all numeric they reference a new
`<Model>OrderableField` enum. Literals keep working; **typed variables fail
validation**, because GraphQL has no enum subtyping:

```graphql
# no longer valid where the model gained orderable measures
query ($f: [PostMeasureField!]) { postsAggregate(measures: { min: $f }) { … } }
```

<a id="typescript-upgrades-what-fails-at-startup-1"></a>

#### What fails at startup

**Abilities built with `createAbility`.** It is backed by `@casl/prisma`, whose
conditions matcher is exactly what golem now replaces.

```diff
- import { createAbility } from '@eleven-am/authorizer/prisma';
+ import { createGolemAbility } from '@eleven-am/golem-authorizer';

  abilityFactory() {
-   return new AbilityBuilder(createAbility);
+   return new AbilityBuilder(createGolemAbility);
  }
```

If you never set `abilityFactory`, golem installs its own and nothing changes.

**Policy conditions golem cannot render exactly.** Conditions are Prisma's
`WhereInput`. Anything outside it is refused when the ability is built, naming
the rule and the operator, rather than approximated.

**Node below 20.19**, and **any MySQL dialect**. Kysely is ESM-only, and MySQL
is removed rather than left as a claim that was never executed against a server.
SQLite and Postgres are both tested against live ones.

<a id="typescript-upgrades-behaviour-that-changed-without-failing"></a>

#### Behaviour that changed without failing

**`upsert` picks its branch from rows you may update.** A row that exists beyond
your reach used to count as existing and route you to the update branch, which
answered differently from a missing row — enough to enumerate unique keys. Both
now answer alike. A caller who may create still learns a key is taken from the
unique violation; that is the database's disclosure, not golem's to hide.

**Subscriptions end when the subscriber leaves.** One parked at an `await` could
not honour `return`, so a subscription that filtered its events, or whose events
an ability silenced, never unwound — it kept querying for a departed client and
held its context alive. No action needed; your event bus needs nothing new.

<a id="typescript-upgrades-new-and-opt-in"></a>

#### New, and opt-in

**`@BatchedComputedField`** answers a computed field for a whole page in one
query instead of one per row. Scoped per request and per subscription event, so
a cache never crosses a caller.

**`ctx.$scoped(model)`** gives you a Kysely builder rooted at a model with the
policy predicate and field policy already applied, for analytical reads the
generated surface cannot express.

Full detail, including where these guarantees stop, is in [the versioned TypeScript release history](https://github.com/eleven-am/golem/blob/0b58da0a881fb39a464fe7cd51262524c5d7c749/RELEASE_NOTES.md).

---

<a id="typescript-upgrades-migrating-eleven-amgolem-queue-to-050"></a>

### Migrating `@eleven-am/golem-queue` to 0.5.0

The queue has no golem dependency, so this upgrade is independent of the golem
packages. Coming from 0.4.0 or earlier, apply the 0.4.1 section below first.

<a id="typescript-upgrades-replace-one-index-with-two"></a>

#### Replace one index with two

The `Job` model gains two indexes and loses one:

```diff
 model Job {
   // ...
   @@index([status, runAt])
   @@index([status, leaseExpiresAt])
   @@index([scopeType, scopeId, status])
-  @@index([type, startedAt])
+  @@index([type, status, runAt])
+  @@index([status, updatedAt])
 }
```

`[type, status, runAt]` serves the claim poll, which every handler runs every
`pollIntervalMs`; `[status, updatedAt]` serves the retention sweep. Without
them both scan every due job, or every row, on each pass. `[type, startedAt]`
was listed in earlier READMEs but never in the bundled schema, and nothing
queries it; drop it if you added it. The complete model is in
`typescript/packages/queue/prisma/golem-queue.prisma`, shipped in the package
as `@eleven-am/golem-queue/prisma/golem-queue.prisma`.

Generate the migration with `prisma migrate dev` and apply it with
`prisma migrate deploy`. The queue runs correctly without the indexes, only
slower. A plain `CREATE INDEX` blocks writes to `Job` while it builds, so on a
large Postgres table either apply the migration in a quiet window or build the
indexes with `CREATE INDEX CONCURRENTLY`, which must run outside a
transaction.

<a id="typescript-upgrades-custom-stores-must-fence-terminal-writes-on-the-lease"></a>

#### Custom stores must fence terminal writes on the lease

If you implement `JobStore` yourself, `complete`, `fail`, `retry` and
`findOwned` must match only a row that is `RUNNING`, owned by `leaseOwner`,
and whose `leaseExpiresAt` is still in the future, all in the statement that
writes. Once the lease has expired another worker may have reclaimed the job,
so a late outcome from the previous holder must be refused. Nothing checks
this at startup; a store that does not fence lets an expired worker overwrite
the outcome of the worker that reclaimed its job. The bundled stores do it.

<a id="typescript-upgrades-behaviour-that-changes-without-failing-1"></a>

#### Behaviour that changes without failing

- **A renewal error no longer aborts the handler.** A thrown renewal is logged
  and retried at the next interval. The lease is lost, and the handler
  aborted, only when a renewal is refused or when the lease reaches its expiry
  without a renewal landing. Renewals run one at a time.
- **An expired worker records nothing.** Its `complete`, `fail` or `retry`
  matches no row, and the job stays `RUNNING` until lease recovery reclaims
  it, so a job whose worker overran its lease runs again rather than being
  recorded by the stale worker.
- **Shutdown.** Work still running when `shutdownGraceMs` ends is aborted and
  retried, even if the handler then resolves. A handler that resolved before
  the grace ran out is recorded as succeeded. Handlers must already be
  idempotent; this is one more case of at-least-once execution.

<a id="typescript-upgrades-migrating-eleven-amgolem-queue-to-041"></a>

### Migrating `@eleven-am/golem-queue` to 0.4.1

These changes shipped in 0.4.1. Earlier copies of this guide headed them
0.5.0, but no 0.5.0 was published before the section above.

Add two things to your schema and migrate:

```prisma
model Job {
  // ...
  startedAt      DateTime?
}

model JobGuard {
  key         String    @id
  seq         BigInt    @default(0)
  windowStart DateTime?
  spent       BigInt    @default(0)
}
```

`JobGuard` is the serialization point for claim guards. Every worker competing for the same guard writes one shared row before reading, which is what makes the guard hold across processes and across engines. Rows are created as needed; there is nothing to seed or prune.

`startedAt` records when a job most recently entered RUNNING and is written on every claim, including lease recovery, so both additions are required even if you never declare a guard. Rate budgets are charged on the `JobGuard` row (`windowStart`, `spent`), and retention prunes terminal rows by `updatedAt`.

This section used to tell you to add `@@index([type, startedAt])` as well. Nothing queries it; 0.5.0 replaces it with the two indexes above.

<a id="typescript-upgrades-the-table-option-on-prismajobstore-is-gone"></a>

#### The `table` option on `PrismaJobStore` is gone

It only existed to interpolate a table name into hand-written SQL. Claims now go through Prisma's query API, which resolves `@@map` itself, so the option had no effect. Delete it; your mapped table keeps working.

<a id="typescript-upgrades-custom-stores-must-declare-that-they-enforce-guards"></a>

#### Custom stores must declare that they enforce guards

If you implement `JobStore` yourself and any handler uses `serializeByScope`, `waitsFor`, `notWhileRunning`, or a resource pool, evaluate those guards inside the statement or transaction that claims, then set:

```ts
readonly enforcesClaimGuards = true;
```

The dispatcher refuses to start otherwise. The guards are optional fields on `ClaimInput`, so a store written before they existed type-checks and silently runs every guarded job unguarded — the refusal replaces that with a loud failure.

<a id="typescript-upgrades-migrating-eleven-amgolem-queue-to-030"></a>

### Migrating `@eleven-am/golem-queue` to 0.3.0

A handler now receives one `JobEvent` instead of a payload and an execution object. Breaking, and mechanical.

```diff
-async handle(payload: Record<string, unknown>, { signal }: JobExecution) {
-  const articleId = payload.articleId as string;
+async handle({ payload, signal }: JobEvent<'article.extract'>) {
+  const articleId = payload.articleId;      // typed, no cast
 }
```

Add the interface so the payload is checked against your registration:

```diff
-export class ExtractHandler {
+export class ExtractHandler implements JobWork<'article.extract'> {
```

The decorator is unchanged. As in `@eleven-am/authorizer`, the decorator registers and the interface types — `@QueueHandler` is to `JobWork` what `@Authorizer` is to `WillAuthorize`, and as there, parameters are annotated explicitly.

<a id="typescript-upgrades-what-a-jobevent-carries"></a>

#### What a JobEvent carries

`id`, `type`, `payload`, `attempt`, `maxAttempts`, `scope`, and `signal`.

`attempt` and `maxAttempts` were previously unreachable even though the dispatcher had them, so a handler could not tell whether it was on its last try. It can now:

```ts
if (res.status === 429 && attempt < maxAttempts) throw new RetryableJobError('rate limited', 60_000);
if (res.status === 429) await this.notifyGaveUp(payload.articleId);
```

<a id="typescript-upgrades-typing-is-optional"></a>

#### Typing is optional

Handlers written as `implements JobWork` without a job type still compile, and `queue.add` still accepts any string, unless you declare a `jobs` map. See the README.


<a id="typescript-upgrades-older-typescript-applications"></a>

#### Older TypeScript applications

For earlier decorator, authorization, queue-store and subscription changes, use the [versioned migration history](https://github.com/eleven-am/golem/blob/0b58da0a881fb39a464fe7cd51262524c5d7c749/MIGRATION.md). The requirements above still apply to applications entering the current releases.
<!-- /chapter:typescript-upgrades -->
