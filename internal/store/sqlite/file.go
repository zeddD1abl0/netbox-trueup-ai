package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	// The SQLite driver, registered as "sqlite"; openDB uses its connector.
	sqlitedriver "modernc.org/sqlite"
)

// busyTimeout is how long a connection waits for another's lock before
// failing with "database is locked". Writes are short: recording the fetch
// of devicetype-library's 17,000 files takes well under a second.
const busyTimeout = 30 * time.Second

// createFile creates the database's directory, readable by its owner only,
// and its file, readable and writable by its owner only, if they don't
// exist. SQLite would otherwise create the file with mode 0644, less any
// umask. An existing file must be a regular file; if every user can read it,
// a warning says so, as for secret files.
func createFile(ctx context.Context, path string, log *slog.Logger) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// security: path is the operator's, from database.url or data_dir (B5
	// in docs/architecture.md); Open made it absolute and clean.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create the database's directory: %w", err)
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if err := f.Close(); err != nil {
			return fmt.Errorf("create the database file: %w", err)
		}
		log.Info("created the database")
		return nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create the database file: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("check the database file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("the database's path, %s, isn't a file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o004 != 0 {
		log.Warn("every user on this host can read the database file: restrict it with chmod o-r, and the -wal and -shm files beside it")
	}
	return nil
}

// openDB opens the database file, which createFile made, and checks that
// its connections have the settings the package needs.
func openDB(ctx context.Context, path string) (*sql.DB, error) {
	connector, err := sqlitedriver.NewConnector(dsn(path))
	if err != nil {
		return nil, fmt.Errorf("connect to the database: %w", err)
	}
	db := sql.OpenDB(connector)
	if err := checkSettings(ctx, db); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return db, nil
}

// dsn returns the URI that opens the database at path. mode=rw is SQLite's
// own option, never to create the file: createFile does, with its mode. The
// options that start with an underscore are the driver's, applied to every
// connection it opens; busy_timeout comes first among the pragmas.
func dsn(path string) string {
	q := url.Values{}
	q.Set("mode", "rw")
	q.Set("_txlock", "immediate")
	// Defensive mode turns off what lets SQL corrupt the file on purpose,
	// such as writable_schema.
	q.Set("_defensive", "1")
	q.Add("_pragma", "busy_timeout("+strconv.FormatInt(busyTimeout.Milliseconds(), 10)+")")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/x.db, on Windows, as file:///C:/x.db
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}
	return u.String()
}

// checkSettings checks, on one of the pool's connections, what each
// connection's pragmas set. A pragma that SQLite can't apply fails the
// connection, but journal_mode reports the mode it could set instead, which
// on some file systems isn't WAL.
func checkSettings(ctx context.Context, db *sql.DB) error {
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("read the database's journal mode: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("the database can't use WAL mode, only %q: is it on a network file system?", mode)
	}
	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return fmt.Errorf("read whether the database enforces foreign keys: %w", err)
	}
	if fk != 1 {
		return errors.New("the database doesn't enforce foreign keys")
	}
	return nil
}
