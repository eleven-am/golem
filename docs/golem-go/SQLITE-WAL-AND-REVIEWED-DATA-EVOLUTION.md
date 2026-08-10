# SQLite WAL and reviewed PostgreSQL data evolution

Status: **accepted implementation contract; not shipped**.

Audience: the engineer implementing this database-hardening slice and the
reviewer deciding whether it is complete. This file covers exactly three
changes:

1. safe provider-owned SQLite WAL configuration;
2. an allowlist of value-preserving PostgreSQL type widenings; and
3. immutable, explicitly reviewed PostgreSQL column backfills.

It does not authorize a new migration system, arbitrary runtime SQL, online
schema-change orchestration, distributed SQLite, application callbacks during
migration, or changes to model/query/policy/business semantics.

Implementation handoff rule: treat every “must” in this file as acceptance
criteria. If the current lifecycle, migration format, or provider API makes one
requirement impossible or contradictory, stop and report that exact boundary
before inventing a broader migration/runtime feature.

## 1. Current baseline

The existing implementation already has important boundaries that must be
preserved:

- SQLite owns and verifies `foreign_keys=1`, `busy_timeout=5000`, immediate
  transaction locking, and a bounded four-connection pool.
- Application DSNs cannot override provider-owned SQLite pragmas through
  spelling, case, or percent-encoding tricks.
- Migration history is immutable, checksummed, parent-chain-hashed, and tied to
  exact before/after physical snapshots.
- `App.Open` does not migrate; deployment applies reviewed migrations
  explicitly.
- PostgreSQL currently classifies every physical type change as data-loss risk
  and refuses `AlterColumnType` in the automatic runner.
- The migration IR already has `BackfillColumn`, `DataTransform`, risk,
  dependency, phase, approval, file checksum, and `ManualCompanion` concepts,
  but provider runners reject ordinary reviewed backfills.

The implementation must extend these foundations rather than bypassing them.

## 2. SQLite WAL decision

### 2.1 Supported topology remains unchanged

WAL improves concurrency between readers and the single SQLite writer. It does
not make SQLite a multi-node database, permit two Golem application instances
to share a database over the network, or remove SQLite's serialized-write
model.

The supported profile remains:

- one application node;
- one local authoritative SQLite file; and
- many concurrent users/requests through Golem's verified local pool.

NATS, if separately configured later, distributes events only. It does not
replicate the SQLite file.

### 2.2 Required default

For every persistent, writable file-backed SQLite database, public
`provider/sqlite.Open` must establish and verify:

```text
PRAGMA journal_mode = WAL
PRAGMA synchronous = FULL
PRAGMA foreign_keys = ON
PRAGMA busy_timeout = 5000
transaction locking mode = immediate
```

`WAL` and `FULL` are provider-owned, not application tuning inputs in version
1. `FULL` is intentional: enabling WAL must not silently weaken the durability
default. A future relaxed durability profile requires a separate accepted
proposal and an explicit public name; it must not be introduced as an
undocumented DSN escape.

Do not add arbitrary `wal_autocheckpoint`, cache-size, mmap, page-size, or
checkpoint tuning in this slice. SQLite's defaults remain in force unless this
contract names a value.

### 2.3 Correct lifecycle

`journal_mode` is database-file state while `synchronous`, `foreign_keys`, and
`busy_timeout` are connection state. The lifecycle must respect that
difference:

1. Canonicalize and validate the DSN before allocation.
2. Open one bootstrap connection before publishing the pool.
3. Execute `PRAGMA journal_mode=WAL` and require the returned mode to be exactly
   `wal` (case-insensitive). A busy, read-only, unsupported-filesystem, or
   different-mode result is a closed open failure; never fall back to DELETE.
4. Configure `synchronous=FULL`, `foreign_keys=ON`, and `busy_timeout=5000` on
   every connection through provider-owned connection initialization.
5. Publish the bounded pool only after all four held slots report the exact
   required state, including `journal_mode=wal` and `synchronous=FULL`.
6. On any failure, close every allocated connection and leave no goroutine or
   partially published `provider.Database`.

Reopening an existing database already in WAL must be idempotent. Moving an
existing writable DELETE-mode database to WAL must preserve all application,
system, migration-ledger, outbox, and semantic-index rows byte-for-byte at the
logical snapshot level.

### 2.4 DSN ownership and exceptional modes

The canonical DSN validator must reject application attempts to set or override
any of these provider-owned values:

- `journal_mode`;
- `synchronous`;
- `foreign_keys`;
- `busy_timeout`; and
- transaction locking mode.

The refusal must cover duplicate keys, mixed case, whitespace variants,
assignment/function pragma syntax, and percent-encoded spellings.

