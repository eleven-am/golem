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
| `table=` | physical table name |
| `graphql=` | GraphQL type name |
| `unique=name(cols)` | unique constraint |
| `index=name(cols)` | index |

On a real field:

| Key | Meaning |
|---|---|
| `id=` | canonical field identity |
| `pk` | primary key |
| `default=uuid` / `default=now` | value golem supplies on create when the field is not written; no column `DEFAULT` is emitted |
| `readonly` | cannot be written by any caller |
| `updated` | set to the mutation time on every update of the row, including one whose values equal the stored ones |
| `immutable` | writable on create, never on update |
| `type=` | physical column type |
| `hidden` | excluded from the generated API |
| `writeonly` | writable, never readable |
| `relation=has_many` / `has_one` / `belongs_to` / `many_to_many` | relation kind |
| `fields=` / `references=` | the local and foreign columns joining it |

`db:"-"` marks a field as having no column of its own, which every relation
needs.

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
that row. Golem locks the affected rows before the parent is deleted and emits
a change event for each one: a deleted event for every cascaded row and an
updated event for every cleared reference, in the same transaction as the
delete. Each subscriber still receives only the events its own read policy
allows. A delete whose cascade would touch more rows than
`MutationLimits.MaxTouchedRows` allows (1,000 by default) is refused.

## Callers and the system client

```go
caller, err := application.ForPrincipal(ctx, Principal{UserID: id, Authenticated: true})
system := application.System()
```

`ForPrincipal` resolves your principal into an `Actor` and applies policy to
everything it does. `System()` bypasses policy entirely — use it for seeding,
migrations and background work, never for a request.

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

### Mutations return only what you select

This is the API's sharpest edge. A mutation returns a `Row` containing
**nothing** unless you pass a projection:

```go
author, _ := system.Authors.Create(ctx, notes.Authors.Create(notes.Authors.Handle.Create("ada")))
id, present := golem.Value(author, notes.Authors.ID).Get()
// present == false — not even the identity is there
```

Pass `notes.Authors.Select(notes.Authors.ID)` and it is. The same applies to
`Update`, `Upsert` and `Delete`.

`Get()` returns the value **and whether it is present**. Absent-because-not-
selected and absent-because-policy-masked are deliberately indistinguishable,
so a caller cannot discover a field exists by watching it vanish. Ignore the
second return and you get a zero value you cannot explain.

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

## Changing a schema

Editing a model means a new migration before regeneration:

```
golem migration new --schema ./notes --name add-author
golem generate --schema ./notes --app-out ./notes
golem migration apply --provider sqlite --dsn "file:notes.db"
```

`golem migration plan` shows what a migration will do before it runs, and
`golem check --app-out ./notes` fails when generated code no longer matches
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

### Drift

Application startup and `golem doctor` compare the live database with the
reviewed migrations and report any difference as drift. Temporal columns
must match exactly, including the time zone: a `timestamp with time zone`
column altered to `timestamp without time zone` (or a `time` column gaining a
time zone) is drift.

## Errors you will meet early

| Error | Cause |
|---|---|
| `P1_BINDING_POLICY_REQUIRED` | an exposed model has no `DefinePolicy` |
| `generated applications require a reviewed non-empty migration history` | `generate` ran before `migration new` |
| `CONFLICT: mutation conflicted` | a unique constraint rejected the write |
