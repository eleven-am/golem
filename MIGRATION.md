# Migrating `@eleven-am/golem-queue` to 0.5.0

Add two things to your schema and migrate:

```prisma
model Job {
  // ...
  startedAt      DateTime?

  @@index([type, startedAt])
}

model JobGuard {
  key         String    @id
  seq         BigInt    @default(0)
  windowStart DateTime?
  spent       BigInt    @default(0)
}
```

`JobGuard` is the serialization point for claim guards. Every worker competing for the same guard writes one shared row before reading, which is what makes the guard hold across processes and across engines. Rows are created as needed; there is nothing to seed or prune.

`startedAt` records when a job most recently entered RUNNING, set on every claim including lease recovery. Rate budgets count starts inside a sliding window, and `prune` uses it to keep rows that are still inside one.

## `excludes` splits into two constraints

`excludes` promised non-overlap and delivered something else: it stopped the declaring type from *starting*, but the other type was free to start while the declarer was already running. Replace it with whichever you actually meant.

```diff
-@QueueHandler({ type: 'track-hydrate', excludes: ['history-import'] })
+@QueueHandler({ type: 'track-hydrate', notWhileRunning: ['history-import'] })
+@QueueHandler({ type: 'history-import', notWhileRunning: ['track-hydrate'] })
```

`notWhileRunning` prevents overlap and is safe to declare on both sides — **declare it on both**, or the undeclared side can still start underneath the other. Use `waitsFor` instead if you meant "drain that queue before I run"; it blocks on outstanding work rather than only running work, and so remains one-way.

## The `table` option on `PrismaJobStore` is gone

It only existed to interpolate a table name into hand-written SQL. Claims now go through Prisma's query API, which resolves `@@map` itself, so the option had no effect. Delete it; your mapped table keeps working.

## Custom stores must declare that they enforce guards

If you implement `JobStore` yourself and any handler uses `serializeByScope`, `waitsFor`, `notWhileRunning`, or a resource pool, evaluate those guards inside the statement or transaction that claims, then set:

```ts
readonly enforcesClaimGuards = true;
```

The dispatcher refuses to start otherwise. The guards are optional fields on `ClaimInput`, so a store written before they existed type-checks and silently runs every guarded job unguarded — the refusal replaces that with a loud failure.

# Migrating `@eleven-am/golem-queue` to 0.3.0

A handler now receives one `JobEvent` instead of a payload and an execution object. Breaking, and mechanical.

```diff
-async handle(payload: Record<string, unknown>, { signal }: JobExecution) {
-  const articleId = payload.articleId as string;
+async handle({ payload, signal }: JobEvent<'article.extract'>) {
+  const articleId = payload.articleId;      // typed, no cast
 }
```

Add the interface so the payload is checked against your registration:

```diff
-export class ExtractHandler {
+export class ExtractHandler implements JobWork<'article.extract'> {
```

The decorator is unchanged. As in `@eleven-am/authorizer`, the decorator registers and the interface types — `@QueueHandler` is to `JobWork` what `@Authorizer` is to `WillAuthorize`, and as there, parameters are annotated explicitly.

## What a JobEvent carries

`id`, `type`, `payload`, `attempt`, `maxAttempts`, `scope`, and `signal`.

`attempt` and `maxAttempts` were previously unreachable even though the dispatcher had them, so a handler could not tell whether it was on its last try. It can now:

```ts
if (res.status === 429 && attempt < maxAttempts) throw new RetryableJobError('rate limited', 60_000);
if (res.status === 429) await this.notifyGaveUp(payload.articleId);
```

## Typing is optional

Handlers written as `implements JobWork` without a job type still compile, and `queue.add` still accepts any string, unless you declare a `jobs` map. See the README.

# Migrating to 0.5.1

Regenerate first: `npx prisma generate`. This release changes what the generated module emits.

## Registration is global, and missing registration now fails loudly

The generated module registers your schema through a global `GolemRegister` interface rather than a module augmentation. You never write or read it; the change matters for two reasons.

**Any Golem package can now read the registration without depending on the others.** That is what lets `@eleven-am/golem-queue` type job payloads while keeping zero dependency on `@eleven-am/golem`.

**Forgetting to regenerate is now a compile error rather than silent type loss.** Previously an unregistered schema fell back to permissive types, so model names stopped being checked and nothing said so. Now the first decorated model name reports:

```
Argument of type '"Article"' is not assignable to parameter of type
'"GOLEM_SCHEMA_NOT_REGISTERED_RUN_PRISMA_GENERATE"'.
```

