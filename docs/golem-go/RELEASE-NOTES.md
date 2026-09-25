# Release notes — Go module

The Go module is released independently of the TypeScript packages. Its
versions are the `go/v*` tags; the root `v*` tags belong to the TypeScript
packages and do not describe this module.

```
go get github.com/eleven-am/golem/go@v0.5.0
```

The module lives in the repository's `go/` directory, so its tags carry that
prefix. A plain `v0.3.0` tag would not make this module fetchable.

---

## Unreleased

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
is bounded at eight calls a pass. A pass with nothing stored yet has nothing to
probe, and there isolating the batch is the only way to learn anything. The
count never grows with the number of batches in the page.

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
