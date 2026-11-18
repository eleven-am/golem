# Semantic indexes

Declaring a semantic index gives a model search by text, search by a prepared
vector, rows like this row, and authorized stored-vector reads. None needs a
resolver.

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

## Searching with a prepared vector

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

## Reading stored vectors

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

## When the provider fails

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

## Ranking is exact

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

## Cost

No new tables beyond the index's own storage. A search costs one provider call,
one authorized ranking statement, and the authorized row read that hydrates the
ranked identities; a similarity request costs one authorized source read, one
source-vector read, one ranking statement and that same hydration read, with
no provider call. Exact ranking work grows
with the authorized candidate set. Predicates therefore improve SQLite cost as
well as narrowing results; large public corpora trade latency for the exactness
guarantee on both providers.

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

<a id="local-exact-ranking-characterization-unreleased"></a>

## Local exact-ranking characterization (go/v0.9.0)

The [generated public-API benchmark](../../go/internal/semantic/runtime/rank_public_benchmark_test.go)
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
