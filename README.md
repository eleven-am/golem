# Golem

This README documents the released TypeScript/NestJS implementation. The Go
implementation is a separate module under [`go`](./go). A TypeScript package
version or release note does not imply that a Go module version has been
published.

**Write your Prisma schema. The backend comes alive, and it defends itself.**

Golem builds a complete GraphQL API from your Prisma schema at runtime. You write no resolvers, no DTOs, no services, and there are no generated classes to maintain. Every model gets queries with filtering and pagination, mutations with nested writes, and live subscriptions. If you connect an authorization adapter, a single set of CASL rules is enforced on every row, every column, and every relation hop, across every entry point.

## Why Golem

The typical NestJS + Prisma + GraphQL app repeats the same four layers per model: a resolver that calls a service that calls a repository that calls Prisma, plus input types for each operation. None of that code is your product. Golem replaces all of it with one generator line and one module import, and keeps the parts that are your product (business rules, custom operations, access policy) in first-class, typed extension points.

What you get out of the box:

- Queries: `user(where)`, `users(where, orderBy, take, skip)` with Prisma-style filter inputs
- Mutations: `createUser`, `updateUser`, `deleteUser`, `updateManyUsers`, `deleteManyUsers`, with relation-first nested writes (`connect`, `create`, `disconnect`)
- Subscriptions: a per-model event stream with the subscriber's own field selection
- One Prisma client with two stances: plain calls act as the system, `forContext(ctx)` calls act as the caller with policy enforced
- An authorization kernel: row constraints compiled into queries, transactional write verification, field-level write permissions, per-row read masking
- Typed hooks and typed custom operations
- Guardrails on by default: query depth limits, take limits, no foreign-key forgery, no existence leaks

## Packages

| Package | Role |
|---|---|
| `@eleven-am/golem` | The NestJS module. This is the one you import. |
| `@eleven-am/golem-core` | Engine, policy kernel, and schema builder. Framework-free. |
| `@eleven-am/golem-generator` | The Prisma generator (`provider = "golem"`). |
| `@eleven-am/golem-authorizer` | Authorization adapter for `@eleven-am/authorizer` (CASL). Optional. |
| `@eleven-am/golem-queue` | Durable NestJS job queue with leases, retries, cancellation, and Prisma persistence. Optional. |
| `@eleven-am/golem-render` | Nest-native SPA hosting and route-specific link-preview metadata. Optional. |

Upgrading an existing application? Follow the [migration guide](./MIGRATION.md).

Golem supports the `postgresql` and `sqlite` datasource providers. The generator refuses any other `datasource` provider, and the engine refuses one at startup, naming it; there is no fallback path for other databases.

## Quickstart

**1. Install**

```bash
npm i @eleven-am/golem @eleven-am/golem-core
npm i -D @eleven-am/golem-generator
```

**2. Add the generator to `schema.prisma`**

```prisma
generator client {
  provider = "prisma-client"
  output   = "../src/generated/prisma"
}

generator golem {
  provider = "golem"
  output   = "../src/generated/golem"
}

model Article {
  id        String    @id @default(cuid())
  title     String
  content   String?
  published Boolean   @default(false)
  savedAt   DateTime  @default(now())
  author    User      @relation(fields: [authorId], references: [id])
  authorId  String
}
```

**3. Generate**

```bash
npx prisma generate
```

Three artifacts land in `src/generated/golem`: the datamodel, a fully typed instrumented Prisma client (`GolemPrismaService`), and a type map for hooks and programmatic calls.

**4. Wire the module**

```typescript
@Module({
  imports: [
    GolemModule.forRoot({
      client: GolemPrismaService,
      prismaOptions: { adapter: new PrismaPg({ connectionString: process.env.DATABASE_URL }) },
      datamodel: getDatamodel(),
      defaults: { maxTake: 100 },
      models: {
        Article: { subscriptions: true },
      },
    }),
    GraphQLModule.forRootAsync<ApolloDriverConfig>({
      driver: ApolloDriver,
      inject: [GOLEM_GRAPHQL],
      useFactory: (golem: GolemGraphQLArtifacts) => ({
        typeDefs: golem.typeDefs,
        transformResolvers: golem.transformResolvers,
        fieldResolverEnhancers: golem.fieldResolverEnhancers,
        subscriptions: { 'graphql-ws': true },
      }),
    }),
  ],
})
export class AppModule {}
```

If you set `GraphQLModule`'s `context`, it must be a function — `context: ({ req }) => ({ req })` — never a static object. `@nestjs/apollo` reuses a static object across requests with the first caller's `req` still attached, so every later caller would be served as the first one. Golem detects a context that belongs to another request and fails the operation instead of answering with the wrong caller's rows.

**5. Use the API**

```graphql
mutation {
  createArticle(data: {
    title: "Hello Golem"
    author: { connect: { id: "u1" } }
  }) { id savedAt }
}

query {
  articles(
    where: { published: { equals: true } }
    orderBy: [{ savedAt: desc }]
    take: 20
  ) { title author { email } }
}

subscription {
  articleEvents { type id entity { title published } }
}
```

