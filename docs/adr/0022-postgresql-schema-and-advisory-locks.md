---
status: accepted
date: 2026-10-26
decision-makers: "@zeddD1abl0"
---

# Keep TrueUp's PostgreSQL tables in a schema of their own, and order processes with advisory locks

## Context and problem

[ADR 0011](0011-store-state-with-sqlc-and-goose.md) keeps TrueUp's state in SQLite or PostgreSQL, and [ADR 0021](0021-forward-only-migrations-when-opening.md) migrates the database when a command opens it, under a shared lock that each open store holds, an exclusive lock that a migration takes, and a gate. On SQLite those are file locks, and the file's `application_id` marks it as TrueUp's. Roadmap step 7 adds PostgreSQL, which needs answers to questions the SQLite engine didn't raise:

- How does TrueUp recognise its own database, when PostgreSQL has no `application_id`, and where do its tables go in a database that may hold others?
- Which advisory locks stand for the shared lock, the exclusive lock and the gate, and what happens when the connection that holds one is lost?
- How is the connection secured, and which PostgreSQL releases are supported?

## Decision drivers

- TrueUp never changes another application's tables, nor uses them by mistake.
- The schema never changes under a running process, as on SQLite, even when a connection drops.
- A process that crashes, or loses its server, never leaves a lock held for good.
- Connections work as libpq's documentation says, so that database administrators need nothing TrueUp-specific.

## Considered options

Where the tables go, and how TrueUp knows them:

1. A schema of its own, `netbox_trueup_ai`, marked with a comment
2. The schema the connection's `search_path` picks, with a marker table
3. A schema of its own whose name is a setting

The locks:

1. Advisory locks, on a connection of the store's own, and on each pooled connection
2. Advisory locks on the store's own connection alone
3. A lease row with an expiry and a heartbeat

TLS:

1. libpq's default, `sslmode=prefer`, and libpq's parameters in `database.url`
2. `verify-full` unless the URL says otherwise
3. Always `verify-full`, with a `database.ca_bundle` setting, as for NetBox

## Decision

Chosen options: "a schema of its own, `netbox_trueup_ai`", "advisory locks, on a connection of the store's own, and on each pooled connection", and "libpq's default".

**The schema.** TrueUp keeps every table, goose's included, in the schema `netbox_trueup_ai`. A migration creates it if it doesn't exist, which needs the CREATE privilege on the database, or a database administrator creates it beforehand, owned by TrueUp's role. Every connection sets its `search_path` to that schema alone as it connects, rather than passing it as a startup parameter, which some poolers drop. So a `search_path` from the URL's options, the role's settings or a pooler can't send TrueUp's tables elsewhere, and objects in `public`, which others may be able to create, can't stand in for TrueUp's. The first migration sets the schema's comment to `netbox-trueup-ai`. Before it changes anything, opening refuses a schema of that name that another role owns, that another role may create objects in, or that holds a relation, function, type or operator another role owns, since such a role could have TrueUp run its code: a function of its own that matches one of TrueUp's calls better than the catalog's does would be the one called. It reads the catalog for that on the connection that holds the locks, whose `search_path` is `pg_catalog` alone, and TrueUp's own lock and setting calls name their functions with their schema. The queries that run once the schema is checked, sqlc's and goose's, rely on that check: the schema then holds only what TrueUp's role put there. Roles that can act as TrueUp's, its members and superusers, can still create objects in it, but gain nothing by it. It also refuses a schema that holds tables or views without the mark. A missing schema, or one with no tables but goose's, is new: one a database administrator made, or one a process left before its first migration finished. Opening also refuses a database whose encoding isn't UTF-8.

