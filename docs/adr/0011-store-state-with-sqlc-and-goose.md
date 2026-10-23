---
status: accepted
date: 2026-10-23
decision-makers: "@zeddD1abl0"
---

# Store state in SQLite or PostgreSQL through sqlc and goose

## Context and problem

TrueUp keeps state:

- sources and snapshots, and comparisons;
- links, plans and sync history;
- overrides and settings;
- users, sessions and jobs.

The README promises a single node on SQLite and a highly available cluster on PostgreSQL. [tooling.md §3.3](../tooling.md#33-storage) lists modernc.org/sqlite, pgx, sqlc and goose. This ADR records how they fit together.

## Decision drivers

- Both engines from one codebase.
- Pure Go, with no CGO, so cross-compiling stays simple.
- SQL that is explicit and visible in review.
- Migrations that run safely when several replicas start at once.

## Considered options

1. Both engines, with a sqlc query set and goose migrations for each
2. PostgreSQL only
3. SQLite only, with replication for availability
4. An ORM across both engines, such as GORM or ent

## Decision

Chosen option: "Both engines, with a sqlc query set and goose migrations for each".

- SQLite through modernc.org/sqlite, and PostgreSQL through pgx.
- Each engine has its own migrations (goose, embedded in the binary) and query files (sqlc), behind one Go interface. A schema change covers both engines in the same pull request.
- One test suite runs against both engines.
- Migrations are applied at startup under a lock (a PostgreSQL advisory lock), or by the `migrate` command. A binary refuses to run against a schema newer than it knows.
- SQLite runs in WAL mode, with foreign keys enforced and a busy timeout. It supports a single replica only.
- Generated code is committed, and CI checks that it matches its sources.
- Timestamps are stored in UTC. A feature specific to one engine is used only if the other has an equivalent.

### Consequences

- Good, because there is one binary, no CGO, and SQLite needs no setup.
- Good, because the SQL is visible in review.
- Bad, because every query is written twice. The `db-change` project skill and the shared tests keep the two in step.
- Bad, because a SQLite deployment can't scale out. Moving one to PostgreSQL will need an export and import path.

## Pros and cons of the options

### PostgreSQL only

- Good, because there is one query set.
- Bad, because even the smallest deployment would need a database server.

### SQLite only, with replication

- Good, because it is the simplest to run.
- Bad, because SQLite replication tools provide read replicas or failover, not several writers, so replicas couldn't all serve the UI.

### An ORM across both engines

- Good, because there is one model for both engines.
- Bad, because it hides the SQL and relies on reflection or heavy code generation ([tooling.md §9](../tooling.md#9-considered-and-not-chosen)).
