# Migrating to 0.6.0

Most of this release fails loudly at startup, which is the easy kind. Four
things do not: they fail on traffic that works today. Check those first.

`@eleven-am/golem-queue` and `@eleven-am/golem-render` carry no golem
dependency and did not change. Leave them where they are.

## Hardening upgrade: required schema and client changes

This upgrade closes the context-aware upsert race, bounds subscription and batch-event memory, exposes compound identities through GraphQL, makes masked output nullability truthful, and adds configured relation dimensions. Apply these steps before deploying the regenerated client.

### 1. Add the internal upsert guard migration

Every database used by an authorization-enabled Golem application must contain this reserved model:

```prisma
model GolemUpsertGuard {
  stripe Int    @id
  seq    BigInt @default(0)

  @@map("_golem_upsert_guard")
}
```

Copy the model from `typescript/packages/core/prisma/golem-core.prisma` or apply the provider SQL in `typescript/packages/core/prisma/migrations/sqlite/001_golem_upsert_guard.sql` or `typescript/packages/core/prisma/migrations/postgresql/001_golem_upsert_guard.sql`, then run your normal Prisma migration and generation workflow. Golem reserves `GolemUpsertGuard`, removes it from generated GraphQL and `forContext` model surfaces, and validates the delegate/table during Nest startup. Missing infrastructure now fails deployment rather than waiting for the first upsert.

The table stays bounded by `defaults.upsertGuardStripes` (4,096 by default). It stores only a stripe and monotonic sequence; model names and unique selector values are hashed and never persisted. All participating context-aware upserts for the same canonical model/selector serialize before the policy branch probe. Plain Prisma/external writers and differently addressed selectors do not participate. A caller-owned SQLite transaction that established an incompatible snapshot before the guard write now returns stable `CONFLICT`; retry only by repeating the complete transaction when that is safe.

### 2. Regenerate GraphQL and TypeScript clients

With authorization and `checkReadFields` enabled, every visible scalar/enum model output is now nullable, including required database columns. Inputs retain their existing Prisma requiredness, lists keep their list/item structure, and event identities stay non-null. Re-run GraphQL codegen and fix consumers that assumed a selected scalar could never be `null`.

Compound `@@id` and `@@unique` selectors now appear as Prisma-shaped nested input objects across find-one, update, delete, upsert, connect, connect-or-create, and nested update/delete. A composite subscription event changes `id` from an unsupported scalar to a model-specific object in declared key order:

```graphql
subscription {
  postTagEvents {
    type
    id { postId tagId }
  }
}
```

Single-column event schemas are unchanged. Regenerate typed operations for any newly exposed composite model or subscription.

### 3. Set operational event bounds deliberately

One reference-counted iterator now serves each model/schema's local subscribers. Every subscriber has a bounded queue (`subscription.queueCapacity`, default 64). A full queue disconnects that subscriber with `GOLEM_SUBSCRIPTION_OVERFLOW`; events are not dropped. Wire `subscription.observer` if queue depth, suppressions, evaluation latency, delivery counts, or overflow must feed your metrics system.

Top-level `updateMany` and `deleteMany` now publish deterministic per-row events for subscribable models. The generated client performs the auxiliary work on the active transaction, buffers until commit, and discards on rollback. Defaults are 1,000 rows and 1 MiB encoded payload:

```ts
GolemModule.forRoot({
  // ...
  batchEvents: {
    maxRows: 1_000,
    maxPayloadBytes: 1_048_576,
  },
  subscription: {
    queueCapacity: 64,
    observer,
  },
});
```

Over-limit batches reject before mutation and never truncate. Eventful `updateMany` rejects writes to primary-key components. `deleteMany` snapshots and deletes the exact selected identities and rolls back on a concurrency mismatch. Nested batches and out-of-process writes are still not captured. Publication is commit-aware but neither durable nor exactly-once: a process crash after commit and before publication can lose events.

### 4. Configure relation dimensions explicitly

Prisma-shaped `groupBy` is unchanged. A model with `relationDimensions` gains a separate `<models>RelationGrouped` GraphQL field and the separately typed `forContext(ctx).model.relationGroupBy()` method (including its transaction view and the engine):

```ts
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
}
```

Only one explicit forward to-one path is accepted. Reverse-only, to-many/many-to-many, multiple paths, related-model measures, hidden/write-only keys, and unreadable terminal fields fail closed. Root facts whose terminal target is absent or policy-invisible use inner-join semantics and do not contribute. Final `having`, ordering, `skip`, and `take` run after merging; averages are reconstructed from sum/non-null count; the complete first-phase set is capped before pagination. Keep `$scoped()` for shapes outside these constraints.

## Check these four before you deploy

**1. Your GraphQL `context` must be a function.**

```diff
  GraphQLModule.forRoot<ApolloDriverConfig>({
-   context: { pubSub },
+   context: ({ req }) => ({ req, pubSub }),
  })
```

