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
   stronger mode. `plan` decides, from the keys the transaction already holds,
   which keys still need a lock and whether each may wait.
2. **One lock statement shape.** Every row lock is
   `SELECT <columns>, <match>, <ordinal> FROM t WHERE <keys> ORDER BY <ordinal>
   FOR UPDATE` (or `FOR KEY SHARE`, `NOWAIT` where step 3 requires it), one
   statement per run of keys sharing a model, a mode, the wait choice and (for
   a locking read) the reader, chunked to the parameter limit. `WHERE` names only
   primary keys, so every row it finds is locked and returned, and recorded in
   the ledger. A plain `Lock` reads no caller columns and has no predicate.
3. **Never wait out of order.** A key that sorts before the highest key the
   transaction already holds, or an upgrade of a key it holds (KEY SHARE to
   UPDATE), is taken with `NOWAIT`. Lock failure (55P03) is reported as
   `CONFLICT`. Waiting happens only in ledger order, so the waits-for graph has
   no cycle.
4. **Predicate selections: enumerate, then lock and read in one statement**
   (`SelectMatching`). The caller's query enumerates the candidate rows without
   locks. Each candidate the transaction does not already hold is then locked
   by a statement that also returns the caller's columns and evaluates the
   caller's predicate as a column (`<match>`): PostgreSQL re-evaluates both on
   the row version it locked, so the result is the row as locked and whether it
   still matches. Each enumerating statement has its own `Reader` (columns,
   predicate, arguments); the nested set difference enumerates current members
   and each desired target separately, and a row both enumerate is locked by
   the first reader's statement and read again, without waiting, by the
   second's. Rows already held are returned as enumerated: no other
   transaction can have changed them.

   The merged lock-read is used only for a reader proven, from its rendered
   SQL, to read nothing but the target row, and whose own arguments leave room
   in the parameter bound for at least one identity (`Reader.mergeable`). The
   proof is syntactic and closed: after skipping quoted identifiers and
   literals, the columns and predicate may not contain `SELECT`, `FROM`, `JOIN`,
   `TABLE`, `LATERAL` or `VALUES`, and every function call must be a listed
   operator form (`COALESCE`, `CASE`, ...), a `pg_catalog.` function or a type
   cast. Anything else, including a policy or field grant that follows a
   relation (an `EXISTS (SELECT ...)`), takes the fresh-read path: the reader's
   keys are locked with a plain `Lock` in the same ordered round. Once the
   round has taken any lock, every such reader that enumerated rows runs its
   enumeration again as a new statement and keeps the rows the transaction
   holds, including a reader whose rows were all held before the round: any
   lock in the round may have waited, and a grant it depends on may have been
   revoked meanwhile. Why: a statement's subqueries read other tables at the
   snapshot taken when the statement started. If a lock waits, a grant revoked
   by the blocking transaction would be unseen and the write authorized on
   stale data; a new statement after the lock sees it. A merged reader whose
   rows were all held keeps its enumeration: it reads only those rows, and no
   other transaction can change a row this one holds. The parameter fallback keeps
   a selection whose own arguments fill the bound working, as it did before.
5. **Complete selections: enumerate, lock, enumerate again** (`Select`). The
   cascade closure, the root upsert probe and foreign-key parents found through
   a unique key must see every row, including one that started to match after
   enumeration, which a locking read of known keys cannot find. They lock with
   `Lock` and run the enumeration again under the locks.
