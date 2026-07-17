# Security and performance hardening

This report records the behavior decisions and representative measurements for the hardening pass.

## Security behavior

- Nested Prisma envelopes are normalized once by `planNestedWrites`. Preauthorization and transactional result verification consume the same plan.
- Added children are checked as `create` and/or `update`, retained nested updates are checked as `update` with field checks based on the actual scalar diff, and removed children are checked as `update` or `delete` from their pre-write snapshot.
- `connectOrCreate` is conservatively preauthorized for both `create` and `update`; its appeared row is checked for both because the generic Prisma result does not identify which branch ran.
- Relation shorthand reads are expanded to explicit scalar selections only when read-field enforcement is enabled. This lets conditional fields be masked and `never` fields be rejected without changing the default selection shape when enforcement is disabled.
- `maxTake` applies to `Math.abs(take)`, so forward and backward pagination have the same cap.
- Filtered deletion events are suppressed because their filter cannot be evaluated after deletion. Authorized unfiltered deletion events require a pre-delete snapshot and a fresh instance check; missing or insufficient snapshots fail closed. The snapshot is never exposed as GraphQL `entity`.
- Raw subscribed-model upserts probe the unique target immediately before the native Prisma upsert. This preserves Prisma and caller-owned transaction execution while distinguishing the normal create and update branches.
- Authorization constraints and field classifications are memoized by context identity. The authorizer adapter also memoizes the resolved ability in the request transport store. Subscription events use a new fresh-context store for every event.
- Independent policy checks run with a concurrency limit of eight. Results are inspected in input order so the reported error is deterministic within each batch.

## Compatibility implications

- Filtered subscriptions no longer receive `DELETED` events. This is an intentional fail-closed change.
- Authorized deletion events may be suppressed when a raw delete used a narrow selection that omitted fields required by the policy.
- Raw subscribed-model upserts perform one additional unique-target lookup before the native upsert.
- `connectOrCreate` requires both model-level capabilities before execution and can be stricter than a provider that grants only one branch.
- Relation shorthand may be rewritten to an equivalent explicit scalar `select` when read-field enforcement is enabled.

## Representative benchmark

Run `npm run benchmark:hardening` to reproduce the synthetic benchmark. The sample below was recorded on 2026-07-12. Counts are deterministic; elapsed time varies by machine.

| Path | Mode | Queries | Authorization work | Elapsed |
| --- | --- | ---: | ---: | ---: |
| 100 repeated reads in one context | uncached baseline | 100 | 100 constraints + 100 classifications | 227.64 ms |
| 100 repeated reads in one context | hardened | 100 | 1 constraint + 1 classification | 4.14 ms |
| verified write policy phase | sequential baseline | 3 | 64 checks | 75.23 ms |
| verified write policy phase | bounded concurrency | 3 | 64 checks | 9.49 ms |
| `updateMany` policy phase | sequential baseline | 3 | 128 checks | 150.42 ms |
| `updateMany` policy phase | bounded concurrency | 3 | 128 checks | 19.34 ms |
| 100 subscription events | before/after | 100 | 100 fresh checks | no query/check-count change |

The benchmark also asserts that three authorizer operations in one request resolve one ability, while a fresh subscription context resolves a second ability.

## Remaining limitations

- Subscription fan-out still performs per-subscriber database work for non-deletion events.
- Events emitted inside user-owned Prisma transactions are not held until those transactions commit.
- A concurrent writer can race the raw-upsert branch probe; strict cross-process branch classification requires database-specific instrumentation that is outside the generic query-extension contract.
- Narrow verification hydrates relation-scoped policy conditions (`is`, `isNot`, `some`, `every`, `none`, and directly nested relation filters, recursively) so relation-dependent rules verify against the real related rows instead of an under-selected row. Condition shapes the derivation cannot resolve to concrete scalar or relation selections still fail closed rather than under-select.
- The read pipeline hydrates relation-scoped read conditions symmetrically with write-side verification, sharing the same recursive derivation. When a field classification's requirement names a relation, the returned row is hydrated with that relation before per-row masking runs. For every named relation the injected shape is the union of the related model's full scalar columns with any deeper nested selections the caller's read constraint carries. Unioning the full scalar set (rather than trusting the constraint-derived shape alone) is deliberate: a field classification surfaces only the relation name, never the specific scalar its rule condition reads, so a row constraint that references the relation with a shape omitting that scalar would otherwise leave a field-level `cannot` evaluating against `undefined` and failing open. Injected relations and columns are stripped from the response exactly like injected scalar policy dependencies, including when the caller also selected the relation under a narrower shape.
- Read-side hydration reaches one relation level deep — the level a one-level field condition needs. A condition nested more than one relation deep is not enforced in-memory in either direction: a positive re-grant fails closed to a masked `null`, but a negative (`cannot`) condition deeper than the hydrated level is not enforced and fails open. This is a pre-existing limitation of the field classifier, which reports relation names rather than condition paths; closing it requires condition-depth introspection through the authorization provider and is not attempted here.
- Compound-unique (`@@unique`) selectors are unwrapped into scalar equalities wherever the engine probes or re-targets a row through `findFirst` (the verified-upsert probe, constrained-update target resolution, and the verified-update pre-image), alongside the compound `@@id` selector already handled. A selector key matching nothing is passed through unchanged so Prisma rejects genuinely invalid shapes rather than the engine swallowing them.
- Composite (`@@id`) primary keys are supported for verified writes through `forContext`: row identity uses the compound unique key and every primary-key column is selected across the read/write/re-read cycle, including for composite-key rows that appear as nested children of another model's write, which are diffed and value-verified by their full key like single-column children. Composite-key models are not exposed on the generated GraphQL surface and cannot enable subscriptions — both are rejected at schema build. A nested child whose model declares no primary key at all fails closed rather than skipping verification.
- Bounded concurrency reduces policy latency but does not reduce the number of instance or field checks.
