# Semantic indexes

Declaring a semantic index gives a model two capabilities: search by text, and
find rows like this row. Neither needs a resolver.

The program on this page is executed by `TestSemanticApplicationRuns`.

## Declaring one

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

Generation emits two methods per index, on both caller and system clients:

```go
caller.Notes.SearchRelated(ctx, "flour water", 3)
caller.Notes.SimilarRelated(ctx, notes.Notes.ByID.Value(id), 2)
```

Both accept optional predicates, and both return `SemanticResult` values
carrying `Row()`, `Distance()` and `Similarity()`.

## Supplying an embedding provider

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

`Specification` declares provider, model, revision, dimensions and maximum
batch. The revision participates in a fingerprint, so changing it marks every
row stale rather than silently mixing vectors from two different models in one
index.

## Embedding happens on the queue

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

## Search authorizes before it ranks

The candidate set is the rows the caller may read, and ranking happens within
it. This is not an optimisation: ranking first and filtering afterwards would
turn vector distance into an oracle over rows the caller cannot see — their
existence, and roughly what they are about, leaking through their effect on
the neighbourhood.

Predicates you pass narrow that set further; they cannot widen it.

## Similarity requires reading the source

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

## Freshness

Golem marks a row stale when a write changes an embedded column, and the drain
re-embeds it.

**A stale row does not rank.** It is excluded until re-embedded, and a
similarity request whose source is stale fails with the same "unavailable"
error as a source that was never embedded. Ranking on a vector known to be out
of date would answer confidently with a stale neighbourhood; failing closed
makes the staleness visible.

**No full reconcile runs by default.** `SemanticReconcileInterval` is zero
unless you set it, so golem repairs drift introduced through its own write
path and nothing else. Rows changed by raw SQL, a restore, or another writer
are never noticed. Set an interval if anything writes to your database that is
not golem.

## When the provider fails

**Only a refusal the provider marks as invalid input quarantines a row.** A
batch refused that way is retried one row at a time, so the row it cannot
embed is quarantined and its neighbours are stored.

**Every other failure leaves the rows pending and the drain retries later
without spending an attempt.** That covers an outage, a rate limit, a malformed
response and an error golem cannot classify. The retry waits for the time the
job has already waited, between five seconds and five minutes, so an outage of
any length never dead-letters the drain.

**One row the provider keeps refusing does not hold up the rest.** A batch
refused for an unclassified reason is retried one row at a time, so the rows
around the culprit are stored rather than left pending with it. This holds
wherever the culprit falls: the rows after it in its own batch, and every later
batch in the page, are still attempted.

**A row the provider keeps refusing without saying why is quarantined after
five strikes.** The count lives in `ambiguous_strikes` on the shadow state
table and resets to zero whenever the row embeds successfully or is marked
stale again. At the fifth strike the row's `status` becomes `failed` and its
`error_code` becomes `EMBEDDING_REFUSED_UNCLASSIFIED`. To bring it back, change
the indexed source row: the next pass marks it stale, clears the strike count
and tries again.

**A strike is only ever charged in a pass where a provider call succeeded.**
An outage cannot spend strikes a healthy pass earned earlier. When nothing else
in the pass embedded, golem re-embeds documents it already stored to prove the
provider is answering, and charges nothing if they all fail. It tries up to
three distinct ones and never the same one twice in a pass, so a document the
provider has stopped accepting cannot pin the probe; whichever one answers
becomes the next pass's first choice. Those re-embeds are discarded, and the
rows they borrow are left exactly as they were. A provider that reports
`EMBEDDING_UNAVAILABLE` never strikes at all.

**The strike column requires the migration.** Regenerate and apply it before
the bound can take effect; until then an unclassified refusal is retried
forever, as before. That is the same regeneration the queue's operator-history
indexes need.

**Known limit: an index with nothing stored cannot charge a strike.** Because
a strike is only charged when a provider call succeeded in the same pass, the
pass needs either another row that embedded or a stored document the liveness
probe can borrow. An index whose only document is the one the provider refuses,
with nothing else ever stored, has neither, so it never reaches the bound and
is retried forever.

**An outage costs at most five provider calls per pass.** Two batch calls
failing with nothing stored between them stop the pass for the rest of its
page, and proving liveness costs up to three re-embeds of documents already
stored. Finding which document in a refused batch is at fault happens only once
the provider has answered, so it costs nothing while the provider is down; when
it does run it is bounded at eight calls a pass, and a batch larger than that is
finished by later passes. A pass with nothing stored yet has no candidates to
probe, and there isolating the batch is the only way to learn anything, so it
runs. The count never grows with the number of batches in the page. Each deferred pass is observed as a
`semantic.refresh` retry whose aggregate count is the number of rows it left
pending.

## Ranking is exact

Both providers rank exactly. PostgreSQL deliberately keeps the planner off the
approximate vector index, because an approximate scan returns a full page of
plausible neighbours while silently omitting nearer ones — and a page that is
confidently wrong is worse than a slow one for a feature whose whole purpose is
"these are the closest".

## Cost

No new tables beyond the index's own storage. A search costs one authorized
read and one ranking; a similarity request costs two authorized reads and one
ranking, and no provider call.

## The whole program

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
