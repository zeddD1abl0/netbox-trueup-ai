package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
)

// migrationFiles are the schema's migrations, applied in order of their
// numbers. Never change one that a release has shipped: add another.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// The advisory locks that put processes in order, each named by two keys:
// lockClass, "TRUP" as in SQLite's application ID, and a second that says
// which lock. Advisory locks belong to a database, so these never meet
// another database's.
//
//   - lockKey is held shared by each store's own connection while it is open,
//     and exclusively by a migration, so that the schema never changes under
//     a store that has read it;
//   - gateKey is held by a migration from before it waits for lockKey until
//     it is done, and by each store for a moment as it opens, so that stores
//     that open meanwhile don't keep a migration waiting for ever;
//   - connKey is held shared by each pooled connection, and exclusively by a
//     migration once it holds lockKey, so that a migration also waits for
//     the connections of a store whose own connection was lost;
//   - gooseKey, in PostgreSQL's form with one key, is held by goose, through
//     gooseLocker, on the connection it migrates through, before it reads
//     which migrations are applied, and shared by a store for as long as it
//     reads the schema's version. If the connection that holds a migrating
//     process's other locks breaks while a migration runs, goose's own
//     connection, and its transaction, go on: gooseKey keeps another process
//     from applying the migration again, and a store from reading the
//     version from before it.
//
// A store's pooled connections can't hold lockKey themselves: PostgreSQL
// queues a request behind any waiting one it conflicts with, so a store's
// new connection would wait for a migration that waits for the store.
// Nothing waits for connKey's shared mode, and a migration asks for its
// exclusive mode only once no open store holds lockKey.
const (
	lockClass int32 = 0x54525550
	lockKey   int32 = 1
	gateKey   int32 = 2
	connKey   int32 = 3
	gooseKey        = int64(lockClass)<<32 | 4
)

// lockTimeout is the longest to wait for a lock: for a migration to finish,
// before opening the database, or for other processes to close it, before
// migrating it.
const lockTimeout = 2 * time.Minute

// noticeAfter is how long a wait for a lock lasts before it is logged, so
// that a moment's wait, as when processes open the database at once, isn't.
// internal/filelock waits as long on SQLite.
const noticeAfter = time.Second

// schemaMark is the comment that marks the schema as TrueUp's. The first
// migration sets it.
const schemaMark = "netbox-trueup-ai"

// advisory is one of the locks, in one mode: its keys, and the queries that
// take it and release it.
type advisory struct {
	keys              []any
	try, wait, unlock string
}

// The lock functions are named with their schema, and their keys cast, so
// that nothing of the same name in a search_path can stand in for them.
func shared(key int32) advisory {
	return advisory{
		[]any{lockClass, key},
		"SELECT pg_catalog.pg_try_advisory_lock_shared($1::integer, $2::integer)",
		"SELECT pg_catalog.pg_advisory_lock_shared($1::integer, $2::integer)",
		"SELECT pg_catalog.pg_advisory_unlock_shared($1::integer, $2::integer)",
	}
}

func exclusive(key int32) advisory {
	return advisory{
		[]any{lockClass, key},
		"SELECT pg_catalog.pg_try_advisory_lock($1::integer, $2::integer)",
		"SELECT pg_catalog.pg_advisory_lock($1::integer, $2::integer)",
		"SELECT pg_catalog.pg_advisory_unlock($1::integer, $2::integer)",
	}
}

// gooseExclusive is gooseKey, as goose holds it, and gooseShared as a store
// holds it to read the schema's version.
var gooseExclusive = advisory{
	[]any{gooseKey},
	"SELECT pg_catalog.pg_try_advisory_lock($1::bigint)",
	"SELECT pg_catalog.pg_advisory_lock($1::bigint)",
	"SELECT pg_catalog.pg_advisory_unlock($1::bigint)",
}

var gooseShared = advisory{
	[]any{gooseKey},
	"SELECT pg_catalog.pg_try_advisory_lock_shared($1::bigint)",
	"SELECT pg_catalog.pg_advisory_lock_shared($1::bigint)",
	"SELECT pg_catalog.pg_advisory_unlock_shared($1::bigint)",
}

// errTimeout means a lock wasn't released within the wait allowed.
var errTimeout = errors.New("timed out waiting for the database's lock")

// heldError means another process held one of the database's locks for
// longer than the wait allowed. It says what in its own words, and wraps
// errTimeout.
type heldError struct{ msg string }

