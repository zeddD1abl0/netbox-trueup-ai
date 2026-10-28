// Package postgres is TrueUp's store on PostgreSQL, through pgx
// (docs/adr/0011-store-state-with-sqlc-and-goose.md), for a database that
// processes on several hosts can share.
//
// TrueUp keeps its tables in a schema of its own, netbox_trueup_ai, which it
// creates if it doesn't exist, and every connection's search_path names that
// schema alone, so that TrueUp's tables and queries never mix with another
// application's in the same database. The first migration marks the schema
// with a comment, and opening refuses a schema of that name that holds
// tables without the mark. The database must hold text as UTF-8.
//
// Migrations are the goose files in migrations/, embedded in the binary. The
// locks of docs/adr/0021 are advisory locks (see lockKey in migrate.go): a
// store holds the shared lock on a connection of its own for as long as it
// is open, and opening applies the migrations the database lacks under the
// exclusive lock, so that a migration waits for every other store to close;
// a gate keeps stores that open meanwhile from keeping it waiting. A
// session's advisory locks belong to its server connection, so a pooler such
// as PgBouncer in front of PostgreSQL must pool by session, never by
// transaction or statement. Queries are the files in queries/, which sqlc
// compiles into package postgresdb.
//
// The connection is database.url's, with libpq's parameters and environment
// variables as pgx reads them, TLS among them: by default sslmode prefer,
// which encrypts the connection when the server offers TLS, but verifies
// nothing (B6 in docs/architecture.md).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/store/postgres/postgresdb"
)

// Engine is the name of the engine, as store.Migrated gives it.
const Engine = "postgres"

// closeTimeout is the longest Close waits for the server.
const closeTimeout = 10 * time.Second

// Options configure Open.
type Options struct {
	// URL is database.url: a postgres or postgresql URL, with libpq's
	// parameters.
	URL config.Secret
	// Logger gets a record of each migration applied. Nil means no
	// logging.
	Logger *slog.Logger
}

// Store is TrueUp's store on PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
	q    *postgresdb.Queries
	// lock is the connection that holds the shared lock while the store is
	// open.
	lock *pgx.Conn
}

var _ store.Store = (*Store)(nil)

// Open connects to the database that opts.URL names, creates TrueUp's schema
// in it if it doesn't exist, and applies the migrations it lacks. It refuses
// a schema newer than this release knows, with a *store.SchemaTooNewError,
// and a URL it can't use, with a *ConfigError. When a migration fails, the
// store.Migrated it returns says which were applied before it. Close the
// store when done, to let other processes migrate the database.
func Open(ctx context.Context, opts Options) (*Store, store.Migrated, error) {
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, store.Migrated{Engine: Engine}, fmt.Errorf("read the migrations: %w", err)
	}
	return open(ctx, opts, files, lockTimeout)
}

// open is Open with the migrations in files, waiting up to timeout for the
// database's locks.
func open(ctx context.Context, opts Options, files fs.FS, timeout time.Duration) (_ *Store, migrated store.Migrated, err error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	migrated = store.Migrated{Engine: Engine}
	cfg, err := parseURL(opts.URL)
	if err != nil {
		return nil, migrated, err
	}
	migrated.Location = location(&cfg.ConnConfig.Config)
	known, err := latestVersion(files)
	if err != nil {
		return nil, migrated, err
	}

	lockCfg := cfg.ConnConfig.Copy()
	lockCfg.AfterConnect = setLockSession
	lock, err := pgx.ConnectConfig(ctx, lockCfg)
	if err != nil {
		return nil, migrated, fmt.Errorf("connect to the database at %s: %w", migrated.Location, withoutUser(err))
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, closeLock(ctx, lock, "release the database's lock"))
		}
	}()
	if err := checkDatabase(ctx, lock, migrated.Location, logger); err != nil {
		return nil, migrated, err
	}
	if err := openSchema(ctx, lock, cfg.ConnConfig, files, known, timeout, logger, &migrated); err != nil {
		return nil, migrated, err
	}

	cfg.AfterConnect = guardConn(known)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, migrated, fmt.Errorf("connect to the database at %s: %w", migrated.Location, withoutUser(err))
	}
	return &Store{pool: pool, q: postgresdb.New(pool), lock: lock}, migrated, nil
}

// guardConn returns what each pooled connection does first: it takes
// connKey shared, without waiting, so that a migration waits for it even if
// the store loses its own connection, and checks that the schema is still at
// the version the store opened, in case a migration ran after that.
func guardConn(known int64) func(context.Context, *pgx.Conn) error {
	return func(ctx context.Context, conn *pgx.Conn) error {
		var ok bool
		if err := conn.QueryRow(ctx, shared(connKey).try, lockClass, connKey).Scan(&ok); err != nil {
			return fmt.Errorf("lock the database: %w", err)
		}
		if !ok {
			return errors.New("another TrueUp process is migrating the database: try again once it is done")
		}
		version, err := schemaVersion(ctx, conn)
		if err != nil {
			return err
		}
		if version != known {
			return fmt.Errorf("the database's schema changed from version %d to %d while TrueUp had it open: try again", known, version)
		}
		return nil
	}
}

// Close closes the database's connections, and then the one that holds the
// lock that holds off migrations.
func (s *Store) Close() error {
	s.pool.Close()
	return closeLock(context.Background(), s.lock, "the connection that held off migrations broke while the database was open")
}

