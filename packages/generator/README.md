# @eleven-am/golem-generator

The Prisma generator for [Golem](https://github.com/eleven-am/golem).

```prisma
generator golem {
  provider = "golem"
}
```

`npx prisma generate` emits three artifacts next to your Prisma client: the serialized datamodel, the instrumented `GolemPrismaService` (event publishing + `forContext`), and the type map (`GolemRequest`, `GolemResult`, `GolemTypes`). See the [root README](https://github.com/eleven-am/golem#readme).
