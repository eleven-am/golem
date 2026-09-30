# Release notes — Go module

The Go module is released independently of the TypeScript packages. Its
versions are the `go/v*` tags; the root `v*` tags belong to the TypeScript
packages and do not describe this module.

```
go get github.com/eleven-am/golem/go@v0.6.1
```

The module lives in the repository's `go/` directory, so its tags carry that
prefix. A plain `v0.3.0` tag would not make this module fetchable. Tags before
`go/v0.3.0` predate these notes and are not described here.

---

## go/v0.6.1

**GraphQL introspection works.** In every earlier release, setting
`GraphQLConfig.Introspection: true` had no effect. The generated executor
rejected `__schema`, `__type` and a root-level `__typename` with
`BAD_USER_INPUT` before gqlgen could answer them. Even with that fixed, every
list in an introspection query was charged as a page of `MaxPageSize` rows,
and the standard graphql-js `IntrospectionQuery` is 15 levels deep against a
default `MaxDepth` of 12, so no configuration let a real client introspect.

- `__schema`, `__type` and a root-level `__typename`, including
  `mutation { __typename }`, are answered alongside generated roots in the same
  operation.
- Introspection fields cost one each and do not count toward `MaxDepth`. The
  standard `IntrospectionQuery` passes at default limits. Data fields selected
  beside introspection are costed and depth-limited exactly as before,
  including inside fragments.
- An operation may select at most one `__schema` root and eight `__type` roots;
  more is refused with `QUERY_LIMIT_EXCEEDED`.
- With `Introspection: false`, `__schema` and `__type` are still refused with
  `GRAPHQL_VALIDATION_FAILED`. `{ __typename }` works either way.
- Introspection exposes exactly what `SDL()` publishes; hidden models, fields
  and operations are absent from both.
- A GraphQL server built without an executable schema refuses these meta roots
  with `GRAPHQL_VALIDATION_FAILED` instead of omitting them. Generated
  applications always have one.

GUIDE.md now documents enabling introspection. No regeneration or migration is
needed.

---

## go/v0.6.0

**This release changes behaviour you may depend on.** Three changes can make
code that worked on v0.5.4 fail or behave differently; read them before
upgrading. Everything else is a fix that needs no action, apart from one
optional regeneration described under "Regenerating".

### Changes that need your attention

**A caller can link only to a row it can read.** Every way a caller writes a
foreign key now requires the target row to be readable by that caller: create,
update, update-many, upsert, nested `Connect`, `ConnectOrCreate`, set and
disconnect, through the programmatic clients, `HookExecutor` and GraphQL. A
target the caller cannot read is reported exactly like a target that does not
exist. In v0.5.4 a direct foreign-key write checked nothing about the target,
so a caller could attach a row to one it could not read, and learn whether an
ID existed by comparing the outcome with a random ID. System clients and
foreign-key values written by hooks are not checked. Field modes apply as
before.

`Connect` and `ConnectOrCreate` now require read access to the target instead
of update access. A caller who could read a row but not update it used to get
`NOT_FOUND` from `Connect`, and `ConnectOrCreate` then tried to create a
duplicate and failed with `CONFLICT`; both now succeed. `ConnectOrCreate`
against an existing row the caller cannot read reports not found instead of
attempting a create.

**Cascaded deletes now produce change events.** Deleting a row whose
dependents are declared with `OnDelete(golem.Cascade)`, `SetNull` or
`SetDefault` now locks those dependents in the same transaction and emits a
delete or update event for each, and marks their semantic index entries the
way an ordinary delete or update does. In v0.5.4 the database removed or
changed them silently: subscribers never heard of it, and a cascaded row's
semantic vector stayed stored until a full reconcile. Permission to delete the
parent covers its dependents, as before; subscribers still receive only events
for rows their own read policy allows. A delete whose cascade would touch more
rows than `MutationLimits.MaxTouchedRows` (1,000 by default) is now refused
whole with the row-limit error instead of proceeding. That refusal counts every
dependent, including rows the caller cannot read, so it can tell a caller that
more dependents exist than the limit allows.

