# Upgrading to Golem 0.5.2

For applications currently on `0.3.x`. Covers `0.4.0`, `0.5.0`, `0.5.1`, and `0.5.2` — plus `@eleven-am/golem-queue@0.3.0`, which is breaking on its own schedule. All are on npm now.

```bash
npm i @eleven-am/golem@^0.5.2 @eleven-am/golem-core@^0.5.2
npm i -D @eleven-am/golem-generator@^0.5.2
npx prisma generate      # required before your code will type-check
```

`@eleven-am/golem-authorizer` moves to `0.5.2` with them.

**If you use `@eleven-am/golem-queue`, read section 6 before upgrading it.** It moves to `0.3.0` and every job handler signature changes.

---

## 1. Computed fields are now Nest resolvers (0.4.0)

**This is the change most likely to break you silently, so do it first.**

Computed fields previously bypassed Nest entirely — Golem held the method and invoked it directly. Param decorators, guards, pipes, filters, and request-scoped providers were all inert, and a thrown `GolemValidationError` never reached its error code.

They are now registered through Nest's `Resolver`/`ResolveField`. **The parent argument must be annotated:**

```diff
-@ComputedField('Article', { type: 'String!', requires: ['url'] })
-domain(article: Pick<Article, 'url'>): string {
-  return new URL(article.url).hostname;
-}
+@ComputedField('Article', { type: 'String!', requires: ['url'] })
+domain(@Parent() article: Pick<Article, 'url'>): string {
+  return new URL(article.url).hostname;
+}
```

`@Parent` comes from `@nestjs/graphql`.

Without `@Parent()`, the method still compiles and still runs — it just receives the wrong argument. **Grep for `@ComputedField` and check every one.**

### Two new rules to be aware of

- **One model per class.** A class carrying computed fields for two models now refuses at boot, naming both. Split it.
- **Inherited computed fields are now discovered.** Previously a `@ComputedField` on a base class was silently ignored. If you have one, it starts working — verify that is what you wanted.

### What you gain

`@Parent()`, `@Context()`, `@Args()`, guards, pipes, interceptors, and the Golem exception filter all work on computed fields now. Computed fields can also declare GraphQL arguments:

```ts
@ComputedField('Article', { type: 'String!', args: { format: 'String' } })
publishedAt(@Parent() article: Pick<Article, 'savedAt'>, @Args('format') format?: string) { … }
```

### N+1 batching

Because these are real resolvers with a working `@Context()`, DataLoader is now the answer for computed fields that run their own query:

```ts
@ComputedField('Artist', { type: '[String!]!' })
genreNames(@Parent() artist: { id: string }, @Context() ctx: GqlContext) {
  return ctx.loaders.genresByArtist.load(artist.id);
}
```

Golem deliberately ships no batching API — a request-scoped DataLoader on the GraphQL context is the standard Nest pattern and now works.

---

## 2. Enable Nest field-resolver enhancers (0.4.0)

For guards, interceptors, and filters to apply to computed fields, the GraphQL module needs the enhancer list Golem provides:

```diff
 GraphQLModule.forRootAsync<ApolloDriverConfig>({
   driver: ApolloDriver,
   inject: [GOLEM_GRAPHQL],
   useFactory: (golem: GolemGraphQLArtifacts) => ({
     typeDefs: golem.typeDefs,
     transformResolvers: golem.transformResolvers,
+    fieldResolverEnhancers: golem.fieldResolverEnhancers,
     subscriptions: { 'graphql-ws': true },
   }),
 }),
```

---

## 3. Imports move to the package (0.5.0)

Golem now registers your schema with itself, so nothing schema-specific needs importing from the generated folder:

```diff
-import { AfterCreate, BeforeCreate, GolemHooks } from '@eleven-am/golem';
-import { GolemRequest, GolemResult } from './generated/golem/types';
+import { AfterCreate, BeforeCreate, GolemHooks, GolemRequest, GolemResult } from '@eleven-am/golem';

-import { ComputedField } from './generated/golem';
+import { ComputedField } from '@eleven-am/golem';
```

The generator emits a `declare global { interface GolemRegister { … } }` block registering your models and Prisma types. You never write it, and you should not edit it. Everything stays fully typed. Being global rather than module-scoped is what lets other Golem packages read the same registration without depending on each other.

You still import the client and `getDatamodel()` from `./generated/golem` when wiring the module — that part is unchanged.

**Regenerate before type-checking.** As of 0.5.1 a missing registration is a compile error naming its own cause, rather than silent type loss:

```
Argument of type '"Article"' is not assignable to parameter of type
'"GOLEM_SCHEMA_NOT_REGISTERED_RUN_PRISMA_GENERATE"'.
```

---

## 4. Model names are checked (0.5.0)

`@GolemHooks`, `@ComputedField`, `GolemRequest`, and `GolemResult` now narrow their model parameter to your real models.

**Check your `@GolemHooks` decorators.** Until now, `@GolemHooks('Ghost')` compiled, booted, and registered hooks that never fired — with no error at any point. If you have had a hook that mysteriously did nothing, this release will tell you why, either as a compile error or a boot failure listing the known models.

Passing a `string` variable where a model literal is expected no longer compiles:

```ts
const model: string = 'User';
@GolemHooks(model)          // no longer allowed
@GolemHooks('User')         // fine
```

---

## 5. New: `@eleven-am/golem-render` (optional)

If you serve a built SPA from your Nest app, this replaces the hand-written static-serving and SPA-fallback middleware:

```bash
npm i @eleven-am/golem-render
```

