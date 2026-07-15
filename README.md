# Golem

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

Upgrading an existing application? Follow the [0.1.0 migration guide](./MIGRATION.md).

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
        subscriptions: { 'graphql-ws': true },
      }),
    }),
  ],
})
export class AppModule {}
```

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

`forContext(ctx)` returns Prisma's own delegate types (select-narrowed return types included) restricted to the policy-covered operations: `findUnique`, `findFirst`, `findMany`, `create`, `update`, `updateMany`, `upsert`, `delete`, `deleteMany`. Operations without defined policy semantics (`aggregate`, `groupBy`, raw queries) are compile errors on the bound client, not runtime surprises.

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

`upsert` selects Golem's create or update pipeline, including that branch's hooks. Hooks do not run for plain Prisma delegate calls.

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

## Extensions

Extensions add what the generator cannot know about: computed fields and custom operations. They are declared explicitly in `forRoot` because they change the schema shape.

```typescript
@Injectable()
export class ArticleExtension {
  constructor(private readonly prisma: GolemPrismaService) {}

  @ComputedField('Article', { type: 'String!', requires: ['url'] })
  domain(article: Pick<Article, 'url'>): string {
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

The `requires` list feeds the query planner: those columns are fetched only when the computed field is requested. Custom operations reference the generated type system by SDL name (`'[Article!]!'`, `'ArticleWhereInput'`) and are mounted as real Nest GraphQL resolvers, so Nest guards, pipes, interceptors, filters, and parameter decorators apply normally. They inherit row and field policy when they use `forContext`.

## Configuration reference

```typescript
GolemModule.forRoot({
  client: GolemPrismaService,          // the generated client class
  prismaOptions: { adapter },          // passed to the PrismaClient constructor
  datamodel: getDatamodel(),           // from the generated artifacts
  pubSub: REDIS_PUBSUB,                // any graphql-subscriptions PubSubEngine; optional
  authorization: GolemAuthorizationAdapter,  // optional
  extensions: [ArticleExtension],      // optional
  defaults: { /* global posture */ },
  models: { /* per-model overrides */ },
})
```

**`defaults`**

| Option | Default | Meaning |
|---|---|---|
| `operations` | all seven | Which operations exist, globally |
| `subscriptions` | `false` | Event streams per model |
| `maxTake` | unlimited | `take` above this is rejected with `BAD_USER_INPUT`, never silently clamped |
| `maxDepth` | `5` | Maximum relation nesting per query, rejected beyond |
| `checkWriteResults` | `true` with authorization | Transactional write verification and field-level write permissions |
| `checkReadFields` | `true` with authorization | Read-side field rejection and per-row masking |

**`models`** (per model, overrides `defaults`)

| Option | Meaning |
|---|---|
| `false` | Model does not appear in the API at all |
| `operations: [...]` | Allowlist; disabled operations do not exist in the schema |
| `hidden: [...]` | Field removed from every schema surface: types, filters, inputs |
| `immutable: [...]` | Field accepted on create, absent from update inputs |
| `readOnly: [...]` | Field remains readable, filterable, orderable, and uniquely selectable, but is absent from create and update inputs |
| `writeOnly: [...]` | Field is accepted by create and update inputs but absent from outputs, filters, ordering, and unique selectors |
| `subscriptions` | Event stream for this model |
| `maxTake` | Per-model take limit |

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

Each opted-in model gets `articleEvents(where?)` emitting `{ type: CREATED | UPDATED | DELETED, id, entity }`. Delivery re-fetches the row with the subscriber's own selection and ability, so every subscriber sees exactly what they are allowed to see. Filters evaluate in the database. For production, provide a shared `PubSubEngine` (for example `graphql-redis-subscriptions`); the in-memory default only works on a single instance and logs a warning saying so.

## Known limitations

Stated here because you will hit them eventually, and finding them in a README beats finding them in production:

- **Subscription fan-out.** Delivery costs about two indexed queries plus one ability build per event per subscriber. Fine for typical fan-outs, a scaling consideration for very hot models with thousands of subscribers.
- **Transactions.** Writes through the generated client are buffered until `prisma.$transaction` commits and discarded on rollback. Out-of-process transactions remain outside Golem's event boundary.
- **Conditional read-masked fields should be nullable** in your schema. A masked `null` on a non-nullable GraphQL field produces a standard null-propagation error.
- **Out-of-process writes** (another service, a SQL console) are invisible to the event stream.
- **`retrieveUser` runs per request** and per delivered subscription event. Verify a JWT or cache the lookup; only hit the database on purpose.
- **Batch mutations publish no events.** `updateMany` and `deleteMany` return counts, and there are deliberately no per-row events for them.

## License

GPL-3.0