**A subscription that may have missed events now ends.** When the event
transport drops or fails mid-stream, every affected subscription ends with the
new code `GOLEM_SUBSCRIPTION_RESYNC` (`events.CodeSubscriptionResync`). Before,
the hub reconnected behind the subscriber's back and anything published during
the gap was lost without a signal, on both the in-memory and NATS transports.
On this code, refetch the state you derive from events and subscribe again, as
you already do for `GOLEM_SUBSCRIPTION_OVERFLOW`. GraphQL clients receive the
code unchanged.

On NATS, losing the connection to the broker now ends every open
subscription this way. Core NATS does not replay messages, so an event
published while a subscriber's connection was still reconnecting used to be
lost without a signal, even when both nodes reconnected within milliseconds.
Events published during the outage are not redelivered to subscribers whose
stream ended; they refetch.

For the same reason, `Subscribe` now returns only once the event source is
connected. A transient connection failure is retried while it waits, and
cancelling the context ends the wait; while NATS is down, a new subscription
waits for the connection to return. It used to return at once and retry in
the background, dropping events published before the connection succeeded.

### Regenerating

Upgrading the module alone requires no migration, and a database created by
v0.5.4 opens and runs unchanged. Regenerating your schema brings two
improvements, each as a reviewed migration:

- **Event delivery claim index.** The migration admits a new index,
  `golem_outbox_delivery_claim`, which golem creates the first time it claims
  deliveries, the same way v0.5.0 created the queue-history indexes. The
  migration's SQL is empty. Once the index exists, an older library refuses to
  start because of it. To go back after applying the migration, drop
  `golem_outbox_delivery_claim` and delete that migration's ledger row.
  Rolling the module back without having applied the migration needs none of
  this.
- **Full-text normalization, if you have a full-text index.** Each index moves
  to the current normalization through one reviewed rewrite: its storage and
  triggers are dropped, recreated and backfilled inside the migration
  transaction. Until you apply it, existing indexes behave exactly as in
  v0.5.4.

Migrations written by v0.6.0 no longer record approvals for constraints that
are dropped and re-added while still accepting every existing value, such as
those renamed by a table or column rename. The v0.5 command-line tool reads
such a migration as missing an approval, so author and apply migrations with
the v0.6.0 tool once you start using it.

### Fixes

**Full-text matching.** On a newly generated or rewritten index, case is
folded independently of the database: on a PostgreSQL database created with
`LC_CTYPE=C`, `καφές` now finds `Καφές`. Indexed text and queries are
normalized to Unicode NFC in both folding modes, so text stored decomposed is
found by a composed query; under `FoldNone` on SQLite, writes to such an index
therefore need a connection opened through golem, as `FoldDiacritics` writes
already did. `"service fe"*` is now accepted as a phrase whose last word is a
prefix; it used to fail with a misleading error.

**SQLite rows containing an empty `Bytes` value read correctly.** The SQLite
driver golem uses reads every column after a zero-length BLOB from the wrong
position: integers came back as 0, text as `""`, later columns took their
neighbour's value, and only a type check such as a UUID raised an error. Golem
now returns `Bytes` columns from SQLite in a form the driver reads correctly.
Golem's own writes never stored an empty BLOB before this release, so a v0.5
application was affected only if one reached a `Bytes` column another way,
such as raw SQL, another writer or imported data. If yours could have, rows
read through v0.5 may have carried shifted values into anything derived from
them.

**Values.** Strings containing a NUL byte are rejected with `BAD_USER_INPUT`
before any SQL runs, on both providers. On SQLite they used to be stored and
then match predicates incorrectly, because SQLite's string functions stop at
NUL; rows already stored that way still read. An empty `[]byte` is stored as
empty instead of NULL, on every write path, and reads back as an empty,
non-nil slice. `golem.NewDecimal(0, scale)` returns `Decimal{}`, so a
zero reads the same on both providers. When an upsert takes its create
branch, its create input must set every field of the target to the target's
value, or the upsert is refused with `BAD_USER_INPUT` and nothing is created.
Before, an upsert by ID whose create input left the ID to a default created a
row with a different ID, so repeating the upsert kept creating unrelated rows.
Set the ID explicitly in the create input, or target a field you do set. A nullable computed field over a
masked value resolves to null instead of an internal server error.

