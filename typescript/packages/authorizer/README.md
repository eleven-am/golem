# @eleven-am/golem-authorizer

The authorization adapter connecting [Golem](https://github.com/eleven-am/golem) to [@eleven-am/authorizer](https://www.npmjs.com/package/@eleven-am/authorizer) (CASL).

One rules provider enforces every axis: row constraints compiled into queries, transactional write verification against real rows, field-level write permissions by column diff, and per-row read masking. The default ability (leave `abilityFactory` off your `Authenticator`) is exact for `BigInt` columns at any magnitude. If you override `abilityFactory`, build it with `createGolemAbility` from `@eleven-am/golem-authorizer` — the adapter verifies that its condition matcher agrees with Golem's operator table and refuses to boot otherwise. Rule conditions use Prisma `WhereInput` syntax and are validated before Golem evaluates them in memory or renders them as SQL. See the [full guide](https://github.com/eleven-am/golem#readme).