```ts
GolemRenderModule.forRoot({ client: 'dist-web/client' })
```

That gives content-hashed asset caching, a shell fallback so hard refreshes work, real 404s for missing assets rather than HTML with a 200, and `/api` and `/graphql` reserved.

Add `defaults` for site-wide Open Graph and Twitter tags so shared links stop unfurling as blank cards, and `@RenderRoute` for per-route metadata:

```ts
@Controller()
export class ArticleRender {
  @RenderRoute('/article/:id')
  async article(@Param('id') id: string) {
    const article = await this.prisma.article.findUnique({ where: { id } });
    if (!article) return null;
    return { title: article.title, description: article.excerpt, image: article.image };
  }
}
```

It is a real Nest route, so `@Param`, guards, and pipes all work. Returning `null` or throwing falls back to your defaults; a failed lookup never breaks the page.

**Before adding per-route metadata, understand the exposure:** these resolvers run **unauthenticated**, because link unfurlers carry no cookie. Whatever you return is visible to anyone holding the URL. That is correct for public content and a deliberate disclosure for private content. If your resources are private, ship `defaults` only, or key the resolver off a share token rather than the resource id.

If you mount the frontend only in production, register the module conditionally — it validates at boot and will fail in dev where the build directory does not exist:

```ts
...(process.env.NODE_ENV === 'production'
  ? [GolemRenderModule.forRoot({ client: 'dist-web/client' })]
  : []),
```

---

## 6. Queue handlers take one typed `JobEvent` (`golem-queue@0.3.0`)

**Breaking, and it affects every handler.** Skip this section only if you do not use `@eleven-am/golem-queue`.

```diff
-async handle(payload: Record<string, unknown>, { signal }: JobExecution) {
-  const articleId = payload.articleId as string;
+async handle({ payload, signal }: JobEvent<'article.extract'>) {
+  const articleId = payload.articleId;      // typed, no cast
 }
```

Add the interface so the payload is checked:

```diff
-export class ExtractHandler {
+export class ExtractHandler implements JobWork<'article.extract'> {
```

The decorator is unchanged. As in `@eleven-am/authorizer`, the decorator registers and the interface types — `@QueueHandler` is to `JobWork` what `@Authorizer` is to `WillAuthorize`, annotated parameters included.

### Declare your job types (optional)

```ts
declare global {
  interface GolemRegister {
    jobs: {
      'article.extract': { articleId: string };
      'article.audio': { articleId: string; voiceId: string };
    };
  }
}

export {};
```

With this, a typo'd type in `queue.add` and a payload the handler does not expect are both compile errors. Previously `queue.add('artcile-extract', …)` compiled, enqueued, and the row sat unprocessed forever with no error anywhere.

Unlike model registration this block is hand-written — job types have no source Golem can generate from — and it stays **optional**: omit it and `queue.add` accepts any string and any payload, exactly as before.

### What a `JobEvent` carries

`id`, `type`, `payload`, `attempt`, `maxAttempts`, `scope`, and `signal`.

`attempt` and `maxAttempts` were previously unreachable even though the dispatcher had them, so a handler could not tell whether it was on its last try. It can now:

```ts
if (res.status === 429 && attempt < maxAttempts) throw new RetryableJobError('rate limited', 60_000);
if (res.status === 429) await this.notifyGaveUp(payload.articleId);   // last attempt
```

---

## 7. Computed field return types are checked (0.5.2)

A computed field declaring `type: 'String!'` while returning a number used to compile and fail at query time. It is now a compile error:

```ts
@ComputedField('User', { type: 'String!' })
displayName(): number { return 1; }
//           ^ Type 'number' is not assignable to type 'string'
```

Built-in scalars are checked through their list and nullability modifiers. Object and enum types stay unconstrained on purpose — a field declaring `'[Post!]!'` may legitimately return partial objects, since GraphQL resolves subfields from whatever the resolver returns.

If this surfaces errors, each one is a field that would have failed at runtime.

---

## Upgrade checklist

1. `npm i` the four packages at `^0.5.2`, then `npx prisma generate`.
2. Add `@Parent()` to every computed field. Grep for `@ComputedField`.
3. Add `fieldResolverEnhancers` to the GraphQL module factory.
4. Move `ComputedField`, `GolemRequest`, `GolemResult` imports to `@eleven-am/golem`.
5. Build. Fix any model-name errors the compiler now reports — each one was a silent bug.
6. Boot. Unknown hook models fail loudly with the known models listed.
7. If you use the queue, update every handler to `handle({ payload, signal }: JobEvent<'…'>)`.
8. Run your test suite, including any GraphQL contract tests. The generated SDL should not change except for computed fields that now declare `args`.

## If something looks wrong

- **A computed field returns `undefined`** — missing `@Parent()`.
- **Guards on computed fields do not fire** — missing `fieldResolverEnhancers`.
- **`'"Article"' is not assignable to '"GOLEM_SCHEMA_NOT_REGISTERED_RUN_PRISMA_GENERATE"'`** — you have not run `npx prisma generate`, or the generated module is outside your `tsconfig` include. As of 0.5.1 this is a compile error rather than silent type loss.
- **Boot fails naming two models** — one extension class serves two models; split it.
- **A handler's `payload` is suddenly `undefined`** — the handler still takes `(payload, execution)`; it now receives a single `JobEvent`.
- **`Type 'number' is not assignable to type 'string'` on a computed field** — the declared GraphQL `type` and the method's return type disagree. It would have failed at query time before.

---
