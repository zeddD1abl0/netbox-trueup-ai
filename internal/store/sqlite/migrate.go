package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/filelock"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
)

// migrationFiles are the schema's migrations, applied in order of their
// numbers. Never change one that a release has shipped: add another.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// Two files beside the database put processes in order:
//
//   - each process holds a shared lock on lockSuffix's file while it has the
//     database open, and a migration needs the exclusive one, so that the
//     schema never changes under a process that has read it;
//   - a migration holds gateSuffix's file from before it waits for that
//     exclusive lock until it is done, and each process takes the gate for a
//     moment before its shared lock, so that a migration waiting for running
//     processes to close the database isn't put off for ever by new ones.
const (
	lockSuffix = "-lock"
	gateSuffix = "-gate"
)

// lockTimeout is the longest to wait for either: for a migration to finish,
// before opening the database, or for other processes to close it, before
// migrating it.
const lockTimeout = 2 * time.Minute

// heldError means another process held one of the database's locks for
// longer than the wait allowed. It says what in its own words, and wraps
// filelock.ErrTimeout.
type heldError struct{ msg string }

func (e *heldError) Error() string { return e.msg }
func (e *heldError) Unwrap() error { return filelock.ErrTimeout }

// openSchema returns the shared lock on the file beside the database, which
// the store holds while it is open, once the schema is the latest this
// release knows, migrating it if need be. It holds the gate while it reads
// the schema's version, and, if it migrates, until it is done.
func openSchema(ctx context.Context, db *sql.DB, dbPath string, files fs.FS, timeout time.Duration, log *slog.Logger) (_ *filelock.Lock, migrated store.Migrated, err error) {
	migrated = store.Migrated{Engine: Engine, Location: dbPath}
	known, err := latestVersion(files)
	if err != nil {
		return nil, migrated, err
	}
	gate, err := filelock.Exclusive(ctx, dbPath+gateSuffix, timeout, func() {
		log.Info("waiting for another TrueUp process to finish migrating the database")
	})
	if err != nil {
		return nil, migrated, lockError(err, fmt.Sprintf("another TrueUp process has been migrating the database, or waiting to, for over %s: is a migration stuck?", timeout))
	}
	defer func() {
		if unlockErr := gate.Unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("release the database's gate: %w", unlockErr))
		}
	}()
	// Holding the gate, no migration holds the exclusive lock, so the
	// shared one comes at once.
	shared, err := filelock.Shared(ctx, dbPath+lockSuffix, timeout, nil)
	if err != nil {
		return nil, migrated, lockError(err, "another TrueUp process holds the database's lock without its gate")
	}
	version, err := schemaVersion(ctx, db)
	if err != nil {
		return nil, migrated, errors.Join(err, shared.Unlock())
	}
	migrated.VersionBefore, migrated.Version = version, version
	switch {
	case version == known:
		return shared, migrated, nil
	case version > known:
		return nil, migrated, errors.Join(&store.SchemaTooNewError{Version: version, Known: known}, shared.Unlock())
	}

	// Migrate, holding the gate throughout.
	if err := shared.Unlock(); err != nil {
		return nil, migrated, fmt.Errorf("release the database's lock: %w", err)
	}
	migrated.Applied, err = migrateExclusively(ctx, dbPath, files, known, timeout, log)
	if err != nil {
		return nil, migrated, err
	}
	if shared, err = filelock.Shared(ctx, dbPath+lockSuffix, timeout, nil); err != nil {
		return nil, migrated, lockError(err, "another TrueUp process holds the database's lock without its gate")
	}
	if migrated.Version, err = schemaVersion(ctx, db); err != nil {
		return nil, migrated, errors.Join(err, shared.Unlock())
	}
	if migrated.Version != known {
		return nil, migrated, errors.Join(fmt.Errorf("the database's schema is at version %d after migrating, not %d", migrated.Version, known), shared.Unlock())
	}
	return shared, migrated, nil
}

// migrateExclusively applies the migrations the database lacks, holding the
// exclusive lock, which waits until no other process has the database open.
// It migrates through a pool of connections of its own, closed afterwards,
// so that no setting a migration changes, such as foreign_keys, outlives it.
func migrateExclusively(ctx context.Context, dbPath string, files fs.FS, known int64, timeout time.Duration, log *slog.Logger) (_ []store.Migration, err error) {
	lock, err := filelock.Exclusive(ctx, dbPath+lockSuffix, timeout, func() {
		log.Info("waiting for other TrueUp processes to close the database, to migrate it")
	})
	if err != nil {
		return nil, lockError(err, fmt.Sprintf("other TrueUp processes have had the database open for over %s, so it can't be migrated: let them finish, or stop them, and try again", timeout))
	}
	defer func() {
		if unlockErr := lock.Unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("release the database's lock: %w", unlockErr))
		}
	}()
	migrationDB, err := openDB(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, migrationDB.Close()) }()
	p, err := goose.NewProvider(goose.DialectSQLite3, migrationDB, files, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return nil, fmt.Errorf("read the migrations: %w", err)
	}
	before, err := p.GetDBVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the database's schema version: %w", err)
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
	case isPartial:
		return applied, fmt.Errorf("migrate the database's schema from version %d: migration %s failed: %w",
			before, path.Base(partial.Failed.Source.Path), partial.Err)
	case err != nil:
		return applied, fmt.Errorf("migrate the database's schema from version %d: %w", before, err)
	}
	return applied, nil
}

// lockError describes a failure to take one of the database's locks: held
// for longer than the wait allowed, interrupted, or the lock file's own
// problem.
func lockError(err error, held string) error {
	switch {
	case errors.Is(err, filelock.ErrTimeout):
		return &heldError{held}
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("waiting for the database's lock: %w", err)
	default:
		return fmt.Errorf("lock the database: %w", err)
	}
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

// schemaVersion reads the schema's version as goose records it: the highest
// in its table, or 0 before the table exists. It writes nothing, so that it
// can run under the shared lock.
func schemaVersion(ctx context.Context, db *sql.DB) (int64, error) {
	var tables int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'goose_db_version'").Scan(&tables); err != nil {
		return 0, fmt.Errorf("read the database's schema version: %w", err)
	}
	if tables == 0 {
		return 0, nil
	}
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT MAX(version_id) FROM goose_db_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read the database's schema version: %w", err)
	}
	return version.Int64, nil
}

// checkForeignKeys reports rows that break a foreign key. SQLite checks
// foreign keys as rows change, and only on connections that enforce them,
// so a migration, or another program, can leave such rows behind. It runs
// whenever the store opens, so that a database left that way is refused
// every time, not only by the process that migrated it.
func checkForeignKeys(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("check the database's foreign keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int64
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return fmt.Errorf("check the database's foreign keys: %w", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check the database's foreign keys: %w", err)
	}
	if len(tables) > 0 {
		return fmt.Errorf("the database holds %d rows that break foreign keys, in table %s: restore it from a backup", len(tables), tables[0])
	}
	return nil
}
