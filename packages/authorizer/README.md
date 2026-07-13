# @eleven-am/golem-authorizer

Authorization adapter connecting [Golem](https://github.com/eleven-am/golem) to [@eleven-am/authorizer](https://www.npmjs.com/package/@eleven-am/authorizer) (CASL).

One rules provider enforces every axis: row constraints compiled into queries, transactional write verification against real rows, field-level write permissions, and per-row read masking. Abilities must be built with `createPrismaAbility` and conditions written in Prisma `WhereInput` syntax. See the [root README](https://github.com/eleven-am/golem#readme).
