# Row-lock ledger

Internal decision record for maintainers. One current account: correct it in
place when the design changes.

## Decision

On PostgreSQL every transaction keeps one ordered lock ledger (`Ledger`). Every
row a write reads in order to change it, every foreign-key parent it relies on,
and every upsert selector guard is locked through the ledger, in one total order,
before the write's statements run. SQLite runs one writer at a time and takes no
row locks.

## Why

Before go/v0.6.5 each statement locked rows in whatever order its own query
returned them (`SELECT ... FOR UPDATE` in predicate order, cascades in
dependency order, foreign-key checks inside the write). Valid concurrent writes
deadlocked, PostgreSQL detected it only after `deadlock_timeout` (1s), and one
write was aborted with 40P01. Named cases, each with a regression test in
`runtime/postgresql_lock_order_test.go`, `postgresql_parent_lock_test.go`,
`postgresql_lock_upgrade_test.go` and `upsert_guard_order_test.go`:

- two updates linking self-related rows to each other in opposite directions;
- a cascade delete of a parent racing an update that relinked one of its
  children;
- a cascade delete racing a transaction that updated a child and then created
  another;
- two transactions that both reference a parent (KEY SHARE from the foreign
  key) and then both update it (upgrade deadlock);
- an `UpdateMany` racing a link between the same rows;
- a nested upsert racing a root upsert on the same selector.

The cost was not only the abort: every blocked writer waited the full deadlock
timeout behind the victims, so throughput collapsed under contention.

## Protocol

1. **Order.** A key is `(model ID, encoded primary identity)`; upsert guards sort
   before every row. `strongestSorted` merges duplicate keys, keeping the
   stronger mode.
2. **Enumerate, lock, re-read** (`Select`). The caller's query enumerates the
   candidate rows without locks. `Lock` locks their keys in ledger order, one
   statement per model and mode, `SELECT ... FOR UPDATE` (or `FOR KEY SHARE`)
   ordered by the key ordinal. The query then runs again under the locks.
3. **Never wait out of order.** A key that sorts before the highest key the
   transaction already holds, or an upgrade of a key it holds (KEY SHARE to
   UPDATE), is taken with `NOWAIT`. Lock failure (55P03) is reported as
   `CONFLICT`. Waiting happens only in ledger order, so the waits-for graph has
   no cycle.
4. **Foreign-key parents** a write will reference are locked `FOR KEY SHARE`
   (`LockReferences`) before the write, in the same order, so the database's own
   foreign-key check never takes a lock out of order.
5. **Upsert guards** are transaction advisory locks ordered before every row: a
   guard is acquired blocking only while the transaction holds no row, and
   otherwise with `pg_try_advisory_xact_lock` (failure is `CONTENTION` →
   `CONFLICT`).
6. **Savepoints.** `Mark` records the ledger length when a savepoint opens;
   `RollbackTo` removes, or restores the previous mode of, every key acquired
   after it, because PostgreSQL releases those locks on rollback to the
   savepoint.

## What `Select` returns

The result is the re-read restricted to rows this transaction holds `FOR UPDATE`.

- A row deleted, or changed so it no longer matches, before it was locked is
  simply absent: `Lock` records only the rows it found, and the re-read does
  not return it. A delete's goal state is already reached; an update has
  nothing to change.
- A row that starts matching after enumeration is not held. An enumeration
  with predicate semantics passes the `admit` callback to `AdmitRows` and
  excludes it: `DeleteMany`/`UpdateMany` (root and nested), nested relation
  expansions, and single-target pre-images. This matches a plain READ COMMITTED
  `DELETE ... WHERE` (its snapshot does not see rows committed after it
  started, and a row deleted or changed away under it is skipped after
  re-checking the predicate) and v0.6.4, whose capture was a single
  `SELECT ... FOR UPDATE`. Including such rows would need a second, possibly
  out-of-order, lock round per re-read and could chase concurrent inserts
  without bound.
- An enumeration that must be complete ignores `admit`; a row it returns
  without holding fails `Select` with `ConflictError` (`CONFLICT`). These are
  the cascade closure (the database removes every dependent, so an unlocked
  one would be deleted without its event; new dependents can appear only below
  the first level, under children not yet locked) and the upsert probe (an
  unseen row would collide with the create branch).
- `FOR KEY SHARE` reference keys are returned whether or not the parent exists:
  a missing parent is reported by the foreign key itself.

So a single target that vanished or stopped matching is `NOT_FOUND`, a batch
changes exactly the rows it holds, and the count, hooks, events and cascade
facts are built from those rows. Tests: `runtime/row_lock_ledger_test.go`,
`runtime/row_lock_batch_rule_test.go` (injected through `WithEnumeratedFault`).

## Rejected: keep predicate-order locks and map 40P01 to `CONFLICT`

Measured with the contention harness (32 workers, mixed writes on PostgreSQL
17, 20s runs):

| hot rows | v0.6.4 (40P01 → CONFLICT) | ledger |
|---|---|---|
| 10 | 45 ops/s, success p99 9.1s | 1046 ops/s, success p99 299ms |
| 50 | 191 ops/s, success p99 3.4s | 1409 ops/s, success p99 67ms |

Lowering `deadlock_timeout` to 100ms only shortens the stall: 341 ops/s at 10
hot rows, still about 3x worse than the ledger, and it is a server setting
golem cannot own. Uncontended, the ledger costs about +0.4ms per write (two
extra statements: enumerate and lock) and about 20% single-writer throughput
(528 → 423 updates/s).

## Known trade-off

An application transaction that writes rows in an order other than the ledger
order does not wait: it fails fast with `CONFLICT` when another transaction
holds an earlier key. Writers that touch shared rows in a consistent order, or
retry on `CONFLICT`, are unaffected. The GUIDE says this in user terms.
