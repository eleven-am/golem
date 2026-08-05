# Phase 0 status

Status: **complete as a semantic design phase** on 2026-08-05.

This does not mean the Go backend is implemented. It means P1 can begin without
using “implement Golem” as an undefined task or allowing framework/library
choices to invent product semantics.

## Exit criteria

| Criterion | Status | Evidence |
|---|---|---|
| Current TypeScript product surface is classified | complete | `TS_SURFACE_CLASSIFICATION.md` |
| Framework mechanisms are separated from product parity | complete | package boundary in `TS_SURFACE_CLASSIFICATION.md` |
| Historical limitation drift is reconciled | complete | classification section 10 |
| Go authorization authoring shape is executable | complete | typed fields/relations, `Rules`, evaluator, social fixtures |
| Ordered row and field rules match TypeScript/CASL | complete | four named fixtures plus 259 row and 259 model/field generated chains; reproducible TypeScript digests |
| SQLite and PostgreSQL are fixed equal targets | complete as a decision | `CONTRACT.md`, `P0_DECISIONS.md`; live proof intentionally belongs to P2 |
| Model declaration and generation boundary is fixed | complete | `P0_DECISIONS.md` sections 2–3 |
| Programmatic/system client boundary is fixed | complete | `P0_DECISIONS.md` section 5 |
| GraphQL surface and authorization/nullability boundary is fixed | complete | `P0_DECISIONS.md` section 6 |
| Mutation, nested authorization, hook, transaction, and upsert outcomes are fixed | complete | `P0_DECISIONS.md` sections 7–8 |
| Event/outbox/subscription/CDC boundary is fixed | complete | `P0_DECISIONS.md` section 9 |
| Aggregation/scoped read and error boundaries are fixed | complete | `P0_DECISIONS.md` sections 10–11 |
| Every classified capability group has a phase owner | complete | `PHASE_MAP.md` ownership ledger |
| Phase 0 implementation claims are explicit and narrow | complete | `CONTRACT.md` implementation non-goals and this status |

## Verification at closure

The closure pass completed with:

- `go test -race ./...`
- `go test -shuffle=on -count=20 ./phase0`
- `go vet ./...`
- `go test -cover ./...` (`phase0`: 83.7% statement coverage)
- reproducible TypeScript rule oracle: 259 row chains and 259 model/field
  chains, with both SHA-256 digests matching the Go test constants
- 57 focused TypeScript core schema/composite/nullability/batch-event/relation-
  aggregation tests
- 61 focused TypeScript authorizer rule-chain/classification tests
- whitespace/error check across all Phase 0 files

## What is proven

- Model-specific Go predicates prevent accidental direct cross-model composition.
- Generated-style scalar handles constrain operand types.
- Generated-style relation handles are the explicit model boundary crossing.
- The exercised predicate subset has an in-memory interpreter.
- Request filtering cannot widen policy in the in-memory `findMany` fixture and
  authorization precedes pagination.
- System execution is explicit and actor policies are built independently.
- The Go rule builder matches TypeScript/CASL priority outcomes for the recorded
  depth-three oracle sweep.
- Every remaining product area has a written contract and implementation owner.

## What is not implemented

- Model parsing, IR generation, generated production APIs, or migrations.
- SQL compilation or a live SQLite/PostgreSQL database test.
- Production reads, mutations, nested writes, hooks, or transactions.
- GraphQL generation or execution.
- Aggregation or scoped reads.
- Events, outbox, subscriptions, or CDC.
- Production package names or compatibility promises for the spike types.

## Next phase

P1 begins with one bounded vertical slice:

1. parse two social-network models from Go structs/tags into the versioned IR;
2. validate scalar fields, a compound key/index, and a to-one/to-many relation;
3. generate typed descriptors equivalent to the handwritten Phase 0 handles;
4. generate deterministic SQLite and PostgreSQL initial migration artifacts; and
5. apply each artifact to an empty database and verify the resulting schema
   against the IR fingerprint.

P1 must not add CRUD, authorization SQL, or GraphQL to that first slice.
