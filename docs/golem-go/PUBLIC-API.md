# The public surface

Eighteen packages are public. Everything under `internal/` is not, and Go
enforces that — an application cannot import it.

| Package | What it is for |
|---|---|
| `golem` | schema declaration, policies, values, rows |
| `runtime` | opening an application, limits, queue configuration |
| `provider`, `provider/sqlite`, `provider/postgresql` | opening a database |
| `graphql` | serving the generated GraphQL API |
| `queue` | durable jobs |
| `render` | serving a single-page application |
| `embedding` | supplying an embedding provider |
| `events`, `events/nats` | event transport and its NATS adapter |
| `observe`, `observe/otel`, `observe/slog` | observation |
| `queryplan` | reading a query plan |
| `golemtest`, `events/cdctest`, `events/transporttest` | testing against golem |

## What is guaranteed

**The shape of the surface is recorded and enforced.**
`internal/publicapi/surface.txt` holds every exported symbol of those
packages — types, functions, methods on exported types, and exported struct
fields. `TestPublicSurfaceMatchesItsRecord` compares the code against it and
fails when they differ, separating what was **removed** from what was added,
because removal is what breaks an application.

Changing the surface is allowed. Changing it silently is not: the record has
to be updated in the same commit, which puts the change in the diff a
reviewer reads.

**Behaviour is guaranteed by tests, not by prose.** Earlier releases carried
seven `PUBLIC-*-ABI` documents describing behaviour in words. Words drift
from code without failing anything, and those documents were removed. The
contracts they described are enforced where they can fail: authorization is
pinned by the policy oracles, provider parity by the shared provider
harness, error classification by the code tests, and the documented
workflows by the pages in this directory, each executed by a test.

## What is not guaranteed

**This is a 0.x module.** A minor version may remove or change a public
symbol. When it does, the change appears in the surface record and in the
release notes, and the release notes say what to do about it.

**Generated code is not the public surface.** The `zz_golem_*.gen.go` files
in your own package are yours; their shape follows your schema. What golem
promises is the *generator*, and `golem check` tells you when generated code
no longer matches the schema it came from.

**A declaration can change what is generated.** Declaring a version token
removes nested relation methods, because nested inputs cannot express a
per-row version expectation. Changes of that kind are stated in the release
notes rather than discovered from a compile error.

## Before 1.0

A 1.0 would claim that removals stop happening without a major version. That
claim needs the surface record to have held still across several releases
first. It has held for one.
