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
when one process owns the database and you want WAL's cheaper commits and
readers that do not block the single writer inside that process. Choose
`sqlite.OpenNetworkFilesystem` when anything else must open the file while the
application runs.

A rollback-journal database opened with this profile is converted to WAL. The
reverse works when the filesystem supports ordinary WAL: after a clean close,
`sqlite.OpenNetworkFilesystem` converts the database back to rollback
journaling. On a filesystem that rejects the `-shm` file, any connection
without exclusive locking cannot read a WAL database at all, so convert it back
first from a single connection that runs `PRAGMA locking_mode=EXCLUSIVE;` and
then `PRAGMA journal_mode=DELETE;`.

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
