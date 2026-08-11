# Golem for Go quickstart

Status: **unreleased P8 working documentation**. The commands below are tested
against the repository checkout. There is no released `vX.Y.Z` to install yet;
do not copy the version placeholder into automation. The first version is chosen
only after every mandatory P8 release gate passes.

Golem turns one Go schema into a typed database client, authorized mutations,
one GraphQL API, analytics, durable event facts, and subscriptions. A normal
application writes models, model-attached policies and hooks, custom/computed
functions, authentication, and host wiring. It does not write ordinary CRUD
resolvers or reconstruct authorization in handlers.

The complete checked-in application is [`go/examples/social`](../../go/examples/social).
It intentionally contains User, Session, Post, recursive Comment, Tag, and
PostTag in one schema and one generated application. GraphQL federation is not
required to compose those models.

## Prerequisites

- Go 1.25 or newer;
- SQLite needs no external C library or database server;
- PostgreSQL deployments require PostgreSQL 15 or newer; and
- a clean database dedicated to the generated schema.

The future released command will be installed from the nested Go module:

```text
go install github.com/eleven-am/golem/go/cmd/golem@vX.Y.Z
```

The checked-in example is a clean nested consumer module with no `replace`
directive. Repository CI builds the local command and supplies a temporary
`go.work` only for unreleased testing. Released users install the command above
and do not need a workspace override.

The current in-process GraphQL generator also requires the consumer module to
pin the generator versions directly. This is explicit project setup: Golem does
not silently rewrite `go.mod` or `go.sum`. Until the released scaffold writes
this file, add the same requirements as the checked-in example:

```text
require (
    github.com/99designs/gqlgen v0.17.70
    github.com/eleven-am/golem/go vX.Y.Z
    github.com/vektah/gqlparser/v2 v2.5.23
)
```

Keep those tool packages in the module graph with `tools.go`:

```go
//go:build tools

package tools

import (
	_ "github.com/99designs/gqlgen/graphql"
	_ "github.com/vektah/gqlparser/v2/ast"
)
```

Run `go mod tidy` after adding the file and commit the resulting `go.mod` and
`go.sum`. A missing pin or checksum is a setup error; generation will not mutate
the module to conceal it.

## 1. Read the schema before generating

Start with the example's handwritten
[`models.go`](../../go/examples/social/social/models.go) and
[`schema.go`](../../go/examples/social/social/schema.go). The model structs
declare exact tables, columns, SQL types, nullability, defaults, primary and
unique keys, indexes, and relations. `DefineSchema` composes every selected
model and declares the SQLite and PostgreSQL targets. Policies and hooks are
methods on the model that owns them; there is no central policy or hook map.

From `go/examples/social`, inspect the normalized model, contract, provider
schemas, stable identities, and fingerprints without writing files:

```console
$ golem inspect --schema ./social
```

`inspect` is the right first command in CI and during review. It does not create
a migration or generated source.

## 2. Review the initial migration, then generate

Migration history is reviewed input to generation. The first generated
application cannot be produced from an empty or missing history.

```console
$ golem migration new --schema ./social --migrations migrations --name initial
$ golem generate --schema ./social --app-out ./social --migrations migrations
$ golem check --schema ./social --app-out ./social --migrations migrations
```

Review every new snapshot, provider manifest, and SQL file before committing
it. `generate` publishes deterministic generated artifacts. `check` verifies
that the reviewed history and generated output are current; it does not update
them.

When the model changes, run `migration new` again with a descriptive slug,
review the exact diff, then regenerate. A destructive operation is refused
until its exact operation ID is passed with a repeated `--approve` flag. Golem
does not accept an arbitrary SQL migration as a way around a refused semantic
cast or backfill.

## 3. Apply migrations as a deployment step

SQLite development uses a named file, not private `:memory:`. The example
commands use an ignored local path:

```console
$ golem migration apply --provider sqlite --dsn file:social.sqlite --migrations migrations
$ golem doctor --schema ./social --provider sqlite --dsn file:social.sqlite --migrations migrations
```

