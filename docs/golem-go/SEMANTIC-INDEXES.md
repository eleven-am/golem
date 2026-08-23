# Semantic indexes

Golem semantic indexes turn selected model text fields into a managed embedding
index. Application code declares what a document means and supplies an embedding
provider. It does not declare vector columns, shadow tables, HNSW indexes, or
provider-specific SQL.

## Declare a space and an index

An embedding space is schema-wide. Its name is stable application ABI and its
dimension count must match the configured provider:

```go
func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "records")
	golem.Actor[Principal](schema)
	golem.Model[Record](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
	golem.EmbeddingSpace(schema, "record-content", 1536)
}
```

A model attaches a named semantic index to one space and an ordered set of
string fields. Field order is part of the canonical document and therefore part
of migration and generation review:

```go
func (Record) GolemModel() golem.ModelSpec[Record] {
	return golem.DefineModel(
		golem.SemanticIndex(
			"content",
			"record-content",
			Records.Title,
			Records.Summary,
			Records.Body,
		),
	)
}
```

The model must have a primary identity made only from portable string, UUID, or
signed integer fields. Source fields must be string columns. Duplicate names,
unknown spaces, dimension mismatches, unsupported identities, and provider
schema drift fail generation or application startup.

## Configure the embedding provider

Implement `embedding.Provider`, freeze its identity with
`embedding.NewSpecification`, and register it under the schema space name:

```go
specification, err := embedding.NewSpecification(
	"openai",
	"text-embedding-3-small",
	"2024-01",
	1536,
	128,
)
if err != nil {
	return err
}

providers, err := embedding.NewRegistry(map[string]embedding.Provider{
	"record-content": recordEmbedder{specification: specification},
})
if err != nil {
	return err
}

application, err := generated.Open(ctx, generated.Config[string]{
	Database:         database,
	Embeddings:       providers,
	ResolvePrincipal: resolvePrincipal,
})
```

Providers receive only canonical text for the fields declared in the index and
opaque correlation keys. Golem never implicitly adds model names, database
identities, principals, policies, or raw rows. If an application deliberately
declares an identity-bearing string field as indexed text, its value is part of
the canonical document and is sent like any other declared field.
Returned vectors must preserve input order, exactly match the declared
dimensions, contain only finite `float32` values, and have a non-zero cosine
norm. `Embed` may be called concurrently by independent queries and index jobs;
providers must be concurrency-safe, must honor context cancellation, and should
apply their own upstream timeout, retry, and rate-limit policy. Provider errors
and panics cross the public boundary only as closed embedding errors.

The configured provider is trusted application infrastructure: canonical text
contains every declared source field for every indexed row, including rows that
some callers cannot read, and a similarity call sends its exact query text.
Do not send confidential fields or queries to a third-party embedding service
unless that service is approved to process them. Caller authorization protects
similarity results; it is not a data-loss-prevention boundary between Golem and
the configured embedding provider. Correlation keys are batch-local opaque
ordinals rather than database identities; providers must not interpret or
retain them.

Changing the configured provider, model, revision, or maximum batch size
changes the embedding-space fingerprint. Existing vectors are then invalid, and
the reconcile enqueued at the next `Open` re-embeds every row. Until that
reconcile completes, queries are embedded in the new space and ranked against
vectors produced by the old one, which is a meaningless ordering rather than an
error. Swap an embedding space during a window where that is acceptable, or
drain the reconcile before serving traffic. Changing the schema
dimensions or an index's ordered source fields is a reviewed `RiskRewrite`
migration: Golem drops and recreates only that index's derived state/vector
tables under the same stable index identity, preserves the owner table, and
leaves the startup reconcile to repopulate the empty index. The application build and
configured provider dimensions must change together with that migration.

## Search indexed rows

Generation adds one typed method per semantic index:

```go
results, err := caller.Records.SearchContent(
	ctx,
	"reliable background jobs",
	10,
	Records.Published.Eq(true), // optional additional predicate
)
```

The method name is `Search` plus the exported Go spelling of the declared index
name: `content` becomes `SearchContent`, while `record-content` becomes
`SearchRecordContent`. Generation refuses two names that collapse to the same
Go method.

Each result contains the authorized `golem.Row[Record]`, cosine distance, and
similarity (`1 - distance`). Smaller distance is better. Equal distances use
the canonical primary identity as a deterministic tie-breaker.

Every semantic index on a GraphQL-exposed model also adds one caller-authorized
GraphQL query. For a model whose GraphQL plural is `Records` and an index named
`content`, the root is:

```graphql
searchRecordsByContent(
  query: String!
  take: Int
  where: RecordWhereInput
): [Record!]!
```

`take` defaults to the lower of the model's GraphQL page size and Golem's
portable semantic-result limit. It cannot exceed the lower of the model's
maximum page size and that semantic limit. The optional `where` argument is
applied in addition to the model's read policy. GraphQL returns normal model
objects so selections, computed fields, and field masking behave exactly like
other generated reads. Distance and similarity remain available from the Go
`SemanticResult`; the GraphQL root deliberately does not introduce a second
scored-result object type.

The optional final argument is one typed predicate—not arbitrary read options.
Golem chooses the projection and candidate bound so pagination or an omitted
identity cannot silently turn a global search into a search over an arbitrary
page. The authorized candidate set is unbounded—it is a subquery inside the
ranking statement, never a materialized list—and a query returns at most 1,000
results; exceeding a hard bound fails closed rather than truncating an
arbitrary prefix.
Query text and each canonical source document are valid UTF-8 and at most 16
MiB. Spaces have 1..2,000 dimensions and a provider batch contains at most
2,048 inputs. The provider's configured maximum may be lower.

Caller search first executes the model's ordinary read policy and read hooks.
Only readable rows with readable primary identities are handed to the vector
ranking query. A highly similar forbidden row is therefore absent, not masked
after ranking. The generated `System` client has the corresponding trusted
method. Semantic search methods are intentionally absent from `CallerTx` and
`SystemTx`: provider-native vector ranking does not join an application
mutation transaction.

## Refresh lifecycle

Keeping the index current is background work, not read work. A search or
similarity call is the ranking statement plus the ordinary authorized row
fetch, and nothing else: it never scans the owner table and never calls the
embedding provider for a source document. Search embeds only the query string;
similarity embeds nothing at all.

### The queue is mandatory

A schema that declares any semantic index requires the durable job queue. An
application that configures no `Queue` is refused at `Open`. There is no
acknowledgement or opt-out, because Golem has no other place to record the work
that keeps the index current.

Golem registers two job types of its own into the application's queue registry.
Applications never write these handlers and never name them:

- `semantic.drain` advances one index by exactly the records the write path
  marked. It probes the stale partial index on the shadow state table, reaches
  those records' owner rows through the identity columns the state table
  mirrors, recomputes each canonical document, and embeds only the documents
  whose hash actually changed. A marked record whose document did not change is
  flipped back to ready with no embedding-provider call at all; a marked record
  whose owner row is gone has its vector and state rows deleted.
- `semantic.reconcile` scans the whole owner table, diffs it against the shadow
  state, and repairs whatever it finds. This is what observes writes Golem
  cannot see.

**Golem cannot verify that a worker is running.** The refusal at `Open` covers
the configuration only: it proves the queue exists, not that any process ever
calls `RunQueueWorker`. A deployment that configures the queue and never runs a
worker will accept writes, mark records, enqueue jobs, and serve increasingly
stale vectors indefinitely, with no error anywhere. Running a worker is the
application's responsibility, and monitoring that it runs is part of operating a
semantic index.

### What each path observes

A write made through Golem marks its own records and enqueues one deduped drain
per index, inside the write's own transaction. Nothing else is required of the
application.

A write Golem never saw — the raw `UnsafeSQLX` handle, `golem migration backfill
attach`, another service writing the same table — marks nothing, so no drain
carries it. Only reconciliation observes such a write. Golem enqueues one
deduped reconcile per index at `Open`, which bounds that staleness to a
deployment cycle, and `application.RefreshSemanticIndexes(ctx)` reconciles every
index on demand. The engine also exposes a single-index scope,
`runtime.App.RefreshSemanticIndex(ctx, model, name)`, which the generated client
does not yet surface. There is no per-record refresh call, because writing the
row is the per-record request.

### The freshness tradeoff

A record marked stale keeps ranking, and keeps serving as a similarity source,
on the vector it was last embedded with. It is not hidden and it is not an
error; it is simply behind. A record that has never been embedded has no vector
and is absent from results until a drain reaches it. Semantic search is
read-authorized and eventually index-consistent; it is not a point-in-time
transactional search API.

A drain that finishes with records still marked hands its own successor
forward, because the queue coalesces an enqueue against the job that is still
holding its lease. Every write a pass makes — the unchanged flip, the stored
vector, the removal of a vanished owner — is conditioned on the state row the
pass actually read, so a mark that commits while the pass is running is never
erased by it. The record simply stays marked, the pass's completion probe finds
it, and the successor carries it. Only a mark that commits after that final
probe waits for the next write to that index or the next reconcile.

