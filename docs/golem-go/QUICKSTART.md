# Quickstart

An empty directory becomes a running Golem application in four `golem`
commands. Every command and every line of code below is executed by
`TestQuickstartFromEmptyDirectory`, so this page cannot drift from the tool
without failing the build.

The example is a note-taking application on SQLite.

## 1. A module and a schema

```
mkdir notes-app && cd notes-app
go mod init example.com/notes
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

## 2. A policy for every exposed model

Golem refuses to generate an application whose models have no authorization.
Skip this and `golem inspect` answers:

```
P1_BINDING_POLICY_REQUIRED: exposed model Note has no DefinePolicy binding
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

## 3. Inspect the model golem derived

```
golem inspect --schema ./notes
```

It prints the logical model as JSON — identities, table names, fields, and the
providers it will target. Read it once. It is what every later command
compiles against, and errors here are cheaper than errors after generation.

## 4. Create the initial migration, then generate

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
snapshots for review. `generate` writes the `zz_golem_*.gen.go` files beside
your source and a `.golem/` directory holding the fingerprints it will check
against later.

Both are reviewable artifacts. Commit them.

## 5. Apply the migration

```
golem migration apply --provider sqlite --dsn "file:notes.db"
```

## 6. Use the application

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

## Reading values

`golem.Value(row, field).Get()` returns the value **and whether it is present**.
That second return is not decoration:

```go
created, _ := caller.Notes.Create(ctx, notes.Notes.Create(notes.Notes.Title.Create("x")))
title, present := golem.Value(created, notes.Notes.Title).Get()
// title == "", present == false
```

A mutation returns the row's identity, not the fields you wrote. A field is
absent when it was not selected, and absent when policy masks it — the two are
deliberately indistinguishable, so a caller cannot learn that a field exists
by watching it disappear.

Ignoring `present` and printing the value gives you `""` and no indication
whether the note is untitled or the field was never fetched.

## Where to go next

- Declare a second model and a relation between them; `golem inspect` shows
  what golem derived before you generate.
- `golem check --app-out ./notes` verifies generated code still matches the
  schema. Run it in CI: it fails when someone edits a `zz_golem_*.gen.go`
  file or changes the schema without regenerating.
- `golem doctor --provider sqlite --dsn "file:notes.db"` reports whether a
  live database matches the migrations.
