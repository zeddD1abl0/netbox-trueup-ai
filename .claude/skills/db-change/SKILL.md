---
name: db-change
description: Change TrueUp's database - a table, a column, a query or what store.Store offers. Adds a goose migration and sqlc queries for each engine, regenerates the generated code, extends the shared storage tests and runs them. Use for any change under internal/store or to sqlc.yaml.
---

# Changing the database

TrueUp keeps its state in SQLite or PostgreSQL ([ADR 0011](../../../docs/adr/0011-store-state-with-sqlc-and-goose.md), [ADR 0021](../../../docs/adr/0021-forward-only-migrations-when-opening.md)). Each engine has its own goose migrations and sqlc queries behind one interface, `store.Store`, and one test suite, `storetest`, runs against every engine. A change covers every engine in the same pull request.

**PostgreSQL arrives in roadmap step 7.** Until then only SQLite exists: do the SQLite half below, and skip the steps marked *PostgreSQL*. Once `internal/store/postgres` exists, those steps are required.

## Where things are

| Path | What it holds |
| --- | --- |
| `internal/store/store.go` | `store.Store`, the record types, `Fetch.Prepare` and the errors. Engine-neutral rules live here. |
| `internal/store/storetest/` | The tests every engine must pass. |
| `internal/store/engine/` | Chooses the engine from `database.url`. |
| `internal/store/sqlite/migrations/` | SQLite's goose migrations, embedded in the binary. |
| `internal/store/sqlite/queries/` | SQLite's queries, which sqlc compiles. |
| `internal/store/sqlite/sqlitedb/` | sqlc's output. Generated: never edit it. A hook blocks edits. |
| `internal/store/sqlite/*.go` | The SQLite store: file, settings, lock, migrations, and the methods on top of the generated queries. |
| `internal/store/postgres/...` | The same for PostgreSQL, from step 7. |
| `sqlc.yaml` | One entry per engine. Its `out:` lines also tell `make generate` and the hook which packages are generated. |

## Steps

1. **Decide what `store.Store` needs.** Add or change methods and record types in `internal/store/store.go`. Keep them engine-neutral: no SQL types, no `sql.Null*`. Validation and normalization that every engine needs go there too, as `Fetch.Prepare` does, so the engines can't drift.

2. **Add a migration** for SQLite: `internal/store/sqlite/migrations/NNNNN_what.sql`, numbered one past the newest, five digits.
   - Start with `-- +goose Up`. Migrations only go forward: write no `-- +goose Down` section.
   - Never change a migration that a release has shipped, since databases out there have applied it. Add another.
   - Use `STRICT` tables. Store times as `TEXT` in the layout `2006-01-02T15:04:05.000000Z` (UTC, microseconds, fixed width), with the `GLOB` check `00001_sources.sql` uses. Store text that need not be UTF-8, such as an unsafe path, as `BLOB`.
   - Foreign keys are enforced, and don't cascade: declare `REFERENCES parent (key)` with no `ON DELETE` clause, and delete child rows before their parent, as `RemoveSources` does. Give each key an index whose leading column is the key, or rely on a primary key that starts with it.
   - SQLite can alter a table in place only to add, rename or drop a column (`ALTER TABLE`); prefer that. For anything else, follow SQLite's [twelve steps](https://www.sqlite.org/lang_altertable.html#otheralter) inside goose's transaction, with `PRAGMA defer_foreign_keys = ON` first. Foreign keys can't be turned off inside a transaction, but deferred, the rows that refer to a table being rebuilt break their keys only until the new table takes its name. That works because keys don't cascade: with `ON DELETE CASCADE`, dropping the old table would delete every row that refers to it, and no check would notice.
   - Don't turn foreign keys off in a migration (`-- +goose NO TRANSACTION` with `PRAGMA foreign_keys = OFF`). TrueUp checks every key whenever it opens the database, and refuses one with rows that break them.
   - Test that a migration keeps the data: fill a database at the version before, with the shared suite's records or an engine test's, migrate it, and compare what it holds.
   - *PostgreSQL:* add the migration with the same number and name to `internal/store/postgres/migrations/`, in PostgreSQL's types: `timestamptz`, `bytea`, `bigint`, and `text COLLATE "C"` for any column that is sorted, so that it sorts by bytes as SQLite does.

3. **Write the queries** in the engine's `queries/*.sql`, each starting `-- name: Name :one`, `:many`, `:exec` or `:execrows`.
   - Values only through parameters: `?` for SQLite, `$1` for PostgreSQL. Never build SQL from strings.
   - Comment lines right after the `-- name:` line become the method's doc comment. Don't put a comment at the top of a query file: sqlc attaches it to the first query.
   - Sort with `ORDER BY` in SQL, by bytes. The storage tests check the order.
   - *PostgreSQL:* write the same queries for PostgreSQL.

4. **Regenerate:** `make generate`, then `make generate-check`. Commit the generated files with the SQL they come from. CI runs `make generate-check` in the "Lint and scan" job and fails if they differ. sqlc is pinned in `mise.toml`, and the generated files name its version, so use the pinned one.

5. **Implement the methods** in the engine's store, on top of the generated queries:
   - Writes that change more than one row run in one transaction (`inTx` in the SQLite store), so that a crash or a failure leaves all of them or none. SQLite's write transactions take the write lock as they begin, so they queue rather than fail.
   - Reads that must agree with each other run in one read-only transaction.
   - Wrap errors with what was being done and which record, as the existing methods do. Map `sql.ErrNoRows` to `store.ErrNotFound`.
   - Think through two processes at once. Every process holds a shared lock while it has the database open, so the schema doesn't change under it. Whatever must happen in order, such as two fetches of one source, needs a lock of its own held until it is recorded, as `source.LockCache` is: times from the clock don't order writes, since clocks go back.

6. **Test it.**
   - Add what `store.Store` promises to `storetest`: a function in the table in `Run`. Both engines run it, so test behaviour, not SQL.
   - Add what only one engine does, such as its files or locks, to that engine's own tests.
   - Run `make test`. *PostgreSQL:* run `make test-integration` too, which runs the suite against PostgreSQL in a container.

7. **Document it.** If the change stores a new kind of data, update the data table in [docs/architecture.md](../../../docs/architecture.md#storage). If people or scripts can see it, update [docs/configuration.md](../../../docs/configuration.md). Say in the pull request that the release adds a migration: once a database is migrated, older releases refuse it.
