# @eleven-am/golem-core

The framework-free core of [Golem](https://github.com/eleven-am/golem): datamodel contracts, the operation engine, the policy kernel (row constraints, transactional write verification, field permissions, read masking), the schema builder, and the event machinery.

Install it alongside `@eleven-am/golem` rather than using it directly. See the [full guide](https://github.com/eleven-am/golem#readme).

The public core contracts include:

- `GolemEngine.relationGroupBy()` and `RelationGroupByRequest` for configured, bounded forward-to-one dimensions. Ordinary `groupBy` remains Prisma-shaped; `$scoped()` covers more complex analytics.
- `GolemSubscriptionOptions`/`GolemSubscriptionObserver` for bounded local fan-out (64 queued events per subscriber by default) and queue/evaluation/delivery metrics.
- `GolemBatchEventOptions` for the default 1,000-row/1-MiB top-level batch-event bounds.
- Versioned `encodeGolemEventMessage`/`decodeGolemEventMessage` transport support for BigInt, Decimal, Date, bytes, composite identities, snapshots, and batch envelopes.
- `GolemUpsertGuard` migration artifacts under `prisma/`; authorization-enabled applications must apply the provider migration before startup.

When field read checks are active, generated scalar/enum outputs are nullable so a per-row mask is GraphQL-valid. Composite primary-key models use nested compound selectors and object event identities. See the root migration guide for codegen and migration steps.