**Queue and events.**
- A single failed lease renewal no longer cancels a running job or event
  publish. The lease is treated as lost only when renewal is refused or the
  lease has expired. Before, a `MaxAttempts: 1` job could be finalized as
  failed although its handler never failed.
- A batch acknowledged during shutdown is recorded instead of being delivered
  again after the lease expires.
- A job that succeeds while the worker is shutting down is recorded instead of
  running again on restart.
- One full subscriber on the in-memory transport no longer stalls delivery to
  every other subscriber for up to five minutes; only its own stream ends.
- A slow subscribe no longer blocks a WebSocket connection's pings and stops.

**Migrations.**
- Renaming a table or key column that another table references now applies on
  both providers. It used to leave migration history stuck.
- Reordering fields works: SQLite rebuilds the table, and PostgreSQL, which
  cannot reorder columns in place, no longer compares physical column order in
  drift checks. PostgreSQL used to refuse any field not added at the end.
- SQLite can raise or remove a string's length limit.
- Drift checks now notice a timestamp or time column gaining or losing its
  time zone; they used to report such a schema as current.
- Renames, raised length limits and newly optional columns no longer ask for a
  `DATA_LOSS` approval. When approvals are missing, all of them are listed at
  once with a ready `--approve` line.
- The errors for a required field without a default, and for a missing
  migration file, now name the field and the real path.

GUIDE.md now documents approvals, what each provider changes in place, and
drift.

**Costs that grew with data.** Measured at a small and a large size, per
operation:

| Operation | Sizes | SQLite before | SQLite after | PostgreSQL before | PostgreSQL after |
| --- | --- | --- | --- | --- | --- |
| Queue claim | 1k → 100k jobs | 0.26 → 24.8 ms | 0.15 → 0.15 ms | 0.78 → 25.7 ms | 0.83 → 0.91 ms |
| Event delivery claim, with the index | 1k → 100k groups | 0.13 → 17.9 ms | 0.03 → 0.02 ms | 2.2 → 24.8 ms | 1.4 ms at 100k |
| SQLite event retention | 2k → 200k groups | 1.28 → 180 ms | 0.16 → 0.19 ms | — | — |
| Batched relation loading with `take` | 200 → 20k children per parent | 6.7 → 315 ms | 0.29 → 0.28 ms | 1.8 → 61 ms | 2.3 → 1.0 ms |

The queue claim change needs no migration. Batched relation loading walks an
index in order when the sort fields are not nullable, and on PostgreSQL text
fields only under the C collation; otherwise it takes each parent's top rows
without sorting the whole batch. A long-running SQLite process now refreshes
planner statistics every four hours instead of only at close, removing
full-text shadow-table statistics in the same step. A failed refresh is
reported to `Config.Observer` as a `runtime.maintenance` operation, and the
schedule continues.

On PostgreSQL, the delivery claim index is built with `CREATE INDEX
CONCURRENTLY` by one node at a time, so outbox writes continue while it builds,
and nodes without the lock keep claiming without it. On both providers an index
that is dropped later is rebuilt.

---

## go/v0.5.4

**Take this release if you use full-text search on SQLite.** In v0.5.3, every
write to a table carrying a full-text index got slower as the index grew. Over
20,000 inserts the last rows cost eight to twelve times as much as the first;
this release keeps them flat.

| 20,000 inserts after a migration | First 2,000 rows | Last 2,000 rows | Last ÷ first |
| --- | ---: | ---: | ---: |
| v0.5.3 | 196–203 µs/row | 1,652–2,336 µs/row | 8–12× |
| v0.5.4 | 220–284 µs/row | 221–230 µs/row | 0.8–1.0× |

The cause was planner statistics. Applying a migration ran `ANALYZE` while the
full-text index was still nearly empty, recording that its internal storage
held two rows. FTS5 reads that storage by rowid range on every write, and the
planner, trusting the statistic, chose to scan the whole table instead. The
table grew with every write, so every write got slower. Closing the database
refreshed statistics the same way, so it could poison an index the session had
never touched.

Migrations now analyze only real tables, never the shadow tables SQLite keeps
behind a virtual table, and closing the database removes any shadow-table
statistics its refresh recorded. Close still refreshes statistics for tables
written during the session, which ordinary indexes depend on after bulk
ingestion.

