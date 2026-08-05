# Golem for Go specifications

Start with [`BIBLE.md`](./BIBLE.md). It is the controlling merged product,
security, provider, authoring, and runtime specification.

For active Go implementation, [`p1/P1-CONTRACT.md`](./p1/P1-CONTRACT.md) is the
accepted P1 integration contract. Its three supporting Wave 0 documents preserve
the detailed schema/IR, provider/migration, and compiler/codegen specifications.

P2 work is governed by [`p2/P2-PLAN.md`](./p2/P2-PLAN.md). The accompanying
[`p2/OPERATOR-ABI.md`](./p2/OPERATOR-ABI.md) records the completed P2-A typed
baseline and must not be read as a completed policy-kernel claim.

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
