# @eleven-am/golem-generator

The Prisma generator for [Golem](https://github.com/eleven-am/golem).

```prisma
generator golem {
  provider = "golem"
  output   = "../src/generated/golem"
}
```

Running `npx prisma generate` emits three artifacts next to your Prisma client: the serialized datamodel and its typed `ComputedField` decorator, the instrumented `GolemPrismaService` (commit-aware event publishing plus the policy-bound `forContext`), and the type map behind `GolemRequest` and `GolemResult`. The generated computed-field decorator rejects unknown models and unknown `requires` fields. Plain delegate calls intentionally act as the system; only `forContext(ctx)` enters Golem's policy and hook pipeline. See the [full guide](https://github.com/eleven-am/golem#readme).
