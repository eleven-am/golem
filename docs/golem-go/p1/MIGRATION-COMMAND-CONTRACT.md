# P1 migration command contract

Status: controlling command contract for the P1 migration-history owner.

This document fixes the migration-history command surface.

## Commands

```text
golem migration new --name <slug>
  [--schema <pattern>] [--root <function>]
  [--migrations <directory>] [--approve <operation-id> ...]

golem migration apply --provider <sqlite|postgresql> --dsn <value>
  [--migrations <directory>]
```

`--schema` defaults to `.`, `--root` to `DefineSchema`, and `--migrations` to
the canonical module-relative path `migrations`. A migration slug must match `[a-z][a-z0-9_]{0,62}`. There is no
force, resume, ambient-DSN, manual-SQL, or global destructive-approval flag.
Credentials are never printed.

`--approve` is repeatable and accepts only the stable ID of a current
`DataLoss` or `Manual` operation. The stored approval copies that operation's
exact provider, risk, before fragment, and after fragment. Unknown, stale,
safe, locking, or rewrite operation IDs are refused and diagnostics identify
the provider and exact risk.

## Reviewed history layout

For migration ID `<NNNN>_<slug>`, history is self-contained beneath the
configured root:

```text
<migrations>/.golem-publication.json
<migrations>/<provider>/manifest.json
<migrations>/<provider>/<NNNN>_<slug>.sql
<migrations>/<provider>/<NNNN>_<slug>.before.snapshot.json
<migrations>/<provider>/<NNNN>_<slug>.after.snapshot.json
<migrations>/models/<NNNN>_<slug>.before.snapshot.json
<migrations>/models/<NNNN>_<slug>.after.snapshot.json
```

The publication inventory is separate from generated-code inventory. Ordinary
`generate` therefore cannot create, rewrite, or delete migration history.
Provider manifests are append-only semantically; existing entries and all SQL
and physical/ModelIR snapshot files are immutable. New migrations advance every declared
provider head in lockstep through one crash-recoverable publication.

The initial entry diffs the provider's canonical empty schema and the
domain-separated canonical empty ModelIR. Every later entry requires the exact
previous reviewed physical and ModelIR heads. Planning never reads a live
database. Names, provider order, operation order, files, snapshots, manifests,
and chain hashes are deterministic.

The compiler transfers identities from that previous reviewed ModelIR for
`renameFrom`. A source declaration may reference the prior source-authorable
canonical identity or its 32-hex stable object ID. Migration creation refuses a
missing, ambiguous, wrong-kind, wrong-scope, or multiply claimed reference; it
never degrades one to an inferred drop/create migration.

Each entry contains exactly one provider-rendered SQL artifact. Before database
work, the provider verifies the complete immutable manifest/checksum chain and
proves that the manifest-bound SQL bytes equal its versioned deterministic
renderer for the typed entry. The runner then executes those same reviewed
statements with its private lock, structural guards, introspection, and ledger
orchestration. Arbitrary file SQL is never executable input.

`migration apply` requires an explicit provider and nonblank explicit DSN. It
applies all pending reviewed entries sequentially; each provider call applies
exactly the next entry in its own transaction. It stops at the first failure
and succeeds without mutation when already current.

`golem check` verifies history automatically when the default history root is
present, or the explicitly configured root when `--migrations` is supplied.
This includes publication ownership, immutable checksums, provider manifests,
embedded snapshots, operation/phase order, approvals, and chain continuity.
P1 defines no command-level live-check flags.