func (e *heldError) Error() string { return e.msg }
func (e *heldError) Unwrap() error { return errTimeout }

// acquire takes the lock on conn, for its session, waiting up to timeout for
// other sessions to release it, and calling notice, if not nil, once it has
// waited for noticeAfter. notice can run on another goroutine. It waits in
// PostgreSQL's queue for the lock, so that waiters get it in turn, and
// lock_timeout ends the wait on the server too, even if this process goes
// away without a word while it waits.
func (l advisory) acquire(ctx context.Context, conn *pgx.Conn, timeout time.Duration, notice func()) error {
	var ok bool
	if err := conn.QueryRow(ctx, l.try, l.keys...).Scan(&ok); err != nil {
		return err
	}
	if ok {
		return nil
	}
	// lock_timeout 0 would wait for ever.
	if timeout < time.Millisecond {
		return errTimeout
	}
	if notice != nil {
		t := time.AfterFunc(noticeAfter, notice)
		defer t.Stop()
	}
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		// For this transaction alone. The lock lasts beyond it, as a
		// session's advisory locks do. A statement_timeout or, from
		// PostgreSQL 17, a transaction_timeout of the role's or the
		// database's would end the wait sooner, and say less.
		if _, err := tx.Exec(ctx, "SELECT pg_catalog.set_config('lock_timeout', $1, true), "+noTimeouts("true"), milliseconds(timeout)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, l.wait, l.keys...)
		if isLockTimeout(err) {
			return errTimeout
		}
		return err
	})
}

// noTimeouts is a select list that turns statement_timeout off and, from
// PostgreSQL 17, which has it, transaction_timeout, for the transaction
// alone if local is "true", or for the session if it is "false".
func noTimeouts(local string) string {
	return "pg_catalog.set_config('statement_timeout', '0', " + local + "), " +
		"CASE WHEN pg_catalog.current_setting('server_version_num')::integer >= 170000 " +
		"THEN pg_catalog.set_config('transaction_timeout', '0', " + local + ") END"
}

// milliseconds gives d as a setting such as lock_timeout takes it.
func milliseconds(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10) + "ms"
}

// release releases the lock on conn.
func (l advisory) release(ctx context.Context, conn *pgx.Conn) error {
	var ok bool
	if err := conn.QueryRow(ctx, l.unlock, l.keys...).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errors.New("the session didn't hold the lock")
	}
	return nil
}

// lockError describes a failure to take one of the database's locks: held
// for longer than the wait allowed, interrupted, or another problem.
func lockError(ctx context.Context, err error, held string) error {
	switch {
	case errors.Is(err, errTimeout):
		return &heldError{held}
	case ctx.Err() != nil:
		return fmt.Errorf("waiting for the database's lock: %w", ctx.Err())
	default:
		return fmt.Errorf("lock the database: %w", err)
	}
}

// checkDatabase checks what TrueUp needs of the database before it changes
// anything: UTF-8, since TrueUp stores text such as file names in any
// language, and a schema that is TrueUp's, or none. It warns about a
// PostgreSQL release older than TrueUp supports.
func checkDatabase(ctx context.Context, conn *pgx.Conn, where string, log *slog.Logger) error {
	var encoding string
	var version int
	if err := conn.QueryRow(ctx,
		"SELECT pg_catalog.current_setting('server_encoding'), pg_catalog.current_setting('server_version_num')::integer").Scan(&encoding, &version); err != nil {
		return fmt.Errorf("read the database's settings: %w", err)
	}
	if encoding != "UTF8" {
		return fmt.Errorf("the database %s holds text as %s, but TrueUp needs UTF8: create a database of its own with ENCODING 'UTF8'", where, encoding)
	}
	if version < 150000 {
		log.Warn("this release of PostgreSQL is older than TrueUp supports: use PostgreSQL 15 or later", "server_version_num", version)
	}
	return checkOwner(ctx, conn, where)
}