**A database v0.5.3 already poisoned is repaired on open.** Opening removes
stale shadow-table statistics and replaces the pooled connections that had
loaded them: a connection keeps the statistics it has read even after they are
deleted, so removing the rows alone would leave those connections scanning. You
do not need to regenerate or migrate.

Ordinary tables were never affected. The repair covers every shadow table
SQLite reports, not only full-text ones, so it also removes the statistics
v0.5.3 recorded for the `vec0` table behind a semantic index created before
v0.5.1 and not migrated since.

**The semantic outage cost is corrected.** SEMANTIC.md said an outage costs at
most five provider calls per pass. That holds once an index has stored a
document; an index with nothing stored yet has no document to probe with,
isolates the refused batch instead, and costs up to ten.

---

## go/v0.5.3

**SQLite full-text search now ranks in one linear pass.** The default ranking
counts distinct matched terms per field and applies the field's declared
weight, preserving caller isolation without corpus-wide statistics. An
opt-in BM25 mode is available when corpus-wide term frequency is appropriate.
The ranking query no longer executes a correlated `MATCH` subquery for every
result candidate. Enabling or disabling BM25 on an existing index is a reviewed
rewrite migration. SQLite BM25 can reveal aggregate corpus influence through
scores and ordering, so multi-principal applications should retain the default.

Measured on 200,000 roughly 2 KB documents on an Apple M3 Pro, using weighted
subject, participants and body fields (3/2/1) and five repetitions after
fixture creation:

| SQLite default-ranking query | Median |
| --- | ---: |
| One term, 100 matches | 0.51 ms |
| One term, 1,000 matches | 1.58 ms |
| One term, 10,000 matches | 12.58 ms |
| Three terms | 13.52 ms |
| 10,000-match term with a 0.1% predicate | 7.97 ms |
| Common term, 200,000 matches | 259.51 ms |

The SQLite Unicode-folding callback now returns ASCII text directly. On the
same machine, a 2,070-byte ASCII value fell from 35.5 µs and two allocations
to 0.84 µs and one allocation; non-ASCII folding semantics are unchanged.

Synchronous FTS5 maintenance remains intentionally transactional. A 50,000-row
benchmark with roughly 2 KB bodies measured 36.9 µs per unindexed row and
344.2 µs per indexed row. Profiling at 200,000 rows showed that even a bare
FTS5 insert with no Golem key table, folding, replacement cleanup or additional
trigger statements costs 4.25–4.72 times the owner-table insert. The lower
overhead target proposed during development is therefore not attainable without
weakening immediate search consistency or removing phrase, field or BM25
behavior; this release keeps those guarantees.

**Search APIs are complete across execution modes.** Full-text search has an
additive `TextSearch…Select` form so callers can avoid hydrating large fields.
Full-text and semantic Search/Similar methods are generated for caller and
system transaction clients, and every stage runs on the transaction-bound
executor. Multiple predicates are accepted and combined with `AND`; existing
`TextSearch…(...Predicate)` call sites remain source-compatible.

Committed 200,000-row benchmarks on the same machine measured primary-key and
unique lookups at 107 µs and 88 µs. Indexed ranges returning 20, 200 and
2,000 rows took 94 µs, 324 µs and 7.48 ms. Through the generated public API,
exact SQLite semantic Search and Similar at 1,024 dimensions took 863 ms and
882 ms unfiltered, and 3.2 ms and 4.0 ms with a 0.1% candidate predicate.

**Full-text failures now preserve their public boundary.** Invalid query
syntax and request limits return `BAD_USER_INPUT` with a specific safe reason.
Database execution, streaming and decoding failures follow the internal-error
path instead of being presented as caller mistakes.

`golem doctor` now reports the drifting object's type, name and owning table in
both human and JSON output. Applying a SQLite migration refreshes planner
statistics after schema verification, and closing a verified SQLite handle
runs bounded planner optimization so statistics reflect later ingestion.
Removing a Golem-derived semantic or
full-text index no longer needs a data-loss approval, while historical plans
that already recorded the exact legacy approval remain valid.

SQLite full-text indexes using the default `FoldDiacritics` mode require
inserts and indexed-field updates to use a connection opened through Golem's
SQLite provider. Those triggers call Golem's Unicode-folding function, which a
plain `sqlite3` connection does not register. PostgreSQL direct SQL writes are
unchanged.

