// Package sqlite is TrueUp's store on SQLite, through modernc.org/sqlite,
// which is pure Go (docs/adr/0011-store-state-with-sqlc-and-goose.md).
//
// The database is one file, created readable and writable by its owner only
// (B6 in docs/architecture.md); SQLite gives its -wal and -shm files the
// same mode. Every connection runs in WAL mode, enforces foreign keys, waits
// up to busyTimeout for another connection's lock, and syncs every commit to
// disk (synchronous=FULL), so a committed transaction survives a power cut.
// Write transactions take SQLite's write lock as they begin (_txlock
// immediate), so two writers queue instead of failing when one would have
// to upgrade a read lock.
//
// The file's header marks it as TrueUp's (its application ID), and opening
// refuses a file that holds another application's database.
//
// Migrations are the goose files in migrations/, embedded in the binary.
// Each process holds a shared lock on a file beside the database while it
// has the database open, and opening applies the migrations the database
// lacks under the exclusive lock, so that a migration waits for every other
// process to close the database. The operating system releases the locks if
// a process dies. Queries are the files in queries/, which sqlc compiles
// into package sqlitedb.
//
// Several processes on one host can share the database. It doesn't belong
// on a network file system, where WAL mode doesn't work.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/filelock"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/sqlite/sqlitedb"
)

// Engine is the name of the engine, as store.Migrated gives it.
const Engine = "sqlite"

// timeLayout is how times are stored: UTC, to the microsecond, at a fixed
// width, so that comparing two as text compares the times.
const timeLayout = "2006-01-02T15:04:05.000000Z"

// Options configure Open.
type Options struct {
	// Path is the database file's absolute path. Open creates the file,
	// and its directory, if they don't exist.
	Path string
	// Logger gets a record of each migration applied. Nil means no
	// logging.
	Logger *slog.Logger
}

// Store is TrueUp's store on SQLite.
type Store struct {
	db *sql.DB
	q  *sqlitedb.Queries
	// lock is the shared lock on the file beside the database, held while
	// the store is open.
	lock *filelock.Lock
}

var _ store.Store = (*Store)(nil)

// Open opens the database at opts.Path, creating it if it doesn't exist,
// and applies the migrations it lacks. It refuses a database whose schema
// is newer than this release knows, with a *store.SchemaTooNewError. When a
// migration fails, the store.Migrated it returns says which were applied
// before it. Close the store when done, to let other processes migrate the
// database.
func Open(ctx context.Context, opts Options) (*Store, store.Migrated, error) {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, store.Migrated{Engine: Engine, Location: opts.Path}, fmt.Errorf("read the migrations: %w", err)
	}
	return open(ctx, opts, files, lockTimeout)
}

// open is Open with the migrations in files, waiting up to timeout for the
// database's lock.
func open(ctx context.Context, opts Options, files fs.FS, timeout time.Duration) (_ *Store, migrated store.Migrated, err error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	migrated = store.Migrated{Engine: Engine, Location: opts.Path}
	if !filepath.IsAbs(opts.Path) {
		return nil, migrated, errors.New("the SQLite database's path must be absolute")
	}
	path := filepath.Clean(opts.Path)
	if err := createFile(ctx, path, logger); err != nil {
		return nil, migrated, err
	}
	db, err := openDB(ctx, path)
	if err != nil {
		return nil, migrated, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, db.Close())
		}
	}()
	lock, migrated, err := openSchema(ctx, db, path, files, timeout, logger)
	if err != nil {
		return nil, migrated, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, lock.Unlock())
		}
	}()
	if err := checkForeignKeys(ctx, db); err != nil {
		return nil, migrated, err
	}
	return &Store{db: db, q: sqlitedb.New(db), lock: lock}, migrated, nil
}

// Close closes the database, and then releases the lock that holds off
// migrations.
func (s *Store) Close() error {
	var err error
	if closeErr := s.db.Close(); closeErr != nil {
		err = fmt.Errorf("close the database: %w", closeErr)
	}
	if unlockErr := s.lock.Unlock(); unlockErr != nil {
		err = errors.Join(err, fmt.Errorf("release the database's lock: %w", unlockErr))
	}
	return err
}

// RecordFetch records a fetch of a source in one transaction.
func (s *Store) RecordFetch(ctx context.Context, f store.Fetch) error {
	f, err := f.Prepare()
	if err != nil {
		return fmt.Errorf("record a fetch: %w", err)
	}
	at := f.FetchedAt.Format(timeLayout)
	err = s.inTx(ctx, nil, func(q *sqlitedb.Queries) error {
		if err := q.UpsertSource(ctx, sqlitedb.UpsertSourceParams{
			Name:      f.Source.Name,
			Kind:      f.Source.Kind,
			Url:       f.Source.URL,
			Ref:       f.Source.Ref,
			FetchedAt: at,
			Error:     sql.NullString{String: f.Error, Valid: f.Error != ""},
		}); err != nil {
			return err
		}
		if f.Error != "" {
			return nil
		}
		if err := q.UpsertSnapshot(ctx, sqlitedb.UpsertSnapshotParams{
			Source:    f.Source.Name,
			Url:       f.Source.URL,
			Version:   f.Version,
			Ref:       f.Ref,
			FetchedAt: at,
		}); err != nil {
			return err
		}
		return replaceLists(ctx, q, f)
	})
	if err != nil {
		return fmt.Errorf("record the fetch of source %s: %w", f.Source.Name, err)
	}
	return nil
}