If you see that, run `npx prisma generate`.

## Typed queue jobs (optional)

`queue.add` and `@QueueHandler` now narrow against a job map, if you declare one:

```typescript
declare global {
  interface GolemRegister {
    jobs: {
      'article-extract': { articleId: string };
      'article-summarize': { articleId: string; model: string };
    };
  }
}
```

A typo'd job type and a payload the handler does not expect both become compile errors. Job types have no source Golem can generate from, so this block is written by hand — put it anywhere in your program.

Unlike model registration, **declaring jobs stays optional**: if you omit the block, `queue.add` accepts any string and any payload exactly as before. Model registration fails closed because the generator always provides it, so its absence means you did not regenerate. Nothing provides job types, so their absence means you chose not to type them.

# Migrating to 0.5.0

Golem now registers your schema with the package itself, so decorators and hook payload types are checked against your models without importing anything from the generated folder. Regenerate first: `npx prisma generate`.

## Import decorators and payload types from the package

The generated module now emits a `declare module` block that registers your models and Prisma types with `@eleven-am/golem`. You never write or read it. What changes is where you import from:

```diff
-import { AfterCreate, BeforeCreate, GolemHooks } from '@eleven-am/golem';
-import { GolemRequest, GolemResult } from './generated/golem/types';
+import { AfterCreate, BeforeCreate, GolemHooks, GolemRequest, GolemResult } from '@eleven-am/golem';

-import { ComputedField } from './generated/golem';
+import { ComputedField } from '@eleven-am/golem';
```

`ComputedField` is no longer re-exported from the generated module; import it from `@eleven-am/golem`. It stays fully typed. You still import `getDatamodel()` and the generated client from `./generated/golem` when wiring the module.

## Model names are now checked

`@GolemHooks`, `@ComputedField`, `GolemRequest`, and `GolemResult` all narrow their model parameter to your actual models, so a typo is a compile error rather than a hook that silently never fires. Passing a `string` variable where a literal is expected no longer compiles.

Unknown hook models are also refused at boot, naming the model and listing the known ones, so the check still holds if the generated module is missing from your TypeScript program.

# Migrating to 0.4.0

This release makes computed fields genuine Nest GraphQL field resolvers. It is intentionally breaking for existing positional computed-field methods.

## Migrate computed fields to Nest parameters

Regenerate the Golem artifacts, import the typed `ComputedField` helper from the generated Golem module, and annotate the parent explicitly:

```typescript
import { Parent } from '@nestjs/graphql';
import { ComputedField } from './generated/golem';

@ComputedField('Article', { type: 'String!', requires: ['url'] })
domain(@Parent() article: Pick<Article, 'url'>): string {
  return new URL(article.url).hostname;
}
```

The previous `domain(article)` positional form no longer receives its parent. The generated decorator checks both the model and `requires` field names. Computed fields may now declare GraphQL arguments with `args` and receive them through `@Args()`.

## Enable Nest field-resolver enhancers

Pass the new artifact option through the GraphQL module:

```typescript
GraphQLModule.forRootAsync<ApolloDriverConfig>({
  driver: ApolloDriver,
  inject: [GOLEM_GRAPHQL],
  useFactory: (golem: GolemGraphQLArtifacts) => ({
    typeDefs: golem.typeDefs,
    transformResolvers: golem.transformResolvers,
    fieldResolverEnhancers: golem.fieldResolverEnhancers,
  }),
})
```

Nest disables guards, interceptors, and filters on field resolvers unless this option is enabled. Parameter decorators, pipes, request-scoped providers, guards, interceptors, and Golem exception mapping now run through Nest rather than a Golem-bound callback.

For relation-backed computed fields, use a request-scoped DataLoader. Golem does not add a separate batching API.

## Optional SPA rendering package

`@eleven-am/golem-render` is a new independent package for hosting a compiled SPA and injecting route-specific Open Graph/Twitter metadata. It has no dependency on Golem Core and adds no authorization behavior. See `packages/render/README.md` before exposing unscoped metadata.

---

# Historical: migrating to 0.3.0

This release tightens the policy-aware TypeScript contract, makes GraphQL aggregation precision-safe, closes recursive authorization hydration gaps, and hardens the queue. Regenerate the client after upgrading:

```bash
npx prisma generate
```

## Review `forContext()` compile errors

The context-bound client now exposes an explicit allowlist of arguments that Golem forwards with policy semantics. Projection-sensitive results still use Prisma inference, but unsupported arguments no longer appear merely because Prisma added them to its raw delegate.

