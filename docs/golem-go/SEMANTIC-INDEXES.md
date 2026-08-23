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
norm. `Embed` may be called concurrently by independent queries and refreshes;
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
changes the embedding-space fingerprint. Existing rows are then stale and are
re-embedded before the next similarity query or explicit refresh. Changing the
schema dimensions or an index's ordered source fields is a reviewed
`RiskRewrite` migration: Golem drops and recreates only that index's derived
state/vector tables under the same stable index identity, preserves the owner
table, and lazily repopulates the empty index. The application build and
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
`SystemTx`: refresh and provider-native vector ranking do not join an
application mutation transaction.

## Refresh lifecycle

`application.RefreshSemanticIndexes(ctx)` explicitly reconciles every semantic
index. A similarity query fixes its authorized candidate rows first and, when
that set is non-empty, refreshes only the selected model/index before ranking.
An unrelated embedding space cannot add work to or fail that query. A denied
read, rejected read hook, or empty authorized set performs no embedding-provider
work. Correctness does not depend on an application-maintained worker.

Refresh scans all current source rows, materializes their canonical documents,
and computes stable source hashes. It embeds only missing or stale records,
stores each batch transactionally, and removes vectors whose owner row was
deleted. Incremental therefore describes provider work and durable writes, not
the source scan or total refresh memory. Two refreshes in one application
process are serialized. The embedding provider's declared maximum batch size
is honored. If a later batch fails, earlier committed batches remain valid and
the next refresh safely continues the reconciliation.

The authorized candidate rows are a fixed, operation-local snapshot, but the
refresh and vector query do not join the caller's SQL transaction. A concurrent
source update or delete can therefore become visible to vector storage between
the authorized read and ranking, and the next similarity call reconciles the
new state. Semantic search is read-authorized and eventually index-consistent;
it is not a point-in-time transactional search API.

This v1 lifecycle favors correctness and a zero-worker deployment over write
latency: the first query after a large import can perform substantial embedding
work. Production applications should call the explicit refresh during a
controlled job after bulk changes. Cross-process duplicate embedding calls are
possible, but durable vector/state writes remain idempotent.

Delete cleanup is refresh-driven rather than synchronous. A deleted owner row
cannot appear in an authorized similarity result, but its managed vector and
state row can remain at rest until the next successful refresh. Applications
with a deletion or erasure SLA should refresh after the delete is committed and
must separately enforce the configured embedding service's retention/deletion
policy; Golem cannot erase data retained by that service.

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
`semantic.provider`, and `semantic.rank` records. Refresh statement counts
cover source/state scans and executed vector/state writes or deletes, while its
aggregate count is the number of dirty plus stale rows. Provider records have
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
