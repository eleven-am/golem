# Phase 0 contract

## Purpose

Phase 0 validates the smallest authorization slice needed before designing the
Go runtime. It covers one model, one actor, one operation result, and the
authoring vocabulary needed to state the four Golem actions.

It does not implement a backend.

## Fixed project constraints

- Golem's database integration will use `sqlx`.
- SQLite and PostgreSQL are equal, required providers.
- The policy representation is provider-neutral.
- A later SQL interpreter and the in-memory interpreter must agree.
- Unsupported provider semantics must be rejected before execution; Golem may
  not silently approximate them.

Phase 0 has no database dependency, so it records and verifies these constraints
without pretending to prove SQL that has not been written.

## Model and actor

The executable fixture has this logical shape:

```text
Post(id, authorId, title, published)
Actor(id, admin)
```

The ordinary caller's read rule is:

```text
Post.published = true OR Post.authorId = Actor.id
```

An administrator receives an unconditional rule.

## Authoring contract

Policies are separate objects with a `Define` method. A policy builds rules for
one actor. It never receives an unrestricted persisted model.

The builder supports:

- `CanRead`, `CanCreate`, `CanUpdate`, and `CanDelete`;
- matching `Cannot…` methods;
- conditional read/create/update field rules;
- generated typed scalar fields;
- typed to-one and to-many relation traversal;
- `All` and `None` constants;
- equality, inequality, membership, `AND`, `OR`, and `NOT`;
- `is`, `isNot`, `some`, `every`, and `none` relation predicates.

A `Predicate[Post]` cannot be combined with a `Predicate[User]` directly. A
generated `Relation[Post, User]` is required to cross that boundary. An operand
passed to `Field[Post, bool].Eq` must be a Go `bool`; passing a string is a
compile-time error.

The generated field and relation declarations in `fixtures/` are handwritten
stand-ins. Phase 0 does not implement the model parser or generator;
`P0_DECISIONS.md` fixes Go structs/tags -> model IR -> generated descriptors as
the P1 boundary.

## Rule algebra

Rules are deny-by-default and priority ordered, matching the TypeScript CASL
oracle. The last applicable declaration has the highest priority. Evaluation
walks the applicable chain from newest to oldest:

1. A conditional denial excludes its matching rows from every older grant.
2. A conditional grant contributes a result branch after all newer denials have
   been excluded.
3. The first unconditional grant or denial ends the reachable chain.
4. No reachable grant produces `None`.

This makes declaration order semantic. A conditional grant declared after a
conditional denial can restore access for its matching rows; reversing those
declarations lets the denial win.

Model-wide and field-scoped rules form the same ordered chain for a named field.
A model-wide grant grants every field unless a higher-priority field rule
overrides it. A positive field rule also grants the model action for matching
rows, while a field-scoped denial masks or protects the field without hiding the
whole row. An unmentioned field sees only the model-wide rules.

Normalization still flattens commutative predicate operators, removes identities
and duplicate branches, and sorts branches into a deterministic canonical
representation. It normalizes the derived predicate; it does not erase rule
priority.

## Read execution contract

The `findMany` fixtures lock these behaviors:

1. Caller policy and request filter are combined with `AND`.
2. A request filter may narrow policy but cannot widen it.
3. Authorization is evaluated before ordering, `skip`, and `take`.
4. No matching authorized rows produces an empty list.
5. System execution bypasses caller policy explicitly.
6. A policy is resolved independently for each actor.

Phase 0 models execution in memory only to pin the result. It does not claim
that an SQL compiler exists.

## Provider contract

Every operator exposed by Phase 0 is marked required for both SQLite and
PostgreSQL. The capability test fails if either provider is absent. This is a
design gate for the first SQL-producing phase, not evidence of database
agreement by itself.

Future operators do not enter the public vocabulary merely by being added to a
list. They require evaluator semantics and live agreement tests on both
providers.

## Explicit implementation non-goals

Phase 0 records contracts and phase ownership but does not implement:

- the application-model struct-tag parser;
- production code generation or migrations;
- database drivers, SQL rendering, or execution;
- GraphQL schema generation or execution;
- repositories or CRUD clients;
- mutation transactions or hooks;
- subscriptions, outbox, or CDC;
- aggregation; or
- production package names.

Queue is excluded from the Go product rather than deferred.

`CanCreate`, `CanUpdate`, and `CanDelete` are executable here only as authoring
vocabulary. `P0_DECISIONS.md` defines the future before/after transaction
contract; P4 owns its implementation and provider proof.
