# Golem

Declare your data model and authorization policies. Golem builds a typed data
API, reviewed database migrations, and GraphQL queries, mutations and live
subscriptions for SQLite and PostgreSQL.

The Go backend also includes durable jobs, full-text and semantic search,
authenticated continuation pages, and offline SQLite snapshot/restore tools.
Its TypeScript and React clients use the generated manifest to keep queries
current and handle session and restore boundaries.

## Choose your implementation

| Component | Install / location | Start here |
| --- | --- | --- |
| Go backend and CLI | `github.com/eleven-am/golem/go` · `go/` | [Go quickstart](docs/GUIDE.md#quickstart) |
| Browser client | `@eleven-am/golem-client` · `client/core/` | [Client setup](docs/GUIDE.md#client) |
| React hooks | `@eleven-am/golem-react` · `client/react/` | [React setup and hooks](docs/GUIDE.md#react) |
| Prisma/NestJS backend | `@eleven-am/golem` · `typescript/` | [TypeScript backend guide](docs/GUIDE.md#typescript-backend) |

The backend implementations have distinct APIs, authorization models and event
guarantees. The browser client above targets the Go live protocol. Their release
namespaces are independent: `go/v*` for Go, `client/v*` for the browser client,
and plain `v*` for the Prisma/NestJS packages.

## Start with Go

The documented release is **go/v0.9.2**, with browser client **0.3.1**.
Use Go **1.26.11 or later**, matching CLI/library versions, and regenerate your
application and client manifest when upgrading.

```sh
go install github.com/eleven-am/golem/go/cmd/golem@v0.9.2
mkdir notes-app
cd notes-app
go mod init example.com/notes
go get github.com/eleven-am/golem/go@v0.9.2
```

Create the schema and policy files from the [complete quickstart](docs/GUIDE.md#quickstart),
then inspect, create migration history, generate, and apply:

```sh
golem inspect --schema ./notes
golem migration new --schema ./notes --name init
golem generate --schema ./notes --app-out ./notes
golem migration apply --provider sqlite --dsn "file:notes.db"
```

Every exposed model needs a policy. Caller access is authorized; `System()` is
explicit privileged access and bypasses policies and hooks. Generated code must
match the schema and reviewed migrations. The quickstart includes the program
that opens the database and uses the generated client, and is executed by tests.

## What you can build

- [Models, relations, policies and typed reads/writes](docs/GUIDE.md#application),
  with transactions, hooks and custom operations.
- [Named results and embedded rows](docs/GUIDE.md#application-named-output-objects-and-embedded-model-rows),
  plus [nested inputs, presence and exactly-one choices](docs/GUIDE.md#application-named-inputs-omission-and-exactly-one-choices).
- [GraphQL, events and live subscriptions](docs/GUIDE.md#application-graphql),
  plus [React hooks](docs/GUIDE.md#react) and continuation queries.
- [Durable jobs and actions](docs/GUIDE.md#queue), with retries, cancellation,
  priority lanes and transactional enqueue/promotion.
- [Full-text search](docs/GUIDE.md#fulltext) and [semantic search](docs/GUIDE.md#semantic),
  with authorization applied before ranking.
- [Reviewed schema changes and offline copies](docs/GUIDE.md#application-changing-a-schema),
  [SQLite storage profiles](docs/GUIDE.md#sqlite), and
  [frontend hosting](docs/GUIDE.md#render).

The [upgrade chapter](docs/GUIDE.md#upgrading) states compatibility requirements.
Go 0.9.0 retains automatic V3 event writing and supplies V3/V4 readers;
named-fact runtime admission and V4 writer activation remain deferred.
NAS performance and durability remain unverified.

## Documentation and development

- **[User guide](docs/GUIDE.md)** — installation, application APIs, clients,
  both backend implementations and upgrade requirements.
- **[Developer guide](docs/DEVELOPMENT.md)** — architecture, security and storage
  invariants, verification, compatibility fixtures and releases.

For repository commands, run `make help`. The Go module, browser client and
Prisma/NestJS backend have separate dependency workspaces. Historical release
notes and migration records remain accessible through the versioned links in
the user guide and Git history.
