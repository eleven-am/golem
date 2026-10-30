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

Before copying the database file, stop all application readers and writers,
close the verified handle, and call `sqlite.CheckpointForBackup`. For the
default WAL profile this performs and verifies a truncating checkpoint. For the
network-filesystem profile it verifies rollback-journal mode and returns: a
closed, committed rollback-journal database has no WAL frames to checkpoint.
Do not copy a live database or copy a leftover journal independently.

The network profile reduces concurrency to make one-process access predictable;
it does not make SQLite a distributed database. Do not run multiple application
replicas against the same database file unless the filesystem and operational
model provide SQLite's required locking and consistency semantics.