For PostgreSQL, pass the DSN through your secret manager or process environment;
the literal below is a shell placeholder, not a recommended credential store:

```console
$ golem migration apply --provider postgresql --dsn "$DATABASE_URL" --migrations migrations
$ golem doctor --schema ./social --provider postgresql --dsn "$DATABASE_URL" --migrations migrations
```

`doctor` is read-only. It proves provider capabilities, the complete physical
and system schema, the exact reviewed migration ledger, and generated
compatibility. Its public output is intentionally closed and redacted.

After `doctor` reports current state, start the checked-in host:

```console
$ GOLEM_PROVIDER=sqlite GOLEM_DATABASE_DSN=file:social.sqlite go run ./cmd/social
```

## 4. Open the verified database and application

The host opens either `provider/sqlite` or `provider/postgresql`. Both return a
sealed `*provider.Database` only after provider invariants and capabilities are
proved. The generated application's `Open` receives that handle, borrows it,
and re-proves schema and migration state. It never applies migrations, starts a
goroutine, or closes the borrowed database.

The example server is the executable source of truth for configuration and
shutdown order. The owner must:

1. open and later close the provider database;
2. open the generated application;
3. serve `/graphql` and the safe liveness/readiness endpoints;
4. explicitly run the event publisher when events are configured; and
5. on shutdown, stop HTTP intake, cancel and wait for owned workers, then close
   the provider database.

A minimal SQLite provider opener is ordinary public Go code:

```go
package docsnippet

import (
	"context"

	"github.com/eleven-am/golem/go/provider"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
)

func openSQLite(ctx context.Context, dsn string) (*provider.Database, error) {
	return providersqlite.Open(ctx, providersqlite.Config{DataSourceName: dsn})
}
```

The caller owns `Close`; the generated application only borrows the returned
handle.

## 5. Use the generated caller

Resolve a request principal with the generated application's `ForPrincipal`.
The returned caller exposes one typed client per model. Reads, creates,
updates, deletes, upserts, batches, analytics, and event streams all pass through
the same model policy. A transaction closure receives `CallerTx`, whose model
clients join one `sqlx.Tx`; the closure itself is never replayed.

Results are typed `golem.Row[M]` values. Access selected fields with
`golem.Value`, relations with `golem.One`/`golem.Many`, and preserve the
selected/null/present state. A public null deliberately does not reveal whether
storage or authorization produced it.

The generated social caller is used without a repository layer:

```go
package docsnippet

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/examples/social/social"
)

func recentPosts(ctx context.Context, caller *social.Caller[social.Principal]) ([]golem.Row[social.Post], error) {
	return caller.Posts.FindMany(ctx,
		social.Posts.Where(social.Posts.Published.Eq(true)),
		social.Posts.OrderBy(social.Posts.CreatedAt.Desc(), social.Posts.ID.Asc()),
		social.Posts.Take(20),
		social.Posts.Select(social.Posts.ID, social.Posts.Title, social.Posts.CreatedAt),
	)
}
```

Multiple generated writes compose through one caller transaction:

```go
package docsnippet

import (
	"context"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/examples/social/social"
)

func publish(ctx context.Context, caller *social.Caller[social.Principal], postID golem.UUID) error {
	return caller.Transaction(ctx, func(tx *social.CallerTx[social.Principal]) error {
		_, err := tx.Posts.Update(ctx,
			social.Posts.ByID.Value(postID),
			social.Posts.Update(social.Posts.Published.Set(true)),
		)
		return err
	})
}
```

For trusted infrastructure that cannot use a generated operation,
`database.UnsafeSQLX()` exposes the owned `*sqlx.DB`. It bypasses authorization,
validation, hooks, Golem transactions, invalidation, outbox facts, and events;
it cannot join a caller transaction. Do not use it as an ordinary application
repository.

## 6. Prove the policy without a database

`golemtest` answers, for one concrete actor, the two static questions the
runtime asks before it touches a row: which rows an action reaches, and how each
requested result field will be returned. It uses the production policy kernel,
so a passing assertion is evidence about the runtime rather than about a second
implementation. It opens no database, starts no goroutine, and runs no hook.