## The client: one object, two stances

The generated `GolemPrismaService` is a real Prisma client. It carries the full Prisma API and full Prisma typing, and every write through it publishes subscription events automatically.

```typescript
// Act as the system. Full Prisma, no policy. For workers, jobs, seeds.
await this.prisma.article.update({ where: { id }, data: { status: 'READY' } });

// Act as the caller. Same typing, policy enforced.
await this.prisma.forContext(ctx).article.update({ where: { id }, data: { title } });
```

`forContext(ctx)` returns a generated, Prisma-inferred policy delegate: selections still narrow result types, but only arguments whose semantics Golem implements are present. The supported operations are `findUnique`, `findFirst`, `findMany`, `create`, `update`, `updateMany`, `upsert`, `delete`, `deleteMany`, numeric `count`, `aggregate`, and `groupBy`, plus an interactive `$transaction`. Unsupported forms such as field-selected `count`, batch mutation `limit`, raw queries, and newly added Prisma arguments remain compile errors until Golem gives them explicit policy semantics. If an argument type-checks on this surface, Golem forwards it after applying policy rather than silently ignoring it.

All three read operations merge the caller's read constraint into the query exactly as `findMany` does. A field is aggregable when it is readable on every row the merged constraint matches: under a model-level scoped grant every field qualifies, because the constraint already excludes every row the caller cannot read. Conditions the constraint does not discharge — a field-level rule or an inverted rule — are rejected by name, since a single aggregate value cannot carry per-row masking.

`forContext(ctx).$transaction(fn)` runs an interactive transaction in which every operation is the caller's policy-enforced op bound to the transaction connection. A policy denial anywhere in the callback aborts and rolls the whole transaction back, and buffered subscription events publish only if it commits. Only the callback form exists; there is no sequential-array form on the bound client.

```typescript
await this.prisma.forContext(ctx).$transaction(async (tx) => {
  const author = await tx.user.create({ data: { email } });
  await tx.post.create({ data: { title, author: { connect: { id: author.id } } } });
});
```

This boundary also applies to hooks and field configuration. Generated GraphQL operations and `forContext(ctx)` enter `GolemEngine`, so they run hooks and caller authorization. Plain delegate calls intentionally bypass both as system-level Prisma access, although their writes still publish configured subscription events. `hidden`, `readOnly`, and `writeOnly` control the generated GraphQL schema; they do not remove fields from Prisma's generated types or impose field restrictions on `forContext(ctx)` or plain delegates. Use CASL when programmatic callers also need field-level enforcement.

## Authorization

