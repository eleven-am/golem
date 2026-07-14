# @eleven-am/golem

The NestJS module for [Golem](https://github.com/eleven-am/golem). Feed it a Prisma schema and get a complete GraphQL API: queries, mutations with nested writes, live subscriptions, typed hooks, custom operations, and a CASL authorization kernel enforcing row, field, and relation policy on GraphQL and `forContext(ctx)` calls. Plain generated-client calls are explicit system-level Prisma access and do not run caller policy or Golem hooks.

```bash
npm i @eleven-am/golem @eleven-am/golem-core
npm i -D @eleven-am/golem-generator
```

See the [full guide](https://github.com/eleven-am/golem#readme) for the quickstart, configuration reference, and authorization model.
