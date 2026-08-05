# Phase 0 design

## The problem being tested

Golem must authorize a query before pagination and without loading unrestricted
rows. Therefore a policy such as this cannot be the core abstraction:

```go
func (Post) CanRead(actor Actor) bool
```

An arbitrary Go boolean function cannot reliably become SQL. Filtering its
result after a query would make pagination incorrect and expose unrestricted
rows to application memory.

The spike instead tests policy objects that construct typed expressions:

```go
func (PostPolicy) Define(r *phase0.Rules[Post], actor Actor) {
    owned := PostAuthorID.Eq(actor.ID)
    r.CanRead(PostPublished.Eq(true).Or(owned))
}
```

## Expression tree

`Predicate[M]` owns a provider-neutral tree. Its model type prevents unrelated
model predicates from being combined accidentally. Generated-style fields add
scalar type checking, and generated-style relations perform the intentional
root-model conversion.

The tree, rather than source-code inspection or reflection over a closure, is
the semantic boundary. It can be normalized, explained, cached, evaluated, and
later compiled.

Phase 0 implements one interpreter: `Evaluate`. A future SQL interpreter is not
allowed to redefine the operators. Live SQLite and PostgreSQL agreement tests
will be the acceptance gate for that interpreter.

## Ordered rules, deterministic derived predicates

CASL rule priority is part of the TypeScript behavior and is preserved. Rules
are declared oldest-to-newest and evaluated newest-to-oldest. Conditional
denials constrain older grants, while a newer matching grant can override an
older denial. The first unconditional rule ends the reachable chain.

The Go builder does not depend on CASL, but its derived predicate is checked
against named TypeScript/CASL oracle cases. Model-wide and field-scoped rules
participate in the same ordered chain for a field.

The resulting predicate's canonical representation is intentionally
deterministic. It is suitable
for equality tests and, later, request-local memoization or plan identity. It is
not a stable public serialization format in Phase 0.

## Actor lifecycle

`Define` is called for a resolved actor. Tests build two policies independently
and assert that actor-specific values do not leak between them. Phase 0 does not
choose the authentication transport or caching mechanism.

## Fields

Model scope and field scope are distinct outputs of the same rule chain. A model
may be visible while a higher-priority field denial makes one field conditional
or inaccessible. `Classify` returns `always`, `conditional`, or `never`, matching
the distinction the TypeScript engine needs for nullable masked output fields.

The spike proves per-row evaluation for a self-only email field and an absolute
denial for an immutable identity field. GraphQL nullability and filter-field
execution are outside the executable spike; their contract and phase ownership
are recorded in `P0_DECISIONS.md` and `PHASE_MAP.md`.

## Relations

To-one and to-many handles are separate types. That prevents calling `Some` on
a to-one relation or `Is` on a to-many relation. A nested friendship fixture
proves that a `User` predicate becomes a `Post` predicate only through the
declared `Post.author` and `User.friends` relations.

The record evaluator uses conventional quantifier behavior, including vacuous
truth for `every` over an empty relation. Exact null and missing-relation SQL
semantics must be reconfirmed during the dual-provider SQL phase.

## Database boundary

The production database layer is already decided: `sqlx`, with SQLite and
PostgreSQL both required. Phase 0 imports neither `sqlx` nor a database driver
because it performs no database work. Adding a dependency merely to signal a
future decision would create a false implementation claim.

When SQL work begins, the intended boundary is:

```text
typed predicate -> normalized AST -> provider SQL + bound arguments -> sqlx
```

Placeholder rebinding alone will not be treated as a dialect implementation.
Identifier quoting, null behavior, relation correlation, booleans, JSON,
conflict handling, and returning behavior require live provider conformance.

## Exit decision

Phase 0 succeeds when the TypeScript surface is classified, the authoring core is
type-safe and matches the ordered TypeScript oracle, every product boundary is
written, and every capability is assigned to a bounded implementation phase.
Success authorizes P1 implementation; it is not an implementation claim for the
remaining backend.