Intentional exclusions include:

- `select` on `count`; the supported form returns `number`.
- `limit` on `updateMany` and `deleteMany`.
- raw queries, `createMany`, and other operations without a Golem policy pipeline.

`aggregate` now forwards `orderBy`, `cursor`, `take`, and `skip`. Hook request types now include the model's real `select` and `include` types. Treat a new compile error as a boundary decision: use a supported policy-aware form, or make an explicitly system-level plain Prisma call where that is genuinely intended.

## Review aggregate GraphQL contracts

Generated sum, average, minimum, and maximum fields now use separate output objects and scalar types. BigInt sum/min/max values and all Decimal measures serialize as strings; BigInt average follows Prisma and remains `Float`. Any client code that previously expected a lossy JSON number for a BigInt aggregate must switch to a string/BigInt-aware parser.

The new `Decimal` scalar also enables ordinary Decimal model fields. It serializes Prisma Decimal values exactly as strings.

## Review context-aware upsert retries

`forContext().model.upsert()` performs a policy-scoped branch probe and then runs either the create or update pipeline so exactly that branch's hooks and verification execute. It is not Prisma's atomic native upsert. Concurrent missing-row writers can race; a unique race is reported as Golem `CONFLICT`. Retry only when repeating the entire operation is safe.

## Queue validation changes

`@eleven-am/golem-queue` now rejects invalid timing/retention options and invalid or duplicate handlers during startup. Enqueue rejects empty metadata, invalid dates/attempt counts, BigInt payloads, circular references, and unsupported JSON values with a payload-safe error. `retryFailed()` no longer stops at the human-facing 100-row listing default, and `PrismaJobStore.countByStatus()` now counts in the database.

If an application accidentally depended on JSON silently dropping `undefined`, functions, or symbols from job payloads, normalize that payload before enqueueing.

---

# Historical: migrating to 0.1.0

This release changes the Nest GraphQL integration, strengthens authorization defaults, and updates the generated Prisma client. Existing applications should make the following changes.

## Replace `GOLEM_SCHEMA` with `GOLEM_GRAPHQL`

Golem now lets Nest own custom resolver execution while merging Golem's generated resolvers into the same GraphQL module.

```typescript
import { GOLEM_GRAPHQL } from '@eleven-am/golem';
import type { GolemGraphQLArtifacts } from '@eleven-am/golem';

GraphQLModule.forRootAsync<ApolloDriverConfig>({
  driver: ApolloDriver,
  inject: [GOLEM_GRAPHQL],
  useFactory: (golem: GolemGraphQLArtifacts) => ({
    typeDefs: golem.typeDefs,
    transformResolvers: golem.transformResolvers,
    subscriptions: { 'graphql-ws': true },
  }),
})
```

Remove imports and injections of `GOLEM_SCHEMA` and `GraphQLSchema` from the old setup.

## Update custom operations

`@CustomQuery` and `@CustomMutation` methods are now real Nest GraphQL resolver methods. Use standard Nest parameter decorators and attach guards, pipes, interceptors, and filters normally.

```typescript
@UseGuards(SearchArticlesGuard)
@CustomQuery({ type: '[Article!]!', args: { term: 'String!' } })
searchArticles(@Args() args: { term: string }, @Context() context: unknown) {
  return this.prisma.forContext(context).article.findMany({
    where: { title: { contains: args.term } },
  });
}
```

Calls made through `forContext(context)` run Golem authorization and hooks. Plain generated-client calls remain intentional system-level Prisma access.

## Review authorization opt-outs

When an authorization provider is configured, `checkWriteResults` and `checkReadFields` now default to `true`. Applications using the Golem authorization adapter generally need no configuration change.

A deliberately row-policy-only provider can retain the previous behavior explicitly:

```typescript
GolemModule.forRoot({
  // ...
  authorization: RowPolicyProvider,
  defaults: {
    checkWriteResults: false,
    checkReadFields: false,
  },
})
```

Without those opt-outs, a custom authorization provider must implement the field classification and instance-check methods required by the enabled verification paths.

## Regenerate the Golem client

Run the generator after upgrading:

```bash
npx prisma generate
```

The regenerated client makes subscription events transaction-aware. Writes in interactive and batch `$transaction` calls publish only after commit and are discarded on rollback. Previously generated clients do not contain this transaction wrapper.

## Verify the upgrade

Run your build and test suite, paying particular attention to custom-operation guards and any application-owned Prisma transactions.
