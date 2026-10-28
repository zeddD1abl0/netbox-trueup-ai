---
name: db-change
description: Change TrueUp's database - a table, a column, a query or what store.Store offers. Adds a goose migration and sqlc queries for each engine, regenerates the generated code, extends the shared storage tests and runs them. Use for any change under internal/store or to sqlc.yaml.
---

# Changing the database

TrueUp keeps its state in SQLite or PostgreSQL ([ADR 0011](../../../docs/adr/0011-store-state-with-sqlc-and-goose.md), [ADR 0021](../../../docs/adr/0021-forward-only-migrations-when-opening.md), [ADR 0022](../../../docs/adr/0022-postgresql-schema-and-advisory-locks.md)). Each engine has its own goose migrations and sqlc queries behind one interface, `store.Store`, and one test suite, `storetest`, runs against both. Every change covers both engines in the same pull request: each step below has a part for each.

## Where things are

| Path | What it holds |
| --- | --- |
| `internal/store/store.go` | `store.Store`, the record types, `Fetch.Prepare` and the errors. Engine-neutral rules live here. |
| `internal/store/storetest/` | The tests every engine must pass. |
| `internal/store/engine/` | Chooses the engine from `database.url`. |
| `internal/store/sqlite/migrations/`, `internal/store/postgres/migrations/` | Each engine's goose migrations, embedded in the binary. |
| `internal/store/sqlite/queries/`, `internal/store/postgres/queries/` | Each engine's queries, which sqlc compiles. |
| `internal/store/sqlite/sqlitedb/`, `internal/store/postgres/postgresdb/` | sqlc's output. Generated: never edit it. A hook blocks edits. |
| `internal/store/sqlite/*.go`, `internal/store/postgres/*.go` | Each engine's store: connections, settings, locks, migrations, and the methods on top of the generated queries. |
| `internal/store/postgres/schema.sql` | Tells sqlc about the schema `netbox_trueup_ai`, which TrueUp creates before migrating. Nothing applies it. |
| `internal/store/postgres/pgtest/` | Starts the PostgreSQL that the tests use, and gives each test a database of its own. |
| `sqlc.yaml` | One entry per engine. Its `out:` lines also tell `make generate` and the hook which packages are generated. |

## Steps

1. **Decide what `store.Store` needs.** Add or change methods and record types in `internal/store/store.go`. Keep them engine-neutral: no SQL types, no `sql.Null*` or `pgtype`. Validation and normalization that every engine needs go there too, as `Fetch.Prepare` does, so the engines can't drift. Text that PostgreSQL's `text` can't hold, such as NUL bytes or invalid UTF-8, is refused or replaced there, or stored as bytes.

2. **Add a migration** to each engine, with the same number and name: `NNNNN_what.sql`, numbered one past the newest, five digits.
   - Start with `-- +goose Up`. Migrations only go forward: write no `-- +goose Down` section.
   - Never change a migration that a release has shipped, since databases out there have applied it. Add another.
   - Foreign keys don't cascade: declare `REFERENCES parent (key)` with no `ON DELETE` clause, and delete child rows before their parent, as `RemoveSources` does. Give each key an index whose leading column is the key, or rely on a primary key that starts with it.
   - Test that a migration keeps the data: fill a database at the version before, with the shared suite's records or an engine test's, migrate it, and compare what it holds.
   - *SQLite:* use `STRICT` tables. Store times as `TEXT` in the layout `2006-01-02T15:04:05.000000Z` (UTC, microseconds, fixed width), with the `GLOB` check `00001_sources.sql` uses. Store text that need not be UTF-8, such as an unsafe path, as `BLOB`.
   - *SQLite:* it can alter a table in place only to add, rename or drop a column (`ALTER TABLE`); prefer that. For anything else, follow SQLite's [twelve steps](https://www.sqlite.org/lang_altertable.html#otheralter) inside goose's transaction, with `PRAGMA defer_foreign_keys = ON` first. Foreign keys can't be turned off inside a transaction, but deferred, the rows that refer to a table being rebuilt break their keys only until the new table takes its name. That works because keys don't cascade: with `ON DELETE CASCADE`, dropping the old table would delete every row that refers to it, and no check would notice.
   - *SQLite:* don't turn foreign keys off in a migration (`-- +goose NO TRANSACTION` with `PRAGMA foreign_keys = OFF`). TrueUp checks every key whenever it opens the database, and refuses one with rows that break them.
   - *PostgreSQL:* use `timestamptz` for times, `bigint` for integers, `bytea` for text that need not be UTF-8, and `text COLLATE "C"` for every column that is sorted, and for the columns that refer to it, so that it sorts by bytes as SQLite does, whatever the database's locale. The storage tests run in databases that sort by ICU's English rules, so a missing `COLLATE "C"` fails them.
   - *PostgreSQL:* name tables without a schema. Every connection's `search_path` is `netbox_trueup_ai` alone, so they are created there. PostgreSQL alters tables in place and its DDL is transactional, so a migration rolls back whole.
   - *PostgreSQL:* a statement that holds a semicolon, such as a `DO` block, needs `-- +goose StatementBegin` and `-- +goose StatementEnd` around it.

