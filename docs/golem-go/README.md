# Golem for Go

Declare your data as Go structs. Golem derives the database schema, the
migrations, an authorized query and mutation API, and a GraphQL surface.

Every page here that carries a program is executed by a test. The code on
them is code that ran.

## Start here

- **[QUICKSTART.md](./QUICKSTART.md)** — an empty directory to a running
  application in four commands.
- **[GUIDE.md](./GUIDE.md)** — relations, authorization, queries and
  mutations, hooks, events, kernel mode, limits and error codes. What you need
  for a real application.
- **[REACT.md](./REACT.md)** — a live React front end on your golem server:
  provider, live lists, mutations, infinite scroll and counts.
- **[AGENTS.md](./AGENTS.md)** — one-file working guide for AI agents: rules that bite, every capability, a verified end-to-end example.

## Capabilities

- **[QUEUE.md](./QUEUE.md)** — durable background jobs in your own database,
  so enqueueing shares a transaction with the write that caused it.
- **[SEMANTIC.md](./SEMANTIC.md)** — search by text and find rows like this
  row, authorized before ranking.
- **[FULLTEXT.md](./FULLTEXT.md)** — transactionally maintained ranked term,
  phrase and prefix search on SQLite and PostgreSQL.
- **[RENDER.md](./RENDER.md)** — serve a single-page application with
  per-route metadata for crawlers.

## Contracts

- **[PUBLIC-API.md](./PUBLIC-API.md)** — which packages are public, what is
  guaranteed about them, and where each guarantee is enforced.

## Releases

- **[RELEASE-NOTES.md](./RELEASE-NOTES.md)** — what each `go/v*` release
  changed, what to check before upgrading, and what is deliberately absent.
- **[MIGRATION.md](./MIGRATION.md)** — upgrading from go/v0.6.x to go/v0.7.0:
  every breaking change and what to do about it.

## The idea

Authorization is a predicate, not a callback. A policy is compiled into the
SQL of every query, so an unauthorized row is never read rather than read and
filtered out. That is why search can rank within what a caller may see, why
similarity refuses a source you cannot read, and why a masked field is
indistinguishable from an absent one.

## Repository verification prerequisites

Before the first local `scripts/verify.sh database-bound` or
`scripts/verify.sh oracle-failure-and-event` run, explicitly cache the pinned
Core NATS image with `scripts/verify.sh cache-nats`. The cache action uses the
image declared in the verifier; it does not start a container. It requires an
available local Unix Docker socket. Set `DOCKER_HOST=unix:///absolute/socket/path`
when that socket is not `/var/run/docker.sock`; remote endpoints are refused.

Ordinary verification does not fetch or inspect images in the shell. Its Go
fixtures inspect the exact cached image through a 15-second Docker command
bound and fail required gates when the image or daemon is unavailable. The
explicit cache action can wait on the daemon or network; cancel it if the local
daemon stalls. CI runs it as a separate prerequisite with a five-minute step
deadline, before either NATS-dependent verification tier. It is not an additional
verification matrix tier. PostgreSQL prerequisites and private cluster ownership
remain unchanged.

The database tier first runs the ordinary V3 suite, then a scoped V4 compiler
overlay for the named history/policy-fact runtime tests and four generated
consumers covering authorization equivalence and bound scope/fact templates.
These template consumers require historical route evidence and run in the
mandatory V4 pass. The existing client CI job separately runs
`scripts/verify.sh writer4-client` after installing and checking the locked
client packages. This starts fresh generated applications for continuation,
custom invalidation, and browser recovery after restore on their declared
providers. These required passes reject missing or skipped tests, including
skipped subtests; the checked-in writer default remains V3.

For focused local verification, `scripts/verify.sh writer4-database` runs the
staged database acceptances without repeating the ordinary database tier.
Both explicit writer actions use the existing private PostgreSQL owner. The
client action also requires Node and the installed client dependencies.
Existing Go flags are preserved; an existing compiler overlay is refused rather
than silently replaced. Temporary overlay files are removed on exit.

The staged runtime pass uses temporary stubs for the external test package
when it contains no selected tests, `TestMain`, or `init` functions. This avoids
compiling unrelated generated-application tests for the internal selection;
the ordinary V3 suite still runs them. Finding any of those declarations keeps
the entire external package, including its helpers. The runtime package import
path, required-test inventory, and generated-application checks stay intact.