---

## go/v0.5.2

**First-class full-text indexes are available on SQLite and PostgreSQL.**
`FullTextIndex` declares weighted local text fields and generates
`TextSearch…` caller/system methods plus a caller-only GraphQL root. Storage is
maintained by database triggers in the model write transaction, reviewed
migrations backfill existing rows, and ranking joins the authorized candidate
query before applying the result limit. Indexed-field read masks are part of
candidate authorization. User query text is parsed as literal OR terms,
phrases and bounded prefixes rather than passed to FTS5 or `tsquery`.

SQLite uses contentless-delete FTS5 storage and PostgreSQL uses a `tsvector`
side table with a GIN index. Both providers share canonical Unicode diacritic
folding. PostgreSQL requires UTF-8 and the deterministic
`pg_catalog."und-x-icu"` collation. Regenerate and apply the reviewed migration
before calling generated full-text methods.

**SQLite predicates no longer wrap every condition in `CASE`.** Equality adds
the one required `IS NOT NULL` guard, while other predicates retain their own
NULL behavior. Indexed primary-key and selective candidate predicates are
sargable again without changing three-valued policy semantics.

---

## go/v0.5.1

**Semantic search no longer makes SQLite scan a `vec0` virtual table before
authorization.** Current SQLite semantic vectors use a strict,
dimension-checked BLOB table, and the exact ranking statement drives from the
authorized candidate query before calculating cosine distance. PostgreSQL
continues to use `pgvector` and exact ranking. A reviewed migration replaces
the derived SQLite vector cache, marks existing semantic rows pending on both
providers, and a deduplicated startup drain rebuilds them with the current
embedding contract. Existing semantic rows remain unavailable to ranking until
that rebuild succeeds, so the embedding provider must be available during the
upgrade.

**Embedding providers can distinguish indexed documents from search queries.**
`embedding.Input.Purpose()` reports `PurposeDocument` or `PurposeQuery`, so
providers can correctly map vendor APIs that require separate document and
query modes. Indexed text is now readable newline-separated values; Golem
retains its binary-safe framing only for change detection. The embedding-contract
version participates in the space fingerprint, so vectors produced under the
old contract never mix with new results.

---

## go/v0.5.0

**Golem can now add a column to its own internal tables on an existing
database.** A semantic shadow state table carries a declared contract version,
and a reviewed step between two versions is planned as an additive
`ALTER TABLE ... ADD COLUMN` rather than a drop and recreate. Your vectors are
never discarded and nothing is re-embedded. The first use of this is
`ambiguous_strikes`, which bounds how long the drain retries a document the
embedding provider keeps refusing without saying why: five strikes, then the
row is quarantined with `error_code` `EMBEDDING_REFUSED_UNCLASSIFIED`. A strike
is only ever charged in a pass where a provider call succeeded, so an outage
can never quarantine a healthy row.

**Three operator-history indexes on `golem_queue`.** Measured on the same
200,000-row queue on both providers, best of repeated runs:

| Operation | SQLite before | SQLite after | PostgreSQL before | PostgreSQL after |
| --- | --- | --- | --- | --- |
| `List`, no state filter | 146 ms | 0.12 ms | 18.5 ms | 0.03 ms |
| `List`, `status='succeeded'` | 76 ms | 0.05 ms | 18.5 ms | 0.03 ms |
| `List`, two states | 11.5 ms | 0.08 ms | 5.1 ms | 0.13 ms |
| `ListFailed` | 6.8 ms | 0.05 ms | 3.9 ms | 0.05 ms |
| Retention, periodic batch | 51.9 ms | 0.68 ms | 8.2 ms | 0.82 ms |
| Retention sweeping half the table | — | — | 11.1 ms | 6.6 ms |

Every shape improves on both providers. The one modest case is a PostgreSQL
retention run whose cutoff sweeps half the table: a parallel sequential scan is
genuinely the right plan there, so the index only takes it from 11.1 ms to
6.6 ms. A periodic retention run, which is what a deployment actually issues,
is ten times faster.