// checkOwner checks that the schema netbox_trueup_ai is TrueUp's, or new, so
// that TrueUp never takes over another application's schema of that name, nor
// runs code that another role put there. It changes nothing.
//
// The schema must belong to the role TrueUp connects as, as must every
// object in it that has an owner, and no other role may create objects in
// it: a role that could put a function there, one that matches a
// call of TrueUp's better than the catalog's does, would have TrueUp run it.
// Then the schema is TrueUp's if the first migration has marked it with its
// comment, or new if it holds no tables or views but, perhaps, goose's, as
// when a database administrator created it for TrueUp, or a process stopped
// before the first migration finished. A schema that doesn't exist is new.
//
// It runs on the lock connection, whose search_path is pg_catalog alone, so
// that nothing in the schema can stand in for the catalog while it looks.
func checkOwner(ctx context.Context, conn *pgx.Conn, where string) error {
	var exists, owned, othersCreate, othersOwn bool
	var comment pgtype.Text
	var tables int64
	err := conn.QueryRow(ctx, `
WITH me AS (SELECT r.oid FROM pg_catalog.pg_roles AS r WHERE r.rolname = CURRENT_USER)
SELECT
    n.oid IS NOT NULL,
    pg_catalog.obj_description(n.oid, 'pg_namespace'),
    COALESCE(n.nspowner = (SELECT oid FROM me), false),
    EXISTS (SELECT FROM pg_catalog.aclexplode(n.nspacl) AS a
        WHERE a.privilege_type = 'CREATE' AND a.grantee <> n.nspowner),
    EXISTS (SELECT FROM pg_catalog.pg_class AS c WHERE c.relnamespace = n.oid AND c.relowner <> (SELECT oid FROM me))
        OR EXISTS (SELECT FROM pg_catalog.pg_proc AS f WHERE f.pronamespace = n.oid AND f.proowner <> (SELECT oid FROM me))
        OR EXISTS (SELECT FROM pg_catalog.pg_type AS t WHERE t.typnamespace = n.oid AND t.typowner <> (SELECT oid FROM me))
        OR EXISTS (SELECT FROM pg_catalog.pg_operator AS o WHERE o.oprnamespace = n.oid AND o.oprowner <> (SELECT oid FROM me))
        OR EXISTS (SELECT FROM pg_catalog.pg_collation AS x WHERE x.collnamespace = n.oid AND x.collowner <> (SELECT oid FROM me))
        OR EXISTS (SELECT FROM pg_catalog.pg_conversion AS x WHERE x.connamespace = n.oid AND x.conowner <> (SELECT oid FROM me))
        OR EXISTS (SELECT FROM pg_catalog.pg_opclass AS x WHERE x.opcnamespace = n.oid AND x.opcowner <> (SELECT oid FROM me))
        OR EXISTS (SELECT FROM pg_catalog.pg_opfamily AS x WHERE x.opfnamespace = n.oid AND x.opfowner <> (SELECT oid FROM me))
        OR EXISTS (SELECT FROM pg_catalog.pg_ts_config AS x WHERE x.cfgnamespace = n.oid AND x.cfgowner <> (SELECT oid FROM me))
        OR EXISTS (SELECT FROM pg_catalog.pg_ts_dict AS x WHERE x.dictnamespace = n.oid AND x.dictowner <> (SELECT oid FROM me))
        OR EXISTS (SELECT FROM pg_catalog.pg_statistic_ext AS x WHERE x.stxnamespace = n.oid AND x.stxowner <> (SELECT oid FROM me)),
    (SELECT pg_catalog.count(*) FROM pg_catalog.pg_class AS c
        WHERE c.relnamespace = n.oid AND c.relkind IN ('r', 'p', 'v', 'm', 'f') AND c.relname <> 'goose_db_version')
FROM (VALUES (1)) AS one
LEFT JOIN pg_catalog.pg_namespace AS n ON n.nspname = $1`, Schema).Scan(&exists, &comment, &owned, &othersCreate, &othersOwn, &tables)
	if err != nil {
		return fmt.Errorf("look for the schema %s: %w", Schema, err)
	}
	switch {
	case !exists:
		return nil
	case !owned:
		return fmt.Errorf("the schema %s in the database %s belongs to another role than the one TrueUp connects as, which could make TrueUp run its code: point database.url at a database of TrueUp's own, or have a database administrator drop the schema and create it with CREATE SCHEMA %s AUTHORIZATION followed by TrueUp's role", Schema, where, Schema)
	case othersCreate:
		return fmt.Errorf("other roles may create objects in the schema %s in the database %s, which could make TrueUp run their code: revoke CREATE on the schema from every role but TrueUp's", Schema, where)
	case othersOwn:
		return fmt.Errorf("the schema %s in the database %s holds objects that another role owns, which could make TrueUp run that role's code: point database.url at a database of TrueUp's own, or have a database administrator drop the schema and create it afresh for TrueUp's role", Schema, where)
	case comment.String == schemaMark || tables == 0:
		return nil
	default:
		return fmt.Errorf("the schema %s in the database %s holds another application's tables, not TrueUp's: point database.url at a database of TrueUp's own", Schema, where)
	}
}

