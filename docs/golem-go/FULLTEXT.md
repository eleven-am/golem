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
index for direct SQL writes. On SQLite, with the default `FoldDiacritics`
mode, inserts and updates that touch an indexed field, a primary key column
or a unique key column require a connection opened through Golem's SQLite
provider, because the maintenance trigger calls Golem's Unicode-folding
function. A plain `sqlite3` connection does not have that function and cannot
perform those writes. Deletes, and updates that touch none of those columns,
do not require it. Golem's SQLite provider removes planner statistics for the
FTS5 shadow tables when it opens or closes the database and after each
migration, so indexed writes stay flat as the index grows; running `ANALYZE`
on the whole database yourself records them again until Golem next opens or
closes it.

`FoldDiacritics` is the default. Both providers use the same canonical Unicode
decomposition and remove Unicode mark characters. Use `FoldNone` when accents
must remain distinct.

Matching ignores case on SQLite for every script. On PostgreSQL it ignores
case only as far as the database's character type (`LC_CTYPE`) folds it: a
database created with a linguistic ctype such as `en_US.utf8` folds every
script, but one created with `LC_CTYPE=C` folds ASCII letters only. Accented
Latin letters are unaffected under the default `FoldDiacritics`, because
folding reduces them to ASCII first; letters that stay outside ASCII, such as
Greek and Cyrillic, keep their case, so there `καφές` does not find `Καφές`.
Golem does not check the ctype. PostgreSQL requires UTF-8 and the deterministic `pg_catalog."und-x-icu"`
collation, checked when a migration is applied, and supports at most four
distinct weights because `tsvector` has four weight classes.

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

Caller and system transaction clients expose the same search methods; their
candidate query, ranking and hydration all use the transaction-bound executor,
so uncommitted writes are visible and rollback removes them. The generated
GraphQL root is
`textSearchMessagesByContent(query:, take:, where:)`; it returns rows without
scores.

Terms are OR-joined. Double quotes form a phrase. A trailing `*` enables a
prefix term whose final lexeme has at least two letters or digits. Golem
parses this syntax and quotes every lexeme; raw FTS5 or `tsquery` syntax is
never accepted. Queries are limited to 32 terms and 10,000 bytes of valid
UTF-8, and results to 1,000 rows.

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
