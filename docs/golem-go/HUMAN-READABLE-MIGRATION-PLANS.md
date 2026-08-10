# Human-readable migration plans implementation contract

Status: **accepted implementation contract; not shipped**.

Audience: the engineer implementing the feature and the reviewer deciding
whether it is complete. This file is self-contained and covers presentation of
Golem's existing typed migration plan only.

Implementation handoff rule: every “must” is acceptance criteria. If the
current migration IR cannot truthfully provide a required fact, report the
exact missing fact before adding a guess, a second planner, or a new migration
behavior.

## 1. Product decision

Golem will add a read-only command that explains either a prospective schema
change or an already reviewed migration:

```text
golem migration plan --schema ./social --migrations migrations

golem migration plan --migrations migrations --migration 002_add_post_slug
```

Terminal output is concise human text by default. `--json` emits one canonical,
versioned machine document. Both formats are renderings of the same immutable
typed report.

The command exists so an operator can understand:

- what changes;
- in what order;
- why one operation depends on another;
- which provider is affected;
- whether values are preserved, rewritten, locked, manually transformed, or
  deleted;
- which exact approvals are required;
- which reviewed artifacts and postconditions participate; and
- what Golem does **not** guarantee, especially online or zero-downtime
  execution.

## 2. Non-goals

This feature must not:

- apply, create, attach, approve, edit, publish, or re-chain a migration;
- open a database or inspect live data/schema drift;
- accept a DSN;
- create a second diff, dependency, phase, risk, or approval algorithm;
- infer duration, table size, traffic impact, lock wait, or downtime;
- claim a provider will avoid a rewrite unless that fact is already a frozen
  provider-independent contract;
- generate AI prose or accept a text-generation provider;
- conceal or replace review of generated/reviewed SQL;
- print SQL bodies, row values, DSNs, credentials, environment variables, or
  absolute filesystem paths; or
- make human prose part of migration authority.

The authoritative artifacts remain typed operations, phases, snapshots,
checksums, approvals, provider SQL files, manifest entries, and chain hashes.

## 3. Command modes and flags

Add `plan` to the existing `golem migration` command family.

### 3.1 Prospective mode

```text
golem migration plan \
  --schema ./social \
  --root DefineSchema \
  --migrations migrations \
  [--provider sqlite|postgresql] \
  [--json]
```

Prospective mode compiles the current schema exactly as `migration new` does,
loads the reviewed head as the before state, lowers each declared provider, and
uses the existing migration diff/order/phase/risk machinery. It writes nothing.
It must produce the same provider plans and operation IDs that a subsequent
`migration new` produces against the unchanged tree/head.

`--provider` filters presentation after the complete declared-provider plan is
validated. It must not change operation identity or hide a failure in another
declared provider. Without it, providers appear in canonical provider order.

### 3.2 Reviewed mode

```text
golem migration plan \
  --migrations migrations \
  --migration 002_add_post_slug \
  [--provider sqlite|postgresql] \
  [--json]
```

Reviewed mode loads only immutable published history. It verifies the complete
manifest, parent chain, selected entry, snapshots, approvals, and every file
checksum before rendering. It does not recompile application source and it
must refuse pending backfill drafts as non-history.

### 3.3 Flag rules

- `--migration` and `--schema` are mutually exclusive.
- Prospective mode defaults `--schema .` and `--root DefineSchema` consistently
  with `migration new`.
- Reviewed mode refuses `--root` and any explicit schema pattern.
- `--json` is the only format switch in version 1.
- The command accepts no `--approve`, `--dsn`, `--apply`, `--show-sql`, output
  template, color-control, or arbitrary format flag in version 1.
- Exit status is zero only after the full report was derived and encoded.

## 4. Internal architecture

Create one provider-neutral report package under `internal/migration/explain`.
It consumes only validated normalized values:

- `migration.Plan` for prospective mode; or
- one validated `migration.ManifestEntry` plus its verified file inventory for
  reviewed mode.

It must call `migration.Order` and use the existing plan phases. It must not
sort dependencies independently, recalculate risk, or infer operations from
SQL text.

The package returns one immutable report consumed by both the text and JSON
renderers. Text must never contain information absent from the report. JSON
must never contain information absent from the same report.

## 5. Closed report model

The machine document is `formatVersion: 1` and has this required shape:

```json
{
  "formatVersion": 1,
  "mode": "prospective",
  "status": "REVIEW_REQUIRED",
  "providers": [],
  "warnings": [],
  "guarantees": {
    "appliesChanges": false,
    "usesReviewedTypedPlan": true,
    "zeroDowntime": false,
    "durationEstimated": false
  }
}
```

Closed enums:

```text
mode: prospective | reviewed
status: NO_CHANGES | REVIEW_REQUIRED
provider: sqlite | postgresql
risk: safe | locking | rewrite | dataLoss | manual
transactionMode: transactional | autocommitOnly
effect: valuePreserving | valueRewritten | valueDeleted |
        schemaOnly | manualDataTransform | unknown
warning:
  APPROVAL_REQUIRED
  DATA_LOSS
  TABLE_OR_INDEX_REWRITE
  STRONG_LOCK_POSSIBLE
  MANUAL_REVIEW
  REVIEWED_BACKFILL
  AUTOCOMMIT_BOUNDARY
  ZERO_DOWNTIME_NOT_GUARANTEED
```

Each provider report contains:

- provider and initial/incremental status;
- before/after physical fingerprints;
- ordered phases;
- provider artifact relative paths and SHA-256 values when reviewed;
- provider-level closed warnings; and
- operation counts by risk.

Each phase contains:

- ordinal and transaction mode;
- before/after fingerprints;
- operation IDs in authoritative order; and
- phase-level warnings derived only from member operations.

Each operation contains:

- stable operation ID, kind, stage, and logical path;
- stable affected object/model/field/index/relation identity where the typed
  snapshots make it available;
- optional logical Go-facing model/field name for local readability;
- risk, effect, transaction mode, dependencies, and capabilities;
- exact before/after object digests;
- whether explicit approval is required and whether reviewed mode contains the
  exact matching approval;
- reviewed companion relative path/checksum and postcondition digest, if any;
  and
- closed warnings.

Logical names are display labels only. IDs and digests remain authoritative.
Never include physical schema/table/index/constraint names in the machine
document. The local terminal renderer may show a Go-facing name such as
`Post.Title`, but never a DSN or absolute path.

## 6. Effect classification

Effect is not a new risk system. It is a deterministic display mapping from
operation kind plus already validated before/after typed metadata:

- `valueDeleted`: drop column/table and other explicitly destructive removal;
- `manualDataTransform`: reviewed `BackfillColumn`/manual data operation;
- `valueRewritten`: table rebuild, provider extension recreation, and approved
  type widening/rewrite;
- `valuePreserving`: typed safe widening and operations whose contract
  explicitly preserves existing values;
- `schemaOnly`: rename, index/constraint/default metadata, or new empty object;
  and
- `unknown`: only when no truthful mapping exists.

`unknown` must add `MANUAL_REVIEW`. It must never be silently displayed as safe.
The mapping is exhaustively tested against every `migration.OperationKind` so a
new operation cannot accidentally inherit a benign explanation.

## 7. Terminal rendering

Text output must be deterministic and structured, for example:

```text
Migration plan: prospective
Status: REVIEW REQUIRED

PostgreSQL — incremental — 2 phases
  Phase 0 — transactional
    [rewrite] Post.Title: widen varchar(160) -> varchar(320)
      operation: 7fc2...
      effect: existing values preserved; table rewrite/strong lock possible
      depends on: drop-index-...
      approval: not required by manifest policy
    [manual] Post.Slug: reviewed required-column backfill
      approval: required (--approve 921a... when sealing)
      postcondition: no target value remains NULL

Guarantees: read-only; no duration estimate; zero downtime is not guaranteed.
```

Rules:

- canonical provider, phase, and operation order;
- no ANSI color when output is not a terminal;
- no terminal width-dependent omission of facts;
- no raw SQL or operands;
- checksums may be shortened in text only when the full value remains in JSON;
- all risks and warnings use fixed wording from source templates; and
- a `NO_CHANGES` report still states that no files/database were modified.

## 8. Read-only and privacy proof

Both modes must take a before/after tree snapshot including file bytes, modes,
symlinks, generation locks, WAL/SHM files, and migration history. The command
fails if it changes the tree.

It must not open provider connections, start workers, invoke application hooks,
or read environment DSNs. Build subprocess failures use existing closed build
diagnostics. Errors must redact absolute paths and never echo command arguments
that may contain secrets.

Prospective mode may use an owned temporary build directory outside the module;
it must remove it on success/failure/panic and leave no generated artifacts in
the application tree.

## 9. Relationship to widening and backfills

The accepted
[`SQLITE-WAL-AND-REVIEWED-DATA-EVOLUTION.md`](./SQLITE-WAL-AND-REVIEWED-DATA-EVOLUTION.md)
contract remains controlling for widening/backfill semantics. This feature only
explains those operations. It does not attach a backfill, approve a widening,
or make either operation safer.

The reviewed-backfill attach command must reuse this report renderer to show
the final plan before sealing. That reuse must not let `migration plan` write.

## 10. Mandatory acceptance gates

1. `TestMigrationPlanProspectiveMatchesMigrationNewWithoutWriting`
2. `TestMigrationPlanReviewedVerifiesHistoryAndEveryArtifactBeforeRendering`
3. `TestMigrationPlanTextAndJSONShareOneCanonicalTypedReport`
4. `TestMigrationPlanExplainsEveryOperationRiskEffectDependencyAndApproval`
5. `TestMigrationPlanExplainsSafeWideningAndReviewedBackfillWithoutClaims`
6. `TestMigrationPlanNoChangesIsDeterministicAndReadOnly`
7. `TestMigrationPlanNeverPrintsSQLValuesDSNsPhysicalNamesOrAbsolutePaths`
8. `TestMigrationPlanRejectsTamperPendingDraftUnknownKindAndInvalidFlags`
9. `TestMigrationPlanPublicJSONFormatIsClosedVersionedAndBounded`
10. `TestMigrationPlanFreshExternalModuleCommandCorpus`

The external gate uses a clean `example.com` module and covers SQLite plus
PostgreSQL reviewed histories. It must not require live databases because this
command is deliberately offline.

## 11. Mutation resistance

The gates must kill compiling mutants that:

- recalculate operation order instead of using the typed plan;
- label data loss, rewrite, or unknown work as safe;
- omit an approval, dependency, backfill, or postcondition;
- claim zero downtime or invent a duration;
- print raw SQL, a bound canary, a DSN, or an absolute path;
- render before verifying a reviewed checksum/chain;
- let provider filtering hide a failing declared provider;
- write generated/migration files in prospective mode; or
- emit an unversioned/open-ended JSON field.

Compile or baseline failures are invalid mutation evidence.

## 12. Completion definition

This feature is complete only when an operator can understand the exact typed
prospective or reviewed plan—risks, ordering, dependencies, approvals,
backfills, checksums, and non-guarantees—without changing the repository or
database and without receiving guessed or sensitive information.
