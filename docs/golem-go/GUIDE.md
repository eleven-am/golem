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
| `default=uuid` / `default=now` | database-side default |
| `readonly` | cannot be written by any caller |
| `immutable` | writable on create, never on update |
| `type=` | physical column type |
| `hidden` | excluded from the generated API |
| `writeonly` | writable, never readable |
| `relation=has_many` / `belongs_to` / `many_to_many` | relation kind |
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

## Errors you will meet early

| Error | Cause |
|---|---|
| `P1_BINDING_POLICY_REQUIRED` | an exposed model has no `DefinePolicy` |
| `generated applications require a reviewed non-empty migration history` | `generate` ran before `migration new` |
| `CONFLICT: mutation conflicted` | a unique constraint rejected the write |
