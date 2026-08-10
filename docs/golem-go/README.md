# Golem for Go specifications

For application authors, begin with the executable
[`QUICKSTART.md`](./QUICKSTART.md), then use
[`PRODUCTION.md`](./PRODUCTION.md) for authorization, hooks, custom operations,
analytics, migrations, events, deployment, security, recovery, and upgrades.
Both are honestly marked as unreleased while P8 evidence remains pending.

Start with [`BIBLE.md`](./BIBLE.md). It is the controlling merged product,
security, provider, authoring, and runtime specification.

For active Go implementation, [`p1/P1-CONTRACT.md`](./p1/P1-CONTRACT.md) is the
accepted P1 integration contract. Its three supporting Wave 0 documents preserve
the detailed schema/IR, provider/migration, and compiler/codegen specifications.

P2 work is governed by [`p2/P2-PLAN.md`](./p2/P2-PLAN.md). The accompanying
[`p2/OPERATOR-ABI.md`](./p2/OPERATOR-ABI.md) records the completed P2-A typed
baseline and must not be read as a completed policy-kernel claim. The P2-B public
ABI, internal representation, and provider proof contracts are
[`p2/PUBLIC-ABI.md`](./p2/PUBLIC-ABI.md),
[`p2/INTERNAL-IR.md`](./p2/INTERNAL-IR.md), and
[`p2/PROVIDER-AGREEMENT.md`](./p2/PROVIDER-AGREEMENT.md). Current implementation
evidence, completion status, provider limits, and P3+ exclusions are tracked in
[`p2/STATUS.md`](./p2/STATUS.md).

P3 authorized reads are complete. Its controlling plan and public surface are
[`p3/P3-PLAN.md`](./p3/P3-PLAN.md) and
[`p3/PUBLIC-READ-ABI.md`](./p3/PUBLIC-READ-ABI.md); the exact completed test and
provider ledger is [`p3/P3-EVIDENCE.md`](./p3/P3-EVIDENCE.md).

P4 authorized mutations and closure transactions are complete. They are
governed by [`p4/P4-PLAN.md`](./p4/P4-PLAN.md), with the frozen
application surface in
[`p4/PUBLIC-MUTATION-ABI.md`](./p4/PUBLIC-MUTATION-ABI.md). The completed local,
provider, concurrency, race, repeat, and deterministic evidence ledger is
[`p4/P4-EVIDENCE.md`](./p4/P4-EVIDENCE.md).

P5 generated GraphQL is complete. Its controlling architecture and work waves are
[`p5/P5-PLAN.md`](./p5/P5-PLAN.md), the frozen schema and Go integration are
[`p5/PUBLIC-GRAPHQL-ABI.md`](./p5/PUBLIC-GRAPHQL-ABI.md), and the mandatory
completed local evidence gates are recorded in
[`p5/P5-EVIDENCE.md`](./p5/P5-EVIDENCE.md).

P6 analytics and scoped reads are complete. The controlling architecture,
exact generated Go/GraphQL surface, and completed evidence are
[`p6/P6-PLAN.md`](./p6/P6-PLAN.md),
[`p6/PUBLIC-ANALYTICS-ABI.md`](./p6/PUBLIC-ANALYTICS-ABI.md), and
[`p6/P6-EVIDENCE.md`](./p6/P6-EVIDENCE.md).

P7 events, durable outbox publication, generated caller/GraphQL subscriptions,
fresh per-event authorization, bounded fan-out, and the optional CDC boundary
are complete. The controlling architecture is
[`p7/P7-PLAN.md`](./p7/P7-PLAN.md), the frozen source/transport/GraphQL contract
is [`p7/PUBLIC-EVENT-ABI.md`](./p7/PUBLIC-EVENT-ABI.md), and the completed
provider, crash, concurrency, race, fuzz, and mutation ledger is
[`p7/P7-EVIDENCE.md`](./p7/P7-EVIDENCE.md).

P8 is the final accepted roadmap phase. It hardens and releases the existing
P1–P7 engine; it does not add a second application runtime or make federation a
core requirement. Its controlling plan is
[`p8/P8-PLAN.md`](./p8/P8-PLAN.md), the public provider, observability,
diagnostic, compatibility, and release surface is
[`p8/PUBLIC-PRODUCTION-ABI.md`](./p8/PUBLIC-PRODUCTION-ABI.md), and all mandatory
implementation and hosted-release gates are currently `PENDING` in
[`p8/P8-EVIDENCE.md`](./p8/P8-EVIDENCE.md).

The numbered documents are detailed supporting specifications and research:

1. [`01-operators.md`](./01-operators.md) — operator semantics and agreement;
2. [`02-policy-resolution.md`](./02-policy-resolution.md) — row and field rules;
3. [`03-classification.md`](./03-classification.md) — field-reference security;
4. [`04-statement-shape.md`](./04-statement-shape.md) — SQL planning; and
5. [`05-surface-and-runtime.md`](./05-surface-and-runtime.md) — operations and
   execution.

Those chapters include measured TypeScript behavior, deliberate Go improvements,
and unresolved findings from their original research pass. When one conflicts
with the Bible, the resolution table in Bible section 23 is authoritative.

The earlier `go/phase0` documents remain source history and executable design
evidence. They are no longer the controlling architecture on their own.
