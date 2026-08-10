# Golem

This README documents the released TypeScript/NestJS implementation. The Go
implementation is a separate, unreleased module under [`go`](./go); its current
capabilities, explicit non-claims, and release evidence are documented in
[`docs/golem-go`](./docs/golem-go/README.md). A TypeScript package version or
release note does not imply that a Go module version has been published.

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
| `can('update', 'Article', { userId })` | Fetch-then-mutate. Updating someone else's row returns `NOT_FOUND`, identical to a missing row. No existence leaks. |
| `can('create', 'Article', { type: 'PERSONAL' })` | Transactional verification. The write executes, the real resulting row is read back and checked, and a denial rolls everything back. Dynamic defaults, `{ increment }`, and connect-by-any-unique-key are all handled exactly, because nothing is simulated. |
| `can('update', 'Post', ['published'])` | Field-level write permission by before/after column diff. Changing any other column is rejected with the column named. A no-op write to a restricted column passes. |
| `can('read', 'User', ['phone'], { id })` | Per-row read masking. Your own row shows the value, other rows show `null`. A field the caller could never read is rejected at request time by name. |

Additional guarantees:

- Nested writes are verified per touched model. A forbidden row cannot be smuggled through a relation envelope.
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

Context-aware `upsert` serializes branch selection through Golem's bounded internal guard table. A typed canonical form of the model and unique selector is hashed into one of 4,096 stripes by default; the selector itself is never persisted. In an engine-owned transaction the guard acquisition is the first statement, followed by the policy-scoped probe and exactly one create or update pipeline, so exactly that branch's hooks and truthful `CREATED`/`UPDATED` event run once after commit. Caller-owned transactions participate in the same guard, but SQLite may reject a snapshot that has already read before acquiring it; Golem reports that case as stable `CONFLICT` rather than leaking a provider error. The guarantee covers participating Golem context-aware upserts using the same model and canonical selector. Plain Prisma, external writers, and writes addressing the row through a different selector remain outside it. Hooks do not run for plain Prisma delegate calls.

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
| `BAD_USER_INPUT` | Validation, hook veto, take or depth limit exceeded, relation constraint violation |
| `NOT_FOUND` | Row missing, or existing but outside the caller's ability |
| `CONFLICT` | Unique constraint violation |
| `UNAUTHENTICATED` | No resolvable user while authorization is enabled |
| `FORBIDDEN` | The ability denies the action, row, or named field |

## Subscriptions in detail

Each opted-in model gets `articleEvents(where?)` emitting `{ type: CREATED | UPDATED | DELETED, id, entity }`. A single reference-counted local hub owns the event-bus iterator for each model/schema instance, opening it for the first subscriber and closing it after the last. Delivery still re-fetches with the subscriber's current context, selection, filter, and ability. Evaluation is shared only within one event when the exact context object and canonical filter/selection match; it is never shared across callers.

Each consumer queue holds 64 events by default. Set `subscription.queueCapacity` to change it. A slow consumer that fills the queue is disconnected with `GOLEM_SUBSCRIPTION_OVERFLOW`; no event is silently dropped. `GolemSubscriptionObserver` reports active subscriptions, received events, evaluations and latency, deliveries, suppression reasons, queue depth, and overflow disconnects. The event transport is versioned and safely round-trips BigInt, Decimal, Date, bytes, deletion snapshots, composite identities, and batch envelopes through JSON-like buses.

Single-column models keep a scalar event `id`. Composite-`@@id` models expose a model-specific non-null identity object containing the ordered key components. Named and unnamed compound `@@id` and `@@unique` selectors are available in generated find-one, update, delete, upsert, connect, connect-or-create, and nested update/delete inputs.

Top-level `updateMany` and `deleteMany` emit deterministic per-row events for subscribable models across GraphQL, `forContext`, plain generated delegates, and generated interactive transactions. The default limits are 1,000 rows and 1 MiB encoded payload; exceeding either rejects before mutation and never truncates. Eventful `updateMany` refuses primary-key updates. `deleteMany` snapshots and deletes exactly the selected identities and rolls back on a count mismatch. Events remain buffered until commit and are discarded on rollback. Delivery is commit-aware and in-process, not durable or exactly-once: a process crash after the database commit but before publication can still lose events, and out-of-process writes remain invisible.

## Known limitations

Stated here because you will hit them eventually, and finding them in a README beats finding them in production:

- **Subscription evaluation remains policy-local.** One event-bus iterator fans out locally, but each distinct context/filter/selection group still performs its own policy-scoped evaluation. Golem deliberately does not share unrestricted rows or results across users.
- **Transactions.** Writes through the generated client are buffered until `prisma.$transaction` commits and discarded on rollback. `forContext(ctx).$transaction(fn)` extends this to policy-enforced callers with the callback form only — there is no sequential-array (`$transaction([...])`) form on the bound client. Out-of-process transactions remain outside Golem's event boundary.
- **Aggregates are read-only and hook-free.** `count`, `aggregate`, and `groupBy` merge the caller's read constraint but run no `before`/`after` hooks. They fail closed on any field that is not readable on every row the merged constraint matches — a `never` field, a field-level condition, or an inverted rule — rejected by name, since one aggregate value cannot carry per-row masking. A model-level scoped grant discharges its own conditions, so every field on such a model aggregates normally.
- **Serialized upsert is cooperative.** The striped guard serializes participating Golem context-aware calls using the same canonical model/selector. Plain Prisma, external writers, and differently addressed selectors do not participate. SQLite caller-owned transactions that already established an incompatible snapshot receive stable `CONFLICT`.
- **`maxGroups` bounds the generated GraphQL surface only.** With it set, a `groupBy` that supplies no `take` is fetched with a `take` of `maxGroups + 1` and refused if the extra group appears — bounded, and never silently truncated. An explicit `take` above the cap is refused outright. The programmatic `forContext` client is deliberately uncapped: a developer asking for every distinct group is not an anonymous caller asking for one. Note that Prisma requires any `orderBy` on a `groupBy` to use only fields present in `by`; when a `take` is in play and you supplied no ordering, Golem orders by the grouping keys so the page is deterministic.
- **Authorized schemas make scalar outputs nullable.** With field checks enabled, required database scalar/enum columns are nullable in GraphQL so a genuine per-row mask does not null-propagate through the containing object. Inputs remain Prisma-required where applicable.
- **`BigInt` columns serialize as strings** over GraphQL, since values can exceed `2^53` and JSON cannot carry a raw bigint. Inputs accept an integer string or an integer literal; fractional numbers, unsafe integer numbers, and non-numeric strings are rejected.
- **`Decimal` columns and Decimal aggregates serialize as strings** over GraphQL. Golem preserves the exact Prisma Decimal value; database/provider precision remains the database's responsibility (for example, SQLite numeric aggregation can already be approximate before Prisma returns it).
- **BigInt policy conditions are exact by default.** The default ability — used when your `Authenticator` leaves `abilityFactory` off — compares mixed `BigInt`/number operands exactly (fail-closed on non-numeric and `NaN` operands), so the in-memory checks — read field masking, transactional write verification, and the subscription delete re-check — are exact at any magnitude, with no `2^53` ceiling. If you override `abilityFactory`, build it with `createGolemAbility` from `@eleven-am/golem-authorizer`; the adapter verifies that its condition matcher agrees with Golem's operator table and refuses to boot when it does not. The row-level query filter compiles to SQL and was always exact. List-membership operators (`has`, `hasSome`, `hasEvery`) are BigInt-exact for mixed `BigInt`/number element pairs; non-numeric elements keep JavaScript `Array.includes` (`SameValueZero`) semantics.
- **Out-of-process writes** (another service, a SQL console) are invisible to the event stream.
- **`retrieveUser` runs per request** and per delivered subscription event. Verify a JWT or cache the lookup; only hit the database on purpose.
- **Nested/out-of-process batches are not captured.** Per-row batch events cover top-level generated GraphQL, `forContext`, plain generated delegates, and generated interactive transactions. Nested batch writes and writes made outside the generated client remain outside the event boundary.
- **Relation-aware aggregation is bounded to one forward to-one path.** It supports local measures and local plus terminal dimensions. To-many/many-to-many traversal, multiple relation paths, and related-model measures remain `$scoped()` territory. Ordinary programmatic local `groupBy` remains deliberately uncapped by GraphQL's `maxGroups`.
- **Excluding a model prunes the relations pointing at it.** A relation field cannot reference a GraphQL type that does not exist, so `Artist.genres` disappears from the surface when the join model `ArtistGenre` is `false`. This is silent, and you notice it as an absence rather than an error. Expose what you actually want through a `@ComputedField` — `Artist.genreNames: [String!]!` — which is the better API in any case, since a join table is a storage decision and not something an API should promise. Computed fields resolve per row, so a computed field backed by its own query costs one query per row of the parent list unless it is declared with `@BatchedComputedField`.

## License

GPL-3.0