The index set is `golem_queue_enqueued (enqueued_at, id)`,
`golem_queue_history (status, enqueued_at, id)` and a covering
`golem_queue_terminal (status, finished_at, id)`. On SQLite, retention still
merges one ordered run per state, so its 76x comes from the index being
covering, not from the sort disappearing. SQLite names the index explicitly, because without
`ANALYZE` its planner otherwise picks the claim index and sorts; forcing the
plain `(enqueued_at, id)` index instead would have made a rare-status `List`
around a thousand times *slower*, which is why the set is shaped this way.

**Both changes require regenerating and applying one migration, and in
exchange a library upgrade on its own stays reversible.** Golem creates the new
indexes only when your generated schema admits them, so bumping the module
without regenerating changes nothing and can be rolled back. Regenerating and
applying the migration is the deliberate one-way step, exactly like every other
golem migration. Until you take it you get no speedup and no strike bound. One
regeneration delivers both.

Golem's migrations are forward-only. If you need to go back *after* applying
this one, drop `golem_queue_enqueued`, `golem_queue_history` and
`golem_queue_terminal`, delete this migration's ledger row, and drop
`ambiguous_strikes` from each `_golem_semantic_*_state` table — on SQLite that
last one is a table rebuild. Rolling the module back without having applied the
migration needs none of this.

**A healthy document in a refused batch is now stored instead of waiting.** An
unclassified refusal is retried one row at a time, so only the culprit stays
pending — including the rows after it in its own batch and every later batch in
the page. The outage budget grew to pay for that: a total outage now costs at
most five provider calls per pass rather than two — two batch calls, after which
the pass stops for the rest of its page, and up to three re-embeds of
already-stored documents that prove the provider is answering before any strike
is charged. Finding which document in a refused batch is at fault waits until
the provider has answered, so it costs nothing during an outage; when it runs it
is bounded at eight calls a pass. An index with nothing stored is the exception
and costs up to ten: it has no document to probe with, so isolating the batch is
the only way it can learn anything, and it does that until one document embeds.
Neither count grows with the number of batches in the page.

**Startup now verifies every index on `golem_queue` on both providers, not
just `golem_queue_dedupe`.** `golem_queue_claim` and `golem_queue_exclusive` are
checked for the first time, so a database whose claim index was altered by hand
will now fail startup with an error naming the object and telling you to drop
it. A database created by any release from go/v0.3.0 through go/v0.4.0 passes
unchanged. **An index you added to `golem_queue` yourself is now refused** —
golem owns that table; move the index to one of yours.

---

## go/v0.4.0

**Security: a caller can no longer set a moded foreign key through a
relation.** Writing `author_id` directly was refused when the field was
`system`, `readOnly`, `immutable` (on update) or `hidden`, but connecting,
setting or disconnecting the relation wrote the same key unchecked. Every
earlier release allowed it. The relation write is now refused with the same
message and public code as the direct write, whether or not a hook runs. A
system client may still assign a `system` key. If your application relied on a
caller connecting over a moded key, that write now fails; give the key the
mode you actually want it to have, or perform the assignment with
`SystemEscape`.

**Hooks can author a `system` field on every write path.** v0.3.2 left
nested-child hooks, upsert, versioned mutations, update-many batches and
`HookExecutor` refusing one. Each now accepts a field the hook wrote and still
refuses one the caller's own input named, whatever the hook does to it.
`HookCreateRow` can now create a row whose required `system` field a hook
supplies. A versioned upsert on a model with a required `system` field could
never run; it now does.

**SQLite similarity ranking is exact.** It asked sqlite-vec for a window of
nearest neighbours and filtered afterwards, and when more rows sat at the same
distance than the window held, it returned the wrong ones. It now runs the same
exact query as PostgreSQL. A search is one statement.

**An embedding-provider outage no longer dead-letters the drain.** An outage,
rate limit or unclassifiable error used to spend an attempt, and after five the
drain job was dead-lettered; it could also quarantine every row in the batch as
if the documents were bad. Only a refusal the provider marks as invalid input
quarantines a row now. Everything else retries without spending an attempt,
one refused row no longer holds up the rest of the index, and an outage costs
at most two provider calls per pass. See SEMANTIC.md.

