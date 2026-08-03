# @eleven-am/golem

The NestJS module for [Golem](https://github.com/eleven-am/golem). Feed it a Prisma schema and get a complete GraphQL API: queries, mutations with nested writes, live subscriptions, typed hooks, custom operations, and a CASL authorization kernel enforcing row, field, and relation policy on GraphQL and `forContext(ctx)` calls. Plain generated-client calls are explicit system-level Prisma access and do not run caller policy or Golem hooks.

```bash
npm i @eleven-am/golem @eleven-am/golem-core
npm i -D @eleven-am/golem-generator
```

See the [full guide](https://github.com/eleven-am/golem#readme) for the quickstart, configuration reference, and authorization model.

If you set `GraphQLModule`'s `context`, it must be a function — `context: ({ req }) => ({ req })` — never a static object. `@nestjs/apollo` reuses a static object across requests with the first caller's `req` still attached, so every later caller is served as the first one. Golem stamps each request through a middleware it registers itself and fails any operation whose context belongs to another request; a context deliberately shared across requests must carry `[golemSharedContext]: true` (the symbol is exported from `@eleven-am/golem`).

Since 0.4, computed fields are real Nest field resolvers. Import the typed `ComputedField` helper from the generated Golem module, use `@Parent()`/`@Context()`/`@Args()`, and pass `golem.fieldResolverEnhancers` to `GraphQLModule`. This enables ordinary Nest pipes, guards, interceptors, filters, and request-scoped providers. A computed field that queries for other rows can be declared with `BatchedComputedField` instead, which loads every parent key resolved in the same tick in one query, per request and through the caller's context. See the root migration guide for the positional-parent breaking change.
