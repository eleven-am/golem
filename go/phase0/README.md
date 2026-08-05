# Golem Go — Phase 0 semantic design

> **Historical oracle only — do not use as the production API.** This directory
> records the Phase 0 spike and its source decisions. The controlling merged
> architecture is [`docs/golem-go/BIBLE.md`](../../docs/golem-go/BIBLE.md), and
> current policy status is
> [`docs/golem-go/p2/STATUS.md`](../../docs/golem-go/p2/STATUS.md). Phase 0's string
> identities, `any` records, and in-memory evaluator are retained only as
> independent oracle/traceability evidence. Production packages must not import
> or copy them.

Phase 0 defines the Go product boundary before production implementation begins.
Its executable spike answers the first high-risk question: can Go express
Golem's ordered row and field authorization semantics in a typed,
provider-neutral form before a database or transport exists?

The following is a historical spike example, not current application syntax. It
proved the model-specific policy-object direction before the generated P2 ABI
existed:

```go
type PostPolicy struct{}

func (PostPolicy) Define(r *phase0.Rules[Post], actor Actor) {
    owned := PostAuthorID.Eq(actor.ID)

    r.CanRead(PostPublished.Eq(true).Or(owned))
    r.CanCreate(owned)
    r.CanDelete(owned)
    r.CanUpdateFields(owned, PostTitle, PostBody, PostPublished)
}
```

Generated-style fields create a typed expression tree; they do not inspect a
loaded model. This is essential because the same tree must eventually compile
to authorization-aware SQL and evaluate against a persisted result.

The executable fixtures cover one `Post` read policy:

```text
published = true OR authorId = actor.id
```

Run the spike from `go/`:

```sh
go test ./...
```

Read [CONTRACT.md](./CONTRACT.md) for the exact semantics,
[DESIGN.md](./DESIGN.md) for the choices being tested, and
[TRACEABILITY.md](./TRACEABILITY.md) for the TypeScript evidence behind each
claim. [TS_SURFACE_CLASSIFICATION.md](./TS_SURFACE_CLASSIFICATION.md) inventories
the full current TypeScript product surface, separates semantic parity from
framework-specific replacement work, and records what Phase 0 does not
implement. [P0_DECISIONS.md](./P0_DECISIONS.md) freezes the Go model, runtime,
GraphQL, mutation, hook, event, aggregation, and error boundaries.
[PHASE_MAP.md](./PHASE_MAP.md) assigns every capability area to a bounded build
phase. [STATUS.md](./STATUS.md) records the Phase 0 exit criteria, verification,
non-claims, and the exact first P1 slice.
[SOCIAL_NETWORK_EXAMPLE.md](./SOCIAL_NETWORK_EXAMPLE.md) shows the intended
application-author experience end to end.

This directory is disposable design code. Passing Phase 0 does not turn these
package names or types into the production Go API.