**The locks** are session advisory locks with two keys, the first 1414681936 (`0x54525550`, "TRUP", SQLite's `application_id`), which PostgreSQL keeps for each database:

| Second key | Lock | Held |
| --- | --- | --- |
| 1 | the lock | shared by each open store, on a connection of its own, for as long as it is open; exclusively by a migration |
| 2 | the gate | by a migration from before it waits for the lock until it is done; by each store for a moment as it opens |
| 3 | connections | shared by each of a store's pooled connections; exclusively by a migration once it holds the lock |
| 4, in PostgreSQL's form with one key | goose's | by goose, on the connection a migration runs on, before it reads which migrations are applied; shared by each store while it reads the schema's version |

A wait for a lock is a wait in PostgreSQL's queue for it, under `lock_timeout`, so that waiters get the lock in turn and the server ends the wait itself after 2 minutes; a `statement_timeout` set for the role or the database doesn't end it sooner, nor, from PostgreSQL 17, a `transaction_timeout`. When a command is interrupted, pgx asks the server to cancel the wait, and the command waits for that request to go before it exits, so that the wait doesn't keep its place in the queue, holding the gate. Each pooled connection also checks, as it connects, that the schema is at the version the store opened. TrueUp turns `idle_session_timeout` off for its sessions, since the connection that holds a store's lock is idle while the store is open.

The fourth key covers a migration whose process loses the connection that holds the other locks: goose's own connection, and its transaction, go on, so another process could otherwise pass the gate, read the version from before the migration, and apply it again, or open the database at that version, an older release among them. goose takes the key, through TrueUp's locker, on the connection it migrates through, and reads which migrations are applied only then; once it has the key, the locker checks that a process still holds the lock exclusively, since the migrating process may have lost it before goose began. A store reads the schema's version holding the key shared, waiting for a migration that holds it, and each pooled connection takes it shared to check the version, refusing to wait. A migration's statements wait up to 2 minutes for a table that another session holds, such as one `pg_dump` reads, and then fail, saying so.

The third key covers a store whose own connection is lost, through a network failure or `pg_terminate_backend`: its pooled connections still hold key 3, so a migration waits for them, and any connection the store opens after that fails. Pooled connections can't hold key 1 instead. PostgreSQL queues a request behind any waiting request it conflicts with, so while a migration waits for key 1, a running store's new connection would wait behind it, and the migration waits for that same store.

A session's advisory locks belong to its server connection. TrueUp therefore supports a pooler such as PgBouncer only in session mode. Transaction and statement modes hand server connections from one client to the next, which would leave TrueUp's locks with whichever client comes next. They are documented as unsupported; TrueUp can't tell reliably that it is behind one.

**TLS and connections.** `database.url` is read as pgx reads it, with libpq's parameters and environment variables. TLS is as `sslmode` says, by default `prefer`, which encrypts the connection when the server offers TLS but verifies nothing; `verify-full`, with `sslrootcert` for a private CA, verifies the server. TrueUp sets two defaults of its own: `connect_timeout` 10 seconds and `application_name` `netbox-trueup-ai`, unless the URL or the environment sets them. pgxpool's defaults size the pool, and its URL parameters, such as `pool_max_conns`, change them. There are no new settings.

**Releases.** TrueUp supports PostgreSQL 15 and later, as NetBox 4.7 does, so that a site can use the server its NetBox uses. It warns about older releases rather than refusing them. The storage tests run on PostgreSQL 16 for now. Other releases join them once there is a working system to test.

### Consequences

- Good, because TrueUp can share a server, or even a database, with other applications without touching their tables, a `search_path` set elsewhere changes nothing, and no other role can put code where TrueUp would run it. A database of TrueUp's own is still the simplest: configuration.md recommends it.
- Good, because the schema never changes under an open store, even one whose own connection drops, and a store finds out if a migration ran without it.
- Good, because a process that crashes releases its locks with its sessions, and an interrupted wait leaves nothing in the queue.
- Bad, because a session whose client host vanishes keeps its locks until PostgreSQL notices, which with the operating system's TCP keepalive defaults can take hours. A migration then waits its 2 minutes and fails, saying other processes have the database open, and a database administrator can end the session with `pg_terminate_backend`.
- Bad, because transaction pooling isn't supported, and nothing detects it.
- Bad, because PostgreSQL's own messages, such as a failed login's, name the role. TrueUp prints neither the URL nor the password, and its locations name no user.
- Bad, because, by default, the connection isn't verified: anyone on the network path can read or change what crosses it unless the URL asks for `verify-full`. This follows libpq, as the maintainer chose. configuration.md recommends `verify-full`.
- Bad, because TrueUp needs the CREATE privilege on the database for its first migration, unless the schema is made for it.

## Pros and cons of the options

### The schema the connection's search_path picks, with a marker table

- Good, because there is no schema to create.
- Bad, because TrueUp's tables sit beside whatever else is there, and a `search_path` that changes moves them.

### A schema of its own whose name is a setting

- Good, because several TrueUp deployments could share one database.
- Bad, because it is a setting nobody needs yet, and the lock keys would have to come from the name.

### Advisory locks on the store's own connection alone

- Good, because it is what ADR 0021 describes, with one connection to hold.
- Bad, because a store whose connection drops keeps working through its pool while a migration runs.

### A lease row

- Good, because it works through any pooler.
- Bad, because a process that crashes holds the lock until its lease runs out, and a long migration depends on its heartbeat. ADR 0021 rejected it for SQLite too.

### Always verify-full

- Good, because it is what NetBox's client does, and protects the connection against interception by default.
- Bad, because it departs from libpq's behaviour, which database administrators expect, and fails against a server without a certificate until the URL turns it off.

## More information

- The lock queue: `LockAcquireExtended` in PostgreSQL's `src/backend/storage/lmgr/lock.c` treats a request that conflicts with a waiting one as a conflict, so `pg_try_advisory_lock_shared` fails while an exclusive request waits.
- `CREATE SCHEMA IF NOT EXISTS` checks the CREATE privilege on the database before it looks for the schema, so TrueUp looks first.
- pgx v5.11.0's `ParseConfig` defaults `sslmode` to `prefer` (`configTLS` in `pgconn/config.go`), and its connection errors name the user, so TrueUp prints neither the URL nor pgx's prefix.
- When a context ends during a query, pgx closes the connection in the background (`asyncClose` in `pgconn/pgconn.go`), sending the cancel request from a goroutine that a process exiting at once would end; TrueUp waits for `CleanupDone`.
- goose v3.29.0's providers take a session locker's lock on the connection they migrate through, before they read the applied versions (`initialize` in `provider_run.go`). TrueUp gives goose a locker of its own, which doesn't wait, and checks the exclusive lock.
