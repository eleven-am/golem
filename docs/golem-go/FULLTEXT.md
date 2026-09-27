# Full-text indexes

Full-text indexes provide ranked keyword and phrase search without an
embedding provider. Golem owns their storage, migrations, authorization and
generated query surface on SQLite and PostgreSQL.

The complete program on this page is executed by
`TestFullTextApplicationRuns`.

## Declaration

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

Fields must be local `String` fields. Weights are positive constants. SQLite
uses a contentless FTS5 index; PostgreSQL uses a trigger-maintained `tsvector`
side table and GIN index. Both are updated in the same database transaction as
the model row, including cascades and writes outside Golem's runtime.

`FoldDiacritics` is the default. Both providers use the same canonical Unicode
decomposition and remove Unicode mark characters. Use `FoldNone` when accents
must remain distinct. PostgreSQL requires UTF-8 and the deterministic
`pg_catalog."und-x-icu"` collation, and supports at most four distinct weights
because `tsvector` has four weight classes.

## Querying

Generation adds methods to caller and system clients:

```go
results, err := caller.Messages.TextSearchContent(ctx, `invoice "service fee" ref*`, 20)
for _, result := range results {
	row := result.Row()
	score := result.Score()
	_, _ = row, score
}
```

An optional predicate may narrow the search. The generated GraphQL root is
`textSearchMessagesByContent(query:, take:, where:)`; it returns rows without
scores.

Terms are OR-joined. Double quotes form a phrase. A trailing `*` enables a
prefix term of at least two characters. Golem parses this syntax and quotes
every lexeme; raw FTS5 or `tsquery` syntax is never accepted. Queries are
limited to 32 terms and results to 1,000 rows.

## Authorization and consistency

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
providers or indexes. SQLite ranks weighted field matches without corpus-wide
statistics; PostgreSQL uses per-document `ts_rank_cd`. Hidden rows therefore
cannot appear, alter visible ordering or influence a returned score.

Full-text search does not provide semantic similarity, stemming, synonyms,
snippets or highlighting. Use a semantic index for meaning-based search and
`Similar…`; use a full-text index for exact terms, phrases and prefixes.

## Complete program

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
	results, err := caller.Notes.TextSearchContent(ctx, `renee "service fee"`, 10)
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