// openSchema brings the schema to the latest version this release knows,
// migrating it if need be, and returns with conn holding the shared lock,
// which the store holds while it is open. It holds the gate while it reads
// the schema's version and, if it migrates, until it is done. When it
// fails, the caller closes conn, which releases what it holds.
func openSchema(ctx context.Context, conn *pgx.Conn, cfg *pgx.ConnConfig, files fs.FS, known int64, timeout time.Duration, log *slog.Logger, migrated *store.Migrated) (err error) {
	// The gate and goose's key both wait for a migration, within one
	// timeout.
	deadline := time.Now().Add(timeout)
	gate := exclusive(gateKey)
	if err := gate.acquire(ctx, conn, timeout, func() {
		log.Info("waiting for another TrueUp process to finish migrating the database")
	}); err != nil {
		return lockError(ctx, err, fmt.Sprintf("another TrueUp process has been migrating the database, or waiting to, for over %s: is a migration stuck?", timeout))
	}
	defer func() {
		if err != nil {
			return
		}
		if unlockErr := gate.release(ctx, conn); unlockErr != nil {
			err = fmt.Errorf("release the database's gate: %w", unlockErr)
		}
	}()
	// Holding the gate, no migration holds the exclusive lock, so the
	// shared one comes at once.
	if err := shared(lockKey).acquire(ctx, conn, timeout, nil); err != nil {
		return lockError(ctx, err, "another TrueUp process holds the database's lock without its gate")
	}
	version, err := readVersion(ctx, conn, time.Until(deadline), timeout, log)
	if err != nil {
		return err
	}
	migrated.VersionBefore, migrated.Version = version, version
	switch {
	case version == known:
		return nil
	case version > known:
		return &store.SchemaTooNewError{Version: version, Known: known}
	}

	// Migrate, keeping the gate and the shared lock. The exclusive lock
	// doesn't wait for this session's own shared one, and no other session
	// waits for what this one holds but those that wait for the gate, which
	// hold nothing yet, so nothing deadlocks.
	if migrated.Applied, err = migrateExclusively(ctx, conn, cfg, files, known, timeout, log, migrated.Location); err != nil {
		return err
	}
	if migrated.Version, err = readVersion(ctx, conn, time.Until(deadline), timeout, log); err != nil {
		return err
	}
	if migrated.Version != known {
		return fmt.Errorf("the database's schema is at version %d after migrating, not %d", migrated.Version, known)
	}
	return nil
}