Golem does not implement its own permission language. It enforces [CASL](https://casl.js.org) rules provided through `@eleven-am/authorizer`, so your access policy lives in one rules provider and reads like a specification:

```typescript
@Authorizer()
export class AppRules implements WillAuthorize {
  forUser(user: SessionUser, { can, cannot }: AbilityBuilder<ResolvedAbility>) {
    can(['read', 'create', 'update', 'delete'], 'Article', { userId: user.id });
    can('create', 'Article', { type: 'PERSONAL' });
    can('update', 'Post', ['published']);
    cannot('read', 'User', ['phone']);
    can('read', 'User', ['phone'], { id: user.id });
  }
}
```

```typescript
GolemModule.forRoot({
  // ...
  authorization: GolemAuthorizationAdapter,
})
```

When authorization is configured, transactional write verification and read-field enforcement are enabled by default. Either can still be disabled explicitly for a deliberately row-policy-only integration.

Each rule shape maps to a specific enforcement mechanism:

| Rule | Enforcement |
|---|---|
| `can('read', 'Article', { userId })` | Compiled into the SQL `where` of every read, including relation traversals. Rows outside the ability do not exist as far as the caller can tell. |
| `can('update', 'Article', { userId })` | The update or delete statement itself carries the constraint, so authorisation and write are one atomic statement and a concurrent change cannot move the row out of policy in between. Updating someone else's row returns `NOT_FOUND`, identical to a missing row. No existence leaks. |
| `can('create', 'Article', { type: 'PERSONAL' })` | Transactional verification. The write executes, the real resulting row is read back and checked, and a denial rolls everything back. Dynamic defaults, `{ increment }`, and connect-by-any-unique-key are all handled exactly, because nothing is simulated. |
| `can('update', 'Post', ['published'])` | Field-level write permission by before/after column diff. Changing any other column is rejected with the column named. A no-op write to a restricted column passes. |
| `can('read', 'User', ['phone'], { id })` | Per-row read masking. Your own row shows the value, other rows show `null`. A field the caller could never read is rejected at request time by name. |

Additional guarantees:

- Nested writes are verified per touched model. A forbidden row cannot be smuggled through a relation envelope.
- A caller can link only to a row it can read. Every foreign key a caller writes, whether as a scalar such as `authorId` on `create`, `update`, `updateMany` or `upsert`, or through nested `connect`, `connectOrCreate`, `set` and `disconnect` at any depth, must name a row inside the caller's `read` ability. A target the caller cannot read is reported exactly like one that does not exist: `NOT_FOUND` with `<Model> not found`, so comparing an outcome with a random id reveals nothing. `connect`, `set` and `disconnect` require `read` on the target, not `update`, and a `set` or a to-one `disconnect: true` also requires `read` on every row it detaches, checked in the same transaction as the write: a linked row the caller cannot read is reported as not found and nothing changes; A write containing `connectOrCreate` runs in a transaction and decides its branch before it writes (see upsert below): an existing row is connected by its identity and must be readable by the caller, or the write is refused with `NOT_FOUND`; a missing row is created explicitly and, after the write, must be one the caller can read, or the whole write rolls back with `NOT_FOUND`; a row a concurrent writer inserts in the meantime makes that create fail with `CONFLICT` rather than be linked unchecked. A context-bound write that sets any link runs in a transaction, and each link is judged against the stored state after the write, inside that transaction, so a concurrent change to a target cannot slip a link through; on PostgreSQL the targets are also locked `FOR SHARE` until the write commits. Writes without links keep their usual path. Clients without a caller context and foreign-key values written by before hooks are not checked. For a caller with a context, a missing link target is now always `NOT_FOUND` as well; before, it surfaced as `BAD_USER_INPUT` for a foreign-key scalar, a raw provider error for a to-many `connect`, and silent success for `set`. Writing only some fields of a compound foreign key is refused with `BAD_USER_INPUT`, because the target cannot be identified; a compound foreign key with any member set to `null` references nothing and is not checked. Every caller, the unscoped client included, must write a foreign key as a value or `{ set: value }`; `increment`, `decrement`, `multiply` and `divide` on a foreign-key field are refused with `BAD_USER_INPUT` ("foreign key <Model>.<field> must be set to a value, not changed arithmetically"), and so is a write to only some fields of a compound foreign key, because Golem cannot identify, and so cannot lock, the row such a write links to. A nested `connectOrCreate` or `upsert` decides its branch under lock, outermost first, and Golem then writes the branch it decided explicitly: a `create` when the row is missing, and an `update` (or, for `connectOrCreate`, a `connect`) by the row's identity when it is present. Prisma therefore never re-decides a branch Golem did not check. Only the links of the branch that runs are checked, a branch nested under one that does not run is never decided, locked or checked, and a row that appears or disappears around the decision makes the write fail with `CONFLICT`.
- Denied writes publish no events. Event publishing is transaction-aware.
- Subscriptions re-check the ability on every delivered event. Revoking a user takes effect mid-connection, without a reconnect.
- Enabling authorization makes the entire surface authenticated-only. Unauthenticated requests receive `UNAUTHENTICATED`.
- Conditional and inverted field rules are hydrated from their exact recursive condition trees, including `is`, `isNot`, `some`, `every`, `none`, `AND`, `OR`, and `NOT`. Policy-only fields and relations are stripped before results return. A dependency or condition shape that cannot be resolved through the generated datamodel fails closed before Prisma or an in-memory check can under-select it.

## Hooks

Hooks run inside the engine, below the transport, so the same hook applies to GraphQL calls and `forContext` calls alike. Before-hooks can transform the request or veto it; after-hooks observe results.

```typescript
@GolemHooks('Article')
@Injectable()
export class ArticleHooks {
  constructor(private readonly sessions: SessionService, private readonly queue: ExtractionQueue) {}

  @BeforeCreate()
  async prepare(request: GolemRequest<'Article', 'create'>): Promise<GolemRequest<'Article', 'create'>> {
    const url = normalizeUrl(request.data.url);
    if (!isSafePublicUrl(url)) {
      throw new GolemValidationError('That URL cannot be saved');
    }
    const user = await this.sessions.userFromContext(request.context);
    return { ...request, data: { ...request.data, url, user: { connect: { id: user.id } } } };
  }

  @AfterCreate()
  async enqueue(article: GolemResult<'Article', 'create'>) {
    await this.queue.add({ articleId: article.id!, url: article.url! });
  }
}
```

`GolemRequest<'Article', 'create'>` resolves to Prisma's own input types, so `request.data` autocompletes and a misspelled field is a compile error. Hook classes are ordinary providers with full dependency injection, discovered automatically.

Before-hooks run sequentially in provider discovery order. Each hook receives the request returned by the preceding hook; returning `undefined` preserves the current request, and throwing stops the operation before Prisma runs. The final transformed request is used for authorization, nested-write checks, field checks, and the Prisma call. After-hooks run sequentially after the database operation and read-field processing succeed. They observe results and do not replace them.

| Engine operation | Before decorator | After decorator |
|---|---|---|
| `findUnique` / `findOne` | `@BeforeFindOne()` | `@AfterFindOne()` |
| `findFirst` | `@BeforeFindFirst()` | `@AfterFindFirst()` |
| `findMany` | `@BeforeFindMany()` | `@AfterFindMany()` |
| `create` | `@BeforeCreate()` | `@AfterCreate()` |
| `update` | `@BeforeUpdate()` | `@AfterUpdate()` |
| `delete` | `@BeforeDelete()` | `@AfterDelete()` |
| `updateMany` | `@BeforeUpdateMany()` | `@AfterUpdateMany()` |
| `deleteMany` | `@BeforeDeleteMany()` | `@AfterDeleteMany()` |

Every `upsert` decides its branch in one place, whether it is top-level or nested (including `connectOrCreate`) and whether it comes from GraphQL, `forContext`, the engine or the unscoped client. It takes a stripe of Golem's bounded internal guard table, reads the row its `where` selects, locks that row, and reads it again; if the row appeared or disappeared in between, the write is refused with `CONFLICT`. The decided branch is then written as an explicit create or update, never as a native Prisma upsert, so its nested writes, link checks and locks are exactly the ones that branch needs, and a concurrent insert makes the create fail with `CONFLICT` instead of silently switching branches. A typed canonical form of the model and selector is hashed into one of 4,096 stripes by default (`defaults.upsertGuardStripes`); the selector itself is never persisted. On PostgreSQL every stripe row is created once when the module starts (`prepareUpsertGuard`, which applications running the engine outside Nest must call themselves), so taking a stripe is only a row lock, in the same global order as every other Golem lock: it never waits outside that order, and a stripe that was never prepared is refused rather than skipped. The guard table is addressed through the datamodel the generator emits, so with `multiSchema` it is qualified with its model's `@@schema` exactly like every other locked table. On SQLite the guard is a write, and SQLite serializes writers. Two upserts of the same absent row therefore serialize: one creates, the other updates. Context-aware upserts additionally run exactly one create or update pipeline, so exactly that branch's hooks and its truthful `CREATED`/`UPDATED` event run once after commit. SQLite caller-owned transactions that already read before acquiring the guard may be rejected with stable `CONFLICT`. Writers outside the generated client do not take the guard.

An upsert's create input must carry the target's identity: every unique selector in `where` must be set in `create` to the same value, as a scalar or through a relation `connect` on that key, or the upsert is refused up front with `BAD_USER_INPUT` before any query runs, whichever branch would have run. The same check runs again on the create data a before-create hook returns, immediately before the create executes. This is deliberately stricter than refusing only when the create branch runs: it rejects malformed input at the boundary instead of depending on whether the row exists. `upsert({ where: { id: 5 }, create: { id: 6 } })` and a create input that leaves the id to a default are both refused. The same rule applies to nested `upsert` at any depth inside a create or update; a to-one nested upsert has no selector and nothing to check. It covers every Golem path (GraphQL, `forContext`, interactive transactions and the engine itself); a plain Prisma `upsert` on the unscoped client is raw Prisma and keeps Prisma's own semantics.

### Credentials and write-only fields

Use `writeOnly` when GraphQL must accept a secret without ever exposing it through generated read surfaces. The hook can replace the accepted value before authorization and persistence:

```typescript
GolemModule.forRoot({
  // ...
  models: {
    User: { writeOnly: ['password'], immutable: ['password'] },
  },
});

@GolemHooks('User')
@Injectable()
export class UserHooks {
  constructor(private readonly passwords: PasswordHasher) {}

  @BeforeCreate()
  async hashPassword(request: GolemRequest<'User', 'create'>) {
    return {
      ...request,
      data: {
        ...request.data,
        password: await this.passwords.hash(request.data.password),
      },
    };
  }
}
```

Here `password` exists in `UserCreateInput` but not in `User`, filters, ordering, or unique selectors. Because it is also `immutable`, generated update inputs omit it. CASL must still permit the transformed write. This is a GraphQL schema guarantee, not encryption by itself and not a restriction on `forContext()` or system-level Prisma access.

## Aggregation and analytical dimensions

Models that opt into `aggregations` receive policy-scoped `aggregate` and `groupBy` operations through GraphQL and `forContext(ctx)`. The engine merges the same read constraint used by ordinary reads before Prisma aggregates. GraphQL generates separate sum, average, minimum, and maximum value objects because Prisma's result type depends on both the source scalar and the operation:

- `BigInt` sum/min/max use the exact `BigInt` scalar and serialize as strings; Prisma's BigInt average is a `Float`.
- `Decimal` measures use the exact `Decimal` scalar and serialize the Prisma Decimal value as a string.
- `Int` min/max remain `Int`, while Int sum/average use `Float` because a sum can exceed GraphQL Int's 32-bit range.
- `forContext()` returns Prisma-native `bigint`, `Decimal`, and `number` values unchanged.
- Empty and nullable measure results stay `null`; they are never changed to zero.

Ordinary `groupBy` retains Prisma's local-column shape. Relation dimensions use a separately named, deliberately bounded operation and must be configured explicitly:

```ts
models: {
  Play: {
    aggregations: {
      dimensions: ['albumId'],
      relationDimensions: {
        artistCountry: {
          path: ['track', 'primaryArtist'],
          field: 'country',
        },
      },
      measures: ['msPlayed'],
      maxIntermediateGroups: 10_000,
      maxGroups: 100,
    },
  },
}
```

GraphQL exposes this as `playsRelationGrouped`, separately from `playsGrouped`:

```graphql
query {
  playsRelationGrouped(
    by: [albumId, artistCountry]
    measures: { count: true, sum: [msPlayed], avg: [msPlayed] }
    orderBy: { sum: { msPlayed: desc } }
    take: 20
  ) {
    key { albumId artistCountry }
    count
    sum { msPlayed }
    avg { msPlayed }
  }
}
```

The programmatic counterpart is `forContext(ctx).play.relationGroupBy()` (also present on its transaction view and `GolemEngine`), typed separately from Prisma-shaped `groupBy`. Golem first groups authorized root facts by local dimensions and relation keys, then policy-fetches every configured to-one hop and merges by the terminal value. A root fact whose target is missing or not visible uses inner-join semantics and contributes nothing. Averages are rebuilt from total sum and total non-null count, never from averages. The complete intermediate set is inspected against `maxIntermediateGroups` before final `having`, ordering, `skip`, or `take`; final ordering receives deterministic key tie-breakers and output is capped by `maxGroups`. Paths must be one or more explicit forward to-one relations. To-many, many-to-many, reverse-only, distinct relation paths, related-model measures, unreadable keys, and unreadable terminal fields fail at configuration or evaluation. Use `$scoped()` for analytical shapes outside this contract.

## Extensions

Extensions add what the generator cannot know about: computed fields and custom operations. They are declared explicitly in `forRoot` because they change the schema shape.

```typescript
@Injectable()
export class ArticleExtension {
  constructor(private readonly prisma: GolemPrismaService) {}

  @ComputedField('Article', { type: 'String!', requires: ['url'] })
  domain(@Parent() article: Pick<Article, 'url'>): string {
    return new URL(article.url).hostname;
  }

  @UseGuards(AuthorizationGuard)
  @CanPerform({ action: 'read', subject: 'Article' })
  @CustomQuery({ type: '[Article!]!', args: { term: 'String!' } })
  searchArticles(@Args() args: { term: string }, @Context() ctx: unknown) {
    return this.prisma.forContext(ctx).article.findMany({
      where: { title: { contains: args.term } },
    });
  }
}
```

Import `ComputedField` from the generated Golem module so the model and every entry in `requires` are checked against the Prisma datamodel. The `requires` list feeds the query planner: those columns are fetched only when the computed field is requested. Computed fields and custom operations are mounted as real Nest GraphQL resolvers, so Nest guards, pipes, interceptors, filters, request-scoped providers, and parameter decorators apply normally. Pass `golem.fieldResolverEnhancers` into `GraphQLModule` alongside `typeDefs` and `transformResolvers`; Nest disables guards, interceptors, and filters on field resolvers unless that option is enabled. Existing computed fields written as positional callbacks must migrate from `method(parent)` to `method(@Parent() parent)`. They inherit row and field policy when they use `forContext`.

A computed field that queries for other rows costs one query per parent row. Declare it with `@BatchedComputedField` instead and it costs one query per page: the decorated method is handed every parent key resolved in the same tick and returns a map from key to value.

```typescript
@BatchedComputedField('Article', { type: 'Int!', key: 'id' })
async commentCount(keys: readonly string[], ctx: unknown): Promise<Map<string, number>> {
  const groups = await this.prisma.forContext(ctx).comment.groupBy({
    by: ['articleId'],
    where: { articleId: { in: [...keys] } },
    _count: true,
  });
  const counts = new Map(keys.map((key) => [key, 0]));
  for (const group of groups) {
    counts.set(group.articleId, group._count);
  }
  return counts;
}
```

`key` names the parent column that identifies the row (or a function of the parent for a compound key); it is added to `requires`, so the planner fetches it. The method may also return an array aligned with `keys`, with an `Error` in any slot that failed. It receives the same `ctx` the per-row form receives, so `forContext(ctx)` enforces the caller's policy exactly as before, and the declared field `args` arrive as its third parameter — each distinct set of arguments batches on its own.

Batching is scoped to one request and, within it, to one execution. The loader is keyed by the GraphQL context object, so two requests never share a batch or a cached value and nothing survives the response; it is keyed again by the execution's root value, so a subscription — which holds one context open for the life of the connection — loads afresh for every event instead of serving the first event's answer forever. A parent whose key is null resolves to null without joining the batch. If the batch throws, every parent waiting on it receives that error. A computed field without a batch loader is untouched and still resolves per row.

## Configuration reference

```typescript
GolemModule.forRoot({
  client: GolemPrismaService,          // the generated client class
  prismaOptions: { adapter },          // passed to the PrismaClient constructor
  datamodel: getDatamodel(),           // from the generated artifacts
  pubSub: REDIS_PUBSUB,                // any graphql-subscriptions PubSubEngine; optional
  authorization: GolemAuthorizationAdapter,  // optional
  subscription: { queueCapacity: 64, observer }, // bounded local fan-out
  batchEvents: { maxRows: 1_000, maxPayloadBytes: 1_048_576 },
  extensions: [ArticleExtension],      // optional
  defaults: { /* global posture */ },
  models: { /* per-model overrides */ },
})
```

**`defaults`**

| Option | Default | Meaning |
|---|---|---|
| `operations` | all eight | Which CRUD/read operations exist, globally |
| `subscriptions` | `false` | Event streams per model |
| `maxTake` | unlimited | `take` above this is rejected with `BAD_USER_INPUT`, never silently clamped |
| `maxGroups` | relation aggregation: `100` | Final relation-aware group output cap; also the optional GraphQL-local `groupBy` cap |
| `maxIntermediateGroups` | `10,000` | Complete first-phase cap for relation-aware aggregation |
| `maxDepth` | `5` | Maximum relation nesting per query, rejected beyond |
| `checkWriteResults` | `true` with authorization | Transactional write verification and field-level write permissions |
| `checkReadFields` | `true` with authorization | Read-side field rejection and per-row masking |
| `upsertGuardStripes` | `4,096` | Bounded serialization stripes for context-aware upsert |

**`models`** (per model, overrides `defaults`)

| Option | Meaning |
|---|---|
| `false` | Model is removed from the generated GraphQL surface, but stays reachable under policy through `forContext(ctx)` and plain delegate access. Relation fields on other models that point at it are pruned with it — see below |
| `operations: [...]` | Allowlist; disabled operations do not exist in the schema |
| `hidden: [...]` | Field removed from every schema surface: types, filters, inputs |
| `immutable: [...]` | Field accepted on create, absent from update inputs |
| `readOnly: [...]` | Field remains readable, filterable, orderable, and uniquely selectable, but is absent from create and update inputs |
| `writeOnly: [...]` | Field is accepted by create and update inputs but absent from outputs, filters, ordering, and unique selectors |
| `subscriptions` | Event stream for this model |
| `maxTake` | Per-model take limit |
| `aggregations` | `true`, or local `dimensions`/`measures` plus optional named `relationDimensions`, `maxIntermediateGroups`, and `maxGroups`; relation dimensions add a separately named operation |

Field behavior is explicit across every generated GraphQL surface, including nested inputs:

| Configuration | Output/read | Filter/order/unique | Create input | Update input |
|---|---:|---:|---:|---:|
| `normal` | yes | yes | yes | yes |
| `immutable` | yes | yes | yes | no |
| `readOnly` | yes | yes | no | no |
| `writeOnly` | no | no | yes | yes |
| `writeOnly` + `immutable` | no | no | yes | no |
| `hidden` | no | no | no | no |

Configuration is validated while the schema is built. Unknown fields, write-only primary keys or relations, and conflicting access modes fail startup with the model and field named. `writeOnly` plus `immutable` is the supported combined mode; other overlapping access modes are rejected rather than resolved implicitly.

When authorization is present and `checkReadFields` is enabled, every visible scalar and enum output field is nullable even when its database column is required. A field check may truthfully mask that value to `null`; the containing object, relation list, and event remain intact. Input requiredness still follows Prisma, relation list structure is unchanged, and event identities remain non-null after event authorization. Disable field checks explicitly if you need the old Prisma-required output nullability, then regenerate GraphQL client types.

## Errors

All failures surface as GraphQL errors with stable extension codes and no Prisma internals:

| Code | Meaning |
|---|---|
| `BAD_USER_INPUT` | Validation, hook veto, take or depth limit exceeded, relation constraint violation, a string containing a NUL byte, an upsert create input that does not set its target |
| `NOT_FOUND` | Row missing, or existing but outside the caller's ability |
| `CONFLICT` | Unique constraint violation |
| `UNAUTHENTICATED` | No resolvable user while authorization is enabled |
| `FORBIDDEN` | The ability denies the action, row, or named field |

A context-bound update may not change the fields that identify a row (its primary key, or the unique field Golem identifies it by when it has none), at the root or in a nested update; such an update is refused with `BAD_USER_INPUT` before any query. A string or JSON object key containing a NUL byte (`\u0000`) is refused with `BAD_USER_INPUT` before the statement that carries it runs, wherever it appears: a write payload, a filter, a JSON value or a raw query parameter, on the context-bound client, through GraphQL, and on the unscoped client. On SQLite such strings used to be stored and then matched predicates incorrectly, because SQLite's string functions stop at NUL; PostgreSQL rejected them with a provider error. Rows already stored that way still read.

## Subscriptions in detail

Each opted-in model gets `articleEvents(where?)` emitting `{ type: CREATED | UPDATED | DELETED, id, entity }`. A single reference-counted local hub owns the event-bus iterator for each model/schema instance, opening it for the first subscriber and closing it after the last. Delivery still re-fetches with the subscriber's current context, selection, filter, and ability. Evaluation is shared only within one event when the exact context object and canonical filter/selection match; it is never shared across callers.

Each consumer queue holds 64 events by default. Set `subscription.queueCapacity` to change it. A slow consumer that fills the queue is disconnected with `GOLEM_SUBSCRIPTION_OVERFLOW`; no event is silently dropped. `GolemSubscriptionObserver` reports active subscriptions, received events, evaluations and latency, deliveries, suppression reasons, queue depth, and overflow disconnects. The event transport is versioned and safely round-trips BigInt, Decimal, Date, bytes, deletion snapshots, composite identities, and batch envelopes through JSON-like buses.

Single-column models keep a scalar event `id`. Composite-`@@id` models expose a model-specific non-null identity object containing the ordered key components. Named and unnamed compound `@@id` and `@@unique` selectors are available in generated find-one, update, delete, upsert, connect, connect-or-create, and nested update/delete inputs.

Top-level `updateMany` and `deleteMany` emit deterministic per-row events for subscribable models across GraphQL, `forContext`, plain generated delegates, and generated interactive transactions. The default limits are 1,000 rows and 1 MiB encoded payload; exceeding either rejects before mutation and never truncates. Eventful `updateMany` refuses primary-key updates. `deleteMany` snapshots and deletes exactly the selected identities and rolls back on a count mismatch. Events remain buffered until commit and are discarded on rollback. Delivery is commit-aware and in-process, not durable or exactly-once: a process crash after the database commit but before publication can still lose events, and out-of-process writes remain invisible.

Cascaded deletes produce change events. A `deleteMany` honours its `limit`, deleting no more rows than it names (in primary-key order), and an argument Golem cannot honour is refused with `BAD_USER_INPUT` rather than ignored. Every delete through the generated client, whether `delete`, `deleteMany`, or a nested `delete`/`deleteMany` inside an `update` or `upsert`, and whether it comes from GraphQL, `forContext`, a transaction or the unscoped client, runs one path. It reads and locks the rows it removes and every dependent the database will change through `onDelete: Cascade`, `SetNull` or `SetDefault`, in the same transaction, then deletes. It enumerates those rows, locks them all, then reads them again under the locks; if the set changed in between, the delete is refused with `CONFLICT` rather than acting on rows it did not check. On PostgreSQL every Golem write locks every row it changes or links to: `create`, `createMany`, `update`, `updateMany`, `upsert`, nested writes and deletes, single-row writes included, from GraphQL, `forContext` or the unscoped client. A write outside a transaction opens one for this, and an array `$transaction([...])` on the generated client runs its operations in order inside one interactive transaction, so they take the same locks and commit or roll back together. A nested `update`, `updateMany`, `delete` or `deleteMany` reads the rows it will touch, locks them, and reads them again; if the set changed in between, the write is refused with `CONFLICT`. All these locks are taken in one order (model name, then row identity) through a single lock set per transaction, so concurrent Golem writes over the same rows serialize instead of deadlocking; a lock that a later step of the same transaction needs on a row sorting before ones it already holds is taken without waiting, and if another write holds it the write is refused with `CONFLICT`, and once a delete holds a parent a concurrently inserted dependent waits for it and then fails instead of being removed unseen; SQLite already serializes writers. Each cascaded row then emits `DELETED` with its snapshot, and each row whose foreign key the database clears or resets emits `UPDATED`. Permission to delete the parent covers its dependents, as before, and subscribers still receive only the events their own read policy allows. A delete whose rows plus dependents would exceed `batchEvents.maxRows` (1,000 by default) is refused whole with `BAD_USER_INPUT`, before anything is deleted. The count includes every dependent, including rows the caller cannot read and rows of models nobody subscribes to, so the refusal can tell a caller that more dependents exist than the limit allows. Rows are identified by their primary key or, for a model without one, by its first required `@unique` field, else its first `@@unique` over required fields; every Golem path (link checks, upsert, verification, events and deletes) uses this one identity, and a delete from a model with neither is refused. On PostgreSQL with `multiSchema`, the cascade reads qualify each table with its model's `@@schema`. A subscribable model whose identity includes a foreign key declared `onDelete: SetNull` or `SetDefault` is refused when the module starts, because the cascade would rewrite the identity its change event must name, and the new value of a database default cannot be known. Regenerate to emit each relation's `onDelete` into the datamodel; Prisma's defaults (`SetNull` for an optional relation, `Restrict` for a required one) are emitted when none is declared.

## Known limitations

Stated here because you will hit them eventually, and finding them in a README beats finding them in production:

- **Subscription evaluation remains policy-local.** One event-bus iterator fans out locally, but each distinct context/filter/selection group still performs its own policy-scoped evaluation. Golem deliberately does not share unrestricted rows or results across users.
- **Transactions.** Writes through the generated client are buffered until `prisma.$transaction` commits and discarded on rollback. `forContext(ctx).$transaction(fn)` extends this to policy-enforced callers with the callback form only — there is no sequential-array (`$transaction([...])`) form on the bound client. Out-of-process transactions remain outside Golem's event boundary.
- **Aggregates are read-only and hook-free.** `count`, `aggregate`, and `groupBy` merge the caller's read constraint but run no `before`/`after` hooks. They fail closed on any field that is not readable on every row the merged constraint matches — a `never` field, a field-level condition, or an inverted rule — rejected by name, since one aggregate value cannot carry per-row masking. A model-level scoped grant discharges its own conditions, so every field on such a model aggregates normally.
- **Serialized upsert is cooperative.** The striped guard serializes every Golem upsert using the same canonical model/selector. Writers outside the generated client and differently addressed selectors do not participate; a row they insert or delete while a Golem upsert decides is refused with `CONFLICT` rather than switching the branch. SQLite caller-owned transactions that already established an incompatible snapshot receive stable `CONFLICT`.
- **`maxGroups` bounds the generated GraphQL surface only.** With it set, a `groupBy` that supplies no `take` is fetched with a `take` of `maxGroups + 1` and refused if the extra group appears — bounded, and never silently truncated. An explicit `take` above the cap is refused outright. The programmatic `forContext` client is deliberately uncapped: a developer asking for every distinct group is not an anonymous caller asking for one. Note that Prisma requires any `orderBy` on a `groupBy` to use only fields present in `by`; when a `take` is in play and you supplied no ordering, Golem orders by the grouping keys so the page is deterministic.
- **Authorized schemas make scalar outputs nullable.** With field checks enabled, required database scalar/enum columns are nullable in GraphQL so a genuine per-row mask does not null-propagate through the containing object. Inputs remain Prisma-required where applicable.
- **`BigInt` columns serialize as strings** over GraphQL, since values can exceed `2^53` and JSON cannot carry a raw bigint. Inputs accept an integer string or an integer literal; fractional numbers, unsafe integer numbers, and non-numeric strings are rejected.
- **`Decimal` columns and Decimal aggregates serialize as strings** over GraphQL. Golem preserves the exact Prisma Decimal value; database/provider precision remains the database's responsibility (for example, SQLite numeric aggregation can already be approximate before Prisma returns it).
- **BigInt policy conditions are exact by default.** The default ability — used when your `Authenticator` leaves `abilityFactory` off — compares mixed `BigInt`/number operands exactly (fail-closed on non-numeric and `NaN` operands), so the in-memory checks — read field masking, transactional write verification, and the subscription delete re-check — are exact at any magnitude, with no `2^53` ceiling. If you override `abilityFactory`, build it with `createGolemAbility` from `@eleven-am/golem-authorizer`; the adapter verifies that its condition matcher agrees with Golem's operator table and refuses to boot when it does not. The row-level query filter compiles to SQL and was always exact. List-membership operators (`has`, `hasSome`, `hasEvery`) are BigInt-exact for mixed `BigInt`/number element pairs; non-numeric elements keep JavaScript `Array.includes` (`SameValueZero`) semantics.
- **Out-of-process writes** (another service, a SQL console) are invisible to the event stream.
- **`retrieveUser` runs per request** and per delivered subscription event. Verify a JWT or cache the lookup; only hit the database on purpose.
- **Nested/out-of-process batches are not captured.** Per-row batch events cover top-level generated GraphQL, `forContext`, plain generated delegates, and generated interactive transactions, for `createMany`, `createManyAndReturn`, `updateMany` and `updateManyAndReturn` as well as single-row writes. Every intercepted operation returns exactly what Prisma returns for it, with the caller's `select`, `include` and `omit` applied: a row, an array of rows, or `{ count }`. Nested creates and updates, and writes made outside the generated client, remain outside the event boundary. Nested deletes, and every row a delete cascades to, are captured.
- **Relation-aware aggregation is bounded to one forward to-one path.** It supports local measures and local plus terminal dimensions. To-many/many-to-many traversal, multiple relation paths, and related-model measures remain `$scoped()` territory. Ordinary programmatic local `groupBy` remains deliberately uncapped by GraphQL's `maxGroups`.
- **Excluding a model prunes the relations pointing at it.** A relation field cannot reference a GraphQL type that does not exist, so `Artist.genres` disappears from the surface when the join model `ArtistGenre` is `false`. This is silent, and you notice it as an absence rather than an error. Expose what you actually want through a `@ComputedField` — `Artist.genreNames: [String!]!` — which is the better API in any case, since a join table is a storage decision and not something an API should promise. Computed fields resolve per row, so a computed field backed by its own query costs one query per row of the parent list unless it is declared with `@BatchedComputedField`.

## License

GPL-3.0
