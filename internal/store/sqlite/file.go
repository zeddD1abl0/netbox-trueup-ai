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

// applicationID marks TrueUp's databases in their files' headers:
// 0x54525550, "TRUP". The first migration sets it.
const applicationID = 0x54525550

// openDB opens the database file, which createFile made. It checks that the
// file is TrueUp's database, or a new one, before changing anything in it,
// then puts it in WAL mode, which lasts, and checks that its connections
// have the settings the package needs.
func openDB(ctx context.Context, path string) (_ *sql.DB, err error) {
	connector, err := sqlitedriver.NewConnector(dsn(path))
	if err != nil {
		return nil, fmt.Errorf("connect to the database: %w", err)
	}
	db := sql.OpenDB(connector)
	defer func() {
		if err != nil {
			err = errors.Join(err, db.Close())
		}
	}()
	if err := checkOwner(ctx, db, path); err != nil {
		return nil, err
	}
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode); err != nil {
		return nil, fmt.Errorf("put the database in WAL mode: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return nil, fmt.Errorf("the database can't use WAL mode, only %q: is it on a network file system?", mode)
	}
	var fk int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return nil, fmt.Errorf("read whether the database enforces foreign keys: %w", err)
	}
	if fk != 1 {
		return nil, errors.New("the database doesn't enforce foreign keys")
	}
	return db, nil
}

// checkOwner checks that the database is TrueUp's, by its application ID,
// or new: without the ID, and holding no tables but, perhaps, goose's, as
// when a process stopped before the first migration finished. So TrueUp
// never takes over another application's database that database.url names
// by mistake.
func checkOwner(ctx context.Context, db *sql.DB, path string) error {
	var id int64
	if err := db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return fmt.Errorf("read the database file's header: %w", err)
	}
	if id == applicationID {
		return nil
	}
	if id == 0 {
		var others int
		err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\' AND name <> 'goose_db_version'`).Scan(&others)
		if err != nil {
			return fmt.Errorf("read the database's tables: %w", err)
		}
		if others == 0 {
			return nil
		}
	}
	return fmt.Errorf("%s holds another application's database, not TrueUp's: point database.url at a file of TrueUp's own", path)
}

// dsn returns the URI that opens the database at path. mode=rw is SQLite's
// own option, never to create the file: createFile does, with its mode. The
// options that start with an underscore are the driver's, applied to every
// connection it opens; busy_timeout comes first among the pragmas. WAL mode
// isn't among them, since it lasts in the file once set, and openDB sets it
// only once it knows the file is TrueUp's.
func dsn(path string) string {
	q := url.Values{}
	q.Set("mode", "rw")
	q.Set("_txlock", "immediate")
	// Defensive mode turns off what lets SQL corrupt the file on purpose,
	// such as writable_schema.
	q.Set("_defensive", "1")
	q.Add("_pragma", "busy_timeout("+strconv.FormatInt(busyTimeout.Milliseconds(), 10)+")")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(FULL)")
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/x.db, on Windows, as file:///C:/x.db
	}
	u := url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}
	return u.String()
}