6. **Foreign-key parents** a write will reference are locked `FOR KEY SHARE`
   (`LockReferences`) before the write, in the same order, so the database's own
   foreign-key check never takes a lock out of order. A parent referenced by
   its primary key is keyed from the written value without a query (mutation
   input is refused when it exceeds the column's declared precision or scale,
   and identity encoding is canonical, so the key equals the row's). A parent
   referenced through another unique key is found by a query, a complete
   selection. A parent the transaction already holds is not locked again.
7. **Upsert guards** are transaction advisory locks ordered before every row: a
   guard is acquired blocking only while the transaction holds no row, and
   otherwise with `pg_try_advisory_xact_lock` (failure is `CONTENTION` →
   `CONFLICT`).
8. **Savepoints.** `Mark` records the ledger length when a savepoint opens;
   `RollbackTo` removes, or restores the previous mode of, every key acquired
   after it, because PostgreSQL releases those locks on rollback to the
   savepoint.

## What a selection returns

`SelectMatching` returns the rows this transaction holds `FOR UPDATE` that still
match, each as it was when locked, in enumeration order.

- A row deleted before it was locked is absent: the lock statement does not
  find it. A row changed so it no longer matches is locked but its `<match>`
  column is false, so it is left out. A delete's goal state is already reached;
  an update has nothing to change.
- A row that starts matching after enumeration is not among the keys and is
  not held. This covers `DeleteMany`/`UpdateMany` (root and nested), nested
  relation expansions, and single-target pre-images, on both paths: the merged
  lock-read names only enumerated keys, and the fresh-read path keeps only rows
  the transaction holds. A row that stopped matching is judged on the version
  locked; on the fresh-read path any other table it depends on is read after
  the lock. Including rows that start matching would need
  a second, possibly out-of-order, lock round and could chase concurrent
  inserts without bound.
- The predicate is a column, not part of `WHERE`. In `WHERE`, PostgreSQL would
  lock a row whose new version fails the re-check and not return it, so the
  ledger would not know it holds that lock and could later wait out of order
  while holding it.

`Select` (complete) returns the second enumeration. A row it returns without
holding fails with `ConflictError` (`CONFLICT`): for the cascade closure the
database removes every dependent, so an unlocked one would be deleted without
its event (new dependents can appear only below the first level, under
children not yet locked); for the upsert probe an unseen row would collide with
the create branch. The one exception is a `FOR KEY SHARE` parent key that the
first enumeration requested and that was missing when locked: it is admitted,
and a missing parent is reported by the foreign key itself. A parent key the
second enumeration finds that the first did not (another row took the unique
value) is a `CONFLICT`, since the foreign-key check would lock it out of order.

So a single target that vanished or stopped matching is `NOT_FOUND`, a batch
changes exactly the rows it holds, and the count, hooks, events and cascade
facts are built from those rows. Tests: `runtime/row_lock_ledger_test.go`,
`runtime/row_lock_batch_rule_test.go` (injected through `WithEnumeratedFault`).

## Cost

Beyond its enumeration, a merged predicate selection costs one statement; a
fresh-read predicate selection (a policy, grant or predicate that reads another
table, or arguments that fill the parameter bound) and a complete selection
cost two. A single-row update with a single-table policy is 6 statements inside
its transaction; with a relation policy it is 7, as in go/v0.6.6. The counts
are pinned by the PostgreSQL observation oracles (`internal/p8oracle/mutation`,
`internal/p8oracle/load`, single-table policies), by
`runtime/mutation_cascade_cost_test.go`, and, for the fresh-read path, by
`runtime/row_lock_fresh_read_test.go`.

## Rejected: keep predicate-order locks and map 40P01 to `CONFLICT`

Measured with the contention harness (32 workers, mixed writes on PostgreSQL
17, 20s runs):

| hot rows | v0.6.4 (40P01 → CONFLICT) | ledger |
|---|---|---|
| 10 | 45 ops/s, success p99 9.1s | 1046 ops/s, success p99 299ms |
| 50 | 191 ops/s, success p99 3.4s | 1409 ops/s, success p99 67ms |

Lowering `deadlock_timeout` to 100ms only shortens the stall: 341 ops/s at 10
hot rows, still about 3x worse than the ledger, and it is a server setting
golem cannot own. Uncontended, the ledger as first shipped (go/v0.6.5) cost
about +0.4ms per write (two extra statements: lock and re-read) and about 20%
single-writer throughput (528 → 423 updates/s). Merging the lock and the
re-read (go/v0.6.7) leaves one extra statement per predicate selection whose
reads stay on the target row: a single-row update under a single-table policy
is 6 statements inside its transaction (v0.6.4: 5, v0.6.5: 7), and in alternating runs single-writer update latency fell by about 0.8ms
(p50) with about 12% more updates/s.

## Known trade-off

An application transaction that writes rows in an order other than the ledger
order does not wait: it fails fast with `CONFLICT` when another transaction
holds an earlier key. Writers that touch shared rows in a consistent order, or
retry on `CONFLICT`, are unaffected. The GUIDE says this in user terms.

## Known gap

A row whose primary key another transaction changes, and commits, while a lock
statement waits for it is locked but not returned: PostgreSQL re-checks
`WHERE <keys>` on the new version, which no longer matches. The ledger does not
record that lock, so a later wait in the same transaction can be out of order.
Single updates may change a primary key; batch updates may not. Present since
go/v0.6.5, unchanged by the merged lock-read.

The merged lock-read evaluates the target row's own columns on the version it
locked, but a statement cannot see other tables after its start. That is why a
reader that reads any other table, or that cannot be proven not to, uses the
fresh-read path; the price is the second statement go/v0.6.6 paid.
