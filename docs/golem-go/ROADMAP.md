# Golem for Go roadmap

Status: **post-P8 product direction; not a claim of released functionality**.
An entry marked *implemented* has landed in the module and carries its own
acceptance evidence; it is still not a release claim, because the Go module as a
whole remains unreleased. Every other entry is direction only.

The completed P0–P8 plans remain the controlling record for the first Go
release. This document records deliberately accepted follow-up direction so a
future implementation begins from an explicit product decision rather than
quietly expanding the core.

## Official cross-process event transport: NATS

Golem will provide one maintained NATS client adapter for the existing public
`events.EventTransport` contract. The NATS server is external deployment
infrastructure; Golem will not embed, start, configure, or administer a NATS
server.

The adapter remains provider-neutral. An application may configure it with
either SQLite or PostgreSQL. Golem's database outbox remains the durable source
of accepted event facts, publication remains at least once, and consumers use
the stable event identity for deduplication. The initial adapter is for live
cross-process fan-out. Golem does not expose or own client cursors, retained
consumer history, or a subscription replay API. An application that needs
durable replay configures that behavior in its broker or consumer outside
Golem. Golem also does not administer JetStream.

The adapter must preserve the existing transport boundaries:

- pass the public transport conformance suite;
- use the sealed runtime binding for event decoding;
- reconnect and shut down without leaking processes, connections, or workers;
- keep queues, payloads, and concurrency bounded;
- expose only closed, redacted observations; and
- never turn broker delivery into an exactly-once claim.

## Deployment topology remains explicit

NATS distributes events. It does not replicate application database state and
does not turn separate SQLite files into one database.

### SQLite

SQLite remains the first-class **single-node** profile: one Golem application
node, one authoritative SQLite database, and many concurrent users/requests.
The NATS adapter may publish to external consumers from that node, but it does
not create a supported horizontally scaled or high-availability SQLite
topology. Multiple machines with independent SQLite files are separate
databases even when they share a NATS subject.

### PostgreSQL

PostgreSQL remains the **multi-node** profile: multiple Golem application
instances share one authoritative PostgreSQL database and use the NATS adapter
for cross-process event fan-out. The database, not NATS, is the shared source of
model, migration, semantic-index, session, and outbox truth.

Provider portability means the same model, policy, query, mutation, event, and
error contracts. It does not claim that a local file database and a client/
server database have identical operational scaling or failover topologies.

## Public policy testing kit — implemented

Golem exposes a small public `golemtest` package for database-free,
actor-specific policy inspection. It returns the effective row constraint,
read-field classification, discharge proof, and relation dependency tree by
adapting the existing production policy kernel. It does not introduce a second
policy evaluator, mock relation database, auth/session framework, string-based
field authority, or runtime bypass.

The ten mandatory acceptance gates named in the contract exist and pass,
including the external generated-application gate, which builds a clean
`example.com` consumer with `GOWORK=off` and public packages only and requires
the kit's static answers to agree with a real generated `Caller` on SQLite,
PostgreSQL C collation, and PostgreSQL linguistic collation. The package's
public surface is part of the frozen public Go API inventory. Implemented here
means landed and evidenced, not released: the Go module as a whole is still
unreleased and its P8 hosted-release gates remain pending.

The complete implementation and acceptance contract, including its recorded
limitations, is [`POLICY-TESTING-KIT.md`](./POLICY-TESTING-KIT.md). Application
usage is documented in [`QUICKSTART.md`](./QUICKSTART.md) and
[`PRODUCTION.md`](./PRODUCTION.md).

## SQLite WAL and reviewed PostgreSQL data evolution

SQLite will use provider-owned, verified WAL with full synchronous durability
for persistent writable files, while remaining an explicitly single-node
profile. PostgreSQL will accept only a closed set of value-preserving type
widenings and one narrow checksummed transactional backfill workflow for new
required columns. “Safe widening” means value-preserving, not lock-free or
rewrite-free.

The exact lifecycle, allowlist, backfill boundary, crash behavior, and
acceptance gates are in
[`SQLITE-WAL-AND-REVIEWED-DATA-EVOLUTION.md`](./SQLITE-WAL-AND-REVIEWED-DATA-EVOLUTION.md).

## Human-readable migration plans

Golem will provide a read-only human explanation of its existing typed,
reviewed migration plan. The output will show ordered operations and phases,
dependencies, provider, risk/approval requirements, value-preservation or
data-loss classification, locking/rewrite/manual warnings, backfill
postconditions, and immutable artifact checksums. It will have concise terminal
text and versioned machine JSON.

This is a presentation of the authoritative typed plan, not a second planner.
It will not apply or modify a migration, approve risk, guess execution time,
generate AI prose, hide reviewed SQL, or promise zero downtime.

The complete implementation and acceptance contract is
[`HUMAN-READABLE-MIGRATION-PLANS.md`](./HUMAN-READABLE-MIGRATION-PLANS.md).

## First-class optimistic concurrency

Golem will support an explicit, opt-in model concurrency token. Generated
update, delete, upsert, nested, Caller, transaction, and GraphQL paths will
require the caller's expected token where the operation can overwrite existing
state. A successful write advances the token atomically; a stale token changes
zero rows and returns one stable conflict error. Hooks, facts, events, and all
other transactional work roll back on conflict.

SQLite and PostgreSQL must expose the same public semantics. Golem will not
silently retry application closures, merge records, choose conflict winners,
retain document history, or infer concurrency from timestamps or field names.
Applications decide whether to reload, merge, retry, or report the conflict.

The complete implementation and acceptance contract is
[`OPTIMISTIC-CONCURRENCY.md`](./OPTIMISTIC-CONCURRENCY.md).

## Safe query-plan visibility

Golem will expose a deliberately sanitized, read-only operator diagnostic for
the execution plan of a typed Golem query. It will identify bounded structural
facts such as provider, operation/model IDs, statement count, scan versus index
strategy, join/relation strategy, sort/aggregate presence, bounded statement
shape, and applicable Golem limits. It exists to catch accidental full scans,
missing indexes, N+1 regressions, and provider-plan divergence before
production.

It will not expose raw SQL, bind values, actor or row data, DSNs, credentials,
physical names, unrestricted provider `EXPLAIN` output, database handles, or an
application-facing query-hint/optimizer escape hatch. It must use the same
authorization and typed planning boundary as execution, perform no mutation,
and remain bounded and redacted on both providers.

The complete implementation and acceptance contract is
[`SAFE-QUERY-PLAN-VISIBILITY.md`](./SAFE-QUERY-PLAN-VISIBILITY.md).

## KISS boundaries

The first NATS work does not include:

- SQLite replication or a distributed-SQLite provider;
- an embedded NATS server;
- Golem-managed NATS clustering, accounts, credentials, or subjects;
- JetStream lifecycle administration;
- any Golem client subscription cursor, retained-history, or replay API;
- exactly-once publication or delivery; or
- changes to application authentication or business logic.

Client replay is not a deferred Golem feature: broker/application consumers own
that concern. The other excluded capabilities require a separate proposal and
explicit acceptance before implementation.

The same rule applies to the policy-testing and data-evolution contracts: their
explicit non-goals are controlling, and implementation must not use them as a
reason to add auth/business logic, a second policy engine, runtime raw SQL,
online migration orchestration, or distributed SQLite.

The migration-explanation, concurrency, and query-plan decisions above are also
narrow contracts. They do not authorize automatic migration execution,
conflict resolution, query hints, raw provider access, or application business
logic. Their linked implementation/acceptance contracts are controlling.
