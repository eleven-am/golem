# Release notes — Go module

The Go module is released independently of the TypeScript packages. Its
versions are the `go/v*` tags; the root `v*` tags belong to the TypeScript
packages and do not describe this module.

```
go get github.com/eleven-am/golem/go@v0.3.3
```

The module lives in the repository's `go/` directory, so its tags carry that
prefix. A plain `v0.3.0` tag would not make this module fetchable.

---

## go/v0.3.3

**Take this release if you set `queue.RetentionDisabled` on v0.3.2.** It did
the opposite of what it says.

`RetentionDisabled` is a negative duration, and the worker scheduled its
next pass with `now.Add(RetentionEvery)` — permanently in the past. The
configuration documented as turning retention off ran a retention `DELETE`
on every dispatch iteration instead of never.

`RetentionAge` still gated the delete, so recent rows survived and nothing
looked wrong. What was lost is what the setting was chosen to keep: terminal
jobs past the age, deleted hard, nothing archived, nothing logged on the
success path. If you also lowered `RetentionAge` toward its one-hour
minimum, you lost more.

**If you were on v0.3.2 with retention disabled, your job history was
trimmed to `RetentionAge` regardless.** golem cannot recover it; restore
from your own database backups if you need it.

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