Both paths embed in provider batches with a durable commit per batch, honour the
provider's declared maximum batch size, and are serialized within one process.
If a later batch fails, earlier committed batches remain valid and the next
drain or reconcile continues from there. Cross-process duplicate embedding calls
are possible; durable vector and state writes remain idempotent.

Delete cleanup is drain-driven rather than synchronous. A deleted owner row
cannot appear in an authorized similarity result, but its managed vector and
state row can remain at rest until a drain or reconcile removes them.
Applications with a deletion or erasure SLA should confirm the drain ran and
must separately enforce the configured embedding service's retention/deletion
policy; Golem cannot erase data retained by that service.

### A document the provider will not accept

A provider that refuses a batch is retried one record at a time, so the refusal
lands on the document that caused it rather than on the batch it happened to
travel in. That record is quarantined — its state row records `failed`, a closed
error code, and an incremented attempt count — while the rest of its batch and
every later batch still embed. A quarantined record keeps its last stored vector,
so it still ranks and still serves as a similarity source on whatever it was last
embedded with.

Quarantine is never retried on its own. The stale probe skips a failed record,
because a drain chains a successor for as long as anything is marked, and a
document the provider will never accept would otherwise be refused and chained
for the life of the deployment. A quarantined record is retried when a write to
it marks it again, or when a reconcile compares its document against the stored
hash — so `application.RefreshSemanticIndexes(ctx)` and the reconcile Golem
enqueues at `Open` are the deliberate retry. A provider outage refuses whatever
was being drained rather than one document, and quarantines those records the
same way; the next reconcile is what clears them. Failed records are an
operational signal, visible in the index's state table, and they do not clear
themselves.

## Limits and closed errors

Invalid query text, result limits, or optional-predicate arity return
`embedding.CodeInvalidInput`. Provider failures, panics, malformed result
counts, wrong dimensions, non-finite values, and zero-norm vectors return a
closed `embedding.CodeProvider`. Use `embedding.CodeOf(err)` for branching; do
not parse error strings. Schema, migration, missing-registry, and dimension
drift failures are startup/deployment failures and prevent the application from
opening. A failed refresh never makes stale or missing vectors eligible outside
the authorized candidate set.

## Observation

The configured application observer receives closed `semantic.refresh`,
`semantic.provider`, and `semantic.rank` records. Both the drain and the
reconcile report as `semantic.refresh`; a search or similarity call emits no
`semantic.refresh` record at all. Refresh statement counts cover the stale
probe or full source/state scan and the executed vector/state writes, flips, or
deletes, while its aggregate count is the number of records acted on. Provider
records have
zero statements and expose only the input batch size. Rank counts the
provider-native ranking statements actually executed and exposes only the
number of ranked results. The records never contain provider or
index names, source documents, database identities, provider input keys,
vectors, or raw errors. Refresh records are delivered after the process-local
refresh lock is released, so observer callbacks may not block another refresh
while holding that lock.

## Physical providers

Both providers implement the same public cosine-distance contract but use
their native physical strategy:

- **SQLite:** Golem loads the bundled `sqlite-vec` extension through the
  CGO-free `ncruces/go-sqlite3` WASM runtime. Managed `vec0` shadow tables store
  `float32` vectors. No host SQLite extension, compiler toolchain, or C library
  is required on Linux, macOS, or Windows. Applications must open the database
  through Golem's public SQLite provider so the embedded extension and required
  connection invariants are installed and probed on every connection.
- **PostgreSQL:** reviewed migrations require the `vector` extension, create
  managed shadow tables holding a `vector(N)` column, and create HNSW indexes
  with `vector_cosine_ops`. The database operator must make pgvector 0.8.0 or newer
  available to the target PostgreSQL installation before applying the migration.
  Migration uses `CREATE EXTENSION IF NOT EXISTS vector`; the migration
  principal therefore needs permission to install it or an operator must
  preinstall it. Missing or older pgvector, the vector type, HNSW, or the cosine
  operator class fails migration or schema verification rather than falling
  back to an unindexed or in-memory search.

The shadow tables, state rows, extension declarations, and index names are
Golem-owned migration state. Applications must not query or modify them
directly. Backup and restore must include them together with the owner tables
and the migration ledger.

PostgreSQL HNSW is approximate while SQLite's current sqlite-vec query is exact.
Authorization, distance meaning, deterministic tie-breaking, limits, provider
validation, and error behavior are provider-neutral; an approximate provider
may choose a different near-neighbor set at the boundary of a result page.
