# Golem for Go roadmap

Status: **recovery plan**. This document records what must be true for golem to
be usable, the sequenced work to get there, and the product decisions taken
along the way. Entries are direction, not claims of released functionality.

It replaces the previous roadmap, which tracked completed feature direction.
Shipped behaviour is recorded by the public ABI contracts and each capability's
own document; a roadmap that lists finished work is not a roadmap.

## What usable means

Golem is usable when someone outside this repository can verify all five:

1. **Verification never lies.** When golem reports the schema matches, it
   matches — both providers, current and historical canonical formats.
2. **One entry path, one behaviour.** The same operation classifies the same
   provider error, enforces the same ceiling, and answers "may this field be
   read" identically regardless of which surface invoked it.
3. **Errors are actionable alone.** A configuration or limit failure names the
   field and the bound it violated. Nobody reads golem's source to fix their
   own mistake.
4. **The public contract is whole.** No declaration silently removes generated
   methods, and every shipped public package states its compatibility position.
5. **An empty directory becomes a running application using only the
   documentation** — and CI proves it stays true.

Property 5 holds today. Properties 1 through 4 do not.

## Phase 0 — in flight

Semantic ranking pushdown (removes the authorized-candidate ceiling), then
mark-on-write with queue-driven embedding. The second half also unifies the
duplicated versioned mutation executor and centralizes the event-fact capture
decision, because its structural claim is false without them.

Nothing below begins until that structural claim is real.

## Phase 1 — stop the lies

Everything downstream assumes verification tells the truth.

- SQLite schema verification discards fingerprint errors, so a failed
  comparison compares equal and **passes**. PostgreSQL returns the error.
- SQLite verifies with the current-only fingerprint family in two of three call
  sites and the historical-aware family in the third — inconsistent with
  PostgreSQL and with itself, behind one `Provider.Verify` interface.
- `render` decides whether to send a one-year immutable cache directive from a
  filename-length heuristic. A false positive serves stale bytes for a year.
  Delete the heuristic; conservative caching is strictly safer.
- Two provider-error classifiers have already drifted: a PostgreSQL conflict
  code and SQLite's extended busy codes are handled on one mutation path and
  not the other. Unify on the already-exported owner and delete the copy.

Each fix is pinned by a test written to fail first.

## Phase 2 — decide the optimistic-concurrency contract

Declaring a version token silently removes seven nested relation methods from
generated code. The removal is untested, and the feature appears in no
documentation at all.

Decide: nested writes carry a version expectation, or removal is the contract.
Removal is defensible — nested inputs cannot express per-row expectations — but
then it must be stated, tested by exact-surface assertion, and documented as an
exception in the mutation ABI.

This precedes the documentation work because documentation must describe a
decided contract.

## Phase 3 — collapse duplicated ownership

Each collapse appoints one owner and deletes the copies in the same change. No
parallel paths survive.

- Batch mutation state through the single owner four other families already use.
- Recover-rollback-repanic extracted to one owner — five separate
  implementations exist today. Write the batch panic test against the current
  duplicated code **first**: if the batch copy already diverges from the
  documented contract, that is a Phase 1 bug and must be found before
  refactoring on top of it.
- Field readability owned by the registry; the two reimplementations deleted.
- One GraphQL scalar-name set.
- One max-take ceiling computation.
- One queue identity bound.
- One SQLite pool width.

## Phase 4 — errors that name things

- The events error type carries only an error code, so roughly 28 distinct
  configuration bounds all report the same opaque string while the field name
  and violated bound are in scope at the check. One detail-carrying constructor
  at the owner fixes every site.
- The GraphQL limit defaults and ceilings are unkeyed positional literals; a
  field reorder silently remaps every value, and the validation error reports an
  index rather than a name.
- The semantic result ceiling's error text does not name the limit.

## Phase 5 — evidence surface

- Anchor the CI test filters with the count-check pattern the gate already uses,
  and give the duplicated release filter one source.
- Expose the dropped-observation counter through the public pattern that already
  exists one layer up. The bound itself stays.
- Delete `examples/social`'s tests. Two claims must be re-homed first: that the
  public example imports no internal package, and that its generated inventory
  is checked in. The remainder duplicate the external oracle suite.
- Bring queue and render into the production ABI, or split its non-claims list
  so shipped packages are not listed beside unimplemented ones.

## Phase 6 — documentation, written by using golem

The docs split in two, and the split dissolves the delete-or-rewrite question:

- **Delete**: the phase directories and design documents. They exist to build
  golem, not to use it. No test couples to them.
- **Rewrite**: quickstart, production, queue, render, semantic indexes,
  similarity, README. Queue and render currently open by describing which
  TypeScript package they were ported from, which no user cares about.

Each document is rewritten **by building a small real feature against it**. For
each capability the author first states the conventional shape of that feature
category from outside this repository, then either points at the API covering
each element or records an explicit product decision that it is out of scope.

This exists because auditing cannot find the defects that matter most. Four
parallel audits reading this codebase did not find the missing similarity
capability; it was found by trying to use golem. Auditing compares the
repository against itself, and a capability nobody ever wrote down has nothing
to compare against. This phase is the only one that can discover that class, and
its output may be a new findings list rather than a closed one. Weight it toward
the newest capabilities — semantic, queue, render — where usage is thinnest.

## Product decisions

- **Queue recurring/cron scheduling: out of scope, disclosed.** A cron loop
  calling `Enqueue` with deduplication is a few lines of application code;
  building it in drags timezone handling, drift, and leader election into a
  queue that otherwise has none. The defect is the silence, not the absence.
- **Queue priority: not now, revisit on a named starvation case.** Delayed
  execution plus one queue per job class covers most real orderings.
- **The result ceiling stays non-overridable.** No named user need.
- **The GraphQL page-size ceiling stays GraphQL-only.** That transport is the
  untrusted boundary; native callers are the application trusting itself. The
  triplicated computation collapses; the policy does not extend.

## Deployment topology remains explicit

NATS distributes events. It does not replicate application database state and
does not turn separate SQLite files into one database.

**SQLite** remains the first-class single-node profile: one application node,
one authoritative database, many concurrent users, and the embedded
process-local event transport. The maintained NATS adapter is unavailable on
this profile. Multiple machines with independent SQLite files are separate
databases; a broker cannot make them one authoritative application.

**PostgreSQL** remains the multi-node profile: multiple application instances
share one authoritative database and use the NATS adapter for cross-process
fan-out. The database, not the broker, is the shared source of model, migration,
semantic-index, session, and outbox truth.

Provider portability means the same model, policy, query, mutation, event, and
error contracts. It does not claim identical transport availability, scaling, or
failover topology between a local file database and a client/server database.
Configuring the NATS adapter with SQLite must fail explicitly rather than
silently falling back.

## KISS boundaries

Not included, and each requiring a separate proposal and explicit acceptance:

- SQLite replication or a distributed-SQLite provider;
- an embedded NATS server;
- golem-managed NATS clustering, accounts, credentials, or subjects;
- JetStream lifecycle administration;
- any golem subscription cursor, retained-history, or replay API;
- exactly-once publication or delivery;
- changes to application authentication or business logic.

Client replay is not a deferred feature: broker and application consumers own
that concern.

The same rule governs the policy-testing, data-evolution, migration-explanation,
concurrency, and query-plan contracts. Their explicit non-goals are controlling.
They do not authorize automatic migration execution, conflict resolution, query
hints, raw provider access, a second policy engine, online migration
orchestration, or distributed SQLite.
