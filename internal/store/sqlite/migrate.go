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
	"time"

	"github.com/pressly/goose/v3"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
)

// migrationFiles are the schema's migrations, applied in order of their
// numbers. Never change one that a release has shipped: add another.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// lockSuffix names the file beside the database that migrations lock.
const lockSuffix = "-lock"

// migrate applies the migrations the database lacks. It holds the lock on
// the file beside the database while it reads the schema's version, so that
// processes opening the database at once neither apply a migration twice
// nor read a schema that another is changing.
func migrate(ctx context.Context, db *sql.DB, dbPath string, log *slog.Logger) (store.Migrated, error) {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return store.Migrated{Engine: Engine, Location: dbPath}, fmt.Errorf("read the migrations: %w", err)
	}
	return migrateFrom(ctx, db, dbPath, files, lockTimeout, log)
}

// migrateFrom is migrate with the migrations in files, waiting up to
// timeout for the lock.
func migrateFrom(ctx context.Context, db *sql.DB, dbPath string, files fs.FS, timeout time.Duration, log *slog.Logger) (_ store.Migrated, err error) {
	migrated := store.Migrated{Engine: Engine, Location: dbPath}
	p, err := goose.NewProvider(goose.DialectSQLite3, db, files, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return migrated, fmt.Errorf("read the migrations: %w", err)
	}
	known := latest(p.ListSources())

	unlock, err := lockFile(ctx, dbPath+lockSuffix, timeout, log)
	if err != nil {
		return migrated, err
	}
	defer func() {
		if unlockErr := unlock(); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("release the database's migration lock: %w", unlockErr))
		}
	}()

	if migrated.VersionBefore, err = p.GetDBVersion(ctx); err != nil {
		return migrated, fmt.Errorf("read the database's schema version: %w", err)
	}
	// goose would apply nothing to a newer schema, and say nothing.
	if migrated.VersionBefore > known {
		return migrated, &store.SchemaTooNewError{Version: migrated.VersionBefore, Known: known}
	}
	results, err := p.Up(ctx)
	// When a migration fails, goose returns what it applied before in the
	// error, and the failed migration has rolled back.
	partial, isPartial := errors.AsType[*goose.PartialError](err)
	if isPartial {
		results = partial.Applied
	}
	for _, r := range results {
		m := store.Migration{Version: r.Source.Version, Name: path.Base(r.Source.Path)}
		migrated.Applied = append(migrated.Applied, m)
		log.Info("applied a database migration", "version", m.Version, "migration", m.Name, "duration", r.Duration)
	}
	switch {
	case isPartial:
		return migrated, fmt.Errorf("migrate the database's schema from version %d: migration %s failed: %w",
			migrated.VersionBefore, path.Base(partial.Failed.Source.Path), partial.Err)
	case err != nil:
		return migrated, fmt.Errorf("migrate the database's schema from version %d: %w", migrated.VersionBefore, err)
	}
	if migrated.Version, err = p.GetDBVersion(ctx); err != nil {
		return migrated, fmt.Errorf("read the database's schema version: %w", err)
	}
	if migrated.Version != known {
		return migrated, fmt.Errorf("the database's schema is at version %d after migrating, not %d", migrated.Version, known)
	}
	if len(migrated.Applied) > 0 {
		if err := checkForeignKeys(ctx, db); err != nil {
			return migrated, err
		}
	}
	return migrated, nil
}

// latest returns the newest migration's version.
func latest(sources []*goose.Source) int64 {
	var v int64
	for _, s := range sources {
		v = max(v, s.Version)
	}
	return v
}

// checkForeignKeys reports rows that break a foreign key, which a migration
// that rebuilds a table can leave behind: SQLite checks foreign keys row by
// row, and only when they are on.
func checkForeignKeys(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("check foreign keys after migrating: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int64
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return fmt.Errorf("check foreign keys after migrating: %w", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("check foreign keys after migrating: %w", err)
	}
	if len(tables) > 0 {
		return fmt.Errorf("after migrating, %d rows break foreign keys, in table %s", len(tables), tables[0])
	}
	return nil
}