3. **Write the queries** in each engine's `queries/*.sql`, each starting `-- name: Name :one`, `:many`, `:exec`, `:execrows`, or for PostgreSQL `:copyfrom`, which writes many rows in one `COPY`, as for file lists.
   - Values only through parameters: `?` for SQLite, `$1` for PostgreSQL. Never build SQL from strings.
   - Comment lines right after the `-- name:` line become the method's doc comment. Don't put a comment at the top of a query file: sqlc attaches it to the first query.
   - Sort with `ORDER BY` in SQL, by bytes. The storage tests check the order.
   - Give both engines' queries the same names, so that their stores read alike.

4. **Regenerate:** `make generate`, then `make generate-check`. Commit the generated files with the SQL they come from. CI runs `make generate-check` in the "Lint and scan" job and fails if they differ. sqlc is pinned in `mise.toml`, and the generated files name its version, so use the pinned one.

5. **Implement the methods** in each engine's store, on top of the generated queries:
   - Writes that change more than one row run in one transaction (`inTx` in each store), so that a crash or a failure leaves all of them or none.
   - Reads that must agree with each other run in one read-only transaction: on PostgreSQL at `REPEATABLE READ`, since at its default level each query sees what was committed when it began. `Snapshot` shows both.
   - Think through two processes at once. SQLite runs one write transaction at a time, and its write transactions take the write lock as they begin, so they queue rather than fail. PostgreSQL runs them side by side, locking rows: lock what a transaction reads and then changes, as `ListSourceNames` does with `FOR UPDATE`, so that a write meanwhile either finishes first or waits. Every process holds a shared lock while it has the database open, so the schema doesn't change under it. Whatever must happen in order, such as two fetches of one source, needs a lock of its own held until it is recorded, as `source.LockCache` is: times from the clock don't order writes, since clocks go back.
   - Wrap errors with what was being done and which record, as the existing methods do. Map `sql.ErrNoRows` (SQLite) and `pgx.ErrNoRows` (PostgreSQL) to `store.ErrNotFound`. On PostgreSQL, pass connection errors through `withoutUser`, since pgx's name the user.

6. **Test it.**
   - Add what `store.Store` promises to `storetest`: a function in the table in `Run`. Both engines run it, so test behaviour, not SQL. Promises about concurrency belong there too, as `testReadsAgree` and `testRemoveWhileRecording` show.
   - Add what only one engine does, such as its files, locks or schema, to that engine's own tests: `sqlite_test.go`, or `postgres_integration_test.go`.
   - Run `make test`, which runs the suite on SQLite, and `make test-integration`, which runs it on PostgreSQL 16 in a container too (it needs Docker). To run only the store's tests on PostgreSQL: `go test -race -tags integration ./internal/store/...`.
   - Run concurrent tests many times, with `-count` and the race detector, before trusting them.

7. **Document it.** If the change stores a new kind of data, update the data table in [docs/architecture.md](../../../docs/architecture.md#storage). If people or scripts can see it, update [docs/configuration.md](../../../docs/configuration.md). Say in the pull request that the release adds a migration: once a database is migrated, older releases refuse it.
