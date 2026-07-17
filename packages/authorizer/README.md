# @eleven-am/golem-authorizer

The authorization adapter connecting [Golem](https://github.com/eleven-am/golem) to [@eleven-am/authorizer](https://www.npmjs.com/package/@eleven-am/authorizer) (CASL).

One rules provider enforces every axis: row constraints compiled into queries, transactional write verification against real rows, field-level write permissions by column diff, and per-row read masking. Abilities must be built with `createBigIntSafePrismaAbility` from `@eleven-am/authorizer` (a drop-in for `createPrismaAbility` that makes in-memory checks exact for `BigInt` columns at any magnitude) and rule conditions written in Prisma `WhereInput` syntax, because constraints are copied into queries verbatim. See the [full guide](https://github.com/eleven-am/golem#readme).
