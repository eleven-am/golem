# @eleven-am/golem

Prisma/NestJS backend with GraphQL, typed hooks and caller authorization.

```sh
npm install @eleven-am/golem @eleven-am/golem-core
npm install -D @eleven-am/golem-generator
```

Include the required GolemUpsertGuard model and provider migration before startup. Plain generated-client calls are privileged; use forContext(ctx) for caller access.

[Setup and API guide](https://github.com/eleven-am/golem/blob/main/docs/GUIDE.md#typescript-backend) · [Repository](https://github.com/eleven-am/golem)
