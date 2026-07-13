# Golem

**Write your Prisma schema. The backend comes alive — and defends itself.**

Golem generates a complete GraphQL API from your Prisma schema at runtime — no resolvers, no DTOs, no generated classes to maintain. Queries with filtering and pagination, mutations with nested writes, live subscriptions, and a full authorization kernel enforcing CASL rules on every row, column and relation hop.

```prisma
generator golem {
  provider = "golem"
}
```

```typescript
@Module({
  imports: [
    GolemModule.forRoot({
      client: GolemPrismaService,
      prismaOptions: { adapter: new PrismaPg({ connectionString: process.env.DATABASE_URL }) },
      datamodel: getDatamodel(),
    }),
    GraphQLModule.forRootAsync<ApolloDriverConfig>({
      driver: ApolloDriver,
      inject: [GOLEM_SCHEMA],
      useFactory: (schema: GraphQLSchema) => ({ schema, subscriptions: { 'graphql-ws': true } }),
    }),
  ],
})
export class AppModule {}
```

That is the whole backend. Every model in your schema now has `user`/`users` queries, `createUser`/`updateUser`/`deleteUser`/`updateManyUsers`/`deleteManyUsers` mutations with Prisma-mirroring inputs (relation-first: `connect`/`create` envelopes, no foreign-key forgery), and — per opted-in model — a `userEvents` subscription.

## Packages

| Package | Role |
|---|---|
| `@eleven-am/golem` | The NestJS module — the one you import |
| `@eleven-am/golem-core` | Engine, policy kernel, schema builder (framework-free) |
| `@eleven-am/golem-generator` | The Prisma generator (`provider = "golem"`) |
| `@eleven-am/golem-authorizer` | Authorization adapter for `@eleven-am/authorizer` (CASL) |

## Install

```bash
npm i @eleven-am/golem @eleven-am/golem-core
npm i -D @eleven-am/golem-generator
npm i @eleven-am/golem-authorizer @eleven-am/authorizer   # optional: authorization
```

Add the generator block to `schema.prisma`, run `npx prisma generate`. Three artifacts land next to your Prisma client: the datamodel, a typed instrumented client (`GolemPrismaService`), and a type map (`GolemRequest<'Post', 'create'>` etc.).

## The client: one object, two stances

```typescript
this.prisma.article.update(...)                  // act as the system: full Prisma, no policy
this.prisma.forContext(ctx).article.update(...)  // act as the caller: policy enforced
```

`forContext` returns Prisma's own delegate types (select-narrowed returns included) restricted to the policy-covered operations — `upsert` included, `aggregate`/`groupBy`/raw are compile errors. Every write through either stance publishes subscription events; events during engine transactions are buffered and only publish on commit.

## Authorization: one rules provider, every axis enforced

```typescript
@Authorizer()
export class AppRules implements WillAuthorize {
  forUser(user: SessionUser, { can, cannot }: AbilityBuilder<ResolvedAbility>) {
    can('read', 'Article', { userId: user.id });          // row constraints — compiled into queries
    can('create', 'Article', { type: 'PERSONAL' });       // verified against the REAL row, in a transaction
    can('update', 'Post', ['published']);                  // field-level write permission
    cannot('read', 'User', ['phone']);
    can('read', 'User', ['phone'], { id: user.id });       // per-row read masking
  }
}
```

With `authorization: GolemAuthorizationAdapter` configured:

- **Row constraints** merge into every query — including relation traversals (nested `where` on to-many hops, instance checks on to-one). Inaccessible rows read as `NOT_FOUND`; no existence leaks.
- **Write verification** (`defaults: { checkWriteResults: true }`): writes run in a transaction, the actual resulting row is read back and checked against the ability, and denials roll back — dynamic defaults, atomic ops (`{ increment }`) and connect-by-any-key are all exact because nothing is simulated. Nested writes are verified by relation diff. Denied writes publish no events.
- **Field permissions**: write-side by before/after column diff ("may not *change* this column"); read-side (`checkReadFields: true`) rejects never-readable fields at request time by name and masks conditionally-readable fields to `null` per row.
- **Secure by default**: enabling authorization makes the whole surface authenticated-only.

## Hooks, extensions, configuration

```typescript
@GolemHooks('Article')
export class ArticleHooks {
  @BeforeCreate()
  async prepare(req: GolemRequest<'Article', 'create'>): Promise<GolemRequest<'Article', 'create'>> { ... }
  @AfterCreate()
  async enqueue(article: GolemResult<'Article', 'create'>) { ... }
}
```

Hooks run in the engine — they apply identically to GraphQL calls and `forContext` calls. Extensions add what the generator can't know: `@ComputedField('Article', { type: 'String!', requires: ['url'] })` (its `requires` columns are fetched only when the field is requested) and `@CustomQuery`/`@CustomMutation` with SDL type references into the generated type system.

```typescript
GolemModule.forRoot({
  defaults: { maxTake: 100, maxDepth: 5, checkWriteResults: true, checkReadFields: true },
  models: {
    Article: { subscriptions: true, hidden: ['searchVector'], immutable: ['url'] },
    AuditLog: { operations: ['findMany'] },
    Session: false,
  },
  extensions: [ArticleExtension],
  authorization: GolemAuthorizationAdapter,
})
```

`hidden` removes a field from every schema surface. `immutable` allows create-only. `maxTake` and `maxDepth` reject (never silently clamp) with `BAD_USER_INPUT`. Disabled operations do not exist in the schema.

## Subscriptions

`articleEvents(where?)` per opted-in model: events carry `{ type: CREATED | UPDATED | DELETED, id, entity }`. Delivery re-fetches with the subscriber's own selection and ability — a revoked user stops receiving events mid-connection without reconnecting. The pub/sub backend is any `graphql-subscriptions` `PubSubEngine` (`pubSub` option); the in-memory default is single-instance only and says so in the logs.

## Honest limitations

- **Subscription fan-out**: delivery costs ~2 indexed queries + one ability build per event per subscriber. Fine for typical fan-outs; a known scaling wall for very hot models.
- **Events and your own transactions**: writes inside a user-initiated `prisma.$transaction` publish immediately, not on commit. Engine-managed transactions are buffered correctly.
- **Conditional read-masked fields should be nullable** in your schema — a masked `null` on a non-nullable GraphQL field triggers standard null-propagation errors.
- **Out-of-process writes** (other services, SQL consoles) are invisible to the event stream.
- **`retrieveUser` runs per request** (and per delivered event) — verify a JWT or cache it; don't hit the database every time unless you mean to.
- Batch mutations (`updateMany`/`deleteMany`) deliberately publish no events.

## License

GPL-3.0
