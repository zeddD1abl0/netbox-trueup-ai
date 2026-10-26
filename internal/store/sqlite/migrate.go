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

// lockSuffix names the file beside the database whose lock puts processes
// in order: each holds a shared lock while it has the database open, and a
// migration needs the exclusive one.
const lockSuffix = "-lock"

// lockTimeout is the longest to wait for that lock: for a migration to
// finish, before opening the database, or for other processes to close it,
// before migrating it.
const lockTimeout = 2 * time.Minute

// openSchema takes the shared lock on the file beside the database, which
// the store holds while it is open, and returns it once the schema is the
// latest this release knows, migrating it if need be. To migrate, it trades
// the shared lock for the exclusive one, then takes the shared lock again
// and reads the schema's version afresh, since another process may have
// changed it meanwhile.
func openSchema(ctx context.Context, db *sql.DB, dbPath string, files fs.FS, timeout time.Duration, log *slog.Logger) (*filelock.Lock, store.Migrated, error) {
	migrated := store.Migrated{Engine: Engine, Location: dbPath}
	known, err := latestVersion(files)
	if err != nil {
		return nil, migrated, err
	}
	for first := true; ; first = false {
		lock, err := filelock.Shared(ctx, dbPath+lockSuffix, timeout, func() {
			log.Info("waiting for another TrueUp process to finish migrating the database")
		})
		if err != nil {
			return nil, migrated, lockError(err, fmt.Sprintf("another TrueUp process has been migrating the database for over %s: is a migration stuck?", timeout))
		}
		version, err := schemaVersion(ctx, db)
		if err != nil {
			return nil, migrated, errors.Join(err, lock.Unlock())
		}
		if first {
			migrated.VersionBefore = version
		}
		migrated.Version = version
		switch {
		case version == known:
			return lock, migrated, nil
		case version > known:
			return nil, migrated, errors.Join(&store.SchemaTooNewError{Version: version, Known: known}, lock.Unlock())
		case !first:
			return nil, migrated, errors.Join(fmt.Errorf("the database's schema is at version %d after migrating, not %d", version, known), lock.Unlock())
		}
		if err := lock.Unlock(); err != nil {
			return nil, migrated, fmt.Errorf("release the database's lock: %w", err)
		}
		applied, err := migrateExclusively(ctx, db, dbPath, files, known, timeout, log)
		migrated.Applied = append(migrated.Applied, applied...)
		if err != nil {
			return nil, migrated, err
		}
	}
}

// migrateExclusively applies the migrations the database lacks, holding the
// exclusive lock, which waits until no other process has the database open.
// While it waits, it reads the schema's version through db each second, and
// gives up waiting, applying nothing, once another process has migrated the
// schema: that process then holds its shared lock for as long as it has the
// database open. It migrates through a pool of connections of its own,
// closed afterwards, so that no setting a migration changes, such as
// foreign_keys, outlives it.
func migrateExclusively(ctx context.Context, db *sql.DB, dbPath string, files fs.FS, known int64, timeout time.Duration, log *slog.Logger) (_ []store.Migration, err error) {
	start, noticed := time.Now(), false
	var lock *filelock.Lock
	for lock == nil {
		lock, err = filelock.Exclusive(ctx, dbPath+lockSuffix, min(time.Second, max(timeout-time.Since(start), 0)), nil)
		if err != nil && !errors.Is(err, filelock.ErrTimeout) {
			return nil, lockError(err, "")
		}
		if lock != nil {
			break
		}
		version, err := schemaVersion(ctx, db)
		if err != nil {
			return nil, err
		}
		if version >= known {
			return nil, nil // migrated by another process
		}
		if time.Since(start) >= timeout {
			return nil, fmt.Errorf("other TrueUp processes have had the database open for over %s, so it can't be migrated: let them finish, or stop them, and try again", timeout)
		}
		if !noticed {
			log.Info("waiting for other TrueUp processes to close the database, to migrate it")
			noticed = true
		}
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
	// A newer release may have migrated it since the shared lock went, and
	// goose would apply nothing to a newer schema, and say nothing.
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

// lockError describes a failure to take the database's lock: held for longer
// than the wait allowed, interrupted, or the lock file's own problem.
func lockError(err error, held string) error {
	switch {
	case errors.Is(err, filelock.ErrTimeout):
		return errors.New(held)
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