// migrateExclusively creates TrueUp's schema if it doesn't exist and applies
// the migrations it lacks, holding the exclusive lock, which waits until no
// other store has the database open, and then connKey, which waits until no
// other store's pooled connection does. It migrates through a pool of
// connections of its own, closed afterwards, so that no setting a migration
// changes outlives it, under goose's own lock, gooseKey. A migration's
// statements wait up to timeout for a table another session holds.
func migrateExclusively(ctx context.Context, conn *pgx.Conn, cfg *pgx.ConnConfig, files fs.FS, known int64, timeout time.Duration, log *slog.Logger, where string) (_ []store.Migration, err error) {
	var once sync.Once
	notice := func() {
		once.Do(func() { log.Info("waiting for other TrueUp processes to close the database, to migrate it") })
	}
	held := fmt.Sprintf("other TrueUp processes have had the database open for over %s, so it can't be migrated: let them finish, or stop them, and try again", timeout)
	deadline := time.Now().Add(timeout)
	for _, l := range []advisory{exclusive(lockKey), exclusive(connKey)} {
		if err := l.acquire(ctx, conn, time.Until(deadline), notice); err != nil {
			return nil, lockError(ctx, err, held)
		}
		// After a failure, the caller closes conn, which releases it. The
		// migrations were applied, if release fails, under goose's lock.
		defer func() {
			if err != nil {
				return
			}
			if unlockErr := l.release(ctx, conn); unlockErr != nil {
				err = fmt.Errorf("the migrations were applied, but the connection that held the database's lock broke meanwhile: %w", unlockErr)
			}
		}()
	}
	// createSchema leaves a schema that already exists as it is, and the
	// schema may have changed since checkDatabase looked, before the gate,
	// so it is checked again, as it is now, before anything goes into it.
	if err := createSchema(ctx, conn); err != nil {
		return nil, err
	}
	if err := checkOwner(ctx, conn, where); err != nil {
		return nil, err
	}
	migrationCfg := cfg.Copy()
	migrationCfg.AfterConnect = func(ctx context.Context, c *pgconn.PgConn) error {
		if err := setSession(ctx, c); err != nil {
			return err
		}
		// A role's statement_timeout could cut a migration short.
		if _, err := c.Exec(ctx, "SELECT pg_catalog.set_config('lock_timeout', '"+milliseconds(timeout)+"', false), "+noTimeouts("false")).ReadAll(); err != nil {
			return fmt.Errorf("set the migration's timeouts: %w", err)
		}
		return nil
	}
	db := stdlib.OpenDB(*migrationCfg)
	defer func() { err = errors.Join(err, db.Close()) }()
	// The lock connection's backend, as the server knows it, which is the
	// pid that pg_locks gives even behind a pooler.
	var pid int32
	if err := conn.QueryRow(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&pid); err != nil {
		return nil, fmt.Errorf("read the lock connection's process: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, files, goose.WithDisableGlobalRegistry(true),
		goose.WithTableName(Schema+".goose_db_version"), goose.WithSessionLocker(gooseLocker{pid}))
	if err != nil {
		return nil, fmt.Errorf("read the migrations: %w", err)
	}
	before, err := p.GetDBVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the database's schema version: %w", withoutUser(err))
	}
	// The gate keeps other migrations out since openSchema read the version,
	// but goose would apply nothing to a newer schema and say nothing, so
	// check again rather than rely on it.
	if before > known {
		return nil, &store.SchemaTooNewError{Version: before, Known: known}
	}

	results, err := p.Up(ctx)
	// When a migration fails, goose returns what it applied before in the
	// error, and the failed migration has rolled back.
	partial, isPartial := errors.AsType[*goose.PartialError](err)
	if isPartial {
		results = partial.Applied
	}
	var applied []store.Migration
	for _, r := range results {
		m := store.Migration{Version: r.Source.Version, Name: path.Base(r.Source.Path)}
		applied = append(applied, m)
		log.Info("applied a database migration", "version", m.Version, "migration", m.Name, "duration", r.Duration)
	}
	switch {
	case isPartial && isLockTimeout(partial.Err):
		return applied, fmt.Errorf("migrate the database's schema from version %d: migration %s waited over %s for a table that another session holds, such as a pg_dump: try again once it is done: %w",
			before, path.Base(partial.Failed.Source.Path), timeout, withoutUser(partial.Err))
	case isPartial:
		return applied, fmt.Errorf("migrate the database's schema from version %d: migration %s failed: %w",
			before, path.Base(partial.Failed.Source.Path), withoutUser(partial.Err))
	case err != nil:
		return applied, fmt.Errorf("migrate the database's schema from version %d: %w", before, withoutUser(err))
	}
	return applied, nil
}

// isLockTimeout reports whether err is PostgreSQL's lock_not_available, which
// lock_timeout raises.
func isLockTimeout(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == "55P03"
}

// createSchema creates TrueUp's schema, where goose keeps its table, if it
// doesn't exist, and leaves one that does as it is, for checkOwner to judge.
// Creating it needs the CREATE privilege on the database, so a database
// administrator can create the schema instead, owned by TrueUp's role.
func createSchema(ctx context.Context, conn *pgx.Conn) error {
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_catalog.pg_namespace WHERE nspname = $1)", Schema).Scan(&exists); err != nil {
		return fmt.Errorf("look for the schema %s: %w", Schema, err)
	}
	if exists {
		return nil
	}
	_, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+Schema)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "42501" { // insufficient_privilege
		return fmt.Errorf("create the schema %s: %w: grant TrueUp's role CREATE on the database, or create the schema for it with CREATE SCHEMA %s AUTHORIZATION followed by the role's name", Schema, err, Schema)
	}
	if err != nil {
		return fmt.Errorf("create the schema %s: %w", Schema, err)
	}
	return nil
}