// replaceLists replaces a snapshot's file list and skipped entries.
func replaceLists(ctx context.Context, q *sqlitedb.Queries, f store.Fetch) error {
	if err := q.DeleteSnapshotFiles(ctx, f.Source.Name); err != nil {
		return err
	}
	if err := q.DeleteSnapshotSkipped(ctx, f.Source.Name); err != nil {
		return err
	}
	for _, file := range f.Files {
		if err := q.InsertSnapshotFile(ctx, sqlitedb.InsertSnapshotFileParams{
			Source: f.Source.Name, Path: file.Path, Size: file.Size, Digest: file.Digest,
		}); err != nil {
			return fmt.Errorf("file %q: %w", file.Path, err)
		}
	}
	for i, sk := range f.Skipped {
		if err := q.InsertSnapshotSkipped(ctx, sqlitedb.InsertSnapshotSkippedParams{
			Source: f.Source.Name, Seq: int64(i), Path: []byte(sk.Path), Reason: sk.Reason,
		}); err != nil {
			return fmt.Errorf("skipped entry %q: %w", sk.Path, err)
		}
	}
	return nil
}

// Sources returns every recorded source, sorted by name.
func (s *Store) Sources(ctx context.Context) ([]store.Source, error) {
	rows, err := s.q.ListSources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the sources: %w", err)
	}
	out := make([]store.Source, 0, len(rows))
	for _, r := range rows {
		fetchedAt, err := parseTime(r.FetchedAt)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", r.Name, err)
		}
		src := store.Source{
			SourceConfig: store.SourceConfig{Name: r.Name, Kind: r.Kind, URL: r.Url, Ref: r.Ref},
			FetchedAt:    fetchedAt,
			Error:        r.Error.String,
		}
		if r.SnapshotVersion.Valid {
			snapAt, err := parseTime(r.SnapshotFetchedAt.String)
			if err != nil {
				return nil, fmt.Errorf("source %s: snapshot: %w", r.Name, err)
			}
			src.Snapshot = &store.Snapshot{
				Source:    r.Name,
				URL:       r.SnapshotUrl.String,
				Version:   r.SnapshotVersion.String,
				Ref:       r.SnapshotRef.String,
				FetchedAt: snapAt,
			}
		}
		out = append(out, src)
	}
	return out, nil
}

// Snapshot returns a source's snapshot and its lists, read in one
// transaction so that they agree.
func (s *Store) Snapshot(ctx context.Context, source string) (store.Snapshot, error) {
	var snap store.Snapshot
	err := s.inTx(ctx, &sql.TxOptions{ReadOnly: true}, func(q *sqlitedb.Queries) error {
		row, err := q.GetSnapshot(ctx, source)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		at, err := parseTime(row.FetchedAt)
		if err != nil {
			return err
		}
		snap = store.Snapshot{Source: row.Source, URL: row.Url, Version: row.Version, Ref: row.Ref, FetchedAt: at}
		files, err := q.ListSnapshotFiles(ctx, source)
		if err != nil {
			return fmt.Errorf("list its files: %w", err)
		}
		snap.Files = make([]store.File, len(files))
		for i, f := range files {
			snap.Files[i] = store.File{Path: f.Path, Size: f.Size, Digest: f.Digest}
		}
		skipped, err := q.ListSnapshotSkipped(ctx, source)
		if err != nil {
			return fmt.Errorf("list its skipped entries: %w", err)
		}
		snap.Skipped = make([]store.Skipped, len(skipped))
		for i, sk := range skipped {
			snap.Skipped[i] = store.Skipped{Path: string(sk.Path), Reason: sk.Reason}
		}
		return nil
	})
	if err != nil {
		return store.Snapshot{}, fmt.Errorf("read the snapshot of source %s: %w", source, err)
	}
	return snap, nil
}

// RemoveSources removes every source not named in keep, and its snapshot,
// in one transaction. Foreign keys don't cascade, so it deletes each
// source's rows from the bottom up.
func (s *Store) RemoveSources(ctx context.Context, keep []string) ([]string, error) {
	var removed []string
	err := s.inTx(ctx, nil, func(q *sqlitedb.Queries) error {
		names, err := q.ListSourceNames(ctx)
		if err != nil {
			return err
		}
		for _, name := range names {
			if slices.Contains(keep, name) {
				continue
			}
			for _, del := range []func(context.Context, string) error{
				q.DeleteSnapshotFiles, q.DeleteSnapshotSkipped, q.DeleteSnapshot, q.DeleteSource,
			} {
				if err := del(ctx, name); err != nil {
					return fmt.Errorf("source %s: %w", name, err)
				}
			}
			removed = append(removed, name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("remove sources no longer configured: %w", err)
	}
	return removed, nil
}

// inTx runs fn in a transaction, and commits it if fn succeeds.
func (s *Store) inTx(ctx context.Context, opts *sql.TxOptions, fn func(*sqlitedb.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin a transaction: %w", err)
	}
	if err := fn(s.q.WithTx(tx)); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return errors.Join(err, fmt.Errorf("roll back: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("the database holds a time it can't read, %q", s)
	}
	return t, nil
}