Private `:memory:` databases remain refused by the verified multi-connection
profile. A named `mode=memory&cache=shared` database may remain supported for
tests, but SQLite reports journal mode `memory` there. It must be handled as an
explicit in-memory profile:

- verify `journal_mode=memory` rather than pretending it is WAL;
- retain all other applicable connection invariants;
- never use it as WAL production evidence; and
- document it as test/process-local only.

Read-only mode is not a supported Golem application profile because normal
runtime operation owns migration inspection, outbox/semantic state, and
mutations. A `mode=ro` DSN must fail closed with a configuration/open error
rather than silently running without the WAL guarantee.

### 2.5 Checkpoint, close, and backup truth

WAL and SHM sidecars may contain current committed state. Documentation and
recovery tooling must stop treating the main `.db` file as a complete live
backup.

The supported backup contract is:

1. stop application writes and the event/semantic workers;
2. close the Golem provider handle;
3. perform a bounded `PRAGMA wal_checkpoint(TRUNCATE)` through an owned
   maintenance connection, requiring zero busy frames;
4. close that connection; and
5. copy the database only after verifying no non-empty `-wal` state remains,
   or use SQLite's supported backup API.

A failed/busy checkpoint must fail the backup operation. It must not delete or
truncate sidecars directly. Open/Close should not run an unbounded checkpoint
behind the application's back; explicit recovery/backup code owns the strict
checkpoint boundary.

## 3. Safe PostgreSQL type widening

### 3.1 Meaning of “safe”

Safe means the transition preserves every value representable by the old
logical/storage type. It does **not** mean lock-free, rewrite-free, instant, or
appropriate for an unplanned production peak.

Every accepted widening remains an immutable reviewed migration operation. It
must carry an approval and be conservatively reported as `RiskRewrite` unless
the implementation has exact version-independent proof that PostgreSQL performs
only a metadata change; even then it is at least `RiskLocking` because
`ALTER TABLE` takes a strong lock.

### 3.2 Closed version-1 allowlist

The automatic PostgreSQL renderer may accept only these transitions:

| Before | After | Additional condition |
|---|---|---|
| `smallint` | `integer` | none |
| `smallint` | `bigint` | none |
| `integer` | `bigint` | none |
| `real` | `double precision` | finite/special PostgreSQL values retain their meaning |
| `varchar(n)` | `varchar(m)` | `m >= n` |
| `varchar(n)` | `text` | none |
| `numeric(p,s)` | `numeric(p2,s)` | `p2 >= p`; scale is unchanged |
| `time(p)` | `time(p2)` | `p2 >= p` |
| `timestamptz(p)` | `timestamptz(p2)` | `p2 >= p` |

Transitive integer widening is allowed as shown; no other transitive inference
is allowed. The decision must use normalized typed physical/logical metadata,
not rendered SQL strings or Go type names.

Version 1 explicitly refuses:

- every narrowing transition;
- signed integer to float, decimal, text, or UUID;
- float to numeric or text;
- text to enum/UUID/date/time/JSON or the reverse;
- enum identity/value-set changes disguised as text changes;
- numeric scale changes, even if a particular dataset appears compatible;
- date/timestamp/time family conversions;
- array/list/JSON/bytes conversions;
- collation changes;
- nullability changes disguised as a cast; and
- any provider cast not present in the table above.

A refused transition may be implemented later as a separate reviewed
shadow-column/backfill migration. It must never be admitted because live sample
data happened to cast successfully.

### 3.3 Rendering and operation graph

For an allowlisted widening, the PostgreSQL plan must:

1. retain the same stable model and field identities;
2. emit one `AlterColumnType` operation with exact before/after typed metadata;
3. assign the conservative risk and require matching reviewed approval;
4. drop dependent foreign keys/indexes only where PostgreSQL requires it;
5. execute the typed `ALTER TABLE ... ALTER COLUMN ... TYPE ...` using quoted
   identifiers and an explicit value-preserving `USING` cast when required;
6. restore dependencies in deterministic order;
7. verify the final physical fingerprint before writing the ledger; and
8. roll back the whole phase on cast, lock, timeout, dependency, or fingerprint
   failure.

The renderer must never accept SQL text from the model declaration or select a
cast using a string concatenated from untrusted names. Existing typed snapshots
remain the source of identifiers and storage types.

PostgreSQL C and linguistic profiles must produce the same semantic operation,
risk, dependency graph, and resulting data. Collation must not change merely
because an unrelated column is widened.

## 4. Reviewed PostgreSQL backfills

### 4.1 Purpose and boundary

Version 1 supports one intentionally narrow case: adding a new required column
without a database default to a table that may already contain rows.

The reviewed plan is:

1. add the new column temporarily nullable;
2. execute one reviewed transactional SQL backfill for that column;
3. verify Golem's generated postcondition that no target row remains NULL; and
4. set the column `NOT NULL` and verify the final schema fingerprint.