A kit is built from the three generated artifacts of one generation, narrowed to
one actor and one model:

```go
package docsnippet

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/golemtest"
	"github.com/eleven-am/golem/go/examples/social/social"
)

func TestAlicePostPolicy(t *testing.T) {
	alice := golem.UUID{0x01}

	bindings, err := social.GolemGeneratedApplicationBindings()
	if err != nil {
		t.Fatal(err)
	}
	descriptors, err := social.GolemGeneratedApplicationDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	kit, err := golemtest.New(bindings, descriptors, social.GolemGeneratedSchemaBundle())
	if err != nil {
		t.Fatal(err)
	}
	policies, err := kit.ForActor(social.Actor{UserID: alice, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	posts, err := golemtest.Model(policies, social.GolemGeneratedPostDescriptor)
	if err != nil {
		t.Fatal(err)
	}

	read, err := posts.RowConstraint(golem.FrozenActionRead)
	if err != nil {
		t.Fatal(err)
	}
	expected := social.Posts.Published.Eq(true).Or(social.Posts.AuthorID.Eq(alice))
	equivalent, err := golemtest.Equivalent(read, expected)
	if err != nil || !equivalent {
		t.Fatalf("read constraint equivalent=%v err=%v", equivalent, err)
	}

	plan, err := posts.ClassifyReadFields(
		golemtest.UseProjection,
		golem.FrozenActionRead,
		social.Posts.Title,
		social.Posts.Body,
	)
	if err != nil {
		t.Fatal(err)
	}
	body, ok := plan.Field(social.Posts.Body)
	if !ok || body.Access() != golemtest.AccessConditional {
		t.Fatalf("Body classification=%v present=%v", body.Access(), ok)
	}
}
```

`Equivalent` proves implication in both directions through the production
implication kernel; it is not a comparison of canonical text. `Access` is one of
always, conditional, or never readable, and a conditional field carries the exact
condition the runtime will mask it by.

The kit is a policy inspection and proof kit, not a second authorization engine
and not a mock database. It cannot decide whether a synthetic row is visible, so
relation-aware execution stays an integration test through the generated caller.
[`PRODUCTION.md`](./PRODUCTION.md) describes the full surface and what a passing
assertion does and does not prove.

## 7. Serve GraphQL

The generated application builds one schema for every exposed model. It owns
ordinary query and mutation roots, nested inputs, analytics, and model event
subscriptions. Handwritten custom operations receive the same generated
`*Caller[P]` (or use its transaction closure), so they do not create an
authorization bypass. Computed fields receive authorized dependency rows;
batched computed fields use a bounded generated loader.

The exact model-attached examples are
[`policies.go`](../../go/examples/social/social/policies.go),
[`hooks.go`](../../go/examples/social/social/hooks.go), and
[`extensions.go`](../../go/examples/social/social/extensions.go).

The first release does not provide federation, schema stitching, MySQL,
uploads, custom subscriptions, live queries, automatic production migration,
or raw SQL through an authorized caller/GraphQL surface. The built-in event
transport is process-local rather than a turnkey multi-process broker, and
external SQL writes are invisible unless a conformant CDC adapter is installed.
A deployment that combines this endpoint with another GraphQL system owns that
gateway outside Golem.

## Next reading

- [`PRODUCTION.md`](./PRODUCTION.md) covers authorization, hooks, custom
  operations, analytics, events, security, deployment, troubleshooting, and
  upgrades.
- [`SEMANTIC-INDEXES.md`](./SEMANTIC-INDEXES.md) covers the optional managed
  embedding-provider and similarity-search lifecycle.
- [`p1/MIGRATION-COMMAND-CONTRACT.md`](./p1/MIGRATION-COMMAND-CONTRACT.md)
  defines review and refusal behavior for migration commands.
- [`p8/PUBLIC-PRODUCTION-ABI.md`](./p8/PUBLIC-PRODUCTION-ABI.md) is the frozen
  P8 production boundary while implementation is in progress.