// closeLock releases the locks the lock connection holds and closes it, even
// once ctx has ended, and says why it couldn't release them with failed.
// Closing alone would release them too, but only once the server notices,
// so a migration that waits for them goes ahead as soon as this returns.
func closeLock(ctx context.Context, conn *pgx.Conn, failed string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	var err error
	if !conn.IsClosed() {
		if _, unlockErr := conn.Exec(ctx, "SELECT pg_catalog.pg_advisory_unlock_all()"); unlockErr != nil {
			err = fmt.Errorf("%s: %w", failed, unlockErr)
		}
	}
	if closeErr := conn.Close(ctx); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("close the database's connection: %w", closeErr))
	}
	// When ctx ended during a query, such as a wait for a lock, pgx closed
	// the connection in the background, asking the server to cancel the
	// query. Waiting for that keeps a process that exits next from leaving
	// the server to wait on for the lock, holding the gate.
	select {
	case <-conn.PgConn().CleanupDone():
	case <-ctx.Done():
	}
	return err
}

// RecordFetch records a fetch of a source in one transaction.
func (s *Store) RecordFetch(ctx context.Context, f store.Fetch) error {
	f, err := f.Prepare()
	if err != nil {
		return fmt.Errorf("record a fetch: %w", err)
	}
	at := pgtype.Timestamptz{Time: f.FetchedAt, Valid: true}
	err = s.inTx(ctx, pgx.TxOptions{}, func(q *postgresdb.Queries) error {
		if err := q.UpsertSource(ctx, postgresdb.UpsertSourceParams{
			Name:      f.Source.Name,
			Kind:      f.Source.Kind,
			Url:       f.Source.URL,
			Ref:       f.Source.Ref,
			FetchedAt: at,
			Error:     pgtype.Text{String: f.Error, Valid: f.Error != ""},
		}); err != nil {
			return err
		}
		if f.Error != "" {
			return nil
		}
		if err := q.UpsertSnapshot(ctx, postgresdb.UpsertSnapshotParams{
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

// replaceLists replaces a snapshot's file list and skipped entries, each
// list in one COPY.
func replaceLists(ctx context.Context, q *postgresdb.Queries, f store.Fetch) error {
	if err := q.DeleteSnapshotFiles(ctx, f.Source.Name); err != nil {
		return err
	}
	if err := q.DeleteSnapshotSkipped(ctx, f.Source.Name); err != nil {
		return err
	}
	if len(f.Files) > 0 {
		files := make([]postgresdb.InsertSnapshotFilesParams, len(f.Files))
		for i, file := range f.Files {
			files[i] = postgresdb.InsertSnapshotFilesParams{Source: f.Source.Name, Path: file.Path, Size: file.Size, Digest: file.Digest}
		}
		if _, err := q.InsertSnapshotFiles(ctx, files); err != nil {
			return fmt.Errorf("record its files: %w", err)
		}
	}
	if len(f.Skipped) > 0 {
		skipped := make([]postgresdb.InsertSnapshotSkippedParams, len(f.Skipped))
		for i, sk := range f.Skipped {
			skipped[i] = postgresdb.InsertSnapshotSkippedParams{Source: f.Source.Name, Seq: int64(i), Path: []byte(sk.Path), Reason: sk.Reason}
		}
		if _, err := q.InsertSnapshotSkipped(ctx, skipped); err != nil {
			return fmt.Errorf("record its skipped entries: %w", err)
		}
	}
	return nil
}

// Sources returns every recorded source, sorted by name.
func (s *Store) Sources(ctx context.Context) ([]store.Source, error) {
	rows, err := s.q.ListSources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the sources: %w", withoutUser(err))
	}
	out := make([]store.Source, 0, len(rows))
	for _, r := range rows {
		fetchedAt, err := readTime(r.FetchedAt)
		if err != nil {
			return nil, fmt.Errorf("source %s: %w", r.Name, err)
		}
		src := store.Source{
			SourceConfig: store.SourceConfig{Name: r.Name, Kind: r.Kind, URL: r.Url, Ref: r.Ref},
			FetchedAt:    fetchedAt,
			Error:        r.Error.String,
		}
		if r.SnapshotVersion.Valid {
			snapAt, err := readTime(r.SnapshotFetchedAt)
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
// transaction that sees the database as it was when the transaction began,
// so that they agree.
func (s *Store) Snapshot(ctx context.Context, source string) (store.Snapshot, error) {
	var snap store.Snapshot
	err := s.inTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(q *postgresdb.Queries) error {
		row, err := q.GetSnapshot(ctx, source)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		at, err := readTime(row.FetchedAt)
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
			snap.Files[i] = store.File(f)
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
// source's rows from the bottom up, having locked the sources' rows first.
func (s *Store) RemoveSources(ctx context.Context, keep []string) ([]string, error) {
	var removed []string
	err := s.inTx(ctx, pgx.TxOptions{}, func(q *postgresdb.Queries) error {
		names, err := q.LockSourcesToRemove(ctx, keep)
		if err != nil {
			return err
		}
		for _, name := range names {
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
func (s *Store) inTx(ctx context.Context, opts pgx.TxOptions, fn func(*postgresdb.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin a transaction: %w", withoutUser(err))
	}
	if err := fn(s.q.WithTx(tx)); err != nil {
		// When ctx has ended, pgx closes the connection instead, which
		// rolls the transaction back as surely, and says so as an error.
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) && ctx.Err() == nil {
			return errors.Join(err, fmt.Errorf("roll back: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// readTime returns a time from the database in UTC. The columns are NOT
// NULL, so a time that isn't valid or finite can come only from outside
// TrueUp.
func readTime(t pgtype.Timestamptz) (time.Time, error) {
	if !t.Valid || t.InfinityModifier != pgtype.Finite {
		return time.Time{}, errors.New("the database holds a time it can't read")
	}
	return t.Time.UTC(), nil
}