This is privileged deployment code, comparable to a checked migration SQL
file. It is not a runtime raw-SQL escape hatch and is never reachable from an
application request, hook, custom resolver, GraphQL operation, event handler,
embedding provider, or public `Caller`/`System` value.

### 4.2 Authoring workflow

The implementation must provide this explicit workflow:

```text
golem migration new ... --name add-post-slug

golem migration backfill attach \
  --migrations migrations \
  --migration 002_add_post_slug \
  --field Post.Slug \
  --file ./backfills/post_slug.sql
```

When this exact required-column case is detected, `migration new` must emit a
pending draft bundle and `RiskManual` review output, but must **not** append an
applicable entry to the canonical manifest/chain. `backfill attach` finalizes
only that pending draft as the next canonical head entry. It never rewrites an
existing sealed entry, whether or not some database has applied it. The attach
command must:

- resolve `Post.Slug` through the entry's exact after-model snapshot;
- require that the field is newly added, required, stored, and has no database
  default/generated expression;
- expand the operation into add-nullable, backfill, validate, and set-not-null
  dependencies;
- copy the SQL into the provider migration directory using a deterministic
  operation-ID-derived filename;
- checksum the exact bytes;
- bind the companion, operation, postcondition, risks, approvals, files,
  snapshots, parent hash, and chain hash into a newly sealed head entry;
- render the complete plan for human review; and
- remove the pending draft only after the sealed entry has been written
  atomically; and
- refuse a missing/stale draft, changed parent, foreign provider,
  already-attached field, existing sealed entry, or chain inconsistency.

The command must not accept SQL through a flag value, environment variable, or
stdin. The named file is the only source and is copied into immutable reviewed
history. `migration apply` and every manifest parser must reject pending draft
artifacts as non-history; a half-written or abandoned draft can never become
database authority.

### 4.3 Reviewed artifact rules

Use the existing `BackfillColumn` operation and `ManualCompanion` checksum seam
rather than adding an application callback ABI. Extend manifest validation so a
manual companion may bind exactly one `BackfillColumn`; `ManualStep` remains
unsupported by the automatic provider runner.

The reviewed SQL artifact must be:

- UTF-8 text with LF endings and one final newline;
- at most 1 MiB;
- exactly one PostgreSQL statement as enforced by the driver's extended
  protocol prepare boundary;
- zero-parameter and free of template/interpolation markers; and
- executed exactly as checksummed, with no identifier or value substitution.

The statement is expected to be idempotent, conventionally:

```sql
UPDATE "app"."posts"
SET "slug" = lower("title")
WHERE "slug" IS NULL;
```

Golem cannot prove arbitrary SQL semantics. The operator reviews it as trusted
deployment code. Golem does prove the target operation identity, file bytes,
transaction boundary, generated postcondition, resulting physical fingerprint,
and ledger chain.

The generated postcondition is not author SQL. For this v1 operation it is the
closed condition “the target table contains zero rows whose target column is
NULL,” bound to the stable model/field IDs and hashed into
`ManualCompanion.Postcondition`.

### 4.4 Execution and crash behavior

The PostgreSQL runner must:

1. acquire the existing migration lock;
2. re-verify manifest, chain, approvals, all file checksums, before snapshot,
   and live before fingerprint before executing SQL;
3. run add-nullable, reviewed backfill, generated postcondition, and
   set-not-null inside one database transaction;
4. use the caller's bounded context/deadline and never retry the SQL invisibly;
5. execute the reviewed statement once per database transaction attempt;
6. verify the final physical fingerprint;
7. commit before recording the phase/entry as applied according to the existing
   crash-safe ledger protocol; and
8. expose only closed/redacted migration observations and errors.

A process kill or database disconnect before commit must leave the old schema,
data, and ledger state. A kill after commit but before local acknowledgement
must be recognized from schema/ledger truth and must not apply the backfill to a
different migration state. Reapplying an already-completed migration is an
idempotent no-op.

Large, resumable, chunked, online, concurrent-worker backfills are explicitly
out of scope. Version 1 is a single reviewed transaction suitable only when the
operator accepts its lock/WAL/replication impact and maintenance window.

### 4.5 Privacy and diagnostics

Machine output and observations may include stable migration/operation/model/
field IDs, risk, phase, provider, statement count, and affected-row count. They
must not include:

- SQL file contents;
- row values;
- DSNs or credentials;
- table/column names in public diagnostics;
- raw PostgreSQL errors containing data;
- filesystem paths outside an already-sanitized artifact-relative path; or
- application hook/resolver output.

Operator-facing local review output may display the checked-in SQL before
application, but structured evidence and runtime observations remain closed.

## 5. Mandatory acceptance gates

The implementation is incomplete until these exact gates exist and pass.