**Event publishers no longer take more leases than they can work.** A publisher
claimed `ClaimRows` groups at once but ran `PublisherConcurrency` of them, so
the rest sat leased and unrenewed, and another publisher could take them over
and deliver the same event twice. A claim is now capped at
`PublisherConcurrency`; the default `ClaimRows` of 64 behaves as 8 under the
default concurrency. On shutdown, leases that never started are released
instead of stranded until they expire. The claim observation reports the size
actually requested.

**`events.RetentionDisabled` turns event retention off.** There was no way to
do it. `Limits.RetentionEnabled()` reports it.

**The queue checks its own tables.** A same-named `golem_queue` without its
primary key, or a `golem_queue_dedupe` of any other shape, was accepted, and
deduplication quietly stopped working. Startup now refuses it. Tables created
by v0.3.0 through v0.3.3 pass unchanged. On SQLite a deduplicated enqueue is
now one statement, so a job finishing mid-enqueue can no longer make it fail.

**Queue finalization failures are observed.** When the store rejected a job's
completion, retry, cancel or release, the error was discarded. It is now
observed as a queue commit failure. A lease another worker fenced stays silent,
as before.

**`queue.Backoff.Delay` is defined for every value.** Negative values panicked,
and a large base with a large attempt looped for about 2⁶³ iterations. A zero or
negative `Base` or `Cap` now takes the default.

**Built with Go 1.25.14.** `go.mod` pins the toolchain; 1.25.2 had twenty
reachable standard-library vulnerabilities.

Outbox rows written by something other than golem — manual SQL, a partial
restore — are not delivered until the publisher restarts. Restart it after
inserting them.

---

## go/v0.3.3

**Take this release if you ever set `queue.RetentionDisabled`.** It did the
opposite of what it says, in every release that offered it — v0.3.0, v0.3.1
and v0.3.2. Each shipped the constant and a QUEUE.md instructing its use,
and none gated the worker on it.

`RetentionDisabled` is a negative duration, and the worker scheduled its
next pass with `now.Add(RetentionEvery)` — permanently in the past. The
configuration documented as turning retention off ran a retention `DELETE`
on every dispatch iteration instead of never.

`RetentionAge` still gated the delete, so recent rows survived and nothing
looked wrong. What was lost is what the setting was chosen to keep: terminal
jobs past the age, deleted hard, nothing archived, nothing logged on the
success path. If you also lowered `RetentionAge` toward its one-hour
minimum, you lost more.

**If you ran any of v0.3.0, v0.3.1 or v0.3.2 with retention disabled, your
job history was trimmed to `RetentionAge` regardless.** golem cannot recover
it; restore from your own database backups if you need it.

---

## go/v0.3.2

A field an application owns and no client may set.

`golem:"system"` withholds a field's write builders from caller clients and
keeps them on system clients; `golem:"system;immutable"` keeps only create.
The field never enters a generated GraphQL input, stays readable, and a
policy granting it to a caller does not override the mode.

`SystemEscape(tx)` returns a system client bound to the caller's own
transaction, so a write the caller may not perform commits or rolls back
with the work that caused it. Every call is observed as
`transaction.system_escape`. It reaches extension bodies and hooks, where
custom mutations live.

A hook may author such a field. A field the caller's own input named stays
refused whatever a hook does to it, so a hook chain cannot launder a value.

Nested-child hooks, upsert, versioned mutations and update-many batches
still refuse a hook-authored system field. Each fails closed.

No migration is required. A schema declaring no `system` field generates
byte-identical output.

---

## go/v0.3.1

Fixes an upgrade blocker in v0.3.0. Anyone on v0.3.0 with a semantic index
should take this release; anyone on v0.2.1 should upgrade straight to it.

**An application could not apply migrations authored before v0.3.0.**
v0.3.0 added an identity projection to semantic managed storage, and
migrations predating it carry none — which the renderer already handled by
replaying sealed history in the shape it was reviewed in. The post-apply
introspection did not honour that, re-rendering the same extension without
the replay flag and refusing DDL golem had just written. Applying a
pre-existing initial migration failed even against an empty database, on
both providers, with no way forward but discarding migration history.

### Upgrading from v0.2.1

