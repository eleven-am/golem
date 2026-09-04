# Golem for Go

Declare your data as Go structs. Golem derives the database schema, the
migrations, an authorized query and mutation API, and a GraphQL surface.

Every page here is executed by a test. The code on them is code that ran.

## Start here

- **[QUICKSTART.md](./QUICKSTART.md)** — an empty directory to a running
  application in four commands.
- **[GUIDE.md](./GUIDE.md)** — relations, authorization, queries and
  mutations. What you need for a real application.

## Capabilities

- **[QUEUE.md](./QUEUE.md)** — durable background jobs in your own database,
  so enqueueing shares a transaction with the write that caused it.
- **[SEMANTIC.md](./SEMANTIC.md)** — search by text and find rows like this
  row, authorized before ranking.
- **[RENDER.md](./RENDER.md)** — serve a single-page application with
  per-route metadata for crawlers.

## Releases

- **[RELEASE-NOTES.md](./RELEASE-NOTES.md)** — what each `go/v*` release
  changed, what to check before upgrading, and what is deliberately absent.

## The idea

Authorization is a predicate, not a callback. A policy is compiled into the
SQL of every query, so an unauthorized row is never read rather than read and
filtered out. That is why search can rank within what a caller may see, why
similarity refuses a source you cannot read, and why a masked field is
indistinguishable from an absent one.
