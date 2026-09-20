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
`internal/publicapi/surface.txt` holds the type-checked API of those
packages, one entry per line. It is read from `go/types`, not from source
text, so it records what the compiler sees:

- **functions and methods** with their full signature, including type
  parameters and their constraints. Methods are recorded per method set:
  `(T).M` for a method callable on a value and `(*T).M` for one callable on a
  pointer, so moving a method from a value to a pointer receiver removes an
  entry. Methods promoted through an embedded field are included.
- **types** as either an alias (`= target`) or a defined type with its
  underlying type and type parameters. A defined type that is comparable has
  a `comparable` entry, so losing comparability — which breaks `==` and use
  as a map key — is a removal.
- **struct fields** in each exported field's own entry with its type, whether
  it is embedded, and its tag. Fields promoted through an embedded field are
  recorded as `promoted field`. When every field of an exported struct is
  exported, an `unkeyed literal {A, B, C}` entry records their order, because
  an unkeyed literal `T{a, b, c}` breaks when a field is added, removed, or
  reordered.
- **interfaces** with the list of their methods in their header, so adding
  a method (which breaks every implementation outside golem) is a removal of
  the old header, not only an addition. An interface with an unexported
  method is marked `sealed`. Each method has its own entry with its
  signature, and each type-set element (`~int | ~string`, `comparable`) has
  an `element` entry.
- **constants** with their type and evaluated value, so reordering an `iota`
  block is caught.
- **variables** with their type.
- **types golem does not export but a caller can still reach** — the target
  of a public alias to an `internal/` type, or an unexported type returned by
  an exported function — are recorded the same way under their own path,
  because a caller can use their fields and methods.

Parameter names, declaration order, comments, and the choice between
equivalent spellings (`byte` and `uint8`, `any` and `interface{}`, an alias
and its target) are not API and produce no difference.

`TestPublicSurfaceMatchesItsRecord` compares the code against the record and
fails when they differ, separating what was **removed** from what was added,
because removal is what breaks an application. A changed entry shows as a
removal of the old line and an addition of the new one.

Unexported struct fields are not recorded individually: a caller cannot name
them. What they can change for a caller is recorded instead — whether the
type is comparable, and whether an unkeyed literal is possible at all (it
is not once any field is unexported, so the `unkeyed literal` entry
disappears).

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

**The surface record sees shape, not everything a caller can depend on.**
It does not see:

- behaviour — what a function does with the same signature;
- additions that break only in unusual code: a new method or promoted field
  can make a selector ambiguous in a caller's type that embeds two golem
  types, and a new exported field in a struct that also has unexported fields
  is reported only as added;
- field order, size, and alignment of structs with unexported fields, which
  only `unsafe` and reflection observe;
- declarations behind build constraints for another platform, since the
  record is taken on the platform the test runs on;
- changes inside standard-library or third-party types that golem's
  signatures mention by name.

It also reports some changes that are not breaking: renaming a type
parameter, or rewriting a constraint as a different but equivalent type set,
shows as a change.

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