Given an object, `@nestjs/apollo` attaches the first request's `req` to it and
hands the same object to every request afterwards. Every later caller then
executes with the first caller's identity. Golem now refuses this rather than
letting it run, so the failure is visible instead of silent — but it arrives on
request #2, so a single smoke test passes and the deploy does not.

If you share a context deliberately, opt out with the exported symbol:

```ts
import { golemSharedContext } from '@eleven-am/golem';
const context = { [golemSharedContext]: true, pubSub };
```

**2. Do you write field-scoped CASL rules?**

Search your ability for a rule carrying a field list:

```ts
can('read', 'User', ['phone'], { id: me });
cannot('read', 'User', ['phone']);
```

If you have none, skip to step 3 — nothing here reaches you. If you do, queries
that **filter, order, paginate or deduplicate** by one of those fields are now
refused where they previously returned rows:

```
Cannot filter or order by field "phone" on User: readability depends on id,
which the query constraint does not discharge
```

This is deliberate. Masking the projection was never enough: a caller who saw
`phone: null` could still write `users(where: { phone: { startsWith: "+44" } })`
and read the value out of which rows came back, one character at a time. The
refusal now covers `where`, `orderBy`, `cursor` and `distinct`, at the root of a
read, inside a relation entry in `select`/`include`, and in the filter a batch
or nested write selects rows with.

Grep your callers for those field names in a filter position. A field the caller
may **always** read is unaffected.

**3. Does any ability grant `update` or `delete` without `read`?**

```ts
can('update', 'Post');   // and no can('read', 'Post')
```

Such a caller can no longer filter `updateMany` or `deleteMany` at all — every
field, including `id`, is refused. A `where` interrogates the database and its
count discloses which rows matched, so it is a read, and a caller who may not
read the model is told nothing by one. Grant `read` alongside the write if the
caller legitimately needs to select rows.

**4. Re-run GraphQL codegen.**

`min` and `max` now accept `DateTime` and `String`, so on models whose
aggregatable columns are not all numeric they reference a new
`<Model>OrderableField` enum. Literals keep working; **typed variables fail
validation**, because GraphQL has no enum subtyping:

```graphql
# no longer valid where the model gained orderable measures
query ($f: [PostMeasureField!]) { postsAggregate(measures: { min: $f }) { … } }
```

## What fails at startup

**Abilities built with `createAbility`.** It is backed by `@casl/prisma`, whose
conditions matcher is exactly what golem now replaces.

```diff
- import { createAbility } from '@eleven-am/authorizer/prisma';
+ import { createGolemAbility } from '@eleven-am/golem-authorizer';

  abilityFactory() {
-   return new AbilityBuilder(createAbility);
+   return new AbilityBuilder(createGolemAbility);
  }
```

If you never set `abilityFactory`, golem installs its own and nothing changes.

**Policy conditions golem cannot render exactly.** Conditions are Prisma's
`WhereInput`. Anything outside it is refused when the ability is built, naming
the rule and the operator, rather than approximated.

**Node below 20.19**, and **any MySQL dialect**. Kysely is ESM-only, and MySQL
is removed rather than left as a claim that was never executed against a server.
SQLite and Postgres are both tested against live ones.

## Behaviour that changed without failing

**`upsert` picks its branch from rows you may update.** A row that exists beyond
your reach used to count as existing and route you to the update branch, which
answered differently from a missing row — enough to enumerate unique keys. Both
now answer alike. A caller who may create still learns a key is taken from the
unique violation; that is the database's disclosure, not golem's to hide.

**Subscriptions end when the subscriber leaves.** One parked at an `await` could
not honour `return`, so a subscription that filtered its events, or whose events
an ability silenced, never unwound — it kept querying for a departed client and
held its context alive. No action needed; your event bus needs nothing new.

## New, and opt-in

**`@BatchedComputedField`** answers a computed field for a whole page in one
query instead of one per row. Scoped per request and per subscription event, so
a cache never crosses a caller.

**`ctx.$scoped(model)`** gives you a Kysely builder rooted at a model with the
policy predicate and field policy already applied, for analytical reads the
generated surface cannot express.

Full detail, including where these guarantees stop, is in `RELEASE_NOTES.md`.

---

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

For relation-backed computed fields, declare `@BatchedComputedField` instead of `@ComputedField`: the method receives every parent key resolved in the same tick and returns a map from key to value, loaded once per request through the caller's own context. Existing `@ComputedField` declarations are unaffected.

## Optional SPA rendering package

`@eleven-am/golem-render` is a new independent package for hosting a compiled SPA and injecting route-specific Open Graph/Twitter metadata. It has no dependency on Golem Core and adds no authorization behavior. See `typescript/packages/render/README.md` before exposing unscoped metadata.

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

## Review context-aware upsert participation

`forContext().model.upsert()` now acquires the bounded internal guard before its policy-scoped branch probe, then runs exactly one create or update pipeline so that branch's hooks and verification execute once. Apply the required guard migration described above. Retry a stable SQLite ambient-snapshot `CONFLICT` only when repeating the complete transaction is safe. Plain Prisma/external writers and differently addressed unique selectors do not participate in this serialization guarantee.

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
