# @eleven-am/golem-generator

The Prisma generator for [Golem](https://github.com/eleven-am/golem).

```prisma
generator golem {
  provider = "golem"
  output   = "../src/generated/golem"
}
```

Running `npx prisma generate` emits three artifacts next to your Prisma client: the serialized datamodel, the instrumented `GolemPrismaService` (automatic event publishing plus the policy-bound `forContext`), and the type map behind `GolemRequest` and `GolemResult`. See the [full guide](https://github.com/eleven-am/golem#readme).