### 5.1 SQLite WAL

1. `TestSQLitePublicOpenEnablesAndVerifiesWALOnEveryPooledConnection`
2. `TestSQLitePublicOpenPreservesFullSynchronousAndExistingDataWhenEnablingWAL`
3. `TestSQLitePublicOpenRejectsEveryWALAndSynchronousOverrideSpelling`
4. `TestSQLiteNamedSharedMemoryIsExplicitlyNotWALProductionEvidence`
5. `TestSQLiteWALReaderWriterContentionIsBoundedAndPoolIsReleased`
6. `TestSQLiteBackupCheckpointIncludesCommittedWALStateAndRefusesBusyReaders`

The reader/writer gate must use multiple real pooled connections under `-race`.
It must prove readers can continue around a writer where WAL permits, writes
remain serialized, busy timeout is bounded, foreign keys remain enforced, and
no connection/goroutine survives close.

### 5.2 PostgreSQL type widening

1. `TestPostgreSQLSafeTypeWideningAllowlistAndRiskClassification`
2. `TestPostgreSQLSafeTypeWideningPreservesEveryValueAndDependentObject`
3. `TestPostgreSQLTypeNarrowingAndUnregisteredCastFailBeforeDatabaseWork`
4. `TestPostgreSQLWideningRollbackLeavesSchemaDataAndLedgerUnchanged`
5. `TestPostgreSQLWideningCAndLinguisticProfilesProduceIdenticalTruth`

The value-preservation corpus must cover every allowlisted edge and old-domain
boundary values, including negative integers, infinities/NaN where supported,
maximum-length text, decimal extremes, and maximum old temporal precision.

### 5.3 Reviewed backfills

1. `TestPostgreSQLReviewedBackfillAttachProducesCanonicalImmutableHistory`
2. `TestPostgreSQLReviewedBackfillAddsFillsValidatesAndRequiresColumn`
3. `TestPostgreSQLReviewedBackfillRejectsTamperMultipleStatementsAndWrongTarget`
4. `TestPostgreSQLReviewedBackfillFailureRollsBackSchemaDataAndLedger`
5. `TestPostgreSQLReviewedBackfillCrashBoundariesRecoverExactlyOnce`
6. `TestPostgreSQLReviewedBackfillNeverEntersApplicationRuntimeAuthority`
7. `TestPostgreSQLReviewedBackfillErrorsAndObservationsNeverExposeSQLOrRows`

All live PostgreSQL gates run against both mandatory C and linguistic profiles
with zero skips. Crash evidence must use real subprocess termination at the
same before/inside/after-commit boundaries used by the existing migration
failure suite.

## 6. Mutation resistance

Mandatory tests must kill compiling mutants that:

- silently retain DELETE journal mode;
- configure WAL only on the first SQLite connection;
- lower `synchronous` from FULL;
- allow an encoded DSN override;
- copy only the main database file while committed rows remain in WAL;
- classify a narrowing or numeric-scale change as safe widening;
- skip a widening approval or final fingerprint check;
- run a backfill before checksum/chain validation;
- accept two SQL statements in one companion;
- omit the generated no-NULL postcondition;
- write the migration ledger before the database transaction commits; or
- expose SQL/row/DSN canaries through errors or observations.

Baseline/compile failures are invalid mutation evidence.

## 7. Implementation order

Keep the work in these reviewable waves:

1. **WAL lifecycle:** DSN ownership, bootstrap transition, four-slot proof,
   contention, backup/checkpoint docs and tests.
2. **Widening classifier:** typed allowlist, conservative risk, rendering,
   dependency ordering, zero-work refusals, live data preservation.
3. **Backfill sealing:** attach command, companion validation, checksums,
   operation graph, canonical history tests.
4. **Backfill execution:** transactional runner, postcondition, ledger/crash
   recovery, redacted observations.
5. **Compatibility and docs:** manifest/canonical version handling, historical
   decoder if required, CLI JSON version, Quickstart/Production/runbook updates,
   and final C/linguistic/SQLite evidence.

Do not combine this work with policy-kit, NATS, authentication, optimistic
concurrency, tenant, semantic-index, GraphQL, or TypeScript changes.

## 8. Completion definition

This slice is complete when:

- a normal persistent SQLite application always opens in verified WAL + FULL
  mode across its whole bounded pool and has a truthful backup procedure;
- PostgreSQL automatically accepts only the closed, value-preserving widening
  allowlist with conservative reviewed risk; and
- an operator can seal and apply one checksummed required-column backfill with
  transactional postcondition, crash recovery, immutable history, and no
  runtime SQL authority.

If any path silently falls back, guesses a cast, trusts mutable SQL, skips a
postcondition, or claims online/zero-downtime behavior, the implementation is
not complete.