// latestVersion returns the newest migration's version in files, as goose
// reads it from the file's name.
func latestVersion(files fs.FS) (int64, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return 0, fmt.Errorf("read the migrations: %w", err)
	}
	var latest int64
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		v, err := goose.NumericComponent(e.Name())
		if err != nil {
			return 0, fmt.Errorf("read the migrations: %w", err)
		}
		latest = max(latest, v)
	}
	return latest, nil
}

// readVersion reads the schema's version as schemaVersion does, holding
// gooseKey shared, so that a migration goose is still applying for a process
// that has lost its other locks is waited for, for what remains of the wait,
// rather than read from before it. timeout is the whole wait, for the message.
func readVersion(ctx context.Context, conn *pgx.Conn, remaining, timeout time.Duration, log *slog.Logger) (int64, error) {
	if err := gooseShared.acquire(ctx, conn, remaining, func() {
		log.Info("waiting for another TrueUp process to finish migrating the database")
	}); err != nil {
		return 0, lockError(ctx, err, fmt.Sprintf("another TrueUp process has been migrating the database for over %s: is a migration stuck?", timeout))
	}
	version, err := schemaVersion(ctx, conn)
	if err != nil {
		return 0, err
	}
	if err := gooseShared.release(ctx, conn); err != nil {
		return 0, fmt.Errorf("release goose's lock: %w", err)
	}
	return version, nil
}

// gooseLocker is the lock goose takes on the connection it migrates through,
// before it reads which migrations are applied: gooseKey. It doesn't wait,
// since gooseKey is free unless a migration whose process has lost its other
// locks is still running. Once it holds gooseKey, it checks that the session
// with the backend process pid, the migrating process's lock connection,
// still holds lockKey exclusively, as it took it before goose began:
// otherwise that connection broke before goose took gooseKey, so another
// process may have opened the database, or begun to migrate it, since, and
// goose mustn't change it.
type gooseLocker struct{ pid int32 }

func (l gooseLocker) SessionLock(ctx context.Context, conn *sql.Conn) (err error) {
	var ok bool
	if err := conn.QueryRowContext(ctx, gooseExclusive.try, gooseExclusive.keys...).Scan(&ok); err != nil {
		return fmt.Errorf("take goose's lock: %w", withoutUser(err))
	}
	if !ok {
		return errors.New("another TrueUp process is still applying a migration: try again once it is done")
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, l.SessionUnlock(context.WithoutCancel(ctx), conn))
		}
	}()
	var held bool
	if err := conn.QueryRowContext(ctx, `
SELECT EXISTS (SELECT FROM pg_catalog.pg_locks AS l
    WHERE l.locktype = 'advisory' AND l.mode = 'ExclusiveLock' AND l.granted
        AND l.database = (SELECT d.oid FROM pg_catalog.pg_database AS d WHERE d.datname = pg_catalog.current_database())
        AND l.classid = $1::integer::pg_catalog.oid AND l.objid = $2::integer::pg_catalog.oid AND l.objsubid = 2
        AND l.pid = $3::integer)`,
		lockClass, lockKey, l.pid).Scan(&held); err != nil {
		return fmt.Errorf("check the database's lock: %w", withoutUser(err))
	}
	if !held {
		return errors.New("the connection that held the database's lock broke before the migration began, so another process may have opened the database: try again")
	}
	return nil
}

func (gooseLocker) SessionUnlock(ctx context.Context, conn *sql.Conn) error {
	var ok bool
	if err := conn.QueryRowContext(ctx, gooseExclusive.unlock, gooseExclusive.keys...).Scan(&ok); err != nil {
		return fmt.Errorf("release goose's lock: %w", withoutUser(err))
	}
	if !ok {
		return errors.New("release goose's lock: the session didn't hold it")
	}
	return nil
}

// querier is what schemaVersion needs: a connection, or a transaction.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// schemaVersion reads the schema's version as goose records it: the highest
// in its table, or 0 before the table exists. It writes nothing.
func schemaVersion(ctx context.Context, q querier) (int64, error) {
	var exists bool
	if err := q.QueryRow(ctx, "SELECT pg_catalog.to_regclass($1) IS NOT NULL", Schema+".goose_db_version").Scan(&exists); err != nil {
		return 0, fmt.Errorf("read the database's schema version: %w", err)
	}
	if !exists {
		return 0, nil
	}
	var version pgtype.Int8
	if err := q.QueryRow(ctx, "SELECT pg_catalog.max(version_id) FROM "+Schema+".goose_db_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read the database's schema version: %w", err)
	}
	return version.Int64, nil
}