```
golem migration new --schema ./app --name semantic_identity
golem generate --schema ./app --app-out ./app
golem migration apply --provider sqlite --dsn "file:app.db"
```

That order is enforced: `generate` refuses a schema with no migration for
it and says so. Migration names must match `[a-z][a-z0-9_]{0,62}`, so
`semantic_identity` is accepted and `semantic-identity` is not.

This carries an existing database to current storage in place; no reset is
needed.

---

## go/v0.3.0

The first release intended for use from another application.

### Semantic indexes

Declaring an index generates two capabilities rather than one — search by
text, and find rows like this row:

```go
caller.Notes.SearchRelated(ctx, "flour water", 10)
caller.Notes.SimilarRelated(ctx, notes.Notes.ByID.Value(id), 10)
```

Search authorizes before it ranks: the candidate set is the rows the caller
may read, and ranking happens inside it. Ranking first and filtering
afterwards would let distance disclose the existence and rough content of
rows the caller cannot see.

Similarity resolves its source through an ordinary authorized read, so a key
parameter cannot enumerate hidden rows. Absent, unreadable, and
identity-masked sources produce one indistinguishable `CodeNotFound`. The
source is excluded from its own results before ranking.

Embedding runs on the queue: writes mark rows stale and a worker embeds them,
so rows become searchable shortly after they are written rather than
immediately. You supply the embedding provider; golem never calls a vendor
for you.

See [SEMANTIC.md](./SEMANTIC.md).

### The durable queue is reachable

`Enqueue`, `QueueOperator` and `RunQueueWorker` are generated onto the
application, and enqueueing can share a transaction with the write that
caused it — the job row commits with your data or neither does. Both
providers behave identically.

See [QUEUE.md](./QUEUE.md).

### Serving a frontend

`render` serves a single-page application's shell and rewrites its metadata
per route, so a crawler asking for `/n/42` sees that page's title rather than
the shell's. It needs no schema or database.

See [RENDER.md](./RENDER.md).

### Errors name what they refuse

Configuration and limit failures name the field and the bound rather than
returning an opaque code:

```
GraphQL limit MaxDepth is 33, outside the portable range 1..32
```

Detail is golem-authored and printed; an untrusted cause stays available to
`errors.Is` and is never printed, so provider credentials and payloads cannot
reach an error string. A type-checked test enforces that at every call site.

### Documentation that runs

Five pages, each executed by a test that extracts its code, writes it into an
empty directory, generates, migrates and runs it. See
[README.md](./README.md).

---

## Behaviour worth checking before you upgrade

**Queue retention is on by default.** Workers delete terminal job rows older
than thirty days, checked every minute. Earlier versions deleted nothing. To
keep history:

```go
limits := queue.DefaultLimits()
limits.RetentionEvery = queue.RetentionDisabled
```

**A stale semantic row does not rank.** When a write changes an embedded
column the row is excluded until re-embedded, and a similarity request whose
source is stale fails rather than ranking on a vector known to be out of
date.

**No full semantic reconcile runs by default.** `SemanticReconcileInterval`
is zero unless set, so golem repairs drift introduced through its own write
path and nothing else. If anything else writes to your database — raw SQL, a
restore, another service — set an interval.

**Declaring a version token removes nested relation methods.** Nested inputs
cannot express a per-row version expectation, so generated code omits them
rather than accepting one it cannot honour.

**Error strings changed.** Classification did not: `Is`, `CodeOf` and every
switch on an error code behave as before. Only the text differs, so anything
matching on message text will need updating.

---

## Known limits

Not implemented, and not planned without a separate proposal: MySQL,
federation, automatic production migration, raw SQL through authorized
surfaces, a second policy engine, online migration orchestration, distributed
SQLite, and vendor CDC drivers.

Queue recurring or cron scheduling and job priority are deliberate omissions.
A cron loop calling `Enqueue` with a dedupe key is a few lines of application
code; building it in drags timezones, drift and leader election into a queue
that has none of them.

**Transport availability differs by provider.** SQLite is the single-node
profile with a process-local event transport; the NATS adapter is
unavailable on it. PostgreSQL is the multi-node profile where instances share
one database and use NATS for cross-process fan-out. Provider portability
means the same model, policy, query, mutation, event and error contracts — not
identical transport, scaling or failover topology.
