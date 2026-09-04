# Proposal: system-owned fields

Status: **implemented**. Two details below differ from the original
proposal; both are marked and the reason recorded, because the first draft
described an API that could not be built.

## The gap

An application cannot express a field that it maintains and no client may
set.

The case that surfaced it: an article carries a denormalised tag count. The
application increments it when a tag is attached, inside the same
transaction as the join-table write, because a separate transaction would
let the count desync from the rows it counts. No client should be able to
set that number.

Every existing lever fails.

`readonly` removes the write builders at generation, so `Increment` and
`Decrement` disappear for everyone. It binds `System()` too, because schema
modes describe the data rather than the caller.

`CannotUpdateFields` blocks callers and is bypassed by `System()`, which is
the right shape — but the increment happens inside a caller transaction, and
`CallerTx` holds only a `*Caller`. There is no way to reach the system
stance without leaving the transaction.

Predicate narrowing cannot help: `CanUpdateFields` re-grants against a
predicate over the row, and an increment is a statement about the delta.

The generalisation is broader than counters. `created_by` stamped from the
resolved actor, a `tenant_id` derived at insert, `last_seen_at` — any value
the application owns and a client must not forge.

## What exists today

Schema modes answer **when** a field may be written. Policy answers **who**
may write it. Nothing answers "the application, but never a client."

| | who may write | when |
|---|---|---|
| *(none)* | anyone policy allows | create and update |
| `immutable` | anyone policy allows | create only |
| `readonly` | nobody | never |

`readonly` is absolute by construction: `mutationCapabilities` takes no
client parameter, so one capability set serves both the caller and system
clients, and `internal/mutation/plan/build.go` enforces the same exclusion at
runtime.

## The proposal

Two changes. The second is load-bearing; the first is unusable without it.

### 1. `SystemEscape` — a system client on the caller's transaction

```go
err := caller.Transaction(ctx, func(tx *notes.CallerTx[Principal]) error {
	if _, err := tx.ArticleTags.Create(ctx, ...); err != nil {
		return err
	}
	system := notes.SystemEscape(tx)
	_, err := system.Articles.UpdateMany(ctx,
		notes.Articles.Where(notes.Articles.ID.Eq(id)),
		notes.Articles.Update(notes.Articles.System().TagCount.Increment(1)),
	)
	return err
})
```

**Changed from the first draft.** It specified a `tx.System()` method. That
cannot be emitted: `EmitShell` is caller-only by construction, and
`TestEmitShellMatchesFinalCallerABI` refuses any `System` symbol in the
bootstrap shell. Making the method legal means emitting stub `SystemTx`
types there and granting the declaration-discovery pass a system stance —
a policy expansion, not a mechanical fix. A free function needs none of it,
and matches every other transaction seam in the runtime.

One transaction, both stances. The join write is authorized; the counter
update is not, and says so at the call site.

This is a policy bypass reachable from application code, in a system whose
central claim is that policy compiles into every query. It must be
conspicuous rather than convenient:

- the escape is a method call at the point of use, never an option on the
  transaction or a field on a config
- it emits an observation, so a deployment can see how often application code
  leaves the authorized path
- the generated method carries godoc saying what it skips

### 2. `system` — a field mode that names the owner

```go
TagCount  int  `db:"tag_count" golem:"system"`
CreatedBy uuid `db:"created_by" golem:"system;immutable"`
```

`system` withholds the write builders from caller clients and keeps them on
system clients. `system;immutable` keeps only create.

**Changed from the first draft.** It wrote `Articles.TagCount.Increment(1)`
from inside the escape. A package-level `var Articles` has one type for both
stances, so it can withhold nothing. The write builders live in a second
namespace instead: `Articles.System().TagCount.Increment(1)`. Codegen makes
the mistake unrepresentable on the common path; the runtime plan is what
actually binds.

`system;readonly` is rejected: `readonly` already means nobody writes, so
naming an owner is meaningless.

The mode is not redundant with `CannotUpdateFields`, because the two act in
different layers. `mutationFieldRefusal` consults schema modes, so a
`system` field never appears in the generated GraphQL input type at all,
while a policy-blocked field appears and is refused on submission. A mode
also cannot be forgotten in one policy out of twenty.

| | `system` mode | `CannotUpdateFields` |
|---|---|---|
| GraphQL input | absent | present, refused at runtime |
| stated on | the model | the policy |
| varies by actor | no | yes |
| forgettable | no | yes |

## What this costs

`mutationCapabilities(field, model, contract)` takes no client parameter
today, which is exactly why modes are absolute and easy to reason about.
`system` is the first mode that varies by caller, and adding it means that
function grows a client dimension and every reader of a mode has to ask
"which client".

That is the real price, and it is worth stating before the work starts
rather than discovering it in review.

## Rules to settle

- `system` + `immutable`: system writes at create, then nobody. Accepted.
- `system` + `readonly`: rejected at compile with a message naming both.
- `system` + a policy granting the field to callers: the mode wins. Modes are
  absolute today and this must not become the exception, or a reader can no
  longer tell what a mode means without reading every policy.
- `system` says nothing about reads. `readonly` does not either.
- `system` on a relation field is refused at compile
  (`P1_GOLEM_TAG_SYSTEM_RELATION`). The relation mode path would have dropped
  it silently, and a silent no-op is worse than a stated rule.
- A `system` optimistic-concurrency token is refused (`P1_CONCURRENCY_SYSTEM`)
  rather than falling through to a distant registry error.
- A `system` field still obeys `CanUpdate` on the *row* for system writes?
  No — `System()` bypasses policy entirely, and that does not change.

## Answered: no contract bump

The current contract is version 6 and performs no field-mode whitelist —
`CanonicalContract` only sorts modes. The sole whitelist is `v5FieldModes`,
reached only by `CanonicalDecodeContractV5`, and it is deliberately left
without `system`: a v5 contract cannot contain the mode, and replay refuses
it.

Nothing was added to the serialised shape, so a schema declaring no `system`
field produces byte-identical canonical JSON and an unchanged fingerprint.
Only a schema that uses the mode gets a new one, which is the ordinary
consequence of editing a schema.

## Decided: declarations get the system stance

The first implementation left the escape unreachable from schema-package
code, and left a mutation hook unable to write a `system` field because
hook-replaced inputs re-enter with the caller stance. Both are now closed:
without them the mode mostly produces compile errors in the place custom
mutations actually live — `examples/social/social/extensions.go` already
opens `caller.Transaction` inside the schema package.

The shell exposes the system surface to the whole schema package, because it
type-checks that package as one unit and cannot scope by declaration kind.
That is narrower than it sounds. `SystemEscape` takes a `*CallerTx[P]`, and a
policy body receives `(rules *golem.Rules[M], actor Actor)` — no context, no
caller, no transaction. A policy cannot call it because it has nothing to
pass, and the type system enforces that rather than a convention. Only code
already holding a caller transaction can reach it, which is the code that
needs it.

The ABI test that rejected any `System` symbol in the shell is relaxed to
permit exactly the system surface, and must keep proving the shell and the
final registry agree. A test that merely stops checking would leave
generation-time type-checking free to drift from what actually compiles.

## Not in scope

Per-field policy on system writes, a way for a client to propose a value the
application validates, and any change to how `System()` behaves outside a
transaction.
