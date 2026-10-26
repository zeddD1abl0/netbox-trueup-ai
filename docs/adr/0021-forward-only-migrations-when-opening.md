---
status: accepted
date: 2026-10-26
decision-makers: "@zeddD1abl0"
---

# Migrate the database forward only, when a command opens it

## Context and problem

[ADR 0011](0011-store-state-with-sqlc-and-goose.md) keeps TrueUp's state in SQLite or PostgreSQL, with goose migrations "applied at startup under a lock (a PostgreSQL advisory lock), or by the `migrate` command", and a binary that refuses a schema newer than it knows. Building the store in roadmap step 6 left questions that ADR doesn't answer:

- Which commands migrate the database, and what does a command do with an older schema?
- Can a migration be undone?
- What is the lock on SQLite? goose ships lockers for PostgreSQL and MySQL only.
- How is the engine chosen without the rest of TrueUp knowing which is in use, and where does the SQLite file live?

Several processes can open one database at once: a scheduled `source fetch` and a person running another command, and later replicas sharing PostgreSQL.

## Decision drivers

- One binary that works without a separate setup step.
- Safe when processes open the database at once, and when one of them crashes.
- No code path that destroys data, and none that is never tested.
- Nothing outside the store depends on the engine.

## Considered options

When migrations run:

1. Commands that use the database migrate it when they open it; `migrate` does the same as a step of its own
2. Only `migrate` migrates; other commands refuse an older schema
3. As the first, with a setting to turn migrating on open off

Undoing migrations:

1. Forward only
2. A Down section in every migration, and `migrate --to N`

The lock on SQLite:

1. An operating system lock on a file beside the database
2. A lease row in the database, with an expiry and a heartbeat, like goose's PostgreSQL table locker

## Decision

Chosen options: "commands that use the database migrate it when they open it", "forward only" and "an operating system lock on a file beside the database".

**Opening.** A command that uses the database opens it through `internal/store/engine`, which:

1. creates the database if it doesn't exist, and refuses, before changing anything, a file that holds another application's database;
2. takes the engine's shared lock, which it holds for as long as it has the database open, waiting up to 2 minutes for a migration to finish;
3. reads the schema's version, and refuses a schema newer than this release knows, exiting with 1;
4. if the schema is older, trades the shared lock for the exclusive one, which waits up to 2 minutes for other processes to close the database, unless one of them migrates it meanwhile; applies the migrations it lacks, each in its own transaction, through connections of its own, logging each one; and takes the shared lock again, reading the version afresh;
5. on SQLite, checks that no row breaks a foreign key, on every open.

`source fetch` opens the database now, and `list` and `compare` will. `version`, `config show` and `netbox check` never open it. `migrate` does the same as any command and reports the engine, where the database is, the version before and after, and the migrations applied, as text or JSON (ADR 0018). It exits with 0 whether it applied migrations or found none to apply, 1 when it fails, and 3 for configuration.

**Forward only.** Migrations have no Down sections, and nothing migrates down. Going back to an older release means restoring a backup taken before the upgrade, since the older release refuses the migrated database. A migration a release has shipped is never changed.

**Locks.** On SQLite, the lock is on `<database file>-lock`: shared while a process has the database open, exclusive while one migrates it, through `flock` on Unix and `LockFileEx` on Windows (`internal/filelock`, on `golang.org/x/sys`). A lock belongs to the open file, so it excludes other processes and other opens in the same one, and the operating system releases it when the process ends, however it ends. So a migration never runs under a process that read the schema before it, and that process can't write into a schema it doesn't know. On PostgreSQL, from step 7, the same is done with advisory locks.

**Engines.** `store.Store` in `internal/store` is the interface the rest of TrueUp uses. Each engine is a package below it, `internal/store/sqlite` and later `internal/store/postgres`, with its migrations, its queries and sqlc's output. `internal/store/engine` alone knows the engines, and chooses one from `database.url`. `internal/store/storetest` holds the tests every engine runs. A `postgres` or `postgresql` URL is a configuration error, exit code 3, until step 7 adds PostgreSQL.

**SQLite.** The database is `<data_dir>/netbox-trueup-ai.db`, unless `database.url` names a file by its absolute path, as in `sqlite:///srv/trueup/trueup.db`; a `sqlite:` URL can't carry a host, user, query or fragment. TrueUp creates the file, and its directory, readable by its owner only, and SQLite gives the `-wal` and `-shm` files the same mode. On Windows, which ignores those modes, the file takes its directory's permissions for now. Every connection runs in WAL mode, enforces foreign keys, waits up to 30 seconds for a lock, syncs each commit (`synchronous=FULL`), runs in SQLite's defensive mode, and takes the write lock when a write transaction begins. Times are stored as UTC text to the microsecond, at a fixed width, so that they sort as text and match what PostgreSQL keeps.

### Consequences

- Good, because installing a new release is the whole upgrade: the first command that needs the database migrates it, and processes that start at once take turns.
- Bad, because a migration waits for every process that has the database open. A long `source fetch` can hold up a new release's first command for its 2 minutes, which then fails and says why, and a long-running server will have to stop before a migration.
- Good, because a crashed process never leaves the SQLite lock held, and a failed migration rolls back, leaving the schema at the last one that succeeded.
- Good, because there is no code to undo migrations, so none goes untested.
- Bad, because an upgrade changes the database the first time any command uses it. Teams that want that to be a deliberate step run `migrate` themselves, after a backup; a setting to stop migrating on open can be added if they need more.
- Bad, because going back to an older release needs a backup from before the upgrade.
- Bad, because the SQLite lock is one more file beside the database. Like WAL mode, it doesn't work across a network file system, where the database doesn't belong.
- Bad, because syncing every commit costs time. TrueUp writes little: recording a fetch of 20,000 files, more than devicetype-library holds, takes about half a second on an SSD.

## Pros and cons of the options

### Only `migrate` migrates

- Good, because the database changes only when someone asks.
- Bad, because every upgrade, and every new installation, needs a step that is easy to forget, and commands fail until it runs.

### A setting to turn migrating on open off

- Good, because teams with change control get a deliberate step.
- Bad, because it is a setting nobody needs yet. It can be added without breaking anything.

### Down migrations

- Good, because a release can be rolled back without a backup.
- Bad, because each migration's Down needs tests on both engines, and a Down that drops a column destroys what was stored since. A backup restores a known state.

### A lease row in the database

- Good, because it needs no extra file and no code for each platform.
- Bad, because a process that crashes holds the lock until its lease runs out, and a long migration depends on its heartbeat staying alive.

## More information

- goose's providers apply migrations on the connection a session locker holds (`initialize` in goose v3.29.0's `provider_run.go`), so a lock on SQLite can't be a transaction on that connection.
- goose applies nothing to a database newer than its migrations, and reports no error (`UpVersions` in `internal/gooseutil/resolve.go`), which is why TrueUp checks the version itself.
