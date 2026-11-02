# SQLite durability profiles

Golem continues to use and enforce write-ahead logging by default. Local-disk
deployments do not need to change their configuration.

SQLite databases deliberately placed on a network filesystem can opt into the
rollback-journal profile:

```go
database, err := sqlite.OpenNetworkFilesystem(ctx, sqlite.Config{
    DataSourceName: "file:/data/application.sqlite",
})
```

This profile enforces `journal_mode=DELETE`, `synchronous=FULL`, foreign keys,
a 5-second busy timeout, immediate transactions, and a single open/idle
connection. It is explicit rather than filesystem-detected. Named in-memory
databases and caller overrides of provider-owned pragmas remain rejected.

## Write-ahead logging on a network filesystem

`sqlite.OpenNetworkFilesystem` avoids WAL because ordinary WAL keeps its index
in a `<database>-shm` file that every connection memory-maps and grows. On Linux
the driver grows that file with `fallocate(2)`, which NFS 4.1 mounts reject
with `EOPNOTSUPP`, so ordinary WAL fails there and leaves an empty `-shm`
behind. SQLite can run WAL without that file when the connection uses
exclusive locking: the WAL index then lives in the process's heap memory.
`sqlite.OpenNetworkFilesystemWAL` opts into exactly that:

```go
database, err := sqlite.OpenNetworkFilesystemWAL(ctx, sqlite.Config{
    DataSourceName: "file:/data/application.sqlite",
})
```

Every connection the provider creates sets `locking_mode=EXCLUSIVE` as its
first statement, before it can read the database or enter WAL, and the profile
then enforces and verifies `journal_mode=WAL`, `locking_mode=EXCLUSIVE`,
`synchronous=FULL`, foreign keys, a 5-second busy timeout, immediate
transactions, and a single open/idle connection that never expires. Caller
overrides of `locking_mode` are rejected for every profile. A database that is
not file-backed is rejected. A non-empty `<database>-shm` next to the database
is refused, because another process may be using it in ordinary WAL mode; an
empty leftover is tolerated.

The trade-off is exclusive access. While the application holds the handle, no
other process can read or write the database: not another replica, not the
`sqlite3` command-line tool, and not a backup job. They fail with a busy error
until the handle is closed. Choose this profile over the rollback-journal one
when one process owns the database and you want WAL's cheaper commits, which
append to the log instead of rewriting database pages through a rollback
journal. The pool holds a single connection, so the profile adds no read
concurrency inside the process. Choose
`sqlite.OpenNetworkFilesystem` when anything else must open the file while the
application runs.

A rollback-journal database opened with this profile is converted to WAL. The
reverse needs no manual step. On a filesystem that rejects the `-shm` file, a
connection without exclusive locking cannot read a WAL database at all, so
`sqlite.OpenNetworkFilesystem` converts it before the normal open. When the
database file's header declares WAL, the provider opens one dedicated
connection with `locking_mode=EXCLUSIVE` set before its first database access,
verifies exclusive locking, runs `PRAGMA wal_checkpoint(TRUNCATE)` and
`PRAGMA journal_mode=DELETE`, requires `PRAGMA integrity_check` to report `ok`,
and closes the connection before opening the rollback-journal pool. The same
conversion recovers a database left in WAL by `sqlite.Open` or by a crash,
including frames that were only in the `-wal` file; SQLite removes that file
when the conversion completes. The conversion fails closed and changes nothing
when a non-empty `<database>-shm` exists, because another process may be using
the database in ordinary WAL mode, and when another process holds the database
so the exclusive lock cannot be obtained. An empty `-shm` leftover is
tolerated. The provider never deletes or truncates a sidecar file itself. A
database already in rollback-journal mode opens exactly as before, and the WAL
profiles never convert.

### Recovering from a stale `-shm`

A non-empty `<database>-shm` blocks both network profiles: the WAL profile
refuses to open the database and the rollback profile refuses to convert it.
Golem never deletes that file automatically, because a live process may still
be using it. If the file is stale, for example after a crash of a process that
used ordinary WAL, recover it by hand:

1. Stop every process that uses the database.
2. Confirm that none still holds it: open the database with the `sqlite3`
   command-line tool and check that `BEGIN IMMEDIATE; ROLLBACK;` does not
   report a busy or locked error.
3. Only then remove the `-shm` file, and open the database with the profile
   you need.

## Backups and replicas

Before copying the database file, stop all application readers and writers,
close the verified handle, and call `sqlite.CheckpointForBackup`. For the
default WAL profile this performs and verifies a truncating checkpoint. For the
network-filesystem profile it verifies rollback-journal mode and returns: a
closed, committed rollback-journal database has no WAL frames to checkpoint.
For the network-filesystem WAL profile it performs and verifies the truncating
checkpoint through a connection that also uses exclusive locking.
Do not copy a live database or copy a leftover journal independently.

The network profile reduces concurrency to make one-process access predictable;
it does not make SQLite a distributed database. Do not run multiple application
replicas against the same database file unless the filesystem and operational
model provide SQLite's required locking and consistency semantics.
